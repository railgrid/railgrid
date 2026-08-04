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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/faroshq/provider-app-studio/workspace"
)

// projectAssistantToolDisclosureMinimal keeps the action feed opaque: product
// categories and lifecycle state remain visible, while targets and outcomes
// are withheld. The default emits only server-owned, product-facing fields.
var projectAssistantToolDisclosureMinimal = strings.EqualFold(
	strings.TrimSpace(os.Getenv("APP_STUDIO_TOOL_DISCLOSURE")), "minimal")

const (
	projectAssistantActionFeedItemInspect = "inspect"
	projectAssistantActionFeedItemClarify = "clarify"
	projectAssistantActionFeedItemEdit    = "edit"
	projectAssistantActionFeedItemRun     = "run"
	projectAssistantActionFeedItemCommit  = "commit"
	projectAssistantActionFeedItemPlan    = "plan"
	projectAssistantActionFeedItemOther   = "other"
)

const (
	projectAssistantActionFeedStatusRunning   = "running"
	projectAssistantActionFeedStatusWaiting   = "waiting"
	projectAssistantActionFeedStatusSucceeded = "succeeded"
	projectAssistantActionFeedStatusSkipped   = "skipped"
	projectAssistantActionFeedStatusFailed    = "failed"
	projectAssistantActionFeedStatusRejected  = "rejected"
	projectAssistantActionFeedStatusRetrying  = "retrying"
	projectAssistantActionFeedStatusRecovered = "recovered"
)

const (
	projectAssistantActionFeedSeverityNormal    = "normal"
	projectAssistantActionFeedSeverityAttention = "attention"
	projectAssistantActionFeedSeverityError     = "error"
)

type projectAssistantActionFeedItem struct {
	ID         string                            `json:"id"`
	Kind       string                            `json:"kind"`
	Status     string                            `json:"status"`
	Title      string                            `json:"title"`
	Target     string                            `json:"target,omitempty"`
	Outcome    string                            `json:"outcome,omitempty"`
	Count      int                               `json:"count,omitempty"`
	Severity   string                            `json:"severity"`
	GroupKey   string                            `json:"groupKey,omitempty"`
	GroupTitle string                            `json:"groupTitle,omitempty"`
	Sequence   int                               `json:"sequence,omitempty"`
	RecoveryOf string                            `json:"recoveryOf,omitempty"`
	Diagnostic *projectAssistantActionDiagnostic `json:"diagnostic,omitempty"`
	Exec       *projectAssistantExecMetadata     `json:"exec,omitempty"`
}

type projectAssistantActionDiagnostic struct {
	Category    string `json:"category"`
	Message     string `json:"message"`
	ReferenceID string `json:"referenceID"`
	Code        string `json:"code,omitempty"`
	Operation   string `json:"operation,omitempty"`
	Path        string `json:"path,omitempty"`
	Guidance    string `json:"guidance,omitempty"`
}

func projectAssistantActionFeedItemFromToolCall(toolCall projectToolCallStreamEvent) projectAssistantActionFeedItem {
	item := presentProjectAssistantAction(
		toolCall.ID,
		toolCall.Name,
		toolCall.Status,
		toolCall.Arguments,
		toolCall.Summary,
		toolCall.Error,
	)
	var permissionExec *projectAssistantExecMetadata
	if toolCall.Permission != nil {
		permissionExec = toolCall.Permission.Exec
	}
	item.Exec = mergeProjectAssistantExecMetadata(permissionExec, toolCall.Exec)
	if toolCall.Mutation != nil {
		item.RecoveryOf = projectAssistantBoundedMutationField(toolCall.Mutation.RecoveryOf, 120)
	}
	if item.RecoveryOf == "" {
		item.RecoveryOf = projectAssistantBoundedMutationField(toolCall.RecoveryOf, 120)
	}
	if (item.Status == projectAssistantActionFeedStatusFailed || item.Status == projectAssistantActionFeedStatusRejected) &&
		projectAssistantWorkspaceMutationTool(toolCall.Name) {
		item.Diagnostic = projectAssistantActionFeedMutationDiagnostic(toolCall.ID, toolCall.Name, toolCall.Mutation, toolCall.MutationError, toolCall.Error)
	}
	if projectToolBaseName(toolCall.Name) == projectToolInspectDevelopmentPreview {
		projectAssistantApplyPreviewInspectionPresentation(&item, toolCall.ID, toolCall.PreviewInspection)
	}
	if toolCall.Mutation != nil && projectAssistantWorkspaceMutationTool(toolCall.Name) && !projectAssistantToolDisclosureMinimal {
		item.Target = projectAssistantActionSafeTarget(projectAssistantMutationTargetFromResult(toolCall.Mutation, projectToolBaseName(toolCall.Name)))
	}
	item.Sequence = toolCall.Sequence
	return item
}

