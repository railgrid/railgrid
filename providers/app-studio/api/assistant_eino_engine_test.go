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
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestEinoAssistantEngineRequiresProject(t *testing.T) {
	engine := NewEinoAssistantEngine(&Server{})
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{},
	)
	if err == nil || !strings.Contains(err.Error(), "project is required") {
		t.Fatalf("StreamProjectAssistant error = %v, want missing project error", err)
	}
}

func TestProjectEinoAssistantMaxIterationsExceededUsesSentinel(t *testing.T) {
	if !projectEinoAssistantMaxIterationsExceeded(
		fmt.Errorf("wrapped: %w", adk.ErrExceedMaxIterations),
	) {
		t.Fatal("wrapped Eino max-iteration sentinel was not recognized")
	}
	if projectEinoAssistantMaxIterationsExceeded(
		errors.New("exceeds max iterations"),
	) {
		t.Fatal("lookalike string must not be recognized")
	}
}

func TestProjectEinoAssistantMessageOutputPublishesAssistantStreamChunks(t *testing.T) {
	var chunks []string
	output := &adk.TypedMessageVariant[*schema.Message]{
		IsStreaming: true,
		MessageStream: schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("Hello ", nil),
			schema.AssistantMessage("world", nil),
		}),
		Role: schema.Assistant,
	}

	msg, err := projectEinoAssistantMessageOutput(context.Background(), output, projectAssistantStreamCallbacks{
		OnChunk: func(chunk string) { chunks = append(chunks, chunk) },
	})
	if err != nil {
		t.Fatalf("message output returned error: %v", err)
	}
	if msg == nil || msg.Content != "Hello world" {
		t.Fatalf("message = %#v, want concatenated assistant content", msg)
	}
	if len(chunks) != 1 || chunks[0] != "Hello world" {
		t.Fatalf("chunks = %#v, want one accepted assistant response", chunks)
	}
}

func TestProjectEinoAssistantMessageOutputPublishesAcceptedStreamAfterEOF(t *testing.T) {
	stream, writer := schema.Pipe[*schema.Message](0)
	output := &adk.TypedMessageVariant[*schema.Message]{
		IsStreaming:   true,
		MessageStream: stream,
		Role:          schema.Assistant,
	}
	chunks := make(chan string, 2)
	provisional := make(chan string, 2)
	result := make(chan struct {
		msg *schema.Message
		err error
	}, 1)
	go func() {
		msg, err := projectEinoAssistantMessageOutput(context.Background(), output, projectAssistantStreamCallbacks{
			OnChunk:           func(chunk string) { chunks <- chunk },
			OnProvisionalText: func(content string) { provisional <- content },
		})
		result <- struct {
			msg *schema.Message
			err error
		}{msg: msg, err: err}
	}()

	if closed := writer.Send(schema.AssistantMessage("Hello ", nil), nil); closed {
		t.Fatal("stream closed before first chunk was sent")
	}
	if got := <-provisional; got != "Hello " {
		t.Fatalf("first provisional text = %q, want %q", got, "Hello ")
	}

	if closed := writer.Send(schema.AssistantMessage("world", nil), nil); closed {
		t.Fatal("stream closed before second chunk was sent")
	}
	if got := <-provisional; got != "Hello world" {
		t.Fatalf("second provisional text = %q, want %q", got, "Hello world")
	}
	select {
	case got := <-chunks:
		t.Fatalf("accepted assistant chunk %q was published before stream EOF", got)
	default:
	}
	writer.Close()
	got := <-result
	if got.err != nil {
		t.Fatalf("message output returned error: %v", got.err)
	}
	if got.msg == nil || got.msg.Content != "Hello world" {
		t.Fatalf("message = %#v, want concatenated assistant content", got.msg)
	}
	select {
	case chunk := <-chunks:
		if chunk != "Hello world" {
			t.Fatalf("accepted assistant chunk = %q, want %q", chunk, "Hello world")
		}
	default:
		t.Fatal("accepted assistant response was not published after EOF")
	}
	select {
	case chunk := <-chunks:
		t.Fatalf("unexpected extra assistant chunk %q", chunk)
	default:
	}
}

func TestProjectEinoAssistantMessageOutputDoesNotPublishFailedStream(t *testing.T) {
	stream, writer := schema.Pipe[*schema.Message](2)
	_ = writer.Send(schema.AssistantMessage("rejected response", nil), nil)
	_ = writer.Send(nil, io.ErrUnexpectedEOF)
	writer.Close()
	output := &adk.TypedMessageVariant[*schema.Message]{
		IsStreaming:   true,
		MessageStream: stream,
		Role:          schema.Assistant,
	}
	var chunks []string
	var provisional []string
	resets := 0

	_, err := projectEinoAssistantMessageOutput(context.Background(), output, projectAssistantStreamCallbacks{
		OnChunk:            func(chunk string) { chunks = append(chunks, chunk) },
		OnProvisionalText:  func(content string) { provisional = append(provisional, content) },
		OnProvisionalReset: func() { resets++ },
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("message output error = %v, want unexpected EOF", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("chunks = %#v, want no output from failed stream", chunks)
	}
	if len(provisional) != 1 || provisional[0] != "rejected response" {
		t.Fatalf("provisional = %#v, want rejected partial text shown provisionally", provisional)
	}
	if resets != 1 {
		t.Fatalf("resets = %d, want provisional reset on stream failure", resets)
	}
}

func TestProjectEinoAssistantMessageOutputStreamsToolCallContent(t *testing.T) {
	var chunks []string
	output := &adk.TypedMessageVariant[*schema.Message]{
		IsStreaming: true,
		MessageStream: schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("I will inspect the project.", nil),
			schema.AssistantMessage("", []schema.ToolCall{{
				ID:   "call-readiness",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolCheckProjectReadiness,
					Arguments: `{}`,
				},
			}}),
		}),
		Role: schema.Assistant,
	}

	msg, err := projectEinoAssistantMessageOutput(context.Background(), output, projectAssistantStreamCallbacks{
		OnChunk: func(chunk string) { chunks = append(chunks, chunk) },
	})
	if err != nil {
		t.Fatalf("message output returned error: %v", err)
	}
	if msg == nil || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != projectToolCheckProjectReadiness {
		t.Fatalf("message = %#v, want preserved tool call for existing tool summary UX", msg)
	}
	if strings.Join(chunks, "") != "I will inspect the project." {
		t.Fatalf("chunks = %#v, want assistant content streamed even when a tool call follows", chunks)
	}
}

func TestEinoAssistantRetriesTransientModelFailure(t *testing.T) {
	chatModel := &retryingEinoChatModel{
		streamErrors: []error{io.ErrUnexpectedEOF},
		content:      "accepted response",
	}
	engine := newRetryingProjectEinoAssistantEngine(chatModel)
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileDiscussion)
	var chunks []string
	req.StreamCallbacks.OnChunk = func(chunk string) {
		chunks = append(chunks, chunk)
	}

	result, err := engine.StreamProjectAssistant(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %T: %v", err, err)
	}
	if chatModel.calls != 2 {
		t.Fatalf("model calls = %d, want 2", chatModel.calls)
	}
	if result.Content != "accepted response" {
		t.Fatalf("content = %q, want accepted response", result.Content)
	}
	if len(chunks) != 1 || chunks[0] != "accepted response" {
		t.Fatalf("chunks = %#v, want accepted response only", chunks)
	}
}

func TestEinoAssistantExhaustsTransientModelRetries(t *testing.T) {
	chatModel := &retryingEinoChatModel{
		streamErrors: []error{
			io.ErrUnexpectedEOF,
			io.ErrUnexpectedEOF,
			io.ErrUnexpectedEOF,
		},
		content: "unreachable",
	}
	engine := newRetryingProjectEinoAssistantEngine(chatModel)

	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectEinoRunRequestForProfileTest(projectAssistantTurnProfileDiscussion),
	)
	if !errors.Is(err, adk.ErrExceedMaxRetries) {
		t.Fatalf("StreamProjectAssistant error = %T: %v, want wrapped ErrExceedMaxRetries", err, err)
	}
	if chatModel.calls != 3 {
		t.Fatalf("model calls = %d, want 3", chatModel.calls)
	}
}

func TestEinoAssistantDoesNotRetryPermanentModelFailure(t *testing.T) {
	apiError := &openaimodel.APIError{HTTPStatusCode: http.StatusUnauthorized}
	chatModel := &retryingEinoChatModel{
		streamErrors: []error{apiError},
		content:      "unreachable",
	}
	engine := newRetryingProjectEinoAssistantEngine(chatModel)

	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectEinoRunRequestForProfileTest(projectAssistantTurnProfileDiscussion),
	)
	var gotAPIError *openaimodel.APIError
	if !errors.As(err, &gotAPIError) || gotAPIError.HTTPStatusCode != http.StatusUnauthorized {
		t.Fatalf("StreamProjectAssistant error = %v, want OpenAI 401", err)
	}
	if chatModel.calls != 1 {
		t.Fatalf("model calls = %d, want 1", chatModel.calls)
	}
}

func TestEinoAssistantDoesNotRetryPartialStreamFailure(t *testing.T) {
	chatModel := &partialStreamFailureEinoChatModel{}
	engine := newRetryingProjectEinoAssistantEngine(chatModel)
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileDiscussion)
	var chunks []string
	req.StreamCallbacks.OnChunk = func(chunk string) {
		chunks = append(chunks, chunk)
	}

	_, err := engine.StreamProjectAssistant(context.Background(), req)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("StreamProjectAssistant error = %T: %v, want unexpected EOF", err, err)
	}
	if chatModel.calls != 1 {
		t.Fatalf("model calls = %d, want 1", chatModel.calls)
	}
	if len(chunks) != 0 {
		t.Fatalf("chunks = %#v, want no output from rejected partial stream", chunks)
	}
}

func TestCollectProjectAssistantTurnEventsIgnoresWillRetryError(t *testing.T) {
	iter, generator := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.Message]]()
	generator.Send(&adk.TypedAgentEvent[*schema.Message]{
		Err: &adk.WillRetryError{ErrStr: "transient failure"},
	})
	generator.Send(&adk.TypedAgentEvent[*schema.Message]{
		Output: &adk.TypedAgentOutput[*schema.Message]{
			MessageOutput: &adk.TypedMessageVariant[*schema.Message]{
				Message: schema.AssistantMessage("accepted response", nil),
				Role:    schema.Assistant,
			},
		},
	})
	generator.Close()

	outcome := collectProjectAssistantTurnEventsForTest(t, iter)
	if !outcome.receivedOutput || outcome.result.Content != "accepted response" {
		t.Fatalf("outcome = %#v, want accepted response after retry signal", outcome)
	}
}

func TestCollectProjectAssistantTurnEventsIgnoresMessageStreamWillRetryError(t *testing.T) {
	rejectedStream, writer := schema.Pipe[*schema.Message](1)
	_ = writer.Send(nil, &adk.WillRetryError{ErrStr: "transient stream failure"})
	writer.Close()
	iter, generator := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.Message]]()
	generator.Send(&adk.TypedAgentEvent[*schema.Message]{
		Output: &adk.TypedAgentOutput[*schema.Message]{
			MessageOutput: &adk.TypedMessageVariant[*schema.Message]{
				IsStreaming:   true,
				MessageStream: rejectedStream,
				Role:          schema.Assistant,
			},
		},
	})
	generator.Send(&adk.TypedAgentEvent[*schema.Message]{
		Output: &adk.TypedAgentOutput[*schema.Message]{
			MessageOutput: &adk.TypedMessageVariant[*schema.Message]{
				Message: schema.AssistantMessage("accepted response", nil),
				Role:    schema.Assistant,
			},
		},
	})
	generator.Close()

	outcome := collectProjectAssistantTurnEventsForTest(t, iter)
	if !outcome.receivedOutput || outcome.result.Content != "accepted response" {
		t.Fatalf("outcome = %#v, want accepted response after stream retry signal", outcome)
	}
}

func TestCollectProjectAssistantTurnEventsPropagatesMaxIterations(t *testing.T) {
	iter, generator := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.Message]]()
	generator.Send(&adk.TypedAgentEvent[*schema.Message]{
		Err: fmt.Errorf("wrapped: %w", adk.ErrExceedMaxIterations),
	})
	generator.Close()

	_, err := collectProjectAssistantTurnEventsResultForTest(iter)
	if !errors.Is(err, adk.ErrExceedMaxIterations) {
		t.Fatalf("collectProjectAssistantTurnEvents error = %v, want max-iteration sentinel", err)
	}
}

func collectProjectAssistantTurnEventsForTest(
	t *testing.T,
	iter *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.Message]],
) *projectEinoAssistantTurnOutcome {
	t.Helper()
	outcome, err := collectProjectAssistantTurnEventsResultForTest(iter)
	if err != nil {
		t.Fatalf("collectProjectAssistantTurnEvents returned error: %v", err)
	}
	return outcome
}

func collectProjectAssistantTurnEventsResultForTest(
	iter *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.Message]],
) (*projectEinoAssistantTurnOutcome, error) {
	loop := adk.NewTurnLoop[projectAssistantTurnItem, *schema.Message](
		adk.TurnLoopConfig[projectAssistantTurnItem, *schema.Message]{
			GenInput: func(
				context.Context,
				*adk.TurnLoop[projectAssistantTurnItem, *schema.Message],
				[]projectAssistantTurnItem,
			) (*adk.GenInputResult[projectAssistantTurnItem, *schema.Message], error) {
				return nil, nil
			},
			PrepareAgent: func(
				context.Context,
				*adk.TurnLoop[projectAssistantTurnItem, *schema.Message],
				[]projectAssistantTurnItem,
			) (adk.TypedAgent[*schema.Message], error) {
				return nil, nil
			},
		},
	)
	outcome := &projectEinoAssistantTurnOutcome{}
	engine := projectEinoAssistantEngine{}

	err := engine.collectProjectAssistantTurnEvents(
		context.Background(),
		&adk.TurnContext[projectAssistantTurnItem, *schema.Message]{Loop: loop},
		iter,
		projectAssistantRunRequest{},
		newProjectEinoAssistantRunState(),
		outcome,
	)
	return outcome, err
}

func TestEinoAssistantEngineDoesNotUseToolSearchForSmallReadToolSet(t *testing.T) {
	chatModel := &scriptedEinoChatModel{}
	projectTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        "inspect_workspace",
			Description: "Inspect the workspace.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
			Risk:        projectAssistantToolRiskRead,
		},
		result: `{"path":"src/App.tsx","ok":true}`,
	}
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantTool(projectTool, req, state)}, nil
		},
	}
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Identity:       identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
			Project:        &aiv1alpha1.Project{},
			WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"},
			ToolPort:       projectAssistantDirectToolPort{},
		},
	)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "done after tool" {
		t.Fatalf("content = %q, want final Eino model response", result.Content)
	}
	if projectTool.calls != 1 {
		t.Fatalf("tool calls = %d, want Eino to execute one tool call", projectTool.calls)
	}
	if projectTool.lastRequest.Arguments["path"] != "src/App.tsx" {
		t.Fatalf("tool arguments = %#v, want model arguments", projectTool.lastRequest.Arguments)
	}
	if len(chatModel.toolNames) != 2 {
		t.Fatalf("model calls = %d, want direct tool call and final response", len(chatModel.toolNames))
	}
	if !stringSliceEqual(chatModel.toolNames[0], []string{"inspect_workspace"}) {
		t.Fatalf("initial model tools = %#v, want direct inspect_workspace", chatModel.toolNames[0])
	}
	if stringSliceContains(chatModel.toolNames[0], "tool_search") {
		t.Fatalf("initial model tools = %#v, want no tool_search for small read-only set", chatModel.toolNames[0])
	}
	if len(chatModel.inputs) != 2 {
		t.Fatalf("model calls = %d, want direct tool call and final response", len(chatModel.inputs))
	}
	if !einoMessagesContainToolResult(chatModel.inputs[1], "call-inspect", "src/App.tsx") {
		t.Fatalf("second model input = %#v, want Eino-propagated tool result", chatModel.inputs[1])
	}
}

