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
	"errors"
	"io/fs"
	"sync"
	"testing"
	"time"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/workspace"
)

type executionFenceSandboxClient struct {
	mu sync.Mutex

	workspaceRevision uint64
	workspaceDigest   string
	workspaceChanges  []projectAssistantSandboxWorkspaceChange
	workspaceActions  []string
	execResponses     map[string]projectSandboxExecResponse
	execRequests      []projectSandboxExecRequest
}

func (c *executionFenceSandboxClient) Workspace(ctx context.Context, _ identity, _ dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	if err := ctx.Err(); err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workspaceActions = append(c.workspaceActions, request.Action)
	if request.Action == "read" {
		return projectAssistantSandboxWorkspaceResponse{}, fs.ErrNotExist
	}
	if request.Action == "checkpoint" && request.CheckpointAction == "create" {
		return projectAssistantSandboxWorkspaceResponse{
			Status: "ok", SourceRevision: c.workspaceRevision, SourceDigest: c.workspaceDigest, CheckpointID: "baseline-next",
		}, nil
	}
	return projectAssistantSandboxWorkspaceResponse{
		Status: "ok", SourceRevision: c.workspaceRevision, SourceDigest: c.workspaceDigest,
		Changes: append([]projectAssistantSandboxWorkspaceChange(nil), c.workspaceChanges...),
	}, nil
}

func (c *executionFenceSandboxClient) Exec(ctx context.Context, _ identity, _ dataPlaneRef, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	if err := ctx.Err(); err != nil {
		return projectSandboxExecResponse{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execRequests = append(c.execRequests, request)
	response, ok := c.execResponses[request.Action]
	if !ok {
		return projectSandboxExecResponse{}, errors.New("unexpected exec action")
	}
	return response, nil
}

func newExecutionFenceSandboxFixture(t *testing.T, content string, profile projectAssistantTurnProfile, client *executionFenceSandboxClient) (*Server, *projectEinoAssistantRunState, *projectAssistantRunSandbox, workspace.Scope) {
	t.Helper()
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "project-uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "main.go", Content: content}}); err != nil {
		t.Fatal(err)
	}
	revision, err := files.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.RecordPreviewCheckpoint(ctx, scope, workspace.SourceChange{SourceRevision: revision}); err != nil {
		t.Fatal(err)
	}
	digest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: content}})
	client.workspaceRevision = 1
	client.workspaceDigest = digest
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(profile))
	project := &aiv1alpha1.Project{}
	project.Name = scope.ProjectName
	server := &Server{workspaces: files}
	sandbox := &projectAssistantRunSandbox{
		server: server, client: client, id: identity{orgUUID: scope.OrgUUID, workspaceUUID: scope.WorkspaceUUID},
		project: project, scope: scope, runState: state,
		target: projectDevelopmentSyncTargetInfo{Resource: "instances", ResourceName: "run"},
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: revision, SourceDigest: digest,
			RemoteRevision: 1, RemoteDigest: digest, RemoteCheckpointID: "baseline",
			CheckpointRevision: 1, CheckpointDigest: digest,
			RunID: "run", HardExpiresAt: time.Now().Add(time.Hour),
		},
	}
	state.SetSandbox(sandbox)
	return server, state, sandbox, scope
}

func TestProjectAssistantRunSandboxUnknownCancelCannotWriteBackOrReuse(t *testing.T) {
	client := &executionFenceSandboxClient{execResponses: map[string]projectSandboxExecResponse{
		"cancel": {RequestID: "request-unknown", SessionID: "session-unknown", State: "canceled"},
		"start":  {RequestID: "request-next", SessionID: "session-next", State: "succeeded", ExitCode: intPointer(0)},
	}}
	server, _, sandbox, scope := newExecutionFenceSandboxFixture(t, "shared source\n", projectAssistantTurnProfileImplementation, client)
	sandbox.mu.Lock()
	sandbox.metadata.ExecutionUnresolved = true
	sandbox.metadata.RemoteFenceNeedsRefresh = true
	sandbox.metadata.PendingExecRequestID = "request-unknown"
	sandbox.metadata.PendingExecSessionID = "session-unknown"
	sandbox.mu.Unlock()

	if _, err := sandbox.exec(context.Background(), dataPlaneRef{}, projectSandboxExecRequest{
		Action: "cancel", RequestID: "request-unknown", SessionID: "session-unknown",
	}); err != nil {
		t.Fatalf("unknown cancel request: %v", err)
	}
	metadata := sandbox.metadataSnapshot()
	if !metadata.ExecutionUnresolved || !metadata.RemoteFenceNeedsRefresh ||
		metadata.PendingExecRequestID != "request-unknown" || metadata.PendingExecSessionID != "session-unknown" {
		t.Fatalf("cancel without a terminal exit receipt cleared the pending command: %#v", metadata)
	}

	if checkpointed, err := server.checkpointProjectAssistantRunSandboxIfDirty(context.Background(), sandbox.runState); !checkpointed || !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("unsettled command checkpoint = (%v, %v), want fail-closed conflict", checkpointed, err)
	}
	if _, err := sandbox.exec(context.Background(), dataPlaneRef{}, projectSandboxExecRequest{Action: "start", RequestID: "request-next"}); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("new command after unproven cancellation = %v, want fail-closed conflict", err)
	}
	client.mu.Lock()
	workspaceActions := append([]string(nil), client.workspaceActions...)
	execRequests := append([]projectSandboxExecRequest(nil), client.execRequests...)
	client.mu.Unlock()
	if len(workspaceActions) != 0 || len(execRequests) != 1 || execRequests[0].Action != "cancel" {
		t.Fatalf("unknown command reached writeback or was reused: workspace=%v exec=%#v", workspaceActions, execRequests)
	}
	file, err := server.workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "main.go", MaxBytes: workspace.MaxReadMaxBytes})
	if err != nil || file.Content != "shared source\n" {
		t.Fatalf("unknown command changed shared source: file=%#v err=%v", file, err)
	}
}

