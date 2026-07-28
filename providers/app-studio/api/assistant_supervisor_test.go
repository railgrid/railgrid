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

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/gorilla/mux"
)

func TestProjectAssistantSupervisorOwnsExecutionAfterStarterCancellation(t *testing.T) {
	supervisor := newProjectAssistantSupervisor(context.Background(), store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: run.CreatedAt, UpdatedAt: run.CreatedAt}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: run.CreatedAt, UpdatedAt: run.CreatedAt}
	created, err := supervisor.store.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}

	starter, cancelStarter := context.WithCancel(context.Background())
	started := make(chan struct{})
	finished := make(chan struct{})
	if err := supervisor.Start(starter, scope, created, assistant, func(ctx context.Context, snapshots *projectAssistantSnapshotAccumulator) {
		close(started)
		<-ctx.Done()
		close(finished)
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	cancelStarter()

	select {
	case <-finished:
		t.Fatal("starter cancellation canceled server-owned execution")
	case <-time.After(25 * time.Millisecond):
	}
	if !supervisor.Abort(scope, created.ID) {
		t.Fatal("Abort did not find active worker")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Abort did not cancel active worker")
	}
}

func TestProjectAssistantSupervisorShutdownLogsOneInterruptedTerminalTransition(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	var interrupted int
	supervisor.lifecycleLog = func(event string, gotScope store.Scope, gotRun store.AssistantRun) {
		if event == "interrupted" {
			interrupted++
			if gotScope != scope || gotRun.Status != store.AssistantRunStatusInterrupted || gotRun.ID != run.ID {
				t.Fatalf("interrupted lifecycle fields = %#v %#v", gotScope, gotRun)
			}
		}
	}
	entered := make(chan struct{})
	if err := supervisor.Start(context.Background(), scope, run, assistant, func(ctx context.Context, _ *projectAssistantSnapshotAccumulator) {
		close(entered)
		<-ctx.Done()
	}); err != nil {
		t.Fatal(err)
	}
	<-entered
	supervisor.Shutdown(context.Background())
	if interrupted != 1 {
		t.Fatalf("interrupted lifecycle events = %d, want one", interrupted)
	}
}

func TestProjectAssistantSupervisorReservationProtectsFreshDurableRunUntilAttach(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	server := NewWithWorkspace(nil, memoryStore, nil, "", false)
	server.assistantSupervisor = supervisor
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	release, err := supervisor.Reserve(scope)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	defer release()
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	if err := server.reconcileOrphanedProjectAssistantRun(context.Background(), scope); err != nil {
		t.Fatalf("reconcile while reserved: %v", err)
	}
	persisted, err := memoryStore.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != store.AssistantRunStatusRunning {
		t.Fatalf("reserved fresh run was orphaned as %q", persisted.Status)
	}
	if _, err := supervisor.Attach(scope, persisted, assistant); err != nil {
		t.Fatalf("Attach: %v", err)
	}
}

func TestProjectAssistantApprovedPlanGrantDoesNotShadowOrphanedConversationRun(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	stale := store.AssistantRun{ID: "run-stale", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-stale", UserMessageID: "user-stale", ActiveMessageID: "assistant-stale", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := messages.CreateAssistantRun(context.Background(), scope,
		store.Message{ID: stale.UserMessageID, Role: "user", Content: "stale", CreatedAt: now, UpdatedAt: now},
		store.Message{ID: stale.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}, stale,
	); err != nil {
		t.Fatalf("CreateAssistantRun stale: %v", err)
	}
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), scope, &projectAssistantApprovedPlan{
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		TargetPaths:  []string{"src/"},
	}); err != nil {
		t.Fatalf("saveProjectAssistantApprovedPlan: %v", err)
	}

	if err := server.reconcileOrphanedProjectAssistantRun(context.Background(), scope); err != nil {
		t.Fatalf("reconcileOrphanedProjectAssistantRun: %v", err)
	}
	interrupted, err := messages.GetAssistantRun(context.Background(), scope, stale.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun stale: %v", err)
	}
	if interrupted.Status != store.AssistantRunStatusInterrupted {
		t.Fatalf("stale run status = %q, want interrupted", interrupted.Status)
	}

	started, err := server.startProjectAssistantRunDurably(context.Background(), scope, "new conversation", "request-new", func(store.AssistantRun, store.Message, bool) error { return nil })
	if err != nil {
		t.Fatalf("startProjectAssistantRunDurably after reconciliation: %v", err)
	}
	if !started.Started || started.Run.ClientRequestID != "request-new" {
		t.Fatalf("started run = %#v, want a new started conversation", started)
	}
}

