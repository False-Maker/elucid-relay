package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type providerHTTPResponseTransformer interface {
	TransformHTTPResponse(route routeInfo, endpoint string, requestBody []byte, status int, header http.Header, responseBody []byte) (int, http.Header, []byte)
}

type providerSSEEventTransformer interface {
	TransformEvent(event []byte) ([][]byte, error)
	Finish() ([][]byte, error)
}

type providerSSETransformerFactory interface {
	NewSSETransformer(route routeInfo, endpoint string, requestBody []byte) providerSSEEventTransformer
}

type providerRawStreamTransformer interface {
	TransformChunk(chunk []byte) ([][]byte, error)
	Finish() ([][]byte, error)
}

type providerRawStreamTransformerFactory interface {
	NewRawStreamTransformer(route routeInfo, endpoint string, requestBody []byte) providerRawStreamTransformer
}

func transformProviderResponse(adapter providerAdapter, route routeInfo, endpoint string, requestBody []byte, status int, header http.Header, responseBody []byte) (int, http.Header, []byte) {
	transformer, ok := adapter.(providerHTTPResponseTransformer)
	if !ok {
		return status, header, responseBody
	}
	return transformer.TransformHTTPResponse(route, endpoint, requestBody, status, header, responseBody)
}

func newProviderSSETransformer(adapter providerAdapter, route routeInfo, endpoint string, requestBody []byte) providerSSEEventTransformer {
	factory, ok := adapter.(providerSSETransformerFactory)
	if !ok {
		return nil
	}
	return factory.NewSSETransformer(route, endpoint, requestBody)
}

func newProviderRawStreamTransformer(adapter providerAdapter, route routeInfo, endpoint string, requestBody []byte) providerRawStreamTransformer {
	factory, ok := adapter.(providerRawStreamTransformerFactory)
	if !ok {
		return nil
	}
	return factory.NewRawStreamTransformer(route, endpoint, requestBody)
}

func isGeminiCLIChatCompletionRequest(original *http.Request) bool {
	return original != nil && original.URL != nil && original.URL.Path == "/v1/chat/completions"
}

func prepareGeminiCLIChatRequest(original *http.Request, route routeInfo, body []byte) (*http.Request, context.CancelFunc, error) {
	outboundBody, err := geminiCLIOpenAIChatRequestBody(original, route, body)
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
	applyGeminiOfficialHeaders(req.Header, original, route, outboundBody)
	applyGeminiCodeAssistHeaders(req.Header, original, route, outboundBody)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	if shouldSetRelayRequestID(route) {
		req.Header.Set("X-Request-Id", requestIDFromContext(original.Context()))
	}
	if err := applyRouteHeaderRules(req, original, route); err != nil {
		cancel()
		return nil, func() {}, err
	}
	return req, cancel, nil
}

func geminiCLIMethodPath(basePath string, method string) string {
	clean := strings.TrimRight(basePath, "/")
	if clean == "" {
		return "/v1internal:" + method
	}
	if strings.HasSuffix(clean, "/v1internal") {
		return clean + ":" + method
	}
	return clean + "/v1internal:" + method
}

func geminiCLIOpenAIChatRequestBody(original *http.Request, route routeInfo, body []byte) ([]byte, error) {
	payload, err := decodeGeminiCLIJSONBody(body)
	if err != nil {
		return nil, err
	}
	model := firstNonEmpty(route.UpstreamModel, metadataText(payload["model"]), "gemini-2.5-pro")
	contents, systemInstruction := geminiCLIContentsFromMessages(payload["messages"])
	request := map[string]any{
		"contents": contents,
	}
	if systemInstruction != nil {
		request["systemInstruction"] = systemInstruction
	}
	if tools := geminiCLIToolsFromOpenAI(payload); len(tools) > 0 {
		request["tools"] = tools
	}
	if toolConfig := geminiCLIToolConfigFromOpenAI(payload); toolConfig != nil {
		request["toolConfig"] = toolConfig
	}
	if generationConfig := geminiCLIGenerationConfig(payload, route, model); len(generationConfig) > 0 {
		request["generationConfig"] = generationConfig
	}
	if safetySettings := geminiCLISafetySettings(payload, route); len(safetySettings) > 0 {
		request["safetySettings"] = safetySettings
	}
	sessionID := geminiCLISessionID(original, route, payload, body)
	if sessionID != "" {
		request["session_id"] = sessionID
	}
	if labels := geminiCLILabels(payload, route); len(labels) > 0 {
		request["labels"] = labels
	}

	wrapper := map[string]any{
		"model":   model,
		"request": request,
	}
	if project := geminiCLIProject(route); project != "" {
		wrapper["project"] = project
	}
	if userPromptID := geminiCLIUserPromptID(original, route, sessionID, body); userPromptID != "" {
		wrapper["user_prompt_id"] = userPromptID
	}
	if credits := routeMetadataList(route, "gemini", "enabled_credit_types", "credit_types"); len(credits) > 0 {
		wrapper["enabled_credit_types"] = credits
	}
	return json.Marshal(wrapper)
}

