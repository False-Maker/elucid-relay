package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/config"
	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

type authContext struct {
	SessionID       string
	UserID          string
	UserType        string
	Email           string
	Name            string
	Status          string
	Audience        string
	CSRFToken       string
	EmailVerifiedAt sql.NullTime
	TokenFrom       string
}

type sessionHandler func(http.ResponseWriter, *http.Request, authContext) error

type adminPermission string

const (
	adminPermSelf          adminPermission = "self"
	adminPermOverview      adminPermission = "overview"
	adminPermUsersRead     adminPermission = "users_read"
	adminPermUsersReset    adminPermission = "users_reset"
	adminPermModels        adminPermission = "models"
	adminPermPool          adminPermission = "pool"
	adminPermUpstream      adminPermission = "upstream"
	adminPermProxies       adminPermission = "proxies"
	adminPermOAuth         adminPermission = "oauth"
	adminPermUsageRead     adminPermission = "usage_read"
	adminPermAudit         adminPermission = "audit"
	adminPermPlatformOwner adminPermission = "platform_owner"
)

var operatorAdminPermissions = map[adminPermission]bool{
	adminPermSelf:       true,
	adminPermOverview:   true,
	adminPermUsersRead:  true,
	adminPermUsersReset: true,
	adminPermModels:     true,
	adminPermPool:       true,
	adminPermUpstream:   true,
	adminPermProxies:    true,
	adminPermOAuth:      true,
	adminPermUsageRead:  true,
	adminPermAudit:      true,
}

func SeedOwner(ctx context.Context, cfg config.Config, database *sql.DB) error {
	if cfg.SeedOwnerEmail == "" || cfg.SeedOwnerPassword == "" {
		return seedPersonalUser(ctx, cfg, database)
	}

	var exists bool
	err := database.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE email = $1)", cfg.SeedOwnerEmail).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return seedPersonalUser(ctx, cfg, database)
	}

	passwordHash, err := security.HashPassword(cfg.SeedOwnerPassword)
	if err != nil {
		return err
	}

	_, err = database.ExecContext(ctx, `
		INSERT INTO users (user_type, email, password_hash, display_name, status)
		VALUES ('platform_owner', $1, $2, $3, 'active')
	`, strings.ToLower(strings.TrimSpace(cfg.SeedOwnerEmail)), passwordHash, cfg.SeedOwnerDisplayName)
	if err != nil {
		return err
	}
	return seedPersonalUser(ctx, cfg, database)
}

