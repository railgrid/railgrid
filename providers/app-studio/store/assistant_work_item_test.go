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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMemoryStoreWorkItemsAreIsolatedByProjectUID(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	first := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-old"}
	second := first
	second.ProjectUID = "project-new"

	if _, err := store.CreateWorkItemAndAssistantRun(ctx, first, testWorkItem("item-old", "user-old"), testWorkItemUser("user-old"), testWorkItemAssistant("assistant-old"), testWorkItemRun("run-old", "item-old", "user-old", "assistant-old")); err != nil {
		t.Fatalf("CreateWorkItemAndAssistantRun first: %v", err)
	}
	if _, err := store.CreateWorkItemAndAssistantRun(ctx, second, testWorkItem("item-new", "user-new"), testWorkItemUser("user-new"), testWorkItemAssistant("assistant-new"), testWorkItemRun("run-new", "item-new", "user-new", "assistant-new")); err != nil {
		t.Fatalf("CreateWorkItemAndAssistantRun second: %v", err)
	}
	if _, err := store.GetAssistantWorkItem(ctx, second, "item-old"); !errors.Is(err, ErrAssistantWorkItemNotFound) {
		t.Fatalf("old work item visible through recreated Project UID: %v", err)
	}
}

func TestMemoryStoreWorkItemLifecycleAndGrantAreAtomic(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	createdAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	item := testWorkItem("item-1", "user-1")
	item.CreatedAt = createdAt
	item.UpdatedAt = createdAt
	run := testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")
	run.CreatedAt = createdAt
	run.UpdatedAt = createdAt
	created, err := store.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), run)
	if err != nil {
		t.Fatalf("CreateWorkItemAndAssistantRun: %v", err)
	}
	if created.ActiveRunID != "run-1" || created.Revision != 1 {
		t.Fatalf("created work item = %#v", created)
	}

	secondItem := testWorkItem("item-2", "user-2")
	if _, err := store.CreateWorkItemAndAssistantRun(ctx, scope, secondItem, testWorkItemUser("user-2"), testWorkItemAssistant("assistant-2"), testWorkItemRun("run-2", "item-2", "user-2", "assistant-2")); !errors.Is(err, ErrAssistantWorkItemConflict) {
		t.Fatalf("second active work item error = %v, want conflict", err)
	}

	grant := json.RawMessage(`{"capabilities":["workspace_mutate"]}`)
	approved, err := store.ApproveWorkItemPlan(ctx, scope, item.ID, run.ID, created.Revision, "grant-1", grant, createdAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ApproveWorkItemPlan: %v", err)
	}
	if approved.GrantRevision != "grant-1" || string(approved.PlanGrant) != string(grant) {
		t.Fatalf("approved work item = %#v", approved)
	}
	persistedRun, err := store.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun: %v", err)
	}
	if persistedRun.ExpectedGrantRevision != "grant-1" {
		t.Fatalf("run grant revision = %q, want grant-1", persistedRun.ExpectedGrantRevision)
	}

	persistedRun.Status = AssistantRunStatusCompleted
	persistedRun.Revision++
	persistedRun.Checkpoint = json.RawMessage(`{"must":"clear"}`)
	if err := store.TransitionWorkItemAndRun(ctx, scope, approved.ID, approved.Revision, persistedRun, AssistantWorkItemStatusCompleted, "completed", createdAt.Add(2*time.Minute)); err != nil {
		t.Fatalf("TransitionWorkItemAndRun: %v", err)
	}
	terminal, err := store.GetAssistantWorkItem(ctx, scope, item.ID)
	if err != nil {
		t.Fatalf("GetAssistantWorkItem: %v", err)
	}
	if terminal.ActiveRunID != "" || terminal.GrantRevision != "" || len(terminal.PlanGrant) != 0 || terminal.Status != AssistantWorkItemStatusCompleted {
		t.Fatalf("terminal work item = %#v, want cleared run and grant", terminal)
	}
	persistedRun, err = store.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun after transition: %v", err)
	}
	if len(persistedRun.Checkpoint) != 0 {
		t.Fatalf("terminal checkpoint = %s, want empty", persistedRun.Checkpoint)
	}
}

