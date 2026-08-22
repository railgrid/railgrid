/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"context"
	"maps"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

func TestOpenAPIToSimpleSchema(t *testing.T) {
	raw := []byte(`{
		"type": "object",
		"properties": {
			"name":       {"type": "string", "description": "logical name"},
			"size":       {"type": "string", "enum": ["small","medium","large"], "default": "small"},
			"replicas":   {"type": "integer", "default": 1, "minimum": 1, "maximum": 10},
			"persistent": {"type": "boolean", "default": false}
		},
		"required": ["name"]
	}`)

	got, err := openAPIToSimpleSchema(raw)
	if err != nil {
		t.Fatalf("openAPIToSimpleSchema: %v", err)
	}

	want := map[string]string{
		"name":       `string | required=true description="logical name"`,
		"size":       `string | enum="small,medium,large" default="small"`,
		"replicas":   `integer | default=1 minimum=1 maximum=10`,
		"persistent": `boolean | default=false`,
	}
	for field, exp := range want {
		gotStr, ok := got[field].(string)
		if !ok {
			t.Errorf("field %q: not a string leaf: %#v", field, got[field])
			continue
		}
		if gotStr != exp {
			t.Errorf("field %q:\n  got:  %s\n  want: %s", field, gotStr, exp)
		}
	}
}

func TestOpenAPIToSimpleSchemaNested(t *testing.T) {
	raw := []byte(`{
		"type": "object",
		"properties": {
			"tls": {"type": "object", "properties": {"enabled": {"type": "boolean", "default": true}}}
		}
	}`)
	got, err := openAPIToSimpleSchema(raw)
	if err != nil {
		t.Fatalf("openAPIToSimpleSchema: %v", err)
	}
	nested, ok := got["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls: expected nested map, got %#v", got["tls"])
	}
	if nested["enabled"] != `boolean | default=true` {
		t.Errorf("tls.enabled: got %v", nested["enabled"])
	}
}

func TestOpenAPIToSimpleSchemaMap(t *testing.T) {
	raw := []byte(`{
		"type": "object",
		"properties": {
			"env":    {"type": "object", "additionalProperties": {"type": "string"}, "default": {}, "description": "env vars"},
			"limits": {"type": "object", "additionalProperties": {"type": "integer"}},
			"full":   {"type": "object", "additionalProperties": {"type": "string"}, "default": {"A": "b"}}
		}
	}`)
	got, err := openAPIToSimpleSchema(raw)
	if err != nil {
		t.Fatalf("openAPIToSimpleSchema: %v", err)
	}
	if got["env"] != `map[string]string | default={} description="env vars"` {
		t.Errorf("env: got %v", got["env"])
	}
	if got["limits"] != `map[string]integer` {
		t.Errorf("limits: got %v", got["limits"])
	}
	// Non-empty map defaults are inexpressible in kro's marker syntax (no
	// quote escaping) — the default must be dropped, never emitted corrupt.
	if got["full"] != `map[string]string` {
		t.Errorf("full: non-empty map default must be dropped, got %v", got["full"])
	}
}

func TestBuildRGD(t *testing.T) {
	tmpl := &infrav1alpha1.Template{}
	tmpl.Name = "redis-cache"
	tmpl.Spec.Version = "0.1.0"
	tmpl.Spec.InstanceCRD = infrav1alpha1.TemplateInstanceCRD{
		Group:    "infrastructure.faros.sh",
		Version:  "v1alpha1",
		Resource: "rediscaches",
		Kind:     "RedisCache",
	}
	tmpl.Spec.Schema = &runtime.RawExtension{Raw: []byte(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)}
	tmpl.Spec.BackendConfig = &runtime.RawExtension{Raw: []byte(`{"resources":[{"id":"statefulset","template":{"apiVersion":"apps/v1","kind":"StatefulSet"}}]}`)}

	rgd, err := buildRGD(tmpl, testTokens())
	if err != nil {
		t.Fatalf("buildRGD: %v", err)
	}

	if rgd.GetAPIVersion() != rgdAPIVersion || rgd.GetKind() != rgdKind {
		t.Errorf("GVK = %s/%s", rgd.GetAPIVersion(), rgd.GetKind())
	}
	if rgd.GetName() != "redis-cache" {
		t.Errorf("name = %q", rgd.GetName())
	}
	if lbl := rgd.GetLabels()["faros.sh/template"]; lbl != "redis-cache" {
		t.Errorf("template label = %q", lbl)
	}

	assertNested := func(want string, fields ...string) {
		got, found, err := unstructured.NestedString(rgd.Object, fields...)
		if err != nil || !found {
			t.Errorf("%v: not found (err=%v)", fields, err)
			return
		}
		if got != want {
			t.Errorf("%v = %q, want %q", fields, got, want)
		}
	}
	assertNested("v1alpha1", "spec", "schema", "apiVersion")
	assertNested("infrastructure.faros.sh", "spec", "schema", "group")
	assertNested("RedisCache", "spec", "schema", "kind")
	assertNested("Namespaced", "spec", "schema", "scope")

	resources, found, err := unstructured.NestedSlice(rgd.Object, "spec", "resources")
	if err != nil || !found || len(resources) != 1 {
		t.Fatalf("spec.resources: found=%v len=%d err=%v", found, len(resources), err)
	}
}