func TestProjectAssistantNormalizesPausedLegacyRunFromCheckpointBeforeClaim(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	checkpoint, err := json.Marshal(projectAssistantCheckpointState{AssistantMessageID: "assistant-legacy"})
	if err != nil {
		t.Fatal(err)
	}
	run := store.AssistantRun{
		ID:         "run-legacy",
		Status:     store.AssistantRunStatusPendingPermission,
		RequestID:  "permission-legacy",
		Checkpoint: checkpoint,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := messages.SaveAssistantRun(context.Background(), scope, run); err != nil {
		t.Fatalf("SaveAssistantRun: %v", err)
	}
	if err := messages.AppendMessage(context.Background(), scope, store.Message{
		ID:        "assistant-legacy",
		Role:      "assistant",
		Metadata:  map[string]any{projectAssistantMetadataRunID: run.ID},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	normalized, err := server.normalizeProjectAssistantPausedRun(context.Background(), scope, run, "")
	if err != nil {
		t.Fatalf("normalizeProjectAssistantPausedRun: %v", err)
	}
	if normalized.ActiveMessageID != "assistant-legacy" {
		t.Fatalf("active message id = %q, want checkpoint message", normalized.ActiveMessageID)
	}
	claimed, err := messages.ClaimAssistantRun(context.Background(), scope, run.ID, run.RequestID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimAssistantRun after normalization: %v", err)
	}
	if claimed.ActiveMessageID != "assistant-legacy" || claimed.Status != store.AssistantRunStatusRunning {
		t.Fatalf("claimed run = %#v, want running run with normalized active message", claimed)
	}
}

func TestAbortProjectAssistantRunRepairsPrePatchMessageIdentityFromInterrupt(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	id := identity{orgUUID: "org-a", workspaceUUID: "workspace-a"}
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	run := store.AssistantRun{
		ID:              "run-pre-patch",
		ClientRequestID: "client-pre-patch",
		Status:          store.AssistantRunStatusPendingPermission,
		RequestID:       "permission-pre-patch",
		Checkpoint:      json.RawMessage(`{}`),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := messages.SaveAssistantRun(context.Background(), scope, run); err != nil {
		t.Fatalf("SaveAssistantRun: %v", err)
	}
	if err := messages.AppendMessage(context.Background(), scope, store.Message{
		ID:   "assistant-pre-patch",
		Role: "assistant",
		Metadata: map[string]any{
			projectMessageMetadataStatus: projectMessageStatusPendingPermission,
			projectMessageMetadataAssistantInterrupt: projectAssistantUIInterruptRequest{
				Status: "pending",
				Action: &projectAssistantUIInterruptAction{RunID: run.ID, RequestID: run.RequestID},
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	resp, err := server.abortProjectAssistantRun(context.Background(), id, project, run.ID)
	if err != nil {
		t.Fatalf("abortProjectAssistantRun: %v", err)
	}
	if resp.Status != store.AssistantRunStatusAborted {
		t.Fatalf("abort status = %q, want aborted", resp.Status)
	}
	persisted, err := messages.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun: %v", err)
	}
	if persisted.ActiveMessageID != "assistant-pre-patch" {
		t.Fatalf("ActiveMessageID = %q, want interrupt-bound assistant message", persisted.ActiveMessageID)
	}
}

func TestProjectAssistantSupervisorReservationReleaseAllowsRetryAfterStartFailure(t *testing.T) {
	supervisor := newProjectAssistantSupervisor(context.Background(), store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	release, err := supervisor.Reserve(scope)
	if err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	// The HTTP start handler defers this release when durable creation or
	// attachment fails, so a subsequent caller is not wedged behind a stale
	// in-memory reservation.
	release()
	retryRelease, err := supervisor.Reserve(scope)
	if err != nil {
		t.Fatalf("Reserve after failed start release: %v", err)
	}
	retryRelease()
}

func TestProjectAssistantSupervisorScopesLiveSnapshotMessages(t *testing.T) {
	supervisor := newProjectAssistantSupervisor(context.Background(), store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{
		ID:              "run-1",
		Status:          store.AssistantRunStatusRunning,
		ActiveMessageID: "assistant-1",
		Revision:        1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	message := store.Message{
		ID:        run.ActiveMessageID,
		Role:      "assistant",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := supervisor.Attach(scope, run, message); err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe, err := supervisor.Subscribe(scope, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	snapshot := <-updates
	if snapshot.Run.ProjectName != scope.ProjectName || snapshot.Message.ProjectName != scope.ProjectName {
		t.Fatalf("snapshot scope = run %q message %q, want %q", snapshot.Run.ProjectName, snapshot.Message.ProjectName, scope.ProjectName)
	}
}

func TestProjectAssistantSupervisorCoalescesSlowSubscriberSnapshots(t *testing.T) {
	supervisor := newProjectAssistantSupervisor(context.Background(), store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: run.CreatedAt, UpdatedAt: run.CreatedAt}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: run.CreatedAt, UpdatedAt: run.CreatedAt}
	created, err := supervisor.store.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	accumulator, err := supervisor.Attach(scope, created, assistant)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	updates, unsubscribe, err := supervisor.Subscribe(scope, created.ID, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsubscribe()
	<-updates // initial snapshot

	for _, content := range []string{"one", "two", "three"} {
		if err := accumulator.UpdateText(context.Background(), content, true); err != nil {
			t.Fatalf("UpdateText(%q): %v", content, err)
		}
	}
	select {
	case snapshot := <-updates:
		if snapshot.Message.Content != "three" {
			t.Fatalf("coalesced content = %q, want latest", snapshot.Message.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive coalesced snapshot")
	}
}

func TestProjectAssistantSupervisorTrailingFlushKeepsNewerText(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	accumulator, err := supervisor.Attach(scope, created, assistant)
	if err != nil {
		t.Fatal(err)
	}
	if err := accumulator.UpdateText(context.Background(), "old", false); err != nil {
		t.Fatal(err)
	}
	supervisor.mu.Lock()
	active := supervisor.runs[projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName}]
	active.beforeTextFlushPersist = func() {
		if updateErr := accumulator.UpdateText(context.Background(), "new", false); updateErr != nil {
			t.Errorf("UpdateText(new): %v", updateErr)
		}
	}
	supervisor.mu.Unlock()
	if err := accumulator.UpdateText(context.Background(), "old", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(projectAssistantTextSnapshotInterval + 50*time.Millisecond)
	page, err := memoryStore.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Items {
		if message.ID == assistant.ID {
			if message.Content != "new" {
				t.Fatalf("durable trailing text = %q, want newer chunk", message.Content)
			}
			return
		}
	}
	t.Fatal("assistant message not found")
}

func TestProjectAssistantSupervisorCursorAtTerminalRevisionCloses(t *testing.T) {
	supervisor := newProjectAssistantSupervisor(context.Background(), store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := supervisor.store.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	accumulator, err := supervisor.Attach(scope, created, assistant)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := accumulator.SetStatus(context.Background(), store.AssistantRunStatusCompleted); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	updates, unsubscribe, err := supervisor.Subscribe(scope, created.ID, created.Revision+1)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsubscribe()
	if updates == nil {
		t.Fatal("Subscribe returned a nil channel, which leaves an SSE handler open forever")
	}
	select {
	case _, open := <-updates:
		if open {
			t.Fatal("terminal cursor subscription stayed open")
		}
	case <-time.After(time.Second):
		t.Fatal("terminal cursor subscription did not close")
	}
}

func TestProjectAssistantSupervisorStartsOneWorkerForRun(t *testing.T) {
	supervisor := newProjectAssistantSupervisor(context.Background(), store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := supervisor.store.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if err := supervisor.Start(context.Background(), scope, created, assistant, func(context.Context, *projectAssistantSnapshotAccumulator) { close(started); <-release }); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	<-started
	if err := supervisor.Start(context.Background(), scope, created, assistant, func(context.Context, *projectAssistantSnapshotAccumulator) { t.Fatal("duplicate worker executed") }); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("duplicate Start error = %v, want conflict", err)
	}
	close(release)
}

func TestProjectAssistantSupervisorAbortCannotBeOverwrittenByLateCompletion(t *testing.T) {
	supervisor := newProjectAssistantSupervisor(context.Background(), store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := supervisor.store.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	acc, err := supervisor.Attach(scope, created, assistant)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !supervisor.Abort(scope, created.ID) {
		t.Fatal("Abort = false")
	}
	if err := acc.SetStatus(context.Background(), store.AssistantRunStatusCompleted); err != nil {
		t.Fatalf("late completion: %v", err)
	}
	got, err := supervisor.store.GetAssistantRun(context.Background(), scope, created.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun: %v", err)
	}
	if got.Status != store.AssistantRunStatusAborted {
		t.Fatalf("status = %q, want aborted", got.Status)
	}
}

func TestProjectAssistantSupervisorAbortPersistsAuditAndClearsPendingInterrupt(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusPendingPermission, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", RequestID: "permission-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", Metadata: map[string]any{
		projectMessageMetadataStatus:             projectMessageStatusPendingPermission,
		projectMessageMetadataAssistantInterrupt: projectAssistantUIInterruptRequest{Action: &projectAssistantUIInterruptAction{RunID: run.ID, RequestID: run.RequestID}},
	}, CreatedAt: now, UpdatedAt: now}
	created, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Attach(scope, created, assistant); err != nil {
		t.Fatal(err)
	}
	aborted, err := supervisor.AbortWith(scope, created.ID, func(current *store.AssistantRun, message *store.Message) error {
		updated, auditErr := finalizeProjectAssistantRunAudit(*current, projectAssistantAuditOutcomeAborted, time.Now().UTC())
		if auditErr != nil {
			return auditErr
		}
		*current = updated
		projectAssistantClearPendingInterruptMetadata(message, current.ID)
		return nil
	})
	if err != nil || !aborted {
		t.Fatalf("AbortWith = (%v, %v), want (true, nil)", aborted, err)
	}
	got, err := memoryStore.GetAssistantRun(context.Background(), scope, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.AssistantRunStatusAborted {
		t.Fatalf("status = %q, want aborted", got.Status)
	}
	audit := decodeProjectAssistantRunAudit(t, got.Audit)
	if audit.Outcome != projectAssistantAuditOutcomeAborted {
		t.Fatalf("audit outcome = %q, want aborted", audit.Outcome)
	}
	page, err := memoryStore.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Items {
		if message.ID != assistant.ID {
			continue
		}
		if _, found := message.Metadata[projectMessageMetadataAssistantInterrupt]; found {
			t.Fatalf("assistant metadata still has pending interrupt: %#v", message.Metadata)
		}
		return
	}
	t.Fatal("assistant message not found")
}

func TestProjectAssistantSupervisorShutdownInterruptsWorker(t *testing.T) {
	supervisor := newProjectAssistantSupervisor(context.Background(), store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := supervisor.store.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	if err := supervisor.Start(context.Background(), scope, created, assistant, func(ctx context.Context, _ *projectAssistantSnapshotAccumulator) { <-ctx.Done(); close(done) }); err != nil {
		t.Fatal(err)
	}
	supervisor.Shutdown(context.Background())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker not canceled")
	}
	got, err := supervisor.store.GetAssistantRun(context.Background(), scope, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.AssistantRunStatusInterrupted {
		t.Fatalf("status = %q, want interrupted", got.Status)
	}
	page, err := supervisor.store.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("message count = %d, want 2", len(page.Items))
	}
	for _, message := range page.Items {
		if message.ID != assistant.ID {
			continue
		}
		if message.Metadata[projectAssistantMetadataWorkingStatus] != "Interrupted" || message.Metadata[projectAssistantMetadataRevision] != int64(2) {
			t.Fatalf("interrupted message metadata = %#v", message.Metadata)
		}
		break
	}
}

func TestProjectAssistantSupervisorShutdownLeavesPendingCheckpointResumable(t *testing.T) {
	msgStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), msgStore)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusPendingInput, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Metadata: projectAssistantDurableMetadataForTransition(run, projectMessageStatusPendingInput, false, false, nil, nil), CreatedAt: now, UpdatedAt: now}
	if _, err := msgStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Attach(scope, run, assistant); err != nil {
		t.Fatal(err)
	}
	supervisor.Shutdown(context.Background())
	got, err := msgStore.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.AssistantRunStatusPendingInput || got.Revision != 1 {
		t.Fatalf("pending run changed during shutdown: %#v", got)
	}
}

func TestProjectAssistantSupervisorParentCancellationPersistsInterrupted(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	supervisor := newProjectAssistantSupervisor(parent, store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := supervisor.store.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	if err := supervisor.Start(context.Background(), scope, created, assistant, func(ctx context.Context, _ *projectAssistantSnapshotAccumulator) { <-ctx.Done(); close(done) }); err != nil {
		t.Fatal(err)
	}
	cancelParent()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not stop worker")
	}
	deadline := time.After(time.Second)
	for {
		got, getErr := supervisor.store.GetAssistantRun(context.Background(), scope, created.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if got.Status == store.AssistantRunStatusInterrupted {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("status = %q, want interrupted", got.Status)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestProjectAssistantSupervisorReleasesPendingWorkerOwnership(t *testing.T) {
	supervisor := newProjectAssistantSupervisor(context.Background(), store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusPendingPermission, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := supervisor.store.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan struct{})
	if err := supervisor.Start(context.Background(), scope, created, assistant, func(context.Context, *projectAssistantSnapshotAccumulator) { close(firstDone) }); err != nil {
		t.Fatal(err)
	}
	<-firstDone
	// finish runs after worker return; wait briefly for the ownership release.
	time.Sleep(10 * time.Millisecond)
	secondDone := make(chan struct{})
	if err := supervisor.Start(context.Background(), scope, created, assistant, func(context.Context, *projectAssistantSnapshotAccumulator) { close(secondDone) }); err != nil {
		t.Fatalf("resume Start: %v", err)
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("resumed worker did not start")
	}
}

func TestProjectAssistantSupervisorRestartAttachesPendingRunWithoutMutation(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusPendingInput, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	if _, err := supervisor.Attach(scope, created, assistant); err != nil {
		t.Fatal(err)
	}
	got, err := memoryStore.GetAssistantRun(context.Background(), scope, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.AssistantRunStatusPendingInput || got.Revision != 1 {
		t.Fatalf("restart attach mutated durable run: %#v", got)
	}
}

func TestProjectAssistantSupervisorClaimPublishesRunningRevision(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusPendingPermission, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", RequestID: "permission-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", Metadata: map[string]any{
		projectMessageMetadataAssistantActions:       []projectAssistantUIAction{{ID: "prior", Status: "succeeded", Label: "Wrote file"}},
		projectAssistantMetadataPreviewRefreshNeeded: true,
		projectMessageMetadataAssistantInterrupt:     &projectAssistantUIInterruptRequest{InterruptID: "resolved"},
	}, CreatedAt: now, UpdatedAt: now}
	created, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	accumulator, err := supervisor.Attach(scope, created, assistant)
	if err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe, err := supervisor.Subscribe(scope, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	<-updates

	claimed, err := accumulator.ClaimPending(context.Background(), created.RequestID)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if claimed.Status != store.AssistantRunStatusRunning || claimed.Revision != created.Revision+1 {
		t.Fatalf("claimed run = %#v, want durable running revision", claimed)
	}
	page, err := memoryStore.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Items {
		if message.ID != assistant.ID {
			continue
		}
		if message.Metadata[projectAssistantMetadataWorkingStatus] != "Working" || message.Metadata[projectAssistantMetadataRevision] != claimed.Revision || message.Metadata[projectAssistantMetadataPreviewRefreshNeeded] != true {
			t.Fatalf("claimed message metadata = %#v, want durable running revision", message.Metadata)
		}
		if _, found := message.Metadata[projectMessageMetadataAssistantInterrupt]; found {
			t.Fatalf("claimed metadata retained resolved interrupt: %#v", message.Metadata)
		}
		if len(projectAssistantUIActionsFromMetadata(message.Metadata[projectMessageMetadataAssistantActions])) != 1 {
			t.Fatalf("claimed metadata lost prior action: %#v", message.Metadata)
		}
		break
	}
	if err := accumulator.UpdateSnapshot(context.Background(), func(current *store.AssistantRun, message *store.Message) {
		next := *current
		next.Revision++
		message.Metadata = projectAssistantDurableMetadataForTransition(next, "Writing files", false, true, []projectToolCallStreamEvent{{ID: "tool-1", Name: projectToolWriteFile, Status: "succeeded"}}, nil)
	}); err != nil {
		t.Fatalf("persist resumed tool metadata: %v", err)
	}
	if err := accumulator.SetStatus(context.Background(), store.AssistantRunStatusPendingInput); err != nil {
		t.Fatalf("persist resumed pending status: %v", err)
	}
	if err := accumulator.SetStatus(context.Background(), store.AssistantRunStatusCompleted); err != nil {
		t.Fatalf("persist resumed terminal status: %v", err)
	}
	page, err = memoryStore.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Items {
		if message.ID != assistant.ID {
			continue
		}
		if message.Metadata[projectAssistantMetadataWorkingStatus] != "Completed" || message.Metadata[projectAssistantMetadataPreviewRefreshNeeded] != true {
			t.Fatalf("resumed terminal metadata = %#v", message.Metadata)
		}
		if _, ok := message.Metadata[projectMessageMetadataAssistantActions]; !ok {
			t.Fatalf("resumed terminal metadata lost actions: %#v", message.Metadata)
		}
		break
	}
	select {
	case snapshot := <-updates:
		if snapshot.Run.Status != store.AssistantRunStatusCompleted || snapshot.Run.Revision <= claimed.Revision {
			t.Fatalf("snapshot = %#v, want later completed metadata revision", snapshot.Run)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed metadata transitions did not publish a terminal snapshot")
	}
}

func TestResumedAssistantSegmentPublishesTerminalMessageAndRunAtomically(t *testing.T) {
	msgStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), msgStore)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Metadata: projectAssistantDurableMetadataForTransition(run, "Working", false, false, nil, nil), CreatedAt: now, UpdatedAt: now}
	if _, err := msgStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	accumulator, err := supervisor.Attach(scope, run, assistant)
	if err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe, err := supervisor.Subscribe(scope, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	<-updates
	state := &projectAssistantDurableMetadataState{status: "Writing files", toolCalls: []projectToolCallStreamEvent{{ID: "tool-1", Name: projectToolWriteFile, Status: "succeeded"}}}
	server := NewWithWorkspace(nil, msgStore, nil, "", false)
	if err := server.persistProjectAssistantDurableMetadata(context.Background(), accumulator, projectWorkspaceScope(identity{}, scope.ProjectName), state, nil); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.UpdateText(context.Background(), "done", true); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.SetStatus(context.Background(), store.AssistantRunStatusCompleted); err != nil {
		t.Fatal(err)
	}
	var terminal projectAssistantRunSnapshot
	select {
	case terminal = <-updates:
	case <-time.After(time.Second):
		t.Fatal("missing resumed terminal snapshot")
	}
	if terminal.Run.Status != store.AssistantRunStatusCompleted || terminal.Message.Content != "done" || terminal.Message.Metadata[projectAssistantMetadataPreviewRefreshNeeded] != true {
		t.Fatalf("terminal snapshot = %#v", terminal)
	}
	if _, ok := terminal.Message.Metadata[projectMessageMetadataAssistantActions]; !ok {
		t.Fatalf("terminal metadata lost actions: %#v", terminal.Message.Metadata)
	}
}

type failingResumeSnapshotStore struct {
	store.Store
	err error
}

func (s failingResumeSnapshotStore) SaveAssistantRunSnapshot(context.Context, store.Scope, store.AssistantRun, []store.Message, int64) error {
	return s.err
}

func TestResumeSnapshotPersistenceFailurePreventsSuccessfulTerminalTransition(t *testing.T) {
	inner := store.NewMemoryStore()
	failing := failingResumeSnapshotStore{Store: inner, err: errors.New("snapshot unavailable")}
	supervisor := newProjectAssistantSupervisor(context.Background(), failing)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	accumulator, err := supervisor.Attach(scope, run, assistant)
	if err != nil {
		t.Fatal(err)
	}
	if err := accumulator.UpdateText(context.Background(), "partial", true); err == nil {
		t.Fatal("expected snapshot persistence error")
	}
	if err := accumulator.SetStatus(context.Background(), store.AssistantRunStatusCompleted); err != nil {
		t.Fatal(err)
	}
	persisted, err := inner.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status == store.AssistantRunStatusCompleted {
		t.Fatalf("persistence failure allowed successful completion: %#v", persisted)
	}
}

func TestWriteProjectAssistantRunStartReturnsRunUserMessage(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(nil, memoryStore, nil, "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	user := store.Message{ID: "user-z", Role: "user", Content: "build a todo app", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: user.ID, ActiveMessageID: assistant.ID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	created, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := memoryStore.AppendMessage(context.Background(), scope, store.Message{ID: "user-a", Role: "user", Content: "an unrelated earlier-looking message", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.writeProjectAssistantRunStart(recorder, http.StatusAccepted, scope, created)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	var response projectAssistantRunStartResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.User == nil || response.User.ID != user.ID || response.User.Content != user.Content {
		t.Fatalf("response user = %#v, want %#v", response.User, projectMessageToAPI(user))
	}
}

func TestWriteProjectAssistantRunStartFindsOriginatingUserBeyondFirstFiveHundredMessages(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(nil, memoryStore, nil, "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	user := store.Message{ID: "user-target", Role: "user", Content: "the intended prompt", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-target", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusCompleted, ClientRequestID: "request-1", UserMessageID: user.ID, ActiveMessageID: assistant.ID, Revision: 2, CreatedAt: now, UpdatedAt: now}
	if err := memoryStore.SaveAssistantRun(context.Background(), scope, run); err != nil {
		t.Fatal(err)
	}
	if err := memoryStore.AppendMessage(context.Background(), scope, user); err != nil {
		t.Fatal(err)
	}
	if err := memoryStore.AppendMessage(context.Background(), scope, assistant); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 550; i++ {
		if err := memoryStore.AppendMessage(context.Background(), scope, store.Message{ID: fmt.Sprintf("noise-%03d", i), Role: "user", Content: "unrelated", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	recorder := httptest.NewRecorder()
	server.writeProjectAssistantRunStart(recorder, http.StatusAccepted, scope, run)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	var response projectAssistantRunStartResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.User == nil || response.User.ID != user.ID || response.Assistant.ID != assistant.ID {
		t.Fatalf("response did not load exact long-history messages: %#v", response)
	}
}

func TestProjectAssistantRunStartIdempotentLegacyRunOmitsUnknownUser(t *testing.T) {
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\nspec: {}\n"}}}})
	}))
	defer graphQL.Close()
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), memoryStore, nil, "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	assistant := store.Message{ID: "assistant-1", Role: "assistant", Content: "still readable", CreatedAt: now, UpdatedAt: now}
	if err := memoryStore.AppendMessage(context.Background(), scope, assistant); err != nil {
		t.Fatal(err)
	}
	legacy := store.AssistantRun{ID: "run-legacy", Status: store.AssistantRunStatusCompleted, ClientRequestID: "request-legacy", ActiveMessageID: assistant.ID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := memoryStore.SaveAssistantRun(context.Background(), scope, legacy); err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	server.Register(router)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/messages", strings.NewReader(`{"content":"retry","clientRequestID":"request-legacy"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("legacy retry status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	var response projectAssistantRunStartResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Run.ID != legacy.ID || response.Assistant.ID != assistant.ID || response.User != nil {
		t.Fatalf("legacy retry response = %#v, want readable run/assistant and no fabricated user", response)
	}
}

func TestProjectAssistantRunStartInitialProjectPromptGrantsOnlyEmptyTranscript(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	secret, err := json.Marshal(projectLLMSettingsSecret(settings).Object)
	if err != nil {
		t.Fatal(err)
	}
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.Contains(request.Query, "ProjectYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\nspec: {}\n"}}}})
		case strings.Contains(request.Query, "SecretYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"v1": map[string]any{"SecretYaml": string(secret)}}})
		default:
			t.Fatalf("unexpected GraphQL query: %s", request.Query)
		}
	}))
	defer graphQL.Close()

	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), store.NewMemoryStore(), nil, "", false)
	engine := &initialProjectPromptCaptureEngine{requests: make(chan projectAssistantRunRequest, 2)}
	server.assistantEngine = engine
	router := mux.NewRouter()
	server.Register(router)

	post := func(body string) {
		request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/messages", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer caller-token")
		request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
		request.Header.Set("X-Kedge-Cluster", "cluster-a")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("start status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
		}
	}

	post(`{"content":"build a todo app","clientRequestID":"request-1","initialProjectPrompt":true}`)
	select {
	case request := <-engine.requests:
		if request.InitialApprovedPlan == nil {
			t.Fatal("first initial-project durable run did not receive its run-local approval grant")
		}
		if !request.InitialApprovedPlan.RunLocal {
			t.Fatalf("first initial-project grant = %#v, want run-local", request.InitialApprovedPlan)
		}
	case <-time.After(time.Second):
		t.Fatal("first durable run did not reach assistant engine")
	}

	post(`{"content":"continue","clientRequestID":"request-2","initialProjectPrompt":true}`)
	select {
	case request := <-engine.requests:
		if request.InitialApprovedPlan != nil {
			t.Fatalf("later durable run received initial-project grant: %#v", request.InitialApprovedPlan)
		}
	case <-time.After(time.Second):
		t.Fatal("later durable run did not reach assistant engine")
	}
}

func TestProjectAssistantRunStartInitialProjectPromptSeesTranscriptAfterReservation(t *testing.T) {
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	messages := store.NewMemoryStore()
	observingStore := &reservationObservingStore{Store: messages, scope: scope}
	server := NewWithWorkspace(nil, observingStore, nil, "", false)
	observingStore.supervisor = server.projectAssistantSupervisor()
	now := time.Now().UTC()
	if err := messages.AppendMessage(context.Background(), scope, store.Message{ID: "prior-user", Role: "user", Content: "already started", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var transcriptEmpty bool
	_, err := server.startProjectAssistantRunDurably(context.Background(), scope, "continue", "request-after-prior", func(_ store.AssistantRun, _ store.Message, empty bool) error {
		transcriptEmpty = empty
		return nil
	})
	if err != nil {
		t.Fatalf("startProjectAssistantRunDurably: %v", err)
	}
	if transcriptEmpty {
		t.Fatal("durable start retained a stale empty-transcript result after reserving the project")
	}
	if !observingStore.observedReservation {
		t.Fatal("durable start did not read the transcript through the reservation-observing store")
	}
}

func TestProjectAssistantSnapshotStreamReconcilesRestartedRunningRun(t *testing.T) {
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(request.Query, "ProjectYaml") {
			t.Fatalf("unexpected GraphQL query: %s", request.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\nspec: {}\n"}},
		}})
	}))
	defer graphQL.Close()
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), memoryStore, nil, "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", Content: "working", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	server.Register(router)
	request := httptest.NewRequest(http.MethodGet, "/api/projects/demo/assistant/run-1/stream", nil)
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	raw, found := firstSSELine(recorder.Body.Bytes())
	if !found {
		t.Fatalf("response did not contain an SSE snapshot: %s", recorder.Body.String())
	}
	var snapshot projectAssistantRunSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != store.AssistantRunStatusInterrupted {
		t.Fatalf("streamed status = %q, want interrupted", snapshot.Run.Status)
	}
}

func TestProjectAssistantRunRoutesStartLatestAbortAndIsolateTenantStreams(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	secret, err := json.Marshal(projectLLMSettingsSecret(settings).Object)
	if err != nil {
		t.Fatal(err)
	}
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		switch {
		case strings.Contains(request.Query, "ProjectYaml"):
			response = map[string]any{"data": map[string]any{"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\nspec: {}\n"}}}}
		case strings.Contains(request.Query, "SecretYaml"):
			response = map[string]any{"data": map[string]any{"v1": map[string]any{"SecretYaml": string(secret)}}}
		default:
			t.Fatalf("unexpected GraphQL query: %s", request.Query)
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer graphQL.Close()
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), memoryStore, nil, "", false)
	var terminalLogsMu sync.Mutex
	var terminalLogs []store.AssistantRun
	server.projectAssistantSupervisor().lifecycleLog = func(event string, _ store.Scope, run store.AssistantRun) {
		if event == "completed" || event == "failed" || event == "aborted" {
			terminalLogsMu.Lock()
			terminalLogs = append(terminalLogs, run)
			terminalLogsMu.Unlock()
		}
	}
	engine := &blockingStartRouteEngine{entered: make(chan struct{}), finished: make(chan struct{})}
	server.assistantEngine = engine
	router := mux.NewRouter()
	server.Register(router)
	starter, cancelStarter := context.WithCancel(context.Background())
	defer cancelStarter()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/messages/stream", strings.NewReader(`{"content":"build a todo app","clientRequestID":"request-1"}`)).WithContext(starter)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	started := httptest.NewRecorder()
	streamDone := make(chan struct{})
	go func() {
		router.ServeHTTP(started, request)
		close(streamDone)
	}()
	select {
	case <-engine.entered:
	case <-time.After(time.Second):
		t.Fatal("legacy start worker did not enter")
	}
	cancelStarter()
	select {
	case <-streamDone:
	case <-time.After(time.Second):
		t.Fatal("legacy stream did not detach after request cancellation")
	}
	if started.Code != http.StatusOK {
		t.Fatalf("legacy start status = %d, want %d: %s", started.Code, http.StatusOK, started.Body.String())
	}
	run, err := memoryStore.LatestAssistantRun(context.Background(), store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := memoryStore.ListMessages(context.Background(), store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || run.UserMessageID == "" || run.ActiveMessageID == "" {
		t.Fatalf("legacy start persisted %#v with run %#v, want exactly one user and one assistant", page.Items, run)
	}
	latest := httptest.NewRecorder()
	latestRequest := httptest.NewRequest(http.MethodGet, "/api/projects/demo/assistant/runs/latest", nil)
	latestRequest.Header = request.Header.Clone()
	router.ServeHTTP(latest, latestRequest)
	if latest.Code != http.StatusOK {
		t.Fatalf("latest status = %d, want %d: %s", latest.Code, http.StatusOK, latest.Body.String())
	}
	var latestSnapshot projectAssistantRunSnapshot
	if err := json.NewDecoder(latest.Body).Decode(&latestSnapshot); err != nil {
		t.Fatal(err)
	}
	if latestSnapshot.Run.ID != run.ID || latestSnapshot.Run.Status != run.Status || latestSnapshot.Run.Revision != run.Revision || latestSnapshot.Message.ID != run.ActiveMessageID {
		t.Fatalf("latest snapshot = %#v, want started run and active assistant message", latestSnapshot)
	}
	otherWorkspace := httptest.NewRecorder()
	otherWorkspaceRequest := httptest.NewRequest(http.MethodGet, "/api/projects/demo/assistant/"+run.ID+"/stream", nil)
	otherWorkspaceRequest.Header = request.Header.Clone()
	otherWorkspaceRequest.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-b")
	otherWorkspaceRequest.Header.Set("X-Kedge-Cluster", "cluster-a")
	router.ServeHTTP(otherWorkspace, otherWorkspaceRequest)
	if otherWorkspace.Code == http.StatusOK {
		t.Fatalf("cross-workspace stream unexpectedly succeeded: %s", otherWorkspace.Body.String())
	}
	abort := httptest.NewRecorder()
	abortRequest := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/"+run.ID+"/abort", nil)
	abortRequest.Header = request.Header.Clone()
	router.ServeHTTP(abort, abortRequest)
	if abort.Code != http.StatusAccepted {
		t.Fatalf("abort status = %d, want %d: %s", abort.Code, http.StatusAccepted, abort.Body.String())
	}
	select {
	case <-engine.finished:
	case <-time.After(time.Second):
		t.Fatal("explicit abort did not stop started worker")
	}
	terminal, err := memoryStore.GetAssistantRun(context.Background(), store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}, run.ID)
	if err != nil || terminal.Status != store.AssistantRunStatusAborted {
		t.Fatalf("latest durable run after legacy disconnect/abort = %#v, %v; want recoverable aborted run", terminal, err)
	}

	// The modern POST response is short-lived too: canceling its request after
	// the durable start must not cancel the server-owned worker.
	normalEngine := &blockingStartRouteEngine{entered: make(chan struct{}), finished: make(chan struct{})}
	server.assistantEngine = normalEngine
	normalContext, cancelNormal := context.WithCancel(context.Background())
	normalRequest := httptest.NewRequest(http.MethodPost, "/api/projects/demo/messages", strings.NewReader(`{"content":"continue building","clientRequestID":"request-2"}`)).WithContext(normalContext)
	normalRequest.Header = request.Header.Clone()
	normal := httptest.NewRecorder()
	router.ServeHTTP(normal, normalRequest)
	if normal.Code != http.StatusAccepted {
		t.Fatalf("normal start status = %d, want %d: %s", normal.Code, http.StatusAccepted, normal.Body.String())
	}
	select {
	case <-normalEngine.entered:
	case <-time.After(time.Second):
		t.Fatal("normal start worker did not enter")
	}
	cancelNormal()
	select {
	case <-normalEngine.finished:
		t.Fatal("canceling normal start request canceled worker")
	case <-time.After(25 * time.Millisecond):
	}
	normalRun, err := memoryStore.LatestAssistantRun(context.Background(), store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"})
	if err != nil || normalRun.ClientRequestID != "request-2" {
		t.Fatalf("latest normal durable run = %#v, %v", normalRun, err)
	}
	if !server.projectAssistantSupervisor().Abort(store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}, normalRun.ID) {
		t.Fatal("Abort did not find normal worker")
	}
	select {
	case <-normalEngine.finished:
	case <-time.After(time.Second):
		t.Fatal("abort did not stop normal worker")
	}
	for _, tt := range []struct{ requestID, chunk, reply string }{
		{requestID: "request-3", reply: "returned only"},
		{requestID: "request-4", chunk: "partial", reply: "partial returned final"},
	} {
		server.assistantEngine = replyStartRouteEngine{chunk: tt.chunk, reply: tt.reply}
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/projects/demo/messages", strings.NewReader(`{"content":"finish","clientRequestID":"`+tt.requestID+`"}`))
		req.Header = request.Header.Clone()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusAccepted {
			t.Fatalf("reply start status = %d: %s", response.Code, response.Body.String())
		}
		var durable store.AssistantRun
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			durable, err = memoryStore.LatestAssistantRun(context.Background(), store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"})
			if err == nil && durable.ClientRequestID == tt.requestID && durable.Status == store.AssistantRunStatusCompleted {
				break
			}
			time.Sleep(time.Millisecond)
		}
		message, messageErr := server.findProjectMessage(context.Background(), store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}, durable.ActiveMessageID)
		if durable.Status != store.AssistantRunStatusCompleted || messageErr != nil || message.Content != tt.reply {
			t.Fatalf("durable reply %#v message %#v err %v, want completed %q", durable, message, messageErr, tt.reply)
		}
	}
	server.assistantEngine = failingStartRouteEngine{}
	failing := httptest.NewRecorder()
	failingRequest := httptest.NewRequest(http.MethodPost, "/api/projects/demo/messages", strings.NewReader(`{"content":"fail","clientRequestID":"request-5"}`))
	failingRequest.Header = request.Header.Clone()
	router.ServeHTTP(failing, failingRequest)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		failed, getErr := memoryStore.LatestAssistantRun(context.Background(), store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"})
		if getErr == nil && failed.ClientRequestID == "request-5" && failed.Status == store.AssistantRunStatusFailed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	terminalLogsMu.Lock()
	logs := append([]store.AssistantRun(nil), terminalLogs...)
	terminalLogsMu.Unlock()
	seen := map[store.AssistantRunStatus]bool{}
	for _, logged := range logs {
		if logged.Revision < 2 || !assistantRunTerminal(logged.Status) {
			t.Fatalf("terminal lifecycle log = %#v, want committed terminal revision", logged)
		}
		seen[logged.Status] = true
	}
	for _, status := range []store.AssistantRunStatus{store.AssistantRunStatusCompleted, store.AssistantRunStatusFailed, store.AssistantRunStatusAborted} {
		if !seen[status] {
			t.Fatalf("terminal lifecycle logs = %#v, missing %q", logs, status)
		}
	}
}

