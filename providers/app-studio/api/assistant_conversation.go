// Copyright 2026 The Faros Authors.
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

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/faroshq/provider-app-studio/store"
)

const (
	projectAssistantConversationUser          = "user_message"
	projectAssistantConversationAssistant     = "assistant_message"
	projectAssistantConversationToolCall      = "tool_call"
	projectAssistantConversationToolResult    = "tool_result"
	projectAssistantConversationSteering      = "steering"
	projectAssistantConversationCompaction    = "compaction"
	projectAssistantConversationInterruption  = "interruption"
	projectAssistantConversationRolloutBudget = "rollout_budget"
	// A generic run is created before provider-specific turn startup. If that
	// startup fails after the user conversation item is appended, this marker
	// makes the originating item inert in every later projection without
	// deleting append-only evidence or changing the store schema.
	projectAssistantConversationStartFailure  = "start_failure"
	projectAssistantConversationPageSize      = 500
	projectAssistantConversationCheckpointV1  = 1
	projectAssistantConversationSummaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"
)

// projectAssistantInterruptedContinuationPrompt is the server-owned content
// used by the Continue action when the user supplies no additional request.
// It gives the model an explicit recovery instruction while the durable
// interruption conversation item supplies the boundary and prior tool state.
const projectAssistantInterruptedContinuationPrompt = "Continue the interrupted turn from its durable history. Inspect the current workspace before acting, preserve completed effects, and do not replay an in-flight effect solely because the prior turn was interrupted."

// projectAssistantInterruptedBoundaryMessage is deliberately model-visible.
// The run status alone is UI metadata; the next Chat Completions request must
// receive the same kind of boundary Codex records in its rollout.
const projectAssistantInterruptedBoundaryMessage = "<turn_aborted>\nThe previous assistant turn was interrupted on purpose. Any running workspace tools may have partially executed. Completed tool effects remain authoritative. Inspect the current workspace before continuing and do not replay an effect solely because the prior turn was interrupted.\n</turn_aborted>"

func appendProjectAssistantInterruptedBoundary(ctx context.Context, messageStore store.Store, scope store.Scope, run store.AssistantRun) error {
	err := appendProjectAssistantConversationMessage(
		ctx,
		messageStore,
		scope,
		run.ID,
		"interruption-"+strings.TrimSpace(run.ID),
		projectAssistantConversationInterruption,
		chatMessage{Role: "system", Content: projectAssistantInterruptedBoundaryMessage},
	)
	// Older interrupted runs may already contain the pre-Codex marker under
	// this deterministic item ID. Preserve that durable evidence rather than
	// turning restart reconciliation into a persistence failure.
	if errors.Is(err, store.ErrAssistantConversationItemConflict) {
		return nil
	}
	return err
}

type projectAssistantConversationCompactionCheckpoint struct {
	Version                         int           `json:"version"`
	ReplacementHistory              []chatMessage `json:"replacementHistory"`
	Summary                         string        `json:"summary"`
	TriggerID                       string        `json:"triggerID"`
	WindowNumber                    uint64        `json:"windowNumber"`
	FirstWindowID                   string        `json:"firstWindowID"`
	PreviousWindowID                string        `json:"previousWindowID,omitempty"`
	WindowID                        string        `json:"windowID"`
	PriorHistoryTokenEstimate       int           `json:"priorHistoryTokenEstimate"`
	ReplacementHistoryTokenEstimate int           `json:"replacementHistoryTokenEstimate"`
	// CompactedThroughSequence is the durable stream tail captured immediately
	// before summary sampling. Items appended after this sequence must survive
	// checkpoint replacement even when they were persisted before the checkpoint
	// item itself (notably concurrently acknowledged steering).
	CompactedThroughSequence int64 `json:"compactedThroughSequence,omitempty"`
}

type projectAssistantConversationProjection struct {
	messages             []chatMessage
	compactionCheckpoint *projectAssistantConversationCompactionCheckpoint
	lastSequence         int64
}

type projectAssistantConversationRolloutBudgetState struct {
	Version int                                `json:"version"`
	State   projectAssistantRolloutBudgetState `json:"state"`
}

