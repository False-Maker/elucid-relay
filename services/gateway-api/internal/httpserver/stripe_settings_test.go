package httpserver

import "testing"

func TestStripeAPIBaseURL(t *testing.T) {
	if got := stripeAPIBaseURL(""); got != "https://api.stripe.com" {
		t.Fatalf("empty Stripe API base = %q", got)
	}
	if got := stripeAPIBaseURL(" http://stripe.local "); got != "http://stripe.local" {
		t.Fatalf("custom Stripe API base = %q", got)
	}
}
