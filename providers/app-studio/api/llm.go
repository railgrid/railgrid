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
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
)

const (
	projectLLMSecretName           = "kedge-projects-llm"
	projectLLMSecretNamespace      = "default"
	defaultProjectLLMProvider      = "openai-compatible"
	defaultProjectLLMBaseURL       = "https://api.openai.com/v1"
	defaultProjectLLMGoogleBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	defaultProjectLLMModel         = "gpt-4o-mini"
	projectLLMProviderGoogle       = "google-ai-studio"
	projectLLMGoogleCloudScope     = "https://www.googleapis.com/auth/cloud-platform"

	// maxAssistantToolTurns bounds how many tool-call/round-trips a single
	// assistant generation may take before the final turn is forced to answer
	// in text (tool_choice=none). It guards against a model that loops on
	// failing or disallowed tool calls.
	maxAssistantToolTurns = 8
)

var (
	errProjectLLMNotConfigured = errors.New("project LLM API key is not configured")
	secretGVR                  = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
)

type ProjectLLMSettingsView struct {
	Provider   string `json:"provider"`
	BaseURL    string `json:"baseURL"`
	Model      string `json:"model"`
	Configured bool   `json:"configured"`
}

type PatchProjectLLMSettingsRequest struct {
	Provider *string `json:"provider,omitempty"`
	BaseURL  *string `json:"baseURL,omitempty"`
	Model    *string `json:"model,omitempty"`
	APIKey   *string `json:"apiKey,omitempty"`
}

type projectLLMSettings struct {
	Provider string
	BaseURL  string
	Model    string
	APIKey   string
}

type googleServiceAccountCredential struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

type chatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	Tools       []chatTool    `json:"tools,omitempty"`
	ToolChoice  string        `json:"tool_choice,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function chatToolCallFunction `json:"function"`
	// For Google Gemini OpenAI compatibility, function-call metadata is required
	// to preserve thought-signature during tool-call turns.
	ExtraContent map[string]any `json:"extra_content,omitempty"`
}

type chatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	} `json:"error,omitempty"`
}

type projectAssistantReply struct {
	Content   string
	ToolCalls []chatToolCall
}

type chatCompletionStreamChoice struct {
	Delta struct {
		Content   string              `json:"content"`
		ToolCalls []chatStreamingCall `json:"tool_calls"`
	} `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

type chatStreamingCall struct {
	Index        int            `json:"index"`
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	ExtraContent map[string]any `json:"extra_content,omitempty"`
	Function     struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatCompletionStreamResponse struct {
	Choices []chatCompletionStreamChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	} `json:"error,omitempty"`
}

type projectMCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func (s *Server) getProjectLLMSettings(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	settings, err := readProjectLLMSettings(r.Context(), c)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings.view())
}

func (s *Server) patchProjectLLMSettings(w http.ResponseWriter, r *http.Request) {
	// The hub used to gate this on the kedge "admin" membership role. The
	// provider acts as the caller, so the workspace Secret's own RBAC is the
	// authority: a non-admin caller's Update is rejected by the apiserver.
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var req PatchProjectLLMSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	settings, err := readProjectLLMSettings(r.Context(), c)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if req.Provider != nil {
		settings.Provider = strings.TrimSpace(*req.Provider)
		if settings.Provider == "" {
			settings.Provider = defaultProjectLLMProvider
		}
	}
	if req.BaseURL != nil {
		baseURL, err := normalizeLLMBaseURL(*req.BaseURL)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		settings.BaseURL = baseURL
	}
	if req.Model != nil {
		settings.Model = strings.TrimSpace(*req.Model)
		if settings.Model == "" {
			writeProjectError(w, newValidationError("model cannot be empty"))
			return
		}
	}
	if req.APIKey != nil {
		settings.APIKey = strings.TrimSpace(*req.APIKey)
	}
	if err := normalizeProjectLLMSettings(&settings); err != nil {
		writeProjectError(w, err)
		return
	}
	if err := writeProjectLLMSettings(r.Context(), c, settings); err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings.view())
}

