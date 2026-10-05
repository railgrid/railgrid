// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/dispatch"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/llm"
)

// ---- a runner that answers from a script ------------------------------------

// fakeRunner is one attempt's worth of scripted protocol. It records what was
// dispatched, which is where the epoch and session assertions look.
type fakeRunner struct {
	mu sync.Mutex

	// starts, resumes and cancels are what the backend sent, in order.
	starts  []runner.StartRequest
	resumes []runner.ResumeRequest
	cancels []runner.CancelRequest

	// receipt is what every call answers with, and phases (when non-empty) is
	// the sequence Inspect walks through, one per call.
	receipt runner.Receipt
	phases  []runner.Phase
	// inspectReceipts and resumeReceipt let recovery tests script transitions
	// that happen while an answer is in flight.
	inspectReceipts []runner.Receipt
	resumeReceipt   *runner.Receipt

	// events is the stream the backend tails. It ends in io.EOF, which is how the
	// runner ends a quiet stream.
	events []runner.Event

	startErr         error
	eventsErrorAfter uint64
	inspects         int
}

func (f *fakeRunner) Start(_ context.Context, req runner.StartRequest) (runner.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, req)
	if f.startErr != nil {
		return runner.Receipt{}, f.startErr
	}
	return f.current(), nil
}

func (f *fakeRunner) Resume(_ context.Context, req runner.ResumeRequest) (runner.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes = append(f.resumes, req)
	if f.resumeReceipt != nil {
		f.receipt = *f.resumeReceipt
	}
	return f.current(), nil
}

func (f *fakeRunner) Cancel(_ context.Context, req runner.CancelRequest) (runner.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, req)
	return f.current(), nil
}

func (f *fakeRunner) Inspect(_ context.Context, _ string) (runner.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspects++
	// Walk the scripted phases, one per Inspect, holding the last.
	if len(f.phases) > 0 {
		f.receipt.Phase = f.phases[0]
		if len(f.phases) > 1 {
			f.phases = f.phases[1:]
		}
	}
	if len(f.inspectReceipts) > 0 {
		f.receipt = f.inspectReceipts[0]
		f.inspectReceipts = f.inspectReceipts[1:]
	}
	return f.current(), nil
}

func (f *fakeRunner) current() runner.Receipt {
	r := f.receipt
	if len(f.events) > 0 {
		r.Cursor = f.events[len(f.events)-1].Cursor
	}
	return r
}

func (f *fakeRunner) Events(_ context.Context, _ string, after uint64) (dispatch.Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []runner.Event
	for _, e := range f.events {
		if e.Cursor > after && (f.eventsErrorAfter == 0 || e.Cursor <= f.eventsErrorAfter) {
			out = append(out, e)
		}
	}
	if f.eventsErrorAfter > after {
		return &failingSliceStream{sliceStream: sliceStream{events: out}, err: errors.New("simulated stream loss")}, nil
	}
	return &sliceStream{events: out}, nil
}

type sliceStream struct {
	events []runner.Event
	i      int
}

func (s *sliceStream) Next(ctx context.Context) (runner.Event, error) {
	if err := ctx.Err(); err != nil {
		return runner.Event{}, err
	}
	if s.i >= len(s.events) {
		return runner.Event{}, io.EOF
	}
	e := s.events[s.i]
	s.i++
	return e, nil
}

func (s *sliceStream) Close() error { return nil }

type failingSliceStream struct {
	sliceStream
	err error
}

func (s *failingSliceStream) Next(ctx context.Context) (runner.Event, error) {
	if s.i < len(s.events) {
		return s.sliceStream.Next(ctx)
	}
	return runner.Event{}, s.err
}

// ---- a sink that records ----------------------------------------------------

type recordingSink struct {
	deltas      []string
	toolStarts  []string
	toolEnds    []backend.ToolEvent
	assistants  []backend.AssistantMessage
	checkpoints []json.RawMessage
	abort       error
}

func (r *recordingSink) Delta(text string) { r.deltas = append(r.deltas, text) }
func (r *recordingSink) Assistant(msg backend.AssistantMessage) {
	r.assistants = append(r.assistants, msg)
}
func (r *recordingSink) ToolStart(id, name, _ string) {
	r.toolStarts = append(r.toolStarts, id+":"+name)
}
func (r *recordingSink) ToolEnd(ev backend.ToolEvent) { r.toolEnds = append(r.toolEnds, ev) }
func (r *recordingSink) Checkpoint(state json.RawMessage) {
	r.checkpoints = append(r.checkpoints, state)
}
func (r *recordingSink) Aborted(context.Context) error { return r.abort }

// ---- helpers ----------------------------------------------------------------

func event(cursor uint64, typ, message string, data string) runner.Event {
	e := runner.Event{Cursor: cursor, Type: typ, Message: message, AttemptEpoch: 1}
	if data != "" {
		e.Data = json.RawMessage(data)
	}
	return e
}

func testConfig(f *fakeRunner, epoch uint64, session string) Config {
	ids := 0
	return Config{
		Runner:          f,
		TaskID:          "agent-scout-chat",
		AttemptID:       "run-1",
		Epoch:           epoch,
		SessionID:       session,
		WorkspaceID:     "agent-scout-chat",
		RequiredHarness: llm.HarnessAdvertisedName(llm.ProviderClaudeCode),
		Credential:      llm.HarnessIdentity{Kind: "claude-oauth", Value: "sk-ant-oat-secret"},
		NewID: func() string {
			ids++
			return "req-" + string(rune('a'+ids-1))
		},
	}
}

func testRun() *backend.Run {
	return &backend.Run{ID: "run-1", SessionID: "chat", Agent: "scout", Trigger: "chat"}
}

func parkedState(t *testing.T, out backend.Outcome) State {
	t.Helper()
	if out.Parked == nil {
		t.Fatal("outcome is not parked")
	}
	var state State
	if err := json.Unmarshal(out.Parked.State, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// ---- the dispatch contract --------------------------------------------------

// Every field the runner refuses a dispatch without, and the two that are the
// whole point of a conversational turn: a WORKSPACE attempt (no repository, no
// commit, no git result) and maxTurns 1.
func TestStartRequestSatisfiesTheProtocolContract(t *testing.T) {
	f := &fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, SessionID: "sess-1"},
		events: []runner.Event{
			event(1, runner.EventProgress, "working", ""),
			event(2, runner.EventCompleted, "", ""),
		},
	}
	b := New(testConfig(f, 1, ""))
	sink := &recordingSink{}
	out, err := b.Turn(context.Background(), testRun(), backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "what changed?"}},
		// A toolset is supplied and must be ignored: the harness owns its tools.
		Tools: []backend.Tool{{Name: "web_search"}},
	}, sink)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if out.Status != backend.StatusCompleted {
		t.Fatalf("status = %s, want completed", out.Status)
	}
	if len(f.starts) != 1 {
		t.Fatalf("starts = %d, want 1", len(f.starts))
	}
	req := f.starts[0]
	switch {
	case req.WorkspaceID == "":
		t.Error("a conversational turn must be a workspace attempt")
	case req.RepositoryID != "" || req.BaseCommit != "" || req.Repository != nil:
		t.Error("a workspace attempt must not name a repository or a commit")
	case req.ExportGitResult:
		t.Error("a workspace attempt cannot export a Git result")
	case req.Limits.MaxTurns != 1:
		t.Errorf("limits.maxTurns = %d, want 1", req.Limits.MaxTurns)
	case req.HarnessCredential == nil || req.HarnessCredential.Value == "":
		t.Error("every dispatch carries a harness credential; a runner has no identity of its own")
	case req.RequiredHarness != "claude-code":
		t.Errorf("requiredHarness = %q, want the ADVERTISED name claude-code", req.RequiredHarness)
	case req.AttemptEpoch != 1:
		t.Errorf("attemptEpoch = %d, want 1", req.AttemptEpoch)
	}
	// approvedInput must be a non-empty object carrying provenance, or the runner
	// refuses the start outright.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(req.ApprovedInput, &envelope); err != nil || len(envelope["provenance"]) == 0 {
		t.Fatalf("approvedInput = %s, want a provenance envelope", req.ApprovedInput)
	}
	var provenance map[string]any
	if err := json.Unmarshal(envelope["provenance"], &provenance); err != nil {
		t.Fatal(err)
	}
	if provenance["agent"] != "scout" || provenance["runID"] != "run-1" || provenance["trigger"] != "chat" {
		t.Fatalf("provenance = %v, want the run's own", provenance)
	}
	// The instructions carry the conversation; the toolset does not appear.
	if !strings.Contains(req.Instructions, "what changed?") {
		t.Fatalf("instructions = %q", req.Instructions)
	}
	if strings.Contains(req.Instructions, "web_search") {
		t.Fatal("the provider's toolset must not be described to a harness that ignores it")
	}
}

