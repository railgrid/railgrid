/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package session reconciles Session CRs — the control-plane projection of
// assistant conversation threads. It has two deterministic jobs:
//
//  1. Status mirror: project the store's thread + active turn into
//     Session.status, so `kubectl get sessions.ai` tells the truth without
//     touching Postgres.
//  2. Purge finalizer: when the Session CR is deleted, remove the thread and
//     its transcript from the store. The Session is ownerRef'd to its
//     Project, so deleting a Project garbage-collects its conversations.
//  3. Retention: a Session whose conversation has been idle for longer than
//     the configured retention is deleted, which runs (2). The reconciler
//     requeues at status.lastActivityAt + retention — a computed deadline, not
//     a sweep interval — so a conversation ages out on its own clock.
//
// Retention used to be a process-wide ticker deleting every message older
// than one cutoff (main.go runRetention). That sweep could not see a thread
// still being read, ran on every replica at once, and its granularity was the
// message rather than the conversation. The deadline here replaces it: one
// owner (the controller leader), one object, and the purge is the Session's
// own finalizer rather than a bulk DELETE behind the store's back.
//
// The store is authoritative for conversation data; the CR is its
// projection. The identity annotations bridge the two keyspaces (the store
// is keyed by org/workspace UUIDs; the reconciler only knows the cluster).
//
// Store changes are invisible to the watch, so the assistant layer signals
// them (package reconcilesignal): every thread or turn transition publishes
// the Session's (cluster, name) — the Session is named after its thread —
// and the reconciler re-projects on arrival. A slow safety resync covers a
// signal published before the controller subscribed.
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

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/bindings"
	"github.com/railgrid/provider-app-studio/internal/reconcilesignal"
	"github.com/railgrid/provider-app-studio/store"
)

// Reconciler projects store threads into Session CRs and purges the store
// when a Session is deleted.
type Reconciler struct {
	Manager mcmanager.Manager
	Store   store.Store
	// Signals carries thread/turn transitions from the assistant layer as
	// (cluster, session name) keys. Nil means no signals.
	Signals *reconcilesignal.Bus
	// Retention is how long a conversation is kept after its last activity.
	// Zero disables retention entirely and the reconciler never deletes a
	// Session of its own accord.
	Retention time.Duration
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	b := mcbuilder.ControllerManagedBy(mgr).
		Named("app-studio-session").
		For(&aiv1alpha1.Session{})
	if r.Signals != nil {
		b = b.WatchesRawSource(r.Signals.Source())
	}
	return b.Complete(r)
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

	// Retention is a deadline on this conversation, evaluated against the
	// status just written. An expired Session is deleted; the finalizer above
	// does the store purge, so there is exactly one path that removes a
	// conversation.
	expiry, ok := r.retentionDeadline(next)
	if !ok {
		return ctrl.Result{}, nil
	}
	if remaining := time.Until(expiry); remaining > 0 {
		return ctrl.Result{RequeueAfter: remaining}, nil
	}
	if err := c.Delete(ctx, &s); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// retentionDeadline is when this conversation may be purged. An in-flight
// turn defers it: a conversation being worked on is active however old its
// last recorded activity is.
func (r *Reconciler) retentionDeadline(status aiv1alpha1.SessionStatus) (time.Time, bool) {
	if r.Retention <= 0 || status.LastActivityAt == nil || status.LastActivityAt.IsZero() {
		return time.Time{}, false
	}
	if strings.TrimSpace(status.ActiveTurnID) != "" &&
		status.ActiveTurnStatus == string(store.AssistantTurnStatusInProgress) {
		return time.Time{}, false
	}
	return status.LastActivityAt.Add(r.Retention), true
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
	// turnCount/lastActivityAt come from the store's own summary. A store
	// that cannot answer leaves them unset, which also leaves this Session
	// outside retention — better than guessing an age and purging early.
	if reader, ok := r.Store.(store.AssistantThreadActivityReader); ok {
		activity, err := reader.AssistantThreadActivity(ctx, scope, threadID)
		switch {
		case errors.Is(err, store.ErrAssistantThreadNotFound):
			return aiv1alpha1.SessionStatus{}, false, nil
		case err != nil:
			return aiv1alpha1.SessionStatus{}, true, err
		}
		next.TurnCount = int32(activity.TurnCount)
		if !activity.LastActivityAt.IsZero() {
			last := metav1.NewTime(activity.LastActivityAt)
			next.LastActivityAt = &last
		}
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
const projectUIDAnnotation = "ai.railgrid.ai/project-uid"

// statusEqual compares mirrored status.
func statusEqual(a, b aiv1alpha1.SessionStatus) bool {
	if a.Title != b.Title || a.Phase != b.Phase ||
		a.ActiveTurnID != b.ActiveTurnID || a.ActiveTurnStatus != b.ActiveTurnStatus {
		return false
	}
	if a.TurnCount != b.TurnCount {
		return false
	}
	return sameTime(a.UpdatedAt, b.UpdatedAt) && sameTime(a.LastActivityAt, b.LastActivityAt)
}

func sameTime(a, b *metav1.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Equal(b)
	}
}
