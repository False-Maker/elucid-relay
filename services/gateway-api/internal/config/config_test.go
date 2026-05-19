package config

import (
	"strings"
	"testing"
)

func TestValidateForServeAllowsDevelopmentDefaults(t *testing.T) {
	cfg := Config{
		DatabaseURL:        "postgres://user:pass@localhost/db",
		AppEnv:             "development",
		SessionTTLHours:    168,
		VaultKey:           defaultVaultKey,
		CORSAllowedOrigins: defaultCORSAllowedOrigins,
	}
	if err := cfg.ValidateForServe(); err != nil {
		t.Fatalf("ValidateForServe returned error: %v", err)
	}
}

func TestValidateForServeRejectsProductionDefaults(t *testing.T) {
	cfg := productionConfig()
	cfg.VaultKey = defaultVaultKey
	if err := cfg.ValidateForServe(); err == nil || !strings.Contains(err.Error(), "VAULT_KEY") {
		t.Fatalf("ValidateForServe error = %v, expected VAULT_KEY error", err)
	}

	cfg = productionConfig()
	cfg.CookieSecure = false
	if err := cfg.ValidateForServe(); err == nil || !strings.Contains(err.Error(), "COOKIE_SECURE") {
		t.Fatalf("ValidateForServe error = %v, expected COOKIE_SECURE error", err)
	}

	cfg = productionConfig()
	cfg.CORSAllowedOrigins = defaultCORSAllowedOrigins
	if err := cfg.ValidateForServe(); err == nil || !strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") {
		t.Fatalf("ValidateForServe error = %v, expected CORS_ALLOWED_ORIGINS error", err)
	}

	cfg = productionConfig()
	cfg.PortalBaseURL = defaultPortalBaseURL
	if err := cfg.ValidateForServe(); err == nil || !strings.Contains(err.Error(), "PORTAL_BASE_URL") {
		t.Fatalf("ValidateForServe error = %v, expected PORTAL_BASE_URL error", err)
	}

	cfg = productionConfig()
	cfg.SMTPHost = ""
	if err := cfg.ValidateForServe(); err == nil || !strings.Contains(err.Error(), "SMTP_HOST") {
		t.Fatalf("ValidateForServe error = %v, expected SMTP_HOST error", err)
	}

	cfg = productionConfig()
	cfg.SMTPFrom = ""
	if err := cfg.ValidateForServe(); err == nil || !strings.Contains(err.Error(), "SMTP_FROM") {
		t.Fatalf("ValidateForServe error = %v, expected SMTP_FROM error", err)
	}
}

func TestValidateForServeRejectsInvalidTrustedProxyCIDRs(t *testing.T) {
	cfg := productionConfig()
	cfg.TrustedProxyCIDRs = "10.0.0.0/8,not-a-cidr"
	if err := cfg.ValidateForServe(); err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXY_CIDRS") {
		t.Fatalf("ValidateForServe error = %v, expected TRUSTED_PROXY_CIDRS error", err)
	}
}

func productionConfig() Config {
	return Config{
		DatabaseURL:        "postgres://user:pass@localhost/db",
		AppEnv:             "production",
		CookieSecure:       true,
		SessionTTLHours:    24,
		VaultKey:           "production-vault-key-32-bytes-long",
		CORSAllowedOrigins: "https://admin.example.com,https://portal.example.com",
		PortalBaseURL:      "https://portal.example.com",
		SMTPHost:           "smtp.example.com",
		SMTPPort:           587,
		SMTPFrom:           "Elucid Relay <noreply@example.com>",
		SMTPTLSMode:        "starttls",
	}
}
