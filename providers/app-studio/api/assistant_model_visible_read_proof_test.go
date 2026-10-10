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
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/railgrid/provider-app-studio/workspace"
)

func recordProjectAssistantModelVisibleReadForTest(runState *projectEinoAssistantRunState, path, version string, binary bool) {
	runState.RecordObservedReadFileVersion(path, version)
	readOrdinal := runState.NextModelCallOrdinal()
	runState.RecordModelVisibleReadFileVersion(path, version, binary, readOrdinal)
	runState.NextModelCallOrdinal()
}

func projectAssistantReadReceiptForProofTest(t *testing.T, path, content, version string, binary bool) string {
	t.Helper()
	size := int64(len([]byte(content)))
	if binary {
		size = 6
	}
	return projectEinoAssistantReadFileJSONForTest(t, path, content, version, size, true, false, binary, 1, 2000)
}

func projectAssistantReadResultMessagesForProofTest(receipt, resultName, callID, callName string) []*schema.Message {
	messages := make([]*schema.Message, 0, 2)
	if callID != "" {
		messages = append(messages, schema.AssistantMessage("", []schema.ToolCall{{
			ID: callID, Type: "function", Function: schema.FunctionCall{Name: callName, Arguments: `{}`},
		}}))
	}
	messages = append(messages, schema.ToolMessage(receipt, callID, schema.WithToolName(resultName)))
	return messages
}

func seedProjectAssistantModelVisibleReadProofForReconciliation(state *projectEinoAssistantRunState, path, version string, binary bool) {
	state.RecordObservedReadFileVersion(path, version)
	ordinal := state.NextModelCallOrdinal()
	state.RecordModelVisibleReadFileVersion(path, version, binary, ordinal)
}

func reconcileProjectAssistantReadProofAtModelBoundary(t *testing.T, state *projectEinoAssistantRunState, messages []*schema.Message) {
	t.Helper()
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{}, state).(*projectEinoAssistantLifecycle)
	_, rewritten, err := lifecycle.BeforeModelRewriteState(context.Background(), &adk.ChatModelAgentState{Messages: messages}, nil)
	if err != nil {
		t.Fatalf("prepare next model input: %v", err)
	}
	if rewritten == nil {
		t.Fatal("model boundary returned nil state")
	}
}

func invokeProjectAssistantReadFileThroughModelProjection(
	t *testing.T,
	runState *projectEinoAssistantRunState,
	files *workspace.FileStore,
	scope workspace.Scope,
	arguments map[string]any,
) string {
	t.Helper()
	runState.NextModelCallOrdinal()
	req := projectAssistantToolCallRequest{
		WorkspaceScope: scope,
		RunState:       runState,
		Arguments:      arguments,
	}
	middleware := projectEinoAssistantModelToolOutputMiddlewareForModel(runState).(*projectEinoAssistantModelToolOutputMiddleware)
	wrapped, err := middleware.WrapInvokableToolCall(
		context.Background(),
		func(ctx context.Context, _ string, _ ...einotool.Option) (string, error) {
			return projectAssistantReadFileTool(ctx, files, req)
		},
		&adk.ToolContext{Name: projectToolReadFile, CallID: "read-file-test"},
	)
	if err != nil {
		t.Fatalf("wrap read_file: %v", err)
	}
	encodedArguments, err := json.Marshal(arguments)
	if err != nil {
		t.Fatalf("encode read_file arguments: %v", err)
	}
	result, err := wrapped(context.Background(), string(encodedArguments))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	return result
}

func parseProjectAssistantModelReadForTest(t *testing.T, result string) projectEinoAssistantLiteralReadFileOutput {
	t.Helper()
	parsed, ok := projectEinoAssistantParseLiteralReadFileOutput(result)
	if !ok {
		t.Fatalf("model read result is not the literal-source envelope:\n%s", result)
	}
	return parsed
}

