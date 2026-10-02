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
// What this package holds is only what makes a turn an AGENT'S turn: how a
// conversation is rendered as the one prompt a launch takes, how a session's
// turns are numbered so the runner refuses a double dispatch, what provenance
// the dispatch carries, and how the seam's parks and verdicts map onto the
// protocol's. The attempt lifecycle itself — following, parking, resuming,
// cancelling, and reading a harness's stream into text, tools and cost — is
// railgrid's pkg/runner/dispatch, shared with every other product that drives a
// runner, so a rule learned once is learned for all of them.
//
// Three protocol rules shape what is here:
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
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/dispatch"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/llm"
)

// Config is everything one harness-backed turn is dispatched with. The provider
// resolves it per turn, the same way it resolves a chat model per turn: the
// credential, the epoch and the session all change from one turn to the next.
type Config struct {
	// Runner reaches the enrolled runner. dispatch.Wrap adapts the shared client.
	Runner dispatch.Runner

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

// position renders the state as the lifecycle's coordinates.
func (s State) position() dispatch.Position {
	return dispatch.Position{
		TaskID: s.TaskID, AttemptID: s.AttemptID, Epoch: s.Epoch, SessionID: s.SessionID,
		Cursor: s.Cursor, ClarificationID: s.ClarificationID, PermissionID: s.PermissionID,
	}
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

func (b *Backend) observe(pos dispatch.Position) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if pos.AttemptID != "" {
		b.observed.AttemptID = pos.AttemptID
	}
	// The receipt's session id is AUTHORITATIVE — see the package comment —
	// and Position carries what the receipt said.
	if pos.SessionID != "" {
		b.observed.SessionID = pos.SessionID
	}
	if pos.Epoch != 0 {
		b.observed.Epoch = pos.Epoch
	}
}

// start is where this turn begins, before anything was observed.
func (b *Backend) start() dispatch.Position {
	return dispatch.Position{TaskID: b.cfg.TaskID, AttemptID: b.cfg.AttemptID, Epoch: b.cfg.Epoch, SessionID: b.cfg.SessionID}
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
	receipt, err := b.cfg.Runner.Start(ctx, req)
	if err != nil {
		return backend.Outcome{Status: b.statusFor(ctx)}, dispatch.Describe("starting the harness turn", err)
	}
	out, err := dispatch.Follow(ctx, b.cfg.Runner, receipt, b.start(), b.observer(sink, backend.Cost{}))
	return b.outcome(ctx, out, err, backend.Cost{})
}

// Continue picks a parked turn back up.
//
// Two shapes arrive here, and they are not the same call. A DECIDED answer is
// the user's resolution of a question the harness asked, or their verdict on a
// permission prompt, and it is a resume. An UNDECIDED answer is a run whose
// process died, and there is nothing to resolve — the attempt may well still be
// executing on the machine — so it is a RE-JOIN: reconcile with Inspect and
// keep tailing from the cursor. A re-joined attempt found waiting on input is
// parked AGAIN rather than answered with nothing: the provider died between the
// park and recording it, and the person still gets to answer.
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
	pos := state.position()
	obs := b.observer(sink, state.Spent)

	if !answer.Decided {
		out, err := dispatch.Rejoin(ctx, b.cfg.Runner, pos, obs)
		return b.outcome(ctx, out, err, state.Spent)
	}

	receipt, err := b.cfg.Runner.Inspect(ctx, state.AttemptID)
	if err != nil {
		return backend.Outcome{Status: b.statusFor(ctx)}, dispatch.Describe("inspecting the parked attempt", err)
	}
	pos.Observe(receipt)
	b.observe(pos)
	// Already finished while nobody was watching: the receipt is the answer, and
	// resuming it would be a second dispatch of work that is done. Still
	// executing: the park was answered by events, so re-join it. Only a receipt
	// genuinely waiting on input takes the answer.
	if receipt.Phase != runner.PhaseNeedsInput {
		out, err := dispatch.Rejoin(ctx, b.cfg.Runner, pos, obs)
		return b.outcome(ctx, out, err, state.Spent)
	}
	approved, err := b.approvedInput(r)
	if err != nil {
		return backend.Outcome{Status: backend.StatusFailed}, err
	}
	credential := b.credential()
	// A permission park takes a VERDICT and the person's own note as the
	// reason; a question takes the resolution rendered for it. A denial is
	// phrased as one so the harness reacts to a refusal rather than to an empty
	// answer. The lifecycle picks the field the park is answered through.
	resume := dispatch.ResumeRequest(b.newID(), receipt, pos, dispatch.Answer{
		Resolution: resolution(answer, receipt.Permission != nil),
		Allow:      answer.Approved,
	}, approved, &credential)
	out, err := dispatch.Resume(ctx, b.cfg.Runner, resume, pos, obs)
	return b.outcome(ctx, out, err, state.Spent)
}