// The chaining rule: turn 2 carries the session the RECEIPT reported (a harness
// may fork on resume, so what was sent is not authoritative) and an epoch that
// advanced — the runner refuses a start that does not.
func TestConsecutiveTurnsChainOntoTheReceiptsSession(t *testing.T) {
	first := &fakeRunner{
		// Sent nothing; the receipt comes back naming a session the harness
		// created — and a DIFFERENT one from anything the caller could know.
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, SessionID: "sess-forked"},
		events:  []runner.Event{event(1, runner.EventCompleted, "", "")},
	}
	b1 := New(testConfig(first, 1, ""))
	if _, err := b1.Turn(context.Background(), testRun(), backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "turn one"}},
	}, &recordingSink{}); err != nil {
		t.Fatalf("turn one: %v", err)
	}
	if first.starts[0].SessionID != "" {
		t.Fatalf("the first turn of a conversation continues nothing, got sessionID %q", first.starts[0].SessionID)
	}
	observed := b1.Observed()
	if observed.SessionID != "sess-forked" {
		t.Fatalf("observed session = %q, want the receipt's sess-forked", observed.SessionID)
	}

	// Turn two: the provider chains onto what came back, with the next epoch.
	second := &fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-2", AttemptEpoch: 2, Phase: runner.PhaseCompleted, SessionID: "sess-forked"},
		events:  []runner.Event{event(1, runner.EventCompleted, "", "")},
	}
	cfg := testConfig(second, 2, observed.SessionID)
	cfg.AttemptID = "run-2"
	b2 := New(cfg)
	if _, err := b2.Turn(context.Background(), &backend.Run{ID: "run-2", SessionID: "chat", Agent: "scout", Trigger: "chat"},
		backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "turn two"}}}, &recordingSink{}); err != nil {
		t.Fatalf("turn two: %v", err)
	}
	req := second.starts[0]
	if req.SessionID != "sess-forked" {
		t.Fatalf("turn two sessionID = %q, want sess-forked — consecutive turns are one conversation", req.SessionID)
	}
	if req.TaskID != first.starts[0].TaskID {
		t.Fatalf("turn two taskID = %q, want the same task %q", req.TaskID, first.starts[0].TaskID)
	}
	if req.AttemptEpoch != 2 {
		t.Fatalf("turn two epoch = %d, want 2; an epoch that does not advance is refused with stale_attempt", req.AttemptEpoch)
	}
	if req.AttemptID == first.starts[0].AttemptID {
		t.Fatal("each turn is its own attempt (run → attemptID)")
	}
}

// needs_input parks the turn, carrying the QUESTION rather than a gated call —
// nothing was gated — and resume state that can answer it.
func TestNeedsInputParksWithTheQuestion(t *testing.T) {
	f := &fakeRunner{
		receipt: runner.Receipt{
			AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
			Blocker:       "Claude Code asked a product question",
			Clarification: &runner.Clarification{ID: "clar-7", Text: "Should I bump the minor or the patch version?"},
		},
		events: []runner.Event{
			event(1, runner.EventProgress, "reading the changelog", ""),
			event(2, runner.EventNeedsInput, "Claude Code asked a product question", ""),
		},
	}
	b := New(testConfig(f, 1, ""))
	out, err := b.Turn(context.Background(), testRun(), backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "cut a release"}},
	}, &recordingSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if out.Status != backend.StatusParked || out.Parked == nil {
		t.Fatalf("status = %s, parked = %v; want a parked turn", out.Status, out.Parked)
	}
	if out.Parked.Tool != "" || out.Parked.Args != "" {
		t.Fatalf("a harness question gated no call, got tool=%q args=%q", out.Parked.Tool, out.Parked.Args)
	}
	if !strings.Contains(out.Parked.Question, "minor or the patch") {
		t.Fatalf("question = %q, want the clarification text", out.Parked.Question)
	}
	// The park carries no RequestID: the provider files the inbox item, because
	// reaching the store is its side of the seam.
	if out.Parked.RequestID != "" {
		t.Fatalf("RequestID = %q; a harness park leaves it to the provider", out.Parked.RequestID)
	}
	var state State
	if err := json.Unmarshal(out.Parked.State, &state); err != nil {
		t.Fatal(err)
	}
	if state.AttemptID != "run-1" || state.ClarificationID != "clar-7" || state.SessionID != "sess-1" {
		t.Fatalf("resume state = %+v, want the coordinates the answer is sent to", state)
	}
	if state.ParkType != parkTypeQuestion || len(state.Snapshot) == 0 {
		t.Fatalf("resume state park/snapshot = %q / %s, want a question and normalized snapshot", state.ParkType, state.Snapshot)
	}

	// Answering it resumes THAT question, in that session, at that epoch.
	answered := &fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
			Clarification: &runner.Clarification{ID: "clar-7", Text: "Should I bump the minor or the patch version?"}},
		phases: []runner.Phase{runner.PhaseNeedsInput, runner.PhaseCompleted},
		events: []runner.Event{event(1, runner.EventProgress, "ok", ""), event(2, runner.EventCompleted, "", "")},
	}
	rb := New(testConfig(answered, 1, "sess-1"))
	if _, err := rb.Continue(context.Background(), testRun(), backend.Answer{
		State: out.Parked.State, Decided: true, Approved: true, Note: "the patch version",
	}, &recordingSink{}); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if len(answered.resumes) != 1 {
		t.Fatalf("resumes = %d, want 1", len(answered.resumes))
	}
	resume := answered.resumes[0]
	if resume.ClarificationID != "clar-7" {
		t.Fatalf("resume clarificationID = %q, want clar-7", resume.ClarificationID)
	}
	if resume.Resolution != "the patch version" {
		t.Fatalf("resume resolution = %q, want the user's answer", resume.Resolution)
	}
	if resume.AttemptEpoch != 1 {
		t.Fatalf("resume epoch = %d; a resume addresses the SAME attempt, so it keeps its epoch", resume.AttemptEpoch)
	}
	if resume.HarnessCredential == nil {
		t.Fatal("a resume carries the credential again: it may arrive after a runner restart")
	}
}

func TestParkedStateCarriesAgentIncarnation(t *testing.T) {
	f := &fakeRunner{
		receipt: runner.Receipt{
			AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
			Clarification: &runner.Clarification{ID: "clar-1", Text: "Continue?"},
		},
		events: []runner.Event{event(1, runner.EventNeedsInput, "Continue?", "")},
	}
	cfg := testConfig(f, 1, "")
	cfg.AgentUID = "agent-uid-1"
	out, err := New(cfg).Turn(context.Background(), testRun(), backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "do the task"}},
	}, &recordingSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if out.Parked == nil {
		t.Fatalf("outcome = %+v, want a parked turn", out)
	}
	var state State
	if err := json.Unmarshal(out.Parked.State, &state); err != nil {
		t.Fatal(err)
	}
	if state.AgentUID != "agent-uid-1" {
		t.Fatalf("parked AgentUID = %q, want agent-uid-1", state.AgentUID)
	}
}

// The rule the task states plainly: until the terminal receipt is observed the
// attempt is Cancelling, not Cancelled, and Cancel must not claim otherwise.
func TestCancelDoesNotReportCancelledUntilObserved(t *testing.T) {
	stuck := &fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCancelling},
		phases:  []runner.Phase{runner.PhaseCancelling},
	}
	b := New(testConfig(stuck, 1, ""))
	// Give it a bound the way the provider does; the fake never reaches a
	// terminal phase, so the bound is what ends the wait.
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	err := b.Cancel(ctx, testRun())
	if err == nil {
		t.Fatal("Cancel returned nil without ever seeing a terminal receipt")
	}
	if !strings.Contains(err.Error(), "not cancelled") {
		t.Fatalf("error %v does not say the attempt is still cancelling", err)
	}
	if len(stuck.cancels) != 1 {
		t.Fatalf("cancels = %d, want the cancel to have been posted once", len(stuck.cancels))
	}
	if stuck.inspects == 0 {
		t.Fatal("Cancel must go and look, not assume")
	}

	// A runner that does reach the terminal phase answers nil.
	settles := &fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCancelling},
		phases:  []runner.Phase{runner.PhaseCancelled},
	}
	sb := New(testConfig(settles, 1, ""))
	if err := sb.Cancel(context.Background(), testRun()); err != nil {
		t.Fatalf("Cancel on a runner that settled: %v", err)
	}
}