func (s *Server) generateProjectAssistantStream(
	r *http.Request,
	id identity,
	c *asclient.Client,
	p *aiv1alpha1.Project,
	onChunk func(string),
) (string, error) {
	ctx := r.Context()
	if s.store == nil {
		return "", fmt.Errorf("project message store not configured")
	}
	settings, err := readProjectLLMSettings(ctx, c)
	if err != nil {
		return "", err
	}
	if err := normalizeProjectLLMSettings(&settings); err != nil {
		return "", err
	}
	if strings.TrimSpace(settings.APIKey) == "" {
		return "", errProjectLLMNotConfigured
	}
	if id.orgUUID == "" || id.workspaceUUID == "" {
		return "", errors.New("tenant context missing")
	}
	recent, err := s.store.LoadRecentMessages(ctx, projectMessageScope(id.orgUUID, id.workspaceUUID, p.Name), 24)
	if err != nil {
		return "", err
	}
	messages := projectPromptMessages(p, recent)
	tools, toolsErr := s.loadProjectMCPTools(r, id, settings)
	if toolsErr == nil {
		if toolPrompt := projectMCPToolsPrompt(tools); toolPrompt != "" {
			messages = append(messages, chatMessage{
				Role:    "system",
				Content: toolPrompt,
			})
		}
	} else {
		messages = append(messages, chatMessage{
			Role:    "system",
			Content: projectMCPToolsFailurePrompt(toolsErr),
		})
	}

	logger := klog.FromContext(ctx)

	// seenToolCalls tracks each (name, args) signature the model has already
	// requested. A model that re-issues an identical call is looping rather
	// than making progress, so we stop offering tools and force a text answer
	// instead of grinding through every turn and dying with a generic error.
	seenToolCalls := map[string]int{}
	forceTextAnswer := false

	for i := 0; i < maxAssistantToolTurns; i++ {
		// On the last allowed turn (or once we have detected a loop) set
		// tool_choice=none so the model MUST produce a final text answer. This
		// turns "exceeded safe turn limit" into a graceful explanation of why
		// the assistant could not complete the request.
		finalTurn := forceTextAnswer || i == maxAssistantToolTurns-1

		reqBody := chatCompletionRequest{
			Model:       settings.Model,
			Messages:    messages,
			Temperature: 0.2,
			Stream:      true,
		}
		if len(tools) > 0 {
			reqBody.Tools = tools
			if finalTurn {
				reqBody.ToolChoice = "none"
			} else {
				reqBody.ToolChoice = "auto"
			}
		}
		maybeInjectGoogleThoughtSignature(settings, reqBody.Messages)

		reply, err := callProjectChatCompletionStream(ctx, settings, reqBody, onChunk)
		if err != nil {
			return "", err
		}
		if len(reply.ToolCalls) > 0 && !finalTurn {
			repeated := false
			for _, tc := range reply.ToolCalls {
				sig := tc.Function.Name + "\x00" + tc.Function.Arguments
				seenToolCalls[sig]++
				if seenToolCalls[sig] > 1 {
					repeated = true
				}
				logger.V(2).Info("project assistant requested tool call",
					"turn", i, "tool", tc.Function.Name, "args", tc.Function.Arguments)
			}

			messages = append(messages, chatMessage{
				Role:      aiv1alpha1.ProjectMessageRoleAssistant,
				ToolCalls: reply.ToolCalls,
			})
			nextMessages, callErr := s.resolveProjectToolCalls(ctx, id, reply.ToolCalls, r)
			if callErr != nil {
				return "", callErr
			}
			messages = append(messages, nextMessages...)

			if repeated {
				logger.Info("project assistant repeated an identical tool call across turns; forcing a final text answer",
					"turn", i, "project", p.Name)
				forceTextAnswer = true
			}
			continue
		}

		if strings.TrimSpace(reply.Content) == "" {
			return "", errors.New("LLM API returned an empty assistant message")
		}
		return reply.Content, nil
	}

	return "", errors.New("LLM tool loop exceeded safe turn limit")
}

const googleThoughtSignatureSkipValue = "skip_thought_signature_validator"

func maybeInjectGoogleThoughtSignature(settings projectLLMSettings, messages []chatMessage) {
	if !strings.EqualFold(strings.TrimSpace(settings.Provider), projectLLMProviderGoogle) {
		return
	}
	if len(messages) == 0 {
		return
	}

	latestUserMessageIndex := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == aiv1alpha1.ProjectMessageRoleUser && strings.TrimSpace(messages[i].Content) != "" {
			latestUserMessageIndex = i
			break
		}
	}
	if latestUserMessageIndex < 0 {
		return
	}

	for i := latestUserMessageIndex + 1; i < len(messages); i++ {
		if len(messages[i].ToolCalls) == 0 {
			continue
		}
		ensureGoogleThoughtSignatureInToolCalls(messages[i].ToolCalls)
	}
}

