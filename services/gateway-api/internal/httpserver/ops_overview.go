package httpserver

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"
)

func (s *Server) adminRuntimeOverview(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		WITH recent_usage AS (
			SELECT
				COUNT(*) AS total_requests,
				COUNT(*) FILTER (WHERE status = 'failed') AS failed_requests,
				COUNT(*) FILTER (WHERE status = 'rejected') AS rejected_requests,
				COUNT(*) FILTER (WHERE error_code = 'upstream_rejected' AND created_at > now() - interval '1 hour') AS upstream_rejected_last_hour,
				COUNT(*) FILTER (WHERE error_code = 'upstream_error' AND created_at > now() - interval '1 hour') AS upstream_error_last_hour,
				COALESCE(SUM(actual_cost), 0)::text AS settled_cost,
				COALESCE(SUM(CASE WHEN status = 'failed' THEN estimated_cost ELSE 0 END), 0)::text AS reserved_failed_cost
			FROM usage_records
			WHERE created_at > now() - interval '24 hours'
		),
		active_accounts AS (
			SELECT COUNT(*) AS active_accounts
			FROM account_runtime_states
			WHERE active_requests > 0
		),
		cooldown_accounts AS (
			SELECT COUNT(*) AS cooldown_accounts
			FROM account_runtime_states
			WHERE cooldown_until IS NOT NULL AND cooldown_until > now()
		),
		circuit_accounts AS (
			SELECT
				COUNT(*) FILTER (WHERE circuit_state = 'open') AS circuit_open_accounts,
				COUNT(*) FILTER (WHERE circuit_state = 'half_open') AS circuit_half_open_accounts
			FROM account_runtime_states
		),
		failed_settlements AS (
			SELECT COUNT(*) AS failed_settlements
			FROM usage_records ur
			WHERE ur.status = 'success'
			  AND ur.actual_cost > 0
			  AND ur.created_at > now() - interval '24 hours'
			  AND NOT EXISTS (
			    SELECT 1
			    FROM wallet_ledgers wl
			    WHERE wl.reference_type = 'northbound_request'
			      AND wl.reference_id = ur.request_id
			      AND wl.entry_type = 'debit'
			  )
		)
		SELECT 'total_requests' AS key, total_requests::text AS value FROM recent_usage
		UNION ALL SELECT 'failed_requests', failed_requests::text FROM recent_usage
		UNION ALL SELECT 'rejected_requests', rejected_requests::text FROM recent_usage
		UNION ALL SELECT 'upstream_rejected_last_hour', upstream_rejected_last_hour::text FROM recent_usage
		UNION ALL SELECT 'upstream_error_last_hour', upstream_error_last_hour::text FROM recent_usage
		UNION ALL SELECT 'settled_cost', settled_cost FROM recent_usage
		UNION ALL SELECT 'reserved_failed_cost', reserved_failed_cost FROM recent_usage
		UNION ALL SELECT 'active_accounts', active_accounts::text FROM active_accounts
		UNION ALL SELECT 'cooldown_accounts', cooldown_accounts::text FROM cooldown_accounts
		UNION ALL SELECT 'circuit_open_accounts', circuit_open_accounts::text FROM circuit_accounts
		UNION ALL SELECT 'circuit_half_open_accounts', circuit_half_open_accounts::text FROM circuit_accounts
		UNION ALL SELECT 'failed_settlements', failed_settlements::text FROM failed_settlements
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	items, err := scanKeyValueRows(rows)
	if err != nil {
		return err
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"window":  "24h",
		"metrics": items,
	}, nil)
	return nil
}

