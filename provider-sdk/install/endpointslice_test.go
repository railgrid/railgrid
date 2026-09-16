/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const (
	testSliceName = "edges.providers.railgrid.ai"
	testExport    = "edges.providers.railgrid.ai"
)

// sliceClient returns a fake client scoped to a workspace whose LogicalCluster
// carries workspacePath (none when empty), seeded with seed.
func sliceClient(t *testing.T, workspacePath string, seed ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	t.Helper()
	objs := []runtime.Object{}
	if workspacePath != "" {
		objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "core.kcp.io/v1alpha1",
			"kind":       "LogicalCluster",
			"metadata": map[string]any{
				"name":        "cluster",
				"annotations": map[string]any{"kcp.io/path": workspacePath},
			},
		}})
	}
	for _, o := range seed {
		objs = append(objs, o)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			logicalClusterGVR:         "LogicalClusterList",
			apiExportEndpointSliceGVR: "APIExportEndpointSliceList",
		}, objs...)
}

// existingSlice is a slice as an earlier install left it. path "" means the
// spec.export.path field is absent, which is what SDKs before this change wrote.
func existingSlice(path string) *unstructured.Unstructured {
	export := map[string]any{"name": testExport}
	if path != "" {
		export["path"] = path
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha1",
		"kind":       "APIExportEndpointSlice",
		"metadata":   map[string]any{"name": testSliceName},
		"spec":       map[string]any{"export": export},
	}}
}

func slicePath(t *testing.T, cl *dynamicfake.FakeDynamicClient) (string, bool) {
	t.Helper()
	got, err := cl.Resource(apiExportEndpointSliceGVR).Get(context.Background(), testSliceName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get slice: %v", err)
	}
	path, found, err := unstructured.NestedString(got.Object, "spec", "export", "path")
	if err != nil {
		t.Fatalf("read spec.export.path: %v", err)
	}
	return path, found
}

func sliceDeletes(cl *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, a := range cl.Actions() {
		if a.GetVerb() == "delete" && a.GetResource() == apiExportEndpointSliceGVR {
			n++
		}
	}
	return n
}

// The slice must always carry a canonical spec.export.path. kcp's per-shard
// endpoint publisher finds a slice from an APIBinding by the binding's
// canonical export path, and a path-less slice is indexed only under its
// logical-cluster name — so a shard whose first consumer appears never
// publishes its virtual-workspace URL for it.
func TestEnsureAPIExportEndpointSlice(t *testing.T) {
	const (
		platformPath = "root:railgrid:providers:edges"
		orgPath      = "root:railgrid:tenants:86b7f9e7:providers:edges"
	)

	for _, tc := range []struct {
		name          string
		workspacePath string // canonical path on the targeted workspace's LogicalCluster
		argPath       string // workspacePath argument
		seed          []*unstructured.Unstructured
		wantPath      string
		wantDeletes   int
	}{
		{
			name:          "empty path resolves the platform workspace and writes it",
			workspacePath: platformPath,
			wantPath:      platformPath,
		},
		{
			name:          "empty path resolves an org-owned workspace and writes it",
			workspacePath: orgPath,
			wantPath:      orgPath,
		},
		{
			name:        "explicit path is written as given without a lookup",
			argPath:     "root:elsewhere",
			wantPath:    "root:elsewhere",
			wantDeletes: 0,
		},
		{
			name:          "path-less slice from an older SDK is recreated with the path",
			workspacePath: platformPath,
			seed:          []*unstructured.Unstructured{existingSlice("")},
			wantPath:      platformPath,
			wantDeletes:   1,
		},
		{
			name:          "slice that already carries the resolved path is left alone",
			workspacePath: platformPath,
			seed:          []*unstructured.Unstructured{existingSlice(platformPath)},
			wantPath:      platformPath,
		},
		{
			name:          "slice with a different path is recreated",
			workspacePath: platformPath,
			argPath:       "root:elsewhere",
			seed:          []*unstructured.Unstructured{existingSlice(platformPath)},
			wantPath:      "root:elsewhere",
			wantDeletes:   1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl := sliceClient(t, tc.workspacePath, tc.seed...)

			if err := EnsureAPIExportEndpointSlice(context.Background(), cl, testSliceName, testExport, tc.argPath); err != nil {
				t.Fatalf("EnsureAPIExportEndpointSlice: %v", err)
			}

			path, found := slicePath(t, cl)
			if !found {
				t.Fatalf("spec.export.path is absent; want %q", tc.wantPath)
			}
			if path != tc.wantPath {
				t.Errorf("spec.export.path = %q, want %q", path, tc.wantPath)
			}
			if got := sliceDeletes(cl); got != tc.wantDeletes {
				t.Errorf("slice deletes = %d, want %d", got, tc.wantDeletes)
			}
		})
	}
}

// Running twice must not churn: the second run sees the path it wrote and
// neither deletes nor recreates the slice, so providers do not lose their
// virtual-workspace endpoints on every restart.
func TestEnsureAPIExportEndpointSliceIsIdempotent(t *testing.T) {
	cl := sliceClient(t, "root:railgrid:providers:edges", existingSlice(""))
	for i := range 2 {
		if err := EnsureAPIExportEndpointSlice(context.Background(), cl, testSliceName, testExport, ""); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if got := sliceDeletes(cl); got != 1 {
		t.Errorf("slice deletes after two runs = %d, want 1 (the one-time migration)", got)
	}
}

// Without a resolvable path the slice is not written at all rather than written
// path-less, which would silently reintroduce the bug.
func TestEnsureAPIExportEndpointSliceNeedsResolvablePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		lc   *unstructured.Unstructured
	}{
		{name: "no LogicalCluster"},
		{
			name: "LogicalCluster without kcp.io/path",
			lc: &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "core.kcp.io/v1alpha1",
				"kind":       "LogicalCluster",
				"metadata":   map[string]any{"name": "cluster"},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seed []*unstructured.Unstructured
			if tc.lc != nil {
				seed = append(seed, tc.lc)
			}
			cl := sliceClient(t, "", seed...)

			err := EnsureAPIExportEndpointSlice(context.Background(), cl, testSliceName, testExport, "")
			if err == nil {
				t.Fatal("expected an error when the workspace path cannot be resolved")
			}
			if !strings.Contains(err.Error(), "resolving workspace path") {
				t.Errorf("unexpected error: %v", err)
			}
			for _, a := range cl.Actions() {
				if a.GetResource() == apiExportEndpointSliceGVR {
					t.Errorf("slice was touched (%s) although the path is unknown", a.GetVerb())
				}
			}
		})
	}
}
