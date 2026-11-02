// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuralcel "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celschema "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"
)

// TestGeneratedModelCredentialSchemaAdmissionValidation exercises the schema
// shipped to kcp, including schema defaults and CEL. Keeping this against the
// generated APIResourceSchema catches CEL rules that look right in Go markers
// but are missing, malformed, or behave differently after code generation.
func TestGeneratedModelCredentialSchemaAdmissionValidation(t *testing.T) {
	validator := loadGeneratedModelCredentialSchema(t)
	tests := []struct {
		name                   string
		spec                   map[string]interface{}
		wantError              string
		wantKubernetesDefaults bool
	}{
		{
			name:                   "connection binding defaults provider and secret key",
			spec:                   map[string]interface{}{"connectionRef": "shared", "model": "gpt-4o"},
			wantKubernetesDefaults: true,
		},
		{
			name: "connection binding accepts its default values explicitly",
			spec: map[string]interface{}{
				"connectionRef": "shared", "model": "gpt-4o",
				"provider": ModelProviderOpenAICompatible, "secretKey": DefaultModelSecretKey,
			},
		},
		{
			name: "connection binding rejects provider override",
			spec: map[string]interface{}{
				"connectionRef": "shared", "model": "gpt-4o", "provider": ModelProviderOpenAI,
			},
			wantError: "a referenced model inherits its provider from its connection",
		},
		{
			name: "connection binding rejects harness provider override",
			spec: map[string]interface{}{
				"connectionRef": "shared", "model": "gpt-4o", "provider": ModelProviderClaudeCode,
			},
			wantError: "a referenced model inherits its provider from its connection",
		},
		{
			name: "connection binding rejects Secret key override",
			spec: map[string]interface{}{
				"connectionRef": "shared", "model": "gpt-4o", "secretKey": "oauthToken",
			},
			wantError: "a referenced model inherits its Secret key from its connection",
		},
		{
			name: "connection binding rejects Anthropic workspace override",
			spec: map[string]interface{}{
				"connectionRef": "shared", "model": "gpt-4o", "anthropicWorkspaceID": "wrkspc_Tenant123",
			},
			wantError: "a referenced model inherits its Anthropic workspace from its connection",
		},
		{
			name: "legacy direct chat credential keeps custom transport values",
			spec: map[string]interface{}{
				"provider": ModelProviderOpenAI, "baseURL": "https://api.openai.com/v1",
				"secretRef": map[string]interface{}{"name": "legacy-key"},
				"secretKey": "accessToken", "anthropicWorkspaceID": "wrkspc_Tenant123",
			},
		},
		{
			name: "legacy direct harness credential remains valid",
			spec: map[string]interface{}{
				"provider":  ModelProviderCodex,
				"secretRef": map[string]interface{}{"name": "legacy-harness-login"},
				"secretKey": HarnessSecretKeyCodexAuth,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			object := map[string]interface{}{
				"apiVersion": SchemeGroupVersion.String(),
				"kind":       "ModelCredential",
				"metadata":   map[string]interface{}{"name": "test"},
				"spec":       runtime.DeepCopyJSONValue(tt.spec),
			}

			structuraldefaulting.Default(object, validator.structural)
			spec := object["spec"].(map[string]interface{})
			if tt.wantKubernetesDefaults {
				if spec["provider"] != ModelProviderOpenAICompatible {
					t.Errorf("provider default = %v, want %q", spec["provider"], ModelProviderOpenAICompatible)
				}
				if spec["secretKey"] != DefaultModelSecretKey {
					t.Errorf("secretKey default = %v, want %q", spec["secretKey"], DefaultModelSecretKey)
				}
			}

			errs := validator.validate(object)
			if tt.wantError == "" {
				if len(errs) != 0 {
					t.Fatalf("valid object rejected by generated schema: %v", errs)
				}
				return
			}
			if !containsFieldError(errs, tt.wantError) {
				t.Fatalf("generated schema errors %v do not include %q", errs, tt.wantError)
			}
		})
	}
}

type generatedModelCredentialSchemaValidator struct {
	structural       *structuralschema.Structural
	openAPIValidator apiservervalidation.SchemaValidator
	celValidator     *structuralcel.Validator
}

func loadGeneratedModelCredentialSchema(t *testing.T) *generatedModelCredentialSchemaValidator {
	t.Helper()

	const path = "../../deploy/chart/files/schemas/modelcredentials.agents.railgrid.ai.yaml"
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read generated ModelCredential APIResourceSchema %s: %v", path, err)
	}
	var generated struct {
		Spec struct {
			Versions []struct {
				Schema apiextensionsv1.JSONSchemaProps `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(data, &generated); err != nil {
		t.Fatalf("decode generated ModelCredential APIResourceSchema: %v", err)
	}
	if len(generated.Spec.Versions) != 1 {
		t.Fatalf("generated schema has %d versions, want one", len(generated.Spec.Versions))
	}

	internal := new(apiextensionsinternal.JSONSchemaProps)
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&generated.Spec.Versions[0].Schema, internal, nil); err != nil {
		t.Fatalf("convert generated OpenAPI schema: %v", err)
	}
	structural, err := structuralschema.NewStructural(internal)
	if err != nil {
		t.Fatalf("build structural schema from generated APIResourceSchema: %v", err)
	}
	openAPIValidator, _, err := apiservervalidation.NewSchemaValidator(internal)
	if err != nil {
		t.Fatalf("build OpenAPI admission validator from generated APIResourceSchema: %v", err)
	}
	return &generatedModelCredentialSchemaValidator{
		structural:       structural,
		openAPIValidator: openAPIValidator,
		celValidator:     structuralcel.NewValidator(structural, true, celschema.PerCallLimit),
	}
}

func (v *generatedModelCredentialSchemaValidator) validate(object map[string]interface{}) field.ErrorList {
	errs := apiservervalidation.ValidateCustomResource(nil, object, v.openAPIValidator)
	celErrs, _ := v.celValidator.Validate(context.Background(), nil, v.structural, object, nil, celschema.RuntimeCELCostBudget)
	return append(errs, celErrs...)
}

func containsFieldError(errs field.ErrorList, message string) bool {
	for _, err := range errs {
		if strings.Contains(err.Error(), message) {
			return true
		}
	}
	return false
}