func decodeGeminiCLIJSONBody(body []byte) (map[string]any, error) {
	payload := map[string]any{}
	if len(bytes.TrimSpace(body)) == 0 {
		return payload, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, badRequest("Invalid JSON request body.")
	}
	return payload, nil
}

func geminiCLIProject(route routeInfo) string {
	return firstNonEmpty(routeMetadataString(route, "gemini", "project", "project_id", "google_cloud_project", "quota_project_id", "user_project"))
}

func geminiCLIUserPromptID(original *http.Request, route routeInfo, sessionID string, body []byte) string {
	if userPromptID := firstNonEmpty(
		original.Header.Get("X-Gemini-User-Prompt-Id"),
		original.Header.Get("X-Goog-Request-Reason"),
		routeMetadataString(route, "gemini", "user_prompt_id", "prompt_id"),
		requestIDFromContext(original.Context()),
		sessionID,
	); userPromptID != "" {
		return userPromptID
	}
	return geminiCLIDerivedID("prompt", original, route, body)
}

func geminiCLISessionID(original *http.Request, route routeInfo, payload map[string]any, body []byte) string {
	if metadata, ok := objectValue(payload["metadata"]); ok {
		if sessionID := firstNonEmpty(metadataText(metadata["session_id"]), metadataText(metadata["conversation_id"]), metadataText(metadata["chat_id"])); sessionID != "" {
			return sessionID
		}
	}
	if sessionID := firstNonEmpty(
		metadataText(payload["session_id"]),
		metadataText(payload["conversation_id"]),
		original.Header.Get("X-Elucid-Relay-Session"),
		original.Header.Get("X-Gemini-Session-Id"),
		routeMetadataString(route, "gemini", "session_id", "conversation_id", "chat_id"),
		requestIDFromContext(original.Context()),
	); sessionID != "" {
		return sessionID
	}
	return geminiCLIDerivedID("session", original, route, body)
}

func geminiCLILabels(payload map[string]any, route routeInfo) map[string]any {
	if labels, ok := objectValue(payload["labels"]); ok {
		return labels
	}
	if metadata, ok := objectValue(payload["metadata"]); ok {
		if labels, ok := objectValue(metadata["labels"]); ok {
			return labels
		}
	}
	if labels, ok := routeMetadataObject(route, "gemini", "labels"); ok {
		return labels
	}
	return nil
}

func geminiCLIDerivedID(prefix string, original *http.Request, route routeInfo, body []byte) string {
	seed := strings.Join([]string{
		prefix,
		original.Method,
		original.URL.Path,
		route.OwnerUserID,
		route.AccountID,
		route.ChannelID,
		firstNonEmpty(route.UpstreamModel, metadataText(route.TokenMetadata["model"])),
		string(body),
	}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	return prefix + "-" + hex.EncodeToString(sum[:])[:24]
}

func geminiCLIContentsFromMessages(value any) ([]any, map[string]any) {
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
			modelParts := append([]any{}, parts...)
			for _, toolCall := range geminiCLIToolCallsFromMessage(message["tool_calls"], toolCallNames) {
				modelParts = append(modelParts, toolCall)
			}
			if len(modelParts) > 0 {
				contents = append(contents, map[string]any{"role": "model", "parts": modelParts})
			}
		case "tool", "function":
			name := geminiCLIToolResponseName(message, toolCallNames)
			response := geminiCLIToolResponseContent(message["content"])
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []any{
					map[string]any{"functionResponse": map[string]any{"name": name, "response": response}},
				},
			})
		default:
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "user", "parts": parts})
			}
		}
	}
	var systemInstruction map[string]any
	if len(systemParts) > 0 {
		systemInstruction = map[string]any{"role": "user", "parts": systemParts}
	}
	return contents, systemInstruction
}

func geminiCLIContentParts(value any) []any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []any{map[string]any{"text": typed}}
	case []any:
		parts := []any{}
		for _, item := range typed {
			parts = append(parts, geminiCLIContentPart(item)...)
		}
		return parts
	case map[string]any:
		return geminiCLIContentPart(typed)
	default:
		if text := metadataText(typed); text != "" {
			return []any{map[string]any{"text": text}}
		}
		return nil
	}
}

