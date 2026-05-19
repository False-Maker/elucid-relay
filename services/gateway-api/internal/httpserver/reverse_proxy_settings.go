package httpserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const reverseProxySettingKey = "reverse_proxy.client_like"

type reverseProxySettings struct {
	DigestAffinityEnabled  bool                        `json:"digest_affinity_enabled"`
	ReplayGuardEnabled     bool                        `json:"replay_guard_enabled"`
	ReplayGuardMaxItems    int                         `json:"replay_guard_max_items"`
	SignatureRepairEnabled bool                        `json:"signature_repair_enabled"`
	QuotaHeaderSyncEnabled bool                        `json:"quota_header_sync_enabled"`
	StreamKeepAliveSeconds int                         `json:"stream_keep_alive_seconds"`
	AffinityRules          []affinityRuleSettings      `json:"affinity_rules"`
	AnthropicBetaPolicy    anthropicBetaPolicySettings `json:"anthropic_beta_policy"`
}

type affinityRuleSettings struct {
	Name               string         `json:"name"`
	PathRegex          []string       `json:"path_regex"`
	ModelRegex         []string       `json:"model_regex"`
	HeaderKeys         []string       `json:"header_keys"`
	QueryKeys          []string       `json:"query_keys"`
	JSONPaths          []string       `json:"json_paths"`
	JSONKeys           []string       `json:"json_keys"`
	TTLSeconds         int            `json:"ttl_seconds"`
	IncludeModelName   bool           `json:"include_model_name"`
	IncludeEndpoint    bool           `json:"include_endpoint"`
	SkipRetryOnFailure bool           `json:"skip_retry_on_failure"`
	HeaderPassthrough  []string       `json:"header_passthrough"`
	ParamOverrides     map[string]any `json:"param_overrides"`
}

type anthropicBetaPolicySettings struct {
	Enabled      bool     `json:"enabled"`
	Mode         string   `json:"mode"`
	Scope        []string `json:"scope"`
	ModelScope   []string `json:"model_scope"`
	BlockedBetas []string `json:"blocked_betas"`
	DropBetas    []string `json:"drop_betas"`
	AllowedBetas []string `json:"allowed_betas"`
}

type reverseProxySettingsPayload struct {
	DigestAffinityEnabled  *bool                       `json:"digest_affinity_enabled"`
	ReplayGuardEnabled     *bool                       `json:"replay_guard_enabled"`
	ReplayGuardMaxItems    int                         `json:"replay_guard_max_items"`
	SignatureRepairEnabled *bool                       `json:"signature_repair_enabled"`
	QuotaHeaderSyncEnabled *bool                       `json:"quota_header_sync_enabled"`
	StreamKeepAliveSeconds *int                        `json:"stream_keep_alive_seconds"`
	AffinityRules          []affinityRuleSettings      `json:"affinity_rules"`
	AnthropicBetaPolicy    anthropicBetaPolicySettings `json:"anthropic_beta_policy"`
}

func defaultReverseProxySettings() reverseProxySettings {
	return reverseProxySettings{
		DigestAffinityEnabled:  true,
		ReplayGuardEnabled:     true,
		ReplayGuardMaxItems:    defaultReplayGuardMaxItems,
		SignatureRepairEnabled: true,
		QuotaHeaderSyncEnabled: true,
		AnthropicBetaPolicy: anthropicBetaPolicySettings{
			Enabled: false,
			Mode:    "pass",
		},
	}
}

func (settings reverseProxySettings) normalized() reverseProxySettings {
	if settings.ReplayGuardMaxItems <= 0 {
		settings.ReplayGuardMaxItems = defaultReplayGuardMaxItems
	}
	if settings.ReplayGuardMaxItems < 4 {
		settings.ReplayGuardMaxItems = 4
	}
	if settings.ReplayGuardMaxItems > 200 {
		settings.ReplayGuardMaxItems = 200
	}
	if settings.StreamKeepAliveSeconds < 0 {
		settings.StreamKeepAliveSeconds = 0
	}
	if settings.StreamKeepAliveSeconds > 300 {
		settings.StreamKeepAliveSeconds = 300
	}
	settings.AnthropicBetaPolicy.Mode = strings.ToLower(strings.TrimSpace(settings.AnthropicBetaPolicy.Mode))
	if settings.AnthropicBetaPolicy.Mode == "" {
		settings.AnthropicBetaPolicy.Mode = "pass"
	}
	settings.AnthropicBetaPolicy.Scope = normalizeMetadataList(settings.AnthropicBetaPolicy.Scope)
	settings.AnthropicBetaPolicy.ModelScope = normalizeMetadataList(settings.AnthropicBetaPolicy.ModelScope)
	settings.AnthropicBetaPolicy.BlockedBetas = normalizeMetadataList(settings.AnthropicBetaPolicy.BlockedBetas)
	settings.AnthropicBetaPolicy.DropBetas = normalizeMetadataList(settings.AnthropicBetaPolicy.DropBetas)
	settings.AnthropicBetaPolicy.AllowedBetas = normalizeMetadataList(settings.AnthropicBetaPolicy.AllowedBetas)
	settings.AffinityRules = normalizeAffinityRules(settings.AffinityRules)
	return settings
}

