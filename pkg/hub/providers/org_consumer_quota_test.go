// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func consumerQuotaForTest(t *testing.T, client *dynamicfake.FakeDynamicClient) map[string]string {
	t.Helper()
	object, err := client.Resource(orgQuotaGVR).Namespace("admin").Get(context.Background(), OrgConsumerQuotaName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if object.GetAnnotations()["experimental.quota.kcp.io/cluster-scoped"] != "true" {
		t.Fatal("consumer quota does not protect whole logical cluster")
	}
	hard, _, err := unstructured.NestedStringMap(object.Object, "spec", "hard")
	if err != nil {
		t.Fatal(err)
	}
	return hard
}

func TestOrgConsumerQuotaRetainsFamiliesAndBoundsBuiltinStorage(t *testing.T) {
	client := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
	mockOrgQuotaController(client)
	limits := ConsumerQuotaLimits{MaxResourceTypes: 11, MaxObjectsPerResource: 12}
	receipts := schema.GroupResource{Group: "planner.example", Resource: "receipts"}
	if err := EnsureOrgConsumerQuota(context.Background(), client, []schema.GroupResource{receipts, receipts}, limits); err != nil {
		t.Fatal(err)
	}
	hard := consumerQuotaForTest(t, client)
	for _, key := range []string{"count/secrets", "count/configmaps", "count/serviceaccounts", "count/namespaces", "count/roles.rbac.authorization.k8s.io", "count/rolebindings.rbac.authorization.k8s.io", "count/clusterroles.rbac.authorization.k8s.io", "count/clusterrolebindings.rbac.authorization.k8s.io", "count/leases.coordination.k8s.io", "count/mcpservers.railgrid.ai", "count/receipts.planner.example"} {
		if hard[key] != "12" {
			t.Errorf("missing count bound %s: %v", key, hard)
		}
	}
	limits.MaxObjectsPerResource = 4
	if err := EnsureOrgConsumerQuota(context.Background(), client, nil, limits); err != nil {
		t.Fatal(err)
	}
	hard = consumerQuotaForTest(t, client)
	if hard["count/receipts.planner.example"] != "4" {
		t.Fatal("retired kind lost quota or retained obsolete larger allowance")
	}
	if err := EnsureOrgConsumerQuota(context.Background(), client, []schema.GroupResource{{Group: "planner.example", Resource: "newkind"}}, limits); err == nil {
		t.Fatal("retired family did not consume lifetime budget")
	}
	if !reflect.DeepEqual(hard, consumerQuotaForTest(t, client)) {
		t.Fatal("rejected allocation changed quota")
	}
}

func TestOrgConsumerQuotaRejectsUnboundedOrReservedInputs(t *testing.T) {
	for _, limits := range []ConsumerQuotaLimits{{}, {MaxResourceTypes: 9, MaxObjectsPerResource: 1}, {MaxResourceTypes: 20}, {MaxResourceTypes: 20, MaxObjectsPerResource: -1}} {
		client := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
		if err := EnsureOrgConsumerQuota(context.Background(), client, nil, limits); err == nil {
			t.Fatalf("invalid operator limits admitted: %+v", limits)
		}
		if len(client.Actions()) != 0 {
			t.Fatal("invalid limits mutated storage")
		}
	}
	for _, resource := range []schema.GroupResource{{Resource: "resourcequotas"}, {Resource: "events"}, {Resource: "serviceaccounts/token"}, {Group: "apis.kcp.io", Resource: "apiexports"}, {Group: "railgrid.ai", Resource: "arbitrary"}, {Group: "dataplane.railgrid.ai", Resource: "dataplaneendpointslices"}, {Group: "tenant.example", Resource: "*"}, {Group: "tenant.example", Resource: "objects/status"}} {
		client := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
		if err := EnsureOrgConsumerQuota(context.Background(), client, []schema.GroupResource{resource}, ConsumerQuotaLimits{MaxResourceTypes: 20, MaxObjectsPerResource: 10}); err == nil {
			t.Fatalf("unsupported family admitted: %s", resource)
		}
		if len(client.Actions()) != 0 {
			t.Fatal("invalid resource mutated storage")
		}
	}
}

