package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func limitFromRequest(r *http.Request, fallback int, max int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}

func trimEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

func nullableSQLTime(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return nullableTime(&value.Time)
}

func nullableTimeString(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func jsonRaw(value string) json.RawMessage {
	if value == "" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(value)
}

func jsonArrayRaw(value string) json.RawMessage {
	if value == "" {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(value)
}

func encodeJSON(value any) (string, error) {
	if value == nil {
		return "null", nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "", badRequest("Invalid JSON field.")
	}
	return string(body), nil
}

func mustEncodeJSON(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(body)
}

func normalizeStringList(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func decimalValue(value *string, field string, fallback string, required bool) (string, bool, error) {
	if value == nil {
		if required {
			return fallback, true, nil
		}
		return fallback, false, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return "", false, badRequest(field + " cannot be empty.")
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || parsed < 0 {
		return "", false, badRequest(field + " must be a non-negative decimal string.")
	}
	return trimmed, true, nil
}

func modelPriceValue(value *string, field string, required bool) (string, bool, error) {
	return decimalValue(value, field, "0", required)
}

func statusValue(value *string, fallback string, required bool, allowed ...string) (string, bool, error) {
	if value == nil {
		if required {
			return fallback, true, nil
		}
		return fallback, false, nil
	}
	trimmed := strings.TrimSpace(*value)
	for _, candidate := range allowed {
		if trimmed == candidate {
			return trimmed, true, nil
		}
	}
	return "", false, badRequest("Invalid status.")
}

func defaultedStatus(value string, fallback string, allowed ...string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback, nil
	}
	for _, candidate := range allowed {
		if trimmed == candidate {
			return trimmed, nil
		}
	}
	return "", badRequest("Invalid status.")
}

func validateURL(value string, field string, allowedSchemes ...string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", badRequest(field + " must be a valid absolute URL.")
	}
	for _, scheme := range allowedSchemes {
		if parsed.Scheme == scheme {
			return trimmed, nil
		}
	}
	return "", badRequest(field + " has an unsupported URL scheme.")
}

func (s *Server) requireIDExists(ctx context.Context, table string, id string, message string) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1)", id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return notFound(message)
	}
	return nil
}

const clientIPKey contextKey = "client_ip"

func (s *Server) clientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIPForRequest(r)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey, ip)))
	})
}

func (s *Server) clientIPForRequest(r *http.Request) string {
	remote := remoteIP(r)
	if !s.trustsProxy(remote) {
		return remote
	}
	if forwarded := firstForwardedIP(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		return forwarded
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(realIP) != nil {
		return realIP
	}
	return remote
}

func clientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(clientIPKey).(string); ok && ip != "" {
		return ip
	}
	return remoteIP(r)
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func firstForwardedIP(value string) string {
	parts := strings.Split(value, ",")
	if len(parts) == 0 {
		return ""
	}
	ip := strings.TrimSpace(parts[0])
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

func (s *Server) trustsProxy(ipText string) bool {
	if len(s.trustedNets) == 0 {
		return false
	}
	ip := net.ParseIP(ipText)
	if ip == nil {
		return false
	}
	for _, network := range s.trustedNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func trustedProxyNetworks(raw string) []*net.IPNet {
	networks := []*net.IPNet{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(entry)
		if err == nil {
			networks = append(networks, network)
		}
	}
	return networks
}

func audit(ctx context.Context, exec sqlExecutor, actorUserID string, actorType string, action string, targetType string, targetID string, r *http.Request, metadata any) {
	metadataJSON, err := encodeJSON(metadata)
	if err != nil {
		metadataJSON = "{}"
	}

	var actor any
	if actorUserID == "" {
		actor = nil
	} else {
		actor = actorUserID
	}

	_, _ = exec.ExecContext(ctx, `
		INSERT INTO audit_logs (actor_user_id, actor_type, action, target_type, target_id, ip_address, user_agent, metadata_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
	`, actor, actorType, action, targetType, targetID, clientIP(r), r.UserAgent(), metadataJSON)
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func truncateForStorage(value string, maxLength int) string {
	value = strings.TrimSpace(value)
	if maxLength <= 0 || len(value) <= maxLength {
		return value
	}
	return value[:maxLength]
}
