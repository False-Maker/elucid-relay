package httpserver

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/security"
)

const (
	paymentSettingsKey = "billing.payments"
	defaultFXRate      = "7.2000000000"
)

type paymentSettings struct {
	Enabled                 bool
	FXUSDCNY                string
	OrderTimeoutMinutes     int
	MaxPendingOrdersPerUser int
	CancelCooldownSeconds   int
	AutoReconcileEnabled    bool
	ProviderSelection       string
	HelpText                string
	StripeSuccessURL        string
	StripeCancelURL         string
	StripeSuccessURLSource  string
	StripeCancelURLSource   string
}

type paymentProviderInstance struct {
	ID               string
	ProviderType     string
	Name             string
	Status           string
	Priority         int
	Weight           int
	SupportedMethods []string
	MinAmountUSD     string
	MaxAmountUSD     string
	DailyLimitUSD    string
	Config           map[string]any
	Secret           map[string]any
	Metadata         map[string]any
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type paymentCheckoutResult struct {
	Provider              string
	Method                string
	ProviderInstanceID    string
	UpstreamTradeNo       string
	UpstreamTransactionID string
	CheckoutURL           string
	QRCodeURL             string
	PayCurrency           string
	PayAmountCents        int
	FXRate                string
	RawResponse           map[string]any
	ExpiresAt             time.Time
}

type unifiedPaymentEvent struct {
	ID                    string
	Provider              string
	Method                string
	OrderID               string
	EventType             string
	UpstreamTradeNo       string
	UpstreamTransactionID string
	Payload               []byte
	Object                map[string]any
}

type paymentMethodRoute struct {
	Method        string
	Enabled       bool
	DisplayName   string
	ProviderTypes []string
	MinAmountUSD  string
	MaxAmountUSD  string
	Metadata      map[string]any
}

func (s *Server) adminPaymentSettings(w http.ResponseWriter, r *http.Request, auth authContext) error {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.paymentSettings(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, paymentSettingsResponse(settings), nil)
		return nil
	case http.MethodPut:
		var req struct {
			Enabled                 *bool  `json:"enabled"`
			FXUSDCNY                string `json:"fx_usd_cny"`
			OrderTimeoutMinutes     *int   `json:"order_timeout_minutes"`
			MaxPendingOrdersPerUser *int   `json:"max_pending_orders_per_user"`
			CancelCooldownSeconds   *int   `json:"cancel_cooldown_seconds"`
			AutoReconcileEnabled    *bool  `json:"auto_reconcile_enabled"`
			ProviderSelection       string `json:"provider_selection"`
			HelpText                string `json:"help_text"`
			StripeSuccessURL        string `json:"stripe_success_url"`
			StripeCancelURL         string `json:"stripe_cancel_url"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		current, err := s.paymentSettings(r.Context())
		if err != nil {
			return err
		}
		if req.Enabled != nil {
			current.Enabled = *req.Enabled
		}
		if strings.TrimSpace(req.FXUSDCNY) != "" {
			if parsePositiveFloat(req.FXUSDCNY, 0) <= 0 {
				return badRequest("fx_usd_cny must be positive.")
			}
			current.FXUSDCNY = strings.TrimSpace(req.FXUSDCNY)
		}
		if req.OrderTimeoutMinutes != nil {
			if *req.OrderTimeoutMinutes <= 0 {
				return badRequest("order_timeout_minutes must be positive.")
			}
			current.OrderTimeoutMinutes = *req.OrderTimeoutMinutes
		}
		if req.MaxPendingOrdersPerUser != nil {
			if *req.MaxPendingOrdersPerUser <= 0 {
				return badRequest("max_pending_orders_per_user must be positive.")
			}
			current.MaxPendingOrdersPerUser = *req.MaxPendingOrdersPerUser
		}
		if req.CancelCooldownSeconds != nil {
			if *req.CancelCooldownSeconds < 0 {
				return badRequest("cancel_cooldown_seconds must be non-negative.")
			}
			current.CancelCooldownSeconds = *req.CancelCooldownSeconds
		}
		if req.AutoReconcileEnabled != nil {
			current.AutoReconcileEnabled = *req.AutoReconcileEnabled
		}
		if strings.TrimSpace(req.ProviderSelection) != "" {
			selection, err := defaultedEnum(req.ProviderSelection, "priority", "priority", "weighted")
			if err != nil {
				return err
			}
			current.ProviderSelection = selection
		}
		current.HelpText = strings.TrimSpace(req.HelpText)
		current.StripeSuccessURL = strings.TrimSpace(req.StripeSuccessURL)
		current.StripeCancelURL = strings.TrimSpace(req.StripeCancelURL)
		if current.StripeSuccessURL != "" {
			current.StripeSuccessURLSource = "database"
		}
		if current.StripeCancelURL != "" {
			current.StripeCancelURLSource = "database"
		}
		value, err := encodeJSON(map[string]any{
			"enabled":                     current.Enabled,
			"fx_usd_cny":                  current.FXUSDCNY,
			"order_timeout_minutes":       current.OrderTimeoutMinutes,
			"max_pending_orders_per_user": current.MaxPendingOrdersPerUser,
			"cancel_cooldown_seconds":     current.CancelCooldownSeconds,
			"auto_reconcile_enabled":      current.AutoReconcileEnabled,
			"provider_selection":          current.ProviderSelection,
			"help_text":                   current.HelpText,
			"stripe_success_url":          current.StripeSuccessURL,
			"stripe_cancel_url":           current.StripeCancelURL,
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
		`, paymentSettingsKey, value, auth.UserID); err != nil {
			return err
		}
		audit(r.Context(), s.db, auth.UserID, "admin", "payment_settings.update", "system_setting", paymentSettingsKey, r, nil)
		writeJSON(w, http.StatusOK, paymentSettingsResponse(current), nil)
		return nil
	default:
		return notFound("Endpoint was not found.")
	}
}

func (s *Server) adminPaymentProviders(w http.ResponseWriter, r *http.Request, auth authContext) error {
	switch r.Method {
	case http.MethodGet:
		providers, err := s.listPaymentProviders(r.Context(), false)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, paymentProviderResponses(providers), nil)
		return nil
	case http.MethodPost:
		id, err := s.upsertPaymentProvider(r, "", true, auth.UserID)
		if err != nil {
			return err
		}
		audit(r.Context(), s.db, auth.UserID, "admin", "payment_provider.create", "payment_provider", id, r, nil)
		writeJSON(w, http.StatusCreated, map[string]any{"id": id}, nil)
		return nil
	default:
		return notFound("Endpoint was not found.")
	}
}

func (s *Server) adminPatchPaymentProvider(w http.ResponseWriter, r *http.Request, auth authContext) error {
	id, err := s.upsertPaymentProvider(r, r.PathValue("providerId"), false, auth.UserID)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "payment_provider.update", "payment_provider", id, r, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true}, nil)
	return nil
}

func (s *Server) adminTestPaymentProvider(w http.ResponseWriter, r *http.Request, auth authContext) error {
	provider, err := s.getPaymentProvider(r.Context(), r.PathValue("providerId"))
	if err != nil {
		return err
	}
	result, err := s.testPaymentProvider(r.Context(), provider)
	if err != nil {
		return err
	}
	audit(r.Context(), s.db, auth.UserID, "admin", "payment_provider.test", "payment_provider", provider.ID, r, map[string]any{"provider_type": provider.ProviderType})
	writeJSON(w, http.StatusOK, result, nil)
	return nil
}

