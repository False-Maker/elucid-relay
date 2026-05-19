package httpserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type riskRule struct {
	ID       string
	RuleType string
	Name     string
	Pattern  string
	Action   string
	Severity string
	Metadata map[string]any
}

type riskMatch struct {
	RuleID       string         `json:"rule_id"`
	RuleType     string         `json:"rule_type"`
	Name         string         `json:"name"`
	Action       string         `json:"action"`
	Severity     string         `json:"severity"`
	Target       string         `json:"target"`
	MatchedValue string         `json:"matched_value"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type riskDecision struct {
	Action  string      `json:"action"`
	Matches []riskMatch `json:"matches"`
}

func allowRiskDecision() riskDecision {
	return riskDecision{Action: "allow", Matches: []riskMatch{}}
}

func (decision riskDecision) Err() error {
	switch decision.Action {
	case "block":
		return forbidden("Request was blocked by risk controls.")
	case "throttle":
		return appError{status: http.StatusTooManyRequests, code: "risk_throttled", message: "Request was throttled by risk controls.", typ: "rate_limit_error"}
	default:
		return nil
	}
}

func (s *Server) evaluateRisk(ctx context.Context, auth apiKeyAuth, r *http.Request, requestID string, endpoint string, body []byte) (riskDecision, error) {
	rules, err := s.activeRiskRules(ctx)
	if err != nil {
		return allowRiskDecision(), err
	}
	if len(rules) == 0 {
		return allowRiskDecision(), nil
	}

	decision := allowRiskDecision()
	bodyText := string(body)
	for _, rule := range rules {
		target, matched, ok := matchRiskRule(rule, r, endpoint, body, bodyText)
		if !ok {
			continue
		}
		match := riskMatch{
			RuleID:       rule.ID,
			RuleType:     rule.RuleType,
			Name:         rule.Name,
			Action:       rule.Action,
			Severity:     rule.Severity,
			Target:       target,
			MatchedValue: truncateForStorage(matched, 500),
			Metadata:     rule.Metadata,
		}
		decision.Matches = append(decision.Matches, match)
		if riskActionRank(rule.Action) > riskActionRank(decision.Action) {
			decision.Action = rule.Action
		}
	}
	if len(decision.Matches) == 0 {
		return decision, nil
	}
	if err := s.recordRiskEvents(ctx, auth, requestID, decision); err != nil {
		return decision, err
	}
	return decision, nil
}

func (s *Server) activeRiskRules(ctx context.Context) ([]riskRule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, rule_type, name, pattern, action, severity, metadata_json::text
		FROM risk_rules
		WHERE status = 'active'
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []riskRule{}
	for rows.Next() {
		var rule riskRule
		var metadataRaw string
		if err := rows.Scan(&rule.ID, &rule.RuleType, &rule.Name, &rule.Pattern, &rule.Action, &rule.Severity, &metadataRaw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(metadataRaw), &rule.Metadata)
		if rule.Metadata == nil {
			rule.Metadata = map[string]any{}
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func matchRiskRule(rule riskRule, r *http.Request, endpoint string, body []byte, bodyText string) (string, string, bool) {
	switch rule.RuleType {
	case "sensitive_word":
		if patternMatches(rule.Pattern, bodyText) {
			return "body", rule.Pattern, true
		}
	case "ssrf_target":
		for _, candidate := range requestURLCandidates(r, body) {
			if isRiskyURL(candidate) || patternMatches(rule.Pattern, candidate) {
				return "url", candidate, true
			}
		}
	case "request_limit":
		if maxBytes, ok := metadataInt64(rule.Metadata, "max_body_bytes"); ok && int64(len(body)) > maxBytes {
			return "body_bytes", strconv.Itoa(len(body)), true
		}
		if endpointPattern := strings.TrimSpace(riskMetadataString(rule.Metadata, "endpoint")); endpointPattern != "" && patternMatches(endpointPattern, endpoint) {
			return "endpoint", endpoint, true
		}
		if patternMatches(rule.Pattern, r.URL.Path) {
			return "path", r.URL.Path, true
		}
	case "bot_protection":
		ua := r.UserAgent()
		lowerUA := strings.ToLower(ua)
		if strings.Contains(lowerUA, "bot") || strings.Contains(lowerUA, "crawler") || strings.Contains(lowerUA, "spider") || patternMatches(rule.Pattern, ua) {
			return "user_agent", ua, true
		}
	case "abuse_pattern":
		if patternMatches(rule.Pattern, bodyText) {
			return "body", rule.Pattern, true
		}
		if patternMatches(rule.Pattern, r.UserAgent()) {
			return "user_agent", r.UserAgent(), true
		}
	}
	return "", "", false
}

func (s *Server) recordRiskEvents(ctx context.Context, auth apiKeyAuth, requestID string, decision riskDecision) error {
	for _, match := range decision.Matches {
		metadata := map[string]any{
			"name":     match.Name,
			"metadata": match.Metadata,
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO risk_events (user_id, api_key_id, rule_id, request_id, rule_type, action, severity, target, matched_value, metadata_json)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)
		`, nullUUID(auth.UserID), nullUUID(auth.APIKeyID), nullUUID(match.RuleID), requestID, match.RuleType, match.Action, match.Severity, match.Target, match.MatchedValue, mustEncodeJSON(metadata)); err != nil {
			return err
		}
	}
	return nil
}

