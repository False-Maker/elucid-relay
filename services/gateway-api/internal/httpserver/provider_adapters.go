package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/sync/singleflight"
)

type providerAdapter interface {
	PrepareRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error)
	PrepareWebSocket(original *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error)
	ParseUsage(endpoint string, requestBody []byte, responseBody []byte) meteringResult
	ParseStreamEvent(endpoint string, event []byte, acc *streamMeteringAccumulator)
}

type meteringResult struct {
	InputTokens         int
	OutputTokens        int
	ImageCount          int
	AudioSeconds        float64
	RequestCount        int
	UsageSource         string
	StreamEventCount    int
	WebSocketFrameCount int
	Metadata            map[string]any
}

func (result meteringResult) usageCounts() usageCounts {
	return usageCounts{InputTokens: result.InputTokens, OutputTokens: result.OutputTokens}
}

func (result meteringResult) meteringMetrics() meteringMetrics {
	return meteringMetrics{
		InputTokens:  result.InputTokens,
		OutputTokens: result.OutputTokens,
		ImageCount:   result.ImageCount,
		AudioSeconds: result.AudioSeconds,
		RequestCount: nonZeroRequestCount(result.RequestCount),
	}
}

type streamMeteringAccumulator struct {
	InputTokens      int
	OutputTokens     int
	EventCount       int
	HasProviderUsage bool
	Metadata         map[string]any
}

func (acc *streamMeteringAccumulator) mergeUsage(usage usageCounts) {
	if usage.InputTokens > 0 {
		acc.InputTokens = maxInt(acc.InputTokens, usage.InputTokens)
		acc.HasProviderUsage = true
	}
	if usage.OutputTokens > 0 {
		acc.OutputTokens = maxInt(acc.OutputTokens, usage.OutputTokens)
		acc.HasProviderUsage = true
	}
}

func (acc *streamMeteringAccumulator) meteringResult(fallback meteringMetrics, source string) meteringResult {
	inputTokens := maxInt(acc.InputTokens, fallback.InputTokens)
	outputTokens := maxInt(acc.OutputTokens, fallback.OutputTokens)
	usageSource := source
	if !acc.HasProviderUsage {
		usageSource = "estimated_fallback"
	}
	return meteringResult{
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		ImageCount:       fallback.ImageCount,
		AudioSeconds:     fallback.AudioSeconds,
		RequestCount:     nonZeroRequestCount(fallback.RequestCount),
		UsageSource:      usageSource,
		StreamEventCount: acc.EventCount,
		Metadata:         acc.Metadata,
	}
}

type openaiCompatibleAdapter struct{}
type codexCompatibleAdapter struct{}
type anthropicAdapter struct{}
type githubCopilotAdapter struct{}
type geminiOpenAICompatibleAdapter struct{}
type geminiNativeAdapter struct{}
type geminiCLIAdapter struct{}

type codexModelsCache struct {
	mu        sync.Mutex
	group     singleflight.Group
	snapshots map[string]codexModelSnapshot
}

func newCodexModelsCache() *codexModelsCache {
	return &codexModelsCache{snapshots: map[string]codexModelSnapshot{}}
}

type codexModelSnapshot struct {
	loadedAt time.Time
	models   map[string]routeMetadataSnapshot
}

const codexModelsCacheTTL = 5 * time.Minute

type routeMetadataSnapshot struct {
	instructions string
	reasoning    map[string]any
	capabilities map[string]any
}

func (cache *codexModelsCache) merge(route routeInfo, clientVersion string, client httpDoer) (routeInfo, error) {
	if cache == nil {
		return route, nil
	}
	key := cache.cacheKey(route, clientVersion)
	if snapshot, ok := cache.fresh(key); ok {
		return mergeCodexRouteMetadata(route, snapshot.models[route.UpstreamModel]), nil
	}
	snapshot, err, _ := cache.group.Do(key, func() (any, error) {
		if snapshot, ok := cache.fresh(key); ok {
			return snapshot, nil
		}
		return cache.load(route, clientVersion, client)
	})
	if err != nil {
		return route, err
	}
	modelSnapshot, _ := snapshot.(codexModelSnapshot)
	cache.store(key, modelSnapshot)
	return mergeCodexRouteMetadata(route, modelSnapshot.models[route.UpstreamModel]), nil
}

func (cache *codexModelsCache) fresh(key string) (codexModelSnapshot, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	snapshot, ok := cache.snapshots[key]
	if !ok || time.Since(snapshot.loadedAt) > codexModelsCacheTTL {
		return codexModelSnapshot{}, false
	}
	return snapshot, true
}

func (cache *codexModelsCache) store(key string, snapshot codexModelSnapshot) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if snapshot.loadedAt.IsZero() {
		snapshot.loadedAt = time.Now()
	}
	cache.snapshots[key] = snapshot
}

func (cache *codexModelsCache) cacheKey(route routeInfo, clientVersion string) string {
	return strings.Join([]string{
		strings.TrimSpace(route.BaseURL),
		strings.TrimSpace(route.AccountID),
		strings.TrimSpace(route.TokenSubject),
		strings.TrimSpace(route.TokenProvider),
		strings.TrimSpace(clientVersion),
	}, "|")
}

func (cache *codexModelsCache) load(route routeInfo, clientVersion string, client httpDoer) (codexModelSnapshot, error) {
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	endpoint := codexModelsEndpoint(route)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return codexModelSnapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+route.APIKey)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", codexUserAgent(route, firstNonEmpty(routeMetadataString(route, "codex", "originator"), "codex_exec"), clientVersion))
	req.Header.Set("originator", firstNonEmpty(routeMetadataString(route, "codex", "originator"), "codex_exec"))
	req.Header.Set("version", clientVersion)
	q := req.URL.Query()
	if q.Get("client_version") == "" {
		q.Set("client_version", clientVersion)
		req.URL.RawQuery = q.Encode()
	}
	if accountID := firstNonEmpty(
		routeMetadataString(route, "codex", "chatgpt_account_id", "account_id", "workspace_id"),
		route.TokenSubject,
	); accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return codexModelSnapshot{}, err
	}
	defer resp.Body.Close()
	body, err := readUpstreamHTTPResponseBody(resp)
	if err != nil {
		return codexModelSnapshot{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return codexModelSnapshot{}, upstreamUnavailable("codex_models_sync_failed", "Codex models endpoint returned a non-success status.")
	}
	var decoded struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return codexModelSnapshot{}, err
	}
	models := map[string]routeMetadataSnapshot{}
	for _, model := range decoded.Models {
		snapshot := codexModelSnapshotFromModel(model)
		if slug, ok := model["slug"].(string); ok && strings.TrimSpace(slug) != "" {
			models[slug] = snapshot
		}
	}
	return codexModelSnapshot{loadedAt: time.Now(), models: models}, nil
}

func codexModelSnapshotFromModel(model map[string]any) routeMetadataSnapshot {
	snapshot := routeMetadataSnapshot{}
	if text := metadataText(model["base_instructions"]); text != "" {
		snapshot.instructions = text
	}
	if reasoning, ok := objectValue(model["default_reasoning"]); ok {
		snapshot.reasoning = reasoning
	} else if effort := metadataText(model["default_reasoning_level"]); effort != "" {
		reasoning := map[string]any{"effort": effort}
		if summary := metadataText(model["default_reasoning_summary"]); summary != "" {
			reasoning["summary"] = summary
		}
		snapshot.reasoning = reasoning
	}
	capabilities := map[string]any{}
	for _, key := range []string{
		"display_name",
		"description",
		"supported_reasoning_levels",
		"shell_type",
		"visibility",
		"supported_in_api",
		"priority",
		"additional_speed_tiers",
		"availability_nux",
		"upgrade",
		"model_messages",
		"supports_parallel_tool_calls",
		"supports_reasoning_summaries",
		"support_verbosity",
		"default_verbosity",
		"apply_patch_tool_type",
		"web_search_tool_type",
		"truncation_policy",
		"supports_image_detail_original",
		"context_window",
		"max_context_window",
		"auto_compact_token_limit",
		"effective_context_window_percent",
		"experimental_supported_tools",
		"input_modalities",
	} {
		if value, ok := model[key]; ok {
			capabilities[key] = value
		}
	}
	if len(capabilities) > 0 {
		snapshot.capabilities = capabilities
	}
	return snapshot
}

func mergeCodexRouteMetadata(route routeInfo, snapshot routeMetadataSnapshot) routeInfo {
	if route.TokenMetadata == nil {
		route.TokenMetadata = map[string]any{}
	}
	codexMeta, _ := objectValue(route.TokenMetadata["codex"])
	if codexMeta == nil {
		codexMeta = map[string]any{}
		route.TokenMetadata["codex"] = codexMeta
	}
	if snapshot.instructions != "" {
		if routeMetadataText(route, "codex", "base_instructions", "instructions", "model_instructions") == "" {
			codexMeta["base_instructions"] = snapshot.instructions
			codexMeta["instructions"] = snapshot.instructions
		}
	}
	if snapshot.reasoning != nil {
		if _, exists := routeMetadataObject(route, "codex", "default_reasoning", "reasoning"); !exists {
			codexMeta["default_reasoning"] = snapshot.reasoning
			codexMeta["reasoning"] = snapshot.reasoning
		}
		if effort := metadataText(snapshot.reasoning["effort"]); effort != "" {
			if routeMetadataString(route, "codex", "reasoning_effort", "default_reasoning_effort", "model_reasoning_effort", "default_reasoning_level", "model_reasoning_level") == "" {
				codexMeta["reasoning_effort"] = effort
				codexMeta["default_reasoning_effort"] = effort
				codexMeta["default_reasoning_level"] = effort
			}
		}
		if summary := metadataText(snapshot.reasoning["summary"]); summary != "" {
			if routeMetadataString(route, "codex", "reasoning_summary", "default_reasoning_summary", "model_reasoning_summary") == "" {
				codexMeta["reasoning_summary"] = summary
				codexMeta["default_reasoning_summary"] = summary
			}
		}
	}
	if snapshot.capabilities != nil {
		for key, value := range snapshot.capabilities {
			if _, exists := codexMeta[key]; !exists {
				codexMeta[key] = value
			}
		}
	}
	return route
}

func codexModelsEndpoint(route routeInfo) string {
	baseURL, err := url.Parse(strings.TrimRight(route.BaseURL, "/"))
	if err != nil {
		return strings.TrimRight(route.BaseURL, "/") + "/models"
	}
	path := strings.TrimRight(baseURL.Path, "/")
	if path == "" {
		path = "/models"
	} else {
		path += "/models"
	}
	baseURL.Path = path
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	return baseURL.String()
}

func providerAdapterFor(providerType string) (providerAdapter, error) {
	switch strings.TrimSpace(providerType) {
	case "", "openai", "openai_compatible", "cli_openai_compatible":
		return openaiCompatibleAdapter{}, nil
	case "codex_compatible":
		return codexCompatibleAdapter{}, nil
	case "anthropic", "anthropic_compatible", "claude_compatible":
		return anthropicAdapter{}, nil
	case "github_copilot", "github_copilot_compatible", "copilot":
		return githubCopilotAdapter{}, nil
	case "gemini_openai_compatible":
		return geminiOpenAICompatibleAdapter{}, nil
	case "gemini_cli", "google_gemini_cli", "google_gemini":
		return geminiCLIAdapter{}, nil
	case "antigravity", "google_antigravity", "google_antigravity_cli":
		return antigravityAdapter{}, nil
	case "kiro", "aws_kiro", "amazon_q_kiro", "kiro_compatible":
		return kiroAdapter{}, nil
	case "windsurf", "codeium", "windsurf_codeium", "codeium_compatible":
		return windsurfCodeiumAdapter{}, nil
	case "gemini", "gemini_compatible":
		return geminiNativeAdapter{}, nil
	default:
		return nil, upstreamUnavailable("unsupported_provider", "Provider type is not supported by this relay runtime.")
	}
}

func (adapter openaiCompatibleAdapter) PrepareRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	if isCodexOfficialRoute(route) {
		return codexCompatibleAdapter{}.PrepareRequest(original, route, body)
	}
	return prepareProviderRequest(original, route, body, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+route.APIKey)
	})
}

func (adapter openaiCompatibleAdapter) PrepareWebSocket(original *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error) {
	if isCodexOfficialRoute(route) {
		return codexCompatibleAdapter{}.PrepareWebSocket(original, route)
	}
	return prepareProviderWebSocket(original, route, func(header http.Header) {
		header.Set("Authorization", "Bearer "+route.APIKey)
	})
}

func (adapter openaiCompatibleAdapter) ParseUsage(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	return parseProviderUsageResult(endpoint, requestBody, responseBody)
}

func (adapter openaiCompatibleAdapter) ParseStreamEvent(endpoint string, event []byte, acc *streamMeteringAccumulator) {
	parseUsageFromStreamEvent(event, acc)
}

