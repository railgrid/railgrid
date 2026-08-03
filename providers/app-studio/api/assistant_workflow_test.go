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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	approvaltool "github.com/cloudwego/eino-examples/adk/common/tool"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	einoschema "github.com/cloudwego/eino/schema"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

type projectAssistantFailingGraphTool struct {
	calls *int
	err   error
}

func (t projectAssistantFailingGraphTool) Info(context.Context) (*einoschema.ToolInfo, error) {
	return &einoschema.ToolInfo{Name: "failing_graph_tool"}, nil
}

func (t projectAssistantFailingGraphTool) InvokableRun(context.Context, string, ...einotool.Option) (string, error) {
	*t.calls++
	return "", t.err
}

type projectAssistantLegacyExecTool struct {
	calls *int
}

func (t projectAssistantLegacyExecTool) Info(context.Context) (*einoschema.ToolInfo, error) {
	return &einoschema.ToolInfo{Name: projectToolExecCommand}, nil
}

func (t projectAssistantLegacyExecTool) InvokableRun(context.Context, string, ...einotool.Option) (string, error) {
	*t.calls++
	return "legacy exec tool unexpectedly ran", nil
}

func TestProjectAssistantDurableGraphToolFailureIsExactModelFeedback(t *testing.T) {
	ctx := context.Background()
	messages, scope := newAssistantRunEventLedgerTestStore(t, "run-graph-failure")
	calls := 0
	backendErr := errors.New("workflow dependency unavailable")
	spec := projectAssistantToolSpec{Name: "failing_graph_tool", Risk: projectAssistantToolRiskRead}
	tool := projectAssistantDurableGraphTool{
		InvokableTool: projectAssistantFailingGraphTool{calls: &calls, err: backendErr},
		spec:          spec,
		ledger:        newProjectAssistantRunEventLedger(messages, scope, "run-graph-failure"),
	}
	result, err := tool.invokableRun(ctx, "call-graph", `{}`)
	if err != nil || result != "Tool call failed: workflow dependency unavailable" {
		t.Fatalf("graph failure = (%q, %v), want model-visible feedback", result, err)
	}

	tool.ledger = newProjectAssistantRunEventLedger(messages, scope, "run-graph-failure")
	replayed, err := tool.invokableRun(ctx, "call-graph", `{}`)
	if err != nil || replayed != result {
		t.Fatalf("graph failure replay = (%q, %v), want %q", replayed, err, result)
	}
	if calls != 1 {
		t.Fatalf("graph backend calls = %d, want durable replay without redispatch", calls)
	}
	events := listAssistantRunEventLedgerEvents(t, messages, scope, "run-graph-failure")
	if len(events) != 2 {
		t.Fatalf("graph events = %#v, want call and failed result", events)
	}
	var payload projectAssistantRunToolResultPayload
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Failed || payload.Result != result || payload.Error != backendErr.Error() {
		t.Fatalf("durable graph failure = %#v", payload)
	}
}

func TestProjectAssistantExecCommandRejectsOversizedArgvBeforeApproval(t *testing.T) {
	for _, mode := range []store.AssistantApprovalMode{
		store.AssistantApprovalModeOnRequest,
		store.AssistantApprovalModeAlwaysAsk,
	} {
		t.Run(string(mode), func(t *testing.T) {
			runID := "run-exec-preflight-" + string(mode)
			messages, scope := newAssistantRunEventLedgerTestStore(t, runID)
			tool, err := newProjectAssistantExecCommandGraphTool(projectAssistantWorkflowRunContext{
				ApprovalMode: mode,
				EventLedger:  newProjectAssistantRunEventLedger(messages, scope, runID),
				AdmitMutation: func(context.Context) error {
					return nil
				},
			})
			if err != nil {
				t.Fatalf("create exec tool: %v", err)
			}
			node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{
				Tools:               []einotool.BaseTool{tool},
				ExecuteSequentially: true,
			})
			if err != nil {
				t.Fatalf("create tool node: %v", err)
			}
			arguments, err := json.Marshal(map[string]any{
				"component": "backend",
				"argv":      []string{"node", "-e", strings.Repeat("x", 313)},
			})
			if err != nil {
				t.Fatal(err)
			}
			output, err := node.Invoke(context.Background(), einoschema.AssistantMessage("", []einoschema.ToolCall{{
				ID:   "exec-invalid",
				Type: "function",
				Function: einoschema.FunctionCall{
					Name:      projectToolExecCommand,
					Arguments: string(arguments),
				},
			}}))
			if err != nil {
				t.Fatalf("invalid exec invocation interrupted: %v", err)
			}
			if len(output) != 1 || !strings.Contains(output[0].Content, "invalid arguments") || !strings.Contains(output[0].Content, "argv token 3") {
				t.Fatalf("invalid exec output = %#v, want model-visible validation failure", output)
			}
			events := listAssistantRunEventLedgerEvents(t, messages, scope, runID)
			if len(events) != 2 || events[0].Type != projectAssistantRunToolRequestEventType || events[1].Type != projectAssistantRunToolResultEventType {
				t.Fatalf("invalid exec ledger events = %#v, want request and failed result without approval/admission", events)
			}
		})
	}
}

func TestProjectAssistantExecCommandPreflightPreservesApprovalWithCanonicalArguments(t *testing.T) {
	for _, mode := range []store.AssistantApprovalMode{
		store.AssistantApprovalModeAlwaysAsk,
	} {
		t.Run(string(mode), func(t *testing.T) {
			runID := "run-exec-approval-" + string(mode)
			messages, scope := newAssistantRunEventLedgerTestStore(t, runID)
			tool, err := newProjectAssistantExecCommandGraphTool(projectAssistantWorkflowRunContext{
				ApprovalMode: mode,
				EventLedger:  newProjectAssistantRunEventLedger(messages, scope, runID),
				AdmitMutation: func(context.Context) error {
					t.Fatal("runtime admission must not run before approval")
					return nil
				},
			})
			if err != nil {
				t.Fatalf("create exec tool: %v", err)
			}
			node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{
				Tools:               []einotool.BaseTool{tool},
				ExecuteSequentially: true,
			})
			if err != nil {
				t.Fatalf("create tool node: %v", err)
			}
			arguments := `{"component":"backend","argv":["go","test"]}`
			graph := compose.NewGraph[*einoschema.Message, []*einoschema.Message]()
			if err := graph.AddToolsNode("exec", node); err != nil {
				t.Fatalf("add exec node: %v", err)
			}
			if err := graph.AddEdge(compose.START, "exec"); err != nil {
				t.Fatalf("add graph start: %v", err)
			}
			if err := graph.AddEdge("exec", compose.END); err != nil {
				t.Fatalf("add graph end: %v", err)
			}
			runner, err := graph.Compile(context.Background())
			if err != nil {
				t.Fatalf("compile exec graph: %v", err)
			}
			_, err = runner.Invoke(context.Background(), einoschema.AssistantMessage("", []einoschema.ToolCall{{
				ID:   "exec-valid",
				Type: "function",
				Function: einoschema.FunctionCall{
					Name:      projectToolExecCommand,
					Arguments: arguments,
				},
			}}))
			if err == nil {
				t.Fatal("valid exec invocation completed without approval")
			}
			interrupt, ok := compose.ExtractInterruptInfo(err)
			if !ok || interrupt == nil {
				t.Fatalf("valid exec error = %v, want approval interrupt", err)
			}
			var approval *approvaltool.ApprovalInfo
			for _, context := range interrupt.InterruptContexts {
				if context == nil {
					continue
				}
				switch info := context.Info.(type) {
				case *approvaltool.ApprovalInfo:
					approval = info
				case approvaltool.ApprovalInfo:
					approval = &info
				}
				if approval != nil {
					break
				}
			}
			if approval == nil || approval.ToolName != projectToolExecCommand {
				t.Fatalf("approval info = %#v, want exec_command approval", approval)
			}
			var canonical map[string]any
			if err := json.Unmarshal([]byte(approval.ArgumentsInJSON), &canonical); err != nil {
				t.Fatalf("approval arguments = %q: %v", approval.ArgumentsInJSON, err)
			}
			if canonical["component"] != "backend" || canonical["timeoutSeconds"] != float64(projectAssistantExecDefaultTimeout) {
				t.Fatalf("approval canonical arguments = %#v, want default timeout %d", canonical, projectAssistantExecDefaultTimeout)
			}
			argv, ok := canonical["argv"].([]any)
			if !ok || len(argv) != 2 || argv[0] != "go" || argv[1] != "test" {
				t.Fatalf("approval canonical argv = %#v, want [go test]", canonical["argv"])
			}
			events := listAssistantRunEventLedgerEvents(t, messages, scope, runID)
			if len(events) != 0 {
				t.Fatalf("approval-pending exec ledger events = %#v, want no runtime admission before approval", events)
			}
		})
	}
}

