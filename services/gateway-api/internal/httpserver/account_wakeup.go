package httpserver

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type accountPlatformConfig struct {
	ProviderType                string
	DisplayName                 string
	Status                      string
	HealthEnabled               bool
	QuotaRefreshEnabled         bool
	WakeupEnabled               bool
	HealthIntervalSeconds       int
	QuotaRefreshIntervalSeconds int
	WakeupIntervalSeconds       int
	QuotaLowThresholdPercent    int
	MaxFailureCount             int
	Metadata                    map[string]any
	CreatedAt                   *time.Time
	UpdatedAt                   *time.Time
}

func (s *Server) adminWakeupJobs(w http.ResponseWriter, r *http.Request, auth authContext) error {
	args := []any{limitFromRequest(r, 100, 500)}
	filters := []string{}
	if accountID := strings.TrimSpace(r.URL.Query().Get("account_id")); accountID != "" {
		args = append(args, accountID)
		filters = append(filters, "awj.account_id::text = $"+strconv.Itoa(len(args)))
	}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		args = append(args, status)
		filters = append(filters, "awj.status = $"+strconv.Itoa(len(args)))
	}
	where := ""
	if len(filters) > 0 {
		where = "WHERE " + strings.Join(filters, " AND ")
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT awj.id::text, COALESCE(awj.account_id::text, ''), COALESCE(a.name, ''),
		       awj.status, awj.trigger_type, COALESCE(awj.requested_by::text, ''),
		       awj.target_path, awj.model_name, awj.endpoint, awj.scheduled_for,
		       awj.total_count, awj.success_count, awj.failed_count, awj.skipped_count,
		       awj.error_message, awj.result_json::text, awj.metadata_json::text,
		       awj.started_at, awj.finished_at, awj.created_at
		FROM account_wakeup_jobs awj
		LEFT JOIN accounts a ON a.id = awj.account_id
		`+where+`
		ORDER BY awj.created_at DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, accountID, accountName, status, triggerType, requestedBy, targetPath, modelName, endpoint, errorMessage, resultJSON, metadata string
		var totalCount, successCount, failedCount, skippedCount int
		var scheduledFor, createdAt time.Time
		var startedAt, finishedAt sql.NullTime
		if err := rows.Scan(&id, &accountID, &accountName, &status, &triggerType, &requestedBy, &targetPath, &modelName, &endpoint, &scheduledFor, &totalCount, &successCount, &failedCount, &skippedCount, &errorMessage, &resultJSON, &metadata, &startedAt, &finishedAt, &createdAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":            id,
			"account_id":    accountID,
			"account_name":  accountName,
			"status":        status,
			"trigger_type":  triggerType,
			"requested_by":  requestedBy,
			"target_path":   targetPath,
			"model_name":    modelName,
			"endpoint":      endpoint,
			"scheduled_for": scheduledFor.UTC().Format(time.RFC3339),
			"total_count":   totalCount,
			"success_count": successCount,
			"failed_count":  failedCount,
			"skipped_count": skippedCount,
			"error_message": errorMessage,
			"result":        jsonArrayRaw(resultJSON),
			"metadata":      jsonRaw(metadata),
			"started_at":    nullableSQLTime(startedAt),
			"finished_at":   nullableSQLTime(finishedAt),
			"created_at":    createdAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateWakeupJob(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		AccountIDs   []string       `json:"account_ids"`
		TargetPath   string         `json:"target_path"`
		ModelName    string         `json:"model_name"`
		Endpoint     string         `json:"endpoint"`
		ScheduledFor string         `json:"scheduled_for"`
		RunNow       *bool          `json:"run_now"`
		Metadata     map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	ids := normalizeStringList(req.AccountIDs)
	if len(ids) > 50 {
		return badRequest("account_ids cannot exceed 50.")
	}
	triggerType := "manual"
	scheduledFor := time.Now().UTC()
	if strings.TrimSpace(req.ScheduledFor) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ScheduledFor))
		if err != nil {
			return badRequest("scheduled_for must be RFC3339.")
		}
		scheduledFor = parsed.UTC()
		triggerType = "scheduled"
	}
	runNow := req.RunNow == nil || *req.RunNow
	if scheduledFor.After(time.Now().UTC()) && req.RunNow == nil {
		runNow = false
	}
	if len(ids) == 0 && runNow {
		nextIDs, err := s.accountsForWakeup(r.Context(), 50)
		if err != nil {
			return err
		}
		ids = nextIDs
	}
	jobID, err := s.createWakeupJob(r.Context(), ids, triggerType, auth.UserID, req.TargetPath, req.ModelName, req.Endpoint, scheduledFor, req.Metadata)
	if err != nil {
		return err
	}
	result := map[string]any{"id": jobID, "status": "pending", "total_count": len(ids)}
	if runNow {
		result, err = s.runWakeupJob(r.Context(), jobID)
		if err != nil {
			return err
		}
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "account_wakeup_job.create", "account_wakeup_job", jobID, r, result)
	writeJSON(w, http.StatusCreated, result, nil)
	return nil
}