// Usage: Claude Code is told what it was charged and the cost is written
// through; Codex reports tokens only, and the cost stays ZERO rather than being
// invented from a guessed rate.
func TestUsageComesFromWhatTheHarnessReported(t *testing.T) {
	t.Run("a harness that reports cost", func(t *testing.T) {
		f := &fakeRunner{
			receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted},
			events: []runner.Event{
				event(1, runner.EventProgress, "done", `{"type":"result","result":"the answer","total_cost_usd":0.1234,"usage":{"input_tokens":1000,"output_tokens":200,"cache_read_input_tokens":50}}`),
				event(2, runner.EventCompleted, "", ""),
			},
		}
		out, err := New(testConfig(f, 1, "")).Turn(context.Background(), testRun(),
			backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "hi"}}}, &recordingSink{})
		if err != nil {
			t.Fatal(err)
		}
		if out.Usage.Total.CostMicros != 123400 {
			t.Fatalf("cost = %d micros, want 123400", out.Usage.Total.CostMicros)
		}
		if out.Usage.Total.InputTokens != 1050 || out.Usage.Total.OutputTokens != 200 {
			t.Fatalf("tokens = %+v, want cache reads counted as input", out.Usage.Total.Tokens)
		}
		// Fresh turn: Total and Billed are the same thing.
		if out.Usage.Billed != out.Usage.Total {
			t.Fatalf("billed %+v != total %+v on a fresh turn", out.Usage.Billed, out.Usage.Total)
		}
		if out.Final != "the answer" {
			t.Fatalf("final = %q, want the result record's text", out.Final)
		}
	})

	t.Run("a harness that reports tokens only", func(t *testing.T) {
		f := &fakeRunner{
			receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted},
			events: []runner.Event{
				event(1, runner.EventProgress, "the answer", `{"delta":"the answer"}`),
				event(2, runner.EventProgress, "completed", `{"turn":{"id":"t1","status":"completed","usage":{"inputTokens":500,"outputTokens":60}}}`),
				event(3, runner.EventCompleted, "", ""),
			},
		}
		out, err := New(testConfig(f, 1, "")).Turn(context.Background(), testRun(),
			backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "hi"}}}, &recordingSink{})
		if err != nil {
			t.Fatal(err)
		}
		if out.Usage.Total.InputTokens != 500 || out.Usage.Total.OutputTokens != 60 {
			t.Fatalf("tokens = %+v, want what was reported", out.Usage.Total.Tokens)
		}
		if out.Usage.Total.CostMicros != 0 {
			t.Fatalf("cost = %d, want zero: no cost was reported and none may be invented", out.Usage.Total.CostMicros)
		}
		if !strings.Contains(out.Text, "the answer") {
			t.Fatalf("text = %q, want the streamed prose", out.Text)
		}
	})
}

// Progress becomes a Delta, and a payload that NAMES a tool item becomes a tool
// call. See turnState.observe for why the first is all the wire allows for Claude
// Code.
func TestProgressMapsOntoTheSinkAsThePayloadsAllow(t *testing.T) {
	f := &fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted},
		events: []runner.Event{
			event(1, runner.EventProgress, "thinking about it", ""),
			event(2, runner.EventProgress, "", `{"item":{"id":"i1","type":"command_execution","command":"go test ./...","status":"in_progress"}}`),
			event(3, runner.EventProgress, "", `{"item":{"id":"i1","type":"command_execution","command":"go test ./...","status":"completed","output":"ok"}}`),
			event(4, runner.EventCompleted, "", ""),
		},
	}
	sink := &recordingSink{}
	if _, err := New(testConfig(f, 1, "")).Turn(context.Background(), testRun(),
		backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "test it"}}}, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.deltas) != 1 || sink.deltas[0] != "thinking about it" {
		t.Fatalf("deltas = %v, want the prose only", sink.deltas)
	}
	if len(sink.toolStarts) != 1 || sink.toolStarts[0] != "i1:command_execution" {
		t.Fatalf("toolStarts = %v", sink.toolStarts)
	}
	if len(sink.toolEnds) != 1 || sink.toolEnds[0].Result != "ok" || sink.toolEnds[0].Err {
		t.Fatalf("toolEnds = %+v", sink.toolEnds)
	}
}

// An agent outcome depends on its complete transcript and usage. If the runner
// cannot supply a cursor gap, the adapter must fail instead of returning a
// partial completion from the terminal receipt.
func TestFreshCursorGapDoesNotReturnAnEmptyCompletion(t *testing.T) {
	f := &gappyRunner{fakeRunner: fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, Cursor: 9},
		phases:  []runner.Phase{runner.PhaseCompleted},
	}}
	cfg := testConfig(&f.fakeRunner, 1, "")
	cfg.Runner = f
	out, err := New(cfg).Turn(context.Background(), testRun(),
		backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "hi"}}}, &recordingSink{})
	if err == nil || !strings.Contains(err.Error(), "event history is incomplete") {
		t.Fatalf("Turn error = %v, want an explicit incomplete-history error", err)
	}
	if out.Status != backend.StatusFailed {
		t.Fatalf("status = %s, want failed rather than a partial completion", out.Status)
	}
}

// gappyRunner answers the first stream with a cursor-expired error carrying
// SnapshotRequired, the way the shared client reports a gap.
type gappyRunner struct {
	fakeRunner
	served bool
}

func (g *gappyRunner) Events(ctx context.Context, attemptID string, after uint64) (dispatch.Stream, error) {
	if !g.served {
		g.served = true
		return &erroringStream{err: &runner.Error{
			Code: runner.ErrorCursorExpired, Retryable: true, SnapshotRequired: true,
			Message: "event cursor gap",
		}}, nil
	}
	return g.fakeRunner.Events(ctx, attemptID, after)
}

type erroringStream struct{ err error }

func (e *erroringStream) Next(context.Context) (runner.Event, error) { return runner.Event{}, e.err }
func (e *erroringStream) Close() error                               { return nil }

// A dispatch that cannot be made is a failure with the runner's own code kept
// for errors.As, never a message a caller has to parse.
func TestStartFailureKeepsTheProtocolError(t *testing.T) {
	f := &fakeRunner{startErr: &runner.Error{Code: runner.ErrorStaleAttempt, Message: "attempt epoch is obsolete"}}
	_, err := New(testConfig(f, 1, "")).Turn(context.Background(), testRun(),
		backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "hi"}}}, &recordingSink{})
	if err == nil {
		t.Fatal("expected a failure")
	}
	var protocol *runner.Error
	if !errors.As(err, &protocol) || protocol.Code != runner.ErrorStaleAttempt {
		t.Fatalf("error %v does not carry the protocol code", err)
	}
}

// A turn with no credential is refused before anything is dispatched: a runner
// has no identity of its own, and a start without one is a bug to name here
// rather than a 400 from a machine.
func TestTurnRefusesADispatchWithoutTheRequiredIdentity(t *testing.T) {
	f := &fakeRunner{}
	cfg := testConfig(f, 1, "")
	cfg.Credential = llm.HarnessIdentity{}
	_, err := New(cfg).Turn(context.Background(), testRun(),
		backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "hi"}}}, &recordingSink{})
	if err == nil || !strings.Contains(err.Error(), "harness credential") {
		t.Fatalf("error = %v, want a refusal naming the credential", err)
	}
	if len(f.starts) != 0 {
		t.Fatal("nothing may be dispatched without an identity")
	}
}

// A PERMISSION park is the other kind of needs_input, and it must not be
// reported as a question: it names a call and its arguments, which is what
// makes the provider file an approval with Approve and Deny rather than a
// question with a text box.
func TestPermissionParksWithTheToolAndItsArguments(t *testing.T) {
	f := &fakeRunner{
		receipt: runner.Receipt{
			AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
			Blocker:    "the harness is asking permission to use Bash",
			Permission: &runner.PermissionRequest{ID: "permission-9", Tool: "Bash", Input: `{"command":"rm -rf /tmp/x"}`},
		},
		events: []runner.Event{
			event(1, runner.EventProgress, "looking at the tree", ""),
			event(2, runner.EventNeedsInput, "the harness is asking permission to use Bash", ""),
		},
	}
	b := New(testConfig(f, 1, ""))
	out, err := b.Turn(context.Background(), testRun(), backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "clean up"}},
	}, &recordingSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if out.Status != backend.StatusParked || out.Parked == nil {
		t.Fatalf("status = %s, parked = %v; want a parked turn", out.Status, out.Parked)
	}
	if out.Parked.Tool != "Bash" {
		t.Fatalf("Tool = %q, want Bash — without it the provider files a question", out.Parked.Tool)
	}
	if out.Parked.Args != `{"command":"rm -rf /tmp/x"}` {
		t.Fatalf("Args = %q, want the call the approval authorizes", out.Parked.Args)
	}
	if out.Parked.Question != "" {
		t.Fatalf("Question = %q; a permission park is a verdict, not an answer", out.Parked.Question)
	}
	var state State
	if err := json.Unmarshal(out.Parked.State, &state); err != nil {
		t.Fatal(err)
	}
	if state.PermissionID != "permission-9" || state.ClarificationID != "" {
		t.Fatalf("resume state = %+v, want the permission id alone", state)
	}
}

