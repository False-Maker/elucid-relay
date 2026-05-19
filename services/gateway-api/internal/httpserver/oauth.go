package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

const oauthTokenRefreshSkew = 10 * time.Minute

type tokenBundle struct {
	Type         string         `json:"type"`
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token,omitempty"`
	ExpiresAt    string         `json:"expires_at,omitempty"`
	Scopes       []string       `json:"scopes,omitempty"`
	Provider     string         `json:"provider,omitempty"`
	AuthScheme   string         `json:"auth_scheme,omitempty"`
	Subject      string         `json:"subject,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

func normalizedTokenBundleFromSecret(secret string) (tokenBundle, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return tokenBundle{}, upstreamUnavailable("missing_upstream_credential", "Upstream credential is missing.")
	}
	var bundle tokenBundle
	if strings.HasPrefix(secret, "{") {
		if err := json.Unmarshal([]byte(secret), &bundle); err != nil {
			return tokenBundle{}, upstreamUnavailable("invalid_token_bundle", "Upstream credential bundle is invalid.")
		}
	} else {
		bundle = tokenBundle{Type: "api_key", AccessToken: secret}
	}
	bundle.Type = strings.TrimSpace(bundle.Type)
	if bundle.Type == "" {
		bundle.Type = "oauth"
	}
	bundle.AccessToken = strings.TrimSpace(bundle.AccessToken)
	bundle.RefreshToken = strings.TrimSpace(bundle.RefreshToken)
	bundle.ExpiresAt = strings.TrimSpace(bundle.ExpiresAt)
	bundle.Provider = strings.TrimSpace(bundle.Provider)
	bundle.AuthScheme = normalizeAuthScheme(bundle.AuthScheme, bundle.Type)
	bundle.Subject = strings.TrimSpace(bundle.Subject)
	bundle.Scopes = normalizeStringList(bundle.Scopes)
	if bundle.AccessToken == "" {
		return tokenBundle{}, upstreamUnavailable("missing_upstream_credential", "Upstream access token is missing.")
	}
	return bundle, nil
}

func (bundle tokenBundle) authSecret() string {
	return bundle.AccessToken
}

func (bundle tokenBundle) hasAccessToken() bool {
	return strings.TrimSpace(bundle.AccessToken) != ""
}

func (bundle tokenBundle) expiresTime() (sql.NullTime, error) {
	if strings.TrimSpace(bundle.ExpiresAt) == "" {
		return sql.NullTime{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, bundle.ExpiresAt)
	if err != nil {
		return sql.NullTime{}, badRequest("expires_at must be RFC3339.")
	}
	return sql.NullTime{Time: parsed, Valid: true}, nil
}

func encodeTokenBundle(bundle tokenBundle) (string, error) {
	if strings.TrimSpace(bundle.Type) == "" {
		bundle.Type = "oauth"
	}
	bundle.AccessToken = strings.TrimSpace(bundle.AccessToken)
	if bundle.AccessToken == "" {
		return "", badRequest("access_token is required.")
	}
	bundle.Scopes = normalizeStringList(bundle.Scopes)
	bundle.AuthScheme = normalizeAuthScheme(bundle.AuthScheme, bundle.Type)
	body, err := json.Marshal(bundle)
	if err != nil {
		return "", badRequest("Invalid token bundle.")
	}
	return string(body), nil
}

func normalizeAuthScheme(authScheme string, bundleType string) string {
	authScheme = strings.ToLower(strings.TrimSpace(authScheme))
	if authScheme != "" {
		return authScheme
	}
	if strings.EqualFold(strings.TrimSpace(bundleType), "api_key") {
		return "api_key"
	}
	return "bearer"
}

func bundleFromAPIKey(apiKey string) (tokenBundle, error) {
	return normalizedTokenBundleFromSecret(apiKey)
}

func parseAccountSecret(apiKey string, bundle tokenBundle) (tokenBundle, bool, error) {
	if strings.TrimSpace(apiKey) != "" {
		parsed, err := bundleFromAPIKey(apiKey)
		return parsed, parsed.Type != "api_key", err
	}
	if bundle.hasAccessToken() {
		if strings.TrimSpace(bundle.Type) == "" {
			bundle.Type = "oauth"
		}
		return bundle, true, nil
	}
	return tokenBundle{}, false, badRequest("api_key or token_bundle.access_token is required.")
}

func validateAuthStatus(status string) (string, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		return "active", nil
	}
	if status != "pending" &&
		status != "active" &&
		status != "refresh_due" &&
		status != "reauth_required" &&
		status != "revoked" &&
		status != "failed" &&
		status != "disabled" {
		return "", badRequest("Invalid auth_status.")
	}
	return status, nil
}

func refreshDueFromExpires(expires sql.NullTime) sql.NullTime {
	if !expires.Valid {
		return sql.NullTime{}
	}
	due := expires.Time.UTC().Add(-oauthTokenRefreshSkew)
	if due.Before(time.Now().UTC()) {
		due = time.Now().UTC()
	}
	return sql.NullTime{Time: due, Valid: true}
}

func oauthWrapperMode(authMode string) string {
	mode := strings.TrimSpace(authMode)
	if mode == "" {
		return "oauth"
	}
	return mode
}

func (s *Server) requireOAuthWrapper(r *http.Request) error {
	expected := strings.TrimSpace(s.cfg.OAuthWrapperToken)
	if expected == "" {
		return forbidden("OAuth wrapper token is not configured.")
	}
	token := security.NormalizeBearer(r.Header.Get("Authorization"))
	if token == "" || token != expected {
		return unauthorized("OAuth wrapper token is required.")
	}
	return nil
}

func (s *Server) storeCredentialBundle(ctx context.Context, exec queryExecutor, bundle tokenBundle) (string, error) {
	encoded, err := encodeTokenBundle(bundle)
	if err != nil {
		return "", err
	}
	ciphertext, nonce, err := security.EncryptSecret(s.cfg.VaultKey, encoded)
	if err != nil {
		return "", err
	}
	var vaultID string
	if err := exec.QueryRowContext(ctx, `
		INSERT INTO credential_vault_records (secret_ciphertext, secret_nonce)
		VALUES ($1, $2)
		RETURNING id::text
	`, ciphertext, nonce).Scan(&vaultID); err != nil {
		return "", err
	}
	return vaultID, nil
}

type queryExecutor interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Server) upsertAccountAuthState(ctx context.Context, exec sqlExecutor, accountID string, authMode string, status string, bundle tokenBundle, lastError string) error {
	if strings.TrimSpace(status) == "" {
		status = "active"
	}
	expiresAt, err := bundle.expiresTime()
	if err != nil {
		return err
	}
	refreshDueAt := refreshDueFromExpires(expiresAt)
	scopes, err := encodeJSON(bundle.Scopes)
	if err != nil {
		return err
	}
	metadata, err := encodeJSON(bundle.Metadata)
	if err != nil {
		return err
	}
	var expires any
	if expiresAt.Valid {
		expires = expiresAt.Time
	}
	var refreshDue any
	if refreshDueAt.Valid {
		refreshDue = refreshDueAt.Time
	}
	var lastRefresh any
	if status == "active" || status == "refresh_due" {
		lastRefresh = time.Now().UTC()
	}
	_, err = exec.ExecContext(ctx, `
		INSERT INTO account_auth_states (
			account_id, auth_mode, auth_status, provider_subject, scopes_json,
			expires_at, refresh_due_at, last_refresh_at, last_error, metadata_json
		)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10::jsonb)
		ON CONFLICT (account_id) DO UPDATE SET
			auth_mode = EXCLUDED.auth_mode,
			auth_status = EXCLUDED.auth_status,
			provider_subject = EXCLUDED.provider_subject,
			scopes_json = EXCLUDED.scopes_json,
			expires_at = EXCLUDED.expires_at,
			refresh_due_at = EXCLUDED.refresh_due_at,
			last_refresh_at = COALESCE(EXCLUDED.last_refresh_at, account_auth_states.last_refresh_at),
			last_error = EXCLUDED.last_error,
			metadata_json = EXCLUDED.metadata_json
	`, accountID, oauthWrapperMode(authMode), status, bundle.Subject, scopes, expires, refreshDue, lastRefresh, truncateForStorage(lastError, 1000), metadata)
	return err
}

func (s *Server) upsertPendingAccountAuthState(ctx context.Context, exec sqlExecutor, accountID string, authMode string, status string, lastError string) error {
	status, err := validateAuthStatus(status)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `
		INSERT INTO account_auth_states (
			account_id, auth_mode, auth_status, provider_subject, scopes_json,
			expires_at, refresh_due_at, last_refresh_at, last_error, metadata_json
		)
		VALUES ($1, $2, $3, '', '[]'::jsonb, NULL, NULL, NULL, $4, '{}'::jsonb)
		ON CONFLICT (account_id) DO UPDATE SET
			auth_mode = EXCLUDED.auth_mode,
			auth_status = EXCLUDED.auth_status,
			provider_subject = EXCLUDED.provider_subject,
			scopes_json = EXCLUDED.scopes_json,
			expires_at = EXCLUDED.expires_at,
			refresh_due_at = EXCLUDED.refresh_due_at,
			last_refresh_at = EXCLUDED.last_refresh_at,
			last_error = EXCLUDED.last_error,
			metadata_json = EXCLUDED.metadata_json
	`, accountID, oauthWrapperMode(authMode), status, truncateForStorage(lastError, 1000))
	return err
}

func authStatusAllowsRouting(status string, expiresAt sql.NullTime, refreshDueAt sql.NullTime, now time.Time) bool {
	switch status {
	case "", "active", "refresh_due":
	default:
		return false
	}
	if expiresAt.Valid && !expiresAt.Time.After(now) {
		return false
	}
	return true
}

func routingModeValue(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "pool", nil
	}
	if value != "pool" && value != "byo" {
		return "", badRequest("routing_mode must be pool or byo.")
	}
	return value, nil
}

func requireBYOOwner(routingMode string, ownerUserID string) error {
	if routingMode == "byo" && strings.TrimSpace(ownerUserID) == "" {
		return badRequest("owner_user_id is required for BYO accounts.")
	}
	if routingMode == "pool" && strings.TrimSpace(ownerUserID) != "" {
		return badRequest("pool accounts cannot have owner_user_id.")
	}
	return nil
}

func queueJobTypeForStatus(status string) string {
	switch status {
	case "revoked":
		return "revoke"
	case "reauth_required":
		return "reauth"
	case "refresh_due":
		return "refresh"
	default:
		return "onboarding"
	}
}

func oauthJobResponse(
	id string,
	accountID string,
	providerClientID string,
	requestedByUserID string,
	jobType string,
	authMode string,
	status string,
	priority int,
	idempotencyKey string,
	leaseOwner string,
	leasedUntil sql.NullTime,
	attemptCount int,
	maxAttempts int,
	payload string,
	result string,
	lastError string,
	createdAt time.Time,
	updatedAt time.Time,
	completedAt sql.NullTime,
) map[string]any {
	return map[string]any{
		"id":                   id,
		"account_id":           accountID,
		"provider_client_id":   providerClientID,
		"requested_by_user_id": requestedByUserID,
		"job_type":             jobType,
		"auth_mode":            authMode,
		"status":               status,
		"priority":             priority,
		"idempotency_key":      idempotencyKey,
		"lease_owner":          leaseOwner,
		"leased_until":         nullableSQLTime(leasedUntil),
		"attempt_count":        attemptCount,
		"max_attempts":         maxAttempts,
		"payload":              oauthPayloadForResponse(payload),
		"result":               jsonRaw(result),
		"oauth_progress":       oauthProgressFromResult(result),
		"last_error":           lastError,
		"created_at":           createdAt.UTC().Format(time.RFC3339),
		"updated_at":           updatedAt.UTC().Format(time.RFC3339),
		"completed_at":         nullableSQLTime(completedAt),
	}
}

func oauthProgressFromResult(result string) json.RawMessage {
	if strings.TrimSpace(result) == "" {
		return json.RawMessage(`{}`)
	}
	var payload struct {
		OAuthProgress json.RawMessage `json:"oauth_progress"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil || len(payload.OAuthProgress) == 0 {
		return json.RawMessage(`{}`)
	}
	return payload.OAuthProgress
}

