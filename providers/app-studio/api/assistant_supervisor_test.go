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
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/workspace"
	"github.com/gorilla/mux"
)

func TestProjectAssistantSupervisorOwnsExecutionAfterStarterCancellation(t *testing.T) {
	supervisor := newProjectAssistantSupervisor(context.Background(), store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", Content: "hello", CreatedAt: run.CreatedAt, UpdatedAt: run.CreatedAt}
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

func TestProjectAssistantSupervisorPersistsAndQueuesSteeringOnActiveRun(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := bindProjectAssistantStartRequest(&run, "test-user", "build it"); err != nil {
		t.Fatal(err)
	}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "test-user", Content: "build it", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now.Add(time.Microsecond), UpdatedAt: now.Add(time.Microsecond)}
	if _, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	if err := supervisor.Start(context.Background(), scope, run, assistant, func(ctx context.Context, _ *projectAssistantSnapshotAccumulator) {
		close(started)
		<-ctx.Done()
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	defer supervisor.Abort(scope, run.ID)
	accumulator := supervisor.accumulatorFor(scope, run.ID)
	if err := accumulator.UpdateMessage(context.Background(), "first partial", projectAssistantDurableMetadataForTransition(run, "Working", true, false, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.UpdateText(context.Background(), "first persisted partial", false); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.UpdateText(context.Background(), "latest throttled partial", false); err != nil {
		t.Fatal(err)
	}
	beforeSteering, err := memoryStore.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}

	updated, steeringMessage, _, handled, err := supervisor.EnqueueSteering(
		context.Background(), scope, run.ID, "test-user", "also add tests", "steer-1", store.AssistantRunModeDefault,
	)
	if err != nil || !handled {
		t.Fatalf("EnqueueSteering handled=%v err=%v", handled, err)
	}
	if updated.ID != run.ID || updated.Revision != beforeSteering.Revision {
		t.Fatalf("queued run = %#v, want unchanged output revision %d", updated, beforeSteering.Revision)
	}
	persisted, err := memoryStore.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil || persisted.Revision != updated.Revision {
		t.Fatalf("persisted run = %#v err=%v", persisted, err)
	}
	persistedMessages, err := memoryStore.ListMessages(context.Background(), scope, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	var persistedMessage store.Message
	for _, message := range persistedMessages.Items {
		if message.ID == steeringMessage.ID {
			persistedMessage = message
			break
		}
	}
	if persistedMessage.Content != "also add tests" {
		t.Fatalf("persisted steering message = %#v", persistedMessage)
	}
	if got := len(persistedMessages.Items); got != 3 {
		t.Fatalf("persisted message count = %d, want user receipt before boundary rotation", got)
	}
	for index, role := range []string{"user", "assistant", "user"} {
		if persistedMessages.Items[index].Role != role {
			t.Fatalf("persisted role[%d] = %q, want %q", index, persistedMessages.Items[index].Role, role)
		}
	}
	priorAssistant := persistedMessages.Items[1]
	if priorAssistant.Content != "first persisted partial" || priorAssistant.Metadata[projectAssistantMetadataWorkingStatus] != "Working" {
		t.Fatalf("in-flight assistant segment changed before safe boundary: %#v", priorAssistant)
	}
	var steeringInput projectAssistantSteeringInput
	select {
	case input := <-supervisor.Steering(scope, run.ID):
		if input.MessageID != steeringMessage.ID || input.Content != steeringMessage.Content {
			t.Fatalf("steering input = %#v", input)
		}
		steeringInput = input
	default:
		t.Fatal("steering input was not queued")
	}
	if err := supervisor.ActivateSteering(context.Background(), scope, run.ID, []projectAssistantSteeringInput{steeringInput}); err != nil {
		t.Fatalf("ActivateSteering: %v", err)
	}
	updated, err = memoryStore.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil || updated.Revision != beforeSteering.Revision+1 {
		t.Fatalf("activated run = %#v err=%v, want revision %d", updated, err, beforeSteering.Revision+1)
	}
	persistedMessages, err = memoryStore.ListMessages(context.Background(), scope, 50, "")
	if err != nil || len(persistedMessages.Items) != 4 {
		t.Fatalf("activated message stream count=%d err=%v, want four", len(persistedMessages.Items), err)
	}
	for index, role := range []string{"user", "assistant", "user", "assistant"} {
		if persistedMessages.Items[index].Role != role {
			t.Fatalf("activated role[%d] = %q, want %q", index, persistedMessages.Items[index].Role, role)
		}
	}
	priorAssistant = persistedMessages.Items[1]
	if priorAssistant.Metadata[projectAssistantMetadataWorkingStatus] == "Working" || priorAssistant.Metadata[projectAssistantMetadataProvisional] != false {
		t.Fatalf("assistant segment was not closed at safe boundary: %#v", priorAssistant)
	}

	replayed, replayedMessage, _, handled, err := supervisor.EnqueueSteering(
		context.Background(), scope, run.ID, "test-user", "also add tests", "steer-1", store.AssistantRunModeDefault,
	)
	if err != nil || !handled || replayed.Revision != updated.Revision || replayedMessage.ID != steeringMessage.ID {
		t.Fatalf("idempotent replay run=%#v message=%#v handled=%v err=%v", replayed, replayedMessage, handled, err)
	}
	if _, _, _, handled, err := supervisor.EnqueueSteering(
		context.Background(), scope, run.ID, "other-user", "inject", "steer-other", store.AssistantRunModeDefault,
	); !handled || !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("cross-actor steering handled=%v err=%v, want conflict", handled, err)
	}
	if _, _, _, handled, err := supervisor.EnqueueSteering(
		context.Background(), scope, "run-other", "test-user", "misdirected", "steer-other-run", store.AssistantRunModeDefault,
	); handled || err != nil {
		t.Fatalf("wrong-run steering handled=%v err=%v, want unhandled", handled, err)
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	supervisor.mu.Lock()
	supervisor.runs[key].steeringReceipts = map[string]store.Message{}
	supervisor.mu.Unlock()
	replayed, replayedMessage, _, handled, err = supervisor.EnqueueSteering(
		context.Background(), scope, run.ID, "test-user", "also add tests", "steer-1", store.AssistantRunModeDefault,
	)
	if err != nil || !handled || replayed.Revision != updated.Revision || replayedMessage.ID != steeringMessage.ID {
		t.Fatalf("reattached durable replay run=%#v message=%#v handled=%v err=%v", replayed, replayedMessage, handled, err)
	}
	persistedMessages, err = memoryStore.ListMessages(context.Background(), scope, 50, "")
	if err != nil || len(persistedMessages.Items) != 4 {
		t.Fatalf("durable replay duplicated messages: count=%d err=%v", len(persistedMessages.Items), err)
	}
	if !supervisor.SealSteering(scope, run.ID) {
		t.Fatal("SealSteering did not close an empty steering boundary")
	}
	_, _, _, handled, err = supervisor.EnqueueSteering(
		context.Background(), scope, run.ID, "test-user", "too late", "steer-2", store.AssistantRunModeDefault,
	)
	if !handled || !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("post-seal steering handled=%v err=%v, want conflict", handled, err)
	}
}

func TestProjectAssistantSupervisorShutdownLogsOneInterruptedTerminalTransition(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	var interruptedCount int
	supervisor.lifecycleLog = func(event string, gotScope store.Scope, gotRun store.AssistantRun) {
		if event == "interrupted" {
			interruptedCount++
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
	if interruptedCount != 1 {
		t.Fatalf("interrupted lifecycle events = %d, want one", interruptedCount)
	}
}

func TestProjectAssistantSupervisorReservationProtectsFreshDurableRunUntilAttach(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	server := NewWithWorkspace(nil, memoryStore, nil, "", false)
	server.assistantSupervisor = supervisor
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	release, err := supervisor.Reserve(scope)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	defer release()
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", Content: "hello", CreatedAt: now, UpdatedAt: now}
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

func TestProjectAssistantReconcilesOrphanedConversationRun(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	stale := store.AssistantRun{ID: "run-stale", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-stale", UserMessageID: "user-stale", ActiveMessageID: "assistant-stale", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := messages.CreateAssistantRun(context.Background(), scope,
		store.Message{ID: stale.UserMessageID, Role: "user", ActorID: "test-user", Content: "stale", CreatedAt: now, UpdatedAt: now},
		store.Message{ID: stale.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}, stale,
	); err != nil {
		t.Fatalf("CreateAssistantRun stale: %v", err)
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

	started, err := server.startProjectAssistantRunDurablyWithMode(context.Background(), scope, "test-user", "new conversation", "request-new", store.AssistantRunModeDefault, func(store.AssistantRun, store.Message, bool) error { return nil })
	if err != nil {
		t.Fatalf("startProjectAssistantRunDurably after reconciliation: %v", err)
	}
	if !started.Started || started.Run.ClientRequestID != "request-new" {
		t.Fatalf("started run = %#v, want a new started conversation", started)
	}
}

func TestProjectAssistantSupervisorReservationReleaseAllowsRetryAfterStartFailure(t *testing.T) {
	supervisor := newProjectAssistantSupervisor(context.Background(), store.NewMemoryStore())
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", Content: "hello", CreatedAt: run.CreatedAt, UpdatedAt: run.CreatedAt}
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", ActorID: "test-user", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
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
	active := supervisor.runs[projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}]
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", ActorID: "test-user", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", Content: "hello", CreatedAt: now, UpdatedAt: now}
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", Content: "hello", CreatedAt: now, UpdatedAt: now}
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
	if got.Status != store.AssistantRunStatusInterrupted {
		t.Fatalf("status = %q, want interrupted", got.Status)
	}
}

func TestProjectAssistantSupervisorAbortPersistsAuditAndClearsPendingInterrupt(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusPendingPermission, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", RequestID: "permission-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", Metadata: map[string]any{
		projectAssistantMetadataWorkingStatus:    projectMessageStatusPendingPermission,
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
	if got.Status != store.AssistantRunStatusInterrupted {
		t.Fatalf("status = %q, want interrupted", got.Status)
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now}
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusPendingInput, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now}
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now}
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusPendingPermission, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now}
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusPendingInput, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now}
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

func TestProjectAssistantSupervisorQueuesSteeringWhilePermissionIsPending(t *testing.T) {
	memory := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memory)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusPendingPermission, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", RequestID: "permission-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := bindProjectAssistantStartRequest(&run, "test-user", "inspect it"); err != nil {
		t.Fatal(err)
	}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "test-user", Content: "inspect it", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := memory.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Attach(scope, created, assistant); err != nil {
		t.Fatal(err)
	}
	queued, receipt, currentAssistant, handled, err := supervisor.EnqueueSteering(
		context.Background(), scope, run.ID, "test-user", "also inspect tests", "steer-pending", store.AssistantRunModePlan,
	)
	if err != nil || !handled {
		t.Fatalf("pending EnqueueSteering handled=%v err=%v", handled, err)
	}
	if queued.Status != store.AssistantRunStatusPendingPermission || queued.Revision != run.Revision || currentAssistant.ID != assistant.ID {
		t.Fatalf("pending steering rotated output before resume: run=%#v assistant=%#v", queued, currentAssistant)
	}
	select {
	case input := <-supervisor.Steering(scope, run.ID):
		if input.MessageID != receipt.ID || input.Content != "also inspect tests" {
			t.Fatalf("queued pending steering = %#v", input)
		}
	default:
		t.Fatal("pending steering was not queued for the resumed sampling boundary")
	}
}

func TestProjectAssistantSupervisorQueuesApprovalDuringPendingWorkerHandoff(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "test-user", Content: "restart it", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}

	pendingPublished := make(chan struct{})
	releasePendingWorker := make(chan struct{})
	if err := supervisor.Start(context.Background(), scope, created, assistant, func(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator) {
		if err := accumulator.UpdateRun(ctx, func(current *store.AssistantRun) {
			current.Status = store.AssistantRunStatusPendingPermission
			current.RequestID = "perm-1"
		}); err != nil {
			t.Errorf("publish pending approval: %v", err)
		}
		close(pendingPublished)
		<-releasePendingWorker
	}); err != nil {
		t.Fatal(err)
	}
	<-pendingPublished

	resumed := make(chan struct{})
	if err := supervisor.Start(context.Background(), scope, created, assistant, func(context.Context, *projectAssistantSnapshotAccumulator) {
		close(resumed)
	}); err != nil {
		t.Fatalf("queue approval during worker handoff: %v", err)
	}
	select {
	case <-resumed:
		t.Fatal("resumed worker overlapped the pending worker")
	default:
	}
	if err := supervisor.Start(context.Background(), scope, created, assistant, func(context.Context, *projectAssistantSnapshotAccumulator) {}); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("duplicate queued approval error = %v, want conflict", err)
	}

	close(releasePendingWorker)
	select {
	case <-resumed:
	case <-time.After(time.Second):
		t.Fatal("queued approval did not acquire worker ownership")
	}
}