func TestEinoAssistantEngineUsesBoundedAppStudioSystemInstruction(t *testing.T) {
	chatModel := &capturingEinoChatModel{content: "instruction captured"}
	engine := newRetryingProjectEinoAssistantEngine(chatModel)
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileDiscussion)

	if _, err := engine.StreamProjectAssistant(context.Background(), req); err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if len(chatModel.inputs) != 1 {
		t.Fatalf("model calls = %d, want one", len(chatModel.inputs))
	}

	var instruction string
	for _, msg := range chatModel.inputs[0] {
		if msg != nil && msg.Role == schema.System && msg.Content == projectEinoAssistantDeepInstruction {
			instruction = msg.Content
			break
		}
	}
	if instruction == "" {
		t.Fatal("model input has no system instruction")
	}
	if instruction != projectEinoAssistantDeepInstruction {
		t.Errorf(
			"system instruction length = %d, want exact bounded App Studio instruction length %d",
			len(instruction),
			len(projectEinoAssistantDeepInstruction),
		)
	}

	for name, required := range map[string]string{
		"only exposed App Studio tools": "only the currently exposed App Studio tools",
		"approved source scope":         "approved target-path grant",
		"fresh verification":            "successful development verification before commit",
		"authoritative writes":          "successful whole-file writes as authoritative",
		"bounded rereads":               "do not reread them unless",
		"minimal changes":               "Keep changes minimal and focused",
		"honest blockers":               "report blockers honestly",
		"initial execution plan":        "define_initial_project_plan",
		"real approval only":            "unless you have actually called a permission-bearing tool",
		"same objective repairs":        "Repair defects found by verification inside the same objective",
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(strings.ToLower(instruction), strings.ToLower(required)) {
				t.Errorf("system instruction missing %q", required)
			}
		})
	}
}

func TestProjectEinoAssistantInputDoesNotExposeAutoApproveMode(t *testing.T) {
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileAdaptive)
	req.ApprovalMode = store.AssistantApprovalModeAutoApprove

	messages, err := projectEinoAssistantInputMessages(
		context.Background(),
		req,
		newProjectEinoAssistantRunState(),
	)
	if err != nil {
		t.Fatalf("projectEinoAssistantInputMessages returned error: %v", err)
	}

	var systemText strings.Builder
	for _, message := range messages {
		if message != nil && message.Role == schema.System {
			systemText.WriteString(message.Content)
			systemText.WriteByte('\n')
		}
	}
	prompt := systemText.String()
	if !strings.Contains(prompt, projectToolRequestProjectPlanApproval) {
		t.Errorf("assistant instruction missing required grant-bearing tool %q", projectToolRequestProjectPlanApproval)
	}
	if strings.Contains(strings.ToLower(prompt), "auto-approve") {
		t.Error("assistant instruction exposes executor approval policy to the model")
	}
}

func TestEinoAssistantToolSearchKeepsAppStudioToolsStatic(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	runState := newProjectEinoAssistantRunState()
	runState.SetToolDiscovery(projectEinoAssistantToolDiscovery{IncludeCommitBridge: true})
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileImplementation)
	req.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	tools, err := newProjectEinoAssistantToolsFactory(server)(context.Background(), req, runState)
	if err != nil {
		t.Fatalf("new tools returned error: %v", err)
	}

	staticTools, dynamicTools, err := projectEinoAssistantToolSearchSets(context.Background(), tools)
	if err != nil {
		t.Fatalf("projectEinoAssistantToolSearchSets returned error: %v", err)
	}
	staticNames := einoToolNamesForTest(t, staticTools)
	dynamicNames := einoToolNamesForTest(t, dynamicTools)

	for _, want := range []string{
		projectToolPlanProjectChanges,
		projectToolCheckProjectReadiness,
		projectToolPrepareProjectDeployment,
		projectToolWriteFile,
		projectToolApplyPatch,
		projectToolMkdir,
		projectToolCommitProjectFiles,
		projectToolGetRuntimeStatus,
		projectToolGetPreviewURL,
		projectToolAskFollowUp,
		projectToolRequestProjectPlanApproval,
	} {
		if !stringSliceContains(staticNames, want) {
			t.Fatalf("static tools = %#v, want %s", staticNames, want)
		}
	}
	if len(dynamicNames) != 0 {
		t.Fatalf("dynamic tools = %#v, want no provider tools", dynamicNames)
	}
}

func TestEinoAssistantToolSearchDefersOnlySearchableMCPTools(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileImplementation)
	state := newProjectEinoAssistantRunState()
	local, ok := server.projectAssistantToolRegistry().Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	mcpTool := &recordingProjectAssistantTool{spec: projectAssistantToolSpec{
		Name:        "provider__searchable_tool",
		Description: "A provider tool.",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}}

	staticTools, dynamicTools, err := projectEinoAssistantToolSearchSets(context.Background(), []einotool.BaseTool{
		newProjectEinoAssistantTool(local, req, state),
		newProjectEinoAssistantSearchableMCPTool(server, mcpTool, req, state),
	})
	if err != nil {
		t.Fatalf("projectEinoAssistantToolSearchSets returned error: %v", err)
	}
	if got := einoToolNamesForTest(t, staticTools); !stringSliceEqual(got, []string{projectToolWriteFile}) {
		t.Fatalf("static tools = %#v, want only local write_file", got)
	}
	if got := einoToolNamesForTest(t, dynamicTools); !stringSliceEqual(got, []string{"provider__searchable_tool"}) {
		t.Fatalf("dynamic tools = %#v, want only searchable provider tool", got)
	}
}

func TestEinoAssistantEngineDiscussionAndGuidanceExposeNoTools(t *testing.T) {
	for _, profile := range []projectAssistantTurnProfile{
		projectAssistantTurnProfileDiscussion,
		projectAssistantTurnProfileGuidance,
	} {
		t.Run(string(profile), func(t *testing.T) {
			server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
			chatModel := &toolCapturingEinoChatModel{content: "direct answer"}
			engine := projectEinoAssistantEngine{
				server: server,
				newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
					return chatModel, nil
				},
				newTools: newProjectEinoAssistantToolsFactory(server),
			}
			result, err := engine.StreamProjectAssistant(context.Background(), projectEinoRunRequestForProfileTest(profile))
			if err != nil {
				t.Fatalf("StreamProjectAssistant returned error: %v", err)
			}
			if result.Content != "direct answer" {
				t.Fatalf("content = %q, want direct answer", result.Content)
			}
			if len(chatModel.toolNames) != 1 || len(chatModel.toolNames[0]) != 0 {
				t.Fatalf("%s model tools = %#v, want no visible tools", profile, chatModel.toolNames)
			}
			for _, content := range chatModel.contents {
				for _, unwanted := range []string{"No tools were discovered", "Available tools in this workspace", "tool_search"} {
					if strings.Contains(content, unwanted) {
						t.Fatalf("%s model input unexpectedly mentions %q:\n%s", profile, unwanted, content)
					}
				}
			}
		})
	}
}

func TestEinoAssistantEngineDeepTodosRequireAnApprovedMultiStepImplementationPlan(t *testing.T) {
	readTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        projectToolReadFile,
			Description: "Read a project file.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Risk:        projectAssistantToolRiskRead,
		},
		result: `{"path":"src/App.tsx"}`,
	}
	tests := []struct {
		name      string
		req       projectAssistantRunRequest
		wantTodos bool
		wantCalls int
		wantErr   bool
	}{
		{
			name: "multi-step implementation plan",
			req: projectAssistantRunRequest{
				Project:    projectWithRepository("demo-repo", "demo", "github"),
				TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
				InitialApprovedPlan: &projectAssistantApprovedPlan{Steps: []string{
					"inspect the existing application",
					"implement the requested change",
				}},
			},
			wantTodos: true,
			wantCalls: 1,
		},
		{
			name: "one-step implementation plan",
			req: projectAssistantRunRequest{
				Project:    projectWithRepository("demo-repo", "demo", "github"),
				TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
				InitialApprovedPlan: &projectAssistantApprovedPlan{Steps: []string{
					"update the application title",
				}},
			},
			wantCalls: 1,
		},
		{
			name: "discussion turn",
			req: projectAssistantRunRequest{
				Project:     projectWithRepository("demo-repo", "demo", "github"),
				TurnProfile: projectAssistantTurnProfileDiscussion,
			},
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
			chatModel := &toolCapturingEinoChatModel{content: "concise report"}
			engine := projectEinoAssistantEngine{
				server: server,
				newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
					return chatModel, nil
				},
				newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
					return []einotool.BaseTool{newProjectEinoAssistantTool(readTool, req, state)}, nil
				},
			}
			if tt.req.WorkspaceScope == (workspace.Scope{}) {
				tt.req.WorkspaceScope = workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
			}
			if tt.req.MessageScope == (store.Scope{}) {
				tt.req.MessageScope = store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
			}
			tt.req.ToolPort = projectAssistantDirectToolPort{}
			tt.req.executionAuthority = &projectAssistantExplicitTestAuthority{}

			_, err := engine.StreamProjectAssistant(context.Background(), tt.req)
			if tt.wantErr && !errors.Is(err, adk.ErrExceedMaxRetries) {
				t.Fatalf("StreamProjectAssistant error = %v, want Eino retry exhaustion", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("StreamProjectAssistant returned error: %v", err)
			}
			if len(chatModel.toolNames) != tt.wantCalls {
				t.Fatalf("model calls = %d, want %d", len(chatModel.toolNames), tt.wantCalls)
			}
			if got := stringSliceContains(chatModel.toolNames[0], projectEinoAssistantWriteTodosTool); got != tt.wantTodos {
				t.Fatalf("visible tools = %#v, write_todos present = %t, want %t", chatModel.toolNames[0], got, tt.wantTodos)
			}
		})
	}
}

func TestEinoAssistantEngineDeepPhaseRejectsHiddenWriteTodos(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	chatModel := &hiddenWriteTodosEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileImplementation)
	req.InitialApprovedPlan = &projectAssistantApprovedPlan{Steps: []string{"make the small change"}}

	if _, err := engine.StreamProjectAssistant(context.Background(), req); err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if chatModel.todosWritten {
		t.Fatal("hidden write_todos call updated the session outside a multi-step approved plan")
	}
}

func TestEinoAssistantEngineDeepPhaseRejectsHiddenRepeatedPlanWithoutWideningGrant(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	chatModel := &hiddenRepeatedPlanEinoChatModel{}
	var runState *projectEinoAssistantRunState
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(ctx context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			runState = state
			return newProjectEinoAssistantToolsFactory(server)(ctx, req, state)
		},
	}
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileImplementation)
	grant := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:            "Update the application shell.",
		Steps:              []string{"edit the application shell"},
		TargetPaths:        []string{"src/"},
		Version:            projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities:       []string{projectAssistantCapabilityWorkspaceMutate},
		AcceptanceCriteria: []string{"the application shell is updated"},
		ApprovalTool:       projectToolRequestProjectPlanApproval,
	})
	req.InitialApprovedPlan = &grant

	result, err := engine.StreamProjectAssistant(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "reported hidden plan denial" {
		t.Fatalf("content = %q, want model-visible phase denial report", result.Content)
	}
	if len(chatModel.inputs) != 2 {
		t.Fatalf("model calls = %d, want hidden plan denial followed by report", len(chatModel.inputs))
	}
	if !einoMessagesContainContent(chatModel.inputs[1], "unavailable in the current assistant phase") {
		t.Fatalf("second model input = %#v, want phase denial tool result", chatModel.inputs[1])
	}
	if runState == nil {
		t.Fatal("assistant run state was not captured")
	}
	if got := runState.ApprovedPlan(); !reflect.DeepEqual(got, &grant) {
		t.Fatalf("in-memory grant = %#v, want unchanged %#v", got, &grant)
	}
}

func TestEinoAssistantEngineDeepPhaseHidesApprovalAfterApproval(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	chatModel := &planThenReportToolCapturingEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: newProjectEinoAssistantToolsFactory(server),
	}
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileImplementation)
	req = attachProjectAssistantBuildRunForEngineTest(t, server, req, "deep-phase-approval")

	_, err := engine.StreamProjectAssistant(context.Background(), req)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want plan approval permission", err)
	}
	run, err := messages.GetAssistantRun(context.Background(), req.MessageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	claimed := claimProjectAssistantRunForWorkItemCommitTest(t, server, messages, req.MessageScope, run, permissionErr.RequestID)
	req.AssistantRun = &claimed

	if _, err := engine.ResumeProjectAssistant(context.Background(), req, projectAssistantResumeRequest{
		RequestID: permissionErr.RequestID,
		Decision:  string(projectAssistantPermissionAllow),
	}, checkpoint); err != nil {
		t.Fatalf("ResumeProjectAssistant returned error: %v", err)
	}
	if len(chatModel.toolNames) != 2 {
		t.Fatalf("model calls = %d, want plan and one post-approval report", len(chatModel.toolNames))
	}
	if !stringSliceContains(chatModel.toolNames[0], projectToolRequestProjectPlanApproval) {
		t.Fatalf("initial tools = %#v, want plan approval", chatModel.toolNames[0])
	}
	if stringSliceContains(chatModel.toolNames[1], projectToolRequestProjectPlanApproval) {
		t.Fatalf("post-approval tools = %#v, must hide plan approval", chatModel.toolNames[1])
	}
	if !stringSliceContains(chatModel.toolNames[1], projectToolWriteFile) {
		t.Fatalf("post-approval tools = %#v, want mutation tools", chatModel.toolNames[1])
	}
}

func TestEinoAssistantEngineDeepPhasePreservesTerminalPhaseAcrossReductionAfterSuccessfulVerification(t *testing.T) {
	writeTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        projectToolWriteFile,
			Description: "Write a project file.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Risk:        projectAssistantToolRiskWrite,
		},
		result: `{"operation":"write_file"}`,
	}
	verifyTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        projectToolVerifyDevelopmentRuntime,
			Description: "Verify the development runtime.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Risk:        projectAssistantToolRiskRead,
		},
		result: `{"status":"ready"}`,
	}
	commitTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        projectToolCommitProjectFiles,
			Description: "Commit project files.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Risk:        projectAssistantToolRiskCommit,
		},
	}
	largeSource := strings.Repeat("source ", 9000)
	tests := []struct {
		name            string
		initialCreation bool
		wantTools       []string
		wantCalls       int
	}{
		{name: "initial creation report", initialCreation: true, wantCalls: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := store.NewMemoryStore()
			server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
			chatModel := &writeVerifyThenReportEinoChatModel{writeContent: largeSource}
			var runState *projectEinoAssistantRunState
			engine := projectEinoAssistantEngine{
				server: server,
				newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
					return chatModel, nil
				},
				newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
					runState = state
					return []einotool.BaseTool{
						newProjectEinoAssistantServerTool(server, writeTool, req, state),
						newProjectEinoAssistantServerTool(server, verifyTool, req, state),
						newProjectEinoAssistantServerTool(server, commitTool, req, state),
					}, nil
				},
			}
			req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileImplementation)
			if tt.initialCreation {
				initialPlan := projectAssistantInitialCreationPlan()
				req.InitialApprovedPlan = &initialPlan
			}

			_, err := engine.StreamProjectAssistant(context.Background(), req)
			if tt.initialCreation {
				if err != nil {
					t.Fatalf("StreamProjectAssistant returned error: %v", err)
				}
			}
			if len(chatModel.toolNames) != tt.wantCalls {
				t.Fatalf("model calls = %d, want write, verify, required commit when applicable, and terminal-phase report", len(chatModel.toolNames))
			}
			if len(chatModel.inputs) != tt.wantCalls {
				t.Fatalf("model inputs = %d, want write, verify, required commit when applicable, and terminal-phase report", len(chatModel.inputs))
			}
			if einoMessagesContainToolArguments(chatModel.inputs[2], largeSource) {
				t.Fatal("post-verification model input retained the large workspace mutation payload")
			}
			if runState == nil {
				t.Fatal("assistant run state was not captured")
			}
			checkpointState := runState.CheckpointState()
			checkpointJSON, err := json.Marshal(checkpointState)
			if err != nil {
				t.Fatalf("marshal checkpoint state returned error: %v", err)
			}
			if strings.Contains(string(checkpointJSON), largeSource) {
				t.Fatal("checkpoint state retained the large workspace mutation payload")
			}
			foundCompactedWrite := false
			for _, message := range checkpointState.Messages {
				if message.Role == string(schema.Tool) &&
					projectToolBaseName(message.Name) == projectToolWriteFile &&
					projectEinoAssistantPhaseSuccessfulToolResult(&schema.Message{
						Role:     schema.Tool,
						ToolName: message.Name,
						Content:  message.Content,
					}) {
					foundCompactedWrite = true
				}
			}
			if !foundCompactedWrite {
				t.Fatalf("checkpoint messages = %#v, want retained successful write evidence", checkpointState.Messages)
			}
			if stringSliceContains(chatModel.toolNames[0], projectToolCommitProjectFiles) {
				t.Fatalf("initial tools = %#v, must not contain commit", chatModel.toolNames[0])
			}
			if stringSliceContains(chatModel.toolNames[1], projectToolCommitProjectFiles) {
				t.Fatalf("post-write tools = %#v, must not contain commit before verification", chatModel.toolNames[1])
			}
			if !stringSliceEqual(chatModel.toolNames[2], tt.wantTools) {
				t.Fatalf("post-verification tools = %#v, want terminal phase tools %#v", chatModel.toolNames[2], tt.wantTools)
			}
		})
	}
}