func (s *Server) adminPaymentMethodRoutes(w http.ResponseWriter, r *http.Request, auth authContext) error {
	switch r.Method {
	case http.MethodGet:
		routes, err := s.listPaymentRoutes(r.Context(), false)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, paymentRouteResponses(routes), nil)
		return nil
	case http.MethodPut:
		var req struct {
			Routes []struct {
				Method        string         `json:"method"`
				Enabled       bool           `json:"enabled"`
				DisplayName   string         `json:"display_name"`
				ProviderTypes []string       `json:"provider_types"`
				MinAmountUSD  string         `json:"min_amount_usd"`
				MaxAmountUSD  string         `json:"max_amount_usd"`
				Metadata      map[string]any `json:"metadata"`
			} `json:"routes"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		for _, route := range req.Routes {
			method, err := defaultedEnum(route.Method, "", "stripe", "alipay", "wechat")
			if err != nil || method == "" {
				return badRequest("Invalid payment method.")
			}
			min := defaultString(route.MinAmountUSD, "0")
			max := defaultString(route.MaxAmountUSD, "0")
			if _, err := nullableNonNegativeDecimal(&min); err != nil {
				return badRequest("min_amount_usd must be non-negative.")
			}
			if _, err := nullableNonNegativeDecimal(&max); err != nil {
				return badRequest("max_amount_usd must be non-negative.")
			}
			metadata, err := encodeJSON(defaultMap(route.Metadata))
			if err != nil {
				return err
			}
			if _, err := s.db.ExecContext(r.Context(), `
				INSERT INTO payment_method_routes (method, enabled, display_name, provider_types, min_amount_usd, max_amount_usd, metadata_json)
				VALUES ($1, $2, $3, $4::text[], $5::numeric, $6::numeric, $7::jsonb)
				ON CONFLICT (method) DO UPDATE SET
				  enabled = EXCLUDED.enabled,
				  display_name = EXCLUDED.display_name,
				  provider_types = EXCLUDED.provider_types,
				  min_amount_usd = EXCLUDED.min_amount_usd,
				  max_amount_usd = EXCLUDED.max_amount_usd,
				  metadata_json = EXCLUDED.metadata_json
			`, method, route.Enabled, defaultPaymentDisplayName(method, route.DisplayName), stringSliceLiteral(normalizeStringList(route.ProviderTypes)), min, max, metadata); err != nil {
				return err
			}
		}
		audit(r.Context(), s.db, auth.UserID, "admin", "payment_routes.update", "payment_routes", "all", r, nil)
		routes, err := s.listPaymentRoutes(r.Context(), false)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, paymentRouteResponses(routes), nil)
		return nil
	default:
		return notFound("Endpoint was not found.")
	}
}

func (s *Server) portalPaymentMethods(w http.ResponseWriter, r *http.Request, auth authContext) error {
	methods, err := s.portalPaymentMethodsList(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, methods, nil)
	return nil
}

func (s *Server) paymentWebhook(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.PathValue("provider"))
	providerID := strings.TrimSpace(r.PathValue("providerId"))
	body, err := readLimitedRequestBody(r.Body, 1<<20)
	if err != nil {
		writeError(w, r, err)
		return
	}
	providerRecord, err := s.getPaymentProvider(r.Context(), providerID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if providerRecord.ProviderType != provider {
		writeError(w, r, badRequest("Webhook provider does not match provider instance."))
		return
	}
	if providerRecord.ProviderType == "stripe" {
		s.stripeProviderWebhook(w, r, providerRecord, body)
		return
	}
	event, err := s.decodeUnifiedPaymentWebhook(r.Context(), providerRecord, r, body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if event.OrderID == "" && event.UpstreamTradeNo != "" {
		event.OrderID = s.lookupUnifiedPaymentOrderID(r.Context(), event.Provider, event.UpstreamTradeNo)
	}
	if event.ID == "" {
		event.ID = event.Provider + ":" + event.UpstreamTradeNo + ":" + event.UpstreamTransactionID + ":" + event.EventType
	}
	paymentEventID, inserted, err := s.insertProviderPaymentEvent(r.Context(), event.OrderID, event.Provider, event.ID, event.EventType, event.Payload)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !inserted {
		writeJSON(w, http.StatusOK, map[string]any{"duplicate": true}, nil)
		return
	}
	if err := s.markPaymentEventProcessing(r.Context(), paymentEventID); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.handleUnifiedPaymentEvent(r.Context(), event); err != nil {
		_ = s.markPaymentEventFailed(r.Context(), paymentEventID, err)
		writeError(w, r, err)
		return
	}
	if _, err := s.markPaymentEventSucceeded(r.Context(), paymentEventID, "processed"); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": true}, nil)
}

func (s *Server) stripeProviderWebhook(w http.ResponseWriter, r *http.Request, provider paymentProviderInstance, body []byte) {
	secret := strings.TrimSpace(s.paymentSecret(provider, "webhook_secret"))
	if secret != "" && !verifyStripeSignature(body, r.Header.Get("Stripe-Signature"), secret) {
		writeError(w, r, unauthorized("Invalid Stripe signature."))
		return
	}
	event, err := decodeStripeWebhookEvent(body)
	if err != nil {
		writeError(w, r, badRequest("Invalid Stripe webhook payload."))
		return
	}
	orderID := stripeOrderID(event.Data.Object)
	if orderID == "" {
		orderID = s.lookupStripeOrderID(r.Context(), event.Data.Object)
	}
	paymentEventID, inserted, err := s.insertProviderPaymentEvent(r.Context(), orderID, "stripe", event.ID, event.Type, body)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !inserted {
		writeJSON(w, http.StatusOK, map[string]any{"duplicate": true}, nil)
		return
	}
	if err := s.markPaymentEventProcessing(r.Context(), paymentEventID); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.handleStripeEvent(r.Context(), orderID, event.Type, event.Data.Object); err != nil {
		_ = s.markPaymentEventFailed(r.Context(), paymentEventID, err)
		writeError(w, r, err)
		return
	}
	if _, err := s.markPaymentEventSucceeded(r.Context(), paymentEventID, "processed"); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": true}, nil)
}

func (s *Server) paymentSettings(ctx context.Context) (paymentSettings, error) {
	settings := paymentSettings{
		Enabled:                 false,
		FXUSDCNY:                defaultFXRate,
		OrderTimeoutMinutes:     30,
		MaxPendingOrdersPerUser: 5,
		CancelCooldownSeconds:   60,
		AutoReconcileEnabled:    true,
		ProviderSelection:       "priority",
		StripeSuccessURL:        strings.TrimSpace(s.cfg.BillingSuccessURL),
		StripeCancelURL:         strings.TrimSpace(s.cfg.BillingCancelURL),
		StripeSuccessURLSource:  sourceLabel(s.cfg.BillingSuccessURL, "env"),
		StripeCancelURLSource:   sourceLabel(s.cfg.BillingCancelURL, "env"),
	}
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT setting_value_json::text FROM system_settings WHERE setting_key = $1", paymentSettingsKey).Scan(&raw)
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
	settings.Enabled = boolValue(stored["enabled"], settings.Enabled)
	settings.FXUSDCNY = defaultString(stringValue(stored["fx_usd_cny"], ""), settings.FXUSDCNY)
	settings.OrderTimeoutMinutes = intValue(stored["order_timeout_minutes"], settings.OrderTimeoutMinutes)
	settings.MaxPendingOrdersPerUser = intValue(stored["max_pending_orders_per_user"], settings.MaxPendingOrdersPerUser)
	settings.CancelCooldownSeconds = intValue(stored["cancel_cooldown_seconds"], settings.CancelCooldownSeconds)
	settings.AutoReconcileEnabled = boolValue(stored["auto_reconcile_enabled"], settings.AutoReconcileEnabled)
	settings.ProviderSelection = defaultString(stringValue(stored["provider_selection"], ""), settings.ProviderSelection)
	settings.HelpText = stringValue(stored["help_text"], "")
	if successURL := stringValue(stored["stripe_success_url"], ""); successURL != "" {
		settings.StripeSuccessURL = successURL
		settings.StripeSuccessURLSource = "database"
	}
	if cancelURL := stringValue(stored["stripe_cancel_url"], ""); cancelURL != "" {
		settings.StripeCancelURL = cancelURL
		settings.StripeCancelURLSource = "database"
	}
	return settings, nil
}

func paymentSettingsResponse(settings paymentSettings) map[string]any {
	return map[string]any{
		"enabled":                     settings.Enabled,
		"fx_usd_cny":                  settings.FXUSDCNY,
		"order_timeout_minutes":       settings.OrderTimeoutMinutes,
		"max_pending_orders_per_user": settings.MaxPendingOrdersPerUser,
		"cancel_cooldown_seconds":     settings.CancelCooldownSeconds,
		"auto_reconcile_enabled":      settings.AutoReconcileEnabled,
		"provider_selection":          settings.ProviderSelection,
		"help_text":                   settings.HelpText,
		"stripe_success_url":          settings.stripeSuccessURL("check", ""),
		"stripe_cancel_url":           settings.stripeCancelURL("check", ""),
		"stripe_success_url_source":   settings.StripeSuccessURLSource,
		"stripe_cancel_url_source":    settings.StripeCancelURLSource,
	}
}

func (settings paymentSettings) stripeSuccessURL(orderID string, portalBaseURL string) string {
	if settings.StripeSuccessURL != "" {
		return settings.StripeSuccessURL
	}
	if portalBaseURL == "" {
		return ""
	}
	return strings.TrimRight(portalBaseURL, "/") + "/?billing=success&order_id=" + url.QueryEscape(orderID)
}

func (settings paymentSettings) stripeCancelURL(orderID string, portalBaseURL string) string {
	if settings.StripeCancelURL != "" {
		return settings.StripeCancelURL
	}
	if portalBaseURL == "" {
		return ""
	}
	return strings.TrimRight(portalBaseURL, "/") + "/?billing=cancel&order_id=" + url.QueryEscape(orderID)
}

func (s *Server) upsertPaymentProvider(r *http.Request, providerID string, insert bool, userID string) (string, error) {
	var req struct {
		ProviderType     string         `json:"provider_type"`
		Name             string         `json:"name"`
		Status           string         `json:"status"`
		Priority         *int           `json:"priority"`
		Weight           *int           `json:"weight"`
		SupportedMethods []string       `json:"supported_methods"`
		MinAmountUSD     string         `json:"min_amount_usd"`
		MaxAmountUSD     string         `json:"max_amount_usd"`
		DailyLimitUSD    string         `json:"daily_limit_usd"`
		Config           map[string]any `json:"config"`
		Secrets          map[string]any `json:"secrets"`
		Metadata         map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return "", err
	}
	providerType, err := defaultedEnum(req.ProviderType, "", "stripe", "easypay", "alipay", "wechat")
	if err != nil || providerType == "" {
		return "", badRequest("provider_type is required.")
	}
	name := strings.TrimSpace(req.Name)
	if insert && name == "" {
		return "", badRequest("name is required.")
	}
	status, err := defaultedEnum(req.Status, "disabled", "active", "disabled")
	if err != nil {
		return "", err
	}
	priority := 100
	if req.Priority != nil {
		priority = *req.Priority
	}
	if priority < 0 {
		return "", badRequest("priority must be non-negative.")
	}
	weight := 1
	if req.Weight != nil {
		weight = *req.Weight
	}
	if weight < 0 {
		return "", badRequest("weight must be non-negative.")
	}
	methods := normalizePaymentMethods(providerType, req.SupportedMethods)
	minAmount := defaultString(req.MinAmountUSD, "0")
	maxAmount := defaultString(req.MaxAmountUSD, "0")
	dailyLimit := defaultString(req.DailyLimitUSD, "0")
	for _, pair := range []struct {
		field string
		value string
	}{{"min_amount_usd", minAmount}, {"max_amount_usd", maxAmount}, {"daily_limit_usd", dailyLimit}} {
		value := pair.value
		if _, err := nullableNonNegativeDecimal(&value); err != nil {
			return "", badRequest(pair.field + " must be non-negative.")
		}
	}
	config := defaultMap(req.Config)
	metadata := defaultMap(req.Metadata)
	secretJSON := map[string]any{}
	if !insert {
		current, err := s.getPaymentProvider(r.Context(), providerID)
		if err != nil {
			return "", err
		}
		if name == "" {
			name = current.Name
		}
		if len(req.SupportedMethods) == 0 {
			methods = current.SupportedMethods
		}
		if len(config) == 0 {
			config = current.Config
		}
		if len(metadata) == 0 {
			metadata = current.Metadata
		}
		secretJSON = current.Secret
	}
	if req.Secrets != nil {
		encrypted, err := s.encryptPaymentSecrets(req.Secrets)
		if err != nil {
			return "", err
		}
		for key, value := range encrypted {
			secretJSON[key] = value
		}
	}
	configText, err := encodeJSON(config)
	if err != nil {
		return "", err
	}
	secretText, err := encodeJSON(secretJSON)
	if err != nil {
		return "", err
	}
	metadataText, err := encodeJSON(metadata)
	if err != nil {
		return "", err
	}
	if insert {
		err = s.db.QueryRowContext(r.Context(), `
			INSERT INTO payment_provider_instances (provider_type, name, status, priority, weight, supported_methods, min_amount_usd, max_amount_usd, daily_limit_usd, config_json, secret_json, metadata_json)
			VALUES ($1, $2, $3, $4, $5, $6::text[], $7::numeric, $8::numeric, $9::numeric, $10::jsonb, $11::jsonb, $12::jsonb)
			RETURNING id::text
		`, providerType, name, status, priority, weight, stringSliceLiteral(methods), minAmount, maxAmount, dailyLimit, configText, secretText, metadataText).Scan(&providerID)
		return providerID, err
	}
	if _, err := s.db.ExecContext(r.Context(), `
		UPDATE payment_provider_instances
		SET provider_type = $2,
		    name = $3,
		    status = $4,
		    priority = $5,
		    weight = $6,
		    supported_methods = $7::text[],
		    min_amount_usd = $8::numeric,
		    max_amount_usd = $9::numeric,
		    daily_limit_usd = $10::numeric,
		    config_json = $11::jsonb,
		    secret_json = $12::jsonb,
		    metadata_json = $13::jsonb
		WHERE id = $1
	`, providerID, providerType, name, status, priority, weight, stringSliceLiteral(methods), minAmount, maxAmount, dailyLimit, configText, secretText, metadataText); err != nil {
		return "", err
	}
	return providerID, nil
}

func (s *Server) encryptPaymentSecrets(raw map[string]any) (map[string]any, error) {
	result := map[string]any{}
	for key, value := range raw {
		secret := strings.TrimSpace(fmt.Sprint(value))
		if secret == "" {
			continue
		}
		ciphertext, nonce, err := security.EncryptSecret(s.cfg.VaultKey, secret)
		if err != nil {
			return nil, err
		}
		result[key] = map[string]any{
			"ciphertext": base64.StdEncoding.EncodeToString(ciphertext),
			"nonce":      base64.StdEncoding.EncodeToString(nonce),
		}
	}
	return result, nil
}

func (s *Server) paymentSecret(provider paymentProviderInstance, key string) string {
	raw, ok := provider.Secret[key]
	if !ok {
		return ""
	}
	entry, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	secret, ok := s.decryptSettingSecret(stringValue(entry["ciphertext"], ""), stringValue(entry["nonce"], ""))
	if !ok {
		return ""
	}
	return secret
}

func (s *Server) listPaymentProviders(ctx context.Context, activeOnly bool) ([]paymentProviderInstance, error) {
	where := ""
	if activeOnly {
		where = "WHERE status = 'active'"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, provider_type, name, status, priority, weight, supported_methods::text,
		       min_amount_usd::text, max_amount_usd::text, daily_limit_usd::text,
		       config_json::text, secret_json::text, metadata_json::text, created_at, updated_at
		FROM payment_provider_instances
		`+where+`
		ORDER BY priority ASC, created_at ASC
		LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []paymentProviderInstance{}
	for rows.Next() {
		item, err := scanPaymentProvider(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Server) getPaymentProvider(ctx context.Context, id string) (paymentProviderInstance, error) {
	var provider paymentProviderInstance
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text, provider_type, name, status, priority, weight, supported_methods::text,
		       min_amount_usd::text, max_amount_usd::text, daily_limit_usd::text,
		       config_json::text, secret_json::text, metadata_json::text, created_at, updated_at
		FROM payment_provider_instances
		WHERE id = $1
	`, id).Scan(
		&provider.ID, &provider.ProviderType, &provider.Name, &provider.Status, &provider.Priority, &provider.Weight,
		(*pgTextArray)(&provider.SupportedMethods), &provider.MinAmountUSD, &provider.MaxAmountUSD, &provider.DailyLimitUSD,
		(*jsonMap)(&provider.Config), (*jsonMap)(&provider.Secret), (*jsonMap)(&provider.Metadata), &provider.CreatedAt, &provider.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return paymentProviderInstance{}, notFound("Payment provider was not found.")
	}
	return provider, err
}

