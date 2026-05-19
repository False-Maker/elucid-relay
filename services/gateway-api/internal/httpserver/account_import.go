package httpserver

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

type accountImportTemplate struct {
	ID               string         `json:"id"`
	Label            string         `json:"label"`
	ProviderType     string         `json:"provider_type"`
	AuthMode         string         `json:"auth_mode"`
	TokenType        string         `json:"token_type"`
	TokenProvider    string         `json:"token_provider"`
	AuthScheme       string         `json:"auth_scheme"`
	RuntimeSupported bool           `json:"runtime_supported"`
	QuotaAdapter     string         `json:"quota_adapter"`
	CredentialHint   string         `json:"credential_hint"`
	Metadata         map[string]any `json:"metadata"`
	SampleItem       map[string]any `json:"sample_item"`
	Notes            []string       `json:"notes"`
}

type accountImportRequest struct {
	Template        string                 `json:"template"`
	ProviderID      string                 `json:"provider_id"`
	ChannelID       string                 `json:"channel_id"`
	ProxyID         string                 `json:"proxy_id"`
	RoutingMode     string                 `json:"routing_mode"`
	OwnerUserID     string                 `json:"owner_user_id"`
	PoolGroup       string                 `json:"pool_group"`
	RouteTags       []string               `json:"route_tags"`
	DefaultMetadata map[string]any         `json:"default_metadata"`
	Items           []accountImportPayload `json:"items"`
}

type accountKeyImportRequest struct {
	Template        string         `json:"template"`
	ProviderID      string         `json:"provider_id"`
	ChannelID       string         `json:"channel_id"`
	ProxyID         string         `json:"proxy_id"`
	RoutingMode     string         `json:"routing_mode"`
	OwnerUserID     string         `json:"owner_user_id"`
	PoolGroup       string         `json:"pool_group"`
	RouteTags       []string       `json:"route_tags"`
	DefaultMetadata map[string]any `json:"default_metadata"`
	Metadata        map[string]any `json:"metadata"`
	NamePrefix      string         `json:"name_prefix"`
	Priority        int            `json:"priority"`
	MaxConcurrency  int            `json:"max_concurrency"`
	Keys            []string       `json:"keys"`
	Text            string         `json:"text"`
}

type accountImportPayload struct {
	Template         string         `json:"template"`
	ProviderID       string         `json:"provider_id"`
	ChannelID        string         `json:"channel_id"`
	ProxyID          string         `json:"proxy_id"`
	OwnerUserID      string         `json:"owner_user_id"`
	RoutingMode      string         `json:"routing_mode"`
	Name             string         `json:"name"`
	APIKey           string         `json:"api_key"`
	TokenBundle      tokenBundle    `json:"token_bundle"`
	AuthMode         string         `json:"auth_mode"`
	Status           string         `json:"status"`
	Priority         int            `json:"priority"`
	MaxConcurrency   int            `json:"max_concurrency"`
	Metadata         map[string]any `json:"metadata"`
	PoolGroup        string         `json:"pool_group"`
	RouteTags        []string       `json:"route_tags"`
	ExternalID       string         `json:"external_id"`
	Email            string         `json:"email"`
	Subject          string         `json:"subject"`
	QuotaEndpoint    string         `json:"quota_endpoint"`
	ClientVersion    string         `json:"client_version"`
	Platform         string         `json:"platform"`
	Arch             string         `json:"arch"`
	DeviceID         string         `json:"device_id"`
	UserAgent        string         `json:"user_agent"`
	QuotaProjectID   string         `json:"quota_project_id"`
	OrganizationID   string         `json:"organization_id"`
	ProjectID        string         `json:"project_id"`
	Fingerprint      string         `json:"fingerprint"`
	IDEName          string         `json:"ide_name"`
	IDEVersion       string         `json:"ide_version"`
	ExtensionName    string         `json:"extension_name"`
	ExtensionVersion string         `json:"extension_version"`
}

type normalizedAccountImportItem struct {
	ProviderID        string
	ChannelID         string
	ProxyID           string
	OwnerUserID       string
	RoutingMode       string
	Name              string
	AuthMode          string
	Status            string
	Priority          int
	MaxConcurrency    int
	Metadata          map[string]any
	Credential        tokenBundle
	HasAuthState      bool
	CredentialPresent bool
}

