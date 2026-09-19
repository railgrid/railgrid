// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package schedule

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/executor"
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

// recorder is the executor: it remembers every job and can refuse.
type recorder struct {
	mu   sync.Mutex
	jobs []executor.Job
	err  error
}

func (r *recorder) Submit(_ context.Context, job executor.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.jobs = append(r.jobs, job)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs)
}

var now = time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)

func cronSchedule(name, expr string) *agentsv1alpha1.Schedule {
	s := &agentsv1alpha1.Schedule{ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1}}
	s.Spec.Type = agentsv1alpha1.ScheduleTypeCron
	s.Spec.Schedule = expr
	s.Spec.AgentRef = "helper"
	s.Spec.Task = "summarize the inbox"
	s.Spec.ChannelRef = "news"
	return s
}

type harness struct {
	c    client.Client
	r    *Reconciler
	exec *recorder
	req  mcreconcile.Request
}

func newHarness(t *testing.T, sched *agentsv1alpha1.Schedule, wrap ...func(client.WithWatch) client.Client) *harness {
	t.Helper()
	base := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Schedule{}).
		WithObjects(sched).
		Build()
	var c client.Client = base
	for _, w := range wrap {
		c = w(base)
	}
	exec := &recorder{}
	return &harness{
		c:    c,
		exec: exec,
		r:    &Reconciler{Manager: fakeManager{c: c}, Submit: exec, Now: func() time.Time { return now }},
		req:  mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: sched.Name}}},
	}
}

