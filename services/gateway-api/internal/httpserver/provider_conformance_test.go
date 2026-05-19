package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const replayRedactedValue = "[REDACTED]"

type oauthOfficialCaptureFixture struct {
	Name     string `json:"name"`
	Recorder struct {
		Requests []oauthOfficialCapturedRequest `json:"requests"`
	} `json:"recorder"`
}

type oauthOfficialCaptureManifest struct {
	Fixtures []oauthOfficialCaptureManifestEntry `json:"fixtures"`
}

type oauthOfficialCaptureManifestEntry struct {
	Provider      string   `json:"provider"`
	File          string   `json:"file"`
	MinRequests   int      `json:"min_requests"`
	RequiredPaths []string `json:"required_paths"`
}

type oauthOfficialCapturedRequest struct {
	Index            int                     `json:"index"`
	Method           string                  `json:"method"`
	URL              string                  `json:"url"`
	Path             string                  `json:"path"`
	Query            map[string]string       `json:"query"`
	Headers          map[string]string       `json:"headers"`
	BodyText         string                  `json:"bodyText"`
	JSONBody         any                     `json:"jsonBody"`
	RawBodyBytes     int                     `json:"rawBodyBytes"`
	DecodedBodyBytes int                     `json:"decodedBodyBytes"`
	WebSocket        *oauthOfficialWebSocket `json:"websocket"`
}

type oauthOfficialWebSocket struct {
	Upgrade     bool   `json:"upgrade"`
	ParentIndex int    `json:"parent_index"`
	Direction   string `json:"direction"`
	Opcode      int    `json:"opcode"`
	Fin         bool   `json:"fin"`
}

type requestReplaySnapshot struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Header map[string]string `json:"header"`
	Body   string            `json:"body,omitempty"`
}

func TestCodexOfficialRequestReplayConformance(t *testing.T) {
	fixture := loadOAuthOfficialCaptureFixture(t, "codex.json")
	if len(fixture.Recorder.Requests) < 3 {
		t.Fatalf("codex capture fixture has %d request(s), want at least 3", len(fixture.Recorder.Requests))
	}

	ignoredHeaders := defaultReplayIgnoredHeaders()

	t.Run("models", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		route := routeInfo{
			BaseURL:       "https://chatgpt.test",
			ProviderType:  "codex_compatible",
			AuthMode:      "codex_cli",
			TokenProvider: "openai_codex",
			AuthScheme:    "bearer",
			APIKey:        "cxtok",
			TokenSubject:  "acc-capture",
			TokenMetadata: map[string]any{
				"client_version": "0.0.0",
			},
		}

		upstream, cancel, err := openaiCompatibleAdapter{}.PrepareRequest(req, route, nil)
		if err != nil {
			t.Fatalf("PrepareRequest returned error: %v", err)
		}
		defer cancel()

		got := normalizePreparedRequestReplay(t, upstream, ignoredHeaders)
		want := normalizeCapturedRequestReplay(fixture.Recorder.Requests[0], ignoredHeaders)
		assertReplaySnapshot(t, got, want)
	})

	t.Run("websocket", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/responses?session_id=019dfd51-fc6d-7511-b09c-f2076d52883c", nil)
		route := routeInfo{
			BaseURL:       "https://chatgpt.test",
			ProviderType:  "codex_compatible",
			AuthMode:      "codex_cli",
			TokenProvider: "openai_codex",
			AuthScheme:    "bearer",
			APIKey:        "cxtok",
			TokenSubject:  "acc-capture",
			UpstreamModel: "gpt-5.1-codex-max",
			TokenMetadata: map[string]any{
				"client_version":               "0.0.0",
				"installation_id":              "ad360871-7ade-4554-a811-692cd8faf2ce",
				"thread_source":                "user",
				"supports_reasoning_summaries": true,
			},
		}

		upstreamURL, headers, dialer, err := codexCompatibleAdapter{}.PrepareWebSocket(req, route)
		if err != nil {
			t.Fatalf("PrepareWebSocket returned error: %v", err)
		}
		if dialer == nil {
			t.Fatal("PrepareWebSocket returned nil dialer")
		}

		got := normalizePreparedWebSocketReplay(upstreamURL, headers, ignoredHeaders)
		want := normalizeCapturedRequestReplay(fixture.Recorder.Requests[1], ignoredHeaders)
		assertReplaySnapshot(t, got, want)

		frame := []byte(fixture.Recorder.Requests[2].BodyText)
		rewritten := rewriteCodexOfficialWebSocketFrame(req, route, frame)
		wantBody := canonicalJSONTextFromAny(t, fixture.Recorder.Requests[2].JSONBody)
		gotBody := canonicalJSONTextFromBytes(t, rewritten)
		if gotBody != wantBody {
			t.Fatalf("websocket frame body mismatch:\n%s", diffStrings("got", gotBody, "want", wantBody))
		}
	})
}

