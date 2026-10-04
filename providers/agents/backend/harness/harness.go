// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package harness runs a turn on a coding harness — Claude Code or Codex —
// executing on an edge, and reports it through the same seam the in-process
// model backend reports through.
//
// It is the SECOND implementation of provider-agents/backend, and the point of
// the seam is that the provider's lifecycle does not learn there are two: the
// run, its budget, its transcript, its inbox, its cancellation and its recovery
// are all still the provider's, and what changes is only where the turn happens.
//
// The protocol is runner/v1 (railgrid's pkg/runner), reached through the one
// typed client for it (pkg/runner/client) over the edges provider's published
// Service proxy. Three of its rules shape everything here:
//
//   - A conversational turn is a WORKSPACE attempt: it names a workspaceID and
//     runs in a directory the runner keeps across attempts. It pins no commit,
//     clones nothing and exports no Git result.
//
//   - Consecutive turns are one conversation because each start carries the
//     sessionID an EARLIER attempt created. The harness may FORK that session on
//     a resume, so the authoritative id is always the one on the RECEIPT — which
//     is what the provider persists and chains the next turn onto, never what was
//     sent.
//
//   - Each turn is a new dispatch of the same task, so the epoch increments:
//     session → taskID, run → attemptID, turn number → attemptEpoch. A start
//     whose epoch did not advance past the task's highest is refused with
//     stale_attempt, which is exactly the protection wanted — two replicas
//     answering the same message cannot both dispatch.
//
// The harness credential is dispatch data: it is held for the length of a turn,
// put on the start (and on every resume, because a resume may arrive after the
// runner restarted), and never persisted, never logged, and never placed in an
// event or a status field.
//
// The harness owns its tools. Input.Tools is IGNORED, with intent: the toolset
// the provider assembles — its policy, its approval gates, its audit rows — is a
// description of what an in-process loop may call, and a remote harness will
// call its own Bash and its own Edit whatever we hand it. Pretending otherwise
// would publish an approval gate that gates nothing, which is worse than not
// offering one. The gate that DOES work here is the harness's own
// request-user-input, which arrives as needs_input and parks the run.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/railgrid/railgrid/pkg/runner"
	runnerharness "github.com/railgrid/railgrid/pkg/runner/harness"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/llm"
)

// Config is everything one harness-backed turn is dispatched with. The provider
// resolves it per turn, the same way it resolves a chat model per turn: the
// credential, the epoch and the session all change from one turn to the next.
type Config struct {
	// Dispatcher reaches the enrolled runner.
	Dispatcher Dispatcher

	// TaskID identifies the CONVERSATION on the runner. Every turn of one
	// session is a new dispatch of this task.
	TaskID string
	// AttemptID identifies this turn's attempt. The provider passes the run id,
	// so an attempt and a run are the same thing on both sides of the seam.
	AttemptID string
	// Epoch is this turn's number within the task, and it must be strictly
	// greater than every earlier turn's — the runner refuses a start that does
	// not advance it (stale_attempt).
	Epoch uint64
	// SessionID is the harness session an earlier turn created, empty on the
	// first turn of a conversation.
	SessionID string

	// WorkspaceID names the directory the runner keeps for this agent across
	// attempts.
	WorkspaceID string
	// RequiredHarness is what the harness ADVERTISES (claude-code, codex), not
	// the selector a person writes. Use llm.HarnessAdvertisedName.
	RequiredHarness string
	// Model is passed through to the harness; empty leaves its default alone.
	Model string
	// Credential is the identity the turn runs as. Required.
	Credential llm.HarnessIdentity
	// Provenance is what the run is, for the approvedInput envelope the runner
	// requires on every dispatch.
	Provenance map[string]any
	// MaxDurationSeconds bounds the attempt on the runner, in addition to the
	// provider's own run timeout. Zero leaves the runner's default.
	MaxDurationSeconds int

	// NewID mints attempt and request identifiers. nil means uuid.NewString.
	NewID func() string
	// Now is the clock; nil means time.Now. Tests pin it.
	Now func() time.Time
}

// Backend executes a turn on a harness. One per turn, like the model backend,
// because what it needs is resolved per turn.
type Backend struct {
	cfg Config

	// mu guards observed only. Cancel runs on another goroutine than the turn.
	mu       sync.Mutex
	observed Observed
}

var _ backend.Backend = (*Backend)(nil)

