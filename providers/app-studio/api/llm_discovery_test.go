// Copyright 2026 The Faros Authors.
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
	"strings"
	"testing"

	asclient "github.com/faroshq/provider-app-studio/client"
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
		_, _ = w.Write([]byte(`{"data":[{"id":"text-embedding-3-large"},{"id":"custom-chat"},{"id":"gpt-5.4"}]}`))
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
	if response.Models[0].ID != "gpt-5.4" || response.Models[0].Compatibility != "recommended" {
		t.Fatalf("first model = %#v, want recommended gpt-5.4", response.Models[0])
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
			Provider: defaultProjectLLMProvider,
			BaseURL:  upstream.URL,
			Model:    "gpt-5.4",
			APIKey:   "stored-workspace-key",
		},
	}}}
	secret, err := projectLLMRegistrySecret(registry)
	if err != nil {
		t.Fatal(err)
	}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: secret})
	server := &Server{
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
	server := &Server{projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
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
	secret, err := projectLLMRegistrySecret(registry)
	if err != nil {
		t.Fatal(err)
	}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: secret})
	server := &Server{projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
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
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org-a:workspace-a")
	request.Header.Set("X-Faros-Cluster", "cluster-a")
	return request
}