func TestGeminiOfficialRequestReplayConformance(t *testing.T) {
	fixture := loadOAuthOfficialCaptureFixture(t, "gemini.json")
	if len(fixture.Recorder.Requests) != 2 {
		t.Fatalf("gemini capture fixture has %d request(s), want 2", len(fixture.Recorder.Requests))
	}

	route := routeInfo{
		BaseURL:       "https://cloudcode-pa.googleapis.com",
		ProviderType:  "gemini_cli",
		APIKey:        "gmtok",
		TokenProvider: "google_gemini",
		AuthScheme:    "bearer",
		TokenMetadata: map[string]any{
			"client_version": "0.42.0-nightly.20260428.g59b2dea0e",
			"project_id":     "gemini-capture-project",
			"session_id":     "gemini-capture-session",
			"user_prompt_id": "gemini-capture-prompt",
			"platform":       "linux",
			"arch":           "x64",
		},
	}
	ignoredHeaders := defaultReplayIgnoredHeaders()

	for _, capture := range fixture.Recorder.Requests {
		capture := capture
		t.Run(capture.Path, func(t *testing.T) {
			stream := strings.Contains(capture.Path, ":streamGenerateContent")
			body := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"Say ok."}],"max_tokens":8,"stream":` + fmt.Sprint(stream) + `}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			upstream, cancel, err := geminiCLIAdapter{}.PrepareRequest(req, route, body)
			if err != nil {
				t.Fatalf("PrepareRequest returned error: %v", err)
			}
			defer cancel()

			got := normalizePreparedRequestReplay(t, upstream, ignoredHeaders)
			want := normalizeCapturedRequestReplay(capture, ignoredHeaders)
			assertReplaySnapshot(t, got, want)
		})
	}
}

