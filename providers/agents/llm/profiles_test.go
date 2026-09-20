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

// Discovery is curated at this one call, which is what makes the `discover`
// verb's answer and the reconciler's status.models the same list. An endpoint
// that serves speech, embeddings and a responses-only family answers with all
// of them; none of those reach a model picker.
func TestDiscoverModelsReturnsOnlyChatModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"whisper-1"},{"id":"zeta-chat"},{"id":"gpt-4o-mini"},{"id":"dall-e-3"},
			{"id":"gpt-4o"},{"id":"gpt-5.3-codex"},{"id":"text-embedding-3-small"},
			{"id":"gpt-4o-mini-tts"},{"id":"o3-pro"},{"id":"gpt-4o-realtime-preview"},
			{"id":"chatgpt-4o-latest"},{"id":"gpt-3.5-turbo-instruct"},{"id":"alpha-chat"}
		]}`))
	}))
	defer upstream.Close()

	got, _, err := DiscoverModels(t.Context(), upstream.URL, "private-key")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gpt-4o", "gpt-4o-mini", "alpha-chat", "zeta-chat"}
	if len(got) != len(want) {
		t.Fatalf("discovered %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("discovered %v, want %v (catalog-known first, then alphabetical)", got, want)
		}
	}
}