func TestMemoryStoreStopAndGrantRevocationAreAtomic(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := testWorkItem("item-1", "user-1")
	run := testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")
	created, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), run)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := s.ApproveWorkItemPlan(ctx, scope, item.ID, run.ID, created.Revision, "grant-1", json.RawMessage(`{"capabilities":["workspace_mutate"]}`), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	stopping, err := s.RequestAssistantRunStop(ctx, scope, item.ID, run.ID, approved.Revision, run.Revision, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if stopping.Status != AssistantRunStatusStopping || stopping.Revision != run.Revision+1 {
		t.Fatalf("stopping run = %#v", stopping)
	}
	revoked, err := s.GetAssistantWorkItem(ctx, scope, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.GrantRevision != "" || len(revoked.PlanGrant) != 0 || revoked.Revision != approved.Revision+1 {
		t.Fatalf("stopped WorkItem = %#v, want atomically revoked grant", revoked)
	}
}

func TestMemoryStoreLifecycleMessageTransitionsAreAtomic(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := testWorkItem("item-atomic", "user-atomic")
	run := testWorkItemRun("run-atomic", item.ID, item.RootMessageID, "assistant-atomic")
	created, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser(item.RootMessageID), testWorkItemAssistant(run.ActiveMessageID), run)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted.Status = AssistantRunStatusCompleted
	persisted.Revision++
	message := Message{ID: run.ActiveMessageID, WorkItemID: item.ID, Role: "assistant", Content: "done"}
	if err := s.TransitionWorkItemAndRunWithAssistantMessage(ctx, scope, item.ID, created.Revision, persisted, AssistantWorkItemStatusCompleted, "completed", message, time.Now().UTC()); err != nil {
		t.Fatalf("TransitionWorkItemAndRunWithAssistantMessage: %v", err)
	}
	page, err := s.ListMessages(ctx, scope, 10, "")
	if err != nil || len(page.Items) != 2 || page.Items[1].Content != "done" {
		t.Fatalf("terminal message was not persisted with lifecycle: page=%#v err=%v", page, err)
	}

	// A mismatched assistant message must leave the terminal state untouched.
	item = testWorkItem("item-rejected", "user-rejected")
	run = testWorkItemRun("run-rejected", item.ID, item.RootMessageID, "assistant-rejected")
	created, err = s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser(item.RootMessageID), testWorkItemAssistant(run.ActiveMessageID), run)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ = s.GetAssistantRun(ctx, scope, run.ID)
	persisted.Status = AssistantRunStatusCompleted
	persisted.Revision++
	err = s.TransitionWorkItemAndRunWithAssistantMessage(ctx, scope, item.ID, created.Revision, persisted, AssistantWorkItemStatusCompleted, "completed", Message{ID: "wrong", WorkItemID: item.ID, Role: "assistant"}, time.Now().UTC())
	if err == nil {
		t.Fatal("terminal lifecycle accepted a mismatched message")
	}
	unchanged, _ := s.GetAssistantWorkItem(ctx, scope, item.ID)
	if unchanged.Status != AssistantWorkItemStatusActive {
		t.Fatalf("failed atomic transition changed work item: %#v", unchanged)
	}
}

