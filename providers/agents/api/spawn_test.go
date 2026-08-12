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
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tools"
)

func TestWorkerFamilies(t *testing.T) {
	grantable := []string{"core", "web", "mcp"}

	tests := []struct {
		name      string
		requested []string
		want      []string
	}{
		{"empty request gets the default", nil, []string{"core", "web"}},
		{"core is always present", []string{"web"}, []string{"core", "web"}},
		{"granted families pass through", []string{"web", "mcp"}, []string{"core", "web", "mcp"}},
		{"ungranted families are dropped, not failed", []string{"web", "github", "edges"}, []string{"core", "web"}},
		{"duplicates collapse", []string{"web", "web", "core"}, []string{"core", "web"}},
		{"blanks ignored", []string{"", "  ", "mcp"}, []string{"core", "mcp"}},
		// The whole authorization model: a worker can never widen its parent.
		{"an all-ungranted request narrows to core only", []string{"github"}, []string{"core"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := workerFamilies(tc.requested, grantable)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("workerFamilies(%v) = %v, want %v", tc.requested, got, tc.want)
			}
		})
	}
}

func TestGrantableWorkerFamiliesDropsEdges(t *testing.T) {
	got := grantableWorkerFamilies([]string{"core", "web", "edges", "mcp"})
	if slices.Contains(got, "edges") {
		t.Fatalf("edges must not be passable to a worker (it authenticates as the calling human); got %v", got)
	}
	for _, want := range []string{"core", "web", "mcp"} {
		if !slices.Contains(got, want) {
			t.Fatalf("expected %q to survive; got %v", want, got)
		}
	}
}

func TestSpawnPolicyFor(t *testing.T) {
	agentWithLimits := func(perRun, concurrent int32) *agentsv1alpha1.Agent {
		a := &agentsv1alpha1.Agent{}
		a.Spec.Limits.MaxSpawnsPerRun = perRun
		a.Spec.Limits.MaxConcurrentSpawns = concurrent
		return a
	}

	t.Run("zero uses provider defaults", func(t *testing.T) {
		p := spawnPolicyFor(agentWithLimits(0, 0), []string{"core", "web"})
		if p.MaxPerRun != defaultMaxSpawnsPerRun || p.MaxConcurrent != defaultMaxConcurrentSpawns {
			t.Fatalf("perRun=%d concurrent=%d, want %d/%d", p.MaxPerRun, p.MaxConcurrent, defaultMaxSpawnsPerRun, defaultMaxConcurrentSpawns)
		}
	})

	t.Run("agent limits are honored", func(t *testing.T) {
		p := spawnPolicyFor(agentWithLimits(3, 2), nil)
		if p.MaxPerRun != 3 || p.MaxConcurrent != 2 {
			t.Fatalf("perRun=%d concurrent=%d, want 3/2", p.MaxPerRun, p.MaxConcurrent)
		}
	})

	t.Run("provider caps override an over-large spec", func(t *testing.T) {
		p := spawnPolicyFor(agentWithLimits(1000, 1000), nil)
		if p.MaxPerRun != maxMaxSpawnsPerRun || p.MaxConcurrent != maxMaxConcurrentSpawns {
			t.Fatalf("perRun=%d concurrent=%d, want %d/%d", p.MaxPerRun, p.MaxConcurrent, maxMaxSpawnsPerRun, maxMaxConcurrentSpawns)
		}
	})
}

func TestClampSpawnToolTurns(t *testing.T) {
	if got := clampSpawnToolTurns(0); got != defaultSpawnToolTurns {
		t.Fatalf("0 → %d, want default %d", got, defaultSpawnToolTurns)
	}
	if got := clampSpawnToolTurns(4); got != 4 {
		t.Fatalf("4 → %d, want 4", got)
	}
	if got := clampSpawnToolTurns(999); got != maxSpawnToolTurns {
		t.Fatalf("999 → %d, want cap %d", got, maxSpawnToolTurns)
	}
}

