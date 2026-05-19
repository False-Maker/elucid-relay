package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type antigravityAdapter struct{}
type kiroAdapter struct{}
type windsurfCodeiumAdapter struct{}

func (adapter antigravityAdapter) PrepareRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	if isGeminiCLIChatCompletionRequest(original) {
		return prepareAntigravityChatRequest(original, route, body)
	}
	return prepareProviderRequest(original, route, body, func(req *http.Request) {
		applyAntigravityHeaders(req.Header, original, route, body)
	})
}

func (adapter antigravityAdapter) PrepareWebSocket(original *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error) {
	return prepareProviderWebSocket(original, route, func(header http.Header) {
		applyAntigravityHeaders(header, original, route, nil)
	})
}

func (adapter antigravityAdapter) ParseUsage(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	return parseGeminiCLIUsageResult(endpoint, requestBody, responseBody)
}

func (adapter antigravityAdapter) ParseStreamEvent(endpoint string, event []byte, acc *streamMeteringAccumulator) {
	parseGeminiCLIStreamUsage(event, acc)
}

func (adapter antigravityAdapter) TransformHTTPResponse(route routeInfo, endpoint string, requestBody []byte, status int, header http.Header, responseBody []byte) (int, http.Header, []byte) {
	return geminiCLIAdapter{}.TransformHTTPResponse(route, endpoint, requestBody, status, header, responseBody)
}

func (adapter antigravityAdapter) NewSSETransformer(route routeInfo, endpoint string, requestBody []byte) providerSSEEventTransformer {
	return geminiCLIAdapter{}.NewSSETransformer(route, endpoint, requestBody)
}

func prepareAntigravityChatRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	outboundBody, err := antigravityOpenAIChatRequestBody(original, route, body)
	if err != nil {
		return nil, func() {}, err
	}
	baseURL, err := url.Parse(strings.TrimRight(route.BaseURL, "/"))
	if err != nil {
		return nil, func() {}, err
	}
	streaming := isStreamingRequest(body, original.Header.Get("Content-Type"), original.Header.Get("Accept"))
	method := "generateContent"
	if streaming {
		method = "streamGenerateContent"
	}
	baseURL.Path = geminiCLIMethodPath(baseURL.Path, method)
	baseURL.RawPath = ""
	query := baseURL.Query()
	if streaming {
		query.Set("alt", "sse")
	}
	baseURL.RawQuery = query.Encode()

	timeout := time.Duration(route.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(original.Context(), timeout)
	req, err := http.NewRequestWithContext(ctx, original.Method, baseURL.String(), bytes.NewReader(outboundBody))
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	copyUpstreamRequestHeaders(req.Header, original.Header)
	applyAntigravityHeaders(req.Header, original, route, outboundBody)
	req.Header.Set("Content-Type", "application/json")
	if streaming {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("Accept-Encoding", "identity")
	if err := applyRouteHeaderRules(req, original, route); err != nil {
		cancel()
		return nil, func() {}, err
	}
	return req, cancel, nil
}

func antigravityOpenAIChatRequestBody(original *http.Request, route routeInfo, body []byte) ([]byte, error) {
	payload, err := decodeGeminiCLIJSONBody(body)
	if err != nil {
		return nil, err
	}
	model := antigravityModelName(route, payload)
	contents, systemInstruction := antigravityContentsFromMessages(payload["messages"])
	request := map[string]any{
		"contents": contents,
	}
	if systemInstruction != nil {
		request["systemInstruction"] = systemInstruction
	}
	if tools := antigravityToolsFromOpenAI(payload, model); len(tools) > 0 {
		request["tools"] = tools
	}
	if toolConfig := antigravityToolConfigFromOpenAI(payload, model); toolConfig != nil {
		request["toolConfig"] = toolConfig
	}
	generationConfig := geminiCLIGenerationConfig(payload, route, model)
	if firstPresent(payload, "max_tokens", "max_completion_tokens") == nil {
		generationConfig["maxOutputTokens"] = antigravityDefaultMaxOutputTokens(route)
	}
	if len(generationConfig) > 0 {
		request["generationConfig"] = generationConfig
	}
	if safetySettings := antigravitySafetySettings(payload, route); len(safetySettings) > 0 {
		request["safetySettings"] = safetySettings
	}
	if sessionID := antigravitySessionID(original, route, payload, body); sessionID != "" {
		request["sessionId"] = sessionID
	}

	wrapper := map[string]any{
		"model":       model,
		"userAgent":   antigravityUserAgent(route),
		"requestType": firstNonEmpty(routeMetadataString(route, "antigravity", "request_type", "requestType"), "agent"),
		"requestId":   antigravityRequestID(original, route, body),
		"request":     request,
	}
	if project := antigravityProject(route); project != "" {
		wrapper["project"] = project
	}
	return json.Marshal(wrapper)
}

func applyAntigravityHeaders(header http.Header, original *http.Request, route routeInfo, body []byte) {
	copyOriginalHeaders(header, original.Header, "X-Goog-Request-Reason")
	header.Set("Authorization", "Bearer "+route.APIKey)
	header.Set("User-Agent", antigravityUserAgent(route))
	if header.Get("Content-Type") == "" && original.Method != http.MethodGet {
		header.Set("Content-Type", "application/json")
	}
	if header.Get("Accept") == "" {
		header.Set("Accept", "application/json")
	}
	if project := antigravityProject(route); project != "" && header.Get("X-Goog-User-Project") == "" {
		header.Set("X-Goog-User-Project", project)
	}
	if reason := routeMetadataString(route, "antigravity", "request_reason", "x_goog_request_reason"); reason != "" && header.Get("X-Goog-Request-Reason") == "" {
		header.Set("X-Goog-Request-Reason", reason)
	}
	header.Del("X-Goog-Api-Key")
	header.Del("X-Goog-Api-Client")
	header.Del("Accept-Encoding")
	_ = body
}

func antigravityUserAgent(route routeInfo) string {
	if userAgent := routeMetadataString(route, "antigravity", "user_agent", "antigravity_user_agent"); userAgent != "" {
		return userAgent
	}
	version := firstNonEmpty(routeMetadataString(route, "antigravity", "client_version", "version", "ide_version"), "1.20.5")
	platform := firstNonEmpty(routeMetadataString(route, "antigravity", "platform", "os"), antigravityPlatform())
	arch := firstNonEmpty(routeMetadataString(route, "antigravity", "arch"), antigravityArch())
	return "antigravity/" + version + " " + platform + "/" + arch
}

func antigravityPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	default:
		return runtime.GOOS
	}
}

func antigravityArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	default:
		return runtime.GOARCH
	}
}

func antigravityProject(route routeInfo) string {
	return firstNonEmpty(routeMetadataString(route, "antigravity", "project", "project_id", "cloudaicompanion_project", "google_cloud_project", "quota_project_id"))
}

