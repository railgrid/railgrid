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
	"testing"

	"github.com/gorilla/mux"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		store: store.NewMemoryStore(),
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
	server := &Server{store: backend, projectClientFor: func(identity) (*asclient.Client, error) { return client, nil }}
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
