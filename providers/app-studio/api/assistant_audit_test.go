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
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantRunAuditIsBoundedAndSanitized(t *testing.T) {
	started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	run := &store.AssistantRun{ID: "run-1"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{
		LLM: projectLLMSettings{
			Provider: "google-ai-studio",
			Model:    "google/gemini-3.5-flash",
			APIKey:   "secret-api-key",
			BaseURL:  "https://secret.example.test",
		},
		TurnProfile: projectAssistantTurnProfileImplementation,
	}, run, started)

	recorder.recordPhaseAt(projectEinoAssistantPhaseApproval, started)
	recorder.recordPhaseAt(projectEinoAssistantPhaseApproval, started.Add(time.Millisecond))
	for i := 0; i < projectAssistantAuditMaxPhases+10; i++ {
		phase := projectEinoAssistantPhaseMutate
		if i%2 == 1 {
			phase = projectEinoAssistantPhaseVerify
		}
		recorder.recordPhaseAt(phase, started.Add(time.Duration(i+1)*time.Millisecond))
	}
	recorder.recordToolAt(projectToolCallStreamEvent{
		ID:        "call-write",
		Name:      projectToolWriteFile,
		Status:    "requested",
		Arguments: "path src/App.tsx; 123 bytes",
		Summary:   "source=do-not-store",
	}, started.Add(time.Second))
	recorder.recordToolAt(projectToolCallStreamEvent{
		ID:        "call-write",
		Name:      projectToolWriteFile,
		Status:    "succeeded",
		Arguments: "path src/App.tsx; 123 bytes",
		Summary:   "source=do-not-store",
	}, started.Add(2*time.Second))
	recorder.recordToolAt(projectToolCallStreamEvent{
		ID:        "call-search",
		Name:      projectToolSearchProjectFiles,
		Status:    "succeeded",
		Arguments: "query secret-search-term; maxResults 20",
		Summary:   "secret-result",
	}, started.Add(3*time.Second))
	recorder.recordToolAt(projectToolCallStreamEvent{
		ID:        "call-env",
		Name:      projectToolSetRuntimeEnv,
		Status:    "succeeded",
		Arguments: "2 variable(s): API_TOKEN, PASSWORD",
		Summary:   "secret-env-result",
	}, started.Add(4*time.Second))
	for i := 0; i < projectAssistantAuditMaxTools+20; i++ {
		recorder.recordToolAt(projectToolCallStreamEvent{
			ID:        "call-extra-" + strconv.Itoa(i),
			Name:      "unknown__tool",
			Status:    "succeeded",
			Arguments: `{"content":"do-not-store","apiKey":"secret-api-key"}`,
			Summary:   "secret-result",
		}, started.Add(time.Duration(i+5)*time.Second))
	}
	recorder.finalizeAt(projectAssistantAuditOutcomeSucceeded, started.Add(10*time.Minute))

	raw := string(run.Audit)
	for _, secret := range []string{
		"secret-api-key",
		"secret.example.test",
		"do-not-store",
		"secret-search-term",
		"secret-result",
		"API_TOKEN",
		"PASSWORD",
	} {
		if strings.Contains(raw, secret) {
			t.Fatalf("audit contains sensitive value %q: %s", secret, raw)
		}
	}

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if audit.Version != projectAssistantAuditVersion || audit.Provider != "google-ai-studio" || audit.Model != "google/gemini-3.5-flash" {
		t.Fatalf("audit identity = %#v", audit)
	}
	if audit.Outcome != projectAssistantAuditOutcomeSucceeded || audit.DurationMS != 600000 {
		t.Fatalf("audit completion = %#v", audit)
	}
	if len(audit.PhaseTransitions) > projectAssistantAuditMaxPhases {
		t.Fatalf("phase transitions = %d, want <= %d", len(audit.PhaseTransitions), projectAssistantAuditMaxPhases)
	}
	if len(audit.Tools) > projectAssistantAuditMaxTools {
		t.Fatalf("tools = %d, want <= %d", len(audit.Tools), projectAssistantAuditMaxTools)
	}
	var writeEntries int
	for _, entry := range audit.Tools {
		if entry.ID == "call-write" {
			writeEntries++
			if entry.Path != "src/App.tsx" || entry.Status != "succeeded" {
				t.Fatalf("write audit entry = %#v", entry)
			}
		}
	}
	if writeEntries != 1 {
		t.Fatalf("write audit entries = %d, want one upserted entry", writeEntries)
	}
}

func TestProjectAssistantPermissionAuditDoesNotPersistRawPayloads(t *testing.T) {
	run, err := appendProjectAssistantRunAudit(store.AssistantRun{}, projectAssistantPermissionAudit{
		RequestID:       "perm-1",
		Decision:        projectAssistantPermissionAllow,
		Actor:           "user@example.test",
		ToolCallID:      "call-1",
		ToolName:        projectToolWriteFile,
		EditedArguments: map[string]any{"path": "src/App.tsx", "content": "secret-source"},
		Result:          `{"content":"secret-result"}`,
		Error:           "secret-error",
		ResolvedAt:      time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("append audit: %v", err)
	}
	raw := string(run.Audit)
	for _, secret := range []string{"secret-source", "secret-result", "secret-error", "editedArguments", `"result"`, `"error"`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("permission audit contains %q: %s", secret, raw)
		}
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.Decisions) != 1 || audit.Decisions[0].Reason != "operation_failed" {
		t.Fatalf("permission audit = %#v, want safe failure reason", audit)
	}
}