// Observed is what a turn learned that the provider has to persist: which
// attempt ran, and — authoritatively, off the receipt — which harness session
// the next turn must chain onto.
type Observed struct {
	AttemptID string
	SessionID string
	Epoch     uint64
}

// State is this backend's resume state, opaque to the provider, persisted with
// the run and handed back as Answer.State.
//
// It is the coordinates of a conversation in flight, not a snapshot of it: the
// harness holds the transcript, and re-joining is addressing the same attempt at
// the same cursor rather than replaying anything.
type State struct {
	TaskID    string `json:"taskID"`
	AttemptID string `json:"attemptID"`
	Epoch     uint64 `json:"epoch"`
	// SessionID is the session the receipt reported, which a resume re-states
	// and the next turn chains onto.
	SessionID string `json:"sessionID,omitempty"`
	// Cursor is the last event consumed, so a resume tails from there instead of
	// re-streaming a conversation the provider has already recorded.
	Cursor uint64 `json:"cursor"`
	// ClarificationID is what a resume must echo to answer the question the
	// harness asked. Empty when the park was not a question.
	ClarificationID string `json:"clarificationID,omitempty"`
	// PermissionID is what a resume must echo to answer a PERMISSION prompt,
	// which is a different park from a question and resumes differently: the
	// harness child never stopped, the tool call is still open on it, and the
	// resume carries a verdict into that call rather than starting a turn.
	// Exactly one of ClarificationID and PermissionID is ever set.
	PermissionID string `json:"permissionID,omitempty"`
	// Spent is what this attempt had already consumed when it parked, so a
	// resumed turn bills only the delta.
	Spent backend.Cost `json:"spent,omitzero"`
}

// New builds a Backend for one turn.
func New(cfg Config) *Backend { return &Backend{cfg: cfg} }

func (b *Backend) newID() string {
	if b.cfg.NewID != nil {
		return b.cfg.NewID()
	}
	return uuid.NewString()
}

// Observed reports what the turn learned. Read after Turn or Continue returns.
func (b *Backend) Observed() Observed {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.observed
}

func (b *Backend) observe(receipt runner.Receipt) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if receipt.AttemptID != "" {
		b.observed.AttemptID = receipt.AttemptID
	}
	// The receipt's session id is AUTHORITATIVE: the harness may fork the
	// session on a resume, and chaining the next turn onto what we sent would
	// then continue a conversation nobody is having.
	if receipt.SessionID != "" {
		b.observed.SessionID = receipt.SessionID
	}
	if receipt.AttemptEpoch != 0 {
		b.observed.Epoch = receipt.AttemptEpoch
	}
}

// Turn dispatches a fresh turn and follows it to a terminal phase.
//
// in.Tools is ignored — see the package comment. in.Limits bounds the attempt on
// the runner as far as the protocol allows: one launch is one turn, so maxTurns
// is 1 and the tool-call allowance the provider computed has no counterpart.
func (b *Backend) Turn(ctx context.Context, r *backend.Run, in backend.Input, sink backend.EventSink) (backend.Outcome, error) {
	if err := b.validate(); err != nil {
		return backend.Outcome{Status: backend.StatusFailed}, err
	}
	if err := sink.Aborted(ctx); err != nil {
		return backend.Outcome{Status: backend.StatusCancelled}, err
	}
	approved, err := b.approvedInput(r)
	if err != nil {
		return backend.Outcome{Status: backend.StatusFailed}, err
	}
	credential := b.credential()
	req := runner.StartRequest{
		RequestID:    b.newID(),
		TaskID:       b.cfg.TaskID,
		AttemptID:    b.cfg.AttemptID,
		AttemptEpoch: b.cfg.Epoch,
		// A workspace attempt: a directory the runner keeps, no commit to pin.
		WorkspaceID:       b.cfg.WorkspaceID,
		SessionID:         b.cfg.SessionID,
		HarnessCredential: &credential,
		Instructions:      instructions(in.Messages),
		Model:             b.cfg.Model,
		ApprovedInput:     approved,
		RequiredHarness:   b.cfg.RequiredHarness,
		// Opt into the permission round-trip. This provider is the one that CAN
		// answer: it already has an inbox, an approval card and a resume that
		// files a verdict, so a tool call the harness's permission mode does not
		// pre-approve should reach a person rather than being denied where
		// nobody can see it. A runner too old to know the field ignores it and
		// behaves as it always did.
		AskPermission: true,
		Limits: runner.ExecutionLimits{
			// One launch is one turn in this protocol revision, and the runner
			// refuses anything else.
			MaxTurns:           1,
			MaxDurationSeconds: b.cfg.MaxDurationSeconds,
		},
	}
	if strings.TrimSpace(req.Instructions) == "" {
		return backend.Outcome{Status: backend.StatusFailed}, errors.New("the turn has nothing to ask the harness")
	}
	receipt, err := b.cfg.Dispatcher.Start(ctx, req)
	if err != nil {
		return backend.Outcome{Status: b.statusFor(ctx)}, dispatchError("starting the harness turn", err)
	}
	b.observe(receipt)
	return b.follow(ctx, receipt, sink, backend.Cost{}, 0)
}

