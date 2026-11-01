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
	"reflect"
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

func TestIsOfficialOpenAIEndpointSharedFixtures(t *testing.T) {
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
			if got := IsOfficialOpenAIEndpoint(tc.Provider, tc.BaseURL); got != tc.Official {
				t.Errorf("IsOfficialOpenAIEndpoint(%q, %q) = %v, want %v", tc.Provider, tc.BaseURL, got, tc.Official)
			}
		})
	}
}

func TestAnthropicReasoningProfilesUseExactOfficialModelsAndEndpoint(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		baseURL     string
		model       string
		wantID      string
		wantEfforts []string
		wantDefault string
		wantOK      bool
	}{
		{
			name:     "Haiku 5.5 has all five efforts and defaults to medium",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL,
			model: "claude-haiku-5-5", wantID: "claude-haiku-5-5",
			wantEfforts: []string{"low", "medium", "high", "xhigh", "max"}, wantDefault: "medium", wantOK: true,
		},
		{
			name:     "Opus 5.5 defaults to medium",
			provider: "openai", baseURL: officialAnthropicBaseURL,
			model: "claude-opus-5-5", wantID: "claude-opus-5-5",
			wantEfforts: []string{"low", "medium", "high", "xhigh", "max"}, wantDefault: "medium", wantOK: true,
		},
		{
			name:     "Opus 5 has all five efforts",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL,
			model: "claude-opus-5", wantID: "claude-opus-5",
			wantEfforts: []string{"low", "medium", "high", "xhigh", "max"}, wantDefault: "high", wantOK: true,
		},
		{
			name:     "Opus 4.8 has all five efforts",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL,
			model: "claude-opus-4-8", wantID: "claude-opus-4-8",
			wantEfforts: []string{"low", "medium", "high", "xhigh", "max"}, wantDefault: "high", wantOK: true,
		},
		{
			name:     "Opus 4.7 has all five efforts",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL,
			model: "claude-opus-4-7", wantID: "claude-opus-4-7",
			wantEfforts: []string{"low", "medium", "high", "xhigh", "max"}, wantDefault: "high", wantOK: true,
		},
		{
			name:     "Sonnet 5.5 has all five efforts",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL,
			model: "claude-sonnet-5-5", wantID: "claude-sonnet-5-5",
			wantEfforts: []string{"low", "medium", "high", "xhigh", "max"}, wantDefault: "high", wantOK: true,
		},
		{
			name:     "Sonnet 5 has all five efforts",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL,
			model: "claude-sonnet-5", wantID: "claude-sonnet-5",
			wantEfforts: []string{"low", "medium", "high", "xhigh", "max"}, wantDefault: "high", wantOK: true,
		},
		{
			name:     "Opus 4.6 does not advertise xhigh",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL,
			model: "claude-opus-4-6", wantID: "claude-opus-4-6",
			wantEfforts: []string{"low", "medium", "high", "max"}, wantDefault: "high", wantOK: true,
		},
		{
			name:     "Sonnet 4.6 does not advertise xhigh",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL,
			model: "claude-sonnet-4-6", wantID: "claude-sonnet-4-6",
			wantEfforts: []string{"low", "medium", "high", "max"}, wantDefault: "high", wantOK: true,
		},
		{
			name:     "Opus 4.5 dated ID",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL,
			model: "claude-opus-4-5-20251101", wantID: "claude-opus-4-5-20251101",
			wantEfforts: []string{"low", "medium", "high"}, wantDefault: "high", wantOK: true,
		},
		{
			name:     "Opus 4.5 documented API alias",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL,
			model: "claude-opus-4-5", wantID: "claude-opus-4-5-20251101",
			wantEfforts: []string{"low", "medium", "high"}, wantDefault: "high", wantOK: true,
		},
		{
			name:     "Haiku 4.5 has no effort profile",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL, model: "claude-haiku-4-5-20251001",
		},
		{
			name:     "Sonnet 4.5 has no effort profile",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL, model: "claude-sonnet-4-5-20250929",
		},
		{
			name:     "gateway namespace is not inferred",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL, model: "openrouter/anthropic/claude-opus-5-5",
		},
		{
			name:     "custom gateway does not inherit Anthropic capabilities",
			provider: "openai-compatible", baseURL: "https://gateway.example.test/v1", model: "claude-opus-5-5",
		},
		{
			name:     "Anthropic model on OpenAI endpoint is not inferred",
			provider: "openai-compatible", baseURL: officialOpenAIBaseURL, model: "claude-opus-5-5",
		},
		{
			name:     "OpenAI model on Anthropic endpoint is not inferred",
			provider: "openai-compatible", baseURL: officialAnthropicBaseURL, model: "gpt-6-luna",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, ok := ReasoningProfileForConnection(tt.provider, tt.baseURL, tt.model)
			if ok != tt.wantOK {
				t.Fatalf("ReasoningProfileForConnection() ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if profile.ID != tt.wantID {
				t.Errorf("profile ID = %q, want %q", profile.ID, tt.wantID)
			}
			if !reflect.DeepEqual(profile.Efforts, tt.wantEfforts) {
				t.Errorf("efforts = %v, want %v", profile.Efforts, tt.wantEfforts)
			}
			if profile.DefaultEffort != tt.wantDefault {
				t.Errorf("default effort = %q, want %q", profile.DefaultEffort, tt.wantDefault)
			}
			if !profile.AnthropicMessages {
				t.Error("Anthropic profile did not identify the Messages API transport")
			}
		})
	}
}

