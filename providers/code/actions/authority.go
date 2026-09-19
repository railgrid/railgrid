// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"errors"
	"strings"

	"github.com/railgrid/provider-code/install"
	"github.com/railgrid/provider-sdk/dataplane"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var repositories = schema.GroupVersionResource{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "repositories"}
var connections = schema.GroupVersionResource{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "connections"}

// ExportClient resolves a provider client through its accepted APIExport. It
// cannot use the caller's credentials or enter an unbound tenant workspace.
func ExportClient(config *rest.Config) func(context.Context, string, schema.GroupVersionResource, string) (dynamic.Interface, error) {
	return func(ctx context.Context, cluster string, gvr schema.GroupVersionResource, name string) (dynamic.Interface, error) {
		if config == nil || !dataplane.IsClusterID(cluster) {
			return nil, errors.New("code provider authority unavailable")
		}
		own, err := dynamic.NewForConfig(config)
		if err != nil {
			return nil, err
		}
		endpoints, err := own.Resource(schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices"}).Get(ctx, install.APIExportEndpointSliceName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		items, _, _ := unstructured.NestedSlice(endpoints.Object, "status", "endpoints")
		for _, item := range items {
			value, ok := item.(map[string]any)
			if !ok {
				continue
			}
			endpoint, _ := value["url"].(string)
			if endpoint == "" {
				continue
			}
			cfg := rest.CopyConfig(config)
			cfg.Host = strings.TrimRight(endpoint, "/") + "/clusters/" + cluster
			client, err := dynamic.NewForConfig(cfg)
			if err != nil {
				return nil, err
			}
			if _, err = client.Resource(gvr).Get(ctx, name, metav1.GetOptions{}); err == nil {
				return client, nil
			}
		}
		return nil, errors.New("object unavailable through Code export")
	}
}
