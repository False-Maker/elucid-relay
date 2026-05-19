package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

var errInvalidResponsesSSEEvent = errors.New("invalid responses stream event")

type responsesSSEFramerReader struct {
	src         io.Reader
	pending     []byte
	output      []byte
	eof         bool
	outputItems []json.RawMessage
}

func newResponsesSSEFramerReader(src io.Reader) io.Reader {
	return &responsesSSEFramerReader{src: src}
}

func (reader *responsesSSEFramerReader) Read(dst []byte) (int, error) {
	for len(reader.output) == 0 && !reader.eof {
		buffer := make([]byte, 32*1024)
		n, err := reader.src.Read(buffer)
		if n > 0 {
			reader.pending = append(reader.pending, buffer[:n]...)
			if processErr := reader.process(false); processErr != nil {
				return 0, processErr
			}
		}
		if err != nil {
			if err == io.EOF {
				reader.eof = true
				if processErr := reader.process(true); processErr != nil {
					return 0, processErr
				}
				break
			}
			return 0, err
		}
	}
	if len(reader.output) == 0 {
		return 0, io.EOF
	}
	n := copy(dst, reader.output)
	reader.output = reader.output[n:]
	return n, nil
}

func (reader *responsesSSEFramerReader) process(final bool) error {
	reader.pending = normalizeSSELineEndings(reader.pending)
	for {
		event, rest, ok := nextResponsesSSEEvent(reader.pending)
		if !ok {
			break
		}
		reader.pending = rest
		output, err := reader.normalizeEvent(event)
		if err != nil {
			return err
		}
		reader.output = append(reader.output, output...)
	}
	if final && len(bytes.TrimSpace(reader.pending)) > 0 {
		output, err := reader.normalizeEvent(reader.pending)
		if err != nil {
			return err
		}
		reader.output = append(reader.output, output...)
		reader.pending = nil
	}
	return nil
}

func (reader *responsesSSEFramerReader) normalizeEvent(event []byte) ([]byte, error) {
	data, ok := sseEventData(event)
	if !ok || data == "[DONE]" {
		return ensureSSEEventTerminator(event), nil
	}
	trimmedData := strings.TrimSpace(data)
	if (strings.HasPrefix(trimmedData, "{") || strings.HasPrefix(trimmedData, "[")) && !json.Valid([]byte(trimmedData)) {
		return nil, errInvalidResponsesSSEEvent
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmedData), &payload); err != nil {
		return ensureSSEEventTerminator(event), nil
	}
	eventType := firstString(payload["type"], sseEventName(event))
	switch eventType {
	case "response.output_item.done":
		if raw := responseOutputItemRaw(payload); len(raw) > 0 {
			reader.outputItems = append(reader.outputItems, raw)
		}
	case "response.completed":
		if reader.patchCompletedOutput(payload) {
			encoded, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			return replaceSSEEventData(event, string(encoded)), nil
		}
	}
	return ensureSSEEventTerminator(event), nil
}

func (reader *responsesSSEFramerReader) patchCompletedOutput(payload map[string]any) bool {
	if len(reader.outputItems) == 0 {
		return false
	}
	response, ok := payload["response"].(map[string]any)
	if !ok {
		return false
	}
	if output, ok := response["output"].([]any); ok && len(output) > 0 {
		return false
	}
	items := make([]any, 0, len(reader.outputItems))
	for _, raw := range reader.outputItems {
		var item any
		if err := json.Unmarshal(raw, &item); err == nil {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return false
	}
	response["output"] = items
	return true
}

func nextResponsesSSEEvent(buffer []byte) ([]byte, []byte, bool) {
	explicitIndex, explicitLength := firstSSEEventSeparator(buffer)
	implicitIndex := -1
	if len(buffer) > 1 {
		if index := bytes.Index(buffer[1:], []byte("\nevent:")); index >= 0 {
			implicitIndex = index + 1
		}
	}
	if implicitIndex >= 0 && (explicitIndex < 0 || implicitIndex < explicitIndex) {
		split := implicitIndex
		return buffer[:split], buffer[split+1:], true
	}
	if explicitIndex >= 0 {
		return buffer[:explicitIndex], buffer[explicitIndex+explicitLength:], true
	}
	return nil, nil, false
}

func firstSSEEventSeparator(buffer []byte) (int, int) {
	separators := [][]byte{[]byte("\n\n"), []byte("\r\n\r\n"), []byte("\r\r")}
	bestIndex := -1
	bestLength := 0
	for _, separator := range separators {
		index := bytes.Index(buffer, separator)
		if index < 0 {
			continue
		}
		if bestIndex < 0 || index < bestIndex {
			bestIndex = index
			bestLength = len(separator)
		}
	}
	return bestIndex, bestLength
}

func normalizeSSELineEndings(buffer []byte) []byte {
	buffer = bytes.ReplaceAll(buffer, []byte("\r\n"), []byte("\n"))
	buffer = bytes.ReplaceAll(buffer, []byte("\r"), []byte("\n"))
	return buffer
}

func ensureSSEEventTerminator(event []byte) []byte {
	event = bytes.TrimRight(normalizeSSELineEndings(event), "\n")
	return append(append([]byte(nil), event...), []byte("\n\n")...)
}

func replaceSSEEventData(event []byte, data string) []byte {
	normalized := string(normalizeSSELineEndings(event))
	lines := strings.Split(normalized, "\n")
	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "data:") {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	for _, line := range strings.Split(data, "\n") {
		out = append(out, "data: "+line)
	}
	return []byte(strings.Join(out, "\n") + "\n\n")
}

func sseEventName(event []byte) string {
	normalized := string(normalizeSSELineEndings(event))
	for _, line := range strings.Split(normalized, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	return ""
}

func responseOutputItemRaw(payload map[string]any) json.RawMessage {
	for _, key := range []string{"item", "output_item"} {
		if value, ok := payload[key]; ok {
			raw, err := json.Marshal(value)
			if err == nil && len(raw) > 0 && string(raw) != "null" {
				return raw
			}
		}
	}
	return nil
}

func firstString(values ...any) string {
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text != "" {
			return text
		}
	}
	return ""
}

func shouldFrameResponsesSSE(route routeInfo, endpoint string) bool {
	if endpoint != "responses" {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(route.ProviderType))
	return provider == "" ||
		provider == "openai" ||
		provider == "openai_compatible" ||
		provider == "codex" ||
		provider == "codex_cli" ||
		provider == "github_copilot" ||
		provider == "copilot"
}