func geminiCLIContentPart(value any) []any {
	item, ok := value.(map[string]any)
	if !ok {
		if text := metadataText(value); text != "" {
			return []any{map[string]any{"text": text}}
		}
		return nil
	}
	partType := strings.ToLower(metadataText(item["type"]))
	switch partType {
	case "text", "input_text":
		if text := metadataText(item["text"]); text != "" {
			return []any{map[string]any{"text": text}}
		}
	case "image_url", "input_image":
		if part := geminiCLIImagePart(item); part != nil {
			return []any{part}
		}
	case "file", "input_file":
		if part := geminiCLIFilePart(item); part != nil {
			return []any{part}
		}
	default:
		if text := metadataText(item["text"]); text != "" {
			return []any{map[string]any{"text": text}}
		}
	}
	return nil
}

func geminiCLIImagePart(item map[string]any) map[string]any {
	urlValue := item["image_url"]
	if urlValue == nil {
		urlValue = item["image"]
	}
	imageURL := metadataText(urlValue)
	if image, ok := objectValue(urlValue); ok {
		imageURL = metadataText(image["url"])
	}
	imageURL = firstNonEmpty(imageURL, metadataText(item["url"]))
	if imageURL == "" {
		return nil
	}
	mimeType := firstNonEmpty(metadataText(item["mime_type"]), metadataText(item["mimeType"]), "image/png")
	if dataMime, data, ok := geminiCLIDataURL(imageURL); ok {
		return map[string]any{"inlineData": map[string]any{"mimeType": dataMime, "data": data}}
	}
	return map[string]any{"fileData": map[string]any{"mimeType": mimeType, "fileUri": imageURL}}
}

func geminiCLIFilePart(item map[string]any) map[string]any {
	fileValue := item["file"]
	fileURL := metadataText(fileValue)
	if file, ok := objectValue(fileValue); ok {
		fileURL = firstNonEmpty(metadataText(file["file_data"]), metadataText(file["url"]), metadataText(file["file_uri"]), metadataText(file["fileUri"]))
	}
	fileURL = firstNonEmpty(fileURL, metadataText(item["file_data"]), metadataText(item["file_id"]), metadataText(item["url"]))
	if fileURL == "" {
		return nil
	}
	mimeType := firstNonEmpty(metadataText(item["mime_type"]), metadataText(item["mimeType"]), "application/octet-stream")
	if dataMime, data, ok := geminiCLIDataURL(fileURL); ok {
		return map[string]any{"inlineData": map[string]any{"mimeType": dataMime, "data": data}}
	}
	return map[string]any{"fileData": map[string]any{"mimeType": mimeType, "fileUri": fileURL}}
}

func geminiCLIDataURL(value string) (string, string, bool) {
	if !strings.HasPrefix(value, "data:") {
		return "", "", false
	}
	metaAndData := strings.TrimPrefix(value, "data:")
	meta, data, ok := strings.Cut(metaAndData, ",")
	if !ok || !strings.Contains(strings.ToLower(meta), ";base64") {
		return "", "", false
	}
	mimeType := strings.Split(meta, ";")[0]
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return mimeType, data, data != ""
}

func geminiCLIToolCallsFromMessage(value any, toolCallNames map[string]string) []any {
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
		if id := metadataText(toolCall["id"]); id != "" {
			toolCallNames[id] = name
		}
		args := map[string]any{}
		if rawArgs := metadataText(function["arguments"]); rawArgs != "" {
			_ = json.Unmarshal([]byte(rawArgs), &args)
		}
		parts = append(parts, map[string]any{"functionCall": map[string]any{"name": name, "args": args}})
	}
	return parts
}

func geminiCLIToolResponseName(message map[string]any, toolCallNames map[string]string) string {
	if name := metadataText(message["name"]); name != "" {
		return name
	}
	if toolCallID := metadataText(message["tool_call_id"]); toolCallID != "" {
		if name := toolCallNames[toolCallID]; name != "" {
			return name
		}
		return toolCallID
	}
	return "tool"
}

func geminiCLIToolResponseContent(value any) map[string]any {
	if response, ok := objectValue(value); ok {
		return response
	}
	if text := metadataText(value); text != "" {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(text), &decoded); err == nil {
			return decoded
		}
		return map[string]any{"content": text}
	}
	return map[string]any{}
}

