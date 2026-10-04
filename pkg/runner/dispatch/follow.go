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
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// Position is where an attempt can be re-joined: the coordinates of work in
// flight, not a snapshot of it. The harness holds the transcript; re-joining
// is addressing the same attempt at the same cursor.
//
// Every field a receipt reports is taken from the receipt when it is set, and
// the session id in particular is AUTHORITATIVE: a harness may fork its session
// on a resume, and chaining onto what was sent would continue a conversation
// nobody is having.
type Position struct {
	TaskID    string
	AttemptID string
	Epoch     uint64
	SessionID string
	// Cursor is the last event consumed, so a re-join tails from there instead
	// of re-streaming what the caller has already recorded.
	Cursor uint64
	// ClarificationID is what a resume must echo to answer the QUESTION the
	// harness asked; PermissionID is what it must echo to answer a PERMISSION
	// prompt. They are different parks that resume differently, and at most
	// one is set.
	ClarificationID string
	PermissionID    string
}

// Observe folds a receipt into the position: every identity the receipt
// reports is taken from it when set.
//
// NOT the cursor. A receipt's cursor is the RUNNER's head — how far it has
// got — and Position.Cursor is how far the CALLER has consumed. Taking the
// receipt's would skip every event between the two, which is exactly what a
// re-join exists to read. The cursor advances only as events are consumed, and
// jumps only on a reconcile after a cursor gap, where the skipped events are
// already gone.
func (p *Position) Observe(receipt runner.Receipt) {
	if receipt.TaskID != "" {
		p.TaskID = receipt.TaskID
	}
	if receipt.AttemptID != "" {
		p.AttemptID = receipt.AttemptID
	}
	if receipt.AttemptEpoch != 0 {
		p.Epoch = receipt.AttemptEpoch
	}
	if receipt.SessionID != "" {
		p.SessionID = receipt.SessionID
	}
}

// Usage is what an attempt reported spending. Where a harness reports tokens
// and no price the cost stays zero: a number invented from a token count and a
// guessed rate would be wrong in a billing column.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
}

// Snapshot is a Position plus what the follow had accumulated when it was
// taken. It is what Observer.Checkpoint offers.
type Snapshot struct {
	Position Position
	Usage    Usage
}

// Summary is what the stream said, accumulated, so a caller need not.
type Summary struct {
	// Text is every piece of prose the harness produced, in order.
	Text string
	// Final is the harness's own answer record when it produced one, and
	// otherwise the text. It is the presentation answer.
	Final string
	// Usage is what THIS follow saw reported. A caller re-joining an attempt
	// adds what it had already recorded.
	Usage Usage
}

// Parked describes a needs_input receipt. Exactly one field is set, and the two
// are answered differently: a Question with words, a Permission with a verdict
// on the named call. See Answer.
type Parked struct {
	Question   *runner.Clarification
	Permission *runner.PermissionRequest
}

// Outcome is how a follow ended. Receipt is the authority; Position is where
// the attempt stands; Parked is set when the receipt is needs_input.
type Outcome struct {
	Receipt  runner.Receipt
	Position Position
	Summary  Summary
	Parked   *Parked
}

// Terminal reports whether the attempt is over.
func (o Outcome) Terminal() bool { return o.Receipt.Phase.IsTerminal() }

// Completed reports a successful end.
func (o Outcome) Completed() bool { return o.Receipt.Phase == runner.PhaseCompleted }

const (
	// cancelPoll is how often the receipt is re-read after a cancel was posted.
	// The runner stops a harness child and waits for it, so the gap between
	// "cancelling" and "cancelled" is real but short.
	cancelPoll = 250 * time.Millisecond

	// quietPoll spaces reconnects when a stream ends with nothing new on it.
	// Ordinarily the runner holds a stream open for 30 seconds before ending
	// it, so a reconnect costs one round trip a minute — but nothing in the
	// protocol PROMISES that, and a runner that answered every stream at once
	// would otherwise be hammered by a tight loop.
	quietPoll = 250 * time.Millisecond
)

