/*
Copyright 2026 The Faros Authors.

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

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectCommittedAppendsEventToLatestRunTurn(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	server := NewWithWorkspace(nil, memory, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-demo"}
	workspaceScope := workspace.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	commit := ProjectCommit{RepositoryRef: "demo-repo", CommitSHA: "0123456789abcdef", CommitURL: "https://github.example/commit/0123456", Branch: "main", Files: []string{"app.txt"}}

	// No assistant run yet: skipped silently.
	server.ProjectCommitted(ctx, workspaceScope, commit)

	now := time.Now().UTC()
	run := store.AssistantRun{
		ID:              "run-1",
		Mode:            store.AssistantRunModeDefault,
		ApprovalMode:    store.AssistantApprovalModeOnRequest,
		Status:          store.AssistantRunStatusCompleted,
		ClientRequestID: "request-1",
		UserMessageID:   "user-1",
		ActiveMessageID: "assistant-1",
		Revision:        1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	user := store.Message{ID: run.UserMessageID, ActorID: "test-user", Role: "user", Content: "Build a timer", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Content: "Done.", CreatedAt: now, UpdatedAt: now}
	if _, err := memory.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	createAssistantThreadForHTTPTest(t, memory, scope, "thread-1", "test-user")
	createAssistantTurnForHTTPTest(t, memory, scope, "thread-1", run)

	server.ProjectCommitted(ctx, workspaceScope, commit)

	events, err := memory.ListAssistantThreadEvents(ctx, scope, "thread-1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var committed []store.AssistantThreadEvent
	for _, event := range events {
		if event.Type == assistantThreadEventProjectCommitted {
			committed = append(committed, event)
		}
	}
	if len(committed) != 1 {
		t.Fatalf("project.committed events = %+v, want exactly one", committed)
	}
	if committed[0].TurnID != run.ID {
		t.Fatalf("event turn = %q, want %q", committed[0].TurnID, run.ID)
	}
	var payload map[string]any
	if err := json.Unmarshal(committed[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"commitSHA":     commit.CommitSHA,
		"commitURL":     commit.CommitURL,
		"branch":        commit.Branch,
		"repositoryRef": commit.RepositoryRef,
		"files":         []any{"app.txt"},
	}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
}
