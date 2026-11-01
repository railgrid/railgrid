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
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type sessionTitleModel struct {
	einomodel.BaseChatModel
	input   chan []*schema.Message
	release <-chan struct{}
	done    chan struct{}
	reply   *schema.Message
	err     error
}

func (m *sessionTitleModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	copyInput := append([]*schema.Message(nil), input...)
	if m.input != nil {
		m.input <- copyInput
	}
	if m.release != nil {
		select {
		case <-m.release:
		case <-ctx.Done():
			if m.done != nil {
				close(m.done)
			}
			return nil, ctx.Err()
		}
	}
	if m.done != nil {
		close(m.done)
	}
	return m.reply, m.err
}

type sessionTitleStore struct {
	store.Store
	claims chan bool
	sets   chan bool
}

func (s *sessionTitleStore) ClaimSessionTitle(ctx context.Context, scope store.Scope, sessionID, firstUserMessageID string) (bool, error) {
	claimed, err := s.Store.ClaimSessionTitle(ctx, scope, sessionID, firstUserMessageID)
	if s.claims != nil {
		s.claims <- claimed
	}
	return claimed, err
}

func (s *sessionTitleStore) SetSessionTitleIfEmpty(ctx context.Context, scope store.Scope, sessionID, firstUserMessageID, title string) (bool, error) {
	changed, err := s.Store.SetSessionTitleIfEmpty(ctx, scope, sessionID, firstUserMessageID, title)
	if s.sets != nil {
		s.sets <- changed
	}
	return changed, err
}

type contextCheckingSessionTitleStore struct{ store.Store }

func (s contextCheckingSessionTitleStore) SetSessionTitleIfEmpty(ctx context.Context, scope store.Scope, sessionID, firstUserMessageID, title string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.Store.SetSessionTitleIfEmpty(ctx, scope, sessionID, firstUserMessageID, title)
}