// Start dispatches a fresh attempt and follows it to the end or to a park.
//
// A caller with a durability fence of its own — one that must write the start
// receipt down before anything else happens — calls Runner.Start itself and
// then Follow; this is the convenience for callers without one.
func Start(ctx context.Context, r Runner, req runner.StartRequest, obs Observer) (Outcome, error) {
	receipt, err := r.Start(ctx, req)
	if err != nil {
		return Outcome{}, Describe("starting the attempt", err)
	}
	return Follow(ctx, r, receipt, Position{}, obs)
}

// Resume answers a park and follows the attempt onward. Build req with
// ResumeRequest so the park is answered through the right field.
func Resume(ctx context.Context, r Runner, req runner.ResumeRequest, from Position, obs Observer) (Outcome, error) {
	receipt, err := r.Resume(ctx, req)
	if err != nil {
		return Outcome{Position: from}, Describe("resuming the attempt", err)
	}
	// The park is answered; the ids that named it must not survive onto the
	// next park, which the receipt will name afresh.
	from.ClarificationID, from.PermissionID = "", ""
	return Follow(ctx, r, receipt, from, obs)
}

// Rejoin picks an attempt back up from a position, which is what a caller does
// after its own process died: it does not know whether the attempt finished,
// parked or is still working, and Inspect is what tells it.
//
//   - Already over: the receipt is the answer, and resuming it would be a
//     second dispatch of work that is done.
//   - Parked: the receipt names the question or the prompt, and the caller
//     decides how to answer. Nothing is sent.
//   - Still working: tail it from the cursor. Nothing was lost.
func Rejoin(ctx context.Context, r Runner, pos Position, obs Observer) (Outcome, error) {
	if pos.AttemptID == "" {
		return Outcome{Position: pos}, errors.New("re-joining an attempt needs its id")
	}
	receipt, err := r.Inspect(ctx, pos.AttemptID)
	if err != nil {
		return Outcome{Position: pos}, Describe("inspecting the attempt", err)
	}
	pos.Observe(receipt)
	if receipt.Phase.IsTerminal() || receipt.Phase == runner.PhaseNeedsInput {
		return outcomeFor(receipt, pos, newStream(obs, pos.Cursor))
	}
	return Follow(ctx, r, receipt, pos, obs)
}