type accountImportCandidate struct {
	Index             int
	Template          accountImportTemplate
	Item              normalizedAccountImportItem
	Valid             bool
	Errors            []string
	Warnings          []string
	DedupKey          string
	ExistingAccountID string
}

func accountImportTemplates() []accountImportTemplate {
	return []accountImportTemplate{
		{
			ID: "generic", Label: "通用导入", ProviderType: "", AuthMode: "api_key", TokenType: "api_key", AuthScheme: "api_key",
			RuntimeSupported: true, CredentialHint: "api_key 或 token_bundle.access_token",
			Metadata:   map[string]any{},
			SampleItem: map[string]any{"name": "account-1", "api_key": "sk-...", "metadata": map[string]any{"route_tags": []string{"premium"}}},
		},
		{
			ID: "openai_compatible", Label: "OpenAI 兼容", ProviderType: "openai_compatible", AuthMode: "api_key", TokenType: "api_key", AuthScheme: "api_key",
			RuntimeSupported: true, QuotaAdapter: "openai_compatible", CredentialHint: "OpenAI-compatible API key",
			Metadata:   map[string]any{"quota_adapter": "openai_compatible"},
			SampleItem: map[string]any{"name": "openai-compatible-1", "api_key": "sk-...", "quota_endpoint": "/usage/quota"},
		},
		{
			ID: "codex", Label: "Codex CLI", ProviderType: "codex_compatible", AuthMode: "codex_cli", TokenType: "oauth", TokenProvider: "openai_codex", AuthScheme: "bearer",
			RuntimeSupported: true, QuotaAdapter: "codex", CredentialHint: "Codex/OpenAI OAuth access token",
			Metadata:   map[string]any{"quota_adapter": "codex", "codex": map[string]any{"originator": "codex_exec"}},
			SampleItem: map[string]any{"name": "codex-1", "api_key": "tok", "subject": "workspace-or-account-id", "client_version": "0.0.0"},
		},
		{
			ID: "claude_code", Label: "Claude Code", ProviderType: "anthropic", AuthMode: "claude_cli", TokenType: "oauth", TokenProvider: "anthropic_claude", AuthScheme: "bearer",
			RuntimeSupported: true, QuotaAdapter: "anthropic", CredentialHint: "Claude OAuth access token",
			Metadata:   map[string]any{"quota_adapter": "anthropic", "claude": map[string]any{"client_version": "2.1.104"}},
			SampleItem: map[string]any{"name": "claude-code-1", "api_key": "tok", "organization_id": "org-uuid"},
		},
		{
			ID: "github_copilot", Label: "GitHub Copilot", ProviderType: "github_copilot", AuthMode: "oauth", TokenType: "oauth", TokenProvider: "github_copilot", AuthScheme: "bearer",
			RuntimeSupported: true, QuotaAdapter: "github_copilot", CredentialHint: "Copilot access token",
			Metadata:   map[string]any{"quota_adapter": "github_copilot"},
			SampleItem: map[string]any{"name": "copilot-1", "api_key": "ghu_...", "subject": "github-login"},
		},
		{
			ID: "gemini_cli", Label: "Gemini CLI", ProviderType: "gemini_cli", AuthMode: "google_pkce", TokenType: "oauth", TokenProvider: "google_gemini", AuthScheme: "bearer",
			RuntimeSupported: true, QuotaAdapter: "gemini_cli", CredentialHint: "Google OAuth access token",
			Metadata:   map[string]any{"quota_adapter": "gemini_cli", "gemini": map[string]any{"platform": "linux", "arch": "x64"}},
			SampleItem: map[string]any{"name": "gemini-cli-1", "api_key": "ya29....", "quota_project_id": "project-id"},
		},
		{
			ID: "antigravity", Label: "Antigravity", ProviderType: "antigravity", AuthMode: "google_pkce", TokenType: "oauth", TokenProvider: "google_antigravity", AuthScheme: "bearer",
			RuntimeSupported: true, QuotaAdapter: "antigravity", CredentialHint: "Google Antigravity OAuth access token",
			Metadata:   map[string]any{"quota_adapter": "antigravity", "antigravity": map[string]any{"client_version": "1.20.5"}},
			SampleItem: map[string]any{"name": "antigravity-1", "api_key": "ya29....", "project_id": "project-id"},
		},
		{
			ID: "kiro", Label: "Kiro", ProviderType: "kiro", AuthMode: "oauth", TokenType: "oauth", AuthScheme: "bearer",
			RuntimeSupported: true, QuotaAdapter: "kiro", CredentialHint: "Kiro access token",
			Metadata:   map[string]any{"quota_adapter": "kiro", "kiro": map[string]any{"client_version": "0.7.45"}},
			SampleItem: map[string]any{"name": "kiro-1", "api_key": "tok", "fingerprint": "machine-fingerprint"},
		},
		{
			ID: "windsurf_codeium", Label: "Windsurf / Codeium", ProviderType: "windsurf_codeium", AuthMode: "api_key", TokenType: "api_key", AuthScheme: "bearer",
			RuntimeSupported: true, QuotaAdapter: "windsurf_codeium", CredentialHint: "Codeium API key",
			Metadata:   map[string]any{"quota_adapter": "windsurf_codeium", "windsurf": map[string]any{"ide_name": "windsurf", "extension_name": "windsurf"}},
			SampleItem: map[string]any{"name": "windsurf-1", "api_key": "ckey", "ide_version": "1.20.9"},
		},
		{
			ID: "cursor_openai", Label: "Cursor OpenAI 兼容", ProviderType: "openai_compatible", AuthMode: "api_key", TokenType: "api_key", AuthScheme: "api_key",
			RuntimeSupported: true, QuotaAdapter: "openai_compatible", CredentialHint: "Cursor 暴露的 OpenAI-compatible key/base_url",
			Metadata:   map[string]any{"quota_adapter": "openai_compatible", "product": "cursor"},
			SampleItem: map[string]any{"name": "cursor-compatible-1", "api_key": "sk-..."},
			Notes:      []string{"该模板只表示 OpenAI-compatible 转发配置，不等同于本地 Cursor 客户端控制。"},
		},
		{
			ID: "agent_openai", Label: "其他 Agent OpenAI 兼容", ProviderType: "openai_compatible", AuthMode: "api_key", TokenType: "api_key", AuthScheme: "api_key",
			RuntimeSupported: true, QuotaAdapter: "openai_compatible", CredentialHint: "Qoder/CodeBuddy/Trae/Zed 等可暴露的 OpenAI-compatible key/base_url",
			Metadata:   map[string]any{"quota_adapter": "openai_compatible", "product": "agent_openai_compatible"},
			SampleItem: map[string]any{"name": "agent-compatible-1", "api_key": "sk-...", "metadata": map[string]any{"product": "qoder"}},
			Notes:      []string{"未实现这些桌面产品的本地多开和本地凭据读取。"},
		},
	}
}

