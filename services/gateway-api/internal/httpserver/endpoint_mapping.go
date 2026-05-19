package httpserver

import (
	"net/http"
	"net/url"
	"strings"
)

type routeEndpointMapping struct {
	Path   string
	Method string
}

func applyCustomEndpointMapping(original *http.Request, route routeInfo, upstream *http.Request) error {
	if original == nil || original.URL == nil || upstream == nil || upstream.URL == nil {
		return nil
	}
	mapping, ok := customEndpointMappingForRoute(route, endpointFromPath(original.URL.Path))
	if !ok {
		return nil
	}
	if mapping.Method != "" {
		upstream.Method = mapping.Method
	}
	return applyMappedPath(upstream.URL, mapping.Path)
}

func applyCustomWebSocketEndpointMapping(original *http.Request, route routeInfo, upstreamURL string) (string, error) {
	if original == nil || original.URL == nil {
		return upstreamURL, nil
	}
	mapping, ok := customEndpointMappingForRoute(route, endpointFromPath(original.URL.Path))
	if !ok {
		return upstreamURL, nil
	}
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		return "", err
	}
	if err := applyMappedPath(parsed, mapping.Path); err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func customEndpointMappingForRoute(route routeInfo, endpoint string) (routeEndpointMapping, bool) {
	for _, metadata := range routeMetadataMaps(route) {
		for _, key := range []string{"endpoint_mapping", "endpoint_mappings", "custom_endpoint_mapping", "custom_endpoint_mappings"} {
			mappings, ok := objectValue(metadata[key])
			if !ok {
				continue
			}
			for _, endpointKey := range endpointMappingKeys(endpoint) {
				if mapping, ok := parseRouteEndpointMapping(mappings[endpointKey]); ok {
					return mapping, true
				}
			}
		}
	}
	return routeEndpointMapping{}, false
}

func endpointMappingKeys(endpoint string) []string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}
	keys := []string{endpoint}
	dash := strings.ReplaceAll(endpoint, "_", "-")
	if dash != endpoint {
		keys = append(keys, dash)
	}
	return keys
}

func parseRouteEndpointMapping(value any) (routeEndpointMapping, bool) {
	if path := metadataString(value); path != "" {
		return routeEndpointMapping{Path: path}, true
	}
	object, ok := objectValue(value)
	if !ok {
		return routeEndpointMapping{}, false
	}
	path := firstNonEmpty(metadataString(object["path"]), metadataString(object["url_path"]), metadataString(object["pathname"]))
	if path == "" {
		return routeEndpointMapping{}, false
	}
	method := strings.ToUpper(strings.TrimSpace(metadataString(object["method"])))
	if !validCustomEndpointMethod(method) {
		method = ""
	}
	return routeEndpointMapping{Path: path, Method: method}, true
}

func applyMappedPath(target *url.URL, mappedPath string) error {
	mappedPath = strings.TrimSpace(mappedPath)
	if mappedPath == "" {
		return nil
	}
	parsed, err := url.Parse(mappedPath)
	if err != nil {
		return upstreamUnavailable("invalid_endpoint_mapping", "Endpoint mapping path is invalid.")
	}
	if parsed.IsAbs() || parsed.Host != "" {
		return upstreamUnavailable("invalid_endpoint_mapping", "Endpoint mapping must not override upstream host.")
	}
	if !strings.HasPrefix(parsed.Path, "/") {
		return upstreamUnavailable("invalid_endpoint_mapping", "Endpoint mapping path must start with '/'.")
	}
	target.Path = parsed.Path
	target.RawPath = ""
	if parsed.RawQuery != "" {
		target.RawQuery = mergeMappedRawQuery(target.RawQuery, parsed.RawQuery)
	}
	return nil
}

func mergeMappedRawQuery(existing string, mapped string) string {
	if mapped == "" {
		return existing
	}
	if existing == "" {
		return mapped
	}
	values, err := url.ParseQuery(existing)
	if err != nil {
		if strings.TrimSpace(existing) == "" {
			return mapped
		}
		return existing + "&" + mapped
	}
	mappedValues, err := url.ParseQuery(mapped)
	if err != nil {
		return existing + "&" + mapped
	}
	for key, value := range mappedValues {
		values[key] = value
	}
	return values.Encode()
}

func validCustomEndpointMethod(method string) bool {
	switch method {
	case "", http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
