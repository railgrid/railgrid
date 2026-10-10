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
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/workspace"
)

func TestRunSandboxCheckpointRejectsExecSourceWriteBesideApprovedUnrelatedToolWrite(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "app.ts", Content: "shared source\n"}}); err != nil {
		t.Fatal(err)
	}
	remote := newSandboxWritebackGuardClient(map[string]string{"app.ts": "shared source\n"})
	digest := remote.sourceDigest
	sandbox := newSandboxWritebackGuardSandbox(files, scope, remote, digest)

	if _, err := sandbox.mutate(ctx, projectAssistantSandboxWorkspaceRequest{Action: "create", Path: "notes.txt", Content: "approved tool write\n"}); err != nil {
		t.Fatalf("unrelated tool write: %v", err)
	}
	if _, err := sandbox.exec(ctx, sandbox.target.dataPlaneRefFor("workspace"), projectSandboxExecRequest{Action: "start", Argv: []string{"source-write"}}); err != nil {
		t.Fatalf("source-writing command: %v", err)
	}

	err := sandbox.checkpoint(ctx, projectAssistantRunRequest{Workspace: files, WorkspaceScope: scope})
	var mutationErr *workspace.MutationError
	if !errors.As(err, &mutationErr) || mutationErr.Code != workspace.MutationErrorConflict || len(mutationErr.ChangedFiles) != 1 || mutationErr.ChangedFiles[0] != "app.ts" {
		t.Fatalf("checkpoint error = %v, want a structured app.ts conflict", err)
	}
	if got, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "app.ts"}); err != nil || got.Content != "shared source\n" {
		t.Fatalf("command source write reached shared workspace: %#v, %v", got, err)
	}
	if _, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "notes.txt"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("part of a rejected checkpoint reached shared workspace: %v", err)
	}
	proposal := sandbox.metadataSnapshot().Reconciliations["app.ts"].ConflictProposal
	if proposal == nil || proposal.Operation != workspace.ManagedFileReplace || proposal.Content != "command source\n" {
		t.Fatalf("unapplied command proposal = %#v", proposal)
	}

	before := remote.mutationCalls
	if _, err := sandbox.mutate(ctx, projectAssistantSandboxWorkspaceRequest{Action: "replace", Path: "app.ts", Content: "targeted tool edit\n"}); !errors.As(err, &mutationErr) || mutationErr.Code != workspace.MutationErrorStale {
		t.Fatalf("same-path tool edit = %v, want reread-required blocker", err)
	}
	if remote.mutationCalls != before {
		t.Fatal("same-path tool edit reached the private worker before authoritative reread")
	}

	read, returnedProposal, err := sandbox.readWithConflictProposal(ctx, "app.ts")
	if err != nil || read.Content != "shared source\n" {
		t.Fatalf("authoritative reread = %#v, %v", read, err)
	}
	if returnedProposal == nil || returnedProposal.Content != proposal.Content || returnedProposal.ExpectedVersion != proposal.ExpectedVersion {
		t.Fatalf("reread proposal = %#v, want preserved %#v", returnedProposal, proposal)
	}
	if _, err := sandbox.mutate(ctx, projectAssistantSandboxWorkspaceRequest{Action: "replace", Path: "app.ts", Content: "reconciled tool edit\n"}); !errors.As(err, &mutationErr) {
		t.Fatalf("edit after failed private reread = %v, want persistent blocker", err)
	}
}

