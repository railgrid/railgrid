/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package install holds the one-shot bootstrap steps the code provider runs
// against its own kcp workspace. Today that is a single step: ensuring an
// APIExportEndpointSlice exists for the provider's APIExport so the
// multicluster apiexport provider can discover tenant workspaces.
//
// The hub provisioner creates the sub-workspace, the four APIResourceSchemas,
// the APIExport, the ServiceAccount, and the minted kubeconfig — but it does
// NOT create an APIExportEndpointSlice (that is the consumer's job, since the
// slice's export path and name are consumer-chosen). Without the slice,
// apiexport.New has no endpoints to watch and the controllers never engage any
// tenant cluster. EnsureAPIExportEndpointSlice closes that gap; it is called
// idempotently at serve startup (and by the init subcommand).
package install

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

const (
	// APIExportName is the provider's APIExport (manifest.yaml spec.apiExport.name).
	APIExportName = "code.providers.kedge.faros.sh"
	// APIExportEndpointSliceName is the slice the multicluster manager watches.
	// Matches controller_manager.go's endpointSliceName and, by convention, the
	// APIExport name.
	APIExportEndpointSliceName = "code.providers.kedge.faros.sh"
)

var apiExportEndpointSliceGVR = schema.GroupVersionResource{
	Group:    apisv1alpha1.SchemeGroupVersion.Group,
	Version:  apisv1alpha1.SchemeGroupVersion.Version,
	Resource: "apiexportendpointslices",
}

// EnsureAPIExportEndpointSlice ensures an APIExportEndpointSlice referencing the
// provider's APIExport exists in the provider workspace. workspacePath is the
// logical-cluster path the APIExport lives in (root:kedge:providers:code) —
// REQUIRED so kcp can resolve the export and publish endpoint URLs in status.
//
// spec.export is immutable, so a pre-existing slice with a stale path is
// deleted + recreated. Idempotent once the path already matches.
func EnsureAPIExportEndpointSlice(ctx context.Context, config *rest.Config, workspacePath string) error {
	log := klog.FromContext(ctx).WithName("install.apiexportendpointslice")
	if workspacePath == "" {
		return fmt.Errorf("workspacePath is required to publish APIExportEndpointSlice endpoints")
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}

	want := &apisv1alpha1.APIExportEndpointSlice{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apisv1alpha1.SchemeGroupVersion.String(),
			Kind:       "APIExportEndpointSlice",
		},
		ObjectMeta: metav1.ObjectMeta{Name: APIExportEndpointSliceName},
		Spec: apisv1alpha1.APIExportEndpointSliceSpec{
			APIExport: apisv1alpha1.ExportBindingReference{
				Name: APIExportName,
				Path: workspacePath,
			},
		},
	}
	obj, err := toUnstructured(want)
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

func toUnstructured(s *apisv1alpha1.APIExportEndpointSlice) (*unstructured.Unstructured, error) {
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