// Approving resumes with a VERDICT on that request — never with a
// clarification, which would relaunch the harness and abandon the open call.
func TestPermissionApprovalResumesWithAVerdict(t *testing.T) {
	parked := runner.Receipt{
		AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
		Permission: &runner.PermissionRequest{ID: "permission-9", Tool: "Bash", Input: `{"command":"ls"}`},
	}
	for _, tc := range []struct {
		name     string
		answer   backend.Answer
		decision string
		note     string
	}{
		{"approved", backend.Answer{Decided: true, Approved: true}, runner.PermissionAllow, ""},
		{"approved with a note", backend.Answer{Decided: true, Approved: true, Note: "only this once"}, runner.PermissionAllow, "only this once"},
		{"denied", backend.Answer{Decided: true, Approved: false, Note: "not on production"}, runner.PermissionDeny, "not on production"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{
				receipt: parked,
				phases:  []runner.Phase{runner.PhaseNeedsInput, runner.PhaseCompleted},
				events:  []runner.Event{event(1, runner.EventCompleted, "", "")},
			}
			b := New(testConfig(f, 1, "sess-1"))
			state, err := json.Marshal(State{TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", PermissionID: "permission-9"})
			if err != nil {
				t.Fatal(err)
			}
			answer := tc.answer
			answer.State = state
			if _, err := b.Continue(context.Background(), testRun(), answer, &recordingSink{}); err != nil {
				t.Fatalf("Continue: %v", err)
			}
			if len(f.resumes) != 1 {
				t.Fatalf("resumes = %d, want 1", len(f.resumes))
			}
			resume := f.resumes[0]
			if resume.PermissionID != "permission-9" {
				t.Fatalf("resume permissionID = %q, want permission-9", resume.PermissionID)
			}
			if resume.PermissionDecision != tc.decision {
				t.Fatalf("resume decision = %q, want %q", resume.PermissionDecision, tc.decision)
			}
			if resume.ClarificationID != "" {
				t.Fatalf("resume clarificationID = %q; a verdict is not an answer", resume.ClarificationID)
			}
			if resume.Resolution != tc.note {
				t.Fatalf("resume resolution = %q, want %q", resume.Resolution, tc.note)
			}
			if resume.AttemptEpoch != 1 {
				t.Fatalf("resume epoch = %d; a resume addresses the SAME attempt", resume.AttemptEpoch)
			}
		})
	}
}

func TestContinueDoesNotApplyAnOldAnswerToANewerPark(t *testing.T) {
	tests := []struct {
		name  string
		state State
		rcpt  runner.Receipt
		check func(*testing.T, backend.Outcome)
	}{
		{
			name: "new permission ID",
			state: State{
				TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", Cursor: 1,
				PermissionID: "permission-old", ParkType: parkTypePermission,
			},
			rcpt: runner.Receipt{
				AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
				Blocker:    "permission for a new call",
				Permission: &runner.PermissionRequest{ID: "permission-new", Tool: "Bash", Input: `{"command":"new call"}`},
			},
			check: func(t *testing.T, out backend.Outcome) {
				t.Helper()
				if out.Parked == nil || out.Parked.Tool != "Bash" {
					t.Fatalf("parked outcome = %+v, want the new Bash permission", out.Parked)
				}
				var state State
				if err := json.Unmarshal(out.Parked.State, &state); err != nil || state.PermissionID != "permission-new" {
					t.Fatalf("re-parked state = %+v (%v), want permission-new", state, err)
				}
			},
		},
		{
			name: "new clarification ID",
			state: State{
				TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", Cursor: 1,
				ClarificationID: "clarification-old", ParkType: parkTypeQuestion,
			},
			rcpt: runner.Receipt{
				AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
				Blocker:       "the new question",
				Clarification: &runner.Clarification{ID: "clarification-new", Text: "the new question"},
			},
			check: func(t *testing.T, out backend.Outcome) {
				t.Helper()
				if out.Parked == nil || out.Parked.Question != "the new question" {
					t.Fatalf("parked outcome = %+v, want the new question", out.Parked)
				}
				var state State
				if err := json.Unmarshal(out.Parked.State, &state); err != nil || state.ClarificationID != "clarification-new" {
					t.Fatalf("re-parked state = %+v (%v), want clarification-new", state, err)
				}
			},
		},
		{
			name: "same ID but a different park type",
			state: State{
				TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", Cursor: 1,
				PermissionID: "same-id", ParkType: parkTypePermission,
			},
			rcpt: runner.Receipt{
				AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
				Blocker:       "a question with a colliding ID",
				Clarification: &runner.Clarification{ID: "same-id", Text: "a question with a colliding ID"},
			},
			check: func(t *testing.T, out backend.Outcome) {
				t.Helper()
				if out.Parked == nil || out.Parked.Question != "a question with a colliding ID" {
					t.Fatalf("parked outcome = %+v, want the current question", out.Parked)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.state.Snapshot) == 0 {
				snapshot, err := json.Marshal(dispatch.Snapshot{
					Position: tc.state.position(), State: json.RawMessage(`{"version":1}`),
				})
				if err != nil {
					t.Fatal(err)
				}
				tc.state.Snapshot = snapshot
			}
			f := &fakeRunner{
				receipt: tc.rcpt,
				events:  []runner.Event{event(2, runner.EventNeedsInput, tc.rcpt.Blocker, "")},
			}
			state, err := json.Marshal(tc.state)
			if err != nil {
				t.Fatal(err)
			}
			out, err := New(testConfig(f, 1, "sess-1")).Continue(context.Background(), testRun(), backend.Answer{
				State: state, Decided: true, Approved: true, Note: "answer for the old park",
			}, &recordingSink{})
			if err != nil {
				t.Fatalf("Continue: %v", err)
			}
			if len(f.resumes) != 0 {
				t.Fatalf("resumes = %+v, want the new park left unanswered", f.resumes)
			}
			if out.Status != backend.StatusParked {
				t.Fatalf("status = %s, want parked", out.Status)
			}
			tc.check(t, out)
		})
	}
}

func TestLegacyAnonymousQuestionStillAcceptsItsAnswer(t *testing.T) {
	state, err := marshalStateWithSnapshot(t, State{
		TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", Cursor: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{
		receipt: runner.Receipt{
			AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
			Blocker: "the harness is waiting for an answer",
		},
		events: []runner.Event{event(2, runner.EventNeedsInput, "the harness is waiting for an answer", "")},
	}
	out, err := New(testConfig(f, 1, "sess-1")).Continue(context.Background(), testRun(), backend.Answer{
		State: state, Decided: true, Approved: true, Note: "use the patch version",
	}, &recordingSink{})
	if err != nil {
		t.Fatalf("Continue legacy anonymous question: %v", err)
	}
	if len(f.resumes) != 1 || f.resumes[0].ClarificationID != "" || f.resumes[0].Resolution != "use the patch version" {
		t.Fatalf("resume = %+v, want the answer applied to the anonymous question", f.resumes)
	}
	if out.Status != backend.StatusParked || out.Parked == nil || out.Parked.Question == "" {
		t.Fatalf("outcome = %+v, want the runner's current question", out)
	}
}

func TestContinueRejectsAStateFromAnotherRunner(t *testing.T) {
	f := &fakeRunner{}
	cfg := testConfig(f, 1, "sess-1")
	cfg.BackendKey = "edge-b"
	state, err := json.Marshal(State{
		TaskID: "task-1", AttemptID: "run-1", Epoch: 1, BackendKey: "edge-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(cfg).Continue(context.Background(), testRun(), backend.Answer{State: state}, &recordingSink{})
	if err == nil || !strings.Contains(err.Error(), "different harness backend") {
		t.Fatalf("Continue error = %v, want a backend identity mismatch", err)
	}
	if f.inspects != 0 || len(f.resumes) != 0 {
		t.Fatalf("runner calls after backend mismatch: inspections=%d resumes=%d", f.inspects, len(f.resumes))
	}
}

func TestSpentAccumulatesAcrossParksAndSkipsAlreadyObservedEvents(t *testing.T) {
	first := &fakeRunner{
		receipt: runner.Receipt{
			AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
			Clarification: &runner.Clarification{ID: "clar-1", Text: "first question"},
		},
		events: []runner.Event{
			event(1, runner.EventProgress, "", `{"usage":{"input_tokens":100,"output_tokens":10},"total_cost_usd":0.01}`),
			event(2, runner.EventNeedsInput, "first question", ""),
		},
	}
	firstOut, err := New(testConfig(first, 1, "")).Turn(context.Background(), testRun(),
		backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "start"}}}, &recordingSink{})
	if err != nil {
		t.Fatalf("initial turn: %v", err)
	}
	firstState := parkedState(t, firstOut)
	wantFirstSpent := backend.Cost{Tokens: backend.Tokens{InputTokens: 100, OutputTokens: 10}, CostMicros: 10_000}
	if firstState.Cursor != 2 || firstState.Spent != wantFirstSpent {
		t.Fatalf("first parked state = %+v, want cursor 2 and cumulative usage %+v", firstState, wantFirstSpent)
	}

	second := &fakeRunner{
		receipt: runner.Receipt{
			AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
			Clarification: &runner.Clarification{ID: "clar-2", Text: "second question"},
		},
		events: []runner.Event{
			event(1, runner.EventProgress, "", `{"usage":{"input_tokens":100,"output_tokens":10},"total_cost_usd":0.01}`),
			event(2, runner.EventNeedsInput, "first question", ""),
			event(3, runner.EventProgress, "", `{"usage":{"input_tokens":50,"output_tokens":5},"total_cost_usd":0.02}`),
			event(4, runner.EventNeedsInput, "second question", ""),
		},
	}
	secondOut, err := New(testConfig(second, 1, "sess-1")).Continue(context.Background(), testRun(), backend.Answer{
		State: firstOut.Parked.State,
	}, &recordingSink{})
	if err != nil {
		t.Fatalf("rejoin through second park: %v", err)
	}
	secondState := parkedState(t, secondOut)
	wantSecondSpent := backend.Cost{Tokens: backend.Tokens{InputTokens: 150, OutputTokens: 15}, CostMicros: 30_000}
	if secondState.Cursor != 4 || secondState.Spent != wantSecondSpent {
		t.Fatalf("second parked state = %+v, want cursor 4 and cumulative usage %+v", secondState, wantSecondSpent)
	}
	if secondOut.Usage.Billed != (backend.Cost{Tokens: backend.Tokens{InputTokens: 50, OutputTokens: 5}, CostMicros: 20_000}) {
		t.Fatalf("second park billed %+v, want only the new segment", secondOut.Usage.Billed)
	}

	third := &fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, SessionID: "sess-1"},
		events: []runner.Event{
			event(1, runner.EventProgress, "", `{"usage":{"input_tokens":100,"output_tokens":10},"total_cost_usd":0.01}`),
			event(2, runner.EventNeedsInput, "first question", ""),
			event(3, runner.EventProgress, "", `{"usage":{"input_tokens":50,"output_tokens":5},"total_cost_usd":0.02}`),
			event(4, runner.EventNeedsInput, "second question", ""),
			event(5, runner.EventProgress, "", `{"usage":{"input_tokens":25,"output_tokens":3},"total_cost_usd":0.03}`),
			event(6, runner.EventCompleted, "", ""),
		},
	}
	completed, err := New(testConfig(third, 1, "sess-1")).Continue(context.Background(), testRun(), backend.Answer{
		State: secondOut.Parked.State,
	}, &recordingSink{})
	if err != nil {
		t.Fatalf("rejoin to completion: %v", err)
	}
	wantBilled := backend.Cost{Tokens: backend.Tokens{InputTokens: 25, OutputTokens: 3}, CostMicros: 30_000}
	wantTotal := backend.Cost{Tokens: backend.Tokens{InputTokens: 175, OutputTokens: 18}, CostMicros: 60_000}
	if completed.Usage.Billed != wantBilled || completed.Usage.Total != wantTotal {
		t.Fatalf("final usage = %+v, want billed %+v and total %+v", completed.Usage, wantBilled, wantTotal)
	}
}

