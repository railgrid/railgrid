/*
Copyright 2026 The Railgrid Authors.

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

package api

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestStructuredResultSchemaValidation(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["verdict"],"properties":{"verdict":{"type":"string","enum":["pass","fail"]}},"additionalProperties":false}`)
	if err := validateResultSchema(schema); err != nil {
		t.Fatalf("valid schema rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		schema json.RawMessage
		result string
	}{
		{"wrong type", schema, `{"verdict":1}`},
		{"invalid enum", schema, `{"verdict":"maybe"}`},
		{"missing required", schema, `{}`},
		{"additional property", schema, `{"verdict":"pass","extra":true}`},
		{"malformed result", schema, `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateStructuredResult(tc.schema, []byte(tc.result)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if err := validateStructuredResult(schema, []byte(`{"verdict":"pass"}`)); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
}

func TestStructuredResultSchemaRejectsUnboundedOrRemoteReferences(t *testing.T) {
	for _, raw := range []string{
		`{"$ref":"https://example.com/schema.json"}`,
		`{"$ref":"#"}`,
	} {
		if err := validateResultSchema(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted schema %s", raw)
		}
	}
	tooLarge := make([]byte, maxResultSchemaBytes+1)
	if err := validateResultSchema(tooLarge); err == nil {
		t.Fatal("accepted oversized schema")
	}
	if _, err := structuredJSON(" \n"); !errors.Is(err, errStructuredResultMissing) {
		t.Fatalf("missing result error = %v", err)
	}
	if err := validateStructuredResult(json.RawMessage(`{"type":"string"}`), make([]byte, maxStructuredResultBytes+1)); err == nil {
		t.Fatal("accepted oversized result")
	}
}
