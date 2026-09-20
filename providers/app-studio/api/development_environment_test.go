/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/workspace"
)

// New projects get a development environment with NO binding: nothing runs
// until a template is bound (assistant interview, portal picker, or PUT
// /template). The legacy always-on SandboxRunner default is gone.
func TestDefaultProjectDevelopmentEnvironmentHasNoBinding(t *testing.T) {
	env := defaultProjectDevelopmentEnvironment("todo")
	if got, want := env.Name, "development"; got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
	if got, want := env.Mode, aiv1alpha1.ProjectEnvironmentModeLive; got != want {
		t.Fatalf("Mode = %q, want %q", got, want)
	}
	if got := len(env.Bindings); got != 0 {
		t.Fatalf("bindings = %d, want none until a template is selected", got)
	}
}

func TestProjectAssistantRuntimePreviewURLPrefersDevelopment(t *testing.T) {
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Status: aiv1alpha1.ProjectStatus{
			Environments: []aiv1alpha1.ProjectEnvironmentStatus{
				{
					Name: "test",
					Bindings: []aiv1alpha1.ProjectProviderBindingStatus{{
						Name:       "web",
						PreviewURL: "/test",
					}},
				},
				{
					Name: "development",
					Mode: aiv1alpha1.ProjectEnvironmentModeLive,
					Bindings: []aiv1alpha1.ProjectProviderBindingStatus{{
						Name:       "dev",
						Provider:   "app-studio",
						PreviewURL: "/dev",
					}},
				},
			},
		},
	}
	if got, want := projectAssistantRuntimePreviewURL(p), "/dev"; got != want {
		t.Fatalf("preview URL = %q, want %q", got, want)
	}
}

func TestCreateProjectSpecIncludesDevelopmentEnvironment(t *testing.T) {
	spec := defaultProjectSpec("todo", "Todo", "Tasks", &aiv1alpha1.ProjectRepositoryBinding{RepositoryRef: "todo"})
	if got := len(spec.Environments); got != 1 {
		t.Fatalf("environments = %d, want 1", got)
	}
	if got, want := spec.Environments[0].Name, "development"; got != want {
		t.Fatalf("environment name = %q, want %q", got, want)
	}
	if got, want := spec.Sharing.Preview.Mode, aiv1alpha1.ProjectSharingModePrivate; got != want {
		t.Fatalf("preview sharing mode = %q, want %q", got, want)
	}
	if got, want := spec.Sharing.Publishing.Mode, aiv1alpha1.ProjectSharingModePrivate; got != want {
		t.Fatalf("publishing sharing mode = %q, want %q", got, want)
	}
}

func TestProjectViewDefaultsMissingSharingToPrivate(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Todo",
		},
	}
	view := projectView(context.Background(), nil, project, identity{})
	if got, want := view.Sharing.Preview.Mode, aiv1alpha1.ProjectSharingModePrivate; got != want {
		t.Fatalf("preview sharing mode = %q, want %q", got, want)
	}
	if got, want := view.Sharing.Publishing.Mode, aiv1alpha1.ProjectSharingModePrivate; got != want {
		t.Fatalf("publishing sharing mode = %q, want %q", got, want)
	}
}

func TestProjectViewExposesImmutableIdentityAndDeletionState(t *testing.T) {
	now := metav1.Now()
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "todo",
			UID:               "project-uid",
			DeletionTimestamp: &now,
		},
		Spec: aiv1alpha1.ProjectSpec{DisplayName: "Todo"},
	}

	view := projectView(context.Background(), nil, project, identity{})
	if got, want := view.UID, "project-uid"; got != want {
		t.Fatalf("UID = %q, want %q", got, want)
	}
	if !view.Deleting {
		t.Fatal("Deleting = false, want true when metadata.deletionTimestamp is present")
	}
}

// defaultProjectSpec must not hand a new Project an exposed sharing policy:
// a project is private until someone uses the preview or publishing verb.
func TestDefaultProjectSpecStartsPrivate(t *testing.T) {
	spec := defaultProjectSpec("todo", "Todo", "Tasks", nil)
	got := effectiveProjectSharingSpec(spec.Sharing)
	if got.Preview.Mode != aiv1alpha1.ProjectSharingModePrivate || got.Publishing.Mode != aiv1alpha1.ProjectSharingModePrivate {
		t.Fatalf("default sharing = %+v, want private/private", got)
	}
}

func TestProjectAssistantPreviewRefreshNeededUsesSuccessfulMutatingToolCalls(t *testing.T) {
	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	if !server.projectAssistantPreviewRefreshNeeded(context.Background(), workspace.Scope{}, "", false, []projectToolCallStreamEvent{{
		Name:   projectToolEditFile,
		Status: "succeeded",
	}}) {
		t.Fatal("preview refresh = false, want true after successful workspace mutation")
	}
	if server.projectAssistantPreviewRefreshNeeded(context.Background(), workspace.Scope{}, "", false, []projectToolCallStreamEvent{{
		Name:   projectToolEditFile,
		Status: "failed",
	}}) {
		t.Fatal("preview refresh = true, want false after failed workspace mutation")
	}
	if server.projectAssistantPreviewRefreshNeeded(context.Background(), workspace.Scope{}, "", false, []projectToolCallStreamEvent{{
		Name:   projectToolReadFile,
		Status: "succeeded",
	}}) {
		t.Fatal("preview refresh = true, want false after read-only tool")
	}
}
