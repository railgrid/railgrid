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
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

type projectAssistantV2DirectToolPort struct{}

func (projectAssistantV2DirectToolPort) DiscoverMCP(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, bool, error) {
	return nil, false, nil
}

func (projectAssistantV2DirectToolPort) Invoke(ctx context.Context, tool projectAssistantTool, req projectAssistantToolCallRequest) (string, error) {
	return tool.Call(ctx, req)
}

type projectAssistantV2CountingCommitPort struct {
	calls int
}

func (*projectAssistantV2CountingCommitPort) DiscoverMCP(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, bool, error) {
	return nil, false, nil
}

func (p *projectAssistantV2CountingCommitPort) Invoke(context.Context, projectAssistantTool, projectAssistantToolCallRequest) (string, error) {
	p.calls++
	return `{"phase":"Succeeded","message":"committed"}`, nil
}

type projectAssistantV2ToolHarness struct {
	server     *Server
	messages   *store.MemoryStore
	workspaces *workspace.FileStore
	project    *aiv1alpha1.Project
	scope      store.Scope
	req        projectAssistantRunRequest
}

func newProjectAssistantV2ToolHarness(t *testing.T, requestID string) projectAssistantV2ToolHarness {
	t.Helper()
	ctx := context.Background()
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "test-project-uid-demo"}}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", user: "alice"}
	scope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	started, err := server.startProjectAssistantRunDurablyWithMode(
		ctx,
		scope,
		id.user,
		"update the app",
		requestID,
		store.AssistantRunModeDefault,
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.projectAssistantSupervisor().Attach(scope, started.Run, started.Assistant); err != nil {
		t.Fatal(err)
	}
	run := started.Run
	return projectAssistantV2ToolHarness{
		server: server, messages: messages, workspaces: workspaces, project: project, scope: scope,
		req: projectAssistantRunRequest{
			Identity: id, Project: project, Workspace: workspaces,
			WorkspaceScope: projectWorkspaceScope(id, project), MessageScope: scope,
			ToolPort: projectAssistantV2DirectToolPort{}, AssistantRun: &run,
			ApprovalMode: run.ApprovalMode, CollaborationMode: projectAssistantCollaborationModeDefault,
			eventLedger: newProjectAssistantRunEventLedger(messages, scope, run.ID),
		},
	}
}

func TestEinoV2MutationReplayDispatchesExactlyOnce(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "v2-replay")
	var calls int
	backend := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			calls++
			return `{"operation":"apply_patch","paths":["src/App.tsx"],"additions":1}`, nil
		},
	}
	runState := newProjectEinoAssistantRunState()
	tool := projectEinoAssistantTool{server: h.server, tool: backend, req: h.req, runState: runState}
	args := map[string]any{"patch": "*** Begin Patch\n*** Add File: src/App.tsx\n+ok\n*** End Patch"}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tool.invokeAllowedTool(context.Background(), "call-patch", backend.Spec(), args); err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
	}
	if calls != 1 {
		t.Fatalf("backend calls = %d, want exactly one", calls)
	}
	if current, _ := runState.SourceMutationRevisions(); current != 1 {
		t.Fatalf("source mutation revision = %d, want one", current)
	}
	events, err := h.messages.ListAssistantRunEvents(context.Background(), h.scope, h.req.AssistantRun.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("durable events = %#v, want one call/result pair", events)
	}
}

func TestEinoV2RejectsIdenticalFailedPatchAtSameRevision(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "v2-failed-patch-fingerprint")
	var calls int
	backend := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			calls++
			return "", &workspace.PatchError{
				Code:            workspace.PatchErrorContextNotFound,
				Path:            "src/App.tsx",
				Hunk:            1,
				Message:         "expected source was not found",
				ExpectedContext: "old",
				ActualContext:   "current",
			}
		},
	}
	runState := newProjectEinoAssistantRunState()
	tool := projectEinoAssistantTool{server: h.server, tool: backend, req: h.req, runState: runState}
	args := map[string]any{"patch": "*** Begin Patch\n*** Update File: src/App.tsx\n@@\n-old\n+new\n*** End Patch"}

	first, err := tool.invokeAllowedTool(context.Background(), "call-first", backend.Spec(), args)
	if err != nil {
		t.Fatal(err)
	}
	var firstResult struct {
		Status string               `json:"status"`
		Error  workspace.PatchError `json:"error"`
	}
	if err := json.Unmarshal([]byte(first), &firstResult); err != nil {
		t.Fatalf("decode first failure: %v; result=%q", err, first)
	}
	if firstResult.Status != "failed" || firstResult.Error.Code != workspace.PatchErrorContextNotFound || firstResult.Error.SourceMutationRevision != 0 {
		t.Fatalf("first result = %#v", firstResult)
	}
	if !strings.Contains(first, `"sourceMutationRevision":0`) {
		t.Fatalf("first result omitted the current source mutation revision: %s", first)
	}

	checkpoint := runState.CheckpointState()
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	tool.runState = restored
	secondArgs := map[string]any{"patch": strings.ReplaceAll(projectToolString(args["patch"]), "\n", "\r\n") + "\r\n"}
	second, err := tool.invokeAllowedTool(context.Background(), "call-second", backend.Spec(), secondArgs)
	if err != nil {
		t.Fatal(err)
	}
	var secondResult struct {
		Status string               `json:"status"`
		Error  workspace.PatchError `json:"error"`
	}
	if err := json.Unmarshal([]byte(second), &secondResult); err != nil {
		t.Fatalf("decode duplicate failure: %v; result=%q", err, second)
	}
	if secondResult.Error.Code != workspace.PatchErrorStrategyChange || calls != 1 {
		t.Fatalf("second result = %#v, backend calls = %d; want strategy change without redispatch", secondResult, calls)
	}

	changedArgs := map[string]any{"patch": strings.Replace(projectToolString(args["patch"]), "+new", "+different", 1)}
	if _, err := tool.invokeAllowedTool(context.Background(), "call-changed", backend.Spec(), changedArgs); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("backend calls after changed patch = %d, want 2", calls)
	}
	restored.RecordSourceMutation()
	if _, err := tool.invokeAllowedTool(context.Background(), "call-new-revision", backend.Spec(), args); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("backend calls after revision advance = %d, want 3", calls)
	}
}

