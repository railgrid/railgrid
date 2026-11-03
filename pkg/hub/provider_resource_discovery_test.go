// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/actionproof"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

func TestProviderResourceDiscoveryBoundaries(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "databricks.railgrid.ai", Version: "v1alpha1", Resource: "tables"}
	for _, tc := range []struct {
		name                      string
		proof, bound, allowed     bool
		resource, version, method string
		status                    int
	}{
		{name: "allowed metadata only", proof: true, bound: true, allowed: true, status: 200},
		{name: "missing caller proof", bound: true, allowed: true, status: 401},
		{name: "unbound provider", proof: true, allowed: true, status: 403},
		{name: "caller cannot list", proof: true, bound: true, status: 403},
		{name: "cannot enumerate secrets", proof: true, bound: true, allowed: true, resource: "secrets", status: 404},
		{name: "cannot select another API", proof: true, bound: true, allowed: true, version: "other.railgrid.ai/v1", status: 404},
		{name: "no writes", proof: true, bound: true, allowed: true, method: http.MethodPost, status: 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := providers.NewRegistry()
			registry.Upsert(providers.Provider{Name: "databricks", EndpointsValid: true, Export: &providersv1alpha1.ProviderExport{
				Name: "databricks.railgrid.ai", Resources: []providersv1alpha1.ProviderExportResource{{
					Name: "tables", APIVersion: "databricks.railgrid.ai/v1alpha1", Kind: "Table",
					Actions: []providersv1alpha1.ProviderAction{{Name: "query-table", Version: "v1"}},
				}},
			}})
			dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "TableList"}, &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "databricks.railgrid.ai/v1alpha1", "kind": "Table",
				"metadata": map[string]any{"name": "orders", "uid": "table-uid", "resourceVersion": "42", "annotations": map[string]any{"private": "must-not-escape"}},
				"spec":     map[string]any{"credential": "must-not-escape"}, "status": map[string]any{"sample": "must-not-escape"},
			}})
			listCalls := 0
			dyn.PrependReactor("list", "tables", func(ktesting.Action) (bool, runtime.Object, error) {
				listCalls++
				if !tc.allowed {
					return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("private transport detail"))
				}
				return false, nil, nil
			})
			h := &providerResourceDiscovery{
				registry: registry,
				verify: func(context.Context, *http.Request) (actionproof.Claims, error) {
					if !tc.proof {
						return actionproof.Claims{}, errors.New("invalid proof")
					}
					return actionproof.Claims{OrgUUID: "org", ClusterID: "tenant1", RBACIdentity: "alice"}, nil
				},
				bound: func(_ context.Context, cluster string, provider providers.Provider) (bool, error) {
					if cluster != "tenant1" || provider.Name != "databricks" {
						t.Fatal("scope changed")
					}
					return tc.bound, nil
				},
				client: func(c actionproof.Claims) (dynamic.Interface, error) {
					if c.ClusterID != "tenant1" || c.RBACIdentity != "alice" {
						t.Fatal("unverified read identity")
					}
					return dyn, nil
				},
			}
			resource, version, method := tc.resource, tc.version, tc.method
			if resource == "" {
				resource = "tables"
			}
			if version == "" {
				version = "databricks.railgrid.ai/v1alpha1"
			}
			if method == "" {
				method = http.MethodGet
			}
			r := mux.SetURLVars(httptest.NewRequest(method, "/api/providers/databricks/resources/"+resource+"?apiVersion="+version, nil), map[string]string{"provider": "databricks", "resource": resource})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "must-not-escape") || strings.Contains(w.Body.String(), "private transport detail") {
				t.Fatal("sensitive resource or error details escaped")
			}
			if tc.status == 200 && !strings.Contains(w.Body.String(), `"name":"orders"`) {
				t.Fatal("missing discoverable resource")
			}
			if tc.status != 200 && tc.name != "caller cannot list" && listCalls != 0 {
				t.Fatal("resource read occurred before authorization")
			}
		})
	}
}

func TestProviderCatalogRequiresProofAndProviderAuthentication(t *testing.T) {
	for _, valid := range []bool{true, false} {
		human := func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Error("proof request fell back to human authentication")
				w.WriteHeader(500)
			})
		}
		h := providerCatalogMiddleware(human, func(*http.Request) (string, string, error) {
			t.Fatal("proof request fell back to workload token")
			return "", "", nil
		}, func(context.Context, *http.Request) (actionproof.Claims, error) {
			if !valid {
				return actionproof.Claims{}, errors.New("provider bearer rejected")
			}
			return actionproof.Claims{UserID: "alice", OrgUUID: "org", WorkspaceUUID: "ws"}, nil
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
		r := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
		r.Header.Set(actionproof.Header, "signed-proof")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 401
		if valid {
			want = 204
		}
		if w.Code != want {
			t.Fatalf("status %d want %d", w.Code, want)
		}
	}
}