func TestBuildRGDSubstitutesGatewayRef(t *testing.T) {
	tmpl := &infrav1alpha1.Template{}
	tmpl.Name = "application"
	tmpl.Spec.InstanceCRD = infrav1alpha1.TemplateInstanceCRD{
		Group: "infrastructure.faros.sh", Version: "v1alpha1", Resource: "applications", Kind: "Application",
	}
	tmpl.Spec.Schema = &runtime.RawExtension{Raw: []byte(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)}
	// A graph with an unconditional HTTPRoute is exposure "public"; anything
	// else is rejected as a marker that contradicts the graph.
	tmpl.Spec.Exposure = infrav1alpha1.ExposurePublic
	tmpl.Spec.BackendConfig = &runtime.RawExtension{Raw: []byte(`{"resources":[{"id":"httpRoute","template":{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","spec":{"parentRefs":[{"name":"${faros.gatewayName}","namespace":"${faros.gatewayNamespace}"}]}}}]}`)}

	rgd, err := buildRGD(tmpl, testTokens())
	if err != nil {
		t.Fatalf("buildRGD: %v", err)
	}
	resources, _, err := unstructured.NestedSlice(rgd.Object, "spec", "resources")
	if err != nil || len(resources) != 1 {
		t.Fatalf("spec.resources: len=%d err=%v", len(resources), err)
	}
	parentRefs, found, err := unstructured.NestedSlice(resources[0].(map[string]any), "template", "spec", "parentRefs")
	if err != nil || !found || len(parentRefs) != 1 {
		t.Fatalf("parentRefs not found: found=%v len=%d err=%v", found, len(parentRefs), err)
	}
	ref := parentRefs[0].(map[string]any)
	if ref["name"] != "cloudflare-tunnel" {
		t.Errorf("parentRefs[0].name = %q, want %q (token not substituted)", ref["name"], "cloudflare-tunnel")
	}
	if ref["namespace"] != "cfgate-system" {
		t.Errorf("parentRefs[0].namespace = %q, want %q (token not substituted)", ref["namespace"], "cfgate-system")
	}
}

func TestSubstituteTokensLeavesKroRefs(t *testing.T) {
	// kro's own ${...} references must survive substitution untouched.
	in := []byte(`{"a":"${schema.spec.name}","b":"${faros.gatewayName}","c":"${faros.gatewayNamespace}","d":"${svc.metadata.name}"}`)
	out := string(substituteTokens(in, map[string]string{gatewayNameToken: "my-gw", gatewayNamespaceToken: "my-ns"}))
	if want := `{"a":"${schema.spec.name}","b":"my-gw","c":"my-ns","d":"${svc.metadata.name}"}`; out != want {
		t.Errorf("substituteTokens = %s, want %s", out, want)
	}
}

func TestSubstituteTokensAppPublicPort(t *testing.T) {
	// The status.url CEL embeds the token inside a quoted CEL string; both
	// values must yield a valid expression.
	in := []byte(`{"url":"${\"https://\" + httpRoute.spec.hostnames[0] + \"${faros.appPublicPort}\"}"}`)

	// Local kind: FAROS_APP_PUBLIC_PORT=10443 → ":10443" suffix.
	out := string(substituteTokens(in, map[string]string{appPublicPortToken: ":10443"}))
	if want := `{"url":"${\"https://\" + httpRoute.spec.hostnames[0] + \":10443\"}"}`; out != want {
		t.Errorf("with port: substituteTokens = %s, want %s", out, want)
	}

	// Production (token unset in the caller's map): the placeholder must not
	// leak — it resolves to the empty string.
	out = string(substituteTokens(in, map[string]string{}))
	if want := `{"url":"${\"https://\" + httpRoute.spec.hostnames[0] + \"\"}"}`; out != want {
		t.Errorf("without port: substituteTokens = %s, want %s", out, want)
	}
}

