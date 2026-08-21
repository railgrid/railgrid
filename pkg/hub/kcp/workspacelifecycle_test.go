/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kcp

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
)

func workspaceClient(t *testing.T) dynamic.Interface {
	t.Helper()
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{workspaceGVR: "WorkspaceList"})
}

func workspaceObj(name, phase string, deleting bool) *unstructured.Unstructured {
	meta := map[string]any{"name": name}
	if deleting {
		meta["deletionTimestamp"] = "2026-08-20T10:00:00Z"
		// A real terminating object always carries a finalizer; without one it
		// would already be gone.
		meta["finalizers"] = []any{"tenancy.kcp.io/workspace"}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   meta,
		"spec":       map[string]any{"cluster": "abc123"},
		"status":     map[string]any{"phase": phase},
	}}
}

func seed(t *testing.T, client dynamic.Interface, ws *unstructured.Unstructured) {
	t.Helper()
	if _, err := client.Resource(workspaceGVR).Create(context.Background(), ws, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
}

// The bug behind "removing and reinstalling a provider" failing with
// "workspace access not permitted": kcp keeps a Workspace in phase Ready while
// it terminates, so a readiness check that looks only at phase hands back a
// workspace whose RBAC is already being collected. The first write into it is
// then refused — a permission error for what is really a lifecycle race, and
// one that looks nothing like its cause.
func TestWaitForWorkspaceReadyRejectsTerminating(t *testing.T) {
	client := workspaceClient(t)
	seed(t, client, workspaceObj("infrastructure", "Ready", true /* deleting */))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := waitForWorkspaceReady(ctx, client, "infrastructure"); err == nil {
		t.Fatal("a terminating workspace was reported ready; callers will write into a workspace losing its RBAC")
	}
}

// The positive case, so the test above cannot pass by the wait never
// succeeding at all.
func TestWaitForWorkspaceReadyAcceptsLiveWorkspace(t *testing.T) {
	client := workspaceClient(t)
	seed(t, client, workspaceObj("infrastructure", "Ready", false))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := waitForWorkspaceReady(ctx, client, "infrastructure"); err != nil {
		t.Fatalf("a live Ready workspace was not accepted: %v", err)
	}
}

func TestWaitForWorkspaceGone(t *testing.T) {
	t.Run("returns once the workspace is absent", func(t *testing.T) {
		client := workspaceClient(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := waitForWorkspaceGone(ctx, client, "infrastructure", 3*time.Second); err != nil {
			t.Fatalf("absent workspace not reported gone: %v", err)
		}
	})

	t.Run("keeps waiting while it lingers", func(t *testing.T) {
		client := workspaceClient(t)
		seed(t, client, workspaceObj("infrastructure", "Ready", true))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Must time out rather than return success: returning early would put
		// the caller straight back into the AlreadyExists-then-forbidden path
		// this exists to avoid.
		if err := waitForWorkspaceGone(ctx, client, "infrastructure", time.Second); err == nil {
			t.Fatal("a lingering workspace was reported gone")
		}
	})
}