func TestProjectAssistantModelOutputTruncationDoesNotAuthorizeWholeFileReplacement(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-read-proof", WorkspaceUUID: "ws-read-proof", ProjectName: "demo", ProjectUID: "read-proof"}
	content := "const banner = `café — 雪 <&> \\\\path`;\n" + strings.Repeat("const marker = `雪<&> \\\\path`;\n", 1200)
	if _, err := files.WriteFile(ctx, scope, workspace.WriteOptions{Path: "src/large.ts", Content: content}); err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	modelResult := invokeProjectAssistantReadFileThroughModelProjection(t, runState, files, scope, map[string]any{
		"file_path": "src/large.ts",
		"limit":     2000,
	})
	if len(modelResult) > projectEinoAssistantModelToolOutputMaxBytes || !utf8.ValidString(modelResult) {
		t.Fatalf("model read result is %d bytes or invalid UTF-8", len(modelResult))
	}
	visible := parseProjectAssistantModelReadForTest(t, modelResult)
	if visible.path != "src/large.ts" || visible.complete || visible.version != "" || !visible.truncated || !visible.modelClipped {
		t.Fatalf("model-visible read metadata = %#v; want a truncated excerpt without read authority", visible)
	}
	if !strings.Contains(visible.shown, "café — 雪 <&>") || !strings.Contains(modelResult, "Warning: source read or model output was truncated") {
		t.Fatalf("UTF-8/source excerpt lost expected content or truncation notice: %q", visible.shown)
	}
	if serverVersion := runState.ReadFileVersion("src/large.ts"); serverVersion == "" {
		t.Fatal("server-observed full read version was lost; stale-version fences still need it")
	}
	if _, ok := runState.ModelVisibleReadFileVersion("src/large.ts"); ok {
		t.Fatal("truncated model result created a complete-read proof")
	}
	_, err := projectAssistantRequireMutationRead(ctx, projectAssistantToolCallRequest{
		WorkspaceScope: scope,
		RunState:       runState,
	}, files, "src/large.ts")
	if err == nil || !strings.Contains(err.Error(), "model-output limit") || !strings.Contains(err.Error(), "edit_file") {
		t.Fatalf("replace guard error = %v; want a truncation diagnostic directing targeted edits to edit_file", err)
	}
}

func TestProjectAssistantModelVisibleReadRequiresLaterModelResponseAndSurvivesResume(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-read-proof", WorkspaceUUID: "ws-read-proof", ProjectName: "demo", ProjectUID: "read-proof"}
	if _, err := files.WriteFile(ctx, scope, workspace.WriteOptions{Path: "src/small.ts", Content: "const value = 1;\n"}); err != nil {
		t.Fatal(err)
	}
	state := newProjectEinoAssistantRunState()
	result := invokeProjectAssistantReadFileThroughModelProjection(t, state, files, scope, map[string]any{"file_path": "src/small.ts"})
	read := parseProjectAssistantModelReadForTest(t, result)
	if !read.complete || read.version == "" {
		t.Fatalf("complete model read = %#v", read)
	}
	if proof, ok := state.ModelVisibleReadFileVersion(read.path); !ok || proof.ModelCallOrdinal != 1 || proof.Version != read.version {
		t.Fatalf("recorded visible proof = %#v, ok=%v", proof, ok)
	}
	req := projectAssistantToolCallRequest{WorkspaceScope: scope, RunState: state}
	if _, err := projectAssistantRequireMutationRead(ctx, req, files, read.path, read.version); err == nil || !strings.Contains(err.Error(), "earlier model response") {
		t.Fatalf("same-response mutation error = %v; want a response-boundary rejection", err)
	}

	checkpoint := state.CheckpointState()
	resumed := newProjectEinoAssistantRunState()
	resumed.RestoreCheckpointState(checkpoint)
	resumedReq := projectAssistantToolCallRequest{WorkspaceScope: scope, RunState: resumed}
	if _, err := projectAssistantRequireMutationRead(ctx, resumedReq, files, read.path); err == nil {
		t.Fatal("resumed same-response proof authorized a mutation before the next model boundary")
	}
	if got := resumed.NextModelCallOrdinal(); got != 2 {
		t.Fatalf("next model-call ordinal = %d, want 2", got)
	}
	version, err := projectAssistantRequireMutationRead(ctx, resumedReq, files, read.path, "model-fabricated-version")
	if err != nil || version != read.version {
		t.Fatalf("later-response mutation version = %q, err=%v; want authoritative read version %q", version, err, read.version)
	}
}