func (adapter codexCompatibleAdapter) PrepareRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	body = rewriteCodexOfficialRequestBody(original, route, body)
	req, cancel, err := prepareProviderRequest(original, route, body, func(req *http.Request) {
		applyCodexOfficialHeaders(req.Header, original, route)
	})
	if err != nil {
		return nil, cancel, err
	}
	if err := compressCodexResponsesRequest(req, original, route); err != nil {
		cancel()
		return nil, func() {}, err
	}
	return req, cancel, nil
}

func (adapter codexCompatibleAdapter) PrepareWebSocket(original *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error) {
	return prepareProviderWebSocket(original, route, func(header http.Header) {
		applyCodexOfficialHeaders(header, original, route)
		header.Del("Accept")
		header.Del("User-Agent")
		header.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
	})
}

func (adapter codexCompatibleAdapter) ParseUsage(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	return parseProviderUsageResult(endpoint, requestBody, responseBody)
}

func (adapter codexCompatibleAdapter) ParseStreamEvent(endpoint string, event []byte, acc *streamMeteringAccumulator) {
	parseUsageFromStreamEvent(event, acc)
}

func (adapter anthropicAdapter) PrepareRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	if isClaudeCodeRoute(route) {
		body = rewriteClaudeCodeMessagesRequestBody(original, route, body)
	}
	req, cancel, err := prepareProviderRequest(original, route, body, func(req *http.Request) {
		applyAnthropicAuth(req.Header, original.Header, route)
		if version := original.Header.Get("Anthropic-Version"); version != "" {
			req.Header.Set("Anthropic-Version", version)
		} else {
			req.Header.Set("Anthropic-Version", "2023-06-01")
		}
		if isClaudeCodeRoute(route) {
			applyClaudeCodeOfficialHeaders(req.Header, original, route)
		}
	})
	if err != nil {
		return nil, cancel, err
	}
	if err := applyAnthropicBetaPolicy(req.Header, route); err != nil {
		cancel()
		return nil, func() {}, err
	}
	return req, cancel, nil
}

func (adapter anthropicAdapter) PrepareWebSocket(original *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error) {
	upstreamURL, headers, dialer, err := prepareProviderWebSocket(original, route, func(header http.Header) {
		applyAnthropicAuth(header, original.Header, route)
		if version := original.Header.Get("Anthropic-Version"); version != "" {
			header.Set("Anthropic-Version", version)
		} else {
			header.Set("Anthropic-Version", "2023-06-01")
		}
		if isClaudeCodeRoute(route) {
			if isClaudeSessionsSubscribeWebSocketPath(original.URL.Path) {
				applyClaudeCodeSessionsSubscribeWebSocketHeaders(header)
			} else {
				applyClaudeCodeOfficialHeaders(header, original, route)
			}
		}
	})
	if err != nil {
		return "", nil, nil, err
	}
	if err := applyAnthropicBetaPolicy(headers, route); err != nil {
		return "", nil, nil, err
	}
	return upstreamURL, headers, dialer, nil
}

func applyAnthropicAuth(header http.Header, original http.Header, route routeInfo) {
	if strings.EqualFold(route.AuthScheme, "bearer") {
		header.Set("Authorization", "Bearer "+route.APIKey)
		header.Del("X-API-Key")
		if isClaudeCodeRoute(route) {
			if beta := original.Get("Anthropic-Beta"); beta != "" {
				header.Set("Anthropic-Beta", beta)
			}
			return
		}
		header.Set("Anthropic-Beta", appendAnthropicBeta(original.Get("Anthropic-Beta"), "oauth-2025-04-20"))
		return
	}
	header.Set("X-API-Key", route.APIKey)
	header.Del("Authorization")
	if beta := original.Get("Anthropic-Beta"); beta != "" {
		header.Set("Anthropic-Beta", beta)
	}
}

func appendAnthropicBeta(current string, beta string) string {
	return appendHeaderValue(current, beta)
}

func applyAnthropicBetaPolicy(header http.Header, route routeInfo) error {
	current := header.Get("Anthropic-Beta")
	if current == "" {
		return nil
	}
	tokens := splitHeaderTokens(current)
	if len(tokens) == 0 {
		header.Del("Anthropic-Beta")
		return nil
	}

	blocked := routeMetadataTokenSet(route, "claude",
		"blocked_betas", "deny_betas", "forbidden_betas", "beta_blocklist",
	)
	for _, token := range tokens {
		if _, ok := blocked[strings.ToLower(token)]; ok {
			return badRequest("Anthropic beta token is blocked by route policy.")
		}
	}

	dropped := routeMetadataTokenSet(route, "claude",
		"drop_betas", "filter_betas", "blocked_betas_filter", "beta_drop", "beta_filter",
	)
	allowed := routeMetadataTokenSet(route, "claude",
		"allowed_betas", "allow_betas", "beta_allowlist",
	)
	if len(dropped) == 0 && len(allowed) == 0 {
		return nil
	}

	filtered := make([]string, 0, len(tokens))
	for _, token := range tokens {
		key := strings.ToLower(token)
		if _, ok := dropped[key]; ok {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[key]; !ok {
				continue
			}
		}
		filtered = append(filtered, token)
	}
	if len(filtered) == 0 {
		header.Del("Anthropic-Beta")
		return nil
	}
	header.Set("Anthropic-Beta", strings.Join(filtered, ","))
	return nil
}

func isCodexOfficialRoute(route routeInfo) bool {
	return strings.EqualFold(route.ProviderType, "codex_compatible") ||
		strings.EqualFold(route.AuthMode, "codex_cli") ||
		strings.EqualFold(route.TokenProvider, "openai_codex")
}

func applyCodexOfficialHeaders(header http.Header, original *http.Request, route routeInfo) {
	copyOriginalHeaders(header, original.Header, codexOfficialPassHeaders...)

	header.Set("Authorization", "Bearer "+route.APIKey)
	header.Del("X-API-Key")

	originator := firstNonEmpty(routeMetadataString(route, "codex", "originator"), "codex_exec")
	version := codexClientVersion(route)
	header.Set("originator", originator)
	header.Set("version", version)
	header.Set("User-Agent", codexUserAgent(route, originator, version))

	if accountID := firstNonEmpty(
		routeMetadataString(route, "codex", "chatgpt_account_id", "account_id", "workspace_id"),
		route.TokenSubject,
	); accountID != "" {
		header.Set("ChatGPT-Account-ID", accountID)
	}
	if routeMetadataBool(route, "codex", "chatgpt_account_is_fedramp", "is_fedramp_account", "fedramp") {
		header.Set("X-OpenAI-Fedramp", "true")
	}
	if residency := routeMetadataString(route, "codex", "residency", "codex_residency"); residency != "" {
		header.Set("x-openai-internal-codex-residency", residency)
	}
	if org := routeMetadataString(route, "codex", "organization", "organization_id", "openai_organization"); org != "" {
		header.Set("OpenAI-Organization", org)
	}
	if project := routeMetadataString(route, "codex", "project", "project_id", "openai_project"); project != "" {
		header.Set("OpenAI-Project", project)
	}

	sessionID := codexConversationID(original, route)
	if sessionID != "" {
		header.Set("session_id", sessionID)
		if header.Get("x-client-request-id") == "" {
			header.Set("x-client-request-id", sessionID)
		}
	}
	if installationID := codexInstallationID(route); installationID != "" && isCodexCompactPath(original.URL.Path) {
		header.Set("x-codex-installation-id", installationID)
	}
	if windowID := codexWindowID(original, route, sessionID); windowID != "" {
		header.Set("x-codex-window-id", windowID)
	}
	if header.Get("x-codex-turn-metadata") == "" {
		if turnMetadata := codexTurnMetadata(original, route, sessionID); turnMetadata != "" {
			header.Set("x-codex-turn-metadata", turnMetadata)
		}
	}
	if routeMetadataBool(route, "codex", "include_timing_metrics", "responsesapi_include_timing_metrics") && header.Get("x-responsesapi-include-timing-metrics") == "" {
		header.Set("x-responsesapi-include-timing-metrics", "true")
	}
	if isCodexResponsesPath(original.URL.Path) && original.Method != http.MethodGet && header.Get("Accept") == "" {
		header.Set("Accept", "text/event-stream")
	} else if !isCodexResponsesPath(original.URL.Path) && original.Method == http.MethodGet && header.Get("Accept") == "" {
		header.Set("Accept", "*/*")
	}
	header.Del("Accept-Encoding")
}

var codexOfficialPassHeaders = []string{
	"OpenAI-Beta",
	"x-client-request-id",
	"session_id",
	"x-codex-beta-features",
	"x-codex-turn-state",
	"x-codex-turn-metadata",
	"x-codex-parent-thread-id",
	"x-codex-window-id",
	"x-openai-memgen-request",
	"x-openai-subagent",
	"x-responsesapi-include-timing-metrics",
}

func rewriteCodexOfficialRequestBody(original *http.Request, route routeInfo, body []byte) []byte {
	if isCodexResponsesPath(original.URL.Path) {
		return rewriteCodexResponsesRequestBody(original, route, body)
	}
	if isCodexCompactPath(original.URL.Path) {
		return rewriteCodexCompactRequestBody(original, route, body)
	}
	return body
}

func rewriteCodexOfficialWebSocketFrame(original *http.Request, route routeInfo, frame []byte) []byte {
	if original == nil {
		return frame
	}
	if !isCodexResponsesPath(original.URL.Path) {
		return frame
	}
	var payload map[string]any
	if err := json.Unmarshal(frame, &payload); err != nil {
		return frame
	}
	if payload["type"] != "response.create" {
		return frame
	}
	changed := rewriteCodexWebSocketResponseCreatePayload(original, route, payload)
	if ensureCodexWebSocketClientMetadata(original, route, payload) {
		changed = true
	}
	if !changed {
		return frame
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return frame
	}
	return rewritten
}

