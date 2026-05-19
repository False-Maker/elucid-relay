package httpserver

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

const (
	securityTokenTTL       = 30 * time.Minute
	emailVerificationTTL   = 24 * time.Hour
	registrationCodeTTL    = 10 * time.Minute
	passwordResetTokenType = "password_reset"
	emailVerifyTokenType   = "email_verification"
)

type securityTokenStore interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Server) portalRequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	email := trimEmail(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		writeError(w, r, badRequest("A valid email is required."))
		return
	}

	response := map[string]any{"requested": true}
	var userID string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT id::text
		FROM users
		WHERE email = $1 AND user_type = 'personal_user' AND status = 'active'
	`, email).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, response, nil)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}

	token, expiresAt, err := createUserSecurityToken(r.Context(), s.db, userID, passwordResetTokenType, securityTokenTTL)
	if err != nil {
		writeError(w, r, err)
		return
	}
	audit(r.Context(), s.db, userID, "personal_user", "user.password_reset.request", "user", userID, r, nil)
	if err := s.deliverPasswordResetEmail(r.Context(), email, token, expiresAt); err != nil {
		s.handleSecurityEmailFailure(r.Context(), userID, "password_reset", err)
	}
	if s.exposesSecurityTokens() {
		response["expires_at"] = expiresAt.UTC().Format(time.RFC3339)
		response["reset_token"] = token
	}
	writeJSON(w, http.StatusOK, response, nil)
}

func (s *Server) portalRegistrationEmailVerificationStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"required": s.registrationEmailVerificationEnabled(r.Context()),
	}, nil)
}

func (s *Server) portalRequestRegistrationEmailCode(w http.ResponseWriter, r *http.Request) {
	if !s.registrationEmailVerificationEnabled(r.Context()) {
		writeJSON(w, http.StatusOK, map[string]any{"required": false, "sent": false}, nil)
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	email := trimEmail(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		writeError(w, r, badRequest("A valid email is required."))
		return
	}

	var exists bool
	if err := s.db.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM users WHERE email = $1)", email).Scan(&exists); err != nil {
		writeError(w, r, err)
		return
	}
	if exists {
		writeError(w, r, conflict("Email is already registered."))
		return
	}

	code, err := registrationEmailCode()
	if err != nil {
		writeError(w, r, err)
		return
	}
	expiresAt := time.Now().UTC().Add(registrationCodeTTL)
	if _, err := s.db.ExecContext(r.Context(), `
		UPDATE registration_email_codes
		SET consumed_at = now()
		WHERE email = $1 AND consumed_at IS NULL
	`, email); err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `
		INSERT INTO registration_email_codes (email, code_hash, expires_at)
		VALUES ($1, $2, $3)
	`, email, security.HashSecret(code), expiresAt); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.deliverRegistrationVerificationEmail(r.Context(), email, code, expiresAt); err != nil {
		s.handleSecurityEmailFailure(r.Context(), "", "registration_email_verification", err)
		writeError(w, r, upstreamUnavailable("email_delivery_failed", "Verification email could not be sent."))
		return
	}

	response := map[string]any{
		"required":   true,
		"sent":       true,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	}
	if s.exposesSecurityTokens() {
		response["verification_code"] = code
	}
	writeJSON(w, http.StatusOK, response, nil)
}

func (s *Server) consumeRegistrationEmailCode(ctx context.Context, email string, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return badRequest("Email verification code is required.")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE registration_email_codes
		SET consumed_at = now()
		WHERE id = (
		  SELECT id
		  FROM registration_email_codes
		  WHERE email = $1
		    AND code_hash = $2
		    AND consumed_at IS NULL
		    AND expires_at > now()
		  ORDER BY created_at DESC
		  LIMIT 1
		)
	`, email, security.HashSecret(code))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return unauthorized("Invalid or expired email verification code.")
	}
	return nil
}

