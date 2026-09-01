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
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func projectEinoAssistantRunOptions(req projectAssistantRunRequest, runState *projectEinoAssistantRunState) []adk.AgentRunOption {
	handler := newProjectEinoAssistantModelCallbackHandler(req.StreamCallbacks, runState, req.auditRecorder)
	opts := []adk.AgentRunOption{}
	// TurnLoop.Stop can only propagate a graceful/immediate cancellation into
	// the active agent (and therefore into a running tool) when the agent run
	// opts into Eino's cancellation context. Without this option Stop merely
	// waits for the current tool to return, leaving a remote exec session alive
	// while the durable run is already being interrupted.
	cancelOption, _ := adk.WithCancel()
	opts = append(opts, cancelOption)
	if handler != nil {
		opts = append(opts, adk.WithCallbacks(handler))
	}
	if snapshot := runState.SessionSnapshot(); snapshot != nil {
		opts = append(opts, adk.WithSessionValues(map[string]any{
			projectEinoAssistantSessionSnapshotKey: *snapshot,
		}))
	}
	return opts
}

func newProjectEinoAssistantModelCallbackHandler(
	streamCallbacks projectAssistantStreamCallbacks,
	runState *projectEinoAssistantRunState,
	auditRecorder *projectAssistantRunAuditRecorder,
) callbacks.Handler {
	recorder := &projectEinoAssistantModelCallbackRecorder{
		streamCallbacks: streamCallbacks,
		runState:        runState,
		auditRecorder:   auditRecorder,
	}
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			recorder.recordModelInput(ctx, input)
			return ctx
		}).
		OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			recorder.recordModelOutput(ctx, output)
			return ctx
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, _ *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			recorder.recordModelStream(ctx, output)
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, _ *callbacks.RunInfo, err error) context.Context {
			recorder.recordModelError(ctx, err)
			return ctx
		}).
		Build()
}

type projectEinoAssistantModelCallbackRecorder struct {
	streamCallbacks projectAssistantStreamCallbacks
	runState        *projectEinoAssistantRunState
	auditRecorder   *projectAssistantRunAuditRecorder

	mu                      sync.Mutex
	reportedToolPreparation bool
	modelInputs             map[int][]projectAssistantModelInputEvent
}

func (r *projectEinoAssistantModelCallbackRecorder) recordModelInput(ctx context.Context, input callbacks.CallbackInput) {
	modelInput := einomodel.ConvCallbackInput(input)
	if modelInput == nil || len(modelInput.Messages) == 0 {
		return
	}
	// The callback runs after Eino has copied the state into the model input and
	// immediately before the provider call. Remove model-only attachment
	// messages from the graph state while retaining the callback's input slice,
	// so an interrupt/cancel checkpoint cannot persist verified image bytes.
	_ = compose.ProcessState[*adk.State](ctx, func(_ context.Context, state *adk.State) error {
		state.Messages = projectEinoAssistantMessagesWithoutAttachments(state.Messages)
		return nil
	})
	ordinal := r.runState.CurrentModelCallOrdinal()
	attachments := projectAssistantModelInputEvents(modelInput.Messages, ordinal)
	if len(attachments) > 0 {
		r.mu.Lock()
		fresh := make([]projectAssistantModelInputEvent, 0, len(attachments))
		for _, attachment := range attachments {
			if r.runState.ModelInputCompleted(attachment.ID) {
				continue
			}
			fresh = append(fresh, attachment)
		}
		if r.modelInputs == nil {
			r.modelInputs = map[int][]projectAssistantModelInputEvent{}
		}
		r.modelInputs[ordinal] = append([]projectAssistantModelInputEvent(nil), fresh...)
		r.mu.Unlock()
		for _, attachment := range fresh {
			r.emitModelInput(attachment)
		}
	}
	r.runState.RecordModelInput(projectEinoMessagesToChat(modelInput.Messages))
}