func (s *Server) adminRunWakeupJob(w http.ResponseWriter, r *http.Request, auth authContext) error {
	jobID := r.PathValue("jobId")
	result, err := s.runWakeupJob(r.Context(), jobID)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "account_wakeup_job.run", "account_wakeup_job", jobID, r, result)
	writeJSON(w, http.StatusOK, result, nil)
	return nil
}

func (s *Server) createWakeupJob(ctx context.Context, accountIDs []string, triggerType string, requestedBy string, targetPath string, modelName string, endpoint string, scheduledFor time.Time, metadata map[string]any) (string, error) {
	ids := normalizeStringList(accountIDs)
	var account any
	if len(ids) == 1 {
		account = ids[0]
	}
	var requester any
	if strings.TrimSpace(requestedBy) != "" {
		requester = strings.TrimSpace(requestedBy)
	}
	if strings.TrimSpace(targetPath) == "" {
		targetPath = "/models"
	}
	jobMeta := mergeMetadataMaps(defaultMap(metadata), map[string]any{"account_ids": ids})
	var id string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO account_wakeup_jobs (
		  account_id, status, trigger_type, requested_by, target_path, model_name, endpoint,
		  scheduled_for, total_count, metadata_json
		)
		VALUES ($1, 'pending', $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
		RETURNING id::text
	`, account, defaultString(triggerType, "manual"), requester, targetPath, strings.TrimSpace(modelName), strings.TrimSpace(endpoint), scheduledFor.UTC(), len(ids), mustEncodeJSON(jobMeta)).Scan(&id)
	return id, err
}

func (s *Server) runWakeupJob(ctx context.Context, jobID string) (map[string]any, error) {
	var status, targetPath, modelName, endpoint, metadataRaw string
	var scheduledFor time.Time
	err := s.db.QueryRowContext(ctx, `
		UPDATE account_wakeup_jobs
		SET status = 'running', started_at = COALESCE(started_at, now()), error_message = ''
		WHERE id::text = $1 AND status IN ('pending', 'failed')
		RETURNING status, target_path, model_name, endpoint, scheduled_for, metadata_json::text
	`, strings.TrimSpace(jobID)).Scan(&status, &targetPath, &modelName, &endpoint, &scheduledFor, &metadataRaw)
	if err == sql.ErrNoRows {
		return nil, badRequest("Wakeup job is not pending or retryable.")
	}
	if err != nil {
		return nil, err
	}
	metadata := jsonObjectFromString(metadataRaw)
	ids := metadataStringList(metadata["account_ids"])
	if len(ids) == 0 {
		nextIDs, err := s.accountsForWakeup(ctx, 50)
		if err != nil {
			return nil, err
		}
		ids = nextIDs
	}
	successCount := 0
	failedCount := 0
	results := []map[string]any{}
	for _, accountID := range ids {
		result := map[string]any{"account_id": accountID}
		check, err := s.runChannelCheckContext(ctx, "", accountID, modelName, endpoint, targetPath)
		if err != nil {
			failedCount++
			message := truncateForStorage(err.Error(), 500)
			result["status"] = "failed"
			result["error_message"] = message
			if markErr := s.markAccountWakeupFailed(ctx, accountID, message); markErr != nil {
				result["mark_error"] = markErr.Error()
			}
			results = append(results, result)
			continue
		}
		for key, value := range check {
			result[key] = value
		}
		if metadataText(check["status"]) == "success" {
			successCount++
			if err := s.markAccountWakeupSuccess(ctx, accountID); err != nil {
				result["mark_error"] = err.Error()
			}
		} else {
			failedCount++
			message := truncateForStorage(metadataText(check["error_message"]), 500)
			if message == "" {
				message = "wakeup check failed"
			}
			if err := s.markAccountWakeupFailed(ctx, accountID, message); err != nil {
				result["mark_error"] = err.Error()
			}
		}
		results = append(results, result)
	}
	jobStatus := "success"
	errorMessage := ""
	if successCount == 0 && failedCount > 0 {
		jobStatus = "failed"
		errorMessage = "All wakeup attempts failed."
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE account_wakeup_jobs
		SET status = $2, total_count = $3, success_count = $4, failed_count = $5,
		    skipped_count = 0, error_message = $6, result_json = $7::jsonb,
		    finished_at = now(), metadata_json = metadata_json || $8::jsonb
		WHERE id::text = $1
	`, jobID, jobStatus, len(ids), successCount, failedCount, errorMessage, mustEncodeJSON(results), mustEncodeJSON(map[string]any{"account_ids": ids, "last_run_at": time.Now().UTC().Format(time.RFC3339)})); err != nil {
		return nil, err
	}
	return map[string]any{
		"id":            jobID,
		"status":        jobStatus,
		"total_count":   len(ids),
		"success_count": successCount,
		"failed_count":  failedCount,
		"result":        results,
	}, nil
}

