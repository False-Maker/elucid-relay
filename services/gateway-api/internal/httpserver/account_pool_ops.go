package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

type accountQuotaTarget struct {
	AccountID       string
	AccountName     string
	ProviderID      string
	ProviderType    string
	ChannelID       string
	BaseURL         string
	ProxyURL        string
	AccountMetadata map[string]any
	ChannelMetadata map[string]any
	ProviderMeta    map[string]any
	APIKey          string
	AuthScheme      string
}

type quotaReading struct {
	Status       string
	Source       string
	WindowType   string
	Remaining    string
	Limit        string
	ResetAt      *time.Time
	ErrorMessage string
	Raw          map[string]any
}

func (s *Server) adminQuotaSnapshots(w http.ResponseWriter, r *http.Request, auth authContext) error {
	args := []any{limitFromRequest(r, 100, 500)}
	filters := []string{}
	if accountID := strings.TrimSpace(r.URL.Query().Get("account_id")); accountID != "" {
		args = append(args, accountID)
		filters = append(filters, "aqs.account_id::text = $"+strconv.Itoa(len(args)))
	}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		args = append(args, status)
		filters = append(filters, "aqs.status = $"+strconv.Itoa(len(args)))
	}
	where := ""
	if len(filters) > 0 {
		where = "WHERE " + strings.Join(filters, " AND ")
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT aqs.id::text, aqs.account_id::text, COALESCE(a.name, ''), COALESCE(aqs.provider_id::text, ''),
		       COALESCE(aqs.channel_id::text, ''), COALESCE(c.name, ''), COALESCE(aqs.refresh_job_id::text, ''),
		       aqs.status, aqs.source, aqs.window_type, COALESCE(aqs.remaining::text, ''),
		       COALESCE(aqs.limit_value::text, ''), aqs.reset_at, aqs.error_message, aqs.raw_json::text, aqs.created_at
		FROM account_quota_snapshots aqs
		LEFT JOIN accounts a ON a.id = aqs.account_id
		LEFT JOIN channels c ON c.id = aqs.channel_id
		`+where+`
		ORDER BY aqs.created_at DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, accountID, accountName, providerID, channelID, channelName, jobID, status, source, windowType, remaining, limitValue, errorMessage, raw string
		var resetAt sql.NullTime
		var createdAt time.Time
		if err := rows.Scan(&id, &accountID, &accountName, &providerID, &channelID, &channelName, &jobID, &status, &source, &windowType, &remaining, &limitValue, &resetAt, &errorMessage, &raw, &createdAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":             id,
			"account_id":     accountID,
			"account_name":   accountName,
			"provider_id":    providerID,
			"channel_id":     channelID,
			"channel_name":   channelName,
			"refresh_job_id": jobID,
			"status":         status,
			"source":         source,
			"window_type":    windowType,
			"remaining":      nullableString(remaining),
			"limit_value":    nullableString(limitValue),
			"reset_at":       nullableSQLTime(resetAt),
			"error_message":  errorMessage,
			"raw":            jsonRaw(raw),
			"created_at":     createdAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminQuotaRefreshJobs(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT qj.id::text, COALESCE(qj.account_id::text, ''), COALESCE(a.name, ''), qj.status, qj.trigger_type,
		       COALESCE(qj.requested_by::text, ''), qj.total_count, qj.success_count, qj.failed_count,
		       qj.unsupported_count, qj.error_message, qj.metadata_json::text, qj.started_at, qj.finished_at, qj.created_at
		FROM account_quota_refresh_jobs qj
		LEFT JOIN accounts a ON a.id = qj.account_id
		ORDER BY qj.created_at DESC
		LIMIT $1
	`, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, accountID, accountName, status, triggerType, requestedBy, errorMessage, metadata string
		var totalCount, successCount, failedCount, unsupportedCount int
		var startedAt, finishedAt sql.NullTime
		var createdAt time.Time
		if err := rows.Scan(&id, &accountID, &accountName, &status, &triggerType, &requestedBy, &totalCount, &successCount, &failedCount, &unsupportedCount, &errorMessage, &metadata, &startedAt, &finishedAt, &createdAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":                id,
			"account_id":        accountID,
			"account_name":      accountName,
			"status":            status,
			"trigger_type":      triggerType,
			"requested_by":      requestedBy,
			"total_count":       totalCount,
			"success_count":     successCount,
			"failed_count":      failedCount,
			"unsupported_count": unsupportedCount,
			"error_message":     errorMessage,
			"metadata":          jsonRaw(metadata),
			"started_at":        nullableSQLTime(startedAt),
			"finished_at":       nullableSQLTime(finishedAt),
			"created_at":        createdAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminRefreshAccountQuota(w http.ResponseWriter, r *http.Request, auth authContext) error {
	accountID := r.PathValue("accountId")
	result, err := s.runQuotaRefresh(r.Context(), []string{accountID}, "manual", auth.UserID)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "account.quota_refresh", "account", accountID, r, result)
	writeJSON(w, http.StatusOK, result, nil)
	return nil
}

func (s *Server) adminRefreshAccountsQuota(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		AccountIDs []string `json:"account_ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	result, err := s.runQuotaRefresh(r.Context(), normalizeStringList(req.AccountIDs), "manual", auth.UserID)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "account.quota_refresh_batch", "account", "", r, result)
	writeJSON(w, http.StatusOK, result, nil)
	return nil
}

func (s *Server) runQuotaRefresh(ctx context.Context, accountIDs []string, triggerType string, requestedBy string) (map[string]any, error) {
	ids := normalizeStringList(accountIDs)
	if len(ids) == 0 {
		var nextIDs []string
		var err error
		if triggerType == "scheduled" {
			nextIDs, err = s.accountsForScheduledQuotaRefresh(ctx, 50)
		} else {
			nextIDs, err = s.accountsForManualQuotaRefresh(ctx, 200)
		}
		if err != nil {
			return nil, err
		}
		ids = nextIDs
	}
	if len(ids) == 0 {
		return map[string]any{"job_id": "", "total_count": 0, "success_count": 0, "failed_count": 0, "unsupported_count": 0}, nil
	}
	if len(ids) > 200 {
		return nil, badRequest("account_ids cannot exceed 200.")
	}
	jobID, err := s.createQuotaRefreshJob(ctx, ids, triggerType, requestedBy)
	if err != nil {
		return nil, err
	}
	successCount := 0
	failedCount := 0
	unsupportedCount := 0
	for _, accountID := range ids {
		status, err := s.refreshAccountQuota(ctx, accountID, jobID)
		if err != nil {
			failedCount++
			slog.WarnContext(ctx, "account quota refresh failed", "account_id", accountID, "error", err)
			if writeErr := s.writeQuotaFailureSnapshot(ctx, accountID, jobID, "failed", err.Error()); writeErr != nil {
				slog.WarnContext(ctx, "quota failure snapshot failed", "account_id", accountID, "error", writeErr)
			}
			continue
		}
		switch status {
		case "success":
			successCount++
		case "unsupported":
			unsupportedCount++
		default:
			failedCount++
		}
	}
	jobStatus := "success"
	errorMessage := ""
	if successCount == 0 && failedCount > 0 {
		jobStatus = "failed"
		errorMessage = "All quota refresh attempts failed."
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE account_quota_refresh_jobs
		SET status = $2, total_count = $3, success_count = $4, failed_count = $5, unsupported_count = $6,
		    error_message = $7, finished_at = now(), metadata_json = $8::jsonb
		WHERE id = $1
	`, jobID, jobStatus, len(ids), successCount, failedCount, unsupportedCount, errorMessage, mustEncodeJSON(map[string]any{"account_ids": ids})); err != nil {
		return nil, err
	}
	return map[string]any{
		"job_id":            jobID,
		"status":            jobStatus,
		"total_count":       len(ids),
		"success_count":     successCount,
		"failed_count":      failedCount,
		"unsupported_count": unsupportedCount,
	}, nil
}

func (s *Server) createQuotaRefreshJob(ctx context.Context, accountIDs []string, triggerType string, requestedBy string) (string, error) {
	var account any
	if len(accountIDs) == 1 {
		account = accountIDs[0]
	}
	var requester any
	if strings.TrimSpace(requestedBy) != "" {
		requester = strings.TrimSpace(requestedBy)
	}
	var id string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO account_quota_refresh_jobs (account_id, status, trigger_type, requested_by, total_count, started_at, metadata_json)
		VALUES ($1, 'running', $2, $3, $4, now(), $5::jsonb)
		RETURNING id::text
	`, account, defaultString(triggerType, "manual"), requester, len(accountIDs), mustEncodeJSON(map[string]any{"account_ids": accountIDs})).Scan(&id)
	return id, err
}

func (s *Server) accountsForManualQuotaRefresh(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id::text
		FROM accounts a
		WHERE a.status = 'active'
		ORDER BY a.priority ASC, a.created_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Server) accountsForScheduledQuotaRefresh(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id::text
		FROM accounts a
		JOIN providers p ON p.id = a.provider_id
		LEFT JOIN channels c ON c.id = a.channel_id
		LEFT JOIN account_platform_configs pc ON pc.provider_type = p.provider_type
		LEFT JOIN LATERAL (
		  SELECT created_at
		  FROM account_quota_snapshots
		  WHERE account_id = a.id
		  ORDER BY created_at DESC
		  LIMIT 1
		) last_snapshot ON true
		WHERE a.status = 'active'
		  AND COALESCE(pc.status = 'active' AND pc.quota_refresh_enabled, true)
		  AND (
		    a.metadata_json ? 'quota' OR a.metadata_json ? 'quota_endpoint' OR
		    c.metadata_json ? 'quota' OR c.metadata_json ? 'quota_endpoint' OR
		    p.metadata_json ? 'quota' OR p.metadata_json ? 'quota_endpoint' OR
		    (jsonb_typeof(a.metadata_json->'quota_adapter') = 'object' AND a.metadata_json->'quota_adapter' ? 'endpoint') OR
		    (jsonb_typeof(c.metadata_json->'quota_adapter') = 'object' AND c.metadata_json->'quota_adapter' ? 'endpoint') OR
		    (jsonb_typeof(p.metadata_json->'quota_adapter') = 'object' AND p.metadata_json->'quota_adapter' ? 'endpoint')
		  )
		  AND (last_snapshot.created_at IS NULL OR last_snapshot.created_at < now() - interval '15 minutes')
		ORDER BY last_snapshot.created_at ASC NULLS FIRST, a.priority ASC, a.created_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Server) refreshAccountQuota(ctx context.Context, accountID string, jobID string) (string, error) {
	target, err := s.loadAccountQuotaTarget(ctx, accountID)
	if err != nil {
		return "failed", err
	}
	readings, err := s.discoverAccountQuota(ctx, target)
	if err != nil {
		return "failed", err
	}
	if len(readings) == 0 {
		readings = []quotaReading{{
			Status:       "unsupported",
			Source:       "unsupported",
			WindowType:   "requests",
			ErrorMessage: "Provider does not expose an automatic quota source for this account.",
			Raw:          map[string]any{"provider_type": target.ProviderType},
		}}
	}
	overall := "success"
	for _, reading := range readings {
		if reading.Status == "" {
			reading.Status = "success"
		}
		if reading.WindowType == "" {
			reading.WindowType = "requests"
		}
		if reading.Raw == nil {
			reading.Raw = map[string]any{}
		}
		if reading.Status == "unsupported" && overall == "success" {
			overall = "unsupported"
		}
		if reading.Status == "failed" {
			overall = "failed"
		}
		if err := s.persistQuotaReading(ctx, target, jobID, reading); err != nil {
			return "failed", err
		}
	}
	return overall, nil
}

func (s *Server) loadAccountQuotaTarget(ctx context.Context, accountID string) (accountQuotaTarget, error) {
	var target accountQuotaTarget
	var accountMetadata, channelMetadata, providerMetadata string
	var ciphertext, nonce []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT a.id::text, a.name, p.id::text, p.provider_type, COALESCE(c.id::text, ''),
		       COALESCE(c.base_url, ''), COALESCE(ap.proxy_url, cp.proxy_url, ''),
		       a.metadata_json::text, COALESCE(c.metadata_json::text, '{}'), p.metadata_json::text,
		       COALESCE(v.secret_ciphertext, '\x'::bytea), COALESCE(v.secret_nonce, '\x'::bytea)
		FROM accounts a
		JOIN providers p ON p.id = a.provider_id
		LEFT JOIN channels c ON c.id = a.channel_id
		LEFT JOIN proxies cp ON cp.id = c.proxy_id AND cp.status = 'active'
		LEFT JOIN proxies ap ON ap.id = a.proxy_id AND ap.status = 'active'
		LEFT JOIN credential_vault_records v ON v.id = a.credential_vault_record_id
		WHERE a.id = $1
	`, accountID).Scan(&target.AccountID, &target.AccountName, &target.ProviderID, &target.ProviderType, &target.ChannelID, &target.BaseURL, &target.ProxyURL, &accountMetadata, &channelMetadata, &providerMetadata, &ciphertext, &nonce)
	if err == sql.ErrNoRows {
		return target, notFound("Account was not found.")
	}
	if err != nil {
		return target, err
	}
	target.AccountMetadata = jsonObjectFromString(accountMetadata)
	target.ChannelMetadata = jsonObjectFromString(channelMetadata)
	target.ProviderMeta = jsonObjectFromString(providerMetadata)
	if len(ciphertext) > 0 {
		secret, err := security.DecryptSecret(s.cfg.VaultKey, ciphertext, nonce)
		if err != nil {
			return target, err
		}
		bundle, err := normalizedTokenBundleFromSecret(secret)
		if err != nil {
			return target, err
		}
		target.APIKey = bundle.authSecret()
		target.AuthScheme = bundle.AuthScheme
	}
	return target, nil
}

func (s *Server) discoverAccountQuota(ctx context.Context, target accountQuotaTarget) ([]quotaReading, error) {
	for _, adapter := range accountQuotaAdaptersForTarget(target) {
		readings, err := adapter.Discover(ctx, s, target)
		if err != nil {
			return nil, err
		}
		if len(readings) > 0 {
			return readings, nil
		}
	}
	return nil, nil
}

func quotaReadingsFromMetadata(metadata map[string]any, source string) []quotaReading {
	quotaValue, ok := metadata["quota"]
	if !ok {
		return nil
	}
	return quotaReadingsFromValue(quotaValue, source)
}

func quotaReadingsFromValue(value any, source string) []quotaReading {
	switch typed := value.(type) {
	case []any:
		readings := []quotaReading{}
		for _, item := range typed {
			readings = append(readings, quotaReadingsFromValue(item, source)...)
		}
		return readings
	case map[string]any:
		for _, key := range []string{"windows", "items", "quotas"} {
			if nested, ok := typed[key]; ok {
				return quotaReadingsFromValue(nested, source)
			}
		}
		remaining, okRemaining := decimalStringFromAny(typed["remaining"])
		limitValue, okLimit := decimalStringFromAny(firstQuotaField(typed, "limit", "limit_value", "total"))
		if !okRemaining && !okLimit {
			return nil
		}
		resetAt := timeFromAny(firstQuotaField(typed, "reset_at", "resets_at", "expires_at"))
		return []quotaReading{{
			Status:     "success",
			Source:     defaultString(metadataText(typed["source"]), source),
			WindowType: defaultString(firstNonEmpty(metadataText(typed["window_type"]), metadataText(typed["type"])), "requests"),
			Remaining:  remaining,
			Limit:      limitValue,
			ResetAt:    resetAt,
			Raw:        typed,
		}}
	default:
		return nil
	}
}

func (s *Server) quotaReadingsFromEndpoint(ctx context.Context, target accountQuotaTarget, endpoint string) ([]quotaReading, error) {
	return s.quotaReadingsFromEndpointWithSchema(ctx, target, endpoint, "", "quota_endpoint")
}

func (s *Server) quotaReadingsFromEndpointWithSchema(ctx context.Context, target accountQuotaTarget, endpoint string, schema string, source string) ([]quotaReading, error) {
	if target.BaseURL == "" {
		return nil, badRequest("quota_endpoint requires an account channel base_url.")
	}
	targetURL, err := absoluteOrRelativeURL(target.BaseURL, endpoint)
	if err != nil {
		return nil, err
	}
	var client httpDoer = s.httpClient
	if target.ProxyURL != "" {
		if s.upstreamPool == nil {
			s.upstreamPool = newUpstreamClientPool()
		}
		nextClient, err := s.upstreamPool.client(routeInfo{ProviderType: target.ProviderType, ProxyURL: target.ProxyURL}, true)
		if err != nil {
			return nil, err
		}
		client = withHTTPClientTimeout(nextClient, 20*time.Second)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, badRequest("quota_endpoint produced an invalid URL.")
	}
	if target.APIKey != "" {
		scheme := strings.TrimSpace(target.AuthScheme)
		if scheme == "" {
			scheme = "Bearer"
		}
		req.Header.Set("Authorization", scheme+" "+target.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, appError{status: http.StatusBadGateway, code: "quota_refresh_failed", message: truncateForStorage(string(body), 500), typ: "upstream_error"}
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, badRequest("quota_endpoint returned invalid JSON.")
	}
	readings := quotaReadingsFromValueWithSchema(payload, defaultString(source, "quota_endpoint"), schema)
	if len(readings) == 0 {
		return []quotaReading{{
			Status:       "unsupported",
			Source:       defaultString(source, "quota_endpoint"),
			WindowType:   "requests",
			ErrorMessage: "quota_endpoint JSON did not contain remaining or limit fields.",
			Raw:          map[string]any{"response": payload},
		}}, nil
	}
	return readings, nil
}

func (s *Server) persistQuotaReading(ctx context.Context, target accountQuotaTarget, jobID string, reading quotaReading) error {
	raw := defaultMap(reading.Raw)
	var resetAt any
	if reading.ResetAt != nil {
		resetAt = reading.ResetAt.UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var snapshotID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO account_quota_snapshots (
		  account_id, provider_id, channel_id, refresh_job_id, status, source, window_type,
		  remaining, limit_value, reset_at, error_message, raw_json
		)
		VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, $6, $7, NULLIF($8, '')::numeric,
		        NULLIF($9, '')::numeric, $10, $11, $12::jsonb)
		RETURNING id::text
	`, target.AccountID, target.ProviderID, target.ChannelID, jobID, reading.Status, reading.Source, reading.WindowType, reading.Remaining, reading.Limit, resetAt, truncateForStorage(reading.ErrorMessage, 1000), mustEncodeJSON(raw)).Scan(&snapshotID); err != nil {
		return err
	}
	if reading.Status == "success" {
		metadata := map[string]any{
			"limit":                  reading.Limit,
			"source":                 reading.Source,
			"last_quota_snapshot_id": snapshotID,
			"last_quota_refresh_at":  time.Now().UTC().Format(time.RFC3339),
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE account_quota_windows
			SET remaining = NULLIF($3, '')::numeric,
			    reset_at = $4,
			    metadata_json = metadata_json || $5::jsonb
			WHERE id = (
			  SELECT id FROM account_quota_windows
			  WHERE account_id = $1 AND window_type = $2
			  ORDER BY created_at DESC
			  LIMIT 1
			)
		`, target.AccountID, reading.WindowType, reading.Remaining, resetAt, mustEncodeJSON(metadata))
		if err != nil {
			return err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if updated == 0 {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO account_quota_windows (account_id, window_type, reset_at, remaining, metadata_json)
				VALUES ($1, $2, $3, NULLIF($4, '')::numeric, $5::jsonb)
			`, target.AccountID, reading.WindowType, resetAt, reading.Remaining, mustEncodeJSON(metadata)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Server) writeQuotaFailureSnapshot(ctx context.Context, accountID string, jobID string, status string, message string) error {
	target, err := s.loadAccountQuotaTarget(ctx, accountID)
	if err != nil {
		return err
	}
	return s.persistQuotaReading(ctx, target, jobID, quotaReading{
		Status:       status,
		Source:       "quota_refresh",
		WindowType:   "requests",
		ErrorMessage: truncateForStorage(message, 1000),
		Raw:          map[string]any{"error": message},
	})
}

func (s *Server) startAccountPoolWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.accountPoolWorkerTick(ctx)
			}
		}
	}()
}

