package httpserver

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	initialized, err := s.hasPlatformOwner(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"initialized": initialized}, nil)
}

func (s *Server) setupCreateOwner(w http.ResponseWriter, r *http.Request) {
	initialized, err := s.hasPlatformOwner(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if initialized {
		writeError(w, r, conflict("System has already been initialized."))
		return
	}

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

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = email
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

	if _, err := tx.ExecContext(r.Context(), "LOCK TABLE users IN EXCLUSIVE MODE"); err != nil {
		writeError(w, r, err)
		return
	}

	var ownerExists bool
	if err := tx.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM users WHERE user_type = 'platform_owner')").Scan(&ownerExists); err != nil {
		writeError(w, r, err)
		return
	}
	if ownerExists {
		writeError(w, r, conflict("System has already been initialized."))
		return
	}

	var userID string
	err = tx.QueryRowContext(r.Context(), `
		INSERT INTO users (user_type, email, password_hash, display_name, status)
		VALUES ('platform_owner', $1, $2, $3, 'active')
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

	audit(r.Context(), tx, userID, "platform_owner", "system.setup", "user", userID, r, map[string]any{"email": email})
	if err := tx.Commit(); err != nil {
		writeError(w, r, err)
		return
	}

	auth, err := s.loginUser(r.Context(), email, req.Password, "admin")
	if err != nil {
		writeError(w, r, err)
		return
	}

	s.setSessionCookie(w, "admin", auth.Token, auth.ExpiresAt)
	writeJSON(w, http.StatusCreated, auth.Response, nil)
}

func (s *Server) hasPlatformOwner(r *http.Request) (bool, error) {
	var initialized bool
	err := s.db.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM users WHERE user_type = 'platform_owner')").Scan(&initialized)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return initialized, err
}
