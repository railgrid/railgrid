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

package identity

import (
	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// WorkspaceRegistryCatalog resolves policy using actual bindings. A bare
// platform catalog lookup cannot describe a customer-authored provider or the
// organization copy of a same-named provider.
type WorkspaceRegistryCatalog struct {
	registry *providers.Registry
	bindings *APIBindingChecker
}

func NewWorkspaceRegistryCatalog(registry *providers.Registry, bindings *APIBindingChecker) *WorkspaceRegistryCatalog {
	return &WorkspaceRegistryCatalog{registry: registry, bindings: bindings}
}

// GroupOwner fails closed; Authorize first calls ForWorkspace.
func (*WorkspaceRegistryCatalog) GroupOwner(string) (string, bool)      { return "", false }
func (*WorkspaceRegistryCatalog) ExportedGroups(string) []string        { return nil }
func (*WorkspaceRegistryCatalog) DeclaredVerbs(string, string) []string { return nil }

func (c *WorkspaceRegistryCatalog) ForWorkspace(cluster string) (ProviderCatalog, error) {
	if c == nil || c.registry == nil || c.bindings == nil || cluster == "" {
		return nil, fmt.Errorf("workspace provider catalog is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := c.bindings.clients(cluster)
	if err != nil {
		return nil, err
	}
	logical, err := client.Resource(schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	org, workspace, valid := kcppaths.SplitWorkspacePath(logical.GetAnnotations()["kcp.io/path"])
	if !valid || org == "" || workspace == "" {
		return nil, fmt.Errorf("identity policy requires a tenant workspace")
	}
	bindings, err := client.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	entries := map[string]providers.Provider{}
	ambiguous := map[string]bool{}
	for _, binding := range bindings.Items {
		ready, _ := providers.BindingReadiness(&binding)
		if !ready || binding.GetDeletionTimestamp() != nil {
			continue
		}
		path, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "path")
		export, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "name")
		issuer, _, _ := unstructured.NestedString(binding.Object, "status", "apiExportClusterName")
		for _, entry := range c.registry.List() {
			if !entry.Ready() || entry.APIExportPath != path || entry.APIExportName != export || entry.WorkspaceCluster != issuer || issuer == "" || (entry.OrgUUID != "" && entry.OrgUUID != org) {
				continue
			}
			if _, duplicate := entries[entry.Name]; duplicate {
				ambiguous[entry.Name] = true
			}
			entries[entry.Name] = entry
		}
	}
	for name := range ambiguous {
		delete(entries, name)
	}
	return workspaceCatalogSnapshot(entries), nil
}

type workspaceCatalogSnapshot map[string]providers.Provider

func (c workspaceCatalogSnapshot) IsBound(_ string, name string) (bool, error) {
	_, exists := c[name]
	return exists, nil
}

func (c workspaceCatalogSnapshot) ExportedGroups(name string) []string {
	return append([]string(nil), c[name].APIGroups...)
}

func (c workspaceCatalogSnapshot) DeclaredVerbs(name, resource string) []string {
	return dedupeSorted(providersv1alpha1.ProviderDeclaredVerbs(c[name].Export, resource))
}

func (c workspaceCatalogSnapshot) GroupOwner(group string) (string, bool) {
	names := make([]string, 0, len(c))
	for name, entry := range c {
		if containsAny(entry.APIGroups, group) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", false
	}
	return names[0], true
}
