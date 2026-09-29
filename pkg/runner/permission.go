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

package runner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// The runner's half of the permission round-trip.
//
// A clarification park and a permission park look the same from outside — the
// attempt is needs_input and a human has to act — but underneath they are
// opposites. A clarification park happens because the harness turn ENDED: the
// child is gone, the resume launches a new one, and the session is what carries
// the conversation across. A permission park happens because the harness turn
// is still in the middle of a tool call: the child is alive, the call is open,
// and the resume has to reach back INTO it. Relaunching would abandon a turn
// that is only waiting.
//
// So a permission park keeps the execution goroutine running and blocked, and
// the resume hands a verdict to the blocked call rather than starting anything.
// Two consequences follow and are handled here:
//
//   - The attempt's duration limit bounds WORK, not waiting on a human. A
//     workClock stops while parked, or a turn parked overnight would be killed
//     by a limit that was written for a coding turn.
//
//   - The gate lives in memory, like the credential, and for the same reason:
//     it is a live process's waiting state, not durable state. A runner that
//     restarts loses the child too, so there is nothing for a persisted gate to
//     point at.

// permissionGate is one attempt's live permission channel. Only one request is
// ever outstanding at a time: the harness child is blocked on the tool call it
// asked about, so it cannot be asking about two things at once.
type permissionGate struct {
	mu        sync.Mutex
	requestID string
	answer    chan harness.PermissionVerdict
	clock     *workClock
}

// open registers a request and returns the channel its verdict arrives on.
func (g *permissionGate) open(requestID string) chan harness.PermissionVerdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	answer := make(chan harness.PermissionVerdict, 1)
	g.requestID = requestID
	g.answer = answer
	return answer
}

// close forgets the outstanding request, whatever became of it.
func (g *permissionGate) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requestID = ""
	g.answer = nil
}

// deliver hands a verdict to the waiting call. It reports false when the gate
// is not waiting on requestID — a replayed or mis-addressed resume — so the
// caller can refuse rather than answer the wrong question.
func (g *permissionGate) deliver(requestID string, verdict harness.PermissionVerdict) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.answer == nil || g.requestID == "" || g.requestID != requestID {
		return false
	}
	// Buffered by one and cleared here, so a second delivery for the same id
	// cannot block and cannot be applied twice.
	g.answer <- verdict
	g.requestID = ""
	g.answer = nil
	return true
}

// workClock enforces an attempt's duration limit against time spent WORKING.
//
// context.WithTimeout cannot express this: its deadline is wall clock, and an
// attempt parked on a human is not consuming the budget the limit was written
// for. So the limit is a timer the gate stops when the attempt parks and
// restarts, with what is left, when the verdict arrives. An attempt with no
// limit gets a nil clock and every method is a no-op.
type workClock struct {
	mu        sync.Mutex
	remaining time.Duration
	startedAt time.Time
	timer     *time.Timer
	running   bool
	stopped   bool
	ranOut    bool
}

// newWorkClock starts the limit. expire is called once, from the timer, when
// the working budget runs out.
func newWorkClock(limit time.Duration, expire func()) *workClock {
	if limit <= 0 {
		return nil
	}
	c := &workClock{remaining: limit, startedAt: time.Now(), running: true}
	c.timer = time.AfterFunc(limit, func() {
		c.mu.Lock()
		c.ranOut = true
		c.mu.Unlock()
		expire()
	})
	return c
}

// expired reports that the working budget ran out, as opposed to the context
// having been cancelled for any other reason. The clock cancels the execution
// context itself, so without this the runner could not tell a limit from a
// shutdown — and would report an overrun as a resumable interruption.
func (c *workClock) expired() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ranOut
}

// pause stops the clock while the attempt waits on a human.
func (c *workClock) pause() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || !c.running {
		return
	}
	c.running = false
	c.timer.Stop()
	c.remaining -= time.Since(c.startedAt)
	if c.remaining < 0 {
		c.remaining = 0
	}
}

// unpause restarts the clock with whatever budget is left.
func (c *workClock) unpause() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || c.running {
		return
	}
	c.running = true
	c.startedAt = time.Now()
	c.timer.Reset(c.remaining)
}

// stop ends the limit for good, when the attempt's execution is over.
func (c *workClock) stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	c.running = false
	c.timer.Stop()
}

// attemptPermissions is the harness.PermissionAsker the runner hands one
// launch. It is bound to one attempt and does nothing for any other.
type attemptPermissions struct {
	runner    *Runner
	attemptID string
}

var _ harness.PermissionAsker = (*attemptPermissions)(nil)

// AskPermission parks the attempt on request and blocks until a human answers
// or ctx ends.
//
// A ctx-done return is a DENIAL, not an error: the turn is being torn down, and
// the adapter needs an answer to give the harness so its child can finish
// rather than being left blocked on a call nobody will ever settle.
func (p *attemptPermissions) AskPermission(ctx context.Context, request harness.PermissionRequest) (harness.PermissionVerdict, error) {
	request = boundedPermissionRequest(request)
	if !validPermissionRequest(request) {
		return harness.PermissionVerdict{}, errors.New("harness permission request is malformed")
	}
	answer, err := p.runner.parkOnPermission(p.attemptID, request)
	if err != nil {
		return harness.PermissionVerdict{}, err
	}
	select {
	case verdict := <-answer:
		return verdict, nil
	case <-ctx.Done():
		p.runner.abandonPermission(p.attemptID, request.ID)
		return harness.PermissionVerdict{
			Allow:   false,
			Message: "The runner stopped this turn before anyone could answer; the call was not approved.",
		}, nil
	}
}

