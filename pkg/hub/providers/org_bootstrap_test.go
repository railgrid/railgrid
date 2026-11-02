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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkinstall "github.com/railgrid/provider-sdk/install"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
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
	paths, err := filepath.Glob(filepath.Join(root, "deploy/chart/files/schemas/*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		request.Schemas = append(request.Schemas, read(path))
	}
	return request
}

func TestOrgBootstrapAdmitsRealProviderAndOwnsRouting(t *testing.T) {
	request := quickstartBootstrap(t)
	request.APIExport.SetAnnotations(map[string]string{"internal.kcp.io/replicate": "true"})
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
	// An undeclared claim or export identity is never forwarded to the admin client.
	if err := unstructured.SetNestedSlice(request.APIExport.Object, []any{map[string]any{"group": "rbac.authorization.k8s.io", "resource": "clusterrolebindings", "verbs": []any{"*"}}}, "spec", "permissionClaims"); err != nil {
		t.Fatal(err)
	}
	admitted, err := validateOrgBootstrap("quickstart", "org", NewRegistry(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(admitted.APIExport.GetAnnotations()) != 0 || admitted.APIExport.Object["status"] != nil {
		t.Fatal("untrusted metadata/status escaped admission")
	}
	clean, _, _ := unstructured.NestedSlice(admitted.APIExport.Object, "spec", "resources")
	for _, raw := range clean {
		item := raw.(map[string]any)
		if item["name"] == "greetings/greet" {
			group, _, _ := unstructured.NestedString(item, "storage", "virtual", "reference", "apiGroup")
			name, _, _ := unstructured.NestedString(item, "storage", "virtual", "reference", "name")
			if group != "dataplane.railgrid.ai" || name != admitted.APIExport.GetName() {
				t.Fatal("caller chose endpoint reference")
			}
		}
	}
	claims, _, _ := unstructured.NestedSlice(admitted.APIExport.Object, "spec", "permissionClaims")
	for _, claim := range claims {
		if claim.(map[string]any)["resource"] == "clusterrolebindings" {
			t.Fatal("hidden permission claim admitted")
		}
	}
}

func TestOrgBootstrapRejectsControlPlaneEscapes(t *testing.T) {
	tests := map[string]func(*sdkinstall.OrganizationBootstrap){
		"private schema count": func(r *sdkinstall.OrganizationBootstrap) {
			r.PrivateCRDs = make([]*unstructured.Unstructured, 9)
		},
		"ordinary schema resource mismatch": func(r *sdkinstall.OrganizationBootstrap) {
			for _, s := range r.Schemas {
				plural, _, _ := unstructured.NestedString(s.Object, "spec", "names", "plural")
				if plural == "greetings" {
					_ = unstructured.SetNestedField(s.Object, "otherthings", "spec", "names", "plural")
				}
			}
		},
		"foreign claim without provider": func(r *sdkinstall.OrganizationBootstrap) {
			_ = unstructured.SetNestedSlice(r.CatalogEntry.Object, []any{map[string]any{"group": "edges.railgrid.ai", "resources": []any{map[string]any{"name": "linuxservers", "verbs": []any{"get", "list"}}}}}, "spec", "requires")
		},
		"control plane claim": func(r *sdkinstall.OrganizationBootstrap) {
			_ = unstructured.SetNestedSlice(r.CatalogEntry.Object, []any{map[string]any{"group": "rbac.authorization.k8s.io", "resources": []any{map[string]any{"name": "clusterrolebindings", "verbs": []any{"create"}}}}}, "spec", "requires")
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
		"ordinary virtual storage": func(r *sdkinstall.OrganizationBootstrap) {
			resources, _, _ := unstructured.NestedSlice(r.APIExport.Object, "spec", "resources")
			for _, raw := range resources {
				item := raw.(map[string]any)
				if item["name"] == "greetings" {
					item["storage"] = map[string]any{"virtual": map[string]any{}}
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
		"private endpoint storage": func(r *sdkinstall.OrganizationBootstrap) {
			r.PrivateCRDs = []*unstructured.Unstructured{privateBootstrapCRD("dataplane.railgrid.ai", "dataplaneendpointslices")}
		},
		"private conversion webhook": func(r *sdkinstall.OrganizationBootstrap) {
			crd := privateBootstrapCRD("private.example", "records")
			_ = unstructured.SetNestedMap(crd.Object, map[string]any{"strategy": "Webhook"}, "spec", "conversion")
			r.PrivateCRDs = []*unstructured.Unstructured{crd}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			request := quickstartBootstrap(t)
			change(&request)
			if _, err := validateOrgBootstrap("quickstart", "org", NewRegistry(), request); err == nil {
				t.Fatal("malicious declaration admitted")
			}
		})
	}
}

func TestOrgBootstrapRefusesDifferentExistingImmutableSchema(t *testing.T) {
	request := quickstartBootstrap(t)
	existing := request.Schemas[0].DeepCopy()
	_ = unstructured.SetNestedField(existing.Object, "other.example", "spec", "group")
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), existing)
	err := applyOrgBootstrap(context.Background(), client, nil, OrgBootstrapIdentity{}, request, "http://relay.invalid")
	if err == nil || !strings.Contains(err.Error(), "different contents") {
		t.Fatalf("existing different schema was silently reused: %v", err)
	}
	for _, action := range client.Actions() {
		if action.GetResource() != orgSchemaGVR {
			t.Fatalf("published objects after incompatible schema: %v", action)
		}
	}
}

func TestOrgBootstrapExportQuotaRefusalCannotAccumulateEndpoints(t *testing.T) {
	request := quickstartBootstrap(t)
	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	client.PrependReactor("create", orgCRDGVR.Resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
		crd := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		_ = unstructured.SetNestedSlice(crd.Object, []any{map[string]any{"type": "Established", "status": "True"}}, "status", "conditions")
		return false, nil, nil
	})
	client.PrependReactor("create", orgExportGVR.Resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(orgExportGVR.GroupResource(), request.APIExport.GetName(), fmt.Errorf("exceeded export quota"))
	})
	err := applyOrgBootstrap(context.Background(), client, nil, OrgBootstrapIdentity{}, request, "http://relay.invalid")
	if !apierrors.IsForbidden(err) {
		t.Fatalf("expected export quota rejection, got %v", err)
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "dataplaneendpointslices" {
			t.Fatalf("published an endpoint before admitting its export: %v", action)
		}
	}
	// Persisted schemas and the singleton CRD remain bounded; no export or
	// per-attempt endpoint survives the rejected new name.
	if _, err := client.Resource(orgExportGVR).Get(context.Background(), request.APIExport.GetName(), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("rejected export persisted: %v", err)
	}
}

func privateBootstrapCRD(group, resource string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{"name": resource + "." + group},
		"spec":     map[string]any{"group": group, "names": map[string]any{"plural": resource, "kind": "Record"}, "scope": "Namespaced", "versions": []any{map[string]any{"name": "v1", "served": true, "storage": true, "schema": map[string]any{"openAPIV3Schema": map[string]any{"type": "object"}}}}},
	}}
}
