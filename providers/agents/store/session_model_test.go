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
	"errors"
	"fmt"
	"sync"
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

func testSessionModelQuota(t *testing.T, s Store, scope Scope) {
	t.Helper()
	ctx := context.Background()
	if ps, ok := s.(*PostgresStore); ok {
		t.Cleanup(func() {
			if _, err := ps.db.ExecContext(ctx, `DELETE FROM agents_session_models WHERE org_uuid=$1`, scope.OrgUUID); err != nil {
				t.Errorf("clean session model preferences: %v", err)
			}
		})
	}

	// Preferences may be saved before the first chat message starts its session.
	beforeMessage := SessionModelSettings{ModelCredential: "model-before-first-message"}
	if err := s.PutSessionModelSettings(ctx, scope, "before-first-message", beforeMessage); err != nil {
		t.Fatalf("save preference before first message: %v", err)
	}
	if got, err := s.GetSessionModelSettings(ctx, scope, "before-first-message"); err != nil || got.ModelCredential != beforeMessage.ModelCredential {
		t.Fatalf("pre-message preference = %+v, err=%v", got, err)
	}

	// A quota belongs to a single scoped Agent, not its workspace or sibling Agents.
	otherAgent := scope
	otherAgent.AgentName += "-sibling"
	otherWorkspace := scope
	otherWorkspace.WorkspaceUUID += "-other"
	for _, other := range []Scope{otherAgent, otherWorkspace} {
		if err := s.PutSessionModelSettings(ctx, other, "same-session", SessionModelSettings{ModelCredential: "independent"}); err != nil {
			t.Fatalf("save scoped preference for %+v: %v", other, err)
		}
	}

	// Leave one slot, then have concurrent new-session writes compete for it.
	for i := 0; i < MaxSessionModelSettingsPerAgent-2; i++ {
		session := fmt.Sprintf("fill-%04d", i)
		if err := s.PutSessionModelSettings(ctx, scope, session, SessionModelSettings{ModelCredential: "filler"}); err != nil {
			t.Fatalf("fill preference slot %d: %v", i, err)
		}
	}
	const contenders = 12
	start := make(chan struct{})
	results := make(chan error, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results <- s.PutSessionModelSettings(ctx, scope, fmt.Sprintf("racer-%02d", i), SessionModelSettings{ModelCredential: "racer"})
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrSessionModelLimit):
			rejected++
		default:
			t.Fatalf("concurrent preference write: %v", err)
		}
	}
	if accepted != 1 || rejected != contenders-1 {
		t.Fatalf("concurrent writes accepted=%d rejected=%d; want one accepted at the cap", accepted, rejected)
	}

	// Updating a stored session does not consume another slot.
	updated := SessionModelSettings{ModelCredential: "updated-at-cap"}
	if err := s.PutSessionModelSettings(ctx, scope, "before-first-message", updated); err != nil {
		t.Fatalf("update preference at cap: %v", err)
	}
	if got, err := s.GetSessionModelSettings(ctx, scope, "before-first-message"); err != nil || got.ModelCredential != updated.ModelCredential {
		t.Fatalf("updated preference = %+v, err=%v", got, err)
	}

	// DeleteSession removes the corresponding preference row and returns its slot.
	if err := s.DeleteSession(ctx, scope, "before-first-message"); err != nil {
		t.Fatalf("delete session at cap: %v", err)
	}
	if err := s.PutSessionModelSettings(ctx, scope, "after-delete", SessionModelSettings{ModelCredential: "reclaimed"}); err != nil {
		t.Fatalf("use preference slot after DeleteSession: %v", err)
	}
	if err := s.PutSessionModelSettings(ctx, scope, "over-cap", SessionModelSettings{}); !errors.Is(err, ErrSessionModelLimit) {
		t.Fatalf("preference beyond cap = %v; want ErrSessionModelLimit", err)
	}
	for _, other := range []Scope{otherAgent, otherWorkspace} {
		if got, err := s.GetSessionModelSettings(ctx, other, "same-session"); err != nil || got.ModelCredential != "independent" {
			t.Fatalf("independent preference for %+v = %+v, err=%v", other, got, err)
		}
	}
}

func TestMemorySessionModelQuota(t *testing.T) {
	testSessionModelQuota(t, NewMemoryStore(), testScope())
}

func TestPostgresSessionModelQuota(t *testing.T) {
	s := openTestPostgres(t)
	testSessionModelQuota(t, s, pgScope(t, s))
}

func testListSessionsTitlePreviewAcrossSessions(t *testing.T, s Store, scope Scope) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	messages := []Message{
		{ID: scope.OrgUUID + "-a-first", AgentName: scope.AgentName, SessionID: "list-a", Role: "user", Content: "A appended first", CreatedAt: now.Add(2 * time.Second)},
		{ID: scope.OrgUUID + "-b-first", AgentName: scope.AgentName, SessionID: "list-b", Role: "user", Content: "B first", CreatedAt: now.Add(3 * time.Second)},
		{ID: scope.OrgUUID + "-a-preview", AgentName: scope.AgentName, SessionID: "list-a", Role: "user", Content: "A earlier timestamp preview", CreatedAt: now.Add(time.Second)},
		{ID: scope.OrgUUID + "-b-later", AgentName: scope.AgentName, SessionID: "list-b", Role: "user", Content: "B later", CreatedAt: now.Add(4 * time.Second)},
		{ID: scope.OrgUUID + "-c-assistant", AgentName: scope.AgentName, SessionID: "list-c", Role: "assistant", Content: "No user preview", CreatedAt: now.Add(5 * time.Second)},
	}
	for _, message := range messages {
		if err := s.AppendMessage(ctx, scope, message); err != nil {
			t.Fatalf("append %s: %v", message.ID, err)
		}
	}
	for _, title := range []struct {
		session string
		firstID string
		value   string
	}{
		{session: "list-a", firstID: messages[0].ID, value: "Conversation A"},
		{session: "list-b", firstID: messages[1].ID, value: "Conversation B"},
	} {
		if claimed, err := s.ClaimSessionTitle(ctx, scope, title.session, title.firstID); err != nil || !claimed {
			t.Fatalf("claim %s title = %t, err=%v", title.session, claimed, err)
		}
		if written, err := s.SetSessionTitleIfEmpty(ctx, scope, title.session, title.firstID, title.value); err != nil || !written {
			t.Fatalf("write %s title = %t, err=%v", title.session, written, err)
		}
	}

	sessions, err := s.ListSessions(ctx, scope, 10)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	byID := make(map[string]Session, len(sessions))
	for _, session := range sessions {
		byID[session.ID] = session
	}
	for id, want := range map[string]struct {
		count   int
		title   string
		preview string
	}{
		"list-a": {count: 2, title: "Conversation A", preview: "A earlier timestamp preview"},
		"list-b": {count: 2, title: "Conversation B", preview: "B first"},
		"list-c": {count: 1, title: "", preview: ""},
	} {
		got, ok := byID[id]
		if !ok || got.MessageCount != want.count || got.Title != want.title || got.Preview != want.preview {
			t.Errorf("session %s = %+v, present=%t; want count/title/preview %d/%q/%q", id, got, ok, want.count, want.title, want.preview)
		}
	}
}

func TestMemoryListSessionsTitlePreviewAcrossSessions(t *testing.T) {
	testListSessionsTitlePreviewAcrossSessions(t, NewMemoryStore(), testScope())
}

func TestPostgresListSessionsTitlePreviewAcrossSessions(t *testing.T) {
	s := openTestPostgres(t)
	testListSessionsTitlePreviewAcrossSessions(t, s, pgScope(t, s))
}
