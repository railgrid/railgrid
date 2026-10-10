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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestProjectEinoAssistantToolBatchOrderUsesCanonicalWorkspacePaths(t *testing.T) {
	calls := []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("write", projectToolEditFile, `{"path":"src\\components\\App.tsx","oldString":"x","newString":"y"}`),
		projectEinoAssistantToolCallForAdmissionTest("read", projectToolReadFile, `{"file_path":"src/components/App.tsx"}`),
		projectEinoAssistantToolCallForAdmissionTest("move", projectToolMoveFile, `{"sourcePath":"src/components/App.tsx","destinationPath":"src/components/New.tsx"}`),
		projectEinoAssistantToolCallForAdmissionTest("read-destination", projectToolReadFile, `{"file_path":"src/components/New.tsx"}`),
		projectEinoAssistantToolCallForAdmissionTest("read-independent", projectToolReadFile, `{"file_path":"README.md"}`),
		projectEinoAssistantToolCallForAdmissionTest("external-read", "provider__read_file", `{"file_path":"README.md"}`),
	}
	order := projectEinoAssistantNewToolBatchOrder(calls, projectEinoAssistantAvailableBatchTestTools(calls))

	assertPredecessors := func(callID string, want ...string) {
		t.Helper()
		call := order.call(callID)
		if call == nil {
			t.Fatalf("call %q has no scheduled node", callID)
		}
		got := make([]string, 0, len(call.predecessors))
		for _, predecessor := range call.predecessors {
			got = append(got, predecessor.callID)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("call %q predecessors = %v, want %v", callID, got, want)
		}
	}

	assertPredecessors("write")
	assertPredecessors("read", "write")
	assertPredecessors("move", "write", "read")
	assertPredecessors("read-destination", "move")
	assertPredecessors("read-independent")
	assertPredecessors("external-read", "write", "read", "move", "read-destination", "read-independent")
	if order.call("external-read") == nil {
		t.Fatal("an available external tool should be conservatively ordered with workspace operations")
	}
}

func TestProjectEinoAssistantToolBatchOrderTreatsInvalidPathAsWildcard(t *testing.T) {
	calls := []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("invalid", projectToolMoveFile, `{"sourcePath":"../outside","destinationPath":"src/new.ts"}`),
		projectEinoAssistantToolCallForAdmissionTest("read", projectToolReadFile, `{"file_path":"README.md"}`),
	}
	order := projectEinoAssistantNewToolBatchOrder(calls, projectEinoAssistantAvailableBatchTestTools(calls))
	if got := len(order.call("read").predecessors); got != 1 {
		t.Fatalf("read predecessors = %d, want invalid move to conservatively block unknown paths", got)
	}
}

func TestProjectEinoAssistantToolBatchOrderSerializesExecAndBroadSearchWithWorkspaceWrites(t *testing.T) {
	calls := []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("edit", projectToolEditFile, `{"path":"src/App.tsx"}`),
		projectEinoAssistantToolCallForAdmissionTest("exec", projectToolExecCommand, `{"component":"frontend","argv":["node","--check","src/App.tsx"]}`),
		projectEinoAssistantToolCallForAdmissionTest("grep", projectToolGrep, `{"pattern":"needle","path":"src"}`),
		projectEinoAssistantToolCallForAdmissionTest("read", projectToolReadFile, `{"file_path":"src/App.tsx"}`),
		projectEinoAssistantToolCallForAdmissionTest("search", projectEinoAssistantToolSearchTool, `{"query":"database"}`),
		projectEinoAssistantToolCallForAdmissionTest("skill", projectToolLoadSkill, `{"id":"project:example"}`),
	}
	order := projectEinoAssistantNewToolBatchOrder(calls, projectEinoAssistantAvailableBatchTestTools(calls))
	predecessorIDs := func(callID string) []string {
		t.Helper()
		call := order.call(callID)
		if call == nil {
			t.Fatalf("call %q has no scheduled node", callID)
		}
		ids := make([]string, 0, len(call.predecessors))
		for _, predecessor := range call.predecessors {
			ids = append(ids, predecessor.callID)
		}
		return ids
	}
	if got := strings.Join(predecessorIDs("exec"), ","); got != "edit" {
		t.Fatalf("exec predecessors = %q, want workspace edit", got)
	}
	if got := strings.Join(predecessorIDs("grep"), ","); got != "edit,exec" {
		t.Fatalf("grep predecessors = %q, want preceding write and exec", got)
	}
	if got := strings.Join(predecessorIDs("read"), ","); got != "edit,exec" {
		t.Fatalf("read predecessors = %q, want preceding write and exec but not another read", got)
	}
	if order.call("search") != nil || order.call("skill") != nil {
		t.Fatal("catalog-only tools should remain independent of workspace operations")
	}
}

