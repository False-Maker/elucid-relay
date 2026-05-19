package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxParamOverrideOperations = 50
	maxParamOverridePathDepth  = 16
	maxParamOverrideAuditLines = 50
)

type paramOverrideAuditContextKey struct{}
type paramOverrideAuditAllContextKey struct{}

var paramOverrideKeyAuditPaths = map[string]struct{}{
	"model":          {},
	"original_model": {},
	"upstream_model": {},
	"service_tier":   {},
	"inference_geo":  {},
	"speed":          {},
}

func routeReverseProxyMetadata(route routeInfo) map[string]any {
	var values []map[string]any
	if metadata := metadataMapFromJSON(route.ChannelMeta); metadata != nil {
		values = append(values, metadata)
	}
	if metadata := metadataMapFromJSON(route.AbilityMeta); metadata != nil {
		values = append(values, metadata)
	}
	if metadata := metadataMapFromJSON(route.AccountMeta); metadata != nil {
		values = append(values, metadata)
	}
	if len(values) == 0 {
		return nil
	}
	return mergeMetadataMaps(values...)
}

func clientResponseStatus(route routeInfo, upstreamStatus int) int {
	if mapped, ok := routeStatusCodeMapping(route, upstreamStatus); ok {
		return mapped
	}
	return upstreamStatus
}

func copyRouteResponse(w http.ResponseWriter, route routeInfo, upstreamStatus int, header http.Header, body []byte) {
	copyResponse(w, clientResponseStatus(route, upstreamStatus), header, body)
}

type responseRewriteRule struct {
	From string
	To   string
}

type responseRewriteSettings struct {
	Rules        []responseRewriteRule
	Headers      []string
	ContentTypes []string
}

func applyRouteResponseRewrite(original *http.Request, route routeInfo, header http.Header, body []byte) (http.Header, []byte) {
	settings, ok := routeResponseRewriteSettings(original, route)
	if !ok || len(settings.Rules) == 0 {
		return header, body
	}

	outHeader := header
	headerCloned := false
	ensureHeader := func() http.Header {
		if outHeader == nil {
			outHeader = http.Header{}
			headerCloned = true
			return outHeader
		}
		if !headerCloned {
			outHeader = header.Clone()
			headerCloned = true
		}
		return outHeader
	}

	for _, name := range settings.Headers {
		name = http.CanonicalHeaderKey(strings.TrimSpace(name))
		if name == "" || outHeader == nil {
			continue
		}
		values := outHeader.Values(name)
		if len(values) == 0 {
			continue
		}
		changed := false
		next := make([]string, 0, len(values))
		for _, value := range values {
			rewritten := applyResponseRewriteRules(value, settings.Rules)
			if rewritten != value {
				changed = true
			}
			next = append(next, rewritten)
		}
		if changed {
			target := ensureHeader()
			target.Del(name)
			for _, value := range next {
				target.Add(name, value)
			}
		}
	}

	if !responseRewriteBodyAllowed(outHeader, body, settings.ContentTypes) {
		return outHeader, body
	}
	text := string(body)
	rewritten := applyResponseRewriteRules(text, settings.Rules)
	if rewritten == text {
		return outHeader, body
	}
	target := ensureHeader()
	target.Del("Content-Length")
	target.Del("Content-Md5")
	target.Del("Etag")
	return outHeader, []byte(rewritten)
}

func routeResponseRewriteSettings(original *http.Request, route routeInfo) (responseRewriteSettings, bool) {
	metadata := routeReverseProxyMetadata(route)
	if metadata == nil {
		return responseRewriteSettings{}, false
	}
	raw := firstPresent(metadata, "response_rewrite", "response_rewrites", "url_rewrite", "url_rewrites")
	if raw == nil {
		return responseRewriteSettings{}, false
	}
	object, ok := objectValue(raw)
	if !ok {
		return responseRewriteSettings{}, false
	}
	settings := responseRewriteSettings{
		Headers: []string{"Location"},
	}
	settings.Rules = appendResponseRewriteRules(settings.Rules, firstPresent(object, "replace", "replacements", "map", "rules"), original, route)
	settings.Rules = appendResponseRewriteRuleObjects(settings.Rules, firstPresent(object, "rules", "items"), original, route)
	if len(settings.Rules) == 0 {
		settings.Rules = appendResponseRewriteRuleObjects(settings.Rules, raw, original, route)
	}
	if headers := metadataList(firstPresent(object, "headers", "header_names")); len(headers) > 0 {
		settings.Headers = headers
	}
	settings.ContentTypes = metadataList(firstPresent(object, "content_types", "body_content_types"))
	return settings, len(settings.Rules) > 0
}

