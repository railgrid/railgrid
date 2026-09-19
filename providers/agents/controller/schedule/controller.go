// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package schedule makes Schedule CRs fire on their own clock. One reconcile
// per Schedule decides, from spec and status alone, whether the schedule is
// due; it claims the fire with an optimistic status update (a conflict means
// another replica or an older event got there first) and hands the run to the
// executor. Between fires the reconciler sleeps on RequeueAfter until the
// planned time, so there is no periodic list of every schedule in every
// tenant workspace — and an edited or newly bound schedule is picked up the
// moment its watch event lands rather than on the next tick.
//
// Fire times live on the CR (status.nextRun / lastRun), so a restart, a
// leadership change, or a replica that missed a requeue all re-derive the
// same answer from the same state: a fire that was due while nobody was
// watching fires on the first reconcile after, and a fire that was claimed
// is never claimed twice.
//
// The reconciler also reports spec validity as the Validated condition. The
// REST create/update handlers (api/schedules.go: applyScheduleCreate /
// applyScheduleUpdate) used to reject a schedule that could never fire — no
// agentRef, a cron type with no cron expression, a wakeup with no runAt — with
// a 400, before it was stored. A writer going straight to the kube API gets
// past the CRD schema with all of those, and the only sign used to be a
// schedule that sat there and never ran. Now it says why. The verdict is
// advisory: nothing here refuses to fire a schedule on account of it, and the
// existing DisabledReason still does the actual disabling.
package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/executor"
	"github.com/railgrid/provider-agents/internal/schedulepolicy"
)

// Requeue bounds. A planned fire further out than MaxRequeue is re-checked
// at MaxRequeue anyway (a workqueue timer that long is fragile across
// leadership changes and clock jumps, and re-deriving is free); a fire closer
// than MinRequeue is not worth a sub-second timer.
const (
	MinRequeue = time.Second
	MaxRequeue = time.Hour
)

// Reconciler fires due schedules.
type Reconciler struct {
	Manager mcmanager.Manager
	// Submit queues the run. The provider's submitter persists a Pending run
	// row first, so a fire that was claimed on the CR is visible in the run
	// list even if the executor never gets to it.
	Submit executor.Submitter
	// Now is the clock; nil means time.Now. Tests pin it.
	Now func() time.Time
}

// SetupWithManager wires the reconciler into the multicluster manager. Agents
// are watched so that creating the missing Agent clears an UnknownAgentRef
// without the Schedule being touched.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-schedule").
		For(&agentsv1alpha1.Schedule{}).
		Watches(&agentsv1alpha1.Agent{}, schedulesForAgent).
		Complete(r)
}

