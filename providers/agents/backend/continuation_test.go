// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package backend

import (
	"strings"
	"testing"
)

func TestContinuationMetadataAllowlistAndCopy(t *testing.T) {
	input := map[string]any{
		continuationReasoningExtraKey: "private reasoning",
		continuationSignatureExtraKey: "signed bytes",
		"unrelated":                   "must not cross the seam",
	}
	metadata, err := ContinuationMetadataFromExtra(input)
	if err != nil {
		t.Fatal(err)
	}
	if metadata == nil || metadata.ReasoningContent != "private reasoning" || metadata.ReasoningSignature != "signed bytes" {
		t.Fatalf("metadata = %#v", metadata)
	}
	input[continuationReasoningExtraKey] = "changed"
	if metadata.ReasoningContent != "private reasoning" {
		t.Fatal("extracted metadata aliases the provider Extra map")
	}

	replayed := metadata.EinoExtra()
	if len(replayed) != 2 || replayed[continuationReasoningExtraKey] != "private reasoning" || replayed[continuationSignatureExtraKey] != "signed bytes" {
		t.Fatalf("replayed Extra = %#v", replayed)
	}
	replayed[continuationReasoningExtraKey] = "changed"
	if metadata.ReasoningContent != "private reasoning" {
		t.Fatal("replayed Extra aliases continuation metadata")
	}
}

func TestContinuationMetadataRejectsPartialAndOversizedValues(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"partial": {
			continuationReasoningExtraKey: "reasoning without signature",
		},
		"wrong type": {
			continuationReasoningExtraKey: "reasoning",
			continuationSignatureExtraKey: 1,
		},
		"wrong reasoning type": {
			continuationReasoningExtraKey: 42,
			continuationSignatureExtraKey: "signature",
		},
		"nil reasoning type": {
			continuationReasoningExtraKey: nil,
			continuationSignatureExtraKey: "signature",
		},
		"empty signature": {
			continuationSignatureExtraKey: "",
		},
		"oversized reasoning": {
			continuationReasoningExtraKey: strings.Repeat("x", MaxContinuationReasoningBytes+1),
			continuationSignatureExtraKey: "signature",
		},
		"oversized signature": {
			continuationReasoningExtraKey: "reasoning",
			continuationSignatureExtraKey: strings.Repeat("x", MaxContinuationSignatureBytes+1),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ContinuationMetadataFromExtra(extra); err == nil {
				t.Fatal("invalid continuation metadata was accepted")
			}
		})
	}
}

func TestContinuationMetadataEnvelopeAllowsOnlyKnownFields(t *testing.T) {
	metadata, err := ContinuationMetadataFromEnvelope(map[string]any{
		"reasoningContent":   "private reasoning",
		"reasoningSignature": "signed bytes",
	})
	if err != nil || metadata == nil {
		t.Fatalf("decode envelope = %#v, %v", metadata, err)
	}
	if _, err := ContinuationMetadataFromEnvelope(map[string]any{
		"reasoningContent":   "private reasoning",
		"reasoningSignature": "signed bytes",
		"opaqueHeader":       "must not persist",
	}); err == nil {
		t.Fatal("unknown continuation metadata field was accepted")
	}
}

func TestContinuationMetadataPreservesSignatureWhenThinkingIsOmitted(t *testing.T) {
	metadata, err := ContinuationMetadataFromExtra(map[string]any{
		continuationReasoningExtraKey: "",
		continuationSignatureExtraKey: "opaque-signature",
	})
	if err != nil || metadata == nil {
		t.Fatalf("signature-only continuation = %#v, %v", metadata, err)
	}
	if extra := metadata.EinoExtra(); extra[continuationReasoningExtraKey] != "" || extra[continuationSignatureExtraKey] != "opaque-signature" {
		t.Fatalf("signature-only Eino Extra = %#v", extra)
	}
	restored, err := ContinuationMetadataFromEnvelope(metadata)
	if err != nil || !restored.Equal(metadata) {
		t.Fatalf("signature-only envelope round trip = %#v, %v", restored, err)
	}

	metadata, err = ContinuationMetadataFromExtra(map[string]any{
		continuationSignatureExtraKey: "signature-without-thinking-key",
	})
	if err != nil || metadata == nil || metadata.ReasoningContent != "" || metadata.ReasoningSignature != "signature-without-thinking-key" {
		t.Fatalf("Extra with only signature = %#v, %v; want empty reasoning and preserved signature", metadata, err)
	}
}
