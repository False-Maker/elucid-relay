package httpserver

import (
	"context"
	"strconv"
	"strings"
	"time"
)

type accountQuotaAdapter interface {
	Discover(context.Context, *Server, accountQuotaTarget) ([]quotaReading, error)
}

type metadataQuotaAdapter struct{}
type configuredEndpointQuotaAdapter struct{}

type quotaAdapterConfig struct {
	Type     string
	Schema   string
	Endpoint string
	Source   string
}

func accountQuotaAdaptersForTarget(accountQuotaTarget) []accountQuotaAdapter {
	return []accountQuotaAdapter{
		metadataQuotaAdapter{},
		configuredEndpointQuotaAdapter{},
	}
}

func (adapter metadataQuotaAdapter) Discover(ctx context.Context, s *Server, target accountQuotaTarget) ([]quotaReading, error) {
	_ = ctx
	_ = s
	if readings := quotaReadingsFromMetadata(target.AccountMetadata, "account_metadata"); len(readings) > 0 {
		return readings, nil
	}
	if readings := quotaReadingsFromMetadata(target.ChannelMetadata, "channel_metadata"); len(readings) > 0 {
		return readings, nil
	}
	if readings := quotaReadingsFromMetadata(target.ProviderMeta, "provider_metadata"); len(readings) > 0 {
		return readings, nil
	}
	return nil, nil
}

func (adapter configuredEndpointQuotaAdapter) Discover(ctx context.Context, s *Server, target accountQuotaTarget) ([]quotaReading, error) {
	config := quotaAdapterConfigForTarget(target)
	endpoint := firstNonEmpty(
		config.Endpoint,
		metadataText(target.AccountMetadata["quota_endpoint"]),
		metadataText(target.ChannelMetadata["quota_endpoint"]),
		metadataText(target.ProviderMeta["quota_endpoint"]),
	)
	if endpoint == "" {
		return nil, nil
	}
	schema := firstNonEmpty(
		config.Schema,
		metadataText(target.AccountMetadata["quota_schema"]),
		metadataText(target.ChannelMetadata["quota_schema"]),
		metadataText(target.ProviderMeta["quota_schema"]),
	)
	source := firstNonEmpty(config.Source, config.Type, "quota_endpoint")
	readings, err := s.quotaReadingsFromEndpointWithSchema(ctx, target, endpoint, schema, source)
	if err != nil {
		return nil, err
	}
	for index := range readings {
		if readings[index].Raw == nil {
			readings[index].Raw = map[string]any{}
		}
		if config.Type != "" {
			readings[index].Raw["quota_adapter"] = config.Type
		}
		if schema != "" {
			readings[index].Raw["quota_schema"] = schema
		}
	}
	return readings, nil
}

func quotaAdapterConfigForTarget(target accountQuotaTarget) quotaAdapterConfig {
	for _, metadata := range []map[string]any{target.AccountMetadata, target.ChannelMetadata, target.ProviderMeta} {
		if config, ok := quotaAdapterConfigFromMetadata(metadata); ok {
			return config
		}
	}
	return quotaAdapterConfig{Type: strings.TrimSpace(target.ProviderType)}
}

func quotaAdapterConfigFromMetadata(metadata map[string]any) (quotaAdapterConfig, bool) {
	if metadata == nil {
		return quotaAdapterConfig{}, false
	}
	config := quotaAdapterConfig{
		Schema:   metadataText(metadata["quota_schema"]),
		Endpoint: metadataText(metadata["quota_endpoint"]),
		Source:   metadataText(metadata["quota_source"]),
	}
	value, ok := metadata["quota_adapter"]
	if !ok {
		return config, config.Schema != "" || config.Endpoint != "" || config.Source != ""
	}
	switch typed := value.(type) {
	case string:
		config.Type = strings.TrimSpace(typed)
	case map[string]any:
		config.Type = firstNonEmpty(metadataText(typed["type"]), metadataText(typed["name"]), metadataText(typed["adapter"]))
		config.Schema = firstNonEmpty(metadataText(typed["schema"]), config.Schema)
		config.Endpoint = firstNonEmpty(metadataText(typed["endpoint"]), config.Endpoint)
		config.Source = firstNonEmpty(metadataText(typed["source"]), config.Source)
	default:
		return config, config.Schema != "" || config.Endpoint != "" || config.Source != ""
	}
	return config, true
}