func (s *Server) accountsForWakeup(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id::text
		FROM accounts a
		JOIN providers p ON p.id = a.provider_id
		LEFT JOIN account_platform_configs pc ON pc.provider_type = p.provider_type
		WHERE a.channel_id IS NOT NULL
		  AND a.status IN ('cooldown', 'exhausted')
		  AND COALESCE(pc.status = 'active' AND pc.wakeup_enabled, true)
		ORDER BY a.priority ASC, a.updated_at ASC
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

func (s *Server) runDueWakeupJobs(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text
		FROM account_wakeup_jobs
		WHERE status = 'pending' AND scheduled_for <= now()
		ORDER BY scheduled_for ASC, created_at ASC
		LIMIT 5
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
		if _, err := s.runWakeupJob(ctx, id); err != nil {
			slog.WarnContext(ctx, "scheduled wakeup job failed", "job_id", id, "error", err)
		}
	}
	return nil
}

func (s *Server) markAccountWakeupSuccess(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE accounts SET status = 'active' WHERE id::text = $1;
		UPDATE account_runtime_states
		SET cooldown_until = NULL,
		    last_error = '',
		    failure_count = 0,
		    circuit_state = 'closed',
		    circuit_failure_count = 0,
		    circuit_opened_at = NULL,
		    circuit_half_open_after = NULL
		WHERE account_id::text = $1
	`, accountID)
	return err
}

func (s *Server) markAccountWakeupFailed(ctx context.Context, accountID string, message string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE accounts SET status = 'cooldown' WHERE id::text = $1 AND status = 'active';
		UPDATE account_runtime_states
		SET cooldown_until = now() + interval '5 minutes',
		    last_error = $2,
		    failure_count = failure_count + 1
		WHERE account_id::text = $1
	`, accountID, truncateForStorage(message, 500))
	return err
}

