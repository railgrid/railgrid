/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// Reserved ${faros.*} placeholders a Template author writes in backendConfig to
// defer a platform-owned value (the exposure Gateway, the sandbox runner images)
// out of per-tenant data. They are substituted for the configured value before
// the RGD is authored. The "faros." namespace keeps them from colliding with
// kro's own ${...} reference syntax (${schema.spec.x}, ${res.metadata.name}).
const (
	gatewayNameToken      = "${faros.gatewayName}"
	gatewayNamespaceToken = "${faros.gatewayNamespace}"

	// devImageTokenPrefix is the reserved token family template authors put in
	// spec.development.components[].devImage — ${faros.devImage.<toolchain>},
	// resolved from FAROS_DEV_IMAGE_<TOOLCHAIN>. Platform-managed: tenants
	// never pick dev images (docs/app-studio-template-sandboxes.md §1).
	devImageTokenPrefix = "${faros.devImage."

	// devAgentImageToken is the injector image carrying the static
	// faros-dev-agent binary every dev-mode component runs
	// (FAROS_DEV_AGENT_IMAGE).
	devAgentImageToken = "${faros.devAgentImage}"

	// previewConsoleVerificationJWKSConfigKey is internal backend configuration,
	// not a template substitution token. The provider passes the platform-owned
	// current/previous public ES256 keys to dev-agent init containers, which
	// install them beside the Vite plugin. Private signing keys never enter the
	// infrastructure provider or tenant pod.
	previewConsoleVerificationJWKSConfigKey = "faros.previewConsoleVerificationJWKS"

	// appPublicPortToken is the ":<port>" suffix templates append to
	// synthesized exposure URLs (status.url). Empty in production — the
	// Gateway serves on 443 and the URL implies it — and ":10443" style when
	// the Gateway is only reachable on a forwarded port (local kind).
	// Resolved from FAROS_APP_PUBLIC_PORT (the bare port number).
	appPublicPortToken = "${faros.appPublicPort}"

	// Access-gate token family. Publishable templates render the platform
	// faros-access-proxy as a component of their own graph (the gate) and the
	// shared-Gateway HTTPRoute always points at it. These tokens carry the
	// platform-owned gate configuration; tenants never choose the gate image
	// or the hub endpoints.
	//
	//   - accessProxyImageToken — the gate container image
	//     (FAROS_ACCESS_PROXY_IMAGE).
	//   - hubURLToken — in-cluster hub origin the gate exchanges sign-in codes
	//     against (FAROS_ACCESS_HUB_URL, falling back to FAROS_HUB_URL).
	//   - hubPublicURLToken — browser-reachable hub origin for sign-in
	//     redirects (FAROS_ACCESS_HUB_PUBLIC_URL, falling back to hubURLToken).
	//   - hubInsecureToken — "true"/"false" TLS-verification skip for gate→hub
	//     calls (FAROS_ACCESS_HUB_INSECURE; local self-signed hubs only).
	accessProxyImageToken = "${faros.accessProxyImage}"
	hubURLToken           = "${faros.hubUrl}"
	hubPublicURLToken     = "${faros.hubPublicUrl}"
	hubInsecureToken      = "${faros.hubInsecure}"
)

const (
	rgdAPIVersion = "kro.run/v1alpha1"
	rgdKind       = "ResourceGraphDefinition"

	// instanceScope is Namespaced: the per-template kro CRs live only on
	// the runtime cluster now (tenants author the flattened Instance kind
	// in kcp; the instance controller materializes one runtime CR per
	// Instance in that tenant's runtime namespace). Namespacing them is
	// what isolates same-named instances from different tenant workspaces,
	// and it makes vanilla kro place every namespaced child in the
	// instance's own namespace — the job the fork's deploy-to-local-runtime
	// namespace localization used to do.
	instanceScope = "Namespaced"
)

// rgdGVR is the ResourceGraphDefinition resource on the kro runtime cluster.
var rgdGVR = schema.GroupVersionResource{
	Group:    "kro.run",
	Version:  "v1alpha1",
	Resource: "resourcegraphdefinitions",
}

