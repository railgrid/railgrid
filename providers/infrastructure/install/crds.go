/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package install embeds the infrastructure provider's CRDs and
// installs them into the provider's own workspace at startup. The
// hub deliberately does NOT carry these CRDs in its bootstrap embed
// — the provider is self-contained, including its API types.
package install

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

//go:embed crds/*.yaml
var crdsFS embed.FS

// crdGVR is what the provider's dynamic client targets to write CRDs.
// CRDs always live in apiextensions.k8s.io/v1.
var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// CRDs installs every CRD baked into the binary's embedded crds/
// directory into the workspace the supplied rest.Config points at.
// Idempotent — existing CRDs are patched in-place, ResourceVersion
// preserved so the apiserver doesn't reject the update.
//
// Callers pass a rest.Config scoped to the provider's own kcp
// workspace (root:kedge:providers:infrastructure). The hub's catalog
// controller has already created that workspace + the APIExport by
// the time the provider binary runs; we just need to land our
// platform CRDs inside it.
func CRDs(ctx context.Context, config *rest.Config) error {
	log := klog.FromContext(ctx).WithName("install.crds")

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("dynamic client for CRD install: %w", err)
	}

	entries, err := fs.ReadDir(crdsFS, "crds")
	if err != nil {
		return fmt.Errorf("read embedded crds/: %w", err)
	}

	var applied int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		bytes, err := fs.ReadFile(crdsFS, "crds/"+e.Name())
		if err != nil {
			return fmt.Errorf("read embedded crds/%s: %w", e.Name(), err)
		}
		if err := applyCRD(ctx, client, bytes); err != nil {
			return fmt.Errorf("apply %s: %w", e.Name(), err)
		}
		applied++
	}
	log.Info("installed CRDs", "count", applied)
	return nil
}

// applyCRD CREATEs or UPDATEs a CRD from raw YAML. Round-trips through
// the dynamic client (rather than the apiextensions clientset) to keep
// the provider binary's dependency surface tight.
func applyCRD(ctx context.Context, client dynamic.Interface, raw []byte) error {
	var crd apiextensionsv1.CustomResourceDefinition
	if err := utilyaml.Unmarshal(raw, &crd); err != nil {
		return fmt.Errorf("unmarshal CRD YAML: %w", err)
	}
	if crd.Name == "" {
		return fmt.Errorf("CRD has no metadata.name")
	}

	obj, err := unstructuredFromCRD(&crd)
	if err != nil {
		return fmt.Errorf("convert to unstructured: %w", err)
	}

	existing, err := client.Resource(crdGVR).Get(ctx, crd.Name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get existing CRD: %w", err)
	}
	if apierrors.IsNotFound(err) {
		_, err = client.Resource(crdGVR).Create(ctx, obj, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create CRD: %w", err)
		}
		return nil
	}

	// Preserve the apiserver-supplied ResourceVersion so the update
	// is a proper compare-and-set rather than a blind overwrite.
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, err = client.Resource(crdGVR).Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update CRD: %w", err)
	}
	return nil
}

// unstructuredFromCRD converts the typed CRD into the
// map[string]any shape the dynamic client expects without registering
// the apiextensions scheme for one operation.
func unstructuredFromCRD(crd *apiextensionsv1.CustomResourceDefinition) (*unstructured.Unstructured, error) {
	// The CRD type has JSON tags; encoding/json round-trip gives us
	// the canonical map[string]any layout the dynamic client wants.
	data, err := json.Marshal(crd)
	if err != nil {
		return nil, err
	}
	out := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &out.Object); err != nil {
		return nil, err
	}
	out.SetAPIVersion("apiextensions.k8s.io/v1")
	out.SetKind("CustomResourceDefinition")
	return out, nil
}
