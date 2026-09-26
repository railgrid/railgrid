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
	"context"
	"testing"

	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/llm"
)

func TestCompactionMaxTokensOptionPayload(t *testing.T) {
	tests := []struct {
		name            string
		model           string
		wantField       string
		unexpectedField string
	}{
		{
			name:            "GPT-5 uses max completion tokens",
			model:           "gpt-5.6-luna",
			wantField:       "max_completion_tokens",
			unexpectedField: "max_tokens",
		},
		{
			name:            "GPT-4o uses max tokens",
			model:           "gpt-4o",
			wantField:       "max_tokens",
			unexpectedField: "max_completion_tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newCompactFixture(t, 0, 0, tt.model)
			model, err := f.s.buildModelForPurpose(ctx, f.creds, f.agent, llm.PurposeCompaction)
			if err != nil {
				t.Fatalf("build compaction model: %v", err)
			}

			_, err = f.s.summarizeBatch(ctx, f.run(), model, tt.model, []engine.Message{{
				Role: engine.RoleUser, Content: "Summarize this conversation evidence.",
			}})
			if err != nil {
				t.Fatalf("summarize batch: %v", err)
			}

			requests := f.llm.requestSnapshot()
			if len(requests) != 1 {
				t.Fatalf("fake model received %d requests, want 1", len(requests))
			}
			request := requests[0]
			if got := request[tt.wantField]; got != float64(compactOutputTokens) {
				t.Errorf("request[%q] = %v, want %d", tt.wantField, got, compactOutputTokens)
			}
			if got, exists := request[tt.unexpectedField]; exists {
				t.Errorf("request unexpectedly includes %q = %v", tt.unexpectedField, got)
			}
		})
	}
}