// Follow tails an attempt's events from a position to a terminal phase or a
// park, reporting them through obs, and then reads the receipt — which is the
// authority on how it ended.
//
// The loop reconnects, and that is not a retry loop: the runner ends a stream
// itself after a silent interval so a caller resumes from its cursor rather
// than holding one connection open for the length of a coding turn. Each
// reconnect is also where the two things that must not be skipped happen: the
// caller's cancel flag is consulted, and the receipt is re-read in case the
// attempt ended during the quiet.
func Follow(ctx context.Context, r Runner, receipt runner.Receipt, from Position, obs Observer) (Outcome, error) {
	if obs == nil {
		obs = Noop{}
	}
	pos := from
	pos.Observe(receipt)
	attemptID := pos.AttemptID
	if attemptID == "" {
		return Outcome{Position: pos}, errors.New("following an attempt needs its id")
	}
	s := newStream(obs, from.Cursor)
	if receipt.Clarification != nil {
		s.clarification = receipt.Clarification
	}
	// caughtUp guards the ONE catch-up pass a terminal receipt is allowed: the
	// receipt's cursor can be ahead of ours when the phase changed during a
	// quiet stream, and the events in between are worth one more read. Without
	// the flag a runner whose cursor stays ahead — because the events it
	// counted have already been dropped — would be re-read forever.
	caughtUp := false
	for {
		if err := obs.Aborted(ctx); err != nil {
			return Outcome{Receipt: receipt, Position: s.position(pos), Summary: s.summary()}, err
		}
		stream, err := r.Events(ctx, attemptID, s.cursor)
		if err != nil {
			return Outcome{Receipt: receipt, Position: s.position(pos), Summary: s.summary()},
				Describe("following the attempt", err)
		}
		before := s.cursor
		terminal, quiet, err := drain(ctx, stream, s)
		_ = stream.Close()
		switch {
		case err != nil:
			// A cursor the runner no longer holds means our view is incomplete,
			// and Inspect is the only thing that can restore it. Reconciling and
			// carrying on is the whole point of SnapshotRequired; failing over a
			// dropped event would throw away a turn that is still working.
			if IsSnapshotRequired(err) {
				reconciled, ierr := r.Inspect(ctx, attemptID)
				if ierr != nil {
					return Outcome{Receipt: receipt, Position: s.position(pos), Summary: s.summary()},
						Describe("reconciling after a cursor gap", ierr)
				}
				receipt = reconciled
				pos.Observe(reconciled)
				s.cursor = reconciled.Cursor
				if reconciled.Clarification != nil {
					s.clarification = reconciled.Clarification
				}
				if reconciled.Phase.IsTerminal() || reconciled.Phase == runner.PhaseNeedsInput {
					return outcomeFor(reconciled, pos, s)
				}
				continue
			}
			return Outcome{Receipt: receipt, Position: s.position(pos), Summary: s.summary()}, err
		case terminal:
			final, ierr := r.Inspect(ctx, attemptID)
			if ierr != nil {
				return Outcome{Receipt: receipt, Position: s.position(pos), Summary: s.summary()},
					Describe("reading the finished attempt", ierr)
			}
			receipt = final
			pos.Observe(final)
			// A terminal EVENT and a terminal RECEIPT are two facts, and the
			// event can arrive first: the runner publishes to subscribers while
			// it is still settling the attempt. Believing the event over the
			// receipt failed a turn with "neither finished nor waiting" on work
			// that had in fact completed. The receipt is the authority, so go
			// round again and ask it once more rather than concluding from the
			// event.
			if !final.Phase.IsTerminal() && final.Phase != runner.PhaseNeedsInput {
				select {
				case <-ctx.Done():
					return Outcome{Receipt: receipt, Position: s.position(pos), Summary: s.summary()}, ctx.Err()
				case <-time.After(quietPoll):
				}
				continue
			}
			return outcomeFor(final, pos, s)
		case quiet:
			// The runner closed a quiet stream. Offer a checkpoint here — this
			// is a point where nothing is half-consumed — and ask the receipt
			// whether the attempt ended while we were not listening.
			obs.Checkpoint(Snapshot{Position: s.position(pos), Usage: s.usage})
			current, ierr := r.Inspect(ctx, attemptID)
			if ierr != nil {
				return Outcome{Receipt: receipt, Position: s.position(pos), Summary: s.summary()},
					Describe("checking on the attempt", ierr)
			}
			receipt = current
			pos.Observe(current)
			if current.Phase.IsTerminal() || current.Phase == runner.PhaseNeedsInput {
				if current.Cursor > s.cursor && !caughtUp {
					// Events were produced between our last read and the phase
					// change; pick them up before concluding. Once only — see
					// caughtUp.
					caughtUp = true
					continue
				}
				return outcomeFor(current, pos, s)
			}
			if s.cursor == before {
				// Nothing new and still running: wait before asking again.
				select {
				case <-ctx.Done():
					return Outcome{Receipt: receipt, Position: s.position(pos), Summary: s.summary()}, ctx.Err()
				case <-time.After(quietPoll):
				}
			}
		}
	}
}

// drain consumes one stream. quiet reports the ordinary end-of-stream the
// runner sends after a silent interval, as opposed to a terminal event.
func drain(ctx context.Context, st Stream, s *stream) (terminal, quiet bool, err error) {
	for {
		event, err := st.Next(ctx)
		switch {
		case errors.Is(err, io.EOF):
			return false, true, nil
		case err != nil:
			return false, false, err
		}
		if s.observe(event) {
			return true, false, nil
		}
		if event.Type == runner.EventCheckpoint {
			s.obs.Checkpoint(Snapshot{Position: s.position(Position{}), Usage: s.usage})
		}
	}
}

