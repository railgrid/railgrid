/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package dispatch

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
)

// ---- a runner that answers from a script ------------------------------------

// fakeRunner is one attempt's worth of scripted protocol. It records what was
// sent, which is where the request assertions look.
type fakeRunner struct {
	mu sync.Mutex

	starts  []runner.StartRequest
	resumes []runner.ResumeRequest
	cancels []runner.CancelRequest

	// receipt is what every call answers with, and phases (when non-empty) is
	// the sequence Inspect walks through, one per call, holding the last.
	receipt runner.Receipt
	phases  []runner.Phase

	// events is the stream the lifecycle tails. It ends in io.EOF, which is how
	// the runner ends a quiet stream.
	events []runner.Event
	// gapAt, when set, makes the first Events call from a cursor below it fail
	// with cursor_expired, so the reconcile path is exercised.
	gapAt    uint64
	gapFired bool

	startErr  error
	cancelErr error
	inspects  int
	streams   []uint64
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
	if f.cancelErr != nil {
		return runner.Receipt{}, f.cancelErr
	}
	return f.current(), nil
}

func (f *fakeRunner) Inspect(_ context.Context, _ string) (runner.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspects++
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
	if len(f.events) > 0 && r.Cursor == 0 {
		r.Cursor = f.events[len(f.events)-1].Cursor
	}
	return r
}

func (f *fakeRunner) Events(_ context.Context, _ string, after uint64) (Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streams = append(f.streams, after)
	if f.gapAt > 0 && !f.gapFired && after < f.gapAt {
		f.gapFired = true
		return &sliceStream{err: &runner.Error{Code: runner.ErrorCursorExpired, SnapshotRequired: true, Message: "cursor expired"}}, nil
	}
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
	err    error
}

func (s *sliceStream) Next(ctx context.Context) (runner.Event, error) {
	if err := ctx.Err(); err != nil {
		return runner.Event{}, err
	}
	if s.err != nil {
		return runner.Event{}, s.err
	}
	if s.i >= len(s.events) {
		return runner.Event{}, io.EOF
	}
	e := s.events[s.i]
	s.i++
	return e, nil
}

func (s *sliceStream) Close() error { return nil }

// ---- an observer that records ----------------------------------------------

type recorder struct {
	Noop
	text        []string
	toolStarts  []string
	toolEnds    []ToolResult
	checkpoints []Snapshot
	assistants  []AssistantResult
	abort       error
}

func (r *recorder) Text(delta string)            { r.text = append(r.text, delta) }
func (r *recorder) ToolStart(id, name, _ string) { r.toolStarts = append(r.toolStarts, id+":"+name) }
func (r *recorder) ToolEnd(t ToolResult)         { r.toolEnds = append(r.toolEnds, t) }
func (r *recorder) Checkpoint(s Snapshot)        { r.checkpoints = append(r.checkpoints, s) }
func (r *recorder) Assistant(a AssistantResult)  { r.assistants = append(r.assistants, a) }
func (r *recorder) Aborted(context.Context) error {
	return r.abort
}

func event(cursor uint64, typ, message, data string) runner.Event {
	e := runner.Event{Cursor: cursor, Type: typ, Message: message, AttemptEpoch: 1}
	if data != "" {
		e.Data = json.RawMessage(data)
	}
	return e
}

func receipt(phase runner.Phase) runner.Receipt {
	return runner.Receipt{ProtocolVersion: runner.ProtocolVersion, TaskID: "task-1", AttemptID: "attempt-1", AttemptEpoch: 1, Phase: phase, SessionID: "sess-1"}
}

func ctxWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// ---- the rules --------------------------------------------------------------

