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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

type projectAssistantAuditProbeHTTPRequest struct {
	path string
	body []byte
}

type projectAssistantAuditProbeMessageCapture struct {
	mu       sync.Mutex
	messages []*schema.Message
}

func (c *projectAssistantAuditProbeMessageCapture) capture(input callbacks.CallbackInput) {
	if c == nil {
		return
	}
	modelInput := einomodel.ConvCallbackInput(input)
	if modelInput == nil {
		return
	}
	c.mu.Lock()
	c.messages = append([]*schema.Message(nil), modelInput.Messages...)
	c.mu.Unlock()
}

func (c *projectAssistantAuditProbeMessageCapture) snapshot() []*schema.Message {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*schema.Message(nil), c.messages...)
}

func TestProjectEinoAssistantAuditMeasuresFinalChatProviderInput(t *testing.T) {
	settings := projectLLMSettings{
		Provider: "openai-compatible",
		Model:    "gpt-4o-mini",
		APIKey:   "test-key",
	}
	response := `{"id":"audit-chat","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8}}`
	result := projectAssistantAuditRunDeepModelProbe(t, settings, response, true)
	projectAssistantAuditAssertFinalInput(t, result, "/v1/chat/completions")
	if got := projectAssistantAuditProbeSystemMessageCount(result.messages, projectEinoAssistantV2DeepInstruction); got != 1 {
		t.Fatalf("Deep instruction count = %d, want one", got)
	}
	if got := projectAssistantAuditProbeSystemMessageCountContaining(result.messages, "A user update may be useful for this substantial work"); got != 1 {
		t.Fatalf("progress reminder count = %d, want one", got)
	}
	if got := projectAssistantAuditProbeToolContentCount(result.messages, "transient receipt probe marker"); got != 1 {
		t.Fatalf("expanded transient receipt count = %d, want one", got)
	}
	if strings.Contains(string(result.run.Audit), "transient receipt probe marker") ||
		strings.Contains(string(result.run.Audit), projectEinoAssistantV2DeepInstruction) {
		t.Fatal("audit persisted model input content")
	}
}

func TestProjectEinoAssistantAuditMeasuresFinalResponsesProviderInput(t *testing.T) {
	settings := projectLLMSettings{
		Provider: "openai-compatible",
		Model:    "gpt-6.1-sol",
		APIKey:   "test-key",
	}
	result := projectAssistantAuditRunDeepModelProbe(t, settings, projectResponsesFixtureText, false)
	projectAssistantAuditAssertFinalInput(t, result, "/v1/responses")
	if got := projectAssistantAuditProbeSystemMessageCount(result.messages, projectEinoAssistantV2DeepInstruction); got != 1 {
		t.Fatalf("Deep instruction count = %d, want one", got)
	}
}