func (s *Server) accountPoolWorkerTick(ctx context.Context) {
	if s.db == nil {
		return
	}
	workerCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if s.boolSystemSetting(workerCtx, "account_pool.quota_refresh_enabled", false) && s.quotaRefreshDue(workerCtx) {
		if _, err := s.runQuotaRefresh(workerCtx, nil, "scheduled", ""); err != nil {
			slog.WarnContext(ctx, "scheduled quota refresh failed", "error", err)
		}
	}
	if s.boolSystemSetting(workerCtx, "account_pool.wakeup_enabled", false) {
		if err := s.runDueWakeupJobs(workerCtx); err != nil {
			slog.WarnContext(ctx, "scheduled wakeup jobs failed", "error", err)
		}
	}
	if s.boolSystemSetting(workerCtx, "account_pool.quality_enabled", false) && s.qualityCheckDue(workerCtx) {
		applyActions := s.boolSystemSetting(workerCtx, "account_pool.quality_isolation_enabled", false)
		isolationThreshold := s.scoreSystemSetting(workerCtx, "account_pool.quality_isolation_threshold", 40)
		watchThreshold := s.scoreSystemSetting(workerCtx, "account_pool.quality_watch_threshold", 70)
		if _, err := s.recomputeAccountQuality(workerCtx, nil, applyActions, isolationThreshold, watchThreshold, ""); err != nil {
			slog.WarnContext(ctx, "scheduled account quality check failed", "error", err)
		}
	}
	if s.boolSystemSetting(workerCtx, "account_pool.health_enabled", false) && s.healthCheckDue(workerCtx) {
		if err := s.runScheduledAccountHealth(workerCtx); err != nil {
			slog.WarnContext(ctx, "scheduled account health check failed", "error", err)
		}
	}
}

