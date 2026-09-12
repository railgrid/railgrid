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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func recoveryFixture() (*aiv1alpha1.Project, *unstructured.Unstructured) {
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}, Spec: aiv1alpha1.ProjectSpec{DisplayName: "Demo", Repository: &aiv1alpha1.ProjectRepositoryBinding{RepositoryRef: "failed-repo", Name: "failed-repo", ConnectionRef: "github"}}}
	repo := codeRepositoryObject("failed-repo", "failed-repo", "github", false)
	repo.SetGeneration(1)
	repo.SetLabels(map[string]string{projectRepositoryProjectLabel: p.Name})
	repo.SetAnnotations(map[string]string{projectRepositoryUIDAnnotation: string(p.UID), "code.faros.sh/create-only": "true"})
	repo.Object["status"] = map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "RepositoryIdentityConflict"}}}
	return p, repo
}

func TestProjectRepositoryRecovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*aiv1alpha1.Project, *unstructured.Unstructured)
		allowed bool
	}{
		{name: "lost creation receipt", allowed: true},
		{name: "already confirmed", mutate: func(_ *aiv1alpha1.Project, r *unstructured.Unstructured) {
			r.Object["status"].(map[string]any)["repoID"] = "42"
		}},
		{name: "imported", mutate: func(p *aiv1alpha1.Project, _ *unstructured.Unstructured) { p.Spec.Repository.Adopted = true }},
		{name: "other incarnation", mutate: func(_ *aiv1alpha1.Project, r *unstructured.Unstructured) {
			r.SetAnnotations(map[string]string{"code.faros.sh/create-only": "true", projectRepositoryUIDAnnotation: "old-uid"})
		}},
		{name: "stale status", mutate: func(_ *aiv1alpha1.Project, r *unstructured.Unstructured) { r.SetGeneration(2) }},
		{name: "ordinary provisioning error", mutate: func(_ *aiv1alpha1.Project, r *unstructured.Unstructured) {
			r.Object["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "Ready", "status": "False", "reason": "EnsureFailed"}}
		}},
		{name: "ready", mutate: func(_ *aiv1alpha1.Project, r *unstructured.Unstructured) {
			r.Object["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "Ready", "status": "True"}}
		}},
		{name: "legacy creation", mutate: func(_ *aiv1alpha1.Project, r *unstructured.Unstructured) {
			a := r.GetAnnotations()
			delete(a, "code.faros.sh/create-only")
			r.SetAnnotations(a)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, repo := recoveryFixture()
			if tc.mutate != nil {
				tc.mutate(p, repo)
			}
			c := newProjectCreationTestClient(repo, codeConnectionObjectWithValidated("github", metav1.ConditionTrue))
			if _, err := c.Projects().Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			s := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, projectClientFor: func(identity) (*asclient.Client, error) { return c, nil }}
			before := repo.DeepCopy()
			view := projectRepositoryViewFromGetter(context.Background(), p, func(ctx context.Context, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error) {
				return c.Resource(codeResourceFor(gvr), "").Get(ctx, name, metav1.GetOptions{})
			})
			if view.CanRetryCreation != tc.allowed {
				t.Fatalf("view retryable=%v", view.CanRetryCreation)
			}
			body := `{"connectionRef":"github","retryRepositoryRef":"failed-repo","projectUID":"project-uid"}`
			call := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPut, "/api/projects/demo/repository", strings.NewReader(body))
				setPublishingIdentity(req)
				req = mux.SetURLVars(req, map[string]string{"project": "demo"})
				w := httptest.NewRecorder()
				s.putProjectRepository(w, req)
				return w
			}
			w := call()
			if !tc.allowed {
				if w.Code != http.StatusConflict {
					t.Fatalf("unsafe replacement: %d %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("retry: %d %s", w.Code, w.Body.String())
			}
			updated, err := c.Projects().Get(context.Background(), "demo", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if updated.Spec.Repository.RepositoryRef == "failed-repo" || updated.Annotations["ai.faros.sh/initialize-repository"] != updated.Spec.Repository.RepositoryRef {
				t.Fatal("did not reserve new source upload")
			}
			old, err := c.Resource(codeRepositoryResource, "").Get(context.Background(), "failed-repo", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(before)
			b, _ := json.Marshal(old)
			if string(a) != string(b) {
				t.Fatal("uncertain old resource was modified")
			}
			if again := call(); again.Code != http.StatusConflict {
				t.Fatalf("stale retry must not create another binding: %d", again.Code)
			}
		})
	}
}
