// Copyright 2026 The Railgrid Authors.
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
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/railgrid/provider-app-studio/store"
)

func assistantTurnFailureTestItem(t *testing.T, itemID, itemStatus string, action projectAssistantActionFeedItem) assistantThreadItem {
	t.Helper()
	data, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	return assistantThreadItem{ID: itemID, Type: assistantThreadEventDynamicToolCall, Status: itemStatus, Content: action.Title, Data: data}
}

func TestAssistantThreadTurnViewOmitsSummaryWithoutFailures(t *testing.T) {
	var toolItems assistantThreadTurnToolItems
	toolItems.observe("tool-a", assistantTurnFailureTestItem(t, "tool-a", "completed", projectAssistantActionFeedItem{
		ID: "call-a", Kind: projectAssistantActionFeedItemInspect, Status: projectAssistantActionFeedStatusSucceeded, Title: "Inspected project",
	}))
	// Non-tool items never count, even when failed.
	toolItems.observe("assistant-a", assistantThreadItem{ID: "assistant-a", Type: assistantThreadEventAssistantMessage, Status: "failed"})
	view := newAssistantThreadTurnView(store.AssistantTurn{ID: "turn-a", Status: store.AssistantTurnStatusCompleted}, &toolItems)
	if view.FailedItems != 0 || view.RecoveredItems != 0 || view.RejectedItems != 0 || len(view.Failures) != 0 {
		t.Fatalf("summary without failures = %#v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"failedItems"`, `"recoveredItems"`, `"rejectedItems"`, `"failures"`} {
		if strings.Contains(string(encoded), field) {
			t.Fatalf("turn without failures encoded %s: %s", field, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"status":"completed"`) || !strings.Contains(string(encoded), `"id":"turn-a"`) {
		t.Fatalf("turn view lost the canonical turn fields: %s", encoded)
	}
	if empty := newAssistantThreadTurnView(store.AssistantTurn{ID: "turn-empty"}, nil); empty.FailedItems != 0 || empty.Failures != nil {
		t.Fatalf("nil tool items summary = %#v", empty)
	}
}

func TestAssistantThreadTurnViewSummarizesFailedToolItems(t *testing.T) {
	var toolItems assistantThreadTurnToolItems
	for index := 0; index < assistantThreadTurnMaxFailures+1; index++ {
		itemID := fmt.Sprintf("tool-failed-%d", index)
		toolItems.observe(itemID, assistantTurnFailureTestItem(t, itemID, "failed", projectAssistantActionFeedItem{
			ID: fmt.Sprintf("call-failed-%d", index), Kind: projectAssistantActionFeedItemRun, Status: projectAssistantActionFeedStatusFailed, Title: "Run failed",
			Severity:   projectAssistantActionFeedSeverityError,
			Diagnostic: &projectAssistantActionDiagnostic{Category: "configuration", Message: fmt.Sprintf("failure %d", index), ReferenceID: fmt.Sprintf("ref-%d", index)},
		}))
	}
	// A failure relabeled as recovered by the action feed is not a failure.
	toolItems.observe("tool-recovered", assistantTurnFailureTestItem(t, "tool-recovered", "failed", projectAssistantActionFeedItem{
		ID: "call-recovered", Kind: projectAssistantActionFeedItemEdit, Status: projectAssistantActionFeedStatusFailed, Title: "Edit failed",
	}))
	toolItems.observe("tool-recovered", assistantTurnFailureTestItem(t, "tool-recovered", "in_progress", projectAssistantActionFeedItem{
		ID: "call-recovered", Kind: projectAssistantActionFeedItemEdit, Status: projectAssistantActionFeedStatusRecovered, Title: "Recovered file update",
	}))
	toolItems.observe("tool-ok", assistantTurnFailureTestItem(t, "tool-ok", "completed", projectAssistantActionFeedItem{
		ID: "call-ok", Kind: projectAssistantActionFeedItemEdit, Status: projectAssistantActionFeedStatusSucceeded, Title: "Edited files",
	}))
	// A step the user declined at an approval prompt is rejected, not failed,
	// even though its thread item reads failed.
	toolItems.observe("tool-rejected", assistantTurnFailureTestItem(t, "tool-rejected", "failed", projectAssistantActionFeedItem{
		ID: "call-rejected", Kind: projectAssistantActionFeedItemRun, Status: projectAssistantActionFeedStatusRejected, Title: "Promote rejected",
	}))

	view := newAssistantThreadTurnView(store.AssistantTurn{ID: "turn-b", Status: store.AssistantTurnStatusCompleted}, &toolItems)
	if view.Status != store.AssistantTurnStatusCompleted {
		t.Fatalf("summary changed turn status to %q", view.Status)
	}
	if view.FailedItems != assistantThreadTurnMaxFailures+1 || view.RecoveredItems != 1 || view.RejectedItems != 1 {
		t.Fatalf("failedItems=%d recoveredItems=%d rejectedItems=%d, want %d, 1 and 1", view.FailedItems, view.RecoveredItems, view.RejectedItems, assistantThreadTurnMaxFailures+1)
	}
	if len(view.Failures) != assistantThreadTurnMaxFailures {
		t.Fatalf("failures = %d entries, want cap %d", len(view.Failures), assistantThreadTurnMaxFailures)
	}
	want := assistantThreadTurnFailure{ItemID: "tool-failed-0", Title: "Run failed", Category: "configuration", Message: "failure 0", ReferenceID: "ref-0"}
	if view.Failures[0] != want {
		t.Fatalf("first failure = %#v, want %#v", view.Failures[0], want)
	}
}

func TestAssistantThreadTurnViewClosesPendingModelInputAtTerminal(t *testing.T) {
	var toolItems assistantThreadTurnToolItems
	pending := assistantTurnFailureTestItem(t, "model-input-a", "in_progress", projectAssistantActionFeedItem{
		ID: "call-image", Kind: projectAssistantActionFeedItemInspect, MediaKind: projectAssistantActionFeedMediaImage, Status: projectAssistantActionFeedStatusRunning, Title: "Viewing image",
	})
	pending.Type = assistantThreadEventModelInput
	toolItems.observe(pending.ID, pending)
	if running := newAssistantThreadTurnView(store.AssistantTurn{Status: store.AssistantTurnStatusInProgress}, &toolItems); running.FailedItems != 0 {
		t.Fatalf("in-progress turn counted a pending image as failed: %#v", running)
	}
	// Matches materializeAssistantThreadItems: an image never accepted before a
	// completed turn ended is presented, and therefore counted, as failed.
	if completed := newAssistantThreadTurnView(store.AssistantTurn{Status: store.AssistantTurnStatusCompleted}, &toolItems); completed.FailedItems != 1 {
		t.Fatalf("completed turn summary = %#v, want the unaccepted image counted", completed)
	}
	if interrupted := newAssistantThreadTurnView(store.AssistantTurn{Status: store.AssistantTurnStatusInterrupted}, &toolItems); interrupted.FailedItems != 0 {
		t.Fatalf("interrupted turn summary = %#v, want the image canceled, not failed", interrupted)
	}
}

// TestAssistantThreadTurnCompletedCarriesFailureSummary reproduces the field
// report: a turn completes after a file update failed (and was recovered) and
// preview inspection failed twice across a steered segment. Status stays
// completed, while turn.completed and the turn detail route both report the
// unrecovered failures.
func TestAssistantThreadTurnCompletedCarriesFailureSummary(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	server := newAssistantTurnDetailServer(messages)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	createAssistantThreadForHTTPTest(t, messages, scope, "thread-failures", "test-user")
	now := time.Now().UTC()
	run := store.AssistantRun{
		ID: "turn-failures", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantRunStatusRunning, ClientRequestID: "client-failures", UserMessageID: "user-failures",
		ActiveMessageID: "assistant-failures-1", Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := messages.CreateAssistantRun(ctx, scope, store.Message{ID: run.UserMessageID, Role: "user", ActorID: "test-user", Content: "build it", CreatedAt: now, UpdatedAt: now},
		store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}, run); err != nil {
		t.Fatal(err)
	}
	turn := store.AssistantTurn{ID: run.ID, ThreadID: "thread-failures", ActorID: "test-user", ClientUserMessageID: run.ClientRequestID,
		Mode: run.Mode, ApprovalMode: run.ApprovalMode, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	startedPayload, _ := json.Marshal(map[string]any{"turn": turn})
	if _, err := messages.CreateAssistantTurn(ctx, scope, turn, []store.AssistantThreadEvent{{Type: assistantThreadEventTurnStarted, Payload: startedPayload, CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}

	read := projectAssistantActionFeedItem{ID: "call-read", Kind: projectAssistantActionFeedItemInspect, Status: projectAssistantActionFeedStatusSucceeded, Title: "Inspected project", Severity: projectAssistantActionFeedSeverityNormal}
	staleEdit := projectAssistantActionFeedItem{ID: "call-stale", Kind: projectAssistantActionFeedItemEdit, Status: projectAssistantActionFeedStatusFailed, Title: "Edit failed", Severity: projectAssistantActionFeedSeverityError,
		Diagnostic: &projectAssistantActionDiagnostic{Category: "workspace", Message: "The source is stale.", ReferenceID: "ref-stale", Code: "stale_source"}}
	retryEdit := projectAssistantActionFeedItem{ID: "call-retry", Kind: projectAssistantActionFeedItemEdit, Status: projectAssistantActionFeedStatusSucceeded, Title: "Edited files", Severity: projectAssistantActionFeedSeverityNormal, RecoveryOf: staleEdit.ID}
	previewFailure := func(id, referenceID string) projectAssistantActionFeedItem {
		return projectAssistantActionFeedItem{ID: id, Kind: projectAssistantActionFeedItemRun, Status: projectAssistantActionFeedStatusFailed, Title: "Run failed", Severity: projectAssistantActionFeedSeverityError,
			Diagnostic: &projectAssistantActionDiagnostic{Category: "configuration", Message: "Private preview inspection is unavailable because RAILGRID_HUB_PUBLIC_URL is not configured.", ReferenceID: referenceID}}
	}
	firstInspect := previewFailure("call-inspect-1", "ref-inspect-1")
	secondInspect := previewFailure("call-inspect-2", "ref-inspect-2")
	feed := func(actions ...projectAssistantActionFeedItem) map[string]any {
		return map[string]any{projectMessageMetadataAssistantActionFeed: actions}
	}

	state := assistantThreadMirrorState{}
	project := func(current store.AssistantRun, message store.Message) {
		t.Helper()
		if err := server.projectAssistantThreadSnapshot(ctx, scope, turn.ThreadID, turn, run, &state, projectAssistantRunSnapshot{Run: current, Message: message}); err != nil {
			t.Fatal(err)
		}
	}
	first := store.Message{ID: "assistant-failures-1", Role: "assistant", CreatedAt: now}
	first.Metadata = feed(read, staleEdit)
	project(run, first)
	first.Metadata = feed(read, staleEdit, retryEdit, firstInspect)
	project(run, first)

	steered := run
	steered.ActiveMessageID = "assistant-failures-2"
	steered.Revision = 2
	second := store.Message{ID: steered.ActiveMessageID, Role: "assistant", Content: "Done.", CreatedAt: now, Metadata: feed(secondInspect)}
	project(steered, second)
	steered.Status = store.AssistantRunStatusCompleted
	project(steered, second)

	events, err := messages.ListAssistantThreadEvents(ctx, scope, turn.ThreadID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var completed *assistantThreadTurnView
	for _, event := range events {
		if event.Type != assistantThreadEventTurnCompleted {
			continue
		}
		var envelope struct {
			Turn assistantThreadTurnView `json:"turn"`
		}
		if err := json.Unmarshal(event.Payload, &envelope); err != nil {
			t.Fatal(err)
		}
		completed = &envelope.Turn
	}
	if completed == nil {
		t.Fatalf("no turn.completed event: %#v", events)
	}
	if completed.Status != store.AssistantTurnStatusCompleted {
		t.Fatalf("turn.completed status = %q, want completed", completed.Status)
	}
	wantFailures := []assistantThreadTurnFailure{
		{ItemID: assistantThreadDynamicToolItemID("assistant-failures-1", firstInspect.ID), Title: "Run failed", Category: "configuration", Message: firstInspect.Diagnostic.Message, ReferenceID: "ref-inspect-1"},
		{ItemID: assistantThreadDynamicToolItemID("assistant-failures-2", secondInspect.ID), Title: "Run failed", Category: "configuration", Message: secondInspect.Diagnostic.Message, ReferenceID: "ref-inspect-2"},
	}
	if completed.FailedItems != 2 || completed.RecoveredItems != 1 || !reflect.DeepEqual(completed.Failures, wantFailures) {
		t.Fatalf("turn.completed summary = failedItems %d recoveredItems %d failures %#v, want 2, 1, %#v", completed.FailedItems, completed.RecoveredItems, completed.Failures, wantFailures)
	}

	router := mux.NewRouter()
	server.Register(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, assistantTurnDetailHTTPTestRequest(http.MethodGet, "/api/projects/demo/assistant/threads/thread-failures/turns/turn-failures", "test-user"))
	if response.Code != http.StatusOK {
		t.Fatalf("turn detail status = %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"failedItems":2`) {
		t.Fatalf("turn detail body lacks failedItems: %s", response.Body.String())
	}
	var detail assistantThreadTurnDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.Turn.Status != store.AssistantTurnStatusCompleted || detail.Turn.FailedItems != completed.FailedItems ||
		detail.Turn.RecoveredItems != completed.RecoveredItems || !reflect.DeepEqual(detail.Turn.Failures, completed.Failures) {
		t.Fatalf("turn detail summary = %#v, want the turn.completed summary %#v", detail.Turn, *completed)
	}
}

// TestReconcileAssistantThreadTurnCarriesFailureSummary covers the restart
// path: the terminal event written by reconciliation summarizes the tool items
// that were durable before the provider lost the live mirror.
func TestReconcileAssistantThreadTurnCarriesFailureSummary(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	threadID, turnID := "thread-reconcile-failures", "turn-reconcile-failures"
	if _, err := inner.CreateAssistantThread(ctx, scope, store.AssistantThread{ID: threadID, ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	turn := store.AssistantTurn{ID: turnID, ThreadID: threadID, ActorID: "alice", ClientUserMessageID: "client", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantTurn(ctx, scope, turn, nil); err != nil {
		t.Fatal(err)
	}
	user := store.Message{ID: "user-reconcile-failures", Role: "user", ActorID: "alice", CreatedAt: now}
	assistant := store.Message{ID: "assistant-reconcile-failures", Role: "assistant", Content: "done", CreatedAt: now}
	run := store.AssistantRun{ID: turnID, Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, ClientRequestID: "client", UserMessageID: user.ID, ActiveMessageID: assistant.ID, Status: store.AssistantRunStatusCompleted, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	itemID := assistantThreadDynamicToolItemID(assistant.ID, "call-edit")
	failed := assistantTurnFailureTestItem(t, itemID, "failed", projectAssistantActionFeedItem{
		ID: "call-edit", Kind: projectAssistantActionFeedItemEdit, Status: projectAssistantActionFeedStatusFailed, Title: "Edit failed",
		Diagnostic: &projectAssistantActionDiagnostic{Category: "workspace", Message: "The source is stale.", ReferenceID: "ref-edit"},
	})
	failed.TurnID = turnID
	payload, _ := json.Marshal(map[string]any{"item": failed})
	if _, err := inner.AppendAssistantThreadEvent(ctx, scope, store.AssistantThreadEvent{ThreadID: threadID, TurnID: turnID, Type: assistantThreadEventItemCompleted, ItemID: itemID, Payload: payload}, 0); err != nil {
		t.Fatal(err)
	}

	if err := server.reconcileProjectAssistantThreadTurn(ctx, scope, turn); err != nil {
		t.Fatal(err)
	}
	events, err := inner.ListAssistantThreadEvents(ctx, scope, threadID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type != assistantThreadEventTurnCompleted {
			continue
		}
		found = true
		var envelope struct {
			Turn assistantThreadTurnView `json:"turn"`
		}
		if err := json.Unmarshal(event.Payload, &envelope); err != nil {
			t.Fatal(err)
		}
		want := []assistantThreadTurnFailure{{ItemID: itemID, Title: "Edit failed", Category: "workspace", Message: "The source is stale.", ReferenceID: "ref-edit"}}
		if envelope.Turn.Status != store.AssistantTurnStatusCompleted || envelope.Turn.FailedItems != 1 || !reflect.DeepEqual(envelope.Turn.Failures, want) {
			t.Fatalf("reconciled turn.completed = %#v, want completed with one failure %#v", envelope.Turn, want)
		}
	}
	if !found {
		t.Fatalf("reconciliation wrote no turn.completed event: %#v", events)
	}
}
