// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"testing"

	"github.com/railgrid/provider-agents/backend"
)

func TestSessionCheckpointValidatesAndClonesContinuation(t *testing.T) {
	continuation := &backend.ContinuationMetadata{ReasoningSignature: "opaque-signature"}
	checkpoint := &SessionCheckpoint{
		Version: 1, ThroughSequence: 1,
		ReplacementHistory: []SessionCheckpointMessage{{Role: "assistant", Continuation: continuation}},
	}
	if err := validateSessionCheckpoint(checkpoint); err != nil {
		t.Fatalf("signature-only continuation checkpoint rejected: %v", err)
	}

	clone := cloneSessionSummary(SessionSummary{Checkpoint: checkpoint}).Checkpoint
	clone.ReplacementHistory[0].Continuation.ReasoningSignature = "changed"
	if continuation.ReasoningSignature != "opaque-signature" {
		t.Fatal("session summary clone aliases continuation metadata")
	}

	for name, invalid := range map[string]*SessionCheckpoint{
		"unsigned content": {
			Version: 1, ThroughSequence: 1,
			ReplacementHistory: []SessionCheckpointMessage{{Role: "assistant", Continuation: &backend.ContinuationMetadata{ReasoningContent: "unsigned"}}},
		},
		"wrong role": {
			Version: 1, ThroughSequence: 1,
			ReplacementHistory: []SessionCheckpointMessage{{Role: "user", Continuation: continuation}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSessionCheckpoint(invalid); err == nil {
				t.Fatal("invalid continuation checkpoint was accepted")
			}
		})
	}
}
