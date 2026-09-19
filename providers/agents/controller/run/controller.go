// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package run reconciles Run CRs — the projection of one agent execution, and
// the durable queue the unattended ones are executed from.
//
// Three jobs.
//
// **The claim.** A Run submitted by a schedule, a trigger or an inbound channel
// message is written Pending with no owner, and nothing else happens: the
// object IS the queue. This reconciler claims one by writing its own identity
// to status.owner through the status subresource, and optimistic concurrency
// settles the race — a loser sees a conflict and drops the run rather than
// running it twice. Only then is it handed to the local worker pool.
//
// That replaces an in-process channel. The channel lost every queued job on a
// restart, so a schedule that fired seconds before a deploy simply never ran,
// and an inbound webhook had to be answered with 503 + Retry-After whenever the
// pool was saturated. Neither is true of an object: the watch re-delivers every
// Run when the process starts, so unclaimed work is picked up by definition,
// and a producer's job is done once the write returns.
//
// A claim is not a lease held open. Because only the leader reconciles, an
// owner that is not this process is a PREVIOUS leader, and a run still sitting
// unstarted under one past ClaimGrace is work nobody is doing. status.attempt
// counts the claims so a run that kills whatever picks it up is eventually
// closed instead of re-queued forever by each new leader — the failure mode
// that otherwise stops the provider from ever starting cleanly again.
//
// **The deadline.** A run is allowed a bounded amount of time
// (Agent.spec.limits.timeoutSeconds, with a platform default). Enforcing that
// used to be a 30-second sweep that listed every non-terminal run in every
// tenant and compared timestamps: a timer doing a table scan to discover
// something each object already knows. Here the deadline is written on the
// object when the run starts, and the reconciler requeues itself to wake at
// exactly that instant. This is the sanctioned computed-deadline RequeueAfter
// from the contract — the interval is derived from the object's own state, not
// a fixed poll standing in for a watch.
//
// A run past its deadline is not killed from here. The reconciler says the
// deadline passed; the provider's executor, which holds the run's context, is
// what stops it. The two meet at the store: TimedOut records the request, the
// engine reads it between tool rounds on whichever replica is executing, and
// the run ends where it is running rather than being declared dead by an
// observer that cannot see it.
//
// **The purge.** A Run's transcript, step trace and checkpoint are Postgres
// rows keyed by the object's name. Deleting the object — directly, or by
// garbage collection when its owning Agent goes away — has to take them with
// it, or a tenant who deletes a run still has its content, invisible and
// billable. A finalizer closes that, on the same terms as the Agent's: it is
// added only when PurgeData is wired, so the dev/in-memory path never grows a
// finalizer nothing would clear, and it releases after purgeGiveUp however the
// purge goes, because a run object nobody can delete is worse than a row
// nobody collects.
package run

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// DataFinalizer guards a Run until its rows in the provider store have been
// purged. Namespaced to this provider so nothing else claims it.
const DataFinalizer = "agents.railgrid.ai/purge-run-data"

const (
	// ClaimGrace is how long a claim holds before another process may take the
	// run. It bounds how long a run sits unstarted after the leader that
	// claimed it died, and it must comfortably exceed the time between writing
	// the claim and writing Running — otherwise a healthy claim looks abandoned
	// and the run is executed twice.
	ClaimGrace = 2 * time.Minute
	// MaxClaims caps how many times a run may be claimed. A run that kills the
	// process executing it would otherwise be re-queued by every new leader,
	// forever, and the provider would never start cleanly again.
	MaxClaims = 3
	// purgeRetryInterval spaces retries of a failed purge.
	purgeRetryInterval = 30 * time.Second
	// purgeGiveUp bounds how long a delete may be held open for the purge.
	purgeGiveUp = 10 * time.Minute
	// DefaultTimeout is how long a run may take when its agent sets no
	// spec.limits.timeoutSeconds. Generous: a research run with a dozen tool
	// calls is ordinary, and a deadline that fires on healthy work is worse
	// than one that fires late.
	DefaultTimeout = 30 * time.Minute
	// MaxTimeout caps what an agent may ask for, so one agent cannot pin a
	// worker indefinitely.
	MaxTimeout = 2 * time.Hour
	// deadlineSlack is added to the requeue so the reconciler wakes just after
	// the deadline rather than just before it and having to requeue again.
	deadlineSlack = time.Second
)

