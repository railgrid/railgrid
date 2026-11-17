// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/store"
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

// checkStructuredResult judges a run's final answer against the schema it was
// asked to meet. It is the one rule both kinds of run are held to — a
// repository run's repository.resultSchema and a model run's resultSchema —
// so the same answer cannot pass one and fail the other.
//
// A valid answer comes back as the artifact to store and a "valid" verdict
// carrying its digest and size. Otherwise err says why, and the verdict's
// State is "missing" (no answer at all) or "invalid" (an answer that is not
// one JSON document matching the schema); no artifact is produced.
func checkStructuredResult(schema json.RawMessage, finalContent string) (backend.Artifact, *store.StructuredResult, error) {
	data, err := structuredJSON(unfenceJSON(finalContent))
	if err == nil {
		err = validateStructuredResult(schema, data)
	}
	if err != nil {
		state := "invalid"
		if errors.Is(err, errStructuredResultMissing) {
			state = "missing"
		}
		return backend.Artifact{}, &store.StructuredResult{State: state}, err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	artifact := backend.Artifact{
		Name: StructuredResultArtifactName, MediaType: "application/json",
		Data: data, Size: int64(len(data)), Digest: digest,
	}
	return artifact, &store.StructuredResult{State: "valid", Digest: digest, Size: int64(len(data))}, nil
}

// unfenceJSON removes one Markdown code fence wrapped around the whole answer
// (```json … ``` or ``` … ```). Chat models routinely fence a JSON answer even
// when told not to; a fence is presentation, not content, so it is not a
// reason to fail an otherwise valid result. Anything else is returned as is,
// and structuredJSON decides.
func unfenceJSON(text string) string {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "```") || !strings.HasSuffix(t, "```") || len(t) < 6 {
		return text
	}
	body := strings.TrimSuffix(strings.TrimPrefix(t, "```"), "```")
	newline := strings.IndexByte(body, '\n')
	if newline < 0 {
		return text
	}
	// The info string ("json", "JSON", nothing) runs to the first newline.
	if info := strings.TrimSpace(body[:newline]); info != "" && !strings.EqualFold(info, "json") {
		return text
	}
	return body[newline+1:]
}
