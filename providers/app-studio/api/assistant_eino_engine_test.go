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
		if msg != nil && msg.Role == schema.System {
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
		"one mutation per path":         "never emit more than one mutation tool call for the same path",
		"inspect after failed edit":     "After an edit or patch fails, inspect the current file content before retrying that path",
		"complete phase progression":    "advance through plan, mutate, verify, and commit",
		"report only when terminal":     "only produce the final user report in the terminal report phase",
		"batch independent reads":       "Batch independent workspace reads in one model response",
		"sequence dependent reads":      "Keep reads sequential when a later call depends on an earlier result",
		"batch distinct paths":          "Mutations for distinct paths may be batched",
		"inspect before existing edit":  "Before editing an existing file, inspect its current content",
		"one active todo":               "keep exactly one todo item in progress",
		"prefer direct edits":           "Prefer editing the project files that implement the requested feature over creating side-channel scripts or configuration",
		"brief milestone narration":     "Brief milestone or blocker prose is allowed",
		"no per-tool narration":         "do not narrate each tool call",
		"direct operational actions":    "use its exact operational tool and tool-level approval",
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(strings.ToLower(instruction), strings.ToLower(required)) {
				t.Errorf("system instruction missing %q", required)
			}
		})
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
		projectToolListProjectFiles,
		projectToolReadProjectFile,
		projectToolSearchProjectFiles,
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
			Name:        projectToolReadProjectFile,
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
			wantCalls: 3,
			wantErr:   true,
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
			wantCalls: 3,
			wantErr:   true,
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
			chatModel := &toolCapturingEinoChatModel{content: "concise report"}
			engine := projectEinoAssistantEngine{
				newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
					return chatModel, nil
				},
				newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
					return []einotool.BaseTool{newProjectEinoAssistantTool(readTool, req, state)}, nil
				},
			}

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
	chatModel := &hiddenWriteTodosEinoChatModel{}
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}
	req := projectEinoRunRequestForProfileTest(projectAssistantTurnProfileImplementation)
	req.InitialApprovedPlan = &projectAssistantApprovedPlan{Steps: []string{"make the small change"}}

	if _, err := engine.StreamProjectAssistant(context.Background(), req); !errors.Is(err, adk.ErrExceedMaxRetries) {
		t.Fatalf("StreamProjectAssistant error = %v, want Eino retry exhaustion", err)
	}
	if chatModel.todosWritten {
		t.Fatal("hidden write_todos call updated the session outside a multi-step approved plan")
	}
	if len(chatModel.inputs) != 4 || !einoMessagesContainToolResult(chatModel.inputs[1], "call-hidden-todos", "Tool call denied") || !einoMessagesContainContent(chatModel.inputs[2], "mutate phase requires progress") {
		t.Fatalf("model inputs = %#v, want denied hidden write_todos result and bounded phase-progress retries", chatModel.inputs)
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
		Operations:         []string{projectToolWriteFile},
		AcceptanceCriteria: []string{"the application shell is updated"},
		ApprovalTool:       projectToolRequestProjectPlanApproval,
	})
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), req.MessageScope, &grant); err != nil {
		t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
	}

	if _, err := engine.StreamProjectAssistant(context.Background(), req); !errors.Is(err, adk.ErrExceedMaxRetries) {
		t.Fatalf("StreamProjectAssistant error = %v, want Eino retry exhaustion", err)
	}
	if len(chatModel.inputs) != 4 || !einoMessagesContainToolResult(chatModel.inputs[1], "call-hidden-plan", "Tool call denied") || !einoMessagesContainContent(chatModel.inputs[2], "mutate phase requires progress") {
		t.Fatalf("model inputs = %#v, want denied hidden repeated plan result and bounded phase-progress retries", chatModel.inputs)
	}
	if runState == nil {
		t.Fatal("assistant run state was not captured")
	}
	if got := runState.ApprovedPlan(); !reflect.DeepEqual(got, &grant) {
		t.Fatalf("in-memory grant = %#v, want unchanged %#v", got, &grant)
	}
	if got := server.loadProjectAssistantApprovedPlan(context.Background(), req.MessageScope); !reflect.DeepEqual(got, &grant) {
		t.Fatalf("persisted grant = %#v, want unchanged %#v", got, &grant)
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

	if _, err := engine.ResumeProjectAssistant(context.Background(), req, projectAssistantResumeRequest{
		RequestID: permissionErr.RequestID,
		Decision:  string(projectAssistantPermissionAllow),
	}, checkpoint); !errors.Is(err, adk.ErrExceedMaxRetries) {
		t.Fatalf("ResumeProjectAssistant error = %v, want Eino retry exhaustion", err)
	}
	if len(chatModel.toolNames) != 4 {
		t.Fatalf("model calls = %d, want plan plus bounded post-approval retries", len(chatModel.toolNames))
	}
	if !stringSliceContains(chatModel.toolNames[0], projectToolRequestProjectPlanApproval) {
		t.Fatalf("initial tools = %#v, want plan approval", chatModel.toolNames[0])
	}
	if stringSliceContains(chatModel.toolNames[1], projectToolRequestProjectPlanApproval) {
		t.Fatalf("post-approval tools = %#v, must not contain plan approval", chatModel.toolNames[1])
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
		{name: "commit", wantTools: []string{projectToolCommitProjectFiles}, wantCalls: 4},
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
			} else {
				grant := projectAssistantApprovedPlan{
					Steps:       []string{"write the change", "verify the preview"},
					TargetPaths: []string{"src/"},
					Operations:  []string{projectToolWriteFile},
				}
				if err := server.saveProjectAssistantApprovedPlan(context.Background(), req.MessageScope, &grant); err != nil {
					t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
				}
			}

			_, err := engine.StreamProjectAssistant(context.Background(), req)
			if tt.initialCreation {
				if err != nil {
					t.Fatalf("StreamProjectAssistant returned error: %v", err)
				}
			} else {
				var permissionErr *projectAssistantPermissionRequiredError
				if !errors.As(err, &permissionErr) || permissionErr.ToolName != projectToolCommitProjectFiles {
					t.Fatalf("StreamProjectAssistant error = %v, want commit permission request", err)
				}
				run, getErr := messages.GetAssistantRun(context.Background(), req.MessageScope, permissionErr.RunID)
				if getErr != nil {
					t.Fatalf("GetAssistantRun returned error: %v", getErr)
				}
				var checkpoint projectAssistantCheckpointState
				if unmarshalErr := json.Unmarshal(run.Checkpoint, &checkpoint); unmarshalErr != nil {
					t.Fatalf("decode checkpoint returned error: %v", unmarshalErr)
				}
				if _, resumeErr := engine.ResumeProjectAssistant(context.Background(), req, projectAssistantResumeRequest{
					RequestID: permissionErr.RequestID,
					Decision:  string(projectAssistantPermissionAllow),
				}, checkpoint); resumeErr != nil {
					t.Fatalf("ResumeProjectAssistant returned error: %v", resumeErr)
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
					message.Content == `{"operation":"write_file"}` {
					foundCompactedWrite = true
				}
			}
			if !foundCompactedWrite {
				t.Fatalf("checkpoint messages = %#v, want compact machine-readable write evidence", checkpointState.Messages)
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
			wantAllow:  []string{projectToolCheckProjectReadiness, projectToolReadProjectFile},
			wantReject: []string{projectToolGetRuntimeStatus, projectToolRestartRuntime, projectToolWriteFile, projectToolCommitProjectFiles},
		},
		{
			name:       "debugging",
			profile:    projectAssistantTurnProfileDebugging,
			wantAllow:  []string{projectToolCheckProjectReadiness, projectToolReadProjectFile, projectToolGetRuntimeStatus, projectToolGetPreviewURL},
			wantReject: []string{projectToolRestartRuntime, projectToolWriteFile, projectToolCommitProjectFiles},
		},
		{
			name:    "runtime-state exploration",
			profile: projectAssistantTurnProfileExploration,
			policy: projectAssistantTurnPolicy{
				profile:              projectAssistantTurnProfileExploration,
				requiresRuntimeState: true,
			},
			wantAllow:  []string{projectToolCheckProjectReadiness, projectToolReadProjectFile, projectToolGetRuntimeStatus, projectToolGetPreviewURL},
			wantReject: []string{projectToolRestartRuntime, projectToolWriteFile, projectToolCommitProjectFiles},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatModel := &toolCapturingEinoChatModel{content: "read-only answer"}
			var filteredNames []string
			engine := projectEinoAssistantEngine{
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

func TestEinoAssistantEngineAddsProjectSnapshotToInput(t *testing.T) {
	chatModel := &capturingEinoChatModel{content: "snapshot received"}
	workspaces := workspace.NewFileStore(t.TempDir())
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
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
	firstInput := chatModel.inputs[0]
	if !einoMessagesContainContent(firstInput, "Current project snapshot:") {
		t.Fatalf("input = %#v, want compact project snapshot system message", firstInput)
	}
	for _, want := range []string{
		`"repoReady":true`,
		`"lastKnownBranch"`,
		`"lastFileSnapshot":["package.json","src/App.tsx"]`,
		`"recommendedChecks":["build","test"]`,
	} {
		if !einoMessagesContainContent(firstInput, want) {
			t.Fatalf("input = %#v, want snapshot content %s", firstInput, want)
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

func TestEinoAssistantEngineFallsBackWhenTurnLoopHasNoAssistantOutput(t *testing.T) {
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
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if !strings.Contains(result.Content, "couldn't produce a response") || strings.Contains(result.Content, "eino") {
		t.Fatalf("result content = %q, want user-facing empty-output fallback", result.Content)
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
	project.Spec.Memory.Requirements = []string{"ship a tested build"}
	req := projectAssistantRunRequest{
		Identity:       id,
		Project:        project,
		Repository:     &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady, Ready: true},
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
	}
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
	if messages.saveAssistantRunCount != 2 {
		t.Fatalf("assistant run saves = %d, want running audit plus follow-up checkpoint", messages.saveAssistantRunCount)
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
			spec: projectAssistantToolSpec{Name: projectToolReadProjectFile, Risk: projectAssistantToolRiskRead},
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
	var assistantEvents []projectAssistantEvent
	var toolEvents []projectToolCallStreamEvent
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Identity:       id,
			Project:        project,
			WorkspaceScope: projectWorkspaceScope(id, project.Name),
			MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
			StreamCallbacks: projectAssistantStreamCallbacks{
				OnAssistantEvent: func(event projectAssistantEvent) {
					assistantEvents = append(assistantEvents, event)
				},
				OnToolCall: func(event projectToolCallStreamEvent) {
					toolEvents = append(toolEvents, event)
				},
			},
		},
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want permission required", err)
	}
	if messages.saveAssistantRunCount != 2 {
		t.Fatalf("assistant run saves = %d, want running audit plus permission checkpoint", messages.saveAssistantRunCount)
	}
	if countProjectAssistantEvents(assistantEvents, projectAssistantEventPermissionNeeded) != 1 || countProjectAssistantEvents(assistantEvents, projectAssistantEventCheckpointSaved) != 1 {
		t.Fatalf("assistant events = %#v, want one permission and one checkpoint", assistantEvents)
	}
	if projectToolEventsWithStatus(toolEvents, "permission_required") != 1 {
		t.Fatalf("tool events = %#v, want exactly one permission-required tool event", toolEvents)
	}
	run, err := messages.GetAssistantRun(context.Background(), projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name), permissionErr.RunID)
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
	var assistantEvents []projectAssistantEvent
	req := projectAssistantRunRequest{
		Identity:       id,
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnAssistantEvent: func(event projectAssistantEvent) {
				assistantEvents = append(assistantEvents, event)
			},
		},
	}
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
	writeCompletions := 0
	req := projectAssistantRunRequest{
		Identity:       id,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
		TurnProfile:    projectAssistantTurnProfileImplementation,
		StreamCallbacks: projectAssistantStreamCallbacks{OnToolCall: func(event projectToolCallStreamEvent) {
			if event.Name == projectToolWriteFile && event.Status == "succeeded" {
				writeCompletions++
			}
		}},
	}

	_, err := engine.StreamProjectAssistant(context.Background(), req)
	if !errors.Is(err, adk.ErrExceedMaxRetries) {
		t.Fatalf("StreamProjectAssistant error = %v, want Eino retry exhaustion after hidden write denial", err)
	}
	if len(chatModel.toolNames) == 0 {
		t.Fatal("model was not invoked")
	}
	if stringSliceContains(chatModel.toolNames[0], projectToolWriteFile) {
		t.Fatalf("initial tools = %#v, must not expose direct write_file before plan approval", chatModel.toolNames[0])
	}
	if stringSliceContains(chatModel.toolNames[0], "tool_search") {
		t.Fatalf("initial tools = %#v, want no tool_search without provider tools", chatModel.toolNames[0])
	}
	if len(chatModel.inputs) < 2 || !einoMessagesContainToolResult(chatModel.inputs[1], "call-write", "Tool call denied") {
		t.Fatalf("model inputs = %#v, want model-visible hidden-tool denial", chatModel.inputs)
	}
	if writeCompletions != 0 {
		t.Fatalf("write completions = %d, want hidden write never invoked", writeCompletions)
	}
	if _, err := workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"}); err == nil {
		t.Fatal("hidden write created src/App.tsx")
	}
}

func TestEinoAssistantEngineAutoApprovesWriteTools(t *testing.T) {
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
	var assistantEvents []projectAssistantEvent
	var toolEvents []projectToolCallStreamEvent
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Identity:           id,
			Project:            project,
			Workspace:          workspaces,
			WorkspaceScope:     projectWorkspaceScope(id, project.Name),
			MessageScope:       projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
			AutoApproveActions: true,
			TurnProfile:        projectAssistantTurnProfileImplementation,
			TurnPolicy:         projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
			InitialApprovedPlan: &projectAssistantApprovedPlan{
				Summary:     "Write the first source file only.",
				Steps:       []string{"write the first source file"},
				TargetPaths: []string{"src/one.tsx"},
				Operations:  []string{projectToolWriteFile},
			},
			StreamCallbacks: projectAssistantStreamCallbacks{
				OnAssistantEvent: func(event projectAssistantEvent) {
					assistantEvents = append(assistantEvents, event)
				},
				OnToolCall: func(event projectToolCallStreamEvent) {
					toolEvents = append(toolEvents, event)
				},
			},
		},
	)
	if !errors.Is(err, adk.ErrExceedMaxRetries) {
		t.Fatalf("StreamProjectAssistant error = %v, want retry exhaustion after the bounded write batch", err)
	}
	if result.Content != "" {
		t.Fatalf("content = %q, want no premature success report", result.Content)
	}
	if countProjectAssistantEvents(assistantEvents, projectAssistantEventPermissionNeeded) != 0 || countProjectAssistantEvents(assistantEvents, projectAssistantEventCheckpointSaved) != 0 {
		t.Fatalf("assistant events = %#v, want no permission events", assistantEvents)
	}
	if projectToolEventsWithStatus(toolEvents, "permission_required") != 0 {
		t.Fatalf("tool events = %#v, want no permission-required event", toolEvents)
	}
	if _, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: "src/one.tsx"}); err != nil {
		t.Fatalf("approved src/one.tsx write failed: %v", err)
	}
	if _, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: "src/two.tsx"}); err == nil {
		t.Fatal("out-of-plan src/two.tsx write unexpectedly succeeded")
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
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
	_, err := engine.StreamProjectAssistant(context.Background(), projectAssistantRunRequest{
		Identity:            id,
		Project:             project,
		Workspace:           workspaces,
		WorkspaceScope:      projectWorkspaceScope(id, project.Name),
		MessageScope:        scope,
		InitialApprovedPlan: ptrProjectAssistantApprovedPlan(projectAssistantInitialCreationPlan()),
	})
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	read, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil || read.Content != "initial build\n" {
		t.Fatalf("initial write = %#v, %v; want initial build", read, err)
	}
	if messages.saveAssistantRunCount != 2 {
		t.Fatalf("assistant run saves = %d, want running and completed audit rows", messages.saveAssistantRunCount)
	}
	if grant := server.loadProjectAssistantApprovedPlan(context.Background(), scope); grant != nil {
		t.Fatalf("persisted initial creation grant = %#v, want nil", grant)
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
	req := projectAssistantRunRequest{
		Identity:       id,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
	}

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
	if messages.saveAssistantRunCount != 2 {
		t.Fatalf("assistant run saves = %d, want running audit plus plan checkpoint only", messages.saveAssistantRunCount)
	}
}

func TestEinoAssistantEngineRejectsPendingApprovalAfterGrantRevisionChanges(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo"}
	if err := server.clearProjectAssistantApprovedPlan(context.Background(), scope); err != nil {
		t.Fatalf("clearProjectAssistantApprovedPlan returned error: %v", err)
	}
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
		ApprovedPlanGrantRevision: "",
		Eino: &projectAssistantEinoCheckpointState{
			CheckpointID: "run-stale",
			Checkpoint:   []byte(`{"checkpoint":"stale"}`),
			InterruptID:  "interrupt-stale",
		},
	}

	_, err := engine.ResumeProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{Project: project, MessageScope: scope},
		projectAssistantResumeRequest{RequestID: "perm-stale", Decision: string(projectAssistantPermissionAllow)},
		state,
	)
	if !errors.Is(err, errProjectAssistantCheckpointGrantStale) {
		t.Fatalf("ResumeProjectAssistant error = %v, want stale grant revision", err)
	}
}

