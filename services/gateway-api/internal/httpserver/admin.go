package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	auth, err := s.loginUser(r.Context(), trimEmail(req.Email), req.Password, "admin")
	if err != nil {
		writeError(w, r, err)
		return
	}

	s.setSessionCookie(w, "admin", auth.Token, auth.ExpiresAt)
	audit(r.Context(), s.db, auth.UserID, "admin", "admin.login", "user", auth.UserID, r, nil)
	writeJSON(w, http.StatusOK, auth.Response, nil)
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request, auth authContext) error {
	_, err := s.db.ExecContext(r.Context(), "UPDATE user_sessions SET revoked_at = now() WHERE session_id = $1", auth.SessionID)
	if err != nil {
		return err
	}
	s.clearSessionCookie(w, "admin")
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true}, nil)
	return nil
}

func (s *Server) adminMe(w http.ResponseWriter, r *http.Request, auth authContext) error {
	writeJSON(w, http.StatusOK, userResponse(auth), nil)
	return nil
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request, auth authContext) error {
	limit := limitFromRequest(r, 50, 200)
	search := "%" + strings.TrimSpace(r.URL.Query().Get("q")) + "%"
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT u.id::text, u.user_type, u.email::text, u.display_name, u.status, u.last_login_at,
		       u.created_at, u.updated_at, COALESCE(w.balance::text, '0'), COALESCE(w.reserved_balance::text, '0'),
		       (SELECT COUNT(*) FROM api_keys k WHERE k.owner_id = u.id AND k.status <> 'revoked') AS api_key_count,
		       (SELECT COALESCE(SUM(actual_cost), 0)::text FROM usage_records ur WHERE ur.user_id = u.id) AS total_usage_cost
		FROM users u
		LEFT JOIN wallet_accounts w ON w.user_id = u.id
		WHERE ($1 = '%%' OR u.email ILIKE $1 OR u.display_name ILIKE $1)
		  AND ($3 = '' OR u.status = $3)
		ORDER BY u.created_at DESC
		LIMIT $2
	`, search, limit, strings.TrimSpace(r.URL.Query().Get("status")))
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, userType, email, displayName, status, balance, reserved, totalUsageCost string
		var apiKeyCount int
		var lastLogin sql.NullTime
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &userType, &email, &displayName, &status, &lastLogin, &createdAt, &updatedAt, &balance, &reserved, &apiKeyCount, &totalUsageCost); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":            id,
			"user_type":     userType,
			"email":         email,
			"display_name":  displayName,
			"status":        status,
			"last_login_at": nullableTime(&lastLogin.Time),
			"created_at":    createdAt.UTC().Format(time.RFC3339),
			"updated_at":    updatedAt.UTC().Format(time.RFC3339),
			"wallet": map[string]any{
				"balance":          balance,
				"reserved_balance": reserved,
				"currency":         "USD",
			},
			"api_key_count":    apiKeyCount,
			"total_usage_cost": totalUsageCost,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		UserType    string `json:"user_type"`
		Status      string `json:"status"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	email := trimEmail(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		return badRequest("A valid email is required.")
	}
	userType := strings.TrimSpace(req.UserType)
	if userType == "" {
		userType = "personal_user"
	}
	if userType != "personal_user" && userType != "operator" {
		return badRequest("user_type must be personal_user or operator.")
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "disabled" && status != "pending" {
		return badRequest("Invalid user status.")
	}
	password := req.Password
	generatedPassword := ""
	if strings.TrimSpace(password) == "" {
		nextPassword, err := security.NewOpaqueToken("tmp_", 24)
		if err != nil {
			return err
		}
		password = nextPassword
		generatedPassword = nextPassword
	}
	if len(password) < 8 {
		return badRequest("Password must be at least 8 characters.")
	}
	passwordHash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = email
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var userID string
	err = tx.QueryRowContext(r.Context(), `
		INSERT INTO users (user_type, email, password_hash, display_name, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id::text
	`, userType, email, passwordHash, displayName, status).Scan(&userID)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			return conflict("Email is already registered.")
		}
		return err
	}
	if userType == "personal_user" {
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO wallet_accounts (user_id) VALUES ($1)", userID); err != nil {
			return err
		}
	}
	audit(r.Context(), tx, auth.UserID, "admin", "user.create", "user", userID, r, map[string]any{"email": email, "user_type": userType})
	if err := tx.Commit(); err != nil {
		return err
	}

	response := map[string]any{"id": userID, "email": email, "user_type": userType, "status": status}
	if generatedPassword != "" {
		response["temporary_password"] = generatedPassword
	}
	writeJSON(w, http.StatusCreated, response, nil)
	return nil
}

func (s *Server) adminUserDetail(w http.ResponseWriter, r *http.Request, auth authContext) error {
	userID := r.PathValue("userId")
	var id, userType, email, displayName, status string
	var lastLogin sql.NullTime
	var emailVerifiedAt sql.NullTime
	var createdAt, updatedAt time.Time
	err := s.db.QueryRowContext(r.Context(), `
		SELECT id::text, user_type, email::text, display_name, status, last_login_at, email_verified_at, created_at, updated_at
		FROM users
		WHERE id = $1
	`, userID).Scan(&id, &userType, &email, &displayName, &status, &lastLogin, &emailVerifiedAt, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("User was not found.")
	}
	if err != nil {
		return err
	}

	wallet, err := s.adminOptionalWalletForUser(r.Context(), userID)
	if err != nil {
		return err
	}
	counts, err := s.adminUserDetailCounts(r.Context(), userID)
	if err != nil {
		return err
	}
	groups, err := s.adminUserGroups(r.Context(), userID)
	if err != nil {
		return err
	}
	orders, err := s.listOrders(r.Context(), "user_id = $2::uuid", []any{10, userID})
	if err != nil {
		return err
	}
	subscriptions, err := s.adminUserSubscriptions(r.Context(), userID, 10)
	if err != nil {
		return err
	}
	auditRows, err := s.adminUserAuditRows(r.Context(), userID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":                id,
			"user_type":         userType,
			"email":             email,
			"display_name":      displayName,
			"status":            status,
			"last_login_at":     nullableSQLTime(lastLogin),
			"email_verified_at": nullableSQLTime(emailVerifiedAt),
			"created_at":        createdAt.UTC().Format(time.RFC3339),
			"updated_at":        updatedAt.UTC().Format(time.RFC3339),
		},
		"wallet":        wallet,
		"counts":        counts,
		"groups":        groups,
		"orders":        orders,
		"subscriptions": subscriptions,
		"audit":         auditRows,
	}, nil)
	return nil
}

func (s *Server) adminOptionalWalletForUser(ctx context.Context, userID string) (map[string]any, error) {
	var id, balance, reservedBalance, currency, status string
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text, balance::text, reserved_balance::text, currency, status
		FROM wallet_accounts
		WHERE user_id = $1
	`, userID).Scan(&id, &balance, &reservedBalance, &currency, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":               id,
		"user_id":          userID,
		"balance":          balance,
		"reserved_balance": reservedBalance,
		"currency":         currency,
		"status":           status,
	}, nil
}

func (s *Server) adminUserDetailCounts(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT 'api_keys', COUNT(*)::text FROM api_keys WHERE owner_id = $1
		UNION ALL SELECT 'usage_records', COUNT(*)::text FROM usage_records WHERE user_id = $1
		UNION ALL SELECT 'orders', COUNT(*)::text FROM orders WHERE user_id = $1
		UNION ALL SELECT 'subscriptions', COUNT(*)::text FROM user_subscriptions WHERE user_id = $1
		UNION ALL SELECT 'audit_logs', COUNT(*)::text FROM audit_logs WHERE actor_user_id = $1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		counts[key] = value
	}
	return counts, rows.Err()
}

func (s *Server) adminUserGroups(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.id::text, g.name, g.status, m.role, m.created_at
		FROM user_group_memberships m
		JOIN groups g ON g.id = m.group_id
		WHERE m.user_id = $1
		ORDER BY m.created_at DESC
		LIMIT 100
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, status, role string
		var createdAt time.Time
		if err := rows.Scan(&id, &name, &status, &role, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": id, "name": name, "status": status, "role": role, "created_at": createdAt.UTC().Format(time.RFC3339)})
	}
	return items, rows.Err()
}

func (s *Server) adminUserAuditRows(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, COALESCE(actor_user_id::text, ''), actor_type, action, target_type, target_id,
		       ip_address, user_agent, metadata_json::text, created_at
		FROM audit_logs
		WHERE actor_user_id = $1 OR target_id = $2
		ORDER BY created_at DESC
		LIMIT 20
	`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, actorUserID, actorType, action, targetType, targetID, ipAddress, userAgent, metadata string
		var createdAt time.Time
		if err := rows.Scan(&id, &actorUserID, &actorType, &action, &targetType, &targetID, &ipAddress, &userAgent, &metadata, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": id, "actor_user_id": actorUserID, "actor_type": actorType, "action": action, "target_type": targetType, "target_id": targetID, "ip_address": ipAddress, "user_agent": userAgent, "metadata": jsonRaw(metadata), "created_at": createdAt.UTC().Format(time.RFC3339)})
	}
	return items, rows.Err()
}

