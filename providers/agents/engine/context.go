// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"
)

// ContextEstimate describes the estimated model input size at a request
// boundary. Tool schemas are included because they consume the same model
// context as conversation messages.
type ContextEstimate struct {
	MessageTokens    int
	ToolSchemaTokens int
	TotalTokens      int
	BudgetTokens     int
}

// ContextBudgetError reports that the estimated request exceeds its configured
// budget and the engine could not replace it with a fitting history.
type ContextBudgetError struct {
	Estimate ContextEstimate
	Reason   string
}

func (e *ContextBudgetError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("engine: context budget exceeded (%d estimated tokens, budget %d): %s", e.Estimate.TotalTokens, e.Estimate.BudgetTokens, e.Reason)
	}
	return fmt.Sprintf("engine: context budget exceeded (%d estimated tokens, budget %d)", e.Estimate.TotalTokens, e.Estimate.BudgetTokens)
}

// estimateMessageTokens approximates a wire message's cost, including a small
// per-message envelope allowance for role and tool-call metadata.
func estimateMessageTokens(m *schema.Message) int {
	if m == nil {
		return 0
	}
	n := estimateTokens(m.Content) + estimateTokens(string(m.Role)) + 8
	n += estimateTokens(m.ToolCallID) + estimateTokens(m.ToolName)
	for _, tc := range m.ToolCalls {
		n += estimateTokens(tc.ID) + estimateTokens(tc.Type)
		n += estimateTokens(tc.Function.Name) + estimateTokens(tc.Function.Arguments) + 8
	}
	// Image payload tokenization varies by model and provider. A conservative
	// reserve ensures an image-bearing history can trigger compaction before the
	// provider rejects it. Image bytes are not expanded into this estimate.
	n += multimodalImageCount(m) * historicalImageTokenEstimate
	return n
}

// estimateToolSchemaTokens estimates the JSON form of the schemas bound to the
// model. ToolInfo's JSON representation is the schema shape passed to Eino's
// model adapters, so it includes names, descriptions, and parameter details.
func estimateToolSchemaTokens(tools []Tool) (int, error) {
	infos, err := toToolInfos(tools)
	if err != nil {
		return 0, err
	}
	if len(infos) == 0 {
		return 0, nil
	}
	raw, err := json.Marshal(infos)
	if err != nil {
		return 0, err
	}
	return estimateTokens(string(raw)), nil
}

// estimateTokens is the same 4-bytes-per-token heuristic the llm package uses,
// duplicated here to keep the engine free of a dependency on it (the engine is
// provider-agnostic and SDK-portable).
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

const historicalImageTokenEstimate = 4096

func multimodalImageCount(m *schema.Message) int {
	if m == nil {
		return 0
	}
	n := 0
	for _, part := range m.UserInputMultiContent {
		if part.Type == schema.ChatMessagePartTypeImageURL {
			n++
		}
	}
	for _, part := range m.MultiContent {
		if part.Type == schema.ChatMessagePartTypeImageURL {
			n++
		}
	}
	return n
}

func estimateConversationTokens(in []*schema.Message) int {
	total := 0
	for _, m := range in {
		total += estimateMessageTokens(m)
	}
	return total
}

// requestContextEstimate uses reported usage from the latest completed model
// request when available. That usage already includes its bound tool schemas;
// only subsequent messages are added. The structural estimate remains a floor
// and is also the fallback after restoring a durable checkpoint.
func requestContextEstimate(in []*schema.Message, toolSchemaTokens, budget int) ContextEstimate {
	messageTokens := estimateConversationTokens(in)
	total := messageTokens + toolSchemaTokens
	for i := len(in) - 1; i >= 0; i-- {
		m := in[i]
		if m == nil || m.Role != schema.Assistant || m.ResponseMeta == nil || m.ResponseMeta.Usage == nil {
			continue
		}
		usage := m.ResponseMeta.Usage
		reported := max(usage.TotalTokens, usage.PromptTokens+usage.CompletionTokens)
		if reported <= 0 {
			continue
		}
		total = max(total, reported+estimateConversationTokens(in[i+1:]))
		break
	}
	return ContextEstimate{MessageTokens: total - toolSchemaTokens, ToolSchemaTokens: toolSchemaTokens, TotalTokens: total, BudgetTokens: budget}
}

// EstimateHistoryTokens measures the structured replacement history using the
// same wire estimator as the engine's final context-budget gate.
func EstimateHistoryTokens(messages []Message) int {
	wire, _ := toEinoWithIdentities(messages)
	return estimateConversationTokens(wire)
}

type historyIdentity struct {
	id        string
	sequence  int64
	ephemeral bool
}

func messagesFromEino(in []*schema.Message, identities map[*schema.Message]historyIdentity) []Message {
	out := make([]Message, 0, len(in))
	for _, m := range in {
		if m == nil {
			continue
		}
		msg := Message{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       messageName(m),
			ToolCalls:  cloneToolCalls(m.ToolCalls),
		}
		if identity, ok := identities[m]; ok {
			msg.ID = identity.id
			msg.Sequence = identity.sequence
			msg.Ephemeral = identity.ephemeral
		}
		if msg.Content == "" && (len(m.UserInputMultiContent) > 0 || len(m.MultiContent) > 0) {
			msg.Content = "[multimodal content from tool calls omitted during context compaction]"
		}
		out = append(out, msg)
	}
	return out
}

func messageName(message *schema.Message) string {
	if message.ToolName != "" {
		return message.ToolName
	}
	return message.Name
}