func (s *Server) adminAccountPlatformConfigs(w http.ResponseWriter, r *http.Request, auth authContext) error {
	configs, err := s.loadAccountPlatformConfigs(r.Context())
	if err != nil {
		return err
	}
	stats, err := s.accountPlatformStats(r.Context())
	if err != nil {
		return err
	}
	providerTypes := map[string]bool{}
	for _, template := range accountImportTemplates() {
		if strings.TrimSpace(template.ProviderType) != "" {
			providerTypes[template.ProviderType] = true
		}
	}
	for providerType := range configs {
		providerTypes[providerType] = true
	}
	for providerType := range stats {
		providerTypes[providerType] = true
	}
	keys := make([]string, 0, len(providerTypes))
	for providerType := range providerTypes {
		keys = append(keys, providerType)
	}
	sort.Strings(keys)
	items := []map[string]any{}
	for _, providerType := range keys {
		config := defaultAccountPlatformConfig(providerType)
		if existing, ok := configs[providerType]; ok {
			config = existing
		}
		row := map[string]any{
			"provider_type":                  config.ProviderType,
			"display_name":                   config.DisplayName,
			"status":                         config.Status,
			"health_enabled":                 config.HealthEnabled,
			"quota_refresh_enabled":          config.QuotaRefreshEnabled,
			"wakeup_enabled":                 config.WakeupEnabled,
			"health_interval_seconds":        config.HealthIntervalSeconds,
			"quota_refresh_interval_seconds": config.QuotaRefreshIntervalSeconds,
			"wakeup_interval_seconds":        config.WakeupIntervalSeconds,
			"quota_low_threshold_percent":    config.QuotaLowThresholdPercent,
			"max_failure_count":              config.MaxFailureCount,
			"metadata":                       config.Metadata,
			"created_at":                     nullableTime(config.CreatedAt),
			"updated_at":                     nullableTime(config.UpdatedAt),
		}
		for key, value := range stats[providerType] {
			row[key] = value
		}
		items = append(items, row)
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminPutAccountPlatformConfig(w http.ResponseWriter, r *http.Request, auth authContext) error {
	providerType := strings.TrimSpace(r.PathValue("providerType"))
	if providerType == "" {
		return badRequest("provider_type is required.")
	}
	var req struct {
		DisplayName                 string         `json:"display_name"`
		Status                      string         `json:"status"`
		HealthEnabled               *bool          `json:"health_enabled"`
		QuotaRefreshEnabled         *bool          `json:"quota_refresh_enabled"`
		WakeupEnabled               *bool          `json:"wakeup_enabled"`
		HealthIntervalSeconds       int            `json:"health_interval_seconds"`
		QuotaRefreshIntervalSeconds int            `json:"quota_refresh_interval_seconds"`
		WakeupIntervalSeconds       int            `json:"wakeup_interval_seconds"`
		QuotaLowThresholdPercent    int            `json:"quota_low_threshold_percent"`
		MaxFailureCount             int            `json:"max_failure_count"`
		Metadata                    map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	status, err := defaultedStatus(req.Status, "active", "active", "disabled")
	if err != nil {
		return err
	}
	config := defaultAccountPlatformConfig(providerType)
	config.DisplayName = strings.TrimSpace(firstNonEmpty(req.DisplayName, providerType))
	config.Status = status
	if req.HealthEnabled != nil {
		config.HealthEnabled = *req.HealthEnabled
	}
	if req.QuotaRefreshEnabled != nil {
		config.QuotaRefreshEnabled = *req.QuotaRefreshEnabled
	}
	if req.WakeupEnabled != nil {
		config.WakeupEnabled = *req.WakeupEnabled
	}
	if req.HealthIntervalSeconds > 0 {
		config.HealthIntervalSeconds = req.HealthIntervalSeconds
	}
	if req.QuotaRefreshIntervalSeconds > 0 {
		config.QuotaRefreshIntervalSeconds = req.QuotaRefreshIntervalSeconds
	}
	if req.WakeupIntervalSeconds > 0 {
		config.WakeupIntervalSeconds = req.WakeupIntervalSeconds
	}
	if req.QuotaLowThresholdPercent >= 0 && req.QuotaLowThresholdPercent <= 100 {
		config.QuotaLowThresholdPercent = req.QuotaLowThresholdPercent
	}
	if req.MaxFailureCount >= 0 {
		config.MaxFailureCount = req.MaxFailureCount
	}
	config.Metadata = defaultMap(req.Metadata)
	if _, err := s.db.ExecContext(r.Context(), `
		INSERT INTO account_platform_configs (
		  provider_type, display_name, status, health_enabled, quota_refresh_enabled, wakeup_enabled,
		  health_interval_seconds, quota_refresh_interval_seconds, wakeup_interval_seconds,
		  quota_low_threshold_percent, max_failure_count, metadata_json
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb)
		ON CONFLICT (provider_type) DO UPDATE SET
		  display_name = EXCLUDED.display_name,
		  status = EXCLUDED.status,
		  health_enabled = EXCLUDED.health_enabled,
		  quota_refresh_enabled = EXCLUDED.quota_refresh_enabled,
		  wakeup_enabled = EXCLUDED.wakeup_enabled,
		  health_interval_seconds = EXCLUDED.health_interval_seconds,
		  quota_refresh_interval_seconds = EXCLUDED.quota_refresh_interval_seconds,
		  wakeup_interval_seconds = EXCLUDED.wakeup_interval_seconds,
		  quota_low_threshold_percent = EXCLUDED.quota_low_threshold_percent,
		  max_failure_count = EXCLUDED.max_failure_count,
		  metadata_json = EXCLUDED.metadata_json
	`, config.ProviderType, config.DisplayName, config.Status, config.HealthEnabled, config.QuotaRefreshEnabled, config.WakeupEnabled, config.HealthIntervalSeconds, config.QuotaRefreshIntervalSeconds, config.WakeupIntervalSeconds, config.QuotaLowThresholdPercent, config.MaxFailureCount, mustEncodeJSON(config.Metadata)); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "account_platform_config.upsert", "account_platform_config", providerType, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"provider_type": providerType, "updated": true}, nil)
	return nil
}

func (s *Server) loadAccountPlatformConfigs(ctx context.Context) (map[string]accountPlatformConfig, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider_type, display_name, status, health_enabled, quota_refresh_enabled, wakeup_enabled,
		       health_interval_seconds, quota_refresh_interval_seconds, wakeup_interval_seconds,
		       quota_low_threshold_percent, max_failure_count, metadata_json::text, created_at, updated_at
		FROM account_platform_configs
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	configs := map[string]accountPlatformConfig{}
	for rows.Next() {
		var config accountPlatformConfig
		var metadata string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&config.ProviderType, &config.DisplayName, &config.Status, &config.HealthEnabled, &config.QuotaRefreshEnabled, &config.WakeupEnabled, &config.HealthIntervalSeconds, &config.QuotaRefreshIntervalSeconds, &config.WakeupIntervalSeconds, &config.QuotaLowThresholdPercent, &config.MaxFailureCount, &metadata, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		config.Metadata = jsonObjectFromString(metadata)
		config.CreatedAt = &createdAt
		config.UpdatedAt = &updatedAt
		configs[config.ProviderType] = config
	}
	return configs, rows.Err()
}

func (s *Server) accountPlatformStats(ctx context.Context) (map[string]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.provider_type,
		       COUNT(DISTINCT p.id) AS provider_count,
		       COUNT(DISTINCT c.id) AS channel_count,
		       COUNT(DISTINCT a.id) AS account_count,
		       COUNT(DISTINCT a.id) FILTER (WHERE a.status = 'active') AS active_accounts,
		       COUNT(DISTINCT a.id) FILTER (WHERE a.id IS NOT NULL AND a.status <> 'active') AS abnormal_accounts
		FROM providers p
		LEFT JOIN channels c ON c.provider_id = p.id
		LEFT JOIN accounts a ON a.provider_id = p.id
		GROUP BY p.provider_type
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := map[string]map[string]any{}
	for rows.Next() {
		var providerType string
		var providerCount, channelCount, accountCount, activeAccounts, abnormalAccounts int
		if err := rows.Scan(&providerType, &providerCount, &channelCount, &accountCount, &activeAccounts, &abnormalAccounts); err != nil {
			return nil, err
		}
		stats[providerType] = map[string]any{
			"provider_count":    providerCount,
			"channel_count":     channelCount,
			"account_count":     accountCount,
			"active_accounts":   activeAccounts,
			"abnormal_accounts": abnormalAccounts,
		}
	}
	return stats, rows.Err()
}

func defaultAccountPlatformConfig(providerType string) accountPlatformConfig {
	return accountPlatformConfig{
		ProviderType:                strings.TrimSpace(providerType),
		DisplayName:                 strings.TrimSpace(providerType),
		Status:                      "active",
		HealthEnabled:               true,
		QuotaRefreshEnabled:         true,
		WakeupEnabled:               true,
		HealthIntervalSeconds:       300,
		QuotaRefreshIntervalSeconds: 900,
		WakeupIntervalSeconds:       300,
		QuotaLowThresholdPercent:    20,
		MaxFailureCount:             5,
		Metadata:                    map[string]any{},
	}
}
