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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/workspace"
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
	copy := b.metadata
	if b.metadata.ApprovedMutations != nil {
		copy.ApprovedMutations = make(map[string]projectAssistantSandboxMutationReceipt, len(b.metadata.ApprovedMutations))
		for path, value := range b.metadata.ApprovedMutations {
			copy.ApprovedMutations[path] = value
		}
	}
	if b.metadata.Reconciliations != nil {
		copy.Reconciliations = make(map[string]projectAssistantSandboxReconciliation, len(b.metadata.Reconciliations))
		for path, value := range b.metadata.Reconciliations {
			if value.ConflictProposal != nil {
				proposal := *value.ConflictProposal
				value.ConflictProposal = &proposal
			}
			copy.Reconciliations[path] = value
		}
	}
	return copy
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
	file, _, err := b.readWithConflictProposal(ctx, path)
	return file, err
}

func (b *projectAssistantRunSandbox) readWithConflictProposal(ctx context.Context, path string) (workspace.FileContent, *projectAssistantSandboxConflictProposal, error) {
	clean, err := workspace.CleanProjectPath(path)
	if err != nil {
		return workspace.FileContent{}, nil, err
	}
	if reconciliation, ok := b.metadataSnapshot().Reconciliations[clean]; ok && reconciliation.NeedsReread {
		if err := b.rereadSharedFile(ctx, clean); err != nil {
			// A private source write can invalidate the worker's managed-file
			// manifest, which prevents mirroring the authoritative reread back to
			// that sandbox. Still return the shared source and the preserved
			// proposal so the caller can inspect/reconcile it; keep the marker set
			// so no subsequent mutation can bless the private version.
			latest, exists, readErr := b.sharedFile(ctx, clean)
			if readErr != nil {
				return workspace.FileContent{}, nil, err
			}
			metadata := b.metadataSnapshot()
			proposal := metadata.Reconciliations[clean].ConflictProposal
			if !exists {
				latest = workspace.FileContent{Path: clean}
			}
			return latest, proposal, nil
		}
	}
	response, err := b.request(ctx, projectAssistantSandboxWorkspaceRequest{Action: "read", Path: path})
	metadata := b.metadataSnapshot()
	reconciliation, hasReconciliation := metadata.Reconciliations[clean]
	if err != nil && hasReconciliation && reconciliation.ConflictProposal != nil && projectAssistantRunSandboxTargetMissing(err) {
		return workspace.FileContent{Path: clean}, reconciliation.ConflictProposal, nil
	}
	if err != nil {
		return workspace.FileContent{}, nil, err
	}
	if hasReconciliation {
		return response.File, reconciliation.ConflictProposal, nil
	}
	return response.File, nil, nil
}

func (b *projectAssistantRunSandbox) rereadSharedFile(ctx context.Context, path string) error {
	if b.server == nil || b.server.workspaces == nil {
		return errors.New("shared workspace is unavailable for conflict reconciliation")
	}
	latest, err := b.server.workspaces.ReadFile(ctx, b.scope, workspace.ReadOptions{Path: path, MaxBytes: workspace.MaxReadMaxBytes})
	missing := errors.Is(err, fs.ErrNotExist)
	var mutationErr *workspace.MutationError
	if errors.As(err, &mutationErr) && mutationErr.Code == workspace.MutationErrorTargetNotFound {
		missing = true
	}
	if err != nil && !missing {
		return err
	}
	if latest.Binary || latest.Truncated {
		return errors.New("conflicting file requires a complete text reread before reconciliation")
	}
	reconciliation := b.metadataSnapshot().Reconciliations[path]
	remote, remoteErr := b.request(ctx, projectAssistantSandboxWorkspaceRequest{Action: "read", Path: path})
	remoteMissing := errors.Is(remoteErr, fs.ErrNotExist)
	if errors.As(remoteErr, &mutationErr) && mutationErr.Code == workspace.MutationErrorTargetNotFound {
		remoteMissing = true
	}
	var statusErr *projectDevelopmentSyncHTTPError
	if errors.As(remoteErr, &statusErr) && statusErr.status == 404 {
		remoteMissing = true
	}
	if remoteErr != nil && !remoteMissing {
		return remoteErr
	}
	request := projectAssistantSandboxWorkspaceRequest{Path: path, ExpectedVersion: remote.File.Version, Content: latest.Content}
	switch {
	case missing && remoteMissing:
	case missing:
		request.Action = "delete"
	case remoteMissing:
		request.Action = "create"
	default:
		request.Action = "replace"
	}
	if request.Action != "" {
		if _, err := b.request(ctx, request); err != nil {
			return err
		}
	}
	b.mu.Lock()
	b.metadata.Reconciliations[path] = projectAssistantSandboxReconciliation{
		Version:          latest.Version,
		ConflictProposal: reconciliation.ConflictProposal,
	}
	if b.metadata.ApprovedMutations == nil {
		b.metadata.ApprovedMutations = map[string]projectAssistantSandboxMutationReceipt{}
	}
	b.metadata.ApprovedMutations[path] = projectAssistantSandboxMutationReceipt{
		Version: latest.Version,
		Deleted: missing,
	}
	b.mu.Unlock()
	if b.runState != nil {
		b.runState.SetSandboxMetadata(b.metadataSnapshot())
	}
	return nil
}

