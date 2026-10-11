// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/dispatch"

	"github.com/railgrid/provider-agents/store"
)

// busyDispatcher is a runner at capacity: every Start is refused with the
// protocol's retryable busy error and nothing is created. Since nothing was
// created, there is nothing to cancel, inspect or follow either — those answer
// as a runner that has never heard of the attempt would.
type busyDispatcher struct {
	starts int
}

func (d *busyDispatcher) Start(context.Context, runner.StartRequest) (runner.Receipt, error) {
	d.starts++
	return runner.Receipt{}, &runner.Error{Code: runner.ErrorBusy, Retryable: true, Message: "runner execution capacity is full"}
}

func (*busyDispatcher) Resume(context.Context, runner.ResumeRequest) (runner.Receipt, error) {
	return runner.Receipt{}, unknownAttempt()
}

func (*busyDispatcher) Cancel(context.Context, runner.CancelRequest) (runner.Receipt, error) {
	return runner.Receipt{}, unknownAttempt()
}

func (*busyDispatcher) Inspect(context.Context, string) (runner.Receipt, error) {
	return runner.Receipt{}, unknownAttempt()
}

func (*busyDispatcher) Events(context.Context, string, uint64) (dispatch.Stream, error) {
	return nil, unknownAttempt()
}

func unknownAttempt() error {
	return &runner.Error{Code: runner.ErrorUnavailable, Retryable: false, Message: "attempt not found"}
}

// queuedRun records the Pending row Submit would have written for an
// unattended run, so the turn under test is a claimed dispatch of it.
func queuedRun(t *testing.T, s *Server, run taskRun, retries int) {
	t.Helper()
	now := time.Now().UTC()
	if err := s.store.SaveRun(t.Context(), run.Scope, store.Run{
		ID: run.RunID, AgentName: run.Agent.Name, SessionID: run.SessionID, Trigger: run.Trigger,
		Phase: store.RunPhasePending, Input: run.Task, Retries: retries, CreatedAt: now, UpdatedAt: now,
		Delivery: &store.RunDelivery{Kind: run.DeliveryKind, SourceName: run.SourceName},
	}); err != nil {
		t.Fatal(err)
	}
}

// A runner at capacity is not an outcome for a trigger-fired run. The run goes
// back to the queue it came from — Pending, with a wake-up time and one retry
// counted — instead of being failed and reported to a channel.
func TestABusyRunnerRequeuesAnUnattendedRun(t *testing.T) {
	dispatcher := &busyDispatcher{}
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 60)
	run.DeliveryKind, run.SourceName = "trigger", "pull-request"
	run.Retries = 0
	queuedRun(t, s, run, 0)

	before := time.Now().UTC()
	result, err := s.runTurn(t.Context(), run, nil)
	var requeued *runRequeued
	if !errors.As(err, &requeued) {
		t.Fatalf("runTurn error = %v; want the run re-queued", err)
	}
	if result.Phase != store.RunPhasePending {
		t.Fatalf("runTurn phase = %q; want Pending", result.Phase)
	}
	if dispatcher.starts != 1 {
		t.Fatalf("runner Start called %d times; the turn must not retry in place", dispatcher.starts)
	}
	stored, err := s.store.GetRun(t.Context(), run.Scope, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != store.RunPhasePending || stored.Retries != 1 {
		t.Fatalf("stored run = phase %q retries %d; want Pending with one retry", stored.Phase, stored.Retries)
	}
	if stored.RetryAt == nil || stored.RetryAt.Before(before.Add(runRetryBaseBackoff)) || stored.RetryAt.After(time.Now().Add(runRetryBaseBackoff+time.Minute)) {
		t.Fatalf("retryAt = %v; want about %v after the refusal", stored.RetryAt, runRetryBaseBackoff)
	}
	if stored.StartedAt != nil || stored.FinishedAt != nil || len(stored.Checkpoint) != 0 {
		t.Fatalf("a re-queued run must look unstarted: startedAt=%v finishedAt=%v checkpoint=%d bytes", stored.StartedAt, stored.FinishedAt, len(stored.Checkpoint))
	}
	if requeued.attempt != 1 || requeued.max != defaultMaxRunAttempts {
		t.Fatalf("requeued = attempt %d of %d; want 1 of the provider default", requeued.attempt, requeued.max)
	}
}

