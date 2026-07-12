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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
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
	if strings.Join(chunks, "") != "Hello world" || len(chunks) != 2 {
		t.Fatalf("chunks = %#v, want two public assistant chunks", chunks)
	}
}

func TestProjectEinoAssistantMessageOutputPublishesAssistantStreamChunksBeforeEOF(t *testing.T) {
	stream, writer := schema.Pipe[*schema.Message](0)
	output := &adk.TypedMessageVariant[*schema.Message]{
		IsStreaming:   true,
		MessageStream: stream,
		Role:          schema.Assistant,
	}
	chunks := make(chan string, 2)
	result := make(chan struct {
		msg *schema.Message
		err error
	}, 1)
	go func() {
		msg, err := projectEinoAssistantMessageOutput(context.Background(), output, projectAssistantStreamCallbacks{
			OnChunk: func(chunk string) { chunks <- chunk },
		})
		result <- struct {
			msg *schema.Message
			err error
		}{msg: msg, err: err}
	}()

	if closed := writer.Send(schema.AssistantMessage("Hello ", nil), nil); closed {
		t.Fatal("stream closed before first chunk was sent")
	}
	select {
	case got := <-chunks:
		if got != "Hello " {
			t.Fatalf("first streamed chunk = %q, want %q", got, "Hello ")
		}
	case <-time.After(250 * time.Millisecond):
		writer.Close()
		<-result
		t.Fatal("first assistant chunk was not published before stream EOF")
	}

	if closed := writer.Send(schema.AssistantMessage("world", nil), nil); closed {
		t.Fatal("stream closed before second chunk was sent")
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
		if chunk != "world" {
			t.Fatalf("second streamed chunk = %q, want %q", chunk, "world")
		}
	default:
		t.Fatal("second assistant chunk was not published")
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

func TestEinoAssistantToolSearchUsesBundlesForProductToolbox(t *testing.T) {
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

	if !stringSliceEqual(staticNames, []string{projectToolAskFollowUp, projectToolRequestProjectPlanApproval}) {
		t.Fatalf("static tools = %#v, want only collaboration tools", staticNames)
	}
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
	} {
		if !stringSliceContains(dynamicNames, want) {
			t.Fatalf("dynamic tools = %#v, want %s", dynamicNames, want)
		}
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
			for _, want := range tt.wantAllow {
				if !stringSliceContains(filteredNames, want) {
					t.Fatalf("%s filtered tools = %#v, want %s", tt.profile, filteredNames, want)
				}
			}
			for _, unwanted := range tt.wantReject {
				if stringSliceContains(filteredNames, unwanted) {
					t.Fatalf("%s filtered tools = %#v, should not expose %s", tt.profile, filteredNames, unwanted)
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
	if messages.saveAssistantRunCount != 1 {
		t.Fatalf("assistant run saves = %d, want one follow-up checkpoint", messages.saveAssistantRunCount)
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
	if messages.saveAssistantRunCount != 1 {
		t.Fatalf("assistant run saves = %d, want exactly one permission checkpoint", messages.saveAssistantRunCount)
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
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: newProjectEinoAssistantToolsFactory(server),
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
	if checkpoint.Eino == nil || checkpoint.Eino.InterruptType != projectAssistantInterruptTypeApproval || checkpoint.Eino.ToolName != projectToolRestartRuntime {
		t.Fatalf("checkpoint eino state = %#v, want runtime approval checkpoint", checkpoint.Eino)
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

func TestEinoAssistantEngineRequestsPermissionForDynamicWriteTool(t *testing.T) {
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
		t.Fatalf("StreamProjectAssistant error = %v, want dynamic write permission required", err)
	}
	if permissionErr.ToolName != projectToolWriteFile {
		t.Fatalf("permission tool = %q, want write_file", permissionErr.ToolName)
	}
	if len(chatModel.toolNames) < 2 {
		t.Fatalf("model tool names = %#v, want search and selected write tool calls", chatModel.toolNames)
	}
	if stringSliceContains(chatModel.toolNames[0], projectToolWriteFile) {
		t.Fatalf("initial tools = %#v, want write_file deferred behind tool_search", chatModel.toolNames[0])
	}
	if !stringSliceContains(chatModel.toolNames[0], "tool_search") {
		t.Fatalf("initial tools = %#v, want tool_search", chatModel.toolNames[0])
	}
	if !stringSliceContains(chatModel.toolNames[1], projectToolWriteFile) {
		t.Fatalf("selected tools = %#v, want write_file loaded by tool_search", chatModel.toolNames[1])
	}
	run, err := messages.GetAssistantRun(context.Background(), req.MessageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	if checkpoint.Eino == nil || checkpoint.Eino.InterruptType != projectAssistantInterruptTypePermission || checkpoint.Eino.ToolName != projectToolWriteFile {
		t.Fatalf("checkpoint eino state = %#v, want dynamic write permission checkpoint", checkpoint.Eino)
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
	if result.Content != "dynamic write completed" {
		t.Fatalf("content = %q, want final dynamic write response", result.Content)
	}
	read, err := workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "dynamic write\n" {
		t.Fatalf("content = %q, want approved dynamic write", read.Content)
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
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "unexpected continuation" {
		t.Fatalf("content = %q, want continuation after auto-approved writes", result.Content)
	}
	if messages.saveAssistantRunCount != 0 {
		t.Fatalf("assistant run saves = %d, want no permission checkpoint", messages.saveAssistantRunCount)
	}
	if countProjectAssistantEvents(assistantEvents, projectAssistantEventPermissionNeeded) != 0 || countProjectAssistantEvents(assistantEvents, projectAssistantEventCheckpointSaved) != 0 {
		t.Fatalf("assistant events = %#v, want no permission events", assistantEvents)
	}
	if projectToolEventsWithStatus(toolEvents, "permission_required") != 0 {
		t.Fatalf("tool events = %#v, want no permission-required event", toolEvents)
	}
	for _, path := range []string{"src/one.tsx", "src/two.tsx"} {
		if _, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: path}); err != nil {
			t.Fatalf("ReadFile(%q) returned error: %v", path, err)
		}
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
	if messages.saveAssistantRunCount != 1 {
		t.Fatalf("assistant run saves = %d, want no second permission checkpoint", messages.saveAssistantRunCount)
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
	if messages.saveAssistantRunCount != 0 {
		t.Fatalf("permission checkpoints = %d, want none while a plan grant is active", messages.saveAssistantRunCount)
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
			ID:   "call-search-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "tool_search",
				Arguments: `{"query":"select:write_file"}`,
			},
		}}), nil
	case 2:
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
			ID:   "call-commit",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolCommitProjectFiles,
				Arguments: `{"repositoryRef":"repo-1","paths":["src/App.tsx"],"message":"Initial app"}`,
			},
		}}), nil
	case 4:
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
}

func (s *countingAssistantRunStore) SaveAssistantRun(ctx context.Context, scope store.Scope, run store.AssistantRun) error {
	// Only count real run checkpoints. The reserved plan-grant run is
	// cross-turn approval bookkeeping, not a permission/follow-up checkpoint.
	if run.ID != projectAssistantApprovedPlanGrantRunID {
		s.saveAssistantRunCount++
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
