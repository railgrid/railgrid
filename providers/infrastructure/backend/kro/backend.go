/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package kro implements the backend.Backend that turns a Template into a
// kro ResourceGraphDefinition on the runtime cluster.
//
// The Template is the source of truth (it lives in the provider's kcp
// workspace; the Template controller publishes its CRD/APIResourceSchema so
// tenants can create instances). This backend derives the matching RGD from
// the same Template and writes it to the kro runtime cluster — the cluster
// kro's controller-runtime manager watches RGDs on (a kind cluster in dev),
// NOT kcp. Once the RGD exists, kro registers the dynamic watch and
// reconciles instances; instance workloads land on the runtime cluster while
// the instance object + status stay in the tenant's kcp workspace (see the
// kro fork's --deploy-to-local-runtime split).
//
// This backend does NOT reconcile instances itself — the kro controller does
// that. Run() therefore just blocks: the RGD authoring happens in
// SetupTemplate/TeardownTemplate, driven by the Template controller.
package kro

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	infrav1alpha1 "github.com/faroshq/faros-kedge/providers/infrastructure/apis/v1alpha1"
	"github.com/faroshq/faros-kedge/providers/infrastructure/backend"
)

// Name is the backend identifier operators put in Template.spec.backend.
const Name = "kro"

// Backend authors kro ResourceGraphDefinitions on the runtime cluster from
// Templates. It implements backend.Backend.
type Backend struct {
	// runtime is a dynamic client scoped to the kro runtime cluster (where
	// the kro controller watches RGDs). In dev this is the kind cluster
	// pointed at by KRO_KUBECONFIG.
	runtime dynamic.Interface
}

var _ backend.Backend = (*Backend)(nil)

// New constructs the kro backend against the runtime cluster's dynamic
// client. The caller (controller_manager) builds it from KRO_KUBECONFIG.
func New(runtime dynamic.Interface) *Backend {
	return &Backend{runtime: runtime}
}

// Name returns "kro".
func (b *Backend) Name() string { return Name }

// SetupTemplate derives the RGD from the Template and applies it to the
// runtime cluster. Idempotent: re-applies on every reconcile pass. A build
// error (malformed schema/backendConfig) is returned so the Template
// controller surfaces BackendError; a successful apply reports Ready=true.
func (b *Backend) SetupTemplate(ctx context.Context, tmpl *infrav1alpha1.Template) (backend.TemplateStatus, error) {
	rgd, err := buildRGD(tmpl)
	if err != nil {
		return backend.TemplateStatus{Ready: false, Message: err.Error()}, err
	}
	if err := b.applyRGD(ctx, rgd); err != nil {
		return backend.TemplateStatus{Ready: false, Message: "applying RGD: " + err.Error()}, err
	}
	klog.FromContext(ctx).WithName("backend.kro").Info("applied ResourceGraphDefinition to runtime cluster",
		"template", tmpl.Name, "rgd", tmpl.Name)
	return backend.TemplateStatus{Ready: true, Message: "RGD applied to runtime cluster"}, nil
}

// TeardownTemplate removes the Template's RGD from the runtime cluster. kro
// then garbage-collects the generated CRD + stops the dynamic watch. 404 is
// success (already gone). Idempotent.
func (b *Backend) TeardownTemplate(ctx context.Context, tmpl *infrav1alpha1.Template) error {
	err := b.runtime.Resource(rgdGVR).Delete(ctx, tmpl.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting RGD %q: %w", tmpl.Name, err)
	}
	return nil
}

// Run blocks until ctx is cancelled. Instance reconciliation is owned by the
// kro controller (which watches the RGDs this backend writes), so there is
// no per-process loop to run here. vwConfig is unused for the same reason.
func (b *Backend) Run(ctx context.Context, _ *rest.Config) error {
	klog.FromContext(ctx).WithName("backend.kro").Info("kro backend ready (RGDs authored on the runtime cluster; reconciliation handled by the kro controller)")
	<-ctx.Done()
	return nil
}

// applyRGD creates or updates the RGD on the runtime cluster, preserving the
// server-assigned resourceVersion on update so it's a compare-and-set.
func (b *Backend) applyRGD(ctx context.Context, rgd *unstructured.Unstructured) error {
	existing, err := b.runtime.Resource(rgdGVR).Get(ctx, rgd.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := b.runtime.Resource(rgdGVR).Create(ctx, rgd, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	rgd.SetResourceVersion(existing.GetResourceVersion())
	if _, err := b.runtime.Resource(rgdGVR).Update(ctx, rgd, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return nil
}