func rewriteCodexResponsesRequestBody(original *http.Request, route routeInfo, body []byte) []byte {
	if !isCodexResponsesPath(original.URL.Path) {
		return body
	}
	mediaType, _, _ := mime.ParseMediaType(original.Header.Get("Content-Type"))
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	changed := rewriteCodexResponseCreatePayload(original, route, payload)
	if ensureCodexHTTPClientMetadata(original, route, payload) {
		changed = true
	}
	if !changed {
		return body
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return rewritten
}

func rewriteCodexResponseCreatePayload(original *http.Request, route routeInfo, payload map[string]any) bool {
	return rewriteCodexResponseCreatePayloadWithOptions(original, route, payload, true)
}

func rewriteCodexWebSocketResponseCreatePayload(original *http.Request, route routeInfo, payload map[string]any) bool {
	return rewriteCodexResponseCreatePayloadWithOptions(original, route, payload, false)
}

func rewriteCodexResponseCreatePayloadWithOptions(original *http.Request, route routeInfo, payload map[string]any, includeInstructions bool) bool {
	changed := false
	if normalizeCodexLegacyRequestPayload(payload) {
		changed = true
	}
	if upstreamModel := strings.TrimSpace(route.UpstreamModel); upstreamModel != "" {
		if _, ok := payload["model"]; ok && metadataText(payload["model"]) != upstreamModel {
			payload["model"] = upstreamModel
			changed = true
		}
	}
	if _, ok := payload["stream"]; !ok {
		payload["stream"] = true
		changed = true
	}
	if includeInstructions {
		if instructions, ok := payload["instructions"].(string); !ok || strings.TrimSpace(instructions) == "" {
			instructions := strings.TrimSpace(codexInstructionsFromPayload(payload))
			if instructions == "" {
				instructions = codexInstructions(route)
			}
			payload["instructions"] = instructions
			changed = true
		}
	}
	if _, ok := payload["tools"]; !ok {
		if tools, ok := routeMetadataArray(route, "codex", "tools", "default_tools"); ok {
			payload["tools"] = tools
		} else {
			payload["tools"] = []any{}
		}
		changed = true
	}
	if _, ok := payload["reasoning"]; !ok {
		payload["reasoning"] = codexReasoning(route)
		changed = true
	}
	if sessionID := codexConversationID(original, route); sessionID != "" {
		if _, ok := payload["prompt_cache_key"]; !ok {
			payload["prompt_cache_key"] = sessionID
			changed = true
		}
	}
	if value, ok := payload["store"].(bool); !ok || value {
		payload["store"] = false
		changed = true
	}
	if _, ok := payload["parallel_tool_calls"]; !ok {
		payload["parallel_tool_calls"] = codexParallelToolCalls(route)
		changed = true
	}
	if _, ok := payload["tool_choice"]; !ok {
		payload["tool_choice"] = "auto"
		changed = true
	}
	if _, ok := payload["include"]; !ok {
		if payload["reasoning"] != nil {
			payload["include"] = []any{"reasoning.encrypted_content"}
		} else {
			payload["include"] = []any{}
		}
		changed = true
	}
	if text, ok := routeMetadataObject(route, "codex", "text", "default_text"); ok {
		if _, exists := payload["text"]; !exists {
			payload["text"] = text
			changed = true
		}
	}
	if serviceTier := routeMetadataString(route, "codex", "service_tier", "default_service_tier"); serviceTier != "" {
		if _, exists := payload["service_tier"]; !exists {
			payload["service_tier"] = serviceTier
			changed = true
		}
	}
	if normalizeCodexResponseInput(payload) {
		changed = true
	}
	return changed
}

func ensureCodexHTTPClientMetadata(original *http.Request, route routeInfo, payload map[string]any) bool {
	if installationID := codexInstallationID(route); installationID != "" {
		clientMetadata, ok := objectValue(payload["client_metadata"])
		if !ok {
			clientMetadata = map[string]any{}
			payload["client_metadata"] = clientMetadata
		}
		if _, ok := clientMetadata["x-codex-installation-id"]; !ok {
			clientMetadata["x-codex-installation-id"] = installationID
			return true
		}
	}
	return false
}

func rewriteCodexCompactRequestBody(original *http.Request, route routeInfo, body []byte) []byte {
	mediaType, _, _ := mime.ParseMediaType(original.Header.Get("Content-Type"))
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	changed := false
	for _, key := range []string{"store", "stream"} {
		if _, ok := payload[key]; ok {
			delete(payload, key)
			changed = true
		}
	}
	if instructions, ok := payload["instructions"].(string); !ok || strings.TrimSpace(instructions) == "" {
		payload["instructions"] = codexInstructions(route)
		changed = true
	}
	if _, ok := payload["tools"]; !ok {
		if tools, ok := routeMetadataArray(route, "codex", "tools", "default_tools"); ok {
			payload["tools"] = tools
		} else {
			payload["tools"] = []any{}
		}
		changed = true
	}
	if _, ok := payload["parallel_tool_calls"]; !ok {
		payload["parallel_tool_calls"] = codexParallelToolCalls(route)
		changed = true
	}
	if _, ok := payload["reasoning"]; !ok {
		if reasoning := codexReasoning(route); reasoning != nil {
			payload["reasoning"] = reasoning
			changed = true
		}
	}
	if !changed {
		return body
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return rewritten
}

func normalizeCodexResponseInput(payload map[string]any) bool {
	input, ok := payload["input"].([]any)
	if !ok {
		return false
	}
	filtered := make([]any, 0, len(input))
	changed := false
	for _, item := range input {
		if object, ok := item.(map[string]any); ok {
			if strings.EqualFold(metadataText(object["type"]), "reasoning") {
				changed = true
				continue
			}
			if normalized, normalizedChanged := normalizeCodexInputItem(object); normalizedChanged {
				if expanded, ok := normalized.([]any); ok {
					filtered = append(filtered, expanded...)
				} else {
					filtered = append(filtered, normalized)
				}
				changed = true
				continue
			}
		}
		filtered = append(filtered, item)
	}
	if changed {
		payload["input"] = filtered
	}
	return changed
}

func normalizeCodexLegacyRequestPayload(payload map[string]any) bool {
	changed := false
	if _, hasInput := payload["input"]; !hasInput {
		if messages, ok := arrayValue(payload["messages"]); ok {
			payload["input"] = messages
			delete(payload, "messages")
			changed = true
		}
	}
	if _, hasTools := payload["tools"]; !hasTools {
		if functions, ok := arrayValue(payload["functions"]); ok {
			tools := make([]any, 0, len(functions))
			for _, fn := range functions {
				tools = append(tools, map[string]any{"type": "function", "function": fn})
			}
			payload["tools"] = tools
			delete(payload, "functions")
			changed = true
		}
	}
	if _, hasChoice := payload["tool_choice"]; !hasChoice {
		if functionCall, ok := payload["function_call"]; ok {
			payload["tool_choice"] = codexToolChoiceFromLegacyFunctionCall(functionCall)
			delete(payload, "function_call")
			changed = true
		}
	}
	return changed
}

func codexToolChoiceFromLegacyFunctionCall(value any) any {
	switch typed := value.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "", "auto":
			return "auto"
		case "none":
			return "none"
		default:
			return map[string]any{"type": "function", "function": map[string]any{"name": typed}}
		}
	case map[string]any:
		if name := metadataText(typed["name"]); name != "" {
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		}
	}
	return "auto"
}

func codexInstructionsFromPayload(payload map[string]any) string {
	input, ok := arrayValue(payload["input"])
	if !ok {
		return ""
	}
	parts := []string{}
	for _, item := range input {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(metadataText(object["role"])))
		itemType := strings.ToLower(strings.TrimSpace(metadataText(object["type"])))
		if role != "system" && role != "developer" && itemType != "system" && itemType != "developer" {
			continue
		}
		if text := codexContentText(object["content"]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func normalizeCodexInputItem(object map[string]any) (any, bool) {
	role := strings.ToLower(strings.TrimSpace(metadataText(object["role"])))
	itemType := strings.ToLower(strings.TrimSpace(metadataText(object["type"])))
	if role == "tool" || itemType == "tool_result" {
		callID := firstNonEmpty(metadataText(object["call_id"]), metadataText(object["tool_call_id"]), metadataText(object["id"]))
		if callID == "" {
			return object, false
		}
		return map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  codexContentText(object["content"]),
		}, true
	}
	if role == "assistant" {
		if toolCalls, ok := arrayValue(object["tool_calls"]); ok && len(toolCalls) > 0 {
			items := make([]any, 0, len(toolCalls))
			for _, call := range toolCalls {
				callObject, ok := call.(map[string]any)
				if !ok {
					continue
				}
				function, _ := objectValue(callObject["function"])
				name := firstNonEmpty(metadataText(function["name"]), metadataText(callObject["name"]))
				if name == "" {
					continue
				}
				items = append(items, map[string]any{
					"type":      "function_call",
					"call_id":   firstNonEmpty(metadataText(callObject["id"]), metadataText(callObject["call_id"])),
					"name":      name,
					"arguments": firstNonEmpty(metadataText(function["arguments"]), metadataText(callObject["arguments"]), "{}"),
				})
			}
			if len(items) == 1 {
				return items[0], true
			}
			if len(items) > 1 {
				return items, true
			}
		}
	}
	return object, false
}

func codexContentText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		parts := []string{}
		for _, item := range typed {
			if object, ok := item.(map[string]any); ok {
				if text := metadataText(object["text"]); text != "" {
					parts = append(parts, text)
					continue
				}
				if text := metadataText(object["content"]); text != "" {
					parts = append(parts, text)
				}
			} else if text := metadataText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return metadataText(value)
	}
}

func codexInstructions(route routeInfo) string {
	if instructions := routeMetadataText(route, "codex", "instructions", "base_instructions", "model_instructions"); instructions != "" {
		return instructions
	}
	return "You are Codex, a coding agent based on GPT-5. You and the user share the same workspace and collaborate to achieve the user's goals."
}

func codexReasoning(route routeInfo) any {
	if reasoning, ok := routeMetadataObject(route, "codex", "reasoning", "default_reasoning"); ok {
		return reasoning
	}
	if routeMetadataBool(route, "codex", "disable_reasoning", "reasoning_disabled", "disable_default_reasoning") {
		return nil
	}
	if supportsReasoningSummaries, ok := routeMetadataBoolValue(route, "codex", "supports_reasoning_summaries", "model_supports_reasoning_summaries"); ok && !supportsReasoningSummaries {
		return nil
	}
	effort := firstNonEmpty(
		routeMetadataString(route, "codex", "reasoning_effort", "default_reasoning_effort", "model_reasoning_effort", "default_reasoning_level", "model_reasoning_level"),
		"low",
	)
	if strings.EqualFold(effort, "none") {
		return nil
	}
	summary := firstNonEmpty(
		routeMetadataString(route, "codex", "reasoning_summary", "default_reasoning_summary", "model_reasoning_summary"),
		"auto",
	)
	reasoning := map[string]any{"effort": effort}
	if !strings.EqualFold(summary, "none") {
		reasoning["summary"] = summary
	}
	return reasoning
}

func codexParallelToolCalls(route routeInfo) bool {
	if value, ok := routeMetadataBoolValue(route, "codex", "parallel_tool_calls", "supports_parallel_tool_calls"); ok {
		return value
	}
	return true
}

func routeMetadataBoolValue(route routeInfo, namespace string, keys ...string) (bool, bool) {
	for _, metadata := range routeMetadataMaps(route) {
		value := metadataValue(metadata, namespace, keys...)
		if parsed, ok := metadataBool(value); ok {
			return parsed, true
		}
	}
	return false, false
}

func codexWebSocketClientMetadata(original *http.Request, route routeInfo) map[string]string {
	clientMetadata := map[string]string{}
	if installationID := codexInstallationID(route); installationID != "" {
		clientMetadata["x-codex-installation-id"] = installationID
	}
	sessionID := codexConversationID(original, route)
	if windowID := codexWindowID(original, route, sessionID); windowID != "" {
		clientMetadata["x-codex-window-id"] = windowID
	}
	if subagent := firstNonEmpty(
		original.Header.Get("x-openai-subagent"),
		routeMetadataString(route, "codex", "subagent", "openai_subagent", "x_openai_subagent"),
	); subagent != "" {
		clientMetadata["x-openai-subagent"] = subagent
	}
	if parentThreadID := firstNonEmpty(
		original.Header.Get("x-codex-parent-thread-id"),
		routeMetadataString(route, "codex", "parent_thread_id", "codex_parent_thread_id", "x_codex_parent_thread_id"),
	); parentThreadID != "" {
		clientMetadata["x-codex-parent-thread-id"] = parentThreadID
	}
	if turnMetadata := firstNonEmpty(original.Header.Get("x-codex-turn-metadata"), codexTurnMetadata(original, route, sessionID)); turnMetadata != "" && json.Valid([]byte(turnMetadata)) {
		clientMetadata["x-codex-turn-metadata"] = turnMetadata
	}
	if traceparent := firstNonEmpty(original.Header.Get("traceparent"), routeMetadataString(route, "codex", "traceparent", "ws_request_header_traceparent")); traceparent != "" {
		clientMetadata["ws_request_header_traceparent"] = traceparent
	}
	if tracestate := firstNonEmpty(original.Header.Get("tracestate"), routeMetadataString(route, "codex", "tracestate", "ws_request_header_tracestate")); tracestate != "" {
		clientMetadata["ws_request_header_tracestate"] = tracestate
	}
	if extra, ok := routeMetadataObject(route, "codex", "ws_client_metadata", "websocket_client_metadata"); ok {
		for key, value := range extra {
			if text := metadataString(value); text != "" {
				if _, exists := clientMetadata[key]; !exists {
					clientMetadata[key] = text
				}
			}
		}
	}
	return clientMetadata
}

func ensureCodexWebSocketClientMetadata(original *http.Request, route routeInfo, payload map[string]any) bool {
	metadata := codexWebSocketClientMetadata(original, route)
	if len(metadata) == 0 {
		return false
	}
	clientMetadata, ok := objectValue(payload["client_metadata"])
	changed := false
	if !ok {
		clientMetadata = map[string]any{}
		payload["client_metadata"] = clientMetadata
		changed = true
	}
	for key, value := range metadata {
		if _, exists := clientMetadata[key]; !exists {
			clientMetadata[key] = value
			changed = true
		}
	}
	return changed
}

func isCodexResponsesPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return strings.HasSuffix(path, "/responses")
}

func isCodexCompactPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return strings.HasSuffix(path, "/responses/compact")
}

func isCodexModelsPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return strings.HasSuffix(path, "/models")
}

func compressCodexResponsesRequest(req *http.Request, original *http.Request, route routeInfo) error {
	if !isCodexResponsesPath(original.URL.Path) || req.Body == nil {
		return nil
	}
	if routeMetadataBool(route, "codex", "disable_request_compression", "disable_body_compression") {
		return nil
	}
	if req.Header.Get("Content-Encoding") != "" {
		return nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return badRequest("Could not read upstream request body.")
	}
	_ = req.Body.Close()
	if len(body) == 0 {
		req.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	var compressed bytes.Buffer
	writer, err := zstd.NewWriter(&compressed)
	if err != nil {
		return upstreamUnavailable("codex_request_compression_failed", "Could not initialize Codex request compression.")
	}
	if _, err := writer.Write(body); err != nil {
		_ = writer.Close()
		return upstreamUnavailable("codex_request_compression_failed", "Could not compress Codex request body.")
	}
	if err := writer.Close(); err != nil {
		return upstreamUnavailable("codex_request_compression_failed", "Could not compress Codex request body.")
	}
	compressedBody := compressed.Bytes()
	req.Body = io.NopCloser(bytes.NewReader(compressedBody))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(compressedBody)), nil
	}
	req.ContentLength = int64(len(compressedBody))
	req.Header.Set("Content-Encoding", "zstd")
	return nil
}