func riskDecisionSnapshot(decision riskDecision) map[string]any {
	if decision.Action == "" {
		decision.Action = "allow"
	}
	if decision.Matches == nil {
		decision.Matches = []riskMatch{}
	}
	return map[string]any{
		"action":  decision.Action,
		"matches": decision.Matches,
	}
}

func encodeRiskDecision(decision riskDecision) string {
	return mustEncodeJSON(riskDecisionSnapshot(decision))
}

func riskActionRank(action string) int {
	switch action {
	case "block":
		return 3
	case "throttle":
		return 2
	case "flag":
		return 1
	default:
		return 0
	}
}

func patternMatches(pattern string, value string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || value == "" {
		return false
	}
	if compiled, err := regexp.Compile(pattern); err == nil {
		return compiled.MatchString(value)
	}
	return strings.Contains(strings.ToLower(value), strings.ToLower(pattern))
}

func requestURLCandidates(r *http.Request, body []byte) []string {
	values := []string{}
	for _, items := range r.URL.Query() {
		values = append(values, items...)
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err == nil {
		collectURLStrings(payload, &values)
	}
	return values
}

func collectURLStrings(value any, values *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			lowerKey := strings.ToLower(key)
			if strings.Contains(lowerKey, "url") || strings.Contains(lowerKey, "uri") || strings.Contains(lowerKey, "endpoint") || strings.Contains(lowerKey, "callback") {
				if text, ok := item.(string); ok {
					*values = append(*values, text)
				}
			}
			collectURLStrings(item, values)
		}
	case []any:
		for _, item := range typed {
			collectURLStrings(item, values)
		}
	}
}

func isRiskyURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return true
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || strings.Contains(host, "metadata.google.internal") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func metadataInt64(metadata map[string]any, key string) (int64, bool) {
	switch value := metadata[key].(type) {
	case float64:
		return int64(value), value >= 0
	case int64:
		return value, value >= 0
	case int:
		return int64(value), value >= 0
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed, err == nil && parsed >= 0
	default:
		return 0, false
	}
}

func riskMetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func nullUUID(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (s *Server) adminRiskEvents(w http.ResponseWriter, r *http.Request, auth authContext) error {
	args := []any{limitFromRequest(r, 100, 500)}
	filters := []string{}
	if action := strings.TrimSpace(r.URL.Query().Get("action")); action != "" {
		args = append(args, action)
		filters = append(filters, "action = $"+strconv.Itoa(len(args)))
	}
	if userID := strings.TrimSpace(r.URL.Query().Get("user_id")); userID != "" {
		args = append(args, userID)
		filters = append(filters, "user_id::text = $"+strconv.Itoa(len(args)))
	}
	where := ""
	if len(filters) > 0 {
		where = "WHERE " + strings.Join(filters, " AND ")
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id::text, COALESCE(user_id::text, ''), COALESCE(api_key_id::text, ''), COALESCE(rule_id::text, ''),
		       request_id, rule_type, action, severity, target, matched_value, metadata_json::text, created_at
		FROM risk_events
		`+where+`
		ORDER BY created_at DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, userID, apiKeyID, ruleID, requestID, ruleType, action, severity, target, matchedValue, metadata string
		var createdAt time.Time
		if err := rows.Scan(&id, &userID, &apiKeyID, &ruleID, &requestID, &ruleType, &action, &severity, &target, &matchedValue, &metadata, &createdAt); err != nil {
			return err
		}
		items = append(items, map[string]any{
			"id":            id,
			"user_id":       userID,
			"api_key_id":    apiKeyID,
			"rule_id":       ruleID,
			"request_id":    requestID,
			"rule_type":     ruleType,
			"action":        action,
			"severity":      severity,
			"target":        target,
			"matched_value": matchedValue,
			"metadata":      jsonRaw(metadata),
			"created_at":    createdAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}
