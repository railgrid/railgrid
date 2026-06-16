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
	assistantID string
	write       func(projectMessageStreamEvent) error
}

func (w projectAssistantStreamWriter) EmitProjectAssistantEvent(
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
		return w.write(projectMessageStreamEvent{
			Type:               "chunk",
			AssistantMessageID: w.assistantID,
			Content:            event.Delta,
		})
	case projectAssistantEventStatus:
		if event.Status == "" {
			return nil
		}
		return w.write(projectMessageStreamEvent{
			Type:   "status",
			Status: event.Status,
		})
	case projectAssistantEventToolCallStarted, projectAssistantEventToolCallFinished:
		if event.ToolCall == nil || event.ToolCall.ID == "" || event.ToolCall.Status == "" {
			return nil
		}
		toolCall := event.ToolCall.streamEvent()
		return w.write(projectMessageStreamEvent{
			Type:               "tool_call",
			AssistantMessageID: w.assistantID,
			ToolCall:           &toolCall,
		})
	case projectAssistantEventRunFailed:
		if event.Error == "" {
			return nil
		}
		return w.write(projectMessageStreamEvent{
			Type:  "error",
			Error: event.Error,
		})
	case projectAssistantEventRunFinished:
		return w.write(projectMessageStreamEvent{
			Type:               "done",
			AssistantMessageID: w.assistantID,
		})
	default:
		return nil
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
