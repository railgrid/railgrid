// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package run

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// requeued is a run its executor handed back after a transient refusal: still
// owned by the process that ran it, Pending again, with a wake-up time.
func requeued(name string, retryAt time.Time, mutate func(*agentsv1alpha1.Run)) *agentsv1alpha1.Run {
	return unattended(name, func(o *agentsv1alpha1.Run) {
		o.Status.Owner = "pod-a"
		o.Status.ClaimedAt = &metav1.Time{Time: started.Add(-time.Minute)}
		o.Status.Attempt = 1
		o.Status.Retries = 1
		o.Status.RetryAt = &metav1.Time{Time: retryAt}
		o.Status.Message = "attempt 1 of 24 refused: runner execution capacity is full; retrying at " + retryAt.Format(time.RFC3339)
		if mutate != nil {
			mutate(o)
		}
	})
}

// A run handed back for a retry waits for its wake-up time, and the reconciler
// asks to be woken exactly then — not before, and not at the next unrelated
// event. While it waits, the object says why.
func TestARequeuedRunWaitsForItsWakeUp(t *testing.T) {
	d := &dispatcher{}
	r := &Reconciler{Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch}
	c, result := run(t, r, requeued("r1", started.Add(3*time.Minute), nil), newAgent(600))

	if len(d.runs) != 0 {
		t.Fatalf("dispatched %v before the run's wake-up time", d.runs)
	}
	if result.RequeueAfter != 3*time.Minute {
		t.Errorf("RequeueAfter = %v, want the time until retryAt (3m)", result.RequeueAfter)
	}
	got := get(t, c, "r1")
	cond := meta.FindStatusCondition(got.Status.Conditions, agentsv1alpha1.ConditionValidated)
	if cond == nil || cond.Reason != agentsv1alpha1.ReasonRunRetryScheduled || cond.Message != got.Status.Message {
		t.Errorf("condition = %+v, want RetryScheduled carrying the executor's message", cond)
	}
	if got.Status.RetryAt == nil || got.Status.Owner != "pod-a" || got.Status.Attempt != 1 {
		t.Errorf("waiting run = owner %q attempt %d retryAt %v; the claim fields must be untouched while it waits", got.Status.Owner, got.Status.Attempt, got.Status.RetryAt)
	}
}

// At its wake-up time the run is claimed again — by the same process that ran
// it last, which an ordinary claim would refuse as "our own claim" — and the
// retry opens a fresh pickup series: attempt 1, no wake-up, no stale deadline,
// no explanation for a wait that is over.
func TestARequeuedRunIsClaimedAtItsWakeUp(t *testing.T) {
	d := &dispatcher{}
	r := &Reconciler{Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch}
	object := requeued("r1", started.Add(-time.Second), func(o *agentsv1alpha1.Run) {
		o.Status.DeadlineAt = &metav1.Time{Time: started.Add(-time.Hour)}
		meta.SetStatusCondition(&o.Status.Conditions, metav1.Condition{
			Type: agentsv1alpha1.ConditionValidated, Status: metav1.ConditionFalse,
			Reason: agentsv1alpha1.ReasonRunRetryScheduled, Message: o.Status.Message,
		})
	})
	c, _ := run(t, r, object, newAgent(600))

	if len(d.runs) != 1 || d.runs[0] != "r1" {
		t.Fatalf("dispatched %v, want [r1]", d.runs)
	}
	got := get(t, c, "r1")
	if got.Status.Owner != "pod-a" || got.Status.ClaimedAt == nil || !got.Status.ClaimedAt.Time.Equal(started) {
		t.Errorf("claim = owner %q at %v, want this process now", got.Status.Owner, got.Status.ClaimedAt)
	}
	if got.Status.Attempt != 1 {
		t.Errorf("attempt = %d, want the series to start over at 1", got.Status.Attempt)
	}
	if got.Status.RetryAt != nil {
		t.Errorf("retryAt = %v, want it consumed by the claim", got.Status.RetryAt)
	}
	if got.Status.DeadlineAt != nil {
		t.Errorf("deadlineAt = %v, want the previous attempt's deadline cleared", got.Status.DeadlineAt)
	}
	if got.Status.Retries != 1 {
		t.Errorf("retries = %d, want the executor's count left alone", got.Status.Retries)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, agentsv1alpha1.ConditionValidated); cond != nil {
		t.Errorf("condition = %+v, want the RetryScheduled explanation removed with the wait", cond)
	}
}

// The hand-back was orderly, so the previous owner is not consulted: a run a
// former leader re-queued is claimed at its wake-up time even though that
// claim is well inside the grace.
func TestARequeuedRunIsClaimedWhoeverOwnedIt(t *testing.T) {
	d := &dispatcher{}
	r := &Reconciler{Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch}
	object := requeued("r1", started, func(o *agentsv1alpha1.Run) {
		o.Status.Owner = "pod-b"
		o.Status.ClaimedAt = &metav1.Time{Time: started.Add(-10 * time.Second)}
	})
	c, _ := run(t, r, object, newAgent(600))

	if len(d.runs) != 1 {
		t.Fatalf("dispatched %v, want the re-queued run taken at its wake-up time", d.runs)
	}
	if got := get(t, c, "r1"); got.Status.Owner != "pod-a" || got.Status.Attempt != 1 {
		t.Errorf("owner = %q attempt = %d, want pod-a on attempt 1", got.Status.Owner, got.Status.Attempt)
	}
}

// A re-queued run that was then claimed MaxClaims times without finishing is
// still closed: the retry resets the series, the crash guard does not reset
// the retry.
func TestARequeuedRunStillHonoursMaxClaims(t *testing.T) {
	d := &dispatcher{}
	stopped := ""
	r := &Reconciler{
		Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch,
		Stop: func(_ context.Context, _, _, runID, _ string) error { stopped = runID; return nil },
	}
	object := unattended("r1", func(o *agentsv1alpha1.Run) {
		o.Status.Owner = "pod-b"
		o.Status.ClaimedAt = &metav1.Time{Time: started.Add(-time.Hour)}
		o.Status.Attempt = MaxClaims
		o.Status.Retries = 2
	})
	run(t, r, object, newAgent(600))
	if len(d.runs) != 0 || stopped != "r1" {
		t.Fatalf("dispatched %v, stopped %q; want the run closed, not run again", d.runs, stopped)
	}
}
