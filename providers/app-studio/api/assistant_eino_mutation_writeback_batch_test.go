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
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/railgrid/provider-app-studio/workspace"
)

func TestEinoV2SandboxMutationsBatchAtSharedWritebackBoundary(t *testing.T) {
	ctx := context.Background()
	h, tool, state, sandbox, client := newDeferredMutationWritebackFixture(t, "deferred-batch-success")

	for _, mutation := range []struct {
		callID  string
		path    string
		content string
	}{
		{callID: "create-first", path: "src/first.ts", content: "first private edit\n"},
		{callID: "create-second", path: "src/second.ts", content: "second private edit\n"},
	} {
		result, err := tool.invokeAllowedTool(ctx, mutation.callID, tool.tool.Spec(), map[string]any{
			"path": mutation.path, "content": mutation.content,
		})
		if err != nil {
			t.Fatalf("invoke %s: %v", mutation.callID, err)
		}
		assertPendingPrivateWritebackResult(t, result)
		if _, err := h.workspaces.ReadFile(ctx, h.req.WorkspaceScope, workspace.ReadOptions{Path: mutation.path}); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s reached shared workspace before the boundary: %v", mutation.path, err)
		}
	}

	if got := client.checkpointDiffCount(); got != 0 {
		t.Fatalf("private mutations requested %d full workspace diffs before the boundary, want zero", got)
	}
	client.delegate.mu.Lock()
	privateFiles := cloneSandboxWritebackFiles(client.delegate.files)
	client.delegate.mu.Unlock()
	if privateFiles["src/first.ts"] != "first private edit\n" || privateFiles["src/second.ts"] != "second private edit\n" {
		t.Fatalf("private workspace after sequential edits = %#v", privateFiles)
	}
	checkpoint := state.CheckpointState().Sandbox
	if checkpoint == nil || len(checkpoint.Metadata.ApprovedMutations) != 2 {
		t.Fatalf("durable private mutation receipts = %#v, want both pending paths", checkpoint)
	}

	// Commit argument preparation is an existing shared-writeback boundary. It
	// should checkpoint both staged files with one remote diff before building
	// the commit's durable source bundle.
	arguments, err := tool.v2CommitArguments(ctx, map[string]any{"message": "batch edit"})
	if err != nil {
		t.Fatalf("checkpoint pending edits at commit boundary: %v", err)
	}
	if got := client.checkpointDiffCount(); got != 1 {
		t.Fatalf("boundary requested %d remote workspace diffs, want one for both edits", got)
	}
	if got := client.checkpointCreateCount(); got != 1 {
		t.Fatalf("boundary created %d fresh remote baselines, want one", got)
	}
	for path, want := range map[string]string{
		"src/first.ts":  "first private edit\n",
		"src/second.ts": "second private edit\n",
	} {
		got, err := h.workspaces.ReadFile(ctx, h.req.WorkspaceScope, workspace.ReadOptions{Path: path})
		if err != nil || got.Content != want {
			t.Fatalf("shared %s = %#v, %v; want %q", path, got, err, want)
		}
	}
	dirty, err := h.workspaces.UncommittedPaths(ctx, h.req.WorkspaceScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) != 2 {
		t.Fatalf("durable dirty paths = %#v, want both batched edits (args %#v)", dirty, arguments)
	}
	if receipts := sandbox.metadataSnapshot().ApprovedMutations; len(receipts) != 0 {
		t.Fatalf("successful batch checkpoint retained private approval receipts: %#v", receipts)
	}
}

