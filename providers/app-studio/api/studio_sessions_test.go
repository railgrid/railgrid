/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
)

func studioSharedServiceTemplate(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": templatesGVR.GroupVersion().String(),
		"kind":       templateResource.Kind,
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"backend": "kro"},
	}}
}

// studioMissingSearchRef is the shape the bug left behind: a Studio written
// while the searxng Template was not yet visible in the workspace (its
// infrastructure APIBinding had not landed), so spec.search carries a size but
// no resourceRef, while browser was retrofitted later and is complete.
func studioMissingSearchRef() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Studio",
		"metadata":   map[string]any{"name": aiv1alpha1.StudioName},
		"spec": map[string]any{
			"search": map[string]any{"size": "small"},
			"browser": map[string]any{
				"size": "small",
				"resourceRef": map[string]any{
					"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
					"kind":       "Instance",
					"name":       studioBrowserInstanceName,
					"resource":   "instances",
				},
			},
		},
	}}
}

func studioServiceRef(t *testing.T, c *asclient.Client, service string) map[string]any {
	t.Helper()
	st, err := c.Resource(studioResource, "").Get(context.Background(), aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ref, _, _ := unstructured.NestedMap(st.Object, "spec", service, "resourceRef")
	return ref
}

func TestRetrofitStudioServicesBackfillsSearch(t *testing.T) {
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(),
		studioMissingSearchRef(),
		studioSharedServiceTemplate("searxng"),
		studioSharedServiceTemplate("browser"),
	)
	c := asclient.NewFromDynamic(dynamicClient)
	server := &Server{}

	st, err := c.Resource(studioResource, "").Get(context.Background(), aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !server.retrofitStudioServices(context.Background(), c, st) {
		t.Fatal("retrofit reported incomplete although both templates resolve")
	}

	search := studioServiceRef(t, c, "search")
	if search["resource"] != "instances" || search["name"] != studioSearchInstanceName || search["kind"] != "Instance" {
		t.Fatalf("search resourceRef = %#v, want instances/%s", search, studioSearchInstanceName)
	}
	browser := studioServiceRef(t, c, "browser")
	if browser["name"] != studioBrowserInstanceName {
		t.Fatalf("browser resourceRef = %#v, want it left as it was", browser)
	}
}

func TestRetrofitStudioServicesSkipsDisabledService(t *testing.T) {
	st := studioMissingSearchRef()
	_ = unstructured.SetNestedField(st.Object, true, "spec", "search", "disabled")
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(), st, studioSharedServiceTemplate("searxng"), studioSharedServiceTemplate("browser"))
	c := asclient.NewFromDynamic(dynamicClient)
	server := &Server{}

	if !server.retrofitStudioServices(context.Background(), c, st.DeepCopy()) {
		t.Fatal("a disabled service must not count as unresolved")
	}
	if ref := studioServiceRef(t, c, "search"); len(ref) != 0 {
		t.Fatalf("disabled search gained a resourceRef: %#v", ref)
	}
	if len(dynamicClient.Actions()) != 1 { // the Get in studioServiceRef only; no Update
		t.Fatalf("expected no write for a disabled service, got actions %v", dynamicClient.Actions())
	}
}

func TestEnsureStudioRetriesUntilSearchTemplateResolves(t *testing.T) {
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(),
		studioMissingSearchRef(),
		studioSharedServiceTemplate("browser"),
	)
	c := asclient.NewFromDynamic(dynamicClient)
	server := &Server{}
	id := identity{clusterID: "ensure-studio-retries-" + t.Name()}
	t.Cleanup(func() { studioEnsured.Delete(id.clusterID) })

	// The searxng Template is not visible yet: the Studio stays as it is and
	// the workspace is NOT marked ensured, so the next call tries again.
	server.ensureStudio(context.Background(), c, id)
	if ref := studioServiceRef(t, c, "search"); len(ref) != 0 {
		t.Fatalf("search gained a resourceRef without a template: %#v", ref)
	}
	if _, done := studioEnsured.Load(id.clusterID); done {
		t.Fatal("workspace marked ensured while search is still unresolved")
	}

	// The APIBinding lands and the Template appears: the next project
	// creation retrofits search and the workspace is finally ensured.
	if err := dynamicClient.Tracker().Add(studioSharedServiceTemplate("searxng")); err != nil {
		t.Fatal(err)
	}
	server.ensureStudio(context.Background(), c, id)
	if ref := studioServiceRef(t, c, "search"); ref["name"] != studioSearchInstanceName {
		t.Fatalf("search resourceRef = %#v after the template appeared, want %s", ref, studioSearchInstanceName)
	}
	if _, done := studioEnsured.Load(id.clusterID); !done {
		t.Fatal("workspace not marked ensured once every service resolved")
	}

	// Ensured: no further reads or writes for this workspace.
	before := len(dynamicClient.Actions())
	server.ensureStudio(context.Background(), c, id)
	if got := len(dynamicClient.Actions()); got != before {
		t.Fatalf("ensured workspace still touched the Studio: %d new actions", got-before)
	}
}

func TestEnsureStudioCreatesWithBothReferencesAndRetriesUnresolved(t *testing.T) {
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(),
		studioSharedServiceTemplate("browser"),
	)
	c := asclient.NewFromDynamic(dynamicClient)
	server := &Server{}
	id := identity{clusterID: "ensure-studio-create-" + t.Name()}
	t.Cleanup(func() { studioEnsured.Delete(id.clusterID) })

	server.ensureStudio(context.Background(), c, id)
	if ref := studioServiceRef(t, c, "browser"); ref["name"] != studioBrowserInstanceName {
		t.Fatalf("browser resourceRef = %#v on the created Studio", ref)
	}
	if ref := studioServiceRef(t, c, "search"); len(ref) != 0 {
		t.Fatalf("search resourceRef = %#v although searxng is not resolvable", ref)
	}
	if _, done := studioEnsured.Load(id.clusterID); done {
		t.Fatal("workspace marked ensured although search is unresolved")
	}

	if err := dynamicClient.Tracker().Add(studioSharedServiceTemplate("searxng")); err != nil {
		t.Fatal(err)
	}
	server.ensureStudio(context.Background(), c, id)
	if ref := studioServiceRef(t, c, "search"); ref["name"] != studioSearchInstanceName {
		t.Fatalf("search resourceRef = %#v after the template appeared", ref)
	}
	if _, done := studioEnsured.Load(id.clusterID); !done {
		t.Fatal("workspace not marked ensured once search resolved")
	}
}
