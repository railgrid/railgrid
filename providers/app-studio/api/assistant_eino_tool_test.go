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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantTurnNeedsInfrastructureMCP(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    bool
	}{
		{"list instances", "list instances via mcp", true},
		{"single instance", "show me the status of my instance", true},
		{"platform vocabulary", "what platform resources do I have?", true},
		{"mcp mention", "call mcp to enumerate things", true},
		{"templates", "what templates are available?", true},
		{"databricks tables", "can you query my Databricks table metadata?", true},
		{"data prompt", "I need to inspect table data for this project", true},
		{"generic UI table", "render a table of todos in app.js", false},
		{"unrelated", "fix the button styling in app.js", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := []store.Message{{
				Role:    aiv1alpha1.ProjectMessageRoleUser,
				Content: tc.content,
			}}
			if got := projectAssistantTurnNeedsInfrastructureMCP(history); got != tc.want {
				t.Fatalf("projectAssistantTurnNeedsInfrastructureMCP(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

func TestProjectAssistantTurnPolicyCanUseDatabricksMCP(t *testing.T) {
	req := projectAssistantRunRequest{
		History: []store.Message{{
			Role:    aiv1alpha1.ProjectMessageRoleUser,
			Content: "make me a dashboard from my Databricks table",
		}},
	}
	policy := projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileExploration)
	if !projectAssistantTurnPolicyCanUseMCP(policy, req) {
		t.Fatal("expected exploration turn with Databricks table request to use MCP")
	}

	req.History[0].Content = "fix the button styling"
	if projectAssistantTurnPolicyCanUseMCP(policy, req) {
		t.Fatal("expected unrelated turn to skip MCP discovery")
	}

	req.History[0].Content = "render a table of todos in app.js"
	if projectAssistantTurnPolicyCanUseMCP(policy, req) {
		t.Fatal("expected generic UI table request to skip MCP discovery")
	}
}

func TestProjectEinoAssistantToolRedactsFailedResult(t *testing.T) {
	const secret = "sk-super-secret"
	var failedEvent projectToolCallStreamEvent
	localTool := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name: "failing_local_tool",
			Risk: projectAssistantToolRiskRead,
		},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			return "", errors.New(
				"Authorization: Bearer " + secret + " " +
					strings.Repeat("x", projectToolInfoLimit),
			)
		},
	}
	tool := projectEinoAssistantTool{
		tool: localTool,
		req: projectAssistantRunRequest{
			StreamCallbacks: projectAssistantStreamCallbacks{
				OnToolCall: func(event projectToolCallStreamEvent) {
					failedEvent = event
				},
			},
		},
		runState: newProjectEinoAssistantRunState(),
	}

	result, err := tool.invokeAllowedTool(
		context.Background(),
		"call-failing-local-tool",
		localTool.Spec(),
		map[string]any{},
	)
	if err != nil {
		t.Fatalf("invokeAllowedTool returned error: %v", err)
	}
	if strings.Contains(result, secret) || !strings.Contains(result, "[REDACTED]") {
		t.Fatalf("model-visible result = %q, want secret redacted", result)
	}
	if result != truncateProjectToolInfo(result) {
		t.Fatalf("model-visible result length = %d, want bounded by truncateProjectToolInfo", len(result))
	}
	if failedEvent.Status != "failed" {
		t.Fatalf("tool event status = %q, want failed", failedEvent.Status)
	}
	if strings.Contains(failedEvent.Error, secret) || !strings.Contains(failedEvent.Error, "[REDACTED]") {
		t.Fatalf("tool event error = %q, want secret redacted", failedEvent.Error)
	}
	if failedEvent.Error != truncateProjectToolInfo(failedEvent.Error) {
		t.Fatalf("tool event error length = %d, want bounded by truncateProjectToolInfo", len(failedEvent.Error))
	}
}

