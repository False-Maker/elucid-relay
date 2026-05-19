package httpserver

import "testing"

func TestScoreAccountQualityIsolatesLowQuotaAndFailures(t *testing.T) {
	result := scoreAccountQuality(accountQualityTarget{
		AccountStatus:       "active",
		ProviderType:        "openai_compatible",
		ChannelID:           "channel-1",
		LastTestStatus:      "failed",
		LastTestLatencyMS:   6200,
		RecentTestCount:     10,
		RecentFailedCount:   9,
		FailureCount:        8,
		SuccessCount:        2,
		CircuitState:        "open",
		CircuitFailureCount: 3,
		QuotaRemaining:      "0",
		QuotaLimit:          "100",
	}, 40, 70)

	if result.Decision != "isolate" || result.QualityStatus != "isolated" {
		t.Fatalf("decision=%q status=%q, want isolate/isolated", result.Decision, result.QualityStatus)
	}
	if result.QualityScore >= 40 {
		t.Fatalf("quality score = %d, want below isolation threshold", result.QualityScore)
	}
}

func TestScoreAccountQualityAllowsHealthyAccount(t *testing.T) {
	result := scoreAccountQuality(accountQualityTarget{
		AccountStatus:      "active",
		ProviderType:       "openai_compatible",
		ChannelID:          "channel-1",
		LastTestStatus:     "success",
		LastTestLatencyMS:  200,
		RecentTestCount:    10,
		RecentFailedCount:  0,
		RecentAvgLatencyMS: 220,
		SuccessCount:       20,
		FailureCount:       0,
		CircuitState:       "closed",
		QuotaRemaining:     "80",
		QuotaLimit:         "100",
	}, 40, 70)

	if result.Decision != "allow" || result.QualityStatus != "healthy" {
		t.Fatalf("decision=%q status=%q, want allow/healthy", result.Decision, result.QualityStatus)
	}
	if result.QualityScore < 70 {
		t.Fatalf("quality score = %d, want healthy score", result.QualityScore)
	}
}