func (s *Server) quotaRefreshDue(ctx context.Context) bool {
	interval := s.intSystemSetting(ctx, "account_pool.quota_refresh_interval_seconds", 900)
	var last sql.NullTime
	if err := s.db.QueryRowContext(ctx, "SELECT max(created_at) FROM account_quota_refresh_jobs WHERE trigger_type = 'scheduled'").Scan(&last); err != nil {
		return false
	}
	return !last.Valid || time.Since(last.Time) >= time.Duration(interval)*time.Second
}

func (s *Server) healthCheckDue(ctx context.Context) bool {
	interval := s.intSystemSetting(ctx, "account_pool.health_interval_seconds", 300)
	var last sql.NullTime
	if err := s.db.QueryRowContext(ctx, "SELECT max(tested_at) FROM channel_test_results WHERE test_type = 'health'").Scan(&last); err != nil {
		return false
	}
	return !last.Valid || time.Since(last.Time) >= time.Duration(interval)*time.Second
}

func (s *Server) runScheduledAccountHealth(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id::text
		FROM accounts a
		JOIN providers p ON p.id = a.provider_id
		LEFT JOIN account_platform_configs pc ON pc.provider_type = p.provider_type
		WHERE a.channel_id IS NOT NULL
		  AND a.status IN ('active', 'cooldown')
		  AND COALESCE(pc.status = 'active' AND pc.health_enabled, true)
		ORDER BY a.priority ASC, a.created_at ASC
		LIMIT 50
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		result, err := s.runChannelCheckContext(ctx, "", id, "", "", "/models")
		if err != nil {
			return err
		}
		status := metadataText(result["status"])
		if status == "success" {
			if _, err := s.db.ExecContext(ctx, `
				UPDATE accounts SET status = 'active' WHERE id = $1 AND status = 'cooldown';
				UPDATE account_runtime_states
				SET cooldown_until = NULL, last_error = '', failure_count = 0,
				    circuit_state = 'closed', circuit_failure_count = 0,
				    circuit_opened_at = NULL, circuit_half_open_after = NULL
				WHERE account_id = $1
			`, id); err != nil {
				return err
			}
			continue
		}
		message := truncateForStorage(metadataText(result["error_message"]), 500)
		if message == "" {
			message = "scheduled health check failed"
		}
		if _, err := s.db.ExecContext(ctx, `
			UPDATE accounts SET status = 'cooldown' WHERE id = $1 AND status = 'active';
			UPDATE account_runtime_states
			SET cooldown_until = now() + interval '5 minutes',
			    last_error = $2,
			    failure_count = failure_count + 1
			WHERE account_id = $1
		`, id, message); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) boolSystemSetting(ctx context.Context, key string, fallback bool) bool {
	value := map[string]any{}
	var raw string
	if err := s.db.QueryRowContext(ctx, "SELECT setting_value_json::text FROM system_settings WHERE setting_key = $1", key).Scan(&raw); err != nil {
		return fallback
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return fallback
	}
	if enabled, ok := value["enabled"].(bool); ok {
		return enabled
	}
	return fallback
}

