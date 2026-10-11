/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

func TestProjectCommittedUsesCapturedTurnWhenAnotherThreadHasNewerRun(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	server := NewWithWorkspace(nil, memory, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-demo"}
	workspaceScope := workspace.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	now := time.Now().UTC()
	createProjectCommitTestRun(t, memory, scope, "thread-a", "run-a", now)
	createProjectCommitTestRun(t, memory, scope, "thread-b", "run-b", now.Add(time.Second))

	commit := ProjectCommit{
		RepositoryRef: "demo-repo",
		CommitSHA:     "0123456789abcdef",
		CommitURL:     "https://github.example/commit/0123456",
		Branch:        "main",
		Files:         []string{"a-only.go"},
		// This snapshot was captured when A's source was sent. B's newer run
		// has no relationship to the committed bytes.
		Attributions: []workspace.SourceAttribution{{ThreadID: "thread-a", RunID: "run-a", Paths: []string{"a-only.go"}}},
	}
	server.ProjectCommitted(ctx, workspaceScope, commit)

	eventsA := projectCommittedEvents(t, memory, scope, "thread-a")
	eventsB := projectCommittedEvents(t, memory, scope, "thread-b")
	if len(eventsA) != 1 || eventsA[0].TurnID != "run-a" {
		t.Fatalf("thread A committed events = %+v, want exactly run-a", eventsA)
	}
	if len(eventsB) != 0 {
		t.Fatalf("thread B committed events = %+v, want none", eventsB)
	}
	var payload map[string]any
	if err := json.Unmarshal(eventsA[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"commitSHA":     commit.CommitSHA,
		"commitURL":     commit.CommitURL,
		"branch":        commit.Branch,
		"repositoryRef": commit.RepositoryRef,
		"files":         []any{"a-only.go"},
	}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("thread A payload = %#v, want %#v", payload, want)
	}
}

func TestProjectCommittedSplitsMixedBatchByCapturedTurn(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	server := NewWithWorkspace(nil, memory, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-demo"}
	workspaceScope := workspace.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	now := time.Now().UTC()
	createProjectCommitTestRun(t, memory, scope, "thread-a", "run-a", now)
	createProjectCommitTestRun(t, memory, scope, "thread-b", "run-b", now.Add(time.Second))

	commit := ProjectCommit{
		RepositoryRef: "demo-repo",
		CommitSHA:     "abcdef0123456789",
		Branch:        "main",
		Files:         []string{"a-only.go", "b-only.go", "manual.txt", "shared.go"},
		Attributions: []workspace.SourceAttribution{
			{ThreadID: "thread-a", RunID: "run-a", Paths: []string{"a-only.go", "shared.go"}},
			{ThreadID: "thread-b", RunID: "run-b", Paths: []string{"b-only.go", "shared.go"}},
		},
	}
	server.ProjectCommitted(ctx, workspaceScope, commit)

	eventsA := projectCommittedEvents(t, memory, scope, "thread-a")
	eventsB := projectCommittedEvents(t, memory, scope, "thread-b")
	if len(eventsA) != 1 || eventsA[0].TurnID != "run-a" {
		t.Fatalf("thread A committed events = %+v, want exactly run-a", eventsA)
	}
	if len(eventsB) != 1 || eventsB[0].TurnID != "run-b" {
		t.Fatalf("thread B committed events = %+v, want exactly run-b", eventsB)
	}
	assertCommittedEventFiles(t, eventsA[0], []string{"a-only.go"})
	assertCommittedEventFiles(t, eventsB[0], []string{"b-only.go"})
}

func createProjectCommitTestRun(t *testing.T, memory *store.MemoryStore, scope store.Scope, threadID, runID string, at time.Time) {
	t.Helper()
	run := store.AssistantRun{
		ID:              runID,
		ThreadID:        threadID,
		Mode:            store.AssistantRunModeDefault,
		ApprovalMode:    store.AssistantApprovalModeOnRequest,
		Status:          store.AssistantRunStatusCompleted,
		ClientRequestID: "same-request-id",
		UserMessageID:   "user-" + runID,
		ActiveMessageID: "assistant-" + runID,
		Revision:        1,
		CreatedAt:       at,
		UpdatedAt:       at,
	}
	user := store.Message{ID: run.UserMessageID, ActorID: "test-user", Role: "user", Content: "Build something", CreatedAt: at, UpdatedAt: at}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Content: "Done.", CreatedAt: at, UpdatedAt: at}
	if _, err := memory.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatalf("CreateAssistantRun(%s): %v", runID, err)
	}
	createAssistantThreadForHTTPTest(t, memory, scope, threadID, "test-user")
	createAssistantTurnForHTTPTest(t, memory, scope, threadID, run)
}

func projectCommittedEvents(t *testing.T, memory *store.MemoryStore, scope store.Scope, threadID string) []store.AssistantThreadEvent {
	t.Helper()
	events, err := memory.ListAssistantThreadEvents(context.Background(), scope, threadID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var committed []store.AssistantThreadEvent
	for _, event := range events {
		if event.Type == assistantThreadEventProjectCommitted {
			committed = append(committed, event)
		}
	}
	return committed
}

func assertCommittedEventFiles(t *testing.T, event store.AssistantThreadEvent, want []string) {
	t.Helper()
	var payload projectCommittedEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload.Files, want) {
		t.Fatalf("turn %s event files = %#v, want %#v", event.TurnID, payload.Files, want)
	}
}
