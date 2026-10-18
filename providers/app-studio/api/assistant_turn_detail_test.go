/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/store"
)

type assistantTurnRepairLedgerFailureStore struct {
	store.Store
	err   error
	calls int
}

func (s *assistantTurnRepairLedgerFailureStore) ListAssistantRunEvents(_ context.Context, _ store.Scope, _ string, _ int64, _ int) ([]store.AssistantRunEvent, error) {
	s.calls++
	return nil, s.err
}

func TestGetProjectAssistantThreadTurnReturnsTerminalSettingsWithoutAudit(t *testing.T) {
	messages := store.NewMemoryStore()
	server := newAssistantTurnDetailServer(messages)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	createAssistantThreadForHTTPTest(t, messages, scope, "thread-detail", "test-user")

	now := time.Now().UTC()
	run := store.AssistantRun{
		ID: "turn-detail", ThreadID: "thread-detail", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantRunStatusCompleted, ClientRequestID: "client-detail", UserMessageID: "user-detail",
		ActiveMessageID: "assistant-detail", Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	audit, err := json.Marshal(projectAssistantRunAudit{
		Version:  projectAssistantAuditVersion,
		Provider: "openai-compatible",
		Model:    "safe-model",
		Outcome:  projectAssistantAuditOutcomeSucceeded,
		Failure:  &projectAssistantAuditFailure{Kind: "private-audit-only", Summary: "private-audit-secret"},
		EffectiveSettings: &projectAssistantAuditEffectiveSettings{
			Provider:                 "openai-compatible",
			Model:                    "safe-model",
			Reasoning:                &projectAssistantAuditReasoning{RequestedEffort: reasoningEffortPointer(""), ConfiguredEffort: "medium", EffectiveEffort: "none", Transport: projectLLMTransportChatCompletions},
			OptimizationMode:         "codex_poc",
			ToolContractDigest:       "sha256:tools",
			DynamicToolCatalogDigest: "sha256:catalog",
			InstructionDigest:        "sha256:instructions",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run.Audit = audit
	if _, err := messages.CreateAssistantRun(context.Background(), scope, store.Message{
		ID: run.UserMessageID, Role: "user", ActorID: "test-user", Content: "request", CreatedAt: now, UpdatedAt: now,
	}, store.Message{ID: run.ActiveMessageID, Role: "assistant", Content: "answer", CreatedAt: now, UpdatedAt: now}, run); err != nil {
		t.Fatal(err)
	}
	turn, err := messages.CreateAssistantTurn(context.Background(), scope, store.AssistantTurn{
		ID: run.ID, ThreadID: "thread-detail", ActorID: "test-user", ClientUserMessageID: run.ClientRequestID,
		Mode: run.Mode, ApprovalMode: run.ApprovalMode, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = store.AssistantTurnStatusCompleted
	turn.UpdatedAt = now.Add(time.Millisecond)
	if err := messages.SaveAssistantTurn(context.Background(), scope, turn); err != nil {
		t.Fatal(err)
	}
	if err := messages.SaveAssistantRun(context.Background(), scope, run); err != nil {
		t.Fatal(err)
	}

	router := mux.NewRouter()
	server.Register(router)
	request := assistantTurnDetailHTTPTestRequest(http.MethodGet, "/api/projects/demo/assistant/threads/thread-detail/turns/turn-detail", "test-user")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("turn detail status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private-audit-secret") || strings.Contains(response.Body.String(), `"audit"`) {
		t.Fatalf("turn detail leaked private audit: %s", response.Body.String())
	}
	var detail assistantThreadTurnDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.Turn.ID != run.ID || detail.Turn.Status != store.AssistantTurnStatusCompleted {
		t.Fatalf("turn detail turn = %#v", detail.Turn)
	}
	if detail.Turn.FailedItems != 0 || len(detail.Turn.Failures) != 0 {
		t.Fatalf("turn without tool failures reported a failure summary: %#v", detail.Turn)
	}
	if detail.EffectiveSettings == nil {
		t.Fatal("terminal turn omitted effective settings")
	}
	if detail.EffectiveSettings.Provider != "openai-compatible" || detail.EffectiveSettings.Model != "safe-model" || detail.EffectiveSettings.OptimizationMode != "codex_poc" {
		t.Fatalf("effective settings = %#v", detail.EffectiveSettings)
	}
	if detail.EffectiveSettings.ToolContractDigest != "sha256:tools" || detail.EffectiveSettings.DynamicToolCatalogDigest != "sha256:catalog" || detail.EffectiveSettings.InstructionDigest != "sha256:instructions" {
		t.Fatalf("effective settings digests = %#v", detail.EffectiveSettings)
	}
	if detail.EffectiveSettings.Reasoning == nil || detail.EffectiveSettings.Reasoning.RequestedEffort == nil || *detail.EffectiveSettings.Reasoning.RequestedEffort != "" || detail.EffectiveSettings.Reasoning.ConfiguredEffort != "medium" || detail.EffectiveSettings.Reasoning.EffectiveEffort != "none" || detail.EffectiveSettings.Reasoning.Transport != projectLLMTransportChatCompletions {
		t.Fatalf("effective reasoning settings = %#v", detail.EffectiveSettings.Reasoning)
	}
}

func TestGetProjectAssistantThreadTurnEnforcesThreadOwnership(t *testing.T) {
	messages := store.NewMemoryStore()
	server := newAssistantTurnDetailServer(messages)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	createAssistantThreadForHTTPTest(t, messages, scope, "thread-owned", "test-user")
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "turn-owned", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantRunStatusCompleted, ClientRequestID: "client-owned", UserMessageID: "user-owned", ActiveMessageID: "assistant-owned", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := messages.CreateAssistantRun(context.Background(), scope, store.Message{ID: run.UserMessageID, Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now}, store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}, run); err != nil {
		t.Fatal(err)
	}
	turn, err := messages.CreateAssistantTurn(context.Background(), scope, store.AssistantTurn{ID: run.ID, ThreadID: "thread-owned", ActorID: "test-user", ClientUserMessageID: run.ClientRequestID, Mode: run.Mode, ApprovalMode: run.ApprovalMode, Status: store.AssistantTurnStatusCompleted, CreatedAt: now, UpdatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.SaveAssistantTurn(context.Background(), scope, turn); err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	server.Register(router)
	request := assistantTurnDetailHTTPTestRequest(http.MethodGet, "/api/projects/demo/assistant/threads/thread-owned/turns/turn-owned", "other-user")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-user turn detail status = %d, want %d: %s", response.Code, http.StatusNotFound, response.Body.String())
	}
}

func TestGetProjectAssistantThreadTurnReturnsStoredProjectionWhenReceiptRepairFails(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemoryStore()
	ledgerErr := errors.New("corrupt durable run receipt ledger")
	messages := &assistantTurnRepairLedgerFailureStore{Store: inner, err: ledgerErr}
	server := newAssistantTurnDetailServer(messages)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	threadID, turnID, assistantID := "thread-repair-read", "turn-repair-read", "assistant-repair-read"
	if _, err := inner.CreateAssistantThread(ctx, scope, store.AssistantThread{
		ID: threadID, ActorID: "test-user", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
	}, nil); err != nil {
		t.Fatal(err)
	}
	call := projectToolCallStreamEvent{
		ID: "call-repair-read", Name: projectToolExecCommand, Status: "running",
		Exec: &projectAssistantExecMetadata{Component: "workspace", Argv: []string{"go", "test"}, TimeoutSeconds: 90, Status: "running"},
	}
	action := projectAssistantActionFeedItemFromToolCall(call)
	action.Sequence = 1
	action = finalizeProjectAssistantActionFeed([]projectAssistantActionFeedItem{action}, store.AssistantRunStatusInterrupted)[0]
	itemID := assistantThreadDynamicToolItemID(assistantID, action.ID)
	item := assistantThreadItem{
		ID: itemID, TurnID: turnID, Type: assistantThreadEventDynamicToolCall,
		Status: projectAssistantThreadToolItemStatus(action.Status), Content: action.Title,
		AssistantMessageID: assistantID, Sequence: int64(action.Sequence), CreatedAt: now,
	}
	item.Data, _ = json.Marshal(action)
	itemPayload, _ := json.Marshal(map[string]any{"item": item})
	turn := store.AssistantTurn{
		ID: turnID, ThreadID: threadID, ActorID: "test-user", ClientUserMessageID: "client-repair-read",
		Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantTurnStatusInterrupted, CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	if _, err := inner.CreateAssistantTurn(ctx, scope, turn, []store.AssistantThreadEvent{
		{Type: assistantThreadEventTurnStarted, CreatedAt: now},
		{Type: assistantThreadEventItemStarted, ItemID: itemID, Payload: itemPayload, CreatedAt: now},
		{Type: assistantThreadEventTurnInterrupted, CreatedAt: now.Add(time.Second)},
	}); err != nil {
		t.Fatal(err)
	}
	run := store.AssistantRun{
		ID: turnID, ThreadID: threadID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID,
		Mode: turn.Mode, ApprovalMode: turn.ApprovalMode, Status: store.AssistantRunStatusInterrupted,
		ClientRequestID: turn.ClientUserMessageID, UserMessageID: "user-repair-read", ActiveMessageID: assistantID,
		Revision: 1, CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	if _, err := inner.CreateAssistantRun(ctx, scope,
		store.Message{ID: run.UserMessageID, Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now},
		store.Message{ID: assistantID, Role: "assistant", ActorID: "test-user", Metadata: map[string]any{projectMessageMetadataAssistantActionFeed: []projectAssistantActionFeedItem{action}}, CreatedAt: now, UpdatedAt: now}, run); err != nil {
		t.Fatal(err)
	}

	router := mux.NewRouter()
	server.Register(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, assistantTurnDetailHTTPTestRequest(http.MethodGet, "/api/projects/demo/assistant/threads/"+threadID+"/turns/"+turnID, "test-user"))
	if response.Code != http.StatusOK {
		t.Fatalf("turn detail status = %d, want %d despite optional repair error: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var detail assistantThreadTurnDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.Turn.Status != store.AssistantTurnStatusInterrupted || detail.Turn.FailedItems != 1 || len(detail.Turn.Failures) != 1 || detail.Turn.Failures[0].ItemID != itemID {
		t.Fatalf("turn detail = %#v, want the saved interrupted projection and unknown-result row", detail.Turn)
	}
	if messages.calls != 1 {
		t.Fatalf("durable receipt repair reads=%d, want one bounded attempt", messages.calls)
	}
}

func assistantTurnDetailHTTPTestRequest(method, path, user string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request = stampTestCaller(request, testUserForToken(user+"-token"))
	request.Header.Set("X-Railgrid-User", user)
	request.Header.Set("X-Railgrid-Tenant", "cluster-a")
	request.Header.Set("X-Railgrid-Cluster", "cluster-a")
	return request
}

func newAssistantTurnDetailServer(messages store.Store) *Server {
	scheme := runtime.NewScheme()
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.railgrid.ai/v1alpha1",
		"kind":       "Project",
		"metadata": map[string]any{
			"name": "demo",
			"uid":  "test-project-uid-demo",
		},
	}}
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, project)
	server := NewWithWorkspace(nil, messages, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	server.projectClientFor = func(identity) (*asclient.Client, error) {
		return asclient.NewFromDynamic(dynamicClient), nil
	}
	return server
}