func TestMemoryStoreLoadRecentDiscussionMessages(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	now := time.Now().UTC()
	for _, message := range []Message{
		{ID: "discussion-1", Role: "user", Content: "hello", CreatedAt: now},
		{ID: "work-item", WorkItemID: "item-1", Role: "assistant", Content: "mutating", CreatedAt: now.Add(time.Second)},
		{ID: "discussion-2", Role: "assistant", Content: "reply", CreatedAt: now.Add(2 * time.Second)},
	} {
		if err := s.AppendMessage(ctx, scope, message); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.LoadRecentDiscussionMessages(ctx, scope, 10)
	if err != nil || len(items) != 2 || items[0].ID != "discussion-1" || items[1].ID != "discussion-2" {
		t.Fatalf("discussion history = %#v err=%v", items, err)
	}
}

func TestMemoryStoreCancellationReceiptIsSeparateFromPlanGrant(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := testWorkItem("item-cancel", "user-cancel")
	run := testWorkItemRun("run-cancel", item.ID, item.RootMessageID, "assistant-cancel")
	created, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser(item.RootMessageID), testWorkItemAssistant(run.ActiveMessageID), run)
	if err != nil {
		t.Fatal(err)
	}
	created.Status = AssistantWorkItemStatusCancelled
	created.StatusReason = "cancelled by user"
	created.ActiveRunID = ""
	created.PlanGrant = nil
	created.GrantRevision = ""
	created.CancellationReceipt = json.RawMessage(`{"kind":"cancel_receipt","clientRequestID":"cancel-1"}`)
	created.Revision++
	if err := s.CompareAndSwapAssistantWorkItem(ctx, scope, created, created.Revision-1); err != nil {
		t.Fatal(err)
	}
	persisted, err := s.GetAssistantWorkItem(ctx, scope, item.ID)
	if err != nil || len(persisted.PlanGrant) != 0 || !jsonSemanticallyEqual(persisted.CancellationReceipt, created.CancellationReceipt) {
		t.Fatalf("cancel receipt was not stored independently: %#v err=%v", persisted, err)
	}
}

func TestMemoryAndEncryptedStoreRetireWorkItemPlanContract(t *testing.T) {
	for _, tt := range []struct {
		name string
		new  func(*testing.T) Store
	}{
		{name: "memory", new: func(*testing.T) Store { return NewMemoryStore() }},
		{name: "encrypted", new: func(t *testing.T) Store {
			wrapped, err := NewEncryptedStore(NewMemoryStore(), testEncryptionKeys(t))
			if err != nil {
				t.Fatal(err)
			}
			return wrapped
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testRetireWorkItemPlanContract(t, tt.new(t))
		})
	}
}

func TestMemoryAndEncryptedStoreExecutionPlanContract(t *testing.T) {
	for _, tt := range []struct {
		name string
		new  func(*testing.T) Store
	}{
		{name: "memory", new: func(*testing.T) Store { return NewMemoryStore() }},
		{name: "encrypted", new: func(t *testing.T) Store {
			wrapped, err := NewEncryptedStore(NewMemoryStore(), testEncryptionKeys(t))
			if err != nil {
				t.Fatal(err)
			}
			return wrapped
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testWorkItemExecutionPlanContract(t, tt.new(t))
		})
	}
}

