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
	projectAssistantConversationPageSize      = 500
	projectAssistantConversationCheckpointV1  = 1
	projectAssistantConversationSummaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"
)

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

type projectAssistantSequencedConversationMessage struct {
	sequence int64
	message  chatMessage
}

func projectAssistantConversationForRun(
	projection projectAssistantConversationProjection,
	recent []store.Message,
) ([]chatMessage, bool) {
	checkpointed := projection.compactionCheckpoint != nil
	if checkpointed {
		return cloneChatMessages(projection.messages), true
	}
	return mergeProjectAssistantLegacyConversation(projection.messages, recent), false
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

func loadProjectAssistantConversationRolloutBudgetState(
	ctx context.Context,
	messageStore store.Store,
	scope store.Scope,
) (*projectAssistantRolloutBudgetState, error) {
	if messageStore == nil {
		return nil, nil
	}
	var latest *projectAssistantRolloutBudgetState
	after := int64(0)
	for {
		page, err := messageStore.ListAssistantConversationItems(ctx, scope, after, projectAssistantConversationPageSize)
		if err != nil {
			return nil, err
		}
		for _, item := range page {
			if item.Type != projectAssistantConversationRolloutBudget {
				continue
			}
			var envelope projectAssistantConversationRolloutBudgetState
			if json.Unmarshal(item.Payload, &envelope) != nil || envelope.Version != 1 || envelope.State.LimitTokens <= 0 {
				continue
			}
			state := cloneProjectAssistantRolloutBudgetState(envelope.State)
			latest = &state
		}
		if len(page) < projectAssistantConversationPageSize {
			return latest, nil
		}
		after = page[len(page)-1].Sequence
	}
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
			projection.messages = append(projection.messages, message)
			tailSinceCheckpoint = append(tailSinceCheckpoint, projectAssistantSequencedConversationMessage{sequence: item.Sequence, message: message})
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