func ensureGoogleThoughtSignatureInToolCalls(toolCalls []chatToolCall) {
	if len(toolCalls) == 0 {
		return
	}
	for i := range toolCalls {
		if hasGoogleThoughtSignature(toolCalls[i]) {
			continue
		}
		setGoogleThoughtSignature(&toolCalls[i], googleThoughtSignatureSkipValue)
	}
}

func hasGoogleThoughtSignature(toolCall chatToolCall) bool {
	extra := toolCall.ExtraContent
	if len(extra) == 0 {
		return false
	}
	google, ok := extra["google"].(map[string]any)
	if !ok || google == nil {
		return false
	}
	rawSig, ok := google["thought_signature"]
	if !ok {
		return false
	}
	sig, ok := rawSig.(string)
	return ok && strings.TrimSpace(sig) != ""
}

func setGoogleThoughtSignature(tc *chatToolCall, signature string) {
	if tc == nil {
		return
	}
	if tc.ExtraContent == nil {
		tc.ExtraContent = map[string]any{}
	}
	google, ok := tc.ExtraContent["google"].(map[string]any)
	if !ok || google == nil {
		google = map[string]any{}
	}
	google["thought_signature"] = signature
	tc.ExtraContent["google"] = google
}

func callProjectChatCompletionStream(
	ctx context.Context,
	settings projectLLMSettings,
	reqBody chatCompletionRequest,
	onChunk func(string),
) (projectAssistantReply, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return projectAssistantReply{}, fmt.Errorf("encoding request: %w", err)
	}

	endpoint := strings.TrimRight(settings.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return projectAssistantReply{}, fmt.Errorf("creating request: %w", err)
	}
	authToken, err := projectLLMAuthToken(ctx, settings)
	if err != nil {
		return projectAssistantReply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return projectAssistantReply{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return projectAssistantReply{}, fmt.Errorf("reading response: %w", readErr)
		}
		var decoded chatCompletionResponse
		if len(body) > 0 {
			_ = json.Unmarshal(body, &decoded)
		}
		detail := strings.TrimSpace(string(body))
		if decoded.Error != nil && decoded.Error.Message != "" {
			detail = decoded.Error.Message
		}
		if detail == "" {
			detail = resp.Status
		}
		// Surfaces upstream LLM rejections (auth, model, request-shape) so a
		// failing chat is debuggable from the provider logs without guessing
		// whether it's the provider or the LLM endpoint.
		klog.FromContext(ctx).Error(nil, "LLM API returned non-2xx",
			"endpoint", endpoint, "model", reqBody.Model, "status", resp.Status, "detail", detail)
		return projectAssistantReply{}, fmt.Errorf("LLM API returned %s: %s", resp.Status, detail)
	}

	var toolCallByIndex = map[int]chatToolCall{}
	var content strings.Builder
	var sawStreamingEvents bool
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "" || raw == "[DONE]" {
			continue
		}
		var chunk chatCompletionStreamResponse
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			return projectAssistantReply{}, fmt.Errorf("decode stream chunk: %w", err)
		}
		if chunk.Error != nil {
			detail := strings.TrimSpace(chunk.Error.Message)
			if detail == "" {
				detail = "unknown stream error"
			}
			return projectAssistantReply{}, fmt.Errorf("LLM API stream error: %s", detail)
		}
		sawStreamingEvents = true
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if onChunk != nil {
					onChunk(choice.Delta.Content)
				}
			}
			for _, toolCall := range choice.Delta.ToolCalls {
				existing := toolCallByIndex[toolCall.Index]
				if existing.ID == "" {
					existing.ID = toolCall.ID
				}
				if existing.Type == "" {
					existing.Type = toolCall.Type
				}
				if existing.Type == "" && toolCall.Type == "" {
					existing.Type = "function"
				}
				if existing.Function.Name == "" && toolCall.Function.Name != "" {
					existing.Function.Name = toolCall.Function.Name
				}
				existing.Function.Arguments += toolCall.Function.Arguments
				if len(toolCall.ExtraContent) > 0 {
					if existing.ExtraContent == nil {
						existing.ExtraContent = map[string]any{}
					}
					for key, value := range toolCall.ExtraContent {
						existing.ExtraContent[key] = value
					}
				}
				toolCallByIndex[toolCall.Index] = existing
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return projectAssistantReply{}, fmt.Errorf("scan stream response: %w", err)
	}
	if !sawStreamingEvents {
		return projectAssistantReply{}, errors.New("LLM API returned no streamed choices")
	}
	if content.Len() == 0 && len(toolCallByIndex) == 0 {
		return projectAssistantReply{}, errors.New("LLM API returned no streamed choices")
	}
	toolCalls := make([]chatToolCall, 0, len(toolCallByIndex))
	indices := make([]int, 0, len(toolCallByIndex))
	for index := range toolCallByIndex {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		toolCalls = append(toolCalls, toolCallByIndex[index])
	}
	return projectAssistantReply{
		Content:   content.String(),
		ToolCalls: toolCalls,
	}, nil
}

