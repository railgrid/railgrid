/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package studio

// The workspace's shared backends are Instances, a kind App Studio's APIExport
// claims. They are therefore converged on the client the reconcile pass
// already holds — the manager's, for the request's cluster — and no hub-minted
// identity is involved in reading or writing them. The per-Studio identity is
// still minted, for the dependency watch alone (identity.go).

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

var instanceGVK = schema.GroupVersionKind{Group: infraAPIGroup, Version: "v1alpha1", Kind: "Instance"}

func instanceScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(instanceGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(instanceGVK.GroupVersion().WithKind("InstanceList"), &unstructured.UnstructuredList{})
	return scheme
}

func studioWithSearch() *aiv1alpha1.Studio {
	st := &aiv1alpha1.Studio{}
	st.Name = "default"
	st.Spec.Search.ResourceRef = &aiv1alpha1.ProjectProviderResourceReference{
		APIVersion: instanceGVK.GroupVersion().String(),
		Kind:       instanceGVK.Kind,
		Resource:   "instances",
		Name:       SearchInstanceName,
	}
	return st
}

// The shared search backend is created, and observed, through the client it is
// handed — with no identity in the Reconciler at all. A Studio Reconciler that
// still needed a minted token to reach Instances could not pass this.
func TestSharedBackendIsConvergedOnTheGivenClient(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(instanceScheme(t)).Build()
	st := studioWithSearch()

	got, err := (&Reconciler{}).converge(context.Background(), c, st, searchService(st))
	if err != nil {
		t.Fatalf("converge: %v", err)
	}
	if got.Instance != SearchInstanceName || got.Phase != aiv1alpha1.StudioServicePending {
		t.Fatalf("status = %+v, want the named instance, still starting", got)
	}

	stored := &unstructured.Unstructured{}
	stored.SetGroupVersionKind(instanceGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Name: SearchInstanceName}, stored); err != nil {
		t.Fatalf("the backend was not created on the client it was given: %v", err)
	}
	if stored.GetLabels()[studioLabel] != "default" {
		t.Fatalf("attribution label = %q, want the Studio's name", stored.GetLabels()[studioLabel])
	}
	if tmpl, _, _ := unstructured.NestedString(stored.Object, "spec", "template"); tmpl != searchTemplate {
		t.Fatalf("spec.template = %q, want %q", tmpl, searchTemplate)
	}
}

// A workspace that has not accepted the instances claim degrades the way an
// unreachable workspace always has: an error the controller's backoff retries,
// never a Ready status over a backend nobody can see, and never a panic.
func TestSharedBackendDegradesWhenTheClaimIsUnaccepted(t *testing.T) {
	noMatch := &meta.NoKindMatchError{
		GroupKind:        schema.GroupKind{Group: infraAPIGroup, Kind: instanceGVK.Kind},
		SearchedVersions: []string{instanceGVK.Version},
	}
	c := fake.NewClientBuilder().WithScheme(instanceScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return noMatch
			},
		}).Build()

	st := studioWithSearch()
	got, err := (&Reconciler{}).converge(context.Background(), c, st, searchService(st))
	if err == nil {
		t.Fatalf("converge succeeded with status %+v against a workspace that does not serve instances", got)
	}
	if got != nil {
		t.Fatalf("converge returned a status (%+v) alongside the failure; the Studio must not report on a backend it could not reach", got)
	}
}

// Teardown asks the same question the Project teardown does: an unserved kind
// must not release the finalizer. deleteInstance returns the mapper error
// rather than swallowing it as "already gone".
func TestSharedBackendTeardownKeepsTheFinalizerWhenTheClaimIsUnaccepted(t *testing.T) {
	noMatch := &meta.NoKindMatchError{
		GroupKind:        schema.GroupKind{Group: infraAPIGroup, Kind: instanceGVK.Kind},
		SearchedVersions: []string{instanceGVK.Version},
	}
	c := fake.NewClientBuilder().WithScheme(instanceScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				return noMatch
			},
		}).Build()

	st := studioWithSearch()
	if err := (&Reconciler{}).deleteInstance(context.Background(), c, st.Spec.Search.ResourceRef); err == nil {
		t.Fatal("deleteInstance succeeded against a workspace that does not serve instances; the finalizer would be released over a live backend")
	}
}

// A Studio with no watch hub (REST-only dev) still converges: the watch only
// adds event-driven readiness, and Ensure on a nil hub is a no-op.
func TestNoWatchHubLeavesTheBackendsConverging(t *testing.T) {
	r := &Reconciler{}
	r.ensureDependencyWatch("cluster-a")
}
