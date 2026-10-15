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
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	appscheme "github.com/railgrid/provider-app-studio/scheme"
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
	if got.GetAnnotations()["code.railgrid.ai/create-only"] != "true" {
		t.Fatal("new repositories must require creation, never remote adoption")
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

func TestEnsureRepositoryEnforcesProjectOwnership(t *testing.T) {
	for _, adopted := range []bool{false, true} {
		for _, tc := range []struct {
			name, owner, uid, connection string
			allowed                      bool
		}{
			{"owned", "demo", "uid-1", "github", true},
			{"legacy-owned", "demo", "", "github", true},
			{"unclaimed", "", "", "github", false},
			{"other-project", "another", "uid-2", "github", false},
			{"recreated-project", "demo", "old-uid", "github", false},
			{"other-connection", "demo", "uid-1", "another", false},
		} {
			t.Run(tc.name+map[bool]string{true: "-adopted", false: "-created"}[adopted], func(t *testing.T) {
				p := repoProject(adopted)
				repo := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": repositoryGVK.GroupVersion().String(), "kind": repositoryGVK.Kind,
					"metadata": map[string]any{"name": p.Spec.Repository.RepositoryRef,
						"labels":      map[string]any{projectRepositoryLabel: tc.owner},
						"annotations": map[string]any{projectRepositoryUIDAnnotation: tc.uid}},
					"spec": map[string]any{"connectionRef": tc.connection},
				}}
				c := fake.NewClientBuilder().WithScheme(appscheme.NewScheme()).WithRuntimeObjects(repo).Build()
				got, err := (&Reconciler{}).ensureRepository(context.Background(), c, p)
				if tc.allowed {
					if err != nil || got == nil {
						t.Fatalf("owned repository rejected: %v", err)
					}
				} else if err == nil || got != nil {
					t.Fatalf("unsafe repository accepted: %#v %v", got, err)
				}
			})
		}
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

func TestEnsureRepositoryRetriesRemoteNameCollisions(t *testing.T) {
	ctx := context.Background()
	p := repoProject(false)
	c := fake.NewClientBuilder().WithScheme(appscheme.NewScheme()).WithObjects(p).Build()
	r := &Reconciler{}
	seen := map[string]bool{}
	// More than one collision must keep progressing, including one on a
	// generated name. Successful provisioning must stop changing the binding.
	for attempt := 0; attempt < 7; attempt++ {
		ref := p.Spec.Repository.RepositoryRef
		if seen[ref] {
			t.Fatalf("reused colliding name %q", ref)
		}
		seen[ref] = true
		if _, err := r.ensureRepository(ctx, c, p); err != nil {
			t.Fatal(err)
		}
		repo := &unstructured.Unstructured{}
		repo.SetGroupVersionKind(repositoryGVK)
		if err := c.Get(ctx, types.NamespacedName{Name: ref}, repo); err != nil {
			t.Fatal(err)
		}
		repo.SetGeneration(1)
		repo.Object["status"] = map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "RepositoryIdentityConflict"}}}
		if err := c.Update(ctx, repo); err != nil {
			t.Fatal(err)
		}
		before := repo.DeepCopy()
		if got, err := r.ensureRepository(ctx, c, p); err != nil || got != nil {
			t.Fatalf("retry: %v %v", got, err)
		}
		saved := &aiv1alpha1.Project{}
		if err := c.Get(ctx, types.NamespacedName{Name: p.Name}, saved); err != nil {
			t.Fatal(err)
		}
		p = saved
		if p.Spec.Repository.RepositoryRef == ref || p.Spec.Repository.Name != p.Spec.Repository.RepositoryRef || p.Annotations[initializeRepositoryAnnotation] != p.Spec.Repository.RepositoryRef {
			t.Fatalf("new intent not persisted: %+v", p)
		}
		if err := c.Get(ctx, types.NamespacedName{Name: ref}, repo); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before.Object["spec"], repo.Object["spec"]) || !reflect.DeepEqual(before.Object["status"], repo.Object["status"]) {
			t.Fatal("changed conflicting repository spec or status")
		}
		if repo.GetLabels()[projectRepositoryLabel] != "" || repo.GetAnnotations()[projectRepositoryUIDAnnotation] != "" {
			t.Fatal("old claim retained")
		}
		if p.Annotations[repositoryNameAttemptsAnnotation] != strconv.Itoa(attempt+1) {
			t.Fatal("attempt not recorded")
		}
		if suffix := strings.TrimPrefix(p.Spec.Repository.RepositoryRef, "demo-"); len(suffix) != 12 || strings.Contains(suffix, "-") {
			t.Fatalf("bad suffix %q", suffix)
		}
	}
	if _, err := r.ensureRepository(ctx, c, p); err != nil {
		t.Fatal(err)
	}
	ref := p.Spec.Repository.RepositoryRef
	repo := &unstructured.Unstructured{}
	repo.SetGroupVersionKind(repositoryGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: ref}, repo); err != nil {
		t.Fatal(err)
	}
	repo.Object["status"] = map[string]any{"repoID": "42", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	if err := c.Update(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ensureRepository(ctx, c, p); err != nil || !repositoryReady(got) {
		t.Fatalf("ready: %v %v", got, err)
	}
	if p.Spec.Repository.RepositoryRef != ref {
		t.Fatal("renamed confirmed repository")
	}
}

func TestEnsureRepositoryDoesNotRetryUnsafeConflicts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*aiv1alpha1.Project, *unstructured.Unstructured)
	}{
		{"adopted", func(p *aiv1alpha1.Project, _ *unstructured.Unstructured) { p.Spec.Repository.Adopted = true }},
		{"confirmed identity", func(_ *aiv1alpha1.Project, r *unstructured.Unstructured) {
			r.Object["status"].(map[string]any)["repoID"] = "42"
		}},
		{"stale status", func(_ *aiv1alpha1.Project, r *unstructured.Unstructured) { r.SetGeneration(2) }},
		{"ordinary failure", func(_ *aiv1alpha1.Project, r *unstructured.Unstructured) {
			r.Object["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "Ready", "status": "False", "reason": "EnsureFailed"}}
		}},
		{"legacy ownership", func(_ *aiv1alpha1.Project, r *unstructured.Unstructured) {
			a := r.GetAnnotations()
			delete(a, projectRepositoryUIDAnnotation)
			r.SetAnnotations(a)
		}},
		{"legacy intent", func(_ *aiv1alpha1.Project, r *unstructured.Unstructured) {
			a := r.GetAnnotations()
			delete(a, "code.railgrid.ai/create-only")
			r.SetAnnotations(a)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			p := repoProject(false)
			c := fake.NewClientBuilder().WithScheme(appscheme.NewScheme()).WithObjects(p).Build()
			r := &Reconciler{}
			if _, err := r.ensureRepository(ctx, c, p); err != nil {
				t.Fatal(err)
			}
			repo := &unstructured.Unstructured{}
			repo.SetGroupVersionKind(repositoryGVK)
			if err := c.Get(ctx, types.NamespacedName{Name: p.Spec.Repository.RepositoryRef}, repo); err != nil {
				t.Fatal(err)
			}
			repo.SetGeneration(1)
			repo.Object["status"] = map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "RepositoryIdentityConflict"}}}
			tc.mutate(p, repo)
			if err := c.Update(ctx, repo); err != nil {
				t.Fatal(err)
			}
			if got, err := r.ensureRepository(ctx, c, p); err != nil || got == nil {
				t.Fatalf("got=%v error=%v", got, err)
			}
			if p.Spec.Repository.RepositoryRef != "demo-repo" {
				t.Fatal("changed unsafe binding")
			}
		})
	}
}

