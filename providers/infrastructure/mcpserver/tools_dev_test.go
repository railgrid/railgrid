/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRouteDevSyncFilesRootComponentReceivesEverything(t *testing.T) {
	files := []devSyncFile{
		{Path: "src/index.js", Content: "a"},
		{Path: "package.json", Content: "b"},
	}
	routed := routeDevSyncFiles(files, map[string]string{"app": "."})
	if len(routed["app"]) != 2 {
		t.Fatalf("app routed %d files, want 2", len(routed["app"]))
	}
	if routed["app"][0].Path != "src/index.js" {
		t.Errorf("root component must keep paths as-is, got %q", routed["app"][0].Path)
	}
}

func TestRouteDevSyncFilesStripsComponentPrefix(t *testing.T) {
	components := map[string]string{"backend": "api", "frontend": "web"}
	files := []devSyncFile{
		{Path: "api/index.js", Content: "a"},
		{Path: "web/src/App.jsx", Content: "b"},
		{Path: "README.md", Content: "c"},
		{Path: "apixel/trap.js", Content: "d"}, // prefix of a prefix — must NOT match "api/"
	}
	routed := routeDevSyncFiles(files, components)
	if got := countRoutedDevFiles(routed); got != 2 {
		t.Fatalf("routed %d files, want 2 (README + apixel outside every component)", got)
	}
	if len(routed["backend"]) != 1 || routed["backend"][0].Path != "index.js" {
		t.Errorf("backend routed = %+v, want [index.js]", routed["backend"])
	}
	if len(routed["frontend"]) != 1 || routed["frontend"][0].Path != "src/App.jsx" {
		t.Errorf("frontend routed = %+v, want [src/App.jsx]", routed["frontend"])
	}
}

func TestRequireDevComponentDefaultsWhenSingle(t *testing.T) {
	target := devTarget{components: map[string]string{"app": "."}}
	got, err := requireDevComponent(target, "")
	if err != nil || got != "app" {
		t.Fatalf("single-component default = (%q, %v), want (app, nil)", got, err)
	}

	multi := devTarget{components: map[string]string{"frontend": "web", "backend": "api"}}
	if _, err := requireDevComponent(multi, ""); err == nil || !strings.Contains(err.Error(), "backend, frontend") {
		t.Errorf("multi-component empty pick must list components, got %v", err)
	}
	if _, err := requireDevComponent(multi, "db"); err == nil || !strings.Contains(err.Error(), "backend, frontend") {
		t.Errorf("unknown component must list components, got %v", err)
	}
}

// captureHandler records the request callDataPlane synthesizes.
type captureHandler struct {
	req  *http.Request
	body string
}

func (c *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.req = r
	b, _ := io.ReadAll(r.Body)
	c.body = string(b)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func TestCallDataPlaneSynthesizesHubShapedRequest(t *testing.T) {
	h := &captureHandler{}
	ident := identity{tenantPath: "root:orgs:acme", clusterID: "abc123xyz", user: "dev@acme.io", token: "tok"}

	body, status, err := callDataPlane(context.Background(), h, ident, http.MethodPost, "simplewebapps", "my-site", "app", "sync", []byte(`{"files":[]}`))
	if err != nil {
		t.Fatalf("callDataPlane: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("status/body = %d %q", status, string(body))
	}
	wantPath := "/dataplane/clusters/abc123xyz/simplewebapps/my-site/components/app/sync"
	if h.req.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", h.req.URL.Path, wantPath)
	}
	if got := h.req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q, want caller bearer", got)
	}
	if got := h.req.Header.Get("X-Kedge-Tenant"); got != "root:orgs:acme" {
		t.Errorf("X-Kedge-Tenant = %q", got)
	}
	if h.body != `{"files":[]}` {
		t.Errorf("body = %q", h.body)
	}
}

func TestCallDataPlaneRequiresClusterID(t *testing.T) {
	h := &captureHandler{}
	_, _, err := callDataPlane(context.Background(), h, identity{token: "tok"}, http.MethodGet, "simplewebapps", "x", "app", "log", nil)
	if err == nil || !strings.Contains(err.Error(), "X-Kedge-Cluster") {
		t.Fatalf("missing cluster ID must fail with an addressing error, got %v", err)
	}
	if h.req != nil {
		t.Error("handler must not be invoked without a cluster ID")
	}
}

func TestTemplateDevelopmentFromSpec(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.kedge.faros.sh/v1alpha1",
		"kind":       "Template",
		"metadata":   map[string]any{"name": "application"},
		"spec": map[string]any{
			"development": map[string]any{
				"components": map[string]any{
					"frontend": map[string]any{"workspacePath": "web", "imageInput": "frontendImage"},
					"backend":  map[string]any{"workspacePath": "api", "imageInput": "backendImage"},
				},
			},
		},
	}}
	dev := templateDevelopmentFromSpec(u)
	if dev == nil {
		t.Fatal("development block must be projected")
	}
	if dev.Components["frontend"].WorkspacePath != "web" || dev.Components["backend"].WorkspacePath != "api" {
		t.Errorf("components = %+v", dev.Components)
	}

	plain := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"displayName": "db"},
	}}
	if got := templateDevelopmentFromSpec(plain); got != nil {
		t.Errorf("template without development block must project nil, got %+v", got)
	}
}
