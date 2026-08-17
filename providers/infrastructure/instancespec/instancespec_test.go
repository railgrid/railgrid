/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package instancespec

// Tests for the effective values contract: the reserved-field injection the
// retired per-template CRDs used to carry (ported from the template
// controller's farosmode tests) plus the defaulting + validation pipeline
// the instance controller runs in place of the apiserver.

import (
	"context"
	"encoding/json"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

func testTemplate(t *testing.T, schema map[string]any) *infrav1alpha1.Template {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	return &infrav1alpha1.Template{
		Spec: infrav1alpha1.TemplateSpec{
			Version: "0.1.0",
			Backend: "stub",
			Schema:  &runtime.RawExtension{Raw: raw},
		},
	}
}

func simpleSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":     map[string]any{"type": "string"},
			"replicas": map[string]any{"type": "integer", "minimum": 1, "maximum": 5, "default": 1},
			"size":     map[string]any{"type": "string", "enum": []any{"small", "large"}, "default": "small"},
		},
		"required": []any{"name"},
	}
}

func withDevelopment(tmpl *infrav1alpha1.Template) *infrav1alpha1.Template {
	tmpl.Spec.Development = &infrav1alpha1.TemplateDevelopment{
		Components: map[string]infrav1alpha1.TemplateDevelopmentComponent{
			"app": {WorkspacePath: ".", DevImage: "${faros.devImage.node}", StartCommand: "npm run dev"},
		},
	}
	return tmpl
}

func enumValues(prop apiextensionsv1.JSONSchemaProps) []string {
	var out []string
	for _, e := range prop.Enum {
		var s string
		_ = json.Unmarshal(e.Raw, &s)
		out = append(out, s)
	}
	return out
}

func TestFarosModeInjectedProductionOnly(t *testing.T) {
	spec, err := EffectiveSchema(testTemplate(t, simpleSchema()))
	if err != nil {
		t.Fatalf("EffectiveSchema: %v", err)
	}
	prop, ok := spec.Properties[infrav1alpha1.FarosModeField]
	if !ok {
		t.Fatalf("effective schema lacks the reserved %q property", infrav1alpha1.FarosModeField)
	}
	got := enumValues(prop)
	if len(got) != 1 || got[0] != infrav1alpha1.FarosModeProduction {
		t.Errorf("farosMode enum = %v, want [production] for a template without a development block", got)
	}
	var def string
	if prop.Default == nil || json.Unmarshal(prop.Default.Raw, &def) != nil || def != infrav1alpha1.FarosModeProduction {
		t.Errorf("farosMode default = %v, want %q", prop.Default, infrav1alpha1.FarosModeProduction)
	}
}

func TestFarosModeEnumIncludesDevelopment(t *testing.T) {
	spec, err := EffectiveSchema(withDevelopment(testTemplate(t, simpleSchema())))
	if err != nil {
		t.Fatalf("EffectiveSchema: %v", err)
	}
	got := enumValues(spec.Properties[infrav1alpha1.FarosModeField])
	want := map[string]bool{infrav1alpha1.FarosModeProduction: false, infrav1alpha1.FarosModeDevelopment: false}
	for _, v := range got {
		if _, ok := want[v]; ok {
			want[v] = true
		}
	}
	if !want[infrav1alpha1.FarosModeProduction] || !want[infrav1alpha1.FarosModeDevelopment] || len(got) != 2 {
		t.Errorf("farosMode enum = %v, want [production development]", got)
	}
}

func TestReservedPropertiesRejected(t *testing.T) {
	for _, reserved := range []string{infrav1alpha1.FarosModeField, infrav1alpha1.FarosActionsInstanceField} {
		schema := map[string]any{
			"type": "object",
			"properties": map[string]any{
				reserved: map[string]any{"type": "string"},
			},
		}
		// Reserved for development templates AND production-only ones.
		if _, err := EffectiveSchema(testTemplate(t, schema)); err == nil {
			t.Errorf("EffectiveSchema accepted a production template claiming reserved %q", reserved)
		}
		if _, err := EffectiveSchema(withDevelopment(testTemplate(t, schema))); err == nil {
			t.Errorf("EffectiveSchema accepted a development template claiming reserved %q", reserved)
		}
	}
}