// The bound is the bound. A run refused on its last permitted attempt is
// failed like any other failure, with the refusal as its message.
func TestABusyRunnerFailsARunOutOfAttempts(t *testing.T) {
	dispatcher := &busyDispatcher{}
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 60)
	run.DeliveryKind, run.SourceName = "trigger", "pull-request"
	run.Retries = defaultMaxRunAttempts - 1
	queuedRun(t, s, run, run.Retries)

	result, err := s.runTurn(t.Context(), run, nil)
	var requeued *runRequeued
	if errors.As(err, &requeued) {
		t.Fatalf("runTurn re-queued the run on its last attempt: %v", err)
	}
	if result.Phase != store.RunPhaseFailed {
		t.Fatalf("runTurn phase = %q; want Failed", result.Phase)
	}
	stored, err := s.store.GetRun(t.Context(), run.Scope, run.RunID)
	if err != nil || stored.Phase != store.RunPhaseFailed || stored.Retries != defaultMaxRunAttempts-1 {
		t.Fatalf("stored run = %+v, %v; want Failed with the retry count untouched", stored, err)
	}
}

// A schedule's own policy wins over the provider default. maxAttempts: 1 is
// "never retry".
func TestASchedulesRetryPolicyBoundsTheRetries(t *testing.T) {
	dispatcher := &busyDispatcher{}
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 60)
	run.DeliveryKind, run.SourceName, run.MaxAttempts = "schedule", "nightly", 1
	queuedRun(t, s, run, 0)

	result, err := s.runTurn(t.Context(), run, nil)
	var requeued *runRequeued
	if errors.As(err, &requeued) || result.Phase != store.RunPhaseFailed {
		t.Fatalf("runTurn = %q, %v; a schedule with maxAttempts 1 must not be retried", result.Phase, err)
	}
}

// Somebody is waiting on an attended run. They get the error now, not a retry
// they cannot see, and the reconciler never claims a run with no unattended
// delivery kind anyway.
func TestABusyRunnerFailsAnAttendedRun(t *testing.T) {
	dispatcher := &busyDispatcher{}
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 60)
	queuedRun(t, s, run, 0)

	result, err := s.runTurn(t.Context(), run, nil)
	var requeued *runRequeued
	if errors.As(err, &requeued) || result.Phase != store.RunPhaseFailed {
		t.Fatalf("runTurn = %q, %v; an attended run is never re-queued", result.Phase, err)
	}
}

// A cancel that landed while the runner was refusing wins over the retry: the
// person ended the run, and a retry would undo that.
func TestACancelledRunIsNotRequeued(t *testing.T) {
	dispatcher := &busyDispatcher{}
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 60)
	run.DeliveryKind, run.SourceName = "trigger", "pull-request"
	queuedRun(t, s, run, 0)
	cancelling := &cancelOnRunningStore{Store: s.store}
	s.store = cancelling

	result, err := s.runTurn(t.Context(), run, nil)
	var requeued *runRequeued
	if errors.As(err, &requeued) {
		t.Fatalf("runTurn re-queued a cancelled run: %v", err)
	}
	if result.Phase == store.RunPhasePending {
		t.Fatalf("runTurn phase = Pending; a cancelled run must settle")
	}
}

// cancelOnRunningStore marks the run cancelled the moment it is recorded
// Running — a cancel racing the dispatch.
type cancelOnRunningStore struct {
	store.Store
}

