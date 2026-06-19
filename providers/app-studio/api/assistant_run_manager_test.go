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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantRunManagerPreemptsActiveTurnForSameProject(t *testing.T) {
	manager := newProjectAssistantRunManager()
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	firstCtx, firstDone := manager.Begin(context.Background(), newProjectAssistantTurnItem(projectAssistantTurnMessage, id, "demo"))
	secondCtx, secondDone := manager.Begin(context.Background(), newProjectAssistantTurnItem(projectAssistantTurnMessage, id, "demo"))
	t.Cleanup(secondDone)

	select {
	case <-firstCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("first turn was not preempted")
	}
	if !errors.Is(context.Cause(firstCtx), errProjectAssistantTurnPreempted) {
		t.Fatalf("first context cause = %v, want preempted", context.Cause(firstCtx))
	}
	if err := secondCtx.Err(); err != nil {
		t.Fatalf("second turn context error = %v, want active", err)
	}
	firstDone()
	if got := manager.activeCount(); got != 1 {
		t.Fatalf("active count after stale finish = %d, want newer turn still active", got)
	}
	secondDone()
	if got := manager.activeCount(); got != 0 {
		t.Fatalf("active count after second finish = %d, want no active turns", got)
	}
}

func TestProjectAssistantRunManagerScopesActiveTurnsByTenantProject(t *testing.T) {
	manager := newProjectAssistantRunManager()
	firstCtx, firstDone := manager.Begin(context.Background(), newProjectAssistantTurnItem(projectAssistantTurnMessage, identity{
		orgUUID:       "org-a",
		workspaceUUID: "ws-1",
	}, "demo"))
	defer firstDone()
	secondCtx, secondDone := manager.Begin(context.Background(), newProjectAssistantTurnItem(projectAssistantTurnMessage, identity{
		orgUUID:       "org-b",
		workspaceUUID: "ws-1",
	}, "demo"))
	defer secondDone()

	if err := firstCtx.Err(); err != nil {
		t.Fatalf("first turn context error = %v, want active for different org", err)
	}
	if err := secondCtx.Err(); err != nil {
		t.Fatalf("second turn context error = %v, want active", err)
	}
	if got := manager.activeCount(); got != 2 {
		t.Fatalf("active count = %d, want separate tenant turns", got)
	}
}

func TestProjectAssistantRunManagerIgnoresUnscopedTurns(t *testing.T) {
	manager := newProjectAssistantRunManager()
	ctx, done := manager.Begin(context.Background(), projectAssistantTurnItem{Kind: projectAssistantTurnMessage})
	defer done()
	if err := ctx.Err(); err != nil {
		t.Fatalf("unscoped turn context error = %v, want unchanged context", err)
	}
	if got := manager.activeCount(); got != 0 {
		t.Fatalf("active count = %d, want unscoped turn ignored", got)
	}
}