// Inject a crash at each write boundary, then reconstruct the reconciler from
// persisted state. Cleanup and CR creation must converge without new names.
func TestRepositoryCollisionCrashRecovery(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		for _, deleting := range []bool{false, true} {
			t.Run(fmt.Sprintf("write-%d-delete-%v", failAt, deleting), func(t *testing.T) {
				ctx := context.Background()
				p := repoProject(false)
				patches := 0
				inject := false
				c := fake.NewClientBuilder().WithScheme(appscheme.NewScheme()).WithObjects(p).WithInterceptorFuncs(interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						if inject {
							patches++
							if patches == failAt {
								return errors.New("simulated process failure")
							}
						}
						return c.Update(ctx, obj, opts...)
					},
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						patches++
						if inject && patches == failAt {
							return errors.New("simulated process failure")
						}
						return c.Patch(ctx, obj, patch, opts...)
					},
				}).Build()
				r := &Reconciler{}
				if _, err := r.ensureRepository(ctx, c, p); err != nil {
					t.Fatal(err)
				}
				old := &unstructured.Unstructured{}
				old.SetGroupVersionKind(repositoryGVK)
				if err := c.Get(ctx, types.NamespacedName{Name: "demo-repo"}, old); err != nil {
					t.Fatal(err)
				}
				old.SetGeneration(1)
				old.Object["status"] = map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "RepositoryIdentityConflict"}}}
				if err := c.Update(ctx, old); err != nil {
					t.Fatal(err)
				}
				inject = true
				if _, err := r.ensureRepository(ctx, c, p); err == nil {
					t.Fatal("expected injected failure")
				}
				inject = false
				if err := c.Get(ctx, types.NamespacedName{Name: p.Name}, p); err != nil {
					t.Fatal(err)
				}
				persisted := p.Spec.Repository.RepositoryRef
				if failAt == 1 && persisted != "demo-repo" {
					t.Fatal("failed write changed intent")
				}
				if deleting {
					if err := r.settleRepository(ctx, c, p); err != nil {
						t.Fatal(err)
					}
				} else {
					for i := 0; i < 3; i++ {
						if _, err := (&Reconciler{}).ensureRepository(ctx, c, p); err != nil {
							t.Fatal(err)
						}
						if err := c.Get(ctx, types.NamespacedName{Name: p.Name}, p); err != nil {
							t.Fatal(err)
						}
					}
					if failAt > 1 && persisted != p.Spec.Repository.RepositoryRef {
						t.Fatal("restart selected a second replacement")
					}
					list := &unstructured.UnstructuredList{}
					list.SetGroupVersionKind(repositoryGVK.GroupVersion().WithKind("RepositoryList"))
					if err := c.List(ctx, list); err != nil {
						t.Fatal(err)
					}
					if len(list.Items) != 2 {
						t.Fatalf("created %d repositories", len(list.Items))
					}
				}
				if err := c.Get(ctx, types.NamespacedName{Name: "demo-repo"}, old); err != nil {
					t.Fatal(err)
				}
				if old.GetLabels()[projectRepositoryLabel] != "" || old.GetAnnotations()[projectRepositoryUIDAnnotation] != "" {
					t.Fatal("superseded claim leaked")
				}
				if p.Annotations[supersededRepositoryAnnotation] != "" {
					t.Fatal("cleanup marker not cleared")
				}
			})
		}
	}
}

func TestRepositoryReplacementCandidateAlreadyExists(t *testing.T) {
	ctx := context.Background()
	p := repoProject(false)
	c := fake.NewClientBuilder().WithScheme(appscheme.NewScheme()).WithObjects(p).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if obj.GetObjectKind().GroupVersionKind() == repositoryGVK && key.Name != "demo-repo" {
				return nil
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
	r := &Reconciler{}
	if _, err := r.ensureRepository(ctx, c, p); err != nil {
		t.Fatal(err)
	}
	repo := &unstructured.Unstructured{}
	repo.SetGroupVersionKind(repositoryGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: "demo-repo"}, repo); err != nil {
		t.Fatal(err)
	}
	repo.SetGeneration(1)
	repo.Object["status"] = map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "RepositoryIdentityConflict"}}}
	if err := c.Update(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ensureRepository(ctx, c, p); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error=%v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: p.Name}, p); err != nil {
		t.Fatal(err)
	}
	if p.Spec.Repository.RepositoryRef != "demo-repo" {
		t.Fatal("changed intent despite collision")
	}
}