func accountImportTemplateByID(id string) (accountImportTemplate, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "generic"
	}
	for _, template := range accountImportTemplates() {
		if template.ID == id {
			return template, true
		}
	}
	return accountImportTemplate{}, false
}

func (s *Server) adminAccountImportTemplates(w http.ResponseWriter, r *http.Request, auth authContext) error {
	writeJSON(w, http.StatusOK, accountImportTemplates(), nil)
	return nil
}

func (s *Server) adminPreviewAccountImport(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req accountImportRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	candidates, summary, err := s.normalizeAccountImportCandidates(r.Context(), req)
	if err != nil {
		return err
	}
	items := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, candidate.previewRow())
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": summary, "items": items}, nil)
	return nil
}

func (s *Server) adminImportAccountKeys(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req accountKeyImportRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	keys := accountImportKeysFromRequest(req)
	if len(keys) == 0 {
		return badRequest("keys or text is required.")
	}
	if len(keys) > 200 {
		return badRequest("keys cannot exceed 200.")
	}
	metadata := mergeMetadataMaps(req.DefaultMetadata, req.Metadata)
	importReq := accountImportRequest{
		Template:        req.Template,
		ProviderID:      req.ProviderID,
		ChannelID:       req.ChannelID,
		ProxyID:         req.ProxyID,
		RoutingMode:     req.RoutingMode,
		OwnerUserID:     req.OwnerUserID,
		PoolGroup:       req.PoolGroup,
		RouteTags:       req.RouteTags,
		DefaultMetadata: metadata,
		Items:           make([]accountImportPayload, 0, len(keys)),
	}
	namePrefix := strings.TrimSpace(req.NamePrefix)
	if namePrefix == "" {
		namePrefix = strings.TrimSpace(req.Template)
	}
	if namePrefix == "" {
		namePrefix = "account"
	}
	for index, key := range keys {
		importReq.Items = append(importReq.Items, accountImportPayload{
			Name:           fmt.Sprintf("%s-%d", namePrefix, index+1),
			APIKey:         key,
			Priority:       req.Priority,
			MaxConcurrency: req.MaxConcurrency,
			Metadata: map[string]any{
				"import": map[string]any{
					"source": "admin_key_import",
					"index":  index + 1,
				},
			},
		})
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	accountIDs, summary, err := s.importAccountsFromRequest(r.Context(), tx, importReq)
	if err != nil {
		return err
	}
	imported := len(accountIDs)
	audit(r.Context(), tx, auth.UserID, "admin", "account.import_keys", "account", "", r, map[string]any{"count": imported, "summary": summary})
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"imported": imported, "account_ids": accountIDs, "summary": summary}, nil)
	return nil
}

