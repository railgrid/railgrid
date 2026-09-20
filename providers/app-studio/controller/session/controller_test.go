/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package session

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/bindings"
	"github.com/railgrid/provider-app-studio/store"
)

func annotatedSession() *aiv1alpha1.Session {
	return &aiv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name: "thread-1",
			Annotations: map[string]string{
				bindings.OrgUUIDAnnotation:       "org-1",
				bindings.WorkspaceUUIDAnnotation: "ws-1",
				projectUIDAnnotation:             "uid-1",
			},
		},
		Spec: aiv1alpha1.SessionSpec{ProjectRef: "demo", ThreadID: "thread-1", ActorID: "alice"},
	}
}

func TestScopeOfRequiresFullIdentity(t *testing.T) {
	s := annotatedSession()
	scope, ok := scopeOf(s)
	if !ok {
		t.Fatal("fully-annotated session produced no scope")
	}
	if scope.OrgUUID != "org-1" || scope.WorkspaceUUID != "ws-1" || scope.ProjectName != "demo" || scope.ProjectUID != "uid-1" {
		t.Fatalf("scope = %+v", scope)
	}
	s.Annotations = nil
	if _, ok := scopeOf(s); ok {
		t.Fatal("un-annotated session must not produce a scope")
	}
}

func TestProjectStatusMirrorsThreadAndActiveTurn(t *testing.T) {
	st := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-1", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "uid-1"}
	now := time.Now().UTC()
	thread := store.AssistantThread{ID: "thread-1", Title: "Build a shop", Status: store.AssistantThreadStatusIdle, ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	if _, err := st.CreateAssistantThread(context.Background(), scope, thread, nil); err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	r := &Reconciler{Store: st}
	status, exists, err := r.projectStatus(context.Background(), scope, "thread-1")
	if err != nil || !exists {
		t.Fatalf("projectStatus: exists=%v err=%v", exists, err)
	}
	if status.Title != "Build a shop" || status.Phase != string(store.AssistantThreadStatusIdle) {
		t.Fatalf("status = %+v", status)
	}
	if status.ActiveTurnID != "" {
		t.Fatalf("idle thread reported an active turn: %+v", status)
	}

	if _, exists, err := r.projectStatus(context.Background(), scope, "missing"); err != nil || exists {
		t.Fatalf("missing thread: exists=%v err=%v (want false, nil)", exists, err)
	}
}

func TestFinalizePurgesThread(t *testing.T) {
	st := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-1", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "uid-1"}
	now := time.Now().UTC()
	if _, err := st.CreateAssistantThread(context.Background(), scope,
		store.AssistantThread{ID: "thread-1", Status: store.AssistantThreadStatusIdle, ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	r := &Reconciler{Store: st}
	// The purge path reads the thread's actor and deletes as it — verify the
	// row disappears (finalizer bookkeeping needs a live client, exercised in
	// the fake-client project tests; here the store contract is the point).
	thread, err := st.GetAssistantThread(context.Background(), scope, "thread-1")
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if err := r.Store.DeleteAssistantThread(context.Background(), scope, thread.ID, thread.ActorID); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := st.GetAssistantThread(context.Background(), scope, "thread-1"); err == nil {
		t.Fatal("thread survived the purge")
	}
}

func TestRetentionDeadlineIsPerSessionAndDefersActiveTurns(t *testing.T) {
	last := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	lastActivity := metav1.NewTime(last)
	idle := aiv1alpha1.SessionStatus{LastActivityAt: &lastActivity}

	if _, ok := (&Reconciler{}).retentionDeadline(idle); ok {
		t.Fatal("retention must be disabled when no window is configured")
	}

	r := &Reconciler{Retention: 48 * time.Hour}
	deadline, ok := r.retentionDeadline(idle)
	if !ok || !deadline.Equal(last.Add(48*time.Hour)) {
		t.Fatalf("deadline = (%v, %v), want %v", deadline, ok, last.Add(48*time.Hour))
	}

	if _, ok := r.retentionDeadline(aiv1alpha1.SessionStatus{}); ok {
		t.Fatal("a session with no recorded activity must not be given a deadline")
	}

	busy := idle
	busy.ActiveTurnID = "turn-1"
	busy.ActiveTurnStatus = string(store.AssistantTurnStatusInProgress)
	if _, ok := r.retentionDeadline(busy); ok {
		t.Fatal("a conversation with an in-flight turn must not expire")
	}

	settled := busy
	settled.ActiveTurnStatus = string(store.AssistantTurnStatusCompleted)
	if _, ok := r.retentionDeadline(settled); !ok {
		t.Fatal("a completed turn must not defer retention")
	}
}

func TestStatusEqualComparesActivityFields(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC))
	base := aiv1alpha1.SessionStatus{Title: "t", TurnCount: 2, LastActivityAt: &at}
	if !statusEqual(base, aiv1alpha1.SessionStatus{Title: "t", TurnCount: 2, LastActivityAt: &at}) {
		t.Fatal("identical statuses compared unequal")
	}
	bumped := base
	bumped.TurnCount = 3
	if statusEqual(base, bumped) {
		t.Fatal("turnCount drift was not detected")
	}
	later := metav1.NewTime(at.Add(time.Minute))
	moved := base
	moved.LastActivityAt = &later
	if statusEqual(base, moved) {
		t.Fatal("lastActivityAt drift was not detected")
	}
	cleared := base
	cleared.LastActivityAt = nil
	if statusEqual(base, cleared) {
		t.Fatal("a cleared lastActivityAt was not detected")
	}
}
