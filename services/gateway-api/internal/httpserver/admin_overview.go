package httpserver

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request, auth authContext) error {
	metrics, err := s.adminOverviewMetrics(r.Context())
	if err != nil {
		return err
	}
	configStatus, err := s.adminConfigStatus(r.Context())
	if err != nil {
		return err
	}
	checklist := setupChecklistFromStatus(configStatus, metrics)
	writeJSON(w, http.StatusOK, map[string]any{
		"metrics":       metrics,
		"config_status": configStatus,
		"checklist":     checklist,
	}, nil)
	return nil
}

func (s *Server) adminSetupChecklist(w http.ResponseWriter, r *http.Request, auth authContext) error {
	metrics, err := s.adminOverviewMetrics(r.Context())
	if err != nil {
		return err
	}
	configStatus, err := s.adminConfigStatus(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":         setupChecklistFromStatus(configStatus, metrics),
		"config_status": configStatus,
	}, nil)
	return nil
}

func (s *Server) adminTestSetupCheck(w http.ResponseWriter, r *http.Request, auth authContext) error {
	checkID := strings.TrimSpace(r.PathValue("checkId"))
	metrics, err := s.adminOverviewMetrics(r.Context())
	if err != nil {
		return err
	}
	configStatus, err := s.adminConfigStatus(r.Context())
	if err != nil {
		return err
	}
	for _, item := range setupChecklistFromStatus(configStatus, metrics) {
		if item["id"] == checkID {
			audit(r.Context(), s.db, auth.UserID, "admin", "setup_check.test", "setup_check", checkID, r, map[string]any{"complete": item["complete"]})
			writeJSON(w, http.StatusOK, item, nil)
			return nil
		}
	}
	return notFound("Setup check was not found.")
}

func (s *Server) adminOverviewMetrics(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH user_stats AS (
			SELECT
				COUNT(*) AS total_users,
				COUNT(*) FILTER (WHERE created_at > now() - interval '24 hours') AS new_users_24h,
				COUNT(*) FILTER (WHERE status = 'active') AS active_users,
				COUNT(*) FILTER (WHERE status = 'disabled') AS disabled_users,
				COUNT(*) FILTER (WHERE user_type = 'operator') AS operator_users,
				COUNT(*) FILTER (WHERE user_type = 'platform_owner') AS owner_users
			FROM users
		),
		wallet_stats AS (
			SELECT
				COALESCE(SUM(balance), 0)::text AS wallet_balance,
				COALESCE(SUM(reserved_balance), 0)::text AS reserved_balance,
				COUNT(*) FILTER (WHERE status = 'active' AND balance <= reserved_balance) AS low_balance_users
			FROM wallet_accounts
		),
		usage_stats AS (
			SELECT
				COUNT(*) AS requests_24h,
				COUNT(*) FILTER (WHERE status = 'failed') AS failed_requests_24h,
				COUNT(*) FILTER (WHERE status = 'rejected') AS rejected_requests_24h,
				COALESCE(SUM(actual_cost), 0)::text AS cost_24h
			FROM usage_records
			WHERE created_at > now() - interval '24 hours'
		),
		upstream_stats AS (
			SELECT
				(SELECT COUNT(*) FROM providers) AS provider_count,
				(SELECT COUNT(*) FROM channels) AS channel_count,
				(SELECT COUNT(*) FROM channels WHERE status = 'active') AS active_channels,
				(SELECT COUNT(*) FROM accounts) AS account_count,
				(SELECT COUNT(*) FROM accounts WHERE status = 'active') AS active_accounts,
				(SELECT COUNT(*) FROM proxies) AS proxy_count,
				(SELECT COUNT(*) FROM proxies WHERE status = 'active') AS active_proxies,
				(SELECT COUNT(*) FROM model_catalog) AS model_count,
				(SELECT COUNT(*) FROM model_catalog WHERE status = 'active' AND public_visible = true) AS public_model_count
		),
		risk_stats AS (
			SELECT COUNT(*) AS risk_events_24h
			FROM risk_events
			WHERE created_at > now() - interval '24 hours'
		)
		SELECT 'total_users', total_users::text FROM user_stats
		UNION ALL SELECT 'new_users_24h', new_users_24h::text FROM user_stats
		UNION ALL SELECT 'active_users', active_users::text FROM user_stats
		UNION ALL SELECT 'disabled_users', disabled_users::text FROM user_stats
		UNION ALL SELECT 'operator_users', operator_users::text FROM user_stats
		UNION ALL SELECT 'owner_users', owner_users::text FROM user_stats
		UNION ALL SELECT 'wallet_balance', wallet_balance FROM wallet_stats
		UNION ALL SELECT 'reserved_balance', reserved_balance FROM wallet_stats
		UNION ALL SELECT 'low_balance_users', low_balance_users::text FROM wallet_stats
		UNION ALL SELECT 'requests_24h', requests_24h::text FROM usage_stats
		UNION ALL SELECT 'failed_requests_24h', failed_requests_24h::text FROM usage_stats
		UNION ALL SELECT 'rejected_requests_24h', rejected_requests_24h::text FROM usage_stats
		UNION ALL SELECT 'cost_24h', cost_24h FROM usage_stats
		UNION ALL SELECT 'provider_count', provider_count::text FROM upstream_stats
		UNION ALL SELECT 'channel_count', channel_count::text FROM upstream_stats
		UNION ALL SELECT 'active_channels', active_channels::text FROM upstream_stats
		UNION ALL SELECT 'account_count', account_count::text FROM upstream_stats
		UNION ALL SELECT 'active_accounts', active_accounts::text FROM upstream_stats
		UNION ALL SELECT 'proxy_count', proxy_count::text FROM upstream_stats
		UNION ALL SELECT 'active_proxies', active_proxies::text FROM upstream_stats
		UNION ALL SELECT 'model_count', model_count::text FROM upstream_stats
		UNION ALL SELECT 'public_model_count', public_model_count::text FROM upstream_stats
		UNION ALL SELECT 'risk_events_24h', risk_events_24h::text FROM risk_stats
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	metrics := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		metrics[key] = value
	}
	return metrics, rows.Err()
}

