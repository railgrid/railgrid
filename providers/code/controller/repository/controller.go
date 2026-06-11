/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package repository reconciles Repository CRs: it ensures the repository
// exists on the git host (creating it via the backend) and, on delete, removes
// it before releasing the finalizer.
package repository

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

// Reconciler ensures Repository CRs against the git host.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
}

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-repository").
		For(&codev1alpha1.Repository{}).
		Complete(r)
}

// Reconcile ensures one Repository.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("repository", req.Name, "cluster", req.ClusterName)

	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var repo codev1alpha1.Repository
	if err := c.Get(ctx, req.NamespacedName, &repo); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	b, conn, cred, notReady := r.resolve(ctx, c, &repo)
	ready := notReady == ""

	// Deletion path.
	if !repo.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&repo, codev1alpha1.FinalizerRepository) {
			// Best-effort host delete only when we could resolve everything;
			// if the Connection/credential is already gone we still release.
			if ready {
				if err := b.DeleteRepository(ctx, conn, cred, &repo); err != nil {
					return r.fail(ctx, c, &repo, "DeleteFailed", err.Error())
				}
			}
			controllerutil.RemoveFinalizer(&repo, codev1alpha1.FinalizerRepository)
			if err := c.Update(ctx, &repo); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&repo, codev1alpha1.FinalizerRepository) {
		if err := c.Update(ctx, &repo); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if !ready {
		// notReady names exactly which piece is missing (wrong connectionRef,
		// unknown provider, or unreadable credential) so the status is actionable.
		return r.fail(ctx, c, &repo, "ConnectionNotReady", notReady)
	}

	res, err := b.EnsureRepository(ctx, conn, cred, &repo)
	if err != nil {
		return r.fail(ctx, c, &repo, "EnsureFailed", err.Error())
	}

	repo.Status.ObservedGeneration = repo.Generation
	repo.Status.RepoID = res.RepoID
	repo.Status.HTMLURL = res.HTMLURL
	repo.Status.CloneURL = res.CloneURL
	repo.Status.SSHURL = res.SSHURL
	shared.SetCondition(&repo.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionTrue, codev1alpha1.ReasonReady, "", repo.Generation)
	if err := c.Status().Update(ctx, &repo); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("Repository ensured", "url", res.HTMLURL)
	return ctrl.Result{}, nil
}

// resolve loads the backend, Connection, and credential for repo. The returned
// string is empty on success, otherwise a specific human-readable reason naming
// the missing piece (used for the NotReady status; the caller decides whether
// that's fatal for create or tolerable for delete).
func (r *Reconciler) resolve(ctx context.Context, c client.Client, repo *codev1alpha1.Repository) (backend.GitBackend, *codev1alpha1.Connection, backend.Credential, string) {
	conn, err := shared.ResolveConnection(ctx, c, repo.Spec.ConnectionRef)
	if err != nil {
		return nil, nil, backend.Credential{}, err.Error()
	}
	b, ok := r.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		return nil, conn, backend.Credential{}, fmt.Sprintf("no backend registered for provider %q (connection %q)", conn.Spec.Provider, conn.Name)
	}
	cred, err := shared.ResolveCredential(ctx, c, conn)
	if err != nil {
		return b, conn, backend.Credential{}, fmt.Sprintf("credential for connection %q unavailable: %v", conn.Name, err)
	}
	return b, conn, cred, ""
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, repo *codev1alpha1.Repository, reason, msg string) (ctrl.Result, error) {
	repo.Status.ObservedGeneration = repo.Generation
	shared.SetCondition(&repo.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, reason, msg, repo.Generation)
	if err := c.Status().Update(ctx, repo); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, fmt.Errorf("%s: %s", reason, msg)
}
