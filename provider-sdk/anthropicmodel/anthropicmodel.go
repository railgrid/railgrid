// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package anthropicmodel constructs Eino chat models for Anthropic's native
// Messages API. Opus, Sonnet, and Haiku 5.5 requests opt into Anthropic's
// thinking block binding controls with drop_block: if a saved conversation
// changes its prompt, tools, or earlier messages, Anthropic drops invalid
// signed thinking blocks and continues the request. This is a compatibility
// stopgap; dropped reasoning may need to be regenerated, increasing output
// tokens and latency. See
// https://platform.claude.com/docs/en/build-with-claude/preserved-thinking.
package anthropicmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/railgrid/provider-sdk/internal/claude"
	"github.com/railgrid/provider-sdk/modelcatalog"
)

const (
	// DefaultMaxTokens bounds the native Messages API response when callers do
	// not provide an Eino model.WithMaxTokens option. It leaves room for
	// reasoning output while avoiding an unbounded default.
	DefaultMaxTokens = 16384
	// DefaultMaxTokensForHighEffort leaves room for thinking and response
	// output when callers explicitly select xhigh or max without a token limit.
	DefaultMaxTokensForHighEffort = 65536

	defaultBaseURL              = "https://api.anthropic.com/v1"
	thinkingBindingControlsBeta = "thinking-binding-controls-2026-08-01"

	opus45ThinkingBudgetTokens = 8192
	minimumThinkingBudget      = 1024
	minimumThinkingOutput      = 1024
)

// Config contains the connection settings shared by providers that use
// Anthropic's native Messages API. APIKey is sent as an Authorization bearer
// token. Effort is only accepted for known models at the exact official
// Anthropic endpoint. Unless MaxTokens is set, the output ceiling is
// DefaultMaxTokens, or DefaultMaxTokensForHighEffort for explicit xhigh/max.
type Config struct {
	BaseURL     string
	Model       string
	APIKey      string
	WorkspaceID string
	Effort      string
	// MaxTokens overrides the default output ceiling. An explicit Eino
	// model.WithMaxTokens option on Generate or Stream overrides this value.
	MaxTokens  int
	HTTPClient *http.Client
}

// New creates the native Anthropic Messages API chat model. The returned model
// supports Eino's immutable WithTools interface.
func New(ctx context.Context, cfg Config) (model.ToolCallingChatModel, error) {
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		return nil, errors.New("anthropic model is required")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic API key is required")
	}
	if cfg.MaxTokens < 0 {
		return nil, errors.New("anthropic max tokens must be positive")
	}
	workspaceID := strings.TrimSpace(cfg.WorkspaceID)
	if strings.ContainsAny(workspaceID, "\r\n") {
		return nil, errors.New("anthropic workspace ID must not contain line breaks")
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if !UsesEndpoint(baseURL) {
		return nil, errors.New("native Anthropic chat models require https://api.anthropic.com/v1")
	}
	if err := modelcatalog.ValidateReasoningEffort("openai-compatible", baseURL, modelName, cfg.Effort); err != nil {
		return nil, err
	}
	sdkBaseURL, err := sdkBaseURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Anthropic base URL: %w", err)
	}

	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens(cfg.Effort)
	}
	thinkingMode := nativeThinkingMode(modelName, cfg.Effort)
	if thinkingMode == thinkingExtended && maxTokens < minimumThinkingBudget+minimumThinkingOutput {
		return nil, fmt.Errorf("anthropic max tokens must be at least %d when effort enables Opus 4.5 extended thinking", minimumThinkingBudget+minimumThinkingOutput)
	}

	// The native adapter appends /v1/messages to its configured base URL.
	// Normalize saved OpenAI-compatible URLs ending in /v1 to the API origin
	// so they do not become /v1/v1/messages.
	baseURLForSDK := sdkBaseURL
	httpClient := isolatedHTTPClient(cfg.HTTPClient, workspaceID)
	additionalHeaderFields := map[string]string{}
	additionalRequestFields := map[string]any{}
	if cfg.Effort != "" {
		additionalRequestFields["output_config.effort"] = cfg.Effort
	}
	if supportsThinkingBindingControls(modelName) {
		additionalHeaderFields["anthropic-beta"] = thinkingBindingControlsBeta
		// Haiku and Sonnet 5.5 accept block_binding only with adaptive
		// thinking. Opus 5.5 also uses adaptive thinking by default.
		additionalRequestFields["thinking.type"] = "adaptive"
		additionalRequestFields["thinking.block_binding.prefix_mismatch_behavior"] = "drop_block"
	}
	nativeConfig := &claude.Config{
		AuthToken:  cfg.APIKey,
		BaseURL:    &baseURLForSDK,
		Model:      modelName,
		MaxTokens:  maxTokens,
		HTTPClient: httpClient,
	}
	if len(additionalHeaderFields) != 0 {
		nativeConfig.AdditionalHeaderFields = additionalHeaderFields
	}
	if len(additionalRequestFields) != 0 {
		nativeConfig.AdditionalRequestFields = additionalRequestFields
	}
	switch thinkingMode {
	case thinkingAdaptive:
		nativeConfig.ThinkingConfig = &anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{},
		}
	}

	chatModel, err := claude.NewChatModel(ctx, nativeConfig)
	if err != nil {
		return nil, fmt.Errorf("create native Anthropic chat model: %w", err)
	}
	var toolCallingModel model.ToolCallingChatModel = chatModel
	if thinkingMode == thinkingExtended {
		toolCallingModel = &opus45ThinkingModel{inner: chatModel, defaultMaxTokens: maxTokens}
	}
	toolCallingModel = &streamingGenerateModel{
		inner:            toolCallingModel,
		modelName:        modelName,
		defaultMaxTokens: maxTokens,
	}
	return &signedThinkingReplayModel{inner: toolCallingModel}, nil
}