func appendResponseRewriteRules(rules []responseRewriteRule, raw any, original *http.Request, route routeInfo) []responseRewriteRule {
	replacements, ok := objectValue(raw)
	if !ok {
		return rules
	}
	for from, value := range replacements {
		from = resolveResponseRewriteValue(from, original, route)
		to := resolveResponseRewriteValue(metadataText(value), original, route)
		if from == "" || from == to {
			continue
		}
		rules = append(rules, responseRewriteRule{From: from, To: to})
	}
	return rules
}

func appendResponseRewriteRuleObjects(rules []responseRewriteRule, raw any, original *http.Request, route routeInfo) []responseRewriteRule {
	items, ok := arrayValue(raw)
	if !ok {
		return rules
	}
	for _, item := range items {
		object, ok := objectValue(item)
		if !ok {
			continue
		}
		from := resolveResponseRewriteValue(firstNonEmpty(metadataText(object["from"]), metadataText(object["source"])), original, route)
		to := resolveResponseRewriteValue(firstNonEmpty(metadataText(object["to"]), metadataText(object["target"]), metadataText(object["replacement"])), original, route)
		if from == "" || from == to {
			continue
		}
		rules = append(rules, responseRewriteRule{From: from, To: to})
	}
	return rules
}

func applyResponseRewriteRules(value string, rules []responseRewriteRule) string {
	for _, rule := range rules {
		if rule.From == "" || rule.From == rule.To {
			continue
		}
		value = strings.ReplaceAll(value, rule.From, rule.To)
		if strings.Contains(rule.From, "/") {
			value = strings.ReplaceAll(value, strings.ReplaceAll(rule.From, "/", `\/`), strings.ReplaceAll(rule.To, "/", `\/`))
		}
	}
	return value
}

func responseRewriteBodyAllowed(header http.Header, body []byte, allowed []string) bool {
	if len(body) == 0 || !utf8.Valid(body) {
		return false
	}
	contentType := ""
	if header != nil {
		contentType = responseRewriteMediaType(header.Get("Content-Type"))
	}
	if len(allowed) > 0 {
		return responseRewriteContentTypeAllowed(contentType, allowed)
	}
	if contentType == "" {
		return true
	}
	return strings.HasPrefix(contentType, "text/") ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "javascript") ||
		strings.Contains(contentType, "xml")
}

func responseRewriteContentTypeAllowed(contentType string, allowed []string) bool {
	for _, item := range allowed {
		item = responseRewriteMediaType(item)
		if item == "" {
			continue
		}
		if item == "*" || item == "*/*" || item == contentType {
			return true
		}
		if strings.HasSuffix(item, "/*") && strings.HasPrefix(contentType, strings.TrimSuffix(item, "*")) {
			return true
		}
	}
	return false
}

func responseRewriteMediaType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if idx := strings.Index(value, ";"); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}
	return value
}

var requestPlaceholderPattern = regexp.MustCompile(`\{request:([^}]+)\}`)

func resolveResponseRewriteValue(value string, original *http.Request, route routeInfo) string {
	value = resolveRouteHeaderValue(value, original, route)
	return requestPlaceholderPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := requestPlaceholderPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return ""
		}
		switch strings.ToLower(strings.TrimSpace(parts[1])) {
		case "origin", "scheme_host":
			return requestOrigin(original)
		case "host":
			if original == nil {
				return ""
			}
			urlHost := ""
			if original.URL != nil {
				urlHost = original.URL.Host
			}
			return strings.TrimSpace(firstNonEmpty(original.Host, urlHost))
		case "scheme":
			return requestScheme(original)
		default:
			return ""
		}
	})
}

func requestOrigin(r *http.Request) string {
	if r == nil {
		return ""
	}
	urlHost := ""
	if r.URL != nil {
		urlHost = r.URL.Host
	}
	host := strings.TrimSpace(firstNonEmpty(r.Host, urlHost))
	if host == "" {
		return ""
	}
	return requestScheme(r) + "://" + host
}