func TestProjectAssistantSupervisorClaimPublishesRunningRevision(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memoryStore)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusPendingPermission, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", RequestID: "permission-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", Metadata: map[string]any{
		projectMessageMetadataAssistantActionFeed:    []projectAssistantActionFeedItem{{ID: "prior", Status: "succeeded", Title: "Wrote file"}},
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
		if len(projectAssistantActionFeedFromMetadata(message.Metadata[projectMessageMetadataAssistantActionFeed])) != 1 {
			t.Fatalf("claimed metadata lost prior action: %#v", message.Metadata)
		}
		break
	}
	if err := accumulator.UpdateSnapshot(context.Background(), func(current *store.AssistantRun, message *store.Message) {
		next := *current
		next.Revision++
		message.Metadata = projectAssistantDurableMetadataForTransition(next, "Writing files", false, true, []projectToolCallStreamEvent{{ID: "tool-1", Name: projectToolApplyPatch, Status: "succeeded"}}, nil)
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
		if _, ok := message.Metadata[projectMessageMetadataAssistantActionFeed]; !ok {
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Checkpoint: json.RawMessage(`{"permission":"stale"}`), Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now}
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
	state := &projectAssistantDurableMetadataState{status: "Writing files", toolCalls: []projectToolCallStreamEvent{{ID: "tool-1", Name: projectToolApplyPatch, Status: "succeeded"}}}
	server := NewWithWorkspace(nil, msgStore, nil, "", false)
	if err := server.persistProjectAssistantDurableMetadata(context.Background(), accumulator, workspace.Scope{}, state, nil); err != nil {
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
	if len(terminal.Run.Checkpoint) != 0 {
		t.Fatalf("terminal checkpoint = %s, want cleared with terminal status", terminal.Run.Checkpoint)
	}
	if _, ok := terminal.Message.Metadata[projectMessageMetadataAssistantActionFeed]; !ok {
		t.Fatalf("terminal metadata lost actions: %#v", terminal.Message.Metadata)
	}
	persisted, err := msgStore.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Checkpoint) != 0 {
		t.Fatalf("persisted terminal checkpoint = %s, want cleared", persisted.Checkpoint)
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now}
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

func TestConversationPersistenceFailureTerminalizesWorkerOwnedRun(t *testing.T) {
	memory := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), memory)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := bindProjectAssistantStartRequest(&run, "test-user", "build it"); err != nil {
		t.Fatal(err)
	}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "test-user", Content: "build it", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if _, err := memory.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	accumulator, err := supervisor.Attach(scope, run, assistant)
	if err != nil {
		t.Fatal(err)
	}
	accumulator.FailPersistence(errors.New("conversation append unavailable"))
	persisted, err := memory.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != store.AssistantRunStatusFailed || len(persisted.Error) == 0 {
		t.Fatalf("conversation persistence failure left nonterminal run: %#v", persisted)
	}
}

