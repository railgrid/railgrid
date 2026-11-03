// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/railgrid/railgrid/pkg/hub/providers"
)

func TestProviderBoundCopy(t *testing.T) {
	const tenant = "root:railgrid:tenants:test-org:workspace"
	platform := providers.Provider{Name: "edges", APIExportPath: "root:railgrid:providers:edges", APIExportName: "edges.example", WorkspaceCluster: "platform-cluster"}
	org := providers.Provider{Name: "edges", OrgUUID: "test-org", APIExportPath: "root:railgrid:tenants:test-org:providers:edges", APIExportName: "edges.example", WorkspaceCluster: "org-cluster"}
	for _, tc := range []struct {
		name          string
		entry         providers.Provider
		mutate        func(*unstructured.Unstructured)
		remove        bool
		path          string
		want          bool
		apiLessShadow bool
		noBinding     bool
	}{
		{name: "platform despite org shadow", entry: platform, want: true},
		{name: "platform despite API-less org shadow", entry: platform, apiLessShadow: true, want: true},
		{name: "API-less provider without binding", entry: org, apiLessShadow: true, noBinding: true, want: true},
		{name: "API-less shadow cannot replace stale platform binding", entry: platform, apiLessShadow: true, mutate: func(b *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(b.Object, "old-cluster", "status", "apiExportClusterName")
		}},
		{name: "org copy", entry: org, want: true},
		{name: "removed registration", entry: org, remove: true},
		{name: "stale export cluster", entry: org, mutate: func(b *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(b.Object, "old-cluster", "status", "apiExportClusterName")
		}},
		{name: "wrong export name", entry: org, mutate: func(b *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(b.Object, "other", "spec", "reference", "export", "name")
		}},
		{name: "not bound", entry: org, mutate: func(b *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(b.Object, "Binding", "status", "phase")
		}},
		{name: "foreign logical cluster", entry: org, path: "root:railgrid:tenants:foreign:workspace"},
		{name: "foreign registration", entry: providers.Provider{Name: "edges", OrgUUID: "foreign", APIExportPath: "root:railgrid:tenants:foreign:providers:edges", APIExportName: "edges.example", WorkspaceCluster: "foreign-cluster"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := providers.NewRegistry()
			reg.Upsert(platform)
			if !tc.remove {
				reg.Upsert(org)
			}
			if tc.apiLessShadow {
				shadow := org
				shadow.APIExportName = ""
				reg.Upsert(shadow)
			}
			if tc.entry.OrgUUID == "foreign" {
				reg.Upsert(tc.entry)
			}
			path := tenant
			if tc.path != "" {
				path = tc.path
			}
			logical := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster", "metadata": map[string]any{"name": "cluster", "annotations": map[string]any{"kcp.io/path": path}}}}
			binding := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": "arbitrary-binding-name"}, "spec": map[string]any{"reference": map[string]any{"export": map[string]any{"path": tc.entry.APIExportPath, "name": tc.entry.APIExportName}}}, "status": map[string]any{"phase": "Bound", "apiExportClusterName": tc.entry.WorkspaceCluster}}}
			if tc.mutate != nil {
				tc.mutate(binding)
			}
			objects := []runtime.Object{logical}
			if !tc.noBinding {
				objects = append(objects, binding)
			}
			client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}: "APIBindingList"}, objects...)
			got, err := resolveProviderBoundCopy(context.Background(), client, reg, tenant, "consumer-cluster", "edges")
			if (err == nil) != tc.want {
				t.Fatalf("got provider %s/%s err=%v, want allowed=%v", got.OrgUUID, got.Name, err, tc.want)
			}
			if tc.want && got.WorkspaceCluster != tc.entry.WorkspaceCluster {
				t.Fatalf("selected wrong copy: %#v", got)
			}
		})
	}
}
