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
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsscheme "github.com/railgrid/provider-agents/scheme"
)

type fakeManager struct {
	mcmanager.Manager
	c client.Client
}

func (m fakeManager) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return fakeCluster{c: m.c}, nil
}

type fakeCluster struct {
	cluster.Cluster
	c client.Client
}

func (c fakeCluster) GetClient() client.Client { return c.c }

var (
	started  = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	cluster1 = "tenant-a"
)

func newRun(name string, mutate func(*agentsv1alpha1.Run)) *agentsv1alpha1.Run {
	object := &agentsv1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       agentsv1alpha1.RunSpec{AgentRef: "scout", Trigger: agentsv1alpha1.RunTriggerAPI},
		Status: agentsv1alpha1.RunStatus{
			Phase:     agentsv1alpha1.RunPhaseRunning,
			StartedAt: &metav1.Time{Time: started},
		},
	}
	if mutate != nil {
		mutate(object)
	}
	return object
}

func newAgent(timeoutSeconds int32) *agentsv1alpha1.Agent {
	return &agentsv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "scout"},
		Spec:       agentsv1alpha1.AgentSpec{Limits: agentsv1alpha1.AgentLimits{TimeoutSeconds: timeoutSeconds}},
	}
}

func run(t *testing.T, r *Reconciler, objects ...client.Object) (client.Client, ctrl.Result) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Run{}, &agentsv1alpha1.Agent{}).
		WithObjects(objects...).
		Build()
	r.Manager = fakeManager{c: c}
	result, err := r.Reconcile(context.Background(), mcreconcile.Request{
		ClusterName: multicluster.ClusterName(cluster1),
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: objects[0].GetName()}},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return c, result
}