func antigravityDefaultMaxOutputTokens(route routeInfo) int {
	if value := routeMetadataString(route, "antigravity", "default_max_output_tokens", "max_output_tokens"); value != "" {
		if parsed, ok := geminiCLIIntValue(value); ok && parsed > 0 {
			return parsed
		}
	}
	return 64000
}

func antigravitySafetySettings(payload map[string]any, route routeInfo) []any {
	if settings, ok := payload["safety_settings"].([]any); ok {
		return settings
	}
	if settings, ok := payload["safetySettings"].([]any); ok {
		return settings
	}
	if routeMetadataBool(route, "antigravity", "disable_default_safety_settings", "disable_safety_settings") {
		return nil
	}
	categories := []string{
		"HARM_CATEGORY_HARASSMENT",
		"HARM_CATEGORY_HATE_SPEECH",
		"HARM_CATEGORY_SEXUALLY_EXPLICIT",
		"HARM_CATEGORY_DANGEROUS_CONTENT",
		"HARM_CATEGORY_CIVIC_INTEGRITY",
	}
	settings := make([]any, 0, len(categories))
	for _, category := range categories {
		settings = append(settings, map[string]any{"category": category, "threshold": "OFF"})
	}
	return settings
}

func antigravitySessionID(original *http.Request, route routeInfo, payload map[string]any, body []byte) string {
	if metadata, ok := objectValue(payload["metadata"]); ok {
		if sessionID := firstNonEmpty(metadataText(metadata["session_id"]), metadataText(metadata["conversation_id"]), metadataText(metadata["chat_id"])); sessionID != "" {
			return sessionID
		}
	}
	if sessionID := firstNonEmpty(
		metadataText(payload["session_id"]),
		metadataText(payload["conversation_id"]),
		original.Header.Get("X-Elucid-Relay-Session"),
		original.Header.Get("X-Antigravity-Session-Id"),
		routeMetadataString(route, "antigravity", "session_id", "conversation_id", "chat_id"),
		requestIDFromContext(original.Context()),
	); sessionID != "" {
		return sessionID
	}
	return geminiCLIDerivedID("antigravity-session", original, route, body)
}

func antigravityRequestID(original *http.Request, route routeInfo, body []byte) string {
	requestID := firstNonEmpty(
		original.Header.Get("X-Antigravity-Request-Id"),
		original.Header.Get("X-Request-Id"),
		requestIDFromContext(original.Context()),
		routeMetadataString(route, "antigravity", "request_id"),
	)
	if requestID != "" {
		if strings.HasPrefix(requestID, "agent-") {
			return requestID
		}
		return "agent-" + requestID
	}
	return "agent-" + geminiCLIDerivedID("request", original, route, body)
}

const antigravitySkipThoughtSignature = "skip_thought_signature_validator"

func antigravityModelName(route routeInfo, payload map[string]any) string {
	model := firstNonEmpty(route.UpstreamModel, metadataText(payload["model"]), "claude-sonnet-4-5")
	aliases := map[string]string{
		"gemini-claude-sonnet-4-5":                "claude-sonnet-4-5",
		"gemini-claude-sonnet-4-5-thinking":       "claude-sonnet-4-5-thinking",
		"gemini-claude-opus-4-5-thinking":         "claude-opus-4-5-thinking",
		"claude-sonnet-4.5":                       "claude-sonnet-4-5",
		"claude-sonnet-4.5-thinking":              "claude-sonnet-4-5-thinking",
		"claude-opus-4.5-thinking":                "claude-opus-4-5-thinking",
		"claude-opus-4.6":                         "claude-opus-4-6-thinking",
		"claude-opus-4.6-thinking":                "claude-opus-4-6-thinking",
		"claude-sonnet-4-5-20251001":              "claude-sonnet-4-5",
		"gemini-3-pro-preview":                    "gemini-3-pro-high",
		"gemini-3-flash-preview":                  "gemini-3-flash",
		"gemini-3-pro-image-preview":              "gemini-3-pro-image",
		"gemini-2.5-computer-use-preview-10-2025": "rev19-uic3-1p",
	}
	if mapped := aliases[strings.ToLower(model)]; mapped != "" {
		return mapped
	}
	return model
}

func antigravityContentsFromMessages(value any) ([]any, map[string]any) {
	messages, _ := value.([]any)
	contents := []any{}
	systemParts := []any{}
	toolCallNames := map[string]string{}
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(metadataText(message["role"]))
		parts := geminiCLIContentParts(message["content"])
		switch role {
		case "system", "developer":
			systemParts = append(systemParts, parts...)
		case "assistant":
			modelParts := antigravityAnnotateParts(append([]any{}, parts...))
			for _, toolCall := range antigravityToolCallsFromMessage(message["tool_calls"], toolCallNames) {
				modelParts = append(modelParts, toolCall)
			}
			if len(modelParts) > 0 {
				contents = append(contents, map[string]any{"role": "model", "parts": modelParts})
			}
		case "tool", "function":
			name := geminiCLIToolResponseName(message, toolCallNames)
			functionResponse := map[string]any{
				"name":     name,
				"response": map[string]any{"result": antigravityToolResult(message["content"])},
			}
			if toolCallID := metadataText(message["tool_call_id"]); toolCallID != "" {
				functionResponse["id"] = toolCallID
			}
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"functionResponse": functionResponse}},
			})
		default:
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "user", "parts": antigravityAnnotateParts(parts)})
			}
		}
	}
	var systemInstruction map[string]any
	if len(systemParts) > 0 {
		systemInstruction = map[string]any{"role": "user", "parts": systemParts}
	}
	return contents, systemInstruction
}

func antigravityAnnotateParts(parts []any) []any {
	for _, item := range parts {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := objectValue(part["functionCall"]); ok {
			part["thoughtSignature"] = antigravitySkipThoughtSignature
		}
		if _, ok := objectValue(part["inlineData"]); ok {
			part["thoughtSignature"] = antigravitySkipThoughtSignature
		}
	}
	return parts
}

func antigravityToolCallsFromMessage(value any, toolCallNames map[string]string) []any {
	calls, _ := value.([]any)
	parts := []any{}
	for _, item := range calls {
		toolCall, ok := item.(map[string]any)
		if !ok {
			continue
		}
		function, ok := objectValue(toolCall["function"])
		if !ok {
			continue
		}
		name := metadataText(function["name"])
		if name == "" {
			continue
		}
		id := metadataText(toolCall["id"])
		if id != "" {
			toolCallNames[id] = name
		}
		args := map[string]any{}
		if rawArgs := metadataText(function["arguments"]); rawArgs != "" {
			if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
				args = map[string]any{"value": rawArgs}
			}
		}
		functionCall := map[string]any{"name": name, "args": args}
		if id != "" {
			functionCall["id"] = id
		}
		parts = append(parts, map[string]any{
			"functionCall":     functionCall,
			"thoughtSignature": antigravitySkipThoughtSignature,
		})
	}
	return parts
}