func TestProjectAssistantExecCommandPreflightResumesLegacyInvalidApproval(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprintf("approved_%t", approved), func(t *testing.T) {
			checkpointStore := newProjectEinoAssistantCheckpointStore()
			legacyCalls := 0
			legacyApproval := approvaltool.InvokableApprovableTool{InvokableTool: projectAssistantLegacyExecTool{calls: &legacyCalls}}
			legacyNode, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{
				Tools:               []einotool.BaseTool{legacyApproval},
				ExecuteSequentially: true,
			})
			if err != nil {
				t.Fatalf("create legacy tool node: %v", err)
			}
			legacyGraph := compose.NewGraph[*einoschema.Message, []*einoschema.Message]()
			if err := legacyGraph.AddToolsNode("exec", legacyNode); err != nil {
				t.Fatalf("add legacy exec node: %v", err)
			}
			if err := legacyGraph.AddEdge(compose.START, "exec"); err != nil {
				t.Fatalf("add legacy graph start: %v", err)
			}
			if err := legacyGraph.AddEdge("exec", compose.END); err != nil {
				t.Fatalf("add legacy graph end: %v", err)
			}
			legacyRunner, err := legacyGraph.Compile(context.Background(),
				compose.WithGraphName("legacy-exec-approval"),
				compose.WithCheckPointStore(checkpointStore),
			)
			if err != nil {
				t.Fatalf("compile legacy graph: %v", err)
			}
			invalidArguments := `{"component":"backend","argv":["node","-e","` + strings.Repeat("x", 313) + `"]}`
			_, err = legacyRunner.Invoke(context.Background(), einoschema.AssistantMessage("", []einoschema.ToolCall{{
				ID:   "legacy-exec-invalid",
				Type: "function",
				Function: einoschema.FunctionCall{
					Name:      projectToolExecCommand,
					Arguments: invalidArguments,
				},
			}}), compose.WithCheckPointID("legacy-exec-checkpoint"))
			if err == nil {
				t.Fatal("legacy approval invocation completed without interrupt")
			}
			interrupt, ok := compose.ExtractInterruptInfo(err)
			if !ok || interrupt == nil {
				t.Fatalf("legacy approval error = %v, want approval interrupt", err)
			}
			var approvalID string
			for _, context := range interrupt.InterruptContexts {
				if context == nil {
					continue
				}
				switch context.Info.(type) {
				case *approvaltool.ApprovalInfo, approvaltool.ApprovalInfo:
					approvalID = context.ID
				}
				if approvalID != "" {
					break
				}
			}
			if approvalID == "" {
				t.Fatalf("legacy approval interrupts = %#v, want approval info", interrupt.InterruptContexts)
			}

			messages, scope := newAssistantRunEventLedgerTestStore(t, "run-legacy-exec-approval")
			newTool, err := newProjectAssistantExecCommandGraphTool(projectAssistantWorkflowRunContext{
				ApprovalMode: store.AssistantApprovalModeAlwaysAsk,
				EventLedger:  newProjectAssistantRunEventLedger(messages, scope, "run-legacy-exec-approval"),
				AdmitMutation: func(context.Context) error {
					t.Fatal("legacy invalid approval resumed into runtime admission")
					return nil
				},
			})
			if err != nil {
				t.Fatalf("create current exec tool: %v", err)
			}
			currentNode, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{
				Tools:               []einotool.BaseTool{newTool},
				ExecuteSequentially: true,
			})
			if err != nil {
				t.Fatalf("create current tool node: %v", err)
			}
			currentGraph := compose.NewGraph[*einoschema.Message, []*einoschema.Message]()
			if err := currentGraph.AddToolsNode("exec", currentNode); err != nil {
				t.Fatalf("add current exec node: %v", err)
			}
			if err := currentGraph.AddEdge(compose.START, "exec"); err != nil {
				t.Fatalf("add current graph start: %v", err)
			}
			if err := currentGraph.AddEdge("exec", compose.END); err != nil {
				t.Fatalf("add current graph end: %v", err)
			}
			currentRunner, err := currentGraph.Compile(context.Background(),
				compose.WithGraphName("legacy-exec-approval"),
				compose.WithCheckPointStore(checkpointStore),
			)
			if err != nil {
				t.Fatalf("compile current graph: %v", err)
			}
			resumeCtx := compose.ResumeWithData(context.Background(), approvalID, &approvaltool.ApprovalResult{Approved: approved})
			output, err := currentRunner.Invoke(resumeCtx, nil, compose.WithCheckPointID("legacy-exec-checkpoint"))
			if err != nil {
				t.Fatalf("resume legacy invalid approval (approved=%t): %v", approved, err)
			}
			if len(output) != 1 || !strings.Contains(output[0].Content, "invalid arguments") || !strings.Contains(output[0].Content, "argv token 3") {
				t.Fatalf("legacy invalid approval output (approved=%t) = %#v, want validation failure", approved, output)
			}
			if legacyCalls != 0 {
				t.Fatalf("legacy tool calls = %d, want no execution", legacyCalls)
			}
			events := listAssistantRunEventLedgerEvents(t, messages, scope, "run-legacy-exec-approval")
			if len(events) != 2 || events[0].Type != projectAssistantRunToolRequestEventType || events[1].Type != projectAssistantRunToolResultEventType {
				t.Fatalf("legacy invalid approval ledger events = %#v, want request and failed result", events)
			}
		})
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestProjectAssistantWorkflowToolsAreEinoGraphTools(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	req := projectAssistantRunRequest{
		Identity:       identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		Project:        &aiv1alpha1.Project{},
		WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"},
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	runState := newProjectEinoAssistantRunState()
	tools, err := newProjectEinoAssistantToolsFactory(server)(context.Background(), req, runState)
	if err != nil {
		t.Fatalf("new tools returned error: %v", err)
	}
	for _, toolName := range []string{
		projectToolPlanProjectChanges,
		projectToolCheckProjectReadiness,
		projectToolPrepareProjectDeployment,
		projectToolInspectDevelopmentTemplates,
		projectToolGetRuntimeStatus,
		projectToolGetPreviewURL,
		projectToolVerifyDevelopmentRuntime,
	} {
		tool := einoToolByNameForTest(t, tools, toolName)
		toolType := reflect.TypeOf(tool).String()
		if !strings.Contains(toolType, "graphtool.InvokableGraphTool") {
			t.Fatalf("%s tool type = %s, want Eino graphtool.InvokableGraphTool", toolName, toolType)
		}
	}
}

func TestProjectAssistantInspectDevelopmentTemplatesGraphToolFiltersAndBoundsCatalog(t *testing.T) {
	withDev := applicationTemplateObject()
	_ = unstructured.SetNestedField(withDev.Object, "Simple application", "spec", "displayName")
	_ = unstructured.SetNestedField(withDev.Object, "A development-capable application", "spec", "description")
	_ = unstructured.SetNestedField(withDev.Object, "Use this for a simple app.", "spec", "agent", "usage")
	prodOnly := applicationTemplateObject()
	prodOnly.SetName("database")
	unstructured.RemoveNestedField(prodOnly.Object, "spec", "development")
	broken := applicationTemplateObject()
	broken.SetName("broken")
	unstructured.RemoveNestedField(broken.Object, "spec", "instanceCRD", "kind")

	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	req := projectAssistantRunRequest{
		Identity:       identity{orgUUID: "org-a", workspaceUUID: "ws-1"},
		Client:         asclient.NewFromDynamic(templateCatalogDynamicClient{items: []unstructured.Unstructured{*broken, *prodOnly, *withDev}}),
		Project:        project,
		WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: project.Name, ProjectUID: "test-project-uid"},
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	tools, err := newProjectEinoAssistantToolsFactory(server)(context.Background(), req, newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatalf("new tools returned error: %v", err)
	}
	tool := einoToolByNameForTest(t, tools, projectToolInspectDevelopmentTemplates)
	invokable, ok := tool.(einotool.InvokableTool)
	if !ok {
		t.Fatalf("%T does not implement InvokableTool", tool)
	}
	raw, err := invokable.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun returned error: %v", err)
	}
	var result projectAssistantTemplateInspectionResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	if result.Status != "ok" || len(result.Templates) != 1 {
		t.Fatalf("result = %#v, want one valid template", result)
	}
	candidate := result.Templates[0]
	if candidate.Name != "application" || candidate.DisplayName != "Simple application" || candidate.AgentUsage != "Use this for a simple app." {
		t.Fatalf("candidate = %#v, want surfaced catalog metadata", candidate)
	}
	if len(candidate.Components) != 2 || candidate.Components["frontend"].WorkspacePath != "web" {
		t.Fatalf("components = %#v, want development component map", candidate.Components)
	}
	// The runtime contract is what stops an agent writing a component in a
	// language its sandbox cannot execute, so it must reach the model here —
	// this is the one read that precedes choosing a template.
	backend := candidate.Components["backend"]
	if backend.Toolchain != "node" || backend.StartCommand != "npm run dev || npm start" {
		t.Errorf("backend component = %#v, want the toolchain and start command surfaced", backend)
	}
	if project.Spec.Template != nil {
		t.Fatalf("inspection mutated project template = %#v", project.Spec.Template)
	}
}