func (h *harness) reconcile(t *testing.T) reconcile.Result {
	t.Helper()
	res, err := h.r.Reconcile(context.Background(), h.req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func (h *harness) get(t *testing.T) agentsv1alpha1.Schedule {
	t.Helper()
	var got agentsv1alpha1.Schedule
	if err := h.c.Get(context.Background(), client.ObjectKey{Name: h.req.Name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// First sight of a cron schedule arms it: nextRun is persisted, nothing fires,
// and the reconciler sleeps until the planned time.
func TestFirstSightArmsWithoutFiring(t *testing.T) {
	h := newHarness(t, cronSchedule("daily", "0 10 * * *"))
	res := h.reconcile(t)

	got := h.get(t)
	want := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	if got.Status.NextRun == nil || !got.Status.NextRun.Time.Equal(want) {
		t.Fatalf("nextRun = %v, want %v", got.Status.NextRun, want)
	}
	if got.Status.ObservedGeneration != 1 {
		t.Fatalf("observedGeneration = %d, want 1", got.Status.ObservedGeneration)
	}
	if got.Status.LastRun != nil || h.exec.count() != 0 {
		t.Fatal("arming must not fire")
	}
	if res.RequeueAfter != time.Hour {
		t.Fatalf("RequeueAfter = %s, want 1h (until 10:00)", res.RequeueAfter)
	}
}

// A due schedule is claimed (lastRun/nextRun advance) and submitted once, then
// the reconciler sleeps until the next occurrence.
func TestDueScheduleFiresOnceAndAdvances(t *testing.T) {
	s := cronSchedule("hourly", "0 * * * *")
	s.Status.ObservedGeneration = 1
	s.Status.NextRun = &metav1.Time{Time: now.Add(-time.Minute)}
	h := newHarness(t, s)
	res := h.reconcile(t)

	if h.exec.count() != 1 {
		t.Fatalf("submitted %d jobs, want 1", h.exec.count())
	}
	job := h.exec.jobs[0]
	if job.Kind != executor.KindSchedule || job.ClusterID != "tenant-a" || job.SourceName != "hourly" ||
		job.AgentRef != "helper" || job.Task != "summarize the inbox" || job.Trigger != agentsv1alpha1.RunTriggerSchedule ||
		job.SessionID != "schedule:hourly" || job.NotifyChannel != "news" {
		t.Fatalf("job = %+v", job)
	}
	got := h.get(t)
	if got.Status.LastRun == nil || !got.Status.LastRun.Time.Equal(now) {
		t.Fatalf("lastRun = %v, want %v", got.Status.LastRun, now)
	}
	next := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	if got.Status.NextRun == nil || !got.Status.NextRun.Time.Equal(next) {
		t.Fatalf("nextRun = %v, want %v", got.Status.NextRun, next)
	}
	if res.RequeueAfter != time.Hour {
		t.Fatalf("RequeueAfter = %s, want 1h", res.RequeueAfter)
	}

	// Reconciling again at the same instant (a duplicate event) is a no-op:
	// the claim moved nextRun into the future.
	h.reconcile(t)
	if h.exec.count() != 1 {
		t.Fatal("a claimed fire must not be submitted twice")
	}
}

// A heartbeat fires its checklist wrapped in the quiet-OK prompt.
func TestHeartbeatFiresChecklist(t *testing.T) {
	s := cronSchedule("pulse", "*/15 * * * *")
	s.Spec.Type = agentsv1alpha1.ScheduleTypeHeartbeat
	s.Spec.Task = ""
	s.Spec.Checklist = "- inbox\n- PRs"
	s.Status.ObservedGeneration = 1
	s.Status.NextRun = &metav1.Time{Time: now}
	h := newHarness(t, s)
	h.reconcile(t)
	if h.exec.count() != 1 {
		t.Fatalf("submitted %d jobs, want 1", h.exec.count())
	}
	job := h.exec.jobs[0]
	if job.Trigger != agentsv1alpha1.RunTriggerHeartbeat || !strings.Contains(job.Task, "- inbox\n- PRs") || !strings.Contains(job.Task, "reply with exactly OK") {
		t.Fatalf("job = %+v", job)
	}
}

// A wakeup fires once at runAt; afterwards there is nothing to wait for.
func TestWakeupFiresOnceThenIdles(t *testing.T) {
	s := &agentsv1alpha1.Schedule{ObjectMeta: metav1.ObjectMeta{Name: "later", Generation: 1}}
	s.Spec.Type = agentsv1alpha1.ScheduleTypeWakeup
	s.Spec.AgentRef = "helper"
	s.Spec.Task = "check the build"
	s.Spec.RunAt = &metav1.Time{Time: now.Add(2 * time.Hour)}
	h := newHarness(t, s)

	res := h.reconcile(t)
	if h.exec.count() != 0 {
		t.Fatal("a future wakeup must not fire")
	}
	if res.RequeueAfter != time.Hour {
		t.Fatalf("RequeueAfter = %s, want the 1h clamp (runAt is 2h out)", res.RequeueAfter)
	}

	h.r.Now = func() time.Time { return now.Add(2*time.Hour + time.Second) }
	res = h.reconcile(t)
	if h.exec.count() != 1 || h.exec.jobs[0].Trigger != agentsv1alpha1.RunTriggerWakeup {
		t.Fatalf("wakeup should have fired once, jobs=%+v", h.exec.jobs)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("RequeueAfter = %s, want none after a one-shot", res.RequeueAfter)
	}

	res = h.reconcile(t)
	if h.exec.count() != 1 || res.RequeueAfter != 0 {
		t.Fatal("a fired wakeup must stay quiet")
	}
}

// A spec edit (generation bump) drops the stale nextRun and re-arms from the
// new spec instead of firing on the old clock.
func TestSpecEditReArmsFromNewSpec(t *testing.T) {
	s := cronSchedule("retimed", "0 9 * * *")
	s.Spec.TimeZone = "Europe/Vilnius"
	s.Generation = 2
	s.Status.ObservedGeneration = 1
	s.Status.NextRun = &metav1.Time{Time: now.Add(-2 * time.Hour)} // old cadence, in the past
	h := newHarness(t, s)
	h.reconcile(t)

	if h.exec.count() != 0 {
		t.Fatal("an edited schedule must not fire on its stale nextRun")
	}
	got := h.get(t)
	want := time.Date(2026, 7, 14, 6, 0, 0, 0, time.UTC) // 09:00 Vilnius tomorrow
	if got.Status.NextRun == nil || !got.Status.NextRun.Time.Equal(want) {
		t.Fatalf("nextRun = %v, want %v", got.Status.NextRun, want)
	}
	if got.Status.ObservedGeneration != 2 {
		t.Fatalf("observedGeneration = %d, want 2", got.Status.ObservedGeneration)
	}
}

// Suspended and disabled schedules are left alone entirely.
func TestSuspendedAndDisabledDoNothing(t *testing.T) {
	for name, mutate := range map[string]func(*agentsv1alpha1.Schedule){
		"suspended": func(s *agentsv1alpha1.Schedule) { s.Spec.Suspend = true },
		"disabled":  func(s *agentsv1alpha1.Schedule) { s.Status.DisabledReason = "revoked" },
	} {
		t.Run(name, func(t *testing.T) {
			s := cronSchedule("quiet", "* * * * *")
			s.Status.ObservedGeneration = 1
			s.Status.NextRun = &metav1.Time{Time: now.Add(-time.Minute)}
			mutate(s)
			h := newHarness(t, s)
			res := h.reconcile(t)
			if h.exec.count() != 0 || res.RequeueAfter != 0 {
				t.Fatalf("jobs=%d requeue=%s, want nothing", h.exec.count(), res.RequeueAfter)
			}
		})
	}
}

// An invalid spec is a permanent error: disabledReason is written and nothing
// is requeued.
func TestBadCronDisables(t *testing.T) {
	h := newHarness(t, cronSchedule("broken", "not-a-cron"))
	res := h.reconcile(t)
	got := h.get(t)
	if !strings.Contains(got.Status.DisabledReason, "invalid cron") {
		t.Fatalf("disabledReason = %q", got.Status.DisabledReason)
	}
	if res.RequeueAfter != 0 || h.exec.count() != 0 {
		t.Fatal("a disabled schedule must neither fire nor requeue")
	}
}

// A due schedule with nothing to run is disabled rather than fired empty.
func TestEmptyTaskDisables(t *testing.T) {
	s := cronSchedule("empty", "0 * * * *")
	s.Spec.Task = "   "
	s.Status.ObservedGeneration = 1
	s.Status.NextRun = &metav1.Time{Time: now}
	h := newHarness(t, s)
	h.reconcile(t)
	if got := h.get(t); got.Status.DisabledReason != "schedule has no task/checklist" {
		t.Fatalf("disabledReason = %q", got.Status.DisabledReason)
	}
	if h.exec.count() != 0 {
		t.Fatal("must not submit an empty task")
	}
}

// A conflict on the claim means another replica won: no submit, no error, no
// requeue — the watch delivers the object that replica wrote.
func TestClaimConflictYieldsToOtherReplica(t *testing.T) {
	s := cronSchedule("contended", "0 * * * *")
	s.Status.ObservedGeneration = 1
	s.Status.NextRun = &metav1.Time{Time: now}
	conflict := func(c client.WithWatch) client.Client {
		return interceptor.NewClient(c, interceptor.Funcs{
			SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
				return apierrors.NewConflict(schema.GroupResource{Group: "agents.railgrid.ai", Resource: "schedules"}, "contended", errors.New("the object has been modified"))
			},
		})
	}
	h := newHarness(t, s, conflict)
	res := h.reconcile(t)
	if h.exec.count() != 0 {
		t.Fatal("a lost claim must not submit")
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("RequeueAfter = %s, want none", res.RequeueAfter)
	}
}

// Any other write failure on the claim is returned so the workqueue retries.
func TestClaimErrorIsReturned(t *testing.T) {
	s := cronSchedule("flaky", "0 * * * *")
	s.Status.ObservedGeneration = 1
	s.Status.NextRun = &metav1.Time{Time: now}
	boom := func(c client.WithWatch) client.Client {
		return interceptor.NewClient(c, interceptor.Funcs{
			SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
				return apierrors.NewServiceUnavailable("virtual workspace is unavailable")
			},
		})
	}
	h := newHarness(t, s, boom)
	if _, err := h.r.Reconcile(context.Background(), h.req); err == nil {
		t.Fatal("want the claim error back")
	}
	if h.exec.count() != 0 {
		t.Fatal("must not submit without a claim")
	}
}

// A refused submit (queue full) does not fail the reconcile: the fire is
// already claimed on the CR, and retrying would double-run once the queue
// drains. The next occurrence is still scheduled.
func TestRefusedSubmitIsNotRetried(t *testing.T) {
	s := cronSchedule("busy", "0 * * * *")
	s.Status.ObservedGeneration = 1
	s.Status.NextRun = &metav1.Time{Time: now}
	h := newHarness(t, s)
	h.exec.err = executor.ErrQueueFull
	res := h.reconcile(t)
	if res.RequeueAfter != time.Hour {
		t.Fatalf("RequeueAfter = %s, want the next occurrence", res.RequeueAfter)
	}
	if got := h.get(t); got.Status.LastRun == nil {
		t.Fatal("the claim must stand even though the submit was refused")
	}
}

// A missing schedule is not an error.
func TestMissingScheduleIsIgnored(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(agentsscheme.NewScheme()).Build()
	r := &Reconciler{Manager: fakeManager{c: c}, Submit: &recorder{}}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "gone"}}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("a missing schedule must not error: %v", err)
	}
}