// deadlineExceededReason is what a timed-out run says about itself. It names
// the limit and where to change it, because "aborted" alone leaves the reader
// unable to tell a deadline from a crash.
const deadlineExceededReason = "the run exceeded its time limit (the agent's spec.limits.timeoutSeconds) and was stopped"

// Reconciler enforces run deadlines and purges a run's store data on delete.
type Reconciler struct {
	Manager mcmanager.Manager

	// PurgeData removes one run's rows (transcript, tool calls, the run record)
	// from the provider store. clusterID is the tenant logical cluster, which
	// the provider maps to the store's org/workspace scope on its side — the
	// reconciler has no tenant identity of its own and must not invent one.
	//
	// nil (no store configured, or the dev in-memory path) disables the
	// finalizer entirely rather than adding one nothing would ever clear.
	PurgeData func(ctx context.Context, clusterID, agentName, runID string) error

	// Dispatch hands a claimed run to the local worker pool. The reconciler has
	// decided WHEN; everything about how a run executes — the virtual-workspace
	// client, the agent's identity, the toolset — belongs to the provider and
	// stays there.
	//
	// It is called only after the claim has been written and accepted, so an
	// implementation may assume this process owns the run. nil leaves unattended
	// runs queued and unexecuted, which is the right answer for a replica with
	// no tenant access.
	Dispatch func(ctx context.Context, clusterID string, object *agentsv1alpha1.Run) error

	// Recover picks up a run that was already EXECUTING when the process doing
	// it went away. Unlike Dispatch, which starts fresh work, this resumes from
	// whatever the run checkpointed — or closes it honestly and tells whoever
	// was waiting, when it cannot be resumed.
	//
	// It is the per-object half of what used to be a sweep over every
	// non-terminal run in every tenant, every thirty seconds. The policy
	// (resumable? attempted too often? cancelled meanwhile?) stays with the
	// provider, which owns the checkpoint; the reconciler only decides when to
	// ask, and it knows because the run's own claim went stale.
	Recover func(ctx context.Context, clusterID, agentName, runID string) error

	// Stop closes a run the reconciler has decided must not continue: one past
	// its deadline, or one that has been claimed MaxClaims times without
	// finishing. The executor holding the run's context is what actually stops
	// it; this is how the reconciler says so, and the two meet at the durable
	// cancel flag the engine reads between tool rounds.
	Stop func(ctx context.Context, clusterID, agentName, runID, reason string) error

	// ProcessID identifies this process as an owner. Empty disables claiming
	// outright: a process that cannot say who it is must not take work, because
	// nothing could then tell its claim from an abandoned one.
	ProcessID string

	// Now is the clock; nil means time.Now. Tests pin it.
	Now func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// SetupWithManager wires the reconciler into the multicluster manager.
//
// Runs only — deliberately. An Agent's timeout changing mid-run does not move
// a deadline that is already written: the run was admitted under the limit in
// force when it started, and re-deriving it would let an edit shorten work
// already in flight.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-run").
		For(&agentsv1alpha1.Run{}).
		Complete(r)
}

