/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/hub/providers"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// providerNameFromExportPath decides which APIBindings count as "this provider
// is enabled here". Getting it wrong either hides enabled providers or reports
// another Org's provider as enabled.
func TestProviderNameFromExportPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		orgUUID  string
		wantName string
		wantOK   bool
	}{
		{
			name:     "platform provider",
			path:     kcppaths.ProviderPath("edges"),
			orgUUID:  "org1",
			wantName: "edges",
			wantOK:   true,
		},
		{
			name:     "own org provider",
			path:     kcppaths.OrgProviderPath("org1", "vault"),
			orgUUID:  "org1",
			wantName: "vault",
			wantOK:   true,
		},
		{
			// The isolation case: a binding pointing at another Org's export
			// must never be reported as an enabled provider here.
			name:    "another org's provider",
			path:    kcppaths.OrgProviderPath("org2", "vault"),
			orgUUID: "org1",
		},
		{
			name:    "core platform export is not a provider",
			path:    kcppaths.SystemControllers,
			orgUUID: "org1",
		},
		{
			name:    "team workspace path is not a provider",
			path:    kcppaths.WorkspacePath("org1", "ws1"),
			orgUUID: "org1",
		},
		{
			name:    "providers parent alone names no provider",
			path:    kcppaths.ProvidersParent,
			orgUUID: "org1",
		},
		{
			name:    "nested path below a platform provider",
			path:    kcppaths.ProviderPath("edges") + ":nested",
			orgUUID: "org1",
		},
		{
			name:    "empty path",
			path:    "",
			orgUUID: "org1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := providerNameFromExportPath(tc.path, tc.orgUUID)
			if ok != tc.wantOK {
				t.Fatalf("providerNameFromExportPath(%q, %q) ok = %v, want %v", tc.path, tc.orgUUID, ok, tc.wantOK)
			}
			if got != tc.wantName {
				t.Errorf("providerNameFromExportPath(%q, %q) = %q, want %q", tc.path, tc.orgUUID, got, tc.wantName)
			}
		})
	}
}

func TestGetOrgProviderWorkspaceProjectsBootstrapPolicyAndTermination(t *testing.T) {
	for _, tc := range []struct {
		name        string
		annotations map[string]any
		wantMarker  string
		deleting    bool
	}{
		{name: "restricted live source", annotations: map[string]any{providers.OrgProviderBootstrapPolicyAnnotation: providers.OrgProviderBootstrapPolicy}, wantMarker: providers.OrgProviderBootstrapPolicy},
		{name: "unrestricted legacy source", annotations: map[string]any{}, wantMarker: ""},
		{name: "terminating source", annotations: map[string]any{providers.OrgProviderBootstrapPolicyAnnotation: providers.OrgProviderBootstrapPolicy}, wantMarker: providers.OrgProviderBootstrapPolicy, deleting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/apis/tenancy.kcp.io/v1alpha1/workspaces/planner") {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				metadata := map[string]any{"name": "planner", "annotations": tc.annotations}
				if tc.deleting {
					metadata["deletionTimestamp"] = "2026-11-01T00:00:00Z"
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace", "metadata": metadata,
					"spec": map[string]any{"cluster": "provider-cluster"}, "status": map[string]any{"phase": "Ready"},
				})
			}))
			defer srv.Close()
			got, err := NewBootstrapper(&rest.Config{Host: srv.URL}).GetOrgProviderWorkspace(context.Background(), "org-1", "planner")
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || got.BootstrapPolicy != tc.wantMarker || got.Terminating != tc.deleting {
				t.Fatalf("workspace = %#v, want marker %q and terminating %v", got, tc.wantMarker, tc.deleting)
			}
		})
	}
}

