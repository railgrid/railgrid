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
	temperature := float32(0.2)
	model, err := openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
		APIKey:      strings.TrimSpace(settings.APIKey),
		BaseURL:     strings.TrimRight(strings.TrimSpace(settings.BaseURL), "/"),
		Model:       strings.TrimSpace(settings.Model),
		Temperature: &temperature,
		HTTPClient:  &http.Client{},
	})
	if err != nil {
		return nil, fmt.Errorf("create native Eino OpenAI chat model: %w", err)
	}
	return model, nil
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
