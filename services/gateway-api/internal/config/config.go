package config

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	defaultVaultKey           = "local-development-vault-key-32!!"
	defaultCORSAllowedOrigins = "http://localhost:5173,http://127.0.0.1:5173,http://localhost:18081,http://127.0.0.1:18081"
	defaultPortalBaseURL      = "http://localhost:18081"
)

type Config struct {
	Port                 string
	DatabaseURL          string
	RedisAddr            string
	MigrateOnStart       bool
	AppEnv               string
	CookieSecure         bool
	SessionTTLHours      int
	VaultKey             string
	CORSAllowedOrigins   string
	TrustedProxyCIDRs    string
	RateLimitEnabled     bool
	OAuthWrapperToken    string
	PortalBaseURL        string
	PublicBaseURL        string
	StripeSecretKey      string
	StripeWebhookSecret  string
	StripeAPIBaseURL     string
	BillingSuccessURL    string
	BillingCancelURL     string
	SMTPHost             string
	SMTPPort             int
	SMTPUsername         string
	SMTPPassword         string
	SMTPFrom             string
	SMTPTLSMode          string
	SeedOwnerEmail       string
	SeedOwnerPassword    string
	SeedOwnerDisplayName string
	SeedPersonalEmail    string
	SeedPersonalPassword string
	SeedPersonalName     string
}

func Load() Config {
	return Config{
		Port:                 env("PORT", "8080"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		RedisAddr:            env("REDIS_ADDR", "localhost:6379"),
		MigrateOnStart:       envBool("MIGRATE_ON_START", false),
		AppEnv:               env("APP_ENV", "development"),
		CookieSecure:         envBool("COOKIE_SECURE", false),
		SessionTTLHours:      envInt("SESSION_TTL_HOURS", 168),
		VaultKey:             env("VAULT_KEY", defaultVaultKey),
		CORSAllowedOrigins:   env("CORS_ALLOWED_ORIGINS", defaultCORSAllowedOrigins),
		TrustedProxyCIDRs:    os.Getenv("TRUSTED_PROXY_CIDRS"),
		RateLimitEnabled:     envBool("RATE_LIMIT_ENABLED", true),
		OAuthWrapperToken:    os.Getenv("OAUTH_WRAPPER_BEARER_TOKEN"),
		PortalBaseURL:        env("PORTAL_BASE_URL", defaultPortalBaseURL),
		PublicBaseURL:        env("PUBLIC_BASE_URL", defaultPortalBaseURL),
		StripeSecretKey:      os.Getenv("STRIPE_SECRET_KEY"),
		StripeWebhookSecret:  os.Getenv("STRIPE_WEBHOOK_SECRET"),
		StripeAPIBaseURL:     env("STRIPE_API_BASE_URL", "https://api.stripe.com"),
		BillingSuccessURL:    os.Getenv("BILLING_SUCCESS_URL"),
		BillingCancelURL:     os.Getenv("BILLING_CANCEL_URL"),
		SMTPHost:             os.Getenv("SMTP_HOST"),
		SMTPPort:             envInt("SMTP_PORT", 587),
		SMTPUsername:         os.Getenv("SMTP_USERNAME"),
		SMTPPassword:         os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:             os.Getenv("SMTP_FROM"),
		SMTPTLSMode:          env("SMTP_TLS_MODE", "starttls"),
		SeedOwnerEmail:       os.Getenv("SEED_OWNER_EMAIL"),
		SeedOwnerPassword:    os.Getenv("SEED_OWNER_PASSWORD"),
		SeedOwnerDisplayName: env("SEED_OWNER_DISPLAY_NAME", "Platform Owner"),
		SeedPersonalEmail:    os.Getenv("SEED_PERSONAL_EMAIL"),
		SeedPersonalPassword: os.Getenv("SEED_PERSONAL_PASSWORD"),
		SeedPersonalName:     env("SEED_PERSONAL_NAME", "Personal Test User"),
	}
}

func (c Config) HTTPAddr() string {
	return ":" + c.Port
}

func (c Config) ValidateForServe() error {
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return errors.New("DATABASE_URL is required")
	}
	if c.SessionTTLHours <= 0 {
		return errors.New("SESSION_TTL_HOURS must be positive")
	}
	if c.SMTPPort < 0 || (strings.TrimSpace(c.SMTPHost) != "" && c.SMTPPort == 0) {
		return errors.New("SMTP_PORT must be positive")
	}
	if err := validateSMTPTLSMode(c.SMTPTLSMode); err != nil {
		return err
	}
	if err := validateTrustedProxyCIDRs(c.TrustedProxyCIDRs); err != nil {
		return err
	}
	if strings.TrimSpace(c.AppEnv) == "development" {
		return nil
	}
	if strings.TrimSpace(c.VaultKey) == "" || c.VaultKey == defaultVaultKey {
		return errors.New("VAULT_KEY must be set to a non-default value outside development")
	}
	if !c.CookieSecure {
		return errors.New("COOKIE_SECURE=true is required outside development")
	}
	if strings.TrimSpace(c.CORSAllowedOrigins) == "" || strings.TrimSpace(c.CORSAllowedOrigins) == defaultCORSAllowedOrigins {
		return errors.New("CORS_ALLOWED_ORIGINS must be restricted outside development")
	}
	if strings.TrimSpace(c.PortalBaseURL) == "" || strings.TrimSpace(c.PortalBaseURL) == defaultPortalBaseURL {
		return errors.New("PORTAL_BASE_URL must be set outside development")
	}
	if strings.TrimSpace(c.SMTPHost) == "" {
		return errors.New("SMTP_HOST is required outside development")
	}
	if strings.TrimSpace(c.SMTPFrom) == "" {
		return errors.New("SMTP_FROM is required outside development")
	}
	if c.SMTPPort <= 0 {
		return errors.New("SMTP_PORT must be positive")
	}
	return nil
}

func validateSMTPTLSMode(raw string) error {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" || mode == "starttls" || mode == "tls" || mode == "none" {
		return nil
	}
	return errors.New("SMTP_TLS_MODE must be starttls, tls, or none")
}

func validateTrustedProxyCIDRs(raw string) error {
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			continue
		}
		if _, _, err := net.ParseCIDR(entry); err != nil {
			return errors.New("TRUSTED_PROXY_CIDRS must contain only IPs or CIDRs")
		}
	}
	return nil
}

func env(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
