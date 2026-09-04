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

package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestModelReasoningEffort(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  string
	}{
		{model: "gpt-5.6-luna", want: "none"},
		{model: "openai/gpt-5.6-luna", want: "none"},
		{model: "gpt-5.6-terra"},
		{model: "gpt-5.4"},
		{model: "gpt-4o"},
	} {
		t.Run("model="+tc.model, func(t *testing.T) {
			if got := string(modelReasoningEffort(tc.model)); got != tc.want {
				t.Fatalf("modelReasoningEffort(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}

func TestBuildModelReasoningEffortPayload(t *testing.T) {
	for _, tc := range []struct {
		model       string
		wantEffort  string
		wantPresent bool
	}{
		{model: "gpt-5.6-luna", wantEffort: "none", wantPresent: true},
		{model: "gpt-4o", wantPresent: false},
	} {
		t.Run("model="+tc.model, func(t *testing.T) {
			var payload map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" {
					t.Errorf("request path = %q, want /chat/completions", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()

			model, err := BuildModel(context.Background(), Profile{
				Provider: ProviderOpenAICompatible,
				BaseURL:  server.URL,
				Model:    tc.model,
				APIKey:   "test-key",
			})
			if err != nil {
				t.Fatalf("BuildModel: %v", err)
			}
			_, err = model.Generate(context.Background(), []*schema.Message{
				{Role: schema.User, Content: "hello"},
			}, einomodel.WithTools([]*schema.ToolInfo{{Name: "noop", Desc: "No-op test tool"}}))
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}

			got, present := payload["reasoning_effort"]
			if present != tc.wantPresent {
				t.Fatalf("reasoning_effort presence = %v, want %v; payload: %#v", present, tc.wantPresent, payload)
			}
			if present && got != tc.wantEffort {
				t.Fatalf("reasoning_effort = %#v, want %q", got, tc.wantEffort)
			}
			if tools, ok := payload["tools"].([]any); !ok || len(tools) != 1 {
				t.Fatalf("tools = %#v, want one function tool", payload["tools"])
			}
		})
	}
}
