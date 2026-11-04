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

package providers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkinstall "github.com/railgrid/provider-sdk/install"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/yaml"
)

func quickstartBootstrap(t *testing.T) sdkinstall.OrganizationBootstrap {
	t.Helper()
	read := func(path string) *unstructured.Unstructured {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(raw, &out.Object); err != nil {
			t.Fatal(err)
		}
		return out
	}
	root := "../../../providers/quickstart"
	request := sdkinstall.OrganizationBootstrap{
		CatalogEntry: read(filepath.Join(root, "manifest.yaml")),
		APIExport:    read(filepath.Join(root, "deploy/chart/files/apiexport.yaml")),
	}
	_ = unstructured.SetNestedField(request.CatalogEntry.Object, "http://quickstart.railgrid-provider-quickstart.svc", "spec", "serving", "backend", "url")
	paths, err := filepath.Glob(filepath.Join(root, "deploy/chart/files/schemas/*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		request.Schemas = append(request.Schemas, read(path))
	}
	return request
}

func TestOrgBootstrapRebuildsExportStorageAndClaims(t *testing.T) {
	request := quickstartBootstrap(t)
	request.APIExport.SetAnnotations(map[string]string{"internal.kcp.io/replicate": "forged"})
	request.APIExport.Object["status"] = map[string]any{"identityHash": "forged"}
	resources, _, _ := unstructured.NestedSlice(request.APIExport.Object, "spec", "resources")
	for _, raw := range resources {
		item := raw.(map[string]any)
		if item["name"] == "greetings/greet" {
			item["storage"] = map[string]any{"virtual": map[string]any{"reference": map[string]any{"apiGroup": "evil.example", "kind": "InternalURL", "name": "metadata"}}}
		}
	}
	if err := unstructured.SetNestedSlice(request.APIExport.Object, resources, "spec", "resources"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(request.APIExport.Object, []any{map[string]any{"group": "rbac.authorization.k8s.io", "resource": "clusterrolebindings", "verbs": []any{"*"}}}, "spec", "permissionClaims"); err != nil {
		t.Fatal(err)
	}
	admitted, err := validateOrgBootstrap("quickstart", "org", NewRegistry(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(admitted.APIExport.GetAnnotations()) != 0 || admitted.APIExport.Object["status"] != nil {
		t.Fatal("untrusted APIExport metadata or status escaped admission")
	}
	clean, _, _ := unstructured.NestedSlice(admitted.APIExport.Object, "spec", "resources")
	for _, raw := range clean {
		item := raw.(map[string]any)
		if item["name"] == "greetings/greet" {
			group, _, _ := unstructured.NestedString(item, "storage", "virtual", "reference", "apiGroup")
			name, _, _ := unstructured.NestedString(item, "storage", "virtual", "reference", "name")
			if group != "dataplane.railgrid.ai" || name != admitted.APIExport.GetName() {
				t.Fatal("provider chose the custom-subresource endpoint reference")
			}
		}
	}
	claims, _, _ := unstructured.NestedSlice(admitted.APIExport.Object, "spec", "permissionClaims")
	for _, raw := range claims {
		if raw.(map[string]any)["resource"] == "clusterrolebindings" {
			t.Fatal("hidden permission claim was admitted")
		}
	}
}

func TestOrgBootstrapAdmitsOnlyLocalCacheProjection(t *testing.T) {
	request := quickstartBootstrap(t)
	request.PrivateCRDs = []*unstructured.Unstructured{privateBootstrapCRD("quickstart.providers.railgrid.ai", "greetings")}
	request.VirtualStorage = []sdkinstall.VirtualStorageResource{{Group: "quickstart.providers.railgrid.ai", Resource: "greetings", Name: "greetings-cache"}}
	admitted, err := validateOrgBootstrap("quickstart", "org", NewRegistry(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(admitted.VirtualStorage) != 1 || admitted.VirtualStorage[0].Version != "v1alpha1" {
		t.Fatalf("admitted cache projection=%+v", admitted.VirtualStorage)
	}
	resources, _, _ := unstructured.NestedSlice(admitted.APIExport.Object, "spec", "resources")
	for _, raw := range resources {
		item := raw.(map[string]any)
		if item["name"] != "greetings" {
			continue
		}
		group, _, _ := unstructured.NestedString(item, "storage", "virtual", "reference", "apiGroup")
		kind, _, _ := unstructured.NestedString(item, "storage", "virtual", "reference", "kind")
		name, _, _ := unstructured.NestedString(item, "storage", "virtual", "reference", "name")
		if group != "cache.kcp.io" || kind != "ClusterCachedResourceEndpointSlice" || name != "greetings-cache" {
			t.Fatalf("cache storage was not rebuilt locally: %#v", item["storage"])
		}
		return
	}
	t.Fatal("ordinary greetings resource not found")
}

func TestOrgBootstrapRejectsControlPlaneEscapes(t *testing.T) {
	tests := map[string]func(*sdkinstall.OrganizationBootstrap){
		"private CRD count": func(r *sdkinstall.OrganizationBootstrap) {
			r.PrivateCRDs = make([]*unstructured.Unstructured, OrgProviderPrivateResourceLimit+1)
		},
		"different provider": func(r *sdkinstall.OrganizationBootstrap) { r.CatalogEntry.SetName("other") },
		"different export":   func(r *sdkinstall.OrganizationBootstrap) { r.APIExport.SetName("other.example") },
		"missing schemas":    func(r *sdkinstall.OrganizationBootstrap) { r.Schemas = nil },
		"reserved schema group": func(r *sdkinstall.OrganizationBootstrap) {
			_ = unstructured.SetNestedField(r.Schemas[0].Object, "rbac.authorization.k8s.io", "spec", "group")
		},
		"schema conversion webhook": func(r *sdkinstall.OrganizationBootstrap) {
			_ = unstructured.SetNestedMap(r.Schemas[0].Object, map[string]any{"strategy": "Webhook", "webhook": map[string]any{"url": "https://127.0.0.1/admin"}}, "spec", "conversion")
		},
		"unlisted virtual storage": func(r *sdkinstall.OrganizationBootstrap) {
			resources, _, _ := unstructured.NestedSlice(r.APIExport.Object, "spec", "resources")
			for _, raw := range resources {
				item := raw.(map[string]any)
				if item["name"] == "greetings" {
					item["storage"] = map[string]any{"virtual": map[string]any{"reference": map[string]any{"apiGroup": "evil.example", "kind": "InternalURL", "name": "metadata"}}}
				}
			}
			_ = unstructured.SetNestedSlice(r.APIExport.Object, resources, "spec", "resources")
		},
		"undeclared verb": func(r *sdkinstall.OrganizationBootstrap) {
			resources, _, _ := unstructured.NestedSlice(r.APIExport.Object, "spec", "resources")
			for _, raw := range resources {
				item := raw.(map[string]any)
				if item["name"] == "greetings/greet" {
					item["name"] = "greetings/backdoor"
				}
			}
			_ = unstructured.SetNestedSlice(r.APIExport.Object, resources, "spec", "resources")
		},
		"private endpoint group": func(r *sdkinstall.OrganizationBootstrap) {
			r.PrivateCRDs = []*unstructured.Unstructured{privateBootstrapCRD("dataplane.railgrid.ai", "dataplaneendpointslices")}
		},
		"private conversion webhook": func(r *sdkinstall.OrganizationBootstrap) {
			crd := privateBootstrapCRD("private.example", "records")
			_ = unstructured.SetNestedMap(crd.Object, map[string]any{"strategy": "Webhook"}, "spec", "conversion")
			r.PrivateCRDs = []*unstructured.Unstructured{crd}
		},
		"invalid backend URL": func(r *sdkinstall.OrganizationBootstrap) {
			_ = unstructured.SetNestedField(r.CatalogEntry.Object, "https://example.com/admin", "spec", "serving", "backend", "url")
		},
		"private schema projection lacks CRD": func(r *sdkinstall.OrganizationBootstrap) {
			r.VirtualStorage = []sdkinstall.VirtualStorageResource{{Group: "quickstart.providers.railgrid.ai", Resource: "greetings", Name: "greetings-cache"}}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			request := quickstartBootstrap(t)
			change(&request)
			if _, err := validateOrgBootstrap("quickstart", "org", NewRegistry(), request); err == nil {
				t.Fatal("unsafe declaration admitted")
			}
		})
	}
}

func TestOrgBootstrapRejectsGroupOwnedByAnotherVisibleProvider(t *testing.T) {
	registry := NewRegistry()
	registry.Upsert(Provider{OrgUUID: "org", Name: "planner", APIGroups: []string{"quickstart.providers.railgrid.ai"}})
	request := quickstartBootstrap(t)
	if _, err := validateOrgBootstrap("quickstart", "org", registry, request); err == nil || !strings.Contains(err.Error(), "already served") {
		t.Fatalf("foreign group ownership was not refused: %v", err)
	}
}

func TestOrgBootstrapRefusesDifferentExistingImmutableSchema(t *testing.T) {
	request := quickstartBootstrap(t)
	existing := request.Schemas[0].DeepCopy()
	_ = unstructured.SetNestedField(existing.Object, "other.example", "spec", "group")
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), existing)
	err := applyOrgBootstrap(context.Background(), client, NewProvisioner(nil), OrgBootstrapIdentity{}, request, "http://relay.invalid")
	if err == nil || !strings.Contains(err.Error(), "different contents") {
		t.Fatalf("existing different schema was silently reused: %v", err)
	}
	for _, action := range client.Actions() {
		if action.GetResource() != orgSchemaGVR {
			t.Fatalf("published objects after incompatible schema: %v", action)
		}
	}
}

func TestOrgBootstrapReapplyPreservesKCPMetadataAndRevokesOldClaims(t *testing.T) {
	ctx := context.Background()
	name := "planner.providers.railgrid.ai"
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIExport",
		"metadata": map[string]any{
			"name":        name,
			"annotations": map[string]any{"internal.kcp.io/replicate": "true"},
		},
		"spec": map[string]any{
			"permissionClaims": []any{map[string]any{
				"group": "legacy.example", "resource": "secrets", "verbs": []any{"get"},
			}},
		},
		"status": map[string]any{"phase": "Ready"},
	}}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), existing)
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIExport",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"permissionClaims": []any{map[string]any{
				"group": "authorization.k8s.io", "resource": "subjectaccessreviews", "verbs": []any{"create"},
			}},
		},
	}}
	for attempt := 0; attempt < 2; attempt++ {
		if err := applyOrgObject(ctx, client, orgExportGVR, desired); err != nil {
			t.Fatalf("bootstrap apply %d: %v", attempt+1, err)
		}
	}
	got, err := client.Resource(orgExportGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetAnnotations()["internal.kcp.io/replicate"] != "true" {
		t.Fatalf("bootstrap update removed KCP replication annotation: %#v", got.GetAnnotations())
	}
	if phase, _, _ := unstructured.NestedString(got.Object, "status", "phase"); phase != "Ready" {
		t.Fatalf("bootstrap reapply removed server status: %q", phase)
	}
	claims, _, _ := unstructured.NestedSlice(got.Object, "spec", "permissionClaims")
	if len(claims) != 1 {
		t.Fatalf("permission claims=%#v, want only the current declaration", claims)
	}
	claim := claims[0].(map[string]any)
	if claim["group"] != "authorization.k8s.io" || claim["resource"] != "subjectaccessreviews" {
		t.Fatalf("stale permission claim survived bootstrap reapply: %#v", claim)
	}
}