func (s *Server) adminUserSubscriptions(ctx context.Context, userID string, limit int) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT us.id::text, us.user_id::text, u.email::text, us.plan_id::text, sp.name, us.status,
		       COALESCE(us.stripe_subscription_id, ''), COALESCE(us.granted_group_id::text, ''), us.starts_at, us.ends_at,
		       us.current_period_start, us.current_period_end, us.created_at, us.updated_at
		FROM user_subscriptions us
		JOIN users u ON u.id = us.user_id
		JOIN subscription_plans sp ON sp.id = us.plan_id
		WHERE us.user_id = $1
		ORDER BY us.created_at DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

func (s *Server) adminPatchUser(w http.ResponseWriter, r *http.Request, auth authContext) error {
	userID := r.PathValue("userId")
	var req struct {
		DisplayName *string `json:"display_name"`
		Status      *string `json:"status"`
		UserType    *string `json:"user_type"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	var displayName, status, userType string
	err := s.db.QueryRowContext(r.Context(), "SELECT display_name, status, user_type FROM users WHERE id = $1", userID).Scan(&displayName, &status, &userType)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("User was not found.")
	}
	if err != nil {
		return err
	}

	if req.DisplayName != nil {
		displayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.Status != nil {
		nextStatus := strings.TrimSpace(*req.Status)
		if nextStatus != "active" && nextStatus != "disabled" && nextStatus != "pending" {
			return badRequest("Invalid user status.")
		}
		status = nextStatus
	}
	if req.UserType != nil {
		nextType := strings.TrimSpace(*req.UserType)
		if nextType != "personal_user" && nextType != "operator" && nextType != "platform_owner" {
			return badRequest("Invalid user type.")
		}
		userType = nextType
	}

	_, err = s.db.ExecContext(r.Context(), `
		UPDATE users SET display_name = $2, status = $3, user_type = $4 WHERE id = $1
	`, userID, displayName, status, userType)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "user.update", "user", userID, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": userID, "updated": true}, nil)
	return nil
}

func (s *Server) adminUserAPIKeys(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, display_prefix, name, status, routing_mode, expires_at, ip_allowlist_json::text, model_scope_json::text, last_used_at, created_at, updated_at
		FROM api_keys
		WHERE owner_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, r.PathValue("userId"), limitFromRequest(r, 50, 200))
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		item, err := scanAPIKey(rows)
		if err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminUserWallet(w http.ResponseWriter, r *http.Request, auth authContext) error {
	wallet, err := s.walletForUser(r.Context(), r.PathValue("userId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, wallet, nil)
	return nil
}

func (s *Server) adminUserWalletLedger(w http.ResponseWriter, r *http.Request, auth authContext) error {
	ledger, err := s.walletLedgerForUser(r.Context(), r.PathValue("userId"), limitFromRequest(r, 50, 500))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, ledger, nil)
	return nil
}

func (s *Server) adminWalletAdjustment(w http.ResponseWriter, r *http.Request, auth authContext) error {
	userID := r.PathValue("userId")
	var req struct {
		EntryType string         `json:"entry_type"`
		Amount    string         `json:"amount"`
		Reason    string         `json:"reason"`
		Metadata  map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.EntryType == "" {
		req.EntryType = "adjustment"
	}
	if req.EntryType != "credit" && req.EntryType != "debit" && req.EntryType != "adjustment" {
		return badRequest("entry_type must be credit, debit, or adjustment.")
	}
	amount, err := amountString(req.Amount)
	if err != nil {
		return err
	}
	metadata, err := encodeJSON(map[string]any{"reason": req.Reason, "metadata": req.Metadata})
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var walletID, balanceAfter, reservedAfter string
	operator := "+"
	if req.EntryType == "debit" {
		operator = "-"
	}
	query := `
		UPDATE wallet_accounts
		SET balance = balance ` + operator + ` $2::numeric
		WHERE user_id = $1
		  AND status = 'active'
		  AND ($3 <> 'debit' OR balance - $2::numeric >= reserved_balance)
		RETURNING id::text, balance::text, reserved_balance::text
	`
	err = tx.QueryRowContext(r.Context(), query, userID, amount, req.EntryType).Scan(&walletID, &balanceAfter, &reservedAfter)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if existsErr := tx.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM wallet_accounts WHERE user_id = $1)", userID).Scan(&exists); existsErr != nil {
			return existsErr
		}
		if !exists {
			return notFound("Wallet was not found.")
		}
		return billingError("insufficient_balance", "Wallet balance is insufficient.")
	}
	if err != nil {
		return err
	}

	var ledgerID string
	if err := tx.QueryRowContext(r.Context(), `
		INSERT INTO wallet_ledgers (wallet_account_id, entry_type, amount, balance_after, reserved_after, reference_type, reference_id, metadata_json)
		VALUES ($1, $2, $3::numeric, $4::numeric, $5::numeric, 'admin_adjustment', $6, $7::jsonb)
		RETURNING id::text
	`, walletID, req.EntryType, amount, balanceAfter, reservedAfter, requestIDFromContext(r.Context()), metadata).Scan(&ledgerID); err != nil {
		return err
	}

	audit(r.Context(), tx, auth.UserID, "admin", "wallet.adjust", "user", userID, r, map[string]any{"ledger_id": ledgerID, "amount": amount})
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ledger_id": ledgerID, "balance": balanceAfter, "reserved_balance": reservedAfter}, nil)
	return nil
}

func (s *Server) adminRedeemCodes(w http.ResponseWriter, r *http.Request, auth authContext) error {
	limit := limitFromRequest(r, 50, 200)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, display_prefix, grant_value::text, currency, max_claims, claim_count,
		       expires_at, status, metadata_json::text, created_at, updated_at
		FROM redeem_codes
		WHERE ($2 = '' OR status = $2)
		ORDER BY created_at DESC
		LIMIT $1
	`, limit, status)
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, displayPrefix, grantValue, currency, status, metadata string
		var maxClaims, claimCount int
		var expiresAt sql.NullTime
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &displayPrefix, &grantValue, &currency, &maxClaims, &claimCount, &expiresAt, &status, &metadata, &createdAt, &updatedAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":             id,
			"display_prefix": displayPrefix,
			"grant_value":    grantValue,
			"currency":       currency,
			"max_claims":     maxClaims,
			"claim_count":    claimCount,
			"expires_at":     nullableTime(&expiresAt.Time),
			"status":         status,
			"metadata":       jsonRaw(metadata),
			"created_at":     createdAt.UTC().Format(time.RFC3339),
			"updated_at":     updatedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminRedeemCodeClaims(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT c.id::text, c.user_id::text, u.email::text, c.wallet_ledger_id::text, c.claimed_at
		FROM redeem_claims c
		JOIN users u ON u.id = c.user_id
		WHERE c.redeem_code_id = $1
		ORDER BY c.claimed_at DESC
		LIMIT $2
	`, r.PathValue("redeemCodeId"), limitFromRequest(r, 50, 500))
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, userID, email, walletLedgerID string
		var claimedAt time.Time
		if err := rows.Scan(&id, &userID, &email, &walletLedgerID, &claimedAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":               id,
			"user_id":          userID,
			"email":            email,
			"wallet_ledger_id": walletLedgerID,
			"claimed_at":       claimedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateRedeemCodes(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		GrantValue string         `json:"grant_value"`
		Count      int            `json:"count"`
		MaxClaims  int            `json:"max_claims"`
		ExpiresAt  string         `json:"expires_at"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	grantValue, err := amountString(req.GrantValue)
	if err != nil {
		return err
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Count > 500 {
		return badRequest("count cannot exceed 500.")
	}
	if req.MaxClaims <= 0 {
		req.MaxClaims = 1
	}
	var expiresAt any
	if strings.TrimSpace(req.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			return badRequest("expires_at must be RFC3339.")
		}
		expiresAt = parsed
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

	codes := []map[string]any{}
	for i := 0; i < req.Count; i++ {
		code, err := security.NewOpaqueToken("rc_", 24)
		if err != nil {
			return err
		}
		var id string
		if err := tx.QueryRowContext(r.Context(), `
			INSERT INTO redeem_codes (code_hash, display_prefix, grant_value, max_claims, expires_at, metadata_json)
			VALUES ($1, $2, $3::numeric, $4, $5, $6::jsonb)
			RETURNING id::text
		`, security.HashSecret(code), security.DisplayPrefix(code), grantValue, req.MaxClaims, expiresAt, metadata).Scan(&id); err != nil {
			return err
		}
		codes = append(codes, map[string]any{"id": id, "code": code, "display_prefix": security.DisplayPrefix(code)})
	}

	audit(r.Context(), tx, auth.UserID, "admin", "redeem_code.create", "redeem_code", "", r, map[string]any{"count": req.Count, "grant_value": grantValue})
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"codes": codes}, nil)
	return nil
}

