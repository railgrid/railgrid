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
	"strings"

	"github.com/faroshq/provider-app-studio/workspace"
)

type projectAssistantPermissionDecision string

const (
	projectAssistantPermissionAllow projectAssistantPermissionDecision = "allow"
	projectAssistantPermissionAsk   projectAssistantPermissionDecision = "ask"
	projectAssistantPermissionDeny  projectAssistantPermissionDecision = "deny"
	// Replan is an internal headless-mode outcome. It rejects the attempted
	// write, retires the stale plan envelope, and lets the next model
	// iteration request a replacement plan. It is never a user decision.
	projectAssistantPermissionReplan projectAssistantPermissionDecision = "replan"
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
		var proposedPlan projectAssistantApprovedPlan
		if projectToolBaseName(spec.Name) == projectToolRequestProjectPlanApproval {
			var err error
			proposedPlan, err = projectAssistantApprovedPlanFromArguments(args)
			if err != nil {
				return projectAssistantPermissionDeny
			}
		}
		if autoApprove {
			return projectAssistantPermissionAllow
		}
		if runState != nil && projectToolBaseName(spec.Name) == projectToolRequestProjectPlanApproval {
			activePlan := runState.ApprovedPlan()
			if projectAssistantApprovedPlanActive(activePlan) {
				if projectAssistantApprovedPlanCoversPlan(activePlan, &proposedPlan) {
					return projectAssistantPermissionAllow
				}
			}
		}
		return projectAssistantPermissionAsk
	case projectAssistantToolRiskWrite:
		if projectAssistantApprovedPlanAllowsWrite(runState.ApprovedPlan(), spec.Name, args) {
			return projectAssistantPermissionAllow
		}
		if autoApprove {
			if strings.TrimSpace(spec.Name) == projectToolSelectTemplate {
				return projectAssistantPermissionAllow
			}
			if projectAssistantApprovedPlanActive(runState.ApprovedPlan()) &&
				projectAssistantPlanCanAuthorizeWriteTool(spec.Name) {
				return projectAssistantPermissionReplan
			}
			return projectAssistantPermissionDeny
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
	return plan != nil &&
		plan.Version == projectAssistantApprovedPlanVersionWorkspaceMutation &&
		projectAssistantApprovedPlanHasCapability(plan, projectAssistantCapabilityWorkspaceMutate) &&
		(!plan.AllowAllWrites || plan.RunLocal) &&
		(plan.RunLocal || len(plan.TargetPaths) > 0)
}

func projectAssistantApprovedPlanAllowsWrite(plan *projectAssistantApprovedPlan, toolName string, args map[string]any) bool {
	if !projectAssistantApprovedPlanActive(plan) {
		return false
	}
	toolName = strings.TrimSpace(toolName)
	switch toolName {
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
	default:
		return false
	}
	// Initial project creation is the only unbounded grant. It is derived from
	// the user's create request and remains local to that Eino run.
	if plan.AllowAllWrites {
		_, err := projectAssistantWriteTargetPath(toolName, args)
		return plan.RunLocal && err == nil
	}
	if !projectAssistantApprovedPlanHasCapability(plan, projectAssistantCapabilityWorkspaceMutate) {
		return false
	}
	targetPath, err := projectAssistantWriteTargetPath(toolName, args)
	if err != nil {
		return false
	}
	for _, approved := range plan.TargetPaths {
		if projectAssistantPathWithinApprovedTarget(targetPath, approved) {
			return true
		}
	}
	return false
}

func projectAssistantApprovedPlanHasCapability(plan *projectAssistantApprovedPlan, capability string) bool {
	if plan == nil {
		return false
	}
	capability = strings.TrimSpace(capability)
	for _, candidate := range plan.Capabilities {
		if strings.TrimSpace(candidate) == capability {
			return true
		}
	}
	return false
}

func projectAssistantApprovedPlanCoversPlan(existing, proposed *projectAssistantApprovedPlan) bool {
	if !projectAssistantApprovedPlanActive(existing) || proposed == nil || len(proposed.TargetPaths) == 0 {
		return false
	}
	if projectAssistantApprovedPlanHasCapability(proposed, projectAssistantCapabilityWorkspaceMutate) &&
		!existing.AllowAllWrites &&
		!projectAssistantApprovedPlanHasCapability(existing, projectAssistantCapabilityWorkspaceMutate) {
		return false
	}
	if existing.AllowAllWrites {
		return true
	}
	for _, requested := range proposed.TargetPaths {
		covered := false
		for _, approved := range existing.TargetPaths {
			if projectAssistantApprovedTargetWithinApprovedTarget(requested, approved) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

func projectAssistantApprovedTargetWithinApprovedTarget(requested, approved string) bool {
	requestedDirectory := strings.HasSuffix(strings.TrimSpace(requested), "/")
	approvedDirectory := strings.HasSuffix(strings.TrimSpace(approved), "/")
	var err error
	requested, err = projectAssistantCanonicalGrantTarget(requested, requestedDirectory)
	if err != nil {
		return false
	}
	approved, err = projectAssistantCanonicalGrantTarget(approved, approvedDirectory)
	if err != nil {
		return false
	}
	if requestedDirectory && !approvedDirectory {
		return false
	}
	return requested == approved || (approvedDirectory && strings.HasPrefix(strings.TrimSuffix(requested, "/"), approved))
}

func projectAssistantPlanCanAuthorizeWriteTool(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
		return true
	default:
		return false
	}
}

func projectAssistantDirectApprovalGrantsWritePlan(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
		return true
	default:
		return false
	}
}

func projectAssistantWriteTargetPath(toolName string, args map[string]any) (string, error) {
	switch strings.TrimSpace(toolName) {
	case projectToolWriteFile, projectToolApplyPatch:
		return projectAssistantCanonicalGrantTarget(projectToolString(args["path"]), false)
	case projectToolMkdir:
		return projectAssistantCanonicalGrantTarget(projectToolString(args["path"]), true)
	default:
		return "", fmt.Errorf("tool %q cannot use workspace mutation grants", toolName)
	}
}

func projectAssistantPathWithinApprovedTarget(candidate, approved string) bool {
	approvedDirectory := strings.HasSuffix(strings.TrimSpace(approved), "/")
	var err error
	candidate, err = projectAssistantCanonicalGrantTarget(candidate, false)
	if err != nil {
		return false
	}
	approved, err = projectAssistantCanonicalGrantTarget(approved, approvedDirectory)
	if err != nil {
		return false
	}
	if approvedDirectory {
		return candidate == strings.TrimSuffix(approved, "/") || strings.HasPrefix(candidate, approved)
	}
	return candidate == approved
}

func projectAssistantCanonicalGrantTarget(value string, directory bool) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	directory = directory || strings.HasSuffix(value, "/")
	clean, err := workspace.CleanProjectPath(value)
	if err != nil {
		return "", err
	}
	if directory {
		return clean + "/", nil
	}
	return clean, nil
}

func projectAssistantCanonicalGrantTargets(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	for _, value := range values {
		clean, err := projectAssistantCanonicalGrantTarget(value, false)
		if err != nil {
			return nil, fmt.Errorf("invalid workspace grant target %q: %w", value, err)
		}
		out = append(out, clean)
	}
	return normalizeProjectAssistantStringList(out), nil
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
