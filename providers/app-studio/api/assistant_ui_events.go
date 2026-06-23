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

import "fmt"

const projectAssistantUIEventType = "ui"

const (
	projectAssistantUIActionInspect = "inspect"
	projectAssistantUIActionClarify = "clarify"
	projectAssistantUIActionEdit    = "edit"
	projectAssistantUIActionRun     = "run"
	projectAssistantUIActionCommit  = "commit"
	projectAssistantUIActionPlan    = "plan"
	projectAssistantUIActionOther   = "other"
)

type projectAssistantUIEvent struct {
	BeginRendering   *projectAssistantUIBeginRendering   `json:"beginRendering,omitempty"`
	SurfaceUpdate    *projectAssistantUISurfaceUpdate    `json:"surfaceUpdate,omitempty"`
	DataModelUpdate  *projectAssistantUIDataModelUpdate  `json:"dataModelUpdate,omitempty"`
	InterruptRequest *projectAssistantUIInterruptRequest `json:"interruptRequest,omitempty"`
}

type projectAssistantUIBeginRendering struct {
	SurfaceID string `json:"surfaceId"`
	Root      string `json:"root"`
}

type projectAssistantUISurfaceUpdate struct {
	SurfaceID  string                        `json:"surfaceId"`
	Components []projectAssistantUIComponent `json:"components,omitempty"`
}

type projectAssistantUIComponent struct {
	ID             string                    `json:"id"`
	Type           string                    `json:"type"`
	ToolDisclosure *projectAssistantUIAction `json:"toolDisclosure,omitempty"`
}

type projectAssistantUIAction struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Label   string `json:"label"`
	Summary string `json:"summary,omitempty"`
	Count   int    `json:"count,omitempty"`
}

type projectAssistantUIDataModelUpdate struct {
	SurfaceID string                          `json:"surfaceId"`
	Contents  []projectAssistantUIDataContent `json:"contents,omitempty"`
}

type projectAssistantUIDataContent struct {
	Key         string `json:"key"`
	ValueString string `json:"valueString,omitempty"`
	Append      bool   `json:"append,omitempty"`
}

type projectAssistantUIInterruptRequest struct {
	InterruptID string                             `json:"interruptId"`
	Kind        string                             `json:"kind,omitempty"`
	SurfaceID   string                             `json:"surfaceId,omitempty"`
	Description string                             `json:"description,omitempty"`
	Questions   []string                           `json:"questions,omitempty"`
	Status      string                             `json:"status,omitempty"`
	Action      *projectAssistantUIInterruptAction `json:"action,omitempty"`
}

type projectAssistantUIInterruptAction struct {
	RunID              string `json:"runId"`
	RequestID          string `json:"requestId"`
	AssistantMessageID string `json:"assistantMessageId,omitempty"`
}

func projectAssistantUIBeginRenderingEvent(surfaceID string) projectAssistantUIEvent {
	return projectAssistantUIEvent{
		BeginRendering: &projectAssistantUIBeginRendering{
			SurfaceID: surfaceID,
			Root:      "assistant-message",
		},
	}
}

func projectAssistantUIContentDeltaEvent(surfaceID, delta string) projectAssistantUIEvent {
	return projectAssistantUIEvent{
		DataModelUpdate: &projectAssistantUIDataModelUpdate{
			SurfaceID: surfaceID,
			Contents: []projectAssistantUIDataContent{{
				Key:         "assistant.content",
				ValueString: delta,
				Append:      true,
			}},
		},
	}
}

func projectAssistantUIStatusEvent(status string) projectAssistantUIEvent {
	return projectAssistantUIEvent{
		DataModelUpdate: &projectAssistantUIDataModelUpdate{
			SurfaceID: "conversation",
			Contents: []projectAssistantUIDataContent{{
				Key:         "assistant.status",
				ValueString: status,
			}},
		},
	}
}

func projectAssistantUIToolDisclosureEvent(surfaceID string, action projectAssistantUIAction) projectAssistantUIEvent {
	event := projectAssistantUIEvent{
		SurfaceUpdate: &projectAssistantUISurfaceUpdate{
			SurfaceID: surfaceID,
			Components: []projectAssistantUIComponent{{
				ID:             action.ID,
				Type:           "toolDisclosure",
				ToolDisclosure: &action,
			}},
		},
	}
	if progress := projectAssistantUIProgressText(action); progress != "" {
		event.DataModelUpdate = &projectAssistantUIDataModelUpdate{
			SurfaceID: surfaceID,
			Contents: []projectAssistantUIDataContent{{
				Key:         "assistant.progress",
				ValueString: progress,
			}},
		}
	}
	return event
}

