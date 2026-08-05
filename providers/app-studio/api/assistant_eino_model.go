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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"cloud.google.com/go/auth/credentials"
	geminimodel "github.com/cloudwego/eino-ext/components/model/gemini"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"google.golang.org/genai"
)

const defaultProjectLLMGoogleCloudLocation = "global"

func newProjectEinoAssistantModelFactory(server *Server) projectEinoAssistantModelFactory {
	return func(ctx context.Context, req projectAssistantRunRequest, _ *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
		if server == nil {
			return nil, errors.New("server is not configured")
		}
		return newProjectEinoChatModel(ctx, req.LLM)
	}
}

type projectEinoAssistantTransientEvidenceModel struct {
	einomodel.BaseChatModel
	runState *projectEinoAssistantRunState
}

func (m *projectEinoAssistantTransientEvidenceModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.Message, error) {
	return m.BaseChatModel.Generate(ctx, m.runState.ExpandTransientToolMessages(input), opts...)
}

func (m *projectEinoAssistantTransientEvidenceModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return m.BaseChatModel.Stream(ctx, m.runState.ExpandTransientToolMessages(input), opts...)
}

// projectEinoAssistantProgressReminderModel adds a queued progress nudge only
// to the input slice handed to the provider model. Eino's agent state,
// callback-recorded conversation, and checkpoint payload all retain the
// original input; the reminder is therefore advisory and ephemeral.
type projectEinoAssistantProgressReminderModel struct {
	einomodel.BaseChatModel
	req      projectAssistantRunRequest
	runState *projectEinoAssistantRunState
}

func (m *projectEinoAssistantProgressReminderModel) reminderInput(input []*schema.Message) []*schema.Message {
	if m == nil || m.runState == nil {
		return input
	}
	available := projectEinoAssistantProgressEnabled(m.req, m.runState)
	reminder, ok := m.runState.TakeProgressReminder(available)
	if !ok {
		return input
	}
	message := schema.SystemMessage(projectEinoAssistantProgressReminderInstruction(reminder))
	withReminder := make([]*schema.Message, 0, len(input)+1)
	withReminder = append(withReminder, input...)
	withReminder = append(withReminder, message)
	return withReminder
}

func (m *projectEinoAssistantProgressReminderModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.Message, error) {
	return m.BaseChatModel.Generate(ctx, m.reminderInput(input), opts...)
}

func (m *projectEinoAssistantProgressReminderModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return m.BaseChatModel.Stream(ctx, m.reminderInput(input), opts...)
}

func newProjectEinoChatModel(ctx context.Context, settings projectLLMSettings) (einomodel.BaseChatModel, error) {
	if err := normalizeProjectLLMSettings(&settings); err != nil {
		return nil, err
	}
	if strings.TrimSpace(settings.APIKey) == "" {
		return nil, errProjectLLMNotConfigured
	}
	switch strings.TrimSpace(settings.Provider) {
	case projectLLMProviderGoogle:
		return newProjectEinoGeminiChatModel(ctx, settings)
	default:
		return newProjectEinoOpenAIChatModel(ctx, settings)
	}
}

func newProjectEinoOpenAIChatModel(ctx context.Context, settings projectLLMSettings) (einomodel.BaseChatModel, error) {
	config := &openaimodel.ChatModelConfig{
		APIKey:     strings.TrimSpace(settings.APIKey),
		BaseURL:    strings.TrimRight(strings.TrimSpace(settings.BaseURL), "/"),
		Model:      strings.TrimSpace(settings.Model),
		HTTPClient: &http.Client{},
	}
	// GPT-5 and the o-series reasoning models reject any temperature other than
	// the fixed default of 1, so only request a custom temperature when the
	// configured model supports it.
	if projectModelSupportsTemperature(settings.Model) {
		temperature := float32(0.2)
		config.Temperature = &temperature
	}
	model, err := openaimodel.NewChatModel(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create native Eino OpenAI chat model: %w", err)
	}
	return &projectEinoAssistantOpenAIPayloadModel{BaseChatModel: model}, nil
}