func antigravityToolResult(value any) any {
	if text := metadataText(value); text != "" {
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); err == nil {
			return decoded
		}
		return text
	}
	if value != nil {
		return value
	}
	return map[string]any{}
}

func antigravityToolsFromOpenAI(payload map[string]any, model string) []any {
	functionDeclarations := []any{}
	nativeTools := []any{}
	if tools, ok := payload["tools"].([]any); ok {
		for _, item := range tools {
			tool, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch strings.ToLower(metadataText(tool["type"])) {
			case "function":
				if declaration := antigravityFunctionDeclaration(tool["function"], model); declaration != nil {
					functionDeclarations = append(functionDeclarations, declaration)
				}
			case "google_search", "googlesearch":
				nativeTools = append(nativeTools, map[string]any{"googleSearch": antigravityNativeToolConfig(tool, "google_search", "googleSearch")})
			case "code_execution", "codeexecution":
				nativeTools = append(nativeTools, map[string]any{"codeExecution": antigravityNativeToolConfig(tool, "code_execution", "codeExecution")})
			case "url_context", "urlcontext":
				nativeTools = append(nativeTools, map[string]any{"urlContext": antigravityNativeToolConfig(tool, "url_context", "urlContext")})
			}
		}
	}
	if functions, ok := payload["functions"].([]any); ok {
		for _, function := range functions {
			if declaration := antigravityFunctionDeclaration(function, model); declaration != nil {
				functionDeclarations = append(functionDeclarations, declaration)
			}
		}
	}
	tools := []any{}
	if len(functionDeclarations) > 0 {
		tools = append(tools, map[string]any{"functionDeclarations": functionDeclarations})
	}
	tools = append(tools, nativeTools...)
	return tools
}

func antigravityFunctionDeclaration(value any, model string) map[string]any {
	function, ok := objectValue(value)
	if !ok {
		return nil
	}
	name := metadataText(function["name"])
	if name == "" {
		return nil
	}
	declaration := map[string]any{"name": name}
	if description := metadataText(function["description"]); description != "" {
		declaration["description"] = description
	}
	parameters, ok := objectValue(function["parameters"])
	if !ok {
		parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	parameters = antigravityCleanSchema(parameters)
	if antigravityIsClaudeModel(model) {
		declaration["parameters"] = parameters
	} else {
		declaration["parametersJsonSchema"] = parameters
	}
	return declaration
}

func antigravityCleanSchema(schema map[string]any) map[string]any {
	return antigravityCleanSchemaValue(schema, true)
}

func antigravityCleanSchemaValue(schema map[string]any, topLevel bool) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	out := map[string]any{}
	for key, value := range schema {
		if key == "strict" || key == "additionalProperties" || strings.HasPrefix(key, "$") {
			continue
		}
		if nested, ok := objectValue(value); ok {
			out[key] = antigravityCleanSchemaValue(nested, false)
			continue
		}
		if items, ok := value.([]any); ok {
			cleaned := make([]any, 0, len(items))
			for _, item := range items {
				if nested, ok := objectValue(item); ok {
					cleaned = append(cleaned, antigravityCleanSchemaValue(nested, false))
				} else {
					cleaned = append(cleaned, item)
				}
			}
			out[key] = cleaned
			continue
		}
		out[key] = value
	}
	if out["required"] != nil {
		if _, ok := out["required"].([]any); !ok {
			delete(out, "required")
		}
	}
	if !topLevel {
		return out
	}
	if metadataText(out["type"]) == "" {
		out["type"] = "object"
	}
	if _, ok := objectValue(out["properties"]); !ok {
		out["properties"] = map[string]any{}
	}
	return out
}

func antigravityNativeToolConfig(tool map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if config, ok := objectValue(tool[key]); ok {
			return config
		}
	}
	return map[string]any{}
}

func antigravityToolConfigFromOpenAI(payload map[string]any, model string) map[string]any {
	if config := geminiCLIToolConfigFromOpenAI(payload); config != nil {
		return config
	}
	if antigravityIsClaudeModel(model) {
		return map[string]any{"functionCallingConfig": map[string]any{"mode": "VALIDATED"}}
	}
	return nil
}

func antigravityIsClaudeModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "claude") || strings.HasPrefix(lower, "sonnet") || strings.HasPrefix(lower, "opus")
}

func (adapter kiroAdapter) PrepareRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	if isGeminiCLIChatCompletionRequest(original) {
		return prepareKiroChatRequest(original, route, body)
	}
	return prepareProviderRequest(original, route, body, func(req *http.Request) {
		applyKiroHeaders(req.Header, original, route)
	})
}

func (adapter kiroAdapter) PrepareWebSocket(original *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error) {
	return prepareProviderWebSocket(original, route, func(header http.Header) {
		applyKiroHeaders(header, original, route)
	})
}

func (adapter kiroAdapter) ParseUsage(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	return parseProviderUsageResult(endpoint, requestBody, responseBody)
}

func (adapter kiroAdapter) ParseStreamEvent(endpoint string, event []byte, acc *streamMeteringAccumulator) {
	parseUsageFromStreamEvent(event, acc)
}

func (adapter kiroAdapter) TransformHTTPResponse(route routeInfo, endpoint string, requestBody []byte, status int, header http.Header, responseBody []byte) (int, http.Header, []byte) {
	if endpoint != "chat" {
		return status, header, responseBody
	}
	outHeader := geminiCLITransformedJSONHeader(header)
	if status < 200 || status >= 300 {
		return status, outHeader, geminiCLIErrorResponse(status, responseBody)
	}
	if transformed, ok := kiroChatCompletionResponse(route, requestBody, responseBody); ok {
		return status, outHeader, transformed
	}
	return status, header, responseBody
}

func (adapter kiroAdapter) NewRawStreamTransformer(route routeInfo, endpoint string, requestBody []byte) providerRawStreamTransformer {
	if endpoint != "chat" {
		return nil
	}
	return newKiroRawStreamTransformer(route, requestBody)
}

func prepareKiroChatRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	outboundBody, err := kiroOpenAIChatRequestBody(original, route, body)
	if err != nil {
		return nil, func() {}, err
	}
	baseURL, err := url.Parse(strings.TrimRight(route.BaseURL, "/"))
	if err != nil {
		return nil, func() {}, err
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/generateAssistantResponse"
	baseURL.RawPath = ""
	baseURL.RawQuery = ""

	timeout := time.Duration(route.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(original.Context(), timeout)
	req, err := http.NewRequestWithContext(ctx, original.Method, baseURL.String(), bytes.NewReader(outboundBody))
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	copyUpstreamRequestHeaders(req.Header, original.Header)
	applyKiroHeaders(req.Header, original, route)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity")
	if err := applyRouteHeaderRules(req, original, route); err != nil {
		cancel()
		return nil, func() {}, err
	}
	return req, cancel, nil
}

func kiroOpenAIChatRequestBody(original *http.Request, route routeInfo, body []byte) ([]byte, error) {
	payload, err := decodeGeminiCLIJSONBody(body)
	if err != nil {
		return nil, err
	}
	model := firstNonEmpty(route.UpstreamModel, metadataText(payload["model"]), "auto")
	messages, _ := payload["messages"].([]any)
	systemParts := []string{}
	entries := []kiroConversationEntry{}
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(metadataText(message["role"]))
		switch role {
		case "system", "developer":
			content := kiroMessageText(message["content"])
			if content != "" {
				systemParts = append(systemParts, content)
			}
		case "assistant":
			data := kiroMessageDataFromOpenAIMessage(message)
			assistantResponse := map[string]any{"content": firstNonEmpty(data.Text, "(empty)")}
			if len(data.ToolUses) > 0 {
				assistantResponse["toolUses"] = data.ToolUses
			}
			entries = append(entries, kiroConversationEntry{Kind: "assistant", Message: map[string]any{"assistantResponseMessage": assistantResponse}})
		case "tool", "function":
			data := kiroMessageDataFromOpenAIMessage(message)
			toolResults := data.ToolResults
			if len(toolResults) == 0 {
				toolResults = []any{kiroToolResult(metadataText(message["tool_call_id"]), message["content"])}
			}
			userInput := map[string]any{
				"content": "(empty)",
				"modelId": model,
				"origin":  "AI_EDITOR",
				"userInputMessageContext": map[string]any{
					"toolResults": toolResults,
				},
			}
			entries = append(entries, kiroConversationEntry{Kind: "user", Message: map[string]any{"userInputMessage": userInput}})
		default:
			data := kiroMessageDataFromOpenAIMessage(message)
			userInput := kiroUserInputFromMessageData(data, model)
			entries = append(entries, kiroConversationEntry{Kind: "user", Message: map[string]any{"userInputMessage": userInput}})
		}
	}
	entries = kiroNormalizeConversationEntries(entries, model)
	if len(systemParts) > 0 {
		kiroPrependSystemPrompt(entries, strings.Join(systemParts, "\n\n"))
	}
	history, userInput := kiroSplitHistoryAndCurrent(entries, model)
	userInput = kiroMergeUserInputContext(userInput, "tools", kiroToolsFromOpenAI(payload))
	conversationState := map[string]any{
		"chatTriggerType": "MANUAL",
		"conversationId":  kiroConversationID(original, route, body),
		"currentMessage":  map[string]any{"userInputMessage": userInput},
	}
	if len(history) > 0 {
		conversationState["history"] = history
	}
	outbound := map[string]any{"conversationState": conversationState}
	if profileArn := routeMetadataString(route, "kiro", "profile_arn", "profileArn"); profileArn != "" {
		outbound["profileArn"] = profileArn
	}
	return json.Marshal(outbound)
}

type kiroConversationEntry struct {
	Kind    string
	Message map[string]any
}

type kiroMessageData struct {
	Text        string
	Images      []any
	ToolResults []any
	ToolUses    []any
}

func kiroMessageDataFromOpenAIMessage(message map[string]any) kiroMessageData {
	text, images, toolResults, toolUses := kiroMessageContentData(message["content"])
	if len(toolUses) == 0 {
		toolUses = kiroToolUsesFromOpenAI(message["tool_calls"])
	}
	if role := strings.ToLower(metadataText(message["role"])); role == "tool" || role == "function" {
		if len(toolResults) == 0 {
			toolResults = []any{kiroToolResult(metadataText(message["tool_call_id"]), message["content"])}
		}
	}
	return kiroMessageData{Text: text, Images: images, ToolResults: toolResults, ToolUses: toolUses}
}

func kiroMessageContentData(value any) (string, []any, []any, []any) {
	switch typed := value.(type) {
	case nil:
		return "", nil, nil, nil
	case string:
		return typed, nil, nil, nil
	case []any:
		textParts := []string{}
		images := []any{}
		toolResults := []any{}
		toolUses := []any{}
		for _, item := range typed {
			block, ok := item.(map[string]any)
			if !ok {
				if text := kiroMessageText(item); text != "" {
					textParts = append(textParts, text)
				}
				continue
			}
			switch strings.ToLower(metadataText(block["type"])) {
			case "text", "input_text":
				if text := metadataText(block["text"]); text != "" {
					textParts = append(textParts, text)
				}
			case "image", "image_url", "input_image":
				if image, fallback := kiroImageFromContentBlock(block); image != nil {
					images = append(images, image)
				} else if fallback != "" {
					textParts = append(textParts, fallback)
				}
			case "tool_result":
				toolResults = append(toolResults, kiroToolResult(metadataText(block["tool_use_id"]), block["content"]))
			case "tool_use":
				toolUses = append(toolUses, kiroToolUseFromBlock(block))
			default:
				if text := kiroMessageText(block); text != "" {
					textParts = append(textParts, text)
				}
			}
		}
		return strings.Join(textParts, "\n"), images, toolResults, toolUses
	case map[string]any:
		if strings.EqualFold(metadataText(typed["type"]), "tool_result") {
			return "", nil, []any{kiroToolResult(metadataText(typed["tool_use_id"]), typed["content"])}, nil
		}
		if strings.EqualFold(metadataText(typed["type"]), "tool_use") {
			return "", nil, nil, []any{kiroToolUseFromBlock(typed)}
		}
		if image, fallback := kiroImageFromContentBlock(typed); image != nil {
			return "", []any{image}, nil, nil
		} else if fallback != "" {
			return fallback, nil, nil, nil
		}
	}
	return kiroMessageText(value), nil, nil, nil
}

func kiroUserInputFromMessageData(data kiroMessageData, model string) map[string]any {
	content := firstNonEmpty(data.Text, "(empty)")
	userInput := map[string]any{
		"content": content,
		"modelId": model,
		"origin":  "AI_EDITOR",
	}
	if len(data.Images) > 0 {
		userInput["images"] = data.Images
	}
	if len(data.ToolResults) > 0 {
		userInput = kiroMergeUserInputContext(userInput, "toolResults", data.ToolResults)
	}
	return userInput
}

func kiroNormalizeConversationEntries(entries []kiroConversationEntry, model string) []kiroConversationEntry {
	if len(entries) == 0 {
		return []kiroConversationEntry{kiroSyntheticUserEntry("Continue", model)}
	}
	normalized := make([]kiroConversationEntry, 0, len(entries)+2)
	for _, entry := range entries {
		if entry.Kind != "assistant" {
			entry.Kind = "user"
		}
		if len(normalized) == 0 && entry.Kind == "assistant" {
			normalized = append(normalized, kiroSyntheticUserEntry("(empty)", model))
		}
		if len(normalized) > 0 && normalized[len(normalized)-1].Kind == entry.Kind {
			if entry.Kind == "user" {
				normalized = append(normalized, kiroSyntheticAssistantEntry("(empty)"))
			} else {
				normalized = append(normalized, kiroSyntheticUserEntry("(empty)", model))
			}
		}
		normalized = append(normalized, entry)
	}
	return normalized
}