func TestProjectAssistantSupervisorWorkerPersistsPlanSnapshots(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	secret, err := json.Marshal(projectLLMSettingsSecret(settings).Object)
	if err != nil {
		t.Fatal(err)
	}
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.Contains(request.Query, "ProjectYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\nspec: {}\n"}}}})
		case strings.Contains(request.Query, "SecretYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"v1": map[string]any{"SecretYaml": string(secret)}}})
		default:
			t.Fatalf("unexpected GraphQL query: %s", request.Query)
		}
	}))
	defer graphQL.Close()

	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), memoryStore, nil, "", false)
	firstPlan := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Inspect project", ActiveForm: "Inspecting project", Status: "in_progress"},
	}}
	latestPlan := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Inspect project", ActiveForm: "Inspecting project", Status: "completed"},
		{Content: "Verify preview", ActiveForm: "Verifying preview", Status: "in_progress"},
	}}
	engine := &planStartRouteEngine{plans: []projectAssistantPlanSnapshot{firstPlan, latestPlan}, published: make(chan struct{}), release: make(chan struct{})}
	server.assistantEngine = engine
	router := mux.NewRouter()
	server.Register(router)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/messages", strings.NewReader(`{"content":"finish the plan","clientRequestID":"plan-request"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, want %d: %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	select {
	case <-engine.published:
	case <-time.After(time.Second):
		t.Fatal("worker did not publish plan")
	}
	var started projectAssistantRunStartResponse
	if err := json.NewDecoder(response.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	updates, unsubscribe, err := server.projectAssistantSupervisor().Subscribe(scope, started.Run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	var live projectAssistantRunSnapshot
	select {
	case live = <-updates:
	case <-time.After(time.Second):
		t.Fatal("missing live plan snapshot")
	}
	if got := live.Message.Metadata[projectAssistantMetadataPlan]; !reflect.DeepEqual(got, latestPlan) {
		t.Fatalf("live plan = %#v, want latest %#v", got, latestPlan)
	}
	if live.Run.Revision <= started.Run.Revision {
		t.Fatalf("live revision = %d, want greater than start revision %d", live.Run.Revision, started.Run.Revision)
	}

	close(engine.release)
	var terminal store.AssistantRun
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		terminal, err = memoryStore.GetAssistantRun(context.Background(), scope, started.Run.ID)
		if err == nil && terminal.Status == store.AssistantRunStatusCompleted {
			break
		}
		time.Sleep(time.Millisecond)
	}
	message, err := server.findProjectMessage(context.Background(), scope, terminal.ActiveMessageID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != store.AssistantRunStatusCompleted || !reflect.DeepEqual(message.Metadata[projectAssistantMetadataPlan], latestPlan) {
		t.Fatalf("terminal run = %#v, message = %#v, want completed latest plan", terminal, message)
	}
	if terminal.Revision <= live.Run.Revision {
		t.Fatalf("terminal revision = %d, want greater than live revision %d", terminal.Revision, live.Run.Revision)
	}
}

func TestProjectAssistantSupervisorResumeWorkerPersistsLatestPlanSnapshot(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	secret, err := json.Marshal(projectLLMSettingsSecret(settings).Object)
	if err != nil {
		t.Fatal(err)
	}
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.Contains(request.Query, "ProjectYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\nspec: {}\n"}}}})
		case strings.Contains(request.Query, "SecretYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"v1": map[string]any{"SecretYaml": string(secret)}}})
		default:
			t.Fatalf("unexpected GraphQL query: %s", request.Query)
		}
	}))
	defer graphQL.Close()

	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), memoryStore, nil, "", false)
	latestPlan := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Inspect project", ActiveForm: "Inspecting project", Status: "completed"},
		{Content: "Verify preview", ActiveForm: "Verifying preview", Status: "in_progress"},
	}}
	engine := &planResumeRouteEngine{plans: []projectAssistantPlanSnapshot{
		{Steps: []projectAssistantPlanStep{{Content: "Inspect project", ActiveForm: "Inspecting project", Status: "in_progress"}}},
		latestPlan,
	}, published: make(chan struct{}), release: make(chan struct{})}
	server.assistantEngine = engine
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	checkpoint, err := json.Marshal(projectAssistantCheckpointState{Eino: &projectAssistantEinoCheckpointState{
		CheckpointID: "run-1", Checkpoint: []byte("checkpoint"), InterruptID: "interrupt-1", InterruptType: projectAssistantInterruptTypePermission, ToolCallID: "tool-1", ToolName: projectToolWriteFile,
	}})
	if err != nil {
		t.Fatal(err)
	}
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusPendingPermission, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", RequestID: "permission-1", Checkpoint: checkpoint, Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	server.Register(router)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/run-1/resume", strings.NewReader(`{"requestID":"permission-1","decision":"allow"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d, want %d: %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	select {
	case <-engine.published:
	case <-time.After(time.Second):
		t.Fatal("resumed worker did not publish plans")
	}
	updates, unsubscribe, err := server.projectAssistantSupervisor().Subscribe(scope, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	var live projectAssistantRunSnapshot
	select {
	case live = <-updates:
	case <-time.After(time.Second):
		t.Fatal("missing resumed live plan snapshot")
	}
	if !reflect.DeepEqual(live.Message.Metadata[projectAssistantMetadataPlan], latestPlan) {
		t.Fatalf("live plan = %#v, want latest %#v", live.Message.Metadata[projectAssistantMetadataPlan], latestPlan)
	}
	if live.Run.Revision <= run.Revision {
		t.Fatalf("live revision = %d, want greater than initial revision %d", live.Run.Revision, run.Revision)
	}

	close(engine.release)
	var terminal store.AssistantRun
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		terminal, err = memoryStore.GetAssistantRun(context.Background(), scope, run.ID)
		if err == nil && terminal.Status == store.AssistantRunStatusCompleted {
			break
		}
		time.Sleep(time.Millisecond)
	}
	message, err := server.findProjectMessage(context.Background(), scope, terminal.ActiveMessageID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != store.AssistantRunStatusCompleted || !reflect.DeepEqual(message.Metadata[projectAssistantMetadataPlan], latestPlan) {
		t.Fatalf("terminal run = %#v, message = %#v, want completed latest plan", terminal, message)
	}
	if terminal.Revision <= live.Run.Revision {
		t.Fatalf("terminal revision = %d, want greater than live revision %d", terminal.Revision, live.Run.Revision)
	}
}

func TestResumeProjectAssistantRouteDetachesRequestAndPublishesRunningSnapshot(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	secret, err := json.Marshal(projectLLMSettingsSecret(settings).Object)
	if err != nil {
		t.Fatal(err)
	}
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		switch {
		case strings.Contains(request.Query, "ProjectYaml"):
			response = map[string]any{"data": map[string]any{
				"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\nspec: {}\n"}},
			}}
		case strings.Contains(request.Query, "SecretYaml"):
			response = map[string]any{"data": map[string]any{"v1": map[string]any{"SecretYaml": string(secret)}}}
		default:
			t.Fatalf("unexpected GraphQL query: %s", request.Query)
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer graphQL.Close()
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), memoryStore, nil, "", false)
	engine := &blockingResumeRouteEngine{entered: make(chan struct{}), finished: make(chan struct{})}
	server.assistantEngine = engine
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	checkpoint, err := json.Marshal(projectAssistantCheckpointState{Eino: &projectAssistantEinoCheckpointState{
		CheckpointID: "run-1", Checkpoint: []byte("checkpoint"), InterruptID: "interrupt-1", InterruptType: projectAssistantInterruptTypePermission, ToolCallID: "tool-1", ToolName: projectToolWriteFile,
	}})
	if err != nil {
		t.Fatal(err)
	}
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusPendingPermission, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", RequestID: "permission-1", Checkpoint: checkpoint, Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	server.Register(router)
	starter, cancelStarter := context.WithCancel(context.Background())
	defer cancelStarter()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/run-1/resume", strings.NewReader(`{"requestID":"permission-1","decision":"allow"}`)).WithContext(starter)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	select {
	case <-engine.entered:
	case <-time.After(time.Second):
		got, getErr := memoryStore.GetAssistantRun(context.Background(), scope, run.ID)
		t.Fatalf("resume engine did not start; run = %#v, err = %v", got, getErr)
	}
	updates, unsubscribe, err := server.projectAssistantSupervisor().Subscribe(scope, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	select {
	case snapshot := <-updates:
		if snapshot.Run.Status != store.AssistantRunStatusRunning || snapshot.Run.Revision != 2 {
			t.Fatalf("resume snapshot = %#v, want running revision 2", snapshot.Run)
		}
	case <-time.After(time.Second):
		t.Fatal("resume did not publish running snapshot")
	}
	cancelStarter()
	select {
	case <-engine.finished:
		t.Fatal("canceling initiating HTTP request canceled resumed worker")
	case <-time.After(25 * time.Millisecond):
	}
	if !server.projectAssistantSupervisor().Abort(scope, run.ID) {
		t.Fatal("Abort did not find resumed worker")
	}
	select {
	case <-engine.finished:
	case <-time.After(time.Second):
		t.Fatal("Abort did not cancel resumed worker")
	}
}

func TestResumeProjectAssistantRouteRepairsPrePatchMessageIdentity(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	secret, err := json.Marshal(projectLLMSettingsSecret(settings).Object)
	if err != nil {
		t.Fatal(err)
	}
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		switch {
		case strings.Contains(request.Query, "ProjectYaml"):
			response = map[string]any{"data": map[string]any{
				"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\nspec: {}\n"}},
			}}
		case strings.Contains(request.Query, "SecretYaml"):
			response = map[string]any{"data": map[string]any{"v1": map[string]any{"SecretYaml": string(secret)}}}
		default:
			t.Fatalf("unexpected GraphQL query: %s", request.Query)
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer graphQL.Close()

	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), memoryStore, nil, "", false)
	engine := &blockingResumeRouteEngine{entered: make(chan struct{}), finished: make(chan struct{})}
	server.assistantEngine = engine
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo"}
	now := time.Now().UTC()
	checkpoint, err := json.Marshal(projectAssistantCheckpointState{Eino: &projectAssistantEinoCheckpointState{
		CheckpointID: "run-legacy", Checkpoint: []byte("checkpoint"), InterruptID: "interrupt-legacy", InterruptType: projectAssistantInterruptTypePermission, ToolCallID: "tool-legacy", ToolName: projectToolWriteFile,
	}})
	if err != nil {
		t.Fatal(err)
	}
	run := store.AssistantRun{ID: "run-legacy", Status: store.AssistantRunStatusPendingPermission, ClientRequestID: "request-legacy", RequestID: "permission-legacy", Checkpoint: checkpoint, Revision: 1, CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{
		ID:   "assistant-legacy",
		Role: "assistant",
		Metadata: map[string]any{
			projectMessageMetadataStatus: projectMessageStatusPendingPermission,
			projectMessageMetadataAssistantInterrupt: projectAssistantUIInterruptRequest{
				Status: "pending",
				Action: &projectAssistantUIInterruptAction{RunID: run.ID, RequestID: run.RequestID, AssistantMessageID: "assistant-legacy"},
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := memoryStore.SaveAssistantRun(context.Background(), scope, run); err != nil {
		t.Fatal(err)
	}
	if err := memoryStore.AppendMessage(context.Background(), scope, assistant); err != nil {
		t.Fatal(err)
	}

	router := mux.NewRouter()
	server.Register(router)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/run-legacy/resume", strings.NewReader(`{"requestID":"permission-legacy","decision":"allow","assistantMessageID":"assistant-legacy"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	var snapshot projectAssistantRunSnapshot
	if err := json.NewDecoder(recorder.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.ActiveMessageID != assistant.ID || snapshot.Message.ID != assistant.ID {
		t.Fatalf("resume snapshot = %#v, want recovered active message %q", snapshot, assistant.ID)
	}
	select {
	case <-engine.entered:
	case <-time.After(time.Second):
		t.Fatal("resume engine did not start")
	}
	if !server.projectAssistantSupervisor().Abort(scope, run.ID) {
		t.Fatal("Abort did not find resumed worker")
	}
	select {
	case <-engine.finished:
	case <-time.After(time.Second):
		t.Fatal("Abort did not cancel resumed worker")
	}
}

type blockingResumeRouteEngine struct {
	entered  chan struct{}
	finished chan struct{}
}

type blockingStartRouteEngine struct {
	entered  chan struct{}
	finished chan struct{}
}

type replyStartRouteEngine struct{ chunk, reply string }

type planStartRouteEngine struct {
	plans     []projectAssistantPlanSnapshot
	published chan struct{}
	release   chan struct{}
}

type planResumeRouteEngine struct {
	plans     []projectAssistantPlanSnapshot
	published chan struct{}
	release   chan struct{}
}

type initialProjectPromptCaptureEngine struct {
	requests chan projectAssistantRunRequest
}

type reservationObservingStore struct {
	store.Store
	supervisor          *projectAssistantSupervisor
	scope               store.Scope
	observedReservation bool
}

func (s *reservationObservingStore) ListMessages(ctx context.Context, scope store.Scope, limit int, cursor string) (store.Page, error) {
	if scope == s.scope {
		if s.supervisor == nil || !s.supervisor.reserved(scope) {
			return store.Page{}, errors.New("transcript read occurred before project reservation")
		}
		s.observedReservation = true
	}
	return s.Store.ListMessages(ctx, scope, limit, cursor)
}

func (e *initialProjectPromptCaptureEngine) StreamProjectAssistant(_ context.Context, request projectAssistantRunRequest) (projectAssistantRunResult, error) {
	e.requests <- request
	return projectAssistantRunResult{}, nil
}

func (*initialProjectPromptCaptureEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("unexpected resume")
}

func (e replyStartRouteEngine) StreamProjectAssistant(_ context.Context, req projectAssistantRunRequest) (projectAssistantRunResult, error) {
	if e.chunk != "" {
		req.StreamCallbacks.OnChunk(e.chunk)
	}
	return projectAssistantRunResult{Content: e.reply}, nil
}

func (e *planStartRouteEngine) StreamProjectAssistant(_ context.Context, req projectAssistantRunRequest) (projectAssistantRunResult, error) {
	for _, plan := range e.plans {
		req.StreamCallbacks.OnPlan(plan)
	}
	close(e.published)
	<-e.release
	return projectAssistantRunResult{Content: "plan complete"}, nil
}

func (*planStartRouteEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("unexpected resume")
}

func (*planResumeRouteEngine) StreamProjectAssistant(context.Context, projectAssistantRunRequest) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("unexpected stream")
}

func (e *planResumeRouteEngine) ResumeProjectAssistant(_ context.Context, req projectAssistantRunRequest, _ projectAssistantResumeRequest, _ projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	for _, plan := range e.plans {
		req.StreamCallbacks.OnPlan(plan)
	}
	close(e.published)
	<-e.release
	return projectAssistantRunResult{Content: "plan complete"}, nil
}

func (replyStartRouteEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("unexpected resume")
}

type failingStartRouteEngine struct{}

func (failingStartRouteEngine) StreamProjectAssistant(context.Context, projectAssistantRunRequest) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("expected failure")
}

func (failingStartRouteEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("unexpected resume")
}

func (e *blockingStartRouteEngine) StreamProjectAssistant(ctx context.Context, _ projectAssistantRunRequest) (projectAssistantRunResult, error) {
	close(e.entered)
	<-ctx.Done()
	close(e.finished)
	return projectAssistantRunResult{}, context.Cause(ctx)
}

func (*blockingStartRouteEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("unexpected resume")
}

func (*blockingResumeRouteEngine) StreamProjectAssistant(context.Context, projectAssistantRunRequest) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("unexpected stream")
}

func (e *blockingResumeRouteEngine) ResumeProjectAssistant(ctx context.Context, _ projectAssistantRunRequest, _ projectAssistantResumeRequest, _ projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	close(e.entered)
	<-ctx.Done()
	close(e.finished)
	return projectAssistantRunResult{}, context.Cause(ctx)
}