func accountImportKeysFromRequest(req accountKeyImportRequest) []string {
	seen := map[string]struct{}{}
	keys := make([]string, 0, len(req.Keys))
	add := func(value string) {
		key := strings.TrimSpace(value)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for _, key := range req.Keys {
		add(key)
	}
	for _, line := range strings.FieldsFunc(req.Text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t' || r == ',' || r == ';'
	}) {
		add(line)
	}
	return keys
}

func (s *Server) importAccountsFromRequest(ctx context.Context, tx *sql.Tx, req accountImportRequest) ([]string, map[string]any, error) {
	candidates, summary, err := s.normalizeAccountImportCandidates(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if invalid, _ := summary["invalid_count"].(int); invalid > 0 {
		return nil, summary, badRequest("Import contains invalid items; run preview for details.")
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		id, err := s.insertImportedAccountTx(ctx, tx, candidate.Item)
		if err != nil {
			return nil, summary, err
		}
		ids = append(ids, id)
	}
	return ids, summary, nil
}

func (s *Server) normalizeAccountImportCandidates(ctx context.Context, req accountImportRequest) ([]accountImportCandidate, map[string]any, error) {
	if len(req.Items) == 0 {
		return nil, nil, badRequest("items is required.")
	}
	if len(req.Items) > 200 {
		return nil, nil, badRequest("items cannot exceed 200.")
	}
	defaultTemplate, ok := accountImportTemplateByID(req.Template)
	if !ok {
		return nil, nil, badRequest("Unsupported import template.")
	}
	seen := map[string]int{}
	candidates := make([]accountImportCandidate, 0, len(req.Items))
	validCount := 0
	invalidCount := 0
	warningCount := 0
	for index, payload := range req.Items {
		template := defaultTemplate
		templateError := ""
		if strings.TrimSpace(payload.Template) != "" {
			var found bool
			template, found = accountImportTemplateByID(payload.Template)
			if !found {
				templateError = "Unsupported item import template."
				template = defaultTemplate
			}
		}
		candidate := s.normalizeAccountImportPayload(ctx, req, payload, template, index)
		if templateError != "" {
			candidate.Errors = append(candidate.Errors, templateError)
		}
		if previous, exists := seen[candidate.DedupKey]; candidate.DedupKey != "" && exists {
			candidate.Errors = append(candidate.Errors, fmt.Sprintf("Duplicate item in import payload; first seen at index %d.", previous))
		} else if candidate.DedupKey != "" {
			seen[candidate.DedupKey] = index
		}
		if candidate.ExistingAccountID != "" {
			candidate.Errors = append(candidate.Errors, "Account with the same provider/channel/name already exists.")
		}
		candidate.Valid = len(candidate.Errors) == 0
		if candidate.Valid {
			validCount++
		} else {
			invalidCount++
		}
		if len(candidate.Warnings) > 0 {
			warningCount++
		}
		candidates = append(candidates, candidate)
	}
	summary := map[string]any{
		"total_count":   len(candidates),
		"valid_count":   validCount,
		"invalid_count": invalidCount,
		"warning_count": warningCount,
	}
	return candidates, summary, nil
}

func (s *Server) normalizeAccountImportPayload(ctx context.Context, req accountImportRequest, payload accountImportPayload, template accountImportTemplate, index int) accountImportCandidate {
	errors := []string{}
	warnings := []string{}
	providerID := strings.TrimSpace(firstNonEmpty(payload.ProviderID, req.ProviderID))
	channelID := strings.TrimSpace(firstNonEmpty(payload.ChannelID, req.ChannelID))
	if providerID == "" && channelID != "" {
		if channelProviderID, err := s.providerIDForChannel(ctx, channelID); err == nil {
			providerID = channelProviderID
		} else {
			errors = append(errors, "channel_id was not found.")
		}
	}
	if providerID == "" {
		errors = append(errors, "provider_id is required.")
	} else if exists, err := s.idExists(ctx, "providers", providerID); err != nil {
		errors = append(errors, err.Error())
	} else if !exists {
		errors = append(errors, "provider_id was not found.")
	}
	if channelID != "" {
		channelProviderID, err := s.providerIDForChannel(ctx, channelID)
		if err != nil {
			errors = append(errors, "channel_id was not found.")
		} else if providerID != "" && channelProviderID != providerID {
			errors = append(errors, "channel_id belongs to a different provider.")
		}
	}
	routingMode, err := routingModeValue(firstNonEmpty(payload.RoutingMode, req.RoutingMode, "pool"))
	if err != nil {
		errors = append(errors, err.Error())
		routingMode = "pool"
	}
	ownerUserID := strings.TrimSpace(firstNonEmpty(payload.OwnerUserID, req.OwnerUserID))
	if err := requireBYOOwner(routingMode, ownerUserID); err != nil {
		errors = append(errors, err.Error())
	}
	status, err := defaultedStatus(payload.Status, "active", "active", "disabled", "cooldown", "exhausted")
	if err != nil {
		errors = append(errors, err.Error())
		status = "active"
	}
	priority := payload.Priority
	if priority == 0 {
		priority = 100
	}
	maxConcurrency := payload.MaxConcurrency
	if maxConcurrency == 0 {
		maxConcurrency = 10
	}
	if maxConcurrency <= 0 {
		errors = append(errors, "max_concurrency must be positive.")
		maxConcurrency = 10
	}
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		name = firstNonEmpty(strings.TrimSpace(payload.Email), strings.TrimSpace(payload.Subject), strings.TrimSpace(payload.ExternalID))
	}
	if name == "" {
		name = fmt.Sprintf("%s-%d", template.ID, index+1)
		warnings = append(warnings, "name was generated from the template.")
	}
	metadata := mergeMetadataMaps(template.Metadata, req.DefaultMetadata, payload.Metadata)
	metadata = applyImportMetadata(metadata, req, payload, template)
	authMode := strings.TrimSpace(firstNonEmpty(payload.AuthMode, template.AuthMode, "api_key"))
	credential, hasAuthState, credentialPresent, err := credentialForImportPayload(payload, template)
	if err != nil {
		errors = append(errors, err.Error())
	}
	if !credentialPresent {
		errors = append(errors, "api_key or token_bundle.access_token is required.")
	}
	if !template.RuntimeSupported {
		warnings = append(warnings, "Template is metadata-only and has no runtime adapter.")
	}
	var existingAccountID string
	if providerID != "" && name != "" {
		existingAccountID, _ = s.existingAccountID(ctx, providerID, channelID, name)
	}
	dedupKey := strings.ToLower(strings.Join([]string{providerID, channelID, name}, "|"))
	return accountImportCandidate{
		Index:             index,
		Template:          template,
		Item:              normalizedAccountImportItem{ProviderID: providerID, ChannelID: channelID, ProxyID: strings.TrimSpace(firstNonEmpty(payload.ProxyID, req.ProxyID)), OwnerUserID: ownerUserID, RoutingMode: routingMode, Name: name, AuthMode: authMode, Status: status, Priority: priority, MaxConcurrency: maxConcurrency, Metadata: metadata, Credential: credential, HasAuthState: hasAuthState, CredentialPresent: credentialPresent},
		Errors:            errors,
		Warnings:          warnings,
		DedupKey:          dedupKey,
		ExistingAccountID: existingAccountID,
	}
}