// buildRGD projects a Template into the kro ResourceGraphDefinition that
// drives reconciliation of its instances. The Template is the source of
// truth; the RGD is derived, 1:1:
//
//   - metadata.name           = Template.name
//   - spec.schema.{group,apiVersion,kind,scope} = Template.spec.instanceCRD (+ Namespaced)
//   - spec.schema.spec        = Template.spec.schema (OpenAPI) → kro SimpleSchema
//   - spec.schema.status      = Template.spec.backendConfig.status (optional)
//   - spec.resources          = Template.spec.backendConfig.resources (verbatim)
func buildRGD(tmpl *infrav1alpha1.Template, tokens map[string]string) (*unstructured.Unstructured, error) {
	if tmpl.Spec.Schema == nil || len(tmpl.Spec.Schema.Raw) == 0 {
		return nil, fmt.Errorf("template %q: spec.schema is required", tmpl.Name)
	}
	simpleSpec, err := openAPIToSimpleSchema(tmpl.Spec.Schema.Raw)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", tmpl.Name, err)
	}

	resources, status, err := backendConfig(tmpl, tokens)
	if err != nil {
		return nil, err
	}

	if err := checkExposure(tmpl, resources); err != nil {
		return nil, err
	}
	if err := checkStatusExpressions(tmpl, status); err != nil {
		return nil, err
	}

	// A development block extends the graph with the mechanically synthesized
	// dev overlay (mode-gated dev workloads, workspace PVCs, control plane)
	// and injects the farosMode field into the RGD schema. See devoverlay.go.
	if tmpl.Spec.Development != nil {
		resources, status, err = applyDevOverlay(tmpl, simpleSpec, resources, status, tokens)
		if err != nil {
			return nil, err
		}
	}

	// Templates author every namespaced child with the placeholder
	// namespace "default" (a convention the dev overlay also relies on).
	// The RGD instance is Namespaced now, so children must inherit the
	// instance's own runtime namespace — kro does that for children with NO
	// metadata.namespace, so the placeholder is stripped here, after the dev
	// overlay synthesized its resources. Anything other than "default" is a
	// template targeting a foreign namespace, which the platform has never
	// supported — refuse rather than deploy it somewhere surprising.
	if err := stripPlaceholderNamespaces(tmpl.Name, resources); err != nil {
		return nil, err
	}

	schemaBlock := map[string]any{
		"apiVersion": tmpl.Spec.InstanceCRD.Version,
		"group":      tmpl.Spec.InstanceCRD.Group,
		"kind":       tmpl.Spec.InstanceCRD.Kind,
		"scope":      instanceScope,
		"spec":       simpleSpec,
	}
	if status != nil {
		schemaBlock["status"] = status
	}

	rgd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": rgdAPIVersion,
		"kind":       rgdKind,
		"metadata": map[string]any{
			"name": tmpl.Name,
			// Trace the RGD back to its Template + version, and mark it
			// faros-authored so a human (or a future GC) can tell these
			// apart from hand-applied RGDs on the runtime cluster.
			"labels": map[string]any{
				"faros.sh/template":            tmpl.Name,
				"faros.sh/template-version":    tmpl.Spec.Version,
				"app.kubernetes.io/managed-by": "faros-infrastructure",
			},
		},
		"spec": map[string]any{
			"schema":    schemaBlock,
			"resources": resources,
		},
	}}
	return rgd, nil
}