func TestEinoV2MoveTracksSourceAndDestinationAsDirty(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "v2-move-dirty-paths")
	ctx := context.Background()
	writeTestWorkspaceFiles(t, ctx, h.workspaces, h.req.WorkspaceScope, []workspace.File{{Path: "src/old.ts", Content: "old\n"}})
	backend, ok := h.server.projectAssistantToolRegistry().Get(projectToolApplyPatch)
	if !ok {
		t.Fatal("apply_patch missing")
	}
	runState := newProjectEinoAssistantRunState()
	tool := projectEinoAssistantTool{server: h.server, tool: backend, req: h.req, runState: runState}
	args := map[string]any{"patch": "*** Begin Patch\n*** Update File: src/old.ts\n*** Move to: src/new.ts\n@@\n-old\n+new\n*** End Patch"}
	if _, err := tool.invokeAllowedTool(ctx, "call-move", backend.Spec(), args); err != nil {
		t.Fatal(err)
	}
	dirty, err := h.workspaces.UncommittedPaths(ctx, h.req.WorkspaceScope)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(dirty, ","); got != "src/new.ts,src/old.ts" {
		t.Fatalf("dirty paths = %q", got)
	}
	if _, err := h.workspaces.WorkspaceDigest(ctx, h.req.WorkspaceScope, dirty); err != nil {
		t.Fatalf("digest moved workspace: %v", err)
	}
}

type projectAssistantIncompleteThenCompleteModel struct {
	calls            int
	partialToolCall  bool
	alwaysIncomplete bool
	setupErrors      int
}

func (m *projectAssistantIncompleteThenCompleteModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	return nil, errors.New("Generate should not be called")
}

func (m *projectAssistantIncompleteThenCompleteModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	if m.calls <= m.setupErrors {
		return nil, io.ErrUnexpectedEOF
	}
	message := schema.AssistantMessage("discarded partial", nil)
	if m.partialToolCall {
		message = schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "partial-call",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolApplyPatch,
				Arguments: `{"patch":`,
			},
		}})
	}
	if m.calls > 1 && !m.alwaysIncomplete {
		message = schema.AssistantMessage("recovered response", nil)
		message.ResponseMeta = &schema.ResponseMeta{FinishReason: "stop"}
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestEinoV2PublishesReconnectForPreStreamFailure(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "v2-pre-stream-retry")
	model := &projectAssistantIncompleteThenCompleteModel{setupErrors: 1}
	h.req.LLM = projectLLMSettings{
		Provider:          defaultProjectLLMProvider,
		MaxRetries:        5,
		RetryBackoff:      time.Millisecond,
		StreamIdleTimeout: time.Second,
	}
	var statuses []string
	h.req.StreamCallbacks.OnStatus = func(status string) { statuses = append(statuses, status) }
	engine := projectEinoAssistantEngine{
		server: h.server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}

	result, err := engine.StreamProjectAssistant(context.Background(), h.req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "recovered response" || model.calls != 2 {
		t.Fatalf("result = %q after %d calls, want recovered response after setup retry", result.Content, model.calls)
	}
	if len(statuses) != 1 || statuses[0] != "Model connection was interrupted; reconnecting 1/5" {
		t.Fatalf("statuses = %#v, want one reconnect warning", statuses)
	}
}

func TestEinoV2ExhaustionStopsAtConfiguredRetryCount(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "v2-stream-exhaustion")
	model := &projectAssistantIncompleteThenCompleteModel{alwaysIncomplete: true}
	h.req.LLM = projectLLMSettings{
		Provider:          defaultProjectLLMProvider,
		MaxRetries:        5,
		RetryBackoff:      time.Millisecond,
		StreamIdleTimeout: time.Second,
	}
	var statuses []string
	h.req.StreamCallbacks.OnStatus = func(status string) { statuses = append(statuses, status) }
	engine := projectEinoAssistantEngine{
		server: h.server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}

	_, err := engine.StreamProjectAssistant(context.Background(), h.req)
	var incomplete *projectEinoAssistantIncompleteStreamError
	if !errors.As(err, &incomplete) {
		t.Fatalf("terminal error = %v, want original incomplete-stream error", err)
	}
	if model.calls != 6 {
		t.Fatalf("model calls = %d, want initial call plus five retries", model.calls)
	}
	if len(statuses) != 5 || statuses[len(statuses)-1] != "Model connection was interrupted; reconnecting 5/5" {
		t.Fatalf("statuses = %#v, want exactly 1/5 through 5/5", statuses)
	}
}