func defaultMaxTokens(effort string) int {
	if effort == "xhigh" || effort == "max" {
		return DefaultMaxTokensForHighEffort
	}
	return DefaultMaxTokens
}

func supportsThinkingBindingControls(modelName string) bool {
	switch strings.ToLower(strings.TrimSpace(modelName)) {
	case "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5":
		return true
	default:
		return false
	}
}

// streamingGenerateModel adapts large non-streaming Eino calls to the native
// stream path when Anthropic's SDK says Generate would exceed its safe
// non-streaming timeout. It still returns one aggregated schema message.
type streamingGenerateModel struct {
	inner            model.ToolCallingChatModel
	modelName        string
	defaultMaxTokens int
}

func (m *streamingGenerateModel) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	defaultModel := m.modelName
	defaultMaxTokens := m.defaultMaxTokens
	common := model.GetCommonOptions(&model.Options{
		Model:     &defaultModel,
		MaxTokens: &defaultMaxTokens,
	}, opts...)
	effectiveModel := defaultModel
	if common.Model != nil {
		effectiveModel = *common.Model
	}
	effectiveMaxTokens := defaultMaxTokens
	if common.MaxTokens != nil {
		effectiveMaxTokens = *common.MaxTokens
	}
	if _, err := anthropic.CalculateNonStreamingTimeout(effectiveMaxTokens, anthropic.Model(effectiveModel), nil); err == nil {
		return m.inner.Generate(ctx, messages, opts...)
	}

	reader, err := m.inner.Stream(ctx, messages, opts...)
	if err != nil {
		return nil, fmt.Errorf("start Anthropic stream for Generate: %w", err)
	}
	defer reader.Close()

	var chunks []*schema.Message
	for {
		chunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, fmt.Errorf("read Anthropic stream for Generate: %w", recvErr)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		return nil, errors.New("anthropic stream for Generate returned no messages")
	}
	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, fmt.Errorf("combine Anthropic stream for Generate: %w", err)
	}
	return message, nil
}

func (m *streamingGenerateModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.inner.Stream(ctx, messages, opts...)
}

func (m *streamingGenerateModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	withTools, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &streamingGenerateModel{
		inner:            withTools,
		modelName:        m.modelName,
		defaultMaxTokens: m.defaultMaxTokens,
	}, nil
}

var _ model.ToolCallingChatModel = (*streamingGenerateModel)(nil)

type thinkingMode uint8

const (
	thinkingNone thinkingMode = iota
	thinkingAdaptive
	thinkingExtended
)