func (s *cancelOnRunningStore) SaveRun(ctx context.Context, scope store.Scope, run store.Run) error {
	if err := s.Store.SaveRun(ctx, scope, run); err != nil {
		return err
	}
	if run.Phase == store.RunPhaseRunning {
		return s.RequestCancel(ctx, scope, run.ID, time.Now().UTC())
	}
	return nil
}

// Only a refusal that left nothing running is retried. A refusal with a
// receipt names an attempt that exists; a transport error may have started
// one; a cancelled context was the caller's decision.
func TestRetryableRefusalIsNarrow(t *testing.T) {
	busy := &runner.Error{Code: runner.ErrorBusy, Retryable: true, Message: "runner execution capacity is full"}
	cases := map[string]struct {
		err  error
		want bool
	}{
		"busy":                    {dispatch.Describe("starting the harness turn", busy), true},
		"unavailable, retryable":  {&runner.Error{Code: runner.ErrorUnavailable, Retryable: true}, true},
		"unavailable, final":      {&runner.Error{Code: runner.ErrorUnavailable, Retryable: false}, false},
		"busy with a receipt":     {&runner.Error{Code: runner.ErrorBusy, Retryable: true, Receipt: &runner.Receipt{AttemptID: "a"}}, false},
		"stale attempt":           {&runner.Error{Code: runner.ErrorStaleAttempt, Retryable: false}, false},
		"cursor expired":          {&runner.Error{Code: runner.ErrorCursorExpired, Retryable: true}, false},
		"transport":               {errors.New("runner transport failed; Inspect before retrying a mutation: dial tcp: refused"), false},
		"cancelled":               {context.Canceled, false},
		"deadline":                {context.DeadlineExceeded, false},
		"nil":                     {nil, false},
		"turn ran and failed":     {errors.New("the harness exited 1"), false},
		"clone credential absent": {errors.New("clone credential not available after restart; retry"), false},
	}
	for name, tc := range cases {
		if _, got := retryableRefusal(tc.err); got != tc.want {
			t.Errorf("%s: retryableRefusal = %t, want %t", name, got, tc.want)
		}
	}
}

func TestRunRetryBackoffDoublesToTheCap(t *testing.T) {
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := runRetryBackoff(i + 1); got != w {
			t.Errorf("retry %d: backoff = %v, want %v", i+1, got, w)
		}
	}
}

// A retried run assembles its turn over the history that preceded it; its own
// refused attempt is not part of that.
func TestWithoutRunDropsOnlyThatRunsRows(t *testing.T) {
	history := []store.Message{
		{ID: "1", RunID: "earlier", Role: "user", Content: "yesterday's task"},
		{ID: "2", RunID: "earlier", Role: "assistant", Content: "done"},
		{ID: "3", RunID: "this", Role: "user", Content: "today's task"},
		{ID: "4", RunID: "this", Role: "assistant", Content: "re-queued"},
	}
	kept := withoutRun(history, "this")
	if len(kept) != 2 || kept[0].ID != "1" || kept[1].ID != "2" {
		t.Fatalf("withoutRun kept %+v; want only the earlier run's rows", kept)
	}
	if got := withoutRun(history, ""); len(got) != len(history) {
		t.Fatalf("withoutRun with no run id dropped rows: %d of %d", len(got), len(history))
	}
}

// queuedSibling records another run of the same agent that is already waiting
// in the queue for its wake-up time.
func queuedSibling(t *testing.T, s *Server, run taskRun, id string, createdAt, retryAt time.Time) {
	t.Helper()
	if err := s.store.SaveRun(t.Context(), run.Scope, store.Run{
		ID: id, AgentName: run.Agent.Name, SessionID: run.SessionID, Trigger: run.Trigger,
		Phase: store.RunPhasePending, Input: "queued", Retries: 1, RetryAt: &retryAt, CreatedAt: createdAt, UpdatedAt: createdAt,
		Message:  "attempt 1 of 24 refused: runner execution capacity is full; retrying at " + retryAt.UTC().Format(time.RFC3339),
		Delivery: &store.RunDelivery{Kind: "trigger", SourceName: "pull-request"},
	}); err != nil {
		t.Fatal(err)
	}
}

