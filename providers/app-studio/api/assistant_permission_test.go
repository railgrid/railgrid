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
	"strings"
	"testing"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func projectAssistantTestEditArgs(path string) map[string]any {
	return map[string]any{"path": path, "oldString": "old", "newString": "new", "expectedVersion": "sha256:test"}
}

func TestProjectAssistantV2PermissionPolicy(t *testing.T) {
	tests := []struct {
		name string
		risk projectAssistantToolRisk
		want projectAssistantPermissionDecision
	}{
		{name: "read", risk: projectAssistantToolRiskRead, want: projectAssistantPermissionAllow},
		{name: "plan", risk: projectAssistantToolRiskPlan, want: projectAssistantPermissionAllow},
		{name: "write", risk: projectAssistantToolRiskWrite, want: projectAssistantPermissionAsk},
		{name: "runtime", risk: projectAssistantToolRiskRuntime, want: projectAssistantPermissionAsk},
		{name: "commit", risk: projectAssistantToolRiskCommit, want: projectAssistantPermissionAsk},
		{name: "unknown", risk: projectAssistantToolRisk("unknown"), want: projectAssistantPermissionDeny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectAssistantPermissionForV2(projectAssistantToolSpec{Name: tt.name, Risk: tt.risk}, store.AssistantApprovalModeAlwaysAsk, nil, nil, false)
			if got != tt.want {
				t.Fatalf("permission = %q, want %q", got, tt.want)
			}
		})
	}
	if got := projectAssistantPermissionForV2(projectAssistantToolSpec{Name: projectToolEditFile, Risk: projectAssistantToolRiskWrite}, store.AssistantApprovalModeAutoApprove, nil, nil, false); got != projectAssistantPermissionAllow {
		t.Fatalf("auto-approved edit permission = %q, want allow", got)
	}
	if got := projectAssistantPermissionForV2(projectAssistantToolSpec{Name: projectToolEditFile, Risk: projectAssistantToolRiskWrite}, store.AssistantApprovalModeNever, nil, nil, false); got != projectAssistantPermissionDeny {
		t.Fatalf("never-approved edit permission = %q, want deny", got)
	}
}

func TestProjectAssistantWorkspaceGrantAuthorizesAllOrdinaryMutations(t *testing.T) {
	plan := &projectAssistantApprovedPlan{
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		TargetPaths:  []string{"src/", "package.json"},
	}
	tests := []struct {
		name string
		tool string
		args map[string]any
		want bool
	}{
		{name: "create", tool: projectToolCreateFile, args: map[string]any{"path": "src/App.tsx", "content": "new"}, want: true},
		{name: "edit", tool: projectToolEditFile, args: projectAssistantTestEditArgs("src/App.tsx"), want: true},
		{name: "delete", tool: projectToolDeleteFile, args: map[string]any{"path": "package.json", "expectedVersion": "sha256:test"}, want: true},
		{name: "move source and destination", tool: projectToolMoveFile, args: map[string]any{"sourcePath": "src/App.tsx", "destinationPath": "src/Main.tsx", "expectedVersion": "sha256:test"}, want: true},
		{name: "outside", tool: projectToolEditFile, args: projectAssistantTestEditArgs("README.md"), want: false},
		{name: "lookalike", tool: "provider__write_file", args: map[string]any{"path": "src/App.tsx"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectAssistantApprovedPlanAllowsWrite(plan, tt.tool, tt.args); got != tt.want {
				t.Fatalf("plan allows %s = %t, want %t", tt.tool, got, tt.want)
			}
		})
	}
}

func TestProjectAssistantWriteTargetPathsAreServerNormalized(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args map[string]any
		want []string
		fail bool
	}{
		{name: "create", tool: projectToolCreateFile, args: map[string]any{"path": "./src/App.tsx"}, want: []string{"src/App.tsx"}},
		{name: "edit", tool: projectToolEditFile, args: projectAssistantTestEditArgs("src/App.tsx"), want: []string{"src/App.tsx"}},
		{name: "delete", tool: projectToolDeleteFile, args: map[string]any{"path": "src/old.ts", "expectedVersion": "sha256:test"}, want: []string{"src/old.ts"}},
		{name: "move", tool: projectToolMoveFile, args: map[string]any{"sourcePath": "src/old.ts", "destinationPath": "src/new.ts", "expectedVersion": "sha256:test"}, want: []string{"src/old.ts", "src/new.ts"}},
		{name: "unsafe", tool: projectToolEditFile, args: projectAssistantTestEditArgs("../secret"), fail: true},
		{name: "same move", tool: projectToolMoveFile, args: map[string]any{"sourcePath": "src/a", "destinationPath": "src/a", "expectedVersion": "sha256:test"}, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := projectAssistantWriteTargetPaths(tt.tool, tt.args)
			if tt.fail {
				if err == nil {
					t.Fatal("write target paths accepted invalid input")
				}
				return
			}
			if err != nil || strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("paths = %v, err=%v, want %v", got, err, tt.want)
			}
		})
	}
}

