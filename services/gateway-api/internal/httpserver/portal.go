package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

type authRequest struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	DisplayName      string `json:"display_name"`
	VerificationCode string `json:"verification_code"`
}

func (s *Server) portalRegister(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	email := trimEmail(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		writeError(w, r, badRequest("A valid email is required."))
		return
	}
	if len(req.Password) < 8 {
		writeError(w, r, badRequest("Password must be at least 8 characters."))
		return
	}
	if s.registrationEmailVerificationEnabled(r.Context()) {
		if err := s.consumeRegistrationEmailCode(r.Context(), email, req.VerificationCode); err != nil {
			writeError(w, r, err)
			return
		}
	}

	passwordHash, err := security.HashPassword(req.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer tx.Rollback()

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = email
	}

	var userID string
	err = tx.QueryRowContext(r.Context(), `
		INSERT INTO users (user_type, email, password_hash, display_name, status)
		VALUES ('personal_user', $1, $2, $3, 'active')
		RETURNING id::text
	`, email, passwordHash, displayName).Scan(&userID)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			writeError(w, r, conflict("Email is already registered."))
			return
		}
		writeError(w, r, err)
		return
	}

	if _, err := tx.ExecContext(r.Context(), "INSERT INTO wallet_accounts (user_id) VALUES ($1)", userID); err != nil {
		writeError(w, r, err)
		return
	}

	verificationToken, verificationExpiresAt, err := createUserSecurityToken(r.Context(), tx, userID, emailVerifyTokenType, emailVerificationTTL)
	if err != nil {
		writeError(w, r, err)
		return
	}

	audit(r.Context(), tx, userID, "personal_user", "user.register", "user", userID, r, map[string]any{"email": email})

	if err := tx.Commit(); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.deliverEmailVerificationEmail(r.Context(), email, verificationToken, verificationExpiresAt); err != nil {
		s.handleSecurityEmailFailure(r.Context(), userID, "email_verification", err)
	}

	auth, err := s.loginUser(r.Context(), email, req.Password, "portal")
	if err != nil {
		writeError(w, r, err)
		return
	}

	s.setSessionCookie(w, "portal", auth.Token, auth.ExpiresAt)
	if s.exposesSecurityTokens() {
		auth.Response["email_verification_token"] = verificationToken
	}
	writeJSON(w, http.StatusCreated, auth.Response, nil)
}

func (s *Server) portalLogin(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	auth, err := s.loginUser(r.Context(), trimEmail(req.Email), req.Password, "portal")
	if err != nil {
		writeError(w, r, err)
		return
	}

	s.setSessionCookie(w, "portal", auth.Token, auth.ExpiresAt)
	audit(r.Context(), s.db, auth.UserID, "personal_user", "user.login", "user", auth.UserID, r, nil)
	writeJSON(w, http.StatusOK, auth.Response, nil)
}

func (s *Server) unifiedLogin(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}

	audience, auth, err := s.loginUserByRole(r.Context(), trimEmail(req.Email), req.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}

	s.setSessionCookie(w, audience, auth.Token, auth.ExpiresAt)
	action := "user.login"
	if audience == "admin" {
		action = "admin.login"
	}
	audit(r.Context(), s.db, auth.UserID, auth.UserType, action, "user", auth.UserID, r, nil)
	writeJSON(w, http.StatusOK, auth.Response, nil)
}

type loginResult struct {
	UserID    string
	UserType  string
	Token     string
	ExpiresAt time.Time
	Response  map[string]any
}

func (s *Server) loginUser(ctx context.Context, email string, password string, audience string) (loginResult, error) {
	user, err := s.loadLoginUser(ctx, email, password)
	if err != nil {
		return loginResult{}, err
	}
	if audience == "portal" && user.UserType != "personal_user" {
		return loginResult{}, forbidden("Portal is only available to personal users.")
	}
	if audience == "admin" && user.UserType != "operator" && user.UserType != "platform_owner" {
		return loginResult{}, forbidden("Admin permission is required.")
	}
	return s.createLoginResult(ctx, user, audience)
}

