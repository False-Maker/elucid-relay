package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRouteStatusCodeMappingOnlyChangesClientStatus(t *testing.T) {
	route := routeInfo{ChannelMeta: `{"status_code_mapping":{"529":429}}`}

	if got := clientResponseStatus(route, 529); got != http.StatusTooManyRequests {
		t.Fatalf("client status = %d", got)
	}
	if !isRetryableUpstreamStatusForRoute(route, 529) {
		t.Fatal("status mapping must not make the original upstream status non-retryable")
	}
	metadata := usageMetadataWithClientStatusMapping(route, 529, nil)
	mapping, ok := metadata["client_status_mapping"].(map[string]any)
	if !ok {
		t.Fatalf("missing client status mapping metadata: %#v", metadata)
	}
	if mapping["upstream_status"] != 529 || mapping["client_status"] != http.StatusTooManyRequests {
		t.Fatalf("mapping metadata = %#v", mapping)
	}
}

func TestRetryableStatusOverridesFromRouteMetadata(t *testing.T) {
	route := routeInfo{
		ChannelMeta: `{"retry":{"retryable_statuses":[418,529],"non_retryable_statuses":[503]}}`,
		AccountMeta: `{"retry":{"non_retryable_statuses":[503,529]}}`,
	}

	if !isRetryableUpstreamStatusForRoute(route, http.StatusTeapot) {
		t.Fatal("configured retryable status was not retryable")
	}
	if isRetryableUpstreamStatusForRoute(route, http.StatusServiceUnavailable) {
		t.Fatal("configured non-retryable status was retryable")
	}
	if isRetryableUpstreamStatusForRoute(route, 529) {
		t.Fatal("account-level non-retryable status should override channel retryable status")
	}
}

func TestCircuitBreakerStatusOpenSecondsFromRouteMetadata(t *testing.T) {
	route := routeInfo{ChannelMeta: `{"circuit_breaker":{"status_open_seconds":{"429":45}}}`}

	if got := circuitBreakerOpenDuration(route, http.StatusTooManyRequests); got != 45*time.Second {
		t.Fatalf("status-specific open duration = %s", got)
	}
	if got := circuitBreakerOpenDuration(route, http.StatusBadGateway); got != 2*time.Minute {
		t.Fatalf("default 5xx duration = %s", got)
	}
}

func TestRouteStatusAndCircuitAliases(t *testing.T) {
	route := routeInfo{ChannelMeta: `{
		"status_code_mappings": {"529": 429},
		"circuit_breaker": {"status_cooldown_seconds": {"503": 90}}
	}`}

	if got := clientResponseStatus(route, 529); got != http.StatusTooManyRequests {
		t.Fatalf("alias client status = %d", got)
	}
	if got := circuitBreakerOpenDuration(route, http.StatusServiceUnavailable); got != 90*time.Second {
		t.Fatalf("alias circuit duration = %s", got)
	}
}

func TestRouteResponseRewriteRewritesTextBodyAndHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://relay.example/v1/chat/completions", nil)
	route := routeInfo{
		BaseURL: `https://upstream.example/api`,
		ChannelMeta: `{
			"response_rewrite": {
				"replace": {
					"{route:base_url}": "{request:origin}/upstream",
					"https://files.example": "{request:origin}/files"
				},
				"headers": ["Location", "X-Asset-Url"],
				"content_types": ["application/json"]
			}
		}`,
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("Content-Length", "156")
	header.Set("Etag", "etag-1")
	header.Set("Location", "https://upstream.example/api/backend-api/conversation")
	header.Set("X-Asset-Url", "https://files.example/file-a")
	body := []byte(`{"url":"https://upstream.example/api/backend-api/conversation","file":"https://files.example/file-a","escaped":"https:\/\/files.example\/file-a"}`)

	outHeader, outBody := applyRouteResponseRewrite(req, route, header, body)

	if got := string(outBody); got != `{"url":"https://relay.example/upstream/backend-api/conversation","file":"https://relay.example/files/file-a","escaped":"https:\/\/relay.example\/files\/file-a"}` {
		t.Fatalf("body = %s", got)
	}
	if got := outHeader.Get("Location"); got != "https://relay.example/upstream/backend-api/conversation" {
		t.Fatalf("Location = %q", got)
	}
	if got := outHeader.Get("X-Asset-Url"); got != "https://relay.example/files/file-a" {
		t.Fatalf("X-Asset-Url = %q", got)
	}
	for _, key := range []string{"Content-Length", "Etag"} {
		if got := outHeader.Get(key); got != "" {
			t.Fatalf("%s = %q", key, got)
		}
	}
	if got := header.Get("Location"); got != "https://upstream.example/api/backend-api/conversation" {
		t.Fatalf("input header was mutated: %q", got)
	}
}