func privateBootstrapCRD(group, resource string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{"name": resource + "." + group},
		"spec": map[string]any{
			"group":    group,
			"names":    map[string]any{"plural": resource, "kind": "Record"},
			"scope":    "Cluster",
			"versions": []any{map[string]any{"name": "v1alpha1", "served": true, "storage": true, "schema": map[string]any{"openAPIV3Schema": map[string]any{"type": "object"}}}},
		},
	}}
}

func TestOrgBootstrapRejectsUnsupportedScopeAndVersion(t *testing.T) {
	for _, scenario := range []string{"namespaced cache", "namespaced verb parent", "unserved catalog version"} {
		t.Run(scenario, func(t *testing.T) {
			request := quickstartBootstrap(t)
			switch scenario {
			case "namespaced cache":
				crd := privateBootstrapCRD("quickstart.providers.railgrid.ai", "greetings")
				_ = unstructured.SetNestedField(crd.Object, "Namespaced", "spec", "scope")
				request.PrivateCRDs = []*unstructured.Unstructured{crd}
				request.VirtualStorage = []sdkinstall.VirtualStorageResource{{Group: "quickstart.providers.railgrid.ai", Resource: "greetings", Name: "greetings-cache"}}
			case "namespaced verb parent":
				for _, shape := range request.Schemas {
					plural, _, _ := unstructured.NestedString(shape.Object, "spec", "names", "plural")
					if plural == "greetings" {
						_ = unstructured.SetNestedField(shape.Object, "Namespaced", "spec", "scope")
					}
				}
			case "unserved catalog version":
				resources, _, _ := unstructured.NestedSlice(request.CatalogEntry.Object, "spec", "export", "resources")
				resources[0].(map[string]any)["apiVersion"] = "quickstart.providers.railgrid.ai/v999"
				_ = unstructured.SetNestedSlice(request.CatalogEntry.Object, resources, "spec", "export", "resources")
			}
			if _, err := validateOrgBootstrap("quickstart", "org", NewRegistry(), request); err == nil {
				t.Fatal("unsupported declaration accepted")
			}
		})
	}
	// A namespaced private CRD is supported when it is not a cache projection.
	request := quickstartBootstrap(t)
	crd := privateBootstrapCRD("private.example", "records")
	_ = unstructured.SetNestedField(crd.Object, "Namespaced", "spec", "scope")
	request.PrivateCRDs = []*unstructured.Unstructured{crd}
	if _, err := validateOrgBootstrap("quickstart", "org", NewRegistry(), request); err != nil {
		t.Fatal("ordinary namespaced private CRD:", err)
	}
}