func TestProjectEinoAssistantToolPropagatesControlFlowErrors(t *testing.T) {
	interruptErr := einotool.StatefulInterrupt(
		context.Background(),
		"approval required",
		map[string]string{"status": "waiting"},
	)
	tests := []struct {
		name string
		err  error
	}{
		{name: "context canceled", err: context.Canceled},
		{name: "context deadline exceeded", err: context.DeadlineExceeded},
		{name: "stream canceled", err: adk.ErrStreamCanceled},
		{name: "forbidden", err: apierrors.NewForbidden(
			k8sschema.GroupResource{Group: "ai.kedge.faros.sh", Resource: "projects"},
			"demo",
			errors.New("denied"),
		)},
		{name: "unauthorized", err: apierrors.NewUnauthorized("denied")},
		{name: "stateful interrupt", err: interruptErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localTool := projectAssistantToolFunc{
				spec: projectAssistantToolSpec{
					Name: "control_flow_tool",
					Risk: projectAssistantToolRiskRead,
				},
				call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
					return "", tt.err
				},
			}
			tool := projectEinoAssistantTool{
				tool:     localTool,
				runState: newProjectEinoAssistantRunState(),
			}

			result, gotErr := tool.invokeAllowedTool(
				context.Background(),
				"call-control-flow",
				localTool.Spec(),
				map[string]any{},
			)
			if gotErr != tt.err {
				t.Fatalf("error = %v, want original error %v", gotErr, tt.err)
			}
			if result != "" {
				t.Fatalf("result = %q, want empty result for propagated error", result)
			}
		})
	}
}

func TestEinoApprovePlanToolDerivesWorkspaceMutationCapability(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	runState := newProjectEinoAssistantRunState()
	tool := projectEinoAssistantTool{
		server: server,
		req: projectAssistantRunRequest{
			MessageScope: store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo"},
		},
		runState: runState,
	}

	result, err := tool.invokeApprovedPlanTool(context.Background(), "call-plan", projectAssistantToolSpec{
		Name: projectToolRequestProjectPlanApproval,
		Risk: projectAssistantToolRiskPlan,
	}, map[string]any{
		"summary":            "Build dashboard",
		"steps":              []any{"Write app shell"},
		"targetPaths":        []any{"src/"},
		"acceptanceCriteria": []any{"src/App.tsx exists"},
	})
	if err != nil {
		t.Fatalf("invokeApprovedPlanTool returned error: %v", err)
	}

	if !strings.Contains(result, `"status":"approved"`) {
		t.Fatalf("result = %q, want approved plan", result)
	}
	plan := runState.ApprovedPlan()
	if plan == nil {
		t.Fatal("approved plan = nil")
	}
	if plan.Version != projectAssistantApprovedPlanVersionWorkspaceMutation {
		t.Fatalf("approved plan version = %d, want %d", plan.Version, projectAssistantApprovedPlanVersionWorkspaceMutation)
	}
	if !stringSliceEqual(plan.Capabilities, []string{projectAssistantCapabilityWorkspaceMutate}) {
		t.Fatalf("approved plan capabilities = %#v, want workspace mutation", plan.Capabilities)
	}
	for _, toolName := range []string{projectToolWriteFile, projectToolApplyPatch, projectToolMkdir} {
		if !projectAssistantApprovedPlanAllowsWrite(plan, toolName, map[string]any{"path": "src/App.tsx"}) {
			t.Fatalf("derived workspace grant does not authorize %s", toolName)
		}
	}
}

func TestEinoApprovePlanReplacesExpandedScopeInsteadOfUnioning(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo"}
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(projectAssistantApprovedPlan{
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		TargetPaths:  []string{"src/"},
	})
	tool := projectEinoAssistantTool{
		server:   server,
		req:      projectAssistantRunRequest{MessageScope: scope},
		runState: runState,
	}

	result, err := tool.invokeApprovedPlanTool(
		context.Background(),
		"call-plan",
		projectAssistantToolSpec{Name: projectToolRequestProjectPlanApproval, Risk: projectAssistantToolRiskPlan},
		map[string]any{
			"summary":     "Move implementation to generated output",
			"steps":       []any{"write generated files"},
			"targetPaths": []any{"generated/"},
		},
	)
	if err != nil {
		t.Fatalf("invokeApprovedPlanTool returned error: %v", err)
	}
	if !strings.Contains(result, `"status":"approved"`) {
		t.Fatalf("result = %q, want approved plan", result)
	}
	plan := runState.ApprovedPlan()
	if plan == nil {
		t.Fatal("approved plan = nil")
	}
	if !stringSliceEqual(plan.TargetPaths, []string{"generated/"}) {
		t.Fatalf("approved plan paths = %#v, want replacement scope only", plan.TargetPaths)
	}
}