// Reconcile handles one Run: purge-and-release on the way out, otherwise the
// deadline.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()

	var object agentsv1alpha1.Run
	if err := c.Get(ctx, req.NamespacedName, &object); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	clusterID := req.ClusterName.String()

	if !object.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, c, &object, clusterID)
	}
	if r.PurgeData != nil && !controllerutil.ContainsFinalizer(&object, DataFinalizer) {
		controllerutil.AddFinalizer(&object, DataFinalizer)
		if err := c.Update(ctx, &object); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}
		// The metadata write re-delivers the object; status is written from
		// the fresh copy rather than the stale one in hand.
		return ctrl.Result{}, nil
	}

	if object.Status.Attempt >= MaxClaims && !agentsv1alpha1.RunIsSettled(object.Status.Phase) && object.Status.Phase == agentsv1alpha1.RunPhasePending {
		// Picked up its last permitted time and still not started. Close it
		// rather than leave a Pending object nothing will ever execute.
		if err := r.abandon(ctx, &object, clusterID); err != nil {
			return ctrl.Result{RequeueAfter: purgeRetryInterval}, nil //nolint:nilerr // reported on the object, retried on a timer
		}
		return ctrl.Result{}, nil
	}
	if claimable, requeue := r.claimable(&object); claimable {
		return r.claimAndDispatch(ctx, c, &object, clusterID)
	} else if requeue > 0 {
		// Somebody else's claim, still inside the grace. Wake when it lapses so
		// an abandoned run is picked up then rather than at the next unrelated
		// event.
		return ctrl.Result{RequeueAfter: requeue}, nil
	}

	if stranded, requeue := r.stranded(&object); stranded {
		if err := r.Recover(ctx, clusterID, object.Spec.AgentRef, object.Name); err != nil {
			klog.FromContext(ctx).Error(err, "recovering a stranded run", "run", object.Name, "cluster", clusterID)
			return ctrl.Result{RequeueAfter: purgeRetryInterval}, nil
		}
		return ctrl.Result{}, nil
	} else if requeue > 0 {
		return ctrl.Result{RequeueAfter: requeue}, nil
	}

	return r.enforceDeadline(ctx, c, &object, clusterID)
}

// UnattendedKinds are the delivery kinds this reconciler executes: work with
// nobody holding a connection open for it. A run a person started executes on
// the replica that served their request and is already owned by the time the
// object exists, so it is never claimable here — which is what keeps a chat
// turn from being run a second time by the leader.
var UnattendedKinds = map[string]bool{"schedule": true, "trigger": true, "channel": true}

// claimable reports whether this process may take the run, and otherwise how
// long to wait before asking again.
//
// The rule is deliberately narrow: unattended work, not yet started, and either
// unclaimed or claimed by a process that has had its chance. Everything else is
// somebody's — and a run executed twice is worse than a run executed late.
func (r *Reconciler) claimable(object *agentsv1alpha1.Run) (bool, time.Duration) {
	if r.Dispatch == nil || r.ProcessID == "" {
		return false, 0
	}
	if object.Spec.Delivery == nil || !UnattendedKinds[object.Spec.Delivery.Kind] {
		return false, 0
	}
	// Anything that has started, settled, or been asked to stop is not queued
	// work. Only Pending is.
	if object.Status.Phase != "" && object.Status.Phase != agentsv1alpha1.RunPhasePending {
		return false, 0
	}
	if object.Status.Attempt >= MaxClaims {
		return false, 0
	}
	if object.Status.Owner == "" {
		return true, 0
	}
	if object.Status.Owner == r.ProcessID {
		// Our own claim. The dispatch already happened; re-running it because
		// the object was re-delivered is exactly the double execution the
		// claim exists to prevent.
		return false, 0
	}
	if object.Status.ClaimedAt == nil {
		// Claimed by someone who did not say when. Treat it as fresh rather
		// than abandoned: a missing timestamp is a bug, and the safe reading of
		// a bug is "somebody may be running this".
		return false, ClaimGrace
	}
	if held := r.now().Sub(object.Status.ClaimedAt.Time); held < ClaimGrace {
		return false, ClaimGrace - held
	}
	return true, 0
}

// stranded reports whether a run was executing on a process that is gone, and
// otherwise how long to wait before asking again.
//
// "Gone" is inferred, not observed, and the inference is only sound because
// exactly one process reconciles: an owner that is not this one, on a run that
// has not settled, is a previous leader. The grace is what separates that from
// a claim written moments ago by the leader this process just succeeded.
func (r *Reconciler) stranded(object *agentsv1alpha1.Run) (bool, time.Duration) {
	if r.Recover == nil || r.ProcessID == "" {
		return false, 0
	}
	// Pending is the claim path's business; settled runs are nobody's.
	if object.Status.Phase == "" || object.Status.Phase == agentsv1alpha1.RunPhasePending ||
		agentsv1alpha1.RunIsSettled(object.Status.Phase) {
		return false, 0
	}
	if object.Status.Owner == "" || object.Status.Owner == r.ProcessID {
		// Unowned in-flight work predates the claim (or this process is doing
		// it). Either way, the deadline is what bounds it.
		return false, 0
	}
	if object.Status.ClaimedAt == nil {
		return false, ClaimGrace
	}
	if held := r.now().Sub(object.Status.ClaimedAt.Time); held < ClaimGrace {
		return false, ClaimGrace - held
	}
	return true, 0
}

