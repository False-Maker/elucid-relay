package httpserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

const notificationMaxAttempts = 5

type notificationEventInput struct {
	EventType  string
	Severity   string
	Title      string
	Message    string
	TargetType string
	TargetID   string
	Payload    map[string]any
}

type notificationEvent struct {
	ID         string
	EventType  string
	Severity   string
	Title      string
	Message    string
	TargetType string
	TargetID   string
	Payload    string
	Status     string
	Attempts   int
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastError  string
}

type notificationChannel struct {
	ID                      string
	Name                    string
	ChannelType             string
	TargetURLCiphertext     []byte
	TargetURLNonce          []byte
	SigningSecretCiphertext []byte
	SigningSecretNonce      []byte
	SigningSecret           string
	MinSeverity             string
	EventTypes              string
	Status                  string
	Metadata                string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func (s *Server) startNotificationDispatcher(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.dispatchPendingNotifications(ctx, 10); err != nil {
					slog.WarnContext(ctx, "notification dispatch failed", "error", err)
				}
			}
		}
	}()
}

func (s *Server) adminNotificationChannels(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, name, channel_type, target_url_ciphertext, target_url_nonce,
		       signing_secret_ciphertext, signing_secret_nonce,
		       min_severity, event_types_json::text, status, metadata_json::text, created_at, updated_at
		FROM notification_channels
		ORDER BY created_at DESC
		LIMIT $1
	`, limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		channel, err := scanNotificationChannel(rows)
		if err != nil {
			return err
		}
		items = append(items, notificationChannelResponse(channel))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminCreateNotificationChannel(w http.ResponseWriter, r *http.Request, auth authContext) error {
	channel, err := s.upsertNotificationChannel(r.Context(), "", r)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "notification_channel.create", "notification_channel", channel.ID, r, nil)
	writeJSON(w, http.StatusCreated, notificationChannelResponse(channel), nil)
	return nil
}

func (s *Server) adminPatchNotificationChannel(w http.ResponseWriter, r *http.Request, auth authContext) error {
	channelID := r.PathValue("channelId")
	channel, err := s.upsertNotificationChannel(r.Context(), channelID, r)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "notification_channel.update", "notification_channel", channel.ID, r, nil)
	writeJSON(w, http.StatusOK, notificationChannelResponse(channel), nil)
	return nil
}

func (s *Server) adminNotificationEvents(w http.ResponseWriter, r *http.Request, auth authContext) error {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, event_type, severity, title, message, target_type, target_id,
		       payload_json::text, status, attempts, created_at, updated_at, last_error
		FROM notification_events
		WHERE ($1 = '' OR status = $1)
		  AND ($2 = '' OR severity = $2)
		ORDER BY created_at DESC
		LIMIT $3
	`, strings.TrimSpace(r.URL.Query().Get("status")), strings.TrimSpace(r.URL.Query().Get("severity")), limitFromRequest(r, 100, 500))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		event, err := scanNotificationEvent(rows)
		if err != nil {
			return err
		}
		items = append(items, notificationEventResponse(event))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *Server) adminRetryNotificationEvent(w http.ResponseWriter, r *http.Request, auth authContext) error {
	eventID := strings.TrimSpace(r.PathValue("eventId"))
	event, err := s.retryNotificationEvent(r.Context(), eventID)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "notification_event.retry", "notification_event", event.ID, r, nil)
	writeJSON(w, http.StatusOK, notificationEventResponse(event), nil)
	return nil
}

func (s *Server) retryNotificationEvent(ctx context.Context, eventID string) (notificationEvent, error) {
	var event notificationEvent
	err := s.db.QueryRowContext(ctx, `
		UPDATE notification_events
		SET status = 'pending',
		    attempts = 0,
		    next_attempt_at = now(),
		    last_error = '',
		    updated_at = now()
		WHERE id = $1
		  AND status = ANY($2)
		RETURNING id::text, event_type, severity, title, message, target_type, target_id,
		          payload_json::text, status, attempts, created_at, updated_at, last_error
	`, eventID, notificationRetryableStatuses()).Scan(
		&event.ID,
		&event.EventType,
		&event.Severity,
		&event.Title,
		&event.Message,
		&event.TargetType,
		&event.TargetID,
		&event.Payload,
		&event.Status,
		&event.Attempts,
		&event.CreatedAt,
		&event.UpdatedAt,
		&event.LastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return notificationEvent{}, conflict("Only pending, failed, or suppressed notification events can be retried.")
	}
	return event, err
}

func notificationRetryableStatuses() []string {
	return []string{"pending", "failed", "suppressed"}
}

