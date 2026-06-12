/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package client

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	aiv1alpha1 "github.com/faroshq/faros-kedge/apis/ai/v1alpha1"
	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

func TestTypedResourceCreateInjectsTypeMeta(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		OrganizationGVR: "OrganizationList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
	c := NewFromDynamic(dyn)

	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "test-org"},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName: "Test",
		},
	}
	created, err := c.Organizations().Create(context.Background(), org, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if created.Name != "test-org" {
		t.Errorf("expected name 'test-org', got %q", created.Name)
	}

	// Round-trip the unstructured via the fake to verify apiVersion+kind
	// were set on the wire payload.
	got, err := dyn.Resource(OrganizationGVR).Get(context.Background(), "test-org", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.GetAPIVersion() != "tenancy.kedge.faros.sh/v1alpha1" {
		t.Errorf("expected apiVersion 'tenancy.kedge.faros.sh/v1alpha1', got %q", got.GetAPIVersion())
	}
	if got.GetKind() != "Organization" {
		t.Errorf("expected kind 'Organization', got %q", got.GetKind())
	}
}

func TestTypedProjectResourceCreateInjectsTypeMeta(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		ProjectGVR: "ProjectList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
	c := NewFromDynamic(dyn)

	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "test-project"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Test",
		},
	}
	created, err := c.Projects().Create(context.Background(), project, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if created.Name != "test-project" {
		t.Errorf("expected name 'test-project', got %q", created.Name)
	}

	got, err := dyn.Resource(ProjectGVR).Get(context.Background(), "test-project", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.GetAPIVersion() != "ai.kedge.faros.sh/v1alpha1" {
		t.Errorf("expected apiVersion 'ai.kedge.faros.sh/v1alpha1', got %q", got.GetAPIVersion())
	}
	if got.GetKind() != "Project" {
		t.Errorf("expected kind 'Project', got %q", got.GetKind())
	}

	list, err := c.Projects().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "test-project" {
		t.Fatalf("unexpected list: %#v", list.Items)
	}
}