// claimAndDispatch takes the run and hands it to the local pool.
//
// The status write is the lock. It goes first and its conflict is the whole
// concurrency story: two processes racing for the same run both write, one
// wins, and the loser returns without dispatching.
func (r *Reconciler) claimAndDispatch(ctx context.Context, c client.Client, object *agentsv1alpha1.Run, clusterID string) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("run", object.Name, "agent", object.Spec.AgentRef, "cluster", clusterID)

	now := metav1.NewTime(r.now())
	object.Status.Owner = r.ProcessID
	object.Status.ClaimedAt = &now
	object.Status.Attempt++
	if object.Status.Phase == "" {
		object.Status.Phase = agentsv1alpha1.RunPhasePending
	}
	attempt := object.Status.Attempt
	if err := c.Status().Update(ctx, object); err != nil {
		if apierrors.IsConflict(err) {
			// Lost the race. The winner is running it; there is nothing here to
			// retry and nothing to report.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if attempt >= MaxClaims {
		// This was the last chance. Say so on the way in rather than after it
		// fails again, so the reason survives whatever it does to the process.
		logger.Info("claiming a run for the last permitted attempt", "attempt", attempt, "max", MaxClaims)
	}
	if err := r.Dispatch(ctx, clusterID, object); err != nil {
		// The claim stands: releasing it here would hand the run straight back
		// to a process that has just failed to take it. ClaimGrace is what
		// gives it to somebody else, and status.attempt is what stops that
		// going on forever.
		logger.Error(err, "dispatching a claimed run", "attempt", attempt)
		return ctrl.Result{RequeueAfter: ClaimGrace}, nil
	}
	logger.V(2).Info("run claimed and dispatched", "attempt", attempt, "owner", r.ProcessID)
	return ctrl.Result{}, nil
}

// abandon closes a run that has been claimed too many times without finishing.
func (r *Reconciler) abandon(ctx context.Context, object *agentsv1alpha1.Run, clusterID string) error {
	if r.Stop == nil {
		return nil
	}
	return r.Stop(ctx, clusterID, object.Spec.AgentRef, object.Name,
		"this run was picked up "+itoa(int(object.Status.Attempt))+" times without finishing, so it will not be retried again")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// enforceDeadline stamps the deadline on a run that has started without one,
// sleeps until it, and reports a run that has passed it.
func (r *Reconciler) enforceDeadline(ctx context.Context, c client.Client, object *agentsv1alpha1.Run, clusterID string) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("run", object.Name, "agent", object.Spec.AgentRef, "cluster", clusterID)

	// A settled run has no deadline to keep. PendingApproval counts as settled:
	// it is waiting on a human, and a person who takes an afternoon to approve
	// a tool call has not made the run overrun.
	if agentsv1alpha1.RunIsSettled(object.Status.Phase) {
		if object.Status.DeadlineAt == nil {
			return ctrl.Result{}, nil
		}
		object.Status.DeadlineAt = nil
		return ctrl.Result{}, ignoreConflict(c.Status().Update(ctx, object))
	}
	if object.Status.StartedAt == nil {
		// Accepted but not started. There is nothing to time out yet, and the
		// transition to Running re-delivers the object.
		return ctrl.Result{}, nil
	}

	deadline := object.Status.DeadlineAt
	if deadline == nil {
		at, err := r.deadlineFor(ctx, c, object)
		if err != nil {
			return ctrl.Result{}, err
		}
		object.Status.DeadlineAt = &at
		if err := c.Status().Update(ctx, object); err != nil {
			return ctrl.Result{}, ignoreConflict(err)
		}
		deadline = &at
	}

	if remaining := deadline.Sub(r.now()); remaining > 0 {
		// The sanctioned computed-deadline requeue: the interval comes from the
		// object, so this wakes once, when there is something to do.
		return ctrl.Result{RequeueAfter: remaining + deadlineSlack}, nil
	}

	if r.Stop == nil {
		logger.V(2).Info("run is past its deadline but no stop handler is configured")
		return ctrl.Result{}, nil
	}
	if err := r.Stop(ctx, clusterID, object.Spec.AgentRef, object.Name, deadlineExceededReason); err != nil {
		logger.Error(err, "recording the run's timeout; will retry")
		return ctrl.Result{RequeueAfter: purgeRetryInterval}, nil
	}
	// Say so on the object too. The executor writes the terminal phase when it
	// notices; the condition is what explains WHY to anyone reading in between.
	if meta.SetStatusCondition(&object.Status.Conditions, metav1.Condition{
		Type:               agentsv1alpha1.ConditionValidated,
		Status:             metav1.ConditionFalse,
		Reason:             agentsv1alpha1.ReasonRunDeadlineExceeded,
		Message:            "the run exceeded its time limit and was asked to stop",
		ObservedGeneration: object.Generation,
	}) {
		return ctrl.Result{}, ignoreConflict(c.Status().Update(ctx, object))
	}
	return ctrl.Result{}, nil
}

