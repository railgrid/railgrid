/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package repositorycommit reconciles RepositoryCommit requests by materializing
// provider-owned source bundles into git commits on the referenced Repository.
package repositorycommit

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
	"github.com/faroshq/provider-code/commitbundle"
	"github.com/faroshq/provider-code/controller/shared"
)

// Reconciler applies RepositoryCommit requests to the git host.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
	Bundles  commitbundle.Store
}

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-repositorycommits").
		For(&codev1alpha1.RepositoryCommit{}).
		Complete(r)
}

// Reconcile commits the referenced bundle once and records the terminal result.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("repositorycommit", req.Name, "cluster", req.ClusterName)

	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var commit codev1alpha1.RepositoryCommit
	if err := c.Get(ctx, req.NamespacedName, &commit); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !commit.DeletionTimestamp.IsZero() || isTerminal(commit.Status.Phase) {
		return ctrl.Result{}, nil
	}
	if r.Bundles == nil {
		return ctrl.Result{}, r.fail(ctx, c, &commit, "bundle store is unavailable")
	}
	if r.Backends == nil {
		return ctrl.Result{}, r.fail(ctx, c, &commit, "git backends are unavailable")
	}

	now := metav1.Now()
	if commit.Status.Phase == "" {
		commit.Status.Phase = codev1alpha1.RepositoryCommitPhasePending
	}
	if commit.Status.StartedAt == nil {
		commit.Status.StartedAt = &now
	}
	commit.Status.Phase = codev1alpha1.RepositoryCommitPhaseRunning
	commit.Status.ObservedGeneration = commit.Generation
	shared.SetCondition(&commit.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, "Commit is running.", commit.Generation)
	if err := updateStatusIfChanged(ctx, c, &commit); err != nil {
		return ctrl.Result{}, err
	}

	repo, err := shared.ResolveRepository(ctx, c, commit.Spec.RepositoryRef)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, c, &commit, err.Error())
	}
	conn, err := shared.ResolveConnection(ctx, c, repo.Spec.ConnectionRef)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, c, &commit, err.Error())
	}
	gitBackend, ok := r.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		return ctrl.Result{}, r.fail(ctx, c, &commit, fmt.Sprintf("git provider %q is not registered", conn.Spec.Provider))
	}
	committer, ok := gitBackend.(backend.RepositoryCommitter)
	if !ok {
		return ctrl.Result{}, r.fail(ctx, c, &commit, fmt.Sprintf("git provider %q does not support committing files", conn.Spec.Provider))
	}
	cred, err := shared.ResolveCredential(ctx, c, conn)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, c, &commit, err.Error())
	}
	if _, err := gitBackend.EnsureRepository(ctx, conn, cred, repo); err != nil {
		return ctrl.Result{}, r.fail(ctx, c, &commit, fmt.Sprintf("ensure repository: %v", err))
	}

	bundleRef := commit.Spec.Source.BundleRef
	bundle, err := r.Bundles.Get(ctx, string(req.ClusterName), bundleRef.Name, bundleRef.Digest)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, c, &commit, err.Error())
	}
	files := make([]backend.RepositoryCommitFile, 0, len(bundle.Files))
	fileStatus := make([]codev1alpha1.RepositoryCommitFileStatus, 0, len(bundle.Files))
	for _, f := range bundle.Files {
		files = append(files, backend.RepositoryCommitFile{Path: f.Path, Content: f.Content})
		fileStatus = append(fileStatus, codev1alpha1.RepositoryCommitFileStatus{
			Path:   f.Path,
			Size:   f.Size,
			Digest: f.Digest,
		})
	}
	res, err := committer.CommitFiles(ctx, conn, cred, repo, backend.RepositoryCommitInput{
		Message:        commit.Spec.Message,
		Branch:         commit.Spec.Branch,
		IdempotencyKey: repositoryCommitIdempotencyKey(&commit),
		Files:          files,
	})
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, c, &commit, err.Error())
	}

	completed := metav1.Now()
	next := commit.DeepCopy()
	next.Status.Phase = codev1alpha1.RepositoryCommitPhaseSucceeded
	next.Status.ObservedGeneration = commit.Generation
	next.Status.CompletedAt = &completed
	next.Status.Branch = res.Branch
	next.Status.CommitSHA = res.CommitSHA
	next.Status.CommitURL = res.CommitURL
	next.Status.Source = &codev1alpha1.RepositoryCommitSourceStatus{
		Digest:    bundle.Digest,
		Size:      bundle.Size,
		FileCount: len(bundle.Files),
	}
	next.Status.Files = fileStatus
	shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionTrue, codev1alpha1.ReasonReady, "Commit succeeded.", commit.Generation)
	if err := c.Status().Update(ctx, next); err != nil {
		return ctrl.Result{}, fmt.Errorf("update repositorycommit %q status: %w", commit.Name, err)
	}
	if err := r.Bundles.Delete(ctx, string(req.ClusterName), bundleRef.Name, bundleRef.Digest); err != nil {
		logger.Error(err, "delete committed source bundle", "bundle", bundleRef.Name)
	}
	logger.V(3).Info("repository commit succeeded", "repository", commit.Spec.RepositoryRef, "commitSHA", res.CommitSHA)
	return ctrl.Result{}, nil
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, commit *codev1alpha1.RepositoryCommit, message string) error {
	next := commit.DeepCopy()
	now := metav1.Now()
	if next.Status.StartedAt == nil {
		next.Status.StartedAt = &now
	}
	next.Status.CompletedAt = &now
	next.Status.ObservedGeneration = commit.Generation
	next.Status.Phase = codev1alpha1.RepositoryCommitPhaseFailed
	shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonError, message, commit.Generation)
	if err := c.Status().Update(ctx, next); err != nil {
		return fmt.Errorf("update repositorycommit %q failure status: %w", commit.Name, err)
	}
	return nil
}

func updateStatusIfChanged(ctx context.Context, c client.Client, commit *codev1alpha1.RepositoryCommit) error {
	current := &codev1alpha1.RepositoryCommit{}
	if err := c.Get(ctx, client.ObjectKey{Name: commit.Name}, current); err != nil {
		return err
	}
	if reflect.DeepEqual(current.Status, commit.Status) {
		return nil
	}
	if err := c.Status().Update(ctx, commit); err != nil {
		return fmt.Errorf("update repositorycommit %q status: %w", commit.Name, err)
	}
	return nil
}

func isTerminal(phase codev1alpha1.RepositoryCommitPhase) bool {
	return phase == codev1alpha1.RepositoryCommitPhaseSucceeded || phase == codev1alpha1.RepositoryCommitPhaseFailed
}

func repositoryCommitIdempotencyKey(commit *codev1alpha1.RepositoryCommit) string {
	if commit == nil {
		return ""
	}
	if commit.UID != "" {
		return string(commit.UID)
	}
	return commit.Name
}
