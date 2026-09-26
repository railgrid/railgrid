/*
Copyright 2026 The Railgrid Authors.

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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
)

func TestListHandlerIncludesActionDiscoveryMetadataWithoutTransportURLs(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{
		Name:           "actions",
		DisplayName:    "Actions",
		EndpointsValid: true,
		BackendURL:     mustProviderURL(t, "https://provider.example/backend"),
		APIExportName:  "actions.providers.railgrid.ai",
		APIExportPath:  "root:railgrid:providers:actions",
		Export: &providersv1alpha1.ProviderExport{
			Name: "actions.providers.railgrid.ai",
			Resources: []providersv1alpha1.ProviderExportResource{{
				Name:       "widgets",
				APIVersion: "example.railgrid.ai/v1alpha1",
				Kind:       "Widget",
				Verbs: []providersv1alpha1.ProviderVerb{{
					Name:        "logs",
					Description: "Stream the widget's logs.",
					Stream:      true,
					ReadOnly:    true,
				}},
				Actions: []providersv1alpha1.ProviderAction{{
					Name:          "mutate",
					Version:       "v1",
					DisplayName:   "Mutate",
					Description:   "Mutates one bound resource.",
					InputSchema:   &runtime.RawExtension{Raw: []byte(`{"type":"object"}`)},
					OutputSchema:  &runtime.RawExtension{Raw: []byte(`{"type":"object"}`)},
					SchemaDigest:  "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
					ExecutionMode: providersv1alpha1.ProviderActionExecutionSync,
					ReadOnly:      false,
					Risk:          providersv1alpha1.ProviderActionRiskHigh,
					Idempotency:   providersv1alpha1.ProviderActionIdempotencyKeyed,
					Limits: providersv1alpha1.ProviderActionLimits{
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
			}},
		},
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
	export, ok := item["export"].(map[string]any)
	if !ok {
		t.Fatalf("export = %#v, want object", item["export"])
	}
	if export["name"] != "actions.providers.railgrid.ai" || export["path"] != "root:railgrid:providers:actions" {
		t.Errorf("export coordinates = %#v", export)
	}
	resources, ok := export["resources"].([]any)
	if !ok || len(resources) != 1 {
		t.Fatalf("export.resources = %#v, want one resource", export["resources"])
	}
	resource, ok := resources[0].(map[string]any)
	if !ok {
		t.Fatalf("export resource = %#v, want object", resources[0])
	}
	// The coordinate a caller addresses is (this resource, that verb), so the
	// apiVersion and kind are declared once on the resource and never repeated
	// per action.
	if resource["name"] != "widgets" || resource["apiVersion"] != "example.railgrid.ai/v1alpha1" || resource["kind"] != "Widget" {
		t.Errorf("export resource = %#v", resource)
	}
	verbs, ok := resource["verbs"].([]any)
	if !ok || len(verbs) != 1 {
		t.Fatalf("resource.verbs = %#v, want one verb", resource["verbs"])
	}
	verb, _ := verbs[0].(map[string]any)
	if verb["name"] != "logs" || verb["stream"] != true || verb["readOnly"] != true {
		t.Errorf("verb = %#v", verbs[0])
	}
	actions, ok := resource["actions"].([]any)
	if !ok || len(actions) != 1 {
		t.Fatalf("resource.actions = %#v, want one action", resource["actions"])
	}
	action, ok := actions[0].(map[string]any)
	if !ok {
		t.Fatalf("action = %#v, want object", actions[0])
	}
	for _, field := range []string{
		"id", "name", "version", "displayName", "description", "inputSchema", "outputSchema",
		"schemaDigest", "executionMode", "readOnly", "risk", "idempotency", "limits", "consent", "deprecation",
	} {
		if _, ok := action[field]; !ok {
			t.Errorf("action missing discovery field %q: %#v", field, action)
		}
	}
	// The resource an action is bound to is its parent entry, so a per-action
	// bound-resource block would be a second place for it to disagree.
	if _, ok := action["boundResource"]; ok {
		t.Errorf("action published a boundResource: %#v", action)
	}
	if _, ok := item["backendURL"]; ok {
		t.Error("provider discovery response exposed backendURL")
	}
	hub, ok := item["hub"].(map[string]any)
	if !ok {
		t.Fatalf("hub = %#v, want object", item["hub"])
	}
	skills, ok := hub["assistantSkills"].([]any)
	if !ok || len(skills) != 1 {
		t.Fatalf("hub.assistantSkills = %#v, want one inline package", hub["assistantSkills"])
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
	// The identity a grant, a consent record and the assistant catalog key on
	// stays "<name>/<version>", even though the coordinate kcp routes on is the
	// name alone.
	if got := action["id"]; got != "mutate/v1" {
		t.Errorf("action id = %#v, want mutate/v1", got)
	}
	consent, ok := action["consent"].(map[string]any)
	if !ok || consent["required"] != true || consent["scope"] != "resource" {
		t.Errorf("consent = %#v", action["consent"])
	}
}

// The Enable dialog is built from requires[], so the catalog response has to
// carry it exactly as declared: the provider a requirement names (which is also
// the dependency edge), the verbs it asks for on a plain kind, the absence of
// verbs on a verb coordinate, and the selector that narrows a core claim.
func TestListHandlerProjectsRequirements(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{
		Name:           "app-studio",
		DisplayName:    "App Studio",
		EndpointsValid: true,
		APIExportName:  "ai.providers.railgrid.ai",
		Requires: []providersv1alpha1.ProviderRequirement{{
			Provider: "infrastructure",
			Group:    "infrastructure.railgrid.ai",
			Resources: []providersv1alpha1.ProviderRequiredResource{
				{Name: "instances", Verbs: []providersv1alpha1.ProviderRequiredVerb{
					providersv1alpha1.RequiredVerbGet, providersv1alpha1.RequiredVerbCreate,
				}},
				{Name: "instances/exec"},
			},
		}, {
			Resources: []providersv1alpha1.ProviderRequiredResource{{
				Name:     "secrets",
				Verbs:    []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet},
				Selector: &providersv1alpha1.ProviderLabelSelector{MatchLabels: map[string]string{"railgrid.ai/owner": "app-studio"}},
			}},
		}},
	})

	r := httptest.NewRequest(http.MethodGet, PathListProviders, nil)
	w := httptest.NewRecorder()
	NewListHandler(reg).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var raw struct {
		Items []struct {
			Requires []providersv1alpha1.ProviderRequirement `json:"requires"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(raw.Items) != 1 || len(raw.Items[0].Requires) != 2 {
		t.Fatalf("requires = %#v, want two entries", raw.Items)
	}
	infra := raw.Items[0].Requires[0]
	if infra.Provider != "infrastructure" || infra.Group != "infrastructure.railgrid.ai" {
		t.Errorf("first requirement = %#v", infra)
	}
	if len(infra.Resources) != 2 || len(infra.Resources[0].Verbs) != 2 {
		t.Fatalf("infrastructure resources = %#v", infra.Resources)
	}
	// A verb coordinate carries no verbs: the verb IS the capability.
	if infra.Resources[1].Name != "instances/exec" || len(infra.Resources[1].Verbs) != 0 {
		t.Errorf("verb coordinate = %#v", infra.Resources[1])
	}
	core := raw.Items[0].Requires[1]
	if core.Provider != "" || core.Group != "" {
		t.Errorf("core requirement named a provider or group: %#v", core)
	}
	if core.Resources[0].Selector == nil || core.Resources[0].Selector.MatchLabels["railgrid.ai/owner"] != "app-studio" {
		t.Errorf("core secrets claim lost its selector: %#v", core.Resources[0])
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

func TestListHandlerProjectsOnlySanitizedFailureReadiness(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{
		Name:                  "code",
		DisplayName:           "Code",
		EndpointsValid:        true,
		BackendURL:            mustProviderURL(t, "https://secret.internal.invalid"),
		BackendHealthRequired: true,
		BackendHealthy:        false,
	})
	reg.Upsert(Provider{
		Name:           "healthy",
		DisplayName:    "Healthy",
		EndpointsValid: true,
		BackendURL:     mustProviderURL(t, "https://healthy.internal.invalid"),
		BackendHealthy: true,
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
	for _, item := range raw.Items {
		byName[item["name"].(string)] = item
	}
	if got := byName["code"]["readinessReason"]; got != "BackendUnhealthy" {
		t.Fatalf("code readinessReason = %#v", got)
	}
	if got := byName["code"]["readinessMessage"]; got != "Provider backend is unavailable." {
		t.Fatalf("code readinessMessage = %#v", got)
	}
	if strings.Contains(w.Body.String(), "secret.internal") {
		t.Fatalf("provider response leaked backend authority: %s", w.Body.String())
	}
	for _, field := range []string{"readinessReason", "readinessMessage"} {
		if _, ok := byName["healthy"][field]; ok {
			t.Fatalf("healthy provider emitted %s: %#v", field, byName["healthy"])
		}
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