func TestEinoV2DoesNotDispatchToolFromRejectedIncompleteResponse(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "v2-incomplete-tool-stream")
	model := &projectAssistantIncompleteThenCompleteModel{partialToolCall: true}
	h.req.LLM = projectLLMSettings{
		Provider:          defaultProjectLLMProvider,
		MaxRetries:        5,
		RetryBackoff:      time.Millisecond,
		StreamIdleTimeout: time.Second,
	}
	backendCalls := 0
	backend := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			backendCalls++
			return `{"operation":"apply_patch","paths":["src/App.tsx"]}`, nil
		},
	}
	engine := projectEinoAssistantEngine{
		server: h.server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(h.server, backend, req, state)}, nil
		},
	}

	result, err := engine.StreamProjectAssistant(context.Background(), h.req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "recovered response" || model.calls != 2 {
		t.Fatalf("result = %q after %d calls, want recovered response after one retry", result.Content, model.calls)
	}
	if backendCalls != 0 {
		t.Fatalf("backend calls = %d, want no dispatch from rejected incomplete response", backendCalls)
	}
	events, err := h.messages.ListAssistantRunEvents(context.Background(), h.scope, h.req.AssistantRun.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("durable tool events = %#v, want none from rejected incomplete response", events)
	}
}

func TestEinoV2RecoversIncompleteChatCompletionLikeCodex(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "v2-incomplete-stream")
	model := &projectAssistantIncompleteThenCompleteModel{}
	h.req.LLM = projectLLMSettings{
		Provider:          defaultProjectLLMProvider,
		MaxRetries:        5,
		RetryBackoff:      time.Millisecond,
		StreamIdleTimeout: time.Second,
	}
	var statuses []string
	var accepted []string
	h.req.StreamCallbacks = projectAssistantStreamCallbacks{
		OnStatus: func(status string) { statuses = append(statuses, status) },
		OnChunk:  func(content string) { accepted = append(accepted, content) },
	}
	engine := projectEinoAssistantEngine{
		server: h.server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}

	result, err := engine.StreamProjectAssistant(context.Background(), h.req)
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls = %d, want one retry", model.calls)
	}
	if result.Content != "recovered response" || strings.Join(accepted, "") != "recovered response" {
		t.Fatalf("accepted response = (%q, %#v), want only recovered response", result.Content, accepted)
	}
	foundReconnect := false
	for _, status := range statuses {
		if status == "Model connection was interrupted; reconnecting 1/5" {
			foundReconnect = true
		}
	}
	if !foundReconnect {
		t.Fatalf("statuses = %#v, want reconnect 1/5", statuses)
	}
}

func TestEinoV2PartialPatchRollbackTracksActualDeltaOnce(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "v2-partial-patch")
	actual := workspace.MutationResult{
		Operation: "apply_patch", Path: "src/App.tsx", Paths: []string{"src/App.tsx"}, Additions: 1, Deletions: 1,
	}
	var calls int
	backend := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			calls++
			return projectAssistantToolJSONResult(actual, &workspace.PatchError{
				Code: workspace.PatchErrorApplyFailed, Path: "src/theme.css",
				Message: "patch application failed; rollback was incomplete", ActualChanges: []workspace.MutationResult{actual},
			})
		},
	}
	runState := newProjectEinoAssistantRunState()
	runState.RecordObservedReadFile("src/App.tsx")
	tool := projectEinoAssistantTool{server: h.server, tool: backend, req: h.req, runState: runState}
	args := map[string]any{"patch": "*** Begin Patch\n*** Update File: src/App.tsx\n@@\n-old\n+new\n*** End Patch"}
	result, err := tool.invokeAllowedTool(context.Background(), "call-partial", backend.Spec(), args)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatal(err)
	}
	if projectToolString(decoded["status"]) != "partial_failure" || !strings.Contains(projectToolString(decoded["message"]), "remain changed") {
		t.Fatalf("partial result = %#v", decoded)
	}
	if current, verified := runState.SourceMutationRevisions(); current != 1 || verified != 0 {
		t.Fatalf("source revisions = (%d, %d), want (1, 0)", current, verified)
	}
	if got := strings.Join(runState.SuccessfulMutationPaths(), ","); got != "src/App.tsx" {
		t.Fatalf("changed paths = %q, want src/App.tsx", got)
	}
	if got := runState.ObservedReadFilePaths(); len(got) != 0 {
		t.Fatalf("stale read coverage = %#v, want invalidated", got)
	}
	replayed, err := tool.invokeAllowedTool(context.Background(), "call-partial", backend.Spec(), args)
	if err != nil || replayed != result || calls != 1 {
		t.Fatalf("replay = (%q, %v), calls=%d; want exact durable result and one dispatch", replayed, err, calls)
	}
}

