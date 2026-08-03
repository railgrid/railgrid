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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	approvaltool "github.com/cloudwego/eino-examples/adk/common/tool"
	einotool "github.com/cloudwego/eino/components/tool"

	"github.com/faroshq/provider-app-studio/store"
)

func projectAssistantTestAddPatch(path string) string {
	return fmt.Sprintf("*** Begin Patch\n*** Add File: %s\n+test\n*** End Patch", path)
}

func TestProjectAssistantV2AlwaysAskPermissionPolicy(t *testing.T) {
	tests := []struct {
		name string
		risk projectAssistantToolRisk
		want projectAssistantPermissionDecision
	}{
		{name: "read tools auto allow", risk: projectAssistantToolRiskRead, want: projectAssistantPermissionAllow},
		{name: "plan tools are presentation state", risk: projectAssistantToolRiskPlan, want: projectAssistantPermissionAllow},
		{name: "write tools ask", risk: projectAssistantToolRiskWrite, want: projectAssistantPermissionAsk},
		{name: "commit tools ask", risk: projectAssistantToolRiskCommit, want: projectAssistantPermissionAsk},
		{name: "runtime tools ask", risk: projectAssistantToolRiskRuntime, want: projectAssistantPermissionAsk},
		{name: "unknown risk denies", risk: projectAssistantToolRisk("danger"), want: projectAssistantPermissionDeny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectAssistantPermissionForV2(projectAssistantToolSpec{
				Name: "tool",
				Risk: tt.risk,
			}, store.AssistantApprovalModeAlwaysAsk, nil, nil, false)
			if got != tt.want {
				t.Fatalf("permission = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectAssistantV2RuntimePermissionFollowsApprovalMode(t *testing.T) {
	spec := projectAssistantToolSpec{
		Name: projectToolRestartRuntime,
		Risk: projectAssistantToolRiskRuntime,
	}
	if decision := projectAssistantPermissionForV2(spec, store.AssistantApprovalModeAlwaysAsk, nil, nil, false); decision != projectAssistantPermissionAsk {
		t.Fatalf("always-ask runtime permission = %q, want %q", decision, projectAssistantPermissionAsk)
	}
	if decision := projectAssistantPermissionForV2(spec, store.AssistantApprovalModeAutoApprove, nil, nil, false); decision != projectAssistantPermissionAllow {
		t.Fatalf("auto-approved runtime permission = %q, want %q", decision, projectAssistantPermissionAllow)
	}
}

func TestProjectAssistantV2PlanDoesNotBypassApprovalPreference(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantApprovedPlan{
		Summary:      "Build dashboard",
		TargetPaths:  []string{"src/", "package.json"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		ApprovedAt:   testProjectAssistantApprovalTime(),
		ApprovalTool: projectToolDefineInitialProjectPlan,
		RunLocal:     true,
	})

	writeDecision := projectAssistantPermissionForV2(projectAssistantToolSpec{
		Name: projectToolApplyPatch,
		Risk: projectAssistantToolRiskWrite,
	}, store.AssistantApprovalModeAlwaysAsk, state, map[string]any{
		"patch": projectAssistantTestAddPatch("src/App.tsx"),
	}, false)
	if writeDecision != projectAssistantPermissionAsk {
		t.Fatalf("write permission = %q, want %q", writeDecision, projectAssistantPermissionAsk)
	}

	outsideDecision := projectAssistantPermissionForV2(projectAssistantToolSpec{
		Name: projectToolApplyPatch,
		Risk: projectAssistantToolRiskWrite,
	}, store.AssistantApprovalModeAlwaysAsk, state, map[string]any{
		"patch": projectAssistantTestAddPatch("README.md"),
	}, false)
	if outsideDecision != projectAssistantPermissionAsk {
		t.Fatalf("outside write permission = %q, want %q", outsideDecision, projectAssistantPermissionAsk)
	}

	commitDecision := projectAssistantPermissionForV2(projectAssistantToolSpec{
		Name: projectToolCommitProjectFiles,
		Risk: projectAssistantToolRiskCommit,
	}, store.AssistantApprovalModeAlwaysAsk, state, map[string]any{
		"paths": []any{"src/App.tsx"},
	}, false)
	if commitDecision != projectAssistantPermissionAsk {
		t.Fatalf("commit permission = %q, want %q", commitDecision, projectAssistantPermissionAsk)
	}
	autoCommitDecision := projectAssistantPermissionForV2(projectAssistantToolSpec{
		Name: projectToolCommitProjectFiles,
		Risk: projectAssistantToolRiskCommit,
	}, store.AssistantApprovalModeAutoApprove, state, map[string]any{
		"paths": []any{"src/App.tsx"},
	}, false)
	if autoCommitDecision != projectAssistantPermissionAllow {
		t.Fatalf("auto-approved commit permission = %q, want %q", autoCommitDecision, projectAssistantPermissionAllow)
	}
}

func TestProjectAssistantWorkspaceMutationGrantAllowsCanonicalEditsWithinScope(t *testing.T) {
	plan := &projectAssistantApprovedPlan{
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		TargetPaths:  []string{"src/", "package.json"},
	}

	for _, tt := range []struct {
		name string
		tool string
		path string
		want bool
	}{
		{name: "apply patch", tool: projectToolApplyPatch, path: "src/App.tsx", want: true},
		{name: "exact file", tool: projectToolApplyPatch, path: "package.json", want: true},
		{name: "outside scope", tool: projectToolApplyPatch, path: "README.md"},
		{name: "unknown write tool", tool: "custom_write_tool", path: "src/App.tsx"},
		{name: "namespaced write lookalike", tool: "provider__write_file", path: "src/App.tsx"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{"path": tt.path}
			if tt.tool == projectToolApplyPatch {
				args = map[string]any{"patch": projectAssistantTestAddPatch(tt.path)}
			}
			got := projectAssistantApprovedPlanAllowsWrite(plan, tt.tool, args)
			if got != tt.want {
				t.Fatalf("plan allows %s on %q = %t, want %t", tt.tool, tt.path, got, tt.want)
			}
		})
	}
}

func TestProjectAssistantV2InitialExecutionPlanIsPresentationState(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	spec := projectAssistantToolSpec{
		Name: projectToolDefineInitialProjectPlan,
		Risk: projectAssistantToolRiskPlan,
	}
	args := map[string]any{
		"summary":            "Build the app",
		"targetPaths":        []any{"src/"},
		"acceptanceCriteria": []any{"The app starts"},
	}
	if decision := projectAssistantPermissionForV2(spec, store.AssistantApprovalModeAutoApprove, state, args, false); decision != projectAssistantPermissionAllow {
		t.Fatalf("unbound initial plan permission = %q, want allow", decision)
	}
	state.ApprovePlan(projectAssistantInitialCreationPlan("Build the app"))
	if decision := projectAssistantPermissionForV2(spec, store.AssistantApprovalModeAlwaysAsk, state, args, false); decision != projectAssistantPermissionAllow {
		t.Fatalf("run-local initial plan permission = %q, want allow", decision)
	}
}

func TestProjectAssistantInitialExecutionPlanDoesNotNarrowCreationAuthority(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantInitialCreationPlan("Build a storefront"))
	state.SetExecutionPlan(projectAssistantApprovedPlan{
		Goal:         "Build a storefront",
		TargetPaths:  []string{"web/"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		ApprovalTool: projectToolDefineInitialProjectPlan,
		RunLocal:     true,
	})

	authority := state.ApprovedPlan()
	if authority == nil || !authority.AllowAllWrites || authority.ApprovalTool != "project_create_prompt" {
		t.Fatalf("creation authority = %#v, want unchanged user-derived workspace grant", authority)
	}
	decision := projectAssistantPermissionForV2(
		projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
		store.AssistantApprovalModeAlwaysAsk,
		state,
		map[string]any{"patch": projectAssistantTestAddPatch("package.json")},
		false,
	)
	if decision != projectAssistantPermissionAsk {
		t.Fatalf("root write permission = %q, want approval independent of informational web/ plan", decision)
	}
}

func TestProjectAssistantInitialSourceWriteApprovalIsIndependentOfTemplateBinding(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantInitialCreationPlan("Build a storefront"))
	spec := projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite}
	args := map[string]any{"patch": projectAssistantTestAddPatch("package.json")}
	decision := projectAssistantPermissionForV2(
		spec,
		store.AssistantApprovalModeAutoApprove,
		state,
		args,
		true,
	)
	if decision != projectAssistantPermissionAllow {
		t.Fatalf("unbound-template write permission = %q, want approval policy decision", decision)
	}
}

func TestProjectAssistantPermissionDenialReportsDeniedAndAuthoritativePaths(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantApprovedPlan{
		TargetPaths:  []string{"web/"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		ApprovalTool: projectToolDefineInitialProjectPlan,
		RunLocal:     true,
	})
	state.SetSessionSnapshot(projectEinoAssistantSessionSnapshot{
		DevelopmentComponents: map[string]projectTemplateComponent{
			"app": {WorkspacePath: "."},
		},
	})
	reason := projectAssistantPermissionDenialReason(
		projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
		state,
		map[string]any{"patch": projectAssistantTestAddPatch("package.json")},
		false,
	)
	for _, want := range []string{"path_outside_approved_scope", "denied paths: package.json", "approved paths: web/", "development component roots: ."} {
		if !strings.Contains(reason, want) {
			t.Fatalf("denial reason = %q, want %q", reason, want)
		}
	}
}

func TestProjectAssistantLegacyAutoApproveDoesNotDeriveAuthorityFromPlan(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantApprovedPlan{
		Summary:      "Update the app shell",
		TargetPaths:  []string{"src/"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		ApprovedAt:   testProjectAssistantApprovalTime(),
		ApprovalTool: projectToolDefineInitialProjectPlan,
		RunLocal:     true,
	})

	tests := []struct {
		name string
		spec projectAssistantToolSpec
		args map[string]any
		want projectAssistantPermissionDecision
	}{
		{
			name: "approved operation and path are allowed",
			spec: projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"patch": projectAssistantTestAddPatch("src/App.tsx")},
			want: projectAssistantPermissionAllow,
		},
		{
			name: "outside path is independent of model-authored plan scope",
			spec: projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"patch": projectAssistantTestAddPatch("package.json")},
			want: projectAssistantPermissionAllow,
		},
		{
			name: "local auto-approval can authorize template selection independently",
			spec: projectAssistantToolSpec{Name: projectToolSelectTemplate, Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"template": "simple-webapp"},
			want: projectAssistantPermissionAllow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectAssistantPermissionForV2(tt.spec, store.AssistantApprovalModeAutoApprove, state, tt.args, true); got != tt.want {
				t.Fatalf("permission = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectAssistantInitialCreationWildcardAuthorizesAnyPath(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantApprovedPlan{
		Summary:        "Initial project creation prompt authorizes source edits for this run.",
		Version:        projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities:   []string{projectAssistantCapabilityWorkspaceMutate},
		AllowAllWrites: true,
		ApprovedAt:     testProjectAssistantApprovalTime(),
		ApprovalTool:   "project_create_prompt",
		RunLocal:       true,
	})

	for _, path := range []string{"src/App.tsx", "README.md", "deploy/values.yaml"} {
		decision := projectAssistantPermissionForV2(projectAssistantToolSpec{
			Name: projectToolApplyPatch,
			Risk: projectAssistantToolRiskWrite,
		}, store.AssistantApprovalModeAlwaysAsk, state, map[string]any{
			"patch": projectAssistantTestAddPatch(path),
		}, false)
		if decision != projectAssistantPermissionAsk {
			t.Fatalf("write permission for %q = %q, want %q", path, decision, projectAssistantPermissionAsk)
		}
	}

	commitDecision := projectAssistantPermissionForV2(projectAssistantToolSpec{
		Name: projectToolCommitProjectFiles,
		Risk: projectAssistantToolRiskCommit,
	}, store.AssistantApprovalModeAlwaysAsk, state, map[string]any{
		"paths": []any{"src/App.tsx"},
	}, false)
	if commitDecision != projectAssistantPermissionAsk {
		t.Fatalf("commit permission = %q, want %q", commitDecision, projectAssistantPermissionAsk)
	}
}

func TestProjectAssistantWorkspaceGrantRejectsUnsafePaths(t *testing.T) {
	tests := []struct {
		name      string
		approved  string
		candidate string
	}{
		{name: "traversal", approved: "secrets/app.ts", candidate: "src/../secrets/app.ts"},
		{name: "absolute", approved: "src/App.tsx", candidate: "/src/App.tsx"},
		{name: "reserved", approved: ".git/config", candidate: ".git/config"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := &projectAssistantApprovedPlan{
				TargetPaths:  []string{tt.approved},
				Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
				Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
			}
			if projectAssistantApprovedPlanAllowsWrite(plan, projectToolApplyPatch, map[string]any{"patch": projectAssistantTestAddPatch(tt.candidate)}) {
				t.Fatalf("unsafe path %q was authorized by grant %#v", tt.candidate, plan.TargetPaths)
			}
		})
	}
}

