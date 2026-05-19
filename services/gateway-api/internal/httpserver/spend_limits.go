package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type spendLimitRecord struct {
	ID                  string
	TargetType          string
	TargetID            string
	DailyUSDLimit       sql.NullString
	MonthlyUSDLimit     sql.NullString
	DailyRequestLimit   sql.NullInt64
	MonthlyRequestLimit sql.NullInt64
	LowBalanceThreshold sql.NullString
	Status              string
	Metadata            string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type spendLimitRequest struct {
	TargetType          string         `json:"target_type"`
	TargetID            string         `json:"target_id"`
	DailyUSDLimit       *string        `json:"daily_usd_limit"`
	MonthlyUSDLimit     *string        `json:"monthly_usd_limit"`
	DailyRequestLimit   *int           `json:"daily_request_limit"`
	MonthlyRequestLimit *int           `json:"monthly_request_limit"`
	LowBalanceThreshold *string        `json:"low_balance_threshold"`
	Status              *string        `json:"status"`
	Metadata            map[string]any `json:"metadata"`
}

func (s *Server) portalSpendLimits(w http.ResponseWriter, r *http.Request, auth authContext) error {
	userLimit, err := s.spendLimitForTarget(r.Context(), "user", auth.UserID)
	if err != nil {
		return err
	}
	keyLimits, err := s.spendLimitsForUserAPIKeys(r.Context(), auth.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":     nullableSpendLimit(userLimit),
		"api_keys": keyLimits,
	}, nil)
	return nil
}

func (s *Server) portalPutAPIKeySpendLimit(w http.ResponseWriter, r *http.Request, auth authContext) error {
	apiKeyID := r.PathValue("apiKeyId")
	var exists bool
	if err := s.db.QueryRowContext(r.Context(), `
		SELECT EXISTS (SELECT 1 FROM api_keys WHERE id = $1 AND owner_id = $2)
	`, apiKeyID, auth.UserID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return notFound("API key was not found.")
	}
	var req spendLimitRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	record, err := s.upsertSpendLimit(r.Context(), "api_key", apiKeyID, req)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "personal_user", "spend_limit.update", "api_key", apiKeyID, r, nil)
	writeJSON(w, http.StatusOK, spendLimitResponse(record), nil)
	return nil
}