func normalizeAffinityRules(rules []affinityRuleSettings) []affinityRuleSettings {
	if len(rules) == 0 {
		return nil
	}
	normalized := make([]affinityRuleSettings, 0, len(rules))
	for _, rule := range rules {
		rule.Name = strings.TrimSpace(rule.Name)
		rule.PathRegex = normalizeMetadataList(rule.PathRegex)
		rule.ModelRegex = normalizeMetadataList(rule.ModelRegex)
		rule.HeaderKeys = normalizeMetadataList(rule.HeaderKeys)
		rule.QueryKeys = normalizeMetadataList(rule.QueryKeys)
		rule.JSONPaths = normalizeMetadataList(rule.JSONPaths)
		rule.JSONKeys = normalizeMetadataList(rule.JSONKeys)
		rule.HeaderPassthrough = normalizeMetadataList(rule.HeaderPassthrough)
		if rule.TTLSeconds < 0 {
			rule.TTLSeconds = 0
		}
		if rule.TTLSeconds > 60*60*24*30 {
			rule.TTLSeconds = 60 * 60 * 24 * 30
		}
		if rule.ParamOverrides == nil {
			rule.ParamOverrides = map[string]any{}
		}
		if rule.Name == "" && len(rule.HeaderKeys) == 0 && len(rule.QueryKeys) == 0 && len(rule.JSONPaths) == 0 && len(rule.JSONKeys) == 0 && len(rule.HeaderPassthrough) == 0 && len(rule.ParamOverrides) == 0 {
			continue
		}
		normalized = append(normalized, rule)
	}
	return normalized
}

func (s *Server) reverseProxySettingsOrDefault(ctx context.Context) reverseProxySettings {
	settings, err := s.reverseProxySettings(ctx)
	if err != nil {
		return defaultReverseProxySettings()
	}
	return settings
}

func (s *Server) reverseProxySettings(ctx context.Context) (reverseProxySettings, error) {
	if s == nil || s.db == nil {
		return defaultReverseProxySettings(), nil
	}
	now := time.Now()
	s.settingsMu.Lock()
	if now.Before(s.reverseProxySettingsCacheUntil) {
		settings := s.reverseProxySettingsCache
		s.settingsMu.Unlock()
		return settings, nil
	}
	s.settingsMu.Unlock()

	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT setting_value_json::text FROM system_settings WHERE setting_key = $1", reverseProxySettingKey).Scan(&raw)
	if err != nil && !errorsIsSQLNoRows(err) {
		return reverseProxySettings{}, err
	}
	settings := parseReverseProxySettings(raw)
	s.settingsMu.Lock()
	s.reverseProxySettingsCache = settings
	s.reverseProxySettingsCacheUntil = now.Add(15 * time.Second)
	s.settingsMu.Unlock()
	return settings, nil
}

func parseReverseProxySettings(raw string) reverseProxySettings {
	settings := defaultReverseProxySettings()
	if strings.TrimSpace(raw) == "" {
		return settings.normalized()
	}
	var payload reverseProxySettingsPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return settings.normalized()
	}
	if payload.DigestAffinityEnabled != nil {
		settings.DigestAffinityEnabled = *payload.DigestAffinityEnabled
	}
	if payload.ReplayGuardEnabled != nil {
		settings.ReplayGuardEnabled = *payload.ReplayGuardEnabled
	}
	if payload.ReplayGuardMaxItems > 0 {
		settings.ReplayGuardMaxItems = payload.ReplayGuardMaxItems
	}
	if payload.SignatureRepairEnabled != nil {
		settings.SignatureRepairEnabled = *payload.SignatureRepairEnabled
	}
	if payload.QuotaHeaderSyncEnabled != nil {
		settings.QuotaHeaderSyncEnabled = *payload.QuotaHeaderSyncEnabled
	}
	if payload.StreamKeepAliveSeconds != nil {
		settings.StreamKeepAliveSeconds = *payload.StreamKeepAliveSeconds
	}
	settings.AffinityRules = payload.AffinityRules
	settings.AnthropicBetaPolicy = payload.AnthropicBetaPolicy
	if !payload.AnthropicBetaPolicy.Enabled && payload.AnthropicBetaPolicy.Mode == "" && len(payload.AnthropicBetaPolicy.BlockedBetas) == 0 && len(payload.AnthropicBetaPolicy.DropBetas) == 0 && len(payload.AnthropicBetaPolicy.AllowedBetas) == 0 {
		settings.AnthropicBetaPolicy = defaultReverseProxySettings().AnthropicBetaPolicy
	}
	return settings.normalized()
}