func TestEinoV2SandboxBatchCheckpointConflictRemainsStructuredAndAtomic(t *testing.T) {
	ctx := context.Background()
	h, tool, _, sandbox, client := newDeferredMutationWritebackFixture(t, "deferred-batch-conflict")

	for _, mutation := range []struct {
		callID  string
		path    string
		content string
	}{
		{callID: "conflicting-create", path: "src/conflicting.ts", content: "private proposal\n"},
		{callID: "disjoint-create", path: "src/disjoint.ts", content: "disjoint proposal\n"},
	} {
		result, err := tool.invokeAllowedTool(ctx, mutation.callID, tool.tool.Spec(), map[string]any{
			"path": mutation.path, "content": mutation.content,
		})
		if err != nil {
			t.Fatalf("invoke %s: %v", mutation.callID, err)
		}
		assertPendingPrivateWritebackResult(t, result)
	}

	// A sibling owns the first path before the pending private batch reaches the
	// shared FileStore. The complete transaction must fail closed; the second
	// private file must not leak through as a partial batch.
	if _, err := h.workspaces.CreateFile(ctx, h.req.WorkspaceScope, workspace.CreateOptions{
		Path: "src/conflicting.ts", Content: "sibling edit\n",
	}); err != nil {
		t.Fatalf("create sibling conflict: %v", err)
	}
	_, err := tool.v2CommitArguments(ctx, map[string]any{"message": "batch edit"})
	var mutationErr *workspace.MutationError
	if !errors.As(err, &mutationErr) || mutationErr.Code == "" {
		t.Fatalf("commit boundary error = %v, want a structured shared-file mutation blocker", err)
	}
	if len(mutationErr.ChangedFiles) == 0 {
		t.Fatalf("structured blocker has no changed paths: %#v", mutationErr)
	}
	if got, err := h.workspaces.ReadFile(ctx, h.req.WorkspaceScope, workspace.ReadOptions{Path: "src/conflicting.ts"}); err != nil || got.Content != "sibling edit\n" {
		t.Fatalf("sibling bytes after failed batch = %#v, %v", got, err)
	}
	if _, err := h.workspaces.ReadFile(ctx, h.req.WorkspaceScope, workspace.ReadOptions{Path: "src/disjoint.ts"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("disjoint private bytes escaped failed atomic batch: %v", err)
	}
	if got := client.checkpointDiffCount(); got != 1 {
		t.Fatalf("failed boundary requested %d remote workspace diffs, want one", got)
	}
	if proposal := sandbox.metadataSnapshot().Reconciliations["src/conflicting.ts"].ConflictProposal; proposal == nil || proposal.Content != "private proposal\n" {
		t.Fatalf("structured conflict did not preserve the unapplied private proposal: %#v", proposal)
	}
}

func assertPendingPrivateWritebackResult(t *testing.T, result string) {
	t.Helper()
	var decoded struct {
		Writeback struct {
			Status  string `json:"status"`
			Scope   string `json:"scope"`
			Message string `json:"message"`
		} `json:"writeback"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("mutation result is not JSON: %q: %v", result, err)
	}
	if decoded.Writeback.Status != "pending" || decoded.Writeback.Scope != "private" || decoded.Writeback.Message == "" {
		t.Fatalf("mutation result writeback = %#v, want a visible pending private-scope receipt: %s", decoded.Writeback, result)
	}
}

func newDeferredMutationWritebackFixture(
	t *testing.T,
	requestID string,
) (projectAssistantV2ToolHarness, projectEinoAssistantTool, *projectEinoAssistantRunState, *projectAssistantRunSandbox, *deferredMutationWritebackSandboxClient) {
	t.Helper()
	ctx := context.Background()
	h := newProjectAssistantV2ToolHarness(t, requestID)
	if err := h.workspaces.ApplyFiles(ctx, h.req.WorkspaceScope, []workspace.File{{Path: "src/base.ts", Content: "base source\n"}}); err != nil {
		t.Fatal(err)
	}
	delegate := newSandboxWritebackGuardClient(map[string]string{"src/base.ts": "base source\n"})
	client := &deferredMutationWritebackSandboxClient{delegate: delegate}
	sandbox := newSandboxWritebackGuardSandbox(h.workspaces, h.req.WorkspaceScope, delegate, delegate.sourceDigest)
	sandbox.server = h.server
	sandbox.client = client
	sandbox.id = h.req.Identity
	sandbox.project = h.project
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	sandbox.runState = state
	state.SetSandbox(sandbox)

	backend, ok := h.server.projectAssistantToolRegistry().Get(projectToolCreateFile)
	if !ok {
		t.Fatal("create_file missing")
	}
	req := h.req
	req.ToolPort = projectAssistantV2DirectToolPort{}
	tool := projectEinoAssistantTool{server: h.server, tool: backend, req: req, runState: state}
	return h, tool, state, sandbox, client
}

type deferredMutationWritebackSandboxClient struct {
	delegate *sandboxWritebackGuardClient
	mu       sync.Mutex
	diffs    int
	creates  int
}

func (c *deferredMutationWritebackSandboxClient) Workspace(
	ctx context.Context,
	id identity,
	ref dataPlaneRef,
	request projectAssistantSandboxWorkspaceRequest,
) (projectAssistantSandboxWorkspaceResponse, error) {
	if request.Action == "checkpoint" {
		c.mu.Lock()
		if request.CheckpointAction == "create" {
			c.creates++
		} else {
			c.diffs++
		}
		c.mu.Unlock()
	}
	response, err := c.delegate.Workspace(ctx, id, ref, request)
	if err == nil && response.Mutation.Changed {
		switch request.Action {
		case "create":
			response.Mutation.Operation = projectToolCreateFile
		case "replace":
			response.Mutation.Operation = projectToolReplaceFile
		case "edit":
			response.Mutation.Operation = projectToolEditFile
		case "delete":
			response.Mutation.Operation = projectToolDeleteFile
		case "move":
			response.Mutation.Operation = projectToolMoveFile
		}
	}
	return response, err
}

func (c *deferredMutationWritebackSandboxClient) Exec(
	ctx context.Context,
	id identity,
	ref dataPlaneRef,
	request projectSandboxExecRequest,
) (projectSandboxExecResponse, error) {
	return c.delegate.Exec(ctx, id, ref, request)
}

func (c *deferredMutationWritebackSandboxClient) checkpointDiffCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.diffs
}

func (c *deferredMutationWritebackSandboxClient) checkpointCreateCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creates
}

var _ projectAssistantSandboxClient = (*deferredMutationWritebackSandboxClient)(nil)

func TestEinoV2MutationReductionPreservesPendingPrivateWriteback(t *testing.T) {
	call := schema.ToolCall{
		ID:       "private-write-call",
		Type:     "function",
		Function: schema.FunctionCall{Name: projectToolCreateFile, Arguments: `{"path":"src/new.ts"}`},
	}
	assistant := schema.AssistantMessage("", []schema.ToolCall{call})
	response := schema.ToolMessage(
		`{"operation":"create_file","path":"src/new.ts","changed":true,"writeback":{"status":"pending","scope":"private","message":"checkpoint before verification"}}`,
		call.ID,
		schema.WithToolName(projectToolCreateFile),
	)

	rewritten, err := projectEinoAssistantRewriteWorkspaceMutations(context.Background(), assistant, []*schema.Message{response})
	if err != nil {
		t.Fatal(err)
	}
	if len(rewritten) != 1 || !projectEinoAssistantSyntheticWorkspaceMutationEvidence(rewritten[0]) {
		t.Fatalf("rewrite = %#v, want one provenance-marked compacted mutation message", rewritten)
	}
	content := rewritten[0].Content
	for _, want := range []string{"private workspace", "shared-project writeback was pending", "do not repeat"} {
		if !strings.Contains(content, want) {
			t.Fatalf("compacted mutation evidence %q lost pending-writeback detail %q", content, want)
		}
	}
}