func TestCheckpointRecoveryRestoresDispatchSnapshotAtTerminalReceipt(t *testing.T) {
	initial := &fakeRunner{
		receipt:          runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseRunning, SessionID: "sess-1"},
		eventsErrorAfter: 6,
		events: []runner.Event{
			event(1, runner.EventProgress, "", `{"turn":{"status":"inProgress"}}`),
			event(2, runner.EventProgress, "The result is done.", `{"threadId":"thread-1","turnId":"turn-1","itemId":"msg-1","delta":"The result is done."}`),
			event(3, runner.EventProgress, "", `{"tokenUsage":{"last":{"inputTokens":600,"outputTokens":10},"total":{"inputTokens":600,"outputTokens":10}}}`),
			event(4, runner.EventProgress, "", `{"item":{"id":"exec-1","type":"commandExecution","command":"go test ./...","status":"inProgress"}}`),
			event(5, runner.EventProgress, "", `{"item":{"id":"msg-1","type":"agentMessage","text":"The result is done.","phase":"final_answer"}}`),
			event(6, runner.EventCheckpoint, "", ""),
		},
	}
	initialSink := &recordingSink{}
	cfg := testConfig(initial, 1, "")
	cfg.BackendKey = "sha256:runner-a"
	_, err := New(cfg).Turn(context.Background(), testRun(), backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "run tests"}},
	}, initialSink)
	if err == nil || !strings.Contains(err.Error(), "simulated stream loss") {
		t.Fatalf("Turn error = %v, want the simulated loss after its checkpoint", err)
	}
	if len(initialSink.checkpoints) != 1 {
		t.Fatalf("checkpoints = %d, want the cursor-6 snapshot", len(initialSink.checkpoints))
	}
	var checkpoint State
	if err := json.Unmarshal(initialSink.checkpoints[0], &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Cursor != 6 || checkpoint.BackendKey != "sha256:runner-a" || checkpoint.Spent.InputTokens != 600 {
		t.Fatalf("checkpoint = %+v, want cursor 6, runner key and 600 input tokens", checkpoint)
	}
	var saved dispatch.Snapshot
	if err := json.Unmarshal(checkpoint.Snapshot, &saved); err != nil {
		t.Fatal(err)
	}
	var normalized struct {
		Text  string `json:"text"`
		Final string `json:"final"`
		Tools map[string]struct {
			Ended bool `json:"ended"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(saved.State, &normalized); err != nil {
		t.Fatal(err)
	}
	if normalized.Text != "The result is done." || normalized.Final != "The result is done." || normalized.Tools["exec-1"].Ended {
		t.Fatalf("normalized checkpoint = %+v, want text/final and open tool state", normalized)
	}

	// Rejoin after the process died. The snapshot supplies the normalized prefix
	// while the runner supplies only the tail after cursor 6.
	recovered := &fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, SessionID: "sess-1"},
		events: []runner.Event{
			event(1, runner.EventProgress, "", `{"turn":{"status":"inProgress"}}`),
			event(2, runner.EventProgress, "The result is done.", `{"threadId":"thread-1","turnId":"turn-1","itemId":"msg-1","delta":"The result is done."}`),
			event(3, runner.EventProgress, "", `{"tokenUsage":{"last":{"inputTokens":600,"outputTokens":10},"total":{"inputTokens":600,"outputTokens":10}}}`),
			event(4, runner.EventProgress, "", `{"item":{"id":"exec-1","type":"commandExecution","command":"go test ./...","status":"inProgress"}}`),
			event(5, runner.EventProgress, "", `{"item":{"id":"msg-1","type":"agentMessage","text":"The result is done.","phase":"final_answer"}}`),
			event(6, runner.EventCheckpoint, "", ""),
			event(7, runner.EventProgress, "", `{"tokenUsage":{"last":{"inputTokens":600,"outputTokens":10},"total":{"inputTokens":600,"outputTokens":10}}}`),
			event(8, runner.EventProgress, "", `{"item":{"id":"exec-1","type":"commandExecution","command":"go test ./...","status":"completed","durationMs":850,"aggregatedOutput":"ok"}}`),
			event(9, runner.EventProgress, "completed", `{"turn":{"id":"turn-1","status":"completed","durationMs":1340}}`),
			event(10, runner.EventCompleted, "", ""),
		},
	}
	resumeCfg := testConfig(recovered, 1, "sess-1")
	resumeCfg.BackendKey = "sha256:runner-a"
	recoveredSink := &recordingSink{}
	out, err := New(resumeCfg).Continue(context.Background(), testRun(), backend.Answer{State: initialSink.checkpoints[0]}, recoveredSink)
	if err != nil {
		t.Fatalf("Continue from terminal receipt: %v", err)
	}
	if out.Status != backend.StatusCompleted || out.Text != "The result is done." || out.Final != "The result is done." {
		t.Fatalf("recovered outcome = status %s, text %q, final %q", out.Status, out.Text, out.Final)
	}
	if len(recoveredSink.deltas) != 0 || len(recoveredSink.toolStarts) != 0 || len(recoveredSink.toolEnds) != 1 {
		t.Fatalf("recovered events = deltas %q, starts %q, ends %+v", recoveredSink.deltas, recoveredSink.toolStarts, recoveredSink.toolEnds)
	}
	if recoveredSink.toolEnds[0].ID != "exec-1" || recoveredSink.toolEnds[0].Duration != 850*time.Millisecond {
		t.Fatalf("recovered tool end = %+v, want the saved open call closed with its duration", recoveredSink.toolEnds[0])
	}
	if got := out.Usage; got.Total.InputTokens != 600 || got.Total.OutputTokens != 10 || got.Billed.InputTokens != 0 || got.Billed.OutputTokens != 0 {
		t.Fatalf("recovered usage = %+v, want 600/10 total and no duplicate billing", got)
	}
	if len(recoveredSink.assistants) != 1 || recoveredSink.assistants[0].Duration != 490*time.Millisecond {
		t.Fatalf("recovered assistant messages = %+v, want 490ms after subtracting the restored tool", recoveredSink.assistants)
	}
}

func TestLegacyResumeReplaysPrefixSilentlyAndResetsReplayCost(t *testing.T) {
	legacy, err := json.Marshal(State{
		TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", Cursor: 3,
		Spent: backend.Cost{Tokens: backend.Tokens{InputTokens: 20, OutputTokens: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, SessionID: "sess-1"},
		events: []runner.Event{
			event(1, runner.EventProgress, "", `{"turn":{"status":"inProgress"}}`),
			event(2, runner.EventProgress, "", `{"tokenUsage":{"last":{"inputTokens":20,"outputTokens":2},"total":{"inputTokens":20,"outputTokens":2}}}`),
			event(3, runner.EventProgress, "old ", `{"threadId":"thread-1","turnId":"turn-1","itemId":"msg-1","delta":"old "}`),
			event(4, runner.EventProgress, "", `{"tokenUsage":{"last":{"inputTokens":20,"outputTokens":2},"total":{"inputTokens":20,"outputTokens":2}}}`),
			event(5, runner.EventProgress, "tail", `{"threadId":"thread-1","turnId":"turn-1","itemId":"msg-1","delta":"tail"}`),
			event(6, runner.EventProgress, "", `{"item":{"id":"msg-1","type":"agentMessage","text":"old tail","phase":"final_answer"}}`),
			event(7, runner.EventCompleted, "", ""),
		},
	}
	sink := &recordingSink{}
	out, err := New(testConfig(f, 1, "sess-1")).Continue(context.Background(), testRun(), backend.Answer{State: legacy}, sink)
	if err != nil {
		t.Fatalf("Continue legacy state: %v", err)
	}
	if out.Text != "old tail" || out.Final != "old tail" {
		t.Fatalf("legacy recovery text/final = %q / %q, want the replayed prefix plus tail", out.Text, out.Final)
	}
	if len(sink.deltas) != 1 || sink.deltas[0] != "tail" {
		t.Fatalf("legacy recovery deltas = %q, want only the post-cursor tail", sink.deltas)
	}
	if out.Usage.Total.InputTokens != 20 || out.Usage.Total.OutputTokens != 2 || out.Usage.Billed != (backend.Cost{}) {
		t.Fatalf("legacy recovery usage = %+v, want replayed spend once and no second bill", out.Usage)
	}
}

func TestLegacySnapshotMigratesWithoutReplayingAnExpiredPrefix(t *testing.T) {
	legacySnapshot := json.RawMessage(`{"version":1,"text":"saved prefix ","final":"saved prefix ","codexTurnID":"turn-1","turnStarted":true,"codexTotalTokens":22,"codexTotalSeen":true,"codexLast":"same-call"}`)
	state, err := json.Marshal(State{
		TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", Cursor: 3,
		Spent: backend.Cost{Tokens: backend.Tokens{InputTokens: 20, OutputTokens: 2}}, Snapshot: legacySnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &legacySnapshotRunner{fakeRunner: fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, SessionID: "sess-1"},
		events: []runner.Event{
			event(4, runner.EventProgress, "", `{"tokenUsage":{"last":{"inputTokens":20,"outputTokens":2},"total":{"inputTokens":20,"outputTokens":2}}}`),
			event(5, runner.EventProgress, "tail", `{"threadId":"thread-1","turnId":"turn-1","itemId":"msg-1","delta":"tail"}`),
			event(6, runner.EventProgress, "", `{"item":{"id":"msg-1","type":"agentMessage","text":"saved prefix tail","phase":"final_answer"}}`),
			event(7, runner.EventCompleted, "", ""),
		},
	}}
	sink := &recordingSink{}
	cfg := testConfig(&f.fakeRunner, 1, "sess-1")
	cfg.Runner = f
	out, err := New(cfg).Continue(context.Background(), testRun(), backend.Answer{State: state}, sink)
	if err != nil {
		t.Fatalf("Continue v1 snapshot: %v", err)
	}
	if out.Text != "saved prefix tail" || out.Usage.Billed != (backend.Cost{}) {
		t.Fatalf("migrated snapshot outcome = text %q, usage %+v", out.Text, out.Usage)
	}
	if len(sink.deltas) != 1 || sink.deltas[0] != "tail" {
		t.Fatalf("v1 snapshot replayed deltas %q, want only the tail", sink.deltas)
	}
	if f.zeroCursorReads != 0 {
		t.Fatalf("v1 snapshot made %d reads from cursor zero; its saved normalizer must survive expired prefix history", f.zeroCursorReads)
	}
}

func TestRejoinUsesNewestClarificationFromTheTail(t *testing.T) {
	state := State{
		TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", Cursor: 1,
		ClarificationID: "clarification-old", ParkType: parkTypeQuestion,
	}
	inner, err := json.Marshal(struct {
		Version       int                   `json:"version"`
		Text          string                `json:"text,omitempty"`
		Clarification *runner.Clarification `json:"clarification,omitempty"`
	}{Version: 1, Text: "saved prefix\n", Clarification: &runner.Clarification{ID: "clarification-old", Text: "the old question"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(dispatch.Snapshot{Position: state.position(), State: inner})
	if err != nil {
		t.Fatal(err)
	}
	state.Snapshot = snapshot
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{
		receipt: runner.Receipt{
			AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
			Blocker: "the newest question",
		},
		events: []runner.Event{
			event(1, runner.EventNeedsInput, "the old question", `{"clarification":{"id":"clarification-old","text":"the old question"}}`),
			event(2, runner.EventNeedsInput, "the middle question", `{"clarification":{"id":"clarification-middle","text":"the middle question"}}`),
			event(3, runner.EventNeedsInput, "the newest question", `{"clarification":{"id":"clarification-new","text":"the newest question"}}`),
		},
	}
	out, err := New(testConfig(f, 1, "sess-1")).Continue(context.Background(), testRun(), backend.Answer{State: raw}, &recordingSink{})
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	if out.Status != backend.StatusParked || out.Parked == nil || out.Parked.Question != "the newest question" {
		t.Fatalf("rejoined park = %+v, want the newest question", out.Parked)
	}
	if got := parkedState(t, out).ClarificationID; got != "clarification-new" {
		t.Fatalf("rejoined clarification ID = %q, want clarification-new", got)
	}
}

func TestAnsweredClarificationDoesNotReplaceANewerQuestion(t *testing.T) {
	first := &fakeRunner{
		receipt: runner.Receipt{
			AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
			Clarification: &runner.Clarification{ID: "clar-1", Text: "the first question"},
		},
		events: []runner.Event{
			event(1, runner.EventNeedsInput, "the first question", `{"clarification":{"id":"clar-1","text":"the first question"}}`),
		},
	}
	parked, err := New(testConfig(first, 1, "")).Turn(context.Background(), testRun(), backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "start"}},
	}, &recordingSink{})
	if err != nil {
		t.Fatalf("initial turn: %v", err)
	}

	oldPark := first.receipt
	newPark := runner.Receipt{
		AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
		Blocker:       "the new question",
		Clarification: &runner.Clarification{ID: "clar-2", Text: "the new question"},
	}
	running := runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseRunning, SessionID: "sess-1"}
	continued := &fakeRunner{
		receipt:         newPark,
		inspectReceipts: []runner.Receipt{oldPark, newPark},
		resumeReceipt:   &running,
		events: []runner.Event{
			event(1, runner.EventNeedsInput, "the first question", `{"clarification":{"id":"clar-1","text":"the first question"}}`),
			event(2, runner.EventAccepted, "resume accepted", ""),
			event(3, runner.EventNeedsInput, "the new question", `{"clarification":{"id":"clar-2","text":"the new question"}}`),
		},
	}
	answer := backend.Answer{State: parked.Parked.State, Decided: true, Note: "the answer"}
	out, err := New(testConfig(continued, 1, "sess-1")).Continue(context.Background(), testRun(), answer, &recordingSink{})
	if err != nil {
		t.Fatalf("continue to second question: %v", err)
	}
	if len(continued.resumes) != 1 || continued.resumes[0].ClarificationID != "clar-1" {
		t.Fatalf("resume = %+v, want the answered clarification ID", continued.resumes)
	}
	if out.Status != backend.StatusParked || out.Parked == nil || out.Parked.Question != "the new question" {
		t.Fatalf("second park = %+v, want the new question", out.Parked)
	}
	if got := parkedState(t, out).ClarificationID; got != "clar-2" {
		t.Fatalf("second clarification ID = %q, want clar-2", got)
	}
}

func TestRecoveryDrainsPastAnOldNeedsInputEventWhenReceiptIsTerminal(t *testing.T) {
	for _, oldTerminal := range []string{runner.EventNeedsInput, runner.EventCompleted} {
		t.Run(oldTerminal, func(t *testing.T) {
			inner := json.RawMessage(`{"version":1,"text":"already observed\n"}`)
			state, err := json.Marshal(State{
				TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", Cursor: 1,
				Snapshot: inner,
			})
			if err != nil {
				t.Fatal(err)
			}
			f := &fakeRunner{
				receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, SessionID: "sess-1"},
				events: []runner.Event{
					event(1, runner.EventProgress, "already observed", ""),
					event(2, oldTerminal, "", ""),
					event(3, runner.EventProgress, "", `{"type":"result","subtype":"success","is_error":false,"result":"the approved command finished"}`),
					event(4, runner.EventCompleted, "", ""),
				},
			}
			out, err := New(testConfig(f, 1, "sess-1")).Continue(context.Background(), testRun(), backend.Answer{State: state}, &recordingSink{})
			if err != nil {
				t.Fatalf("Continue: %v", err)
			}
			if out.Status != backend.StatusCompleted || out.Final != "the approved command finished" {
				t.Fatalf("recovered outcome = status %s, final %q; want completion after old %s event", out.Status, out.Final, oldTerminal)
			}
		})
	}
}

func TestRecoveryFailsWhenQuietStreamsCannotReachTerminalReceiptCursor(t *testing.T) {
	state, err := json.Marshal(State{
		TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", Cursor: 1,
		Snapshot: json.RawMessage(`{"version":1,"text":"saved prefix\n"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &quietRunner{fakeRunner: fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, SessionID: "sess-1", Cursor: 4},
	}}
	out, err := New(testConfig(&f.fakeRunner, 1, "sess-1")).Continue(context.Background(), testRun(), backend.Answer{State: state}, &recordingSink{})
	if err == nil || !strings.Contains(err.Error(), "event history is incomplete after saved cursor 1") {
		t.Fatalf("Continue error = %v, want an explicit error for the unavailable terminal tail", err)
	}
	if out.Status != backend.StatusFailed {
		t.Fatalf("status = %s, want failed instead of a partial completion", out.Status)
	}
}

func TestRecoveredSnapshotFailsWhenPostCheckpointEventsExpired(t *testing.T) {
	state, err := json.Marshal(State{
		TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", Cursor: 1,
		Snapshot: json.RawMessage(`{"version":1,"text":"saved prefix","final":"saved prefix"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &gappyRunner{fakeRunner: fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted},
		events: []runner.Event{
			event(1, runner.EventProgress, "already saved", ""),
			event(2, runner.EventCompleted, "", ""),
		},
	}}
	cfg := testConfig(&f.fakeRunner, 1, "sess-1")
	cfg.Runner = f
	sink := &recordingSink{}
	out, err := New(cfg).Continue(context.Background(), testRun(), backend.Answer{State: state}, sink)
	if err == nil || !strings.Contains(err.Error(), "event history is incomplete after saved cursor 1") {
		t.Fatalf("Continue error = %v, want an explicit error for the expired post-checkpoint tail", err)
	}
	if out.Status != backend.StatusFailed || len(sink.deltas) != 0 {
		t.Fatalf("outcome = %+v, deltas %q; want failure without partial transcript", out, sink.deltas)
	}
}

func TestCodexResumeTimingResetsOnlyForClarification(t *testing.T) {
	turnOne := []runner.Event{
		event(1, runner.EventProgress, "", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress"}}`),
		event(2, runner.EventProgress, "", `{"item":{"id":"tool-1","type":"commandExecution","command":"first","status":"inProgress"}}`),
		event(3, runner.EventProgress, "", `{"item":{"id":"tool-1","type":"commandExecution","command":"first","status":"completed","durationMs":400}}`),
	}
	running := runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseRunning, SessionID: "sess-1"}
	completedReceipt := runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, SessionID: "sess-1"}

	t.Run("clarification starts a new native turn", func(t *testing.T) {
		firstPark := runner.Receipt{
			AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
			Clarification: &runner.Clarification{ID: "clar-1", Text: "continue?"},
		}
		initial := &fakeRunner{receipt: firstPark, events: append(append([]runner.Event{}, turnOne...),
			event(4, runner.EventNeedsInput, "first question", ""),
		)}
		firstOut, err := New(testConfig(initial, 1, "")).Turn(context.Background(), testRun(), backend.Input{
			Messages: []backend.Message{{Role: backend.RoleUser, Content: "start"}},
		}, &recordingSink{})
		if err != nil {
			t.Fatalf("initial turn: %v", err)
		}

		secondPark := runner.Receipt{
			AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
			Clarification: &runner.Clarification{ID: "clar-2", Text: "continue again?"},
		}
		secondEvents := append(append([]runner.Event{}, turnOne...),
			event(4, runner.EventNeedsInput, "first question", ""),
			event(5, runner.EventProgress, "", `{"threadId":"thread-1","turn":{"id":"turn-2","status":"inProgress"}}`),
			event(6, runner.EventProgress, "", `{"item":{"id":"tool-2","type":"commandExecution","command":"second","status":"inProgress"}}`),
			event(7, runner.EventProgress, "", `{"item":{"id":"tool-2","type":"commandExecution","command":"second","status":"completed","durationMs":500}}`),
			event(8, runner.EventNeedsInput, "second question", `{"clarification":{"id":"clar-2","text":"continue again?"}}`),
		)
		resumeRunner := &fakeRunner{
			receipt: secondPark, inspectReceipts: []runner.Receipt{firstPark, secondPark},
			resumeReceipt: &running, events: secondEvents,
		}
		secondOut, err := New(testConfig(resumeRunner, 1, "sess-1")).Continue(context.Background(), testRun(), backend.Answer{
			State: firstOut.Parked.State, Decided: true, Note: "yes",
		}, &recordingSink{})
		if err != nil {
			t.Fatalf("continue to second park: %v", err)
		}
		secondState := parkedState(t, secondOut)
		turn, tools := codexTimingSnapshot(t, secondState)
		if turn != "turn-2" || tools != int64(500*time.Millisecond) {
			t.Fatalf("second turn timing = %q / %s, want turn-2 and 500ms", turn, time.Duration(tools))
		}

		thirdEvents := []runner.Event{
			event(9, runner.EventProgress, "", `{"threadId":"thread-1","turn":{"id":"turn-3","status":"inProgress"}}`),
			event(10, runner.EventProgress, "completed", `{"turn":{"id":"turn-3","status":"completed","durationMs":1500}}`),
			event(11, runner.EventProgress, "", `{"item":{"id":"msg-1","type":"agentMessage","text":"Done.","phase":"final_answer"}}`),
			event(12, runner.EventCompleted, "", ""),
		}
		thirdRunner := &fakeRunner{
			receipt: completedReceipt, inspectReceipts: []runner.Receipt{secondPark, completedReceipt},
			resumeReceipt: &running, events: thirdEvents,
		}
		thirdSink := &recordingSink{}
		completed, err := New(testConfig(thirdRunner, 1, "sess-1")).Continue(context.Background(), testRun(), backend.Answer{
			State: secondOut.Parked.State, Decided: true, Note: "yes",
		}, thirdSink)
		if err != nil {
			t.Fatalf("continue to completion: %v", err)
		}
		if completed.Status != backend.StatusCompleted || len(thirdSink.assistants) != 1 || thirdSink.assistants[0].Duration != 1500*time.Millisecond {
			t.Fatalf("completed timing = status %s, assistants %+v; want a fresh 1500ms turn", completed.Status, thirdSink.assistants)
		}
	})

	t.Run("permission verdict stays in the current native turn", func(t *testing.T) {
		permission := &runner.PermissionRequest{ID: "permission-1", Tool: "Bash", Input: `{"command":"second"}`}
		firstPark := runner.Receipt{
			AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1", Permission: permission,
		}
		initial := &fakeRunner{receipt: firstPark, events: append(append([]runner.Event{}, turnOne...),
			event(4, runner.EventNeedsInput, "permission required", ""),
		)}
		firstOut, err := New(testConfig(initial, 1, "")).Turn(context.Background(), testRun(), backend.Input{
			Messages: []backend.Message{{Role: backend.RoleUser, Content: "run the command"}},
		}, &recordingSink{})
		if err != nil {
			t.Fatalf("initial permission park: %v", err)
		}
		firstState := parkedState(t, firstOut)
		turn, tools := codexTimingSnapshot(t, firstState)
		if turn != "turn-1" || tools != int64(400*time.Millisecond) {
			t.Fatalf("permission timing = %q / %s, want turn-1 and 400ms", turn, time.Duration(tools))
		}

		tail := []runner.Event{
			event(5, runner.EventProgress, "permission granted", ""),
			event(6, runner.EventProgress, "", `{"item":{"id":"tool-2","type":"commandExecution","command":"second","status":"inProgress"}}`),
			event(7, runner.EventProgress, "", `{"item":{"id":"tool-2","type":"commandExecution","command":"second","status":"completed","durationMs":500}}`),
			event(8, runner.EventProgress, "completed", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","durationMs":1500}}`),
			event(9, runner.EventProgress, "", `{"item":{"id":"msg-1","type":"agentMessage","text":"Done.","phase":"final_answer"}}`),
			event(10, runner.EventCompleted, "", ""),
		}
		events := append(append([]runner.Event{}, turnOne...), event(4, runner.EventNeedsInput, "permission required", ""))
		events = append(events, tail...)
		resumeRunner := &fakeRunner{
			receipt: completedReceipt, inspectReceipts: []runner.Receipt{firstPark, completedReceipt},
			resumeReceipt: &running, events: events,
		}
		sink := &recordingSink{}
		out, err := New(testConfig(resumeRunner, 1, "sess-1")).Continue(context.Background(), testRun(), backend.Answer{
			State: firstOut.Parked.State, Decided: true, Approved: true,
		}, sink)
		if err != nil {
			t.Fatalf("approve and continue: %v", err)
		}
		if out.Status != backend.StatusCompleted || len(sink.assistants) != 1 || sink.assistants[0].Duration != 600*time.Millisecond {
			t.Fatalf("completed timing = status %s, assistants %+v; want 600ms after both same-turn tools", out.Status, sink.assistants)
		}
	})
}

func codexTimingSnapshot(t *testing.T, state State) (string, int64) {
	t.Helper()
	var snapshot dispatch.Snapshot
	if err := json.Unmarshal(state.Snapshot, &snapshot); err != nil {
		t.Fatalf("decode dispatch snapshot: %v", err)
	}
	var normalized struct {
		CodexTurnID    string `json:"codexTurnID"`
		ToolDurationNS int64  `json:"toolDurationNS"`
	}
	if err := json.Unmarshal(snapshot.State, &normalized); err != nil {
		t.Fatalf("decode normalizer snapshot: %v", err)
	}
	return normalized.CodexTurnID, normalized.ToolDurationNS
}

type quietRunner struct{ fakeRunner }

func (q *quietRunner) Events(context.Context, string, uint64) (dispatch.Stream, error) {
	return &sliceStream{}, nil
}

type legacySnapshotRunner struct {
	fakeRunner
	zeroCursorReads int
}

func (r *legacySnapshotRunner) Events(ctx context.Context, attemptID string, after uint64) (dispatch.Stream, error) {
	if after == 0 {
		r.zeroCursorReads++
		return &erroringStream{err: &runner.Error{Code: runner.ErrorCursorExpired, SnapshotRequired: true, Message: "expired prefix"}}, nil
	}
	return r.fakeRunner.Events(ctx, attemptID, after)
}

func TestLegacyResumeFailsWhenTheSavedPrefixIsIncomplete(t *testing.T) {
	state, err := json.Marshal(State{TaskID: "task-1", AttemptID: "run-1", Epoch: 1, Cursor: 2})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted},
		events:  []runner.Event{event(2, runner.EventProgress, "partial", "")},
	}
	sink := &recordingSink{}
	if _, err := New(testConfig(f, 1, "")).Continue(context.Background(), testRun(), backend.Answer{State: state}, sink); err == nil || !strings.Contains(err.Error(), "expected event 1, got cursor 2") {
		t.Fatalf("Continue error = %v, want an explicit incomplete-prefix error", err)
	}
	if len(sink.deltas) != 0 {
		t.Fatalf("incomplete prefix emitted %q before refusing recovery", sink.deltas)
	}
}