func TestGenerateProjectAssistantStreamPreemptsActiveProjectTurn(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: "http://llm.example.test", Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	engine := &preemptProbeProjectAssistantEngine{
		entered:    make(chan struct{}),
		firstCause: make(chan error, 1),
	}
	server.assistantEngine = engine
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"

	firstErr := make(chan error, 1)
	go func() {
		_, err := server.generateProjectAssistantStream(
			httptest.NewRequest(http.MethodPost, "/", nil),
			id,
			client,
			project,
			projectAssistantStreamCallbacks{},
		)
		firstErr <- err
	}()
	select {
	case <-engine.entered:
	case <-time.After(time.Second):
		t.Fatal("first assistant turn did not start")
	}

	secondReply, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	if err != nil {
		t.Fatalf("second generateProjectAssistantStream returned error: %v", err)
	}
	if secondReply != "second turn" {
		t.Fatalf("second reply = %q, want second turn", secondReply)
	}
	select {
	case err := <-firstErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first turn error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first assistant turn was not canceled")
	}
	select {
	case cause := <-engine.firstCause:
		if !errors.Is(cause, errProjectAssistantTurnPreempted) {
			t.Fatalf("first turn cause = %v, want preempted", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("first assistant turn did not report cancellation cause")
	}
}

func TestResumeProjectAssistantFinalizesClaimedRunAfterPreemption(t *testing.T) {
	settings := projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: "http://llm.example.test", Model: "test-model", APIKey: "test-key"}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{secret: projectLLMSettingsSecret(settings)})
	baseStore := store.NewMemoryStore()
	messages := cancelSensitiveAssistantRunStore{Store: baseStore}
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	engine := &preemptProbeResumeAssistantEngine{
		resumeEntered: make(chan struct{}),
		resumeCause:   make(chan error, 1),
	}
	server.assistantEngine = engine
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", user: "user@example.com"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	messageScope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	state := projectAssistantCheckpointState{
		ToolCalls: []chatToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: chatToolCallFunction{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"approved\n"}`,
			},
		}},
		CurrentIndex: 0,
		Eino: &projectAssistantEinoCheckpointState{
			CheckpointID:  "run-resume",
			Checkpoint:    []byte("fake-checkpoint"),
			InterruptID:   "interrupt-write",
			InterruptType: projectAssistantInterruptTypePermission,
			ToolCallID:    "call-write",
			ToolName:      projectToolWriteFile,
		},
	}
	rawCheckpoint, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode checkpoint returned error: %v", err)
	}
	run := store.AssistantRun{
		ID:         "run-resume",
		Status:     store.AssistantRunStatusPendingPermission,
		RequestID:  "perm-resume",
		Checkpoint: rawCheckpoint,
	}
	if err := messages.SaveAssistantRun(context.Background(), messageScope, run); err != nil {
		t.Fatalf("SaveAssistantRun returned error: %v", err)
	}

	resumeErr := make(chan error, 1)
	go func() {
		_, err := server.resumeProjectAssistantRunWithRepositoryAndClient(
			context.Background(),
			httptest.NewRequest(http.MethodPost, "/", nil),
			id,
			client,
			project,
			&ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
			run.ID,
			projectAssistantResumeRequest{RequestID: run.RequestID, Decision: string(projectAssistantPermissionAllow)},
		)
		resumeErr <- err
	}()
	select {
	case <-engine.resumeEntered:
	case <-time.After(time.Second):
		t.Fatal("resume turn did not start")
	}

	reply, err := server.generateProjectAssistantStream(
		httptest.NewRequest(http.MethodPost, "/", nil),
		id,
		client,
		project,
		projectAssistantStreamCallbacks{},
	)
	if err != nil {
		t.Fatalf("generateProjectAssistantStream returned error: %v", err)
	}
	if reply != "new turn" {
		t.Fatalf("reply = %q, want new turn", reply)
	}
	select {
	case err := <-resumeErr:
		if !errors.Is(err, errProjectAssistantTurnPreempted) {
			t.Fatalf("resume error = %v, want preempted", err)
		}
	case <-time.After(time.Second):
		t.Fatal("resume turn was not preempted")
	}
	got, err := messages.GetAssistantRun(context.Background(), messageScope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if got.Status != store.AssistantRunStatusCompleted {
		t.Fatalf("run status = %q, want completed after preempted resume cleanup", got.Status)
	}
	audit := decodeProjectAssistantRunAudit(t, got.Audit)
	if len(audit.Decisions) != 1 || audit.Decisions[0].Actor != id.user || !strings.Contains(audit.Decisions[0].Error, "preempted") {
		t.Fatalf("audit = %#v, want preempted resume decision", audit)
	}
	select {
	case cause := <-engine.resumeCause:
		if !errors.Is(cause, errProjectAssistantTurnPreempted) {
			t.Fatalf("resume cause = %v, want preempted", cause)
		}
	default:
		t.Fatal("resume engine did not report cancellation cause")
	}
}

type preemptProbeProjectAssistantEngine struct {
	calls      atomic.Int32
	entered    chan struct{}
	firstCause chan error
}

func (e *preemptProbeProjectAssistantEngine) StreamProjectAssistant(
	ctx context.Context,
	_ projectAssistantRunRequest,
) (projectAssistantRunResult, error) {
	if e.calls.Add(1) == 1 {
		close(e.entered)
		<-ctx.Done()
		e.firstCause <- context.Cause(ctx)
		return projectAssistantRunResult{}, ctx.Err()
	}
	return projectAssistantRunResult{Content: "second turn"}, nil
}

func (e *preemptProbeProjectAssistantEngine) ResumeProjectAssistant(
	context.Context,
	projectAssistantRunRequest,
	projectAssistantResumeRequest,
	projectAssistantCheckpointState,
) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{}, errors.New("unexpected resume")
}

type preemptProbeResumeAssistantEngine struct {
	resumeEntered chan struct{}
	resumeCause   chan error
}

func (e *preemptProbeResumeAssistantEngine) StreamProjectAssistant(
	context.Context,
	projectAssistantRunRequest,
) (projectAssistantRunResult, error) {
	return projectAssistantRunResult{Content: "new turn"}, nil
}

func (e *preemptProbeResumeAssistantEngine) ResumeProjectAssistant(
	ctx context.Context,
	_ projectAssistantRunRequest,
	_ projectAssistantResumeRequest,
	_ projectAssistantCheckpointState,
) (projectAssistantRunResult, error) {
	close(e.resumeEntered)
	<-ctx.Done()
	cause := context.Cause(ctx)
	e.resumeCause <- cause
	return projectAssistantRunResult{}, cause
}

type cancelSensitiveAssistantRunStore struct {
	store.Store
}

func (s cancelSensitiveAssistantRunStore) SaveAssistantRun(ctx context.Context, scope store.Scope, run store.AssistantRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Store.SaveAssistantRun(ctx, scope, run)
}