func nativeThinkingMode(modelName, effort string) thinkingMode {
	if effort == "" {
		return thinkingNone
	}
	// The official API leaves adaptive thinking off by default on these
	// 4.6+ models, so explicit effort must enable it. Claude 5.x models use
	// adaptive thinking by default. Opus 4.5 supports effort only with its
	// older extended-thinking mode, which is configured per call below.
	// See https://platform.claude.com/docs/en/build-with-claude/effort and
	// https://platform.claude.com/docs/en/build-with-claude/extended-thinking.
	switch strings.ToLower(strings.TrimSpace(modelName)) {
	case "claude-opus-4-5", "claude-opus-4-5-20251101":
		return thinkingExtended
	case "claude-opus-4-6", "claude-sonnet-4-6", "claude-opus-4-7", "claude-opus-4-8":
		return thinkingAdaptive
	default:
		return thinkingNone
	}
}

// opus45ThinkingModel keeps the native adapter's wire and stream handling,
// while sizing Opus 4.5's required extended-thinking budget to the effective
// max_tokens value for both config defaults and Eino per-call overrides.
type opus45ThinkingModel struct {
	inner            model.ToolCallingChatModel
	defaultMaxTokens int
}

func (m *opus45ThinkingModel) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	opts, err := m.withThinkingBudget(opts)
	if err != nil {
		return nil, err
	}
	return m.inner.Generate(ctx, messages, opts...)
}

func (m *opus45ThinkingModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	opts, err := m.withThinkingBudget(opts)
	if err != nil {
		return nil, err
	}
	return m.inner.Stream(ctx, messages, opts...)
}

func (m *opus45ThinkingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	withTools, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &opus45ThinkingModel{inner: withTools, defaultMaxTokens: m.defaultMaxTokens}, nil
}

func (m *opus45ThinkingModel) withThinkingBudget(opts []model.Option) ([]model.Option, error) {
	common := model.GetCommonOptions(&model.Options{}, opts...)
	maxTokens := m.defaultMaxTokens
	if common.MaxTokens != nil {
		maxTokens = *common.MaxTokens
	}
	if maxTokens < minimumThinkingBudget+minimumThinkingOutput {
		return nil, fmt.Errorf("anthropic max tokens must be at least %d for Opus 4.5 extended thinking", minimumThinkingBudget+minimumThinkingOutput)
	}

	budgetTokens := maxTokens - minimumThinkingOutput
	if budgetTokens > opus45ThinkingBudgetTokens {
		budgetTokens = opus45ThinkingBudgetTokens
	}
	if budgetTokens < minimumThinkingBudget {
		budgetTokens = minimumThinkingBudget
	}
	thinking := anthropic.ThinkingConfigParamUnion{
		OfEnabled: &anthropic.ThinkingConfigEnabledParam{BudgetTokens: int64(budgetTokens)},
	}
	return append(opts, claude.WithThinkingConfig(&thinking)), nil
}

var _ model.ToolCallingChatModel = (*opus45ThinkingModel)(nil)

const (
	claudeThinkingExtraKey          = "_eino_claude_thinking"
	claudeThinkingSignatureExtraKey = "_eino_claude_thinking_signature"

	maxSignedThinkingReplayBytes = 64 << 20
	maxThinkingSignatureBytes    = 1 << 20
	maxThinkingContentBytes      = 4 << 20
)

type signedThinkingReplayContextKey struct{}

type assistantThinkingReplay struct {
	thinking                 string
	signature                string
	replaceEmptyTextFallback bool
}

type signedThinkingReplayContext struct {
	assistantMessages []assistantThinkingReplay
	assistantGroups   []int
}

// signedThinkingReplayModel adds request context for the one shape the pinned
// native Eino adapter drops: a signed thinking block whose displayed thinking
// string is empty. The transport restores that exact block in the native
// Messages request without changing nonempty signed text.
type signedThinkingReplayModel struct {
	inner model.ToolCallingChatModel
}

func (m *signedThinkingReplayModel) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	ctx, err := withSignedThinkingReplay(ctx, messages)
	if err != nil {
		return nil, err
	}
	return m.inner.Generate(ctx, messages, opts...)
}

func (m *signedThinkingReplayModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	ctx, err := withSignedThinkingReplay(ctx, messages)
	if err != nil {
		return nil, err
	}
	return m.inner.Stream(ctx, messages, opts...)
}