func get(t *testing.T, c client.Client, name string) agentsv1alpha1.Run {
	t.Helper()
	var got agentsv1alpha1.Run
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// The deadline is written from the AGENT's limit at the moment the run starts,
// and the reconciler then asks to be woken at exactly that instant. This is the
// whole point of the kind: the 30-second sweep that listed every non-terminal
// run in every tenant is replaced by one requeue per run, derived from the
// object's own state.
func TestDeadlineIsStampedAndRequeuedAt(t *testing.T) {
	now := started.Add(time.Minute)
	r := &Reconciler{Now: func() time.Time { return now }}
	c, result := run(t, r, newRun("r1", nil), newAgent(600))

	got := get(t, c, "r1")
	if got.Status.DeadlineAt == nil {
		t.Fatal("no deadline was stamped")
	}
	if want := started.Add(10 * time.Minute); !got.Status.DeadlineAt.Time.Equal(want) {
		t.Errorf("deadline = %v, want startedAt+600s = %v", got.Status.DeadlineAt.Time, want)
	}
	// Stamping and scheduling happen in the SAME pass: there is no reason to
	// spend a second reconcile discovering a deadline this one just computed.
	if want := 9*time.Minute + deadlineSlack; result.RequeueAfter != want {
		t.Errorf("RequeueAfter = %v, want the time left on the deadline (%v)", result.RequeueAfter, want)
	}

	// And a later pass over an already-stamped run neither re-reads the agent
	// nor moves the deadline — the run was admitted under the limit in force
	// when it started.
	_, result = run(t, r, &got, newAgent(60))
	if want := 9*time.Minute + deadlineSlack; result.RequeueAfter != want {
		t.Errorf("RequeueAfter = %v after an agent edit, want the original deadline (%v)", result.RequeueAfter, want)
	}
}

// An agent that asks for more than MaxTimeout does not get it: one agent must
// not be able to pin a worker indefinitely.
func TestDeadlineIsCapped(t *testing.T) {
	r := &Reconciler{Now: func() time.Time { return started }}
	c, _ := run(t, r, newRun("r1", nil), newAgent(int32((24 * time.Hour).Seconds())))
	got := get(t, c, "r1")
	if want := started.Add(MaxTimeout); !got.Status.DeadlineAt.Time.Equal(want) {
		t.Errorf("deadline = %v, want the cap %v", got.Status.DeadlineAt.Time, want)
	}
}

// An agent with no limit gets the platform default, and an agent that has been
// deleted while its run was in flight still gets a deadline: a run with none is
// worse than a run with a generous one.
func TestDeadlineFallsBackToTheDefault(t *testing.T) {
	r := &Reconciler{Now: func() time.Time { return started }}
	for _, tc := range []struct {
		name    string
		objects []client.Object
	}{
		{"agent sets no limit", []client.Object{newRun("r1", nil), newAgent(0)}},
		{"agent is gone", []client.Object{newRun("r1", nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := run(t, r, tc.objects...)
			got := get(t, c, "r1")
			if got.Status.DeadlineAt == nil || !got.Status.DeadlineAt.Time.Equal(started.Add(DefaultTimeout)) {
				t.Errorf("deadline = %v, want startedAt+%v", got.Status.DeadlineAt, DefaultTimeout)
			}
		})
	}
}

// Past the deadline the reconciler reports the timeout and says so on the
// object. It does NOT write a terminal phase: the executor holds the run's
// context and is what stops it, and an observer declaring a run dead that is
// still producing output would be a lie the transcript contradicts.
func TestPastTheDeadlineTheRunIsReportedNotKilled(t *testing.T) {
	var got struct {
		cluster, agent, run, reason string
		calls                       int
	}
	r := &Reconciler{
		Now: func() time.Time { return started.Add(time.Hour) },
		Stop: func(_ context.Context, clusterID, agentName, runID, reason string) error {
			got.cluster, got.agent, got.run, got.reason = clusterID, agentName, runID, reason
			got.calls++
			return nil
		},
	}
	object := newRun("r1", func(o *agentsv1alpha1.Run) {
		o.Status.DeadlineAt = &metav1.Time{Time: started.Add(10 * time.Minute)}
	})
	c, result := run(t, r, object, newAgent(600))

	if got.calls != 1 || got.cluster != cluster1 || got.agent != "scout" || got.run != "r1" {
		t.Fatalf("Stop called %d time(s) with (%q, %q, %q)", got.calls, got.cluster, got.agent, got.run)
	}
	if !strings.Contains(got.reason, "time limit") {
		t.Errorf("reason = %q, should name the limit so a reader can tell a deadline from a crash", got.reason)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("a reported timeout requeued after %v; there is nothing left to wait for", result.RequeueAfter)
	}
	after := get(t, c, "r1")
	if after.Status.Phase != agentsv1alpha1.RunPhaseRunning {
		t.Errorf("phase = %q; the reconciler must not write a terminal phase", after.Status.Phase)
	}
	condition := meta.FindStatusCondition(after.Status.Conditions, agentsv1alpha1.ConditionValidated)
	if condition == nil || condition.Reason != agentsv1alpha1.ReasonRunDeadlineExceeded {
		t.Errorf("condition = %+v, want %s", condition, agentsv1alpha1.ReasonRunDeadlineExceeded)
	}
}

// A run waiting on a human has not overrun. PendingApproval is settled from the
// deadline's point of view, and the stale deadline is cleared so a later resume
// is measured afresh rather than starting already expired.
func TestAWaitingRunHasNoDeadline(t *testing.T) {
	r := &Reconciler{
		Now: func() time.Time { return started.Add(time.Hour) },
		Stop: func(context.Context, string, string, string, string) error {
			t.Fatal("a settled run must not time out")
			return nil
		},
	}
	object := newRun("r1", func(o *agentsv1alpha1.Run) {
		o.Status.Phase = agentsv1alpha1.RunPhasePendingApproval
		o.Status.DeadlineAt = &metav1.Time{Time: started.Add(10 * time.Minute)}
	})
	c, _ := run(t, r, object, newAgent(600))
	if got := get(t, c, "r1"); got.Status.DeadlineAt != nil {
		t.Errorf("deadline = %v, want it cleared while the run waits on a human", got.Status.DeadlineAt)
	}
}

// A run that has been accepted but not started has nothing to time out. The
// transition to Running re-delivers it.
func TestAnUnstartedRunGetsNoDeadline(t *testing.T) {
	r := &Reconciler{Now: func() time.Time { return started }}
	object := newRun("r1", func(o *agentsv1alpha1.Run) {
		o.Status.Phase = agentsv1alpha1.RunPhasePending
		o.Status.StartedAt = nil
	})
	c, result := run(t, r, object, newAgent(600))
	if got := get(t, c, "r1"); got.Status.DeadlineAt != nil {
		t.Errorf("deadline = %v on a run that has not started", got.Status.DeadlineAt)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v; there is nothing to wake for", result.RequeueAfter)
	}
}

// The finalizer is added only when there is a store to purge from. Without one
// — the dev in-memory path — a finalizer would make every run undeletable.
func TestFinalizerIsAddedOnlyWithAPurge(t *testing.T) {
	for _, tc := range []struct {
		name  string
		purge func(context.Context, string, string, string) error
		want  bool
	}{
		{"with a purge", func(context.Context, string, string, string) error { return nil }, true},
		{"without one", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Reconciler{Now: func() time.Time { return started }, PurgeData: tc.purge}
			c, _ := run(t, r, newRun("r1", nil), newAgent(600))
			got := get(t, c, "r1")
			if controllerutil.ContainsFinalizer(&got, DataFinalizer) != tc.want {
				t.Errorf("finalizer present = %v, want %v", !tc.want, tc.want)
			}
		})
	}
}

// Deleting a Run purges its rows and then releases. This is what makes the
// object's deletion mean something: without it a tenant who deletes a run still
// has its transcript, invisible and billable.
func TestDeletePurgesThenReleases(t *testing.T) {
	var purged struct {
		cluster, agent, run string
		calls               int
	}
	r := &Reconciler{
		Now: func() time.Time { return started },
		PurgeData: func(_ context.Context, clusterID, agentName, runID string) error {
			purged.cluster, purged.agent, purged.run = clusterID, agentName, runID
			purged.calls++
			return nil
		},
	}
	deleting := newRun("r1", func(o *agentsv1alpha1.Run) {
		o.Finalizers = []string{DataFinalizer}
		o.DeletionTimestamp = &metav1.Time{Time: started}
	})
	c, _ := run(t, r, deleting, newAgent(600))

	if purged.calls != 1 || purged.cluster != cluster1 || purged.agent != "scout" || purged.run != "r1" {
		t.Fatalf("PurgeData called %d time(s) with (%q, %q, %q)", purged.calls, purged.cluster, purged.agent, purged.run)
	}
	// Releasing the last finalizer lets the object go, so it is gone rather
	// than present-without-finalizer.
	var got agentsv1alpha1.Run
	if err := c.Get(context.Background(), client.ObjectKey{Name: "r1"}, &got); err == nil {
		if controllerutil.ContainsFinalizer(&got, DataFinalizer) {
			t.Error("the finalizer was not released")
		}
	}
}

// A purge that keeps failing must not make the run undeletable. Before the
// deadline it retries; past it, the rows are logged and the object is released
// — a recoverable problem (rows an operator can delete) beats an unrecoverable
// one (an object the user cannot remove).
func TestAFailingPurgeRetriesThenGivesUp(t *testing.T) {
	failing := func(context.Context, string, string, string) error { return errors.New("store is down") }

	early := &Reconciler{Now: func() time.Time { return started.Add(time.Minute) }, PurgeData: failing}
	deleting := newRun("r1", func(o *agentsv1alpha1.Run) {
		o.Finalizers = []string{DataFinalizer}
		o.DeletionTimestamp = &metav1.Time{Time: started}
	})
	c, result := run(t, early, deleting, newAgent(600))
	if result.RequeueAfter != purgeRetryInterval {
		t.Errorf("RequeueAfter = %v, want a retry in %v", result.RequeueAfter, purgeRetryInterval)
	}
	if got := get(t, c, "r1"); !controllerutil.ContainsFinalizer(&got, DataFinalizer) {
		t.Error("the finalizer was released while the purge could still be retried")
	}

	late := &Reconciler{Now: func() time.Time { return started.Add(purgeGiveUp + time.Minute) }, PurgeData: failing}
	deleting = newRun("r1", func(o *agentsv1alpha1.Run) {
		o.Finalizers = []string{DataFinalizer}
		o.DeletionTimestamp = &metav1.Time{Time: started}
	})
	c, _ = run(t, late, deleting, newAgent(600))
	var got agentsv1alpha1.Run
	if err := c.Get(context.Background(), client.ObjectKey{Name: "r1"}, &got); err == nil &&
		controllerutil.ContainsFinalizer(&got, DataFinalizer) {
		t.Error("the finalizer was still held past the give-up deadline")
	}
}

// ---- the claim: the object is the queue -------------------------------------

func unattended(name string, mutate func(*agentsv1alpha1.Run)) *agentsv1alpha1.Run {
	object := newRun(name, func(o *agentsv1alpha1.Run) {
		o.Spec.Delivery = &agentsv1alpha1.RunDelivery{Kind: "schedule", SourceName: "daily"}
		o.Status.Phase = agentsv1alpha1.RunPhasePending
		o.Status.StartedAt = nil
	})
	if mutate != nil {
		mutate(object)
	}
	return object
}

type dispatcher struct {
	runs []string
	err  error
}

func (d *dispatcher) dispatch(_ context.Context, _ string, object *agentsv1alpha1.Run) error {
	d.runs = append(d.runs, object.Name)
	return d.err
}

// Unclaimed unattended work is claimed and handed to the pool, and the claim is
// recorded on the object — owner, when, and which attempt. Without all three a
// restart cannot tell work nobody is doing from work somebody is.
func TestUnclaimedWorkIsClaimedAndDispatched(t *testing.T) {
	d := &dispatcher{}
	r := &Reconciler{Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch}
	c, _ := run(t, r, unattended("r1", nil), newAgent(600))

	if len(d.runs) != 1 || d.runs[0] != "r1" {
		t.Fatalf("dispatched %v, want [r1]", d.runs)
	}
	got := get(t, c, "r1")
	if got.Status.Owner != "pod-a" {
		t.Errorf("owner = %q, want this process", got.Status.Owner)
	}
	if got.Status.ClaimedAt == nil || !got.Status.ClaimedAt.Time.Equal(started) {
		t.Errorf("claimedAt = %v, want the claim to say when", got.Status.ClaimedAt)
	}
	if got.Status.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", got.Status.Attempt)
	}
}

