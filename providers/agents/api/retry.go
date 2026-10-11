// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Retrying unattended runs.
//
// A turn can fail before it runs: the runner the harness lives on is at
// capacity, or is not ready yet. Nothing executed, nobody is holding a
// connection open for the answer, and the condition clears on its own — so
// failing the run and telling a channel "runner execution capacity is full" is
// the wrong answer for a schedule fire or a webhook delivery. Instead the run
// is handed back to the queue it came from: Phase Pending, a RetryAt the Run
// reconciler wakes on, and a Retries count the executor bounds. There is no
// queue besides the Run object and no timer besides the reconciler's computed
// requeue, which is the contract for every unattended run (AGENTS.md §5.8).
//
// The classification is deliberately narrow. What is retried is a REFUSAL the
// runner protocol marks retryable and that carries no receipt — "refused,
// nothing ran", the same distinction every dispatch fence rests on — because
// re-dispatching anything else risks doing work twice: a transport error on
// Start may have started an attempt, and a turn that ran and failed has done
// whatever it did. Those stay terminal, as does anything on an attended run,
// where the person waiting gets the error instead.
//
// The runner has one slot. A queue of refused runs that all wake on the same
// timer is a herd: every wake-up dispatches all of them, one wins, the rest are
// refused again — each refusal a round-trip, a retry spent and a longer wait —
// and the slot sits idle between wake-ups. Two things keep the queue moving at
// the runner's rate instead of the backoff's. A refused run's wake-up is
// staggered by its position in the queue, so runs refused together come back
// one at a time. And when a run on the agent finishes, the oldest queued run
// is woken at once (wakeNextQueued): the slot it needs has just been freed.
// One run, not all — waking them all is the herd again. If something else
// takes the slot first, the woken run is refused and handed back as usual.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/dispatch"

	runctrl "github.com/railgrid/provider-agents/controller/run"
	"github.com/railgrid/provider-agents/store"
)

const (
	// defaultMaxRunAttempts bounds how many times an unattended run is tried
	// when its schedule names no spec.retry.maxAttempts (triggers and channel
	// messages have no policy of their own and always use it). With the
	// backoff below the last attempt lands roughly two hours after the first,
	// which outlasts one harness turn on a single-execution runner.
	defaultMaxRunAttempts = 24
	// runRetryBaseBackoff is the wait before the first retry; each retry
	// doubles it up to runRetryMaxBackoff. Short at first because a busy runner
	// often frees up within a minute; capped because a run that has waited an
	// hour gains nothing from waiting another fifteen minutes to check.
	runRetryBaseBackoff = time.Minute
	runRetryMaxBackoff  = 5 * time.Minute
	// runRetryStagger spaces the wake-ups of runs queued behind each other on
	// the same agent: the nth run in the queue waits n times this on top of
	// its backoff, bounded by runRetryMaxBackoff. A turn takes tens of seconds,
	// so this is enough for the run ahead to have started before the next
	// asks, and short enough not to matter once wakeNextQueued is doing the
	// pacing.
	runRetryStagger = 15 * time.Second
)

// runRetryBackoff is how long the nth retry (1-based) waits before the run may
// be claimed again.
func runRetryBackoff(retry int) time.Duration {
	backoff := runRetryBaseBackoff
	for i := 1; i < retry && backoff < runRetryMaxBackoff; i++ {
		backoff *= 2
	}
	return min(backoff, runRetryMaxBackoff)
}

// retryableRefusal reports whether err is a runner refusal that left nothing
// running and is expected to clear on its own, and names what refused.
//
// A cancelled or timed-out context is never one: the person (or the deadline)
// ended the run, and a retry would undo that. A refusal that carries a receipt
// is never one either, because a receipt means an attempt exists on the runner
// and a fresh start would collide with it.
func retryableRefusal(err error) (string, bool) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", false
	}
	refusal, ok := dispatch.Refusal(err)
	if !ok || !refusal.Retryable || refusal.Receipt != nil {
		return "", false
	}
	switch refusal.Code {
	case runner.ErrorBusy, runner.ErrorUnavailable:
		return refusal.Message, true
	}
	return "", false
}

// runRequeued is the error a turn returns when its run was handed back to the
// queue instead of failed. It wraps the refusal so a log line says why, and the
// background handler recognises it so the schedule's failure counter and the
// agent's channel are left alone — a retry is not an outcome.
type runRequeued struct {
	cause   error
	at      time.Time
	attempt int // the attempt that was refused, 1-based
	max     int
}

func (e *runRequeued) Error() string {
	return fmt.Sprintf("re-queued after attempt %d of %d; retrying at %s: %v",
		e.attempt, e.max, e.at.UTC().Format(time.RFC3339), e.cause)
}

func (e *runRequeued) Unwrap() error { return e.cause }