// Continue picks a parked turn back up.
//
// Two shapes arrive here, and they are not the same call. A DECIDED answer is
// the user's resolution of a question the harness asked, and it is a resume:
// the clarification id is echoed and the resolution is handed to the session. An
// UNDECIDED answer is a run whose process died, and there is nothing to resolve
// — the attempt may well still be executing on the machine — so it is a
// RE-JOIN: reconcile with Inspect and keep tailing from the cursor, and only
// resume if the runner is genuinely waiting for input.
func (b *Backend) Continue(ctx context.Context, r *backend.Run, answer backend.Answer, sink backend.EventSink) (backend.Outcome, error) {
	if err := b.validate(); err != nil {
		return backend.Outcome{Status: backend.StatusFailed}, err
	}
	var state State
	if err := json.Unmarshal(answer.State, &state); err != nil {
		return backend.Outcome{Status: backend.StatusFailed}, fmt.Errorf("reading the turn's resume state: %w", err)
	}
	if state.AttemptID == "" {
		return backend.Outcome{Status: backend.StatusFailed}, errors.New("the turn's resume state names no attempt")
	}
	if err := sink.Aborted(ctx); err != nil {
		return backend.Outcome{Status: backend.StatusCancelled}, err
	}

	receipt, err := b.cfg.Dispatcher.Inspect(ctx, state.AttemptID)
	if err != nil {
		return backend.Outcome{Status: b.statusFor(ctx)}, dispatchError("inspecting the parked attempt", err)
	}
	b.observe(receipt)
	// Already finished while nobody was watching: the receipt is the answer, and
	// resuming it would be a second dispatch of work that is done.
	if receipt.Phase.IsTerminal() {
		return b.outcomeFor(ctx, receipt, newTurnState(sink), state.Spent)
	}
	if receipt.Phase != runner.PhaseNeedsInput {
		// Still executing. Re-join it rather than resuming: a resume would be
		// refused (the attempt is not waiting) and the work is not lost.
		return b.follow(ctx, receipt, sink, state.Spent, state.Cursor)
	}

	approved, err := b.approvedInput(r)
	if err != nil {
		return backend.Outcome{Status: backend.StatusFailed}, err
	}
	credential := b.credential()
	resume := runner.ResumeRequest{
		RequestID:    b.newID(),
		TaskID:       state.TaskID,
		AttemptID:    state.AttemptID,
		AttemptEpoch: state.Epoch,
		SessionID:    firstNonEmpty(receipt.SessionID, state.SessionID),
		// The id the harness gave the question. Echoing it is what makes the
		// answer an answer to THAT question rather than a new instruction.
		ClarificationID:   clarificationID(receipt, state),
		Resolution:        resolution(answer),
		ApprovedInput:     approved,
		HarnessCredential: &credential,
	}
	// A permission park is answered with a VERDICT on a named call, not with
	// text, and the two are mutually exclusive on the wire. Setting the verdict
	// clears the clarification id so a resume never claims to be both.
	if id := permissionID(receipt, state); id != "" {
		resume.ClarificationID = ""
		resume.PermissionID = id
		resume.PermissionDecision = runner.PermissionDeny
		// An undecided resume is a recovery, not a verdict — the run was picked
		// up after its process died. Nothing was approved, so the honest answer
		// to a call still waiting is no.
		if answer.Decided && answer.Approved {
			resume.PermissionDecision = runner.PermissionAllow
		}
		// Resolution is the person's own words and reaches the model as the
		// reason. On an approval the stock "Approved. Continue." is an
		// instruction for a NEW turn and means nothing to a waiting tool call,
		// so only a real note is passed on.
		resume.Resolution = strings.TrimSpace(answer.Note)
	}
	resumed, err := b.cfg.Dispatcher.Resume(ctx, resume)
	if err != nil {
		return backend.Outcome{Status: b.statusFor(ctx)}, dispatchError("resuming the harness turn", err)
	}
	b.observe(resumed)
	return b.follow(ctx, resumed, sink, state.Spent, state.Cursor)
}

