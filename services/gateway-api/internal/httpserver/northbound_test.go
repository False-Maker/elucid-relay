package httpserver

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"
	utls "github.com/refraction-networking/utls"
)

func TestEndpointFromPath(t *testing.T) {
	cases := map[string]string{
		"/v1/chat/completions":      "chat",
		"/v1/responses":             "responses",
		"/v1/messages":              "messages",
		"/v1/messages/count_tokens": "messages",
		"/v1/embeddings":            "embeddings",
		"/v1/images/generations":    "images",
		"/v1/audio/transcriptions":  "audio",
		"/v1/realtime/sessions":     "realtime",
		"/v1/rerank":                "rerank",
	}
	for path, expected := range cases {
		if actual := endpointFromPath(path); actual != expected {
			t.Fatalf("endpointFromPath(%q) = %q, expected %q", path, actual, expected)
		}
	}
}

func TestClaudeCodeSidecarEndpoint(t *testing.T) {
	cases := map[string]string{
		"/v1/files":                                 "claude_files",
		"/v1/files/file_123/content":                "claude_files",
		"/v1/mcp_servers":                           "claude_mcp_servers",
		"/v1/sessions/session_123/events":           "claude_sessions",
		"/v1/sessions/ws/session_123/subscribe":     "claude_sessions_ws",
		"/v1/code/sessions/cse_123/teleport-events": "claude_code_sessions",
		"/v1/session_ingress/session/session_123":   "claude_session_ingress",
		"/v1/environments/bridge":                   "claude_environments",
		"/v1/environment_providers":                 "claude_environment_providers",
		"/v1/environment_providers/cloud/create":    "claude_environment_providers",
		"/api/oauth/profile":                        "claude_oauth",
		"/api/oauth/files/file_uuid/content":        "claude_oauth",
		"/api/oauth/file_upload":                    "claude_oauth",
		"/api/claude_cli_profile":                   "claude_oauth",
		"/v1/unknown":                               "claude_sidecar",
	}
	for path, expected := range cases {
		if actual := claudeCodeSidecarEndpoint(path); actual != expected {
			t.Fatalf("claudeCodeSidecarEndpoint(%q) = %q, expected %q", path, actual, expected)
		}
		if expected != "claude_sidecar" && !isClaudeCodeOfficialSidecarPath(path) {
			t.Fatalf("isClaudeCodeOfficialSidecarPath(%q) = false", path)
		}
	}
}

func TestCalculateActualCost(t *testing.T) {
	cost := calculateActualCost(modelInfo{
		InputUSDPer1K:  0.01,
		OutputUSDPer1K: 0.02,
		RequestUSD:     0.001,
		MinChargeUSD:   0.005,
	}, usageCounts{InputTokens: 100, OutputTokens: 100}, meteringMetrics{RequestCount: 1})

	if cost != 0.005 {
		t.Fatalf("cost = %v, expected min charge 0.005", cost)
	}
}

func TestCalculateActualCostWithOperationalPricingOverrides(t *testing.T) {
	cost := calculateActualCost(modelInfo{
		InputUSDPer1K:      0.01,
		OutputUSDPer1K:     0.02,
		RequestUSD:         0.001,
		CacheReadUSDPer1K:  0.003,
		CacheWriteUSDPer1K: 0.004,
		ImageUSDPerUnit:    0.1,
		AudioUSDPerSecond:  0.01,
	}, usageCounts{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 2000, CacheWriteTokens: 500}, meteringMetrics{RequestCount: 2, ImageCount: 2, AudioSeconds: 3})

	if diff := cost - 0.26; diff > 0.0000001 || diff < -0.0000001 {
		t.Fatalf("cost = %v, expected 0.26", cost)
	}
}

func TestCalculateActualCostWithBillingExpression(t *testing.T) {
	cost := calculateActualCost(modelInfo{
		BillingMode: "tiered_expr",
		BillingExpr: "max(request_count * 0.01, input_tokens / 1000 * 0.02 + cache_read_tokens / 1000 * 0.003 + images * 0.1)",
	}, usageCounts{InputTokens: 2000, CacheReadTokens: 1000}, meteringMetrics{RequestCount: 1, ImageCount: 1})

	if diff := cost - 0.143; diff > 0.0000001 || diff < -0.0000001 {
		t.Fatalf("cost = %v, expected 0.143", cost)
	}
}

func TestParseUsageIncludesCacheTokens(t *testing.T) {
	usage := parseUsage([]byte(`{"usage":{"input_tokens":1200,"output_tokens":300,"cache_creation_input_tokens":400,"input_tokens_details":{"cached_tokens":500}}}`))
	if usage.InputTokens != 1200 || usage.OutputTokens != 300 || usage.CacheWriteTokens != 400 || usage.CacheReadTokens != 500 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestActualCostForRoutingMode(t *testing.T) {
	if got := actualCostForRoutingMode("byo", 12.34); got != 0 {
		t.Fatalf("BYO cost = %v, expected zero", got)
	}
	if got := actualCostForRoutingMode("pool", 12.34); got != 12.34 {
		t.Fatalf("pool cost = %v, expected original cost", got)
	}
}

func TestValidateModelScope(t *testing.T) {
	if err := validateModelScope(`[]`, "gpt-test", "gpt-test"); err != nil {
		t.Fatalf("empty scope should allow model: %v", err)
	}
	if err := validateModelScope(`["gpt-test"]`, "gpt-test", "gpt-test"); err != nil {
		t.Fatalf("matching scope should allow model: %v", err)
	}
	if err := validateModelScope(`["gpt-canonical"]`, "alias", "gpt-canonical"); err != nil {
		t.Fatalf("canonical scope should allow alias request: %v", err)
	}
	if err := validateModelScope(`["alias"]`, "gpt-canonical", "gpt-canonical", "alias"); err != nil {
		t.Fatalf("alias scope should allow canonical request: %v", err)
	}
	if err := validateModelScope(`["other"]`, "gpt-test", "gpt-test"); err == nil {
		t.Fatal("non-matching scope should reject model")
	}
}

func TestNorthboundModelItemIncludesMetadata(t *testing.T) {
	item := northboundModelItem(northboundModelRecord{
		ModelName:            "gpt-test",
		DisplayName:          "GPT Test",
		ProviderHint:         "openai",
		EndpointCapabilities: `["chat","responses"]`,
		Metadata:             `{"family":"gpt","quality":"test"}`,
		Aliases:              `["alias-a","alias-b"]`,
		Created:              1710000000,
	})

	body, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if decoded["id"] != "gpt-test" || decoded["object"] != "model" || decoded["owned_by"] != "elucid-relay" {
		t.Fatalf("unexpected model identity fields: %#v", decoded)
	}
	if decoded["display_name"] != "GPT Test" || decoded["provider_hint"] != "openai" {
		t.Fatalf("unexpected model metadata fields: %#v", decoded)
	}
	aliases, ok := decoded["aliases"].([]any)
	if !ok || len(aliases) != 2 || aliases[0] != "alias-a" || aliases[1] != "alias-b" {
		t.Fatalf("aliases = %#v", decoded["aliases"])
	}
	capabilities, ok := decoded["endpoint_capabilities"].([]any)
	if !ok || len(capabilities) != 2 || capabilities[0] != "chat" || capabilities[1] != "responses" {
		t.Fatalf("endpoint_capabilities = %#v", decoded["endpoint_capabilities"])
	}
	metadata, ok := decoded["metadata"].(map[string]any)
	if !ok || metadata["family"] != "gpt" || metadata["quality"] != "test" {
		t.Fatalf("metadata = %#v", decoded["metadata"])
	}
}

func TestRouteSelectionSeedUsesAffinityThenRequestID(t *testing.T) {
	ctx := context.WithValue(context.Background(), requestIDKey, "req-1")
	if got := routeSelectionSeed(ctx, "gpt-test", "chat", "pool", " session-1 ", 1); got != "session-1:1" {
		t.Fatalf("affinity seed = %q", got)
	}
	if got := routeSelectionSeed(ctx, "gpt-test", "chat", "pool", "", 1); got != "req-1:1" {
		t.Fatalf("request id seed = %q", got)
	}
	if got := routeSelectionSeed(context.Background(), "gpt-test", "chat", "pool", "", 0); got != "gpt-test:chat:pool:0" {
		t.Fatalf("fallback seed = %q", got)
	}
}

func TestReadLimitedRequestBodyTooLargeUses413(t *testing.T) {
	_, err := readLimitedRequestBody(strings.NewReader("abcd"), 3)
	var appErr appError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected appError, got %T", err)
	}
	if appErr.status != http.StatusRequestEntityTooLarge || appErr.code != "request_body_too_large" {
		t.Fatalf("unexpected error: %#v", appErr)
	}
}

func TestValidateNorthboundClientRequest(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		body     string
		wantErr  string
	}{
		{
			name:     "stream must be boolean",
			endpoint: "chat",
			body:     `{"model":"gpt-test","stream":"true"}`,
			wantErr:  "stream must be a boolean.",
		},
		{
			name:     "previous response id rejects message id",
			endpoint: "responses",
			body:     `{"model":"gpt-test","previous_response_id":"msg_123"}`,
			wantErr:  "previous_response_id must be a response id, not a message id.",
		},
		{
			name:     "function output requires call id",
			endpoint: "responses",
			body:     `{"model":"gpt-test","input":[{"type":"function_call_output","output":"ok"}]}`,
			wantErr:  "function_call_output items require call_id.",
		},
		{
			name:     "function output requires local context without previous response id",
			endpoint: "responses",
			body:     `{"model":"gpt-test","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
			wantErr:  "function_call_output items require matching item_reference when previous_response_id is absent.",
		},
		{
			name:     "function output accepts matching item reference",
			endpoint: "responses",
			body:     `{"model":"gpt-test","input":[{"type":"item_reference","id":"call_1"},{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
		},
		{
			name:     "valid responses continuation shape",
			endpoint: "responses",
			body:     `{"model":"gpt-test","stream":true,"previous_response_id":"resp_123","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNorthboundClientRequest(tc.endpoint, []byte(tc.body), "application/json")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validate returned error: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("validate error = %v, expected %q", err, tc.wantErr)
			}
		})
	}
}

func TestRouteAffinityKeyFromClientLikeFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","previous_response_id":"resp_123"}`))
	req.Header.Set("Content-Type", "application/json")
	if got := routeAffinityKeyFromRequest(req, []byte(`{"model":"gpt-test","previous_response_id":"resp_123"}`), "application/json"); got != "resp_123" {
		t.Fatalf("previous_response_id affinity = %q", got)
	}

	headerReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	headerReq.Header.Set("X-Gemini-User-Prompt-Id", "prompt-123")
	if got := routeAffinityKeyFromRequest(headerReq, nil, "application/json"); got != "prompt-123" {
		t.Fatalf("client header affinity = %q", got)
	}
}

func TestAffinityRuleKeyFromRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses?conversation=ignored", nil)
	req.Header.Set("Content-Type", "application/json")
	rule := affinityRuleSettings{
		PathRegex:        []string{`^/v1/responses$`},
		JSONPaths:        []string{"metadata.thread_id"},
		IncludeModelName: true,
		IncludeEndpoint:  true,
		TTLSeconds:       60,
	}

	got := affinityRuleKeyFromRequest(rule, req, []byte(`{"metadata":{"thread_id":"thread-1"}}`), "application/json", "gpt-test", "responses")
	if got != "gpt-test:responses:thread-1" {
		t.Fatalf("affinity rule key = %q", got)
	}
}

func TestParseReverseProxySettingsIncludesAffinityRules(t *testing.T) {
	settings := parseReverseProxySettings(`{"stream_keep_alive_seconds":15,"affinity_rules":[{"name":"prompt","header_keys":["X-Prompt-ID"],"ttl_seconds":90}],"anthropic_beta_policy":{"mode":"pass"}}`)
	if len(settings.AffinityRules) != 1 {
		t.Fatalf("affinity rule count = %d", len(settings.AffinityRules))
	}
	if settings.AffinityRules[0].Name != "prompt" || settings.AffinityRules[0].HeaderKeys[0] != "X-Prompt-ID" || settings.AffinityRules[0].TTLSeconds != 90 {
		t.Fatalf("affinity rule = %#v", settings.AffinityRules[0])
	}
	if settings.StreamKeepAliveSeconds != 15 {
		t.Fatalf("stream keep alive seconds = %d", settings.StreamKeepAliveSeconds)
	}
}

func TestDigestAffinityKeyIgnoresVolatileSamplingFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	bodyA := []byte(`{"model":"gpt-test","stream":true,"temperature":0.2,"messages":[{"role":"user","content":"hi"}]}`)
	bodyB := []byte(`{"temperature":1,"stream":false,"messages":[{"content":"hi","role":"user"}],"model":"gpt-test"}`)
	keyA := routeDigestAffinityKeyFromRequest(req, "chat", bodyA, "application/json")
	keyB := routeDigestAffinityKeyFromRequest(req, "chat", bodyB, "application/json")
	if keyA == "" || keyA != keyB {
		t.Fatalf("digest affinity keys = %q / %q", keyA, keyB)
	}
}

func TestReplayGuardTrimsLongMessagesPreservingToolBoundary(t *testing.T) {
	messages := make([]any, 0, 30)
	for i := 0; i < 30; i++ {
		messages = append(messages, map[string]any{"role": "user", "content": "msg-" + strconv.Itoa(i)})
	}
	messages[5] = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-1", "function": map[string]any{"name": "lookup"}}}}
	messages[6] = map[string]any{"role": "tool", "tool_call_id": "call-1", "content": "result"}
	body, err := json.Marshal(map[string]any{"model": "gpt-test", "messages": messages})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	rewritten, changed := applyReplayGuard("chat", body, "application/json", 24)
	if !changed {
		t.Fatal("expected replay guard to trim body")
	}
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("unmarshal rewritten: %v", err)
	}
	trimmed, ok := arrayValue(payload["messages"])
	if !ok {
		t.Fatalf("messages = %#v", payload["messages"])
	}
	if len(trimmed) != 25 {
		t.Fatalf("trimmed messages len = %d", len(trimmed))
	}
	first, _ := trimmed[0].(map[string]any)
	if metadataText(first["role"]) != "assistant" {
		t.Fatalf("first trimmed message = %#v", first)
	}
}

func TestUpstreamMaxAttemptsFromRoute(t *testing.T) {
	if got := upstreamMaxAttemptsFromRoute(routeInfo{}); got != defaultUpstreamMaxAttempts {
		t.Fatalf("default attempts = %d", got)
	}
	if got := upstreamMaxAttemptsFromRoute(routeInfo{ChannelMeta: `{"retry":{"max_attempts":3}}`}); got != 3 {
		t.Fatalf("channel retry attempts = %d", got)
	}
	if got := upstreamMaxAttemptsFromRoute(routeInfo{AccountMeta: `{"retry":{"max_attempts":"1"}}`}); got != 1 {
		t.Fatalf("account retry attempts = %d", got)
	}
	if got := upstreamMaxAttemptsFromRoute(routeInfo{ChannelMeta: `{"retry":{"max_attempts":99}}`}); got != maxConfiguredUpstreamAttempts {
		t.Fatalf("clamped retry attempts = %d", got)
	}
	if got := upstreamMaxAttemptsFromRoute(routeInfo{ChannelMeta: `{"retry":{"max_attempts":"invalid"}}`}); got != defaultUpstreamMaxAttempts {
		t.Fatalf("invalid retry attempts = %d", got)
	}
}

func TestAppendFailedRouteAccountID(t *testing.T) {
	ids := appendFailedRouteAccountID(nil, routeInfo{AccountID: "acc-1"})
	ids = appendFailedRouteAccountID(ids, routeInfo{AccountID: "acc-1"})
	ids = appendFailedRouteAccountID(ids, routeInfo{AccountID: "acc-2"})
	if want := []string{"acc-1", "acc-2"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("excluded account ids = %#v, want %#v", ids, want)
	}
}

func TestCircuitBreakerFailureThresholdFromRoute(t *testing.T) {
	if got := circuitBreakerFailureThreshold(routeInfo{}); got != defaultCircuitBreakerFailureThreshold {
		t.Fatalf("default threshold = %d", got)
	}
	if got := circuitBreakerFailureThreshold(routeInfo{ChannelMeta: `{"circuit_breaker":{"failure_threshold":5}}`}); got != 5 {
		t.Fatalf("channel threshold = %d", got)
	}
	if got := circuitBreakerFailureThreshold(routeInfo{AccountMeta: `{"circuit_breaker":{"failure_threshold":"1"}}`}); got != 1 {
		t.Fatalf("account threshold = %d", got)
	}
	if got := circuitBreakerFailureThreshold(routeInfo{ChannelMeta: `{"circuit_breaker":{"failure_threshold":99}}`}); got != maxCircuitBreakerFailureThreshold {
		t.Fatalf("clamped threshold = %d", got)
	}
	if got := circuitBreakerFailureThreshold(routeInfo{ChannelMeta: `{"circuit_breaker":{"failure_threshold":"invalid"}}`}); got != defaultCircuitBreakerFailureThreshold {
		t.Fatalf("invalid threshold = %d", got)
	}
}

func TestCircuitBreakerOpenDurationFromRoute(t *testing.T) {
	if got := circuitBreakerOpenDuration(routeInfo{}, http.StatusTooManyRequests); got != 10*time.Minute {
		t.Fatalf("429 duration = %s", got)
	}
	if got := circuitBreakerOpenDuration(routeInfo{}, http.StatusBadGateway); got != 2*time.Minute {
		t.Fatalf("5xx duration = %s", got)
	}
	if got := circuitBreakerOpenDuration(routeInfo{}, http.StatusBadRequest); got != 30*time.Second {
		t.Fatalf("default duration = %s", got)
	}
	if got := circuitBreakerOpenDuration(routeInfo{ChannelMeta: `{"circuit_breaker":{"open_seconds":1}}`}, http.StatusBadGateway); got != time.Duration(minCircuitBreakerOpenSeconds)*time.Second {
		t.Fatalf("min-clamped duration = %s", got)
	}
	if got := circuitBreakerOpenDuration(routeInfo{ChannelMeta: `{"circuit_breaker":{"open_seconds":99999}}`}, http.StatusBadGateway); got != time.Duration(maxCircuitBreakerOpenSeconds)*time.Second {
		t.Fatalf("max-clamped duration = %s", got)
	}
	if got := circuitBreakerOpenDuration(routeInfo{AccountMeta: `{"circuit_breaker":{"open_seconds":"45"}}`}, http.StatusBadGateway); got != 45*time.Second {
		t.Fatalf("account duration = %s", got)
	}
}

func TestClientIPIgnoresForwardedForWithoutTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	req.Header.Set("X-Forwarded-For", "198.51.100.20")

	got := (&Server{}).clientIPForRequest(req)
	if got != "203.0.113.10" {
		t.Fatalf("clientIPForRequest = %q, expected remote address", got)
	}
}

func TestClientIPUsesForwardedForFromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "10.1.2.3:12345"
	req.Header.Set("X-Forwarded-For", "198.51.100.20, 10.1.2.3")
	server := &Server{trustedNets: trustedProxyNetworks("10.0.0.0/8")}

	got := server.clientIPForRequest(req)
	if got != "198.51.100.20" {
		t.Fatalf("clientIPForRequest = %q, expected forwarded client IP", got)
	}
}

func TestReadLimitedRequestBodyRejectsTooLarge(t *testing.T) {
	body, err := readLimitedRequestBody(strings.NewReader("abcde"), 5)
	if err != nil {
		t.Fatalf("readLimitedRequestBody returned error: %v", err)
	}
	if string(body) != "abcde" {
		t.Fatalf("body = %q", body)
	}

	_, err = readLimitedRequestBody(strings.NewReader("abcdef"), 5)
	var appErr appError
	if !errors.As(err, &appErr) || appErr.status != http.StatusRequestEntityTooLarge || appErr.code != "request_body_too_large" {
		t.Fatalf("readLimitedRequestBody error = %v, expected request_body_too_large appError", err)
	}
}

func TestModelFromBodyJSON(t *testing.T) {
	model, err := modelFromBody([]byte(`{"model":"gpt-test"}`), "application/json")
	if err != nil {
		t.Fatalf("modelFromBody returned error: %v", err)
	}
	if model != "gpt-test" {
		t.Fatalf("model = %q", model)
	}
}

