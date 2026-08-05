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
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tools"
)

// A whole research pass, driven through the real executeTask path: the parent
// decomposes, spawns three workers, joins them, and synthesizes; the workers run
// concurrently on the cheap background model, each doing a web_fetch. Nothing is
// stubbed except the model endpoint and the network.
//
// This is the end-to-end case the design doc lists as the remaining gap, and it
// is what the "Research agent" preset produces.

// scriptedLLM is an OpenAI-compatible endpoint that answers each request based
// on what it sees in the conversation, so one server can play both the parent
// (which calls spawn/join) and the workers (which fetch and report).
type scriptedLLM struct {
	srv *httptest.Server
	// workerFetches makes each worker call web_fetch before reporting.
	workerFetches bool
	// workerNeverGivesUp makes each worker retry its fetch on every turn, to
	// prove the per-worker tool-turn budget is what stops it.
	workerNeverGivesUp bool

	mu sync.Mutex
	// calls records (model, kind) per request for assertions about which model
	// did what.
	calls []scriptedCall
}

type scriptedCall struct {
	model string
	kind  string // "parent-plan" | "parent-join" | "parent-final" | "worker-fetch" | "worker-final"
}

func newScriptedLLM(t *testing.T, subtasks []string) *scriptedLLM {
	t.Helper()
	s := &scriptedLLM{}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		var convo strings.Builder
		isWorker, sawToolResult := false, false
		for _, m := range req.Messages {
			convo.WriteString(m.Role + ":" + m.Content + "\n")
			if strings.Contains(m.Content, "scoped worker started by") {
				isWorker = true
			}
			if m.Role == "tool" {
				sawToolResult = true
			}
		}
		text := convo.String()
		hasTool := func(name string) bool {
			for _, tl := range req.Tools {
				if tl.Function.Name == name {
					return true
				}
			}
			return false
		}

		var kind string
		var reply sseReply
		switch {
		case isWorker && s.workerNeverGivesUp:
			kind = "worker-fetch"
			reply = toolCallReply("wf", "web_fetch", `{"url":"http://127.0.0.1:9/blocked"}`)
		case isWorker && s.workerFetches && !sawToolResult:
			// A worker's first move: read a source. The URL is loopback, which the
			// web family's SSRF guard refuses — deliberately, so this exercises a
			// worker whose tool call fails. It attempts the fetch once and then
			// reports, which is what a well-behaved model does with a hard refusal.
			kind = "worker-fetch"
			reply = toolCallReply("wf", "web_fetch", `{"url":"http://127.0.0.1:9/blocked","maxChars":40000}`)
		case isWorker:
			// Report with a Sources block, as the worker preamble asks.
			kind = "worker-final"
			reply = textReply("Finding: throughput doubled in 2026.\n\nSources:\n- https://example.test/a")
		case !strings.Contains(text, "started worker"):
			// Parent, first turn: decompose into one spawn per sub-question. The
			// model emits them as parallel tool calls in a single assistant turn,
			// which is what a real model does.
			kind = "parent-plan"
			if !hasTool("spawn") {
				// Without the grant there is no fan-out to script; answer directly
				// so the "not granted" case is still exercised end to end.
				reply = textReply("I cannot run parallel research here.")
				break
			}
			var calls []toolCall
			for i, st := range subtasks {
				args, _ := json.Marshal(map[string]any{
					"task": st, "tools": []string{"web"}, "maxToolTurns": 3,
				})
				calls = append(calls, toolCall{ID: fmt.Sprintf("sp%d", i), Name: "spawn", Args: string(args)})
			}
			reply = toolCallsReply(calls)
		case !strings.Contains(text, "── worker"):
			// Every worker started: collect them in ONE join.
			kind = "parent-join"
			reply = toolCallReply("jn", "join", `{"timeoutSeconds":30}`)
		default:
			kind = "parent-final"
			reply = textReply("Synthesis: throughput doubled in 2026 across all three areas.\n\nSources:\n- https://example.test/a")
		}

		s.mu.Lock()
		s.calls = append(s.calls, scriptedCall{model: req.Model, kind: kind})
		s.mu.Unlock()
		writeSSE(w, reply)
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *scriptedLLM) countKind(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if c.kind == kind {
			n++
		}
	}
	return n
}

func (s *scriptedLLM) modelsFor(kind string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if c.kind == kind {
			out = append(out, c.model)
		}
	}
	return out
}

type toolCall struct {
	ID, Name, Args string
}

