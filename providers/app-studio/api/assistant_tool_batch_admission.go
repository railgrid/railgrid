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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var errProjectAssistantInvalidToolBatch = errors.New("invalid assistant tool batch")

type projectEinoAssistantInvalidToolBatchError struct {
	Code   string
	Reason string
}

func (e *projectEinoAssistantInvalidToolBatchError) Error() string {
	if e == nil {
		return errProjectAssistantInvalidToolBatch.Error()
	}
	return fmt.Sprintf("%s (%s): %s", errProjectAssistantInvalidToolBatch, e.Code, e.Reason)
}

func (e *projectEinoAssistantInvalidToolBatchError) Unwrap() error {
	return errProjectAssistantInvalidToolBatch
}

type projectEinoAssistantToolBatchMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	runState         *projectEinoAssistantRunState
	executionContext *projectAssistantExecutionContext
}

func projectEinoAssistantToolBatchAdmissionMiddleware(
	runState *projectEinoAssistantRunState,
	executionContexts ...*projectAssistantExecutionContext,
) adk.ChatModelAgentMiddleware {
	middleware := &projectEinoAssistantToolBatchMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		runState:                     runState,
	}
	if len(executionContexts) > 0 {
		middleware.executionContext = executionContexts[0]
	}
	return middleware
}

// AfterModelRewriteState is the last admission boundary before Eino hands an
// assistant response to its tools node. This hook rejects structurally invalid
// batches, canonicalizes accepted calls, and assigns stable call IDs. It preserves the
// model's call order and cardinality. Execution policy belongs to the tool
// concurrency gate and invocation-time authority, not to a second model-facing
// read/action protocol.
func (m *projectEinoAssistantToolBatchMiddleware) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	response := state.Messages[len(state.Messages)-1]
	if response == nil || response.Role != schema.Assistant || len(response.ToolCalls) == 0 {
		return ctx, state, nil
	}

	rawCalls := append([]schema.ToolCall(nil), response.ToolCalls...)
	modelCallOrdinal := m.runState.CurrentModelCallOrdinal()
	if modelCallOrdinal == 0 {
		// Lifecycle normally advances the durable model-call ordinal before
		// admission. Keep the admission boundary safe when it is exercised in
		// isolation or by a future middleware ordering change.
		modelCallOrdinal = m.runState.NextModelCallOrdinal()
	}
	normalized, err := projectEinoAssistantNormalizeToolBatch(rawCalls, modelCallOrdinal)
	if err != nil {
		// This should only be reachable when another middleware rewrites the
		// response after retry admission. Never dispatch an unvalidated batch.
		m.runState.discardLatestModelToolBatch(response.ToolCalls)
		return ctx, nil, err
	}
	response.ToolCalls = normalized
	m.runState.reconcileLatestModelToolBatch(rawCalls, normalized, false)
	return ctx, state, nil
}

// WrapInvokableToolCall preserves Eino's native per-output-item scheduling but
// applies a Codex-style reader/writer gate around every fixed executable tool,
// including framework-provided filesystem reads. Only explicitly safe reads
// overlap; effects and unknown contracts take exclusive ownership.
func (m *projectEinoAssistantToolBatchMiddleware) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	if m.executionContext == nil || toolCtx == nil {
		return endpoint, nil
	}
	parallelSafe := projectEinoAssistantToolParallelSafe(toolCtx.Name)
	return func(ctx context.Context, argumentsInJSON string, opts ...einotool.Option) (string, error) {
		if parallelSafe {
			m.executionContext.toolMu.RLock()
			defer m.executionContext.toolMu.RUnlock()
		} else {
			m.executionContext.toolMu.Lock()
			defer m.executionContext.toolMu.Unlock()
		}
		return endpoint(ctx, argumentsInJSON, opts...)
	}, nil
}