const (
	// cancelPoll is how often the terminal receipt is re-read after a cancel was
	// posted. The runner stops a harness child and waits for it, so the gap
	// between "cancelling" and "cancelled" is real but short.
	cancelPoll = 250 * time.Millisecond

	// quietPoll spaces reconnects when a stream ends with nothing new on it.
	// Ordinarily the runner holds a stream open for 30 seconds before ending it,
	// so a reconnect costs one round trip a minute — but nothing in the protocol
	// PROMISES that, and a runner that answered every stream immediately would
	// otherwise be hammered by a tight loop.
	quietPoll = 250 * time.Millisecond
)

// Cancel stops a turn executing on the runner, and then OBSERVES that it
// stopped.
//
// The observation is the point. The provider calls this from whoever asked for
// the cancel, having already cancelled the run's context — so the local half is
// done either way, and what is left is the half that is not local: an attempt on
// somebody else's machine, which is `cancelling` until its harness child has
// actually been reaped and its terminal receipt says `cancelled`. Returning nil
// before that would report a cancellation nobody has seen, and the next thing
// that read the attempt would find it still running.
//
// The provider bounds this call; when the bound runs out the error says the
// attempt is still cancelling, which is the truth and is what gets logged.
func (b *Backend) Cancel(ctx context.Context, _ *backend.Run) error {
	if b.cfg.Dispatcher == nil {
		return errors.New("no runner dispatcher configured")
	}
	attemptID := b.Observed().AttemptID
	if attemptID == "" {
		attemptID = b.cfg.AttemptID
	}
	if attemptID == "" {
		return nil // nothing was ever dispatched
	}
	epoch := b.Observed().Epoch
	if epoch == 0 {
		epoch = b.cfg.Epoch
	}
	receipt, err := b.cfg.Dispatcher.Cancel(ctx, runner.CancelRequest{
		RequestID:    b.newID(),
		TaskID:       b.cfg.TaskID,
		AttemptID:    attemptID,
		AttemptEpoch: epoch,
	})
	if err != nil {
		// An attempt the runner has never heard of, or one already terminal, is
		// not a failure to cancel: there is nothing running.
		if terminalProtocolError(err) {
			return nil
		}
		return dispatchError("cancelling the harness turn", err)
	}
	for !receipt.Phase.IsTerminal() {
		select {
		case <-ctx.Done():
			return fmt.Errorf("attempt %s is %s, not cancelled: %w", attemptID, receipt.Phase, ctx.Err())
		case <-time.After(cancelPoll):
		}
		receipt, err = b.cfg.Dispatcher.Inspect(ctx, attemptID)
		if err != nil {
			return dispatchError("observing the cancelled attempt", err)
		}
	}
	b.observe(receipt)
	return nil
}

