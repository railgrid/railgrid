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
	"encoding/json"
	"fmt"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	rbacvalidation "k8s.io/component-helpers/auth/rbac/validation"
)

func TestOrgProviderRoleAllowsRuntimeWithoutBootstrapAuthority(t *testing.T) {
	raw, err := json.Marshal(orgProviderClusterRoleRules("planner"))
	if err != nil {
		t.Fatal(err)
	}
	var rules []rbacv1.PolicyRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		group, resource, verb, name string
		allowed                     bool
	}{
		{"core.kcp.io", "logicalclusters", "get", "cluster", true},
		{"apis.kcp.io", "apiexports", "get", "planner.example.com", true},
		{"apis.kcp.io", "apiexports/content", "create", "planner.example.com", true},
		{"providers.railgrid.ai", "catalogentries", "get", "planner", true},
		{"providers.railgrid.ai", "catalogentries", "get", "other", false},
		{"coordination.k8s.io", "leases", "create", "leader", true},
		{"authorization.k8s.io", "subjectaccessreviews", "create", "", true},
		{"apis.kcp.io", "apiexports", "update", "planner.example.com", false},
		{"apis.kcp.io", "apiexports", "bind", "other", false},
		{"apis.kcp.io", "apibindings", "create", "other", false},
		{"apis.kcp.io", "apiresourceschemas", "create", "schema", false},
		{"apiextensions.k8s.io", "customresourcedefinitions", "create", "anything", false},
		{"rbac.authorization.k8s.io", "clusterroles", "create", "anything", false},
		{"rbac.authorization.k8s.io", "clusterroles", "escalate", "anything", false},
		{"rbac.authorization.k8s.io", "rolebindings", "create", "anything", false},
		{"", "serviceaccounts", "update", "provider", false},
		{"", "serviceaccounts/token", "create", "provider", false},
		{"", "users", "impersonate", "anyone", false},
		{"", "events", "create", "anything", false},
		{"", "resourcequotas", "update", "anything", false},
		{"core.kcp.io", "logicalclusters", "update", "cluster", false},
		{"dataplane.railgrid.ai", "dataplaneendpointslices/status", "update", "planner.example.com", false},
		{"providers.railgrid.ai", "catalogentries", "update", "planner", false},
		{"tenant.example.com", "privateobjects", "create", "anything", false},
	} {
		t.Run(tc.group+"/"+tc.resource+"/"+tc.verb+"/"+tc.name, func(t *testing.T) {
			wanted := rbacv1.PolicyRule{APIGroups: []string{tc.group}, Resources: []string{tc.resource}, Verbs: []string{tc.verb}}
			if tc.name != "" {
				wanted.ResourceNames = []string{tc.name}
			}
			allowed, _ := rbacvalidation.Covers(rules, []rbacv1.PolicyRule{wanted})
			if allowed != tc.allowed {
				t.Fatalf("allowed=%t want %t", allowed, tc.allowed)
			}
		})
	}
}

