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
	"sort"
	"strings"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

type projectAssistantPermissionDecision string

const (
	projectAssistantPermissionAllow projectAssistantPermissionDecision = "allow"
	projectAssistantPermissionAsk   projectAssistantPermissionDecision = "ask"
	projectAssistantPermissionDeny  projectAssistantPermissionDecision = "deny"
)

func projectAssistantToolHasEffect(spec projectAssistantToolSpec) bool {
	switch spec.Risk {
	case projectAssistantToolRiskPlan, projectAssistantToolRiskWrite, projectAssistantToolRiskRuntime, projectAssistantToolRiskCommit:
		return true
	default:
		return false
	}
}

// projectAssistantRequireMutationRead enforces read-before-mutation and
// returns the AUTHORITATIVE expectedVersion the caller must pass to the
// workspace mutation. For an existing file this is the version recorded by a
// complete same-turn read — NOT whatever token the model supplied. Models
// routinely fabricate a git-blob-style hash instead of echoing the opaque
// version from their read result, and correcting them in the error text does
// not reliably work. The safety the version exists for is preserved: a
// complete read must have happened this turn, and the workspace layer still
// rejects the returned version if the file changed on disk since that read.
func projectAssistantRequireMutationRead(ctx context.Context, req projectAssistantToolCallRequest, workspaces *workspace.FileStore, rawPath string, expectedVersions ...string) (string, error) {
	return projectAssistantResolveMutationVersion(ctx, req, workspaces, rawPath, true, expectedVersions...)
}

// projectAssistantResolveEditVersion preserves a recorded read version when
// one exists, but permits edit_file to operate against the current file when
// the model has not performed a separate read. The workspace edit path then
// reads and matches the file under its mutation lock, like Codex apply_patch
// and Pi's edit tool.
func projectAssistantResolveEditVersion(ctx context.Context, req projectAssistantToolCallRequest, workspaces *workspace.FileStore, rawPath string, expectedVersions ...string) (string, error) {
	return projectAssistantResolveMutationVersion(ctx, req, workspaces, rawPath, false, expectedVersions...)
}

func projectAssistantResolveMutationVersion(ctx context.Context, req projectAssistantToolCallRequest, workspaces *workspace.FileStore, rawPath string, requireRead bool, expectedVersions ...string) (string, error) {
	expectedVersion := ""
	if len(expectedVersions) > 0 {
		expectedVersion = strings.TrimSpace(expectedVersions[0])
	}
	path, err := workspace.CleanProjectPath(rawPath)
	if err != nil {
		return "", fmt.Errorf("mutation path is invalid: %w", err)
	}
	if workspaces == nil {
		return "", errors.New("project workspace store is not configured")
	}
	exists, err := workspaces.FileExists(ctx, req.WorkspaceScope, path)
	if err != nil {
		return "", err
	}
	if !exists {
		// Create-style mutation of a non-existent path — no prior version.
		return expectedVersion, nil
	}
	if req.RunState == nil {
		if !requireRead {
			return "", nil
		}
		return "", fmt.Errorf("mutation of existing file %q requires a same-turn read", path)
	}
	observed := req.RunState.ReadFileVersion(path)
	if observed == "" {
		if !requireRead {
			return "", nil
		}
		return "", fmt.Errorf("mutation of existing file %q requires a complete same-turn read first: call read_file at offset 1 covering the whole file (a partial or ranged read does not authorize an edit)", path)
	}
	// A complete read happened this turn — authorize the mutation using that
	// read's version, whatever the model passed.
	return observed, nil
}