func TestEinoV2CommitWorkspaceDigestRejectsPostApprovalChange(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{Path: "src/App.tsx", Content: "before\n"}})
	if _, err := workspaces.AddUncommittedPaths(ctx, scope, []string{"src/App.tsx"}); err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.RecordSuccessfulMutationPath("src/App.tsx")
	runState.RecordSourceMutation()
	runState.RecordDevelopmentVerificationResult(`{"checkedMutationRevision":1,"status":"ready"}`)
	digest, err := projectEinoAssistantWorkspaceDigest(ctx, workspaces, scope, []string{"src/App.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	runState.RecordVerifiedWorkspaceDigest(digest)
	tool := projectEinoAssistantTool{req: projectAssistantRunRequest{Workspace: workspaces, WorkspaceScope: scope}, runState: runState}
	args, err := tool.v2CommitArguments(ctx, map[string]any{"paths": []any{"src/App.tsx"}, "message": "Update app"})
	if err != nil {
		t.Fatal(err)
	}
	if got := projectToolString(args["workspaceDigest"]); got != digest {
		t.Fatalf("bound digest = %q, want %q", got, digest)
	}
	if err := tool.validateV2CommitWorkspace(ctx, args); err != nil {
		t.Fatalf("unchanged workspace rejected: %v", err)
	}
	if _, err := workspaces.WriteFile(ctx, scope, workspace.WriteOptions{Path: "src/App.tsx", Content: "after\n"}); err != nil {
		t.Fatal(err)
	}
	if err := tool.validateV2CommitWorkspace(ctx, args); err == nil || !strings.Contains(err.Error(), "changed after commit approval") {
		t.Fatalf("changed workspace error = %v", err)
	}
}

func TestEinoV2CommitUsesCompleteDirtyBundleAndRejectsMembershipChange(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{
		{Path: "src/App.tsx", Content: "app\n"},
		{Path: "src/api.ts", Content: "api\n"},
		{Path: "src/new.ts", Content: "new\n"},
	})
	if _, err := workspaces.AddUncommittedPaths(ctx, scope, []string{"src/App.tsx", "src/api.ts"}); err != nil {
		t.Fatal(err)
	}
	tool := projectEinoAssistantTool{
		req:      projectAssistantRunRequest{Workspace: workspaces, WorkspaceScope: scope},
		runState: newProjectEinoAssistantRunState(),
	}
	args, err := tool.v2CommitArguments(ctx, map[string]any{
		"paths":   []any{"src/App.tsx"},
		"message": "Commit the project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(projectToolStringList(args["paths"]), ","); got != "src/App.tsx,src/api.ts" {
		t.Fatalf("server commit bundle = %q, want complete dirty set", got)
	}
	if err := tool.validateV2CommitWorkspace(ctx, args); err != nil {
		t.Fatalf("unchanged complete bundle rejected: %v", err)
	}
	if _, err := workspaces.AddUncommittedPaths(ctx, scope, []string{"src/new.ts"}); err != nil {
		t.Fatal(err)
	}
	if err := tool.validateV2CommitWorkspace(ctx, args); err == nil || !strings.Contains(err.Error(), "membership changed") {
		t.Fatalf("changed membership error = %v, want approval invalidation", err)
	}
}

func TestEinoV2CommitStateBindsVerifiedAndCommittedDigest(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.RecordSourceMutation()
	runState.RecordDevelopmentVerificationResult(`{"checkedMutationRevision":1,"status":"ready"}`)
	runState.RecordVerifiedWorkspaceDigest("sha256:same")
	runState.RecordSourceCommit("sha256:same")
	checkpoint := runState.CheckpointState()
	if checkpoint.VerifiedWorkspaceDigest != "sha256:same" || checkpoint.CommittedWorkspaceDigest != "sha256:same" {
		t.Fatalf("checkpoint digests = verified %q committed %q, want same digest", checkpoint.VerifiedWorkspaceDigest, checkpoint.CommittedWorkspaceDigest)
	}
	evidence := runState.CompletionEvidence()
	if !evidence.LatestMutationVerified || !evidence.LatestMutationCommitted {
		t.Fatalf("completion evidence = %#v, want verified and committed for the same digest", evidence)
	}

	unverified := newProjectEinoAssistantRunState()
	unverified.RecordSourceMutation()
	unverified.RecordSourceCommit("sha256:commit-only")
	checkpoint = unverified.CheckpointState()
	if checkpoint.CommittedWorkspaceDigest != "sha256:commit-only" || checkpoint.VerifiedWorkspaceDigest != "" {
		t.Fatalf("unverified commit checkpoint = %#v", checkpoint)
	}
	if evidence := unverified.CompletionEvidence(); evidence.LatestMutationVerified || !evidence.LatestMutationCommitted {
		t.Fatalf("unverified commit evidence = %#v, want committed without verified", evidence)
	}
}

func TestProjectEinoAssistantFinalContentCarriesServerOwnedPreviewEvidenceScope(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.RecordToolMessage(chatMessage{
		Role:       "tool",
		Name:       projectToolInspectDevelopmentPreview,
		ToolCallID: "inspect-1",
		Content:    `{"status":"succeeded","evidenceScope":"rendered_state_only","interactionEvidence":false}`,
	})
	result := projectEinoAssistantResultWithCompletion(projectAssistantRunResult{Content: "Everything is working."}, runState)
	for _, want := range []string{
		"Everything is working.",
		"Verification scope: rendered state and accessibility assertions passed.",
		"source-reviewed but not exercised by the read-only preview inspector",
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("final content = %q, want %q", result.Content, want)
		}
	}
	if duplicated := projectEinoAssistantFinalContentWithEvidenceScope(result.Content, runState); duplicated != result.Content {
		t.Fatalf("evidence note duplicated: %q", duplicated)
	}
}