// Cancel stops an attempt executing on the runner, and then OBSERVES that it
// stopped.
//
// The observation is the point. An attempt on somebody else's machine is
// `cancelling` until its harness child has actually been reaped and its
// terminal receipt says `cancelled`; returning before that would report a
// cancellation nobody has seen, and the next thing that read the attempt would
// find it still running. The caller bounds this through ctx; when the bound
// runs out the error says the attempt is still cancelling, which is the truth.
//
// A nil receipt with a nil error means there was nothing to cancel: the runner
// never heard of the attempt, or it had already ended.
func Cancel(ctx context.Context, r Runner, req runner.CancelRequest) (*runner.Receipt, error) {
	receipt, err := r.Cancel(ctx, req)
	if err != nil {
		if IsNothingToCancel(err) {
			return nil, nil
		}
		return nil, Describe("cancelling the attempt", err)
	}
	for !receipt.Phase.IsTerminal() {
		select {
		case <-ctx.Done():
			return &receipt, fmt.Errorf("attempt %s is %s, not cancelled: %w", req.AttemptID, receipt.Phase, ctx.Err())
		case <-time.After(cancelPoll):
		}
		receipt, err = r.Inspect(ctx, req.AttemptID)
		if err != nil {
			return nil, Describe("observing the cancelled attempt", err)
		}
	}
	return &receipt, nil
}

// outcomeFor maps a terminal or parked receipt, plus what the stream said, onto
// an Outcome.
func outcomeFor(receipt runner.Receipt, pos Position, s *stream) (Outcome, error) {
	pos = s.position(pos)
	out := Outcome{Receipt: receipt, Position: pos}
	switch receipt.Phase {
	case runner.PhaseNeedsInput:
		// Two parks arrive on this phase and they are not interchangeable. A
		// PERMISSION request is a named tool call waiting on a verdict. A
		// QUESTION has no call at all and is answered with words. Reporting
		// either as the other gives a person a control that cannot mean
		// anything.
		if permission := receipt.Permission; permission != nil {
			out.Position.PermissionID = permission.ID
			out.Position.ClarificationID = ""
			out.Parked = &Parked{Permission: permission}
			out.Summary = s.summary()
			return out, nil
		}
		question := s.clarification
		if receipt.Clarification != nil {
			question = receipt.Clarification
		}
		if question == nil || strings.TrimSpace(question.Text) == "" {
			text := strings.TrimSpace(receipt.Blocker)
			if text == "" {
				text = "the harness is waiting for input"
			}
			id := ""
			if question != nil {
				id = question.ID
			}
			question = &runner.Clarification{ID: id, Text: text}
		}
		out.Position.ClarificationID = question.ID
		out.Position.PermissionID = ""
		out.Parked = &Parked{Question: question}
		out.Summary = s.summary()
		return out, nil
	case runner.PhaseCancelled, runner.PhaseFailed:
		s.endOpenTools()
		out.Summary = s.summary()
		return out, Ended(receipt)
	case runner.PhaseCompleted:
		s.endOpenTools()
		out.Summary = s.summary()
		return out, nil
	default:
		// Not terminal and not parked: the caller only reaches this with a
		// receipt it believed was one of the two, so say so rather than
		// reporting a phase as an answer.
		out.Summary = s.summary()
		return out, fmt.Errorf("attempt %s is %s, which is neither finished nor waiting", receipt.AttemptID, receipt.Phase)
	}
}

// summary renders what the stream accumulated. The clarification block is
// protocol, not prose: the question is carried as Parked.Question, and leaving
// the raw markers in the text showed a person the machinery instead of the
// question.
func (s *stream) summary() Summary {
	text := harness.StripClarification(s.text.String())
	final := harness.StripClarification(s.final)
	if strings.TrimSpace(final) == "" {
		final = strings.TrimSpace(text)
	}
	if strings.TrimSpace(text) == "" {
		text = final
	}
	return Summary{Text: text, Final: final, Usage: s.usage}
}

// position folds the stream's cursor into a position.
func (s *stream) position(pos Position) Position {
	if s.cursor > pos.Cursor {
		pos.Cursor = s.cursor
	}
	return pos
}

func maxEpoch(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