func projectLLMAuthToken(ctx context.Context, settings projectLLMSettings) (string, error) {
	apiKey := strings.TrimSpace(settings.APIKey)
	_, usesGoogleServiceAccount, err := googleServiceAccountCredentialFromJSON(apiKey)
	if err != nil {
		return "", err
	}
	if !usesGoogleServiceAccount {
		return apiKey, nil
	}
	jwtConfig, err := google.JWTConfigFromJSON([]byte(apiKey), projectLLMGoogleCloudScope)
	if err != nil {
		return "", fmt.Errorf("loading Google service-account JSON credential: %w", err)
	}
	token, err := jwtConfig.TokenSource(ctx).Token()
	if err != nil {
		return "", fmt.Errorf("minting Google service-account access token: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return "", errors.New("minting Google service-account access token returned an empty token")
	}
	return token.AccessToken, nil
}

func (s *Server) resolveProjectToolCalls(ctx context.Context, id identity, toolCalls []chatToolCall, r *http.Request) ([]chatMessage, error) {
	if len(toolCalls) == 0 {
		return nil, nil
	}

	var toolMessages []chatMessage
	mcpEndpoint := s.mcpEndpoint(id.tenantPath)
	logger := klog.FromContext(ctx)

	for _, tc := range toolCalls {
		if tc.Function.Name == "" {
			logger.Info("project assistant tool call rejected", "reason", "missing function name")
			toolMessages = append(toolMessages, chatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    "Tool call failed: missing function name",
			})
			continue
		}
		if !projectMCPToolAllowed(tc.Function.Name) {
			logger.Info("project assistant tool call rejected", "reason", "disallowed MCP tool name", "tool", tc.Function.Name)
			toolMessages = append(toolMessages, chatMessage{
				Role:       "tool",
				Name:       tc.Function.Name,
				ToolCallID: tc.ID,
				Content:    "Tool call failed: disallowed MCP tool name",
			})
			continue
		}
		if tc.Type == "" {
			tc.Type = "function"
		}
		if tc.Type != "function" {
			logger.Info("project assistant tool call rejected", "reason", "unsupported tool call type", "tool", tc.Function.Name, "type", tc.Type)
			toolMessages = append(toolMessages, chatMessage{
				Role:       "tool",
				Name:       tc.Function.Name,
				ToolCallID: tc.ID,
				Content:    "Tool call failed: " + tc.Type,
			})
			continue
		}
		args := map[string]any{}
		if strings.TrimSpace(tc.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				logger.Info("project assistant tool call rejected", "reason", "invalid arguments", "tool", tc.Function.Name, "args", tc.Function.Arguments, "err", err.Error())
				toolMessages = append(toolMessages, chatMessage{
					Role:       "tool",
					Name:       tc.Function.Name,
					ToolCallID: tc.ID,
					Content:    "Tool call failed: invalid arguments: " + err.Error(),
				})
				continue
			}
		}
		resp, err := callProjectMCPTool(ctx, mcpEndpoint, r, id.tenantPath, s.mcpInsecureSkipTLSVerify, tc.Function.Name, args)
		if err != nil {
			logger.Error(err, "project assistant MCP tool call failed", "tool", tc.Function.Name, "endpoint", mcpEndpoint)
			toolMessages = append(toolMessages, chatMessage{
				Role:       "tool",
				Name:       tc.Function.Name,
				ToolCallID: tc.ID,
				Content:    "Tool call failed: " + err.Error(),
			})
			continue
		}
		logger.V(2).Info("project assistant MCP tool call succeeded", "tool", tc.Function.Name, "responseBytes", len(resp))
		toolMessages = append(toolMessages, chatMessage{
			Role:       "tool",
			Name:       tc.Function.Name,
			ToolCallID: tc.ID,
			Content:    resp,
		})
	}

	return toolMessages, nil
}