func projectAssistantModelInputEvents(messages []*schema.Message, ordinal int) []projectAssistantModelInputEvent {
	if ordinal <= 0 || len(messages) == 0 {
		return nil
	}
	events := make([]projectAssistantModelInputEvent, 0)
	seen := map[string]struct{}{}
	for _, message := range messages {
		if !projectEinoAssistantAttachmentMessage(message) || len(message.UserInputMultiContent) == 0 {
			continue
		}
		id, _ := message.Extra[projectAssistantAttachmentMessageIDKey].(string)
		filename, _ := message.Extra[projectAssistantAttachmentMessageFilenameKey].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		for _, part := range message.UserInputMultiContent {
			if part.Type != schema.ChatMessagePartTypeImageURL || part.Image == nil || part.Image.Base64Data == nil {
				continue
			}
			key := id
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			events = append(events, projectAssistantModelInputEvent{
				ID:          "image-input-" + key,
				Filename:    filename,
				ContentType: part.Image.MIMEType,
				Ordinal:     ordinal,
				Status:      "started",
			})
		}
	}
	return events
}

func (r *projectEinoAssistantModelCallbackRecorder) emitModelInput(event projectAssistantModelInputEvent) {
	if r == nil || r.streamCallbacks.OnModelInput == nil {
		return
	}
	r.streamCallbacks.OnModelInput(event)
}

func (r *projectEinoAssistantModelCallbackRecorder) finishModelInputs(status, errText string) {
	if r == nil {
		return
	}
	ordinal := r.runState.CurrentModelCallOrdinal()
	r.mu.Lock()
	attachments := append([]projectAssistantModelInputEvent(nil), r.modelInputs[ordinal]...)
	delete(r.modelInputs, ordinal)
	if status == "completed" && len(attachments) > 0 {
		for _, attachment := range attachments {
			r.runState.RecordCompletedModelInput(attachment.ID)
		}
	}
	r.mu.Unlock()
	for _, attachment := range attachments {
		attachment.Status = status
		attachment.Error = errText
		r.emitModelInput(attachment)
	}
}

func (r *projectEinoAssistantModelCallbackRecorder) recordModelOutput(ctx context.Context, output callbacks.CallbackOutput) {
	modelOutput := einomodel.ConvCallbackOutput(output)
	if modelOutput == nil || modelOutput.Message == nil {
		r.finishModelInputs("failed", "model provider returned no response")
		return
	}
	r.finishModelInputs("completed", "")
	if r.auditRecorder != nil && projectEinoAssistantMeaningfulModelChunk(modelOutput.Message) {
		r.auditRecorder.recordModelResponseChunk(ctx, len(modelOutput.Message.ToolCalls) > 0)
	}
	if r.auditRecorder != nil {
		_ = r.auditRecorder.recordModelResult(ctx, r.runState.CurrentModelCallOrdinal(), modelOutput.Message)
	}
	reply := projectEinoAssistantReplyFromMessage(modelOutput.Message)
	if strings.TrimSpace(reply.Content) == "" && len(reply.ToolCalls) == 0 {
		return
	}
	r.runState.RecordAssistantReply(reply)
}

