// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestQuerySpecSchema_PaginationMatchesKueryContract(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Type                 string                 `json:"type"`
			Minimum              *float64               `json:"minimum"`
			Properties           map[string]schemaField `json:"properties"`
			AdditionalProperties bool                   `json:"additionalProperties"`
			Items                *schemaField           `json:"items"`
			Required             []string               `json:"required"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(QuerySpecSchema), &schema); err != nil {
		t.Fatalf("QuerySpecSchema is not valid JSON: %v", err)
	}

	page := schema.Properties["page"]
	if page.Type != "object" || page.AdditionalProperties {
		t.Fatalf("page schema = %+v, want a closed object", page)
	}
	if got := page.Properties["first"]; got.Type != "integer" || got.Minimum == nil || *got.Minimum != 0 {
		t.Fatalf("page.first schema = %+v, want integer minimum 0", got)
	}
	if got := page.Properties["cursor"]; got.Type != "string" {
		t.Fatalf("page.cursor type = %q, want string", got.Type)
	}

	order := schema.Properties["order"]
	if order.Type != "array" || order.Items == nil || order.Items.Type != "object" || order.Items.AdditionalProperties {
		t.Fatalf("order schema = %+v, want an array of closed objects", order)
	}
	if !reflect.DeepEqual(order.Items.Required, []string{"field"}) {
		t.Fatalf("order item required fields = %#v, want [field]", order.Items.Required)
	}
	if !reflect.DeepEqual(order.Items.Properties["field"].Enum, []string{
		"name", "namespace", "kind", "apiGroup", "cluster", "creationTimestamp",
	}) {
		t.Fatalf("order.field enum = %#v", order.Items.Properties["field"].Enum)
	}
	if !reflect.DeepEqual(order.Items.Properties["direction"].Enum, []string{"Asc", "Desc"}) {
		t.Fatalf("order.direction enum = %#v", order.Items.Properties["direction"].Enum)
	}

	if got := schema.Properties["cursor"].Type; got != "boolean" {
		t.Fatalf("cursor type = %q, want boolean", got)
	}
}

type schemaField struct {
	Type                 string                 `json:"type"`
	Minimum              *float64               `json:"minimum"`
	Properties           map[string]schemaField `json:"properties"`
	AdditionalProperties bool                   `json:"additionalProperties"`
	Items                *schemaField           `json:"items"`
	Enum                 []string               `json:"enum"`
	Required             []string               `json:"required"`
}
