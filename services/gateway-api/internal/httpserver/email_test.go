package httpserver

import (
	"strings"
	"testing"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/config"
)

func TestSecurityActionLinkUsesFragment(t *testing.T) {
	server := &Server{cfg: config.Config{PortalBaseURL: "https://portal.example.com/"}}
	link := server.securityActionLink("reset_token", "pwd_test/token")
	if link != "https://portal.example.com/#reset_token=pwd_test%2Ftoken" {
		t.Fatalf("link = %q", link)
	}
}

func TestEmailMessageSanitizesSubject(t *testing.T) {
	message := emailMessage("Relay <noreply@example.com>", "user@example.com", "Reset\r\nBCC: bad@example.com", "Hello\nWorld")
	if strings.Contains(message, "\r\nBCC: bad@example.com") {
		t.Fatalf("message subject allowed header injection: %q", message)
	}
	if !strings.Contains(message, "Hello\r\nWorld") {
		t.Fatalf("message did not normalize body newlines: %q", message)
	}
}
