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

import "context"

type projectAssistantStreamWriter struct {
	assistantID       string
	began             bool
	pendingPermission *projectAssistantPermission
	pendingFollowUp   *projectAssistantFollowUp
	write             func(projectMessageStreamEvent) error
}

func (w *projectAssistantStreamWriter) EmitProjectAssistantEvent(
	ctx context.Context,
	event projectAssistantEvent,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch event.Type {
	case projectAssistantEventMessageDelta:
		if event.Delta == "" {
			return nil
		}
		return w.writeAssistantUI(ctx, projectAssistantUIContentDeltaEvent(w.assistantID, event.Delta))
	case projectAssistantEventStatus:
		if event.Status == "" {
			return nil
		}
		return w.writeUI(ctx, "", projectAssistantUIStatusEvent(event.Status), false)
	case projectAssistantEventToolCallStarted, projectAssistantEventToolCallFinished:
		if event.ToolCall == nil || event.ToolCall.ID == "" || event.ToolCall.Status == "" {
			return nil
		}
		action := projectAssistantUIActionFromAssistantToolCall(*event.ToolCall)
		return w.writeAssistantUI(ctx, projectAssistantUIToolDisclosureEvent(w.assistantID, action))
	case projectAssistantEventPermissionNeeded:
		if event.Permission == nil || event.Permission.ID == "" {
			return nil
		}
		permission := *event.Permission
		permission.Input = nil
		w.pendingPermission = &permission
		action := projectAssistantUIActionFromPermission(permission)
		return w.writeAssistantUI(ctx, projectAssistantUIToolDisclosureEvent(w.assistantID, action))
	case projectAssistantEventInputNeeded:
		if event.FollowUp == nil || event.FollowUp.ID == "" {
			return nil
		}
		followUp := *event.FollowUp
		w.pendingFollowUp = &followUp
		action := projectAssistantUIActionFromFollowUp(followUp)
		return w.writeAssistantUI(ctx, projectAssistantUIToolDisclosureEvent(w.assistantID, action))
	case projectAssistantEventCheckpointSaved:
		if event.Checkpoint == nil || event.Checkpoint.ID == "" {
			return nil
		}
		if w.pendingFollowUp != nil {
			followUp := *w.pendingFollowUp
			checkpoint := *event.Checkpoint
			return w.writeAssistantUI(ctx, projectAssistantUIFollowUpInterruptRequestEvent(w.assistantID, followUp, checkpoint))
		}
		if w.pendingPermission != nil {
			permission := *w.pendingPermission
			checkpoint := *event.Checkpoint
			return w.writeAssistantUI(ctx, projectAssistantUIInterruptRequestEvent(w.assistantID, permission, checkpoint))
		}
		return nil
	case projectAssistantEventBuilderEvent:
		if event.BuilderEvent == nil || event.BuilderEvent.ID == "" {
			return nil
		}
		return w.writeAssistantUI(ctx, projectAssistantUIBuilderEvent(w.assistantID, *event.BuilderEvent))
	case projectAssistantEventRunFailed:
		if event.Error == "" {
			return nil
		}
		return w.write(projectMessageStreamEvent{
			Type:  string(projectAssistantEventRunFailed),
			Error: event.Error,
		})
	case projectAssistantEventRunFinished:
		return w.write(projectMessageStreamEvent{
			Type:               string(projectAssistantEventRunFinished),
			AssistantMessageID: w.assistantID,
		})
	default:
		return nil
	}
}

func (w *projectAssistantStreamWriter) writeAssistantUI(ctx context.Context, ui projectAssistantUIEvent) error {
	return w.writeUI(ctx, w.assistantID, ui, true)
}

func (w *projectAssistantStreamWriter) writeUI(ctx context.Context, assistantID string, ui projectAssistantUIEvent, begin bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if begin && assistantID != "" && !w.began {
		beginUI := projectAssistantUIBeginRenderingEvent(assistantID)
		if err := w.write(projectMessageStreamEvent{
			Type:               projectAssistantUIEventType,
			AssistantMessageID: assistantID,
			UI:                 &beginUI,
		}); err != nil {
			return err
		}
		w.began = true
	}
	return w.write(projectMessageStreamEvent{
		Type:               projectAssistantUIEventType,
		AssistantMessageID: assistantID,
		UI:                 &ui,
	})
}

func projectAssistantUIBuilderEvent(surfaceID string, event projectBuilderEventView) projectAssistantUIEvent {
	return projectAssistantUIBuilderDataEvent(surfaceID, event.Type)
}

func projectAssistantUIBuilderDataEvent(surfaceID, eventType string) projectAssistantUIEvent {
	return projectAssistantUIEvent{
		DataModelUpdate: &projectAssistantUIDataModelUpdate{
			SurfaceID: surfaceID,
			Contents: []projectAssistantUIDataContent{{
				Key:         "builder.event",
				ValueString: eventType,
			}},
		},
	}
}

func (tc projectAssistantToolCall) streamEvent() projectToolCallStreamEvent {
	return projectToolCallStreamEvent{
		ID:        tc.ID,
		Name:      tc.Name,
		Status:    tc.Status,
		Arguments: tc.Arguments,
		Summary:   tc.Summary,
		Error:     tc.Error,
	}
}

func projectAssistantEventTypeForToolCallStatus(status string) projectAssistantEventType {
	switch status {
	case "requested", "running":
		return projectAssistantEventToolCallStarted
	default:
		return projectAssistantEventToolCallFinished
	}
}
