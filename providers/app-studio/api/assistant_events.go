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
	"strconv"
)

type projectAssistantStreamWriter struct {
	assistantID                 string
	began                       bool
	rootChildren                []string
	msgIdx                      int
	assistantDataKey            string
	assistantShellSet           bool
	acceptedAssistantContent    string
	provisionalAssistantContent string
	pendingPermission           *projectAssistantPermission
	pendingFollowUp             *projectAssistantFollowUp
	write                       func(projectMessageStreamEvent) error
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
		return w.writeAssistantContent(ctx, event.Delta)
	case projectAssistantEventStatus:
		if event.Status == "" {
			return nil
		}
		return w.writeUI(ctx, w.assistantID, projectAssistantUIStatusEvent(event.Status), false)
	case projectAssistantEventToolCallStarted, projectAssistantEventToolCallFinished:
		// Tool lifecycle is carried only by assistantActionFeed in durable
		// message snapshots. A2UI remains reserved for assistant content and
		// interrupt surfaces.
		return nil
	case projectAssistantEventPermissionNeeded:
		if event.Permission == nil || event.Permission.ID == "" {
			return nil
		}
		permission := *event.Permission
		permission.Input = nil
		w.pendingPermission = &permission
		return nil
	case projectAssistantEventInputNeeded:
		if event.FollowUp == nil || event.FollowUp.ID == "" {
			return nil
		}
		followUp := *event.FollowUp
		w.pendingFollowUp = &followUp
		return nil
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
			Type:               string(projectAssistantEventRunFailed),
			AssistantMessageID: w.assistantID,
			Error:              event.Error,
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

func (w *projectAssistantStreamWriter) writeAssistantContent(ctx context.Context, delta string) error {
	return w.WriteAcceptedAssistantContent(ctx, delta)
}

func (w *projectAssistantStreamWriter) WriteAcceptedAssistantContent(ctx context.Context, delta string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delta == "" {
		return nil
	}
	if err := w.ensureAssistantContentShell(ctx); err != nil {
		return err
	}
	w.acceptedAssistantContent += delta
	if w.provisionalAssistantContent != "" {
		rendered := w.acceptedAssistantContent[:len(w.acceptedAssistantContent)-len(delta)] + w.provisionalAssistantContent
		w.provisionalAssistantContent = ""
		if rendered == w.acceptedAssistantContent {
			return nil
		}
		return w.write(projectMessageStreamEventFromUI(projectAssistantUIDataUpdateEvent(
			w.assistantID,
			w.assistantDataKey,
			w.acceptedAssistantContent,
		)))
	}
	return w.write(projectMessageStreamEventFromUI(projectAssistantUIDataAppendEvent(w.assistantID, w.assistantDataKey, delta)))
}

// WriteDurableAssistantContent applies a complete durable snapshot. Unlike
// WriteAcceptedAssistantContent it never treats the input as a token delta:
// reconnects and legacy adapters receive full revisions and must replace the
// stable data-model value instead of appending it again.
func (w *projectAssistantStreamWriter) WriteDurableAssistantContent(ctx context.Context, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.ensureAssistantContentShell(ctx); err != nil {
		return err
	}
	if w.acceptedAssistantContent == content && w.provisionalAssistantContent == "" {
		return nil
	}
	w.acceptedAssistantContent = content
	w.provisionalAssistantContent = ""
	return w.write(projectMessageStreamEventFromUI(projectAssistantUIDataUpdateEvent(
		w.assistantID,
		w.assistantDataKey,
		content,
	)))
}

func (w *projectAssistantStreamWriter) WriteProvisionalAssistantContent(ctx context.Context, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if content == "" {
		return w.ResetProvisionalAssistantContent(ctx)
	}
	if err := w.ensureAssistantContentShell(ctx); err != nil {
		return err
	}
	w.provisionalAssistantContent = content
	return w.write(projectMessageStreamEventFromUI(projectAssistantUIDataUpdateEvent(
		w.assistantID,
		w.assistantDataKey,
		w.acceptedAssistantContent+content,
	)))
}

func (w *projectAssistantStreamWriter) ResetProvisionalAssistantContent(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.provisionalAssistantContent == "" {
		return nil
	}
	w.provisionalAssistantContent = ""
	return w.write(projectMessageStreamEventFromUI(projectAssistantUIDataUpdateEvent(
		w.assistantID,
		w.assistantDataKey,
		w.acceptedAssistantContent,
	)))
}

func (w *projectAssistantStreamWriter) ensureAssistantContentShell(ctx context.Context) error {
	if err := w.ensureBegin(ctx); err != nil {
		return err
	}
	if !w.assistantShellSet {
		cardID, colID, roleID, contentID := w.nextMessageComponentIDs()
		w.assistantDataKey = w.assistantID + "/" + contentID
		w.rootChildren = append(w.rootChildren, cardID)
		if err := w.write(projectMessageStreamEventFromUI(projectAssistantUIMessageShellEvent(
			w.assistantID,
			w.rootChildren,
			cardID,
			colID,
			roleID,
			contentID,
			w.assistantDataKey,
			"assistant",
		))); err != nil {
			return err
		}
		w.assistantShellSet = true
	}
	return nil
}

func (w *projectAssistantStreamWriter) writeUI(ctx context.Context, assistantID string, ui projectAssistantUIEvent, begin bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if projectAssistantUIEventIsEmpty(ui) {
		return nil
	}
	if begin {
		if err := w.ensureBegin(ctx); err != nil {
			return err
		}
	}
	return w.write(projectMessageStreamEventFromUI(ui))
}

func (w *projectAssistantStreamWriter) ensureBegin(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.assistantID == "" || w.began {
		return nil
	}
	if err := w.write(projectMessageStreamEventFromUI(projectAssistantUIBeginRenderingEvent(w.assistantID))); err != nil {
		return err
	}
	w.began = true
	return nil
}

func (w *projectAssistantStreamWriter) nextMessageComponentIDs() (string, string, string, string) {
	idx := w.msgIdx
	w.msgIdx++
	id := strconv.Itoa(idx)
	cardID := "msg-" + id + "-card"
	colID := "msg-" + id + "-col"
	labelID := "msg-" + id + "-label"
	textID := "msg-" + id + "-text"
	return cardID, colID, labelID, textID
}

func projectAssistantUIEventIsEmpty(ui projectAssistantUIEvent) bool {
	return ui.BeginRendering == nil && ui.SurfaceUpdate == nil && ui.DataModelUpdate == nil && ui.InterruptRequest == nil
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