func (s *Server) adminPatchRedeemCode(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id := r.PathValue("redeemCodeId")
	var req struct {
		Status    *string `json:"status"`
		ExpiresAt *string `json:"expires_at"`
		MaxClaims *int    `json:"max_claims"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := s.requireIDExists(r.Context(), "redeem_codes", id, "Redeem code was not found."); err != nil {
		return err
	}
	if req.Status != nil {
		status := strings.TrimSpace(*req.Status)
		if status != "active" && status != "disabled" && status != "expired" {
			return badRequest("Invalid redeem code status.")
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE redeem_codes SET status = $2 WHERE id = $1", id, status); err != nil {
			return err
		}
	}
	if req.MaxClaims != nil {
		if *req.MaxClaims <= 0 {
			return badRequest("max_claims must be positive.")
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE redeem_codes SET max_claims = $2 WHERE id = $1", id, *req.MaxClaims); err != nil {
			return err
		}
	}
	if req.ExpiresAt != nil {
		var expiresAt any
		if strings.TrimSpace(*req.ExpiresAt) != "" {
			parsed, err := time.Parse(time.RFC3339, *req.ExpiresAt)
			if err != nil {
				return badRequest("expires_at must be RFC3339.")
			}
			expiresAt = parsed
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE redeem_codes SET expires_at = $2 WHERE id = $1", id, expiresAt); err != nil {
			return err
		}
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "redeem_code.update", "redeem_code", id, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) adminUsage(w http.ResponseWriter, r *http.Request, auth authContext) error {
	limit := limitFromRequest(r, 100, 500)
	whereSQL, args, err := usageWhereFromRequest(r, nil, nil)
	if err != nil {
		return err
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, request_id, requested_model, upstream_model, endpoint, input_tokens, output_tokens,
		       image_count, audio_seconds::text, request_count, actual_cost::text,
		       COALESCE(group_id::text, ''), billing_multiplier::text, effective_policy_json::text, risk_decision_json::text,
		       status, error_code,
		       upstream_status, duration_ms, usage_source, stream_event_count, websocket_frame_count, created_at
		FROM usage_records
		`+whereSQL+`
		ORDER BY created_at DESC
		LIMIT $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items, err := scanUsageRows(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request, auth authContext) error {
	limit := limitFromRequest(r, 100, 500)
	query := r.URL.Query()
	clauses := []string{}
	args := []any{}
	if actorID := strings.TrimSpace(query.Get("actor_id")); actorID != "" {
		args = append(args, actorID)
		clauses = append(clauses, "actor_user_id = $"+strconv.Itoa(len(args))+"::uuid")
	}
	if action := strings.TrimSpace(query.Get("action")); action != "" {
		args = append(args, "%"+action+"%")
		clauses = append(clauses, "action ILIKE $"+strconv.Itoa(len(args)))
	}
	if targetType := strings.TrimSpace(query.Get("target_type")); targetType != "" {
		args = append(args, targetType)
		clauses = append(clauses, "target_type = $"+strconv.Itoa(len(args)))
	}
	if from := strings.TrimSpace(query.Get("from")); from != "" {
		parsed, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return badRequest("from must be RFC3339.")
		}
		args = append(args, parsed)
		clauses = append(clauses, "created_at >= $"+strconv.Itoa(len(args)))
	}
	if to := strings.TrimSpace(query.Get("to")); to != "" {
		parsed, err := time.Parse(time.RFC3339, to)
		if err != nil {
			return badRequest("to must be RFC3339.")
		}
		args = append(args, parsed)
		clauses = append(clauses, "created_at <= $"+strconv.Itoa(len(args)))
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, COALESCE(actor_user_id::text, ''), actor_type, action, target_type, target_id,
		       ip_address, user_agent, metadata_json::text, created_at
		FROM audit_logs
		`+whereSQL+`
		ORDER BY created_at DESC
		LIMIT $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, actorUserID, actorType, action, targetType, targetID, ipAddress, userAgent, metadata string
		var createdAt time.Time
		if err := rows.Scan(&id, &actorUserID, &actorType, &action, &targetType, &targetID, &ipAddress, &userAgent, &metadata, &createdAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":            id,
			"actor_user_id": actorUserID,
			"actor_type":    actorType,
			"action":        action,
			"target_type":   targetType,
			"target_id":     targetID,
			"ip_address":    ipAddress,
			"user_agent":    userAgent,
			"metadata":      jsonRaw(metadata),
			"created_at":    createdAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminModels(w http.ResponseWriter, r *http.Request, auth authContext) error {
	models, err := s.listModels(r.Context(), false, "")
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, models, nil)
	return nil
}

func (s *Server) adminCreateModel(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req modelPayload
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.ModelName) == "" {
		return badRequest("model_name is required.")
	}
	if err := s.upsertModel(r, auth, req.ModelName, req, true); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"model_name": req.ModelName}, nil)
	return nil
}

func (s *Server) adminPatchModel(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req modelPayload
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	modelName := r.PathValue("modelName")
	if err := s.upsertModel(r, auth, modelName, req, false); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"model_name": modelName, "updated": true}, nil)
	return nil
}

type modelPayload struct {
	ModelName            string         `json:"model_name"`
	DisplayName          string         `json:"display_name"`
	ProviderHint         string         `json:"provider_hint"`
	EndpointCapabilities []string       `json:"endpoint_capabilities"`
	Aliases              []string       `json:"aliases"`
	InputUSDPer1K        *string        `json:"input_usd_per_1k"`
	OutputUSDPer1K       *string        `json:"output_usd_per_1k"`
	RequestUSD           *string        `json:"request_usd"`
	MinChargeUSD         *string        `json:"min_charge_usd"`
	BillingMode          *string        `json:"billing_mode"`
	BillingExpr          *string        `json:"billing_expr"`
	CacheReadUSDPer1K    *string        `json:"cache_read_usd_per_1k"`
	CacheWriteUSDPer1K   *string        `json:"cache_write_usd_per_1k"`
	ImageUSDPerUnit      *string        `json:"image_usd_per_unit"`
	AudioUSDPerSecond    *string        `json:"audio_usd_per_second"`
	PublicVisible        *bool          `json:"public_visible"`
	Status               *string        `json:"status"`
	Metadata             map[string]any `json:"metadata"`
}

func (s *Server) upsertModel(r *http.Request, auth authContext, modelName string, req modelPayload, insert bool) error {
	inputPrice, inputPriceSet, err := modelPriceValue(req.InputUSDPer1K, "input_usd_per_1k", insert)
	if err != nil {
		return err
	}
	outputPrice, outputPriceSet, err := modelPriceValue(req.OutputUSDPer1K, "output_usd_per_1k", insert)
	if err != nil {
		return err
	}
	requestUSD, requestUSDSet, err := modelPriceValue(req.RequestUSD, "request_usd", insert)
	if err != nil {
		return err
	}
	minCharge, minChargeSet, err := modelPriceValue(req.MinChargeUSD, "min_charge_usd", insert)
	if err != nil {
		return err
	}
	status, statusSet, err := statusValue(req.Status, "active", insert, "active", "disabled")
	if err != nil {
		return err
	}
	aliases := normalizeStringList(req.Aliases)
	hasCapabilities := req.EndpointCapabilities != nil
	capabilityList := normalizeStringList(req.EndpointCapabilities)
	publicVisible := true
	publicVisibleSet := req.PublicVisible != nil
	if req.PublicVisible != nil {
		publicVisible = *req.PublicVisible
	}
	capabilities, err := encodeJSON(capabilityList)
	if err != nil {
		return err
	}
	metadataValue := any(req.Metadata)
	if insert && req.Metadata == nil {
		metadataValue = map[string]any{}
	}
	metadata, err := encodeJSON(metadataValue)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if insert {
		_, err = tx.ExecContext(r.Context(), `
			INSERT INTO model_catalog (model_name, display_name, provider_hint, endpoint_capabilities,
				input_usd_per_1k, output_usd_per_1k, request_usd, min_charge_usd, public_visible, status, metadata_json)
			VALUES ($1, $2, $3, $4::jsonb, $5::numeric, $6::numeric, $7::numeric, $8::numeric, $9, $10, $11::jsonb)
		`, modelName, req.DisplayName, req.ProviderHint, capabilities, inputPrice, outputPrice, requestUSD, minCharge, publicVisible, status, metadata)
	} else {
		result, updateErr := tx.ExecContext(r.Context(), `
			UPDATE model_catalog
			SET display_name = COALESCE(NULLIF($2, ''), display_name),
			    provider_hint = COALESCE(NULLIF($3, ''), provider_hint),
			    endpoint_capabilities = CASE WHEN $4::boolean THEN $5::jsonb ELSE endpoint_capabilities END,
			    input_usd_per_1k = CASE WHEN $6::boolean THEN $7::numeric ELSE input_usd_per_1k END,
			    output_usd_per_1k = CASE WHEN $8::boolean THEN $9::numeric ELSE output_usd_per_1k END,
			    request_usd = CASE WHEN $10::boolean THEN $11::numeric ELSE request_usd END,
			    min_charge_usd = CASE WHEN $12::boolean THEN $13::numeric ELSE min_charge_usd END,
			    public_visible = CASE WHEN $14::boolean THEN $15 ELSE public_visible END,
			    status = CASE WHEN $16::boolean THEN $17 ELSE status END,
			    metadata_json = CASE WHEN $18::jsonb = 'null'::jsonb THEN metadata_json ELSE $18::jsonb END
			WHERE model_name = $1
		`, modelName, req.DisplayName, req.ProviderHint, hasCapabilities, capabilities,
			inputPriceSet, inputPrice,
			outputPriceSet, outputPrice,
			requestUSDSet, requestUSD,
			minChargeSet, minCharge,
			publicVisibleSet, publicVisible,
			statusSet, status,
			metadata)
		err = updateErr
		if err == nil {
			if rows, _ := result.RowsAffected(); rows == 0 {
				err = notFound("Model was not found.")
			}
		}
	}
	if err != nil {
		return err
	}
	if err := s.upsertModelPricingOverrideTx(r.Context(), tx, modelName, req, insert); err != nil {
		return err
	}
	if req.Aliases != nil {
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM model_aliases WHERE model_name = $1", modelName); err != nil {
			return err
		}
		for _, alias := range aliases {
			if alias == modelName {
				continue
			}
			if _, err := tx.ExecContext(r.Context(), `
				INSERT INTO model_aliases (alias, model_name)
				VALUES ($1, $2)
				ON CONFLICT (alias) DO UPDATE SET model_name = EXCLUDED.model_name
			`, alias, modelName); err != nil {
				return err
			}
		}
	}
	audit(r.Context(), tx, auth.UserID, "admin", "model.upsert", "model", modelName, r, map[string]any{"aliases": aliases})
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Server) upsertModelPricingOverrideTx(ctx context.Context, tx *sql.Tx, modelName string, req modelPayload, insert bool) error {
	billingMode, billingModeSet, err := billingModeValue(req.BillingMode, insert)
	if err != nil {
		return err
	}
	billingExpr, billingExprSet := optionalTrimmedString(req.BillingExpr)
	cacheRead, cacheReadSet, err := modelPriceValue(req.CacheReadUSDPer1K, "cache_read_usd_per_1k", false)
	if err != nil {
		return err
	}
	cacheWrite, cacheWriteSet, err := modelPriceValue(req.CacheWriteUSDPer1K, "cache_write_usd_per_1k", false)
	if err != nil {
		return err
	}
	imagePrice, imagePriceSet, err := modelPriceValue(req.ImageUSDPerUnit, "image_usd_per_unit", false)
	if err != nil {
		return err
	}
	audioPrice, audioPriceSet, err := modelPriceValue(req.AudioUSDPerSecond, "audio_usd_per_second", false)
	if err != nil {
		return err
	}
	shouldWrite := insert || billingModeSet || billingExprSet || cacheReadSet || cacheWriteSet || imagePriceSet || audioPriceSet
	if !shouldWrite {
		return nil
	}

	nextMode := billingMode
	nextExpr := billingExpr
	if !insert {
		var currentMode, currentExpr string
		err := tx.QueryRowContext(ctx, "SELECT billing_mode, billing_expr FROM model_pricing_overrides WHERE model_name = $1", modelName).Scan(&currentMode, &currentExpr)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !billingModeSet {
			nextMode = defaultString(currentMode, "standard")
		}
		if !billingExprSet {
			nextExpr = currentExpr
		}
	}
	if nextMode == "tiered_expr" {
		if strings.TrimSpace(nextExpr) == "" {
			return badRequest("billing_expr is required when billing_mode=tiered_expr.")
		}
		if _, err := evaluateBillingExpression(nextExpr, billingExpressionVars(usageCounts{InputTokens: 1000, OutputTokens: 1000}, meteringMetrics{RequestCount: 1, ImageCount: 1, AudioSeconds: 1})); err != nil {
			return badRequest(err.Error())
		}
	}
	if insert {
		if !billingModeSet {
			billingMode = "standard"
		}
		if !billingExprSet {
			billingExpr = ""
		}
		if !cacheReadSet {
			cacheRead = "0"
		}
		if !cacheWriteSet {
			cacheWrite = "0"
		}
		if !imagePriceSet {
			imagePrice = "0"
		}
		if !audioPriceSet {
			audioPrice = "0"
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO model_pricing_overrides (
			model_name, billing_mode, billing_expr, cache_read_usd_per_1k, cache_write_usd_per_1k,
			image_usd_per_unit, audio_usd_per_second
		)
		VALUES ($1, $2, $3, $4::numeric, $5::numeric, $6::numeric, $7::numeric)
		ON CONFLICT (model_name) DO UPDATE SET
		  billing_mode = CASE WHEN $8::boolean THEN EXCLUDED.billing_mode ELSE model_pricing_overrides.billing_mode END,
		  billing_expr = CASE WHEN $9::boolean THEN EXCLUDED.billing_expr ELSE model_pricing_overrides.billing_expr END,
		  cache_read_usd_per_1k = CASE WHEN $10::boolean THEN EXCLUDED.cache_read_usd_per_1k ELSE model_pricing_overrides.cache_read_usd_per_1k END,
		  cache_write_usd_per_1k = CASE WHEN $11::boolean THEN EXCLUDED.cache_write_usd_per_1k ELSE model_pricing_overrides.cache_write_usd_per_1k END,
		  image_usd_per_unit = CASE WHEN $12::boolean THEN EXCLUDED.image_usd_per_unit ELSE model_pricing_overrides.image_usd_per_unit END,
		  audio_usd_per_second = CASE WHEN $13::boolean THEN EXCLUDED.audio_usd_per_second ELSE model_pricing_overrides.audio_usd_per_second END
	`, modelName, billingMode, billingExpr, cacheRead, cacheWrite, imagePrice, audioPrice,
		insert || billingModeSet, insert || billingExprSet, insert || cacheReadSet, insert || cacheWriteSet, insert || imagePriceSet, insert || audioPriceSet)
	return err
}

