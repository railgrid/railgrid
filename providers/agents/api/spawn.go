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
	"time"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tools"
)

// Spawn is fan-out inside one run: the agent decomposes its task, starts a
// worker per independent piece, and joins the answers. A worker is the SAME
// agent — same identity, same workspace, same budget — on a fresh context with
// a narrowed toolset, recorded as a child run (ParentRunID) so the run tree in
// the portal is the research trace.
//
// It sits ABOVE the engine deliberately. The engine's tool loop stays strictly
// serial (parallelizing it would reorder checkpoints, interleave approval
// interrupts, and scramble transcript order); concurrency lives here instead,
// in child runs the parent starts cheaply and collects with one blocking call.
//
// Distinct from delegation (tools/core.go "delegate"), which hands work to a
// *different* pre-configured Agent CR: spawn needs no allow-list because a
// worker's toolset is a SUBSET of what the calling run already holds, so it can
// never reach anything the parent could not.
const (
	// defaultMaxSpawnsPerRun / maxMaxSpawnsPerRun bound total workers per run.
	defaultMaxSpawnsPerRun = 10
	maxMaxSpawnsPerRun     = 20
	// defaultMaxConcurrentSpawns / maxMaxConcurrentSpawns bound simultaneous
	// workers; the rest queue on a semaphore.
	defaultMaxConcurrentSpawns = 4
	maxMaxConcurrentSpawns     = 8
	// A worker's own tool-call loop is shorter than a top-level run's: it works
	// one sub-task, and an unbounded worker is how a fan-out becomes a bill.
	defaultSpawnToolTurns = 8
	maxSpawnToolTurns     = 16
	// maxSpawnDepth is how deep the tree may go. A depth-2 worker exists but
	// gets no spawn tool.
	maxSpawnDepth = 2
	// spawnResultClip bounds one worker's answer as fed back to the parent.
	// Larger than the 1.5 KiB tool-observation replay clip because synthesis
	// needs the material; the full text stays on the child run record.
	spawnResultClip = 8 * 1024
	// Join wait bounds.
	defaultJoinTimeout = 300 * time.Second
	maxJoinTimeout     = 900 * time.Second
)

// workerDefaultFamilies is what a worker gets when the model names no families:
// reading the web, which is what the overwhelming majority of sub-tasks need.
var workerDefaultFamilies = []string{"web"}

// workerExcludedTools are core tools a worker never gets. They are all about
// the agent managing itself or talking to a human — a worker does neither: it
// answers one question and returns. Excluding them keeps a fan-out from
// spamming the user's channel, filling the inbox with questions nobody can
// answer in time, or having ten workers each write memory notes and reschedule
// the agent.
var workerExcludedTools = map[string]bool{
	"notify": true, "ask": true, "memory_save": true,
	"schedule_create": true, "schedule_update": true, "schedule_delete": true, "schedules_list": true,
	"delegate": true,
}

// workerRun marks a run as a spawned worker and carries the constraints its
// parent imposed. Nil on every other kind of run.
type workerRun struct {
	// Depth is 1 for a worker spawned by a top-level run.
	Depth int
	// Instructions is extra guidance from the parent, folded into the system
	// context under the worker preamble.
	Instructions string
	// ParentTask is the larger task the worker's piece serves, quoted in the
	// preamble as orientation only. A worker that knows nothing about the whole
	// tends to answer a subtly different question than the one that was needed.
	ParentTask string
	// Families is the worker's effective tool grant, already intersected with
	// the parent's own.
	Families []string
	// ClassTrigger is the parent's trigger. The worker inherits its approval
	// class, so a worker of an interactive run is gated like an interactive run
	// rather than picking up the background grant's rules.
	ClassTrigger string
	// MaxToolTurns bounds the worker's tool loop.
	MaxToolTurns int
}

// spawnTask is one worker's slot: identity, lifecycle, and result. done is
// closed exactly once, by the goroutine that ran it; readers only touch the
// result fields after done is closed or under mu.
type spawnTask struct {
	id    string
	task  string
	runID string
	done  chan struct{}

	mu       sync.Mutex
	phase    store.RunPhase
	result   string
	sources  []string
	err      error
	pending  bool // stopped on an approval gate
	started  time.Time
	finished time.Time
	// collected marks that join has already reported this task, so a bare
	// join (no ids) collects only what is outstanding.
	collected bool
}