func TestContinueKeepsObservedEpochWhenConfigIsAheadAcrossPermissionParks(t *testing.T) {
	firstState, err := marshalStateWithSnapshot(t, State{
		TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", Cursor: 10,
		PermissionID: "permission-1", ParkType: parkTypePermission,
	})
	if err != nil {
		t.Fatal(err)
	}
	resumeAndPark := func(startCursor uint64, state json.RawMessage) (*fakeRunner, backend.Outcome, *recordingSink) {
		t.Helper()
		f := &fakeRunner{
			receipt: runner.Receipt{
				AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
				Permission: &runner.PermissionRequest{ID: "permission-1", Tool: "Bash", Input: `{"command":"printf ok"}`},
			},
			events: []runner.Event{
				event(startCursor+1, runner.EventProgress, "permission granted", ""),
				event(startCursor+2, runner.EventNeedsInput, "another permission", ""),
			},
		}
		sink := &recordingSink{}
		cfg := testConfig(f, 2, "sess-1")
		out, err := New(cfg).Continue(context.Background(), testRun(), backend.Answer{
			State: state, Decided: true, Approved: true,
		}, sink)
		if err != nil {
			t.Fatalf("Continue from cursor %d: %v", startCursor, err)
		}
		return f, out, sink
	}

	first, firstOut, firstSink := resumeAndPark(10, firstState)
	if len(first.resumes) != 1 || first.resumes[0].AttemptEpoch != 1 {
		t.Fatalf("first resume = %+v, want runner attempt epoch 1", first.resumes)
	}
	firstPark := parkedState(t, firstOut)
	if firstPark.Epoch != 1 || firstPark.Cursor != 12 {
		t.Fatalf("first re-park state = %+v, want observed epoch 1 and cursor 12", firstPark)
	}
	if len(firstSink.deltas) != 0 {
		t.Fatalf("permission lifecycle deltas = %q, want no assistant transcript text", firstSink.deltas)
	}

	second, secondOut, secondSink := resumeAndPark(firstPark.Cursor, firstOut.Parked.State)
	if len(second.resumes) != 1 || second.resumes[0].AttemptEpoch != 1 {
		t.Fatalf("second resume = %+v, want the same runner attempt epoch 1", second.resumes)
	}
	secondPark := parkedState(t, secondOut)
	if secondPark.Epoch != 1 {
		t.Fatalf("second re-park epoch = %d, want observed runner epoch 1", secondPark.Epoch)
	}
	if len(secondSink.deltas) != 0 {
		t.Fatalf("permission lifecycle deltas = %q, want no assistant transcript text", secondSink.deltas)
	}
}

