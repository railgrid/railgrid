/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

// CachedResource projection. Templates live in the provider workspace
// (root:kedge:providers:infrastructure). For tenants who APIBind to
// the infrastructure APIExport to be able to `kubectl get templates`
// in their OWN workspace, kcp needs a CachedResource here pointing at
// templates.infrastructure.kedge.faros.sh. The kcp cache machinery
// then projects every Template into every tenant workspace that has
// the binding — read-only, no extra controller required on our side.
//
// PR A took care of putting templates.infrastructure.kedge.faros.sh
// into APIExport.spec.resources (via install/apiexport.go). This
// file is the second half: the CachedResource itself.
//
// Lives in install/ because it's a one-shot startup task. Idempotent;
// safe to re-apply on every binary boot.

import (
	"context"
	"encoding/json"
	"fmt"

	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// cachedResourceGVR is what we Get/Create against. CachedResource is
// cluster-scoped in cache.kcp.io/v1alpha1.
var cachedResourceGVR = schema.GroupVersionResource{
	Group:    cachev1alpha1.SchemeGroupVersion.Group,
	Version:  cachev1alpha1.SchemeGroupVersion.Version,
	Resource: "cachedresources",
}

// CachedResourceTemplatesName is the well-known name of the
// CachedResource that publishes Templates to tenants. Hardcoded so
// idempotent re-apply on every startup is a Get→exists shortcut.
const CachedResourceTemplatesName = "publish-templates"

// PlatformCachedResources ensures the CachedResource(s) the platform
// owns exist in the provider workspace. There's one today:
// publish-templates, projecting templates.infrastructure.kedge.faros.sh
// to every APIBound tenant workspace.
//
// Idempotent. Errors mean "binary boot didn't complete" — same
// failure semantics as install.CRDs and install.PlatformSchemaInAPIExport.
func PlatformCachedResources(ctx context.Context, config *rest.Config) error {
	log := klog.FromContext(ctx).WithName("install.cachedresource")
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}

	cr := &cachev1alpha1.CachedResource{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cachev1alpha1.SchemeGroupVersion.String(),
			Kind:       "CachedResource",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: CachedResourceTemplatesName,
		},
		Spec: cachev1alpha1.CachedResourceSpec{
			GroupVersionResource: cachev1alpha1.GroupVersionResource{
				Group:    infrav1alpha1.GroupName,
				Version:  infrav1alpha1.Version,
				Resource: "templates",
			},
			// No label selector — every Template in the provider
			// workspace is part of the global catalog. Per-tenant
			// allowlist gating is a future feature; the design doc
			// leaves it for later.
		},
	}

	obj, err := cachedResourceToUnstructured(cr)
	if err != nil {
		return fmt.Errorf("to unstructured: %w", err)
	}

	existing, err := dyn.Resource(cachedResourceGVR).Get(ctx, cr.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get existing CachedResource: %w", err)
	}
	if apierrors.IsNotFound(err) {
		_, err = dyn.Resource(cachedResourceGVR).Create(ctx, obj, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create CachedResource: %w", err)
		}
		log.Info("CachedResource created", "name", cr.Name, "resource", cr.Spec.Resource)
		return nil
	}

	// Spec drift is the only reason to update; the kcp cache
	// machinery reacts to changes there, and we want the platform
	// to own the desired spec.
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, err = dyn.Resource(cachedResourceGVR).Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update CachedResource: %w", err)
	}
	log.Info("CachedResource reconciled", "name", cr.Name)
	return nil
}

// cachedResourceToUnstructured marshalls a typed CachedResource into
// the map shape the dynamic client expects. Same approach the
// install/apiexport.go helpers use.
func cachedResourceToUnstructured(cr *cachev1alpha1.CachedResource) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(cr)
	if err != nil {
		return nil, err
	}
	out := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &out.Object); err != nil {
		return nil, err
	}
	out.SetAPIVersion(cachev1alpha1.SchemeGroupVersion.String())
	out.SetKind("CachedResource")
	return out, nil
}