func testWorkItemExecutionPlanContract(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	item := testWorkItem("execution-item-1", "execution-user-1")
	run := testWorkItemRun("execution-run-1", item.ID, "execution-user-1", "execution-assistant-1")
	created, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("execution-user-1"), testWorkItemAssistant("execution-assistant-1"), run)
	if err != nil {
		t.Fatalf("CreateWorkItemAndAssistantRun: %v", err)
	}
	if _, err := s.SaveWorkItemExecutionPlan(ctx, scope, item.ID, run.ID, created.Revision, "execution-plan-invalid", json.RawMessage(`{"summary":`), now); err == nil {
		t.Fatal("SaveWorkItemExecutionPlan accepted invalid JSON")
	}
	firstPlan := json.RawMessage(`{"summary":"Build it","steps":[{"id":"one"}]}`)
	saved, err := s.SaveWorkItemExecutionPlan(ctx, scope, item.ID, run.ID, created.Revision, "execution-plan-1", firstPlan, now)
	if err != nil {
		t.Fatalf("SaveWorkItemExecutionPlan: %v", err)
	}
	if saved.Revision != created.Revision+1 || saved.ExecutionPlanRevision != "execution-plan-1" || !jsonSemanticallyEqual(saved.ExecutionPlan, firstPlan) {
		t.Fatalf("saved execution plan = %#v", saved)
	}
	persisted, err := s.GetAssistantWorkItem(ctx, scope, item.ID)
	if err != nil {
		t.Fatalf("GetAssistantWorkItem: %v", err)
	}
	if persisted.ExecutionPlanRevision != saved.ExecutionPlanRevision || !jsonSemanticallyEqual(persisted.ExecutionPlan, firstPlan) {
		t.Fatalf("persisted execution plan = %#v", persisted)
	}

	secondPlan := json.RawMessage(`{"summary":"Build and repair it","steps":[{"id":"one"},{"id":"repair"}]}`)
	if _, err := s.SaveWorkItemExecutionPlan(ctx, scope, item.ID, run.ID, created.Revision, "execution-plan-stale", secondPlan, now.Add(time.Minute)); !errors.Is(err, ErrAssistantWorkItemConflict) {
		t.Fatalf("stale execution plan save error = %v, want %v", err, ErrAssistantWorkItemConflict)
	}
	unchanged, err := s.GetAssistantWorkItem(ctx, scope, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != saved.Revision || unchanged.ExecutionPlanRevision != saved.ExecutionPlanRevision || !jsonSemanticallyEqual(unchanged.ExecutionPlan, firstPlan) {
		t.Fatalf("stale save mutated execution plan: %#v", unchanged)
	}

	persistedRun, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	persistedRun.Status = AssistantRunStatusPendingPermission
	persistedRun.Revision++
	if err := s.SaveAssistantRunSnapshot(ctx, scope, persistedRun, nil, persistedRun.Revision-1); err != nil {
		t.Fatalf("SaveAssistantRunSnapshot pending permission: %v", err)
	}
	if _, err := s.SaveWorkItemExecutionPlan(ctx, scope, item.ID, run.ID, saved.Revision, "execution-plan-2", secondPlan, now.Add(2*time.Minute)); !errors.Is(err, ErrAssistantRunConflict) && !errors.Is(err, ErrAssistantWorkItemConflict) {
		t.Fatalf("non-running execution plan save error = %v, want run/work item conflict", err)
	}
}