// follow tails an attempt's events to a terminal phase, mapping them onto the
// sink, and then reads the receipt — which is the authority on how it ended.
//
// The loop reconnects, and that is not a retry loop: the runner ends a stream
// itself after 30 seconds of silence (pkg/runner/http.go serveEvents) so a
// caller resumes from its cursor rather than holding a connection open for the
// length of a coding turn. Each reconnect is also where the two things that must
// not be skipped happen: the durable cancel flag is consulted, and the receipt is
// re-read in case the attempt ended during the quiet.
func (b *Backend) follow(ctx context.Context, receipt runner.Receipt, sink backend.EventSink, prior backend.Cost, after uint64) (backend.Outcome, error) {
	state := newTurnState(sink)
	state.cursor = after
	// A resumed or re-joined Codex thread may have emitted turn/started before
	// this follower attached. The receipt's session ID establishes that its
	// subsequent tokenUsage updates belong to an active attempt.
	state.turnStarted = receipt.SessionID != ""
	if receipt.Clarification != nil {
		state.clarification = receipt.Clarification
	}
	attemptID := receipt.AttemptID
	// caughtUp guards the ONE catch-up pass a terminal receipt is allowed: the
	// receipt's cursor can be ahead of ours when the phase changed during a quiet
	// stream, and the events in between are worth one more read. Without the flag
	// a runner whose cursor stays ahead — because the events it counted have
	// already been dropped — would be re-read forever.
	caughtUp := false
	for {
		if err := sink.Aborted(ctx); err != nil {
			return backend.Outcome{Status: backend.StatusCancelled, Usage: b.usage(state, prior)}, err
		}
		stream, err := b.cfg.Dispatcher.Events(ctx, attemptID, state.cursor)
		if err != nil {
			return backend.Outcome{Status: b.statusFor(ctx), Usage: b.usage(state, prior)},
				dispatchError("following the harness turn", err)
		}
		before := state.cursor
		terminal, quiet, err := b.drain(ctx, stream, state, prior)
		_ = stream.Close()
		switch {
		case err != nil:
			// A cursor the runner no longer holds means our view is incomplete,
			// and Inspect is the only thing that can restore it. Reconciling and
			// carrying on is the whole point of SnapshotRequired; failing the run
			// over a dropped event would throw away a turn that is still working.
			if snapshotRequired(err) {
				reconciled, ierr := b.cfg.Dispatcher.Inspect(ctx, attemptID)
				if ierr != nil {
					return backend.Outcome{Status: b.statusFor(ctx), Usage: b.usage(state, prior)},
						dispatchError("reconciling after a cursor gap", ierr)
				}
				b.observe(reconciled)
				state.cursor = reconciled.Cursor
				if reconciled.Clarification != nil {
					state.clarification = reconciled.Clarification
				}
				if reconciled.Phase.IsTerminal() || reconciled.Phase == runner.PhaseNeedsInput {
					return b.outcomeFor(ctx, reconciled, state, prior)
				}
				continue
			}
			return backend.Outcome{Status: b.statusFor(ctx), Usage: b.usage(state, prior)}, err
		case terminal:
			final, ierr := b.cfg.Dispatcher.Inspect(ctx, attemptID)
			if ierr != nil {
				return backend.Outcome{Status: b.statusFor(ctx), Usage: b.usage(state, prior)},
					dispatchError("reading the finished attempt", ierr)
			}
			b.observe(final)
			// A terminal EVENT and a terminal RECEIPT are two facts, and the
			// event can arrive first: the runner publishes to subscribers while
			// it is still settling the attempt. Believing the event over the
			// receipt failed the turn with "neither finished nor waiting" —
			// reported from a live run — on work that had in fact completed.
			// The receipt is the authority, so go round again and ask it once
			// more rather than concluding from the event.
			if !final.Phase.IsTerminal() && final.Phase != runner.PhaseNeedsInput {
				select {
				case <-ctx.Done():
					return backend.Outcome{Status: backend.StatusCancelled, Usage: b.usage(state, prior)}, ctx.Err()
				case <-time.After(quietPoll):
				}
				continue
			}
			return b.outcomeFor(ctx, final, state, prior)
		case quiet:
			// The runner closed a quiet stream. Offer a checkpoint here — this is
			// a point where nothing is half-consumed — and ask the receipt whether
			// the attempt ended while we were not listening.
			b.checkpoint(sink, state, prior)
			current, ierr := b.cfg.Dispatcher.Inspect(ctx, attemptID)
			if ierr != nil {
				return backend.Outcome{Status: b.statusFor(ctx), Usage: b.usage(state, prior)},
					dispatchError("checking on the harness turn", ierr)
			}
			b.observe(current)
			if current.Phase.IsTerminal() || current.Phase == runner.PhaseNeedsInput {
				if current.Cursor > state.cursor && !caughtUp {
					// Events were produced between our last read and the phase
					// change; pick them up before concluding. Once only — see
					// caughtUp.
					caughtUp = true
					continue
				}
				return b.outcomeFor(ctx, current, state, prior)
			}
			if state.cursor == before {
				// Nothing new and still running: wait before asking again.
				select {
				case <-ctx.Done():
					return backend.Outcome{Status: backend.StatusCancelled, Usage: b.usage(state, prior)}, ctx.Err()
				case <-time.After(quietPoll):
				}
			}
		}
	}
}

// drain consumes one stream. quiet reports the ordinary end-of-stream the runner
// sends after a silent interval, as opposed to a terminal event.
func (b *Backend) drain(ctx context.Context, stream Stream, state *turnState, prior backend.Cost) (terminal, quiet bool, err error) {
	for {
		event, err := stream.Next(ctx)
		switch {
		case errors.Is(err, io.EOF):
			return false, true, nil
		case err != nil:
			return false, false, err
		}
		if state.observe(event) {
			return true, false, nil
		}
		if event.Type == runner.EventCheckpoint {
			b.checkpoint(state.sink, state, prior)
		}
	}
}