func geminiCLIToolsFromOpenAI(payload map[string]any) []any {
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
				if declaration := geminiCLIFunctionDeclaration(tool["function"]); declaration != nil {
					functionDeclarations = append(functionDeclarations, declaration)
				}
			case "google_search", "googlesearch":
				nativeTools = append(nativeTools, map[string]any{"googleSearch": map[string]any{}})
			case "code_execution", "codeexecution":
				nativeTools = append(nativeTools, map[string]any{"codeExecution": map[string]any{}})
			case "url_context", "urlcontext":
				nativeTools = append(nativeTools, map[string]any{"urlContext": map[string]any{}})
			}
		}
	}
	if functions, ok := payload["functions"].([]any); ok {
		for _, function := range functions {
			if declaration := geminiCLIFunctionDeclaration(function); declaration != nil {
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

func geminiCLIFunctionDeclaration(value any) map[string]any {
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
	if parameters, ok := objectValue(function["parameters"]); ok {
		declaration["parametersJsonSchema"] = parameters
	} else {
		declaration["parametersJsonSchema"] = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return declaration
}

func geminiCLIToolConfigFromOpenAI(payload map[string]any) map[string]any {
	choice, ok := payload["tool_choice"]
	if !ok {
		choice = payload["function_call"]
	}
	if choice == nil {
		return nil
	}
	config := map[string]any{}
	switch typed := choice.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "none":
			config["mode"] = "NONE"
		case "required", "any":
			config["mode"] = "ANY"
		default:
			config["mode"] = "AUTO"
		}
	case map[string]any:
		name := ""
		if function, ok := objectValue(typed["function"]); ok {
			name = metadataText(function["name"])
		}
		name = firstNonEmpty(name, metadataText(typed["name"]))
		if name != "" {
			config["mode"] = "ANY"
			config["allowedFunctionNames"] = []string{name}
		}
	}
	if len(config) == 0 {
		return nil
	}
	return map[string]any{"functionCallingConfig": config}
}

func geminiCLIGenerationConfig(payload map[string]any, route routeInfo, model string) map[string]any {
	generationConfig := map[string]any{}
	defaultsEnabled := !routeMetadataBool(route, "gemini", "disable_default_generation_config", "disable_generation_defaults")
	if defaultsEnabled {
		if _, ok := payload["temperature"]; !ok {
			generationConfig["temperature"] = 1.0
		}
		if _, ok := payload["top_p"]; !ok {
			generationConfig["topP"] = 0.95
		}
		if _, ok := payload["top_k"]; !ok {
			generationConfig["topK"] = 64
		}
	}
	if value, ok := payload["temperature"]; ok {
		generationConfig["temperature"] = value
	}
	if value, ok := payload["top_p"]; ok {
		generationConfig["topP"] = value
	}
	if value, ok := payload["top_k"]; ok {
		generationConfig["topK"] = value
	}
	if value := firstPresent(payload, "max_tokens", "max_completion_tokens"); value != nil {
		if parsed, ok := geminiCLIIntValue(value); ok {
			generationConfig["maxOutputTokens"] = parsed
		}
	}
	if value := firstPresent(payload, "n", "candidate_count"); value != nil {
		if parsed, ok := geminiCLIIntValue(value); ok && parsed > 0 {
			generationConfig["candidateCount"] = parsed
		}
	}
	if sequences := geminiCLIStopSequences(payload["stop"]); len(sequences) > 0 {
		generationConfig["stopSequences"] = sequences
	}
	if value, ok := payload["presence_penalty"]; ok {
		generationConfig["presencePenalty"] = value
	}
	if value, ok := payload["frequency_penalty"]; ok {
		generationConfig["frequencyPenalty"] = value
	}
	if value, ok := payload["seed"]; ok {
		generationConfig["seed"] = value
	}
	geminiCLIApplyResponseFormat(generationConfig, payload)
	if modalities := geminiCLIResponseModalities(payload["modalities"]); len(modalities) > 0 {
		generationConfig["responseModalities"] = modalities
	}
	if imageConfig, ok := objectValue(payload["image_config"]); ok {
		generationConfig["imageConfig"] = imageConfig
	}
	if mediaResolution := metadataText(payload["media_resolution"]); mediaResolution != "" {
		generationConfig["mediaResolution"] = mediaResolution
	}
	if thinkingConfig := geminiCLIThinkingConfig(payload, model, defaultsEnabled); len(thinkingConfig) > 0 {
		generationConfig["thinkingConfig"] = thinkingConfig
	}
	return generationConfig
}

func geminiCLIStopSequences(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []any:
		sequences := []string{}
		for _, item := range typed {
			if text := metadataText(item); text != "" {
				sequences = append(sequences, text)
			}
		}
		return sequences
	default:
		return nil
	}
}

func geminiCLIApplyResponseFormat(generationConfig map[string]any, payload map[string]any) {
	responseFormat, ok := objectValue(payload["response_format"])
	if !ok {
		return
	}
	formatType := strings.ToLower(metadataText(responseFormat["type"]))
	if formatType == "json_object" || formatType == "json_schema" {
		generationConfig["responseMimeType"] = "application/json"
	}
	if jsonSchema, ok := objectValue(responseFormat["json_schema"]); ok {
		if schema, ok := objectValue(jsonSchema["schema"]); ok {
			generationConfig["responseJsonSchema"] = schema
		}
	}
}

func geminiCLIResponseModalities(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	modalities := []string{}
	for _, item := range items {
		text := strings.ToUpper(metadataText(item))
		if text != "" {
			modalities = append(modalities, text)
		}
	}
	return modalities
}

func geminiCLIThinkingConfig(payload map[string]any, model string, defaultsEnabled bool) map[string]any {
	reasoning, _ := objectValue(payload["reasoning"])
	if value := firstPresentMap(payload, reasoning, "thinking_budget", "thinkingBudget", "budget"); value != nil {
		if budget, ok := geminiCLIIntValue(value); ok {
			includeThoughts := budget != 0
			if include, ok := metadataBool(firstPresentMap(payload, reasoning, "include_thoughts", "includeThoughts")); ok {
				includeThoughts = include
			}
			return map[string]any{"thinkingBudget": budget, "includeThoughts": includeThoughts}
		}
	}
	effort := firstNonEmpty(metadataText(payload["reasoning_effort"]), metadataText(reasoning["effort"]))
	if effort != "" {
		switch strings.ToLower(effort) {
		case "none", "off", "disabled", "disable":
			return map[string]any{"thinkingBudget": 0, "includeThoughts": false}
		case "low":
			return map[string]any{"thinkingBudget": 1024, "includeThoughts": true}
		case "high":
			return map[string]any{"thinkingBudget": 24576, "includeThoughts": true}
		case "auto":
			return map[string]any{"thinkingBudget": -1, "includeThoughts": true}
		default:
			return map[string]any{"thinkingBudget": 8192, "includeThoughts": true}
		}
	}
	if !defaultsEnabled {
		return nil
	}
	model = strings.ToLower(model)
	if strings.HasPrefix(model, "gemini-3") {
		return map[string]any{"includeThoughts": true, "thinkingLevel": "HIGH"}
	}
	if strings.HasPrefix(model, "gemini-2.5") {
		return map[string]any{"thinkingBudget": 8192, "includeThoughts": true}
	}
	return nil
}

func firstPresentMap(primary map[string]any, secondary map[string]any, keys ...string) any {
	if primary != nil {
		if value := firstPresent(primary, keys...); value != nil {
			return value
		}
	}
	if secondary != nil {
		return firstPresent(secondary, keys...)
	}
	return nil
}

func geminiCLIIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed), true
		}
		floatValue, err := strconv.ParseFloat(typed.String(), 64)
		if err == nil {
			return int(floatValue), true
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func geminiCLISafetySettings(payload map[string]any, route routeInfo) []any {
	if settings, ok := payload["safety_settings"].([]any); ok {
		return settings
	}
	if settings, ok := payload["safetySettings"].([]any); ok {
		return settings
	}
	if !routeMetadataBool(route, "gemini", "enable_default_safety_settings", "default_safety_settings") {
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
		settings = append(settings, map[string]any{"category": category, "threshold": "BLOCK_NONE"})
	}
	return settings
}

func (adapter geminiCLIAdapter) TransformHTTPResponse(route routeInfo, endpoint string, requestBody []byte, status int, header http.Header, responseBody []byte) (int, http.Header, []byte) {
	if endpoint != "chat" {
		return status, header, responseBody
	}
	outHeader := geminiCLITransformedJSONHeader(header)
	if status < 200 || status >= 300 {
		return status, outHeader, geminiCLIErrorResponse(status, responseBody)
	}
	if transformed, ok := geminiCLIChatCompletionResponse(route, requestBody, responseBody); ok {
		return status, outHeader, transformed
	}
	return status, header, responseBody
}

func geminiCLITransformedJSONHeader(header http.Header) http.Header {
	out := header.Clone()
	out.Del("Content-Length")
	out.Del("Content-Encoding")
	out.Del("Transfer-Encoding")
	out.Set("Content-Type", "application/json")
	return out
}

func geminiCLIErrorResponse(status int, body []byte) []byte {
	message := strings.TrimSpace(string(body))
	code := "upstream_error"
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		if errorPayload, ok := objectValue(payload["error"]); ok {
			if text := metadataText(errorPayload["message"]); text != "" {
				message = text
			}
			if text := metadataText(errorPayload["code"]); text != "" {
				code = text
			}
		}
	}
	if message == "" {
		message = http.StatusText(status)
	}
	if geminiCLICloudCodePrivateAPIDisabled(message) {
		code = "cloud_code_private_api_disabled"
	}
	response, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    code,
		},
	})
	return response
}