func TestEinoApprovePlanToolFailsClosedOnGrantRevisionConflict(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo"}
	if err := server.clearProjectAssistantApprovedPlan(context.Background(), scope); err != nil {
		t.Fatalf("clearProjectAssistantApprovedPlan returned error: %v", err)
	}
	runState := newProjectEinoAssistantRunState()
	tool := projectEinoAssistantTool{
		server:   server,
		req:      projectAssistantRunRequest{MessageScope: scope},
		runState: runState,
	}

	result, err := tool.invokeApprovedPlanTool(
		context.Background(),
		"call-plan",
		projectAssistantToolSpec{Name: projectToolRequestProjectPlanApproval, Risk: projectAssistantToolRiskPlan},
		map[string]any{
			"summary":     "Update the app",
			"steps":       []any{"edit source"},
			"targetPaths": []any{"src/"},
		},
	)
	if !errors.Is(err, errProjectAssistantPlanGrantPersistence) || result != "" {
		t.Fatalf("plan approval result = %q error = %v, want terminal persistence conflict", result, err)
	}
	if plan := runState.ApprovedPlan(); plan != nil {
		t.Fatalf("in-memory plan after persistence conflict = %#v, want nil", plan)
	}
}

func TestEinoPendingLegacyPlanApprovalCannotBecomeCapabilityGrant(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	tool := projectEinoAssistantTool{runState: runState}

	result, err := tool.invokeApprovedPlanTool(
		context.Background(),
		"call-legacy-plan",
		projectAssistantToolSpec{Name: projectToolRequestProjectPlanApproval, Risk: projectAssistantToolRiskPlan},
		map[string]any{
			"summary":           "Update the app",
			"steps":             []any{"edit source"},
			"targetPaths":       []any{"src/"},
			"allowedOperations": []any{projectToolWriteFile},
		},
	)
	if err != nil {
		t.Fatalf("invokeApprovedPlanTool returned error: %v", err)
	}
	if !strings.Contains(result, "must be requested again") {
		t.Fatalf("legacy plan result = %q, want a fresh-approval requirement", result)
	}
	if plan := runState.ApprovedPlan(); plan != nil {
		t.Fatalf("legacy pending approval created capability grant %#v", plan)
	}
}

func TestEinoDirectWriteGrantFailsClosedOnGrantRevisionConflict(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo"}
	if err := server.clearProjectAssistantApprovedPlan(context.Background(), scope); err != nil {
		t.Fatalf("clearProjectAssistantApprovedPlan returned error: %v", err)
	}
	runState := newProjectEinoAssistantRunState()
	tool := projectEinoAssistantTool{
		server:   server,
		req:      projectAssistantRunRequest{MessageScope: scope},
		runState: runState,
	}

	err := tool.grantWriteUntilCommit(context.Background(), projectToolWriteFile, map[string]any{"path": "src/App.tsx"})
	if !errors.Is(err, errProjectAssistantPlanGrantPersistence) {
		t.Fatalf("grantWriteUntilCommit error = %v, want terminal persistence conflict", err)
	}
	if plan := runState.ApprovedPlan(); plan != nil {
		t.Fatalf("in-memory direct grant after persistence conflict = %#v, want nil", plan)
	}
}

func TestEinoDirectWriteGrantAuthorizesCanonicalEditsOnlyOnApprovedPath(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo"}
	runState := newProjectEinoAssistantRunState()
	tool := projectEinoAssistantTool{
		server:   server,
		req:      projectAssistantRunRequest{MessageScope: scope},
		runState: runState,
	}

	if err := tool.grantWriteUntilCommit(context.Background(), projectToolWriteFile, map[string]any{"path": "src/App.tsx"}); err != nil {
		t.Fatalf("grantWriteUntilCommit returned error: %v", err)
	}
	plan := runState.ApprovedPlan()
	if plan == nil {
		t.Fatal("direct write grant = nil")
	}
	if !stringSliceEqual(plan.TargetPaths, []string{"src/App.tsx"}) {
		t.Fatalf("direct write grant paths = %#v, want exact approved path", plan.TargetPaths)
	}
	if !projectAssistantApprovedPlanAllowsWrite(plan, projectToolApplyPatch, map[string]any{"path": "src/App.tsx"}) {
		t.Fatal("direct write grant should authorize apply_patch on the approved path")
	}
	if projectAssistantApprovedPlanAllowsWrite(plan, projectToolWriteFile, map[string]any{"path": "src/Other.tsx"}) {
		t.Fatal("direct write grant must not authorize a different path")
	}
}