func TestProviderResourceDiscoveryUsesActualBoundCopy(t *testing.T) {
	const providerName = "databricks"
	gvr := schema.GroupVersionResource{Group: "databricks.railgrid.ai", Version: "v1alpha1", Resource: "tables"}
	for _, tc := range []struct {
		name, owner, issuer string
		orgReady            bool
		mutateBinding       func(*unstructured.Unstructured)
		status              int
		kind                string
	}{
		{name: "platform binding under ready org shadow", issuer: "platform-provider", orgReady: true, status: 200, kind: "PlatformTable"},
		{name: "platform binding under unready org shadow", issuer: "platform-provider", status: 200, kind: "PlatformTable"},
		{name: "actual org binding", owner: "org", issuer: "org-provider", orgReady: true, status: 200, kind: "OrgTable"},
		{name: "actual org unready never falls back", owner: "org", issuer: "org-provider", status: 503},
		{name: "stale platform identity never falls back", issuer: "replacement", orgReady: true, status: 403},
		{name: "missing resolved identity", orgReady: true, status: 403},
		{name: "foreign org never selected", owner: "foreign", issuer: "foreign-provider", orgReady: true, status: 403},
		{name: "invalid native binding", issuer: "platform-provider", orgReady: true, status: 403, mutateBinding: func(b *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(b.Object, []any{map[string]any{"type": "APIExportValid", "status": "False"}}, "status", "conditions")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := providers.NewRegistry()
			entry := func(org, issuer, path, kind string, ready bool) providers.Provider {
				return providers.Provider{Name: providerName, OrgUUID: org, WorkspaceCluster: issuer, APIExportName: "databricks.railgrid.ai", APIExportPath: path, EndpointsValid: ready, Export: &providersv1alpha1.ProviderExport{Name: "databricks.railgrid.ai", Resources: []providersv1alpha1.ProviderExportResource{{Name: "tables", APIVersion: "databricks.railgrid.ai/v1alpha1", Kind: kind, Actions: []providersv1alpha1.ProviderAction{{Name: "query-table", Version: "v1"}}}}}}
			}
			registry.Upsert(entry("", "platform-provider", "root:railgrid:providers:databricks", "PlatformTable", true))
			registry.Upsert(entry("org", "org-provider", "root:railgrid:tenants:org:providers:databricks", "OrgTable", tc.orgReady))
			registry.Upsert(entry("foreign", "foreign-provider", "root:railgrid:tenants:foreign:providers:databricks", "ForeignTable", true))
			path := "root:railgrid:providers:databricks"
			if tc.owner != "" {
				path = "root:railgrid:tenants:" + tc.owner + ":providers:databricks"
			}
			binding := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": "databricks"}, "spec": map[string]any{"reference": map[string]any{"export": map[string]any{"name": "databricks.railgrid.ai", "path": path}}}, "status": map[string]any{"phase": "Bound", "apiExportClusterName": tc.issuer}}}
			if tc.mutateBinding != nil {
				tc.mutateBinding(binding)
			}
			admin := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}: "APIBindingList"}, binding)
			bridge := &providerActionProofBridge{clients: func(cluster string) (dynamic.Interface, error) {
				if cluster != "tenant1" {
					t.Fatalf("wrong native tenant %s", cluster)
				}
				return admin, nil
			}}
			callerReads := 0
			callerClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "TableList"})
			callerClient.PrependReactor("list", "tables", func(ktesting.Action) (bool, runtime.Object, error) {
				callerReads++
				return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{{Object: map[string]any{"apiVersion": "databricks.railgrid.ai/v1alpha1", "kind": "Table", "metadata": map[string]any{"name": "orders", "uid": "orders-uid"}}}}}, nil
			})
			h := &providerResourceDiscovery{registry: registry, bound: bridge.bound, verify: func(context.Context, *http.Request) (actionproof.Claims, error) {
				return actionproof.Claims{OrgUUID: "org", ClusterID: "tenant1", RBACIdentity: "alice"}, nil
			}, client: func(c actionproof.Claims) (dynamic.Interface, error) {
				if c.RBACIdentity != "alice" {
					t.Fatal("caller identity changed")
				}
				return callerClient, nil
			}}
			request := mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/api/providers/databricks/resources/tables?apiVersion=databricks.railgrid.ai/v1alpha1", nil), map[string]string{"provider": "databricks", "resource": "tables"})
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.status == 200 {
				var body struct {
					Kind  string
					Items []discoveredProviderResource
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Kind != tc.kind || len(body.Items) != 1 || body.Items[0].Metadata.Name != "orders" || callerReads != 1 {
					t.Fatalf("wrong bound contract/read: %#v reads=%d", body, callerReads)
				}
			} else if callerReads != 0 {
				t.Fatal("caller resource read before native binding checks")
			}
		})
	}
}
