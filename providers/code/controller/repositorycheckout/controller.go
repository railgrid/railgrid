/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package repositorycheckout reconciles RepositoryCheckout requests by reading
// a Repository's text tree through the git backend into a provider-owned
// source bundle — the RepositoryCommit flow in reverse. The consumer (the
// checkout MCP tool) reads the bundle and deletes it; contents never land in
// the CR.
package repositorycheckout

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

// Reconciler reads RepositoryCheckout requests from the git host into bundles.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
	Bundles  commitbundle.Store
}

// Checkout bounds, passed explicitly so every backend behaves consistently
// (never backend defaults). They mirror the App Studio workspace limits the
// checked-out tree ultimately lands in: 500 files, 256 KiB per file, 16 MiB
// total.
const (
	checkoutMaxFiles      = 500
	checkoutMaxFileBytes  = 256 << 10
	checkoutMaxTotalBytes = 16 << 20
)

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-repositorycheckouts").
		For(&codev1alpha1.RepositoryCheckout{}).
		Complete(r)
}

// Reconcile performs the checkout once and records the terminal result.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("repositorycheckout", req.Name, "cluster", req.ClusterName)

	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var checkout codev1alpha1.RepositoryCheckout
	if err := c.Get(ctx, req.NamespacedName, &checkout); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !checkout.DeletionTimestamp.IsZero() || isTerminal(checkout.Status.Phase) {
		return ctrl.Result{}, nil
	}
	fail := func(message string) error { return r.fail(ctx, c, &checkout, message) }
	if r.Bundles == nil {
		return ctrl.Result{}, fail("bundle store is unavailable")
	}
	if r.Backends == nil {
		return ctrl.Result{}, fail("git backends are unavailable")
	}

	now := metav1.Now()
	if checkout.Status.StartedAt == nil {
		checkout.Status.StartedAt = &now
	}
	checkout.Status.Phase = codev1alpha1.RepositoryCheckoutPhaseRunning
	checkout.Status.ObservedGeneration = checkout.Generation
	shared.SetCondition(&checkout.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, "Checkout is running.", checkout.Generation)
	if err := updateStatusIfChanged(ctx, c, &checkout); err != nil {
		return ctrl.Result{}, err
	}

	repo, err := shared.ResolveRepository(ctx, c, checkout.Spec.RepositoryRef)
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
	reader, ok := gitBackend.(backend.RepositoryReader)
	if !ok {
		return ctrl.Result{}, fail(fmt.Sprintf("git provider %q does not support reading files", conn.Spec.Provider))
	}
	cred, err := shared.ResolveCredential(ctx, c, conn)
	if err != nil {
		return ctrl.Result{}, fail(err.Error())
	}

	res, err := reader.CheckoutFiles(ctx, conn, cred, repo, backend.RepositoryCheckoutInput{
		Ref:           checkout.Spec.Ref,
		MaxFiles:      checkoutMaxFiles,
		MaxFileBytes:  checkoutMaxFileBytes,
		MaxTotalBytes: checkoutMaxTotalBytes,
	})
	if err != nil {
		return ctrl.Result{}, fail(err.Error())
	}

	// Bundle scope = this CR's cluster, same convention the commit flow reads
	// bundles under; the checkout MCP tool reads it there and deletes it.
	bundleScope := string(req.ClusterName)
	files := make([]commitbundle.File, 0, len(res.Files))
	for _, f := range res.Files {
		files = append(files, commitbundle.File{Path: f.Path, Content: f.Content})
	}
	bundle, err := r.Bundles.Put(ctx, bundleScope, files)
	if err != nil {
		return ctrl.Result{}, fail(fmt.Sprintf("store checkout bundle: %v", err))
	}

	completed := metav1.Now()
	next := checkout.DeepCopy()
	next.Status.Phase = codev1alpha1.RepositoryCheckoutPhaseSucceeded
	next.Status.ObservedGeneration = checkout.Generation
	next.Status.CompletedAt = &completed
	next.Status.Ref = res.Ref
	next.Status.CommitSHA = res.CommitSHA
	next.Status.BundleRef = &codev1alpha1.RepositoryCommitBundleReference{
		Name:   bundle.Name,
		Digest: bundle.Digest,
	}
	next.Status.Source = &codev1alpha1.RepositoryCommitSourceStatus{
		Digest:    bundle.Digest,
		Size:      bundle.Size,
		FileCount: len(bundle.Files),
	}
	next.Status.Skipped = res.Skipped
	shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionTrue, codev1alpha1.ReasonReady, "Checkout succeeded.", checkout.Generation)
	if err := updateStatusIfChanged(ctx, c, next); err != nil {
		// The consumer will never see the bundle reference; reclaim it.
		_ = r.Bundles.Delete(ctx, bundleScope, bundle.Name, bundle.Digest)
		return ctrl.Result{}, fmt.Errorf("update repositorycheckout %q status: %w", checkout.Name, err)
	}
	logger.V(3).Info("repository checkout succeeded", "repository", checkout.Spec.RepositoryRef, "commitSHA", res.CommitSHA, "files", len(bundle.Files))
	return ctrl.Result{}, nil
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, checkout *codev1alpha1.RepositoryCheckout, message string) error {
	next := checkout.DeepCopy()
	now := metav1.Now()
	if next.Status.StartedAt == nil {
		next.Status.StartedAt = &now
	}
	next.Status.CompletedAt = &now
	next.Status.ObservedGeneration = checkout.Generation
	next.Status.Phase = codev1alpha1.RepositoryCheckoutPhaseFailed
	shared.SetCondition(&next.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonError, message, checkout.Generation)
	if err := updateStatusIfChanged(ctx, c, next); err != nil {
		return fmt.Errorf("update repositorycheckout %q failure status: %w", checkout.Name, err)
	}
	return nil
}

func updateStatusIfChanged(ctx context.Context, c client.Client, checkout *codev1alpha1.RepositoryCheckout) error {
	current := &codev1alpha1.RepositoryCheckout{}
	if err := c.Get(ctx, client.ObjectKey{Name: checkout.Name}, current); err != nil {
		return err
	}
	if reflect.DeepEqual(current.Status, checkout.Status) {
		return nil
	}
	current.Status = checkout.Status
	if err := c.Status().Update(ctx, current); err != nil {
		return fmt.Errorf("update repositorycheckout %q status: %w", checkout.Name, err)
	}
	return nil
}

func isTerminal(phase codev1alpha1.RepositoryCheckoutPhase) bool {
	return phase == codev1alpha1.RepositoryCheckoutPhaseSucceeded || phase == codev1alpha1.RepositoryCheckoutPhaseFailed
}