// checkpoint persists where this turn can be re-joined. The state is ours, not
// the harness's: a harness-backed run is recovered by addressing the same
// attempt at the same cursor, so what has to survive is the coordinates.
func (b *Backend) checkpoint(sink backend.EventSink, state *turnState, prior backend.Cost) {
	raw, err := json.Marshal(b.state(state, "", "", prior))
	if err != nil {
		// A checkpoint that cannot be serialized costs recoverability, which is
		// strictly better than failing a working turn over it.
		return
	}
	sink.Checkpoint(raw)
}

// state renders the resume coordinates.
func (b *Backend) state(s *turnState, clarificationID, permissionID string, prior backend.Cost) State {
	observed := b.Observed()
	epoch := observed.Epoch
	if epoch == 0 {
		epoch = b.cfg.Epoch
	}
	return State{
		TaskID:          b.cfg.TaskID,
		AttemptID:       firstNonEmpty(observed.AttemptID, b.cfg.AttemptID),
		Epoch:           epoch,
		SessionID:       firstNonEmpty(observed.SessionID, b.cfg.SessionID),
		Cursor:          s.cursor,
		ClarificationID: clarificationID,
		PermissionID:    permissionID,
		Spent:           addCost(prior, s.cost),
	}
}

// outcomeFor maps a terminal (or parked) receipt plus what the stream said onto
// the seam.
func (b *Backend) outcomeFor(ctx context.Context, receipt runner.Receipt, s *turnState, prior backend.Cost) (backend.Outcome, error) {
	usage := b.usage(s, prior)
	switch receipt.Phase {
	case runner.PhaseNeedsInput:
		// Two parks arrive on this phase and they are not interchangeable. A
		// PERMISSION request is a named tool call waiting on a verdict, and the
		// provider already knows how to file one of those: Tool and Args make
		// it an approval, with Approve and Deny. A QUESTION has no call at all
		// and is answered with words. Reporting either as the other gives a
		// person a control that cannot mean anything.
		if permission := receipt.Permission; permission != nil {
			raw, err := json.Marshal(b.state(s, "", permission.ID, prior))
			if err != nil {
				return backend.Outcome{Status: backend.StatusFailed, Usage: usage},
					fmt.Errorf("recording the turn's resume state: %w", err)
			}
			return backend.Outcome{
				Status: backend.StatusParked,
				Text:   runnerharness.StripClarification(s.text.String()),
				Usage:  usage,
				// Args is the harness's own rendering of the call's input,
				// already bounded by the runner. It is what the person is shown
				// and what their approval authorizes — that call, those
				// arguments.
				Parked: &backend.Parked{Tool: permission.Tool, Args: permission.Input, State: raw},
			}, nil
		}
		clarification := s.clarification
		if receipt.Clarification != nil {
			clarification = receipt.Clarification
		}
		question := strings.TrimSpace(receipt.Blocker)
		id := ""
		if clarification != nil {
			id = clarification.ID
			if text := strings.TrimSpace(clarification.Text); text != "" {
				question = text
			}
		}
		if question == "" {
			question = "the harness is waiting for input"
		}
		raw, err := json.Marshal(b.state(s, id, "", prior))
		if err != nil {
			// Without resumable state the park would strand the run: no answer
			// could ever continue it. Fail it instead, which at least ends it
			// where a person can see it.
			return backend.Outcome{Status: backend.StatusFailed, Usage: usage},
				fmt.Errorf("recording the turn's resume state: %w", err)
		}
		return backend.Outcome{
			Status: backend.StatusParked,
			// The clarification block is protocol, not prose: the question is
			// already carried as Parked.Question, and leaving the raw markers in
			// the transcript showed a person the machinery instead of the
			// question.
			Text:  runnerharness.StripClarification(s.text.String()),
			Usage: usage,
			// No Tool/Args: nothing was gated. The harness asked a question, and
			// what resolves it is an answer rather than a verdict on a call.
			Parked: &backend.Parked{Question: question, State: raw},
		}, nil
	case runner.PhaseCancelled:
		s.endOpenTools()
		return backend.Outcome{Status: backend.StatusCancelled, Usage: usage}, cancelledError(receipt)
	case runner.PhaseFailed:
		s.endOpenTools()
		return backend.Outcome{Status: backend.StatusFailed, Usage: usage}, failedError(receipt)
	case runner.PhaseCompleted:
		s.endOpenTools()
		text := runnerharness.StripClarification(s.text.String())
		final := runnerharness.StripClarification(s.final)
		if strings.TrimSpace(final) == "" {
			final = strings.TrimSpace(text)
		}
		if strings.TrimSpace(text) == "" {
			text = final
		}
		if s.turnDuration > 0 {
			// Codex's duration is for the whole turn. ToolEnd already accounts
			// for commandExecution durations, so attribute only the remainder to
			// the completed assistant segment.
			assistantDuration := s.turnDuration - s.toolDuration
			if assistantDuration < 0 {
				assistantDuration = 0
			}
			s.sink.Assistant(backend.AssistantMessage{
				Content: final, Complete: true, Duration: assistantDuration,
			})
		}
		output, sources := backend.SplitSources(text)
		return backend.Outcome{
			Status: backend.StatusCompleted,
			Text:   text, Output: output, Sources: sources,
			Final: final,
			Usage: usage,
		}, nil
	default:
		// Not terminal and not parked: the caller only reaches this with a
		// receipt it believed was one of the two, so say so rather than
		// reporting a phase as an answer.
		return backend.Outcome{Status: b.statusFor(ctx), Usage: usage},
			fmt.Errorf("attempt %s is %s, which is neither finished nor waiting", receipt.AttemptID, receipt.Phase)
	}
}