func TestEncryptedStoreExecutionPlanIsEncryptedAtRest(t *testing.T) {
	base := NewMemoryStore()
	wrapped, err := NewEncryptedStore(base, testEncryptionKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := testWorkItem("encrypted-execution-item", "encrypted-execution-user")
	run := testWorkItemRun("encrypted-execution-run", item.ID, item.RootMessageID, "encrypted-execution-assistant")
	created, err := wrapped.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser(item.RootMessageID), testWorkItemAssistant(run.ActiveMessageID), run)
	if err != nil {
		t.Fatal(err)
	}
	plan := json.RawMessage(`{"summary":"secret objective","acceptanceCriteria":["secret result"]}`)
	if _, err := wrapped.SaveWorkItemExecutionPlan(ctx, scope, item.ID, run.ID, created.Revision, "execution-plan-1", plan, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	raw, err := base.GetAssistantWorkItem(ctx, scope, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw.ExecutionPlan) == string(plan) || !bytes.Contains(raw.ExecutionPlan, []byte(`"encrypted":true`)) {
		t.Fatalf("execution plan was not encrypted at rest: %s", raw.ExecutionPlan)
	}
	decrypted, err := wrapped.GetAssistantWorkItem(ctx, scope, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonSemanticallyEqual(decrypted.ExecutionPlan, plan) {
		t.Fatalf("decrypted execution plan = %s, want %s", decrypted.ExecutionPlan, plan)
	}
}

func testRetireWorkItemPlanContract(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	item := testWorkItem("retire-item-1", "retire-user-1")
	run := testWorkItemRun("retire-run-1", item.ID, "retire-user-1", "retire-assistant-1")
	run.Checkpoint = json.RawMessage(`{"checkpoint":"preserve"}`)
	run.Audit = json.RawMessage(`{"audit":"preserve"}`)
	created, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("retire-user-1"), testWorkItemAssistant("retire-assistant-1"), run)
	if err != nil {
		t.Fatalf("CreateWorkItemAndAssistantRun: %v", err)
	}
	approved, err := s.ApproveWorkItemPlan(ctx, scope, item.ID, run.ID, created.Revision, "grant-1", json.RawMessage(`{"capabilities":["workspace_mutate"]}`), now)
	if err != nil {
		t.Fatalf("ApproveWorkItemPlan: %v", err)
	}
	beforeRun, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun before retirement: %v", err)
	}
	retired, err := s.RetireWorkItemPlan(ctx, scope, item.ID, run.ID, "actor-1", approved.Revision, "grant-1", "grant-tombstone-1", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("RetireWorkItemPlan: %v", err)
	}
	if retired.Revision != approved.Revision+1 || retired.GrantRevision != "grant-tombstone-1" || !workItemPlanGrantCleared(retired.PlanGrant) || retired.Status != AssistantWorkItemStatusActive || retired.ActiveRunID != run.ID {
		t.Fatalf("retired WorkItem = %#v", retired)
	}
	afterRun, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun after retirement: %v", err)
	}
	if afterRun.ExpectedGrantRevision != "grant-tombstone-1" || afterRun.Status != beforeRun.Status || afterRun.Revision != beforeRun.Revision || !jsonSemanticallyEqual(afterRun.Checkpoint, beforeRun.Checkpoint) || !jsonSemanticallyEqual(afterRun.Audit, beforeRun.Audit) {
		t.Fatalf("retired run = %#v; before = %#v", afterRun, beforeRun)
	}

	assertRetireConflictPreservesState(t, s, scope, item.ID, run.ID, "wrong-actor", retired.Revision, "grant-tombstone-1", "grant-tombstone-2", ErrAssistantWorkItemConflict)
	assertRetireConflictPreservesState(t, s, scope, item.ID, run.ID, "actor-1", retired.Revision-1, "grant-tombstone-1", "grant-tombstone-2", ErrAssistantWorkItemConflict)
	assertRetireConflictPreservesState(t, s, scope, item.ID, run.ID, "actor-1", retired.Revision, "stale-grant", "grant-tombstone-2", ErrAssistantWorkItemConflict)

	currentRun, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	currentRun.Status = AssistantRunStatusPendingPermission
	currentRun.UpdatedAt = now.Add(2 * time.Minute)
	if err := s.SaveAssistantRun(ctx, scope, currentRun); err != nil {
		t.Fatalf("SaveAssistantRun non-running: %v", err)
	}
	assertRetireConflictPreservesState(t, s, scope, item.ID, run.ID, "actor-1", retired.Revision, "grant-tombstone-1", "grant-tombstone-2", ErrAssistantRunConflict)
}

