package httpserver

import "testing"

func TestWebhookSignature(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	got := webhookSignature("whsec_test", "1700000000", body)
	const expected = "c89214b5b5da833daed6f0b8c5bb6bd58cea9022bd80ccc78230f3942d632925"
	if got != expected {
		t.Fatalf("signature = %q, expected %q", got, expected)
	}
}

func TestNotificationRetryableStatuses(t *testing.T) {
	got := notificationRetryableStatuses()
	want := []string{"pending", "failed", "suppressed"}
	if len(got) != len(want) {
		t.Fatalf("got %d statuses, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