func (m *signedThinkingReplayModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	withTools, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &signedThinkingReplayModel{inner: withTools}, nil
}

var _ model.ToolCallingChatModel = (*signedThinkingReplayModel)(nil)

func withSignedThinkingReplay(ctx context.Context, messages []*schema.Message) (context.Context, error) {
	replay := signedThinkingReplayContext{}
	assistantGroupSize := 0
	hasReplay := false
	for _, message := range messages {
		if message == nil || message.Role != schema.Assistant {
			if assistantGroupSize > 0 {
				replay.assistantGroups = append(replay.assistantGroups, assistantGroupSize)
				assistantGroupSize = 0
			}
			continue
		}

		assistantGroupSize++
		entry := assistantThinkingReplay{}
		if message.Extra != nil {
			signatureValue, hasSignature := message.Extra[claudeThinkingSignatureExtraKey]
			if hasSignature {
				signature, ok := signatureValue.(string)
				if !ok {
					return nil, errors.New("signed Anthropic thinking signature metadata must be a string")
				}
				if signature != "" {
					thinking, hasThinking := message.Extra[claudeThinkingExtraKey].(string)
					if _, exists := message.Extra[claudeThinkingExtraKey]; exists && !hasThinking {
						return nil, errors.New("signed Anthropic thinking content metadata must be a string")
					}
					if !hasThinking {
						thinking = message.ReasoningContent
					}
					if !hasThinking || thinking == "" {
						if len(signature) > maxThinkingSignatureBytes {
							return nil, fmt.Errorf("anthropic thinking signature exceeds %d bytes", maxThinkingSignatureBytes)
						}
						if len(thinking) > maxThinkingContentBytes {
							return nil, fmt.Errorf("anthropic thinking content exceeds %d bytes", maxThinkingContentBytes)
						}
						entry.thinking = thinking
						entry.signature = signature
						entry.replaceEmptyTextFallback = thinking == "" && hasNoAssistantPayload(message)
						hasReplay = true
					}
				}
			}
		}
		replay.assistantMessages = append(replay.assistantMessages, entry)
	}
	if assistantGroupSize > 0 {
		replay.assistantGroups = append(replay.assistantGroups, assistantGroupSize)
	}
	if !hasReplay {
		return ctx, nil
	}
	return context.WithValue(ctx, signedThinkingReplayContextKey{}, replay), nil
}

func hasNoAssistantPayload(message *schema.Message) bool {
	return message.Content == "" &&
		len(message.MultiContent) == 0 &&
		len(message.AssistantGenMultiContent) == 0 &&
		len(message.UserInputMultiContent) == 0 &&
		len(message.ToolCalls) == 0
}

type signedThinkingReplayTransport struct {
	base http.RoundTripper
}

func (t signedThinkingReplayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	replay, ok := req.Context().Value(signedThinkingReplayContextKey{}).(signedThinkingReplayContext)
	if !ok || req.Method != http.MethodPost || req.URL == nil ||
		!isOfficialAnthropicOrigin(req.URL) || req.URL.EscapedPath() != "/v1/messages" {
		return t.base.RoundTrip(req)
	}
	if req.Body == nil {
		return nil, errors.New("cannot restore signed Anthropic thinking without a request body")
	}
	if req.ContentLength > maxSignedThinkingReplayBytes {
		return nil, fmt.Errorf("anthropic request body exceeds %d bytes while restoring signed thinking", maxSignedThinkingReplayBytes)
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxSignedThinkingReplayBytes+1))
	_ = req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read Anthropic request body for signed thinking replay: %w", err)
	}
	if len(body) > maxSignedThinkingReplayBytes {
		return nil, fmt.Errorf("anthropic request body exceeds %d bytes while restoring signed thinking", maxSignedThinkingReplayBytes)
	}
	restored, changed, err := restoreSignedThinking(body, replay)
	if err != nil {
		return nil, err
	}
	if changed {
		body = restored
	}
	setRequestBody(req, body)
	return t.base.RoundTrip(req)
}

