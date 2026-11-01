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
	"reflect"

	"github.com/cloudwego/eino/schema"

	"github.com/railgrid/provider-agents/backend"
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
		if len(tc.Extra) > 0 {
			if extra, err := json.Marshal(tc.Extra); err == nil {
				n += estimateTokens(string(extra))
			}
		}
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
	id                string
	sequence          int64
	ephemeral         bool
	resultReference   *backend.ToolResultReference
	resultUnavailable bool
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
		if m.Role == schema.Assistant {
			if continuation, err := backend.ContinuationMetadataFromExtra(m.Extra); err == nil {
				msg.Continuation = continuation
			}
		}
		if identity, ok := identities[m]; ok {
			msg.ID = identity.id
			msg.Sequence = identity.sequence
			msg.Ephemeral = identity.ephemeral
			if identity.resultReference != nil {
				ref := identity.resultReference.Clone()
				msg.ResultReference = &ref
			}
			msg.ResultUnavailable = identity.resultUnavailable
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
		if i >= len(wire) || (message.ID == "" && message.Sequence == 0 && !message.Ephemeral && message.ResultReference == nil && !message.ResultUnavailable) {
			continue
		}
		identity := historyIdentity{
			id: message.ID, sequence: message.Sequence, ephemeral: message.Ephemeral,
			resultUnavailable: message.ResultUnavailable,
		}
		if message.ResultReference != nil {
			ref := message.ResultReference.Clone()
			identity.resultReference = &ref
		}
		identities[wire[i]] = identity
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
	safeHistory := redactReferenceOnlyForCompaction(history)
	replacement, err := cfg.ContextCompactor(ctx, safeHistory, estimate)
	if err != nil {
		return nil, nil, false, fmt.Errorf("engine: context compactor: %w", err)
	}
	if len(replacement) == 0 {
		return nil, nil, false, &ContextBudgetError{Estimate: estimate, Reason: "context compactor returned empty history"}
	}
	if err := validateToolMessagePairing(replacement); err != nil {
		return nil, nil, false, fmt.Errorf("engine: context compactor: %w", err)
	}
	if err := validateRetainedToolCallMetadata(history, replacement); err != nil {
		return nil, nil, false, fmt.Errorf("engine: context compactor: %w", err)
	}
	if err := validateReferenceOnlyCompaction(history, replacement); err != nil {
		return nil, nil, false, err
	}
	if err := validateContinuationCompaction(history, replacement); err != nil {
		return nil, nil, false, err
	}
	if err := ensureLatestUserMessageRetained(replacement, task); err != nil {
		return nil, nil, false, fmt.Errorf("engine: context compactor: %w", err)
	}
	if err := ensureSystemMessagesRetained(safeHistory, replacement); err != nil {
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

// validateRetainedToolCallMetadata keeps opaque adapter state attached to any
// tool call the compactor retains. A whole call/result group may be summarized
// away, but a retained call must keep its original provider continuation data.
func validateRetainedToolCallMetadata(source, replacement []Message) error {
	metadataByID := make(map[string]map[string]any)
	for _, message := range source {
		if message.Role != RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if len(call.Extra) > 0 {
				metadataByID[call.ID] = call.Extra
			}
		}
	}
	for _, message := range replacement {
		if message.Role != RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			prior, ok := metadataByID[call.ID]
			if ok && !reflect.DeepEqual(prior, call.Extra) {
				return fmt.Errorf("context compactor changed continuation metadata for tool call %q", call.ID)
			}
		}
	}
	return nil
}

// redactReferenceOnlyForCompaction preserves opaque references but never sends
// reference-only result bytes to the callback that writes replacement history.
func redactReferenceOnlyForCompaction(messages []Message) []Message {
	out := append([]Message(nil), messages...)
	for i := range out {
		out[i].Continuation = out[i].Continuation.Clone()
		if out[i].ResultReference != nil {
			ref := out[i].ResultReference.Clone()
			out[i].ResultReference = &ref
			out[i].Content = backend.ToolResultReferencePlaceholder
		} else if out[i].ResultUnavailable {
			out[i].Content = backend.ToolResultUnavailablePlaceholder
		}
	}
	return out
}

