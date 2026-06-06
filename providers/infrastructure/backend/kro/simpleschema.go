/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"encoding/json"
	"fmt"
	"strings"
)

// openAPISchema is a minimal subset of OpenAPI v3 / JSONSchemaProps — just
// the fields the Template author uses in spec.schema and that kro's
// SimpleSchema can express. Anything richer is ignored (lossy by design;
// SimpleSchema is deliberately a small surface).
type openAPISchema struct {
	Type        string                   `json:"type"`
	Description string                   `json:"description"`
	Properties  map[string]openAPISchema `json:"properties"`
	Required    []string                 `json:"required"`
	Enum        []any                    `json:"enum"`
	Default     any                      `json:"default"`
	Minimum     *float64                 `json:"minimum"`
	Maximum     *float64                 `json:"maximum"`
	Pattern     string                   `json:"pattern"`
	Items       *openAPISchema           `json:"items"`
}

// openAPIToSimpleSchema converts a Template's spec.schema (OpenAPI JSON
// Schema for the instance's spec) into the map shape kro expects at
// ResourceGraphDefinition.spec.schema.spec, where each field is a kro
// SimpleSchema string ("string | required=true default=...") or, for
// nested objects, a nested map. See
// https://kro.run/docs/concepts/simple-schema/.
//
// The raw input is the object schema for the instance spec (type: object
// with properties). Returns the properties projected to SimpleSchema.
func openAPIToSimpleSchema(raw []byte) (map[string]any, error) {
	var s openAPISchema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode spec.schema as OpenAPI object: %w", err)
	}
	if len(s.Properties) == 0 {
		// A schema with no properties yields an empty instance spec; kro
		// accepts that, but it almost certainly signals a malformed
		// Template, so surface it rather than emitting a useless RGD.
		return nil, fmt.Errorf("spec.schema has no properties")
	}
	return objectToSimpleSchema(s)
}

// objectToSimpleSchema projects an object schema's properties into the
// SimpleSchema map. Nested objects recurse; leaves become marker strings.
func objectToSimpleSchema(s openAPISchema) (map[string]any, error) {
	required := make(map[string]struct{}, len(s.Required))
	for _, r := range s.Required {
		required[r] = struct{}{}
	}
	out := make(map[string]any, len(s.Properties))
	for name, prop := range s.Properties {
		if prop.Type == "object" && len(prop.Properties) > 0 {
			nested, err := objectToSimpleSchema(prop)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", name, err)
			}
			out[name] = nested
			continue
		}
		_, req := required[name]
		out[name] = leafToSimpleSchema(prop, req)
	}
	return out, nil
}

// leafToSimpleSchema renders one scalar/array field as a kro SimpleSchema
// string: "<type> | marker=value marker=value ...". Markers are emitted in
// a fixed order so the output is deterministic (stable RGD spec → no
// spurious revisions on re-reconcile).
func leafToSimpleSchema(p openAPISchema, required bool) string {
	typ := openAPIToKROType(p)

	var markers []string
	if required {
		markers = append(markers, "required=true")
	}
	if len(p.Enum) > 0 {
		vals := make([]string, 0, len(p.Enum))
		for _, e := range p.Enum {
			vals = append(vals, fmt.Sprintf("%v", e))
		}
		markers = append(markers, "enum="+quote(strings.Join(vals, ",")))
	}
	if p.Default != nil {
		markers = append(markers, "default="+formatScalar(p.Default))
	}
	if p.Minimum != nil {
		markers = append(markers, fmt.Sprintf("minimum=%v", *p.Minimum))
	}
	if p.Maximum != nil {
		markers = append(markers, fmt.Sprintf("maximum=%v", *p.Maximum))
	}
	if p.Pattern != "" {
		markers = append(markers, "pattern="+quote(p.Pattern))
	}
	if p.Description != "" {
		// description last: it's free-form and the longest marker.
		markers = append(markers, "description="+quote(p.Description))
	}

	if len(markers) == 0 {
		return typ
	}
	return typ + " | " + strings.Join(markers, " ")
}

// openAPIToKROType maps an OpenAPI type to kro's SimpleSchema type token.
// Arrays render as "[]<elem>" using the items type (defaulting to string).
func openAPIToKROType(p openAPISchema) string {
	switch p.Type {
	case "integer":
		return "integer"
	case "number":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		elem := "string"
		if p.Items != nil && p.Items.Type != "" {
			elem = openAPIToKROType(*p.Items)
		}
		return "[]" + elem
	case "object":
		return "object"
	case "string":
		return "string"
	default:
		return "string"
	}
}

// formatScalar renders a default value: strings are quoted; numbers and
// bools are emitted bare so kro coerces them to the right type. JSON numbers
// arrive as float64, so an integer default like 1 prints as "1".
func formatScalar(v any) string {
	switch t := v.(type) {
	case string:
		return quote(t)
	case bool:
		return fmt.Sprintf("%v", t)
	case float64:
		// Print integers without a trailing ".0".
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	default:
		return quote(fmt.Sprintf("%v", t))
	}
}

// quote wraps a marker value in double quotes for kro's tokenizer, which
// treats a bare double quote as a delimiter and does not understand
// backslash escapes — so any inner double quote is downgraded to a single
// quote rather than risking a malformed token.
func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `'`) + `"`
}