func TestEinoAssistantEngineProfileFiltersReadOnlyAndRuntimeTools(t *testing.T) {
	tests := []struct {
		name       string
		profile    projectAssistantTurnProfile
		policy     projectAssistantTurnPolicy
		wantAllow  []string
		wantReject []string
	}{
		{
			name:       "exploration",
			profile:    projectAssistantTurnProfileExploration,
			wantAllow:  []string{projectToolCheckProjectReadiness},
			wantReject: []string{projectToolGetRuntimeStatus, projectToolRestartRuntime, projectToolWriteFile, projectToolCommitProjectFiles},
		},
		{
			name:       "adaptive",
			profile:    projectAssistantTurnProfileAdaptive,
			wantAllow:  []string{projectToolCheckProjectReadiness, projectToolGetRuntimeStatus, projectToolGetPreviewURL, projectToolRequestProjectPlanApproval},
			wantReject: []string{projectToolRestartRuntime, projectToolWriteFile, projectToolCommitProjectFiles},
		},
		{
			name:       "debugging",
			profile:    projectAssistantTurnProfileDebugging,
			wantAllow:  []string{projectToolCheckProjectReadiness, projectToolGetRuntimeStatus, projectToolGetPreviewURL},
			wantReject: []string{projectToolRestartRuntime, projectToolWriteFile, projectToolCommitProjectFiles},
		},
		{
			name:    "runtime-state exploration",
			profile: projectAssistantTurnProfileExploration,
			policy: projectAssistantTurnPolicy{
				profile:              projectAssistantTurnProfileExploration,
				requiresRuntimeState: true,
			},
			wantAllow:  []string{projectToolCheckProjectReadiness, projectToolGetRuntimeStatus, projectToolGetPreviewURL},
			wantReject: []string{projectToolRestartRuntime, projectToolWriteFile, projectToolCommitProjectFiles},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
			chatModel := &toolCapturingEinoChatModel{content: "read-only answer"}
			var filteredNames []string
			engine := projectEinoAssistantEngine{
				server: server,
				newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
					return chatModel, nil
				},
				newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
					tools, err := projectEinoToolsForProfileTest(t, req, state)
					filteredNames = einoToolNamesForTest(t, tools)
					return tools, err
				},
			}
			req := projectEinoRunRequestForProfileTest(tt.profile)
			req.TurnPolicy = tt.policy
			if _, err := engine.StreamProjectAssistant(context.Background(), req); err != nil {
				t.Fatalf("StreamProjectAssistant returned error: %v", err)
			}
			if len(chatModel.toolNames) != 1 {
				t.Fatalf("%s model calls = %d, want one", tt.profile, len(chatModel.toolNames))
			}
			for _, want := range tt.wantAllow {
				if !stringSliceContains(filteredNames, want) {
					t.Fatalf("%s filtered tools = %#v, want %s", tt.profile, filteredNames, want)
				}
				if !stringSliceContains(chatModel.toolNames[0], want) {
					t.Fatalf("%s model tools = %#v, want policy-selected %s", tt.profile, chatModel.toolNames[0], want)
				}
			}
			for _, unwanted := range tt.wantReject {
				if stringSliceContains(filteredNames, unwanted) {
					t.Fatalf("%s filtered tools = %#v, should not expose %s", tt.profile, filteredNames, unwanted)
				}
				if stringSliceContains(chatModel.toolNames[0], unwanted) {
					t.Fatalf("%s model tools = %#v, should not expose %s", tt.profile, chatModel.toolNames[0], unwanted)
				}
			}
		})
	}
}

func TestEinoAssistantEngineUsesScopedCanonicalFilesystemReads(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if _, err := workspaces.WriteFile(ctx, scope, workspace.WriteOptions{
		Path:    "README.md",
		Content: "# Project README\nCanonical filesystem integration.\n",
	}); err != nil {
		t.Fatalf("write README fixture returned error: %v", err)
	}
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)

	var events []projectToolCallStreamEvent
	var runState *projectEinoAssistantRunState
	readModel := &canonicalFilesystemReadEinoChatModel{}
	readEngine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return readModel, nil
		},
		newTools: func(ctx context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			runState = state
			return newProjectEinoAssistantToolsFactory(server)(ctx, req, state)
		},
	}
	readReq := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileExploration)
	readReq.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileExploration)
	readReq.StreamCallbacks.OnToolCall = func(event projectToolCallStreamEvent) {
		events = append(events, event)
	}
	result, err := readEngine.StreamProjectAssistant(ctx, readReq)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "README inspected" {
		t.Fatalf("result content = %q, want completion", result.Content)
	}
	if len(readModel.toolInfos) < 1 {
		t.Fatal("model captured no tool inventory")
	}
	firstNames := projectEinoAssistantPhaseToolNames(readModel.toolInfos[0])
	for _, name := range []string{projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep} {
		if !stringSliceContains(firstNames, name) {
			t.Errorf("first model inventory = %#v, want %s", firstNames, name)
		}
	}
	for _, name := range []string{"edit_file", "execute"} {
		if stringSliceContains(firstNames, name) {
			t.Errorf("first model inventory = %#v, must not expose %s", firstNames, name)
		}
	}
	if len(readModel.inputs) < 2 ||
		!einoMessagesContainToolResult(readModel.inputs[1], "call-read-readme", "     1\t# Project README") {
		t.Fatalf("second model input = %#v, want line-numbered README result", readModel.inputs)
	}
	if len(events) != 3 {
		t.Fatalf("read events = %#v, want requested/running/succeeded", events)
	}
	for i, status := range []string{"requested", "running", "succeeded"} {
		if events[i].ID != "call-read-readme" || events[i].Name != projectToolReadFile || events[i].Status != status {
			t.Errorf("read event %d = %#v, want call-read-readme %s", i, events[i], status)
		}
	}
	if runState == nil {
		t.Fatal("run state was not captured")
	}
	evidence := runState.CheckpointState().LastToolMessages
	if len(evidence) != 1 || evidence[0].ToolCallID != "call-read-readme" ||
		!strings.Contains(evidence[0].Content, "# Project README") {
		t.Fatalf("run-state read evidence = %#v, want real tool-call ID and result", evidence)
	}

	implementationModel := &canonicalFilesystemReadEinoChatModel{requestFollowUp: true}
	implementationEngine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return implementationModel, nil
		},
		newTools: newProjectEinoAssistantToolsFactory(server),
	}
	implementationReq := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileImplementation)
	implementationReq.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	implementationReq.InitialApprovedPlan = &projectAssistantApprovedPlan{
		Summary:        "Inspect the implementation inventory.",
		Steps:          []string{"Inspect", "Edit"},
		Version:        projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities:   []string{projectAssistantCapabilityWorkspaceMutate},
		AllowAllWrites: true,
	}
	_, err = implementationEngine.StreamProjectAssistant(ctx, implementationReq)
	var inputErr *projectAssistantInputRequiredError
	if !errors.As(err, &inputErr) {
		t.Fatalf("implementation error = %v, want follow-up interrupt after inventory capture", err)
	}
	if len(implementationModel.toolInfos) != 1 {
		t.Fatalf("implementation inventories = %d, want one before interrupt", len(implementationModel.toolInfos))
	}
	writeCount := 0
	for _, info := range implementationModel.toolInfos[0] {
		if info == nil || info.Name != projectToolWriteFile {
			continue
		}
		writeCount++
		if info.Extra["risk"] != string(projectAssistantToolRiskWrite) ||
			info.Extra["bundle"] != string(projectAssistantToolBundleEdit) {
			t.Fatalf("write_file metadata = %#v, want App Studio write/edit metadata", info.Extra)
		}
	}
	if writeCount != 1 {
		t.Fatalf("implementation write_file count = %d, want only App Studio registry tool", writeCount)
	}

	for _, profile := range []projectAssistantTurnProfile{
		projectAssistantTurnProfileDiscussion,
		projectAssistantTurnProfileGuidance,
	} {
		t.Run(string(profile), func(t *testing.T) {
			model := &canonicalFilesystemReadEinoChatModel{directAnswer: true}
			engine := projectEinoAssistantEngine{
				server: server,
				newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
					return model, nil
				},
				newTools: newProjectEinoAssistantToolsFactory(server),
			}
			req := projectEinoRunRequestForProfileTest(profile)
			req.Project.Name = "demo-" + string(profile)
			req.WorkspaceScope.ProjectName = req.Project.Name
			req.MessageScope.ProjectName = req.Project.Name
			req.TurnPolicy = projectAssistantTurnPolicyForProfile(profile)
			if _, err := engine.StreamProjectAssistant(ctx, req); err != nil {
				t.Fatalf("StreamProjectAssistant returned error: %v", err)
			}
			if len(model.toolInfos) != 1 {
				t.Fatalf("model inventories = %d, want one", len(model.toolInfos))
			}
			names := projectEinoAssistantPhaseToolNames(model.toolInfos[0])
			for _, name := range []string{projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep} {
				if stringSliceContains(names, name) {
					t.Errorf("%s inventory = %#v, must not expose %s", profile, names, name)
				}
			}
		})
	}
}

func TestEinoAssistantEngineReportsCanonicalFilesystemBackendFailureAsFailed(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	model := &canonicalFilesystemReadEinoChatModel{
		readPath:   "missing/README.md",
		completion: "continued after safe read failure",
	}
	var events []projectToolCallStreamEvent
	var runState *projectEinoAssistantRunState
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: func(ctx context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			runState = state
			return newProjectEinoAssistantToolsFactory(server)(ctx, req, state)
		},
	}
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileExploration)
	req.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileExploration)
	req.StreamCallbacks.OnToolCall = func(event projectToolCallStreamEvent) {
		events = append(events, event)
	}

	result, err := engine.StreamProjectAssistant(ctx, req)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "continued after safe read failure" {
		t.Fatalf("result content = %q, want model to continue after safe tool error", result.Content)
	}
	if len(events) != 3 {
		t.Fatalf("events = %#v, want requested/running/failed", events)
	}
	for i, status := range []string{"requested", "running", "failed"} {
		if events[i].Status != status || events[i].ID != "call-read-readme" {
			t.Errorf("event %d = %#v, want call-read-readme %s", i, events[i], status)
		}
	}
	for _, event := range events {
		if event.Status == "succeeded" {
			t.Fatalf("backend failure emitted succeeded telemetry: %#v", events)
		}
	}
	if len(model.inputs) < 2 ||
		!einoMessagesContainToolResult(model.inputs[1], "call-read-readme", "Tool call failed:") {
		t.Fatalf("second model input = %#v, want existing safe-shaped tool failure", model.inputs)
	}
	if runState == nil {
		t.Fatal("run state was not captured")
	}
	evidence := runState.CheckpointState().LastToolMessages
	if len(evidence) != 1 || evidence[0].ToolCallID != "call-read-readme" ||
		!strings.HasPrefix(evidence[0].Content, "Tool call failed: ") ||
		evidence[0].Content != truncateProjectToolInfo(evidence[0].Content) {
		t.Fatalf("run-state failure evidence = %#v, want bounded safe failure with real call ID", evidence)
	}
}

func TestEinoAssistantEngineTerminalPhaseDeniesCanonicalReadBeforeTelemetry(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	const unreadContent = "terminal-phase-endpoint-must-not-run"
	if _, err := workspaces.WriteFile(ctx, scope, workspace.WriteOptions{
		Path:    "README.md",
		Content: unreadContent,
	}); err != nil {
		t.Fatalf("write README fixture returned error: %v", err)
	}
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	model := &canonicalFilesystemReadEinoChatModel{completion: "reported terminal denial"}
	var events []projectToolCallStreamEvent
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileImplementation)
	req.TurnPolicy = projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
	req.StreamCallbacks.OnToolCall = func(event projectToolCallStreamEvent) {
		events = append(events, event)
	}
	req.InitialApprovedPlan = &projectAssistantApprovedPlan{
		Summary:        "Previously completed initial project build.",
		Steps:          []string{"Write", "Verify"},
		Version:        projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities:   []string{projectAssistantCapabilityWorkspaceMutate},
		AllowAllWrites: true,
	}
	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(req.TurnPolicy)
	runState.ApprovePlan(*req.InitialApprovedPlan)
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: newProjectEinoAssistantToolsFactory(server),
	}
	agent, err := engine.newAgent(ctx, req, runState)
	if err != nil {
		t.Fatalf("newAgent returned error: %v", err)
	}
	iter := agent.Run(ctx, &adk.AgentInput{Messages: []*schema.Message{
		schema.UserMessage("Report the completed work."),
		projectEinoAssistantPhaseToolResult(projectToolWriteFile, `{"operation":"write_file"}`),
		projectEinoAssistantPhaseToolResult(projectToolVerifyDevelopmentRuntime, `{"status":"ready"}`),
	}})
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatalf("agent event returned error: %v", event.Err)
		}
	}
	if len(model.inputs) < 2 ||
		!einoMessagesContainToolResult(model.inputs[1], "call-read-readme", "Tool call denied:") {
		t.Fatalf("second model input = %#v, want terminal-phase denial", model.inputs)
	}
	if einoMessagesContainContent(model.inputs[1], unreadContent) {
		t.Fatal("terminal phase reached the canonical filesystem endpoint")
	}
	if len(events) != 0 {
		t.Fatalf("terminal phase denial emitted filesystem telemetry: %#v", events)
	}
}

func TestEinoAssistantEngineWriteProfilesRetainPlanApprovalBeforeWrites(t *testing.T) {
	for _, profile := range []projectAssistantTurnProfile{
		projectAssistantTurnProfileImplementation,
		projectAssistantTurnProfileDebugFix,
	} {
		t.Run(string(profile), func(t *testing.T) {
			server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
			chatModel := &planThenWriteEinoChatModel{}
			engine := projectEinoAssistantEngine{
				server: server,
				newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
					return chatModel, nil
				},
				newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
					return projectEinoToolsForProfileTest(t, req, state)
				},
			}
			_, err := engine.StreamProjectAssistant(context.Background(), projectEinoRunRequestForProfileTest(profile))
			var permissionErr *projectAssistantPermissionRequiredError
			if !errors.As(err, &permissionErr) {
				t.Fatalf("StreamProjectAssistant error = %v, want plan approval permission", err)
			}
			if permissionErr.ToolName != projectToolRequestProjectPlanApproval {
				t.Fatalf("permission tool = %q, want %s", permissionErr.ToolName, projectToolRequestProjectPlanApproval)
			}
		})
	}
}