func TestOrgProviderWorkspaceEnrollmentMarkerIsSetOnlyOnCreate(t *testing.T) {
	for _, scenario := range []string{"new", "restricted", "legacy"} {
		t.Run(scenario, func(t *testing.T) {
			var provider *unstructured.Unstructured
			if scenario != "new" {
				provider = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace", "metadata": map[string]any{"name": "planner"}, "spec": map[string]any{"cluster": "provider-cluster"}, "status": map[string]any{"phase": "Ready"}}}
				if scenario == "restricted" {
					provider.SetAnnotations(map[string]string{providers.OrgProviderBootstrapPolicyAnnotation: providers.OrgProviderBootstrapPolicy})
				}
			}
			writes := 0
			quotas := map[string]*unstructured.Unstructured{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				status := func(code int, reason metav1.StatusReason) {
					w.WriteHeader(code)
					_ = json.NewEncoder(w).Encode(&metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: reason, Code: int32(code)})
				}
				if strings.Contains(r.URL.Path, "/api/v1/namespaces") {
					if strings.Contains(r.URL.Path, "/configmaps") {
						var object unstructured.Unstructured
						_ = json.NewDecoder(r.Body).Decode(&object)
						w.WriteHeader(403)
						_ = json.NewEncoder(w).Encode(&metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: metav1.StatusReasonForbidden, Code: 403, Message: "exceeded quota: " + object.GetName()})
						return
					}
					if strings.Contains(r.URL.Path, "/resourcequotas") {
						name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
						if r.Method == http.MethodGet && name == "resourcequotas" {
							items := []any{}
							for _, object := range quotas {
								items = append(items, object.Object)
							}
							_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "ResourceQuotaList", "items": items})
							return
						}
						if r.Method == http.MethodPost || r.Method == http.MethodPut {
							object := &unstructured.Unstructured{}
							_ = json.NewDecoder(r.Body).Decode(object)
							hard, _, _ := unstructured.NestedStringMap(object.Object, "spec", "hard")
							used := map[string]string{}
							for key := range hard {
								used[key] = "0"
							}
							_ = unstructured.SetNestedStringMap(object.Object, hard, "status", "hard")
							_ = unstructured.SetNestedStringMap(object.Object, used, "status", "used")
							quotas[object.GetName()] = object
							_ = json.NewEncoder(w).Encode(object)
						} else if r.Method == http.MethodDelete {
							delete(quotas, name)
							_ = json.NewEncoder(w).Encode(&metav1.Status{Status: "Success"})
						} else if object := quotas[name]; object != nil {
							_ = json.NewEncoder(w).Encode(object)
						} else {
							status(404, metav1.StatusReasonNotFound)
						}
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "admin"}})
					return
				}
				if strings.HasPrefix(r.URL.Path, "/clusters/root:railgrid:tenants:org-a/apis/") {
					if r.Method == http.MethodPost {
						status(409, metav1.StatusReasonAlreadyExists)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace", "metadata": map[string]any{"name": "providers"}, "status": map[string]any{"phase": "Ready"}})
					return
				}
				switch r.Method {
				case http.MethodGet:
					if provider == nil {
						status(404, metav1.StatusReasonNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode(provider)
				case http.MethodPost:
					if provider != nil {
						status(409, metav1.StatusReasonAlreadyExists)
						return
					}
					provider = &unstructured.Unstructured{}
					if err := json.NewDecoder(r.Body).Decode(provider); err != nil {
						t.Error(err)
					}
					_ = unstructured.SetNestedField(provider.Object, "provider-cluster", "spec", "cluster")
					_ = unstructured.SetNestedField(provider.Object, "Ready", "status", "phase")
					writes++
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(provider)
				default:
					t.Errorf("unexpected write %s %s", r.Method, r.URL.Path)
					status(500, metav1.StatusReasonInternalError)
				}
			}))
			defer srv.Close()
			b := &Bootstrapper{config: &rest.Config{Host: srv.URL}}
			cluster, err := b.EnsureOrgProviderWorkspace(context.Background(), "org-a", "planner")
			if scenario == "legacy" {
				if err == nil {
					t.Fatal("adopted legacy workspace")
				}
				if len(provider.GetAnnotations()) != 0 || writes != 0 {
					t.Fatal("legacy workspace was marked restricted")
				}
				return
			}
			if err != nil || cluster != "provider-cluster" {
				t.Fatalf("cluster=%q error=%v", cluster, err)
			}
			if provider.GetAnnotations()[providers.OrgProviderBootstrapPolicyAnnotation] != providers.OrgProviderBootstrapPolicy {
				t.Fatal("new workspace lacks trusted marker")
			}
			wantWrites := 0
			if scenario == "new" {
				wantWrites = 1
			}
			if writes != wantWrites {
				t.Fatalf("writes=%d want %d", writes, wantWrites)
			}
		})
	}
}