func TestProjectAssistantPermissionAuditKeepsOnlyRecentDecisions(t *testing.T) {
	run := store.AssistantRun{}
	for i := 0; i < projectAssistantAuditMaxDecisions+10; i++ {
		var err error
		run, err = appendProjectAssistantRunAudit(run, projectAssistantPermissionAudit{
			RequestID:  "perm-" + strconv.Itoa(i),
			Decision:   projectAssistantPermissionAllow,
			ToolCallID: "call-" + strconv.Itoa(i),
			ToolName:   projectToolWriteFile,
			ResolvedAt: time.Date(2026, 7, 26, 12, 0, 0, i, time.UTC),
		})
		if err != nil {
			t.Fatalf("append decision %d: %v", i, err)
		}
	}

	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.Decisions) != projectAssistantAuditMaxDecisions {
		t.Fatalf("decisions = %d, want %d", len(audit.Decisions), projectAssistantAuditMaxDecisions)
	}
	if audit.Decisions[0].RequestID != "perm-10" ||
		audit.Decisions[len(audit.Decisions)-1].RequestID != "perm-"+strconv.Itoa(projectAssistantAuditMaxDecisions+9) {
		t.Fatalf("decision window = %q..%q, want most recent decisions",
			audit.Decisions[0].RequestID,
			audit.Decisions[len(audit.Decisions)-1].RequestID,
		)
	}
}

func TestCompleteClaimedProjectAssistantRunAfterResumeErrorFinalizesAudit(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo"}
	started := time.Now().UTC().Add(-2 * time.Second)
	run := store.AssistantRun{
		ID:          "run-1",
		ProjectName: scope.ProjectName,
		Status:      store.AssistantRunStatusRunning,
		RequestID:   "perm-1",
		CreatedAt:   started,
		UpdatedAt:   started,
	}
	newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{
		LLM:         projectLLMSettings{Provider: "google-ai-studio", Model: "google/gemini-3.5-flash"},
		TurnProfile: projectAssistantTurnProfileImplementation,
	}, &run, started)
	if err := messages.SaveAssistantRun(context.Background(), scope, run); err != nil {
		t.Fatalf("save run: %v", err)
	}
	state := projectAssistantCheckpointState{
		CurrentIndex: 0,
		ToolCalls: []chatToolCall{{
			ID: "call-1",
			Function: chatToolCallFunction{
				Name: projectToolWriteFile,
			},
		}},
	}
	cause := errors.New("resume failed before Eino")
	_, err := server.completeClaimedProjectAssistantRunAfterResumeError(
		context.Background(),
		scope,
		run,
		state,
		projectAssistantResumeRequest{},
		projectAssistantPermissionAllow,
		"user@example.test",
		projectAssistantResumeResponse{},
		nil,
		cause,
	)
	if !errors.Is(err, cause) {
		t.Fatalf("resume completion error = %v, want original cause", err)
	}

	saved, err := messages.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(saved.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if saved.Status != store.AssistantRunStatusCompleted ||
		audit.Outcome != projectAssistantAuditOutcomeFailed ||
		audit.DurationMS < 1000 {
		t.Fatalf("saved run = %#v, audit = %#v; want finalized failure", saved, audit)
	}
}

func TestEinoAssistantEnginePersistsCompletedAndFailedRunAudits(t *testing.T) {
	tests := []struct {
		name        string
		profile     projectAssistantTurnProfile
		content     string
		wantOutcome projectAssistantAuditOutcome
		wantErr     bool
	}{
		{
			name:        "completed discussion",
			profile:     projectAssistantTurnProfileDiscussion,
			content:     "Here is the answer.",
			wantOutcome: projectAssistantAuditOutcomeSucceeded,
		},
		{
			name:        "failed incomplete implementation",
			profile:     projectAssistantTurnProfileImplementation,
			content:     "I reviewed the request.",
			wantOutcome: projectAssistantAuditOutcomeFailed,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
			server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
			chatModel := &retryingEinoChatModel{content: tt.content}
			engine := projectEinoAssistantEngine{
				server: server,
				newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
					return chatModel, nil
				},
				newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
					return nil, nil
				},
			}
			req := projectEinoRunRequestForProfileTest(tt.profile)
			req.LLM = projectLLMSettings{Provider: "google-ai-studio", Model: "google/gemini-3.5-flash"}

			_, err := engine.StreamProjectAssistant(context.Background(), req)
			if tt.wantErr && err == nil {
				t.Fatal("StreamProjectAssistant error = nil, want lifecycle failure")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("StreamProjectAssistant returned error: %v", err)
			}
			if tt.wantErr && !errors.Is(err, adk.ErrExceedMaxRetries) && !strings.Contains(err.Error(), "exceeds max retries") {
				t.Fatalf("StreamProjectAssistant error = %v, want Eino retry exhaustion", err)
			}
			if messages.lastAssistantRun == nil {
				t.Fatal("no assistant run persisted")
			}
			if messages.lastAssistantRun.Status != store.AssistantRunStatusCompleted {
				t.Fatalf("run status = %q, want completed terminal row", messages.lastAssistantRun.Status)
			}
			var audit projectAssistantRunAudit
			if err := json.Unmarshal(messages.lastAssistantRun.Audit, &audit); err != nil {
				t.Fatalf("decode audit: %v", err)
			}
			if audit.Outcome != tt.wantOutcome || audit.Provider != req.LLM.Provider || audit.Model != req.LLM.Model {
				t.Fatalf("audit = %#v", audit)
			}
		})
	}
}