// projectAssistantPermissionForV2 keeps collaboration mode, approval policy,
// and tool risk independent. Default mode supplies mutation intent; the
// approval preference decides whether an individual effect pauses. Workspace
// and lifecycle validation still run at invocation time.
func projectAssistantPermissionForV2(
	spec projectAssistantToolSpec,
	mode store.AssistantApprovalMode,
	runState *projectEinoAssistantRunState,
	args map[string]any,
	templateBootstrapAllowed bool,
) projectAssistantPermissionDecision {
	if strings.TrimSpace(string(mode)) == "" {
		mode = store.AssistantApprovalModeOnRequest
	}
	switch spec.Risk {
	case projectAssistantToolRiskRead, projectAssistantToolRiskInput:
		return projectAssistantPermissionAllow
	case projectAssistantToolRiskPlan:
		// Plans are presentation/progress state. They never confer authority.
		return projectAssistantPermissionAllow
	case projectAssistantToolRiskWrite:
		if mode == store.AssistantApprovalModeOnRequest || mode == store.AssistantApprovalModeAutoApprove {
			return projectAssistantPermissionAllow
		}
		if mode == store.AssistantApprovalModeNever {
			return projectAssistantPermissionDeny
		}
		return projectAssistantPermissionAsk
	case projectAssistantToolRiskRuntime:
		// Never is a fail-closed mode for every runtime effect, including
		// bounded compiler/test/lint execution. Keep this check before the
		// on-request exception list so a newly-automatic runtime action cannot
		// accidentally become executable under Never.
		if mode == store.AssistantApprovalModeNever {
			return projectAssistantPermissionDeny
		}
		if projectAssistantOnRequestRequiresApproval(spec.Name) {
			return projectAssistantPermissionAsk
		}
		if mode == store.AssistantApprovalModeOnRequest || mode == store.AssistantApprovalModeAutoApprove {
			return projectAssistantPermissionAllow
		}
		return projectAssistantPermissionAsk
	case projectAssistantToolRiskCommit:
		if mode == store.AssistantApprovalModeNever {
			return projectAssistantPermissionDeny
		}
		if mode == store.AssistantApprovalModeAutoApprove {
			return projectAssistantPermissionAllow
		}
		return projectAssistantPermissionAsk
	default:
		return projectAssistantPermissionDeny
	}
}

// projectAssistantRevalidatePermissionEdit treats edited approval arguments as
// new untrusted input. Same-scope workspace edits preserve the approval dialog's
// intended content-editing UX; any other effectful argument change can alter the
// operation's authority or risk and therefore requires a fresh approval.
func projectAssistantRevalidatePermissionEdit(
	spec projectAssistantToolSpec,
	original map[string]any,
	edited map[string]any,
) (map[string]any, bool, error) {
	effective := cloneProjectAssistantToolArguments(edited)
	if !projectAssistantToolHasEffect(spec) {
		return nil, false, fmt.Errorf("tool %q does not support permission-edited arguments", spec.Name)
	}
	if err := projectAssistantValidateGrantBearingToolArguments(spec, effective); err != nil {
		return nil, false, err
	}
	if projectToolBaseName(spec.Name) == projectToolExecCommand {
		normalized, err := normalizeProjectAssistantExecCommandArguments(effective)
		if err != nil {
			return nil, false, err
		}
		effective = normalized
	}

	if projectAssistantWorkspaceMutationTool(spec.Name) {
		before, err := projectAssistantWriteTargetPaths(spec.Name, original)
		if err != nil {
			return nil, false, fmt.Errorf("revalidate originally approved workspace scope: %w", err)
		}
		after, err := projectAssistantWriteTargetPaths(spec.Name, effective)
		if err != nil {
			return nil, false, err
		}
		return effective, strings.Join(before, "\x00") != strings.Join(after, "\x00"), nil
	}

	before, err := projectAssistantCanonicalJSON(original)
	if err != nil {
		return nil, false, fmt.Errorf("revalidate originally approved arguments: %w", err)
	}
	after, err := projectAssistantCanonicalJSON(effective)
	if err != nil {
		return nil, false, fmt.Errorf("revalidate edited arguments: %w", err)
	}
	return effective, string(before) != string(after), nil
}

func projectAssistantOnRequestRequiresApproval(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolInfrastructureProvision, projectToolPrepareProjectDeployment:
		return true
	default:
		return false
	}
}

