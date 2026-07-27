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

const projectEinoAssistantReductionContextTokens int64 = 12000

// projectEinoAssistantReductionMiddleware removes large historical workspace
// mutation payloads before they force the more expensive session summarizer,
// while retaining compact tool-result evidence for phase derivation and
// checkpoint resume. It deliberately keeps the latest tool-call group intact.
func projectEinoAssistantReductionMiddleware(ctx context.Context) (adk.ChatModelAgentMiddleware, error) {
	return reduction.New(ctx, &reduction.Config{
		SkipTruncation: true,
		// ClearMessageRewriter is the only reduction policy App Studio wants.
		// Disable Eino's default clear handler so read results, failures, and
		// mixed tool groups returned unchanged by the rewriter stay intact.
		SkipClear:                 true,
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
	messages = append(messages, schema.AssistantMessage("", compactedCalls))
	messages = append(messages, compactedResponses...)
	messages = append(messages, schema.UserMessage(
		"<system-reminder>Workspace mutations succeeded: "+strings.Join(summaries, "; ")+". Inspect the current workspace before relying on prior file contents.</system-reminder>",
	))
	return messages, nil
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
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
		return true
	default:
		return false
	}
}

func projectEinoAssistantSuccessfulWorkspaceMutationResult(name, content string) bool {
	result := map[string]any{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &result); err != nil {
		return false
	}
	return projectToolString(result["operation"]) == projectToolBaseName(name)
}

func projectEinoAssistantOriginalToolMessageGroup(toolCallMessage *schema.Message, toolResponseMessages []*schema.Message) []*schema.Message {
	messages := make([]*schema.Message, 0, len(toolResponseMessages)+1)
	messages = append(messages, toolCallMessage)
	messages = append(messages, toolResponseMessages...)
	return messages
}
