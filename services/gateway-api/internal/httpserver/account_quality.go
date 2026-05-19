package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type accountQualityTarget struct {
	AccountID           string
	AccountName         string
	AccountStatus       string
	ProviderID          string
	ProviderName        string
	ProviderType        string
	ChannelID           string
	ChannelName         string
	ActiveRequests      int
	SuccessCount        int64
	FailureCount        int64
	CircuitState        string
	CircuitFailureCount int
	CooldownUntil       sql.NullTime
	LastRuntimeError    string
	LastTestStatus      string
	LastTestLatencyMS   int
	LastTestError       string
	LastTestedAt        sql.NullTime
	RecentTestCount     int
	RecentFailedCount   int
	RecentAvgLatencyMS  int
	QuotaRemaining      string
	QuotaLimit          string
	QuotaStatus         string
	QuotaResetAt        sql.NullTime
}

type accountQualityResult struct {
	QualityStatus     string
	Decision          string
	QualityScore      int
	AvailabilityScore int
	LatencyScore      int
	QuotaScore        int
	ErrorScore        int
	Reasons           []string
	Metrics           map[string]any
}

type accountPoolStrategyEventInput struct {
	AccountID      string
	ProviderID     string
	ChannelID      string
	ActorUserID    string
	EventType      string
	Action         string
	Reason         string
	PreviousStatus string
	NextStatus     string
	Decision       string
	QualityScore   *int
	Metadata       map[string]any
}

