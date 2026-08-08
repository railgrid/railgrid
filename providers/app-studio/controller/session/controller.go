/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package session reconciles Session CRs — the control-plane projection of
// assistant conversation threads. Two jobs, both deterministic (the same
// shape vibe-studio's session reconciler proved):
//
//  1. Status mirror: project the store's thread + active turn into
//     Session.status, so `kubectl get sessions.ai` tells the truth without
//     touching Postgres.
//  2. Purge finalizer: when the Session CR is deleted, remove the thread and
//     its transcript from the store. The Session is ownerRef'd to its
//     Project, so deleting a Project garbage-collects its conversations.
//
// The store is authoritative for conversation data; the CR is its
// projection. The identity annotations bridge the two keyspaces (the store
// is keyed by org/workspace UUIDs; the reconciler only knows the cluster).
package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	"github.com/faroshq/provider-app-studio/store"
)

// mirrorInterval is how often the projection refreshes when nothing else
// triggers a reconcile (store changes are invisible to the watch).
const mirrorInterval = 30 * time.Second

// Reconciler projects store threads into Session CRs and purges the store
// when a Session is deleted.
type Reconciler struct {
	Manager mcmanager.Manager
	Store   store.Store
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("app-studio-session").
		For(&aiv1alpha1.Session{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	if r.Store == nil {
		return ctrl.Result{}, nil
	}
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("cluster %q: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var s aiv1alpha1.Session
	if err := c.Get(ctx, req.NamespacedName, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	scope, ok := scopeOf(&s)
	if !ok {
		// A Session without identity annotations cannot address the store —
		// nothing to mirror, and purging would be guesswork. Leave it inert.
		return ctrl.Result{}, nil
	}

	if !s.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, c, &s, scope)
	}

	if !controllerutil.ContainsFinalizer(&s, aiv1alpha1.SessionFinalizer) {
		controllerutil.AddFinalizer(&s, aiv1alpha1.SessionFinalizer)
		if err := c.Update(ctx, &s); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	next, threadExists, err := r.projectStatus(ctx, scope, s.Spec.ThreadID)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !threadExists {
		// The store row is gone (interactive deletion raced ahead, or a
		// legacy cleanup). The projection has nothing left to project.
		if err := c.Delete(ctx, &s); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if !statusEqual(s.Status, next) {
		s.Status = next
		if err := c.Status().Update(ctx, &s); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: mirrorInterval}, nil
}

// projectStatus folds the store's thread + active turn into a status.
func (r *Reconciler) projectStatus(ctx context.Context, scope store.Scope, threadID string) (aiv1alpha1.SessionStatus, bool, error) {
	thread, err := r.Store.GetAssistantThread(ctx, scope, threadID)
	if errors.Is(err, store.ErrAssistantThreadNotFound) {
		return aiv1alpha1.SessionStatus{}, false, nil
	}
	if err != nil {
		return aiv1alpha1.SessionStatus{}, false, err
	}
	next := aiv1alpha1.SessionStatus{
		Title: thread.Title,
		Phase: string(thread.Status),
	}
	if !thread.UpdatedAt.IsZero() {
		t := metav1.NewTime(thread.UpdatedAt)
		next.UpdatedAt = &t
	}
	turn, err := r.Store.ActiveAssistantTurn(ctx, scope, threadID)
	if err == nil {
		next.ActiveTurnID = turn.ID
		next.ActiveTurnStatus = string(turn.Status)
	} else if !errors.Is(err, store.ErrAssistantTurnNotFound) {
		return aiv1alpha1.SessionStatus{}, true, err
	}
	return next, true, nil
}

// finalize purges the thread from the store, then releases the finalizer.
// DeleteAssistantThread requires the owning actor, so the thread is read
// first; a thread already gone means the purge is done.
func (r *Reconciler) finalize(ctx context.Context, c client.Client, s *aiv1alpha1.Session, scope store.Scope) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(s, aiv1alpha1.SessionFinalizer) {
		return ctrl.Result{}, nil
	}
	thread, err := r.Store.GetAssistantThread(ctx, scope, s.Spec.ThreadID)
	switch {
	case errors.Is(err, store.ErrAssistantThreadNotFound):
		// already purged (interactive deletion, or a prior pass)
	case err != nil:
		return ctrl.Result{}, err
	default:
		if err := r.Store.DeleteAssistantThread(ctx, scope, thread.ID, thread.ActorID); err != nil &&
			!errors.Is(err, store.ErrAssistantThreadNotFound) {
			return ctrl.Result{}, fmt.Errorf("purging thread %s: %w", thread.ID, err)
		}
	}
	controllerutil.RemoveFinalizer(s, aiv1alpha1.SessionFinalizer)
	if err := c.Update(ctx, s); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// scopeOf derives the store scope from the Session's identity annotations
// and project reference.
func scopeOf(s *aiv1alpha1.Session) (store.Scope, bool) {
	org := strings.TrimSpace(s.Annotations[bindings.OrgUUIDAnnotation])
	ws := strings.TrimSpace(s.Annotations[bindings.WorkspaceUUIDAnnotation])
	projectUID := strings.TrimSpace(s.Annotations[projectUIDAnnotation])
	if org == "" || ws == "" || s.Spec.ProjectRef == "" || projectUID == "" {
		return store.Scope{}, false
	}
	return store.Scope{
		OrgUUID:       org,
		WorkspaceUUID: ws,
		ProjectName:   s.Spec.ProjectRef,
		ProjectUID:    projectUID,
	}, true
}

// projectUIDAnnotation records the owning Project's UID — part of the store
// scope key (a recreated Project must not inherit the deleted one's rows).
const projectUIDAnnotation = "ai.kedge.faros.sh/project-uid"

// statusEqual compares mirrored status.
func statusEqual(a, b aiv1alpha1.SessionStatus) bool {
	if a.Title != b.Title || a.Phase != b.Phase ||
		a.ActiveTurnID != b.ActiveTurnID || a.ActiveTurnStatus != b.ActiveTurnStatus {
		return false
	}
	switch {
	case a.UpdatedAt == nil && b.UpdatedAt == nil:
		return true
	case a.UpdatedAt == nil || b.UpdatedAt == nil:
		return false
	default:
		return a.UpdatedAt.Equal(b.UpdatedAt)
	}
}