func TestOrgProviderSAIsRestrictedAndLegacyIdentityIsRefused(t *testing.T) {
	ctx := context.Background()
	cl := newRBACDynamicFake()
	mockOrgQuotaController(cl.(*dynamicfake.FakeDynamicClient))
	if err := ensureOrgProviderSA(ctx, cl, "planner"); err != nil {
		t.Fatal(err)
	}
	if err := ensureOrgProviderSA(ctx, cl, "planner"); err != nil {
		t.Fatalf("idempotent ensure: %v", err)
	}
	binding, err := cl.Resource(clusterRoleBindingGVR).Get(ctx, providerSABindingName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if role, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name"); role != OrgProviderClusterRoleName {
		t.Fatalf("bound role=%q", role)
	}
	sa, err := cl.Resource(serviceAccountGVR).Namespace(ProviderSANamespace).Get(ctx, ProviderSAName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sa.SetAnnotations(nil)
	if _, err := cl.Resource(serviceAccountGVR).Namespace(ProviderSANamespace).Update(ctx, sa, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureOrgProviderSA(ctx, cl, "planner"); err == nil {
		t.Fatal("unmarked legacy service account was adopted")
	}
}

func TestOrgPrivateResourceValidationReservesAuthorityGroups(t *testing.T) {
	for _, group := range []string{"", "*", "rbac.authorization.k8s.io", "apis.kcp.io", "cache.kcp.io", "tenancy.kcp.io", "apiextensions.k8s.io", "dataplane.railgrid.ai", "providers.railgrid.ai", "tenants.railgrid.ai", "admin.railgrid.ai", "railgrid.ai"} {
		if err := ValidateOrgProviderPrivateResource(schema.GroupResource{Group: group, Resource: "objects"}); err == nil {
			t.Errorf("admitted reserved group %q", group)
		}
	}
	for _, resource := range []string{"*", "objects/status", "../secrets"} {
		if err := ValidateOrgProviderPrivateResource(schema.GroupResource{Group: "planner.example.com", Resource: resource}); err == nil {
			t.Errorf("admitted invalid resource %q", resource)
		}
	}
	if err := ValidateOrgProviderPrivateResource(schema.GroupResource{Group: "planner.example.com", Resource: "workitems"}); err != nil {
		t.Fatal(err)
	}
}

// Simulate the quota controller only; the production helper must wait for this
// observation before granting a runtime identity access.
func mockOrgQuotaController(cl *dynamicfake.FakeDynamicClient) {
	cl.PrependReactor("create", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		object := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, object.GetName(), fmt.Errorf("exceeded quota: %s", object.GetName()))
	})
	cl.PrependReactor("get", "resourcequotas", func(action k8stesting.Action) (bool, runtime.Object, error) {
		name := action.(k8stesting.GetAction).GetName()
		object, err := cl.Tracker().Get(orgQuotaGVR, action.GetNamespace(), name)
		if err != nil {
			return true, nil, err
		}
		quota := object.(*unstructured.Unstructured)
		hard, _, _ := unstructured.NestedStringMap(quota.Object, "spec", "hard")
		used := map[string]string{}
		for key := range hard {
			used[key] = "0"
		}
		_ = unstructured.SetNestedStringMap(quota.Object, hard, "status", "hard")
		_ = unstructured.SetNestedStringMap(quota.Object, used, "status", "used")
		return true, quota, nil
	})
}

func TestOrgProviderQuotaProtectsClusterScopedAndFormerPrivateResources(t *testing.T) {
	cl := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
	mockOrgQuotaController(cl)
	ctx := context.Background()
	for _, resources := range [][]schema.GroupResource{{{Group: "planner.example", Resource: "receipts"}}, {{Group: "planner.example", Resource: "runs"}}, nil} {
		if err := ensureOrgProviderQuota(ctx, cl, resources); err != nil {
			t.Fatal(err)
		}
	}
	quota, err := cl.Resource(orgQuotaGVR).Namespace("admin").Get(ctx, orgQuotaName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if quota.GetAnnotations()["experimental.quota.kcp.io/cluster-scoped"] != "true" {
		t.Fatal("quota does not cover whole workspace")
	}
	hard, _, _ := unstructured.NestedStringMap(quota.Object, "spec", "hard")
	for key, want := range map[string]string{"count/secrets": "64", "count/configmaps": "64", "count/leases.coordination.k8s.io": "32", "count/apiresourceschemas.apis.kcp.io": "64", "count/customresourcedefinitions.apiextensions.k8s.io": "9", "count/receipts.planner.example": "32", "count/runs.planner.example": "32"} {
		if hard[key] != want {
			t.Errorf("quota %s=%s want %s", key, hard[key], want)
		}
	}
}

func TestOrgProviderQuotaDoesNotGrantCredentialBeforeControllerObservation(t *testing.T) {
	cl := newRBACDynamicFake()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ensureOrgProviderSA(ctx, cl, "planner"); err == nil {
		t.Fatal("credential provisioned without observed quota")
	}
	if _, err := cl.Resource(serviceAccountGVR).Namespace(ProviderSANamespace).Get(context.Background(), ProviderSAName, metav1.GetOptions{}); err == nil {
		t.Fatal("service account created before quota was observed")
	}
}

func TestOrgProviderAdmissionProbeRejectsDisabledQuotaAndCleansUp(t *testing.T) {
	cl := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
	mockOrgQuotaController(cl)
	cl.PrependReactor("create", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, action.(k8stesting.CreateAction).GetObject(), nil
	})
	if err := verifyOrgQuotaAdmission(context.Background(), cl); err == nil {
		t.Fatal("accepted disabled quota admission")
	}
	var created, deleted string
	for _, action := range cl.Actions() {
		if action.GetResource() != orgQuotaGVR {
			continue
		}
		switch action.GetVerb() {
		case "create":
			created = action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).GetName()
		case "delete":
			deleted = action.(k8stesting.DeleteAction).GetName()
		}
	}
	if created == "" || created != deleted {
		t.Fatalf("probe quota cleanup created=%s deleted=%s", created, deleted)
	}
}