func TestSplitSources(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantBody    string
		wantSources []string
	}{
		{
			name:     "no sources block",
			content:  "Just an answer.",
			wantBody: "Just an answer.",
		},
		{
			name:        "bulleted list",
			content:     "Findings.\n\nSources:\n- https://a.example/x\n- https://b.example/y\n",
			wantBody:    "Findings.",
			wantSources: []string{"https://a.example/x", "https://b.example/y"},
		},
		{
			name:        "bare urls and markdown heading",
			content:     "Findings.\n**Sources:**\nhttps://a.example/x\n",
			wantBody:    "Findings.",
			wantSources: []string{"https://a.example/x"},
		},
		{
			name:        "non-url lines are skipped",
			content:     "Findings.\nSources:\n- internal knowledge\n- https://a.example/x\n",
			wantBody:    "Findings.",
			wantSources: []string{"https://a.example/x"},
		},
		{
			name:        "trailing prose on a source line is dropped",
			content:     "Findings.\nSources:\n- https://a.example/x (the spec)\n",
			wantBody:    "Findings.",
			wantSources: []string{"https://a.example/x"},
		},
		{
			name:        "duplicates collapse",
			content:     "Findings.\nSources:\n- https://a.example/x\n- https://a.example/x\n",
			wantBody:    "Findings.",
			wantSources: []string{"https://a.example/x"},
		},
		{
			name:        "the last heading wins when the word appears in the body",
			content:     "I checked several sources:\nnamely two.\n\nSources:\n- https://a.example/x\n",
			wantBody:    "I checked several sources:\nnamely two.",
			wantSources: []string{"https://a.example/x"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, sources := splitSources(tc.content)
			if body != tc.wantBody {
				t.Fatalf("body = %q, want %q", body, tc.wantBody)
			}
			if !slices.Equal(sources, tc.wantSources) {
				t.Fatalf("sources = %v, want %v", sources, tc.wantSources)
			}
		})
	}
}

// newTestCoordinator builds a coordinator whose workers are the supplied stub
// rather than a real agent run.
func newTestCoordinator(ctx context.Context, exec func(context.Context, taskRun) (runResult, error), maxPerRun, maxConcurrent int) *spawnCoordinator {
	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "researcher"}}
	return &spawnCoordinator{
		exec:   exec,
		runCtx: ctx,
		parent: taskRun{
			Agent: agent, RunID: "parent-run", Trigger: agentsv1alpha1.RunTriggerChat,
			Scope: store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "researcher"},
			Task:  "research the thing",
		},
		families:      []string{"core", "web"},
		maxPerRun:     maxPerRun,
		maxConcurrent: maxConcurrent,
		sem:           make(chan struct{}, maxConcurrent),
		tasks:         map[string]*spawnTask{},
	}
}

