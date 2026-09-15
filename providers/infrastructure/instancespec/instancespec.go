/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package instancespec turns a Template's schema into the effective
// validation contract for Instance.spec.values and applies it.
//
// The per-template CRDs used to make the apiserver do this work — schema
// pruning, defaulting, CEL rules, and the platform-reserved field injection
// (railgridMode, railgridActions*) all rode on the synthesized CRD. With the
// single flattened Instance kind the apiserver only guarantees "values is
// an object", so the instance controller runs the same machinery the
// apiserver would have: structural-schema defaulting, openapi validation,
// and CEL, against the SAME effective schema (template schema + injected
// reserved fields). An Instance that fails reports Valid=False instead of
// being rejected at admission.
package instancespec

import (
	"context"
	"encoding/json"
	"fmt"

	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuralcel "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
)

// EffectiveSchema is the JSON schema Instance.spec.values must satisfy for
// a given Template: the author-declared Template.spec.schema plus the
// platform-reserved fields the retired per-template CRDs used to inject —
// railgridMode always, the railgridActions* context only for development-capable
// templates. A Template that claims a reserved property itself is rejected.
func EffectiveSchema(tmpl *infrav1alpha1.Template) (*apiextensionsv1.JSONSchemaProps, error) {
	if tmpl.Spec.Schema == nil || len(tmpl.Spec.Schema.Raw) == 0 {
		return nil, fmt.Errorf("template.spec.schema is required")
	}

	var spec apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(tmpl.Spec.Schema.Raw, &spec); err != nil {
		return nil, fmt.Errorf("decode spec.schema as JSONSchemaProps: %w", err)
	}

	if err := injectRailgridMode(&spec, tmpl); err != nil {
		return nil, err
	}
	if err := injectRailgridActions(&spec, tmpl.Spec.Development != nil); err != nil {
		return nil, err
	}
	if err := injectRailgridNetworkPhase(&spec, tmpl.Spec.Development != nil); err != nil {
		return nil, err
	}
	return &spec, nil
}

// injectRailgridNetworkPhase adds the platform-owned setup/runtime phase to
// development contracts. The Instance controller overwrites the value before
// materializing the runtime CR, so a tenant cannot keep setup egress enabled.
func injectRailgridNetworkPhase(spec *apiextensionsv1.JSONSchemaProps, enabled bool) error {
	if _, exists := spec.Properties[infrav1alpha1.RailgridNetworkPhaseField]; exists {
		return fmt.Errorf("spec.schema declares reserved property %q; the platform injects network phase", infrav1alpha1.RailgridNetworkPhaseField)
	}
	if !enabled {
		return nil
	}
	if spec.Properties == nil {
		spec.Properties = map[string]apiextensionsv1.JSONSchemaProps{}
	}
	spec.Properties[infrav1alpha1.RailgridNetworkPhaseField] = apiextensionsv1.JSONSchemaProps{
		Type:        "string",
		Description: "Platform-reserved network phase. Setup egress is removed when the runtime graph becomes Ready.",
		Enum: []apiextensionsv1.JSON{
			{Raw: []byte(`"` + infrav1alpha1.RailgridNetworkPhaseSetup + `"`)},
			{Raw: []byte(`"` + infrav1alpha1.RailgridNetworkPhaseRuntime + `"`)},
		},
		Default: &apiextensionsv1.JSON{Raw: []byte(`"` + infrav1alpha1.RailgridNetworkPhaseSetup + `"`)},
	}
	return nil
}

// injectRailgridMode adds the platform-reserved railgridMode property. The enum
// only admits "development" when the Template declares a development block,
// so an invalid mode fails values validation rather than controller logic.
func injectRailgridMode(spec *apiextensionsv1.JSONSchemaProps, tmpl *infrav1alpha1.Template) error {
	if _, exists := spec.Properties[infrav1alpha1.RailgridModeField]; exists {
		return fmt.Errorf("spec.schema declares reserved property %q; the platform injects it", infrav1alpha1.RailgridModeField)
	}
	modes := []apiextensionsv1.JSON{{Raw: []byte(`"` + infrav1alpha1.RailgridModeProduction + `"`)}}
	description := "Platform-reserved provisioning mode. This template is production-only."
	if tmpl.Spec.Development != nil {
		modes = append(modes, apiextensionsv1.JSON{Raw: []byte(`"` + infrav1alpha1.RailgridModeDevelopment + `"`)})
		description = "Platform-reserved provisioning mode. In development mode the declared development components run platform-managed dev images with the hot-reload agent; everything else runs as declared."
	}
	if spec.Properties == nil {
		spec.Properties = map[string]apiextensionsv1.JSONSchemaProps{}
	}
	spec.Properties[infrav1alpha1.RailgridModeField] = apiextensionsv1.JSONSchemaProps{
		Type:        "string",
		Description: description,
		Enum:        modes,
		Default:     &apiextensionsv1.JSON{Raw: []byte(`"` + infrav1alpha1.RailgridModeProduction + `"`)},
	}
	return nil
}