func TestInitialProjectPlanProgressBoundsLabelsForDurablePresentation(t *testing.T) {
	longStep := "Create backend server.js with PostgreSQL schema, horse seeding, and API routes (GET /api/horses, POST /api/swipe, GET /api/matches)"
	if len(longStep) <= projectEinoAssistantTodoProgressMaxLabelBytes {
		t.Fatalf("test step length = %d, want more than %d bytes", len(longStep), projectEinoAssistantTodoProgressMaxLabelBytes)
	}
	plan := projectAssistantApprovedPlan{Steps: []string{
		"Create backend package.json with Express, pg, cors dependencies",
		longStep,
	}}
	progress := projectAssistantInitialPlanProgress(plan)
	if !projectAssistantPlanSnapshotValid(progress) {
		t.Fatalf("initial plan progress = %#v, want valid durable metadata", progress)
	}
	if progress.Steps[0].Status != "in_progress" || progress.Steps[1].Status != "pending" {
		t.Fatalf("initial plan statuses = %#v, want first step active", progress.Steps)
	}
	if got := progress.Steps[1].Content; len(got) > projectEinoAssistantTodoProgressMaxLabelBytes || !strings.HasSuffix(got, "...") {
		t.Fatalf("bounded plan label = %q (%d bytes)", got, len(got))
	}
	metadata := projectAssistantDurableMetadataForTransition(
		store.AssistantRun{ID: "run-plan", Revision: 1},
		"Building · 0 of 2 steps",
		false,
		false,
		nil,
		&progress,
	)
	if _, ok := metadata[projectAssistantMetadataPlan]; !ok {
		t.Fatalf("durable metadata dropped bounded plan: %#v", metadata)
	}

	runState := newProjectEinoAssistantRunState()
	runState.SetExecutionPlan(plan)
	runState.SetPlanProgress(progress)
	runState.CompleteExecutionPlan()
	if !runState.ExecutionPlanComplete() || !runState.CompletionEvidence().PlanComplete {
		t.Fatalf("bounded presentation labels broke execution-plan completion: %#v", runState.PlanProgress())
	}
}

func TestInitialProjectPlanRequiresBoundTemplateAndPreservesAuthority(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(projectAssistantInitialCreationPlan("Build a storefront"))
	tool := projectEinoAssistantTool{
		req:      projectAssistantRunRequest{Project: &aiv1alpha1.Project{}},
		runState: runState,
	}
	result, err := tool.invokeInitialProjectPlanTool(
		context.Background(),
		"call-plan",
		projectAssistantToolSpec{Name: projectToolDefineInitialProjectPlan, Risk: projectAssistantToolRiskPlan},
		map[string]any{
			"summary":            "Build the storefront",
			"steps":              []any{"Create the app"},
			"targetPaths":        []any{"web/"},
			"acceptanceCriteria": []any{"The storefront starts"},
		},
	)
	if err != nil || !strings.Contains(result, "template_not_bound") {
		t.Fatalf("plan before template = (%q, %v), want actionable rejection", result, err)
	}
	if authority := runState.ApprovedPlan(); authority == nil || !authority.AllowAllWrites || authority.ApprovalTool != "project_create_prompt" {
		t.Fatalf("creation authority after rejected plan = %#v", authority)
	}
}

func TestTemplateSelectionInvalidatesExecutionPlanAndRepairsLegacyAuthority(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	legacy := projectAssistantApprovedPlan{
		Goal:         "Build a storefront",
		TargetPaths:  []string{"web/"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		ApprovalTool: projectToolDefineInitialProjectPlan,
		RunLocal:     true,
	}
	runState.ApprovePlan(legacy)
	runState.SetExecutionPlan(legacy)
	project := &aiv1alpha1.Project{Spec: aiv1alpha1.ProjectSpec{
		Template: &aiv1alpha1.ProjectTemplateSpec{Name: "simple-webapp"},
	}}
	tool := projectEinoAssistantTool{
		req:      projectAssistantRunRequest{Project: project},
		runState: runState,
	}
	tool.refreshInitialBuildAfterTemplateSelection(context.Background())

	if plan := runState.ExecutionPlan(); plan != nil {
		t.Fatalf("execution plan survived template selection: %#v", plan)
	}
	if authority := runState.ApprovedPlan(); authority == nil || !authority.AllowAllWrites || authority.ApprovalTool != "project_create_prompt" {
		t.Fatalf("repaired creation authority = %#v", authority)
	}
}

func TestEinoV2CommitSettlementClearsCompleteDirtyBundleAtToolBoundary(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{Path: "src/App.tsx", Content: "app\n"}})
	if _, err := workspaces.AddUncommittedPaths(ctx, scope, []string{"src/App.tsx"}); err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.RecordSourceMutation()
	tool := projectEinoAssistantTool{
		req:      projectAssistantRunRequest{Workspace: workspaces, WorkspaceScope: scope},
		runState: runState,
	}
	digest, err := projectEinoAssistantWorkspaceDigest(ctx, workspaces, scope, []string{"src/App.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tool.recordV2CommitSettlement(ctx, projectAssistantToolSpec{Name: projectToolCommitProjectFiles, Risk: projectAssistantToolRiskCommit}, map[string]any{
		"paths":           []any{"src/App.tsx"},
		"workspaceDigest": digest,
	}, true); err != nil {
		t.Fatal(err)
	}
	paths, err := workspaces.UncommittedPaths(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("dirty paths after successful tool-boundary settlement = %#v, want cleared", paths)
	}
	if got := runState.CheckpointState().CommittedWorkspaceDigest; got != digest {
		t.Fatalf("committed workspace digest = %q, want %q", got, digest)
	}
}