// Runs refused together must not wake together: the one-slot runner can take
// one of them, and the rest would only be refused again. A run handed back
// behind others already in the queue waits a stagger per run ahead of it.
func TestARefusedRunIsStaggeredBehindTheQueue(t *testing.T) {
	dispatcher := &busyDispatcher{}
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 60)
	run.DeliveryKind, run.SourceName = "trigger", "pull-request"
	queuedRun(t, s, run, 0)
	now := time.Now().UTC()
	queuedSibling(t, s, run, "ahead-1", now.Add(-2*time.Minute), now.Add(time.Minute))
	queuedSibling(t, s, run, "ahead-2", now.Add(-time.Minute), now.Add(time.Minute))

	before := time.Now().UTC()
	if _, err := s.runTurn(t.Context(), run, nil); err == nil {
		t.Fatal("runTurn must report the re-queue")
	}
	stored, err := s.store.GetRun(t.Context(), run.Scope, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	earliest := before.Add(runRetryBaseBackoff + 2*runRetryStagger)
	if stored.RetryAt == nil || stored.RetryAt.Before(earliest) || stored.RetryAt.After(time.Now().Add(runRetryBaseBackoff+2*runRetryStagger+time.Minute)) {
		t.Fatalf("retryAt = %v; want the backoff plus two staggers after %v", stored.RetryAt, before)
	}
}

// When a run on the agent finishes, its runner slot is free, and the oldest run
// waiting in the queue is woken to take it rather than left to its timer. One
// run only: the rest keep their wake-ups, and a run that is due already is the
// reconciler's business.
func TestAFinishedRunWakesTheOldestQueuedRun(t *testing.T) {
	s, run := newHarnessRunLifecycleFixture(t, &busyDispatcher{}, 60)
	now := time.Now().UTC()
	queuedSibling(t, s, run, "due-already", now.Add(-10*time.Minute), now.Add(-time.Minute))
	queuedSibling(t, s, run, "oldest-waiting", now.Add(-5*time.Minute), now.Add(5*time.Minute))
	queuedSibling(t, s, run, "newer-waiting", now.Add(-time.Minute), now.Add(5*time.Minute))
	if err := s.store.SaveRun(t.Context(), run.Scope, store.Run{
		ID: "finishing", AgentName: run.Agent.Name, SessionID: run.SessionID, Trigger: run.Trigger,
		Phase: store.RunPhaseRunning, Input: "review", CreatedAt: now, UpdatedAt: now, StartedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	end := now.Add(40 * time.Second)
	s.finishRun(t.Context(), run.Scope, "finishing", runOutcome{Phase: store.RunPhaseSucceeded}, end)

	woken, err := s.store.GetRun(t.Context(), run.Scope, "oldest-waiting")
	if err != nil {
		t.Fatal(err)
	}
	if woken.RetryAt == nil || !woken.RetryAt.Equal(end) || woken.Phase != store.RunPhasePending {
		t.Fatalf("oldest waiting run = phase %q retryAt %v; want Pending, woken at %v", woken.Phase, woken.RetryAt, end)
	}
	if !strings.HasSuffix(woken.Message, "a run on this agent finished, retrying now") || strings.Contains(woken.Message, "retrying at") {
		t.Fatalf("message = %q; want the wait's explanation replaced by the wake-up's", woken.Message)
	}
	for _, id := range []string{"due-already", "newer-waiting"} {
		other, err := s.store.GetRun(t.Context(), run.Scope, id)
		if err != nil {
			t.Fatal(err)
		}
		if other.RetryAt == nil || other.RetryAt.Equal(end) || !strings.Contains(other.Message, "retrying at") {
			t.Errorf("%s = retryAt %v message %q; must be left alone", id, other.RetryAt, other.Message)
		}
	}
}