func TestRequeueInClamps(t *testing.T) {
	for _, tc := range []struct {
		planned time.Time
		want    time.Duration
	}{
		{now.Add(-time.Minute), MinRequeue},
		{now.Add(200 * time.Millisecond), MinRequeue},
		{now.Add(30 * time.Minute), 30 * time.Minute},
		{now.Add(48 * time.Hour), MaxRequeue},
	} {
		if got := requeueIn(tc.planned, now); got != tc.want {
			t.Errorf("requeueIn(%v) = %s, want %s", tc.planned.Sub(now), got, tc.want)
		}
	}
}

// ---- the Validated condition ---------------------------------------------------

// helperAgent is the agent cronSchedule drives, with the channel it names.
func helperAgent() *agentsv1alpha1.Agent {
	a := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "helper"}}
	a.Spec.DisplayName = "helper"
	a.Spec.Channels = []agentsv1alpha1.AgentChannel{{Name: "news", ConnectionRef: "tg", Primary: true}}
	return a
}

// reconcileWith reconciles the schedule with the rest of the workspace present
// and returns it as stored.
func reconcileWith(t *testing.T, sched *agentsv1alpha1.Schedule, rest ...client.Object) agentsv1alpha1.Schedule {
	t.Helper()
	objs := append([]client.Object{sched}, rest...)
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Schedule{}).
		WithObjects(objs...).
		Build()
	r := &Reconciler{Manager: fakeManager{c: c}, Submit: &recorder{}, Now: func() time.Time { return now }}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: sched.Name}}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got agentsv1alpha1.Schedule
	if err := c.Get(context.Background(), client.ObjectKey{Name: sched.Name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func validated(t *testing.T, s agentsv1alpha1.Schedule) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(s.Status.Conditions, agentsv1alpha1.ConditionValidated)
	if c == nil {
		t.Fatalf("no %s condition: %+v", agentsv1alpha1.ConditionValidated, s.Status.Conditions)
	}
	return *c
}

func TestValidatedTrueOnASoundSchedule(t *testing.T) {
	cond := validated(t, reconcileWith(t, cronSchedule("daily", "0 9 * * *"), helperAgent()))
	if cond.Status != metav1.ConditionTrue || cond.Reason != agentsv1alpha1.ReasonValidated {
		t.Fatalf("condition = %s/%s (%q), want True/Validated", cond.Status, cond.Reason, cond.Message)
	}
}

// applyScheduleCreate: "name, agentRef, and type are required".
func TestValidatedRejectsAMissingAgentRef(t *testing.T) {
	s := cronSchedule("daily", "0 9 * * *")
	s.Spec.AgentRef = ""
	cond := validated(t, reconcileWith(t, s))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("condition = %s/%s, want False/InvalidSpec", cond.Status, cond.Reason)
	}
	if !strings.Contains(cond.Message, "agentRef") {
		t.Fatalf("message must name the field: %q", cond.Message)
	}
}