func TestGitHubCopilotOfficialRequestReplayConformance(t *testing.T) {
	fixture := loadOAuthOfficialCaptureFixture(t, "github-copilot.json")
	if len(fixture.Recorder.Requests) != 5 {
		t.Fatalf("github-copilot capture fixture has %d request(s), want 5", len(fixture.Recorder.Requests))
	}

	route := routeInfo{
		BaseURL:       "https://api.githubcopilot.com",
		ProviderType:  "github_copilot",
		APIKey:        "ghcptok",
		TokenProvider: "github_copilot",
		AuthScheme:    "bearer",
		TokenMetadata: map[string]any{
			"client_version": "0.44.0",
			"vscode_version": "1.109.3",
			"api_version":    "2025-05-01",
		},
	}
	ignoredHeaders := defaultReplayIgnoredHeaders()

	for _, capture := range fixture.Recorder.Requests {
		capture := capture
		t.Run(fmt.Sprintf("%s %s", capture.Method, capture.Path), func(t *testing.T) {
			relayPath := githubCopilotRelayPath(capture.Path)
			var body []byte
			if capture.JSONBody != nil {
				body = []byte(canonicalJSONTextFromAny(t, capture.JSONBody))
			}
			req := httptest.NewRequest(capture.Method, relayPath, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if accept := capture.Headers["accept"]; accept != "" {
				req.Header.Set("Accept", accept)
			}
			req = req.WithContext(context.WithValue(req.Context(), requestIDKey, capture.Headers["x-request-id"]))

			upstream, cancel, err := githubCopilotAdapter{}.PrepareRequest(req, route, body)
			if err != nil {
				t.Fatalf("PrepareRequest returned error: %v", err)
			}
			defer cancel()

			got := normalizePreparedRequestReplay(t, upstream, ignoredHeaders)
			want := normalizeCapturedRequestReplay(capture, ignoredHeaders)
			assertReplaySnapshot(t, got, want)
		})
	}
}

func TestClaudeOfficialRequestReplayConformance(t *testing.T) {
	fixture := loadOAuthOfficialCaptureFixture(t, "claude.json")
	if len(fixture.Recorder.Requests) != 1 {
		t.Fatalf("claude capture fixture has %d request(s), want 1", len(fixture.Recorder.Requests))
	}

	capture := fixture.Recorder.Requests[0]
	body := []byte(canonicalJSONTextFromAny(t, capture.JSONBody))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", capture.Headers["x-claude-code-session-id"])
	route := routeInfo{
		BaseURL:       "https://api.anthropic.com",
		ProviderType:  "anthropic",
		AuthMode:      "claude_cli",
		TokenProvider: "anthropic_claude",
		APIKey:        "cltok",
		AuthScheme:    "bearer",
	}

	upstream, cancel, err := anthropicAdapter{}.PrepareRequest(req, route, body)
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	defer cancel()

	ignoredHeaders := defaultReplayIgnoredHeaders()
	got := normalizePreparedRequestReplay(t, upstream, ignoredHeaders)
	want := normalizeCapturedRequestReplay(capture, ignoredHeaders)
	assertReplaySnapshot(t, got, want)
}

func TestOAuthOfficialCaptureFixtureManifestCoverage(t *testing.T) {
	manifest := loadOAuthOfficialCaptureManifest(t)
	if len(manifest.Fixtures) == 0 {
		t.Fatal("oauth official capture manifest has no fixtures")
	}

	seenFiles := map[string]bool{"manifest.json": true}
	for _, entry := range manifest.Fixtures {
		entry := entry
		t.Run(entry.Provider, func(t *testing.T) {
			if strings.TrimSpace(entry.Provider) == "" {
				t.Fatal("manifest provider is empty")
			}
			if strings.TrimSpace(entry.File) == "" {
				t.Fatal("manifest file is empty")
			}
			seenFiles[entry.File] = true
			fixture := loadOAuthOfficialCaptureFixture(t, entry.File)
			if len(fixture.Recorder.Requests) < entry.MinRequests {
				t.Fatalf("%s has %d request(s), want at least %d", entry.File, len(fixture.Recorder.Requests), entry.MinRequests)
			}
			paths := map[string]bool{}
			for _, request := range fixture.Recorder.Requests {
				if strings.TrimSpace(request.Path) == "" {
					t.Fatalf("%s request %d has empty path", entry.File, request.Index)
				}
				paths[request.Path] = true
			}
			for _, requiredPath := range entry.RequiredPaths {
				if !paths[requiredPath] {
					t.Fatalf("%s missing required path %q", entry.File, requiredPath)
				}
			}
		})
	}

	files, err := os.ReadDir(filepath.Join("testdata", "oauth-official"))
	if err != nil {
		t.Fatalf("read oauth-official fixture dir: %v", err)
	}
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		if !seenFiles[file.Name()] {
			t.Fatalf("fixture file %s is not listed in manifest", file.Name())
		}
	}
}

func defaultReplayIgnoredHeaders() map[string]struct{} {
	return map[string]struct{}{
		"accept-encoding":          {},
		"accept-language":          {},
		"connection":               {},
		"content-length":           {},
		"host":                     {},
		"sec-fetch-mode":           {},
		"sec-websocket-extensions": {},
		"sec-websocket-key":        {},
		"sec-websocket-version":    {},
		"upgrade":                  {},
	}
}

func githubCopilotRelayPath(capturePath string) string {
	if strings.HasPrefix(capturePath, "/v1/") {
		return capturePath
	}
	return "/v1" + capturePath
}

func loadOAuthOfficialCaptureFixture(t *testing.T, name string) oauthOfficialCaptureFixture {
	t.Helper()

	path := filepath.Join("testdata", "oauth-official", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture fixture %s: %v", path, err)
	}
	var fixture oauthOfficialCaptureFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode capture fixture %s: %v", path, err)
	}
	return fixture
}

func loadOAuthOfficialCaptureManifest(t *testing.T) oauthOfficialCaptureManifest {
	t.Helper()

	path := filepath.Join("testdata", "oauth-official", "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture fixture manifest %s: %v", path, err)
	}
	var manifest oauthOfficialCaptureManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode capture fixture manifest %s: %v", path, err)
	}
	return manifest
}

func normalizePreparedRequestReplay(t *testing.T, req *http.Request, ignoredHeaders map[string]struct{}) requestReplaySnapshot {
	t.Helper()
	if req == nil {
		return requestReplaySnapshot{}
	}
	body := []byte(nil)
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		_ = req.Body.Close()
	}
	return requestReplaySnapshot{
		Method: req.Method,
		Path:   req.URL.Path,
		Query:  normalizeReplayQuery(req.URL.Query()),
		Header: normalizeReplayHeaders(req.Header, ignoredHeaders),
		Body:   normalizeReplayBody(body),
	}
}