func identitiesFromMessages(messages []Message, wire []*schema.Message) map[*schema.Message]historyIdentity {
	identities := make(map[*schema.Message]historyIdentity, len(messages))
	for i, message := range messages {
		if i >= len(wire) || (message.ID == "" && message.Sequence == 0 && !message.Ephemeral) {
			continue
		}
		identities[wire[i]] = historyIdentity{id: message.ID, sequence: message.Sequence, ephemeral: message.Ephemeral}
	}
	return identities
}

func latestUserMessage(messages []Message) *Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser && !messages[i].Ephemeral {
			message := messages[i]
			return &message
		}
	}
	return nil
}

func ensureLatestUserMessageRetained(replacement []Message, anchor *Message) error {
	if anchor == nil {
		return nil
	}
	for _, message := range replacement {
		if message.Role != RoleUser || message.Ephemeral {
			continue
		}
		if anchor.ID != "" {
			if message.ID == anchor.ID && message.Content == anchor.Content {
				return nil
			}
			continue
		}
		if message.Content == anchor.Content {
			return nil
		}
	}
	return fmt.Errorf("context compactor dropped the latest user message")
}

func ensureSystemMessagesRetained(original, replacement []Message) error {
	for _, required := range original {
		if required.Role != RoleSystem {
			continue
		}
		retained := false
		for _, candidate := range replacement {
			if candidate.Role != RoleSystem || candidate.Content != required.Content {
				continue
			}
			if required.ID != "" && (candidate.ID != required.ID || candidate.Sequence != required.Sequence) {
				continue
			}
			retained = true
			break
		}
		if !retained {
			return fmt.Errorf("context compactor dropped a system instruction")
		}
	}
	return nil
}

func compactBeforeRequest(
	ctx context.Context,
	in []*schema.Message,
	identities map[*schema.Message]historyIdentity,
	toolSchemaTokens int,
	task *Message,
	cfg TurnConfig,

) ([]*schema.Message, map[*schema.Message]historyIdentity, bool, error) {
	if cfg.ContextBudgetTokens <= 0 {
		return in, identities, false, nil
	}
	estimate := requestContextEstimate(in, toolSchemaTokens, cfg.ContextBudgetTokens)
	if estimate.TotalTokens <= estimate.BudgetTokens {
		return in, identities, false, nil
	}
	if cfg.ContextCompactor == nil {
		return nil, nil, false, &ContextBudgetError{Estimate: estimate, Reason: "no context compactor is configured"}
	}
	history := messagesFromEino(in, identities)
	replacement, err := cfg.ContextCompactor(ctx, history, estimate)
	if err != nil {
		return nil, nil, false, fmt.Errorf("engine: context compactor: %w", err)
	}
	if len(replacement) == 0 {
		return nil, nil, false, &ContextBudgetError{Estimate: estimate, Reason: "context compactor returned empty history"}
	}
	if err := validateToolMessagePairing(replacement); err != nil {
		return nil, nil, false, fmt.Errorf("engine: context compactor: %w", err)
	}
	if err := ensureLatestUserMessageRetained(replacement, task); err != nil {
		return nil, nil, false, fmt.Errorf("engine: context compactor: %w", err)
	}
	if err := ensureSystemMessagesRetained(history, replacement); err != nil {
		return nil, nil, false, fmt.Errorf("engine: context compactor: %w", err)
	}
	rebuilt, rebuiltIdentities := toEinoWithIdentities(replacement)
	messageTokens := estimateConversationTokens(rebuilt)
	estimate.MessageTokens = messageTokens
	estimate.TotalTokens = messageTokens + toolSchemaTokens
	if estimate.TotalTokens > estimate.BudgetTokens {
		return nil, nil, false, &ContextBudgetError{Estimate: estimate, Reason: "context compactor returned history over budget"}
	}
	return rebuilt, rebuiltIdentities, true, nil
}

func cloneToolCalls(in []schema.ToolCall) []schema.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, len(in))
	copy(out, in)
	for i := range out {
		if in[i].Index != nil {
			index := *in[i].Index
			out[i].Index = &index
		}
		if in[i].Extra != nil {
			out[i].Extra = make(map[string]any, len(in[i].Extra))
			for k, v := range in[i].Extra {
				out[i].Extra[k] = v
			}
		}
	}
	return out
}

// validateToolMessagePairing rejects replacements that would leave the model
// with an incomplete or mismatched assistant tool-call group.
func validateToolMessagePairing(messages []Message) error {
	pending := make(map[string]string)
	for i, message := range messages {
		if len(pending) > 0 && message.Role != RoleTool {
			return contextPairingError(i, "tool-call group is interrupted before all results arrive")
		}
		switch message.Role {
		case RoleAssistant:
			for _, call := range message.ToolCalls {
				if call.ID == "" {
					return contextPairingError(i, "assistant tool call has no ID")
				}
				if _, exists := pending[call.ID]; exists {
					return contextPairingError(i, "duplicate assistant tool-call ID")
				}
				pending[call.ID] = call.Function.Name
			}
		case RoleTool:
			if message.ToolCallID == "" {
				return contextPairingError(i, "tool result has no tool-call ID")
			}
			if _, exists := pending[message.ToolCallID]; !exists {
				return contextPairingError(i, "tool result has no matching assistant tool call")
			}
			delete(pending, message.ToolCallID)
		}
	}
	if len(pending) > 0 {
		return contextPairingError(len(messages), "assistant tool call has no matching tool result")
	}
	return nil
}

func contextPairingError(index int, reason string) error {
	return fmt.Errorf("invalid compacted history at message %d: %s", index, reason)
}