func (s *Server) adminSpendLimits(w http.ResponseWriter, r *http.Request, auth authContext) error {
	targetType := strings.TrimSpace(r.URL.Query().Get("target_type"))
	targetID := strings.TrimSpace(r.URL.Query().Get("target_id"))
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, target_type, target_id::text,
		       daily_usd_limit::text, monthly_usd_limit::text,
		       daily_request_limit, monthly_request_limit, low_balance_threshold::text,
		       status, metadata_json::text, created_at, updated_at
		FROM spend_limits
		WHERE ($1 = '' OR target_type = $1)
		  AND ($2 = '' OR target_id::text = $2)
		ORDER BY updated_at DESC
		LIMIT $3
	`, targetType, targetID, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		record, err := scanSpendLimit(rows)
		if err != nil {
			return err
		}
		items = append(items, spendLimitResponse(record))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminPutSpendLimit(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req spendLimitRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	targetType := strings.TrimSpace(req.TargetType)
	targetID := strings.TrimSpace(req.TargetID)
	if targetType != "user" && targetType != "api_key" {
		return badRequest("target_type must be user or api_key.")
	}
	if targetID == "" {
		return badRequest("target_id is required.")
	}
	if err := s.validateSpendLimitTarget(r.Context(), targetType, targetID); err != nil {
		return err
	}
	record, err := s.upsertSpendLimit(r.Context(), targetType, targetID, req)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "spend_limit.update", targetType, targetID, r, nil)
	writeJSON(w, http.StatusOK, spendLimitResponse(record), nil)
	return nil
}

func (s *Server) enforceSpendLimits(ctx context.Context, auth apiKeyAuth, reserveAmount float64) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, target_type, target_id::text,
		       daily_usd_limit::text, monthly_usd_limit::text,
		       daily_request_limit, monthly_request_limit, low_balance_threshold::text,
		       status, metadata_json::text, created_at, updated_at
		FROM spend_limits
		WHERE status = 'active'
		  AND (
		    (target_type = 'user' AND target_id = $1::uuid)
		    OR (target_type = 'api_key' AND target_id = $2::uuid)
		  )
	`, auth.UserID, auth.APIKeyID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		limit, err := scanSpendLimit(rows)
		if err != nil {
			return err
		}
		if err := s.enforceSpendLimit(ctx, limit, auth, reserveAmount); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Server) enforceSpendLimit(ctx context.Context, limit spendLimitRecord, auth apiKeyAuth, reserveAmount float64) error {
	userID := auth.UserID
	apiKeyID := ""
	if limit.TargetType == "api_key" {
		apiKeyID = auth.APIKeyID
	}
	daySpend, dayRequests, err := s.spendUsageSince(ctx, userID, apiKeyID, "day")
	if err != nil {
		return err
	}
	monthSpend, monthRequests, err := s.spendUsageSince(ctx, userID, apiKeyID, "month")
	if err != nil {
		return err
	}
	if limit.DailyUSDLimit.Valid {
		maxSpend, _ := strconv.ParseFloat(limit.DailyUSDLimit.String, 64)
		if daySpend+reserveAmount > maxSpend {
			return billingError("daily_spend_limit_exceeded", "Daily spend limit exceeded.")
		}
	}
	if limit.MonthlyUSDLimit.Valid {
		maxSpend, _ := strconv.ParseFloat(limit.MonthlyUSDLimit.String, 64)
		if monthSpend+reserveAmount > maxSpend {
			return billingError("monthly_spend_limit_exceeded", "Monthly spend limit exceeded.")
		}
	}
	if limit.DailyRequestLimit.Valid && dayRequests+1 > limit.DailyRequestLimit.Int64 {
		return appError{status: http.StatusTooManyRequests, code: "daily_request_limit_exceeded", message: "Daily request limit exceeded.", typ: "rate_limit_error"}
	}
	if limit.MonthlyRequestLimit.Valid && monthRequests+1 > limit.MonthlyRequestLimit.Int64 {
		return appError{status: http.StatusTooManyRequests, code: "monthly_request_limit_exceeded", message: "Monthly request limit exceeded.", typ: "rate_limit_error"}
	}
	return nil
}