func (s *Server) adminOpsOverview(w http.ResponseWriter, r *http.Request, auth authContext) error {
	timeRange, interval, trendBucket, trendStep := opsTimeRange(r)

	readyCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	readyStatus, readyChecks, _ := s.readinessState(readyCtx)

	summary, err := s.opsSummary(r.Context(), interval)
	if err != nil {
		return err
	}
	throughput, err := s.opsThroughputTrend(r.Context(), interval, trendBucket, trendStep)
	if err != nil {
		return err
	}
	latency, err := s.opsLatencyDistribution(r.Context(), interval)
	if err != nil {
		return err
	}
	errorsByCode, err := s.opsErrorDistribution(r.Context(), interval)
	if err != nil {
		return err
	}
	accountHealth, err := s.opsAccountHealth(r.Context())
	if err != nil {
		return err
	}
	recentFailures, err := s.opsRecentFailures(r.Context(), interval)
	if err != nil {
		return err
	}
	paymentEvents, err := s.opsPaymentEvents(r.Context())
	if err != nil {
		return err
	}
	notificationEvents, err := s.opsNotificationEvents(r.Context())
	if err != nil {
		return err
	}
	riskEvents, err := s.opsRiskEvents(r.Context())
	if err != nil {
		return err
	}
	poolEvents, err := s.opsPoolEvents(r.Context())
	if err != nil {
		return err
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"time_range":   timeRange,
		"refreshed_at": time.Now().UTC().Format(time.RFC3339),
		"readiness": map[string]any{
			"status": readyStatus,
			"checks": readyChecks,
		},
		"summary":              summary,
		"throughput_trend":     throughput,
		"latency_distribution": latency,
		"error_distribution":   errorsByCode,
		"account_health":       accountHealth,
		"recent_failures":      recentFailures,
		"events": map[string]any{
			"payments":      paymentEvents,
			"notifications": notificationEvents,
			"risk":          riskEvents,
			"pool":          poolEvents,
		},
	}, nil)
	return nil
}

func opsTimeRange(r *http.Request) (string, string, string, string) {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("time_range"))) {
	case "1h":
		return "1h", "1 hour", "hour", "1 hour"
	case "6h":
		return "6h", "6 hours", "hour", "1 hour"
	case "7d":
		return "7d", "7 days", "day", "1 day"
	default:
		return "24h", "24 hours", "hour", "1 hour"
	}
}

