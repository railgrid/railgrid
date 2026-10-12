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

package kcp

import (
	"context"
	"reflect"
	"testing"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

// The embedded core export is the single source for the hub's own binding
// claims: every claim comes through Accepted, the tenancy placeholder is
// filled, and the apibindings claim that lets the hub watch tenant bindings is
// among them.
func TestCoreBindingClaimsComeFromTheEmbeddedExport(t *testing.T) {
	b := &Bootstrapper{workspaceIdentityHash: "abc123"}
	claims, err := b.coreBindingClaims()
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]apisv1alpha2.AcceptablePermissionClaim{}
	for _, c := range claims {
		if c.State != apisv1alpha2.ClaimAccepted {
			t.Errorf("claim %s/%s is %s, want Accepted", c.Group, c.Resource, c.State)
		}
		byKey[c.Group+"/"+c.Resource] = c
	}
	ab, ok := byKey["apis.kcp.io/apibindings"]
	if !ok {
		t.Fatalf("core export does not claim apis.kcp.io/apibindings; the binding-claims reconciler cannot see tenant bindings without it: %v", byKey)
	}
	if !reflect.DeepEqual(ab.Verbs, []string{"get", "list", "watch"}) || !ab.Selector.MatchAll {
		t.Errorf("apibindings claim = %+v, want get/list/watch matchAll", ab)
	}
	ws, ok := byKey["tenancy.kcp.io/workspaces"]
	if !ok || ws.IdentityHash != "abc123" {
		t.Errorf("tenancy claim identity = %+v, want the resolved hash", ws)
	}
	if _, ok := byKey["/secrets"]; !ok {
		t.Errorf("core secrets claim missing: %v", byKey)
	}
}

func TestCoreBindingClaimsNeedTheIdentityHash(t *testing.T) {
	if _, err := (&Bootstrapper{}).coreBindingClaims(); err == nil {
		t.Fatal("expected an error before Bootstrap resolved the tenancy identity hash")
	}
}

func TestMergeBindingClaims(t *testing.T) {
	narrow := apisv1alpha2.PermissionClaimSelector{LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"railgrid.ai/owner": "agents"}}}
	current := []apisv1alpha2.AcceptablePermissionClaim{
		// Accepted at Enable with the wide selector of the day.
		{ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{GroupResource: apisv1alpha2.GroupResource{Resource: "secrets"}, Verbs: []string{"get"}},
			Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		}, State: apisv1alpha2.ClaimAccepted},
		// Unticked at Enable.
		{ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{GroupResource: apisv1alpha2.GroupResource{Resource: "configmaps"}, Verbs: []string{"get"}},
			Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		}, State: apisv1alpha2.ClaimRejected},
		// No longer declared.
		{ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{GroupResource: apisv1alpha2.GroupResource{Group: "old.railgrid.ai", Resource: "things"}, Verbs: []string{"get"}},
			Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		}, State: apisv1alpha2.ClaimAccepted},
	}
	declared := []ProviderClaim{
		{Resource: "secrets", Verbs: []string{"get", "list"}, Accepted: true, MatchLabels: map[string]string{"railgrid.ai/owner": "agents"}},
		{Resource: "configmaps", Verbs: []string{"get"}, Accepted: true},
		{Group: "authentication.k8s.io", Resource: "tokenreviews", Verbs: []string{"create"}, Accepted: true},
		{Group: "code.railgrid.ai", Resource: "repositories", Verbs: []string{"get"}, Accepted: false, MatchLabels: map[string]string{"railgrid.ai/owner": "agents"}},
	}

	got := mergeBindingClaims(current, declared)

	want := []apisv1alpha2.AcceptablePermissionClaim{
		// New verbs taken, selector KEPT (kcp makes it immutable), still Accepted.
		{ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{GroupResource: apisv1alpha2.GroupResource{Resource: "secrets"}, Verbs: []string{"get", "list"}},
			Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		}, State: apisv1alpha2.ClaimAccepted},
		// Explicit rejection is never overturned.
		{ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{GroupResource: apisv1alpha2.GroupResource{Resource: "configmaps"}, Verbs: []string{"get"}},
			Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		}, State: apisv1alpha2.ClaimRejected},
		// New claim: Accepted with the declared scope.
		{ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{GroupResource: apisv1alpha2.GroupResource{Group: "authentication.k8s.io", Resource: "tokenreviews"}, Verbs: []string{"create"}},
			Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		}, State: apisv1alpha2.ClaimAccepted},
		// Unconsented composition: present, Rejected, with its declared scope.
		{ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{GroupResource: apisv1alpha2.GroupResource{Group: "code.railgrid.ai", Resource: "repositories"}, Verbs: []string{"get"}},
			Selector:        narrow,
		}, State: apisv1alpha2.ClaimRejected},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeBindingClaims =\n%+v\nwant\n%+v", got, want)
	}
}