// applyScheduleCreate: "a cron schedule is required for cron/heartbeat types".
func TestValidatedRejectsACronTypeWithoutACronExpression(t *testing.T) {
	s := cronSchedule("daily", "")
	cond := validated(t, reconcileWith(t, s, helperAgent()))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("condition = %s/%s (%q), want False/InvalidSpec", cond.Status, cond.Reason, cond.Message)
	}
}

// applyScheduleCreate: "runAt is required for wakeup type".
func TestValidatedRejectsAWakeupWithoutRunAt(t *testing.T) {
	s := &agentsv1alpha1.Schedule{ObjectMeta: metav1.ObjectMeta{Name: "later", Generation: 1}}
	s.Spec.Type = agentsv1alpha1.ScheduleTypeWakeup
	s.Spec.AgentRef = "helper"
	s.Spec.Task = "check again"
	cond := validated(t, reconcileWith(t, s, helperAgent()))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("condition = %s/%s (%q), want False/InvalidSpec", cond.Status, cond.Reason, cond.Message)
	}
	if !strings.Contains(cond.Message, "runAt") {
		t.Fatalf("message must name the field: %q", cond.Message)
	}
}

// applyScheduleCreate: "type must be cron, wakeup, or heartbeat".
func TestValidatedRejectsAnUnknownType(t *testing.T) {
	s := cronSchedule("daily", "0 9 * * *")
	s.Spec.Type = "fortnightly"
	cond := validated(t, reconcileWith(t, s, helperAgent()))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("condition = %s/%s (%q), want False/InvalidSpec", cond.Status, cond.Reason, cond.Message)
	}
}

