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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"

	sdkinstall "github.com/railgrid/provider-sdk/install"
)

// Exercise the shared init/operator sequence over the real REST clients with a
// fake dynamic client as its server. This checks write order and merge behavior,
// not kcp admission or controller convergence (those require a live cluster).
func TestBootstrapWorkspaceFreshAndUpgrade(t *testing.T) {
	ctx := context.Background()
	cl := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster",
		"metadata": map[string]any{"name": "cluster", "annotations": map[string]any{"kcp.io/path": "root:railgrid:providers:infrastructure"}},
	}})
	// Simulate the status written by the CRD and cache controllers. Delaying
	// identity until the export exists also catches a wait-before-publish deadlock.
	cl.PrependReactor("get", "*", func(a clienttesting.Action) (bool, runtime.Object, error) {
		get := a.(clienttesting.GetAction)
		obj, err := cl.Tracker().Get(a.GetResource(), "", get.GetName())
		if err != nil {
			return true, nil, err
		}
		u := obj.(*unstructured.Unstructured)
		if a.GetResource().Resource == "customresourcedefinitions" {
			_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Established", "status": "True"}}, "status", "conditions")
		}
		if a.GetResource().Resource == "clustercachedresources" {
			if _, err := cl.Tracker().Get(schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}, "", APIExportName); err == nil {
				_ = unstructured.SetNestedField(u.Object, "test-identity", "status", "identityHash")
			}
		}
		return true, u, nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 4 || parts[0] != "apis" {
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		resource := cl.Resource(schema.GroupVersionResource{Group: parts[1], Version: parts[2], Resource: parts[3]})
		var obj *unstructured.Unstructured
		var err error
		switch r.Method {
		case http.MethodGet:
			obj, err = resource.Get(r.Context(), parts[4], metav1.GetOptions{})
		case http.MethodPost, http.MethodPut:
			body := &unstructured.Unstructured{}
			if err = json.NewDecoder(r.Body).Decode(body); err == nil {
				if r.Method == http.MethodPost {
					obj, err = resource.Create(r.Context(), body, metav1.CreateOptions{})
				} else {
					obj, err = resource.Update(r.Context(), body, metav1.UpdateOptions{})
				}
			}
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			if status, ok := err.(apierrors.APIStatus); ok {
				s := status.Status()
				s.APIVersion = "v1"
				s.Kind = "Status"
				w.WriteHeader(int(s.Code))
				_ = json.NewEncoder(w).Encode(s)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(obj)
	}))
	defer srv.Close()
	opts := sdkinstall.Options{Config: &rest.Config{Host: srv.URL, QPS: 1000, Burst: 1000}, ExportName: APIExportName, WorkspacePath: "root:railgrid:providers:infrastructure", KCPDir: "../deploy/chart/files", DataPlaneURL: "https://provider.example"}
	exportGVR := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	for _, pass := range []string{"fresh", "upgrade"} {
		t.Run(pass, func(t *testing.T) {
			cl.ClearActions()
			if err := BootstrapWorkspace(ctx, opts); err != nil {
				t.Fatal(err)
			}
			sliceWrite, exportWrite := -1, -1
			for i, a := range cl.Actions() {
				var obj *unstructured.Unstructured
				switch a.GetVerb() {
				case "create":
					obj = a.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured)
				case "update":
					obj = a.(clienttesting.UpdateAction).GetObject().(*unstructured.Unstructured)
				default:
					continue
				}
				if a.GetResource() == cachedResourceEndpointSliceGVR {
					sliceWrite = i
				}
				if a.GetResource() != exportGVR {
					continue
				}
				exportWrite = i
				resources, _, _ := unstructured.NestedSlice(obj.Object, "spec", "resources")
				found, foreign := false, false
				for _, raw := range resources {
					entry := raw.(map[string]any)
					if entry["group"] == "sample.example" {
						foreign = true
					}
					if entry["name"] != "templates" {
						continue
					}
					found = true
					ref, _, _ := unstructured.NestedString(entry, "storage", "virtual", "reference", "name")
					_, crd, _ := unstructured.NestedMap(entry, "storage", "crd")
					if ref != EndpointSliceTemplatesName || crd {
						t.Fatalf("Templates storage on export write: %#v", entry)
					}
				}
				if !found {
					t.Fatal("Templates missing from export write")
				}
				if pass == "upgrade" && !foreign {
					t.Fatal("runtime template entry lost during upgrade")
				}
			}
			if exportWrite < 0 || (pass == "fresh" && (sliceWrite < 0 || sliceWrite >= exportWrite)) {
				t.Fatalf("slice write=%d export write=%d", sliceWrite, exportWrite)
			}
		})
		if pass == "fresh" {
			export, err := cl.Resource(exportGVR).Get(ctx, APIExportName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			resources, _, _ := unstructured.NestedSlice(export.Object, "spec", "resources")
			resources = append(resources, map[string]any{"group": "sample.example", "name": "widgets", "schema": "v1.widgets.sample.example", "storage": map[string]any{"crd": map[string]any{}}})
			if err := unstructured.SetNestedSlice(export.Object, resources, "spec", "resources"); err != nil {
				t.Fatal(err)
			}
			if _, err := cl.Resource(exportGVR).Update(ctx, export, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
}
