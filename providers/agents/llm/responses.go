/*
Copyright 2026 The Railgrid Authors.

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

package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	openaischema "github.com/cloudwego/eino/schema/openai"
	"github.com/openai/openai-go/v3/responses"
)

const (
	// ResponsesToolItemIDExtraKey stores the provider's function-call item ID.
	ResponsesToolItemIDExtraKey = "railgrid_openai_responses_item_id"
	// ResponsesToolReasoningExtraKey stores encrypted reasoning continuation data.
	ResponsesToolReasoningExtraKey = "railgrid_openai_responses_reasoning"
	// ResponsesToolOriginExtraKey stores the producing credential fingerprint.
	ResponsesToolOriginExtraKey = "railgrid_openai_responses_origin"
	responsesToolStateKey       = "railgrid_openai_responses_tool_state"
)

// responsesChatModel adapts Eino's native Responses model to Eino's ChatModel
// boundary used by the Agents runtime, middleware, and callbacks.
// Eino owns the HTTP protocol, SSE parsing, and provider item conversion.
// The conversions follow App Studio's assistant_eino_responses adapter; Agents
// additionally binds tools through WithTools and guards credential provenance.
type responsesChatModel struct {
	model      einomodel.AgenticModel
	modelName  string
	origin     string
	boundTools []*schema.ToolInfo
}

var _ einomodel.BaseChatModel = (*responsesChatModel)(nil)
var _ einomodel.ToolCallingChatModel = (*responsesChatModel)(nil)

// NewResponsesModel creates a ChatModel backed by Eino's native OpenAI
// Responses implementation. The native adapter owns request encoding, HTTP,
// SSE parsing, and conversion between Responses items and Eino's agentic
// messages; this wrapper preserves the ChatModel contract used by Agents.
func NewResponsesModel(ctx context.Context, settings Profile) (einomodel.BaseChatModel, error) {
	settings = settings.Normalized()
	store, retries := false, 0
	var reasoning *responses.ReasoningParam
	if effort := strings.TrimSpace(settings.ReasoningEffort); effort != "" {
		reasoning = &responses.ReasoningParam{Effort: responses.ReasoningEffort(effort)}
	}
	model, err := agenticopenai.NewResponsesModel(ctx, &agenticopenai.ResponsesConfig{
		APIKey:     strings.TrimSpace(settings.APIKey),
		BaseURL:    strings.TrimRight(strings.TrimSpace(settings.BaseURL), "/"),
		Model:      strings.TrimSpace(settings.Model),
		HTTPClient: &http.Client{Transport: &responsesTransport{base: http.DefaultTransport}},
		MaxRetries: &retries,
		Store:      &store,
		Include:    []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		Reasoning:  reasoning,
	})
	if err != nil {
		return nil, fmt.Errorf("create native Eino OpenAI Responses model: %w", err)
	}
	originInput := strings.Join([]string{
		strings.TrimRight(strings.TrimSpace(settings.BaseURL), "/"),
		strings.TrimSpace(settings.Model),
		strings.TrimSpace(settings.APIKey),
	}, "\x00")
	origin := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(originInput)))
	return &responsesChatModel{model: model, modelName: strings.TrimSpace(settings.Model), origin: origin}, nil
}

func (*responsesChatModel) GetType() string { return "OpenAIResponses" }

func (*responsesChatModel) IsCallbacksEnabled() bool { return true }

// ResponsesOrigin returns the stable, one-way model credential fingerprint
// used to bind opaque tool continuation metadata to its originating endpoint.
func (m *responsesChatModel) ResponsesOrigin() string { return m.origin }

// WithTools returns a model clone with default tools attached. Per-call
// WithTools options take precedence, matching Eino's normal option semantics.
func (m *responsesChatModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	normalized, err := responsesTools(tools)
	if err != nil {
		return nil, err
	}
	clone := *m
	clone.boundTools = normalized
	return &clone, nil
}

func (m *responsesChatModel) effectiveOptions(opts []einomodel.Option) []einomodel.Option {
	if m.boundTools == nil || einomodel.GetCommonOptions(nil, opts...).Tools != nil {
		return opts
	}
	result := make([]einomodel.Option, 0, len(opts)+1)
	result = append(result, opts...)
	result = append(result, einomodel.WithTools(m.boundTools))
	return result
}

func (m *responsesChatModel) validateInputOrigin(input []*schema.Message) error {
	for _, message := range input {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		calls, err := responsesInputToolCalls(message)
		if err != nil {
			return err
		}
		for _, call := range calls {
			origin, _ := call.Extra[ResponsesToolOriginExtraKey].(string)
			if origin != "" && origin != m.origin {
				return errors.New("OpenAI Responses continuation belongs to a different model credential")
			}
		}
	}
	return nil
}

func (m *responsesChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	opts = m.effectiveOptions(opts)
	ctx, config := m.start(ctx, input, opts)
	if err := m.validateInputOrigin(input); err != nil {
		callbacks.OnError(ctx, err)
		return nil, err
	}
	messages, err := responsesInput(input)
	var nativeOptions []einomodel.Option
	if err == nil {
		nativeOptions, err = responsesOptions(opts)
	}
	if err != nil {
		callbacks.OnError(ctx, err)
		return nil, err
	}
	result, err := m.model.Generate(responsesNativeContext(ctx), messages, nativeOptions...)
	if err == nil {
		err = responsesCompletionError(result, true)
	}
	if err == nil {
		err = responsesValidateContinuation(result)
	}
	if err != nil {
		callbacks.OnError(ctx, err)
		return nil, err
	}
	out := responsesOutput(result)
	responsesCompleteOutput(out, result, false, m.origin)
	callbacks.OnEnd(ctx, responsesCallbackOutput(out, config))
	return out, nil
}

func (m *responsesChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	opts = m.effectiveOptions(opts)
	ctx, config := m.start(ctx, input, opts)
	if err := m.validateInputOrigin(input); err != nil {
		callbacks.OnError(ctx, err)
		return nil, err
	}
	messages, err := responsesInput(input)
	var nativeOptions []einomodel.Option
	if err == nil {
		nativeOptions, err = responsesOptions(opts)
	}
	if err != nil {
		callbacks.OnError(ctx, err)
		return nil, err
	}
	metadata := &responsesStreamMetadata{}
	nativeCtx := context.WithValue(responsesNativeContext(ctx), responsesStreamMetadataKey{}, metadata)
	native, err := m.model.Stream(nativeCtx, messages, nativeOptions...)
	if err != nil {
		err = metadata.wrapError(err)
		callbacks.OnError(ctx, err)
		return nil, err
	}
	var chunks []*schema.AgenticMessage
	callIndices := map[string]int{}
	completed := false
	out := schema.StreamReaderWithConvert(native, func(chunk *schema.AgenticMessage) (*einomodel.CallbackOutput, error) {
		if chunk == nil {
			return nil, schema.ErrNoValue
		}
		if err := responsesCompletionError(chunk, false); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
		for _, block := range chunk.ContentBlocks {
			if block != nil && block.FunctionToolCall != nil && block.FunctionToolCall.CallID != "" && block.StreamingMeta != nil {
				callIndices[block.FunctionToolCall.CallID] = block.StreamingMeta.Index
			}
		}
		message := responsesOutput(chunk)
		if responsesCompleted(chunk) {
			full, err := schema.ConcatAgenticMessages(chunks)
			if err != nil {
				return nil, fmt.Errorf("assemble OpenAI Responses output: %w", err)
			}
			if err := metadata.restore(full); err != nil {
				return nil, err
			}
			// Native concatenation drops StreamingMeta. Restore each call's
			// original index so terminal metadata merges into the same call.
			for _, block := range full.ContentBlocks {
				if block != nil && block.FunctionToolCall != nil {
					if index, ok := callIndices[block.FunctionToolCall.CallID]; ok {
						block.StreamingMeta = &schema.StreamingMeta{Index: index}
					}
				}
			}
			responsesCompleteOutput(message, full, true, m.origin)
			completed = true
			chunks = nil
		}
		if !completed && message.Content == "" && len(message.ToolCalls) == 0 {
			// Keep setup and opaque reasoning chunks in the native assembly above,
			// but do not expose empty assistant messages to ChatModel readers. In
			// particular, FallbackModel must not treat response.created as output.
			return nil, schema.ErrNoValue
		}
		return responsesCallbackOutput(message, config), nil
	}, schema.WithErrWrapper(metadata.wrapError), schema.WithOnEOF(func() (any, error) {
		if !completed {
			return nil, metadata.wrapError(fmt.Errorf("OpenAI Responses stream ended before response.completed: %w", io.ErrUnexpectedEOF))
		}
		return nil, io.EOF
	}))
	_, out = callbacks.OnEndWithStreamOutput(ctx, out)
	return schema.StreamReaderWithConvert(out, func(chunk *einomodel.CallbackOutput) (*schema.Message, error) {
		return chunk.Message, nil
	}), nil
}

func (m *responsesChatModel) start(ctx context.Context, input []*schema.Message, opts []einomodel.Option) (context.Context, *einomodel.Config) {
	options := einomodel.GetCommonOptions(nil, opts...)
	config := &einomodel.Config{Model: m.modelName}
	if options.Model != nil {
		config.Model = *options.Model
	}
	if options.MaxTokens != nil {
		config.MaxTokens = *options.MaxTokens
	}
	ctx = callbacks.EnsureRunInfo(ctx, m.GetType(), components.ComponentOfChatModel)
	return callbacks.OnStart(ctx, &einomodel.CallbackInput{
		Messages: input, Tools: options.Tools, ToolChoice: options.ToolChoice, Config: config,
	}), config
}

func responsesNativeContext(ctx context.Context) context.Context {
	// Run callbacks consume schema.Message. The wrapper emits those callbacks;
	// the native Agentic model must not invoke the same run handlers a second time.
	return callbacks.InitCallbacks(ctx, &callbacks.RunInfo{
		Type: "AgenticOpenAI/Responses", Component: components.ComponentOfAgenticModel,
	})
}

func responsesCallbackOutput(message *schema.Message, config *einomodel.Config) *einomodel.CallbackOutput {
	out := &einomodel.CallbackOutput{Message: message, Config: config}
	if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		usage := message.ResponseMeta.Usage
		out.TokenUsage = &einomodel.TokenUsage{
			PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens,
			PromptTokenDetails:      einomodel.PromptTokenDetails{CachedTokens: usage.PromptTokenDetails.CachedTokens},
			CompletionTokensDetails: einomodel.CompletionTokensDetails{ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens},
		}
	}
	return out
}

func responsesOptions(opts []einomodel.Option) ([]einomodel.Option, error) {
	// Chat-specific options cannot be forwarded: the native Agentic model
	// rejects ToolChoice and expects its typed AgenticToolChoice equivalent.
	common := einomodel.GetCommonOptions(nil, opts...)
	var result []einomodel.Option
	if common.Model != nil {
		result = append(result, einomodel.WithModel(*common.Model))
	}
	if common.Temperature != nil {
		result = append(result, einomodel.WithTemperature(*common.Temperature))
	}
	if common.TopP != nil {
		result = append(result, einomodel.WithTopP(*common.TopP))
	}
	if common.MaxTokens != nil {
		result = append(result, einomodel.WithMaxTokens(*common.MaxTokens))
	}
	if common.Stop != nil {
		result = append(result, einomodel.WithStop(common.Stop))
	}
	if common.Tools != nil {
		tools, err := responsesTools(common.Tools)
		if err != nil {
			return nil, err
		}
		result = append(result, einomodel.WithTools(tools))
	}
	if common.DeferredTools != nil {
		tools, err := responsesTools(common.DeferredTools)
		if err != nil {
			return nil, err
		}
		result = append(result, einomodel.WithDeferredTools(tools))
	}
	if common.ToolSearchTool != nil {
		tools, err := responsesTools([]*schema.ToolInfo{common.ToolSearchTool})
		if err != nil {
			return nil, err
		}
		result = append(result, einomodel.WithToolSearchTool(tools[0]))
	}
	// Responses normalizes function schemas to strict mode unless disabled.
	// Existing Agents tools have optional fields and free-form objects;
	// keep their declared schema semantics through the native SDK's JSON paths.
	if count := len(common.Tools) + len(common.DeferredTools); count > 0 {
		fields := make(map[string]any, count)
		for index := 0; index < count; index++ {
			fields[fmt.Sprintf("tools.%d.strict", index)] = false
		}
		result = append(result, agenticopenai.WithExtraFields(fields))
	}
	choice := common.AgenticToolChoice
	if common.ToolChoice != nil {
		choice = &schema.AgenticToolChoice{Type: *common.ToolChoice}
		if len(common.AllowedToolNames) > 0 {
			tools := make([]*schema.AllowedTool, 0, len(common.AllowedToolNames))
			for _, name := range common.AllowedToolNames {
				tools = append(tools, &schema.AllowedTool{FunctionName: name})
			}
			if *common.ToolChoice == schema.ToolChoiceForced {
				choice.Forced = &schema.AgenticForcedToolChoice{Tools: tools}
			} else {
				choice.Allowed = &schema.AgenticAllowedToolChoice{Tools: tools}
			}
		}
	}
	if choice != nil {
		result = append(result, einomodel.WithAgenticToolChoice(choice))
	}
	return result, nil
}

func responsesTools(tools []*schema.ToolInfo) ([]*schema.ToolInfo, error) {
	normalized := make([]*schema.ToolInfo, len(tools))
	for index, tool := range tools {
		if tool == nil {
			return nil, errors.New("OpenAI Responses tool definition is nil")
		}
		copy := *tool
		if copy.ParamsOneOf == nil {
			copy.ParamsOneOf = schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})
		}
		normalized[index] = &copy
	}
	return normalized, nil
}

func responsesInput(input []*schema.Message) ([]*schema.AgenticMessage, error) {
	messages := make([]*schema.AgenticMessage, 0, len(input))
	for _, message := range input {
		if message == nil {
			continue
		}
		native := &schema.AgenticMessage{Role: schema.AgenticRoleType(message.Role)}
		switch message.Role {
		case schema.Assistant:
			calls, err := responsesInputToolCalls(message)
			if err != nil {
				return nil, err
			}
			for _, call := range calls {
				if encoded, ok := call.Extra[ResponsesToolReasoningExtraKey].(string); ok && encoded != "" {
					var reasoning []responsesOpaqueReasoning
					if err := json.Unmarshal([]byte(encoded), &reasoning); err != nil {
						return nil, fmt.Errorf("restore OpenAI Responses reasoning: %w", err)
					}
					// Eino excludes provider-specific reasoning from messages it
					// cannot identify as OpenAI output. Preserve that origin marker
					// when restoring our opaque Responses continuation metadata.
					native.ResponseMeta = &schema.AgenticResponseMeta{OpenAIExtension: &openaischema.ResponseMetaExtension{}}
					for _, item := range reasoning {
						native.ContentBlocks = append(native.ContentBlocks, &schema.ContentBlock{
							Type:      schema.ContentBlockTypeReasoning,
							Reasoning: &schema.Reasoning{Signature: item.EncryptedContent},
							Extra:     map[string]any{"openai-item-id": item.ID},
						})
					}
				}
			}
			if message.Content != "" {
				native.ContentBlocks = append(native.ContentBlocks, schema.NewContentBlock(&schema.AssistantGenText{Text: message.Content}))
			}
			for _, call := range calls {
				block := schema.NewContentBlock(&schema.FunctionToolCall{CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
				if id, ok := call.Extra[ResponsesToolItemIDExtraKey].(string); ok && id != "" {
					block.Extra = map[string]any{"openai-item-id": id}
				}
				native.ContentBlocks = append(native.ContentBlocks, block)
			}
		case schema.Tool:
			native.Role = schema.AgenticRoleTypeUser
			native.ContentBlocks = []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{
				CallID: message.ToolCallID, Name: message.ToolName,
				Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: message.Content}}},
			})}
		case schema.System, schema.User:
			blocks, err := responsesInputContent(message)
			if err != nil {
				return nil, err
			}
			native.ContentBlocks = blocks
		default:
			return nil, fmt.Errorf("OpenAI Responses does not support message role %q", message.Role)
		}
		messages = append(messages, native)
	}
	return messages, nil
}

// HydrateResponsesToolCalls restores provider continuation metadata from the
// message-level Extra field used by Eino's graph concatenator. It returns a
// copy and does not mutate the message or its tool-call metadata.
func HydrateResponsesToolCalls(message *schema.Message) []schema.ToolCall {
	if message == nil {
		return nil
	}
	calls, err := responsesInputToolCalls(message)
	if err != nil {
		return message.ToolCalls
	}
	return calls
}

// HydrateResponsesToolCalls implements the optional continuation metadata
// interface for callers that only retain a BaseChatModel value.
func (*responsesChatModel) HydrateResponsesToolCalls(message *schema.Message) []schema.ToolCall {
	return HydrateResponsesToolCalls(message)
}

func responsesInputToolCalls(message *schema.Message) ([]schema.ToolCall, error) {
	encoded, _ := message.Extra[responsesToolStateKey].(string)
	if encoded == "" {
		return message.ToolCalls, nil
	}
	var state map[string]map[string]string
	if err := json.Unmarshal([]byte(encoded), &state); err != nil {
		return nil, fmt.Errorf("restore OpenAI Responses tool state: %w", err)
	}
	calls := make([]schema.ToolCall, len(message.ToolCalls))
	copy(calls, message.ToolCalls)
	for index := range calls {
		stored := state[calls[index].ID]
		if len(stored) == 0 {
			continue
		}
		extra := make(map[string]any, len(calls[index].Extra)+len(stored))
		for key, value := range calls[index].Extra {
			extra[key] = value
		}
		for _, key := range []string{ResponsesToolItemIDExtraKey, ResponsesToolReasoningExtraKey, ResponsesToolOriginExtraKey} {
			if value := stored[key]; value != "" {
				extra[key] = value
			}
		}
		calls[index].Extra = extra
	}
	return calls, nil
}

func responsesInputContent(message *schema.Message) ([]*schema.ContentBlock, error) {
	var blocks []*schema.ContentBlock
	if len(message.UserInputMultiContent) > 0 {
		for _, part := range message.UserInputMultiContent {
			switch part.Type {
			case schema.ChatMessagePartTypeText:
				blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: part.Text}))
			case schema.ChatMessagePartTypeImageURL:
				if part.Image == nil {
					return nil, errors.New("OpenAI Responses image input is missing image data")
				}
				image := &schema.UserInputImage{MIMEType: part.Image.MIMEType, Detail: part.Image.Detail}
				if part.Image.URL != nil {
					image.URL = *part.Image.URL
				}
				if part.Image.Base64Data != nil {
					image.Base64Data = *part.Image.Base64Data
				}
				blocks = append(blocks, schema.NewContentBlock(image))
			default:
				return nil, fmt.Errorf("unsupported OpenAI Responses input content type %q", part.Type)
			}
		}
		return blocks, nil
	}
	if len(message.MultiContent) > 0 {
		for _, part := range message.MultiContent {
			switch part.Type {
			case schema.ChatMessagePartTypeText:
				blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: part.Text}))
			case schema.ChatMessagePartTypeImageURL:
				if part.ImageURL == nil {
					return nil, errors.New("OpenAI Responses image input is missing image data")
				}
				blocks = append(blocks, schema.NewContentBlock(&schema.UserInputImage{URL: part.ImageURL.URL, Detail: part.ImageURL.Detail}))
			default:
				return nil, fmt.Errorf("unsupported OpenAI Responses input content type %q", part.Type)
			}
		}
		return blocks, nil
	}
	return []*schema.ContentBlock{schema.NewContentBlock(&schema.UserInputText{Text: message.Content})}, nil
}

func responsesOutput(native *schema.AgenticMessage) *schema.Message {
	message := &schema.Message{Role: schema.Assistant}
	for index, block := range native.ContentBlocks {
		if block == nil {
			continue
		}
		if block.AssistantGenText != nil {
			message.Content += block.AssistantGenText.Text
			if extension := block.AssistantGenText.OpenAIExtension; extension != nil && extension.Refusal != nil {
				message.Content += extension.Refusal.Reason
			}
		}
		if block.FunctionToolCall != nil {
			if block.StreamingMeta != nil {
				index = block.StreamingMeta.Index
			}
			call := block.FunctionToolCall
			message.ToolCalls = append(message.ToolCalls, schema.ToolCall{
				Index: &index, ID: call.CallID, Type: "function",
				Function: schema.FunctionCall{Name: call.Name, Arguments: call.Arguments},
			})
		}
	}
	return message
}

type responsesOpaqueReasoning struct {
	ID               string `json:"id"`
	EncryptedContent string `json:"encrypted_content"`
}

func responsesCompleteOutput(message *schema.Message, native *schema.AgenticMessage, streaming bool, origin string) {
	message.ResponseMeta = &schema.ResponseMeta{FinishReason: "stop", Usage: native.ResponseMeta.TokenUsage}
	var reasoning []responsesOpaqueReasoning
	for _, block := range native.ContentBlocks {
		if block != nil && block.Reasoning != nil && block.Reasoning.Signature != "" {
			reasoning = append(reasoning, responsesOpaqueReasoning{
				ID: responsesItemID(block), EncryptedContent: block.Reasoning.Signature,
			})
		}
	}
	var encodedReasoning string
	if len(reasoning) > 0 {
		encoded, _ := json.Marshal(reasoning)
		encodedReasoning = string(encoded)
	}
	callNumber := 0
	toolState := map[string]map[string]string{}
	for index, block := range native.ContentBlocks {
		if block == nil || block.FunctionToolCall == nil {
			continue
		}
		message.ResponseMeta.FinishReason = "tool_calls"
		extra := map[string]any{}
		if origin != "" {
			extra[ResponsesToolOriginExtraKey] = origin
		}
		if id := responsesItemID(block); id != "" {
			extra[ResponsesToolItemIDExtraKey] = id
		}
		if callNumber == 0 && encodedReasoning != "" {
			extra[ResponsesToolReasoningExtraKey] = encodedReasoning
		}
		if streaming {
			stored := make(map[string]string, len(extra))
			for key, value := range extra {
				stored[key] = value.(string)
			}
			toolState[block.FunctionToolCall.CallID] = stored
			if block.StreamingMeta != nil {
				index = block.StreamingMeta.Index
			}
			// A metadata-only chunk merges into the existing call without repeating
			// the function name or arguments. Opaque continuation metadata is durable.
			message.ToolCalls = append(message.ToolCalls, schema.ToolCall{Index: &index, Extra: extra})
		} else {
			message.ToolCalls[callNumber].Extra = extra
		}
		callNumber++
	}
	if len(toolState) > 0 {
		// Eino's Chat stream concatenator keeps only the first ToolCall.Extra,
		// whereas the runtime's callback recorder merges later call metadata.
		// Carry the same opaque data once at message level for live Eino graph
		// continuation/checkpoints; no plaintext reasoning or native structs.
		encoded, _ := json.Marshal(toolState)
		message.Extra = map[string]any{responsesToolStateKey: string(encoded)}
	}
}

func responsesItemID(block *schema.ContentBlock) string {
	if value := block.Extra["openai-item-id"]; value != nil {
		// Eino uses a private string alias in live messages, plain strings after
		// checkpoint serialization. Both represent the same provider item ID.
		return fmt.Sprint(value)
	}
	return ""
}

func responsesValidateContinuation(message *schema.AgenticMessage) error {
	hasTools := false
	missingReasoning := false
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		hasTools = hasTools || block.FunctionToolCall != nil
		missingReasoning = missingReasoning || (block.Reasoning != nil && block.Reasoning.Signature == "")
	}
	if hasTools && missingReasoning {
		return errors.New("OpenAI Responses omitted encrypted reasoning required to continue tool calls")
	}
	return nil
}

func responsesCompleted(message *schema.AgenticMessage) bool {
	return message != nil && message.ResponseMeta != nil && message.ResponseMeta.OpenAIExtension != nil && message.ResponseMeta.OpenAIExtension.Status == "completed"
}

type responsesFailureError struct {
	Code    string
	Message string
	Err     error
}

func (e *responsesFailureError) Error() string {
	return fmt.Sprintf("OpenAI Responses failed: %s: %s", e.Code, e.Message)
}

func (e *responsesFailureError) Unwrap() error { return e.Err }

func responsesCompletionError(message *schema.AgenticMessage, requireComplete bool) error {
	if message != nil && message.ResponseMeta != nil && message.ResponseMeta.OpenAIExtension != nil {
		meta := message.ResponseMeta.OpenAIExtension
		if meta.Error != nil {
			return &responsesFailureError{Code: string(meta.Error.Code), Message: meta.Error.Message}
		}
		switch meta.Status {
		case "completed":
			return nil
		case "incomplete":
			reason := "unknown reason"
			if meta.IncompleteDetails != nil {
				reason = meta.IncompleteDetails.Reason
			}
			return fmt.Errorf("OpenAI Responses incomplete: %s", reason)
		case "failed", "cancelled":
			return fmt.Errorf("OpenAI Responses %s", meta.Status)
		}
	}
	if requireComplete {
		return errors.New("OpenAI Responses returned without a completed response")
	}
	return nil
}
