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

	// events is the stream the backend tails. It ends in io.EOF, which is how the
	// runner ends a quiet stream.
	events []runner.Event

	startErr error
	inspects int
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
	return f.current(), nil
}

func (f *fakeRunner) current() runner.Receipt {
	r := f.receipt
	if len(f.events) > 0 {
		r.Cursor = f.events[len(f.events)-1].Cursor
	}
	return r
}

func (f *fakeRunner) Events(_ context.Context, _ string, after uint64) (Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []runner.Event
	for _, e := range f.events {
		if e.Cursor > after {
			out = append(out, e)
		}
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

// ---- a sink that records ----------------------------------------------------

type recordingSink struct {
	deltas      []string
	toolStarts  []string
	toolEnds    []backend.ToolEvent
	checkpoints []json.RawMessage
	abort       error
}

func (r *recordingSink) Delta(text string)                  { r.deltas = append(r.deltas, text) }
func (r *recordingSink) Assistant(backend.AssistantMessage) {}
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
		Dispatcher:      f,
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

// A cursor the runner no longer holds is reconciled with Inspect and the turn
// carries on, rather than failing over a dropped event.
func TestCursorGapReconcilesInsteadOfFailing(t *testing.T) {
	f := &gappyRunner{fakeRunner: fakeRunner{
		receipt: runner.Receipt{AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, Cursor: 9},
		phases:  []runner.Phase{runner.PhaseCompleted},
	}}
	cfg := testConfig(&f.fakeRunner, 1, "")
	cfg.Dispatcher = f
	out, err := New(cfg).Turn(context.Background(), testRun(),
		backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "hi"}}}, &recordingSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if out.Status != backend.StatusCompleted {
		t.Fatalf("status = %s, want the reconciled completion", out.Status)
	}
}

// gappyRunner answers the first stream with a cursor-expired error carrying
// SnapshotRequired, the way the shared client reports a gap.
type gappyRunner struct {
	fakeRunner
	served bool
}

func (g *gappyRunner) Events(ctx context.Context, attemptID string, after uint64) (Stream, error) {
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
		// A recovery resume decided nothing. A call still waiting cannot be
		// treated as approved by default.
		{"undecided", backend.Answer{}, runner.PermissionDeny, ""},
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
