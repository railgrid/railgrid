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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
		{model: "gpt-6-luna", want: "none"},
		{model: "openai/gpt-6-luna", want: "none"},
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
		model           string
		wantEffort      string
		wantPresent     bool
		wantTemperature bool
		wantResponses   bool
	}{
		{model: "gpt-5.6-luna", wantEffort: "none", wantPresent: true},
		{model: "gpt-6-luna", wantResponses: true},
		{model: "gpt-6-sol", wantResponses: true},
		{model: "gpt6-sol", wantResponses: true},
		{model: "openai/gpt6.1-sol", wantResponses: true},
		{model: "openai/gpt-6-sol", wantResponses: true},
		{model: "gpt-6.1-sol", wantResponses: true},
		{model: "gpt-5.4"},
		{model: "o3"},
		{model: "gpt-4o", wantPresent: false, wantTemperature: true},
	} {
		t.Run("model="+tc.model, func(t *testing.T) {
			var payload map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/chat/completions"
				if tc.wantResponses {
					wantPath = "/responses"
				}
				if r.URL.Path != wantPath {
					t.Errorf("request path = %q, want %s", r.URL.Path, wantPath)
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.wantResponses {
					_, _ = w.Write([]byte(`{"id":"resp_test","object":"response","status":"completed","output":[{"type":"message","id":"msg_test","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
					return
				}
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

			temperature, hasTemperature := payload["temperature"]
			if hasTemperature != tc.wantTemperature {
				t.Fatalf("temperature presence = %v, want %v; payload: %#v", hasTemperature, tc.wantTemperature, payload)
			}
			if hasTemperature && temperature != 0.2 {
				t.Fatalf("temperature = %v, want 0.2", temperature)
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

func TestSelectedReasoningCapabilities(t *testing.T) {
	for _, tc := range []struct {
		provider, endpoint, model, effort string
		valid                             bool
	}{
		{"openai", "https://api.openai.com/v1", "gpt-6-luna", "max", true},
		{"openai-compatible", "https://api.openai.com/v1", "o3", "high", true},
		{"openai", "https://custom.example/v1", "gpt-6-luna", "high", false},
		{"openai", "", "gpt-4o", "high", false},
		{"openai", "", "gpt-5.6-luna", "high", true},
		{"openai", "", "gpt-6-luna", "unknown", false},
		{"openai", "", "unknown", "", true},
	} {
		if err := ValidateReasoningEffort(tc.provider, tc.endpoint, tc.model, tc.effort); (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

type reasoningTransport func(*http.Request) (*http.Response, error)

func (f reasoningTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSelectedReasoningReachesModelRequest(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	var payload map[string]any
	http.DefaultTransport = reasoningTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("reasoning request used %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_test","object":"response","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))}, nil
	})
	m, err := BuildModel(context.Background(), Profile{Provider: "openai", BaseURL: "https://api.openai.com/v1", Model: "gpt-6-luna", APIKey: "test", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Generate(context.Background(), []*schema.Message{schema.UserMessage("test")}); err != nil {
		t.Fatal(err)
	}
	reasoning, _ := payload["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("reasoning not forwarded: %v", payload["reasoning"])
	}
}