func (r *projectEinoAssistantModelCallbackRecorder) recordModelStream(
	ctx context.Context,
	output *schema.StreamReader[callbacks.CallbackOutput],
) {
	if output == nil {
		r.finishModelInputs("failed", "model provider returned no stream")
		return
	}
	defer output.Close()

	var content strings.Builder
	toolCalls := map[int]chatToolCall{}
	var latestUsage *schema.TokenUsage
	for {
		chunk, err := output.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			r.finishModelInputs("failed", err.Error())
			if r.auditRecorder != nil {
				r.auditRecorder.recordModelTransportError(ctx, err)
			}
			return
		}
		modelOutput := einomodel.ConvCallbackOutput(chunk)
		if modelOutput == nil || modelOutput.Message == nil {
			continue
		}
		msg := modelOutput.Message
		if msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
			usage := *msg.ResponseMeta.Usage
			latestUsage = &usage
		}
		if r.auditRecorder != nil && projectEinoAssistantMeaningfulModelChunk(msg) {
			r.auditRecorder.recordModelResponseChunk(ctx, len(msg.ToolCalls) > 0)
		}
		if msg.Content != "" {
			content.WriteString(msg.Content)
		}
		if len(msg.ToolCalls) > 0 {
			r.reportToolPreparation()
			projectEinoMergeToolCalls(toolCalls, msg.ToolCalls)
		}
	}
	r.finishModelInputs("completed", "")
	reply := projectAssistantReply{
		Content:   content.String(),
		ToolCalls: projectEinoSortedToolCalls(toolCalls),
	}
	if r.auditRecorder != nil {
		message := schema.AssistantMessage(reply.Content, nil)
		for _, call := range reply.ToolCalls {
			message.ToolCalls = append(message.ToolCalls, schema.ToolCall{
				ID:   call.ID,
				Type: call.Type,
				Function: schema.FunctionCall{
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				},
			})
		}
		if latestUsage != nil {
			message.ResponseMeta = &schema.ResponseMeta{Usage: latestUsage}
		}
		_ = r.auditRecorder.recordModelResult(ctx, r.runState.CurrentModelCallOrdinal(), message)
	}
	if strings.TrimSpace(reply.Content) == "" && len(reply.ToolCalls) == 0 {
		return
	}
	r.runState.RecordAssistantReply(reply)
}

func projectEinoAssistantMeaningfulModelChunk(msg *schema.Message) bool {
	return msg != nil &&
		(strings.TrimSpace(msg.Content) != "" ||
			strings.TrimSpace(msg.ReasoningContent) != "" ||
			len(msg.ToolCalls) > 0 ||
			len(msg.AssistantGenMultiContent) > 0)
}

func (r *projectEinoAssistantModelCallbackRecorder) recordModelError(ctx context.Context, modelErr error) {
	errText := "model provider request failed"
	if modelErr != nil && strings.TrimSpace(modelErr.Error()) != "" {
		errText = modelErr.Error()
	}
	r.finishModelInputs("failed", errText)
	if r.auditRecorder != nil {
		r.auditRecorder.recordModelTransportError(ctx, modelErr)
		r.auditRecorder.recordModelError()
	}
}

func (r *projectEinoAssistantModelCallbackRecorder) reportToolPreparation() {
	if r.streamCallbacks.OnStatus == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reportedToolPreparation {
		return
	}
	r.reportedToolPreparation = true
	r.streamCallbacks.OnStatus("Preparing action")
}

func projectEinoAssistantReplyFromMessage(msg *schema.Message) projectAssistantReply {
	if msg == nil {
		return projectAssistantReply{}
	}
	return projectAssistantReply{
		Content:   msg.Content,
		ToolCalls: projectEinoToolCallsToChat(msg.ToolCalls),
	}
}

func projectEinoMergeToolCalls(out map[int]chatToolCall, toolCalls []schema.ToolCall) {
	for position, toolCall := range toolCalls {
		index := position
		if toolCall.Index != nil {
			index = *toolCall.Index
		}
		existing := out[index]
		if existing.ID == "" {
			existing.ID = toolCall.ID
		}
		if existing.Type == "" {
			existing.Type = projectEinoToolCallType(toolCall.Type)
		}
		if existing.Function.Name == "" {
			existing.Function.Name = toolCall.Function.Name
		}
		existing.Function.Arguments += toolCall.Function.Arguments
		if len(toolCall.Extra) > 0 {
			if existing.ExtraContent == nil {
				existing.ExtraContent = map[string]any{}
			}
			for key, value := range toolCall.Extra {
				existing.ExtraContent[key] = value
			}
		}
		out[index] = existing
	}
}

func projectEinoSortedToolCalls(toolCalls map[int]chatToolCall) []chatToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	out := make([]chatToolCall, 0, len(toolCalls))
	for _, index := range indices {
		out = append(out, toolCalls[index])
	}
	return out
}
