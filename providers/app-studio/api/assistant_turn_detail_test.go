/*
Copyright 2026 The Faros Authors.

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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

func TestGetProjectAssistantThreadTurnReturnsTerminalSettingsWithoutAudit(t *testing.T) {
	messages := store.NewMemoryStore()
	server := newAssistantTurnDetailServer(messages)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	createAssistantThreadForHTTPTest(t, messages, scope, "thread-detail", "test-user")

	now := time.Now().UTC()
	run := store.AssistantRun{
		ID: "turn-detail", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
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
	if detail.EffectiveSettings == nil {
		t.Fatal("terminal turn omitted effective settings")
	}
	if detail.EffectiveSettings.Provider != "openai-compatible" || detail.EffectiveSettings.Model != "safe-model" || detail.EffectiveSettings.OptimizationMode != "codex_poc" {
		t.Fatalf("effective settings = %#v", detail.EffectiveSettings)
	}
	if detail.EffectiveSettings.ToolContractDigest != "sha256:tools" || detail.EffectiveSettings.DynamicToolCatalogDigest != "sha256:catalog" || detail.EffectiveSettings.InstructionDigest != "sha256:instructions" {
		t.Fatalf("effective settings digests = %#v", detail.EffectiveSettings)
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

func assistantTurnDetailHTTPTestRequest(method, path, user string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-User", user)
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	return request
}

func newAssistantTurnDetailServer(messages store.Store) *Server {
	scheme := runtime.NewScheme()
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.kedge.faros.sh/v1alpha1",
		"kind":       "Project",
		"metadata": map[string]any{
			"name": "demo",
			"uid":  "test-project-uid-demo",
		},
	}}
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, project)
	server := NewWithWorkspace(nil, messages, nil, "", false)
	server.projectClientFor = func(identity) (*asclient.Client, error) {
		return asclient.NewFromDynamic(dynamicClient), nil
	}
	return server
}