func codexConversationID(original *http.Request, route routeInfo) string {
	return firstNonEmpty(
		sessionValueFromHeader(original.Header, "session_id", "x-client-request-id", "X-Elucid-Relay-Session", "X-Relay-Session", "X-Subrouter-Session", "X-Codex-Session-ID"),
		sessionValueFromQuery(original, "session_id", "conversation_id", "thread_id"),
		normalizeRouteAffinityKey(routeMetadataString(route, "codex", "session_id", "conversation_id", "thread_id")),
		normalizeRouteAffinityKey(requestIDFromContext(original.Context())),
	)
}

func codexInstallationID(route routeInfo) string {
	return routeMetadataString(route, "codex", "installation_id", "codex_installation_id")
}

func codexClientVersion(route routeInfo) string {
	return firstNonEmpty(routeMetadataString(route, "codex", "client_version", "version"), "0.0.0")
}

func codexWindowID(original *http.Request, route routeInfo, sessionID string) string {
	if windowID := firstNonEmpty(
		normalizeRouteAffinityKey(original.Header.Get("x-codex-window-id")),
		normalizeRouteAffinityKey(routeMetadataString(route, "codex", "window_id", "codex_window_id")),
	); windowID != "" {
		return windowID
	}
	if sessionID != "" {
		return sessionID + ":0"
	}
	return ""
}

func codexTurnMetadata(original *http.Request, route routeInfo, sessionID string) string {
	if exact := routeMetadataString(route, "codex", "turn_metadata_header", "x_codex_turn_metadata", "turn_metadata_json"); exact != "" && json.Valid([]byte(exact)) {
		return exact
	}
	if sessionID == "" {
		return ""
	}
	payload := map[string]any{
		"session_id":    sessionID,
		"thread_source": firstNonEmpty(routeMetadataString(route, "codex", "thread_source"), "user"),
		"turn_id":       firstNonEmpty(routeMetadataString(route, "codex", "turn_id"), normalizeRouteAffinityKey(requestIDFromContext(original.Context()))),
		"sandbox":       firstNonEmpty(routeMetadataString(route, "codex", "sandbox"), "seccomp"),
	}
	if extra, ok := routeMetadataObject(route, "codex", "turn_metadata", "responsesapi_client_metadata"); ok {
		for key, value := range extra {
			if _, exists := payload[key]; !exists {
				payload[key] = value
			}
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func codexUserAgent(route routeInfo, originator string, version string) string {
	if userAgent := routeMetadataString(route, "codex", "user_agent"); userAgent != "" {
		return userAgent
	}
	osName := firstNonEmpty(routeMetadataString(route, "codex", "os_name"), codexOSName())
	osVersion := firstNonEmpty(routeMetadataString(route, "codex", "os_version"), "24.4.0")
	terminal := firstNonEmpty(routeMetadataString(route, "codex", "terminal", "terminal_user_agent"), "xterm-256color ("+originator+"; "+version+")")
	return originator + "/" + version + " (" + osName + " " + osVersion + "; " + codexArch() + ") " + terminal
}

func codexOSName() string {
	switch runtime.GOOS {
	case "linux":
		return "Ubuntu"
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}

func codexArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return runtime.GOARCH
	}
}

func isClaudeCodeRoute(route routeInfo) bool {
	return strings.EqualFold(route.AuthMode, "claude_cli") ||
		strings.EqualFold(route.TokenProvider, "anthropic_claude")
}

func isGitHubCopilotRoute(route routeInfo) bool {
	return strings.EqualFold(route.ProviderType, "github_copilot") ||
		strings.EqualFold(route.ProviderType, "github_copilot_compatible") ||
		strings.EqualFold(route.ProviderType, "copilot") ||
		strings.EqualFold(route.TokenProvider, "github_copilot")
}

func (adapter githubCopilotAdapter) PrepareRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	req, cancel, err := prepareProviderRequest(original, route, body, func(req *http.Request) {
		applyGitHubCopilotOfficialHeaders(req.Header, original, route)
	})
	if err != nil {
		return nil, cancel, err
	}
	if original.Method != http.MethodGet && original.Header.Get("X-Initiator") == "" && routeMetadataString(route, "github", "initiator", "x_initiator") == "" {
		req.Header.Set("X-Initiator", githubCopilotInitiatorFromBody(body, original.Header.Get("Content-Type")))
	}
	if githubCopilotVisionRequestFromBody(body, original.Header.Get("Content-Type")) {
		req.Header.Set("copilot-vision-request", "true")
	}
	return req, cancel, nil
}

func (adapter githubCopilotAdapter) PrepareWebSocket(original *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error) {
	return prepareProviderWebSocket(original, route, func(header http.Header) {
		applyGitHubCopilotOfficialHeaders(header, original, route)
	})
}

func (adapter githubCopilotAdapter) ParseUsage(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	return parseProviderUsageResult(endpoint, requestBody, responseBody)
}

func (adapter githubCopilotAdapter) ParseStreamEvent(endpoint string, event []byte, acc *streamMeteringAccumulator) {
	parseUsageFromStreamEvent(event, acc)
}

func applyGitHubCopilotOfficialHeaders(header http.Header, original *http.Request, route routeInfo) {
	copyOriginalHeaders(header, original.Header, githubCopilotOfficialPassHeaders...)

	header.Set("Authorization", "Bearer "+route.APIKey)
	header.Del("X-API-Key")

	if header.Get("Content-Type") == "" && original.Method != http.MethodGet {
		header.Set("Content-Type", "application/json")
	}
	if header.Get("Accept") == "" {
		header.Set("Accept", "application/json")
	}
	header.Set("User-Agent", githubCopilotUserAgent(route))
	header.Set("copilot-integration-id", firstNonEmpty(routeMetadataString(route, "github", "copilot_integration_id", "integration_id"), "vscode-chat"))
	header.Set("editor-version", "vscode/"+githubCopilotVSCodeVersion(route))
	header.Set("editor-plugin-version", "copilot-chat/"+githubCopilotClientVersion(route))
	intent := githubCopilotIntent(original, route)
	header.Set("openai-intent", intent)
	header.Set("x-github-api-version", githubCopilotAPIVersion(route))
	header.Set("x-vscode-user-agent-library-version", firstNonEmpty(routeMetadataString(route, "github", "vscode_user_agent_library_version", "fetch_library_version"), "electron-fetch"))
	if header.Get("x-onbehalf-extension-id") == "" {
		if extensionID := githubCopilotOnBehalfExtensionID(original, route); extensionID != "" {
			header.Set("x-onbehalf-extension-id", extensionID)
		}
	}

	requestID := firstNonEmpty(header.Get("x-request-id"), requestIDFromContext(original.Context()))
	if header.Get("x-request-id") == "" {
		header.Set("x-request-id", requestID)
	}
	if original.Method != http.MethodGet {
		if header.Get("x-interaction-id") == "" {
			header.Set("x-interaction-id", githubCopilotInteractionID(original, route, requestID))
		}
		if header.Get("x-interaction-type") == "" {
			header.Set("x-interaction-type", githubCopilotInteractionType(route, intent))
		}
		if header.Get("x-agent-task-id") == "" {
			header.Set("x-agent-task-id", githubCopilotAgentTaskID(route, requestID))
		}
	}
	if original.Method != http.MethodGet && header.Get("X-Initiator") == "" {
		header.Set("X-Initiator", firstNonEmpty(routeMetadataString(route, "github", "initiator", "x_initiator"), "user"))
	}
	if githubCopilotVisionRequest(original, route) {
		header.Set("copilot-vision-request", "true")
	}
	header.Del("Accept-Encoding")
}

var githubCopilotOfficialPassHeaders = []string{
	"X-Initiator",
	"x-request-id",
	"x-interaction-id",
	"x-interaction-type",
	"x-agent-task-id",
	"x-onbehalf-extension-id",
	"copilot-vision-request",
}

func githubCopilotUserAgent(route routeInfo) string {
	if userAgent := routeMetadataString(route, "github", "user_agent", "copilot_user_agent"); userAgent != "" {
		return userAgent
	}
	return "GitHubCopilotChat/" + githubCopilotClientVersion(route)
}

func githubCopilotClientVersion(route routeInfo) string {
	return firstNonEmpty(routeMetadataString(route, "github", "client_version", "copilot_version", "version"), "0.44.0")
}

func githubCopilotVSCodeVersion(route routeInfo) string {
	return firstNonEmpty(routeMetadataString(route, "github", "vscode_version", "editor_version"), "1.109.3")
}

func githubCopilotAPIVersion(route routeInfo) string {
	return firstNonEmpty(routeMetadataString(route, "github", "api_version", "github_api_version"), "2025-05-01")
}

func githubCopilotIntent(original *http.Request, route routeInfo) string {
	if intent := routeMetadataString(route, "github", "openai_intent", "intent"); intent != "" {
		return intent
	}
	if original != nil && original.URL != nil && endpointFromPath(original.URL.Path) == "responses" {
		return "responses-proxy"
	}
	return "conversation-panel"
}

func githubCopilotInteractionType(route routeInfo, intent string) string {
	return firstNonEmpty(routeMetadataString(route, "github", "interaction_type", "x_interaction_type"), intent)
}

func githubCopilotInteractionID(original *http.Request, route routeInfo, requestID string) string {
	return firstNonEmpty(
		routeMetadataString(route, "github", "interaction_id", "x_interaction_id"),
		original.Header.Get("X-Elucid-Relay-Session"),
		requestID,
	)
}

func githubCopilotAgentTaskID(route routeInfo, requestID string) string {
	return firstNonEmpty(routeMetadataString(route, "github", "agent_task_id", "x_agent_task_id"), requestID)
}

func githubCopilotOnBehalfExtensionID(original *http.Request, route routeInfo) string {
	if value := firstNonEmpty(
		original.Header.Get("x-onbehalf-extension-id"),
		routeMetadataString(route, "github", "onbehalf_extension_id", "x_onbehalf_extension_id"),
	); value != "" {
		return value
	}
	extensionID := firstNonEmpty(routeMetadataString(route, "github", "extension_id", "copilot_extension_id"))
	if extensionID == "" {
		return ""
	}
	extensionVersion := firstNonEmpty(routeMetadataString(route, "github", "extension_version", "copilot_extension_version"), githubCopilotClientVersion(route))
	if extensionVersion == "" {
		return extensionID
	}
	return extensionID + "/" + extensionVersion
}

func githubCopilotVisionRequest(original *http.Request, route routeInfo) bool {
	if value, ok := routeMetadataBoolValue(route, "github", "vision_request", "copilot_vision_request"); ok {
		return value
	}
	return strings.Contains(strings.ToLower(original.Header.Get("X-Elucid-Relay-Modalities")), "image")
}

func githubCopilotVisionRequestFromBody(body []byte, contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return false
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return jsonTreeContainsString(payload, "image_url") || jsonTreeContainsString(payload, "input_image")
}

func githubCopilotInitiatorFromBody(body []byte, contentType string) string {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return "user"
	}
	var payload struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "user"
	}
	for _, message := range payload.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "assistant" || role == "tool" {
			return "agent"
		}
	}
	return "user"
}

func jsonTreeContainsString(value any, needle string) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if strings.EqualFold(strings.TrimSpace(key), needle) {
				return true
			}
			if jsonTreeContainsString(child, needle) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if jsonTreeContainsString(child, needle) {
				return true
			}
		}
	case string:
		return strings.EqualFold(strings.TrimSpace(v), needle)
	}
	return false
}

func rewriteClaudeCodeMessagesRequestBody(original *http.Request, route routeInfo, body []byte) []byte {
	isMessages := isClaudeMessagesPath(original.URL.Path)
	isCountTokens := isClaudeMessagesCountTokensPath(original.URL.Path)
	if !isMessages && !isCountTokens {
		return body
	}
	mediaType, _, _ := mime.ParseMediaType(original.Header.Get("Content-Type"))
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}

	changed := stripClaudeThinkingCacheControl(payload)
	if isMessages {
		if _, ok := payload["stream"]; !ok {
			payload["stream"] = true
			changed = true
		}
		if _, ok := payload["max_tokens"]; !ok {
			payload["max_tokens"] = float64(32000)
			changed = true
		}
		if _, ok := payload["thinking"]; !ok {
			payload["thinking"] = map[string]any{"type": "adaptive"}
			changed = true
		}
		if _, ok := payload["context_management"]; !ok {
			payload["context_management"] = map[string]any{
				"edits": []any{
					map[string]any{
						"type": "clear_thinking_20251015",
						"keep": "all",
					},
				},
			}
			changed = true
		}
	}
	sessionID := claudeCodeSessionID(original, route)
	if ensureClaudeCodeMetadataUserID(payload, original, route, sessionID) {
		changed = true
	}
	if prependClaudeCodeBillingSystem(payload, claudeCodeBillingHeader(route)) {
		changed = true
	}
	if syncClaudeCodeBillingHeaderVersion(payload, route) {
		changed = true
	}
	willSignCCH := claudeCodeBillingCCHSigningEnabled(route) && claudePayloadContainsBillingCCHPlaceholder(payload)
	if !changed && !willSignCCH {
		return body
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	if willSignCCH {
		rewritten = signClaudeCodeBillingCCH(rewritten)
	}
	return rewritten
}

func isClaudeMessagesPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return strings.HasSuffix(path, "/messages")
}

func isClaudeMessagesCountTokensPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return strings.HasSuffix(path, "/messages/count_tokens")
}

func stripClaudeThinkingCacheControl(value any) bool {
	changed := false
	switch typed := value.(type) {
	case map[string]any:
		if blockType, _ := typed["type"].(string); isClaudeThinkingBlockType(blockType) {
			if _, ok := typed["cache_control"]; ok {
				delete(typed, "cache_control")
				changed = true
			}
		}
		for _, child := range typed {
			if stripClaudeThinkingCacheControl(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range typed {
			if stripClaudeThinkingCacheControl(child) {
				changed = true
			}
		}
	}
	return changed
}

func isClaudeThinkingBlockType(blockType string) bool {
	switch strings.ToLower(strings.TrimSpace(blockType)) {
	case "thinking", "redacted_thinking":
		return true
	default:
		return false
	}
}

func shouldRetryClaudeSignatureRepair(route routeInfo, status int, body []byte) bool {
	return status == http.StatusBadRequest &&
		isClaudeCodeRoute(route) &&
		routeMetadataBool(route, "claude", "signature_repair_retry", "repair_thinking_signature", "enable_signature_repair") &&
		isAnthropicSignatureErrorText(string(body))
}

func repairClaudeThinkingBlocksForRetry(body []byte) ([]byte, bool) {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false
	}
	_, changed := filterClaudeThinkingBlocks(payload)
	if !changed {
		return body, false
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return rewritten, true
}

func filterClaudeThinkingBlocks(value any) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if blockType, _ := typed["type"].(string); isClaudeThinkingBlockType(blockType) {
			return nil, true
		}
		changed := false
		for key, child := range typed {
			filteredChild, childChanged := filterClaudeThinkingBlocks(child)
			if childChanged {
				changed = true
				if filteredChild == nil {
					delete(typed, key)
				} else {
					typed[key] = filteredChild
				}
			}
		}
		return typed, changed
	case []any:
		changed := false
		filtered := make([]any, 0, len(typed))
		for _, child := range typed {
			filteredChild, childChanged := filterClaudeThinkingBlocks(child)
			if childChanged {
				changed = true
				if filteredChild == nil {
					continue
				}
			}
			filtered = append(filtered, filteredChild)
		}
		if changed {
			return filtered, true
		}
		return typed, false
	default:
		return value, false
	}
}

func isClaudeFilesPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return path == "/v1/files" || strings.HasPrefix(path, "/v1/files/")
}

func isClaudeMCPServersPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return path == "/v1/mcp_servers" || strings.HasPrefix(path, "/v1/mcp_servers/")
}

func isClaudeSessionsPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return path == "/v1/sessions" || strings.HasPrefix(path, "/v1/sessions/")
}

func isClaudeSessionsSubscribeWebSocketPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return strings.HasPrefix(path, "/v1/sessions/ws/") && strings.HasSuffix(path, "/subscribe")
}

func isClaudeCodeSessionsPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return path == "/v1/code/sessions" || strings.HasPrefix(path, "/v1/code/sessions/")
}

func isClaudeSessionIngressPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return path == "/v1/session_ingress" || strings.HasPrefix(path, "/v1/session_ingress/")
}

func isClaudeEnvironmentsPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return path == "/v1/environments" || strings.HasPrefix(path, "/v1/environments/")
}

func isClaudeEnvironmentProvidersPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return path == "/v1/environment_providers" || strings.HasPrefix(path, "/v1/environment_providers/")
}

func isClaudeOAuthAPIPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return path == "/api/oauth/profile" ||
		strings.HasPrefix(path, "/api/oauth/") ||
		path == "/api/claude_cli_profile"
}

func isClaudeOAuthFileContentPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return strings.HasPrefix(path, "/api/oauth/files/") && strings.HasSuffix(path, "/content")
}

func isClaudeCodeOfficialSidecarPath(path string) bool {
	return isClaudeFilesPath(path) ||
		isClaudeMCPServersPath(path) ||
		isClaudeSessionsPath(path) ||
		isClaudeCodeSessionsPath(path) ||
		isClaudeSessionIngressPath(path) ||
		isClaudeEnvironmentsPath(path) ||
		isClaudeEnvironmentProvidersPath(path) ||
		isClaudeOAuthAPIPath(path)
}

func ensureClaudeCodeMetadataUserID(payload map[string]any, original *http.Request, route routeInfo, sessionID string) bool {
	metadata, ok := objectValue(payload["metadata"])
	changed := false
	if !ok {
		metadata = map[string]any{}
		payload["metadata"] = metadata
		changed = true
	}
	if _, ok := metadata["user_id"]; ok {
		return changed
	}
	metadata["user_id"] = claudeCodeMetadataUserID(original, route, sessionID)
	return true
}

func claudeCodeMetadataUserID(original *http.Request, route routeInfo, sessionID string) string {
	if userID := routeMetadataString(route, "claude", "metadata_user_id", "user_id"); userID != "" {
		return userID
	}
	deviceID := claudeCodeDeviceID(route)
	accountUUID := claudeCodeAccountUUID(route)
	payload := map[string]string{
		"device_id":    deviceID,
		"account_uuid": accountUUID,
		"session_id":   sessionID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func claudeCodeDeviceID(route routeInfo) string {
	if deviceID := routeMetadataString(route, "claude", "device_id", "claude_code_device_id"); deviceID != "" {
		return deviceID
	}
	if fingerprint := routeMetadataString(route, "claude", "fingerprint", "client_fingerprint"); fingerprint != "" {
		return sha256Hex("claude-code-device:" + fingerprint)
	}
	return sha256Hex("claude-code-device:" + claudeCodeFingerprintSeed(route))
}

func claudeCodeAccountUUID(route routeInfo) string {
	return firstNonEmpty(
		routeMetadataString(route, "claude", "account_uuid", "anthropic_account_uuid", "account_id"),
		route.TokenSubject,
	)
}

func claudeCodeFingerprintSeed(route routeInfo) string {
	if seed := routeMetadataString(route, "claude", "fingerprint_seed", "identity_seed", "device_seed"); seed != "" {
		return seed
	}
	parts := []string{
		strings.TrimSpace(route.TokenProvider),
		strings.TrimSpace(route.AuthMode),
		strings.TrimSpace(route.TokenSubject),
		strings.TrimSpace(route.AccountID),
		strings.TrimSpace(route.OwnerUserID),
		strings.TrimSpace(route.ChannelID),
		strings.TrimSpace(route.BaseURL),
	}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) == 0 {
		return "elucid-relay-claude-code"
	}
	return strings.Join(filtered, "|")
}

func prependClaudeCodeBillingSystem(payload map[string]any, billingHeader string) bool {
	if billingHeader == "" {
		return false
	}
	if claudeSystemContainsBillingHeader(payload["system"]) {
		return false
	}
	billingBlock := map[string]any{
		"type": "text",
		"text": billingHeader,
	}
	switch system := payload["system"].(type) {
	case nil:
		payload["system"] = []any{billingBlock}
	case string:
		payload["system"] = []any{
			billingBlock,
			map[string]any{
				"type": "text",
				"text": system,
			},
		}
	case []any:
		payload["system"] = append([]any{billingBlock}, system...)
	default:
		return false
	}
	return true
}

func claudeSystemContainsBillingHeader(system any) bool {
	switch typed := system.(type) {
	case string:
		return strings.Contains(typed, "x-anthropic-billing-header:")
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.Contains(text, "x-anthropic-billing-header:") {
				return true
			}
			if block, ok := objectValue(item); ok {
				if text, ok := block["text"].(string); ok && strings.Contains(text, "x-anthropic-billing-header:") {
					return true
				}
			}
		}
	}
	return false
}

func claudeCodeBillingHeader(route routeInfo) string {
	header := "x-anthropic-billing-header: cc_version=" + claudeCodeBillingVersion(route) + "; cc_entrypoint=" + claudeCodeEntrypoint(route) + "; cch=" + claudeCodeBillingHash(route) + ";"
	if workload := routeMetadataString(route, "claude", "workload"); workload != "" {
		header += " cc_workload=" + workload + ";"
	}
	return header
}

func claudeCodeBillingVersion(route routeInfo) string {
	if billingVersion := routeMetadataString(route, "claude", "billing_version", "cc_version"); billingVersion != "" {
		return billingVersion
	}
	version := firstNonEmpty(claudeCodeClientVersionFromUserAgent(claudeCodeUserAgent(route)), claudeCodeClientVersion(route))
	build := routeMetadataString(route, "claude", "client_build", "build", "version_build")
	if build == "" {
		build = "e50"
	}
	if strings.HasSuffix(version, "."+build) {
		return version
	}
	return version + "." + build
}

func claudeCodeBillingHash(route routeInfo) string {
	return firstNonEmpty(routeMetadataString(route, "claude", "billing_hash", "cch"), "00000")
}

func syncClaudeCodeBillingHeaderVersion(payload map[string]any, route routeInfo) bool {
	version := claudeCodeBillingVersion(route)
	if version == "" {
		return false
	}
	changed := false
	switch system := payload["system"].(type) {
	case string:
		if rewritten, ok := replaceClaudeBillingHeaderField(system, "cc_version", version); ok {
			payload["system"] = rewritten
			changed = true
		}
	case []any:
		for index, item := range system {
			if text, ok := item.(string); ok {
				if rewritten, ok := replaceClaudeBillingHeaderField(text, "cc_version", version); ok {
					system[index] = rewritten
					changed = true
				}
			} else if block, ok := objectValue(item); ok {
				if text, ok := block["text"].(string); ok {
					if rewritten, ok := replaceClaudeBillingHeaderField(text, "cc_version", version); ok {
						block["text"] = rewritten
						changed = true
					}
				}
			}
		}
	}
	return changed
}

func replaceClaudeBillingHeaderField(text string, field string, value string) (string, bool) {
	if value == "" || !strings.Contains(text, "x-anthropic-billing-header:") {
		return text, false
	}
	needle := field + "="
	start := strings.Index(text, needle)
	if start < 0 {
		return text, false
	}
	valueStart := start + len(needle)
	valueEnd := strings.Index(text[valueStart:], ";")
	if valueEnd < 0 {
		valueEnd = len(text)
	} else {
		valueEnd += valueStart
	}
	if text[valueStart:valueEnd] == value {
		return text, false
	}
	return text[:valueStart] + value + text[valueEnd:], true
}

func claudePayloadContainsBillingCCHPlaceholder(payload map[string]any) bool {
	return claudeSystemContainsText(payload["system"], "x-anthropic-billing-header:") &&
		claudeSystemContainsText(payload["system"], "cch=00000")
}

func claudeSystemContainsText(system any, needle string) bool {
	switch typed := system.(type) {
	case string:
		return strings.Contains(typed, needle)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.Contains(text, needle) {
				return true
			}
			if block, ok := objectValue(item); ok {
				if text, ok := block["text"].(string); ok && strings.Contains(text, needle) {
					return true
				}
			}
		}
	}
	return false
}

func claudeCodeBillingCCHSigningEnabled(route routeInfo) bool {
	return routeMetadataBool(route, "claude", "sign_billing_cch", "enable_billing_cch", "enable_cch", "cch_signing", "sign_cch")
}

const claudeCodeBillingCCHSeed uint64 = 0x6e52736ac806831e

func signClaudeCodeBillingCCH(body []byte) []byte {
	marker := []byte("x-anthropic-billing-header:")
	headerStart := bytes.Index(body, marker)
	if headerStart < 0 {
		return body
	}
	placeholder := []byte("cch=00000")
	placeholderStart := bytes.Index(body[headerStart:], placeholder)
	if placeholderStart < 0 {
		return body
	}
	placeholderStart += headerStart
	signature := claudeCodeBillingCCHSignature(body)
	rewritten := append([]byte(nil), body...)
	copy(rewritten[placeholderStart+len("cch="):placeholderStart+len(placeholder)], signature)
	return rewritten
}

func claudeCodeBillingCCHSignature(body []byte) string {
	digest := xxhash.NewWithSeed(claudeCodeBillingCCHSeed)
	_, _ = digest.Write(body)
	signature := strconv.FormatUint(digest.Sum64()&0xfffff, 16)
	if len(signature) >= 5 {
		return signature
	}
	return strings.Repeat("0", 5-len(signature)) + signature
}

