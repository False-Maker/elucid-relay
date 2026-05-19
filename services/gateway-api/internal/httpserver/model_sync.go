package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

type discoveredModel struct {
	ID        string
	Raw       map[string]any
	Endpoints []string
}

type channelAbilitySnapshot struct {
	ModelName     string
	Endpoint      string
	UpstreamModel string
	Status        string
}

type modelSyncDiff struct {
	AddedModels    []string `json:"added_models"`
	UpdatedModels  []string `json:"updated_models"`
	MissingModels  []string `json:"missing_models"`
	UnchangedCount int      `json:"unchanged_count"`
}

func (s *Server) adminChannelModelSync(w http.ResponseWriter, r *http.Request, auth authContext) error {
	channelID := r.PathValue("channelId")
	jobID, err := s.createModelSyncJob(r.Context(), channelID, auth.UserID)
	if err != nil {
		return err
	}
	result, syncErr := s.runChannelModelSync(r.Context(), channelID, jobID)
	if syncErr != nil {
		_, _ = s.db.ExecContext(r.Context(), `
			UPDATE model_sync_jobs
			SET status = 'failed', finished_at = now(), error_message = $2, metadata_json = $3::jsonb
			WHERE id = $1
		`, jobID, truncateForStorage(syncErr.Error(), 1000), mustEncodeJSON(result))
		return syncErr
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "channel.model_sync", "channel", channelID, r, result)
	writeJSON(w, http.StatusOK, result, nil)
	return nil
}

