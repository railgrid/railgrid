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

package api

import (
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
)

func TestProjectReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		settings projectLLMSettings
		want     string
	}{
		{settings: projectLLMSettings{Model: "gpt-5.6-luna"}, want: "none"},
		{settings: projectLLMSettings{Model: "gpt-5.6-luna", ReasoningEffort: "high"}, want: "high"},
		{settings: projectLLMSettings{Model: "gpt-5.6-terra"}},
		{settings: projectLLMSettings{Model: "gpt-5.4"}},
	} {
		t.Run("model="+tc.settings.Model, func(t *testing.T) {
			if got := string(projectReasoningEffort(tc.settings)); got != tc.want {
				t.Fatalf("projectReasoningEffort(%#v) = %q, want %q", tc.settings, got, tc.want)
			}
		})
	}
}

func TestProjectLLMTransport(t *testing.T) {
	base := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL}
	for _, tc := range []struct {
		name     string
		settings projectLLMSettings
		want     string
	}{
		{name: "GPT-6 retains Responses routing", settings: projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-6-luna"}, want: projectLLMTransportResponses},
		{name: "GPT-5.6 Luna automatic remains Chat", settings: reasoningSettings(base, "gpt-5.6-luna", ""), want: projectLLMTransportChatCompletions},
		{name: "GPT-5.6 Luna none remains Chat", settings: reasoningSettings(base, "gpt-5.6-luna", "none"), want: projectLLMTransportChatCompletions},
		{name: "GPT-5.6 Luna explicit reasoning uses Responses", settings: reasoningSettings(base, "gpt-5.6-luna", "high"), want: projectLLMTransportResponses},
		{name: "custom endpoint keeps existing Chat routing", settings: projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: "https://llm.example.test/v1", Model: "gpt-5.6-luna", ReasoningEffort: "high"}, want: projectLLMTransportChatCompletions},
		{name: "frozen Chat transport wins", settings: projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-6-luna", ReasoningTransport: projectLLMTransportChatCompletions}, want: projectLLMTransportChatCompletions},
		{name: "frozen Responses transport wins", settings: projectLLMSettings{Provider: projectLLMProviderGoogle, Model: "gemini-2.5-pro", ReasoningTransport: projectLLMTransportResponses}, want: projectLLMTransportResponses},
		{name: "Google uses Gemini", settings: projectLLMSettings{Provider: projectLLMProviderGoogle, Model: "gemini-2.5-pro"}, want: projectLLMTransportGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectLLMTransport(tc.settings); got != tc.want {
				t.Fatalf("projectLLMTransport() = %q, want %q", got, tc.want)
			}
		})
	}
}

func reasoningSettings(base projectLLMSettings, model, effort string) projectLLMSettings {
	base.Model = model
	base.ReasoningEffort = effort
	return base
}

func TestProjectLLMEffectiveReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		settings projectLLMSettings
		want     string
	}{
		{settings: projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-5.6-luna"}, want: "none"},
		{settings: projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-5.6-luna", ReasoningEffort: "high"}, want: "high"},
		{settings: projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-5.6-luna", ReasoningEffort: "high", ReasoningEffectiveEffort: reasoningEffortPointer("")}, want: ""},
		{settings: projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-5.6-luna", ReasoningEffectiveEffort: reasoningEffortPointer("none")}, want: "none"},
		{settings: projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-6-luna"}},
		{settings: projectLLMSettings{Provider: defaultProjectLLMProvider, Model: "gpt-5.1"}},
	} {
		if got := projectLLMEffectiveReasoningEffort(tc.settings); got != tc.want {
			t.Fatalf("projectLLMEffectiveReasoningEffort(%#v) = %q, want %q", tc.settings, got, tc.want)
		}
	}
}

func TestProjectReasoningEffortFrozenEffectiveValueWins(t *testing.T) {
	settings := projectLLMSettings{
		Model:                    "gpt-5.6-luna",
		ReasoningEffort:          "high",
		ReasoningEffectiveEffort: reasoningEffortPointer(""),
	}
	if got := string(projectReasoningEffort(settings)); got != "" {
		t.Fatalf("projectReasoningEffort() = %q, want frozen empty effort", got)
	}
}

func TestProjectMaxTokensOptions(t *testing.T) {
	cases := []struct {
		model string
		// Reasoning-family models reject the legacy max_tokens field, so the
		// helper must not emit the common MaxTokens option for them.
		wantCommonMaxTokens bool
	}{
		{model: "gpt-4o", wantCommonMaxTokens: true},
		{model: "gpt-4.1-mini", wantCommonMaxTokens: true},
		{model: "deepseek-v4", wantCommonMaxTokens: true},
		{model: "gemini-2.5-pro", wantCommonMaxTokens: true},
		{model: "", wantCommonMaxTokens: true},
		{model: "gpt-5", wantCommonMaxTokens: false},
		{model: "gpt-5-mini", wantCommonMaxTokens: false},
		{model: "openai/gpt-5.2", wantCommonMaxTokens: false},
		{model: "o3-mini", wantCommonMaxTokens: false},
		{model: "o4-mini", wantCommonMaxTokens: false},
		{model: "gpt-6.1-sol", wantCommonMaxTokens: true},
		{model: "openai/gpt-6-luna", wantCommonMaxTokens: true},
	}
	for _, tc := range cases {
		t.Run("model="+tc.model, func(t *testing.T) {
			opts := projectMaxTokensOptions(tc.model, 4096)
			if len(opts) != 1 {
				t.Fatalf("expected exactly one option, got %d", len(opts))
			}
			common := einomodel.GetCommonOptions(&einomodel.Options{}, opts...)
			if tc.wantCommonMaxTokens {
				if common.MaxTokens == nil || *common.MaxTokens != 4096 {
					t.Fatalf("expected common MaxTokens=4096, got %v", common.MaxTokens)
				}
				return
			}
			if common.MaxTokens != nil {
				t.Fatalf("reasoning model %q must not send legacy max_tokens, got %d", tc.model, *common.MaxTokens)
			}
		})
	}
}

func TestProjectMaxTokensOptionsForSettingsFollowsTransport(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "gpt-5.6-luna", ReasoningEffort: "high"}
	options := projectMaxTokensOptionsForSettings(settings, 4096)
	common := einomodel.GetCommonOptions(&einomodel.Options{}, options...)
	if common.MaxTokens == nil || *common.MaxTokens != 4096 {
		t.Fatalf("Responses transport MaxTokens = %v, want 4096", common.MaxTokens)
	}
}
