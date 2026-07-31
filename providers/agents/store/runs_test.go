// Copyright 2026 The Faros Authors.
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

func seedRuns(t *testing.T, st Store, scope Scope) {
	t.Helper()
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	runs := []Run{
		{ID: "r1", AgentName: "ada", SessionID: "default", Trigger: "chat", Phase: RunPhaseSucceeded},
		{ID: "r2", AgentName: "ada", SessionID: "schedule:brief", Trigger: "schedule", Phase: RunPhaseFailed},
		{ID: "r3", AgentName: "bob", SessionID: "default", Trigger: "chat", Phase: RunPhasePendingApproval},
		{ID: "r4", AgentName: "ada", SessionID: "default", Trigger: "delegation", Phase: RunPhaseSucceeded, ParentRunID: "r1"},
	}
	for i, run := range runs {
		run.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		run.UpdatedAt = run.CreatedAt
		s := scope
		s.AgentName = run.AgentName
		if err := st.SaveRun(context.Background(), s, run); err != nil {
			t.Fatalf("seed %s: %v", run.ID, err)
		}
	}
}

func TestQueryRunsFilters(t *testing.T) {
	st := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws"}
	seedRuns(t, st, scope)
	ctx := context.Background()

	ids := func(page RunPage) []string {
		out := make([]string, 0, len(page.Items))
		for _, r := range page.Items {
			out = append(out, r.ID)
		}
		return out
	}

	t.Run("newest first", func(t *testing.T) {
		page, err := st.QueryRuns(ctx, scope, RunQuery{})
		if err != nil {
			t.Fatal(err)
		}
		got := ids(page)
		want := []string{"r4", "r3", "r2", "r1"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})

	t.Run("by agent", func(t *testing.T) {
		s := scope
		s.AgentName = "ada"
		page, _ := st.QueryRuns(ctx, s, RunQuery{})
		for _, r := range page.Items {
			if r.AgentName != "ada" {
				t.Fatalf("agent filter leaked %s", r.AgentName)
			}
		}
		if len(page.Items) != 3 {
			t.Fatalf("want 3 ada runs, got %d", len(page.Items))
		}
	})

	t.Run("by phase", func(t *testing.T) {
		page, _ := st.QueryRuns(ctx, scope, RunQuery{Phase: RunPhasePendingApproval})
		if len(page.Items) != 1 || page.Items[0].ID != "r3" {
			t.Fatalf("phase filter = %v", ids(page))
		}
	})

	t.Run("by trigger and session", func(t *testing.T) {
		page, _ := st.QueryRuns(ctx, scope, RunQuery{Trigger: "schedule"})
		if len(page.Items) != 1 || page.Items[0].ID != "r2" {
			t.Fatalf("trigger filter = %v", ids(page))
		}
		page, _ = st.QueryRuns(ctx, scope, RunQuery{SessionID: "schedule:brief"})
		if len(page.Items) != 1 || page.Items[0].ID != "r2" {
			t.Fatalf("session filter = %v", ids(page))
		}
	})

	t.Run("delegation children by parent", func(t *testing.T) {
		page, _ := st.QueryRuns(ctx, scope, RunQuery{ParentRunID: "r1"})
		if len(page.Items) != 1 || page.Items[0].ID != "r4" {
			t.Fatalf("parent filter = %v", ids(page))
		}
	})

	t.Run("cursor pagination walks every run once", func(t *testing.T) {
		seen := map[string]bool{}
		cursor := ""
		for range 10 {
			page, err := st.QueryRuns(ctx, scope, RunQuery{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range page.Items {
				if seen[r.ID] {
					t.Fatalf("run %s returned twice across pages", r.ID)
				}
				seen[r.ID] = true
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
		if len(seen) != 4 {
			t.Fatalf("paged through %d runs, want 4", len(seen))
		}
	})

	t.Run("scopes are isolated", func(t *testing.T) {
		other := Scope{OrgUUID: "org", WorkspaceUUID: "other-ws"}
		page, _ := st.QueryRuns(ctx, other, RunQuery{})
		if len(page.Items) != 0 {
			t.Fatalf("cross-tenant leak: %v", ids(page))
		}
	})
}

func TestListToolCallsOrdersByExecution(t *testing.T) {
	st := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "ada"}
	ctx := context.Background()
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	for i, tc := range []ToolCall{
		{ID: "c2", RunID: "r1", Tool: "second", Outcome: "ok"},
		{ID: "c1", RunID: "r1", Tool: "first", Outcome: "ok"},
		{ID: "c3", RunID: "other", Tool: "elsewhere", Outcome: "ok"},
	} {
		// c1 executed before c2 despite insertion order.
		offsets := map[string]int{"c1": 0, "c2": 1, "c3": 2}
		tc.AgentName = "ada"
		tc.CreatedAt = base.Add(time.Duration(offsets[tc.ID]) * time.Second)
		if err := st.AppendToolCall(ctx, scope, tc); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	calls, err := st.ListToolCalls(ctx, scope, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("want 2 calls for r1, got %d", len(calls))
	}
	if calls[0].Tool != "first" || calls[1].Tool != "second" {
		t.Fatalf("steps out of execution order: %s, %s", calls[0].Tool, calls[1].Tool)
	}
}