func kiroPrependSystemPrompt(entries []kiroConversationEntry, systemPrompt string) {
	if systemPrompt == "" {
		return
	}
	for _, entry := range entries {
		if entry.Kind != "user" {
			continue
		}
		userInput, ok := objectValue(entry.Message["userInputMessage"])
		if !ok {
			continue
		}
		content := firstNonEmpty(metadataText(userInput["content"]), "(empty)")
		userInput["content"] = systemPrompt + "\n\n" + content
		return
	}
}

func kiroSplitHistoryAndCurrent(entries []kiroConversationEntry, model string) ([]any, map[string]any) {
	if len(entries) == 0 {
		return nil, kiroSyntheticUserInput("Continue", model)
	}
	last := entries[len(entries)-1]
	if last.Kind != "user" {
		history := make([]any, 0, len(entries))
		for _, entry := range entries {
			history = append(history, entry.Message)
		}
		return history, kiroSyntheticUserInput("Continue", model)
	}
	userInput, ok := objectValue(last.Message["userInputMessage"])
	if !ok {
		userInput = kiroSyntheticUserInput("Continue", model)
	}
	history := make([]any, 0, len(entries)-1)
	for _, entry := range entries[:len(entries)-1] {
		history = append(history, entry.Message)
	}
	return history, userInput
}

func kiroSyntheticUserEntry(content string, model string) kiroConversationEntry {
	return kiroConversationEntry{Kind: "user", Message: map[string]any{"userInputMessage": kiroSyntheticUserInput(content, model)}}
}

func kiroSyntheticAssistantEntry(content string) kiroConversationEntry {
	return kiroConversationEntry{Kind: "assistant", Message: map[string]any{"assistantResponseMessage": map[string]any{"content": content}}}
}

func kiroSyntheticUserInput(content string, model string) map[string]any {
	return map[string]any{
		"content": firstNonEmpty(content, "Continue"),
		"modelId": model,
		"origin":  "AI_EDITOR",
	}
}

func kiroMergeUserInputContext(userInput map[string]any, key string, values []any) map[string]any {
	if len(values) == 0 {
		return userInput
	}
	contextValue, _ := objectValue(userInput["userInputMessageContext"])
	if contextValue == nil {
		contextValue = map[string]any{}
		userInput["userInputMessageContext"] = contextValue
	}
	contextValue[key] = values
	return userInput
}

func kiroImageFromContentBlock(block map[string]any) (map[string]any, string) {
	source, _ := objectValue(block["source"])
	if strings.EqualFold(metadataText(source["type"]), "base64") {
		mimeType := firstNonEmpty(metadataText(source["media_type"]), metadataText(source["mediaType"]), "image/jpeg")
		if data := metadataText(source["data"]); data != "" {
			return kiroImageFromBase64(mimeType, data), ""
		}
	}
	urlValue := block["image_url"]
	if urlValue == nil {
		urlValue = firstPresent(block, "url", "image", "data")
	}
	imageURL := metadataText(urlValue)
	if image, ok := objectValue(urlValue); ok {
		imageURL = firstNonEmpty(metadataText(image["url"]), metadataText(image["data"]))
	}
	if imageURL == "" {
		return nil, ""
	}
	if mimeType, data, ok := geminiCLIDataURL(imageURL); ok {
		return kiroImageFromBase64(mimeType, data), ""
	}
	return nil, "[image: " + imageURL + "]"
}

func kiroImageFromBase64(mimeType string, data string) map[string]any {
	format := strings.TrimPrefix(strings.ToLower(firstNonEmpty(mimeType, "image/jpeg")), "image/")
	if format == "jpg" {
		format = "jpeg"
	}
	return map[string]any{
		"format": format,
		"source": map[string]any{"bytes": data},
	}
}

func kiroToolResult(toolUseID string, content any) map[string]any {
	text := kiroMessageText(content)
	if text == "" {
		text = "(empty result)"
	}
	return map[string]any{
		"content":   []any{map[string]any{"text": text}},
		"status":    "success",
		"toolUseId": toolUseID,
	}
}

func kiroToolUsesFromOpenAI(value any) []any {
	calls, _ := value.([]any)
	toolUses := []any{}
	for _, item := range calls {
		toolCall, ok := item.(map[string]any)
		if !ok {
			continue
		}
		function, ok := objectValue(toolCall["function"])
		if !ok {
			continue
		}
		name := metadataText(function["name"])
		if name == "" {
			continue
		}
		input := map[string]any{}
		if rawArgs := metadataText(function["arguments"]); rawArgs != "" {
			if err := json.Unmarshal([]byte(rawArgs), &input); err != nil {
				input = map[string]any{"value": rawArgs}
			}
		}
		toolUses = append(toolUses, map[string]any{
			"name":      name,
			"input":     input,
			"toolUseId": metadataText(toolCall["id"]),
		})
	}
	return toolUses
}

func kiroToolUseFromBlock(block map[string]any) map[string]any {
	input := map[string]any{}
	if object, ok := objectValue(block["input"]); ok {
		input = object
	} else if rawInput := metadataText(block["input"]); rawInput != "" {
		if err := json.Unmarshal([]byte(rawInput), &input); err != nil {
			input = map[string]any{"value": rawInput}
		}
	}
	return map[string]any{
		"name":      metadataText(block["name"]),
		"input":     input,
		"toolUseId": firstNonEmpty(metadataText(block["id"]), metadataText(block["tool_use_id"])),
	}
}

func kiroMessageText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		parts := []string{}
		for _, item := range typed {
			text := kiroMessageText(item)
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if text := metadataText(typed["text"]); text != "" {
			return text
		}
		if imageURL, ok := objectValue(typed["image_url"]); ok {
			if urlText := metadataText(imageURL["url"]); urlText != "" {
				return "[image: " + urlText + "]"
			}
		}
		if data, err := json.Marshal(typed); err == nil {
			return string(data)
		}
	}
	if text := metadataText(value); text != "" {
		return text
	}
	return ""
}

func kiroToolsFromOpenAI(payload map[string]any) []any {
	tools := []any{}
	if items, ok := payload["tools"].([]any); ok {
		for _, item := range items {
			tool, ok := item.(map[string]any)
			if !ok || !strings.EqualFold(metadataText(tool["type"]), "function") {
				continue
			}
			if converted := kiroToolFromFunction(tool["function"]); converted != nil {
				tools = append(tools, converted)
			}
		}
	}
	if functions, ok := payload["functions"].([]any); ok {
		for _, function := range functions {
			if converted := kiroToolFromFunction(function); converted != nil {
				tools = append(tools, converted)
			}
		}
	}
	return tools
}

