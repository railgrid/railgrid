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
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestProjectEinoDeferredLocalAndWorkflowToolsRemainDiscoverable(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "local-demand-loading")
	h.req.TurnProfile = projectAssistantTurnProfileImplementation
	h.req.TurnPolicy = projectAssistantTurnPolicyForProfile(h.req.TurnProfile)
	discovery := projectEinoAssistantDiscoverTools(context.Background(), h.server, h.req)
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(h.req.TurnPolicy)
	state.SetAgentOptimizationMode(projectEinoAssistantOptimizationCodexPOC)
	state.SetToolDiscovery(discovery)
	lifecycle := projectEinoAssistantLifecycleMiddleware(h.req, state, h.server).(*projectEinoAssistantLifecycle)
	model := &adk.ChatModelAgentState{}
	if err := lifecycle.refreshExecutableToolContext(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolReadFile, true)
	assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolEditFile, true)
	assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolExecCommand, true)
	assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectEinoAssistantToolSearchTool, true)
	assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolGetRuntimeStatus, false)
	assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolRestartRuntime, false)
	initialCount := len(model.ToolInfos)
	tools, err := projectEinoAssistantToolsForDiscovery(context.Background(), h.server, h.req, state, discovery)
	if err != nil {
		t.Fatal(err)
	}
	statusTool := projectEinoAssistantTestToolByName(t, tools, projectToolGetRuntimeStatus)
	guardedStatusTool, ok := statusTool.(projectEinoAssistantDynamicSelectionRequired)
	if !ok || !guardedStatusTool.RequiresDynamicToolSelection() {
		t.Fatalf("runtime workflow tool %T has no selection guard", statusTool)
	}
	invokableStatusTool, ok := statusTool.(einotool.InvokableTool)
	if !ok {
		t.Fatalf("runtime workflow tool %T is not invokable", statusTool)
	}
	blocked, err := invokableStatusTool.InvokableRun(context.Background(), `{}`)
	if err != nil || !strings.Contains(blocked, "call tool_search first") {
		t.Fatalf("unselected runtime workflow result = %q, error = %v", blocked, err)
	}
	matches := projectEinoAssistantSearchDynamicToolSpecs(projectEinoAssistantDynamicToolSpecs(h.server, h.req, discovery), projectToolGetRuntimeStatus, 1)
	if len(matches) != 1 || matches[0].Name != projectToolGetRuntimeStatus {
		t.Fatalf("runtime search lost capability: %#v", matches)
	}
	selection, err := json.Marshal(projectEinoAssistantToolSearchResult{CatalogDigest: projectEinoAssistantDynamicToolCatalogDigest(discovery), Matches: matches})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.ApplyDynamicToolSearchResult(string(selection)); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.refreshExecutableToolContext(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolGetRuntimeStatus, true)
	assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolRestartRuntime, false)
	if len(model.ToolInfos) != initialCount+1 {
		t.Fatalf("search loaded unselected schemas: before=%d after=%d", initialCount, len(model.ToolInfos))
	}
}

func TestProjectEinoDeferredWorkflowToolsRemainVisibleWhenOptimizationIsOff(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "workflow-demand-loading-off")
	h.req.TurnProfile = projectAssistantTurnProfileImplementation
	h.req.TurnPolicy = projectAssistantTurnPolicyForProfile(h.req.TurnProfile)
	discovery := projectEinoAssistantDiscoverTools(context.Background(), h.server, h.req)
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(h.req.TurnPolicy)
	state.SetAgentOptimizationMode("")
	state.SetToolDiscovery(discovery)
	lifecycle := projectEinoAssistantLifecycleMiddleware(h.req, state, h.server).(*projectEinoAssistantLifecycle)
	model := &adk.ChatModelAgentState{}
	if err := lifecycle.refreshExecutableToolContext(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolGetRuntimeStatus, true)
	assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolRestartRuntime, true)
	assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectEinoAssistantToolSearchTool, false)
}

