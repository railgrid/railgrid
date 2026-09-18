// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"errors"
	"testing"
)

// A cancel that arrives through CheckAbort (a durable flag written elsewhere)
// stops the loop before the next tool call and before the next model round,
// with that error, rather than after the whole turn.
func TestCheckAbortStopsBetweenToolRounds(t *testing.T) {
	model := &repeatedToolModel{}
	stop := errors.New("cancelled by user")
	toolCalls := 0
	checks := 0
	res, err := New().StreamTurnWithTools(context.Background(), model,
		[]Message{{Role: RoleUser, Content: "keep trying"}},
		[]Tool{{Name: "noop", Desc: "does nothing", Exec: func(context.Context, string) (string, error) {
			toolCalls++
			return "ok", nil
		}}},
		TurnConfig{MaxIters: 4}, Callbacks{CheckAbort: func(context.Context) error {
			checks++
			// Let the first model round and its tool call through; stop the
			// second round before it asks the model again.
			if checks >= 3 {
				return stop
			}
			return nil
		}})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the abort error", err)
	}
	if res.Content != "" {
		t.Fatalf("an aborted turn returns no result, got %q", res.Content)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d, want 1 (the second round must not start)", model.calls)
	}
	if toolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", toolCalls)
	}
}

// Without the hook nothing changes: the loop runs to its limit as before.
func TestNoCheckAbortRunsToLimit(t *testing.T) {
	model := &repeatedToolModel{}
	_, err := New().StreamTurnWithTools(context.Background(), model,
		[]Message{{Role: RoleUser, Content: "keep trying"}},
		[]Tool{{Name: "noop", Desc: "does nothing", Exec: func(context.Context, string) (string, error) { return "ok", nil }}},
		TurnConfig{MaxIters: 2}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls = %d, want 2", model.calls)
	}
}