func TestProjectAssistantUnsafePlanTargetIsDenied(t *testing.T) {
	err := projectAssistantValidateGrantBearingToolArguments(
		projectAssistantToolSpec{Name: projectToolDefineInitialProjectPlan, Risk: projectAssistantToolRiskPlan},
		map[string]any{
			"summary":            "Unsafe plan",
			"targetPaths":        []any{"src/../secrets/"},
			"acceptanceCriteria": []any{"The app starts"},
		},
	)
	if err == nil {
		t.Fatal("unsafe plan target passed V2 grant validation")
	}
}

func TestProjectAssistantInitialCreationPlanDoesNotOverrideApprovalPolicy(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantInitialCreationPlan())

	decision := projectAssistantPermissionForV2(projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite}, store.AssistantApprovalModeAlwaysAsk, state, map[string]any{"patch": projectAssistantTestAddPatch("src/App.tsx")}, false)
	if decision != projectAssistantPermissionAsk {
		t.Fatalf("%s permission = %q, want ask", projectToolApplyPatch, decision)
	}
	if decision := projectAssistantPermissionForV2(
		projectAssistantToolSpec{Name: projectToolSelectTemplate, Risk: projectAssistantToolRiskWrite},
		store.AssistantApprovalModeAlwaysAsk,
		state,
		map[string]any{"template": "simple-webapp"},
		false,
	); decision != projectAssistantPermissionAsk {
		t.Fatalf("%s permission = %q, want ask", projectToolSelectTemplate, decision)
	}
	if decision := projectAssistantPermissionForV2(
		projectAssistantToolSpec{Name: projectToolSelectTemplate, Risk: projectAssistantToolRiskWrite},
		store.AssistantApprovalModeAutoApprove,
		state,
		map[string]any{"template": "simple-webapp"},
		true,
	); decision != projectAssistantPermissionAllow {
		t.Fatalf("%s permission during template bootstrap = %q, want allow", projectToolSelectTemplate, decision)
	}
	for _, tt := range []struct {
		spec projectAssistantToolSpec
		want projectAssistantPermissionDecision
	}{
		{spec: projectAssistantToolSpec{Name: projectToolRestartRuntime, Risk: projectAssistantToolRiskRuntime}, want: projectAssistantPermissionAsk},
		{spec: projectAssistantToolSpec{Name: projectToolInfrastructureProvision, Risk: projectAssistantToolRiskWrite}, want: projectAssistantPermissionAsk},
		{spec: projectAssistantToolSpec{Name: projectToolCommitProjectFiles, Risk: projectAssistantToolRiskCommit}, want: projectAssistantPermissionAsk},
	} {
		decision := projectAssistantPermissionForV2(tt.spec, store.AssistantApprovalModeAlwaysAsk, state, map[string]any{"path": "src/App.tsx"}, false)
		if decision != tt.want {
			t.Fatalf("%s permission = %q, want %q", tt.spec.Name, decision, tt.want)
		}
	}
}

