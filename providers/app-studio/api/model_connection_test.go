// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	asclient "github.com/faroshq/provider-app-studio/client"
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
	secret, err := projectLLMRegistrySecret(registry)
	if err != nil {
		t.Fatal(err)
	}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: secret})
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
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