func oauthPayloadForResponse(payload string) json.RawMessage {
	if strings.TrimSpace(payload) == "" {
		return json.RawMessage(`{}`)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return jsonRaw(payload)
	}
	if manual, ok := value["manual_input"].(map[string]any); ok {
		redacted := map[string]any{}
		for key, item := range manual {
			redacted[key] = item
		}
		if _, ok := redacted["authorization_code"]; ok {
			redacted["authorization_code"] = "submitted"
		}
		if _, ok := redacted["code"]; ok {
			redacted["code"] = "submitted"
		}
		value["manual_input"] = redacted
	}
	body, err := json.Marshal(value)
	if err != nil {
		return jsonRaw(payload)
	}
	return json.RawMessage(body)
}

func (s *Server) adminOAuthJobs(w http.ResponseWriter, r *http.Request, auth authContext) error {
	limit := limitFromRequest(r, 100, 500)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, account_id::text, COALESCE(provider_client_id::text, ''), COALESCE(requested_by_user_id::text, ''),
		       job_type, auth_mode, status, priority, idempotency_key, lease_owner, leased_until,
		       attempt_count, max_attempts, payload_json::text, result_json::text, last_error,
		       created_at, updated_at, completed_at
		FROM oauth_jobs
		WHERE ($1 = '' OR status = $1)
		  AND ($2 = '' OR account_id::text = $2)
		ORDER BY created_at DESC
		LIMIT $3
	`, status, accountID, limit)
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, rowAccountID, providerClientID, requestedByUserID, jobType, authMode, jobStatus, idempotencyKey, leaseOwner, payload, result, lastError string
		var priority, attemptCount, maxAttempts int
		var leasedUntil, completedAt sql.NullTime
		var createdAt, updatedAt time.Time
		if err := rows.Scan(
			&id, &rowAccountID, &providerClientID, &requestedByUserID, &jobType, &authMode, &jobStatus, &priority, &idempotencyKey, &leaseOwner, &leasedUntil,
			&attemptCount, &maxAttempts, &payload, &result, &lastError, &createdAt, &updatedAt, &completedAt,
		); err != nil {
			return err
		}
		items = append(items, oauthJobResponse(
			id, rowAccountID, providerClientID, requestedByUserID, jobType, authMode, jobStatus, priority, idempotencyKey, leaseOwner,
			leasedUntil, attemptCount, maxAttempts, payload, result, lastError, createdAt, updatedAt, completedAt,
		))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateOAuthJob(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		AccountID        string         `json:"account_id"`
		ProviderClientID string         `json:"provider_client_id"`
		JobType          string         `json:"job_type"`
		AuthMode         string         `json:"auth_mode"`
		Priority         int            `json:"priority"`
		IdempotencyKey   string         `json:"idempotency_key"`
		MaxAttempts      int            `json:"max_attempts"`
		Payload          map[string]any `json:"payload"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		return badRequest("account_id is required.")
	}
	jobType := strings.TrimSpace(req.JobType)
	if jobType == "" {
		jobType = "onboarding"
	}
	if jobType != "onboarding" && jobType != "refresh" && jobType != "revoke" && jobType != "reauth" {
		return badRequest("Invalid OAuth job type.")
	}
	authMode := oauthWrapperMode(req.AuthMode)
	if req.Priority == 0 {
		req.Priority = 100
	}
	if req.MaxAttempts == 0 {
		req.MaxAttempts = 5
	}
	if req.MaxAttempts < 1 {
		return badRequest("max_attempts must be positive.")
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = accountID + ":" + jobType + ":" + authMode
	}
	payload, err := encodeJSON(req.Payload)
	if err != nil {
		return err
	}
	var providerClient any
	if strings.TrimSpace(req.ProviderClientID) != "" {
		providerClient = strings.TrimSpace(req.ProviderClientID)
	}
	var id string
	err = s.db.QueryRowContext(r.Context(), `
		INSERT INTO oauth_jobs (account_id, provider_client_id, requested_by_user_id, job_type, auth_mode, priority, idempotency_key, max_attempts, payload_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
		ON CONFLICT (idempotency_key) DO UPDATE SET
			priority = EXCLUDED.priority,
			payload_json = EXCLUDED.payload_json,
			last_error = '',
			lease_owner = '',
			leased_until = NULL
		RETURNING id::text
	`, accountID, providerClient, auth.UserID, jobType, authMode, req.Priority, idempotencyKey, req.MaxAttempts, payload).Scan(&id)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "oauth_job.create", "oauth_job", id, r, map[string]any{"account_id": accountID, "job_type": jobType})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminPatchOAuthJob(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id := r.PathValue("jobId")
	var req struct {
		Status   *string `json:"status"`
		Priority *int    `json:"priority"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Status != nil {
		status := strings.TrimSpace(*req.Status)
		if status != "queued" && status != "canceled" {
			return badRequest("OAuth jobs can only be re-queued or canceled from admin.")
		}
		if _, err := s.db.ExecContext(r.Context(), `
			UPDATE oauth_jobs
			SET status = $2, lease_owner = '', leased_until = NULL
			WHERE id = $1
		`, id, status); err != nil {
			return err
		}
	}
	if req.Priority != nil {
		if _, err := s.db.ExecContext(r.Context(), "UPDATE oauth_jobs SET priority = $2 WHERE id = $1", id, *req.Priority); err != nil {
			return err
		}
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "oauth_job.update", "oauth_job", id, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) adminAccountAuthStates(w http.ResponseWriter, r *http.Request, auth authContext) error {
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT account_id::text, auth_mode, auth_status, provider_subject, scopes_json::text,
		       expires_at, refresh_due_at, last_refresh_at, last_error, metadata_json::text, created_at, updated_at
		FROM account_auth_states
		WHERE ($1 = '' OR account_id::text = $1)
		ORDER BY updated_at DESC
		LIMIT $2
	`, accountID, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var rowAccountID, authMode, authStatus, providerSubject, scopes, lastError, metadata string
		var expiresAt, refreshDueAt, lastRefreshAt sql.NullTime
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&rowAccountID, &authMode, &authStatus, &providerSubject, &scopes, &expiresAt, &refreshDueAt, &lastRefreshAt, &lastError, &metadata, &createdAt, &updatedAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"account_id":       rowAccountID,
			"auth_mode":        authMode,
			"auth_status":      authStatus,
			"provider_subject": providerSubject,
			"scopes":           jsonArrayRaw(scopes),
			"expires_at":       nullableSQLTime(expiresAt),
			"refresh_due_at":   nullableSQLTime(refreshDueAt),
			"last_refresh_at":  nullableSQLTime(lastRefreshAt),
			"last_error":       lastError,
			"metadata":         jsonRaw(metadata),
			"created_at":       createdAt.UTC().Format(time.RFC3339),
			"updated_at":       updatedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminPatchAccountAuthState(w http.ResponseWriter, r *http.Request, auth authContext) error {
	accountID := r.PathValue("accountId")
	var req struct {
		AuthStatus string `json:"auth_status"`
		LastError  string `json:"last_error"`
		QueueJob   bool   `json:"queue_job"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	status, err := validateAuthStatus(req.AuthStatus)
	if err != nil {
		return err
	}
	var authMode string
	err = s.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(aas.auth_mode, 'oauth')
		FROM accounts a
		LEFT JOIN account_auth_states aas ON aas.account_id = a.id
		WHERE a.id = $1
	`, accountID).Scan(&authMode)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("Account was not found.")
	}
	if err != nil {
		return err
	}
	if err := s.upsertPendingAccountAuthState(r.Context(), s.db, accountID, authMode, status, req.LastError); err != nil {
		return err
	}
	if req.QueueJob {
		jobType := queueJobTypeForStatus(status)
		idempotencyKey := accountID + ":" + jobType + ":" + strconv.FormatInt(time.Now().UTC().Unix(), 10)
		if _, err := s.db.ExecContext(r.Context(), `
			INSERT INTO oauth_jobs (account_id, requested_by_user_id, job_type, auth_mode, idempotency_key)
			SELECT $1, $2, $3, auth_mode, $4
			FROM account_auth_states
			WHERE account_id = $1
		`, accountID, auth.UserID, jobType, idempotencyKey); err != nil {
			return err
		}
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "account_auth_state.update", "account", accountID, r, map[string]any{"auth_status": status})
	writeJSON(w, http.StatusOK, map[string]any{"account_id": accountID, "updated": true}, nil)
	return nil
}

func (s *Server) portalOAuthAccounts(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT a.id::text, a.provider_id::text, COALESCE(a.channel_id::text, ''), a.name, a.status,
		       COALESCE(aas.auth_mode, ''), COALESCE(aas.auth_status, ''), COALESCE(aas.provider_subject, ''),
		       COALESCE(aas.scopes_json::text, '[]'), aas.expires_at, aas.refresh_due_at, aas.last_refresh_at,
		       COALESCE(aas.last_error, ''), a.created_at, a.updated_at,
		       COALESCE(lj.id, ''), COALESCE(lj.job_type, ''), COALESCE(lj.auth_mode, ''), COALESCE(lj.status, ''),
		       COALESCE(lj.result_json::text, '{}'), COALESCE(lj.last_error, ''), lj.updated_at
		FROM accounts a
		LEFT JOIN account_auth_states aas ON aas.account_id = a.id
		LEFT JOIN LATERAL (
			SELECT oj.id::text, oj.job_type, oj.auth_mode, oj.status, oj.result_json, oj.last_error, oj.updated_at
			FROM oauth_jobs oj
			WHERE oj.account_id = a.id
			ORDER BY oj.updated_at DESC, oj.created_at DESC
			LIMIT 1
		) lj ON true
		WHERE a.routing_mode = 'byo'
		  AND a.owner_user_id = $1
		ORDER BY a.created_at DESC
	`, auth.UserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, providerID, channelID, name, status, authMode, authStatus, providerSubject, scopes, lastError, jobID, jobType, jobAuthMode, jobStatus, jobResult, jobLastError string
		var expiresAt, refreshDueAt, lastRefreshAt, jobUpdatedAt sql.NullTime
		var createdAt, updatedAt time.Time
		if err := rows.Scan(
			&id, &providerID, &channelID, &name, &status, &authMode, &authStatus, &providerSubject, &scopes, &expiresAt, &refreshDueAt, &lastRefreshAt, &lastError, &createdAt, &updatedAt,
			&jobID, &jobType, &jobAuthMode, &jobStatus, &jobResult, &jobLastError, &jobUpdatedAt,
		); err != nil {
			return err
		}
		item := map[string]any{
			"id":           id,
			"provider_id":  providerID,
			"channel_id":   channelID,
			"name":         name,
			"status":       status,
			"routing_mode": "byo",
			"auth": map[string]any{
				"auth_mode":        authMode,
				"auth_status":      authStatus,
				"provider_subject": providerSubject,
				"scopes":           jsonArrayRaw(scopes),
				"expires_at":       nullableSQLTime(expiresAt),
				"refresh_due_at":   nullableSQLTime(refreshDueAt),
				"last_refresh_at":  nullableSQLTime(lastRefreshAt),
				"last_error":       lastError,
			},
			"created_at": createdAt.UTC().Format(time.RFC3339),
			"updated_at": updatedAt.UTC().Format(time.RFC3339),
		}
		if jobID != "" {
			item["latest_job"] = map[string]any{
				"id":             jobID,
				"job_type":       jobType,
				"auth_mode":      jobAuthMode,
				"status":         jobStatus,
				"result":         jsonRaw(jobResult),
				"oauth_progress": oauthProgressFromResult(jobResult),
				"last_error":     jobLastError,
				"updated_at":     nullableSQLTime(jobUpdatedAt),
			}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) portalOAuthOptions(w http.ResponseWriter, r *http.Request, auth authContext) error {
	providerRows, err := s.db.QueryContext(r.Context(), `
		SELECT p.id::text, p.name, p.provider_type
		FROM providers p
		WHERE p.status = 'active'
		  AND EXISTS (
		    SELECT 1 FROM channels c
		    WHERE c.provider_id = p.id
		      AND c.status = 'active'
		  )
		ORDER BY p.name ASC
	`)
	if err != nil {
		return err
	}
	defer providerRows.Close()
	providers := []map[string]any{}
	for providerRows.Next() {
		var id, name, providerType string
		if err := providerRows.Scan(&id, &name, &providerType); err != nil {
			return err
		}
		providers = append(providers, map[string]any{"id": id, "name": name, "provider_type": providerType})
	}
	if err := providerRows.Err(); err != nil {
		return err
	}

	channelRows, err := s.db.QueryContext(r.Context(), `
		SELECT c.id::text, c.provider_id::text, c.name,
		       COALESCE(jsonb_agg(
		         jsonb_build_object(
		           'model_name', ca.model_name,
		           'endpoint', ca.endpoint,
		           'upstream_model', COALESCE(NULLIF(ca.upstream_model, ''), ca.model_name)
		         )
		         ORDER BY ca.model_name, ca.endpoint
		       ) FILTER (WHERE ca.id IS NOT NULL), '[]'::jsonb)::text
		FROM channels c
		JOIN providers p ON p.id = c.provider_id AND p.status = 'active'
		LEFT JOIN channel_abilities ca ON ca.channel_id = c.id AND ca.status = 'active'
		WHERE c.status = 'active'
		GROUP BY c.id, c.provider_id, c.name
		ORDER BY c.name ASC
	`)
	if err != nil {
		return err
	}
	defer channelRows.Close()
	channels := []map[string]any{}
	for channelRows.Next() {
		var id, providerID, name, abilities string
		if err := channelRows.Scan(&id, &providerID, &name, &abilities); err != nil {
			return err
		}
		channels = append(channels, map[string]any{
			"id":          id,
			"provider_id": providerID,
			"name":        name,
			"abilities":   jsonArrayRaw(abilities),
		})
	}
	if err := channelRows.Err(); err != nil {
		return err
	}

	clientRows, err := s.db.QueryContext(r.Context(), `
		SELECT pc.id::text, pc.provider_id::text, pc.name, pc.client_type
		FROM provider_clients pc
		JOIN providers p ON p.id = pc.provider_id AND p.status = 'active'
		WHERE pc.status = 'active'
		ORDER BY pc.name ASC
	`)
	if err != nil {
		return err
	}
	defer clientRows.Close()
	clients := []map[string]any{}
	for clientRows.Next() {
		var id, providerID, name, clientType string
		if err := clientRows.Scan(&id, &providerID, &name, &clientType); err != nil {
			return err
		}
		clients = append(clients, map[string]any{
			"id":          id,
			"provider_id": providerID,
			"name":        name,
			"client_type": clientType,
		})
	}
	if err := clientRows.Err(); err != nil {
		return err
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"providers":        providers,
		"channels":         channels,
		"provider_clients": clients,
		"auth_modes":       []string{"codex_cli", "openai_cli", "claude_cli", "google_pkce", "github_device", "oauth", "mock"},
	}, nil)
	return nil
}

func (s *Server) portalCreateOAuthAccount(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		ProviderID       string         `json:"provider_id"`
		ChannelID        string         `json:"channel_id"`
		ProviderClientID string         `json:"provider_client_id"`
		Name             string         `json:"name"`
		AuthMode         string         `json:"auth_mode"`
		TokenBundle      tokenBundle    `json:"token_bundle"`
		Metadata         map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.ProviderID) == "" || strings.TrimSpace(req.Name) == "" {
		return badRequest("provider_id and name are required.")
	}
	authMode := oauthWrapperMode(req.AuthMode)
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.validatePortalOAuthSelection(r.Context(), tx, req.ProviderID, req.ChannelID, req.ProviderClientID); err != nil {
		return err
	}

	var channel any
	if strings.TrimSpace(req.ChannelID) != "" {
		channel = strings.TrimSpace(req.ChannelID)
	}
	var vault any
	if req.TokenBundle.hasAccessToken() {
		vaultID, err := s.storeCredentialBundle(r.Context(), tx, req.TokenBundle)
		if err != nil {
			return err
		}
		vault = vaultID
	}
	var accountID string
	if err := tx.QueryRowContext(r.Context(), `
		INSERT INTO accounts (provider_id, channel_id, owner_user_id, routing_mode, credential_vault_record_id, name, status, metadata_json)
		VALUES ($1, $2, $3, 'byo', $4, $5, 'active', $6::jsonb)
		RETURNING id::text
	`, strings.TrimSpace(req.ProviderID), channel, auth.UserID, vault, strings.TrimSpace(req.Name), metadata).Scan(&accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(r.Context(), "INSERT INTO account_runtime_states (account_id) VALUES ($1)", accountID); err != nil {
		return err
	}
	status := "active"
	if req.TokenBundle.hasAccessToken() {
		if err := s.upsertAccountAuthState(r.Context(), tx, accountID, authMode, status, req.TokenBundle, ""); err != nil {
			return err
		}
	} else {
		status = "pending"
		if err := s.upsertPendingAccountAuthState(r.Context(), tx, accountID, authMode, status, ""); err != nil {
			return err
		}
		var providerClient any
		if strings.TrimSpace(req.ProviderClientID) != "" {
			providerClient = strings.TrimSpace(req.ProviderClientID)
		}
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO oauth_jobs (account_id, provider_client_id, requested_by_user_id, job_type, auth_mode, idempotency_key)
			VALUES ($1, $2, $3, 'onboarding', $4, $5)
			ON CONFLICT (idempotency_key) DO NOTHING
		`, accountID, providerClient, auth.UserID, authMode, accountID+":onboarding:"+authMode); err != nil {
			return err
		}
	}
	audit(r.Context(), tx, auth.UserID, "personal_user", "oauth_account.create", "account", accountID, r, map[string]any{"auth_mode": authMode})
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": accountID, "auth_status": status}, nil)
	return nil
}