func kiroToolFromFunction(value any) map[string]any {
	function, ok := objectValue(value)
	if !ok {
		return nil
	}
	name := metadataText(function["name"])
	if name == "" {
		return nil
	}
	description := firstNonEmpty(metadataText(function["description"]), "Tool: "+name)
	inputSchema, ok := objectValue(function["parameters"])
	if !ok {
		inputSchema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{
		"toolSpecification": map[string]any{
			"name":        name,
			"description": description,
			"inputSchema": map[string]any{"json": inputSchema},
		},
	}
}

func kiroConversationID(original *http.Request, route routeInfo, body []byte) string {
	if conversationID := firstNonEmpty(
		original.Header.Get("X-Kiro-Conversation-Id"),
		original.Header.Get("X-Elucid-Relay-Session"),
		routeMetadataString(route, "kiro", "conversation_id", "session_id"),
		requestIDFromContext(original.Context()),
	); conversationID != "" {
		return conversationID
	}
	return geminiCLIDerivedID("kiro-conversation", original, route, body)
}

func applyKiroHeaders(header http.Header, original *http.Request, route routeInfo) {
	fingerprint := firstNonEmpty(routeMetadataString(route, "kiro", "fingerprint", "machine_fingerprint"), sha256Hex(firstNonEmpty(route.AccountID, route.TokenSubject, "elucid-relay-kiro")))
	version := firstNonEmpty(routeMetadataString(route, "kiro", "client_version", "kiro_version"), "0.7.45")
	nodeVersion := firstNonEmpty(routeMetadataString(route, "kiro", "node_version"), "22.21.1")
	osText := firstNonEmpty(routeMetadataString(route, "kiro", "os"), "win32#10.0.19044")
	userAgent := firstNonEmpty(
		routeMetadataString(route, "kiro", "user_agent"),
		"aws-sdk-js/1.0.27 ua/2.1 os/"+osText+" lang/js md/nodejs#"+nodeVersion+" api/codewhispererstreaming#1.0.27 m/E KiroIDE-"+version+"-"+fingerprint,
	)
	header.Set("Authorization", "Bearer "+route.APIKey)
	header.Set("Content-Type", "application/json")
	header.Set("User-Agent", userAgent)
	header.Set("x-amz-user-agent", firstNonEmpty(routeMetadataString(route, "kiro", "x_amz_user_agent"), "aws-sdk-js/1.0.27 KiroIDE-"+version+"-"+fingerprint))
	header.Set("x-amzn-codewhisperer-optout", firstNonEmpty(routeMetadataString(route, "kiro", "codewhisperer_optout", "optout"), "true"))
	header.Set("x-amzn-kiro-agent-mode", firstNonEmpty(routeMetadataString(route, "kiro", "agent_mode"), "vibe"))
	header.Set("amz-sdk-invocation-id", firstNonEmpty(original.Header.Get("amz-sdk-invocation-id"), requestIDFromContext(original.Context()), geminiCLIDerivedID("kiro-invocation", original, route, nil)))
	header.Set("amz-sdk-request", firstNonEmpty(routeMetadataString(route, "kiro", "amz_sdk_request"), "attempt=1; max=3"))
	header.Del("Accept-Encoding")
}

type kiroParsedEvent struct {
	Kind         string
	Content      string
	ToolCall     map[string]any
	UsageCredits float64
	ContextUsage float64
}

type kiroPendingToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type kiroEventStreamParser struct {
	buffer      string
	lastContent string
	currentTool *kiroPendingToolCall
}

func (parser *kiroEventStreamParser) Feed(chunk []byte) []kiroParsedEvent {
	if parser == nil || len(chunk) == 0 {
		return nil
	}
	parser.buffer += string(chunk)
	events := []kiroParsedEvent{}
	for {
		pos, kind := kiroNextJSONEvent(parser.buffer)
		if pos < 0 {
			parser.trimBuffer()
			return events
		}
		if pos > 0 {
			parser.buffer = parser.buffer[pos:]
		}
		end := kiroFindMatchingBrace(parser.buffer, 0)
		if end < 0 {
			parser.trimBuffer()
			return events
		}
		raw := parser.buffer[:end+1]
		parser.buffer = parser.buffer[end+1:]
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			continue
		}
		events = append(events, parser.process(payload, kind)...)
	}
}

func (parser *kiroEventStreamParser) Finish() []kiroParsedEvent {
	if parser == nil {
		return nil
	}
	events := parser.Feed(nil)
	if toolCall := parser.finalizeToolCall(); toolCall != nil {
		events = append(events, kiroParsedEvent{Kind: "tool_call", ToolCall: toolCall})
	}
	parser.buffer = ""
	return events
}

func (parser *kiroEventStreamParser) process(payload map[string]any, kind string) []kiroParsedEvent {
	switch kind {
	case "content":
		if skip, _ := metadataBool(payload["followupPrompt"]); skip {
			return nil
		}
		content := metadataText(payload["content"])
		if content == "" || content == parser.lastContent {
			return nil
		}
		parser.lastContent = content
		return []kiroParsedEvent{{Kind: "content", Content: content}}
	case "tool_start":
		events := []kiroParsedEvent{}
		if toolCall := parser.finalizeToolCall(); toolCall != nil {
			events = append(events, kiroParsedEvent{Kind: "tool_call", ToolCall: toolCall})
		}
		parser.currentTool = &kiroPendingToolCall{
			ID:        firstNonEmpty(metadataText(payload["toolUseId"]), "call_"+geminiCLIStableID([]byte(metadataText(payload["name"]) + kiroToolInputString(payload["input"])))[:24]),
			Name:      metadataText(payload["name"]),
			Arguments: kiroToolInputString(payload["input"]),
		}
		if stop, _ := metadataBool(payload["stop"]); stop {
			if toolCall := parser.finalizeToolCall(); toolCall != nil {
				events = append(events, kiroParsedEvent{Kind: "tool_call", ToolCall: toolCall})
			}
		}
		return events
	case "tool_input":
		if parser.currentTool != nil {
			parser.currentTool.Arguments += kiroToolInputString(payload["input"])
		}
	case "tool_stop":
		if stop, _ := metadataBool(payload["stop"]); stop {
			if toolCall := parser.finalizeToolCall(); toolCall != nil {
				return []kiroParsedEvent{{Kind: "tool_call", ToolCall: toolCall}}
			}
		}
	case "usage":
		if value, ok := kiroFloatValue(payload["usage"]); ok {
			return []kiroParsedEvent{{Kind: "usage", UsageCredits: value}}
		}
	case "context_usage":
		if value, ok := kiroFloatValue(payload["contextUsagePercentage"]); ok {
			return []kiroParsedEvent{{Kind: "context_usage", ContextUsage: value}}
		}
	}
	return nil
}