func assertRetireConflictPreservesState(t *testing.T, s Store, scope Scope, workItemID, runID, actor string, revision int64, expectedGrantRevision, tombstoneGrantRevision string, want error) {
	t.Helper()
	beforeItem, err := s.GetAssistantWorkItem(context.Background(), scope, workItemID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRun, err := s.GetAssistantRun(context.Background(), scope, runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetireWorkItemPlan(context.Background(), scope, workItemID, runID, actor, revision, expectedGrantRevision, tombstoneGrantRevision, time.Now().UTC()); !errors.Is(err, want) {
		t.Fatalf("RetireWorkItemPlan error = %v, want %v", err, want)
	}
	afterItem, err := s.GetAssistantWorkItem(context.Background(), scope, workItemID)
	if err != nil {
		t.Fatal(err)
	}
	afterRun, err := s.GetAssistantRun(context.Background(), scope, runID)
	if err != nil {
		t.Fatal(err)
	}
	if afterItem.Revision != beforeItem.Revision || afterItem.GrantRevision != beforeItem.GrantRevision || !rawMessagesEqual(afterItem.PlanGrant, beforeItem.PlanGrant) || afterRun.Revision != beforeRun.Revision || afterRun.Status != beforeRun.Status || afterRun.ExpectedGrantRevision != beforeRun.ExpectedGrantRevision || !rawMessagesEqual(afterRun.Checkpoint, beforeRun.Checkpoint) {
		t.Fatalf("conflict partially changed state: before item=%#v after item=%#v before run=%#v after run=%#v", beforeItem, afterItem, beforeRun, afterRun)
	}
}

func rawMessagesEqual(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	return jsonSemanticallyEqual(left, right)
}

func workItemPlanGrantCleared(grant json.RawMessage) bool {
	return len(grant) == 0 || jsonSemanticallyEqual(grant, json.RawMessage(`{}`))
}

func TestMemoryStoreResumeWorkItemAndCreateAssistantRunIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := testWorkItem("item-1", "user-1")
	first := testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")
	if _, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), first); err != nil {
		t.Fatal(err)
	}
	first.Status = AssistantRunStatusInterrupted
	first.Revision++
	if err := s.TransitionWorkItemAndRun(ctx, scope, item.ID, 1, first, AssistantWorkItemStatusSuspended, "interrupted", time.Now().UTC()); err != nil {
		t.Fatalf("suspend work item: %v", err)
	}

	nextUser := Message{ID: "user-2", Role: "user", ActorID: "actor-1", WorkItemID: item.ID, Content: "continue", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	nextAssistant := Message{ID: "assistant-2", Role: "assistant", WorkItemID: item.ID, CreatedAt: nextUser.CreatedAt, UpdatedAt: nextUser.UpdatedAt}
	nextRun := AssistantRun{ID: "run-2", WorkItemID: item.ID, Mode: AssistantRunModeContinue, Status: AssistantRunStatusRunning, ClientRequestID: "request-2", UserMessageID: nextUser.ID, ActiveMessageID: nextAssistant.ID, Revision: 1, CreatedAt: nextUser.CreatedAt, UpdatedAt: nextUser.UpdatedAt}
	resumed, err := s.ResumeWorkItemAndCreateAssistantRun(ctx, scope, item.ID, "actor-1", 2, nextUser, nextAssistant, nextRun)
	if err != nil {
		t.Fatalf("ResumeWorkItemAndCreateAssistantRun: %v", err)
	}
	if resumed.Status != AssistantWorkItemStatusActive || resumed.ActiveRunID != nextRun.ID || resumed.Revision != 3 {
		t.Fatalf("resumed work item = %#v", resumed)
	}
	if _, err := s.GetAssistantRun(ctx, scope, nextRun.ID); err != nil {
		t.Fatalf("continued run was not created: %v", err)
	}
	if _, err := s.ResumeWorkItemAndCreateAssistantRun(ctx, scope, item.ID, "other", resumed.Revision, nextUser, nextAssistant, nextRun); !errors.Is(err, ErrAssistantWorkItemConflict) {
		t.Fatalf("wrong actor error = %v, want work item conflict", err)
	}
}

func TestMemoryStoreRejectsImmutableWorkItemMembershipAndRunMode(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := testWorkItem("item-1", "user-1")
	run := testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")
	if _, err := store.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), run); err != nil {
		t.Fatalf("CreateWorkItemAndAssistantRun: %v", err)
	}
	if err := store.AppendMessage(ctx, scope, Message{ID: "user-1", Role: "user", ActorID: "actor-1", WorkItemID: "item-2"}); !errors.Is(err, ErrAssistantWorkItemConflict) {
		t.Fatalf("relink message error = %v, want work item conflict", err)
	}
	run.Mode = AssistantRunModeDiscussion
	run.Revision++
	if err := store.SaveAssistantRunSnapshot(ctx, scope, run, nil, 1); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("change run mode error = %v, want run conflict", err)
	}
}

