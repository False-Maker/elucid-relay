package httpserver

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

const stripeSettingKey = "billing.stripe"

type stripeSettings struct {
	SecretKey               string
	SecretKeyCiphertext     string
	SecretKeyNonce          string
	WebhookSecret           string
	WebhookSecretCiphertext string
	WebhookSecretNonce      string
	SuccessURL              string
	CancelURL               string
	SecretKeySource         string
	WebhookSecretSource     string
	SuccessURLSource        string
	CancelURLSource         string
}

func (s *Server) adminEffectiveSystemSettings(w http.ResponseWriter, r *http.Request, auth authContext) error {
	emailSettings, err := s.emailVerificationSettings(r.Context())
	if err != nil {
		return err
	}
	stripeSettings, err := s.stripeSettings(r.Context())
	if err != nil {
		return err
	}
	configStatus, err := s.adminConfigStatus(r.Context())
	if err != nil {
		return err
	}
	siteStatus, _ := configStatus["site"].(map[string]any)
	writeJSON(w, http.StatusOK, map[string]any{
		"site": map[string]any{
			"portal_base_url":   s.cfg.PortalBaseURL,
			"public_base_url":   s.cfg.PublicBaseURL,
			"open_registration": siteStatus["open_registration"],
		},
		"auth": map[string]any{
			"registration_email_verification_enabled": emailSettings.RegistrationVerificationEnabled,
			"smtp": emailVerificationSettingsResponse(emailSettings)["smtp"],
		},
		"stripe": map[string]any{
			"secret_key_configured":     stripeSettings.secretKeyConfigured(),
			"webhook_secret_configured": stripeSettings.webhookSecretConfigured(),
			"success_url":               stripeSettings.successURL("check", s.cfg.PortalBaseURL),
			"cancel_url":                stripeSettings.cancelURL("check", s.cfg.PortalBaseURL),
			"secret_key_source":         stripeSettings.SecretKeySource,
			"webhook_secret_source":     stripeSettings.WebhookSecretSource,
		},
		"security": configStatus["security"],
	}, nil)
	return nil
}

func (s *Server) adminStripeSettings(w http.ResponseWriter, r *http.Request, auth authContext) error {
	settings, err := s.stripeSettings(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, stripeSettingsResponse(settings, s.cfg.PortalBaseURL), nil)
	return nil
}

