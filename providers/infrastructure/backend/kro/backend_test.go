/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"testing"

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

func TestBuildRGD(t *testing.T) {
	tmpl := &infrav1alpha1.Template{}
	tmpl.Name = "redis-cache"
	tmpl.Spec.Version = "0.1.0"
	tmpl.Spec.InstanceCRD = infrav1alpha1.TemplateInstanceCRD{
		Group:    "infrastructure.kedge.faros.sh",
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
	if lbl := rgd.GetLabels()["kedge.faros.sh/template"]; lbl != "redis-cache" {
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
	assertNested("infrastructure.kedge.faros.sh", "spec", "schema", "group")
	assertNested("RedisCache", "spec", "schema", "kind")
	assertNested("Cluster", "spec", "schema", "scope")

	resources, found, err := unstructured.NestedSlice(rgd.Object, "spec", "resources")
	if err != nil || !found || len(resources) != 1 {
		t.Fatalf("spec.resources: found=%v len=%d err=%v", found, len(resources), err)
	}
}

func TestBuildRGDSubstitutesGatewayRef(t *testing.T) {
	tmpl := &infrav1alpha1.Template{}
	tmpl.Name = "application"
	tmpl.Spec.InstanceCRD = infrav1alpha1.TemplateInstanceCRD{
		Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1", Resource: "applications", Kind: "Application",
	}
	tmpl.Spec.Schema = &runtime.RawExtension{Raw: []byte(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)}
	tmpl.Spec.BackendConfig = &runtime.RawExtension{Raw: []byte(`{"resources":[{"id":"httpRoute","template":{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","spec":{"parentRefs":[{"name":"${kedge.gatewayName}","namespace":"${kedge.gatewayNamespace}"}]}}}]}`)}

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
	in := []byte(`{"a":"${schema.spec.name}","b":"${kedge.gatewayName}","c":"${kedge.gatewayNamespace}","d":"${svc.metadata.name}"}`)
	out := string(substituteTokens(in, map[string]string{gatewayNameToken: "my-gw", gatewayNamespaceToken: "my-ns"}))
	if want := `{"a":"${schema.spec.name}","b":"my-gw","c":"my-ns","d":"${svc.metadata.name}"}`; out != want {
		t.Errorf("substituteTokens = %s, want %s", out, want)
	}
}

func TestSubstituteTokensSandboxImages(t *testing.T) {
	in := []byte(`{"runner":"${kedge.sandboxRunnerImage}","token":"${kedge.sandboxTokenGeneratorImage}"}`)
	tokens := map[string]string{
		sandboxRunnerImageToken:    "ghcr.io/faroshq/kedge-sandbox-runner@sha256:abc",
		sandboxTokenGeneratorToken: "docker.io/bitnami/kubectl@sha256:def",
	}
	out := string(substituteTokens(in, tokens))
	want := `{"runner":"ghcr.io/faroshq/kedge-sandbox-runner@sha256:abc","token":"docker.io/bitnami/kubectl@sha256:def"}`
	if out != want {
		t.Errorf("substituteTokens = %s, want %s", out, want)
	}
}

// testTokens is the platform-config token map the backend builds from env,
// with the gateway defaults and placeholder sandbox images, for buildRGD tests.
func testTokens() map[string]string {
	return map[string]string{
		gatewayNameToken:           DefaultGatewayName,
		gatewayNamespaceToken:      DefaultGatewayNamespace,
		sandboxRunnerImageToken:    "ghcr.io/faroshq/kedge-sandbox-runner:test",
		sandboxTokenGeneratorToken: "docker.io/bitnami/kubectl:test",
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