func projectEinoAssistantAvailableBatchTestTools(calls []schema.ToolCall) map[string]struct{} {
	names := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		names[call.Function.Name] = struct{}{}
	}
	return names
}

func projectEinoAssistantBatchTestToolInfos(calls []schema.ToolCall) []*schema.ToolInfo {
	infos := make([]*schema.ToolInfo, 0, len(calls))
	for name := range projectEinoAssistantAvailableBatchTestTools(calls) {
		infos = append(infos, &schema.ToolInfo{Name: name})
	}
	return infos
}

func TestProjectEinoAssistantToolBatchApprovalInterruptDefersDependentRead(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	executionContext := &projectAssistantExecutionContext{}
	middleware := projectEinoAssistantToolBatchAdmissionMiddleware(runState, executionContext).(*projectEinoAssistantToolBatchMiddleware)
	calls := []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("edit-call", projectToolEditFile, `{"path":"src/App.tsx","oldString":"old","newString":"new"}`),
		projectEinoAssistantToolCallForAdmissionTest("read-call", projectToolReadFile, `{"file_path":"src/App.tsx"}`),
	}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.AssistantMessage("", calls)}, ToolInfos: projectEinoAssistantBatchTestToolInfos(calls)}
	ctx, _, err := middleware.AfterModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("admit batch: %v", err)
	}

	writer, err := middleware.WrapInvokableToolCall(ctx, func(ctx context.Context, _ string, _ ...einotool.Option) (string, error) {
		return "", einotool.StatefulInterrupt(ctx, "approval required", map[string]string{"operation": "edit"})
	}, &adk.ToolContext{Name: projectToolEditFile, CallID: "edit-call"})
	if err != nil {
		t.Fatalf("wrap writer: %v", err)
	}
	if _, err := writer(ctx, `{}`); err == nil {
		t.Fatal("writer did not return its Eino approval interrupt")
	} else if _, ok := compose.IsInterruptRerunError(err); !ok {
		t.Fatalf("writer error = %v, want Eino interrupt", err)
	}

	var readCalls atomic.Int32
	reader, err := middleware.WrapInvokableToolCall(ctx, func(context.Context, string, ...einotool.Option) (string, error) {
		readCalls.Add(1)
		return `{"content":"old","complete":true,"version":"sha256:old"}`, nil
	}, &adk.ToolContext{Name: projectToolReadFile, CallID: "read-call"})
	if err != nil {
		t.Fatalf("wrap reader: %v", err)
	}
	result, err := reader(ctx, `{}`)
	if err != nil {
		t.Fatalf("dependent read: %v", err)
	}
	var deferred struct {
		Status   string `json:"status"`
		Executed bool   `json:"executed"`
		Reason   string `json:"reason"`
		Content  string `json:"content"`
		Version  string `json:"version"`
		Complete bool   `json:"complete"`
	}
	if err := json.Unmarshal([]byte(result), &deferred); err != nil {
		t.Fatalf("decode deferred result: %v (%s)", err, result)
	}
	if deferred.Status != "deferred" || deferred.Executed || !strings.Contains(deferred.Reason, "Retry") {
		t.Fatalf("deferred result = %#v, want bounded retry guidance without execution", deferred)
	}
	if deferred.Content != "" || deferred.Version != "" || deferred.Complete {
		t.Fatalf("deferred result contains read evidence: %#v", deferred)
	}
	if got := readCalls.Load(); got != 0 {
		t.Fatalf("dependent read endpoint calls = %d, want zero while approval is pending", got)
	}
	if len(result) > 300 {
		t.Fatalf("deferred result is %d bytes, want a bounded response", len(result))
	}
}