func TestMemoryStoreGenericMessageUpdatesPreserveActorAndWorkItem(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := testWorkItem("item-1", "user-1")
	if _, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")); !errors.Is(err, ErrAssistantWorkItemConflict) {
		t.Fatalf("duplicate WorkItem creation = %v, want conflict for API-level idempotency recovery", err)
	}
	if err := s.AppendMessage(ctx, scope, Message{ID: "user-1", Role: "user", Content: "changed", ActorID: "attacker", WorkItemID: item.ID}); !errors.Is(err, ErrAssistantWorkItemConflict) {
		t.Fatalf("generic actor rewrite = %v, want conflict", err)
	}
}

func TestMemoryStoreWorkItemAttachesMatchingUnassignedRootMessageOnce(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	root := testWorkItemUser("user-1")
	root.WorkItemID = ""
	if err := s.AppendMessage(ctx, scope, root); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	item := testWorkItem("item-1", "user-1")
	if _, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")); err != nil {
		t.Fatalf("CreateWorkItemAndAssistantRun attach root: %v", err)
	}
	messages, err := s.LoadMessagesForWorkItem(ctx, scope, item.ID, 10)
	if err != nil || len(messages) != 2 || messages[0].ID != "user-1" {
		t.Fatalf("attached messages = %#v, err=%v", messages, err)
	}
	secondUser := testWorkItemUser("user-1")
	secondUser.WorkItemID = "item-2"
	if _, err := s.CreateWorkItemAndAssistantRun(ctx, scope, testWorkItem("item-2", "user-1"), secondUser, testWorkItemAssistant("assistant-2"), testWorkItemRun("run-2", "item-2", "user-1", "assistant-2")); !errors.Is(err, ErrAssistantWorkItemConflict) {
		t.Fatalf("second root attachment error = %v, want conflict", err)
	}
	otherStore := NewMemoryStore()
	if err := otherStore.AppendMessage(ctx, scope, root); err != nil {
		t.Fatalf("AppendMessage other store: %v", err)
	}
	otherActor := testWorkItemUser("user-1")
	otherActor.ActorID = "actor-2"
	otherActor.WorkItemID = "item-3"
	if _, err := otherStore.CreateWorkItemAndAssistantRun(ctx, scope, testWorkItem("item-3", "user-1"), otherActor, testWorkItemAssistant("assistant-3"), testWorkItemRun("run-3", "item-3", "user-1", "assistant-3")); err == nil {
		t.Fatal("cross-actor root attachment unexpectedly succeeded")
	}
}

func TestMemoryStoreTerminalTransitionRequiresMatchingLifecycle(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := testWorkItem("item-1", "user-1")
	run := testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")
	created, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), run)
	if err != nil {
		t.Fatalf("CreateWorkItemAndAssistantRun: %v", err)
	}
	run.Status = AssistantRunStatusCompleted
	run.Revision++
	if err := s.TransitionWorkItemAndRun(ctx, scope, item.ID, created.Revision, run, AssistantWorkItemStatusSuspended, "wrong target", time.Now()); err == nil {
		t.Fatal("TransitionWorkItemAndRun accepted completed run as suspended work item")
	}
}

func TestMemoryStoreWorkItemCreationRejectsPreinstalledGrant(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := testWorkItem("item-1", "user-1")
	item.PlanGrant = json.RawMessage(`{"capabilities":["workspace_mutate"]}`)
	item.GrantRevision = "grant-1"
	run := testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")
	run.ExpectedGrantRevision = "grant-1"
	if _, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), run); err == nil {
		t.Fatal("CreateWorkItemAndAssistantRun accepted a preinstalled grant")
	}
}

func TestMemoryStoreApproveWorkItemPlanRequiresRunningRun(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := testWorkItem("item-1", "user-1")
	run := testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")
	created, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), run)
	if err != nil {
		t.Fatalf("CreateWorkItemAndAssistantRun: %v", err)
	}
	run.Status = AssistantRunStatusPendingPermission
	run.Revision++
	if err := s.SaveAssistantRunSnapshot(ctx, scope, run, nil, 1); err != nil {
		t.Fatalf("SaveAssistantRunSnapshot: %v", err)
	}
	if _, err := s.ApproveWorkItemPlan(ctx, scope, item.ID, run.ID, created.Revision, "grant-1", json.RawMessage(`{"capabilities":["workspace_mutate"]}`), time.Now()); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("ApproveWorkItemPlan pending run error = %v, want run conflict", err)
	}
}

