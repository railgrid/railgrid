// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
)

// fakeCreds serves model-credential Secrets from a map, standing in for the
// tenant client.
type fakeCreds map[string]*corev1.Secret

func (f fakeCreds) GetSecret(_ context.Context, _, name string) (*corev1.Secret, error) {
	if sec, ok := f[name]; ok {
		return sec, nil
	}
	return nil, fmt.Errorf("secret %q not found", name)
}

func credsFor(baseURL string, models map[string]string) fakeCreds {
	out := fakeCreds{}
	for credName, modelID := range models {
		out[llm.CredentialSecretName(credName)] = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: llm.CredentialSecretName(credName)},
			Data: map[string][]byte{
				"provider": []byte(llm.ProviderOpenAICompatible),
				"baseURL":  []byte(baseURL),
				"model":    []byte(modelID),
				"apiKey":   []byte("test-key"),
			},
		}
	}
	return out
}

// fakeLLM is an OpenAI-compatible streaming endpoint that answers every
// completion with reply. It records how many calls it served.
type fakeLLM struct {
	srv   *httptest.Server
	calls atomic.Int64
	// lastRequest is the decoded body of the most recent call.
	lastRequest atomic.Pointer[map[string]any]
}

func newFakeLLM(t *testing.T, reply string) *fakeLLM {
	t.Helper()
	f := &fakeLLM{}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastRequest.Store(&body)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flush, _ := w.(http.Flusher)
		chunk := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
			if flush != nil {
				flush.Flush()
			}
		}
		chunk(map[string]any{
			"id": "c1", "object": "chat.completion.chunk", "created": 1, "model": "fake",
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"role": "assistant", "content": reply},
			}},
		})
		chunk(map[string]any{
			"id": "c1", "object": "chat.completion.chunk", "created": 1, "model": "fake",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120},
		})
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flush != nil {
			flush.Flush()
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// compactFixture is a server plus one agent and a seeded session.
type compactFixture struct {
	s     *Server
	scope store.Scope
	agent *agentsv1alpha1.Agent
	creds fakeCreds
	llm   *fakeLLM
}

// newCompactFixture seeds `msgs` alternating user/assistant messages, each
// `bodyChars` long, oldest first.
func newCompactFixture(t *testing.T, msgs, bodyChars int, modelID string) *compactFixture {
	t.Helper()
	f := &compactFixture{
		s:     &Server{store: store.NewMemoryStore(), engine: engine.New(), events: newEventBus(), liveRuns: newRunRegistry()},
		scope: store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "scout"},
		llm:   newFakeLLM(t, "COMPACTED: the user asked about deploys; the assistant set replicas to 3."),
	}
	f.agent = &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "scout"}}
	f.agent.Spec.SystemPrompt = "You are scout."
	f.agent.Spec.Models = map[string]string{"chat": "main"}
	f.creds = credsFor(f.llm.srv.URL, map[string]string{"main": modelID})

	base := time.Now().UTC().Add(-time.Duration(msgs) * time.Minute)
	for i := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := f.s.store.AppendMessage(context.Background(), f.scope, store.Message{
			ID: fmt.Sprintf("m%03d", i), AgentName: "scout", SessionID: "chat",
			Role: role, Content: fmt.Sprintf("msg%03d ", i) + strings.Repeat("x", bodyChars),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *compactFixture) run() taskRun {
	return taskRun{
		Creds: f.creds, Scope: f.scope, Agent: f.agent,
		SessionID: "chat", Task: "what next?", Trigger: agentsv1alpha1.RunTriggerChat,
	}
}

func TestLoadSessionContextDropsCoveredMessages(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 10, 10, "gpt-4o")

	all := f.s.loadSessionContext(ctx, f.scope, "chat", 40)
	if all.Summary != nil {
		t.Fatal("no summary should exist yet")
	}
	if len(all.Messages) != 10 {
		t.Fatalf("got %d messages, want 10", len(all.Messages))
	}

	// Fold the first six.
	through := all.Messages[5].CreatedAt
	if err := f.s.store.PutSessionSummary(ctx, f.scope, store.SessionSummary{
		SessionID: "chat", Summary: "earlier talk", ThroughAt: through, MessageCount: 6,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	after := f.s.loadSessionContext(ctx, f.scope, "chat", 40)
	if after.Summary == nil {
		t.Fatal("expected the summary to be loaded")
	}
	if len(after.Messages) != 4 {
		t.Fatalf("got %d messages after the summary, want 4", len(after.Messages))
	}
	// The message exactly at ThroughAt is covered, not replayed.
	for _, m := range after.Messages {
		if !m.CreatedAt.After(through) {
			t.Fatalf("message %s at or before ThroughAt is still replayed", m.ID)
		}
	}
}

func TestAssembleTurnCtxReplaysSummaryInsteadOfFoldedMessages(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 10, 10, "gpt-4o")
	msgs := f.s.loadSessionContext(ctx, f.scope, "chat", 40).Messages
	if err := f.s.store.PutSessionSummary(ctx, f.scope, store.SessionSummary{
		SessionID: "chat", Summary: "THE-SUMMARY", ThroughAt: msgs[5].CreatedAt, MessageCount: 6,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	for _, m := range f.s.assembleTurnCtx(ctx, f.run(), "chat", "", false) {
		b.WriteString(m.Role + ": " + m.Content + "\n")
	}
	out := b.String()

	if !strings.Contains(out, "THE-SUMMARY") {
		t.Fatalf("the summary must be replayed:\n%s", out)
	}
	if strings.Contains(out, "msg000") || strings.Contains(out, "msg005") {
		t.Fatalf("folded messages must not be replayed as well:\n%s", out)
	}
	if !strings.Contains(out, "msg009") {
		t.Fatalf("messages after the summary must still be replayed:\n%s", out)
	}
	// The model has to know this is a record of earlier turns, not a fresh turn.
	if !strings.Contains(out, "Summary of this conversation") {
		t.Fatalf("the summary needs framing so it is not mistaken for a user turn:\n%s", out)
	}
}

func TestMaybeCompactSessionUnderBudgetDoesNothing(t *testing.T) {
	ctx := context.Background()
	// gpt-4o is a 128k window; a handful of short messages is nowhere near it.
	f := newCompactFixture(t, 20, 50, "gpt-4o")

	f.s.maybeCompactSession(ctx, f.run(), "chat", "gpt-4o")

	if _, ok, _ := f.s.store.GetSessionSummary(ctx, f.scope, "chat"); ok {
		t.Fatal("a small session must not be compacted")
	}
	if n := f.llm.calls.Load(); n != 0 {
		t.Fatalf("the compaction model was called %d times for a small session", n)
	}
}

func TestMaybeCompactSessionFoldsOlderMessages(t *testing.T) {
	ctx := context.Background()
	// claude-3-5-haiku is 200k tokens; 30 messages of 40k chars each is ~300k
	// tokens of history, comfortably over the 70% threshold.
	f := newCompactFixture(t, 30, 40000, "claude-3-5-haiku")

	f.s.maybeCompactSession(ctx, f.run(), "chat", "claude-3-5-haiku")

	sum, ok, err := f.s.store.GetSessionSummary(ctx, f.scope, "chat")
	if err != nil || !ok {
		t.Fatalf("expected a summary to be stored: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(sum.Summary, "COMPACTED") {
		t.Fatalf("summary = %q, want the compaction model's output", sum.Summary)
	}
	if sum.MessageCount != 30-compactKeepMessages {
		t.Fatalf("MessageCount = %d, want %d (all but the newest %d)", sum.MessageCount, 30-compactKeepMessages, compactKeepMessages)
	}
	if n := f.llm.calls.Load(); n != 1 {
		t.Fatalf("the compaction model was called %d times, want 1", n)
	}

	// The newest messages survive verbatim; the rest are represented by the summary.
	after := f.s.loadSessionContext(ctx, f.scope, "chat", 40)
	if len(after.Messages) != compactKeepMessages {
		t.Fatalf("%d messages replay after compaction, want %d", len(after.Messages), compactKeepMessages)
	}
	if !strings.HasPrefix(after.Messages[0].Content, "msg020") {
		t.Fatalf("the kept window should start at msg020, got %q", after.Messages[0].Content[:10])
	}

	t.Run("compaction is billed to the agent", func(t *testing.T) {
		u, err := f.s.store.GetUsage(ctx, f.scope, "scout", time.Now().UTC(), 30*24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if u.InputTokens != 100 || u.OutputTokens != 20 {
			t.Fatalf("usage = %d in / %d out, want the compaction call's 100/20 — it is real spend", u.InputTokens, u.OutputTokens)
		}
	})

	t.Run("the summarizer is asked to preserve decisions", func(t *testing.T) {
		body := f.llm.lastRequest.Load()
		if body == nil {
			t.Fatal("no request recorded")
		}
		raw, _ := json.Marshal(*body)
		if !strings.Contains(string(raw), "Decisions made and commitments given") {
			t.Fatal("the compaction system prompt should be sent with the fold request")
		}
	})
}

func TestMaybeCompactSessionFoldsPreviousSummary(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 30, 40000, "claude-3-5-haiku")

	// First pass.
	f.s.maybeCompactSession(ctx, f.run(), "chat", "claude-3-5-haiku")
	first, ok, _ := f.s.store.GetSessionSummary(ctx, f.scope, "chat")
	if !ok {
		t.Fatal("expected a first summary")
	}

	// More conversation arrives, pushing it over again.
	base := time.Now().UTC()
	for i := range 20 {
		if err := f.s.store.AppendMessage(ctx, f.scope, store.Message{
			ID: fmt.Sprintf("n%03d", i), AgentName: "scout", SessionID: "chat", Role: "user",
			Content:   fmt.Sprintf("new%03d ", i) + strings.Repeat("y", 40000),
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.s.maybeCompactSession(ctx, f.run(), "chat", "claude-3-5-haiku")

	second, ok, _ := f.s.store.GetSessionSummary(ctx, f.scope, "chat")
	if !ok {
		t.Fatal("expected the summary to still exist")
	}
	if !second.ThroughAt.After(first.ThroughAt) {
		t.Fatal("the second fold must advance ThroughAt")
	}
	// The row always stands for the whole prefix, so its count accumulates.
	if second.MessageCount <= first.MessageCount {
		t.Fatalf("MessageCount went %d → %d; the new summary subsumes the old one and must cover more",
			first.MessageCount, second.MessageCount)
	}
	// The previous summary is folded in, not left behind as a second block.
	body := f.llm.lastRequest.Load()
	raw, _ := json.Marshal(*body)
	if !strings.Contains(string(raw), "Summary of the conversation before this excerpt") {
		t.Fatal("the previous summary should be handed to the summarizer to merge")
	}
}

func TestMaybeCompactSessionSkipsWorkers(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 30, 40000, "claude-3-5-haiku")
	run := f.run()
	run.Worker = &workerRun{Depth: 1, MaxToolTurns: 4}

	f.s.maybeCompactSession(ctx, run, "chat", "claude-3-5-haiku")

	if _, ok, _ := f.s.store.GetSessionSummary(ctx, f.scope, "chat"); ok {
		t.Fatal("a worker starts from a fresh session; there is nothing to compact and nothing to pay for")
	}
}

func TestMaybeCompactSessionSurvivesAnUnavailableModel(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 30, 40000, "claude-3-5-haiku")
	run := f.run()
	run.Creds = fakeCreds{} // no credentials at all

	// Must not panic and must not wedge the run: compaction degrades, the turn
	// proceeds with whatever context it has.
	f.s.maybeCompactSession(ctx, run, "chat", "claude-3-5-haiku")

	if _, ok, _ := f.s.store.GetSessionSummary(ctx, f.scope, "chat"); ok {
		t.Fatal("no summary should be stored when summarization failed")
	}
}

func TestDeleteSessionClearsTheSummary(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 10, 10, "gpt-4o")
	if err := f.s.store.PutSessionSummary(ctx, f.scope, store.SessionSummary{
		SessionID: "chat", Summary: "s", ThroughAt: time.Now().UTC(), MessageCount: 3,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.store.DeleteSession(ctx, f.scope, "chat"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.s.store.GetSessionSummary(ctx, f.scope, "chat"); ok {
		t.Fatal("/new wipes the transcript, so the summary standing in for it must go too")
	}
}

func TestTurnContextBudget(t *testing.T) {
	// A known model uses its catalog window; an unknown one the documented floor.
	if got, want := turnContextBudget("gpt-4o"), 128000*turnContextBudgetPct/100; got != want {
		t.Fatalf("budget for gpt-4o = %d, want %d", got, want)
	}
	if got, want := turnContextBudget("some-unlisted-model"), llm.DefaultContextWindow*turnContextBudgetPct/100; got != want {
		t.Fatalf("budget for an unknown model = %d, want the default-window budget %d", got, want)
	}
	// The in-turn budget must be looser than the compaction threshold, so
	// compaction gets first chance and clipping is the fallback.
	if turnContextBudgetPct <= compactThresholdPct {
		t.Fatalf("turn budget %d%% must exceed the compaction threshold %d%%", turnContextBudgetPct, compactThresholdPct)
	}
}

// A streaming run must outlive its stream. Closing a tab used to cancel the run
// behind it, which for a research fan-out throws away minutes of real spend and
// leaves the user with a transcript that looks like the reply never came.
func TestDetachedStreamContext(t *testing.T) {
	reqCtx, cancel := context.WithCancel(context.Background())
	r, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "/api/agents/x/chat", nil)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, clientGone := detachedStreamContext(r)

	if clientGone() {
		t.Fatal("the client is still connected")
	}
	if runCtx.Err() != nil {
		t.Fatal("the run context should be live")
	}

	cancel() // the client hangs up

	if !clientGone() {
		t.Fatal("clientGone must report the disconnect so writes stop")
	}
	if runCtx.Err() != nil {
		t.Fatal("the run must survive the client hanging up — that is the whole point")
	}

	// Values still resolve: the run context is a detach, not a fresh context, so
	// anything the middleware attached is still reachable.
	type key struct{}
	valued := context.WithValue(reqCtx, key{}, "v")
	r2, _ := http.NewRequestWithContext(valued, http.MethodPost, "/", nil)
	runCtx2, _ := detachedStreamContext(r2)
	if runCtx2.Value(key{}) != "v" {
		t.Fatal("detaching must preserve request-scoped values")
	}
}

// The end the detach exists for: a run whose caller has gone away still finishes
// and still records its answer where the UI will find it.
func TestRunSurvivesCallerCancellationWhenDetached(t *testing.T) {
	f := newCompactFixture(t, 0, 0, "gpt-4o")
	callerCtx, cancel := context.WithCancel(context.Background())

	// Detach exactly as the chat handler does, then lose the caller before the
	// run starts.
	r, _ := http.NewRequestWithContext(callerCtx, http.MethodPost, "/", nil)
	runCtx, clientGone := detachedStreamContext(r)
	cancel()
	if !clientGone() {
		t.Fatal("expected the caller to be gone")
	}

	res, err := f.s.executeTask(runCtx, taskRun{
		Creds: f.creds, CR: fakeCR{}, Scope: f.scope, Agent: f.agent,
		SessionID: "chat", Task: "research this", Trigger: agentsv1alpha1.RunTriggerChat,
	})
	if err != nil {
		t.Fatalf("the run should have completed despite the caller leaving: %v", err)
	}

	run, err := f.s.store.GetRun(context.Background(), f.scope, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Phase != store.RunPhaseSucceeded {
		t.Fatalf("phase = %s, want Succeeded", run.Phase)
	}
	if run.Output == "" {
		t.Fatal("the answer must be on the run record for GET /api/runs/{id} to return it")
	}
	// And in the transcript, which is what reopening the chat reads.
	msgs, err := f.s.store.LoadRecentMessages(context.Background(), f.scope, "chat", 10)
	if err != nil {
		t.Fatal(err)
	}
	var sawAssistant bool
	for _, m := range msgs {
		if m.Role == "assistant" && m.Content != "" {
			sawAssistant = true
		}
	}
	if !sawAssistant {
		t.Fatal("the reply must land in the session transcript, or reopening the chat shows nothing")
	}
}