func projectAssistantActionFeedItemFromAssistantToolCall(toolCall projectAssistantToolCall) projectAssistantActionFeedItem {
	item := presentProjectAssistantAction(
		toolCall.ID,
		toolCall.Name,
		toolCall.Status,
		toolCall.Arguments,
		toolCall.Summary,
		toolCall.Error,
	)
	item.Exec = cloneProjectAssistantExecMetadata(toolCall.Exec)
	item.RecoveryOf = projectAssistantBoundedMutationField(toolCall.RecoveryOf, 120)
	if (item.Status == projectAssistantActionFeedStatusFailed || item.Status == projectAssistantActionFeedStatusRejected) &&
		projectAssistantWorkspaceMutationTool(toolCall.Name) {
		item.Diagnostic = projectAssistantActionFeedMutationDiagnostic(toolCall.ID, toolCall.Name, nil, nil, toolCall.Error)
	}
	if projectToolBaseName(toolCall.Name) == projectToolInspectDevelopmentPreview {
		projectAssistantApplyPreviewInspectionPresentation(&item, toolCall.ID, projectAssistantPreviewInspectionActionFromText(string(toolCall.Result)))
	}
	return item
}

func projectAssistantActionFeedItemFromPermission(permission projectAssistantPermission) projectAssistantActionFeedItem {
	item := presentProjectAssistantAction(
		permission.ToolCallID,
		permission.ToolName,
		"permission_required",
		"",
		permission.Reason,
		"",
	)
	item.Exec = cloneProjectAssistantExecMetadata(permission.Exec)
	return item
}

func projectAssistantActionFeedItemFromFollowUp(followUp projectAssistantFollowUp) projectAssistantActionFeedItem {
	return presentProjectAssistantAction(
		followUp.ToolCallID,
		projectToolAskFollowUp,
		"input_required",
		"",
		followUp.Prompt,
		"",
	)
}

