/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

// deletingProject is a Project the tenant has asked to delete, bound to a
// repository App Studio created for it.
func deletingProject(deleteRepository bool) *aiv1alpha1.Project {
	now := metav1.Now()
	annotations := map[string]string{
		orgUUIDAnnotation:       "org-a",
		workspaceUUIDAnnotation: "ws-1",
	}
	if deleteRepository {
		annotations[aiv1alpha1.ProjectDeleteRepositoryAnnotation] = "true"
	}
	return &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "demo",
			UID:               types.UID("project-uid"),
			DeletionTimestamp: &now,
			Finalizers:        []string{aiv1alpha1.ProjectFinalizer},
			Annotations:       annotations,
		},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Demo",
			Repository:  &aiv1alpha1.ProjectRepositoryBinding{RepositoryRef: "demo-repo"},
		},
	}
}

func claimedRepository(adopted bool) *unstructured.Unstructured {
	annotations := map[string]any{projectRepositoryUIDAnnotation: "project-uid", projectRepositoryProjectAnnotation: "demo"}
	if adopted {
		annotations[projectRepositoryAdoptedAnnotation] = "true"
	}
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": repositoryGVK.GroupVersion().String(),
		"kind":       repositoryGVK.Kind,
		"metadata": map[string]any{
			"name":        "demo-repo",
			"labels":      map[string]any{projectRepositoryLabel: "demo"},
			"annotations": annotations,
		},
		"spec": map[string]any{"name": "demo-repo"},
	}}
	return repo
}

func tenantClientWith(objects ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if len(objects) > 0 {
		builder = builder.WithObjects(objects...)
	}
	return builder.Build()
}

// The default is release, not delete: git holds the user's work and a
// repository outlives the workspace UI concept that pointed at it.
func TestTeardownReleasesTheRepositoryByDefault(t *testing.T) {
	ctx := context.Background()
	repo := claimedRepository(false)
	tc := tenantClientWith(repo)
	r := &Reconciler{}
	if err := r.settleRepository(ctx, tc, deletingProject(false)); err != nil {
		t.Fatalf("settleRepository: %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(repositoryGVK)
	if err := tc.Get(ctx, types.NamespacedName{Name: "demo-repo"}, got); err != nil {
		t.Fatalf("the repository was deleted by a release: %v", err)
	}
	if claim := got.GetLabels()[projectRepositoryLabel]; claim != "" {
		t.Fatalf("claim label = %q, want it cleared so the repository can be imported again", claim)
	}
	if uid := got.GetAnnotations()[projectRepositoryUIDAnnotation]; uid != "" {
		t.Fatalf("claim annotation = %q, want it cleared", uid)
	}
}

// Deletion is the opt-in, read off the object the finalizer is finalizing.
func TestTeardownDeletesTheRepositoryWhenTheDeletionAskedFor(t *testing.T) {
	ctx := context.Background()
	tc := tenantClientWith(claimedRepository(false))
	r := &Reconciler{}
	if err := r.settleRepository(ctx, tc, deletingProject(true)); err != nil {
		t.Fatalf("settleRepository: %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(repositoryGVK)
	err := tc.Get(ctx, types.NamespacedName{Name: "demo-repo"}, got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("repository read after an opted-in deletion = %v, want NotFound", err)
	}
}

// An adopted repository existed before the project. Whatever the deletion
// asked for, it is not this provider's to destroy — it is released instead.
func TestTeardownNeverDeletesAnAdoptedRepository(t *testing.T) {
	ctx := context.Background()
	tc := tenantClientWith(claimedRepository(true))
	r := &Reconciler{}
	if err := r.settleRepository(ctx, tc, deletingProject(true)); err != nil {
		t.Fatalf("settleRepository: %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(repositoryGVK)
	if err := tc.Get(ctx, types.NamespacedName{Name: "demo-repo"}, got); err != nil {
		t.Fatalf("an adopted repository was deleted: %v", err)
	}
	if claim := got.GetLabels()[projectRepositoryLabel]; claim != "" {
		t.Fatalf("adopted repository claim = %q, want it released", claim)
	}
}

// A repository another project has since claimed is not ours to touch at all.
func TestTeardownLeavesAForeignRepositoryAlone(t *testing.T) {
	ctx := context.Background()
	repo := claimedRepository(false)
	labels := repo.GetLabels()
	labels[projectRepositoryLabel] = "someone-else"
	repo.SetLabels(labels)
	tc := tenantClientWith(repo)
	r := &Reconciler{}
	if err := r.settleRepository(ctx, tc, deletingProject(true)); err != nil {
		t.Fatalf("settleRepository: %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(repositoryGVK)
	if err := tc.Get(ctx, types.NamespacedName{Name: "demo-repo"}, got); err != nil {
		t.Fatalf("a foreign repository was deleted: %v", err)
	}
	if claim := got.GetLabels()[projectRepositoryLabel]; claim != "someone-else" {
		t.Fatalf("foreign claim = %q, want it untouched", claim)
	}
}

// The teardown waits for a turn it has asked to stop rather than purging
// underneath it.
func TestTeardownWaitsForTheAssistantToStop(t *testing.T) {
	ctx := context.Background()
	stopped := 0
	busy := true
	r := &Reconciler{
		StopAssistant: func(context.Context, workspace.Scope) error { stopped++; return nil },
		Busy:          func(workspace.Scope) bool { return busy },
	}
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	if err := r.quiesceAssistant(ctx, scope); err == nil {
		t.Fatal("quiesceAssistant succeeded while the turn was still running")
	}
	busy = false
	if err := r.quiesceAssistant(ctx, scope); err != nil {
		t.Fatalf("quiesceAssistant after the turn ended: %v", err)
	}
	if stopped != 2 {
		t.Fatalf("StopAssistant calls = %d, want one per pass", stopped)
	}
}

// The conversation rows go with the project; the purge is what makes a
// deletion final rather than merely invisible.
func TestTeardownPurgesConversations(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	if err := messages.AppendMessage(ctx, scope, store.Message{ID: "m1", Role: "user", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Store: messages}
	if err := r.purgeConversations(ctx, scope); err != nil {
		t.Fatalf("purgeConversations: %v", err)
	}
	page, err := messages.ListMessages(ctx, scope, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("messages after the purge = %d, want none", len(page.Items))
	}
}

// The local working copy goes with the project. It is pod-local until Cut
// D.3, so this only proves the leader's own copy.
func TestTeardownRemovesTheLocalWorkspaceTree(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	if _, err := workspaces.PutFile(ctx, scope, workspace.PutOptions{Path: "index.html", Data: []byte("hi\n")}); err != nil {
		t.Fatal(err)
	}
	(&Reconciler{Workspace: workspaces}).removeWorkspaceTree(ctx, scope)
	if _, err := workspaces.ReadFileBytes(ctx, scope, "index.html", 0); err == nil {
		t.Fatal("the project's local tree survived its deletion")
	}
}
