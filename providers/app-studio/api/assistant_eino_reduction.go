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
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/schema"
)

const (
	projectEinoAssistantWorkspaceMutationEvidencePrefix = "Workspace mutation evidence:"
	projectEinoAssistantSyntheticMessageKindKey         = "app_studio_synthetic_message_kind"
	projectEinoAssistantWorkspaceMutationEvidenceKind   = "workspace_mutation_evidence"
)

const projectEinoAssistantReductionContextTokens int64 = 12000

// projectEinoAssistantCompactedToolCallPreamble keeps the rewritten tool-call
// message non-empty. The OpenAI wire encoder drops an empty content field
// entirely, and several OpenAI-compatible providers reject the resulting
// assistant message with "expected a string, got null" — which would then fail
// every subsequent request in the session, since the rewrite is durable.
const projectEinoAssistantCompactedToolCallPreamble = "Applying workspace mutations."

// projectEinoAssistantReductionMiddleware removes large historical workspace
// mutation payloads before they force the more expensive session summarizer,
// while retaining compact tool-result evidence for phase derivation and
// checkpoint resume. It deliberately keeps the latest tool-call group intact.
func projectEinoAssistantReductionMiddleware(ctx context.Context) (adk.ChatModelAgentMiddleware, error) {
	return reduction.New(ctx, &reduction.Config{
		SkipTruncation: true,
		// Preserve the latest tool group while clearing older read payloads.
		// Successful workspace mutations use the compact App Studio rewrite
		// below; ordinary stale reads use Eino's default bounded placeholder.
		SkipClear:                 false,
		MaxTokensForClear:         projectEinoAssistantReductionContextTokens,
		ClearRetentionSuffixLimit: 1,
		ClearAtLeastTokens:        1,
		ClearMessageRewriter:      projectEinoAssistantRewriteWorkspaceMutations,
	})
}

func projectEinoAssistantRewriteWorkspaceMutations(
	_ context.Context,
	toolCallMessage *schema.Message,
	toolResponseMessages []*schema.Message,
) ([]*schema.Message, error) {
	if !projectEinoAssistantSuccessfulWorkspaceMutationGroup(toolCallMessage, toolResponseMessages) {
		return projectEinoAssistantOriginalToolMessageGroup(toolCallMessage, toolResponseMessages), nil
	}

	summaries := make([]string, 0, len(toolCallMessage.ToolCalls))
	compactedCalls := make([]schema.ToolCall, 0, len(toolCallMessage.ToolCalls))
	compactedResponses := make([]*schema.Message, 0, len(toolCallMessage.ToolCalls))
	for index, toolCall := range toolCallMessage.ToolCalls {
		summary := summarizeProjectToolResult(toolCall.Function.Name, toolResponseMessages[index].Content)
		if summary == "" {
			return projectEinoAssistantOriginalToolMessageGroup(toolCallMessage, toolResponseMessages), nil
		}
		summaries = append(summaries, summary)
		compactedCall := toolCall
		compactedCall.Function.Arguments = `{}`
		compactedCall.Extra = nil
		compactedCalls = append(compactedCalls, compactedCall)
		compactedResult, err := json.Marshal(struct {
			Operation string `json:"operation"`
		}{
			Operation: projectToolBaseName(toolCall.Function.Name),
		})
		if err != nil {
			return nil, err
		}
		compactedResponses = append(compactedResponses, schema.ToolMessage(
			string(compactedResult),
			toolCall.ID,
			schema.WithToolName(toolCall.Function.Name),
		))
	}
	messages := make([]*schema.Message, 0, len(compactedResponses)+2)
	messages = append(messages, schema.AssistantMessage(projectEinoAssistantCompactedToolCallPreamble, compactedCalls))
	messages = append(messages, compactedResponses...)
	evidence := schema.UserMessage(
		projectEinoAssistantWorkspaceMutationEvidencePrefix + " " + strings.Join(summaries, "; ") + ". Treat these completed results as authoritative; reread only after a conflict, failed mutation, or later mutation.",
	)
	evidence.Extra = map[string]any{
		projectEinoAssistantSyntheticMessageKindKey: projectEinoAssistantWorkspaceMutationEvidenceKind,
	}
	messages = append(messages, evidence)
	return messages, nil
}

func projectEinoAssistantSyntheticWorkspaceMutationEvidence(message *schema.Message) bool {
	if message == nil || message.Role != schema.User || message.Extra == nil {
		return false
	}
	kind, _ := message.Extra[projectEinoAssistantSyntheticMessageKindKey].(string)
	return kind == projectEinoAssistantWorkspaceMutationEvidenceKind
}

func projectEinoAssistantSuccessfulWorkspaceMutationGroup(toolCallMessage *schema.Message, toolResponseMessages []*schema.Message) bool {
	if toolCallMessage == nil || len(toolCallMessage.ToolCalls) == 0 || len(toolCallMessage.ToolCalls) != len(toolResponseMessages) {
		return false
	}
	for index, toolCall := range toolCallMessage.ToolCalls {
		if !projectEinoAssistantWorkspaceMutationTool(toolCall.Function.Name) {
			return false
		}
		response := toolResponseMessages[index]
		if response == nil || (response.ToolCallID != "" && response.ToolCallID != toolCall.ID) || !projectEinoAssistantSuccessfulWorkspaceMutationResult(toolCall.Function.Name, response.Content) {
			return false
		}
	}
	return true
}

func projectEinoAssistantWorkspaceMutationTool(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile, projectToolMoveFile:
		return true
	default:
		return false
	}
}

func projectEinoAssistantSuccessfulWorkspaceMutationResult(name, content string) bool {
	// Mutation-shaped JSON can carry a failed semantic disposition while still
	// exposing a path (and no explicit changed=false). Consult the typed
	// settlement adapter before treating the shape as source progress; the
	// transport-error partial-mutation branch deliberately uses its separate
	// HasChanges predicate so genuine post-write failures remain recoverable.
	if projectAssistantToolResultDisposition(name, content, nil) != projectAssistantToolDispositionSucceeded {
		return false
	}
	var result struct {
		Operation string `json:"operation"`
		Changed   *bool  `json:"changed"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &result); err != nil {
		return false
	}
	if result.Operation != projectToolBaseName(name) {
		return false
	}
	return result.Changed == nil || *result.Changed
}

func projectEinoAssistantOriginalToolMessageGroup(toolCallMessage *schema.Message, toolResponseMessages []*schema.Message) []*schema.Message {
	messages := make([]*schema.Message, 0, len(toolResponseMessages)+1)
	messages = append(messages, toolCallMessage)
	messages = append(messages, toolResponseMessages...)
	return messages
}