func projectAssistantUIInterruptRequestEvent(surfaceID string, permission projectAssistantPermission, checkpoint projectAssistantCheckpoint) projectAssistantUIEvent {
	return projectAssistantUIEvent{
		InterruptRequest: projectAssistantUIInterruptRequestFromPermissionCheckpoint(surfaceID, permission, checkpoint),
	}
}

func projectAssistantUIFollowUpInterruptRequestEvent(surfaceID string, followUp projectAssistantFollowUp, checkpoint projectAssistantCheckpoint) projectAssistantUIEvent {
	return projectAssistantUIEvent{
		InterruptRequest: projectAssistantUIInterruptRequestFromFollowUpCheckpoint(surfaceID, followUp, checkpoint),
	}
}

func projectAssistantUIActionFromToolCall(toolCall projectToolCallStreamEvent) projectAssistantUIAction {
	return projectAssistantUIActionFromFields(toolCall.ID, toolCall.Name, toolCall.Status)
}

func projectAssistantUIActionFromAssistantToolCall(toolCall projectAssistantToolCall) projectAssistantUIAction {
	return projectAssistantUIActionFromFields(toolCall.ID, toolCall.Name, toolCall.Status)
}

func projectAssistantUIActionFromPermission(permission projectAssistantPermission) projectAssistantUIAction {
	return projectAssistantUIActionFromFields(permission.ToolCallID, permission.ToolName, "permission_required")
}

func projectAssistantUIActionFromFollowUp(followUp projectAssistantFollowUp) projectAssistantUIAction {
	return projectAssistantUIActionFromFields(followUp.ToolCallID, projectToolAskFollowUp, "input_required")
}

func projectAssistantUIActionFromFields(id, name, status string) projectAssistantUIAction {
	kind := projectAssistantUIActionKind(name)
	status = projectAssistantUIActionStatus(status)
	label, summary := projectAssistantUIActionText(kind, status, 1)
	return projectAssistantUIAction{
		ID:      id,
		Kind:    kind,
		Status:  status,
		Label:   label,
		Summary: summary,
		Count:   1,
	}
}

func projectAssistantUIActionStatus(status string) string {
	switch status {
	case "permission_required":
		return "awaiting_approval"
	case "input_required":
		return "awaiting_input"
	case "":
		return "running"
	default:
		return status
	}
}

func projectAssistantUIInterruptRequestFromPermissionCheckpoint(surfaceID string, permission projectAssistantPermission, checkpoint projectAssistantCheckpoint) *projectAssistantUIInterruptRequest {
	return &projectAssistantUIInterruptRequest{
		InterruptID: permission.ID,
		Kind:        "permission",
		SurfaceID:   surfaceID,
		Description: permission.Reason,
		Status:      "pending",
		Action: &projectAssistantUIInterruptAction{
			RunID:              checkpoint.ID,
			RequestID:          permission.ID,
			AssistantMessageID: surfaceID,
		},
	}
}

func projectAssistantUIInterruptRequestFromFollowUpCheckpoint(surfaceID string, followUp projectAssistantFollowUp, checkpoint projectAssistantCheckpoint) *projectAssistantUIInterruptRequest {
	return &projectAssistantUIInterruptRequest{
		InterruptID: followUp.ID,
		Kind:        "follow_up",
		SurfaceID:   surfaceID,
		Description: followUp.Prompt,
		Questions:   append([]string(nil), followUp.Questions...),
		Status:      "pending",
		Action: &projectAssistantUIInterruptAction{
			RunID:              checkpoint.ID,
			RequestID:          followUp.ID,
			AssistantMessageID: surfaceID,
		},
	}
}

func projectAssistantUIResolvedInterruptEvent(surfaceID, interruptID string) projectAssistantUIEvent {
	return projectAssistantUIEvent{
		InterruptRequest: &projectAssistantUIInterruptRequest{
			InterruptID: interruptID,
			SurfaceID:   surfaceID,
			Status:      "resolved",
		},
	}
}

func projectAssistantUIActionKind(name string) string {
	switch base := projectToolBaseName(name); {
	case base == projectToolAskFollowUp:
		return projectAssistantUIActionClarify
	case base == projectToolRequestProjectPlanApproval:
		return projectAssistantUIActionPlan
	case base == projectToolPlanProjectChanges:
		return projectAssistantUIActionPlan
	case base == projectToolCheckProjectReadiness || base == projectToolPrepareProjectDeployment || base == projectToolDeployProjectRuntime || base == projectToolGetRuntimeStatus || base == projectToolGetPreviewURL:
		return projectAssistantUIActionRun
	case base == projectToolCommitProjectFiles || base == projectToolCommitFiles:
		return projectAssistantUIActionCommit
	case base == projectToolWriteFile || base == projectToolApplyPatch || base == projectToolMkdir:
		return projectAssistantUIActionEdit
	case base == projectToolListProjectFiles || base == projectToolReadProjectFile || base == projectToolSearchProjectFiles:
		return projectAssistantUIActionInspect
	default:
		return projectAssistantUIActionOther
	}
}

