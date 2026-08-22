/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package template reconciles infrastructure.faros.sh/v1alpha1 Template CRs.
// Each Template represents one catalog entry; the controller's job is to
// (a) validate the Template's contract — the values schema (including the
// platform-reserved field injection) and the development block — and
// (b) hand the Template to the named backend for backend-specific setup
// (kro: author an RGD on the runtime cluster; stub: no-op).
//
// Templates no longer project per-template CRDs or APIResourceSchemas into
// kcp: tenants author the single flattened Instance kind
// (instances.infrastructure.faros.sh, installed at init like templates),
// and the instance controller validates spec.values against the Template's
// schema at reconcile time. Adding or changing a Template therefore never
// touches the APIExport's resource list.
package template

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/backend"
	"github.com/faroshq/provider-infrastructure/instancespec"
)

// Reconciler reconciles Template objects.
type Reconciler struct {
	// Client reads Templates and writes their status. Comes from the
	// controller-runtime manager.
	Client client.Client

	// Backends is the registry main.go populated at startup. The
	// reconciler dispatches SetupTemplate / TeardownTemplate through
	// it; an unknown backend name surfaces as a status condition,
	// not a crash.
	Backends *backend.Registry
	// CodingSandboxEnabled gates the platform-owned universal coding sandbox
	// Template. It defaults false so a manually submitted copy cannot bypass
	// the bootstrap seed gate.
	CodingSandboxEnabled bool
}

// SetupWithManager wires the reconciler into a controller-runtime
// Manager. Watches Template CRs in the workspace the manager is
// configured against (the provider's own workspace at startup).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := infrav1alpha1.AddToScheme(mgr.GetScheme()); err != nil {
		return fmt.Errorf("template controller: adding scheme: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("template").
		For(&infrav1alpha1.Template{}, builder.WithPredicates()).
		Complete(r)
}

// Reconcile drives the Template through validation and backend setup;
// the aggregate Ready condition flips True once the backend accepted it.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("template", req.Name)

	var tmpl infrav1alpha1.Template
	if err := r.Client.Get(ctx, req.NamespacedName, &tmpl); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get template: %w", err)
	}

	// Snapshot for the status patch base.
	patchBase := tmpl.DeepCopy()

	if !tmpl.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &tmpl, patchBase)
	}

	if controllerutil.AddFinalizer(&tmpl, infrav1alpha1.FinalizerTemplateReconcile) {
		if err := r.Client.Update(ctx, &tmpl); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		// Update returns a fresh ResourceVersion; let the next event
		// loop drive us forward rather than racing the update here.
		return ctrl.Result{Requeue: true}, nil
	}
	if tmpl.Name == infrav1alpha1.UniversalCodingSandboxTemplateName && !r.CodingSandboxEnabled {
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonCodingSandboxDisabled,
			"the universal coding sandbox is disabled by provider configuration")
		return r.writeStatus(ctx, &tmpl, patchBase)
	}
	if err := infrav1alpha1.ValidateUniversalCodingSandboxTemplate(&tmpl); err != nil {
		setCondition(&tmpl, infrav1alpha1.ConditionSchemaValid, metav1.ConditionFalse,
			infrav1alpha1.ReasonInvalidSpec, err.Error())
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonInvalidSpec, err.Error())
		return r.writeStatus(ctx, &tmpl, patchBase)
	}

	// Retired platform templates are deleted on sight (see retired.go).
	// AFTER the finalizer add so the deletion runs the full finalize chain,
	// BEFORE the backend lookup so a retired template whose backend is no
	// longer registered still gets swept instead of parking on a
	// BackendNotFound condition.
	if reason, retired := retiredTemplates[tmpl.Name]; retired {
		logger.Info("deleting retired platform template", "reason", reason)
		if err := r.Client.Delete(ctx, &tmpl); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete retired template: %w", err)
		}
		return ctrl.Result{}, nil // the deletion event re-enters via finalize
	}

	// Look up backend FIRST so a typo on spec.backend never results in a
	// Ready template without a corresponding handler.
	b, ok := r.Backends.Get(tmpl.Spec.Backend)
	if !ok {
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonBackendNotFound,
			fmt.Sprintf("backend %q is not registered on this process; registered=%v",
				tmpl.Spec.Backend, r.Backends.Names()))
		return r.writeStatus(ctx, &tmpl, patchBase)
	}

	// Structural rules kubebuilder markers can't express (development
	// component names/paths, data-plane component cross-refs). Invalid specs
	// don't fix themselves on retry, so surface the condition and stop.
	if err := tmpl.Spec.ValidateDevelopment(); err != nil {
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonInvalidSpec, err.Error())
		return r.writeStatus(ctx, &tmpl, patchBase)
	}

	// The values contract: spec.schema must parse, compile to a structural
	// schema, and not claim platform-reserved properties. This is exactly
	// what the instance controller will hold Instances to, so a Template
	// failing here would strand every instance — refuse it up front.
	if _, err := instancespec.NewContract(&tmpl); err != nil {
		setCondition(&tmpl, infrav1alpha1.ConditionSchemaValid, metav1.ConditionFalse,
			infrav1alpha1.ReasonInvalidSpec, err.Error())
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonInvalidSpec, err.Error())
		return r.writeStatus(ctx, &tmpl, patchBase)
	}
	setCondition(&tmpl, infrav1alpha1.ConditionSchemaValid, metav1.ConditionTrue,
		infrav1alpha1.ReasonReady, "")

	// Backend handoff.
	bs, berr := b.SetupTemplate(ctx, &tmpl)
	tmpl.Status.Backend = infrav1alpha1.TemplateBackendStatus{
		Name:    b.Name(),
		Ready:   bs.Ready,
		Message: bs.Message,
	}
	if berr != nil {
		setCondition(&tmpl, infrav1alpha1.ConditionBackendReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonBackendError, berr.Error())
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonBackendError, berr.Error())
		_, _ = r.writeStatus(ctx, &tmpl, patchBase)
		return ctrl.Result{}, fmt.Errorf("backend %q SetupTemplate: %w", b.Name(), berr)
	}
	if !bs.Ready {
		setCondition(&tmpl, infrav1alpha1.ConditionBackendReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonBackendError, bs.Message)
		setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
			infrav1alpha1.ReasonBackendError, bs.Message)
		return r.writeStatus(ctx, &tmpl, patchBase)
	}
	setCondition(&tmpl, infrav1alpha1.ConditionBackendReady, metav1.ConditionTrue,
		infrav1alpha1.ReasonReady, "")

	// All sub-conditions True → Ready=True.
	setCondition(&tmpl, infrav1alpha1.ConditionReady, metav1.ConditionTrue,
		infrav1alpha1.ReasonReady, "")
	tmpl.Status.ObservedGeneration = tmpl.Generation
	logger.V(1).Info("template ready", "backend", b.Name())
	return r.writeStatus(ctx, &tmpl, patchBase)
}

