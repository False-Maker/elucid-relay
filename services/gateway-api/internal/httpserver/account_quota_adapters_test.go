package httpserver

import "testing"

func TestQuotaReadingsFromOpenAICreditGrantsTotals(t *testing.T) {
	readings := quotaReadingsFromValueWithSchema(map[string]any{
		"total_granted":   100.0,
		"total_used":      35.5,
		"total_available": 64.5,
	}, "openai_compatible", "openai_credit_grants")
	if len(readings) != 1 {
		t.Fatalf("readings length = %d", len(readings))
	}
	if readings[0].WindowType != "credits" {
		t.Fatalf("window type = %q", readings[0].WindowType)
	}
	if readings[0].Remaining != "64.5" {
		t.Fatalf("remaining = %q", readings[0].Remaining)
	}
	if readings[0].Limit != "100" {
		t.Fatalf("limit = %q", readings[0].Limit)
	}
}

func TestQuotaAdapterConfigFromObjectMetadata(t *testing.T) {
	config, ok := quotaAdapterConfigFromMetadata(map[string]any{
		"quota_adapter": map[string]any{
			"type":     "openai_compatible",
			"schema":   "openai_credit_grants",
			"endpoint": "/dashboard/billing/credit_grants",
		},
	})
	if !ok {
		t.Fatal("config should be detected")
	}
	if config.Type != "openai_compatible" {
		t.Fatalf("type = %q", config.Type)
	}
	if config.Schema != "openai_credit_grants" {
		t.Fatalf("schema = %q", config.Schema)
	}
	if config.Endpoint != "/dashboard/billing/credit_grants" {
		t.Fatalf("endpoint = %q", config.Endpoint)
	}
}