// usage prices the turn. Total is the run's cumulative consumption and Billed is
// what THIS turn added: equal on a fresh turn, and on a resume the totals include
// what the attempt had spent before it parked (which was billed then).
func (b *Backend) usage(s *turnState, prior backend.Cost) backend.Usage {
	total := addCost(prior, s.cost)
	return backend.Usage{Total: total, Billed: s.cost}
}

func addCost(a, b backend.Cost) backend.Cost {
	return backend.Cost{
		Tokens: backend.Tokens{
			InputTokens:  a.InputTokens + b.InputTokens,
			OutputTokens: a.OutputTokens + b.OutputTokens,
		},
		CostMicros: a.CostMicros + b.CostMicros,
	}
}

// statusFor classifies an error the turn could not continue past. A turn stopped
// on purpose — the user's cancel, the run's deadline — is not a fault, and the
// context it ran under is what knows which happened.
func (b *Backend) statusFor(ctx context.Context) backend.Status {
	if ctx.Err() != nil {
		return backend.StatusCancelled
	}
	return backend.StatusFailed
}

func (b *Backend) validate() error {
	switch {
	case b.cfg.Dispatcher == nil:
		return errors.New("no runner dispatcher configured for this harness agent")
	case strings.TrimSpace(b.cfg.TaskID) == "":
		return errors.New("a harness turn needs a task id (the conversation)")
	case strings.TrimSpace(b.cfg.AttemptID) == "":
		return errors.New("a harness turn needs an attempt id (the run)")
	case b.cfg.Epoch == 0:
		return errors.New("a harness turn needs a nonzero epoch (the turn number)")
	case strings.TrimSpace(b.cfg.WorkspaceID) == "":
		return errors.New("a harness turn needs a workspace id")
	case b.cfg.Credential.Empty():
		return errors.New("a harness turn needs a harness credential; every dispatch carries one, and a runner has no identity of its own")
	}
	return nil
}

// credential renders the protocol shape. It is built per call and never stored:
// the value is a login, and the fewer places it sits the better.
func (b *Backend) credential() runner.HarnessCredential {
	return runner.HarnessCredential{Kind: b.cfg.Credential.Kind, Value: b.cfg.Credential.Value}
}

// approvedInput is the envelope the runner requires on every dispatch. It must
// be a non-empty JSON object carrying provenance, and the provenance is the
// run's own: what agent, which run, what started it, which workspace.
func (b *Backend) approvedInput(r *backend.Run) (json.RawMessage, error) {
	provenance := map[string]any{}
	for k, v := range b.cfg.Provenance {
		provenance[k] = v
	}
	if r != nil {
		if r.Agent != "" {
			provenance["agent"] = r.Agent
		}
		if r.ID != "" {
			provenance["runID"] = r.ID
		}
		if r.SessionID != "" {
			provenance["sessionID"] = r.SessionID
		}
		if r.Trigger != "" {
			provenance["trigger"] = r.Trigger
		}
	}
	provenance["dispatcher"] = "agents.railgrid.ai"
	if len(provenance) == 0 {
		return nil, errors.New("a harness dispatch needs provenance")
	}
	raw, err := json.Marshal(map[string]any{"provenance": provenance})
	if err != nil {
		return nil, fmt.Errorf("building the dispatch provenance: %w", err)
	}
	return raw, nil
}

