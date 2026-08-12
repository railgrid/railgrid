/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func testProviderAction() ProviderActionSpec {
	action := ProviderActionSpec{
		ID:          "query_table/v1",
		DisplayName: "Query table",
		Description: "Run a bounded read-only query.",
		BoundResource: ProviderActionBoundResource{
			APIVersion: "databricks.faros.sh/v1alpha1",
			Kind:       "Table",
			Resource:   "tables",
		},
		InputSchema: &runtime.RawExtension{Raw: []byte(`{"type":"object","properties":{"limit":{"type":"integer"}}}`)},
		OutputSchema: &runtime.RawExtension{Raw: []byte(`{
  "type": "object",
  "required": ["rows"],
  "properties": {"rows": {"type": "array"}}
}`)},
		ExecutionMode: ProviderActionExecutionSync,
		ReadOnly:      true,
		Risk:          ProviderActionRiskLow,
		Idempotency:   ProviderActionIdempotencyInherent,
		Limits: ProviderActionLimits{
			TimeoutSeconds: 45,
			MaxInputBytes:  8192,
			MaxOutputBytes: 65536,
			MaxResultItems: 100,
		},
		Consent: ProviderActionConsent{},
	}
	digest, _ := ProviderActionSchemaDigest(action)
	action.SchemaDigest = digest
	return action
}

func TestValidateProviderActionAndSchemaDigest(t *testing.T) {
	action := testProviderAction()
	digest, err := ProviderActionSchemaDigest(action)
	if err != nil {
		t.Fatalf("schema digest: %v", err)
	}
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		t.Fatalf("digest = %q, want sha256 plus 64 hex digits", digest)
	}
	action.SchemaDigest = digest
	if err := ValidateProviderAction(action); err != nil {
		t.Fatalf("valid action rejected: %v", err)
	}

	// Whitespace and object-key order do not change the digest.
	reordered := action
	reordered.SchemaDigest = ""
	reordered.InputSchema = &runtime.RawExtension{Raw: []byte(`{
  "properties": {"limit": {"type": "integer"}},
  "type": "object"
}`)}
	reorderedDigest, err := ProviderActionSchemaDigest(reordered)
	if err != nil {
		t.Fatalf("reordered schema digest: %v", err)
	}
	if reorderedDigest != digest {
		t.Fatalf("reordered digest = %q, want %q", reorderedDigest, digest)
	}

	reordered.SchemaDigest = "sha256:" + strings.Repeat("0", 64)
	if err := ValidateProviderAction(reordered); err == nil || !strings.Contains(err.Error(), "schemaDigest") {
		t.Fatalf("mismatched digest error = %v, want schemaDigest mismatch", err)
	}
}

func TestValidateProviderActionsRejectsMalformedDeclarations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ProviderActionSpec)
		want   string
	}{
		{name: "invalid id", mutate: func(action *ProviderActionSpec) { action.ID = "query_table/latest" }, want: "id must match"},
		{name: "missing input schema", mutate: func(action *ProviderActionSpec) { action.InputSchema = nil }, want: "inputSchema is required"},
		{name: "invalid schema", mutate: func(action *ProviderActionSpec) { action.OutputSchema = &runtime.RawExtension{Raw: []byte(`[]`)} }, want: "schema must be a JSON object"},
		{name: "invalid execution mode", mutate: func(action *ProviderActionSpec) { action.ExecutionMode = "stream" }, want: "executionMode"},
		{name: "invalid limits", mutate: func(action *ProviderActionSpec) { action.Limits.TimeoutSeconds = 0 }, want: "timeoutSeconds"},
		{name: "missing schema digest", mutate: func(action *ProviderActionSpec) { action.SchemaDigest = "" }, want: "schemaDigest"},
		{name: "consent metadata without requirement", mutate: func(action *ProviderActionSpec) { action.Consent.Scope = "tenant" }, want: "prompt and scope"},
		{name: "deprecation requires message", mutate: func(action *ProviderActionSpec) { action.Deprecation = &ProviderActionDeprecation{Deprecated: true} }, want: "message is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action := testProviderAction()
			tc.mutate(&action)
			if err := ValidateProviderActions([]ProviderActionSpec{action}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want substring %q", err, tc.want)
			}
		})
	}

	duplicate := testProviderAction()
	if err := ValidateProviderActions([]ProviderActionSpec{duplicate, duplicate}); err == nil || !strings.Contains(err.Error(), "duplicate action ID") {
		t.Fatalf("duplicate validation error = %v, want duplicate action ID", err)
	}
}
