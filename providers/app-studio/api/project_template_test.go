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
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

func applicationTemplateObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "Template",
		"metadata":   map[string]any{"name": "application"},
		"spec": map[string]any{
			"instanceCRD": map[string]any{
				"group":    "infrastructure.kedge.faros.sh",
				"version":  "v1alpha1",
				"resource": "applications",
				"kind":     "Application",
			},
			"development": map[string]any{
				"components": map[string]any{
					"frontend": map[string]any{"workspacePath": "web", "devImage": "${kedge.devImage.node}"},
					"backend":  map[string]any{"workspacePath": "api", "devImage": "${kedge.devImage.node}"},
				},
			},
		},
	}}
}

func TestProjectTemplateInfoFromUnstructured(t *testing.T) {
	info, err := projectTemplateInfoFromUnstructured(applicationTemplateObject())
	if err != nil {
		t.Fatalf("projectTemplateInfoFromUnstructured: %v", err)
	}
	if info.APIVersion != "infrastructure.kedge.faros.sh/v1alpha1" || info.Kind != "Application" || info.Resource != "applications" {
		t.Errorf("instance coordinates = %s/%s/%s", info.APIVersion, info.Kind, info.Resource)
	}
	want := map[string]string{"frontend": "web", "backend": "api"}
	if !reflect.DeepEqual(info.Components, want) {
		t.Errorf("components = %v, want %v", info.Components, want)
	}

	// A template without a development block yields no components.
	obj := applicationTemplateObject()
	unstructured.RemoveNestedField(obj.Object, "spec", "development")
	info, err = projectTemplateInfoFromUnstructured(obj)
	if err != nil {
		t.Fatalf("without development: %v", err)
	}
	if len(info.Components) != 0 {
		t.Errorf("components = %v, want none", info.Components)
	}

	// Incomplete instanceCRD is rejected.
	obj = applicationTemplateObject()
	unstructured.RemoveNestedField(obj.Object, "spec", "instanceCRD", "kind")
	if _, err := projectTemplateInfoFromUnstructured(obj); err == nil {
		t.Error("expected error for incomplete instanceCRD")
	}
}

func TestProjectTemplateInstanceNameBoundsLongNames(t *testing.T) {
	short := &aiv1alpha1.Project{}
	short.Name = "shop"
	if got := projectTemplateInstanceName(short); got != "shop-dev" {
		t.Errorf("short name = %q, want shop-dev", got)
	}

	long := &aiv1alpha1.Project{}
	long.Name = strings.Repeat("verylongname-", 12) // 156 chars
	got := projectTemplateInstanceName(long)
	// Template graphs derive Service names like "<name>-dev-<component>-control"
	// from the instance name; the base must leave room under the DNS-label cap.
	if len(got) > projectTemplateInstanceNameMaxBase+4 {
		t.Errorf("long name = %q (len %d), want ≤ %d", got, len(got), projectTemplateInstanceNameMaxBase+4)
	}
	if !strings.HasSuffix(got, "-dev") {
		t.Errorf("long name = %q, want -dev suffix", got)
	}
	// Deterministic: the same project always maps to the same instance.
	if again := projectTemplateInstanceName(long); again != got {
		t.Errorf("instance name not deterministic: %q vs %q", got, again)
	}
	// Distinct long names must not collide.
	other := &aiv1alpha1.Project{}
	other.Name = strings.Repeat("verylongname-", 11) + "x"
	if projectTemplateInstanceName(other) == got {
		t.Error("distinct long project names collided")
	}
}

func TestProjectTemplateDevBinding(t *testing.T) {
	p := &aiv1alpha1.Project{}
	p.Name = "shop"
	info, err := projectTemplateInfoFromUnstructured(applicationTemplateObject())
	if err != nil {
		t.Fatalf("template info: %v", err)
	}
	binding, err := projectTemplateDevBinding(p, info)
	if err != nil {
		t.Fatalf("projectTemplateDevBinding: %v", err)
	}
	if binding.Name != projectDevelopmentBindingName || binding.Provider != projectDevelopmentProviderAppStudio {
		t.Errorf("binding identity = %s/%s", binding.Name, binding.Provider)
	}
	if binding.ResourceRef.Kind != "Application" || binding.ResourceRef.Resource != "applications" || binding.ResourceRef.Name != "shop-dev" {
		t.Errorf("resourceRef = %+v", binding.ResourceRef)
	}
	var values map[string]any
	if err := json.Unmarshal(binding.Values.Raw, &values); err != nil {
		t.Fatalf("values: %v", err)
	}
	if values["name"] != "shop-dev" || values["kedgeMode"] != "development" {
		t.Errorf("values = %v, want name=shop-dev kedgeMode=development", values)
	}
}