func TestProjectAssistantVisibleReadPreservesStaleVersionFence(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-read-proof", WorkspaceUUID: "ws-read-proof", ProjectName: "demo", ProjectUID: "read-proof"}
	if _, err := files.WriteFile(ctx, scope, workspace.WriteOptions{Path: "src/stale.ts", Content: "const value = 1;\n"}); err != nil {
		t.Fatal(err)
	}
	state := newProjectEinoAssistantRunState()
	result := invokeProjectAssistantReadFileThroughModelProjection(t, state, files, scope, map[string]any{"file_path": "src/stale.ts"})
	read := parseProjectAssistantModelReadForTest(t, result)
	if read.version == "" {
		t.Fatalf("decode full read result: %#v", read)
	}
	state.NextModelCallOrdinal()
	effective, err := projectAssistantRequireMutationRead(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, RunState: state}, files, read.path)
	if err != nil || effective != read.version {
		t.Fatalf("resolve read version = %q, err=%v; want %q", effective, err, read.version)
	}
	state.RecordObservedReadFileVersion(read.path, "sha256:external-new-version")
	if _, ok := state.ModelVisibleReadFileVersion(read.path); ok {
		t.Fatal("new server-observed version retained proof for the old source")
	}
	if _, err := projectAssistantRequireMutationRead(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, RunState: state}, files, read.path); err == nil {
		t.Fatal("new server-observed version reused the prior model-visible proof")
	}
	if _, err := files.WriteFile(ctx, scope, workspace.WriteOptions{Path: read.path, Content: "const value = 2;\n"}); err != nil {
		t.Fatal(err)
	}
	_, err = files.ReplaceFile(ctx, scope, workspace.ReplaceOptions{Path: read.path, Content: "const value = 3;\n", ExpectedVersion: effective})
	var mutationErr *workspace.MutationError
	if !errors.As(err, &mutationErr) || mutationErr.Code != workspace.MutationErrorStale {
		t.Fatalf("replacement with changed source version error = %v; want stale-source rejection", err)
	}
}

func TestProjectAssistantModelVisibleReadProofCannotRaceSiblingMutation(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-read-proof", WorkspaceUUID: "ws-read-proof", ProjectName: "demo", ProjectUID: "read-proof"}
	if _, err := files.WriteFile(ctx, scope, workspace.WriteOptions{Path: "src/sibling.ts", Content: "const value = 1;\n"}); err != nil {
		t.Fatal(err)
	}
	serverRead, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "src/sibling.ts"})
	if err != nil {
		t.Fatal(err)
	}
	state := newProjectEinoAssistantRunState()
	state.RecordObservedReadFileVersion(serverRead.Path, serverRead.Version)
	state.NextModelCallOrdinal()
	middleware := projectEinoAssistantModelToolOutputMiddlewareForModel(state).(*projectEinoAssistantModelToolOutputMiddleware)
	siblingReceipt := projectAssistantReadReceiptForProofTest(t, "src/sibling.ts", "const value = 1;\n", serverRead.Version, false)
	wrapped, err := middleware.WrapInvokableToolCall(
		ctx,
		func(context.Context, string, ...einotool.Option) (string, error) {
			return siblingReceipt, nil
		},
		&adk.ToolContext{Name: projectToolReadFile},
	)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	readDone := make(chan error, 1)
	mutationDone := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, readErr := wrapped(ctx, `{}`)
		readDone <- readErr
	}()
	go func() {
		defer wg.Done()
		<-start
		_, mutationErr := projectAssistantRequireMutationRead(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, RunState: state}, files, serverRead.Path)
		mutationDone <- mutationErr
	}()
	close(start)
	wg.Wait()
	if err := <-readDone; err != nil {
		t.Fatalf("sibling read_file: %v", err)
	}
	if err := <-mutationDone; err == nil {
		t.Fatal("sibling mutation used a read proof created in the same model response")
	}
	proof, ok := state.ModelVisibleReadFileVersion(serverRead.Path)
	if !ok || proof.ModelCallOrdinal != state.CurrentModelCallOrdinal() {
		t.Fatalf("sibling read proof = %#v, ok=%v; want proof from current response", proof, ok)
	}
}