func requestScheme(r *http.Request) string {
	if r == nil {
		return ""
	}
	if r.URL != nil && strings.TrimSpace(r.URL.Scheme) != "" {
		return strings.TrimSpace(r.URL.Scheme)
	}
	if proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); proto == "http" || proto == "https" {
		return proto
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func routeStatusCodeMapping(route routeInfo, upstreamStatus int) (int, bool) {
	if !validHTTPStatusCode(upstreamStatus) {
		return 0, false
	}
	metadata := routeReverseProxyMetadata(route)
	if metadata == nil {
		return 0, false
	}
	mapping, ok := objectValue(metadata["status_code_mapping"])
	if !ok {
		mapping, ok = objectValue(metadata["status_code_mappings"])
	}
	if !ok {
		return 0, false
	}
	for key, value := range mapping {
		source, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil || source != upstreamStatus {
			continue
		}
		target, ok := metadataInt(value)
		if !ok || !validHTTPStatusCode(target) {
			return 0, false
		}
		return target, true
	}
	return 0, false
}

func usageMetadataWithClientStatusMapping(route routeInfo, upstreamStatus int, metadata map[string]any) map[string]any {
	clientStatus, ok := routeStatusCodeMapping(route, upstreamStatus)
	if !ok || clientStatus == upstreamStatus {
		return metadata
	}
	next := map[string]any{}
	for key, value := range metadata {
		next[key] = value
	}
	next["client_status_mapping"] = map[string]any{
		"upstream_status": upstreamStatus,
		"client_status":   clientStatus,
	}
	return next
}

func usageMetadataWithParamOverrideAudit(r *http.Request, metadata map[string]any) map[string]any {
	audit := paramOverrideAuditFromRequest(r)
	if len(audit) == 0 {
		return metadata
	}
	next := map[string]any{}
	for key, value := range metadata {
		next[key] = value
	}
	next["param_override_audit"] = audit
	return next
}

func paramOverrideAuditFromRequest(r *http.Request) []string {
	if r == nil {
		return nil
	}
	return paramOverrideAuditFromContext(r.Context())
}

func paramOverrideAuditFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	values, _ := ctx.Value(paramOverrideAuditContextKey{}).([]string)
	if len(values) == 0 {
		return nil
	}
	return append([]string{}, values...)
}

func withParamOverrideAuditAll(ctx context.Context) context.Context {
	return context.WithValue(ctx, paramOverrideAuditAllContextKey{}, true)
}

func shouldAuditAllParamOverrides(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(paramOverrideAuditAllContextKey{}).(bool)
	return enabled
}

func recordParamOverrideAudit(r *http.Request, op paramOverrideOperation) {
	if r == nil {
		return
	}
	line := paramOverrideAuditLine(r.Context(), op)
	if line == "" {
		return
	}
	current := paramOverrideAuditFromContext(r.Context())
	for _, item := range current {
		if item == line {
			return
		}
	}
	if len(current) >= maxParamOverrideAuditLines {
		return
	}
	next := append(append([]string{}, current...), line)
	*r = *r.WithContext(context.WithValue(r.Context(), paramOverrideAuditContextKey{}, next))
}

func paramOverrideAuditLine(ctx context.Context, op paramOverrideOperation) string {
	if !shouldAuditParamOverrideOperation(ctx, op) {
		return ""
	}
	switch op.op {
	case "set", "delete", "append", "prepend", "regex_replace", "replace":
		return op.op + " " + op.path
	case "copy", "move":
		return op.op + " " + op.from + " -> " + op.to
	case "set_header", "delete_header":
		return op.op + " " + op.headerName()
	case "copy_header", "move_header":
		return op.op + " " + op.from + " -> " + op.to
	case "pass_headers":
		return "pass_headers"
	case "system_prompt":
		return "system_prompt " + op.path
	default:
		return ""
	}
}

func shouldAuditParamOverrideOperation(ctx context.Context, op paramOverrideOperation) bool {
	if shouldAuditAllParamOverrides(ctx) || op.isHeaderOperation() || op.op == "system_prompt" {
		return true
	}
	for _, candidate := range []string{op.path, op.to} {
		if _, ok := paramOverrideKeyAuditPaths[strings.TrimSpace(candidate)]; ok {
			return true
		}
	}
	return false
}

func isRetryableUpstreamStatusForRoute(route routeInfo, status int) bool {
	if routeStatusListContains(route, "non_retryable_statuses", status) {
		return false
	}
	if routeStatusListContains(route, "retryable_statuses", status) {
		return true
	}
	return isRetryableUpstreamStatus(status)
}