type sseReply struct {
	content string
	calls   []toolCall
}

func textReply(s string) sseReply { return sseReply{content: s} }
func toolCallReply(id, name, args string) sseReply {
	return sseReply{calls: []toolCall{{id, name, args}}}
}
func toolCallsReply(calls []toolCall) sseReply { return sseReply{calls: calls} }

func writeSSE(w http.ResponseWriter, r sseReply) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flush, _ := w.(http.Flusher)
	emit := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flush != nil {
			flush.Flush()
		}
	}
	delta := map[string]any{"role": "assistant"}
	if r.content != "" {
		delta["content"] = r.content
	}
	if len(r.calls) > 0 {
		var tcs []any
		for i, c := range r.calls {
			tcs = append(tcs, map[string]any{
				"index": i, "id": c.ID, "type": "function",
				"function": map[string]any{"name": c.Name, "arguments": c.Args},
			})
		}
		delta["tool_calls"] = tcs
	}
	emit(map[string]any{
		"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "m",
		"choices": []any{map[string]any{"index": 0, "delta": delta}},
	})
	emit(map[string]any{
		"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "m",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flush != nil {
		flush.Flush()
	}
}

// researchAgent is what the "Research agent" preset creates: the research
// persona plus core+web+spawn, with a cheap background model for workers.
func researchAgent(withSpawn bool) *agentsv1alpha1.Agent {
	a := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "researcher"}}
	a.Spec.SystemPrompt = "You are a research agent."
	a.Spec.Models = map[string]string{"chat": "strong", "background": "cheap"}
	a.Spec.Autonomy = agentsv1alpha1.AutonomyAuto
	fams := []string{"core", "web"}
	if withSpawn {
		fams = append(fams, "spawn")
	}
	a.Spec.Tools.Interactive = agentsv1alpha1.ToolGrant{Families: fams}
	a.Spec.Limits.MaxToolTurns = 8
	return a
}

