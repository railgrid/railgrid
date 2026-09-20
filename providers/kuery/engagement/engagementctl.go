// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engagement

import (
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/railgrid/provider-sdk/sharding"

	kuerystore "github.com/railgrid/kuery/pkg/store"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

// The Engagement reconciler runs on the provider's OWN workspace — the
// multicluster manager's local manager — and it is what replaced two timers:
//
//   - the one-minute runOrphanSweep that listed every "active" cluster row and
//     marked anything without a recent heartbeat stale. Staleness is now a
//     consequence of a Lease expiring, and the Lease is watched, so the sweep
//     happens when the fact changes rather than up to a minute later;
//   - the five-minute garbage-collection ticker over the whole store. Purging
//     one stale engagement's rows is now a RequeueAfter on that engagement,
//     scheduled for the moment its TTL runs out.
//
// Both timers scanned everything to find the few things that had changed. A
// watch plus a deadline does the same work proportional to what actually
// happened.

// purgeGrace is how long a stale or disengaged edge's rows survive before they
// are purged. It matches the TTL written onto the cluster row (kuery's own
// default), so a flapping edge that reconnects inside the window re-engages
// against rows that are still there instead of re-syncing the whole cluster.
const purgeGrace = clusterTTLSeconds * time.Second

// staleFloor bounds how soon after a heartbeat an engagement may be judged
// stale. It is two claim TTLs: one for the owner to miss a renewal, one for a
// peer to take the Lease over before anyone calls the edge stale.
const staleFloor = 2 * claimTTL

// engagementReconciler owns the lifecycle of the Engagement records in the
// provider workspace. It never engages anything itself — that belongs to the
// replica holding the edge's Lease — it only reconciles what the records say
// against what the Leases say.
type engagementReconciler struct {
	client     client.Client
	controller *Controller
	now        func() time.Time
}

// setupEngagementReconciler registers the reconciler on the provider
// workspace's manager, watching Engagements and the per-edge claim Leases
// whose expiry is what makes an engagement stale.
func setupEngagementReconciler(mgr manager.Manager, c *Controller) error {
	r := &engagementReconciler{client: mgr.GetClient(), controller: c, now: time.Now}
	return builder.ControllerManagedBy(mgr).
		Named("kuery-engagement").
		For(&kueryv1alpha1.Engagement{}).
		Watches(&coordinationv1.Lease{}, handler.EnqueueRequestsFromMapFunc(engagementForLease(c.claims))).
		Complete(r)
}

// engagementForLease maps a per-edge claim Lease back to its Engagement. The
// shard stamps the key it claims onto every Lease it writes, so this needs no
// index and no knowledge of how the Lease was named; anything else in the
// namespace (the controller lease, for one) maps to nothing.
func engagementForLease(claims *sharding.Shard) handler.MapFunc {
	return func(_ context.Context, object client.Object) []reconcile.Request {
		storeName, ok := claims.KeyFor(object)
		if !ok {
			return nil
		}
		cluster, edge := SplitStoreName(storeName)
		if cluster == "" || edge == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: EngagementName(cluster, edge)}}}
	}
}

// Reconcile settles one Engagement against its Lease.
//
// Engaged with a live Lease is the steady state and schedules the next check
// for when that Lease could expire. Engaged with an expired Lease is the
// orphan case the old sweep existed for: the record goes Stale and the index
// rows are marked so nothing reads them. Stale or Disengaged schedules the
// purge, and performs it once the grace has passed.
func (r *engagementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("engagement", req.Name)

	engagement := &kueryv1alpha1.Engagement{}
	if err := r.client.Get(ctx, req.NamespacedName, engagement); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !engagement.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	storeName := StoreName(engagement.Spec.Cluster, engagement.Spec.Edge)
	now := r.now()

	holder, err := r.leaseHolderFor(ctx, storeName, now)
	if err != nil {
		return ctrl.Result{}, err
	}

	switch engagement.Status.Phase {
	case kueryv1alpha1.EngagementPhaseEngaged, kueryv1alpha1.EngagementPhasePending:
		if holder != "" {
			// Somebody is syncing it. Re-check when the claim could lapse.
			return ctrl.Result{RequeueAfter: r.staleCheckDelay(engagement, now)}, nil
		}
		// Nobody holds the claim: this is the orphan the one-minute sweep used
		// to find. Mark the index rows stale (kuery's GC only reaps rows whose
		// status is "stale") and leave last_seen alone, so the row expires
		// relative to the heartbeat it actually last received.
		if err := r.markRowsStale(ctx, storeName); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.setPhase(ctx, engagement, kueryv1alpha1.EngagementPhaseStale, "",
			"no replica holds this edge's claim"); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("engagement went stale", "edge", storeName)
		return ctrl.Result{RequeueAfter: purgeGrace}, nil

	case kueryv1alpha1.EngagementPhaseStale, kueryv1alpha1.EngagementPhaseDisengaged:
		if holder != "" {
			// A replica picked it back up; the owner's own heartbeat will
			// return it to Engaged.
			return ctrl.Result{RequeueAfter: r.staleCheckDelay(engagement, now)}, nil
		}
		remaining := r.purgeDelay(engagement, now)
		if remaining > 0 {
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
		if err := r.purge(ctx, storeName); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("purged an expired engagement", "edge", storeName)
		// The record goes with the rows: it is rebuilt from the workspace's
		// edge watch if the edge ever comes back.
		if err := r.client.Delete(ctx, engagement); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("deleting engagement %s: %w", engagement.Name, err)
		}
		return ctrl.Result{}, nil

	default:
		// No phase yet: the record was just created and its owner has not
		// reported. Give the claim one TTL to be taken.
		if holder == "" {
			return ctrl.Result{RequeueAfter: claimTTL}, nil
		}
		return ctrl.Result{RequeueAfter: r.staleCheckDelay(engagement, now)}, nil
	}
}