func TestEinoDirectMkdirGrantAuthorizesChildEdits(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo"}
	runState := newProjectEinoAssistantRunState()
	tool := projectEinoAssistantTool{
		server:   server,
		req:      projectAssistantRunRequest{MessageScope: scope},
		runState: runState,
	}

	if err := tool.grantWriteUntilCommit(context.Background(), projectToolMkdir, map[string]any{"path": "src/components"}); err != nil {
		t.Fatalf("grantWriteUntilCommit returned error: %v", err)
	}
	plan := runState.ApprovedPlan()
	if plan == nil {
		t.Fatal("direct mkdir grant = nil")
	}
	if !stringSliceEqual(plan.TargetPaths, []string{"src/components/"}) {
		t.Fatalf("direct mkdir grant paths = %#v, want directory subtree", plan.TargetPaths)
	}
	if !projectAssistantApprovedPlanAllowsWrite(plan, projectToolWriteFile, map[string]any{"path": "src/components/Foo.tsx"}) {
		t.Fatal("direct mkdir grant should authorize writes below the approved directory")
	}
}

func TestEinoDirectWriteGrantDoesNotMergeObsoleteAuthority(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo"}
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(projectAssistantApprovedPlan{
		TargetPaths:    []string{"secrets/"},
		AllowAllWrites: true,
	})
	tool := projectEinoAssistantTool{
		server:   server,
		req:      projectAssistantRunRequest{MessageScope: scope},
		runState: runState,
	}

	if err := tool.grantWriteUntilCommit(context.Background(), projectToolWriteFile, map[string]any{"path": "src/App.tsx"}); err != nil {
		t.Fatalf("grantWriteUntilCommit returned error: %v", err)
	}
	plan := runState.ApprovedPlan()
	if plan == nil {
		t.Fatal("direct write grant = nil")
	}
	if plan.AllowAllWrites || !stringSliceEqual(plan.TargetPaths, []string{"src/App.tsx"}) {
		t.Fatalf("direct write grant = %#v, want only fresh approved path", plan)
	}
}

