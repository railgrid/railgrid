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

package modelcatalog

import (
	"encoding/json"
	"os"
	"testing"
)

func TestReasoningProfileForConnection(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		baseURL  string
		model    string
		wantID   string
		wantOK   bool
	}{
		{name: "blank provider and base URL are official defaults", model: "gpt-6-luna", wantID: "gpt-6-luna", wantOK: true},
		{name: "GPT-5 mini dated alias maps to its profile", provider: "openai-compatible", baseURL: "https://api.openai.com/v1", model: "gpt-5-mini-2025-08-07", wantID: "gpt-5-mini", wantOK: true},
		{name: "o3 dated alias maps to its profile", provider: "openai-compatible", baseURL: "https://api.openai.com/v1", model: "o3-2025-04-16", wantID: "o3", wantOK: true},
		{name: "namespaced ID is not an official model ID", provider: "openai-compatible", baseURL: "https://api.openai.com/v1", model: "openai/gpt-6-luna"},
		{name: "unknown exact model", provider: "openai-compatible", baseURL: "https://api.openai.com/v1", model: "gpt-6-luna-preview"},
		{name: "date suffix is not inferred", provider: "openai-compatible", baseURL: "https://api.openai.com/v1", model: "gpt-6-luna-2026-10-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, ok := ReasoningProfileForConnection(tt.provider, tt.baseURL, tt.model)
			if ok != tt.wantOK {
				t.Fatalf("ReasoningProfileForConnection() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && profile.ID != tt.wantID {
				t.Fatalf("ReasoningProfileForConnection() profile ID = %q, want %q", profile.ID, tt.wantID)
			}
		})
	}
}

func TestUsesOfficialOpenAIEndpointSharedFixtures(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name     string `json:"name"`
			Provider string `json:"provider"`
			BaseURL  string `json:"baseURL"`
			Official bool   `json:"official"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("testdata/endpoints.json")
	if err != nil {
		t.Fatalf("read shared endpoint fixtures: %v", err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode shared endpoint fixtures: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("shared endpoint fixture has no cases")
	}

	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if got := usesOfficialOpenAIEndpoint(tc.Provider, tc.BaseURL); got != tc.Official {
				t.Errorf("usesOfficialOpenAIEndpoint(%q, %q) = %v, want %v", tc.Provider, tc.BaseURL, got, tc.Official)
			}
		})
	}
}

func TestValidateReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		effort  string
		baseURL string
		wantOK  bool
	}{
		{name: "empty value keeps provider default", model: "custom-model", effort: "", wantOK: true},
		{name: "GPT-6 Astra max", model: "gpt-6-astra", effort: "max", wantOK: true},
		{name: "GPT-6 Astra rejects none", model: "gpt-6-astra", effort: "none"},
		{name: "GPT-6 Luna none", model: "gpt-6-luna", effort: "none", wantOK: true},
		{name: "GPT-5 minimal", model: "gpt-5", effort: "minimal", wantOK: true},
		{name: "GPT-5 rejects none", model: "gpt-5", effort: "none"},
		{name: "exact effort casing", model: "gpt-5.1", effort: "High"},
		{name: "no effort trimming", model: "gpt-5.1", effort: " high "},
		{name: "custom endpoint cannot configure effort", model: "gpt-6-luna", effort: "high", baseURL: "https://llm.example.test/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL := "https://api.openai.com/v1"
			if tt.baseURL != "" {
				baseURL = tt.baseURL
			}
			err := ValidateReasoningEffort("openai-compatible", baseURL, tt.model, tt.effort)
			if (err == nil) != tt.wantOK {
				t.Fatalf("ValidateReasoningEffort() error = %v, want success %v", err, tt.wantOK)
			}
		})
	}
}

func TestReasoningProfilesReturnsCopies(t *testing.T) {
	profiles := ReasoningProfiles()
	if len(profiles) == 0 {
		t.Fatal("ReasoningProfiles() returned no entries")
	}
	profiles[0].Efforts[0] = "mutated"
	profiles[0].Aliases = append(profiles[0].Aliases, "mutated")

	for _, profile := range ReasoningProfiles() {
		if profile.Efforts[0] == "mutated" {
			t.Fatal("ReasoningProfiles() exposed its internal effort slice")
		}
		for _, alias := range profile.Aliases {
			if alias == "mutated" {
				t.Fatal("ReasoningProfiles() exposed its internal alias slice")
			}
		}
	}
}

func TestReasoningProfileForConnectionReturnsCopies(t *testing.T) {
	profile, ok := ReasoningProfileForConnection("openai-compatible", "", "gpt-5")
	if !ok {
		t.Fatal("expected GPT-5 profile")
	}
	profile.Efforts[0] = "mutated"
	profile.Aliases[0] = "mutated"

	profile, ok = ReasoningProfileForConnection("openai-compatible", "", "gpt-5")
	if !ok || profile.Efforts[0] == "mutated" || profile.Aliases[0] == "mutated" {
		t.Fatalf("ReasoningProfileForConnection() exposed internal slices: %#v", profile)
	}
}