func geminiCLICloudCodePrivateAPIDisabled(message string) bool {
	text := strings.ToLower(message)
	return strings.Contains(text, "cloud code private api has not been used") ||
		strings.Contains(text, "cloudcode-pa.googleapis.com")
}

func geminiCLIChatCompletionResponse(route routeInfo, requestBody []byte, responseBody []byte) ([]byte, bool) {
	root, ok := geminiCLIMapFromJSON(responseBody)
	if !ok {
		return nil, false
	}
	response := geminiCLIResponsePayload(root)
	if response == nil {
		return nil, false
	}
	id := firstNonEmpty(metadataText(root["traceId"]), metadataText(response["responseId"]), "chatcmpl-"+geminiCLIStableID(responseBody)[:24])
	model := firstNonEmpty(metadataText(response["modelVersion"]), geminiCLIRequestModelFromBody(route, requestBody))
	choices := []any{}
	if candidates, ok := response["candidates"].([]any); ok {
		for idx, item := range candidates {
			candidate, ok := item.(map[string]any)
			if !ok {
				continue
			}
			choice := geminiCLIChoiceFromCandidate(candidate, idx)
			if choice != nil {
				choices = append(choices, choice)
			}
		}
	}
	if len(choices) == 0 {
		choices = append(choices, map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": ""},
			"finish_reason": "stop",
		})
	}
	output := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": choices,
	}
	if usage, ok := geminiCLIOpenAIUsage(response); ok {
		output["usage"] = usage
	}
	transformed, err := json.Marshal(output)
	return transformed, err == nil
}