func routeStatusListContains(route routeInfo, key string, status int) bool {
	metadata := routeReverseProxyMetadata(route)
	if metadata == nil {
		return false
	}
	retry, ok := objectValue(metadata["retry"])
	if !ok {
		return false
	}
	for _, item := range metadataList(retry[key]) {
		parsed, err := strconv.Atoi(strings.TrimSpace(item))
		if err == nil && parsed == status {
			return true
		}
	}
	return false
}

func routeCircuitStatusOpenSeconds(route routeInfo, upstreamStatus int) (int, bool) {
	metadata := routeReverseProxyMetadata(route)
	if metadata == nil {
		return 0, false
	}
	circuitBreaker, ok := objectValue(metadata["circuit_breaker"])
	if !ok {
		return 0, false
	}
	values, ok := objectValue(circuitBreaker["status_open_seconds"])
	if !ok {
		values, ok = objectValue(circuitBreaker["status_cooldown_seconds"])
	}
	if !ok {
		return 0, false
	}
	for key, value := range values {
		status, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil || status != upstreamStatus {
			continue
		}
		return metadataInt(value)
	}
	return 0, false
}

func applyRouteParamOverrideOperations(r *http.Request, route routeInfo, body []byte) ([]byte, error) {
	var err error
	body, err = applyRouteSystemPrompt(r, route, body)
	if err != nil {
		return nil, err
	}
	settings, ok, err := routeParamOverrideSettings(route)
	if err != nil || !ok {
		return body, err
	}
	operations, err := parseParamOverrideOperations(settings)
	if err != nil {
		return nil, err
	}
	bodyOperations := make([]paramOverrideOperation, 0, len(operations))
	for _, op := range operations {
		if op.isBodyOperation() {
			bodyOperations = append(bodyOperations, op)
		}
	}
	if len(bodyOperations) == 0 {
		return body, nil
	}
	contentType := ""
	if r != nil {
		contentType = r.Header.Get("Content-Type")
	}
	decoded, ok := decodeAffinityRuleJSON(body, contentType)
	if !ok {
		return nil, invalidParamOverride("Param override requires a JSON object request body.")
	}
	root, ok := decoded.(map[string]any)
	if !ok {
		return nil, invalidParamOverride("Param override requires a JSON object request body.")
	}
	for _, op := range bodyOperations {
		if !settings.allowModelOverride && op.touchesModel() {
			continue
		}
		if err := applyParamOverrideOperation(root, op); err != nil {
			return nil, err
		}
		recordParamOverrideAudit(r, op)
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

type routeParamOverrideConfig struct {
	allowModelOverride bool
	operations         []any
}

func routeParamOverrideSettings(route routeInfo) (routeParamOverrideConfig, bool, error) {
	metadata := routeReverseProxyMetadata(route)
	if metadata == nil {
		return routeParamOverrideConfig{}, false, nil
	}
	raw, ok := metadata["param_override"]
	if !ok {
		return routeParamOverrideConfig{}, false, nil
	}
	object, ok := objectValue(raw)
	if !ok {
		return routeParamOverrideConfig{}, false, invalidParamOverride("Param override must be an object.")
	}
	settings := routeParamOverrideConfig{}
	if allow, ok := metadataBool(object["allow_model_override"]); ok {
		settings.allowModelOverride = allow
	}
	if rawOperations, ok := object["operations"]; ok {
		operations, ok := arrayValue(rawOperations)
		if !ok {
			return routeParamOverrideConfig{}, false, invalidParamOverride("Param override operations must be an array.")
		}
		settings.operations = operations
	}
	return settings, true, nil
}

type paramOverrideOperation struct {
	op          string
	path        string
	from        string
	to          string
	pattern     string
	replacement string
	value       any
}

func parseParamOverrideOperations(settings routeParamOverrideConfig) ([]paramOverrideOperation, error) {
	if len(settings.operations) == 0 {
		return nil, nil
	}
	if len(settings.operations) > maxParamOverrideOperations {
		return nil, invalidParamOverride("Param override operation count exceeds the limit.")
	}
	operations := make([]paramOverrideOperation, 0, len(settings.operations))
	for _, item := range settings.operations {
		op, err := parseParamOverrideOperation(item)
		if err != nil {
			return nil, err
		}
		operations = append(operations, op)
	}
	return operations, nil
}

func parseParamOverrideOperation(value any) (paramOverrideOperation, error) {
	object, ok := objectValue(value)
	if !ok {
		return paramOverrideOperation{}, invalidParamOverride("Param override operations must be objects.")
	}
	mode := strings.TrimSpace(metadataText(object["op"]))
	if mode == "" {
		mode = strings.TrimSpace(metadataText(object["mode"]))
	}
	op := paramOverrideOperation{
		op:          strings.ToLower(mode),
		path:        strings.TrimSpace(metadataText(object["path"])),
		from:        strings.TrimSpace(metadataText(object["from"])),
		to:          strings.TrimSpace(metadataText(object["to"])),
		pattern:     metadataText(object["pattern"]),
		replacement: metadataText(object["replacement"]),
		value:       object["value"],
	}
	if op.path == "" {
		op.path = strings.TrimSpace(firstNonEmpty(metadataText(object["header"]), metadataText(object["name"])))
	}
	if op.pattern == "" {
		op.pattern = metadataText(object["from"])
	}
	if op.replacement == "" {
		op.replacement = metadataText(object["to"])
	}
	switch op.op {
	case "set", "delete", "append", "prepend", "regex_replace", "replace":
		if op.path == "" {
			return paramOverrideOperation{}, invalidParamOverride("Param override operation path is required.")
		}
	case "copy", "move":
		if op.from == "" || op.to == "" {
			return paramOverrideOperation{}, invalidParamOverride("Param override copy and move require from and to.")
		}
	case "set_header", "delete_header":
		if op.headerName() == "" {
			return paramOverrideOperation{}, invalidParamOverride("Param override header operation requires a header name.")
		}
	case "copy_header", "move_header":
		if op.from == "" || op.to == "" {
			return paramOverrideOperation{}, invalidParamOverride("Param override header copy and move require from and to.")
		}
	case "pass_headers":
		if op.value == nil && op.path == "" {
			return paramOverrideOperation{}, invalidParamOverride("Param override pass_headers requires value.")
		}
	default:
		return paramOverrideOperation{}, invalidParamOverride("Param override operation is unsupported.")
	}
	if op.op == "regex_replace" && strings.TrimSpace(op.pattern) == "" {
		return paramOverrideOperation{}, invalidParamOverride("Param override regex_replace requires pattern.")
	}
	return op, nil
}

func (op paramOverrideOperation) isBodyOperation() bool {
	return !op.isHeaderOperation()
}

func (op paramOverrideOperation) isHeaderOperation() bool {
	switch op.op {
	case "set_header", "delete_header", "copy_header", "move_header", "pass_headers":
		return true
	default:
		return false
	}
}

func (op paramOverrideOperation) headerName() string {
	return strings.TrimSpace(firstNonEmpty(op.path, op.to))
}

func (op paramOverrideOperation) touchesModel() bool {
	return paramOverridePathIsModel(op.path) || paramOverridePathIsModel(op.from) || paramOverridePathIsModel(op.to)
}

func paramOverridePathIsModel(path string) bool {
	return strings.EqualFold(strings.TrimSpace(path), "model")
}

func applyParamOverrideOperation(root map[string]any, op paramOverrideOperation) error {
	switch op.op {
	case "set":
		parts, err := parseParamOverridePath(op.path)
		if err != nil {
			return err
		}
		_, _, err = setJSONPathAny(root, parts, op.value)
		return err
	case "delete":
		parts, err := parseParamOverridePath(op.path)
		if err != nil {
			return err
		}
		_, _, err = deleteJSONPathAny(root, parts)
		return err
	case "copy":
		return copyJSONPath(root, op.from, op.to, false)
	case "move":
		return copyJSONPath(root, op.from, op.to, true)
	case "append":
		return appendJSONPath(root, op.path, op.value, false)
	case "prepend":
		return appendJSONPath(root, op.path, op.value, true)
	case "regex_replace":
		return regexReplaceJSONPath(root, op.path, op.pattern, op.replacement)
	case "replace":
		return replaceJSONPath(root, op.path, op.from, op.to)
	default:
		return invalidParamOverride("Param override operation is unsupported.")
	}
}

func copyJSONPath(root map[string]any, from string, to string, move bool) error {
	fromParts, err := parseParamOverridePath(from)
	if err != nil {
		return err
	}
	toParts, err := parseParamOverridePath(to)
	if err != nil {
		return err
	}
	value, ok, err := getJSONPathAny(root, fromParts)
	if err != nil || !ok {
		return err
	}
	_, _, err = setJSONPathAny(root, toParts, cloneJSONValue(value))
	if err != nil {
		return err
	}
	if move {
		_, _, err = deleteJSONPathAny(root, fromParts)
	}
	return err
}

func appendJSONPath(root map[string]any, path string, value any, prepend bool) error {
	parts, err := parseParamOverridePath(path)
	if err != nil {
		return err
	}
	current, ok, err := getJSONPathAny(root, parts)
	if err != nil {
		return err
	}
	if !ok {
		_, _, err = setJSONPathAny(root, parts, appendOverrideItems(value))
		return err
	}
	switch typed := current.(type) {
	case []any:
		items := appendOverrideItems(value)
		next := make([]any, 0, len(typed)+len(items))
		if prepend {
			next = append(next, items...)
			next = append(next, typed...)
		} else {
			next = append(next, typed...)
			next = append(next, items...)
		}
		_, _, err = setJSONPathAny(root, parts, next)
		return err
	case string:
		text := metadataText(value)
		next := typed + text
		if prepend {
			next = text + typed
		}
		_, _, err = setJSONPathAny(root, parts, next)
		return err
	default:
		return invalidParamOverride("Param override append and prepend require a string, array, or missing target.")
	}
}

func regexReplaceJSONPath(root map[string]any, path string, pattern string, replacement string) error {
	parts, err := parseParamOverridePath(path)
	if err != nil {
		return err
	}
	current, ok, err := getJSONPathAny(root, parts)
	if err != nil || !ok {
		return err
	}
	text, ok := current.(string)
	if !ok {
		return invalidParamOverride("Param override regex_replace requires a string target.")
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return invalidParamOverride("Param override regex_replace pattern is invalid.")
	}
	_, _, err = setJSONPathAny(root, parts, compiled.ReplaceAllString(text, replacement))
	return err
}

func replaceJSONPath(root map[string]any, path string, from string, to string) error {
	if from == "" {
		return invalidParamOverride("Param override replace requires from.")
	}
	parts, err := parseParamOverridePath(path)
	if err != nil {
		return err
	}
	current, ok, err := getJSONPathAny(root, parts)
	if err != nil || !ok {
		return err
	}
	text, ok := current.(string)
	if !ok {
		return invalidParamOverride("Param override replace requires a string target.")
	}
	_, _, err = setJSONPathAny(root, parts, strings.ReplaceAll(text, from, to))
	return err
}

func applyRouteParamOverrideHeaderOperations(original *http.Request, route routeInfo, header http.Header) error {
	if header == nil {
		return nil
	}
	settings, ok, err := routeParamOverrideSettings(route)
	if err != nil || !ok {
		return err
	}
	operations, err := parseParamOverrideOperations(settings)
	if err != nil {
		return err
	}
	for _, op := range operations {
		if !op.isHeaderOperation() {
			continue
		}
		if err := applyParamOverrideHeaderOperation(original, route, header, op); err != nil {
			return err
		}
		recordParamOverrideAudit(original, op)
	}
	return nil
}

func applyParamOverrideHeaderOperation(original *http.Request, route routeInfo, header http.Header, op paramOverrideOperation) error {
	switch op.op {
	case "set_header":
		name, err := safeParamOverrideHeaderName(op.headerName())
		if err != nil {
			return err
		}
		value := resolveRouteHeaderValue(metadataText(op.value), original, route)
		if strings.TrimSpace(value) == "" {
			header.Del(name)
			return nil
		}
		header.Set(name, value)
	case "delete_header":
		name, err := safeParamOverrideHeaderName(op.headerName())
		if err != nil {
			return err
		}
		header.Del(name)
	case "copy_header", "move_header":
		source, err := safeParamOverrideHeaderName(op.from)
		if err != nil {
			return err
		}
		target, err := safeParamOverrideHeaderName(op.to)
		if err != nil {
			return err
		}
		values := header.Values(source)
		if len(values) == 0 && original != nil {
			values = original.Header.Values(source)
		}
		if len(values) == 0 {
			return nil
		}
		header.Del(target)
		for _, value := range values {
			header.Add(target, value)
		}
		if op.op == "move_header" {
			header.Del(source)
		}
	case "pass_headers":
		names, err := decodeParamOverridePassHeaders(op)
		if err != nil {
			return err
		}
		if original == nil {
			return nil
		}
		for _, name := range names {
			if name == "*" {
				passSafeRequestHeaders(header, original.Header, nil)
				continue
			}
			passRequestHeader(header, original.Header, name, nil)
		}
	}
	return nil
}

func safeParamOverrideHeaderName(name string) (string, error) {
	canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
	if canonical == "" ||
		proxyHeaderBlocked(canonical, proxyBlockedRequestHeaders, nil) ||
		proxyHeaderBlocked(canonical, proxyBlockedRouteOverrideHeaders, nil) ||
		hasGatewayHeaderPrefix(canonical) {
		return "", invalidParamOverride("Param override header operation targets a blocked header.")
	}
	return canonical, nil
}

func decodeParamOverridePassHeaders(op paramOverrideOperation) ([]string, error) {
	if op.path != "" {
		return []string{op.path}, nil
	}
	if values := metadataList(op.value); len(values) > 0 {
		return values, nil
	}
	if object, ok := objectValue(op.value); ok {
		for _, key := range []string{"headers", "names", "header"} {
			if values := metadataList(object[key]); len(values) > 0 {
				return values, nil
			}
		}
	}
	return nil, invalidParamOverride("Param override pass_headers value is invalid.")
}

func applyRouteSystemPrompt(r *http.Request, route routeInfo, body []byte) ([]byte, error) {
	metadata := routeReverseProxyMetadata(route)
	if metadata == nil {
		return body, nil
	}
	raw, ok := metadata["system_prompt"]
	if !ok {
		return body, nil
	}
	settings, ok := systemPromptSettings(raw)
	if !ok || settings.text == "" {
		return body, nil
	}
	contentType := ""
	if r != nil {
		contentType = r.Header.Get("Content-Type")
	}
	decoded, ok := decodeAffinityRuleJSON(body, contentType)
	if !ok {
		return body, nil
	}
	root, ok := decoded.(map[string]any)
	if !ok {
		return body, nil
	}
	changed := applySystemPromptToPayload(root, settings.mode, settings.text)
	if !changed {
		return body, nil
	}
	recordParamOverrideAudit(r, paramOverrideOperation{op: "system_prompt", path: settings.mode})
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

type routeSystemPromptSettings struct {
	mode string
	text string
}

func systemPromptSettings(raw any) (routeSystemPromptSettings, bool) {
	if text := strings.TrimSpace(metadataText(raw)); text != "" {
		return routeSystemPromptSettings{mode: "if_absent", text: text}, true
	}
	object, ok := objectValue(raw)
	if !ok {
		return routeSystemPromptSettings{}, false
	}
	mode := strings.ToLower(strings.TrimSpace(metadataText(object["mode"])))
	if mode == "" {
		mode = "if_absent"
	}
	switch mode {
	case "if_absent", "prepend", "replace":
	default:
		mode = "if_absent"
	}
	text := strings.TrimSpace(firstNonEmpty(metadataText(object["text"]), metadataText(object["content"]), metadataText(object["prompt"])))
	return routeSystemPromptSettings{mode: mode, text: text}, text != ""
}

func applySystemPromptToPayload(root map[string]any, mode string, text string) bool {
	if messages, ok := root["messages"].([]any); ok {
		updated, changed := applyOpenAISystemPrompt(messages, mode, text)
		if changed {
			root["messages"] = updated
		}
		return changed
	}
	if _, ok := root["system"]; ok {
		return applyAnthropicSystemPrompt(root, mode, text)
	}
	root["system"] = text
	return true
}

func applyOpenAISystemPrompt(messages []any, mode string, text string) ([]any, bool) {
	systemIndex := -1
	for i, item := range messages {
		object, ok := objectValue(item)
		if !ok || !strings.EqualFold(metadataText(object["role"]), "system") {
			continue
		}
		systemIndex = i
		break
	}
	if systemIndex < 0 {
		return append([]any{map[string]any{"role": "system", "content": text}}, messages...), true
	}
	if mode == "if_absent" {
		return messages, false
	}
	object, _ := objectValue(messages[systemIndex])
	if mode == "replace" {
		object["content"] = text
		return messages, true
	}
	if existing := metadataText(object["content"]); existing != "" {
		object["content"] = text + "\n" + existing
		return messages, true
	}
	object["content"] = text
	return messages, true
}

func applyAnthropicSystemPrompt(root map[string]any, mode string, text string) bool {
	existing, exists := root["system"]
	if !exists || systemPromptValueEmpty(existing) {
		root["system"] = text
		return true
	}
	if mode == "if_absent" {
		return false
	}
	if mode == "replace" {
		root["system"] = text
		return true
	}
	switch typed := existing.(type) {
	case string:
		root["system"] = text + "\n" + typed
	case []any:
		root["system"] = append([]any{map[string]any{"type": "text", "text": text}}, typed...)
	default:
		root["system"] = text
	}
	return true
}

func systemPromptValueEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	default:
		return strings.TrimSpace(metadataText(value)) == ""
	}
}

func parseParamOverridePath(path string) ([]string, error) {
	raw := strings.Split(strings.TrimSpace(path), ".")
	if len(raw) == 0 || len(raw) > maxParamOverridePathDepth {
		return nil, invalidParamOverride("Param override path depth is invalid.")
	}
	parts := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, invalidParamOverride("Param override path contains an empty segment.")
		}
		parts = append(parts, item)
	}
	return parts, nil
}