func TestRouteResponseRewriteSkipsBinaryBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://relay.example/v1/chat/completions", nil)
	route := routeInfo{
		BaseURL:     `https://upstream.example`,
		ChannelMeta: `{"response_rewrite":{"replace":{"https://upstream.example":"https://relay.example/upstream"}}}`,
	}
	header := http.Header{"Content-Type": []string{"image/png"}, "Content-Length": []string{"32"}}
	body := []byte("png https://upstream.example bytes")

	outHeader, outBody := applyRouteResponseRewrite(req, route, header, body)

	if !bytes.Equal(outBody, body) {
		t.Fatalf("binary body was rewritten: %q", string(outBody))
	}
	if got := outHeader.Get("Content-Length"); got != "32" {
		t.Fatalf("Content-Length = %q", got)
	}
}

func TestRouteParamOverrideOperations(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{ChannelMeta: `{
		"param_override": {
			"operations": [
				{"op":"set","path":"stream_options.include_usage","value":true},
				{"op":"set","path":"model","value":"blocked-model"},
				{"op":"delete","path":"service_tier"},
				{"op":"copy","from":"metadata.thread_id","to":"session_id"},
				{"op":"move","from":"max_tokens","to":"max_completion_tokens"},
				{"op":"append","path":"tools","value":["tool-b"]},
				{"op":"prepend","path":"messages","value":["system"]},
				{"op":"regex_replace","path":"input","pattern":"foo","replacement":"bar"}
			]
		}
	}`}
	body := []byte(`{"model":"gpt-test","metadata":{"thread_id":"thread-1"},"max_tokens":12,"tools":["tool-a"],"messages":["user"],"input":"foo","service_tier":"auto"}`)

	rewritten, err := (&Server{}).applyReverseProxyParamOverrides(context.Background(), req, route, body)
	if err != nil {
		t.Fatalf("applyReverseProxyParamOverrides returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("rewritten body is invalid JSON: %v", err)
	}
	if payload["model"] != "gpt-test" {
		t.Fatalf("model override should be blocked, got %#v", payload["model"])
	}
	if _, ok := payload["service_tier"]; ok {
		t.Fatalf("service_tier was not deleted: %#v", payload)
	}
	if _, ok := payload["max_tokens"]; ok {
		t.Fatalf("max_tokens was not moved: %#v", payload)
	}
	if payload["max_completion_tokens"] != float64(12) {
		t.Fatalf("max_completion_tokens = %#v", payload["max_completion_tokens"])
	}
	if payload["session_id"] != "thread-1" {
		t.Fatalf("session_id = %#v", payload["session_id"])
	}
	if payload["input"] != "bar" {
		t.Fatalf("input = %#v", payload["input"])
	}
	if jsonPathString(payload, "stream_options.include_usage") != "true" {
		t.Fatalf("stream_options.include_usage = %#v", payload["stream_options"])
	}
	if got := metadataList(payload["tools"]); len(got) != 2 || got[0] != "tool-a" || got[1] != "tool-b" {
		t.Fatalf("tools = %#v", payload["tools"])
	}
	if got := metadataList(payload["messages"]); len(got) != 2 || got[0] != "system" || got[1] != "user" {
		t.Fatalf("messages = %#v", payload["messages"])
	}
}

func TestRouteParamOverrideModeAliasReplaceAndAudit(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(withParamOverrideAuditAll(req.Context()))
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{ChannelMeta: `{"param_override":{"operations":[{"mode":"replace","path":"input","from":"foo","to":"bar"},{"mode":"set","path":"service_tier","value":"flex"}]}}`}

	rewritten, err := (&Server{}).applyReverseProxyParamOverrides(context.Background(), req, route, []byte(`{"input":"foo foo","service_tier":"auto"}`))
	if err != nil {
		t.Fatalf("applyReverseProxyParamOverrides returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("rewritten body is invalid JSON: %v", err)
	}
	if payload["input"] != "bar bar" || payload["service_tier"] != "flex" {
		t.Fatalf("payload = %#v", payload)
	}
	audit := paramOverrideAuditFromRequest(req)
	if len(audit) != 2 || audit[0] != "replace input" || audit[1] != "set service_tier" {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestRouteParamOverrideSupportsArrayPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{ChannelMeta: `{"param_override":{"operations":[{"op":"set","path":"messages.0.content","value":"bar"},{"op":"copy","from":"messages.0.content","to":"metadata.first_message"}]}}`}

	rewritten, err := (&Server{}).applyReverseProxyParamOverrides(context.Background(), req, route, []byte(`{"messages":[{"content":"foo"}]}`))
	if err != nil {
		t.Fatalf("applyReverseProxyParamOverrides returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("rewritten body is invalid JSON: %v", err)
	}
	if got := jsonPathString(payload, "messages.0.content"); got != "bar" {
		t.Fatalf("messages.0.content = %q", got)
	}
	if got := jsonPathString(payload, "metadata.first_message"); got != "bar" {
		t.Fatalf("metadata.first_message = %q", got)
	}
}

func TestRouteParamOverrideRejectsInvalidRegex(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{ChannelMeta: `{"param_override":{"operations":[{"op":"regex_replace","path":"input","pattern":"["}]}}`}

	_, err := (&Server{}).applyReverseProxyParamOverrides(context.Background(), req, route, []byte(`{"input":"foo"}`))
	if err == nil {
		t.Fatal("expected invalid regex error")
	}
	if got := errorCode(err); got != "invalid_param_override" {
		t.Fatalf("error code = %q", got)
	}
}

func TestRouteParamOverrideHeaderOperations(t *testing.T) {
	original := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	original.Header.Set("X-Trace-Id", "trace-1")
	original.Header.Set("X-Pass", "pass-1")
	original = original.WithContext(withParamOverrideAuditAll(original.Context()))
	header := http.Header{}
	header.Set("X-Delete", "delete-me")
	header.Set("X-Move-Source", "move-me")
	route := routeInfo{ChannelMeta: `{"param_override":{"operations":[
		{"op":"set_header","path":"X-Set","value":"{request_header:X-Trace-Id}"},
		{"op":"copy_header","from":"X-Trace-Id","to":"X-Copied"},
		{"op":"move_header","from":"X-Move-Source","to":"X-Moved"},
		{"op":"delete_header","path":"X-Delete"},
		{"op":"pass_headers","value":["X-Pass"]}
	]}}`}

	if err := applyRouteParamOverrideHeaderOperations(original, route, header); err != nil {
		t.Fatalf("applyRouteParamOverrideHeaderOperations returned error: %v", err)
	}
	if got := header.Get("X-Set"); got != "trace-1" {
		t.Fatalf("X-Set = %q", got)
	}
	if got := header.Get("X-Copied"); got != "trace-1" {
		t.Fatalf("X-Copied = %q", got)
	}
	if got := header.Get("X-Moved"); got != "move-me" {
		t.Fatalf("X-Moved = %q", got)
	}
	if got := header.Get("X-Move-Source"); got != "" {
		t.Fatalf("X-Move-Source = %q", got)
	}
	if got := header.Get("X-Delete"); got != "" {
		t.Fatalf("X-Delete = %q", got)
	}
	if got := header.Get("X-Pass"); got != "pass-1" {
		t.Fatalf("X-Pass = %q", got)
	}
	if audit := paramOverrideAuditFromRequest(original); len(audit) != 5 {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestRouteParamOverrideHeaderOperationsRejectBlockedHeaders(t *testing.T) {
	original := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	route := routeInfo{ChannelMeta: `{"param_override":{"operations":[{"op":"set_header","path":"Authorization","value":"Bearer test"}]}}`}

	err := applyRouteParamOverrideHeaderOperations(original, route, http.Header{})
	if err == nil {
		t.Fatal("expected blocked header error")
	}
	if got := errorCode(err); got != "invalid_param_override" {
		t.Fatalf("error code = %q", got)
	}
}

func TestAdminReverseProxyParamOverridePreview(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/v1/reverse-proxy/param-override-preview", bytes.NewBufferString(`{
		"content_type": "application/json",
		"channel_metadata": {
			"system_prompt": {"mode": "if_absent", "text": "policy"},
			"param_override": {"operations": [
				{"op": "set", "path": "stream_options.include_usage", "value": true},
				{"op": "set_header", "path": "X-Trace", "value": "{request_header:X-Trace-Id}"}
			]}
		},
		"headers": {"X-Trace-Id": "trace-1"},
		"body": {"model": "gpt-test", "messages": [{"role": "user", "content": "hello"}]}
	}`))
	rec := httptest.NewRecorder()

	if err := (&Server{}).adminReverseProxyParamOverridePreview(rec, req, authContext{}); err != nil {
		t.Fatalf("adminReverseProxyParamOverridePreview returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data struct {
			Body struct {
				Messages      []map[string]any `json:"messages"`
				StreamOptions map[string]any   `json:"stream_options"`
			} `json:"body"`
			Headers map[string][]string `json:"headers"`
			Audit   []string            `json:"audit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON invalid: %v", err)
	}
	if len(response.Data.Body.Messages) == 0 || response.Data.Body.Messages[0]["role"] != "system" {
		t.Fatalf("preview body messages = %#v", response.Data.Body.Messages)
	}
	if response.Data.Headers["X-Trace"][0] != "trace-1" {
		t.Fatalf("preview headers = %#v", response.Data.Headers)
	}
	if len(response.Data.Audit) != 3 {
		t.Fatalf("preview audit = %#v", response.Data.Audit)
	}
}

func TestRouteSystemPromptModes(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	route := routeInfo{ChannelMeta: `{"system_prompt":{"mode":"prepend","text":"policy"}}`}

	rewritten, err := (&Server{}).applyReverseProxyParamOverrides(context.Background(), req, route, []byte(`{"messages":[{"role":"system","content":"base"},{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("applyReverseProxyParamOverrides returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("rewritten body is invalid JSON: %v", err)
	}
	if got := jsonPathString(payload, "messages.0.content"); got != "policy\nbase" {
		t.Fatalf("system prompt = %q", got)
	}
}

func TestRoutePreparationErrorIsLocalAppError(t *testing.T) {
	if !isRoutePreparationError(invalidParamOverride("bad override")) {
		t.Fatal("invalid param override should be treated as a local route preparation error")
	}
	if isRoutePreparationError(context.Canceled) {
		t.Fatal("context cancellation should not be treated as a route preparation error")
	}
}

func TestRouteHeaderRulesUseAbilityMetadataAndRoutePlaceholders(t *testing.T) {
	original := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	original.Header.Set("X-Trace-Id", "trace-1")
	upstream := httptest.NewRequest(http.MethodPost, "https://upstream.example/v1/chat/completions", nil)
	route := routeInfo{
		AccountID:     "acc-1",
		ChannelID:     "ch-1",
		ProviderType:  "openai",
		UpstreamModel: "gpt-upstream",
		ChannelMeta:   `{"request_headers":{"set":{"X-Route":"channel"}}}`,
		AbilityMeta:   `{"request_headers":{"set":{"X-Ability":"yes","X-Route":"ability"}}}`,
		AccountMeta:   `{"request_headers":{"set":{"X-Route":"account","X-Trace-Copy":"{request_header:X-Trace-Id}","X-Provider":"{route:provider_type}","X-Account":"{route:account_id}","X-Model":"{route:upstream_model}"}}}`,
	}

	if err := applyRouteHeaderRules(upstream, original, route); err != nil {
		t.Fatalf("applyRouteHeaderRules returned error: %v", err)
	}
	if got := upstream.Header.Get("X-Route"); got != "account" {
		t.Fatalf("X-Route = %q", got)
	}
	if got := upstream.Header.Get("X-Ability"); got != "yes" {
		t.Fatalf("X-Ability = %q", got)
	}
	if got := upstream.Header.Get("X-Trace-Copy"); got != "trace-1" {
		t.Fatalf("X-Trace-Copy = %q", got)
	}
	if got := upstream.Header.Get("X-Provider"); got != "openai" {
		t.Fatalf("X-Provider = %q", got)
	}
	if got := upstream.Header.Get("X-Account"); got != "acc-1" {
		t.Fatalf("X-Account = %q", got)
	}
	if got := upstream.Header.Get("X-Model"); got != "gpt-upstream" {
		t.Fatalf("X-Model = %q", got)
	}
}