func (s *Server) opsSummary(ctx context.Context, interval string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH recent_usage AS (
			SELECT
				COUNT(*) AS total_requests,
				COUNT(*) FILTER (WHERE status = 'success') AS success_requests,
				COUNT(*) FILTER (WHERE status = 'failed') AS failed_requests,
				COUNT(*) FILTER (WHERE status = 'rejected') AS rejected_requests,
				COUNT(*) FILTER (WHERE error_code = 'upstream_rejected' AND created_at > now() - interval '1 hour') AS upstream_rejected_last_hour,
				COUNT(*) FILTER (WHERE error_code = 'upstream_error' AND created_at > now() - interval '1 hour') AS upstream_error_last_hour,
				COALESCE(SUM(input_tokens), 0)::text AS input_tokens,
				COALESCE(SUM(output_tokens), 0)::text AS output_tokens,
				COALESCE(SUM(actual_cost), 0)::text AS actual_cost,
				COALESCE(ROUND(AVG(duration_ms))::text, '0') AS avg_duration_ms
			FROM usage_records
			WHERE created_at > now() - $1::interval
		),
		account_runtime AS (
			SELECT
				COUNT(*) FILTER (WHERE active_requests > 0) AS runtime_active_accounts,
				COALESCE(SUM(active_requests), 0) AS active_requests,
				COUNT(*) FILTER (WHERE cooldown_until IS NOT NULL AND cooldown_until > now()) AS cooldown_accounts,
				COUNT(*) FILTER (WHERE circuit_state = 'open') AS circuit_open_accounts,
				COUNT(*) FILTER (WHERE circuit_state = 'half_open') AS circuit_half_open_accounts
			FROM account_runtime_states
		),
		account_status AS (
			SELECT
				COUNT(*) AS total_accounts,
				COUNT(*) FILTER (WHERE status = 'active') AS active_accounts,
				COUNT(*) FILTER (WHERE status = 'cooldown') AS cooldown_status_accounts,
				COUNT(*) FILTER (WHERE status = 'disabled') AS disabled_accounts,
				COUNT(*) FILTER (WHERE status = 'exhausted') AS exhausted_accounts
			FROM accounts
		),
		channel_status AS (
			SELECT
				COUNT(*) AS total_channels,
				COUNT(*) FILTER (WHERE status = 'active') AS active_channels,
				COUNT(*) FILTER (WHERE status = 'cooldown') AS cooldown_channels,
				COUNT(*) FILTER (WHERE status = 'disabled') AS disabled_channels
			FROM channels
		),
		event_stats AS (
			SELECT
				(SELECT COUNT(*) FROM payment_events WHERE status IN ('pending', 'processing', 'failed')) AS payment_attention,
				(SELECT COUNT(*) FROM notification_events WHERE status IN ('pending', 'failed', 'suppressed')) AS notification_attention,
				(SELECT COUNT(*) FROM risk_events WHERE created_at > now() - $1::interval) AS risk_events,
				(SELECT COUNT(*) FROM account_pool_strategy_events WHERE created_at > now() - $1::interval) AS pool_events
		),
		failed_settlements AS (
			SELECT COUNT(*) AS failed_settlements
			FROM usage_records ur
			WHERE ur.status = 'success'
			  AND ur.actual_cost > 0
			  AND ur.created_at > now() - $1::interval
			  AND NOT EXISTS (
			    SELECT 1
			    FROM wallet_ledgers wl
			    WHERE wl.reference_type = 'northbound_request'
			      AND wl.reference_id = ur.request_id
			      AND wl.entry_type = 'debit'
			  )
		)
		SELECT 'total_requests', total_requests::text FROM recent_usage
		UNION ALL SELECT 'success_requests', success_requests::text FROM recent_usage
		UNION ALL SELECT 'failed_requests', failed_requests::text FROM recent_usage
		UNION ALL SELECT 'rejected_requests', rejected_requests::text FROM recent_usage
		UNION ALL SELECT 'upstream_rejected_last_hour', upstream_rejected_last_hour::text FROM recent_usage
		UNION ALL SELECT 'upstream_error_last_hour', upstream_error_last_hour::text FROM recent_usage
		UNION ALL SELECT 'input_tokens', input_tokens FROM recent_usage
		UNION ALL SELECT 'output_tokens', output_tokens FROM recent_usage
		UNION ALL SELECT 'actual_cost', actual_cost FROM recent_usage
		UNION ALL SELECT 'avg_duration_ms', avg_duration_ms FROM recent_usage
		UNION ALL SELECT 'runtime_active_accounts', runtime_active_accounts::text FROM account_runtime
		UNION ALL SELECT 'active_requests', active_requests::text FROM account_runtime
		UNION ALL SELECT 'cooldown_accounts', cooldown_accounts::text FROM account_runtime
		UNION ALL SELECT 'circuit_open_accounts', circuit_open_accounts::text FROM account_runtime
		UNION ALL SELECT 'circuit_half_open_accounts', circuit_half_open_accounts::text FROM account_runtime
		UNION ALL SELECT 'total_accounts', total_accounts::text FROM account_status
		UNION ALL SELECT 'active_accounts', active_accounts::text FROM account_status
		UNION ALL SELECT 'cooldown_status_accounts', cooldown_status_accounts::text FROM account_status
		UNION ALL SELECT 'disabled_accounts', disabled_accounts::text FROM account_status
		UNION ALL SELECT 'exhausted_accounts', exhausted_accounts::text FROM account_status
		UNION ALL SELECT 'total_channels', total_channels::text FROM channel_status
		UNION ALL SELECT 'active_channels', active_channels::text FROM channel_status
		UNION ALL SELECT 'cooldown_channels', cooldown_channels::text FROM channel_status
		UNION ALL SELECT 'disabled_channels', disabled_channels::text FROM channel_status
		UNION ALL SELECT 'payment_attention', payment_attention::text FROM event_stats
		UNION ALL SELECT 'notification_attention', notification_attention::text FROM event_stats
		UNION ALL SELECT 'risk_events', risk_events::text FROM event_stats
		UNION ALL SELECT 'pool_events', pool_events::text FROM event_stats
		UNION ALL SELECT 'failed_settlements', failed_settlements::text FROM failed_settlements
	`, interval)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKeyValueRows(rows)
}

func (s *Server) opsThroughputTrend(ctx context.Context, interval string, bucket string, step string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH bounds AS (
			SELECT date_trunc($2, now() - $1::interval) AS start_at,
			       date_trunc($2, now()) AS end_at
		),
		series AS (
			SELECT generate_series(start_at, end_at, $3::interval) AS bucket
			FROM bounds
		),
		usage AS (
			SELECT date_trunc($2, created_at) AS bucket,
			       COUNT(*) AS total,
			       COUNT(*) FILTER (WHERE status = 'success') AS success,
			       COUNT(*) FILTER (WHERE status = 'failed') AS failed,
			       COUNT(*) FILTER (WHERE status = 'rejected') AS rejected,
			       COALESCE(ROUND(AVG(duration_ms))::text, '0') AS avg_duration_ms,
			       COALESCE(SUM(actual_cost), 0)::text AS cost
			FROM usage_records
			WHERE created_at > now() - $1::interval
			GROUP BY 1
		)
		SELECT s.bucket,
		       COALESCE(u.total, 0),
		       COALESCE(u.success, 0),
		       COALESCE(u.failed, 0),
		       COALESCE(u.rejected, 0),
		       COALESCE(u.avg_duration_ms, '0'),
		       COALESCE(u.cost, '0')
		FROM series s
		LEFT JOIN usage u ON u.bucket = s.bucket
		ORDER BY s.bucket ASC
	`, interval, bucket, step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var bucket time.Time
		var total, success, failed, rejected int64
		var avgDuration, cost string
		if err := rows.Scan(&bucket, &total, &success, &failed, &rejected, &avgDuration, &cost); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"bucket":          bucket.UTC().Format(time.RFC3339),
			"total":           total,
			"success":         success,
			"failed":          failed,
			"rejected":        rejected,
			"avg_duration_ms": avgDuration,
			"cost":            cost,
		})
	}
	return items, rows.Err()
}

