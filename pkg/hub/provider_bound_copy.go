/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hub

import (
	"context"
	"fmt"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

var boundAPIExportBindings = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
var boundLogicalClusters = schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}

// newProviderBoundCopyResolver routes sensitive authenticated backend calls to
// the provider copy named by the caller's actual workspace APIBinding.
func newProviderBoundCopyResolver(config *rest.Config, registry *providers.Registry) providers.BoundCopyResolver {
	return func(ctx context.Context, tenantPath, clusterID, providerName string) (providers.Provider, error) {
		orgUUID, workspaceUUID, _ := kcppaths.SplitWorkspacePath(tenantPath)
		if orgUUID == "" || workspaceUUID == "" || clusterID == "" || providerName == "" || config == nil || registry == nil {
			return providers.Provider{}, providers.ErrBoundCopyUnavailable
		}
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterID)
		client, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return providers.Provider{}, fmt.Errorf("create selected-workspace client: %w", err)
		}
		lc, err := client.Resource(boundLogicalClusters).Get(ctx, "cluster", v1.GetOptions{})
		if err != nil {
			return providers.Provider{}, fmt.Errorf("read selected LogicalCluster: %w", err)
		}
		annotations := lc.GetAnnotations()
		if annotations["kcp.io/path"] != tenantPath || annotations["kcp.io/cluster"] != clusterID {
			return providers.Provider{}, providers.ErrBoundCopyUnavailable
		}
		bindings, err := client.Resource(boundAPIExportBindings).List(ctx, v1.ListOptions{})
		if err != nil {
			return providers.Provider{}, fmt.Errorf("list selected workspace APIBindings: %w", err)
		}
		var selected providers.Provider
		found := false
		for i := range bindings.Items {
			binding := &bindings.Items[i]
			name := binding.GetName()
			if name != providerName {
				continue
			}
			path, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "path")
			exportName, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "name")
			issuer, _, _ := unstructured.NestedString(binding.Object, "status", "apiExportClusterName")
			provider, ok := registry.BoundCopy(orgUUID, providerName, path, exportName, issuer)
			if !ok {
				continue
			}
			ready, _ := providers.BindingReadiness(binding)
			if !ready {
				return providers.Provider{}, providers.ErrBoundCopyUnavailable
			}
			if found {
				return providers.Provider{}, providers.ErrBoundCopyUnavailable
			}
			selected, found = provider, true
		}
		if !found {
			return providers.Provider{}, providers.ErrBoundCopyUnavailable
		}
		return selected, nil
	}
}

// newProviderBoundCopySnapshotResolver reads the selected LogicalCluster and
// its APIBindings once for an MCP enumeration pass, so query cost does not
// scale with the number of registered providers and no source snapshot is
// cached across requests.
func newProviderBoundCopySnapshotResolver(config *rest.Config, registry *providers.Registry) providers.BoundCopySnapshotResolver {
	return func(ctx context.Context, tenantPath string) (map[string]providers.Provider, error) {
		orgUUID, _, ok := kcppaths.SplitWorkspacePath(tenantPath)
		if !ok || config == nil || registry == nil {
			return nil, providers.ErrBoundCopyUnavailable
		}
		pathConfig := rest.CopyConfig(config)
		pathConfig.Host = apiurl.KCPClusterURL(pathConfig.Host, tenantPath)
		pathClient, err := dynamic.NewForConfig(pathConfig)
		if err != nil {
			return nil, fmt.Errorf("create selected-workspace path client: %w", err)
		}
		lc, err := pathClient.Resource(boundLogicalClusters).Get(ctx, "cluster", v1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("read selected LogicalCluster: %w", err)
		}
		annotations := lc.GetAnnotations()
		clusterID := annotations["kcp.io/cluster"]
		if clusterID == "" || annotations["kcp.io/path"] != tenantPath {
			return nil, providers.ErrBoundCopyUnavailable
		}
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterID)
		client, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("create selected-workspace client: %w", err)
		}
		bindings, err := client.Resource(boundAPIExportBindings).List(ctx, v1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list selected workspace APIBindings: %w", err)
		}
		selected := make(map[string]providers.Provider)
		ambiguous := make(map[string]bool)
		for i := range bindings.Items {
			binding := &bindings.Items[i]
			name := binding.GetName()
			path, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "path")
			exportName, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "name")
			issuer, _, _ := unstructured.NestedString(binding.Object, "status", "apiExportClusterName")
			provider, match := registry.BoundCopy(orgUUID, name, path, exportName, issuer)
			if !match {
				continue
			}
			ready, _ := providers.BindingReadiness(binding)
			if !ready {
				delete(selected, name)
				ambiguous[name] = true
				continue
			}
			if _, exists := selected[name]; exists {
				delete(selected, name)
				ambiguous[name] = true
				continue
			}
			selected[name] = provider
		}
		for name := range ambiguous {
			delete(selected, name)
		}
		return selected, nil
	}
}
