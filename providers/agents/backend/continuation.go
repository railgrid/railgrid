// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	ContinuationMetadataEnvelopeKey = "continuation"

	continuationReasoningExtraKey = "_eino_claude_thinking"
	continuationSignatureExtraKey = "_eino_claude_thinking_signature"

	// These caps bound what can be copied from provider protocol metadata into
	// checkpoints and transcript metadata. Values are rejected, never truncated,
	// because the signature authenticates the exact reasoning bytes.
	MaxContinuationReasoningBytes = 1 << 20
	MaxContinuationSignatureBytes = 1 << 20
)

// ContinuationMetadata is bounded protocol state needed to continue a model
// reasoning response across tool calls, approvals, and durable replay. It is
// provider-neutral at the backend seam; adapters map their own wire fields to
// this allowlisted pair. It must not be presented as user-facing prose.
type ContinuationMetadata struct {
	ReasoningContent   string `json:"reasoningContent,omitempty"`
	ReasoningSignature string `json:"reasoningSignature,omitempty"`
}

// Validate checks that the continuation is complete and within the storage
// bounds. Empty reasoning content is valid when a nonempty signature was
// issued for a thinking block whose display was omitted.
func (m ContinuationMetadata) Validate() error {
	if m.ReasoningSignature == "" {
		return errors.New("continuation metadata requires a signature")
	}
	if len(m.ReasoningContent) > MaxContinuationReasoningBytes {
		return fmt.Errorf("continuation reasoning content exceeds %d bytes", MaxContinuationReasoningBytes)
	}
	if len(m.ReasoningSignature) > MaxContinuationSignatureBytes {
		return fmt.Errorf("continuation reasoning signature exceeds %d bytes", MaxContinuationSignatureBytes)
	}
	return nil
}

// Clone returns an independent copy, or nil for a nil receiver.
func (m *ContinuationMetadata) Clone() *ContinuationMetadata {
	if m == nil {
		return nil
	}
	copy := *m
	return &copy
}

// Equal reports exact equality, including nil state.
func (m *ContinuationMetadata) Equal(other *ContinuationMetadata) bool {
	if m == nil || other == nil {
		return m == nil && other == nil
	}
	return m.ReasoningContent == other.ReasoningContent && m.ReasoningSignature == other.ReasoningSignature
}

// ContinuationMetadataFromExtra copies only the two native Claude fields from
// Eino message Extra. Unknown Extra values are deliberately ignored.
func ContinuationMetadataFromExtra(extra map[string]any) (*ContinuationMetadata, error) {
	if extra == nil {
		return nil, nil
	}
	reasoning, hasReasoning := extra[continuationReasoningExtraKey]
	signature, hasSignature := extra[continuationSignatureExtraKey]
	if !hasReasoning && !hasSignature {
		return nil, nil
	}
	signatureText, ok := signature.(string)
	if !ok {
		return nil, errors.New("continuation reasoning signature must be a string")
	}
	var reasoningText string
	if hasReasoning {
		reasoningText, ok = reasoning.(string)
		if !ok {
			return nil, errors.New("continuation reasoning content must be a string")
		}
	}
	metadata := &ContinuationMetadata{ReasoningContent: reasoningText, ReasoningSignature: signatureText}
	if err := metadata.Validate(); err != nil {
		return nil, err
	}
	return metadata, nil
}

// EinoExtra returns a fresh allowlisted Extra map suitable for adapter replay.
func (m *ContinuationMetadata) EinoExtra() map[string]any {
	if m == nil || m.Validate() != nil {
		return nil
	}
	return map[string]any{
		continuationReasoningExtraKey: m.ReasoningContent,
		continuationSignatureExtraKey: m.ReasoningSignature,
	}
}

// ContinuationMetadataFromEnvelope reads the existing JSON metadata envelope
// used by transcript rows. Only the two known string fields are accepted.
func ContinuationMetadataFromEnvelope(value any) (*ContinuationMetadata, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal continuation metadata: %w", err)
	}
	if len(encoded) > MaxContinuationReasoningBytes+MaxContinuationSignatureBytes+256 {
		return nil, errors.New("continuation metadata envelope exceeds the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var metadata ContinuationMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return nil, fmt.Errorf("decode continuation metadata: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("continuation metadata envelope has trailing data")
	}
	if err := metadata.Validate(); err != nil {
		return nil, err
	}
	return &metadata, nil
}