// projectEinoAssistantOpenAIPayloadModel repairs the serialized chat completion
// request before it leaves the process. The OpenAI wire encoder tags content
// with omitempty, so any message without text — an assistant message that only
// carries tool calls, most commonly — is sent with no content field at all.
// OpenAI itself accepts that; several OpenAI-compatible providers read the
// missing key as null and reject the request with "Invalid value for 'content':
// expected a string, got null".
type projectEinoAssistantOpenAIPayloadModel struct {
	einomodel.BaseChatModel
}

func (m *projectEinoAssistantOpenAIPayloadModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.Message, error) {
	return m.BaseChatModel.Generate(ctx, input, projectEinoAssistantOpenAIPayloadOptions(opts)...)
}

func (m *projectEinoAssistantOpenAIPayloadModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return m.BaseChatModel.Stream(ctx, input, projectEinoAssistantOpenAIPayloadOptions(opts)...)
}

func projectEinoAssistantOpenAIPayloadOptions(opts []einomodel.Option) []einomodel.Option {
	normalized := make([]einomodel.Option, 0, len(opts)+1)
	normalized = append(normalized, opts...)
	return append(normalized, openaimodel.WithRequestPayloadModifier(projectEinoAssistantBackfillMessageContent))
}

// projectEinoAssistantBackfillMessageContent gives every message an explicit
// string content. The payload is rewritten only when a message is missing the
// field or carries an explicit null; anything unparseable is forwarded
// untouched so a malformed body still surfaces the provider's own error.
func projectEinoAssistantBackfillMessageContent(_ context.Context, _ []*schema.Message, rawBody []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return rawBody, nil
	}
	rawMessages, ok := payload["messages"]
	if !ok {
		return rawBody, nil
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return rawBody, nil
	}
	changed := false
	for _, message := range messages {
		content, ok := message["content"]
		if ok && !bytes.Equal(bytes.TrimSpace(content), []byte("null")) {
			continue
		}
		message["content"] = json.RawMessage(`""`)
		changed = true
	}
	if !changed {
		return rawBody, nil
	}
	encodedMessages, err := json.Marshal(messages)
	if err != nil {
		return rawBody, nil
	}
	payload["messages"] = encodedMessages
	encoded, err := json.Marshal(payload)
	if err != nil {
		return rawBody, nil
	}
	return encoded, nil
}

// projectModelSupportsTemperature reports whether the given model accepts a
// custom sampling temperature. OpenAI's GPT-5 family and the o-series reasoning
// models (o1/o3/o4) fix temperature, top_p, and the penalties, and return an
// error if a non-default value is sent.
func projectModelSupportsTemperature(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true
	}
	// Drop any provider prefix such as "openai/".
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	switch {
	case strings.HasPrefix(m, "gpt-5"), strings.HasPrefix(m, "gpt5"):
		return false
	case strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"):
		return false
	}
	return true
}

// projectTemperatureOptions returns the per-call temperature option for the
// given model, or nil when the model does not accept a custom temperature.
func projectTemperatureOptions(model string, temperature float32) []einomodel.Option {
	if !projectModelSupportsTemperature(model) {
		return nil
	}
	return []einomodel.Option{einomodel.WithTemperature(temperature)}
}

func newProjectEinoGeminiChatModel(ctx context.Context, settings projectLLMSettings) (einomodel.BaseChatModel, error) {
	clientConfig, err := projectEinoGeminiClientConfig(settings)
	if err != nil {
		return nil, err
	}
	client, err := genai.NewClient(ctx, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("create Google GenAI client: %w", err)
	}
	temperature := float32(0.2)
	model, err := geminimodel.NewChatModel(ctx, &geminimodel.Config{
		Client:      client,
		Model:       strings.TrimSpace(settings.Model),
		Temperature: &temperature,
	})
	if err != nil {
		return nil, fmt.Errorf("create native Eino Gemini chat model: %w", err)
	}
	return model, nil
}