// parkOnPermission records the outstanding request on the receipt, moves the
// attempt to needs_input, stops the working clock, and returns the channel the
// verdict will arrive on.
func (r *Runner) parkOnPermission(attemptID string, request harness.PermissionRequest) (chan harness.PermissionVerdict, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	attempt, ok := r.state.Attempts[attemptID]
	if !ok {
		return nil, errors.New("attempt no longer exists")
	}
	if attempt.Receipt.Phase.IsTerminal() || attempt.Receipt.Phase == PhaseCancelling {
		return nil, errors.New("attempt is no longer accepting a permission request")
	}
	gate := r.permissions[attemptID]
	if gate == nil {
		return nil, errors.New("attempt has no permission gate in this process")
	}
	answer := gate.open(request.ID)
	gate.clock.pause()
	attempt.Receipt.Phase = PhaseNeedsInput
	attempt.Receipt.Blocker = permissionBlocker(request)
	// Exactly one of the two is ever set: a caller branches on which, and a
	// stale clarification alongside a permission park would be answered with
	// text that goes nowhere.
	attempt.Receipt.Clarification = nil
	attempt.Receipt.Permission = &PermissionRequest{ID: request.ID, Tool: request.Tool, Input: request.Input}
	attempt.Receipt.UpdatedAt = eventNow()
	if _, err := r.appendEventLocked(attemptID, EventNeedsInput, attempt.Receipt.Blocker, nil); err != nil {
		gate.close()
		gate.clock.unpause()
		return nil, err
	}
	if err := r.persistLocked(); err != nil {
		gate.close()
		gate.clock.unpause()
		return nil, err
	}
	return answer, nil
}

// abandonPermission clears an outstanding request whose asker gave up. The
// phase is left to the execution path: the adapter is about to return, and that
// is what decides how the attempt ended.
func (r *Runner) abandonPermission(attemptID, requestID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if gate := r.permissions[attemptID]; gate != nil {
		gate.close()
		gate.clock.unpause()
	}
	attempt, ok := r.state.Attempts[attemptID]
	if !ok {
		return
	}
	if attempt.Receipt.Permission != nil && attempt.Receipt.Permission.ID == requestID {
		attempt.Receipt.Permission = nil
		attempt.Receipt.UpdatedAt = eventNow()
		_ = r.persistLocked()
	}
}

// answerPermissionLocked delivers a verdict to the blocked tool call and puts
// the attempt back to running. The caller holds r.mu.
func (r *Runner) answerPermissionLocked(attempt *attemptRecord, requestID string, verdict harness.PermissionVerdict) error {
	attemptID := attempt.Receipt.AttemptID
	gate := r.permissions[attemptID]
	if gate == nil {
		return errors.New("the harness is no longer waiting on that permission request")
	}
	if !gate.deliver(requestID, verdict) {
		return errors.New("the harness is no longer waiting on that permission request")
	}
	gate.clock.unpause()
	attempt.Receipt.Permission = nil
	attempt.Receipt.Phase = PhaseRunning
	attempt.Receipt.Blocker = ""
	attempt.Receipt.LastError = nil
	attempt.Receipt.UpdatedAt = eventNow()
	message := "permission granted"
	if !verdict.Allow {
		message = "permission denied"
	}
	if _, err := r.appendEventLocked(attemptID, EventProgress, message, nil); err != nil {
		return err
	}
	return r.persistLocked()
}

// permissionBlocker is the one-line reason a caller shows for the park. It
// names the tool and nothing else: the input is on the receipt, where a caller
// that renders it can bound it itself.
func permissionBlocker(request harness.PermissionRequest) string {
	return "the harness is asking permission to use " + request.Tool
}

// boundedPermissionRequest trims a request to what may travel. An adapter is
// supposed to bound its own, but this is the seam between a child process's
// output and everything durable, so it is enforced here too.
func boundedPermissionRequest(request harness.PermissionRequest) harness.PermissionRequest {
	request.ID = strings.TrimSpace(request.ID)
	request.Tool = strings.TrimSpace(request.Tool)
	if len(request.Input) > harness.MaxPermissionInputBytes {
		cut := harness.MaxPermissionInputBytes
		for cut > 0 && !utf8.RuneStart(request.Input[cut]) {
			cut--
		}
		request.Input = request.Input[:cut]
	}
	return request
}

func validPermissionRequest(request harness.PermissionRequest) bool {
	if !validClarificationID(request.ID) {
		return false
	}
	if request.Tool == "" || len(request.Tool) > maxIdentifierBytes || !utf8.ValidString(request.Tool) {
		return false
	}
	return utf8.ValidString(request.Input)
}

func clonePermission(value *PermissionRequest) *PermissionRequest {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
