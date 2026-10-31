// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemorySessionTitleContract(t *testing.T) {
	testSessionTitleContract(t, NewMemoryStore(), testScope())
}

func TestPostgresSessionTitleContract(t *testing.T) {
	ps := openTestPostgres(t)
	testSessionTitleContract(t, ps, pgScope(t, ps))
}

func TestMemoryRunPurgeClearsOnlyStaleSessionTitle(t *testing.T) {
	testRunPurgeSessionTitleContract(t, NewMemoryStore(), testScope())
}

func TestPostgresRunPurgeClearsOnlyStaleSessionTitle(t *testing.T) {
	ps := openTestPostgres(t)
	testRunPurgeSessionTitleContract(t, ps, pgScope(t, ps))
}

func testRunPurgeSessionTitleContract(t *testing.T, s Store, scope Scope) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	sessionID := "title-purge-" + uuid.NewString()
	firstRunID, laterRunID, nextRunID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	firstMessageID, laterMessageID, nextMessageID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for i, entry := range []struct {
		runID     string
		messageID string
		content   string
	}{
		{firstRunID, firstMessageID, "Original first request"},
		{laterRunID, laterMessageID, "Later request"},
	} {
		at := now.Add(time.Duration(i) * time.Second)
		if err := s.SaveRun(ctx, scope, Run{ID: entry.runID, AgentName: scope.AgentName, SessionID: sessionID,
			Phase: RunPhaseSucceeded, CreatedAt: at, UpdatedAt: at}); err != nil {
			t.Fatalf("save run %s: %v", entry.runID, err)
		}
		if err := s.AppendMessage(ctx, scope, Message{ID: entry.messageID, AgentName: scope.AgentName,
			SessionID: sessionID, RunID: entry.runID, Role: "user", Content: entry.content, CreatedAt: at}); err != nil {
			t.Fatalf("append message %s: %v", entry.messageID, err)
		}
	}
	if claimed, err := s.ClaimSessionTitle(ctx, scope, sessionID, firstMessageID); err != nil || !claimed {
		t.Fatalf("claim original title = %t, err=%v", claimed, err)
	}
	if written, err := s.SetSessionTitleIfEmpty(ctx, scope, sessionID, firstMessageID, "Original conversation title"); err != nil || !written {
		t.Fatalf("write original title = %t, err=%v", written, err)
	}

	// Removing a later run leaves the first message and its title valid.
	if err := s.DeleteRunData(ctx, scope, laterRunID); err != nil {
		t.Fatalf("delete later run: %v", err)
	}
	sessions, err := s.ListSessions(ctx, scope, 10)
	if err != nil || len(sessions) != 1 || sessions[0].Title != "Original conversation title" {
		t.Fatalf("session after later-run purge = %+v, err=%v; want original title retained", sessions, err)
	}

	// Add a surviving message from another run, then remove the run that owned
	// the title's first-user coordinate. Its stale claim must be cleared so the
	// same session ID can accept a new first message and title.
	if err := s.SaveRun(ctx, scope, Run{ID: nextRunID, AgentName: scope.AgentName, SessionID: sessionID,
		Phase: RunPhaseSucceeded, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)}); err != nil {
		t.Fatalf("save next run: %v", err)
	}
	if err := s.AppendMessage(ctx, scope, Message{ID: nextMessageID, AgentName: scope.AgentName,
		SessionID: sessionID, RunID: nextRunID, Role: "user", Content: "Surviving request", CreatedAt: now.Add(2 * time.Second)}); err != nil {
		t.Fatalf("append surviving message: %v", err)
	}
	if err := s.DeleteRunData(ctx, scope, firstRunID); err != nil {
		t.Fatalf("delete first run: %v", err)
	}
	if written, err := s.SetSessionTitleIfEmpty(ctx, scope, sessionID, firstMessageID, "Late deleted-run title"); err != nil || written {
		t.Fatalf("late title result after first-message purge = %t, err=%v; want false", written, err)
	}
	sessions, err = s.ListSessions(ctx, scope, 10)
	if err != nil || len(sessions) != 1 || sessions[0].Title != "" || sessions[0].Preview != "Surviving request" {
		t.Fatalf("session after first-message purge = %+v, err=%v; want surviving preview and no stale title", sessions, err)
	}
	if claimed, err := s.ClaimSessionTitle(ctx, scope, sessionID, nextMessageID); err != nil || !claimed {
		t.Fatalf("claim new first message after purge = %t, err=%v", claimed, err)
	}
	if written, err := s.SetSessionTitleIfEmpty(ctx, scope, sessionID, nextMessageID, "Surviving conversation title"); err != nil || !written {
		t.Fatalf("write new title after purge = %t, err=%v", written, err)
	}
}

