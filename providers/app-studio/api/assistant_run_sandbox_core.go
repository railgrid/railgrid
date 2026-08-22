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
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

type projectAssistantRunSandbox struct {
	server   *Server
	client   projectAssistantSandboxClient
	id       identity
	project  *aiv1alpha1.Project
	scope    workspace.Scope
	target   projectDevelopmentSyncTargetInfo
	instance projectAssistantSandboxInstance
	runState *projectEinoAssistantRunState
	mu       sync.Mutex
	metadata projectAssistantRunSandboxMetadata
	closed   bool
}

func projectAssistantRunSandboxForRequest(req projectAssistantToolCallRequest) *projectAssistantRunSandbox {
	if req.RunState == nil {
		return nil
	}
	return req.RunState.Sandbox()
}

func ensureProjectAssistantRunSandboxForRequest(ctx context.Context, req projectAssistantToolCallRequest) (*projectAssistantRunSandbox, error) {
	if req.RunState == nil || !req.RunState.SandboxRemoteEnabled() {
		if req.RunState == nil {
			return nil, nil
		}
		return req.RunState.Sandbox(), nil
	}
	return req.RunState.EnsureSandbox(ctx)
}

func (b *projectAssistantRunSandbox) metadataSnapshot() projectAssistantRunSandboxMetadata {
	if b == nil {
		return projectAssistantRunSandboxMetadata{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.metadata
}

func (b *projectAssistantRunSandbox) touch() error {
	if b == nil {
		return errProjectAssistantRunSandboxClosed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || strings.EqualFold(b.metadata.Status, "closed") {
		return errProjectAssistantRunSandboxClosed
	}
	now := time.Now().UTC()
	if !b.metadata.HardExpiresAt.IsZero() && now.After(b.metadata.HardExpiresAt) {
		b.metadata.Status = "expired"
		return fmt.Errorf("%w: sandbox hard lifetime has expired", errProjectAssistantRunSandboxConflict)
	}
	if !b.metadata.IdleExpiresAt.IsZero() && now.After(b.metadata.IdleExpiresAt) {
		b.metadata.Status = "expired"
		return fmt.Errorf("%w: sandbox idle lifetime has expired", errProjectAssistantRunSandboxConflict)
	}
	b.metadata.LastActivityAt = now
	b.metadata.IdleExpiresAt = now.Add(projectAssistantRunSandboxIdleTTL)
	if !b.metadata.HardExpiresAt.IsZero() && b.metadata.IdleExpiresAt.After(b.metadata.HardExpiresAt) {
		b.metadata.IdleExpiresAt = b.metadata.HardExpiresAt
	}
	return nil
}

func (b *projectAssistantRunSandbox) request(ctx context.Context, req projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	if b == nil || b.client == nil {
		return projectAssistantSandboxWorkspaceResponse{}, errors.New("assistant sandbox client is not configured")
	}
	if err := b.touch(); err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	meta := b.metadataSnapshot()
	req.SourceRevision = meta.RemoteRevision
	if req.SourceRevision == 0 {
		req.SourceRevision = meta.SourceRevision
	}
	req.SourceDigest = meta.RemoteDigest
	if req.SourceDigest == "" {
		req.SourceDigest = meta.SourceDigest
	}
	response, err := b.client.Workspace(ctx, b.id, b.target.dataPlaneRefFor("workspace"), req)
	if err != nil {
		return response, err
	}
	b.mu.Lock()
	// Keep the durable FileStore fence separate from the remote worker fence;
	// every remote compare-and-swap advances the latter before checkpoint.
	if response.SourceRevision != 0 {
		b.metadata.RemoteRevision = response.SourceRevision
	}
	if strings.TrimSpace(response.SourceDigest) != "" {
		b.metadata.RemoteDigest = strings.TrimSpace(response.SourceDigest)
	}
	if strings.TrimSpace(response.CheckpointID) != "" {
		b.metadata.RemoteCheckpointID = strings.TrimSpace(response.CheckpointID)
	}
	b.metadata.LastActivityAt = time.Now().UTC()
	b.metadata.IdleExpiresAt = b.metadata.LastActivityAt.Add(projectAssistantRunSandboxIdleTTL)
	if !b.metadata.HardExpiresAt.IsZero() && b.metadata.IdleExpiresAt.After(b.metadata.HardExpiresAt) {
		b.metadata.IdleExpiresAt = b.metadata.HardExpiresAt
	}
	b.mu.Unlock()
	if b.runState != nil {
		b.runState.SetSandboxMetadata(b.metadataSnapshot())
	}
	return response, nil
}

func (b *projectAssistantRunSandbox) read(ctx context.Context, path string) (workspace.FileContent, error) {
	response, err := b.request(ctx, projectAssistantSandboxWorkspaceRequest{Action: "read", Path: path})
	return response.File, err
}

func (b *projectAssistantRunSandbox) list(ctx context.Context, path string, limit int) (projectAssistantSandboxWorkspaceResponse, error) {
	return b.request(ctx, projectAssistantSandboxWorkspaceRequest{Action: "list", Path: path, Limit: limit})
}

func (b *projectAssistantRunSandbox) mutate(ctx context.Context, request projectAssistantSandboxWorkspaceRequest) (workspace.MutationResult, error) {
	response, err := b.request(ctx, request)
	return response.Mutation, err
}

func (b *projectAssistantRunSandbox) exec(ctx context.Context, ref dataPlaneRef, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	if b == nil || b.client == nil {
		return projectSandboxExecResponse{}, errors.New("assistant sandbox client is not configured")
	}
	if err := b.touch(); err != nil {
		return projectSandboxExecResponse{}, err
	}
	meta := b.metadataSnapshot()
	if strings.EqualFold(strings.TrimSpace(request.Action), "start") {
		request.SourceRevision = meta.RemoteRevision
		if request.SourceRevision == 0 {
			request.SourceRevision = meta.SourceRevision
		}
		request.SourceDigest = meta.RemoteDigest
		if request.SourceDigest == "" {
			request.SourceDigest = meta.SourceDigest
		}
	} else {
		// Poll/cancel identify an existing bounded process. They must not carry
		// a stale source fence from the start request.
		request.SourceRevision = 0
		request.SourceDigest = ""
	}
	var response projectSandboxExecResponse
	var err error
	if strings.EqualFold(strings.TrimSpace(request.Action), "start") {
		response, err = retryProjectAssistantExecStart(ctx, request, func(startCtx context.Context, startRequest projectSandboxExecRequest) (projectSandboxExecResponse, error) {
			return b.client.Exec(startCtx, b.id, ref, startRequest)
		})
	} else {
		// Poll and cancel are deliberately single-attempt operations. Retrying
		// either could duplicate lifecycle transitions against a live process.
		response, err = b.client.Exec(ctx, b.id, ref, request)
	}
	if b.runState != nil {
		b.runState.SetSandboxMetadata(b.metadataSnapshot())
	}
	return response, err
}

func projectAssistantSandboxRemoteFence(metadata projectAssistantRunSandboxMetadata) (uint64, string) {
	revision := metadata.RemoteRevision
	if revision == 0 {
		revision = metadata.SourceRevision
	}
	digest := metadata.RemoteDigest
	if digest == "" {
		digest = metadata.SourceDigest
	}
	return revision, digest
}

// projectAssistantRunSandboxDirty compares the worker's current fence with the
// last remote checkpoint fence persisted by App Studio. SourceRevision and
// SourceDigest belong to the FileStore domain and must never decide whether a
// remote worker mutation is dirty. Checkpoints without a persisted remote
// fence fail closed as dirty whenever a remote fence is available; using a
// local source fence here would make a warm no-op depend on unrelated
// FileStore revisions.
func projectAssistantRunSandboxDirty(metadata projectAssistantRunSandboxMetadata) bool {
	remoteRevision, remoteDigest := metadata.RemoteRevision, metadata.RemoteDigest
	if remoteRevision == 0 && strings.TrimSpace(remoteDigest) == "" {
		return false
	}
	checkpointRevision, checkpointDigest := metadata.CheckpointRevision, metadata.CheckpointDigest
	if checkpointRevision == 0 && strings.TrimSpace(checkpointDigest) == "" {
		return true
	}
	if remoteRevision != checkpointRevision {
		return true
	}
	return !sandboxDigestEqual(remoteDigest, checkpointDigest)
}

// checkpointIfDirty performs the one bounded remote-diff -> FileStore
// transaction used by same-turn verification. It intentionally does not
// checkpoint a debugging/read-only run: a sandbox may be retained across a
// permission interrupt or resume, but that does not grant the current run
// mutation authority. The bool reports whether a dirty sandbox was handled
// (including a failed attempt), so callers can distinguish a clean/no-op from
// a fail-closed checkpoint conflict.
func (b *projectAssistantRunSandbox) checkpointIfDirty(ctx context.Context, req projectAssistantRunRequest) (bool, error) {
	if b == nil {
		return false, nil
	}
	metadata := b.metadataSnapshot()
	if status := strings.TrimSpace(metadata.Status); status != "" && !strings.EqualFold(status, "active") {
		return false, nil
	}
	if b.runState != nil && !projectAssistantTurnProfileAllowsMutation(b.runState.TurnProfile()) {
		return false, nil
	}
	if !projectAssistantRunSandboxDirty(metadata) {
		return false, nil
	}
	if req.Workspace == nil && b.server != nil {
		req.Workspace = b.server.workspaces
	}
	if req.Workspace == nil {
		return true, fmt.Errorf("%w: project workspace store is not configured", errProjectAssistantRunSandboxConflict)
	}
	if b.runState == nil {
		return true, fmt.Errorf("%w: run mutation state is not configured", errProjectAssistantRunSandboxConflict)
	}
	if revision, _ := b.runState.SourceMutationRevisions(); revision == 0 {
		return true, fmt.Errorf("%w: source mutation revision is unavailable", errProjectAssistantRunSandboxConflict)
	}
	if err := b.checkpoint(ctx, req); err != nil {
		return true, err
	}
	return true, nil
}

// checkpointProjectAssistantRunSandboxIfDirty is the server-owned bridge used
// by preview and runtime tools. Tool requests intentionally do not carry a
// workspace store or credentials; the active sandbox supplies its immutable
// tenant/project scope, while Server supplies the authoritative FileStore.
func (s *Server) checkpointProjectAssistantRunSandboxIfDirty(ctx context.Context, runState *projectEinoAssistantRunState) (bool, error) {
	if s == nil || runState == nil {
		return false, nil
	}
	sandbox := runState.Sandbox()
	if sandbox == nil {
		return false, nil
	}
	return sandbox.checkpointIfDirty(ctx, projectAssistantRunRequest{
		Identity:       sandbox.id,
		Project:        sandbox.project,
		WorkspaceScope: sandbox.scope,
		Workspace:      s.workspaces,
	})
}

func projectAssistantRunSandboxCheckpointFailure(err error) string {
	if err == nil {
		return "the current workspace mutation could not be checkpointed into the run sandbox"
	}
	reason := strings.TrimSpace(err.Error())
	if errors.Is(err, errProjectAssistantRunSandboxConflict) {
		return "the current workspace mutation is not current because the run sandbox checkpoint conflicted: " + reason
	}
	return "the current workspace mutation is not current because the run sandbox checkpoint failed: " + reason
}