type projectAssistantConversationStartFailureMarker struct {
	Version       int    `json:"version"`
	UserMessageID string `json:"userMessageID"`
}

type projectAssistantSequencedConversationMessage struct {
	sequence int64
	itemID   string
	message  chatMessage
}

func projectAssistantConversationForRun(
	projection projectAssistantConversationProjection,
	recent []store.Message,
) ([]chatMessage, bool) {
	checkpointed := projection.compactionCheckpoint != nil
	if checkpointed {
		return normalizeProjectAssistantToolCallPairing(cloneChatMessages(projection.messages)), true
	}
	return normalizeProjectAssistantToolCallPairing(mergeProjectAssistantLegacyConversation(projection.messages, recent)), false
}

// projectAssistantUnsettledToolResult answers a tool call that never settled,
// so an interrupted run still replays as a complete tool group.
const projectAssistantUnsettledToolResult = `{"status":"unavailable","summary":"tool result unavailable: the assistant run was interrupted before this call settled"}`

// normalizeProjectAssistantToolCallPairing rebuilds a replayable tool-call
// structure from the durable item stream. Parallel tool calls are persisted one
// item per call and their results are appended as each call settles, so the
// stream legitimately interleaves a second call between the first call and its
// result. Providers reject that outright — "tool_call_id ... not found in
// 'tool_calls' of previous message" — and because the stream is append-only,
// every later turn on the thread fails the same way. Consecutive calls are
// merged back into the single assistant message the model actually produced,
// each call is answered immediately after it, and results that no longer have a
// call are dropped because they cannot be sent at all.
func normalizeProjectAssistantToolCallPairing(messages []chatMessage) []chatMessage {
	results := make(map[string]chatMessage, len(messages))
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		id := strings.TrimSpace(message.ToolCallID)
		if id == "" {
			continue
		}
		if _, ok := results[id]; !ok {
			results[id] = message
		}
	}

	normalized := make([]chatMessage, 0, len(messages))
	answered := make(map[string]bool, len(results))
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		role := strings.TrimSpace(message.Role)
		if strings.EqualFold(role, "tool") {
			// Results are emitted with the call group they answer.
			continue
		}
		if !strings.EqualFold(role, "assistant") || len(message.ToolCalls) == 0 {
			normalized = append(normalized, message)
			continue
		}
		group := message
		group.ToolCalls = append([]chatToolCall(nil), message.ToolCalls...)
		for index+1 < len(messages) {
			next := messages[index+1]
			if !strings.EqualFold(strings.TrimSpace(next.Role), "assistant") || len(next.ToolCalls) == 0 {
				break
			}
			group.ToolCalls = append(group.ToolCalls, next.ToolCalls...)
			group.Content = joinProjectAssistantToolCallContent(group.Content, next.Content)
			index++
		}
		calls := make([]chatToolCall, 0, len(group.ToolCalls))
		answers := make([]chatMessage, 0, len(group.ToolCalls))
		for _, call := range group.ToolCalls {
			id := strings.TrimSpace(call.ID)
			if id == "" || answered[id] {
				continue
			}
			answered[id] = true
			calls = append(calls, call)
			result, ok := results[id]
			if !ok {
				result = chatMessage{
					Role:       "tool",
					Name:       call.Function.Name,
					ToolCallID: id,
					Content:    projectAssistantUnsettledToolResult,
				}
			}
			answers = append(answers, result)
		}
		if len(calls) == 0 {
			// Nothing answerable survives; keep any prose the turn carried.
			if strings.TrimSpace(group.Content) != "" {
				group.ToolCalls = nil
				normalized = append(normalized, group)
			}
			continue
		}
		group.ToolCalls = calls
		normalized = append(normalized, group)
		normalized = append(normalized, answers...)
	}
	return normalized
}

func joinProjectAssistantToolCallContent(existing, addition string) string {
	if strings.TrimSpace(existing) == "" {
		return addition
	}
	if strings.TrimSpace(addition) == "" {
		return existing
	}
	return existing + "\n\n" + addition
}

