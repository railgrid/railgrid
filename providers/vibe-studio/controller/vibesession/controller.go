// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Package vibesession reconciles Session CRs — the control-plane projection
// of conversations. Two jobs, both deterministic:
//
//  1. Status mirror: fold the store's event log and project phase /
//     checkpoints / active turn into Session.status, so `kubectl get
//     sessions.vibe` tells the truth without touching Postgres.
//  2. Purge finalizer: when the Session CR is deleted, remove everything the
//     store holds for it (events, workspace files, listing row). The owned
//     Project is garbage-collected by kcp via its ownerReference.
//
// The store is authoritative for conversation data; the CR is its
// projection. The tenant annotation bridges the two keyspaces (the store is
// keyed by hub tenant path; the reconciler only knows the cluster).
package vibesession

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/session"
	"github.com/faroshq/provider-vibe-studio/store"
)

// mirrorInterval is how often the projection refreshes when nothing else
// triggers a reconcile (store changes are invisible to the watch).
const mirrorInterval = 30 * time.Second

// Reconciler owns a Session end to end: it drives provisioning as the
// session's ServiceAccount, projects store state into Session.status, and
// purges the store when the CR is deleted.
type Reconciler struct {
	Manager mcmanager.Manager
	Store   store.Store
	// HubBase / HubInsecure address the hub for data-plane and MCP calls.
	HubBase     string
	HubInsecure bool
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("vibe-studio-session").
		For(&vibev1alpha1.Session{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("cluster %q: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var s vibev1alpha1.Session
	if err := c.Get(ctx, req.NamespacedName, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	scope := store.Scope{Tenant: s.Annotations[vibev1alpha1.SessionTenantAnnotation]}

	if !s.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&s, vibev1alpha1.SessionFinalizer) {
			return ctrl.Result{}, nil
		}
		// Purge the data plane. A missing tenant annotation means there is
		// nothing addressable to purge — release rather than wedge deletion.
		if scope.Tenant != "" {
			if err := r.Store.PurgeSession(ctx, scope, s.Name); err != nil {
				return ctrl.Result{}, fmt.Errorf("purging session %s: %w", s.Name, err)
			}
		}
		controllerutil.RemoveFinalizer(&s, vibev1alpha1.SessionFinalizer)
		return ctrl.Result{}, c.Update(ctx, &s)
	}

	if !controllerutil.ContainsFinalizer(&s, vibev1alpha1.SessionFinalizer) {
		controllerutil.AddFinalizer(&s, vibev1alpha1.SessionFinalizer)
		if err := c.Update(ctx, &s); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if scope.Tenant == "" {
		// Unprojectable without the tenant key; nothing else to do.
		return ctrl.Result{}, nil
	}
	events, err := r.Store.ListEvents(ctx, scope, s.Name, 0, 0)
	if err != nil {
		if err == store.ErrNotFound {
			// Store has nothing (yet, or already purged out-of-band).
			return ctrl.Result{RequeueAfter: mirrorInterval}, nil
		}
		return ctrl.Result{}, err
	}
	state := session.Fold(events)
	next := ProjectStatus(state, metav1.Now())
	// The commit bookkeeping is the controller's own, not a projection of
	// the log — carry it across the mirror.
	next.WorkspaceRevision = s.Status.WorkspaceRevision
	next.CommittedRevision = s.Status.CommittedRevision
	next.LastCommitSHA = s.Status.LastCommitSHA
	next.CommittedOrdinal = s.Status.CommittedOrdinal
	if rev, err := r.Store.WorkspaceRevision(ctx, scope, s.Name); err == nil {
		next.WorkspaceRevision = rev
	}
	if !statusEqual(s.Status, next) {
		s.Status = next
		if err := c.Status().Update(ctx, &s); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Keep git in step with the sandbox: commit once the workspace has moved
	// on and the turn that moved it has finished.
	if state.Phase == session.PhaseStudio && state.ActiveTurnID == "" {
		if committed, err := r.commitWorkspace(ctx, c, &s, scope, state); err != nil {
			return ctrl.Result{}, err
		} else if committed {
			return ctrl.Result{RequeueAfter: mirrorInterval}, nil
		}
	}

	// Report where a promotion got to (the Project reconciler does the work).
	if err := r.mirrorPromotion(ctx, c, &s, scope, state); err != nil {
		return ctrl.Result{}, fmt.Errorf("mirroring promotion: %w", err)
	}

	// Provisioning is this controller's work, not the API server's: it runs
	// as the session's own ServiceAccount and retries on its own schedule.
	if session.NextAction(state) == session.ActionRunProvision {
		res, err := r.runProvisioning(ctx, c, &s, scope, state)
		if err != nil {
			return ctrl.Result{}, err
		}
		if res.requeueAfter > 0 {
			return ctrl.Result{RequeueAfter: res.requeueAfter}, nil
		}
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{RequeueAfter: mirrorInterval}, nil
}

// ProjectStatus folds session state into the CR status projection. Pure.
func ProjectStatus(st session.SessionState, now metav1.Time) vibev1alpha1.SessionStatus {
	out := vibev1alpha1.SessionStatus{
		Phase:        string(st.Phase),
		ActiveTurnID: st.ActiveTurnID,
		LastOrdinal:  st.LastOrdinal,
		UpdatedAt:    &now,
	}
	for _, name := range []session.CheckpointName{
		session.CheckpointTemplate, session.CheckpointGit, session.CheckpointCI, session.CheckpointProduction,
	} {
		if cp, ok := st.Checkpoints[name]; ok {
			out.Checkpoints = append(out.Checkpoints, vibev1alpha1.SessionCheckpointStatus{
				Name: string(cp.Name), State: string(cp.State), Reason: cp.Reason,
			})
		}
	}
	return out
}

// statusEqual ignores UpdatedAt so quiet mirrors don't churn resourceVersion.
func statusEqual(a, b vibev1alpha1.SessionStatus) bool {
	if a.Phase != b.Phase || a.ActiveTurnID != b.ActiveTurnID || a.LastOrdinal != b.LastOrdinal ||
		a.WorkspaceRevision != b.WorkspaceRevision || a.CommittedRevision != b.CommittedRevision ||
		a.LastCommitSHA != b.LastCommitSHA || a.CommittedOrdinal != b.CommittedOrdinal ||
		len(a.Checkpoints) != len(b.Checkpoints) {
		return false
	}
	for i := range a.Checkpoints {
		if a.Checkpoints[i] != b.Checkpoints[i] {
			return false
		}
	}
	return true
}