func applyClaudeCodeOfficialHeaders(header http.Header, original *http.Request, route routeInfo) {
	copyOriginalHeaders(header, original.Header, claudeCodeOfficialPassHeaders...)

	switch {
	case isClaudeMessagesCountTokensPath(original.URL.Path):
		applyClaudeCodeCountTokensHeaders(header, original, route)
	case isClaudeMessagesPath(original.URL.Path):
		applyClaudeCodeMessagesHeaders(header, original, route)
	case isClaudeFilesPath(original.URL.Path):
		applyClaudeCodeFilesHeaders(header, route)
	case isClaudeMCPServersPath(original.URL.Path):
		applyClaudeCodeMCPServersHeaders(header, route)
	case isClaudeSessionsSubscribeWebSocketPath(original.URL.Path):
		applyClaudeCodeSessionsSubscribeWebSocketHeaders(header)
	case isClaudeSessionsPath(original.URL.Path):
		applyClaudeCodeSessionsHeaders(header, route)
	case isClaudeCodeSessionsPath(original.URL.Path):
		applyClaudeCodeJSONOAuthHeaders(header, route)
	case isClaudeSessionIngressPath(original.URL.Path):
		applyClaudeCodeSessionsHeaders(header, route)
	case isClaudeEnvironmentsPath(original.URL.Path):
		applyClaudeCodeEnvironmentsHeaders(header, route)
	case isClaudeEnvironmentProvidersPath(original.URL.Path):
		applyClaudeCodeEnvironmentProvidersHeaders(header, route)
	case isClaudeOAuthAPIPath(original.URL.Path):
		applyClaudeCodeOAuthAPIHeaders(header, original)
	default:
		applyClaudeCodeMessagesHeaders(header, original, route)
	}
}

func applyClaudeCodeMessagesHeaders(header http.Header, original *http.Request, route routeInfo) {
	header.Set("x-app", firstNonEmpty(routeMetadataString(route, "claude", "app"), "cli"))
	header.Set("User-Agent", claudeCodeUserAgent(route))
	if header.Get("Accept") == "" {
		header.Set("Accept", "application/json")
	}
	header.Set("Accept-Encoding", "gzip, deflate")
	if header.Get("anthropic-dangerous-direct-browser-access") == "" {
		header.Set("anthropic-dangerous-direct-browser-access", "true")
	}
	applyClaudeCodeStainlessHeaders(header, route)
	if header.Get("accept-language") == "" {
		header.Set("accept-language", "*")
	}
	if header.Get("sec-fetch-mode") == "" {
		header.Set("sec-fetch-mode", "cors")
	}

	sessionID := claudeCodeSessionID(original, route)
	if sessionID != "" {
		header.Set("X-Claude-Code-Session-Id", sessionID)
	}

	if clientApp := routeMetadataString(route, "claude", "client_app", "agent_sdk_client_app"); clientApp != "" {
		header.Set("x-client-app", clientApp)
	}
	if containerID := routeMetadataString(route, "claude", "remote_container_id", "container_id", "claude_code_container_id"); containerID != "" {
		header.Set("x-claude-remote-container-id", containerID)
	}
	if remoteSessionID := routeMetadataString(route, "claude", "remote_session_id", "claude_code_remote_session_id"); remoteSessionID != "" {
		header.Set("x-claude-remote-session-id", remoteSessionID)
	}
	if routeMetadataBool(route, "claude", "additional_protection", "additional_protection_enabled", "claude_code_additional_protection") {
		header.Set("x-anthropic-additional-protection", "true")
	}

	betas := []string{"claude-code-20250219"}
	if strings.EqualFold(route.AuthScheme, "bearer") {
		betas = append(betas, "oauth-2025-04-20")
	}
	betas = append(betas,
		"interleaved-thinking-2025-05-14",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"effort-2025-11-24",
	)
	betas = append(betas, routeMetadataList(route, "claude", "betas", "anthropic_betas", "extra_betas")...)
	header.Set("Anthropic-Beta", appendHeaderValue(header.Get("Anthropic-Beta"), betas...))
}

func applyClaudeCodeCountTokensHeaders(header http.Header, original *http.Request, route routeInfo) {
	applyClaudeCodeMessagesHeaders(header, original, route)
	header.Set("Anthropic-Beta", appendHeaderValue(header.Get("Anthropic-Beta"), "token-counting-2024-11-01"))
}

func applyClaudeCodeFilesHeaders(header http.Header, route routeInfo) {
	if header.Get("Accept") == "" {
		header.Set("Accept", "application/json, text/plain, */*")
	}
	applyClaudeCodeStainlessHeaders(header, route)
	header.Set("Anthropic-Beta", appendHeaderValue(header.Get("Anthropic-Beta"), "files-api-2025-04-14", "oauth-2025-04-20"))
}

func applyClaudeCodeMCPServersHeaders(header http.Header, route routeInfo) {
	if header.Get("Accept") == "" {
		header.Set("Accept", "application/json, text/plain, */*")
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}
	applyClaudeCodeStainlessHeaders(header, route)
	header.Set("Anthropic-Beta", appendHeaderValue(header.Get("Anthropic-Beta"), "mcp-servers-2025-12-04"))
}

func applyClaudeCodeSessionsHeaders(header http.Header, route routeInfo) {
	applyClaudeCodeJSONOAuthHeaders(header, route)
	header.Set("Anthropic-Beta", appendHeaderValue(header.Get("Anthropic-Beta"), "ccr-byoc-2025-07-29"))
	if organizationUUID := routeMetadataString(route, "claude", "organization_uuid", "org_uuid"); organizationUUID != "" && header.Get("x-organization-uuid") == "" {
		header.Set("x-organization-uuid", organizationUUID)
	}
}

func applyClaudeCodeEnvironmentsHeaders(header http.Header, route routeInfo) {
	applyClaudeCodeJSONOAuthHeaders(header, route)
	header.Set("Anthropic-Beta", appendHeaderValue(header.Get("Anthropic-Beta"), "environments-2025-11-01"))
	if runnerVersion := routeMetadataString(route, "claude", "environment_runner_version", "runner_version"); runnerVersion != "" && header.Get("x-environment-runner-version") == "" {
		header.Set("x-environment-runner-version", runnerVersion)
	}
	if trustedDeviceToken := routeMetadataString(route, "claude", "trusted_device_token"); trustedDeviceToken != "" && header.Get("X-Trusted-Device-Token") == "" {
		header.Set("X-Trusted-Device-Token", trustedDeviceToken)
	}
}

func applyClaudeCodeEnvironmentProvidersHeaders(header http.Header, route routeInfo) {
	if header.Get("Accept") == "" {
		header.Set("Accept", "application/json, text/plain, */*")
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}
	if organizationUUID := routeMetadataString(route, "claude", "organization_uuid", "org_uuid"); organizationUUID != "" && header.Get("x-organization-uuid") == "" {
		header.Set("x-organization-uuid", organizationUUID)
	}
}

func applyClaudeCodeJSONOAuthHeaders(header http.Header, route routeInfo) {
	if header.Get("Accept") == "" {
		header.Set("Accept", "application/json, text/plain, */*")
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}
	applyClaudeCodeStainlessHeaders(header, route)
}

func applyClaudeCodeOAuthAPIHeaders(header http.Header, original *http.Request) {
	if header.Get("Accept") == "" {
		header.Set("Accept", "application/json, text/plain, */*")
	}
	if header.Get("Content-Type") == "" && !isClaudeOAuthFileContentPath(original.URL.Path) {
		if !isClaudeOAuthMultipartUploadPath(original.URL.Path) && !isClaudeClaudeCLIProfilePath(original.URL.Path) {
			header.Set("Content-Type", "application/json")
		}
	}
	if strings.HasPrefix(strings.TrimRight(original.URL.Path, "/"), "/api/oauth/claude_cli/") {
		header.Set("Anthropic-Beta", appendHeaderValue(header.Get("Anthropic-Beta"), "oauth-2025-04-20"))
	}
}

func isClaudeOAuthMultipartUploadPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return path == "/api/oauth/file_upload"
}

func isClaudeClaudeCLIProfilePath(path string) bool {
	path = strings.TrimRight(path, "/")
	return path == "/api/claude_cli_profile"
}

func applyClaudeCodeSessionsSubscribeWebSocketHeaders(header http.Header) {
	header.Del("Accept")
	header.Del("Accept-Encoding")
	header.Del("Anthropic-Beta")
	header.Del("Content-Type")
	header.Del("User-Agent")
}

var claudeCodeOfficialPassHeaders = []string{
	"x-stainless-retry-count",
	"x-stainless-timeout",
	"x-stainless-lang",
	"x-stainless-package-version",
	"x-stainless-os",
	"x-stainless-arch",
	"x-stainless-runtime",
	"x-stainless-runtime-version",
	"anthropic-dangerous-direct-browser-access",
	"accept-language",
	"sec-fetch-mode",
	"x-organization-uuid",
	"x-environment-runner-version",
	"x-trusted-device-token",
}

func claudeCodeSessionID(original *http.Request, route routeInfo) string {
	return firstNonEmpty(
		sessionValueFromHeader(original.Header, "X-Claude-Code-Session-Id"),
		normalizeRouteAffinityKey(routeMetadataString(route, "claude", "session_id", "claude_code_session_id")),
		sessionValueFromHeader(original.Header, "X-Elucid-Relay-Session", "X-Relay-Session", "X-Subrouter-Session", "X-Session-ID"),
		sessionValueFromQuery(original, "session_id", "conversation_id", "thread_id"),
		normalizeRouteAffinityKey(requestIDFromContext(original.Context())),
	)
}

func applyClaudeCodeStainlessHeaders(header http.Header, route routeInfo) {
	setHeaderIfEmpty(header, "x-stainless-retry-count", "0")
	setHeaderIfEmpty(header, "x-stainless-timeout", firstNonEmpty(routeMetadataString(route, "claude", "stainless_timeout", "timeout"), "600"))
	setHeaderIfEmpty(header, "x-stainless-lang", "js")
	setHeaderIfEmpty(header, "x-stainless-package-version", firstNonEmpty(routeMetadataString(route, "claude", "stainless_package_version", "sdk_version"), "0.81.0"))
	setHeaderIfEmpty(header, "x-stainless-os", claudeCodeStainlessOS(route))
	setHeaderIfEmpty(header, "x-stainless-arch", firstNonEmpty(routeMetadataString(route, "claude", "stainless_arch", "arch"), stainlessArch()))
	setHeaderIfEmpty(header, "x-stainless-runtime", "node")
	setHeaderIfEmpty(header, "x-stainless-runtime-version", firstNonEmpty(routeMetadataString(route, "claude", "stainless_runtime_version", "runtime_version", "node_version"), "v22.22.1"))
}

func setHeaderIfEmpty(header http.Header, key string, value string) {
	if value != "" && header.Get(key) == "" {
		header.Set(key, value)
	}
}

func stainlessOS() string {
	switch runtime.GOOS {
	case "linux":
		return "Linux"
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}

func claudeCodeStainlessOS(route routeInfo) string {
	if osName := routeMetadataString(route, "claude", "stainless_os", "os"); osName != "" {
		return osName
	}
	if platform := routeMetadataString(route, "claude", "platform"); platform != "" {
		switch strings.ToLower(strings.TrimSpace(platform)) {
		case "darwin", "mac", "macos", "osx":
			return "MacOS"
		case "windows", "win32", "win":
			return "Windows"
		case "linux":
			return "Linux"
		default:
			return platform
		}
	}
	return stainlessOS()
}

func stainlessArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func claudeCodeUserAgent(route routeInfo) string {
	if userAgent := routeMetadataString(route, "claude", "user_agent"); userAgent != "" {
		return userAgent
	}
	version := claudeCodeClientVersion(route)
	userType := firstNonEmpty(routeMetadataString(route, "claude", "user_type"), "external")
	entrypoint := claudeCodeEntrypoint(route)
	parts := []string{userType, entrypoint}
	if agentSDKVersion := routeMetadataString(route, "claude", "agent_sdk_version", "claude_agent_sdk_version"); agentSDKVersion != "" {
		parts = append(parts, "agent-sdk/"+agentSDKVersion)
	}
	if clientApp := routeMetadataString(route, "claude", "client_app", "agent_sdk_client_app"); clientApp != "" {
		parts = append(parts, "client-app/"+clientApp)
	}
	if workload := routeMetadataString(route, "claude", "workload"); workload != "" {
		parts = append(parts, "workload/"+workload)
	}
	return "claude-cli/" + version + " (" + strings.Join(parts, ", ") + ")"
}

func claudeCodeClientVersion(route routeInfo) string {
	return firstNonEmpty(routeMetadataString(route, "claude", "client_version", "version"), "2.1.104")
}

func claudeCodeClientVersionFromUserAgent(userAgent string) string {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		return ""
	}
	lower := strings.ToLower(userAgent)
	for _, prefix := range []string{"claude-cli/"} {
		index := strings.Index(lower, prefix)
		if index < 0 {
			continue
		}
		value := userAgent[index+len(prefix):]
		if end := strings.IndexFunc(value, func(r rune) bool {
			return r == ' ' || r == '(' || r == ';' || r == ','
		}); end >= 0 {
			value = value[:end]
		}
		return strings.TrimSpace(value)
	}
	return ""
}