func (s *Server) emitNotification(ctx context.Context, input notificationEventInput) error {
	eventType := strings.TrimSpace(input.EventType)
	if eventType == "" {
		return nil
	}
	severity := strings.TrimSpace(input.Severity)
	if severity == "" {
		severity = "warning"
	}
	if severity != "info" && severity != "warning" && severity != "critical" {
		severity = "warning"
	}
	title := truncateForStorage(input.Title, 200)
	if title == "" {
		title = eventType
	}
	payload, err := encodeJSON(input.Payload)
	if err != nil {
		payload = "{}"
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO notification_events (event_type, severity, title, message, target_type, target_id, payload_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
	`, eventType, severity, title, truncateForStorage(input.Message, 1000), truncateForStorage(input.TargetType, 80), truncateForStorage(input.TargetID, 120), payload)
	return err
}

func (s *Server) dispatchPendingNotifications(ctx context.Context, limit int) error {
	for i := 0; i < limit; i++ {
		event, ok, err := s.claimNotificationEvent(ctx)
		if err != nil || !ok {
			return err
		}
		if err := s.dispatchNotificationEvent(ctx, event); err != nil {
			slog.WarnContext(ctx, "notification event dispatch failed", "event_id", event.ID, "error", err)
		}
	}
	return nil
}

func (s *Server) claimNotificationEvent(ctx context.Context) (notificationEvent, bool, error) {
	var event notificationEvent
	err := s.db.QueryRowContext(ctx, `
		UPDATE notification_events
		SET attempts = attempts + 1,
		    next_attempt_at = now() + interval '1 minute'
		WHERE id = (
		  SELECT id
		  FROM notification_events
		  WHERE status = 'pending'
		    AND next_attempt_at <= now()
		  ORDER BY created_at ASC
		  FOR UPDATE SKIP LOCKED
		  LIMIT 1
		)
		RETURNING id::text, event_type, severity, title, message, target_type, target_id,
		          payload_json::text, status, attempts, created_at, updated_at, last_error
	`).Scan(
		&event.ID,
		&event.EventType,
		&event.Severity,
		&event.Title,
		&event.Message,
		&event.TargetType,
		&event.TargetID,
		&event.Payload,
		&event.Status,
		&event.Attempts,
		&event.CreatedAt,
		&event.UpdatedAt,
		&event.LastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return notificationEvent{}, false, nil
	}
	return event, err == nil, err
}

func (s *Server) dispatchNotificationEvent(ctx context.Context, event notificationEvent) error {
	channels, err := s.notificationChannelsForEvent(ctx, event)
	if err != nil {
		return err
	}
	if len(channels) == 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE notification_events
			SET status = 'suppressed',
			    last_error = 'no active notification channel matched',
			    updated_at = now()
			WHERE id = $1
		`, event.ID)
		return err
	}

	successes := 0
	failures := []string{}
	for _, channel := range channels {
		if err := s.sendNotificationWebhook(ctx, channel, event); err != nil {
			failures = append(failures, channel.Name+": "+err.Error())
			continue
		}
		successes++
	}
	if successes > 0 {
		_, err := s.db.ExecContext(ctx, `
			UPDATE notification_events
			SET status = 'sent',
			    sent_at = now(),
			    last_error = $2,
			    updated_at = now()
			WHERE id = $1
		`, event.ID, strings.Join(failures, "; "))
		return err
	}
	nextStatus := "pending"
	if event.Attempts >= notificationMaxAttempts {
		nextStatus = "failed"
	}
	delay := time.Duration(event.Attempts*event.Attempts) * time.Minute
	if delay > time.Hour {
		delay = time.Hour
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE notification_events
		SET status = $2,
		    next_attempt_at = $3,
		    last_error = $4,
		    updated_at = now()
		WHERE id = $1
	`, event.ID, nextStatus, time.Now().UTC().Add(delay), truncateForStorage(strings.Join(failures, "; "), 1000))
	return err
}

func (s *Server) notificationChannelsForEvent(ctx context.Context, event notificationEvent) ([]notificationChannel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, name, channel_type, target_url_ciphertext, target_url_nonce,
		       signing_secret_ciphertext, signing_secret_nonce,
		       min_severity, event_types_json::text, status, metadata_json::text, created_at, updated_at
		FROM notification_channels
		WHERE status = 'active'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := []notificationChannel{}
	for rows.Next() {
		channel, err := scanNotificationChannel(rows)
		if err != nil {
			return nil, err
		}
		if severityRank(event.Severity) < severityRank(channel.MinSeverity) {
			continue
		}
		if !notificationChannelAllowsEvent(channel.EventTypes, event.EventType) {
			continue
		}
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}

func (s *Server) sendNotificationWebhook(ctx context.Context, channel notificationChannel, event notificationEvent) error {
	targetURL, err := security.DecryptSecret(s.cfg.VaultKey, channel.TargetURLCiphertext, channel.TargetURLNonce)
	if err != nil {
		return err
	}
	payload := notificationEventResponse(event)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "elucid-relay-notifier/1.0")
	req.Header.Set("X-Elucid-Event-Id", event.ID)
	if len(channel.SigningSecretCiphertext) == 0 || len(channel.SigningSecretNonce) == 0 {
		return errors.New("webhook signing secret is missing; rotate channel secret")
	}
	signingSecret, err := security.DecryptSecret(s.cfg.VaultKey, channel.SigningSecretCiphertext, channel.SigningSecretNonce)
	if err != nil {
		return err
	}
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	req.Header.Set("X-Elucid-Timestamp", timestamp)
	req.Header.Set("X-Elucid-Signature", "v1="+webhookSignature(signingSecret, timestamp, body))
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New(resp.Status)
	}
	return nil
}

func (s *Server) upsertNotificationChannel(ctx context.Context, channelID string, r *http.Request) (notificationChannel, error) {
	var req struct {
		Name                *string        `json:"name"`
		TargetURL           *string        `json:"target_url"`
		MinSeverity         *string        `json:"min_severity"`
		EventTypes          []string       `json:"event_types"`
		Status              *string        `json:"status"`
		RotateSigningSecret *bool          `json:"rotate_signing_secret"`
		Metadata            map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return notificationChannel{}, err
	}

	name := "Webhook"
	minSeverity := "warning"
	status := "active"
	eventTypesJSON := "[]"
	metadataJSON := "{}"
	var ciphertext, nonce, signingCiphertext, signingNonce []byte
	if channelID != "" {
		var eventTypes string
		err := s.db.QueryRowContext(ctx, `
			SELECT name, target_url_ciphertext, target_url_nonce, signing_secret_ciphertext, signing_secret_nonce,
			       min_severity, event_types_json::text, status, metadata_json::text
			FROM notification_channels
			WHERE id = $1
		`, channelID).Scan(&name, &ciphertext, &nonce, &signingCiphertext, &signingNonce, &minSeverity, &eventTypes, &status, &metadataJSON)
		if errors.Is(err, sql.ErrNoRows) {
			return notificationChannel{}, notFound("Notification channel was not found.")
		}
		if err != nil {
			return notificationChannel{}, err
		}
		eventTypesJSON = eventTypes
	}
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			return notificationChannel{}, badRequest("Notification channel name is required.")
		}
	}
	if req.TargetURL != nil {
		targetURL := strings.TrimSpace(*req.TargetURL)
		if !strings.HasPrefix(targetURL, "https://") && !strings.HasPrefix(targetURL, "http://") {
			return notificationChannel{}, badRequest("target_url must be http or https.")
		}
		var err error
		ciphertext, nonce, err = security.EncryptSecret(s.cfg.VaultKey, targetURL)
		if err != nil {
			return notificationChannel{}, err
		}
	}
	if len(ciphertext) == 0 || len(nonce) == 0 {
		return notificationChannel{}, badRequest("target_url is required.")
	}
	rotateSigningSecret := channelID == ""
	if req.RotateSigningSecret != nil && *req.RotateSigningSecret {
		rotateSigningSecret = true
	}
	plainSigningSecret := ""
	if rotateSigningSecret || len(signingCiphertext) == 0 || len(signingNonce) == 0 {
		var err error
		plainSigningSecret, err = security.NewOpaqueToken("whsec_", 32)
		if err != nil {
			return notificationChannel{}, err
		}
		signingCiphertext, signingNonce, err = security.EncryptSecret(s.cfg.VaultKey, plainSigningSecret)
		if err != nil {
			return notificationChannel{}, err
		}
	}
	if req.MinSeverity != nil {
		minSeverity = strings.TrimSpace(*req.MinSeverity)
		if minSeverity != "info" && minSeverity != "warning" && minSeverity != "critical" {
			return notificationChannel{}, badRequest("min_severity must be info, warning, or critical.")
		}
	}
	if req.EventTypes != nil {
		encoded, err := encodeJSON(normalizedEventTypes(req.EventTypes))
		if err != nil {
			return notificationChannel{}, err
		}
		eventTypesJSON = encoded
	}
	if req.Status != nil {
		status = strings.TrimSpace(*req.Status)
		if status != "active" && status != "disabled" {
			return notificationChannel{}, badRequest("Invalid notification channel status.")
		}
	}
	if req.Metadata != nil {
		encoded, err := encodeJSON(defaultMap(req.Metadata))
		if err != nil {
			return notificationChannel{}, err
		}
		metadataJSON = encoded
	}

	var channel notificationChannel
	if channelID == "" {
		err := s.db.QueryRowContext(ctx, `
			INSERT INTO notification_channels (
			  name, target_url_ciphertext, target_url_nonce, signing_secret_ciphertext, signing_secret_nonce,
			  min_severity, event_types_json, status, metadata_json
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9::jsonb)
			RETURNING id::text, name, channel_type, target_url_ciphertext, target_url_nonce,
			          signing_secret_ciphertext, signing_secret_nonce,
			          min_severity, event_types_json::text, status, metadata_json::text, created_at, updated_at
		`, name, ciphertext, nonce, signingCiphertext, signingNonce, minSeverity, eventTypesJSON, status, metadataJSON).Scan(
			&channel.ID, &channel.Name, &channel.ChannelType, &channel.TargetURLCiphertext, &channel.TargetURLNonce,
			&channel.SigningSecretCiphertext, &channel.SigningSecretNonce,
			&channel.MinSeverity, &channel.EventTypes, &channel.Status, &channel.Metadata, &channel.CreatedAt, &channel.UpdatedAt,
		)
		channel.SigningSecret = plainSigningSecret
		return channel, err
	}
	err := s.db.QueryRowContext(ctx, `
		UPDATE notification_channels
		SET name = $2,
		    target_url_ciphertext = $3,
		    target_url_nonce = $4,
		    signing_secret_ciphertext = $5,
		    signing_secret_nonce = $6,
		    min_severity = $7,
		    event_types_json = $8::jsonb,
		    status = $9,
		    metadata_json = $10::jsonb
		WHERE id = $1
		RETURNING id::text, name, channel_type, target_url_ciphertext, target_url_nonce,
		          signing_secret_ciphertext, signing_secret_nonce,
		          min_severity, event_types_json::text, status, metadata_json::text, created_at, updated_at
	`, channelID, name, ciphertext, nonce, signingCiphertext, signingNonce, minSeverity, eventTypesJSON, status, metadataJSON).Scan(
		&channel.ID, &channel.Name, &channel.ChannelType, &channel.TargetURLCiphertext, &channel.TargetURLNonce,
		&channel.SigningSecretCiphertext, &channel.SigningSecretNonce,
		&channel.MinSeverity, &channel.EventTypes, &channel.Status, &channel.Metadata, &channel.CreatedAt, &channel.UpdatedAt,
	)
	channel.SigningSecret = plainSigningSecret
	return channel, err
}

func scanNotificationChannel(scanner interface{ Scan(...any) error }) (notificationChannel, error) {
	var channel notificationChannel
	err := scanner.Scan(
		&channel.ID, &channel.Name, &channel.ChannelType, &channel.TargetURLCiphertext, &channel.TargetURLNonce,
		&channel.SigningSecretCiphertext, &channel.SigningSecretNonce,
		&channel.MinSeverity, &channel.EventTypes, &channel.Status, &channel.Metadata, &channel.CreatedAt, &channel.UpdatedAt,
	)
	return channel, err
}

func scanNotificationEvent(scanner interface{ Scan(...any) error }) (notificationEvent, error) {
	var event notificationEvent
	err := scanner.Scan(
		&event.ID, &event.EventType, &event.Severity, &event.Title, &event.Message, &event.TargetType, &event.TargetID,
		&event.Payload, &event.Status, &event.Attempts, &event.CreatedAt, &event.UpdatedAt, &event.LastError,
	)
	return event, err
}

func notificationChannelResponse(channel notificationChannel) map[string]any {
	response := map[string]any{
		"id":                     channel.ID,
		"name":                   channel.Name,
		"channel_type":           channel.ChannelType,
		"target_url_present":     len(channel.TargetURLCiphertext) > 0,
		"signing_secret_present": len(channel.SigningSecretCiphertext) > 0,
		"min_severity":           channel.MinSeverity,
		"event_types":            jsonArrayRaw(channel.EventTypes),
		"status":                 channel.Status,
		"metadata":               jsonRaw(channel.Metadata),
		"created_at":             channel.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":             channel.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if channel.SigningSecret != "" {
		response["signing_secret"] = channel.SigningSecret
	}
	return response
}

func notificationEventResponse(event notificationEvent) map[string]any {
	return map[string]any{
		"id":          event.ID,
		"event_type":  event.EventType,
		"severity":    event.Severity,
		"title":       event.Title,
		"message":     event.Message,
		"target_type": event.TargetType,
		"target_id":   event.TargetID,
		"payload":     jsonRaw(event.Payload),
		"status":      event.Status,
		"attempts":    event.Attempts,
		"last_error":  event.LastError,
		"created_at":  event.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":  event.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func notificationChannelAllowsEvent(raw string, eventType string) bool {
	var allowed []string
	if err := json.Unmarshal([]byte(raw), &allowed); err != nil || len(allowed) == 0 {
		return true
	}
	for _, item := range allowed {
		if strings.EqualFold(strings.TrimSpace(item), eventType) {
			return true
		}
	}
	return false
}

func normalizedEventTypes(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func severityRank(severity string) int {
	switch severity {
	case "critical":
		return 3
	case "warning":
		return 2
	default:
		return 1
	}
}

func webhookSignature(secret string, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