func billingModeValue(value *string, required bool) (string, bool, error) {
	if value == nil {
		if required {
			return "standard", true, nil
		}
		return "standard", false, nil
	}
	mode := strings.ToLower(strings.TrimSpace(*value))
	if mode == "" {
		return "", false, badRequest("billing_mode cannot be empty.")
	}
	if mode != "standard" && mode != "tiered_expr" {
		return "", false, badRequest("billing_mode must be standard or tiered_expr.")
	}
	return mode, true, nil
}

func optionalTrimmedString(value *string) (string, bool) {
	if value == nil {
		return "", false
	}
	return strings.TrimSpace(*value), true
}

func (s *Server) adminProviders(w http.ResponseWriter, r *http.Request, auth authContext) error {
	items, err := s.listSimple(r, "providers", "id::text, name, provider_type, status, metadata_json::text, created_at, updated_at")
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateProvider(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		Name         string         `json:"name"`
		ProviderType string         `json:"provider_type"`
		Status       string         `json:"status"`
		Metadata     map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" {
		return badRequest("Provider name is required.")
	}
	if req.ProviderType == "" {
		req.ProviderType = "openai_compatible"
	}
	status, err := defaultedStatus(req.Status, "active", "active", "disabled")
	if err != nil {
		return err
	}
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return err
	}
	var id string
	if err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO providers (name, provider_type, status, metadata_json)
		VALUES ($1, $2, $3, $4::jsonb)
		RETURNING id::text
	`, strings.TrimSpace(req.Name), strings.TrimSpace(req.ProviderType), status, metadata).Scan(&id); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "provider.create", "provider", id, r, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminPatchProvider(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id := r.PathValue("providerId")
	var req struct {
		Name         *string        `json:"name"`
		ProviderType *string        `json:"provider_type"`
		Status       *string        `json:"status"`
		Metadata     map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := s.requireIDExists(r.Context(), "providers", id, "Provider was not found."); err != nil {
		return err
	}
	if req.Name != nil {
		if _, err := s.db.ExecContext(r.Context(), "UPDATE providers SET name = $2 WHERE id = $1", id, strings.TrimSpace(*req.Name)); err != nil {
			return err
		}
	}
	if req.ProviderType != nil {
		if _, err := s.db.ExecContext(r.Context(), "UPDATE providers SET provider_type = $2 WHERE id = $1", id, strings.TrimSpace(*req.ProviderType)); err != nil {
			return err
		}
	}
	if req.Status != nil {
		status, err := defaultedStatus(*req.Status, "active", "active", "disabled")
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE providers SET status = $2 WHERE id = $1", id, status); err != nil {
			return err
		}
	}
	if req.Metadata != nil {
		metadata, err := encodeJSON(defaultMap(req.Metadata))
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE providers SET metadata_json = $2::jsonb WHERE id = $1", id, metadata); err != nil {
			return err
		}
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "provider.update", "provider", id, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) adminChannels(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT c.id::text, c.provider_id::text, COALESCE(c.proxy_id::text, ''), c.name, c.base_url, c.status, c.priority, c.weight,
		       c.timeout_seconds, c.metadata_json::text,
		       COALESCE((
		         SELECT jsonb_agg(
		           jsonb_build_object(
		             'model_name', ca.model_name,
		             'endpoint', ca.endpoint,
		             'upstream_model', ca.upstream_model,
		             'transform_capability', ca.transform_capability_json,
		             'priority', ca.priority,
		             'weight', ca.weight,
		             'retry_priority', ca.retry_priority
		           )
		           ORDER BY ca.model_name, ca.endpoint
		         )
		         FROM channel_abilities ca
		         WHERE ca.channel_id = c.id
		       ), '[]'::jsonb)::text,
		       c.created_at, c.updated_at
		FROM channels c
		ORDER BY c.priority, c.created_at DESC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, providerID, proxyID, name, baseURL, status, metadata, abilities string
		var priority, weight, timeoutSeconds int
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &providerID, &proxyID, &name, &baseURL, &status, &priority, &weight, &timeoutSeconds, &metadata, &abilities, &createdAt, &updatedAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":              id,
			"provider_id":     providerID,
			"proxy_id":        proxyID,
			"name":            name,
			"base_url":        baseURL,
			"status":          status,
			"priority":        priority,
			"weight":          weight,
			"timeout_seconds": timeoutSeconds,
			"metadata":        jsonRaw(metadata),
			"abilities":       jsonArrayRaw(abilities),
			"created_at":      createdAt.UTC().Format(time.RFC3339),
			"updated_at":      updatedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

type channelAbilityPayload struct {
	ModelName           string         `json:"model_name"`
	Endpoint            string         `json:"endpoint"`
	UpstreamModel       string         `json:"upstream_model"`
	TransformCapability map[string]any `json:"transform_capability"`
	Priority            int            `json:"priority"`
	Weight              int            `json:"weight"`
	RetryPriority       int            `json:"retry_priority"`
}

func (s *Server) adminCreateChannel(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		ProviderID     string                  `json:"provider_id"`
		ProxyID        string                  `json:"proxy_id"`
		Name           string                  `json:"name"`
		BaseURL        string                  `json:"base_url"`
		Status         string                  `json:"status"`
		Priority       int                     `json:"priority"`
		Weight         int                     `json:"weight"`
		TimeoutSeconds int                     `json:"timeout_seconds"`
		Abilities      []channelAbilityPayload `json:"abilities"`
		Metadata       map[string]any          `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.ProviderID) == "" || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.BaseURL) == "" {
		return badRequest("provider_id, name, and base_url are required.")
	}
	status, err := defaultedStatus(req.Status, "active", "active", "disabled", "cooldown")
	if err != nil {
		return err
	}
	if req.Weight == 0 {
		req.Weight = 1
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 120
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
	var id string
	var proxy any
	if strings.TrimSpace(req.ProxyID) != "" {
		proxy = strings.TrimSpace(req.ProxyID)
	}
	baseURL, err := validateURL(req.BaseURL, "base_url", "http", "https")
	if err != nil {
		return err
	}
	if err := tx.QueryRowContext(r.Context(), `
		INSERT INTO channels (provider_id, proxy_id, name, base_url, status, priority, weight, timeout_seconds, metadata_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
		RETURNING id::text
	`, req.ProviderID, proxy, strings.TrimSpace(req.Name), baseURL, status, req.Priority, req.Weight, req.TimeoutSeconds, metadata).Scan(&id); err != nil {
		return err
	}
	if err := upsertChannelAbilities(r.Context(), tx, id, req.Abilities); err != nil {
		return err
	}
	audit(r.Context(), tx, auth.UserID, "admin", "channel.create", "channel", id, r, nil)
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminPatchChannel(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id := r.PathValue("channelId")
	var req struct {
		Name           *string                 `json:"name"`
		BaseURL        *string                 `json:"base_url"`
		Status         *string                 `json:"status"`
		Priority       *int                    `json:"priority"`
		Weight         *int                    `json:"weight"`
		TimeoutSeconds *int                    `json:"timeout_seconds"`
		ProxyID        *string                 `json:"proxy_id"`
		Metadata       map[string]any          `json:"metadata"`
		Abilities      []channelAbilityPayload `json:"abilities"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := s.requireIDExists(r.Context(), "channels", id, "Channel was not found."); err != nil {
		return err
	}
	if req.Name != nil {
		if _, err := s.db.ExecContext(r.Context(), "UPDATE channels SET name = $2 WHERE id = $1", id, strings.TrimSpace(*req.Name)); err != nil {
			return err
		}
	}
	if req.BaseURL != nil {
		baseURL, err := validateURL(*req.BaseURL, "base_url", "http", "https")
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE channels SET base_url = $2 WHERE id = $1", id, baseURL); err != nil {
			return err
		}
	}
	if req.Status != nil {
		status, err := defaultedStatus(*req.Status, "active", "active", "disabled", "cooldown")
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE channels SET status = $2 WHERE id = $1", id, status); err != nil {
			return err
		}
	}
	if req.Priority != nil {
		if _, err := s.db.ExecContext(r.Context(), "UPDATE channels SET priority = $2 WHERE id = $1", id, *req.Priority); err != nil {
			return err
		}
	}
	if req.Weight != nil {
		if *req.Weight <= 0 {
			return badRequest("weight must be positive.")
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE channels SET weight = $2 WHERE id = $1", id, *req.Weight); err != nil {
			return err
		}
	}
	if req.TimeoutSeconds != nil {
		if *req.TimeoutSeconds <= 0 {
			return badRequest("timeout_seconds must be positive.")
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE channels SET timeout_seconds = $2 WHERE id = $1", id, *req.TimeoutSeconds); err != nil {
			return err
		}
	}
	if req.ProxyID != nil {
		var proxy any
		if strings.TrimSpace(*req.ProxyID) != "" {
			proxy = strings.TrimSpace(*req.ProxyID)
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE channels SET proxy_id = $2 WHERE id = $1", id, proxy); err != nil {
			return err
		}
	}
	if req.Metadata != nil {
		metadata, err := encodeJSON(defaultMap(req.Metadata))
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE channels SET metadata_json = $2::jsonb WHERE id = $1", id, metadata); err != nil {
			return err
		}
	}
	if req.Abilities != nil {
		if err := upsertChannelAbilities(r.Context(), s.db, id, req.Abilities); err != nil {
			return err
		}
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "channel.update", "channel", id, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func upsertChannelAbilities(ctx context.Context, exec sqlExecutor, channelID string, abilities []channelAbilityPayload) error {
	for _, ability := range abilities {
		modelName := strings.TrimSpace(ability.ModelName)
		endpoint := strings.TrimSpace(ability.Endpoint)
		if modelName == "" || endpoint == "" {
			return badRequest("Channel ability requires model_name and endpoint.")
		}
		if err := ensureChannelAbilityModel(ctx, exec, modelName, endpoint); err != nil {
			return err
		}
		upstreamModel := strings.TrimSpace(ability.UpstreamModel)
		if upstreamModel == "" {
			upstreamModel = modelName
		}
		transform := ability.TransformCapability
		if transform == nil {
			transform = map[string]any{"mode": "native", "lossless": true}
		}
		transformJSON, err := encodeJSON(transform)
		if err != nil {
			return err
		}
		if ability.Priority == 0 {
			ability.Priority = 100
		}
		if ability.Weight == 0 {
			ability.Weight = 1
		}
		if ability.RetryPriority == 0 {
			ability.RetryPriority = 100
		}
		if _, err := exec.ExecContext(ctx, `
			INSERT INTO channel_abilities (channel_id, model_name, endpoint, upstream_model, transform_capability_json, priority, weight, retry_priority)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
			ON CONFLICT (channel_id, model_name, endpoint) DO UPDATE SET
				upstream_model = EXCLUDED.upstream_model,
				transform_capability_json = EXCLUDED.transform_capability_json,
				priority = EXCLUDED.priority,
				weight = EXCLUDED.weight,
				retry_priority = EXCLUDED.retry_priority
		`, channelID, modelName, endpoint, upstreamModel, transformJSON, ability.Priority, ability.Weight, ability.RetryPriority); err != nil {
			return err
		}
	}
	return nil
}

func ensureChannelAbilityModel(ctx context.Context, exec sqlExecutor, modelName string, endpoint string) error {
	metadata, err := encodeJSON(map[string]any{"created_from_channel_ability": true})
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `
		INSERT INTO model_catalog (model_name, display_name, provider_hint, endpoint_capabilities, public_visible, status, metadata_json)
		VALUES ($1, $1, 'manual_channel', jsonb_build_array($2::text), false, 'active', $3::jsonb)
		ON CONFLICT (model_name) DO UPDATE SET
			endpoint_capabilities = CASE
				WHEN model_catalog.endpoint_capabilities ? $2::text THEN model_catalog.endpoint_capabilities
				ELSE model_catalog.endpoint_capabilities || jsonb_build_array($2::text)
			END,
			provider_hint = COALESCE(NULLIF(model_catalog.provider_hint, ''), 'manual_channel')
	`, modelName, endpoint, metadata)
	return err
}

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT a.id::text, a.provider_id::text, COALESCE(a.channel_id::text, ''), COALESCE(a.proxy_id::text, ''),
		       COALESCE(a.owner_user_id::text, ''), a.routing_mode,
		       a.name, a.status, a.priority, a.max_concurrency, a.metadata_json::text,
		       COALESCE(ars.active_requests, 0), ars.cooldown_until, COALESCE(ars.last_error, ''),
		       COALESCE(ars.success_count, 0), COALESCE(ars.failure_count, 0),
		       COALESCE(ars.circuit_state, 'closed'), COALESCE(ars.circuit_failure_count, 0),
		       ars.circuit_opened_at, ars.circuit_half_open_after,
		       COALESCE(aas.auth_mode, ''), COALESCE(aas.auth_status, ''), COALESCE(aas.provider_subject, ''),
		       COALESCE(aas.scopes_json::text, '[]'), aas.expires_at, aas.refresh_due_at, aas.last_refresh_at,
		       COALESCE(aas.last_error, ''),
		       COALESCE(pool_groups.pool_groups::text, '[]'), COALESCE(quota_summary.quota_summary::text, '[]'),
		       COALESCE(last_quota.status, ''), COALESCE(last_quota.error_message, ''), last_quota.created_at,
		       a.created_at, a.updated_at
		FROM accounts a
		LEFT JOIN account_runtime_states ars ON ars.account_id = a.id
		LEFT JOIN account_auth_states aas ON aas.account_id = a.id
		LEFT JOIN LATERAL (
		  SELECT jsonb_agg(jsonb_build_object('id', g.id::text, 'name', g.name, 'status', g.status) ORDER BY g.priority, g.name) AS pool_groups
		  FROM account_pool_group_members m
		  JOIN account_pool_groups g ON g.id = m.group_id
		  WHERE m.account_id = a.id
		) pool_groups ON true
		LEFT JOIN LATERAL (
		  SELECT jsonb_agg(jsonb_build_object(
		    'window_type', qw.window_type,
		    'remaining', COALESCE(qw.remaining::text, ''),
		    'reset_at', qw.reset_at,
		    'metadata', qw.metadata_json
		  ) ORDER BY qw.created_at DESC) AS quota_summary
		  FROM account_quota_windows qw
		  WHERE qw.account_id = a.id
		) quota_summary ON true
		LEFT JOIN LATERAL (
		  SELECT status, error_message, created_at
		  FROM account_quota_snapshots
		  WHERE account_id = a.id
		  ORDER BY created_at DESC
		  LIMIT 1
		) last_quota ON true
		ORDER BY a.priority, a.created_at DESC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, providerID, channelID, proxyID, ownerUserID, routingMode, name, status, metadata, lastError string
		var circuitState string
		var authMode, authStatus, providerSubject, scopes, authLastError string
		var poolGroups, quotaSummary, lastQuotaStatus, lastQuotaError string
		var priority, maxConcurrency, activeRequests int
		var successCount, failureCount, circuitFailureCount int64
		var cooldownUntil, circuitOpenedAt, circuitHalfOpenAfter, authExpiresAt, refreshDueAt, lastRefreshAt, lastQuotaAt sql.NullTime
		var createdAt, updatedAt time.Time
		if err := rows.Scan(
			&id, &providerID, &channelID, &proxyID, &ownerUserID, &routingMode, &name, &status, &priority, &maxConcurrency, &metadata,
			&activeRequests, &cooldownUntil, &lastError, &successCount, &failureCount,
			&circuitState, &circuitFailureCount, &circuitOpenedAt, &circuitHalfOpenAfter,
			&authMode, &authStatus, &providerSubject, &scopes, &authExpiresAt, &refreshDueAt, &lastRefreshAt, &authLastError,
			&poolGroups, &quotaSummary, &lastQuotaStatus, &lastQuotaError, &lastQuotaAt,
			&createdAt, &updatedAt,
		); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":              id,
			"provider_id":     providerID,
			"channel_id":      channelID,
			"proxy_id":        proxyID,
			"owner_user_id":   ownerUserID,
			"routing_mode":    routingMode,
			"name":            name,
			"status":          status,
			"priority":        priority,
			"max_concurrency": maxConcurrency,
			"metadata":        jsonRaw(metadata),
			"runtime": map[string]any{
				"active_requests":         activeRequests,
				"cooldown_until":          nullableSQLTime(cooldownUntil),
				"last_error":              lastError,
				"success_count":           successCount,
				"failure_count":           failureCount,
				"circuit_state":           circuitState,
				"circuit_failure_count":   circuitFailureCount,
				"circuit_opened_at":       nullableSQLTime(circuitOpenedAt),
				"circuit_half_open_after": nullableSQLTime(circuitHalfOpenAfter),
			},
			"auth": map[string]any{
				"auth_mode":        authMode,
				"auth_status":      authStatus,
				"provider_subject": providerSubject,
				"scopes":           jsonArrayRaw(scopes),
				"expires_at":       nullableSQLTime(authExpiresAt),
				"refresh_due_at":   nullableSQLTime(refreshDueAt),
				"last_refresh_at":  nullableSQLTime(lastRefreshAt),
				"last_error":       authLastError,
			},
			"pool_groups":   jsonArrayRaw(poolGroups),
			"quota_summary": jsonArrayRaw(quotaSummary),
			"last_quota_refresh": map[string]any{
				"status":        lastQuotaStatus,
				"error_message": lastQuotaError,
				"created_at":    nullableSQLTime(lastQuotaAt),
			},
			"created_at": createdAt.UTC().Format(time.RFC3339),
			"updated_at": updatedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateAccount(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		ProviderID     string         `json:"provider_id"`
		ChannelID      string         `json:"channel_id"`
		ProxyID        string         `json:"proxy_id"`
		OwnerUserID    string         `json:"owner_user_id"`
		RoutingMode    string         `json:"routing_mode"`
		Name           string         `json:"name"`
		APIKey         string         `json:"api_key"`
		TokenBundle    tokenBundle    `json:"token_bundle"`
		AuthMode       string         `json:"auth_mode"`
		Status         string         `json:"status"`
		Priority       int            `json:"priority"`
		MaxConcurrency int            `json:"max_concurrency"`
		Metadata       map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.ProviderID) == "" || strings.TrimSpace(req.Name) == "" {
		return badRequest("provider_id and name are required.")
	}
	routingMode, err := routingModeValue(req.RoutingMode)
	if err != nil {
		return err
	}
	ownerUserID := strings.TrimSpace(req.OwnerUserID)
	if err := requireBYOOwner(routingMode, ownerUserID); err != nil {
		return err
	}
	bundle, hasAuthState, err := parseAccountSecret(req.APIKey, req.TokenBundle)
	if err != nil {
		return err
	}
	status, err := defaultedStatus(req.Status, "active", "active", "disabled", "cooldown", "exhausted")
	if err != nil {
		return err
	}
	if req.MaxConcurrency == 0 {
		req.MaxConcurrency = 10
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
	vaultID, err := s.storeCredentialBundle(r.Context(), tx, bundle)
	if err != nil {
		return err
	}
	var channel any
	if req.ChannelID != "" {
		channel = req.ChannelID
	}
	var proxy any
	if strings.TrimSpace(req.ProxyID) != "" {
		proxy = strings.TrimSpace(req.ProxyID)
	}
	var owner any
	if ownerUserID != "" {
		owner = ownerUserID
	}
	var id string
	if err := tx.QueryRowContext(r.Context(), `
		INSERT INTO accounts (provider_id, channel_id, proxy_id, owner_user_id, routing_mode, credential_vault_record_id, name, status, priority, max_concurrency, metadata_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb)
		RETURNING id::text
	`, req.ProviderID, channel, proxy, owner, routingMode, vaultID, strings.TrimSpace(req.Name), status, req.Priority, req.MaxConcurrency, metadata).Scan(&id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(r.Context(), "INSERT INTO account_runtime_states (account_id) VALUES ($1)", id); err != nil {
		return err
	}
	if hasAuthState {
		if err := s.upsertAccountAuthState(r.Context(), tx, id, req.AuthMode, "active", bundle, ""); err != nil {
			return err
		}
	}
	if group := metadataText(req.Metadata["pool_group"]); group != "" {
		if err := s.setAccountPoolGroupByNameTx(r.Context(), tx, id, group); err != nil {
			return err
		}
	}
	audit(r.Context(), tx, auth.UserID, "admin", "account.create", "account", id, r, nil)
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminExportAccounts(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT a.id::text, a.provider_id::text, COALESCE(a.channel_id::text, ''), COALESCE(a.proxy_id::text, ''),
		       COALESCE(a.owner_user_id::text, ''), a.routing_mode, a.name, a.status, a.priority,
		       a.max_concurrency, a.metadata_json::text, a.created_at, a.updated_at
		FROM accounts a
		ORDER BY a.priority, a.created_at DESC
		LIMIT $1
	`, limitFromRequest(r, 500, 2000))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, providerID, channelID, proxyID, ownerUserID, routingMode, name, status, metadata string
		var priority, maxConcurrency int
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &providerID, &channelID, &proxyID, &ownerUserID, &routingMode, &name, &status, &priority, &maxConcurrency, &metadata, &createdAt, &updatedAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":              id,
			"provider_id":     providerID,
			"channel_id":      channelID,
			"proxy_id":        proxyID,
			"owner_user_id":   ownerUserID,
			"routing_mode":    routingMode,
			"name":            name,
			"status":          status,
			"priority":        priority,
			"max_concurrency": maxConcurrency,
			"metadata":        jsonRaw(metadata),
			"created_at":      createdAt.UTC().Format(time.RFC3339),
			"updated_at":      updatedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "exported_at": time.Now().UTC().Format(time.RFC3339)}, nil)
	return nil
}