func TestSpawnAndJoin(t *testing.T) {
	ctx := context.Background()
	c := newTestCoordinator(ctx, func(_ context.Context, run taskRun) (runResult, error) {
		return runResult{RunID: "child-" + run.SessionID, Content: "found: " + run.Task + "\nSources:\n- https://a.example/x"}, nil
	}, 10, 4)

	id1, err := c.spawn(ctx, tools.SpawnRequest{Task: "sub-task one"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := c.spawn(ctx, tools.SpawnRequest{Task: "sub-task two"})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("task ids must be distinct, both %q", id1)
	}

	out, err := c.join(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sub-task one", "sub-task two", "found: sub-task one", "https://a.example/x"} {
		if !strings.Contains(out, want) {
			t.Fatalf("join output missing %q:\n%s", want, out)
		}
	}

	t.Run("a bare join collects only what is outstanding", func(t *testing.T) {
		again, err := c.join(ctx, nil, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(again, "already collected") {
			t.Fatalf("expected the second bare join to report nothing outstanding, got:\n%s", again)
		}
	})

	t.Run("explicit ids re-report a collected worker", func(t *testing.T) {
		again, err := c.join(ctx, []string{id1}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(again, "found: sub-task one") {
			t.Fatalf("expected the result again for %s, got:\n%s", id1, again)
		}
	})
}

func TestSpawnLimits(t *testing.T) {
	ctx := context.Background()
	c := newTestCoordinator(ctx, func(context.Context, taskRun) (runResult, error) {
		return runResult{Content: "ok"}, nil
	}, 2, 1)

	for i := range 2 {
		if _, err := c.spawn(ctx, tools.SpawnRequest{Task: fmt.Sprintf("task %d", i)}); err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
	}
	_, err := c.spawn(ctx, tools.SpawnRequest{Task: "one too many"})
	if err == nil {
		t.Fatal("expected the third spawn to be refused at maxPerRun=2")
	}
	if !strings.Contains(err.Error(), "spawn limit reached") {
		t.Fatalf("error should name the limit, got: %v", err)
	}

	t.Run("an empty task is refused", func(t *testing.T) {
		if _, err := c.spawn(ctx, tools.SpawnRequest{Task: "   "}); err == nil {
			t.Fatal("expected a blank task to be refused")
		}
	})
}

func TestSpawnConcurrencyBound(t *testing.T) {
	ctx := context.Background()
	const limit = 2
	var live, peak atomic.Int64
	release := make(chan struct{})

	c := newTestCoordinator(ctx, func(context.Context, taskRun) (runResult, error) {
		n := live.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		live.Add(-1)
		return runResult{Content: "ok"}, nil
	}, 6, limit)

	for i := range 6 {
		if _, err := c.spawn(ctx, tools.SpawnRequest{Task: fmt.Sprintf("task %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	// Let the first wave sit at the semaphore, then release everything.
	deadline := time.Now().Add(2 * time.Second)
	for live.Load() < limit && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	close(release)
	c.wait()

	if got := peak.Load(); got > limit {
		t.Fatalf("%d workers ran at once, want at most %d", got, limit)
	}
	if got := peak.Load(); got < limit {
		t.Fatalf("only %d workers ran at once; the fan-out is not actually parallel", got)
	}
}

func TestJoinTimeoutReportsPartialResults(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	var slow atomic.Bool

	c := newTestCoordinator(ctx, func(_ context.Context, run taskRun) (runResult, error) {
		if strings.Contains(run.Task, "slow") {
			slow.Store(true)
			<-release
			return runResult{Content: "eventually"}, nil
		}
		return runResult{Content: "quick answer"}, nil
	}, 10, 4)

	if _, err := c.spawn(ctx, tools.SpawnRequest{Task: "fast task"}); err != nil {
		t.Fatal(err)
	}
	slowID, err := c.spawn(ctx, tools.SpawnRequest{Task: "slow task"})
	if err != nil {
		t.Fatal(err)
	}

	// timeoutSeconds is clamped to a minimum of one second, so this join returns
	// with the fast worker collected and the slow one outstanding.
	out, err := c.join(ctx, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "quick answer") {
		t.Fatalf("expected the finished worker's answer, got:\n%s", out)
	}
	if !strings.Contains(out, "still running") {
		t.Fatalf("expected the unfinished worker reported as still running, got:\n%s", out)
	}

	// The straggler is not lost: it keeps running and a later join collects it.
	close(release)
	out, err = c.join(ctx, []string{slowID}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "eventually") {
		t.Fatalf("expected the straggler collected on the second join, got:\n%s", out)
	}
	c.wait()
}

func TestJoinUnknownID(t *testing.T) {
	ctx := context.Background()
	c := newTestCoordinator(ctx, func(context.Context, taskRun) (runResult, error) {
		return runResult{Content: "ok"}, nil
	}, 4, 2)
	if _, err := c.join(ctx, []string{"t99"}, 1); err == nil {
		t.Fatal("expected an error for an id that was never spawned")
	}
}

func TestJoinReportsWorkerFailureAndApprovalGate(t *testing.T) {
	ctx := context.Background()
	c := newTestCoordinator(ctx, func(_ context.Context, run taskRun) (runResult, error) {
		switch {
		case strings.Contains(run.Task, "boom"):
			return runResult{}, fmt.Errorf("search backend unreachable")
		case strings.Contains(run.Task, "gated"):
			return runResult{Content: "partial", Pending: &pendingInfo{Tool: "github__merge"}}, nil
		}
		return runResult{Content: "fine"}, nil
	}, 10, 4)

	for _, task := range []string{"boom task", "gated task", "ok task"} {
		if _, err := c.spawn(ctx, tools.SpawnRequest{Task: task}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := c.join(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "search backend unreachable") {
		t.Fatalf("a failed worker must say why:\n%s", out)
	}
	// A gated worker reports "not attempted" rather than handing back its
	// partial text as if it were the answer.
	if !strings.Contains(out, "not attempted") {
		t.Fatalf("a gated worker must be reported as not attempted:\n%s", out)
	}
	if strings.Contains(out, "partial") {
		t.Fatalf("a gated worker's partial text must not be presented as a result:\n%s", out)
	}
	if !strings.Contains(out, "fine") {
		t.Fatalf("the healthy worker's answer should still be there:\n%s", out)
	}
}

func TestSpawnCancelledParentAbortsQueuedWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var once sync.Once
	var ran atomic.Int64

	c := newTestCoordinator(ctx, func(runCtx context.Context, _ taskRun) (runResult, error) {
		ran.Add(1)
		once.Do(func() { close(started) })
		<-runCtx.Done()
		return runResult{}, runCtx.Err()
	}, 4, 1)

	for i := range 3 {
		if _, err := c.spawn(ctx, tools.SpawnRequest{Task: fmt.Sprintf("task %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	<-started
	cancel()
	c.wait() // must not hang: queued workers give up on the parent's context

	if got := ran.Load(); got != 1 {
		t.Fatalf("%d workers started, want 1 — the queued ones must abort rather than run after cancel", got)
	}
}

func TestSpawnWorkerRunShape(t *testing.T) {
	ctx := context.Background()
	var got taskRun
	var mu sync.Mutex
	c := newTestCoordinator(ctx, func(_ context.Context, run taskRun) (runResult, error) {
		mu.Lock()
		got = run
		mu.Unlock()
		return runResult{Content: "ok"}, nil
	}, 4, 2)

	if _, err := c.spawn(ctx, tools.SpawnRequest{
		Task: "check the docs", Instructions: "be brief", Families: []string{"web"}, MaxToolTurns: 3,
	}); err != nil {
		t.Fatal(err)
	}
	c.wait()
	mu.Lock()
	defer mu.Unlock()

	if got.Trigger != agentsv1alpha1.RunTriggerSpawn {
		t.Fatalf("trigger = %q, want %q", got.Trigger, agentsv1alpha1.RunTriggerSpawn)
	}
	if got.ParentRunID != "parent-run" {
		t.Fatalf("parentRunID = %q, want the parent's run id", got.ParentRunID)
	}
	if got.Scope.AgentName != "researcher" {
		t.Fatalf("a worker is the same agent; scope agent = %q", got.Scope.AgentName)
	}
	if !strings.HasPrefix(got.SessionID, "spawn:parent-run:") {
		t.Fatalf("sessionID = %q, want a fresh spawn session under the parent", got.SessionID)
	}
	if got.Worker == nil {
		t.Fatal("worker runs must carry a workerRun")
	}
	if got.Worker.Depth != 1 {
		t.Fatalf("depth = %d, want 1 for a worker of a top-level run", got.Worker.Depth)
	}
	if got.Worker.MaxToolTurns != 3 {
		t.Fatalf("maxToolTurns = %d, want the requested 3", got.Worker.MaxToolTurns)
	}
	if got.Worker.Instructions != "be brief" {
		t.Fatalf("instructions = %q", got.Worker.Instructions)
	}
	if got.Worker.ClassTrigger != agentsv1alpha1.RunTriggerChat {
		t.Fatalf("classTrigger = %q, want the parent's trigger so approval class is inherited", got.Worker.ClassTrigger)
	}
	if got.Worker.ParentTask != "research the thing" {
		t.Fatalf("parentTask = %q, want the parent's task for orientation", got.Worker.ParentTask)
	}
}

// toolNames lists a toolset's tool names for assertions.
func toolNames(ts []engine.Tool) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func TestBuildToolsetSpawnGrantAndDepth(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}
	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "researcher"}}
	agent.Spec.Tools.Interactive = agentsv1alpha1.ToolGrant{Families: []string{"core", "web", "spawn"}}
	deps := func() tools.Deps {
		return tools.Deps{Store: s.store, Agent: agent, CR: fakeCR{}, RunID: "r1",
			Scope: store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "researcher"}}
	}

	t.Run("granted at top level", func(t *testing.T) {
		got, _, closer := s.buildToolset(ctx, deps(), taskRun{Agent: agent, Trigger: agentsv1alpha1.RunTriggerChat})
		defer closer()
		for _, want := range []string{"spawn", "join"} {
			if !slices.Contains(toolNames(got), want) {
				t.Fatalf("expected %q in the toolset, got %v", want, toolNames(got))
			}
		}
	})

	t.Run("absent without the family grant", func(t *testing.T) {
		plain := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}
		plain.Spec.Tools.Interactive = agentsv1alpha1.ToolGrant{Families: []string{"core", "web"}}
		d := deps()
		d.Agent = plain
		got, _, closer := s.buildToolset(ctx, d, taskRun{Agent: plain, Trigger: agentsv1alpha1.RunTriggerChat})
		defer closer()
		if slices.Contains(toolNames(got), "spawn") {
			t.Fatalf("spawn must be opt-in; got %v", toolNames(got))
		}
	})

	t.Run("a depth-1 worker may still spawn", func(t *testing.T) {
		run := taskRun{Agent: agent, Trigger: agentsv1alpha1.RunTriggerSpawn, Worker: &workerRun{
			Depth: 1, Families: []string{"core", "web", "spawn"}, ClassTrigger: agentsv1alpha1.RunTriggerChat, MaxToolTurns: 4,
		}}
		got, _, closer := s.buildToolset(ctx, deps(), run)
		defer closer()
		if !slices.Contains(toolNames(got), "spawn") {
			t.Fatalf("depth 1 is below the limit, spawn should be present; got %v", toolNames(got))
		}
	})

	t.Run("a depth-2 worker may not", func(t *testing.T) {
		run := taskRun{Agent: agent, Trigger: agentsv1alpha1.RunTriggerSpawn, Worker: &workerRun{
			Depth: maxSpawnDepth, Families: []string{"core", "web", "spawn"}, ClassTrigger: agentsv1alpha1.RunTriggerChat, MaxToolTurns: 4,
		}}
		got, _, closer := s.buildToolset(ctx, deps(), run)
		defer closer()
		if slices.Contains(toolNames(got), "spawn") {
			t.Fatalf("depth %d is the limit, spawn must be withheld; got %v", maxSpawnDepth, toolNames(got))
		}
		if slices.Contains(toolNames(got), "join") {
			t.Fatalf("join is useless without spawn; got %v", toolNames(got))
		}
	})
}

func TestBuildToolsetWorkerExcludesSelfManagementTools(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}
	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "researcher"}}
	agent.Spec.Delegates = []string{"other"}
	agent.Spec.Tools.Interactive = agentsv1alpha1.ToolGrant{Families: []string{"core", "web"}}
	deps := tools.Deps{Store: s.store, Agent: agent, CR: fakeCR{}, RunID: "r1",
		Scope: store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "researcher"}}

	parent, _, closeParent := s.buildToolset(ctx, deps, taskRun{Agent: agent, Trigger: agentsv1alpha1.RunTriggerChat})
	closeParent()
	// Sanity: the parent does have them, so the worker assertion below is
	// testing the filter rather than an absence that was always there.
	for _, want := range []string{"notify", "ask", "memory_save", "schedule_create", "delegate"} {
		if !slices.Contains(toolNames(parent), want) {
			t.Fatalf("expected the parent to have %q; got %v", want, toolNames(parent))
		}
	}

	worker, _, closeWorker := s.buildToolset(ctx, deps, taskRun{
		Agent: agent, Trigger: agentsv1alpha1.RunTriggerSpawn,
		Worker: &workerRun{Depth: 1, Families: []string{"core", "web"}, ClassTrigger: agentsv1alpha1.RunTriggerChat, MaxToolTurns: 4},
	})
	defer closeWorker()
	names := toolNames(worker)
	for _, banned := range []string{"notify", "ask", "memory_save", "schedule_create", "schedule_update", "schedule_delete", "schedules_list", "delegate"} {
		if slices.Contains(names, banned) {
			t.Fatalf("a worker must not get %q (it manages the agent or talks to a human); got %v", banned, names)
		}
	}
	// It keeps what a worker actually needs.
	for _, want := range []string{"web_fetch", "web_search", "wait", "memory_list"} {
		if !slices.Contains(names, want) {
			t.Fatalf("expected a worker to keep %q; got %v", want, names)
		}
	}
}

func TestWorkerTurnContextIsFresh(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "researcher"}
	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "researcher"}}
	agent.Spec.SystemPrompt = "You are Faros's research agent."

	// A memory note and a prior transcript in the SAME session, both of which a
	// worker must not inherit.
	if err := s.store.PutMemory(ctx, scope, store.Memory{
		ID: "m1", AgentName: "researcher", Title: "secret-note", Body: "remembered thing",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.AppendMessage(ctx, scope, store.Message{
		ID: "msg1", AgentName: "researcher", SessionID: "shared", Role: "user",
		Content: "earlier conversation turn", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	joined := func(msgs []engine.Message) string {
		var b strings.Builder
		for _, m := range msgs {
			b.WriteString(m.Role + ": " + m.Content + "\n")
		}
		return b.String()
	}

	t.Run("an ordinary run gets memory and history", func(t *testing.T) {
		out := joined(s.assembleTurnCtx(ctx, taskRun{
			Scope: scope, Agent: agent, Task: "do the thing", Trigger: agentsv1alpha1.RunTriggerChat,
		}, "shared", "", false))
		if !strings.Contains(out, "secret-note") || !strings.Contains(out, "earlier conversation turn") {
			t.Fatalf("baseline run should carry memory + history:\n%s", out)
		}
	})

	t.Run("a worker gets neither, plus the sub-agent preamble", func(t *testing.T) {
		out := joined(s.assembleTurnCtx(ctx, taskRun{
			Scope: scope, Agent: agent, Task: "check one fact", Trigger: agentsv1alpha1.RunTriggerSpawn,
			Worker: &workerRun{Depth: 1, Instructions: "be brief", ParentTask: "the big question", MaxToolTurns: 4},
		}, "shared", "", false))
		if strings.Contains(out, "secret-note") {
			t.Fatalf("a worker must not get memory injection:\n%s", out)
		}
		if strings.Contains(out, "earlier conversation turn") {
			t.Fatalf("a worker must not inherit session history:\n%s", out)
		}
		if !strings.Contains(out, "Sources:") {
			t.Fatalf("the preamble must ask for sources (join parses them):\n%s", out)
		}
		if !strings.Contains(out, agent.Spec.SystemPrompt) {
			t.Fatalf("a worker keeps the agent's persona:\n%s", out)
		}
		if !strings.Contains(out, "be brief") {
			t.Fatalf("parent instructions must reach the worker:\n%s", out)
		}
		if !strings.Contains(out, "the big question") {
			t.Fatalf("the parent task should orient the worker:\n%s", out)
		}
		if !strings.Contains(out, "check one fact") {
			t.Fatalf("the sub-task itself is missing:\n%s", out)
		}
	})
}

func TestFinishRunPersistsOutputAndSources(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "a"}
	now := time.Now().UTC()

	if err := s.store.SaveRun(ctx, scope, store.Run{
		ID: "r1", AgentName: "a", Phase: store.RunPhaseRunning, Input: "q", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	s.finishRun(ctx, scope, "r1", runOutcome{
		Phase:   store.RunPhaseSucceeded,
		Usage:   engine.Usage{InputTokens: 10, OutputTokens: 20},
		Output:  "the answer",
		Sources: []string{"https://a.example/x"},
	}, now)

	got, err := s.store.GetRun(ctx, scope, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "the answer" {
		t.Fatalf("output = %q, want the run's answer on the record", got.Output)
	}
	if !slices.Equal(got.Sources, []string{"https://a.example/x"}) {
		t.Fatalf("sources = %v", got.Sources)
	}
	if got.Phase != store.RunPhaseSucceeded {
		t.Fatalf("phase = %q", got.Phase)
	}

	t.Run("a failure records the reason and no output", func(t *testing.T) {
		if err := s.store.SaveRun(ctx, scope, store.Run{
			ID: "r2", AgentName: "a", Phase: store.RunPhaseRunning, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		s.finishRun(ctx, scope, "r2", runOutcome{Phase: store.RunPhaseFailed, Message: "model unavailable"}, now)
		got, err := s.store.GetRun(ctx, scope, "r2")
		if err != nil {
			t.Fatal(err)
		}
		if got.Output != "" {
			t.Fatalf("a failed run should carry no answer, got %q", got.Output)
		}
		if got.Message != "model unavailable" {
			t.Fatalf("message = %q", got.Message)
		}
	})
}

// The capability has to carry its own instructions. Measured behaviour without
// this: an agent holding spawn + web_search with an empty system prompt ran
// sixteen sequential searches and never called spawn — so a grant that depends on
// the operator also pasting a recipe is not a working feature.
func TestFanOutGuidanceComesWithTheGrant(t *testing.T) {
	ctx := context.Background()
	s := &Server{store: store.NewMemoryStore()}
	scope := store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "researcher"}
	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "researcher"}}
	// Deliberately EMPTY system prompt: the whole point is that none is needed.
	run := taskRun{Scope: scope, Agent: agent, Task: "do the research", Trigger: agentsv1alpha1.RunTriggerChannel}

	joined := func(msgs []engine.Message) string {
		var b strings.Builder
		for _, m := range msgs {
			b.WriteString(m.Role + ": " + m.Content + "\n")
		}
		return b.String()
	}

	t.Run("granted: the mechanics are injected", func(t *testing.T) {
		out := joined(s.assembleTurnCtx(ctx, run, "chat", "", true))
		for _, want := range []string{"join ONCE", "do NOT investigate them one after another", "stand alone"} {
			if !strings.Contains(out, want) {
				t.Fatalf("guidance missing %q:\n%s", want, out)
			}
		}
		// It must also say when NOT to, or every trivial question becomes a fan-out.
		if !strings.Contains(out, "single narrow question") {
			t.Fatalf("guidance should bound itself:\n%s", out)
		}
	})

	t.Run("not granted: nothing is injected", func(t *testing.T) {
		out := joined(s.assembleTurnCtx(ctx, run, "chat", "", false))
		if strings.Contains(out, "join ONCE") {
			t.Fatal("an agent without the grant must not be told to fan out")
		}
	})

	t.Run("a worker is never told to fan out", func(t *testing.T) {
		// A depth-limited worker has no spawn tool; telling it to spawn would have
		// it call a tool it does not have.
		w := run
		w.Worker = &workerRun{Depth: 2, MaxToolTurns: 4}
		out := joined(s.assembleTurnCtx(ctx, w, "chat", "", false))
		if strings.Contains(out, "join ONCE") {
			t.Fatal("worker context must not carry fan-out mechanics")
		}
	})

	t.Run("the agent's own persona still leads", func(t *testing.T) {
		withPersona := run
		a := *agent
		a.Spec.SystemPrompt = "You are Bob, terse and sceptical."
		withPersona.Agent = &a
		out := joined(s.assembleTurnCtx(ctx, withPersona, "chat", "", true))
		if strings.Index(out, "You are Bob") > strings.Index(out, "join ONCE") {
			t.Fatal("the agent's persona should come before provider mechanics")
		}
	})
}
