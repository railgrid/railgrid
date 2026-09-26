// Copyright 2026 The Railgrid Authors.
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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

// fakeCreds resolves model credentials the way the tenant client does: the
// ModelCredential carries the endpoint, the Secret it points at carries the
// key.
type fakeCreds struct {
	creds   map[string]*agentsv1alpha1.ModelCredential
	secrets map[string]*corev1.Secret
}

func (f fakeCreds) GetSecret(_ context.Context, _, name string) (*corev1.Secret, error) {
	if sec, ok := f.secrets[name]; ok {
		return sec, nil
	}
	return nil, fmt.Errorf("secret %q not found", name)
}

func (f fakeCreds) GetModelCredential(_ context.Context, name string) (*agentsv1alpha1.ModelCredential, error) {
	if cred, ok := f.creds[name]; ok {
		return cred, nil
	}
	return nil, fmt.Errorf("model credential %q not found", name)
}

func credsFor(baseURL string, models map[string]string) fakeCreds {
	out := fakeCreds{
		creds:   map[string]*agentsv1alpha1.ModelCredential{},
		secrets: map[string]*corev1.Secret{},
	}
	for credName, modelID := range models {
		secretName := CredentialSecretName(credName)
		out.creds[credName] = &agentsv1alpha1.ModelCredential{
			ObjectMeta: metav1.ObjectMeta{Name: credName},
			Spec: agentsv1alpha1.ModelCredentialSpec{
				Provider:  llm.ProviderOpenAICompatible,
				BaseURL:   baseURL,
				Model:     modelID,
				SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: secretName},
			},
		}
		out.secrets[secretName] = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName},
			Data:       map[string][]byte{agentsv1alpha1.DefaultModelSecretKey: []byte("test-key")},
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
	mu          sync.Mutex
	requests    []map[string]any
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
		f.mu.Lock()
		f.requests = append(f.requests, body)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flush, _ := w.(http.Flusher)
		chunk := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
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
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flush != nil {
			flush.Flush()
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLLM) requestSnapshot() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.requests...)
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

func TestLoadSessionContextReadsCompletePaginatedHistory(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 520, 4, "gpt-4o")

	loaded, err := f.s.loadSessionContext(ctx, f.scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Checkpoint != nil || len(loaded.Messages) != 520 {
		t.Fatalf("loaded context has checkpoint=%v and %d messages, want no checkpoint and all 520 rows", loaded.Checkpoint != nil, len(loaded.Messages))
	}
	if loaded.Messages[0].ID != "m000" || loaded.Messages[len(loaded.Messages)-1].ID != "m519" {
		t.Fatalf("pagination did not restore chronological history: first=%s last=%s", loaded.Messages[0].ID, loaded.Messages[len(loaded.Messages)-1].ID)
	}
}