func (s *Server) intSystemSetting(ctx context.Context, key string, fallback int) int {
	value := map[string]any{}
	var raw string
	if err := s.db.QueryRowContext(ctx, "SELECT setting_value_json::text FROM system_settings WHERE setting_key = $1", key).Scan(&raw); err != nil {
		return fallback
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return fallback
	}
	if seconds, ok := value["seconds"].(float64); ok && seconds > 0 {
		return int(seconds)
	}
	return fallback
}

func absoluteOrRelativeURL(baseURL string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", badRequest("quota_endpoint is required.")
	}
	if parsed, err := url.Parse(value); err == nil && parsed.IsAbs() {
		return parsed.String(), nil
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", badRequest("channel base_url is invalid.")
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	base.Path = strings.TrimRight(base.Path, "/") + value
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

func jsonObjectFromString(value string) map[string]any {
	result := map[string]any{}
	if strings.TrimSpace(value) == "" {
		return result
	}
	_ = json.Unmarshal([]byte(value), &result)
	return result
}

func decimalStringFromAny(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "", false
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return "", false
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil || parsed < 0 {
			return "", false
		}
		return text, true
	case float64:
		if typed < 0 {
			return "", false
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case int:
		if typed < 0 {
			return "", false
		}
		return strconv.Itoa(typed), true
	default:
		return "", false
	}
}

func firstQuotaField(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func timeFromAny(value any) *time.Time {
	text := strings.TrimSpace(metadataText(value))
	if text == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return nil
	}
	return &parsed
}

func (s *Server) adminCreateAccountPoolGroup(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id, err := s.upsertAccountPoolGroup(r, auth, "", true)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminPatchAccountPoolGroup(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id, err := s.upsertAccountPoolGroup(r, auth, r.PathValue("groupId"), false)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) upsertAccountPoolGroup(r *http.Request, auth authContext, id string, insert bool) (string, error) {
	var req struct {
		Name             string         `json:"name"`
		Description      string         `json:"description"`
		Status           string         `json:"status"`
		Priority         *int           `json:"priority"`
		DefaultRouteTags []string       `json:"default_route_tags"`
		DefaultMetadata  map[string]any `json:"default_metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return "", err
	}
	if insert && strings.TrimSpace(req.Name) == "" {
		return "", badRequest("name is required.")
	}
	status, err := defaultedStatus(req.Status, "active", "active", "disabled")
	if err != nil {
		return "", err
	}
	priority := 100
	if req.Priority != nil {
		priority = *req.Priority
	}
	tags, err := encodeJSON(normalizeStringList(req.DefaultRouteTags))
	if err != nil {
		return "", err
	}
	metadata, err := encodeJSON(defaultMap(req.DefaultMetadata))
	if err != nil {
		return "", err
	}
	if insert {
		err = s.db.QueryRowContext(r.Context(), `
			INSERT INTO account_pool_groups (name, description, status, priority, default_route_tags_json, default_metadata_json)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb)
			RETURNING id::text
		`, strings.TrimSpace(req.Name), req.Description, status, priority, tags, metadata).Scan(&id)
	} else {
		if err := s.requireIDExists(r.Context(), "account_pool_groups", id, "Account pool group was not found."); err != nil {
			return "", err
		}
		_, err = s.db.ExecContext(r.Context(), `
			UPDATE account_pool_groups
			SET name = COALESCE(NULLIF($2, ''), name),
			    description = $3,
			    status = $4,
			    priority = $5,
			    default_route_tags_json = $6::jsonb,
			    default_metadata_json = $7::jsonb
			WHERE id = $1
		`, id, strings.TrimSpace(req.Name), req.Description, status, priority, tags, metadata)
	}
	if err != nil {
		return "", err
	}
	action := "account_pool_group.update"
	if insert {
		action = "account_pool_group.create"
	}
	audit(r.Context(), s.db, auth.UserID, "admin", action, "account_pool_group", id, r, nil)
	return id, nil
}

func (s *Server) adminAddAccountPoolGroupMember(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		AccountID string `json:"account_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	groupID := r.PathValue("groupId")
	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		return badRequest("account_id is required.")
	}
	if _, err := s.db.ExecContext(r.Context(), `
		INSERT INTO account_pool_group_members (group_id, account_id)
		VALUES ($1, $2)
		ON CONFLICT (group_id, account_id) DO NOTHING
	`, groupID, accountID); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "account_pool_group.member_upsert", "account_pool_group", groupID, r, map[string]any{"account_id": accountID})
	writeJSON(w, http.StatusOK, map[string]any{"group_id": groupID, "account_id": accountID, "updated": true}, nil)
	return nil
}

func (s *Server) adminDeleteAccountPoolGroupMember(w http.ResponseWriter, r *http.Request, auth authContext) error {
	groupID := r.PathValue("groupId")
	accountID := r.PathValue("accountId")
	if _, err := s.db.ExecContext(r.Context(), "DELETE FROM account_pool_group_members WHERE group_id = $1 AND account_id = $2", groupID, accountID); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "account_pool_group.member_delete", "account_pool_group", groupID, r, map[string]any{"account_id": accountID})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true}, nil)
	return nil
}

func (s *Server) setAccountPoolGroupByNameTx(ctx context.Context, tx *sql.Tx, accountID string, groupName string) error {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return nil
	}
	var groupID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO account_pool_groups (name)
		VALUES ($1)
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id::text
	`, groupName).Scan(&groupID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM account_pool_group_members WHERE account_id = $1", accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO account_pool_group_members (group_id, account_id)
		VALUES ($1, $2)
		ON CONFLICT (group_id, account_id) DO NOTHING
	`, groupID, accountID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE accounts
		SET metadata_json = jsonb_set(metadata_json, '{pool_group}', to_jsonb($2::text), true)
		WHERE id = $1
	`, accountID, groupName)
	return err
}