func geminiCLIChoiceFromCandidate(candidate map[string]any, fallbackIndex int) map[string]any {
	index := fallbackIndex
	if parsed, ok := geminiCLIIntValue(candidate["index"]); ok {
		index = parsed
	}
	content, reasoning, toolCalls := geminiCLIMessagePartsFromCandidate(candidate)
	message := map[string]any{"role": "assistant", "content": content}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	if len(toolCalls) > 0 {
		if content == "" {
			message["content"] = nil
		}
		message["tool_calls"] = toolCalls
	}
	return map[string]any{
		"index":         index,
		"message":       message,
		"finish_reason": geminiCLIMapFinishReason(metadataText(candidate["finishReason"]), len(toolCalls) > 0),
	}
}

func geminiCLIMessagePartsFromCandidate(candidate map[string]any) (string, string, []any) {
	contentPayload, _ := objectValue(candidate["content"])
	parts, _ := contentPayload["parts"].([]any)
	textParts := []string{}
	reasoningParts := []string{}
	toolCalls := []any{}
	for _, item := range parts {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if functionCall, ok := objectValue(part["functionCall"]); ok {
			toolCalls = append(toolCalls, geminiCLIOpenAIToolCall(functionCall, len(toolCalls)))
			continue
		}
		text := metadataText(part["text"])
		if text == "" {
			continue
		}
		if thought, _ := metadataBool(part["thought"]); thought {
			reasoningParts = append(reasoningParts, text)
		} else {
			textParts = append(textParts, text)
		}
	}
	return strings.Join(textParts, ""), strings.Join(reasoningParts, ""), toolCalls
}

func geminiCLIOpenAIToolCall(functionCall map[string]any, index int) map[string]any {
	name := metadataText(functionCall["name"])
	argsValue := functionCall["args"]
	args := "{}"
	if argsValue != nil {
		if encoded, err := json.Marshal(argsValue); err == nil {
			args = string(encoded)
		}
	}
	return map[string]any{
		"id":    "call_" + geminiCLIStableID([]byte(name + args))[:24],
		"type":  "function",
		"index": index,
		"function": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
}

func geminiCLIMapFinishReason(reason string, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_calls"
	}
	switch strings.ToUpper(reason) {
	case "", "STOP", "FINISH_REASON_STOP":
		return "stop"
	case "MAX_TOKENS", "FINISH_REASON_MAX_TOKENS":
		return "length"
	case "SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "content_filter"
	default:
		return strings.ToLower(reason)
	}
}

