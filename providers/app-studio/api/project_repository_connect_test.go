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
	"net/http/httptest"
	"strings"
	"testing"

	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

func TestOptionalGitCreationEndpoints(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, mode := range []string{"none", "auto", "create"} {
			t.Run(mode+map[bool]string{true: "-stream", false: "-plain"}[stream], func(t *testing.T) {
				dyn := newProjectCreationTestDynamicClient()
				codeCalls := 0
				dyn.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
					if action.GetResource().Group == codeAPIGroup {
						codeCalls++
						return true, nil, errors.New("the server could not find the requested resource")
					}
					return false, nil, nil
				})
				server := &Server{projectClientFor: func(identity) (*asclient.Client, error) { return asclient.NewFromDynamic(dyn), nil }}
				req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"displayName":"Demo","repositoryMode":"`+mode+`"}`))
				setPublishingIdentity(req)
				response := httptest.NewRecorder()
				if stream {
					server.createProjectStream(response, req)
				} else {
					server.createProject(response, req)
				}
				projects, err := asclient.NewFromDynamic(dyn).Projects().List(context.Background(), metav1.ListOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "create" {
					if len(projects.Items) != 0 {
						t.Fatal("Git-required request created a project")
					}
					if stream && !strings.Contains(response.Body.String(), "event: error") {
						t.Fatal(response.Body.String())
					}
					if !stream && response.Code < 400 {
						t.Fatal(response.Body.String())
					}
				} else {
					if len(projects.Items) != 1 || projects.Items[0].Spec.Repository != nil {
						t.Fatalf("projects = %#v; response %s", projects.Items, response.Body.String())
					}
					if strings.Contains(response.Body.String(), "Creating repository") {
						t.Fatal("reported nonexistent repository")
					}
				}
				if mode == "none" && codeCalls != 0 {
					t.Fatalf("none made %d Code calls", codeCalls)
				}
			})
		}
	}
}

func TestOptionalGitSelectionAndValidation(t *testing.T) {
	for _, req := range []CreateProjectRequest{
		{RepositoryMode: "invalid"}, {RepositoryMode: "none", ConnectionRef: "github"}, {RepositoryMode: "none", ExistingRepositoryRef: "repo"},
	} {
		if validateProjectRepositoryMode(req) == nil {
			t.Fatalf("accepted %#v", req)
		}
	}
	for _, status := range []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionFalse} {
		client := newProjectCreationTestClient(codeConnectionObjectWithValidated("github", status))
		plan, err := (&Server{}).prepareOptionalProjectRepository(context.Background(), client, CreateProjectRequest{DisplayName: "Demo"}, "demo")
		if err != nil || (plan.Ref != "") != (status == metav1.ConditionTrue) {
			t.Fatalf("plan=%#v err=%v", plan, err)
		}
		_, err = (&Server{}).prepareOptionalProjectRepository(context.Background(), client, CreateProjectRequest{DisplayName: "Demo", ConnectionRef: "github"}, "demo")
		if (err == nil) != (status == metav1.ConditionTrue) {
			t.Fatalf("explicit connection err=%v", err)
		}
	}
	dyn := newProjectCreationTestDynamicClient()
	dyn.PrependReactor("list", "connections", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("discovery unavailable")
	})
	if _, err := (&Server{}).prepareOptionalProjectRepository(context.Background(), asclient.NewFromDynamic(dyn), CreateProjectRequest{}, "demo"); err == nil {
		t.Fatal("masked discovery failure")
	}
}

func TestConnectRepositoryIsIdempotentAndPermissionScoped(t *testing.T) {
	dyn := newProjectCreationTestDynamicClient(codeConnectionObjectWithValidated("github", metav1.ConditionTrue))
	client := asclient.NewFromDynamic(dyn)
	project, err := (&Server{}).createProjectFromRequest(context.Background(), client, identity{orgUUID: "org-a", workspaceUUID: "ws-1"}, CreateProjectRequest{DisplayName: "Demo", RepositoryMode: "none"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{projectClientFor: func(id identity) (*asclient.Client, error) {
		if id.orgUUID != "org-a" || id.workspaceUUID != "ws-1" || id.user != "alice" {
			t.Fatalf("wrong identity %#v", id)
		}
		return client, nil
	}}
	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/projects/demo/repository", strings.NewReader(body))
		setPublishingIdentity(req)
		req = mux.SetURLVars(req, map[string]string{"project": project.Name})
		response := httptest.NewRecorder()
		server.putProjectRepository(response, req)
		return response
	}
	for i := 0; i < 2; i++ {
		response := call(`{"connectionRef":"github"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("connect %d: %d %s", i, response.Code, response.Body.String())
		}
	}
	attached, err := client.Projects().Get(context.Background(), project.Name, metav1.GetOptions{})
	if err != nil || attached.Spec.Repository == nil || attached.Annotations["ai.faros.sh/initialize-repository"] != attached.Spec.Repository.RepositoryRef {
		t.Fatalf("attachment=%#v err=%v", attached, err)
	}
	if attached.Spec.Repository.Name == project.Name || !strings.HasPrefix(attached.Spec.Repository.Name, project.Name+"-") {
		t.Fatalf("connect later must reserve a fresh remote name: %q", attached.Spec.Repository.Name)
	}
	if response := call(`{"connectionRef":"another"}`); response.Code != http.StatusConflict {
		t.Fatalf("replacement accepted: %d", response.Code)
	}
	dyn.PrependReactor("update", "projects", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(action.GetResource().GroupResource(), project.Name, errors.New("denied"))
	})
	if response := call(`{"connectionRef":"github"}`); response.Code != http.StatusForbidden {
		t.Fatalf("unauthorized retry: %d %s", response.Code, response.Body.String())
	}
}

