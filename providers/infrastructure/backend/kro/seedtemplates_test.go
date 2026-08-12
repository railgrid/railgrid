/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// TestSeedTemplatesBuildRGD loads every embedded seed Template and runs it
// through buildRGD — the same path SetupTemplate uses. It catches malformed
// schema/backendConfig (bad YAML, schema with no properties, empty resource
// list, unsubstituted-but-required tokens) before they ship in the binary.
func TestSeedTemplatesBuildRGD(t *testing.T) {
	dir := filepath.Join("..", "..", "install", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}

	var seen int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		seen++
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			tmpl := decodeTemplate(t, raw)

			rgd, err := buildRGD(tmpl, testTokens())
			if err != nil {
				t.Fatalf("buildRGD(%s): %v", e.Name(), err)
			}

			// Sanity: the RGD must carry the resource graph and a schema.
			if _, found, _ := unstructured.NestedSlice(rgd.Object, "spec", "resources"); !found {
				t.Errorf("%s: RGD has no spec.resources", e.Name())
			}
			// No faros tokens may survive into the authored RGD.
			if strings.Contains(mustJSON(t, rgd.Object), "${faros.") {
				t.Errorf("%s: RGD still contains an unsubstituted ${faros.*} token", e.Name())
			}
		})
	}
	if seen == 0 {
		t.Fatal("no seed templates found")
	}
}

func TestSeedTemplatesIncludeStandaloneDatabase(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "install", "templates", "database.yaml"))
	if err != nil {
		t.Fatalf("read database seed template: %v", err)
	}
	tmpl := decodeTemplate(t, raw)
	if got, want := tmpl.Name, "database"; got != want {
		t.Fatalf("Template name = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.Backend, Name; got != want {
		t.Fatalf("backend = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.Category, "Databases"; got != want {
		t.Fatalf("category = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.InstanceCRD.Group, "infrastructure.faros.sh"; got != want {
		t.Fatalf("instance group = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.InstanceCRD.Kind, "PostgresDatabase"; got != want {
		t.Fatalf("instance kind = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.InstanceCRD.Resource, "postgresdatabases"; got != want {
		t.Fatalf("instance resource = %q, want %q", got, want)
	}

	rgd, err := buildRGD(tmpl, testTokens())
	if err != nil {
		t.Fatalf("buildRGD(database): %v", err)
	}
	for _, id := range []string{"credentials", "pwgenAccount", "pwgenRole", "pwgenBinding", "pwgen", "statefulset", "service"} {
		if findResource(t, rgd, id) == nil {
			t.Fatalf("database template missing %s resource", id)
		}
	}
	for _, id := range []string{"backendDeployment", "frontendDeployment", "httpRoute", "oauthDeployment"} {
		if findResource(t, rgd, id) != nil {
			t.Fatalf("database template must not include application resource %s", id)
		}
	}
	for _, field := range []string{"ready", "host", "port", "connectionSecretRef"} {
		if _, found, _ := unstructured.NestedFieldNoCopy(rgd.Object, "spec", "schema", "status", field); !found {
			t.Fatalf("database status missing %s", field)
		}
	}
}

// TestSeedTemplatesSimpleWebappIsDevelopmentCapable pins the simple-webapp
// contract App Studio depends on: a single dev component ("app") claiming the
// workspace root, the synthesized dev overlay in the RGD, public exposure via
// HTTPRoute, and the status fields the preview (status.url) and data plane
// (controlSecretRef, components) resolve through.
func TestSeedTemplatesSimpleWebappIsDevelopmentCapable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "install", "templates", "simple-webapp.yaml"))
	if err != nil {
		t.Fatalf("read simple-webapp seed template: %v", err)
	}
	tmpl := decodeTemplate(t, raw)
	if got, want := tmpl.Spec.InstanceCRD.Kind, "SimpleWebApp"; got != want {
		t.Fatalf("instance kind = %q, want %q", got, want)
	}
	if tmpl.Spec.Development == nil {
		t.Fatal("simple-webapp declares no spec.development — it cannot back an App Studio project")
	}
	comp, ok := tmpl.Spec.Development.Components["app"]
	if !ok {
		t.Fatalf("development components = %v, want key %q", tmpl.Spec.Development.Components, "app")
	}
	if got, want := comp.WorkspacePath, "."; got != want {
		t.Fatalf("app component workspacePath = %q, want %q (whole workspace)", got, want)
	}
	if tmpl.Spec.DataPlane == nil {
		t.Fatal("simple-webapp declares no dataPlane — sync/log/restart verbs would 404")
	}
	if _, ok := tmpl.Spec.DataPlane.Components["app"]; !ok {
		t.Fatal("simple-webapp declares no dataPlane component for app — sync/log/restart verbs would 404")
	}

	rgd, err := buildRGD(tmpl, testTokens())
	if err != nil {
		t.Fatalf("buildRGD(simple-webapp): %v", err)
	}
	for _, id := range []string{"appDeployment", "appService", "httpRoute", "appDevDeployment", "appDevWorkspace", "appDevControlService", "farosDevControlSecret"} {
		if findResource(t, rgd, id) == nil {
			t.Fatalf("simple-webapp RGD missing %s resource", id)
		}
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(rgd.Object, "spec", "schema", "spec", "farosMode"); !found {
		t.Fatal("simple-webapp RGD schema missing farosMode (dev overlay not applied)")
	}
	for _, field := range []string{"url", "host", "ready", "runtimeNamespace", "controlSecretRef", "components"} {
		if _, found, _ := unstructured.NestedFieldNoCopy(rgd.Object, "spec", "schema", "status", field); !found {
			t.Fatalf("simple-webapp status missing %s", field)
		}
	}
}

func TestSeedTemplatesDoNotExposeStandaloneSandboxPreviewHTTPRoute(t *testing.T) {
	path := filepath.Join("..", "..", "install", "templates", "sandbox-preview-httproute.yaml")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("standalone sandbox preview HTTPRoute template still exists at %s", path)
	}
}

func decodeTemplate(t *testing.T, raw []byte) *infrav1alpha1.Template {
	t.Helper()
	var obj map[string]any
	if err := utilyaml.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal YAML: %v", err)
	}
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var tmpl infrav1alpha1.Template
	if err := json.Unmarshal(data, &tmpl); err != nil {
		t.Fatalf("unmarshal into Template: %v", err)
	}
	return &tmpl
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func findResource(t *testing.T, rgd *unstructured.Unstructured, id string) map[string]any {
	t.Helper()
	resources, found, err := unstructured.NestedSlice(rgd.Object, "spec", "resources")
	if err != nil {
		t.Fatalf("read spec.resources: %v", err)
	}
	if !found {
		t.Fatal("RGD has no spec.resources")
	}
	for _, item := range resources {
		resource, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("resource has type %T, want map[string]any", item)
		}
		if got, _, _ := unstructured.NestedString(resource, "id"); got == id {
			return resource
		}
	}
	return nil
}