func TestProjectEinoAssistantEngineMeasuresProductionLunaAndChatInputs(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		wantPath string
	}{
		{name: "GPT-6 Luna Responses", model: "gpt-6-luna", wantPath: "/v1/responses"},
		{name: "GPT-6.1 Sol Responses", model: "gpt-6.1-sol", wantPath: "/v1/responses"},
		{name: "Chat Completions", model: "gpt-4o-mini", wantPath: "/v1/chat/completions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan projectAssistantAuditProbeHTTPRequest, 2)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				requests <- projectAssistantAuditProbeHTTPRequest{path: r.URL.Path, body: body}
				w.Header().Set("Content-Type", "text/event-stream")
				if r.URL.Path == "/v1/responses" {
					projectEinoAssistantAuditInputResponsesStream(w)
					return
				}
				projectEinoAssistantAuditInputChatStream(w)
			}))
			t.Cleanup(provider.Close)

			project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "audit-project", UID: "audit-project-uid"}}
			id := identity{orgUUID: "audit-org", workspaceUUID: "audit-workspace", user: "audit-user"}
			workspaceStore := workspace.NewFileStore(t.TempDir())
			now := time.Now().UTC()
			run := &store.AssistantRun{
				ID:   "production-audit-" + strings.ReplaceAll(test.model, ".", "-"),
				Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning,
				CreatedAt: now, UpdatedAt: now,
			}
			scope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
			runStore := store.NewMemoryStore()
			if err := runStore.SaveAssistantRun(context.Background(), scope, *run); err != nil {
				t.Fatalf("save fixture assistant run: %v", err)
			}
			settings := projectLLMSettings{
				Provider: "openai-compatible", BaseURL: provider.URL + "/v1", Model: test.model, APIKey: "fixture-key",
			}
			req := projectAssistantRunRequest{
				Identity: id, Project: project, Workspace: workspaceStore,
				WorkspaceScope: projectWorkspaceScope(id, project), MessageScope: scope,
				AssistantRun: run, LLM: settings,
				Conversation:       []chatMessage{{Role: "user", Content: "Read the current project instructions and reply OK."}},
				CollaborationMode:  projectAssistantCollaborationModeDefault,
				executionAuthority: projectAssistantAuditInputEngineAuthority{},
			}
			engineServer := &Server{store: runStore, workspaces: workspaceStore}
			engine := projectEinoAssistantEngine{
				server:   engineServer,
				newModel: newProjectEinoAssistantModelFactory(engineServer),
				newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
					return nil, nil
				},
			}
			result, err := engine.StreamProjectAssistant(context.Background(), req)
			if err != nil {
				t.Fatalf("production engine run: %v", err)
			}
			if result.Content != "OK" {
				t.Fatalf("production engine result = %q, want OK", result.Content)
			}
			var request projectAssistantAuditProbeHTTPRequest
			select {
			case request = <-requests:
			case <-time.After(5 * time.Second):
				t.Fatal("production model made no provider HTTP request")
			}
			if request.path != test.wantPath {
				t.Fatalf("production model path = %q, want %q", request.path, test.wantPath)
			}
			var requestPayload any
			if err := json.Unmarshal(request.body, &requestPayload); err != nil {
				t.Fatalf("decode production request body: %v", err)
			}
			if got := projectAssistantAuditInputCountString(requestPayload, projectEinoAssistantV2DeepInstructionWithoutNativeBrowser); got != 1 {
				t.Fatalf("production engine Deep instruction count = %d, want one (body bytes=%d, prefix present=%t)", got, len(request.body), strings.Contains(string(request.body), "You are the App Studio project assistant."))
			}
			var audit projectAssistantRunAudit
			if err := json.Unmarshal(run.Audit, &audit); err != nil {
				t.Fatalf("decode production model audit: %v", err)
			}
			if len(audit.ModelCalls) != 1 || audit.ModelCallStats == nil {
				t.Fatalf("production model audit rows = %#v", audit.ModelCalls)
			}
			call := audit.ModelCalls[0]
			contentBytes := projectAssistantAuditInputContentBytes(requestPayload)
			if !call.ProviderInputObserved || call.SystemMessageBytes < int64(len(projectEinoAssistantV2DeepInstructionWithoutNativeBrowser)) ||
				call.InputBytes < int64(contentBytes) || call.MessageBytes < int64(contentBytes) {
				t.Fatalf("production engine audit does not cover final provider input: call=%#v observedContentBytes=%d", call, contentBytes)
			}
			if audit.ModelCallStats.MessageBytes != call.MessageBytes || audit.ModelCallStats.SystemMessageBytes != call.SystemMessageBytes ||
				audit.ModelCallStats.InputBytes != call.InputBytes {
				t.Fatalf("production engine rollup differs from final provider input: stats=%#v call=%#v", audit.ModelCallStats, call)
			}
		})
	}
}

