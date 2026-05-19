package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
)

const defaultReplayGuardMaxItems = 24

func (s *Server) applyNorthboundReplayGuard(ctx context.Context, endpoint string, body []byte, contentType string) []byte {
	settings := s.reverseProxySettingsOrDefault(ctx)
	if !settings.ReplayGuardEnabled {
		return body
	}
	maxItems := settings.ReplayGuardMaxItems
	if maxItems <= 0 {
		maxItems = defaultReplayGuardMaxItems
	}
	rewritten, changed := applyReplayGuard(endpoint, body, contentType, maxItems)
	if !changed {
		return body
	}
	return rewritten
}

func applyReplayGuard(endpoint string, body []byte, contentType string, maxItems int) ([]byte, bool) {
	if maxItems <= 0 || !replayGuardEligibleEndpoint(endpoint) || !isJSONContentType(contentType) || len(bytes.TrimSpace(body)) == 0 {
		return body, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	payload := map[string]any{}
	if err := decoder.Decode(&payload); err != nil {
		return body, false
	}
	changed := false
	for _, key := range []string{"messages", "input"} {
		items, ok := arrayValue(payload[key])
		if !ok {
			continue
		}
		trimmed, trimmedChanged := trimReplayItems(items, maxItems)
		if trimmedChanged {
			payload[key] = trimmed
			changed = true
		}
	}
	if !changed {
		return body, false
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return rewritten, true
}

func replayGuardEligibleEndpoint(endpoint string) bool {
	switch strings.ToLower(strings.TrimSpace(endpoint)) {
	case "chat", "messages", "responses":
		return true
	default:
		return false
	}
}

func trimReplayItems(items []any, maxItems int) ([]any, bool) {
	if maxItems <= 0 || len(items) <= maxItems {
		return items, false
	}
	start := len(items) - maxItems
	for start > 0 && replayBoundaryNeedsPrevious(items[start]) {
		start--
	}
	for start > 0 && replayBoundaryPairNeedsPrevious(items[start-1], items[start]) {
		start--
	}
	if start <= 0 {
		return items, false
	}
	trimmed := make([]any, 0, len(items)-start)
	trimmed = append(trimmed, items[start:]...)
	return trimmed, true
}

func replayBoundaryNeedsPrevious(item any) bool {
	object, ok := item.(map[string]any)
	if !ok {
		return false
	}
	itemType := strings.ToLower(strings.TrimSpace(metadataText(object["type"])))
	role := strings.ToLower(strings.TrimSpace(metadataText(object["role"])))
	if role == "tool" || itemType == "tool_result" || itemType == "function_call_output" {
		return true
	}
	return valueContainsReplayToolResult(object["content"])
}

func replayBoundaryPairNeedsPrevious(previous any, current any) bool {
	if !replayBoundaryNeedsPrevious(current) {
		return false
	}
	return valueContainsReplayToolCall(previous)
}

func valueContainsReplayToolCall(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		itemType := strings.ToLower(strings.TrimSpace(metadataText(typed["type"])))
		if itemType == "tool_use" || itemType == "function_call" || itemType == "tool_call" {
			return true
		}
		if _, ok := typed["tool_calls"]; ok {
			return true
		}
		if _, ok := typed["function_call"]; ok {
			return true
		}
		for _, child := range typed {
			if valueContainsReplayToolCall(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if valueContainsReplayToolCall(child) {
				return true
			}
		}
	}
	return false
}

func valueContainsReplayToolResult(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		itemType := strings.ToLower(strings.TrimSpace(metadataText(typed["type"])))
		if itemType == "tool_result" || itemType == "function_call_output" {
			return true
		}
		for _, child := range typed {
			if valueContainsReplayToolResult(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if valueContainsReplayToolResult(child) {
				return true
			}
		}
	}
	return false
}

func routeDigestAffinityKeyFromRequest(r *http.Request, endpoint string, body []byte, contentType string) string {
	if r == nil || !replayGuardEligibleEndpoint(endpoint) || !isJSONContentType(contentType) || len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	payload := map[string]any{}
	if err := decoder.Decode(&payload); err != nil {
		return ""
	}
	if _, ok := payload["messages"]; !ok {
		if _, ok := payload["input"]; !ok {
			return ""
		}
	}
	canonical := digestCanonicalPayload(payload)
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(append([]byte(strings.ToLower(strings.TrimSpace(endpoint))+"\n"), encoded...))
	return "digest:" + hex.EncodeToString(hash[:])
}

func digestCanonicalPayload(payload map[string]any) map[string]any {
	canonical := map[string]any{}
	for key, value := range payload {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if digestVolatileTopLevelField(normalizedKey) {
			continue
		}
		switch normalizedKey {
		case "model", "messages", "input", "system", "metadata":
			canonical[normalizedKey] = digestCanonicalValue(value, 0)
		}
	}
	return canonical
}

func digestCanonicalValue(value any, depth int) any {
	if depth > 16 {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		canonical := map[string]any{}
		for key, child := range typed {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			if digestVolatileNestedField(normalizedKey) {
				continue
			}
			canonical[normalizedKey] = digestCanonicalValue(child, depth+1)
		}
		return canonical
	case []any:
		items := make([]any, 0, len(typed))
		for _, child := range typed {
			items = append(items, digestCanonicalValue(child, depth+1))
		}
		return items
	default:
		return value
	}
}

func digestVolatileTopLevelField(key string) bool {
	switch key {
	case "stream", "temperature", "top_p", "n", "max_tokens", "max_completion_tokens", "max_output_tokens", "presence_penalty", "frequency_penalty", "service_tier":
		return true
	default:
		return digestVolatileNestedField(key)
	}
}

func digestVolatileNestedField(key string) bool {
	switch key {
	case "id", "created", "created_at", "updated_at", "request_id", "client_request_id", "traceparent", "tracestate":
		return true
	default:
		return false
	}
}

func isJSONContentType(contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") || mediaType == ""
}