func TestOptionalGitGraphQLMissingProviderIsAdvisory(t *testing.T) {
	dyn := newProjectCreationTestDynamicClient()
	dyn.PrependReactor("list", "connections", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New(`Cannot query field "code_faros_sh" on type "Query". Did you mean "ai_faros_sh"?`)
	})
	client := asclient.NewFromDynamic(dyn)
	readiness, err := projectCreateReadiness(context.Background(), client)
	if err != nil || readiness.GitConnection.Status != projectCreateGitStatusProviderMissing {
		t.Fatalf("readiness=%#v err=%v", readiness, err)
	}
	plan, err := (&Server{}).prepareOptionalProjectRepository(context.Background(), client, CreateProjectRequest{}, "demo")
	if err != nil || plan.projectBinding() != nil {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if codeProviderResourceMissing(errors.New(`Cannot query field "ai_faros_sh" on type "Query"`)) {
		t.Fatal("misclassified App Studio initialization")
	}
	if codeProviderResourceMissing(errors.New("forbidden: code_faros_sh")) {
		t.Fatal("misclassified authorization failure")
	}
}

func TestConnectLaterReservesDifferentRepositories(t *testing.T) {
	c := newProjectCreationTestClient(codeConnectionObjectWithValidated("github", metav1.ConditionTrue))
	s := &Server{projectClientFor: func(identity) (*asclient.Client, error) { return c, nil }}
	refs := []string{}
	for _, name := range []string{"project-one", "project-two"} {
		p, err := s.createProjectFromRequest(context.Background(), c, identity{orgUUID: "org-a", workspaceUUID: "ws-1"}, CreateProjectRequest{Name: name, DisplayName: "Demo", RepositoryMode: "none"}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPut, "/api/projects/"+name+"/repository", strings.NewReader(`{"connectionRef":"github"}`))
		setPublishingIdentity(req)
		req = mux.SetURLVars(req, map[string]string{"project": p.Name})
		rec := httptest.NewRecorder()
		s.putProjectRepository(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("connect: %d %s", rec.Code, rec.Body.String())
		}
		p, err = c.Projects().Get(context.Background(), p.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, p.Spec.Repository.RepositoryRef)
	}
	if refs[0] == refs[1] {
		t.Fatalf("distinct projects attached to the same repository before reconciliation: %v", refs)
	}
}

func TestRepositoryClaimTransfersProjectIncarnationOnImport(t *testing.T) {
	ctx := context.Background()
	repo := codeRepositoryObject("repo", "repo", "github", true)
	repo.SetAnnotations(map[string]string{projectRepositoryUIDAnnotation: "old-uid"})
	c := newProjectCreationTestClient(repo)
	plan := projectRepositoryPlan{Ref: "repo", Adopted: true}
	if err := claimProjectRepository(ctx, c, "demo", "new-uid", plan); err != nil {
		t.Fatal(err)
	}
	claimed, err := c.Resource(codeRepositoryResource, "").Get(ctx, "repo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.GetLabels()[projectRepositoryProjectLabel] != "demo" || claimed.GetAnnotations()[projectRepositoryUIDAnnotation] != "new-uid" {
		t.Fatalf("wrong repository owner: %v %v", claimed.GetLabels(), claimed.GetAnnotations())
	}
	if err := releaseProjectRepository(ctx, c, "repo"); err != nil {
		t.Fatal(err)
	}
	released, err := c.Resource(codeRepositoryResource, "").Get(ctx, "repo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if released.GetLabels()[projectRepositoryProjectLabel] != "" || released.GetAnnotations()[projectRepositoryUIDAnnotation] != "" {
		t.Fatalf("released repository still has an owner: %v %v", released.GetLabels(), released.GetAnnotations())
	}
}