func errorsIsSQLNoRows(err error) bool {
	return err == sql.ErrNoRows
}

func (s *Server) adminReverseProxySettings(w http.ResponseWriter, r *http.Request, auth authContext) error {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.reverseProxySettings(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, settings, nil)
		return nil
	case http.MethodPut:
		var req reverseProxySettings
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		next := req.normalized()
		value, err := encodeJSON(next)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), `
			INSERT INTO system_settings (setting_key, category, setting_value_json, is_public, updated_by)
			VALUES ($1, 'runtime', $2::jsonb, false, $3)
			ON CONFLICT (setting_key) DO UPDATE SET
			  category = EXCLUDED.category,
			  setting_value_json = EXCLUDED.setting_value_json,
			  is_public = false,
			  updated_by = EXCLUDED.updated_by
		`, reverseProxySettingKey, value, auth.UserID); err != nil {
			return err
		}
		s.settingsMu.Lock()
		s.reverseProxySettingsCache = next
		s.reverseProxySettingsCacheUntil = time.Now().Add(15 * time.Second)
		s.settingsMu.Unlock()
		audit(r.Context(), s.db, auth.UserID, "admin", "reverse_proxy_settings.update", "system_setting", reverseProxySettingKey, r, nil)
		writeJSON(w, http.StatusOK, next, nil)
		return nil
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
}

func (s *Server) applyReverseProxyHeaderPolicies(ctx context.Context, original *http.Request, header http.Header, route routeInfo) error {
	settings := s.reverseProxySettingsOrDefault(ctx)
	applyAffinityRuleHeaderPassthrough(original, header, route, settings.AffinityRules)
	if err := applyRouteParamOverrideHeaderOperations(original, route, header); err != nil {
		return err
	}
	return applyGlobalAnthropicBetaPolicy(header, route, settings.AnthropicBetaPolicy)
}

type reverseProxyParamOverridePreviewRequest struct {
	ProviderType    string            `json:"provider_type"`
	UpstreamModel   string            `json:"upstream_model"`
	ContentType     string            `json:"content_type"`
	ChannelMetadata json.RawMessage   `json:"channel_metadata"`
	AbilityMetadata json.RawMessage   `json:"ability_metadata"`
	AccountMetadata json.RawMessage   `json:"account_metadata"`
	Headers         map[string]string `json:"headers"`
	UpstreamHeaders map[string]string `json:"upstream_headers"`
	Body            json.RawMessage   `json:"body"`
}

func (s *Server) adminReverseProxyParamOverridePreview(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req reverseProxyParamOverridePreviewRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	channelMeta, err := previewMetadataJSON(req.ChannelMetadata)
	if err != nil {
		return badRequest("Channel metadata must be a JSON object.")
	}
	abilityMeta, err := previewMetadataJSON(req.AbilityMetadata)
	if err != nil {
		return badRequest("Ability metadata must be a JSON object.")
	}
	accountMeta, err := previewMetadataJSON(req.AccountMetadata)
	if err != nil {
		return badRequest("Account metadata must be a JSON object.")
	}
	body := previewBodyBytes(req.Body)
	contentType := strings.TrimSpace(req.ContentType)
	if contentType == "" {
		contentType = "application/json"
	}

	route := routeInfo{
		ProviderType:   firstNonEmpty(strings.TrimSpace(req.ProviderType), "openai_compatible"),
		UpstreamModel:  strings.TrimSpace(req.UpstreamModel),
		ChannelMeta:    channelMeta,
		AbilityMeta:    abilityMeta,
		AccountMeta:    accountMeta,
		ChannelID:      "preview-channel",
		AccountID:      "preview-account",
		TimeoutSeconds: 120,
	}
	original, err := http.NewRequestWithContext(withParamOverrideAuditAll(r.Context()), http.MethodPost, "https://preview.local/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	original.Header.Set("Content-Type", contentType)
	for key, value := range req.Headers {
		original.Header.Set(key, value)
	}
	upstream, err := http.NewRequestWithContext(original.Context(), http.MethodPost, "https://upstream.preview/v1/chat/completions", nil)
	if err != nil {
		return err
	}
	upstream.Header.Set("Content-Type", contentType)
	for key, value := range req.UpstreamHeaders {
		upstream.Header.Set(key, value)
	}
	if err := applyRouteHeaderRules(upstream, original, route); err != nil {
		return err
	}
	rewrittenBody, err := applyRouteParamOverrideOperations(original, route, body)
	if err != nil {
		return err
	}
	if err := applyRouteParamOverrideHeaderOperations(original, route, upstream.Header); err != nil {
		return err
	}
	response := map[string]any{
		"body": previewBodyJSON(rewrittenBody),
		"body_text": func() string {
			if json.Valid(rewrittenBody) {
				return ""
			}
			return string(rewrittenBody)
		}(),
		"headers": headerValuesMap(upstream.Header),
		"audit":   paramOverrideAuditFromRequest(original),
	}
	writeJSON(w, http.StatusOK, response, nil)
	return nil
}

func previewMetadataJSON(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return "{}", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return "{}", nil
		}
		if !json.Valid([]byte(text)) {
			return "", errInvalidJSONPreviewMetadata()
		}
		raw = json.RawMessage(text)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func errInvalidJSONPreviewMetadata() error {
	return errors.New("preview metadata is not valid JSON")
}

func previewBodyBytes(raw json.RawMessage) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []byte(`{}`)
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil && json.Valid([]byte(strings.TrimSpace(text))) {
		return []byte(strings.TrimSpace(text))
	}
	return append([]byte{}, trimmed...)
}