func marshalStateWithSnapshot(t *testing.T, state State) (json.RawMessage, error) {
	t.Helper()
	snapshot, err := json.Marshal(dispatch.Snapshot{
		Position: state.position(), Usage: dispatchUsage(state.Spent),
		State: json.RawMessage(`{"version":1}`),
	})
	if err != nil {
		return nil, err
	}
	state.Snapshot = snapshot
	return json.Marshal(state)
}

// A recovery resume decided nothing, and it must not decide now either. The
// provider died somewhere between the harness asking and the inbox recording
// it; the call is still open on the runner and the person still gets to
// answer. It used to be denied on recovery — "nothing was approved, so no" —
// which threw away the prompt a person was about to see. A re-join that finds
// the attempt waiting parks it AGAIN, with the same call, and sends nothing.
func TestUndecidedRecoveryOnAPermissionParkParksAgain(t *testing.T) {
	parked := runner.Receipt{
		AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "sess-1",
		Permission: &runner.PermissionRequest{ID: "permission-9", Tool: "Bash", Input: `{"command":"ls"}`},
	}
	f := &fakeRunner{receipt: parked, phases: []runner.Phase{runner.PhaseNeedsInput}}
	b := New(testConfig(f, 1, "sess-1"))
	state, err := json.Marshal(State{TaskID: "task-1", AttemptID: "run-1", Epoch: 1, SessionID: "sess-1", PermissionID: "permission-9"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := b.Continue(context.Background(), testRun(), backend.Answer{State: state}, &recordingSink{})
	if err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if len(f.resumes) != 0 {
		t.Fatalf("resumes = %d; a recovery must not answer a prompt nobody has seen", len(f.resumes))
	}
	if out.Status != backend.StatusParked || out.Parked == nil || out.Parked.Tool != "Bash" {
		t.Fatalf("outcome = %+v, want the same permission park again", out)
	}
	var again State
	if err := json.Unmarshal(out.Parked.State, &again); err != nil || again.PermissionID != "permission-9" || again.ClarificationID != "" {
		t.Fatalf("re-parked state = %+v (%v); the verdict must still address permission-9", again, err)
	}
}

// The provider is the side that CAN answer a permission prompt, so every start
// it dispatches opts into the round-trip. Without it the runner has nobody to
// ask and denies silently — which is the bug this exists to fix.
func TestStartOptsIntoThePermissionRoundTrip(t *testing.T) {
	f := &fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, SessionID: "sess-1"},
		events:  []runner.Event{event(1, runner.EventCompleted, "done", "")},
	}
	b := New(testConfig(f, 1, ""))
	if _, err := b.Turn(context.Background(), testRun(), backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "look something up"}},
	}, &recordingSink{}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(f.starts) != 1 {
		t.Fatalf("starts = %d, want 1", len(f.starts))
	}
	if !f.starts[0].AskPermission {
		t.Fatal("the start did not ask for the permission round-trip; prompts would be denied with nobody to see them")
	}
}

func TestInitialStateCarriesCancellationCoordinatesWithoutCredential(t *testing.T) {
	cfg := testConfig(&fakeRunner{}, 7, "native-session")
	cfg.TaskID = "agent-scout-uid-chat"
	cfg.AttemptID = "run-before-dispatch"
	cfg.BackendKey = "cluster/edge/codex"
	b := New(cfg)

	raw, err := b.InitialState()
	if err != nil {
		t.Fatalf("InitialState: %v", err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode InitialState: %v", err)
	}
	if state.TaskID != cfg.TaskID || state.AttemptID != cfg.AttemptID || state.Epoch != cfg.Epoch || state.SessionID != cfg.SessionID || state.BackendKey != cfg.BackendKey {
		t.Fatalf("initial state = %+v; want the configured cancellation coordinates", state)
	}
	if len(state.Snapshot) != 0 {
		t.Fatalf("initial state unexpectedly claims to be a resumable event snapshot: %s", state.Snapshot)
	}
	if strings.Contains(string(raw), cfg.Credential.Value) || strings.Contains(string(raw), cfg.WorkspaceID) {
		t.Fatal("initial state contains credential or workspace data")
	}
}
