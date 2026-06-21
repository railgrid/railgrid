/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// logicalClusterGVR is kcp's per-workspace LogicalCluster singleton.
var logicalClusterGVR = schema.GroupVersionResource{
	Group:    "core.kcp.io",
	Version:  "v1alpha1",
	Resource: "logicalclusters",
}

// discoverWorkspacePath returns the kcp workspace path the supplied config is
// scoped to, read from the LogicalCluster's kcp.io/path annotation. This lets a
// workspace-scoped provider kubeconfig drive the operator without the caller
// having to also pass spec.providerWorkspace.
func discoverWorkspacePath(ctx context.Context, cfg *rest.Config) (string, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", err
	}
	obj, err := dyn.Resource(logicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get LogicalCluster to resolve workspace path: %w", err)
	}
	if path := obj.GetAnnotations()["kcp.io/path"]; path != "" {
		return path, nil
	}
	return "", fmt.Errorf("LogicalCluster has no kcp.io/path annotation; set spec.providerWorkspace explicitly")
}