// finalize runs the cleanup chain: backend teardown, finalizer drop. Each
// step is idempotent so a partial-finalize crash recovers on the next
// reconcile.
func (r *Reconciler) finalize(ctx context.Context, tmpl, patchBase *infrav1alpha1.Template) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("template", tmpl.Name, "phase", "finalize")

	if controllerutil.ContainsFinalizer(tmpl, infrav1alpha1.FinalizerTemplateReconcile) {
		if b, ok := r.Backends.Get(tmpl.Spec.Backend); ok {
			if err := b.TeardownTemplate(ctx, tmpl); err != nil {
				setCondition(tmpl, infrav1alpha1.ConditionReady, metav1.ConditionFalse,
					infrav1alpha1.ReasonBackendError, "teardown: "+err.Error())
				_, _ = r.writeStatus(ctx, tmpl, patchBase)
				return ctrl.Result{}, err
			}
		}
		controllerutil.RemoveFinalizer(tmpl, infrav1alpha1.FinalizerTemplateReconcile)
		if err := r.Client.Update(ctx, tmpl); err != nil {
			return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
		}
	}
	logger.V(1).Info("template finalized")
	return ctrl.Result{}, nil
}

// writeStatus persists status conditions. Uses a JSON-merge patch so
// concurrent Template spec updates don't race the status write.
func (r *Reconciler) writeStatus(ctx context.Context, tmpl, patchBase *infrav1alpha1.Template) (ctrl.Result, error) {
	patch := client.MergeFrom(patchBase)
	if err := r.Client.Status().Patch(ctx, tmpl, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch status: %w", err)
	}
	return ctrl.Result{}, nil
}

// setCondition is a small wrapper that sets a Condition with
// LastTransitionTime defaulted and ObservedGeneration tracked. Keeps
// the Reconcile body readable without a helper from k8s.io/utils.
func setCondition(tmpl *infrav1alpha1.Template, condType string, status metav1.ConditionStatus, reason, message string) {
	conds := tmpl.Status.Conditions
	now := metav1.Now()
	for i := range conds {
		if conds[i].Type == condType {
			if conds[i].Status != status || conds[i].Reason != reason || conds[i].Message != message {
				conds[i].Status = status
				conds[i].Reason = reason
				conds[i].Message = message
				conds[i].LastTransitionTime = now
				conds[i].ObservedGeneration = tmpl.Generation
			}
			tmpl.Status.Conditions = conds
			return
		}
	}
	tmpl.Status.Conditions = append(conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: tmpl.Generation,
	})
}