func (m *projectEinoAssistantToolBatchMiddleware) WrapEnhancedInvokableToolCall(
	_ context.Context,
	endpoint adk.EnhancedInvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (adk.EnhancedInvokableToolCallEndpoint, error) {
	if m.executionContext == nil || toolCtx == nil {
		return endpoint, nil
	}
	parallelSafe := projectEinoAssistantToolParallelSafe(toolCtx.Name)
	return func(ctx context.Context, argument *schema.ToolArgument, opts ...einotool.Option) (*schema.ToolResult, error) {
		if parallelSafe {
			m.executionContext.toolMu.RLock()
			defer m.executionContext.toolMu.RUnlock()
		} else {
			m.executionContext.toolMu.Lock()
			defer m.executionContext.toolMu.Unlock()
		}
		return endpoint(ctx, argument, opts...)
	}, nil
}

func projectEinoAssistantToolParallelSafe(name string) bool {
	name = projectToolBaseName(name)
	if projectEinoAssistantFilesystemReadTool(name) || name == projectEinoAssistantToolSearchTool {
		return true
	}
	if spec, ok := projectAssistantWorkflowToolSpec(name); ok {
		return spec.Risk == projectAssistantToolRiskRead && spec.ParallelSafe
	}
	if spec, ok := projectAssistantLocalToolRegistry(nil).Spec(name); ok {
		return spec.Risk == projectAssistantToolRiskRead && spec.ParallelSafe
	}
	// MCP and unknown tools have no trusted server-owned concurrency contract.
	return false
}

func projectEinoAssistantNormalizeToolBatch(
	calls []schema.ToolCall,
	modelCallOrdinal int,
) ([]schema.ToolCall, error) {
	normalized, err := projectEinoAssistantAnalyzeToolBatch(calls)
	if err != nil {
		return nil, err
	}
	usedIDs := make(map[string]struct{}, len(normalized))
	for index := range normalized {
		call := &normalized[index]
		if call.ID == "" {
			call.ID = projectEinoAssistantDeterministicToolCallID(modelCallOrdinal, index, *call)
		}
		if _, exists := usedIDs[call.ID]; exists {
			return nil, &projectEinoAssistantInvalidToolBatchError{
				Code:   "conflicting_tool_call_id",
				Reason: fmt.Sprintf("admitted call %d reuses another admitted call ID", index+1),
			}
		}
		usedIDs[call.ID] = struct{}{}
		position := index
		call.Index = &position
	}
	return normalized, nil
}

func projectEinoAssistantAnalyzeToolBatch(calls []schema.ToolCall) ([]schema.ToolCall, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	normalized := make([]schema.ToolCall, 0, len(calls))
	signatureByID := make(map[string]string, len(calls))

	for index := range calls {
		call, signature, _, err := projectEinoAssistantCanonicalToolCall(calls[index], index)
		if err != nil {
			return nil, err
		}
		if call.ID != "" {
			if previous, exists := signatureByID[call.ID]; exists && previous != signature {
				return nil, &projectEinoAssistantInvalidToolBatchError{
					Code:   "conflicting_tool_call_id",
					Reason: fmt.Sprintf("call %d reuses an existing call ID for a different operation", index+1),
				}
			}
			signatureByID[call.ID] = signature
		}
		normalized = append(normalized, call)
	}
	return normalized, nil
}

func projectEinoAssistantCanonicalToolCall(
	raw schema.ToolCall,
	index int,
) (schema.ToolCall, string, bool, error) {
	call := raw
	call.ID = strings.TrimSpace(call.ID)
	call.Type = strings.TrimSpace(call.Type)
	if call.Type == "" {
		call.Type = "function"
	}
	if call.Type != "function" {
		return schema.ToolCall{}, "", false, &projectEinoAssistantInvalidToolBatchError{
			Code:   "unsupported_tool_call_type",
			Reason: fmt.Sprintf("call %d is not a function call", index+1),
		}
	}
	call.Function.Name = strings.TrimSpace(call.Function.Name)
	if call.Function.Name == "" {
		return schema.ToolCall{}, "", false, &projectEinoAssistantInvalidToolBatchError{
			Code:   "missing_tool_name",
			Reason: fmt.Sprintf("call %d has no tool name", index+1),
		}
	}
	arguments, err := projectEinoAssistantCanonicalToolArguments(call.Function.Arguments)
	if err != nil {
		return schema.ToolCall{}, "", false, &projectEinoAssistantInvalidToolBatchError{
			Code:   "invalid_tool_arguments",
			Reason: fmt.Sprintf("call %d does not contain one JSON object for its arguments", index+1),
		}
	}
	call.Function.Arguments = arguments
	sum := sha256.Sum256([]byte(call.Function.Name + "\x00" + arguments))
	signature := hex.EncodeToString(sum[:])
	return call, signature, projectEinoAssistantToolBatchRead(call.Function.Name), nil
}

func projectEinoAssistantCanonicalToolArguments(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var arguments map[string]any
	if err := decoder.Decode(&arguments); err != nil || arguments == nil {
		return "", errors.New("tool arguments must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("tool arguments contain trailing JSON")
	}
	canonical, err := json.Marshal(arguments)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func projectEinoAssistantDeterministicToolCallID(
	modelCallOrdinal int,
	index int,
	call schema.ToolCall,
) string {
	return projectEinoAssistantSyntheticToolCallID(
		modelCallOrdinal,
		index,
		call.Function.Name,
		call.Function.Arguments,
	)
}

func projectEinoAssistantSyntheticToolCallID(
	modelCallOrdinal int,
	index int,
	name string,
	arguments string,
) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%d\x00%d\x00%s\x00%s",
		modelCallOrdinal,
		index,
		strings.TrimSpace(name),
		arguments,
	)))
	return "call_appstudio_" + hex.EncodeToString(sum[:12])
}

