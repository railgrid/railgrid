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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

func TestChatResponsesTransportIsPinnedBeforePendingAndScopedToChat(t *testing.T) {
	ctx := t.Context()
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"}
	creds := credsFor(llm.DefaultBaseURL, map[string]string{"luna": "gpt-6-luna", "default": "gpt-4o"})
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.SetModelCredential(llm.PurposeChat, "default")
	if err := s.store.PutSessionModelSettings(ctx, scope, "first", store.SessionModelSettings{
		ModelCredential: "luna", ReasoningEffortByModel: map[string]string{"luna": "low"},
	}); err != nil {
		t.Fatal(err)
	}
	run := taskRun{Creds: creds, HubToken: "test-caller", Scope: scope, Agent: agent, SessionID: "first", Trigger: agentsv1alpha1.RunTriggerChat}
	if err := s.resolveChatModelSettings(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if run.ModelSettings.Transport != llm.TransportResponses || run.ModelSettings.ReasoningEffort != "low" {
		t.Fatalf("admission did not freeze Responses/low: %+v", run.ModelSettings)
	}
	now := time.Now()
	if err := s.store.SaveRun(ctx, scope, store.Run{
		ID: "pending", AgentName: scope.AgentName, SessionID: "first", Phase: store.RunPhasePending,
		ModelSettings: run.ModelSettings, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.PutSessionModelSettings(ctx, scope, "first", store.SessionModelSettings{ModelCredential: "default"}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.store.GetRun(ctx, scope, "pending")
	if err != nil {
		t.Fatal(err)
	}
	run.ModelSettings = saved.ModelSettings
	if err := s.resolveChatModelSettings(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if run.ModelSettings.ModelCredential != "luna" || run.ModelSettings.Transport != llm.TransportResponses || run.ModelSettings.ReasoningEffort != "low" {
		t.Fatalf("resume inherited changed preferences: %+v", run.ModelSettings)
	}
	if agent.Spec.ModelCredentialFor(llm.PurposeChat) != "default" {
		t.Fatal("transport selection modified the Agent default")
	}
	run.ModelSettings = nil
	run.SessionID = "second"
	if err := s.resolveChatModelSettings(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if run.ModelSettings.ModelCredential != "default" || run.ModelSettings.Transport != llm.TransportChatCompletions || run.ModelSettings.ReasoningEffort != "" {
		t.Fatalf("another chat inherited Luna settings: %+v", run.ModelSettings)
	}
	run.ModelSettings = nil
	run.Trigger = "schedule"
	if err := s.resolveChatModelSettings(ctx, &run); err != nil || run.ModelSettings != nil {
		t.Fatalf("background work inherited chat settings: %+v, %v", run.ModelSettings, err)
	}
}

func TestFreshRunBuilderUsesChatCompletionsForCustomGPT6(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Errorf("request = %s %s, want POST /chat/completions", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "gpt-6-luna" || body["reasoning_effort"] != "none" {
			t.Errorf("fresh-run request = %#v, want GPT-6 Luna Chat Completions with effort none", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-fresh","object":"chat.completion","created":1,"model":"gpt-6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"ready"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	creds := credsFor(upstream.URL, map[string]string{"primary": "gpt-6-luna"})
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.SetModelCredential(llm.PurposeChat, "primary")
	s := &Server{}
	model, err := s.buildModelForPurpose(t.Context(), creds, agent, llm.PurposeChat)
	if err != nil {
		t.Fatalf("build fresh-run model: %v", err)
	}
	response, err := model.Generate(t.Context(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil || response == nil || response.Content != "ready" || calls != 1 {
		t.Fatalf("fresh-run response = %#v after %d calls, err=%v", response, calls, err)
	}
}

func TestInheritedMissingPrimaryAdmitsAndBuildsHealthyFallback(t *testing.T) {
	ctx := t.Context()
	s := &Server{store: store.NewMemoryStore()}
	llmServer := newFakeLLM(t, "answered by fallback")
	creds := credsFor(llmServer.srv.URL, map[string]string{"healthy": "gpt-4o"})
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.SetModelCredential(llm.PurposeChat, "missing")
	agent.Spec.SetModelFallbacks([]string{"healthy"})
	run := taskRun{
		Creds: creds, Scope: store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"},
		Agent: agent, SessionID: "chat", Trigger: agentsv1alpha1.RunTriggerChat,
	}
	if err := s.resolveChatModelSettings(ctx, &run); err != nil {
		t.Fatalf("admission rejected configured fallback: %v", err)
	}
	if run.ModelSettings == nil || run.ModelSettings.ModelCredential != "missing" || run.ModelSettings.Transport != "" || run.ModelSettings.ReasoningEffort != "" {
		t.Fatalf("missing primary snapshot = %+v, want only the inherited primary name", run.ModelSettings)
	}

	model, err := s.buildModelForPurpose(ctx, run.Creds, run.Agent, llm.PurposeChat, run.ModelSettings)
	if err != nil {
		t.Fatalf("build configured fallback: %v", err)
	}
	stream, err := model.Stream(ctx, []*schema.Message{schema.UserMessage("Say hello.")})
	if err != nil {
		t.Fatalf("start configured fallback stream: %v", err)
	}
	defer stream.Close()
	response, err := stream.Recv()
	if err != nil {
		t.Fatalf("read configured fallback stream: %v", err)
	}
	if response.Content != "answered by fallback" {
		t.Fatalf("fallback response = %q", response.Content)
	}
	requests := llmServer.requestSnapshot()
	if len(requests) != 1 || requests[0]["model"] != "gpt-4o" {
		t.Fatalf("fallback requests = %#v, want one request for gpt-4o", requests)
	}
}

func TestInheritedDefaultWithoutResolverFailsAdmission(t *testing.T) {
	s := &Server{store: store.NewMemoryStore()}
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.SetModelCredential(llm.PurposeChat, "primary")
	run := taskRun{
		Scope: store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"},
		Agent: agent, SessionID: "chat", Trigger: agentsv1alpha1.RunTriggerChat,
	}
	if err := s.resolveChatModelSettings(t.Context(), &run); !errors.Is(err, llm.ErrNotConfigured) {
		t.Fatalf("admission without a credential resolver = %v, want ErrNotConfigured", err)
	}
	if run.ModelSettings != nil {
		t.Fatal("admission without a resolver pinned a model snapshot")
	}
}

func TestInheritedPrimarySecretFailureKeepsFallbackTransportAndEffortSeparate(t *testing.T) {
	ctx := t.Context()
	s := &Server{store: store.NewMemoryStore()}
	llmServer := newFakeLLM(t, "answered by chat-completions fallback")
	creds := credsFor(llm.DefaultBaseURL, map[string]string{
		"primary": "gpt-6-luna", "healthy": "gpt-4o",
	})
	creds.creds["healthy"].Spec.BaseURL = llmServer.srv.URL
	delete(creds.secrets, CredentialSecretName("primary"))
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.SetModelCredential(llm.PurposeChat, "primary")
	agent.Spec.SetModelFallbacks([]string{"healthy"})
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"}
	if err := s.store.PutSessionModelSettings(ctx, scope, "chat", store.SessionModelSettings{
		ReasoningEffortByModel: map[string]string{"primary": "low"},
	}); err != nil {
		t.Fatal(err)
	}
	run := taskRun{
		Creds: creds, Scope: scope, Agent: agent, SessionID: "chat", Trigger: agentsv1alpha1.RunTriggerChat,
	}
	if err := s.resolveChatModelSettings(ctx, &run); err != nil {
		t.Fatalf("admission rejected unreadable primary Secret: %v", err)
	}
	if run.ModelSettings.ModelCredential != "primary" || run.ModelSettings.Transport != llm.TransportResponses || run.ModelSettings.ReasoningEffort != "low" {
		t.Fatalf("primary snapshot = %+v, want its own Responses/low settings", run.ModelSettings)
	}

	model, err := s.buildModelForPurpose(ctx, run.Creds, run.Agent, llm.PurposeChat, run.ModelSettings)
	if err != nil {
		t.Fatalf("build healthy fallback: %v", err)
	}
	stream, err := model.Stream(ctx, []*schema.Message{schema.UserMessage("Say hello.")})
	if err != nil {
		t.Fatalf("start healthy fallback stream: %v", err)
	}
	defer stream.Close()
	response, err := stream.Recv()
	if err != nil {
		t.Fatalf("read healthy fallback stream: %v", err)
	}
	if response.Content != "answered by chat-completions fallback" {
		t.Fatalf("fallback response = %q", response.Content)
	}
	requests := llmServer.requestSnapshot()
	if len(requests) != 1 || requests[0]["model"] != "gpt-4o" {
		t.Fatalf("fallback requests = %#v, want one chat-completions request for gpt-4o", requests)
	}
	if _, exists := requests[0]["reasoning_effort"]; exists {
		t.Fatalf("primary reasoning effort leaked into fallback request: %#v", requests[0])
	}
}

type transientCredentialReadResolver struct {
	fakeCreds
	failed bool
}

func (r *transientCredentialReadResolver) GetModelCredential(ctx context.Context, name string) (*agentsv1alpha1.ModelCredential, error) {
	if !r.failed {
		r.failed = true
		return nil, errors.New("temporary credential read failure")
	}
	return r.fakeCreds.GetModelCredential(ctx, name)
}

func TestInheritedTransientMetadataFailureRetainsCredentialEffortForBuilderRetry(t *testing.T) {
	ctx := t.Context()
	s := &Server{store: store.NewMemoryStore()}
	creds := &transientCredentialReadResolver{fakeCreds: credsFor(llm.DefaultBaseURL, map[string]string{"primary": "gpt-6-luna"})}
	agent := &agentsv1alpha1.Agent{}
	agent.Spec.SetModelCredential(llm.PurposeChat, "primary")
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"}
	if err := s.store.PutSessionModelSettings(ctx, scope, "chat", store.SessionModelSettings{
		ReasoningEffortByModel: map[string]string{"primary": "high"},
	}); err != nil {
		t.Fatal(err)
	}
	run := taskRun{Creds: creds, Scope: scope, Agent: agent, SessionID: "chat", Trigger: agentsv1alpha1.RunTriggerChat}
	if err := s.resolveChatModelSettings(ctx, &run); err != nil {
		t.Fatalf("admission rejected transient default metadata read: %v", err)
	}
	if run.ModelSettings.ModelCredential != "primary" || run.ModelSettings.ReasoningEffort != "high" || run.ModelSettings.Transport != "" {
		t.Fatalf("transient-read snapshot = %+v, want primary/high with transport unresolved", run.ModelSettings)
	}
	model, err := s.buildModelForPurpose(ctx, run.Creds, run.Agent, llm.PurposeChat, run.ModelSettings)
	if err != nil {
		t.Fatalf("builder retry did not resolve primary credential: %v", err)
	}
	if _, ok := model.(interface{ ResponsesOrigin() string }); !ok {
		t.Fatalf("model after metadata retry is %T, want a Responses transport model", model)
	}
}