func (candidate accountImportCandidate) previewRow() map[string]any {
	return map[string]any{
		"index":               candidate.Index,
		"template":            candidate.Template.ID,
		"template_label":      candidate.Template.Label,
		"provider_id":         candidate.Item.ProviderID,
		"channel_id":          candidate.Item.ChannelID,
		"proxy_id":            candidate.Item.ProxyID,
		"routing_mode":        candidate.Item.RoutingMode,
		"owner_user_id":       candidate.Item.OwnerUserID,
		"name":                candidate.Item.Name,
		"auth_mode":           candidate.Item.AuthMode,
		"status":              candidate.Item.Status,
		"priority":            candidate.Item.Priority,
		"max_concurrency":     candidate.Item.MaxConcurrency,
		"metadata":            candidate.Item.Metadata,
		"credential_present":  candidate.Item.CredentialPresent,
		"has_auth_state":      candidate.Item.HasAuthState,
		"existing_account_id": candidate.ExistingAccountID,
		"valid":               candidate.Valid,
		"errors":              candidate.Errors,
		"warnings":            candidate.Warnings,
	}
}

func credentialForImportPayload(payload accountImportPayload, template accountImportTemplate) (tokenBundle, bool, bool, error) {
	if payload.TokenBundle.hasAccessToken() {
		bundle := defaultedImportTokenBundle(payload.TokenBundle, template)
		return bundle, bundle.Type != "api_key", true, nil
	}
	apiKey := strings.TrimSpace(payload.APIKey)
	if apiKey == "" {
		return tokenBundle{}, false, false, nil
	}
	if strings.HasPrefix(apiKey, "{") {
		bundle, err := normalizedTokenBundleFromSecret(apiKey)
		if err != nil {
			return tokenBundle{}, false, true, err
		}
		bundle = defaultedImportTokenBundle(bundle, template)
		return bundle, bundle.Type != "api_key", true, nil
	}
	tokenType := strings.TrimSpace(template.TokenType)
	if tokenType == "" {
		tokenType = "api_key"
	}
	if tokenType != "api_key" || template.AuthMode != "api_key" || template.TokenProvider != "" {
		bundle := defaultedImportTokenBundle(tokenBundle{Type: tokenType, AccessToken: apiKey}, template)
		return bundle, true, true, nil
	}
	bundle, hasAuthState, err := parseAccountSecret(apiKey, tokenBundle{})
	return bundle, hasAuthState, true, err
}