func TestProjectAssistantInspectDevelopmentTemplatesGraphToolReturnsEveryEligibleTemplate(t *testing.T) {
	templates := make([]unstructured.Unstructured, 0, 4)
	for _, name := range []string{"zeta", "alpha", "delta", "beta"} {
		template := applicationTemplateObject()
		template.SetName(name)
		templates = append(templates, *template)
	}

	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	req := projectAssistantRunRequest{
		Identity:       identity{orgUUID: "org-a", workspaceUUID: "ws-1"},
		Client:         asclient.NewFromDynamic(templateCatalogDynamicClient{items: templates}),
		Project:        project,
		WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: project.Name, ProjectUID: "test-project-uid"},
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	tools, err := newProjectEinoAssistantToolsFactory(server)(context.Background(), req, newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatalf("new tools returned error: %v", err)
	}
	invokable := einoToolByNameForTest(t, tools, projectToolInspectDevelopmentTemplates).(einotool.InvokableTool)
	raw, err := invokable.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun returned error: %v", err)
	}
	var result projectAssistantTemplateInspectionResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	if len(result.Templates) != 4 {
		t.Fatalf("templates = %#v, want every eligible template", result.Templates)
	}
	got := []string{result.Templates[0].Name, result.Templates[1].Name, result.Templates[2].Name, result.Templates[3].Name}
	if want := []string{"alpha", "beta", "delta", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template names = %#v, want %#v", got, want)
	}
}

func TestProjectAssistantRuntimeStatusRequiresApplicationResponse(t *testing.T) {
	result, err := formatProjectAssistantRuntimeStatusResult(context.Background(), projectAssistantRuntimeWorkflowInput{
		Project:           &aiv1alpha1.Project{},
		RuntimeResolved:   true,
		RuntimeHasBinding: true,
		RuntimePreview:    projectSandboxPreviewURLResponse{Ready: true, PreviewURL: "https://app.example.com"},
	})
	if err != nil {
		t.Fatalf("formatProjectAssistantRuntimeStatusResult returned error: %v", err)
	}
	if result.Status == "ready" {
		t.Fatalf("status = %q, edge reachability must not imply application readiness", result.Status)
	}
	for _, step := range result.NextSteps {
		if strings.Contains(step, projectToolRestartRuntime) {
			t.Fatalf("next steps recommend an unproven runtime restart: %#v", result.NextSteps)
		}
	}
}

func TestProjectAssistantRuntimeProvisioningDoesNotSuggestRestartOrFetchLogs(t *testing.T) {
	reasons := []string{"development_instance_not_found", "development_url_not_ready", previewReasonEdgeProvisioning, "runtime_unavailable"}
	for _, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			input := projectAssistantRuntimeWorkflowInput{
				Project:           &aiv1alpha1.Project{},
				RuntimeResolved:   true,
				RuntimeHasBinding: true,
				RuntimePreview: projectSandboxPreviewURLResponse{
					Reason:  reason,
					Message: "Development environment is still converging.",
				},
			}
			result, err := formatProjectAssistantRuntimeStatusResult(context.Background(), input)
			if err != nil {
				t.Fatalf("formatProjectAssistantRuntimeStatusResult returned error: %v", err)
			}
			if reason == "runtime_unavailable" && result.Status != "unavailable" {
				t.Fatalf("status = %q, want unavailable", result.Status)
			}
			if reason != "runtime_unavailable" && result.Status != "provisioning" {
				t.Fatalf("status = %q, want provisioning", result.Status)
			}
			for _, step := range result.NextSteps {
				if strings.Contains(step, projectToolRestartRuntime) {
					t.Fatalf("next steps recommend an unproven runtime restart: %#v", result.NextSteps)
				}
			}
			if runtimeVerificationShouldCollectLogs(nil, input) {
				t.Fatal("known provisioning state should not fetch diagnostic logs")
			}
		})
	}
}

func TestProjectAssistantRuntimeVerificationCollectsLogsWhenPreviewEdgeIsReachable(t *testing.T) {
	input := projectAssistantRuntimeWorkflowInput{
		Project:           &aiv1alpha1.Project{},
		RuntimeResolved:   true,
		RuntimeHasBinding: true,
		RuntimePreview: projectSandboxPreviewURLResponse{
			Ready:      true,
			PreviewURL: "https://app.example.com",
		},
	}
	if !runtimeVerificationShouldCollectLogs(nil, input) {
		t.Fatal("reachable preview should collect runtime logs for application-level diagnostics")
	}
}

func TestCollectProjectAssistantRuntimeVerificationBrowserConsoleSummarizesWithoutRawEvents(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	signer, err := newEphemeralPreviewConsoleCapabilitySigner()
	if err != nil {
		t.Fatal(err)
	}
	server.previewConsoleEnabled = true
	server.previewConsoleStore = newPreviewConsoleStore()
	server.previewConsoleSigner = signer
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "project-uid-demo"
	project.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}
	id := identity{clusterID: "cluster-1", orgUUID: "org-1", workspaceUUID: "workspace-1", user: "alice@example.com"}
	scope, err := projectPreviewConsoleScope(id, project)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	server.previewConsoleStore.now = func() time.Time { return now }
	const generation = "826e6fa5-c38b-4bdb-8f8f-098198b74f65"
	session, err := server.previewConsoleStore.create(scope, "https://demo.preview.example", "https://console.example", generation, previewConsoleProtocolVersion, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := server.previewConsoleStore.append(session.ID, scope, generation, previewConsoleProtocolVersion, []previewConsoleIncomingEvent{
		{Sequence: 1, DocumentID: generation, Level: "error", Message: "stale error before the source change", ClientTime: now.Format(time.RFC3339Nano), SourceURL: "https://demo.preview.example/"},
	}); err != nil {
		t.Fatal(err)
	}
	fresh := time.Now().UTC().Add(time.Millisecond)
	server.previewConsoleStore.now = func() time.Time { return fresh }
	if _, _, _, err := server.previewConsoleStore.append(session.ID, scope, generation, previewConsoleProtocolVersion, []previewConsoleIncomingEvent{
		{Sequence: 2, DocumentID: generation, Level: "warn", Message: "warning details stay transient", ClientTime: fresh.Format(time.RFC3339Nano), SourceURL: "https://demo.preview.example/"},
		{Sequence: 3, DocumentID: generation, Level: "pageerror", Message: "secret error details stay transient", ClientTime: fresh.Format(time.RFC3339Nano), SourceURL: "https://demo.preview.example/"},
	}); err != nil {
		t.Fatal(err)
	}
	runCtx := projectAssistantWorkflowRunContext{Server: server, Project: project, Identity: id}
	collected, err := collectProjectAssistantRuntimeVerificationBrowserConsole(runCtx)(context.Background(), &projectAssistantRuntimeVerificationContext{RunContext: runCtx})
	if err != nil {
		t.Fatalf("collect browser console returned error: %v", err)
	}
	if collected.BrowserConsole == nil ||
		collected.BrowserConsole.Status != "available" ||
		collected.BrowserConsole.ErrorCount != 2 ||
		collected.BrowserConsole.WarningCount != 1 {
		t.Fatalf("browser console = %#v, want two summarized errors and one warning", collected.BrowserConsole)
	}
	encoded, err := json.Marshal(collected.BrowserConsole)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret error details", "warning details", "stale error", `"events"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("verification summary persisted transient console content %q: %s", forbidden, encoded)
		}
	}
}

func TestPollProjectAssistantRuntimeVerificationWaitsForReady(t *testing.T) {
	calls := 0
	input, result, err := pollProjectAssistantRuntimeVerification(
		context.Background(),
		time.Millisecond,
		50*time.Millisecond,
		func(context.Context) (projectAssistantRuntimeWorkflowInput, *projectAssistantRuntimeWorkflowResult, error) {
			calls++
			status := "provisioning"
			if calls == 3 {
				status = "ready"
			}
			return projectAssistantRuntimeWorkflowInput{}, &projectAssistantRuntimeWorkflowResult{Status: status}, nil
		},
	)
	if err != nil {
		t.Fatalf("poll runtime verification returned error: %v", err)
	}
	if result == nil || result.Status != "ready" {
		t.Fatalf("result = %#v, want ready", result)
	}
	if calls != 3 {
		t.Fatalf("resolver calls = %d, want 3", calls)
	}
	if !reflect.DeepEqual(input, projectAssistantRuntimeWorkflowInput{}) {
		t.Fatalf("input = %#v, want zero test input", input)
	}
}