// spawnSnapshot is a lock-free copy of a task's state for formatting.
type spawnSnapshot struct {
	id       string
	task     string
	runID    string
	phase    store.RunPhase
	result   string
	sources  []string
	err      error
	pending  bool
	started  time.Time
	finished time.Time
	done     bool
}

// snapshot copies the task's state under its lock. done is passed in because it
// is derived from the channel, not the guarded fields.
func (t *spawnTask) snapshot(done bool) spawnSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return spawnSnapshot{
		id: t.id, task: t.task, runID: t.runID, phase: t.phase, result: t.result,
		sources: t.sources, err: t.err, pending: t.pending,
		started: t.started, finished: t.finished, done: done,
	}
}

// spawnCoordinator owns one run's fan-out: the limits, the semaphore, and the
// task table join reads from. One per run, built in buildToolset.
type spawnCoordinator struct {
	// exec runs one worker. Always s.executeTask in production; the seam exists
	// so the fan-out logic (limits, semaphore, join) is testable without a model.
	exec func(context.Context, taskRun) (runResult, error)
	// runCtx is the PARENT RUN's context, not the tool call's. Workers outlive
	// the spawn call that started them but not the run that owns them:
	// cancelling the parent (or its timeout firing) cancels every worker.
	runCtx   context.Context
	parent   taskRun
	depth    int
	families []string

	maxPerRun     int
	maxConcurrent int
	sem           chan struct{}

	mu    sync.Mutex
	tasks map[string]*spawnTask
	order []string
	wg    sync.WaitGroup
}

// spawnPolicyFor resolves this run's spawn envelope from the agent's limits.
func spawnPolicyFor(agent *agentsv1alpha1.Agent, families []string) tools.SpawnPolicy {
	maxPerRun := defaultMaxSpawnsPerRun
	if v := int(agent.Spec.Limits.MaxSpawnsPerRun); v > 0 {
		maxPerRun = min(v, maxMaxSpawnsPerRun)
	}
	maxConcurrent := defaultMaxConcurrentSpawns
	if v := int(agent.Spec.Limits.MaxConcurrentSpawns); v > 0 {
		maxConcurrent = min(v, maxMaxConcurrentSpawns)
	}
	return tools.SpawnPolicy{
		Families:         families,
		DefaultFamilies:  workerFamilies(nil, families),
		MaxPerRun:        maxPerRun,
		MaxConcurrent:    maxConcurrent,
		DefaultToolTurns: defaultSpawnToolTurns,
		MaxToolTurns:     maxSpawnToolTurns,
	}
}

// spawnDepth reports how deep the current run sits in the spawn tree.
func spawnDepth(run taskRun) int {
	if run.Worker == nil {
		return 0
	}
	return run.Worker.Depth
}

// grantableWorkerFamilies narrows the calling run's effective families to those
// a worker may be granted. "edges" is dropped: it authenticates as the calling
// human through the hub's aggregate MCP endpoint, and a worker is not the human
// — the same reason delegation does not inherit it.
func grantableWorkerFamilies(parentFamilies []string) []string {
	out := make([]string, 0, len(parentFamilies))
	for _, f := range parentFamilies {
		if f == "edges" {
			continue
		}
		out = append(out, f)
	}
	return out
}

// workerFamilies resolves a worker's grant: what it asked for, intersected with
// what the parent may pass on. An empty request means the default; a request
// that survives the intersection empty falls back to core only (which the
// toolset always includes), so an over-reaching worker is narrowed rather than
// failed — it just has less to work with and will say so.
func workerFamilies(requested, grantable []string) []string {
	if len(requested) == 0 {
		requested = workerDefaultFamilies
	}
	out := []string{"core"}
	for _, f := range requested {
		f = strings.TrimSpace(f)
		if f == "" || f == "core" || slices.Contains(out, f) {
			continue
		}
		if !slices.Contains(grantable, f) {
			continue // silently narrowed: a worker never widens the parent's grant
		}
		out = append(out, f)
	}
	return out
}

func clampSpawnToolTurns(v int) int {
	if v <= 0 {
		return defaultSpawnToolTurns
	}
	return min(v, maxSpawnToolTurns)
}

func clampJoinTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultJoinTimeout
	}
	return min(time.Duration(seconds)*time.Second, maxJoinTimeout)
}

// spawn registers a worker and starts it, returning its task id immediately.
// The worker runs concurrently (bounded by the semaphore); join collects it.
func (c *spawnCoordinator) spawn(_ context.Context, req tools.SpawnRequest) (string, error) {
	task := strings.TrimSpace(req.Task)
	if task == "" {
		return "", fmt.Errorf("task is required")
	}

	c.mu.Lock()
	if len(c.order) >= c.maxPerRun {
		c.mu.Unlock()
		return "", fmt.Errorf("spawn limit reached (%d workers per run) — join the workers you already started and answer from those findings", c.maxPerRun)
	}
	st := &spawnTask{
		id:    fmt.Sprintf("t%d", len(c.order)+1),
		task:  task,
		done:  make(chan struct{}),
		phase: store.RunPhasePending,
	}
	c.tasks[st.id] = st
	c.order = append(c.order, st.id)
	c.mu.Unlock()

	families := workerFamilies(req.Families, c.families)
	worker := &workerRun{
		Depth:        c.depth + 1,
		Instructions: strings.TrimSpace(req.Instructions),
		ParentTask:   c.parent.Task,
		Families:     families,
		ClassTrigger: c.parent.Trigger,
		MaxToolTurns: clampSpawnToolTurns(req.MaxToolTurns),
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(st.done)

		// Queue for a slot. A parent cancelled while workers are still queued
		// never starts them.
		select {
		case c.sem <- struct{}{}:
		case <-c.runCtx.Done():
			st.fail(store.RunPhaseAborted, fmt.Errorf("cancelled before starting: %w", c.runCtx.Err()))
			return
		}
		defer func() { <-c.sem }()
		// Re-check after acquiring: when the parent is cancelled at the same
		// moment a slot frees up, both select cases are ready and the choice is
		// random. Without this a cancelled run could still start work.
		if err := c.runCtx.Err(); err != nil {
			st.fail(store.RunPhaseAborted, fmt.Errorf("cancelled before starting: %w", err))
			return
		}

		st.mu.Lock()
		st.phase, st.started = store.RunPhaseRunning, time.Now().UTC()
		st.mu.Unlock()

		res, err := c.exec(c.runCtx, taskRun{
			Creds: c.parent.Creds,
			CR:    c.parent.CR,
			// Same scope as the parent — same agent, same workspace. Usage
			// therefore lands in the parent's own budget bucket with no explicit
			// rollup (unlike delegation, whose child is a different agent);
			// adding one here would double-count.
			Scope:     c.parent.Scope,
			Agent:     c.parent.Agent,
			SessionID: "spawn:" + c.parent.RunID + ":" + st.id,
			Task:      task,
			Trigger:   agentsv1alpha1.RunTriggerSpawn,
			// SourceName attributes the worker to the run that started it, which
			// is what the portal shows as the trigger source.
			SourceName:  c.parent.Agent.Name,
			ParentRunID: c.parent.RunID,
			// The worker acts as the same caller for instance-backed tools. Edges
			// is deliberately not inherited (see grantableWorkerFamilies).
			ClusterID: c.parent.ClusterID,
			HubToken:  c.parent.HubToken,
			Worker:    worker,
		})
		if err != nil {
			phase := store.RunPhaseFailed
			if c.runCtx.Err() != nil {
				phase = store.RunPhaseAborted
			}
			st.fail(phase, err)
			return
		}
		st.succeed(res)
	}()

	return st.id, nil
}

func (t *spawnTask) fail(phase store.RunPhase, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.phase, t.err, t.finished = phase, err, time.Now().UTC()
}

func (t *spawnTask) succeed(res runResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.runID, t.finished = res.RunID, time.Now().UTC()
	body, sources := splitSources(res.Content)
	t.result, t.sources = body, sources
	if res.Pending != nil {
		// A worker cannot pause for a human: nobody is waiting on its inbox item
		// and the parent is blocked in join. Report it as not attempted so the
		// parent can decide — do it itself (where the approval flow exists) or
		// work around it — rather than treating a partial answer as final.
		t.phase, t.pending = store.RunPhaseAborted, true
		return
	}
	t.phase = store.RunPhaseSucceeded
}

