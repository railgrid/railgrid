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
	"fmt"
	pathpkg "path"
	"strings"
)

type projectAssistantPermissionDecision string

const (
	projectAssistantPermissionAllow projectAssistantPermissionDecision = "allow"
	projectAssistantPermissionAsk   projectAssistantPermissionDecision = "ask"
	projectAssistantPermissionDeny  projectAssistantPermissionDecision = "deny"
)

func projectAssistantPermissionForTool(spec projectAssistantToolSpec) projectAssistantPermissionDecision {
	return projectAssistantPermissionForToolWithPolicy(spec, false)
}

func projectAssistantPermissionForToolWithPolicy(spec projectAssistantToolSpec, autoApprove bool) projectAssistantPermissionDecision {
	return projectAssistantPermissionForToolWithRunState(spec, autoApprove, nil, nil)
}

func projectAssistantPermissionForToolWithRunState(spec projectAssistantToolSpec, autoApprove bool, runState *projectEinoAssistantRunState, args map[string]any) projectAssistantPermissionDecision {
	switch spec.Risk {
	case projectAssistantToolRiskRead, projectAssistantToolRiskInput:
		return projectAssistantPermissionAllow
	case projectAssistantToolRiskPlan:
		// The first plan in a commit cycle prompts the user. Once a grant is
		// active it stays active until the next commit, so re-stating the plan
		// on later turns must not re-prompt — it only widens the envelope.
		if autoApprove || projectAssistantApprovedPlanActive(runState.ApprovedPlan()) {
			return projectAssistantPermissionAllow
		}
		return projectAssistantPermissionAsk
	case projectAssistantToolRiskWrite:
		if autoApprove || projectAssistantApprovedPlanAllowsWrite(runState.ApprovedPlan(), spec.Name, args) {
			return projectAssistantPermissionAllow
		}
		return projectAssistantPermissionAsk
	case projectAssistantToolRiskCommit:
		return projectAssistantPermissionAsk
	case projectAssistantToolRiskRuntime:
		return projectAssistantPermissionAsk
	default:
		return projectAssistantPermissionDeny
	}
}

func projectAssistantApprovedPlanActive(plan *projectAssistantApprovedPlan) bool {
	return plan != nil && len(plan.Operations) > 0
}

func projectAssistantApprovedPlanAllowsWrite(plan *projectAssistantApprovedPlan, toolName string, args map[string]any) bool {
	if plan == nil {
		return false
	}
	toolName = projectToolBaseName(toolName)
	switch toolName {
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
	default:
		return false
	}
	if !projectAssistantApprovedPlanAllowsOperation(plan, toolName) {
		return false
	}
	targetPath := projectAssistantWriteTargetPath(toolName, args)
	if targetPath == "" {
		return false
	}
	for _, approved := range plan.TargetPaths {
		if projectAssistantPathWithinApprovedTarget(targetPath, approved) {
			return true
		}
	}
	return false
}

func projectAssistantApprovedPlanAllowsOperation(plan *projectAssistantApprovedPlan, toolName string) bool {
	if plan == nil {
		return false
	}
	if len(plan.Operations) == 0 {
		return false
	}
	for _, op := range plan.Operations {
		if projectToolBaseName(op) == toolName {
			return true
		}
	}
	return false
}

func projectAssistantWriteTargetPath(toolName string, args map[string]any) string {
	switch projectToolBaseName(toolName) {
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
		return normalizeProjectAssistantRelativePath(projectToolString(args["path"]))
	default:
		return ""
	}
}

func projectAssistantPathWithinApprovedTarget(candidate, approved string) bool {
	candidate = normalizeProjectAssistantRelativePath(candidate)
	approved = strings.TrimSpace(approved)
	if approved == "" || candidate == "" {
		return false
	}
	directory := strings.HasSuffix(approved, "/")
	approved = normalizeProjectAssistantRelativePath(approved)
	if approved == "" {
		return false
	}
	if directory {
		return candidate == approved || strings.HasPrefix(candidate, approved+"/")
	}
	return candidate == approved
}

func normalizeProjectAssistantRelativePath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimPrefix(value, "/")
	value = pathpkg.Clean(value)
	if value == "." || strings.HasPrefix(value, "../") || value == ".." {
		return ""
	}
	return value
}

func parseProjectAssistantPermissionDecision(value string) (projectAssistantPermissionDecision, error) {
	decision := projectAssistantPermissionDecision(strings.ToLower(strings.TrimSpace(value)))
	switch decision {
	case projectAssistantPermissionAllow, projectAssistantPermissionDeny:
		return decision, nil
	default:
		return "", newValidationError("decision must be allow or deny")
	}
}

type projectAssistantPermissionRequiredError struct {
	RunID     string
	RequestID string
	ToolName  string
}

func (e *projectAssistantPermissionRequiredError) Error() string {
	if e == nil {
		return "assistant tool permission required"
	}
	if e.ToolName != "" {
		return fmt.Sprintf("assistant tool %q requires permission", e.ToolName)
	}
	return "assistant tool permission required"
}

type projectAssistantInputRequiredError struct {
	RunID     string
	RequestID string
}

func (e *projectAssistantInputRequiredError) Error() string {
	return "assistant needs follow-up input"
}

func projectAssistantPermissionDeniedToolMessage(tc chatToolCall, reason string) chatMessage {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "permission denied"
	}
	return chatMessage{
		Role:       "tool",
		Name:       tc.Function.Name,
		ToolCallID: tc.ID,
		Content:    "Tool call failed: permission denied: " + reason,
	}
}