func TestResearchRunEndToEnd(t *testing.T) {
	ctx := context.Background()
	subtasks := []string{"ecosystem in 2026", "main competitors", "recent benchmarks"}
	llm := newScriptedLLM(t, subtasks)

	s := &Server{store: store.NewMemoryStore(), engine: engine.New(), events: newEventBus(), liveRuns: newRunRegistry()}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "researcher"}
	agent := researchAgent(true)
	creds := credsFor(llm.srv.URL, map[string]string{"strong": "gpt-4o", "cheap": "gpt-4o-mini"})

	started := time.Now()
	res, err := s.executeTask(ctx, taskRun{
		Creds: creds, CR: fakeCR{}, Scope: scope, Agent: agent,
		SessionID: "chat", Task: "Research the state of X thoroughly.",
		Trigger: agentsv1alpha1.RunTriggerChat,
	})
	if err != nil {
		t.Fatalf("research run failed: %v", err)
	}
	elapsed := time.Since(started)

	t.Run("the parent answers with a synthesis", func(t *testing.T) {
		if !strings.Contains(res.Content, "Synthesis") {
			t.Fatalf("content = %q, want the parent's synthesis", res.Content)
		}
	})

	t.Run("one spawn per sub-question and exactly one join", func(t *testing.T) {
		if got := llm.countKind("parent-plan"); got != 1 {
			t.Fatalf("%d planning turns, want 1", got)
		}
		if got := llm.countKind("parent-join"); got != 1 {
			t.Fatalf("%d join turns, want 1 — joining per spawn would serialize the fan-out", got)
		}
		if got := llm.countKind("worker-final"); got != len(subtasks) {
			t.Fatalf("%d workers reported, want %d", got, len(subtasks))
		}
	})

	t.Run("workers ran on the cheap background model, the parent on the strong one", func(t *testing.T) {
		for _, m := range llm.modelsFor("worker-final") {
			if m != "gpt-4o-mini" {
				t.Fatalf("a worker ran on %q, want the background model gpt-4o-mini", m)
			}
		}
		for _, m := range llm.modelsFor("parent-final") {
			if m != "gpt-4o" {
				t.Fatalf("the parent ran on %q, want the chat model gpt-4o", m)
			}
		}
	})

	t.Run("the run tree records a child per worker, with output and sources", func(t *testing.T) {
		page, err := s.store.QueryRuns(ctx, scope, store.RunQuery{ParentRunID: res.RunID, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != len(subtasks) {
			t.Fatalf("%d child runs, want %d", len(page.Items), len(subtasks))
		}
		seen := map[string]bool{}
		for _, child := range page.Items {
			if child.Trigger != agentsv1alpha1.RunTriggerSpawn {
				t.Fatalf("child %s trigger = %q, want spawn", child.ID, child.Trigger)
			}
			if child.Phase != store.RunPhaseSucceeded {
				t.Fatalf("child %s phase = %s", child.ID, child.Phase)
			}
			if child.Output == "" {
				t.Fatalf("child %s has no output on its run record", child.ID)
			}
			// The Sources block the preamble asks for is parsed into structure.
			if len(child.Sources) != 1 || child.Sources[0] != "https://example.test/a" {
				t.Fatalf("child %s sources = %v, want the parsed URL", child.ID, child.Sources)
			}
			// And it must NOT be left in the prose.
			if strings.Contains(child.Output, "Sources:") {
				t.Fatalf("child %s output still carries the raw Sources block: %q", child.ID, child.Output)
			}
			seen[child.Input] = true
		}
		for _, st := range subtasks {
			if !seen[st] {
				t.Fatalf("no child run for sub-task %q; got %v", st, seen)
			}
		}
	})

	t.Run("the parent's own output and sources are recorded", func(t *testing.T) {
		run, err := s.store.GetRun(ctx, scope, res.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(run.Output, "Synthesis") {
			t.Fatalf("parent output = %q", run.Output)
		}
		if len(run.Sources) != 1 {
			t.Fatalf("parent sources = %v, want the cited URL", run.Sources)
		}
	})

	t.Run("all spend lands in one bucket — the agent's own", func(t *testing.T) {
		u, err := s.store.GetUsage(ctx, scope, "researcher", time.Now().UTC(), 30*24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		// Parent turns + worker turns, all against the same agent: workers are the
		// same agent in the same scope, so there is no separate rollup to get wrong.
		wantCalls := int64(llm.countKind("parent-plan") + llm.countKind("parent-join") + llm.countKind("parent-final") +
			llm.countKind("worker-fetch") + llm.countKind("worker-final"))
		if u.InputTokens != wantCalls*10 {
			t.Fatalf("input tokens = %d, want %d (10 per model call across parent and workers)", u.InputTokens, wantCalls*10)
		}
	})

	t.Run("the fan-out actually overlapped", func(t *testing.T) {
		// Three workers, each two model calls against a local server. This is a
		// smoke check that nothing serialized pathologically, not a benchmark.
		if elapsed > 20*time.Second {
			t.Fatalf("the run took %s; a three-worker fan-out against a local server should be far faster", elapsed)
		}
	})
}

// A worker whose tool call fails must still report, and the pass must still
// finish: a research run that dies because one of ten sources was unreachable
// would be useless.
func TestResearchWorkerToolFailureDoesNotSinkTheRun(t *testing.T) {
	ctx := context.Background()
	llm := newScriptedLLM(t, []string{"first area", "second area"})
	// Each worker calls web_fetch on a loopback URL, which the real SSRF guard
	// refuses — an authentic tool failure, not a simulated one.
	llm.workerFetches = true

	s := &Server{store: store.NewMemoryStore(), engine: engine.New(), events: newEventBus(), liveRuns: newRunRegistry()}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "researcher"}
	creds := credsFor(llm.srv.URL, map[string]string{"strong": "gpt-4o", "cheap": "gpt-4o-mini"})

	res, err := s.executeTask(ctx, taskRun{
		Creds: creds, CR: fakeCR{}, Scope: scope, Agent: researchAgent(true),
		SessionID: "chat", Task: "Research the state of X thoroughly.",
		Trigger: agentsv1alpha1.RunTriggerChat,
	})
	if err != nil {
		t.Fatalf("the run should survive a worker's failed tool call: %v", err)
	}
	if !strings.Contains(res.Content, "Synthesis") {
		t.Fatalf("the parent should still synthesize; got %q", res.Content)
	}
	if got := llm.countKind("worker-fetch"); got != 2 {
		t.Fatalf("%d workers attempted a fetch, want 2", got)
	}

	// The refusal is recorded as a step on the worker's own run, so the failure is
	// inspectable rather than invisible.
	page, err := s.store.QueryRuns(ctx, scope, store.RunQuery{ParentRunID: res.RunID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("%d child runs, want 2", len(page.Items))
	}
	sawRefusal := false
	for _, child := range page.Items {
		if child.Phase != store.RunPhaseSucceeded {
			t.Fatalf("child %s phase = %s; a failed tool call is an observation, not a failed run", child.ID, child.Phase)
		}
		calls, err := s.store.ListToolCalls(ctx, scope, child.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range calls {
			if c.Tool == "web_fetch" && c.Outcome == "error" && strings.Contains(c.Error, "non-public") {
				sawRefusal = true
			}
		}
	}
	if !sawRefusal {
		t.Fatal("expected the SSRF guard's refusal recorded as a failed web_fetch step on a worker run")
	}
}

func TestResearchRunWithoutTheSpawnGrant(t *testing.T) {
	ctx := context.Background()
	llm := newScriptedLLM(t, []string{"a", "b"})
	s := &Server{store: store.NewMemoryStore(), engine: engine.New(), events: newEventBus(), liveRuns: newRunRegistry()}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "researcher"}
	creds := credsFor(llm.srv.URL, map[string]string{"strong": "gpt-4o", "cheap": "gpt-4o-mini"})

	res, err := s.executeTask(ctx, taskRun{
		Creds: creds, CR: fakeCR{}, Scope: scope, Agent: researchAgent(false),
		SessionID: "chat", Task: "Research the state of X thoroughly.",
		Trigger: agentsv1alpha1.RunTriggerChat,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Fan-out is opt-in: without the grant the tools are simply absent, and the
	// agent answers as best it can rather than erroring.
	if strings.Contains(res.Content, "Synthesis") {
		t.Fatal("no spawn grant, so there should have been no fan-out")
	}
	page, err := s.store.QueryRuns(ctx, scope, store.RunQuery{ParentRunID: res.RunID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("%d child runs without the spawn grant", len(page.Items))
	}
}

// The preset's whole job is that these two lists match what the tools actually
// need; a preset that grants the wrong families is worse than none.
func TestResearchPresetGrantsWhatTheToolsNeed(t *testing.T) {
	agent := researchAgent(true)
	deps := tools.Deps{Agent: agent, CR: fakeCR{}, RunID: "r1", Store: store.NewMemoryStore(),
		Scope: store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "researcher"}}
	s := &Server{store: deps.Store}

	got, _, closer := s.buildToolset(context.Background(), deps,
		taskRun{Agent: agent, Trigger: agentsv1alpha1.RunTriggerChat})
	defer closer()

	for _, want := range []string{"spawn", "join", "web_search", "web_fetch"} {
		if !slicesContains(toolNames(got), want) {
			t.Fatalf("the research preset must yield %q; got %v", want, toolNames(got))
		}
	}
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// A worker that keeps retrying a failing tool must be stopped by its own
// tool-turn budget. Without that bound one unreachable source would burn the
// agent's budget in a loop — and the parent chooses the budget per spawn, so this
// is the knob that has to actually hold.
func TestResearchWorkerRetryIsBoundedByItsToolTurns(t *testing.T) {
	ctx := context.Background()
	llm := newScriptedLLM(t, []string{"only area"})
	llm.workerNeverGivesUp = true // asks for the same failing fetch every turn

	s := &Server{store: store.NewMemoryStore(), engine: engine.New(), events: newEventBus(), liveRuns: newRunRegistry()}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "researcher"}
	creds := credsFor(llm.srv.URL, map[string]string{"strong": "gpt-4o", "cheap": "gpt-4o-mini"})

	if _, err := s.executeTask(ctx, taskRun{
		Creds: creds, CR: fakeCR{}, Scope: scope, Agent: researchAgent(true),
		SessionID: "chat", Task: "Research the state of X.",
		Trigger: agentsv1alpha1.RunTriggerChat,
	}); err != nil {
		t.Fatal(err)
	}

	// The spawn call in the script asks for maxToolTurns: 3, so the worker gets
	// exactly three attempts and then the loop stops it.
	if got := llm.countKind("worker-fetch"); got != 3 {
		t.Fatalf("the worker made %d attempts, want exactly its 3 tool turns", got)
	}
	page, err := s.store.QueryRuns(ctx, scope, store.RunQuery{ParentRunID: "", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	// The worker still ends Succeeded with the engine's truncation marker, rather
	// than failing the pass.
	for _, r := range page.Items {
		if r.Trigger == agentsv1alpha1.RunTriggerSpawn {
			if r.Phase != store.RunPhaseSucceeded {
				t.Fatalf("worker run phase = %s, want Succeeded with a truncation note", r.Phase)
			}
			if !strings.Contains(r.Output, "tool-call limit") {
				t.Fatalf("worker output should say it hit the limit, got %q", r.Output)
			}
		}
	}
}