func TestProjectEinoAssistantEngineGatesNativeBrowserGuidanceByDiscovery(t *testing.T) {
	if got, want := len(projectEinoAssistantV2DeepInstruction)-len(projectEinoAssistantV2DeepInstructionWithoutNativeBrowser), len(projectEinoAssistantNativeBrowserInstruction); got != want {
		t.Fatalf("browserless prompt byte reduction = %d, want browser clause size %d", got, want)
	}
	browserTool := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{Name: "browser_snapshot", Description: "Capture the preview accessibility snapshot."},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) { return "{}", nil },
	}
	browserDiscovery := projectEinoAssistantToolDiscovery{BrowserTools: []projectAssistantTool{browserTool}}
	legacyInspectorDiscovery := projectEinoAssistantToolDiscovery{
		IncludePreviewInspection: true,
		Prompt:                   "Preview inspection capability: inspect_development_preview is available through the compatibility inspector.",
	}
	tests := []struct {
		name              string
		discovery         projectEinoAssistantToolDiscovery
		visibleTools      []einotool.BaseTool
		wantInstruction   string
		wantBrowserSchema bool
		wantLegacyTool    bool
	}{
		{
			name:              "browser available and visible",
			discovery:         browserDiscovery,
			visibleTools:      []einotool.BaseTool{projectEinoAssistantAuditInputTool{name: "browser_snapshot"}},
			wantInstruction:   projectEinoAssistantV2DeepInstruction,
			wantBrowserSchema: true,
		},
		{
			name:            "browser available but deferred",
			discovery:       browserDiscovery,
			wantInstruction: projectEinoAssistantV2DeepInstruction,
		},
		{
			name:            "native browser absent with compatibility inspector",
			discovery:       legacyInspectorDiscovery,
			visibleTools:    []einotool.BaseTool{projectEinoAssistantAuditInputTool{name: "inspect_development_preview"}},
			wantInstruction: projectEinoAssistantV2DeepInstructionWithoutNativeBrowser,
			wantLegacyTool:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan projectAssistantAuditProbeHTTPRequest, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				requests <- projectAssistantAuditProbeHTTPRequest{path: r.URL.Path, body: body}
				w.Header().Set("Content-Type", "text/event-stream")
				projectEinoAssistantAuditInputChatStream(w)
			}))
			t.Cleanup(provider.Close)

			project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "browser-guidance-project", UID: "browser-guidance-uid"}}
			id := identity{orgUUID: "browser-guidance-org", workspaceUUID: "browser-guidance-workspace", user: "browser-guidance-user"}
			workspaceStore := workspace.NewFileStore(t.TempDir())
			now := time.Now().UTC()
			run := &store.AssistantRun{
				ID:   "browser-guidance-" + strings.ReplaceAll(test.name, " ", "-"),
				Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning,
				CreatedAt: now, UpdatedAt: now,
			}
			scope := testProjectMessageScope(id.orgUUID, id.workspaceUUID, project.Name)
			runStore := store.NewMemoryStore()
			if err := runStore.SaveAssistantRun(context.Background(), scope, *run); err != nil {
				t.Fatalf("save fixture assistant run: %v", err)
			}
			settings := projectLLMSettings{
				Provider: "openai-compatible", BaseURL: provider.URL + "/v1", Model: "gpt-4o-mini", APIKey: "fixture-key",
			}
			req := projectAssistantRunRequest{
				Identity: id, Project: project, Workspace: workspaceStore,
				WorkspaceScope: projectWorkspaceScope(id, project), MessageScope: scope,
				AssistantRun: run, LLM: settings,
				Conversation:       []chatMessage{{Role: "user", Content: "Inspect the preview and reply OK."}},
				CollaborationMode:  projectAssistantCollaborationModeDefault,
				executionAuthority: projectAssistantAuditInputEngineAuthority{},
			}
			engineServer := &Server{store: runStore, workspaces: workspaceStore}
			engine := projectEinoAssistantEngine{
				server:   engineServer,
				newModel: newProjectEinoAssistantModelFactory(engineServer),
				newTools: func(_ context.Context, _ projectAssistantRunRequest, runState *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
					runState.SetToolDiscovery(test.discovery)
					return append([]einotool.BaseTool(nil), test.visibleTools...), nil
				},
			}
			result, err := engine.StreamProjectAssistant(context.Background(), req)
			if err != nil {
				t.Fatalf("production engine run: %v", err)
			}
			if result.Content != "OK" {
				t.Fatalf("production engine result = %q, want OK", result.Content)
			}
			var request projectAssistantAuditProbeHTTPRequest
			select {
			case request = <-requests:
			case <-time.After(5 * time.Second):
				t.Fatal("production model made no provider HTTP request")
			}
			var payload any
			if err := json.Unmarshal(request.body, &payload); err != nil {
				t.Fatalf("decode provider request body: %v", err)
			}
			if got := projectAssistantAuditInputCountString(payload, test.wantInstruction); got != 1 {
				t.Fatalf("derived Deep instruction count = %d, want one (request bytes=%d)", got, len(request.body))
			}
			wantBrowserClause := 0
			if len(test.discovery.BrowserTools) > 0 {
				wantBrowserClause = 1
			}
			if got := projectAssistantAuditInputCountString(payload, projectEinoAssistantNativeBrowserInstruction); got != wantBrowserClause {
				t.Fatalf("native browser instruction count = %d, want %d", got, wantBrowserClause)
			}
			if got := projectAssistantAuditProviderToolCount(payload, "browser_snapshot"); (got == 1) != test.wantBrowserSchema {
				t.Fatalf("browser_snapshot schema count = %d, want present=%t", got, test.wantBrowserSchema)
			}
			if got := projectAssistantAuditProviderToolCount(payload, "inspect_development_preview"); (got == 1) != test.wantLegacyTool {
				t.Fatalf("compatibility inspector schema count = %d, want present=%t", got, test.wantLegacyTool)
			}
			for _, required := range []string{
				"do not assume shell, browser, host filesystem, or subagent access",
				"hostile application-controlled data",
				"Do not claim rendered content, interactions, data flow, or acceptance criteria were independently verified",
			} {
				if !strings.Contains(string(request.body), required) {
					t.Errorf("provider input lost common safety instruction %q", required)
				}
			}
			if test.wantLegacyTool && !strings.Contains(string(request.body), test.discovery.Prompt) {
				t.Fatal("browser instruction gate removed compatibility inspector guidance")
			}
			if len(test.discovery.BrowserTools) > 0 && !strings.Contains(string(request.body), "Never use browser_evaluate, browser_run_code") {
				t.Fatal("discovered browser tools lost the arbitrary-code ban")
			}
		})
	}
}