func presentProjectAssistantAction(id, name, rawStatus, arguments, summary, errText string) projectAssistantActionFeedItem {
	kind := projectAssistantActionFeedItemKind(name)
	status := projectAssistantActionFeedItemStatus(rawStatus)
	item := projectAssistantActionFeedItem{
		ID:       projectAssistantActionPublicID(id),
		Kind:     kind,
		Status:   status,
		Title:    projectAssistantActionFeedItemTitle(kind, status),
		Severity: projectAssistantActionFeedItemSeverity(status),
	}
	if status == projectAssistantActionFeedStatusFailed || status == projectAssistantActionFeedStatusRejected {
		item.Diagnostic = projectAssistantActionFeedDiagnostic(id, errText)
		if projectToolBaseName(name) == projectToolInspectDevelopmentPreview && strings.TrimSpace(errText) == "" {
			item.Diagnostic.Category = "runtime"
			item.Diagnostic.Message = "Browser inspection did not verify the preview. Review the rendered, console, and assertion evidence."
		}
	}
	if projectAssistantToolDisclosureMinimal {
		return item
	}

	args := projectAssistantActionArguments(arguments)
	base := projectToolBaseName(name)
	switch base {
	case projectToolReadFile:
		path := projectAssistantActionArgumentField(args, arguments, "file_path", "path")
		if projectAssistantCanonicalFilesystemReadTool(name) {
			if decoded, ok := unescapeProjectCanonicalToolSummaryValue(path); ok {
				path = decoded
			} else {
				path = ""
			}
		}
		item.Target = projectAssistantActionSafeTarget(path)
		item.Title = projectAssistantActionLifecycleTitle(status, "Reading file", "Read file", "File read failed")
	case projectToolLS, projectToolGlob:
		item.Title = projectAssistantActionLifecycleTitle(status, "Reading project files", "Read project files", "Project inspection failed")
		if count, ok := projectAssistantSummaryCount(summary, "path(s)"); ok {
			item.Count = count
			item.Outcome = projectAssistantCountOutcome(count, "project file", "project files")
		}
	case projectToolGrep:
		item.Title = projectAssistantActionLifecycleTitle(status, "Searching project", "Searched project", "Project search failed")
		if count, ok := projectAssistantSummaryCount(summary, "result line(s)"); ok {
			item.Count = count
			item.Outcome = projectAssistantCountOutcome(count, "reference", "references")
		}
	case projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile, projectToolMoveFile:
		item.Target = projectAssistantActionSafeTarget(projectAssistantMutationTarget(args, arguments, base))
		item.Title = projectAssistantActionLifecycleTitle(status, "Updating files", "Updated files", "File update failed")
		if status == projectAssistantActionFeedStatusSucceeded {
			item.Outcome = projectAssistantMutationOutcome(summary)
		}
	case projectToolVerifyDevelopmentRuntime:
		item.Title = projectAssistantActionLifecycleTitle(status, "Checking development preview", "Checked development preview", "Preview check failed")
	case projectToolGetPreviewURL:
		item.Title = projectAssistantActionLifecycleTitle(status, "Checking preview", "Checked preview", "Preview check failed")
	case projectToolInspectDevelopmentPreview:
		item.Target = projectAssistantActionSafeTarget(projectAssistantActionArgumentField(args, arguments, "path", "path"))
		item.Title = projectAssistantActionLifecycleTitle(status, "Inspecting development preview", "Inspected development preview", "Preview inspection failed")
	case projectToolCheckProjectReadiness:
		item.Title = projectAssistantActionLifecycleTitle(status, "Checking project readiness", "Checked project readiness", "Readiness check failed")
	case projectToolPrepareProjectDeployment:
		item.Title = projectAssistantActionLifecycleTitle(status, "Preparing development preview", "Prepared development preview", "Preview preparation failed")
	case projectToolGetRuntimeStatus:
		item.Title = projectAssistantActionLifecycleTitle(status, "Checking development runtime", "Checked development runtime", "Runtime check failed")
	case projectToolGetRuntimeLogs:
		item.Title = projectAssistantActionLifecycleTitle(status, "Reviewing runtime logs", "Reviewed runtime logs", "Runtime log review failed")
		if count, ok := projectAssistantSummaryCount(summary, "log line(s)"); ok {
			item.Count = count
			item.Outcome = projectAssistantCountOutcome(count, "log line", "log lines")
		}
	case projectToolGetPreviewConsoleLogs:
		item.Title = projectAssistantActionLifecycleTitle(status, "Reviewing preview console", "Reviewed preview console", "Preview console review failed")
		if count, ok := projectAssistantSummaryCount(summary, "browser console event(s)"); ok {
			item.Count = count
			item.Outcome = projectAssistantCountOutcome(count, "console event", "console events")
		}
	case projectToolRestartRuntime:
		item.Title = projectAssistantActionLifecycleTitle(status, "Restarting development runtime", "Restarted development runtime", "Runtime restart failed")
	case projectToolSetRuntimeEnv:
		item.Title = projectAssistantActionLifecycleTitle(status, "Updating environment", "Updated environment", "Environment update failed")
		if env, ok := args["env"].(map[string]any); ok {
			item.Count = len(env)
			item.Outcome = projectAssistantCountOutcome(len(env), "environment variable", "environment variables")
		} else if count, ok := projectAssistantSummaryCount(arguments, "variable(s):"); ok {
			item.Count = count
			item.Outcome = projectAssistantCountOutcome(count, "environment variable", "environment variables")
		}
	case projectToolExecCommand:
		item.Title = projectAssistantActionLifecycleTitle(status, "Running command", "Ran command", "Command failed")
		if component := projectToolString(args["component"]); component != "" {
			item.Target = projectAssistantActionSafeTarget(component)
		}
	case projectToolCommitFiles, projectToolCommitProjectFiles:
		item.Title = projectAssistantActionLifecycleTitle(status, "Committing changes", "Committed changes", "Commit failed")
		paths := projectToolFilePaths(args["files"])
		if len(paths) == 0 {
			paths = projectToolStringList(args["paths"])
		}
		if len(paths) > 0 {
			item.Count = len(paths)
			item.Outcome = projectAssistantCountOutcome(len(paths), "file", "files")
		} else if count, ok := projectAssistantSummaryCount(arguments, "file(s):"); ok {
			item.Count = count
			item.Outcome = projectAssistantCountOutcome(count, "file", "files")
		}
		if ref := projectAssistantSummaryField(summary, "commit"); ref != "" {
			item.Target = projectAssistantActionSafeTarget(ref)
		}
	}
	projectAssistantActionFeedGrouping(&item, base)
	return item
}

func projectAssistantMutationOutcome(summary string) string {
	parts := strings.Split(summary, ";")
	counts := make([]string, 0, 2)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "+") || strings.HasPrefix(part, "-") {
			counts = append(counts, part)
		}
	}
	return strings.Join(counts, " ")
}

func projectAssistantMutationTarget(args map[string]any, summary, toolName string) string {
	if toolName == projectToolMoveFile {
		source := projectAssistantActionArgumentField(args, summary, "sourcePath", "source")
		destination := projectAssistantActionArgumentField(args, summary, "destinationPath", "destination")
		if source == "" {
			return destination
		}
		if destination == "" {
			return source
		}
		return source + " -> " + destination
	}
	return projectAssistantActionArgumentField(args, summary, "path", "path")
}

func projectAssistantValidatedMutationRecoveryOf(runState *projectEinoAssistantRunState, args map[string]any, toolName ...string) string {
	if runState == nil || args == nil {
		return ""
	}
	value := strings.TrimSpace(projectToolString(args["recoveryOf"]))
	name := ""
	if len(toolName) > 0 {
		name = toolName[0]
	}
	if value == "" || len([]byte(value)) > 120 || name == "" ||
		!runState.IsMutationRecoveryReferenceCompatible(value, name, args) {
		return ""
	}
	return value
}