func (s *Server) adminConfigStatus(ctx context.Context) (map[string]any, error) {
	emailSettings, err := s.emailVerificationSettings(ctx)
	if err != nil {
		return nil, err
	}
	stripeSettings, err := s.stripeSettings(ctx)
	if err != nil {
		return nil, err
	}
	paymentSettings, err := s.paymentSettings(ctx)
	if err != nil {
		return nil, err
	}
	paymentRoutes, err := s.listPaymentRoutes(ctx, true)
	if err != nil {
		return nil, err
	}
	paymentProviders, err := s.listPaymentProviders(ctx, true)
	if err != nil {
		return nil, err
	}
	openRegistration := true
	var siteRaw string
	if err := s.db.QueryRowContext(ctx, "SELECT setting_value_json::text FROM system_settings WHERE setting_key = 'site'").Scan(&siteRaw); err == nil {
		var site map[string]any
		if decodeJSONString(siteRaw, &site) == nil {
			if value, ok := site["open_registration"].(bool); ok {
				openRegistration = value
			}
		}
	} else if err != sql.ErrNoRows {
		return nil, err
	}

	paymentConfigured := stripeSettings.secretKeyConfigured()
	for _, route := range paymentRoutes {
		if route.Method == "stripe" && (hasProviderForRoute(route, paymentProviders) || stripeSettings.secretKeyConfigured()) {
			paymentConfigured = true
			break
		}
		if route.Method != "stripe" && paymentSettings.Enabled && hasProviderForRoute(route, paymentProviders) {
			paymentConfigured = true
			break
		}
	}
	return map[string]any{
		"site": map[string]any{
			"portal_base_url":       s.cfg.PortalBaseURL,
			"public_base_url":       s.cfg.PublicBaseURL,
			"open_registration":     openRegistration,
			"portal_url_configured": configuredNonLocalURL(s.cfg.PortalBaseURL),
		},
		"auth": map[string]any{
			"registration_email_verification_enabled": emailSettings.RegistrationVerificationEnabled,
			"smtp_configured":                         emailSettings.SMTP.enabled(),
		},
		"stripe": map[string]any{
			"secret_key_configured":     stripeSettings.secretKeyConfigured(),
			"webhook_secret_configured": stripeSettings.webhookSecretConfigured(),
			"success_url_configured":    configuredNonLocalURL(stripeSettings.successURL("check", s.cfg.PortalBaseURL)),
			"cancel_url_configured":     configuredNonLocalURL(stripeSettings.cancelURL("check", s.cfg.PortalBaseURL)),
			"secret_key_source":         stripeSettings.SecretKeySource,
			"webhook_secret_source":     stripeSettings.WebhookSecretSource,
		},
		"payment": map[string]any{
			"configured":            paymentConfigured,
			"enabled":               paymentSettings.Enabled,
			"enabled_method_count":  len(paymentRoutes),
			"active_provider_count": len(paymentProviders),
			"legacy_stripe_enabled": stripeSettings.secretKeyConfigured(),
		},
		"security": map[string]any{
			"vault_key_configured": strings.TrimSpace(s.cfg.VaultKey) != "",
			"cookie_secure":        s.cfg.CookieSecure,
			"cors_allowed_origins": s.cfg.CORSAllowedOrigins,
		},
	}, nil
}

func setupChecklistFromStatus(status map[string]any, metrics map[string]string) []map[string]any {
	authStatus, _ := status["auth"].(map[string]any)
	paymentStatus, _ := status["payment"].(map[string]any)
	checks := []map[string]any{
		{"id": "site", "label": "站点基础配置", "complete": true, "target_view": "content"},
		{"id": "smtp", "label": "SMTP 可发送注册验证码", "complete": overviewBool(authStatus["smtp_configured"]), "target_view": "controls"},
		{"id": "registration_email", "label": "注册邮箱验证码策略", "complete": overviewBool(authStatus["registration_email_verification_enabled"]), "target_view": "controls"},
		{"id": "payment", "label": "支付配置", "complete": overviewBool(paymentStatus["configured"]), "target_view": "billing"},
		{"id": "provider", "label": "至少一个供应商", "complete": intMetric(metrics, "provider_count") > 0, "target_view": "upstream"},
		{"id": "channel", "label": "至少一个可用通道", "complete": intMetric(metrics, "active_channels") > 0, "target_view": "upstream"},
		{"id": "model", "label": "至少一个公开模型", "complete": intMetric(metrics, "public_model_count") > 0, "target_view": "models"},
		{"id": "account", "label": "至少一个可用账号", "complete": intMetric(metrics, "active_accounts") > 0, "target_view": "pool"},
	}
	return checks
}

func configuredNonLocalURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host != "" && host != "localhost" && host != "127.0.0.1"
}

func overviewBool(value any) bool {
	result, _ := value.(bool)
	return result
}

func intMetric(metrics map[string]string, key string) int {
	value := strings.TrimSpace(metrics[key])
	n := 0
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			break
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
