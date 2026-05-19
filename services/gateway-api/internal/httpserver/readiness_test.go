package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/elucid/elucid-relay/services/gateway-api/internal/config"
)

func TestHealthzIsOnlyLiveness(t *testing.T) {
	server := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()

	server.healthz(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if strings.TrimSpace(recorder.Body.String()) != "ok" {
		t.Fatalf("healthz body = %q", recorder.Body.String())
	}
}

func TestReadyzReportsMissingDependencies(t *testing.T) {
	server := &Server{cfg: config.Config{AppEnv: "development", SessionTTLHours: 168, SMTPPort: 587, VaultKey: "local-development-vault-key-32!!"}}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	recorder := httptest.NewRecorder()

	server.readyz(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(recorder.Body.String(), `"database":"missing"`) || !strings.Contains(recorder.Body.String(), `"redis":"missing"`) {
		t.Fatalf("readyz body did not include dependency status: %s", recorder.Body.String())
	}
}
