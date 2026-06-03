// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package kro

import (
	"fmt"
	"strings"
)

// convertSimpleSchemaBlock converts a kro SimpleSchema map into a
// JSON-schema-shaped object suitable for the portal's DynamicForm and
// for ajv validation on submit. SimpleSchema is documented at
// https://kro.run/docs/concepts/simple-schema/ — each field is either:
//
//	name: type
//	name: type | required=true | default=foo | description="bar"
//	name:                       (nested object: a child map)
//	  child: type
//
// Supported types in v1: string, integer, number, boolean, object,
// array (with element type inferred from a homogenous Default if
// present). Unsupported types fall through as {"type": "string"}.
//
// The kro RGD may store the input fields either at spec.schema.spec
// (the modern convention — instance.spec fields) or directly as
// children of spec.schema (the older layout). Pass whichever map the
// caller has; we look for both.
func convertSimpleSchemaBlock(schema map[string]any) (jsonSchema map[string]any, sampleValues map[string]any) {
	// Prefer the modern spec.schema.spec layout.
	body, ok := schema["spec"].(map[string]any)
	if !ok {
		body = schema
	}
	return ConvertSimpleSchema(body)
}

// ConvertSimpleSchema does the work. Exposed so the stub client can
// build hand-rolled SimpleSchema maps for the baked-in catalog.
func ConvertSimpleSchema(body map[string]any) (jsonSchema map[string]any, sampleValues map[string]any) {
	properties := map[string]any{}
	required := []string{}
	samples := map[string]any{}

	for fieldName, raw := range body {
		// Skip kro-reserved keys that may appear at the top level.
		if fieldName == "kind" || fieldName == "apiVersion" || fieldName == "group" {
			continue
		}
		switch v := raw.(type) {
		case map[string]any:
			// Nested object — recurse.
			nested, nestedSamples := ConvertSimpleSchema(v)
			properties[fieldName] = nested
			if len(nestedSamples) > 0 {
				samples[fieldName] = nestedSamples
			}
		case string:
			prop, isRequired, sample := parseSimpleSchemaLeaf(v)
			properties[fieldName] = prop
			if isRequired {
				required = append(required, fieldName)
			}
			if sample != nil {
				samples[fieldName] = sample
			}
		default:
			// Unknown shape — default to string so the form still
			// renders something.
			properties[fieldName] = map[string]any{"type": "string"}
		}
	}

	jsonSchema = map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		jsonSchema["required"] = required
	}
	return jsonSchema, samples
}

// parseSimpleSchemaLeaf turns a single kro SimpleSchema field spec into a
// JSON-schema property. The kro grammar is `type | marker1=v1 marker2=v2 ...`
// — the FIRST bar separates the type from the marker list, then markers are
// SPACE-separated (with quoted values allowed for any marker that contains
// commas/spaces, e.g. enum="ClusterIP,NodePort,LoadBalancer"). See the
// fork's website/docs/docs/concepts/rgd/01-schema.md.
//
// We deliberately also accept the older `|`-as-marker-separator that the
// stub catalog uses internally, so the same parser handles both. Quoted
// values use either single or double quotes.
//
// Unknown markers are silently ignored — kro itself rejects them at RGD
// admission, so by the time we see one the source is trusted.
func parseSimpleSchemaLeaf(spec string) (prop map[string]any, required bool, sample any) {
	typeText, markerText, _ := strings.Cut(spec, "|")
	jsonType := simpleSchemaToJSONType(typeText)
	prop = map[string]any{"type": jsonType}

	for _, seg := range tokenizeMarkers(markerText) {
		k, v, ok := splitKV(seg)
		if !ok {
			continue
		}
		switch k {
		case "required":
			if v == "true" {
				required = true
			}
		case "default":
			prop["default"] = coerceTo(v, jsonType)
			sample = prop["default"]
		case "description":
			prop["description"] = v
		case "enum":
			parts := strings.Split(v, ",")
			out := make([]any, len(parts))
			for i, p := range parts {
				out[i] = strings.TrimSpace(p)
			}
			prop["enum"] = out
		case "minimum":
			if n, ok := tryParseFloat(v); ok {
				prop["minimum"] = n
			}
		case "maximum":
			if n, ok := tryParseFloat(v); ok {
				prop["maximum"] = n
			}
		case "pattern":
			prop["pattern"] = v
		}
	}
	return prop, required, sample
}

// tokenizeMarkers splits a kro marker section like
//
//	enum="a,b,c" default=a description="something with spaces"
//
// into ['enum="a,b,c"', 'default=a', 'description="something with spaces"'].
// The trick is respecting quoted strings: spaces (and bars, for back-compat
// with the stub) inside quotes do NOT split. We also tolerate the legacy
// `|`-separated form ("a | b") by treating bars at the top level as
// whitespace.
func tokenizeMarkers(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inQuote := byte(0) // 0 = not in quote, otherwise the quote char
	flush := func() {
		t := strings.TrimSpace(cur.String())
		if t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
				continue // strip the quote chars from the value
			}
			cur.WriteByte(c)
			continue
		}
		switch c {
		case '"', '\'':
			inQuote = c
		case ' ', '\t', '|':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

func simpleSchemaToJSONType(t string) string {
	switch strings.TrimSpace(t) {
	case "integer", "int", "int32", "int64":
		return "integer"
	case "number", "float", "double":
		return "number"
	case "boolean", "bool":
		return "boolean"
	case "object", "map":
		return "object"
	case "array", "[]string", "[]integer", "[]any":
		return "array"
	case "string":
		fallthrough
	default:
		return "string"
	}
}

func splitKV(s string) (k, v string, ok bool) {
	s = strings.TrimSpace(s)
	idx := strings.IndexByte(s, '=')
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+1:]), true
}

// coerceTo converts a string from a `default=…` qualifier into the
// matching Go type so the JSON sample renders the value as a number /
// bool rather than a string. Falls back to the raw string on parse
// failure so a malformed default still produces *something* the form
// can show.
func coerceTo(s, jsonType string) any {
	s = strings.Trim(s, `"'`)
	switch jsonType {
	case "integer":
		if n, ok := tryParseInt(s); ok {
			return n
		}
	case "number":
		if n, ok := tryParseFloat(s); ok {
			return n
		}
	case "boolean":
		return s == "true"
	}
	return s
}

func tryParseInt(s string) (int64, bool) {
	var out int64
	if _, err := fmt.Sscanf(s, "%d", &out); err == nil {
		return out, true
	}
	return 0, false
}

func tryParseFloat(s string) (float64, bool) {
	var out float64
	if _, err := fmt.Sscanf(s, "%f", &out); err == nil {
		return out, true
	}
	return 0, false
}
