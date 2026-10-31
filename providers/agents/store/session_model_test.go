// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"testing"
	"time"
)

func testSessionModelIsolation(t *testing.T, s Store, sc Scope) {
	t.Helper()
	ctx := context.Background()
	prefs := SessionModelSettings{ModelCredential: "chosen", ReasoningEffortByModel: map[string]string{"chosen": "high"}}
	if err := s.PutSessionModelSettings(ctx, sc, "one", prefs); err != nil {
		t.Fatal(err)
	}
	other := sc
	other.WorkspaceUUID += "-other"
	for _, tc := range []struct {
		scope   Scope
		session string
	}{{sc, "two"}, {other, "one"}} {
		got, err := s.GetSessionModelSettings(ctx, tc.scope, tc.session)
		if err != nil || got.ModelCredential != "" {
			t.Fatalf("leaked preference: %+v %v", got, err)
		}
	}
	got, err := s.GetSessionModelSettings(ctx, sc, "one")
	if err != nil || got.ModelCredential != "chosen" || got.ReasoningEffortByModel["chosen"] != "high" {
		t.Fatalf("preferences: %+v %v", got, err)
	}
	now := time.Now()
	run := Run{ID: "model-snapshot", AgentName: sc.AgentName, SessionID: "one", Phase: RunPhaseRunning, CreatedAt: now, UpdatedAt: now, ModelSettings: &ModelSettings{ModelCredential: "chosen", ReasoningEffort: "high"}}
	if err := s.SaveRun(ctx, sc, run); err != nil {
		t.Fatal(err)
	}
	run.ModelSettings = &ModelSettings{ModelCredential: "different"}
	if err := s.SaveRun(ctx, sc, run); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetRun(ctx, sc, run.ID)
	if err != nil || saved.ModelSettings == nil || saved.ModelSettings.ModelCredential != "chosen" || saved.ModelSettings.ReasoningEffort != "high" {
		t.Fatalf("snapshot changed: %+v %v", saved.ModelSettings, err)
	}
	if err := s.DeleteSession(ctx, sc, "one"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSessionModelSettings(ctx, sc, "one")
	if err != nil || got.ModelCredential != "" {
		t.Fatalf("session preferences not deleted: %+v %v", got, err)
	}
}
func TestMemorySessionModelIsolation(t *testing.T) {
	testSessionModelIsolation(t, NewMemoryStore(), testScope())
}
func TestPostgresSessionModelIsolation(t *testing.T) {
	s := openTestPostgres(t)
	testSessionModelIsolation(t, s, pgScope(t, s))
}