func previewBodyJSON(body []byte) any {
	if !json.Valid(body) {
		return nil
	}
	return json.RawMessage(body)
}

func headerValuesMap(header http.Header) map[string][]string {
	result := map[string][]string{}
	for key, values := range header {
		result[key] = append([]string{}, values...)
	}
	return result
}

func (s *Server) shouldRetryClaudeSignatureRepair(ctx context.Context, route routeInfo, status int, body []byte) bool {
	if status != http.StatusBadRequest || !isClaudeCodeRoute(route) || !isAnthropicSignatureErrorText(string(body)) {
		return false
	}
	if routeMetadataBool(route, "claude", "disable_signature_repair", "signature_repair_disabled", "disable_signature_repair_retry") {
		return false
	}
	if enabled, ok := routeMetadataBoolValue(route, "claude", "signature_repair_retry", "repair_thinking_signature", "enable_signature_repair"); ok {
		return enabled
	}
	return s.reverseProxySettingsOrDefault(ctx).SignatureRepairEnabled
}

func applyGlobalAnthropicBetaPolicy(header http.Header, route routeInfo, policy anthropicBetaPolicySettings) error {
	if !policy.Enabled || header == nil || header.Get("Anthropic-Beta") == "" || !anthropicBetaPolicyApplies(route, policy) {
		return nil
	}
	tokens := splitHeaderTokens(header.Get("Anthropic-Beta"))
	if len(tokens) == 0 {
		header.Del("Anthropic-Beta")
		return nil
	}
	blocked := stringSet(policy.BlockedBetas)
	dropped := stringSet(policy.DropBetas)
	allowed := stringSet(policy.AllowedBetas)
	mode := strings.ToLower(strings.TrimSpace(policy.Mode))
	filterUnlisted := len(allowed) > 0 && (mode == "" || mode == "filter" || mode == "filter_unlisted" || mode == "allowlist")
	blockUnlisted := len(allowed) > 0 && (mode == "block" || mode == "block_unlisted" || mode == "deny_unlisted")

	filtered := make([]string, 0, len(tokens))
	for _, token := range tokens {
		key := strings.ToLower(strings.TrimSpace(token))
		if _, ok := blocked[key]; ok {
			return badRequest("Anthropic beta token is blocked by global policy.")
		}
		if _, ok := dropped[key]; ok {
			continue
		}
		if _, ok := allowed[key]; len(allowed) > 0 && !ok {
			if blockUnlisted {
				return badRequest("Anthropic beta token is not allowed by global policy.")
			}
			if filterUnlisted {
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

func anthropicBetaPolicyApplies(route routeInfo, policy anthropicBetaPolicySettings) bool {
	if len(policy.Scope) > 0 {
		routeScopes := map[string]struct{}{}
		if strings.EqualFold(route.AuthScheme, "bearer") {
			routeScopes["oauth"] = struct{}{}
		} else {
			routeScopes["apikey"] = struct{}{}
			routeScopes["api_key"] = struct{}{}
		}
		if isClaudeCodeRoute(route) {
			routeScopes["claude_code"] = struct{}{}
		}
		if strings.Contains(strings.ToLower(route.ProviderType), "anthropic") || isClaudeCodeRoute(route) {
			routeScopes["anthropic"] = struct{}{}
		}
		matched := false
		for _, scope := range policy.Scope {
			if _, ok := routeScopes[strings.ToLower(strings.TrimSpace(scope))]; ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(policy.ModelScope) == 0 {
		return true
	}
	model := strings.ToLower(strings.TrimSpace(route.UpstreamModel))
	for _, candidate := range policy.ModelScope {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "*" || candidate == model || (strings.HasSuffix(candidate, "*") && strings.HasPrefix(model, strings.TrimSuffix(candidate, "*"))) {
			return true
		}
	}
	return false
}

func stringSet(values []string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	return set
}
