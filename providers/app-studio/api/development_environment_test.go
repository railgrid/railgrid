/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)




// New projects get a development environment with NO binding: nothing runs
// until a template is bound (assistant interview, portal picker, or PUT
// /template). The legacy always-on SandboxRunner default is gone.
func TestDefaultProjectDevelopmentEnvironmentHasNoBinding(t *testing.T) {
	t.Setenv("APP_STUDIO_PREVIEW_BASE_DOMAIN", "")
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
	t.Setenv("APP_STUDIO_PREVIEW_BASE_DOMAIN", "")
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

func TestApplyProjectPatchRequestPersistsSharing(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "todo"},
		Spec:       defaultProjectSpec("todo", "Todo", "Tasks", nil),
	}
	changed, err := applyProjectPatchRequest(project, PatchProjectRequest{
		Sharing: &aiv1alpha1.ProjectSharingSpec{
			Preview: aiv1alpha1.ProjectSharingPolicy{
				Mode: aiv1alpha1.ProjectSharingModeShared,
			},
			Publishing: aiv1alpha1.ProjectSharingPolicy{
				Mode: aiv1alpha1.ProjectSharingModePublic,
			},
		},
	})
	if err != nil {
		t.Fatalf("applyProjectPatchRequest returned error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if got, want := project.Spec.Sharing.Preview.Mode, aiv1alpha1.ProjectSharingModeShared; got != want {
		t.Fatalf("preview sharing mode = %q, want %q", got, want)
	}
	if got, want := project.Spec.Sharing.Publishing.Mode, aiv1alpha1.ProjectSharingModePublic; got != want {
		t.Fatalf("publishing sharing mode = %q, want %q", got, want)
	}
}


func TestProjectAssistantPreviewRefreshNeededUsesSuccessfulMutatingToolCalls(t *testing.T) {
	server := NewWithWorkspace(nil, nil, nil, "http://hub.example", false)
	if !server.projectAssistantPreviewRefreshNeeded(context.Background(), workspace.Scope{}, "", false, []projectToolCallStreamEvent{{
		Name:   projectToolWriteFile,
		Status: "succeeded",
	}}) {
		t.Fatal("preview refresh = false, want true after successful workspace mutation")
	}
	if server.projectAssistantPreviewRefreshNeeded(context.Background(), workspace.Scope{}, "", false, []projectToolCallStreamEvent{{
		Name:   projectToolWriteFile,
		Status: "failed",
	}}) {
		t.Fatal("preview refresh = true, want false after failed workspace mutation")
	}
	if server.projectAssistantPreviewRefreshNeeded(context.Background(), workspace.Scope{}, "", false, []projectToolCallStreamEvent{{
		Name:   projectToolReadProjectFile,
		Status: "succeeded",
	}}) {
		t.Fatal("preview refresh = true, want false after read-only tool")
	}
}






















type previewOverlayProbeEngine struct {
	previewURL string
}

func (e *previewOverlayProbeEngine) StreamProjectAssistant(_ context.Context, req projectAssistantRunRequest) (projectAssistantRunResult, error) {
	e.previewURL = projectAssistantRuntimePreviewURL(req.Project)
	return projectAssistantRunResult{Content: "ok"}, nil
}

func (e *previewOverlayProbeEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, fmt.Errorf("unexpected resume")
}