func scanPaymentProvider(scanner interface{ Scan(...any) error }) (paymentProviderInstance, error) {
	var provider paymentProviderInstance
	err := scanner.Scan(
		&provider.ID, &provider.ProviderType, &provider.Name, &provider.Status, &provider.Priority, &provider.Weight,
		(*pgTextArray)(&provider.SupportedMethods), &provider.MinAmountUSD, &provider.MaxAmountUSD, &provider.DailyLimitUSD,
		(*jsonMap)(&provider.Config), (*jsonMap)(&provider.Secret), (*jsonMap)(&provider.Metadata), &provider.CreatedAt, &provider.UpdatedAt,
	)
	return provider, err
}

func paymentProviderResponses(providers []paymentProviderInstance) []map[string]any {
	items := []map[string]any{}
	for _, provider := range providers {
		secretConfigured := map[string]bool{}
		for key, value := range provider.Secret {
			if _, ok := value.(map[string]any); ok {
				secretConfigured[key] = true
			}
		}
		items = append(items, map[string]any{
			"id":                provider.ID,
			"provider_type":     provider.ProviderType,
			"name":              provider.Name,
			"status":            provider.Status,
			"priority":          provider.Priority,
			"weight":            provider.Weight,
			"supported_methods": provider.SupportedMethods,
			"min_amount_usd":    provider.MinAmountUSD,
			"max_amount_usd":    provider.MaxAmountUSD,
			"daily_limit_usd":   provider.DailyLimitUSD,
			"config":            provider.Config,
			"secret_configured": secretConfigured,
			"metadata":          provider.Metadata,
			"webhook_url":       "/api/billing/v1/payment/webhook/" + provider.ProviderType + "/" + provider.ID,
			"created_at":        provider.CreatedAt.UTC().Format(time.RFC3339),
			"updated_at":        provider.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return items
}

func (s *Server) listPaymentRoutes(ctx context.Context, publicOnly bool) ([]paymentMethodRoute, error) {
	where := ""
	if publicOnly {
		where = "WHERE enabled = true"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT method, enabled, display_name, provider_types::text, min_amount_usd::text, max_amount_usd::text, metadata_json::text
		FROM payment_method_routes
		`+where+`
		ORDER BY CASE method WHEN 'alipay' THEN 1 WHEN 'wechat' THEN 2 WHEN 'stripe' THEN 3 ELSE 99 END
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	routes := []paymentMethodRoute{}
	for rows.Next() {
		var route paymentMethodRoute
		if err := rows.Scan(&route.Method, &route.Enabled, &route.DisplayName, (*pgTextArray)(&route.ProviderTypes), &route.MinAmountUSD, &route.MaxAmountUSD, (*jsonMap)(&route.Metadata)); err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

func paymentRouteResponses(routes []paymentMethodRoute) []map[string]any {
	items := []map[string]any{}
	for _, route := range routes {
		items = append(items, map[string]any{
			"method":         route.Method,
			"enabled":        route.Enabled,
			"display_name":   defaultPaymentDisplayName(route.Method, route.DisplayName),
			"provider_types": route.ProviderTypes,
			"min_amount_usd": route.MinAmountUSD,
			"max_amount_usd": route.MaxAmountUSD,
			"metadata":       route.Metadata,
		})
	}
	return items
}

func (s *Server) portalPaymentMethodsList(ctx context.Context) ([]map[string]any, error) {
	settings, err := s.paymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	stripeSettings, err := s.stripeSettings(ctx)
	if err != nil {
		return nil, err
	}
	routes, err := s.listPaymentRoutes(ctx, true)
	if err != nil {
		return nil, err
	}
	providers, err := s.listPaymentProviders(ctx, true)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	included := map[string]bool{}
	for _, route := range routes {
		if !settings.Enabled && route.Method != "stripe" {
			continue
		}
		if route.Method != "stripe" && !hasProviderForRoute(route, providers) {
			continue
		}
		if route.Method == "stripe" && !hasProviderForRoute(route, providers) && !stripeSettings.secretKeyConfigured() {
			continue
		}
		items = append(items, map[string]any{
			"method":         route.Method,
			"display_name":   defaultPaymentDisplayName(route.Method, route.DisplayName),
			"min_amount_usd": route.MinAmountUSD,
			"max_amount_usd": route.MaxAmountUSD,
			"metadata":       route.Metadata,
		})
		included[route.Method] = true
	}
	if !included["stripe"] && stripeSettings.secretKeyConfigured() {
		items = append(items, map[string]any{
			"method":         "stripe",
			"display_name":   "Stripe",
			"min_amount_usd": "0",
			"max_amount_usd": "0",
			"metadata":       map[string]any{"source": "legacy_stripe_settings"},
		})
	}
	return items, nil
}

func hasProviderForRoute(route paymentMethodRoute, providers []paymentProviderInstance) bool {
	for _, provider := range providers {
		if providerSupportsMethod(provider, route.Method) && routeAllowsProvider(route, provider.ProviderType) {
			return true
		}
	}
	return false
}

func (s *Server) createUnifiedCheckout(ctx context.Context, order orderRecord, method string) (paymentCheckoutResult, error) {
	method, err := defaultedEnum(method, "stripe", "stripe", "alipay", "wechat")
	if err != nil {
		return paymentCheckoutResult{}, err
	}
	if method == "stripe" {
		providerID := ""
		if provider, err := s.firstActivePaymentProvider(ctx, "stripe", "stripe"); err == nil {
			providerID = provider.ID
		}
		session, err := s.createStripeCheckoutSession(ctx, order)
		if err != nil {
			return paymentCheckoutResult{}, err
		}
		return paymentCheckoutResult{
			Provider:           "stripe",
			Method:             "stripe",
			ProviderInstanceID: providerID,
			UpstreamTradeNo:    session.ID,
			CheckoutURL:        session.URL,
			PayCurrency:        "USD",
			FXRate:             "1",
			RawResponse:        map[string]any{"id": session.ID, "url": session.URL},
			ExpiresAt:          time.Now().UTC().Add(24 * time.Hour),
		}, nil
	}
	settings, err := s.paymentSettings(ctx)
	if err != nil {
		return paymentCheckoutResult{}, err
	}
	if !settings.Enabled {
		return paymentCheckoutResult{}, appError{status: http.StatusServiceUnavailable, code: "payments_disabled", message: "Payment system is disabled.", typ: "billing_error"}
	}
	if err := s.enforcePaymentPendingOrderLimit(ctx, order.UserID, settings.MaxPendingOrdersPerUser); err != nil {
		return paymentCheckoutResult{}, err
	}
	route, err := s.paymentRoute(ctx, method)
	if err != nil {
		return paymentCheckoutResult{}, err
	}
	if !route.Enabled {
		return paymentCheckoutResult{}, appError{status: http.StatusServiceUnavailable, code: "payment_method_disabled", message: "Payment method is disabled.", typ: "billing_error"}
	}
	if !amountWithinRange(order.AmountUSD, route.MinAmountUSD, route.MaxAmountUSD) {
		return paymentCheckoutResult{}, badRequest("Order amount is outside payment method limits.")
	}
	provider, err := s.selectPaymentProvider(ctx, method, route, order.AmountUSD, settings.ProviderSelection)
	if err != nil {
		return paymentCheckoutResult{}, err
	}
	expiresAt := time.Now().UTC().Add(time.Duration(settings.OrderTimeoutMinutes) * time.Minute)
	payAmountCents := cnyCentsFromUSD(order.AmountUSD, settings.FXUSDCNY)
	switch provider.ProviderType {
	case "easypay":
		return s.createEasyPayCheckout(ctx, provider, order, method, settings.FXUSDCNY, payAmountCents, expiresAt)
	case "alipay":
		return s.createAlipayCheckout(ctx, provider, order, settings.FXUSDCNY, payAmountCents, expiresAt)
	case "wechat":
		return s.createWeChatCheckout(ctx, provider, order, settings.FXUSDCNY, payAmountCents, expiresAt)
	default:
		return paymentCheckoutResult{}, badRequest("Selected provider does not support this method.")
	}
}

func (s *Server) firstActivePaymentProvider(ctx context.Context, providerType string, method string) (paymentProviderInstance, error) {
	providers, err := s.listPaymentProviders(ctx, true)
	if err != nil {
		return paymentProviderInstance{}, err
	}
	for _, provider := range providers {
		if provider.ProviderType == providerType && providerSupportsMethod(provider, method) {
			return provider, nil
		}
	}
	return paymentProviderInstance{}, notFound("Payment provider was not found.")
}

func (s *Server) paymentRoute(ctx context.Context, method string) (paymentMethodRoute, error) {
	var route paymentMethodRoute
	err := s.db.QueryRowContext(ctx, `
		SELECT method, enabled, display_name, provider_types::text, min_amount_usd::text, max_amount_usd::text, metadata_json::text
		FROM payment_method_routes
		WHERE method = $1
	`, method).Scan(&route.Method, &route.Enabled, &route.DisplayName, (*pgTextArray)(&route.ProviderTypes), &route.MinAmountUSD, &route.MaxAmountUSD, (*jsonMap)(&route.Metadata))
	if errors.Is(err, sql.ErrNoRows) {
		return paymentMethodRoute{}, notFound("Payment method was not found.")
	}
	return route, err
}

func (s *Server) selectPaymentProvider(ctx context.Context, method string, route paymentMethodRoute, amountUSD string, selection string) (paymentProviderInstance, error) {
	providers, err := s.listPaymentProviders(ctx, true)
	if err != nil {
		return paymentProviderInstance{}, err
	}
	candidates := []paymentProviderInstance{}
	for _, provider := range providers {
		if !providerSupportsMethod(provider, method) || !routeAllowsProvider(route, provider.ProviderType) || !amountWithinRange(amountUSD, provider.MinAmountUSD, provider.MaxAmountUSD) {
			continue
		}
		if provider.DailyLimitUSD != "" && parsePositiveFloat(provider.DailyLimitUSD, 0) > 0 {
			used, err := s.providerDailyPaidAmount(ctx, provider.ID)
			if err != nil {
				return paymentProviderInstance{}, err
			}
			if used+parsePositiveFloat(amountUSD, 0) > parsePositiveFloat(provider.DailyLimitUSD, 0) {
				continue
			}
		}
		candidates = append(candidates, provider)
	}
	if len(candidates) == 0 {
		return paymentProviderInstance{}, appError{status: http.StatusServiceUnavailable, code: "payment_provider_unavailable", message: "No payment provider is available for this method.", typ: "billing_error"}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority == candidates[j].Priority {
			return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
		}
		return candidates[i].Priority < candidates[j].Priority
	})
	if selection != "weighted" {
		return candidates[0], nil
	}
	total := 0
	for _, candidate := range candidates {
		total += candidate.Weight
	}
	if total <= 0 {
		return candidates[0], nil
	}
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	pick := int(b[0])<<8 + int(b[1])
	pick = pick % total
	for _, candidate := range candidates {
		if pick < candidate.Weight {
			return candidate, nil
		}
		pick -= candidate.Weight
	}
	return candidates[0], nil
}

func (s *Server) providerDailyPaidAmount(ctx context.Context, providerID string) (float64, error) {
	var total string
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount_usd), 0)::text
		FROM orders
		WHERE provider_instance_id = $1
		  AND status = 'paid'
		  AND paid_at >= date_trunc('day', now())
	`, providerID).Scan(&total)
	if err != nil {
		return 0, err
	}
	return parsePositiveFloat(total, 0), nil
}

func (s *Server) enforcePaymentPendingOrderLimit(ctx context.Context, userID string, maxPending int) error {
	if maxPending <= 0 {
		return nil
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders WHERE user_id = $1 AND status = 'pending'", userID).Scan(&count); err != nil {
		return err
	}
	if count > maxPending {
		return conflict("Too many pending payment orders.")
	}
	return nil
}

func (s *Server) createEasyPayCheckout(ctx context.Context, provider paymentProviderInstance, order orderRecord, method string, fxRate string, payAmountCents int, expiresAt time.Time) (paymentCheckoutResult, error) {
	apiBase := strings.TrimRight(stringValue(provider.Config["api_base_url"], ""), "/")
	if apiBase == "" {
		return paymentCheckoutResult{}, badRequest("EasyPay api_base_url is not configured.")
	}
	pid := strings.TrimSpace(s.paymentSecret(provider, "pid"))
	key := strings.TrimSpace(s.paymentSecret(provider, "key"))
	if pid == "" || key == "" {
		return paymentCheckoutResult{}, appError{status: http.StatusServiceUnavailable, code: "easypay_not_configured", message: "EasyPay pid/key is not configured.", typ: "billing_error"}
	}
	tradeNo := order.ID
	params := url.Values{}
	params.Set("pid", pid)
	params.Set("type", easyPayType(method))
	params.Set("out_trade_no", tradeNo)
	params.Set("notify_url", s.providerWebhookURL(provider))
	params.Set("return_url", strings.TrimRight(s.cfg.PortalBaseURL, "/")+"/?billing=success&order_id="+url.QueryEscape(order.ID))
	params.Set("name", paymentOrderName(order))
	params.Set("money", centsToDecimal(payAmountCents))
	params.Set("sign_type", "MD5")
	params.Set("sign", easyPaySign(params, key))
	endpoint := apiBase + "/submit.php?" + params.Encode()
	return paymentCheckoutResult{
		Provider:           "easypay",
		Method:             method,
		ProviderInstanceID: provider.ID,
		UpstreamTradeNo:    tradeNo,
		CheckoutURL:        endpoint,
		QRCodeURL:          endpoint,
		PayCurrency:        "CNY",
		PayAmountCents:     payAmountCents,
		FXRate:             fxRate,
		RawResponse:        map[string]any{"url": endpoint},
		ExpiresAt:          expiresAt,
	}, nil
}

func (s *Server) createAlipayCheckout(ctx context.Context, provider paymentProviderInstance, order orderRecord, fxRate string, payAmountCents int, expiresAt time.Time) (paymentCheckoutResult, error) {
	appID := strings.TrimSpace(stringValue(provider.Config["app_id"], ""))
	gateway := defaultString(stringValue(provider.Config["gateway_url"], ""), "https://openapi.alipay.com/gateway.do")
	privateKey := s.paymentSecret(provider, "private_key")
	if appID == "" || privateKey == "" {
		return paymentCheckoutResult{}, appError{status: http.StatusServiceUnavailable, code: "alipay_not_configured", message: "Alipay app_id/private_key is not configured.", typ: "billing_error"}
	}
	tradeNo := order.ID
	bizContent, _ := encodeJSON(map[string]any{
		"out_trade_no":    tradeNo,
		"total_amount":    centsToDecimal(payAmountCents),
		"subject":         paymentOrderName(order),
		"timeout_express": strconv.Itoa(int(time.Until(expiresAt).Minutes())) + "m",
	})
	params := url.Values{}
	params.Set("app_id", appID)
	params.Set("method", "alipay.trade.precreate")
	params.Set("charset", "utf-8")
	params.Set("sign_type", "RSA2")
	params.Set("timestamp", time.Now().Format("2006-01-02 15:04:05"))
	params.Set("version", "1.0")
	params.Set("notify_url", s.providerWebhookURL(provider))
	params.Set("biz_content", bizContent)
	signature, err := alipaySign(params, privateKey)
	if err != nil {
		return paymentCheckoutResult{}, err
	}
	params.Set("sign", signature)
	body, err := postForm(ctx, s.httpClient, gateway, params, nil)
	if err != nil {
		return paymentCheckoutResult{}, err
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	response := mapValue(raw["alipay_trade_precreate_response"])
	if stringValue(response["code"], "") != "10000" {
		return paymentCheckoutResult{}, appError{status: http.StatusBadGateway, code: "alipay_checkout_failed", message: truncateForStorage(string(body), 500), typ: "billing_error"}
	}
	qr := stringValue(response["qr_code"], "")
	if qr == "" {
		return paymentCheckoutResult{}, upstreamUnavailable("alipay_checkout_invalid", "Alipay checkout response did not include qr_code.")
	}
	return paymentCheckoutResult{
		Provider:           "alipay",
		Method:             "alipay",
		ProviderInstanceID: provider.ID,
		UpstreamTradeNo:    tradeNo,
		CheckoutURL:        qr,
		QRCodeURL:          qr,
		PayCurrency:        "CNY",
		PayAmountCents:     payAmountCents,
		FXRate:             fxRate,
		RawResponse:        raw,
		ExpiresAt:          expiresAt,
	}, nil
}

func (s *Server) createWeChatCheckout(ctx context.Context, provider paymentProviderInstance, order orderRecord, fxRate string, payAmountCents int, expiresAt time.Time) (paymentCheckoutResult, error) {
	appID := strings.TrimSpace(stringValue(provider.Config["appid"], ""))
	mchID := strings.TrimSpace(stringValue(provider.Config["mchid"], ""))
	serialNo := strings.TrimSpace(stringValue(provider.Config["serial_no"], ""))
	privateKey := s.paymentSecret(provider, "private_key")
	if appID == "" || mchID == "" || serialNo == "" || privateKey == "" {
		return paymentCheckoutResult{}, appError{status: http.StatusServiceUnavailable, code: "wechat_not_configured", message: "WeChat appid/mchid/serial_no/private_key is not configured.", typ: "billing_error"}
	}
	endpoint := defaultString(stringValue(provider.Config["native_url"], ""), "https://api.mch.weixin.qq.com/v3/pay/transactions/native")
	payload := map[string]any{
		"appid":        appID,
		"mchid":        mchID,
		"description":  paymentOrderName(order),
		"out_trade_no": order.ID,
		"notify_url":   s.providerWebhookURL(provider),
		"amount":       map[string]any{"total": payAmountCents, "currency": "CNY"},
		"time_expire":  expiresAt.Format(time.RFC3339),
	}
	body, _ := json.Marshal(payload)
	respBody, err := wechatPostJSON(ctx, s.httpClient, endpoint, body, mchID, serialNo, privateKey)
	if err != nil {
		return paymentCheckoutResult{}, err
	}
	var raw map[string]any
	_ = json.Unmarshal(respBody, &raw)
	codeURL := stringValue(raw["code_url"], "")
	if codeURL == "" {
		return paymentCheckoutResult{}, appError{status: http.StatusBadGateway, code: "wechat_checkout_failed", message: truncateForStorage(string(respBody), 500), typ: "billing_error"}
	}
	return paymentCheckoutResult{
		Provider:           "wechat",
		Method:             "wechat",
		ProviderInstanceID: provider.ID,
		UpstreamTradeNo:    order.ID,
		CheckoutURL:        codeURL,
		QRCodeURL:          codeURL,
		PayCurrency:        "CNY",
		PayAmountCents:     payAmountCents,
		FXRate:             fxRate,
		RawResponse:        raw,
		ExpiresAt:          expiresAt,
	}, nil
}

func (s *Server) updateOrderAfterCheckout(ctx context.Context, order orderRecord, result paymentCheckoutResult) error {
	if result.Provider == "stripe" {
		_, err := s.db.ExecContext(ctx, `
			UPDATE orders
			SET stripe_checkout_session_id = $2,
			    checkout_url = $3,
			    payment_provider = 'stripe',
			    payment_method = 'stripe',
			    upstream_trade_no = $2,
			    pay_currency = COALESCE(NULLIF($4, ''), 'USD'),
			    expires_at = $5
			WHERE id = $1
		`, order.ID, result.UpstreamTradeNo, result.CheckoutURL, result.PayCurrency, result.ExpiresAt)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE orders
		SET payment_provider = $2,
		    payment_method = $3,
		    provider_instance_id = NULLIF($4, '')::uuid,
		    pay_currency = $5,
		    pay_amount_cents = $6,
		    fx_rate = $7::numeric,
		    upstream_trade_no = $8,
		    upstream_transaction_id = $9,
		    checkout_url = $10,
		    expires_at = $11
		WHERE id = $1
	`, order.ID, result.Provider, result.Method, result.ProviderInstanceID, result.PayCurrency, result.PayAmountCents, result.FXRate, result.UpstreamTradeNo, result.UpstreamTransactionID, result.CheckoutURL, result.ExpiresAt)
	if err != nil {
		return err
	}
	raw, _ := encodeJSON(result.RawResponse)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO payment_order_attempts (order_id, provider_instance_id, provider_type, method, status, upstream_trade_no, upstream_transaction_id, pay_currency, pay_amount_cents, checkout_url, qr_code_url, raw_response_json)
		VALUES ($1, NULLIF($2, '')::uuid, $3, $4, 'pending', $5, $6, $7, $8, $9, $10, $11::jsonb)
	`, order.ID, result.ProviderInstanceID, result.Provider, result.Method, result.UpstreamTradeNo, result.UpstreamTransactionID, result.PayCurrency, result.PayAmountCents, result.CheckoutURL, result.QRCodeURL, raw)
	return err
}

func (s *Server) decodeUnifiedPaymentWebhook(ctx context.Context, provider paymentProviderInstance, r *http.Request, body []byte) (unifiedPaymentEvent, error) {
	switch provider.ProviderType {
	case "easypay":
		return s.decodeEasyPayWebhook(provider, body)
	case "alipay":
		return s.decodeAlipayWebhook(provider, body)
	case "wechat":
		return s.decodeWeChatWebhook(provider, r, body)
	default:
		return unifiedPaymentEvent{}, badRequest("Unsupported payment provider.")
	}
}

func (s *Server) decodeEasyPayWebhook(provider paymentProviderInstance, body []byte) (unifiedPaymentEvent, error) {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return unifiedPaymentEvent{}, badRequest("Invalid EasyPay webhook payload.")
	}
	key := s.paymentSecret(provider, "key")
	if key == "" || !verifyEasyPaySign(values, key) {
		return unifiedPaymentEvent{}, unauthorized("Invalid EasyPay signature.")
	}
	outTradeNo := strings.TrimSpace(values.Get("out_trade_no"))
	tradeStatus := strings.TrimSpace(values.Get("trade_status"))
	if tradeStatus != "TRADE_SUCCESS" && tradeStatus != "TRADE_FINISHED" {
		return unifiedPaymentEvent{}, badRequest("EasyPay trade is not successful.")
	}
	return unifiedPaymentEvent{
		ID:                    "easypay:" + values.Get("trade_no"),
		Provider:              "easypay",
		Method:                paymentMethodFromEasyPay(values.Get("type")),
		OrderID:               outTradeNo,
		EventType:             "payment.succeeded",
		UpstreamTradeNo:       outTradeNo,
		UpstreamTransactionID: values.Get("trade_no"),
		Payload:               []byte(jsonFromValues(values)),
		Object:                mapFromValues(values),
	}, nil
}

func (s *Server) decodeAlipayWebhook(provider paymentProviderInstance, body []byte) (unifiedPaymentEvent, error) {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return unifiedPaymentEvent{}, badRequest("Invalid Alipay webhook payload.")
	}
	publicKey := s.paymentSecret(provider, "alipay_public_key")
	if publicKey == "" {
		return unifiedPaymentEvent{}, appError{status: http.StatusServiceUnavailable, code: "alipay_not_configured", message: "Alipay public key is not configured.", typ: "billing_error"}
	}
	if !verifyAlipaySign(values, publicKey) {
		return unifiedPaymentEvent{}, unauthorized("Invalid Alipay signature.")
	}
	status := values.Get("trade_status")
	if status != "TRADE_SUCCESS" && status != "TRADE_FINISHED" {
		return unifiedPaymentEvent{}, badRequest("Alipay trade is not successful.")
	}
	outTradeNo := strings.TrimSpace(values.Get("out_trade_no"))
	return unifiedPaymentEvent{
		ID:                    "alipay:" + values.Get("trade_no"),
		Provider:              "alipay",
		Method:                "alipay",
		OrderID:               outTradeNo,
		EventType:             "payment.succeeded",
		UpstreamTradeNo:       outTradeNo,
		UpstreamTransactionID: values.Get("trade_no"),
		Payload:               []byte(jsonFromValues(values)),
		Object:                mapFromValues(values),
	}, nil
}

func (s *Server) decodeWeChatWebhook(provider paymentProviderInstance, r *http.Request, body []byte) (unifiedPaymentEvent, error) {
	apiV3Key := s.paymentSecret(provider, "api_v3_key")
	publicKey := s.paymentSecret(provider, "wechatpay_public_key")
	if publicKey == "" {
		return unifiedPaymentEvent{}, appError{status: http.StatusServiceUnavailable, code: "wechat_not_configured", message: "WeChat Pay public key is not configured.", typ: "billing_error"}
	}
	if !verifyWeChatSignature(r, body, publicKey) {
		return unifiedPaymentEvent{}, unauthorized("Invalid WeChat signature.")
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return unifiedPaymentEvent{}, badRequest("Invalid WeChat webhook payload.")
	}
	object := raw
	if resource, ok := raw["resource"].(map[string]any); ok {
		plaintext, err := decryptWeChatResource(resource, apiV3Key)
		if err != nil {
			return unifiedPaymentEvent{}, err
		}
		if err := json.Unmarshal(plaintext, &object); err != nil {
			return unifiedPaymentEvent{}, badRequest("Invalid WeChat resource payload.")
		}
	}
	if stringValue(object["trade_state"], "") != "SUCCESS" {
		return unifiedPaymentEvent{}, badRequest("WeChat trade is not successful.")
	}
	outTradeNo := stringValue(object["out_trade_no"], "")
	return unifiedPaymentEvent{
		ID:                    "wechat:" + stringValue(object["transaction_id"], ""),
		Provider:              "wechat",
		Method:                "wechat",
		OrderID:               outTradeNo,
		EventType:             "payment.succeeded",
		UpstreamTradeNo:       outTradeNo,
		UpstreamTransactionID: stringValue(object["transaction_id"], ""),
		Payload:               body,
		Object:                object,
	}, nil
}

func (s *Server) handleUnifiedPaymentEvent(ctx context.Context, event unifiedPaymentEvent) error {
	if event.EventType != "payment.succeeded" || event.OrderID == "" {
		return nil
	}
	order, err := s.getOrder(ctx, event.OrderID, "")
	if err != nil {
		return err
	}
	if err := verifyUnifiedPaymentAmount(order, event); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE orders
		SET upstream_transaction_id = COALESCE(NULLIF($2, ''), upstream_transaction_id)
		WHERE id = $1
	`, event.OrderID, event.UpstreamTransactionID); err != nil {
		return err
	}
	return s.markOrderPaid(ctx, event.OrderID, "", event.UpstreamTransactionID, "")
}

func verifyUnifiedPaymentAmount(order orderRecord, event unifiedPaymentEvent) error {
	if order.PayAmountCents <= 0 || order.PayCurrency != "CNY" {
		return nil
	}
	switch event.Provider {
	case "easypay":
		got := decimalToCents(stringValue(event.Object["money"], ""))
		if got != order.PayAmountCents {
			return badRequest("EasyPay amount does not match order.")
		}
	case "alipay":
		got := decimalToCents(stringValue(event.Object["total_amount"], ""))
		if got != order.PayAmountCents {
			return badRequest("Alipay amount does not match order.")
		}
	case "wechat":
		if amount, ok := event.Object["amount"].(map[string]any); ok {
			got := intValue(amount["total"], 0)
			if got != order.PayAmountCents {
				return badRequest("WeChat amount does not match order.")
			}
		}
	}
	return nil
}

func (s *Server) lookupUnifiedPaymentOrderID(ctx context.Context, provider string, tradeNo string) string {
	var orderID string
	_ = s.db.QueryRowContext(ctx, `
		SELECT id::text
		FROM orders
		WHERE payment_provider = $1 AND upstream_trade_no = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, provider, tradeNo).Scan(&orderID)
	return orderID
}

func (s *Server) insertProviderPaymentEvent(ctx context.Context, orderID string, provider string, eventID string, eventType string, payload []byte) (string, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO payment_events (order_id, provider, provider_event_id, event_type, payload_json)
		VALUES (NULLIF($1, '')::uuid, $2, $3, $4, $5::jsonb)
		ON CONFLICT DO NOTHING
		RETURNING id::text
	`, orderID, provider, eventID, eventType, jsonPayload(payload)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, id != "", err
}

func (s *Server) testPaymentProvider(ctx context.Context, provider paymentProviderInstance) (map[string]any, error) {
	switch provider.ProviderType {
	case "stripe":
		secret := s.paymentSecret(provider, "secret_key")
		if secret == "" {
			return nil, appError{status: http.StatusServiceUnavailable, code: "stripe_not_configured", message: "Stripe provider secret_key is not configured.", typ: "billing_error"}
		}
		if secret == "" {
			return nil, appError{status: http.StatusServiceUnavailable, code: "stripe_not_configured", message: "Stripe secret key is not configured.", typ: "billing_error"}
		}
		account, err := s.testStripeAccount(ctx, secret)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "provider_type": "stripe", "account_id": account.ID, "mode": stripeKeyMode(secret)}, nil
	case "easypay":
		if s.paymentSecret(provider, "pid") == "" || s.paymentSecret(provider, "key") == "" || stringValue(provider.Config["api_base_url"], "") == "" {
			return nil, appError{status: http.StatusServiceUnavailable, code: "easypay_not_configured", message: "EasyPay pid/key/api_base_url is not configured.", typ: "billing_error"}
		}
		return map[string]any{"ok": true, "provider_type": "easypay"}, nil
	case "alipay":
		if stringValue(provider.Config["app_id"], "") == "" || s.paymentSecret(provider, "private_key") == "" {
			return nil, appError{status: http.StatusServiceUnavailable, code: "alipay_not_configured", message: "Alipay app_id/private_key is not configured.", typ: "billing_error"}
		}
		return map[string]any{"ok": true, "provider_type": "alipay"}, nil
	case "wechat":
		if stringValue(provider.Config["appid"], "") == "" || stringValue(provider.Config["mchid"], "") == "" || s.paymentSecret(provider, "private_key") == "" {
			return nil, appError{status: http.StatusServiceUnavailable, code: "wechat_not_configured", message: "WeChat appid/mchid/private_key is not configured.", typ: "billing_error"}
		}
		return map[string]any{"ok": true, "provider_type": "wechat"}, nil
	default:
		return nil, badRequest("Unsupported provider type.")
	}
}

func (s *Server) startPaymentReconciler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.reconcilePendingPayments(ctx)
			}
		}
	}()
}

