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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/bindings"
	"github.com/railgrid/provider-app-studio/controller/tenantwatch"
)

func watchedObject(name string, labels map[string]string, spec map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": name}}}
	if len(labels) > 0 {
		obj.SetLabels(labels)
	}
	if spec != nil {
		obj.Object["spec"] = spec
	}
	return obj
}

// The dependency kinds arrive on the per-workspace watch, so what has to be
// right is the mapping from a watched object back to the Project it belongs
// to. Each kind answers that question differently, and a wrong answer is
// either a project that never converges or a reconcile storm.
func TestMapDependencyEventResolvesTheOwningProject(t *testing.T) {
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
	r := &Reconciler{}
	names := func(evt tenantwatch.Event) []string {
		var out []string
		for _, target := range r.mapDependencyEvent(ctx, c, evt) {
			out = append(out, target.Name)
		}
		return out
	}
	one := func(t *testing.T, got []string, want string) {
		t.Helper()
		if len(got) != 1 || got[0] != want {
			t.Fatalf("owners = %v, want [%s]", got, want)
		}
	}

	// Instances map through the project label the reconciler stamps; an
	// instance without it (a run sandbox the API layer owns, or the Studio's
	// shared search backend) maps to nothing.
	one(t, names(tenantwatch.Event{GVR: tenantwatch.InstancesGVR, Object: watchedObject("shop-dev", map[string]string{bindings.ProjectLabel: "shop"}, nil)}), "shop")
	if got := names(tenantwatch.Event{GVR: tenantwatch.InstancesGVR, Object: watchedObject("sandbox", map[string]string{"railgrid.ai/app-studio-run-sandbox": "true"}, nil)}); len(got) != 0 {
		t.Fatalf("unowned instance → %v, want none", got)
	}

	// Repositories map through the claim label, else through the Project that
	// binds them by name — an adopted repository carries no label.
	one(t, names(tenantwatch.Event{GVR: tenantwatch.RepositoriesGVR, Object: watchedObject("shop-repo", map[string]string{projectRepositoryLabel: "shop"}, nil)}), "shop")
	one(t, names(tenantwatch.Event{GVR: tenantwatch.RepositoriesGVR, Object: watchedObject("blog-repo", nil, nil)}), "blog")

	// RepositoryCommits name only their repository.
	one(t, names(tenantwatch.Event{GVR: tenantwatch.RepositoryCommitsGVR, Object: watchedObject("commit-1", nil, map[string]any{"repositoryRef": "shop-repo"})}), "shop")
	if got := names(tenantwatch.Event{GVR: tenantwatch.RepositoryCommitsGVR, Object: watchedObject("commit-2", nil, map[string]any{"repositoryRef": "unknown-repo"})}); len(got) != 0 {
		t.Fatalf("commit for an unbound repository → %v, want none", got)
	}

	// A kind nothing subscribes to, and an event with no object, name nothing
	// rather than guessing or panicking.
	if got := names(tenantwatch.Event{GVR: schema.GroupVersionResource{Group: "x", Version: "v1", Resource: "things"}, Object: watchedObject("thing", nil, nil)}); len(got) != 0 {
		t.Fatalf("unknown kind → %v", got)
	}
	if got := names(tenantwatch.Event{GVR: tenantwatch.InstancesGVR}); len(got) != 0 {
		t.Fatalf("empty event → %v", got)
	}
	// A mapping that needs a client and has none returns nothing: the local
	// (unnamed) manager engages no cluster client.
	if got := r.mapDependencyEvent(ctx, nil, tenantwatch.Event{GVR: tenantwatch.RepositoryCommitsGVR, Object: watchedObject("commit-1", nil, map[string]any{"repositoryRef": "shop-repo"})}); len(got) != 0 {
		t.Fatalf("nil client → %v", got)
	}
}

// The watched kinds and the identity's composition are one contract: a kind
// the reconciler watches but the identity may not list is a watch that fails
// with 403 forever.
func TestDependencyKindsAreTheComposedKinds(t *testing.T) {
	want := map[string]bool{"instances": true, "repositories": true, "repositorycommits": true}
	if len(dependencyKinds) != len(want) {
		t.Fatalf("dependencyKinds = %v", dependencyKinds)
	}
	for _, gvr := range dependencyKinds {
		if !want[gvr.Resource] {
			t.Fatalf("watched kind %q is outside the declared composition", gvr.Resource)
		}
	}
}