func TestProjectAssistantInitialExecutionPlanDoesNotAuthorizeOutOfScopeWrites(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Goal:         "Build the app",
		Steps:        []string{"Build"},
		TargetPaths:  []string{"src/"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		ApprovalTool: projectToolDefineInitialProjectPlan,
		RunLocal:     true,
	}))
	decision := projectAssistantPermissionForV2(
		projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
		store.AssistantApprovalModeAlwaysAsk,
		state,
		map[string]any{"patch": projectAssistantTestAddPatch("package.json")},
		false,
	)
	if decision != projectAssistantPermissionAsk {
		t.Fatalf("out-of-scope initial write permission = %q, want ask", decision)
	}
}

func TestProjectAssistantPermissionReasonsDescribeExactActionAndTarget(t *testing.T) {
	tests := []struct {
		name string
		spec projectAssistantToolSpec
		args map[string]any
		want []string
	}{
		{
			name: "template selection",
			spec: projectAssistantToolSpec{Name: projectToolSelectTemplate, Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"template": "application"},
			want: []string{`template "application"`, "tears down", "re-provisions"},
		},
		{
			name: "runtime component restart",
			spec: projectAssistantToolSpec{Name: projectToolRestartRuntime, Risk: projectAssistantToolRiskRuntime},
			args: map[string]any{"component": "api"},
			want: []string{`component "api"`},
		},
		{
			name: "infrastructure provisioning",
			spec: projectAssistantToolSpec{Name: projectToolInfrastructureProvision, Risk: projectAssistantToolRiskRuntime},
			args: map[string]any{"template": "postgres", "name": "orders-db"},
			want: []string{`instance "orders-db"`, `template "postgres"`},
		},
		{
			name: "production promotion",
			spec: projectAssistantToolSpec{Name: projectToolPromoteProject, Risk: projectAssistantToolRiskRuntime},
			want: []string{"production environment"},
		},
		{
			name: "build workflow retry",
			spec: projectAssistantToolSpec{Name: projectToolRebuildProject, Risk: projectAssistantToolRiskRuntime},
			args: map[string]any{"ref": "feature/orders"},
			want: []string{"build workflow", `"feature/orders"`, "without changing code"},
		},
		{
			name: "path scoped plan capability",
			spec: projectAssistantToolSpec{Name: projectToolDefineInitialProjectPlan, Risk: projectAssistantToolRiskPlan},
			args: map[string]any{"targetPaths": []any{"src/", "package.json"}},
			want: []string{`"src/"`, `"package.json"`, "workspace edit tools", "until the next commit request"},
		},
		{
			name: "direct patch capability",
			spec: projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"patch": projectAssistantTestAddPatch("src/App.tsx")},
			want: []string{`"src/App.tsx"`, "this workspace edit"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectAssistantPermissionReasonForArguments(tt.spec, tt.args)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("reason = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestProjectAssistantRunLocalPlanWithoutCapabilityStillDoesNotDecideWrites(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantApprovedPlan{
		Summary:      "Build dashboard",
		TargetPaths:  []string{"src/"},
		ApprovedAt:   testProjectAssistantApprovalTime(),
		ApprovalTool: projectToolDefineInitialProjectPlan,
		RunLocal:     true,
	})

	decision := projectAssistantPermissionForV2(projectAssistantToolSpec{
		Name: projectToolApplyPatch,
		Risk: projectAssistantToolRiskWrite,
	}, store.AssistantApprovalModeAlwaysAsk, state, map[string]any{
		"patch": projectAssistantTestAddPatch("src/App.tsx"),
	}, false)
	if decision != projectAssistantPermissionAsk {
		t.Fatalf("write permission = %q, want %q", decision, projectAssistantPermissionAsk)
	}
}

func TestProjectAssistantPermissionDeniedToolMessageIsVisibleToModel(t *testing.T) {
	msg := projectAssistantPermissionDeniedToolMessage(chatToolCall{
		ID: "call-1",
		Function: chatToolCallFunction{
			Name: "dangerous_tool",
		},
	}, "unknown tool risk")
	if msg.Role != "tool" || msg.ToolCallID != "call-1" || msg.Name != "dangerous_tool" {
		t.Fatalf("tool message = %#v, want model-visible tool response", msg)
	}
	if !strings.Contains(msg.Content, "permission denied") || !strings.Contains(msg.Content, "unknown tool risk") {
		t.Fatalf("tool content = %q, want permission denial reason", msg.Content)
	}
}

func TestProjectAssistantV2AutoApproveNeverAsks(t *testing.T) {
	tests := []struct {
		name             string
		spec             projectAssistantToolSpec
		args             map[string]any
		initialAuthority bool
		want             projectAssistantPermissionDecision
	}{
		{
			name: "valid initial execution plan",
			spec: projectAssistantToolSpec{Name: projectToolDefineInitialProjectPlan, Risk: projectAssistantToolRiskPlan},
			args: map[string]any{
				"summary":            "Update the app",
				"targetPaths":        []any{"src"},
				"acceptanceCriteria": []any{"The app starts"},
			},
			initialAuthority: true,
			want:             projectAssistantPermissionAllow,
		},
		{
			name: "commit",
			spec: projectAssistantToolSpec{Name: projectToolCommitProjectFiles, Risk: projectAssistantToolRiskCommit},
			want: projectAssistantPermissionAllow,
		},
		{
			name: "runtime",
			spec: projectAssistantToolSpec{Name: projectToolRestartRuntime, Risk: projectAssistantToolRiskRuntime},
			want: projectAssistantPermissionAllow,
		},
		{
			name: "template selection",
			spec: projectAssistantToolSpec{Name: projectToolSelectTemplate, Risk: projectAssistantToolRiskWrite},
			want: projectAssistantPermissionAllow,
		},
		{
			name: "authorized Default source write",
			spec: projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"patch": projectAssistantTestAddPatch("src/App.tsx")},
			want: projectAssistantPermissionAllow,
		},
		{
			name: "unknown risk denied",
			spec: projectAssistantToolSpec{Name: "unknown"},
			want: projectAssistantPermissionDeny,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newProjectEinoAssistantRunState()
			if tt.initialAuthority {
				state.ApprovePlan(projectAssistantInitialCreationPlan("Update the app"))
			}
			got := projectAssistantPermissionForV2(
				tt.spec,
				store.AssistantApprovalModeAutoApprove,
				state,
				tt.args,
				true,
			)
			if got != tt.want {
				t.Fatalf("permission = %q, want %q", got, tt.want)
			}
			if got == projectAssistantPermissionAsk {
				t.Fatal("auto-approve returned an approval interrupt")
			}
		})
	}
}

