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

import "testing"

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
		{name: "normalized official URL", provider: "OPENAI-COMPATIBLE", baseURL: "HTTPS://API.OPENAI.COM:443/v1/", model: "gpt-5-mini-2025-08-07", wantID: "gpt-5-mini", wantOK: true},
		{name: "official URL without explicit port", provider: "openai-compatible", baseURL: "https://api.openai.com/v1", model: "o3-2025-04-16", wantID: "o3", wantOK: true},
		{name: "namespaced ID is not an official model ID", provider: "openai-compatible", baseURL: "https://api.openai.com/v1", model: "openai/gpt-6-luna"},
		{name: "custom compatible endpoint", provider: "openai-compatible", baseURL: "https://llm.example.test/v1", model: "gpt-6-luna"},
		{name: "proxy endpoint", provider: "openai-compatible", baseURL: "https://api.openai.com/proxy/v1", model: "gpt-6-luna"},
		{name: "empty query marker", baseURL: "https://api.openai.com/v1?", model: "gpt-6-luna"},
		{name: "empty fragment marker", baseURL: "https://api.openai.com/v1#", model: "gpt-6-luna"},
		{name: "dot path is not normalized", baseURL: "https://api.openai.com/proxy/../v1", model: "gpt-6-luna"},
		{name: "escaped path is not normalized", baseURL: "https://api.openai.com/%76%31", model: "gpt-6-luna"},
		{name: "azure endpoint", provider: "openai-compatible", baseURL: "https://example.openai.azure.com/openai/v1", model: "gpt-6-luna"},
		{name: "wrong provider", provider: "azure-openai", baseURL: "https://api.openai.com/v1", model: "gpt-6-luna"},
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
