package httpserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestVerifyStripeSignature(t *testing.T) {
	payload := []byte(`{"id":"evt_test"}`)
	secret := "whsec_test"
	timestamp := time.Now().Unix()
	header := stripeSignatureHeader(payload, secret, timestamp)

	if !verifyStripeSignature(payload, header, secret) {
		t.Fatal("expected valid Stripe signature")
	}

	if verifyStripeSignature(payload, header, "wrong") {
		t.Fatal("expected invalid Stripe signature for wrong secret")
	}
}

func TestVerifyStripeSignatureRejectsExpiredTimestamp(t *testing.T) {
	payload := []byte(`{"id":"evt_test"}`)
	secret := "whsec_test"
	header := stripeSignatureHeader(payload, secret, time.Now().Add(-10*time.Minute).Unix())

	if verifyStripeSignature(payload, header, secret) {
		t.Fatal("expected expired Stripe signature to be rejected")
	}
}

func TestStripeInvoiceRenewalDetection(t *testing.T) {
	cases := []struct {
		name string
		obj  map[string]any
		want bool
	}{
		{name: "first invoice ignored", obj: map[string]any{"billing_reason": "subscription_create"}, want: false},
		{name: "cycle renews", obj: map[string]any{"billing_reason": "subscription_cycle"}, want: true},
		{name: "subscription update renews", obj: map[string]any{"billing_reason": "subscription_update"}, want: true},
		{name: "missing reason ignored", obj: map[string]any{}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripeInvoiceIsSubscriptionRenewal(tc.obj); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPaymentEventReplayEligibility(t *testing.T) {
	replayable := []string{"pending", "failed"}
	for _, status := range replayable {
		if !paymentEventCanReplay(status) {
			t.Fatalf("expected %q to be replayable", status)
		}
	}
	notReplayable := []string{"processing", "processed", "replayed", "sent", ""}
	for _, status := range notReplayable {
		if paymentEventCanReplay(status) {
			t.Fatalf("expected %q to not be replayable", status)
		}
	}
}

func TestPaymentEventStatusValidation(t *testing.T) {
	valid := []string{"pending", "processing", "processed", "failed", "replayed"}
	for _, status := range valid {
		if !validPaymentEventStatus(status) {
			t.Fatalf("expected %q to be valid", status)
		}
	}
	invalid := []string{"sent", "suppressed", "unknown", ""}
	for _, status := range invalid {
		if validPaymentEventStatus(status) {
			t.Fatalf("expected %q to be invalid", status)
		}
	}
}

func TestDecodeStripeWebhookEvent(t *testing.T) {
	event, err := decodeStripeWebhookEvent([]byte(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_1","metadata":{"order_id":"order_1"}}}}`))
	if err != nil {
		t.Fatalf("decodeStripeWebhookEvent returned error: %v", err)
	}
	if event.ID != "evt_1" || event.Type != "checkout.session.completed" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if got := stripeOrderID(event.Data.Object); got != "order_1" {
		t.Fatalf("order id = %q, want order_1", got)
	}
	if _, err := decodeStripeWebhookEvent([]byte(`{"type":"checkout.session.completed"}`)); err == nil {
		t.Fatal("expected missing id to fail")
	}
}

func TestStripeInvoicePeriodAndSubscriptionPeriodEnd(t *testing.T) {
	startUnix := int64(1_700_000_000)
	endUnix := int64(1_702_592_000)
	start, end := stripeInvoicePeriod(map[string]any{
		"period_start": float64(startUnix),
		"period_end":   float64(endUnix),
	})
	if start == nil || start.Unix() != startUnix {
		t.Fatalf("unexpected period_start: %#v", start)
	}
	if end == nil || end.Unix() != endUnix {
		t.Fatalf("unexpected period_end: %#v", end)
	}

	monthEnd := subscriptionPeriodEnd(time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC), "month")
	if monthEnd.Month() != time.June || monthEnd.Day() != 10 {
		t.Fatalf("unexpected month end: %s", monthEnd)
	}
	yearEnd := subscriptionPeriodEnd(time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC), "year")
	if yearEnd.Year() != 2027 || yearEnd.Month() != time.May || yearEnd.Day() != 10 {
		t.Fatalf("unexpected year end: %s", yearEnd)
	}
}

func TestRiskRuleMatching(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?callback=http://127.0.0.1/hook", strings.NewReader(`{"prompt":"leak secret","callback_url":"http://169.254.169.254/latest"}`))
	body := []byte(`{"prompt":"leak secret","callback_url":"http://169.254.169.254/latest"}`)

	target, matched, ok := matchRiskRule(riskRule{RuleType: "sensitive_word", Pattern: "secret"}, req, "chat", body, string(body))
	if !ok || target != "body" || matched != "secret" {
		t.Fatalf("sensitive word mismatch: target=%q matched=%q ok=%v", target, matched, ok)
	}

	target, matched, ok = matchRiskRule(riskRule{RuleType: "ssrf_target"}, req, "chat", body, string(body))
	if !ok || target != "url" || matched == "" {
		t.Fatalf("ssrf mismatch: target=%q matched=%q ok=%v", target, matched, ok)
	}

	botReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	botReq.Header.Set("User-Agent", "ExampleBot/1.0")
	target, matched, ok = matchRiskRule(riskRule{RuleType: "bot_protection"}, botReq, "chat", nil, "")
	if !ok || target != "user_agent" || matched != "ExampleBot/1.0" {
		t.Fatalf("bot mismatch: target=%q matched=%q ok=%v", target, matched, ok)
	}
}

func TestRiskActionRankBlockWinsOverThrottleAndFlag(t *testing.T) {
	if riskActionRank("block") <= riskActionRank("throttle") || riskActionRank("throttle") <= riskActionRank("flag") || riskActionRank("flag") <= riskActionRank("allow") {
		t.Fatal("risk action precedence should be block > throttle > flag > allow")
	}
}

func TestParseDiscoveredModels(t *testing.T) {
	models := parseDiscoveredModels([]byte(`{"data":[{"id":"gpt-test","endpoint_capabilities":["chat","responses"]},"text-only"]}`), "openai_compatible")
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if models[0].ID != "gpt-test" || strings.Join(models[0].Endpoints, ",") != "chat,responses" {
		t.Fatalf("unexpected first model: %#v", models[0])
	}
	if models[1].ID != "text-only" || strings.Join(models[1].Endpoints, ",") != "chat,responses" {
		t.Fatalf("unexpected second model: %#v", models[1])
	}
}

func TestModelDiscoveryURL(t *testing.T) {
	if got := modelDiscoveryURL(routeInfo{BaseURL: "https://upstream.example/v1"}); got != "https://upstream.example/v1/models" {
		t.Fatalf("got %q", got)
	}
	if got := modelDiscoveryURL(routeInfo{BaseURL: "https://upstream.example/api"}); got != "https://upstream.example/api/v1/models" {
		t.Fatalf("got %q", got)
	}
}

func TestParseDiscoveredModelsSupportsModelsArray(t *testing.T) {
	models := parseDiscoveredModels([]byte(`{"models":[{"name":"claude-test","capabilities":["messages"]}]}`), "anthropic")
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ID != "claude-test" || strings.Join(models[0].Endpoints, ",") != "messages" {
		t.Fatalf("unexpected model: %#v", models[0])
	}
}

func TestDiffChannelModels(t *testing.T) {
	existing := []channelAbilitySnapshot{
		{ModelName: "gpt-a", Endpoint: "chat", UpstreamModel: "gpt-a", Status: "active"},
		{ModelName: "gpt-b", Endpoint: "chat", UpstreamModel: "old", Status: "active"},
		{ModelName: "gpt-c", Endpoint: "chat", UpstreamModel: "gpt-c", Status: "active"},
		{ModelName: "gpt-d", Endpoint: "chat", UpstreamModel: "gpt-d", Status: "disabled"},
	}
	discovered := []discoveredModel{
		{ID: "gpt-a", Endpoints: []string{"chat"}},
		{ID: "gpt-b", Endpoints: []string{"chat"}},
		{ID: "gpt-new", Endpoints: []string{"chat"}},
	}

	diff := diffChannelModels(existing, discovered, "openai_compatible")
	if diff.UnchangedCount != 1 {
		t.Fatalf("unchanged = %d", diff.UnchangedCount)
	}
	if strings.Join(diff.AddedModels, ",") != "gpt-new:chat" {
		t.Fatalf("added = %#v", diff.AddedModels)
	}
	if strings.Join(diff.UpdatedModels, ",") != "gpt-b:chat" {
		t.Fatalf("updated = %#v", diff.UpdatedModels)
	}
	if strings.Join(diff.MissingModels, ",") != "gpt-c:chat" {
		t.Fatalf("missing = %#v", diff.MissingModels)
	}
}

func TestApplyBillingMultiplier(t *testing.T) {
	policy := effectivePolicy{BillingMultiplier: 2.5}
	if got := applyBillingMultiplier(4, policy); got != 10 {
		t.Fatalf("got %f, want 10", got)
	}
	policy.BillingMultiplier = 0
	if got := applyBillingMultiplier(4, policy); got != 0 {
		t.Fatalf("got %f, want zero when multiplier is zero", got)
	}
}

func TestEffectivePolicyModelAccessAllowListAndDeny(t *testing.T) {
	if err := (effectivePolicy{}).enforceModelAccess(); err != nil {
		t.Fatalf("default policy should allow: %v", err)
	}
	if err := (effectivePolicy{GroupID: "group-1", Permission: "deny"}).enforceModelAccess(); err == nil {
		t.Fatal("deny permission should reject")
	}
	if err := (effectivePolicy{GroupID: "group-1", GroupAllowCount: 2}).enforceModelAccess(); err == nil {
		t.Fatal("group allow-list without matching allow should reject")
	}
	if err := (effectivePolicy{GroupID: "group-1", GroupAllowCount: 2, Permission: "allow"}).enforceModelAccess(); err != nil {
		t.Fatalf("matching allow should pass: %v", err)
	}
	if policyAllowsListedModel(effectivePolicy{GroupID: "group-1", GroupAllowCount: 1}) {
		t.Fatal("listed models should hide non-allowed model")
	}
}

func TestOrderStatusAllowsRefundBlockedRetry(t *testing.T) {
	for _, status := range []string{"paid", "refund_blocked"} {
		if !orderStatusAllowsRefund(status) {
			t.Fatalf("%s should be refundable", status)
		}
	}
	for _, status := range []string{"pending", "failed", "canceled", "refunded"} {
		if orderStatusAllowsRefund(status) {
			t.Fatalf("%s should not be refundable", status)
		}
	}
}

func TestCNYCentsFromUSDUsesCeiling(t *testing.T) {
	if got := cnyCentsFromUSD("1.01", "7.2"); got != 728 {
		t.Fatalf("got %d, want 728", got)
	}
}

func TestEasyPaySignature(t *testing.T) {
	values := url.Values{}
	values.Set("pid", "1000")
	values.Set("type", "alipay")
	values.Set("out_trade_no", "order_1")
	values.Set("money", "72.00")
	values.Set("sign_type", "MD5")
	values.Set("sign", easyPaySign(values, "secret"))
	if !verifyEasyPaySign(values, "secret") {
		t.Fatal("expected EasyPay signature to verify")
	}
	if verifyEasyPaySign(values, "wrong") {
		t.Fatal("expected EasyPay signature to reject wrong key")
	}
}

func TestNormalizePaymentMethods(t *testing.T) {
	if got := strings.Join(normalizePaymentMethods("easypay", nil), ","); got != "alipay,wechat" {
		t.Fatalf("got %q", got)
	}
	if got := strings.Join(normalizePaymentMethods("stripe", []string{"wechat", "bad", "wechat"}), ","); got != "wechat" {
		t.Fatalf("got %q", got)
	}
}

func stripeSignatureHeader(payload []byte, secret string, timestamp int64) string {
	timestampText := strconv.FormatInt(timestamp, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestampText))
	mac.Write([]byte("."))
	mac.Write(payload)
	return "t=" + timestampText + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