// A schedule with nothing to run reaches its moment and disables itself. Said
// up front, a silent wait becomes an answer.
func TestValidatedRejectsAScheduleWithNothingToRun(t *testing.T) {
	s := cronSchedule("daily", "0 9 * * *")
	s.Spec.Task = ""
	if cond := validated(t, reconcileWith(t, s, helperAgent())); cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("cron without a task: reason = %s, want InvalidSpec", cond.Reason)
	}

	h := &agentsv1alpha1.Schedule{ObjectMeta: metav1.ObjectMeta{Name: "pulse", Generation: 1}}
	h.Spec.Type = agentsv1alpha1.ScheduleTypeHeartbeat
	h.Spec.Schedule = "*/15 * * * *"
	h.Spec.AgentRef = "helper"
	if cond := validated(t, reconcileWith(t, h, helperAgent())); cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("heartbeat without a checklist: reason = %s, want InvalidSpec", cond.Reason)
	}
}

// Nothing ever checked this: a schedule pointing at an agent that does not
// exist fires into nothing.
func TestValidatedRejectsAnUnknownAgentRef(t *testing.T) {
	cond := validated(t, reconcileWith(t, cronSchedule("daily", "0 9 * * *")))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonUnknownAgentRef {
		t.Fatalf("condition = %s/%s (%q), want False/UnknownAgentRef", cond.Status, cond.Reason, cond.Message)
	}
	if !strings.Contains(cond.Message, "helper") {
		t.Fatalf("message must name the agent: %q", cond.Message)
	}
}

// A channelRef the agent does not have still fires; it just answers somewhere
// its author did not ask for.
func TestValidatedRejectsAChannelRefTheAgentDoesNotHave(t *testing.T) {
	a := helperAgent()
	a.Spec.Channels = []agentsv1alpha1.AgentChannel{{Name: "primary", ConnectionRef: "tg", Primary: true}}
	cond := validated(t, reconcileWith(t, cronSchedule("daily", "0 9 * * *"), a))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("condition = %s/%s (%q), want False/InvalidSpec", cond.Status, cond.Reason, cond.Message)
	}
	if !strings.Contains(cond.Message, "news") {
		t.Fatalf("message must name the channel: %q", cond.Message)
	}
}

// schedulepolicy.Due is the one cron parser; its verdict reaches the condition
// rather than a second parser that could disagree with it.
func TestValidatedCarriesTheCronParseFailure(t *testing.T) {
	got := reconcileWith(t, cronSchedule("daily", "not a cron"), helperAgent())
	if got.Status.DisabledReason == "" {
		t.Fatal("a bad cron must still disable the schedule")
	}
	cond := validated(t, got)
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("condition = %s/%s, want False/InvalidSpec", cond.Status, cond.Reason)
	}
	if cond.Message != got.Status.DisabledReason {
		t.Fatalf("condition message %q must be the disable reason %q", cond.Message, got.Status.DisabledReason)
	}
}

// A suspended schedule is exactly the one whose author most needs to be told
// what is wrong with it, so the verdict is written for it too.
func TestValidatedIsWrittenForASuspendedSchedule(t *testing.T) {
	s := cronSchedule("daily", "0 9 * * *")
	s.Spec.Suspend = true
	got := reconcileWith(t, s) // no agent
	if cond := validated(t, got); cond.Reason != agentsv1alpha1.ReasonUnknownAgentRef {
		t.Fatalf("reason = %s, want UnknownAgentRef", cond.Reason)
	}
	if got.Status.NextRun != nil {
		t.Fatalf("a suspended schedule must not be armed: %v", got.Status.NextRun)
	}
}
