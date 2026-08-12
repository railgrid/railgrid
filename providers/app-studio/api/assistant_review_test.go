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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/tenant"
)

func TestAssistantReviewStartBuildsSeparateBoundedReadOnlyTurn(t *testing.T) {
	request, err := (assistantThreadReviewStartRequest{
		ClientUserMessageID: " review-message ",
		Target: assistantReviewTarget{
			Type:         " CURRENT_WORKSPACE ",
			Instructions: " Focus on durability regressions. ",
		},
	}).turnRequest()
	if err != nil {
		t.Fatal(err)
	}
	if request.CollaborationMode != store.AssistantRunModeReview || request.ClientUserMessageID != "review-message" {
		t.Fatalf("review turn request = %#v", request)
	}
	if request.Content != "Focus on durability regressions." {
		t.Fatalf("review content = %q", request.Content)
	}

	tooLarge := strings.Repeat("x", projectAssistantReviewInstructionsMaxBytes+1)
	if _, err := (assistantThreadReviewStartRequest{ClientUserMessageID: "review", Target: assistantReviewTarget{Type: projectAssistantReviewTargetCurrentWorkspace, Instructions: tooLarge}}).turnRequest(); err == nil {
		t.Fatal("oversized review instructions were accepted")
	}
}

func TestAssistantReviewModeIsReadOnlyAndFindingOriented(t *testing.T) {
	if !strings.Contains(projectEinoAssistantV2DeepInstruction, "Default, Plan, or Review") ||
		!strings.Contains(projectEinoAssistantV2DeepInstruction, "Plan and Review are read-only") {
		t.Fatalf("base assistant instruction does not describe Review mode: %q", projectEinoAssistantV2DeepInstruction)
	}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo"}}
	prompt := projectSystemPromptForMode(project, nil, projectAssistantCollaborationModeReview, false)
	for _, want := range []string{"separate read-only execution", "Lead with findings ordered by severity", "Do not edit files"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("review prompt missing %q", want)
		}
	}

	tools := []projectAssistantTool{
		projectAssistantToolFunc{spec: projectAssistantToolSpec{Name: "read", Risk: projectAssistantToolRiskRead}},
		projectAssistantToolFunc{spec: projectAssistantToolSpec{Name: "write", Risk: projectAssistantToolRiskWrite}},
	}
	filtered := projectAssistantToolsForCollaborationMode(tools, projectAssistantCollaborationModeReview)
	if len(filtered) != 1 || filtered[0].Spec().Name != "read" {
		t.Fatalf("review tools = %#v, want only read", filtered)
	}
}

func newAssistantReviewHTTPTest(t *testing.T) (*mux.Router, *store.MemoryStore, store.Scope, *initialProjectBootstrapCaptureEngine) {
	t.Helper()
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: defaultProjectLLMBaseURL, Model: "test-model", APIKey: "test-key"}
	secret, err := json.Marshal(projectLLMSettingsSecret(settings).Object)
	if err != nil {
		t.Fatal(err)
	}
	projectYAML := "apiVersion: ai.faros.sh/v1alpha1\nkind: Project\nmetadata:\n  name: demo\n  uid: test-project-uid-demo\nspec: {}\n"
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case strings.Contains(request.Query, "ProjectYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ai_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": projectYAML}}}})
		case strings.Contains(request.Query, "SecretYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"v1": map[string]any{"SecretYaml": string(secret)}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		}
	}))
	t.Cleanup(graphQL.Close)

	messages := store.NewMemoryStore()
	server := NewWithWorkspace(tenant.NewGraphQLClient(graphQL.URL, false), messages, nil, "", false)
	engine := &initialProjectBootstrapCaptureEngine{requests: make(chan projectAssistantRunRequest, 1)}
	server.assistantEngine = engine
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	createAssistantThreadForHTTPTest(t, messages, scope, "thread-review", "test-user")
	router := mux.NewRouter()
	server.Register(router)
	return router, messages, scope, engine
}

func assistantReviewHTTPTestRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Faros-User", "test-user")
	request.Header.Set("X-Faros-Tenant", "root:faros:tenants:org-a:workspace-a")
	request.Header.Set("X-Faros-Cluster", "cluster-a")
	return request
}

func TestAssistantReviewRoutePersistsSeparateTerminalTurnAndReconcilesIdempotently(t *testing.T) {
	router, messages, scope, engine := newAssistantReviewHTTPTest(t)
	var err error

	request := assistantReviewHTTPTestRequest(http.MethodPost, "/api/projects/demo/assistant/threads/thread-review/reviews", `{"clientUserMessageID":"review-1","target":{"type":"current_workspace","instructions":"Focus on durable recovery."}}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("review start status = %d, want %d: %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	var started assistantThreadTurnStartResponse
	if err := json.NewDecoder(response.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	if started.Turn.Mode != store.AssistantRunModeReview {
		t.Fatalf("review turn mode = %q", started.Turn.Mode)
	}
	select {
	case runRequest := <-engine.requests:
		if runRequest.CollaborationMode != projectAssistantCollaborationModeReview || projectAssistantTurnProfileAllowsMutation(runRequest.TurnPolicy.profile) {
			t.Fatalf("review execution policy = mode %q profile %q", runRequest.CollaborationMode, runRequest.TurnPolicy.profile)
		}
	case <-time.After(time.Second):
		t.Fatal("review execution did not reach the assistant engine")
	}

	deadline := time.Now().Add(2 * time.Second)
	var terminal store.AssistantTurn
	for {
		terminal, err = messages.GetAssistantTurn(context.Background(), scope, "thread-review", started.Turn.ID)
		if err == nil && terminal.Status == store.AssistantTurnStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("review turn did not become terminal: turn=%#v err=%v", terminal, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	eventsBefore, err := messages.ListAssistantThreadEvents(context.Background(), scope, "thread-review", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !assistantReviewTestHasEvent(eventsBefore, assistantThreadEventTurnStarted) || !assistantReviewTestHasEvent(eventsBefore, assistantThreadEventTurnCompleted) {
		t.Fatalf("review events = %#v, want start and completion", eventsBefore)
	}

	restarted := NewWithWorkspace(nil, messages, nil, "", false)
	if err := restarted.reconcileProjectAssistantThreadTurn(context.Background(), scope, terminal); err != nil {
		t.Fatal(err)
	}
	eventsAfter, err := messages.ListAssistantThreadEvents(context.Background(), scope, "thread-review", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("restart reconciliation duplicated review events: before=%d after=%d", len(eventsBefore), len(eventsAfter))
	}
}

func TestGenericAssistantThreadTurnRejectsReviewMode(t *testing.T) {
	router, _, _, engine := newAssistantReviewHTTPTest(t)
	request := assistantReviewHTTPTestRequest(http.MethodPost, "/api/projects/demo/assistant/threads/thread-review/turns", `{"content":"review the workspace","clientUserMessageID":"generic-review-1","collaborationMode":"review"}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("generic review turn status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	select {
	case runRequest := <-engine.requests:
		t.Fatalf("generic review turn reached assistant engine with mode %q", runRequest.CollaborationMode)
	default:
	}
}

func assistantReviewTestHasEvent(events []store.AssistantThreadEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