// The claim is what stops a run being executed twice. A run this process
// already owns must not be re-dispatched when the watch re-delivers it — which
// it does on every status write, including the claim's own.
func TestOurOwnClaimIsNotReDispatched(t *testing.T) {
	d := &dispatcher{}
	r := &Reconciler{Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch}
	claimed := unattended("r1", func(o *agentsv1alpha1.Run) {
		o.Status.Owner = "pod-a"
		o.Status.ClaimedAt = &metav1.Time{Time: started}
		o.Status.Attempt = 1
	})
	run(t, r, claimed, newAgent(600))

	if len(d.runs) != 0 {
		t.Fatalf("dispatched %v; this process already owns that run", d.runs)
	}
}

// Somebody else's claim is left alone until the grace lapses, and the
// reconciler asks to be woken exactly then — so an abandoned run is picked up
// promptly rather than at the next unrelated event.
func TestAnotherProcessesClaimIsRespectedUntilTheGraceLapses(t *testing.T) {
	claimed := func(age time.Duration) *agentsv1alpha1.Run {
		return unattended("r1", func(o *agentsv1alpha1.Run) {
			o.Status.Owner = "pod-b"
			o.Status.ClaimedAt = &metav1.Time{Time: started.Add(-age)}
			o.Status.Attempt = 1
		})
	}

	d := &dispatcher{}
	r := &Reconciler{Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch}
	_, result := run(t, r, claimed(30*time.Second), newAgent(600))
	if len(d.runs) != 0 {
		t.Fatalf("dispatched %v while another process's claim was fresh", d.runs)
	}
	if want := ClaimGrace - 30*time.Second; result.RequeueAfter != want {
		t.Errorf("RequeueAfter = %v, want the rest of the grace (%v)", result.RequeueAfter, want)
	}

	d = &dispatcher{}
	r = &Reconciler{Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch}
	c, _ := run(t, r, claimed(ClaimGrace+time.Minute), newAgent(600))
	if len(d.runs) != 1 {
		t.Fatalf("dispatched %v, want the abandoned run to be re-claimed", d.runs)
	}
	if got := get(t, c, "r1"); got.Status.Owner != "pod-a" || got.Status.Attempt != 2 {
		t.Errorf("owner = %q attempt = %d, want pod-a on attempt 2", got.Status.Owner, got.Status.Attempt)
	}
}