func projectAssistantMutationRecoveryIdentityFromTool(name string, args map[string]any) (projectAssistantMutationRecoveryIdentity, bool) {
	operation := projectToolBaseName(name)
	family := projectAssistantMutationRecoveryOperationFamily(operation)
	if family == "" || args == nil {
		return projectAssistantMutationRecoveryIdentity{}, false
	}
	key := "path"
	if operation == projectToolMoveFile {
		key = "sourcePath"
	}
	rawTarget := projectToolString(args[key])
	if rawTarget == "" {
		return projectAssistantMutationRecoveryIdentity{}, false
	}
	target, err := workspace.CleanProjectPath(rawTarget)
	if err != nil || len([]byte(target)) > 240 {
		return projectAssistantMutationRecoveryIdentity{}, false
	}
	return projectAssistantMutationRecoveryIdentity{Operation: family, Target: target}, true
}

func projectAssistantMutationRecoveryOperationFamily(operation string) string {
	switch projectToolBaseName(operation) {
	case "create", projectToolCreateFile:
		return "create"
	case "edit", projectToolReplaceFile, projectToolEditFile:
		return "edit"
	case "delete", projectToolDeleteFile:
		return "delete"
	case "move", projectToolMoveFile:
		return "move"
	default:
		return ""
	}
}

func projectAssistantMutationRecoveryIdentityCompatible(prior, current projectAssistantMutationRecoveryIdentity) bool {
	if prior.Target == "" || current.Target == "" || prior.Target != current.Target {
		return false
	}
	if prior.Operation == current.Operation {
		return true
	}
	// A create that reports target_exists may be repaired by replacing or
	// editing the existing file. The reverse direction is not accepted.
	return prior.Operation == "create" && current.Operation == "edit"
}

func projectAssistantMutationFailureFromError(name string, args map[string]any, invokeErr error, recoveryOf string) projectAssistantMutationFailure {
	operation := projectToolBaseName(name)
	if !projectAssistantWorkspaceMutationTool(operation) {
		operation = "mutation"
	}
	code := "mutation_failed"
	path := projectAssistantMutationFailurePath(operation, args)
	var mutationErr *workspace.MutationError
	if errors.As(invokeErr, &mutationErr) && mutationErr != nil {
		code = string(mutationErr.Code)
		if clean, err := workspace.CleanProjectPath(mutationErr.Path); err == nil {
			path = projectAssistantActionSafeTarget(clean)
		}
	}
	return projectAssistantMutationFailure{
		Code:       projectAssistantBoundedMutationField(code, 64),
		Operation:  projectAssistantBoundedMutationField(operation, 64),
		Path:       projectAssistantBoundedMutationField(path, 240),
		Guidance:   projectAssistantBoundedMutationField(projectAssistantMutationRecoveryGuidance(operation, code), 320),
		RecoveryOf: projectAssistantBoundedMutationField(recoveryOf, 120),
	}
}

func projectAssistantMutationFailurePath(operation string, args map[string]any) string {
	if args == nil {
		return ""
	}
	if operation == projectToolMoveFile {
		if source := projectToolString(args["sourcePath"]); source != "" {
			if clean, err := workspace.CleanProjectPath(source); err == nil {
				return clean
			}
		}
		return ""
	}
	path := projectToolString(args["path"])
	if clean, err := workspace.CleanProjectPath(path); err == nil {
		return clean
	}
	return ""
}

func projectAssistantBoundedMutationField(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if value == "" || len([]byte(value)) <= maxBytes {
		return value
	}
	raw := []byte(value)[:maxBytes]
	for len(raw) > 0 && !utf8.Valid(raw) {
		raw = raw[:len(raw)-1]
	}
	return strings.TrimSpace(string(raw))
}

func projectAssistantMutationRecoveryGuidance(operation, code string) string {
	switch {
	case operation == projectToolCreateFile && code == string(workspace.MutationErrorTargetExists):
		return "The create target already exists. Read the complete file, then retry with replace_file and its current expectedVersion, or choose a different path."
	case operation == projectToolMoveFile && code == string(workspace.MutationErrorTargetExists):
		return "The move destination already exists. Read the current workspace and choose a different destination; do not overwrite it implicitly."
	case (operation == projectToolReplaceFile || operation == projectToolEditFile) && code == string(workspace.MutationErrorStale):
		return "The source is stale. Read the complete current file, then retry with its current expectedVersion; edit_file also needs an exact current oldString."
	case (operation == projectToolDeleteFile || operation == projectToolMoveFile) && code == string(workspace.MutationErrorTargetNotFound):
		return "The source file is missing. Re-read the workspace and retry only with an existing source path."
	case operation == projectToolEditFile && code == string(workspace.MutationErrorAmbiguous):
		return "The edit matched multiple locations. Re-read the file and provide a narrower exact oldString, or set replaceAll when every match should change."
	case code == string(workspace.MutationErrorConflict):
		return "The workspace changed during the mutation. Re-read the affected file and retry against the current version."
	case code == string(workspace.MutationErrorVersionRequired):
		return "Read the complete current file and pass its opaque version as expectedVersion before retrying."
	case code == string(workspace.MutationErrorNoChanges):
		return "The requested mutation would not change the file; reread the current content and choose a meaningful update."
	default:
		return "Reread the current workspace state and retry the bounded mutation only after confirming the target and expectedVersion."
	}
}