func TestEinoV2SuccessfulCommitReplayRepairsLocalSettlement(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{Path: "src/App.tsx", Content: "app\n"}})
	if _, err := workspaces.AddUncommittedPaths(ctx, scope, []string{"src/App.tsx"}); err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.RecordSourceMutation()
	tool := projectEinoAssistantTool{
		req:      projectAssistantRunRequest{Workspace: workspaces, WorkspaceScope: scope},
		runState: runState,
	}
	digest, err := projectEinoAssistantWorkspaceDigest(ctx, workspaces, scope, []string{"src/App.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"paths":           []any{"src/App.tsx"},
		"workspaceDigest": digest,
	}
	spec := projectAssistantToolSpec{Name: projectToolCommitProjectFiles, Risk: projectAssistantToolRiskCommit}
	if err := tool.recordV2CommitSettlement(ctx, spec, args, false); err != nil {
		t.Fatal(err)
	}
	if _, count := runState.RepeatedCompletedAction(); count != 1 {
		t.Fatalf("completed action count before replay = %d, want 1", count)
	}
	if _, err := tool.replayDurableToolCall(ctx, "call-commit", projectAssistantToolSpec{
		Name: projectToolCommitProjectFiles,
		Risk: projectAssistantToolRiskCommit,
	}, args, projectAssistantRunToolCallOutcome{
		Result:      `{"phase":"Succeeded"}`,
		Disposition: projectAssistantToolDispositionSucceeded,
	}); err != nil {
		t.Fatal(err)
	}
	if _, count := runState.RepeatedCompletedAction(); count != 1 {
		t.Fatalf("completed action count after replay = %d, want unchanged at 1", count)
	}
	paths, err := workspaces.UncommittedPaths(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("dirty paths after successful commit replay = %#v, want cleared", paths)
	}
	if got := runState.CheckpointState().CommittedWorkspaceDigest; got != digest {
		t.Fatalf("replayed committed workspace digest = %q, want %q", got, digest)
	}
}

func TestEinoV2UnknownHandlerDynamicCommitSettlesAndReplaysExactlyOnce(t *testing.T) {
	ctx := context.Background()
	h := newProjectAssistantV2ToolHarness(t, "v2-dynamic-commit-settlement")
	defer h.server.Shutdown(ctx)
	writeTestWorkspaceFiles(t, ctx, h.workspaces, h.req.WorkspaceScope, []workspace.File{{Path: "src/App.tsx", Content: "app\n"}})
	if _, err := h.workspaces.AddUncommittedPaths(ctx, h.req.WorkspaceScope, []string{"src/App.tsx"}); err != nil {
		t.Fatal(err)
	}
	port := &projectAssistantV2CountingCommitPort{}
	h.req.ToolPort = port
	h.req.Repository = &ProjectRepositoryView{Ref: "demo-repo", Status: projectRepositoryStatusReady, Ready: true}
	h.req.ApprovalMode = store.AssistantApprovalModeAutoApprove
	h.req.TurnProfile = projectAssistantTurnProfileImplementation
	h.req.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	runState.SetProjectRepositoryRef("demo-repo")
	runState.RecordSourceMutation()
	runState.SetToolDiscovery(projectEinoAssistantToolDiscovery{IncludeCommitBridge: true})

	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		UnknownToolsHandler: projectEinoUnknownToolHandler(h.server, h.req, runState),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call-dynamic-commit",
		Function: schema.FunctionCall{
			Name:      projectToolCommitProjectFiles,
			Arguments: `{"message":"commit the complete bundle"}`,
		},
	}})
	for attempt := 0; attempt < 2; attempt++ {
		messages, err := node.Invoke(ctx, input)
		if err != nil {
			t.Fatalf("dynamic commit attempt %d: %v", attempt+1, err)
		}
		if len(messages) != 1 || !strings.Contains(messages[0].Content, `"phase":"Succeeded"`) {
			contents := make([]string, 0, len(messages))
			for _, message := range messages {
				contents = append(contents, message.Content)
			}
			t.Fatalf("dynamic commit attempt %d result = %#v, want typed success", attempt+1, contents)
		}
	}
	if port.calls != 1 {
		t.Fatalf("dynamic commit backend calls = %d, want exactly one", port.calls)
	}
	paths, err := h.workspaces.UncommittedPaths(ctx, h.req.WorkspaceScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("dirty paths after dynamic commit settlement = %#v, want cleared", paths)
	}
	if got := runState.CheckpointState().CommittedWorkspaceDigest; got == "" {
		t.Fatal("dynamic commit did not record the committed workspace digest")
	}
	if _, count := runState.RepeatedCompletedAction(); count != 1 {
		t.Fatalf("dynamic commit completed action count = %d, want replay excluded", count)
	}
}