func TestOrgConsumerQuotaRejectsExistingUsageAboveAllocation(t *testing.T) {
	for _, usage := range []string{"5", "-1", "1500m", "invalid"} {
		t.Run(usage, func(t *testing.T) {
			client := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
			client.PrependReactor("get", "resourcequotas", func(action k8stesting.Action) (bool, runtime.Object, error) {
				object, err := client.Tracker().Get(orgQuotaGVR, action.GetNamespace(), action.(k8stesting.GetAction).GetName())
				if err != nil {
					return true, nil, err
				}
				quota := object.(*unstructured.Unstructured)
				hard, _, _ := unstructured.NestedStringMap(quota.Object, "spec", "hard")
				used := map[string]string{}
				for key := range hard {
					used[key] = "0"
				}
				used["count/configmaps"] = usage
				_ = unstructured.SetNestedStringMap(quota.Object, hard, "status", "hard")
				_ = unstructured.SetNestedStringMap(quota.Object, used, "status", "used")
				return true, quota, nil
			})
			if err := EnsureOrgConsumerQuota(context.Background(), client, nil, ConsumerQuotaLimits{MaxResourceTypes: 10, MaxObjectsPerResource: 4}); err == nil {
				t.Fatal("existing storage outside the proposed allocation was admitted")
			}
		})
	}
}

func TestOrgConsumerQuotaConcurrentLastSlotRechecksLatestResourceVersion(t *testing.T) {
	client := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
	mockOrgQuotaController(client)
	limits := ConsumerQuotaLimits{MaxResourceTypes: 11, MaxObjectsPerResource: 3}
	if err := EnsureOrgConsumerQuota(context.Background(), client, nil, limits); err != nil {
		t.Fatal(err)
	}
	object, err := client.Tracker().Get(orgQuotaGVR, "admin", OrgConsumerQuotaName)
	if err != nil {
		t.Fatal(err)
	}
	initial := object.(*unstructured.Unstructured)
	initial.SetResourceVersion("1")
	if err := client.Tracker().Update(orgQuotaGVR, initial, "admin"); err != nil {
		t.Fatal(err)
	}
	updates := 0
	client.PrependReactor("update", "resourcequotas", func(action k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		requested := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		stored, err := client.Tracker().Get(orgQuotaGVR, "admin", OrgConsumerQuotaName)
		if err != nil {
			return true, nil, err
		}
		current := stored.(*unstructured.Unstructured)
		if updates == 1 {
			// A second provider consumes the last slot between this request's GET
			// and UPDATE. Model etcd's version conflict, not a last-writer-wins fake.
			hard, _, _ := unstructured.NestedStringMap(current.Object, "spec", "hard")
			hard["count/winner.other.example"] = "3"
			_ = unstructured.SetNestedStringMap(current.Object, hard, "spec", "hard")
			current.SetResourceVersion("2")
			if err := client.Tracker().Update(orgQuotaGVR, current, "admin"); err != nil {
				return true, nil, err
			}
		}
		if requested.GetResourceVersion() != current.GetResourceVersion() {
			return true, nil, apierrors.NewConflict(orgQuotaGVR.GroupResource(), OrgConsumerQuotaName, fmt.Errorf("stale resourceVersion"))
		}
		return false, nil, nil
	})
	err = EnsureOrgConsumerQuota(context.Background(), client, []schema.GroupResource{{Group: "planner.example", Resource: "loser"}}, limits)
	if err == nil || !strings.Contains(err.Error(), "lifetime budget exceeded") {
		t.Fatalf("concurrent allocation did not fail closed: %v", err)
	}
	hard := consumerQuotaForTest(t, client)
	if len(hard) != 11 || hard["count/winner.other.example"] != "3" || hard["count/loser.planner.example"] != "" {
		t.Fatalf("quota exceeded lifetime cap: %v", hard)
	}
	if updates != 1 {
		t.Fatalf("invalid retry issued another update: %d", updates)
	}
}