func TestRunSandboxCheckpointIgnoresUntrackedCommandOutputAndAppliesUnrelatedToolWrite(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "app.ts", Content: "shared source\n"}}); err != nil {
		t.Fatal(err)
	}
	remote := newSandboxWritebackGuardClient(map[string]string{"app.ts": "shared source\n"})
	sandbox := newSandboxWritebackGuardSandbox(files, scope, remote, remote.sourceDigest)
	if _, err := sandbox.exec(ctx, sandbox.target.dataPlaneRefFor("workspace"), projectSandboxExecRequest{Action: "start", Argv: []string{"output-write"}}); err != nil {
		t.Fatalf("build command: %v", err)
	}
	if _, err := sandbox.mutate(ctx, projectAssistantSandboxWorkspaceRequest{Action: "create", Path: "notes.txt", Content: "approved tool write\n"}); err != nil {
		t.Fatalf("unrelated tool write after build output: %v", err)
	}
	if err := sandbox.checkpoint(ctx, projectAssistantRunRequest{Workspace: files, WorkspaceScope: scope}); err != nil {
		t.Fatalf("checkpoint approved tool write: %v", err)
	}
	if got, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "notes.txt"}); err != nil || got.Content != "approved tool write\n" {
		t.Fatalf("approved tool write = %#v, %v", got, err)
	}
	if _, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "dist/bundle.js"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("untracked build output reached shared source: %v", err)
	}
	if receipts := sandbox.metadataSnapshot().ApprovedMutations; len(receipts) != 0 {
		t.Fatalf("successful checkpoint retained approval receipts: %#v", receipts)
	}
}

func TestRunSandboxQuarantinesPersistentConflictAndCommitsDisjointToolWrite(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "app.ts", Content: "shared source\n"}}); err != nil {
		t.Fatal(err)
	}
	remote := newSandboxWritebackGuardClient(map[string]string{"app.ts": "shared source\n"})
	sandbox := newSandboxWritebackGuardSandbox(files, scope, remote, remote.sourceDigest)
	if _, err := sandbox.mutate(ctx, projectAssistantSandboxWorkspaceRequest{Action: "replace", Path: "app.ts", Content: "thread proposal\n"}); err != nil {
		t.Fatalf("hot-file tool write: %v", err)
	}
	if _, err := sandbox.mutate(ctx, projectAssistantSandboxWorkspaceRequest{Action: "create", Path: "notes.txt", Content: "independent work\n"}); err != nil {
		t.Fatalf("disjoint tool write: %v", err)
	}
	current, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "app.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.ApplyManagedTransaction(ctx, scope, []workspace.ManagedFileChange{{
		Path: "app.ts", Operation: workspace.ManagedFileReplace, Content: "sibling thread won\n", ExpectedVersion: current.Version,
	}}); err != nil {
		t.Fatalf("concurrent sibling update: %v", err)
	}

	var mutationErr *workspace.MutationError
	err = sandbox.checkpoint(ctx, projectAssistantRunRequest{Workspace: files, WorkspaceScope: scope})
	if !errors.As(err, &mutationErr) || (mutationErr.Code != workspace.MutationErrorStale && mutationErr.Code != workspace.MutationErrorConflict) || len(mutationErr.ChangedFiles) != 1 || mutationErr.ChangedFiles[0] != "app.ts" {
		t.Fatalf("first checkpoint error = %v, want a structured app.ts stale-source conflict", err)
	}
	if sandbox.metadataSnapshot().Reconciliations["app.ts"].ConflictProposal == nil {
		t.Fatal("first conflict did not preserve the hot-file proposal")
	}

	// The hot path remains quarantined, but its contention must not turn an
	// approved cold-path mutation into a failed tool result.
	if err := sandbox.checkpoint(ctx, projectAssistantRunRequest{Workspace: files, WorkspaceScope: scope}); err != nil {
		t.Fatalf("checkpoint disjoint approved write while hot file is blocked: %v", err)
	}
	if got, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "app.ts"}); err != nil || got.Content != "sibling thread won\n" {
		t.Fatalf("quarantined proposal overwrote sibling thread: %#v, %v", got, err)
	}
	if got, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "notes.txt"}); err != nil || got.Content != "independent work\n" {
		t.Fatalf("independent approved write was not committed: %#v, %v", got, err)
	}
	metadata := sandbox.metadataSnapshot()
	reconciliation := metadata.Reconciliations["app.ts"]
	if !reconciliation.NeedsReread || reconciliation.ConflictProposal == nil || reconciliation.ConflictProposal.Content != "thread proposal\n" {
		t.Fatalf("hot-file quarantine was not preserved: %#v", reconciliation)
	}
	if _, retained := metadata.ApprovedMutations["app.ts"]; !retained {
		t.Fatal("hot-file approval receipt was lost before authoritative reconciliation")
	}
	if _, retained := metadata.ApprovedMutations["notes.txt"]; retained {
		t.Fatal("successfully checkpointed cold-file receipt was not cleared")
	}
	if sandboxDigestEqual(metadata.SourceDigest, metadata.RemoteDigest) {
		t.Fatal("private snapshot incorrectly certified the shared revision while a proposal remains quarantined")
	}
	lastChange, err := files.LastSourceChange(ctx, scope)
	if err != nil || lastChange == nil || lastChange.ThreadID != "thread-1" || lastChange.RunID != "run-1" {
		t.Fatalf("disjoint checkpoint provenance = %#v, %v", lastChange, err)
	}
	err = sandbox.checkpointForTerminalSettlement(ctx, projectAssistantRunRequest{Workspace: files, WorkspaceScope: scope})
	if !errors.As(err, &mutationErr) || len(mutationErr.ChangedFiles) != 1 || mutationErr.ChangedFiles[0] != "app.ts" {
		t.Fatalf("terminal settlement error = %v, want unresolved app.ts blocker", err)
	}
}