func TestEinoAssistantCheckpointPreservesTurnPolicy(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.SetTurnPolicy(projectAssistantTurnPolicy{
		profile:              projectAssistantTurnProfileExploration,
		requiresRuntimeState: true,
	})
	checkpoint := runState.CheckpointState()
	if checkpoint.TurnPolicy.Profile != projectAssistantTurnProfileExploration || !checkpoint.TurnPolicy.RequiresRuntimeState {
		t.Fatalf("checkpoint policy = %#v, want runtime-state exploration", checkpoint.TurnPolicy)
	}
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	if got := restored.TurnPolicy(); got.profile != projectAssistantTurnProfileExploration || !got.requiresRuntimeState {
		t.Fatalf("restored policy = %#v, want runtime-state exploration", got)
	}
}

func TestEinoAssistantCheckpointHashesSeenToolCallArgumentsAndRestoresLegacySignatures(t *testing.T) {
	const secretSource = "checkpoint-secret-source"
	call := chatToolCall{
		ID:   "call-write",
		Type: "function",
		Function: chatToolCallFunction{
			Name:      projectToolWriteFile,
			Arguments: `{"path":"src/App.tsx","content":"` + secretSource + `"}`,
		},
	}
	legacySignature := call.Function.Name + "\x00" + call.Function.Arguments
	runState := newProjectEinoAssistantRunState()
	runState.RestoreCheckpointState(projectAssistantCheckpointState{
		SeenToolCalls: map[string]int{legacySignature: 1},
	})
	runState.RecordAssistantReply(projectAssistantReply{ToolCalls: []chatToolCall{call}})

	checkpoint := runState.CheckpointState()
	if len(checkpoint.SeenToolCalls) != 1 {
		t.Fatalf("seen tool signatures = %#v, want one hashed signature", checkpoint.SeenToolCalls)
	}
	for signature, count := range checkpoint.SeenToolCalls {
		if !strings.HasPrefix(signature, "sha256:") || strings.Contains(signature, secretSource) {
			t.Fatalf("seen tool signature = %q, want non-reversible hash", signature)
		}
		if count != 2 {
			t.Fatalf("seen tool count = %d, want legacy and current calls combined", count)
		}
	}
	raw, err := json.Marshal(checkpoint.SeenToolCalls)
	if err != nil {
		t.Fatalf("marshal checkpoint returned error: %v", err)
	}
	if strings.Contains(string(raw), secretSource) {
		t.Fatalf("seen tool signatures retained tool-call source payload: %s", raw)
	}
}

func TestEinoAssistantBoundedClosingInputIncludesAllActionEvidence(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.RecordModelInput([]chatMessage{
		{Role: "system", Content: "Build the approved project."},
		{Role: "user", Content: "Implement the profile page."},
	})
	runState.RecordAssistantReply(projectAssistantReply{ToolCalls: []chatToolCall{{
		ID: "call-write",
		Function: chatToolCallFunction{
			Name:      projectToolWriteFile,
			Arguments: `{"file_path":"src/Profile.tsx"}`,
		},
	}}})
	runState.RecordToolMessage(chatMessage{
		Role:       "tool",
		Name:       projectToolWriteFile,
		ToolCallID: "call-write",
		Content:    `{"operation":"write_file","path":"src/Profile.tsx","size":128}`,
	})
	runState.RecordAssistantReply(projectAssistantReply{ToolCalls: []chatToolCall{{
		ID: "call-verify",
		Function: chatToolCallFunction{
			Name:      projectToolVerifyDevelopmentRuntime,
			Arguments: `{}`,
		},
	}}})
	runState.RecordToolMessage(chatMessage{
		Role:       "tool",
		Name:       projectToolVerifyDevelopmentRuntime,
		ToolCallID: "call-verify",
		Content:    `{"phase":"Failed","message":"npm test failed"}`,
	})

	checkpoint := runState.CheckpointState()
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	messages := restored.ToolLoopFinalAnswerMessages()
	var joined string
	for _, msg := range messages {
		if msg.Role == "tool" || len(msg.ToolCalls) > 0 {
			t.Fatalf("closing input contains executable tool history: %#v", messages)
		}
		joined += msg.Content + "\n"
	}
	for _, want := range []string{
		"Project action evidence",
		projectToolWriteFile,
		"src/Profile.tsx",
		projectToolVerifyDevelopmentRuntime,
		"npm test failed",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("closing input = %q, want evidence %q", joined, want)
		}
	}
}

func TestEinoAssistantEngineAddsProjectSnapshotToInput(t *testing.T) {
	chatModel := &capturingEinoChatModel{content: "snapshot received"}
	workspaces := workspace.NewFileStore(t.TempDir())
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Spec.DisplayName = "Demo App"
	project.Spec.Memory.Requirements = []string{"ship a tested build"}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	scope := projectWorkspaceScope(id, project.Name)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "package.json", Content: `{"scripts":{"build":"vite build","test":"vitest"}}`}); err != nil {
		t.Fatalf("WriteFile package.json returned error: %v", err)
	}
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "src/App.tsx", Content: "export function App() { return null }\n"}); err != nil {
		t.Fatalf("WriteFile src/App.tsx returned error: %v", err)
	}
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}

	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Identity:       id,
			Project:        project,
			Repository:     &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady, Ready: true},
			WorkspaceScope: scope,
			Workspace:      workspaces,
		},
	)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if len(chatModel.inputs) == 0 {
		t.Fatal("model received no input")
	}
	if chatModel.sessionSnapshot == nil {
		t.Fatal("model saw no App Studio project snapshot in Eino session values")
	}
	if !chatModel.sessionSnapshot.RepoReady {
		t.Fatalf("session snapshot repoReady = false, want true")
	}
	if !stringSliceEqual(chatModel.sessionSnapshot.LastFileSnapshot, []string{"package.json", "src/App.tsx"}) {
		t.Fatalf("session snapshot files = %#v, want package.json and src/App.tsx", chatModel.sessionSnapshot.LastFileSnapshot)
	}
	input := chatModel.inputs[0]
	if !einoMessagesContainContent(input, "Current project snapshot (") {
		t.Fatalf("input = %#v, want compact project snapshot system message", input)
	}
	for _, want := range []string{
		`"repoReady":true`,
		`"lastKnownBranch"`,
		`"lastFileSnapshot":["package.json","src/App.tsx"]`,
		`"recommendedChecks":["build","test"]`,
	} {
		if !einoMessagesContainContent(input, want) {
			t.Fatalf("input = %#v, want snapshot content %s", input, want)
		}
	}
}

func TestEinoAssistantRunStateCheckpointsProjectSnapshot(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.SetSessionSnapshot(projectEinoAssistantSessionSnapshot{
		ProjectName:       "demo",
		RepoReady:         true,
		LastKnownBranch:   "main",
		LastFileSnapshot:  []string{"package.json"},
		RecommendedChecks: []string{"build"},
	})

	checkpoint := runState.CheckpointState()
	if checkpoint.SessionSnapshot == nil {
		t.Fatal("checkpoint session snapshot = nil, want snapshot")
	}
	checkpoint.SessionSnapshot.LastFileSnapshot[0] = "mutated"

	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	snapshot := restored.SessionSnapshot()
	if snapshot == nil {
		t.Fatal("restored session snapshot = nil, want snapshot")
	}
	if snapshot.ProjectName != "demo" || !snapshot.RepoReady || snapshot.LastKnownBranch != "main" {
		t.Fatalf("restored snapshot = %#v, want project/repo state", snapshot)
	}
	if !stringSliceEqual(snapshot.LastFileSnapshot, []string{"mutated"}) {
		t.Fatalf("restored files = %#v, want checkpoint value", snapshot.LastFileSnapshot)
	}
	checkpoint.SessionSnapshot.LastFileSnapshot[0] = "mutated-again"
	if !stringSliceEqual(restored.SessionSnapshot().LastFileSnapshot, []string{"mutated"}) {
		t.Fatalf("restored snapshot aliases checkpoint files")
	}
}

func TestEinoAssistantEngineFailsWhenTurnLoopHasNoAssistantOutput(t *testing.T) {
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return emptyOutputEinoChatModel{}, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Project: &aiv1alpha1.Project{},
		},
	)
	if !errors.Is(err, errProjectAssistantNoOutput) {
		t.Fatalf("StreamProjectAssistant error = %v, want no-output failure", err)
	}
	if !projectEinoAssistantBoundedClosingAnswerValid(result.Content) {
		t.Fatalf("result content = %q, want structured incomplete handoff", result.Content)
	}
}

func TestEinoAssistantEngineAcceptsAssistantMultiContentOutput(t *testing.T) {
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return multiContentOutputEinoChatModel{}, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Project:     &aiv1alpha1.Project{},
			TurnProfile: projectAssistantTurnProfileDiscussion,
		},
	)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "multi content answer" {
		t.Fatalf("content = %q, want multi content answer", result.Content)
	}
}

func TestEinoAssistantEngineSummarizesLongProjectSessions(t *testing.T) {
	chatModel := &summarizingEinoChatModel{}
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}
	history := make([]store.Message, 0, projectEinoAssistantSummaryContextMessages+2)
	for i := 0; i < projectEinoAssistantSummaryContextMessages+2; i++ {
		role := aiv1alpha1.ProjectMessageRoleUser
		if i%2 == 1 {
			role = aiv1alpha1.ProjectMessageRoleAssistant
		}
		history = append(history, store.Message{
			ID:      "message",
			Role:    role,
			Content: "Need a production dashboard with auth, metrics, and repository handoff.",
		})
	}
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Project: &aiv1alpha1.Project{},
			History: history,
		},
	)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "continued with summarized context" {
		t.Fatalf("content = %q, want summarized continuation", result.Content)
	}
	if chatModel.summaryCalls != 1 {
		t.Fatalf("summary calls = %d, want one Eino summarization call", chatModel.summaryCalls)
	}
	if len(chatModel.inputs) != 2 {
		t.Fatalf("model calls = %d, want summarization plus assistant continuation", len(chatModel.inputs))
	}
	if !einoMessagesContainContent(chatModel.inputs[1], "summary: production dashboard requirements retained") {
		t.Fatalf("assistant input = %#v, want generated summary in continuation context", chatModel.inputs[1])
	}
}

func TestEinoAssistantEngineContinuesWhenSummaryIsEmpty(t *testing.T) {
	chatModel := &blankSummaryEinoChatModel{}
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}
	history := make([]store.Message, 0, projectEinoAssistantSummaryContextMessages+2)
	for i := 0; i < projectEinoAssistantSummaryContextMessages+2; i++ {
		role := aiv1alpha1.ProjectMessageRoleUser
		if i%2 == 1 {
			role = aiv1alpha1.ProjectMessageRoleAssistant
		}
		history = append(history, store.Message{
			ID:      "message",
			Role:    role,
			Content: "Need a production dashboard with auth, metrics, and repository handoff.",
		})
	}
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Project: &aiv1alpha1.Project{},
			History: history,
		},
	)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "continued after blank summary" {
		t.Fatalf("content = %q, want assistant continuation", result.Content)
	}
	if chatModel.summaryCalls != 1 {
		t.Fatalf("summary calls = %d, want one Eino summarization call", chatModel.summaryCalls)
	}
	if len(chatModel.inputs) != 2 {
		t.Fatalf("model calls = %d, want summarization plus assistant continuation", len(chatModel.inputs))
	}
	if !einoMessagesContainContent(chatModel.inputs[1], "Summary unavailable; preserving recent App Studio context") {
		t.Fatalf("assistant input = %#v, want fallback summary in continuation context", chatModel.inputs[1])
	}
}

func TestEinoAssistantEngineAsksFollowUpThroughEinoInterrupt(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	followUpTool, ok := server.projectAssistantToolRegistry().Get(projectToolAskFollowUp)
	if !ok {
		t.Fatal("ask_follow_up tool missing")
	}
	chatModel := &followUpEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, followUpTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Spec.Memory.Requirements = []string{"ship a tested build"}
	req := projectAssistantRunRequest{
		Identity:       id,
		Project:        project,
		Repository:     &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady, Ready: true},
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
	}
	req = attachProjectAssistantDiscussionRunForEngineTest(t, server, messages, req, "follow-up")
	if _, err := workspaces.WriteFile(context.Background(), req.WorkspaceScope, workspace.WriteOptions{Path: "package.json", Content: `{"scripts":{"build":"vite build","test":"vitest"}}`}); err != nil {
		t.Fatalf("WriteFile package.json returned error: %v", err)
	}
	var assistantEvents []projectAssistantEvent
	streamReq := req
	streamReq.StreamCallbacks.OnAssistantEvent = func(event projectAssistantEvent) {
		assistantEvents = append(assistantEvents, event)
	}

	_, err := engine.StreamProjectAssistant(context.Background(), streamReq)
	var inputErr *projectAssistantInputRequiredError
	if !errors.As(err, &inputErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want input required", err)
	}
	if inputErr.RunID == "" || inputErr.RequestID == "" {
		t.Fatalf("input error = %#v, want run and request IDs", inputErr)
	}
	if messages.saveAssistantRunCount != 1 {
		t.Fatalf("assistant run snapshots = %d, want the attached supervisor checkpoint", messages.saveAssistantRunCount)
	}
	if countProjectAssistantEvents(assistantEvents, projectAssistantEventInputNeeded) != 1 || countProjectAssistantEvents(assistantEvents, projectAssistantEventCheckpointSaved) != 1 {
		t.Fatalf("assistant events = %#v, want input required and checkpoint events", assistantEvents)
	}
	run, err := messages.GetAssistantRun(context.Background(), req.MessageScope, inputErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusPendingInput {
		t.Fatalf("run status = %q, want pending input", run.Status)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	if checkpoint.Eino == nil || checkpoint.Eino.InterruptType != projectAssistantInterruptTypeFollowUp || checkpoint.Eino.InterruptID == "" {
		t.Fatalf("checkpoint eino state = %#v, want follow-up turn loop checkpoint and interrupt id", checkpoint.Eino)
	}
	if checkpoint.SessionSnapshot == nil {
		t.Fatal("checkpoint session snapshot = nil, want persisted project snapshot")
	}
	if !checkpoint.SessionSnapshot.RepoReady || checkpoint.SessionSnapshot.RepositoryRef != "demo-repo" {
		t.Fatalf("checkpoint snapshot repository = %#v, want ready demo-repo", checkpoint.SessionSnapshot)
	}
	if !stringSliceEqual(checkpoint.SessionSnapshot.LastFileSnapshot, []string{"package.json"}) {
		t.Fatalf("checkpoint snapshot files = %#v, want package.json", checkpoint.SessionSnapshot.LastFileSnapshot)
	}

	result, err := engine.ResumeProjectAssistant(
		context.Background(),
		req,
		projectAssistantResumeRequest{
			RequestID: inputErr.RequestID,
			Answer:    "A compact React task dashboard for solo founders.",
		},
		checkpoint,
	)
	if err != nil {
		t.Fatalf("ResumeProjectAssistant returned error: %v", err)
	}
	if result.Content != "thanks, I can build that" {
		t.Fatalf("content = %q, want resumed follow-up response", result.Content)
	}
	if len(chatModel.inputs) != 2 || !einoMessagesContainToolResult(chatModel.inputs[1], "call-follow-up", "solo founders") {
		t.Fatalf("model inputs = %#v, want follow-up answer as tool result", chatModel.inputs)
	}
	if len(chatModel.sessionSnapshots) != 2 || chatModel.sessionSnapshots[1] == nil {
		t.Fatalf("model session snapshots = %#v, want snapshot on resumed model call", chatModel.sessionSnapshots)
	}
	if !chatModel.sessionSnapshots[1].RepoReady || !stringSliceEqual(chatModel.sessionSnapshots[1].LastFileSnapshot, []string{"package.json"}) {
		t.Fatalf("resumed session snapshot = %#v, want checkpointed project snapshot", chatModel.sessionSnapshots[1])
	}
}

func TestProjectEinoAssistantToolInfoPreservesSchemaAndRisk(t *testing.T) {
	projectTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        projectToolWriteFile,
			Description: "Write a file.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
			Risk:        projectAssistantToolRiskWrite,
		},
	}
	info, err := newProjectEinoAssistantTool(projectTool, projectAssistantRunRequest{}, newProjectEinoAssistantRunState()).Info(context.Background())
	if err != nil {
		t.Fatalf("Info returned error: %v", err)
	}
	if info.Name != projectToolWriteFile || info.Desc != "Write a file." {
		t.Fatalf("tool info = %#v, want App Studio spec metadata", info)
	}
	if info.Extra["risk"] != string(projectAssistantToolRiskWrite) {
		t.Fatalf("tool risk = %#v, want write", info.Extra["risk"])
	}
	if info.Extra["bundle"] != string(projectAssistantToolBundleEdit) {
		t.Fatalf("tool bundle = %#v, want edit", info.Extra["bundle"])
	}
	if info.ParamsOneOf == nil {
		t.Fatal("ParamsOneOf is nil, want JSON schema parameters")
	}
}