// deadlineFor is startedAt plus the agent's per-run limit.
//
// The limit is read from the Agent at the moment the run starts and then
// written down, so a later edit to the agent cannot shorten work already under
// way. An agent that cannot be read falls back to the default rather than
// failing the reconcile: a run with no enforceable deadline is worse than one
// with a generous one.
func (r *Reconciler) deadlineFor(ctx context.Context, c client.Client, object *agentsv1alpha1.Run) (metav1.Time, error) {
	timeout := DefaultTimeout
	var agent agentsv1alpha1.Agent
	switch err := c.Get(ctx, client.ObjectKey{Name: object.Spec.AgentRef}, &agent); {
	case err == nil:
		if seconds := agent.Spec.Limits.TimeoutSeconds; seconds > 0 {
			timeout = min(time.Duration(seconds)*time.Second, MaxTimeout)
		}
	case apierrors.IsNotFound(err):
		// The agent was deleted while its run was in flight. The run still gets
		// a deadline; garbage collection of the object is a separate story.
	default:
		return metav1.Time{}, err
	}
	return metav1.NewTime(object.Status.StartedAt.Add(timeout)), nil
}

// finalize purges the run's rows from the provider store and releases the
// finalizer.
//
// The release is unconditional past purgeGiveUp, for the same reason the
// Agent's is: holding a deletion open on a failing purge trades a recoverable
// problem (rows an operator can delete) for an unrecoverable one (an object
// the user cannot remove and cannot explain).
func (r *Reconciler) finalize(ctx context.Context, c client.Client, object *agentsv1alpha1.Run, clusterID string) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("run", object.Name, "agent", object.Spec.AgentRef, "cluster", clusterID)
	if !controllerutil.ContainsFinalizer(object, DataFinalizer) {
		return ctrl.Result{}, nil
	}
	if r.PurgeData != nil {
		if err := r.PurgeData(ctx, clusterID, object.Spec.AgentRef, object.Name); err != nil {
			if r.now().Sub(object.DeletionTimestamp.Time) < purgeGiveUp {
				logger.Error(err, "purging the run's store data; will retry", "retryIn", purgeRetryInterval)
				return ctrl.Result{RequeueAfter: purgeRetryInterval}, nil
			}
			logger.Error(err, "giving up on purging the run's store data; releasing the finalizer and leaving the rows behind",
				"deadline", purgeGiveUp)
		}
	}
	controllerutil.RemoveFinalizer(object, DataFinalizer)
	if err := c.Update(ctx, object); err != nil {
		return ctrl.Result{}, ignoreConflict(err)
	}
	logger.V(2).Info("run store data purged and finalizer released")
	return ctrl.Result{}, nil
}

// ignoreConflict swallows the one error that is not a problem: a conflict means
// a newer object exists and the watch is about to deliver it.
func ignoreConflict(err error) error {
	if apierrors.IsConflict(err) {
		return nil
	}
	return err
}
