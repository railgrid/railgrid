// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateQuerySpec_Accepts(t *testing.T) {
	for _, document := range []string{
		``,
		`null`,
		`{}`,
		`{"root":"clusters"}`,
		`{"limit":50,"cursor":true,"count":false,"maxDepth":3}`,
		`{"page":{"first":0}}`,
		`{"page":{"cursor":"opaque"}}`,
		`{"order":[{"field":"cluster","direction":"Asc"},{"field":"name"}]}`,
		`{"cluster":{"name":"edge-1"}}`,
		`{"filter":{"objects":[{"groupKind":{"apiGroup":"apps","kind":"Deployment"},"namespace":"default","labels":{"a":"b"},"categories":["all"]}]}}`,
		// The sparse projection is deliberately untyped: it mirrors whatever
		// shape the object has.
		`{"objects":{"cluster":true,"object":{"metadata":{"name":true},"spec":{"replicas":true}}}}`,
		`{"objects":{"relations":{"descendants+":{"limit":10,"objects":{"id":true}}}}}`,
	} {
		if err := ValidateQuerySpec([]byte(document)); err != nil {
			t.Errorf("ValidateQuerySpec(%s) = %v, want accepted", document, err)
		}
	}
}

// Every rejection must name the member it is about, because the message is
// what a tenant reads off the SavedView's Ready condition.
func TestValidateQuerySpec_RejectsAndExplains(t *testing.T) {
	for _, tc := range []struct {
		document string
		mentions string
	}{
		{`[]`, "query"},
		{`{"limit":"fifty"}`, "query.limit"},
		{`{"root":"everything"}`, "query.root"},
		{`{"order":[{"direction":"Asc"}]}`, "field"},
		{`{"order":[{"field":"colour"}]}`, "query.order[0].field"},
		{`{"page":{"first":-1}}`, "query.page.first"},
		{`{"relation":{}}`, "query.relation"},
		{`{"filter":{"object":[]}}`, "query.filter.object"},
		{`{"filter":{"objects":[{"kind":"Deployment"}]}}`, "query.filter.objects[0].kind"},
		{`{"cluster":{"labels":{"tenant":"someone-else"}}}`, "query.cluster.labels"},
		{`{"objects":{"relations":{"owners":{"limit":"ten"}}}}`, "limit"},
		{`{} {}`, "trailing"},
		{`{`, "valid JSON"},
	} {
		err := ValidateQuerySpec([]byte(tc.document))
		if err == nil {
			t.Errorf("ValidateQuerySpec(%s) accepted an invalid document", tc.document)
			continue
		}
		if !strings.Contains(err.Error(), tc.mentions) {
			t.Errorf("ValidateQuerySpec(%s) = %q, want a message naming %q", tc.document, err, tc.mentions)
		}
	}
}

// A near-miss gets a suggestion, because a rejected query is usually a typo.
func TestValidateQuerySpec_SuggestsTheNearestMember(t *testing.T) {
	err := ValidateQuerySpec([]byte(`{"filter":{"object":[]}}`))
	if err == nil || !strings.Contains(err.Error(), `did you mean "objects"`) {
		t.Fatalf("err = %v, want a suggestion of the nearest member", err)
	}
}

// The checker covers exactly the vocabulary the schema uses. A keyword added
// to QuerySpecSchema without support here would silently validate nothing, so
// this fails the build instead.
func TestQuerySpecSchemaUsesOnlySupportedKeywords(t *testing.T) {
	var root any
	if err := json.Unmarshal([]byte(QuerySpecSchema), &root); err != nil {
		t.Fatalf("QuerySpecSchema is not valid JSON: %v", err)
	}
	var walk func(node any, path string)
	walk = func(node any, path string) {
		object, ok := node.(map[string]any)
		if !ok {
			return
		}
		for key, value := range object {
			// Below "properties" and "definitions" the keys are member names,
			// not keywords; below an untyped node anything goes.
			switch key {
			case "properties", "definitions":
				for name, child := range value.(map[string]any) {
					walk(child, path+"."+key+"."+name)
				}
				continue
			case "enum", "required":
				continue
			}
			if !schemaKeywords[key] {
				t.Errorf("%s uses unsupported JSON Schema keyword %q; teach validateAgainst about it or drop it", path, key)
			}
			walk(value, path+"."+key)
		}
	}
	walk(root, "$")
}