func TestDevelopmentTemplateViews(t *testing.T) {
	withDev := applicationTemplateObject()
	_ = unstructured.SetNestedField(withDev.Object, "Web application", "spec", "displayName")
	_ = unstructured.SetNestedField(withDev.Object, "Frontend + backend pair", "spec", "description")
	_ = unstructured.SetNestedField(withDev.Object, "web", "spec", "category")

	// No development block → not a development template, filtered out.
	prodOnly := applicationTemplateObject()
	prodOnly.SetName("database")
	unstructured.RemoveNestedField(prodOnly.Object, "spec", "development")

	// Malformed spec (incomplete instanceCRD) is skipped, not surfaced as an
	// error — a broken catalog entry must not hide the rest of the catalog.
	broken := applicationTemplateObject()
	broken.SetName("broken")
	unstructured.RemoveNestedField(broken.Object, "spec", "instanceCRD", "kind")

	// Second valid entry, named to sort before "application" if ordering were
	// insertion order — proves the sort.
	second := applicationTemplateObject()
	second.SetName("api-service")

	views := developmentTemplateViews([]unstructured.Unstructured{*withDev, *prodOnly, *broken, *second})

	if len(views) != 2 {
		t.Fatalf("views = %+v, want exactly the two development templates", views)
	}
	if views[0].Name != "api-service" || views[1].Name != "application" {
		t.Errorf("order = %s, %s; want api-service, application (sorted by name)", views[0].Name, views[1].Name)
	}
	app := views[1]
	if app.DisplayName != "Web application" || app.Description != "Frontend + backend pair" || app.Category != "web" {
		t.Errorf("metadata = %+v, want displayName/description/category surfaced", app)
	}
	wantComponents := map[string]string{"frontend": "web", "backend": "api"}
	if !reflect.DeepEqual(app.Components, wantComponents) {
		t.Errorf("components = %v, want %v", app.Components, wantComponents)
	}

	if got := developmentTemplateViews(nil); got == nil || len(got) != 0 {
		t.Errorf("empty catalog = %v, want empty non-nil slice", got)
	}
}

// templateCatalogDynamicClient is a minimal dynamic.Interface fake serving a
// fixed Template list (or a fixed error) for the catalog GVR.
type templateCatalogDynamicClient struct {
	items []unstructured.Unstructured
	err   error
}

func (c templateCatalogDynamicClient) Resource(gvr k8sschema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return templateCatalogDynamicResource{gvr: gvr, items: c.items, err: c.err}
}

type templateCatalogDynamicResource struct {
	dynamic.NamespaceableResourceInterface
	gvr   k8sschema.GroupVersionResource
	items []unstructured.Unstructured
	err   error
}

func (r templateCatalogDynamicResource) List(_ context.Context, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if r.gvr != templatesGVR {
		return nil, apierrors.NewNotFound(k8sschema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "")
	}
	if r.err != nil {
		return nil, r.err
	}
	return &unstructured.UnstructuredList{Items: r.items}, nil
}