func TestLegacySummaryRemainsUntrustedAndReplayable(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 10, 10, "gpt-4o")
	all, err := f.s.loadSessionContext(ctx, f.scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	through := all.Messages[5].CreatedAt
	if err := f.s.store.PutSessionSummary(ctx, f.scope, store.SessionSummary{
		SessionID: "chat", Summary: "THE-LEGACY-SUMMARY", ThroughAt: through, MessageCount: 6,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := f.s.loadSessionContext(ctx, f.scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Checkpoint != nil || loaded.Summary == nil || len(loaded.Messages) != 4 {
		t.Fatalf("legacy load = checkpoint %v, summary %v, %d tail messages", loaded.Checkpoint != nil, loaded.Summary != nil, len(loaded.Messages))
	}
	messages, err := f.s.assembleTurnCtx(ctx, f.run(), "chat", "", false)
	if err != nil {
		t.Fatal(err)
	}
	var summary engine.Message
	for _, message := range messages {
		if strings.Contains(message.Content, "THE-LEGACY-SUMMARY") {
			summary = message
		}
	}
	if summary.Role != engine.RoleUser || !strings.Contains(summary.Content, compactSummaryPrefix) {
		t.Fatalf("legacy summary = %+v, want untrusted user evidence", summary)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "msg000") || strings.Contains(message.Content, "msg005") {
			t.Fatalf("covered legacy messages were replayed: %+v", message)
		}
	}
	if !strings.Contains(messages[len(messages)-2].Content, "msg009") {
		t.Fatalf("legacy summary tail did not include the newest row: %+v", messages[len(messages)-2])
	}
}

func TestAssembleTurnCtxRejectsUnsupportedAndEmptySummaries(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name    string
		summary store.SessionSummary
	}{
		{
			name: "unsupported checkpoint version",
			summary: store.SessionSummary{SessionID: "chat", Summary: "nonempty", Checkpoint: &store.SessionCheckpoint{
				Version: 9, ThroughSequence: 1, ReplacementHistory: []store.SessionCheckpointMessage{{Role: engine.RoleUser, Content: "old"}},
			}},
		},
		{
			name:    "empty legacy summary",
			summary: store.SessionSummary{SessionID: "chat", ThroughAt: time.Now().UTC()},
		},
		{
			name: "empty checkpoint summary",
			summary: store.SessionSummary{SessionID: "chat", Checkpoint: &store.SessionCheckpoint{
				Version: 1, ThroughSequence: 1, ReplacementHistory: []store.SessionCheckpointMessage{{Role: engine.RoleUser, Content: "old"}},
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCompactFixture(t, 0, 0, "gpt-4o")
			f.s.store = &summaryOverrideStore{Store: f.s.store, summary: &test.summary}
			if _, err := f.s.assembleTurnCtx(ctx, f.run(), "chat", "", false); err == nil {
				t.Fatal("invalid summary unexpectedly assembled")
			}
		})
	}
}

func TestLoadSessionContextFailureFailsRunBeforeAppendingTask(t *testing.T) {
	f := newCompactFixture(t, 0, 0, "gpt-4o")
	baseStore := f.s.store
	f.s.store = &summaryOverrideStore{Store: baseStore, readErr: errors.New("injected summary read failure")}

	result, err := f.s.executeTask(context.Background(), taskRun{
		Creds: f.creds, CR: fakeCR{}, Scope: f.scope, Agent: f.agent,
		SessionID: "chat", Task: "must not run without history", Trigger: agentsv1alpha1.RunTriggerChat,
	})
	if err == nil || !strings.Contains(err.Error(), "load session summary") {
		t.Fatalf("executeTask error = %v, want history read failure", err)
	}
	if result.Phase != store.RunPhaseFailed || result.RunID == "" {
		t.Fatalf("result = %+v, want a terminal failed run", result)
	}
	stored, err := baseStore.GetRun(context.Background(), f.scope, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != store.RunPhaseFailed {
		t.Fatalf("stored phase = %s, want Failed", stored.Phase)
	}
	rows, err := baseStore.LoadRecentMessages(context.Background(), f.scope, "chat", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("task was appended despite history load failure: %+v", rows)
	}
}

func TestContextCompactorKeepsCurrentTaskAndLargeToolGroupValid(t *testing.T) {
	f := newCompactFixture(t, 0, 0, "gpt-4o")
	base := time.Now().UTC().Add(-time.Hour)
	for i := range 4 {
		appendCompactMessage(t, f, store.Message{ID: fmt.Sprintf("u%d", i), Role: "user", Content: fmt.Sprintf("request-%d %s", i, strings.Repeat("u", 300)), CreatedAt: base.Add(time.Duration(i*2) * time.Minute)})
		appendCompactMessage(t, f, store.Message{ID: fmt.Sprintf("a%d", i), Role: "assistant", Content: fmt.Sprintf("reply-%d %s", i, strings.Repeat("a", 300)), CreatedAt: base.Add(time.Duration(i*2+1) * time.Minute)})
	}
	toolAt := base.Add(8 * time.Minute)
	call := assistantCallRow("assistant-tool", "prior-run", "call-large", "read_order", `{"orderID":"order-1"}`, toolAt)
	appendCompactMessage(t, f, call)
	resultText := strings.Repeat("sold_at=2033-04-05T06:07:08Z; ", 1200)
	appendCompactMessage(t, f, toolResultRow("tool-large", "prior-run", "call-large", "read_order", resultText, toolAt.Add(time.Second)))
	for i := range 2 {
		appendCompactMessage(t, f, store.Message{ID: fmt.Sprintf("u%d", i+4), Role: "user", Content: fmt.Sprintf("recent-request-%d %s", i, strings.Repeat("r", 300)), CreatedAt: base.Add(time.Duration(10+i*2) * time.Minute)})
		appendCompactMessage(t, f, store.Message{ID: fmt.Sprintf("a%d", i+4), Role: "assistant", Content: fmt.Sprintf("recent-reply-%d %s", i, strings.Repeat("s", 300)), CreatedAt: base.Add(time.Duration(11+i*2) * time.Minute)})
	}

	run, history := appendAndAssembleCurrentTask(t, f, "run-current", "CURRENT TASK MUST SURVIVE")
	result, err := runContextCompactor(t, f, run, history, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if engineHistoryTokens(result) > 5000 {
		t.Fatalf("compacted history uses %d tokens, want at most 5000", engineHistoryTokens(result))
	}
	if err := validateStoredHistoryToolPairing(result); err != nil {
		t.Fatalf("compacted history split or orphaned a tool group: %v", err)
	}
	currentCount := 0
	for _, message := range result {
		if message.ID == "task-run-current" && message.Content == "CURRENT TASK MUST SURVIVE" {
			currentCount++
		}
	}
	if currentCount != 1 {
		t.Fatalf("current task appears %d times after compaction, want exactly once", currentCount)
	}
	requests := f.llm.requestSnapshot()
	if len(requests) == 0 {
		t.Fatal("compaction did not call the summary model")
	}
	foundToolEvidence := false
	for _, request := range requests {
		encoded, _ := json.Marshal(request)
		if strings.Contains(string(encoded), "call-large") && strings.Contains(string(encoded), "sold_at=2033-04-05T06:07:08Z") {
			foundToolEvidence = true
			break
		}
	}
	if !foundToolEvidence {
		t.Fatal("the complete tool group was not provided together as summary evidence")
	}

	loaded := loadReplayHistory(t, f)
	assertUniqueHistoryIDs(t, loaded)
	currentCount = 0
	for _, message := range loaded {
		if message.ID == "task-run-current" {
			currentCount++
		}
	}
	if currentCount != 1 {
		t.Fatalf("reloaded current task appears %d times, want exactly once", currentCount)
	}
}

func TestRepeatedCompactionReloadHasNoDuplicateSourceIDs(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 20, 1800, "gpt-4o")
	run1, history1 := appendAndAssembleCurrentTask(t, f, "run-one", "first compaction task")
	if _, err := runContextCompactor(t, f, run1, history1, 12000); err != nil {
		t.Fatal(err)
	}
	first, ok, err := f.s.store.GetSessionSummary(ctx, f.scope, "chat")
	if err != nil || !ok || first.Checkpoint == nil {
		t.Fatalf("first checkpoint missing: ok=%v err=%v", ok, err)
	}

	base := time.Now().UTC().Add(time.Minute)
	for i := range 8 {
		appendCompactMessage(t, f, store.Message{ID: fmt.Sprintf("new-u%d", i), Role: "user", Content: strings.Repeat("new request ", 180), CreatedAt: base.Add(time.Duration(i*2) * time.Minute)})
		appendCompactMessage(t, f, store.Message{ID: fmt.Sprintf("new-a%d", i), Role: "assistant", Content: strings.Repeat("new answer ", 180), CreatedAt: base.Add(time.Duration(i*2+1) * time.Minute)})
	}
	run2, history2 := appendAndAssembleCurrentTask(t, f, "run-two", "second compaction task")
	if _, err := runContextCompactor(t, f, run2, history2, 12000); err != nil {
		t.Fatal(err)
	}
	second, ok, err := f.s.store.GetSessionSummary(ctx, f.scope, "chat")
	if err != nil || !ok || second.Checkpoint == nil {
		t.Fatalf("second checkpoint missing: ok=%v err=%v", ok, err)
	}
	if second.Checkpoint.ThroughSequence <= first.Checkpoint.ThroughSequence {
		t.Fatalf("checkpoint boundary did not advance: first=%d second=%d", first.Checkpoint.ThroughSequence, second.Checkpoint.ThroughSequence)
	}
	loaded := loadReplayHistory(t, f)
	assertUniqueHistoryIDs(t, loaded)
	if !containsHistoryID(loaded, "task-run-two") {
		t.Fatalf("reloaded history lost the current task: %+v", loaded)
	}
}

func TestCompactionPersistenceFailureLeavesOriginalCheckpoint(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 20, 1800, "gpt-4o")
	run1, history1 := appendAndAssembleCurrentTask(t, f, "run-base", "initial task")
	if _, err := runContextCompactor(t, f, run1, history1, 12000); err != nil {
		t.Fatal(err)
	}
	baseStore := f.s.store
	before, ok, err := baseStore.GetSessionSummary(ctx, f.scope, "chat")
	if err != nil || !ok || before.Checkpoint == nil {
		t.Fatalf("original checkpoint missing: ok=%v err=%v", ok, err)
	}
	base := time.Now().UTC().Add(time.Minute)
	for i := range 8 {
		appendCompactMessage(t, f, store.Message{ID: fmt.Sprintf("failure-u%d", i), Role: "user", Content: strings.Repeat("more request ", 180), CreatedAt: base.Add(time.Duration(i*2) * time.Minute)})
		appendCompactMessage(t, f, store.Message{ID: fmt.Sprintf("failure-a%d", i), Role: "assistant", Content: strings.Repeat("more answer ", 180), CreatedAt: base.Add(time.Duration(i*2+1) * time.Minute)})
	}
	f.s.store = &summaryOverrideStore{Store: baseStore, putErr: errors.New("injected checkpoint write failure")}
	run2, history2 := appendAndAssembleCurrentTask(t, f, "run-failed", "must not replace original checkpoint")
	if _, err := runContextCompactor(t, f, run2, history2, 12000); err == nil || !strings.Contains(err.Error(), "checkpoint write failure") {
		t.Fatalf("compaction error = %v, want checkpoint persistence failure", err)
	}
	after, ok, err := baseStore.GetSessionSummary(ctx, f.scope, "chat")
	if err != nil || !ok {
		t.Fatalf("original checkpoint disappeared after failed write: ok=%v err=%v", ok, err)
	}
	if after.Checkpoint.ThroughSequence != before.Checkpoint.ThroughSequence || after.Summary != before.Summary {
		t.Fatalf("failed persistence replaced the existing checkpoint: before=%+v after=%+v", before.Checkpoint, after.Checkpoint)
	}
}

func TestConcurrentAppendAfterCompactionSnapshotRemainsInSuffix(t *testing.T) {
	ctx := context.Background()
	f := newCompactFixture(t, 20, 1200, "gpt-4o")
	baseStore := f.s.store
	concurrentID := "concurrent-after-snapshot"
	f.s.store = &summaryOverrideStore{Store: baseStore, beforePut: func(ctx context.Context, scope store.Scope) {
		appendErr := baseStore.AppendMessage(ctx, scope, store.Message{
			ID: concurrentID, AgentName: "scout", SessionID: "chat", RunID: "another-run",
			Role: "user", Content: "newer concurrent transcript row", CreatedAt: time.Now().UTC().Add(time.Hour),
		})
		if appendErr != nil {
			t.Errorf("append concurrent row: %v", appendErr)
		}
	}}
	run, history := appendAndAssembleCurrentTask(t, f, "run-concurrent", "current task")
	if _, err := runContextCompactor(t, f, run, history, 12000); err != nil {
		t.Fatal(err)
	}
	loaded, err := f.s.loadSessionContext(ctx, f.scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range loaded.Messages {
		if row.ID == concurrentID {
			found = true
		}
	}
	if !found {
		t.Fatalf("row appended after the checkpoint snapshot was lost; through=%d suffix=%+v", loaded.Checkpoint.ThroughSequence, loaded.Messages)
	}
}

func appendCompactMessage(t *testing.T, f *compactFixture, message store.Message) {
	t.Helper()
	message.AgentName = f.agent.Name
	message.SessionID = "chat"
	if err := f.s.store.AppendMessage(context.Background(), f.scope, message); err != nil {
		t.Fatal(err)
	}
}

func appendAndAssembleCurrentTask(t *testing.T, f *compactFixture, runID, task string) (taskRun, []engine.Message) {
	t.Helper()
	run := f.run()
	run.RunID, run.Task = runID, task
	history, err := f.s.assembleTurnCtx(context.Background(), run, "chat", "", false)
	if err != nil {
		t.Fatal(err)
	}

	message := store.Message{
		ID: "task-" + runID, AgentName: f.agent.Name, SessionID: "chat", RunID: runID,
		Role: "user", Content: task,
	}
	rows, err := f.s.store.LoadRecentMessages(context.Background(), f.scope, "chat", 500)
	if err != nil {
		t.Fatal(err)
	}
	message.CreatedAt = time.Now().UTC()
	if len(rows) > 0 && !rows[len(rows)-1].CreatedAt.Before(message.CreatedAt) {
		message.CreatedAt = rows[len(rows)-1].CreatedAt.Add(time.Nanosecond)
	}
	if err := f.s.store.AppendMessage(context.Background(), f.scope, message); err != nil {
		t.Fatal(err)
	}
	rows, err = f.s.store.LoadRecentMessages(context.Background(), f.scope, "chat", 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == message.ID {
			message = row
			break
		}
	}
	history[len(history)-1].ID = message.ID
	history[len(history)-1].Sequence = message.Sequence
	return run, history
}

func runContextCompactor(t *testing.T, f *compactFixture, run taskRun, history []engine.Message, budget int) ([]engine.Message, error) {
	t.Helper()
	messageTokens := engineHistoryTokens(history)
	estimate := engine.ContextEstimate{
		MessageTokens: messageTokens, ToolSchemaTokens: 128,
		TotalTokens: messageTokens + 128, BudgetTokens: budget,
	}
	return f.s.contextCompactor(run, "chat", "gpt-4o")(context.Background(), history, estimate)
}

func loadReplayHistory(t *testing.T, f *compactFixture) []engine.Message {
	t.Helper()
	loaded, err := f.s.loadSessionContext(context.Background(), f.scope, "chat")
	if err != nil {
		t.Fatal(err)
	}
	var replay []engine.Message
	if loaded.Checkpoint != nil {
		replay, err = engineMessagesFromCheckpoint(loaded.Checkpoint.ReplacementHistory)
		if err != nil {
			t.Fatal(err)
		}
	} else if loaded.Summary != nil {
		replay = append(replay, summaryMessage(*loaded.Summary))
	}
	replay = append(replay, engineMessagesFromHistory(loaded.Messages)...)
	return replay
}

func assertUniqueHistoryIDs(t *testing.T, history []engine.Message) {
	t.Helper()
	seen := map[string]bool{}
	for _, message := range history {
		if message.ID == "" {
			continue
		}
		if seen[message.ID] {
			t.Fatalf("source message ID %q was replayed more than once", message.ID)
		}
		seen[message.ID] = true
	}
}

func containsHistoryID(history []engine.Message, id string) bool {
	for _, message := range history {
		if message.ID == id {
			return true
		}
	}
	return false
}

type summaryOverrideStore struct {
	store.Store
	summary   *store.SessionSummary
	readErr   error
	putErr    error
	beforePut func(context.Context, store.Scope)
}

func (s *summaryOverrideStore) GetSessionSummary(ctx context.Context, scope store.Scope, sessionID string) (store.SessionSummary, bool, error) {
	if s.readErr != nil {
		return store.SessionSummary{}, false, s.readErr
	}
	if s.summary != nil {
		return *s.summary, true, nil
	}
	return s.Store.GetSessionSummary(ctx, scope, sessionID)
}

func (s *summaryOverrideStore) PutSessionSummary(ctx context.Context, scope store.Scope, summary store.SessionSummary) error {
	if s.beforePut != nil {
		s.beforePut(ctx, scope)
	}
	if s.putErr != nil {
		return s.putErr
	}
	return s.Store.PutSessionSummary(ctx, scope, summary)
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
