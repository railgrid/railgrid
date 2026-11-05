// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package kcp

import (
	"context"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type pausedRoleClient struct {
	dynamic.Interface
	paused chan struct{}
	resume chan struct{}
	once   sync.Once
}
type pausedRoleResource struct {
	dynamic.NamespaceableResourceInterface
	owner    *pausedRoleClient
	resource schema.GroupVersionResource
}

func (c *pausedRoleClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &pausedRoleResource{NamespaceableResourceInterface: c.Interface.Resource(gvr), owner: c, resource: gvr}
}
func (r *pausedRoleResource) Get(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	if r.resource == tenantAdminRoleGVR && name == TenantAdminRoleName {
		r.owner.once.Do(func() {
			close(r.owner.paused)
			select {
			case <-r.owner.resume:
			case <-ctx.Done():
			}
		})
	}
	return r.NamespaceableResourceInterface.Get(ctx, name, opts, subresources...)
}

// Pause a refresh before its role read, then let a binding refresh complete.
func TestTenantAdminRefreshPreservesConcurrentBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := newCRBClient(t)
	if err := ensureTenantAdminRole(ctx, base); err != nil {
		t.Fatal(err)
	}
	stale := &pausedRoleClient{Interface: base, paused: make(chan struct{}), resume: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- ensureTenantAdminRole(ctx, stale) }()
	select {
	case <-stale.paused:
	case <-ctx.Done():
		t.Fatal("old refresh did not pause after reading its snapshot")
	}
	binding := tenantAdminBinding("planner.example", "boards")
	if _, err := base.Resource(apiBindingGVR).Create(ctx, &binding, metav1.CreateOptions{}); err != nil {
		close(stale.resume)
		t.Fatal(err)
	}
	if err := ensureTenantAdminRole(ctx, base); err != nil {
		close(stale.resume)
		t.Fatal(err)
	}
	current, err := base.Resource(tenantAdminRoleGVR).Get(ctx, TenantAdminRoleName, metav1.GetOptions{})
	if err != nil {
		close(stale.resume)
		t.Fatal(err)
	}
	before, _, _ := unstructured.NestedSlice(current.Object, "rules")
	hasGroup := func(rules []any) bool {
		for _, raw := range rules {
			rule, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
			for _, group := range groups {
				if group == "planner.example" {
					return true
				}
			}
		}
		return false
	}
	if !hasGroup(before) {
		close(stale.resume)
		t.Fatal("positive control: new binding did not grant its group")
	}
	close(stale.resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	current, err = base.Resource(tenantAdminRoleGVR).Get(ctx, TenantAdminRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after, _, _ := unstructured.NestedSlice(current.Object, "rules")
	if !hasGroup(after) {
		t.Fatal("stale refresh removed the new binding grant")
	}
	stillBound, err := base.Resource(apiBindingGVR).Get(ctx, binding.GetName(), metav1.GetOptions{})
	if err != nil || stillBound == nil {
		t.Fatal("binding must still exist after the stale refresh")
	}

}