func (s *Server) loadProjectMCPTools(r *http.Request, id identity, settings projectLLMSettings) ([]chatTool, error) {
	if id.tenantPath == "" {
		return nil, errors.New("tenant context missing")
	}
	mcpEndpoint := s.mcpEndpoint(id.tenantPath)
	tools, err := fetchProjectMCPTools(r.Context(), mcpEndpoint, r, id.tenantPath, s.mcpInsecureSkipTLSVerify)
	if err != nil {
		return nil, err
	}
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]chatTool, 0, len(tools))
	for _, t := range tools {
		if strings.TrimSpace(t.Name) == "" {
			continue
		}
		if !projectMCPToolAllowed(t.Name) {
			continue
		}
		out = append(out, chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return out, nil
}

// mcpEndpoint returns the hub's unified MCPServer virtual-workspace endpoint for
// the given tenant cluster path. The provider always reaches MCP through the
// hub (KEDGE_HUB_URL), not its own host.
func (s *Server) mcpEndpoint(tenantPath string) string {
	return mcpServerURL(s.hubBase, tenantPath, "default")
}

// mcpServerURL mirrors pkg/apiurl.MCPServerURL in the kedge monorepo:
// {hub}/services/mcpserver/{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp
func mcpServerURL(hubBase, cluster, mcpServerName string) string {
	return strings.TrimRight(hubBase, "/") +
		fmt.Sprintf("/services/mcpserver/%s/apis/kedge.faros.sh/v1alpha1/mcpservers/%s/mcp", cluster, mcpServerName)
}

func fetchProjectMCPTools(ctx context.Context, endpoint string, r *http.Request, tenantPath string, skipTLSVerify bool) ([]projectMCPTool, error) {
	params := []byte(`{}`)
	body, err := projectMCPRequest(ctx, endpoint, "tools/list", params, r, tenantPath, skipTLSVerify)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Tools []projectMCPTool `json:"tools"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode tools/list response: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("provider MCP error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Tools, nil
}

func callProjectMCPTool(ctx context.Context, endpoint string, r *http.Request, tenantPath string, skipTLSVerify bool, name string, args map[string]any) (string, error) {
	params, err := json.Marshal(map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return "", fmt.Errorf("encode tool args: %w", err)
	}
	body, err := projectMCPRequest(ctx, endpoint, "tools/call", params, r, tenantPath, skipTLSVerify)
	if err != nil {
		return "", err
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
		IsError           bool            `json:"isError,omitempty"`
		ErrorMessage      string          `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err == nil {
		if result.IsError {
			if result.ErrorMessage != "" {
				return "", errors.New(result.ErrorMessage)
			}
		}
		textParts := make([]string, 0, len(result.Content))
		for _, item := range result.Content {
			if item.Type == "text" && item.Text != "" {
				textParts = append(textParts, item.Text)
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n"), nil
		}
		if len(result.StructuredContent) > 0 {
			return string(result.StructuredContent), nil
		}
	}
	return string(body), nil
}

func projectMCPRequest(ctx context.Context, endpoint, method string, paramsJSON json.RawMessage, r *http.Request, tenantPath string, skipTLSVerify bool) (json.RawMessage, error) {
	env := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  json.RawMessage(paramsJSON),
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("encode MCP request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("new MCP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if auth := r.Header.Get("Authorization"); strings.TrimSpace(auth) != "" {
		req.Header.Set("Authorization", auth)
	}
	if tenantPath != "" {
		req.Header.Set("X-Kedge-Tenant", tenantPath)
	}

	transport := projectMCPTransport(skipTLSVerify)
	client := &http.Client{Timeout: 15 * time.Second, Transport: transport}
	resp, err := client.Do(req)
	if err != nil && projectMCPShouldRetryInsecure(endpoint, err, skipTLSVerify) {
		transport = projectMCPTransport(true)
		client = &http.Client{Timeout: 15 * time.Second, Transport: transport}
		resp, err = client.Do(req)
	}
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read MCP body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	raw := body
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		parsed, ok := firstSSELine(raw)
		if !ok {
			return nil, errors.New("MCP response had no SSE data")
		}
		raw = parsed
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode MCP JSON-RPC envelope: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("provider MCP error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Result, nil
}

func projectMCPTransport(insecureSkipVerify bool) http.RoundTripper {
	if !insecureSkipVerify {
		return http.DefaultTransport
	}

	if baseTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		clone := baseTransport.Clone()
		clone.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev-only
		return clone
	}

	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // dev-only
}

func projectMCPShouldRetryInsecure(endpoint string, err error, skipTLSVerify bool) bool {
	if skipTLSVerify {
		return false
	}
	if !isLocalhostEndpointForMCP(endpoint) {
		return false
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		var unknownAuthority x509.UnknownAuthorityError
		if errors.As(certErr.Err, &unknownAuthority) {
			return true
		}
	}
	var unknownAuthority *x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var certInvalid *x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return true
	}
	var hostErr *x509.HostnameError
	if errors.As(err, &hostErr) {
		return true
	}
	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "unknown certificate authority") ||
		strings.Contains(errMsg, "certificate verification") ||
		strings.Contains(errMsg, "bad certificate") ||
		strings.Contains(errMsg, "certificate is not valid")
}

func isLocalhostEndpointForMCP(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
}

func firstSSELine(body []byte) (json.RawMessage, bool) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			return json.RawMessage(strings.TrimPrefix(line, "data: ")), true
		}
	}
	return nil, false
}