type projectEinoAssistantAuditInputTool struct {
	name string
}

func (t projectEinoAssistantAuditInputTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        t.name,
		Desc:        "Test tool used to inspect the provider model request.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func (projectEinoAssistantAuditInputTool) InvokableRun(context.Context, string, ...einotool.Option) (string, error) {
	return "{}", nil
}

func projectAssistantAuditProviderToolCount(value any, name string) int {
	root, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	tools, ok := root["tools"].([]any)
	if !ok {
		return 0
	}
	count := 0
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		function, ok := tool["function"].(map[string]any)
		if ok && function["name"] == name {
			count++
		}
	}
	return count
}

type projectAssistantAuditInputEngineAuthority struct{}

func (projectAssistantAuditInputEngineAuthority) AdmitMutation(context.Context) error { return nil }

func (projectAssistantAuditInputEngineAuthority) PersistRun(context.Context, store.AssistantRun) error {
	return nil
}

func (projectAssistantAuditInputEngineAuthority) PersistAudit(context.Context, []byte) error {
	return nil
}

func projectEinoAssistantAuditInputChatStream(w io.Writer) {
	_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-audit\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":null}]}\n\n")
	_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-audit\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":1,\"total_tokens\":13}}\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func projectEinoAssistantAuditInputResponsesStream(w io.Writer) {
	projectResponsesFixtureEvent(w, `{"type":"response.created","response":{"id":"resp_audit","status":"in_progress","output":[]}}`)
	projectResponsesFixtureEvent(w, `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_audit","role":"assistant","status":"in_progress","content":[]}}`)
	projectResponsesFixtureEvent(w, `{"type":"response.content_part.added","item_id":"msg_audit","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`)
	projectResponsesFixtureEvent(w, `{"type":"response.output_text.delta","item_id":"msg_audit","output_index":0,"content_index":0,"delta":"OK"}`)
	projectResponsesFixtureEvent(w, `{"type":"response.output_text.done","item_id":"msg_audit","output_index":0,"content_index":0,"text":"OK"}`)
	projectResponsesFixtureEvent(w, `{"type":"response.content_part.done","item_id":"msg_audit","output_index":0,"content_index":0,"part":{"type":"output_text","text":"OK","annotations":[]}}`)
	projectResponsesFixtureEvent(w, `{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_audit","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}}`)
	projectResponsesFixtureEvent(w, `{"type":"response.completed","response":{"id":"resp_audit","status":"completed","output":[{"type":"message","id":"msg_audit","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":1,"total_tokens":13,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`)
}