func (s *Server) spendUsageSince(ctx context.Context, userID string, apiKeyID string, window string) (float64, int64, error) {
	trunc := "day"
	if window == "month" {
		trunc = "month"
	}
	args := []any{userID}
	apiKeyClause := ""
	if apiKeyID != "" {
		args = append(args, apiKeyID)
		apiKeyClause = "AND api_key_id = $2::uuid"
	}
	var spendText string
	var requests int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(actual_cost), 0)::text, COUNT(*)
		FROM usage_records
		WHERE user_id = $1::uuid
		  `+apiKeyClause+`
		  AND created_at >= date_trunc('`+trunc+`', now())
		  AND status IN ('success', 'failed')
	`, args...).Scan(&spendText, &requests)
	if err != nil {
		return 0, 0, err
	}
	spend, err := strconv.ParseFloat(spendText, 64)
	if err != nil {
		return 0, 0, err
	}
	return spend, requests, nil
}

func (s *Server) upsertSpendLimit(ctx context.Context, targetType string, targetID string, req spendLimitRequest) (spendLimitRecord, error) {
	if targetType != "user" && req.LowBalanceThreshold != nil && strings.TrimSpace(*req.LowBalanceThreshold) != "" {
		return spendLimitRecord{}, badRequest("low_balance_threshold is only supported for user targets.")
	}
	dailyUSD, err := nullableNonNegativeDecimal(req.DailyUSDLimit)
	if err != nil {
		return spendLimitRecord{}, err
	}
	monthlyUSD, err := nullableNonNegativeDecimal(req.MonthlyUSDLimit)
	if err != nil {
		return spendLimitRecord{}, err
	}
	lowBalance, err := nullableNonNegativeDecimal(req.LowBalanceThreshold)
	if err != nil {
		return spendLimitRecord{}, err
	}
	dailyRequests, err := nullableNonNegativeInt(req.DailyRequestLimit)
	if err != nil {
		return spendLimitRecord{}, err
	}
	monthlyRequests, err := nullableNonNegativeInt(req.MonthlyRequestLimit)
	if err != nil {
		return spendLimitRecord{}, err
	}
	status := "active"
	if req.Status != nil {
		status = strings.TrimSpace(*req.Status)
		if status != "active" && status != "disabled" {
			return spendLimitRecord{}, badRequest("Invalid spend limit status.")
		}
	}
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return spendLimitRecord{}, err
	}
	var record spendLimitRecord
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO spend_limits (
		  target_type, target_id, daily_usd_limit, monthly_usd_limit,
		  daily_request_limit, monthly_request_limit, low_balance_threshold, status, metadata_json
		)
		VALUES ($1, $2::uuid, $3::numeric, $4::numeric, $5, $6, $7::numeric, $8, $9::jsonb)
		ON CONFLICT (target_type, target_id)
		DO UPDATE SET daily_usd_limit = EXCLUDED.daily_usd_limit,
		              monthly_usd_limit = EXCLUDED.monthly_usd_limit,
		              daily_request_limit = EXCLUDED.daily_request_limit,
		              monthly_request_limit = EXCLUDED.monthly_request_limit,
		              low_balance_threshold = EXCLUDED.low_balance_threshold,
		              status = EXCLUDED.status,
		              metadata_json = EXCLUDED.metadata_json
		RETURNING id::text, target_type, target_id::text,
		          daily_usd_limit::text, monthly_usd_limit::text,
		          daily_request_limit, monthly_request_limit, low_balance_threshold::text,
		          status, metadata_json::text, created_at, updated_at
	`, targetType, targetID, dailyUSD, monthlyUSD, dailyRequests, monthlyRequests, lowBalance, status, metadata).Scan(
		&record.ID,
		&record.TargetType,
		&record.TargetID,
		&record.DailyUSDLimit,
		&record.MonthlyUSDLimit,
		&record.DailyRequestLimit,
		&record.MonthlyRequestLimit,
		&record.LowBalanceThreshold,
		&record.Status,
		&record.Metadata,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	return record, err
}

func (s *Server) validateSpendLimitTarget(ctx context.Context, targetType string, targetID string) error {
	var exists bool
	query := "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)"
	if targetType == "api_key" {
		query = "SELECT EXISTS (SELECT 1 FROM api_keys WHERE id = $1)"
	}
	if err := s.db.QueryRowContext(ctx, query, targetID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return notFound("Spend limit target was not found.")
	}
	return nil
}

