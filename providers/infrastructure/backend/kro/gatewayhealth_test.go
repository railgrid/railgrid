/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func healthCheckTestGraph() []any {
	return []any{
		map[string]any{
			"id": "gateService",
			"template": map[string]any{
				"apiVersion": "v1",
				"kind":       "Service",
				"metadata":   map[string]any{"name": "${schema.spec.name}-gate"},
			},
		},
		map[string]any{
			"id":          "httpRoute",
			"includeWhen": []any{"${schema.spec.expose.enabled}"},
			"template": map[string]any{
				"apiVersion": "gateway.networking.k8s.io/v1",
				"kind":       "HTTPRoute",
				"spec": map[string]any{
					"rules": []any{
						map[string]any{"backendRefs": []any{
							map[string]any{"name": "${gateService.metadata.name}", "port": 8080},
						}},
						map[string]any{"backendRefs": []any{
							// Same Service again: one policy, not two.
							map[string]any{"name": "${gateService.metadata.name}", "port": 8080, "kind": "Service"},
							// Not a core Service: a HealthCheckPolicy on a Service would not apply.
							map[string]any{"name": "imported", "group": "net.gke.io", "kind": "ServiceImport"},
						}},
					},
				},
			},
		},
		map[string]any{
			// An existing resource that already uses the id the policy would get.
			"id":       "httpRouteGatewayHealthCheck",
			"template": map[string]any{"apiVersion": "v1", "kind": "ConfigMap"},
		},
		map[string]any{
			"id": "previewRoute",
			"template": map[string]any{
				"apiVersion": "gateway.networking.k8s.io/v1",
				"kind":       "HTTPRoute",
				"spec": map[string]any{
					"rules": []any{
						map[string]any{"backendRefs": []any{
							map[string]any{"name": "${schema.spec.name}-preview", "port": 5173},
						}},
					},
				},
			},
		},
	}
}

func healthCheckPolicies(resources []any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, raw := range resources {
		res := raw.(map[string]any)
		tpl, _ := res["template"].(map[string]any)
		if tpl["kind"] == "HealthCheckPolicy" {
			out[res["id"].(string)] = res
		}
	}
	return out
}

func TestInjectGatewayHealthChecksDisabledLeavesGraphUntouched(t *testing.T) {
	graph := healthCheckTestGraph()
	got, err := injectGatewayHealthChecks("t", graph, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, healthCheckTestGraph()) {
		t.Fatal("empty mode changed the graph")
	}
}

func TestInjectGatewayHealthChecksRejectsUnknownMode(t *testing.T) {
	if _, err := injectGatewayHealthChecks("t", healthCheckTestGraph(), "aws-http"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestInjectGatewayHealthChecksGKETCP(t *testing.T) {
	graph := healthCheckTestGraph()
	got, err := injectGatewayHealthChecks("t", graph, GatewayBackendHealthCheckGKETCP)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(graph)+2 {
		t.Fatalf("got %d resources, want %d (one policy per distinct Service backend)", len(got), len(graph)+2)
	}

	policies := healthCheckPolicies(got)
	gate, ok := policies["httpRouteGatewayHealthCheck2"]
	if !ok {
		t.Fatalf("no policy for the gate backend under a non-colliding id; have %v", policyIDs(policies))
	}
	wantTemplate := map[string]any{
		"apiVersion": "networking.gke.io/v1",
		"kind":       "HealthCheckPolicy",
		"metadata":   map[string]any{"name": "${gateService.metadata.name}"},
		"spec": map[string]any{
			"default": map[string]any{
				"config": map[string]any{
					"type":           "TCP",
					"tcpHealthCheck": map[string]any{"portSpecification": "USE_SERVING_PORT"},
				},
			},
			"targetRef": map[string]any{"group": "", "kind": "Service", "name": "${gateService.metadata.name}"},
		},
	}
	if !reflect.DeepEqual(gate["template"], wantTemplate) {
		t.Errorf("gate policy template = %#v\nwant %#v", gate["template"], wantTemplate)
	}
	if !reflect.DeepEqual(gate["includeWhen"], []any{"${schema.spec.expose.enabled}"}) {
		t.Errorf("gate policy includeWhen = %v, want the route's", gate["includeWhen"])
	}
	// The copy must not alias the route's slice: kro-facing mutation of one
	// must never edit the other.
	gate["includeWhen"].([]any)[0] = "mutated"
	if route := graph[1].(map[string]any); route["includeWhen"].([]any)[0] != "${schema.spec.expose.enabled}" {
		t.Error("policy includeWhen aliases the route's")
	}

	preview, ok := policies["previewRouteGatewayHealthCheck"]
	if !ok {
		t.Fatalf("no policy for the preview backend; have %v", policyIDs(policies))
	}
	if _, has := preview["includeWhen"]; has {
		t.Error("ungated route produced a gated policy")
	}
	if name := preview["template"].(map[string]any)["spec"].(map[string]any)["targetRef"].(map[string]any)["name"]; name != "${schema.spec.name}-preview" {
		t.Errorf("preview policy targets %v", name)
	}
}

func policyIDs(m map[string]map[string]any) []string {
	var ids []string
	for id := range m {
		ids = append(ids, id)
	}
	return ids
}

// Every seed template, built with the setting on, carries a policy for each
// Service its HTTPRoutes route to, gated like the route; with it off, none.
func TestSeedTemplatesGatewayHealthChecks(t *testing.T) {
	dir := filepath.Join("..", "..", "install", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}
	on := maps.Clone(testTokens())
	on[gatewayBackendHealthCheckConfigKey] = GatewayBackendHealthCheckGKETCP

	var covered int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}

			off, err := buildRGD(decodeTemplate(t, raw), testTokens())
			if err != nil {
				t.Fatalf("buildRGD off: %v", err)
			}
			offRes, _, _ := unstructured.NestedSlice(off.Object, "spec", "resources")
			if n := len(healthCheckPolicies(offRes)); n != 0 {
				t.Fatalf("setting off but graph has %d HealthCheckPolicies", n)
			}

			rgd, err := buildRGD(decodeTemplate(t, raw), on)
			if err != nil {
				t.Fatalf("buildRGD on: %v", err)
			}
			resources, _, _ := unstructured.NestedSlice(rgd.Object, "spec", "resources")
			byTarget := map[string]map[string]any{}
			for _, p := range healthCheckPolicies(resources) {
				name := p["template"].(map[string]any)["spec"].(map[string]any)["targetRef"].(map[string]any)["name"].(string)
				byTarget[name] = p
			}
			for _, raw := range resources {
				res := raw.(map[string]any)
				tpl, _ := res["template"].(map[string]any)
				if tpl["kind"] != "HTTPRoute" {
					continue
				}
				for _, backend := range httpRouteServiceBackends(tpl) {
					covered++
					p, ok := byTarget[backend]
					if !ok {
						t.Errorf("route %v backend %q has no HealthCheckPolicy", res["id"], backend)
						continue
					}
					if !reflect.DeepEqual(p["includeWhen"], res["includeWhen"]) {
						t.Errorf("policy for %q includeWhen = %v, route's = %v", backend, p["includeWhen"], res["includeWhen"])
					}
				}
			}
		})
	}
	if covered == 0 {
		t.Fatal("no seed template routes to a Service; the test checks nothing")
	}
}