// retryRun hands an unattended run back to the queue after a refusal that left
// nothing running, and reports whether it did.
//
// It did not when the run is attended (the caller is waiting and gets the
// error), when the failure is not a retryable refusal, when the run was
// cancelled meanwhile, or when its attempts are spent — and then the caller
// fails the run exactly as it would have. The store row is the authority and
// is re-read here rather than trusted from the turn: the retry count on it is
// what bounds the loop, and a cancel that landed during the refused attempt
// must win.
func (s *Server) retryRun(ctx context.Context, scope store.Scope, run taskRun, cause error, at time.Time) (*runRequeued, bool) {
	if !runctrl.UnattendedKinds[run.DeliveryKind] {
		return nil, false
	}
	refused, ok := retryableRefusal(cause)
	if !ok {
		return nil, false
	}
	persistCtx, cancel := boundedPersistContext(ctx)
	defer cancel()
	stored, err := s.store.GetRun(persistCtx, scope, run.RunID)
	if err != nil || stored.CancelRequested {
		return nil, false
	}
	maxAttempts := int(run.MaxAttempts)
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxRunAttempts
	}
	attempt := stored.Retries + 1 // the one that was just refused
	if attempt >= maxAttempts {
		return nil, false
	}
	stored.Retries++
	retryAt := at.Add(runRetryBackoff(stored.Retries)).Add(s.queueStagger(persistCtx, scope, run.RunID))
	stored.Phase = store.RunPhasePending
	stored.RetryAt = &retryAt
	stored.StartedAt = nil
	stored.FinishedAt = nil
	stored.Checkpoint = nil
	stored.Message = fmt.Sprintf("attempt %d of %d refused: %s; retrying at %s",
		attempt, maxAttempts, refused, retryAt.UTC().Format(time.RFC3339))
	stored.UpdatedAt = at
	if err := s.saveRun(persistCtx, scope, stored); err != nil {
		log.Printf("run %s: handing back to the queue for retry: %v", run.RunID, err)
		return nil, false
	}
	return &runRequeued{cause: cause, at: retryAt, attempt: attempt, max: maxAttempts}, true
}

// queueStagger is the extra wait a run handed back now takes so it wakes after
// the runs already queued ahead of it, rather than with them. The count is read
// from the store, so runs refused in the same instant see each other as they
// land; a read that fails staggers nothing, which is only the old behaviour.
func (s *Server) queueStagger(ctx context.Context, scope store.Scope, runID string) time.Duration {
	queued, err := s.store.ListQueuedRuns(ctx, scope)
	if err != nil {
		return 0
	}
	ahead := 0
	for _, other := range queued {
		if other.ID != runID {
			ahead++
		}
	}
	return min(time.Duration(ahead)*runRetryStagger, runRetryMaxBackoff)
}

// wakeNextQueued brings the oldest queued run of an agent forward to now. It
// is called when a run on that agent finishes, which is when the runner's slot
// is known to be free; the Run reconciler sees the moved wake-up on the object
// and claims the run. Runs already due are left alone — the reconciler has
// them — and exactly one run is woken, because waking every queued run is the
// herd this exists to avoid. A failure here costs nothing but the head start:
// the run's own wake-up still stands.
func (s *Server) wakeNextQueued(ctx context.Context, scope store.Scope, agentName string, now time.Time) {
	if s == nil || s.store == nil || agentName == "" {
		return
	}
	scope.AgentName = agentName
	queued, err := s.store.ListQueuedRuns(ctx, scope)
	if err != nil {
		log.Printf("agent %s: reading the queue after a run finished: %v", agentName, err)
		return
	}
	for _, run := range queued {
		if run.RetryAt == nil || !run.RetryAt.After(now) {
			continue
		}
		wakeAt := now
		run.RetryAt = &wakeAt
		run.UpdatedAt = now
		message := run.Message
		if i := strings.LastIndex(message, "; retrying at "); i >= 0 {
			message = message[:i]
		}
		run.Message = message + "; a run on this agent finished, retrying now"
		if err := s.saveRun(ctx, scope, run); err != nil {
			log.Printf("run %s: waking it after a run finished: %v", run.ID, err)
			return
		}
		s.publishRunEvent(scope, runEvent{ID: run.ID, Agent: run.AgentName, Trigger: run.Trigger, ParentRunID: run.ParentRunID, Phase: store.RunPhasePending})
		log.Printf("run %s: woken early, a run on agent %s finished", run.ID, agentName)
		return
	}
}

// withoutRun drops the rows one run wrote from a session's history. A retried
// run assembles its turn over the history that preceded it; its own refused
// attempt — the task row and the re-queued marker — is not part of that.
func withoutRun(history []store.Message, runID string) []store.Message {
	if runID == "" {
		return history
	}
	kept := make([]store.Message, 0, len(history))
	for _, message := range history {
		if message.RunID != runID {
			kept = append(kept, message)
		}
	}
	return kept
}