func TestProjectAssistantPartialAndFailedReadsDoNotCreateModelVisibleProof(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-read-proof", WorkspaceUUID: "ws-read-proof", ProjectName: "demo", ProjectUID: "read-proof"}
	if _, err := files.WriteFile(ctx, scope, workspace.WriteOptions{Path: "src/partial.ts", Content: "first\nsecond\nthird\n"}); err != nil {
		t.Fatal(err)
	}
	state := newProjectEinoAssistantRunState()
	partialResult := invokeProjectAssistantReadFileThroughModelProjection(t, state, files, scope, map[string]any{
		"file_path": "src/partial.ts", "offset": 2, "limit": 1,
	})
	partial := parseProjectAssistantModelReadForTest(t, partialResult)
	if partial.complete || partial.version != "" || partial.shown != "second" {
		t.Fatalf("partial result = %#v; want exact selected source and no full-read version", partial)
	}
	if state.ReadFileVersion("src/partial.ts") != "" {
		t.Fatal("partial read created a server-observed complete-read version")
	}
	if _, ok := state.ModelVisibleReadFileVersion("src/partial.ts"); ok {
		t.Fatal("partial read created model-visible proof")
	}

	state.NextModelCallOrdinal()
	middleware := projectEinoAssistantModelToolOutputMiddlewareForModel(state).(*projectEinoAssistantModelToolOutputMiddleware)
	completeEnvelope := `{"path":"src/partial.ts","content":"whole","version":"sha256:cancelled","complete":true}`
	state.RecordObservedReadFileVersion("src/partial.ts", "sha256:cancelled")
	wrapped, err := middleware.WrapInvokableToolCall(
		ctx,
		func(context.Context, string, ...einotool.Option) (string, error) {
			return completeEnvelope, context.Canceled
		},
		&adk.ToolContext{Name: projectToolReadFile},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped(ctx, `{}`); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read error = %v; want context.Canceled", err)
	}
	if _, ok := state.ModelVisibleReadFileVersion("src/partial.ts"); ok {
		t.Fatal("failed/cancelled read created model-visible proof")
	}
}