func TestPollProjectAssistantRuntimeVerificationReturnsProvisioningAtDeadline(t *testing.T) {
	calls := 0
	_, result, err := pollProjectAssistantRuntimeVerification(
		context.Background(),
		time.Millisecond,
		4*time.Millisecond,
		func(context.Context) (projectAssistantRuntimeWorkflowInput, *projectAssistantRuntimeWorkflowResult, error) {
			calls++
			return projectAssistantRuntimeWorkflowInput{}, &projectAssistantRuntimeWorkflowResult{Status: "provisioning"}, nil
		},
	)
	if err != nil {
		t.Fatalf("poll runtime verification returned error: %v", err)
	}
	if result == nil || result.Status != "provisioning" {
		t.Fatalf("result = %#v, want provisioning", result)
	}
	if calls < 2 {
		t.Fatalf("resolver calls = %d, want at least 2", calls)
	}
}

func TestFormatProjectAssistantRuntimeVerificationRejectsBrokenProcessLogs(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		Readiness: &projectAssistantReadinessWorkflowResult{Status: "ready_to_verify"},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			Summary:    "The preview edge is reachable.",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{
			Status:   "failed",
			Blockers: []string{"[api] npm error Missing script: start"},
		},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "not_ready" {
		t.Fatalf("status = %q, want not_ready", result.Status)
	}
	if len(result.Blockers) != 1 {
		t.Fatalf("blockers = %#v, want process failure", result.Blockers)
	}
}

func TestFormatProjectAssistantRuntimeVerificationReportsBrowserConsoleErrorsAsAdvisory(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		Readiness: &projectAssistantReadinessWorkflowResult{Status: "ready_to_verify"},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			Summary:    "The preview edge is reachable.",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{
			Status: "available",
			Lines:  []string{"server listening on port 3000"},
		},
		BrowserConsole: &projectAssistantBrowserConsoleResult{
			Status:     "available",
			Summary:    "1 browser console event(s): 1 pageerror",
			ErrorCount: 1,
		},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "ready" || len(result.Blockers) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("result = %#v, want ready with browser-console warning", result)
	}
	if disposition := projectEinoAssistantRuntimeVerificationDisposition(*result); disposition != projectEinoAssistantVerificationReadyDisposition {
		t.Fatalf("disposition = %q, want ready", disposition)
	}
}

func TestFormatProjectAssistantRuntimeVerificationTreatsDisconnectedBrowserConsoleAsAdvisory(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		Readiness: &projectAssistantReadinessWorkflowResult{Status: "ready_to_verify"},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			Summary:    "The preview edge is reachable.",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{
			Status: "available",
			Lines:  []string{"server listening on port 3000"},
		},
		BrowserConsole: &projectAssistantBrowserConsoleResult{
			Status:  "not_connected",
			Summary: "Browser console evidence is not connected.",
		},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "ready" || len(result.Blockers) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("result = %#v, want ready with one advisory warning", result)
	}
}

func TestFormatInitialProjectRuntimeVerificationRequiresProcessEvidence(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		RequireProcessEvidence: true,
		Readiness:              &projectAssistantReadinessWorkflowResult{Status: "ready_to_verify"},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			Summary:    "The preview edge is reachable.",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{Status: "unavailable"},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "unavailable" || len(result.Blockers) != 1 {
		t.Fatalf("result = %#v, want unavailable process evidence blocker", result)
	}
}

func TestRuntimeVerificationRetriesOneFailedCurrentRevisionSync(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "project-uid-demo"
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1"}
	state := newProjectEinoAssistantRunState()
	revision := state.BeginDevelopmentSyncForNextMutation()
	state.RecordSourceMutation()
	state.CompleteDevelopmentSync(revision, fmt.Errorf("transient sync failure"))

	var attempts atomic.Int32
	server.developmentSyncAfterMutation = func(_ identity, _ *aiv1alpha1.Project, _ string) error {
		attempts.Add(1)
		return nil
	}
	initialize := initializeProjectAssistantRuntimeVerification(projectAssistantWorkflowRunContext{
		Server:   server,
		Project:  project,
		Identity: id,
		RunState: state,
	})
	result, err := initialize(context.Background(), &projectAssistantRuntimeVerificationToolInput{})
	if err != nil {
		t.Fatal(err)
	}
	if result.DevelopmentSyncStatus != "succeeded" || result.DevelopmentSyncFailure != "" {
		t.Fatalf("retry sync evidence = (%q, %q), want succeeded", result.DevelopmentSyncStatus, result.DevelopmentSyncFailure)
	}
	if attempts.Load() != 1 {
		t.Fatalf("sync retry attempts = %d, want one", attempts.Load())
	}
	if checkpoint := state.CheckpointState(); checkpoint.DevelopmentSyncRetry != revision {
		t.Fatalf("checkpoint retry revision = %d, want %d", checkpoint.DevelopmentSyncRetry, revision)
	}
}

func TestRuntimeVerificationAwaitsPendingCurrentRevisionSync(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	revision := state.BeginDevelopmentSyncForNextMutation()
	state.RecordSourceMutation()
	initialize := initializeProjectAssistantRuntimeVerification(projectAssistantWorkflowRunContext{
		RunState: state,
	})

	type outcome struct {
		result *projectAssistantRuntimeVerificationContext
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := initialize(context.Background(), &projectAssistantRuntimeVerificationToolInput{})
		finished <- outcome{result: result, err: err}
	}()

	select {
	case got := <-finished:
		t.Fatalf("verification returned before pending sync completed: result=%#v err=%v", got.result, got.err)
	case <-time.After(20 * time.Millisecond):
	}
	state.CompleteDevelopmentSync(revision, nil)
	select {
	case got := <-finished:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.result.DevelopmentSyncStatus != "succeeded" || got.result.DevelopmentSyncFailure != "" {
			t.Fatalf("sync evidence = (%q, %q), want succeeded", got.result.DevelopmentSyncStatus, got.result.DevelopmentSyncFailure)
		}
	case <-time.After(time.Second):
		t.Fatal("verification did not resume after pending sync completed")
	}
}

func TestFormatProjectAssistantRuntimeVerificationRejectsEmptyProcessLogs(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		CheckedMutationRevision: 4,
		DevelopmentSyncStatus:   "succeeded",
		RequireProcessEvidence:  true,
		Readiness:               &projectAssistantReadinessWorkflowResult{Status: "ready_to_verify"},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{
			Status:  "ok",
			Summary: "No runtime output is available yet; the development process may still be starting.",
		},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "unavailable" || len(result.Blockers) != 1 {
		t.Fatalf("result = %#v, want unavailable empty process evidence", result)
	}
}