func TestEinoToolReplansWhenPersistedGrantDoesNotCoverAutoApprovedWrite(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	req := projectAssistantRunRequest{
		Identity:           id,
		Project:            project,
		MessageScope:       projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		AutoApproveActions: true,
	}
	stalePlan := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:      "Update the public client.",
		Steps:        []string{"update public/index.html", "update public/app.js"},
		TargetPaths:  []string{"public/index.html", "public/app.js"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
	})
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), req.MessageScope, &stalePlan); err != nil {
		t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(stalePlan)
	runState.SetApprovedPlanGrantRevision(projectAssistantGrantRevisionForTest(t, server, req.MessageScope))

	mutationCalls := 0
	var events []projectToolCallStreamEvent
	req.StreamCallbacks.OnToolCall = func(event projectToolCallStreamEvent) {
		events = append(events, event)
	}
	writeTool := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:       projectToolWriteFile,
			Risk:       projectAssistantToolRiskWrite,
			Parameters: json.RawMessage(`{"type":"object"}`),
		},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			mutationCalls++
			return `{"operation":"write_file","path":"server.js"}`, nil
		},
	}
	writeAdapter := projectEinoAssistantTool{
		server:   server,
		tool:     writeTool,
		req:      req,
		runState: runState,
	}

	result, err := writeAdapter.InvokableRun(context.Background(), `{"path":"server.js","content":"serve()"}`)
	if err != nil {
		t.Fatalf("out-of-plan write returned error: %v, want model-visible replanning result", err)
	}
	if mutationCalls != 0 {
		t.Fatalf("out-of-plan mutation calls = %d, want 0", mutationCalls)
	}
	if !strings.Contains(result, "plan approval required") {
		t.Fatalf("out-of-plan result = %q, want revised-plan guidance", result)
	}
	if runState.ApprovedPlan() != nil {
		t.Fatalf("run-local plan = %#v, want stale grant cleared", runState.ApprovedPlan())
	}
	if persisted := server.loadProjectAssistantApprovedPlan(context.Background(), req.MessageScope); persisted != nil {
		t.Fatalf("persisted plan = %#v, want stale grant cleared", persisted)
	}
	for _, event := range events {
		if event.Status == "permission_required" {
			t.Fatalf("events = %#v, want replanning without direct write interrupt", events)
		}
	}

	phaseState := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			projectEinoAssistantPhaseToolResult(projectToolWriteFile, result),
		},
		ToolInfos: []*schema.ToolInfo{
			projectEinoAssistantPhaseToolInfo(projectToolRequestProjectPlanApproval, projectAssistantToolRiskPlan, projectAssistantToolBundleCollaboration),
			projectEinoAssistantPhaseToolInfo(projectToolWriteFile, projectAssistantToolRiskWrite, projectAssistantToolBundleEdit),
		},
	}
	phaseMiddleware := projectEinoAssistantPhaseMiddleware(req, runState)
	_, phaseState, err = phaseMiddleware.BeforeModelRewriteState(context.Background(), phaseState, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	visible := projectEinoAssistantPhaseToolNames(phaseState.ToolInfos)
	if !projectEinoAssistantPhaseToolNamesContain(visible, projectToolRequestProjectPlanApproval) {
		t.Fatalf("approval-phase tools = %#v, want %s visible", visible, projectToolRequestProjectPlanApproval)
	}
	if projectEinoAssistantPhaseToolNamesContain(visible, projectToolWriteFile) {
		t.Fatalf("approval-phase tools = %#v, want unauthorized write hidden", visible)
	}

	planTool := projectAssistantToolFunc{spec: projectAssistantToolSpec{
		Name:       projectToolRequestProjectPlanApproval,
		Risk:       projectAssistantToolRiskPlan,
		Parameters: json.RawMessage(`{"type":"object"}`),
	}}
	planAdapter := projectEinoAssistantTool{
		server:   server,
		tool:     planTool,
		req:      req,
		runState: runState,
	}
	planResult, err := planAdapter.InvokableRun(context.Background(), `{
		"summary":"Add the server behavior.",
		"steps":["Update server.js"],
		"targetPaths":["server.js"],
		"acceptanceCriteria":["server.js is updated"]
	}`)
	if err != nil {
		t.Fatalf("replacement plan returned error: %v", err)
	}
	if !strings.Contains(planResult, `"status":"approved"`) {
		t.Fatalf("replacement plan result = %q, want auto-approved plan", planResult)
	}
	replacement := runState.ApprovedPlan()
	if replacement == nil ||
		!stringSliceEqual(replacement.TargetPaths, []string{"server.js"}) ||
		!stringSliceEqual(replacement.Capabilities, []string{projectAssistantCapabilityWorkspaceMutate}) {
		t.Fatalf("replacement plan = %#v, want server.js workspace mutation envelope only", replacement)
	}
	if persisted := server.loadProjectAssistantApprovedPlan(context.Background(), req.MessageScope); persisted == nil ||
		!stringSliceEqual(persisted.TargetPaths, []string{"server.js"}) {
		t.Fatalf("persisted replacement plan = %#v, want server.js envelope", persisted)
	}

	result, err = writeAdapter.InvokableRun(context.Background(), `{"path":"server.js","content":"serve()"}`)
	if err != nil {
		t.Fatalf("replanned write returned error: %v", err)
	}
	if mutationCalls != 1 {
		t.Fatalf("replanned mutation calls = %d, want 1", mutationCalls)
	}
	if !strings.Contains(result, `"operation":"write_file"`) {
		t.Fatalf("replanned write result = %q, want mutation result", result)
	}
}