func projectAssistantUIActionText(kind, status string, count int) (string, string) {
	active := status == "requested" || status == "running" || status == "awaiting_approval" || status == "awaiting_input"
	failed := status == "failed" || status == "rejected"
	switch kind {
	case projectAssistantUIActionClarify:
		return projectAssistantUIActionLabel(active, failed, "Clarifying requirements", "Clarified requirements", "Clarification failed"), projectAssistantUIActionCount(count, "question", "questions")
	case projectAssistantUIActionInspect:
		return projectAssistantUIActionLabel(active, failed, "Inspecting project", "Inspected project", "Inspection failed"), projectAssistantUIActionCount(count, "inspection", "inspections")
	case projectAssistantUIActionEdit:
		return projectAssistantUIActionLabel(active, failed, "Editing files", "Edited files", "Edit failed"), projectAssistantUIActionCount(count, "edit action", "edit actions")
	case projectAssistantUIActionRun:
		return projectAssistantUIActionLabel(active, failed, "Running checks", "Ran checks", "Run failed"), projectAssistantUIActionCount(count, "check", "checks")
	case projectAssistantUIActionCommit:
		return projectAssistantUIActionLabel(active, failed, "Preparing commit", "Committed changes", "Commit failed"), projectAssistantUIActionCount(count, "commit step", "commit steps")
	case projectAssistantUIActionPlan:
		return projectAssistantUIActionLabel(active, failed, "Reviewing plan", "Reviewed plan", "Plan rejected"), projectAssistantUIActionCount(count, "plan step", "plan steps")
	default:
		return projectAssistantUIActionLabel(active, failed, "Working", "Completed actions", "Action failed"), projectAssistantUIActionCount(count, "tool action", "tool actions")
	}
}

func projectAssistantUIProgressText(action projectAssistantUIAction) string {
	switch action.Status {
	case "awaiting_approval":
		return "Waiting for your approval."
	case "awaiting_input":
		return "Waiting for your answer."
	}
	active := action.Status == "requested" || action.Status == "running"
	failed := action.Status == "failed" || action.Status == "rejected"
	if !active && !failed {
		switch action.Kind {
		case projectAssistantUIActionClarify:
			return "I have the clarification I need."
		case projectAssistantUIActionEdit:
			return "Updated the project files."
		case projectAssistantUIActionCommit:
			return "Saved the completed changes."
		case projectAssistantUIActionPlan:
			return "I have a plan for the change and am moving into implementation."
		default:
			return ""
		}
	}
	switch action.Kind {
	case projectAssistantUIActionClarify:
		return projectAssistantUIProgressLabel(active, failed, "Asking for the detail needed to continue.", "Clarification failed.")
	case projectAssistantUIActionInspect:
		return projectAssistantUIProgressLabel(active, failed, "Looking through the project to find the relevant code.", "Inspection failed.")
	case projectAssistantUIActionEdit:
		return projectAssistantUIProgressLabel(active, failed, "Applying the requested change in the project files.", "Edit failed.")
	case projectAssistantUIActionRun:
		return projectAssistantUIProgressLabel(active, failed, "Checking the project state so I can make the change safely.", "Run failed.")
	case projectAssistantUIActionCommit:
		return projectAssistantUIProgressLabel(active, failed, "Saving the completed changes.", "Commit failed.")
	case projectAssistantUIActionPlan:
		return projectAssistantUIProgressLabel(active, failed, "Reviewing the change plan before editing.", "Plan rejected.")
	default:
		return projectAssistantUIProgressLabel(active, failed, "Working through the next step.", "Action failed.")
	}
}

func projectAssistantUIProgressLabel(active, failed bool, activeLabel, failedLabel string) string {
	if failed {
		return failedLabel
	}
	if active {
		return activeLabel
	}
	return ""
}

func projectAssistantUIActionLabel(active, failed bool, activeLabel, doneLabel, failedLabel string) string {
	if failed {
		return failedLabel
	}
	if active {
		return activeLabel
	}
	return doneLabel
}

func projectAssistantUIActionCount(count int, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", count, plural)
}