func TestProjectAssistantRunSandboxSuccessfulCommandForcesFreshSourceCheck(t *testing.T) {
	for _, profile := range []projectAssistantTurnProfile{projectAssistantTurnProfileDebugging, projectAssistantTurnProfileImplementation} {
		t.Run(string(profile), func(t *testing.T) {
			client := &executionFenceSandboxClient{execResponses: map[string]projectSandboxExecResponse{
				"start": {RequestID: "request-write", SessionID: "session-write", State: "succeeded", ExitCode: intPointer(0)},
			}}
			server, state, sandbox, scope := newExecutionFenceSandboxFixture(t, "shared source\n", profile, client)
			client.workspaceRevision = 2
			client.workspaceDigest = projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "command wrote this\n"}})
			client.workspaceChanges = []projectAssistantSandboxWorkspaceChange{{
				Path: "main.go", Operation: string(workspace.ManagedFileReplace), Content: "command wrote this\n",
				ExpectedVersion: projectAssistantSandboxContentVersion("shared source\n"),
			}}
			if _, err := sandbox.exec(context.Background(), dataPlaneRef{}, projectSandboxExecRequest{Action: "start", RequestID: "request-write"}); err != nil {
				t.Fatalf("successful command: %v", err)
			}
			if !sandbox.metadataSnapshot().RemoteFenceNeedsRefresh {
				t.Fatal("terminal shell result was treated as a current source fence without a fresh diff")
			}
			checkpointed, err := server.checkpointProjectAssistantRunSandboxIfDirty(context.Background(), state)
			var mutationErr *workspace.MutationError
			if !checkpointed || !errors.As(err, &mutationErr) || mutationErr.Code != workspace.MutationErrorConflict {
				t.Fatalf("command source mutation check = (%v, %v), want fresh-diff conflict", checkpointed, err)
			}
			client.mu.Lock()
			workspaceActions := append([]string(nil), client.workspaceActions...)
			client.mu.Unlock()
			if len(workspaceActions) != 1 || workspaceActions[0] != "checkpoint" {
				t.Fatalf("terminal command did not force exactly one remote source check: %v", workspaceActions)
			}
			file, readErr := server.workspaces.ReadFile(context.Background(), scope, workspace.ReadOptions{Path: "main.go", MaxBytes: workspace.MaxReadMaxBytes})
			if readErr != nil || file.Content != "shared source\n" {
				t.Fatalf("unapproved shell write reached the shared workspace: file=%#v err=%v", file, readErr)
			}
		})
	}
}

func TestProjectAssistantRunSandboxCleanReadOnlyCommandCanVerify(t *testing.T) {
	for _, profile := range []projectAssistantTurnProfile{projectAssistantTurnProfileDebugging, projectAssistantTurnProfileImplementation} {
		t.Run(string(profile), func(t *testing.T) {
			client := &executionFenceSandboxClient{execResponses: map[string]projectSandboxExecResponse{
				"start": {RequestID: "request-clean", SessionID: "session-clean", State: "succeeded", ExitCode: intPointer(0)},
			}}
			server, state, sandbox, scope := newExecutionFenceSandboxFixture(t, "shared source\n", profile, client)
			if _, err := sandbox.exec(context.Background(), dataPlaneRef{}, projectSandboxExecRequest{Action: "start", RequestID: "request-clean"}); err != nil {
				t.Fatalf("clean command: %v", err)
			}
			runCtx := projectAssistantWorkflowRunContext{
				Server: server, Identity: sandbox.id, Project: sandbox.project, WorkspaceScope: scope, RunState: state,
			}
			input, err := initializeProjectAssistantRuntimeVerification(runCtx)(context.Background(), &projectAssistantRuntimeVerificationToolInput{})
			if err != nil {
				t.Fatalf("initialize runtime verification: %v", err)
			}
			input.Runtime = &projectAssistantRuntimeWorkflowResult{Status: "ready", PreviewURL: "https://demo.preview.example/"}
			result, err := formatProjectAssistantRuntimeVerification(context.Background(), input)
			if err != nil {
				t.Fatalf("format runtime verification: %v", err)
			}
			if result.Status != "ready" {
				t.Fatalf("clean command did not verify current source: %#v", result)
			}
			metadata := sandbox.metadataSnapshot()
			if metadata.ExecutionUnresolved || metadata.RemoteFenceNeedsRefresh {
				t.Fatalf("successful clean source check left execution fence dirty: %#v", metadata)
			}
			client.mu.Lock()
			workspaceActions := append([]string(nil), client.workspaceActions...)
			client.mu.Unlock()
			if len(workspaceActions) != 1 || workspaceActions[0] != "checkpoint" {
				t.Fatalf("clean command verification skipped its fresh source check: %v", workspaceActions)
			}
		})
	}
}

