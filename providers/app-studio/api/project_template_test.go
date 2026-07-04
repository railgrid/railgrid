/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
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

	// Legacy target falls back to the sandbox runner descriptor.
	legacy := projectDevelopmentSyncTargetInfo{ResourceName: "kedge-sandbox-abc"}
	res, err = legacy.instanceResource()
	if err != nil {
		t.Fatalf("legacy instanceResource: %v", err)
	}
	if res.Kind != "SandboxRunner" {
		t.Errorf("legacy instanceResource = %+v", res)
	}
	if ref := legacy.dataPlaneRefFor(""); ref.Resource != sandboxRunnersResource {
		t.Errorf("legacy ref = %+v", ref)
	}
}