func TestEinoToolFailsClosedWhenPersistedGrantCannotBeRetired(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	req := projectAssistantRunRequest{
		Identity:           id,
		Project:            project,
		MessageScope:       projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		AutoApproveActions: true,
	}
	stalePlan := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:      "Update the public client.",
		Steps:        []string{"update public/app.js"},
		TargetPaths:  []string{"public/app.js"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
	})
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), req.MessageScope, &stalePlan); err != nil {
		t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
	}
	server.store = failProjectAssistantPlanGrantClearStore{Store: messages}
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(stalePlan)
	runState.SetApprovedPlanGrantRevision(projectAssistantGrantRevisionForTest(t, server, req.MessageScope))

	mutationCalls := 0
	writeAdapter := projectEinoAssistantTool{
		server: server,
		tool: projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:       projectToolWriteFile,
				Risk:       projectAssistantToolRiskWrite,
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
			call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
				mutationCalls++
				return `{"operation":"write_file","path":"server.js"}`, nil
			},
		},
		req:      req,
		runState: runState,
	}

	result, err := writeAdapter.InvokableRun(context.Background(), `{"path":"server.js","content":"serve()"}`)
	if err == nil || !strings.Contains(err.Error(), "retire stale App Studio plan grant") {
		t.Fatalf("out-of-plan write error = %v, want persisted-grant retirement failure", err)
	}
	if !errors.Is(err, errProjectAssistantPlanRetirement) {
		t.Fatalf("out-of-plan write error = %v, want terminal plan-retirement classification", err)
	}
	if result != "" {
		t.Fatalf("out-of-plan result = %q, want no normal replanning result", result)
	}
	if mutationCalls != 0 {
		t.Fatalf("out-of-plan mutation calls = %d, want 0", mutationCalls)
	}
	if persisted := server.loadProjectAssistantApprovedPlan(context.Background(), req.MessageScope); persisted == nil {
		t.Fatal("persisted plan unexpectedly cleared by failing store")
	}
}

func TestEinoCommitRetiresPersistedGrantBeforeRepositoryMutation(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	req := projectAssistantRunRequest{
		Identity:     id,
		Project:      project,
		MessageScope: projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
	}
	plan := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:      "Update the application.",
		Steps:        []string{"update src/App.tsx"},
		TargetPaths:  []string{"src/App.tsx"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
	})
	if err := server.saveProjectAssistantApprovedPlan(context.Background(), req.MessageScope, &plan); err != nil {
		t.Fatalf("saveProjectAssistantApprovedPlan returned error: %v", err)
	}
	server.store = failProjectAssistantPlanGrantClearStore{Store: messages}
	runState := newProjectEinoAssistantRunState()
	runState.ApprovePlan(plan)
	runState.SetApprovedPlanGrantRevision(projectAssistantGrantRevisionForTest(t, server, req.MessageScope))

	commitCalls := 0
	commitAdapter := projectEinoAssistantTool{
		server: server,
		tool: projectAssistantToolFunc{
			spec: projectAssistantToolSpec{
				Name:       projectToolCommitProjectFiles,
				Risk:       projectAssistantToolRiskCommit,
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
			call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
				commitCalls++
				return `{"status":"committed"}`, nil
			},
		},
		req:      req,
		runState: runState,
	}

	err := commitAdapter.requestPermission(
		context.Background(),
		"call-commit",
		commitAdapter.tool.Spec(),
		map[string]any{"message": "Update application"},
		`{"message":"Update application"}`,
	)
	result := ""
	if err == nil || !strings.Contains(err.Error(), "retire approved plan before commit") {
		t.Fatalf("commit error = %v, want pre-commit plan retirement failure", err)
	}
	if result != "" {
		t.Fatalf("commit result = %q, want no successful repository result", result)
	}
	if commitCalls != 0 {
		t.Fatalf("repository commit calls = %d, want 0", commitCalls)
	}
	if runState.ApprovedPlan() != nil {
		t.Fatal("run-local plan remained active after retirement failure")
	}
	if persisted := server.loadProjectAssistantApprovedPlan(context.Background(), req.MessageScope); persisted == nil {
		t.Fatal("persisted plan unexpectedly cleared by failing store")
	}
}

