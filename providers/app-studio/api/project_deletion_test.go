/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

type failOnceProjectDeleteStore struct {
	store.Store
	fail bool
}

func (s *failOnceProjectDeleteStore) DeleteProjectMessages(ctx context.Context, scope store.Scope) error {
	if s.fail {
		s.fail = false
		return errors.New("temporary attachment cleanup failure")
	}
	return s.Store.DeleteProjectMessages(ctx, scope)
}

func TestProjectViewProjectsImmutableUIDAndDeletionMetadata(t *testing.T) {
	deletionTimestamp := metav1.Now()
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "todo",
			UID:               types.UID("project-old"),
			DeletionTimestamp: &deletionTimestamp,
		},
		Spec: aiv1alpha1.ProjectSpec{DisplayName: "Todo"},
	}

	view := projectView(context.Background(), nil, project, identity{})
	if got, want := view.UID, "project-old"; got != want {
		t.Fatalf("UID = %q, want %q", got, want)
	}
	if !view.Deleting {
		t.Fatal("Deleting = false, want true for metadata.deletionTimestamp")
	}

	project.DeletionTimestamp = nil
	view = projectView(context.Background(), nil, project, identity{})
	if view.Deleting {
		t.Fatal("Deleting = true, want false when metadata.deletionTimestamp is absent")
	}
	if got, want := view.UID, "project-old"; got != want {
		t.Fatalf("UID after status-only refresh = %q, want %q", got, want)
	}
}

func TestDeleteProjectRequiresAndForwardsExpectedUID(t *testing.T) {
	dyn := publishingTestDynamic(publishingTestProject("demo", "project-current", ""))
	client := asclient.NewFromDynamic(dyn)
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		store:            store.NewMemoryStore(),
		projectClientFor: func(identity) (*asclient.Client, error) {
			return client, nil
		},
	}
	router := mux.NewRouter()
	server.Register(router)

	missing := publishingDo(t, router, http.MethodDelete, "/api/projects/demo", "")
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing UID status = %d: %s, want 400", missing.Code, missing.Body.String())
	}

	stale := publishingDo(t, router, http.MethodDelete, "/api/projects/demo?uid=project-old", "")
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale UID status = %d: %s, want 409", stale.Code, stale.Body.String())
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "delete" && action.GetResource().Resource == "projects" {
			t.Fatal("stale UID reached the Project delete client")
		}
	}

	accepted := publishingDo(t, router, http.MethodDelete, "/api/projects/demo?uid=project-current", "")
	if accepted.Code != http.StatusNoContent {
		t.Fatalf("matching UID status = %d: %s, want 204", accepted.Code, accepted.Body.String())
	}
	for _, action := range dyn.Actions() {
		deleteAction, ok := action.(k8stesting.DeleteAction)
		if !ok || action.GetResource().Resource != "projects" {
			continue
		}
		preconditions := deleteAction.GetDeleteOptions().Preconditions
		if preconditions == nil || preconditions.UID == nil || *preconditions.UID != types.UID("project-current") {
			t.Fatalf("delete preconditions = %#v, want UID project-current", preconditions)
		}
		return
	}
	t.Fatal("matching UID did not issue a Project delete")
}

func TestDeleteProjectRetriesCleanupForTerminatingProject(t *testing.T) {
	dyn := publishingTestDynamic(publishingTestProject("demo", "project-current", ""))
	dyn.PrependReactor("delete", "projects", func(action k8stesting.Action) (bool, runtime.Object, error) {
		object, err := dyn.Tracker().Get(asclient.ProjectGVR, "", "demo")
		if err != nil {
			return true, nil, err
		}
		project := object.DeepCopyObject()
		accessor, err := meta.Accessor(project)
		if err != nil {
			return true, nil, err
		}
		now := metav1.Now()
		accessor.SetDeletionTimestamp(&now)
		return true, nil, dyn.Tracker().Update(asclient.ProjectGVR, project, "")
	})
	client := asclient.NewFromDynamic(dyn)
	backend := &failOnceProjectDeleteStore{Store: store.NewMemoryStore(), fail: true}
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, store: backend, projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
	router := mux.NewRouter()
	server.Register(router)

	first := publishingDo(t, router, http.MethodDelete, "/api/projects/demo?uid=project-current", "")
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first cleanup status = %d: %s, want 500", first.Code, first.Body.String())
	}
	terminating, err := client.Projects().Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil || terminating.DeletionTimestamp == nil || len(terminating.Finalizers) == 0 {
		t.Fatalf("terminating project after cleanup failure = %#v, %v", terminating, err)
	}
	second := publishingDo(t, router, http.MethodDelete, "/api/projects/demo?uid=project-current", "")
	if second.Code != http.StatusNoContent {
		t.Fatalf("retry cleanup status = %d: %s, want 204", second.Code, second.Body.String())
	}
	cleaned, err := client.Projects().Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned.Finalizers) != 0 {
		t.Fatalf("cleanup finalizer after retry = %v", cleaned.Finalizers)
	}
}

