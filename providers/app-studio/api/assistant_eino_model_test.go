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

package api

import (
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
)

func TestProjectReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  string
	}{
		{model: "gpt-5.6-luna", want: "none"},
		{model: "openai/gpt-5.6-luna", want: "none"},
		{model: "gpt-5.6-terra"},
		{model: "gpt-5.4"},
	} {
		t.Run("model="+tc.model, func(t *testing.T) {
			if got := string(projectReasoningEffort(tc.model)); got != tc.want {
				t.Fatalf("projectReasoningEffort(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
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