func (s *Server) adminImportAccounts(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req accountImportRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	accountIDs, summary, err := s.importAccountsFromRequest(r.Context(), tx, req)
	if err != nil {
		return err
	}
	imported := len(accountIDs)
	audit(r.Context(), tx, auth.UserID, "admin", "account.import", "account", "", r, map[string]any{"count": imported, "summary": summary})
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"imported": imported, "account_ids": accountIDs}, nil)
	return nil
}

func (s *Server) adminAccountBatch(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		AccountIDs []string       `json:"account_ids"`
		Action     string         `json:"action"`
		Group      string         `json:"group"`
		Tags       []string       `json:"tags"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.AccountIDs) == 0 {
		return badRequest("account_ids is required.")
	}
	if len(req.AccountIDs) > 200 {
		return badRequest("account_ids cannot exceed 200.")
	}
	action := strings.TrimSpace(req.Action)
	if action == "" {
		return badRequest("action is required.")
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated := int64(0)
	for _, rawID := range req.AccountIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return badRequest("account_ids cannot contain empty values.")
		}
		var result sql.Result
		switch action {
		case "enable":
			result, err = tx.ExecContext(r.Context(), "UPDATE accounts SET status = 'active' WHERE id = $1", id)
		case "disable":
			result, err = tx.ExecContext(r.Context(), "UPDATE accounts SET status = 'disabled' WHERE id = $1", id)
		case "cooldown":
			result, err = tx.ExecContext(r.Context(), "UPDATE accounts SET status = 'cooldown' WHERE id = $1", id)
		case "exhausted":
			result, err = tx.ExecContext(r.Context(), "UPDATE accounts SET status = 'exhausted' WHERE id = $1", id)
		case "wakeup":
			result, err = tx.ExecContext(r.Context(), `
				UPDATE accounts SET status = 'active' WHERE id = $1
			`, id)
			if err == nil {
				_, err = tx.ExecContext(r.Context(), `
					UPDATE account_runtime_states
					SET cooldown_until = NULL,
					    last_error = '',
					    failure_count = 0,
					    circuit_state = 'closed',
					    circuit_failure_count = 0,
					    circuit_opened_at = NULL,
					    circuit_half_open_after = NULL
					WHERE account_id = $1
				`, id)
			}
		case "set_group":
			if strings.TrimSpace(req.Group) == "" {
				return badRequest("group is required.")
			}
			err = s.setAccountPoolGroupByNameTx(r.Context(), tx, id, req.Group)
			if err == nil {
				result, err = tx.ExecContext(r.Context(), "UPDATE accounts SET metadata_json = metadata_json WHERE id = $1", id)
			}
		case "set_tags":
			tags, err := encodeJSON(normalizeStringList(req.Tags))
			if err != nil {
				return err
			}
			result, err = tx.ExecContext(r.Context(), `
				UPDATE accounts
				SET metadata_json = jsonb_set(metadata_json, '{route_tags}', $2::jsonb, true)
				WHERE id = $1
			`, id, tags)
		case "patch_metadata":
			metadata, err := encodeJSON(defaultMap(req.Metadata))
			if err != nil {
				return err
			}
			result, err = tx.ExecContext(r.Context(), "UPDATE accounts SET metadata_json = metadata_json || $2::jsonb WHERE id = $1", id, metadata)
		default:
			return badRequest("Unsupported account batch action.")
		}
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		updated += count
	}
	audit(r.Context(), tx, auth.UserID, "admin", "account.batch_update", "account", "", r, map[string]any{"action": action, "count": updated})
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated, "action": action}, nil)
	return nil
}

func (s *Server) adminAccountHealthCheck(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		AccountIDs []string `json:"account_ids"`
		ModelName  string   `json:"model_name"`
		Endpoint   string   `json:"endpoint"`
		TargetPath string   `json:"target_path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.AccountIDs) == 0 {
		return badRequest("account_ids is required.")
	}
	if len(req.AccountIDs) > 50 {
		return badRequest("account_ids cannot exceed 50.")
	}
	results := []map[string]any{}
	for _, rawID := range req.AccountIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return badRequest("account_ids cannot contain empty values.")
		}
		result, err := s.runChannelCheck(r, "", id, req.ModelName, req.Endpoint, req.TargetPath)
		if err != nil {
			return err
		}
		results = append(results, result)
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "account.health_check", "account", "", r, map[string]any{"count": len(results)})
	writeJSON(w, http.StatusOK, map[string]any{"results": results}, nil)
	return nil
}