func registrationEmailCode() (string, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", fmt.Errorf("generate registration email code: %w", err)
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

func (s *Server) portalConfirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeError(w, r, badRequest("Reset token is required."))
		return
	}
	if len(req.Password) < 8 {
		writeError(w, r, badRequest("Password must be at least 8 characters."))
		return
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

	var userID string
	err = tx.QueryRowContext(r.Context(), `
		UPDATE user_security_tokens
		SET consumed_at = now()
		WHERE token_hash = $1
		  AND token_type = $2
		  AND consumed_at IS NULL
		  AND expires_at > now()
		RETURNING user_id::text
	`, security.HashSecret(token), passwordResetTokenType).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, unauthorized("Invalid or expired reset token."))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE users
		SET password_hash = $2,
		    password_changed_at = now()
		WHERE id = $1
	`, userID, passwordHash); err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), "UPDATE user_sessions SET revoked_at = now() WHERE user_id = $1", userID); err != nil {
		writeError(w, r, err)
		return
	}
	audit(r.Context(), tx, userID, "personal_user", "user.password_reset.confirm", "user", userID, r, nil)
	if err := tx.Commit(); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reset": true}, nil)
}

func (s *Server) portalRequestEmailVerification(w http.ResponseWriter, r *http.Request, auth authContext) error {
	if auth.EmailVerifiedAt.Valid {
		writeJSON(w, http.StatusOK, map[string]any{"verified": true}, nil)
		return nil
	}
	token, expiresAt, err := createUserSecurityToken(r.Context(), s.db, auth.UserID, emailVerifyTokenType, emailVerificationTTL)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "personal_user", "user.email_verification.request", "user", auth.UserID, r, nil)
	if err := s.deliverEmailVerificationEmail(r.Context(), auth.Email, token, expiresAt); err != nil {
		s.handleSecurityEmailFailure(r.Context(), auth.UserID, "email_verification", err)
		return upstreamUnavailable("email_delivery_failed", "Verification email could not be sent.")
	}
	response := map[string]any{
		"requested":  true,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	}
	if s.exposesSecurityTokens() {
		response["verification_token"] = token
	}
	writeJSON(w, http.StatusOK, response, nil)
	return nil
}

func (s *Server) portalConfirmEmailVerification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	userID, err := consumeUserSecurityToken(r.Context(), s.db, strings.TrimSpace(req.Token), emailVerifyTokenType)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `
		UPDATE users
		SET email_verified_at = COALESCE(email_verified_at, now())
		WHERE id = $1
	`, userID); err != nil {
		writeError(w, r, err)
		return
	}
	audit(r.Context(), s.db, userID, "personal_user", "user.email_verification.confirm", "user", userID, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"verified": true}, nil)
}

func (s *Server) adminCreateUserPasswordReset(w http.ResponseWriter, r *http.Request, auth authContext) error {
	userID := r.PathValue("userId")
	var exists bool
	if err := s.db.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)", userID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return notFound("User was not found.")
	}
	token, expiresAt, err := createUserSecurityToken(r.Context(), s.db, userID, passwordResetTokenType, securityTokenTTL)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "user.password_reset.issue", "user", userID, r, nil)
	writeJSON(w, http.StatusCreated, map[string]any{
		"user_id":     userID,
		"reset_token": token,
		"expires_at":  expiresAt.UTC().Format(time.RFC3339),
	}, nil)
	return nil
}

func createUserSecurityToken(ctx context.Context, store securityTokenStore, userID string, tokenType string, ttl time.Duration) (string, time.Time, error) {
	prefix := "sec_"
	if tokenType == passwordResetTokenType {
		prefix = "pwd_"
	}
	if tokenType == emailVerifyTokenType {
		prefix = "ev_"
	}
	token, err := security.NewOpaqueToken(prefix, 32)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(ttl)
	if _, err := store.ExecContext(ctx, `
		UPDATE user_security_tokens
		SET consumed_at = now()
		WHERE user_id = $1
		  AND token_type = $2
		  AND consumed_at IS NULL
	`, userID, tokenType); err != nil {
		return "", time.Time{}, err
	}
	if _, err := store.ExecContext(ctx, `
		INSERT INTO user_security_tokens (user_id, token_type, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
	`, userID, tokenType, security.HashSecret(token), expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func consumeUserSecurityToken(ctx context.Context, store securityTokenStore, token string, tokenType string) (string, error) {
	if token == "" {
		return "", badRequest("Token is required.")
	}
	var userID string
	err := store.QueryRowContext(ctx, `
		UPDATE user_security_tokens
		SET consumed_at = now()
		WHERE token_hash = $1
		  AND token_type = $2
		  AND consumed_at IS NULL
		  AND expires_at > now()
		RETURNING user_id::text
	`, security.HashSecret(token), tokenType).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", unauthorized("Invalid or expired token.")
	}
	return userID, err
}

func (s *Server) exposesSecurityTokens() bool {
	return strings.TrimSpace(s.cfg.AppEnv) == "development"
}