func restoreSignedThinking(body []byte, replay signedThinkingReplayContext) ([]byte, bool, error) {
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, false, fmt.Errorf("decode Anthropic request for signed thinking replay: %w", err)
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(request["messages"], &messages); err != nil {
		return nil, false, fmt.Errorf("decode Anthropic messages for signed thinking replay: %w", err)
	}

	type assistantRequest struct {
		index   int
		message map[string]json.RawMessage
	}
	var assistants []assistantRequest
	for index, rawMessage := range messages {
		var message map[string]json.RawMessage
		if err := json.Unmarshal(rawMessage, &message); err != nil {
			return nil, false, fmt.Errorf("decode Anthropic message %d for signed thinking replay: %w", index, err)
		}
		var role string
		if err := json.Unmarshal(message["role"], &role); err != nil {
			return nil, false, fmt.Errorf("decode role for Anthropic message %d during signed thinking replay: %w", index, err)
		}
		if role == "assistant" {
			assistants = append(assistants, assistantRequest{index: index, message: message})
		}
	}

	var changed bool
	switch {
	case len(assistants) == len(replay.assistantMessages):
		for assistantIndex, apiMessage := range assistants {
			updated, didChange, err := restoreThinkingInAssistant(apiMessage.message, replay.assistantMessages[assistantIndex])
			if err != nil {
				return nil, false, fmt.Errorf("restore signed thinking in assistant message %d: %w", apiMessage.index, err)
			}
			if didChange {
				raw, err := json.Marshal(updated)
				if err != nil {
					return nil, false, fmt.Errorf("encode Anthropic assistant message %d after signed thinking replay: %w", apiMessage.index, err)
				}
				messages[apiMessage.index] = raw
				changed = true
			}
		}
	case len(assistants) == len(replay.assistantGroups):
		assistantIndex := 0
		messageIndex := 0
		for _, groupSize := range replay.assistantGroups {
			if groupSize > 1 {
				for i := 0; i < groupSize; i++ {
					if replay.assistantMessages[messageIndex+i].signature != "" {
						return nil, false, errors.New("cannot safely restore signed thinking across merged adjacent assistant messages")
					}
				}
			} else {
				apiMessage := assistants[assistantIndex]
				updated, didChange, err := restoreThinkingInAssistant(apiMessage.message, replay.assistantMessages[messageIndex])
				if err != nil {
					return nil, false, fmt.Errorf("restore signed thinking in assistant message %d: %w", apiMessage.index, err)
				}
				if didChange {
					raw, err := json.Marshal(updated)
					if err != nil {
						return nil, false, fmt.Errorf("encode Anthropic assistant message %d after signed thinking replay: %w", apiMessage.index, err)
					}
					messages[apiMessage.index] = raw
					changed = true
				}
			}
			assistantIndex++
			messageIndex += groupSize
		}
	default:
		return nil, false, fmt.Errorf(
			"cannot safely restore signed thinking: request has %d assistant messages, history has %d messages in %d adjacent-assistant groups",
			len(assistants), len(replay.assistantMessages), len(replay.assistantGroups),
		)
	}
	if !changed {
		return body, false, nil
	}
	encodedMessages, err := json.Marshal(messages)
	if err != nil {
		return nil, false, fmt.Errorf("encode Anthropic messages after signed thinking replay: %w", err)
	}
	request["messages"] = encodedMessages
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		return nil, false, fmt.Errorf("encode Anthropic request after signed thinking replay: %w", err)
	}
	if len(encodedRequest) > maxSignedThinkingReplayBytes {
		return nil, false, fmt.Errorf("anthropic request body exceeds %d bytes after signed thinking replay", maxSignedThinkingReplayBytes)
	}
	return encodedRequest, true, nil
}