func TestProjectEinoAssistantToolInfoClassifiesProductWorkflowBundles(t *testing.T) {
	tests := []struct {
		name string
		spec projectAssistantToolSpec
		want projectAssistantToolBundle
	}{
		{
			name: "workflow",
			spec: projectAssistantToolSpec{Name: projectToolCheckProjectReadiness, Risk: projectAssistantToolRiskRead},
			want: projectAssistantToolBundleWorkflow,
		},
		{
			name: "workspace read",
			spec: projectAssistantToolSpec{Name: projectToolReadFile, Risk: projectAssistantToolRiskRead},
			want: projectAssistantToolBundleWorkspaceRead,
		},
		{
			name: "edit",
			spec: projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
			want: projectAssistantToolBundleEdit,
		},
		{
			name: "repo",
			spec: projectAssistantToolSpec{Name: projectToolCommitProjectFiles, Risk: projectAssistantToolRiskCommit},
			want: projectAssistantToolBundleRepo,
		},
		{
			name: "runtime",
			spec: projectAssistantToolSpec{Name: "restart_runtime", Risk: projectAssistantToolRiskRuntime},
			want: projectAssistantToolBundleRuntime,
		},
		{
			name: "infrastructure read",
			spec: projectAssistantToolSpec{Name: projectToolInfrastructureListTemplates, Risk: projectAssistantToolRiskRead},
			want: projectAssistantToolBundleInfrastructure,
		},
		{
			name: "infrastructure provision",
			spec: projectAssistantToolSpec{Name: projectToolInfrastructureProvision, Risk: projectAssistantToolRiskRuntime},
			want: projectAssistantToolBundleInfrastructure,
		},
		{
			name: "unknown write risk",
			spec: projectAssistantToolSpec{Name: "replace_project_file", Risk: projectAssistantToolRiskWrite},
			want: projectAssistantToolBundleEdit,
		},
		{
			name: "unknown commit risk",
			spec: projectAssistantToolSpec{Name: "push_project_changes", Risk: projectAssistantToolRiskCommit},
			want: projectAssistantToolBundleRepo,
		},
		{
			name: "collaboration",
			spec: projectAssistantToolSpec{Name: projectToolAskFollowUp, Risk: projectAssistantToolRiskInput},
			want: projectAssistantToolBundleCollaboration,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectTool := &recordingProjectAssistantTool{spec: tt.spec}
			info, err := newProjectEinoAssistantTool(projectTool, projectAssistantRunRequest{}, newProjectEinoAssistantRunState()).Info(context.Background())
			if err != nil {
				t.Fatalf("Info returned error: %v", err)
			}
			if got := info.Extra["bundle"]; got != string(tt.want) {
				t.Fatalf("bundle = %#v, want %q", got, tt.want)
			}
		})
	}
}

func TestEinoAssistantEngineStopsToolBatchAfterPermissionRequest(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	writeTool, ok := server.projectAssistantToolRegistry().Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	chatModel := &multipleToolCallEinoChatModel{toolCalls: []schema.ToolCall{
		{
			ID:   "call-one",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/one.tsx","content":"one"}`,
			},
		},
		{
			ID:   "call-two",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/two.tsx","content":"two"}`,
			},
		},
	}}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, writeTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	var assistantEvents []projectAssistantEvent
	var toolEvents []projectToolCallStreamEvent
	req := attachProjectAssistantBuildRunForEngineTest(t, server, projectAssistantRunRequest{
		Identity:       id,
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnAssistantEvent: func(event projectAssistantEvent) {
				assistantEvents = append(assistantEvents, event)
			},
			OnToolCall: func(event projectToolCallStreamEvent) {
				toolEvents = append(toolEvents, event)
			},
		},
	}, "stop-tool-batch")
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		req,
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want permission required", err)
	}
	if messages.saveAssistantRunCount != 1 {
		t.Fatalf("assistant run saves = %d, want one durable permission checkpoint", messages.saveAssistantRunCount)
	}
	if countProjectAssistantEvents(assistantEvents, projectAssistantEventPermissionNeeded) != 1 || countProjectAssistantEvents(assistantEvents, projectAssistantEventCheckpointSaved) != 1 {
		t.Fatalf("assistant events = %#v, want one permission and one checkpoint", assistantEvents)
	}
	if projectToolEventsWithStatus(toolEvents, "permission_required") != 1 {
		t.Fatalf("tool events = %#v, want exactly one permission-required tool event", toolEvents)
	}
	run, err := messages.GetAssistantRun(context.Background(), req.MessageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	if checkpoint.Eino == nil || len(checkpoint.Eino.Checkpoint) == 0 || checkpoint.Eino.InterruptID == "" {
		t.Fatalf("checkpoint eino state = %#v, want turn loop checkpoint and interrupt id", checkpoint.Eino)
	}
	turnCheckpoint := decodeProjectEinoTurnLoopCheckpointForTest(t, checkpoint.Eino.Checkpoint)
	if !turnCheckpoint.HasRunnerState || len(turnCheckpoint.CanceledItems) != 1 || turnCheckpoint.CanceledItems[0].Kind != projectAssistantTurnMessage {
		t.Fatalf("turn loop checkpoint = %#v, want interrupted message turn with runner state", turnCheckpoint)
	}
}

func TestEinoAssistantEngineRequiresPermissionForRuntimeGraphTool(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	chatModel := &deployRuntimeEinoChatModel{}
	runtimeSpec, ok := server.projectAssistantToolRegistry().Spec(projectToolRestartRuntime)
	if !ok {
		t.Fatal("restart_runtime tool missing")
	}
	runtimeTool := &recordingProjectAssistantTool{
		spec:   runtimeSpec,
		result: `{"status":"not_configured"}`,
	}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, runtimeTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	var assistantEvents []projectAssistantEvent
	req := projectAssistantRunRequest{
		Identity:       identity{orgUUID: id.orgUUID, workspaceUUID: id.workspaceUUID, tenantPath: id.tenantPath, user: "alice"},
		ToolPort:       projectAssistantDirectToolPort{},
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnAssistantEvent: func(event projectAssistantEvent) {
				assistantEvents = append(assistantEvents, event)
			},
		},
	}
	req = attachProjectAssistantBuildRunForEngineTest(t, server, req, "runtime-permission")
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		req,
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want runtime permission required", err)
	}
	if permissionErr.ToolName != projectToolRestartRuntime {
		t.Fatalf("permission tool = %q, want %s", permissionErr.ToolName, projectToolRestartRuntime)
	}
	if countProjectAssistantEvents(assistantEvents, projectAssistantEventPermissionNeeded) != 1 || countProjectAssistantEvents(assistantEvents, projectAssistantEventCheckpointSaved) != 1 {
		t.Fatalf("assistant events = %#v, want one permission and one checkpoint", assistantEvents)
	}
	run, err := messages.GetAssistantRun(context.Background(), req.MessageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	if checkpoint.Eino == nil || checkpoint.Eino.InterruptType != projectAssistantInterruptTypePermission || checkpoint.Eino.ToolName != projectToolRestartRuntime {
		t.Fatalf("checkpoint eino state = %#v, want runtime permission checkpoint", checkpoint.Eino)
	}
	claimed := claimProjectAssistantRunForWorkItemCommitTest(t, server, messages, req.MessageScope, run, permissionErr.RequestID)
	req.AssistantRun = &claimed

	result, err := engine.ResumeProjectAssistant(
		context.Background(),
		req,
		projectAssistantResumeRequest{
			RequestID: permissionErr.RequestID,
			Decision:  string(projectAssistantPermissionAllow),
		},
		checkpoint,
	)
	if err != nil {
		t.Fatalf("ResumeProjectAssistant returned error: %v", err)
	}
	if result.Content != "runtime deployed" {
		t.Fatalf("content = %q, want final runtime response", result.Content)
	}
	if len(chatModel.inputs) < 2 || !einoMessagesContainToolResult(chatModel.inputs[len(chatModel.inputs)-1], "call-deploy-runtime", "not_configured") {
		t.Fatalf("model inputs = %#v, want resumed runtime graph tool result", chatModel.inputs)
	}
}

func TestEinoAssistantEngineRejectsHiddenDirectWriteToolBeforeInvocation(t *testing.T) {
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	chatModel := &dynamicWritePermissionEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			var out []einotool.BaseTool
			for _, tool := range server.projectAssistantToolRegistry().Tools(true) {
				out = append(out, newProjectEinoAssistantServerTool(server, tool, req, state))
			}
			return out, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	writeCompletions := 0
	req := projectAssistantRunRequest{
		Identity:           id,
		ToolPort:           newProjectAssistantHTTPToolPort(server, httptest.NewRequest(http.MethodPost, "/", nil)),
		executionAuthority: &projectAssistantExplicitTestAuthority{},
		Project:            project,
		WorkspaceScope:     projectWorkspaceScope(id, project.Name),
		MessageScope:       testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:          workspaces,
		TurnProfile:        projectAssistantTurnProfileImplementation,
		StreamCallbacks: projectAssistantStreamCallbacks{OnToolCall: func(event projectToolCallStreamEvent) {
			if event.Name == projectToolWriteFile && event.Status == "succeeded" {
				writeCompletions++
			}
		}},
	}

	result, err := engine.StreamProjectAssistant(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "dynamic write completed" {
		t.Fatalf("content = %q, want model-visible phase denial report", result.Content)
	}
	if len(chatModel.toolNames) == 0 {
		t.Fatal("model was not invoked")
	}
	if stringSliceContains(chatModel.toolNames[0], projectToolWriteFile) {
		t.Fatalf("initial tools = %#v, must hide write_file before plan approval", chatModel.toolNames[0])
	}
	if stringSliceContains(chatModel.toolNames[0], "tool_search") {
		t.Fatalf("initial tools = %#v, want no tool_search without provider tools", chatModel.toolNames[0])
	}
	if writeCompletions != 0 {
		t.Fatalf("write completions = %d, want hidden write never invoked", writeCompletions)
	}
	if _, err := workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"}); err == nil {
		t.Fatal("hidden write created src/App.tsx")
	}
}

func TestEinoAssistantEngineApprovedPlanAllowsOnlyScopedWrite(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	writeTool, ok := server.projectAssistantToolRegistry().Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	chatModel := &multipleToolCallEinoChatModel{toolCalls: []schema.ToolCall{
		{
			ID:   "call-one",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/one.tsx","content":"one"}`,
			},
		},
		{
			ID:   "call-two",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/two.tsx","content":"two"}`,
			},
		},
	}}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, writeTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	var assistantEvents []projectAssistantEvent
	var toolEvents []projectToolCallStreamEvent
	approvedPlan := projectAssistantApprovedPlan{
		Summary:      "Write the first source file only.",
		Steps:        []string{"write the first source file"},
		TargetPaths:  []string{"src/one.tsx"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
	}
	req := projectAssistantRunRequest{
		Identity:       identity{orgUUID: id.orgUUID, workspaceUUID: id.workspaceUUID, tenantPath: id.tenantPath, user: "alice"},
		Project:        project,
		Workspace:      workspaces,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnAssistantEvent: func(event projectAssistantEvent) {
				assistantEvents = append(assistantEvents, event)
			},
			OnToolCall: func(event projectToolCallStreamEvent) {
				toolEvents = append(toolEvents, event)
			},
		},
	}
	req = attachProjectAssistantBuildRunForEngineTest(t, server, req, "auto-approve-scoped-write")
	if _, err := server.persistProjectAssistantWorkItemApprovedPlan(
		context.Background(), req.MessageScope, req.Identity.user, *req.AssistantRun, &approvedPlan, "",
	); err != nil {
		t.Fatalf("persist approved plan: %v", err)
	}
	refreshedRun, err := messages.GetAssistantRun(context.Background(), req.MessageScope, req.AssistantRun.ID)
	if err != nil {
		t.Fatalf("refresh approved run: %v", err)
	}
	req.AssistantRun = &refreshedRun
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		req,
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant returned error %v, want fresh permission for out-of-plan write", err)
	}
	if permissionErr.ToolName != projectToolWriteFile {
		t.Fatalf("permission = %#v, want write_file", permissionErr)
	}
	if strings.TrimSpace(result.Content) != "" {
		t.Fatalf("content = %q, want interrupted turn", result.Content)
	}
	if countProjectAssistantEvents(assistantEvents, projectAssistantEventPermissionNeeded) != 1 || countProjectAssistantEvents(assistantEvents, projectAssistantEventCheckpointSaved) != 1 {
		t.Fatalf("assistant events = %#v, want one permission checkpoint", assistantEvents)
	}
	if projectToolEventsWithStatus(toolEvents, "permission_required") != 1 {
		t.Fatalf("tool events = %#v, want one permission-required event", toolEvents)
	}
	if _, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: "src/one.tsx"}); err != nil {
		t.Fatalf("approved src/one.tsx write failed: %v", err)
	}
	if _, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: "src/two.tsx"}); err == nil {
		t.Fatal("out-of-plan src/two.tsx write unexpectedly succeeded")
	}
}

func TestProjectEinoAssistantHistoryRestoresMutation(t *testing.T) {
	history := []store.Message{
		{
			Role: "assistant",
			Metadata: map[string]any{
				projectMessageMetadataAssistantActionFeed: []projectAssistantActionFeedItem{{
					Kind:   projectAssistantActionFeedItemEdit,
					Status: projectAssistantActionFeedStatusSucceeded,
				}},
			},
		},
	}
	if !projectEinoAssistantHistoryHasSourceMutation(history) {
		t.Fatal("successful durable edit was not restored as a source mutation")
	}
}