// Cancel stops a turn executing on the runner, and then OBSERVES that it
// stopped. The provider calls this from whoever asked for the cancel, having
// already cancelled the run's context — so the local half is done either way,
// and what is left is the half on somebody else's machine. The provider bounds
// this call; when the bound runs out the error says the attempt is still
// cancelling, which is the truth and is what gets logged.
func (b *Backend) Cancel(ctx context.Context, _ *backend.Run) error {
	if b.cfg.Runner == nil {
		return errors.New("no runner configured")
	}
	observed := b.Observed()
	attemptID := observed.AttemptID
	if attemptID == "" {
		attemptID = b.cfg.AttemptID
	}
	if attemptID == "" {
		return nil // nothing was ever dispatched
	}
	epoch := observed.Epoch
	if epoch == 0 {
		epoch = b.cfg.Epoch
	}
	receipt, err := dispatch.Cancel(ctx, b.cfg.Runner, runner.CancelRequest{
		RequestID:    b.newID(),
		TaskID:       b.cfg.TaskID,
		AttemptID:    attemptID,
		AttemptEpoch: epoch,
	})
	if receipt != nil {
		pos := dispatch.Position{}
		pos.Observe(*receipt)
		b.observe(pos)
	}
	return err
}

// outcome maps what the lifecycle reported onto the seam.
//
// prior is what the run had spent before this turn began — zero for a fresh
// turn, the checkpoint's accumulator for a resumed one. Total is the run's
// cumulative consumption and Billed is what THIS turn added.
func (b *Backend) outcome(ctx context.Context, out dispatch.Outcome, err error, prior backend.Cost) (backend.Outcome, error) {
	b.observe(out.Position)
	usage := usage(out.Summary.Usage, prior)
	if err != nil {
		// A turn stopped on purpose is not a fault. The lifecycle reports a
		// cancelled receipt as an error carrying the runner's words; the seam
		// records it as aborted rather than failed.
		status := b.statusFor(ctx)
		switch out.Receipt.Phase {
		case runner.PhaseCancelled:
			status = backend.StatusCancelled
		case runner.PhaseFailed:
			status = backend.StatusFailed
		}
		return backend.Outcome{Status: status, Usage: usage}, err
	}
	if parked := out.Parked; parked != nil {
		raw, merr := json.Marshal(b.state(out.Position, prior, out.Summary.Usage))
		if merr != nil {
			// Without resumable state the park would strand the run: no answer
			// could ever continue it. Fail it instead, which at least ends it
			// where a person can see it.
			return backend.Outcome{Status: backend.StatusFailed, Usage: usage},
				fmt.Errorf("recording the turn's resume state: %w", merr)
		}
		if parked.Permission != nil {
			// Tool and Args make it an approval, with Approve and Deny. Args is
			// the harness's own rendering of the call's input, already bounded
			// by the runner: it is what the person is shown and what their
			// approval authorizes — that call, those arguments.
			return backend.Outcome{
				Status: backend.StatusParked, Text: out.Summary.Text, Usage: usage,
				Parked: &backend.Parked{Tool: parked.Permission.Tool, Args: parked.Permission.Input, State: raw},
			}, nil
		}
		// No Tool/Args: nothing was gated. The harness asked a question, and
		// what resolves it is an answer rather than a verdict on a call.
		return backend.Outcome{
			Status: backend.StatusParked, Text: out.Summary.Text, Usage: usage,
			Parked: &backend.Parked{Question: parked.Question.Text, State: raw},
		}, nil
	}
	text := out.Summary.Text
	output, sources := backend.SplitSources(text)
	return backend.Outcome{
		Status: backend.StatusCompleted,
		Text:   text, Output: output, Sources: sources,
		Final: out.Summary.Final,
		Usage: usage,
	}, nil
}

