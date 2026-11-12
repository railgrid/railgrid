// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"testing"
	"time"
)

func TestMemoryDelegatedUsageIsOnceAcrossReplayAndWindowRollover(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	parentScope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
	childScope := parentScope
	childScope.AgentName = "reviewer"
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, seed := range []struct {
		scope Scope
		run   Run
	}{
		{parentScope, Run{ID: "parent", AgentName: parentScope.AgentName, Phase: RunPhaseRunning, CreatedAt: now, UpdatedAt: now}},
		{childScope, Run{ID: "child", AgentName: childScope.AgentName, ParentRunID: "parent", Phase: RunPhaseSucceeded, CreatedAt: now, UpdatedAt: now}},
	} {
		if err := m.SaveRun(ctx, seed.scope, seed.run); err != nil {
			t.Fatal(err)
		}
	}
	stale, err := m.GetRun(ctx, childScope, "child")
	if err != nil {
		t.Fatal(err)
	}
	window := 24 * time.Hour
	usage, applied, err := m.AddDelegatedUsage(ctx, parentScope, "parent", "child", 12, 7, 90, now, window)
	if err != nil || !applied || usage.InputTokens != 12 || usage.OutputTokens != 7 || usage.USDMicros != 90 {
		t.Fatalf("first contribution = %+v applied=%t err=%v", usage, applied, err)
	}
	stale.Message = "later bookkeeping from a pre-rollup copy"
	if err := m.SaveRun(ctx, childScope, stale); err != nil {
		t.Fatal(err)
	}
	stored, err := m.GetRun(ctx, childScope, "child")
	if err != nil || !stored.DelegatedUsageRolledUp {
		t.Fatalf("stale save cleared contribution marker: marker=%t err=%v", stored.DelegatedUsageRolledUp, err)
	}
	usage, applied, err = m.AddDelegatedUsage(ctx, parentScope, "parent", "child", 12, 7, 90, now.Add(48*time.Hour), window)
	if err != nil || applied {
		t.Fatalf("replay after window rollover applied=%t err=%v", applied, err)
	}
	usage, err = m.GetUsage(ctx, parentScope, parentScope.AgentName, now, window)
	if err != nil || usage.InputTokens != 12 || usage.OutputTokens != 7 || usage.USDMicros != 90 {
		t.Fatalf("original usage bucket = %+v err=%v", usage, err)
	}
	usage, err = m.GetUsage(ctx, parentScope, parentScope.AgentName, now.Add(48*time.Hour), window)
	if err != nil || usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.USDMicros != 0 {
		t.Fatalf("new usage bucket = %+v err=%v", usage, err)
	}
}

func TestMemoryDelegatedUsageRejectsNegativeValuesWithoutMarkingChild(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		in, out   int64
		usdMicros int64
	}{
		{name: "input", in: -1},
		{name: "output", out: -1},
		{name: "cost", usdMicros: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMemoryStore()
			scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coordinator"}
			childScope := scope
			childScope.AgentName = "reviewer"
			now := time.Now().UTC()
			if err := m.SaveRun(ctx, scope, Run{ID: "parent", AgentName: scope.AgentName, Phase: RunPhaseRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := m.SaveRun(ctx, childScope, Run{ID: "child", AgentName: childScope.AgentName, ParentRunID: "parent", Phase: RunPhaseSucceeded, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if _, applied, err := m.AddDelegatedUsage(ctx, scope, "parent", "child", tc.in, tc.out, tc.usdMicros, now, time.Hour); err == nil || applied {
				t.Fatalf("negative contribution applied=%t err=%v", applied, err)
			}
			child, err := m.GetRun(ctx, childScope, "child")
			if err != nil || child.DelegatedUsageRolledUp {
				t.Fatalf("failed contribution changed marker=%t err=%v", child.DelegatedUsageRolledUp, err)
			}
		})
	}
}