func projectEinoGeminiClientConfig(settings projectLLMSettings) (*genai.ClientConfig, error) {
	apiKey := strings.TrimSpace(settings.APIKey)
	credential, usesServiceAccount, err := googleServiceAccountCredentialFromJSON(apiKey)
	if err != nil {
		return nil, err
	}
	httpOptions := projectEinoGeminiHTTPOptions(settings.BaseURL)
	if !usesServiceAccount {
		return &genai.ClientConfig{
			APIKey:      apiKey,
			Backend:     genai.BackendGeminiAPI,
			HTTPOptions: httpOptions,
		}, nil
	}
	authCredentials, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes:          []string{projectLLMGoogleCloudScope},
		CredentialsJSON: []byte(apiKey),
	})
	if err != nil {
		return nil, fmt.Errorf("loading Google service-account JSON credential: %w", err)
	}
	return &genai.ClientConfig{
		Backend:     genai.BackendVertexAI,
		Project:     strings.TrimSpace(credential.ProjectID),
		Location:    projectEinoGoogleCloudLocation(settings.BaseURL),
		Credentials: authCredentials,
		HTTPOptions: httpOptions,
	}, nil
}

func projectEinoGeminiHTTPOptions(baseURL string) genai.HTTPOptions {
	nativeBaseURL := projectEinoGeminiNativeBaseURL(baseURL)
	if nativeBaseURL == "" {
		return genai.HTTPOptions{}
	}
	return genai.HTTPOptions{BaseURL: nativeBaseURL}
}

func projectEinoGeminiNativeBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return baseURL
	}
	host := strings.ToLower(u.Host)
	if host == "generativelanguage.googleapis.com" || strings.HasSuffix(host, ".generativelanguage.googleapis.com") {
		return u.Scheme + "://" + u.Host
	}
	if strings.Contains(host, "aiplatform.googleapis.com") {
		return u.Scheme + "://" + u.Host
	}
	return baseURL
}

func projectEinoGoogleCloudLocation(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL != "" {
		if u, err := url.Parse(baseURL); err == nil {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			for i, part := range parts {
				if part == "locations" && i+1 < len(parts) && strings.TrimSpace(parts[i+1]) != "" {
					return strings.TrimSpace(parts[i+1])
				}
			}
			host := strings.ToLower(u.Host)
			if suffix := "-aiplatform.googleapis.com"; strings.HasSuffix(host, suffix) {
				location := strings.TrimSuffix(host, suffix)
				if strings.TrimSpace(location) != "" {
					return strings.TrimSpace(location)
				}
			}
		}
	}
	return defaultProjectLLMGoogleCloudLocation
}

func projectEinoMessagesToChat(messages []*schema.Message) []chatMessage {
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
			Extra:      projectAssistantDurableMessageExtra(msg.Extra),
		})
		if len(out) > 0 && msg.Role == schema.Tool && out[len(out)-1].Name == "" {
			out[len(out)-1].Name = msg.ToolName
		}
	}
	return out
}

func projectChatMessagesToEino(messages []chatMessage) ([]*schema.Message, error) {
	out := make([]*schema.Message, 0, len(messages))
	for _, msg := range messages {
		var converted *schema.Message
		switch schema.RoleType(msg.Role) {
		case schema.System:
			converted = schema.SystemMessage(msg.Content)
		case schema.User:
			converted = schema.UserMessage(msg.Content)
		case schema.Assistant:
			converted = schema.AssistantMessage(msg.Content, projectChatToolCallsToEino(msg.ToolCalls))
		case schema.Tool:
			converted = schema.ToolMessage(msg.Content, msg.ToolCallID, schema.WithToolName(msg.Name))
		default:
			return nil, fmt.Errorf("unsupported assistant message role %q", msg.Role)
		}
		converted.Extra = projectAssistantDurableMessageExtra(msg.Extra)
		out = append(out, converted)
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
			Type:  projectEinoToolCallType(tc.Type),
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
		out = append(out, chatToolCall{
			ID:           tc.ID,
			Type:         projectEinoToolCallType(tc.Type),
			ExtraContent: extra,
			Function: chatToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return out
}

func projectEinoToolCallType(toolType string) string {
	toolType = strings.TrimSpace(toolType)
	if toolType == "" {
		return "function"
	}
	return toolType
}
