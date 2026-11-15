// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

var errStructuredResultMissing = errors.New("structured result missing")

func validateResultSchema(raw json.RawMessage) error {
	if len(raw) > maxResultSchemaBytes {
		return fmt.Errorf("exceeds %d bytes", maxResultSchemaBytes)
	}
	var shape any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&shape); err != nil {
		return fmt.Errorf("is invalid JSON: %w", err)
	}
	if err := checkSchemaBounds(shape, 0); err != nil {
		return err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("is not a supported JSON Schema: %w", err)
	}
	if _, err := schema.Resolve(&jsonschema.ResolveOptions{}); err != nil {
		return fmt.Errorf("is not a supported JSON Schema: %w", err)
	}
	return nil
}

// Reject remote and recursive references; cap complexity before the schema
// resolver or instance validator sees caller-controlled input.
func checkSchemaBounds(v any, depth int) error {
	if depth > 16 {
		return errors.New("nesting exceeds 16 levels")
	}
	switch x := v.(type) {
	case map[string]any:
		if len(x) > 256 {
			return errors.New("schema object exceeds 256 fields")
		}
		if _, ok := x["$ref"]; ok {
			return errors.New("$ref is unsupported")
		}
		if _, ok := x["$dynamicRef"]; ok {
			return errors.New("$dynamicRef is unsupported")
		}
		for key, child := range x {
			if len(key) > 128 {
				return errors.New("schema keyword exceeds 128 bytes")
			}
			if err := checkSchemaBounds(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(x) > 256 {
			return errors.New("schema array exceeds 256 entries")
		}
		for _, child := range x {
			if err := checkSchemaBounds(child, depth+1); err != nil {
				return err
			}
		}
	case string:
		if len(x) > 4096 {
			return errors.New("schema string exceeds 4096 bytes")
		}
	}
	return nil
}

func validateStructuredResult(schemaRaw, resultRaw []byte) error {
	if len(resultRaw) > maxStructuredResultBytes {
		return fmt.Errorf("structured result exceeds %d bytes", maxStructuredResultBytes)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return errors.New("stored result schema is invalid")
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{})
	if err != nil {
		return fmt.Errorf("stored result schema is invalid: %w", err)
	}
	var result any
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return fmt.Errorf("structured result is invalid JSON: %w", err)
	}
	if err := resolved.Validate(result); err != nil {
		// Validator diagnostics may quote instance values. Keep those out of
		// durable/public Run messages; consumers only need the validity state.
		return errors.New("structured result does not match schema")
	}
	return nil
}

func structuredJSON(text string) ([]byte, error) {
	raw := []byte(strings.TrimSpace(text))
	if len(raw) == 0 {
		return nil, errStructuredResultMissing
	}
	if !json.Valid(raw) {
		return nil, errors.New("structured result is not valid JSON")
	}
	return raw, nil
}
