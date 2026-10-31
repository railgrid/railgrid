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
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestBuildModelLunaReasoningWithToolsUsesResponses(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	var payload map[string]any
	http.DefaultTransport = reasoningTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Fatalf("Luna tool request used %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"resp_luna","object":"response","status":"completed","output":[{"id":"msg_luna","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ready","annotations":[]}]}],"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}`)),
		}, nil
	})
	for _, effort := range []string{"low", "high", "max"} {
		t.Run(effort, func(t *testing.T) {
			model, err := BuildModel(t.Context(), Profile{
				Provider: ProviderOpenAI, BaseURL: DefaultBaseURL,
				Model: "gpt-6-luna", APIKey: "test-key", ReasoningEffort: effort,
			})
			if err != nil {
				t.Fatal(err)
			}
			toolModel, ok := model.(einomodel.ToolCallingChatModel)
			if !ok {
				t.Fatal("selected Responses model cannot bind Eino tools")
			}
			bound, err := toolModel.WithTools([]*schema.ToolInfo{{Name: "status", Desc: "Read status"}})
			if err != nil {
				t.Fatal(err)
			}
			reply, err := bound.Generate(t.Context(), []*schema.Message{schema.UserMessage("Check status")})
			if err != nil || reply.Content != "ready" {
				t.Fatalf("Luna result = %+v, %v", reply, err)
			}
			reasoning, _ := payload["reasoning"].(map[string]any)
			if reasoning["effort"] != effort || payload["store"] != false {
				t.Fatalf("incorrect Responses configuration: %#v", payload)
			}
			if tools, _ := payload["tools"].([]any); len(tools) != 1 {
				t.Fatalf("bound tools missing: %#v", payload)
			}
			if _, legacy := payload["reasoning_effort"]; legacy {
				t.Fatal("Responses request contains a Chat Completions reasoning field")
			}
		})
	}
}

func TestModelTransportMatchesAppStudio(t *testing.T) {
	for _, tc := range []struct {
		name      string
		profile   Profile
		transport string
		effort    string
	}{
		{"Luna default", Profile{Model: "gpt-6-luna"}, TransportResponses, ""},
		{"Luna low", Profile{Provider: ProviderOpenAI, Model: "gpt-6-luna", ReasoningEffort: "low"}, TransportResponses, "low"},
		{"Luna none", Profile{Model: "gpt-6-luna", ReasoningEffort: "none"}, TransportResponses, "none"},
		{"namespaced GPT-6", Profile{Model: "openai/GPT6.1-sol"}, TransportResponses, ""},
		{"Astra tools", Profile{Model: "gpt-6-astra"}, TransportResponses, ""},
		{"Sol tools", Profile{Model: "gpt-6.1-sol"}, TransportResponses, ""},
		{"legacy Luna default", Profile{Model: "gpt-5.6-luna"}, TransportChatCompletions, "none"},
		{"legacy Luna none", Profile{Model: "gpt-5.6-luna", ReasoningEffort: "none"}, TransportChatCompletions, "none"},
		{"legacy Luna high", Profile{Model: "gpt-5.6-luna", ReasoningEffort: "high"}, TransportResponses, "high"},
		{"GPT-4o", Profile{Model: "gpt-4o"}, TransportChatCompletions, ""},
		{"o3 reasoning", Profile{Model: "o3", ReasoningEffort: "high"}, TransportChatCompletions, "high"},
		{"unknown gateway", Profile{BaseURL: "https://gateway.example/v1", Model: "custom-chat"}, TransportChatCompletions, ""},
		{"similar family name", Profile{Model: "gpt-60-custom"}, TransportChatCompletions, ""},
		{"frozen Responses", Profile{Model: "gpt-4o", Transport: TransportResponses}, TransportResponses, ""},
		{"frozen legacy", Profile{Model: "gpt-6-luna", Transport: TransportChatCompletions}, TransportChatCompletions, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelTransport(tc.profile); got != tc.transport {
				t.Fatalf("transport = %q, want %q", got, tc.transport)
			}
			if got := EffectiveReasoningEffort(tc.profile); got != tc.effort {
				t.Fatalf("effective effort = %q, want %q", got, tc.effort)
			}
		})
	}
}

func TestResponsesReasoningCapabilities(t *testing.T) {
	for _, name := range []string{"gpt-6-luna", "gpt-5.6-luna"} {
		for _, effort := range []string{"none", "low", "medium", "high", "xhigh", "max"} {
			if err := ValidateReasoningEffort(ProviderOpenAI, DefaultBaseURL, name, effort); err != nil {
				t.Fatalf("%s/%s should be available: %v", name, effort, err)
			}
		}
	}
	if got := DefaultReasoningEffort("gpt-6-luna"); got != "" {
		t.Fatalf("Responses default effort = %q, want model default", got)
	}
	if err := ValidateReasoningEffort(ProviderOpenAI, "https://custom.example/v1", "gpt-6-luna", "low"); err == nil {
		t.Fatal("custom gateway unexpectedly inherited official endpoint capabilities")
	}
}
