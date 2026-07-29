// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestApprovalModeSchemaMigrationIsAdditive(t *testing.T) {
	statements := approvalModeSchemaStatements()
	if len(statements) != 2 {
		t.Fatalf("approval mode schema statements = %#v", statements)
	}
	if !strings.Contains(statements[0], "app_studio_assistant_approval_preferences") {
		t.Fatalf("preference table statement = %q", statements[0])
	}
	if !strings.Contains(statements[1], "ADD COLUMN IF NOT EXISTS approval_mode") {
		t.Fatalf("assistant run migration statement = %q", statements[1])
	}
}

func TestMemoryStoreAssistantApprovalPreferenceIsActorAndProjectScoped(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-a"}

	defaultPreference, err := s.GetAssistantApprovalPreference(ctx, scope, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if defaultPreference.Mode != AssistantApprovalModeAlwaysAsk {
		t.Fatalf("default mode = %q, want %q", defaultPreference.Mode, AssistantApprovalModeAlwaysAsk)
	}

	saved, err := s.SetAssistantApprovalPreference(ctx, scope, AssistantApprovalPreference{
		ActorID: "alice",
		Mode:    AssistantApprovalModeAutoApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Mode != AssistantApprovalModeAutoApprove || saved.UpdatedAt.IsZero() {
		t.Fatalf("saved preference = %#v", saved)
	}

	for name, candidate := range map[string]struct {
		scope Scope
		actor string
	}{
		"other actor":         {scope: scope, actor: "bob"},
		"reused project name": {scope: Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-b"}, actor: "alice"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := s.GetAssistantApprovalPreference(ctx, candidate.scope, candidate.actor)
			if err != nil {
				t.Fatal(err)
			}
			if got.Mode != AssistantApprovalModeAlwaysAsk {
				t.Fatalf("mode = %q, want isolated default", got.Mode)
			}
		})
	}
}

func TestMemoryStoreAssistantRunApprovalModeIsDurableAndImmutable(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-a"}
	now := time.Now().UTC()
	user := Message{ID: "user-1", ActorID: "alice", Role: "user", Content: "build", CreatedAt: now, UpdatedAt: now}
	assistant := Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	run := AssistantRun{
		ID:              "run-1",
		Mode:            AssistantRunModeAdaptive,
		ApprovalMode:    AssistantApprovalModeAutoApprove,
		Status:          AssistantRunStatusRunning,
		ClientRequestID: "request-1",
		UserMessageID:   user.ID,
		ActiveMessageID: assistant.ID,
		Revision:        1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	created, err := s.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	if created.ApprovalMode != AssistantApprovalModeAutoApprove {
		t.Fatalf("created approval mode = %q", created.ApprovalMode)
	}

	created.Status = AssistantRunStatusCompleted
	created.Revision = 2
	created.UpdatedAt = now.Add(time.Second)
	if err := s.SaveAssistantRunSnapshot(ctx, scope, created, nil, 1); err != nil {
		t.Fatal(err)
	}
	persisted, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ApprovalMode != AssistantApprovalModeAutoApprove {
		t.Fatalf("persisted approval mode = %q", persisted.ApprovalMode)
	}

	changed := persisted
	changed.ApprovalMode = AssistantApprovalModeAlwaysAsk
	changed.Revision = 3
	if err := s.SaveAssistantRunSnapshot(ctx, scope, changed, nil, 2); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("changed approval mode error = %v, want conflict", err)
	}
}

func TestMemoryStoreAssistantRunApprovalModeDefaultsAndValidates(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-a"}
	now := time.Now().UTC()
	user := Message{ID: "user-1", ActorID: "alice", Role: "user", Content: "ask", CreatedAt: now, UpdatedAt: now}
	assistant := Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	run := AssistantRun{
		ID:              "run-1",
		Mode:            AssistantRunModeDiscussion,
		Status:          AssistantRunStatusCompleted,
		ClientRequestID: "request-1",
		UserMessageID:   user.ID,
		ActiveMessageID: assistant.ID,
		Revision:        1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	created, err := s.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	if created.ApprovalMode != AssistantApprovalModeAlwaysAsk {
		t.Fatalf("default approval mode = %q", created.ApprovalMode)
	}

	run.ID = "run-2"
	run.ClientRequestID = "request-2"
	run.ApprovalMode = AssistantApprovalMode("unsafe")
	if _, err := s.CreateAssistantRun(ctx, scope, user, assistant, run); !errors.Is(err, ErrAssistantApprovalModeInvalid) {
		t.Fatalf("invalid approval mode error = %v", err)
	}
}
