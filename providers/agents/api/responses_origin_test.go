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
	"strings"
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
)

func TestRuntimeResponsesOriginUsesSharedProviderSigningKey(t *testing.T) {
	creds := credsFor(llm.DefaultBaseURL, map[string]string{"primary": "gpt-6-luna"})
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.SetModelCredential(llm.PurposeChat, "primary")
	first := &Server{cfg: Config{WebhookKey: "shared-provider-key"}}
	model, err := first.buildModelForPurpose(t.Context(), creds, agent, llm.PurposeChat)
	if err != nil {
		t.Fatal(err)
	}
	origin := model.(interface{ ResponsesOrigin() string }).ResponsesOrigin()
	for _, test := range []struct {
		name string
		key  string
		want bool
	}{
		{name: "another replica", key: "shared-provider-key", want: true},
		{name: "different provider deployment", key: "another-provider-key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{cfg: Config{WebhookKey: test.key}}
			rebuilt, err := server.buildModelForPurpose(t.Context(), creds, agent, llm.PurposeChat)
			if err != nil {
				t.Fatal(err)
			}
			if got := llm.ModelHasResponsesOrigin(rebuilt, origin); got != test.want {
				t.Fatalf("runtime origin matches = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDurableResponsesModelRequiresStableSigningMaterial(t *testing.T) {
	creds := credsFor(llm.DefaultBaseURL, map[string]string{"primary": "gpt-6-luna", "backup": "gpt-4o"})
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.SetModelCredential(llm.PurposeChat, "primary")
	server := &Server{cfg: Config{DatabaseURL: "durable-store-configured"}}
	if _, err := server.buildModelForPurpose(t.Context(), creds, agent, llm.PurposeChat); err == nil || !strings.Contains(err.Error(), "continuation signing unavailable") {
		t.Fatalf("unsigned durable Responses model error = %v", err)
	}
	server.cfg.InMemoryStore = true
	if _, err := server.buildModelForPurpose(t.Context(), creds, agent, llm.PurposeChat); err != nil {
		t.Fatalf("ephemeral model did not use its process signing key: %v", err)
	}
	server.cfg.InMemoryStore = false
	server.cfg.WebhookKey = "durable-provider-key"
	if _, err := server.buildModelForPurpose(t.Context(), creds, agent, llm.PurposeChat); err != nil {
		t.Fatalf("durable model rejected configured signing key: %v", err)
	}
	server.cfg.WebhookKey = ""
	agent.Spec.SetModelFallbacks([]string{"backup"})
	model, err := server.buildModelForPurpose(t.Context(), creds, agent, llm.PurposeChat)
	if err != nil {
		t.Fatalf("unavailable Responses signing blocked the Chat Completions fallback: %v", err)
	}
	if _, responses := model.(interface{ ResponsesOrigin() string }); responses {
		t.Fatal("durable model used an ephemeral signing key instead of the configured fallback")
	}
}