// A Rejected the hub wrote as a consent placeholder must give way once the
// Grant records consent, by whatever path consent arrived; a Rejected on a
// provider's own claim is the tenant's and stays.
func TestMergeBindingClaimsConsentOverridesHeldRejection(t *testing.T) {
	held := []apisv1alpha2.AcceptablePermissionClaim{
		{ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{GroupResource: apisv1alpha2.GroupResource{Group: "code.railgrid.ai", Resource: "repositories"}, Verbs: []string{"get"}},
			Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		}, State: apisv1alpha2.ClaimRejected},
		{ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{GroupResource: apisv1alpha2.GroupResource{Resource: "configmaps"}, Verbs: []string{"get"}},
			Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		}, State: apisv1alpha2.ClaimRejected},
	}
	got := mergeBindingClaims(held, []ProviderClaim{
		{Group: "code.railgrid.ai", Resource: "repositories", Verbs: []string{"get"}, Accepted: true, Consented: true},
		{Resource: "configmaps", Verbs: []string{"get"}, Accepted: true},
	})
	if got[0].State != apisv1alpha2.ClaimAccepted {
		t.Fatalf("consented composition stayed %s", got[0].State)
	}
	if got[1].State != apisv1alpha2.ClaimRejected {
		t.Fatalf("tenant's rejection of an own claim was overturned: %s", got[1].State)
	}
}

func TestReacceptClaimsOnBindingWritesOnlyWhenNeeded(t *testing.T) {
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{apiBindingGVR: "APIBindingList"})
	existing := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]interface{}{"name": "agents", "labels": map[string]interface{}{"keep": "me"}},
		"spec": map[string]interface{}{
			"reference": map[string]interface{}{"export": map[string]interface{}{"path": "root:railgrid:providers:agents", "name": "agents.railgrid.ai"}},
			"permissionClaims": []interface{}{map[string]interface{}{
				"resource": "secrets", "verbs": []interface{}{"get"}, "selector": map[string]interface{}{"matchAll": true}, "state": "Accepted",
			}},
			"somethingNewer": "kept",
		},
	}}
	if _, err := dyn.Resource(apiBindingGVR).Create(context.Background(), existing, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	claims := []ProviderClaim{
		{Resource: "secrets", Verbs: []string{"get"}, Accepted: true},
		{Group: "authentication.k8s.io", Resource: "tokenreviews", Verbs: []string{"create"}, Accepted: true},
	}
	changed, err := reacceptClaimsOnBinding(context.Background(), dyn, "agents", claims)
	if err != nil || !changed {
		t.Fatalf("first pass: changed=%v err=%v, want true/nil", changed, err)
	}
	got, _ := dyn.Resource(apiBindingGVR).Get(context.Background(), "agents", metav1.GetOptions{})
	specClaims, _, _ := unstructured.NestedSlice(got.Object, "spec", "permissionClaims")
	if len(specClaims) != 2 {
		t.Fatalf("claims after merge = %v", specClaims)
	}
	if v, _, _ := unstructured.NestedString(got.Object, "spec", "somethingNewer"); v != "kept" {
		t.Fatalf("unknown spec field dropped by the rewrite: %v", got.Object["spec"])
	}
	if got.GetLabels()["keep"] != "me" {
		t.Fatalf("metadata disturbed: %v", got.GetLabels())
	}
	changed, err = reacceptClaimsOnBinding(context.Background(), dyn, "agents", claims)
	if err != nil || changed {
		t.Fatalf("second pass: changed=%v err=%v, want false/nil", changed, err)
	}
}

func TestBindingClaimState(t *testing.T) {
	item := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"permissionClaims": []interface{}{
				map[string]interface{}{"resource": "secrets", "state": "Accepted"},
				map[string]interface{}{"group": "code.railgrid.ai", "resource": "repositories", "state": "Rejected"},
			},
		},
		"status": map[string]interface{}{
			"exportPermissionClaims": []interface{}{
				map[string]interface{}{"resource": "secrets"},
				map[string]interface{}{"group": "code.railgrid.ai", "resource": "repositories"},
				map[string]interface{}{"group": "authentication.k8s.io", "resource": "tokenreviews"},
			},
			"conditions": []interface{}{
				map[string]interface{}{"type": "PermissionClaimsValid", "status": "False", "reason": "PermissionClaimsMismatch", "message": "secrets. (selector differs from defaultSelector)"},
			},
		},
	}}
	got := bindingClaimState(item)
	want := BindingClaimState{
		Pending:  []ClaimRef{{Group: "authentication.k8s.io", Resource: "tokenreviews"}},
		Rejected: []ClaimRef{{Group: "code.railgrid.ai", Resource: "repositories"}},
		Mismatch: "secrets. (selector differs from defaultSelector)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bindingClaimState = %+v, want %+v", got, want)
	}

	clean := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec":   map[string]interface{}{"permissionClaims": []interface{}{map[string]interface{}{"resource": "secrets", "state": "Accepted"}}},
		"status": map[string]interface{}{"exportPermissionClaims": []interface{}{map[string]interface{}{"resource": "secrets"}}, "conditions": []interface{}{map[string]interface{}{"type": "PermissionClaimsValid", "status": "True"}}},
	}}
	if got := bindingClaimState(clean); !reflect.DeepEqual(got, BindingClaimState{}) {
		t.Fatalf("clean binding reports %+v", got)
	}
}