func TestProjectAssistantV2AlwaysAskRequiresApprovalForEffects(t *testing.T) {
	tests := []struct {
		name string
		spec projectAssistantToolSpec
		args map[string]any
	}{
		{name: "write", spec: projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite}, args: map[string]any{"patch": projectAssistantTestAddPatch("src/App.tsx")}},
		{
			name: "runtime",
			spec: projectAssistantToolSpec{Name: projectToolRestartRuntime, Risk: projectAssistantToolRiskRuntime},
		},
		{name: "commit", spec: projectAssistantToolSpec{Name: projectToolCommitProjectFiles, Risk: projectAssistantToolRiskCommit}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectAssistantPermissionForV2(
				tt.spec,
				store.AssistantApprovalModeAlwaysAsk,
				newProjectEinoAssistantRunState(),
				tt.args,
				false,
			)
			if got != projectAssistantPermissionAsk {
				t.Fatalf("permission = %q, want explicit user preference to ask", got)
			}
		})
	}
}

func TestProjectAssistantRuntimeGraphToolsRespectApprovalMode(t *testing.T) {
	tests := []struct {
		name string
		new  func(projectAssistantWorkflowRunContext) (einotool.BaseTool, error)
	}{
		{name: "restart", new: newProjectAssistantRestartRuntimeGraphTool},
		{name: "set runtime env", new: newProjectAssistantSetRuntimeEnvGraphTool},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admitMutation := func(context.Context) error { return nil }
			for _, mode := range []store.AssistantApprovalMode{
				store.AssistantApprovalModeOnRequest,
				store.AssistantApprovalModeAutoApprove,
				store.AssistantApprovalModeNever,
			} {
				autoTool, err := tt.new(projectAssistantWorkflowRunContext{ApprovalMode: mode})
				if err != nil {
					t.Fatalf("create %s tool: %v", mode, err)
				}
				if _, wrapped := autoTool.(approvaltool.InvokableApprovableTool); wrapped {
					t.Fatalf("%s runtime tool retained an approval wrapper", mode)
				}
			}

			askTool, err := tt.new(projectAssistantWorkflowRunContext{ApprovalMode: store.AssistantApprovalModeAlwaysAsk})
			if err != nil {
				t.Fatalf("create always-ask tool: %v", err)
			}
			if _, wrapped := askTool.(approvaltool.InvokableApprovableTool); !wrapped {
				t.Fatal("always-ask runtime tool is missing its approval wrapper")
			}

			ledger := newProjectAssistantRunEventLedger(store.NewMemoryStore(), store.Scope{
				OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo",
			}, "run-v2")
			durableAuto, err := tt.new(projectAssistantWorkflowRunContext{
				ApprovalMode:  store.AssistantApprovalModeAutoApprove,
				EventLedger:   ledger,
				AdmitMutation: admitMutation,
			})
			if err != nil {
				t.Fatalf("create durable auto-approve tool: %v", err)
			}
			if _, ok := durableAuto.(projectAssistantDurableGraphTool); !ok {
				t.Fatalf("durable auto-approve tool = %T, want ledger wrapper", durableAuto)
			}
			durableAsk, err := tt.new(projectAssistantWorkflowRunContext{
				ApprovalMode:  store.AssistantApprovalModeAlwaysAsk,
				EventLedger:   ledger,
				AdmitMutation: admitMutation,
			})
			if err != nil {
				t.Fatalf("create durable always-ask tool: %v", err)
			}
			approval, ok := durableAsk.(approvaltool.InvokableApprovableTool)
			if !ok {
				t.Fatalf("durable always-ask tool = %T, want approval wrapper", durableAsk)
			}
			if _, ok := approval.InvokableTool.(projectAssistantDurableGraphTool); !ok {
				t.Fatalf("approval inner tool = %T, want ledger inside approval boundary", approval.InvokableTool)
			}
		})
	}
}

func TestProjectAssistantDurableRuntimeGraphToolRejectsStoppedRun(t *testing.T) {
	tool := projectAssistantDurableGraphTool{
		spec: projectAssistantToolSpec{Name: projectToolRestartRuntime, Risk: projectAssistantToolRiskRuntime},
		admitMutation: func(context.Context) error {
			return store.ErrAssistantRunConflict
		},
	}
	if _, err := tool.InvokableRun(context.Background(), `{}`); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("InvokableRun error = %v, want stopped-run conflict", err)
	}
}

func testProjectAssistantApprovalTime() time.Time {
	return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
}
