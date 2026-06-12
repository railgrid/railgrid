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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// ingressClassToken is the reserved placeholder a Template author writes in
// backendConfig (e.g. an Ingress's spec.ingressClassName) to defer the
// exposure-layer controller choice to platform config. It is substituted
// for the configured ingress class before the RGD is authored. The
// "kedge." namespace keeps it from colliding with kro's own ${...}
// reference syntax (${schema.spec.x}, ${someResource.metadata.name}).
const ingressClassToken = "${kedge.ingressClass}"

const (
	rgdAPIVersion = "kro.run/v1alpha1"
	rgdKind       = "ResourceGraphDefinition"

	// instanceScope mirrors the per-template CRD scope the Template
	// controller publishes (ClusterScoped — see
	// controller/template/controller.go and the portal's api.ts note).
	// The RGD's generated CRD must agree or kro and the API surface would
	// disagree on whether instances are namespaced.
	instanceScope = "Cluster"
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
//   - spec.schema.{group,apiVersion,kind,scope} = Template.spec.instanceCRD (+ Cluster)
//   - spec.schema.spec        = Template.spec.schema (OpenAPI) → kro SimpleSchema
//   - spec.schema.status      = Template.spec.backendConfig.status (optional)
//   - spec.resources          = Template.spec.backendConfig.resources (verbatim)
func buildRGD(tmpl *infrav1alpha1.Template, ingressClass string) (*unstructured.Unstructured, error) {
	if tmpl.Spec.Schema == nil || len(tmpl.Spec.Schema.Raw) == 0 {
		return nil, fmt.Errorf("template %q: spec.schema is required", tmpl.Name)
	}
	simpleSpec, err := openAPIToSimpleSchema(tmpl.Spec.Schema.Raw)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", tmpl.Name, err)
	}

	resources, status, err := backendConfig(tmpl, ingressClass)
	if err != nil {
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
			// kedge-authored so a human (or a future GC) can tell these
			// apart from hand-applied RGDs on the runtime cluster.
			"labels": map[string]any{
				"kedge.faros.sh/template":         tmpl.Name,
				"kedge.faros.sh/template-version": tmpl.Spec.Version,
				"app.kubernetes.io/managed-by":    "kedge-infrastructure",
			},
		},
		"spec": map[string]any{
			"schema":    schemaBlock,
			"resources": resources,
		},
	}}
	return rgd, nil
}

// backendConfig decodes Template.spec.backendConfig and extracts the kro
// resource graph (required) and an optional status-mapping block. The
// backendConfig is opaque to the platform; only this backend interprets it.
func backendConfig(tmpl *infrav1alpha1.Template, ingressClass string) (resources []any, status map[string]any, err error) {
	if tmpl.Spec.BackendConfig == nil || len(tmpl.Spec.BackendConfig.Raw) == 0 {
		return nil, nil, fmt.Errorf("template %q: spec.backendConfig is required for the kro backend", tmpl.Name)
	}
	raw := substituteTokens(tmpl.Spec.BackendConfig.Raw, ingressClass)
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

// substituteTokens replaces reserved kedge ${kedge.*} placeholders in a
// raw backendConfig with platform config values, before the JSON is parsed
// into the RGD. Only the kedge namespace is touched; kro's own ${...}
// references pass through untouched for kro to resolve at reconcile time.
//
// Today the only token is ${kedge.ingressClass}. The replacement is a plain
// string substitution on the JSON bytes — safe because the configured value
// is a DNS-style class name with no JSON metacharacters.
func substituteTokens(raw []byte, ingressClass string) []byte {
	if ingressClass == "" {
		ingressClass = DefaultIngressClass
	}
	return bytes.ReplaceAll(raw, []byte(ingressClassToken), []byte(ingressClass))
}
