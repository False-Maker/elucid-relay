package httpserver

import "testing"

func TestRoutingModeValue(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		expected  string
		wantError bool
	}{
		{name: "defaults to pool", value: "", expected: "pool"},
		{name: "keeps pool", value: "pool", expected: "pool"},
		{name: "keeps byo", value: "byo", expected: "byo"},
		{name: "rejects invalid", value: "shared", wantError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := routingModeValue(tc.value)
			if tc.wantError {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expected {
				t.Fatalf("routingModeValue(%q) = %q, expected %q", tc.value, got, tc.expected)
			}
		})
	}
}

func TestRequireBYOOwner(t *testing.T) {
	cases := []struct {
		name        string
		routingMode string
		ownerUserID string
		wantError   bool
	}{
		{name: "pool has no owner", routingMode: "pool"},
		{name: "pool rejects owner", routingMode: "pool", ownerUserID: "user-1", wantError: true},
		{name: "byo requires owner", routingMode: "byo", wantError: true},
		{name: "byo accepts owner", routingMode: "byo", ownerUserID: "user-1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireBYOOwner(tc.routingMode, tc.ownerUserID)
			if tc.wantError && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
