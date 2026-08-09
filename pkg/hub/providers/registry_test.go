// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	providersv1alpha1 "github.com/faroshq/faros-kedge/apis/providers/v1alpha1"
)

func TestParseProviderActionsCanonicalCatalogShape(t *testing.T) {
	parsed, err := ParseProviderActions([]providersv1alpha1.ProviderActionSpec{{
		ID: "query_table/v1",
		BoundResource: providersv1alpha1.ProviderActionBoundResource{
			APIVersion: "databricks.kedge.faros.sh/v1alpha1",
			Kind:       "Table",
			Resource:   "tables",
		},
		InputSchema:   &runtime.RawExtension{Raw: []byte(`{"type":"object","additionalProperties":false}`)},
		OutputSchema:  &runtime.RawExtension{Raw: []byte(`{"type":"object"}`)},
		SchemaDigest:  "sha256:abc",
		ExecutionMode: providersv1alpha1.ProviderActionExecutionSync,
		Idempotency:   providersv1alpha1.ProviderActionIdempotencyKeyed,
		Limits:        providersv1alpha1.ProviderActionLimits{TimeoutSeconds: 45, MaxInputBytes: 8192, MaxOutputBytes: 65536, MaxResultItems: 100},
	}})
	if err != nil {
		t.Fatalf("parse provider actions: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed actions = %#v, want one action", parsed)
	}
	action := parsed[0]
	if action.Name != "query_table" || action.Version != "v1" {
		t.Fatalf("action identity = %#v", action)
	}
	if action.Resource.APIVersion != "databricks.kedge.faros.sh/v1alpha1" || action.Resource.Kind != "Table" || action.Resource.Resource != "tables" {
		t.Fatalf("action resource = %#v", action.Resource)
	}
	if string(action.InputSchema) != `{"type":"object","additionalProperties":false}` {
		t.Fatalf("action input schema = %s", action.InputSchema)
	}
	if string(action.OutputSchema) != `{"type":"object"}` || action.SchemaDigest != "sha256:abc" || action.Idempotency != "keyed" || action.Limits.MaxOutputBytes != 65536 {
		t.Fatalf("action policy fields = %#v", action)
	}
	if action.InputValidator == nil || action.OutputValidator == nil {
		t.Fatal("action schemas were not compiled")
	}
}

func TestParseProviderActionsRejectsExternalSchemaReferences(t *testing.T) {
	_, err := ParseProviderActions([]providersv1alpha1.ProviderActionSpec{{
		ID: "query_table/v1",
		BoundResource: providersv1alpha1.ProviderActionBoundResource{
			APIVersion: "databricks.kedge.faros.sh/v1alpha1", Kind: "Table", Resource: "tables",
		},
		InputSchema:  &runtime.RawExtension{Raw: json.RawMessage(`{"type":"object","$ref":"https://attacker.invalid/schema"}`)},
		OutputSchema: &runtime.RawExtension{Raw: json.RawMessage(`{"type":"object"}`)},
	}})
	if err == nil || !strings.Contains(err.Error(), "local fragment") {
		t.Fatalf("external schema reference error = %v, want local-fragment rejection", err)
	}
}
