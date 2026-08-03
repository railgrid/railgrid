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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroshq/provider-app-studio/store"
)

func TestAssistantThreadTurnPublicModeRejectsReview(t *testing.T) {
	for _, mode := range []string{"review", " REVIEW "} {
		_, err := (assistantThreadTurnCreateRequest{CollaborationMode: store.AssistantRunMode(mode)}).publicAssistantThreadTurnMode()
		if err == nil || !strings.Contains(err.Error(), projectAssistantReviewDedicatedRouteMessage) {
			t.Fatalf("public thread mode %q error = %v, want dedicated-review validation", mode, err)
		}
	}
	for _, mode := range []store.AssistantRunMode{store.AssistantRunModeDefault, store.AssistantRunModePlan} {
		got, err := (assistantThreadTurnCreateRequest{CollaborationMode: mode}).publicAssistantThreadTurnMode()
		if err != nil || got != mode {
			t.Fatalf("public thread mode %q = %q, %v; want accepted", mode, got, err)
		}
	}
}

func TestAssistantCollaborationModeForRunAcceptsPersistedReview(t *testing.T) {
	got, ok := projectAssistantCollaborationModeForRun(store.AssistantRun{Mode: store.AssistantRunModeReview})
	if !ok || got != projectAssistantCollaborationModeReview {
		t.Fatalf("persisted review mode = %q, %v; want review, true", got, ok)
	}
}

func TestProjectAssistantResumeStrictDecoderRejectsClientMessageAndRetiredFields(t *testing.T) {
	for _, payload := range []string{
		`{"requestID":"permission-1","decision":"allow","assistantMessageID":"message-1"}`,
		`{"requestID":"permission-1","decision":"allow","workItemID":"work-item-1"}`,
		`{"requestID":"permission-1","decision":"allow","engineVersion":"engine-v1"}`,
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/api/projects/demo/assistant/run-1/resume", strings.NewReader(payload))
		var body projectAssistantResumeRequest
		if decodeStrictJSON(recorder, request, &body) {
			t.Fatalf("decodeStrictJSON accepted retired resume field in %s", payload)
		}
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
	}
}
