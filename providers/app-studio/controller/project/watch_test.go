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

func TestMapDependencyEventResolvesOwningProject(t *testing.T) {
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
	r := &Reconciler{}
	names := func(evt tenantwatch.Event) []string {
		var out []string
		for _, nn := range r.mapDependencyEvent(context.Background(), c, evt) {
			out = append(out, nn.Name)
		}
		return out
	}

	// Instances map through the project label the reconciler stamps; an
	// instance without it (a sandbox the API layer owns) maps to nothing.
	if got := names(tenantwatch.Event{GVR: tenantwatch.InstancesGVR, Object: watchedObject("shop-dev", map[string]string{bindings.ProjectLabel: "shop"}, nil)}); len(got) != 1 || got[0] != "shop" {
		t.Fatalf("instance → %v, want [shop]", got)
	}
	if got := names(tenantwatch.Event{GVR: tenantwatch.InstancesGVR, Object: watchedObject("sandbox", map[string]string{"railgrid.ai/app-studio-run-sandbox": "true"}, nil)}); len(got) != 0 {
		t.Fatalf("unowned instance → %v, want none", got)
	}

	// Repositories map through the claim label, else through the Project
	// that binds them by name.
	if got := names(tenantwatch.Event{GVR: tenantwatch.RepositoriesGVR, Object: watchedObject("shop-repo", map[string]string{projectRepositoryLabel: "shop"}, nil)}); len(got) != 1 || got[0] != "shop" {
		t.Fatalf("labelled repository → %v, want [shop]", got)
	}
	if got := names(tenantwatch.Event{GVR: tenantwatch.RepositoriesGVR, Object: watchedObject("blog-repo", nil, nil)}); len(got) != 1 || got[0] != "blog" {
		t.Fatalf("adopted repository → %v, want [blog]", got)
	}

	// RepositoryCommits name only their repository.
	if got := names(tenantwatch.Event{GVR: tenantwatch.RepositoryCommitsGVR, Object: watchedObject("commit-1", nil, map[string]any{"repositoryRef": "shop-repo"})}); len(got) != 1 || got[0] != "shop" {
		t.Fatalf("commit → %v, want [shop]", got)
	}
	if got := names(tenantwatch.Event{GVR: tenantwatch.RepositoryCommitsGVR, Object: watchedObject("commit-2", nil, map[string]any{"repositoryRef": "unknown-repo"})}); len(got) != 0 {
		t.Fatalf("commit for an unbound repository → %v, want none", got)
	}

	// Kinds the reconciler does not watch, a nil object, and a nil client
	// are all inert.
	if got := names(tenantwatch.Event{GVR: schema.GroupVersionResource{Group: "x", Version: "v1", Resource: "things"}, Object: watchedObject("thing", nil, nil)}); len(got) != 0 {
		t.Fatalf("unknown kind → %v", got)
	}
	if got := names(tenantwatch.Event{GVR: tenantwatch.InstancesGVR}); len(got) != 0 {
		t.Fatalf("nil object → %v", got)
	}
	if got := r.mapDependencyEvent(context.Background(), nil, tenantwatch.Event{GVR: tenantwatch.RepositoryCommitsGVR, Object: watchedObject("commit-1", nil, map[string]any{"repositoryRef": "shop-repo"})}); len(got) != 0 {
		t.Fatalf("nil client → %v", got)
	}
}
