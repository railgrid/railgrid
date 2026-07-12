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

func testScope() Scope {
	return Scope{OrgUUID: "org1", WorkspaceUUID: "ws1", AgentName: "helper"}
}

func TestMemoryStore_MessagesRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	base := time.Now().UTC()

	for i := range 5 {
		if err := s.AppendMessage(ctx, sc, Message{
			ID:        string(rune('a' + i)),
			AgentName: sc.AgentName,
			SessionID: "sess",
			Role:      "user",
			Content:   "hi",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	recent, err := s.LoadRecentMessages(ctx, sc, "sess", 3)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 3 || recent[len(recent)-1].ID != "e" {
		t.Fatalf("recent got %d msgs, last=%q", len(recent), recent[len(recent)-1].ID)
	}

	// Cursor pagination: 2 + 2 + 1.
	seen := 0
	cursor := ""
	for range 10 {
		page, err := s.ListMessages(ctx, sc, "sess", 2, cursor)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		seen += len(page.Items)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if seen != 5 {
		t.Fatalf("paginated %d messages, want 5", seen)
	}
}

func TestMemoryStore_RunClaimIsExclusive(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	now := time.Now().UTC()

	if err := s.SaveRun(ctx, sc, Run{ID: "r1", AgentName: sc.AgentName, Trigger: "schedule", Phase: RunPhasePending, CreatedAt: now}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := s.ClaimRun(ctx, sc, "r1", "req-1", now); err != nil {
		t.Fatalf("first claim should win: %v", err)
	}
	if _, err := s.ClaimRun(ctx, sc, "r1", "req-2", now); err == nil {
		t.Fatalf("second claim should fail while running")
	}
}

func TestMemoryStore_UsageAccumulates(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	now := time.Now().UTC()
	win := 30 * 24 * time.Hour

	if _, err := s.AddUsage(ctx, sc, "helper", 100, 50, 2000, now, win); err != nil {
		t.Fatalf("add usage: %v", err)
	}
	u, err := s.AddUsage(ctx, sc, "helper", 10, 5, 300, now, win)
	if err != nil {
		t.Fatalf("add usage 2: %v", err)
	}
	if u.InputTokens != 110 || u.OutputTokens != 55 || u.USDMicros != 2300 {
		t.Fatalf("usage rollup wrong: %+v", u)
	}
	got, err := s.GetUsage(ctx, sc, "helper", now, win)
	if err != nil {
		t.Fatalf("get usage: %v", err)
	}
	if got.USDMicros != 2300 {
		t.Fatalf("get usage got %d micros, want 2300", got.USDMicros)
	}
}

func TestMemoryStore_InboxResolve(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	sc := testScope()
	now := time.Now().UTC()

	if err := s.AddInboxItem(ctx, sc, InboxItem{
		ID: "i1", AgentName: "helper", RunID: "r1",
		Kind: InboxKindApproval, State: InboxStatePending,
		Prompt: "merge PR #42?", CreatedAt: now,
	}); err != nil {
		t.Fatalf("add inbox: %v", err)
	}
	pending, err := s.ListInbox(ctx, sc, InboxStatePending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("list pending: %v n=%d", err, len(pending))
	}
	if _, err := s.ResolveInboxItem(ctx, sc, "i1", InboxStateApproved, "ok", now); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	pending, _ = s.ListInbox(ctx, sc, InboxStatePending)
	if len(pending) != 0 {
		t.Fatalf("still %d pending after resolve", len(pending))
	}
}