func (s *Server) opsLatencyDistribution(ctx context.Context, interval string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH bucketed AS (
			SELECT CASE
			         WHEN duration_ms < 500 THEN '<500ms'
			         WHEN duration_ms < 1000 THEN '0.5-1s'
			         WHEN duration_ms < 3000 THEN '1-3s'
			         WHEN duration_ms < 10000 THEN '3-10s'
			         ELSE '10s+'
			       END AS bucket,
			       CASE
			         WHEN duration_ms < 500 THEN 1
			         WHEN duration_ms < 1000 THEN 2
			         WHEN duration_ms < 3000 THEN 3
			         WHEN duration_ms < 10000 THEN 4
			         ELSE 5
			       END AS sort_key
			FROM usage_records
			WHERE created_at > now() - $1::interval
			  AND duration_ms > 0
		)
		SELECT bucket, COUNT(*) AS count
		FROM bucketed
		GROUP BY bucket, sort_key
		ORDER BY sort_key ASC
	`, interval)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLabelCountRows(rows)
}

func (s *Server) opsErrorDistribution(ctx context.Context, interval string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(error_code, ''), 'unknown') AS label,
		       COUNT(*) AS count
		FROM usage_records
		WHERE created_at > now() - $1::interval
		  AND status <> 'success'
		GROUP BY label
		ORDER BY count DESC, label ASC
		LIMIT 10
	`, interval)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLabelCountRows(rows)
}

