package httpserver

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/config"
	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

const emailVerificationSettingKey = "auth.email_verification"

type smtpSettings struct {
	Host               string
	Port               int
	Username           string
	Password           string
	PasswordCiphertext string
	PasswordNonce      string
	From               string
	TLSMode            string
}

type emailVerificationSettings struct {
	RegistrationVerificationEnabled bool
	SMTP                            smtpSettings
}

func (s *Server) deliverPasswordResetEmail(ctx context.Context, email string, token string, expiresAt time.Time) error {
	if !s.emailDeliveryEnabled() {
		return nil
	}
	link := s.securityActionLink("reset_token", token)
	body := fmt.Sprintf("Use this link to reset your Elucid Relay password:\n\n%s\n\nThis link expires at %s.\n\nIf you did not request this, ignore this email.\n", link, expiresAt.UTC().Format(time.RFC3339))
	return s.sendEmail(ctx, email, "Reset your Elucid Relay password", body)
}

func (s *Server) deliverEmailVerificationEmail(ctx context.Context, email string, token string, expiresAt time.Time) error {
	if !s.emailDeliveryEnabled() {
		return nil
	}
	link := s.securityActionLink("verification_token", token)
	body := fmt.Sprintf("Use this link to verify your Elucid Relay email address:\n\n%s\n\nThis link expires at %s.\n\nIf you did not request this, ignore this email.\n", link, expiresAt.UTC().Format(time.RFC3339))
	return s.sendEmail(ctx, email, "Verify your Elucid Relay email", body)
}

func (s *Server) deliverRegistrationVerificationEmail(ctx context.Context, email string, code string, expiresAt time.Time) error {
	settings, err := s.emailVerificationSettings(ctx)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("Your Elucid Relay registration verification code is:\n\n%s\n\nThis code expires at %s.\n\nIf you did not request this, ignore this email.\n", code, expiresAt.UTC().Format(time.RFC3339))
	return s.sendEmailWithSMTP(ctx, settings.SMTP, email, "Elucid Relay registration code", body)
}

func (s *Server) emailDeliveryEnabled() bool {
	return strings.TrimSpace(s.cfg.SMTPHost) != "" && strings.TrimSpace(s.cfg.SMTPFrom) != ""
}

func (s *Server) registrationEmailVerificationEnabled(ctx context.Context) bool {
	settings, err := s.emailVerificationSettings(ctx)
	return err == nil && settings.RegistrationVerificationEnabled && settings.SMTP.enabled()
}

func (s *Server) emailVerificationSettings(ctx context.Context) (emailVerificationSettings, error) {
	settings := defaultEmailVerificationSettings(s.cfg)
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT setting_value_json::text FROM system_settings WHERE setting_key = $1", emailVerificationSettingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}

	var stored map[string]any
	if err := decodeJSONString(raw, &stored); err != nil {
		return settings, err
	}
	settings.RegistrationVerificationEnabled = boolValue(stored["registration_verification_enabled"], settings.RegistrationVerificationEnabled)
	if smtpValue, ok := stored["smtp"].(map[string]any); ok {
		settings.SMTP.Host = stringValue(smtpValue["host"], settings.SMTP.Host)
		settings.SMTP.Port = intValue(smtpValue["port"], settings.SMTP.Port)
		settings.SMTP.Username = stringValue(smtpValue["username"], settings.SMTP.Username)
		settings.SMTP.PasswordCiphertext = stringValue(smtpValue["password_ciphertext"], settings.SMTP.PasswordCiphertext)
		settings.SMTP.PasswordNonce = stringValue(smtpValue["password_nonce"], settings.SMTP.PasswordNonce)
		settings.SMTP.From = stringValue(smtpValue["from"], settings.SMTP.From)
		settings.SMTP.TLSMode = stringValue(smtpValue["tls_mode"], settings.SMTP.TLSMode)
	}
	if settings.SMTP.PasswordCiphertext != "" && settings.SMTP.PasswordNonce != "" {
		ciphertext, cipherErr := base64.StdEncoding.DecodeString(settings.SMTP.PasswordCiphertext)
		nonce, nonceErr := base64.StdEncoding.DecodeString(settings.SMTP.PasswordNonce)
		if cipherErr == nil && nonceErr == nil {
			if password, decryptErr := security.DecryptSecret(s.cfg.VaultKey, ciphertext, nonce); decryptErr == nil {
				settings.SMTP.Password = password
			}
		}
	}
	if settings.SMTP.Port <= 0 {
		settings.SMTP.Port = 587
	}
	if strings.TrimSpace(settings.SMTP.TLSMode) == "" {
		settings.SMTP.TLSMode = "starttls"
	}
	return settings, nil
}

