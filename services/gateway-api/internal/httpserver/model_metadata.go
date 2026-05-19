package httpserver

func appendModelDisplayMetadata(item map[string]any, raw string) {
	metadata := metadataMapFromJSON(raw)
	if len(metadata) == 0 {
		return
	}
	for _, key := range []string{"description", "icon", "vendor", "pricing_version"} {
		if value := metadataString(metadata[key]); value != "" {
			item[key] = value
		}
	}
	if tags := metadataStringList(metadata["tags"]); len(tags) > 0 {
		item["tags"] = tags
	}
	if endpoints := metadataStringList(metadata["supported_endpoint_types"]); len(endpoints) > 0 {
		item["supported_endpoint_types"] = endpoints
	}
}