func (s *Server) opsAccountHealth(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id::text,
		       a.name,
		       a.status,
		       COALESCE(p.name, ''),
		       COALESCE(p.provider_type, ''),
		       COALESCE(c.name, ''),
		       COALESCE(ars.active_requests, 0),
		       COALESCE(ars.circuit_state, 'closed'),
		       ars.cooldown_until,
		       COALESCE(ars.last_error, ''),
		       ars.updated_at
		FROM accounts a
		LEFT JOIN providers p ON p.id = a.provider_id
		LEFT JOIN channels c ON c.id = a.channel_id
		LEFT JOIN account_runtime_states ars ON ars.account_id = a.id
		ORDER BY
		  CASE
		    WHEN COALESCE(ars.circuit_state, 'closed') = 'open' THEN 0
		    WHEN a.status IN ('cooldown', 'exhausted') THEN 1
		    WHEN ars.cooldown_until IS NOT NULL AND ars.cooldown_until > now() THEN 1
		    WHEN COALESCE(ars.active_requests, 0) > 0 THEN 2
		    ELSE 3
		  END,
		  ars.updated_at DESC NULLS LAST,
		  a.created_at DESC
		LIMIT 20
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, status, providerName, providerType, channelName, circuitState, lastError string
		var activeRequests int
		var cooldownUntil, updatedAt sql.NullTime
		if err := rows.Scan(&id, &name, &status, &providerName, &providerType, &channelName, &activeRequests, &circuitState, &cooldownUntil, &lastError, &updatedAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":              id,
			"name":            name,
			"status":          status,
			"provider_name":   providerName,
			"provider_type":   providerType,
			"channel_name":    channelName,
			"active_requests": activeRequests,
			"circuit_state":   circuitState,
			"cooldown_until":  nullableSQLTime(cooldownUntil),
			"last_error":      lastError,
			"updated_at":      nullableSQLTime(updatedAt),
		})
	}
	return items, rows.Err()
}