// state renders the resume coordinates the provider persists.
func (b *Backend) state(pos dispatch.Position, prior backend.Cost, spent dispatch.Usage) State {
	return State{
		TaskID:          firstNonEmpty(pos.TaskID, b.cfg.TaskID),
		AttemptID:       firstNonEmpty(pos.AttemptID, b.cfg.AttemptID),
		Epoch:           maxEpoch(pos.Epoch, b.cfg.Epoch),
		SessionID:       firstNonEmpty(pos.SessionID, b.cfg.SessionID),
		Cursor:          pos.Cursor,
		ClarificationID: pos.ClarificationID,
		PermissionID:    pos.PermissionID,
		Spent:           usage(spent, prior).Total,
	}
}

// observer wires the lifecycle's observations onto the seam. No field is
// translated beyond naming: the stream's prose is a delta, its tool calls are
// the seam's tool events, and its checkpoints become this backend's State.
func (b *Backend) observer(sink backend.EventSink, prior backend.Cost) dispatch.Observer {
	return &sinkObserver{b: b, sink: sink, prior: prior}
}

type sinkObserver struct {
	b     *Backend
	sink  backend.EventSink
	prior backend.Cost
}

func (o *sinkObserver) Text(delta string)                 { o.sink.Delta(delta) }
func (o *sinkObserver) ToolStart(id, name, args string)   { o.sink.ToolStart(id, name, args) }
func (o *sinkObserver) Aborted(ctx context.Context) error { return o.sink.Aborted(ctx) }

func (o *sinkObserver) ToolEnd(t dispatch.ToolResult) {
	o.sink.ToolEnd(backend.ToolEvent{ID: t.ID, Name: t.Name, Args: t.Args, Result: t.Result, Err: t.Failed})
}

// Checkpoint persists where this turn can be re-joined. The state is ours, not
// the harness's: a harness-backed run is recovered by addressing the same
// attempt at the same cursor, so what has to survive is the coordinates.
func (o *sinkObserver) Checkpoint(snap dispatch.Snapshot) {
	raw, err := json.Marshal(o.b.state(snap.Position, o.prior, snap.Usage))
	if err != nil {
		// A checkpoint that cannot be serialized costs recoverability, which is
		// strictly better than failing a working turn over it.
		return
	}
	o.sink.Checkpoint(raw)
}

// usage prices what the lifecycle saw against what the run had already spent.
func usage(spent dispatch.Usage, prior backend.Cost) backend.Usage {
	billed := backend.Cost{
		Tokens:     backend.Tokens{InputTokens: spent.InputTokens, OutputTokens: spent.OutputTokens},
		CostMicros: spent.CostMicros,
	}
	total := backend.Cost{
		Tokens: backend.Tokens{
			InputTokens:  prior.InputTokens + billed.InputTokens,
			OutputTokens: prior.OutputTokens + billed.OutputTokens,
		},
		CostMicros: prior.CostMicros + billed.CostMicros,
	}
	return backend.Usage{Total: total, Billed: billed}
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
	case b.cfg.Runner == nil:
		return errors.New("no runner configured for this harness agent")
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

// resolution renders the user's decision for the resume.
//
// For a QUESTION the words are the answer, and a denial is phrased as one so the
// harness reacts to a refusal rather than to an empty answer. For a PERMISSION
// prompt only the person's own note is passed on: the verdict travels in its own
// field, and the stock "Approved. Continue." is an instruction for a new turn
// that means nothing to a tool call that is waiting.
func resolution(answer backend.Answer, permission bool) string {
	note := strings.TrimSpace(answer.Note)
	if permission {
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func maxEpoch(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
