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
	"github.com/cloudwego/eino/schema"
)

func projectEinoAssistantRunOptions(req projectAssistantRunRequest, runState *projectEinoAssistantRunState) []adk.AgentRunOption {
	handler := newProjectEinoAssistantModelCallbackHandler(req.StreamCallbacks, runState)
	opts := []adk.AgentRunOption{}
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

func newProjectEinoAssistantModelCallbackHandler(streamCallbacks projectAssistantStreamCallbacks, runState *projectEinoAssistantRunState) callbacks.Handler {
	recorder := &projectEinoAssistantModelCallbackRecorder{
		streamCallbacks: streamCallbacks,
		runState:        runState,
	}
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			recorder.recordModelInput(input)
			return ctx
		}).
		OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			recorder.recordModelOutput(output)
			return ctx
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, _ *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			recorder.recordModelStream(output)
			return ctx
		}).
		Build()
}

type projectEinoAssistantModelCallbackRecorder struct {
	streamCallbacks projectAssistantStreamCallbacks
	runState        *projectEinoAssistantRunState

	mu                      sync.Mutex
	reportedToolPreparation bool
}

func (r *projectEinoAssistantModelCallbackRecorder) recordModelInput(input callbacks.CallbackInput) {
	modelInput := einomodel.ConvCallbackInput(input)
	if modelInput == nil || len(modelInput.Messages) == 0 {
		return
	}
	r.runState.RecordModelInput(projectEinoMessagesToChat(modelInput.Messages))
}

func (r *projectEinoAssistantModelCallbackRecorder) recordModelOutput(output callbacks.CallbackOutput) {
	modelOutput := einomodel.ConvCallbackOutput(output)
	if modelOutput == nil || modelOutput.Message == nil {
		return
	}
	reply := projectEinoAssistantReplyFromMessage(modelOutput.Message)
	if strings.TrimSpace(reply.Content) == "" && len(reply.ToolCalls) == 0 {
		return
	}
	r.runState.RecordAssistantReply(reply)
}

func (r *projectEinoAssistantModelCallbackRecorder) recordModelStream(output *schema.StreamReader[callbacks.CallbackOutput]) {
	if output == nil {
		return
	}
	defer output.Close()

	var content strings.Builder
	toolCalls := map[int]chatToolCall{}
	for {
		chunk, err := output.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return
		}
		modelOutput := einomodel.ConvCallbackOutput(chunk)
		if modelOutput == nil || modelOutput.Message == nil {
			continue
		}
		msg := modelOutput.Message
		if msg.Content != "" {
			content.WriteString(msg.Content)
		}
		if len(msg.ToolCalls) > 0 {
			r.reportToolPreparation()
			projectEinoMergeToolCalls(toolCalls, msg.ToolCalls)
		}
	}
	reply := projectAssistantReply{
		Content:   content.String(),
		ToolCalls: projectEinoSortedToolCalls(toolCalls),
	}
	if strings.TrimSpace(reply.Content) == "" && len(reply.ToolCalls) == 0 {
		return
	}
	r.runState.RecordAssistantReply(reply)
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