func restoreThinkingInAssistant(message map[string]json.RawMessage, replay assistantThinkingReplay) (map[string]json.RawMessage, bool, error) {
	if replay.signature == "" {
		return message, false, nil
	}
	var content []json.RawMessage
	if err := json.Unmarshal(message["content"], &content); err != nil {
		return nil, false, fmt.Errorf("decode content: %w", err)
	}
	found := false
	for _, rawBlock := range content {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(rawBlock, &block); err != nil {
			return nil, false, fmt.Errorf("decode content block: %w", err)
		}
		var blockType, signature string
		if err := json.Unmarshal(block["type"], &blockType); err != nil {
			return nil, false, fmt.Errorf("decode content block type: %w", err)
		}
		if blockType != "thinking" {
			continue
		}
		if err := json.Unmarshal(block["signature"], &signature); err != nil {
			return nil, false, fmt.Errorf("decode existing thinking signature: %w", err)
		}
		var thinking string
		if err := json.Unmarshal(block["thinking"], &thinking); err != nil {
			return nil, false, fmt.Errorf("decode existing thinking content: %w", err)
		}
		if signature != replay.signature || thinking != replay.thinking {
			return nil, false, errors.New("existing thinking block does not match signed history")
		}
		if found {
			return nil, false, errors.New("signed thinking appears more than once in the request")
		}
		found = true
	}
	if found {
		return message, false, nil
	}

	if replay.replaceEmptyTextFallback && len(content) == 1 && isEmptyTextBlock(content[0]) {
		content = nil
	}
	block, err := json.Marshal(map[string]any{
		"type":      "thinking",
		"thinking":  replay.thinking,
		"signature": replay.signature,
	})
	if err != nil {
		return nil, false, fmt.Errorf("encode signed thinking block: %w", err)
	}
	content = append([]json.RawMessage{block}, content...)
	encodedContent, err := json.Marshal(content)
	if err != nil {
		return nil, false, fmt.Errorf("encode assistant content after signed thinking replay: %w", err)
	}
	message["content"] = encodedContent
	return message, true, nil
}

func isEmptyTextBlock(raw json.RawMessage) bool {
	var block map[string]json.RawMessage
	if err := json.Unmarshal(raw, &block); err != nil || len(block) != 2 {
		return false
	}
	var blockType, text string
	if err := json.Unmarshal(block["type"], &blockType); err != nil || blockType != "text" {
		return false
	}
	return json.Unmarshal(block["text"], &text) == nil && text == ""
}

func setRequestBody(req *http.Request, body []byte) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	bodyCopy := append([]byte(nil), body...)
	req.Body = io.NopCloser(bytes.NewReader(bodyCopy))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyCopy)), nil
	}
	req.ContentLength = int64(len(bodyCopy))
	if req.Header.Get("Content-Length") != "" {
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(bodyCopy)))
	}
}

// UsesEndpoint reports whether baseURL names Anthropic's official Messages API
// endpoint. The endpoint must be exactly https://api.anthropic.com/v1, with an
// optional trailing slash or explicit default HTTPS port.
func UsesEndpoint(baseURL string) bool {
	raw := strings.TrimSpace(baseURL)
	if raw == "" || strings.ContainsAny(raw, "?#") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Opaque != "" {
		return false
	}
	if !isOfficialAnthropicOrigin(u) {
		return false
	}
	return u.EscapedPath() == "/v1" || u.EscapedPath() == "/v1/"
}

func isOfficialAnthropicOrigin(u *url.URL) bool {
	if u == nil || !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	return strings.EqualFold(u.Host, "api.anthropic.com") ||
		strings.EqualFold(u.Host, "api.anthropic.com:443")
}

// sdkBaseURL converts a Messages API URL into the root that the native SDK
// expects before it appends v1/messages. An explicit value is always supplied
// to prevent ANTHROPIC_BASE_URL from changing where credentials are sent.
func sdkBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("expected an absolute URL without credentials, query, or fragment")
	}

	path := strings.TrimRight(u.Path, "/")
	if path == "/v1" {
		u.Path = ""
		u.RawPath = ""
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func isolatedHTTPClient(source *http.Client, workspaceID string) *http.Client {
	var client http.Client
	if source != nil {
		client = *source
	}

	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	base = signedThinkingReplayTransport{base: base}
	client.Transport = workspaceTransport{workspaceID: strings.TrimSpace(workspaceID), base: base}

	previousCheckRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && !sameOrigin(via[len(via)-1].URL, req.URL) {
			return http.ErrUseLastResponse
		}
		if previousCheckRedirect != nil {
			return previousCheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &client
}

func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil &&
		strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Host, b.Host)
}

type workspaceTransport struct {
	workspaceID string
	base        http.RoundTripper
}

func (t workspaceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.workspaceID != "" && isOfficialAnthropicOrigin(req.URL) {
		req = req.Clone(req.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("anthropic-workspace-id", t.workspaceID)
	}
	return t.base.RoundTrip(req)
}