func projectAssistantRunSandboxTargetMissing(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var mutationErr *workspace.MutationError
	if errors.As(err, &mutationErr) && mutationErr.Code == workspace.MutationErrorTargetNotFound {
		return true
	}
	var statusErr *projectDevelopmentSyncHTTPError
	return errors.As(err, &statusErr) && statusErr.status == 404
}

func (b *projectAssistantRunSandbox) list(ctx context.Context, path string, limit int) (projectAssistantSandboxWorkspaceResponse, error) {
	return b.request(ctx, projectAssistantSandboxWorkspaceRequest{Action: "list", Path: path, Limit: limit})
}

func (b *projectAssistantRunSandbox) mutate(ctx context.Context, request projectAssistantSandboxWorkspaceRequest) (workspace.MutationResult, error) {
	paths, err := projectAssistantSandboxMutationPaths(request)
	if err != nil {
		return workspace.MutationResult{}, err
	}
	if err := b.guardMutationPaths(ctx, paths); err != nil {
		return workspace.MutationResult{}, err
	}
	response, err := b.request(ctx, request)
	if err != nil {
		return response.Mutation, err
	}
	if response.Mutation.Changed {
		if err := b.recordApprovedMutations(ctx, paths); err != nil {
			return response.Mutation, fmt.Errorf("record approved private workspace mutation: %w", err)
		}
	}
	return response.Mutation, nil
}