func (s *Server) reconcilePendingPayments(ctx context.Context) error {
	settings, err := s.paymentSettings(ctx)
	if err != nil || !settings.AutoReconcileEnabled {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, payment_provider, COALESCE(provider_instance_id::text, ''), upstream_trade_no, expires_at
		FROM orders
		WHERE status = 'pending'
		  AND payment_provider IN ('easypay', 'alipay', 'wechat')
		  AND upstream_trade_no <> ''
		ORDER BY created_at ASC
		LIMIT 100
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pendingOrder struct {
		ID         string
		Provider   string
		ProviderID string
		TradeNo    string
		ExpiresAt  sql.NullTime
	}
	orders := []pendingOrder{}
	for rows.Next() {
		var item pendingOrder
		if err := rows.Scan(&item.ID, &item.Provider, &item.ProviderID, &item.TradeNo, &item.ExpiresAt); err != nil {
			return err
		}
		orders = append(orders, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, order := range orders {
		if order.ProviderID == "" {
			continue
		}
		provider, err := s.getPaymentProvider(ctx, order.ProviderID)
		if err != nil {
			continue
		}
		paid, transactionID, err := s.queryProviderPayment(ctx, provider, order.TradeNo)
		if err == nil && paid {
			if _, err := s.db.ExecContext(ctx, "UPDATE orders SET upstream_transaction_id = COALESCE(NULLIF($2, ''), upstream_transaction_id) WHERE id = $1", order.ID, transactionID); err != nil {
				continue
			}
			_ = s.markOrderPaid(ctx, order.ID, "", transactionID, "")
			continue
		}
		if order.ExpiresAt.Valid && time.Now().After(order.ExpiresAt.Time) {
			if err == nil && !paid {
				_, _ = s.db.ExecContext(ctx, "UPDATE orders SET status = 'canceled' WHERE id = $1 AND status = 'pending'", order.ID)
			}
		}
	}
	return nil
}

func (s *Server) queryProviderPayment(ctx context.Context, provider paymentProviderInstance, tradeNo string) (bool, string, error) {
	switch provider.ProviderType {
	case "easypay":
		return s.queryEasyPayPayment(ctx, provider, tradeNo)
	case "alipay":
		return s.queryAlipayPayment(ctx, provider, tradeNo)
	case "wechat":
		return s.queryWeChatPayment(ctx, provider, tradeNo)
	default:
		return false, "", badRequest("Unsupported payment provider.")
	}
}

func (s *Server) queryEasyPayPayment(ctx context.Context, provider paymentProviderInstance, tradeNo string) (bool, string, error) {
	apiBase := strings.TrimRight(stringValue(provider.Config["api_base_url"], ""), "/")
	pid := s.paymentSecret(provider, "pid")
	key := s.paymentSecret(provider, "key")
	if apiBase == "" || pid == "" || key == "" {
		return false, "", badRequest("EasyPay is not configured.")
	}
	params := url.Values{}
	params.Set("act", "order")
	params.Set("pid", pid)
	params.Set("out_trade_no", tradeNo)
	params.Set("sign_type", "MD5")
	params.Set("sign", easyPaySign(params, key))
	body, err := postForm(ctx, s.httpClient, apiBase+"/api.php", params, nil)
	if err != nil {
		return false, "", err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return false, "", err
	}
	status := stringValue(raw["status"], "")
	tradeStatus := stringValue(raw["trade_status"], "")
	return status == "1" || tradeStatus == "TRADE_SUCCESS" || tradeStatus == "TRADE_FINISHED", stringValue(raw["trade_no"], ""), nil
}

func (s *Server) queryAlipayPayment(ctx context.Context, provider paymentProviderInstance, tradeNo string) (bool, string, error) {
	appID := stringValue(provider.Config["app_id"], "")
	gateway := defaultString(stringValue(provider.Config["gateway_url"], ""), "https://openapi.alipay.com/gateway.do")
	privateKey := s.paymentSecret(provider, "private_key")
	if appID == "" || privateKey == "" {
		return false, "", badRequest("Alipay is not configured.")
	}
	bizContent, _ := encodeJSON(map[string]any{"out_trade_no": tradeNo})
	params := url.Values{}
	params.Set("app_id", appID)
	params.Set("method", "alipay.trade.query")
	params.Set("charset", "utf-8")
	params.Set("sign_type", "RSA2")
	params.Set("timestamp", time.Now().Format("2006-01-02 15:04:05"))
	params.Set("version", "1.0")
	params.Set("biz_content", bizContent)
	signature, err := alipaySign(params, privateKey)
	if err != nil {
		return false, "", err
	}
	params.Set("sign", signature)
	body, err := postForm(ctx, s.httpClient, gateway, params, nil)
	if err != nil {
		return false, "", err
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	response := mapValue(raw["alipay_trade_query_response"])
	status := stringValue(response["trade_status"], "")
	return status == "TRADE_SUCCESS" || status == "TRADE_FINISHED", stringValue(response["trade_no"], ""), nil
}

func (s *Server) queryWeChatPayment(ctx context.Context, provider paymentProviderInstance, tradeNo string) (bool, string, error) {
	mchID := stringValue(provider.Config["mchid"], "")
	serialNo := stringValue(provider.Config["serial_no"], "")
	privateKey := s.paymentSecret(provider, "private_key")
	if mchID == "" || serialNo == "" || privateKey == "" {
		return false, "", badRequest("WeChat Pay is not configured.")
	}
	endpoint := defaultString(stringValue(provider.Config["query_url"], ""), "https://api.mch.weixin.qq.com/v3/pay/transactions/out-trade-no/"+url.PathEscape(tradeNo)+"?mchid="+url.QueryEscape(mchID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", wechatAuthorization(http.MethodGet, req.URL.RequestURI(), nil, mchID, serialNo, privateKey))
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, "", upstreamUnavailable("wechat_unavailable", "WeChat request failed.")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, "", appError{status: http.StatusBadGateway, code: "wechat_request_failed", message: truncateForStorage(string(body), 500), typ: "billing_error"}
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	return stringValue(raw["trade_state"], "") == "SUCCESS", stringValue(raw["transaction_id"], ""), nil
}

func postForm(ctx context.Context, client *http.Client, endpoint string, form url.Values, header http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, upstreamUnavailable("payment_provider_unavailable", "Payment provider request failed.")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, appError{status: http.StatusBadGateway, code: "payment_provider_failed", message: truncateForStorage(string(body), 500), typ: "billing_error"}
	}
	return body, nil
}

func wechatPostJSON(ctx context.Context, client *http.Client, endpoint string, body []byte, mchID string, serialNo string, privateKey string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", wechatAuthorization(http.MethodPost, req.URL.RequestURI(), body, mchID, serialNo, privateKey))
	resp, err := client.Do(req)
	if err != nil {
		return nil, upstreamUnavailable("wechat_unavailable", "WeChat request failed.")
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, appError{status: http.StatusBadGateway, code: "wechat_request_failed", message: truncateForStorage(string(respBody), 500), typ: "billing_error"}
	}
	return respBody, nil
}

func alipaySign(values url.Values, privateKeyPEM string) (string, error) {
	key, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(canonicalForm(values, false)))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func verifyAlipaySign(values url.Values, publicKeyPEM string) bool {
	signatureText := values.Get("sign")
	if signatureText == "" {
		return false
	}
	publicKey, err := parseRSAPublicKey(publicKeyPEM)
	if err != nil {
		return false
	}
	signature, err := base64.StdEncoding.DecodeString(signatureText)
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(canonicalForm(values, true)))
	return rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature) == nil
}

func easyPaySign(values url.Values, key string) string {
	base := canonicalForm(values, true)
	sum := md5Hex(base + key)
	return sum
}

func verifyEasyPaySign(values url.Values, key string) bool {
	got := values.Get("sign")
	if got == "" {
		return false
	}
	expected := easyPaySign(values, key)
	return strings.EqualFold(got, expected)
}

func wechatAuthorization(method string, path string, body []byte, mchID string, serialNo string, privateKeyPEM string) string {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := randomHex(16)
	message := method + "\n" + path + "\n" + timestamp + "\n" + nonce + "\n" + string(body) + "\n"
	signature := rsaSHA256Base64(message, privateKeyPEM)
	return fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",timestamp="%s",serial_no="%s",signature="%s"`, mchID, nonce, timestamp, serialNo, signature)
}

func verifyWeChatSignature(r *http.Request, body []byte, publicKeyPEM string) bool {
	timestamp := r.Header.Get("Wechatpay-Timestamp")
	nonce := r.Header.Get("Wechatpay-Nonce")
	signatureText := r.Header.Get("Wechatpay-Signature")
	if timestamp == "" || nonce == "" || signatureText == "" {
		return false
	}
	publicKey, err := parseRSAPublicKey(publicKeyPEM)
	if err != nil {
		return false
	}
	signature, err := base64.StdEncoding.DecodeString(signatureText)
	if err != nil {
		return false
	}
	message := timestamp + "\n" + nonce + "\n" + string(body) + "\n"
	digest := sha256.Sum256([]byte(message))
	return rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature) == nil
}

func decryptWeChatResource(resource map[string]any, apiV3Key string) ([]byte, error) {
	if apiV3Key == "" {
		return nil, badRequest("WeChat api_v3_key is required to decrypt webhook resource.")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(stringValue(resource["ciphertext"], ""))
	if err != nil {
		return nil, badRequest("Invalid WeChat ciphertext.")
	}
	nonce := []byte(stringValue(resource["nonce"], ""))
	associatedData := []byte(stringValue(resource["associated_data"], ""))
	block, err := aesGCMBlock([]byte(apiV3Key))
	if err != nil {
		return nil, err
	}
	plaintext, err := block.Open(nil, nonce, ciphertext, associatedData)
	if err != nil {
		return nil, unauthorized("Invalid WeChat resource ciphertext.")
	}
	return plaintext, nil
}

func aesGCMBlock(key []byte) (cipherAEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func parseRSAPrivateKey(raw string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, badRequest("Invalid RSA private key.")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, badRequest("Invalid RSA private key.")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, badRequest("Invalid RSA private key.")
	}
	return key, nil
}

func parseRSAPublicKey(raw string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, badRequest("Invalid RSA public key.")
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if publicKey, ok := key.(*rsa.PublicKey); ok {
			return publicKey, nil
		}
	}
	if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
		if publicKey, ok := cert.PublicKey.(*rsa.PublicKey); ok {
			return publicKey, nil
		}
	}
	return nil, badRequest("Invalid RSA public key.")
}

func rsaSHA256Base64(message string, privateKeyPEM string) string {
	key, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(signature)
}

func canonicalForm(values url.Values, skipSign bool) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if skipSign && (key == "sign" || key == "sign_type") {
			continue
		}
		if len(values[key]) == 0 || values[key][0] == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	return strings.Join(parts, "&")
}