func TestProviderActionsFieldsInjectedForDevelopmentOnly(t *testing.T) {
	fields := []string{
		infrav1alpha1.FarosActionsExchangeURLField,
		infrav1alpha1.FarosActionsBaseURLField,
		infrav1alpha1.FarosActionsTenantPathField,
		infrav1alpha1.FarosActionsOrgField,
		infrav1alpha1.FarosActionsWorkspaceField,
		infrav1alpha1.FarosActionsProjectField,
		infrav1alpha1.FarosActionsProjectUIDField,
		infrav1alpha1.FarosActionsEnvironmentField,
		infrav1alpha1.FarosActionsInstanceField,
		infrav1alpha1.FarosActionsCABundleField,
	}

	dev, err := EffectiveSchema(withDevelopment(testTemplate(t, simpleSchema())))
	if err != nil {
		t.Fatalf("EffectiveSchema(dev): %v", err)
	}
	for _, f := range fields {
		prop, ok := dev.Properties[f]
		if !ok {
			t.Errorf("development contract lacks reserved field %q", f)
			continue
		}
		var def string
		if prop.Type != "string" || prop.Default == nil || json.Unmarshal(prop.Default.Raw, &def) != nil || def != "" {
			t.Errorf("field %q = type=%q default=%v, want string default empty", f, prop.Type, prop.Default)
		}
	}

	prod, err := EffectiveSchema(testTemplate(t, simpleSchema()))
	if err != nil {
		t.Fatalf("EffectiveSchema(prod): %v", err)
	}
	for _, f := range fields {
		if _, found := prod.Properties[f]; found {
			t.Errorf("production-only contract unexpectedly exposes reserved field %q", f)
		}
	}
}

func TestValidateAndDefaultAppliesDefaults(t *testing.T) {
	contract, err := NewContract(testTemplate(t, simpleSchema()))
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	defaulted, errs := contract.ValidateAndDefault(context.Background(), map[string]any{"name": "web"})
	if len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
	if defaulted["size"] != "small" {
		t.Errorf("size default not applied: %v", defaulted["size"])
	}
	if defaulted["replicas"] != int64(1) && defaulted["replicas"] != float64(1) {
		t.Errorf("replicas default not applied: %v (%T)", defaulted["replicas"], defaulted["replicas"])
	}
	if defaulted[infrav1alpha1.FarosModeField] != infrav1alpha1.FarosModeProduction {
		t.Errorf("farosMode default not applied: %v", defaulted[infrav1alpha1.FarosModeField])
	}
}

func TestValidateAndDefaultRejectsBadValues(t *testing.T) {
	contract, err := NewContract(testTemplate(t, simpleSchema()))
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}

	cases := []struct {
		name   string
		values map[string]any
	}{
		{"missing required", map[string]any{"size": "small"}},
		{"enum violation", map[string]any{"name": "x", "size": "gigantic"}},
		{"range violation", map[string]any{"name": "x", "replicas": float64(99)}},
		{"invalid farosMode for production-only template", map[string]any{"name": "x", "farosMode": "development"}},
	}
	for _, tc := range cases {
		if _, errs := contract.ValidateAndDefault(context.Background(), tc.values); len(errs) == 0 {
			t.Errorf("%s: expected validation errors, got none", tc.name)
		}
	}
}

// TestValidateAndDefaultEvaluatesCEL pins the CEL path: rules the templates
// rely on (e.g. "image required unless farosMode==development") must be
// enforced by the contract now that no apiserver runs them.
func TestValidateAndDefaultEvaluatesCEL(t *testing.T) {
	schema := simpleSchema()
	schema["properties"].(map[string]any)["image"] = map[string]any{"type": "string"}
	schema["x-kubernetes-validations"] = []any{
		map[string]any{
			"rule":    "self.farosMode == 'development' || (has(self.image) && self.image != '')",
			"message": "image is required unless farosMode is development",
		},
	}
	contract, err := NewContract(withDevelopment(testTemplate(t, schema)))
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}

	if _, errs := contract.ValidateAndDefault(context.Background(), map[string]any{"name": "x"}); len(errs) == 0 {
		t.Error("expected CEL violation for production values without image")
	}
	if _, errs := contract.ValidateAndDefault(context.Background(), map[string]any{"name": "x", "farosMode": "development"}); len(errs) != 0 {
		t.Errorf("development values without image must pass: %v", errs)
	}
	if _, errs := contract.ValidateAndDefault(context.Background(), map[string]any{"name": "x", "image": "ghcr.io/x/y:1"}); len(errs) != 0 {
		t.Errorf("production values with image must pass: %v", errs)
	}
}

// TestValidateAndDefaultDoesNotMutateInput guards the copy semantics —
// callers hand over the live values map from the kcp object.
func TestValidateAndDefaultDoesNotMutateInput(t *testing.T) {
	contract, err := NewContract(testTemplate(t, simpleSchema()))
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	in := map[string]any{"name": "web"}
	_, _ = contract.ValidateAndDefault(context.Background(), in)
	if len(in) != 1 {
		t.Errorf("input values mutated: %v", in)
	}
}