func (adapter geminiCLIAdapter) NewSSETransformer(route routeInfo, endpoint string, requestBody []byte) providerSSEEventTransformer {
	if endpoint != "chat" {
		return nil
	}
	return &geminiCLIChatSSETransformer{
		id:           "chatcmpl-" + geminiCLIStableID(requestBody)[:24],
		model:        geminiCLIRequestModelFromBody(route, requestBody),
		created:      time.Now().Unix(),
		sentRole:     map[int]bool{},
		finishReason: map[int]string{},
		seenIndex:    map[int]bool{},
	}
}

type geminiCLIChatSSETransformer struct {
	id           string
	model        string
	created      int64
	sentRole     map[int]bool
	finishReason map[int]string
	seenIndex    map[int]bool
	indexes      []int
	usage        map[string]any
	toolCount    int
}

func (transformer *geminiCLIChatSSETransformer) TransformEvent(event []byte) ([][]byte, error) {
	root, ok := geminiCLIMapFromJSON(event)
	if !ok {
		return nil, nil
	}
	response := geminiCLIResponsePayload(root)
	if response == nil {
		return nil, nil
	}
	if usage, ok := geminiCLIOpenAIUsage(response); ok {
		transformer.usage = usage
	}
	outputs := [][]byte{}
	candidates, _ := response["candidates"].([]any)
	for fallbackIndex, item := range candidates {
		candidate, ok := item.(map[string]any)
		if !ok {
			continue
		}
		index := fallbackIndex
		if parsed, ok := geminiCLIIntValue(candidate["index"]); ok {
			index = parsed
		}
		transformer.markIndex(index)
		if reason := metadataText(candidate["finishReason"]); reason != "" {
			transformer.finishReason[index] = geminiCLIMapFinishReason(reason, false)
		}
		contentPayload, _ := objectValue(candidate["content"])
		parts, _ := contentPayload["parts"].([]any)
		for _, item := range parts {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if functionCall, ok := objectValue(part["functionCall"]); ok {
				outputs = append(outputs, transformer.chunk(index, geminiCLIStreamToolCallDelta(functionCall, transformer.nextToolIndex(index))))
				transformer.finishReason[index] = "tool_calls"
				continue
			}
			text := metadataText(part["text"])
			if text == "" {
				continue
			}
			delta := map[string]any{}
			if thought, _ := metadataBool(part["thought"]); thought {
				delta["reasoning_content"] = text
			} else {
				delta["content"] = text
			}
			outputs = append(outputs, transformer.chunk(index, delta))
		}
	}
	return outputs, nil
}

func (transformer *geminiCLIChatSSETransformer) Finish() ([][]byte, error) {
	if len(transformer.indexes) == 0 {
		transformer.markIndex(0)
	}
	choices := []any{}
	for _, index := range transformer.indexes {
		reason := transformer.finishReason[index]
		if reason == "" {
			reason = "stop"
		}
		choices = append(choices, map[string]any{
			"index":         index,
			"delta":         map[string]any{},
			"finish_reason": reason,
		})
	}
	payload := transformer.basePayload()
	payload["choices"] = choices
	if transformer.usage != nil {
		payload["usage"] = transformer.usage
	}
	return [][]byte{geminiCLISSEData(payload), []byte("data: [DONE]\n\n")}, nil
}

func (transformer *geminiCLIChatSSETransformer) markIndex(index int) {
	if transformer.seenIndex[index] {
		return
	}
	transformer.seenIndex[index] = true
	transformer.indexes = append(transformer.indexes, index)
}

func (transformer *geminiCLIChatSSETransformer) nextToolIndex(index int) int {
	current := transformer.toolCount
	transformer.toolCount++
	return current
}

func (transformer *geminiCLIChatSSETransformer) chunk(index int, delta map[string]any) []byte {
	transformer.markIndex(index)
	if !transformer.sentRole[index] {
		delta["role"] = "assistant"
		transformer.sentRole[index] = true
	}
	payload := transformer.basePayload()
	payload["choices"] = []any{map[string]any{
		"index":         index,
		"delta":         delta,
		"finish_reason": nil,
	}}
	return geminiCLISSEData(payload)
}

func (transformer *geminiCLIChatSSETransformer) basePayload() map[string]any {
	return map[string]any{
		"id":      transformer.id,
		"object":  "chat.completion.chunk",
		"created": transformer.created,
		"model":   transformer.model,
	}
}