func TestAgentSessionTitleRunsDetachedWithPinnedChatCredential(t *testing.T) {
	ctx := context.Background()
	base := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "scout"}
	prompt := "Summarize the rollout risk."
	pageContextPrompt := prompt + agentPageContextMarker + `{"workspace":"w","provider":null,"path":"/clusters/c","view":null}`
	if err := base.AppendMessage(ctx, scope, store.Message{
		ID: "first-user", AgentName: "scout", SessionID: "chat", Role: "user",
		Content: pageContextPrompt, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := base.PutSessionModelSettings(ctx, scope, "chat", store.SessionModelSettings{ModelCredential: "agent-default"}); err != nil {
		t.Fatal(err)
	}
	observed := &sessionTitleStore{Store: base, claims: make(chan bool, 1), sets: make(chan bool, 1)}
	release := make(chan struct{})
	model := &sessionTitleModel{
		input: make(chan []*schema.Message, 1), release: release,
		reply: schema.AssistantMessage("Rollout Risk Assessment", nil),
	}
	factoryCalls := make(chan string, 1)
	s := &Server{
		store: observed, events: newEventBus(),
		sessionTitleModelFactory: func(_ context.Context, _ llm.CredentialResolver, credential string) (einomodel.BaseChatModel, string, error) {
			factoryCalls <- credential
			return model, "test-title-model", nil
		},
	}

	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "scout"}}
	agent.Spec.Backend.Model = &agentsv1alpha1.AgentModelBackend{Credentials: map[string]string{"chat": "agent-default"}}
	run := taskRun{
		Scope: scope, Agent: agent, SessionID: "chat", Task: pageContextPrompt,
		Trigger: agentsv1alpha1.RunTriggerChat, generateSessionTitle: true,
		ModelSettings: &store.ModelSettings{ModelCredential: "conversation-choice", ReasoningEffort: "high"},
	}
	if !s.shouldGenerateSessionTitle(run, agent, nil, nil) {
		t.Fatal("fresh model-backed chat was not eligible for a title")
	}

	started := make(chan struct{})
	go func() {
		s.startSessionTitleGeneration(run, "first-user")
		close(started)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("title generation blocked the chat caller")
	}
	select {
	case claimed := <-observed.claims:
		if !claimed {
			t.Fatal("first user message was not claimed")
		}
	case <-time.After(time.Second):
		t.Fatal("detached title task did not claim the first user message")
	}
	select {
	case credential := <-factoryCalls:
		if credential != "conversation-choice" {
			t.Fatalf("title credential = %q, want the chat's pinned credential", credential)
		}
	case <-time.After(time.Second):
		t.Fatal("title model was not built")
	}
	var messages []*schema.Message
	select {
	case messages = <-model.input:
	case <-time.After(time.Second):
		t.Fatal("title model was not called")
	}
	if len(messages) != 2 || messages[0].Role != schema.System || messages[1].Role != schema.User {
		t.Fatalf("title model input = %#v, want one system and one user message", messages)
	}
	if messages[1].Content != prompt {
		t.Fatalf("title prompt = %q, want the page context envelope removed", messages[1].Content)
	}
	if agent.Spec.ModelCredentialFor("chat") != "agent-default" {
		t.Fatal("title generation mutated the Agent's default chat credential")
	}

	ch, unsubscribe := s.events.subscribe(scope)
	defer unsubscribe()
	close(release)
	select {
	case changed := <-observed.sets:
		if !changed {
			t.Fatal("successful title was not persisted")
		}
	case <-time.After(time.Second):
		t.Fatal("title was not persisted")
	}
	select {
	case ev := <-ch:
		if ev.Type != "session_title" {
			t.Fatalf("event type = %q, want session_title", ev.Type)
		}
		payload, ok := ev.Data.(map[string]any)
		if !ok || payload["agent"] != "scout" || payload["sessionID"] != "chat" || payload["title"] != "Rollout Risk Assessment" {
			t.Fatalf("title event payload = %#v", ev.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("persisted title did not publish an update event")
	}
	prefs, err := base.GetSessionModelSettings(ctx, scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if prefs.ModelCredential != "agent-default" || prefs.ReasoningEffortByModel != nil {
		t.Fatalf("title generation changed chat preferences: %+v", prefs)
	}
}

func TestSessionTitleBuilderUsesChatCompletionsForCustomGPT6(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Errorf("title request = %s %s, want POST /chat/completions", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "gpt-6-luna" || body["reasoning_effort"] != "none" {
			t.Errorf("title request = %#v, want GPT-6 Luna Chat Completions with effort none", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-title","object":"chat.completion","created":1,"model":"gpt-6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"Gateway Title"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	creds := credsFor(upstream.URL, map[string]string{"title": "gpt-6-luna"})
	s := &Server{}
	model, name, err := s.buildSessionTitleModel(t.Context(), creds, "title")
	if err != nil {
		t.Fatalf("build title model: %v", err)
	}
	if name != "gpt-6-luna" {
		t.Fatalf("title model name = %q", name)
	}
	reply, err := model.Generate(t.Context(), []*schema.Message{
		schema.SystemMessage(agentSessionTitleSystemPrompt), schema.UserMessage("Summarize the rollout risk."),
	})
	if err != nil || reply == nil || reply.Content != "Gateway Title" || calls != 1 {
		t.Fatalf("title response = %#v after %d calls, err=%v", reply, calls, err)
	}
}

func TestAgentSessionTitleOnlyClaimsTheFirstUserMessage(t *testing.T) {
	ctx := context.Background()
	base := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "scout"}
	for i, id := range []string{"first", "second"} {
		if err := base.AppendMessage(ctx, scope, store.Message{
			ID: id, AgentName: "scout", SessionID: "chat", Role: "user", Content: id,
			CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	observed := &sessionTitleStore{Store: base, claims: make(chan bool, 1)}
	factoryCalls := make(chan struct{}, 1)
	s := &Server{
		store: observed,
		sessionTitleModelFactory: func(context.Context, llm.CredentialResolver, string) (einomodel.BaseChatModel, string, error) {
			factoryCalls <- struct{}{}
			return &sessionTitleModel{reply: schema.AssistantMessage("Second User Message", nil)}, "test", nil
		},
	}
	run := taskRun{
		Scope: scope, Agent: &agentsv1alpha1.Agent{}, SessionID: "chat", Task: "second",
		Trigger: agentsv1alpha1.RunTriggerChat, generateSessionTitle: true,
		ModelSettings: &store.ModelSettings{ModelCredential: "selected"},
	}
	priorHistory := []backend.Message{{Role: backend.RoleUser, Content: "first"}}
	if s.shouldGenerateSessionTitle(run, run.Agent, nil, priorHistory) {
		s.startSessionTitleGeneration(run, "second")
	}
	select {
	case claimed := <-observed.claims:
		t.Fatalf("later turn started a title claim (claimed=%v)", claimed)
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-factoryCalls:
		t.Fatal("a title model was called for a later user message")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestConcurrentFirstTurnSessionTitlesUseTheStoreClaim(t *testing.T) {
	ctx := context.Background()
	base := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "scout"}
	if err := base.AppendMessage(ctx, scope, store.Message{
		ID: "first-user", AgentName: "scout", SessionID: "chat", Role: "user", Content: "Explain the rollout",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	observed := &sessionTitleStore{Store: base, claims: make(chan bool, 2), sets: make(chan bool, 2)}
	factoryCalls := make(chan struct{}, 2)
	s := &Server{
		store: observed,
		sessionTitleModelFactory: func(context.Context, llm.CredentialResolver, string) (einomodel.BaseChatModel, string, error) {
			factoryCalls <- struct{}{}
			return &sessionTitleModel{reply: schema.AssistantMessage("Rollout Risk Summary", nil)}, "test", nil
		},
	}
	run := taskRun{
		Scope: scope, Agent: &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "scout"}},
		SessionID: "chat", Task: "Explain the rollout", Trigger: agentsv1alpha1.RunTriggerChat,
		generateSessionTitle: true, ModelSettings: &store.ModelSettings{ModelCredential: "selected"},
	}
	if !s.shouldGenerateSessionTitle(run, run.Agent, nil, nil) {
		t.Fatal("concurrent first turns should both reach the authoritative store claim")
	}
	for i := 0; i < 2; i++ {
		go s.startSessionTitleGeneration(run, "first-user")
	}

	twins := 0
	for i := 0; i < 2; i++ {
		select {
		case claimed := <-observed.claims:
			if claimed {
				twins++
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent title attempt did not reach the store claim")
		}
	}
	if twins != 1 {
		t.Fatalf("concurrent first-turn claims won %d times, want exactly once", twins)
	}
	select {
	case <-factoryCalls:
	case <-time.After(time.Second):
		t.Fatal("winning first-turn claim did not generate a title")
	}
	select {
	case <-factoryCalls:
		t.Fatal("losing first-turn claim also generated a title")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case changed := <-observed.sets:
		if !changed {
			t.Fatal("winning title was not persisted")
		}
	case <-time.After(time.Second):
		t.Fatal("winning title was not written")
	}
}

func TestAgentSessionTitleWriteSurvivesModelDeadline(t *testing.T) {
	ctx := context.Background()
	base := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "scout"}
	if err := base.AppendMessage(ctx, scope, store.Message{
		ID: "first-user", AgentName: "scout", SessionID: "chat", Role: "user", Content: "Explain the rollout",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := base.ClaimSessionTitle(ctx, scope, "chat", "first-user"); err != nil || !claimed {
		t.Fatalf("claim first-user title slot = %t, err=%v", claimed, err)
	}
	writeStore := contextCheckingSessionTitleStore{Store: base}
	s := &Server{store: writeStore}
	modelCtx, cancelModel := context.WithCancel(ctx)
	cancelModel() // The model deadline has elapsed before the final store write.
	changed, err := s.persistSessionTitle(modelCtx, taskRun{Scope: scope, SessionID: "chat"}, "first-user", "Rollout Risk Summary")
	if err != nil || !changed {
		t.Fatalf("final write with expired model context = %t, err=%v; want bounded detached persistence", changed, err)
	}
	sessions, err := base.ListSessions(ctx, scope, 10)
	if err != nil || len(sessions) != 1 || sessions[0].Title != "Rollout Risk Summary" {
		t.Fatalf("session after detached final write = %+v, err=%v", sessions, err)
	}
}

func TestAgentSessionTitleFailureLeavesPreviewAndDeleteWins(t *testing.T) {
	t.Run("model failure leaves preview", func(t *testing.T) {
		ctx := context.Background()
		base := store.NewMemoryStore()
		scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "scout"}
		if err := base.AppendMessage(ctx, scope, store.Message{ID: "first", AgentName: "scout", SessionID: "chat", Role: "user", Content: "Explain the deployment risk", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		observed := &sessionTitleStore{Store: base, claims: make(chan bool, 1)}
		model := &sessionTitleModel{input: make(chan []*schema.Message, 1), done: make(chan struct{}), err: errors.New("model unavailable")}
		s := &Server{
			store: observed,
			sessionTitleModelFactory: func(context.Context, llm.CredentialResolver, string) (einomodel.BaseChatModel, string, error) {
				return model, "test", nil
			},
		}
		run := taskRun{Scope: scope, Agent: &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "scout"}}, SessionID: "chat", Task: "Explain the deployment risk", ModelSettings: &store.ModelSettings{ModelCredential: "selected"}}
		s.startSessionTitleGeneration(run, "first")
		select {
		case <-model.input:
		case <-time.After(time.Second):
			t.Fatal("title model was not called")
		}
		select {
		case <-model.done:
		case <-time.After(time.Second):
			t.Fatal("title model failure did not return")
		}
		select {
		case claimed := <-observed.claims:
			if !claimed {
				t.Fatal("initial title claim failed")
			}
		default:
			t.Fatal("title attempt claim was not observed")
		}
		sessions, err := base.ListSessions(ctx, scope, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 1 || sessions[0].Title != "" || sessions[0].Preview != "Explain the deployment risk" {
			t.Fatalf("session after title failure = %#v, want the first-message preview", sessions)
		}
	})

	t.Run("delete during generation prevents stale title", func(t *testing.T) {
		ctx := context.Background()
		base := store.NewMemoryStore()
		scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "scout"}
		if err := base.AppendMessage(ctx, scope, store.Message{ID: "first", AgentName: "scout", SessionID: "chat", Role: "user", Content: "Explain the deployment risk", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		observed := &sessionTitleStore{Store: base, claims: make(chan bool, 1), sets: make(chan bool, 1)}
		release := make(chan struct{})
		model := &sessionTitleModel{input: make(chan []*schema.Message, 1), release: release, reply: schema.AssistantMessage("Deployment Risk Overview", nil)}
		s := &Server{store: observed, events: newEventBus()}
		s.sessionTitleModelFactory = func(context.Context, llm.CredentialResolver, string) (einomodel.BaseChatModel, string, error) {
			return model, "test", nil
		}
		run := taskRun{Scope: scope, Agent: &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "scout"}}, SessionID: "chat", Task: "Explain the deployment risk", ModelSettings: &store.ModelSettings{ModelCredential: "selected"}}
		ch, unsubscribe := s.events.subscribe(scope)
		defer unsubscribe()
		s.startSessionTitleGeneration(run, "first")
		select {
		case <-model.input:
		case <-time.After(time.Second):
			t.Fatal("title model was not called")
		}
		if err := base.DeleteSession(ctx, scope, "chat"); err != nil {
			t.Fatal(err)
		}
		close(release)
		select {
		case changed := <-observed.sets:
			if changed {
				t.Fatal("stale title overwrote a deleted session")
			}
		case <-time.After(time.Second):
			t.Fatal("title task did not attempt the stale-session compare-and-set")
		}
		select {
		case ev := <-ch:
			t.Fatalf("deleted session published stale title event: %#v", ev)
		case <-time.After(30 * time.Millisecond):
		}
		sessions, err := base.ListSessions(ctx, scope, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 0 {
			t.Fatalf("deleted session reappeared: %#v", sessions)
		}
	})
}

func TestAgentSessionTitleExcludesNonFreshChatRuns(t *testing.T) {
	base := taskRun{
		Trigger: agentsv1alpha1.RunTriggerChat, generateSessionTitle: true,
		ModelSettings: &store.ModelSettings{ModelCredential: "selected"},
	}
	baseAgent := &agentsv1alpha1.Agent{}
	s := &Server{store: store.NewMemoryStore()}
	if !s.shouldGenerateSessionTitle(base, baseAgent, nil, nil) {
		t.Fatal("fresh model-backed chat should be eligible")
	}
	cases := []struct {
		name string
		edit func(*taskRun, *agentsv1alpha1.Agent, **continuation)
	}{
		{name: "background trigger", edit: func(run *taskRun, _ *agentsv1alpha1.Agent, _ **continuation) {
			run.Trigger = agentsv1alpha1.RunTriggerSchedule
		}},
		{name: "worker", edit: func(run *taskRun, _ *agentsv1alpha1.Agent, _ **continuation) { run.Worker = &workerRun{} }},
		{name: "child", edit: func(run *taskRun, _ *agentsv1alpha1.Agent, _ **continuation) { run.ParentRunID = "parent" }},
		{name: "repository", edit: func(run *taskRun, _ *agentsv1alpha1.Agent, _ **continuation) { run.Repository = &repositoryAttempt{} }},
		{name: "harness", edit: func(_ *taskRun, agent *agentsv1alpha1.Agent, _ **continuation) {
			agent.Spec.Backend.Type = agentsv1alpha1.AgentBackendHarness
			agent.Spec.Backend.Harness = &agentsv1alpha1.AgentHarnessBackend{}
		}},
		{name: "resume", edit: func(_ *taskRun, _ *agentsv1alpha1.Agent, cont **continuation) { *cont = &continuation{} }},
		{name: "not chat transport", edit: func(run *taskRun, _ *agentsv1alpha1.Agent, _ **continuation) { run.generateSessionTitle = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, agent, cont := base, baseAgent.DeepCopy(), (*continuation)(nil)
			tc.edit(&run, agent, &cont)
			if s.shouldGenerateSessionTitle(run, agent, cont, nil) {
				t.Fatal("ineligible run was allowed to generate a session title")
			}
		})
	}
}

func TestSanitizeAgentSessionTitle(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{input: "Title: `Reviewing deployment readiness`", want: "Reviewing deployment readiness"},
		{input: "Only two", want: ""},
		{input: "one two three four five six seven eight", want: "one two three four five six seven"},
		{input: strings.Repeat("x", 100) + " a b", want: ""},
	} {
		if got := sanitizeAgentSessionTitle(tc.input); got != tc.want {
			t.Errorf("sanitizeAgentSessionTitle(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