func projectAssistantAuditInputCountString(value any, wanted string) int {
	switch current := value.(type) {
	case string:
		return strings.Count(current, wanted)
	case []any:
		count := 0
		for _, item := range current {
			count += projectAssistantAuditInputCountString(item, wanted)
		}
		return count
	case map[string]any:
		count := 0
		for _, item := range current {
			count += projectAssistantAuditInputCountString(item, wanted)
		}
		return count
	}
	return 0
}

func projectAssistantAuditInputContentBytes(value any) int {
	switch current := value.(type) {
	case []any:
		total := 0
		for _, item := range current {
			total += projectAssistantAuditInputContentBytes(item)
		}
		return total
	case map[string]any:
		total := 0
		for key, item := range current {
			if key == "content" {
				total += projectAssistantAuditInputContentTextBytes(item)
				continue
			}
			total += projectAssistantAuditInputContentBytes(item)
		}
		return total
	}
	return 0
}

func projectAssistantAuditInputContentTextBytes(value any) int {
	switch current := value.(type) {
	case string:
		return len(current)
	case []any:
		total := 0
		for _, item := range current {
			total += projectAssistantAuditInputContentTextBytes(item)
		}
		return total
	case map[string]any:
		total := 0
		for key, item := range current {
			if key == "text" {
				if text, ok := item.(string); ok {
					total += len(text)
				}
				continue
			}
			if key == "content" {
				total += projectAssistantAuditInputContentTextBytes(item)
			}
		}
		return total
	}
	return 0
}

type projectAssistantAuditDeepModelProbeResult struct {
	messages       []*schema.Message
	request        projectAssistantAuditProbeHTTPRequest
	requestCount   int
	requestBytes   int
	initialSize    projectAssistantAuditInputSize
	run            *store.AssistantRun
	settings       projectLLMSettings
	modelCall      projectAssistantAuditModelCall
	modelCallStats *projectAssistantAuditModelCallStats
}

