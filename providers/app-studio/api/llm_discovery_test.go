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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	asclient "github.com/railgrid/provider-app-studio/client"
)

func TestDiscoverProjectLLMModelsOpenAICompatible(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer workspace-key" {
			t.Errorf("authorization = %q, want workspace credential", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"text-embedding-3-large"},{"id":"custom-chat"},{"id":"gpt-6.1-sol"}]}`))
	}))
	defer upstream.Close()

	response, err := discoverProjectLLMModels(t.Context(), upstream.Client(), projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  upstream.URL + "/v1",
		APIKey:   "workspace-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Source != defaultProjectLLMProvider || len(response.Models) != 3 {
		t.Fatalf("response = %#v, want three OpenAI-compatible models", response)
	}
	if response.Models[0].ID != "gpt-6.1-sol" || response.Models[0].Compatibility != "recommended" {
		t.Fatalf("first model = %#v, want recommended gpt-6.1-sol", response.Models[0])
	}
	if response.Models[1].ID != "custom-chat" || response.Models[1].Compatibility != "available" {
		t.Fatalf("second model = %#v, want available custom-chat", response.Models[1])
	}
	if response.Models[2].ID != "text-embedding-3-large" || response.Models[2].Compatibility != "unsuitable" {
		t.Fatalf("third model = %#v, want unsuitable embedding model", response.Models[2])
	}
}

func TestDiscoverProjectLLMModelsGoogleAIStudio(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" || r.URL.Query().Get("pageSize") != "1000" {
			t.Errorf("request URL = %q, want /v1beta/models?pageSize=1000", r.URL.String())
		}
		if got := r.Header.Get("x-goog-api-key"); got != "google-key" {
			t.Errorf("x-goog-api-key = %q, want workspace credential", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro","supportedGenerationMethods":["generateContent"]},{"name":"models/text-embedding-004","displayName":"Text Embedding 004","supportedGenerationMethods":["embedContent"]}]}`))
	}))
	defer upstream.Close()

	response, err := discoverProjectLLMModels(t.Context(), upstream.Client(), projectLLMSettings{
		Provider: projectLLMProviderGoogle,
		BaseURL:  upstream.URL,
		APIKey:   "google-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Source != projectLLMProviderGoogle || len(response.Models) != 2 {
		t.Fatalf("response = %#v, want two Google models", response)
	}
	if response.Models[0].ID != "gemini-2.5-pro" || response.Models[0].Compatibility != "recommended" {
		t.Fatalf("first model = %#v, want recommended Gemini model", response.Models[0])
	}
	if response.Models[1].ID != "text-embedding-004" || response.Models[1].Compatibility != "unsuitable" {
		t.Fatalf("second model = %#v, want unsuitable embedding model", response.Models[1])
	}
}

func TestDiscoverProjectLLMModelsHandlerReusesStoredCredential(t *testing.T) {
	var receivedCredential string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCredential = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.4"}]}`))
	}))
	defer upstream.Close()

	registry := projectLLMRegistry{Models: []projectLLMModelSettings{{
		ID:   "gpt-high",
		Name: "GPT High",
		Settings: projectLLMSettings{
			Provider:        defaultProjectLLMProvider,
			BaseURL:         upstream.URL,
			Model:           "gpt-5.4",
			ReasoningEffort: "high",
			APIKey:          "stored-workspace-key",
		},
	}, {
		ID:   "unrelated",
		Name: "Unrelated",
		Settings: projectLLMSettings{
			Provider:        defaultProjectLLMProvider,
			BaseURL:         "https://gateway.example/v1",
			Model:           "custom-model",
			ReasoningEffort: "high",
			APIKey:          "other-workspace-key",
		},
	}}}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{registry: &registry})
	server := &Server{
		tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a")}.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders,
		projectClientFor:       func(identity) (*asclient.Client, error) { return client, nil },
		llmDiscoveryHTTPClient: upstream.Client(),
	}
	request := projectLLMDiscoveryRequest(t, `{"provider":"openai-compatible","baseURL":"`+upstream.URL+`","existingModelID":"gpt-high"}`)
	response := httptest.NewRecorder()

	server.discoverProjectLLMModels(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if receivedCredential != "Bearer stored-workspace-key" {
		t.Fatalf("authorization = %q, want stored workspace credential", receivedCredential)
	}
	var body ProjectLLMModelDiscoveryResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Models) != 1 || body.Models[0].ID != "gpt-5.4" {
		t.Fatalf("response = %#v, want discovered model", body)
	}
}