// A run a person is watching executes on the replica that served their request.
// It has no unattended delivery kind, and the leader must never pick it up —
// this is the one mistake that would charge a tenant twice for one chat turn.
func TestAttendedRunsAreNeverClaimed(t *testing.T) {
	d := &dispatcher{}
	r := &Reconciler{Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch}
	for _, delivery := range []*agentsv1alpha1.RunDelivery{
		nil,
		{SourceName: "api:alice"},
		{Kind: "", SourceName: "api:alice"},
	} {
		object := unattended("r1", func(o *agentsv1alpha1.Run) { o.Spec.Delivery = delivery })
		run(t, r, object, newAgent(600))
	}
	if len(d.runs) != 0 {
		t.Fatalf("dispatched %v; none of those runs is unattended work", d.runs)
	}
}

// A run that has been claimed its last permitted time and still has not started
// is closed, not re-queued. Otherwise a run that kills whatever picks it up is
// taken by every new leader in turn and the provider never starts cleanly.
func TestARunClaimedTooOftenIsClosed(t *testing.T) {
	var stopped struct {
		run, reason string
		calls       int
	}
	d := &dispatcher{}
	r := &Reconciler{
		Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch,
		Stop: func(_ context.Context, _, _, runID, reason string) error {
			stopped.run, stopped.reason = runID, reason
			stopped.calls++
			return nil
		},
	}
	object := unattended("r1", func(o *agentsv1alpha1.Run) {
		o.Status.Owner = "pod-b"
		o.Status.ClaimedAt = &metav1.Time{Time: started.Add(-2 * ClaimGrace)}
		o.Status.Attempt = MaxClaims
	})
	run(t, r, object, newAgent(600))

	if len(d.runs) != 0 {
		t.Fatalf("dispatched %v past the claim limit", d.runs)
	}
	if stopped.calls != 1 || stopped.run != "r1" {
		t.Fatalf("Stop called %d time(s) for %q", stopped.calls, stopped.run)
	}
	if !strings.Contains(stopped.reason, "will not be retried") {
		t.Errorf("reason = %q, should say it gave up", stopped.reason)
	}
}