func TestProjectAssistantBinaryReadProofAllowsOnlyLaterMoveAndDelete(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-read-proof", WorkspaceUUID: "ws-read-proof", ProjectName: "demo", ProjectUID: "read-proof"}
	for _, path := range []string{"public/move.bin", "public/delete.bin"} {
		if _, err := files.PutFile(ctx, scope, workspace.PutOptions{Path: path, Data: []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0xff}}); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, workspaces: files}
	registry := projectAssistantLocalToolRegistry(server)
	state := newProjectEinoAssistantRunState()
	result := invokeProjectAssistantReadFileThroughModelProjection(t, state, files, scope, map[string]any{"file_path": "public/move.bin"})
	read := parseProjectAssistantModelReadForTest(t, result)
	if !read.complete || !read.binary || read.version == "" {
		t.Fatalf("binary read = %#v", read)
	}
	if _, err := projectAssistantRequireMutationRead(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, RunState: state}, files, read.path); err == nil {
		t.Fatal("binary read authorized replace_file")
	}
	if _, err := projectAssistantRequireMutationReadAllowBinary(ctx, projectAssistantToolCallRequest{WorkspaceScope: scope, RunState: state}, files, read.path); err == nil {
		t.Fatal("binary read authorized move_file in the same model response")
	}
	state.NextModelCallOrdinal()
	move, _ := registry.Get(projectToolMoveFile)
	if _, err := move.Call(ctx, projectAssistantToolCallRequest{
		WorkspaceScope: scope,
		RunState:       state,
		Arguments: map[string]any{
			"sourcePath": "public/move.bin", "destinationPath": "public/moved.bin", "expectedVersion": read.version,
		},
	}); err != nil {
		t.Fatalf("move binary after visible complete read: %v", err)
	}

	deleteResult := invokeProjectAssistantReadFileThroughModelProjection(t, state, files, scope, map[string]any{"file_path": "public/delete.bin"})
	read = parseProjectAssistantModelReadForTest(t, deleteResult)
	if !read.complete || !read.binary || read.version == "" {
		t.Fatalf("second binary read = %#v", read)
	}
	state.NextModelCallOrdinal()
	delete, _ := registry.Get(projectToolDeleteFile)
	if _, err := delete.Call(ctx, projectAssistantToolCallRequest{
		WorkspaceScope: scope,
		RunState:       state,
		Arguments:      map[string]any{"path": "public/delete.bin", "expectedVersion": read.version},
	}); err != nil {
		t.Fatalf("delete binary after visible complete read: %v", err)
	}
}

func TestProjectAssistantModelVisibleReadProofOnlyRecordsExactLocalReadTool(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.NextModelCallOrdinal()
	state.RecordObservedReadFileVersion("src/app.ts", "sha256:app")
	result := `{"path":"src/app.ts","content":"const app = true;","version":"sha256:app","complete":true}`
	middleware := projectEinoAssistantModelToolOutputMiddlewareForModel(state).(*projectEinoAssistantModelToolOutputMiddleware)
	for _, toolName := range []string{"mcp_read_file", "provider__read_file", "read_file_extra"} {
		wrapped, err := middleware.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...einotool.Option) (string, error) {
			return result, nil
		}, &adk.ToolContext{Name: toolName})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wrapped(context.Background(), `{}`); err != nil {
			t.Fatal(err)
		}
		if _, ok := state.ModelVisibleReadFileVersion("src/app.ts"); ok {
			t.Fatalf("non-local tool name %q created a read proof", toolName)
		}
	}
	wrapped, err := middleware.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...einotool.Option) (string, error) {
		return `{"path":`, nil
	}, &adk.ToolContext{Name: projectToolReadFile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.ModelVisibleReadFileVersion("src/app.ts"); ok {
		t.Fatal("malformed read_file envelope created a model-visible proof")
	}
}

