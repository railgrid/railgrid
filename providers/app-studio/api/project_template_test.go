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
	"k8s.io/apimachinery/pkg/runtime"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

func applicationTemplateObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.faros.sh/v1alpha1",
		"kind":       "Template",
		"metadata":   map[string]any{"name": "application"},
		"spec": map[string]any{
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"access": map[string]any{"type": "string", "enum": []any{"public", "private"}},
				},
			},
			"instanceCRD": map[string]any{
				"group":    "infrastructure.faros.sh",
				"version":  "v1alpha1",
				"resource": "applications",
				"kind":     "Application",
			},
			"development": map[string]any{
				"build": map[string]any{"workflowPath": ".github/workflows/build.yaml"},
				"components": map[string]any{
					"frontend": map[string]any{
						"workspacePath": "web",
						"devImage":      "${faros.devImage.node}",
						"startCommand":  "npm run dev",
						"port":          "frontend",
						"imageInput":    "frontendImage",
					},
					"backend": map[string]any{
						"workspacePath": "api",
						"devImage":      "${faros.devImage.node}",
						"startCommand":  "npm run dev || npm start",
						"port":          "backend",
						"imageInput":    "backendImage",
					},
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
	if info.APIVersion != "infrastructure.faros.sh/v1alpha1" || info.Kind != "Application" || info.Resource != "applications" {
		t.Errorf("instance coordinates = %s/%s/%s", info.APIVersion, info.Kind, info.Resource)
	}
	// The runtime half of the contract must survive extraction: without the
	// toolchain and start command, nothing downstream can tell an agent which
	// language its code has to be written in.
	want := map[string]projectTemplateComponent{
		"frontend": {WorkspacePath: "web", Toolchain: "node", StartCommand: "npm run dev", Port: "frontend", ImageInput: "frontendImage"},
		"backend":  {WorkspacePath: "api", Toolchain: "node", StartCommand: "npm run dev || npm start", Port: "backend", ImageInput: "backendImage"},
	}
	if !reflect.DeepEqual(info.Components, want) {
		t.Errorf("components = %v, want %v", info.Components, want)
	}
	if info.BuildWorkflowPath != ".github/workflows/build.yaml" {
		t.Errorf("BuildWorkflowPath = %q", info.BuildWorkflowPath)
	}
	if got, want := info.WorkspacePaths(), map[string]string{"frontend": "web", "backend": "api"}; !reflect.DeepEqual(got, want) {
		t.Errorf("WorkspacePaths = %v, want %v", got, want)
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
	if info.BuildWorkflowPath != "" {
		t.Errorf("BuildWorkflowPath without development = %q, want empty", info.BuildWorkflowPath)
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
	p.UID = "test-project-uid-shop"
	info, err := projectTemplateInfoFromUnstructured(applicationTemplateObject())
	if err != nil {
		t.Fatalf("template info: %v", err)
	}
	binding, err := projectTemplateDevBindingWithContext(p, info, projectTemplateBindingContext{})
	if err != nil {
		t.Fatalf("projectTemplateDevBindingWithContext: %v", err)
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
	if values["name"] != "shop-dev" || values["farosMode"] != "development" || values["access"] != "private" {
		t.Errorf("values = %v, want name=shop-dev farosMode=development access=private", values)
	}
}

func TestProjectTemplateDevBindingOmitsAccessForInternalTemplate(t *testing.T) {
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "jobs"}}
	info, err := projectTemplateInfoFromUnstructured(applicationTemplateObject())
	if err != nil {
		t.Fatal(err)
	}
	info.PreviewAccessModes = nil
	binding, err := projectTemplateDevBindingWithContext(p, info, projectTemplateBindingContext{})
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(binding.Values.Raw, &values); err != nil {
		t.Fatal(err)
	}
	if _, found := values["access"]; found {
		t.Fatalf("internal template binding received access: %v", values)
	}
}

func TestProjectTemplateDevBindingCarriesTrustedActionsContext(t *testing.T) {
	p := &aiv1alpha1.Project{}
	p.Name = "shop"
	p.UID = "test-project-uid-shop"
	info, err := projectTemplateInfoFromUnstructured(applicationTemplateObject())
	if err != nil {
		t.Fatalf("template info: %v", err)
	}
	binding, err := projectTemplateDevBindingWithContext(p, info, projectTemplateBindingContext{
		ActionsExchangeURL: "https://hub.example/api/provider-actions/workload/exchange",
		ActionsBaseURL:     "https://hub.example/services/providers/app-studio",
		ActionsCABundle:    "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----",
		TenantPath:         "root:faros:tenants:org:ws",
		Org:                "org",
		Workspace:          "ws",
		Project:            "shop",
		ProjectUID:         "test-project-uid-shop",
		Environment:        "development",
		Instance:           "shop-dev",
	})
	if err != nil {
		t.Fatalf("projectTemplateDevBindingWithContext: %v", err)
	}
	var values map[string]any
	if err := json.Unmarshal(binding.Values.Raw, &values); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"farosActionsExchangeURL": "https://hub.example/api/provider-actions/workload/exchange",
		"farosActionsBaseURL":     "https://hub.example/services/providers/app-studio",
		"farosActionsCABundle":    "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----",
		"farosActionsTenantPath":  "root:faros:tenants:org:ws",
		"farosActionsProjectUID":  "test-project-uid-shop",
		"farosActionsInstance":    "shop-dev",
	} {
		if values[key] != want {
			t.Errorf("%s = %v, want %q", key, values[key], want)
		}
	}
}

func TestProjectTemplateBindingContextAllowsMissingActionsURLWithoutGrant(t *testing.T) {
	p := &aiv1alpha1.Project{}
	p.Name = "shop"
	context, err := (&Server{}).projectTemplateBindingContext(p, identity{})
	if err != nil {
		t.Fatalf("projectTemplateBindingContext: %v", err)
	}
	if context.ActionsExchangeURL != "" || context.ActionsBaseURL != "" {
		t.Fatalf("action URLs = %#v, want empty for a project without grants", context)
	}
}

func TestProjectTemplateBindingContextDoesNotEnableActionsWithoutGrant(t *testing.T) {
	p := &aiv1alpha1.Project{}
	p.Name = "shop"
	context, err := (&Server{actionsExternalURL: "https://hub.example"}).projectTemplateBindingContext(p, identity{})
	if err != nil {
		t.Fatalf("projectTemplateBindingContext: %v", err)
	}
	if context.ActionsExchangeURL != "" || context.ActionsBaseURL != "" {
		t.Fatalf("action URLs = %#v, want empty for a project without active grants", context)
	}
}

func TestProjectTemplateBindingContextIncludesCABundleOnlyWithActiveGrant(t *testing.T) {
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "shop"},
		Spec: aiv1alpha1.ProjectSpec{
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
				Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
					Kind:           aiv1alpha1.ProjectBindingKindProviderReference,
					AllowedActions: []aiv1alpha1.ProjectProviderActionSpec{{Name: "query_table"}},
				}},
			}},
		},
	}
	bundle := "-----BEGIN CERTIFICATE-----\npublic-ca\n-----END CERTIFICATE-----"
	context, err := (&Server{actionsExternalURL: "https://hub.example", actionsCABundle: bundle}).projectTemplateBindingContext(p, identity{})
	if err != nil {
		t.Fatalf("projectTemplateBindingContext: %v", err)
	}
	if context.ActionsCABundle != bundle {
		t.Fatalf("CA bundle = %q, want configured public bundle", context.ActionsCABundle)
	}

	noGrant := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}
	context, err = (&Server{actionsCABundle: bundle}).projectTemplateBindingContext(noGrant, identity{})
	if err != nil {
		t.Fatalf("actionless projectTemplateBindingContext: %v", err)
	}
	if context.ActionsCABundle != "" {
		t.Fatalf("actionless CA bundle = %q, want omitted", context.ActionsCABundle)
	}
}