func seedPersonalUser(ctx context.Context, cfg config.Config, database *sql.DB) error {
	if cfg.SeedPersonalEmail == "" || cfg.SeedPersonalPassword == "" {
		return nil
	}

	email := strings.ToLower(strings.TrimSpace(cfg.SeedPersonalEmail))
	var exists bool
	if err := database.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE email = $1)", email).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}

	passwordHash, err := security.HashPassword(cfg.SeedPersonalPassword)
	if err != nil {
		return err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var userID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO users (user_type, email, password_hash, display_name, status)
		VALUES ('personal_user', $1, $2, $3, 'active')
		RETURNING id::text
	`, email, passwordHash, cfg.SeedPersonalName).Scan(&userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO wallet_accounts (user_id) VALUES ($1)", userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) withPortalSession(next sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, err := s.requireSession(r, "portal")
		if err != nil {
			if adminAuth, adminErr := s.requireSession(r, "admin"); adminErr == nil && (adminAuth.UserType == "operator" || adminAuth.UserType == "platform_owner") {
				auth = adminAuth
				err = nil
			}
		}
		if err != nil {
			writeError(w, r, err)
			return
		}
		if err := s.requireCSRF(r, auth); err != nil {
			writeError(w, r, err)
			return
		}
		if err := next(w, r, auth); err != nil {
			writeError(w, r, err)
		}
	}
}

func (s *Server) withAdminSession(next sessionHandler) http.HandlerFunc {
	return s.withAdminSessionPermission(adminPermSelf, next)
}

func (s *Server) withAdminSessionPermission(permission adminPermission, next sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, err := s.requireSession(r, "admin")
		if err != nil {
			writeError(w, r, err)
			return
		}
		if auth.UserType != "operator" && auth.UserType != "platform_owner" {
			writeError(w, r, forbidden("Admin permission is required."))
			return
		}
		if !hasAdminPermission(auth.UserType, permission) {
			writeError(w, r, forbidden("Admin permission is required."))
			return
		}
		if err := s.requireCSRF(r, auth); err != nil {
			writeError(w, r, err)
			return
		}
		if err := next(w, r, auth); err != nil {
			writeError(w, r, err)
		}
	}
}

func hasAdminPermission(userType string, permission adminPermission) bool {
	if userType == "platform_owner" {
		return true
	}
	if userType == "operator" {
		return operatorAdminPermissions[permission]
	}
	return false
}

func (s *Server) requireSession(r *http.Request, audience string) (authContext, error) {
	token := security.NormalizeBearer(r.Header.Get("Authorization"))
	source := "bearer"
	if token == "" {
		cookieName := "relay_" + audience + "_session"
		cookie, err := r.Cookie(cookieName)
		if err == nil {
			token = cookie.Value
			source = "cookie"
		}
	}
	if token == "" {
		return authContext{}, unauthorized("Authentication is required.")
	}

	tokenHash := security.HashSecret(token)
	var auth authContext
	err := s.db.QueryRowContext(r.Context(), `
		SELECT s.session_id::text, u.id::text, u.user_type, u.email::text, u.display_name, u.status, s.audience, s.csrf_token, u.email_verified_at
		FROM user_sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.audience = $2
		  AND s.revoked_at IS NULL
		  AND s.expires_at > now()
	`, tokenHash, audience).Scan(
		&auth.SessionID,
		&auth.UserID,
		&auth.UserType,
		&auth.Email,
		&auth.Name,
		&auth.Status,
		&auth.Audience,
		&auth.CSRFToken,
		&auth.EmailVerifiedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return authContext{}, unauthorized("Invalid or expired session.")
	}
	if err != nil {
		return authContext{}, err
	}
	if auth.Status != "active" {
		return authContext{}, forbidden("User is disabled.")
	}

	auth.TokenFrom = source
	return auth, nil
}

func (s *Server) requireCSRF(r *http.Request, auth authContext) error {
	if auth.TokenFrom != "cookie" {
		return nil
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return nil
	}
	if r.Header.Get("X-CSRF-Token") != auth.CSRFToken {
		return forbidden("CSRF token is required.")
	}
	return nil
}

func (s *Server) createSession(ctx context.Context, userID string, audience string) (string, string, string, time.Time, error) {
	token, err := security.NewOpaqueToken("sess_", 32)
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	csrfToken, err := security.NewOpaqueToken("csrf_", 24)
	if err != nil {
		return "", "", "", time.Time{}, err
	}

	expiresAt := time.Now().UTC().Add(time.Duration(s.cfg.SessionTTLHours) * time.Hour)
	var sessionID string
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO user_sessions (user_id, audience, token_hash, csrf_token, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING session_id::text
	`, userID, audience, security.HashSecret(token), csrfToken, expiresAt).Scan(&sessionID)
	if err != nil {
		return "", "", "", time.Time{}, err
	}

	return sessionID, token, csrfToken, expiresAt, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, audience string, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     "relay_" + audience + "_session",
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, audience string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "relay_" + audience + "_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func userResponse(auth authContext) map[string]any {
	return map[string]any{
		"id":                auth.UserID,
		"user_type":         auth.UserType,
		"email":             auth.Email,
		"display_name":      auth.Name,
		"status":            auth.Status,
		"email_verified_at": nullableSQLTime(auth.EmailVerifiedAt),
	}
}
