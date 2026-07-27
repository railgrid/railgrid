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
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProjectAssistantPermissionPolicy(t *testing.T) {
	tests := []struct {
		name string
		risk projectAssistantToolRisk
		want projectAssistantPermissionDecision
	}{
		{name: "read tools auto allow", risk: projectAssistantToolRiskRead, want: projectAssistantPermissionAllow},
		{name: "plan approval asks", risk: projectAssistantToolRiskPlan, want: projectAssistantPermissionAsk},
		{name: "write tools ask", risk: projectAssistantToolRiskWrite, want: projectAssistantPermissionAsk},
		{name: "commit tools ask", risk: projectAssistantToolRiskCommit, want: projectAssistantPermissionAsk},
		{name: "runtime tools ask", risk: projectAssistantToolRiskRuntime, want: projectAssistantPermissionAsk},
		{name: "unknown risk denies", risk: projectAssistantToolRisk("danger"), want: projectAssistantPermissionDeny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectAssistantPermissionForTool(projectAssistantToolSpec{
				Name: "tool",
				Risk: tt.risk,
			})
			if got != tt.want {
				t.Fatalf("permission = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectAssistantRuntimePermissionIgnoresAutoApprove(t *testing.T) {
	decision := projectAssistantPermissionForToolWithPolicy(projectAssistantToolSpec{
		Name: projectToolRestartRuntime,
		Risk: projectAssistantToolRiskRuntime,
	}, true)
	if decision != projectAssistantPermissionAsk {
		t.Fatalf("auto-approved runtime permission = %q, want %q", decision, projectAssistantPermissionAsk)
	}
}

func TestProjectAssistantPlanApprovalAllowsScopedWritesButNotCommit(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantApprovedPlan{
		Summary:      "Build dashboard",
		TargetPaths:  []string{"src/", "package.json"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		ApprovedAt:   testProjectAssistantApprovalTime(),
		ApprovalTool: projectToolRequestProjectPlanApproval,
	})

	writeDecision := projectAssistantPermissionForToolWithRunState(projectAssistantToolSpec{
		Name: projectToolWriteFile,
		Risk: projectAssistantToolRiskWrite,
	}, false, state, map[string]any{
		"path": "src/App.tsx",
	})
	if writeDecision != projectAssistantPermissionAllow {
		t.Fatalf("write permission = %q, want %q", writeDecision, projectAssistantPermissionAllow)
	}

	outsideDecision := projectAssistantPermissionForToolWithRunState(projectAssistantToolSpec{
		Name: projectToolWriteFile,
		Risk: projectAssistantToolRiskWrite,
	}, false, state, map[string]any{
		"path": "README.md",
	})
	if outsideDecision != projectAssistantPermissionAsk {
		t.Fatalf("outside write permission = %q, want %q", outsideDecision, projectAssistantPermissionAsk)
	}

	commitDecision := projectAssistantPermissionForToolWithRunState(projectAssistantToolSpec{
		Name: projectToolCommitProjectFiles,
		Risk: projectAssistantToolRiskCommit,
	}, false, state, map[string]any{
		"paths": []any{"src/App.tsx"},
	})
	if commitDecision != projectAssistantPermissionAsk {
		t.Fatalf("commit permission = %q, want %q", commitDecision, projectAssistantPermissionAsk)
	}
	autoCommitDecision := projectAssistantPermissionForToolWithRunState(projectAssistantToolSpec{
		Name: projectToolCommitProjectFiles,
		Risk: projectAssistantToolRiskCommit,
	}, true, state, map[string]any{
		"paths": []any{"src/App.tsx"},
	})
	if autoCommitDecision != projectAssistantPermissionAsk {
		t.Fatalf("auto-approved commit permission = %q, want %q", autoCommitDecision, projectAssistantPermissionAsk)
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
		{name: "write file", tool: projectToolWriteFile, path: "src/App.tsx", want: true},
		{name: "apply patch", tool: projectToolApplyPatch, path: "src/App.tsx", want: true},
		{name: "make directory", tool: projectToolMkdir, path: "src/components", want: true},
		{name: "exact file", tool: projectToolApplyPatch, path: "package.json", want: true},
		{name: "outside scope", tool: projectToolWriteFile, path: "README.md"},
		{name: "unknown write tool", tool: "custom_write_tool", path: "src/App.tsx"},
		{name: "namespaced write lookalike", tool: "provider__write_file", path: "src/App.tsx"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := projectAssistantApprovedPlanAllowsWrite(plan, tt.tool, map[string]any{"path": tt.path})
			if got != tt.want {
				t.Fatalf("plan allows %s on %q = %t, want %t", tt.tool, tt.path, got, tt.want)
			}
		})
	}
}

func TestProjectAssistantOperationOnlyGrantIsInactive(t *testing.T) {
	for _, raw := range []string{
		`{"targetPaths":["src/"],"operations":["write_file"]}`,
		`{"operations":["write_file"],"allowAllWrites":true}`,
	} {
		var plan projectAssistantApprovedPlan
		if err := json.Unmarshal([]byte(raw), &plan); err != nil {
			t.Fatalf("decode obsolete grant: %v", err)
		}
		if projectAssistantApprovedPlanActive(&plan) {
			t.Fatalf("obsolete grant should be inactive after capability migration: %s", raw)
		}
		if projectAssistantApprovedPlanAllowsWrite(&plan, projectToolWriteFile, map[string]any{"path": "src/App.tsx"}) {
			t.Fatalf("obsolete grant must not authorize workspace mutation: %s", raw)
		}
	}
}

func TestProjectAssistantPlanScopeExpansionRequiresApproval(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantApprovedPlan{
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		TargetPaths:  []string{"src/"},
	})
	spec := projectAssistantToolSpec{
		Name: projectToolRequestProjectPlanApproval,
		Risk: projectAssistantToolRiskPlan,
	}

	sameScope := projectAssistantPermissionForToolWithRunState(spec, false, state, map[string]any{
		"targetPaths": []any{"src/App.tsx"},
	})
	if sameScope != projectAssistantPermissionAllow {
		t.Fatalf("same-scope plan permission = %q, want %q", sameScope, projectAssistantPermissionAllow)
	}

	expandedScope := projectAssistantPermissionForToolWithRunState(spec, false, state, map[string]any{
		"targetPaths": []any{"src/", "secrets/"},
	})
	if expandedScope != projectAssistantPermissionAsk {
		t.Fatalf("expanded-scope plan permission = %q, want %q", expandedScope, projectAssistantPermissionAsk)
	}
}

func TestProjectAssistantAutoApprovePreservesApprovedPlanScope(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantApprovedPlan{
		Summary:      "Update the app shell",
		TargetPaths:  []string{"src/"},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		ApprovedAt:   testProjectAssistantApprovalTime(),
		ApprovalTool: projectToolRequestProjectPlanApproval,
	})

	tests := []struct {
		name string
		spec projectAssistantToolSpec
		args map[string]any
		want projectAssistantPermissionDecision
	}{
		{
			name: "approved operation and path are allowed",
			spec: projectAssistantToolSpec{Name: projectToolWriteFile, Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"path": "src/App.tsx"},
			want: projectAssistantPermissionAllow,
		},
		{
			name: "outside path requires replanning without a headless write prompt",
			spec: projectAssistantToolSpec{Name: projectToolWriteFile, Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"path": "package.json"},
			want: projectAssistantPermissionDecision("replan"),
		},
		{
			name: "alternate canonical edit tool is allowed by the capability",
			spec: projectAssistantToolSpec{Name: projectToolApplyPatch, Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"path": "src/App.tsx"},
			want: projectAssistantPermissionAllow,
		},
		{
			name: "write tool outside the plan authorization model remains denied",
			spec: projectAssistantToolSpec{Name: "custom_write_tool", Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"path": "src/App.tsx"},
			want: projectAssistantPermissionDeny,
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
			if got := projectAssistantPermissionForToolWithRunState(tt.spec, true, state, tt.args); got != tt.want {
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
		decision := projectAssistantPermissionForToolWithRunState(projectAssistantToolSpec{
			Name: projectToolWriteFile,
			Risk: projectAssistantToolRiskWrite,
		}, false, state, map[string]any{
			"path": path,
		})
		if decision != projectAssistantPermissionAllow {
			t.Fatalf("write permission for %q = %q, want %q", path, decision, projectAssistantPermissionAllow)
		}
	}

	commitDecision := projectAssistantPermissionForToolWithRunState(projectAssistantToolSpec{
		Name: projectToolCommitProjectFiles,
		Risk: projectAssistantToolRiskCommit,
	}, false, state, map[string]any{
		"paths": []any{"src/App.tsx"},
	})
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
			if projectAssistantApprovedPlanAllowsWrite(plan, projectToolWriteFile, map[string]any{"path": tt.candidate}) {
				t.Fatalf("unsafe path %q was authorized by grant %#v", tt.candidate, plan.TargetPaths)
			}
		})
	}
}