// stripPlaceholderNamespaces removes the authored placeholder
// metadata.namespace ("default") from every graph resource so kro's
// instance-namespace inheritance places the child in the instance's own
// runtime namespace. A declared namespace with any other value is an error:
// per-template graphs deploy into exactly one namespace — the instance's.
//
// RoleBinding/ClusterRoleBinding ServiceAccount subjects get the same
// treatment, but by REWRITE rather than removal — RBAC requires an explicit
// namespace on SA subjects (no inheritance exists for them), so the
// placeholder becomes ${schema.metadata.namespace}, kro's reference to the
// instance's own namespace. Without this the grant points at the literal
// "default" namespace and every credential-minting Job in the graph is
// Forbidden — the job the retired fork's localizeBindingSubjects used to do.
func stripPlaceholderNamespaces(templateName string, resources []any) error {
	for _, raw := range resources {
		res, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		template, ok := res["template"].(map[string]any)
		if !ok {
			continue
		}
		if meta, ok := template["metadata"].(map[string]any); ok {
			if ns, declared := meta["namespace"].(string); declared {
				if ns != "default" {
					id, _ := res["id"].(string)
					return fmt.Errorf("template %q: resource %q declares metadata.namespace %q — graph resources must use the placeholder \"default\" (rewritten to the instance's runtime namespace) and may not target foreign namespaces", templateName, id, ns)
				}
				delete(meta, "namespace")
			}
		}

		kind, _ := template["kind"].(string)
		if kind != "RoleBinding" && kind != "ClusterRoleBinding" {
			continue
		}
		subjects, _ := template["subjects"].([]any)
		for _, sraw := range subjects {
			subject, ok := sraw.(map[string]any)
			if !ok || subject["kind"] != "ServiceAccount" {
				continue
			}
			ns, declared := subject["namespace"].(string)
			if !declared {
				continue
			}
			if ns != "default" && !strings.Contains(ns, "${") {
				id, _ := res["id"].(string)
				return fmt.Errorf("template %q: resource %q binds a ServiceAccount in namespace %q — subjects must use the placeholder \"default\" (rewritten to the instance's runtime namespace) or an explicit ${...} expression", templateName, id, ns)
			}
			if ns == "default" {
				subject["namespace"] = "${schema.metadata.namespace}"
			}
		}
	}
	return nil
}

// schemaRefRE finds a reference to the instance's own spec inside a status
// expression. Word-boundary anchored so a resource legitimately named e.g.
// "openapiSchema" is not mistaken for one.
var schemaRefRE = regexp.MustCompile(`\bschema\.`)

// exprRE spans one ${...} expression. Only their contents are checked: a
// status value may contain the word "schema." as literal text.
var exprRE = regexp.MustCompile(`\$\{[^}]*\}`)