// testTokens is the platform-config token map the backend builds from env (the
// exposure-layer Gateway parent + the dev-overlay images), for buildRGD tests.
func testTokens() map[string]string {
	tokens := map[string]string{
		gatewayNameToken:      DefaultGatewayName,
		gatewayNamespaceToken: DefaultGatewayNamespace,
	}
	maps.Copy(tokens, accessGateTokens())
	maps.Copy(tokens, devImageTokens())
	// Graph-build fixtures model the operator's secure universal-sandbox
	// configuration. The default token map remains mutable for ordinary
	// development and is validated separately when the universal template is
	// admitted.
	tokens[devImageTokenPrefix+"universal}"] = "ghcr.io/example/universal-dev@sha256:" + strings.Repeat("a", 64)
	tokens[devAgentImageToken] = "ghcr.io/example/dev-agent@sha256:" + strings.Repeat("b", 64)
	return tokens
}

func TestDevImageTokensIncludeUniversalDefaultAndDigestOverride(t *testing.T) {
	t.Setenv("FAROS_DEV_IMAGE_UNIVERSAL", "ghcr.io/example/universal-dev@sha256:"+strings.Repeat("a", 64))
	tokens := devImageTokens()
	if got, want := tokens[devImageTokenPrefix+"universal}"], "ghcr.io/example/universal-dev@sha256:"+strings.Repeat("a", 64); got != want {
		t.Fatalf("universal dev image token = %q, want digest override %q", got, want)
	}

	t.Setenv("FAROS_DEV_IMAGE_UNIVERSAL", "")
	tokens = devImageTokens()
	if got, want := tokens[devImageTokenPrefix+"universal}"], DefaultUniversalDevImage; got != want {
		t.Fatalf("universal dev image default = %q, want %q", got, want)
	}
}

func TestBackendRejectsMutableUniversalImageRegardlessOfGate(t *testing.T) {
	for _, gate := range []string{"", "false", "true"} {
		t.Run("gate="+gate, func(t *testing.T) {
			t.Setenv("FAROS_CODING_SANDBOX_ENABLED", gate)
			b := New(nil)
			status, err := b.SetupTemplate(context.Background(), &infrav1alpha1.Template{
				ObjectMeta: metav1.ObjectMeta{Name: infrav1alpha1.UniversalCodingSandboxTemplateName},
			})
			if err == nil {
				t.Fatal("expected mutable universal image to be rejected")
			}
			if status.Ready {
				t.Fatal("mutable universal image unexpectedly reported ready")
			}
			if !strings.Contains(err.Error(), "immutable sha256 digest") {
				t.Fatalf("error = %q, want immutable digest explanation", err)
			}
		})
	}
}

func TestBackendRejectsMutableUniversalDevAgentImage(t *testing.T) {
	t.Setenv("FAROS_DEV_IMAGE_UNIVERSAL", "ghcr.io/example/universal-dev@sha256:"+strings.Repeat("a", 64))
	t.Setenv("FAROS_DEV_AGENT_IMAGE", "ghcr.io/example/dev-agent:latest")
	b := New(nil)
	status, err := b.SetupTemplate(context.Background(), &infrav1alpha1.Template{
		ObjectMeta: metav1.ObjectMeta{Name: infrav1alpha1.UniversalCodingSandboxTemplateName},
	})
	if err == nil || !strings.Contains(err.Error(), "dev agent image") {
		t.Fatalf("error = %v, want mutable dev-agent rejection", err)
	}
	if status.Ready {
		t.Fatal("mutable dev-agent image unexpectedly reported ready")
	}
}

func TestBuildRGDRequiresBackendConfig(t *testing.T) {
	tmpl := &infrav1alpha1.Template{}
	tmpl.Name = "no-config"
	tmpl.Spec.InstanceCRD = infrav1alpha1.TemplateInstanceCRD{Group: "g", Version: "v1alpha1", Resource: "rs", Kind: "R"}
	tmpl.Spec.Schema = &runtime.RawExtension{Raw: []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`)}
	// no BackendConfig
	if _, err := buildRGD(tmpl, testTokens()); err == nil {
		t.Fatal("expected error when backendConfig is missing")
	}
}

func TestAppPublicPortSuffix(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"unset", "", ""},
		{"bare port", "10443", ":10443"},
		{"leading colon tolerated", ":10443", ":10443"},
		{"whitespace trimmed", "  8080  ", ":8080"},
		{"lower bound", "1", ":1"},
		{"upper bound", "65535", ":65535"},
		{"zero rejected", "0", ""},
		{"out of range rejected", "70000", ""},
		{"non-numeric rejected", "abc", ""},
		{"double colon rejected", "::10443", ""},
		{"path injection rejected", "10443/foo", ""},
		{"quote injection rejected", `10443"`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := appPublicPortSuffix(tt.raw); got != tt.want {
				t.Errorf("appPublicPortSuffix(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