// validateContinuationCompaction prevents a compactor from changing metadata
// required to replay a retained assistant turn. Whole assistant messages may
// be folded away; if a message is retained, its allowlisted continuation pair
// must remain byte-for-byte identical.
func validateContinuationCompaction(source, replacement []Message) error {
	want := make(map[string][]*backend.ContinuationMetadata)
	for _, message := range source {
		if message.Role != RoleAssistant {
			continue
		}
		key := continuationMessageKey(message)
		want[key] = append(want[key], message.Continuation.Clone())
	}
	used := make(map[string]int, len(want))
	for _, message := range replacement {
		if message.Role != RoleAssistant {
			if message.Continuation != nil {
				return fmt.Errorf("context compactor changed continuation metadata")
			}
			continue
		}
		if message.Continuation != nil {
			if err := message.Continuation.Validate(); err != nil {
				return fmt.Errorf("context compactor changed continuation metadata")
			}
		}
		key := continuationMessageKey(message)
		candidates, ok := want[key]
		index := used[key]
		if !ok || index >= len(candidates) {
			if message.Continuation != nil {
				return fmt.Errorf("context compactor changed continuation metadata")
			}
			continue
		}
		if !candidates[index].Equal(message.Continuation) {
			return fmt.Errorf("context compactor changed continuation metadata")
		}
		used[key]++
	}
	return nil
}

func continuationMessageKey(message Message) string {
	if message.ID != "" {
		return fmt.Sprintf("id\x00%s\x00%d", message.ID, message.Sequence)
	}
	callIDs := make([]string, 0, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		callIDs = append(callIDs, call.ID)
	}
	encoded, _ := json.Marshal(struct {
		Role    string   `json:"role"`
		Content string   `json:"content"`
		Calls   []string `json:"calls"`
	}{Role: message.Role, Content: message.Content, Calls: callIDs})
	return string(encoded)
}

// validateReferenceOnlyCompaction requires every reference-only receipt to
// survive compaction unchanged and rejects replacement text in place of its
// fixed placeholder.
func validateReferenceOnlyCompaction(source, replacement []Message) error {
	want := make(map[string]Message)
	for _, message := range source {
		if message.ResultReference == nil && !message.ResultUnavailable {
			continue
		}
		if message.Role != RoleTool || message.ToolCallID == "" || message.Name == "" || (message.ResultReference != nil && message.ResultUnavailable) {
			return ErrToolResultReferenceUnavailable
		}
		if message.ResultReference != nil {
			args, ok := findToolCallArgs(source, message.ToolCallID, message.Name)
			if !ok || ValidateToolResultReference(*message.ResultReference, message.Name, message.ToolCallID, args) != nil {
				return ErrToolResultReferenceUnavailable
			}
		} else if message.Content != backend.ToolResultUnavailablePlaceholder {
			return ErrToolResultReferenceUnavailable
		}
		if _, duplicate := want[message.ToolCallID]; duplicate {
			return ErrToolResultReferenceUnavailable
		}
		want[message.ToolCallID] = message
	}
	for _, message := range replacement {
		if message.ResultReference == nil && !message.ResultUnavailable {
			continue
		}
		prior, ok := want[message.ToolCallID]
		if !ok || prior.Name != message.Name || prior.ResultUnavailable != message.ResultUnavailable ||
			((prior.ResultReference == nil) != (message.ResultReference == nil)) ||
			(prior.ResultReference != nil && !prior.ResultReference.Equal(*message.ResultReference)) {
			return ErrToolResultReferenceUnavailable
		}
		if message.ResultReference != nil && message.Content != backend.ToolResultReferencePlaceholder ||
			message.ResultUnavailable && message.Content != backend.ToolResultUnavailablePlaceholder {
			return ErrToolResultReferenceUnavailable
		}
		args, ok := findToolCallArgs(replacement, message.ToolCallID, message.Name)
		priorArgs, priorOK := findToolCallArgs(source, message.ToolCallID, prior.Name)
		if !ok || !priorOK || args != priorArgs {
			return ErrToolResultReferenceUnavailable
		}
		delete(want, message.ToolCallID)
	}
	if len(want) != 0 {
		return ErrToolResultReferenceUnavailable
	}
	return nil
}

func findToolCallArgs(messages []Message, callID, toolName string) (string, bool) {
	var args string
	found := false
	for _, message := range messages {
		if message.Role != RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID != callID || call.Function.Name != toolName {
				continue
			}
			if found {
				return "", false
			}
			args, found = call.Function.Arguments, true
		}
	}
	return args, found
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
