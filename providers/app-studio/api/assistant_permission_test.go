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
		Name: projectToolDeployProjectRuntime,
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
		Operations:   []string{projectToolWriteFile, projectToolApplyPatch, projectToolMkdir},
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

func TestProjectAssistantPlanApprovalWithoutOperationsDoesNotAuthorizeWrites(t *testing.T) {
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