func TestEinoAssistantEnginePersistedPlanGrantSkipsApprovalOnNewTurn(t *testing.T) {
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
	req := projectAssistantRunRequest{
		Identity:       id,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
	}

	// Seed a grant as if a previous turn already earned plan approval.
	grant := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:     "Build app shell",
		TargetPaths: []string{"src/"},
		Operations:  []string{projectToolWriteFile},
	})
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), req.MessageScope, &grant); err != nil {
		t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
	}

	result, err := engine.StreamProjectAssistant(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v, want no plan permission prompt with an active grant", err)
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
	if messages.saveAssistantRunCount != 2 {
		t.Fatalf("assistant run saves = %d, want running and completed audit rows without a permission checkpoint", messages.saveAssistantRunCount)
	}
}

func TestEinoAssistantEngineCommitRequestConsumesApprovedPlan(t *testing.T) {
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
	verifyTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        projectToolVerifyDevelopmentRuntime,
			Description: "Verify the development runtime.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Risk:        projectAssistantToolRiskRead,
		},
		result: `{"status":"reachable"}`,
	}
	commitTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        projectToolCommitProjectFiles,
			Description: "Commit project files.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Risk:        projectAssistantToolRiskCommit,
		},
		result: `{"status":"committed"}`,
	}
	chatModel := &planWriteCommitWriteEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{
				newProjectEinoAssistantServerTool(server, planTool, req, state),
				newProjectEinoAssistantServerTool(server, writeTool, req, state),
				newProjectEinoAssistantServerTool(server, verifyTool, req, state),
				newProjectEinoAssistantTool(commitTool, req, state),
			}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	req := projectAssistantRunRequest{
		Identity:       id,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
	}

	_, err := engine.StreamProjectAssistant(context.Background(), req)
	var planPermissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &planPermissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want plan permission required", err)
	}
	planRun, err := messages.GetAssistantRun(context.Background(), req.MessageScope, planPermissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun(plan) returned error: %v", err)
	}
	var planCheckpoint projectAssistantCheckpointState
	if err := json.Unmarshal(planRun.Checkpoint, &planCheckpoint); err != nil {
		t.Fatalf("decode plan checkpoint returned error: %v", err)
	}

	_, err = engine.ResumeProjectAssistant(
		context.Background(),
		req,
		projectAssistantResumeRequest{
			RequestID: planPermissionErr.RequestID,
			Decision:  string(projectAssistantPermissionAllow),
		},
		planCheckpoint,
	)
	var commitPermissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &commitPermissionErr) {
		t.Fatalf("ResumeProjectAssistant(plan) error = %v, want commit permission required", err)
	}
	if commitPermissionErr.ToolName != projectToolCommitProjectFiles {
		t.Fatalf("permission tool = %q, want commit_project_files", commitPermissionErr.ToolName)
	}
	if commitTool.calls != 0 {
		t.Fatalf("commit calls = %d, want commit blocked on permission", commitTool.calls)
	}
	read, err := workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile after approved write returned error: %v", err)
	}
	if read.Content != "approved plan write\n" {
		t.Fatalf("content = %q, want approved plan write", read.Content)
	}
	commitRun, err := messages.GetAssistantRun(context.Background(), req.MessageScope, commitPermissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun(commit) returned error: %v", err)
	}
	var commitCheckpoint projectAssistantCheckpointState
	if err := json.Unmarshal(commitRun.Checkpoint, &commitCheckpoint); err != nil {
		t.Fatalf("decode commit checkpoint returned error: %v", err)
	}
	if commitCheckpoint.ApprovedPlan != nil {
		t.Fatalf("commit checkpoint approved plan = %#v, want nil after commit request", commitCheckpoint.ApprovedPlan)
	}

	_, err = engine.ResumeProjectAssistant(
		context.Background(),
		req,
		projectAssistantResumeRequest{
			RequestID: commitPermissionErr.RequestID,
			Decision:  string(projectAssistantPermissionAllow),
		},
		commitCheckpoint,
	)
	var writePermissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &writePermissionErr) {
		t.Fatalf("ResumeProjectAssistant(commit) error = %v, want fresh write permission required", err)
	}
	if writePermissionErr.ToolName != projectToolWriteFile {
		t.Fatalf("permission tool = %q, want write_file", writePermissionErr.ToolName)
	}
	if commitTool.calls != 1 {
		t.Fatalf("commit calls = %d, want approved commit to run once", commitTool.calls)
	}
	read, err = workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile after post-commit write request returned error: %v", err)
	}
	if read.Content != "approved plan write\n" {
		t.Fatalf("content = %q, want post-commit write to wait for fresh permission", read.Content)
	}
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
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Identity:       id,
			Project:        project,
			WorkspaceScope: projectWorkspaceScope(id, project.Name),
			MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		},
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want permission required", err)
	}
	run, err := messages.GetAssistantRun(context.Background(), projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name), permissionErr.RunID)
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
	req := projectAssistantRunRequest{
		Identity:       id,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
	}
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
				Arguments: `{"summary":"Build app shell","steps":["Write the app entry"],"targetPaths":["src/"],"allowedOperations":["write_file"],"acceptanceCriteria":["src/App.tsx exists"]}`,
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
				Arguments: `{"summary":"Build app shell","steps":["Write the app entry"],"targetPaths":["src/"],"allowedOperations":["write_file"],"acceptanceCriteria":["src/App.tsx exists"]}`,
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
				Arguments: `{"summary":"Build app shell","steps":["Inspect the app","Write the app entry"],"targetPaths":["src/"],"allowedOperations":["write_file"],"acceptanceCriteria":["src/App.tsx exists"]}`,
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
				Arguments: `{"summary":"Widen hidden grant","steps":["edit secrets"],"targetPaths":["secrets/"],"allowedOperations":["apply_patch"],"acceptanceCriteria":["secrets changed"]}`,
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
	project.Spec.DisplayName = "Demo"
	return projectAssistantRunRequest{
		Identity:       identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
		Project:        project,
		Repository:     &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady, Ready: true},
		WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"},
		MessageScope:   store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"},
		TurnProfile:    profile,
	}
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
	// Only count real run checkpoints. The reserved plan-grant run is
	// cross-turn approval bookkeeping, not a permission/follow-up checkpoint.
	if run.ID != projectAssistantApprovedPlanGrantRunID {
		s.saveAssistantRunCount++
		copy := run
		copy.Audit = append([]byte(nil), run.Audit...)
		copy.Checkpoint = append([]byte(nil), run.Checkpoint...)
		s.lastAssistantRun = &copy
	}
	return s.MemoryStore.SaveAssistantRun(ctx, scope, run)
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