func TestProjectTemplateBindingContextRejectsMissingOrInvalidActionsURLWithGrant(t *testing.T) {
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "shop"},
		Spec: aiv1alpha1.ProjectSpec{Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
			Name: projectDevelopmentEnvironmentName,
			Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
				Name:     "sales",
				Provider: "databricks",
				Kind:     aiv1alpha1.ProjectBindingKindProviderReference,
				AllowedActions: []aiv1alpha1.ProjectProviderActionSpec{{
					Name: "query_table", Version: "v1", SchemaDigest: "sha256:" + strings.Repeat("a", 64),
				}},
			}},
		}}},
	}

	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{name: "missing", want: "required for action-enabled"},
		{name: "http", url: "http://hub.example", want: "must use HTTPS"},
		{name: "path", url: "https://hub.example/actions", want: "absolute HTTPS URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&Server{actionsExternalURL: tc.url}).projectTemplateBindingContext(p, identity{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("projectTemplateBindingContext(%q) error = %v, want substring %q", tc.url, err, tc.want)
			}
		})
	}
}

func TestProjectDevelopmentRuntimeBindingClearsStaleActionsContext(t *testing.T) {
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "shop", UID: "project-uid"}}
	binding := aiv1alpha1.ProjectProviderBindingSpec{
		Name:     projectDevelopmentBindingName,
		Provider: projectDevelopmentProviderAppStudio,
		Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
		Values: runtime.RawExtension{Raw: []byte(`{
			"name":"shop-dev",
			"farosMode":"development",
			"farosActionsExchangeURL":"https://stale.example/api/provider-actions/workload/exchange",
			"farosActionsBaseURL":"https://stale.example/services/providers/app-studio",
			"farosActionsCABundle":"-----BEGIN CERTIFICATE-----stale-----END CERTIFICATE-----",
			"farosActionsTenantPath":"stale-tenant",
			"farosActionsProject":"stale-project"
		}`)},
	}

	updated, err := (&Server{}).projectDevelopmentRuntimeBinding(binding, p, identity{
		tenantPath:    "root:faros:tenants:org:ws",
		orgUUID:       "org",
		workspaceUUID: "ws",
	})
	if err != nil {
		t.Fatalf("projectDevelopmentRuntimeBinding: %v", err)
	}
	var values map[string]any
	if err := json.Unmarshal(updated.Values.Raw, &values); err != nil {
		t.Fatalf("updated values: %v", err)
	}
	for _, key := range []string{"farosActionsExchangeURL", "farosActionsBaseURL", "farosActionsCABundle"} {
		if _, found := values[key]; found {
			t.Errorf("stale %s survived missing external URL: %v", key, values[key])
		}
	}
	for key, want := range map[string]string{
		"farosActionsTenantPath":  "root:faros:tenants:org:ws",
		"farosActionsOrg":         "org",
		"farosActionsWorkspace":   "ws",
		"farosActionsProject":     "shop",
		"farosActionsProjectUID":  "project-uid",
		"farosActionsEnvironment": projectDevelopmentEnvironmentName,
		"farosActionsInstance":    "shop-dev",
	} {
		if values[key] != want {
			t.Errorf("%s = %v, want rebuilt trusted value %q", key, values[key], want)
		}
	}
}