func TestEinoToolPassesSessionSnapshotToLocalTool(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	runState.SetSessionSnapshot(projectEinoAssistantSessionSnapshot{
		LastFileSnapshot:  []string{"package.json"},
		RecommendedChecks: []string{"build"},
	})
	var got *projectEinoAssistantSessionSnapshot
	localTool := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name: "capture_session_snapshot",
			Risk: projectAssistantToolRiskRead,
		},
		call: func(_ context.Context, req projectAssistantToolCallRequest) (string, error) {
			got = req.SessionSnapshot
			return `{"status":"captured"}`, nil
		},
	}
	tool := projectEinoAssistantTool{
		tool:     localTool,
		req:      projectAssistantRunRequest{},
		runState: runState,
	}

	if _, err := tool.invokeAllowedTool(context.Background(), "call-session", localTool.Spec(), nil); err != nil {
		t.Fatalf("invokeAllowedTool returned error: %v", err)
	}
	if got == nil || !stringSliceEqual(got.LastFileSnapshot, []string{"package.json"}) {
		t.Fatalf("session snapshot = %#v, want file snapshot", got)
	}
	if !stringSliceEqual(got.RecommendedChecks, []string{"build"}) {
		t.Fatalf("recommended checks = %#v, want build", got.RecommendedChecks)
	}
	got.LastFileSnapshot[0] = "mutated"
	if !stringSliceEqual(runState.SessionSnapshot().LastFileSnapshot, []string{"package.json"}) {
		t.Fatal("tool received mutable run-state session snapshot")
	}
}

type failProjectAssistantPlanGrantClearStore struct {
	store.Store
}

func projectAssistantGrantRevisionForTest(t *testing.T, server *Server, scope store.Scope) string {
	t.Helper()
	_, revision, err := server.loadProjectAssistantApprovedPlanGrant(context.Background(), scope)
	if err != nil {
		t.Fatalf("loadProjectAssistantApprovedPlanGrant returned error: %v", err)
	}
	return revision
}

func (s failProjectAssistantPlanGrantClearStore) SaveAssistantRun(ctx context.Context, scope store.Scope, run store.AssistantRun) error {
	if run.ID == projectAssistantApprovedPlanGrantRunID {
		var record projectAssistantApprovedPlanGrantRecord
		if err := json.Unmarshal(run.Checkpoint, &record); err == nil && record.Revision != "" && record.Plan == nil {
			return errors.New("injected plan-grant clear failure")
		}
	}
	return s.Store.SaveAssistantRun(ctx, scope, run)
}

func (s failProjectAssistantPlanGrantClearStore) CompareAndSwapAssistantRun(
	ctx context.Context,
	scope store.Scope,
	run store.AssistantRun,
	expectedRequestID string,
) error {
	if run.ID == projectAssistantApprovedPlanGrantRunID {
		var record projectAssistantApprovedPlanGrantRecord
		if err := json.Unmarshal(run.Checkpoint, &record); err == nil && record.Revision != "" && record.Plan == nil {
			return errors.New("injected plan-grant clear failure")
		}
	}
	return s.Store.CompareAndSwapAssistantRun(ctx, scope, run, expectedRequestID)
}

func TestEinoToolSchedulesDevelopmentSyncAfterMutatingTool(t *testing.T) {
	runState := newProjectEinoAssistantRunState()
	server := &Server{}
	var gotName string
	var gotProjectName string
	server.developmentSyncAfterMutation = func(_ identity, p *aiv1alpha1.Project, name string) {
		gotName = name
		if p != nil {
			gotProjectName = p.Name
		}
	}
	localTool := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name: projectToolWriteFile,
			Risk: projectAssistantToolRiskWrite,
		},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			return `{"status":"ok"}`, nil
		},
	}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	tool := projectEinoAssistantTool{
		server: server,
		tool:   localTool,
		req: projectAssistantRunRequest{
			Project:        project,
			WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"},
		},
		runState: runState,
	}

	if _, err := tool.invokeAllowedTool(context.Background(), "call-write", localTool.Spec(), map[string]any{"path": "src/App.tsx"}); err != nil {
		t.Fatalf("invokeAllowedTool returned error: %v", err)
	}
	if gotName != projectToolWriteFile || gotProjectName != "demo" {
		t.Fatalf("scheduled sync = (%q, %q), want (%q, demo)", gotName, gotProjectName, projectToolWriteFile)
	}
}

