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

const projectAssistantUIRootComponentID = "root-col"
const projectAssistantUIDevelopmentPreviewRefreshKey = "development.previewRefreshNeeded"

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
	ID        string                           `json:"id"`
	Component projectAssistantUIComponentValue `json:"component"`
}

type projectAssistantUIComponentValue struct {
	Text   *projectAssistantUITextComponent   `json:"Text,omitempty"`
	Column *projectAssistantUIColumnComponent `json:"Column,omitempty"`
	Card   *projectAssistantUICardComponent   `json:"Card,omitempty"`
	Row    *projectAssistantUIRowComponent    `json:"Row,omitempty"`
}

type projectAssistantUITextComponent struct {
	Value     string `json:"value,omitempty"`
	DataKey   string `json:"dataKey,omitempty"`
	UsageHint string `json:"usageHint,omitempty"`
}

type projectAssistantUIColumnComponent struct {
	Children []string `json:"children"`
}

type projectAssistantUICardComponent struct {
	Children []string `json:"children"`
}

type projectAssistantUIRowComponent struct {
	Children []string `json:"children"`
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
			Root:      projectAssistantUIRootComponentID,
		},
	}
}

func projectAssistantUIDataUpdateEvent(surfaceID, key, value string) projectAssistantUIEvent {
	return projectAssistantUIDataContentEvent(surfaceID, key, value, false)
}

func projectAssistantUIDataAppendEvent(surfaceID, key, value string) projectAssistantUIEvent {
	return projectAssistantUIDataContentEvent(surfaceID, key, value, true)
}

func projectAssistantUIDataContentEvent(surfaceID, key, value string, appendValue bool) projectAssistantUIEvent {
	return projectAssistantUIEvent{
		DataModelUpdate: &projectAssistantUIDataModelUpdate{
			SurfaceID: surfaceID,
			Contents: []projectAssistantUIDataContent{{
				Key:         key,
				ValueString: value,
				Append:      appendValue,
			}},
		},
	}
}

func projectAssistantUIMessageShellEvent(surfaceID string, rootChildren []string, cardID, colID, roleID, contentID, dataKey, roleLabel string) projectAssistantUIEvent {
	return projectAssistantUIEvent{
		SurfaceUpdate: &projectAssistantUISurfaceUpdate{
			SurfaceID: surfaceID,
			Components: []projectAssistantUIComponent{
				projectAssistantUIColumnComponentNode(projectAssistantUIRootComponentID, append([]string(nil), rootChildren...)),
				projectAssistantUICardComponentNode(cardID, []string{colID}),
				projectAssistantUIColumnComponentNode(colID, []string{roleID, contentID}),
				projectAssistantUITextComponentNode(roleID, roleLabel, "caption"),
				projectAssistantUIBoundTextComponentNode(contentID, dataKey, "body"),
			},
		},
	}
}

func projectAssistantUIToolCardEvent(surfaceID string, rootChildren []string, cardID, colID, labelID, textID, kind, text string) projectAssistantUIEvent {
	return projectAssistantUIEvent{
		SurfaceUpdate: &projectAssistantUISurfaceUpdate{
			SurfaceID: surfaceID,
			Components: []projectAssistantUIComponent{
				projectAssistantUIColumnComponentNode(projectAssistantUIRootComponentID, append([]string(nil), rootChildren...)),
				projectAssistantUICardComponentNode(cardID, []string{colID}),
				projectAssistantUIColumnComponentNode(colID, []string{labelID, textID}),
				projectAssistantUITextComponentNode(labelID, kind, "caption"),
				projectAssistantUITextComponentNode(textID, text, "body"),
			},
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

func projectAssistantUIDevelopmentPreviewRefreshEvent() projectAssistantUIEvent {
	return projectAssistantUIDataUpdateEvent("conversation", projectAssistantUIDevelopmentPreviewRefreshKey, "true")
}

func projectAssistantUITextComponentNode(id, value, usageHint string) projectAssistantUIComponent {
	return projectAssistantUIComponent{
		ID: id,
		Component: projectAssistantUIComponentValue{
			Text: &projectAssistantUITextComponent{
				Value:     value,
				UsageHint: usageHint,
			},
		},
	}
}

func projectAssistantUIBoundTextComponentNode(id, dataKey, usageHint string) projectAssistantUIComponent {
	return projectAssistantUIComponent{
		ID: id,
		Component: projectAssistantUIComponentValue{
			Text: &projectAssistantUITextComponent{
				DataKey:   dataKey,
				UsageHint: usageHint,
			},
		},
	}
}

func projectAssistantUIColumnComponentNode(id string, children []string) projectAssistantUIComponent {
	return projectAssistantUIComponent{
		ID: id,
		Component: projectAssistantUIComponentValue{
			Column: &projectAssistantUIColumnComponent{Children: children},
		},
	}
}

func projectAssistantUICardComponentNode(id string, children []string) projectAssistantUIComponent {
	return projectAssistantUIComponent{
		ID: id,
		Component: projectAssistantUIComponentValue{
			Card: &projectAssistantUICardComponent{Children: children},
		},
	}
}

func projectAssistantUIRowComponentNode(id string, children []string) projectAssistantUIComponent {
	return projectAssistantUIComponent{
		ID: id,
		Component: projectAssistantUIComponentValue{
			Row: &projectAssistantUIRowComponent{Children: children},
		},
	}
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
	case base == projectToolCheckProjectReadiness || base == projectToolPrepareProjectDeployment || base == projectToolDeployProjectRuntime || base == projectToolGetRuntimeStatus || base == projectToolGetPreviewURL || base == projectToolGetRuntimeLogs || base == projectToolRestartRuntime || base == projectToolSetRuntimeEnv:
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