func readProjectLLMSettings(ctx context.Context, c *asclient.Client) (projectLLMSettings, error) {
	settings := defaultProjectLLMSettings()
	secret, err := c.Dynamic().Resource(secretGVR).Namespace(projectLLMSecretNamespace).Get(ctx, projectLLMSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	if v := secretDataValue(secret, "provider"); v != "" {
		settings.Provider = v
	}
	if v := secretDataValue(secret, "baseURL"); v != "" {
		settings.BaseURL = v
	}
	if v := secretDataValue(secret, "model"); v != "" {
		settings.Model = v
	}
	settings.APIKey = secretDataValue(secret, "apiKey")
	return settings, nil
}

func writeProjectLLMSettings(ctx context.Context, c *asclient.Client, settings projectLLMSettings) error {
	secret := projectLLMSettingsSecret(settings)
	existing, err := c.Dynamic().Resource(secretGVR).Namespace(projectLLMSecretNamespace).Get(ctx, projectLLMSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.Dynamic().Resource(secretGVR).Namespace(projectLLMSecretNamespace).Create(ctx, secret, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	secret.SetResourceVersion(existing.GetResourceVersion())
	_, err = c.Dynamic().Resource(secretGVR).Namespace(projectLLMSecretNamespace).Update(ctx, secret, metav1.UpdateOptions{})
	return err
}

func defaultProjectLLMSettings() projectLLMSettings {
	return projectLLMSettings{
		Provider: defaultProjectLLMProvider,
		BaseURL:  defaultProjectLLMBaseURL,
		Model:    defaultProjectLLMModel,
	}
}

func normalizeProjectLLMSettings(settings *projectLLMSettings) error {
	settings.Provider = strings.TrimSpace(settings.Provider)
	if settings.Provider == "" {
		settings.Provider = defaultProjectLLMProvider
	}
	settings.APIKey = strings.TrimSpace(settings.APIKey)
	settings.BaseURL = strings.TrimSpace(settings.BaseURL)
	if settings.BaseURL == "" {
		settings.BaseURL = defaultProjectLLMBaseURL
	}
	googleCredential, usesGoogleServiceAccount, err := googleServiceAccountCredentialFromJSON(settings.APIKey)
	if err != nil && strings.EqualFold(settings.Provider, projectLLMProviderGoogle) {
		return err
	}
	if strings.EqualFold(settings.Provider, projectLLMProviderGoogle) {
		switch {
		case usesGoogleServiceAccount && isDefaultGoogleBaseURLCandidate(settings.BaseURL):
			settings.BaseURL = defaultProjectLLMGoogleCloudBaseURL(googleCredential.ProjectID)
		case !usesGoogleServiceAccount && isGenericOpenAIBaseURL(settings.BaseURL):
			settings.BaseURL = defaultProjectLLMGoogleBaseURL
		}
	}
	baseURL, err := normalizeLLMBaseURL(settings.BaseURL)
	if err != nil {
		return err
	}
	settings.BaseURL = baseURL
	if err := validateProjectLLMAPIKey(settings.Provider, settings.APIKey); err != nil {
		return err
	}
	if strings.TrimSpace(settings.Model) == "" {
		return newValidationError("model cannot be empty")
	}
	return nil
}

func isGenericOpenAIBaseURL(raw string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(raw), "/"), defaultProjectLLMBaseURL)
}

func isDefaultGoogleBaseURLCandidate(raw string) bool {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	return raw == "" ||
		strings.EqualFold(raw, defaultProjectLLMBaseURL) ||
		strings.EqualFold(raw, defaultProjectLLMGoogleBaseURL)
}

func defaultProjectLLMGoogleCloudBaseURL(projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return defaultProjectLLMGoogleBaseURL
	}
	return "https://aiplatform.googleapis.com/v1/projects/" + url.PathEscape(projectID) + "/locations/global/endpoints/openapi"
}