func (s *Server) spendLimitForTarget(ctx context.Context, targetType string, targetID string) (*spendLimitRecord, error) {
	var record spendLimitRecord
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text, target_type, target_id::text,
		       daily_usd_limit::text, monthly_usd_limit::text,
		       daily_request_limit, monthly_request_limit, low_balance_threshold::text,
		       status, metadata_json::text, created_at, updated_at
		FROM spend_limits
		WHERE target_type = $1 AND target_id = $2::uuid
	`, targetType, targetID).Scan(
		&record.ID, &record.TargetType, &record.TargetID,
		&record.DailyUSDLimit, &record.MonthlyUSDLimit,
		&record.DailyRequestLimit, &record.MonthlyRequestLimit, &record.LowBalanceThreshold,
		&record.Status, &record.Metadata, &record.CreatedAt, &record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *Server) spendLimitsForUserAPIKeys(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sl.id::text, sl.target_type, sl.target_id::text,
		       sl.daily_usd_limit::text, sl.monthly_usd_limit::text,
		       sl.daily_request_limit, sl.monthly_request_limit, sl.low_balance_threshold::text,
		       sl.status, sl.metadata_json::text, sl.created_at, sl.updated_at
		FROM spend_limits sl
		JOIN api_keys k ON k.id = sl.target_id
		WHERE sl.target_type = 'api_key'
		  AND k.owner_id = $1
		ORDER BY sl.updated_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		record, err := scanSpendLimit(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, spendLimitResponse(record))
	}
	return items, rows.Err()
}

func scanSpendLimit(scanner interface{ Scan(...any) error }) (spendLimitRecord, error) {
	var record spendLimitRecord
	err := scanner.Scan(
		&record.ID,
		&record.TargetType,
		&record.TargetID,
		&record.DailyUSDLimit,
		&record.MonthlyUSDLimit,
		&record.DailyRequestLimit,
		&record.MonthlyRequestLimit,
		&record.LowBalanceThreshold,
		&record.Status,
		&record.Metadata,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	return record, err
}

func spendLimitResponse(record spendLimitRecord) map[string]any {
	return map[string]any{
		"id":                    record.ID,
		"target_type":           record.TargetType,
		"target_id":             record.TargetID,
		"daily_usd_limit":       nullableSQLString(record.DailyUSDLimit),
		"monthly_usd_limit":     nullableSQLString(record.MonthlyUSDLimit),
		"daily_request_limit":   nullableSQLInt(record.DailyRequestLimit),
		"monthly_request_limit": nullableSQLInt(record.MonthlyRequestLimit),
		"low_balance_threshold": nullableSQLString(record.LowBalanceThreshold),
		"status":                record.Status,
		"metadata":              jsonRaw(record.Metadata),
		"created_at":            record.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":            record.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) emitLowBalanceNotification(ctx context.Context, walletID string, balanceAfter string) error {
	if strings.TrimSpace(walletID) == "" || strings.TrimSpace(balanceAfter) == "" {
		return nil
	}
	balance, err := strconv.ParseFloat(balanceAfter, 64)
	if err != nil {
		return nil
	}

	var userID, thresholdText string
	err = s.db.QueryRowContext(ctx, `
		SELECT w.user_id::text, sl.low_balance_threshold::text
		FROM wallet_accounts w
		JOIN spend_limits sl ON sl.target_type = 'user'
		  AND sl.target_id = w.user_id
		  AND sl.status = 'active'
		  AND sl.low_balance_threshold IS NOT NULL
		WHERE w.id = $1
	`, walletID).Scan(&userID, &thresholdText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	threshold, err := strconv.ParseFloat(thresholdText, 64)
	if err != nil || balance > threshold {
		return nil
	}

	var recent bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM notification_events
		  WHERE event_type = 'low_balance'
		    AND target_type = 'user'
		    AND target_id = $1
		    AND created_at >= now() - interval '24 hours'
		)
	`, userID).Scan(&recent); err != nil {
		return err
	}
	if recent {
		return nil
	}

	return s.emitNotification(ctx, notificationEventInput{
		EventType:  "low_balance",
		Severity:   "warning",
		Title:      "Wallet balance is low.",
		Message:    "Wallet balance is at or below the configured threshold.",
		TargetType: "user",
		TargetID:   userID,
		Payload: map[string]any{
			"wallet_id":    walletID,
			"balance":      balanceAfter,
			"threshold":    thresholdText,
			"window":       "24h",
			"deduplicated": true,
		},
	})
}

func nullableSpendLimit(record *spendLimitRecord) any {
	if record == nil {
		return nil
	}
	return spendLimitResponse(*record)
}

func nullableNonNegativeInt(value *int) (any, error) {
	if value == nil {
		return nil, nil
	}
	if *value < 0 {
		return nil, badRequest("Limit must be non-negative.")
	}
	return *value, nil
}

func nullableSQLString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
