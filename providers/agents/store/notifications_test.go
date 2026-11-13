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
	"testing"
	"time"

	"github.com/google/uuid"
)

func testSessionNotifications(t *testing.T, scope Scope, access SessionNotificationStore, messages Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := access.ClaimSessionOwner(ctx, scope, "uid-a", "new-session", "owner", now); err != nil {
		t.Fatal(err)
	}
	if err := access.ClaimSessionOwner(ctx, scope, "uid-a", "new-session", "owner", now); err != nil {
		t.Fatalf("idempotent owner claim: %v", err)
	}
	if err := access.ClaimSessionOwner(ctx, scope, "uid-a", "new-session", "other", now); !errors.Is(err, ErrSessionOwnerConflict) {
		t.Fatalf("second owner claim error = %v", err)
	}
	if err := access.GrantSessionAccess(ctx, scope, "uid-a", "new-session", "owner", "service-account", now); err != nil {
		t.Fatal(err)
	}
	if ok, err := access.HasSessionAccess(ctx, scope, "uid-a", "new-session", "service-account"); err != nil || !ok {
		t.Fatalf("granted access = %v, %v", ok, err)
	}
	if _, _, err := access.CreateSessionNotification(ctx, scope, "uid-a", "new-session", "stranger", "key-1", "hello", now); !errors.Is(err, ErrSessionAccessDenied) {
		t.Fatalf("unauthorized delivery error = %v", err)
	}
	if _, _, err := access.CreateSessionNotification(ctx, scope, "uid-a", "new-session", "service-account", "wrong-owner", "must not arrive", now, "different-owner"); !errors.Is(err, ErrSessionAccessDenied) {
		t.Fatalf("confused-deputy delivery error = %v", err)
	}
	if items, err := access.ListSessionNotifications(ctx, scope, "uid-a", "new-session", "owner", 10); err != nil || len(items) != 0 {
		t.Fatalf("rejected cross-owner delivery inserted data: %+v, %v", items, err)
	}
	first, reused, err := access.CreateSessionNotification(ctx, scope, "uid-a", "new-session", "service-account", "key-1", "hello", now)
	if err != nil || reused {
		t.Fatalf("first notification = %+v, reused=%v, err=%v", first, reused, err)
	}
	second, reused, err := access.CreateSessionNotification(ctx, scope, "uid-a", "new-session", "service-account", "key-1", "hello", now.Add(time.Second))
	if err != nil || !reused || first.ID != second.ID {
		t.Fatalf("retry = %+v, reused=%v, err=%v", second, reused, err)
	}
	if _, _, err := access.CreateSessionNotification(ctx, scope, "uid-a", "new-session", "service-account", "key-1", "changed", now); !errors.Is(err, ErrNotificationConflict) {
		t.Fatalf("key conflict error = %v", err)
	}
	if _, err := access.ListSessionNotifications(ctx, scope, "uid-a", "new-session", "stranger", 10); !errors.Is(err, ErrSessionAccessDenied) {
		t.Fatalf("unauthorized read error = %v", err)
	}
	items, err := access.ListSessionNotifications(ctx, scope, "uid-a", "new-session", "service-account", 10)
	if err != nil || len(items) != 1 || items[0].Payload != "hello" {
		t.Fatalf("feed = %+v, err=%v", items, err)
	}
	if page, err := messages.ListMessages(ctx, scope, "new-session", 10, ""); err != nil || len(page.Items) != 0 {
		t.Fatalf("notification leaked into transcript: %+v, %v", page.Items, err)
	}
	if ok, err := access.HasSessionAccess(ctx, scope, "uid-b", "new-session", "service-account"); err != nil || ok {
		t.Fatalf("recreated Agent inherited session grant: %v, %v", ok, err)
	}
	if err := access.RevokeSessionAccess(ctx, scope, "uid-a", "new-session", "owner", "service-account"); err != nil {
		t.Fatal(err)
	}
	if ok, err := access.HasSessionAccess(ctx, scope, "uid-a", "new-session", "service-account"); err != nil || ok {
		t.Fatalf("revoked access = %v, %v", ok, err)
	}
	if err := messages.DeleteSession(ctx, scope, "new-session"); err != nil {
		t.Fatal(err)
	}
	if ok, err := access.HasSessionAccess(ctx, scope, "uid-a", "new-session", "owner"); err != nil || ok {
		t.Fatalf("deleted session retained owner access: %v, %v", ok, err)
	}
	if err := access.ClaimSessionOwner(ctx, scope, "uid-delete", "deleted-agent", "owner", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.CreateSessionNotification(ctx, scope, "uid-delete", "deleted-agent", "owner", "cleanup", "remove me", now); err != nil {
		t.Fatal(err)
	}
	if err := messages.DeleteAgentData(ctx, scope, scope.AgentName); err != nil {
		t.Fatal(err)
	}
	if ok, err := access.HasSessionAccess(ctx, scope, "uid-delete", "deleted-agent", "owner"); err != nil || ok {
		t.Fatalf("deleted Agent retained session access: %v, %v", ok, err)
	}
}

func testOwnedChatAdmissionOwnership(t *testing.T, scope Scope, access SessionNotificationStore, admission AgentChatOwnerAdmissionStore, transcript Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	legacy := "legacy-session"
	if err := transcript.AppendMessage(ctx, scope, Message{ID: uuid.NewString(), AgentName: scope.AgentName, SessionID: legacy, Role: "user", Content: "legacy", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	create := func(sessionID, runID, owner string) {
		run := Run{ID: runID, AgentName: scope.AgentName, SessionID: sessionID, Phase: RunPhaseRunning, CreatedAt: now, UpdatedAt: now, StartedAt: &now}
		run.Limits = &RunLimits{MaxOutputTokens: 27}
		run.UsageCompleteness, run.CostBasis = "measured", "provider"
		msg := Message{ID: uuid.NewString(), AgentName: scope.AgentName, SessionID: sessionID, RunID: runID, Role: "user", Content: "new turn", CreatedAt: now}
		if err := admission.CreateOwnedAgentChatRun(ctx, scope, "uid", msg, run, nil, "", owner, now); err != nil {
			t.Fatalf("chat admission for %s: %v", sessionID, err)
		}
		stored, err := transcript.GetRun(ctx, scope, runID)
		if err != nil || stored.Limits == nil || stored.Limits.MaxOutputTokens != 27 || stored.UsageCompleteness != run.UsageCompleteness || stored.CostBasis != run.CostBasis {
			t.Fatalf("chat Run lost limits or usage metadata: %+v, %v", stored, err)
		}
	}
	create(legacy, uuid.NewString(), "legacy-caller")
	if ok, err := access.HasSessionAccess(ctx, scope, "uid", legacy, "legacy-caller"); err != nil || ok {
		t.Fatalf("legacy chat unexpectedly acquired an owner: %v, %v", ok, err)
	}
	create("fresh", uuid.NewString(), "first-caller")
	create("fresh", uuid.NewString(), "second-caller")
	if ok, err := access.HasSessionAccess(ctx, scope, "uid", "fresh", "first-caller"); err != nil || !ok {
		t.Fatalf("first caller lost ownership: %v, %v", ok, err)
	}
	if ok, err := access.HasSessionAccess(ctx, scope, "uid", "fresh", "second-caller"); err != nil || ok {
		t.Fatalf("shared chat changed owner: %v, %v", ok, err)
	}
}

func TestMemorySessionNotifications(t *testing.T) {
	mem := NewMemoryStore()
	testSessionNotifications(t, testScope(), mem, mem)
	testOwnedChatAdmissionOwnership(t, testScope(), mem, mem, mem)
	legacy := testScope()
	if err := mem.AppendMessage(context.Background(), legacy, Message{ID: "legacy-row", SessionID: "legacy", Role: "user", Content: "old", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := mem.ClaimSessionOwner(context.Background(), legacy, "uid-a", "legacy", "claimant", time.Now()); !errors.Is(err, ErrSessionOwnerConflict) {
		t.Fatalf("legacy owner claim = %v", err)
	}
}

func TestPostgresSessionNotifications(t *testing.T) {
	ps := openTestPostgres(t)
	scope := pgScope(t, ps)
	testSessionNotifications(t, scope, ps, ps)
	testOwnedChatAdmissionOwnership(t, pgScope(t, ps), ps, ps, ps)
	legacy := pgScope(t, ps)
	if err := ps.AppendMessage(context.Background(), legacy, Message{ID: "legacy-" + scope.WorkspaceUUID, SessionID: "legacy", Role: "user", Content: "old", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := ps.ClaimSessionOwner(context.Background(), legacy, "uid-a", "legacy", "claimant", time.Now()); !errors.Is(err, ErrSessionOwnerConflict) {
		t.Fatalf("legacy owner claim = %v", err)
	}
}

func testSessionNotificationScopeMigration(t *testing.T, scope Scope, access SessionNotificationStore, s Store, clusterID string, ref TenantRef) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := access.ClaimSessionOwner(ctx, scope, "uid-migrate", "session", "owner", now); err != nil {
		t.Fatal(err)
	}
	if err := access.GrantSessionAccess(ctx, scope, "uid-migrate", "session", "owner", "worker", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.CreateSessionNotification(ctx, scope, "uid-migrate", "session", "worker", "key", "during fallback", now); err != nil {
		t.Fatal(err)
	}
	canonical := Scope{OrgUUID: ref.OrgUUID, WorkspaceUUID: ref.WorkspaceUUID, AgentName: scope.AgentName}
	if err := access.ClaimSessionOwner(ctx, canonical, "uid-migrate", "conflict", "canonical-owner", now); err != nil {
		t.Fatal(err)
	}
	if err := access.ClaimSessionOwner(ctx, scope, "uid-migrate", "conflict", "legacy-owner", now); err != nil {
		t.Fatal(err)
	}
	if err := access.GrantSessionAccess(ctx, scope, "uid-migrate", "conflict", "legacy-owner", "legacy-worker", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := access.CreateSessionNotification(ctx, scope, "uid-migrate", "conflict", "legacy-worker", "legacy-key", "must not leak", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTenantRef(ctx, clusterID, ref); err != nil {
		t.Fatal(err)
	}
	if ok, err := access.HasSessionAccess(ctx, canonical, "uid-migrate", "session", "worker"); err != nil || !ok {
		t.Fatalf("migrated exact-session access = %v, %v", ok, err)
	}
	items, err := access.ListSessionNotifications(ctx, canonical, "uid-migrate", "session", "worker", 10)
	if err != nil || len(items) != 1 || items[0].Payload != "during fallback" {
		t.Fatalf("migrated notification feed = %+v, %v", items, err)
	}
	if ok, err := access.HasSessionAccess(ctx, canonical, "uid-migrate", "conflict", "legacy-owner"); err != nil || ok {
		t.Fatalf("superseded legacy owner survived migration: %v, %v", ok, err)
	}
	if ok, err := access.HasSessionAccess(ctx, canonical, "uid-migrate", "conflict", "legacy-worker"); err != nil || ok {
		t.Fatalf("superseded legacy grant survived migration: %v, %v", ok, err)
	}
	items, err = access.ListSessionNotifications(ctx, canonical, "uid-migrate", "conflict", "canonical-owner", 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("superseded owner's notifications leaked: %+v, %v", items, err)
	}
}

func TestMemorySessionNotificationScopeMigration(t *testing.T) {
	mem := NewMemoryStore()
	clusterID := "notification-cluster"
	ref := TenantRef{OrgUUID: "org-migrated", WorkspaceUUID: "workspace-migrated", UpdatedAt: time.Now().UTC()}
	scope := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: clusterID, ClusterID: clusterID, AgentName: "agent"}
	testSessionNotificationScopeMigration(t, scope, mem, mem, clusterID, ref)
}

func TestPostgresSessionNotificationScopeMigration(t *testing.T) {
	ps := openTestPostgres(t)
	base := pgScope(t, ps)
	clusterID := "notification-" + base.WorkspaceUUID
	ref := TenantRef{OrgUUID: base.OrgUUID, WorkspaceUUID: base.WorkspaceUUID, UpdatedAt: time.Now().UTC()}
	scope := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: clusterID, ClusterID: clusterID, AgentName: base.AgentName}
	testSessionNotificationScopeMigration(t, scope, ps, ps, clusterID, ref)
}
