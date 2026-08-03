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

const projectAssistantUIRootComponentID = "root-col"
const projectAssistantUIDevelopmentPreviewRefreshKey = "development.previewRefreshNeeded"

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
	Questions   []projectAssistantFollowUpQuestion `json:"questions,omitempty"`
	Status      string                             `json:"status,omitempty"`
	Action      *projectAssistantUIInterruptAction `json:"action,omitempty"`
}

type projectAssistantUIInterruptAction struct {
	RunID              string                        `json:"runId"`
	RequestID          string                        `json:"requestId"`
	AssistantMessageID string                        `json:"assistantMessageId,omitempty"`
	Exec               *projectAssistantExecMetadata `json:"exec,omitempty"`
}

func projectAssistantUIDataUpdateEvent(surfaceID, key, value string) projectAssistantUIEvent {
	return projectAssistantUIDataContentEvent(surfaceID, key, value, false)
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

func projectAssistantUIDevelopmentPreviewRefreshEvent() projectAssistantUIEvent {
	return projectAssistantUIDataUpdateEvent("conversation", projectAssistantUIDevelopmentPreviewRefreshKey, "true")
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
			Exec:               cloneProjectAssistantExecMetadata(permission.Exec),
		},
	}
}

func projectAssistantUIInterruptRequestFromFollowUpCheckpoint(surfaceID string, followUp projectAssistantFollowUp, checkpoint projectAssistantCheckpoint) *projectAssistantUIInterruptRequest {
	return &projectAssistantUIInterruptRequest{
		InterruptID: followUp.ID,
		Kind:        "follow_up",
		SurfaceID:   surfaceID,
		Description: followUp.Prompt,
		Questions:   cloneProjectAssistantFollowUpQuestions(followUp.Questions),
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