func (s *Server) validatePortalOAuthSelection(ctx context.Context, tx *sql.Tx, providerID string, channelID string, providerClientID string) error {
	providerID = strings.TrimSpace(providerID)
	channelID = strings.TrimSpace(channelID)
	providerClientID = strings.TrimSpace(providerClientID)
	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM providers
		  WHERE id = $1
		    AND status = 'active'
		)
	`, providerID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return badRequest("provider_id is invalid or inactive.")
	}
	if channelID != "" {
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM channels
			  WHERE id = $1
			    AND provider_id = $2
			    AND status = 'active'
			)
		`, channelID, providerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return badRequest("channel_id is invalid, inactive, or not owned by provider_id.")
		}
	}
	if providerClientID != "" {
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM provider_clients
			  WHERE id = $1
			    AND provider_id = $2
			    AND status = 'active'
			)
		`, providerClientID, providerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return badRequest("provider_client_id is invalid, inactive, or not owned by provider_id.")
		}
	}
	return nil
}

func (s *Server) portalReauthOAuthAccount(w http.ResponseWriter, r *http.Request, auth authContext) error {
	accountID := r.PathValue("accountId")
	var req struct {
		ProviderClientID string         `json:"provider_client_id"`
		AuthMode         string         `json:"auth_mode"`
		Payload          map[string]any `json:"payload"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentAuthMode string
	err = tx.QueryRowContext(r.Context(), `
		SELECT COALESCE(aas.auth_mode, '')
		FROM accounts a
		LEFT JOIN account_auth_states aas ON aas.account_id = a.id
		WHERE a.id = $1
		  AND a.routing_mode = 'byo'
		  AND a.owner_user_id = $2
		FOR UPDATE OF a
	`, accountID, auth.UserID).Scan(&currentAuthMode)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("OAuth account was not found.")
	}
	if err != nil {
		return err
	}
	authMode := oauthWrapperMode(req.AuthMode)
	if strings.TrimSpace(req.AuthMode) == "" && currentAuthMode != "" {
		authMode = currentAuthMode
	}
	payload, err := encodeJSON(req.Payload)
	if err != nil {
		return err
	}
	var providerClient any
	if strings.TrimSpace(req.ProviderClientID) != "" {
		providerClient = strings.TrimSpace(req.ProviderClientID)
	}
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE account_auth_states
		SET auth_status = 'reauth_required',
		    last_error = ''
		WHERE account_id = $1
	`, accountID); err != nil {
		return err
	}
	var jobID string
	if err := tx.QueryRowContext(r.Context(), `
		INSERT INTO oauth_jobs (account_id, provider_client_id, requested_by_user_id, job_type, auth_mode, idempotency_key, payload_json)
		VALUES ($1, $2, $3, 'reauth', $4, $5, $6::jsonb)
		ON CONFLICT (idempotency_key) DO UPDATE SET
			status = 'queued',
			lease_owner = '',
			leased_until = NULL,
			payload_json = EXCLUDED.payload_json,
			last_error = ''
		RETURNING id::text
	`, accountID, providerClient, auth.UserID, authMode, accountID+":reauth:"+authMode, payload).Scan(&jobID); err != nil {
		return err
	}
	audit(r.Context(), tx, auth.UserID, "personal_user", "oauth_account.reauth", "account", accountID, r, map[string]any{"job_id": jobID})
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": jobID, "account_id": accountID, "queued": true}, nil)
	return nil
}

func (s *Server) portalRevokeOAuthAccount(w http.ResponseWriter, r *http.Request, auth authContext) error {
	accountID := r.PathValue("accountId")
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var authMode string
	err = tx.QueryRowContext(r.Context(), `
		SELECT COALESCE(aas.auth_mode, 'oauth')
		FROM accounts a
		LEFT JOIN account_auth_states aas ON aas.account_id = a.id
		WHERE a.id = $1
		  AND a.routing_mode = 'byo'
		  AND a.owner_user_id = $2
		FOR UPDATE OF a
	`, accountID, auth.UserID).Scan(&authMode)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("OAuth account was not found.")
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET status = 'disabled' WHERE id = $1", accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE account_auth_states
		SET auth_status = 'revoked',
		    last_error = ''
		WHERE account_id = $1
	`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO oauth_jobs (account_id, requested_by_user_id, job_type, auth_mode, idempotency_key)
		VALUES ($1, $2, 'revoke', $3, $4)
		ON CONFLICT (idempotency_key) DO NOTHING
	`, accountID, auth.UserID, oauthWrapperMode(authMode), accountID+":revoke:"+oauthWrapperMode(authMode)); err != nil {
		return err
	}
	audit(r.Context(), tx, auth.UserID, "personal_user", "oauth_account.revoke", "account", accountID, r, nil)
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": accountID, "revoked": true}, nil)
	return nil
}

func (s *Server) portalSubmitOAuthJobInput(w http.ResponseWriter, r *http.Request, auth authContext) error {
	jobID := r.PathValue("jobId")
	input, err := oauthManualInputFromRequest(r)
	if err != nil {
		return err
	}
	encoded, err := encodeJSON(input)
	if err != nil {
		return err
	}
	var accountID string
	err = s.db.QueryRowContext(r.Context(), `
		UPDATE oauth_jobs j
		SET payload_json = jsonb_set(COALESCE(j.payload_json, '{}'::jsonb), '{manual_input}', $2::jsonb, true),
		    updated_at = now()
		FROM accounts a
		WHERE j.id = $1
		  AND j.account_id = a.id
		  AND a.routing_mode = 'byo'
		  AND a.owner_user_id = $3
		  AND j.status IN ('queued', 'leased')
		RETURNING j.account_id::text
	`, jobID, encoded, auth.UserID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("OAuth job was not found or is not accepting input.")
	}
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "personal_user", "oauth_job.input", "oauth_job", jobID, r, map[string]any{"account_id": accountID})
	writeJSON(w, http.StatusOK, map[string]any{"id": jobID, "submitted": true}, nil)
	return nil
}

func (s *Server) adminSubmitOAuthJobInput(w http.ResponseWriter, r *http.Request, auth authContext) error {
	jobID := r.PathValue("jobId")
	input, err := oauthManualInputFromRequest(r)
	if err != nil {
		return err
	}
	encoded, err := encodeJSON(input)
	if err != nil {
		return err
	}
	var accountID string
	err = s.db.QueryRowContext(r.Context(), `
		UPDATE oauth_jobs
		SET payload_json = jsonb_set(COALESCE(payload_json, '{}'::jsonb), '{manual_input}', $2::jsonb, true),
		    updated_at = now()
		WHERE id = $1
		  AND status IN ('queued', 'leased')
		RETURNING account_id::text
	`, jobID, encoded).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("OAuth job was not found or is not accepting input.")
	}
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "oauth_job.input", "oauth_job", jobID, r, map[string]any{"account_id": accountID})
	writeJSON(w, http.StatusOK, map[string]any{"id": jobID, "submitted": true}, nil)
	return nil
}

func oauthManualInputFromRequest(r *http.Request) (map[string]any, error) {
	var req struct {
		AuthorizationCode string         `json:"authorization_code"`
		Code              string         `json:"code"`
		State             string         `json:"state"`
		Input             map[string]any `json:"input"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return nil, err
	}
	code := strings.TrimSpace(req.AuthorizationCode)
	if code == "" {
		code = strings.TrimSpace(req.Code)
	}
	if code == "" {
		code = strings.TrimSpace(stringFromMap(req.Input, "authorization_code"))
	}
	if code == "" {
		code = strings.TrimSpace(stringFromMap(req.Input, "code"))
	}
	if code == "" {
		return nil, badRequest("authorization_code is required.")
	}
	input := map[string]any{
		"authorization_code": code,
		"submitted_at":       time.Now().UTC().Format(time.RFC3339),
	}
	if state := strings.TrimSpace(req.State); state != "" {
		input["state"] = state
	} else if state := strings.TrimSpace(stringFromMap(req.Input, "state")); state != "" {
		input["state"] = state
	}
	return input, nil
}

func stringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	switch value := values[key].(type) {
	case string:
		return value
	default:
		return ""
	}
}

func (s *Server) oauthJobs(w http.ResponseWriter, r *http.Request) {
	if err := s.requireOAuthWrapper(r); err != nil {
		writeError(w, r, err)
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.oauthClaimJob(w, r)
	default:
		writeError(w, r, notFound("OAuth job endpoint was not found."))
	}
}

func (s *Server) oauthClaimJob(w http.ResponseWriter, r *http.Request) {
	if err := s.requireOAuthWrapper(r); err != nil {
		writeError(w, r, err)
		return
	}
	var req struct {
		LeaseOwner     string   `json:"lease_owner"`
		LeaseSeconds   int      `json:"lease_seconds"`
		ProviderName   string   `json:"provider_name"`
		ProviderType   string   `json:"provider_type"`
		AuthMode       string   `json:"auth_mode"`
		SupportedModes []string `json:"supported_modes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	leaseOwner := strings.TrimSpace(req.LeaseOwner)
	if leaseOwner == "" {
		writeError(w, r, badRequest("lease_owner is required."))
		return
	}
	leaseSeconds := req.LeaseSeconds
	if leaseSeconds <= 0 {
		leaseSeconds = 300
	}
	if leaseSeconds > 1800 {
		leaseSeconds = 1800
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer tx.Rollback()

	supportedModes := map[string]bool{}
	for _, mode := range normalizeStringList(req.SupportedModes) {
		supportedModes[mode] = true
	}

	rows, err := tx.QueryContext(r.Context(), `
		SELECT j.id::text, j.account_id::text, p.id::text, p.name, p.provider_type,
		       j.job_type, j.auth_mode, j.status, j.idempotency_key, j.payload_json::text,
		       COALESCE(pc.id::text, ''), COALESCE(pc.client_type, ''), COALESCE(pc.metadata_json::text, '{}'),
		       av.secret_ciphertext, av.secret_nonce, pcv.secret_ciphertext, pcv.secret_nonce,
		       j.attempt_count + 1, j.max_attempts, now() + make_interval(secs => $1::int),
		       j.created_at, j.updated_at
		FROM oauth_jobs j
		JOIN accounts a ON a.id = j.account_id
		JOIN providers p ON p.id = a.provider_id
		LEFT JOIN provider_clients pc ON pc.id = j.provider_client_id
		LEFT JOIN credential_vault_records av ON av.id = a.credential_vault_record_id
		LEFT JOIN credential_vault_records pcv ON pcv.id = pc.credential_vault_record_id
		WHERE (
			j.status = 'queued'
			OR (j.status = 'leased' AND j.leased_until < now())
		)
		  AND j.attempt_count < j.max_attempts
		  AND ($2 = '' OR p.name = $2)
		  AND ($3 = '' OR p.provider_type = $3)
		  AND ($4 = '' OR j.auth_mode = $4)
		ORDER BY j.priority ASC, j.created_at ASC
		FOR UPDATE OF j SKIP LOCKED
		LIMIT 50
	`, leaseSeconds, strings.TrimSpace(req.ProviderName), strings.TrimSpace(req.ProviderType), strings.TrimSpace(req.AuthMode))
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer rows.Close()

	var (
		id, accountID, providerID, providerName, providerType, jobType, authMode, status, idempotencyKey, payload, clientID, clientType, clientMetadata string
		accountCiphertext, accountNonce, clientCiphertext, clientNonce                                                                                  []byte
		attemptCount, maxAttempts                                                                                                                       int
		leasedUntil                                                                                                                                     sql.NullTime
		createdAt, updatedAt                                                                                                                            time.Time
		found                                                                                                                                           bool
	)
	for rows.Next() {
		if err := rows.Scan(
			&id, &accountID, &providerID, &providerName, &providerType, &jobType, &authMode, &status, &idempotencyKey, &payload,
			&clientID, &clientType, &clientMetadata, &accountCiphertext, &accountNonce, &clientCiphertext, &clientNonce,
			&attemptCount, &maxAttempts, &leasedUntil, &createdAt, &updatedAt,
		); err != nil {
			writeError(w, r, err)
			return
		}
		if len(supportedModes) > 0 && !supportedModes[strings.TrimSpace(authMode)] {
			continue
		}
		found = true
		break
	}
	if err := rows.Err(); err != nil {
		writeError(w, r, err)
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, r, err)
		return
	}
	if found {
		if _, err := tx.ExecContext(r.Context(), `
		UPDATE oauth_jobs
		SET status = 'leased',
		    lease_owner = $2,
		    leased_until = $3,
		    attempt_count = $4
		WHERE id = $1
	`, id, leaseOwner, leasedUntil.Time, attemptCount); err != nil {
			writeError(w, r, err)
			return
		}
		status = "leased"
	}
	if !found {
		if commitErr := tx.Commit(); commitErr != nil {
			writeError(w, r, commitErr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"job": nil}, nil)
		return
	}
	var currentBundle any
	if len(accountCiphertext) > 0 && len(accountNonce) > 0 {
		secret, err := security.DecryptSecret(s.cfg.VaultKey, accountCiphertext, accountNonce)
		if err != nil {
			writeError(w, r, err)
			return
		}
		bundle, err := normalizedTokenBundleFromSecret(secret)
		if err != nil {
			writeError(w, r, err)
			return
		}
		currentBundle = bundle
	}
	clientCredential := ""
	if len(clientCiphertext) > 0 && len(clientNonce) > 0 {
		secret, err := security.DecryptSecret(s.cfg.VaultKey, clientCiphertext, clientNonce)
		if err != nil {
			writeError(w, r, err)
			return
		}
		clientCredential = secret
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"job": map[string]any{
			"id":              id,
			"account_id":      accountID,
			"provider":        map[string]any{"id": providerID, "name": providerName, "provider_type": providerType},
			"provider_client": map[string]any{"id": clientID, "client_type": clientType, "credential": clientCredential, "metadata": jsonRaw(clientMetadata)},
			"job_type":        jobType,
			"auth_mode":       authMode,
			"status":          status,
			"idempotency_key": idempotencyKey,
			"payload":         jsonRaw(payload),
			"token_bundle":    currentBundle,
			"attempt_count":   attemptCount,
			"max_attempts":    maxAttempts,
			"leased_until":    nullableSQLTime(leasedUntil),
			"created_at":      createdAt.UTC().Format(time.RFC3339),
			"updated_at":      updatedAt.UTC().Format(time.RFC3339),
		},
	}, nil)
}

func (s *Server) oauthJobProgress(w http.ResponseWriter, r *http.Request) {
	if err := s.requireOAuthWrapper(r); err != nil {
		writeError(w, r, err)
		return
	}
	jobID := r.PathValue("jobId")
	var req struct {
		LeaseOwner string         `json:"lease_owner"`
		Progress   map[string]any `json:"progress"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	leaseOwner := strings.TrimSpace(req.LeaseOwner)
	if leaseOwner == "" {
		writeError(w, r, badRequest("lease_owner is required."))
		return
	}
	if req.Progress == nil {
		writeError(w, r, badRequest("progress is required."))
		return
	}
	if _, ok := req.Progress["updated_at"]; !ok {
		req.Progress["updated_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	progress, err := encodeJSON(req.Progress)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var accountID string
	err = s.db.QueryRowContext(r.Context(), `
		UPDATE oauth_jobs
		SET result_json = jsonb_set(COALESCE(result_json, '{}'::jsonb), '{oauth_progress}', $3::jsonb, true),
		    updated_at = now()
		WHERE id = $1
		  AND status = 'leased'
		  AND lease_owner = $2
		  AND leased_until > now()
		RETURNING account_id::text
	`, jobID, leaseOwner, progress).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, conflict("OAuth job lease is not active."))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": jobID, "account_id": accountID, "updated": true}, nil)
}

func (s *Server) oauthJobInput(w http.ResponseWriter, r *http.Request) {
	if err := s.requireOAuthWrapper(r); err != nil {
		writeError(w, r, err)
		return
	}
	jobID := r.PathValue("jobId")
	leaseOwner := strings.TrimSpace(r.URL.Query().Get("lease_owner"))
	if leaseOwner == "" {
		writeError(w, r, badRequest("lease_owner is required."))
		return
	}
	var input string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(payload_json->'manual_input', '{}'::jsonb)::text
		FROM oauth_jobs
		WHERE id = $1
		  AND status = 'leased'
		  AND lease_owner = $2
		  AND leased_until > now()
	`, jobID, leaseOwner).Scan(&input)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, conflict("OAuth job lease is not active."))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": jobID, "input": jsonRaw(input)}, nil)
}

func (s *Server) oauthJobComplete(w http.ResponseWriter, r *http.Request) {
	if err := s.requireOAuthWrapper(r); err != nil {
		writeError(w, r, err)
		return
	}
	jobID := r.PathValue("jobId")
	var req struct {
		LeaseOwner      string         `json:"lease_owner"`
		TokenBundle     tokenBundle    `json:"token_bundle"`
		ProviderSubject string         `json:"provider_subject"`
		Scopes          []string       `json:"scopes"`
		AuthStatus      string         `json:"auth_status"`
		Result          map[string]any `json:"result"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	leaseOwner := strings.TrimSpace(req.LeaseOwner)
	if leaseOwner == "" {
		writeError(w, r, badRequest("lease_owner is required."))
		return
	}
	bundle := req.TokenBundle
	if strings.TrimSpace(req.ProviderSubject) != "" {
		bundle.Subject = strings.TrimSpace(req.ProviderSubject)
	}
	if len(req.Scopes) > 0 {
		bundle.Scopes = req.Scopes
	}
	status := strings.TrimSpace(req.AuthStatus)
	if status == "" {
		status = "active"
	}
	if _, err := validateAuthStatus(status); err != nil {
		writeError(w, r, badRequest("Invalid auth_status."))
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer tx.Rollback()

	var accountID, authMode, jobType string
	err = tx.QueryRowContext(r.Context(), `
		SELECT account_id::text, auth_mode, job_type
		FROM oauth_jobs
		WHERE id = $1
		  AND status = 'leased'
		  AND lease_owner = $2
		  AND leased_until > now()
		FOR UPDATE
	`, jobID, leaseOwner).Scan(&accountID, &authMode, &jobType)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, conflict("OAuth job lease is not active."))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	if jobType == "revoke" && strings.TrimSpace(req.AuthStatus) == "" {
		status = "revoked"
	}
	if bundle.hasAccessToken() {
		vaultID, err := s.storeCredentialBundle(r.Context(), tx, bundle)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET credential_vault_record_id = $2 WHERE id = $1", accountID, vaultID); err != nil {
			writeError(w, r, err)
			return
		}
		if err := s.upsertAccountAuthState(r.Context(), tx, accountID, authMode, status, bundle, ""); err != nil {
			writeError(w, r, err)
			return
		}
	} else {
		if status == "active" || status == "refresh_due" {
			writeError(w, r, badRequest("token_bundle.access_token is required for active OAuth job completion."))
			return
		}
		if err := s.upsertPendingAccountAuthState(r.Context(), tx, accountID, authMode, status, ""); err != nil {
			writeError(w, r, err)
			return
		}
	}
	result, err := encodeJSON(req.Result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE oauth_jobs
		SET status = 'succeeded',
		    result_json = COALESCE(result_json, '{}'::jsonb) || $2::jsonb,
		    last_error = '',
		    completed_at = now(),
		    lease_owner = '',
		    leased_until = NULL
		WHERE id = $1
	`, jobID, result); err != nil {
		writeError(w, r, err)
		return
	}
	if jobType == "revoke" {
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET status = 'disabled' WHERE id = $1", accountID); err != nil {
			writeError(w, r, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": jobID, "completed": true, "account_id": accountID}, nil)
}

func (s *Server) oauthJobFail(w http.ResponseWriter, r *http.Request) {
	if err := s.requireOAuthWrapper(r); err != nil {
		writeError(w, r, err)
		return
	}
	jobID := r.PathValue("jobId")
	var req struct {
		LeaseOwner string `json:"lease_owner"`
		Error      string `json:"error"`
		Terminal   bool   `json:"terminal"`
		AuthStatus string `json:"auth_status"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.LeaseOwner) == "" {
		writeError(w, r, badRequest("lease_owner is required."))
		return
	}
	errText := truncateForStorage(req.Error, 1000)
	if errText == "" {
		errText = "OAuth job failed."
	}
	nextStatus := "queued"
	completed := any(nil)
	if req.Terminal {
		nextStatus = "failed"
		completed = time.Now().UTC()
	}
	authStatus := strings.TrimSpace(req.AuthStatus)
	if authStatus == "" {
		authStatus = "reauth_required"
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer tx.Rollback()
	var accountID, authMode, finalStatus string
	err = tx.QueryRowContext(r.Context(), `
		UPDATE oauth_jobs
		SET status = CASE
		      WHEN $4::boolean OR attempt_count >= max_attempts THEN 'failed'
		      ELSE $3
		    END,
		    last_error = $5,
		    completed_at = CASE
		      WHEN $4::boolean OR attempt_count >= max_attempts THEN now()
		      ELSE $6::timestamptz
		    END,
		    lease_owner = '',
		    leased_until = NULL
		WHERE id = $1
		  AND status = 'leased'
		  AND lease_owner = $2
		RETURNING account_id::text, auth_mode, status
	`, jobID, strings.TrimSpace(req.LeaseOwner), nextStatus, req.Terminal, errText, completed).Scan(&accountID, &authMode, &finalStatus)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, conflict("OAuth job lease is not active."))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.upsertPendingAccountAuthState(r.Context(), tx, accountID, authMode, authStatus, errText); err != nil {
		writeError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, r, err)
		return
	}
	if finalStatus == "failed" {
		_ = s.emitNotification(r.Context(), notificationEventInput{
			EventType:  "oauth_job_failed",
			Severity:   "critical",
			Title:      "OAuth job failed",
			Message:    errText,
			TargetType: "oauth_job",
			TargetID:   jobID,
			Payload: map[string]any{
				"account_id":  accountID,
				"auth_mode":   authMode,
				"auth_status": authStatus,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": jobID, "failed": true, "terminal": req.Terminal}, nil)
}