// checkStatusExpressions rejects ${schema.*} in a status mapping. This kro fork
// builds the instance status schema without the spec in scope, so such an
// expression does not degrade — it makes kro reject the whole RGD with
// "references unknown identifiers: [schema]", which surfaces as a template
// that silently never becomes ready.
//
// Catching it here turns an 8-minute E2E failure and an opaque kro message
// into an immediate one that says what to do instead. Status is derived from
// RESOURCES (which carry the resolved values anyway); a field that should only
// exist sometimes gets there by referencing an includeWhen-gated resource,
// which simply does not resolve when that resource is excluded.
func checkStatusExpressions(tmpl *infrav1alpha1.Template, status map[string]any) error {
	var walk func(path string, v any) error
	walk = func(path string, v any) error {
		switch t := v.(type) {
		case string:
			for _, expr := range exprRE.FindAllString(t, -1) {
				if schemaRefRE.MatchString(expr) {
					return fmt.Errorf("template %q: status field %s uses %s — ${schema.*} is not in scope when kro builds the status schema and the RGD is rejected outright; derive the value from a resource instead (an includeWhen-gated resource yields a field that is absent when that resource is excluded)",
						tmpl.Name, path, expr)
				}
			}
		case map[string]any:
			for k, sub := range t {
				if err := walk(path+"."+k, sub); err != nil {
					return err
				}
			}
		case []any:
			for i, sub := range t {
				if err := walk(fmt.Sprintf("%s[%d]", path, i), sub); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for k, v := range status {
		if err := walk("status."+k, v); err != nil {
			return err
		}
	}
	return nil
}

// checkExposure holds spec.exposure to what the graph actually does. The
// marker is what the portal and the MCP tools tell users ("this has no URL"),
// so a template that declares `internal` and then publishes an HTTPRoute would
// have every surface confidently saying the opposite of the truth. An
// unconditional route in an `optional` template is the same lie in the other
// direction: the caller is told to check the instance, but every instance is
// published regardless.
//
// The reverse — public/optional with no route — is NOT an error. A route may
// legitimately arrive from the dev overlay, and a template may declare its
// intent before its graph catches up.
func checkExposure(tmpl *infrav1alpha1.Template, resources []any) error {
	class := tmpl.Spec.ExposureClass()
	if class == infrav1alpha1.ExposurePublic {
		return nil
	}
	for _, item := range resources {
		res, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tpl, _ := res["template"].(map[string]any)
		if kind, _ := tpl["kind"].(string); kind != "HTTPRoute" {
			continue
		}
		id, _ := res["id"].(string)
		if class == infrav1alpha1.ExposureInternal {
			return fmt.Errorf("template %q declares exposure %q but its graph publishes an HTTPRoute (%s) — either drop the route or declare the exposure it really has",
				tmpl.Name, class, id)
		}
		// optional: the route must be conditional, or it isn't optional.
		if when, _ := res["includeWhen"].([]any); len(when) == 0 {
			return fmt.Errorf("template %q declares exposure %q but its HTTPRoute (%s) has no includeWhen — every instance would be published, which is exposure %q",
				tmpl.Name, class, id, infrav1alpha1.ExposurePublic)
		}
	}
	return nil
}

// backendConfig decodes Template.spec.backendConfig and extracts the kro
// resource graph (required) and an optional status-mapping block. The
// backendConfig is opaque to the platform; only this backend interprets it.
func backendConfig(tmpl *infrav1alpha1.Template, tokens map[string]string) (resources []any, status map[string]any, err error) {
	if tmpl.Spec.BackendConfig == nil || len(tmpl.Spec.BackendConfig.Raw) == 0 {
		return nil, nil, fmt.Errorf("template %q: spec.backendConfig is required for the kro backend", tmpl.Name)
	}
	raw := substituteTokens(tmpl.Spec.BackendConfig.Raw, tokens)
	var bc map[string]any
	if err := json.Unmarshal(raw, &bc); err != nil {
		return nil, nil, fmt.Errorf("template %q: decode spec.backendConfig: %w", tmpl.Name, err)
	}
	res, ok := bc["resources"].([]any)
	if !ok || len(res) == 0 {
		return nil, nil, fmt.Errorf("template %q: spec.backendConfig.resources must be a non-empty list", tmpl.Name)
	}
	if st, ok := bc["status"].(map[string]any); ok {
		status = st
	}
	return res, status, nil
}

// substituteTokens replaces reserved faros ${faros.*} placeholders in a raw
// backendConfig with the configured platform values, before the JSON is parsed
// into the RGD. Only the faros namespace is touched; kro's own ${...} references
// pass through untouched for kro to resolve at reconcile time.
//
// The replacement is a plain string substitution on the JSON bytes — safe
// because the configured values are DNS-style names / image references with no
// JSON metacharacters. The gateway tokens fall back to their in-binary defaults
// when unset; other tokens (e.g. the sandbox images) substitute their value
// as-is, so an unset image leaves an empty string the chart guards against at
// install time rather than masking the misconfiguration here.
func substituteTokens(raw []byte, tokens map[string]string) []byte {
	resolved := make(map[string]string, len(tokens))
	maps.Copy(resolved, tokens)
	if resolved[gatewayNameToken] == "" {
		resolved[gatewayNameToken] = DefaultGatewayName
	}
	if resolved[gatewayNamespaceToken] == "" {
		resolved[gatewayNamespaceToken] = DefaultGatewayNamespace
	}
	// Unlike the tokens above, empty is the CORRECT production value here
	// (no port suffix). Force the key so the placeholder never leaks into
	// the RGD when a caller's token map omits it.
	if _, ok := resolved[appPublicPortToken]; !ok {
		resolved[appPublicPortToken] = ""
	}
	for token, value := range resolved {
		if token == previewConsoleVerificationJWKSConfigKey {
			continue
		}
		raw = bytes.ReplaceAll(raw, []byte(token), []byte(value))
	}
	return raw
}
