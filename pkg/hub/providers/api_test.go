/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package providers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
)

func TestListHandlerIncludesActionDiscoveryMetadataWithoutTransportURLs(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{
		Name:                "actions",
		DisplayName:         "Actions",
		EndpointsValid:      true,
		BackendURL:          mustProviderURL(t, "https://provider.example/backend"),
		VirtualWorkspaceURL: mustProviderURL(t, "https://provider.example/vw"),
		Actions: []ProviderAction{{
			ID:          "mutate/v1",
			Name:        "mutate",
			Version:     "v1",
			DisplayName: "Mutate",
			Description: "Mutates one bound resource.",
			Resource: ProviderActionResource{
				APIVersion: "example.faros.sh/v1alpha1",
				Kind:       "Widget",
				Resource:   "widgets",
			},
			InputSchema:   json.RawMessage(`{"type":"object"}`),
			OutputSchema:  json.RawMessage(`{"type":"object"}`),
			SchemaDigest:  "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			ExecutionMode: "sync",
			ReadOnly:      false,
			Risk:          providersv1alpha1.ProviderActionRiskHigh,
			Idempotency:   "keyed",
			Limits: ProviderActionLimits{
				TimeoutSeconds: 30,
				MaxInputBytes:  4096,
				MaxOutputBytes: 8192,
				MaxResultItems: 10,
			},
			Consent: providersv1alpha1.ProviderActionConsent{
				Required: true,
				Prompt:   "Allow mutation?",
				Scope:    "resource",
			},
			Deprecation: &providersv1alpha1.ProviderActionDeprecation{
				Deprecated:    true,
				Message:       "Use mutate/v2.",
				ReplacementID: "mutate/v2",
			},
		}},
		AssistantSkills: []ProviderAssistantSkill{{
			PackageName: "databricks-app-integration",
			Version:     "1.0.0",
			Digest:      "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Skill:       "---\nname: databricks-app-integration\ndescription: guidance\n---\nbody",
			Resources:   []ProviderAssistantSkillResource{{Path: "references/action-contract.md", Content: "contract"}},
		}},
	})

	r := httptest.NewRequest(http.MethodGet, PathListProviders, nil)
	w := httptest.NewRecorder()
	NewListHandler(reg).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, ok := raw["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v, want one provider", raw["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("provider item = %#v, want object", items[0])
	}
	actions, ok := item["actions"].([]any)
	if !ok || len(actions) != 1 {
		t.Fatalf("actions = %#v, want one action", item["actions"])
	}
	action, ok := actions[0].(map[string]any)
	if !ok {
		t.Fatalf("action = %#v, want object", actions[0])
	}
	for _, field := range []string{
		"id", "displayName", "description", "boundResource", "inputSchema", "outputSchema",
		"schemaDigest", "executionMode", "readOnly", "risk", "idempotency", "limits", "consent", "deprecation",
	} {
		if _, ok := action[field]; !ok {
			t.Errorf("action missing discovery field %q: %#v", field, action)
		}
	}
	if _, ok := item["backendURL"]; ok {
		t.Error("provider discovery response exposed backendURL")
	}
	if _, ok := item["virtualWorkspaceURL"]; ok {
		t.Error("provider discovery response exposed virtualWorkspaceURL")
	}
	skills, ok := item["assistantSkills"].([]any)
	if !ok || len(skills) != 1 {
		t.Fatalf("assistantSkills = %#v, want one inline package", item["assistantSkills"])
	}
	skill, ok := skills[0].(map[string]any)
	if !ok || skill["packageName"] != "databricks-app-integration" || skill["version"] != "1.0.0" {
		t.Fatalf("assistant skill = %#v", skills[0])
	}
	for _, field := range []string{"url", "backendURL", "token", "credential"} {
		if _, ok := skill[field]; ok {
			t.Errorf("assistant skill exposed forbidden field %q: %#v", field, skill)
		}
	}
	if got := action["id"]; got != "mutate/v1" {
		t.Errorf("action id = %#v, want mutate/v1", got)
	}
	bound, ok := action["boundResource"].(map[string]any)
	if !ok || bound["resource"] != "widgets" || bound["kind"] != "Widget" {
		t.Errorf("boundResource = %#v", action["boundResource"])
	}
	consent, ok := action["consent"].(map[string]any)
	if !ok || consent["required"] != true || consent["scope"] != "resource" {
		t.Errorf("consent = %#v", action["consent"])
	}
}

// The description is the only thing in the catalog response that tells a new
// user what a provider actually does — the portal's first-run welcome flow and
// the catalog cards both render it, and both degrade to a bare resource name
// without it. It reaches the registry from CatalogEntry.spec.description.
func TestListHandlerProjectsDescription(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{
		Name:           "edges",
		DisplayName:    "Edges",
		Description:    "Connect Kubernetes clusters and Linux servers as edges.",
		EndpointsValid: true,
		UIURL:          mustProviderURL(t, "https://provider.example/ui"),
	})
	// A provider whose CatalogEntry declares no description must omit the
	// field rather than emit an empty string, so the portal can tell "not
	// published" from "published as blank".
	reg.Upsert(Provider{
		Name:           "silent",
		DisplayName:    "Silent",
		EndpointsValid: true,
		UIURL:          mustProviderURL(t, "https://provider.example/ui"),
	})

	r := httptest.NewRequest(http.MethodGet, PathListProviders, nil)
	w := httptest.NewRecorder()
	NewListHandler(reg).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	byName := map[string]map[string]any{}
	for _, it := range raw.Items {
		name, _ := it["name"].(string)
		byName[name] = it
	}

	if got := byName["edges"]["description"]; got != "Connect Kubernetes clusters and Linux servers as edges." {
		t.Errorf("edges description = %#v, want the registry value", got)
	}
	if _, ok := byName["silent"]["description"]; ok {
		t.Errorf("silent emitted a description key: %#v", byName["silent"])
	}
}

func mustProviderURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return u
}
