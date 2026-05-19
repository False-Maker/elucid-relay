package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFinalizeProxyQualityReportScoreAndGrade(t *testing.T) {
	report := &proxyQualityReport{
		PassedCount:    2,
		WarnCount:      1,
		FailedCount:    1,
		ChallengeCount: 1,
	}

	finalizeProxyQualityReport(report)

	if report.Score != 38 {
		t.Fatalf("score = %d, want 38", report.Score)
	}
	if report.Grade != "F" {
		t.Fatalf("grade = %q, want F", report.Grade)
	}
	if report.QualityStatus != "challenge" {
		t.Fatalf("quality status = %q, want challenge", report.QualityStatus)
	}
}

func TestRunProxyQualityTargetAllowedUnauthorizedWarns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	item := runProxyQualityTarget(context.Background(), server.Client(), proxyQualityTarget{
		Target: "openai",
		URL:    server.URL,
		Method: http.MethodGet,
		AllowedStatuses: map[int]bool{
			http.StatusUnauthorized: true,
		},
	})

	if item.Status != "warn" {
		t.Fatalf("status = %q, want warn", item.Status)
	}
	if item.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("http status = %d, want 401", item.HTTPStatus)
	}
}

func TestRunProxyQualityTargetCloudflareChallenge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cf-Ray", "test-ray")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html>Cloudflare challenge</html>`))
	}))
	defer server.Close()

	item := runProxyQualityTarget(context.Background(), server.Client(), proxyQualityTarget{
		Target: "openai",
		URL:    server.URL,
		Method: http.MethodGet,
	})

	if item.Status != "challenge" {
		t.Fatalf("status = %q, want challenge", item.Status)
	}
	if item.CFRay != "test-ray" {
		t.Fatalf("cf ray = %q, want test-ray", item.CFRay)
	}
}

func TestProxyQualityBaseConnectivityWarnStillPasses(t *testing.T) {
	report := &proxyQualityReport{
		Items: []proxyQualityItem{
			{Target: "base_connectivity", Status: "warn", Message: "ip probe failed"},
		},
		WarnCount: 1,
	}

	finalizeProxyQualityReport(report)

	if !proxyQualityBaseConnectivityPass(report) {
		t.Fatalf("base connectivity warn should count as a pass for test status")
	}
	if report.QualityStatus != "warn" {
		t.Fatalf("quality status = %q, want warn", report.QualityStatus)
	}
}

func TestProxyQualityIssueSummaryShowsTargetAndReason(t *testing.T) {
	report := &proxyQualityReport{
		Items: []proxyQualityItem{
			{Target: "openai", Status: "fail", HTTPStatus: http.StatusForbidden, Message: "非预期状态码: 403"},
			{Target: "gemini", Status: "warn", HTTPStatus: http.StatusUnauthorized, Message: "目标可达，返回 HTTP 401"},
		},
	}

	summary := proxyQualityIssueSummary(report)

	if summary != "OpenAI 失败：非预期状态码: 403" {
		t.Fatalf("summary = %q", summary)
	}
}

func TestProxyQualityPrimaryErrorKind(t *testing.T) {
	report := &proxyQualityReport{
		Items: []proxyQualityItem{
			{Target: "openai", Status: "fail", ErrorKind: "unexpected_eof", Message: "unexpected EOF"},
			{Target: "gemini", Status: "warn", ErrorKind: "timeout", Message: "timeout"},
		},
	}

	if got := proxyQualityPrimaryErrorKind(report); got != "unexpected_eof" {
		t.Fatalf("primary error kind = %q", got)
	}
}