// join waits for the named workers (or every outstanding one) and formats their
// answers for the parent's context.
func (c *spawnCoordinator) join(ctx context.Context, ids []string, timeoutSeconds int) (string, error) {
	targets, err := c.resolve(ids)
	if err != nil {
		return "", err
	}
	if len(targets) == 0 {
		return "no workers to collect — call spawn first, or you have already collected every worker you started.", nil
	}

	deadline := time.Now().Add(clampJoinTimeout(timeoutSeconds))
	for _, st := range targets {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		timer := time.NewTimer(remaining)
		select {
		case <-st.done:
			timer.Stop()
		case <-timer.C:
			// Out of time for the rest as well; report what we have. The
			// stragglers keep running and a later join collects them.
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		}
	}

	var b strings.Builder
	outstanding := 0
	for _, st := range targets {
		done := isDone(st)
		if done {
			st.mu.Lock()
			st.collected = true
			st.mu.Unlock()
		} else {
			outstanding++
		}
		writeJoinResult(&b, st.snapshot(done))
	}
	if outstanding > 0 {
		fmt.Fprintf(&b, "\n%d worker(s) are still running. Continue with what you have, or call join again to collect them.\n", outstanding)
	}
	return strings.TrimSpace(b.String()), nil
}

func isDone(st *spawnTask) bool {
	select {
	case <-st.done:
		return true
	default:
		return false
	}
}

// writeJoinResult renders one worker's outcome. Every branch names what
// happened, so the model never has to guess whether an empty result means
// "nothing found" or "it broke".
func writeJoinResult(b *strings.Builder, snap spawnSnapshot) {
	fmt.Fprintf(b, "── worker %s ─ %s\n", snap.id, clipLine(snap.task, 120))
	if !snap.done {
		fmt.Fprintf(b, "still running (started %s ago); not collected yet\n\n", durationLabel(time.Since(snap.started)))
		return
	}
	switch {
	case snap.pending:
		fmt.Fprintf(b, "not attempted: this sub-task needed approval for a tool, which a worker cannot wait for. Do it yourself if you need it.\n\n")
	case snap.err != nil:
		fmt.Fprintf(b, "failed: %s\n\n", clipLine(snap.err.Error(), 500))
	default:
		fmt.Fprintf(b, "completed in %s\n%s\n", durationLabel(snap.finished.Sub(snap.started)), safeTruncate(snap.result, spawnResultClip))
		if len(snap.sources) > 0 {
			b.WriteString("sources:\n")
			for _, src := range snap.sources {
				fmt.Fprintf(b, "- %s\n", src)
			}
		}
		b.WriteString("\n")
	}
}

// resolve maps requested ids to tasks. No ids means every worker not yet
// collected — the common case, and it saves the model from tracking ids.
func (c *spawnCoordinator) resolve(ids []string) ([]*spawnTask, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(ids) == 0 {
		var out []*spawnTask
		for _, id := range c.order {
			st := c.tasks[id]
			st.mu.Lock()
			collected := st.collected
			st.mu.Unlock()
			if !collected {
				out = append(out, st)
			}
		}
		return out, nil
	}
	var out []*spawnTask
	for _, id := range ids {
		id = strings.TrimSpace(id)
		st, ok := c.tasks[id]
		if !ok {
			return nil, fmt.Errorf("no worker %q in this run — use the task ids spawn returned, or call join with no ids to collect them all", id)
		}
		if !slices.Contains(out, st) {
			out = append(out, st)
		}
	}
	return out, nil
}

// wait blocks until every worker has stopped. Called when the parent run ends
// so a finished run leaves no worker writing to its tree — and so the run's
// usage is complete when the budget is next checked.
func (c *spawnCoordinator) wait() { c.wg.Wait() }

// sourcesHeading is the marker the worker preamble asks for.
const sourcesHeading = "sources:"