func TestProjectEinoDeferredWorkflowToolsRespectPlanAndReviewModes(t *testing.T) {
	for _, mode := range []projectAssistantCollaborationMode{projectAssistantCollaborationModePlan, projectAssistantCollaborationModeReview} {
		t.Run(string(mode), func(t *testing.T) {
			h := newProjectAssistantV2ToolHarness(t, "workflow-demand-loading-"+string(mode))
			h.req.TurnProfile = projectAssistantTurnProfileImplementation
			h.req.TurnPolicy = projectAssistantTurnPolicyForProfile(h.req.TurnProfile)
			h.req.CollaborationMode = mode
			discovery := projectEinoAssistantDiscoverTools(context.Background(), h.server, h.req)
			state := newProjectEinoAssistantRunState()
			state.SetTurnPolicy(h.req.TurnPolicy)
			state.SetAgentOptimizationMode(projectEinoAssistantOptimizationCodexPOC)
			state.SetToolDiscovery(discovery)
			lifecycle := projectEinoAssistantLifecycleMiddleware(h.req, state, h.server).(*projectEinoAssistantLifecycle)
			model := &adk.ChatModelAgentState{}
			if err := lifecycle.refreshExecutableToolContext(context.Background(), model); err != nil {
				t.Fatal(err)
			}
			assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolGetRuntimeStatus, false)
			assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolRestartRuntime, false)
			assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolSetRuntimeEnv, false)
			readMatches := projectEinoAssistantSearchDynamicToolSpecs(projectEinoAssistantDynamicToolSpecs(h.server, h.req, discovery), projectToolGetRuntimeStatus, 1)
			if len(readMatches) != 1 || readMatches[0].Name != projectToolGetRuntimeStatus {
				t.Fatalf("read-only workflow was not searchable: %#v", readMatches)
			}
			selection, err := json.Marshal(projectEinoAssistantToolSearchResult{
				CatalogDigest: projectEinoAssistantDynamicToolCatalogDigest(discovery),
				Matches:       readMatches,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := state.ApplyDynamicToolSearchResult(string(selection)); err != nil {
				t.Fatal(err)
			}
			if err := lifecycle.refreshExecutableToolContext(context.Background(), model); err != nil {
				t.Fatal(err)
			}
			assertProjectEinoAssistantToolInfoPresence(t, model.ToolInfos, projectToolGetRuntimeStatus, true)
			matches := projectEinoAssistantSearchDynamicToolSpecs(projectEinoAssistantDynamicToolSpecs(h.server, h.req, discovery), projectToolRestartRuntime, 5)
			for _, match := range matches {
				if match.Name == projectToolRestartRuntime || match.Name == projectToolSetRuntimeEnv {
					t.Fatalf("read-only search returned effectful workflow: %#v", match)
				}
			}
			forged, err := json.Marshal(projectEinoAssistantToolSearchResult{
				CatalogDigest: projectEinoAssistantDynamicToolCatalogDigest(discovery),
				Matches:       []projectEinoAssistantToolSearchMatch{{Name: projectToolRestartRuntime}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := state.ApplyDynamicToolSearchResult(string(forged)); err == nil {
				t.Fatal("read-only mode accepted selection of a runtime mutation")
			}
			if state.DynamicToolSelected(projectToolRestartRuntime) {
				t.Fatal("rejected read-only selection granted runtime mutation")
			}
		})
	}
}

func TestProjectEinoDeferredWorkflowSearchReappliesTurnProfile(t *testing.T) {
	restartSpec, ok := projectAssistantWorkflowToolSpec(projectToolRestartRuntime)
	if !ok {
		t.Fatalf("workflow spec %q missing", projectToolRestartRuntime)
	}
	req := projectAssistantRunRequest{
		TurnProfile:       projectAssistantTurnProfileDebugging,
		TurnPolicy:        projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging),
		CollaborationMode: projectAssistantCollaborationModeDefault,
	}
	discovery := projectEinoAssistantToolDiscovery{
		CollaborationMode:     req.CollaborationMode,
		DeferredWorkflowTools: []projectAssistantToolSpec{restartSpec},
	}
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(req.TurnPolicy)
	state.SetAgentOptimizationMode(projectEinoAssistantOptimizationCodexPOC)
	state.SetToolDiscovery(discovery)
	if matches := projectEinoAssistantSearchDynamicToolSpecs(projectEinoAssistantDynamicToolSpecs(nil, req, discovery), projectToolRestartRuntime, 1); len(matches) != 0 {
		t.Fatalf("debugging profile search returned mutation workflow: %#v", matches)
	}
	forged, err := json.Marshal(projectEinoAssistantToolSearchResult{
		CatalogDigest: projectEinoAssistantDynamicToolCatalogDigest(discovery),
		Matches:       []projectEinoAssistantToolSearchMatch{{Name: projectToolRestartRuntime}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.ApplyDynamicToolSearchResult(string(forged)); err == nil {
		t.Fatal("debugging profile accepted selection of a runtime mutation")
	}
}

func TestProjectEinoDeferredGraphInvocationRequiresSelection(t *testing.T) {
	base := &projectEinoAssistantDeferredGraphTestTool{name: projectToolGetRuntimeStatus}
	spec, ok := projectAssistantWorkflowToolSpec(projectToolGetRuntimeStatus)
	if !ok {
		t.Fatalf("workflow spec %q missing", projectToolGetRuntimeStatus)
	}
	state := newProjectEinoAssistantRunState()
	state.SetAgentOptimizationMode(projectEinoAssistantOptimizationCodexPOC)
	tool, err := newProjectEinoAssistantDeferredGraphTool(base, spec, state, projectAssistantRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	invokable := tool.(einotool.InvokableTool)
	blocked, err := invokable.InvokableRun(context.Background(), `{}`)
	if err != nil || !strings.Contains(blocked, "call tool_search first") || base.calls != 0 {
		t.Fatalf("unselected graph invocation result=%q err=%v calls=%d", blocked, err, base.calls)
	}
	state.SetToolDiscovery(projectEinoAssistantToolDiscovery{
		CollaborationMode:     projectAssistantCollaborationModeDefault,
		DeferredWorkflowTools: []projectAssistantToolSpec{spec},
	})
	state.NextModelCallOrdinal()
	selection, err := json.Marshal(projectEinoAssistantToolSearchResult{
		CatalogDigest: projectEinoAssistantDynamicToolCatalogDigest(projectEinoAssistantToolDiscovery{
			CollaborationMode:     projectAssistantCollaborationModeDefault,
			DeferredWorkflowTools: []projectAssistantToolSpec{spec},
		}),
		Matches: []projectEinoAssistantToolSearchMatch{{Name: spec.Name}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.ApplyDynamicToolSearchResult(string(selection)); err != nil {
		t.Fatal(err)
	}
	if got := state.CurrentModelCallOrdinal(); got != 1 {
		t.Fatalf("selection model-call ordinal = %d, want 1", got)
	}
	if got, err := invokable.InvokableRun(context.Background(), `{}`); err != nil || !strings.Contains(got, "call tool_search first") || base.calls != 0 {
		t.Fatalf("same-batch graph invocation result=%q err=%v calls=%d", got, err, base.calls)
	}
	state.NextModelCallOrdinal()
	if got, err := invokable.InvokableRun(context.Background(), `{}`); err != nil || got != "invoked" || base.calls != 1 {
		t.Fatalf("selected graph invocation result=%q err=%v calls=%d", got, err, base.calls)
	}
}

func TestProjectEinoDeferredOrdinaryToolsRequireNextModelSample(t *testing.T) {
	for _, kind := range []string{"local", "mcp", "browser"} {
		t.Run(kind, func(t *testing.T) {
			h := newProjectAssistantV2ToolHarness(t, "deferred-sample-boundary-"+kind)
			var backendCalls int
			name := "deferred_local_probe"
			switch kind {
			case "mcp":
				name = "mcp_deferred_probe"
			case "browser":
				name = "browser_snapshot"
			}
			tool := projectAssistantToolFunc{
				spec: projectAssistantToolSpec{
					Name: name, Description: "test probe", Parameters: json.RawMessage(`{"type":"object"}`),
					Risk: projectAssistantToolRiskRead, ParallelSafe: true,
				},
				call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
					backendCalls++
					return `{"status":"ok"}`, nil
				},
			}
			discovery := projectEinoAssistantToolDiscovery{CollaborationMode: projectAssistantCollaborationModeDefault}
			switch kind {
			case "local":
				discovery.DeferredLocalTools = []projectAssistantTool{tool}
			case "mcp":
				discovery.MCPTools = []projectAssistantTool{tool}
			case "browser":
				discovery.BrowserTools = []projectAssistantTool{tool}
			}
			state := newProjectEinoAssistantRunState()
			state.SetTurnPolicy(h.req.TurnPolicy)
			state.SetAgentOptimizationMode(projectEinoAssistantOptimizationCodexPOC)
			state.SetToolDiscovery(discovery)
			state.NextModelCallOrdinal()
			selection, err := json.Marshal(projectEinoAssistantToolSearchResult{
				CatalogDigest: projectEinoAssistantDynamicToolCatalogDigest(discovery),
				Matches:       []projectEinoAssistantToolSearchMatch{{Name: name}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := state.ApplyDynamicToolSearchResult(string(selection)); err != nil {
				t.Fatal(err)
			}
			if state.DynamicToolSelectedForCurrentModelCall(name) {
				t.Fatalf("same-sample %s selection was eligible", kind)
			}

			req := h.req
			req.ToolPort = projectAssistantV2DirectToolPort{}
			var wrapped einotool.BaseTool
			switch kind {
			case "local":
				wrapped = newProjectEinoAssistantServerTool(h.server, tool, req, state)
			case "mcp":
				wrapped = newProjectEinoAssistantSearchableMCPTool(h.server, tool, req, state)
			case "browser":
				wrapped = newProjectEinoAssistantNativeBrowserTool(h.server, tool, req, state)
			}
			if guarded, ok := wrapped.(projectEinoAssistantDynamicSelectionRequired); !ok || !guarded.RequiresDynamicToolSelection() {
				t.Fatalf("%s wrapper %T has no selection guard", kind, wrapped)
			}
			runToolCall := func(callID string) ([]*schema.Message, error) {
				node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{
					Tools:               []einotool.BaseTool{wrapped},
					ExecuteSequentially: true,
				})
				if err != nil {
					return nil, err
				}
				return node.Invoke(context.Background(), &schema.Message{
					Role: schema.Assistant,
					ToolCalls: []schema.ToolCall{{
						ID:       callID,
						Function: schema.FunctionCall{Name: name, Arguments: `{}`},
					}},
				})
			}
			if _, err := runToolCall("blocked-" + kind); err != nil {
				t.Fatalf("same-batch %s call returned an infrastructure error: %v", kind, err)
			}
			if backendCalls != 0 {
				t.Fatalf("same-batch %s call reached backend %d times", kind, backendCalls)
			}
			if outcome, ok, err := req.eventLedger.ToolCallOutcome(context.Background(), "blocked-"+kind); err != nil || !ok || !outcome.Failed {
				t.Fatalf("same-batch %s durable outcome = %#v, present=%v, err=%v", kind, outcome, ok, err)
			}
			state.NextModelCallOrdinal()
			if _, err := runToolCall("admitted-" + kind); err != nil {
				t.Fatalf("next-sample %s call: %v", kind, err)
			}
			if backendCalls != 1 {
				t.Fatalf("next-sample %s backend calls = %d, want 1", kind, backendCalls)
			}
		})
	}
}

func TestProjectEinoDynamicToolSelectionDeduplicatesAndPersistsSampleBoundary(t *testing.T) {
	tools := make([]projectAssistantTool, 0, projectEinoAssistantMaxSelectedDynamicTools+1)
	for index := 0; index < projectEinoAssistantMaxSelectedDynamicTools+1; index++ {
		name := fmt.Sprintf("deferred_probe_%02d", index)
		tools = append(tools, projectAssistantToolFunc{spec: projectAssistantToolSpec{
			Name: name, Description: "test probe", Parameters: json.RawMessage(`{"type":"object"}`), Risk: projectAssistantToolRiskRead,
		}})
	}
	discovery := projectEinoAssistantToolDiscovery{
		CollaborationMode:  projectAssistantCollaborationModeDefault,
		DeferredLocalTools: tools,
	}
	state := newProjectEinoAssistantRunState()
	state.SetToolDiscovery(discovery)
	state.NextModelCallOrdinal()
	matches := make([]projectEinoAssistantToolSearchMatch, 0, projectEinoAssistantMaxSelectedDynamicTools*2)
	for index := 0; index < projectEinoAssistantMaxSelectedDynamicTools*2; index++ {
		matches = append(matches, projectEinoAssistantToolSearchMatch{Name: tools[0].Spec().Name})
	}
	for index := 1; index < projectEinoAssistantMaxSelectedDynamicTools; index++ {
		matches = append(matches, projectEinoAssistantToolSearchMatch{Name: tools[index].Spec().Name})
	}
	selection, err := json.Marshal(projectEinoAssistantToolSearchResult{
		CatalogDigest: projectEinoAssistantDynamicToolCatalogDigest(discovery), Matches: matches,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.ApplyDynamicToolSearchResult(string(selection)); err != nil {
		t.Fatal(err)
	}
	checkpoint := state.CheckpointState()
	if len(checkpoint.SelectedDynamicToolNames) != projectEinoAssistantMaxSelectedDynamicTools {
		t.Fatalf("selected tools = %d, want %d: %#v", len(checkpoint.SelectedDynamicToolNames), projectEinoAssistantMaxSelectedDynamicTools, checkpoint.SelectedDynamicToolNames)
	}
	if len(checkpoint.SelectedDynamicToolModelCallOrdinals) != len(checkpoint.SelectedDynamicToolNames) {
		t.Fatalf("selection ordinals = %#v, names = %#v", checkpoint.SelectedDynamicToolModelCallOrdinals, checkpoint.SelectedDynamicToolNames)
	}
	if state.DynamicToolSelectedForCurrentModelCall(tools[0].Spec().Name) {
		t.Fatal("same-sample selection was eligible before another model sample")
	}

	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	if restored.DynamicToolSelectedForCurrentModelCall(tools[0].Spec().Name) {
		t.Fatal("restored same-sample selection became eligible without a new sample")
	}
	restored.NextModelCallOrdinal()
	if !restored.DynamicToolSelectedForCurrentModelCall(tools[0].Spec().Name) {
		t.Fatal("restored selection was not eligible after a later model sample")
	}

	legacy := checkpoint
	legacy.SelectedDynamicToolModelCallOrdinals = nil
	legacyState := newProjectEinoAssistantRunState()
	legacyState.RestoreCheckpointState(legacy)
	if legacyState.DynamicToolSelectedForCurrentModelCall(tools[0].Spec().Name) {
		t.Fatal("legacy checkpoint without sample metadata bypassed same-sample gate")
	}
	legacyState.NextModelCallOrdinal()
	if !legacyState.DynamicToolSelectedForCurrentModelCall(tools[0].Spec().Name) {
		t.Fatal("legacy checkpoint selection did not become eligible after the next sample")
	}
}

func TestProjectEinoDeferredLocalSelectionRejectsStaleAndUnknownTools(t *testing.T) {
	tool := projectAssistantToolFunc{spec: projectAssistantToolSpec{Name: projectToolGetRuntimeStatus, Description: "runtime status", Parameters: json.RawMessage(`{"type":"object"}`), Risk: projectAssistantToolRiskRead}}
	discovery := projectEinoAssistantToolDiscovery{DeferredLocalTools: []projectAssistantTool{tool}}
	state := newProjectEinoAssistantRunState()
	state.SetToolDiscovery(discovery)
	encode := func(digest, name string) string {
		b, err := json.Marshal(projectEinoAssistantToolSearchResult{CatalogDigest: digest, Matches: []projectEinoAssistantToolSearchMatch{{Name: name}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if err := state.ApplyDynamicToolSearchResult(encode("sha256:old", projectToolGetRuntimeStatus)); err == nil {
		t.Fatal("stale catalog accepted")
	}
	if err := state.ApplyDynamicToolSearchResult(encode(projectEinoAssistantDynamicToolCatalogDigest(discovery), projectToolRestartRuntime)); err == nil {
		t.Fatal("unavailable tool accepted")
	}
	if state.DynamicToolSelected(projectToolGetRuntimeStatus) || state.DynamicToolSelected(projectToolRestartRuntime) {
		t.Fatal("rejected search granted a capability")
	}
}

func projectEinoAssistantTestToolByName(t *testing.T, tools []einotool.BaseTool, name string) einotool.BaseTool {
	t.Helper()
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info != nil && info.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

type projectEinoAssistantDeferredGraphTestTool struct {
	name  string
	calls int
}

func (t *projectEinoAssistantDeferredGraphTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (t *projectEinoAssistantDeferredGraphTestTool) InvokableRun(context.Context, string, ...einotool.Option) (string, error) {
	t.calls++
	return "invoked", nil
}
