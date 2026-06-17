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
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type projectEinoChatModel struct {
	settings  projectLLMSettings
	callbacks projectAssistantStreamCallbacks
	runState  *projectEinoAssistantRunState
}

func newProjectEinoAssistantModelFactory(server *Server) projectEinoAssistantModelFactory {
	return func(_ context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
		if server == nil {
			return nil, errors.New("server is not configured")
		}
		return projectEinoChatModel{
			settings:  req.LLM,
			callbacks: req.StreamCallbacks,
			runState:  runState,
		}, nil
	}
}

func (m projectEinoChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	return m.complete(ctx, input, opts...)
}

func (m projectEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.complete(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m projectEinoChatModel) complete(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	messages, err := projectEinoMessagesToChat(input)
	if err != nil {
		return nil, err
	}
	m.runState.RecordModelInput(messages)

	reqBody, err := m.requestBody(messages, opts...)
	if err != nil {
		return nil, err
	}
	maybeInjectGoogleThoughtSignature(m.settings, reqBody.Messages)
	reply, err := callProjectChatCompletionStream(ctx, m.settings, reqBody, m.callbacks.OnChunk, m.callbacks.OnStatus)
	if err != nil {
		return nil, err
	}
	m.runState.RecordAssistantReply(reply)
	return schema.AssistantMessage(reply.Content, projectChatToolCallsToEino(reply.ToolCalls)), nil
}

func (m projectEinoChatModel) requestBody(messages []chatMessage, opts ...einomodel.Option) (chatCompletionRequest, error) {
	modelName := strings.TrimSpace(m.settings.Model)
	temperature := float64(0.2)
	common := einomodel.GetCommonOptions(nil, opts...)
	if common.Model != nil && strings.TrimSpace(*common.Model) != "" {
		modelName = strings.TrimSpace(*common.Model)
	}
	if common.Temperature != nil {
		temperature = float64(*common.Temperature)
	}
	reqBody := chatCompletionRequest{
		Model:       modelName,
		Messages:    messages,
		Temperature: temperature,
		Stream:      true,
	}
	tools, err := projectEinoToolInfosToChat(common.Tools)
	if err != nil {
		return chatCompletionRequest{}, err
	}
	if len(tools) > 0 {
		reqBody.Tools = tools
		reqBody.ToolChoice = "auto"
	}
	if common.ToolChoice != nil {
		switch *common.ToolChoice {
		case schema.ToolChoiceForbidden:
			reqBody.Tools = nil
			reqBody.ToolChoice = "none"
		case schema.ToolChoiceForced:
			reqBody.ToolChoice = "required"
		case schema.ToolChoiceAllowed:
			if len(reqBody.Tools) > 0 {
				reqBody.ToolChoice = "auto"
			}
		}
	}
	return reqBody, nil
}

func projectEinoToolInfosToChat(infos []*schema.ToolInfo) ([]chatTool, error) {
	out := make([]chatTool, 0, len(infos))
	for _, info := range infos {
		if info == nil || strings.TrimSpace(info.Name) == "" {
			continue
		}
		var params json.RawMessage
		if info.Extra != nil {
			if raw, ok := info.Extra["parametersJSON"].(string); ok && strings.TrimSpace(raw) != "" {
				params = json.RawMessage(raw)
			}
		}
		if len(params) == 0 && info.ParamsOneOf != nil {
			raw, err := json.Marshal(info.ParamsOneOf)
			if err != nil {
				return nil, fmt.Errorf("encode eino tool schema %q: %w", info.Name, err)
			}
			params = raw
		}
		out = append(out, chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name:        strings.TrimSpace(info.Name),
				Description: strings.TrimSpace(info.Desc),
				Parameters:  params,
			},
		})
	}
	return out, nil
}

func projectEinoMessagesToChat(messages []*schema.Message) ([]chatMessage, error) {
	out := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		out = append(out, chatMessage{
			Role:       string(msg.Role),
			Content:    msg.Content,
			Name:       msg.Name,
			ToolCallID: msg.ToolCallID,
			ToolCalls:  projectEinoToolCallsToChat(msg.ToolCalls),
		})
		if len(out) > 0 && msg.Role == schema.Tool && out[len(out)-1].Name == "" {
			out[len(out)-1].Name = msg.ToolName
		}
	}
	return out, nil
}

func projectChatMessagesToEino(messages []chatMessage) ([]*schema.Message, error) {
	out := make([]*schema.Message, 0, len(messages))
	for _, msg := range messages {
		switch schema.RoleType(msg.Role) {
		case schema.System:
			out = append(out, schema.SystemMessage(msg.Content))
		case schema.User:
			out = append(out, schema.UserMessage(msg.Content))
		case schema.Assistant:
			out = append(out, schema.AssistantMessage(msg.Content, projectChatToolCallsToEino(msg.ToolCalls)))
		case schema.Tool:
			out = append(out, schema.ToolMessage(msg.Content, msg.ToolCallID, schema.WithToolName(msg.Name)))
		default:
			return nil, fmt.Errorf("unsupported assistant message role %q", msg.Role)
		}
	}
	return out, nil
}

func projectChatToolCallsToEino(toolCalls []chatToolCall) []schema.ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, 0, len(toolCalls))
	for i, tc := range toolCalls {
		index := i
		extra := map[string]any(nil)
		if len(tc.ExtraContent) > 0 {
			extra = map[string]any{}
			for key, value := range tc.ExtraContent {
				extra[key] = value
			}
		}
		out = append(out, schema.ToolCall{
			Index: &index,
			ID:    tc.ID,
			Type:  tc.Type,
			Function: schema.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
			Extra: extra,
		})
	}
	return out
}

func projectEinoToolCallsToChat(toolCalls []schema.ToolCall) []chatToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	out := make([]chatToolCall, 0, len(toolCalls))
	for _, tc := range toolCalls {
		extra := map[string]any(nil)
		if len(tc.Extra) > 0 {
			extra = map[string]any{}
			for key, value := range tc.Extra {
				extra[key] = value
			}
		}
		toolType := strings.TrimSpace(tc.Type)
		if toolType == "" {
			toolType = "function"
		}
		out = append(out, chatToolCall{
			ID:           tc.ID,
			Type:         toolType,
			ExtraContent: extra,
			Function: chatToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return out
}