func TestFormatProjectAssistantRuntimeVerificationAcceptsStructuredReadyProcessWithoutLogs(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		CheckedMutationRevision: 5,
		DevelopmentSyncStatus:   "succeeded",
		RequireProcessEvidence:  true,
		Readiness:               &projectAssistantReadinessWorkflowResult{Status: "ready_to_verify"},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{
			Status: "ok",
			Processes: map[string]projectAssistantProcessStatus{
				"app": {AttemptID: 2, Configured: true, Running: true, Port: "8080", PortReachable: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "ready" || len(result.Blockers) != 0 {
		t.Fatalf("result = %#v, want ready from structured current-process evidence", result)
	}
}

func TestFormatProjectAssistantRuntimeVerificationRejectsIncompleteComponentEvidence(t *testing.T) {
	incomplete := false
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		RequireProcessEvidence: true,
		Readiness:              &projectAssistantReadinessWorkflowResult{Status: "ready_to_verify"},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{
			Status: "ok",
			Lines:  []string{"backend server starting"},
			Processes: map[string]projectAssistantProcessStatus{
				"frontend": {AttemptID: 2, Configured: true, Running: true, Port: "8080", PortReachable: true},
			},
			ProcessEvidenceComplete: &incomplete,
		},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "unavailable" {
		t.Fatalf("result = %#v, want incomplete multi-component evidence to block completion", result)
	}
}

func TestFormatProjectAssistantRuntimeVerificationRejectsUnreachableDeclaredPort(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		RequireProcessEvidence: true,
		Readiness:              &projectAssistantReadinessWorkflowResult{Status: "ready_to_verify"},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{
			Status: "failed",
			Processes: map[string]projectAssistantProcessStatus{
				"backend": {AttemptID: 3, Configured: true, Running: true, Port: "8080"},
			},
			Blockers: []string{"[backend] development process is not accepting connections on declared port 8080"},
		},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "not_ready" || !strings.Contains(result.Blockers[0], "port 8080") {
		t.Fatalf("result = %#v, want declared-port blocker", result)
	}
}

func TestCurrentRuntimeLogBlockersIgnoreRunningFallbackFailure(t *testing.T) {
	process := projectAssistantProcessStatus{Running: true, Port: "8080", PortReachable: true}
	got := currentProjectAssistantRuntimeLogBlockers(process, projectAssistantRuntimeLogBlockers([]string{
		`npm error Missing script: "dev"`,
		"SyntaxError: Unexpected token",
	}))
	if !reflect.DeepEqual(got, []string{"SyntaxError: Unexpected token"}) {
		t.Fatalf("current blockers = %#v", got)
	}
	exited := currentProjectAssistantRuntimeLogBlockers(
		projectAssistantProcessStatus{Running: false},
		[]string{`npm error Missing script: "dev"`},
	)
	if len(exited) != 1 {
		t.Fatalf("exited-process blockers = %#v, want fallback failure retained", exited)
	}
}

func TestWarmupLogsAreNotPositiveStructuredProcessEvidence(t *testing.T) {
	process := projectAssistantProcessStatus{
		Configured:        true,
		Running:           true,
		Port:              "8080",
		PortWarmupPending: true,
	}
	if projectAssistantComponentHasProcessEvidence(true, process, []string{"backend server starting"}) {
		t.Fatal("ordinary startup logs made a warmup-pending structured process ready")
	}
	process.PortWarmupPending = false
	process.PortReachable = true
	if !projectAssistantComponentHasProcessEvidence(true, process, nil) {
		t.Fatal("reachable structured process was not positive evidence")
	}
	if !projectAssistantComponentHasProcessEvidence(false, projectAssistantProcessStatus{}, []string{"legacy server ready"}) {
		t.Fatal("legacy log-only process lost backward-compatible evidence")
	}
}

func TestPollProjectAssistantProcessStatusWaitsForCurrentAttemptPort(t *testing.T) {
	var calls atomic.Int32
	started := time.Now().UnixMilli()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		_ = json.NewEncoder(w).Encode(projectAssistantProcessStatus{
			AttemptID:               7,
			AttemptStartedUnixMilli: started,
			Configured:              true,
			Running:                 true,
			Port:                    "8080",
			PortReachable:           call >= 3,
		})
	}))
	defer upstream.Close()
	server := &Server{hubBase: upstream.URL}
	process, supported, err := pollProjectAssistantProcessStatusWithTiming(
		context.Background(),
		server,
		identity{clusterID: "root"},
		dataPlaneRef{Resource: "applications", Name: "demo", Component: "backend"},
		100*time.Millisecond,
		5*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("poll process status: %v", err)
	}
	if !supported || !process.PortReachable || process.PortWarmupPending || calls.Load() < 3 {
		t.Fatalf("process = %+v supported=%t calls=%d", process, supported, calls.Load())
	}
}

func TestPollProjectAssistantProcessStatusMarksFirstWarmupTimeoutOperational(t *testing.T) {
	var started atomic.Int64
	started.Store(time.Now().UnixMilli())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(projectAssistantProcessStatus{
			AttemptID:               0,
			AttemptStartedUnixMilli: started.Load(),
			Configured:              true,
			Running:                 true,
			Port:                    "8080",
		})
	}))
	defer upstream.Close()
	server := &Server{hubBase: upstream.URL}
	ref := dataPlaneRef{Resource: "applications", Name: "demo", Component: "backend"}
	process, _, err := pollProjectAssistantProcessStatusWithTiming(
		context.Background(), server, identity{clusterID: "root"}, ref,
		25*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("poll process status: %v", err)
	}
	if !process.PortWarmupPending {
		t.Fatalf("process = %+v, want first same-attempt timeout classified as warmup", process)
	}

	started.Store(time.Now().Add(-time.Second).UnixMilli())
	process, _, err = pollProjectAssistantProcessStatusWithTiming(
		context.Background(), server, identity{clusterID: "root"}, ref,
		25*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("poll old process status: %v", err)
	}
	if process.PortWarmupPending {
		t.Fatalf("process = %+v, old attempt should expose a stable port mismatch", process)
	}
}

func TestFormatProjectAssistantRuntimeVerificationPromotesCleanRevisionToReady(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		CheckedMutationRevision: 7,
		DevelopmentSyncStatus:   "succeeded",
		RequireProcessEvidence:  true,
		Readiness:               &projectAssistantReadinessWorkflowResult{Status: "ready_to_verify"},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			Summary:    "The preview edge is reachable.",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{
			Status: "available",
			Lines:  []string{"server listening on port 3000"},
		},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "ready" {
		t.Fatalf("status = %q, want ready", result.Status)
	}
	if result.CheckedMutationRevision != 7 {
		t.Fatalf("checked revision = %d, want 7", result.CheckedMutationRevision)
	}
	if len(result.Blockers) != 0 {
		t.Fatalf("blockers = %#v, want none", result.Blockers)
	}
	for _, want := range []string{
		"operationally ready",
		"synchronization, process and log health, and preview reachability only",
		"application behavior and acceptance criteria were not independently verified",
	} {
		if !strings.Contains(result.Summary, want) {
			t.Fatalf("summary = %q, want operational verification scope %q", result.Summary, want)
		}
	}
}

func TestFormatProjectAssistantRuntimeVerificationSeparatesRepositoryHandoffFromRuntime(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		CheckedMutationRevision: 4,
		DevelopmentSyncStatus:   "succeeded",
		RequireProcessEvidence:  true,
		Readiness: &projectAssistantReadinessWorkflowResult{
			Status:     "needs_repository",
			Summary:    "Project Demo is waiting for its Git repository to become ready.",
			Repository: &projectAssistantWorkflowRepo{Status: projectRepositoryStatusProvisioning},
		},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{
			Status: "available",
			Lines:  []string{"server listening on port 3000"},
		},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "ready" {
		t.Fatalf("status = %q, want ready", result.Status)
	}
	if len(result.Warnings) != 1 || len(result.Blockers) != 0 {
		t.Fatalf("result = %#v, want one handoff warning and no runtime blockers", result)
	}
	if !strings.Contains(result.Summary, "runtime is operationally ready") || !strings.Contains(result.Summary, "commit and CI") ||
		!strings.Contains(result.Summary, "does not independently verify application behavior or acceptance criteria") {
		t.Fatalf("summary = %q, want separate runtime and repository status", result.Summary)
	}
}

func TestFormatProjectAssistantRuntimeVerificationDoesNotHideProcessFailureBehindRepositoryHandoff(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		Readiness: &projectAssistantReadinessWorkflowResult{
			Status:     "needs_repository",
			Repository: &projectAssistantWorkflowRepo{Status: projectRepositoryStatusProvisioning},
		},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{
			Status:   "failed",
			Blockers: []string{"SyntaxError: Unexpected token"},
		},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "not_ready" || len(result.Blockers) != 1 || len(result.Warnings) != 0 {
		t.Fatalf("result = %#v, want process failure to remain blocking", result)
	}
}

func TestFormatProjectAssistantRuntimeVerificationTreatsProjectContextAsHandoffWarning(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		CheckedMutationRevision: 2,
		DevelopmentSyncStatus:   "succeeded",
		Readiness:               &projectAssistantReadinessWorkflowResult{Status: "needs_workspace_context"},
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{Status: "available"},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "ready" {
		t.Fatalf("status = %q, want ready", result.Status)
	}
	if len(result.Blockers) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("result = %#v, want one handoff warning and no operational blocker", result)
	}
}

func TestCollectProjectAssistantRuntimeReadinessRefreshesLiveRepositoryState(t *testing.T) {
	current := projectWithRepository("demo-repo", "demo", "github")
	current.Name = "demo"
	current.UID = "project-uid-demo"
	current.Spec.DisplayName = "Demo"
	current.Spec.Memory.Requirements = []string{"Show a working page"}
	projectObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(current)
	if err != nil {
		t.Fatalf("convert project to unstructured: %v", err)
	}
	projectResource := &unstructured.Unstructured{Object: projectObject}
	projectResource.SetAPIVersion(aiv1alpha1.SchemeGroupVersion.String())
	projectResource.SetKind("Project")
	client := asclient.NewFromDynamic(fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			asclient.ProjectGVR:      "ProjectList",
			codeConnectionsGVR:       "ConnectionList",
			codeRepositoriesGVR:      "RepositoryList",
			codeRepositoryCommitsGVR: "RepositoryCommitList",
		},
		projectResource,
		codeRepositoryObject("demo-repo", "demo", "github", true),
		codeConnectionObject("github"),
	))
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: current.Name, ProjectUID: "test-project-uid"}
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "index.html", Content: "<main>ready</main>\n"}); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	stale := current.DeepCopy()
	runCtx := projectAssistantWorkflowRunContext{
		Server:         NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false),
		Client:         client,
		Project:        stale,
		Repository:     &ProjectRepositoryView{Ref: "demo-repo", Status: projectRepositoryStatusProvisioning},
		WorkspaceScope: scope,
	}
	input := &projectAssistantRuntimeVerificationContext{
		Args: &projectAssistantRuntimeVerificationToolInput{},
	}
	refreshed, err := collectProjectAssistantRuntimeReadiness(runCtx)(context.Background(), input)
	if err != nil {
		t.Fatalf("collect runtime readiness returned error: %v", err)
	}
	if refreshed.Readiness == nil ||
		refreshed.Readiness.Status != "ready_to_verify" ||
		refreshed.Readiness.Repository == nil ||
		refreshed.Readiness.Repository.Status != projectRepositoryStatusReady {
		t.Fatalf("readiness = %#v, want live ready repository rather than stale provisioning snapshot", refreshed.Readiness)
	}
}