func defaultEmailVerificationSettings(cfg config.Config) emailVerificationSettings {
	return emailVerificationSettings{
		RegistrationVerificationEnabled: false,
		SMTP: smtpSettings{
			Host:     strings.TrimSpace(cfg.SMTPHost),
			Port:     cfg.SMTPPort,
			Username: strings.TrimSpace(cfg.SMTPUsername),
			Password: cfg.SMTPPassword,
			From:     strings.TrimSpace(cfg.SMTPFrom),
			TLSMode:  strings.TrimSpace(cfg.SMTPTLSMode),
		},
	}
}

func (settings smtpSettings) enabled() bool {
	return strings.TrimSpace(settings.Host) != "" && strings.TrimSpace(settings.From) != ""
}

func boolValue(value any, fallback bool) bool {
	if parsed, ok := value.(bool); ok {
		return parsed
	}
	return fallback
}

func stringValue(value any, fallback string) string {
	if parsed, ok := value.(string); ok {
		return strings.TrimSpace(parsed)
	}
	return fallback
}

func intValue(value any, fallback int) int {
	switch parsed := value.(type) {
	case float64:
		if parsed > 0 {
			return int(parsed)
		}
	case int:
		if parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func (s *Server) securityActionLink(param string, token string) string {
	base := strings.TrimRight(strings.TrimSpace(s.cfg.PortalBaseURL), "/")
	if base == "" {
		base = "http://localhost:18081"
	}
	values := url.Values{}
	values.Set(param, token)
	return base + "/#" + values.Encode()
}

func (s *Server) sendEmail(ctx context.Context, to string, subject string, textBody string) error {
	settings := smtpSettings{
		Host:     s.cfg.SMTPHost,
		Port:     s.cfg.SMTPPort,
		Username: s.cfg.SMTPUsername,
		Password: s.cfg.SMTPPassword,
		From:     s.cfg.SMTPFrom,
		TLSMode:  s.cfg.SMTPTLSMode,
	}
	return s.sendEmailWithSMTP(ctx, settings, to, subject, textBody)
}

func (s *Server) sendEmailWithSMTP(ctx context.Context, settings smtpSettings, to string, subject string, textBody string) error {
	from, err := mail.ParseAddress(settings.From)
	if err != nil {
		return fmt.Errorf("parse SMTP from address: %w", err)
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("parse email recipient: %w", err)
	}

	host := strings.TrimSpace(settings.Host)
	port := settings.Port
	if port <= 0 {
		port = 587
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	mode := strings.ToLower(strings.TrimSpace(settings.TLSMode))
	if mode == "" {
		mode = "starttls"
	}
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	var conn net.Conn
	if mode == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("dial SMTP server: %w", err)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("create SMTP client: %w", err)
	}
	defer client.Close()

	if mode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	}
	if strings.TrimSpace(settings.Username) != "" {
		auth := smtp.PlainAuth("", settings.Username, settings.Password, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	if err := client.Rcpt(recipient.Address); err != nil {
		return fmt.Errorf("SMTP RCPT TO: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	message := emailMessage(from.String(), recipient.String(), subject, textBody)
	if _, err := writer.Write([]byte(message)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP DATA: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close SMTP DATA: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("SMTP QUIT: %w", err)
	}
	return nil
}

func emailMessage(from string, to string, subject string, textBody string) string {
	subject = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(subject, "\r", " "), "\n", " "))
	headers := []string{
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"From: " + from,
		"To: " + to,
		"Subject: " + mime.QEncoding.Encode("UTF-8", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
	}
	return strings.Join(headers, "\r\n") + "\r\n\r\n" + strings.ReplaceAll(textBody, "\n", "\r\n")
}

func (s *Server) handleSecurityEmailFailure(ctx context.Context, userID string, purpose string, err error) {
	if err == nil {
		return
	}
	slog.WarnContext(ctx, "security email delivery failed", "user_id", userID, "purpose", purpose, "error", err)
	if s.db == nil {
		return
	}
	_ = s.emitNotification(ctx, notificationEventInput{
		EventType:  "security_email_failed",
		Severity:   "critical",
		Title:      "Security email delivery failed.",
		Message:    "A password reset or email verification email could not be delivered.",
		TargetType: "user",
		TargetID:   userID,
		Payload: map[string]any{
			"purpose": purpose,
			"user_id": userID,
		},
	})
}