func TestProjectAssistantExistingMutationRequiresSameTurnRead(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if _, err := files.WriteFile(ctx, scope, workspace.WriteOptions{Path: "src/App.tsx", Content: "old\n"}); err != nil {
		t.Fatal(err)
	}
	req := projectAssistantToolCallRequest{WorkspaceScope: scope, RunState: newProjectEinoAssistantRunState()}
	if err := projectAssistantRequireMutationRead(ctx, req, files, "src/App.tsx", "sha256:missing"); err == nil {
		t.Fatal("existing mutation accepted without same-turn read")
	}
	req.RunState.RecordObservedReadFile("./src/App.tsx")
	if err := projectAssistantRequireMutationRead(ctx, req, files, "src/App.tsx", "sha256:missing"); err == nil {
		t.Fatal("path-only observed read authorized existing mutation")
	}
	read, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	req.RunState.RecordObservedReadFileVersion(read.Path, read.Version)
	if err := projectAssistantRequireMutationRead(ctx, req, files, "src/App.tsx", read.Version); err != nil {
		t.Fatalf("canonical complete observed read rejected: %v", err)
	}
	if err := projectAssistantRequireMutationRead(ctx, req, files, "missing.ts", "sha256:missing"); err != nil {
		t.Fatalf("missing target should defer to typed target-not-found: %v", err)
	}
}

func TestProjectAssistantPermissionDenialDoesNotLeakMutationContent(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.ApprovePlan(projectAssistantApprovedPlan{
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		TargetPaths:  []string{"src/"},
		RunLocal:     true,
	})
	reason := projectAssistantPermissionDenialReason(projectAssistantToolSpec{Name: projectToolEditFile, Risk: projectAssistantToolRiskWrite}, state, projectAssistantTestEditArgs("README.md"), false)
	for _, want := range []string{"path_outside_approved_scope", "denied paths: README.md", "approved paths: src/"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("denial reason = %q, want %q", reason, want)
		}
	}
	if strings.Contains(reason, "old") || strings.Contains(reason, "new") {
		t.Fatalf("denial reason leaked source content: %q", reason)
	}
}

func TestProjectAssistantPermissionDeniedToolMessageIsVisibleToModel(t *testing.T) {
	msg := projectAssistantPermissionDeniedToolMessage(chatToolCall{ID: "call-1", Function: chatToolCallFunction{Name: projectToolEditFile}}, "unknown tool risk")
	if msg.Role != "tool" || msg.ToolCallID != "call-1" || msg.Name != projectToolEditFile {
		t.Fatalf("tool message = %#v", msg)
	}
	if !strings.Contains(msg.Content, "permission denied") || !strings.Contains(msg.Content, "unknown tool risk") {
		t.Fatalf("tool content = %q", msg.Content)
	}
}

func TestProjectAssistantInvalidMutationArgumentsFailClosed(t *testing.T) {
	plan := &projectAssistantApprovedPlan{Version: projectAssistantApprovedPlanVersionWorkspaceMutation, Capabilities: []string{projectAssistantCapabilityWorkspaceMutate}, TargetPaths: []string{"src/"}}
	if projectAssistantApprovedPlanAllowsWrite(plan, projectToolEditFile, map[string]any{"path": "src/a"}) {
		t.Fatal("edit without exact replacement was authorized")
	}
	if projectAssistantApprovedPlanAllowsWrite(plan, projectToolMoveFile, map[string]any{"sourcePath": "src/a"}) {
		t.Fatal("move without destination was authorized")
	}
	if err := projectAssistantValidateGrantBearingToolArguments(projectAssistantToolSpec{Name: projectToolDefineInitialProjectPlan, Risk: projectAssistantToolRiskPlan}, map[string]any{"summary": "bad", "targetPaths": []any{"src/../secret"}, "acceptanceCriteria": []any{"ok"}}); err == nil {
		t.Fatal("unsafe plan target passed grant validation")
	}
}