func (parser *kiroEventStreamParser) finalizeToolCall() map[string]any {
	if parser == nil || parser.currentTool == nil {
		return nil
	}
	tool := parser.currentTool
	parser.currentTool = nil
	if tool.Name == "" {
		return nil
	}
	args := strings.TrimSpace(tool.Arguments)
	if args == "" {
		args = "{}"
	} else {
		var parsed any
		if err := json.Unmarshal([]byte(args), &parsed); err == nil {
			if encoded, err := json.Marshal(parsed); err == nil {
				args = string(encoded)
			}
		} else {
			args = "{}"
		}
	}
	return map[string]any{
		"id":   tool.ID,
		"type": "function",
		"function": map[string]any{
			"name":      tool.Name,
			"arguments": args,
		},
	}
}

func (parser *kiroEventStreamParser) trimBuffer() {
	if parser == nil || len(parser.buffer) <= 65536 {
		return
	}
	parser.buffer = parser.buffer[len(parser.buffer)-65536:]
}

func kiroNextJSONEvent(buffer string) (int, string) {
	patterns := []struct {
		pattern string
		kind    string
	}{
		{`{"content":`, "content"},
		{`{"name":`, "tool_start"},
		{`{"input":`, "tool_input"},
		{`{"stop":`, "tool_stop"},
		{`{"usage":`, "usage"},
		{`{"contextUsagePercentage":`, "context_usage"},
	}
	pos := -1
	kind := ""
	for _, candidate := range patterns {
		next := strings.Index(buffer, candidate.pattern)
		if next >= 0 && (pos < 0 || next < pos) {
			pos = next
			kind = candidate.kind
		}
	}
	return pos, kind
}

func kiroFindMatchingBrace(text string, start int) int {
	if start < 0 || start >= len(text) || text[start] != '{' {
		return -1
	}
	depth := 0
	inString := false
	escapeNext := false
	for idx := start; idx < len(text); idx++ {
		ch := text[idx]
		if escapeNext {
			escapeNext = false
			continue
		}
		if ch == '\\' && inString {
			escapeNext = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return idx
			}
		}
	}
	return -1
}

func kiroToolInputString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case map[string]any, []any:
		if encoded, err := json.Marshal(typed); err == nil {
			return string(encoded)
		}
	default:
		if text := metadataText(value); text != "" {
			return text
		}
		if encoded, err := json.Marshal(value); err == nil && string(encoded) != "null" {
			return string(encoded)
		}
	}
	return ""
}

func kiroFloatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		if parsed, ok := geminiCLIIntValue(typed); ok {
			return float64(parsed), true
		}
	}
	return 0, false
}

type kiroStreamResult struct {
	Content      string
	ToolCalls    []map[string]any
	UsageCredits float64
	ContextUsage float64
}

func kiroCollectStreamResult(body []byte) kiroStreamResult {
	parser := &kiroEventStreamParser{}
	result := kiroStreamResult{}
	for _, event := range parser.Feed(body) {
		kiroApplyParsedEvent(&result, event)
	}
	for _, event := range parser.Finish() {
		kiroApplyParsedEvent(&result, event)
	}
	return result
}

func kiroApplyParsedEvent(result *kiroStreamResult, event kiroParsedEvent) {
	if result == nil {
		return
	}
	switch event.Kind {
	case "content":
		result.Content += event.Content
	case "tool_call":
		if event.ToolCall != nil {
			result.ToolCalls = append(result.ToolCalls, event.ToolCall)
		}
	case "usage":
		result.UsageCredits = event.UsageCredits
	case "context_usage":
		result.ContextUsage = event.ContextUsage
	}
}

func kiroChatCompletionResponse(route routeInfo, requestBody []byte, responseBody []byte) ([]byte, bool) {
	result := kiroCollectStreamResult(responseBody)
	if result.Content == "" && len(result.ToolCalls) == 0 && result.UsageCredits == 0 && result.ContextUsage == 0 {
		return nil, false
	}
	message := map[string]any{"role": "assistant", "content": result.Content}
	finishReason := "stop"
	if len(result.ToolCalls) > 0 {
		if result.Content == "" {
			message["content"] = nil
		}
		message["tool_calls"] = kiroOpenAIToolCalls(result.ToolCalls, false)
		finishReason = "tool_calls"
	}
	output := map[string]any{
		"id":      "chatcmpl-" + geminiCLIStableID(responseBody)[:24],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   kiroRequestModelFromBody(route, requestBody),
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": kiroOpenAIUsage(requestBody, result),
	}
	transformed, err := json.Marshal(output)
	return transformed, err == nil
}

type kiroRawStreamTransformer struct {
	parser      *kiroEventStreamParser
	id          string
	model       string
	created     int64
	requestBody []byte
	sentRole    bool
	result      kiroStreamResult
}

func newKiroRawStreamTransformer(route routeInfo, requestBody []byte) *kiroRawStreamTransformer {
	return &kiroRawStreamTransformer{
		parser:      &kiroEventStreamParser{},
		id:          "chatcmpl-" + geminiCLIStableID(requestBody)[:24],
		model:       kiroRequestModelFromBody(route, requestBody),
		created:     time.Now().Unix(),
		requestBody: requestBody,
	}
}

func (transformer *kiroRawStreamTransformer) TransformChunk(chunk []byte) ([][]byte, error) {
	if transformer == nil || transformer.parser == nil {
		return nil, nil
	}
	outputs := [][]byte{}
	for _, event := range transformer.parser.Feed(chunk) {
		kiroApplyParsedEvent(&transformer.result, event)
		if event.Kind != "content" || event.Content == "" {
			continue
		}
		delta := map[string]any{"content": event.Content}
		if !transformer.sentRole {
			delta["role"] = "assistant"
			transformer.sentRole = true
		}
		outputs = append(outputs, transformer.sseChunk(map[string]any{
			"id":      transformer.id,
			"object":  "chat.completion.chunk",
			"created": transformer.created,
			"model":   transformer.model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": nil,
			}},
		}))
	}
	return outputs, nil
}