// projectAssistantAttachMutationRecoveryOf adds the validated presentation
// correlation to a server-generated mutation result. It is intentionally a
// no-op for non-mutation or malformed results, and never changes the mutation
// operation itself.
func projectAssistantAttachMutationRecoveryOf(name, result, recoveryOf string) string {
	if !projectAssistantWorkspaceMutationTool(name) {
		return result
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result)), &decoded); err != nil {
		return result
	}
	if operation := projectToolString(decoded["operation"]); operation != "" && operation != projectToolBaseName(name) {
		return result
	}
	if _, ok := decoded["status"]; ok && strings.EqualFold(projectToolString(decoded["status"]), "failed") {
		return result
	}
	if recoveryOf = projectAssistantBoundedMutationField(recoveryOf, 120); recoveryOf != "" {
		decoded["recoveryOf"] = recoveryOf
	} else {
		// Tool output is not allowed to mint or smuggle a correlation ref. Only
		// the run-state validation above may reattach one to a result.
		delete(decoded, "recoveryOf")
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return result
	}
	return string(encoded)
}

// projectAssistantMutationTargetFromResult prefers the normalized path(s)
// returned by the workspace store over model-supplied argument summaries.
// This keeps action projections server-derived even when the original input
// used a path alias such as "./src/app.tsx".
func projectAssistantMutationTargetFromResult(mutation *projectAssistantMutation, toolName string) string {
	if mutation == nil {
		return ""
	}
	if toolName == projectToolMoveFile {
		source := projectAssistantCanonicalMutationResultPath(mutation.PreviousPath)
		destination := projectAssistantCanonicalMutationResultPath(mutation.Path)
		if source == "" {
			return destination
		}
		if destination == "" {
			return source
		}
		return source + " -> " + destination
	}
	return projectAssistantCanonicalMutationResultPath(mutation.Path)
}

func projectAssistantCanonicalMutationResultPath(raw string) string {
	clean, err := workspace.CleanProjectPath(raw)
	if err != nil {
		return ""
	}
	return projectAssistantActionSafeTarget(clean)
}

func projectAssistantActionPublicID(id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return "feed-" + hex.EncodeToString(sum[:12])
}

func projectAssistantActionFeedItemKind(name string) string {
	switch base := projectToolBaseName(name); {
	case base == projectToolAskFollowUp:
		return projectAssistantActionFeedItemClarify
	case base == projectToolPlanProjectChanges:
		return projectAssistantActionFeedItemPlan
	case base == projectToolCheckProjectReadiness || base == projectToolPrepareProjectDeployment ||
		base == projectToolVerifyDevelopmentRuntime || base == projectToolGetRuntimeStatus ||
		base == projectToolGetPreviewURL || base == projectToolInspectDevelopmentPreview || base == projectToolGetRuntimeLogs ||
		base == projectToolRestartRuntime || base == projectToolSetRuntimeEnv || base == projectToolExecCommand:
		return projectAssistantActionFeedItemRun
	case base == projectToolCommitProjectFiles || base == projectToolCommitFiles:
		return projectAssistantActionFeedItemCommit
	case base == projectToolCreateFile || base == projectToolReplaceFile || base == projectToolEditFile || base == projectToolDeleteFile || base == projectToolMoveFile:
		return projectAssistantActionFeedItemEdit
	case base == projectToolLS || base == projectToolReadFile || base == projectToolGlob || base == projectToolGrep:
		return projectAssistantActionFeedItemInspect
	default:
		return projectAssistantActionFeedItemOther
	}
}

func projectAssistantActionFeedItemStatus(status string) string {
	switch status {
	case "", "requested", "running":
		return projectAssistantActionFeedStatusRunning
	case "permission_required", "input_required":
		return projectAssistantActionFeedStatusWaiting
	case "failed":
		return projectAssistantActionFeedStatusFailed
	case "rejected":
		return projectAssistantActionFeedStatusRejected
	case "skipped":
		return projectAssistantActionFeedStatusSkipped
	case projectAssistantActionFeedStatusRetrying:
		return projectAssistantActionFeedStatusRetrying
	case projectAssistantActionFeedStatusRecovered:
		return projectAssistantActionFeedStatusRecovered
	default:
		return projectAssistantActionFeedStatusSucceeded
	}
}