func appendProjectAssistantConversationMessage(ctx context.Context, messageStore store.Store, scope store.Scope, runID, itemID, itemType string, message chatMessage) error {
	if messageStore == nil {
		return fmt.Errorf("project message store not configured")
	}
	if strings.TrimSpace(itemID) == "" {
		itemID = "item-" + uuid.NewString()
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode assistant conversation item: %w", err)
	}
	_, err = messageStore.AppendAssistantConversationItem(ctx, scope, store.AssistantConversationItem{
		ID: itemID, RunID: runID, Type: itemType, Payload: payload, CreatedAt: time.Now().UTC(),
	})
	return err
}

func appendProjectAssistantStartFailureMarker(ctx context.Context, messageStore store.Store, scope store.Scope, runID, userMessageID string) error {
	if messageStore == nil {
		return fmt.Errorf("project message store not configured")
	}
	runID = strings.TrimSpace(runID)
	userMessageID = strings.TrimSpace(userMessageID)
	if runID == "" || userMessageID == "" {
		return fmt.Errorf("assistant start-failure marker run and user message IDs are required")
	}
	payload, err := json.Marshal(projectAssistantConversationStartFailureMarker{Version: 1, UserMessageID: userMessageID})
	if err != nil {
		return fmt.Errorf("encode assistant start-failure marker: %w", err)
	}
	_, err = messageStore.AppendAssistantConversationItem(ctx, scope, store.AssistantConversationItem{
		ID:        "start-failure-" + runID,
		RunID:     runID,
		Type:      projectAssistantConversationStartFailure,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
	if errors.Is(err, store.ErrAssistantConversationItemConflict) {
		return nil
	}
	return err
}
func projectAssistantConversationStartFailureMessageMatches(message chatMessage, marker projectAssistantConversationStartFailureMarker) bool {
	return message.conversationItemID == "message-"+strings.TrimSpace(marker.UserMessageID)
}

func projectAssistantConversationRemoveStartFailureMessage(messages []chatMessage, tail []projectAssistantSequencedConversationMessage, marker projectAssistantConversationStartFailureMarker) ([]chatMessage, []projectAssistantSequencedConversationMessage) {
	for index := len(messages) - 1; index >= 0; index-- {
		if !projectAssistantConversationStartFailureMessageMatches(messages[index], marker) {
			continue
		}
		messages = append(messages[:index], messages[index+1:]...)
		break
	}
	for index := len(tail) - 1; index >= 0; index-- {
		if !projectAssistantConversationStartFailureMessageMatches(tail[index].message, marker) {
			continue
		}
		tail = append(tail[:index], tail[index+1:]...)
		break
	}
	return messages, tail
}

func projectAssistantConversationToolResultItemID(runID, callID string) string {
	return "tool-result-" + strings.TrimSpace(runID) + "-" + strings.TrimSpace(callID)
}

func projectAssistantConversationToolCallItemID(runID, callID string, attempt int) string {
	return fmt.Sprintf("tool-call-%s-%s-%d", strings.TrimSpace(runID), strings.TrimSpace(callID), attempt)
}

func appendProjectAssistantConversationRolloutBudgetState(
	ctx context.Context,
	messageStore store.Store,
	scope store.Scope,
	runID string,
	state projectAssistantRolloutBudgetState,
) error {
	payload, err := json.Marshal(projectAssistantConversationRolloutBudgetState{Version: 1, State: state})
	if err != nil {
		return fmt.Errorf("encode assistant rollout budget state: %w", err)
	}
	_, err = messageStore.AppendAssistantConversationItem(ctx, scope, store.AssistantConversationItem{
		ID:        "rollout-budget-state-" + strings.TrimSpace(runID) + "-" + uuid.NewString(),
		RunID:     runID,
		Type:      projectAssistantConversationRolloutBudget,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
	return err
}

// appendProjectAssistantConversationCompactionCheckpoint persists the finalized
// compactor output verbatim. The caller owns replacement selection, window
// identity, and token estimates; this storage boundary must not derive them from
// a separately loaded durable projection.
func appendProjectAssistantConversationCompactionCheckpoint(ctx context.Context, messageStore store.Store, scope store.Scope, runID, itemID string, checkpoint projectAssistantConversationCompactionCheckpoint) error {
	if messageStore == nil {
		return fmt.Errorf("project message store not configured")
	}
	if strings.TrimSpace(itemID) == "" {
		return fmt.Errorf("assistant conversation compaction item ID is required")
	}
	if err := validateProjectAssistantConversationCompactionCheckpoint(checkpoint); err != nil {
		return err
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode assistant conversation compaction checkpoint: %w", err)
	}
	_, err = messageStore.AppendAssistantConversationItem(ctx, scope, store.AssistantConversationItem{
		ID: itemID, RunID: runID, Type: projectAssistantConversationCompaction, Payload: payload, CreatedAt: time.Now().UTC(),
	})
	return err
}

func validateProjectAssistantConversationCompactionCheckpoint(checkpoint projectAssistantConversationCompactionCheckpoint) error {
	if checkpoint.Version != projectAssistantConversationCheckpointV1 {
		return fmt.Errorf("unsupported assistant conversation compaction checkpoint version %d", checkpoint.Version)
	}
	if checkpoint.ReplacementHistory == nil {
		return fmt.Errorf("assistant conversation compaction replacement history is required")
	}
	if strings.TrimSpace(checkpoint.TriggerID) == "" {
		return fmt.Errorf("assistant conversation compaction trigger ID is required")
	}
	if checkpoint.WindowNumber == 0 {
		return fmt.Errorf("assistant conversation compaction window number is required")
	}
	if strings.TrimSpace(checkpoint.FirstWindowID) == "" {
		return fmt.Errorf("assistant conversation compaction first window ID is required")
	}
	if strings.TrimSpace(checkpoint.WindowID) == "" {
		return fmt.Errorf("assistant conversation compaction window ID is required")
	}
	if checkpoint.PriorHistoryTokenEstimate < 0 || checkpoint.ReplacementHistoryTokenEstimate < 0 {
		return fmt.Errorf("assistant conversation compaction token estimates cannot be negative")
	}
	return nil
}

func loadProjectAssistantConversation(ctx context.Context, messageStore store.Store, scope store.Scope) ([]chatMessage, error) {
	projection, err := loadProjectAssistantConversationProjection(ctx, messageStore, scope)
	if err != nil {
		return nil, err
	}
	return projection.messages, nil
}

func loadProjectAssistantConversationProjection(ctx context.Context, messageStore store.Store, scope store.Scope) (projectAssistantConversationProjection, error) {
	projection := projectAssistantConversationProjection{
		messages: make([]chatMessage, 0, projectAssistantConversationPageSize),
	}
	after := int64(0)
	tailSinceCheckpoint := make([]projectAssistantSequencedConversationMessage, 0)
	failedStartMarkers := make([]projectAssistantConversationStartFailureMarker, 0)
	scrubFailedStartMessages := func() {
		for _, marker := range failedStartMarkers {
			projection.messages, tailSinceCheckpoint = projectAssistantConversationRemoveStartFailureMessage(
				projection.messages,
				tailSinceCheckpoint,
				marker,
			)
		}
	}
	for {
		page, err := messageStore.ListAssistantConversationItems(ctx, scope, after, projectAssistantConversationPageSize)
		if err != nil {
			return projectAssistantConversationProjection{}, err
		}
		for _, item := range page {
			projection.lastSequence = item.Sequence
			if item.Type == projectAssistantConversationCompaction {
				var envelope map[string]json.RawMessage
				if err := json.Unmarshal(item.Payload, &envelope); err != nil {
					return projectAssistantConversationProjection{}, fmt.Errorf("decode assistant conversation compaction item %q: %w", item.ID, err)
				}
				versioned := false
				for key := range envelope {
					if strings.EqualFold(key, "version") {
						versioned = true
						break
					}
				}
				if versioned {
					var checkpoint projectAssistantConversationCompactionCheckpoint
					if err := json.Unmarshal(item.Payload, &checkpoint); err != nil {
						return projectAssistantConversationProjection{}, fmt.Errorf("decode versioned assistant conversation compaction checkpoint %q: %w", item.ID, err)
					}
					if err := validateProjectAssistantConversationCompactionCheckpoint(checkpoint); err != nil {
						return projectAssistantConversationProjection{}, fmt.Errorf("invalid assistant conversation compaction checkpoint %q: %w", item.ID, err)
					}
					preserved := make([]chatMessage, 0)
					preservedTail := make([]projectAssistantSequencedConversationMessage, 0)
					if checkpoint.CompactedThroughSequence > 0 {
						for _, candidate := range tailSinceCheckpoint {
							if candidate.sequence <= checkpoint.CompactedThroughSequence {
								continue
							}
							preserved = append(preserved, candidate.message)
							preservedTail = append(preservedTail, candidate)
						}
					}
					projection.messages = append(cloneChatMessages(checkpoint.ReplacementHistory), cloneChatMessages(preserved)...)
					tailSinceCheckpoint = preservedTail
					scrubFailedStartMessages()
					checkpointCopy := checkpoint
					checkpointCopy.ReplacementHistory = cloneChatMessages(checkpoint.ReplacementHistory)
					projection.compactionCheckpoint = &checkpointCopy
					continue
				}
				var legacy chatMessage
				if err := json.Unmarshal(item.Payload, &legacy); err != nil {
					return projectAssistantConversationProjection{}, fmt.Errorf("decode legacy assistant conversation compaction item %q: %w", item.ID, err)
				}
				summary := projectAssistantConversationSummaryText(legacy.Content)
				if strings.TrimSpace(legacy.Role) == "" || summary == "" {
					return projectAssistantConversationProjection{}, fmt.Errorf("invalid legacy assistant conversation compaction item %q", item.ID)
				}
				projection.messages = []chatMessage{projectAssistantConversationSummaryMessage(summary)}
				projection.compactionCheckpoint = nil
				tailSinceCheckpoint = nil
				scrubFailedStartMessages()
				continue
			}
			if item.Type == projectAssistantConversationStartFailure {
				var marker projectAssistantConversationStartFailureMarker
				if err := json.Unmarshal(item.Payload, &marker); err != nil ||
					marker.Version != 1 || strings.TrimSpace(marker.UserMessageID) == "" {
					return projectAssistantConversationProjection{}, fmt.Errorf("invalid assistant conversation start-failure marker %q", item.ID)
				}
				failedStartMarkers = append(failedStartMarkers, marker)
				scrubFailedStartMessages()
				continue
			}
			// Rollout budgets are run-scoped. Preserve their factual item in the
			// append-only stream without leaking a stale remainder into a later run.
			if item.Type == projectAssistantConversationRolloutBudget {
				continue
			}
			var message chatMessage
			if json.Unmarshal(item.Payload, &message) != nil || strings.TrimSpace(message.Role) == "" {
				continue
			}
			message.conversationItemID = item.ID
			projection.messages = append(projection.messages, message)
			tailSinceCheckpoint = append(tailSinceCheckpoint, projectAssistantSequencedConversationMessage{sequence: item.Sequence, itemID: item.ID, message: message})
		}
		if len(page) < projectAssistantConversationPageSize {
			break
		}
		after = page[len(page)-1].Sequence
	}
	return projection, nil
}

func projectAssistantConversationSummaryText(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimSpace(strings.TrimPrefix(content, "Compacted conversation context:"))
	content = strings.TrimSpace(strings.TrimPrefix(content, projectAssistantConversationSummaryPrefix))
	return content
}

func projectAssistantConversationSummaryMessage(summary string) chatMessage {
	return chatMessage{Role: "user", Content: projectAssistantConversationSummaryPrefix + "\n" + strings.TrimSpace(summary)}
}

// mergeProjectAssistantLegacyConversation keeps pre-cutover prose available
// while the append-only response-item stream becomes authoritative. It only
// supplies user/assistant messages absent from the stream; tool evidence and
// compaction order always come from the durable conversation items.
func mergeProjectAssistantLegacyConversation(conversation []chatMessage, recent []store.Message) []chatMessage {
	seen := make(map[string]int, len(conversation))
	for _, message := range conversation {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		seen[message.Role+"\x00"+message.Content]++
	}
	legacy := make([]chatMessage, 0, len(recent))
	for _, message := range recent {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		key := role + "\x00" + message.Content
		if seen[key] > 0 {
			seen[key]--
			continue
		}
		legacy = append(legacy, chatMessage{Role: role, Content: message.Content})
	}
	return append(legacy, cloneChatMessages(conversation)...)
}