func TestProjectAssistantThreadStartConsumesServerOwnedInitialBootstrap(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	secret, err := json.Marshal(projectLLMSettingsSecret(settings).Object)
	if err != nil {
		t.Fatal(err)
	}
	projectYAML := "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\n  uid: test-project-uid-demo\nspec: {}\n"
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.Contains(request.Query, "ProjectYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": projectYAML}}}})
		case strings.Contains(request.Query, "applyYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"applyYaml": projectYAML}})
		case strings.Contains(request.Query, "SecretYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"v1": map[string]any{"SecretYaml": string(secret)}}})
		default:
			t.Fatalf("unexpected GraphQL query: %s", request.Query)
		}
	}))
	defer graphQL.Close()

	messages := store.NewMemoryStore()
	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), messages, nil, "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	if err := messages.CreateProjectBootstrapPermit(context.Background(), scope, "test-user", projectInitialBootstrapPromptDigest("build a todo app")); err != nil {
		t.Fatal(err)
	}
	engine := &initialProjectBootstrapCaptureEngine{requests: make(chan projectAssistantRunRequest, 2)}
	server.assistantEngine = engine
	router := mux.NewRouter()
	server.Register(router)

	post := func(threadID, body string) {
		createAssistantThreadForHTTPTest(t, messages, scope, threadID, "test-user")
		request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/threads/"+threadID+"/turns", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer caller-token")
		request.Header.Set("X-Kedge-User", "test-user")
		request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
		request.Header.Set("X-Kedge-Cluster", "cluster-a")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("start status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
		}
	}

	post("thread-1", `{"content":"build a todo app","clientUserMessageID":"request-1","collaborationMode":"default"}`)
	select {
	case request := <-engine.requests:
		if request.InitialApprovedPlan == nil ||
			!request.InitialApprovedPlan.RunLocal ||
			request.InitialApprovedPlan.ApprovalTool != "project_create_prompt" ||
			request.InitialApprovedPlan.Goal != "build a todo app" {
			t.Fatalf("initial bootstrap authority = %#v", request.InitialApprovedPlan)
		}
	case <-time.After(time.Second):
		t.Fatal("first durable run did not reach assistant engine")
	}
	deadline := time.Now().Add(time.Second)
	for {
		latest, latestErr := messages.LatestAssistantRun(context.Background(), scope)
		if latestErr == nil && assistantRunTerminal(latest.Status) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first durable run did not become terminal: run=%#v err=%v", latest, latestErr)
		}
		time.Sleep(5 * time.Millisecond)
	}

	post("thread-2", `{"content":"continue","clientUserMessageID":"request-2","collaborationMode":"default"}`)
	select {
	case request := <-engine.requests:
		if request.InitialApprovedPlan != nil {
			t.Fatalf("later durable run received initial-project grant: %#v", request.InitialApprovedPlan)
		}
	case <-time.After(time.Second):
		t.Fatal("later durable run did not reach assistant engine")
	}
}