func projectAssistantAuditRunDeepModelProbe(
	t *testing.T,
	settings projectLLMSettings,
	responseBody string,
	includeModelWrappers bool,
) projectAssistantAuditDeepModelProbeResult {
	t.Helper()
	requests := make(chan projectAssistantAuditProbeHTTPRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests <- projectAssistantAuditProbeHTTPRequest{path: r.URL.Path, body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)
	settings.BaseURL = server.URL + "/v1"

	ctx := context.Background()
	baseModel, err := newProjectEinoChatModel(ctx, settings)
	if err != nil {
		t.Fatalf("create production model: %v", err)
	}
	runState := newProjectEinoAssistantRunState()
	req := projectAssistantRunRequest{LLM: settings}
	model := einomodel.BaseChatModel(baseModel)
	inputMessages := []*schema.Message{schema.UserMessage("continue the audit probe")}
	if includeModelWrappers {
		policy := projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)
		req.TurnPolicy = policy
		req.StreamCallbacks.OnProgress = func(string) {}
		runState.SetTurnPolicy(policy)
		placeholder := runState.RegisterTransientToolResult(
			projectToolReadAttachment,
			`{"content":"transient receipt probe marker","complete":true}`,
		)
		if !runState.QueueProgressReminder(projectEinoAssistantProgressReminderVerification, "preview needs one more check") {
			t.Fatal("queue progress reminder")
		}
		call := schema.ToolCall{
			ID:   "read-call",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolReadAttachment,
				Arguments: `{"path":"notes.txt"}`,
			},
		}
		inputMessages = []*schema.Message{
			schema.UserMessage("read the note"),
			schema.AssistantMessage("", []schema.ToolCall{call}),
			{Role: schema.Tool, ToolName: projectToolReadAttachment, ToolCallID: call.ID, Content: placeholder},
			schema.UserMessage("continue"),
		}
		mainModel, _ := projectEinoAssistantModels(baseModel, req, runState)
		model = mainModel
	}

	const deepInstruction = projectEinoAssistantV2DeepInstruction
	agent, err := deep.New(ctx, &deep.Config{
		Name:                   "app-studio-audit-probe",
		Description:            "Audits final model message sizing.",
		ChatModel:              model,
		Instruction:            deepInstruction,
		ToolsConfig:            adk.ToolsConfig{},
		MaxIteration:           1,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	if err != nil {
		t.Fatalf("create Deep agent: %v", err)
	}

	runState.NextModelCallOrdinal()
	run := &store.AssistantRun{ID: "audit-input-probe"}
	auditRecorder := newProjectAssistantRunAuditRecorder(req, run, time.Now().UTC())
	initialSize := projectAssistantAuditMeasureInputForModel(inputMessages, settings)
	if err := auditRecorder.recordModelCallWithInputSize(
		ctx, 1, 0, 0, nil, nil, nil, initialSize,
	); err != nil {
		t.Fatalf("record initial model call: %v", err)
	}
	modelCallback := newProjectEinoAssistantModelCallbackHandler(req.StreamCallbacks, runState, auditRecorder)
	capture := &projectAssistantAuditProbeMessageCapture{}
	captureCallback := callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
		if info != nil && info.Component == "ChatModel" {
			capture.capture(input)
		}
		return ctx
	}).Build()
	it := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent}).Run(
		ctx,
		inputMessages,
		adk.WithCallbacks(modelCallback, captureCallback),
	)
	for {
		event, ok := it.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatalf("Deep agent run: %v", event.Err)
		}
	}
	var request projectAssistantAuditProbeHTTPRequest
	requestCount := 0
	for {
		select {
		case observed := <-requests:
			request = observed
			requestCount++
		default:
			goto requestsDrained
		}
	}
requestsDrained:
	if requestCount != 1 {
		t.Fatalf("provider request count = %d, want one", requestCount)
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode model audit: %v", err)
	}
	if len(audit.ModelCalls) != 1 {
		t.Fatalf("model call rows = %d, want one", len(audit.ModelCalls))
	}
	if audit.ModelCallStats == nil {
		t.Fatal("model call size rollup is missing")
	}
	return projectAssistantAuditDeepModelProbeResult{
		messages:       capture.snapshot(),
		request:        request,
		requestCount:   requestCount,
		requestBytes:   len(request.body),
		initialSize:    initialSize,
		run:            run,
		settings:       settings,
		modelCall:      audit.ModelCalls[0],
		modelCallStats: audit.ModelCallStats,
	}
}