// TestSeedTemplatesInternalByDefaultServices pins the contract behind
// docs/platform-internal-networking.md for the two templates that exist to be
// called by another provider rather than opened by a human: an agent reaches
// them through the instance's `proxy` verb on the data plane, authorized by the
// caller's own RBAC on the instance.
//
// Public exposure is available but off by default, and — this is the part worth
// pinning — it is all-or-nothing. Every resource that makes an instance
// reachable from the internet hangs off ONE condition, so there is no way to
// end up with the route present and the gate absent. That state would be an
// open metasearch proxy or a remotely-driven browser, which is exactly what the
// deleted bearer-token sidecar was defending against.
func TestSeedTemplatesInternalByDefaultServices(t *testing.T) {
	// Everything that publishes an instance, or authenticates what was
	// published. If a resource is added to the exposure path it belongs here.
	exposureResources := []string{
		"httpRoute", "oauthDeployment", "oauthService",
		"oauthCookieSecret", "oauthPwgen", "oauthPwgenAccount", "oauthPwgenRole", "oauthPwgenBinding",
	}
	const gate = "${schema.spec.expose.enabled}"

	for _, tc := range []struct {
		file, kind, upstreamPath string
		methods                  []string
	}{
		// searxng takes no upstreamPath: the caller appends /search, so the verb
		// root addresses the app root. providers/agents/tools/web.go composes
		// exactly that.
		{file: "searxng.yaml", kind: "Searxng", upstreamPath: "", methods: []string{"GET"}},
		// browser pins /mcp instead: an MCP client is handed one URL it must not
		// modify, and pinning it keeps the verb from being walked into the rest
		// of the Playwright control server.
		{file: "browser.yaml", kind: "Browser", upstreamPath: "/mcp", methods: []string{"GET", "POST", "DELETE"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "install", "templates", tc.file))
			if err != nil {
				t.Fatalf("read seed template: %v", err)
			}
			tmpl := decodeTemplate(t, raw)
			if got := tmpl.Spec.InstanceCRD.Kind; got != tc.kind {
				t.Fatalf("instance kind = %q, want %q", got, tc.kind)
			}
			// The marker every caller reads to decide whether to expect a URL.
			if got, want := tmpl.Spec.ExposureClass(), infrav1alpha1.ExposureOptional; got != want {
				t.Fatalf("exposure = %q, want %q", got, want)
			}

			dp := tmpl.Spec.DataPlane
			if dp == nil {
				t.Fatal("no spec.dataPlane — the proxy verb would 404 and an unexposed instance would have no way in at all")
			}
			if got, want := dp.RuntimeNamespacePath, "status.runtimeNamespace"; got != want {
				t.Fatalf("runtimeNamespacePath = %q, want %q", got, want)
			}
			ep, ok := dp.Endpoints["proxy"]
			if !ok {
				t.Fatalf("dataPlane endpoints = %v, want a %q verb", dp.Endpoints, "proxy")
			}
			if got, want := ep.ServicePath, "status.serviceRef"; got != want {
				t.Fatalf("proxy servicePath = %q, want %q", got, want)
			}
			if ep.Port == "" {
				t.Fatal("proxy endpoint names no port")
			}
			if got := ep.UpstreamPath; got != tc.upstreamPath {
				t.Fatalf("proxy upstreamPath = %q, want %q", got, tc.upstreamPath)
			}
			if !slices.Equal(ep.Methods, tc.methods) {
				t.Fatalf("proxy methods = %v, want %v", ep.Methods, tc.methods)
			}

			rgd, err := buildRGD(tmpl, testTokens())
			if err != nil {
				t.Fatalf("buildRGD: %v", err)
			}
			// The status fields the data plane resolves through must actually be
			// published, or every proxy call fails at resolution.
			for _, field := range []string{"runtimeNamespace", "serviceRef"} {
				if _, found, _ := unstructured.NestedFieldNoCopy(rgd.Object, "spec", "schema", "status", field); !found {
					t.Fatalf("status missing %s — dataPlane cannot resolve", field)
				}
			}
			// Exposure is opt-in and indivisible: one condition, every resource.
			for _, id := range exposureResources {
				res := findResource(t, rgd, id)
				if res == nil {
					t.Fatalf("RGD is missing exposure resource %q — exposure %q must actually be available", id, infrav1alpha1.ExposureOptional)
				}
				when, _, _ := unstructured.NestedStringSlice(res, "includeWhen")
				if !slices.Equal(when, []string{gate}) {
					t.Fatalf("%s includeWhen = %v, want exactly [%s] — a resource on a different condition can make the route outlive its gate", id, when, gate)
				}
			}
			// The route must reach the gate, never the app.
			route := findResource(t, rgd, "httpRoute")
			if s := mustJSON(t, route); !strings.Contains(s, "oauthService") {
				t.Fatalf("httpRoute does not point at the oauth gate: %s", s)
			}
			// No ungated mode may be offered. `application` allows oidc.mode=none
			// for demos; these two never can — the app behind it has no auth.
			modes, _, _ := unstructured.NestedStringSlice(rgd.Object, "spec", "schema", "spec", "oidc", "mode", "enum")
			if slices.Contains(modes, "none") {
				t.Fatal("oidc.mode offers \"none\" — an exposed instance would be reachable by anyone with the URL")
			}
			// The bearer-token gate the data plane replaced must stay gone.
			if _, found, _ := unstructured.NestedFieldNoCopy(rgd.Object, "spec", "schema", "spec", "tokenSecretRef"); found {
				t.Fatal("spec.tokenSecretRef is back — the instance is minting a credential the data plane makes unnecessary")
			}
			if findResource(t, rgd, "gate") != nil {
				t.Fatal("the nginx bearer sidecar is back — exposure is gated by OIDC now")
			}
		})
	}
}

