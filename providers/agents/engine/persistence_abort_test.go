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

func TestToolPersistenceFailureAbortsFinalToolRound(t *testing.T) {
	failed := false
	sentinel := errors.New("result persistence failed")
	model := &toolMockModel{}
	_, err := New().StreamTurnWithTools(context.Background(), model,
		[]Message{{Role: RoleUser, Content: "weather?"}},
		[]Tool{{Name: "get_weather", Exec: func(context.Context, string) (string, error) { return "sunny", nil }}},
		TurnConfig{MaxIters: 1}, Callbacks{
			OnTool: func(ToolEvent) { failed = true },
			CheckAbort: func(context.Context) error {
				if failed {
					return sentinel
				}
				return nil
			},
		})
	if !errors.Is(err, sentinel) {
		t.Fatalf("final tool round error = %v, want persistence error", err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls=%d, want one", model.calls)
	}
}