func projectAssistantAuditAssertFinalInput(
	t *testing.T,
	result projectAssistantAuditDeepModelProbeResult,
	wantPath string,
) {
	t.Helper()
	if result.request.path != wantPath {
		t.Fatalf("provider path = %q, want %q", result.request.path, wantPath)
	}
	if result.requestCount != 1 || len(result.messages) == 0 {
		t.Fatalf("captured %d provider request(s) and %d callback messages", result.requestCount, len(result.messages))
	}
	messageJSON, err := json.Marshal(projectEinoMessagesToChat(result.messages))
	if err != nil {
		t.Fatalf("marshal callback messages: %v", err)
	}
	wantSize := projectAssistantAuditMeasureInputForModel(result.messages, result.settings)
	if result.modelCall.MessageBytes != wantSize.MessageBytes ||
		result.modelCall.SystemMessageBytes != wantSize.SystemMessageBytes ||
		result.modelCall.HistoryMessageBytes != wantSize.HistoryMessageBytes ||
		result.modelCall.MessageFramingBytes != wantSize.MessageFramingBytes ||
		result.modelCall.ToolContractBytes != wantSize.ToolContractBytes ||
		result.modelCall.InputBytes != wantSize.InputBytes {
		t.Fatalf("audit size = %#v, want final callback message size %#v", result.modelCall, wantSize)
	}
	if !result.modelCall.ProviderInputObserved {
		t.Fatal("model call input still appears to be only the lifecycle estimate")
	}
	if result.modelCall.MessageBytes != int64(len(messageJSON)) {
		t.Fatalf("message bytes = %d, callback serialization = %d", result.modelCall.MessageBytes, len(messageJSON))
	}
	if result.requestBytes <= int(result.modelCall.MessageBytes) {
		t.Fatalf("provider request body bytes = %d, should include request fields beyond message bytes %d", result.requestBytes, result.modelCall.MessageBytes)
	}
	if result.modelCallStats.InputBytes != result.modelCall.InputBytes ||
		result.modelCallStats.MessageBytes != result.modelCall.MessageBytes ||
		result.modelCallStats.SystemMessageBytes != result.modelCall.SystemMessageBytes ||
		result.modelCallStats.HistoryMessageBytes != result.modelCall.HistoryMessageBytes ||
		result.modelCallStats.MessageFramingBytes != result.modelCall.MessageFramingBytes ||
		result.modelCallStats.ToolContractBytes != result.modelCall.ToolContractBytes {
		t.Fatalf("model call rollup %#v does not match final call %#v", result.modelCallStats, result.modelCall)
	}
}

func projectAssistantAuditProbeSystemMessageCount(messages []*schema.Message, exact string) int {
	count := 0
	for _, message := range messages {
		if message != nil && message.Role == schema.System && message.Content == exact {
			count++
		}
	}
	return count
}

func projectAssistantAuditProbeSystemMessageCountContaining(messages []*schema.Message, fragment string) int {
	count := 0
	for _, message := range messages {
		if message != nil && message.Role == schema.System && strings.Contains(message.Content, fragment) {
			count++
		}
	}
	return count
}

func projectAssistantAuditProbeToolContentCount(messages []*schema.Message, fragment string) int {
	count := 0
	for _, message := range messages {
		if message != nil && message.Role == schema.Tool && strings.Contains(message.Content, fragment) {
			count++
		}
	}
	return count
}

func TestProjectAssistantAuditFinalModelInputSizeReplacesPerOrdinal(t *testing.T) {
	started := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	run := &store.AssistantRun{ID: "audit-final-input"}
	recorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, started)
	initialSize := projectAssistantAuditMeasureInput([]*schema.Message{schema.UserMessage("state before Deep")})
	if err := recorder.recordModelCallWithInputSize(context.Background(), 1, 0, 0, nil, nil, nil, initialSize); err != nil {
		t.Fatalf("record start row: %v", err)
	}
	if recorder.audit.ModelCalls[0].ProviderInputObserved {
		t.Fatal("lifecycle estimate was marked as provider-observed before the callback")
	}
	startEventAt := recorder.audit.ModelCalls[0].AtOffsetMS
	persistCalls := 1
	recorder.setPersister(func(context.Context, []byte) error { persistCalls++; return nil })
	firstAttempt := projectAssistantAuditMeasureInput([]*schema.Message{
		schema.SystemMessage("Deep instruction"), schema.UserMessage("first recovery attempt"),
	})
	lastAttempt := projectAssistantAuditMeasureInput([]*schema.Message{
		schema.SystemMessage("Deep instruction"), schema.UserMessage("trimmed history plus progress reminder"),
	})
	recorder.updateModelCallInputSize(1, firstAttempt)
	if persistCalls != 1 {
		t.Fatalf("input observation persisted early; calls=%d, want only the model-start write", persistCalls)
	}
	recorder.updateModelCallInputSize(1, lastAttempt)
	if persistCalls != 1 {
		t.Fatalf("retry input observation performed an extra write; calls=%d", persistCalls)
	}
	if err := recorder.recordModelResult(context.Background(), 1, schema.AssistantMessage("done", nil)); err != nil {
		t.Fatalf("persist model result: %v", err)
	}
	if persistCalls != 2 {
		t.Fatalf("persist calls = %d, want start plus existing result write", persistCalls)
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.ModelCalls) != 1 {
		t.Fatalf("model call rows = %d", len(audit.ModelCalls))
	}
	call := audit.ModelCalls[0]
	if !call.ProviderInputObserved {
		t.Fatal("last model input observation was not marked provider-observed")
	}
	stats := audit.ModelCallStats
	if call.MessageBytes != lastAttempt.MessageBytes || call.SystemMessageBytes != lastAttempt.SystemMessageBytes ||
		call.HistoryMessageBytes != lastAttempt.HistoryMessageBytes || call.MessageFramingBytes != lastAttempt.MessageFramingBytes {
		t.Fatalf("retained call size = %#v, want last attempt %#v", call, lastAttempt)
	}
	if stats == nil || stats.MessageBytes != lastAttempt.MessageBytes || stats.SystemMessageBytes != lastAttempt.SystemMessageBytes ||
		stats.HistoryMessageBytes != lastAttempt.HistoryMessageBytes || stats.MessageFramingBytes != lastAttempt.MessageFramingBytes {
		t.Fatalf("rollup size = %#v, want last attempt %#v", stats, lastAttempt)
	}
	if call.AtOffsetMS != startEventAt || call.Outcome != "text" {
		t.Fatalf("input measurement changed model event timing/outcome: %#v", call)
	}
}

