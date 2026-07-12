/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package repositorybuildstatus reconciles RepositoryBuildStatus requests by
// inspecting (or re-running) a Repository's CI build workflow through the git
// host's Actions API — the credentialed read/dispatch the tenant-facing MCP
// layer cannot do itself. The consumer (the build-status MCP tool) reads the
// result from status and deletes the request.
package repositorybuildstatus

import (
	"context"
	"fmt"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
	"github.com/faroshq/provider-code/controller/shared"
)

// Reconciler inspects/re-runs build workflows via the git host.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
}

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-repositorybuildstatus").
		For(&codev1alpha1.RepositoryBuildStatus{}).
		Complete(r)
}

// Reconcile performs the request once and records the terminal result.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("repositorybuildstatus", req.Name, "cluster", req.ClusterName)

	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var bs codev1alpha1.RepositoryBuildStatus
	if err := c.Get(ctx, req.NamespacedName, &bs); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !bs.DeletionTimestamp.IsZero() || isTerminal(bs.Status.Phase) {
		return ctrl.Result{}, nil
	}
	fail := func(message string) error { return r.fail(ctx, c, &bs, message) }
	if r.Backends == nil {
		return ctrl.Result{}, fail("git backends are unavailable")
	}

	bs.Status.Phase = codev1alpha1.RepositoryBuildStatusPhaseRunning
	bs.Status.ObservedGeneration = bs.Generation
	shared.SetCondition(&bs.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, "Build status request is running.", bs.Generation)
	if err := updateStatusIfChanged(ctx, c, &bs); err != nil {
		return ctrl.Result{}, err
	}

	repo, err := shared.ResolveRepository(ctx, c, bs.Spec.RepositoryRef)
	if err != nil {
		return ctrl.Result{}, fail(err.Error())
	}
	conn, err := shared.ResolveConnection(ctx, c, repo.Spec.ConnectionRef)
	if err != nil {
		return ctrl.Result{}, fail(err.Error())
	}
	gitBackend, ok := r.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		return ctrl.Result{}, fail(fmt.Sprintf("git provider %q is not registered", conn.Spec.Provider))
	}
	cred, err := shared.ResolveCredential(ctx, c, conn)
	if err != nil {
		return ctrl.Result{}, fail(err.Error())
	}

	next := bs.DeepCopy()
	if bs.Spec.Action == codev1alpha1.RepositoryBuildStatusActionRerun {
		dispatcher, ok := gitBackend.(backend.WorkflowDispatcher)
		if !ok {
			return ctrl.Result{}, fail(fmt.Sprintf("git provider %q does not support re-running workflows", conn.Spec.Provider))
		}
		if err := dispatcher.DispatchWorkflow(ctx, conn, cred, repo, bs.Spec.WorkflowFileName, bs.Spec.Ref); err != nil {
			return ctrl.Result{}, fail(err.Error())
		}
		next.Status.Dispatched = true
	} else {
		reader, ok := gitBackend.(backend.WorkflowRunReader)
		if !ok {
			return ctrl.Result{}, fail(fmt.Sprintf("git provider %q does not support reading workflow runs", conn.Spec.Provider))
		}
		run, err := reader.LatestWorkflowRun(ctx, conn, cred, repo, backend.WorkflowRunQuery{
			WorkflowFileName: bs.Spec.WorkflowFileName,
			HeadSHA:          bs.Spec.Ref,
			MaxLogLines:      bs.Spec.MaxLogLines,
		})
		if err != nil {
			return ctrl.Result{}, fail(err.Error())
		}
		next.Status.Run = workflowRunStatus(run)
	}

	completed := metav1.Now()
	next.Status.Phase = codev1alpha1.RepositoryBuildStatusPhaseSucceeded
	next.Status.ObservedGeneration = bs.Generation
	next.Status.CompletedAt = &completed
	shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionTrue, codev1alpha1.ReasonReady, "Build status request succeeded.", bs.Generation)
	if err := updateStatusIfChanged(ctx, c, next); err != nil {
		return ctrl.Result{}, fmt.Errorf("update repositorybuildstatus %q status: %w", bs.Name, err)
	}
	logger.V(3).Info("repository build status succeeded", "repository", bs.Spec.RepositoryRef, "action", bs.Spec.Action)
	return ctrl.Result{}, nil
}

func workflowRunStatus(run backend.WorkflowRunStatus) *codev1alpha1.RepositoryBuildStatusRun {
	out := &codev1alpha1.RepositoryBuildStatusRun{
		Found:      run.Found,
		RunID:      run.RunID,
		HTMLURL:    run.HTMLURL,
		HeadSHA:    run.HeadSHA,
		Status:     run.Status,
		Conclusion: run.Conclusion,
	}
	for _, j := range run.Jobs {
		out.Jobs = append(out.Jobs, codev1alpha1.RepositoryBuildStatusJob{
			Name:       j.Name,
			Status:     j.Status,
			Conclusion: j.Conclusion,
			FailureLog: j.FailureLog,
		})
	}
	return out
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, bs *codev1alpha1.RepositoryBuildStatus, message string) error {
	next := bs.DeepCopy()
	now := metav1.Now()
	next.Status.CompletedAt = &now
	next.Status.ObservedGeneration = bs.Generation
	next.Status.Phase = codev1alpha1.RepositoryBuildStatusPhaseFailed
	shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonError, message, bs.Generation)
	if err := updateStatusIfChanged(ctx, c, next); err != nil {
		return fmt.Errorf("update repositorybuildstatus %q failure status: %w", bs.Name, err)
	}
	return nil
}

func updateStatusIfChanged(ctx context.Context, c client.Client, bs *codev1alpha1.RepositoryBuildStatus) error {
	current := &codev1alpha1.RepositoryBuildStatus{}
	if err := c.Get(ctx, client.ObjectKey{Name: bs.Name}, current); err != nil {
		return err
	}
	if reflect.DeepEqual(current.Status, bs.Status) {
		return nil
	}
	current.Status = bs.Status
	if err := c.Status().Update(ctx, current); err != nil {
		return fmt.Errorf("update repositorybuildstatus %q status: %w", bs.Name, err)
	}
	return nil
}

func isTerminal(phase codev1alpha1.RepositoryBuildStatusPhase) bool {
	return phase == codev1alpha1.RepositoryBuildStatusPhaseSucceeded || phase == codev1alpha1.RepositoryBuildStatusPhaseFailed
}