// TestFollowReadsTextToolsAndCostOffTheStream: everything a caller gets back
// comes from the events, and the terminal receipt supplies only the phase.
func TestFollowReadsTextToolsAndCostOffTheStream(t *testing.T) {
	f := &fakeRunner{
		receipt: receipt(runner.PhaseCompleted),
		events: []runner.Event{
			event(1, runner.EventStarted, "", ""),
			event(2, runner.EventProgress, "Looking at the repo.", ""),
			event(3, runner.EventProgress, "", `{"item":{"id":"c1","type":"command_execution","command":"ls","status":"in_progress"}}`),
			event(4, runner.EventProgress, "", `{"item":{"id":"c1","type":"command_execution","command":"ls","status":"completed","output":"README.md"}}`),
			event(5, runner.EventProgress, "", `{"type":"result","result":"Done: one file.","total_cost_usd":0.012,"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":50}}`),
			event(6, runner.EventCompleted, "", ""),
		},
	}
	rec := &recorder{}
	out, err := Start(ctxWithTimeout(t), f, runner.StartRequest{TaskID: "task-1", AttemptID: "attempt-1", AttemptEpoch: 1}, rec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !out.Completed() {
		t.Fatalf("phase = %s, want completed", out.Receipt.Phase)
	}
	if got := strings.Join(rec.text, "|"); got != "Looking at the repo." {
		t.Errorf("text streamed = %q", got)
	}
	if len(rec.toolStarts) != 1 || rec.toolStarts[0] != "c1:command_execution" || len(rec.toolEnds) != 1 || rec.toolEnds[0].Result != "README.md" {
		t.Errorf("tools = %v / %+v", rec.toolStarts, rec.toolEnds)
	}
	if out.Summary.Final != "Done: one file." {
		t.Errorf("final = %q", out.Summary.Final)
	}
	if u := out.Summary.Usage; u.InputTokens != 150 || u.OutputTokens != 20 || u.CostMicros != 12000 {
		t.Errorf("usage = %+v", u)
	}
	if out.Position.Cursor != 6 || out.Position.SessionID != "sess-1" {
		t.Errorf("position = %+v", out.Position)
	}
}

func TestFollowNormalizesCodexAppServerEvents(t *testing.T) {
	const answer = "Here is the result.\n"
	f := &fakeRunner{
		receipt: receipt(runner.PhaseCompleted),
		events: []runner.Event{
			event(1, runner.EventStarted, "", ""),
			event(2, runner.EventProgress, "Codex session ready", `{"thread":{"id":"thread-1"}}`),
			event(3, runner.EventProgress, "", `{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress"}}`),
			event(4, runner.EventProgress, answer, `{"delta":"Here is the result.\n"}`),
			event(5, runner.EventProgress, "", `{"item":{"id":"exec-1","type":"commandExecution","command":"echo ok","status":"inProgress"}}`),
			event(6, runner.EventProgress, "", `{"item":{"id":"exec-1","type":"commandExecution","command":"echo ok","status":"completed","aggregatedOutput":"ok\n","durationMs":200}}`),
			event(7, runner.EventProgress, "", `{"tokenUsage":{"last":{"inputTokens":40,"outputTokens":4},"total":{"inputTokens":100,"outputTokens":10}}}`),
			event(8, runner.EventProgress, "", `{"tokenUsage":{"last":{"inputTokens":40,"outputTokens":4},"total":{"inputTokens":100,"outputTokens":10}}}`),
			event(9, runner.EventProgress, "", `{"tokenUsage":{"last":{"inputTokens":50,"outputTokens":5},"total":{"inputTokens":150,"outputTokens":15}}}`),
			event(10, runner.EventProgress, "", `{"tokenUsage":{"last":{"inputTokens":50,"outputTokens":5},"total":{"inputTokens":150,"outputTokens":15}}}`),
			event(11, runner.EventProgress, "", `{"item":{"id":"msg-1","type":"agentMessage","text":"Here is the result.\n","phase":"final_answer"}}`),
			event(12, runner.EventProgress, "completed", `{"turn":{"id":"turn-1","status":"completed","durationMs":700}}`),
			event(13, runner.EventCompleted, "", ""),
		},
	}
	rec := &recorder{}
	out, err := Follow(ctxWithTimeout(t), f, receipt(runner.PhaseCompleted), Position{AttemptID: "attempt-1"}, rec)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if got := strings.Join(rec.text, ""); got != answer {
		t.Errorf("streamed text = %q, want byte-exact delta %q", got, answer)
	}
	if out.Summary.Text != strings.TrimSpace(answer) || out.Summary.Final != strings.TrimSpace(answer) {
		t.Errorf("summary = %+v, want one canonical answer", out.Summary)
	}
	if got := out.Summary.Usage; got.InputTokens != 90 || got.OutputTokens != 9 || got.CostMicros != 0 {
		t.Errorf("Codex usage = %+v, want two distinct model-call deltas", got)
	}
	if len(rec.toolStarts) != 1 || len(rec.toolEnds) != 1 || rec.toolEnds[0].Duration != 200*time.Millisecond || rec.toolEnds[0].Result != "ok\n" {
		t.Errorf("tool events = %v / %+v", rec.toolStarts, rec.toolEnds)
	}
	if len(rec.assistants) != 1 || rec.assistants[0].Content != strings.TrimSpace(answer) || !rec.assistants[0].Complete || rec.assistants[0].Duration != 500*time.Millisecond {
		t.Errorf("assistant timing = %+v, want 500ms after subtracting tool time", rec.assistants)
	}
}

func TestToolItemNonzeroExitIsFailure(t *testing.T) {
	item, ok := toolItem(json.RawMessage(`{"item":{"id":"exec-2","type":"commandExecution","status":"completed","exitCode":1,"aggregatedOutput":"permission denied"}}`))
	if !ok || !item.Done || !item.Failed || item.Result != "permission denied" {
		t.Fatalf("tool item = %+v, %v; want a completed failed call with its output", item, ok)
	}
}

func TestResumeSnapshotPreservesTranscriptAndBillsOnlyNewUsage(t *testing.T) {
	f := &fakeRunner{
		receipt: runner.Receipt{ProtocolVersion: runner.ProtocolVersion, TaskID: "task-1", AttemptID: "attempt-1", AttemptEpoch: 1, Phase: runner.PhaseNeedsInput, SessionID: "session-1"},
		events: []runner.Event{
			event(1, runner.EventProgress, "before park", `{"usage":{"input_tokens":100}}`),
			event(2, runner.EventNeedsInput, "", `{"clarification":{"id":"q-1","text":"Continue?"}}`),
		},
	}
	first, err := FollowSnapshot(ctxWithTimeout(t), f, f.receipt, Snapshot{Position: Position{TaskID: "task-1", AttemptID: "attempt-1", Epoch: 1}}, &recorder{})
	if err != nil {
		t.Fatalf("initial FollowSnapshot: %v", err)
	}
	if first.Parked == nil || first.Snapshot.Position.Cursor != 2 || first.Snapshot.Usage.InputTokens != 100 {
		t.Fatalf("parked result = %+v, want cursor 2 and saved usage 100", first)
	}
	f.events = append(f.events,
		event(3, runner.EventProgress, "after park", `{"usage":{"input_tokens":25}}`),
		event(4, runner.EventCompleted, "", ""),
	)
	f.receipt = runner.Receipt{ProtocolVersion: runner.ProtocolVersion, TaskID: "task-1", AttemptID: "attempt-1", AttemptEpoch: 2, Phase: runner.PhaseCompleted, SessionID: "session-forked"}
	out, err := ResumeSnapshot(ctxWithTimeout(t), f, runner.ResumeRequest{TaskID: "task-1", AttemptID: "attempt-1", AttemptEpoch: 1, ClarificationID: "q-1"}, first.Snapshot, &recorder{})
	if err != nil {
		t.Fatalf("ResumeSnapshot: %v", err)
	}
	if out.Summary.Text != "before park\nafter park" || out.Position.SessionID != "session-forked" {
		t.Errorf("resumed text=%q session=%q; want accumulated text and authoritative forked session", out.Summary.Text, out.Position.SessionID)
	}
	if out.Summary.Usage.InputTokens != 25 || out.Snapshot.Usage.InputTokens != 125 {
		t.Errorf("resumed usage = delta %+v, snapshot %+v; want 25 new and 125 cumulative", out.Summary.Usage, out.Snapshot.Usage)
	}
}

func TestRejoinSnapshotRejectsUnavailableHistory(t *testing.T) {
	f := &fakeRunner{
		receipt: receipt(runner.PhaseCompleted),
		gapAt:   3,
		events: []runner.Event{
			event(1, runner.EventProgress, "saved", ""),
			event(2, runner.EventProgress, "missing", ""),
			event(3, runner.EventCompleted, "", ""),
		},
	}
	s := newStream(Noop{}, 0)
	s.observe(event(1, runner.EventProgress, "saved", ""))
	snap := s.snapshot(Position{TaskID: "task-1", AttemptID: "attempt-1", Epoch: 1})
	out, err := RejoinSnapshot(ctxWithTimeout(t), f, snap, &recorder{})
	if err == nil || !strings.Contains(err.Error(), "event history is incomplete after saved cursor 1") {
		t.Fatalf("RejoinSnapshot error = %v, outcome %+v; want an explicit incomplete-history error", err, out)
	}
}

// TestTerminalEventIsNotBelievedOverTheReceipt: the runner publishes the
// terminal event before it has settled the attempt, so the first Inspect after
// it can still say running. The lifecycle must ask again, not conclude.
func TestTerminalEventIsNotBelievedOverTheReceipt(t *testing.T) {
	f := &fakeRunner{
		receipt: receipt(runner.PhaseRunning),
		// Inspect walks: running (not settled yet), then completed.
		phases: []runner.Phase{runner.PhaseRunning, runner.PhaseCompleted},
		events: []runner.Event{
			event(1, runner.EventProgress, "working", ""),
			event(2, runner.EventCompleted, "", ""),
		},
	}
	out, err := Follow(ctxWithTimeout(t), f, receipt(runner.PhaseRunning), Position{}, Noop{})
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if !out.Completed() {
		t.Fatalf("phase = %s, want completed after the receipt settled", out.Receipt.Phase)
	}
	if f.inspects < 2 {
		t.Errorf("inspects = %d, want the receipt re-read after the unsettled one", f.inspects)
	}
}

// TestCursorGapReconcilesThroughInspect: a cursor the runner dropped is not a
// failure of the turn; the view is restored from the receipt and tailing
// continues from the receipt's cursor.
func TestCursorGapReconcilesThroughInspect(t *testing.T) {
	f := &fakeRunner{
		receipt: receipt(runner.PhaseRunning),
		phases:  []runner.Phase{runner.PhaseRunning, runner.PhaseCompleted},
		gapAt:   3,
		events: []runner.Event{
			event(3, runner.EventProgress, "after the gap", ""),
			event(4, runner.EventCompleted, "", ""),
		},
	}
	f.receipt.Cursor = 2
	rec := &recorder{}
	out, err := Follow(ctxWithTimeout(t), f, receipt(runner.PhaseRunning), Position{}, rec)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if !out.Completed() {
		t.Fatalf("phase = %s", out.Receipt.Phase)
	}
	if strings.Join(rec.text, "") != "after the gap" {
		t.Errorf("text after reconcile = %q", rec.text)
	}
}

// TestQuestionParksWithTheQuestion: needs_input with a clarification is a park
// a caller answers with words, and the position carries the id to echo.
func TestQuestionParksWithTheQuestion(t *testing.T) {
	r := receipt(runner.PhaseNeedsInput)
	r.Clarification = &runner.Clarification{ID: "q-1", Text: "Which branch?"}
	f := &fakeRunner{receipt: r, events: []runner.Event{
		event(1, runner.EventProgress, "I need to ask.", ""),
		event(2, runner.EventNeedsInput, "", `{"clarification":{"id":"q-1","text":"Which branch?"}}`),
	}}
	out, err := Follow(ctxWithTimeout(t), f, receipt(runner.PhaseRunning), Position{}, Noop{})
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if out.Parked == nil || out.Parked.Question == nil || out.Parked.Question.Text != "Which branch?" || out.Parked.Permission != nil {
		t.Fatalf("parked = %+v", out.Parked)
	}
	if out.Position.ClarificationID != "q-1" || out.Position.PermissionID != "" {
		t.Errorf("position ids = %+v", out.Position)
	}
	if out.Terminal() {
		t.Error("a park is not terminal")
	}
}

// TestPermissionParksWithTheCallAndResumesWithAVerdict: a permission prompt is
// the other park, and ResumeRequest answers it through the permission field
// alone, never claiming to be a question as well.
func TestPermissionParksWithTheCallAndResumesWithAVerdict(t *testing.T) {
	r := receipt(runner.PhaseNeedsInput)
	r.Permission = &runner.PermissionRequest{ID: "p-1", Tool: "Bash", Input: `{"command":"rm -rf build"}`}
	// A stale question id on the position must not leak onto a permission
	// resume.
	pos := Position{AttemptID: "attempt-1", ClarificationID: "old-q"}
	f := &fakeRunner{receipt: r, events: []runner.Event{event(1, runner.EventNeedsInput, "", "")}}
	out, err := Rejoin(ctxWithTimeout(t), f, pos, Noop{})
	if err != nil {
		t.Fatalf("Rejoin: %v", err)
	}
	if out.Parked == nil || out.Parked.Permission == nil || out.Parked.Permission.Tool != "Bash" || out.Parked.Question != nil {
		t.Fatalf("parked = %+v", out.Parked)
	}
	if out.Position.PermissionID != "p-1" || out.Position.ClarificationID != "" {
		t.Errorf("position ids = %+v", out.Position)
	}
	req := ResumeRequest("req-1", out.Receipt, out.Position, Answer{Allow: true, Resolution: "go ahead"}, json.RawMessage(`{"provenance":{}}`), &runner.HarnessCredential{Kind: "claude-oauth", Value: "x"})
	if req.PermissionID != "p-1" || req.PermissionDecision != runner.PermissionAllow || req.ClarificationID != "" || req.Resolution != "go ahead" {
		t.Errorf("resume = %+v", req)
	}
	if req.SessionID != "sess-1" || req.AttemptEpoch != 1 || req.TaskID != "task-1" || req.HarnessCredential == nil {
		t.Errorf("resume coordinates = %+v", req)
	}
	denied := ResumeRequest("req-2", out.Receipt, out.Position, Answer{Allow: false}, nil, nil)
	if denied.PermissionDecision != runner.PermissionDeny {
		t.Errorf("deny = %+v", denied)
	}
}

// TestRejoinTailsFromTheCursorInsteadOfReStreaming: a caller that re-joins an
// attempt has already recorded everything up to its cursor, and must not be
// shown it again.
func TestRejoinTailsFromTheCursorInsteadOfReStreaming(t *testing.T) {
	f := &fakeRunner{
		receipt: receipt(runner.PhaseRunning),
		phases:  []runner.Phase{runner.PhaseRunning, runner.PhaseRunning, runner.PhaseCompleted},
		events: []runner.Event{
			event(1, runner.EventProgress, "old", ""),
			event(2, runner.EventProgress, "old too", ""),
			event(3, runner.EventProgress, "new", ""),
			event(4, runner.EventCompleted, "", ""),
		},
	}
	rec := &recorder{}
	out, err := Rejoin(ctxWithTimeout(t), f, Position{AttemptID: "attempt-1", Cursor: 2}, rec)
	if err != nil {
		t.Fatalf("Rejoin: %v", err)
	}
	if !out.Completed() {
		t.Fatalf("phase = %s", out.Receipt.Phase)
	}
	if strings.Join(rec.text, "|") != "new" {
		t.Errorf("re-streamed text = %q, want only what came after the cursor", rec.text)
	}
	if len(f.streams) == 0 || f.streams[0] != 2 {
		t.Errorf("first stream asked from cursor %v, want 2", f.streams)
	}
}

// TestRejoinReturnsAFinishedAttemptWithoutDispatching: an attempt that ended
// while nobody was watching is answered by its receipt; nothing is sent.
func TestRejoinReturnsAFinishedAttemptWithoutDispatching(t *testing.T) {
	f := &fakeRunner{receipt: receipt(runner.PhaseFailed)}
	f.receipt.LastError = &runner.Error{Code: "harness_failed", Message: "it broke"}
	out, err := Rejoin(ctxWithTimeout(t), f, Position{AttemptID: "attempt-1"}, Noop{})
	if err == nil || !strings.Contains(err.Error(), "it broke") {
		t.Fatalf("err = %v, want the runner's words", err)
	}
	if !out.Terminal() || out.Receipt.Phase != runner.PhaseFailed {
		t.Errorf("outcome = %+v", out.Receipt.Phase)
	}
	if len(f.starts)+len(f.resumes) != 0 || len(f.streams) != 0 {
		t.Error("a finished attempt was dispatched or streamed")
	}
}

// TestCancelIsNotDoneUntilObserved: cancelling posts once and then reads the
// receipt until it is terminal; a nil receipt means there was nothing to stop.
func TestCancelIsNotDoneUntilObserved(t *testing.T) {
	f := &fakeRunner{
		receipt: receipt(runner.PhaseCancelling),
		phases:  []runner.Phase{runner.PhaseCancelling, runner.PhaseCancelled},
	}
	got, err := Cancel(ctxWithTimeout(t), f, runner.CancelRequest{TaskID: "task-1", AttemptID: "attempt-1", AttemptEpoch: 1})
	if err != nil || got == nil || got.Phase != runner.PhaseCancelled {
		t.Fatalf("cancel = %+v, %v", got, err)
	}
	if len(f.cancels) != 1 || f.inspects < 1 {
		t.Errorf("cancels=%d inspects=%d", len(f.cancels), f.inspects)
	}

	nothing := &fakeRunner{cancelErr: &runner.Error{Code: runner.ErrorUnavailable, Message: "attempt not found"}}
	got, err = Cancel(ctxWithTimeout(t), nothing, runner.CancelRequest{AttemptID: "gone"})
	if err != nil || got != nil {
		t.Errorf("nothing to cancel = %+v, %v; want nil, nil", got, err)
	}
}

// TestAbortedStopsTheFollow: the caller's own cancel flag is honoured between
// streams and ends the follow with that error.
func TestAbortedStopsTheFollow(t *testing.T) {
	f := &fakeRunner{receipt: receipt(runner.PhaseRunning), events: []runner.Event{event(1, runner.EventProgress, "x", "")}}
	stop := errors.New("operator cancelled")
	_, err := Follow(ctxWithTimeout(t), f, receipt(runner.PhaseRunning), Position{}, &recorder{abort: stop})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the observer's abort", err)
	}
}

