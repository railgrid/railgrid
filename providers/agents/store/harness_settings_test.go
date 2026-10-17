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
	"time"
)

func TestHarnessSettingsAreWriteOnce(t *testing.T) {
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"}
	original := &HarnessSettings{Model: "gpt-6-luna", ReasoningEffort: "max"}
	run := Run{ID: "run", AgentName: "agent", Phase: RunPhasePending, CreatedAt: time.Now(), UpdatedAt: time.Now(), HarnessSettings: original}
	if err := s.SaveRun(t.Context(), scope, run); err != nil {
		t.Fatal(err)
	}
	original.ReasoningEffort = "low"
	run.HarnessSettings = nil
	if err := s.SaveRun(t.Context(), scope, run); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(t.Context(), scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.HarnessSettings == nil || got.HarnessSettings.ReasoningEffort != "max" {
		t.Fatalf("settings = %+v", got.HarnessSettings)
	}
}

func TestPostgresHarnessSettingsAreWriteOnce(t *testing.T) {
	s, _ := openLegacyHistorySchema(t)
	if err := s.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	scope := testScope()
	run := Run{ID: "reasoning-snapshot", AgentName: scope.AgentName, Phase: RunPhasePending, CreatedAt: time.Now(), UpdatedAt: time.Now(), HarnessSettings: &HarnessSettings{Model: "gpt-6-luna", ReasoningEffort: "max"}}
	if err := s.SaveRun(t.Context(), scope, run); err != nil {
		t.Fatal(err)
	}
	run.HarnessSettings = &HarnessSettings{Model: "changed", ReasoningEffort: "low"}
	if err := s.SaveRun(t.Context(), scope, run); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(t.Context(), scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.HarnessSettings == nil || got.HarnessSettings.Model != "gpt-6-luna" || got.HarnessSettings.ReasoningEffort != "max" {
		t.Fatalf("settings = %+v", got.HarnessSettings)
	}
}
