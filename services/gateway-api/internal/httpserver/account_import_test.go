package httpserver

import "testing"

func TestCredentialForImportPayloadAppliesTemplateProvider(t *testing.T) {
	template, ok := accountImportTemplateByID("codex")
	if !ok {
		t.Fatal("codex template missing")
	}
	bundle, hasAuthState, present, err := credentialForImportPayload(accountImportPayload{APIKey: "tok"}, template)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("credential should be present")
	}
	if !hasAuthState {
		t.Fatal("codex import should create auth state")
	}
	if bundle.Type != "oauth" {
		t.Fatalf("bundle type = %q", bundle.Type)
	}
	if bundle.Provider != "openai_codex" {
		t.Fatalf("bundle provider = %q", bundle.Provider)
	}
	if bundle.AuthScheme != "bearer" {
		t.Fatalf("bundle auth scheme = %q", bundle.AuthScheme)
	}
}

func TestApplyImportMetadataMergesDefaults(t *testing.T) {
	template, ok := accountImportTemplateByID("gemini_cli")
	if !ok {
		t.Fatal("gemini_cli template missing")
	}
	metadata := applyImportMetadata(
		mergeMetadataMaps(template.Metadata, map[string]any{"route_tags": []any{"base"}}),
		accountImportRequest{PoolGroup: "premium", RouteTags: []string{"default"}},
		accountImportPayload{RouteTags: []string{"item"}, QuotaProjectID: "quota-project-1", QuotaEndpoint: "/usage/quota"},
		template,
	)
	if got := metadataText(metadata["pool_group"]); got != "premium" {
		t.Fatalf("pool_group = %q", got)
	}
	tags := metadataStringList(metadata["route_tags"])
	if len(tags) != 3 || tags[0] != "base" || tags[1] != "default" || tags[2] != "item" {
		t.Fatalf("route_tags = %#v", tags)
	}
	if got := metadataText(metadata["quota_endpoint"]); got != "/usage/quota" {
		t.Fatalf("quota_endpoint = %q", got)
	}
	gemini := metadataObject(metadata["gemini"])
	if got := metadataText(gemini["quota_project_id"]); got != "quota-project-1" {
		t.Fatalf("gemini.quota_project_id = %q", got)
	}
}

func TestAccountImportKeysFromRequestDeduplicatesDelimitedInput(t *testing.T) {
	keys := accountImportKeysFromRequest(accountKeyImportRequest{
		Keys: []string{" sk-1 ", "sk-2"},
		Text: "sk-2\nsk-3, sk-4;sk-1",
	})
	expected := []string{"sk-1", "sk-2", "sk-3", "sk-4"}
	if len(keys) != len(expected) {
		t.Fatalf("keys = %#v", keys)
	}
	for index := range expected {
		if keys[index] != expected[index] {
			t.Fatalf("keys = %#v", keys)
		}
	}
}