func TestEinoAssistantEngineInitialCreationPlanAllowsWriteWithoutPersistingGrant(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	writeTool, ok := server.projectAssistantToolRegistry().Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	chatModel := &multipleToolCallEinoChatModel{toolCalls: []schema.ToolCall{{
		ID:   "call-write",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      projectToolWriteFile,
			Arguments: `{"path":"src/App.tsx","content":"initial build\n"}`,
		},
	}}}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, writeTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	scope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	req := projectAssistantRunRequest{
		Identity:            identity{orgUUID: id.orgUUID, workspaceUUID: id.workspaceUUID, tenantPath: id.tenantPath, user: "alice"},
		Project:             project,
		Workspace:           workspaces,
		WorkspaceScope:      projectWorkspaceScope(id, project.Name),
		MessageScope:        scope,
		InitialApprovedPlan: ptrProjectAssistantApprovedPlan(projectAssistantInitialCreationPlan()),
		TurnProfile:         projectAssistantTurnProfileImplementation,
		TurnPolicy:          projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	req = attachProjectAssistantBuildRunForEngineTest(t, server, req, "initial-creation-plan")
	_, err := engine.StreamProjectAssistant(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	read, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil || read.Content != "initial build\n" {
		t.Fatalf("initial write = %#v, %v; want initial build", read, err)
	}
	item, err := messages.GetAssistantWorkItem(context.Background(), scope, req.AssistantRun.WorkItemID)
	if err != nil {
		t.Fatalf("GetAssistantWorkItem: %v", err)
	}
	if len(item.PlanGrant) != 0 {
		t.Fatalf("initial build persisted a cross-turn plan grant: %s", item.PlanGrant)
	}
}

func TestEinoAssistantEnginePlanApprovalAllowsScopedWriteOnResume(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	registry := server.projectAssistantToolRegistry()
	planTool, ok := registry.Get(projectToolRequestProjectPlanApproval)
	if !ok {
		t.Fatal("request_project_plan_approval tool missing")
	}
	writeTool, ok := registry.Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	chatModel := &planThenWriteEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{
				newProjectEinoAssistantServerTool(server, planTool, req, state),
				newProjectEinoAssistantServerTool(server, writeTool, req, state),
			}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	req := projectAssistantRunRequest{
		Identity:       identity{orgUUID: id.orgUUID, workspaceUUID: id.workspaceUUID, tenantPath: id.tenantPath, user: "alice"},
		ToolPort:       projectAssistantDirectToolPort{},
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	req = attachProjectAssistantBuildRunForEngineTest(t, server, req, "plan-approval-resume")

	_, err := engine.StreamProjectAssistant(context.Background(), req)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want plan permission required", err)
	}
	if permissionErr.ToolName != projectToolRequestProjectPlanApproval {
		t.Fatalf("permission tool = %q, want plan approval", permissionErr.ToolName)
	}
	if _, err := workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"}); err == nil {
		t.Fatal("write_file ran before plan approval")
	}
	run, err := messages.GetAssistantRun(context.Background(), req.MessageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	if checkpoint.ApprovedPlan != nil {
		t.Fatalf("checkpoint approved plan = %#v, want nil before approval", checkpoint.ApprovedPlan)
	}
	claimed := claimProjectAssistantRunForWorkItemCommitTest(t, server, messages, req.MessageScope, run, permissionErr.RequestID)
	req.AssistantRun = &claimed

	result, err := engine.ResumeProjectAssistant(
		context.Background(),
		req,
		projectAssistantResumeRequest{
			RequestID: permissionErr.RequestID,
			Decision:  string(projectAssistantPermissionAllow),
		},
		checkpoint,
	)
	if err != nil {
		t.Fatalf("ResumeProjectAssistant returned error: %v", err)
	}
	if result.Content != "workspace ready" {
		t.Fatalf("content = %q, want resumed model response", result.Content)
	}
	read, err := workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "approved plan write\n" {
		t.Fatalf("content = %q, want approved plan write", read.Content)
	}
}

func TestEinoAssistantEngineRejectsPendingApprovalAfterGrantRevisionChanges(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			t.Fatal("model factory called for stale checkpoint")
			return nil, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			t.Fatal("tool factory called for stale checkpoint")
			return nil, nil
		},
	}
	project := &aiv1alpha1.Project{}
	project.Name = scope.ProjectName
	state := projectAssistantCheckpointState{
		ApprovedPlanGrantRevision: "stale-revision",
		Eino: &projectAssistantEinoCheckpointState{
			CheckpointID: "run-stale",
			Checkpoint:   []byte(`{"checkpoint":"stale"}`),
			InterruptID:  "interrupt-stale",
		},
	}

	_, err := engine.ResumeProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Project:            project,
			MessageScope:       scope,
			AssistantRun:       &store.AssistantRun{ID: "run-stale", WorkItemID: "work-stale"},
			executionAuthority: &projectAssistantExplicitTestAuthority{},
		},
		projectAssistantResumeRequest{RequestID: "perm-stale", Decision: string(projectAssistantPermissionAllow)},
		state,
	)
	if !errors.Is(err, errProjectAssistantCheckpointGrantStale) {
		t.Fatalf("ResumeProjectAssistant error = %v, want stale grant revision", err)
	}
}

func TestEinoAssistantEngineAdaptivePromotionApprovalRestoresPlanProgressAndImplementationTools(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	registry := server.projectAssistantToolRegistry()
	planTool, ok := registry.Get(projectToolRequestProjectPlanApproval)
	if !ok {
		t.Fatal("request_project_plan_approval tool missing")
	}
	writeTool, ok := registry.Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1", user: "alice"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	scope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	started, err := server.startProjectAssistantAdaptiveRunDurably(
		ctx,
		scope,
		id.user,
		"I just tried to use the queue custom toast but it didnt work",
		"adaptive-engine-promotion-1",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	run := started.Run
	accumulator, err := server.projectAssistantSupervisor().Attach(scope, run, started.Assistant)
	if err != nil {
		t.Fatal(err)
	}
	model := &planThenWriteEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{
				newProjectEinoAssistantServerTool(server, planTool, req, state),
				newProjectEinoAssistantServerTool(server, writeTool, req, state),
			}, nil
		},
	}
	req := projectAssistantRunRequest{
		Identity: id, Project: project, Workspace: workspaces,
		WorkspaceScope: projectWorkspaceScope(id, project.Name), MessageScope: scope,
		ToolPort:     projectAssistantDirectToolPort{},
		TurnProfile:  projectAssistantTurnProfileAdaptive,
		TurnPolicy:   projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileAdaptive),
		AssistantRun: &run,
	}
	var emittedPlans []projectAssistantPlanSnapshot
	req.StreamCallbacks.OnPlan = func(plan projectAssistantPlanSnapshot) {
		emittedPlans = append(emittedPlans, plan)
	}

	_, err = engine.StreamProjectAssistant(ctx, req)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) || permissionErr.ToolName != projectToolRequestProjectPlanApproval {
		t.Fatalf("StreamProjectAssistant error = %v, want plan approval", err)
	}
	pending, err := messages.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.WorkItemID == "" || pending.Mode != store.AssistantRunModeNew {
		t.Fatalf("promoted pending run = %#v", pending)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(pending.Checkpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	claimed, err := accumulator.ClaimPending(ctx, permissionErr.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	req.AssistantRun = &claimed

	result, err := engine.ResumeProjectAssistant(ctx, req, projectAssistantResumeRequest{
		RequestID: permissionErr.RequestID,
		Decision:  string(projectAssistantPermissionAllow),
	}, checkpoint)
	if err != nil {
		t.Fatalf("ResumeProjectAssistant: %v", err)
	}
	if result.Content != "workspace ready" {
		t.Fatalf("resume content = %q, want completed implementation", result.Content)
	}
	if len(emittedPlans) != 1 {
		t.Fatalf("emitted plans = %#v, want one plan-progress snapshot", emittedPlans)
	}
	wantPlan := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Inspect the app", ActiveForm: "Inspecting the app", Status: "completed"},
		{Content: "Write the app entry", ActiveForm: "Writing the app entry", Status: "in_progress"},
	}}
	if !reflect.DeepEqual(emittedPlans[0], wantPlan) {
		t.Fatalf("emitted plan = %#v, want %#v", emittedPlans[0], wantPlan)
	}
	if _, err := workspaces.ReadFile(ctx, req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"}); err != nil {
		t.Fatalf("approved adaptive write did not run: %v", err)
	}
}

func TestEinoAssistantEngineAdaptiveAutoApprovePromotesToolsInline(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1", user: "alice"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	scope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if _, err := messages.SetAssistantApprovalPreference(ctx, scope, store.AssistantApprovalPreference{
		ActorID: id.user,
		Mode:    store.AssistantApprovalModeAutoApprove,
	}); err != nil {
		t.Fatal(err)
	}
	started, err := server.startProjectAssistantAdaptiveRunDurably(
		ctx,
		scope,
		id.user,
		"Make the default location Chicago",
		"adaptive-auto-approve-1",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if started.Run.ApprovalMode != store.AssistantApprovalModeAutoApprove {
		t.Fatalf("approval mode = %q, want auto approve", started.Run.ApprovalMode)
	}
	run := started.Run
	_, err = server.projectAssistantSupervisor().Attach(scope, run, started.Assistant)
	if err != nil {
		t.Fatal(err)
	}
	model := &adaptiveAutoApprovePlanThenWriteEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: newProjectEinoAssistantToolsFactoryWithVerificationResultForTest(
			server,
			`{"status":"ready"}`,
		),
	}
	var toolEvents []projectToolCallStreamEvent
	var assistantEvents []projectAssistantEvent
	req := projectAssistantRunRequest{
		Identity: id, Project: project, Workspace: workspaces,
		WorkspaceScope: projectWorkspaceScope(id, project.Name), MessageScope: scope,
		ToolPort:     projectAssistantDirectToolPort{},
		TurnProfile:  projectAssistantTurnProfileAdaptive,
		TurnPolicy:   projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileAdaptive),
		ApprovalMode: run.ApprovalMode, AssistantRun: &run,
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnToolCall:       func(event projectToolCallStreamEvent) { toolEvents = append(toolEvents, event) },
			OnAssistantEvent: func(event projectAssistantEvent) { assistantEvents = append(assistantEvents, event) },
		},
	}

	result, err := engine.StreamProjectAssistant(ctx, req)
	if err != nil {
		t.Fatalf("StreamProjectAssistant: %v", err)
	}
	if result.Content != "workspace ready" {
		t.Fatalf("content = %q, want completed implementation", result.Content)
	}
	if len(model.toolNames) != 5 {
		t.Fatalf("model tool snapshots = %#v, want plan, two writes, implicit verification, and report calls", model.toolNames)
	}
	for _, hidden := range []string{
		projectToolWriteFile,
		projectToolApplyPatch,
		projectToolMkdir,
		projectEinoAssistantWriteTodosTool,
		projectToolCommitProjectFiles,
		projectEinoAssistantToolSearchTool,
	} {
		if stringSliceContains(model.toolNames[0], hidden) {
			t.Fatalf("initial adaptive tools = %#v, must hide %s", model.toolNames[0], hidden)
		}
	}
	if !stringSliceContains(model.toolNames[0], projectToolRequestProjectPlanApproval) {
		t.Fatalf("initial adaptive tools = %#v, want plan approval", model.toolNames[0])
	}
	if stringSliceContains(model.toolNames[1], projectToolRequestProjectPlanApproval) ||
		!stringSliceContains(model.toolNames[1], projectToolWriteFile) ||
		!stringSliceContains(model.toolNames[1], projectEinoAssistantWriteTodosTool) {
		t.Fatalf("post-approval tools = %#v, want mutation tools without plan approval", model.toolNames[1])
	}
	read, err := workspaces.ReadFile(ctx, req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if read.Content != "Chicago\n" {
		t.Fatalf("content = %q, want Chicago", read.Content)
	}
	if _, err := workspaces.ReadFile(ctx, req.WorkspaceScope, workspace.ReadOptions{Path: "src/location.ts"}); err != nil {
		t.Fatalf("second approved adaptive write did not run: %v", err)
	}
	if countProjectAssistantEvents(assistantEvents, projectAssistantEventPermissionNeeded) != 0 ||
		countProjectAssistantEvents(assistantEvents, projectAssistantEventCheckpointSaved) != 0 {
		t.Fatalf("assistant events = %#v, want inline approval without interrupt", assistantEvents)
	}
	for _, event := range toolEvents {
		if event.Name == projectToolWriteFile && event.Status == "rejected" {
			t.Fatalf("write event = %#v, want successful registered write", event)
		}
	}
	persisted, err := messages.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	audit := decodeProjectAssistantRunAudit(t, persisted.Audit)
	automaticPlanApproval := false
	for _, decision := range audit.Decisions {
		if decision.ToolName == projectToolRequestProjectPlanApproval &&
			decision.Decision == projectAssistantPermissionAllow &&
			decision.Source == "approval_mode" &&
			decision.ApprovalMode == store.AssistantApprovalModeAutoApprove {
			automaticPlanApproval = true
		}
	}
	if !automaticPlanApproval {
		t.Fatalf("audit decisions = %#v, want automatic plan approval", audit.Decisions)
	}
	for _, tool := range audit.Tools {
		if tool.Name == projectToolWriteFile && tool.Status == "rejected" {
			t.Fatalf("audit tools = %#v, write must not be rejected", audit.Tools)
		}
	}
}

func TestEinoAssistantEngineAdaptiveAutoApproveDefersBatchedWriteUntilNextIteration(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1", user: "alice"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	scope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	if _, err := messages.SetAssistantApprovalPreference(ctx, scope, store.AssistantApprovalPreference{
		ActorID: id.user,
		Mode:    store.AssistantApprovalModeAutoApprove,
	}); err != nil {
		t.Fatal(err)
	}
	started, err := server.startProjectAssistantAdaptiveRunDurably(
		ctx,
		scope,
		id.user,
		"Make the default location Chicago",
		"adaptive-auto-batched-write-1",
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	run := started.Run
	_, err = server.projectAssistantSupervisor().Attach(scope, run, started.Assistant)
	if err != nil {
		t.Fatal(err)
	}
	model := &adaptiveAutoApproveBatchedPlanWriteEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return model, nil
		},
		newTools: newProjectEinoAssistantToolsFactory(server),
	}
	req := projectAssistantRunRequest{
		Identity: id, Project: project, Workspace: workspaces,
		WorkspaceScope: projectWorkspaceScope(id, project.Name), MessageScope: scope,
		ToolPort:     projectAssistantDirectToolPort{},
		TurnProfile:  projectAssistantTurnProfileAdaptive,
		TurnPolicy:   projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileAdaptive),
		ApprovalMode: run.ApprovalMode, AssistantRun: &run,
	}

	result, err := engine.StreamProjectAssistant(ctx, req)
	if err != nil {
		t.Fatalf("StreamProjectAssistant: %v", err)
	}
	if result.Content != "workspace ready" {
		t.Fatalf("content = %q, want completed implementation", result.Content)
	}
	if len(model.inputs) < 2 ||
		!einoMessagesContainToolResult(model.inputs[1], "call-write-early", "unavailable in the current assistant phase") {
		t.Fatalf("second model input = %#v, want approval-phase write denial", model.inputs)
	}
	for callID, name := range map[string]string{
		"call-restart-early":  projectToolRestartRuntime,
		"call-template-early": projectToolSelectTemplate,
	} {
		if !einoMessagesContainToolResult(model.inputs[1], callID, "unavailable in the current assistant phase") {
			t.Fatalf("second model input = %#v, want approval-phase %s denial", model.inputs[1], name)
		}
	}
	read, err := workspaces.ReadFile(ctx, req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if read.Content != "retry Chicago\n" {
		t.Fatalf("content = %q, want only the next-iteration write", read.Content)
	}
}