func TestFormatProjectAssistantReadinessUsesConversationalRepositorySummary(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.Spec.DisplayName = "Six String Shop"
	project.Spec.Memory.Requirements = []string{"Show a working page"}
	result, err := formatProjectAssistantReadinessWorkflowResult(context.Background(), projectAssistantWorkflowContext{
		Project:        project,
		Repository:     &ProjectRepositoryView{Ref: "demo-repo", Status: projectRepositoryStatusProvisioning},
		WorkspaceFiles: []string{"index.html"},
	})
	if err != nil {
		t.Fatalf("format readiness returned error: %v", err)
	}
	if result.Status != "needs_repository" ||
		result.Summary != "Project Six String Shop is waiting for its Git repository to become ready." {
		t.Fatalf("result = %#v, want conversational provisioning summary", result)
	}
}

func TestProjectAssistantRuntimeLogBlockersDetectSyntaxAndMissingScript(t *testing.T) {
	for _, line := range []string{
		"SyntaxError: Unexpected token",
		"npm error Missing script: start",
	} {
		if blockers := projectAssistantRuntimeLogBlockers([]string{line}); len(blockers) != 1 {
			t.Fatalf("blockers for %q = %#v, want one", line, blockers)
		}
	}
	if blockers := projectAssistantRuntimeLogBlockers([]string{"server listening on port 3000"}); len(blockers) != 0 {
		t.Fatalf("healthy blockers = %#v, want none", blockers)
	}
}

func TestProjectAssistantVerifyRuntimeGraphToolReturnsReadinessAndNoLogsWithoutBinding(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Spec.DisplayName = "Demo"
	project.Spec.Memory.Requirements = []string{"Show a working page"}
	repo := &ProjectRepositoryView{Ref: "demo", Name: "demo", Status: projectRepositoryStatusReady}
	resultRaw := invokeProjectAssistantWorkflowGraphTool(t, server, identity{orgUUID: "org-a", workspaceUUID: "ws-1"}, projectToolVerifyDevelopmentRuntime, project, repo, workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: project.Name, ProjectUID: "test-project-uid"}, map[string]any{})
	var result projectAssistantRuntimeVerificationResult
	if err := json.Unmarshal([]byte(resultRaw), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, resultRaw)
	}
	if result.Readiness == nil || result.Readiness.Status != "needs_workspace_context" {
		t.Fatalf("readiness = %#v, want workspace context requirement", result.Readiness)
	}
	if result.Runtime == nil || result.Runtime.Status != "not_configured" {
		t.Fatalf("runtime = %#v, want not_configured without runtime client/binding", result.Runtime)
	}
	if result.Logs != nil {
		t.Fatalf("logs = %#v, want no data-plane log call without binding", result.Logs)
	}
}

func TestProjectAssistantVerifyRuntimeAlwaysCollectsWorkspaceEvidence(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Spec.DisplayName = "Demo"
	project.Spec.Memory.Requirements = []string{"Show a working page"}
	scope := projectWorkspaceScope(id, project)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{
		Path:    "web/index.html",
		Content: "<main>ready</main>\n",
	}); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	repo := &ProjectRepositoryView{Ref: "demo", Name: "demo", Status: projectRepositoryStatusReady}

	// Legacy or hallucinated file-list arguments must not disable evidence
	// required by verification, even though they are no longer in the schema.
	resultRaw := invokeProjectAssistantWorkflowGraphTool(
		t,
		server,
		id,
		projectToolVerifyDevelopmentRuntime,
		project,
		repo,
		scope,
		map[string]any{"includeFiles": false, "maxFiles": 1},
	)
	var result projectAssistantRuntimeVerificationResult
	if err := json.Unmarshal([]byte(resultRaw), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, resultRaw)
	}
	if result.Readiness == nil || result.Readiness.Status != "ready_to_verify" {
		t.Fatalf("readiness = %#v, want required workspace evidence", result.Readiness)
	}
	if !stringSliceContains(result.Readiness.Files, "web/index.html") {
		t.Fatalf("readiness files = %#v, want bounded workspace evidence", result.Readiness.Files)
	}
}

func TestProjectAssistantVerifyRuntimeSchemaDoesNotExposeWorkspaceEvidenceControls(t *testing.T) {
	spec, ok := projectAssistantWorkflowToolSpec(projectToolVerifyDevelopmentRuntime)
	if !ok {
		t.Fatalf("%s tool missing", projectToolVerifyDevelopmentRuntime)
	}
	var parameters struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(spec.Parameters, &parameters); err != nil {
		t.Fatalf("decode parameters: %v", err)
	}
	for _, name := range []string{"includeFiles", "maxFiles"} {
		if _, exposed := parameters.Properties[name]; exposed {
			t.Fatalf("%s schema exposes %q, which can disable required verification evidence", projectToolVerifyDevelopmentRuntime, name)
		}
	}
	for _, name := range []string{"includeLogs", "tailLines"} {
		if _, exposed := parameters.Properties[name]; !exposed {
			t.Fatalf("%s schema is missing optional diagnostic control %q", projectToolVerifyDevelopmentRuntime, name)
		}
	}

	graphTool, err := newProjectAssistantVerifyRuntimeGraphTool(projectAssistantWorkflowRunContext{})
	if err != nil {
		t.Fatalf("create graph tool: %v", err)
	}
	info, err := graphTool.Info(context.Background())
	if err != nil {
		t.Fatalf("read graph tool info: %v", err)
	}
	generated, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("generate graph tool schema: %v", err)
	}
	for _, name := range []string{"includeFiles", "maxFiles"} {
		if _, exposed := generated.Properties.Get(name); exposed {
			t.Fatalf("generated %s schema exposes %q", projectToolVerifyDevelopmentRuntime, name)
		}
	}
	for _, name := range []string{"includeLogs", "tailLines"} {
		if _, exposed := generated.Properties.Get(name); !exposed {
			t.Fatalf("generated %s schema is missing %q", projectToolVerifyDevelopmentRuntime, name)
		}
	}
}

func einoToolByNameForTest(t *testing.T, tools []einotool.BaseTool, name string) einotool.BaseTool {
	t.Helper()
	for _, tool := range tools {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatalf("tool Info returned error: %v", err)
		}
		if info.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %s not found", name)
	return nil
}

func TestProjectAssistantWorkflowRegisteredReadOnly(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	spec, ok := registry.Spec(projectToolPlanProjectChanges)
	if !ok {
		t.Fatal("plan_project_changes tool missing from registry")
	}
	if spec.Risk != projectAssistantToolRiskRead {
		t.Fatalf("risk = %q, want read", spec.Risk)
	}
	if got := projectAssistantPermissionForV2(spec, store.AssistantApprovalModeAlwaysAsk, nil, nil, false); got != projectAssistantPermissionAllow {
		t.Fatalf("permission = %q, want allow", got)
	}
	if strings.TrimSpace(string(spec.Parameters)) == "" {
		t.Fatal("workflow tool parameters are empty")
	}
}

func TestProjectAssistantReadinessWorkflowRegisteredReadOnly(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	spec, ok := registry.Spec(projectToolCheckProjectReadiness)
	if !ok {
		t.Fatal("check_project_readiness tool missing from registry")
	}
	if spec.Risk != projectAssistantToolRiskRead {
		t.Fatalf("risk = %q, want read", spec.Risk)
	}
	if got := projectAssistantPermissionForV2(spec, store.AssistantApprovalModeAlwaysAsk, nil, nil, false); got != projectAssistantPermissionAllow {
		t.Fatalf("permission = %q, want allow", got)
	}
	if strings.TrimSpace(string(spec.Parameters)) == "" {
		t.Fatal("readiness workflow tool parameters are empty")
	}
}

func TestProjectAssistantPrepareDeploymentWorkflowRegisteredReadOnly(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	spec, ok := registry.Spec(projectToolPrepareProjectDeployment)
	if !ok {
		t.Fatal("prepare_project_deployment tool missing from registry")
	}
	if spec.Risk != projectAssistantToolRiskRead {
		t.Fatalf("risk = %q, want read", spec.Risk)
	}
	if got := projectAssistantPermissionForV2(spec, store.AssistantApprovalModeAlwaysAsk, nil, nil, false); got != projectAssistantPermissionAllow {
		t.Fatalf("permission = %q, want allow", got)
	}
	if strings.TrimSpace(string(spec.Parameters)) == "" {
		t.Fatal("prepare deployment workflow tool parameters are empty")
	}
}