func projectAssistantPermissionDenialReason(
	spec projectAssistantToolSpec,
	runState *projectEinoAssistantRunState,
	args map[string]any,
	templateBootstrapAllowed bool,
) string {
	toolName := projectToolBaseName(spec.Name)
	if projectAssistantWorkspaceMutationTool(toolName) {
		paths, err := projectAssistantWriteTargetPaths(toolName, args)
		if err != nil {
			return "permission denied: invalid mutation paths; recovery: provide bounded project-relative paths and retry"
		}
		if templateBootstrapAllowed {
			return fmt.Sprintf(
				"permission denied: template_not_bound; denied paths: %s; recovery: if the active per-run coding sandbox is ready, define_initial_project_plan and keep source under its server-owned project workspace root; otherwise select a hosted development/preview template first, then define_initial_project_plan from its component contract",
				strings.Join(paths, ", "),
			)
		}
		approved := []string{}
		if runState != nil {
			if plan := runState.ApprovedPlan(); plan != nil {
				if plan.AllowAllWrites {
					approved = []string{"<project-workspace>"}
				} else {
					approved = append(approved, plan.TargetPaths...)
				}
			}
		}
		denied := make([]string, 0, len(paths))
		for _, candidate := range paths {
			covered := false
			for _, allowed := range approved {
				if allowed == "<project-workspace>" || projectAssistantPathWithinApprovedTarget(candidate, allowed) {
					covered = true
					break
				}
			}
			if !covered {
				denied = append(denied, candidate)
			}
		}
		componentRoots := projectAssistantDevelopmentComponentRoots(runState)
		return fmt.Sprintf(
			"permission denied: path_outside_approved_scope; denied paths: %s; approved paths: %s; development component roots: %s; recovery: use the bound template component roots and request direct user approval if broader authority is required",
			projectAssistantPermissionPathList(denied),
			projectAssistantPermissionPathList(approved),
			projectAssistantPermissionPathList(componentRoots),
		)
	}
	return fmt.Sprintf("permission denied: unsupported_tool_risk; tool: %s; risk: %s", toolName, spec.Risk)
}

