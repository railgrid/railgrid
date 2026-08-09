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

	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectEinoAssistantWorkspaceMutationEvidencePrefix = "Workspace mutation evidence:"
	projectEinoAssistantSyntheticMessageKindKey         = "app_studio_synthetic_message_kind"
	projectEinoAssistantWorkspaceMutationEvidenceKind   = "workspace_mutation_evidence"
)

const projectEinoAssistantReductionContextTokens int64 = 12000

// Eino counts suffix retention in complete assistant tool-call groups. Keep a
// two-group baseline while the controller below separately protects the latest
// two distinct complete read_file results, without exempting older reads from
// bounded clearing altogether.
const projectEinoAssistantReductionReadGroupRetention = 2

const projectEinoAssistantClearedReadFileResult = "[Old tool result content cleared]"

// projectEinoAssistantReductionMiddleware removes large historical workspace
// mutation payloads before they force the more expensive session summarizer,
// while retaining compact tool-result evidence for phase derivation and
// checkpoint resume. It keeps a bounded read evidence window intact.
func projectEinoAssistantReductionMiddleware(ctx context.Context) (adk.ChatModelAgentMiddleware, error) {
	controller := &projectEinoAssistantReductionController{}
	inner, err := reduction.New(ctx, &reduction.Config{
		SkipTruncation: true,
		// Preserve the latest two tool groups while clearing older read payloads.
		// The controller additionally protects the latest two distinct complete
		// reads across interleaved failed mutations. Successful mutations use the
		// compact App Studio rewrite below.
		SkipClear:                 false,
		MaxTokensForClear:         projectEinoAssistantReductionContextTokens,
		ClearRetentionSuffixLimit: projectEinoAssistantReductionReadGroupRetention,
		ClearAtLeastTokens:        1,
		ClearMessageRewriter:      projectEinoAssistantRewriteWorkspaceMutations,
		ToolConfig: map[string]*reduction.ToolReductionConfig{
			projectToolReadFile: {
				ClearHandler: controller.clearReadFile,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	controller.ChatModelAgentMiddleware = inner
	return controller, nil
}

// projectEinoAssistantReductionController keeps the reduction middleware's
// suffix policy bounded while protecting the two latest distinct complete
// reads even when a failed mutation is interleaved between them. Eino's
// ClearRetentionSuffixLimit is message-order based, so a static suffix alone
// can clear one of those reads. The selected call IDs are recomputed for each
// model request and are never persisted as authority.
type projectEinoAssistantReductionController struct {
	adk.ChatModelAgentMiddleware
}

type projectEinoAssistantReductionProtectedReadCallIDsContextKey struct{}

func (m *projectEinoAssistantReductionController) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if m == nil || m.ChatModelAgentMiddleware == nil {
		return ctx, state, nil
	}
	var messages []*schema.Message
	if state != nil {
		messages = state.Messages
	}
	protected := projectEinoAssistantLatestCompleteReadCallIDs(messages)
	ctx = context.WithValue(ctx, projectEinoAssistantReductionProtectedReadCallIDsContextKey{}, protected)
	return m.ChatModelAgentMiddleware.BeforeModelRewriteState(ctx, state, mc)
}

func (m *projectEinoAssistantReductionController) clearReadFile(
	ctx context.Context,
	detail *reduction.ToolDetail,
) (*reduction.ClearResult, error) {
	if detail == nil || detail.ToolResult == nil || detail.ToolContext == nil {
		return &reduction.ClearResult{NeedClear: false}, nil
	}
	protectedIDs, _ := ctx.Value(projectEinoAssistantReductionProtectedReadCallIDsContextKey{}).(map[string]struct{})
	_, protected := protectedIDs[detail.ToolContext.CallID]
	if protected {
		return &reduction.ClearResult{NeedClear: false}, nil
	}
	parts := make([]schema.ToolOutputPart, len(detail.ToolResult.Parts))
	copy(parts, detail.ToolResult.Parts)
	for index := range parts {
		if parts[index].Type == schema.ToolPartTypeText {
			parts[index].Text = projectEinoAssistantClearedReadFileResult
		}
	}
	return &reduction.ClearResult{
		NeedClear:    true,
		ToolArgument: detail.ToolArgument,
		ToolResult:   &schema.ToolResult{Parts: parts},
	}, nil
}

func projectEinoAssistantLatestCompleteReadCallIDs(messages []*schema.Message) map[string]struct{} {
	protected := make(map[string]struct{}, projectEinoAssistantReductionReadGroupRetention)
	seenPaths := make(map[string]struct{}, projectEinoAssistantReductionReadGroupRetention)
	for index := len(messages) - 1; index >= 0 && len(seenPaths) < projectEinoAssistantReductionReadGroupRetention; index-- {
		message := messages[index]
		if message == nil || message.Role != schema.Assistant || len(message.ToolCalls) == 0 {
			continue
		}
		if index+len(message.ToolCalls) >= len(messages) {
			continue
		}
		for offset := len(message.ToolCalls) - 1; offset >= 0 && len(seenPaths) < projectEinoAssistantReductionReadGroupRetention; offset-- {
			toolCall := message.ToolCalls[offset]
			if projectToolBaseName(toolCall.Function.Name) != projectToolReadFile {
				continue
			}
			response := messages[index+offset+1]
			if response == nil || response.Role != schema.Tool ||
				(response.ToolCallID != "" && response.ToolCallID != toolCall.ID) {
				continue
			}
			var evidence struct {
				Path     string `json:"path"`
				Version  string `json:"version"`
				Complete bool   `json:"complete"`
			}
			if json.Unmarshal([]byte(response.Content), &evidence) != nil || !evidence.Complete || strings.TrimSpace(evidence.Version) == "" {
				continue
			}
			path, pathErr := workspace.CleanProjectPath(evidence.Path)
			if pathErr != nil || path == "" {
				continue
			}
			if _, seen := seenPaths[path]; seen {
				continue
			}
			seenPaths[path] = struct{}{}
			if strings.TrimSpace(toolCall.ID) != "" {
				protected[toolCall.ID] = struct{}{}
			}
		}
	}
	return protected
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
	for index, toolCall := range toolCallMessage.ToolCalls {
		summary := summarizeProjectToolResult(toolCall.Function.Name, toolResponseMessages[index].Content)
		if summary == "" {
			return projectEinoAssistantOriginalToolMessageGroup(toolCallMessage, toolResponseMessages), nil
		}
		summaries = append(summaries, summary)
	}
	// Collapse the successful-mutation group into natural-language evidence and
	// DROP the tool-call objects entirely. The earlier code kept each call but
	// blanked its arguments to "{}", which trains the model: a compacted
	// history full of replace_file({}) / edit_file({}) success examples is a
	// few-shot lesson to omit arguments, and the model then emits real
	// mutation calls with empty args that fail validation ("requires path").
	// Evidence text conveys the outcome without leaving a malformed call to
	// imitate. Emit only a provenance-marked synthetic user message: an
	// assistant marker can be mistaken for the model's terminal reply, causing
	// the run to stop while the original task still has unfinished work.
	evidence := schema.UserMessage(
		projectEinoAssistantWorkspaceMutationEvidencePrefix + " [compacted internal history] " + strings.Join(summaries, "; ") + ". Continue the original task; treat these completed results as authoritative and reread only after a conflict, failed mutation, or later mutation.",
	)
	evidence.Extra = map[string]any{
		projectEinoAssistantSyntheticMessageKindKey: projectEinoAssistantWorkspaceMutationEvidenceKind,
	}
	return []*schema.Message{evidence}, nil
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
