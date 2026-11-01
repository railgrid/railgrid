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
		transport       string
		wantEffort      string
		wantPresent     bool
		wantTemperature bool
		wantResponses   bool
	}{
		{model: "gpt-5.6-luna", wantEffort: "none", wantPresent: true},
		{model: "gpt-6-luna", wantResponses: true, transport: TransportResponses},
		{model: "gpt-6-sol", wantResponses: true, transport: TransportResponses},
		{model: "gpt6-sol", wantResponses: true, transport: TransportResponses},
		{model: "openai/gpt6.1-sol", wantResponses: true, transport: TransportResponses},
		{model: "openai/gpt-6-sol", wantResponses: true, transport: TransportResponses},
		{model: "gpt-6.1-sol", wantResponses: true, transport: TransportResponses},
		{model: "gpt-6-luna", wantEffort: "none", wantPresent: true},
		{model: "gpt-6-astra"},
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
				Provider:  ProviderOpenAICompatible,
				BaseURL:   server.URL,
				Model:     tc.model,
				APIKey:    "test-key",
				Transport: tc.transport,
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

	got, _, err := DiscoverModels(t.Context(), Profile{BaseURL: upstream.URL, APIKey: "private-key"})
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

func TestDiscoverModelsAnthropicHeaders(t *testing.T) {
	for _, host := range []string{"api.anthropic.com", "api.openai.com", "api.anthropic.com.example.org"} {
		t.Run(host, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("unexpected discovery request: %s, authorization %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				version := r.Header.Get("anthropic-version")
				wantWorkspace := ""
				if host == "api.anthropic.com" {
					wantWorkspace = "wrkspc_test"
				}
				if got := r.Header.Get("anthropic-workspace-id"); got != wantWorkspace {
					t.Errorf("workspace header = %q, want %q", got, wantWorkspace)
				}
				if host == "api.anthropic.com" && (version != "2023-06-01" || r.Header.Get("Content-Type") != "application/json") {
					http.Error(w, "anthropic-version header is required", http.StatusBadRequest)
					return
				}
				if host != "api.anthropic.com" && version != "" {
					t.Errorf("Anthropic header sent to %s", host)
				}
				_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-4"}]}`)
			}))
			defer upstream.Close()
			// Exercise the public endpoint selection through a local HTTP server,
			// without a live credential or external network request.
			client := &http.Client{Transport: reasoningTransport(func(r *http.Request) (*http.Response, error) {
				local, err := http.NewRequestWithContext(r.Context(), r.Method, upstream.URL+r.URL.Path, nil)
				if err != nil {
					return nil, err
				}
				local.Header = r.Header.Clone()
				return http.DefaultTransport.RoundTrip(local)
			})}
			previous := http.DefaultClient
			http.DefaultClient = client
			t.Cleanup(func() { http.DefaultClient = previous })
			models, _, err := DiscoverModels(t.Context(), Profile{BaseURL: "https://" + host + "/v1/", APIKey: "test-key", AnthropicWorkspaceID: "wrkspc_test"})
			if err != nil || len(models) != 1 || models[0] != "claude-sonnet-4" {
				t.Fatalf("discovery = %v, %v", models, err)
			}
		})
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
		{"openai-compatible", "https://api.anthropic.com/v1", "claude-haiku-5-5", "xhigh", true},
		{"openai", "https://api.anthropic.com/v1", "claude-opus-4-6", "max", true},
		{"openai-compatible", "https://api.anthropic.com/v1", "claude-opus-4-6", "xhigh", false},
		{"openai-compatible", "https://api.anthropic.com/v1", "claude-sonnet-4-5-20250929", "high", false},
		{"openai-compatible", "https://gateway.example/v1", "claude-haiku-5-5", "high", false},
	} {
		if err := ValidateReasoningEffort(tc.provider, tc.endpoint, tc.model, tc.effort); (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestDefaultReasoningEffortForConnection(t *testing.T) {
	for _, tc := range []struct {
		name, provider, endpoint, model, want string
	}{
		{name: "Haiku 5.5 Anthropic default", provider: ProviderOpenAICompatible, endpoint: "https://api.anthropic.com/v1", model: "claude-haiku-5-5", want: "medium"},
		{name: "Opus 5.5 Anthropic default", provider: ProviderOpenAI, endpoint: "https://api.anthropic.com/v1", model: "claude-opus-5-5", want: "medium"},
		{name: "Opus 4.5 Anthropic default", provider: ProviderOpenAICompatible, endpoint: "https://api.anthropic.com/v1", model: "claude-opus-4-5", want: "high"},
		{name: "unsupported Haiku 4.5 has no default", provider: ProviderOpenAICompatible, endpoint: "https://api.anthropic.com/v1", model: "claude-haiku-4-5", want: ""},
		{name: "custom Anthropic-compatible endpoint has no inferred default", provider: ProviderOpenAICompatible, endpoint: "https://gateway.example/v1", model: "claude-haiku-5-5", want: ""},
		{name: "existing OpenAI default remains", provider: ProviderOpenAICompatible, endpoint: "https://api.openai.com/v1", model: "gpt-6-luna", want: DefaultReasoningEffort("gpt-6-luna")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultReasoningEffortForConnection(tc.provider, tc.endpoint, tc.model); got != tc.want {
				t.Fatalf("DefaultReasoningEffortForConnection() = %q, want %q", got, tc.want)
			}
		})
	}
}

type reasoningTransport func(*http.Request) (*http.Response, error)

func (f reasoningTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAnthropicWorkspaceOnChatRequests(t *testing.T) {
	for _, tc := range []struct{ host, workspace, want, effort string }{
		{"api.anthropic.com", " wrkspc_test ", "wrkspc_test", "xhigh"},
		{"api.anthropic.com", "", "", ""},
		{"api.openai.com", "wrkspc_test", "", ""},
		{"api.anthropic.com.example.org", "wrkspc_test", "", ""},
	} {
		t.Run(tc.host+tc.workspace, func(t *testing.T) {
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			calls := 0
			http.DefaultTransport = reasoningTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if got := r.Header.Get("anthropic-workspace-id"); got != tc.want {
					t.Errorf("workspace header = %q, want %q", got, tc.want)
				}
				if tc.host == "api.anthropic.com" && r.URL.Path != "/v1/messages" {
					t.Errorf("Anthropic reasoning must use Messages, got %s", r.URL.Path)
				}
				var payload struct {
					Stream       bool     `json:"stream"`
					Temperature  *float64 `json:"temperature"`
					OutputConfig struct {
						Effort string `json:"effort"`
					} `json:"output_config"`
					ReasoningEffort string `json:"reasoning_effort"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					return nil, err
				}
				if tc.host == "api.anthropic.com" && payload.Temperature != nil {
					t.Error("Anthropic chat and stream requests must omit the default temperature")
				}
				if tc.host == "api.anthropic.com" && (payload.OutputConfig.Effort != tc.effort || payload.ReasoningEffort != "") {
					t.Errorf("native effort = %q, compatibility effort = %q", payload.OutputConfig.Effort, payload.ReasoningEffort)
				}
				if tc.host != "api.anthropic.com" && payload.Temperature == nil {
					t.Error("other chat endpoints must retain their existing default temperature")
				}
				body := `{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
				contentType := "application/json"
				if payload.Stream {
					contentType = "text/event-stream"
					body = "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"
				}
				if tc.host == "api.anthropic.com" {
					body = `{"id":"msg-test","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
					if payload.Stream {
						body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-haiku-5-5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
							"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
							"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
							"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
							"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
							"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
					}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			m, err := BuildModel(t.Context(), Profile{BaseURL: "https://" + tc.host + "/v1", Model: "claude-haiku-5-5", APIKey: "test-key", AnthropicWorkspaceID: tc.workspace, ReasoningEffort: tc.effort})
			if err != nil {
				t.Fatal(err)
			}
			messages := []*schema.Message{schema.UserMessage("test")}
			if _, err := m.Generate(t.Context(), messages); err != nil {
				t.Fatal(err)
			}
			stream, err := m.Stream(t.Context(), messages)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			var content strings.Builder
			for {
				message, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				content.WriteString(message.Content)
			}
			if content.String() != "ok" {
				t.Fatalf("stream content = %q", content.String())
			}
			if calls != 2 {
				t.Fatalf("requests = %d, want 2", calls)
			}
		})
	}
}

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