func claudeCodeEntrypoint(route routeInfo) string {
	return firstNonEmpty(routeMetadataString(route, "claude", "entrypoint"), "cli")
}

func isAnthropicFirstPartyBase(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "api.anthropic.com" || strings.HasSuffix(host, ".anthropic.com")
}

func appendHeaderValue(current string, values ...string) string {
	ordered := []string{}
	seen := map[string]struct{}{}
	add := func(value string) {
		for _, item := range splitHeaderTokens(value) {
			key := strings.ToLower(item)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			ordered = append(ordered, item)
		}
	}
	add(current)
	for _, value := range values {
		add(value)
	}
	return strings.Join(ordered, ",")
}

func splitHeaderTokens(value string) []string {
	items := strings.Split(value, ",")
	tokens := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		tokens = append(tokens, item)
	}
	return tokens
}

func routeMetadataString(route routeInfo, namespace string, keys ...string) string {
	for _, metadata := range routeMetadataMaps(route) {
		value := metadataValue(metadata, namespace, keys...)
		if text := metadataString(value); text != "" {
			return text
		}
	}
	return ""
}

func routeMetadataText(route routeInfo, namespace string, keys ...string) string {
	for _, metadata := range routeMetadataMaps(route) {
		value := metadataValue(metadata, namespace, keys...)
		if text := metadataText(value); text != "" {
			return text
		}
	}
	return ""
}

func routeMetadataObject(route routeInfo, namespace string, keys ...string) (map[string]any, bool) {
	for _, metadata := range routeMetadataMaps(route) {
		value := metadataValue(metadata, namespace, keys...)
		if object, ok := objectValue(value); ok {
			return object, true
		}
	}
	return nil, false
}

func routeMetadataArray(route routeInfo, namespace string, keys ...string) ([]any, bool) {
	for _, metadata := range routeMetadataMaps(route) {
		value := metadataValue(metadata, namespace, keys...)
		if array, ok := arrayValue(value); ok {
			return array, true
		}
	}
	return nil, false
}

func routeMetadataBool(route routeInfo, namespace string, keys ...string) bool {
	for _, metadata := range routeMetadataMaps(route) {
		value := metadataValue(metadata, namespace, keys...)
		if parsed, ok := metadataBool(value); ok {
			return parsed
		}
	}
	return false
}

func routeMetadataInt(route routeInfo, namespace string, keys ...string) (int, bool) {
	for _, metadata := range routeMetadataMaps(route) {
		value := metadataValue(metadata, namespace, keys...)
		if parsed, ok := metadataInt(value); ok {
			return parsed, true
		}
	}
	return 0, false
}

func routeMetadataList(route routeInfo, namespace string, keys ...string) []string {
	for _, metadata := range routeMetadataMaps(route) {
		value := metadataValue(metadata, namespace, keys...)
		if list := metadataList(value); len(list) > 0 {
			return list
		}
	}
	return nil
}

func routeMetadataTokenSet(route routeInfo, namespace string, keys ...string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, metadata := range routeMetadataMaps(route) {
		value := metadataValue(metadata, namespace, keys...)
		for _, token := range metadataList(value) {
			token = strings.ToLower(strings.TrimSpace(token))
			if token == "" {
				continue
			}
			set[token] = struct{}{}
		}
	}
	return set
}

func routeMetadataMaps(route routeInfo) []map[string]any {
	maps := []map[string]any{}
	if route.TokenMetadata != nil {
		maps = append(maps, route.TokenMetadata)
	}
	if metadata := metadataMapFromJSON(route.AbilityMeta); metadata != nil {
		maps = append(maps, metadata)
	}
	if metadata := metadataMapFromJSON(route.AccountMeta); metadata != nil {
		maps = append(maps, metadata)
	}
	if metadata := metadataMapFromJSON(route.ChannelMeta); metadata != nil {
		maps = append(maps, metadata)
	}
	if metadata := metadataMapFromJSON(route.RuntimeMeta); metadata != nil {
		maps = append(maps, metadata)
	}
	return maps
}

func metadataMapFromJSON(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return nil
	}
	return metadata
}

func metadataValue(metadata map[string]any, namespace string, keys ...string) any {
	if value := metadataDirectValue(metadata, keys...); value != nil {
		return value
	}
	if namespace != "" {
		if nested, ok := objectValue(metadata[namespace]); ok {
			if value := metadataDirectValue(nested, keys...); value != nil {
				return value
			}
			if value := metadataProfileValue(nested, "", keys...); value != nil {
				return value
			}
		}
	}
	if value := metadataProfileValue(metadata, namespace, keys...); value != nil {
		return value
	}
	if official, ok := objectValue(metadata["official_client"]); ok {
		if value := metadataDirectValue(official, keys...); value != nil {
			return value
		}
		if namespace != "" {
			if nested, ok := objectValue(official[namespace]); ok {
				if value := metadataDirectValue(nested, keys...); value != nil {
					return value
				}
				if value := metadataProfileValue(nested, "", keys...); value != nil {
					return value
				}
			}
		}
		if value := metadataProfileValue(official, namespace, keys...); value != nil {
			return value
		}
	}
	return nil
}

func metadataProfileValue(metadata map[string]any, namespace string, keys ...string) any {
	for _, profileKey := range []string{"client_profile", "profile"} {
		profile, ok := objectValue(metadata[profileKey])
		if !ok {
			continue
		}
		if value := metadataDirectValue(profile, keys...); value != nil {
			return value
		}
		if namespace != "" {
			if nested, ok := objectValue(profile[namespace]); ok {
				if value := metadataDirectValue(nested, keys...); value != nil {
					return value
				}
			}
		}
	}
	return nil
}

func metadataDirectValue(metadata map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := metadata[key]; ok {
			return value
		}
	}
	return nil
}

func metadataString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return safeHTTPHeaderValue(typed)
	case json.Number:
		return safeHTTPHeaderValue(typed.String())
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return safeHTTPHeaderValue("")
	}
}

func metadataText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		if strings.TrimSpace(typed) == "" {
			return ""
		}
		return typed
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func metadataBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		}
	case float64:
		return typed != 0, true
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed != 0, true
		}
	}
	return false, false
}

func metadataInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed == float64(int(typed)) {
			return int(typed), true
		}
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed), true
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func metadataList(value any) []string {
	switch typed := value.(type) {
	case []string:
		return normalizeMetadataList(typed)
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := metadataString(item); text != "" {
				items = append(items, text)
			}
		}
		return normalizeMetadataList(items)
	case string:
		return normalizeMetadataList(strings.FieldsFunc(typed, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\n' || r == '\t'
		}))
	default:
		return nil
	}
}

func arrayValue(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []string:
		values := make([]any, len(typed))
		for i, item := range typed {
			values[i] = item
		}
		return values, true
	default:
		return nil, false
	}
}

func normalizeMetadataList(values []string) []string {
	items := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = safeHTTPHeaderValue(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, value)
	}
	return items
}

func sessionValueFromHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if value := normalizeRouteAffinityKey(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func sessionValueFromQuery(original *http.Request, names ...string) string {
	for _, name := range names {
		if value := normalizeRouteAffinityKey(original.URL.Query().Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func copyOriginalHeaders(dst http.Header, src http.Header, names ...string) {
	for _, name := range names {
		if value := safeHTTPHeaderValue(src.Get(name)); value != "" {
			dst.Set(name, value)
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = safeHTTPHeaderValue(value); value != "" {
			return value
		}
	}
	return ""
}

func safeHTTPHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, ch := range value {
		if ch < 32 || ch == 127 {
			return ""
		}
	}
	return value
}

func (adapter anthropicAdapter) ParseUsage(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	return parseProviderUsageResult(endpoint, requestBody, responseBody)
}

func (adapter anthropicAdapter) ParseStreamEvent(endpoint string, event []byte, acc *streamMeteringAccumulator) {
	parseUsageFromStreamEvent(event, acc)
}

func (adapter geminiOpenAICompatibleAdapter) PrepareRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	if isGeminiOfficialRoute(route) {
		return prepareProviderRequest(original, route, body, func(req *http.Request) {
			applyGeminiOfficialHeaders(req.Header, original, route, body)
		})
	}
	return prepareProviderRequest(original, route, body, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+route.APIKey)
	})
}

func (adapter geminiOpenAICompatibleAdapter) PrepareWebSocket(original *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error) {
	if isGeminiOfficialRoute(route) {
		return prepareProviderWebSocket(original, route, func(header http.Header) {
			applyGeminiOfficialHeaders(header, original, route, nil)
		})
	}
	return prepareProviderWebSocket(original, route, func(header http.Header) {
		header.Set("Authorization", "Bearer "+route.APIKey)
	})
}

func (adapter geminiOpenAICompatibleAdapter) ParseUsage(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	return parseProviderUsageResult(endpoint, requestBody, responseBody)
}

func (adapter geminiOpenAICompatibleAdapter) ParseStreamEvent(endpoint string, event []byte, acc *streamMeteringAccumulator) {
	parseUsageFromStreamEvent(event, acc)
}

func (adapter geminiNativeAdapter) PrepareRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	return prepareProviderRequest(original, route, body, func(req *http.Request) {
		if isGeminiOAuthRoute(route) {
			applyGeminiOfficialHeaders(req.Header, original, route, body)
			return
		}
		applyGeminiAPIKeyHeaders(req.Header, original, route, body)
	})
}

func (adapter geminiNativeAdapter) PrepareWebSocket(original *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error) {
	return prepareProviderWebSocket(original, route, func(header http.Header) {
		if isGeminiOAuthRoute(route) {
			applyGeminiOfficialHeaders(header, original, route, nil)
			return
		}
		applyGeminiAPIKeyHeaders(header, original, route, nil)
	})
}

func (adapter geminiNativeAdapter) ParseUsage(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	return parseProviderUsageResult(endpoint, requestBody, responseBody)
}

func (adapter geminiNativeAdapter) ParseStreamEvent(endpoint string, event []byte, acc *streamMeteringAccumulator) {
	parseUsageFromStreamEvent(event, acc)
}

func (adapter geminiCLIAdapter) PrepareRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	if isGeminiCLIChatCompletionRequest(original) {
		return prepareGeminiCLIChatRequest(original, route, body)
	}
	return prepareProviderRequest(original, route, body, func(req *http.Request) {
		applyGeminiOfficialHeaders(req.Header, original, route, body)
		applyGeminiCodeAssistHeaders(req.Header, original, route, body)
	})
}

func (adapter geminiCLIAdapter) PrepareWebSocket(original *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error) {
	return prepareProviderWebSocket(original, route, func(header http.Header) {
		applyGeminiOfficialHeaders(header, original, route, nil)
		applyGeminiCodeAssistHeaders(header, original, route, nil)
	})
}

func (adapter geminiCLIAdapter) ParseUsage(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	return parseGeminiCLIUsageResult(endpoint, requestBody, responseBody)
}

func (adapter geminiCLIAdapter) ParseStreamEvent(endpoint string, event []byte, acc *streamMeteringAccumulator) {
	parseGeminiCLIStreamUsage(event, acc)
}

func isGeminiOfficialRoute(route routeInfo) bool {
	return isGeminiCLIRoute(route) || isGeminiOAuthRoute(route)
}

func isGeminiCLIRoute(route routeInfo) bool {
	return strings.EqualFold(route.ProviderType, "gemini_cli") ||
		strings.EqualFold(route.ProviderType, "google_gemini_cli") ||
		strings.EqualFold(route.ProviderType, "google_gemini") ||
		strings.EqualFold(route.TokenProvider, "gemini_cli") ||
		strings.EqualFold(route.TokenProvider, "google_gemini_cli")
}

func isGeminiOAuthRoute(route routeInfo) bool {
	return strings.EqualFold(route.AuthScheme, "bearer") ||
		strings.EqualFold(route.AuthMode, "google_pkce") ||
		strings.EqualFold(route.TokenProvider, "google_gemini") ||
		strings.EqualFold(route.TokenProvider, "gemini_cli") ||
		strings.EqualFold(route.TokenProvider, "google_gemini_cli")
}

func applyGeminiAPIKeyHeaders(header http.Header, original *http.Request, route routeInfo, body []byte) {
	header.Set("X-Goog-Api-Key", route.APIKey)
	header.Del("Authorization")
	applyGeminiClientHeaders(header, original, route, body)
}