// schedulesForAgent enqueues the Schedules whose agentRef names the Agent the
// event came from.
func schedulesForAgent(clusterName multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list agentsv1alpha1.ScheduleList
		if err := cl.GetClient().List(ctx, &list); err != nil {
			klog.FromContext(ctx).Error(err, "listing schedules to re-validate")
			return nil
		}
		var reqs []reconcile.Request
		for i := range list.Items {
			if strings.TrimSpace(list.Items[i].Spec.AgentRef) == obj.GetName() {
				reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
			}
		}
		return reqs
	}))(clusterName, cl)
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Reconcile handles one Schedule.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("schedule", req.Name, "cluster", req.ClusterName)
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()

	var sched agentsv1alpha1.Schedule
	if err := c.Get(ctx, req.NamespacedName, &sched); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !sched.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// The verdict is computed for every schedule, including the ones that do
	// not fire: a suspended or disabled schedule is exactly the one whose
	// author most needs to be told what is wrong with it.
	reason, message, verr := r.validate(ctx, c, &sched)
	if verr != nil {
		return ctrl.Result{}, verr // a failed read is not a verdict
	}
	validatedChanged := setValidated(&sched, reason, message)

	if sched.Spec.Suspend || sched.Status.DisabledReason != "" {
		if validatedChanged {
			return ctrl.Result{}, r.updateStatus(ctx, c, &sched)
		}
		return ctrl.Result{}, nil
	}
	now := r.now()

	// A spec edit bumps metadata.generation. The stored nextRun was computed
	// from the previous cron/timezone/runAt, so it is stale — drop it and let
	// the policy re-derive the next fire time from the new spec. Without this
	// an edited schedule keeps firing (or waiting) on its old clock until it
	// happens to fire once and recomputes.
	genChanged := sched.Generation != sched.Status.ObservedGeneration
	if genChanged {
		sched.Status.NextRun = nil
	}

	fire, next, permErr := schedulepolicy.Due(&sched, now)
	if permErr != nil {
		// An unusable cron expression, timezone or type. It disables the
		// schedule, and it is a spec problem, so it is both.
		sched.Status.DisabledReason = permErr.Error()
		sched.Status.ObservedGeneration = sched.Generation
		setValidated(&sched, agentsv1alpha1.ReasonInvalidSpec, permErr.Error())
		return ctrl.Result{}, r.updateStatus(ctx, c, &sched)
	}

	if !fire {
		// Persist a freshly-armed nextRun on first sight or after a spec edit,
		// and record the observed generation so the re-arm happens exactly once.
		changed := validatedChanged
		if genChanged {
			sched.Status.ObservedGeneration = sched.Generation
			changed = true
		}
		if sched.Status.NextRun == nil && !next.IsZero() {
			sched.Status.NextRun = &metav1.Time{Time: next}
			changed = true
		}
		if changed {
			if err := r.updateStatus(ctx, c, &sched); err != nil {
				return ctrl.Result{}, err
			}
		}
		planned := next
		if planned.IsZero() && sched.Status.NextRun != nil {
			planned = sched.Status.NextRun.Time
		}
		if planned.IsZero() {
			return ctrl.Result{}, nil // a wakeup that already fired: nothing to wait for
		}
		return ctrl.Result{RequeueAfter: requeueIn(planned, now)}, nil
	}

	// Claim: advance lastRun/nextRun against the resourceVersion we read. A
	// conflict means another replica (or a reconcile of a newer event) claimed
	// this fire — drop out silently; the watch delivers the updated object.
	sched.Status.LastRun = &metav1.Time{Time: now}
	if genChanged {
		sched.Status.ObservedGeneration = sched.Generation
	}
	if next.IsZero() {
		// A one-shot has no next occurrence. Leaving the old nextRun in place
		// would read as a planned fire in the past and requeue every second.
		sched.Status.NextRun = nil
	} else {
		sched.Status.NextRun = &metav1.Time{Time: next}
	}
	if err := c.Status().Update(ctx, &sched); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("claiming: %w", err)
	}

	trigger := agentsv1alpha1.RunTriggerSchedule
	task := sched.Spec.Task
	switch sched.Spec.Type {
	case agentsv1alpha1.ScheduleTypeHeartbeat:
		trigger = agentsv1alpha1.RunTriggerHeartbeat
		task = schedulepolicy.HeartbeatPrompt(sched.Spec.Checklist)
	case agentsv1alpha1.ScheduleTypeWakeup:
		trigger = agentsv1alpha1.RunTriggerWakeup
	}
	if strings.TrimSpace(task) == "" {
		sched.Status.DisabledReason = "schedule has no task/checklist"
		setValidated(&sched, agentsv1alpha1.ReasonInvalidSpec, "schedule has no task/checklist to run")
		return ctrl.Result{}, r.updateStatus(ctx, c, &sched)
	}

	cluster := req.ClusterName.String()
	if err := r.Submit.Submit(ctx, executor.Job{
		ID:            fmt.Sprintf("%s/%s/%d", cluster, sched.Name, now.Unix()),
		Kind:          executor.KindSchedule,
		ClusterID:     cluster,
		SourceName:    sched.Name,
		AgentRef:      sched.Spec.AgentRef,
		Task:          task,
		Trigger:       trigger,
		SessionID:     "schedule:" + sched.Name,
		NotifyChannel: sched.Spec.ChannelRef,
	}); err != nil {
		// The fire is claimed on the CR; a refused submit (queue full, executor
		// stopping) is a lost fire, not a retry — re-firing would double-run
		// once the queue drains. Say so and move on to the next occurrence.
		logger.Error(err, "schedule fire not queued")
	} else {
		logger.Info("schedule fired", "trigger", trigger, "next", next)
	}
	if next.IsZero() {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: requeueIn(next, now)}, nil
}

// updateStatus writes status and treats a conflict as "someone else already
// moved this object": the watch re-delivers it and the next reconcile starts
// from the fresh copy.
func (r *Reconciler) updateStatus(ctx context.Context, c client.Client, sched *agentsv1alpha1.Schedule) error {
	if err := c.Status().Update(ctx, sched); err != nil && !apierrors.IsConflict(err) {
		return err
	}
	return nil
}

// requeueIn is how long to sleep until planned, clamped to [MinRequeue,
// MaxRequeue]. A planned time already in the past yields MinRequeue: the
// reconcile that computed it did not see it as due (clock skew between the
// arm and the check), and one second later it will.
func requeueIn(planned, now time.Time) time.Duration {
	d := planned.Sub(now)
	if d < MinRequeue {
		return MinRequeue
	}
	if d > MaxRequeue {
		return MaxRequeue
	}
	return d
}

