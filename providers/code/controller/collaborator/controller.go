/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package collaborator reconciles Collaborator CRs: it grants the user the
// requested permission on the referenced Repository via the git backend and, on
// delete, revokes it (cancelling any pending invitation). The host's
// invitation-pending state is surfaced on the InvitationPending condition.
package collaborator

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
	"github.com/faroshq/provider-code/controller/shared"
)

// Reconciler manages Collaborator CRs.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
}

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-collaborator").
		For(&codev1alpha1.Collaborator{}).
		Complete(r)
}

// Reconcile ensures one Collaborator.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("collaborator", req.Name, "cluster", req.ClusterName)

	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var collab codev1alpha1.Collaborator
	if err := c.Get(ctx, req.NamespacedName, &collab); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	b, conn, repo, cred, ready := r.resolve(ctx, c, &collab)

	// Deletion path.
	if !collab.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&collab, codev1alpha1.FinalizerCollaborator) {
			if ready {
				if err := b.RemoveCollaborator(ctx, conn, cred, repo, &collab); err != nil {
					return r.fail(ctx, c, &collab, "RemoveFailed", err.Error())
				}
			}
			controllerutil.RemoveFinalizer(&collab, codev1alpha1.FinalizerCollaborator)
			if err := c.Update(ctx, &collab); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&collab, codev1alpha1.FinalizerCollaborator) {
		if err := c.Update(ctx, &collab); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if !ready {
		return r.fail(ctx, c, &collab, "RepositoryNotReady", "referenced repository, connection, or credential is not available yet")
	}

	res, err := b.EnsureCollaborator(ctx, conn, cred, repo, &collab)
	if err != nil {
		return r.fail(ctx, c, &collab, "EnsureFailed", err.Error())
	}

	collab.Status.ObservedGeneration = collab.Generation
	collab.Status.InvitationID = res.InvitationID
	if res.Pending {
		shared.SetCondition(&collab.Status.Conditions, codev1alpha1.ConditionInvitationPending, metav1.ConditionTrue, "Invited", "invitation sent; awaiting acceptance", collab.Generation)
		// Ready=true: the grant is correctly applied from our side; acceptance
		// is the invitee's action, tracked separately by InvitationPending.
		shared.SetCondition(&collab.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionTrue, codev1alpha1.ReasonReady, "invitation pending acceptance", collab.Generation)
	} else {
		shared.SetCondition(&collab.Status.Conditions, codev1alpha1.ConditionInvitationPending, metav1.ConditionFalse, codev1alpha1.ReasonReady, "", collab.Generation)
		shared.SetCondition(&collab.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionTrue, codev1alpha1.ReasonReady, "", collab.Generation)
	}
	if err := c.Status().Update(ctx, &collab); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("Collaborator ensured", "user", collab.Spec.Username, "pending", res.Pending)
	return ctrl.Result{}, nil
}

func (r *Reconciler) resolve(ctx context.Context, c client.Client, collab *codev1alpha1.Collaborator) (backend.GitBackend, *codev1alpha1.Connection, *codev1alpha1.Repository, backend.Credential, bool) {
	repo, err := shared.ResolveRepository(ctx, c, collab.Spec.RepositoryRef)
	if err != nil {
		return nil, nil, nil, backend.Credential{}, false
	}
	conn, err := shared.ResolveConnection(ctx, c, repo.Spec.ConnectionRef)
	if err != nil {
		return nil, nil, repo, backend.Credential{}, false
	}
	b, ok := r.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		return nil, conn, repo, backend.Credential{}, false
	}
	cred, err := shared.ResolveCredential(ctx, c, conn)
	if err != nil {
		return b, conn, repo, backend.Credential{}, false
	}
	return b, conn, repo, cred, true
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, collab *codev1alpha1.Collaborator, reason, msg string) (ctrl.Result, error) {
	collab.Status.ObservedGeneration = collab.Generation
	shared.SetCondition(&collab.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, reason, msg, collab.Generation)
	if err := c.Status().Update(ctx, collab); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, fmt.Errorf("%s: %s", reason, msg)
}
