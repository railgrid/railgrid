/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"fmt"
	"slices"
)

// gatewayBackendHealthCheckConfigKey is internal backend configuration, not a
// template substitution token. RAILGRID_GATEWAY_BACKEND_HEALTH_CHECK (CR
// spec.publishing.gateway.backendHealthCheck, falling back to
// spec.application.gateway.backendHealthCheck) selects how the exposure
// Gateway's load balancer health-checks route backends. Empty leaves graphs
// untouched.
const gatewayBackendHealthCheckConfigKey = "railgrid.gatewayBackendHealthCheck"

// GatewayBackendHealthCheckGKETCP makes every graph that publishes an HTTPRoute
// also carry a GKE HealthCheckPolicy (TCP, serving port) for each Service the
// route sends traffic to.
//
// A GKE Gateway does not derive health checks from readiness probes: without a
// policy its load balancer probes "GET /" on the serving port and wants a 200.
// The access gate in front of every published app answers 421 to a request
// whose Host is not the app's, and redirects every path to sign-in for private
// apps, so no HTTP probe can pass and the app answers "no healthy upstream".
// A TCP check asks only whether the gate accepts connections.
const GatewayBackendHealthCheckGKETCP = "gke-tcp"

// injectGatewayHealthChecks appends, for mode gke-tcp, one HealthCheckPolicy
// per distinct Service backend of every HTTPRoute in the graph, gated by the
// route's own includeWhen so the policy exists exactly when the route does.
// Run after the dev overlay so its preview routes are covered too. The policy
// is named after, and targets, the backend Service (its name expression is
// copied verbatim, so kro resolves it and orders the policy after the
// Service). Empty mode returns the graph unchanged, so clusters without the
// GKE CRD never see the kind.
//
// A backend shared by several routes gets one policy, gated by the first
// route's includeWhen. Templates that route to one Service from routes with
// different conditions would need the policy gated by their OR, which kro's
// ANDed includeWhen cannot express; none of the seed templates do
// (TestSeedTemplatesGatewayHealthChecks).
func injectGatewayHealthChecks(templateName string, resources []any, mode string) ([]any, error) {
	switch mode {
	case "":
		return resources, nil
	case GatewayBackendHealthCheckGKETCP:
	default:
		return nil, fmt.Errorf("template %q: unknown gateway backend health check %q (supported: %q)", templateName, mode, GatewayBackendHealthCheckGKETCP)
	}

	ids := map[string]bool{}
	for _, raw := range resources {
		if res, ok := raw.(map[string]any); ok {
			if id, _ := res["id"].(string); id != "" {
				ids[id] = true
			}
		}
	}

	seen := map[string]bool{} // backend Service name expressions already covered
	var policies []any
	for _, raw := range resources {
		res, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		tpl, _ := res["template"].(map[string]any)
		if kind, _ := tpl["kind"].(string); kind != "HTTPRoute" {
			continue
		}
		routeID, _ := res["id"].(string)
		for _, name := range httpRouteServiceBackends(tpl) {
			if seen[name] {
				continue
			}
			seen[name] = true

			id := routeID + "GatewayHealthCheck"
			for i := 2; ids[id]; i++ {
				id = fmt.Sprintf("%sGatewayHealthCheck%d", routeID, i)
			}
			ids[id] = true

			policy := map[string]any{
				"id": id,
				"template": map[string]any{
					"apiVersion": "networking.gke.io/v1",
					"kind":       "HealthCheckPolicy",
					"metadata":   map[string]any{"name": name},
					"spec": map[string]any{
						"default": map[string]any{
							"config": map[string]any{
								"type": "TCP",
								"tcpHealthCheck": map[string]any{
									"portSpecification": "USE_SERVING_PORT",
								},
							},
						},
						"targetRef": map[string]any{
							"group": "",
							"kind":  "Service",
							"name":  name,
						},
					},
				},
			}
			if when, _ := res["includeWhen"].([]any); len(when) > 0 {
				policy["includeWhen"] = slices.Clone(when)
			}
			policies = append(policies, policy)
		}
	}
	return append(resources, policies...), nil
}

// httpRouteServiceBackends returns the names of the core Services an
// HTTPRoute template routes to, in rule order. backendRefs with another group
// or kind (a ServiceImport, an InferencePool) are left alone: a
// HealthCheckPolicy on a Service would not apply to them.
func httpRouteServiceBackends(tpl map[string]any) []string {
	spec, _ := tpl["spec"].(map[string]any)
	rules, _ := spec["rules"].([]any)
	var names []string
	for _, r := range rules {
		rule, _ := r.(map[string]any)
		refs, _ := rule["backendRefs"].([]any)
		for _, b := range refs {
			ref, _ := b.(map[string]any)
			if group, _ := ref["group"].(string); group != "" {
				continue
			}
			if kind, _ := ref["kind"].(string); kind != "" && kind != "Service" {
				continue
			}
			if name, _ := ref["name"].(string); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}