// injectRailgridActions adds the platform-owned Provider Actions context to
// development-template values schemas. The fields are present in the
// contract even when a Project has no action grant: App Studio's dev
// binding can omit them and rely on the empty defaults, while KRO's
// synthesized env/annotation expressions still resolve. For production-only
// templates the fields stay out of the contract, but a template author may
// not claim any reserved field in either mode.
func injectRailgridActions(spec *apiextensionsv1.JSONSchemaProps, enabled bool) error {
	fields := []string{
		infrav1alpha1.RailgridActionsExchangeURLField,
		infrav1alpha1.RailgridActionsBaseURLField,
		infrav1alpha1.RailgridActionsTenantPathField,
		infrav1alpha1.RailgridActionsOrgField,
		infrav1alpha1.RailgridActionsWorkspaceField,
		infrav1alpha1.RailgridActionsProjectField,
		infrav1alpha1.RailgridActionsProjectUIDField,
		infrav1alpha1.RailgridActionsEnvironmentField,
		infrav1alpha1.RailgridActionsInstanceField,
		infrav1alpha1.RailgridActionsCABundleField,
	}
	for _, f := range fields {
		if _, exists := spec.Properties[f]; exists {
			return fmt.Errorf("spec.schema declares reserved property %q; the platform injects Provider Actions context", f)
		}
	}
	if !enabled {
		return nil
	}
	if spec.Properties == nil {
		spec.Properties = map[string]apiextensionsv1.JSONSchemaProps{}
	}
	for _, f := range fields {
		spec.Properties[f] = apiextensionsv1.JSONSchemaProps{
			Type:        "string",
			Description: "Platform-reserved Provider Actions context; values are supplied by App Studio.",
			Default:     &apiextensionsv1.JSON{Raw: []byte(`""`)},
		}
	}
	return nil
}

// Contract is a compiled effective schema, ready to default and validate
// values payloads. Compile once per (Template generation), use many times.
type Contract struct {
	structural *structuralschema.Structural
	validator  apiservervalidation.SchemaValidator
	cel        *structuralcel.Validator
}

// NewContract compiles the effective schema for tmpl.
func NewContract(tmpl *infrav1alpha1.Template) (*Contract, error) {
	v1Schema, err := EffectiveSchema(tmpl)
	if err != nil {
		return nil, err
	}

	internal := &apiextensionsinternal.JSONSchemaProps{}
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v1Schema, internal, nil); err != nil {
		return nil, fmt.Errorf("convert schema: %w", err)
	}

	structural, err := structuralschema.NewStructural(internal)
	if err != nil {
		return nil, fmt.Errorf("structural schema: %w", err)
	}

	validator, _, err := apiservervalidation.NewSchemaValidator(internal)
	if err != nil {
		return nil, fmt.Errorf("schema validator: %w", err)
	}

	// isResourceRoot=false: values is a sub-object of Instance, not a whole
	// custom resource (no apiVersion/kind/metadata in scope for CEL).
	celValidator := structuralcel.NewValidator(structural, false, celconfig.PerCallLimit)

	return &Contract{structural: structural, validator: validator, cel: celValidator}, nil
}

// ValidateAndDefault applies the contract to a values object: returns a
// deep-copied payload with schema defaults applied, plus any validation
// errors (openapi + CEL) found on the defaulted payload. A nil values input
// validates the empty object — templates whose required fields all default
// accept it, everything else reports what is missing.
func (c *Contract) ValidateAndDefault(ctx context.Context, values map[string]any) (map[string]any, field.ErrorList) {
	if values == nil {
		values = map[string]any{}
	}
	defaulted := runtime.DeepCopyJSON(values)
	structuraldefaulting.Default(defaulted, c.structural)

	fldPath := field.NewPath("spec", "values")
	errs := apiservervalidation.ValidateCustomResource(fldPath, defaulted, c.validator)
	if c.cel != nil {
		celErrs, _ := c.cel.Validate(ctx, fldPath, c.structural, defaulted, nil, celconfig.RuntimeCELCostBudget)
		errs = append(errs, celErrs...)
	}
	return defaulted, errs
}