func TestProjectEinoAssistantToolBatchOrderAllowsReadAfterOrdinaryMutationError(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	middleware := projectEinoAssistantToolBatchAdmissionMiddleware(runState, &projectAssistantExecutionContext{}).(*projectEinoAssistantToolBatchMiddleware)
	calls := []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("edit-call", projectToolEditFile, `{"path":"src/App.tsx","oldString":"missing","newString":"new"}`),
		projectEinoAssistantToolCallForAdmissionTest("read-call", projectToolReadFile, `{"file_path":"src/App.tsx"}`),
	}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.AssistantMessage("", calls)}, ToolInfos: projectEinoAssistantBatchTestToolInfos(calls)}
	ctx, _, err := middleware.AfterModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("admit batch: %v", err)
	}
	writer, err := middleware.WrapInvokableToolCall(ctx, func(context.Context, string, ...einotool.Option) (string, error) {
		return "", errors.New("source match failed")
	}, &adk.ToolContext{Name: projectToolEditFile, CallID: "edit-call"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer(ctx, `{}`); err == nil {
		t.Fatal("writer did not return its ordinary error")
	}
	reader, err := middleware.WrapInvokableToolCall(ctx, func(context.Context, string, ...einotool.Option) (string, error) {
		return "observed unchanged source", nil
	}, &adk.ToolContext{Name: projectToolReadFile, CallID: "read-call"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader(ctx, `{}`)
	if err != nil || got != "observed unchanged source" {
		t.Fatalf("read after ordinary mutation failure = (%q, %v), want to observe current file", got, err)
	}
}

func TestProjectEinoAssistantToolBatchOrderWaitCanBeCanceled(t *testing.T) {
	calls := []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("edit", projectToolEditFile, `{"path":"src/App.tsx"}`),
		projectEinoAssistantToolCallForAdmissionTest("read", projectToolReadFile, `{"file_path":"src/App.tsx"}`),
	}
	order := projectEinoAssistantNewToolBatchOrder(calls, projectEinoAssistantAvailableBatchTestTools(calls))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if deferred, err := order.call("read").wait(ctx); !errors.Is(err, context.Canceled) || deferred {
		t.Fatalf("canceled dependency wait = (%t, %v), want canceled", deferred, err)
	}
	if _, err := order.call("edit").invoke(ctx, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mutation invoke error = %v, want canceled", err)
	}
	if got := order.call("edit").state; got != projectEinoAssistantToolBatchCallUnresolved {
		t.Fatalf("canceled mutation state = %v, want unresolved", got)
	}
	if deferred, err := order.call("read").wait(context.Background()); err != nil || !deferred {
		t.Fatalf("read after canceled mutation = (%t, %v), want deferred", deferred, err)
	}
}

func TestProjectEinoAssistantToolBatchOrdersSamePathCallsInActualAgent(t *testing.T) {
	var current string
	var currentMu sync.Mutex
	editStarted := make(chan struct{})
	releaseEdit := make(chan struct{})
	var releaseOnce sync.Once
	readResult := make(chan string, 1)
	model := &projectEinoAssistantToolBatchTestModel{calls: []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("edit-call", projectToolEditFile, `{"path":"src\\App.tsx","oldString":"old","newString":"new"}`),
		projectEinoAssistantToolCallForAdmissionTest("read-call", projectToolReadFile, `{"file_path":"src/App.tsx"}`),
	}}
	editTool := projectEinoAssistantBatchTestTool{name: projectToolEditFile, invoke: func(ctx context.Context, _ string) (string, error) {
		close(editStarted)
		select {
		case <-releaseEdit:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		currentMu.Lock()
		current = "new"
		currentMu.Unlock()
		return "edited", nil
	}}
	readTool := projectEinoAssistantBatchTestTool{name: projectToolReadFile, invoke: func(context.Context, string) (string, error) {
		currentMu.Lock()
		value := current
		currentMu.Unlock()
		readResult <- value
		return `{"content":"` + value + `","complete":true,"version":"sha256:` + value + `"}`, nil
	}}

	agent, err := projectEinoAssistantNewBatchTestAgent(model, editTool, readTool)
	if err != nil {
		t.Fatalf("create Eino agent: %v", err)
	}
	runDone := make(chan error, 1)
	go func() {
		runDone <- projectEinoAssistantRunBatchTestAgent(agent)
	}()
	defer releaseOnce.Do(func() { close(releaseEdit) })
	select {
	case <-editStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Eino did not start the model's first edit call")
	}
	select {
	case value := <-readResult:
		t.Fatalf("same-path read ran before the writer completed and observed %q", value)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(releaseEdit) })
	select {
	case value := <-readResult:
		if value != "new" {
			t.Fatalf("same-path read observed %q, want post-edit contents", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("same-path read did not run after the edit completed")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run Eino agent: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Eino agent did not finish after the edit and read")
	}
	toolResult, ok := model.toolResult(projectToolReadFile, "read-call")
	if !ok || !strings.Contains(toolResult, `"content":"new"`) {
		t.Fatalf("second model input read result = (%q, %t), want new file contents", toolResult, ok)
	}
}

func TestProjectEinoAssistantToolBatchKeepsIndependentReadsParallelInActualAgent(t *testing.T) {
	model := &projectEinoAssistantToolBatchTestModel{calls: []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("read-a", projectToolReadFile, `{"file_path":"src/A.tsx"}`),
		projectEinoAssistantToolCallForAdmissionTest("read-b", projectToolReadFile, `{"file_path":"src/B.tsx"}`),
	}}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	readTool := projectEinoAssistantBatchTestTool{name: projectToolReadFile, invoke: func(ctx context.Context, _ string) (string, error) {
		started <- struct{}{}
		select {
		case <-release:
			return `{"content":"ok"}`, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	agent, err := projectEinoAssistantNewBatchTestAgent(model, readTool)
	if err != nil {
		t.Fatalf("create Eino agent: %v", err)
	}
	runDone := make(chan error, 1)
	go func() {
		runDone <- projectEinoAssistantRunBatchTestAgent(agent)
	}()
	defer releaseOnce.Do(func() { close(release) })
	for index := 0; index < 2; index++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("independent read calls did not overlap in Eino's tool node")
		}
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run Eino agent: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Eino agent did not finish after independent reads")
	}
}

func TestProjectEinoAssistantToolBatchOrdersExecAndGrepAfterWorkspaceEditInActualAgent(t *testing.T) {
	var current string
	var currentMu sync.Mutex
	editStarted := make(chan struct{})
	releaseEdit := make(chan struct{})
	var releaseEditOnce sync.Once
	execStarted := make(chan struct{}, 1)
	releaseExec := make(chan struct{})
	var releaseExecOnce sync.Once
	execFinished := make(chan struct{})
	grepStarted := make(chan struct{}, 1)
	execValue := make(chan string, 1)
	grepValue := make(chan string, 1)
	model := &projectEinoAssistantToolBatchTestModel{calls: []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("edit-call", projectToolEditFile, `{"path":"src/App.tsx","oldString":"old","newString":"new"}`),
		projectEinoAssistantToolCallForAdmissionTest("exec-call", projectToolExecCommand, `{"component":"frontend","argv":["node","--check","src/App.tsx"]}`),
		projectEinoAssistantToolCallForAdmissionTest("grep-call", projectToolGrep, `{"pattern":"new","path":"src"}`),
	}}
	editTool := projectEinoAssistantBatchTestTool{name: projectToolEditFile, invoke: func(ctx context.Context, _ string) (string, error) {
		close(editStarted)
		select {
		case <-releaseEdit:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		currentMu.Lock()
		current = "new"
		currentMu.Unlock()
		return "edited", nil
	}}
	execTool := projectEinoAssistantBatchTestTool{name: projectToolExecCommand, invoke: func(ctx context.Context, _ string) (string, error) {
		execStarted <- struct{}{}
		select {
		case <-releaseExec:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		currentMu.Lock()
		value := current
		currentMu.Unlock()
		execValue <- value
		close(execFinished)
		if value != "new" {
			return "", errors.New("exec observed pre-edit source")
		}
		return "syntax valid", nil
	}}
	grepTool := projectEinoAssistantBatchTestTool{name: projectToolGrep, invoke: func(context.Context, string) (string, error) {
		grepStarted <- struct{}{}
		currentMu.Lock()
		value := current
		currentMu.Unlock()
		grepValue <- value
		select {
		case <-execFinished:
		default:
			return "", errors.New("grep ran before the earlier exec call completed")
		}
		return "search complete", nil
	}}
	agent, err := projectEinoAssistantNewBatchTestAgent(model, editTool, execTool, grepTool)
	if err != nil {
		t.Fatalf("create Eino agent: %v", err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- projectEinoAssistantRunBatchTestAgent(agent) }()
	defer releaseEditOnce.Do(func() { close(releaseEdit) })
	defer releaseExecOnce.Do(func() { close(releaseExec) })
	select {
	case <-editStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Eino did not start the model's first edit call")
	}
	select {
	case <-execStarted:
		t.Fatal("exec endpoint started before the earlier workspace edit completed")
	case <-grepStarted:
		t.Fatal("grep endpoint started before the earlier workspace edit completed")
	case <-time.After(50 * time.Millisecond):
	}
	releaseEditOnce.Do(func() { close(releaseEdit) })
	select {
	case <-execStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("exec did not start after the workspace edit completed")
	}
	select {
	case <-grepStarted:
		t.Fatal("grep endpoint started before the earlier exec completed")
	case <-time.After(50 * time.Millisecond):
	}
	releaseExecOnce.Do(func() { close(releaseExec) })
	select {
	case value := <-execValue:
		if value != "new" {
			t.Fatalf("exec observed %q, want post-edit source", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exec did not finish after release")
	}
	select {
	case value := <-grepValue:
		if value != "new" {
			t.Fatalf("grep observed %q, want post-edit source", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("grep did not run after exec completed")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run Eino agent: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Eino agent did not finish after the ordered operations")
	}
}

func TestProjectEinoAssistantToolBatchUnknownToolBeforeKnownReadDoesNotWaitForever(t *testing.T) {
	model := &projectEinoAssistantToolBatchTestModel{calls: []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("unknown-call", "not_registered", `{}`),
		projectEinoAssistantToolCallForAdmissionTest("read-call", projectToolReadFile, `{"file_path":"README.md"}`),
	}}
	var readCalls atomic.Int32
	readTool := projectEinoAssistantBatchTestTool{name: projectToolReadFile, invoke: func(context.Context, string) (string, error) {
		readCalls.Add(1)
		return "read completed", nil
	}}
	agent, err := projectEinoAssistantNewBatchTestAgent(model, readTool)
	if err != nil {
		t.Fatalf("create Eino agent: %v", err)
	}
	if err := projectEinoAssistantRunBatchTestAgent(agent); err != nil {
		t.Fatalf("run Eino agent with unknown tool: %v", err)
	}
	if got := readCalls.Load(); got != 1 {
		t.Fatalf("read endpoint calls = %d, want one after the unwrapped unknown-tool handler", got)
	}
}

func TestProjectEinoAssistantToolBatchVisibleDynamicToolOrdersBeforeReadInUnknownHandler(t *testing.T) {
	var current string
	var currentMu sync.Mutex
	var readCalls atomic.Int32
	model := &projectEinoAssistantToolBatchTestModel{calls: []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("dynamic-call", "dynamic_workspace_probe", `{}`),
		projectEinoAssistantToolCallForAdmissionTest("read-call", projectToolReadFile, `{"file_path":"README.md"}`),
	}}
	readTool := projectEinoAssistantBatchTestTool{name: projectToolReadFile, invoke: func(context.Context, string) (string, error) {
		readCalls.Add(1)
		currentMu.Lock()
		defer currentMu.Unlock()
		return current, nil
	}}
	visibleTool := &projectEinoAssistantBatchTestVisibleToolInfoMiddleware{name: "dynamic_workspace_probe"}
	executionContext := &projectAssistantExecutionContext{}
	agent, err := projectEinoAssistantNewBatchTestAgentWithUnknownHandler(
		model,
		[]einotool.BaseTool{readTool},
		[]adk.ChatModelAgentMiddleware{visibleTool},
		executionContext,
		func(context.Context, string, string) (string, error) {
			currentMu.Lock()
			current = "dynamic result"
			currentMu.Unlock()
			return "dynamic result", nil
		},
	)
	if err != nil {
		t.Fatalf("create Eino agent: %v", err)
	}
	if err := projectEinoAssistantRunBatchTestAgent(agent); err != nil {
		t.Fatalf("run Eino agent with dynamically visible tool: %v", err)
	}
	if got := readCalls.Load(); got != 1 {
		t.Fatalf("read endpoint calls = %d, want one after dynamic call", got)
	}
	result, ok := model.toolResult(projectToolReadFile, "read-call")
	if !ok || result != "dynamic result" {
		t.Fatalf("read result = (%q, %t), want dynamic result from prior unknown-handler dispatch", result, ok)
	}
}

func TestProjectEinoAssistantToolBatchApprovalDefersDependentCallAcrossADKResume(t *testing.T) {
	var readCalls atomic.Int32
	model := &projectEinoAssistantToolBatchTestModel{calls: []schema.ToolCall{
		projectEinoAssistantToolCallForAdmissionTest("edit-call", projectToolEditFile, `{"path":"src/App.tsx","oldString":"old","newString":"new"}`),
		projectEinoAssistantToolCallForAdmissionTest("read-call", projectToolReadFile, `{"file_path":"src/App.tsx"}`),
	}}
	editTool := projectEinoAssistantBatchTestTool{name: projectToolEditFile, invoke: func(ctx context.Context, _ string) (string, error) {
		if wasInterrupted, _, _ := einotool.GetInterruptState[string](ctx); wasInterrupted {
			return "edited after approval", nil
		}
		return "", einotool.StatefulInterrupt(ctx, "approval required", "edit")
	}}
	readTool := projectEinoAssistantBatchTestTool{name: projectToolReadFile, invoke: func(context.Context, string) (string, error) {
		readCalls.Add(1)
		return `{"content":"old","complete":true,"version":"sha256:old"}`, nil
	}}
	store := &projectEinoAssistantBatchTestCheckpointStore{data: make(map[string][]byte)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstAgent, err := projectEinoAssistantNewBatchTestAgent(model, editTool, readTool)
	if err != nil {
		t.Fatalf("create initial Eino agent: %v", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: firstAgent, CheckPointStore: store})
	first := runner.Run(ctx, []*schema.Message{schema.UserMessage("edit and verify the file")}, adk.WithCheckPointID("batch-order-resume"))
	var interrupted bool
	for {
		event, ok := first.Next()
		if !ok {
			break
		}
		if event != nil && event.Action != nil && event.Action.Interrupted != nil {
			interrupted = true
		}
		if event != nil && event.Err != nil {
			t.Fatalf("initial runner event: %v", event.Err)
		}
	}
	if !interrupted {
		t.Fatal("initial runner did not surface an approval interrupt")
	}
	if got := readCalls.Load(); got != 0 {
		t.Fatalf("dependent read endpoint calls = %d, want zero while approval is pending", got)
	}

	// Resume with a newly constructed agent. Eino must replay the already
	// checkpointed deferred sibling result without invoking the read tool.
	resumedAgent, err := projectEinoAssistantNewBatchTestAgent(model, editTool, readTool)
	if err != nil {
		t.Fatalf("create resumed Eino agent: %v", err)
	}
	resumedRunner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: resumedAgent, CheckPointStore: store})
	resumed, err := resumedRunner.Resume(ctx, "batch-order-resume")
	if err != nil {
		t.Fatalf("resume Eino runner: %v", err)
	}
	var completed bool
	for {
		event, ok := resumed.Next()
		if !ok {
			break
		}
		if event != nil && event.Err != nil {
			t.Fatalf("resumed runner event: %v", event.Err)
		}
		if event != nil && event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil && event.Output.MessageOutput.Message.Content == "finished" {
			completed = true
		}
	}
	if !completed {
		t.Fatal("resumed runner did not complete after the approved edit")
	}
	if got := readCalls.Load(); got != 0 {
		t.Fatalf("dependent read endpoint calls after resume = %d, want zero because its result was checkpointed", got)
	}
	deferred, ok := model.toolResult(projectToolReadFile, "read-call")
	if !ok || !strings.Contains(deferred, `"status":"deferred"`) || !strings.Contains(deferred, `"executed":false`) {
		t.Fatalf("resumed model's dependent tool result = (%q, %t), want a checkpointed non-executed deferred result", deferred, ok)
	}
	if strings.Contains(deferred, `"content"`) || strings.Contains(deferred, `"version"`) || strings.Contains(deferred, `"complete"`) {
		t.Fatalf("deferred tool result claims read evidence: %s", deferred)
	}
}

type projectEinoAssistantBatchTestTool struct {
	name   string
	invoke func(context.Context, string) (string, error)
}

func (t projectEinoAssistantBatchTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	params := map[string]*schema.ParameterInfo{}
	switch t.name {
	case projectToolReadFile:
		params["file_path"] = &schema.ParameterInfo{Type: schema.String, Required: true}
	case projectToolGrep:
		params["pattern"] = &schema.ParameterInfo{Type: schema.String, Required: true}
		params["path"] = &schema.ParameterInfo{Type: schema.String}
	case projectToolExecCommand:
		params["component"] = &schema.ParameterInfo{Type: schema.String, Required: true}
		params["argv"] = &schema.ParameterInfo{Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Required: true}
	default:
		params["path"] = &schema.ParameterInfo{Type: schema.String, Required: true}
	}
	return &schema.ToolInfo{
		Name:        t.name,
		Desc:        "test workspace file operation",
		ParamsOneOf: schema.NewParamsOneOfByParams(params),
	}, nil
}

func (t projectEinoAssistantBatchTestTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...einotool.Option) (string, error) {
	return t.invoke(ctx, argumentsInJSON)
}

type projectEinoAssistantToolBatchTestModel struct {
	einomodel.BaseChatModel
	calls     []schema.ToolCall
	modelCall atomic.Int32
	mu        sync.Mutex
	secondIn  []*schema.Message
}

func (m *projectEinoAssistantToolBatchTestModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.modelCall.Add(1) == 1 {
		return schema.AssistantMessage("", m.calls), nil
	}
	m.mu.Lock()
	m.secondIn = append([]*schema.Message(nil), input...)
	m.mu.Unlock()
	return schema.AssistantMessage("finished", nil), nil
}

func (m *projectEinoAssistantToolBatchTestModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *projectEinoAssistantToolBatchTestModel) toolResult(name, callID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, message := range m.secondIn {
		if message != nil && message.Role == schema.Tool && message.ToolName == name && message.ToolCallID == callID {
			return message.Content, true
		}
	}
	return "", false
}

func projectEinoAssistantNewBatchTestAgent(model *projectEinoAssistantToolBatchTestModel, tools ...einotool.BaseTool) (*adk.ChatModelAgent, error) {
	return projectEinoAssistantNewBatchTestAgentWithUnknownHandler(model, tools, nil, &projectAssistantExecutionContext{}, nil)
}

func projectEinoAssistantNewBatchTestAgentWithUnknownHandler(
	model *projectEinoAssistantToolBatchTestModel,
	tools []einotool.BaseTool,
	additionalHandlers []adk.ChatModelAgentMiddleware,
	executionContext *projectAssistantExecutionContext,
	unknownDispatch func(context.Context, string, string) (string, error),
) (*adk.ChatModelAgent, error) {
	batchMiddleware := newProjectEinoAssistantToolBatchAdmissionMiddleware(newProjectEinoAssistantRunState(), executionContext)
	handlers := append([]adk.ChatModelAgentMiddleware(nil), additionalHandlers...)
	handlers = append(handlers, batchMiddleware)
	if unknownDispatch == nil {
		unknownDispatch = func(_ context.Context, name, _ string) (string, error) {
			return "unknown tool: " + name, nil
		}
	}
	unknownHandler := func(ctx context.Context, name, arguments string) (string, error) {
		var result string
		invoke := func() error {
			if executionContext != nil {
				executionContext.toolMu.Lock()
				defer executionContext.toolMu.Unlock()
			}
			var err error
			result, err = unknownDispatch(ctx, name, arguments)
			return err
		}
		deferred, err := batchMiddleware.invokeBatchCall(ctx, compose.GetToolCallID(ctx), invoke)
		if err != nil {
			return "", err
		}
		if deferred {
			return projectEinoAssistantDeferredWorkspaceCall, nil
		}
		return result, nil
	}
	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:  "batch-order-test",
		Model: model,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               tools,
			UnknownToolsHandler: unknownHandler,
			ExecuteSequentially: false,
		}},
		Handlers:      handlers,
		MaxIterations: 2,
	})
	if err != nil {
		return nil, fmt.Errorf("create Eino agent: %w", err)
	}
	return agent, nil
}

type projectEinoAssistantBatchTestVisibleToolInfoMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	name string
}

func (m *projectEinoAssistantBatchTestVisibleToolInfoMiddleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelContext *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	state.ToolInfos = append(state.ToolInfos, &schema.ToolInfo{Name: m.name})
	return ctx, state, nil
}

func projectEinoAssistantRunBatchTestAgent(agent *adk.ChatModelAgent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	iterator := agent.Run(ctx, &adk.AgentInput{Messages: []*schema.Message{schema.UserMessage("edit and verify the file")}})
	for {
		event, ok := iterator.Next()
		if !ok {
			return nil
		}
		if event != nil && event.Err != nil {
			return event.Err
		}
	}
}

type projectEinoAssistantBatchTestCheckpointStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *projectEinoAssistantBatchTestCheckpointStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.data[id]
	return append([]byte(nil), data...), ok, nil
}

func (s *projectEinoAssistantBatchTestCheckpointStore) Set(_ context.Context, id string, data []byte) error {
	s.mu.Lock()
	s.data[id] = append([]byte(nil), data...)
	s.mu.Unlock()
	return nil
}