func validateProjectLLMAPIKey(provider, apiKey string) error {
	if !strings.EqualFold(strings.TrimSpace(provider), projectLLMProviderGoogle) {
		return nil
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	if _, _, err := googleServiceAccountCredentialFromJSON(apiKey); err != nil {
		return err
	}
	if _, ok, _ := googleServiceAccountCredentialFromJSON(apiKey); ok {
		return nil
	}
	if looksLikeJWTOrOAuthToken(apiKey) {
		return newValidationError("Google Gemini settings require a Gemini API key string or service-account JSON credential, not an OAuth/JWT token")
	}
	return nil
}

func googleServiceAccountCredentialFromJSON(raw string) (googleServiceAccountCredential, bool, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		return googleServiceAccountCredential{}, false, nil
	}
	var credential googleServiceAccountCredential
	if err := json.Unmarshal([]byte(raw), &credential); err != nil {
		return googleServiceAccountCredential{}, true, newValidationError("Google service-account JSON credential is not valid JSON")
	}
	if !strings.EqualFold(strings.TrimSpace(credential.Type), "service_account") &&
		strings.TrimSpace(credential.ClientEmail) == "" &&
		strings.TrimSpace(credential.PrivateKey) == "" {
		return googleServiceAccountCredential{}, true, newValidationError("Google credentials must be a Gemini API key string or a service-account JSON credential")
	}
	missing := []string{}
	if strings.TrimSpace(credential.ProjectID) == "" {
		missing = append(missing, "project_id")
	}
	if strings.TrimSpace(credential.ClientEmail) == "" {
		missing = append(missing, "client_email")
	}
	if strings.TrimSpace(credential.PrivateKey) == "" {
		missing = append(missing, "private_key")
	}
	if strings.TrimSpace(credential.TokenURI) == "" {
		missing = append(missing, "token_uri")
	}
	if len(missing) > 0 {
		return googleServiceAccountCredential{}, true, newValidationError("Google service-account JSON credential is missing " + strings.Join(missing, ", "))
	}
	return credential, true, nil
}

func looksLikeJWTOrOAuthToken(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if strings.HasPrefix(raw, "ya29.") {
		return true
	}
	if strings.Count(raw, ".") != 2 {
		return false
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return false
	}
	typ := strings.TrimSpace(fmt.Sprint(header["typ"]))
	_, hasAlg := header["alg"]
	_, hasKeyID := header["kid"]
	return strings.EqualFold(typ, "JWT") || hasAlg || hasKeyID
}

func (s projectLLMSettings) view() ProjectLLMSettingsView {
	return ProjectLLMSettingsView{
		Provider:   s.Provider,
		BaseURL:    s.BaseURL,
		Model:      s.Model,
		Configured: strings.TrimSpace(s.APIKey) != "",
	}
}

func projectLLMSettingsSecret(settings projectLLMSettings) *unstructured.Unstructured {
	data := map[string]interface{}{
		"provider": encodeSecretValue(settings.Provider),
		"baseURL":  encodeSecretValue(settings.BaseURL),
		"model":    encodeSecretValue(settings.Model),
	}
	if strings.TrimSpace(settings.APIKey) != "" {
		data["apiKey"] = encodeSecretValue(settings.APIKey)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{
			"name":      projectLLMSecretName,
			"namespace": projectLLMSecretNamespace,
		},
		"type": "Opaque",
		"data": data,
	}}
}