// instructions renders the conversation as the one prompt a harness launch
// takes.
//
// A harness holds its own session, so the history the provider assembled is
// mostly redundant — but not entirely: the system prompt is the agent's persona
// and the provider re-asserts it, and the first turn of a conversation has no
// session for the harness to remember. So the messages are flattened, in order,
// with the roles named, and the harness's own memory does the rest.
func instructions(messages []backend.Message) string {
	var b strings.Builder
	for _, m := range messages {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		switch m.Role {
		case backend.RoleSystem:
			b.WriteString("[standing instructions]\n")
		case backend.RoleAssistant:
			b.WriteString("[you said earlier]\n")
		case backend.RoleTool:
			b.WriteString("[tool result from earlier]\n")
		default:
			b.WriteString("[the user says]\n")
		}
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

// resolution renders the user's answer for the resume. A denial is phrased as
// one so the harness reacts to a refusal rather than to an empty answer.
func resolution(answer backend.Answer) string {
	note := strings.TrimSpace(answer.Note)
	if !answer.Decided {
		return note
	}
	if answer.Approved {
		if note == "" {
			return "Approved. Continue."
		}
		return note
	}
	if note == "" {
		return "The user declined. Do not proceed with that; stop and explain what you would have done."
	}
	return "The user declined: " + note
}

func clarificationID(receipt runner.Receipt, state State) string {
	if receipt.Clarification != nil && receipt.Clarification.ID != "" {
		return receipt.Clarification.ID
	}
	return state.ClarificationID
}

// permissionID is the outstanding permission request to answer. The RECEIPT is
// authoritative — it is what the runner is actually waiting on right now — and
// the stored state is the fallback for a resume that could not re-read it.
func permissionID(receipt runner.Receipt, state State) string {
	if receipt.Permission != nil && receipt.Permission.ID != "" {
		return receipt.Permission.ID
	}
	return state.PermissionID
}

// ---- error classification ----------------------------------------------------

// dispatchError wraps a runner failure with what was being attempted, keeping
// the original for errors.As: callers branch on *runner.Error's Code and on
// *client.HTTPError's status, never on a message.
func dispatchError(what string, err error) error {
	var protocol *runner.Error
	if errors.As(err, &protocol) {
		return fmt.Errorf("%s: %s (%s): %w", what, protocol.Message, protocol.Code, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// snapshotRequired reports a cursor the runner no longer holds: the caller's
// view is incomplete and only Inspect can restore it.
func snapshotRequired(err error) bool {
	var protocol *runner.Error
	if !errors.As(err, &protocol) {
		return false
	}
	return protocol.SnapshotRequired || protocol.Code == runner.ErrorCursorExpired
}

// terminalProtocolError reports a cancel that had nothing to cancel: an attempt
// the runner never heard of, or one whose epoch is already obsolete because it
// finished.
func terminalProtocolError(err error) bool {
	var protocol *runner.Error
	if !errors.As(err, &protocol) {
		return false
	}
	switch protocol.Code {
	case runner.ErrorStaleAttempt:
		return protocol.Receipt != nil && protocol.Receipt.Phase.IsTerminal()
	case runner.ErrorUnavailable:
		return protocol.Receipt == nil
	default:
		return false
	}
}

func cancelledError(receipt runner.Receipt) error {
	if blocker := strings.TrimSpace(receipt.Blocker); blocker != "" {
		return fmt.Errorf("the harness turn was stopped: %s", blocker)
	}
	return errors.New("the harness turn was stopped")
}

func failedError(receipt runner.Receipt) error {
	if receipt.LastError != nil && strings.TrimSpace(receipt.LastError.Message) != "" {
		return fmt.Errorf("the harness turn failed: %s (%s)", receipt.LastError.Message, receipt.LastError.Code)
	}
	if blocker := strings.TrimSpace(receipt.Blocker); blocker != "" {
		return fmt.Errorf("the harness turn failed: %s", blocker)
	}
	return errors.New("the harness turn failed")
}