func TestProjectDevelopmentRuntimeBindingClearsActionsAfterGrantRevocation(t *testing.T) {
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", UID: "project-uid"},
		Spec: aiv1alpha1.ProjectSpec{Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
			Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{
				Kind: aiv1alpha1.ProjectBindingKindProviderReference,
				AllowedActions: []aiv1alpha1.ProjectProviderActionSpec{{
					Name: "query_table", Version: "v1", Revoked: true,
				}},
			}},
		}}},
	}
	binding := aiv1alpha1.ProjectProviderBindingSpec{
		Name:     projectDevelopmentBindingName,
		Provider: projectDevelopmentProviderAppStudio,
		Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
		Values: runtime.RawExtension{Raw: []byte(`{
			"name":"shop-dev",
			"farosActionsExchangeURL":"https://stale.example/api/provider-actions/workload/exchange",
			"farosActionsBaseURL":"https://stale.example/services/providers/app-studio",
			"farosActionsCABundle":"-----BEGIN CERTIFICATE-----stale-----END CERTIFICATE-----"
		}`)},
	}

	updated, err := (&Server{actionsExternalURL: "https://hub.example"}).projectDevelopmentRuntimeBinding(binding, p, identity{})
	if err != nil {
		t.Fatalf("projectDevelopmentRuntimeBinding: %v", err)
	}
	var values map[string]any
	if err := json.Unmarshal(updated.Values.Raw, &values); err != nil {
		t.Fatalf("updated values: %v", err)
	}
	for _, key := range []string{"farosActionsExchangeURL", "farosActionsBaseURL", "farosActionsCABundle"} {
		if _, found := values[key]; found {
			t.Errorf("revoked grant left stale %s: %v", key, values[key])
		}
	}
}