func TestOrgQuotaProbeCleanupOnlyRemovesExpiredOwnedArtifacts(t *testing.T) {
	ctx := context.Background()
	cl := newRBACDynamicFake()
	now := time.Now().UTC()
	for _, name := range []string{"expired", "active", "unlabeled", "unprefixed", "other-limit"} {
		quotaName := orgQuotaProbePrefix + name
		if name == "unprefixed" {
			quotaName = "operator-policy"
		}
		expiry := now.Add(-time.Minute)
		if name == "active" {
			expiry = now.Add(time.Minute)
		}
		label := "true"
		if name == "unlabeled" {
			label = "false"
		}
		limit := "0"
		if name == "other-limit" {
			limit = "10"
		}
		quota := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ResourceQuota", "metadata": map[string]any{"name": quotaName, "namespace": "admin", "uid": "uid-" + name, "labels": map[string]any{orgQuotaProbeLabel: label}, "annotations": map[string]any{orgQuotaProbeExpiry: expiry.Format(time.RFC3339), "experimental.quota.kcp.io/cluster-scoped": "true"}}, "spec": map[string]any{"hard": map[string]any{"count/configmaps": limit}}}}
		if _, err := cl.Resource(orgQuotaGVR).Namespace("admin").Create(ctx, quota, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupExpiredOrgQuotaProbes(ctx, cl, now); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"expired", "active", "unlabeled", "unprefixed", "other-limit"} {
		quotaName := orgQuotaProbePrefix + name
		if name == "unprefixed" {
			quotaName = "operator-policy"
		}
		_, err := cl.Resource(orgQuotaGVR).Namespace("admin").Get(ctx, quotaName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) != (name == "expired") {
			t.Errorf("quota %s error=%v", name, err)
		}
	}
}

func TestOrgProviderAdmissionProbeRequiresItsExactQuotaRejection(t *testing.T) {
	for _, scenario := range []string{"exact", "unrelated namespace quota", "probe name prefix", "probe name suffix"} {
		t.Run(scenario, func(t *testing.T) {
			cl := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
			mockOrgQuotaController(cl)
			cl.PrependReactor("create", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
				object := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
				deniedQuota := object.GetName()
				switch scenario {
				case "unrelated namespace quota":
					deniedQuota = "default-namespace-configmap-limit"
				case "probe name prefix":
					deniedQuota = "other-" + deniedQuota
				case "probe name suffix":
					deniedQuota += "-other"
				}
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, object.GetName(), fmt.Errorf("exceeded quota: %s, requested: count/configmaps=1, used: count/configmaps=0, limited: count/configmaps=0", deniedQuota))
			})
			err := verifyOrgQuotaAdmission(context.Background(), cl)
			if (err == nil) != (scenario == "exact") {
				t.Fatalf("quota proof %s error=%v", scenario, err)
			}
			list, listErr := cl.Resource(orgQuotaGVR).Namespace("admin").List(context.Background(), metav1.ListOptions{})
			if listErr != nil || len(list.Items) != 0 {
				t.Fatalf("probe cleanup: %v %v", list, listErr)
			}
		})
	}
}

func TestOrgQuotaConcurrentCreateRetriesWinner(t *testing.T) {
	cl := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
	mockOrgQuotaController(cl)
	creates := 0
	cl.PrependReactor("create", "resourcequotas", func(action k8stesting.Action) (bool, runtime.Object, error) {
		creates++
		object := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		// Another enrollment wins after our NotFound read. Its quota must be
		// re-read and reconciled, rather than making this enrollment fail.
		if err := cl.Tracker().Create(orgQuotaGVR, object, "admin"); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewAlreadyExists(orgQuotaGVR.GroupResource(), object.GetName())
	})
	if err := ensureWorkspaceQuota(context.Background(), cl, orgQuotaName, map[string]string{"count/configmaps": "64"}); err != nil {
		t.Fatalf("concurrent quota creator prevented enrollment: %v", err)
	}
	if creates != 1 {
		t.Fatalf("create calls=%d, expected one then read/update", creates)
	}
	updates := 0
	for _, action := range cl.Actions() {
		if action.GetResource() == orgQuotaGVR && action.GetVerb() == "update" {
			updates++
		}
	}
	if updates != 1 {
		t.Fatalf("winner quota update calls=%d, expected one", updates)
	}
}