// TestListDevelopmentTemplatesHandler drives GET /api/projects/development-templates
// over HTTP: only templates declaring development components are returned,
// metadata fields are surfaced, ordering is deterministic, and list failures
// use the shared /api/projects error mapping instead of a blanket 502.
func TestListDevelopmentTemplatesHandler(t *testing.T) {
	serve := func(t *testing.T, fake templateCatalogDynamicClient) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/projects/development-templates", nil)
		serveDevelopmentTemplates(w, r, asclient.NewFromDynamic(fake))
		return w
	}

	t.Run("filters sorts and shapes the catalog", func(t *testing.T) {
		withDev := applicationTemplateObject()
		_ = unstructured.SetNestedField(withDev.Object, "Web application", "spec", "displayName")
		_ = unstructured.SetNestedField(withDev.Object, "Frontend + backend pair", "spec", "description")
		_ = unstructured.SetNestedField(withDev.Object, "web", "spec", "category")
		prodOnly := applicationTemplateObject()
		prodOnly.SetName("database")
		unstructured.RemoveNestedField(prodOnly.Object, "spec", "development")
		second := applicationTemplateObject()
		second.SetName("api-service")

		w := serve(t, templateCatalogDynamicClient{items: []unstructured.Unstructured{*withDev, *prodOnly, *second}})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
		}
		var body struct {
			Templates []projectDevelopmentTemplateView `json:"templates"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if len(body.Templates) != 2 || body.Templates[0].Name != "api-service" || body.Templates[1].Name != "application" {
			t.Fatalf("templates = %+v, want [api-service application]", body.Templates)
		}
		app := body.Templates[1]
		if app.DisplayName != "Web application" || app.Description != "Frontend + backend pair" || app.Category != "web" {
			t.Errorf("metadata = %+v, want displayName/description/category surfaced", app)
		}
		if !reflect.DeepEqual(app.Components, map[string]string{"frontend": "web", "backend": "api"}) {
			t.Errorf("components = %v", app.Components)
		}
	})

	t.Run("empty catalog returns an empty list not an error", func(t *testing.T) {
		w := serve(t, templateCatalogDynamicClient{})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"templates":[]`) {
			t.Fatalf("status = %d, body %s; want 200 with empty templates array", w.Code, w.Body.String())
		}
	})

	t.Run("forbidden keeps its status", func(t *testing.T) {
		w := serve(t, templateCatalogDynamicClient{err: apierrors.NewForbidden(
			k8sschema.GroupResource{Group: templatesGVR.Group, Resource: templatesGVR.Resource}, "", nil)})
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body %s; want 403", w.Code, w.Body.String())
		}
	})

	t.Run("workspace initializing maps to 503 with retry-after", func(t *testing.T) {
		w := serve(t, templateCatalogDynamicClient{err: &apierrors.StatusError{ErrStatus: metav1.Status{
			Code:    http.StatusNotFound,
			Reason:  metav1.StatusReasonNotFound,
			Message: "the server could not find the requested resource",
		}}})
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, body %s; want 503", w.Code, w.Body.String())
		}
		if w.Header().Get("Retry-After") == "" {
			t.Error("missing Retry-After header on initializing response")
		}
	})
}

func TestRouteProjectSyncFiles(t *testing.T) {
	files := []projectSandboxSyncFile{
		{Path: "web/package.json", Content: "{}"},
		{Path: "web/src/App.tsx", Content: "app"},
		{Path: "api/package.json", Content: "{}"},
		{Path: "api/server.js", Content: "srv"},
		{Path: "README.md", Content: "docs"},
		{Path: "website/index.html", Content: "not web/"},
	}
	routed := routeProjectSyncFiles(files, map[string]string{"frontend": "web", "backend": "api"})

	wantFrontend := []projectSandboxSyncFile{
		{Path: "package.json", Content: "{}"},
		{Path: "src/App.tsx", Content: "app"},
	}
	if !reflect.DeepEqual(routed["frontend"], wantFrontend) {
		t.Errorf("frontend files = %v, want %v", routed["frontend"], wantFrontend)
	}
	wantBackend := []projectSandboxSyncFile{
		{Path: "package.json", Content: "{}"},
		{Path: "server.js", Content: "srv"},
	}
	if !reflect.DeepEqual(routed["backend"], wantBackend) {
		t.Errorf("backend files = %v, want %v", routed["backend"], wantBackend)
	}

	// "." claims the whole workspace (single-component templates), verbatim.
	routedRoot := routeProjectSyncFiles(files, map[string]string{"runner": "."})
	if !reflect.DeepEqual(routedRoot["runner"], files) {
		t.Errorf("root component files = %v, want all files unchanged", routedRoot["runner"])
	}
}

func TestProjectDevelopmentTargetRefs(t *testing.T) {
	target := projectDevelopmentSyncTargetInfo{
		Resource:     "applications",
		Kind:         "Application",
		APIVersion:   "infrastructure.kedge.faros.sh/v1alpha1",
		ResourceName: "shop-dev",
		Components:   map[string]string{"frontend": "web", "backend": "api"},
	}
	ref := target.dataPlaneRefFor("backend")
	if ref.Resource != "applications" || ref.Name != "shop-dev" || ref.Component != "backend" {
		t.Errorf("dataPlaneRefFor = %+v", ref)
	}
	if got := target.sortedComponents(); !reflect.DeepEqual(got, []string{"backend", "frontend"}) {
		t.Errorf("sortedComponents = %v", got)
	}
	res, err := target.instanceResource()
	if err != nil {
		t.Fatalf("instanceResource: %v", err)
	}
	if res.Kind != "Application" || res.GVR.Resource != "applications" {
		t.Errorf("instanceResource = %+v", res)
	}
}
