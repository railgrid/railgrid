// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/store"
)

const ResultContractPrefix = "railgrid-result-contract:"
const StructuredResultArtifact = store.StructuredResultArtifact

// ResultContract is consumer-owned configuration, not an Agents workflow.
// Its schema and tool name travel in the immutable invocation input.
type ResultContract struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

func Result(d Deps, input string) []engine.Tool {
	// Only transport metadata preceding the task body can define a contract.
	lines := strings.SplitN(input, "\n", 3)
	for _, line := range lines[:min(2, len(lines))] {
		if !strings.HasPrefix(line, ResultContractPrefix) {
			continue
		}
		var contract ResultContract
		if json.Unmarshal([]byte(strings.TrimPrefix(line, ResultContractPrefix)), &contract) != nil || contract.Name == "" || len(contract.Name) > 64 || len(contract.Schema) > 16384 {
			return nil
		}
		var schema jsonschema.Schema
		if json.Unmarshal(contract.Schema, &schema) != nil {
			return nil
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return nil
		}
		var advertised map[string]any
		if json.Unmarshal(contract.Schema, &advertised) != nil {
			return nil
		}
		return []engine.Tool{{Name: contract.Name, Desc: contract.Description, JSONSchema: advertised, ReplaySafe: true, Exec: func(ctx context.Context, args string) (string, error) {
			if len(args) > store.MaxStructuredResultBytes {
				return "", fmt.Errorf("structured result exceeds limit")
			}
			var value any
			if err := json.Unmarshal([]byte(args), &value); err != nil {
				return "", fmt.Errorf("invalid structured result: %w", err)
			}
			if err := resolved.Validate(value); err != nil {
				return "", fmt.Errorf("correct the result and retry: %w", err)
			}
			if d.Store == nil || d.RunID == "" {
				return "", fmt.Errorf("durable Run store unavailable")
			}
			data, err := json.Marshal(value)
			if err != nil {
				return "", err
			}
			sum := sha256.Sum256(data)
			artifact := store.RunArtifact{RunID: d.RunID, Name: StructuredResultArtifact, Digest: hex.EncodeToString(sum[:]), MediaType: "application/json", Size: int64(len(data)), Data: data, CreatedAt: time.Now().UTC()}
			if err = d.Store.SaveRunArtifact(ctx, d.Scope, artifact); err != nil {
				return "", err
			}
			return `{"recorded":true,"message":"Typed result recorded durably. The consumer independently validates completion evidence. Stop work after recording; identical retries are safe."}`, nil
		}}}
	}
	return nil
}