func TestMemoryStoreRetentionDoesNotOrphanWorkItemState(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	item := testWorkItem("item-1", "user-1")
	run := testWorkItemRun("run-1", item.ID, "user-1", "assistant-1")
	item.CreatedAt, item.UpdatedAt = now, now
	run.CreatedAt, run.UpdatedAt = now, now
	if _, err := s.CreateWorkItemAndAssistantRun(ctx, scope, item, testWorkItemUser("user-1"), testWorkItemAssistant("assistant-1"), run); err != nil {
		t.Fatalf("CreateWorkItemAndAssistantRun: %v", err)
	}
	if _, err := s.DeleteMessagesOlderThan(ctx, now.Add(time.Hour)); err != nil {
		t.Fatalf("DeleteMessagesOlderThan: %v", err)
	}
	if _, err := s.GetAssistantWorkItem(ctx, scope, item.ID); err != nil {
		t.Fatalf("active WorkItem was deleted or orphaned: %v", err)
	}
	if _, err := s.GetAssistantRun(ctx, scope, run.ID); err != nil {
		t.Fatalf("active WorkItem run was deleted: %v", err)
	}
	messages, err := s.LoadMessagesForWorkItem(ctx, scope, item.ID, 10)
	if err != nil || len(messages) != 2 {
		t.Fatalf("active WorkItem messages = %#v, err=%v", messages, err)
	}
}

func TestEncryptedWorkItemGrantBindsProjectUIDAndWorkItemID(t *testing.T) {
	wrapped, err := NewEncryptedStore(NewMemoryStore(), testEncryptionKeys(t))
	if err != nil {
		t.Fatalf("NewEncryptedStore: %v", err)
	}
	encrypted := wrapped.(*encryptedStore)
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
	item := AssistantWorkItem{ID: "item-1"}
	ciphertext, err := encrypted.encryptAssistantWorkItemGrant(scope, item, json.RawMessage(`{"capabilities":["workspace_mutate"]}`))
	if err != nil {
		t.Fatalf("encryptAssistantWorkItemGrant: %v", err)
	}
	if string(ciphertext) == `{"capabilities":["workspace_mutate"]}` {
		t.Fatal("grant remained plaintext")
	}
	changedItem := AssistantWorkItem{ID: "item-2", PlanGrant: ciphertext}
	if err := encrypted.decryptAssistantWorkItemGrant(scope, &changedItem); err == nil {
		t.Fatal("grant decrypted after WorkItem ID substitution")
	}
	changedScope := scope
	changedScope.ProjectUID = "project-2"
	changedProject := AssistantWorkItem{ID: item.ID, PlanGrant: ciphertext}
	if err := encrypted.decryptAssistantWorkItemGrant(changedScope, &changedProject); err == nil {
		t.Fatal("grant decrypted after Project UID substitution")
	}
}

func testWorkItem(id, rootMessageID string) AssistantWorkItem {
	return AssistantWorkItem{ID: id, RootMessageID: rootMessageID, CreatedBy: "actor-1", Status: AssistantWorkItemStatusActive}
}

func testWorkItemUser(id string) Message {
	return Message{ID: id, Role: "user", Content: "Build it", ActorID: "actor-1", WorkItemID: strings.Replace(id, "user", "item", 1)}
}

func testWorkItemAssistant(id string) Message {
	return Message{ID: id, Role: "assistant", Content: "", WorkItemID: strings.Replace(id, "assistant", "item", 1)}
}

func testWorkItemRun(id, workItemID, userID, assistantID string) AssistantRun {
	return AssistantRun{ID: id, WorkItemID: workItemID, Mode: AssistantRunModeNew, Status: AssistantRunStatusRunning, ClientRequestID: id, UserMessageID: userID, ActiveMessageID: assistantID, Revision: 1}
}