func defaultedImportTokenBundle(bundle tokenBundle, template accountImportTemplate) tokenBundle {
	bundle.Type = firstNonEmpty(bundle.Type, template.TokenType, "api_key")
	bundle.Provider = firstNonEmpty(bundle.Provider, template.TokenProvider)
	bundle.AuthScheme = normalizeAuthScheme(firstNonEmpty(bundle.AuthScheme, template.AuthScheme), bundle.Type)
	if bundle.Metadata == nil {
		bundle.Metadata = map[string]any{}
	}
	return bundle
}

func (s *Server) insertImportedAccountTx(ctx context.Context, tx *sql.Tx, item normalizedAccountImportItem) (string, error) {
	metadata, err := encodeJSON(defaultMap(item.Metadata))
	if err != nil {
		return "", err
	}
	vaultID, err := s.storeCredentialBundle(ctx, tx, item.Credential)
	if err != nil {
		return "", err
	}
	var channel, proxy, owner any
	if item.ChannelID != "" {
		channel = item.ChannelID
	}
	if item.ProxyID != "" {
		proxy = item.ProxyID
	}
	if item.OwnerUserID != "" {
		owner = item.OwnerUserID
	}
	var id string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO accounts (provider_id, channel_id, proxy_id, owner_user_id, routing_mode, credential_vault_record_id, name, status, priority, max_concurrency, metadata_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb)
		RETURNING id::text
	`, item.ProviderID, channel, proxy, owner, item.RoutingMode, vaultID, item.Name, item.Status, item.Priority, item.MaxConcurrency, metadata).Scan(&id); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO account_runtime_states (account_id) VALUES ($1)", id); err != nil {
		return "", err
	}
	if item.HasAuthState {
		if err := s.upsertAccountAuthState(ctx, tx, id, item.AuthMode, "active", item.Credential, ""); err != nil {
			return "", err
		}
	}
	if group := metadataText(item.Metadata["pool_group"]); group != "" {
		if err := s.setAccountPoolGroupByNameTx(ctx, tx, id, group); err != nil {
			return "", err
		}
	}
	return id, nil
}

func mergeMetadataMaps(values ...map[string]any) map[string]any {
	result := map[string]any{}
	for _, value := range values {
		for key, nextValue := range value {
			if existing, ok := result[key].(map[string]any); ok {
				if nextMap, ok := nextValue.(map[string]any); ok {
					result[key] = mergeMetadataMaps(existing, nextMap)
					continue
				}
			}
			result[key] = nextValue
		}
	}
	return result
}

func applyImportMetadata(metadata map[string]any, req accountImportRequest, payload accountImportPayload, template accountImportTemplate) map[string]any {
	metadata = mergeMetadataMaps(metadata)
	tags := normalizeStringList(append(normalizeStringList(req.RouteTags), payload.RouteTags...))
	if existingTags := metadataStringList(metadata["route_tags"]); len(existingTags) > 0 {
		tags = normalizeStringList(append(existingTags, tags...))
	}
	if len(tags) > 0 {
		metadata["route_tags"] = tags
	}
	if poolGroup := strings.TrimSpace(firstNonEmpty(payload.PoolGroup, req.PoolGroup, metadataText(metadata["pool_group"]))); poolGroup != "" {
		metadata["pool_group"] = poolGroup
	}
	if endpoint := strings.TrimSpace(payload.QuotaEndpoint); endpoint != "" {
		metadata["quota_endpoint"] = endpoint
	}
	importMeta := map[string]any{"template": template.ID, "source": "admin_import"}
	for key, value := range map[string]string{"external_id": payload.ExternalID, "email": payload.Email, "subject": payload.Subject} {
		if strings.TrimSpace(value) != "" {
			importMeta[key] = strings.TrimSpace(value)
		}
	}
	metadata["import"] = mergeMetadataMaps(metadataObject(metadata["import"]), importMeta)
	namespace := importMetadataNamespace(template)
	if namespace != "" {
		namespaced := metadataObject(metadata[namespace])
		for key, value := range map[string]string{
			"client_version":    payload.ClientVersion,
			"platform":          payload.Platform,
			"arch":              payload.Arch,
			"device_id":         payload.DeviceID,
			"user_agent":        payload.UserAgent,
			"quota_project_id":  payload.QuotaProjectID,
			"organization":      payload.OrganizationID,
			"organization_uuid": payload.OrganizationID,
			"project_id":        payload.ProjectID,
			"fingerprint":       payload.Fingerprint,
			"ide_name":          payload.IDEName,
			"ide_version":       payload.IDEVersion,
			"extension_name":    payload.ExtensionName,
			"extension_version": payload.ExtensionVersion,
		} {
			if strings.TrimSpace(value) != "" {
				namespaced[key] = strings.TrimSpace(value)
			}
		}
		if len(namespaced) > 0 {
			metadata[namespace] = namespaced
		}
	}
	return metadata
}

func importMetadataNamespace(template accountImportTemplate) string {
	for _, namespace := range []string{"codex", "claude", "gemini", "antigravity", "kiro", "windsurf"} {
		if _, ok := template.Metadata[namespace]; ok {
			return namespace
		}
	}
	return ""
}

func metadataObject(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return mergeMetadataMaps(typed)
	}
	return map[string]any{}
}

func metadataStringList(value any) []string {
	switch typed := value.(type) {
	case []string:
		return normalizeStringList(typed)
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, metadataText(item))
		}
		return normalizeStringList(items)
	default:
		return nil
	}
}

func (s *Server) idExists(ctx context.Context, table string, id string) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id::text = $1)", id).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (s *Server) providerIDForChannel(ctx context.Context, channelID string) (string, error) {
	var providerID string
	err := s.db.QueryRowContext(ctx, "SELECT provider_id::text FROM channels WHERE id::text = $1", strings.TrimSpace(channelID)).Scan(&providerID)
	return providerID, err
}

func (s *Server) existingAccountID(ctx context.Context, providerID string, channelID string, name string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text
		FROM accounts
		WHERE provider_id::text = $1
		  AND COALESCE(channel_id::text, '') = $2
		  AND lower(name) = lower($3)
		LIMIT 1
	`, strings.TrimSpace(providerID), strings.TrimSpace(channelID), strings.TrimSpace(name)).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}
