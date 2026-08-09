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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantThreadContinueInterruptedTurnCreatesLinkedTurn(t *testing.T) {
	router, memory, scope, engine := newAssistantReviewHTTPTest(t)
	now := time.Now().UTC()
	run := store.AssistantRun{
		ID:              "run-interrupted",
		Mode:            store.AssistantRunModeDefault,
		ApprovalMode:    store.AssistantApprovalModeOnRequest,
		Status:          store.AssistantRunStatusInterrupted,
		ClientRequestID: "request-1",
		UserMessageID:   "user-1",
		ActiveMessageID: "assistant-1",
		AbortReason:     store.AssistantRunAbortReasonInterrupted,
		Revision:        1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	user := store.Message{ID: run.UserMessageID, ActorID: "test-user", Role: "user", Content: "Build a todo app", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Content: "I started inspecting the workspace.", CreatedAt: now, UpdatedAt: now}
	if _, err := memory.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	turn := store.AssistantTurn{
		ID:                  run.ID,
		ThreadID:            "thread-review",
		ActorID:             "test-user",
		ClientUserMessageID: run.ClientRequestID,
		Mode:                run.Mode,
		ApprovalMode:        run.ApprovalMode,
		Status:              store.AssistantTurnStatusInterrupted,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	turnPayload, _ := json.Marshal(map[string]any{"turn": turn})
	if _, err := memory.CreateAssistantTurn(context.Background(), scope, turn, []store.AssistantThreadEvent{
		{Type: assistantThreadEventTurnStarted, Payload: turnPayload, CreatedAt: now},
		{Type: assistantThreadEventTurnInterrupted, Payload: turnPayload, CreatedAt: now.Add(time.Microsecond)},
	}); err != nil {
		t.Fatalf("CreateAssistantTurn: %v", err)
	}
	if err := appendProjectAssistantConversationMessage(context.Background(), memory, scope, run.ID, "message-user-1", projectAssistantConversationUser, chatMessage{Role: "user", Content: user.Content}); err != nil {
		t.Fatalf("append user conversation: %v", err)
	}
	if err := appendProjectAssistantInterruptedBoundary(context.Background(), memory, scope, run); err != nil {
		t.Fatalf("append interruption boundary: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects/demo/assistant/threads/thread-review/turns/run-interrupted/continue", strings.NewReader(`{"clientUserMessageID":"continue-1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	request.Header.Set("X-Kedge-User", "test-user")
	request.Header.Set("X-Kedge-Tenant", "root:kedge:tenants:org-a:workspace-a")
	request.Header.Set("X-Kedge-Cluster", "cluster-a")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("continue status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	var response assistantThreadTurnStartResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode continue response: %v", err)
	}
	if response.Turn.ID == "" || response.Turn.ID == run.ID {
		t.Fatalf("continued turn = %#v, want a new turn", response.Turn)
	}
	if response.ContinuationOfTurnID != run.ID {
		t.Fatalf("continuation predecessor = %q, want %q", response.ContinuationOfTurnID, run.ID)
	}

	select {
	case captured := <-engine.requests:
		boundaryFound := false
		for _, message := range captured.Conversation {
			if strings.Contains(message.Content, "turn_aborted") {
				boundaryFound = true
				break
			}
		}
		if !boundaryFound {
			t.Fatalf("continued conversation = %#v, want interrupted boundary", captured.Conversation)
		}
	case <-time.After(time.Second):
		t.Fatal("continued assistant run did not reach engine")
	}

	events, err := memory.ListAssistantThreadEvents(context.Background(), scope, "thread-review", 0, 50)
	if err != nil {
		t.Fatalf("list thread events: %v", err)
	}
	var continuation struct {
		ContinuationOfTurnID string `json:"continuationOfTurnID"`
	}
	found := false
	for _, event := range events {
		if event.Type != assistantThreadEventTurnContinued || event.TurnID != response.Turn.ID {
			continue
		}
		found = json.Unmarshal(event.Payload, &continuation) == nil
	}
	if !found || continuation.ContinuationOfTurnID != run.ID {
		t.Fatalf("continuation event = %#v, want predecessor %q", continuation, run.ID)
	}
}