func TestProjectAssistantReadProofSurvivesOnlyWhenCompleteReceiptRemainsInModelInput(t *testing.T) {
	const path = "src/read.ts"
	const version = "sha256:read-proof-current"
	const content = "const value = '雪 <&>';\n"
	receipt := projectAssistantReadReceiptForProofTest(t, path, content, version, false)

	t.Run("linked result without tool name survives resume", func(t *testing.T) {
		original := newProjectEinoAssistantRunState()
		seedProjectAssistantModelVisibleReadProofForReconciliation(original, path, version, false)
		checkpoint := original.CheckpointState()

		resumed := newProjectEinoAssistantRunState()
		resumed.RestoreCheckpointState(checkpoint)
		messages := projectAssistantReadResultMessagesForProofTest(receipt, "", "call-read-current", projectToolReadFile)
		reconcileProjectAssistantReadProofAtModelBoundary(t, resumed, messages)
		proof, ok := resumed.ModelVisibleReadFileVersion(path)
		if !ok || proof.Version != version || proof.ModelCallOrdinal != 1 || resumed.CurrentModelCallOrdinal() != 2 {
			t.Fatalf("resumed proof after full linked receipt = %#v, ok=%v, ordinal=%d", proof, ok, resumed.CurrentModelCallOrdinal())
		}
	})

	t.Run("direct local result name survives", func(t *testing.T) {
		state := newProjectEinoAssistantRunState()
		seedProjectAssistantModelVisibleReadProofForReconciliation(state, path, version, false)
		messages := projectAssistantReadResultMessagesForProofTest(receipt, projectToolReadFile, "", "")
		reconcileProjectAssistantReadProofAtModelBoundary(t, state, messages)
		if _, ok := state.ModelVisibleReadFileVersion(path); !ok {
			t.Fatal("complete direct read_file receipt did not preserve proof")
		}
	})

	t.Run("literal model input preserves source bytes and proof", func(t *testing.T) {
		literalSource := "const route = \"\\path\";\n"
		receipt := projectAssistantReadReceiptForProofTest(t, path, literalSource, version, false)
		messages := projectAssistantReadResultMessagesForProofTest(receipt, "", "call-read-literal", projectToolReadFile)
		projected := projectEinoAssistantConversationPayload(projectEinoMessagesToChat(messages))
		if len(projected) != len(messages) {
			t.Fatalf("literal model input message count = %d, want %d", len(projected), len(messages))
		}
		read, ok := projectEinoAssistantParseLiteralReadFileOutput(projected[1].Content)
		if !ok || read.content != literalSource || strings.Count(read.content, `\path`) != 1 || strings.Contains(read.content, `\\path`) {
			t.Fatalf("model input changed literal source bytes: %#v, parsed=%v", read, ok)
		}
		state := newProjectEinoAssistantRunState()
		seedProjectAssistantModelVisibleReadProofForReconciliation(state, path, version, false)
		modelMessages, err := projectChatMessagesToEino(projected)
		if err != nil {
			t.Fatalf("convert literal model input: %v", err)
		}
		reconcileProjectAssistantReadProofAtModelBoundary(t, state, modelMessages)
		if proof, ok := state.ModelVisibleReadFileVersion(path); !ok || proof.Version != version {
			t.Fatalf("proof did not reconcile against complete literal model input: %#v, ok=%v", proof, ok)
		}
	})

	t.Run("compaction summary prunes proof and keeps server version", func(t *testing.T) {
		original := newProjectEinoAssistantRunState()
		seedProjectAssistantModelVisibleReadProofForReconciliation(original, path, version, false)
		resumed := newProjectEinoAssistantRunState()
		resumed.RestoreCheckpointState(original.CheckpointState())
		summary := `{"path":"src/read.ts","complete":false,"modelProjectionDeduplicated":true,"supersededByToolCallID":"call-read-latest"}`
		messages := projectAssistantReadResultMessagesForProofTest(summary, "", "call-read-latest", projectToolReadFile)
		reconcileProjectAssistantReadProofAtModelBoundary(t, resumed, messages)
		if _, ok := resumed.ModelVisibleReadFileVersion(path); ok {
			t.Fatal("compaction summary retained a full-read proof")
		}
		if got := resumed.ReadFileVersion(path); got != version {
			t.Fatalf("server-observed version = %q, want preserved stale-version fence %q", got, version)
		}
	})
}