func testSessionTitleContract(t *testing.T, s Store, scope Scope) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	sessionID := "title-contract"
	firstID := "title-first-" + uuid.NewString()
	secondID := "title-second-" + uuid.NewString()
	if err := s.AppendMessage(ctx, scope, Message{
		ID: firstID, AgentName: scope.AgentName, SessionID: sessionID, Role: "user", Content: "First persisted request",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("append first user message: %v", err)
	}
	if claimed, err := s.ClaimSessionTitle(ctx, scope, sessionID, "missing"); err != nil || claimed {
		t.Fatalf("claim nonexistent first message = %t, err=%v; want false", claimed, err)
	}
	if written, err := s.SetSessionTitleIfEmpty(ctx, scope, sessionID, firstID, "unclaimed"); err != nil || written {
		t.Fatalf("set title without a claim = %t, err=%v; want false", written, err)
	}
	// Append order, rather than caller timestamps, identifies the first
	// persisted user message. Preview ordering intentionally keeps its existing
	// timestamp behavior.
	if err := s.AppendMessage(ctx, scope, Message{
		ID: secondID, AgentName: scope.AgentName, SessionID: sessionID, Role: "user", Content: "Earlier timestamp preview",
		CreatedAt: now.Add(-time.Second),
	}); err != nil {
		t.Fatalf("append second user message: %v", err)
	}
	if claimed, err := s.ClaimSessionTitle(ctx, scope, sessionID, secondID); err != nil || claimed {
		t.Fatalf("claim later persisted user message = %t, err=%v; want false", claimed, err)
	}
	// Concurrent attempts must still produce exactly one owner.
	const claimers = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimWins := 0
	claimErrs := make([]error, 0)
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := s.ClaimSessionTitle(ctx, scope, sessionID, firstID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				claimErrs = append(claimErrs, err)
			} else if claimed {
				claimWins++
			}
		}()
	}
	wg.Wait()
	if len(claimErrs) != 0 || claimWins != 1 {
		t.Fatalf("concurrent claim wins=%d errors=%v; want one owner", claimWins, claimErrs)
	}
	if claimed, err := s.ClaimSessionTitle(ctx, scope, sessionID, firstID); err != nil || claimed {
		t.Fatalf("repeat claim = %t, err=%v; want false", claimed, err)
	}

	// A title claim is scoped by organization, workspace, and agent.
	otherScopes := []Scope{
		{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, AgentName: scope.AgentName + "-other-agent"},
		{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID + "-other-workspace", AgentName: scope.AgentName},
		{OrgUUID: scope.OrgUUID + "-other-org", WorkspaceUUID: scope.WorkspaceUUID, AgentName: scope.AgentName},
	}
	if ps, ok := s.(*PostgresStore); ok {
		for _, other := range otherScopes {
			if other.OrgUUID != scope.OrgUUID {
				otherOrg := other.OrgUUID
				t.Cleanup(func() { purgeTestScope(t, ps, otherOrg) })
			}
		}
	}
	for i, other := range otherScopes {
		otherID := fmt.Sprintf("isolated-first-%d-%s", i, uuid.NewString())
		if err := s.AppendMessage(ctx, other, Message{
			ID: otherID, AgentName: other.AgentName, SessionID: sessionID, Role: "user", Content: "isolated transcript", CreatedAt: now,
		}); err != nil {
			t.Fatalf("append isolated message %d: %v", i, err)
		}
		if claimed, err := s.ClaimSessionTitle(ctx, other, sessionID, otherID); err != nil || !claimed {
			t.Fatalf("claim isolated session %d = %t, err=%v", i, claimed, err)
		}
		if written, err := s.SetSessionTitleIfEmpty(ctx, other, sessionID, otherID, "Other scope title"); err != nil || !written {
			t.Fatalf("set isolated session %d = %t, err=%v", i, written, err)
		}
	}

	// Multiple title results race through a compare-and-set. Only one can win.
	titleWins := 0
	titleErrs := make([]error, 0)
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			written, err := s.SetSessionTitleIfEmpty(ctx, scope, sessionID, firstID, fmt.Sprintf("Generated title %d", i))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				titleErrs = append(titleErrs, err)
			} else if written {
				titleWins++
			}
		}(i)
	}
	wg.Wait()
	if len(titleErrs) != 0 || titleWins != 1 {
		t.Fatalf("title CAS wins=%d errors=%v; want one writer", titleWins, titleErrs)
	}

	sessions, err := s.ListSessions(ctx, scope, 10)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("list titled session = %+v, err=%v", sessions, err)
	}
	if sessions[0].Title == "" || sessions[0].Preview != "Earlier timestamp preview" {
		t.Fatalf("session title/preview = %q/%q; want generated title and unchanged preview fallback", sessions[0].Title, sessions[0].Preview)
	}
	for _, other := range otherScopes {
		otherSessions, err := s.ListSessions(ctx, other, 10)
		if err != nil || len(otherSessions) != 1 || otherSessions[0].Title != "Other scope title" {
			t.Fatalf("isolated sessions for %+v = %+v, err=%v", other, otherSessions, err)
		}
	}

	// Deleting and recreating a conversation under the same ID invalidates the
	// previous claim. Its late result cannot write onto the new conversation.
	if err := s.DeleteSession(ctx, scope, sessionID); err != nil {
		t.Fatalf("delete titled session: %v", err)
	}
	newID := "title-recreated-first-" + uuid.NewString()
	if err := s.AppendMessage(ctx, scope, Message{
		ID: newID, AgentName: scope.AgentName, SessionID: sessionID, Role: "user", Content: "A new conversation", CreatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("append recreated session: %v", err)
	}
	if claimed, err := s.ClaimSessionTitle(ctx, scope, sessionID, newID); err != nil || !claimed {
		t.Fatalf("claim recreated session = %t, err=%v", claimed, err)
	}
	if written, err := s.SetSessionTitleIfEmpty(ctx, scope, sessionID, firstID, "late old title"); err != nil || written {
		t.Fatalf("late old title write = %t, err=%v; want false", written, err)
	}
	sessions, err = s.ListSessions(ctx, scope, 10)
	if err != nil || len(sessions) != 1 || sessions[0].Title != "" {
		t.Fatalf("recreated session before title = %+v, err=%v", sessions, err)
	}
	if written, err := s.SetSessionTitleIfEmpty(ctx, scope, sessionID, newID, "New conversation title"); err != nil || !written {
		t.Fatalf("write recreated title = %t, err=%v", written, err)
	}
	sessions, err = s.ListSessions(ctx, scope, 10)
	if err != nil || len(sessions) != 1 || sessions[0].Title != "New conversation title" {
		t.Fatalf("recreated session title = %+v, err=%v", sessions, err)
	}

	if err := s.DeleteAgentData(ctx, scope, scope.AgentName); err != nil {
		t.Fatalf("delete agent data: %v", err)
	}
	agentDataID := "title-after-agent-delete-" + uuid.NewString()
	if err := s.AppendMessage(ctx, scope, Message{
		ID: agentDataID, AgentName: scope.AgentName, SessionID: sessionID, Role: "user", Content: "Fresh agent data", CreatedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("append after agent data deletion: %v", err)
	}
	if claimed, err := s.ClaimSessionTitle(ctx, scope, sessionID, agentDataID); err != nil || !claimed {
		t.Fatalf("claim after agent data deletion = %t, err=%v", claimed, err)
	}
	if written, err := s.SetSessionTitleIfEmpty(ctx, scope, sessionID, agentDataID, "Fresh agent title"); err != nil || !written {
		t.Fatalf("write after agent data deletion = %t, err=%v", written, err)
	}
}