func TestEinoV2CommitSettlementFailureStopsAfterDurableCommitOutcome(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspaces := workspace.NewFileStore(root)
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{Path: "src/App.tsx", Content: "app\n"}})
	if _, err := workspaces.AddUncommittedPaths(ctx, scope, []string{"src/App.tsx"}); err != nil {
		t.Fatal(err)
	}
	settlementPath := filepath.Join(root, ".assistant-snapshots", "org-a", "ws-1", "demo", "project-uid", "commit-settlement.json")
	if err := os.Mkdir(settlementPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.RecordSourceMutation()
	tool := projectEinoAssistantTool{
		req:      projectAssistantRunRequest{Workspace: workspaces, WorkspaceScope: scope},
		runState: runState,
	}
	err := tool.recordV2CommitSettlement(ctx, projectAssistantToolSpec{
		Name: projectToolCommitProjectFiles,
		Risk: projectAssistantToolRiskCommit,
	}, map[string]any{
		"paths":           []any{"src/App.tsx"},
		"workspaceDigest": "sha256:bundle",
	}, true)
	if err != nil {
		t.Fatalf("model-visible settlement error = %v, want successful external outcome preserved", err)
	}
	checkpoint := runState.CheckpointState()
	if checkpoint.CommittedWorkspaceDigest != "sha256:bundle" {
		t.Fatalf("committed digest after failed local settlement = %q, want external success retained", checkpoint.CommittedWorkspaceDigest)
	}
	if len(checkpoint.VerificationBlockers) == 0 || !strings.Contains(checkpoint.VerificationBlockers[0], "local workspace settlement") {
		t.Fatalf("settlement blockers = %#v, want server-owned blocker", checkpoint.VerificationBlockers)
	}
}

func TestEinoV2RestoresDurableDirtyBundleIntoCurrentMutationRevision(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{Path: "src/App.tsx", Content: "app\n"}})
	if _, err := workspaces.AddUncommittedPaths(ctx, scope, []string{"src/App.tsx"}); err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	if err := (projectEinoAssistantEngine{}).restoreProjectAssistantDirtyBundle(ctx, projectAssistantRunRequest{
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo"}},
		Workspace:      workspaces,
		WorkspaceScope: scope,
	}, runState); err != nil {
		t.Fatal(err)
	}
	if revision, _ := runState.SourceMutationRevisions(); revision != 1 {
		t.Fatalf("restored mutation revision = %d, want 1", revision)
	}
	if got := strings.Join(runState.SuccessfulMutationPaths(), ","); got != "src/App.tsx" {
		t.Fatalf("restored paths = %q, want src/App.tsx", got)
	}
}

func TestEinoV2RestoresCommitSettlementBeforeDirtyBundle(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{Path: "src/App.tsx", Content: "app\n"}})
	if _, err := workspaces.AddUncommittedPaths(ctx, scope, []string{"src/App.tsx"}); err != nil {
		t.Fatal(err)
	}
	digest, err := projectEinoAssistantWorkspaceDigest(ctx, workspaces, scope, []string{"src/App.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	if err := workspaces.RecordCommitSettlement(ctx, scope, digest, []string{"src/App.tsx"}); err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	if err := (projectEinoAssistantEngine{}).restoreProjectAssistantDirtyBundle(ctx, projectAssistantRunRequest{
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo"}},
		Workspace:      workspaces,
		WorkspaceScope: scope,
	}, runState); err != nil {
		t.Fatal(err)
	}
	paths, err := workspaces.UncommittedPaths(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("dirty paths after settlement restoration = %#v, want cleared", paths)
	}
	if revision, _ := runState.SourceMutationRevisions(); revision != 0 {
		t.Fatalf("settled commit restored mutation revision = %d, want 0", revision)
	}
}