func TestProjectAssistantRuntimeWorkflowToolsRegistered(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	tests := []struct {
		name       string
		wantRisk   projectAssistantToolRisk
		wantPerm   projectAssistantPermissionDecision
		wantBundle projectAssistantToolBundle
	}{
		{name: "get_runtime_status", wantRisk: projectAssistantToolRiskRead, wantPerm: projectAssistantPermissionAllow, wantBundle: projectAssistantToolBundleRuntime},
		{name: "get_preview_url", wantRisk: projectAssistantToolRiskRead, wantPerm: projectAssistantPermissionAllow, wantBundle: projectAssistantToolBundleRuntime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, ok := registry.Spec(tt.name)
			if !ok {
				t.Fatalf("%s tool missing from registry", tt.name)
			}
			if spec.Risk != tt.wantRisk {
				t.Fatalf("risk = %q, want %q", spec.Risk, tt.wantRisk)
			}
			if got := projectAssistantPermissionForV2(spec, store.AssistantApprovalModeAlwaysAsk, nil, nil, false); got != tt.wantPerm {
				t.Fatalf("permission = %q, want %q", got, tt.wantPerm)
			}
			if got := projectAssistantToolBundleForSpec(spec); got != tt.wantBundle {
				t.Fatalf("bundle = %q, want %q", got, tt.wantBundle)
			}
			if strings.TrimSpace(string(spec.Parameters)) == "" {
				t.Fatalf("%s parameters are empty", tt.name)
			}
		})
	}
}

func TestProjectAssistantWorkflowPlansFromMemoryRepositoryAndWorkspace(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Spec.DisplayName = "Demo App"
	project.Spec.Memory = aiv1alpha1.ProjectMemory{
		Goals:        []string{"ship a task tracker"},
		Requirements: []string{"persist tasks"},
		Constraints:  []string{"avoid external queues"},
	}
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "src/App.tsx", Content: "export function App() { return null }\n"}); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	raw := invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPlanProjectChanges, project, &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady}, scope, map[string]any{"includeFiles": true})
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var plan projectAssistantWorkflowPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if !strings.Contains(plan.Summary, "Demo App") {
		t.Fatalf("summary = %q, want project display name", plan.Summary)
	}
	if !containsString(plan.Goals, "ship a task tracker") || !containsString(plan.Requirements, "persist tasks") || !containsString(plan.Constraints, "avoid external queues") {
		t.Fatalf("plan memory = %#v, want project memory copied", plan)
	}
	if plan.Repository == nil || plan.Repository.Ref != "demo-repo" || plan.Repository.Status != projectRepositoryStatusReady {
		t.Fatalf("repository = %#v, want ready demo-repo", plan.Repository)
	}
	if !containsString(plan.Files, "src/App.tsx") {
		t.Fatalf("files = %#v, want workspace file", plan.Files)
	}
	if len(plan.Steps) == 0 {
		t.Fatalf("steps = %#v, want at least one deterministic next step", plan.Steps)
	}
	steps := strings.Join(plan.Steps, "\n")
	if strings.Contains(steps, "commit_project_files") || strings.Contains(steps, "Defer commit handoff") {
		t.Fatalf("steps = %#v, want no manufactured commit obligation", plan.Steps)
	}
}

func TestProjectAssistantReadinessWorkflowReportsContextWithoutTrace(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Spec.DisplayName = "Demo App"
	project.Spec.Memory.Requirements = []string{"ship a tested build"}
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "package.json", Content: `{"scripts":{"build":"vite build","test":"vitest"}}`}); err != nil {
		t.Fatalf("WriteFile package.json returned error: %v", err)
	}
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "src/App.tsx", Content: "export function App() { return null }\n"}); err != nil {
		t.Fatalf("WriteFile src/App.tsx returned error: %v", err)
	}
	raw := invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolCheckProjectReadiness, project, &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady}, scope, map[string]any{})
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var readiness struct {
		Status            string   `json:"status"`
		Summary           string   `json:"summary"`
		RecommendedChecks []string `json:"recommendedChecks"`
	}
	if err := json.Unmarshal([]byte(raw), &readiness); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if readiness.Status != "ready_to_verify" {
		t.Fatalf("status = %q, want ready_to_verify", readiness.Status)
	}
	if !strings.Contains(readiness.Summary, "Demo App") {
		t.Fatalf("summary = %q, want project display name", readiness.Summary)
	}
	if !containsString(readiness.RecommendedChecks, "build") || !containsString(readiness.RecommendedChecks, "test") {
		t.Fatalf("recommended checks = %#v, want build and test", readiness.RecommendedChecks)
	}
	if strings.Contains(raw, `"trace"`) {
		t.Fatalf("raw = %s, want no user-facing workflow trace", raw)
	}
}

func TestProjectAssistantPrepareDeploymentWorkflowReportsBuildAndRuntimeReadiness(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Spec.DisplayName = "Demo App"
	project.Spec.Memory.Requirements = []string{"ship a tested build"}
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "package.json", Content: `{"scripts":{"build":"vite build","test":"vitest"}}`}); err != nil {
		t.Fatalf("WriteFile package.json returned error: %v", err)
	}
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "src/App.tsx", Content: "export function App() { return null }\n"}); err != nil {
		t.Fatalf("WriteFile src/App.tsx returned error: %v", err)
	}
	raw := invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPrepareProjectDeployment, project, &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady}, scope, map[string]any{})
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var prepared projectAssistantDeploymentPreparationResult
	if err := json.Unmarshal([]byte(raw), &prepared); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if prepared.Status != "ready_for_build" {
		t.Fatalf("status = %q, want ready_for_build", prepared.Status)
	}
	if prepared.Artifact == nil || prepared.Artifact.Type != "oci-image" || prepared.Artifact.Source != "app-studio-build" || prepared.Artifact.Status != "required" {
		t.Fatalf("artifact = %#v, want required App Studio OCI image build artifact", prepared.Artifact)
	}
	if prepared.Runtime == nil || prepared.Runtime.Status != "not_configured" {
		t.Fatalf("runtime = %#v, want not_configured runtime handoff", prepared.Runtime)
	}
	if !containsString(prepared.RecommendedChecks, "build") || !containsString(prepared.RecommendedChecks, "test") {
		t.Fatalf("recommended checks = %#v, want build and test", prepared.RecommendedChecks)
	}
	if !containsString(prepared.Files, "package.json") || !containsString(prepared.Files, "src/App.tsx") {
		t.Fatalf("files = %#v, want workspace files", prepared.Files)
	}
	if len(prepared.Blockers) != 0 {
		t.Fatalf("blockers = %#v, want none before runtime handoff", prepared.Blockers)
	}
	if !containsString(prepared.NextSteps, "Build an OCI image for the current workspace before runtime deployment.") {
		t.Fatalf("next steps = %#v, want OCI build step", prepared.NextSteps)
	}
}

func TestProjectAssistantPrepareDeploymentWorkflowReportsBlockers(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	raw := invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPrepareProjectDeployment, project, nil, projectWorkspaceScope(id, project), map[string]any{"includeFiles": false})
	var prepared projectAssistantDeploymentPreparationResult
	if err := json.Unmarshal([]byte(raw), &prepared); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if prepared.Status != "blocked" {
		t.Fatalf("status = %q, want blocked", prepared.Status)
	}
	if !containsString(prepared.Blockers, "Project requirements are missing.") || !containsString(prepared.Blockers, "Managed repository is not ready.") {
		t.Fatalf("blockers = %#v, want requirements and repository blockers", prepared.Blockers)
	}
}

func TestProjectAssistantWorkflowDoesNotMutateWorkspace(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "README.md", Content: "# Demo\n"}); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	before, err := workspaces.ListFiles(context.Background(), scope, workspace.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles before returned error: %v", err)
	}
	invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPlanProjectChanges, project, nil, scope, map[string]any{"includeFiles": true})
	after, err := workspaces.ListFiles(context.Background(), scope, workspace.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles after returned error: %v", err)
	}
	if strings.Join(workflowTestFilePaths(before.Files), "\n") != strings.Join(workflowTestFilePaths(after.Files), "\n") {
		t.Fatalf("files changed from %#v to %#v", before.Files, after.Files)
	}
}

func TestProjectAssistantPrepareDeploymentWorkflowDoesNotMutateWorkspace(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "README.md", Content: "# Demo\n"}); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	before, err := workspaces.ListFiles(context.Background(), scope, workspace.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles before returned error: %v", err)
	}
	invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPrepareProjectDeployment, project, nil, scope, map[string]any{})
	after, err := workspaces.ListFiles(context.Background(), scope, workspace.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles after returned error: %v", err)
	}
	if strings.Join(workflowTestFilePaths(before.Files), "\n") != strings.Join(workflowTestFilePaths(after.Files), "\n") {
		t.Fatalf("files changed from %#v to %#v", before.Files, after.Files)
	}
}