func (s *Server) loginUserByRole(ctx context.Context, email string, password string) (string, loginResult, error) {
	user, err := s.loadLoginUser(ctx, email, password)
	if err != nil {
		return "", loginResult{}, err
	}
	audience, ok := workspaceForUserType(user.UserType)
	if !ok {
		return "", loginResult{}, forbidden("Admin permission is required.")
	}
	auth, err := s.createLoginResult(ctx, user, audience)
	return audience, auth, err
}

type loginUserRecord struct {
	UserID          string
	UserType        string
	Email           string
	DisplayName     string
	Status          string
	EmailVerifiedAt sql.NullTime
}

func (s *Server) loadLoginUser(ctx context.Context, email string, password string) (loginUserRecord, error) {
	var userID, userType, passwordHash, displayName, status string
	var emailVerifiedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text, user_type, password_hash, display_name, status, email_verified_at
		FROM users
		WHERE email = $1
	`, email).Scan(&userID, &userType, &passwordHash, &displayName, &status, &emailVerifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return loginUserRecord{}, unauthorized("Invalid email or password.")
	}
	if err != nil {
		return loginUserRecord{}, err
	}
	if status != "active" {
		return loginUserRecord{}, forbidden("User is disabled.")
	}
	if !security.CheckPassword(passwordHash, password) {
		return loginUserRecord{}, unauthorized("Invalid email or password.")
	}

	return loginUserRecord{
		UserID:          userID,
		UserType:        userType,
		Email:           email,
		DisplayName:     displayName,
		Status:          status,
		EmailVerifiedAt: emailVerifiedAt,
	}, nil
}

func (s *Server) createLoginResult(ctx context.Context, user loginUserRecord, audience string) (loginResult, error) {
	sessionID, token, csrfToken, expiresAt, err := s.createSession(ctx, user.UserID, audience)
	if err != nil {
		return loginResult{}, err
	}

	_, _ = s.db.ExecContext(ctx, "UPDATE users SET last_login_at = now() WHERE id = $1", user.UserID)

	return loginResult{
		UserID:    user.UserID,
		UserType:  user.UserType,
		Token:     token,
		ExpiresAt: expiresAt,
		Response: map[string]any{
			"user": map[string]any{
				"id":                user.UserID,
				"user_type":         user.UserType,
				"email":             user.Email,
				"display_name":      user.DisplayName,
				"status":            user.Status,
				"email_verified_at": nullableSQLTime(user.EmailVerifiedAt),
			},
			"workspace": audience,
			"session": map[string]any{
				"session_id":    sessionID,
				"session_token": token,
				"csrf_token":    csrfToken,
				"expires_at":    expiresAt.UTC().Format(time.RFC3339),
				"audience":      audience,
			},
		},
	}, nil
}

func workspaceForUserType(userType string) (string, bool) {
	switch userType {
	case "personal_user":
		return "portal", true
	case "operator", "platform_owner":
		return "admin", true
	default:
		return "", false
	}
}

func (s *Server) portalLogout(w http.ResponseWriter, r *http.Request, auth authContext) error {
	_, err := s.db.ExecContext(r.Context(), "UPDATE user_sessions SET revoked_at = now() WHERE session_id = $1", auth.SessionID)
	if err != nil {
		return err
	}
	s.clearSessionCookie(w, "portal")
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true}, nil)
	return nil
}

func (s *Server) portalMe(w http.ResponseWriter, r *http.Request, auth authContext) error {
	writeJSON(w, http.StatusOK, userResponse(auth), nil)
	return nil
}

func (s *Server) portalWallet(w http.ResponseWriter, r *http.Request, auth authContext) error {
	if err := s.ensureWalletForUser(r.Context(), auth.UserID); err != nil {
		return err
	}
	wallet, err := s.walletForUser(r.Context(), auth.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, wallet, nil)
	return nil
}

func (s *Server) portalWalletLedger(w http.ResponseWriter, r *http.Request, auth authContext) error {
	limit := limitFromRequest(r, 50, 200)
	items, err := s.walletLedgerForUser(r.Context(), auth.UserID, limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) walletLedgerForUser(ctx context.Context, userID string, limit int) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id::text, l.entry_type, l.amount::text, l.balance_after::text, l.reserved_after::text,
		       l.reference_type, l.reference_id, l.metadata_json::text, l.created_at
		FROM wallet_ledgers l
		JOIN wallet_accounts w ON w.id = l.wallet_account_id
		WHERE w.user_id = $1
		ORDER BY l.created_at DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, entryType, amount, balanceAfter, reservedAfter, referenceType, referenceID, metadata string
		var createdAt time.Time
		if err := rows.Scan(&id, &entryType, &amount, &balanceAfter, &reservedAfter, &referenceType, &referenceID, &metadata, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":             id,
			"entry_type":     entryType,
			"amount":         amount,
			"balance_after":  balanceAfter,
			"reserved_after": reservedAfter,
			"reference_type": referenceType,
			"reference_id":   referenceID,
			"metadata":       jsonRaw(metadata),
			"created_at":     createdAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Server) portalRedeem(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		return badRequest("Redeem code is required.")
	}
	if err := s.ensureWalletForUser(r.Context(), auth.UserID); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var redeemID, grantValue, currency, status string
	var maxClaims, claimCount int
	var expiresAt sql.NullTime
	err = tx.QueryRowContext(r.Context(), `
		SELECT id::text, grant_value::text, currency, max_claims, claim_count, expires_at, status
		FROM redeem_codes
		WHERE code_hash = $1
		FOR UPDATE
	`, security.HashSecret(code)).Scan(&redeemID, &grantValue, &currency, &maxClaims, &claimCount, &expiresAt, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("Redeem code was not found.")
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return conflict("Redeem code is not active.")
	}
	if expiresAt.Valid && expiresAt.Time.Before(time.Now()) {
		return conflict("Redeem code has expired.")
	}
	if claimCount >= maxClaims {
		return conflict("Redeem code has no remaining claims.")
	}

	var duplicate bool
	if err := tx.QueryRowContext(r.Context(), `
		SELECT EXISTS (SELECT 1 FROM redeem_claims WHERE redeem_code_id = $1 AND user_id = $2)
	`, redeemID, auth.UserID).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return conflict("Redeem code has already been claimed by this user.")
	}

	var walletID, balanceAfter, reservedAfter string
	err = tx.QueryRowContext(r.Context(), `
		UPDATE wallet_accounts
		SET balance = balance + $2::numeric
		WHERE user_id = $1 AND status = 'active'
		RETURNING id::text, balance::text, reserved_balance::text
	`, auth.UserID, grantValue).Scan(&walletID, &balanceAfter, &reservedAfter)
	if err != nil {
		return err
	}

	var ledgerID string
	err = tx.QueryRowContext(r.Context(), `
		INSERT INTO wallet_ledgers (wallet_account_id, entry_type, amount, balance_after, reserved_after, reference_type, reference_id)
		VALUES ($1, 'redeem', $2::numeric, $3::numeric, $4::numeric, 'redeem_code', $5)
		RETURNING id::text
	`, walletID, grantValue, balanceAfter, reservedAfter, redeemID).Scan(&ledgerID)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO redeem_claims (redeem_code_id, user_id, wallet_ledger_id)
		VALUES ($1, $2, $3)
	`, redeemID, auth.UserID, ledgerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE redeem_codes SET claim_count = claim_count + 1 WHERE id = $1
	`, redeemID); err != nil {
		return err
	}

	audit(r.Context(), tx, auth.UserID, "personal_user", "wallet.redeem", "redeem_code", redeemID, r, map[string]any{"amount": grantValue})

	if err := tx.Commit(); err != nil {
		return err
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"redeem_code_id": redeemID,
		"amount":         grantValue,
		"currency":       currency,
		"wallet": map[string]any{
			"id":               walletID,
			"balance":          balanceAfter,
			"reserved_balance": reservedAfter,
		},
	}, nil)
	return nil
}

func (s *Server) portalAPIKeys(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, display_prefix, name, status, routing_mode, expires_at, ip_allowlist_json::text, model_scope_json::text, last_used_at, created_at, updated_at
		FROM api_keys
		WHERE owner_id = $1
		ORDER BY created_at DESC
	`, auth.UserID)
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

