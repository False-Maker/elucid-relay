package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type routeAffinityMatch struct {
	Key                string
	TTLSeconds         int
	RuleName           string
	SkipRetryOnFailure bool
}

func (s *Server) routeAffinityMatchFromRequest(ctx context.Context, r *http.Request, body []byte, contentType string, model string, endpoint string) routeAffinityMatch {
	settings := s.reverseProxySettingsOrDefault(ctx)
	for _, rule := range settings.AffinityRules {
		if !affinityRuleMatchesRequest(rule, r, model, endpoint) {
			continue
		}
		if key := affinityRuleKeyFromRequest(rule, r, body, contentType, model, endpoint); key != "" {
			return routeAffinityMatch{Key: key, TTLSeconds: rule.TTLSeconds, RuleName: rule.Name, SkipRetryOnFailure: rule.SkipRetryOnFailure}
		}
	}
	return routeAffinityMatch{}
}

func affinityRuleMatchesRequest(rule affinityRuleSettings, r *http.Request, model string, endpoint string) bool {
	if r == nil {
		return false
	}
	if !matchesAnyRegex(rule.PathRegex, r.URL.Path) {
		return false
	}
	if !matchesAnyRegex(rule.ModelRegex, model) {
		return false
	}
	return true
}

func matchesAnyRegex(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(value) {
			return true
		}
	}
	return false
}

func affinityRuleKeyFromRequest(rule affinityRuleSettings, r *http.Request, body []byte, contentType string, model string, endpoint string) string {
	prefixParts := []string{}
	if rule.IncludeModelName {
		if model := normalizeRouteAffinityKey(model); model != "" {
			prefixParts = append(prefixParts, model)
		}
	}
	if rule.IncludeEndpoint {
		prefixParts = append(prefixParts, endpoint)
	}
	for _, header := range rule.HeaderKeys {
		if key := normalizeRouteAffinityKey(r.Header.Get(header)); key != "" {
			return joinAffinityRuleKey(prefixParts, key)
		}
	}
	for _, query := range rule.QueryKeys {
		if key := normalizeRouteAffinityKey(r.URL.Query().Get(query)); key != "" {
			return joinAffinityRuleKey(prefixParts, key)
		}
	}
	payload, ok := decodeAffinityRuleJSON(body, contentType)
	if !ok {
		return ""
	}
	for _, path := range rule.JSONPaths {
		if key := normalizeRouteAffinityKey(jsonPathString(payload, path)); key != "" {
			return joinAffinityRuleKey(prefixParts, key)
		}
	}
	if len(rule.JSONKeys) > 0 {
		if key := findNamedJSONKey(payload, stringSet(rule.JSONKeys), 0); key != "" {
			return joinAffinityRuleKey(prefixParts, key)
		}
	}
	return ""
}

func joinAffinityRuleKey(prefixParts []string, key string) string {
	if len(prefixParts) == 0 {
		return key
	}
	parts := append([]string{}, prefixParts...)
	parts = append(parts, key)
	return strings.Join(parts, ":")
}

func decodeAffinityRuleJSON(body []byte, contentType string) (any, bool) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return nil, false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, false
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func jsonPathString(payload any, path string) string {
	value, ok := jsonPathValue(payload, strings.Split(strings.TrimSpace(path), "."))
	if !ok {
		return ""
	}
	return metadataText(value)
}

func jsonPathValue(value any, parts []string) (any, bool) {
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return value, true
	}
	switch typed := value.(type) {
	case map[string]any:
		next, ok := typed[parts[0]]
		if !ok {
			return nil, false
		}
		return jsonPathValue(next, parts[1:])
	case []any:
		index, err := strconv.Atoi(parts[0])
		if err != nil || index < 0 || index >= len(typed) {
			return nil, false
		}
		return jsonPathValue(typed[index], parts[1:])
	default:
		return nil, false
	}
}

func findNamedJSONKey(value any, keys map[string]struct{}, depth int) string {
	if depth > 16 {
		return ""
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, ok := keys[strings.ToLower(key)]; ok {
				if text := normalizeRouteAffinityKey(metadataText(child)); text != "" {
					return text
				}
			}
		}
		for _, child := range typed {
			if key := findNamedJSONKey(child, keys, depth+1); key != "" {
				return key
			}
		}
	case []any:
		for _, child := range typed {
			if key := findNamedJSONKey(child, keys, depth+1); key != "" {
				return key
			}
		}
	}
	return ""
}

func applyAffinityRuleHeaderPassthrough(original *http.Request, header http.Header, route routeInfo, rules []affinityRuleSettings) {
	if original == nil || header == nil {
		return
	}
	for _, rule := range rules {
		if len(rule.HeaderPassthrough) == 0 || !affinityRuleMatchesRequest(rule, original, route.UpstreamModel, endpointFromPath(original.URL.Path)) {
			continue
		}
		for _, name := range rule.HeaderPassthrough {
			values := original.Header.Values(name)
			if len(values) == 0 {
				continue
			}
			header.Del(name)
			for _, value := range values {
				header.Add(name, value)
			}
		}
	}
}

func (s *Server) applyReverseProxyParamOverrides(ctx context.Context, r *http.Request, route routeInfo, body []byte) ([]byte, error) {
	var err error
	body, err = applyRouteParamOverrideOperations(r, route, body)
	if err != nil {
		return nil, err
	}
	settings := s.reverseProxySettingsOrDefault(ctx)
	if len(settings.AffinityRules) == 0 {
		return body, nil
	}
	var payload map[string]any
	changed := false
	for _, rule := range settings.AffinityRules {
		if len(rule.ParamOverrides) == 0 || !affinityRuleMatchesRequest(rule, r, route.UpstreamModel, endpointFromPath(r.URL.Path)) {
			continue
		}
		if payload == nil {
			if decoded, ok := decodeAffinityRuleJSON(body, r.Header.Get("Content-Type")); ok {
				if object, ok := decoded.(map[string]any); ok {
					payload = object
				}
			}
			if payload == nil {
				return body, nil
			}
		}
		for key, value := range rule.ParamOverrides {
			trimmedKey := strings.TrimSpace(key)
			if strings.EqualFold(trimmedKey, "model") {
				continue
			}
			if setJSONPathValue(payload, strings.Split(trimmedKey, "."), value) {
				changed = true
			}
		}
	}
	if !changed {
		return body, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body, nil
	}
	return encoded, nil
}

func setJSONPathValue(root map[string]any, parts []string, value any) bool {
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return false
	}
	current := root
	for _, part := range parts[:len(parts)-1] {
		part = strings.TrimSpace(part)
		if part == "" {
			return false
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	if last == "" {
		return false
	}
	current[last] = value
	return true
}