type projectAssistantAuditCallbackProbeModel struct {
	einomodel.BaseChatModel
}

func (projectAssistantAuditCallbackProbeModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	_ ...einomodel.Option,
) (*schema.Message, error) {
	ctx = callbacks.OnStart(ctx, &einomodel.CallbackInput{Messages: input})
	message := schema.AssistantMessage("compaction summary", nil)
	callbacks.OnEnd(ctx, &einomodel.CallbackOutput{Message: message})
	return message, nil
}

func (projectAssistantAuditCallbackProbeModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, nil
}

func TestProjectEinoAssistantAuditSizeExcludesCompactionModelInput(t *testing.T) {
	ctx := context.Background()
	initialMessages := []*schema.Message{schema.UserMessage("ordinary state before compaction")}
	initialSize := projectAssistantAuditMeasureInput(initialMessages)
	runState := newProjectEinoAssistantRunState()
	runState.NextModelCallOrdinal()
	run := &store.AssistantRun{ID: "audit-compaction-input"}
	auditRecorder := newProjectAssistantRunAuditRecorder(projectAssistantRunRequest{}, run, time.Now().UTC())
	if err := auditRecorder.recordModelCallWithInputSize(ctx, 1, 0, 0, nil, nil, nil, initialSize); err != nil {
		t.Fatalf("record main model call: %v", err)
	}
	mainHandler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{}, runState, auditRecorder)
	ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Component: "ChatModel"}, mainHandler)
	compaction := &projectEinoAssistantCompactionIsolatedModel{BaseChatModel: projectAssistantAuditCallbackProbeModel{}}
	if _, err := compaction.Generate(ctx, []*schema.Message{
		schema.SystemMessage("compaction-only system instruction"),
		schema.UserMessage("compaction-only summary prompt"),
	}); err != nil {
		t.Fatalf("run isolated compaction model: %v", err)
	}
	var audit projectAssistantRunAudit
	if err := json.Unmarshal(run.Audit, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit.ModelCalls) != 1 || audit.ModelCalls[0].MessageBytes != initialSize.MessageBytes ||
		audit.ModelCalls[0].SystemMessageBytes != initialSize.SystemMessageBytes {
		t.Fatalf("compaction input changed main-call audit size: %#v", audit.ModelCalls)
	}
	if audit.ModelCalls[0].ProviderInputObserved {
		t.Fatal("isolated compaction callback was incorrectly marked as observing the main model input")
	}
}
