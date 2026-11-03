// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

func newProviderBoundCopyResolver(config *rest.Config, registry *providers.Registry) providers.BoundCopyResolver {
	// Reuse the transport without caching security-sensitive binding decisions.
	// Registration removal and binding retargeting must take effect on the next call.
	httpClient, transportErr := rest.HTTPClientFor(config)
	return func(ctx context.Context, tenantPath, cluster, name string) (providers.Provider, error) {
		if transportErr != nil {
			return providers.Provider{}, transportErr
		}
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, cluster)
		client, err := dynamic.NewForConfigAndClient(cfg, httpClient)
		if err != nil {
			return providers.Provider{}, err
		}
		return resolveProviderBoundCopy(ctx, client, registry, tenantPath, cluster, name)
	}
}

func resolveProviderBoundCopy(ctx context.Context, client dynamic.Interface, registry *providers.Registry, tenantPath, cluster, name string) (providers.Provider, error) {
	refused := providers.ErrBoundCopyUnavailable
	const tenantPrefix = "root:railgrid:tenants:"
	suffix := strings.TrimPrefix(tenantPath, tenantPrefix)
	org, workspace, ok := strings.Cut(suffix, ":")
	if suffix == tenantPath || !ok || org == "" || workspace == "" || strings.Contains(workspace, ":") || cluster == "" {
		return providers.Provider{}, refused
	}
	logical, err := client.Resource(schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return providers.Provider{}, err
	}
	if logical.GetAnnotations()["kcp.io/path"] != tenantPath {
		return providers.Provider{}, refused
	}
	catalog, found := registry.GetForOrg(org, name)
	bindings, err := client.Resource(schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}).List(ctx, metav1.ListOptions{})
	if err != nil {
		return providers.Provider{}, err
	}
	var selected providers.Provider
	matched := false
	boundAPI := false
	for _, binding := range bindings.Items {
		path, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "path")
		if binding.GetName() == name || path == "root:railgrid:providers:"+name || path == "root:railgrid:tenants:"+org+":providers:"+name {
			boundAPI = true
		}
		ready, _ := providers.BindingReadiness(&binding)
		if !ready || binding.GetDeletionTimestamp() != nil {
			continue
		}
		export, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "name")
		issuer, _, _ := unstructured.NestedString(binding.Object, "status", "apiExportClusterName")
		if entry, ok := registry.BoundCopy(org, name, path, export, issuer); ok {
			if matched {
				return providers.Provider{}, refused
			}
			selected, matched = entry, true
		}
	}
	if !matched {
		// API-less providers have no binding. Do not let an API-less catalog
		// shadow steal requests from an existing or unavailable API binding.
		if !boundAPI && found && catalog.APIExportName == "" {
			return catalog, nil
		}
		return providers.Provider{}, refused
	}
	return selected, nil
}