func TestEinoAssistantEngineAdaptiveAutoApproveFailsClosedBeforeMutation(t *testing.T) {
	tests := []struct {
		name      string
		wrapStore func(store.Store) store.Store
	}{
		{
			name: "durable promotion failure",
			wrapStore: func(inner store.Store) store.Store {
				return failAdaptiveAutoApprovePromotionStore{Store: inner}
			},
		},
		{
			name: "plan persistence failure",
			wrapStore: func(inner store.Store) store.Store {
				return failAdaptiveAutoApprovePlanPersistenceStore{Store: inner}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			messages := tt.wrapStore(store.NewMemoryStore())
			workspaces := workspace.NewFileStore(t.TempDir())
			server := NewWithWorkspace(nil, messages, workspaces, "", false)
			id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1", user: "alice"}
			project := &aiv1alpha1.Project{}
			project.Name = "demo"
			project.UID = "test-project-uid-demo"
			scope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
			if _, err := messages.SetAssistantApprovalPreference(ctx, scope, store.AssistantApprovalPreference{
				ActorID: id.user,
				Mode:    store.AssistantApprovalModeAutoApprove,
			}); err != nil {
				t.Fatal(err)
			}
			started, err := server.startProjectAssistantAdaptiveRunDurably(
				ctx,
				scope,
				id.user,
				"Make the default location Chicago",
				"adaptive-auto-fail-closed-"+strings.ReplaceAll(tt.name, " ", "-"),
				func(store.AssistantRun, store.Message, bool) error { return nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			run := started.Run
			_, err = server.projectAssistantSupervisor().Attach(scope, run, started.Assistant)
			if err != nil {
				t.Fatal(err)
			}
			model := &adaptiveAutoApprovePlanThenWriteEinoChatModel{}
			engine := projectEinoAssistantEngine{
				server: server,
				newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
					return model, nil
				},
				newTools: newProjectEinoAssistantToolsFactory(server),
			}
			req := projectAssistantRunRequest{
				Identity: id, Project: project, Workspace: workspaces,
				WorkspaceScope: projectWorkspaceScope(id, project.Name), MessageScope: scope,
				ToolPort:     projectAssistantDirectToolPort{},
				TurnProfile:  projectAssistantTurnProfileAdaptive,
				TurnPolicy:   projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileAdaptive),
				ApprovalMode: run.ApprovalMode, AssistantRun: &run,
			}

			_, _ = engine.StreamProjectAssistant(ctx, req)
			if _, err := workspaces.ReadFile(ctx, req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"}); err == nil {
				t.Fatal("workspace mutation succeeded after failed durable transition")
			}
			for _, names := range model.toolNames {
				if stringSliceContains(names, projectToolWriteFile) {
					t.Fatalf("tools after failed durable transition = %#v, want read-only catalog", names)
				}
			}
		})
	}
}

type failAdaptiveAutoApprovePromotionStore struct {
	store.Store
}

func (failAdaptiveAutoApprovePromotionStore) PromoteAssistantRunToWorkItem(
	context.Context,
	store.Scope,
	string,
	string,
	string,
	int64,
	time.Time,
) (store.AssistantWorkItem, store.AssistantRun, error) {
	return store.AssistantWorkItem{}, store.AssistantRun{}, errors.New("injected adaptive promotion failure")
}

type failAdaptiveAutoApprovePlanPersistenceStore struct {
	store.Store
}

func (failAdaptiveAutoApprovePlanPersistenceStore) ApproveWorkItemPlan(
	context.Context,
	store.Scope,
	string,
	string,
	int64,
	string,
	json.RawMessage,
	time.Time,
) (store.AssistantWorkItem, error) {
	return store.AssistantWorkItem{}, errors.New("injected adaptive plan persistence failure")
}

func TestEinoAssistantEngineCheckpointsDynamicJSONToolCallMetadata(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	writeTool, ok := server.projectAssistantToolRegistry().Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	chatModel := &multipleToolCallEinoChatModel{toolCalls: []schema.ToolCall{
		{
			ID:   "call-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"hello"}`,
			},
			Extra: map[string]any{
				"runtime": map[string]any{
					"name":   "node",
					"checks": []any{"build", "test"},
				},
			},
		},
	}}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, writeTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	req := attachProjectAssistantBuildRunForEngineTest(t, server, projectAssistantRunRequest{
		Identity:       identity{orgUUID: id.orgUUID, workspaceUUID: id.workspaceUUID, tenantPath: id.tenantPath, user: "alice"},
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
	}, "dynamic-tool-checkpoint")
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		req,
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want permission required", err)
	}
	run, err := messages.GetAssistantRun(context.Background(), req.MessageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	if checkpoint.Eino == nil || len(checkpoint.Eino.Checkpoint) == 0 {
		t.Fatalf("checkpoint eino state = %#v, want turn loop checkpoint", checkpoint.Eino)
	}
	turnCheckpoint := decodeProjectEinoTurnLoopCheckpointForTest(t, checkpoint.Eino.Checkpoint)
	if !turnCheckpoint.HasRunnerState || len(turnCheckpoint.CanceledItems) != 1 || turnCheckpoint.CanceledItems[0].Kind != projectAssistantTurnMessage {
		t.Fatalf("turn loop checkpoint = %#v, want interrupted message turn with runner state", turnCheckpoint)
	}
}

func TestEinoAssistantEngineResumesApprovedToolThroughTurnLoop(t *testing.T) {
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	writeTool, ok := server.projectAssistantToolRegistry().Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	chatModel := &resumePermissionEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, writeTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	req := projectAssistantRunRequest{
		Identity:       identity{orgUUID: id.orgUUID, workspaceUUID: id.workspaceUUID, tenantPath: id.tenantPath, user: "alice"},
		ToolPort:       projectAssistantDirectToolPort{},
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
	}
	req = attachProjectAssistantBuildRunForEngineTest(t, server, req, "approved-tool-resume")
	_, err := engine.StreamProjectAssistant(context.Background(), req)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want permission required", err)
	}
	run, err := messages.GetAssistantRun(context.Background(), req.MessageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	claimed := claimProjectAssistantRunForWorkItemCommitTest(t, server, messages, req.MessageScope, run, permissionErr.RequestID)
	req.AssistantRun = &claimed

	result, err := engine.ResumeProjectAssistant(
		context.Background(),
		req,
		projectAssistantResumeRequest{
			RequestID: permissionErr.RequestID,
			Decision:  string(projectAssistantPermissionAllow),
		},
		checkpoint,
	)
	if err != nil {
		t.Fatalf("ResumeProjectAssistant returned error: %v", err)
	}
	if result.Content != "write completed" {
		t.Fatalf("content = %q, want resumed model response", result.Content)
	}
	read, err := workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "approved\n" {
		t.Fatalf("content = %q, want approved write", read.Content)
	}
	if len(chatModel.inputs) != 2 || !einoMessagesContainToolResult(chatModel.inputs[1], "call-write", "src/App.tsx") {
		t.Fatalf("model inputs = %#v, want resumed Eino tool result", chatModel.inputs)
	}
}

func TestEinoAssistantEngineReturnsUnknownToolResultToModel(t *testing.T) {
	chatModel := &unknownToolEinoChatModel{}
	projectTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        "inspect_workspace",
			Description: "Inspect the workspace.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		result: `{"ok":true}`,
	}
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantTool(projectTool, req, state)}, nil
		},
	}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	var toolEvents []projectToolCallStreamEvent
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Identity: identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
			Project:  project,
			StreamCallbacks: projectAssistantStreamCallbacks{
				OnToolCall: func(event projectToolCallStreamEvent) {
					toolEvents = append(toolEvents, event)
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "recovered from unknown tool" {
		t.Fatalf("content = %q, want recovery after unknown tool result", result.Content)
	}
	if !einoMessagesContainToolResult(chatModel.inputs[1], "call-unknown", "disallowed tool name") {
		t.Fatalf("second model input = %#v, want unknown-tool result", chatModel.inputs[1])
	}
	if projectToolEventsWithStatus(toolEvents, "rejected") != 1 {
		t.Fatalf("tool events = %#v, want one rejected unknown tool event", toolEvents)
	}
	if projectTool.calls != 0 {
		t.Fatalf("registered tool calls = %d, want unknown tool handler only", projectTool.calls)
	}
}

func TestServerRebuildsDefaultEinoAssistantEngine(t *testing.T) {
	server := &Server{}
	if _, ok := server.projectAssistantEngine().(projectEinoAssistantEngine); !ok {
		t.Fatalf("engine = %T, want projectEinoAssistantEngine", server.projectAssistantEngine())
	}
}

func TestNewServerDefaultsToEinoAssistantEngine(t *testing.T) {
	server := NewWithWorkspace(nil, nil, nil, "", false)
	if _, ok := server.projectAssistantEngine().(projectEinoAssistantEngine); !ok {
		t.Fatalf("engine = %T, want projectEinoAssistantEngine", server.projectAssistantEngine())
	}
}

type scriptedEinoChatModel struct {
	inputs    [][]*schema.Message
	toolNames [][]string
}

type retryingEinoChatModel struct {
	calls        int
	streamErrors []error
	content      string
}

type partialStreamFailureEinoChatModel struct {
	calls int
}

func newRetryingProjectEinoAssistantEngine(chatModel einomodel.BaseChatModel) projectEinoAssistantEngine {
	return projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}
}

func newProjectEinoAssistantToolsFactoryWithVerificationResultForTest(
	server *Server,
	result string,
) projectEinoAssistantToolsFactory {
	baseTools := newProjectEinoAssistantToolsFactory(server)
	verifyTool := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectToolVerifyDevelopmentRuntime,
			Description: "Verify the development runtime.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Risk:        projectAssistantToolRiskRead,
		},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			return result, nil
		},
	}
	return func(ctx context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
		tools, err := baseTools(ctx, req, state)
		if err != nil {
			return nil, err
		}
		filtered := make([]einotool.BaseTool, 0, len(tools))
		for _, tool := range tools {
			info, err := tool.Info(ctx)
			if err != nil {
				return nil, err
			}
			if projectToolBaseName(info.Name) != projectToolVerifyDevelopmentRuntime {
				filtered = append(filtered, tool)
			}
		}
		return append(filtered, newProjectEinoAssistantServerTool(server, verifyTool, req, state)), nil
	}
}

func (m *retryingEinoChatModel) Generate(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return schema.AssistantMessage(m.content, nil), nil
}

func (m *retryingEinoChatModel) Stream(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.calls++
	if m.calls <= len(m.streamErrors) {
		return nil, m.streamErrors[m.calls-1]
	}
	return schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage(m.content, nil),
	}), nil
}

func (m *partialStreamFailureEinoChatModel) Generate(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, io.ErrUnexpectedEOF
}

func (m *partialStreamFailureEinoChatModel) Stream(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.calls++
	stream, writer := schema.Pipe[*schema.Message](2)
	_ = writer.Send(schema.AssistantMessage("rejected partial response", nil), nil)
	_ = writer.Send(nil, io.ErrUnexpectedEOF)
	writer.Close()
	return stream, nil
}

type capturingEinoChatModel struct {
	inputs          [][]*schema.Message
	sessionSnapshot *projectEinoAssistantSessionSnapshot
	content         string
}

type toolCapturingEinoChatModel struct {
	content   string
	toolNames [][]string
	contents  []string
}

type canonicalFilesystemReadEinoChatModel struct {
	inputs          [][]*schema.Message
	toolInfos       [][]*schema.ToolInfo
	requestPlan     bool
	requestFollowUp bool
	directAnswer    bool
	readPath        string
	completion      string
}

type planThenReportToolCapturingEinoChatModel struct {
	toolNames [][]string
}

type writeVerifyThenReportEinoChatModel struct {
	inputs       [][]*schema.Message
	toolNames    [][]string
	writeContent string
}

type hiddenWriteTodosEinoChatModel struct {
	inputs       [][]*schema.Message
	todosWritten bool
}

type hiddenRepeatedPlanEinoChatModel struct {
	inputs [][]*schema.Message
}

type emptyOutputEinoChatModel struct{}

func (emptyOutputEinoChatModel) Generate(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return schema.AssistantMessage("", nil), nil
}

func (m emptyOutputEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type multiContentOutputEinoChatModel struct{}

func (multiContentOutputEinoChatModel) Generate(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &schema.Message{
		Role: schema.Assistant,
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "multi content answer"},
		},
	}, nil
}

func (m multiContentOutputEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type projectEinoTurnLoopCheckpointForTest struct {
	RunnerCheckpoint []byte
	HasRunnerState   bool
	UnhandledItems   []projectAssistantTurnItem
	CanceledItems    []projectAssistantTurnItem
}

func decodeProjectEinoTurnLoopCheckpointForTest(t *testing.T, checkpoint []byte) projectEinoTurnLoopCheckpointForTest {
	t.Helper()
	var decoded projectEinoTurnLoopCheckpointForTest
	if err := gob.NewDecoder(bytes.NewReader(checkpoint)).Decode(&decoded); err != nil {
		t.Fatalf("decode turn loop checkpoint returned error: %v", err)
	}
	return decoded
}

type multipleToolCallEinoChatModel struct {
	inputs    [][]*schema.Message
	toolCalls []schema.ToolCall
}

type dynamicWritePermissionEinoChatModel struct {
	inputs    [][]*schema.Message
	toolNames [][]string
}

type deployRuntimeEinoChatModel struct {
	inputs    [][]*schema.Message
	toolNames [][]string
}

type summarizingEinoChatModel struct {
	inputs       [][]*schema.Message
	summaryCalls int
}

type blankSummaryEinoChatModel struct {
	inputs       [][]*schema.Message
	summaryCalls int
}

func (m *summarizingEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if einoMessagesContainContent(input, projectEinoAssistantSummaryInstruction) {
		m.summaryCalls++
		return schema.AssistantMessage("summary: production dashboard requirements retained", nil), nil
	}
	return schema.AssistantMessage("continued with summarized context", nil), nil
}

func (m *blankSummaryEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if einoMessagesContainContent(input, projectEinoAssistantSummaryInstruction) {
		m.summaryCalls++
		return schema.AssistantMessage("", nil), nil
	}
	return schema.AssistantMessage("continued after blank summary", nil), nil
}

func (m *summarizingEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *blankSummaryEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *multipleToolCallEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if len(m.inputs) == 1 {
		return schema.AssistantMessage("", m.toolCalls), nil
	}
	return schema.AssistantMessage("unexpected continuation", nil), nil
}

func (m *multipleToolCallEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *dynamicWritePermissionEinoChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	common := einomodel.GetCommonOptions(nil, opts...)
	names := make([]string, 0, len(common.Tools))
	for _, tool := range common.Tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	m.toolNames = append(m.toolNames, names)
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	switch len(m.inputs) {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"dynamic write\n"}`,
			},
		}}), nil
	default:
		return schema.AssistantMessage("dynamic write completed", nil), nil
	}
}

func (m *dynamicWritePermissionEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *deployRuntimeEinoChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	common := einomodel.GetCommonOptions(nil, opts...)
	names := make([]string, 0, len(common.Tools))
	for _, tool := range common.Tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	m.toolNames = append(m.toolNames, names)
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if len(m.inputs) == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-deploy-runtime",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolRestartRuntime,
				Arguments: `{"targetRef":"runtime-1","image":"example.com/demo:latest","port":3000,"intent":"preview"}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("runtime deployed", nil), nil
}

func (m *deployRuntimeEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type planThenWriteEinoChatModel struct {
	inputs [][]*schema.Message
}

type adaptiveAutoApprovePlanThenWriteEinoChatModel struct {
	toolNames [][]string
	calls     int
}

type adaptiveAutoApproveBatchedPlanWriteEinoChatModel struct {
	inputs [][]*schema.Message
}

func (m *adaptiveAutoApproveBatchedPlanWriteEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	switch len(m.inputs) {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{
			{
				ID:   "call-plan",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolRequestProjectPlanApproval,
					Arguments: `{"summary":"Change the default location","steps":["Inspect the current default","Update the application default"],"targetPaths":["src/"],"acceptanceCriteria":["the default location is Chicago"]}`,
				},
			},
			{
				ID:   "call-write-early",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolWriteFile,
					Arguments: `{"path":"src/App.tsx","content":"early Chicago\n"}`,
				},
			},
			{
				ID:   "call-restart-early",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolRestartRuntime,
					Arguments: `{}`,
				},
			},
			{
				ID:   "call-template-early",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolSelectTemplate,
					Arguments: `{"name":"simple-webapp"}`,
				},
			},
		}), nil
	case 2:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-write-retry",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"retry Chicago\n"}`,
			},
		}}), nil
	default:
		return schema.AssistantMessage("workspace ready", nil), nil
	}
}