func (s *Server) adminModelSyncJobs(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT msj.id::text, COALESCE(msj.channel_id::text, ''), COALESCE(c.name, ''), COALESCE(msj.provider_id::text, ''),
		       msj.status, COALESCE(msj.requested_by::text, ''), msj.discovered_count, msj.updated_count,
		       COALESCE(msj.error_message, ''), COALESCE(msj.metadata_json::text, '{}'), msj.started_at, msj.finished_at, msj.created_at
		FROM model_sync_jobs msj
		LEFT JOIN channels c ON c.id = msj.channel_id
		ORDER BY msj.created_at DESC
		LIMIT $1
	`, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, channelID, channelName, providerID, status, requestedBy, errorMessage, metadata string
		var discovered, updated int
		var startedAt, finishedAt sql.NullTime
		var createdAt time.Time
		if err := rows.Scan(&id, &channelID, &channelName, &providerID, &status, &requestedBy, &discovered, &updated, &errorMessage, &metadata, &startedAt, &finishedAt, &createdAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":               id,
			"channel_id":       channelID,
			"channel_name":     channelName,
			"provider_id":      providerID,
			"status":           status,
			"requested_by":     requestedBy,
			"discovered_count": discovered,
			"updated_count":    updated,
			"error_message":    errorMessage,
			"metadata":         jsonRaw(metadata),
			"started_at":       nullableSQLTime(startedAt),
			"finished_at":      nullableSQLTime(finishedAt),
			"created_at":       createdAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminRunModelSyncJobs(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		ChannelIDs []string `json:"channel_ids"`
		Limit      int      `json:"limit"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	channelIDs := normalizeStringList(req.ChannelIDs)
	if len(channelIDs) == 0 {
		limit := req.Limit
		if limit <= 0 {
			limit = 100
		}
		if limit > 500 {
			limit = 500
		}
		rows, err := s.db.QueryContext(r.Context(), `
			SELECT id::text
			FROM channels
			WHERE status = 'active'
			ORDER BY priority ASC, created_at ASC
			LIMIT $1
		`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var channelID string
			if err := rows.Scan(&channelID); err != nil {
				return err
			}
			channelIDs = append(channelIDs, channelID)
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	results := []map[string]any{}
	successCount := 0
	failedCount := 0
	discoveredCount := 0
	updatedCount := 0
	for _, channelID := range channelIDs {
		jobID, err := s.createModelSyncJob(r.Context(), channelID, auth.UserID)
		if err != nil {
			failedCount++
			results = append(results, map[string]any{"channel_id": channelID, "status": "failed", "error_message": err.Error()})
			continue
		}
		result, syncErr := s.runChannelModelSync(r.Context(), channelID, jobID)
		if syncErr != nil {
			_, _ = s.db.ExecContext(r.Context(), `
				UPDATE model_sync_jobs
				SET status = 'failed', finished_at = now(), error_message = $2, metadata_json = $3::jsonb
				WHERE id = $1
			`, jobID, truncateForStorage(syncErr.Error(), 1000), mustEncodeJSON(result))
			failedCount++
			results = append(results, map[string]any{"job_id": jobID, "channel_id": channelID, "status": "failed", "error_message": syncErr.Error()})
			continue
		}
		successCount++
		discoveredCount += intFromAny(result["discovered_count"])
		updatedCount += intFromAny(result["updated_count"])
		result["status"] = "success"
		results = append(results, result)
	}
	response := map[string]any{
		"channel_count":    len(channelIDs),
		"success_count":    successCount,
		"failed_count":     failedCount,
		"discovered_count": discoveredCount,
		"updated_count":    updatedCount,
		"results":          results,
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "model_sync.batch_run", "model_sync_job", "", r, response)
	writeJSON(w, http.StatusOK, response, nil)
	return nil
}

func (s *Server) createModelSyncJob(ctx context.Context, channelID string, userID string) (string, error) {
	var providerID string
	err := s.db.QueryRowContext(ctx, "SELECT provider_id::text FROM channels WHERE id = $1", channelID).Scan(&providerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("Channel was not found.")
	}
	if err != nil {
		return "", err
	}
	var id string
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO model_sync_jobs (channel_id, provider_id, status, requested_by)
		VALUES ($1, $2, 'pending', $3)
		RETURNING id::text
	`, channelID, providerID, userID).Scan(&id)
	return id, err
}

func (s *Server) runChannelModelSync(ctx context.Context, channelID string, jobID string) (map[string]any, error) {
	_, _ = s.db.ExecContext(ctx, "UPDATE model_sync_jobs SET status = 'running', started_at = now() WHERE id = $1", jobID)
	models, providerType, err := s.discoverChannelModels(ctx, channelID)
	if err != nil {
		return map[string]any{"channel_id": channelID, "job_id": jobID}, err
	}
	existingAbilities, err := s.channelAbilitySnapshots(ctx, channelID)
	if err != nil {
		return nil, err
	}
	diff := diffChannelModels(existingAbilities, models, providerType)
	updated := 0
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		if len(model.Endpoints) == 0 {
			model.Endpoints = defaultSyncEndpoints(providerType)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO model_catalog (model_name, display_name, provider_hint, endpoint_capabilities, public_visible, status, metadata_json)
			VALUES ($1, $1, $2, $3::jsonb, false, 'active', $4::jsonb)
			ON CONFLICT (model_name) DO UPDATE SET
			  provider_hint = COALESCE(NULLIF(model_catalog.provider_hint, ''), EXCLUDED.provider_hint),
			  metadata_json = model_catalog.metadata_json || EXCLUDED.metadata_json
		`, model.ID, "channel_sync", mustEncodeJSON(model.Endpoints), mustEncodeJSON(map[string]any{"last_discovered_from_channel_id": channelID, "upstream": model.Raw})); err != nil {
			return nil, err
		}
		for _, endpoint := range model.Endpoints {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO channel_abilities (channel_id, model_name, endpoint, upstream_model, status)
				VALUES ($1, $2, $3, $2, 'active')
				ON CONFLICT (channel_id, model_name, endpoint)
				DO UPDATE SET status = 'active', upstream_model = EXCLUDED.upstream_model
			`, channelID, model.ID, endpoint); err != nil {
				return nil, err
			}
		}
		updated++
	}
	syncMetadata := map[string]any{
		"last_model_sync_at": time.Now().UTC().Format(time.RFC3339),
		"discovered_count":   len(models),
		"updated_count":      updated,
		"added_models":       diff.AddedModels,
		"updated_models":     diff.UpdatedModels,
		"missing_models":     diff.MissingModels,
		"unchanged_count":    diff.UnchangedCount,
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE channels
		SET metadata_json = CASE
		  WHEN jsonb_typeof(metadata_json) = 'object' THEN metadata_json
		  ELSE '{}'::jsonb
		END || $2::jsonb
		WHERE id = $1
	`, channelID, mustEncodeJSON(syncMetadata)); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE model_sync_jobs
		SET status = 'success', discovered_count = $2, updated_count = $3, finished_at = now(), metadata_json = $4::jsonb
		WHERE id = $1
	`, jobID, len(models), updated, mustEncodeJSON(syncMetadata)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"job_id": jobID, "channel_id": channelID, "discovered_count": len(models), "updated_count": updated, "diff": diff}, nil
}

func (s *Server) channelAbilitySnapshots(ctx context.Context, channelID string) ([]channelAbilitySnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT model_name, endpoint, upstream_model, status
		FROM channel_abilities
		WHERE channel_id = $1
	`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []channelAbilitySnapshot{}
	for rows.Next() {
		var item channelAbilitySnapshot
		if err := rows.Scan(&item.ModelName, &item.Endpoint, &item.UpstreamModel, &item.Status); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func diffChannelModels(existing []channelAbilitySnapshot, discovered []discoveredModel, providerType string) modelSyncDiff {
	existingByKey := map[string]channelAbilitySnapshot{}
	for _, item := range existing {
		key := modelSyncAbilityKey(item.ModelName, item.Endpoint)
		if key != "" {
			existingByKey[key] = item
		}
	}
	discoveredKeys := map[string]struct{}{}
	diff := modelSyncDiff{}
	for _, model := range discovered {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" {
			continue
		}
		endpoints := model.Endpoints
		if len(endpoints) == 0 {
			endpoints = defaultSyncEndpoints(providerType)
		}
		for _, endpoint := range endpoints {
			key := modelSyncAbilityKey(model.ID, endpoint)
			if key == "" {
				continue
			}
			discoveredKeys[key] = struct{}{}
			existingItem, exists := existingByKey[key]
			label := model.ID + ":" + strings.TrimSpace(endpoint)
			switch {
			case !exists:
				diff.AddedModels = append(diff.AddedModels, label)
			case !strings.EqualFold(existingItem.Status, "active") || existingItem.UpstreamModel != model.ID:
				diff.UpdatedModels = append(diff.UpdatedModels, label)
			default:
				diff.UnchangedCount++
			}
		}
	}
	for _, item := range existing {
		if !strings.EqualFold(item.Status, "active") {
			continue
		}
		key := modelSyncAbilityKey(item.ModelName, item.Endpoint)
		if key == "" {
			continue
		}
		if _, ok := discoveredKeys[key]; !ok {
			diff.MissingModels = append(diff.MissingModels, item.ModelName+":"+item.Endpoint)
		}
	}
	return diff
}

func modelSyncAbilityKey(modelName string, endpoint string) string {
	modelName = strings.TrimSpace(modelName)
	endpoint = strings.TrimSpace(endpoint)
	if modelName == "" || endpoint == "" {
		return ""
	}
	return modelName + "\x00" + endpoint
}

func (s *Server) discoverChannelModels(ctx context.Context, channelID string) ([]discoveredModel, string, error) {
	var route routeInfo
	var ciphertext, nonce []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT p.provider_type, c.base_url, COALESCE(ap.proxy_url, cp.proxy_url, ''),
		       COALESCE(v.secret_ciphertext, '\x'::bytea), COALESCE(v.secret_nonce, '\x'::bytea)
		FROM channels c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN proxies cp ON cp.id = c.proxy_id AND cp.status = 'active'
		LEFT JOIN LATERAL (
		  SELECT a.proxy_id, a.credential_vault_record_id
		  FROM accounts a
		  WHERE a.channel_id = c.id AND a.status = 'active'
		  ORDER BY a.priority ASC, a.created_at ASC
		  LIMIT 1
		) a ON true
		LEFT JOIN proxies ap ON ap.id = a.proxy_id AND ap.status = 'active'
		LEFT JOIN credential_vault_records v ON v.id = a.credential_vault_record_id
		WHERE c.id = $1
	`, channelID).Scan(&route.ProviderType, &route.BaseURL, &route.ProxyURL, &ciphertext, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", notFound("Channel was not found.")
	}
	if err != nil {
		return nil, "", err
	}
	if len(ciphertext) > 0 {
		secret, err := security.DecryptSecret(s.cfg.VaultKey, ciphertext, nonce)
		if err != nil {
			return nil, "", err
		}
		bundle, err := normalizedTokenBundleFromSecret(secret)
		if err != nil {
			return nil, "", err
		}
		route.APIKey = bundle.authSecret()
		route.TokenProvider = bundle.Provider
		route.AuthScheme = bundle.AuthScheme
		route.TokenSubject = bundle.Subject
		route.TokenMetadata = bundle.Metadata
	}
	var client httpDoer = s.httpClient
	if strings.TrimSpace(route.ProxyURL) != "" {
		if s.upstreamPool == nil {
			s.upstreamPool = newUpstreamClientPool()
		}
		nextClient, err := s.upstreamPool.client(route, true)
		if err != nil {
			return nil, "", err
		}
		client = withHTTPClientTimeout(nextClient, 20*time.Second)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelDiscoveryURL(route), nil)
	if err != nil {
		return nil, "", err
	}
	if route.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+route.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", upstreamUnavailable("model_sync_failed", "Upstream model discovery failed.")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", appError{status: http.StatusBadGateway, code: "model_sync_failed", message: truncateForStorage(string(body), 500), typ: "upstream_error"}
	}
	models := parseDiscoveredModels(body, route.ProviderType)
	return models, route.ProviderType, nil
}

func modelDiscoveryURL(route routeInfo) string {
	if isCodexOfficialRoute(route) || strings.Contains(route.ProviderType, "codex") {
		return codexModelsEndpoint(route)
	}
	parsed, err := url.Parse(strings.TrimRight(route.BaseURL, "/"))
	if err != nil {
		return strings.TrimRight(route.BaseURL, "/") + "/v1/models"
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		path += "/models"
	} else {
		path += "/v1/models"
	}
	if path == "" {
		path = "/v1/models"
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func parseDiscoveredModels(body []byte, providerType string) []discoveredModel {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	items := []any{}
	switch typed := payload.(type) {
	case map[string]any:
		if data, ok := typed["data"].([]any); ok {
			items = data
		} else if models, ok := typed["models"].([]any); ok {
			items = models
		}
	case []any:
		items = typed
	}
	models := []discoveredModel{}
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			models = append(models, discoveredModel{ID: typed, Endpoints: defaultSyncEndpoints(providerType)})
		case map[string]any:
			id := firstNonEmpty(metadataText(typed["id"]), metadataText(typed["slug"]), metadataText(typed["name"]), metadataText(typed["model"]))
			if id == "" {
				continue
			}
			models = append(models, discoveredModel{ID: id, Raw: typed, Endpoints: endpointsFromModelPayload(typed, providerType)})
		}
	}
	return models
}

func endpointsFromModelPayload(model map[string]any, providerType string) []string {
	for _, key := range []string{"endpoint_capabilities", "endpoints", "capabilities"} {
		if raw, ok := model[key]; ok {
			if values := stringsFromAny(raw); len(values) > 0 {
				return values
			}
		}
	}
	return defaultSyncEndpoints(providerType)
}

func defaultSyncEndpoints(providerType string) []string {
	switch providerType {
	case "anthropic", "anthropic_compatible", "claude_compatible":
		return []string{"messages"}
	case "gemini", "gemini_openai_compatible", "gemini_cli", "antigravity", "kiro":
		return []string{"chat"}
	default:
		return []string{"chat", "responses"}
	}
}

func stringsFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		return normalizeStringList(typed)
	case []any:
		values := []string{}
		for _, item := range typed {
			if text := metadataText(item); text != "" {
				values = append(values, text)
			}
		}
		return normalizeStringList(values)
	case string:
		return normalizeStringList(strings.Split(typed, ","))
	default:
		return nil
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		next, _ := typed.Int64()
		return int(next)
	default:
		return 0
	}
}