func quotaReadingsFromValueWithSchema(value any, source string, schema string) []quotaReading {
	schema = strings.ToLower(strings.TrimSpace(schema))
	switch schema {
	case "", "generic", "metadata":
		return quotaReadingsFromValue(value, source)
	case "openai_credit_grants", "openai_dashboard_credit_grants", "credit_grants":
		if readings := quotaReadingsFromOpenAICreditGrants(value, source); len(readings) > 0 {
			return readings
		}
		return quotaReadingsFromValue(value, source)
	default:
		return quotaReadingsFromValue(value, source)
	}
}

func quotaReadingsFromOpenAICreditGrants(value any, source string) []quotaReading {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	remaining, remainingOK := decimalStringFromAny(firstQuotaField(root, "total_available", "available", "remaining"))
	limitValue, limitOK := decimalStringFromAny(firstQuotaField(root, "total_granted", "granted", "limit", "limit_value", "total"))
	used, usedOK := decimalStringFromAny(firstQuotaField(root, "total_used", "used"))
	if !remainingOK && limitOK && usedOK {
		if remainingValue, ok := subtractDecimalStrings(limitValue, used); ok {
			remaining = remainingValue
			remainingOK = true
		}
	}
	resetAt := timeFromAny(firstQuotaField(root, "expires_at", "reset_at", "grants_expires_at"))
	if !remainingOK || !limitOK {
		if grants, ok := openAICreditGrantItems(root); ok {
			grantRemaining, grantLimit, grantResetAt, grantOK := summarizeOpenAICreditGrants(grants)
			if grantOK {
				remaining = grantRemaining
				limitValue = grantLimit
				resetAt = grantResetAt
				remainingOK = true
				limitOK = true
			}
		}
	}
	if !remainingOK && !limitOK {
		return nil
	}
	return []quotaReading{{
		Status:     "success",
		Source:     defaultString(source, "openai_credit_grants"),
		WindowType: "credits",
		Remaining:  remaining,
		Limit:      limitValue,
		ResetAt:    resetAt,
		Raw:        root,
	}}
}

func openAICreditGrantItems(root map[string]any) ([]any, bool) {
	if items, ok := root["data"].([]any); ok {
		return items, true
	}
	if grants, ok := root["grants"].(map[string]any); ok {
		if items, ok := grants["data"].([]any); ok {
			return items, true
		}
	}
	return nil, false
}

func summarizeOpenAICreditGrants(items []any) (string, string, *time.Time, bool) {
	var remainingSum float64
	var limitSum float64
	var resetAt *time.Time
	found := false
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		grant, grantOK := floatFromAny(firstQuotaField(row, "grant_amount", "amount", "total_granted", "limit"))
		used, _ := floatFromAny(firstQuotaField(row, "used_amount", "total_used", "used"))
		if !grantOK {
			continue
		}
		remaining := grant - used
		if remaining < 0 {
			remaining = 0
		}
		limitSum += grant
		remainingSum += remaining
		if expiresAt := timeFromAny(firstQuotaField(row, "expires_at", "reset_at")); expiresAt != nil {
			if resetAt == nil || expiresAt.Before(*resetAt) {
				resetAt = expiresAt
			}
		}
		found = true
	}
	if !found {
		return "", "", nil, false
	}
	return strconv.FormatFloat(remainingSum, 'f', -1, 64), strconv.FormatFloat(limitSum, 'f', -1, 64), resetAt, true
}

func subtractDecimalStrings(left string, right string) (string, bool) {
	leftValue, err := strconv.ParseFloat(strings.TrimSpace(left), 64)
	if err != nil {
		return "", false
	}
	rightValue, err := strconv.ParseFloat(strings.TrimSpace(right), 64)
	if err != nil {
		return "", false
	}
	result := leftValue - rightValue
	if result < 0 {
		result = 0
	}
	return strconv.FormatFloat(result, 'f', -1, 64), true
}

func floatFromAny(value any) (float64, bool) {
	text, ok := decimalStringFromAny(value)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}