func projectAssistantActionFeedItemSeverity(status string) string {
	switch status {
	case projectAssistantActionFeedStatusWaiting, projectAssistantActionFeedStatusRetrying:
		return projectAssistantActionFeedSeverityAttention
	case projectAssistantActionFeedStatusFailed, projectAssistantActionFeedStatusRejected:
		return projectAssistantActionFeedSeverityError
	default:
		return projectAssistantActionFeedSeverityNormal
	}
}

func projectAssistantActionFeedItemTitle(kind, status string) string {
	switch kind {
	case projectAssistantActionFeedItemInspect:
		return projectAssistantActionLifecycleTitle(status, "Inspecting project", "Inspected project", "Inspection failed")
	case projectAssistantActionFeedItemEdit:
		return projectAssistantActionLifecycleTitle(status, "Editing files", "Edited files", "Edit failed")
	case projectAssistantActionFeedItemRun:
		return projectAssistantActionLifecycleTitle(status, "Running checks", "Ran checks", "Run failed")
	case projectAssistantActionFeedItemCommit:
		return projectAssistantActionLifecycleTitle(status, "Preparing commit", "Committed changes", "Commit failed")
	case projectAssistantActionFeedItemPlan:
		return projectAssistantActionLifecycleTitle(status, "Reviewing plan", "Reviewed plan", "Plan rejected")
	case projectAssistantActionFeedItemClarify:
		return projectAssistantActionLifecycleTitle(status, "Clarifying requirements", "Clarified requirements", "Clarification failed")
	default:
		return projectAssistantActionLifecycleTitle(status, "Working", "Completed action", "Action failed")
	}
}

func projectAssistantActionLifecycleTitle(status, active, succeeded, failed string) string {
	switch status {
	case projectAssistantActionFeedStatusRetrying:
		return "Retrying file update"
	case projectAssistantActionFeedStatusRecovered:
		return "Recovered file update"
	case projectAssistantActionFeedStatusRunning, projectAssistantActionFeedStatusWaiting:
		return active
	case projectAssistantActionFeedStatusFailed, projectAssistantActionFeedStatusRejected:
		return failed
	case projectAssistantActionFeedStatusSkipped:
		return "Skipped duplicate read"
	default:
		return succeeded
	}
}

func projectAssistantActionFeedGrouping(item *projectAssistantActionFeedItem, base string) {
	if item == nil || item.Status != projectAssistantActionFeedStatusSucceeded {
		return
	}
	switch base {
	case projectToolReadFile, projectToolLS, projectToolGlob:
		item.GroupKey = "inspect:files"
		item.GroupTitle = "Read project files"
	case projectToolGrep:
		item.GroupKey = "inspect:search"
		item.GroupTitle = "Searched project"
	case projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile, projectToolMoveFile:
		item.GroupKey = "edit:files"
		item.GroupTitle = "Updated files"
	case projectToolCheckProjectReadiness, projectToolPrepareProjectDeployment,
		projectToolVerifyDevelopmentRuntime, projectToolGetRuntimeStatus,
		projectToolGetPreviewURL, projectToolInspectDevelopmentPreview, projectToolGetRuntimeLogs, projectToolRestartRuntime, projectToolExecCommand:
		item.GroupKey = "run:checks"
		item.GroupTitle = "Ran checks"
	}
}

func projectAssistantActionArguments(raw string) map[string]any {
	var args map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &args) != nil {
		return nil
	}
	return args
}

func projectAssistantActionArgumentField(args map[string]any, summary, rawKey, summaryKey string) string {
	if value := projectToolString(args[rawKey]); value != "" {
		return value
	}
	return projectAssistantSummaryField(summary, summaryKey)
}

func projectAssistantSummaryField(summary, field string) string {
	prefix := field + " "
	for _, part := range strings.Split(summary, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(part, prefix))
		}
	}
	return ""
}

func projectAssistantSummaryCount(summary, marker string) (int, bool) {
	index := strings.Index(summary, marker)
	if index < 0 {
		return 0, false
	}
	fields := strings.Fields(strings.TrimSpace(summary[:index]))
	if len(fields) == 0 {
		return 0, false
	}
	count, err := strconv.Atoi(strings.Trim(fields[len(fields)-1], ";"))
	return count, err == nil && count >= 0
}

func projectAssistantCountOutcome(count int, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func projectAssistantActionSafeTarget(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) {
		return ""
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return ""
		}
	}
	const maxRunes = 240
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes-3]) + "..."
	}
	return value
}