func TestModelFromBodyMultipart(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "whisper-test"); err != nil {
		t.Fatalf("WriteField returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	model, err := modelFromBody(body.Bytes(), writer.FormDataContentType())
	if err != nil {
		t.Fatalf("modelFromBody returned error: %v", err)
	}
	if model != "whisper-test" {
		t.Fatalf("model = %q", model)
	}
}

func TestIsStreamingRequest(t *testing.T) {
	if !isStreamingRequest([]byte(`{"stream":true}`), "application/json", "") {
		t.Fatal("expected JSON stream flag to enable streaming")
	}
	if !isStreamingRequest([]byte(`{}`), "application/json", "text/event-stream") {
		t.Fatal("expected event-stream accept header to enable streaming")
	}
	if isStreamingRequest([]byte(`{"stream":false}`), "application/json", "") {
		t.Fatal("expected non-streaming JSON request")
	}
}

func TestRouteAffinityKeyFromRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses?session_id=query-session", nil)
	req.Header.Set("X-Elucid-Relay-Session", " header-session ")
	if got := routeAffinityKeyFromRequest(req, nil, "application/json"); got != "header-session" {
		t.Fatalf("route affinity header = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := []byte(`{"metadata":{"conversation_id":"json-session"}}`)
	if got := routeAffinityKeyFromRequest(req, body, "application/json"); got != "json-session" {
		t.Fatalf("route affinity JSON = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses?thread_id=query-session", nil)
	if got := routeAffinityKeyFromRequest(req, nil, "application/json"); got != "query-session" {
		t.Fatalf("route affinity query = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("X-Elucid-Relay-Session", "bad\nsession")
	if got := routeAffinityKeyFromRequest(req, nil, "application/json"); got != "" {
		t.Fatalf("control-character affinity key should be ignored, got %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("X-Elucid-Relay-Session", string(bytes.Repeat([]byte("a"), 513)))
	if got := routeAffinityKeyFromRequest(req, nil, "application/json"); got != "" {
		t.Fatalf("oversized affinity key should be ignored, got %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Session_id", "codex-session")
	if got := routeAffinityKeyFromRequest(req, nil, "application/json"); got != "codex-session" {
		t.Fatalf("route affinity Session_id = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body = []byte(`{"metadata":{"user_id":{"session_id":"metadata-session"}}}`)
	if got := routeAffinityKeyFromRequest(req, body, "application/json"); got != "metadata-session" {
		t.Fatalf("route affinity metadata user session = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body = []byte(`{"instructions":"be concise","input":[{"role":"user","content":"hello"}]}`)
	first := routeAffinityKeyFromRequest(req, body, "application/json")
	second := routeAffinityKeyFromRequest(req, body, "application/json")
	if first == "" || first != second || !strings.HasPrefix(first, "responses:") {
		t.Fatalf("responses digest affinity = %q / %q", first, second)
	}
}

func TestRouteTagsFromRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?route_profile=long-context", nil)
	req.Header.Set("X-Elucid-Relay-Route-Tags", "think, Background")
	body := []byte(`{"metadata":{"route_tags":["vision","think"],"route_profile":"premium"},"route_tag":"fast"}`)

	got := routeTagsFromRequest(req, body, "application/json")
	want := []string{"think", "background", "long-context", "fast"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("route tags = %#v, want %#v", got, want)
	}
}

func TestRouteTagsRejectUnsafeValues(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?route_tag=bad%20tag", nil)
	req.Header.Set("X-Elucid-Relay-Route-Tag", "safe_tag")
	req.Header.Set("X-Elucid-Relay-Route-Profile", "bad\nprofile")
	body := []byte(`{"metadata":{"route_tags":["ok.profile","bad/tag","another:ok"]}}`)

	got := routeTagsFromRequest(req, body, "application/json")
	want := []string{"safe_tag", "ok.profile", "another:ok"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("route tags = %#v, want %#v", got, want)
	}
}

func TestRouteMetadataSupportsClientProfile(t *testing.T) {
	route := routeInfo{
		TokenMetadata: map[string]any{
			"client_profile": map[string]any{
				"github": map[string]any{
					"client_version": "9.9.9",
				},
			},
			"gemini": map[string]any{
				"client_profile": map[string]any{
					"user_agent": "GeminiCLI/custom",
				},
			},
		},
		AccountMeta: `{"official_client":{"claude":{"profile":{"client_version":"2.2.2"}}}}`,
	}

	if got := routeMetadataString(route, "github", "client_version"); got != "9.9.9" {
		t.Fatalf("github client_profile version = %q", got)
	}
	if got := routeMetadataString(route, "gemini", "user_agent"); got != "GeminiCLI/custom" {
		t.Fatalf("gemini nested client_profile user agent = %q", got)
	}
	if got := routeMetadataString(route, "claude", "client_version"); got != "2.2.2" {
		t.Fatalf("official client profile version = %q", got)
	}
}

func TestDecodeNorthboundRequestBodySupportsCodexZstd(t *testing.T) {
	var compressed bytes.Buffer
	writer, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("zstd.NewWriter returned error: %v", err)
	}
	if _, err := writer.Write([]byte(`{"model":"gpt-test"}`)); err != nil {
		t.Fatalf("zstd Write returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zstd Close returned error: %v", err)
	}

	decoded, err := decodeNorthboundRequestBody(compressed.Bytes(), "zstd")
	if err != nil {
		t.Fatalf("decodeNorthboundRequestBody returned error: %v", err)
	}
	if string(decoded) != `{"model":"gpt-test"}` {
		t.Fatalf("decoded = %q", string(decoded))
	}
}

func TestDecodeNorthboundRequestBodySupportsGzip(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(`{"model":"gpt-test"}`)); err != nil {
		t.Fatalf("gzip Write returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip Close returned error: %v", err)
	}

	decoded, err := decodeNorthboundRequestBody(compressed.Bytes(), "gzip")
	if err != nil {
		t.Fatalf("decodeNorthboundRequestBody returned error: %v", err)
	}
	if string(decoded) != `{"model":"gpt-test"}` {
		t.Fatalf("decoded = %q", string(decoded))
	}
}

func TestDecodeNorthboundRequestBodySupportsStackedEncodings(t *testing.T) {
	payload := []byte(`{"model":"gpt-test"}`)
	var zstdCompressed bytes.Buffer
	zstdWriter, err := zstd.NewWriter(&zstdCompressed)
	if err != nil {
		t.Fatalf("zstd.NewWriter returned error: %v", err)
	}
	if _, err := zstdWriter.Write(payload); err != nil {
		t.Fatalf("zstd write returned error: %v", err)
	}
	if err := zstdWriter.Close(); err != nil {
		t.Fatalf("zstd close returned error: %v", err)
	}
	var stacked bytes.Buffer
	gzipWriter := gzip.NewWriter(&stacked)
	if _, err := gzipWriter.Write(zstdCompressed.Bytes()); err != nil {
		t.Fatalf("gzip write returned error: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("gzip close returned error: %v", err)
	}

	decoded, err := decodeNorthboundRequestBody(stacked.Bytes(), "zstd, gzip")
	if err != nil {
		t.Fatalf("decodeNorthboundRequestBody returned error: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded = %q", string(decoded))
	}
}

func TestDecodeNorthboundRequestBodySupportsBrotli(t *testing.T) {
	var compressed bytes.Buffer
	writer := brotli.NewWriter(&compressed)
	if _, err := writer.Write([]byte(`{"model":"gpt-test"}`)); err != nil {
		t.Fatalf("brotli write returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("brotli close returned error: %v", err)
	}

	decoded, err := decodeNorthboundRequestBody(compressed.Bytes(), "br")
	if err != nil {
		t.Fatalf("decodeNorthboundRequestBody returned error: %v", err)
	}
	if string(decoded) != `{"model":"gpt-test"}` {
		t.Fatalf("decoded = %q", string(decoded))
	}
}

func TestDecodeNorthboundRequestBodyAcceptsRawJSONWhenEncodingHeaderIsStale(t *testing.T) {
	body := []byte(`{"model":"gpt-test"}`)
	decoded, err := decodeNorthboundRequestBody(body, "gzip")
	if err != nil {
		t.Fatalf("decodeNorthboundRequestBody returned error: %v", err)
	}
	if !bytes.Equal(decoded, body) {
		t.Fatalf("decoded = %q", string(decoded))
	}
}

func TestRewriteRequestModel(t *testing.T) {
	body := rewriteRequestModel([]byte(`{"model":"public-alias","messages":[]}`), "application/json", "upstream-model")
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("rewritten body is not JSON: %v", err)
	}
	if payload["model"] != "upstream-model" {
		t.Fatalf("model = %v, expected upstream-model", payload["model"])
	}
}

func TestRewriteRequestModelSkipsMultipart(t *testing.T) {
	body := []byte("raw")
	rewritten := rewriteRequestModel(body, "multipart/form-data; boundary=x", "upstream-model")
	if !bytes.Equal(rewritten, body) {
		t.Fatal("multipart body should not be rewritten")
	}
}

func TestUpstreamModelForRecord(t *testing.T) {
	model := modelInfo{ModelName: "canonical"}
	if got := upstreamModelForRecord(routeInfo{UpstreamModel: "upstream"}, model); got != "upstream" {
		t.Fatalf("got %q", got)
	}
	if got := upstreamModelForRecord(routeInfo{}, model); got != "canonical" {
		t.Fatalf("got %q", got)
	}
}

func TestNewUpstreamWebSocketDialBuildsSafeHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://relay.local/v1/realtime?model=gpt-test&session_id=s1", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "client-key")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("OpenAI-Beta", "realtime=v1")
	req.Header.Set("X-Trace-Id", "trace-1")
	req.Header.Set("X-Elucid-Relay-Session", "relay-session")

	route := routeInfo{
		BaseURL:      "https://upstream.example/base",
		ProviderType: "openai",
		APIKey:       "upsec",
		ChannelMeta: `{
			"request_headers": {
				"pass": ["*"],
				"set": {
					"Connection": "Upgrade",
					"Sec-WebSocket-Key": "operator-key"
				}
			}
		}`,
	}

	upstreamURL, headers, dialer, err := (&Server{}).newUpstreamWebSocketDial(req, route)
	if err != nil {
		t.Fatalf("newUpstreamWebSocketDial returned error: %v", err)
	}
	if upstreamURL != "wss://upstream.example/base/v1/realtime?model=gpt-test&session_id=s1" {
		t.Fatalf("upstreamURL = %q", upstreamURL)
	}
	if got := headers.Get("Authorization"); got != "Bearer upsec" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := headers.Get("OpenAI-Beta"); got != "realtime=v1" {
		t.Fatalf("OpenAI-Beta = %q", got)
	}
	if got := headers.Get("X-Trace-Id"); got != "trace-1" {
		t.Fatalf("X-Trace-Id = %q", got)
	}
	for _, key := range []string{"Connection", "Upgrade", "Sec-WebSocket-Key", "Sec-WebSocket-Version", "X-Elucid-Relay-Session"} {
		if got := headers.Get(key); got != "" {
			t.Fatalf("%s leaked as %q", key, got)
		}
	}
	if dialer == nil || dialer.HandshakeTimeout != 120*time.Second {
		t.Fatalf("unexpected dialer timeout: %#v", dialer)
	}
}

func TestAnthropicAdapterAppliesClaudeCodeSessionsWebSocketHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://relay.local/v1/sessions/ws/session_123/subscribe?organization_uuid=org-123", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "client-key")
	req.Header.Set("Sec-WebSocket-Version", "13")
	route := claudeCodeTestRoute()

	upstreamURL, headers, _, err := anthropicAdapter{}.PrepareWebSocket(req, route)
	if err != nil {
		t.Fatalf("PrepareWebSocket returned error: %v", err)
	}
	if upstreamURL != "wss://api.anthropic.com/v1/sessions/ws/session_123/subscribe?organization_uuid=org-123" {
		t.Fatalf("upstreamURL = %q", upstreamURL)
	}
	if got := headers.Get("Authorization"); got != "Bearer claudetok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := headers.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q", got)
	}
	for _, key := range []string{"Accept", "Accept-Encoding", "Anthropic-Beta", "Content-Type", "User-Agent", "x-app", "x-stainless-lang"} {
		if got := headers.Get(key); got != "" {
			t.Fatalf("%s should not be synthesized for sessions websocket, got %q", key, got)
		}
	}
}

func TestCodexOfficialRouteAppliesClientHeadersAndBodyMetadata(t *testing.T) {
	body := []byte(`{"model":"gpt-public","store":true,"input":[{"type":"reasoning","id":"rs_1"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Elucid-Relay-Session", "codex-session")
	req.Header.Set("X-Codex-Turn-State", "turn-state")
	route := routeInfo{
		BaseURL:       "https://chatgpt.test/backend-api/codex",
		ProviderType:  "openai",
		AuthMode:      "codex_cli",
		TokenProvider: "openai_codex",
		AuthScheme:    "bearer",
		AccountID:     "relay-account-1",
		APIKey:        "cxtok",
		UpstreamModel: "gpt-upstream",
		TokenMetadata: map[string]any{
			"account_id":                 "chatgpt-account-1",
			"chatgpt_account_is_fedramp": true,
			"originator":                 "codex_cli_rs",
			"client_version":             "0.0.0",
			"base_instructions":          "First line\nSecond line",
			"user_agent":                 "codex_cli_rs/0.0.0 (Linux 6.0; x86_64) test-terminal",
			"installation_id":            "install-1",
			"include_timing_metrics":     true,
			"openai_organization":        "org-1",
			"openai_project":             "proj-1",
		},
	}

	upstream, cancel, err := openaiCompatibleAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.URL.String(); got != "https://chatgpt.test/backend-api/codex/responses" {
		t.Fatalf("upstream URL = %q", got)
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer cxtok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("ChatGPT-Account-ID"); got != "chatgpt-account-1" {
		t.Fatalf("ChatGPT-Account-ID = %q", got)
	}
	if got := upstream.Header.Get("X-OpenAI-Fedramp"); got != "true" {
		t.Fatalf("X-OpenAI-Fedramp = %q", got)
	}
	if got := upstream.Header.Get("originator"); got != "codex_cli_rs" {
		t.Fatalf("originator = %q", got)
	}
	if got := upstream.Header.Get("version"); got != "0.0.0" {
		t.Fatalf("version = %q", got)
	}
	if got := upstream.Header.Get("User-Agent"); got != "codex_cli_rs/0.0.0 (Linux 6.0; x86_64) test-terminal" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := upstream.Header.Get("session_id"); got != "codex-session" {
		t.Fatalf("session_id = %q", got)
	}
	if got := upstream.Header.Get("x-client-request-id"); got != "codex-session" {
		t.Fatalf("x-client-request-id = %q", got)
	}
	if got := upstream.Header.Get("X-Request-Id"); got != "" {
		t.Fatalf("X-Request-Id should be absent for Codex official routes, got %q", got)
	}
	if got := upstream.Header.Get("x-codex-installation-id"); got != "" {
		t.Fatalf("x-codex-installation-id should be absent for /responses, got %q", got)
	}
	if got := upstream.Header.Get("x-codex-window-id"); got != "codex-session:0" {
		t.Fatalf("x-codex-window-id = %q", got)
	}
	if got := upstream.Header.Get("x-codex-turn-metadata"); !strings.Contains(got, `"session_id":"codex-session"`) {
		t.Fatalf("x-codex-turn-metadata = %q", got)
	}
	if got := upstream.Header.Get("x-codex-turn-metadata"); !strings.Contains(got, `"turn_id":""`) {
		t.Fatalf("x-codex-turn-metadata should not default turn_id from relay request id: %q", got)
	}
	if got := upstream.Header.Get("x-codex-turn-state"); got != "turn-state" {
		t.Fatalf("x-codex-turn-state = %q", got)
	}
	if got := upstream.Header.Get("x-responsesapi-include-timing-metrics"); got != "true" {
		t.Fatalf("x-responsesapi-include-timing-metrics = %q", got)
	}
	if got := upstream.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}
	if got := upstream.Header.Get("OpenAI-Organization"); got != "org-1" {
		t.Fatalf("OpenAI-Organization = %q", got)
	}
	if got := upstream.Header.Get("OpenAI-Project"); got != "proj-1" {
		t.Fatalf("OpenAI-Project = %q", got)
	}
	if got := upstream.Header.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	if got := upstream.Header.Get("Accept-Encoding"); got != "" {
		t.Fatalf("Accept-Encoding should be absent for Codex official routes, got %q", got)
	}

	outboundBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	reader, err := zstd.NewReader(bytes.NewReader(outboundBody))
	if err != nil {
		t.Fatalf("zstd.NewReader returned error: %v", err)
	}
	defer reader.Close()
	outboundBody, err = io.ReadAll(reader)
	if err != nil {
		t.Fatalf("zstd ReadAll returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(outboundBody, &payload); err != nil {
		t.Fatalf("upstream body is not JSON: %v", err)
	}
	if got := payload["model"]; got != "gpt-upstream" {
		t.Fatalf("model = %v", got)
	}
	if got := payload["stream"]; got != true {
		t.Fatalf("stream = %v", got)
	}
	if got := payload["instructions"]; got != "First line\nSecond line" {
		t.Fatalf("instructions = %v", got)
	}
	reasoning, ok := payload["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning = %#v", payload["reasoning"])
	}
	if got := reasoning["effort"]; got != "low" {
		t.Fatalf("reasoning.effort = %v", got)
	}
	if got := reasoning["summary"]; got != "auto" {
		t.Fatalf("reasoning.summary = %v", got)
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != 0 {
		t.Fatalf("tools = %#v", payload["tools"])
	}
	if got := payload["store"]; got != false {
		t.Fatalf("store = %v", got)
	}
	input, ok := payload["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input = %#v", payload["input"])
	}
	firstInput, _ := input[0].(map[string]any)
	if got := firstInput["type"]; got != "message" {
		t.Fatalf("reasoning input item should be removed, got %#v", input)
	}
	if got := payload["parallel_tool_calls"]; got != true {
		t.Fatalf("parallel_tool_calls = %v", got)
	}
	if got := payload["tool_choice"]; got != "auto" {
		t.Fatalf("tool_choice = %v", got)
	}
	if got := payload["prompt_cache_key"]; got != "codex-session" {
		t.Fatalf("prompt_cache_key = %v", got)
	}
	include, ok := payload["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", payload["include"])
	}
	clientMetadata, ok := payload["client_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("client_metadata = %#v", payload["client_metadata"])
	}
	if got := clientMetadata["x-codex-installation-id"]; got != "install-1" {
		t.Fatalf("client_metadata installation id = %v", got)
	}
}

func TestCodexOfficialRouteUsesCapturedDefaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	route := routeInfo{
		BaseURL:       "https://chatgpt.test/backend-api/codex",
		ProviderType:  "openai",
		AuthMode:      "codex_cli",
		TokenProvider: "openai_codex",
		AuthScheme:    "bearer",
		TokenSubject:  "acc-capture",
		APIKey:        "cxtok",
	}

	upstream, cancel, err := openaiCompatibleAdapter{}.PrepareRequest(req, route, nil)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()
	if got := upstream.Header.Get("Accept"); got != "*/*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := upstream.Header.Get("Accept-Encoding"); got != "" {
		t.Fatalf("Accept-Encoding should be absent for Codex official routes, got %q", got)
	}
	if got := upstream.Header.Get("User-Agent"); got != "codex_exec/0.0.0 (Ubuntu 24.4.0; x86_64) xterm-256color (codex_exec; 0.0.0)" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := upstream.Header.Get("ChatGPT-Account-ID"); got != "acc-capture" {
		t.Fatalf("ChatGPT-Account-ID = %q", got)
	}
	if got := upstream.URL.String(); got != "https://chatgpt.test/backend-api/codex/models?client_version=0.0.0" {
		t.Fatalf("upstream URL = %q", got)
	}
}

func TestCodexModelsCacheMergesOfficialModelMetadata(t *testing.T) {
	var hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if got := r.URL.Path; got != "/backend-api/codex/models" {
			t.Fatalf("path = %q", got)
		}
		if got := r.URL.Query().Get("client_version"); got != "1.2.3" {
			t.Fatalf("client_version = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer codex-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "chatgpt-account-1" {
			t.Fatalf("ChatGPT-Account-ID = %q", got)
		}
		writeRawJSON(w, http.StatusOK, map[string]any{
			"models": []any{
				map[string]any{
					"slug":                             "gpt-upstream",
					"display_name":                     "GPT Upstream",
					"description":                      "remote metadata",
					"default_reasoning_level":          "medium",
					"supported_reasoning_levels":       []any{map[string]any{"effort": "medium", "description": "medium"}},
					"shell_type":                       "shell_command",
					"visibility":                       "list",
					"supported_in_api":                 true,
					"priority":                         1,
					"base_instructions":                "remote base instructions",
					"supports_reasoning_summaries":     true,
					"default_reasoning_summary":        "auto",
					"support_verbosity":                true,
					"default_verbosity":                "medium",
					"apply_patch_tool_type":            "freeform",
					"web_search_tool_type":             "default",
					"truncation_policy":                map[string]any{"mode": "tokens", "limit": 100000},
					"supports_parallel_tool_calls":     false,
					"supports_image_detail_original":   true,
					"context_window":                   272000,
					"max_context_window":               400000,
					"auto_compact_token_limit":         244000,
					"effective_context_window_percent": 90,
					"experimental_supported_tools":     []any{"apply_patch"},
					"input_modalities":                 []any{"text", "image"},
				},
			},
		})
	}))
	defer upstream.Close()

	server := &Server{codexModels: newCodexModelsCache()}
	route := routeInfo{
		BaseURL:       upstream.URL + "/backend-api/codex",
		ProviderType:  "codex_compatible",
		AccountID:     "relay-account-1",
		TokenSubject:  "chatgpt-account-1",
		TokenProvider: "openai_codex",
		APIKey:        "codex-token",
		UpstreamModel: "gpt-upstream",
		TokenMetadata: map[string]any{
			"client_version": "1.2.3",
		},
	}

	enriched, err := server.enrichCodexRoute(context.Background(), route)
	if err != nil {
		t.Fatalf("enrichCodexRoute returned error: %v", err)
	}
	enrichedAgain, err := server.enrichCodexRoute(context.Background(), route)
	if err != nil {
		t.Fatalf("second enrichCodexRoute returned error: %v", err)
	}
	if hits != 1 {
		t.Fatalf("models endpoint hits = %d", hits)
	}
	if got := codexInstructions(enriched); got != "remote base instructions" {
		t.Fatalf("instructions = %q", got)
	}
	reasoning, ok := codexReasoning(enriched).(map[string]any)
	if !ok {
		t.Fatalf("reasoning = %#v", codexReasoning(enriched))
	}
	if got := reasoning["effort"]; got != "medium" {
		t.Fatalf("reasoning.effort = %v", got)
	}
	if got := reasoning["summary"]; got != "auto" {
		t.Fatalf("reasoning.summary = %v", got)
	}
	if got := codexParallelToolCalls(enriched); got {
		t.Fatal("parallel_tool_calls should follow remote supports_parallel_tool_calls=false")
	}
	codexMeta, ok := objectValue(enriched.TokenMetadata["codex"])
	if !ok {
		t.Fatalf("codex metadata = %#v", enriched.TokenMetadata["codex"])
	}
	for _, key := range []string{"context_window", "max_context_window", "auto_compact_token_limit", "input_modalities", "experimental_supported_tools", "truncation_policy"} {
		if _, ok := codexMeta[key]; !ok {
			t.Fatalf("codex metadata missing %q: %#v", key, codexMeta)
		}
	}
	if got := codexInstructions(enrichedAgain); got != "remote base instructions" {
		t.Fatalf("cached instructions = %q", got)
	}
}

func TestCodexReasoningDisabledWhenModelDoesNotSupportSummaries(t *testing.T) {
	route := routeInfo{
		ProviderType: "codex_compatible",
		TokenMetadata: map[string]any{
			"codex": map[string]any{
				"supports_reasoning_summaries": false,
				"default_reasoning_level":      "medium",
				"default_reasoning_summary":    "auto",
			},
		},
	}
	if got := codexReasoning(route); got != nil {
		t.Fatalf("reasoning should be nil when summaries are unsupported, got %#v", got)
	}
}

func TestCodexOfficialRouteMapsOpenAIBaseV1Once(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{
		BaseURL:      "https://api.openai.test/v1",
		ProviderType: "codex_compatible",
		APIKey:       "cxtok",
		AccountMeta:  `{"codex":{"disable_request_compression":true}}`,
	}

	upstream, cancel, err := codexCompatibleAdapter{}.PrepareRequest(req, route, []byte(`{"model":"gpt-test"}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()
	if got := upstream.URL.String(); got != "https://api.openai.test/v1/responses" {
		t.Fatalf("upstream URL = %q", got)
	}
}

func TestCodexOfficialRouteMapsOpenAIBaseApiCodexOnce(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{
		BaseURL:      "https://api.openai.test/api/codex",
		ProviderType: "codex_compatible",
		APIKey:       "cxtok",
		AccountMeta:  `{"codex":{"disable_request_compression":true}}`,
	}

	upstream, cancel, err := codexCompatibleAdapter{}.PrepareRequest(req, route, []byte(`{"model":"gpt-test"}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()
	if got := upstream.URL.String(); got != "https://api.openai.test/api/codex/responses" {
		t.Fatalf("upstream URL = %q", got)
	}
}

func TestCodexOfficialCompactUsesCompactHeadersAndBody(t *testing.T) {
	body := []byte(`{"model":"gpt-public","input":[],"store":true,"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Elucid-Relay-Session", "codex-session")
	route := routeInfo{
		BaseURL:       "https://chatgpt.test/backend-api/codex",
		ProviderType:  "codex_compatible",
		APIKey:        "cxtok",
		UpstreamModel: "gpt-upstream",
		AccountMeta:   `{"codex":{"disable_request_compression":true,"base_instructions":"Compact base","installation_id":"install-1"}}`,
	}

	upstream, cancel, err := codexCompatibleAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.URL.String(); got != "https://chatgpt.test/backend-api/codex/responses/compact" {
		t.Fatalf("upstream URL = %q", got)
	}
	if got := upstream.Header.Get("x-codex-installation-id"); got != "install-1" {
		t.Fatalf("x-codex-installation-id = %q", got)
	}
	if got := upstream.Header.Get("Accept"); got != "" {
		t.Fatalf("Accept should be absent for compact POST, got %q", got)
	}
	if got := upstream.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding should be absent when compression is disabled, got %q", got)
	}

	outboundBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(outboundBody, &payload); err != nil {
		t.Fatalf("upstream body is not JSON: %v", err)
	}
	if got := payload["model"]; got != "gpt-upstream" {
		t.Fatalf("model = %v", got)
	}
	if got := payload["instructions"]; got != "Compact base" {
		t.Fatalf("instructions = %v", got)
	}
	if got := payload["parallel_tool_calls"]; got != true {
		t.Fatalf("parallel_tool_calls = %v", got)
	}
	if _, ok := payload["reasoning"].(map[string]any); !ok {
		t.Fatalf("reasoning = %#v", payload["reasoning"])
	}
	for _, key := range []string{"stream", "include", "prompt_cache_key", "client_metadata", "store", "tool_choice"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("%s should be absent for compact body: %#v", key, payload[key])
		}
	}
}

func TestCodexInstallationIDDoesNotFallbackToRelayAccountID(t *testing.T) {
	route := routeInfo{
		ProviderType: "codex_compatible",
		AccountID:    "relay-account-id",
	}
	if got := codexInstallationID(route); got != "" {
		t.Fatalf("installation id should not fall back to account id, got %q", got)
	}
}

func TestCodexLegacyPayloadCompatibility(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://relay.local/v1/responses", nil)
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{
		BaseURL:      "https://chatgpt.test/backend-api/codex",
		ProviderType: "codex_compatible",
		APIKey:       "cxtok",
		AccountMeta:  `{"codex":{"disable_request_compression":true}}`,
	}
	body := []byte(`{
		"model":"gpt-test",
		"messages":[
			{"role":"system","content":"Use repo tools."},
			{"role":"user","content":"hi"}
		],
		"functions":[{"name":"lookup","parameters":{"type":"object"}}],
		"function_call":{"name":"lookup"}
	}`)
	upstream, cancel, err := codexCompatibleAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()
	upstreamBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(upstreamBody, &payload); err != nil {
		t.Fatalf("unmarshal upstream body: %v\n%s", err, upstreamBody)
	}
	if _, ok := payload["messages"]; ok {
		t.Fatalf("messages should be converted to input: %#v", payload)
	}
	if _, ok := payload["functions"]; ok {
		t.Fatalf("functions should be converted to tools: %#v", payload)
	}
	if got := metadataText(payload["instructions"]); got != "Use repo tools." {
		t.Fatalf("instructions = %q", got)
	}
	tools, ok := arrayValue(payload["tools"])
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", payload["tools"])
	}
	choice, ok := objectValue(payload["tool_choice"])
	if !ok || metadataText(choice["type"]) != "function" {
		t.Fatalf("tool_choice = %#v", payload["tool_choice"])
	}
}

func TestGitHubCopilotAdapterAppliesOfficialHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"assistant","content":"ok"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Elucid-Relay-Session", "copilot-session")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "copilot-request-1"))
	route := routeInfo{
		BaseURL:       "https://api.githubcopilot.com",
		ProviderType:  "github_copilot",
		APIKey:        "cptok",
		TokenProvider: "github_copilot",
		AuthScheme:    "bearer",
		TokenMetadata: map[string]any{
			"client_version": "0.99.0",
			"vscode_version": "1.105.0",
			"user_agent":     "GitHubCopilotChat/0.99.0",
		},
	}

	upstream, cancel, err := githubCopilotAdapter{}.PrepareRequest(req, route, []byte(`{"model":"gpt-test","messages":[{"role":"assistant","content":"ok"}]}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("Authorization"); got != "Bearer cptok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("User-Agent"); got != "GitHubCopilotChat/0.99.0" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := upstream.Header.Get("copilot-integration-id"); got != "vscode-chat" {
		t.Fatalf("copilot-integration-id = %q", got)
	}
	if got := upstream.Header.Get("editor-version"); got != "vscode/1.105.0" {
		t.Fatalf("editor-version = %q", got)
	}
	if got := upstream.Header.Get("editor-plugin-version"); got != "copilot-chat/0.99.0" {
		t.Fatalf("editor-plugin-version = %q", got)
	}
	if got := upstream.Header.Get("openai-intent"); got != "conversation-panel" {
		t.Fatalf("openai-intent = %q", got)
	}
	if got := upstream.Header.Get("x-github-api-version"); got != "2025-05-01" {
		t.Fatalf("x-github-api-version = %q", got)
	}
	if got := upstream.Header.Get("x-vscode-user-agent-library-version"); got != "electron-fetch" {
		t.Fatalf("x-vscode-user-agent-library-version = %q", got)
	}
	if got := upstream.Header.Get("x-onbehalf-extension-id"); got != "" {
		t.Fatalf("x-onbehalf-extension-id should be omitted when not configured, got %q", got)
	}
	if got := upstream.Header.Get("x-request-id"); got == "" {
		t.Fatal("x-request-id should be synthesized")
	}
	if got := upstream.Header.Get("x-interaction-id"); got != "copilot-session" {
		t.Fatalf("x-interaction-id = %q", got)
	}
	if got := upstream.Header.Get("x-interaction-type"); got != "conversation-panel" {
		t.Fatalf("x-interaction-type = %q", got)
	}
	if got := upstream.Header.Get("x-agent-task-id"); got != "copilot-request-1" {
		t.Fatalf("x-agent-task-id = %q", got)
	}
	if got := upstream.Header.Get("X-Initiator"); got != "agent" {
		t.Fatalf("X-Initiator = %q", got)
	}
	if got := upstream.Header.Get("Accept-Encoding"); got != "" {
		t.Fatalf("Accept-Encoding should be absent, got %q", got)
	}
}

func TestGitHubCopilotAdapterSynthesizesOnBehalfExtensionID(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"ok"}]}`))
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{
		BaseURL:      "https://api.githubcopilot.com",
		ProviderType: "github_copilot",
		APIKey:       "cptok",
		TokenMetadata: map[string]any{
			"client_version":    "0.99.0",
			"vscode_version":    "1.105.0",
			"user_agent":        "GitHubCopilotChat/0.99.0",
			"extension_id":      "my-extension",
			"extension_version": "1.2.3",
		},
	}

	upstream, cancel, err := githubCopilotAdapter{}.PrepareRequest(req, route, []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"ok"}]}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("x-onbehalf-extension-id"); got != "my-extension/1.2.3" {
		t.Fatalf("x-onbehalf-extension-id = %q", got)
	}
}

func TestGeminiOpenAICompatibleAdapterAppliesGoogleOAuthHeaders(t *testing.T) {
	body := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{
		BaseURL:       "https://generativelanguage.googleapis.com/v1beta/openai",
		ProviderType:  "gemini_openai_compatible",
		APIKey:        "ggtok",
		AuthMode:      "google_pkce",
		TokenProvider: "google_gemini",
		AuthScheme:    "bearer",
		TokenMetadata: map[string]any{
			"client_version":       "0.42.0-test",
			"genai_sdk_client":     "google-genai-sdk/1.50.0 gl-node/v22.20.0",
			"quota_project_id":     "quota-project-1",
			"user_agent_prefix":    "GeminiCLI",
			"surface":              "terminal",
			"official_client":      map[string]any{"gemini": map[string]any{"platform": "linux", "arch": "x64"}},
			"x_goog_api_client":    "google-genai-sdk/1.50.0 gl-node/v22.20.0",
			"code_assist_base_url": "https://cloudcode-pa.googleapis.com/v1internal",
		},
	}

	upstream, cancel, err := geminiOpenAICompatibleAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.URL.String(); got != "https://generativelanguage.googleapis.com/v1beta/openai/v1/chat/completions" {
		t.Fatalf("upstream URL = %q", got)
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer ggtok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("X-Goog-Api-Key"); got != "" {
		t.Fatalf("X-Goog-Api-Key should not be set, got %q", got)
	}
	if got := upstream.Header.Get("User-Agent"); got != "GeminiCLI/0.42.0-test/gemini-2.5-pro (linux; x64; terminal)" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := upstream.Header.Get("X-Goog-Api-Client"); got != "google-genai-sdk/1.50.0 gl-node/v22.20.0" {
		t.Fatalf("X-Goog-Api-Client = %q", got)
	}
	if got := upstream.Header.Get("X-Goog-User-Project"); got != "quota-project-1" {
		t.Fatalf("X-Goog-User-Project = %q", got)
	}
	if got := upstream.Header.Get("Accept-Encoding"); got != "" {
		t.Fatalf("Accept-Encoding should be removed, got %q", got)
	}
	if got := upstream.Header.Get("X-Request-Id"); got != "" {
		t.Fatalf("X-Request-Id should not be synthesized for Gemini OAuth, got %q", got)
	}
}

func TestGeminiNativeAdapterUsesAPIKeyUnlessOAuth(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", strings.NewReader(`{"contents":[]}`))
	req.Header.Set("Content-Type", "application/json")
	apiKeyRoute := routeInfo{
		BaseURL:      "https://generativelanguage.googleapis.com",
		ProviderType: "gemini",
		APIKey:       "gmkey",
	}
	apiKeyReq, apiKeyCancel, err := geminiNativeAdapter{}.PrepareRequest(req, apiKeyRoute, []byte(`{"contents":[]}`))
	if err != nil {
		t.Fatalf("PrepareRequest api key returned error: %v", err)
	}
	defer apiKeyCancel()
	if got := apiKeyReq.Header.Get("X-Goog-Api-Key"); got != "gmkey" {
		t.Fatalf("X-Goog-Api-Key = %q", got)
	}
	if got := apiKeyReq.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization should not be set for API key route, got %q", got)
	}

	oauthRoute := routeInfo{
		BaseURL:       "https://generativelanguage.googleapis.com",
		ProviderType:  "gemini",
		APIKey:        "ggtok",
		AuthMode:      "google_pkce",
		TokenProvider: "google_gemini",
		AuthScheme:    "bearer",
	}
	oauthReq, oauthCancel, err := geminiNativeAdapter{}.PrepareRequest(req, oauthRoute, []byte(`{"contents":[]}`))
	if err != nil {
		t.Fatalf("PrepareRequest oauth returned error: %v", err)
	}
	defer oauthCancel()
	if got := oauthReq.Header.Get("Authorization"); got != "Bearer ggtok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := oauthReq.Header.Get("X-Goog-Api-Key"); got != "" {
		t.Fatalf("X-Goog-Api-Key should not be set for OAuth route, got %q", got)
	}
}

func TestGeminiCLIAdapterKeepsCodeAssistMethodPathAndSSEQuery(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent", strings.NewReader(`{"model":"gemini-2.5-pro"}`))
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{
		BaseURL:       "https://cloudcode-pa.googleapis.com/v1internal",
		ProviderType:  "gemini_cli",
		APIKey:        "ggtok",
		TokenProvider: "google_gemini",
		AuthScheme:    "bearer",
		TokenMetadata: map[string]any{
			"client_version": "0.42.0-test",
			"platform":       "linux",
			"arch":           "x64",
		},
	}

	upstream, cancel, err := geminiCLIAdapter{}.PrepareRequest(req, route, []byte(`{"model":"gemini-2.5-pro"}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.URL.String(); got != "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse" {
		t.Fatalf("upstream URL = %q", got)
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer ggtok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("User-Agent"); got != "GeminiCLI/0.42.0-test/gemini-2.5-pro (linux; x64; terminal)" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := upstream.Header.Get("X-Goog-Api-Client"); got != "" {
		t.Fatalf("X-Goog-Api-Client should not be set for Code Assist OAuth path, got %q", got)
	}
	if got := upstream.Header.Get("X-Goog-User-Project"); got != "" {
		t.Fatalf("X-Goog-User-Project should not be set for Code Assist OAuth path, got %q", got)
	}
}

func TestGeminiCLIAdapterTranslatesOpenAIChatToCodeAssistRequest(t *testing.T) {
	body := []byte(`{
		"model":"public-gemini",
		"stream":true,
		"messages":[
			{"role":"system","content":"You are terse."},
			{"role":"user","content":[{"type":"text","text":"hi"}]}
		],
		"tools":[{"type":"function","function":{"name":"lookup","description":"search","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}],
		"tool_choice":{"type":"function","function":{"name":"lookup"}},
		"temperature":0.2,
		"max_tokens":128,
		"reasoning_effort":"high",
		"metadata":{"session_id":"chat-session"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{
		BaseURL:       "https://cloudcode-pa.googleapis.com",
		ProviderType:  "gemini_cli",
		UpstreamModel: "gemini-2.5-pro",
		APIKey:        "ggtok",
		TokenProvider: "google_gemini",
		TokenMetadata: map[string]any{
			"project_id":     "cloud-project",
			"client_version": "0.42.0-test",
			"platform":       "linux",
			"arch":           "x64",
		},
	}

	upstream, cancel, err := geminiCLIAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.URL.String(); got != "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse" {
		t.Fatalf("upstream URL = %q", got)
	}
	if got := upstream.Header.Get("Accept"); got != "*/*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := upstream.Header.Get("Accept-Encoding"); got != "gzip, deflate" {
		t.Fatalf("Accept-Encoding = %q", got)
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer ggtok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("X-Goog-Api-Client"); got != "" {
		t.Fatalf("X-Goog-Api-Client should not be set for Code Assist OAuth path, got %q", got)
	}
	if got := upstream.Header.Get("X-Goog-User-Project"); got != "" {
		t.Fatalf("X-Goog-User-Project should not be set for Code Assist OAuth path, got %q", got)
	}

	var outbound map[string]any
	if err := json.NewDecoder(upstream.Body).Decode(&outbound); err != nil {
		t.Fatalf("Decode outbound body returned error: %v", err)
	}
	if got := metadataText(outbound["model"]); got != "gemini-2.5-pro" {
		t.Fatalf("model = %q", got)
	}
	if got := metadataText(outbound["project"]); got != "cloud-project" {
		t.Fatalf("project = %q", got)
	}
	request, ok := objectValue(outbound["request"])
	if !ok {
		t.Fatalf("request missing: %#v", outbound)
	}
	if got := metadataText(request["session_id"]); got != "chat-session" {
		t.Fatalf("session_id = %q", got)
	}
	if got := metadataText(outbound["user_prompt_id"]); got != "chat-session" {
		t.Fatalf("user_prompt_id = %q", got)
	}
	systemInstruction, _ := objectValue(request["systemInstruction"])
	systemParts, _ := systemInstruction["parts"].([]any)
	if len(systemParts) != 1 {
		t.Fatalf("systemInstruction parts = %#v", systemParts)
	}
	systemPart, _ := objectValue(systemParts[0])
	if got := metadataText(systemPart["text"]); got != "You are terse." {
		t.Fatalf("systemInstruction text = %q", got)
	}
	generationConfig, _ := objectValue(request["generationConfig"])
	if got := generationConfig["temperature"]; got != 0.2 {
		t.Fatalf("temperature = %#v", got)
	}
	if got := generationConfig["topP"]; got != 0.95 {
		t.Fatalf("topP default = %#v", got)
	}
	if got := generationConfig["maxOutputTokens"]; got != float64(128) {
		t.Fatalf("maxOutputTokens = %#v", got)
	}
	thinkingConfig, _ := objectValue(generationConfig["thinkingConfig"])
	if got := thinkingConfig["thinkingBudget"]; got != float64(24576) {
		t.Fatalf("thinkingBudget = %#v", got)
	}
	if got := thinkingConfig["includeThoughts"]; got != true {
		t.Fatalf("includeThoughts = %#v", got)
	}
	tools, _ := request["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	tool, _ := objectValue(tools[0])
	declarations, _ := tool["functionDeclarations"].([]any)
	declaration, _ := objectValue(declarations[0])
	if got := metadataText(declaration["name"]); got != "lookup" {
		t.Fatalf("function declaration name = %q", got)
	}
	if _, ok := declaration["parametersJsonSchema"]; !ok {
		t.Fatalf("parametersJsonSchema missing: %#v", declaration)
	}
	toolConfig, _ := objectValue(request["toolConfig"])
	functionCalling, _ := objectValue(toolConfig["functionCallingConfig"])
	if got := metadataText(functionCalling["mode"]); got != "ANY" {
		t.Fatalf("functionCallingConfig mode = %q", got)
	}
}

func TestGeminiCLIAdapterAddsDefaultPromptAndSessionIDs(t *testing.T) {
	body := []byte(`{"model":"public-gemini","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "gemini-request-1"))
	route := routeInfo{
		BaseURL:       "https://cloudcode-pa.googleapis.com",
		ProviderType:  "gemini_cli",
		UpstreamModel: "gemini-2.5-pro",
		APIKey:        "ggtok",
		TokenProvider: "google_gemini",
	}

	upstream, cancel, err := geminiCLIAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	var outbound map[string]any
	if err := json.NewDecoder(upstream.Body).Decode(&outbound); err != nil {
		t.Fatalf("Decode outbound body returned error: %v", err)
	}
	request, ok := objectValue(outbound["request"])
	if !ok {
		t.Fatalf("request missing: %#v", outbound)
	}
	if got := metadataText(outbound["user_prompt_id"]); got != "gemini-request-1" {
		t.Fatalf("user_prompt_id = %q", got)
	}
	if got := metadataText(request["session_id"]); got != "gemini-request-1" {
		t.Fatalf("session_id = %q", got)
	}
}

func TestGeminiCLIAdapterTransformsCodeAssistResponseToOpenAIChatCompletion(t *testing.T) {
	upstreamBody := []byte(`{
		"response":{
			"modelVersion":"gemini-2.5-pro-001",
			"candidates":[{"index":0,"content":{"parts":[{"text":"thinking","thought":true},{"text":"hello"},{"functionCall":{"name":"lookup","args":{"q":"x"}}}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":4,"thoughtsTokenCount":3,"cachedContentTokenCount":2,"totalTokenCount":19}
		}
	}`)

	status, header, transformed := geminiCLIAdapter{}.TransformHTTPResponse(
		routeInfo{ProviderType: "gemini_cli"},
		"chat",
		[]byte(`{"model":"gemini-2.5-pro"}`),
		http.StatusOK,
		http.Header{"Content-Type": []string{"application/json"}, "Content-Length": []string{"999"}},
		upstreamBody,
	)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if got := header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length should be stripped, got %q", got)
	}

	var payload map[string]any
	if err := json.Unmarshal(transformed, &payload); err != nil {
		t.Fatalf("Unmarshal transformed response returned error: %v", err)
	}
	if got := metadataText(payload["object"]); got != "chat.completion" {
		t.Fatalf("object = %q", got)
	}
	if got := metadataText(payload["model"]); got != "gemini-2.5-pro-001" {
		t.Fatalf("model = %q", got)
	}
	choices, _ := payload["choices"].([]any)
	choice, _ := objectValue(choices[0])
	if got := metadataText(choice["finish_reason"]); got != "tool_calls" {
		t.Fatalf("finish_reason = %q", got)
	}
	message, _ := objectValue(choice["message"])
	if got := metadataText(message["content"]); got != "hello" {
		t.Fatalf("content = %q", got)
	}
	if got := metadataText(message["reasoning_content"]); got != "thinking" {
		t.Fatalf("reasoning_content = %q", got)
	}
	toolCalls, _ := message["tool_calls"].([]any)
	toolCall, _ := objectValue(toolCalls[0])
	function, _ := objectValue(toolCall["function"])
	if got := metadataText(function["name"]); got != "lookup" {
		t.Fatalf("tool call name = %q", got)
	}
	usage, _ := objectValue(payload["usage"])
	if got := usage["prompt_tokens"]; got != float64(12) {
		t.Fatalf("prompt_tokens = %#v", got)
	}
	if got := usage["completion_tokens"]; got != float64(7) {
		t.Fatalf("completion_tokens = %#v", got)
	}
	if got := usage["total_tokens"]; got != float64(19) {
		t.Fatalf("total_tokens = %#v", got)
	}
}

func TestGeminiCLIAdapterTransformsCodeAssistErrorResponse(t *testing.T) {
	status, _, transformed := geminiCLIAdapter{}.TransformHTTPResponse(
		routeInfo{ProviderType: "gemini_cli"},
		"chat",
		[]byte(`{"model":"gemini-2.5-pro"}`),
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"error":{"code":400,"message":"bad model"}}`),
	)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d", status)
	}
	var payload map[string]any
	if err := json.Unmarshal(transformed, &payload); err != nil {
		t.Fatalf("Unmarshal transformed error returned error: %v", err)
	}
	errorPayload, _ := objectValue(payload["error"])
	if got := metadataText(errorPayload["message"]); got != "bad model" {
		t.Fatalf("error message = %q", got)
	}
}

func TestGeminiCLIAdapterClassifiesCloudCodePrivateAPIDisabled(t *testing.T) {
	status, _, transformed := geminiCLIAdapter{}.TransformHTTPResponse(
		routeInfo{ProviderType: "antigravity"},
		"chat",
		[]byte(`{"model":"claude-sonnet-4-5"}`),
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"code":403,"message":"Cloud Code Private API has not been used in project demo before or it is disabled. Enable it by visiting https://console.developers.google.com/apis/api/cloudcode-pa.googleapis.com/overview?project=demo then retry."}}`),
	)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d", status)
	}
	var payload map[string]any
	if err := json.Unmarshal(transformed, &payload); err != nil {
		t.Fatalf("Unmarshal transformed error returned error: %v", err)
	}
	errorPayload, _ := objectValue(payload["error"])
	if got := metadataText(errorPayload["code"]); got != "cloud_code_private_api_disabled" {
		t.Fatalf("error code = %q", got)
	}
}

func TestGitHubCopilotAdapterMarksVisionRequestsFromBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"input_text","text":"describe the image"},{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "copilot-request-vision"))
	route := routeInfo{
		BaseURL:      "https://api.githubcopilot.com",
		ProviderType: "github_copilot",
		APIKey:       "cptok",
	}

	upstream, cancel, err := githubCopilotAdapter{}.PrepareRequest(req, route, []byte(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"input_text","text":"describe the image"},{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("copilot-vision-request"); got != "true" {
		t.Fatalf("copilot-vision-request = %q", got)
	}
	if got := upstream.Header.Get("X-Initiator"); got != "user" {
		t.Fatalf("X-Initiator = %q", got)
	}
}

func TestAntigravityAdapterTranslatesOpenAIChatToCodeAssistRequest(t *testing.T) {
	body := []byte(`{"model":"public-antigravity","stream":true,"messages":[{"role":"system","content":"You are terse."},{"role":"user","content":"hi"}],"reasoning_effort":"high","metadata":{"session_id":"ag-session"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "ag-request-1"))
	route := routeInfo{
		BaseURL:       "https://daily-cloudcode-pa.googleapis.com",
		ProviderType:  "antigravity",
		UpstreamModel: "claude-sonnet-4-5",
		APIKey:        "agtok",
		TokenProvider: "google_antigravity",
		TokenMetadata: map[string]any{
			"client_version":            "1.20.5-test",
			"platform":                  "linux",
			"arch":                      "x64",
			"cloudaicompanion_project":  "ag-project",
			"default_max_output_tokens": "32000",
			"x_goog_request_reason":     "agent-test",
			"official_client":           map[string]any{"antigravity": map[string]any{"request_type": "agent"}},
		},
	}

	upstream, cancel, err := antigravityAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.URL.String(); got != "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse" {
		t.Fatalf("upstream URL = %q", got)
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer agtok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("User-Agent"); got != "antigravity/1.20.5-test linux/x64" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := upstream.Header.Get("X-Goog-User-Project"); got != "ag-project" {
		t.Fatalf("X-Goog-User-Project = %q", got)
	}
	if got := upstream.Header.Get("X-Goog-Request-Reason"); got != "agent-test" {
		t.Fatalf("X-Goog-Request-Reason = %q", got)
	}

	var outbound map[string]any
	if err := json.NewDecoder(upstream.Body).Decode(&outbound); err != nil {
		t.Fatalf("Decode outbound body returned error: %v", err)
	}
	if got := metadataText(outbound["model"]); got != "claude-sonnet-4-5" {
		t.Fatalf("model = %q", got)
	}
	if got := metadataText(outbound["userAgent"]); got != "antigravity/1.20.5-test linux/x64" {
		t.Fatalf("userAgent = %q", got)
	}
	if got := metadataText(outbound["requestType"]); got != "agent" {
		t.Fatalf("requestType = %q", got)
	}
	if got := metadataText(outbound["project"]); got != "ag-project" {
		t.Fatalf("project = %q", got)
	}
	if got := metadataText(outbound["requestId"]); got != "agent-ag-request-1" {
		t.Fatalf("requestId = %q", got)
	}
	request, ok := objectValue(outbound["request"])
	if !ok {
		t.Fatalf("request missing: %#v", outbound)
	}
	if got := metadataText(request["sessionId"]); got != "ag-session" {
		t.Fatalf("sessionId = %q", got)
	}
	generationConfig, _ := objectValue(request["generationConfig"])
	if got := generationConfig["maxOutputTokens"]; got != float64(32000) {
		t.Fatalf("maxOutputTokens = %#v", got)
	}
	thinkingConfig, _ := objectValue(generationConfig["thinkingConfig"])
	if got := thinkingConfig["thinkingBudget"]; got != float64(24576) {
		t.Fatalf("thinkingBudget = %#v", got)
	}
}

func TestAntigravityAdapterPreservesClaudeToolCallsAndResponses(t *testing.T) {
	body := []byte(`{"model":"gemini-claude-sonnet-4-5","messages":[{"role":"user","content":"use tool"},{"role":"assistant","content":"","tool_calls":[{"id":"call_lookup","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},{"role":"tool","tool_call_id":"call_lookup","content":"{\"ok\":true}"}],"tools":[{"type":"function","function":{"name":"lookup","description":"search","strict":true,"parameters":{"type":"object","$schema":"http://json-schema.org/draft-07/schema#","additionalProperties":false,"properties":{"q":{"type":"string","additionalProperties":false}}}}}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	route := routeInfo{
		BaseURL:      "https://daily-cloudcode-pa.googleapis.com",
		ProviderType: "antigravity",
		APIKey:       "agtok",
		TokenMetadata: map[string]any{
			"cloudaicompanion_project": "ag-project",
		},
	}

	upstream, cancel, err := antigravityAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	var outbound map[string]any
	if err := json.NewDecoder(upstream.Body).Decode(&outbound); err != nil {
		t.Fatalf("Decode outbound body returned error: %v", err)
	}
	if got := metadataText(outbound["model"]); got != "claude-sonnet-4-5" {
		t.Fatalf("model = %q", got)
	}
	request, _ := objectValue(outbound["request"])
	tools, _ := request["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", request["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	declarations, _ := tool["functionDeclarations"].([]any)
	declaration, _ := declarations[0].(map[string]any)
	if _, ok := declaration["parametersJsonSchema"]; ok {
		t.Fatalf("parametersJsonSchema should not be used for Claude Antigravity tools: %#v", declaration)
	}
	parameters, _ := objectValue(declaration["parameters"])
	if _, ok := parameters["$schema"]; ok {
		t.Fatalf("$schema was not stripped: %#v", parameters)
	}
	if _, ok := parameters["additionalProperties"]; ok {
		t.Fatalf("additionalProperties was not stripped: %#v", parameters)
	}
	toolConfig, _ := objectValue(request["toolConfig"])
	functionCalling, _ := objectValue(toolConfig["functionCallingConfig"])
	if got := metadataText(functionCalling["mode"]); got != "VALIDATED" {
		t.Fatalf("toolConfig mode = %q", got)
	}
	contents, _ := request["contents"].([]any)
	assistantContent, _ := contents[1].(map[string]any)
	assistantParts, _ := assistantContent["parts"].([]any)
	functionCallPart, _ := assistantParts[0].(map[string]any)
	functionCall, _ := objectValue(functionCallPart["functionCall"])
	if got := metadataText(functionCall["id"]); got != "call_lookup" {
		t.Fatalf("functionCall.id = %q", got)
	}
	if got := metadataText(functionCallPart["thoughtSignature"]); got != "skip_thought_signature_validator" {
		t.Fatalf("thoughtSignature = %q", got)
	}
	toolContent, _ := contents[2].(map[string]any)
	toolParts, _ := toolContent["parts"].([]any)
	responsePart, _ := toolParts[0].(map[string]any)
	functionResponse, _ := objectValue(responsePart["functionResponse"])
	if got := metadataText(functionResponse["id"]); got != "call_lookup" {
		t.Fatalf("functionResponse.id = %q", got)
	}
}

func TestKiroAdapterTranslatesOpenAIChatToGenerateAssistantResponse(t *testing.T) {
	body := []byte(`{"model":"public-kiro","messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","description":"search","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "kiro-request-1"))
	route := routeInfo{
		BaseURL:       "https://q.us-east-1.amazonaws.com",
		ProviderType:  "kiro",
		UpstreamModel: "claude-sonnet-4.5",
		APIKey:        "kirotok",
		TokenMetadata: map[string]any{
			"client_version": "0.7.45-test",
			"fingerprint":    "fingerprint-1",
			"profile_arn":    "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC",
		},
	}

	upstream, cancel, err := kiroAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.URL.String(); got != "https://q.us-east-1.amazonaws.com/generateAssistantResponse" {
		t.Fatalf("upstream URL = %q", got)
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer kirotok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("x-amzn-kiro-agent-mode"); got != "vibe" {
		t.Fatalf("x-amzn-kiro-agent-mode = %q", got)
	}
	if got := upstream.Header.Get("x-amz-user-agent"); got != "aws-sdk-js/1.0.27 KiroIDE-0.7.45-test-fingerprint-1" {
		t.Fatalf("x-amz-user-agent = %q", got)
	}

	var outbound map[string]any
	if err := json.NewDecoder(upstream.Body).Decode(&outbound); err != nil {
		t.Fatalf("Decode outbound body returned error: %v", err)
	}
	if got := metadataText(outbound["profileArn"]); got != "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC" {
		t.Fatalf("profileArn = %q", got)
	}
	state, _ := objectValue(outbound["conversationState"])
	if got := metadataText(state["conversationId"]); got != "kiro-request-1" {
		t.Fatalf("conversationId = %q", got)
	}
	current, _ := objectValue(state["currentMessage"])
	userInput, _ := objectValue(current["userInputMessage"])
	if got := metadataText(userInput["modelId"]); got != "claude-sonnet-4.5" {
		t.Fatalf("modelId = %q", got)
	}
	if got := metadataText(userInput["content"]); got != "Be concise.\n\nhello" {
		t.Fatalf("content = %q", got)
	}
	contextValue, _ := objectValue(userInput["userInputMessageContext"])
	tools, _ := contextValue["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
}

func TestKiroAdapterMapsImagesToolUsesAndToolResults(t *testing.T) {
	body := []byte(`{"model":"public-kiro","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"},{"role":"user","content":"next"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	route := routeInfo{
		BaseURL:       "https://q.us-east-1.amazonaws.com",
		ProviderType:  "kiro",
		UpstreamModel: "claude-sonnet-4.5",
		APIKey:        "kirotok",
	}

	upstream, cancel, err := kiroAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	var outbound map[string]any
	if err := json.NewDecoder(upstream.Body).Decode(&outbound); err != nil {
		t.Fatalf("Decode outbound body returned error: %v", err)
	}
	state, _ := objectValue(outbound["conversationState"])
	history, _ := state["history"].([]any)
	if len(history) != 4 {
		t.Fatalf("history = %#v", history)
	}
	first, _ := history[0].(map[string]any)
	firstUser, _ := objectValue(first["userInputMessage"])
	images, _ := firstUser["images"].([]any)
	if len(images) != 1 {
		t.Fatalf("images = %#v", firstUser["images"])
	}
	image, _ := images[0].(map[string]any)
	if got := metadataText(image["format"]); got != "png" {
		t.Fatalf("image.format = %q", got)
	}
	second, _ := history[1].(map[string]any)
	assistant, _ := objectValue(second["assistantResponseMessage"])
	toolUses, _ := assistant["toolUses"].([]any)
	if len(toolUses) != 1 {
		t.Fatalf("toolUses = %#v", assistant["toolUses"])
	}
	toolUse, _ := toolUses[0].(map[string]any)
	if got := metadataText(toolUse["toolUseId"]); got != "call_1" {
		t.Fatalf("toolUseId = %q", got)
	}
	third, _ := history[2].(map[string]any)
	toolUser, _ := objectValue(third["userInputMessage"])
	contextValue, _ := objectValue(toolUser["userInputMessageContext"])
	toolResults, _ := contextValue["toolResults"].([]any)
	if len(toolResults) != 1 {
		t.Fatalf("toolResults = %#v", contextValue["toolResults"])
	}
	toolResult, _ := toolResults[0].(map[string]any)
	if got := metadataText(toolResult["toolUseId"]); got != "call_1" {
		t.Fatalf("toolResult.toolUseId = %q", got)
	}
	current, _ := objectValue(state["currentMessage"])
	currentUser, _ := objectValue(current["userInputMessage"])
	if got := metadataText(currentUser["content"]); got != "next" {
		t.Fatalf("current content = %q", got)
	}
}

func TestKiroAdapterTransformsEventStreamToOpenAIChatCompletion(t *testing.T) {
	requestBody := []byte(`{"model":"public-kiro","messages":[{"role":"user","content":"hi"}]}`)
	responseBody := []byte("\x00\x00{\"content\":\"Hel\"}\x00{\"content\":\"lo\"}\x00{\"usage\":12}\x00{\"contextUsagePercentage\":0.25}")

	status, header, transformed := kiroAdapter{}.TransformHTTPResponse(
		routeInfo{ProviderType: "kiro", UpstreamModel: "claude-sonnet-4.5"},
		"chat",
		requestBody,
		http.StatusOK,
		http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
		responseBody,
	)

	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if got := header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(transformed, &payload); err != nil {
		t.Fatalf("transformed response is not JSON: %v\n%s", err, transformed)
	}
	choices, _ := payload["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %#v", choices)
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := objectValue(choice["message"])
	if got := metadataText(message["content"]); got != "Hello" {
		t.Fatalf("message.content = %q", got)
	}
	usage, _ := objectValue(payload["usage"])
	if got := usage["credits_used"]; got != float64(12) {
		t.Fatalf("credits_used = %#v", got)
	}
}

func TestKiroAdapterTransformsRawEventStreamToOpenAISSE(t *testing.T) {
	requestBody := []byte(`{"model":"public-kiro","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	responseBody := []byte("\x00{\"content\":\"Hi\"}\x00{\"name\":\"lookup\",\"toolUseId\":\"call_1\",\"input\":\"{\\\"q\\\":\\\"x\\\"}\"}\x00{\"stop\":true}\x00{\"contextUsagePercentage\":0.25}")
	var out bytes.Buffer
	acc := &streamMeteringAccumulator{}

	_, err := copyStreamAndMeterForRoute(&out, bytes.NewReader(responseBody), kiroAdapter{}, routeInfo{ProviderType: "kiro", UpstreamModel: "claude-sonnet-4.5"}, "chat", requestBody, acc)
	if err != nil {
		t.Fatalf("copyStreamAndMeterForRoute returned error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, `"content":"Hi"`) {
		t.Fatalf("stream output missing content: %s", output)
	}
	if !strings.Contains(output, `"tool_calls"`) {
		t.Fatalf("stream output missing tool calls: %s", output)
	}
	if !strings.Contains(output, "data: [DONE]") {
		t.Fatalf("stream output missing DONE: %s", output)
	}
	if acc.EventCount == 0 || acc.InputTokens == 0 {
		t.Fatalf("stream metering not populated: %#v", acc)
	}
}

func TestWindsurfCodeiumAdapterAppliesClientMetadataHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "windsurf-request-1"))
	route := routeInfo{
		BaseURL:      "https://server.codeium.com",
		ProviderType: "windsurf_codeium",
		APIKey:       "ckey",
		TokenMetadata: map[string]any{
			"ide_name":          "windsurf",
			"ide_version":       "1.2.3",
			"extension_name":    "windsurf",
			"extension_version": "1.2.4",
		},
	}

	upstream, cancel, err := windsurfCodeiumAdapter{}.PrepareRequest(req, route, []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("Authorization"); got != "Bearer ckey" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("User-Agent"); got != "Windsurf/1.2.3 Codeium/1.2.4" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := upstream.Header.Get("X-Codeium-IDE-Name"); got != "windsurf" {
		t.Fatalf("X-Codeium-IDE-Name = %q", got)
	}
	if got := upstream.Header.Get("X-Codeium-Request-Id"); got != "windsurf-request-1" {
		t.Fatalf("X-Codeium-Request-Id = %q", got)
	}
}

func TestWindsurfCodeiumAdapterMergesNativeRequestMetadata(t *testing.T) {
	body := []byte(`{"document":{"text":"hello"},"editor_options":{"tab_size":2}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "windsurf-request-2"))
	route := routeInfo{
		BaseURL:      "https://server.codeium.com",
		ProviderType: "windsurf_codeium",
		APIKey:       "ckey",
		TokenMetadata: map[string]any{
			"ide_name":                "neovim",
			"ide_version":             "0.10.2",
			"extension_name":          "codeium.nvim",
			"language_server_version": "1.20.9",
		},
	}

	upstream, cancel, err := windsurfCodeiumAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	var payload map[string]any
	if err := json.NewDecoder(upstream.Body).Decode(&payload); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	metadata, ok := objectValue(payload["metadata"])
	if !ok {
		t.Fatalf("metadata = %#v", payload["metadata"])
	}
	if got := metadataText(metadata["api_key"]); got != "ckey" {
		t.Fatalf("metadata.api_key = %q", got)
	}
	if got := metadataText(metadata["ide_name"]); got != "neovim" {
		t.Fatalf("metadata.ide_name = %q", got)
	}
	if got := metadataText(metadata["extension_name"]); got != "codeium.nvim" {
		t.Fatalf("metadata.extension_name = %q", got)
	}
	if got := metadataText(metadata["extension_version"]); got != "1.20.9" {
		t.Fatalf("metadata.extension_version = %q", got)
	}
	if got := metadataText(metadata["request_id"]); got != "windsurf-request-2" {
		t.Fatalf("metadata.request_id = %q", got)
	}
}

func TestCodexOfficialHTTPClientDisablesImplicitGoCompression(t *testing.T) {
	server := &Server{httpClient: &http.Client{Timeout: 120 * time.Second}}
	client, err := server.upstreamHTTPClient(routeInfo{ProviderType: "codex_compatible"})
	if err != nil {
		t.Fatalf("upstreamHTTPClient returned error: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	if !transport.DisableCompression {
		t.Fatal("DisableCompression = false")
	}
	if transport.DialTLSContext == nil {
		t.Fatal("DialTLSContext is nil")
	}
}

func TestCodexOfficialTLSFingerprintCanBeDisabled(t *testing.T) {
	server := &Server{httpClient: &http.Client{Timeout: 120 * time.Second}}
	client, err := server.upstreamHTTPClient(routeInfo{
		ProviderType: "codex_compatible",
		AccountMeta:  `{"codex":{"tls_fingerprint":"go"}}`,
	})
	if err != nil {
		t.Fatalf("upstreamHTTPClient returned error: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	if transport.DialTLSContext != nil {
		t.Fatal("DialTLSContext should be nil when Codex TLS fingerprinting is disabled")
	}
}

func TestGenericTLSFingerprintDefaultsForOfficialRoutes(t *testing.T) {
	server := &Server{httpClient: &http.Client{Timeout: 120 * time.Second}}
	route := routeInfo{ProviderType: "gemini_cli", AuthScheme: "bearer"}
	if got := routeTLSFingerprintProfile(route); got != "node24" {
		t.Fatalf("Gemini TLS profile = %q", got)
	}
	client, err := server.upstreamHTTPClient(route)
	if err != nil {
		t.Fatalf("upstreamHTTPClient returned error: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	if transport.DialTLSContext == nil {
		t.Fatal("DialTLSContext is nil")
	}

	disabled := routeInfo{ProviderType: "gemini_cli", AuthScheme: "bearer", AccountMeta: `{"tls":{"profile":"go"}}`}
	if got := routeTLSFingerprintProfile(disabled); got != "" {
		t.Fatalf("disabled Gemini TLS profile = %q", got)
	}
}

func TestCodexCompatibleNonOpenAIHostUsesSharedHTTPClient(t *testing.T) {
	shared := &http.Client{Timeout: 120 * time.Second}
	server := &Server{httpClient: shared}
	client, err := server.upstreamHTTPClient(routeInfo{
		BaseURL:      "https://relay.example.com/v1",
		ProviderType: "codex_compatible",
	})
	if err != nil {
		t.Fatalf("upstreamHTTPClient returned error: %v", err)
	}
	if client != shared {
		t.Fatal("expected non-OpenAI Codex-compatible host to use shared client")
	}
}

func TestUpstreamHTTPClientReusesPooledProxyClient(t *testing.T) {
	server := &Server{httpClient: &http.Client{Timeout: 120 * time.Second}}
	route := routeInfo{ProviderType: "openai", ProxyURL: "http://127.0.0.1:8080"}

	first, err := server.upstreamHTTPClient(route)
	if err != nil {
		t.Fatalf("first upstreamHTTPClient returned error: %v", err)
	}
	second, err := server.upstreamHTTPClient(route)
	if err != nil {
		t.Fatalf("second upstreamHTTPClient returned error: %v", err)
	}
	if first != second {
		t.Fatal("expected pooled proxy client reuse")
	}
}

func TestNormalizeProxyURLSupportsSocks5HAndUpgradesSocks5(t *testing.T) {
	normalized, err := normalizeProxyURL(" socks5://User:Pass@LOCALHOST:1080/ ")
	if err != nil {
		t.Fatalf("normalizeProxyURL returned error: %v", err)
	}
	if normalized != "socks5h://User:Pass@localhost:1080" {
		t.Fatalf("normalized proxy = %q", normalized)
	}
	normalized, err = normalizeProxyURL("socks5h://127.0.0.1:1080")
	if err != nil {
		t.Fatalf("normalizeProxyURL socks5h returned error: %v", err)
	}
	if normalized != "socks5h://127.0.0.1:1080" {
		t.Fatalf("normalized socks5h proxy = %q", normalized)
	}
}

func TestNormalizeProxyURLSupportsDirectMode(t *testing.T) {
	for _, raw := range []string{"direct", " none "} {
		normalized, err := normalizeProxyURL(raw)
		if err != nil {
			t.Fatalf("normalizeProxyURL(%q) returned error: %v", raw, err)
		}
		if normalized != "direct" {
			t.Fatalf("normalizeProxyURL(%q) = %q", raw, normalized)
		}
		parsed, err := parseProxyURL(raw)
		if err != nil {
			t.Fatalf("parseProxyURL(%q) returned error: %v", raw, err)
		}
		if parsed != nil {
			t.Fatalf("parseProxyURL(%q) = %#v, expected nil direct transport", raw, parsed)
		}
	}
	if got := proxyProtocolFromURL("direct"); got != "direct" {
		t.Fatalf("proxyProtocolFromURL direct = %q", got)
	}
}

func TestNormalizeProxyURLRejectsMissingHost(t *testing.T) {
	if _, err := normalizeProxyURL("socks5://"); err == nil {
		t.Fatal("expected missing host proxy URL to fail")
	}
	if _, err := normalizeProxyURL("ftp://127.0.0.1:21"); err == nil {
		t.Fatal("expected unsupported proxy scheme to fail")
	}
}

func TestClassifyNetworkErrorUnexpectedEOF(t *testing.T) {
	if got := classifyNetworkError(io.ErrUnexpectedEOF); got != "unexpected_eof" {
		t.Fatalf("classifyNetworkError = %q", got)
	}
	if got := upstreamFailureCode(io.ErrUnexpectedEOF); got != "upstream_unexpected_eof" {
		t.Fatalf("upstreamFailureCode = %q", got)
	}
}

func TestUpstreamStatusErrorCodeDetectsAnthropicThinkingSignature(t *testing.T) {
	body := []byte(`{"type":"error","error":{"message":"Expected thinking block to have a valid signature"}}`)
	if got := upstreamStatusErrorCode(http.StatusBadRequest, body); got != "anthropic_signature_error" {
		t.Fatalf("upstreamStatusErrorCode = %q", got)
	}
	if got := upstreamStatusErrorCode(http.StatusBadRequest, []byte(`{"error":{"message":"invalid model"}}`)); got != "upstream_rejected" {
		t.Fatalf("upstreamStatusErrorCode for generic 400 = %q", got)
	}
}

func TestCodexRustlsClientHelloSpecMatchesCapturedShape(t *testing.T) {
	if codexRustlsJA3Hash != "d39e1be3241d516b1f714bd47c2bc968" {
		t.Fatalf("codexRustlsJA3Hash = %q", codexRustlsJA3Hash)
	}
	spec := codexRustlsClientHelloSpec("api.openai.com")
	if got := spec.CipherSuites; len(got) != 31 || got[0] != 0x1302 || got[1] != 0x1303 || got[30] != 0x00ff {
		t.Fatalf("unexpected cipher suites: %#v", got)
	}
	if got := spec.CompressionMethods; len(got) != 1 || got[0] != 0 {
		t.Fatalf("CompressionMethods = %#v", got)
	}
	if len(spec.Extensions) != 11 {
		t.Fatalf("extension count = %d", len(spec.Extensions))
	}
	if _, ok := spec.Extensions[0].(*utls.SNIExtension); !ok {
		t.Fatalf("extension[0] = %T", spec.Extensions[0])
	}
	if _, ok := spec.Extensions[1].(*utls.SupportedPointsExtension); !ok {
		t.Fatalf("extension[1] = %T", spec.Extensions[1])
	}
	if _, ok := spec.Extensions[2].(*utls.SupportedCurvesExtension); !ok {
		t.Fatalf("extension[2] = %T", spec.Extensions[2])
	}
	if _, ok := spec.Extensions[10].(*utls.UtlsPaddingExtension); !ok {
		t.Fatalf("extension[10] = %T", spec.Extensions[10])
	}

	ipSpec := codexRustlsClientHelloSpec("127.0.0.1")
	if _, ok := ipSpec.Extensions[0].(*utls.SNIExtension); ok {
		t.Fatal("IP address ClientHello should omit SNI extension")
	}
}

func TestCodexUTLSDialerMatchesCapturedRustlsJA3(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	defer listener.Close()

	hello := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		header := make([]byte, 5)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		body := make([]byte, binary.BigEndian.Uint16(header[3:5]))
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		hello <- append(header, body...)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addr := net.JoinHostPort("localhost", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	_, _ = codexUTLSDialTLSContext(routeInfo{})(ctx, "tcp", addr)

	select {
	case raw := <-hello:
		ja3, hash := clientHelloJA3(t, raw)
		if hash != codexRustlsJA3Hash {
			t.Fatalf("JA3 hash = %q, ja3 = %q", hash, ja3)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for ClientHello")
	}
}

func clientHelloJA3(t *testing.T, record []byte) (string, string) {
	t.Helper()
	if len(record) < 5 || record[0] != 22 {
		t.Fatalf("not a TLS handshake record: %x", record[:minTestInt(len(record), 5)])
	}
	offset := 5
	if record[offset] != 1 {
		t.Fatalf("not a ClientHello: handshake type %d", record[offset])
	}
	offset += 4
	clientVersion := binary.BigEndian.Uint16(record[offset : offset+2])
	offset += 2 + 32

	sessionIDLength := int(record[offset])
	offset += 1 + sessionIDLength

	cipherSuiteLength := int(binary.BigEndian.Uint16(record[offset : offset+2]))
	offset += 2
	cipherSuites := make([]uint16, 0, cipherSuiteLength/2)
	for end := offset + cipherSuiteLength; offset < end; offset += 2 {
		cipherSuites = append(cipherSuites, binary.BigEndian.Uint16(record[offset:offset+2]))
	}

	compressionMethodLength := int(record[offset])
	offset += 1 + compressionMethodLength

	extensionLength := int(binary.BigEndian.Uint16(record[offset : offset+2]))
	offset += 2
	extensions := []uint16{}
	supportedGroups := []uint16{}
	ecPointFormats := []uint8{}
	for end := offset + extensionLength; offset+4 <= end; {
		extensionType := binary.BigEndian.Uint16(record[offset : offset+2])
		dataLength := int(binary.BigEndian.Uint16(record[offset+2 : offset+4]))
		dataStart := offset + 4
		dataEnd := dataStart + dataLength
		data := record[dataStart:dataEnd]
		extensions = append(extensions, extensionType)
		switch extensionType {
		case 10:
			supportedGroups = parseClientHelloUint16Vector(data)
		case 11:
			if len(data) > 0 {
				ecPointFormats = append(ecPointFormats, data[1:]...)
			}
		}
		offset = dataEnd
	}

	ja3 := strings.Join([]string{
		strconv.Itoa(int(clientVersion)),
		joinClientHelloUint16s(cipherSuites),
		joinClientHelloUint16s(extensions),
		joinClientHelloUint16s(supportedGroups),
		joinClientHelloUint8s(ecPointFormats),
	}, ",")
	sum := md5.Sum([]byte(ja3))
	return ja3, hex.EncodeToString(sum[:])
}

func parseClientHelloUint16Vector(data []byte) []uint16 {
	if len(data) < 2 {
		return nil
	}
	length := int(binary.BigEndian.Uint16(data[:2]))
	values := []uint16{}
	for offset, end := 2, minTestInt(2+length, len(data)); offset+1 < end; offset += 2 {
		value := binary.BigEndian.Uint16(data[offset : offset+2])
		if !isTLSGREASE(value) {
			values = append(values, value)
		}
	}
	return values
}

func joinClientHelloUint16s(values []uint16) string {
	parts := []string{}
	for _, value := range values {
		if isTLSGREASE(value) {
			continue
		}
		parts = append(parts, strconv.Itoa(int(value)))
	}
	return strings.Join(parts, "-")
}

func joinClientHelloUint8s(values []uint8) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(int(value)))
	}
	return strings.Join(parts, "-")
}

func isTLSGREASE(value uint16) bool {
	return value&0x0f0f == 0x0a0a && byte(value>>8) == byte(value)
}

func minTestInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestCodexOfficialWebSocketAddsResponsesBeta(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://relay.local/v1/responses?session_id=codex-session", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "turn-1"))
	req.Header.Set("OpenAI-Beta", "realtime=v1")
	route := routeInfo{
		BaseURL:      "https://chatgpt.test/backend-api/codex",
		ProviderType: "codex_compatible",
		AccountID:    "relay-account-1",
		APIKey:       "cxtok",
	}

	upstreamURL, headers, dialer, err := codexCompatibleAdapter{}.PrepareWebSocket(req, route)
	if err != nil {
		t.Fatalf("PrepareWebSocket returned error: %v", err)
	}
	if upstreamURL != "wss://chatgpt.test/backend-api/codex/responses" {
		t.Fatalf("upstreamURL = %q", upstreamURL)
	}
	if dialer == nil || dialer.NetDialTLSContext == nil {
		t.Fatalf("Codex websocket dialer did not install uTLS dialer: %#v", dialer)
	}
	if got := headers.Get("Authorization"); got != "Bearer cxtok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := headers.Get("originator"); got != "codex_exec" {
		t.Fatalf("originator = %q", got)
	}
	if got := headers.Get("version"); got != "0.0.0" {
		t.Fatalf("version = %q", got)
	}
	if got := headers.Get("session_id"); got != "codex-session" {
		t.Fatalf("session_id = %q", got)
	}
	if got := headers.Get("x-client-request-id"); got != "codex-session" {
		t.Fatalf("x-client-request-id = %q", got)
	}
	if got := headers.Get("x-codex-installation-id"); got != "" {
		t.Fatalf("x-codex-installation-id should be absent for websocket handshake, got %q", got)
	}
	if got := headers.Get("X-Request-Id"); got != "" {
		t.Fatalf("X-Request-Id should be absent for Codex websocket, got %q", got)
	}
	if got := headers.Get("x-codex-window-id"); got != "codex-session:0" {
		t.Fatalf("x-codex-window-id = %q", got)
	}
	if got := headers.Get("x-codex-turn-metadata"); !strings.Contains(got, `"session_id":"codex-session"`) {
		t.Fatalf("x-codex-turn-metadata = %q", got)
	}
	if got := headers.Get("x-codex-turn-metadata"); !strings.Contains(got, `"sandbox":"seccomp"`) {
		t.Fatalf("x-codex-turn-metadata = %q", got)
	}
	if got := headers.Get("x-codex-turn-metadata"); !strings.Contains(got, `"turn_id":"turn-1"`) {
		t.Fatalf("x-codex-turn-metadata should carry relay request id as turn id, got %q", got)
	}
	if got := headers.Get("Accept"); got != "" {
		t.Fatalf("Accept should be absent for Codex websocket, got %q", got)
	}
	if got := headers.Get("User-Agent"); got != "" {
		t.Fatalf("User-Agent should be absent for Codex websocket, got %q", got)
	}
	if got := headers.Get("OpenAI-Beta"); got != "responses_websockets=2026-02-06" {
		t.Fatalf("OpenAI-Beta = %q", got)
	}
}

func TestCodexOfficialWebSocketResponseCreateAddsClientMetadata(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://relay.local/v1/responses?session_id=codex-session", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "turn-1"))
	req.Header.Set("x-codex-turn-metadata", `{"session_id":"codex-session","thread_source":"user","turn_id":"turn-123","sandbox":"seccomp"}`)
	req.Header.Set("x-openai-subagent", "review")
	req.Header.Set("x-codex-parent-thread-id", "parent-thread-1")
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00")
	req.Header.Set("tracestate", "vendor=value")
	route := routeInfo{
		BaseURL:       "https://chatgpt.test/backend-api/codex",
		ProviderType:  "codex_compatible",
		AccountID:     "relay-account-1",
		UpstreamModel: "gpt-upstream",
		TokenMetadata: map[string]any{
			"installation_id": "install-1",
		},
	}

	rewritten := rewriteCodexOfficialWebSocketFrame(req, route, []byte(`{"type":"response.create","model":"public-alias","input":[]}`))
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("rewritten frame is not JSON: %v", err)
	}
	if got := payload["model"]; got != "gpt-upstream" {
		t.Fatalf("model = %#v", got)
	}
	clientMetadata, ok := payload["client_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("client_metadata = %#v", payload["client_metadata"])
	}
	expected := map[string]string{
		"x-codex-installation-id":       "install-1",
		"x-codex-window-id":             "codex-session:0",
		"x-openai-subagent":             "review",
		"x-codex-parent-thread-id":      "parent-thread-1",
		"ws_request_header_traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
		"ws_request_header_tracestate":  "vendor=value",
	}
	for key, value := range expected {
		if got := clientMetadata[key]; got != value {
			t.Fatalf("%s = %v", key, got)
		}
	}
	turnMetadata, ok := clientMetadata["x-codex-turn-metadata"].(string)
	if !ok || !strings.Contains(turnMetadata, `"session_id":"codex-session"`) || !strings.Contains(turnMetadata, `"turn_id":"turn-123"`) {
		t.Fatalf("x-codex-turn-metadata = %#v", clientMetadata["x-codex-turn-metadata"])
	}
	if got := payload["stream"]; got != true {
		t.Fatalf("stream = %#v", got)
	}
	if got := payload["prompt_cache_key"]; got != "codex-session" {
		t.Fatalf("prompt_cache_key = %#v", got)
	}
}

func TestAnthropicAdapterUsesBearerForOAuthBundles(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{
		BaseURL:       "https://api.anthropic.test",
		ProviderType:  "anthropic",
		APIKey:        "ot",
		AuthScheme:    "bearer",
		UpstreamModel: "claude-test",
	}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, []byte(`{"model":"claude-test"}`))
	defer cancel()
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer ot" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("X-API-Key"); got != "" {
		t.Fatalf("X-API-Key should not be set for OAuth, got %q", got)
	}
	if got := upstream.Header.Get("Anthropic-Beta"); got != "oauth-2025-04-20" {
		t.Fatalf("Anthropic-Beta = %q", got)
	}
	if got := upstream.Header.Get("Accept-Encoding"); got != "identity" {
		t.Fatalf("Accept-Encoding = %q", got)
	}
}

func TestAnthropicAdapterAppliesClaudeCodeOfficialHeadersForOAuth(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req-claude-1"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Beta", "context-1m-2025-08-07")
	req.Header.Set("X-Relay-Session", "claude-session")
	route := routeInfo{
		BaseURL:       "https://api.anthropic.com",
		ProviderType:  "anthropic",
		AuthMode:      "claude_cli",
		TokenProvider: "anthropic_claude",
		APIKey:        "claudetok",
		AuthScheme:    "bearer",
		TokenMetadata: map[string]any{
			"client_version":                "2.1.88",
			"user_type":                     "external",
			"entrypoint":                    "cli",
			"client_app":                    "relay-sdk/1.0",
			"agent_sdk_version":             "0.2.0",
			"workload":                      "cron",
			"claude_code_container_id":      "container-1",
			"claude_code_remote_session_id": "remote-1",
			"additional_protection":         true,
			"extra_betas":                   []any{"fine-grained-tool-streaming-2025-05-14"},
		},
	}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, []byte(`{"model":"claude-test"}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()
	if got := upstream.Header.Get("Authorization"); got != "Bearer claudetok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("X-API-Key"); got != "" {
		t.Fatalf("X-API-Key should not be set for OAuth, got %q", got)
	}
	if got := upstream.Header.Get("x-app"); got != "cli" {
		t.Fatalf("x-app = %q", got)
	}
	if got := upstream.Header.Get("User-Agent"); got != "claude-cli/2.1.88 (external, cli, agent-sdk/0.2.0, client-app/relay-sdk/1.0, workload/cron)" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := upstream.Header.Get("x-client-app"); got != "relay-sdk/1.0" {
		t.Fatalf("x-client-app = %q", got)
	}
	if got := upstream.Header.Get("x-claude-remote-container-id"); got != "container-1" {
		t.Fatalf("x-claude-remote-container-id = %q", got)
	}
	if got := upstream.Header.Get("x-claude-remote-session-id"); got != "remote-1" {
		t.Fatalf("x-claude-remote-session-id = %q", got)
	}
	if got := upstream.Header.Get("x-anthropic-additional-protection"); got != "true" {
		t.Fatalf("x-anthropic-additional-protection = %q", got)
	}
	if got := upstream.Header.Get("X-Claude-Code-Session-Id"); got != "claude-session" {
		t.Fatalf("X-Claude-Code-Session-Id = %q", got)
	}
	if got := upstream.Header.Get("x-client-request-id"); got != "" {
		t.Fatalf("x-client-request-id should not be synthesized for Claude Code, got %q", got)
	}
	if got := upstream.Header.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q", got)
	}
	if got := upstream.Header.Get("x-stainless-lang"); got != "js" {
		t.Fatalf("x-stainless-lang = %q", got)
	}
	if got := upstream.Header.Get("x-stainless-package-version"); got != "0.81.0" {
		t.Fatalf("x-stainless-package-version = %q", got)
	}
	if got := upstream.Header.Get("x-stainless-runtime"); got != "node" {
		t.Fatalf("x-stainless-runtime = %q", got)
	}
	if got := upstream.Header.Get("anthropic-dangerous-direct-browser-access"); got != "true" {
		t.Fatalf("anthropic-dangerous-direct-browser-access = %q", got)
	}
	if got := upstream.Header.Get("accept-language"); got != "*" {
		t.Fatalf("accept-language = %q", got)
	}
	if got := upstream.Header.Get("sec-fetch-mode"); got != "cors" {
		t.Fatalf("sec-fetch-mode = %q", got)
	}
	beta := upstream.Header.Get("Anthropic-Beta")
	for _, expected := range []string{
		"context-1m-2025-08-07",
		"oauth-2025-04-20",
		"claude-code-20250219",
		"interleaved-thinking-2025-05-14",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"effort-2025-11-24",
		"fine-grained-tool-streaming-2025-05-14",
	} {
		if !strings.Contains(","+beta+",", ","+expected+",") {
			t.Fatalf("Anthropic-Beta %q missing %q", beta, expected)
		}
	}

	outboundBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(outboundBody, &payload); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if got := payload["stream"]; got != true {
		t.Fatalf("stream = %#v", got)
	}
	if got := payload["max_tokens"]; got != float64(32000) {
		t.Fatalf("max_tokens = %#v", got)
	}
	thinking, ok := payload["thinking"].(map[string]any)
	if !ok || thinking["type"] != "adaptive" {
		t.Fatalf("thinking = %#v", payload["thinking"])
	}
	contextManagement, ok := payload["context_management"].(map[string]any)
	if !ok {
		t.Fatalf("context_management = %#v", payload["context_management"])
	}
	edits, ok := contextManagement["edits"].([]any)
	if !ok || len(edits) != 1 {
		t.Fatalf("context_management.edits = %#v", contextManagement["edits"])
	}
	edit, ok := edits[0].(map[string]any)
	if !ok || edit["type"] != "clear_thinking_20251015" || edit["keep"] != "all" {
		t.Fatalf("context_management.edits[0] = %#v", edits[0])
	}
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %#v", payload["metadata"])
	}
	userID, ok := metadata["user_id"].(string)
	if !ok {
		t.Fatalf("metadata.user_id = %#v", metadata["user_id"])
	}
	var userIDPayload map[string]string
	if err := json.Unmarshal([]byte(userID), &userIDPayload); err != nil {
		t.Fatalf("unmarshal metadata.user_id: %v", err)
	}
	if got := userIDPayload["session_id"]; got != "claude-session" {
		t.Fatalf("metadata.user_id.session_id = %q", got)
	}
	system, ok := payload["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatalf("system = %#v", payload["system"])
	}
	firstSystem, ok := system[0].(map[string]any)
	firstSystemText, textOK := firstSystem["text"].(string)
	if !ok || !textOK || !strings.Contains(firstSystemText, "cc_version=2.1.88.e50; cc_entrypoint=cli; cch=00000;") {
		t.Fatalf("system[0] = %#v", system[0])
	}
}

func TestAnthropicAdapterUsesCapturedClaudeCodeDefaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req-claude-default"))
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{
		BaseURL:       "https://api.anthropic.com",
		ProviderType:  "anthropic",
		AuthMode:      "claude_cli",
		TokenProvider: "anthropic_claude",
		APIKey:        "claudetok",
		AuthScheme:    "bearer",
	}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, []byte(`{"model":"claude-test"}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()
	if got := upstream.Header.Get("User-Agent"); got != "claude-cli/2.1.104 (external, cli)" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := upstream.Header.Get("Anthropic-Beta"); got != "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,effort-2025-11-24" {
		t.Fatalf("Anthropic-Beta = %q", got)
	}
	if got := upstream.Header.Get("Accept-Encoding"); got != "gzip, deflate" {
		t.Fatalf("Accept-Encoding = %q", got)
	}
	if got := upstream.Header.Get("x-stainless-runtime-version"); got != "v22.22.1" {
		t.Fatalf("x-stainless-runtime-version = %q", got)
	}
	if got := upstream.URL.RawQuery; got != "beta=true" {
		t.Fatalf("RawQuery = %q", got)
	}
	if got := upstream.Header.Get("X-Request-Id"); got != "" {
		t.Fatalf("X-Request-Id should be absent for Claude Code routes, got %q", got)
	}

	outboundBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(outboundBody, &payload); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	system, ok := payload["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatalf("system = %#v", payload["system"])
	}
	firstSystem, ok := system[0].(map[string]any)
	if !ok || firstSystem["text"] != "x-anthropic-billing-header: cc_version=2.1.104.e50; cc_entrypoint=cli; cch=00000;" {
		t.Fatalf("system[0] = %#v", system[0])
	}
}

func TestAnthropicAdapterUsesStableClaudeDeviceFingerprintAcrossSessions(t *testing.T) {
	body := []byte(`{"model":"claude-test"}`)
	route := claudeCodeTestRoute()
	route.AccountID = "account-route-1"
	route.TokenSubject = ""

	reqA := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	reqA = reqA.WithContext(context.WithValue(reqA.Context(), requestIDKey, "req-session-a"))
	reqA.Header.Set("Content-Type", "application/json")
	upstreamA, cancelA, err := anthropicAdapter{}.PrepareRequest(reqA, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest A returned error: %v", err)
	}
	defer cancelA()

	reqB := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	reqB = reqB.WithContext(context.WithValue(reqB.Context(), requestIDKey, "req-session-b"))
	reqB.Header.Set("Content-Type", "application/json")
	upstreamB, cancelB, err := anthropicAdapter{}.PrepareRequest(reqB, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest B returned error: %v", err)
	}
	defer cancelB()

	userIDA := claudeMetadataUserIDFromPreparedRequest(t, upstreamA)
	userIDB := claudeMetadataUserIDFromPreparedRequest(t, upstreamB)
	if userIDA["device_id"] == "" || userIDA["device_id"] != userIDB["device_id"] {
		t.Fatalf("device_id should be stable across sessions: A=%q B=%q", userIDA["device_id"], userIDB["device_id"])
	}
	if userIDA["device_id"] != claudeCodeDeviceID(route) {
		t.Fatalf("device_id = %q, expected %q", userIDA["device_id"], claudeCodeDeviceID(route))
	}
	if userIDA["session_id"] == userIDB["session_id"] {
		t.Fatalf("session_id should remain request/session scoped: A=%q B=%q", userIDA["session_id"], userIDB["session_id"])
	}
}

func TestAnthropicAdapterUsesImportedClaudeFingerprintMetadata(t *testing.T) {
	body := []byte(`{"model":"claude-test"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	route := claudeCodeTestRoute()
	route.TokenMetadata = map[string]any{
		"device_id":    "imported-device",
		"account_uuid": "imported-account",
		"platform":     "darwin",
		"arch":         "arm64",
	}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	userID := claudeMetadataUserIDFromPreparedRequest(t, upstream)
	if got := userID["device_id"]; got != "imported-device" {
		t.Fatalf("metadata.user_id.device_id = %q", got)
	}
	if got := userID["account_uuid"]; got != "imported-account" {
		t.Fatalf("metadata.user_id.account_uuid = %q", got)
	}
	if got := upstream.Header.Get("x-stainless-os"); got != "MacOS" {
		t.Fatalf("x-stainless-os = %q", got)
	}
	if got := upstream.Header.Get("x-stainless-arch"); got != "arm64" {
		t.Fatalf("x-stainless-arch = %q", got)
	}
}

func TestClaudeRuntimeFingerprintMetadataFeedsAdapter(t *testing.T) {
	route := claudeCodeTestRoute()
	route.AccountID = "account-runtime-1"
	route.TokenSubject = ""
	metadata, changed := mergeClaudeRuntimeFingerprintMetadata(route, time.Date(2026, 5, 16, 1, 2, 3, 0, time.UTC))
	if !changed {
		t.Fatal("mergeClaudeRuntimeFingerprintMetadata did not create fingerprint metadata")
	}
	route.RuntimeMeta = mustEncodeJSON(metadata)

	deviceID := claudeCodeDeviceID(route)
	if len(deviceID) != 64 {
		t.Fatalf("runtime device_id length = %d, value = %q", len(deviceID), deviceID)
	}
	if _, err := hex.DecodeString(deviceID); err != nil {
		t.Fatalf("runtime device_id is not hex: %v", err)
	}
	clientID := routeMetadataString(route, "claude", "client_id")
	if len(clientID) != 36 || clientID[8] != '-' || clientID[13] != '-' || clientID[18] != '-' || clientID[23] != '-' {
		t.Fatalf("runtime client_id = %q", clientID)
	}
	if got := routeMetadataString(route, "claude", "fingerprint_created_at"); got != "2026-05-16T01:02:03Z" {
		t.Fatalf("fingerprint_created_at = %q", got)
	}

	body := []byte(`{"model":"claude-test"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	userID := claudeMetadataUserIDFromPreparedRequest(t, upstream)
	if got := userID["device_id"]; got != deviceID {
		t.Fatalf("metadata.user_id.device_id = %q, expected %q", got, deviceID)
	}
	if got := upstream.Header.Get("User-Agent"); got != "claude-cli/2.1.104 (external, cli)" {
		t.Fatalf("User-Agent = %q", got)
	}
}

func TestGenericRuntimeFingerprintMetadataFeedsOfficialAdapters(t *testing.T) {
	route := routeInfo{
		ProviderType:  "gemini_cli",
		AuthScheme:    "bearer",
		AccountID:     "gemini-runtime-1",
		UpstreamModel: "gemini-2.5-pro",
	}
	metadata := map[string]any{}
	changed := mergeGenericRuntimeFingerprintMetadata(route, metadata, time.Date(2026, 5, 16, 1, 2, 3, 0, time.UTC))
	if !changed {
		t.Fatal("mergeGenericRuntimeFingerprintMetadata did not create metadata")
	}
	route.RuntimeMeta = mustEncodeJSON(metadata)
	if got := routeMetadataString(route, "gemini", "tls_profile"); got != "node24" {
		t.Fatalf("runtime gemini tls_profile = %q", got)
	}
	if got := routeMetadataString(route, "gemini", "user_agent"); !strings.Contains(got, "GeminiCLI/") {
		t.Fatalf("runtime gemini user_agent = %q", got)
	}
	official, ok := objectValue(metadata["official_client"])
	if !ok {
		t.Fatalf("official_client = %#v", metadata["official_client"])
	}
	fingerprint, ok := objectValue(official["fingerprint"])
	if !ok {
		t.Fatalf("official_client.fingerprint = %#v", official["fingerprint"])
	}
	if got := metadataText(fingerprint["ja3_hash"]); got != node24JA3Hash {
		t.Fatalf("ja3_hash = %q", got)
	}
}

func TestClaudeExplicitMetadataOverridesRuntimeFingerprint(t *testing.T) {
	body := []byte(`{"model":"claude-test"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	route := claudeCodeTestRoute()
	route.RuntimeMeta = `{"claude":{"device_id":"runtime-device","user_agent":"claude-cli/2.2.3 (external, cli)"}}`
	route.TokenMetadata = map[string]any{
		"device_id":  "token-device",
		"user_agent": "claude-cli/2.3.0 (external, cli)",
	}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	payload := claudePreparedPayloadFromRequest(t, upstream)
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %#v", payload["metadata"])
	}
	userIDText, ok := metadata["user_id"].(string)
	if !ok {
		t.Fatalf("metadata.user_id = %#v", metadata["user_id"])
	}
	var userID map[string]string
	if err := json.Unmarshal([]byte(userIDText), &userID); err != nil {
		t.Fatalf("unmarshal metadata.user_id: %v", err)
	}
	if got := userID["device_id"]; got != "token-device" {
		t.Fatalf("metadata.user_id.device_id = %q", got)
	}
	if got := upstream.Header.Get("User-Agent"); got != "claude-cli/2.3.0 (external, cli)" {
		t.Fatalf("User-Agent = %q", got)
	}
	system, ok := payload["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatalf("system = %#v", payload["system"])
	}
	firstSystem, ok := system[0].(map[string]any)
	if !ok {
		t.Fatalf("system[0] = %#v", system[0])
	}
	systemText, ok := firstSystem["text"].(string)
	if !ok {
		t.Fatalf("system[0].text = %#v", firstSystem["text"])
	}
	if !strings.Contains(systemText, "cc_version=2.3.0.e50;") {
		t.Fatalf("billing system text = %q", systemText)
	}
}

func TestAnthropicAdapterSyncsBillingVersionFromUserAgent(t *testing.T) {
	body := []byte(`{"model":"claude-test"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	route := claudeCodeTestRoute()
	route.TokenMetadata = map[string]any{
		"user_agent": "claude-cli/2.2.3 (external, cli)",
	}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("User-Agent"); got != "claude-cli/2.2.3 (external, cli)" {
		t.Fatalf("User-Agent = %q", got)
	}
	systemText := claudeFirstSystemTextFromPreparedRequest(t, upstream)
	if !strings.Contains(systemText, "cc_version=2.2.3.e50;") {
		t.Fatalf("billing system text = %q", systemText)
	}
}

func TestAnthropicAdapterSignsBillingCCHWhenEnabled(t *testing.T) {
	body := []byte(`{"model":"claude-test"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	route := claudeCodeTestRoute()
	route.TokenMetadata = map[string]any{"sign_billing_cch": true}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	systemText := claudeFirstSystemTextFromPreparedRequest(t, upstream)
	cch := claudeBillingCCHFromSystemText(t, systemText)
	if cch == "00000" {
		t.Fatalf("cch was not signed: %q", systemText)
	}
	if len(cch) != 5 {
		t.Fatalf("cch length = %d, value = %q", len(cch), cch)
	}
	if _, err := strconv.ParseUint(cch, 16, 20); err != nil {
		t.Fatalf("cch is not 5 hex digits: %q", cch)
	}
}

func TestAnthropicAdapterSignsOnlyBillingCCHPlaceholder(t *testing.T) {
	body := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"literal cch=00000 should stay"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	route := claudeCodeTestRoute()
	route.TokenMetadata = map[string]any{"sign_billing_cch": true}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	outboundBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	if !bytes.Contains(outboundBody, []byte("literal cch=00000 should stay")) {
		t.Fatalf("user cch placeholder was rewritten: %s", outboundBody)
	}
	var payload map[string]any
	if err := json.Unmarshal(outboundBody, &payload); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	system := payload["system"].([]any)
	systemText := system[0].(map[string]any)["text"].(string)
	if cch := claudeBillingCCHFromSystemText(t, systemText); cch == "00000" {
		t.Fatalf("billing cch was not signed: %q", systemText)
	}
}

func TestAnthropicAdapterAppliesClaudeBetaPolicy(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Beta", "context-1m-2025-08-07")
	route := claudeCodeTestRoute()
	route.TokenMetadata = map[string]any{
		"drop_betas": []any{"effort-2025-11-24", "prompt-caching-scope-2026-01-05"},
	}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, []byte(`{"model":"claude-test"}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	beta := upstream.Header.Get("Anthropic-Beta")
	for _, dropped := range []string{"effort-2025-11-24", "prompt-caching-scope-2026-01-05"} {
		if strings.Contains(","+beta+",", ","+dropped+",") {
			t.Fatalf("Anthropic-Beta %q still contains dropped beta %q", beta, dropped)
		}
	}
	for _, expected := range []string{"context-1m-2025-08-07", "claude-code-20250219", "oauth-2025-04-20"} {
		if !strings.Contains(","+beta+",", ","+expected+",") {
			t.Fatalf("Anthropic-Beta %q missing %q", beta, expected)
		}
	}
}

func TestAnthropicAdapterBlocksClaudeBetaPolicy(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Beta", "context-1m-2025-08-07")
	route := claudeCodeTestRoute()
	route.TokenMetadata = map[string]any{
		"blocked_betas": []any{"context-1m-2025-08-07"},
	}

	_, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, []byte(`{"model":"claude-test"}`))
	defer cancel()
	if err == nil {
		t.Fatal("expected PrepareRequest to reject blocked beta")
	}
	var appErr appError
	if !errors.As(err, &appErr) || appErr.status != http.StatusBadRequest {
		t.Fatalf("error = %#v", err)
	}
}

func TestGlobalAnthropicBetaPolicyFiltersFinalTokens(t *testing.T) {
	header := http.Header{}
	header.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20,effort-2025-11-24")
	policy := anthropicBetaPolicySettings{
		Enabled:      true,
		Mode:         "filter_unlisted",
		Scope:        []string{"oauth"},
		AllowedBetas: []string{"claude-code-20250219", "oauth-2025-04-20"},
	}
	if err := applyGlobalAnthropicBetaPolicy(header, claudeCodeTestRoute(), policy); err != nil {
		t.Fatalf("applyGlobalAnthropicBetaPolicy returned error: %v", err)
	}
	if got := header.Get("Anthropic-Beta"); got != "claude-code-20250219,oauth-2025-04-20" {
		t.Fatalf("Anthropic-Beta = %q", got)
	}
}

func TestGlobalAnthropicBetaPolicyBlocksFinalTokens(t *testing.T) {
	header := http.Header{}
	header.Set("Anthropic-Beta", "claude-code-20250219,context-1m-2025-08-07")
	policy := anthropicBetaPolicySettings{
		Enabled:      true,
		BlockedBetas: []string{"context-1m-2025-08-07"},
	}
	err := applyGlobalAnthropicBetaPolicy(header, claudeCodeTestRoute(), policy)
	if err == nil {
		t.Fatal("expected global beta policy to reject blocked beta")
	}
}

func TestAnthropicAdapterStripsCacheControlFromThinkingBlocks(t *testing.T) {
	body := []byte(`{
		"model":"claude-test",
		"messages":[{
			"role":"assistant",
			"content":[
				{"type":"thinking","thinking":"plan","signature":"sig","cache_control":{"type":"ephemeral"}},
				{"type":"text","text":"visible","cache_control":{"type":"ephemeral"}}
			]
		}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, claudeCodeTestRoute(), body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	outboundBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(outboundBody, &payload); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	messages := payload["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	thinking := content[0].(map[string]any)
	if _, ok := thinking["cache_control"]; ok {
		t.Fatalf("thinking cache_control was not stripped: %#v", thinking)
	}
	text := content[1].(map[string]any)
	if _, ok := text["cache_control"]; !ok {
		t.Fatalf("text cache_control should be preserved: %#v", text)
	}
}

func TestClaudeSignatureRepairFiltersThinkingBlocksWhenEnabled(t *testing.T) {
	body := []byte(`{
		"model":"claude-test",
		"messages":[{
			"role":"assistant",
			"content":[
				{"type":"thinking","thinking":"plan","signature":"sig"},
				{"type":"redacted_thinking","data":"opaque"},
				{"type":"text","text":"visible"}
			]
		}]
	}`)
	route := claudeCodeTestRoute()
	route.TokenMetadata = map[string]any{"signature_repair_retry": true}
	errBody := []byte(`{"type":"error","error":{"message":"Expected thinking block to have a valid signature"}}`)

	if !shouldRetryClaudeSignatureRepair(route, http.StatusBadRequest, errBody) {
		t.Fatal("expected signature repair retry to be enabled")
	}
	repaired, changed := repairClaudeThinkingBlocksForRetry(body)
	if !changed {
		t.Fatal("expected repair to change body")
	}
	var payload map[string]any
	if err := json.Unmarshal(repaired, &payload); err != nil {
		t.Fatalf("unmarshal repaired body: %v", err)
	}
	messages := payload["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %#v", content)
	}
	if blockType := content[0].(map[string]any)["type"]; blockType != "text" {
		t.Fatalf("remaining block type = %#v", blockType)
	}
}

func TestAnthropicAdapterForwardsClaudeCountTokens(t *testing.T) {
	body := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, claudeCodeTestRoute(), body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.URL.Path; got != "/v1/messages/count_tokens" {
		t.Fatalf("URL.Path = %q", got)
	}
	if got := upstream.URL.RawQuery; got != "beta=true" {
		t.Fatalf("RawQuery = %q", got)
	}
	beta := upstream.Header.Get("Anthropic-Beta")
	for _, expected := range []string{"claude-code-20250219", "oauth-2025-04-20", "token-counting-2024-11-01"} {
		if !strings.Contains(","+beta+",", ","+expected+",") {
			t.Fatalf("Anthropic-Beta %q missing %q", beta, expected)
		}
	}

	outboundBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(outboundBody, &payload); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	for _, unexpected := range []string{"stream", "max_tokens", "thinking", "context_management"} {
		if _, ok := payload[unexpected]; ok {
			t.Fatalf("%s should not be synthesized for count_tokens: %#v", unexpected, payload[unexpected])
		}
	}
	if _, ok := payload["metadata"].(map[string]any); !ok {
		t.Fatalf("metadata = %#v", payload["metadata"])
	}
	if _, ok := payload["system"].([]any); !ok {
		t.Fatalf("system = %#v", payload["system"])
	}
}

func TestAnthropicAdapterAppliesClaudeCodeFilesHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/files?after_created_at=2026-05-06T00%3A00%3A00Z", nil)
	route := claudeCodeTestRoute()

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, nil)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.URL.String(); got != "https://api.anthropic.com/v1/files?after_created_at=2026-05-06T00%3A00%3A00Z" {
		t.Fatalf("URL = %q", got)
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer claudetok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q", got)
	}
	if got := upstream.Header.Get("Anthropic-Beta"); got != "files-api-2025-04-14,oauth-2025-04-20" {
		t.Fatalf("Anthropic-Beta = %q", got)
	}
	if got := upstream.Header.Get("Accept"); got != "application/json, text/plain, */*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := upstream.Header.Get("x-stainless-lang"); got != "js" {
		t.Fatalf("x-stainless-lang = %q", got)
	}
	if got := upstream.Header.Get("Accept-Encoding"); got != "identity" {
		t.Fatalf("Accept-Encoding = %q", got)
	}
	if got := upstream.Header.Get("User-Agent"); got != "" {
		t.Fatalf("User-Agent should not be synthesized for files requests, got %q", got)
	}
	if got := upstream.URL.RawQuery; strings.Contains(got, "beta=true") {
		t.Fatalf("RawQuery should not include messages beta flag, got %q", got)
	}
}

func TestAnthropicAdapterAppliesClaudeCodeMCPServersHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/mcp_servers?limit=1000", nil)
	route := claudeCodeTestRoute()

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, nil)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("Authorization"); got != "Bearer claudetok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := upstream.Header.Get("Accept"); got != "application/json, text/plain, */*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := upstream.Header.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q", got)
	}
	if got := upstream.Header.Get("Anthropic-Beta"); got != "mcp-servers-2025-12-04" {
		t.Fatalf("Anthropic-Beta = %q", got)
	}
	if got := upstream.Header.Get("x-stainless-runtime-version"); got != "v22.22.1" {
		t.Fatalf("x-stainless-runtime-version = %q", got)
	}
	if got := upstream.URL.RawQuery; got != "limit=1000" {
		t.Fatalf("RawQuery = %q", got)
	}
}

func TestAnthropicAdapterAppliesClaudeCodeSessionsHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session_123/events", strings.NewReader(`{"events":[]}`))
	req.Header.Set("Content-Type", "application/json")
	route := claudeCodeTestRoute()
	route.TokenMetadata["organization_uuid"] = "org-123"

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, []byte(`{"events":[]}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("Authorization"); got != "Bearer claudetok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := upstream.Header.Get("Accept"); got != "application/json, text/plain, */*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := upstream.Header.Get("Anthropic-Beta"); got != "ccr-byoc-2025-07-29" {
		t.Fatalf("Anthropic-Beta = %q", got)
	}
	if got := upstream.Header.Get("x-organization-uuid"); got != "org-123" {
		t.Fatalf("x-organization-uuid = %q", got)
	}
	outboundBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	if got := string(outboundBody); got != `{"events":[]}` {
		t.Fatalf("body = %q", got)
	}
}

func TestAnthropicAdapterAppliesClaudeCodeEnvironmentsHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/environments/bridge", strings.NewReader(`{"machine_name":"dev"}`))
	req.Header.Set("Content-Type", "application/json")
	route := claudeCodeTestRoute()
	route.TokenMetadata["runner_version"] = "2.1.104"
	route.TokenMetadata["trusted_device_token"] = "trusted-token"

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, []byte(`{"machine_name":"dev"}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("Anthropic-Beta"); got != "environments-2025-11-01" {
		t.Fatalf("Anthropic-Beta = %q", got)
	}
	if got := upstream.Header.Get("x-environment-runner-version"); got != "2.1.104" {
		t.Fatalf("x-environment-runner-version = %q", got)
	}
	if got := upstream.Header.Get("X-Trusted-Device-Token"); got != "trusted-token" {
		t.Fatalf("X-Trusted-Device-Token = %q", got)
	}
}

func TestAnthropicAdapterAppliesClaudeCodeEnvironmentProvidersHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/environment_providers", nil)
	route := claudeCodeTestRoute()
	route.TokenMetadata["organization_uuid"] = "org-123"

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, nil)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("Authorization"); got != "Bearer claudetok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := upstream.Header.Get("Accept"); got != "application/json, text/plain, */*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := upstream.Header.Get("x-organization-uuid"); got != "org-123" {
		t.Fatalf("x-organization-uuid = %q", got)
	}
	if got := upstream.Header.Get("Anthropic-Beta"); got != "" {
		t.Fatalf("Anthropic-Beta = %q", got)
	}
}

func TestAnthropicAdapterAppliesClaudeCodeOAuthProfileHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/profile", nil)
	route := claudeCodeTestRoute()

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, nil)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("Authorization"); got != "Bearer claudetok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := upstream.Header.Get("Accept"); got != "application/json, text/plain, */*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := upstream.Header.Get("Anthropic-Beta"); got != "" {
		t.Fatalf("Anthropic-Beta = %q", got)
	}
	if got := upstream.Header.Get("x-stainless-lang"); got != "" {
		t.Fatalf("x-stainless-lang should not be synthesized for OAuth API requests, got %q", got)
	}
}

func TestAnthropicAdapterAppliesClaudeCodeOAuthFileContentHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/files/file_uuid/content", nil)
	route := claudeCodeTestRoute()

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, nil)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("Authorization"); got != "Bearer claudetok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("Content-Type"); got != "" {
		t.Fatalf("Content-Type should not be synthesized for OAuth file content, got %q", got)
	}
	if got := upstream.Header.Get("Accept"); got != "application/json, text/plain, */*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := upstream.Header.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q", got)
	}
}

func TestAnthropicAdapterAppliesClaudeCodeOAuthMultipartUploadHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/file_upload", strings.NewReader("--boundary"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	route := claudeCodeTestRoute()

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, []byte("--boundary"))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("Authorization"); got != "Bearer claudetok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("Content-Type"); got != "multipart/form-data; boundary=boundary" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := upstream.Header.Get("Accept"); got != "application/json, text/plain, */*" {
		t.Fatalf("Accept = %q", got)
	}
}

func TestAnthropicAdapterAppliesClaudeCodeAPIKeyProfileHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/claude_cli_profile?account_uuid=account-123", nil)
	route := claudeCodeTestRoute()

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, nil)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	if got := upstream.Header.Get("Authorization"); got != "Bearer claudetok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("Content-Type"); got != "" {
		t.Fatalf("Content-Type should not be synthesized for claude_cli_profile, got %q", got)
	}
	if got := upstream.Header.Get("Accept"); got != "application/json, text/plain, */*" {
		t.Fatalf("Accept = %q", got)
	}
}

func TestAnthropicAdapterClaudeCodeBillingHeaderIncludesWorkload(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
	route := routeInfo{
		BaseURL:       "https://api.anthropic.test",
		ProviderType:  "anthropic",
		AuthMode:      "claude_cli",
		TokenProvider: "anthropic_claude",
		APIKey:        "claudetok",
		AuthScheme:    "bearer",
		TokenMetadata: map[string]any{
			"workload": "cron",
		},
	}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, []byte(`{"model":"claude-test"}`))
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	outboundBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(outboundBody, &payload); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	system, ok := payload["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatalf("system = %#v", payload["system"])
	}
	firstSystem, ok := system[0].(map[string]any)
	if !ok {
		t.Fatalf("system[0] = %#v", system[0])
	}
	text, _ := firstSystem["text"].(string)
	if !strings.Contains(text, "cc_workload=cron;") {
		t.Fatalf("system[0] = %#v", system[0])
	}
}

func claudeMetadataUserIDFromPreparedRequest(t *testing.T, upstream *http.Request) map[string]string {
	t.Helper()
	payload := claudePreparedPayloadFromRequest(t, upstream)
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %#v", payload["metadata"])
	}
	userID, ok := metadata["user_id"].(string)
	if !ok {
		t.Fatalf("metadata.user_id = %#v", metadata["user_id"])
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(userID), &parsed); err != nil {
		t.Fatalf("unmarshal metadata.user_id: %v", err)
	}
	return parsed
}

func claudeFirstSystemTextFromPreparedRequest(t *testing.T, upstream *http.Request) string {
	t.Helper()
	payload := claudePreparedPayloadFromRequest(t, upstream)
	system, ok := payload["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatalf("system = %#v", payload["system"])
	}
	first, ok := system[0].(map[string]any)
	if !ok {
		t.Fatalf("system[0] = %#v", system[0])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatalf("system[0].text = %#v", first["text"])
	}
	return text
}

func claudePreparedPayloadFromRequest(t *testing.T, upstream *http.Request) map[string]any {
	t.Helper()
	outboundBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(outboundBody, &payload); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	return payload
}

func claudeBillingCCHFromSystemText(t *testing.T, systemText string) string {
	t.Helper()
	start := strings.Index(systemText, "cch=")
	if start < 0 {
		t.Fatalf("system text has no cch field: %q", systemText)
	}
	valueStart := start + len("cch=")
	valueEnd := strings.Index(systemText[valueStart:], ";")
	if valueEnd < 0 {
		t.Fatalf("system text has unterminated cch field: %q", systemText)
	}
	return systemText[valueStart : valueStart+valueEnd]
}

func claudeCodeTestRoute() routeInfo {
	return routeInfo{
		BaseURL:       "https://api.anthropic.com",
		ProviderType:  "anthropic",
		AuthMode:      "claude_cli",
		TokenProvider: "anthropic_claude",
		APIKey:        "claudetok",
		AuthScheme:    "bearer",
		TokenMetadata: map[string]any{},
	}
}

func TestAnthropicAdapterUsesAPIKeyByDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
	route := routeInfo{
		BaseURL:      "https://api.anthropic.test",
		ProviderType: "anthropic",
		APIKey:       "api-key",
	}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, []byte(`{"model":"claude-test"}`))
	defer cancel()
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	if got := upstream.Header.Get("X-API-Key"); got != "api-key" {
		t.Fatalf("X-API-Key = %q", got)
	}
	if got := upstream.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization should not be set for API key auth, got %q", got)
	}
}

func TestOpenWebSocketWithRetryDialsUpstream(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer upsec" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("OpenAI-Beta"); got != "realtime=v1" {
			t.Fatalf("OpenAI-Beta = %q", got)
		}
		conn, err := upgrader.Upgrade(w, r, http.Header{"X-Upstream": []string{"ok"}})
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()
		if err := conn.WriteMessage(websocket.TextMessage, []byte("upstream-ok")); err != nil {
			t.Fatalf("write message: %v", err)
		}
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodGet, "/v1/realtime?model=gpt-test", nil)
	req.Header.Set("OpenAI-Beta", "realtime=v1")
	route := routeInfo{
		BaseURL:      upstream.URL,
		ProviderType: "openai",
		APIKey:       "upsec",
	}

	result, err := (&Server{}).openWebSocketWithRetry(req, route, "gpt-test", "realtime", "", "", "pool", "", 0, "", false, nil)
	if err != nil {
		t.Fatalf("openWebSocketWithRetry returned error: %v", err)
	}
	defer result.UpstreamConn.Close()
	if result.Status != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", result.Status)
	}
	if got := result.Header.Get("X-Upstream"); got != "ok" {
		t.Fatalf("X-Upstream = %q", got)
	}
	_, payload, err := result.UpstreamConn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	if string(payload) != "upstream-ok" {
		t.Fatalf("payload = %q", string(payload))
	}
}

func TestCopyUpstreamRequestHeadersFiltersSensitiveAndConnectionScoped(t *testing.T) {
	src := http.Header{}
	src.Set("Authorization", "Bearer downstream")
	src.Set("Cookie", "session=secret")
	src.Set("Connection", "X-Remove, keep-alive")
	src.Set("X-Remove", "gone")
	src.Set("X-Litellm-Provider", "leak")
	src.Set("OpenAI-Beta", "assistants=v2")
	src.Set("Anthropic-Version", "2023-06-01")
	src.Set("Accept", "text/event-stream")
	src.Set("X-Trace-Id", "trace-1")

	dst := http.Header{}
	copyUpstreamRequestHeaders(dst, src)

	for _, key := range []string{"Authorization", "Cookie", "Connection", "X-Remove", "X-Litellm-Provider", "X-Trace-Id"} {
		if got := dst.Get(key); got != "" {
			t.Fatalf("%s leaked as %q", key, got)
		}
	}
	if got := dst.Get("OpenAI-Beta"); got != "assistants=v2" {
		t.Fatalf("OpenAI-Beta = %q", got)
	}
	if got := dst.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q", got)
	}
	if got := dst.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}
}

func TestPassSafeRequestHeadersFiltersForwardedAndCDNHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("X-Trace-Id", "trace-1")
	src.Set("X-Forwarded-For", "203.0.113.10")
	src.Set("X-Forwarded-Proto", "https")
	src.Set("X-Real-IP", "203.0.113.11")
	src.Set("Forwarded", "for=203.0.113.12")
	src.Set("Cf-Ray", "ray-id")
	src.Set("Cf-Connecting-IP", "203.0.113.13")
	src.Set("Cdn-Loop", "cloudflare")
	src.Set("Via", "1.1 proxy")

	dst := http.Header{}
	passSafeRequestHeaders(dst, src, nil)

	if got := dst.Get("X-Trace-Id"); got != "trace-1" {
		t.Fatalf("X-Trace-Id = %q", got)
	}
	for _, key := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Real-IP", "Forwarded", "Cf-Ray", "Cf-Connecting-IP", "Cdn-Loop", "Via"} {
		if got := dst.Get(key); got != "" {
			t.Fatalf("%s leaked as %q", key, got)
		}
	}
}

func TestApplyRouteHeaderRulesPassesAndOverridesConfiguredHeaders(t *testing.T) {
	original := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	original.Header.Set("X-Trace-Id", "trace-1")
	original.Header.Set("X-Client-Feature", "feature-a")
	original.Header.Set("Authorization", "Bearer downstream")
	original.Header.Set("Cookie", "session=secret")
	original.Header.Set("X-Litellm-Provider", "leak")
	original.Header.Set("OpenAI-Beta", "assistants=v2")
	original.Header.Set("X-Elucid-Relay-Session", "relay-session")
	original.Header.Set("X-Subrouter-Session", "subrouter-session")

	upstream := httptest.NewRequest(http.MethodPost, "https://upstream.example/v1/chat/completions", nil)
	route := routeInfo{
		APIKey: "upsec",
		ChannelMeta: `{
			"request_headers": {
				"pass": ["X-Trace-Id", "regex:^X-Client-", "X-Elucid-Relay-Session", "X-Subrouter-Session"],
				"set": {
					"Authorization": "Bearer {api_key}",
					"X-From-Client": "{client_header:X-Trace-Id}",
					"Connection": "close"
				},
				"remove": ["OpenAI-Beta"]
			}
		}`,
	}

	if err := applyRouteHeaderRules(upstream, original, route); err != nil {
		t.Fatalf("applyRouteHeaderRules returned error: %v", err)
	}

	if got := upstream.Header.Get("X-Trace-Id"); got != "trace-1" {
		t.Fatalf("X-Trace-Id = %q", got)
	}
	if got := upstream.Header.Get("X-Client-Feature"); got != "feature-a" {
		t.Fatalf("X-Client-Feature = %q", got)
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer upsec" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := upstream.Header.Get("X-From-Client"); got != "trace-1" {
		t.Fatalf("X-From-Client = %q", got)
	}
	for _, key := range []string{"Cookie", "X-Litellm-Provider", "Connection", "OpenAI-Beta", "X-Elucid-Relay-Session", "X-Subrouter-Session"} {
		if got := upstream.Header.Get(key); got != "" {
			t.Fatalf("%s leaked as %q", key, got)
		}
	}
}

func TestApplyRouteHeaderRulesAccountOverridesChannel(t *testing.T) {
	original := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	upstream := httptest.NewRequest(http.MethodPost, "https://upstream.example/v1/chat/completions", nil)
	route := routeInfo{
		ChannelMeta: `{"request_headers":{"set":{"X-Route":"channel"}}}`,
		AccountMeta: `{"request_headers":{"set":{"X-Route":"account"}}}`,
	}

	if err := applyRouteHeaderRules(upstream, original, route); err != nil {
		t.Fatalf("applyRouteHeaderRules returned error: %v", err)
	}
	if got := upstream.Header.Get("X-Route"); got != "account" {
		t.Fatalf("X-Route = %q", got)
	}
}

func TestRouteHeaderRulesRejectInvalidRegex(t *testing.T) {
	_, err := routeHeaderRulesFromMetadata(`{"request_headers":{"pass":["regex:["]}}`)
	if err == nil {
		t.Fatal("expected invalid regex to be rejected")
	}
	if got := errorCode(err); got != "invalid_route_header_rules" {
		t.Fatalf("error code = %q", got)
	}
}

func TestReadUpstreamResponseBodyLimitedRejectsOversizedPayload(t *testing.T) {
	body, err := readUpstreamResponseBodyLimited(bytes.NewBufferString("abcdef"), 5)
	if err == nil {
		t.Fatal("expected oversized payload error")
	}
	if !errors.Is(err, errUpstreamResponseTooLarge) {
		t.Fatalf("error = %v", err)
	}
	if body != nil {
		t.Fatalf("body = %q", string(body))
	}
}

func TestReadUpstreamHTTPResponseBodyDecodesGzip(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	resp := &http.Response{
		Header: http.Header{"Content-Encoding": []string{"gzip"}},
		Body:   io.NopCloser(bytes.NewReader(compressed.Bytes())),
	}

	body, err := readUpstreamHTTPResponseBody(resp)
	if err != nil {
		t.Fatalf("readUpstreamHTTPResponseBody returned error: %v", err)
	}
	if got := string(body); got != `{"ok":true}` {
		t.Fatalf("body = %q", got)
	}
}

func TestReadUpstreamHTTPResponseBodyDecodesDeflate(t *testing.T) {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	resp := &http.Response{
		Header: http.Header{"Content-Encoding": []string{"deflate"}},
		Body:   io.NopCloser(bytes.NewReader(compressed.Bytes())),
	}

	body, err := readUpstreamHTTPResponseBody(resp)
	if err != nil {
		t.Fatalf("readUpstreamHTTPResponseBody returned error: %v", err)
	}
	if got := string(body); got != `{"ok":true}` {
		t.Fatalf("body = %q", got)
	}
}

func TestReadUpstreamHTTPResponseBodyDecodesStackedAndBrotli(t *testing.T) {
	payload := []byte(`{"ok":true}`)
	var brCompressed bytes.Buffer
	brWriter := brotli.NewWriter(&brCompressed)
	if _, err := brWriter.Write(payload); err != nil {
		t.Fatalf("brotli write: %v", err)
	}
	if err := brWriter.Close(); err != nil {
		t.Fatalf("brotli close: %v", err)
	}
	var stacked bytes.Buffer
	gzipWriter := gzip.NewWriter(&stacked)
	if _, err := gzipWriter.Write(brCompressed.Bytes()); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	resp := &http.Response{
		Header: http.Header{"Content-Encoding": []string{"br, gzip"}},
		Body:   io.NopCloser(bytes.NewReader(stacked.Bytes())),
	}

	body, err := readUpstreamHTTPResponseBody(resp)
	if err != nil {
		t.Fatalf("readUpstreamHTTPResponseBody returned error: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("body = %q", string(body))
	}
}

func TestCopyHeadersFiltersResponseHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "text/event-stream")
	src.Set("Set-Cookie", "session=secret")
	src.Set("Connection", "X-Remove")
	src.Set("X-Remove", "gone")
	src.Set("X-Portkey-Provider", "leak")
	src.Set("X-RateLimit-Limit", "10")

	recorder := httptest.NewRecorder()
	recorder.Header().Set("X-Request-Id", "local")
	copyHeaders(recorder, src)

	for _, key := range []string{"Set-Cookie", "Connection", "X-Remove", "X-Portkey-Provider"} {
		if got := recorder.Header().Get(key); got != "" {
			t.Fatalf("%s leaked as %q", key, got)
		}
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("X-RateLimit-Limit"); got != "10" {
		t.Fatalf("X-RateLimit-Limit = %q", got)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "local" {
		t.Fatalf("X-Request-Id = %q", got)
	}
}

func TestCloneWebSocketResponseHeadersFiltersManagedAndSensitive(t *testing.T) {
	src := http.Header{}
	src.Set("X-RateLimit-Limit", "10")
	src.Set("Set-Cookie", "session=secret")
	src.Set("Connection", "X-Remove")
	src.Set("X-Remove", "gone")
	src.Set("Sec-WebSocket-Accept", "accept")
	src.Set("X-Litellm-Provider", "leak")

	dst := cloneWebSocketResponseHeaders(src)
	if got := dst.Get("X-RateLimit-Limit"); got != "10" {
		t.Fatalf("X-RateLimit-Limit = %q", got)
	}
	for _, key := range []string{"Set-Cookie", "Connection", "X-Remove", "Sec-WebSocket-Accept", "X-Litellm-Provider"} {
		if got := dst.Get(key); got != "" {
			t.Fatalf("%s leaked as %q", key, got)
		}
	}
}

func TestSetStreamingProxyHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	setStreamingProxyHeaders(recorder)

	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-cache, no-transform" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := recorder.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}
}

func TestStreamCommitWriterDelaysHeadersUntilPayload(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := newStreamCommitWriter(recorder, routeInfo{}, http.StatusAccepted, http.Header{"X-Upstream": []string{"yes"}}, 0)
	if writer.Committed() {
		t.Fatal("writer should not be committed before payload")
	}
	if got := recorder.Header().Get("Content-Type"); got != "" {
		t.Fatalf("Content-Type before payload = %q", got)
	}
	if _, err := writer.Write([]byte("data: ok\n\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if !writer.Committed() {
		t.Fatal("writer should be committed after payload")
	}
	if got := recorder.Code; got != http.StatusAccepted {
		t.Fatalf("status = %d", got)
	}
	if got := recorder.Header().Get("X-Upstream"); got != "yes" {
		t.Fatalf("X-Upstream = %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := writer.PayloadBytes(); got != int64(len("data: ok\n\n")) {
		t.Fatalf("payload bytes = %d", got)
	}
}

func TestCanRetryStreamBootstrapOnlyBeforeCommit(t *testing.T) {
	attempt := streamOpenResult{Attempt: 0, MaxAttempts: 2}
	reqCtx := northboundRequestContext{}
	writer := newStreamCommitWriter(httptest.NewRecorder(), routeInfo{}, http.StatusOK, nil, 0)
	if !canRetryStreamBootstrap(io.ErrUnexpectedEOF, writer, attempt, reqCtx) {
		t.Fatal("expected retry before committed payload")
	}
	if _, err := writer.Write([]byte("data: ok\n\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if canRetryStreamBootstrap(io.ErrUnexpectedEOF, writer, attempt, reqCtx) {
		t.Fatal("did not expect retry after committed payload")
	}
	reqCtx.RouteAffinityKey = "session-1"
	reqCtx.RouteAffinitySkipRetry = true
	if canRetryStreamBootstrap(io.ErrUnexpectedEOF, nil, attempt, reqCtx) {
		t.Fatal("did not expect retry when affinity skip retry is active")
	}
}

func TestResponsesSSEFramerRepairsImplicitEventBoundaryAndCompletedOutput(t *testing.T) {
	input := strings.Join([]string{
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","item":{"id":"item_1","type":"message"}}`,
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[]}}`,
		"",
		"",
	}, "\n")

	out, err := io.ReadAll(newResponsesSSEFramerReader(strings.NewReader(input)))
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "event: response.output_item.done\n") || !strings.Contains(text, "event: response.completed\n") {
		t.Fatalf("events were not preserved: %q", text)
	}
	if !strings.Contains(text, `"output":[{"id":"item_1","type":"message"}]`) {
		t.Fatalf("completed output was not patched: %q", text)
	}
}

func TestResponsesSSEFramerRejectsInvalidJSONData(t *testing.T) {
	_, err := io.ReadAll(newResponsesSSEFramerReader(strings.NewReader("data: {bad}\n\n")))
	if !errors.Is(err, errInvalidResponsesSSEEvent) {
		t.Fatalf("err = %v, expected invalid responses SSE event", err)
	}
}

func TestWebSocketScheme(t *testing.T) {
	if got := websocketScheme("https"); got != "wss" {
		t.Fatalf("https scheme = %q", got)
	}
	if got := websocketScheme("http"); got != "ws" {
		t.Fatalf("http scheme = %q", got)
	}
	if got := websocketScheme("wss"); got != "wss" {
		t.Fatalf("wss scheme = %q", got)
	}
}

func TestRetryableUpstreamStatus(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		if !isRetryableUpstreamStatus(status) {
			t.Fatalf("%d should be retryable", status)
		}
	}
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized} {
		if isRetryableUpstreamStatus(status) {
			t.Fatalf("%d should not be retryable", status)
		}
	}
}

func TestSameAccountRetryDecision(t *testing.T) {
	if !shouldRetrySameAccount(routeInfo{}, io.ErrUnexpectedEOF, 0, nil) {
		t.Fatal("unexpected EOF should be same-account retryable")
	}
	if !shouldRetrySameAccount(routeInfo{}, nil, http.StatusServiceUnavailable, nil) {
		t.Fatal("503 without Retry-After should be same-account retryable")
	}
	withRetryAfter := http.Header{"Retry-After": []string{"5"}}
	if shouldRetrySameAccount(routeInfo{}, nil, http.StatusServiceUnavailable, withRetryAfter) {
		t.Fatal("Retry-After should disable same-account retry")
	}
	if shouldRetrySameAccount(routeInfo{}, nil, http.StatusTooManyRequests, nil) {
		t.Fatal("429 should switch account instead of same-account retry")
	}
	disabled := routeInfo{AccountMeta: `{"retry":{"same_account_retries":0}}`}
	if shouldRetrySameAccount(disabled, io.ErrUnexpectedEOF, 0, nil) {
		t.Fatal("same-account retry should honor route disable")
	}
}

func TestUpstreamQuotaSignalsFromHeaders(t *testing.T) {
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	header := http.Header{}
	header.Set("X-RateLimit-Remaining-Requests", "7")
	header.Set("X-RateLimit-Limit-Requests", "10")
	header.Set("X-RateLimit-Reset-Requests", "60")
	header.Set("X-RateLimit-Remaining-Tokens", "1000")
	header.Set("X-RateLimit-Limit-Tokens", "2000")

	signals := upstreamQuotaSignalsFromHeaders(header, http.StatusOK, now)
	if len(signals) != 2 {
		t.Fatalf("signals = %#v", signals)
	}
	if signals[0].WindowType != "requests" || signals[0].Remaining != "7" || signals[0].Limit != "10" {
		t.Fatalf("request signal = %#v", signals[0])
	}
	if signals[0].ResetAt == nil || !signals[0].ResetAt.Equal(now.Add(60*time.Second)) {
		t.Fatalf("request reset = %#v", signals[0].ResetAt)
	}
	if signals[1].WindowType != "tokens" || signals[1].Remaining != "1000" || signals[1].Limit != "2000" {
		t.Fatalf("token signal = %#v", signals[1])
	}
}

func TestUpstreamQuotaSignalsUsesRetryAfterFor429(t *testing.T) {
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	header := http.Header{"Retry-After": []string{"30"}}

	signals := upstreamQuotaSignalsFromHeaders(header, http.StatusTooManyRequests, now)
	if len(signals) != 1 {
		t.Fatalf("signals = %#v", signals)
	}
	if signals[0].WindowType != "requests" || signals[0].Remaining != "0" {
		t.Fatalf("retry-after signal = %#v", signals[0])
	}
	if signals[0].ResetAt == nil || !signals[0].ResetAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("retry-after reset = %#v", signals[0].ResetAt)
	}
}

func TestUpstreamQuotaSignalsFromAnthropicTokenHeaders(t *testing.T) {
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	header := http.Header{}
	header.Set("Anthropic-RateLimit-Requests-Remaining", "3")
	header.Set("Anthropic-RateLimit-Requests-Limit", "5")
	header.Set("Anthropic-RateLimit-Input-Tokens-Remaining", "100")
	header.Set("Anthropic-RateLimit-Input-Tokens-Limit", "200")
	header.Set("Anthropic-RateLimit-Output-Tokens-Remaining", "50")
	header.Set("Anthropic-RateLimit-Output-Tokens-Limit", "80")

	signals := upstreamQuotaSignalsFromHeaders(header, http.StatusOK, now)
	if len(signals) != 3 {
		t.Fatalf("signals = %#v", signals)
	}
	if signals[0].WindowType != "requests" || signals[0].Remaining != "3" || signals[0].Limit != "5" {
		t.Fatalf("request signal = %#v", signals[0])
	}
	if signals[1].WindowType != "input_tokens" || signals[1].Remaining != "100" {
		t.Fatalf("input token signal = %#v", signals[1])
	}
	if signals[2].WindowType != "output_tokens" || signals[2].Remaining != "50" {
		t.Fatalf("output token signal = %#v", signals[2])
	}
}

func TestUpstreamHeaderSignalSnapshot(t *testing.T) {
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	header := http.Header{}
	header.Set("Retry-After", "30")
	header.Set("X-RateLimit-Remaining-Requests", "0")
	snapshot := upstreamHeaderSignalSnapshot(header, http.StatusTooManyRequests, now)
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	if got := metadataText(snapshot["retry_after_at"]); got != now.Add(30*time.Second).Format(time.RFC3339) {
		t.Fatalf("retry_after_at = %q", got)
	}
	rateLimits, ok := objectValue(snapshot["rate_limits"])
	if !ok {
		t.Fatalf("rate_limits = %#v", snapshot["rate_limits"])
	}
	if _, ok := rateLimits["requests"]; !ok {
		t.Fatalf("requests rate limit missing: %#v", rateLimits)
	}
}

func TestStreamMeteringParsesFinalUsageWithoutChangingBody(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":4}}\n\n" +
		"data: [DONE]\n\n"
	var output bytes.Buffer
	acc := &streamMeteringAccumulator{}
	written, err := copyStreamAndMeter(&output, strings.NewReader(input), openaiCompatibleAdapter{}, "chat", acc)
	if err != nil {
		t.Fatalf("copyStreamAndMeter returned error: %v", err)
	}
	if written != int64(len(input)) {
		t.Fatalf("written = %d, expected %d", written, len(input))
	}
	if output.String() != input {
		t.Fatalf("stream body changed: %q", output.String())
	}
	result := acc.meteringResult(meteringMetrics{RequestCount: 1}, "stream_parsed")
	if result.InputTokens != 12 || result.OutputTokens != 4 || result.UsageSource != "stream_parsed" || result.StreamEventCount != 2 {
		t.Fatalf("unexpected metering result: %#v", result)
	}
}

func TestStreamMeteringFallsBackWhenUsageMissing(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"
	acc := &streamMeteringAccumulator{}
	if _, err := copyStreamAndMeter(io.Discard, strings.NewReader(input), openaiCompatibleAdapter{}, "chat", acc); err != nil {
		t.Fatalf("copyStreamAndMeter returned error: %v", err)
	}
	result := acc.meteringResult(meteringMetrics{InputTokens: 3, RequestCount: 1}, "stream_parsed")
	if result.InputTokens != 3 || result.UsageSource != "estimated_fallback" {
		t.Fatalf("unexpected fallback result: %#v", result)
	}
}

func TestStreamCopyErrorEventPayloadByDialect(t *testing.T) {
	openAIEvent := string(streamCopyErrorEventPayload(routeInfo{ProviderType: "openai"}, "chat", "req-1"))
	if !strings.HasPrefix(openAIEvent, "data: ") || !strings.Contains(openAIEvent, `"code":"stream_interrupted"`) || !strings.Contains(openAIEvent, `"request_id":"req-1"`) {
		t.Fatalf("unexpected OpenAI event: %q", openAIEvent)
	}

	anthropicEvent := string(streamCopyErrorEventPayload(routeInfo{ProviderType: "anthropic"}, "messages", "req-1"))
	if !strings.HasPrefix(anthropicEvent, "event: error\n") || !strings.Contains(anthropicEvent, `"type":"api_error"`) {
		t.Fatalf("unexpected Anthropic event: %q", anthropicEvent)
	}
}

func TestShouldEmitStreamCopyErrorEventSkipsClientDisconnects(t *testing.T) {
	if !shouldEmitStreamCopyErrorEvent(io.ErrUnexpectedEOF) {
		t.Fatal("upstream read error should emit an SSE error event")
	}
	if shouldEmitStreamCopyErrorEvent(io.ErrShortWrite) {
		t.Fatal("client write errors should not emit an SSE error event")
	}
	if shouldEmitStreamCopyErrorEvent(errors.New("write tcp: broken pipe")) {
		t.Fatal("broken pipe should not emit an SSE error event")
	}
}

func TestGeminiCLIStreamTransformerConvertsCodeAssistSSE(t *testing.T) {
	input := "data: {\"response\":{\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"thought\",\"thought\":true},{\"text\":\"hi\"},{\"functionCall\":{\"name\":\"lookup\",\"args\":{\"q\":\"x\"}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":9,\"candidatesTokenCount\":2,\"thoughtsTokenCount\":1,\"totalTokenCount\":12}}}\n\n"
	var output bytes.Buffer
	acc := &streamMeteringAccumulator{}
	route := routeInfo{ProviderType: "gemini_cli", UpstreamModel: "gemini-2.5-pro"}
	requestBody := []byte(`{"model":"gemini-2.5-pro","stream":true}`)

	if _, err := copyStreamAndMeterForRoute(&output, strings.NewReader(input), geminiCLIAdapter{}, route, "chat", requestBody, acc); err != nil {
		t.Fatalf("copyStreamAndMeterForRoute returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, `"reasoning_content":"thought"`) {
		t.Fatalf("reasoning chunk missing: %q", out)
	}
	if !strings.Contains(out, `"content":"hi"`) {
		t.Fatalf("content chunk missing: %q", out)
	}
	if !strings.Contains(out, `"tool_calls"`) || !strings.Contains(out, `"name":"lookup"`) {
		t.Fatalf("tool call chunk missing: %q", out)
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("finish chunk missing tool_calls: %q", out)
	}
	if !strings.Contains(out, `"usage":{"completion_tokens":3`) {
		t.Fatalf("usage chunk missing: %q", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("DONE missing: %q", out)
	}

	result := acc.meteringResult(meteringMetrics{RequestCount: 1}, "stream_parsed")
	if result.InputTokens != 9 || result.OutputTokens != 3 || result.UsageSource != "stream_parsed" || result.StreamEventCount != 1 {
		t.Fatalf("unexpected metering result: %#v", result)
	}
}

func TestWebSocketMeteringParsesUsageFrame(t *testing.T) {
	stats := &webSocketMeteringStats{}
	stats.recordFrame([]byte(`{"usage":{"input_tokens":9,"output_tokens":2}}`), openaiCompatibleAdapter{}, "realtime", true)
	result := stats.meteringResult(meteringMetrics{RequestCount: 1})
	if result.InputTokens != 9 || result.OutputTokens != 2 || result.UsageSource != "websocket_parsed" || result.WebSocketFrameCount != 1 {
		t.Fatalf("unexpected websocket metering result: %#v", result)
	}
}
