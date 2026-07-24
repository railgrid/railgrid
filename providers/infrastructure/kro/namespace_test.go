/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// TestEnsureTenantNamespaceStampsLimitRange pins the tenant resource policy:
// creating a tenant namespace also creates the kedge-defaults LimitRange
// (the noisy-neighbor bound), idempotently, and an existing — possibly
// operator-tuned — LimitRange is never overwritten.
func TestEnsureTenantNamespaceStampsLimitRange(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	c := &realClient{dyn: dyn}

	const tenant = "root:kedge:orgs:test"
	ns, err := c.EnsureTenantNamespace(context.Background(), tenant)
	if err != nil {
		t.Fatalf("EnsureTenantNamespace: %v", err)
	}

	lr, err := dyn.Resource(limitRangeGVR).Namespace(ns).Get(context.Background(), tenantLimitRangeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get LimitRange %s/%s: %v", ns, tenantLimitRangeName, err)
	}
	limits, found, _ := unstructured.NestedSlice(lr.Object, "spec", "limits")
	if !found || len(limits) != 1 {
		t.Fatalf("LimitRange spec.limits = %v, want exactly one Container entry", limits)
	}
	entry, _ := limits[0].(map[string]any)
	if got, _, _ := unstructured.NestedString(entry, "type"); got != "Container" {
		t.Fatalf("limit type = %q, want Container", got)
	}
	for _, field := range []string{"defaultRequest", "default", "max"} {
		if _, found, _ := unstructured.NestedMap(entry, field); !found {
			t.Fatalf("LimitRange entry missing %s", field)
		}
	}
	if got, _, _ := unstructured.NestedString(lr.Object, "metadata", "labels", LabelManagedBy); got != ManagedByValue {
		t.Fatalf("managed-by label = %q, want %q", got, ManagedByValue)
	}

	// An operator hand-tunes the LimitRange; a cold-cache re-ensure (fresh
	// client, same cluster) must not put ours back.
	tuned := lr.DeepCopy()
	limits[0].(map[string]any)["max"] = map[string]any{"cpu": "8", "memory": "16Gi"}
	if err := unstructured.SetNestedSlice(tuned.Object, limits, "spec", "limits"); err != nil {
		t.Fatalf("set tuned limits: %v", err)
	}
	if _, err := dyn.Resource(limitRangeGVR).Namespace(ns).Update(context.Background(), tuned, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update LimitRange: %v", err)
	}

	c2 := &realClient{dyn: dyn}
	if _, err := c2.EnsureTenantNamespace(context.Background(), tenant); err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	after, err := dyn.Resource(limitRangeGVR).Namespace(ns).Get(context.Background(), tenantLimitRangeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-get LimitRange: %v", err)
	}
	afterLimits, _, _ := unstructured.NestedSlice(after.Object, "spec", "limits")
	gotMax, _, _ := unstructured.NestedString(afterLimits[0].(map[string]any), "max", "cpu")
	if gotMax != "8" {
		t.Fatalf("re-ensure overwrote the operator-tuned LimitRange: max.cpu = %q, want 8", gotMax)
	}
}

// TestEnsureTenantNamespaceLimitRangeDisabled pins the opt-out: with
// KEDGE_TENANT_LIMITRANGE=disabled no LimitRange is stamped.
func TestEnsureTenantNamespaceLimitRangeDisabled(t *testing.T) {
	t.Setenv(tenantLimitRangeDisableEnv, "disabled")
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	c := &realClient{dyn: dyn}

	ns, err := c.EnsureTenantNamespace(context.Background(), "root:kedge:orgs:optout")
	if err != nil {
		t.Fatalf("EnsureTenantNamespace: %v", err)
	}
	if _, err := dyn.Resource(limitRangeGVR).Namespace(ns).Get(context.Background(), tenantLimitRangeName, metav1.GetOptions{}); err == nil {
		t.Fatal("LimitRange was created despite KEDGE_TENANT_LIMITRANGE=disabled")
	}
}

// TestRuntimeNamespace pins the source→runtime namespace mapping. An empty
// source namespace must default to "default" — instances created without an
// explicit namespace (App Studio's promoted Application/SimpleWebApp) carry ""
// on the instance object, yet kro materializes their children into
// "<cluster>-default". Bare concatenation yielded "<cluster>-", an invalid
// RFC 1123 label that both crashed the bridge reconcile and missed the pods'
// namespace, leaving instances stuck on "no such key: fqdn (data pending)".
func TestRuntimeNamespace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cluster string
		ns      string
		want    string
	}{
		{name: "empty namespace defaults to default", cluster: "jcb49sm6dkg85xwg", ns: "", want: "jcb49sm6dkg85xwg-default"},
		{name: "explicit namespace preserved", cluster: "jcb49sm6dkg85xwg", ns: "team-a", want: "jcb49sm6dkg85xwg-team-a"},
		{name: "explicit default preserved", cluster: "abc", ns: "default", want: "abc-default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RuntimeNamespace(tc.cluster, tc.ns); got != tc.want {
				t.Fatalf("RuntimeNamespace(%q, %q) = %q, want %q", tc.cluster, tc.ns, got, tc.want)
			}
		})
	}
}