func TestProjectAssistantRunSandboxTerminalReceiptIsIdempotentAndCannotClearNewerSession(t *testing.T) {
	client := &executionFenceSandboxClient{execResponses: map[string]projectSandboxExecResponse{
		"start": {RequestID: "request-old", SessionID: "session-old", State: "succeeded", ExitCode: intPointer(0)},
		"poll":  {RequestID: "request-old", SessionID: "session-old", State: "succeeded", ExitCode: intPointer(0)},
	}}
	_, _, sandbox, _ := newExecutionFenceSandboxFixture(t, "shared source\n", projectAssistantTurnProfileDebugging, client)
	if _, err := sandbox.exec(context.Background(), dataPlaneRef{}, projectSandboxExecRequest{Action: "start", RequestID: "request-old"}); err != nil {
		t.Fatalf("start old command: %v", err)
	}
	if _, err := sandbox.exec(context.Background(), dataPlaneRef{}, projectSandboxExecRequest{
		Action: "poll", RequestID: "request-old", SessionID: "session-old",
	}); err != nil {
		t.Fatalf("duplicate terminal receipt for settled command was not idempotent: %v", err)
	}

	sandbox.mu.Lock()
	sandbox.metadata.ExecutionUnresolved = true
	sandbox.metadata.RemoteFenceNeedsRefresh = true
	sandbox.metadata.PendingExecRequestID = "request-new"
	sandbox.metadata.PendingExecSessionID = "session-new"
	sandbox.mu.Unlock()
	if _, err := sandbox.exec(context.Background(), dataPlaneRef{}, projectSandboxExecRequest{
		Action: "poll", RequestID: "request-old", SessionID: "session-old",
	}); err == nil {
		t.Fatal("old terminal receipt unexpectedly settled a different pending command")
	}
	metadata := sandbox.metadataSnapshot()
	if !metadata.ExecutionUnresolved || metadata.PendingExecRequestID != "request-new" || metadata.PendingExecSessionID != "session-new" {
		t.Fatalf("duplicate old receipt cleared or replaced newer pending session: %#v", metadata)
	}
}

func TestProjectAssistantRunSandboxUnknownSessionCannotBeClearedByDifferentReceipt(t *testing.T) {
	client := &executionFenceSandboxClient{execResponses: map[string]projectSandboxExecResponse{
		"poll": {RequestID: "request-unknown", SessionID: "session-other", State: "succeeded", ExitCode: intPointer(0)},
	}}
	_, _, sandbox, _ := newExecutionFenceSandboxFixture(t, "shared source\n", projectAssistantTurnProfileDebugging, client)
	sandbox.mu.Lock()
	sandbox.metadata.ExecutionUnresolved = true
	sandbox.metadata.RemoteFenceNeedsRefresh = true
	sandbox.metadata.PendingExecRequestID = "request-unknown"
	sandbox.metadata.PendingExecSessionID = ""
	sandbox.mu.Unlock()

	if _, err := sandbox.exec(context.Background(), dataPlaneRef{}, projectSandboxExecRequest{
		Action: "poll", RequestID: "request-unknown",
	}); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("terminal receipt for an unbound session = %v, want fail-closed conflict", err)
	}
	metadata := sandbox.metadataSnapshot()
	if !metadata.ExecutionUnresolved || metadata.PendingExecRequestID != "request-unknown" || metadata.PendingExecSessionID != "" {
		t.Fatalf("unmatched terminal receipt cleared the unknown session: %#v", metadata)
	}
	client.mu.Lock()
	workspaceCalls := len(client.workspaceActions)
	client.mu.Unlock()
	if workspaceCalls != 0 {
		t.Fatalf("unmatched terminal receipt reached workspace reconciliation %d times", workspaceCalls)
	}
}
