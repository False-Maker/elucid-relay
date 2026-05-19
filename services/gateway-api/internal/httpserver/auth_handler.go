package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

func (s *Server) authenticateAPIKey(r *http.Request) (apiKeyAuth, error) {
	token := security.NormalizeBearer(r.Header.Get("Authorization"))
	if token == "" {
		return apiKeyAuth{}, unauthorized("Personal API key is required.")
	}

	var auth apiKeyAuth
	err := s.db.QueryRowContext(r.Context(), `
		SELECT u.id::text, u.status, k.id::text, k.status, k.routing_mode, k.expires_at, k.ip_allowlist_json::text, k.model_scope_json::text,
		       w.id::text, w.balance::text, w.reserved_balance::text
		FROM api_keys k
		JOIN users u ON u.id = k.owner_id
		JOIN wallet_accounts w ON w.user_id = u.id
		WHERE k.key_hash = $1
		  AND k.owner_type = 'personal_user'
	`, security.HashSecret(token)).Scan(
		&auth.UserID,
		&auth.UserStatus,
		&auth.APIKeyID,
		&auth.APIKeyStatus,
		&auth.RoutingMode,
		&auth.ExpiresAt,
		&auth.IPAllowlistJSON,
		&auth.ModelScopeJSON,
		&auth.WalletID,
		&auth.Balance,
		&auth.ReservedBalance,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return apiKeyAuth{}, unauthorized("Invalid personal API key.")
	}
	if err != nil {
		return apiKeyAuth{}, err
	}
	if auth.UserStatus != "active" {
		return apiKeyAuth{}, forbidden("User is disabled.")
	}
	if auth.APIKeyStatus != "active" {
		return apiKeyAuth{}, unauthorized("API key is not active.")
	}
	if auth.ExpiresAt.Valid && auth.ExpiresAt.Time.Before(time.Now()) {
		return apiKeyAuth{}, unauthorized("API key has expired.")
	}
	if err := validateIPAllowlist(auth.IPAllowlistJSON, clientIP(r)); err != nil {
		return apiKeyAuth{}, err
	}
	return auth, nil
}

func validateIPAllowlist(raw string, requestIP string) error {
	var entries []string
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return forbidden("Invalid IP allowlist.")
	}
	if len(entries) == 0 {
		return nil
	}
	ip := net.ParseIP(requestIP)
	if ip == nil {
		return forbidden("Request IP is not allowed.")
	}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			_, network, err := net.ParseCIDR(entry)
			if err == nil && network.Contains(ip) {
				return nil
			}
			continue
		}
		if parsed := net.ParseIP(entry); parsed != nil && parsed.Equal(ip) {
			return nil
		}
	}
	return forbidden("Request IP is not allowed.")
}

func validateModelScope(raw string, requestedModel string, canonicalModel string, aliases ...string) error {
	var scope []string
	if err := json.Unmarshal([]byte(raw), &scope); err != nil {
		return forbidden("Invalid model scope.")
	}
	if len(scope) == 0 {
		return nil
	}
	allowedModels := map[string]bool{
		requestedModel: true,
		canonicalModel: true,
	}
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias != "" {
			allowedModels[alias] = true
		}
	}
	for _, allowed := range scope {
		if allowedModels[allowed] {
			return nil
		}
	}
	return forbidden("Model is outside the API key scope.")
}

func (s *Server) claimIdempotency(ctx context.Context, auth apiKeyAuth, requestID string, fingerprint string) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO northbound_idempotency_keys (request_id, api_key_id, user_id, request_fingerprint)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING
	`, requestID, auth.APIKeyID, auth.UserID, fingerprint)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		return nil
	}

	var existingFingerprint, status string
	if err := s.db.QueryRowContext(ctx, `
		SELECT request_fingerprint, status
		FROM northbound_idempotency_keys
		WHERE request_id = $1 AND api_key_id = $2
	`, requestID, auth.APIKeyID).Scan(&existingFingerprint, &status); err != nil {
		return err
	}
	if existingFingerprint != fingerprint {
		return conflict("Duplicate request_id was used with a different request.")
	}
	return conflict("Duplicate request_id is already " + status + ".")
}

func (s *Server) completeIdempotency(ctx context.Context, auth apiKeyAuth, requestID string, status string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE northbound_idempotency_keys
		SET status = $3, completed_at = now()
		WHERE request_id = $1 AND api_key_id = $2
	`, requestID, auth.APIKeyID, status)
	return err
}
