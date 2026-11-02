// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

func TestOrgConsumerAllocationIsPersistentAndIdempotent(t *testing.T) {
	client := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
	mockOrgQuotaController(client)
	path := kcppaths.WorkspacePath("org-a", "team-a")
	for i := 0; i < 2; i++ {
		if err := EnsureOrgConsumerAllocation(context.Background(), client, "org-a", path, "cluster-a", 2); err != nil {
			t.Fatal(err)
		}
	}
	creates := 0
	for _, action := range client.Actions() {
		if action.GetResource() != orgConsumerLeaseGVR {
			continue
		}
		switch action.GetVerb() {
		case "create":
			creates++
		case "update", "patch", "delete":
			t.Fatalf("allocation unexpectedly mutated via %s", action.GetVerb())
		}
	}
	if creates != 1 {
		t.Fatalf("lease creates=%d", creates)
	}
	lease, err := client.Resource(orgConsumerLeaseGVR).Namespace("admin").Get(context.Background(), "consumer-cluster-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lease.GetAnnotations()[orgConsumerAllocationPath] != path || lease.GetAnnotations()[orgConsumerAllocationOrg] != "org-a" {
		t.Fatal("allocation lost authoritative identity")
	}
	if duration, found, _ := unstructured.NestedFieldNoCopy(lease.Object, "spec", "leaseDurationSeconds"); found {
		t.Fatalf("persistent allocation has expiry %v", duration)
	}
	quota, err := client.Resource(orgQuotaGVR).Namespace("admin").Get(context.Background(), "railgrid-provider-registrations", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hard, _, _ := unstructured.NestedStringMap(quota.Object, "spec", "hard")
	if hard["count/workspaces.tenancy.kcp.io"] != "8" || hard["count/leases.coordination.k8s.io"] != "2" {
		t.Fatalf("parent quota lost a capacity bound: %v", hard)
	}
	if err := EnsureOrgConsumerAllocation(context.Background(), client, "org-a", kcppaths.WorkspacePath("org-a", "different-team"), "cluster-a", 2); err == nil {
		t.Fatal("existing cluster allocation rebound to different workspace")
	}
}

func TestOrgConsumerAllocationRejectsReducedCapacityBelowExistingUsage(t *testing.T) {
	client := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
	mockOrgQuotaController(client)
	path := kcppaths.WorkspacePath("org-a", "team-a")
	if err := EnsureOrgConsumerAllocation(context.Background(), client, "org-a", path, "cluster-a", 3); err != nil {
		t.Fatal(err)
	}
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
		used["count/leases.coordination.k8s.io"] = "3"
		_ = unstructured.SetNestedStringMap(quota.Object, hard, "status", "hard")
		_ = unstructured.SetNestedStringMap(quota.Object, used, "status", "used")
		return true, quota, nil
	})
	if err := EnsureOrgConsumerAllocation(context.Background(), client, "org-a", path, "cluster-a", 2); err == nil {
		t.Fatal("existing allocation admitted despite total usage exceeding new policy")
	}
}

func TestOrgConsumerAllocationRereadsConcurrentCreator(t *testing.T) {
	for _, sameIdentity := range []bool{true, false} {
		t.Run(fmt.Sprintf("matching=%v", sameIdentity), func(t *testing.T) {
			client := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
			mockOrgQuotaController(client)
			creates := 0
			client.PrependReactor("create", "leases", func(action k8stesting.Action) (bool, runtime.Object, error) {
				creates++
				lease := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
				lease.SetResourceVersion("2")
				if !sameIdentity {
					annotations := lease.GetAnnotations()
					annotations[orgConsumerAllocationOrg] = "other-org"
					lease.SetAnnotations(annotations)
				}
				if err := client.Tracker().Create(orgConsumerLeaseGVR, lease, "admin"); err != nil {
					return true, nil, err
				}
				return true, nil, apierrors.NewAlreadyExists(orgConsumerLeaseGVR.GroupResource(), lease.GetName())
			})
			err := EnsureOrgConsumerAllocation(context.Background(), client, "org-a", kcppaths.WorkspacePath("org-a", "team-a"), "cluster-a", 2)
			if (err == nil) != sameIdentity {
				t.Fatalf("race identity result=%v", err)
			}
			if creates != 1 {
				t.Fatalf("creator was retried without rereading winner: %d", creates)
			}
			for _, action := range client.Actions() {
				if action.GetResource() == orgConsumerLeaseGVR && action.GetVerb() == "update" {
					t.Fatal("rewrote winning allocation")
				}
			}
		})
	}
}

func TestOrgConsumerAllocationFailsClosed(t *testing.T) {
	for _, scenario := range []string{"zero limit", "foreign org", "provider parent", "nested workspace", "path instead of cluster", "quota unavailable", "quota exhausted"} {
		t.Run(scenario, func(t *testing.T) {
			client := newRBACDynamicFake().(*dynamicfake.FakeDynamicClient)
			org, path, cluster, limit := "org-a", kcppaths.WorkspacePath("org-a", "team-a"), "cluster-a", int64(2)
			ctx := context.Background()
			switch scenario {
			case "zero limit":
				limit = 0
			case "foreign org":
				path = kcppaths.WorkspacePath("org-b", "team-a")
			case "provider parent":
				path = kcppaths.OrgProvidersParent(org)
			case "nested workspace":
				path += ":child"
			case "path instead of cluster":
				cluster = path
			case "quota unavailable":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "quota exhausted":
				mockOrgQuotaController(client)
				client.PrependReactor("create", "leases", func(action k8stesting.Action) (bool, runtime.Object, error) {
					object := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
					return true, nil, apierrors.NewForbidden(orgConsumerLeaseGVR.GroupResource(), object.GetName(), fmt.Errorf("exceeded quota: railgrid-provider-registrations"))
				})
			}
			if err := EnsureOrgConsumerAllocation(ctx, client, org, path, cluster, limit); err == nil {
				t.Fatal("unsafe allocation accepted")
			}
			if _, err := client.Tracker().Get(orgConsumerLeaseGVR, "admin", "consumer-cluster-a"); !apierrors.IsNotFound(err) {
				t.Fatalf("failed allocation persisted lease: %v", err)
			}
		})
	}
}