func newSandboxWritebackGuardSandbox(files *workspace.FileStore, scope workspace.Scope, client *sandboxWritebackGuardClient, digest string) *projectAssistantRunSandbox {
	return &projectAssistantRunSandbox{
		server: &Server{workspaces: files}, client: client,
		id: identity{orgUUID: scope.OrgUUID, workspaceUUID: scope.WorkspaceUUID}, scope: scope,
		target: projectDevelopmentSyncTargetInfo{Resource: "instances", ResourceName: "run", Components: map[string]projectTemplateComponent{
			projectAssistantRunSandboxWorkspaceVerb: {WorkspacePath: "."},
		}},
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", RunID: "run-1", ThreadID: "thread-1", SourceRevision: 1, SourceDigest: digest,
			RemoteRevision: 1, RemoteDigest: digest, RemoteCheckpointID: "baseline-1", HardExpiresAt: time.Now().Add(time.Hour),
		},
	}
}

type sandboxWritebackGuardClient struct {
	mu             sync.Mutex
	files          map[string]string
	outputs        map[string]string
	baseline       map[string]string
	sourceRevision uint64
	sourceDigest   string
	manifestValid  bool
	mutationCalls  int
}

func newSandboxWritebackGuardClient(files map[string]string) *sandboxWritebackGuardClient {
	client := &sandboxWritebackGuardClient{files: cloneSandboxWritebackFiles(files), outputs: map[string]string{}, sourceRevision: 1, manifestValid: true}
	client.baseline = cloneSandboxWritebackFiles(files)
	client.sourceDigest = projectSandboxSyncDigest(projectSandboxFilesFromMap(files))
	return client
}

