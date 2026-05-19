package httpserver

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

func (s *Server) adminProviderClients(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, provider_id::text, name, client_type, status,
		       COALESCE(credential_vault_record_id::text, ''), metadata_json::text, created_at, updated_at
		FROM provider_clients
		ORDER BY created_at DESC
		LIMIT $1
	`, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, providerID, name, clientType, status, vaultID, metadata string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &providerID, &name, &clientType, &status, &vaultID, &metadata, &createdAt, &updatedAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":                         id,
			"provider_id":                providerID,
			"name":                       name,
			"client_type":                clientType,
			"status":                     status,
			"credential_vault_record_id": vaultID,
			"metadata":                   jsonRaw(metadata),
			"created_at":                 createdAt.UTC().Format(time.RFC3339),
			"updated_at":                 updatedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateProviderClient(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		ProviderID string         `json:"provider_id"`
		Name       string         `json:"name"`
		ClientType string         `json:"client_type"`
		Secret     string         `json:"secret"`
		Status     string         `json:"status"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.ProviderID) == "" || strings.TrimSpace(req.Name) == "" {
		return badRequest("provider_id and name are required.")
	}
	if req.ClientType == "" {
		req.ClientType = "api_key"
	}
	status, err := defaultedStatus(req.Status, "active", "active", "disabled")
	if err != nil {
		return err
	}
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var vault any
	if strings.TrimSpace(req.Secret) != "" {
		ciphertext, nonce, err := security.EncryptSecret(s.cfg.VaultKey, req.Secret)
		if err != nil {
			return err
		}
		var vaultID string
		if err := tx.QueryRowContext(r.Context(), `
			INSERT INTO credential_vault_records (secret_ciphertext, secret_nonce)
			VALUES ($1, $2)
			RETURNING id::text
		`, ciphertext, nonce).Scan(&vaultID); err != nil {
			return err
		}
		vault = vaultID
	}

	var id string
	if err := tx.QueryRowContext(r.Context(), `
		INSERT INTO provider_clients (provider_id, name, client_type, status, credential_vault_record_id, metadata_json)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		RETURNING id::text
	`, strings.TrimSpace(req.ProviderID), strings.TrimSpace(req.Name), strings.TrimSpace(req.ClientType), status, vault, metadata).Scan(&id); err != nil {
		return err
	}
	audit(r.Context(), tx, auth.UserID, "admin", "provider_client.create", "provider_client", id, r, nil)
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminPatchProviderClient(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id := r.PathValue("providerClientId")
	var req struct {
		Name       *string        `json:"name"`
		ClientType *string        `json:"client_type"`
		Secret     *string        `json:"secret"`
		Status     *string        `json:"status"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := s.requireIDExists(r.Context(), "provider_clients", id, "Provider client was not found."); err != nil {
		return err
	}
	if req.Name != nil {
		if _, err := s.db.ExecContext(r.Context(), "UPDATE provider_clients SET name = $2 WHERE id = $1", id, strings.TrimSpace(*req.Name)); err != nil {
			return err
		}
	}
	if req.ClientType != nil {
		if _, err := s.db.ExecContext(r.Context(), "UPDATE provider_clients SET client_type = $2 WHERE id = $1", id, strings.TrimSpace(*req.ClientType)); err != nil {
			return err
		}
	}
	if req.Status != nil {
		status, err := defaultedStatus(*req.Status, "active", "active", "disabled")
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE provider_clients SET status = $2 WHERE id = $1", id, status); err != nil {
			return err
		}
	}
	if req.Secret != nil {
		if strings.TrimSpace(*req.Secret) == "" {
			return badRequest("secret cannot be empty.")
		}
		ciphertext, nonce, err := security.EncryptSecret(s.cfg.VaultKey, *req.Secret)
		if err != nil {
			return err
		}
		var vaultID string
		if err := s.db.QueryRowContext(r.Context(), `
			INSERT INTO credential_vault_records (secret_ciphertext, secret_nonce)
			VALUES ($1, $2)
			RETURNING id::text
		`, ciphertext, nonce).Scan(&vaultID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE provider_clients SET credential_vault_record_id = $2 WHERE id = $1", id, vaultID); err != nil {
			return err
		}
	}
	if req.Metadata != nil {
		metadata, err := encodeJSON(defaultMap(req.Metadata))
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE provider_clients SET metadata_json = $2::jsonb WHERE id = $1", id, metadata); err != nil {
			return err
		}
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "provider_client.update", "provider_client", id, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) adminQuotaWindows(w http.ResponseWriter, r *http.Request, auth authContext) error {
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, account_id::text, window_type, reset_at, COALESCE(remaining::text, ''),
		       metadata_json::text, created_at, updated_at
		FROM account_quota_windows
		WHERE ($1 = '' OR account_id::text = $1)
		ORDER BY created_at DESC
		LIMIT $2
	`, accountID, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, rowAccountID, windowType, remaining, metadata string
		var resetAt sql.NullTime
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &rowAccountID, &windowType, &resetAt, &remaining, &metadata, &createdAt, &updatedAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":          id,
			"account_id":  rowAccountID,
			"window_type": windowType,
			"reset_at":    nullableTime(&resetAt.Time),
			"remaining":   nullableString(remaining),
			"metadata":    jsonRaw(metadata),
			"created_at":  createdAt.UTC().Format(time.RFC3339),
			"updated_at":  updatedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateQuotaWindow(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		AccountID  string         `json:"account_id"`
		WindowType string         `json:"window_type"`
		ResetAt    string         `json:"reset_at"`
		Remaining  *string        `json:"remaining"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.AccountID) == "" || strings.TrimSpace(req.WindowType) == "" {
		return badRequest("account_id and window_type are required.")
	}
	resetAt, err := nullableRFC3339(req.ResetAt)
	if err != nil {
		return err
	}
	remaining, err := nullableNonNegativeDecimal(req.Remaining)
	if err != nil {
		return err
	}
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return err
	}
	var id string
	if err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO account_quota_windows (account_id, window_type, reset_at, remaining, metadata_json)
		VALUES ($1, $2, $3, $4::numeric, $5::jsonb)
		RETURNING id::text
	`, req.AccountID, req.WindowType, resetAt, remaining, metadata).Scan(&id); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "quota_window.create", "account_quota_window", id, r, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminPatchQuotaWindow(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id := r.PathValue("quotaWindowId")
	var req struct {
		WindowType *string        `json:"window_type"`
		ResetAt    *string        `json:"reset_at"`
		Remaining  *string        `json:"remaining"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := s.requireIDExists(r.Context(), "account_quota_windows", id, "Quota window was not found."); err != nil {
		return err
	}
	if req.WindowType != nil {
		if _, err := s.db.ExecContext(r.Context(), "UPDATE account_quota_windows SET window_type = $2 WHERE id = $1", id, strings.TrimSpace(*req.WindowType)); err != nil {
			return err
		}
	}
	if req.ResetAt != nil {
		resetAt, err := nullableRFC3339(*req.ResetAt)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE account_quota_windows SET reset_at = $2 WHERE id = $1", id, resetAt); err != nil {
			return err
		}
	}
	if req.Remaining != nil {
		remaining, err := nullableNonNegativeDecimal(req.Remaining)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE account_quota_windows SET remaining = $2::numeric WHERE id = $1", id, remaining); err != nil {
			return err
		}
	}
	if req.Metadata != nil {
		metadata, err := encodeJSON(defaultMap(req.Metadata))
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE account_quota_windows SET metadata_json = $2::jsonb WHERE id = $1", id, metadata); err != nil {
			return err
		}
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "quota_window.update", "account_quota_window", id, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableRFC3339(value string) (any, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, badRequest("reset_at must be RFC3339.")
	}
	return parsed, nil
}

func nullableNonNegativeDecimal(value *string) (any, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || parsed < 0 {
		return nil, badRequest("remaining must be a non-negative decimal string.")
	}
	return trimmed, nil
}

type routeExplainAffinity struct {
	RoutingMode string
	UserID      string
	APIKeyID    string
	AffinityKey string
	RouteTags   []string
}

func (s *Server) routeCandidates(ctx context.Context, model string, endpoint string, affinity routeExplainAffinity) ([]map[string]any, error) {
	affinityHash := ""
	if strings.TrimSpace(affinity.AffinityKey) != "" {
		affinityHash = routeAffinityHash(affinity.AffinityKey)
	}
	routeTagsJSON := "[]"
	if len(affinity.RouteTags) > 0 {
		routeTagsJSON = mustEncodeJSON(affinity.RouteTags)
	}
	routingMode, err := routingModeValue(affinity.RoutingMode)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id::text, c.name, a.id::text, a.name, p.id::text, p.name, p.provider_type,
		       COALESCE(NULLIF(ca.upstream_model, ''), ca.model_name), ca.transform_capability_json::text,
		       p.status, c.status, a.status,
		       a.routing_mode, COALESCE(a.owner_user_id::text, ''),
		       COALESCE(aas.auth_status, ''), aas.expires_at, aas.refresh_due_at,
		       COALESCE(ars.active_requests, 0), a.max_concurrency, ars.cooldown_until, COALESCE(ars.last_error, ''),
		       COALESCE(ars.circuit_state, 'closed'), COALESCE(ars.circuit_failure_count, 0),
		       ars.circuit_opened_at, ars.circuit_half_open_after,
		       (a.credential_vault_record_id IS NOT NULL) AS has_vault,
		       (a.routing_mode = $6 AND (($6 = 'pool' AND a.owner_user_id IS NULL) OR ($6 = 'byo' AND a.owner_user_id::text = $3))) AS owner_scope_match,
		       EXISTS (
		         SELECT 1 FROM account_quota_windows qw
		         WHERE qw.account_id = a.id
		           AND (qw.reset_at IS NULL OR qw.reset_at > now())
		           AND qw.remaining IS NOT NULL
		           AND qw.remaining <= 0
		       ) AS quota_exhausted,
		       COALESCE((
		         SELECT MAX(
		           CASE
		             WHEN (qw.metadata_json->>'limit') ~ '^[0-9]+(\.[0-9]+)?$'
		               AND NULLIF(qw.metadata_json->>'limit', '')::numeric > 0
		               THEN qw.remaining / NULLIF(qw.metadata_json->>'limit', '')::numeric
		             ELSE LEAST(qw.remaining, 1000) / 1000
		           END
		         )
		         FROM account_quota_windows qw
		         WHERE qw.account_id = a.id
		           AND (qw.reset_at IS NULL OR qw.reset_at > now())
		           AND qw.remaining IS NOT NULL
		           AND qw.remaining > 0
		       ), 1)::float8 AS headroom_score,
		       CASE
		         WHEN ars.last_failure_at IS NOT NULL AND ars.last_failure_at > now() - interval '10 minutes'
		           THEN 1 + (ars.failure_count::numeric / GREATEST(ars.success_count + ars.failure_count, 1))
		         ELSE (ars.failure_count::numeric / GREATEST(ars.success_count + ars.failure_count, 1)) * 0.25
		       END::float8 AS failure_penalty,
		       (
		         SELECT MIN(qw.reset_at)
		         FROM account_quota_windows qw
		         WHERE qw.account_id = a.id
		           AND qw.reset_at IS NOT NULL
		           AND qw.reset_at > now()
		           AND qw.remaining IS NOT NULL
		           AND qw.remaining > 0
		       ) AS quota_reset_at,
		       ($3 <> '' AND $4 <> '' AND $5 <> '' AND EXISTS (
		         SELECT 1
		         FROM northbound_route_affinities ra
		         WHERE ra.user_id::text = $3
		           AND ra.api_key_id::text = $4
		           AND ra.model_name = $1
		           AND ra.endpoint = $2
		           AND ra.session_key_hash = $5
		           AND ra.account_id = a.id
		           AND ra.expires_at > now()
		       )) AS affinity_match,
		       c.priority, a.priority, ca.priority, c.weight, ca.weight, ca.retry_priority, a.created_at
		FROM channel_abilities ca
		JOIN channels c ON c.id = ca.channel_id
		JOIN providers p ON p.id = c.provider_id
		JOIN accounts a ON a.channel_id = c.id
		LEFT JOIN account_runtime_states ars ON ars.account_id = a.id
		LEFT JOIN account_auth_states aas ON aas.account_id = a.id
		WHERE ca.model_name = $1 AND ca.endpoint = $2
		  AND (
		    $7::jsonb = '[]'::jsonb
		    OR EXISTS (
		      SELECT 1
		      FROM jsonb_array_elements_text($7::jsonb) requested_tag(value)
		      WHERE requested_tag.value IN (
		        SELECT lower(channel_tag.value)
		        FROM jsonb_array_elements_text(
		          CASE
		            WHEN jsonb_typeof(c.metadata_json->'route_tags') = 'array' THEN c.metadata_json->'route_tags'
		            WHEN jsonb_typeof(c.metadata_json->'routing_profiles') = 'array' THEN c.metadata_json->'routing_profiles'
		            WHEN jsonb_typeof(c.metadata_json->'profiles') = 'array' THEN c.metadata_json->'profiles'
		            WHEN jsonb_typeof(c.metadata_json->'route_profile') = 'string' THEN jsonb_build_array(c.metadata_json->>'route_profile')
		            ELSE '[]'::jsonb
		          END
		        ) channel_tag(value)
		        UNION
		        SELECT lower(account_tag.value)
		        FROM jsonb_array_elements_text(
		          CASE
		            WHEN jsonb_typeof(a.metadata_json->'route_tags') = 'array' THEN a.metadata_json->'route_tags'
		            WHEN jsonb_typeof(a.metadata_json->'routing_profiles') = 'array' THEN a.metadata_json->'routing_profiles'
		            WHEN jsonb_typeof(a.metadata_json->'profiles') = 'array' THEN a.metadata_json->'profiles'
		            WHEN jsonb_typeof(a.metadata_json->'route_profile') = 'string' THEN jsonb_build_array(a.metadata_json->>'route_profile')
		            ELSE '[]'::jsonb
		          END
		        ) account_tag(value)
		      )
		    )
		  )
		ORDER BY owner_scope_match DESC,
		         CASE WHEN COALESCE(aas.auth_status, '') IN ('', 'active', 'refresh_due') AND (aas.expires_at IS NULL OR aas.expires_at > now()) THEN 0 ELSE 1 END ASC,
		         affinity_match DESC,
		         headroom_score DESC,
		         c.priority ASC,
		         ca.priority ASC,
		         a.priority ASC,
		         failure_penalty ASC,
		         quota_reset_at ASC NULLS LAST,
		         COALESCE(ars.active_requests, 0) ASC,
		         a.created_at ASC
		LIMIT 50
	`, model, endpoint, strings.TrimSpace(affinity.UserID), strings.TrimSpace(affinity.APIKeyID), affinityHash, routingMode, routeTagsJSON)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var channelID, channelName, accountID, accountName, providerID, providerName, providerType, upstreamModel, transform, providerStatus, channelStatus, accountStatus, accountRoutingMode, ownerUserID, authStatus, lastError string
		var circuitState string
		var activeRequests, maxConcurrency, circuitFailureCount, channelPriority, accountPriority, abilityPriority, channelWeight, abilityWeight, retryPriority int
		var headroomScore, failurePenalty float64
		var authExpiresAt, refreshDueAt, cooldownUntil, circuitOpenedAt, circuitHalfOpenAfter, quotaResetAt sql.NullTime
		var accountCreatedAt time.Time
		var hasVault, ownerScopeMatch, quotaExhausted, affinityMatch bool
		if err := rows.Scan(
			&channelID, &channelName, &accountID, &accountName, &providerID, &providerName, &providerType, &upstreamModel, &transform,
			&providerStatus, &channelStatus, &accountStatus, &accountRoutingMode, &ownerUserID, &authStatus, &authExpiresAt, &refreshDueAt,
			&activeRequests, &maxConcurrency, &cooldownUntil, &lastError, &circuitState, &circuitFailureCount, &circuitOpenedAt, &circuitHalfOpenAfter,
			&hasVault, &ownerScopeMatch, &quotaExhausted, &headroomScore, &failurePenalty, &quotaResetAt,
			&affinityMatch, &channelPriority, &accountPriority, &abilityPriority, &channelWeight, &abilityWeight, &retryPriority, &accountCreatedAt,
		); err != nil {
			return nil, err
		}

		now := time.Now()
		authRouteable := authStatus == "" || authStatus == "active" || authStatus == "refresh_due"
		authExpired := authExpiresAt.Valid && !authExpiresAt.Time.After(now)
		circuitOpenReady := circuitState == "open" && circuitHalfOpenAfter.Valid && !circuitHalfOpenAfter.Time.After(now)
		circuitOpen := circuitState == "open" && !circuitOpenReady
		circuitHalfOpenBusy := (circuitState == "half_open" || circuitOpenReady) && activeRequests > 0
		eligible := providerStatus == "active" &&
			channelStatus == "active" &&
			accountStatus == "active" &&
			hasVault &&
			ownerScopeMatch &&
			authRouteable &&
			!authExpired &&
			activeRequests < maxConcurrency &&
			(!cooldownUntil.Valid || cooldownUntil.Time.Before(now)) &&
			!circuitOpen &&
			!circuitHalfOpenBusy &&
			!quotaExhausted
		reasons := []string{}
		if !ownerScopeMatch {
			if routingMode == "byo" {
				reasons = append(reasons, "owner_scope_mismatch")
			} else {
				reasons = append(reasons, "routing_mode_mismatch")
			}
		}
		if providerStatus != "active" {
			reasons = append(reasons, "provider_"+providerStatus)
		}
		if channelStatus != "active" {
			reasons = append(reasons, "channel_"+channelStatus)
		}
		if accountStatus != "active" {
			reasons = append(reasons, "account_"+accountStatus)
		}
		if !hasVault {
			reasons = append(reasons, "missing_vault")
		}
		if !authRouteable {
			if authStatus == "" {
				reasons = append(reasons, "auth_missing")
			} else {
				reasons = append(reasons, "auth_"+authStatus)
			}
		}
		if authExpired {
			reasons = append(reasons, "auth_expired")
		}
		if activeRequests >= maxConcurrency {
			reasons = append(reasons, "concurrency_full")
		}
		if cooldownUntil.Valid && cooldownUntil.Time.After(now) {
			reasons = append(reasons, "cooldown")
		}
		if circuitOpen {
			reasons = append(reasons, "circuit_open")
		}
		if circuitHalfOpenBusy {
			reasons = append(reasons, "circuit_half_open_probe_busy")
		}
		if quotaExhausted {
			reasons = append(reasons, "quota_exhausted")
		}

		items = append(items, map[string]any{
			"channel_id":              channelID,
			"channel_name":            channelName,
			"account_id":              accountID,
			"account_name":            accountName,
			"provider_id":             providerID,
			"provider_name":           providerName,
			"provider_type":           providerType,
			"provider_status":         providerStatus,
			"routing_mode":            accountRoutingMode,
			"owner_user_id":           ownerUserID,
			"owner_scope":             strings.TrimSpace(affinity.UserID),
			"owner_scope_match":       ownerScopeMatch,
			"auth_status":             authStatus,
			"auth_expires_at":         nullableSQLTime(authExpiresAt),
			"refresh_due_at":          nullableSQLTime(refreshDueAt),
			"upstream_model":          upstreamModel,
			"transform_capability":    jsonRaw(transform),
			"eligible":                eligible,
			"excluded_reasons":        reasons,
			"reasons":                 reasons,
			"affinity_match":          affinityMatch,
			"active_requests":         activeRequests,
			"max_concurrency":         maxConcurrency,
			"cooldown_until":          nullableSQLTime(cooldownUntil),
			"last_error":              lastError,
			"circuit_state":           circuitState,
			"circuit_failure_count":   circuitFailureCount,
			"circuit_opened_at":       nullableSQLTime(circuitOpenedAt),
			"circuit_half_open_after": nullableSQLTime(circuitHalfOpenAfter),
			"quota_exhausted":         quotaExhausted,
			"quota_reset_at":          nullableSQLTime(quotaResetAt),
			"headroom_score":          headroomScore,
			"failure_penalty":         failurePenalty,
			"channel_priority":        channelPriority,
			"account_priority":        accountPriority,
			"ability_priority":        abilityPriority,
			"channel_weight":          channelWeight,
			"ability_weight":          abilityWeight,
			"retry_priority":          retryPriority,
			"score": map[string]any{
				"priority":            channelPriority + abilityPriority + accountPriority,
				"weight":              channelWeight * abilityWeight,
				"retry_priority":      retryPriority,
				"current_concurrency": activeRequests,
				"concurrency_limit":   maxConcurrency,
			},
			"final_sort": map[string]any{
				"channel_priority":   channelPriority,
				"ability_priority":   abilityPriority,
				"account_priority":   accountPriority,
				"headroom_score":     headroomScore,
				"failure_penalty":    failurePenalty,
				"circuit_state":      circuitState,
				"quota_reset_at":     nullableSQLTime(quotaResetAt),
				"active_requests":    activeRequests,
				"account_created_at": accountCreatedAt.UTC().Format(time.RFC3339),
			},
		})
	}
	return items, rows.Err()
}