func TestEinoV2UsesPriorUncommittedPathsWithoutRestoringMutationRevision(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{Path: "package.json", Content: `{"name":"demo"}`}})
	if _, err := workspaces.AddUncommittedPaths(ctx, scope, []string{"package.json"}); err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	req := projectAssistantRunRequest{Workspace: workspaces, WorkspaceScope: scope, CollaborationMode: projectAssistantCollaborationModeDefault}
	digest, err := projectEinoAssistantWorkspaceDigest(ctx, workspaces, scope, []string{"package.json"})
	if err != nil {
		t.Fatal(err)
	}
	args, err := (projectEinoAssistantTool{req: req, runState: runState}).v2CommitArguments(ctx, map[string]any{
		"paths": []any{"package.json"}, "message": "Commit pending source",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(projectToolStringList(args["paths"]), ","); got != "package.json" {
		t.Fatalf("commit paths = %q, want package.json", got)
	}
	if got := projectToolString(args["workspaceDigest"]); got != digest {
		t.Fatalf("workspace digest = %q, want %q", got, digest)
	}
	if revision, _ := runState.SourceMutationRevisions(); revision != 0 {
		t.Fatalf("durable dirty paths manufactured mutation revision %d", revision)
	}
}

func TestEinoV2ResumeDoesNotTreatPlanAsMutationAuthority(t *testing.T) {
	ctx := context.Background()
	h := newProjectAssistantV2ToolHarness(t, "v2-resume-run-local-grant")
	writeTestWorkspaceFiles(t, ctx, h.workspaces, h.req.WorkspaceScope, []workspace.File{{
		Path: "src/App.tsx",
		Content: `export function App() {
  const greeting = "hello";
  const audience = "world";
  return greeting + " " + audience;
}
`,
	}})

	grant := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Goal:               "Update the app greeting",
		Summary:            "Change the greeting in src/App.tsx.",
		Steps:              []string{"read src/App.tsx", "update its greeting"},
		TargetPaths:        []string{"src/App.tsx"},
		Version:            projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities:       []string{projectAssistantCapabilityWorkspaceMutate},
		AcceptanceCriteria: []string{"The greeting says hello again."},
		ApprovalTool:       projectToolDefineInitialProjectPlan,
		RunLocal:           true,
	})
	h.req.InitialApprovedPlan = &grant
	h.req.TurnProfile = projectAssistantTurnProfileImplementation
	h.req.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	h.req.ApprovalMode = store.AssistantApprovalModeAlwaysAsk
	run := *h.req.AssistantRun
	run.ApprovalMode = store.AssistantApprovalModeAlwaysAsk
	h.req.AssistantRun = &run

	toolCall := func(id, name, arguments string) *schema.Message {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   id,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      name,
				Arguments: arguments,
			},
		}})
	}
	model := &repositoryFlowEinoChatModel{Steps: []repositoryFlowEinoModelStep{
		{Message: toolCall("call-runtime-before-resume", projectToolRestartRuntime, `{}`)},
		{Message: toolCall("call-read-after-resume", projectToolReadFile, `{"file_path":"src/App.tsx","offset":1,"limit":200}`)},
		{Message: toolCall("call-patch-after-resume", projectToolApplyPatch, `{"patch":"*** Begin Patch\n*** Update File: src/App.tsx\n@@\n export function App() {\n-  const greeting = \"hello\";\n+  const greeting = \"hello again\";\n   const audience = \"world\";\n   return greeting + \" \" + audience;\n }\n*** End Patch"}`)},
		{Message: toolCall("call-runtime-after-patch", projectToolRestartRuntime, `{}`)},
	}}
	var runtimeCalls int
	runtimeSpec, ok := projectAssistantWorkflowToolSpec(projectToolRestartRuntime)
	if !ok {
		t.Fatalf("%s workflow spec missing", projectToolRestartRuntime)
	}
	runtimeTool := projectAssistantToolFunc{
		spec: runtimeSpec,
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			runtimeCalls++
			return `{"status":"ready"}`, nil
		},
	}
	patchTool, ok := h.server.projectAssistantToolRegistry().Get(projectToolApplyPatch)
	if !ok {
		t.Fatalf("%s missing from registry", projectToolApplyPatch)
	}
	engine := projectEinoAssistantEngine{
		server: h.server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{
				newProjectEinoAssistantServerTool(h.server, runtimeTool, req, state),
				newProjectEinoAssistantServerTool(h.server, patchTool, req, state),
			}, nil
		},
	}

	_, err := engine.StreamProjectAssistant(ctx, h.req)
	var firstPermission *projectAssistantPermissionRequiredError
	if !errors.As(err, &firstPermission) {
		t.Fatalf("StreamProjectAssistant error = %v, want permission interrupt", err)
	}
	if firstPermission.ToolName != projectToolRestartRuntime {
		t.Fatalf("initial permission tool = %q, want %q", firstPermission.ToolName, projectToolRestartRuntime)
	}
	pending, err := h.messages.GetAssistantRun(ctx, h.scope, firstPermission.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(pending.Checkpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.ApprovedPlan == nil || !checkpoint.ApprovedPlan.RunLocal ||
		strings.Join(checkpoint.ApprovedPlan.TargetPaths, ",") != "src/App.tsx" {
		t.Fatalf("saved run-local grant = %#v", checkpoint.ApprovedPlan)
	}

	accumulator := h.server.projectAssistantSupervisor().accumulatorFor(h.scope, pending.ID)
	if accumulator == nil {
		t.Fatal("assistant run accumulator missing")
	}
	claimed, err := accumulator.ClaimPending(ctx, firstPermission.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	h.req.AssistantRun = &claimed
	h.req.eventLedger = nil
	_, err = engine.ResumeProjectAssistant(ctx, h.req, projectAssistantResumeRequest{
		RequestID: firstPermission.RequestID,
		Decision:  string(projectAssistantPermissionAllow),
	}, checkpoint)
	var secondPermission *projectAssistantPermissionRequiredError
	if !errors.As(err, &secondPermission) {
		t.Fatalf("ResumeProjectAssistant error = %v, want second permission interrupt", err)
	}
	if secondPermission.ToolName != projectToolApplyPatch {
		t.Fatalf("resumed permission tool = %q, want %q", secondPermission.ToolName, projectToolApplyPatch)
	}
	if runtimeCalls != 1 {
		t.Fatalf("approved runtime calls = %d, want one", runtimeCalls)
	}
	read, err := h.workspaces.ReadFile(ctx, h.req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read.Content, `const greeting = "hello again";`) {
		t.Fatalf("workspace content after resume = %q, plan unexpectedly authorized patch", read.Content)
	}
}