func TestProjectAssistantRunStartInitialBootstrapSeesTranscriptAfterReservation(t *testing.T) {
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	messages := store.NewMemoryStore()
	observingStore := &reservationObservingStore{Store: messages, scope: scope}
	server := NewWithWorkspace(nil, observingStore, nil, "", false)
	observingStore.supervisor = server.projectAssistantSupervisor()
	now := time.Now().UTC()
	if err := messages.AppendMessage(context.Background(), scope, store.Message{ID: "prior-user", Role: "user", ActorID: "test-user", Content: "already started", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var transcriptEmpty bool
	_, err := server.startProjectAssistantRunDurablyWithMode(context.Background(), scope, "test-user", "continue", "request-after-prior", store.AssistantRunModeDefault, func(_ store.AssistantRun, _ store.Message, empty bool) error {
		transcriptEmpty = empty
		return nil
	})
	if err != nil {
		t.Fatalf("startProjectAssistantRunDurablyWithMode: %v", err)
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
			"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\n  uid: test-project-uid-demo\nspec: {}\n"}},
		}})
	}))
	defer graphQL.Close()
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), memoryStore, nil, "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", ActorID: "test-user", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-1", Role: "assistant", Content: "working", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	createAssistantThreadForHTTPTest(t, memoryStore, scope, "thread-1", "test-user")
	createAssistantTurnForHTTPTest(t, memoryStore, scope, "thread-1", run)
	router := mux.NewRouter()
	server.Register(router)
	request := httptest.NewRequest(http.MethodGet, "/api/projects/demo/assistant/threads/thread-1/events", nil)
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-User", "test-user")
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
	var event store.AssistantThreadEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != assistantThreadEventItemCompleted {
		t.Fatalf("first streamed event = %q, want %q", event.Type, assistantThreadEventItemCompleted)
	}
	canonical, err := memoryStore.GetAssistantTurn(context.Background(), scope, "thread-1", run.ID)
	if err != nil || canonical.Status != store.AssistantTurnStatusInterrupted {
		t.Fatalf("reconciled turn = %#v err=%v, want interrupted", canonical, err)
	}
}