func consumerSchema(name, plural, scope string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha1", "kind": "APIResourceSchema", "metadata": map[string]any{"name": name}, "spec": map[string]any{"group": "planner.example", "names": map[string]any{"plural": plural}, "scope": scope, "versions": []any{map[string]any{"name": "v1", "served": true, "storage": true}}}}}
}
func consumerExport(resources ...any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIExport", "metadata": map[string]any{"name": "planner"}, "spec": map[string]any{"resources": resources, "permissionClaims": []any{map[string]any{"group": "evil.example", "resource": "secrets", "identityHash": "untrusted"}}}}}
}
func consumerExportResource(name, schemaName string) map[string]any {
	return map[string]any{"group": "planner.example", "name": name, "schema": schemaName, "storage": map[string]any{"crd": map[string]any{}}}
}

func TestOrgConsumerQuotaResourcesUsesAllAuthoritativeSchemaFamilies(t *testing.T) {
	first := consumerSchema("v1.receipts.planner.example", "receipts", "Cluster")
	second := consumerSchema("v2.receipts.planner.example", "receipts", "Cluster")
	_ = unstructured.SetNestedSlice(second.Object, []any{map[string]any{"name": "v2", "served": true, "storage": true}}, "spec", "versions")
	ordinary := consumerSchema("v1.invisible.planner.example", "invisible", "Namespaced")
	verbSchema := consumerSchema("v1.runrequests.planner.example", "runrequests", "Cluster")
	verb := consumerExportResource("receipts/run", verbSchema.GetName())
	verb["storage"] = map[string]any{"virtual": map[string]any{}}
	export := consumerExport(consumerExportResource("receipts", first.GetName()), consumerExportResource("receipts", second.GetName()), consumerExportResource("invisible", ordinary.GetName()), verb)
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), export, first, second, ordinary, verbSchema)
	got, err := ResolveOrgConsumerQuotaResources(context.Background(), client, "planner")
	if err != nil {
		t.Fatal(err)
	}
	want := []schema.GroupResource{{Group: "planner.example", Resource: "invisible"}, {Group: "planner.example", Resource: "receipts"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("families=%v want=%v (versions, request-only kinds, claims must not add families)", got, want)
	}
}

func TestOrgConsumerQuotaResourcesFailsClosedOnUnsupportedStorageAndSchema(t *testing.T) {
	for _, scenario := range []string{"missing schema", "ordinary virtual", "group mismatch", "plural mismatch", "unsupported scope", "reserved group", "legacy declaration", "bad subresource"} {
		t.Run(scenario, func(t *testing.T) {
			object := consumerSchema("v1.receipts.planner.example", "receipts", "Cluster")
			resource := consumerExportResource("receipts", object.GetName())
			export := consumerExport(resource)
			schemas := []*unstructured.Unstructured{object}
			switch scenario {
			case "missing schema":
				schemas = nil
			case "ordinary virtual":
				resource["storage"] = map[string]any{"virtual": map[string]any{}}
			case "group mismatch":
				_ = unstructured.SetNestedField(object.Object, "other.example", "spec", "group")
			case "plural mismatch":
				_ = unstructured.SetNestedField(object.Object, "other", "spec", "names", "plural")
			case "unsupported scope":
				_ = unstructured.SetNestedField(object.Object, "Global", "spec", "scope")
			case "reserved group":
				resource["group"] = "apis.kcp.io"
			case "legacy declaration":
				unstructured.RemoveNestedField(export.Object, "spec", "resources")
				_ = unstructured.SetNestedSlice(export.Object, []any{object.GetName()}, "spec", "latestResourceSchemas")
			case "bad subresource":
				resource["name"] = "receipts/run/extra"
			}
			if _, err := OrgConsumerQuotaResources(export, schemas); err == nil {
				t.Fatal("unsupported resource was treated as bounded")
			}
		})
	}
}