func (s *Server) adminAccountPoolGroups(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT g.id::text, g.name, g.description, g.status, g.priority, g.default_route_tags_json::text,
		       g.default_metadata_json::text, g.created_at, g.updated_at,
		       COUNT(a.id) AS account_count,
		       COUNT(a.id) FILTER (WHERE a.status = 'active') AS active_accounts,
		       COUNT(a.id) FILTER (WHERE a.id IS NOT NULL AND a.status <> 'active') AS abnormal_accounts,
		       COALESCE(jsonb_agg(jsonb_build_object('account_id', a.id::text, 'name', a.name, 'status', a.status) ORDER BY a.created_at DESC) FILTER (WHERE a.id IS NOT NULL), '[]'::jsonb)::text AS members
		FROM account_pool_groups g
		LEFT JOIN account_pool_group_members m ON m.group_id = g.id
		LEFT JOIN accounts a ON a.id = m.account_id
		GROUP BY g.id
		UNION ALL
		SELECT '' AS id, '未分组' AS name, '' AS description, 'active' AS status, 999999 AS priority,
		       '[]'::text AS default_route_tags_json, '{}'::text AS default_metadata_json, now() AS created_at, now() AS updated_at,
		       COUNT(a.id) AS account_count,
		       COUNT(a.id) FILTER (WHERE a.status = 'active') AS active_accounts,
		       COUNT(a.id) FILTER (WHERE a.status <> 'active') AS abnormal_accounts,
		       COALESCE(jsonb_agg(jsonb_build_object('account_id', a.id::text, 'name', a.name, 'status', a.status) ORDER BY a.created_at DESC), '[]'::jsonb)::text AS members
		FROM accounts a
		WHERE NOT EXISTS (SELECT 1 FROM account_pool_group_members m WHERE m.account_id = a.id)
		ORDER BY priority, name
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, groupName, description, status, tags, metadata, members string
		var accountCount, activeAccounts, abnormalAccounts int
		var priority int
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &groupName, &description, &status, &priority, &tags, &metadata, &createdAt, &updatedAt, &accountCount, &activeAccounts, &abnormalAccounts, &members); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":                 id,
			"group_name":         groupName,
			"name":               groupName,
			"description":        description,
			"status":             status,
			"priority":           priority,
			"account_count":      accountCount,
			"active_accounts":    activeAccounts,
			"abnormal_accounts":  abnormalAccounts,
			"default_route_tags": jsonArrayRaw(tags),
			"default_metadata":   jsonRaw(metadata),
			"members":            jsonArrayRaw(members),
			"created_at":         createdAt.UTC().Format(time.RFC3339),
			"updated_at":         updatedAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminPatchAccount(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id := r.PathValue("accountId")
	var req struct {
		Name           *string        `json:"name"`
		APIKey         *string        `json:"api_key"`
		TokenBundle    *tokenBundle   `json:"token_bundle"`
		AuthMode       *string        `json:"auth_mode"`
		OwnerUserID    *string        `json:"owner_user_id"`
		RoutingMode    *string        `json:"routing_mode"`
		Status         *string        `json:"status"`
		Priority       *int           `json:"priority"`
		MaxConcurrency *int           `json:"max_concurrency"`
		ChannelID      *string        `json:"channel_id"`
		ProxyID        *string        `json:"proxy_id"`
		Metadata       map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := s.requireIDExists(r.Context(), "accounts", id, "Account was not found."); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if req.Name != nil {
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET name = $2 WHERE id = $1", id, strings.TrimSpace(*req.Name)); err != nil {
			return err
		}
	}
	if req.APIKey != nil {
		bundle, hasAuthState, err := parseAccountSecret(*req.APIKey, tokenBundle{})
		if err != nil {
			return err
		}
		vaultID, err := s.storeCredentialBundle(r.Context(), tx, bundle)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET credential_vault_record_id = $2 WHERE id = $1", id, vaultID); err != nil {
			return err
		}
		if hasAuthState {
			authMode := "oauth"
			if req.AuthMode != nil {
				authMode = *req.AuthMode
			}
			if err := s.upsertAccountAuthState(r.Context(), tx, id, authMode, "active", bundle, ""); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(r.Context(), "DELETE FROM account_auth_states WHERE account_id = $1", id); err != nil {
			return err
		}
	}
	if req.TokenBundle != nil {
		bundle, _, err := parseAccountSecret("", *req.TokenBundle)
		if err != nil {
			return err
		}
		vaultID, err := s.storeCredentialBundle(r.Context(), tx, bundle)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET credential_vault_record_id = $2 WHERE id = $1", id, vaultID); err != nil {
			return err
		}
		authMode := "oauth"
		if req.AuthMode != nil {
			authMode = *req.AuthMode
		}
		if err := s.upsertAccountAuthState(r.Context(), tx, id, authMode, "active", bundle, ""); err != nil {
			return err
		}
	}
	if req.RoutingMode != nil || req.OwnerUserID != nil {
		var currentRoutingMode, currentOwnerUserID string
		if err := tx.QueryRowContext(r.Context(), `
			SELECT routing_mode, COALESCE(owner_user_id::text, '') FROM accounts WHERE id = $1
		`, id).Scan(&currentRoutingMode, &currentOwnerUserID); err != nil {
			return err
		}
		if req.RoutingMode != nil {
			currentRoutingMode, err = routingModeValue(*req.RoutingMode)
			if err != nil {
				return err
			}
		}
		if req.OwnerUserID != nil {
			currentOwnerUserID = strings.TrimSpace(*req.OwnerUserID)
		}
		if err := requireBYOOwner(currentRoutingMode, currentOwnerUserID); err != nil {
			return err
		}
		var owner any
		if currentOwnerUserID != "" {
			owner = currentOwnerUserID
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET routing_mode = $2, owner_user_id = $3 WHERE id = $1", id, currentRoutingMode, owner); err != nil {
			return err
		}
	}
	if req.Status != nil {
		status, err := defaultedStatus(*req.Status, "active", "active", "disabled", "cooldown", "exhausted")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET status = $2 WHERE id = $1", id, status); err != nil {
			return err
		}
	}
	if req.Priority != nil {
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET priority = $2 WHERE id = $1", id, *req.Priority); err != nil {
			return err
		}
	}
	if req.MaxConcurrency != nil {
		if *req.MaxConcurrency <= 0 {
			return badRequest("max_concurrency must be positive.")
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET max_concurrency = $2 WHERE id = $1", id, *req.MaxConcurrency); err != nil {
			return err
		}
	}
	if req.ChannelID != nil {
		var channel any
		if strings.TrimSpace(*req.ChannelID) != "" {
			channel = strings.TrimSpace(*req.ChannelID)
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET channel_id = $2 WHERE id = $1", id, channel); err != nil {
			return err
		}
	}
	if req.ProxyID != nil {
		var proxy any
		if strings.TrimSpace(*req.ProxyID) != "" {
			proxy = strings.TrimSpace(*req.ProxyID)
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET proxy_id = $2 WHERE id = $1", id, proxy); err != nil {
			return err
		}
	}
	if req.Metadata != nil {
		metadata, err := encodeJSON(defaultMap(req.Metadata))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE accounts SET metadata_json = $2::jsonb WHERE id = $1", id, metadata); err != nil {
			return err
		}
		if group := metadataText(req.Metadata["pool_group"]); group != "" {
			if err := s.setAccountPoolGroupByNameTx(r.Context(), tx, id, group); err != nil {
				return err
			}
		}
	}
	audit(r.Context(), tx, auth.UserID, "admin", "account.update", "account", id, r, nil)
	if err := tx.Commit(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) adminProxies(w http.ResponseWriter, r *http.Request, auth authContext) error {
	items, err := s.listProxyRows(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateProxy(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		Name     string         `json:"name"`
		ProxyURL string         `json:"proxy_url"`
		Status   string         `json:"status"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.ProxyURL) == "" {
		return badRequest("name and proxy_url are required.")
	}
	status, err := defaultedStatus(req.Status, "active", "active", "disabled")
	if err != nil {
		return err
	}
	metadata, err := encodeJSON(defaultMap(req.Metadata))
	if err != nil {
		return err
	}
	proxyURL, err := validateProxyURL(req.ProxyURL)
	if err != nil {
		return err
	}
	var id string
	if err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO proxies (name, proxy_url, status, metadata_json)
		VALUES ($1, $2, $3, $4::jsonb)
		RETURNING id::text
	`, strings.TrimSpace(req.Name), proxyURL, status, metadata).Scan(&id); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "proxy.create", "proxy", id, r, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
	return nil
}

func (s *Server) adminPatchProxy(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id := r.PathValue("proxyId")
	var req struct {
		Name     *string        `json:"name"`
		ProxyURL *string        `json:"proxy_url"`
		Status   *string        `json:"status"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := s.requireIDExists(r.Context(), "proxies", id, "Proxy was not found."); err != nil {
		return err
	}
	if req.Name != nil {
		if _, err := s.db.ExecContext(r.Context(), "UPDATE proxies SET name = $2 WHERE id = $1", id, strings.TrimSpace(*req.Name)); err != nil {
			return err
		}
	}
	if req.ProxyURL != nil {
		proxyURL, err := validateProxyURL(*req.ProxyURL)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE proxies SET proxy_url = $2 WHERE id = $1", id, proxyURL); err != nil {
			return err
		}
	}
	if req.Status != nil {
		status, err := defaultedStatus(*req.Status, "active", "active", "disabled")
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE proxies SET status = $2 WHERE id = $1", id, status); err != nil {
			return err
		}
	}
	if req.Metadata != nil {
		metadata, err := encodeJSON(defaultMap(req.Metadata))
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), "UPDATE proxies SET metadata_json = $2::jsonb WHERE id = $1", id, metadata); err != nil {
			return err
		}
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "proxy.update", "proxy", id, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) adminDeleteProxy(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id := r.PathValue("proxyId")
	if err := s.requireIDExists(r.Context(), "proxies", id, "Proxy was not found."); err != nil {
		return err
	}
	var accountCount, channelCount int
	if err := s.db.QueryRowContext(r.Context(), `
		SELECT
			(SELECT COUNT(*) FROM accounts WHERE proxy_id = $1),
			(SELECT COUNT(*) FROM channels WHERE proxy_id = $1)
	`, id).Scan(&accountCount, &channelCount); err != nil {
		return err
	}
	if accountCount > 0 || channelCount > 0 {
		return conflict(fmt.Sprintf("该代理仍被 %d 个账号、%d 个通道使用，先解绑再删除。", accountCount, channelCount))
	}
	result, err := s.db.ExecContext(r.Context(), "DELETE FROM proxies WHERE id = $1", id)
	if err != nil {
		return err
	}
	deleted, _ := result.RowsAffected()
	audit(r.Context(), s.db, auth.UserID, "admin", "proxy.delete", "proxy", id, r, map[string]any{"deleted": deleted})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": deleted > 0}, nil)
	return nil
}

func (s *Server) adminRouteExplain(w http.ResponseWriter, r *http.Request, auth authContext) error {
	model := r.URL.Query().Get("model")
	endpoint := r.URL.Query().Get("endpoint")
	if model == "" || endpoint == "" {
		return badRequest("model and endpoint query parameters are required.")
	}
	routingMode, err := routingModeValue(r.URL.Query().Get("routing_mode"))
	if err != nil {
		return err
	}
	ownerUserID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if routingMode == "byo" && ownerUserID == "" {
		return badRequest("user_id is required when routing_mode=byo.")
	}

	affinity := routeExplainAffinity{
		RoutingMode: routingMode,
		UserID:      ownerUserID,
		APIKeyID:    strings.TrimSpace(r.URL.Query().Get("api_key_id")),
		AffinityKey: strings.TrimSpace(r.URL.Query().Get("affinity_key")),
		RouteTags:   appendRouteTags(nil, r.URL.Query().Get("route_tags")),
	}
	candidates, err := s.routeCandidates(r.Context(), model, endpoint, affinity)
	if err != nil {
		return err
	}
	var selected any
	for _, candidate := range candidates {
		if eligible, _ := candidate["eligible"].(bool); eligible {
			selected = candidate
			break
		}
	}
	if selected == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available":    false,
			"error_code":   "no_available_account",
			"message":      "No valid upstream account is available.",
			"routing_mode": routingMode,
			"owner_scope":  ownerUserID,
			"route_tags":   affinity.RouteTags,
			"candidates":   candidates,
		}, nil)
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available":    true,
		"routing_mode": routingMode,
		"owner_scope":  ownerUserID,
		"route_tags":   affinity.RouteTags,
		"selected":     selected,
		"candidates":   candidates,
	}, nil)
	return nil
}

func (s *Server) listSimple(r *http.Request, table string, columns string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(r.Context(), "SELECT "+columns+" FROM "+table+" ORDER BY created_at DESC LIMIT $1", limitFromRequest(r, 100, 500))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var a, b, c, d, e string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&a, &b, &c, &d, &e, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":          a,
			"name":        b,
			"type_or_url": c,
			"status":      d,
			"metadata":    jsonRaw(e),
			"created_at":  createdAt.UTC().Format(time.RFC3339),
			"updated_at":  updatedAt.UTC().Format(time.RFC3339),
		})
	}
	return items, rows.Err()
}