func md5Hex(value string) string {
	h := md5.New()
	_, _ = h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}

func randomHex(size int) string {
	b := make([]byte, size)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func cnyCentsFromUSD(amountUSD string, fxRate string) int {
	amount := parsePositiveFloat(amountUSD, 0)
	rate := parsePositiveFloat(fxRate, parsePositiveFloat(defaultFXRate, 7.2))
	return int(math.Ceil(amount * rate * 100))
}

func centsToDecimal(cents int) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func decimalToCents(value string) int {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed < 0 {
		return 0
	}
	return int(math.Round(parsed * 100))
}

func amountWithinRange(amount string, min string, max string) bool {
	value := parsePositiveFloat(amount, 0)
	if parsePositiveFloat(min, 0) > 0 && value < parsePositiveFloat(min, 0) {
		return false
	}
	if parsePositiveFloat(max, 0) > 0 && value > parsePositiveFloat(max, 0) {
		return false
	}
	return true
}

func providerSupportsMethod(provider paymentProviderInstance, method string) bool {
	for _, item := range provider.SupportedMethods {
		if item == method {
			return true
		}
	}
	return false
}

func routeAllowsProvider(route paymentMethodRoute, providerType string) bool {
	if len(route.ProviderTypes) == 0 {
		return true
	}
	for _, item := range route.ProviderTypes {
		if item == providerType {
			return true
		}
	}
	return false
}

func normalizePaymentMethods(providerType string, methods []string) []string {
	if len(methods) == 0 {
		switch providerType {
		case "stripe":
			return []string{"stripe"}
		case "alipay":
			return []string{"alipay"}
		case "wechat":
			return []string{"wechat"}
		case "easypay":
			return []string{"alipay", "wechat"}
		}
	}
	result := []string{}
	for _, method := range methods {
		switch strings.TrimSpace(method) {
		case "stripe", "alipay", "wechat":
			result = append(result, strings.TrimSpace(method))
		}
	}
	return normalizeStringList(result)
}

func defaultPaymentDisplayName(method string, fallback string) string {
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	switch method {
	case "stripe":
		return "Stripe"
	case "alipay":
		return "支付宝"
	case "wechat":
		return "微信支付"
	default:
		return method
	}
}

func paymentOrderName(order orderRecord) string {
	if order.OrderType == "subscription" {
		return "Elucid Relay subscription"
	}
	return "Elucid Relay wallet top-up"
}

func easyPayType(method string) string {
	if method == "wechat" {
		return "wxpay"
	}
	return "alipay"
}

func paymentMethodFromEasyPay(value string) string {
	switch value {
	case "wxpay", "wechat":
		return "wechat"
	default:
		return "alipay"
	}
}

func jsonFromValues(values url.Values) string {
	return mustEncodeJSON(mapFromValues(values))
}

func mapFromValues(values url.Values) map[string]any {
	result := map[string]any{}
	for key := range values {
		result[key] = values.Get(key)
	}
	return result
}

func mapValue(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func stringSliceLiteral(values []string) string {
	escaped := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ReplaceAll(value, `"`, `\"`)
		escaped = append(escaped, `"`+value+`"`)
	}
	return "{" + strings.Join(escaped, ",") + "}"
}

func jsonPayload(payload []byte) string {
	var value any
	if json.Unmarshal(payload, &value) == nil {
		return string(payload)
	}
	return mustEncodeJSON(map[string]any{"raw": string(payload)})
}

func (s *Server) providerWebhookURL(provider paymentProviderInstance) string {
	baseURL := strings.TrimRight(stringValue(provider.Config["public_base_url"], ""), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(s.cfg.PublicBaseURL, "/")
	}
	return baseURL + "/api/billing/v1/payment/webhook/" + provider.ProviderType + "/" + provider.ID
}

type pgTextArray []string

func (a *pgTextArray) Scan(value any) error {
	switch typed := value.(type) {
	case string:
		*a = parsePGTextArray(typed)
	case []byte:
		*a = parsePGTextArray(string(typed))
	default:
		*a = []string{}
	}
	return nil
}

func parsePGTextArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(strings.TrimSuffix(raw, "}"), "{")
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	values := []string{}
	for _, part := range parts {
		values = append(values, strings.Trim(strings.TrimSpace(part), `"`))
	}
	return values
}

type jsonMap map[string]any

func (m *jsonMap) Scan(value any) error {
	var raw []byte
	switch typed := value.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = typed
	default:
		*m = map[string]any{}
		return nil
	}
	result := map[string]any{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	*m = result
	return nil
}

type cipherAEAD interface {
	Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
}