func TestProjectAssistantUnsafePlanTargetIsDenied(t *testing.T) {
	decision := projectAssistantPermissionForToolWithRunState(
		projectAssistantToolSpec{Name: projectToolRequestProjectPlanApproval, Risk: projectAssistantToolRiskPlan},
		false,
		newProjectEinoAssistantRunState(),
		map[string]any{"targetPaths": []any{"src/../secrets/"}},
	)
	if decision != projectAssistantPermissionDeny {
		t.Fatalf("unsafe plan permission = %q, want %q", decision, projectAssistantPermissionDeny)
	}
}

func TestProjectAssistantInitialCreationGrantAllowsSourceEditsButNotTemplateSelection(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantInitialCreationPlan())

	for _, tool := range []string{projectToolWriteFile, projectToolApplyPatch, projectToolMkdir} {
		decision := projectAssistantPermissionForToolWithRunState(projectAssistantToolSpec{Name: tool, Risk: projectAssistantToolRiskWrite}, false, state, map[string]any{"path": "src/App.tsx"})
		if decision != projectAssistantPermissionAllow {
			t.Fatalf("%s permission = %q, want allow", tool, decision)
		}
	}
	if decision := projectAssistantPermissionForToolWithRunState(
		projectAssistantToolSpec{Name: projectToolSelectTemplate, Risk: projectAssistantToolRiskWrite},
		false,
		state,
		map[string]any{"template": "simple-webapp"},
	); decision != projectAssistantPermissionAsk {
		t.Fatalf("%s permission = %q, want explicit approval", projectToolSelectTemplate, decision)
	}
	if decision := projectAssistantPermissionForToolWithRunState(
		projectAssistantToolSpec{Name: projectToolSelectTemplate, Risk: projectAssistantToolRiskWrite},
		true,
		state,
		map[string]any{"template": "simple-webapp"},
	); decision != projectAssistantPermissionAllow {
		t.Fatalf("%s permission with local auto-approval = %q, want allow", projectToolSelectTemplate, decision)
	}
	for _, spec := range []projectAssistantToolSpec{
		{Name: projectToolHydrateWorkspace, Risk: projectAssistantToolRiskWrite},
		{Name: projectToolRestartRuntime, Risk: projectAssistantToolRiskRuntime},
		{Name: projectToolInfrastructureProvision, Risk: projectAssistantToolRiskWrite},
		{Name: projectToolCommitProjectFiles, Risk: projectAssistantToolRiskCommit},
	} {
		decision := projectAssistantPermissionForToolWithRunState(spec, false, state, map[string]any{"path": "src/App.tsx"})
		if decision != projectAssistantPermissionAsk {
			t.Fatalf("%s permission = %q, want ask", spec.Name, decision)
		}
	}
}