func projectAssistantActionFeedDiagnostic(id, rawError string) *projectAssistantActionDiagnostic {
	category := projectAssistantActionDiagnosticCategory(rawError)
	message := projectAssistantActionDiagnosticMessage(category, rawError)
	sum := sha256.Sum256([]byte(id))
	return &projectAssistantActionDiagnostic{
		Category:    category,
		Message:     message,
		ReferenceID: "action-" + hex.EncodeToString(sum[:6]),
	}
}

func projectAssistantApplyPreviewInspectionPresentation(item *projectAssistantActionFeedItem, id string, preview *projectAssistantPreviewInspectionAction) {
	if item == nil || preview == nil {
		return
	}
	diagnostic := projectAssistantActionFeedDiagnostic(id, "")
	diagnostic.Operation = projectToolInspectDevelopmentPreview
	switch preview.FailureKind {
	case "assertion":
		item.Title = "Preview assertions did not match"
		item.Severity = projectAssistantActionFeedSeverityAttention
		diagnostic.Category = "validation"
		diagnostic.Code = "preview_assertion_mismatch"
		if preview.AssertionCount > 0 && preview.FailedAssertionCount > 0 && preview.FailedAssertionCount <= preview.AssertionCount {
			diagnostic.Message = fmt.Sprintf("%d of %d preview assertions did not match.", preview.FailedAssertionCount, preview.AssertionCount)
		} else {
			diagnostic.Message = "One or more preview assertions did not match."
		}
		diagnostic.Guidance = "Review the rendered accessibility evidence, correct the preview or assertion, and inspect again."
	case "application":
		item.Title = "Preview rendered with application errors"
		item.Severity = projectAssistantActionFeedSeverityError
		diagnostic.Category = "runtime"
		diagnostic.Code = "preview_application_error"
		diagnostic.Message = "The preview rendered, but the browser detected application errors."
		diagnostic.Guidance = "Review the browser console and failed document or script requests."
	case "navigation":
		item.Title = "Preview could not be opened"
		item.Severity = projectAssistantActionFeedSeverityError
		diagnostic.Category = "runtime"
		diagnostic.Code = "preview_navigation_failed"
		diagnostic.Message = "The browser could not open the development preview."
		diagnostic.Guidance = "Confirm the preview is ready and reachable, then inspect again."
	case "worker_unavailable":
		item.Title = "Preview inspection unavailable"
		item.Status = projectAssistantActionFeedStatusFailed
		item.Severity = projectAssistantActionFeedSeverityError
		diagnostic.Category = "runtime"
		diagnostic.Code = "preview_worker_unavailable"
		diagnostic.Message = "The browser inspection service was unavailable."
		diagnostic.Guidance = "Restore the browser inspection service, then inspect again."
	case "not_current":
		item.Title = "Waiting for the latest preview"
		item.Status = projectAssistantActionFeedStatusWaiting
		item.Severity = projectAssistantActionFeedSeverityAttention
		diagnostic.Category = "runtime"
		diagnostic.Code = "preview_not_current"
		diagnostic.Message = "The latest workspace changes had not reached the development preview yet."
		diagnostic.Guidance = "Wait for synchronization to finish, then inspect again."
	default:
		return
	}
	item.Diagnostic = diagnostic
}

func projectAssistantActionFeedMutationDiagnostic(id, name string, mutation *projectAssistantMutation, failure *projectAssistantMutationFailure, rawError string) *projectAssistantActionDiagnostic {
	operation := projectToolBaseName(name)
	code := ""
	path := ""
	guidance := ""
	if mutation != nil {
		operation = projectAssistantBoundedMutationField(mutation.Operation, 64)
		path = projectAssistantBoundedMutationField(mutation.Path, 240)
	}
	if failure != nil {
		if failure.Code != "" {
			code = projectAssistantBoundedMutationField(failure.Code, 64)
		}
		if failure.Operation != "" {
			operation = projectAssistantBoundedMutationField(failure.Operation, 64)
		}
		if failure.Path != "" {
			path = projectAssistantBoundedMutationField(failure.Path, 240)
		}
		guidance = projectAssistantBoundedMutationField(failure.Guidance, 320)
	}
	if code == "" {
		code = projectAssistantMutationErrorCode(rawError)
	}
	if guidance == "" {
		guidance = projectAssistantMutationRecoveryGuidance(operation, code)
	}
	if path == "" {
		path = projectAssistantMutationErrorPath(rawError)
	}
	base := projectAssistantActionFeedDiagnostic(id, rawError)
	if base == nil {
		return nil
	}
	base.Code = projectAssistantBoundedMutationField(code, 64)
	base.Operation = projectAssistantBoundedMutationField(operation, 64)
	base.Path = projectAssistantBoundedMutationField(path, 240)
	base.Guidance = projectAssistantBoundedMutationField(guidance, 320)
	if message := projectAssistantMutationDiagnosticMessage(operation, code); message != "" {
		base.Message = message
	}
	return base
}

