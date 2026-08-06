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
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

const (
	projectEinoAssistantModelContextTokensEnv     = "APP_STUDIO_ASSISTANT_MODEL_CONTEXT_TOKENS"
	projectEinoAssistantDefaultModelContextTokens = 128000
	projectEinoAssistantCompactionReservePercent  = 25
)

func projectEinoAssistantCompactionContextTokens() int {
	raw := strings.TrimSpace(os.Getenv(projectEinoAssistantModelContextTokensEnv))
	if raw == "" {
		return projectEinoAssistantDefaultModelContextTokens
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return projectEinoAssistantDefaultModelContextTokens
	}
	return value
}

// The checkpoint prompt and summary preamble below are adapted from OpenAI
// Codex commit 003ec63bba93cf994246799f795a6a4f6d67743a, specifically
// codex-rs/prompts/templates/compact/{prompt,summary_prefix}.md. Copyright
// OpenAI; used under the Apache License, Version 2.0.
const projectEinoAssistantCompactionPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.`

const projectEinoAssistantCompactionSummaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"

func projectEinoAssistantCompactionMiddleware(
	ctx context.Context,
	chatModel einomodel.BaseChatModel,
	server *Server,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) (adk.ChatModelAgentMiddleware, error) {
	var latestCheckpoint *projectAssistantConversationCompactionCheckpoint
	if server != nil {
		if server.store == nil {
			return nil, fmt.Errorf("assistant conversation checkpoint store is not configured")
		}
		projection, err := loadProjectAssistantConversationProjection(ctx, server.store, req.MessageScope)
		if err != nil {
			return nil, fmt.Errorf("load assistant conversation compaction checkpoint: %w", err)
		}
		latestCheckpoint = projection.compactionCheckpoint
	}
	runtime := newProjectEinoAssistantCompactionRuntime(req.auditRecorder, latestCheckpoint)
	base, err := summarization.New(ctx, &summarization.Config{
		Model: &projectEinoAssistantCompactionIsolatedModel{
			BaseChatModel:    chatModel,
			forbidToolChoice: projectEinoAssistantCompactionSupportsForbiddenToolChoice(req.LLM),
		},
		ModelOptions: []einomodel.Option{einomodel.WithMaxTokens(4096)},
		Retry:        projectEinoAssistantCompactionRetryConfig(req),
		Trigger: &summarization.TriggerCondition{
			// Codex rolls context from active model-window pressure rather than an
			// independent message-count ceiling.
			ContextTokens: projectEinoAssistantCompactionContextTokens(),
		},
		UserInstruction: projectEinoAssistantCompactionPrompt,
		// Codex local compaction sends the exact model-visible history followed by
		// one synthesized user checkpoint prompt. Do not inject Eino's generic
		// summarizer system instruction.
		GenModelInput: func(
			modelCtx context.Context,
			systemInstruction *schema.Message,
			userInstruction *schema.Message,
			original []*schema.Message,
		) ([]*schema.Message, error) {
			conversationTail := int64(0)
			if server != nil && server.store != nil {
				projection, err := loadProjectAssistantConversationProjection(modelCtx, server.store, req.MessageScope)
				if err != nil {
					return nil, fmt.Errorf("load assistant conversation tail before compaction: %w", err)
				}
				conversationTail = projection.lastSequence
			}
			if err := runtime.begin(modelCtx, projectEinoAssistantMessagesTokenEstimate(original), conversationTail); err != nil {
				return nil, err
			}
			return projectEinoAssistantCompactionModelInput(modelCtx, systemInstruction, userInstruction, original)
		},
		Finalize: func(finalizeCtx context.Context, original []*schema.Message, summary *schema.Message) ([]*schema.Message, error) {
			toolContinuation := projectEinoAssistantLastNonSystemMessageIsTool(original)
			ignoredToolCallCount := 0
			if summary != nil {
				ignoredToolCallCount = len(summary.ToolCalls)
			}
			canonicalContext, err := projectEinoAssistantCanonicalCompactionContext(finalizeCtx, req, runState)
			if err != nil {
				return nil, runtime.fail(finalizeCtx, err)
			}
			finalized, err := projectEinoAssistantFinalizeCompaction(
				canonicalContext,
				original,
				summary,
				runtime.toolSchemaTokens(),
			)
			if err != nil {
				return nil, runtime.fail(finalizeCtx, err)
			}

			summaryText := projectEinoAssistantSummaryText(summary)
			attempt, ok := runtime.activeAttempt()
			if !ok {
				err := fmt.Errorf("compaction attempt metadata is missing")
				return nil, runtime.fail(finalizeCtx, err)
			}
			checkpoint := projectAssistantConversationCompactionCheckpoint{
				Version:                         projectAssistantConversationCheckpointV1,
				ReplacementHistory:              projectEinoMessagesToChat(finalized),
				Summary:                         summaryText,
				TriggerID:                       attempt.triggerID,
				WindowNumber:                    attempt.windowNumber,
				FirstWindowID:                   attempt.firstWindowID,
				PreviousWindowID:                attempt.previousWindowID,
				WindowID:                        attempt.windowID,
				PriorHistoryTokenEstimate:       attempt.priorTokenEstimate,
				ReplacementHistoryTokenEstimate: projectEinoAssistantMessagesTokenEstimate(finalized),
				CompactedThroughSequence:        attempt.compactedThroughSequence,
			}
			if server != nil && server.store != nil {
				if err := appendProjectAssistantConversationCompactionCheckpoint(
					finalizeCtx,
					server.store,
					req.MessageScope,
					projectAssistantRunID(req),
					attempt.id,
					checkpoint,
				); err != nil {
					return nil, runtime.fail(finalizeCtx, fmt.Errorf("persist assistant conversation compaction: %w", err))
				}
			}
			if err := runState.RolloutBudget().RearmAfterCompaction(finalizeCtx); err != nil {
				return nil, runtime.fail(finalizeCtx, fmt.Errorf("rearm rollout budget after compaction: %w", err))
			}
			if toolContinuation {
				runState.DeferSteeringOnceAfterCompaction()
			}
			if err := runtime.complete(finalizeCtx, checkpoint, ignoredToolCallCount); err != nil {
				return nil, err
			}
			return finalized, nil
		},
	})
	if err != nil {
		return nil, err
	}
	return &projectEinoAssistantCompactionMiddlewareRuntime{
		ChatModelAgentMiddleware: base,
		runtime:                  runtime,
	}, nil
}

// Summary sampling is an internal checkpoint operation, not an ordinary agent
// response item. Replace the inherited agent callback set so a failed
// compaction cannot mutate mirrored model history or main-call audit entries.
type projectEinoAssistantCompactionIsolatedModel struct {
	einomodel.BaseChatModel
	forbidToolChoice bool
}

func (m *projectEinoAssistantCompactionIsolatedModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...einomodel.Option,
) (*schema.Message, error) {
	ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{
		Name:      "app-studio-compaction",
		Type:      "AppStudioCompaction",
		Component: components.ComponentOfChatModel,
	})
	// Codex local compaction exposes no executable tools. Make that boundary
	// explicit for OpenAI-compatible providers: exact history still contains
	// earlier tool calls, and DeepSeek may otherwise continue that pattern even
	// when no tool schemas were supplied by the summarization middleware. Append
	// these options last so inherited or future callers cannot re-enable tools.
	opts = append(opts,
		einomodel.WithTools(nil),
		einomodel.WithDeferredTools(nil),
		einomodel.WithToolSearchTool(nil),
	)
	// DeepSeek V4's direct thinking-mode API rejects the tool_choice field. The
	// OpenCode Zen endpoint used by App Studio accepts it, so keep the stronger
	// explicit prohibition there while omitting the incompatible field only for
	// direct DeepSeek V4 requests. Empty tool sets remain mandatory in both cases.
	if m.forbidToolChoice {
		opts = append(opts, einomodel.WithToolChoice(schema.ToolChoiceForbidden))
	}
	return m.BaseChatModel.Generate(ctx, input, opts...)
}

func projectEinoAssistantCompactionSupportsForbiddenToolChoice(settings projectLLMSettings) bool {
	model := strings.ToLower(strings.TrimSpace(settings.Model))
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		model = model[idx+1:]
	}
	if !strings.HasPrefix(model, "deepseek-v4") {
		return true
	}
	baseURL := strings.ToLower(strings.TrimSpace(settings.BaseURL))
	return !strings.Contains(baseURL, "api.deepseek.com")
}

type projectEinoAssistantCompactionMiddlewareRuntime struct {
	adk.ChatModelAgentMiddleware
	runtime *projectEinoAssistantCompactionRuntime
}

func (m *projectEinoAssistantCompactionMiddlewareRuntime) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelContext *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if m != nil && m.runtime != nil && state != nil {
		m.runtime.setToolSchemaTokens(projectEinoAssistantToolSchemasTokenEstimate(state.ToolInfos))
	}
	nextCtx, nextState, err := m.ChatModelAgentMiddleware.BeforeModelRewriteState(ctx, state, modelContext)
	if err != nil && m != nil && m.runtime != nil {
		return nextCtx, nextState, m.runtime.fail(ctx, err)
	}
	return nextCtx, nextState, err
}

type projectEinoAssistantCompactionAttempt struct {
	id                       string
	triggerID                string
	windowNumber             uint64
	firstWindowID            string
	previousWindowID         string
	windowID                 string
	priorTokenEstimate       int
	compactedThroughSequence int64
}

type projectEinoAssistantCompactionRuntime struct {
	mu                      sync.Mutex
	auditRecorder           *projectAssistantRunAuditRecorder
	active                  *projectEinoAssistantCompactionAttempt
	windowNumber            uint64
	firstWindowID           string
	currentWindowID         string
	toolSchemaTokenEstimate int
}

func newProjectEinoAssistantCompactionRuntime(
	auditRecorder *projectAssistantRunAuditRecorder,
	checkpoint *projectAssistantConversationCompactionCheckpoint,
) *projectEinoAssistantCompactionRuntime {
	firstWindowID := "window-" + uuid.NewString()
	runtime := &projectEinoAssistantCompactionRuntime{
		auditRecorder:   auditRecorder,
		firstWindowID:   firstWindowID,
		currentWindowID: firstWindowID,
	}
	if checkpoint != nil {
		runtime.windowNumber = checkpoint.WindowNumber
		runtime.firstWindowID = checkpoint.FirstWindowID
		runtime.currentWindowID = checkpoint.WindowID
	}
	return runtime
}

func (r *projectEinoAssistantCompactionRuntime) begin(ctx context.Context, priorTokenEstimate int, conversationTail ...int64) error {
	if r == nil {
		return fmt.Errorf("compaction runtime is not configured")
	}
	r.mu.Lock()
	r.windowNumber++
	id := "compaction-" + uuid.NewString()
	attempt := &projectEinoAssistantCompactionAttempt{
		id:                 id,
		triggerID:          id,
		windowNumber:       r.windowNumber,
		firstWindowID:      r.firstWindowID,
		previousWindowID:   r.currentWindowID,
		windowID:           "window-" + uuid.NewString(),
		priorTokenEstimate: priorTokenEstimate,
	}
	if len(conversationTail) > 0 {
		attempt.compactedThroughSequence = max(conversationTail[0], 0)
	}
	r.active = attempt
	r.mu.Unlock()
	if r.auditRecorder == nil {
		return nil
	}
	return r.auditRecorder.recordCompaction(ctx, projectAssistantAuditCompaction{
		ID:                 attempt.id,
		Trigger:            "context_threshold",
		Status:             "started",
		WindowNumber:       attempt.windowNumber,
		PreviousWindowID:   attempt.previousWindowID,
		WindowID:           attempt.windowID,
		PriorTokenEstimate: attempt.priorTokenEstimate,
	})
}

func (r *projectEinoAssistantCompactionRuntime) activeAttempt() (projectEinoAssistantCompactionAttempt, bool) {
	if r == nil {
		return projectEinoAssistantCompactionAttempt{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		return projectEinoAssistantCompactionAttempt{}, false
	}
	return *r.active, true
}

func (r *projectEinoAssistantCompactionRuntime) complete(
	ctx context.Context,
	checkpoint projectAssistantConversationCompactionCheckpoint,
	ignoredToolCallCount int,
) error {
	attempt, ok := r.activeAttempt()
	if !ok {
		return fmt.Errorf("compaction attempt metadata is missing")
	}
	if r.auditRecorder != nil {
		if err := r.auditRecorder.recordCompaction(ctx, projectAssistantAuditCompaction{
			ID:                       attempt.id,
			Trigger:                  "context_threshold",
			Status:                   "completed",
			WindowNumber:             checkpoint.WindowNumber,
			PreviousWindowID:         checkpoint.PreviousWindowID,
			WindowID:                 checkpoint.WindowID,
			PriorTokenEstimate:       checkpoint.PriorHistoryTokenEstimate,
			ReplacementTokenEstimate: checkpoint.ReplacementHistoryTokenEstimate,
			IgnoredToolCallCount:     max(ignoredToolCallCount, 0),
		}); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.currentWindowID = attempt.windowID
	r.active = nil
	r.mu.Unlock()
	return nil
}

func (r *projectEinoAssistantCompactionRuntime) fail(ctx context.Context, cause error) error {
	if r == nil || cause == nil {
		return cause
	}
	attempt, ok := r.activeAttempt()
	if !ok {
		return cause
	}
	if r.auditRecorder != nil {
		auditErr := r.auditRecorder.recordCompaction(ctx, projectAssistantAuditCompaction{
			ID:                 attempt.id,
			Trigger:            "context_threshold",
			Status:             "failed",
			WindowNumber:       attempt.windowNumber,
			PreviousWindowID:   attempt.previousWindowID,
			WindowID:           attempt.windowID,
			PriorTokenEstimate: attempt.priorTokenEstimate,
			Error:              projectEinoAssistantSafeErrorText(cause),
		})
		cause = errors.Join(cause, auditErr)
	}
	r.mu.Lock()
	r.active = nil
	r.mu.Unlock()
	return cause
}

func (r *projectEinoAssistantCompactionRuntime) setToolSchemaTokens(tokens int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.toolSchemaTokenEstimate = max(tokens, 0)
	r.mu.Unlock()
}

func (r *projectEinoAssistantCompactionRuntime) toolSchemaTokens() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.toolSchemaTokenEstimate
}

func projectEinoAssistantCompactionModelInput(
	_ context.Context,
	_ *schema.Message,
	_ *schema.Message,
	original []*schema.Message,
) ([]*schema.Message, error) {
	input := make([]*schema.Message, 0, len(original)+1)
	input = append(input, original...)
	input = append(input, schema.UserMessage(projectEinoAssistantCompactionPrompt))
	return input, nil
}

func projectEinoAssistantCanonicalCompactionContext(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) ([]*schema.Message, error) {
	if req.Project == nil {
		return nil, fmt.Errorf("project is required for compaction context")
	}
	currentReq := req
	// Persisted Kubernetes Projects always carry a resourceVersion. Synthetic
	// unit-test Projects do not have a server object to refresh.
	if req.Client != nil && strings.TrimSpace(req.Project.ResourceVersion) != "" {
		refreshed, err := refreshProjectAssistantWorkflowRunContext(ctx, projectAssistantWorkflowRunContext{
			Client:     req.Client,
			Project:    req.Project,
			Repository: req.Repository,
		})
		if err != nil {
			return nil, fmt.Errorf("refresh project compaction context: %w", err)
		}
		currentReq.Project = refreshed.Project
		currentReq.Repository = refreshed.Repository
	}

	contextMessages := []chatMessage{
		{Role: "system", Content: projectEinoAssistantV2DeepInstruction},
		{Role: "system", Content: projectSystemPromptForMode(
			currentReq.Project,
			currentReq.Repository,
			currentReq.CollaborationMode,
			projectAssistantInitialBuildActive(currentReq, runState),
		)},
	}
	if snapshot, ok := projectEinoAssistantSessionContextMessage(ctx, currentReq, runState); ok {
		contextMessages = append(contextMessages, snapshot)
	}
	if runState != nil {
		if prompt := runState.ToolPrompt(); prompt != "" {
			contextMessages = append(contextMessages, chatMessage{Role: "system", Content: prompt})
		}
		if prompt := runState.SkillPrompt(); prompt != "" {
			contextMessages = append(contextMessages, chatMessage{Role: "system", Content: prompt})
		}
	}
	messages, err := projectChatMessagesToEino(contextMessages)
	if err != nil {
		return nil, fmt.Errorf("build project compaction context: %w", err)
	}
	return messages, nil
}

func projectEinoAssistantCompactionRetryConfig(req projectAssistantRunRequest) *summarization.RetryConfig {
	maxRetries := projectEinoAssistantModelMaxRetries(req.LLM)
	baseBackoff := req.LLM.RetryBackoff
	if baseBackoff <= 0 {
		baseBackoff = 200 * time.Millisecond
	}
	return &summarization.RetryConfig{
		MaxRetries: &maxRetries,
		ShouldRetry: func(ctx context.Context, response *schema.Message, err error) bool {
			if ctx.Err() != nil || err == nil || projectEinoAssistantCompactionHasOutput(response) {
				return false
			}
			if !projectEinoAssistantShouldRetryModelError(err) {
				return false
			}
			// Eino invokes ShouldRetry once per failed response, before applying
			// BackoffFunc for the corresponding one-based retry attempt.
			return true
		},
		BackoffFunc: func(_ context.Context, attempt int, _ *schema.Message, _ error) time.Duration {
			if attempt < 1 {
				attempt = 1
			}
			projectEinoAssistantPublishRetryAttempt(req.StreamCallbacks, attempt, maxRetries)
			delay := baseBackoff * time.Duration(1<<min(attempt-1, 6))
			return min(delay, 10*time.Second)
		},
	}
}

func projectEinoAssistantCompactionHasOutput(message *schema.Message) bool {
	if message == nil {
		return false
	}
	if strings.TrimSpace(message.Content) != "" || len(message.ToolCalls) > 0 {
		return true
	}
	for _, part := range message.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && strings.TrimSpace(part.Text) != "" {
			return true
		}
	}
	return false
}

func projectEinoAssistantFinalizeCompaction(
	canonicalContext, original []*schema.Message,
	summary *schema.Message,
	toolSchemaTokens int,
) ([]*schema.Message, error) {
	if summary == nil || summary.Role != schema.Assistant {
		return nil, fmt.Errorf("compaction model returned an invalid assistant response")
	}
	// Codex records every completed compaction response item but extracts only
	// assistant text for the replacement history. Tool calls from this internal
	// request are inert: they are never dispatched, never copied into the
	// checkpoint, and never allowed to fail the outer agent turn. A tool-only
	// response intentionally produces a prefix-only summary checkpoint.
	summaryText := projectEinoAssistantSummaryText(summary)

	userTokenBudget := projectEinoAssistantCompactionUserTokenBudget(canonicalContext, summaryText, toolSchemaTokens)
	userMessages := projectEinoAssistantRecentUserMessages(original, userTokenBudget)
	// The checkpoint retains only the compacted conversational payload. Project
	// metadata, workspace snapshot, and tool guidance are regenerated at each
	// outer model boundary; persisting them here would make a later resume use
	// stale authority and capability context.
	compacted := make([]*schema.Message, 0, len(userMessages)+1)
	compacted = append(compacted, userMessages...)
	compacted = append(compacted, schema.UserMessage(projectEinoAssistantCompactionSummaryContent(summaryText)))
	return compacted, nil
}

func projectEinoAssistantCompactionUserTokenBudget(
	canonicalContext []*schema.Message,
	summary string,
	toolSchemaTokens int,
) int {
	contextTokens := projectEinoAssistantCompactionContextTokens()
	reserve := contextTokens * projectEinoAssistantCompactionReservePercent / 100
	overhead := projectEinoAssistantMessagesTokenEstimate(canonicalContext) +
		max(toolSchemaTokens, 0) +
		projectEinoAssistantApproxTokenCount(projectEinoAssistantCompactionSummaryContent(summary))
	return max(contextTokens-reserve-overhead, 0)
}

func projectEinoAssistantRecentUserMessages(messages []*schema.Message, maxTokens int) []*schema.Message {
	if maxTokens <= 0 {
		return nil
	}
	remaining := maxTokens
	selected := make([]*schema.Message, 0)
	for index := len(messages) - 1; index >= 0 && remaining > 0; index-- {
		message := messages[index]
		if message == nil || message.Role != schema.User {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" ||
			strings.HasPrefix(content, projectEinoAssistantCompactionSummaryPrefix) ||
			projectEinoAssistantSyntheticWorkspaceMutationEvidence(message) {
			continue
		}
		tokens := projectEinoAssistantApproxTokenCount(content)
		retained := message
		if tokens > remaining {
			retained = schema.UserMessage(projectEinoAssistantTruncateToTokens(content, remaining))
			remaining = 0
		} else {
			remaining -= tokens
		}
		selected = append(selected, retained)
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return selected
}

func projectEinoAssistantMessagesTokenEstimate(messages []*schema.Message) int {
	if len(messages) == 0 {
		return 0
	}
	raw, err := json.Marshal(projectEinoMessagesToChat(messages))
	if err != nil {
		return 0
	}
	return projectEinoAssistantApproxTokenCount(string(raw))
}

func projectEinoAssistantToolSchemasTokenEstimate(toolInfos []*schema.ToolInfo) int {
	if len(toolInfos) == 0 {
		return 0
	}
	total := 0
	for _, info := range toolInfos {
		if info == nil {
			continue
		}
		copyInfo := *info
		copyInfo.Extra = nil
		raw, err := json.Marshal(&copyInfo)
		if err != nil {
			continue
		}
		total += projectEinoAssistantApproxTokenCount(string(raw))
	}
	return total
}

func projectEinoAssistantApproxTokenCount(text string) int {
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return 0
	}
	return (runes + 3) / 4
}

func projectEinoAssistantTruncateToTokens(text string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	maxRunes := maxTokens * 4
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	const marker = "\n[... user message truncated during compaction ...]"
	markerRunes := []rune(marker)
	if len(markerRunes) >= maxRunes {
		return string(markerRunes[:maxRunes])
	}
	return string(runes[:maxRunes-len(markerRunes)]) + marker
}

func projectEinoAssistantCompactionSummaryContent(summary string) string {
	return projectEinoAssistantCompactionSummaryPrefix + "\n" + strings.TrimSpace(summary)
}

func projectEinoAssistantLastNonSystemMessageIsTool(messages []*schema.Message) bool {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message == nil || message.Role == schema.System {
			continue
		}
		if projectEinoAssistantSyntheticWorkspaceMutationEvidence(message) {
			continue
		}
		return message.Role == schema.Tool
	}
	return false
}

func projectEinoAssistantSummaryText(msg *schema.Message) string {
	if msg == nil || msg.Role != schema.Assistant {
		return ""
	}
	var parts []string
	for _, part := range msg.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return strings.TrimSpace(msg.Content)
}
