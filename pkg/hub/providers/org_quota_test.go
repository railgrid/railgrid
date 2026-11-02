//go:build kcp_quota_diagnostic

// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/resourcequota"
	"k8s.io/apiserver/pkg/quota/v1/generic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Exercise native admission with optimistic resourceVersion conflicts. The plain
// pinned dependency fails these regressions. Keep this diagnostic failing until
// a supported upstream dependency fixes the behavior; see
// docs/upstream-kcp-requirements.md.
func TestPinnedKCPQuotaEnforcesClusterScopedPrivateResourceCount(t *testing.T) {
	resourceName := corev1.ResourceName("count/receipts.planner.example")
	quota := func(namespace, name, hard string, clusterScoped bool) *corev1.ResourceQuota {
		annotations := map[string]string{}
		if clusterScoped {
			annotations["experimental.quota.kcp.io/cluster-scoped"] = "true"
		}
		return &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, ResourceVersion: "1", Annotations: annotations}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{resourceName: resource.MustParse(hard)}}, Status: corev1.ResourceQuotaStatus{Hard: corev1.ResourceList{resourceName: resource.MustParse(hard)}, Used: corev1.ResourceList{resourceName: resource.MustParse("0")}}}
	}
	for _, test := range []struct {
		name, namespace string
		quotas          []runtime.Object
	}{
		{"cluster object", "", []runtime.Object{quota("admin", orgQuotaName, "1", true)}},
		{"admin object charges cluster quota once", "admin", []runtime.Object{quota("admin", orgQuotaName, "1", true)}},
		{"cluster ignores unrelated namespace quota", "", []runtime.Object{quota("admin", orgQuotaName, "1", true), quota("unrelated", "namespace-only", "0", false)}},
		{"namespace ignores another namespace quota", "default", []runtime.Object{quota("admin", orgQuotaName, "1", true), quota("unrelated", "namespace-only", "0", false)}},
		{"normal namespace quota remains enforced", "default", []runtime.Object{quota("default", "namespace-only", "1", false)}},
		{"same quota name in distinct namespaces", "default", []runtime.Object{quota("admin", "shared", "2", true), quota("default", "shared", "1", false)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, synced := range []bool{false, true} {
				t.Run(fmt.Sprintf("synced=%v", synced), func(t *testing.T) { verifyNativeQuota(t, test.namespace, test.quotas, synced) })
			}
		})
	}
}

func verifyNativeQuota(t *testing.T, namespace string, quotas []runtime.Object, synced bool) {
	t.Helper()
	client := fake.NewSimpleClientset(quotas...)
	// The admission cache compares etcd resourceVersions; the generic fake does
	// not increment them on UpdateStatus, so model that storage property here.
	client.PrependReactor("update", "resourcequotas", func(action k8stesting.Action) (bool, runtime.Object, error) {
		next := action.(k8stesting.UpdateAction).GetObject().(*corev1.ResourceQuota).DeepCopy()
		stored, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("resourcequotas"), next.Namespace, next.Name)
		if err != nil {
			return true, nil, err
		}
		current := stored.(*corev1.ResourceQuota)
		if current.ResourceVersion != next.ResourceVersion {
			return true, nil, apierrors.NewConflict(corev1.Resource("resourcequotas"), next.Name, fmt.Errorf("stale resource version"))
		}
		version, _ := strconv.Atoi(current.ResourceVersion)
		next.ResourceVersion = strconv.Itoa(version + 1)
		if err := client.Tracker().Update(corev1.SchemeGroupVersion.WithResource("resourcequotas"), next, next.Namespace); err != nil {
			return true, nil, err
		}
		return true, next, nil
	})
	factory := informers.NewSharedInformerFactory(client, 0)
	plugin, err := resourcequota.NewResourceQuota(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	plugin.SetDrainedNotification(stop)
	plugin.SetExternalKubeClientSet(client)
	plugin.SetExternalKubeInformerFactory(factory)
	plugin.SetQuotaConfiguration(generic.NewConfiguration(nil, nil))
	if err := plugin.ValidateInitialization(); err != nil {
		t.Fatal(err)
	}
	if synced {
		factory.Start(stop)
		for resource, ready := range factory.WaitForCacheSync(stop) {
			if !ready {
				t.Fatalf("informer %v did not sync", resource)
			}
		}
	}
	create := func(name string) error {
		object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "planner.example/v1", "kind": "Receipt", "metadata": map[string]any{"name": name, "namespace": namespace}}}
		attr := admission.NewAttributesRecord(object, nil, schema.GroupVersionKind{Group: "planner.example", Version: "v1", Kind: "Receipt"}, namespace, name, schema.GroupVersionResource{Group: "planner.example", Version: "v1", Resource: "receipts"}, "", admission.Create, &metav1.CreateOptions{}, false, nil)
		return plugin.Validate(context.Background(), attr, nil)
	}
	if err := create("one"); err != nil {
		t.Fatalf("first private object rejected: %v", err)
	}
	if err := create("two"); !apierrors.IsForbidden(err) {
		t.Fatalf("over-quota cluster-scoped create error=%v", err)
	}
	if plugin.Handles(admission.Delete) {
		t.Fatal("failed deletes must not decrement admission quota counters")
	}
}