func TestApplyProjectDevelopmentTemplateBuildsInitialBindingIdempotently(t *testing.T) {
	p := &aiv1alpha1.Project{}
	p.Name = "shop"
	p.Spec = defaultProjectSpec("shop", "Shop", "", nil)
	p.Spec.Environments[0].Bindings = append(p.Spec.Environments[0].Bindings, aiv1alpha1.ProjectProviderBindingSpec{
		Name:     "unrelated",
		Provider: "other",
	})
	info, err := projectTemplateInfoFromUnstructured(applicationTemplateObject())
	if err != nil {
		t.Fatalf("template info: %v", err)
	}

	context := projectTemplateBindingContext{
		ActionsExchangeURL: "https://hub.example/api/provider-actions/workload/exchange",
		ActionsBaseURL:     "https://hub.example/services/providers/app-studio",
	}
	if err := applyProjectDevelopmentTemplateWithContext(p, info, context); err != nil {
		t.Fatalf("applyProjectDevelopmentTemplateWithContext: %v", err)
	}
	if p.Spec.Template == nil || p.Spec.Template.Name != "application" {
		t.Fatalf("spec.template = %+v, want application", p.Spec.Template)
	}
	if got := len(p.Spec.Environments[0].Bindings); got != 2 {
		t.Fatalf("development bindings = %d, want unrelated + template binding", got)
	}
	if binding := p.Spec.Environments[0].Bindings[1]; binding.Name != projectDevelopmentBindingName ||
		binding.ResourceRef == nil || binding.ResourceRef.Name != "shop-dev" {
		t.Fatalf("template binding = %+v, want shop-dev", binding)
	}

	if err := applyProjectDevelopmentTemplateWithContext(p, info, context); err != nil {
		t.Fatalf("second applyProjectDevelopmentTemplateWithContext: %v", err)
	}
	if got := len(p.Spec.Environments[0].Bindings); got != 2 {
		t.Fatalf("development bindings after second apply = %d, want idempotent 2", got)
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
	if got, want := app.PreviewAccessModes, []string{"private", "public"}; !reflect.DeepEqual(got, want) {
		t.Errorf("preview access modes = %v, want %v", got, want)
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
	routed := routeProjectSyncFiles(files, devComponentPaths(map[string]string{"frontend": "web", "backend": "api"}))

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
	routedRoot := routeProjectSyncFiles(files, devComponentPaths(map[string]string{"runner": "."}))
	if !reflect.DeepEqual(routedRoot["runner"], files) {
		t.Errorf("root component files = %v, want all files unchanged", routedRoot["runner"])
	}
}

func TestProjectDevelopmentTargetRefs(t *testing.T) {
	target := projectDevelopmentSyncTargetInfo{
		Resource:     "applications",
		Kind:         "Application",
		APIVersion:   "infrastructure.faros.sh/v1alpha1",
		ResourceName: "shop-dev",
		Components:   devComponentPaths(map[string]string{"frontend": "web", "backend": "api"}),
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