func geminiCLIStreamToolCallDelta(functionCall map[string]any, index int) map[string]any {
	name := metadataText(functionCall["name"])
	argsValue := functionCall["args"]
	args := "{}"
	if argsValue != nil {
		if encoded, err := json.Marshal(argsValue); err == nil {
			args = string(encoded)
		}
	}
	return map[string]any{
		"tool_calls": []any{map[string]any{
			"index": index,
			"id":    "call_" + geminiCLIStableID([]byte(name + args))[:24],
			"type":  "function",
			"function": map[string]any{
				"name":      name,
				"arguments": args,
			},
		}},
	}
}

func geminiCLISSEData(payload map[string]any) []byte {
	encoded, _ := json.Marshal(payload)
	out := make([]byte, 0, len(encoded)+8)
	out = append(out, "data: "...)
	out = append(out, encoded...)
	out = append(out, "\n\n"...)
	return out
}

func parseGeminiCLIUsageResult(endpoint string, requestBody []byte, responseBody []byte) meteringResult {
	if usage, ok := geminiCLIUsageCountsFromBody(responseBody); ok {
		metrics := meteringMetricsFromResponse(endpoint, requestBody, responseBody)
		return meteringResult{
			InputTokens:  maxInt(usage.InputTokens, metrics.InputTokens),
			OutputTokens: maxInt(usage.OutputTokens, metrics.OutputTokens),
			ImageCount:   metrics.ImageCount,
			AudioSeconds: metrics.AudioSeconds,
			RequestCount: nonZeroRequestCount(metrics.RequestCount),
			UsageSource:  "provider_usage",
		}
	}
	return parseProviderUsageResult(endpoint, requestBody, responseBody)
}

func parseGeminiCLIStreamUsage(event []byte, acc *streamMeteringAccumulator) {
	if acc == nil || len(bytes.TrimSpace(event)) == 0 {
		return
	}
	if usage, ok := geminiCLIUsageCountsFromBody(event); ok {
		acc.mergeUsage(usage)
		return
	}
	parseUsageFromStreamEvent(event, acc)
}

func geminiCLIUsageCountsFromBody(body []byte) (usageCounts, bool) {
	root, ok := geminiCLIMapFromJSON(body)
	if !ok {
		return usageCounts{}, false
	}
	response := geminiCLIResponsePayload(root)
	if response == nil {
		response = root
	}
	return geminiCLIUsageCounts(response)
}

func geminiCLIOpenAIUsage(response map[string]any) (map[string]any, bool) {
	counts, ok := geminiCLIUsageCounts(response)
	if !ok {
		return nil, false
	}
	usageMetadata, _ := objectValue(response["usageMetadata"])
	cached := firstPositiveNumberField(usageMetadata, "cachedContentTokenCount")
	thoughts := firstPositiveNumberField(usageMetadata, "thoughtsTokenCount")
	total := firstPositiveNumberField(usageMetadata, "totalTokenCount")
	if total == 0 {
		total = counts.InputTokens + counts.OutputTokens
	}
	usage := map[string]any{
		"prompt_tokens":     counts.InputTokens,
		"completion_tokens": counts.OutputTokens,
		"total_tokens":      total,
	}
	if cached > 0 {
		usage["prompt_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	if thoughts > 0 {
		usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": thoughts}
	}
	return usage, true
}

func geminiCLIUsageCounts(response map[string]any) (usageCounts, bool) {
	usageMetadata, ok := objectValue(response["usageMetadata"])
	if !ok {
		return usageCounts{}, false
	}
	input := firstPositiveNumberField(usageMetadata, "promptTokenCount")
	candidates := firstPositiveNumberField(usageMetadata, "candidatesTokenCount")
	thoughts := firstPositiveNumberField(usageMetadata, "thoughtsTokenCount")
	output := candidates + thoughts
	if input == 0 && output == 0 {
		return usageCounts{}, false
	}
	return usageCounts{InputTokens: input, OutputTokens: output}, true
}

func geminiCLIMapFromJSON(body []byte) (map[string]any, bool) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, false
	}
	return payload, true
}

func geminiCLIResponsePayload(root map[string]any) map[string]any {
	if response, ok := objectValue(root["response"]); ok {
		return response
	}
	if _, ok := root["candidates"]; ok {
		return root
	}
	return nil
}

func geminiCLIRequestModelFromBody(route routeInfo, body []byte) string {
	payload, ok := geminiCLIMapFromJSON(body)
	if !ok {
		return firstNonEmpty(route.UpstreamModel, "gemini-2.5-pro")
	}
	return firstNonEmpty(route.UpstreamModel, metadataText(payload["model"]), "gemini-2.5-pro")
}

func geminiCLIStableID(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