// splitSources separates a trailing "Sources:" block from the body. Workers are
// asked for it, so parse it into structure rather than leaving the parent to
// re-read prose. A missing or malformed block is not an error: the body is
// returned whole.
func splitSources(content string) (body string, sources []string) {
	lines := strings.Split(content, "\n")
	// Find the LAST heading: a worker may quote the word earlier in its answer.
	idx := -1
	for i, l := range lines {
		if strings.EqualFold(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l), "**")), sourcesHeading) ||
			strings.EqualFold(strings.TrimSpace(l), "**"+sourcesHeading+"**") {
			idx = i
		}
	}
	if idx < 0 {
		return strings.TrimSpace(content), nil
	}
	for _, l := range lines[idx+1:] {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "-")
		l = strings.TrimPrefix(l, "*")
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// Keep only what looks like a locator; a worker sometimes trails prose
		// after the list.
		if !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") {
			continue
		}
		if fields := strings.Fields(l); len(fields) > 0 {
			l = fields[0]
		}
		if !slices.Contains(sources, l) {
			sources = append(sources, l)
		}
	}
	return strings.TrimSpace(strings.Join(lines[:idx], "\n")), sources
}

// clipLine collapses whitespace and bounds a label to one readable line.
func clipLine(s string, n int) string {
	return safeTruncate(strings.Join(strings.Fields(s), " "), n)
}

func durationLabel(d time.Duration) string {
	if d < time.Second {
		return "under a second"
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// fanOutGuidance is injected into any run whose toolset actually contains spawn.
//
// It lives here rather than in the user's system prompt because a capability that
// only works when the operator separately pastes instructions is not a
// capability. Granting fan-out has to be enough to get fan-out. Measured
// behaviour without this: an agent holding spawn, web_search and a vague request
// ran sixteen sequential searches and never called spawn once — the tool
// description alone does not change what a model reaches for.
//
// The split is deliberate: this supplies the MECHANICS (when to fan out, the
// spawn-all-then-join-once ordering, self-contained tasks), while the agent's own
// system prompt stays about WHO it is and what standards it holds. Users should
// never have to write the mechanics; they cannot be expected to know that joining
// after each spawn silently serializes the whole thing.
const fanOutGuidance = `You can work several things at once, and you should when the request allows it.

When a request has independent parts — different topics, competitors, regions, time periods, options to compare — do NOT investigate them one after another yourself. Instead:

1. Split it into 3-6 parts that do not depend on each other's answers. If one genuinely depends on another, do that one yourself first.
2. Call spawn once per part, all of them, before collecting anything. Each task must stand alone: the worker cannot see this conversation, so restate every name, date, version and constraint it needs.
3. Then call join ONCE. Calling join after each spawn makes the work sequential and defeats the point.
4. Read what comes back critically. Where two workers disagree, or a load-bearing claim is thinly sourced, spawn a short second wave to check just that.
5. Answer in your own voice using their findings and sources, and say plainly what the evidence does not cover.

For a single narrow question, just answer it — a fan-out you do not need is slower than doing the work.

The judgement is yours: a worker reports findings, it does not decide.`

// workerPreamble is the fixed system guidance every worker gets, above the
// parent's persona. It sets the contract the join formatting depends on: the
// final message IS the return value, and sources are listed at the end.
func workerPreamble(agentName, parentTask string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are a scoped worker started by %s to complete ONE sub-task, given below. You are not in a conversation.\n\n", agentName)
	b.WriteString("How your reply is used: your final message is returned verbatim to the agent that started you, as data. " +
		"Do not address a human, do not ask questions (nobody will answer), do not offer follow-ups, and do not describe what you are about to do — just report what you found.\n\n")
	b.WriteString("You have no memory of, and no access to, the conversation that started you: everything you need is in the sub-task. " +
		"If the sub-task is missing something essential, say exactly what is missing instead of guessing.\n\n")
	b.WriteString("Be specific and dense: facts, figures, names, dates, quotes. Prefer primary sources. " +
		"Separate what you verified from what you inferred, and state plainly what you could not determine — a confident wrong answer is worse than an honest gap.\n\n")
	b.WriteString("End your reply with a line containing only \"Sources:\" followed by one URL per line for every source you relied on. Omit that section only if you used no external sources.")
	if strings.TrimSpace(parentTask) != "" {
		fmt.Fprintf(&b, "\n\nFor context, the larger task this serves (do NOT work on it — only your sub-task): %s", clipLine(parentTask, 500))
	}
	return b.String()
}
