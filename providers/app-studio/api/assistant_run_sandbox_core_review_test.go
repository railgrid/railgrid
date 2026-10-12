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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/workspace"
)

type cancelAfterFenceRefreshSandboxClient struct {
	cancel context.CancelFunc

	mu             sync.Mutex
	workspaceCalls int
	execCalls      int
}

func (c *cancelAfterFenceRefreshSandboxClient) Workspace(_ context.Context, _ identity, _ dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	c.mu.Lock()
	c.workspaceCalls++
	c.mu.Unlock()
	if request.Action == "checkpoint" {
		c.cancel()
	}
	return projectAssistantSandboxWorkspaceResponse{
		Status: "ok", SourceRevision: request.SourceRevision, SourceDigest: request.SourceDigest,
	}, nil
}

func (c *cancelAfterFenceRefreshSandboxClient) Exec(context.Context, identity, dataPlaneRef, projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	c.mu.Lock()
	c.execCalls++
	c.mu.Unlock()
	return projectSandboxExecResponse{}, errors.New("unexpected exec dispatch")
}

func TestRunSandboxPrepareExecStartStopsAfterCanceledFenceRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &cancelAfterFenceRefreshSandboxClient{cancel: cancel}
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging))
	digest := "sha256:source"
	sandbox := &projectAssistantRunSandbox{
		client: client, runState: state,
		target: projectDevelopmentSyncTargetInfo{Resource: "instances", ResourceName: "run"},
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: 1, SourceDigest: digest,
			RemoteRevision: 1, RemoteDigest: digest, RemoteCheckpointID: "baseline",
			CheckpointRevision: 1, CheckpointDigest: digest, RemoteFenceNeedsRefresh: true,
		},
	}
	state.SetSandbox(sandbox)

	err := sandbox.prepareExecStart(ctx, "cancel-during-refresh")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare after cancellation during refresh = %v, want context.Canceled", err)
	}
	metadata := sandbox.metadataSnapshot()
	if metadata.ExecutionUnresolved || metadata.PendingExecRequestID != "" {
		t.Fatalf("canceled preflight latched a command: %#v", metadata)
	}
	client.mu.Lock()
	workspaceCalls, execCalls := client.workspaceCalls, client.execCalls
	client.mu.Unlock()
	if workspaceCalls != 1 || execCalls != 0 {
		t.Fatalf("canceled preflight calls = workspace %d, exec %d; want one reconciliation and no START", workspaceCalls, execCalls)
	}
}

func TestRunSandboxPrepareExecStartChecksCancellationAfterOperationContention(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging))
	digest := "sha256:source"
	sandbox := &projectAssistantRunSandbox{
		target: projectDevelopmentSyncTargetInfo{Resource: "instances", ResourceName: "run"},
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: 1, SourceDigest: digest,
			RemoteRevision: 1, RemoteDigest: digest, RemoteCheckpointID: "baseline",
			CheckpointRevision: 1, CheckpointDigest: digest,
		},
	}
	sandbox.operationMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- sandbox.prepareExecStart(ctx, "cancel-during-contention") }()
	select {
	case err := <-result:
		sandbox.operationMu.Unlock()
		cancel()
		t.Fatalf("prepare bypassed the operation lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	sandbox.operationMu.Unlock()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("prepare after lock contention = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled command preflight did not leave operation contention")
	}
	metadata := sandbox.metadataSnapshot()
	if metadata.ExecutionUnresolved || metadata.PendingExecRequestID != "" {
		t.Fatalf("canceled contended preflight latched a command: %#v", metadata)
	}
}

func TestRunSandboxCertificationRequiresTheFullPinnedSourceRevision(t *testing.T) {
	client := &executionFenceSandboxClient{execResponses: map[string]projectSandboxExecResponse{}}
	server, state, _, scope := newExecutionFenceSandboxFixture(t, "shared source\n", projectAssistantTurnProfileDebugging, client)
	if _, err := server.workspaces.CreateFile(context.Background(), scope, workspace.CreateOptions{Path: "sibling.txt", Content: "sibling edit\n"}); err != nil {
		t.Fatalf("create disjoint sibling edit: %v", err)
	}

	checkpointed, err := server.checkpointProjectAssistantRunSandboxIfDirty(context.Background(), state)
	if !checkpointed || !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("certification after disjoint source edit = (%v, %v), want full-source conflict", checkpointed, err)
	}
	if !strings.Contains(err.Error(), "new assistant turn") || !strings.Contains(err.Error(), "latest workspace") {
		t.Fatalf("source revision blocker is not actionable: %v", err)
	}
}

func TestRunSandboxMetadataSnapshotClonesReceiptsAndProposalPointers(t *testing.T) {
	sandbox := &projectAssistantRunSandbox{metadata: projectAssistantRunSandboxMetadata{
		ApprovedMutations: map[string]projectAssistantSandboxMutationReceipt{
			"main.go": {Version: "sha256:approved"},
		},
	}}
	proposal := projectAssistantSandboxConflictProposal{Operation: workspace.ManagedFileReplace, Content: "proposal bytes\n", ExpectedVersion: "sha256:expected"}
	sandbox.recordUnapprovedCheckpointChanges(map[string]projectAssistantSandboxConflictProposal{"main.go": proposal})
	proposal.Content = "caller changed input\n"

	snapshot := sandbox.metadataSnapshot()
	snapshot.ApprovedMutations["main.go"] = projectAssistantSandboxMutationReceipt{Version: "sha256:snapshot-only"}
	snapshotReconciliation := snapshot.Reconciliations["main.go"]
	snapshotReconciliation.ConflictProposal.Content = "snapshot changed proposal\n"
	snapshot.Reconciliations["main.go"] = snapshotReconciliation

	stored := sandbox.metadataSnapshot()
	if got := stored.ApprovedMutations["main.go"].Version; got != "sha256:approved" {
		t.Fatalf("snapshot receipt mutation changed stored metadata: %q", got)
	}
	if got := stored.Reconciliations["main.go"].ConflictProposal.Content; got != "proposal bytes\n" {
		t.Fatalf("snapshot/input proposal mutation changed stored metadata: %q", got)
	}
}