// A process that cannot execute anything must not take work off the queue: a
// claim it cannot honour holds the run for a full grace before anyone else may
// try, which is strictly worse than not claiming at all.
func TestAProcessWithNoDispatcherClaimsNothing(t *testing.T) {
	for _, r := range []*Reconciler{
		{Now: func() time.Time { return started }, ProcessID: "pod-a"},
		{Now: func() time.Time { return started }, Dispatch: (&dispatcher{}).dispatch},
	} {
		c, _ := run(t, r, unattended("r1", nil), newAgent(600))
		if got := get(t, c, "r1"); got.Status.Owner != "" {
			t.Errorf("owner = %q; a process that cannot execute must not claim", got.Status.Owner)
		}
	}
}

// A failed dispatch keeps the claim. Releasing it would hand the run straight
// back to the process that just failed to take it; the grace is what gives it
// to somebody else, and the attempt count is what stops that forever.
func TestAFailedDispatchKeepsTheClaim(t *testing.T) {
	d := &dispatcher{err: errors.New("pool is gone")}
	r := &Reconciler{Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch}
	c, result := run(t, r, unattended("r1", nil), newAgent(600))

	if result.RequeueAfter != ClaimGrace {
		t.Errorf("RequeueAfter = %v, want the grace (%v)", result.RequeueAfter, ClaimGrace)
	}
	got := get(t, c, "r1")
	if got.Status.Owner != "pod-a" || got.Status.Attempt != 1 {
		t.Errorf("owner = %q attempt = %d, want the claim to stand", got.Status.Owner, got.Status.Attempt)
	}
}

// ---- recovery: a run whose executor went away -------------------------------

// A run left EXECUTING by a process that is gone is recovered, not re-queued as
// fresh work: it may have a checkpoint, and starting over would repeat whatever
// it already paid for.
func TestAStrandedRunningRunIsRecovered(t *testing.T) {
	var recovered struct {
		run   string
		calls int
	}
	d := &dispatcher{}
	r := &Reconciler{
		Now: func() time.Time { return started }, ProcessID: "pod-a", Dispatch: d.dispatch,
		Recover: func(_ context.Context, _, _, runID string) error {
			recovered.run = runID
			recovered.calls++
			return nil
		},
	}
	object := unattended("r1", func(o *agentsv1alpha1.Run) {
		o.Status.Phase = agentsv1alpha1.RunPhaseRunning
		o.Status.StartedAt = &metav1.Time{Time: started.Add(-time.Hour)}
		o.Status.Owner = "pod-b"
		o.Status.ClaimedAt = &metav1.Time{Time: started.Add(-2 * ClaimGrace)}
	})
	run(t, r, object, newAgent(600))

	if recovered.calls != 1 || recovered.run != "r1" {
		t.Fatalf("Recover called %d time(s) for %q", recovered.calls, recovered.run)
	}
	if len(d.runs) != 0 {
		t.Fatalf("dispatched %v; an in-flight run is recovered, never restarted", d.runs)
	}
}

// A run this process is executing is not stranded, however old its claim looks.
func TestOurOwnRunningRunIsNotRecovered(t *testing.T) {
	r := &Reconciler{
		Now: func() time.Time { return started }, ProcessID: "pod-a",
		Dispatch: (&dispatcher{}).dispatch,
		Recover: func(context.Context, string, string, string) error {
			t.Fatal("this process is executing that run")
			return nil
		},
	}
	object := unattended("r1", func(o *agentsv1alpha1.Run) {
		o.Status.Phase = agentsv1alpha1.RunPhaseRunning
		o.Status.Owner = "pod-a"
		o.Status.ClaimedAt = &metav1.Time{Time: started.Add(-10 * ClaimGrace)}
	})
	run(t, r, object, newAgent(600))
}
