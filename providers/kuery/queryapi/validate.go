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
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ValidateQuerySpec checks one QuerySpec document against QuerySpecSchema and
// returns the first fault, naming the offending member by JSON path.
//
// It exists so the savedview reconciler can say "this view will not run"
// BEFORE anyone runs it. The SavedView CRD cannot express the QuerySpec shape
// itself — QuerySpec is recursive and controller-gen refuses recursive types —
// so spec.query is an opaque embedded object and this is the validation the
// API server would otherwise have done. The run verb re-validates, so a view
// stamped Ready by an older reconciler cannot smuggle a bad query through.
//
// The checker covers exactly the JSON Schema vocabulary QuerySpecSchema uses:
// type, properties, additionalProperties (false or a schema), items, enum,
// minimum, required, $ref into #/definitions. An unrecognised keyword is
// ignored rather than guessed at — schemaKeywords below is asserted against
// the document in the tests, so a keyword added to the schema without support
// here fails the build rather than silently validating nothing.
func ValidateQuerySpec(document []byte) error {
	root, err := parsedQuerySchema()
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(string(document))
	if trimmed == "" || trimmed == "null" {
		// An absent query is the whole fleet — the engine's own default — and
		// is a legitimate saved view ("everything").
		return nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("query is not valid JSON: %w", err)
	}
	if decoder.More() {
		return fmt.Errorf("query has trailing content after the JSON object")
	}
	return validateAgainst(root, root, value, "query")
}

// schemaKeywords is every JSON Schema keyword validateAgainst understands,
// plus the annotation-only ones it deliberately ignores.
var schemaKeywords = map[string]bool{
	"$schema": true, "$ref": true, "title": true, "description": true,
	"definitions": true, "type": true, "properties": true,
	"additionalProperties": true, "items": true, "enum": true,
	"minimum": true, "required": true,
}

var (
	querySchemaOnce sync.Once
	querySchemaRoot map[string]any
	querySchemaErr  error
)

// parsedQuerySchema decodes QuerySpecSchema once. A malformed schema is a
// programming error in this package, not a caller fault, so it surfaces as an
// error on every validation rather than a panic at init.
func parsedQuerySchema() (map[string]any, error) {
	querySchemaOnce.Do(func() {
		if err := json.Unmarshal([]byte(QuerySpecSchema), &querySchemaRoot); err != nil {
			querySchemaErr = fmt.Errorf("kuery QuerySpec schema is not valid JSON: %w", err)
		}
	})
	return querySchemaRoot, querySchemaErr
}

// validateAgainst walks one value against one schema node. path is the JSON
// path reported in errors.
func validateAgainst(root, node map[string]any, value any, path string) error {
	if ref, ok := node["$ref"].(string); ok {
		resolved, err := resolveRef(root, ref)
		if err != nil {
			return err
		}
		// A sibling of $ref is a description in this schema, never a
		// constraint, so the reference replaces the node outright.
		node = resolved
	}

	if enum, ok := node["enum"].([]any); ok {
		if err := checkEnum(enum, value, path); err != nil {
			return err
		}
	}

	switch declared, _ := node["type"].(string); declared {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return typeError(path, "an object", value)
		}
		return validateObject(root, node, object, path)
	case "array":
		items, ok := value.([]any)
		if !ok {
			return typeError(path, "an array", value)
		}
		itemSchema, _ := node["items"].(map[string]any)
		if itemSchema == nil {
			return nil
		}
		for i, item := range items {
			if err := validateAgainst(root, itemSchema, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	case "string":
		if _, ok := value.(string); !ok {
			return typeError(path, "a string", value)
		}
		return nil
	case "boolean":
		if _, ok := value.(bool); !ok {
			return typeError(path, "a boolean", value)
		}
		return nil
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return typeError(path, "an integer", value)
		}
		parsed, err := number.Int64()
		if err != nil {
			return typeError(path, "an integer", value)
		}
		if minimum, ok := node["minimum"]; ok {
			floor, err := asFloat(minimum)
			if err == nil && float64(parsed) < floor {
				return fmt.Errorf("%s must be at least %v, got %d", path, minimum, parsed)
			}
		}
		return nil
	default:
		// No declared type: an untyped node (the sparse "object" projection
		// under objects.object) accepts anything.
		return nil
	}
}

// validateObject applies properties, additionalProperties and required.
func validateObject(root, node map[string]any, object map[string]any, path string) error {
	properties, _ := node["properties"].(map[string]any)

	if required, ok := node["required"].([]any); ok {
		for _, name := range required {
			key, _ := name.(string)
			if _, present := object[key]; key != "" && !present {
				return fmt.Errorf("%s is missing the required member %q", path, key)
			}
		}
	}

	// Deterministic order: the same bad document must always name the same
	// member, or a test on the message is a coin flip.
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		child := path + "." + key
		if schema, ok := properties[key].(map[string]any); ok {
			if err := validateAgainst(root, schema, object[key], child); err != nil {
				return err
			}
			continue
		}
		switch extra := node["additionalProperties"].(type) {
		case bool:
			if !extra {
				return fmt.Errorf("%s is not a member of the kuery QuerySpec%s", child, nearest(properties, key))
			}
		case map[string]any:
			if err := validateAgainst(root, extra, object[key], child); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveRef follows a local "#/definitions/<name>" pointer. Nothing else is
// expressible in this schema, so anything else is a schema bug.
func resolveRef(root map[string]any, ref string) (map[string]any, error) {
	name, ok := strings.CutPrefix(ref, "#/definitions/")
	if !ok {
		return nil, fmt.Errorf("kuery QuerySpec schema has an unsupported $ref %q", ref)
	}
	definitions, _ := root["definitions"].(map[string]any)
	target, ok := definitions[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("kuery QuerySpec schema has no definition %q", name)
	}
	return target, nil
}

func checkEnum(enum []any, value any, path string) error {
	allowed := make([]string, 0, len(enum))
	for _, candidate := range enum {
		allowed = append(allowed, fmt.Sprintf("%v", candidate))
		if fmt.Sprintf("%v", candidate) == fmt.Sprintf("%v", value) {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of %s, got %v", path, strings.Join(allowed, ", "), value)
}

func typeError(path, want string, got any) error {
	return fmt.Errorf("%s must be %s, got %s", path, want, kindOf(got))
}

func kindOf(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case json.Number:
		return "a number"
	case string:
		return "a string"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	default:
		return "an unexpected value"
	}
}

func asFloat(value any) (float64, error) {
	switch typed := value.(type) {
	case float64:
		return typed, nil
	case json.Number:
		return typed.Float64()
	default:
		return 0, fmt.Errorf("not a number")
	}
}

// nearest suggests the closest known member for a typo, which is most of what
// a rejected query actually is ("relation" for "relations"). Empty when
// nothing is close enough to be worth guessing.
func nearest(properties map[string]any, key string) string {
	lowered := strings.ToLower(key)
	for candidate := range properties {
		lowercase := strings.ToLower(candidate)
		if strings.HasPrefix(lowercase, lowered) || strings.HasPrefix(lowered, lowercase) {
			return fmt.Sprintf(" (did you mean %q?)", candidate)
		}
	}
	return ""
}