func TestProjectAssistantDirectApprovalGrantsWritePlanOnlyForSourceEdits(t *testing.T) {
	for _, tt := range []struct {
		name string
		tool string
		want bool
	}{
		{name: "write file", tool: projectToolWriteFile, want: true},
		{name: "apply patch", tool: projectToolApplyPatch, want: true},
		{name: "mkdir", tool: projectToolMkdir, want: true},
		{name: "template selection", tool: projectToolSelectTemplate},
		{name: "infrastructure provision", tool: projectToolInfrastructureProvision},
		{name: "namespaced write lookalike", tool: "provider__write_file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectAssistantDirectApprovalGrantsWritePlan(tt.tool); got != tt.want {
				t.Fatalf("direct approval grants write plan for %q = %t, want %t", tt.tool, got, tt.want)
			}
		})
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
			spec: projectAssistantToolSpec{Name: projectToolRequestProjectPlanApproval, Risk: projectAssistantToolRiskPlan},
			args: map[string]any{"targetPaths": []any{"src/", "package.json"}},
			want: []string{`"src/"`, `"package.json"`, "workspace edit tools", "until the next commit request"},
		},
		{
			name: "direct write capability",
			spec: projectAssistantToolSpec{Name: projectToolWriteFile, Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"path": "src/App.tsx"},
			want: []string{`"src/App.tsx"`, "workspace edit tools", "until the next commit request"},
		},
		{
			name: "direct mkdir subtree capability",
			spec: projectAssistantToolSpec{Name: projectToolMkdir, Risk: projectAssistantToolRiskWrite},
			args: map[string]any{"path": "src/components"},
			want: []string{`"src/components/"`, "workspace edit tools", "until the next commit request"},
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

func TestProjectAssistantPlanApprovalWithoutCapabilityDoesNotAuthorizeWrites(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantApprovedPlan{
		Summary:      "Build dashboard",
		TargetPaths:  []string{"src/"},
		ApprovedAt:   testProjectAssistantApprovalTime(),
		ApprovalTool: projectToolRequestProjectPlanApproval,
	})

	decision := projectAssistantPermissionForToolWithRunState(projectAssistantToolSpec{
		Name: projectToolWriteFile,
		Risk: projectAssistantToolRiskWrite,
	}, false, state, map[string]any{
		"path": "src/App.tsx",
	})
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

func testProjectAssistantApprovalTime() time.Time {
	return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
}
