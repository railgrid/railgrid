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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/bindings"
)

func watchedObject(gvk schema.GroupVersionKind, name string, labels map[string]string, spec map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": name}}}
	obj.SetGroupVersionKind(gvk)
	if len(labels) > 0 {
		obj.SetLabels(labels)
	}
	if spec != nil {
		obj.Object["spec"] = spec
	}
	return obj
}

// The dependency kinds now ride the manager's own informer, so what has to be
// right is the mapping from a watched object back to the Project it belongs
// to. Each kind answers that question differently, and a wrong answer is
// either a project that never converges or a reconcile storm.
func TestDependencyOwnersResolveTheOwningProject(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := func(name, repositoryRef string) *aiv1alpha1.Project {
		p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if repositoryRef != "" {
			p.Spec.Repository = &aiv1alpha1.ProjectRepositoryBinding{RepositoryRef: repositoryRef}
		}
		return p
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		project("shop", "shop-repo"),
		project("blog", "blog-repo"),
		project("none", ""),
	).Build()
	ctx := context.Background()
	one := func(t *testing.T, got []string, want string) {
		t.Helper()
		if len(got) != 1 || got[0] != want {
			t.Fatalf("owners = %v, want [%s]", got, want)
		}
	}

	// Instances map through the project label the reconciler stamps; an
	// instance without it (a run sandbox the API layer owns, or the Studio's
	// shared search backend) maps to nothing.
	one(t, instanceOwner(ctx, c, watchedObject(instanceGVK, "shop-dev", map[string]string{bindings.ProjectLabel: "shop"}, nil)), "shop")
	if got := instanceOwner(ctx, c, watchedObject(instanceGVK, "sandbox", map[string]string{"railgrid.ai/app-studio-run-sandbox": "true"}, nil)); len(got) != 0 {
		t.Fatalf("unowned instance → %v, want none", got)
	}

	// Repositories map through the claim label, else through the Project that
	// binds them by name — an adopted repository carries no label.
	one(t, repositoryOwner(ctx, c, watchedObject(repositoryGVK, "shop-repo", map[string]string{projectRepositoryLabel: "shop"}, nil)), "shop")
	one(t, repositoryOwner(ctx, c, watchedObject(repositoryGVK, "blog-repo", nil, nil)), "blog")

	// RepositoryCommits name only their repository.
	commit := func(name, repositoryRef string) client.Object {
		return watchedObject(repositoryCommitGVK, name, nil, map[string]any{"repositoryRef": repositoryRef})
	}
	one(t, repositoryCommitOwner(ctx, c, commit("commit-1", "shop-repo")), "shop")
	if got := repositoryCommitOwner(ctx, c, commit("commit-2", "unknown-repo")); len(got) != 0 {
		t.Fatalf("commit for an unbound repository → %v, want none", got)
	}

	// A mapping that needs a client and has none returns nothing rather than
	// panicking: the local (unnamed) manager engages no cluster client.
	if got := repositoryCommitOwner(ctx, nil, commit("commit-1", "shop-repo")); len(got) != 0 {
		t.Fatalf("nil client → %v", got)
	}
	// A typed object arriving on the commit watch is not one this mapping can
	// read; it names nothing rather than guessing.
	if got := repositoryCommitOwner(ctx, c, &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "shop"}}); len(got) != 0 {
		t.Fatalf("unexpected object kind → %v", got)
	}
}