func getJSONPathAny(value any, parts []string) (any, bool, error) {
	if len(parts) == 0 {
		return value, true, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		next, ok := typed[parts[0]]
		if !ok {
			return nil, false, nil
		}
		return getJSONPathAny(next, parts[1:])
	case []any:
		index, err := strconv.Atoi(parts[0])
		if err != nil || index < 0 || index >= len(typed) {
			return nil, false, invalidParamOverride("Param override array index is invalid.")
		}
		return getJSONPathAny(typed[index], parts[1:])
	default:
		return nil, false, nil
	}
}

func setJSONPathAny(value any, parts []string, nextValue any) (any, bool, error) {
	if len(parts) == 0 {
		return nextValue, true, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		key := parts[0]
		if len(parts) == 1 {
			typed[key] = nextValue
			return typed, true, nil
		}
		child, ok := typed[key]
		if !ok {
			child = map[string]any{}
		}
		if _, ok := child.(map[string]any); !ok {
			if _, ok := child.([]any); !ok {
				child = map[string]any{}
			}
		}
		updated, changed, err := setJSONPathAny(child, parts[1:], nextValue)
		if err != nil || !changed {
			return typed, changed, err
		}
		typed[key] = updated
		return typed, true, nil
	case []any:
		index, err := strconv.Atoi(parts[0])
		if err != nil || index < 0 || index >= len(typed) {
			return typed, false, invalidParamOverride("Param override array index is invalid.")
		}
		if len(parts) > 1 {
			child := typed[index]
			if _, ok := child.(map[string]any); !ok {
				if _, ok := child.([]any); !ok {
					child = map[string]any{}
				}
			}
			updated, changed, err := setJSONPathAny(child, parts[1:], nextValue)
			if err != nil || !changed {
				return typed, changed, err
			}
			typed[index] = updated
			return typed, true, nil
		}
		typed[index] = nextValue
		return typed, true, nil
	default:
		return value, false, invalidParamOverride("Param override path target is invalid.")
	}
}

