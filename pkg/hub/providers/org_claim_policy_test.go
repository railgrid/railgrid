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
	"reflect"
	"testing"

	sdkinstall "github.com/railgrid/provider-sdk/install"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOrganizationClaimPolicyFollowsAdmittedManifest(t *testing.T) {
	declaration := sdkinstall.OrganizationBootstrap{
		CatalogEntry: &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
			"requires": []any{
				map[string]any{"provider": "code", "group": "code.railgrid.ai"},
				map[string]any{"group": "authorization.k8s.io"},
			},
		}}},
		APIExport: &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
			"resources": []any{
				map[string]any{"group": "custom.example.test", "name": "notes"},
				map[string]any{"group": "custom.example.test", "name": "notes/run"},
			},
			// Policies derive from declared requirements, never hidden export claims.
			"permissionClaims": []any{map[string]any{"group": "admin.kcp.io"}},
		}}},
	}
	policy, err := orgProviderClaimPolicy(OrgBootstrapIdentity{Cluster: "source1"}, declaration)
	if err != nil {
		t.Fatal(err)
	}
	claims, _, _ := unstructured.NestedSlice(policy.Object, "spec", "claims")
	want := []any{map[string]any{"claimer": "custom.example.test", "groups": []any{"code.railgrid.ai"}}}
	if !reflect.DeepEqual(claims, want) {
		t.Fatalf("claims = %#v, want %#v", claims, want)
	}
	if policy.GetLabels()[orgClaimPolicyOwnerLabel] != "source1" || policy.GetName() == orgClaimPolicyName("source2") {
		t.Fatal("policy was not bound to the exporter identity")
	}
	_ = unstructured.SetNestedSlice(declaration.CatalogEntry.Object, []any{map[string]any{"group": "authorization.k8s.io"}}, "spec", "requires")
	policy, err = orgProviderClaimPolicy(OrgBootstrapIdentity{Cluster: "source1"}, declaration)
	if err != nil || policy != nil {
		t.Fatalf("builtin-only claims need no dynamic policy: %v, %v", policy, err)
	}
}