func (s *Server) adminPutStripeSettings(w http.ResponseWriter, r *http.Request, auth authContext) error {
	current, err := s.stripeSettings(r.Context())
	if err != nil {
		return err
	}
	var req struct {
		SecretKey     string `json:"secret_key"`
		WebhookSecret string `json:"webhook_secret"`
		SuccessURL    string `json:"success_url"`
		CancelURL     string `json:"cancel_url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	next := current
	next.SuccessURL = strings.TrimSpace(req.SuccessURL)
	next.CancelURL = strings.TrimSpace(req.CancelURL)
	if strings.TrimSpace(req.SecretKey) != "" {
		ciphertext, nonce, err := security.EncryptSecret(s.cfg.VaultKey, strings.TrimSpace(req.SecretKey))
		if err != nil {
			return err
		}
		next.SecretKey = strings.TrimSpace(req.SecretKey)
		next.SecretKeyCiphertext = base64.StdEncoding.EncodeToString(ciphertext)
		next.SecretKeyNonce = base64.StdEncoding.EncodeToString(nonce)
		next.SecretKeySource = "database"
	}
	if strings.TrimSpace(req.WebhookSecret) != "" {
		ciphertext, nonce, err := security.EncryptSecret(s.cfg.VaultKey, strings.TrimSpace(req.WebhookSecret))
		if err != nil {
			return err
		}
		next.WebhookSecret = strings.TrimSpace(req.WebhookSecret)
		next.WebhookSecretCiphertext = base64.StdEncoding.EncodeToString(ciphertext)
		next.WebhookSecretNonce = base64.StdEncoding.EncodeToString(nonce)
		next.WebhookSecretSource = "database"
	}
	value, err := encodeJSON(map[string]any{
		"secret_key_ciphertext":     next.SecretKeyCiphertext,
		"secret_key_nonce":          next.SecretKeyNonce,
		"webhook_secret_ciphertext": next.WebhookSecretCiphertext,
		"webhook_secret_nonce":      next.WebhookSecretNonce,
		"success_url":               next.SuccessURL,
		"cancel_url":                next.CancelURL,
	})
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(r.Context(), `
		INSERT INTO system_settings (setting_key, category, setting_value_json, is_public, updated_by)
		VALUES ($1, 'billing', $2::jsonb, false, $3)
		ON CONFLICT (setting_key) DO UPDATE SET
		  category = EXCLUDED.category,
		  setting_value_json = EXCLUDED.setting_value_json,
		  is_public = false,
		  updated_by = EXCLUDED.updated_by
	`, stripeSettingKey, value, auth.UserID); err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "stripe_settings.update", "system_setting", stripeSettingKey, r, map[string]any{"secret_key_updated": strings.TrimSpace(req.SecretKey) != "", "webhook_secret_updated": strings.TrimSpace(req.WebhookSecret) != ""})
	writeJSON(w, http.StatusOK, stripeSettingsResponse(next, s.cfg.PortalBaseURL), nil)
	return nil
}

func (s *Server) adminTestStripeSettings(w http.ResponseWriter, r *http.Request, auth authContext) error {
	settings, err := s.stripeSettings(r.Context())
	if err != nil {
		return err
	}
	secret := strings.TrimSpace(settings.SecretKey)
	if secret == "" {
		return upstreamUnavailable("stripe_not_configured", "Stripe secret key is not configured.")
	}
	if !strings.HasPrefix(secret, "sk_test_") && !strings.HasPrefix(secret, "sk_live_") {
		return badRequest("Stripe secret key format is invalid.")
	}
	account, err := s.testStripeAccount(r.Context(), secret)
	if err != nil {
		return err
	}
	webhookConfigured := settings.webhookSecretConfigured()
	mode := stripeKeyMode(secret)
	audit(r.Context(), s.db, auth.UserID, "admin", "stripe_settings.test", "system_setting", "stripe", r, map[string]any{"webhook_secret_configured": webhookConfigured, "mode": mode, "account_id": account.ID})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                        true,
		"mode":                      mode,
		"webhook_secret_configured": webhookConfigured,
		"success_url":               settings.successURL("check", s.cfg.PortalBaseURL),
		"cancel_url":                settings.cancelURL("check", s.cfg.PortalBaseURL),
		"account": map[string]any{
			"id":                 account.ID,
			"charges_enabled":    account.ChargesEnabled,
			"payouts_enabled":    account.PayoutsEnabled,
			"details_submitted":  account.DetailsSubmitted,
			"default_currency":   account.DefaultCurrency,
			"country":            account.Country,
			"livemode":           account.LiveMode,
			"settings_dashboard": account.DashboardDisplayName,
			"business_profile":   account.BusinessName,
			"api_base_url":       stripeAPIBaseURL(s.cfg.StripeAPIBaseURL),
			"secret_key_source":  settings.SecretKeySource,
			"webhook_key_source": settings.WebhookSecretSource,
		},
	}, nil)
	return nil
}

type stripeAccountResponse struct {
	ID                   string `json:"id"`
	ChargesEnabled       bool   `json:"charges_enabled"`
	PayoutsEnabled       bool   `json:"payouts_enabled"`
	DetailsSubmitted     bool   `json:"details_submitted"`
	DefaultCurrency      string `json:"default_currency"`
	Country              string `json:"country"`
	LiveMode             bool   `json:"livemode"`
	DashboardDisplayName string
	BusinessName         string
}

func (s *Server) testStripeAccount(ctx context.Context, secret string) (stripeAccountResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(stripeAPIBaseURL(s.cfg.StripeAPIBaseURL), "/")+"/v1/account", nil)
	if err != nil {
		return stripeAccountResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Stripe-Version", "2024-06-20")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return stripeAccountResponse{}, upstreamUnavailable("stripe_unreachable", "Stripe API is unreachable.")
	}
	defer resp.Body.Close()

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return stripeAccountResponse{}, upstreamUnavailable("stripe_invalid_response", "Stripe API returned an invalid response.")
	}
	if resp.StatusCode >= 400 {
		message := "Stripe API rejected the secret key."
		if errObj, ok := raw["error"].(map[string]any); ok {
			if value, ok := errObj["message"].(string); ok && strings.TrimSpace(value) != "" {
				message = value
			}
		}
		return stripeAccountResponse{}, appError{status: http.StatusBadGateway, code: "stripe_api_error", message: message, typ: "upstream_error"}
	}

	settings, _ := raw["settings"].(map[string]any)
	dashboard, _ := settings["dashboard"].(map[string]any)
	businessProfile, _ := raw["business_profile"].(map[string]any)
	return stripeAccountResponse{
		ID:                   stringValue(raw["id"], ""),
		ChargesEnabled:       boolValue(raw["charges_enabled"], false),
		PayoutsEnabled:       boolValue(raw["payouts_enabled"], false),
		DetailsSubmitted:     boolValue(raw["details_submitted"], false),
		DefaultCurrency:      stringValue(raw["default_currency"], ""),
		Country:              stringValue(raw["country"], ""),
		LiveMode:             boolValue(raw["livemode"], false),
		DashboardDisplayName: stringValue(dashboard["display_name"], ""),
		BusinessName:         stringValue(businessProfile["name"], ""),
	}, nil
}

func (s *Server) stripeSettings(ctx context.Context) (stripeSettings, error) {
	settings := stripeSettings{
		SecretKey:           strings.TrimSpace(s.cfg.StripeSecretKey),
		WebhookSecret:       strings.TrimSpace(s.cfg.StripeWebhookSecret),
		SuccessURL:          strings.TrimSpace(s.cfg.BillingSuccessURL),
		CancelURL:           strings.TrimSpace(s.cfg.BillingCancelURL),
		SecretKeySource:     sourceLabel(s.cfg.StripeSecretKey, "env"),
		WebhookSecretSource: sourceLabel(s.cfg.StripeWebhookSecret, "env"),
		SuccessURLSource:    sourceLabel(s.cfg.BillingSuccessURL, "env"),
		CancelURLSource:     sourceLabel(s.cfg.BillingCancelURL, "env"),
	}
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT setting_value_json::text FROM system_settings WHERE setting_key = $1", stripeSettingKey).Scan(&raw)
	if err == sql.ErrNoRows {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	var stored map[string]any
	if err := decodeJSONString(raw, &stored); err != nil {
		return settings, err
	}
	settings.SecretKeyCiphertext = stringValue(stored["secret_key_ciphertext"], "")
	settings.SecretKeyNonce = stringValue(stored["secret_key_nonce"], "")
	settings.WebhookSecretCiphertext = stringValue(stored["webhook_secret_ciphertext"], "")
	settings.WebhookSecretNonce = stringValue(stored["webhook_secret_nonce"], "")
	if successURL := stringValue(stored["success_url"], ""); successURL != "" {
		settings.SuccessURL = successURL
		settings.SuccessURLSource = "database"
	}
	if cancelURL := stringValue(stored["cancel_url"], ""); cancelURL != "" {
		settings.CancelURL = cancelURL
		settings.CancelURLSource = "database"
	}
	if decrypted, ok := s.decryptSettingSecret(settings.SecretKeyCiphertext, settings.SecretKeyNonce); ok {
		settings.SecretKey = decrypted
		settings.SecretKeySource = "database"
	}
	if decrypted, ok := s.decryptSettingSecret(settings.WebhookSecretCiphertext, settings.WebhookSecretNonce); ok {
		settings.WebhookSecret = decrypted
		settings.WebhookSecretSource = "database"
	}
	return settings, nil
}

func (s *Server) decryptSettingSecret(ciphertextValue string, nonceValue string) (string, bool) {
	if ciphertextValue == "" || nonceValue == "" {
		return "", false
	}
	ciphertext, cipherErr := base64.StdEncoding.DecodeString(ciphertextValue)
	nonce, nonceErr := base64.StdEncoding.DecodeString(nonceValue)
	if cipherErr != nil || nonceErr != nil {
		return "", false
	}
	secret, err := security.DecryptSecret(s.cfg.VaultKey, ciphertext, nonce)
	return strings.TrimSpace(secret), err == nil && strings.TrimSpace(secret) != ""
}

func stripeSettingsResponse(settings stripeSettings, portalBaseURL string) map[string]any {
	return map[string]any{
		"secret_key_configured":     settings.secretKeyConfigured(),
		"webhook_secret_configured": settings.webhookSecretConfigured(),
		"secret_key_source":         settings.SecretKeySource,
		"webhook_secret_source":     settings.WebhookSecretSource,
		"success_url":               settings.successURL("check", portalBaseURL),
		"cancel_url":                settings.cancelURL("check", portalBaseURL),
		"success_url_source":        settings.SuccessURLSource,
		"cancel_url_source":         settings.CancelURLSource,
		"mode":                      stripeKeyMode(settings.SecretKey),
	}
}

func (settings stripeSettings) secretKeyConfigured() bool {
	return strings.TrimSpace(settings.SecretKey) != ""
}

func (settings stripeSettings) webhookSecretConfigured() bool {
	return strings.TrimSpace(settings.WebhookSecret) != ""
}

func (settings stripeSettings) successURL(orderID string, portalBaseURL string) string {
	if settings.SuccessURL != "" {
		return settings.SuccessURL
	}
	return strings.TrimRight(portalBaseURL, "/") + "/?billing=success&order_id=" + url.QueryEscape(orderID)
}

func (settings stripeSettings) cancelURL(orderID string, portalBaseURL string) string {
	if settings.CancelURL != "" {
		return settings.CancelURL
	}
	return strings.TrimRight(portalBaseURL, "/") + "/?billing=cancel&order_id=" + url.QueryEscape(orderID)
}

func sourceLabel(value string, label string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return label
}

func stripeKeyMode(secret string) string {
	if strings.HasPrefix(secret, "sk_live_") {
		return "live"
	}
	if strings.HasPrefix(secret, "sk_test_") {
		return "test"
	}
	return "unknown"
}

func stripeAPIBaseURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "https://api.stripe.com"
	}
	return value
}