func TestDeleteProjectRepositoryDeletionIsOptInAndRefusesAdopted(t *testing.T) {
	const projectUID = "project-current"
	project := func(adopted bool) *unstructured.Unstructured {
		typed := &aiv1alpha1.Project{
			TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
			ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID(projectUID)},
			Spec: aiv1alpha1.ProjectSpec{Repository: &aiv1alpha1.ProjectRepositoryBinding{
				RepositoryRef: "demo-repo", Name: "demo-repo", ConnectionRef: "github", Adopted: adopted,
			}},
		}
		object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(typed)
		if err != nil {
			t.Fatal(err)
		}
		return &unstructured.Unstructured{Object: object}
	}
	repository := func(adopted bool, claimedBy string) *unstructured.Unstructured {
		repo := codeRepositoryObject("demo-repo", "demo-repo", "github", true)
		repo.SetLabels(map[string]string{projectRepositoryProjectLabel: claimedBy})
		annotations := map[string]string{projectRepositoryProjectAnnotation: claimedBy, projectRepositoryUIDAnnotation: projectUID}
		if adopted {
			annotations[projectRepositoryAdoptedAnnotation] = "true"
		}
		repo.SetAnnotations(annotations)
		return repo
	}

	tests := []struct {
		name            string
		query           string
		project         *unstructured.Unstructured
		repository      *unstructured.Unstructured
		failRepoDelete  bool
		wantCode        int
		wantBody        string
		wantRepoDeleted bool
		wantProjectGone bool
	}{
		{name: "default keeps repository", query: "", project: project(false), repository: repository(false, "demo"), wantCode: http.StatusNoContent, wantProjectGone: true},
		{name: "opt-in deletes created repository", query: "&deleteRepository=true", project: project(false), repository: repository(false, "demo"), wantCode: http.StatusNoContent, wantRepoDeleted: true, wantProjectGone: true},
		{name: "opt-in refuses adopted binding", query: "&deleteRepository=true", project: project(true), repository: repository(true, "demo"), wantCode: http.StatusConflict, wantBody: "never deletes adopted repositories"},
		{name: "opt-in refuses adopted repository", query: "&deleteRepository=true", project: project(false), repository: repository(true, "demo"), wantCode: http.StatusConflict, wantBody: "never deletes adopted repositories"},
		{name: "opt-in refuses foreign repository", query: "&deleteRepository=true", project: project(false), repository: repository(false, "other"), wantCode: http.StatusConflict, wantBody: "is not owned by project"},
		{name: "opt-in surfaces repository delete failure", query: "&deleteRepository=true", project: project(false), repository: repository(false, "demo"), failRepoDelete: true, wantCode: http.StatusForbidden},
		{name: "opt-in without repository resource", query: "&deleteRepository=true", project: project(false), wantCode: http.StatusNoContent, wantProjectGone: true},
		{name: "invalid flag", query: "&deleteRepository=maybe", project: project(false), repository: repository(false, "demo"), wantCode: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := []runtime.Object{tt.project}
			if tt.repository != nil {
				objects = append(objects, tt.repository)
			}
			dyn := newProjectCreationTestDynamicClient(objects...)
			if tt.failRepoDelete {
				dyn.PrependReactor("delete", "repositories", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(action.GetResource().GroupResource(), "demo-repo", errors.New("denied"))
				})
			}
			client := asclient.NewFromDynamic(dyn)
			server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, store: store.NewMemoryStore(), projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
			router := mux.NewRouter()
			server.Register(router)

			response := publishingDo(t, router, http.MethodDelete, "/api/projects/demo?uid="+projectUID+tt.query, "")
			if response.Code != tt.wantCode {
				t.Fatalf("status = %d: %s, want %d", response.Code, response.Body.String(), tt.wantCode)
			}
			if !strings.Contains(response.Body.String(), tt.wantBody) {
				t.Fatalf("body = %s, want %q", response.Body.String(), tt.wantBody)
			}
			_, projectErr := client.Projects().Get(context.Background(), "demo", metav1.GetOptions{})
			if gone := apierrors.IsNotFound(projectErr); gone != tt.wantProjectGone {
				t.Fatalf("project gone = %t (%v), want %t", gone, projectErr, tt.wantProjectGone)
			}
			if tt.repository == nil {
				return
			}
			repo, repoErr := client.Resource(codeRepositoryResource, "").Get(context.Background(), "demo-repo", metav1.GetOptions{})
			if deleted := apierrors.IsNotFound(repoErr); deleted != tt.wantRepoDeleted {
				t.Fatalf("repository deleted = %t (%v), want %t", deleted, repoErr, tt.wantRepoDeleted)
			}
			if tt.wantProjectGone && !tt.wantRepoDeleted && repo.GetLabels()[projectRepositoryProjectLabel] != "" {
				t.Fatalf("kept repository labels = %v, want the project claim released", repo.GetLabels())
			}
		})
	}
}