func (s *Server) portalCreateAPIKey(w http.ResponseWriter, r *http.Request, auth authContext) error {
	var req struct {
		Name        string   `json:"name"`
		RoutingMode string   `json:"routing_mode"`
		ExpiresAt   string   `json:"expires_at"`
		IPAllowlist []string `json:"ip_allowlist"`
		ModelScope  []string `json:"model_scope"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return badRequest("API key name is required.")
	}
	routingMode, err := routingModeValue(req.RoutingMode)
	if err != nil {
		return err
	}

	var expiresAt any
	if strings.TrimSpace(req.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			return badRequest("expires_at must be RFC3339.")
		}
		expiresAt = parsed
	}

	ipAllowlist, err := encodeJSON(req.IPAllowlist)
	if err != nil {
		return err
	}
	modelScope, err := encodeJSON(req.ModelScope)
	if err != nil {
		return err
	}

	secret, err := security.NewOpaqueToken("sk-relay-", 32)
	if err != nil {
		return err
	}
	var id, createdAt string
	err = s.db.QueryRowContext(r.Context(), `
		INSERT INTO api_keys (owner_id, key_hash, display_prefix, name, routing_mode, expires_at, ip_allowlist_json, model_scope_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb)
		RETURNING id::text, created_at::text
	`, auth.UserID, security.HashSecret(secret), security.DisplayPrefix(secret), name, routingMode, expiresAt, ipAllowlist, modelScope).Scan(&id, &createdAt)
	if err != nil {
		return err
	}

	audit(r.Context(), s.db, auth.UserID, "personal_user", "api_key.create", "api_key", id, r, map[string]any{"name": name})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":             id,
		"name":           name,
		"display_prefix": security.DisplayPrefix(secret),
		"routing_mode":   routingMode,
		"secret":         secret,
		"status":         "active",
		"expires_at":     nullableTime(nil),
		"ip_allowlist":   req.IPAllowlist,
		"model_scope":    req.ModelScope,
		"created_at":     createdAt,
	}, nil)
	return nil
}

func (s *Server) portalPatchAPIKey(w http.ResponseWriter, r *http.Request, auth authContext) error {
	apiKeyID := r.PathValue("apiKeyId")
	var req struct {
		Name        *string  `json:"name"`
		Status      *string  `json:"status"`
		RoutingMode *string  `json:"routing_mode"`
		ExpiresAt   *string  `json:"expires_at"`
		IPAllowlist []string `json:"ip_allowlist"`
		ModelScope  []string `json:"model_scope"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	var name string
	var status string
	var routingMode string
	var expiresAt sql.NullTime
	var ipAllowlist, modelScope string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT name, status, routing_mode, expires_at, ip_allowlist_json::text, model_scope_json::text
		FROM api_keys WHERE id = $1 AND owner_id = $2
	`, apiKeyID, auth.UserID).Scan(&name, &status, &routingMode, &expiresAt, &ipAllowlist, &modelScope)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("API key was not found.")
	}
	if err != nil {
		return err
	}

	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			return badRequest("API key name cannot be empty.")
		}
	}
	if req.Status != nil {
		nextStatus := strings.TrimSpace(*req.Status)
		if nextStatus != "active" && nextStatus != "disabled" && nextStatus != "revoked" {
			return badRequest("Invalid API key status.")
		}
		status = nextStatus
	}
	if req.RoutingMode != nil {
		nextRoutingMode, err := routingModeValue(*req.RoutingMode)
		if err != nil {
			return err
		}
		routingMode = nextRoutingMode
	}
	var expiresValue any
	if req.ExpiresAt != nil {
		if strings.TrimSpace(*req.ExpiresAt) == "" {
			expiresValue = nil
		} else {
			parsed, err := time.Parse(time.RFC3339, *req.ExpiresAt)
			if err != nil {
				return badRequest("expires_at must be RFC3339.")
			}
			expiresValue = parsed
		}
	} else if expiresAt.Valid {
		expiresValue = expiresAt.Time
	}
	if req.IPAllowlist != nil {
		encoded, err := encodeJSON(req.IPAllowlist)
		if err != nil {
			return err
		}
		ipAllowlist = encoded
	}
	if req.ModelScope != nil {
		encoded, err := encodeJSON(req.ModelScope)
		if err != nil {
			return err
		}
		modelScope = encoded
	}

	if _, err := s.db.ExecContext(r.Context(), `
		UPDATE api_keys
		SET name = $3, status = $4, routing_mode = $5, expires_at = $6, ip_allowlist_json = $7::jsonb, model_scope_json = $8::jsonb
		WHERE id = $1 AND owner_id = $2
	`, apiKeyID, auth.UserID, name, status, routingMode, expiresValue, ipAllowlist, modelScope); err != nil {
		return err
	}

	audit(r.Context(), s.db, auth.UserID, "personal_user", "api_key.update", "api_key", apiKeyID, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": apiKeyID, "updated": true}, nil)
	return nil
}

func (s *Server) portalDeleteAPIKey(w http.ResponseWriter, r *http.Request, auth authContext) error {
	apiKeyID := r.PathValue("apiKeyId")
	result, err := s.db.ExecContext(r.Context(), `
		UPDATE api_keys SET status = 'revoked' WHERE id = $1 AND owner_id = $2
	`, apiKeyID, auth.UserID)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return notFound("API key was not found.")
	}
	audit(r.Context(), s.db, auth.UserID, "personal_user", "api_key.revoke", "api_key", apiKeyID, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": apiKeyID, "revoked": true}, nil)
	return nil
}

func (s *Server) portalUsage(w http.ResponseWriter, r *http.Request, auth authContext) error {
	limit := limitFromRequest(r, 50, 200)
	whereSQL, args, err := usageWhereFromRequest(r, []string{"user_id = $1"}, []any{auth.UserID})
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

func (s *Server) portalModels(w http.ResponseWriter, r *http.Request, auth authContext) error {
	models, err := s.listModels(r.Context(), true, auth.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, models, nil)
	return nil
}

func (s *Server) walletForUser(ctx context.Context, userID string) (map[string]any, error) {
	var id, balance, reservedBalance, currency, status string
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text, balance::text, reserved_balance::text, currency, status
		FROM wallet_accounts WHERE user_id = $1
	`, userID).Scan(&id, &balance, &reservedBalance, &currency, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("Wallet was not found.")
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

func (s *Server) ensureWalletForUser(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO wallet_accounts (user_id)
		VALUES ($1)
		ON CONFLICT (user_id) DO NOTHING
	`, userID)
	return err
}

type apiKeyScanner interface {
	Scan(dest ...any) error
}

func scanAPIKey(scanner apiKeyScanner) (map[string]any, error) {
	var id, displayPrefix, name, status, routingMode, ipAllowlist, modelScope string
	var expiresAt, lastUsedAt sql.NullTime
	var createdAt, updatedAt time.Time
	if err := scanner.Scan(&id, &displayPrefix, &name, &status, &routingMode, &expiresAt, &ipAllowlist, &modelScope, &lastUsedAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	return map[string]any{
		"id":             id,
		"display_prefix": displayPrefix,
		"name":           name,
		"status":         status,
		"routing_mode":   routingMode,
		"expires_at":     nullableTime(&expiresAt.Time),
		"ip_allowlist":   jsonArrayRaw(ipAllowlist),
		"model_scope":    jsonArrayRaw(modelScope),
		"last_used_at":   nullableTime(&lastUsedAt.Time),
		"created_at":     createdAt.UTC().Format(time.RFC3339),
		"updated_at":     updatedAt.UTC().Format(time.RFC3339),
	}, nil
}

func scanUsageRows(rows *sql.Rows) ([]map[string]any, error) {
	items := []map[string]any{}
	for rows.Next() {
		var id, requestID, requestedModel, upstreamModel, endpoint, audioSeconds, actualCost, groupID, billingMultiplier, effectivePolicy, riskDecision, status, errorCode, usageSource string
		var inputTokens, outputTokens, imageCount, requestCount, durationMS, streamEventCount, webSocketFrameCount int
		var upstreamStatus sql.NullInt64
		var createdAt time.Time
		if err := rows.Scan(&id, &requestID, &requestedModel, &upstreamModel, &endpoint, &inputTokens, &outputTokens, &imageCount, &audioSeconds, &requestCount, &actualCost, &groupID, &billingMultiplier, &effectivePolicy, &riskDecision, &status, &errorCode, &upstreamStatus, &durationMS, &usageSource, &streamEventCount, &webSocketFrameCount, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id":                    id,
			"request_id":            requestID,
			"requested_model":       requestedModel,
			"upstream_model":        upstreamModel,
			"endpoint":              endpoint,
			"input_tokens":          inputTokens,
			"output_tokens":         outputTokens,
			"image_count":           imageCount,
			"audio_seconds":         audioSeconds,
			"request_count":         requestCount,
			"actual_cost":           actualCost,
			"group_id":              groupID,
			"billing_multiplier":    billingMultiplier,
			"effective_policy":      jsonRaw(effectivePolicy),
			"risk_decision":         jsonRaw(riskDecision),
			"status":                status,
			"error_code":            errorCode,
			"upstream_status":       nullableSQLInt(upstreamStatus),
			"duration_ms":           durationMS,
			"usage_source":          usageSource,
			"stream_event_count":    streamEventCount,
			"websocket_frame_count": webSocketFrameCount,
			"created_at":            createdAt.UTC().Format(time.RFC3339),
		})
	}
	return items, rows.Err()
}

func (s *Server) listModels(ctx context.Context, publicOnly bool, userID string) ([]map[string]any, error) {
	query := `
			SELECT mc.model_name, mc.display_name, mc.provider_hint, mc.endpoint_capabilities::text,
			       mc.input_usd_per_1k::text, mc.output_usd_per_1k::text, mc.request_usd::text, mc.min_charge_usd::text,
			       COALESCE(mpo.billing_mode, 'standard'), COALESCE(mpo.billing_expr, ''),
			       COALESCE(mpo.cache_read_usd_per_1k::text, '0'), COALESCE(mpo.cache_write_usd_per_1k::text, '0'),
			       COALESCE(mpo.image_usd_per_unit::text, '0'), COALESCE(mpo.audio_usd_per_second::text, '0'),
			       mc.public_visible, mc.status, mc.metadata_json::text, mc.created_at, mc.updated_at,
			       COALESCE((SELECT jsonb_agg(alias ORDER BY alias)::text FROM model_aliases WHERE model_name = mc.model_name), '[]'),
			       COALESCE(ops.active_channel_count, 0), COALESCE(ops.active_account_count, 0),
			       COALESCE(ops.providers_json, '[]'), COALESCE(ops.endpoints_json, '[]'),
			       COALESCE(health.health_json, '{}')
			FROM model_catalog mc
			LEFT JOIN model_pricing_overrides mpo ON mpo.model_name = mc.model_name
			LEFT JOIN LATERAL (
			  SELECT COUNT(DISTINCT ca.channel_id)::int AS active_channel_count,
			         COUNT(DISTINCT a.id)::int AS active_account_count,
			         COALESCE(jsonb_agg(DISTINCT p.name)::text, '[]') AS providers_json,
			         COALESCE(jsonb_agg(DISTINCT ca.endpoint)::text, '[]') AS endpoints_json
			  FROM channel_abilities ca
			  JOIN channels c ON c.id = ca.channel_id AND c.status = 'active'
			  JOIN providers p ON p.id = c.provider_id AND p.status = 'active'
			  LEFT JOIN accounts a ON a.channel_id = c.id AND a.status = 'active'
			  WHERE ca.model_name = mc.model_name
			    AND ca.status = 'active'
			) ops ON true
			LEFT JOIN LATERAL (
			  SELECT jsonb_build_object(
			           'status', ctr.status,
			           'latency_ms', ctr.latency_ms,
			           'upstream_status', ctr.upstream_status,
			           'error_message', ctr.error_message,
			           'metadata', ctr.metadata_json,
			           'tested_at', ctr.tested_at
			         )::text AS health_json
			  FROM channel_test_results ctr
			  JOIN channel_abilities ca ON ca.channel_id = ctr.channel_id AND ca.model_name = mc.model_name
			  WHERE ctr.channel_id IS NOT NULL
			  ORDER BY ctr.tested_at DESC
			  LIMIT 1
			) health ON true
			WHERE mc.status = 'active'
		`
	if !publicOnly {
		query = strings.Replace(query, "WHERE mc.status = 'active'", "WHERE true", 1)
	}
	query += " ORDER BY mc.model_name"

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var modelName, displayName, providerHint, capabilities, inputPrice, outputPrice, requestUSD, minCharge, billingMode, billingExpr, cacheRead, cacheWrite, imagePrice, audioPrice, status, metadata, aliases, providers, endpoints, health string
		var publicVisible bool
		var activeChannels, activeAccounts int
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&modelName, &displayName, &providerHint, &capabilities, &inputPrice, &outputPrice, &requestUSD, &minCharge, &billingMode, &billingExpr, &cacheRead, &cacheWrite, &imagePrice, &audioPrice, &publicVisible, &status, &metadata, &createdAt, &updatedAt, &aliases, &activeChannels, &activeAccounts, &providers, &endpoints, &health); err != nil {
			return nil, err
		}
		if publicOnly && !publicVisible {
			continue
		}
		pricing := map[string]any{
			"input_usd_per_1k":       inputPrice,
			"output_usd_per_1k":      outputPrice,
			"request_usd":            requestUSD,
			"min_charge_usd":         minCharge,
			"billing_mode":           billingMode,
			"billing_expr":           billingExpr,
			"cache_read_usd_per_1k":  cacheRead,
			"cache_write_usd_per_1k": cacheWrite,
			"image_usd_per_unit":     imagePrice,
			"audio_usd_per_second":   audioPrice,
		}
		var effectivePricing any
		var billingMultiplier any
		if strings.TrimSpace(userID) != "" {
			policy, err := s.resolveEffectivePolicy(ctx, userID, modelName, "")
			if err != nil {
				return nil, err
			}
			billingMultiplier = formatMoney(policy.BillingMultiplier)
			effectivePricing = effectivePricingMap(pricing, policy.BillingMultiplier)
		}
		item := map[string]any{
			"model_name":            modelName,
			"display_name":          displayName,
			"provider_hint":         providerHint,
			"aliases":               jsonArrayRaw(aliases),
			"endpoint_capabilities": jsonArrayRaw(capabilities),
			"pricing":               pricing,
			"effective_pricing":     effectivePricing,
			"billing_multiplier":    billingMultiplier,
			"active_channel_count":  activeChannels,
			"active_account_count":  activeAccounts,
			"providers":             jsonArrayRaw(providers),
			"available_endpoints":   jsonArrayRaw(endpoints),
			"health":                jsonRaw(health),
			"public_visible":        publicVisible,
			"status":                status,
			"metadata":              jsonRaw(metadata),
			"created_at":            createdAt.UTC().Format(time.RFC3339),
			"updated_at":            updatedAt.UTC().Format(time.RFC3339),
		}
		appendModelDisplayMetadata(item, metadata)
		items = append(items, item)
	}
	return items, rows.Err()
}

func effectivePricingMap(pricing map[string]any, multiplier float64) map[string]any {
	if multiplier < 0 {
		multiplier = 0
	}
	result := map[string]any{}
	for key, value := range pricing {
		text, ok := value.(string)
		if !ok || key == "billing_mode" || key == "billing_expr" {
			result[key] = value
			continue
		}
		result[key] = formatMoney(parsePositiveFloat(text, 0) * multiplier)
	}
	return result
}

func amountString(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", badRequest("Amount is required.")
	}
	if strings.HasPrefix(value, "-") {
		return "", badRequest("Amount cannot be negative.")
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		return "", badRequest("Amount must be a positive decimal string.")
	}
	return value, nil
}
