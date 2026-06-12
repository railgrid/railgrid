/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

// CachedResourceEndpointSlice wiring. The APIExport's `templates`
// resource cannot use storage.crd if we want tenants to see Templates
// as a read-only projection through the cache machinery — for that
// the storage has to be `storage.virtual` pointing at a
// CachedResourceEndpointSlice. kcp's CachedResource reconciler does
// auto-create an EndpointSlice from any CachedResource, but it leaves
// spec.export empty (the field is required for the export ↔ slice
// binding to take effect), so we ensure the slice ourselves with both
// references set.
//
// Once the slice is in place and the CachedResource reports a
// populated status.identityHash, install/apiexport.go can flip the
// templates entry from storage.crd to storage.virtual.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// EndpointSliceTemplatesName is the well-known name of the
// CachedResourceEndpointSlice projecting Templates. Matches
// CachedResourceTemplatesName so the slice's auto-create path (named
// after the source CachedResource) reconciles to the same object.
const EndpointSliceTemplatesName = CachedResourceTemplatesName

// CachedResourceReadyTimeout caps how long init waits for the cache
// machinery to populate the CachedResource's status.identityHash.
// 30s covers cold-start of a fresh kcp; longer would mostly hide
// genuine misconfiguration.
const CachedResourceReadyTimeout = 30 * time.Second

var cachedResourceEndpointSliceGVR = schema.GroupVersionResource{
	Group:    cachev1alpha1.SchemeGroupVersion.Group,
	Version:  cachev1alpha1.SchemeGroupVersion.Version,
	Resource: "cachedresourceendpointslices",
}

// APIExportEndpointSliceName is what we pass to the kro chart's
// multicluster.kcp.apiExportEndpointSlice value. Hardcoded so the
// chart's --set, init's create, and the runtime SA's RBAC all agree
// without a configuration knob the dev has to remember.
const APIExportEndpointSliceName = "infrastructure"

var apiExportEndpointSliceGVR = schema.GroupVersionResource{
	Group:    apisv1alpha1.SchemeGroupVersion.Group,
	Version:  apisv1alpha1.SchemeGroupVersion.Version,
	Resource: "apiexportendpointslices",
}

// PlatformAPIExportEndpointSlice ensures an APIExportEndpointSlice
// exists in the provider workspace pointing at APIExportName. This is
// what the kro-multicluster fork's kcp-apiexport provider reads to
// discover the APIExport's virtual-workspace URL — without it, kro
// has no entry point to the per-template kinds tenants create.
//
// Separate from PlatformCachedResourceEndpointSlices because the two
// EndpointSlice types serve different consumers:
//
//   - CachedResourceEndpointSlice (cache.kcp.io/v1alpha1) — kcp's
//     cache machinery; projects Templates to tenant workspaces as a
//     read-only view.
//   - APIExportEndpointSlice (apis.kcp.io/v1alpha1) — every
//     APIExport-VW consumer (kro, future provider controllers, etc.)
//     reads this to find URLs.
//
// Idempotent.
// PlatformAPIExportEndpointSlice ensures the slice the kcp-apiexport kro
// provider watches. workspacePath is the logical-cluster path the APIExport
// lives in (root:kedge:providers:<name>) — REQUIRED so kcp can resolve the
// export's cluster and publish endpoint URLs in status. Without it the slice
// stays endpoint-less and kro never discovers a virtual-workspace URL to
// watch, so tenant instances are never reconciled.
func PlatformAPIExportEndpointSlice(ctx context.Context, config *rest.Config, workspacePath string) error {
	log := klog.FromContext(ctx).WithName("install.apiexportendpointslice")
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}

	want := &apisv1alpha1.APIExportEndpointSlice{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apisv1alpha1.SchemeGroupVersion.String(),
			Kind:       "APIExportEndpointSlice",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: APIExportEndpointSliceName,
		},
		Spec: apisv1alpha1.APIExportEndpointSliceSpec{
			APIExport: apisv1alpha1.ExportBindingReference{
				Name: APIExportName,
				Path: workspacePath,
			},
		},
	}
	obj, err := apiExportEndpointSliceToUnstructured(want)
	if err != nil {
		return fmt.Errorf("to unstructured: %w", err)
	}

	existing, err := dyn.Resource(apiExportEndpointSliceGVR).Get(ctx, want.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get existing APIExportEndpointSlice: %w", err)
	}
	if apierrors.IsNotFound(err) {
		if _, err = dyn.Resource(apiExportEndpointSliceGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create APIExportEndpointSlice: %w", err)
		}
		log.Info("APIExportEndpointSlice created", "name", want.Name, "apiExport", APIExportName, "path", workspacePath)
		return nil
	}
	// spec.export is immutable, so a pre-existing slice with the wrong/empty
	// path can never publish endpoints — delete + recreate it so the corrected
	// path takes effect. Idempotent once the path already matches.
	existingPath, _, _ := unstructured.NestedString(existing.Object, "spec", "export", "path")
	if existingPath == workspacePath {
		log.Info("APIExportEndpointSlice already correct", "name", existing.GetName(), "path", existingPath)
		return nil
	}
	log.Info("APIExportEndpointSlice has stale export path; recreating", "name", existing.GetName(), "from", existingPath, "to", workspacePath)
	if err := dyn.Resource(apiExportEndpointSliceGVR).Delete(ctx, want.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete stale APIExportEndpointSlice: %w", err)
	}
	if _, err = dyn.Resource(apiExportEndpointSliceGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("recreate APIExportEndpointSlice: %w", err)
	}
	return nil
}