// ---- the Validated condition ------------------------------------------------

// setValidated records the verdict. reason=="" is valid; anything else is a
// Validated=False with that reason and message. Reports whether the stored
// conditions changed, so a reconcile that found nothing new writes nothing.
func setValidated(sched *agentsv1alpha1.Schedule, reason, message string) bool {
	cond := metav1.Condition{
		Type:               agentsv1alpha1.ConditionValidated,
		Status:             metav1.ConditionTrue,
		Reason:             agentsv1alpha1.ReasonValidated,
		Message:            "the schedule spec is usable",
		ObservedGeneration: sched.Generation,
	}
	if reason != "" {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, reason, message
	}
	return meta.SetStatusCondition(&sched.Status.Conditions, cond)
}

// validate returns the first problem with the schedule, as (reason, message).
// An error is a failed read, not a verdict.
//
// This is what applyScheduleCreate checked before the resource existed, plus
// the two cross-object checks it could not make cheaply: does the agent this
// schedule drives exist, and is its channelRef a channel that agent has. The
// cron expression and timezone are not parsed here — schedulepolicy.Due
// already does that on every reconcile and its verdict reaches the condition
// through DisabledReason below, so there is one parser, not two that can
// disagree.
func (r *Reconciler) validate(ctx context.Context, c client.Client, sched *agentsv1alpha1.Schedule) (reason, message string, err error) {
	// A schedule the scheduler has already given up on says why; that is the
	// most specific thing anyone can be told about it.
	if d := strings.TrimSpace(sched.Status.DisabledReason); d != "" {
		return agentsv1alpha1.ReasonInvalidSpec, d, nil
	}
	agentRef := strings.TrimSpace(sched.Spec.AgentRef)
	if agentRef == "" {
		return agentsv1alpha1.ReasonInvalidSpec, "spec.agentRef is required", nil
	}
	switch sched.Spec.Type {
	case agentsv1alpha1.ScheduleTypeCron, agentsv1alpha1.ScheduleTypeHeartbeat:
		if strings.TrimSpace(sched.Spec.Schedule) == "" {
			return agentsv1alpha1.ReasonInvalidSpec,
				"spec.schedule (a cron expression) is required for " + sched.Spec.Type + " schedules", nil
		}
	case agentsv1alpha1.ScheduleTypeWakeup:
		if sched.Spec.RunAt == nil {
			return agentsv1alpha1.ReasonInvalidSpec, "spec.runAt is required for wakeup schedules", nil
		}
	case "":
		return agentsv1alpha1.ReasonInvalidSpec, "spec.type is required", nil
	default:
		return agentsv1alpha1.ReasonInvalidSpec,
			fmt.Sprintf("spec.type %q must be cron, wakeup, or heartbeat", sched.Spec.Type), nil
	}
	// What the schedule actually runs. The fire path disables a schedule that
	// reaches its moment with nothing to do; saying so up front turns a silent
	// wait into an answer.
	if sched.Spec.Type == agentsv1alpha1.ScheduleTypeHeartbeat {
		if strings.TrimSpace(sched.Spec.Checklist) == "" {
			return agentsv1alpha1.ReasonInvalidSpec, "spec.checklist is required for heartbeat schedules", nil
		}
	} else if strings.TrimSpace(sched.Spec.Task) == "" {
		return agentsv1alpha1.ReasonInvalidSpec, "spec.task is required for " + sched.Spec.Type + " schedules", nil
	}

	var agent agentsv1alpha1.Agent
	switch err := c.Get(ctx, types.NamespacedName{Name: agentRef}, &agent); {
	case apierrors.IsNotFound(err):
		return agentsv1alpha1.ReasonUnknownAgentRef,
			fmt.Sprintf("spec.agentRef names agent %q, which does not exist", agentRef), nil
	case err != nil:
		return "", "", err
	}
	// A channelRef that names nothing degrades rather than breaks — delivery
	// falls back to the agent's primary channel — so the schedule still runs;
	// it just answers somewhere its author did not ask for.
	if role := strings.TrimSpace(sched.Spec.ChannelRef); role != "" {
		found := false
		for _, ch := range agent.Spec.Channels {
			if strings.TrimSpace(ch.Name) == role {
				found = true
				break
			}
		}
		if !found {
			return agentsv1alpha1.ReasonInvalidSpec,
				fmt.Sprintf("spec.channelRef %q is not a channel of agent %q; output falls back to the primary channel", role, agentRef), nil
		}
	}
	return "", "", nil
}