func projectEinoAssistantToolBatchRead(name string) bool {
	rawName := strings.TrimSpace(name)
	baseName := projectToolBaseName(rawName)
	if projectEinoAssistantFilesystemReadTool(baseName) || baseName == projectEinoAssistantToolSearchTool {
		return true
	}
	if spec, ok := projectAssistantWorkflowToolSpec(baseName); ok {
		return spec.Risk == projectAssistantToolRiskRead
	}
	registry := projectAssistantLocalToolRegistry(nil)
	if spec, ok := registry.Spec(rawName); ok {
		return spec.Risk == projectAssistantToolRiskRead
	}
	if spec, ok := registry.Spec(baseName); ok {
		return spec.Risk == projectAssistantToolRiskRead
	}
	if spec, ok := projectAssistantMCPToolSpec(projectMCPTool{Name: rawName}); ok {
		return spec.Risk == projectAssistantToolRiskRead
	}
	return false
}

// The model callback runs inside Eino's retry wrapper and therefore observes a
// rejected response before ShouldRetry makes its decision. Keep the durable
// run-state projection aligned with Eino's accepted state by removing rejected
// batches and replacing accepted batches with their normalized form.
func (s *projectEinoAssistantRunState) discardLatestModelToolBatch(raw []schema.ToolCall) {
	s.reconcileLatestModelToolBatch(raw, nil, true)
}

func (s *projectEinoAssistantRunState) reconcileLatestModelToolBatch(
	expected []schema.ToolCall,
	replacement []schema.ToolCall,
	discard bool,
) {
	if s == nil || len(expected) == 0 {
		return
	}
	expectedCalls := projectEinoToolCallsToChat(expected)
	replacementCalls := projectEinoToolCallsToChat(replacement)
	s.mu.Lock()
	defer s.mu.Unlock()
	for messageIndex := len(s.messages) - 1; messageIndex >= 0; messageIndex-- {
		message := &s.messages[messageIndex]
		if message.Role != "assistant" || !projectEinoAssistantChatToolBatchesMatch(message.ToolCalls, expectedCalls) {
			continue
		}
		for _, call := range message.ToolCalls {
			signature := projectEinoAssistantToolCallSignature(call.Function.Name, call.Function.Arguments)
			if s.seenToolCalls[signature] <= 1 {
				delete(s.seenToolCalls, signature)
			} else {
				s.seenToolCalls[signature]--
			}
		}
		if discard {
			s.messages = append(s.messages[:messageIndex], s.messages[messageIndex+1:]...)
			s.toolCalls = nil
			if s.turn > 0 {
				s.turn--
			}
			return
		}
		message.ToolCalls = cloneProjectAssistantToolCalls(replacementCalls)
		s.toolCalls = cloneProjectAssistantToolCalls(replacementCalls)
		for _, call := range replacementCalls {
			signature := projectEinoAssistantToolCallSignature(call.Function.Name, call.Function.Arguments)
			s.seenToolCalls[signature]++
		}
		return
	}
}

func projectEinoAssistantChatToolBatchesMatch(left, right []chatToolCall) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if strings.TrimSpace(left[index].Function.Name) != strings.TrimSpace(right[index].Function.Name) ||
			left[index].Function.Arguments != right[index].Function.Arguments {
			return false
		}
	}
	return true
}