func normalizePreparedWebSocketReplay(upstreamURL string, headers http.Header, ignoredHeaders map[string]struct{}) requestReplaySnapshot {
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		return requestReplaySnapshot{}
	}
	return requestReplaySnapshot{
		Method: http.MethodGet,
		Path:   parsed.Path,
		Query:  normalizeReplayQuery(parsed.Query()),
		Header: normalizeReplayHeaders(headers, ignoredHeaders),
	}
}

func normalizeCapturedRequestReplay(request oauthOfficialCapturedRequest, ignoredHeaders map[string]struct{}) requestReplaySnapshot {
	body := normalizeReplayBody([]byte(request.BodyText))
	if request.JSONBody != nil {
		body = canonicalJSONTextFromAny(nil, request.JSONBody)
	}
	return requestReplaySnapshot{
		Method: request.Method,
		Path:   request.Path,
		Query:  normalizeReplayQueryMap(request.Query),
		Header: normalizeReplayHeadersFromCaptured(request.Headers, ignoredHeaders),
		Body:   body,
	}
}

func assertReplaySnapshot(t *testing.T, got requestReplaySnapshot, want requestReplaySnapshot) {
	t.Helper()

	if reflect.DeepEqual(got, want) {
		return
	}
	t.Fatalf("request replay mismatch:\n%s", diffValues(got, want))
}

func normalizeReplayQuery(values url.Values) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(values))
	for key, items := range values {
		if len(items) == 0 {
			out[key] = ""
			continue
		}
		out[key] = items[len(items)-1]
	}
	return out
}

func normalizeReplayQueryMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = strings.TrimSpace(value)
	}
	return out
}

func normalizeReplayHeaders(headers http.Header, ignoredHeaders map[string]struct{}) map[string]string {
	if len(headers) == 0 {
		return map[string]string{}
	}
	out := map[string]string{}
	for key, values := range headers {
		canonicalKey := strings.ToLower(strings.TrimSpace(key))
		if canonicalKey == "" {
			continue
		}
		if _, ok := ignoredHeaders[canonicalKey]; ok {
			continue
		}
		out[canonicalKey] = normalizeReplayHeaderValue(canonicalKey, strings.Join(values, ","))
	}
	return out
}

func normalizeReplayHeadersFromCaptured(headers map[string]string, ignoredHeaders map[string]struct{}) map[string]string {
	if len(headers) == 0 {
		return map[string]string{}
	}
	out := map[string]string{}
	for key, value := range headers {
		canonicalKey := strings.ToLower(strings.TrimSpace(key))
		if canonicalKey == "" {
			continue
		}
		if _, ok := ignoredHeaders[canonicalKey]; ok {
			continue
		}
		out[canonicalKey] = normalizeReplayHeaderValue(canonicalKey, value)
	}
	return out
}

func normalizeReplayHeaderValue(key string, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if isReplaySensitiveHeader(key) {
		return replayRedactedValue
	}
	if json.Valid([]byte(value)) {
		return canonicalJSONTextFromBytes(nil, []byte(value))
	}
	return value
}

func isReplaySensitiveHeader(key string) bool {
	switch key {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "apikey", "x-claude-code-ide-authorization":
		return true
	}
	return strings.Contains(key, "access_token") ||
		strings.Contains(key, "refresh_token") ||
		strings.Contains(key, "id_token") ||
		strings.Contains(key, "api_key") ||
		strings.Contains(key, "client_secret") ||
		strings.Contains(key, "token")
}

func normalizeReplayBody(body []byte) string {
	return canonicalJSONTextFromBytes(nil, body)
}

func canonicalJSONTextFromAny(t *testing.T, value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		if t != nil {
			t.Helper()
			t.Fatalf("canonical JSON marshal failed: %v", err)
		}
		return ""
	}
	return string(encoded)
}

func canonicalJSONTextFromBytes(t *testing.T, body []byte) string {
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) == 0 {
		return ""
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		if t != nil {
			t.Helper()
			t.Fatalf("canonical JSON decode failed: %v", err)
		}
		return string(body)
	}
	return canonicalJSONTextFromAny(t, parsed)
}

func diffValues(got any, want any) string {
	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	return fmt.Sprintf("got:\n%s\nwant:\n%s", gotJSON, wantJSON)
}

func diffStrings(gotLabel string, got string, wantLabel string, want string) string {
	return fmt.Sprintf("%s:\n%s\n%s:\n%s", gotLabel, got, wantLabel, want)
}