func deleteJSONPathAny(value any, parts []string) (any, bool, error) {
	if len(parts) == 0 {
		return value, false, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		key := parts[0]
		if len(parts) == 1 {
			if _, ok := typed[key]; ok {
				delete(typed, key)
				return typed, true, nil
			}
			return typed, false, nil
		}
		child, ok := typed[key]
		if !ok {
			return typed, false, nil
		}
		updated, changed, err := deleteJSONPathAny(child, parts[1:])
		if err != nil || !changed {
			return typed, changed, err
		}
		typed[key] = updated
		return typed, true, nil
	case []any:
		index, err := strconv.Atoi(parts[0])
		if err != nil || index < 0 || index >= len(typed) {
			return typed, false, invalidParamOverride("Param override array index is invalid.")
		}
		if len(parts) == 1 {
			next := append([]any{}, typed[:index]...)
			next = append(next, typed[index+1:]...)
			return next, true, nil
		}
		updated, changed, err := deleteJSONPathAny(typed[index], parts[1:])
		if err != nil || !changed {
			return typed, changed, err
		}
		typed[index] = updated
		return typed, true, nil
	default:
		return value, false, nil
	}
}

func appendOverrideItems(value any) []any {
	if array, ok := arrayValue(value); ok {
		return array
	}
	return []any{value}
}

func cloneJSONValue(value any) any {
	switch value.(type) {
	case map[string]any, []any:
		body, err := json.Marshal(value)
		if err != nil {
			return value
		}
		var cloned any
		if err := json.Unmarshal(body, &cloned); err != nil {
			return value
		}
		return cloned
	default:
		return value
	}
}

func validHTTPStatusCode(status int) bool {
	return status >= 100 && status <= 599
}

func invalidParamOverride(message string) error {
	return upstreamUnavailable("invalid_param_override", message)
}