func applyGeminiOfficialHeaders(header http.Header, original *http.Request, route routeInfo, body []byte) {
	header.Set("Authorization", "Bearer "+route.APIKey)
	header.Del("X-Goog-Api-Key")
	applyGeminiClientHeaders(header, original, route, body)
}

func applyGeminiCodeAssistHeaders(header http.Header, original *http.Request, route routeInfo, body []byte) {
	header.Set("User-Agent", geminiUserAgent(route, geminiModelFromRequest(route, body, original.Header.Get("Content-Type"))))
	header.Del("X-Goog-Api-Client")
	header.Del("X-Goog-User-Project")
}

func applyGeminiClientHeaders(header http.Header, original *http.Request, route routeInfo, body []byte) {
	copyOriginalHeaders(header, original.Header, geminiOfficialPassHeaders...)

	if header.Get("Content-Type") == "" && original.Method != http.MethodGet {
		header.Set("Content-Type", "application/json")
	}
	if header.Get("Accept") == "" {
		header.Set("Accept", "application/json")
	}
	header.Set("User-Agent", geminiUserAgent(route, geminiModelFromRequest(route, body, original.Header.Get("Content-Type"))))
	header.Set("X-Goog-Api-Client", geminiAPIClientHeader(route))
	if project := firstNonEmpty(routeMetadataString(route, "gemini", "quota_project_id", "project_id", "google_cloud_project", "user_project")); project != "" {
		header.Set("X-Goog-User-Project", project)
	}
	header.Del("Accept-Encoding")
}

var geminiOfficialPassHeaders = []string{
	"X-Goog-Api-Client",
	"X-Goog-User-Project",
}

func geminiUserAgent(route routeInfo, model string) string {
	if userAgent := routeMetadataString(route, "gemini", "user_agent", "gemini_user_agent"); userAgent != "" {
		return userAgent
	}
	prefix := firstNonEmpty(routeMetadataString(route, "gemini", "user_agent_prefix", "client_name"), "GeminiCLI")
	version := geminiClientVersion(route)
	if model == "" {
		model = firstNonEmpty(route.UpstreamModel, routeMetadataString(route, "gemini", "model", "default_model"), "gemini-2.5-pro")
	}
	surface := firstNonEmpty(routeMetadataString(route, "gemini", "surface"), "terminal")
	platform := firstNonEmpty(routeMetadataString(route, "gemini", "platform", "os"), geminiPlatform())
	arch := firstNonEmpty(routeMetadataString(route, "gemini", "arch"), geminiArch())
	return prefix + "/" + version + "/" + model + " (" + platform + "; " + arch + "; " + surface + ")"
}

func geminiClientVersion(route routeInfo) string {
	return firstNonEmpty(routeMetadataString(route, "gemini", "client_version", "version", "gemini_cli_version"), "0.42.0-nightly.20260428.g59b2dea0e")
}

func geminiAPIClientHeader(route routeInfo) string {
	return firstNonEmpty(routeMetadataString(route, "gemini", "api_client", "x_goog_api_client", "genai_sdk_client"), "google-genai-sdk/1.41.0 gl-node/v22.19.0")
}

func geminiModelFromRequest(route routeInfo, body []byte, contentType string) string {
	if model, err := modelFromBody(body, contentType); err == nil && strings.TrimSpace(model) != "" {
		if strings.TrimSpace(route.UpstreamModel) != "" {
			return route.UpstreamModel
		}
		return model
	}
	return firstNonEmpty(route.UpstreamModel, routeMetadataString(route, "gemini", "model", "default_model"))
}

func geminiPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	default:
		return runtime.GOOS
	}
}

func geminiArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	default:
		return runtime.GOARCH
	}
}

func prepareProviderRequest(original *http.Request, route routeInfo, body []byte, applyAuth func(*http.Request)) (*http.Request, context.CancelFunc, error) {
	baseURL, err := url.Parse(strings.TrimRight(route.BaseURL, "/"))
	if err != nil {
		return nil, func() {}, err
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + upstreamPathForRoute(baseURL.Path, original.URL.Path, route)
	baseURL.RawQuery = upstreamQueryForRoute(original, route)

	timeout := time.Duration(route.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(original.Context(), timeout)
	outboundBody := rewriteRequestModel(body, original.Header.Get("Content-Type"), route.UpstreamModel)
	req, err := http.NewRequestWithContext(ctx, original.Method, baseURL.String(), bytes.NewReader(outboundBody))
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	copyUpstreamRequestHeaders(req.Header, original.Header)
	req.Header.Set("Accept-Encoding", "identity")
	applyAuth(req)
	if value := original.Header.Get("Content-Type"); value != "" {
		req.Header.Set("Content-Type", value)
	}
	if value := original.Header.Get("Accept"); value != "" {
		req.Header.Set("Accept", value)
	}
	if shouldSetRelayRequestID(route) {
		req.Header.Set("X-Request-Id", requestIDFromContext(original.Context()))
	}
	if err := applyRouteHeaderRules(req, original, route); err != nil {
		cancel()
		return nil, func() {}, err
	}
	return req, cancel, nil
}

func prepareProviderWebSocket(original *http.Request, route routeInfo, applyAuth func(http.Header)) (string, http.Header, *websocket.Dialer, error) {
	upstreamURL, err := url.Parse(strings.TrimRight(route.BaseURL, "/"))
	if err != nil {
		return "", nil, nil, err
	}
	upstreamURL.Scheme = websocketScheme(upstreamURL.Scheme)
	upstreamURL.Path = strings.TrimRight(upstreamURL.Path, "/") + upstreamPathForRoute(upstreamURL.Path, original.URL.Path, route)
	upstreamURL.RawPath = ""
	upstreamURL.RawQuery = upstreamQueryForRoute(original, route)

	headers := http.Header{}
	copyUpstreamRequestHeaders(headers, original.Header)
	applyAuth(headers)
	if shouldSetRelayRequestID(route) {
		headers.Set("X-Request-Id", requestIDFromContext(original.Context()))
	}
	req := &http.Request{Header: headers}
	if err := applyRouteHeaderRules(req, original, route); err != nil {
		return "", nil, nil, err
	}
	stripWebSocketDialHeaders(req.Header)

	timeout := time.Duration(route.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	dialer := &websocket.Dialer{
		HandshakeTimeout:  timeout,
		EnableCompression: true,
	}
	configureTLSFingerprintWebSocketDialer(route, dialer)
	if strings.TrimSpace(route.ProxyURL) != "" {
		proxyURL, err := parseRouteProxyURL(route)
		if err != nil {
			return "", nil, nil, err
		}
		if proxyURL != nil {
			dialer.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return upstreamURL.String(), req.Header, dialer, nil
}

func upstreamPathForRoute(basePath string, originalPath string, route routeInfo) string {
	if isCodexOfficialRoute(route) {
		return codexOfficialUpstreamPath(basePath, originalPath)
	}
	if isGitHubCopilotRoute(route) {
		return githubCopilotUpstreamPath(basePath, originalPath)
	}
	if isGeminiCLIRoute(route) {
		return geminiCLIUpstreamPath(basePath, originalPath)
	}
	return originalPath
}

func upstreamQueryForRoute(original *http.Request, route routeInfo) string {
	rawQuery := original.URL.RawQuery
	if upstreamModel := strings.TrimSpace(route.UpstreamModel); upstreamModel != "" && strings.TrimSpace(original.URL.Query().Get("model")) != "" {
		query := original.URL.Query()
		query.Set("model", upstreamModel)
		rawQuery = query.Encode()
	}
	if isClaudeCodeRoute(route) && (isClaudeMessagesPath(original.URL.Path) || isClaudeMessagesCountTokensPath(original.URL.Path)) {
		query, _ := url.ParseQuery(rawQuery)
		query.Set("beta", "true")
		return query.Encode()
	}
	if !isCodexOfficialRoute(route) {
		if isGeminiCLIRoute(route) && strings.Contains(original.URL.Path, ":streamGenerateContent") && original.URL.Query().Get("alt") == "" {
			query, _ := url.ParseQuery(rawQuery)
			query.Set("alt", "sse")
			return query.Encode()
		}
		return rawQuery
	}
	if original.Method == http.MethodGet && isCodexResponsesPath(original.URL.Path) {
		return ""
	}
	if original.Method != http.MethodGet || !isCodexModelsPath(original.URL.Path) {
		return rawQuery
	}
	if original.URL.Query().Get("client_version") != "" {
		return rawQuery
	}
	clientVersion := codexClientVersion(route)
	if clientVersion == "" {
		return rawQuery
	}
	if rawQuery == "" {
		return "client_version=" + url.QueryEscape(clientVersion)
	}
	return rawQuery + "&client_version=" + url.QueryEscape(clientVersion)
}

func shouldSetRelayRequestID(route routeInfo) bool {
	return !isCodexOfficialRoute(route) && !isClaudeCodeRoute(route) && !isGeminiOfficialRoute(route)
}

func codexOfficialUpstreamPath(basePath string, originalPath string) string {
	if !codexOfficialBasePathConsumesV1(basePath) {
		return originalPath
	}
	if originalPath == "/v1" {
		return "/"
	}
	if strings.HasPrefix(originalPath, "/v1/") {
		return strings.TrimPrefix(originalPath, "/v1")
	}
	return originalPath
}

func codexOfficialBasePathConsumesV1(basePath string) bool {
	clean := "/" + strings.Trim(strings.TrimSpace(basePath), "/")
	if clean == "/" {
		return false
	}
	return strings.HasSuffix(clean, "/backend-api/codex") || strings.HasSuffix(clean, "/api/codex") || strings.HasSuffix(clean, "/v1")
}

func githubCopilotUpstreamPath(basePath string, originalPath string) string {
	if originalPath == "/v1" {
		return "/"
	}
	if strings.HasPrefix(originalPath, "/v1/") {
		return strings.TrimPrefix(originalPath, "/v1")
	}
	return originalPath
}

func geminiCLIUpstreamPath(basePath string, originalPath string) string {
	clean := "/" + strings.Trim(strings.TrimSpace(basePath), "/")
	if !strings.HasSuffix(clean, "/v1internal") {
		return originalPath
	}
	if originalPath == "/v1internal" {
		return "/"
	}
	if strings.HasPrefix(originalPath, "/v1internal:") || strings.HasPrefix(originalPath, "/v1internal/") {
		return strings.TrimPrefix(originalPath, "/v1internal")
	}
	return originalPath
}

func parseProviderUsageResult(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	usage := parseUsage(responseBody)
	metrics := meteringMetricsFromResponse(endpoint, requestBody, responseBody)
	source := "estimated_fallback"
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		source = "provider_usage"
	}
	return meteringResult{
		InputTokens:  maxInt(usage.InputTokens, metrics.InputTokens),
		OutputTokens: maxInt(usage.OutputTokens, metrics.OutputTokens),
		ImageCount:   metrics.ImageCount,
		AudioSeconds: metrics.AudioSeconds,
		RequestCount: nonZeroRequestCount(metrics.RequestCount),
		UsageSource:  source,
	}
}

func parseUsageFromStreamEvent(event []byte, acc *streamMeteringAccumulator) {
	if acc == nil || len(bytes.TrimSpace(event)) == 0 {
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(event, &payload); err != nil {
		return
	}
	usage, ok := usageFromPayload(payload)
	if !ok {
		return
	}
	acc.mergeUsage(usage)
}

func usageFromPayload(payload map[string]any) (usageCounts, bool) {
	if payload == nil {
		return usageCounts{}, false
	}
	if usage, ok := usageFromAny(payload["usage"]); ok {
		return usage, true
	}
	for _, key := range []string{"response", "message", "delta"} {
		nested, ok := payload[key].(map[string]any)
		if !ok {
			continue
		}
		if usage, ok := usageFromAny(nested["usage"]); ok {
			return usage, true
		}
	}
	return usageCounts{}, false
}

func usageFromAny(value any) (usageCounts, bool) {
	usageMap, ok := value.(map[string]any)
	if !ok {
		return usageCounts{}, false
	}
	input := firstPositiveNumberField(usageMap, "input_tokens", "prompt_tokens")
	output := firstPositiveNumberField(usageMap, "output_tokens", "completion_tokens")
	if input == 0 && output == 0 {
		return usageCounts{}, false
	}
	return usageCounts{InputTokens: input, OutputTokens: output}, true
}

func firstPositiveNumberField(payload map[string]any, names ...string) int {
	for _, name := range names {
		switch value := payload[name].(type) {
		case float64:
			if value > 0 {
				return int(value)
			}
		case int:
			if value > 0 {
				return value
			}
		case json.Number:
			parsed, err := value.Int64()
			if err == nil && parsed > 0 {
				return int(parsed)
			}
		}
	}
	return 0
}