func (m *adaptiveAutoApproveBatchedPlanWriteEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *adaptiveAutoApprovePlanThenWriteEinoChatModel) Generate(ctx context.Context, _ []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	common := einomodel.GetCommonOptions(nil, opts...)
	names := make([]string, 0, len(common.Tools))
	for _, tool := range common.Tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	m.toolNames = append(m.toolNames, names)
	m.calls++
	switch m.calls {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-plan",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolRequestProjectPlanApproval,
				Arguments: `{"summary":"Change the default location","steps":["Inspect the current default","Update the application default"],"targetPaths":["src/"],"acceptanceCriteria":["the default location is Chicago"]}`,
			},
		}}), nil
	case 2:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"Chicago\n"}`,
			},
		}}), nil
	case 3:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-write-location",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/location.ts","content":"export const location = 'Chicago'\\n"}`,
			},
		}}), nil
	default:
		return schema.AssistantMessage("workspace ready", nil), nil
	}
}

func (m *adaptiveAutoApprovePlanThenWriteEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *planThenWriteEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	switch len(m.inputs) {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-plan",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolRequestProjectPlanApproval,
				Arguments: `{"summary":"Build app shell","steps":["Inspect the app","Write the app entry"],"targetPaths":["src/"],"acceptanceCriteria":["src/App.tsx exists"]}`,
			},
		}}), nil
	case 2:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-todos",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectEinoAssistantWriteTodosTool,
				Arguments: `{"todos":[{"content":"Inspect the app","activeForm":"Inspecting the app","status":"completed"},{"content":"Write the app entry","activeForm":"Writing the app entry","status":"in_progress"}]}`,
			},
		}}), nil
	case 3:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"approved plan write\n"}`,
			},
		}}), nil
	default:
		return schema.AssistantMessage("workspace ready", nil), nil
	}
}

func (m *planThenWriteEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type planWriteCommitWriteEinoChatModel struct {
	inputs [][]*schema.Message
}

func (m *planWriteCommitWriteEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	switch len(m.inputs) {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-plan",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolRequestProjectPlanApproval,
				Arguments: `{"summary":"Build app shell","steps":["Write the app entry"],"targetPaths":["src/"],"acceptanceCriteria":["src/App.tsx exists"]}`,
			},
		}}), nil
	case 2:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"approved plan write\n"}`,
			},
		}}), nil
	case 3:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-verify",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolVerifyDevelopmentRuntime,
				Arguments: `{}`,
			},
		}}), nil
	case 4:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-commit",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolCommitProjectFiles,
				Arguments: `{"repositoryRef":"repo-1","paths":["src/App.tsx"],"message":"Initial app"}`,
			},
		}}), nil
	case 5:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-post-commit-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"post commit write\n"}`,
			},
		}}), nil
	default:
		return schema.AssistantMessage("workspace ready", nil), nil
	}
}

func (m *planWriteCommitWriteEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type unknownToolEinoChatModel struct {
	inputs [][]*schema.Message
}

func (m *unknownToolEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if len(m.inputs) == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-unknown",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "code__commit_files",
				Arguments: `{"paths":["src/App.tsx"]}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("recovered from unknown tool", nil), nil
}

func (m *unknownToolEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type resumePermissionEinoChatModel struct {
	inputs [][]*schema.Message
}

func (m *resumePermissionEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if len(m.inputs) == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"approved\n"}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("write completed", nil), nil
}

func (m *resumePermissionEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type followUpEinoChatModel struct {
	inputs           [][]*schema.Message
	sessionSnapshots []*projectEinoAssistantSessionSnapshot
}

func (m *followUpEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	var sessionSnapshot *projectEinoAssistantSessionSnapshot
	if raw, ok := adk.GetSessionValue(ctx, projectEinoAssistantSessionSnapshotKey); ok {
		if snapshot, ok := raw.(projectEinoAssistantSessionSnapshot); ok {
			sessionSnapshot = &snapshot
		}
	}
	m.sessionSnapshots = append(m.sessionSnapshots, sessionSnapshot)
	if len(m.inputs) == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-follow-up",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolAskFollowUp,
				Arguments: `{"questions":["What kind of app should I build?"]}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("thanks, I can build that", nil), nil
}

func (m *followUpEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *scriptedEinoChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	common := einomodel.GetCommonOptions(nil, opts...)
	names := make([]string, 0, len(common.Tools))
	for _, tool := range common.Tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	m.toolNames = append(m.toolNames, names)
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	switch len(m.inputs) {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-inspect",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "inspect_workspace",
				Arguments: `{"path":"src/App.tsx"}`,
			},
		}}), nil
	case 2:
		return schema.AssistantMessage("done after tool", nil), nil
	default:
		return schema.AssistantMessage("unexpected extra tool round", nil), nil
	}
}

func (m *scriptedEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *capturingEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if raw, ok := adk.GetSessionValue(ctx, projectEinoAssistantSessionSnapshotKey); ok {
		if snapshot, ok := raw.(projectEinoAssistantSessionSnapshot); ok {
			m.sessionSnapshot = &snapshot
		}
	}
	return schema.AssistantMessage(m.content, nil), nil
}

func (m *capturingEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *toolCapturingEinoChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	common := einomodel.GetCommonOptions(nil, opts...)
	names := make([]string, 0, len(common.Tools))
	for _, tool := range common.Tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	m.toolNames = append(m.toolNames, names)
	for _, msg := range input {
		if msg != nil {
			m.contents = append(m.contents, msg.Content)
		}
	}
	return schema.AssistantMessage(m.content, nil), nil
}

func (m *toolCapturingEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *canonicalFilesystemReadEinoChatModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	common := einomodel.GetCommonOptions(nil, opts...)
	infos := make([]*schema.ToolInfo, 0, len(common.Tools))
	for _, info := range common.Tools {
		if info == nil {
			continue
		}
		clone := *info
		if info.Extra != nil {
			clone.Extra = make(map[string]any, len(info.Extra))
			for key, value := range info.Extra {
				clone.Extra[key] = value
			}
		}
		infos = append(infos, &clone)
	}
	m.toolInfos = append(m.toolInfos, infos)
	if m.directAnswer {
		return schema.AssistantMessage("direct answer", nil), nil
	}
	if m.requestPlan {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-plan",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolRequestProjectPlanApproval,
				Arguments: `{"summary":"Update the project","steps":["Inspect","Edit"],"targetPaths":["src/"],"acceptanceCriteria":["Project updated"]}`,
			},
		}}), nil
	}
	if m.requestFollowUp {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-follow-up",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolAskFollowUp,
				Arguments: `{"questions":["Continue?"]}`,
			},
		}}), nil
	}
	if len(m.inputs) == 1 {
		readPath := strings.TrimSpace(m.readPath)
		if readPath == "" {
			readPath = "README.md"
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-read-readme",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadFile,
				Arguments: fmt.Sprintf(`{"file_path":%q,"offset":1,"limit":20}`, readPath),
			},
		}}), nil
	}
	completion := strings.TrimSpace(m.completion)
	if completion == "" {
		completion = "README inspected"
	}
	return schema.AssistantMessage(completion, nil), nil
}

func (m *canonicalFilesystemReadEinoChatModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *planThenReportToolCapturingEinoChatModel) Generate(ctx context.Context, _ []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.toolNames = append(m.toolNames, projectEinoAssistantToolNamesFromOptions(opts...))
	if len(m.toolNames) == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-plan",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolRequestProjectPlanApproval,
				Arguments: `{"summary":"Build app shell","steps":["Inspect the app","Write the app entry"],"targetPaths":["src/"],"acceptanceCriteria":["src/App.tsx exists"]}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("approved plan is ready to implement", nil), nil
}

func (m *planThenReportToolCapturingEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *writeVerifyThenReportEinoChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	m.toolNames = append(m.toolNames, projectEinoAssistantToolNamesFromOptions(opts...))
	switch len(m.toolNames) {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: fmt.Sprintf(`{"path":"src/App.tsx","content":%q}`, m.writeContent),
			},
		}}), nil
	case 2:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-verify",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolVerifyDevelopmentRuntime,
				Arguments: `{}`,
			},
		}}), nil
	case 3:
		if stringSliceContains(m.toolNames[2], projectToolCommitProjectFiles) {
			return schema.AssistantMessage("", []schema.ToolCall{{
				ID:   "call-commit",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      projectToolCommitProjectFiles,
					Arguments: `{"repositoryRef":"demo-repo","paths":["src/App.tsx"],"message":"Verify application change"}`,
				},
			}}), nil
		}
		return schema.AssistantMessage("verification succeeded", nil), nil
	default:
		return schema.AssistantMessage("verification succeeded", nil), nil
	}
}

func (m *writeVerifyThenReportEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *hiddenWriteTodosEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if len(m.inputs) == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-hidden-todos",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectEinoAssistantWriteTodosTool,
				Arguments: `{"todos":[{"content":"unauthorized","activeForm":"Unauthorized","status":"pending"}]}`,
			},
		}}), nil
	}
	_, m.todosWritten = adk.GetSessionValue(ctx, deep.SessionKeyTodos)
	return schema.AssistantMessage("reported hidden todo rejection", nil), nil
}

func (m *hiddenWriteTodosEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *hiddenRepeatedPlanEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if len(m.inputs) == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-hidden-plan",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolRequestProjectPlanApproval,
				Arguments: `{"summary":"Widen hidden grant","steps":["edit secrets"],"targetPaths":["secrets/"],"acceptanceCriteria":["secrets changed"]}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("reported hidden plan denial", nil), nil
}

func (m *hiddenRepeatedPlanEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func projectEinoAssistantToolNamesFromOptions(opts ...einomodel.Option) []string {
	common := einomodel.GetCommonOptions(nil, opts...)
	names := make([]string, 0, len(common.Tools))
	for _, tool := range common.Tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	return names
}

func einoToolNamesForTest(t *testing.T, tools []einotool.BaseTool) []string {
	t.Helper()
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatalf("tool Info returned error: %v", err)
		}
		names = append(names, info.Name)
	}
	return names
}

type recordingProjectAssistantTool struct {
	spec        projectAssistantToolSpec
	result      string
	calls       int
	lastRequest projectAssistantToolCallRequest
}

func (t *recordingProjectAssistantTool) Spec() projectAssistantToolSpec {
	return t.spec
}

func (t *recordingProjectAssistantTool) Call(_ context.Context, req projectAssistantToolCallRequest) (string, error) {
	t.calls++
	t.lastRequest = req
	return t.result, nil
}

func cloneEinoMessagesForTest(src []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(src))
	for _, msg := range src {
		if msg == nil {
			continue
		}
		clone := *msg
		clone.ToolCalls = append([]schema.ToolCall(nil), msg.ToolCalls...)
		out = append(out, &clone)
	}
	return out
}

func einoMessagesContainToolResult(messages []*schema.Message, toolCallID, text string) bool {
	for _, msg := range messages {
		if msg == nil || msg.Role != schema.Tool || msg.ToolCallID != toolCallID {
			continue
		}
		if strings.Contains(msg.Content, text) {
			return true
		}
	}
	return false
}

func einoMessagesContainContent(messages []*schema.Message, text string) bool {
	for _, msg := range messages {
		if msg != nil && strings.Contains(msg.Content, text) {
			return true
		}
	}
	return false
}

func einoMessagesContainToolArguments(messages []*schema.Message, text string) bool {
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		for _, call := range msg.ToolCalls {
			if strings.Contains(call.Function.Arguments, text) {
				return true
			}
		}
	}
	return false
}

func stringSliceEqual(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func projectEinoRunRequestForProfileTest(profile projectAssistantTurnProfile) projectAssistantRunRequest {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Spec.DisplayName = "Demo"
	return projectAssistantRunRequest{
		Identity:           identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1", user: "alice"},
		Project:            project,
		Repository:         &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady, Ready: true},
		WorkspaceScope:     workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"},
		MessageScope:       store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid-demo"},
		TurnProfile:        profile,
		ToolPort:           projectAssistantDirectToolPort{},
		executionAuthority: &projectAssistantExplicitTestAuthority{},
	}
}

func attachProjectAssistantDiscussionRunForEngineTest(t *testing.T, server *Server, messages store.Store, req projectAssistantRunRequest, suffix string) projectAssistantRunRequest {
	t.Helper()
	req.Identity.user = "alice"
	now := time.Now().UTC()
	user := store.Message{ID: "user-" + suffix, Role: "user", ActorID: "alice", Content: "Continue", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: "assistant-" + suffix, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	run := store.AssistantRun{
		ID:              "run-" + suffix,
		Mode:            store.AssistantRunModeDiscussion,
		Status:          store.AssistantRunStatusRunning,
		ClientRequestID: "request-" + suffix,
		UserMessageID:   user.ID,
		ActiveMessageID: assistant.ID,
		Revision:        1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	created, err := messages.CreateAssistantRun(context.Background(), req.MessageScope, user, assistant, run)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	if _, err := server.projectAssistantSupervisor().Attach(req.MessageScope, created, assistant); err != nil {
		t.Fatalf("Attach assistant supervisor: %v", err)
	}
	req.AssistantRun = &created
	return req
}

func attachProjectAssistantBuildRunForEngineTest(t *testing.T, server *Server, req projectAssistantRunRequest, suffix string) projectAssistantRunRequest {
	t.Helper()
	req.Identity.user = "alice"
	started, err := server.startProjectAssistantBuildRunDurably(
		context.Background(),
		req.MessageScope,
		req.Identity.user,
		"Implement the request",
		"request-"+suffix,
		func(store.AssistantRun, store.Message, bool) error { return nil },
	)
	if err != nil {
		t.Fatalf("start durable build run: %v", err)
	}
	if _, err := server.projectAssistantSupervisor().Attach(req.MessageScope, started.Run, started.Assistant); err != nil {
		t.Fatalf("attach durable build run: %v", err)
	}
	req.AssistantRun = &started.Run
	req.executionAuthority = nil
	if req.ToolPort == nil {
		req.ToolPort = projectAssistantDirectToolPort{}
	}
	return req
}

func projectEinoToolsForProfileTest(t *testing.T, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
	t.Helper()
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	policy := normalizeProjectAssistantTurnPolicy(req.TurnPolicy, req.TurnProfile)
	req.TurnPolicy = policy
	state.SetToolDiscovery(projectEinoAssistantToolDiscovery{IncludeCommitBridge: true})
	return newProjectEinoAssistantToolsFactory(server)(context.Background(), req, state)
}

type countingAssistantRunStore struct {
	*store.MemoryStore
	saveAssistantRunCount int
	lastAssistantRun      *store.AssistantRun
}

func (s *countingAssistantRunStore) SaveAssistantRun(ctx context.Context, scope store.Scope, run store.AssistantRun) error {
	s.saveAssistantRunCount++
	copy := run
	copy.Audit = append([]byte(nil), run.Audit...)
	copy.Checkpoint = append([]byte(nil), run.Checkpoint...)
	s.lastAssistantRun = &copy
	return s.MemoryStore.SaveAssistantRun(ctx, scope, run)
}

func (s *countingAssistantRunStore) SaveAssistantRunSnapshot(ctx context.Context, scope store.Scope, run store.AssistantRun, messages []store.Message, expectedRevision int64) error {
	s.saveAssistantRunCount++
	copy := run
	copy.Audit = append([]byte(nil), run.Audit...)
	copy.Checkpoint = append([]byte(nil), run.Checkpoint...)
	s.lastAssistantRun = &copy
	return s.MemoryStore.SaveAssistantRunSnapshot(ctx, scope, run, messages, expectedRevision)
}

func countProjectAssistantEvents(events []projectAssistantEvent, eventType projectAssistantEventType) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func projectToolEventsWithStatus(events []projectToolCallStreamEvent, status string) int {
	count := 0
	for _, event := range events {
		if event.Status == status {
			count++
		}
	}
	return count
}