func projectAssistantDevelopmentComponentRoots(runState *projectEinoAssistantRunState) []string {
	if runState == nil {
		return nil
	}
	snapshot := runState.SessionSnapshot()
	if snapshot == nil {
		return nil
	}
	roots := make([]string, 0, len(snapshot.DevelopmentComponents))
	for _, component := range snapshot.DevelopmentComponents {
		root := strings.TrimSpace(component.WorkspacePath)
		if root != "" {
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	return normalizeProjectAssistantStringList(roots)
}

func projectAssistantPermissionPathList(paths []string) string {
	if len(paths) == 0 {
		return "<none>"
	}
	return strings.Join(paths, ", ")
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
	case projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile, projectToolMoveFile:
	default:
		return false
	}
	if err := projectAssistantValidateWorkspaceMutationArguments(toolName, args); err != nil {
		return false
	}
	// Initial project creation is the only unbounded grant. It is derived from
	// the user's create request and remains local to that Eino run.
	if plan.AllowAllWrites {
		_, err := projectAssistantWriteTargetPaths(toolName, args)
		return plan.RunLocal && err == nil
	}
	if !projectAssistantApprovedPlanHasCapability(plan, projectAssistantCapabilityWorkspaceMutate) {
		return false
	}
	targetPaths, err := projectAssistantWriteTargetPaths(toolName, args)
	if err != nil {
		return false
	}
	for _, targetPath := range targetPaths {
		covered := false
		for _, approved := range plan.TargetPaths {
			if projectAssistantPathWithinApprovedTarget(targetPath, approved) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return len(targetPaths) > 0
}

// projectAssistantValidateWorkspaceMutationArguments keeps the grant and
// execution boundaries fail-closed even when a caller bypasses model schema
// validation. It validates only bounded argument shape; the workspace store
// remains authoritative for file content and target state.
func projectAssistantValidateWorkspaceMutationArguments(toolName string, args map[string]any) error {
	allowed := map[string]struct{}{}
	switch strings.TrimSpace(toolName) {
	case projectToolCreateFile:
		allowed = map[string]struct{}{"path": {}, "content": {}, "recoveryOf": {}}
	case projectToolReplaceFile:
		allowed = map[string]struct{}{"path": {}, "content": {}, "expectedVersion": {}, "recoveryOf": {}}
	case projectToolEditFile:
		allowed = map[string]struct{}{"path": {}, "oldString": {}, "newString": {}, "replaceAll": {}, "expectedVersion": {}, "recoveryOf": {}}
	case projectToolDeleteFile:
		allowed = map[string]struct{}{"path": {}, "expectedVersion": {}, "recoveryOf": {}}
	case projectToolMoveFile:
		allowed = map[string]struct{}{"sourcePath": {}, "destinationPath": {}, "expectedVersion": {}, "recoveryOf": {}}
	default:
		return fmt.Errorf("tool %q cannot use workspace mutation arguments", toolName)
	}
	for key := range args {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unexpected mutation argument %q", key)
		}
	}
	if _, err := projectAssistantWriteTargetPaths(toolName, args); err != nil {
		return err
	}
	if toolName != projectToolCreateFile && toolName != projectToolEditFile {
		expectedVersion, ok := projectToolRawString(args["expectedVersion"])
		if !ok || strings.TrimSpace(expectedVersion) == "" {
			return fmt.Errorf("%s requires expectedVersion", toolName)
		}
		if len([]byte(expectedVersion)) > workspace.MaxFileVersionBytes {
			return fmt.Errorf("%s expectedVersion is too large", toolName)
		}
	} else if toolName == projectToolEditFile {
		if expectedVersion, ok := projectToolRawString(args["expectedVersion"]); ok && len([]byte(expectedVersion)) > workspace.MaxFileVersionBytes {
			return fmt.Errorf("%s expectedVersion is too large", toolName)
		}
	}
	switch strings.TrimSpace(toolName) {
	case projectToolCreateFile, projectToolReplaceFile:
		content, ok := projectToolRawString(args["content"])
		if !ok {
			return fmt.Errorf("%s requires content", toolName)
		}
		if len([]byte(content)) > workspace.MaxWriteBytes {
			return fmt.Errorf("%s content is too large", toolName)
		}
	case projectToolEditFile:
		oldString, oldOK := projectToolRawString(args["oldString"])
		newString, newOK := projectToolRawString(args["newString"])
		if !oldOK || oldString == "" {
			return fmt.Errorf("%s requires oldString", toolName)
		}
		if !newOK {
			return fmt.Errorf("%s requires newString", toolName)
		}
		if len([]byte(oldString)) > workspace.MaxWriteBytes || len([]byte(newString)) > workspace.MaxWriteBytes {
			return fmt.Errorf("%s replacement strings are too large", toolName)
		}
		if value, ok := args["replaceAll"]; ok {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s replaceAll must be boolean", toolName)
			}
		}
	}
	return nil
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

func projectAssistantWorkspaceMutationTool(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile, projectToolMoveFile:
		return true
	default:
		return false
	}
}

// projectAssistantWriteTargetPaths returns every server-normalized workspace
// path a narrow mutation can affect. Both endpoints of a move are authorized.
func projectAssistantWriteTargetPaths(toolName string, args map[string]any) ([]string, error) {
	switch strings.TrimSpace(toolName) {
	case projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile:
		path, ok := projectToolRawString(args["path"])
		if !ok || strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("%s requires path", toolName)
		}
		clean, err := workspace.CleanProjectPath(path)
		if err != nil {
			return nil, err
		}
		return []string{clean}, nil
	case projectToolMoveFile:
		source, sourceOK := projectToolRawString(args["sourcePath"])
		destination, destinationOK := projectToolRawString(args["destinationPath"])
		if !sourceOK || !destinationOK || strings.TrimSpace(source) == "" || strings.TrimSpace(destination) == "" {
			return nil, fmt.Errorf("move_file requires sourcePath and destinationPath")
		}
		paths := make([]string, 0, 2)
		for _, raw := range []string{source, destination} {
			clean, err := workspace.CleanProjectPath(raw)
			if err != nil {
				return nil, err
			}
			paths = append(paths, clean)
		}
		if paths[0] == paths[1] {
			return nil, fmt.Errorf("move_file sourcePath and destinationPath must differ")
		}
		return paths, nil
	default:
		return nil, fmt.Errorf("tool %q cannot use workspace mutation grants", toolName)
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