func projectAssistantSandboxMutationPaths(request projectAssistantSandboxWorkspaceRequest) ([]string, error) {
	paths := []string{request.Path}
	if strings.EqualFold(strings.TrimSpace(request.Action), "move") {
		paths = []string{request.SourcePath, request.DestinationPath}
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, raw := range paths {
		clean, err := workspace.CleanProjectPath(raw)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out, nil
}

func (b *projectAssistantRunSandbox) remoteFile(ctx context.Context, path string) (workspace.FileContent, bool, error) {
	response, err := b.request(ctx, projectAssistantSandboxWorkspaceRequest{Action: "read", Path: path})
	if projectAssistantRunSandboxTargetMissing(err) {
		return workspace.FileContent{Path: path}, false, nil
	}
	if err != nil {
		return workspace.FileContent{}, false, err
	}
	if response.File.Version == "" {
		return workspace.FileContent{}, false, fmt.Errorf("remote read for %q did not include a complete file version", path)
	}
	return response.File, true, nil
}

func (b *projectAssistantRunSandbox) sharedFile(ctx context.Context, path string) (workspace.FileContent, bool, error) {
	if b.server == nil || b.server.workspaces == nil {
		return workspace.FileContent{}, false, errors.New("shared workspace is unavailable for mutation authorization")
	}
	file, err := b.server.workspaces.ReadFile(ctx, b.scope, workspace.ReadOptions{Path: path, MaxBytes: workspace.MaxReadMaxBytes})
	if projectAssistantRunSandboxTargetMissing(err) {
		return workspace.FileContent{Path: path}, false, nil
	}
	if err != nil {
		return workspace.FileContent{}, false, err
	}
	if file.Version == "" {
		return workspace.FileContent{}, false, fmt.Errorf("shared read for %q did not include a complete file version", path)
	}
	return file, true, nil
}

func projectAssistantSandboxReceiptMatchesFile(receipt projectAssistantSandboxMutationReceipt, file workspace.FileContent, exists bool) bool {
	if receipt.Deleted {
		return !exists
	}
	return exists && receipt.Version != "" && sandboxDigestEqual(receipt.Version, file.Version)
}

func projectAssistantSandboxReceiptMatchesChange(receipt projectAssistantSandboxMutationReceipt, change workspace.ManagedFileChange) bool {
	if change.Operation == workspace.ManagedFileDelete {
		return receipt.Deleted
	}
	if receipt.Deleted || receipt.Version == "" {
		return false
	}
	sum := sha256.Sum256([]byte(change.Content))
	return sandboxDigestEqual(receipt.Version, "sha256:"+hex.EncodeToString(sum[:]))
}

func (b *projectAssistantRunSandbox) guardMutationPaths(ctx context.Context, paths []string) error {
	metadata := b.metadataSnapshot()
	for _, path := range paths {
		if reconciliation := metadata.Reconciliations[path]; reconciliation.NeedsReread {
			return &workspace.MutationError{Code: workspace.MutationErrorStale, Path: path, ChangedFiles: []string{path}, Message: "private workspace changes require an authoritative shared-file reread before another edit"}
		}
		remote, remoteExists, err := b.remoteFile(ctx, path)
		if err != nil {
			return &workspace.MutationError{Code: workspace.MutationErrorConflict, Path: path, ChangedFiles: []string{path}, Message: "private workspace state could not be verified before mutation"}
		}
		if receipt, ok := metadata.ApprovedMutations[path]; ok {
			if projectAssistantSandboxReceiptMatchesFile(receipt, remote, remoteExists) {
				continue
			}
			shared, sharedExists, readErr := b.sharedFile(ctx, path)
			if readErr != nil {
				return readErr
			}
			b.markUnapprovedChange(path, remote, remoteExists, shared, sharedExists)
			return &workspace.MutationError{Code: workspace.MutationErrorConflict, Path: path, ChangedFiles: []string{path}, Message: "private workspace changed after the approved mutation; reread the shared file and reconcile the proposal before editing"}
		}
		shared, sharedExists, err := b.sharedFile(ctx, path)
		if err != nil {
			return err
		}
		if remoteExists == sharedExists && (!remoteExists || (remote.Version != "" && sandboxDigestEqual(remote.Version, shared.Version))) {
			continue
		}
		b.markUnapprovedChange(path, remote, remoteExists, shared, sharedExists)
		return &workspace.MutationError{Code: workspace.MutationErrorConflict, Path: path, ChangedFiles: []string{path}, Message: "private workspace contains an unapproved change; reread the shared file and reconcile the proposal before editing"}
	}
	return nil
}

func (b *projectAssistantRunSandbox) recordApprovedMutations(ctx context.Context, paths []string) error {
	receipts := make(map[string]projectAssistantSandboxMutationReceipt, len(paths))
	for _, path := range paths {
		file, exists, err := b.remoteFile(ctx, path)
		if err != nil {
			return err
		}
		receipt := projectAssistantSandboxMutationReceipt{Deleted: !exists}
		if exists {
			receipt.Version = file.Version
		}
		receipts[path] = receipt
	}
	b.mu.Lock()
	if b.metadata.ApprovedMutations == nil {
		b.metadata.ApprovedMutations = make(map[string]projectAssistantSandboxMutationReceipt, len(receipts))
	}
	for path, receipt := range receipts {
		b.metadata.ApprovedMutations[path] = receipt
		if reconciliation, ok := b.metadata.Reconciliations[path]; ok {
			reconciliation.NeedsReread = false
			reconciliation.ConflictProposal = nil
			b.metadata.Reconciliations[path] = reconciliation
		}
	}
	b.mu.Unlock()
	if b.runState != nil {
		b.runState.SetSandboxMetadata(b.metadataSnapshot())
	}
	return nil
}

func (b *projectAssistantRunSandbox) discoverUnapprovedRemoteChanges(ctx context.Context) ([]string, error) {
	if b == nil || b.server == nil || b.server.workspaces == nil {
		return nil, errors.New("shared workspace is unavailable for private-change inspection")
	}
	snapshot, err := b.server.projectWorkspaceSyncFiles(ctx, b.scope)
	if err != nil {
		return nil, err
	}
	shared := make(map[string]workspace.FileContent, len(snapshot.Files))
	candidates := make(map[string]struct{}, len(snapshot.Files))
	for _, file := range snapshot.Files {
		version := projectAssistantSandboxContentVersion(file.Content)
		shared[file.Path] = workspace.FileContent{Path: file.Path, Content: file.Content, Version: version}
		candidates[file.Path] = struct{}{}
	}
	metadata := b.metadataSnapshot()
	for path := range metadata.ApprovedMutations {
		candidates[path] = struct{}{}
	}
	for path := range metadata.Reconciliations {
		candidates[path] = struct{}{}
	}
	paths := make([]string, 0, len(candidates))
	for path := range candidates {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	unapproved := make([]string, 0)
	for _, path := range paths {
		remote, remoteExists, readErr := b.remoteFile(ctx, path)
		if readErr != nil {
			return nil, readErr
		}
		sharedFile, sharedExists := shared[path]
		if receipt, ok := metadata.ApprovedMutations[path]; ok {
			if projectAssistantSandboxReceiptMatchesFile(receipt, remote, remoteExists) {
				continue
			}
		} else if remoteExists == sharedExists && (!remoteExists || (remote.Version != "" && sandboxDigestEqual(remote.Version, sharedFile.Version))) {
			continue
		}
		b.markUnapprovedChange(path, remote, remoteExists, sharedFile, sharedExists)
		unapproved = append(unapproved, path)
	}
	return unapproved, nil
}

func projectAssistantSandboxContentVersion(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (b *projectAssistantRunSandbox) recordUnapprovedCheckpointChanges(proposals map[string]projectAssistantSandboxConflictProposal) {
	if b == nil || len(proposals) == 0 {
		return
	}
	b.mu.Lock()
	if b.metadata.Reconciliations == nil {
		b.metadata.Reconciliations = make(map[string]projectAssistantSandboxReconciliation, len(proposals))
	}
	for path, proposal := range proposals {
		current := b.metadata.Reconciliations[path]
		current.NeedsReread = true
		copy := proposal
		current.ConflictProposal = &copy
		b.metadata.Reconciliations[path] = current
	}
	b.mu.Unlock()
	if b.runState != nil {
		b.runState.SetSandboxMetadata(b.metadataSnapshot())
	}
}

func sandboxUnapprovedMutationError(rawPaths []string) error {
	paths := append([]string(nil), rawPaths...)
	sort.Strings(paths)
	paths = compactAssistantSandboxPaths(paths)
	if len(paths) == 0 {
		return nil
	}
	return &workspace.MutationError{
		Code:         workspace.MutationErrorConflict,
		Path:         paths[0],
		ChangedFiles: paths,
		Message:      "private sandbox contains source changes without a matching approved workspace mutation; reread and reconcile these files before checkpointing",
	}
}

func projectAssistantSandboxUnresolvedPaths(reconciliations map[string]projectAssistantSandboxReconciliation) []string {
	paths := make([]string, 0)
	for path, reconciliation := range reconciliations {
		if reconciliation.NeedsReread {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

func sandboxUnresolvedMutationError(rawPaths []string) error {
	paths := append([]string(nil), rawPaths...)
	sort.Strings(paths)
	paths = compactAssistantSandboxPaths(paths)
	if len(paths) == 0 {
		return nil
	}
	return &workspace.MutationError{
		Code:         workspace.MutationErrorConflict,
		Path:         paths[0],
		ChangedFiles: paths,
		Message:      "shared-file contention remains unresolved; private edits were not applied",
	}
}

func projectAssistantSandboxRetainReceipts(receipts map[string]projectAssistantSandboxMutationReceipt, retainedPaths []string) map[string]projectAssistantSandboxMutationReceipt {
	if len(receipts) == 0 || len(retainedPaths) == 0 {
		return nil
	}
	retained := make(map[string]struct{}, len(retainedPaths))
	for _, path := range retainedPaths {
		retained[path] = struct{}{}
	}
	out := make(map[string]projectAssistantSandboxMutationReceipt, len(retained))
	for path, receipt := range receipts {
		if _, ok := retained[path]; ok {
			out[path] = receipt
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func compactAssistantSandboxPaths(paths []string) []string {
	if len(paths) < 2 {
		return paths
	}
	out := paths[:1]
	for _, path := range paths[1:] {
		if path != out[len(out)-1] {
			out = append(out, path)
		}
	}
	return out
}

func (b *projectAssistantRunSandbox) markUnapprovedChange(path string, remote workspace.FileContent, remoteExists bool, shared workspace.FileContent, sharedExists bool) {
	proposal := &projectAssistantSandboxConflictProposal{ExpectedVersion: remote.Version}
	switch {
	case remoteExists && !sharedExists:
		proposal.Operation = workspace.ManagedFileCreate
		proposal.Content = remote.Content
	case remoteExists && sharedExists:
		proposal.Operation = workspace.ManagedFileReplace
		proposal.Content = remote.Content
	case !remoteExists && sharedExists:
		proposal.Operation = workspace.ManagedFileDelete
		proposal.ExpectedVersion = shared.Version
	default:
		proposal = nil
	}
	b.mu.Lock()
	if b.metadata.Reconciliations == nil {
		b.metadata.Reconciliations = make(map[string]projectAssistantSandboxReconciliation)
	}
	current := b.metadata.Reconciliations[path]
	current.NeedsReread = true
	if proposal != nil {
		current.ConflictProposal = proposal
	}
	b.metadata.Reconciliations[path] = current
	b.mu.Unlock()
	if b.runState != nil {
		b.runState.SetSandboxMetadata(b.metadataSnapshot())
	}
}

func (b *projectAssistantRunSandbox) exec(ctx context.Context, ref dataPlaneRef, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	if b == nil || b.client == nil {
		err := errors.New("assistant sandbox client is not configured")
		if strings.EqualFold(strings.TrimSpace(request.Action), "start") {
			err = projectAssistantExecStartNotDispatched(err)
		}
		return projectSandboxExecResponse{}, err
	}
	if err := b.touch(); err != nil {
		if strings.EqualFold(strings.TrimSpace(request.Action), "start") {
			err = projectAssistantExecStartNotDispatched(err)
		}
		return projectSandboxExecResponse{}, err
	}
	meta := b.metadataSnapshot()
	if strings.EqualFold(strings.TrimSpace(request.Action), "start") {
		if request.SourceRevision == 0 {
			request.SourceRevision, request.SourceDigest = projectAssistantSandboxRemoteFence(meta)
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
	// Preview and runtime evidence may certify only the snapshot this run
	// actually examined. Editing disjoint files remains valid, but a private
	// command result cannot certify a sibling thread's newer source revision.
	if s.workspaces != nil {
		current, err := s.workspaces.SourceRevision(ctx, sandbox.scope)
		if err != nil {
			return true, err
		}
		if current != sandbox.metadataSnapshot().SourceRevision {
			return true, fmt.Errorf("%w: the shared source changed after this run's snapshot", errProjectAssistantRunSandboxConflict)
		}
	}
	if !projectAssistantTurnProfileAllowsMutation(runState.TurnProfile()) {
		metadata := sandbox.metadataSnapshot()
		if !sandboxDigestEqual(metadata.SourceDigest, metadata.RemoteDigest) {
			return true, fmt.Errorf("%w: read-only command snapshot differs from the shared preview source", errProjectAssistantRunSandboxConflict)
		}
		return false, nil
	}
	checkpointed, err := sandbox.checkpointIfDirty(ctx, projectAssistantRunRequest{
		Identity:       sandbox.id,
		Project:        sandbox.project,
		WorkspaceScope: sandbox.scope,
		Workspace:      s.workspaces,
	})
	if err != nil {
		return checkpointed, err
	}
	metadata := sandbox.metadataSnapshot()
	if !sandboxDigestEqual(metadata.SourceDigest, metadata.RemoteDigest) {
		return true, fmt.Errorf("%w: private command snapshot differs from the shared preview source", errProjectAssistantRunSandboxConflict)
	}
	return checkpointed, nil
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