func TestProjectAssistantReadProofReconciliationRejectsPartialMalformedAndOversizedReceipts(t *testing.T) {
	const path = "src/read.ts"
	const version = "sha256:read-proof-current"
	const content = "const value = 1;\n"
	valid := projectAssistantReadReceiptForProofTest(t, path, content, version, false)
	oversized := projectAssistantReadReceiptForProofTest(t, path, strings.Repeat("const value = 'large';\n", 800), version, false)
	var oversizedOK bool
	oversized, oversizedOK = projectEinoAssistantProjectModelReadFileOutput(oversized, projectEinoAssistantModelToolOutputMaxBytes)
	if !oversizedOK {
		t.Fatal("oversized source receipt was not projected")
	}
	wrongVersion := strings.Replace(valid, version, "sha256:other-version", 1)
	wrongPath := strings.Replace(valid, path, "src/other.ts", 1)
	var wrongSizeFields map[string]any
	if err := json.Unmarshal([]byte(valid), &wrongSizeFields); err != nil {
		t.Fatal(err)
	}
	wrongSizeFields["size"] = float64(len([]byte(content)) + 1)
	wrongSizeBytes, err := json.Marshal(wrongSizeFields)
	if err != nil {
		t.Fatal(err)
	}
	wrongSize := string(wrongSizeBytes)

	tests := []struct {
		name    string
		receipt string
		nameOn  string
		callID  string
		call    string
	}{
		{name: "partial", receipt: `{"path":"src/read.ts","content":"excerpt","complete":false}`, nameOn: projectToolReadFile},
		{name: "model output truncation", receipt: oversized, nameOn: projectToolReadFile},
		{name: "version mismatch", receipt: wrongVersion, nameOn: projectToolReadFile},
		{name: "path mismatch", receipt: wrongPath, nameOn: projectToolReadFile},
		{name: "content size mismatch", receipt: wrongSize, nameOn: projectToolReadFile},
		{name: "error envelope", receipt: `{"path":"src/read.ts","content":"","version":"sha256:read-proof-current","complete":true,"error":"read failed"}`, nameOn: projectToolReadFile},
		{name: "malformed envelope", receipt: `{"path":`, nameOn: projectToolReadFile},
		{name: "invalid UTF-8 envelope", receipt: string([]byte{0xff, '{', '}'}), nameOn: projectToolReadFile},
		{name: "external similarly named tool", receipt: valid, nameOn: "provider__read_file"},
		{name: "ambiguous linked call", receipt: valid, callID: "call-ambiguous", call: projectToolReadFile},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newProjectEinoAssistantRunState()
			seedProjectAssistantModelVisibleReadProofForReconciliation(state, path, version, false)
			messages := projectAssistantReadResultMessagesForProofTest(test.receipt, test.nameOn, test.callID, test.call)
			if test.name == "ambiguous linked call" {
				messages = append([]*schema.Message{
					schema.AssistantMessage("", []schema.ToolCall{{ID: test.callID, Type: "function", Function: schema.FunctionCall{Name: projectToolEditFile, Arguments: `{}`}}}),
				}, messages...)
			}
			reconcileProjectAssistantReadProofAtModelBoundary(t, state, messages)
			if _, ok := state.ModelVisibleReadFileVersion(path); ok {
				t.Fatal("incomplete, malformed, mismatched, oversized, or untrusted result retained proof")
			}
			if got := state.ReadFileVersion(path); got != version {
				t.Fatalf("server-observed version = %q, want %q after proof pruning", got, version)
			}
		})
	}
}

func TestProjectAssistantBinaryReadProofReconciliationRequiresCompleteBinaryReceipt(t *testing.T) {
	const path = "public/asset.bin"
	const version = "sha256:binary-read-proof"
	complete := projectAssistantReadReceiptForProofTest(t, path, "", version, true)
	for _, test := range []struct {
		name    string
		receipt string
		keep    bool
	}{
		{name: "complete binary", receipt: complete, keep: true},
		{name: "binary payload is not exposed", receipt: strings.Replace(complete, `"content":""`, `"content":"opaque"`, 1)},
		{name: "text receipt cannot stand in for binary", receipt: projectAssistantReadReceiptForProofTest(t, path, "", version, false)},
		{name: "truncated binary", receipt: strings.Replace(complete, `"complete":true`, `"complete":false`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newProjectEinoAssistantRunState()
			seedProjectAssistantModelVisibleReadProofForReconciliation(state, path, version, true)
			messages := projectAssistantReadResultMessagesForProofTest(test.receipt, projectToolReadFile, "", "")
			reconcileProjectAssistantReadProofAtModelBoundary(t, state, messages)
			_, ok := state.ModelVisibleReadFileVersion(path)
			if ok != test.keep {
				t.Fatalf("binary proof retained = %v, want %v", ok, test.keep)
			}
		})
	}
}