func (s *Server) adminAccountQuality(w http.ResponseWriter, r *http.Request, auth authContext) error {
	args := []any{}
	filters := []string{}
	if accountID := strings.TrimSpace(r.URL.Query().Get("account_id")); accountID != "" {
		args = append(args, accountID)
		filters = append(filters, "a.id::text = $"+strconv.Itoa(len(args)))
	}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		args = append(args, status)
		filters = append(filters, "a.status = $"+strconv.Itoa(len(args)))
	}
	if decision := strings.TrimSpace(r.URL.Query().Get("decision")); decision != "" {
		args = append(args, decision)
		filters = append(filters, "COALESCE(aq.decision, '') = $"+strconv.Itoa(len(args)))
	}
	if query := strings.TrimSpace(r.URL.Query().Get("q")); query != "" {
		args = append(args, "%"+strings.ToLower(query)+"%")
		filters = append(filters, "(lower(a.name) LIKE $"+strconv.Itoa(len(args))+" OR lower(p.name) LIKE $"+strconv.Itoa(len(args))+" OR lower(COALESCE(c.name, '')) LIKE $"+strconv.Itoa(len(args))+")")
	}
	where := ""
	if len(filters) > 0 {
		where = "WHERE " + strings.Join(filters, " AND ")
	}
	args = append(args, limitFromRequest(r, 200, 500))
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT a.id::text, a.name, a.status, p.id::text, p.name, p.provider_type,
		       COALESCE(c.id::text, ''), COALESCE(c.name, ''),
		       COALESCE(aq.id::text, ''), COALESCE(aq.quality_status, 'unknown'), COALESCE(aq.decision, 'watch'),
		       COALESCE(aq.quality_score, 0), COALESCE(aq.availability_score, 0),
		       COALESCE(aq.latency_score, 0), COALESCE(aq.quota_score, 0), COALESCE(aq.error_score, 0),
		       COALESCE(aq.reason_json::text, '[]'), COALESCE(aq.metrics_json::text, '{}'), aq.created_at
		FROM accounts a
		JOIN providers p ON p.id = a.provider_id
		LEFT JOIN channels c ON c.id = a.channel_id
		LEFT JOIN LATERAL (
		  SELECT *
		  FROM account_quality_snapshots
		  WHERE account_id = a.id
		  ORDER BY created_at DESC
		  LIMIT 1
		) aq ON true
		`+where+`
		ORDER BY COALESCE(aq.quality_score, -1) ASC, a.updated_at DESC
		LIMIT $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var accountID, accountName, accountStatus, providerID, providerName, providerType, channelID, channelName string
		var snapshotID, qualityStatus, decision, reasons, metrics string
		var qualityScore, availabilityScore, latencyScore, quotaScore, errorScore int
		var createdAt sql.NullTime
		if err := rows.Scan(&accountID, &accountName, &accountStatus, &providerID, &providerName, &providerType, &channelID, &channelName, &snapshotID, &qualityStatus, &decision, &qualityScore, &availabilityScore, &latencyScore, &quotaScore, &errorScore, &reasons, &metrics, &createdAt); err != nil {
			return err
		}
		reasonList := jsonStringList(reasons)
		items = append(items, map[string]any{
			"id":                 accountID,
			"account_id":         accountID,
			"account_name":       accountName,
			"account_status":     accountStatus,
			"provider_id":        providerID,
			"provider_name":      providerName,
			"provider_type":      providerType,
			"channel_id":         channelID,
			"channel_name":       channelName,
			"snapshot_id":        snapshotID,
			"quality_status":     qualityStatus,
			"decision":           decision,
			"quality_score":      qualityScore,
			"availability_score": availabilityScore,
			"latency_score":      latencyScore,
			"quota_score":        quotaScore,
			"error_score":        errorScore,
			"reasons":            reasonList,
			"reason_summary":     strings.Join(reasonList, ", "),
			"metrics":            jsonRaw(metrics),
			"created_at":         nullableSQLTime(createdAt),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminRecomputeAccountQuality(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		AccountIDs         []string `json:"account_ids"`
		ApplyActions       *bool    `json:"apply_actions"`
		IsolationThreshold int      `json:"isolation_threshold"`
		WatchThreshold     int      `json:"watch_threshold"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	applyActions := req.ApplyActions != nil && *req.ApplyActions
	isolationThreshold := req.IsolationThreshold
	if isolationThreshold <= 0 {
		isolationThreshold = s.scoreSystemSetting(r.Context(), "account_pool.quality_isolation_threshold", 40)
	}
	watchThreshold := req.WatchThreshold
	if watchThreshold <= 0 {
		watchThreshold = s.scoreSystemSetting(r.Context(), "account_pool.quality_watch_threshold", 70)
	}
	result, err := s.recomputeAccountQuality(r.Context(), normalizeStringList(req.AccountIDs), applyActions, isolationThreshold, watchThreshold, auth.UserID)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "account_quality.recompute", "account", "", r, result)
	writeJSON(w, http.StatusOK, result, nil)
	return nil
}

func (s *Server) adminAccountPoolStrategyEvents(w http.ResponseWriter, r *http.Request, auth authContext) error {
	args := []any{}
	filters := []string{}
	if accountID := strings.TrimSpace(r.URL.Query().Get("account_id")); accountID != "" {
		args = append(args, accountID)
		filters = append(filters, "e.account_id::text = $"+strconv.Itoa(len(args)))
	}
	if action := strings.TrimSpace(r.URL.Query().Get("action")); action != "" {
		args = append(args, action)
		filters = append(filters, "e.action = $"+strconv.Itoa(len(args)))
	}
	where := ""
	if len(filters) > 0 {
		where = "WHERE " + strings.Join(filters, " AND ")
	}
	args = append(args, limitFromRequest(r, 100, 500))
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT e.id::text, COALESCE(e.account_id::text, ''), COALESCE(a.name, ''),
		       COALESCE(e.provider_id::text, ''), COALESCE(p.name, ''),
		       COALESCE(e.channel_id::text, ''), COALESCE(c.name, ''),
		       COALESCE(e.actor_user_id::text, ''), e.event_type, e.action, e.reason,
		       e.previous_status, e.next_status, e.decision, e.quality_score,
		       e.metadata_json::text, e.created_at
		FROM account_pool_strategy_events e
		LEFT JOIN accounts a ON a.id = e.account_id
		LEFT JOIN providers p ON p.id = e.provider_id
		LEFT JOIN channels c ON c.id = e.channel_id
		`+where+`
		ORDER BY e.created_at DESC
		LIMIT $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, accountID, accountName, providerID, providerName, channelID, channelName, actorUserID string
		var eventType, action, reason, previousStatus, nextStatus, decision, metadata string
		var qualityScore sql.NullInt64
		var createdAt time.Time
		if err := rows.Scan(&id, &accountID, &accountName, &providerID, &providerName, &channelID, &channelName, &actorUserID, &eventType, &action, &reason, &previousStatus, &nextStatus, &decision, &qualityScore, &metadata, &createdAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":              id,
			"account_id":      accountID,
			"account_name":    accountName,
			"provider_id":     providerID,
			"provider_name":   providerName,
			"channel_id":      channelID,
			"channel_name":    channelName,
			"actor_user_id":   actorUserID,
			"event_type":      eventType,
			"action":          action,
			"reason":          reason,
			"previous_status": previousStatus,
			"next_status":     nextStatus,
			"decision":        decision,
			"quality_score":   nullableSQLInt(qualityScore),
			"metadata":        jsonRaw(metadata),
			"created_at":      createdAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminAccountQualityAction(w http.ResponseWriter, r *http.Request, auth authContext) error {
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	var req struct {
		Action     string `json:"action"`
		Reason     string `json:"reason"`
		ModelName  string `json:"model_name"`
		Endpoint   string `json:"endpoint"`
		TargetPath string `json:"target_path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	action := strings.TrimSpace(req.Action)
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "manual quality action"
	}
	target, _ := s.loadSingleAccountQualityTarget(r.Context(), accountID)
	previousStatus := target.AccountStatus
	var err error
	switch action {
	case "probe", "test", "check":
		result, err := s.runChannelCheckContext(r.Context(), "", accountID, req.ModelName, req.Endpoint, req.TargetPath)
		if err != nil {
			return err
		}
		audit(r.Context(), s.db, auth.UserID, "admin", "account_quality.probe", "account", accountID, r, result)
		writeJSON(w, http.StatusCreated, result, nil)
		return nil
	case "isolate":
		err = s.applyQualityIsolation(r.Context(), accountID, reason)
	case "cooldown":
		err = s.applyQualityCooldown(r.Context(), accountID, reason, 10*time.Minute)
	case "restore":
		err = s.restoreQualityAccount(r.Context(), accountID)
	default:
		return badRequest("action must be probe, isolate, cooldown, or restore.")
	}
	if err != nil {
		return err
	}
	nextStatus := ""
	if after, loadErr := s.loadSingleAccountQualityTarget(r.Context(), accountID); loadErr == nil {
		nextStatus = after.AccountStatus
		if target.ProviderID == "" {
			target = after
		}
	}
	_ = s.recordAccountPoolStrategyEvent(r.Context(), s.db, accountPoolStrategyEventInput{
		AccountID:      accountID,
		ProviderID:     target.ProviderID,
		ChannelID:      target.ChannelID,
		ActorUserID:    auth.UserID,
		EventType:      "manual_quality_action",
		Action:         action,
		Reason:         reason,
		PreviousStatus: previousStatus,
		NextStatus:     nextStatus,
		Metadata:       map[string]any{"source": "admin"},
	})
	audit(r.Context(), s.db, auth.UserID, "admin", "account_quality."+action, "account", accountID, r, map[string]any{"reason": reason})
	writeJSON(w, http.StatusOK, map[string]any{"account_id": accountID, "action": action, "updated": true}, nil)
	return nil
}

func (s *Server) recomputeAccountQuality(ctx context.Context, accountIDs []string, applyActions bool, isolationThreshold int, watchThreshold int, requestedBy string) (map[string]any, error) {
	if len(accountIDs) > 200 {
		return nil, badRequest("account_ids cannot exceed 200.")
	}
	isolationThreshold = clampThreshold(isolationThreshold, 40)
	watchThreshold = clampThreshold(watchThreshold, 70)
	if watchThreshold <= isolationThreshold {
		watchThreshold = isolationThreshold + 1
	}
	targets, err := s.loadAccountQualityTargets(ctx, accountIDs, 200)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	isolatedCount := 0
	watchCount := 0
	allowCount := 0
	for _, target := range targets {
		result := scoreAccountQuality(target, isolationThreshold, watchThreshold)
		snapshotID, createdAt, err := s.insertAccountQualitySnapshot(ctx, target, result)
		if err != nil {
			return nil, err
		}
		actionApplied := ""
		if applyActions && result.Decision == "isolate" && target.AccountStatus != "disabled" {
			if err := s.applyQualityIsolation(ctx, target.AccountID, "quality score below isolation threshold"); err != nil {
				return nil, err
			}
			actionApplied = "isolate"
			score := result.QualityScore
			_ = s.recordAccountPoolStrategyEvent(ctx, s.db, accountPoolStrategyEventInput{
				AccountID:      target.AccountID,
				ProviderID:     target.ProviderID,
				ChannelID:      target.ChannelID,
				ActorUserID:    requestedBy,
				EventType:      "quality_recompute",
				Action:         "isolate",
				Reason:         "quality score below isolation threshold",
				PreviousStatus: target.AccountStatus,
				NextStatus:     "disabled",
				Decision:       result.Decision,
				QualityScore:   &score,
				Metadata: map[string]any{
					"isolation_threshold": isolationThreshold,
					"watch_threshold":     watchThreshold,
				},
			})
			isolatedCount++
		}
		switch result.Decision {
		case "allow":
			allowCount++
		case "watch":
			watchCount++
		case "isolate":
			if actionApplied == "" {
				isolatedCount++
			}
		}
		items = append(items, map[string]any{
			"id":                 target.AccountID,
			"account_id":         target.AccountID,
			"account_name":       target.AccountName,
			"account_status":     target.AccountStatus,
			"provider_id":        target.ProviderID,
			"provider_name":      target.ProviderName,
			"channel_id":         target.ChannelID,
			"channel_name":       target.ChannelName,
			"snapshot_id":        snapshotID,
			"quality_status":     result.QualityStatus,
			"decision":           result.Decision,
			"quality_score":      result.QualityScore,
			"availability_score": result.AvailabilityScore,
			"latency_score":      result.LatencyScore,
			"quota_score":        result.QuotaScore,
			"error_score":        result.ErrorScore,
			"reasons":            result.Reasons,
			"reason_summary":     strings.Join(result.Reasons, ", "),
			"metrics":            result.Metrics,
			"action_applied":     actionApplied,
			"created_at":         createdAt.UTC().Format(time.RFC3339),
		})
	}
	return map[string]any{
		"total_count":         len(targets),
		"allow_count":         allowCount,
		"watch_count":         watchCount,
		"isolate_count":       isolatedCount,
		"apply_actions":       applyActions,
		"isolation_threshold": isolationThreshold,
		"watch_threshold":     watchThreshold,
		"requested_by":        requestedBy,
		"items":               items,
	}, nil
}

func (s *Server) loadAccountQualityTargets(ctx context.Context, accountIDs []string, limit int) ([]accountQualityTarget, error) {
	args := []any{}
	where := ""
	if len(accountIDs) > 0 {
		parts := make([]string, 0, len(accountIDs))
		for _, id := range accountIDs {
			args = append(args, id)
			parts = append(parts, "$"+strconv.Itoa(len(args)))
		}
		where = "WHERE a.id::text IN (" + strings.Join(parts, ",") + ")"
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id::text, a.name, a.status, p.id::text, p.name, p.provider_type,
		       COALESCE(c.id::text, ''), COALESCE(c.name, ''),
		       COALESCE(ars.active_requests, 0), COALESCE(ars.success_count, 0), COALESCE(ars.failure_count, 0),
		       COALESCE(ars.circuit_state, 'closed'), COALESCE(ars.circuit_failure_count, 0),
		       ars.cooldown_until, COALESCE(ars.last_error, ''),
		       COALESCE(last_test.status, ''), COALESCE(last_test.latency_ms, 0), COALESCE(last_test.error_message, ''), last_test.tested_at,
		       COALESCE(test_agg.total_count, 0), COALESCE(test_agg.failed_count, 0), COALESCE(test_agg.avg_latency_ms, 0),
		       COALESCE(last_quota.remaining::text, last_window.remaining::text, ''),
		       COALESCE(last_quota.limit_value::text, last_window.limit_value, ''),
		       COALESCE(last_quota.status, ''),
		       COALESCE(last_quota.reset_at, last_window.reset_at)
		FROM accounts a
		JOIN providers p ON p.id = a.provider_id
		LEFT JOIN channels c ON c.id = a.channel_id
		LEFT JOIN account_runtime_states ars ON ars.account_id = a.id
		LEFT JOIN LATERAL (
		  SELECT status, latency_ms, error_message, tested_at
		  FROM channel_test_results
		  WHERE account_id = a.id
		  ORDER BY tested_at DESC
		  LIMIT 1
		) last_test ON true
		LEFT JOIN LATERAL (
		  SELECT COUNT(*)::int AS total_count,
		         COUNT(*) FILTER (WHERE status = 'failed')::int AS failed_count,
		         COALESCE(ROUND(AVG(latency_ms))::int, 0) AS avg_latency_ms
		  FROM channel_test_results
		  WHERE account_id = a.id AND tested_at >= now() - interval '24 hours'
		) test_agg ON true
		LEFT JOIN LATERAL (
		  SELECT status, remaining, limit_value, reset_at
		  FROM account_quota_snapshots
		  WHERE account_id = a.id
		  ORDER BY created_at DESC
		  LIMIT 1
		) last_quota ON true
		LEFT JOIN LATERAL (
		  SELECT remaining,
		         CASE
		           WHEN metadata_json->>'limit' ~ '^[0-9]+(\.[0-9]+)?$' THEN metadata_json->>'limit'
		           ELSE ''
		         END AS limit_value,
		         reset_at
		  FROM account_quota_windows
		  WHERE account_id = a.id
		  ORDER BY created_at DESC
		  LIMIT 1
		) last_window ON true
		`+where+`
		ORDER BY a.priority ASC, a.created_at ASC
		LIMIT $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []accountQualityTarget{}
	for rows.Next() {
		var target accountQualityTarget
		if err := rows.Scan(&target.AccountID, &target.AccountName, &target.AccountStatus, &target.ProviderID, &target.ProviderName, &target.ProviderType, &target.ChannelID, &target.ChannelName, &target.ActiveRequests, &target.SuccessCount, &target.FailureCount, &target.CircuitState, &target.CircuitFailureCount, &target.CooldownUntil, &target.LastRuntimeError, &target.LastTestStatus, &target.LastTestLatencyMS, &target.LastTestError, &target.LastTestedAt, &target.RecentTestCount, &target.RecentFailedCount, &target.RecentAvgLatencyMS, &target.QuotaRemaining, &target.QuotaLimit, &target.QuotaStatus, &target.QuotaResetAt); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (s *Server) loadSingleAccountQualityTarget(ctx context.Context, accountID string) (accountQualityTarget, error) {
	targets, err := s.loadAccountQualityTargets(ctx, []string{accountID}, 1)
	if err != nil {
		return accountQualityTarget{}, err
	}
	if len(targets) == 0 {
		return accountQualityTarget{}, sql.ErrNoRows
	}
	return targets[0], nil
}

func scoreAccountQuality(target accountQualityTarget, isolationThreshold int, watchThreshold int) accountQualityResult {
	reasons := []string{}
	availability := 100
	switch target.AccountStatus {
	case "active":
	case "cooldown":
		availability -= 25
		reasons = append(reasons, "account_cooldown")
	case "exhausted":
		availability -= 45
		reasons = append(reasons, "account_exhausted")
	case "disabled":
		availability -= 60
		reasons = append(reasons, "account_disabled")
	default:
		availability -= 20
		reasons = append(reasons, "account_status_unknown")
	}
	if target.ChannelID == "" {
		availability -= 30
		reasons = append(reasons, "no_channel")
	}
	switch target.CircuitState {
	case "open":
		availability -= 40
		reasons = append(reasons, "circuit_open")
	case "half_open":
		availability -= 20
		reasons = append(reasons, "circuit_half_open")
	}
	if target.CooldownUntil.Valid && target.CooldownUntil.Time.After(time.Now()) {
		availability -= 15
		reasons = append(reasons, "cooldown_active")
	}

	latency := 95
	latencyMS := target.RecentAvgLatencyMS
	if latencyMS == 0 {
		latencyMS = target.LastTestLatencyMS
	}
	if target.RecentTestCount == 0 && !target.LastTestedAt.Valid {
		latency = 80
		reasons = append(reasons, "no_health_sample")
	} else if target.LastTestStatus == "failed" {
		latency = 35
		reasons = append(reasons, "last_health_failed")
	} else {
		switch {
		case latencyMS >= 5000:
			latency -= 35
			reasons = append(reasons, "latency_critical")
		case latencyMS >= 2000:
			latency -= 20
			reasons = append(reasons, "latency_high")
		case latencyMS >= 1000:
			latency -= 10
			reasons = append(reasons, "latency_elevated")
		}
	}

	quota := 90
	remaining, hasRemaining := parseNonNegativeFloat(target.QuotaRemaining)
	limitValue, hasLimit := parseNonNegativeFloat(target.QuotaLimit)
	if target.QuotaStatus == "failed" {
		quota = 45
		reasons = append(reasons, "quota_refresh_failed")
	} else if hasRemaining && hasLimit && limitValue > 0 {
		ratio := remaining / limitValue
		switch {
		case remaining <= 0:
			quota = 20
			reasons = append(reasons, "quota_empty")
		case ratio <= 0.1:
			quota = 45
			reasons = append(reasons, "quota_critical")
		case ratio <= 0.2:
			quota = 65
			reasons = append(reasons, "quota_low")
		}
	} else if hasRemaining && remaining <= 0 {
		quota = 25
		reasons = append(reasons, "quota_empty")
	} else if !hasRemaining {
		quota = 75
		reasons = append(reasons, "no_quota_sample")
	}

	errorScore := 100
	if target.RecentTestCount > 0 && target.RecentFailedCount > 0 {
		rate := float64(target.RecentFailedCount) / float64(target.RecentTestCount)
		errorScore -= int(math.Round(rate * 35))
		reasons = append(reasons, "recent_health_failures")
	}
	totalRuntime := target.SuccessCount + target.FailureCount
	if totalRuntime >= 5 && target.FailureCount > 0 {
		rate := float64(target.FailureCount) / float64(totalRuntime)
		errorScore -= int(math.Round(rate * 35))
		reasons = append(reasons, "runtime_failure_rate")
	} else if target.FailureCount > 0 {
		errorScore -= minInt(int(target.FailureCount)*5, 25)
		reasons = append(reasons, "runtime_failures")
	}
	if target.CircuitFailureCount > 0 {
		errorScore -= minInt(target.CircuitFailureCount*5, 20)
	}
	if strings.TrimSpace(target.LastRuntimeError) != "" {
		errorScore -= 10
		reasons = append(reasons, "runtime_error")
	}
	if strings.TrimSpace(target.LastTestError) != "" {
		errorScore -= 8
	}

	availability = clampScore(availability)
	latency = clampScore(latency)
	quota = clampScore(quota)
	errorScore = clampScore(errorScore)
	quality := clampScore(int(math.Round(float64(availability)*0.35 + float64(latency)*0.20 + float64(quota)*0.20 + float64(errorScore)*0.25)))
	decision := "allow"
	qualityStatus := "healthy"
	if quality < isolationThreshold {
		decision = "isolate"
		qualityStatus = "isolated"
	} else if quality < watchThreshold {
		decision = "watch"
		qualityStatus = "degraded"
	}
	return accountQualityResult{
		QualityStatus:     qualityStatus,
		Decision:          decision,
		QualityScore:      quality,
		AvailabilityScore: availability,
		LatencyScore:      latency,
		QuotaScore:        quota,
		ErrorScore:        errorScore,
		Reasons:           reasons,
		Metrics: map[string]any{
			"provider_type":         target.ProviderType,
			"account_status":        target.AccountStatus,
			"active_requests":       target.ActiveRequests,
			"success_count":         target.SuccessCount,
			"failure_count":         target.FailureCount,
			"circuit_state":         target.CircuitState,
			"circuit_failure_count": target.CircuitFailureCount,
			"last_test_status":      target.LastTestStatus,
			"last_test_latency_ms":  target.LastTestLatencyMS,
			"recent_test_count":     target.RecentTestCount,
			"recent_failed_count":   target.RecentFailedCount,
			"recent_avg_latency_ms": target.RecentAvgLatencyMS,
			"quota_remaining":       target.QuotaRemaining,
			"quota_limit":           target.QuotaLimit,
			"quota_status":          target.QuotaStatus,
			"isolation_threshold":   isolationThreshold,
			"watch_threshold":       watchThreshold,
		},
	}
}

func (s *Server) insertAccountQualitySnapshot(ctx context.Context, target accountQualityTarget, result accountQualityResult) (string, time.Time, error) {
	var snapshotID string
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO account_quality_snapshots (
		  account_id, provider_id, channel_id, quality_status, decision, quality_score,
		  availability_score, latency_score, quota_score, error_score, reason_json, metrics_json
		)
		VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12::jsonb)
		RETURNING id::text, created_at
	`, target.AccountID, target.ProviderID, target.ChannelID, result.QualityStatus, result.Decision, result.QualityScore, result.AvailabilityScore, result.LatencyScore, result.QuotaScore, result.ErrorScore, mustEncodeJSON(result.Reasons), mustEncodeJSON(result.Metrics)).Scan(&snapshotID, &createdAt)
	return snapshotID, createdAt, err
}

func (s *Server) applyQualityIsolation(ctx context.Context, accountID string, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE accounts SET status = 'disabled' WHERE id::text = $1
	`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE account_runtime_states
		SET cooldown_until = NULL,
		    last_error = $2,
		    failure_count = failure_count + 1,
		    circuit_state = 'open',
		    circuit_failure_count = circuit_failure_count + 1,
		    circuit_opened_at = now(),
		    circuit_half_open_after = NULL
		WHERE account_id::text = $1
	`, accountID, truncateForStorage(reason, 500)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) applyQualityCooldown(ctx context.Context, accountID string, reason string, duration time.Duration) error {
	if duration <= 0 {
		duration = 10 * time.Minute
	}
	cooldownUntil := time.Now().UTC().Add(duration)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE accounts SET status = 'cooldown' WHERE id::text = $1 AND status <> 'disabled'
	`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE account_runtime_states
		SET cooldown_until = $2,
		    last_error = $3,
		    failure_count = failure_count + 1
		WHERE account_id::text = $1
	`, accountID, cooldownUntil, truncateForStorage(reason, 500)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) restoreQualityAccount(ctx context.Context, accountID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE accounts SET status = 'active' WHERE id::text = $1
	`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE account_runtime_states
		SET cooldown_until = NULL,
		    last_error = '',
		    failure_count = 0,
		    circuit_state = 'closed',
		    circuit_failure_count = 0,
		    circuit_opened_at = NULL,
		    circuit_half_open_after = NULL
		WHERE account_id::text = $1
	`, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) qualityCheckDue(ctx context.Context) bool {
	interval := s.intSystemSetting(ctx, "account_pool.quality_interval_seconds", 300)
	var last sql.NullTime
	if err := s.db.QueryRowContext(ctx, "SELECT max(created_at) FROM account_quality_snapshots").Scan(&last); err != nil {
		return false
	}
	return !last.Valid || time.Since(last.Time) >= time.Duration(interval)*time.Second
}

func (s *Server) scoreSystemSetting(ctx context.Context, key string, fallback int) int {
	value := map[string]any{}
	var raw string
	if err := s.db.QueryRowContext(ctx, "SELECT setting_value_json::text FROM system_settings WHERE setting_key = $1", key).Scan(&raw); err != nil {
		return fallback
	}
	if err := decodeJSONString(raw, &value); err != nil {
		return fallback
	}
	if score, ok := value["score"].(float64); ok && score >= 0 && score <= 100 {
		return int(score)
	}
	return fallback
}

func clampThreshold(value int, fallback int) int {
	if value < 0 || value > 100 {
		return fallback
	}
	return value
}

func clampScore(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func parseNonNegativeFloat(value string) (float64, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}

func (s *Server) recordAccountPoolStrategyEvent(ctx context.Context, exec sqlExecutor, input accountPoolStrategyEventInput) error {
	if strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.Action) == "" {
		return nil
	}
	eventType := strings.TrimSpace(input.EventType)
	if eventType == "" {
		eventType = "account_pool_strategy"
	}
	metadata := input.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	var qualityScore any
	if input.QualityScore != nil {
		qualityScore = *input.QualityScore
	}
	_, err := exec.ExecContext(ctx, `
		INSERT INTO account_pool_strategy_events (
		  account_id, provider_id, channel_id, actor_user_id, event_type, action, reason,
		  previous_status, next_status, decision, quality_score, metadata_json
		)
		VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10, $11, $12::jsonb)
	`, input.AccountID, input.ProviderID, input.ChannelID, input.ActorUserID, eventType, input.Action, truncateForStorage(input.Reason, 500), input.PreviousStatus, input.NextStatus, input.Decision, qualityScore, mustEncodeJSON(metadata))
	return err
}

func jsonStringList(raw string) []string {
	var items []string
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if err := decodeJSONString(raw, &items); err != nil {
		return nil
	}
	return items
}

func decodeJSONString(raw string, target any) error {
	return json.Unmarshal([]byte(raw), target)
}