// leaseHolderFor reads the edge's claim. A missing Lease is "nobody", not an
// error: it is exactly what a released or never-taken claim looks like. An
// expired one is nobody too, on the Lease's own declared terms — that judgment
// lives in the SDK, so the reconciler and the replicas holding the claims can
// never disagree about who owns an edge.
func (r *engagementReconciler) leaseHolderFor(ctx context.Context, storeName string, now time.Time) (string, error) {
	claims := r.controller.claims
	lease := &coordinationv1.Lease{}
	key := client.ObjectKey{Namespace: claimNamespace, Name: claims.LeaseName(storeName)}
	if err := r.client.Get(ctx, key, lease); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading claim for %s: %w", storeName, err)
	}
	return sharding.HolderOf(lease, now), nil
}

// staleCheckDelay is when to look again at a healthy engagement: one claim TTL
// past the point its current heartbeat would go stale, never sooner than
// claimTTL so a busy fleet does not reconcile itself into a hot loop.
func (r *engagementReconciler) staleCheckDelay(engagement *kueryv1alpha1.Engagement, now time.Time) time.Duration {
	if engagement.Status.LastSeen == nil {
		return staleFloor
	}
	deadline := engagement.Status.LastSeen.Add(staleFloor)
	if remaining := deadline.Sub(now); remaining > claimTTL {
		return remaining
	}
	return claimTTL
}

// purgeDelay is how long is left before a stale engagement's rows may go.
func (r *engagementReconciler) purgeDelay(engagement *kueryv1alpha1.Engagement, now time.Time) time.Duration {
	if engagement.Status.LastSeen == nil {
		// Never engaged by anyone: nothing of its is in the index, so there is
		// nothing to wait for.
		return 0
	}
	return engagement.Status.LastSeen.Add(purgeGrace).Sub(now)
}

// setPhase writes one phase transition.
func (r *engagementReconciler) setPhase(
	ctx context.Context,
	engagement *kueryv1alpha1.Engagement,
	phase kueryv1alpha1.EngagementPhase,
	owner, message string,
) error {
	if engagement.Status.Phase == phase && engagement.Status.Owner == owner && engagement.Status.Message == message {
		return nil
	}
	engagement.Status.Phase = phase
	engagement.Status.Owner = owner
	engagement.Status.Message = message
	if err := r.client.Status().Update(ctx, engagement); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("updating engagement %s status: %w", engagement.Name, err)
	}
	return nil
}

// markRowsStale takes one cluster's index rows out of the queryable set
// without deleting them, so a reconnect inside the purge grace is cheap.
// last_seen is deliberately untouched: the row expires relative to the
// heartbeat it actually last received, not a fresh TTL from now.
func (r *engagementReconciler) markRowsStale(ctx context.Context, storeName string) error {
	result := r.controller.cfg.Store.RawDB().WithContext(ctx).
		Model(&kuerystore.ClusterModel{}).
		Where("name = ? AND status = ?", storeName, "active").
		Update("status", "stale")
	if result.Error != nil {
		return fmt.Errorf("marking %s stale: %w", storeName, result.Error)
	}
	return nil
}

// purge deletes one cluster's objects, resource types and row. The index is a
// cache: everything here is rebuildable from the Engagements plus a resync, so
// dropping it is a cost, never a loss.
func (r *engagementReconciler) purge(ctx context.Context, storeName string) error {
	store := r.controller.cfg.Store
	if err := store.DeleteObjectsForCluster(ctx, storeName); err != nil {
		return fmt.Errorf("purging objects for %s: %w", storeName, err)
	}
	if err := store.DeleteResourceTypesForCluster(ctx, storeName); err != nil {
		return fmt.Errorf("purging resource types for %s: %w", storeName, err)
	}
	if err := store.DeleteCluster(ctx, storeName); err != nil {
		return fmt.Errorf("purging cluster row %s: %w", storeName, err)
	}
	return nil
}