func projectAssistantMutationDiagnosticMessage(operation, code string) string {
	switch {
	case operation == projectToolCreateFile && code == string(workspace.MutationErrorTargetExists):
		return "The create target already exists."
	case operation == projectToolMoveFile && code == string(workspace.MutationErrorTargetExists):
		return "The move destination already exists."
	case (operation == projectToolReplaceFile || operation == projectToolEditFile) && code == string(workspace.MutationErrorStale):
		return "The file changed before this update was applied."
	case (operation == projectToolDeleteFile || operation == projectToolMoveFile) && code == string(workspace.MutationErrorTargetNotFound):
		return "The source file no longer exists."
	case operation == projectToolEditFile && code == string(workspace.MutationErrorAmbiguous):
		return "The requested text matched multiple locations."
	case code == string(workspace.MutationErrorConflict):
		return "The workspace changed during this mutation."
	case code == string(workspace.MutationErrorVersionRequired):
		return "This mutation needs the file's current version."
	case code == string(workspace.MutationErrorNoChanges):
		return "The requested update would not change the file."
	case code == string(workspace.MutationErrorInvalid):
		return "The mutation input was invalid."
	case code == "mutation_failed":
		return "The file mutation could not be completed."
	default:
		return ""
	}
}

func projectAssistantMutationErrorCode(raw string) string {
	value := strings.ToLower(raw)
	for _, code := range []workspace.MutationErrorCode{
		workspace.MutationErrorInvalid,
		workspace.MutationErrorTargetExists,
		workspace.MutationErrorTargetNotFound,
		workspace.MutationErrorVersionRequired,
		workspace.MutationErrorStale,
		workspace.MutationErrorAmbiguous,
		workspace.MutationErrorNoChanges,
		workspace.MutationErrorConflict,
	} {
		if strings.Contains(value, string(code)) {
			return string(code)
		}
	}
	return ""
}

func projectAssistantMutationErrorPath(raw string) string {
	const marker = `path "`
	start := strings.Index(raw, marker)
	if start < 0 {
		return ""
	}
	start += len(marker)
	end := strings.Index(raw[start:], `"`)
	if end < 0 {
		return ""
	}
	path, err := workspace.CleanProjectPath(raw[start : start+end])
	if err != nil {
		return ""
	}
	return projectAssistantActionSafeTarget(path)
}

func projectAssistantActionDiagnosticMessage(category, raw string) string {
	value := strings.ToLower(raw)
	if category == "validation" {
		switch {
		case strings.Contains(value, string(workspace.MutationErrorStale)):
			return "The file changed or the requested source text was not present. App Studio will reread it before retrying."
		case strings.Contains(value, string(workspace.MutationErrorAmbiguous)):
			return "The requested source text matched more than one location; provide a narrower exact string or explicitly replace all."
		}
	}
	return map[string]string{
		"timeout":    "The action did not finish before its time limit.",
		"permission": "App Studio did not have permission to complete this action.",
		"validation": "The action could not run because its input was not valid.",
		"runtime":    "The development runtime could not complete this action.",
		"provider":   "A connected provider could not complete this action.",
		"unknown":    "App Studio could not complete this action.",
	}[category]
}

func projectAssistantActionDiagnosticCategory(raw string) string {
	value := strings.ToLower(raw)
	switch {
	case strings.Contains(value, "timeout"), strings.Contains(value, "timed out"), strings.Contains(value, "deadline exceeded"):
		return "timeout"
	case strings.Contains(value, "permission"), strings.Contains(value, "forbidden"), strings.Contains(value, "unauthorized"), strings.Contains(value, "access denied"),
		strings.Contains(value, "plan approval required"), strings.Contains(value, "execution plan revision required"):
		return "permission"
	case strings.Contains(value, "validation"), strings.Contains(value, "invalid"), strings.Contains(value, "malformed"),
		strings.Contains(value, "required"), strings.Contains(value, "repository binding"),
		strings.Contains(value, string(workspace.MutationErrorStale)),
		strings.Contains(value, string(workspace.MutationErrorAmbiguous)),
		strings.Contains(value, string(workspace.MutationErrorTargetExists)),
		strings.Contains(value, string(workspace.MutationErrorTargetNotFound)),
		strings.Contains(value, string(workspace.MutationErrorConflict)),
		strings.Contains(value, string(workspace.MutationErrorNoChanges)):
		return "validation"
	case strings.Contains(value, "runtime"), strings.Contains(value, "preview"), strings.Contains(value, "process exited"), strings.Contains(value, "server did not"):
		return "runtime"
	case strings.Contains(value, "provider"), strings.Contains(value, "mcp"), strings.Contains(value, "upstream"):
		return "provider"
	default:
		return "unknown"
	}
}