func TestDiscoverProjectLLMModelsHandlerRequiresCredential(t *testing.T) {
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{})
	server := &Server{tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a")}.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
	response := httptest.NewRecorder()

	server.discoverProjectLLMModels(response, projectLLMDiscoveryRequest(t, `{"provider":"openai-compatible","baseURL":"https://api.openai.com/v1"}`))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "enter a credential before finding models") {
		t.Fatalf("body = %q, want credential guidance", response.Body.String())
	}
}

func TestDiscoverProjectLLMModelsHandlerDoesNotReuseCredentialForChangedEndpoint(t *testing.T) {
	registry := projectLLMRegistry{Models: []projectLLMModelSettings{{
		ID:   "gpt-high",
		Name: "GPT High",
		Settings: projectLLMSettings{
			Provider: defaultProjectLLMProvider,
			BaseURL:  "https://api.openai.com/v1",
			Model:    "gpt-5.4",
			APIKey:   "stored-workspace-key",
		},
	}}}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{registry: &registry})
	server := &Server{tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a")}.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
	response := httptest.NewRecorder()

	server.discoverProjectLLMModels(response, projectLLMDiscoveryRequest(t, `{"provider":"openai-compatible","baseURL":"https://gateway.example/v1","existingModelID":"gpt-high"}`))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "changed provider or endpoint") {
		t.Fatalf("body = %q, want changed-endpoint credential guidance", response.Body.String())
	}
}

func projectLLMDiscoveryRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/llm-settings/models/discover", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Railgrid-Tenant", "cluster-a")
	request = stampTestCaller(request, testUserForToken("test-token"))
	request.Header.Set("X-Railgrid-Cluster", "cluster-a")
	return request
}

func TestRecommendationsDistinguishCurrentModelsFromLegacyAndVariants(t *testing.T) {
	for _, id := range []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6.1-terra", "gpt-6-luna", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-1", "openai/gpt-6.1-sol"} {
		if got := projectLLMDiscoveredModel(id, "", defaultProjectLLMProvider, nil); got.Compatibility != "recommended" {
			t.Errorf("%s: %s", id, got.Compatibility)
		}
	}
	for _, id := range []string{"gpt-4o", "gpt-5.4", "claude-sonnet-4", "claude-opus-4-5", "gpt-6.1-sol-custom", "claude-opus-5-50"} {
		if got := projectLLMDiscoveredModel(id, "", defaultProjectLLMProvider, nil); got.Compatibility != "available" {
			t.Errorf("%s: %s", id, got.Compatibility)
		}
	}
}

func TestDiscoverProjectAnthropicModels(t *testing.T) {
	for _, endpoint := range []string{"https://api.anthropic.com/v1", "https://openrouter.ai/api/v1", "https://api.anthropic.com.example.org/v1"} {
		t.Run(endpoint, func(t *testing.T) {
			calls := 0
			official := endpoint == "https://api.anthropic.com/v1"
			client := &http.Client{Transport: projectResponsesFixtureRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") != "Bearer key" {
					t.Error("missing credential")
				}
				body := `{"data":[{"id":"claude-sonnet-5-5","display_name":"Sonnet"}],"has_more":false}`
				if official {
					if r.Header.Get("anthropic-version") != "2023-06-01" || r.URL.Query().Get("limit") != "1000" {
						t.Error("missing Anthropic request options")
					}
					if calls == 1 {
						body = `{"data":[{"id":"claude-opus-5-5"}],"has_more":true,"last_id":"claude-opus-5-5"}`
					} else if r.URL.Query().Get("after_id") != "claude-opus-5-5" {
						t.Error("missing pagination cursor")
					}
				} else if r.Header.Get("anthropic-version") != "" || r.URL.RawQuery != "" {
					t.Error("Anthropic options leaked to another endpoint")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			result, err := discoverProjectLLMModels(t.Context(), client, projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: endpoint, APIKey: "key"})
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if official {
				want = 2
			}
			if calls != want || len(result.Models) != want {
				t.Fatalf("calls=%d models=%v", calls, result.Models)
			}
		})
	}
}

func TestDiscoverProjectAnthropicModelsRejectsInvalidPagination(t *testing.T) {
	for _, body := range []string{
		`{"data":[],"has_more":true}`, `{"data":[{"id":"claude-sonnet-5-5"}],"has_more":true,"last_id":"repeat"}`, `{"data":[]}`,
	} {
		calls := 0
		client := &http.Client{Transport: projectResponsesFixtureRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		_, err := discoverProjectLLMModels(t.Context(), client, projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: "https://api.anthropic.com/v1", APIKey: "key"})
		if err == nil || calls > 2 {
			t.Fatalf("expected bounded failure, calls=%d error=%v", calls, err)
		}
	}
}