func (s *Server) opsRecentFailures(ctx context.Context, interval string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, request_id, requested_model, endpoint, status, error_code,
		       upstream_status, duration_ms, actual_cost::text, created_at
		FROM usage_records
		WHERE created_at > now() - $1::interval
		  AND status <> 'success'
		ORDER BY created_at DESC
		LIMIT 20
	`, interval)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, requestID, requestedModel, endpoint, status, errorCode, actualCost string
		var upstreamStatus sql.NullInt64
		var durationMS int
		var createdAt time.Time
		if err := rows.Scan(&id, &requestID, &requestedModel, &endpoint, &status, &errorCode, &upstreamStatus, &durationMS, &actualCost, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":              id,
			"request_id":      requestID,
			"requested_model": requestedModel,
			"endpoint":        endpoint,
			"status":          status,
			"error_code":      errorCode,
			"upstream_status": nullableSQLInt(upstreamStatus),
			"duration_ms":     durationMS,
			"actual_cost":     actualCost,
			"created_at":      createdAt.UTC().Format(time.RFC3339),
		})
	}
	return items, rows.Err()
}

func (s *Server) opsPaymentEvents(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, status, event_type, provider, COALESCE(order_id::text, ''),
		       attempts, COALESCE(NULLIF(last_error, ''), processing_error, ''), created_at
		FROM payment_events
		WHERE status IN ('pending', 'processing', 'failed')
		ORDER BY created_at DESC
		LIMIT 10
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, status, eventType, provider, orderID, lastError string
		var attempts int
		var createdAt time.Time
		if err := rows.Scan(&id, &status, &eventType, &provider, &orderID, &attempts, &lastError, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":         id,
			"status":     status,
			"event_type": eventType,
			"provider":   provider,
			"order_id":   orderID,
			"attempts":   attempts,
			"last_error": lastError,
			"created_at": createdAt.UTC().Format(time.RFC3339),
		})
	}
	return items, rows.Err()
}

func (s *Server) opsNotificationEvents(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, status, severity, event_type, title, attempts, last_error, created_at
		FROM notification_events
		WHERE status IN ('pending', 'failed', 'suppressed')
		ORDER BY created_at DESC
		LIMIT 10
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, status, severity, eventType, title, lastError string
		var attempts int
		var createdAt time.Time
		if err := rows.Scan(&id, &status, &severity, &eventType, &title, &attempts, &lastError, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":         id,
			"status":     status,
			"severity":   severity,
			"event_type": eventType,
			"title":      title,
			"attempts":   attempts,
			"last_error": lastError,
			"created_at": createdAt.UTC().Format(time.RFC3339),
		})
	}
	return items, rows.Err()
}

func (s *Server) opsRiskEvents(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, request_id, rule_type, action, severity, target, matched_value, created_at
		FROM risk_events
		ORDER BY created_at DESC
		LIMIT 10
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, requestID, ruleType, action, severity, target, matchedValue string
		var createdAt time.Time
		if err := rows.Scan(&id, &requestID, &ruleType, &action, &severity, &target, &matchedValue, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":            id,
			"request_id":    requestID,
			"rule_type":     ruleType,
			"action":        action,
			"severity":      severity,
			"target":        target,
			"matched_value": matchedValue,
			"created_at":    createdAt.UTC().Format(time.RFC3339),
		})
	}
	return items, rows.Err()
}

func (s *Server) opsPoolEvents(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id::text, COALESCE(a.name, ''), COALESCE(p.name, ''), COALESCE(c.name, ''),
		       e.event_type, e.action, e.reason, e.previous_status, e.next_status, e.decision, e.created_at
		FROM account_pool_strategy_events e
		LEFT JOIN accounts a ON a.id = e.account_id
		LEFT JOIN providers p ON p.id = e.provider_id
		LEFT JOIN channels c ON c.id = e.channel_id
		ORDER BY e.created_at DESC
		LIMIT 10
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, accountName, providerName, channelName, eventType, action, reason, previousStatus, nextStatus, decision string
		var createdAt time.Time
		if err := rows.Scan(&id, &accountName, &providerName, &channelName, &eventType, &action, &reason, &previousStatus, &nextStatus, &decision, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":              id,
			"account_name":    accountName,
			"provider_name":   providerName,
			"channel_name":    channelName,
			"event_type":      eventType,
			"action":          action,
			"reason":          reason,
			"previous_status": previousStatus,
			"next_status":     nextStatus,
			"decision":        decision,
			"created_at":      createdAt.UTC().Format(time.RFC3339),
		})
	}
	return items, rows.Err()
}

func scanKeyValueRows(rows *sql.Rows) (map[string]string, error) {
	items := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		items[key] = value
	}
	return items, rows.Err()
}

func scanLabelCountRows(rows *sql.Rows) ([]map[string]any, error) {
	items := []map[string]any{}
	for rows.Next() {
		var label string
		var count int64
		if err := rows.Scan(&label, &count); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":    strings.ToLower(strings.ReplaceAll(label, " ", "_")),
			"label": label,
			"count": count,
		})
	}
	return items, rows.Err()
}

func (s *Server) readinessState(ctx context.Context) (string, map[string]string, int) {
	checks := map[string]string{}
	status := "ok"
	httpStatus := http.StatusOK

	if s.db == nil {
		checks["database"] = "missing"
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	} else if err := s.db.PingContext(ctx); err != nil {
		checks["database"] = "error"
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	} else {
		checks["database"] = "ok"
	}

	if s.redis == nil {
		checks["redis"] = "missing"
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	} else if err := s.redis.Ping(ctx).Err(); err != nil {
		checks["redis"] = "error"
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	} else {
		checks["redis"] = "ok"
	}

	if err := s.cfg.ValidateForServe(); err != nil {
		checks["config"] = "error"
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	} else {
		checks["config"] = "ok"
	}

	return status, checks, httpStatus
}