func TestEinoSelectTemplateRefreshesProjectUsedBySubsequentWorkspaceSync(t *testing.T) {
	var projectYAML string
	templateJSON, err := json.Marshal(applicationTemplateObject().Object)
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	graphQLServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode GraphQL request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "TemplateYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"infrastructure_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"TemplateYaml": string(templateJSON)}},
			}})
		case strings.Contains(req.Query, "ApplicationYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"infrastructure_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{
					"ApplicationYaml": `{"apiVersion":"infrastructure.kedge.faros.sh/v1alpha1","kind":"Application","metadata":{"name":"demo-dev"},"status":{"phase":"Ready"}}`,
				}},
			}})
		case strings.Contains(req.Query, "ProjectYaml"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"ai_kedge_faros_sh": map[string]any{"v1alpha1": map[string]any{"ProjectYaml": projectYAML}},
			}})
		case strings.Contains(req.Query, "applyStatusYaml"):
			_, _ = w.Write([]byte(`{"data":{"applyStatusYaml":"ok"}}`))
		case strings.Contains(req.Query, "applyYaml"):
			appliedYAML, _ := req.Variables["yaml"].(string)
			if strings.Contains(appliedYAML, "kind: Project\n") {
				projectYAML = appliedYAML
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"applyYaml": appliedYAML}})
		default:
			t.Fatalf("unexpected GraphQL query: %s", req.Query)
		}
	}))
	t.Cleanup(graphQLServer.Close)

	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(
		tenant.NewGraphQLClient(graphQLServer.URL, false),
		nil,
		workspaces,
		"",
		false,
	)
	type scheduledSync struct {
		name    string
		project *aiv1alpha1.Project
	}
	var scheduled []scheduledSync
	server.developmentSyncAfterMutation = func(_ identity, p *aiv1alpha1.Project, name string) {
		scheduled = append(scheduled, scheduledSync{name: name, project: p.DeepCopy()})
	}

	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
	}
	id := identity{
		clusterID:     "cluster-ws-1",
		token:         "caller-token",
		orgUUID:       "org-a",
		workspaceUUID: "ws-1",
	}
	req := projectAssistantRunRequest{
		Identity:       id,
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
	}
	runState := newProjectEinoAssistantRunState()
	registry := server.projectAssistantToolRegistry()
	invoke := func(name string, arguments map[string]any) {
		t.Helper()
		localTool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("%s missing from registry", name)
		}
		tool := projectEinoAssistantTool{
			server:   server,
			tool:     localTool,
			req:      req,
			runState: runState,
		}
		if _, err := tool.invokeAllowedTool(context.Background(), "call-"+name, localTool.Spec(), arguments); err != nil {
			t.Fatalf("%s returned error: %v", name, err)
		}
	}

	invoke(projectToolSelectTemplate, map[string]any{"template": "application"})
	invoke(projectToolWriteFile, map[string]any{"path": "web/src/App.tsx", "content": "export default function App() {}\n"})

	if len(scheduled) != 2 {
		t.Fatalf("scheduled syncs = %d, want 2", len(scheduled))
	}
	for _, sync := range scheduled {
		if sync.project.Spec.Template == nil || sync.project.Spec.Template.Name != "application" {
			t.Fatalf("%s sync project template = %#v, want application", sync.name, sync.project.Spec.Template)
		}
	}
}

func TestRefreshProjectToolSnapshotKeepsSelfAliasedProject(t *testing.T) {
	project := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "demo",
			Labels: map[string]string{"app": "demo"},
		},
		Spec: aiv1alpha1.ProjectSpec{
			DisplayName: "Demo",
			Template:    &aiv1alpha1.ProjectTemplateSpec{Name: "application"},
			Environments: []aiv1alpha1.ProjectEnvironmentSpec{{
				Name: projectDevelopmentEnvironmentName,
				Mode: aiv1alpha1.ProjectEnvironmentModeLive,
			}},
		},
	}

	refreshProjectToolSnapshot(project, project)

	if project.Spec.Template == nil || project.Spec.Template.Name != "application" {
		t.Fatalf("template = %#v, want application", project.Spec.Template)
	}
	if len(project.Spec.Environments) != 1 || project.Spec.Environments[0].Name != projectDevelopmentEnvironmentName {
		t.Fatalf("environments = %#v, want development", project.Spec.Environments)
	}
	if project.Labels["app"] != "demo" {
		t.Fatalf("labels = %#v, want app=demo", project.Labels)
	}
}