func (f *sandboxWritebackGuardClient) Workspace(ctx context.Context, _ identity, _ dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	if err := ctx.Err(); err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	response := projectAssistantSandboxWorkspaceResponse{Status: "ok", SourceRevision: f.sourceRevision, SourceDigest: f.sourceDigest}
	switch request.Action {
	case "read":
		content, ok := f.files[request.Path]
		if !ok {
			content, ok = f.outputs[request.Path]
		}
		if !ok {
			return projectAssistantSandboxWorkspaceResponse{}, fs.ErrNotExist
		}
		response.File = workspace.FileContent{Path: request.Path, Content: content, Version: sandboxWritebackContentVersion(content)}
		return response, nil
	case "create", "replace", "edit", "delete", "move":
		f.mutationCalls++
		if !f.manifestValid {
			return projectAssistantSandboxWorkspaceResponse{}, errProjectAssistantRunSandboxConflict
		}
		switch request.Action {
		case "create":
			if _, exists := f.files[request.Path]; exists {
				return projectAssistantSandboxWorkspaceResponse{}, errProjectAssistantRunSandboxConflict
			}
			f.files[request.Path] = request.Content
		case "delete":
			if _, exists := f.files[request.Path]; !exists {
				return projectAssistantSandboxWorkspaceResponse{}, fs.ErrNotExist
			}
			delete(f.files, request.Path)
		default:
			if _, exists := f.files[request.Path]; !exists {
				return projectAssistantSandboxWorkspaceResponse{}, fs.ErrNotExist
			}
			f.files[request.Path] = request.Content
		}
		f.advanceSource()
		response.SourceRevision, response.SourceDigest = f.sourceRevision, f.sourceDigest
		response.Mutation = workspace.MutationResult{Path: request.Path, Changed: true}
		return response, nil
	case "checkpoint":
		if request.CheckpointAction == "create" {
			f.baseline = cloneSandboxWritebackFiles(f.files)
			response.CheckpointID = "baseline-next"
			return response, nil
		}
		if !f.manifestValid {
			return projectAssistantSandboxWorkspaceResponse{}, errProjectAssistantRunSandboxConflict
		}
		response.Changes = sandboxWritebackChanges(f.baseline, f.files)
		return response, nil
	default:
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("unexpected workspace action %q", request.Action)
	}
}

func (f *sandboxWritebackGuardClient) Exec(ctx context.Context, _ identity, _ dataPlaneRef, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	if err := ctx.Err(); err != nil {
		return projectSandboxExecResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch request.Argv[0] {
	case "source-write":
		f.files["app.ts"] = "command source\n"
		f.manifestValid = false
		f.advanceSource()
	case "output-write":
		f.outputs["dist/bundle.js"] = "generated bundle\n"
	default:
		return projectSandboxExecResponse{}, fmt.Errorf("unexpected command %q", request.Argv[0])
	}
	exitCode := 0
	return projectSandboxExecResponse{State: "succeeded", ExitCode: &exitCode, Stdout: "done"}, nil
}

func (f *sandboxWritebackGuardClient) advanceSource() {
	f.sourceRevision++
	f.sourceDigest = projectSandboxSyncDigest(projectSandboxFilesFromMap(f.files))
}

func cloneSandboxWritebackFiles(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for path, content := range in {
		out[path] = content
	}
	return out
}

func sandboxWritebackContentVersion(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func sandboxWritebackChanges(baseline, current map[string]string) []projectAssistantSandboxWorkspaceChange {
	paths := make(map[string]struct{}, len(baseline)+len(current))
	for path := range baseline {
		paths[path] = struct{}{}
	}
	for path := range current {
		paths[path] = struct{}{}
	}
	sorted := make([]string, 0, len(paths))
	for path := range paths {
		sorted = append(sorted, path)
	}
	sort.Strings(sorted)
	changes := make([]projectAssistantSandboxWorkspaceChange, 0)
	for _, path := range sorted {
		before, hadBefore := baseline[path]
		after, hasAfter := current[path]
		if hadBefore && hasAfter && before == after {
			continue
		}
		change := projectAssistantSandboxWorkspaceChange{Path: path, Content: after}
		switch {
		case !hadBefore && hasAfter:
			change.Operation = string(workspace.ManagedFileCreate)
		case hadBefore && !hasAfter:
			change.Operation = string(workspace.ManagedFileDelete)
			change.ExpectedVersion = sandboxWritebackContentVersion(before)
		default:
			change.Operation = string(workspace.ManagedFileReplace)
			change.ExpectedVersion = sandboxWritebackContentVersion(before)
		}
		changes = append(changes, change)
	}
	return changes
}