// TestOpenToolsAreClosedWhenTheAttemptEnds: a call the stream never reported
// finishing gets a failed ToolEnd rather than staying open forever.
func TestOpenToolsAreClosedWhenTheAttemptEnds(t *testing.T) {
	f := &fakeRunner{receipt: receipt(runner.PhaseFailed), events: []runner.Event{
		event(1, runner.EventProgress, "", `{"item":{"id":"c1","type":"command_execution","command":"make","status":"in_progress"}}`),
		event(2, runner.EventFailed, "", ""),
	}}
	rec := &recorder{}
	_, _ = Follow(ctxWithTimeout(t), f, receipt(runner.PhaseRunning), Position{}, rec)
	if len(rec.toolEnds) != 1 || !rec.toolEnds[0].Failed || rec.toolEnds[0].ID != "c1" {
		t.Errorf("tool ends = %+v", rec.toolEnds)
	}
}

// TestErrorTaxonomy pins the classifications the dispatch fences rest on.
func TestErrorTaxonomy(t *testing.T) {
	unknown := &runner.Error{Code: runner.ErrorUnavailable, Message: "attempt not found"}
	if !IsAttemptUnknown(unknown) {
		t.Error("an authenticated 'attempt not found' must permit a first start")
	}
	if IsAttemptUnknown(&runner.Error{Code: runner.ErrorUnavailable, Retryable: true}) {
		t.Error("a retryable unavailable may be hiding accepted work")
	}
	if IsAttemptUnknown(errors.New("dial tcp: timeout")) {
		t.Error("a transport failure is never 'unknown'")
	}
	if !IsSnapshotRequired(&runner.Error{Code: runner.ErrorCursorExpired}) {
		t.Error("cursor_expired is a snapshot requirement")
	}
	if !IsUnstartable(&runner.Error{Code: runner.ErrorUnsupportedVersion}) || IsUnstartable(&runner.Error{Code: runner.ErrorInvalidRequest}) {
		t.Error("only unsupported capability/version are unstartable")
	}
	done := receipt(runner.PhaseCompleted)
	if !IsNothingToCancel(&runner.Error{Code: runner.ErrorStaleAttempt, Receipt: &done}) {
		t.Error("a stale cancel against a finished attempt has nothing to cancel")
	}
	wrapped := Describe("starting", unknown)
	var back *runner.Error
	if !errors.As(wrapped, &back) || back.Code != runner.ErrorUnavailable {
		t.Error("Describe must keep the protocol error reachable")
	}
}