func (transformer *kiroRawStreamTransformer) Finish() ([][]byte, error) {
	if transformer == nil || transformer.parser == nil {
		return nil, nil
	}
	outputs := [][]byte{}
	for _, event := range transformer.parser.Finish() {
		kiroApplyParsedEvent(&transformer.result, event)
	}
	if len(transformer.result.ToolCalls) > 0 {
		delta := map[string]any{"tool_calls": kiroOpenAIToolCalls(transformer.result.ToolCalls, true)}
		if !transformer.sentRole {
			delta["role"] = "assistant"
			transformer.sentRole = true
		}
		outputs = append(outputs, transformer.sseChunk(map[string]any{
			"id":      transformer.id,
			"object":  "chat.completion.chunk",
			"created": transformer.created,
			"model":   transformer.model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": nil,
			}},
		}))
	}
	finishReason := "stop"
	if len(transformer.result.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}
	outputs = append(outputs, transformer.sseChunk(map[string]any{
		"id":      transformer.id,
		"object":  "chat.completion.chunk",
		"created": transformer.created,
		"model":   transformer.model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": finishReason,
		}},
		"usage": kiroOpenAIUsage(transformer.requestBody, transformer.result),
	}))
	outputs = append(outputs, []byte("data: [DONE]\n\n"))
	return outputs, nil
}

func (transformer *kiroRawStreamTransformer) sseChunk(payload map[string]any) []byte {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return append(append([]byte("data: "), encoded...), []byte("\n\n")...)
}

func kiroOpenAIToolCalls(toolCalls []map[string]any, streaming bool) []any {
	output := make([]any, 0, len(toolCalls))
	for idx, toolCall := range toolCalls {
		function, _ := objectValue(toolCall["function"])
		converted := map[string]any{
			"id":   metadataText(toolCall["id"]),
			"type": firstNonEmpty(metadataText(toolCall["type"]), "function"),
			"function": map[string]any{
				"name":      metadataText(function["name"]),
				"arguments": firstNonEmpty(metadataText(function["arguments"]), "{}"),
			},
		}
		if streaming {
			converted["index"] = idx
		}
		output = append(output, converted)
	}
	return output
}

func kiroOpenAIUsage(requestBody []byte, result kiroStreamResult) map[string]any {
	promptTokens := maxInt(1, len(bytes.TrimSpace(requestBody))/4)
	completionTokens := len(result.Content) / 4
	if result.Content != "" && completionTokens < 1 {
		completionTokens = 1
	}
	usage := map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	}
	if result.UsageCredits > 0 {
		usage["credits_used"] = result.UsageCredits
	}
	if result.ContextUsage > 0 {
		usage["context_usage_percentage"] = result.ContextUsage
	}
	return usage
}

func kiroRequestModelFromBody(route routeInfo, requestBody []byte) string {
	var payload map[string]any
	_ = json.Unmarshal(requestBody, &payload)
	return firstNonEmpty(route.UpstreamModel, metadataText(payload["model"]), "auto")
}

func (adapter windsurfCodeiumAdapter) PrepareRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	body = rewriteWindsurfCodeiumRequestBody(original, route, body)
	return prepareProviderRequest(original, route, body, func(req *http.Request) {
		applyWindsurfCodeiumHeaders(req.Header, original, route)
	})
}

func (adapter windsurfCodeiumAdapter) PrepareWebSocket(original *http.Request, route routeInfo) (string, http.Header, *websocket.Dialer, error) {
	return prepareProviderWebSocket(original, route, func(header http.Header) {
		applyWindsurfCodeiumHeaders(header, original, route)
	})
}

func (adapter windsurfCodeiumAdapter) ParseUsage(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	return parseProviderUsageResult(endpoint, requestBody, responseBody)
}

func (adapter windsurfCodeiumAdapter) ParseStreamEvent(endpoint string, event []byte, acc *streamMeteringAccumulator) {
	parseUsageFromStreamEvent(event, acc)
}

func applyWindsurfCodeiumHeaders(header http.Header, original *http.Request, route routeInfo) {
	metadata := windsurfCodeiumRequestMetadata(original, route, nil)
	ideName := metadataText(metadata["ide_name"])
	ideVersion := metadataText(metadata["ide_version"])
	extensionName := metadataText(metadata["extension_name"])
	extensionVersion := metadataText(metadata["extension_version"])
	requestID := metadataText(metadata["request_id"])

	header.Set("Authorization", "Bearer "+route.APIKey)
	header.Set("User-Agent", firstNonEmpty(routeMetadataString(route, "windsurf", "user_agent"), "Windsurf/"+ideVersion+" Codeium/"+extensionVersion))
	header.Set("X-Codeium-IDE-Name", ideName)
	header.Set("X-Codeium-IDE-Version", ideVersion)
	header.Set("X-Codeium-Extension-Name", extensionName)
	header.Set("X-Codeium-Extension-Version", extensionVersion)
	header.Set("X-Codeium-Request-Id", requestID)
	if header.Get("Content-Type") == "" && original.Method != http.MethodGet {
		header.Set("Content-Type", "application/json")
	}
	if header.Get("Accept") == "" {
		header.Set("Accept", "application/json")
	}
	header.Del("Accept-Encoding")
}

func rewriteWindsurfCodeiumRequestBody(original *http.Request, route routeInfo, body []byte) []byte {
	mediaType, _, _ := mime.ParseMediaType(original.Header.Get("Content-Type"))
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "" {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	if !windsurfCodeiumNativePayload(payload) {
		return body
	}
	metadata, _ := objectValue(payload["metadata"])
	if metadata == nil {
		metadata = map[string]any{}
		payload["metadata"] = metadata
	}
	for key, value := range windsurfCodeiumRequestMetadata(original, route, body) {
		if _, exists := metadata[key]; !exists {
			metadata[key] = value
		}
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return rewritten
}

func windsurfCodeiumNativePayload(payload map[string]any) bool {
	if _, ok := payload["metadata"]; ok {
		return true
	}
	for _, key := range []string{
		"editor_options",
		"document",
		"other_documents",
		"completion_id",
		"request_id",
		"active_document",
		"workspace",
	} {
		if _, ok := payload[key]; ok {
			return true
		}
	}
	return false
}

func windsurfCodeiumRequestMetadata(original *http.Request, route routeInfo, body []byte) map[string]any {
	ideName := firstNonEmpty(routeMetadataString(route, "windsurf", "ide_name"), "windsurf")
	ideVersion := firstNonEmpty(routeMetadataString(route, "windsurf", "ide_version", "client_version"), "1.20.9")
	extensionName := firstNonEmpty(routeMetadataString(route, "windsurf", "extension_name"), "windsurf")
	extensionVersion := firstNonEmpty(routeMetadataString(route, "windsurf", "extension_version", "language_server_version"), ideVersion)
	requestID := firstNonEmpty(
		original.Header.Get("X-Codeium-Request-Id"),
		original.Header.Get("X-Request-Id"),
		requestIDFromContext(original.Context()),
		routeMetadataString(route, "windsurf", "request_id"),
	)
	if requestID == "" {
		requestID = geminiCLIDerivedID("windsurf-request", original, route, body)
	}
	return map[string]any{
		"api_key":           route.APIKey,
		"ide_name":          ideName,
		"ide_version":       ideVersion,
		"extension_name":    extensionName,
		"extension_version": extensionVersion,
		"request_id":        requestID,
	}
}
