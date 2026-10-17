// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	asclient "github.com/railgrid/provider-app-studio/client"
)

func TestSavedModelConnectionCredentialBoundary(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer stored-key" || r.URL.Path != "/chat/completions" {
			t.Error("wrong credential or probe endpoint")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	registry := projectLLMRegistry{Models: []projectLLMModelSettings{{ID: "main", Name: "Main", Settings: projectLLMSettings{Provider: "openai-compatible", BaseURL: upstream.URL, Model: "gpt-4o", APIKey: "stored-key"}}}}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{registry: &registry})
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
	for _, tt := range []struct {
		name, id, endpoint, provider string
		status                       int
	}{
		{"reuse", "main", upstream.URL, "openai-compatible", 200},
		{"changed endpoint", "main", upstream.URL + "/changed", "openai-compatible", 400},
		{"changed provider", "main", upstream.URL, "google-ai-studio", 400},
		{"missing model", "missing", upstream.URL, "openai-compatible", 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(TestProjectLLMConnectionRequest{ExistingModelID: tt.id, Provider: tt.provider, BaseURL: tt.endpoint, Model: "gpt-4o"})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			server.testProjectLLMConnection(response, projectLLMDiscoveryRequest(t, string(body)))
			if response.Code != tt.status {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "stored-key") {
				t.Fatal("response leaked key")
			}
		})
	}
	if calls != 1 {
		t.Fatalf("unexpected upstream calls: %d", calls)
	}
	view := registry.view()
	if view.Models[0].Catalog == nil || view.Models[0].Catalog.InputPer1M != 2.5 {
		t.Fatalf("missing shared catalog estimate: %#v", view.Models[0])
	}
}

func TestSavedModelConnectionCanResetInvalidReasoningWithoutCredential(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer stored-key" || r.URL.Path != "/chat/completions" {
			t.Errorf("wrong credential or probe endpoint: authorization=%q path=%q", r.Header.Get("Authorization"), r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	registry := projectLLMRegistry{Models: []projectLLMModelSettings{
		{ID: "main", RevisionID: "revision-main", Name: "Main", Settings: projectLLMSettings{
			Provider: defaultProjectLLMProvider, BaseURL: upstream.URL, Model: "gpt-4o", ReasoningEffort: "high", APIKey: "stored-key",
		}},
		{ID: "unrelated", RevisionID: "revision-unrelated", Name: "Unrelated", Settings: projectLLMSettings{
			Provider: defaultProjectLLMProvider, BaseURL: "https://gateway.example/v1", Model: "custom-model", ReasoningEffort: "high", APIKey: "other-key",
		}},
	}}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{registry: &registry})
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		tenantProviders:  defaultTestProviders,
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
	}

	requestBody, err := json.Marshal(TestProjectLLMConnectionRequest{
		ExistingModelID: "main",
		Provider:        defaultProjectLLMProvider,
		BaseURL:         upstream.URL,
		Model:           "gpt-4o",
		ReasoningEffort: reasoningEffortPointer(""),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.testProjectLLMConnection(response, projectLLMDiscoveryRequest(t, string(requestBody)))
	if response.Code != http.StatusOK {
		t.Fatalf("explicit automatic reset returned %d: %s", response.Code, response.Body.String())
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want one after explicit reset", calls)
	}
	if strings.Contains(response.Body.String(), "stored-key") {
		t.Fatal("response leaked the stored credential")
	}

	// An omitted choice still inherits the saved default. Since this stored
	// value is invalid for the custom connection, the test must reject it.
	requestBody, err = json.Marshal(TestProjectLLMConnectionRequest{
		ExistingModelID: "main",
		Provider:        defaultProjectLLMProvider,
		BaseURL:         upstream.URL,
		Model:           "gpt-4o",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	server.testProjectLLMConnection(response, projectLLMDiscoveryRequest(t, string(requestBody)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "reasoning effort") {
		t.Fatalf("omitted reasoning choice returned %d: %s; want saved-default validation", response.Code, response.Body.String())
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d after invalid inherited default, want one", calls)
	}
}

func TestSavedModelConnectionInheritsReasoningWithReplacementCredential(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer replacement-key" || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("wrong credential or probe endpoint: authorization=%q path=%q", r.Header.Get("Authorization"), r.URL.Path)
		}
		var payload struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		if payload.ReasoningEffort != "high" {
			t.Errorf("reasoning_effort = %q, want inherited saved default high", payload.ReasoningEffort)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse fixture endpoint: %v", err)
	}
	originalTransport := http.DefaultTransport
	http.DefaultTransport = &projectOfficialOpenAIInterceptTransport{base: originalTransport, target: target}
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	registry := projectLLMRegistry{Models: []projectLLMModelSettings{{
		ID: "main", RevisionID: "revision-main", Name: "Main", Settings: projectLLMSettings{
			Provider: defaultProjectLLMProvider, BaseURL: "https://gateway.example/v1", Model: "custom-model", ReasoningEffort: "high",
		},
	}}}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{registry: &registry})
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		tenantProviders:  defaultTestProviders,
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
	}

	requestBody, err := json.Marshal(TestProjectLLMConnectionRequest{
		ExistingModelID: "main",
		Provider:        defaultProjectLLMProvider,
		BaseURL:         defaultProjectLLMBaseURL,
		Model:           "gpt-5.1",
		APIKey:          "replacement-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.testProjectLLMConnection(response, projectLLMDiscoveryRequest(t, string(requestBody)))
	if response.Code != http.StatusOK {
		t.Fatalf("connection test returned %d: %s", response.Code, response.Body.String())
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want one", calls)
	}
}
