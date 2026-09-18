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
package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
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

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-schedule").
		For(&agentsv1alpha1.Schedule{}).
		Complete(r)
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
	if !sched.DeletionTimestamp.IsZero() || sched.Spec.Suspend || sched.Status.DisabledReason != "" {
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
		sched.Status.DisabledReason = permErr.Error()
		sched.Status.ObservedGeneration = sched.Generation
		return ctrl.Result{}, r.updateStatus(ctx, c, &sched)
	}

	if !fire {
		// Persist a freshly-armed nextRun on first sight or after a spec edit,
		// and record the observed generation so the re-arm happens exactly once.
		changed := false
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
