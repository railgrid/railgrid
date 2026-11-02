// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestOrgStorageInventoryRejectsOversizedObjectsAndDoesNotDelete(t *testing.T) {
	f := newConsumerPolicyFixture(t)
	resource := schema.GroupResource{Resource: "configmaps"}
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "legacy"},
		"data": map[string]any{"payload": "this existing object is larger than the configured limit"},
	}}
	if err := f.consumer.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, object, "default"); err != nil {
		t.Fatal(err)
	}
	f.policy.maxObjectBytes = 64
	if err := f.policy.validateObjectInventory(context.Background(), "cluster-a", f.consumer, []schema.GroupResource{resource}, 4); err == nil {
		t.Fatal("oversized existing object accepted")
	}
	for _, action := range f.consumer.Actions() {
		if action.GetVerb() == "delete" || action.GetVerb() == "delete-collection" {
			t.Fatalf("inventory audit deleted data through %s", action.GetVerb())
		}
	}
}

func TestOrgStorageInventoryAcceptsEmptyOrBoundedObjects(t *testing.T) {
	for _, scenario := range []string{"empty", "bounded"} {
		t.Run(scenario, func(t *testing.T) {
			f := newConsumerPolicyFixture(t)
			if scenario == "bounded" {
				object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "small"}, "data": map[string]any{"k": "v"}}}
				if err := f.consumer.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, object, "default"); err != nil {
					t.Fatal(err)
				}
			}
			f.policy.maxObjectBytes = 256
			if err := f.policy.validateObjectInventory(context.Background(), "cluster-a", f.consumer, []schema.GroupResource{{Resource: "configmaps"}}, 4); err != nil {
				t.Fatalf("bounded inventory rejected: %v", err)
			}
		})
	}
}

func TestOrgStorageInventoryFailsClosedOnListErrorAndContinuation(t *testing.T) {
	for _, scenario := range []string{"list error", "continuation"} {
		t.Run(scenario, func(t *testing.T) {
			f := newConsumerPolicyFixture(t)
			f.consumer.PrependReactor("list", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if scenario == "list error" {
					return true, nil, fmt.Errorf("inventory unavailable")
				}
				return true, &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMapList", "metadata": map[string]any{"continue": "next"}}}, nil
			})
			f.policy.maxObjectBytes = 256
			if err := f.policy.validateObjectInventory(context.Background(), "cluster-a", f.consumer, []schema.GroupResource{{Resource: "configmaps"}}, 4); err == nil {
				t.Fatal("incomplete inventory accepted")
			}
		})
	}
}

func TestValidateProviderStorageInventoriesEveryHardFamilyIncludingZeroUsage(t *testing.T) {
	f := newConsumerPolicyFixture(t)
	listKinds := map[schema.GroupVersionResource]string{orgQuotaGVR: "ResourceQuotaList"}
	for _, entry := range []struct{ group, version, resource string }{
		{"", "v1", "secrets"}, {"", "v1", "configmaps"}, {"coordination.k8s.io", "v1", "leases"},
		{"apis.kcp.io", "v1alpha1", "apiresourceschemas"}, {"apis.kcp.io", "v1alpha2", "apiexports"}, {"apis.kcp.io", "v1alpha1", "apiexportendpointslices"},
		{"apiextensions.k8s.io", "v1", "customresourcedefinitions"}, {"providers.railgrid.ai", "v1alpha1", "catalogentries"},
	} {
		listKinds[schema.GroupVersionResource{Group: entry.group, Version: entry.version, Resource: entry.resource}] = strings.ToUpper(entry.resource[:1]) + entry.resource[1:] + "List"
	}
	providerClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	f.clients["provider-a"] = providerClient
	f.policy.clientFor = func(path string) (dynamic.Interface, error) {
		client := f.clients[path]
		if client == nil {
			return nil, fmt.Errorf("unknown test path %s", path)
		}
		return client, nil
	}
	if err := f.policy.ValidateProviderStorage(context.Background(), "provider-a"); err == nil {
		t.Fatal("missing provider quota accepted")
	}

	hard := orgProviderBaseQuota()
	specHard := make(map[string]any, len(hard))
	statusHard := make(map[string]any, len(hard))
	used := make(map[string]any, len(hard))
	for key, value := range hard {
		specHard[key] = value
		statusHard[key] = value
		used[key] = "0"
	}
	quota := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ResourceQuota", "metadata": map[string]any{"name": orgQuotaName, "namespace": "admin", "annotations": map[string]any{"experimental.quota.kcp.io/cluster-scoped": "true"}},
		"spec":   map[string]any{"hard": specHard},
		"status": map[string]any{"hard": statusHard, "used": used},
	}}
	if err := providerClient.Tracker().Create(orgQuotaGVR, quota, "admin"); err != nil {
		t.Fatal(err)
	}
	f.policy.discoveryFor = func(string) (discovery.DiscoveryInterface, error) { return consumerPolicyDiscovery{}, nil }
	if err := f.policy.ValidateProviderStorage(context.Background(), "provider-a"); err != nil {
		t.Fatalf("empty audited provider inventory failed: %v", err)
	}
	large := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "legacy-large"}, "data": map[string]any{"payload": "this provider object exceeds the configured serialized size"}}}
	if err := providerClient.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, large, "default"); err != nil {
		t.Fatal(err)
	}
	f.policy.maxObjectBytes = 64
	if err := f.policy.ValidateProviderStorage(context.Background(), "provider-a"); err == nil {
		t.Fatal("oversized provider object with stale zero usage accepted")
	}
	if err := providerClient.Tracker().Delete(orgQuotaGVR, "admin", orgQuotaName); err != nil {
		t.Fatal(err)
	}
}

func TestLimitedResponseBodyRejectsBytesPastBound(t *testing.T) {
	body := &limitedResponseBody{ReadCloser: io.NopCloser(bytes.NewBufferString("0123456789")), remaining: 5}
	read, err := io.ReadAll(body)
	if err == nil || string(read) != "01234" || !strings.Contains(err.Error(), "exceeds configured byte bound") {
		t.Fatalf("bounded response read = %q, %v", read, err)
	}
}
