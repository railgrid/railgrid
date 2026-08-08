/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	appscheme "github.com/faroshq/provider-app-studio/scheme"
)

func repoProject(adopted bool) *aiv1alpha1.Project {
	return &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-1")},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Demo",
			Repository: &aiv1alpha1.ProjectRepositoryBinding{
				RepositoryRef: "demo-repo",
				Name:          "demo-repo",
				ConnectionRef: "github",
				Adopted:       adopted,
			},
		},
	}
}

func TestEnsureRepositoryCreatesForNonAdopted(t *testing.T) {
	scheme := appscheme.NewScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{}

	if _, err := r.ensureRepository(context.Background(), c, repoProject(false)); err != nil {
		t.Fatalf("ensureRepository: %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(repositoryGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "demo-repo"}, got); err != nil {
		t.Fatalf("Repository CR was not created: %v", err)
	}
	if got.GetLabels()[projectRepositoryLabel] != "demo" {
		t.Fatalf("labels = %v, want %s=demo", got.GetLabels(), projectRepositoryLabel)
	}
	autoInit, _, _ := unstructured.NestedBool(got.Object, "spec", "autoInit")
	if !autoInit {
		t.Fatalf("spec.autoInit not set")
	}
	connectionRef, _, _ := unstructured.NestedString(got.Object, "spec", "connectionRef")
	if connectionRef != "github" {
		t.Fatalf("spec.connectionRef = %q", connectionRef)
	}
}

func TestEnsureRepositoryNeverCreatesAdopted(t *testing.T) {
	scheme := appscheme.NewScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{}

	repo, err := r.ensureRepository(context.Background(), c, repoProject(true))
	if err != nil {
		t.Fatalf("ensureRepository: %v", err)
	}
	if repo != nil {
		t.Fatalf("adopted binding returned an object: %v", repo)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(repositoryGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "demo-repo"}, got); err == nil {
		t.Fatal("adopted repository was (re)created — it must never be")
	}
}

func TestRepositoryReady(t *testing.T) {
	if repositoryReady(nil) {
		t.Fatal("nil repository reported ready")
	}
	obj := func(status string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{
				"conditions": []any{map[string]any{"type": "Ready", "status": status}},
			},
		}}
	}
	if !repositoryReady(obj("True")) {
		t.Fatal("Ready=True not detected")
	}
	if repositoryReady(obj("False")) {
		t.Fatal("Ready=False reported ready")
	}
}

func TestScopeOf(t *testing.T) {
	p := repoProject(false)
	if _, ok := scopeOf(p); ok {
		t.Fatal("legacy project without annotations must not produce a scope")
	}
	p.Annotations = map[string]string{
		orgUUIDAnnotation:       "org-1",
		workspaceUUIDAnnotation: "ws-1",
	}
	scope, ok := scopeOf(p)
	if !ok {
		t.Fatal("annotated project produced no scope")
	}
	if scope.OrgUUID != "org-1" || scope.WorkspaceUUID != "ws-1" || scope.ProjectName != "demo" || scope.ProjectUID != "uid-1" {
		t.Fatalf("scope = %+v", scope)
	}
}