func apiExportEndpointSliceToUnstructured(s *apisv1alpha1.APIExportEndpointSlice) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	out := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &out.Object); err != nil {
		return nil, err
	}
	out.SetAPIVersion(apisv1alpha1.SchemeGroupVersion.String())
	out.SetKind("APIExportEndpointSlice")
	return out, nil
}

// PlatformCachedResourceEndpointSlices ensures the
// CachedResourceEndpointSlice for the platform's `publish-templates`
// CachedResource exists, with both the CachedResource and the APIExport
// references set. Idempotent.
//
// Called by init_cmd.go after PlatformCachedResources so the source
// CachedResource is already in place.
func PlatformCachedResourceEndpointSlices(ctx context.Context, config *rest.Config) error {
	log := klog.FromContext(ctx).WithName("install.endpointslice")
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}

	want := &cachev1alpha1.CachedResourceEndpointSlice{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cachev1alpha1.SchemeGroupVersion.String(),
			Kind:       "CachedResourceEndpointSlice",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: EndpointSliceTemplatesName,
		},
		Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
			CachedResource: cachev1alpha1.CachedResourceReference{
				Name: CachedResourceTemplatesName,
			},
			APIExport: cachev1alpha1.ExportBindingReference{
				Name: APIExportName,
			},
		},
	}

	obj, err := endpointSliceToUnstructured(want)
	if err != nil {
		return fmt.Errorf("to unstructured: %w", err)
	}

	existing, err := dyn.Resource(cachedResourceEndpointSliceGVR).Get(ctx, want.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get existing EndpointSlice: %w", err)
	}
	if apierrors.IsNotFound(err) {
		if _, err = dyn.Resource(cachedResourceEndpointSliceGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create EndpointSlice: %w", err)
		}
		log.Info("EndpointSlice created", "name", want.Name)
		return nil
	}

	// kcp's CachedResource reconciler may have auto-created the slice
	// with only CachedResource set — patch in APIExport (and the kcp
	// spec marks both fields as "must not be changed", so we only
	// touch the slice when the existing one is missing the export).
	specExport, _, _ := unstructured.NestedString(existing.Object, "spec", "export", "name")
	if specExport == APIExportName {
		return nil
	}
	if err := unstructured.SetNestedField(existing.Object, APIExportName, "spec", "export", "name"); err != nil {
		return fmt.Errorf("set spec.export.name: %w", err)
	}
	if _, err = dyn.Resource(cachedResourceEndpointSliceGVR).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update EndpointSlice with APIExport ref: %w", err)
	}
	log.Info("EndpointSlice patched with APIExport ref", "name", want.Name, "apiExport", APIExportName)
	return nil
}

// WaitForCachedResourceIdentity polls the CachedResource named
// CachedResourceTemplatesName until status.identityHash is non-empty
// or the timeout expires. Returns the resolved hash on success.
//
// Required before flipping APIExport.spec.resources[templates].storage
// from crd to virtual: the virtual reference carries the hash, and a
// stale / empty value makes the apiserver reject the update.
func WaitForCachedResourceIdentity(ctx context.Context, config *rest.Config) (string, error) {
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return "", fmt.Errorf("dynamic client: %w", err)
	}
	var hash string
	pollCtx, cancel := context.WithTimeout(ctx, CachedResourceReadyTimeout)
	defer cancel()
	err = wait.PollUntilContextCancel(pollCtx, 500*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		cr, err := dyn.Resource(cachedResourceGVR).Get(ctx, CachedResourceTemplatesName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		h, _, _ := unstructured.NestedString(cr.Object, "status", "identityHash")
		if h == "" {
			return false, nil
		}
		hash = h
		return true, nil
	})
	if err != nil {
		return "", fmt.Errorf("waiting for CachedResource %q identityHash: %w", CachedResourceTemplatesName, err)
	}
	return hash, nil
}

func endpointSliceToUnstructured(s *cachev1alpha1.CachedResourceEndpointSlice) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	out := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &out.Object); err != nil {
		return nil, err
	}
	out.SetAPIVersion(cachev1alpha1.SchemeGroupVersion.String())
	out.SetKind("CachedResourceEndpointSlice")
	return out, nil
}