func TestProjectAssistantThreadInterruptReattachesPendingRun(t *testing.T) {
	projectYAML := "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\n  uid: test-project-uid-demo\nspec: {}\n"
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
			"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": projectYAML}},
		}})
	}))
	defer graphQL.Close()

	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), memoryStore, nil, "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	run := store.AssistantRun{
		ID: "run-pending", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantRunStatusPendingPermission, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1",
		RequestID: "perm-1", Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := bindProjectAssistantStopRequest(&run, "test-user", "stop-1"); err != nil {
		t.Fatal(err)
	}
	user := store.Message{ID: run.UserMessageID, ActorID: "test-user", Role: "user", Content: "restart it", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Content: "Waiting for approval", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	createAssistantThreadForHTTPTest(t, memoryStore, scope, "thread-1", "test-user")
	createAssistantTurnForHTTPTest(t, memoryStore, scope, "thread-1", run)
	router := mux.NewRouter()
	server.Register(router)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/threads/thread-1/turns/run-pending/interrupt", strings.NewReader(`{"clientRequestID":"stop-1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-User", "test-user")
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	stopped, err := memoryStore.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != store.AssistantRunStatusInterrupted {
		t.Fatalf("run status = %q, want interrupted", stopped.Status)
	}
}

func TestProjectAssistantThreadMirrorPublishesPendingApproval(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(nil, memoryStore, nil, "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	run := store.AssistantRun{
		ID: "run-approval", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantRunStatusRunning, ClientRequestID: "client-request", UserMessageID: "user-1", ActiveMessageID: "assistant-1",
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	message := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "test-user", Content: "restart it", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, message, run); err != nil {
		t.Fatal(err)
	}
	createAssistantThreadForHTTPTest(t, memoryStore, scope, "thread-approval", "test-user")
	createAssistantTurnForHTTPTest(t, memoryStore, scope, "thread-approval", run)
	canonicalTurn, err := memoryStore.GetAssistantTurn(context.Background(), scope, "thread-approval", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	pendingPublished := make(chan struct{})
	publishInterrupt := make(chan struct{})
	if err := server.projectAssistantSupervisor().Start(context.Background(), scope, run, message, func(ctx context.Context, accumulator *projectAssistantSnapshotAccumulator) {
		if err := accumulator.UpdateRun(ctx, func(current *store.AssistantRun) {
			current.Status = store.AssistantRunStatusPendingPermission
			current.RequestID = "perm-1"
		}); err != nil {
			t.Errorf("publish pending approval: %v", err)
		}
		if err := accumulator.UpdateText(ctx, "Waiting for approval", false); err != nil {
			t.Errorf("publish pending approval text: %v", err)
		}
		close(pendingPublished)
		<-publishInterrupt
		if err := accumulator.UpdateSnapshot(ctx, func(_ *store.AssistantRun, current *store.Message) {
			current.Metadata = map[string]any{
				projectMessageMetadataAssistantInterrupt: projectAssistantUIInterruptRequest{
					InterruptID: "perm-1",
					Kind:        projectAssistantInterruptTypePermission,
					Description: "Restart the development runtime.",
					Status:      "pending",
					Action: &projectAssistantUIInterruptAction{
						RunID:              run.ID,
						RequestID:          "perm-1",
						AssistantMessageID: run.ActiveMessageID,
					},
				},
			}
		}); err != nil {
			t.Errorf("publish pending approval interrupt: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	go server.mirrorAssistantRunIntoThread(scope, "thread-approval", canonicalTurn, run)
	select {
	case <-pendingPublished:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not publish pending approval")
	}
	deadline := time.Now().Add(time.Second)
	for {
		events, err := memoryStore.ListAssistantThreadEvents(context.Background(), scope, "thread-approval", 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		processedPendingSnapshot := false
		for _, event := range events {
			if event.Type == assistantThreadEventApprovalRequested {
				t.Fatalf("approval published before its interrupt payload: %#v", event)
			}
			if event.Type == assistantThreadEventItemDelta && event.ItemID == run.ActiveMessageID {
				processedPendingSnapshot = true
			}
		}
		if processedPendingSnapshot {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mirror did not process pending snapshot without interrupt: %#v", events)
		}
		time.Sleep(time.Millisecond)
	}
	close(publishInterrupt)
	deadline = time.Now().Add(time.Second)
	approvalPublished := false
	for {
		events, err := memoryStore.ListAssistantThreadEvents(context.Background(), scope, "thread-approval", 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Type != assistantThreadEventApprovalRequested {
				continue
			}
			if event.RequestID != "perm-1" || event.ItemID != "perm-1" {
				t.Fatalf("approval event = %#v", event)
			}
			var envelope struct {
				Interrupt *projectAssistantUIInterruptRequest `json:"interrupt"`
			}
			if err := json.Unmarshal(event.Payload, &envelope); err != nil {
				t.Fatal(err)
			}
			interrupt := envelope.Interrupt
			if interrupt == nil || interrupt.Action == nil || interrupt.Action.RunID != run.ID || interrupt.Action.RequestID != "perm-1" {
				t.Fatalf("approval interrupt = %#v, want actionable request", interrupt)
			}
			items := materializeAssistantThreadItems(events)
			for _, item := range items {
				if item.ID == "perm-1" && item.Type == "approval" {
					if item.Status == "completed" {
						return
					}
					if item.Status == "in_progress" && !approvalPublished {
						approvalPublished = true
						if _, stopped, stopErr := server.projectAssistantSupervisor().Stop(scope, run.ID); stopErr != nil || !stopped {
							t.Fatalf("stop pending approval: stopped=%v err=%v", stopped, stopErr)
						}
					}
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("canonical approval did not complete after terminal transition: %#v", events)
		}
		time.Sleep(time.Millisecond)
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
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\n  uid: test-project-uid-demo\nspec: {}\n"}}}})
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	createAssistantThreadForHTTPTest(t, memoryStore, scope, "thread-1", "test-user")
	router := mux.NewRouter()
	server.Register(router)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/threads/thread-1/turns", strings.NewReader(`{"content":"finish the plan","clientUserMessageID":"plan-request","collaborationMode":"plan"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-User", "test-user")
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
	var started assistantThreadTurnStartResponse
	if err := json.NewDecoder(response.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe, err := server.projectAssistantSupervisor().Subscribe(scope, started.Turn.ID, 0)
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
	if got := live.Message.Metadata[projectAssistantMetadataWorkingStatus]; got != "Building · 1 of 2 steps" {
		t.Fatalf("live plan status = %#v, want synchronized completed count", got)
	}
	if live.Run.Revision <= 1 {
		t.Fatalf("live revision = %d, want greater than initial revision", live.Run.Revision)
	}

	close(engine.release)
	var terminal store.AssistantRun
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		terminal, err = memoryStore.GetAssistantRun(context.Background(), scope, started.Turn.ID)
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

func TestProjectAssistantWorkerPersistsCodexTerminalContract(t *testing.T) {
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
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\n  uid: test-project-uid-demo\nspec: {}\n"}}}})
		case strings.Contains(request.Query, "SecretYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"v1": map[string]any{"SecretYaml": string(secret)}}})
		default:
			t.Fatalf("unexpected GraphQL query: %s", request.Query)
		}
	}))
	defer graphQL.Close()

	tests := []struct {
		name        string
		err         error
		wantStatus  store.AssistantRunStatus
		wantAbort   store.AssistantRunAbortReason
		wantErrInfo string
	}{
		{name: "provider failure", err: errors.New("provider unavailable"), wantStatus: store.AssistantRunStatusFailed, wantErrInfo: "other"},
		{name: "iteration limit", err: adk.ErrExceedMaxIterations, wantStatus: store.AssistantRunStatusFailed, wantAbort: store.AssistantRunAbortReasonIterationLimited, wantErrInfo: "max_iterations_exceeded"},
		{name: "rollout budget", err: &projectAssistantSessionBudgetExceededError{LimitTokens: 100, WeightedTokensUsed: 101}, wantStatus: store.AssistantRunStatusFailed, wantAbort: store.AssistantRunAbortReasonBudgetLimited, wantErrInfo: "session_budget_exceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			memoryStore := store.NewMemoryStore()
			server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), memoryStore, nil, "", false)
			server.assistantEngine = terminalStartRouteEngine{err: tt.err}
			scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
			createAssistantThreadForHTTPTest(t, memoryStore, scope, "thread-1", "test-user")
			router := mux.NewRouter()
			server.Register(router)
			request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/threads/thread-1/turns", strings.NewReader(`{"content":"answer this","clientUserMessageID":"terminal-request","collaborationMode":"default"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer caller-token")
			request.Header.Set("X-Kedge-User", "test-user")
			request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
			request.Header.Set("X-Kedge-Cluster", "cluster-a")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusAccepted {
				t.Fatalf("start status = %d, want %d: %s", response.Code, http.StatusAccepted, response.Body.String())
			}

			deadline := time.Now().Add(time.Second)
			for {
				run, getErr := memoryStore.LatestAssistantRun(context.Background(), scope)
				if getErr == nil && assistantRunTerminal(run.Status) {
					if run.Status != tt.wantStatus || run.AbortReason != tt.wantAbort {
						t.Fatalf("terminal run = %#v, want status %q abort %q", run, tt.wantStatus, tt.wantAbort)
					}
					var terminalError projectAssistantRunErrorView
					if json.Unmarshal(run.Error, &terminalError) != nil || terminalError.ErrorInfo != tt.wantErrInfo {
						t.Fatalf("terminal error = %s, want errorInfo %q", run.Error, tt.wantErrInfo)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("assistant run did not become terminal: run=%#v err=%v", run, getErr)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestProjectAssistantSupervisorResumesFreeTextAndPersistsLatestPlanSnapshot(t *testing.T) {
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
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": "apiVersion: ai.kedge.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\n  uid: test-project-uid-demo\nspec: {}\n"}}}})
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	checkpoint, err := json.Marshal(projectAssistantCheckpointState{Eino: &projectAssistantEinoCheckpointState{
		CheckpointID: "run-1", Checkpoint: []byte("checkpoint"), InterruptID: "interrupt-1", InterruptType: projectAssistantInterruptTypeFollowUp, ToolCallID: "tool-1", ToolName: projectToolAskFollowUp,
	}})
	if err != nil {
		t.Fatal(err)
	}
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusPendingInput, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", RequestID: "follow-up-1", Checkpoint: checkpoint, Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	createAssistantThreadForHTTPTest(t, memoryStore, scope, "thread-1", "test-user")
	createAssistantTurnForHTTPTest(t, memoryStore, scope, "thread-1", run)
	router := mux.NewRouter()
	server.Register(router)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/threads/thread-1/turns/run-1/input", strings.NewReader(`{"requestID":"follow-up-1","answer":"Continue with the plan."}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-User", "test-user")
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d, want %d: %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	mirrorKey := assistantThreadMirrorKey(scope, "thread-1", run.ID)
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		_, mirrorActive := server.assistantThreadMirrors[mirrorKey]
		server.mu.Unlock()
		if mirrorActive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("thread resume did not launch its assistant mirror")
		}
		time.Sleep(time.Millisecond)
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
	if got := live.Message.Metadata[projectAssistantMetadataWorkingStatus]; got != "Building · 1 of 2 steps" {
		t.Fatalf("resumed live plan status = %#v, want synchronized completed count", got)
	}
	if live.Run.Revision <= run.Revision {
		t.Fatalf("live revision = %d, want greater than initial revision %d", live.Run.Revision, run.Revision)
	}

	close(engine.release)
	var terminal store.AssistantRun
	deadline = time.Now().Add(time.Second)
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
	if len(terminal.Checkpoint) != 0 {
		t.Fatalf("terminal checkpoint = %s, want stale resume checkpoint cleared", terminal.Checkpoint)
	}
	if terminal.Revision <= live.Run.Revision {
		t.Fatalf("terminal revision = %d, want greater than live revision %d", terminal.Revision, live.Run.Revision)
	}
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

type initialProjectBootstrapCaptureEngine struct {
	requests chan projectAssistantRunRequest
}

type reservationObservingStore struct {
	store.Store
	supervisor          *projectAssistantSupervisor
	scope               store.Scope
	observedReservation bool
}

type failingResumeRouteEngine struct {
	cause error
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

func (e *initialProjectBootstrapCaptureEngine) StreamProjectAssistant(_ context.Context, request projectAssistantRunRequest) (projectAssistantRunResult, error) {
	e.requests <- request
	return projectAssistantRunResult{}, nil
}

func (*initialProjectBootstrapCaptureEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
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

func (failingResumeRouteEngine) StreamProjectAssistant(context.Context, projectAssistantRunRequest) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("unexpected stream")
}

func (e failingResumeRouteEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, e.cause
}

func (replyStartRouteEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("unexpected resume")
}

type failingStartRouteEngine struct{}

type terminalStartRouteEngine struct{ err error }

func (e terminalStartRouteEngine) StreamProjectAssistant(context.Context, projectAssistantRunRequest) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, e.err
}

func (terminalStartRouteEngine) ResumeProjectAssistant(context.Context, projectAssistantRunRequest, projectAssistantResumeRequest, projectAssistantCheckpointState) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("unexpected resume")
}

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

func createAssistantThreadForHTTPTest(t *testing.T, messages store.Store, scope store.Scope, threadID, actor string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := messages.CreateAssistantThread(context.Background(), scope, store.AssistantThread{
		ID: threadID, ActorID: actor, Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
	}, nil); err != nil {
		t.Fatalf("create assistant thread: %v", err)
	}
}

func createAssistantTurnForHTTPTest(t *testing.T, messages store.Store, scope store.Scope, threadID string, run store.AssistantRun) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := messages.CreateAssistantTurn(context.Background(), scope, store.AssistantTurn{
		ID: run.ID, ThreadID: threadID, ActorID: "test-user", ClientUserMessageID: run.ClientRequestID,
		Mode: run.Mode, ApprovalMode: run.ApprovalMode, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now,
	}, nil); err != nil {
		t.Fatalf("create assistant turn: %v", err)
	}
}