func TestProjectAssistantRuntimeStatusAndPreviewWorkflowsReportNotConfiguredWithoutSessionRuntime(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	for _, name := range []string{"get_runtime_status", "get_preview_url"} {
		t.Run(name, func(t *testing.T) {
			id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
			project := projectWithRepository("demo-repo", "demo", "github")
			result := invokeProjectAssistantWorkflowGraphTool(t, server, id, name, project, nil, projectWorkspaceScope(id, project), map[string]any{})
			var decoded map[string]any
			if err := json.Unmarshal([]byte(result), &decoded); err != nil {
				t.Fatalf("decode result: %v\n%s", err, result)
			}
			if got := projectToolString(decoded["status"]); got != "not_configured" {
				t.Fatalf("status = %q, want not_configured", got)
			}
			if got := projectToolString(decoded["previewURL"]); got != "" {
				t.Fatalf("previewURL = %q, want empty without runtime session", got)
			}
			if !containsString(projectToolStringList(decoded["blockers"]), "Runtime provider is not configured.") {
				t.Fatalf("blockers = %#v, want runtime provider blocker", decoded["blockers"])
			}
		})
	}
}

func TestProjectAssistantPreviewURLWorkflowIgnoresInternalAppStudioPreviewPath(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Status.Environments = []aiv1alpha1.ProjectEnvironmentStatus{{
		Name: "development",
		Bindings: []aiv1alpha1.ProjectProviderBindingStatus{{
			Name:       "dev",
			Provider:   "app-studio",
			PreviewURL: "/services/providers/app-studio/api/projects/demo/preview/",
		}},
	}}
	result, err := formatProjectAssistantPreviewURLResult(context.Background(), projectAssistantRuntimeWorkflowInput{Project: project})
	if err != nil {
		t.Fatalf("formatProjectAssistantPreviewURLResult returned error: %v", err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	if got := projectToolString(decoded["status"]); got != "not_configured" {
		t.Fatalf("status = %q, want not_configured", got)
	}
	if got := projectToolString(decoded["previewURL"]); got != "" {
		t.Fatalf("previewURL = %q, want empty for internal app-studio preview path", got)
	}
	runtime, ok := decoded["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("runtime = %#v, want object", decoded["runtime"])
	}
	if got := projectToolString(runtime["url"]); got != "" {
		t.Fatalf("runtime.url = %q, want empty for internal app-studio preview path", got)
	}
}

func TestProjectAssistantPreviewURLWorkflowReturnsExternalPreviewURL(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Status.Environments = []aiv1alpha1.ProjectEnvironmentStatus{{
		Name: "development",
		Bindings: []aiv1alpha1.ProjectProviderBindingStatus{{
			Name:       "preview-route",
			Provider:   "app-studio",
			PreviewURL: "https://demo.preview.example.com/",
		}},
	}}
	result, err := formatProjectAssistantPreviewURLResult(context.Background(), projectAssistantRuntimeWorkflowInput{Project: project})
	if err != nil {
		t.Fatalf("formatProjectAssistantPreviewURLResult returned error: %v", err)
	}
	if got, want := result.PreviewURL, "https://demo.preview.example.com/"; got != want {
		t.Fatalf("PreviewURL = %q, want %q", got, want)
	}
	if result.Runtime == nil || result.Runtime.URL != "https://demo.preview.example.com/" {
		t.Fatalf("Runtime = %#v, want external preview URL", result.Runtime)
	}
}

func TestProjectAssistantWorkflowBoundsLargeResultAsJSON(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Spec.DisplayName = strings.Repeat("Demo App ", 80)
	for i := 0; i < 80; i++ {
		project.Spec.Memory.Goals = append(project.Spec.Memory.Goals, strings.Repeat("goal ", 80))
		project.Spec.Memory.Requirements = append(project.Spec.Memory.Requirements, strings.Repeat("requirement ", 80))
		project.Spec.Memory.Constraints = append(project.Spec.Memory.Constraints, strings.Repeat("constraint ", 80))
	}
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	raw := invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPlanProjectChanges, project, nil, projectWorkspaceScope(id, project), map[string]any{"includeFiles": false})
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var plan projectAssistantWorkflowPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("bounded workflow result is not JSON: %v\n%s", err, raw)
	}
	if len(plan.Steps) == 0 {
		t.Fatalf("steps = %#v, want bounded guidance", plan.Steps)
	}
}

func invokeProjectAssistantWorkflowGraphTool(t *testing.T, server *Server, id identity, toolName string, project *aiv1alpha1.Project, repository *ProjectRepositoryView, scope workspace.Scope, args map[string]any) string {
	t.Helper()
	req := projectAssistantRunRequest{
		Identity:       id,
		Project:        project,
		Repository:     repository,
		WorkspaceScope: scope,
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	runState := newProjectEinoAssistantRunState()
	tools, err := newProjectEinoAssistantToolsFactory(server)(context.Background(), req, runState)
	if err != nil {
		t.Fatalf("new tools returned error: %v", err)
	}
	tool := einoToolByNameForTest(t, tools, toolName)
	invokable, ok := tool.(einotool.InvokableTool)
	if !ok {
		t.Fatalf("%s tool does not implement Eino InvokableTool", toolName)
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode tool args: %v", err)
	}
	result, err := invokable.InvokableRun(context.Background(), string(rawArgs))
	if err != nil {
		t.Fatalf("%s InvokableRun returned error: %v", toolName, err)
	}
	return result
}

func workflowTestFilePaths(files []workspace.FileInfo) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, file.Path)
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// A template's agent.usage states its DEVELOPMENT MODE contract several
// thousand characters in. The old 160-char bound cut it off mid-second-sentence,
// so agents chose templates and wrote code having never seen which runtime the
// sandbox would execute. Anything that shortens this again reintroduces that.
func TestFilterProjectAssistantDevelopmentTemplatesCarriesFullAgentUsage(t *testing.T) {
	usage := strings.Repeat("Production guidance that buries the runtime contract. ", 70) +
		"DEVELOPMENT MODE: each tier runs a Node.js dev server; write package.json."
	if len(usage) < 3500 {
		t.Fatalf("fixture usage is %d chars, too short to exercise the bound", len(usage))
	}
	obj := applicationTemplateObject()
	_ = unstructured.SetNestedField(obj.Object, usage, "spec", "agent", "usage")

	result, err := filterProjectAssistantDevelopmentTemplates(
		context.Background(),
		&projectAssistantTemplateCatalog{Items: []unstructured.Unstructured{*obj}},
	)
	if err != nil {
		t.Fatalf("filterProjectAssistantDevelopmentTemplates: %v", err)
	}
	if len(result.Templates) != 1 {
		t.Fatalf("templates = %d, want 1", len(result.Templates))
	}
	if got := result.Templates[0].AgentUsage; got != usage {
		t.Errorf("agentUsage was truncated at %d of %d chars — the development contract must survive intact", len(got), len(usage))
	}
	if strings.Contains(result.Summary, "shortened") {
		t.Errorf("summary claims truncation for a catalog that fits: %q", result.Summary)
	}
}

// The aggregate budget exists only for an unusually large catalog, and even
// then it must shorten rather than drop: a dropped template is a choice the
// agent never learns exists.
func TestFilterProjectAssistantDevelopmentTemplatesBoundsOversizedCatalog(t *testing.T) {
	usage := strings.Repeat("x", projectAssistantTemplateUsageChars)
	items := make([]unstructured.Unstructured, 0, 40)
	for i := range 40 {
		obj := applicationTemplateObject()
		obj.SetName(fmt.Sprintf("template-%02d", i))
		_ = unstructured.SetNestedField(obj.Object, usage, "spec", "agent", "usage")
		items = append(items, *obj)
	}

	result, err := filterProjectAssistantDevelopmentTemplates(
		context.Background(),
		&projectAssistantTemplateCatalog{Items: items},
	)
	if err != nil {
		t.Fatalf("filterProjectAssistantDevelopmentTemplates: %v", err)
	}
	if len(result.Templates) != 40 {
		t.Fatalf("templates = %d, want all 40 kept", len(result.Templates))
	}
	raw, err := json.Marshal(result.Templates)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(raw) > projectAssistantTemplateInspectionMaxBytes {
		t.Errorf("encoded templates = %d bytes, want <= %d", len(raw), projectAssistantTemplateInspectionMaxBytes)
	}
	if !strings.Contains(result.Summary, "shortened") {
		t.Errorf("summary = %q, want it to tell the model to fetch the full contract", result.Summary)
	}
}
