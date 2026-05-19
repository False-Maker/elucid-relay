package httpserver

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) adminUsageAnalytics(w http.ResponseWriter, r *http.Request, _ authContext) error {
	groupBy := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("group_by")))
	if groupBy == "" {
		groupBy = "model"
	}
	dimensionExpr, err := usageAnalyticsDimensionExpr(groupBy)
	if err != nil {
		return err
	}
	limit := limitFromRequest(r, 50, 200)
	whereSQL, args, err := usageWhereFromRequest(r, nil, nil)
	if err != nil {
		return err
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT `+dimensionExpr+` AS dimension,
		       COUNT(*)::bigint,
		       COUNT(*) FILTER (WHERE status = 'success')::bigint,
		       COUNT(*) FILTER (WHERE status = 'failed')::bigint,
		       COUNT(*) FILTER (WHERE status = 'rejected')::bigint,
		       COALESCE(SUM(input_tokens), 0)::text,
		       COALESCE(SUM(output_tokens), 0)::text,
		       COALESCE(SUM(image_count), 0)::text,
		       COALESCE(SUM(audio_seconds), 0)::text,
		       COALESCE(SUM(request_count), 0)::text,
		       COALESCE(SUM(actual_cost), 0)::text,
		       COALESCE(ROUND(AVG(duration_ms))::text, '0'),
		       COALESCE(SUM(stream_event_count), 0)::text,
		       COALESCE(SUM(websocket_frame_count), 0)::text
		FROM usage_records
		`+whereSQL+`
		GROUP BY 1
		ORDER BY COUNT(*) DESC
		LIMIT $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var dimension, inputTokens, outputTokens, imageCount, audioSeconds, requestCount, actualCost, avgDuration, streamEvents, websocketFrames string
		var total, success, failed, rejected int64
		if err := rows.Scan(&dimension, &total, &success, &failed, &rejected, &inputTokens, &outputTokens, &imageCount, &audioSeconds, &requestCount, &actualCost, &avgDuration, &streamEvents, &websocketFrames); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"dimension":             dimension,
			"group_by":              groupBy,
			"request_count":         total,
			"success_count":         success,
			"failed_count":          failed,
			"rejected_count":        rejected,
			"input_tokens":          inputTokens,
			"output_tokens":         outputTokens,
			"image_count":           imageCount,
			"audio_seconds":         audioSeconds,
			"metered_request_count": requestCount,
			"actual_cost":           actualCost,
			"avg_duration_ms":       avgDuration,
			"stream_event_count":    streamEvents,
			"websocket_frame_count": websocketFrames,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func usageAnalyticsDimensionExpr(groupBy string) (string, error) {
	switch groupBy {
	case "model":
		return "COALESCE(NULLIF(requested_model, ''), 'unknown')", nil
	case "upstream_model":
		return "COALESCE(NULLIF(upstream_model, ''), 'unknown')", nil
	case "user":
		return "COALESCE(user_id::text, 'unknown')", nil
	case "api_key":
		return "COALESCE(api_key_id::text, 'unknown')", nil
	case "channel":
		return "COALESCE(channel_id::text, 'unknown')", nil
	case "account":
		return "COALESCE(account_id::text, 'unknown')", nil
	case "endpoint":
		return "COALESCE(NULLIF(endpoint, ''), 'unknown')", nil
	case "status":
		return "COALESCE(NULLIF(status, ''), 'unknown')", nil
	case "error_code":
		return "COALESCE(NULLIF(error_code, ''), 'none')", nil
	default:
		return "", badRequest("Invalid usage analytics group_by.")
	}
}

func (s *Server) adminAffinityStats(w http.ResponseWriter, r *http.Request, _ authContext) error {
	where, args := affinityStatsWhere(r)
	limit := limitFromRequest(r, 50, 200)
	args = append(args, limit)
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT COALESCE(NULLIF(rule_name, ''), 'default') AS rule_name,
		       model_name,
		       endpoint,
		       COUNT(*)::bigint,
		       COUNT(*) FILTER (WHERE expires_at > now())::bigint,
		       COUNT(*) FILTER (WHERE expires_at <= now())::bigint,
		       COALESCE(SUM(hit_count), 0)::bigint,
		       COALESCE(SUM(miss_count), 0)::bigint,
		       MAX(last_seen_at),
		       MAX(last_hit_at),
		       MAX(last_miss_at)
		FROM northbound_route_affinities
		`+where+`
		GROUP BY 1, 2, 3
		ORDER BY COUNT(*) FILTER (WHERE expires_at > now()) DESC, COALESCE(SUM(hit_count), 0) DESC, COUNT(*) DESC
		LIMIT $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var ruleName, model, endpoint string
		var bindingCount, activeCount, expiredCount, totalHits, totalMisses int64
		var lastSeen, lastHit, lastMiss sql.NullTime
		if err := rows.Scan(&ruleName, &model, &endpoint, &bindingCount, &activeCount, &expiredCount, &totalHits, &totalMisses, &lastSeen, &lastHit, &lastMiss); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"rule_name":     ruleName,
			"model_name":    model,
			"endpoint":      endpoint,
			"binding_count": bindingCount,
			"active_count":  activeCount,
			"expired_count": expiredCount,
			"total_hits":    totalHits,
			"total_misses":  totalMisses,
			"last_seen_at":  nullTimeRFC3339(lastSeen),
			"last_hit_at":   nullTimeRFC3339(lastHit),
			"last_miss_at":  nullTimeRFC3339(lastMiss),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminAffinityCleanup(w http.ResponseWriter, r *http.Request, _ authContext) error {
	var req struct {
		RuleName    string `json:"rule_name"`
		ModelName   string `json:"model_name"`
		Endpoint    string `json:"endpoint"`
		APIKeyID    string `json:"api_key_id"`
		ExpiredOnly bool   `json:"expired_only"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	conditions := []string{}
	args := []any{}
	add := func(condition string, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(condition, len(args)))
	}
	add("rule_name = $%d", req.RuleName)
	add("model_name = $%d", req.ModelName)
	add("endpoint = $%d", req.Endpoint)
	add("api_key_id::text = $%d", req.APIKeyID)
	if req.ExpiredOnly {
		conditions = append(conditions, "expires_at <= now()")
	}
	if len(conditions) == 0 {
		return badRequest("Affinity cleanup requires expired_only or at least one filter.")
	}
	result, err := s.db.ExecContext(r.Context(), "DELETE FROM northbound_route_affinities WHERE "+strings.Join(conditions, " AND "), args...)
	if err != nil {
		return err
	}
	deleted, _ := result.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted}, nil)
	return nil
}

func affinityStatsWhere(r *http.Request) (string, []any) {
	query := r.URL.Query()
	conditions := []string{}
	args := []any{}
	add := func(column string, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	add("rule_name", query.Get("rule_name"))
	add("model_name", query.Get("model"))
	add("endpoint", query.Get("endpoint"))
	if value := strings.TrimSpace(query.Get("api_key_id")); value != "" {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf("api_key_id::text = $%d", len(args)))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func (s *Server) portalAPIKeyUsage(w http.ResponseWriter, r *http.Request, auth authContext) error {
	apiKeyID := strings.TrimSpace(r.PathValue("apiKeyId"))
	if apiKeyID == "" {
		return badRequest("api_key_id is required.")
	}
	var exists bool
	if err := s.db.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM api_keys WHERE id = $1::uuid AND user_id = $2::uuid)", apiKeyID, auth.UserID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return notFound("API key not found.")
	}
	whereSQL, args, err := usageWhereFromRequest(r, []string{"user_id = $1::uuid", "api_key_id = $2::uuid"}, []any{auth.UserID, apiKeyID})
	if err != nil {
		return err
	}
	summary, err := s.usageSummaryFromWhere(r.Context(), whereSQL, args)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, summary, nil)
	return nil
}

func (s *Server) northboundKeyUsage(w http.ResponseWriter, r *http.Request) {
	auth, err := s.authenticateAPIKey(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	whereSQL, args, err := usageWhereFromRequest(r, []string{"api_key_id = $1::uuid"}, []any{auth.APIKeyID})
	if err != nil {
		writeError(w, r, err)
		return
	}
	summary, err := s.usageSummaryFromWhere(r.Context(), whereSQL, args)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeRawJSON(w, http.StatusOK, map[string]any{
		"object":     "usage_summary",
		"api_key_id": auth.APIKeyID,
		"user_id":    auth.UserID,
		"wallet": map[string]any{
			"id":               auth.WalletID,
			"balance":          auth.Balance,
			"reserved_balance": auth.ReservedBalance,
			"currency":         "USD",
		},
		"usage": summary,
	})
}

func (s *Server) usageSummaryFromWhere(ctx context.Context, whereSQL string, args []any) (map[string]any, error) {
	var total, success, failed, rejected int64
	var cost, inputTokens, outputTokens, imageCount, audioSeconds, requestCount, avgDuration, streamEvents, websocketFrames sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE status = 'success'),
		       COUNT(*) FILTER (WHERE status = 'failed'),
		       COUNT(*) FILTER (WHERE status = 'rejected'),
		       COALESCE(SUM(actual_cost), 0)::text,
		       COALESCE(SUM(input_tokens), 0)::text,
		       COALESCE(SUM(output_tokens), 0)::text,
		       COALESCE(SUM(image_count), 0)::text,
		       COALESCE(SUM(audio_seconds), 0)::text,
		       COALESCE(SUM(request_count), 0)::text,
		       COALESCE(ROUND(AVG(duration_ms))::text, '0'),
		       COALESCE(SUM(stream_event_count), 0)::text,
		       COALESCE(SUM(websocket_frame_count), 0)::text
		FROM usage_records
		`+whereSQL, args...).Scan(&total, &success, &failed, &rejected, &cost, &inputTokens, &outputTokens, &imageCount, &audioSeconds, &requestCount, &avgDuration, &streamEvents, &websocketFrames); err != nil {
		return nil, err
	}
	return map[string]any{
		"total":                 total,
		"success":               success,
		"failed":                failed,
		"rejected":              rejected,
		"actual_cost":           nullStringValue(cost, "0"),
		"input_tokens":          nullStringValue(inputTokens, "0"),
		"output_tokens":         nullStringValue(outputTokens, "0"),
		"image_count":           nullStringValue(imageCount, "0"),
		"audio_seconds":         nullStringValue(audioSeconds, "0"),
		"request_count":         nullStringValue(requestCount, "0"),
		"avg_duration_ms":       nullStringValue(avgDuration, "0"),
		"stream_event_count":    nullStringValue(streamEvents, "0"),
		"websocket_frame_count": nullStringValue(websocketFrames, "0"),
	}, nil
}

func nullTimeRFC3339(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return value.Time.UTC().Format(time.RFC3339)
}