func secretDataValue(secret *unstructured.Unstructured, key string) string {
	data, _, _ := unstructured.NestedStringMap(secret.Object, "data")
	if encoded := data[key]; encoded != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil {
			return string(decoded)
		}
	}
	stringData, _, _ := unstructured.NestedStringMap(secret.Object, "stringData")
	return stringData[key]
}

func encodeSecretValue(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

func normalizeLLMBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		raw = defaultProjectLLMBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", newValidationError("baseURL must be an absolute HTTP(S) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", newValidationError("baseURL must use http or https")
	}
	u.Path = normalizeLLMBasePath(u.Path, strings.ToLower(u.Host))
	return strings.TrimRight(u.String(), "/"), nil
}

func normalizeLLMBasePath(path string, host string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	if strings.Contains(host, "generativelanguage.googleapis.com") {
		if strings.HasSuffix(strings.ToLower(path), "/chat/completions") {
			path = strings.TrimRight(path[:len(path)-len("/chat/completions")], "/")
		}
		if strings.Contains(strings.ToLower(path), "/v1beta/models/") && strings.Contains(strings.ToLower(path), ":generatecontent") {
			return "/v1beta/openai"
		}
	}
	return path
}

func projectPromptMessages(p *aiv1alpha1.Project, history []store.Message) []chatMessage {
	messages := []chatMessage{{Role: "system", Content: projectSystemPrompt(p)}}
	for _, m := range history {
		if m.Role != aiv1alpha1.ProjectMessageRoleUser && m.Role != aiv1alpha1.ProjectMessageRoleAssistant {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		messages = append(messages, chatMessage{Role: m.Role, Content: content})
	}
	return messages
}

func projectSystemPrompt(p *aiv1alpha1.Project) string {
	var b strings.Builder
	b.WriteString("You are the assistant for a persistent Kedge Project workspace. ")
	b.WriteString("Help the user reason about and build the application represented by this Project. ")
	b.WriteString("Report tool use when you actually call tools, but do not claim that you changed files or deployed resources unless a tool result or other evidence supports it. ")
	b.WriteString("Ask concise follow-up questions when requirements are unclear.\n\n")
	b.WriteString("Project metadata:\n")
	b.WriteString("- Name: " + p.Name + "\n")
	b.WriteString("- Display name: " + p.Spec.DisplayName + "\n")
	if strings.TrimSpace(p.Spec.Description) != "" {
		b.WriteString("- Description: " + p.Spec.Description + "\n")
	}
	b.WriteString("\nProject memory:\n")
	appendMemoryList(&b, "Goals", p.Spec.Memory.Goals)
	appendMemoryList(&b, "Requirements", p.Spec.Memory.Requirements)
	appendMemoryList(&b, "Constraints", p.Spec.Memory.Constraints)
	return b.String()
}

func projectMCPToolsPrompt(tools []chatTool) string {
	if len(tools) == 0 {
		return "No MCP tools were discovered for this workspace."
	}
	var b strings.Builder
	b.WriteString("Available MCP tools in this workspace:\n")
	for _, tool := range tools {
		desc := strings.TrimSpace(tool.Function.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString("- " + tool.Function.Name + ": " + desc + "\n")
	}
	return b.String()
}

func projectMCPToolsFailurePrompt(err error) string {
	if err == nil {
		return ""
	}
	return "MCP tool discovery failed for this workspace: " + err.Error() + ". Tell the user that MCP tools are unavailable in this session."
}

func projectMCPToolAllowed(name string) bool {
	baseName := projectMCPToolBaseName(name)
	if baseName == "" {
		return false
	}
	lower := strings.ToLower(baseName)
	for _, prefix := range []string{"read", "list", "get", "describe"} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		if len(baseName) == len(prefix) {
			return true
		}
		next := baseName[len(prefix)]
		if next == '-' || next == '_' || next == '.' || next == ':' || next == '/' || next == ' ' || (next >= 'A' && next <= 'Z') {
			return true
		}
	}
	return false
}

func projectMCPToolBaseName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	for _, sep := range []string{"__", ":", "/", "."} {
		if idx := strings.LastIndex(name, sep); idx >= 0 && idx+len(sep) < len(name) {
			name = name[idx+len(sep):]
		}
	}
	return strings.TrimSpace(name)
}

func appendMemoryList(b *strings.Builder, label string, items []string) {
	b.WriteString(label + ":\n")
	if len(items) == 0 {
		b.WriteString("- none\n")
		return
	}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			b.WriteString("- " + item + "\n")
		}
	}
}