func TestUsesOfficialAnthropicEndpoint(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider string
		baseURL  string
		want     bool
	}{
		{name: "canonical endpoint", provider: "openai-compatible", baseURL: officialAnthropicBaseURL, want: true},
		{name: "trailing slash", provider: "openai-compatible", baseURL: officialAnthropicBaseURL + "/", want: true},
		{name: "host case is insensitive", provider: "openai-compatible", baseURL: "https://API.ANTHROPIC.COM/v1", want: true},
		{name: "explicit default TLS port", provider: "openai-compatible", baseURL: "https://api.anthropic.com:443/v1", want: true},
		{name: "empty port is rejected", provider: "openai-compatible", baseURL: "https://api.anthropic.com:/v1"},
		{name: "other provider", provider: "claude-code", baseURL: officialAnthropicBaseURL},
		{name: "HTTP is rejected", provider: "openai-compatible", baseURL: "http://api.anthropic.com/v1"},
		{name: "other host", provider: "openai-compatible", baseURL: "https://api.anthropic.com.example.test/v1"},
		{name: "userinfo is rejected", provider: "openai-compatible", baseURL: "https://user:secret@api.anthropic.com/v1"},
		{name: "nondefault port is rejected", provider: "openai-compatible", baseURL: "https://api.anthropic.com:8443/v1"},
		{name: "query is rejected", provider: "openai-compatible", baseURL: officialAnthropicBaseURL + "?deployment=one"},
		{name: "fragment is rejected", provider: "openai-compatible", baseURL: officialAnthropicBaseURL + "#one"},
		{name: "other path is rejected", provider: "openai-compatible", baseURL: "https://api.anthropic.com/tenant/v1"},
		{name: "escaped path is rejected", provider: "openai-compatible", baseURL: "https://api.anthropic.com/%76%31"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := usesOfficialAnthropicEndpoint(tt.provider, tt.baseURL); got != tt.want {
				t.Errorf("usesOfficialAnthropicEndpoint(%q, %q) = %v, want %v", tt.provider, tt.baseURL, got, tt.want)
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
		{name: "Anthropic Haiku 5.5 xhigh", model: "claude-haiku-5-5", effort: "xhigh", baseURL: officialAnthropicBaseURL, wantOK: true},
		{name: "Anthropic Haiku 5.5 defaults to medium and accepts explicit medium", model: "claude-haiku-5-5", effort: "medium", baseURL: officialAnthropicBaseURL, wantOK: true},
		{name: "Anthropic Opus 4.6 rejects xhigh", model: "claude-opus-4-6", effort: "xhigh", baseURL: officialAnthropicBaseURL},
		{name: "Anthropic Opus 4.5 rejects max", model: "claude-opus-4-5", effort: "max", baseURL: officialAnthropicBaseURL},
		{name: "Anthropic Haiku 4.5 has no effort", model: "claude-haiku-4-5-20251001", effort: "high", baseURL: officialAnthropicBaseURL},
		{name: "Anthropic custom endpoint cannot configure effort", model: "claude-opus-5-5", effort: "high", baseURL: "https://gateway.example.test/v1"},
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