// This kro fork builds the instance status schema without the instance spec in
// scope, so a ${schema.*} reference there does not degrade — kro rejects the
// whole RGD with "references unknown identifiers: [schema]" and the template
// never becomes ready. buildRGD refuses it up front so the author gets a
// message that says what to do instead, rather than an opaque rejection eight
// minutes into an E2E run. (It caught exactly that, once.)
func TestBuildRGDRejectsSchemaRefsInStatus(t *testing.T) {
	tmpl := func(status string) *infrav1alpha1.Template {
		out := &infrav1alpha1.Template{}
		out.Name = "t"
		out.Spec.InstanceCRD = infrav1alpha1.TemplateInstanceCRD{
			Group: "infrastructure.faros.sh", Version: "v1alpha1", Resource: "ts", Kind: "T",
		}
		out.Spec.Schema = &runtime.RawExtension{Raw: []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`)}
		out.Spec.BackendConfig = &runtime.RawExtension{Raw: []byte(
			`{"resources":[{"id":"svc","template":{"apiVersion":"v1","kind":"Service","metadata":{"name":"x"}}}],"status":` + status + `}`)}
		return out
	}

	for _, tc := range []struct {
		name, status string
		wantErr      bool
	}{
		{name: "top-level schema ref", status: `{"url":"https://${schema.spec.name}"}`, wantErr: true},
		{name: "nested in an object", status: `{"ref":{"name":"${schema.spec.name}"}}`, wantErr: true},
		{name: "nested in a list", status: `{"hosts":["${schema.spec.name}"]}`, wantErr: true},
		// The ternary that actually shipped and broke the seed templates.
		{name: "inside a conditional", status: `{"url":"${schema.spec.expose.enabled ? \"a\" : \"\"}"}`, wantErr: true},
		// Resource-derived is the supported form, including a gated resource.
		{name: "resource ref", status: `{"url":"${\"https://\" + httpRoute.spec.hostnames[0]}"}`},
		// "schema." as literal text outside an expression is not a reference,
		// and a resource whose name merely ends in "schema" is not either.
		{name: "literal text", status: `{"note":"see schema.spec for details"}`},
		{name: "lookalike resource name", status: `{"v":"${openapiSchema.metadata.name}"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildRGD(tmpl(tc.status), testTokens())
			if tc.wantErr {
				if err == nil {
					t.Fatal("want a rejection naming the status field")
				}
				if !strings.Contains(err.Error(), "${schema.*}") {
					t.Fatalf("error should explain the rule: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
		})
	}
}
