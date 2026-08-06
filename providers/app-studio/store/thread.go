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

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrAssistantThreadNotFound = errors.New("assistant thread not found")
var ErrAssistantThreadConflict = errors.New("assistant thread conflict")
var ErrAssistantThreadActive = errors.New("assistant thread has an active turn")
var ErrAssistantTurnNotFound = errors.New("assistant turn not found")
var ErrAssistantTurnConflict = errors.New("assistant turn conflict")
var ErrAssistantThreadEventConflict = errors.New("assistant thread event conflict")

type AssistantThreadStatus string

const (
	AssistantThreadStatusIdle     AssistantThreadStatus = "idle"
	AssistantThreadStatusActive   AssistantThreadStatus = "active"
	AssistantThreadStatusArchived AssistantThreadStatus = "archived"
)

// AssistantThread is a conversation targeting one Project workspace. Its
// status and timestamps are materialized projections of the canonical event
// stream, not a second source of transcript truth.
type AssistantThread struct {
	ID        string                `json:"id"`
	Title     string                `json:"title,omitempty"`
	Status    AssistantThreadStatus `json:"status"`
	ActorID   string                `json:"actorID,omitempty"`
	CreatedAt time.Time             `json:"createdAt"`
	UpdatedAt time.Time             `json:"updatedAt"`
}

type AssistantTurnStatus string

const (
	AssistantTurnStatusInProgress  AssistantTurnStatus = "in_progress"
	AssistantTurnStatusCompleted   AssistantTurnStatus = "completed"
	AssistantTurnStatusInterrupted AssistantTurnStatus = "interrupted"
	AssistantTurnStatusFailed      AssistantTurnStatus = "failed"
)

type AssistantTurn struct {
	ID                  string                `json:"id"`
	ThreadID            string                `json:"threadID"`
	ActorID             string                `json:"actorID,omitempty"`
	ClientUserMessageID string                `json:"clientUserMessageID,omitempty"`
	Mode                AssistantRunMode      `json:"mode"`
	ApprovalMode        AssistantApprovalMode `json:"approvalMode"`
	Status              AssistantTurnStatus   `json:"status"`
	Checkpoint          json.RawMessage       `json:"checkpoint,omitempty"`
	Error               json.RawMessage       `json:"error,omitempty"`
	CreatedAt           time.Time             `json:"createdAt"`
	UpdatedAt           time.Time             `json:"updatedAt"`
}

// AssistantThreadEvent is the one append-only authority for conversation,
// lifecycle, tool, plan, approval, input, and compaction state.
type AssistantThreadEvent struct {
	ThreadID  string          `json:"threadID"`
	TurnID    string          `json:"turnID,omitempty"`
	Sequence  int64           `json:"sequence"`
	Type      string          `json:"type"`
	ItemID    string          `json:"itemID,omitempty"`
	RequestID string          `json:"requestID,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

type AssistantThreadPage struct {
	Items      []AssistantThread `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func prepareAssistantThread(thread AssistantThread) (AssistantThread, error) {
	thread.ID = strings.TrimSpace(thread.ID)
	thread.Title = strings.TrimSpace(thread.Title)
	thread.ActorID = strings.TrimSpace(thread.ActorID)
	if thread.ID == "" || thread.ActorID == "" {
		return AssistantThread{}, errors.New("assistant thread id and actor are required")
	}
	if thread.Status == "" {
		thread.Status = AssistantThreadStatusIdle
	}
	switch thread.Status {
	case AssistantThreadStatusIdle, AssistantThreadStatusActive, AssistantThreadStatusArchived:
	default:
		return AssistantThread{}, fmt.Errorf("invalid assistant thread status %q", thread.Status)
	}
	if thread.CreatedAt.IsZero() {
		thread.CreatedAt = time.Now().UTC()
	} else {
		thread.CreatedAt = thread.CreatedAt.UTC()
	}
	if thread.UpdatedAt.IsZero() {
		thread.UpdatedAt = thread.CreatedAt
	} else {
		thread.UpdatedAt = thread.UpdatedAt.UTC()
	}
	return thread, nil
}

func prepareAssistantTurn(turn AssistantTurn) (AssistantTurn, error) {
	turn.ID = strings.TrimSpace(turn.ID)
	turn.ThreadID = strings.TrimSpace(turn.ThreadID)
	turn.ActorID = strings.TrimSpace(turn.ActorID)
	turn.ClientUserMessageID = strings.TrimSpace(turn.ClientUserMessageID)
	if turn.ID == "" || turn.ThreadID == "" || turn.ActorID == "" || turn.ClientUserMessageID == "" {
		return AssistantTurn{}, errors.New("assistant turn id, thread id, actor, and client user message id are required")
	}
	if turn.Mode == "" {
		turn.Mode = AssistantRunModeDefault
	}
	if !assistantRunModeValid(turn.Mode) {
		return AssistantTurn{}, fmt.Errorf("invalid assistant turn mode %q", turn.Mode)
	}
	mode, err := NormalizeAssistantApprovalMode(turn.ApprovalMode)
	if err != nil {
		return AssistantTurn{}, err
	}
	turn.ApprovalMode = mode
	if turn.Status == "" {
		turn.Status = AssistantTurnStatusInProgress
	}
	if !assistantTurnStatusValid(turn.Status) {
		return AssistantTurn{}, fmt.Errorf("invalid assistant turn status %q", turn.Status)
	}
	if turn.CreatedAt.IsZero() {
		turn.CreatedAt = time.Now().UTC()
	} else {
		turn.CreatedAt = turn.CreatedAt.UTC()
	}
	if turn.UpdatedAt.IsZero() {
		turn.UpdatedAt = turn.CreatedAt
	} else {
		turn.UpdatedAt = turn.UpdatedAt.UTC()
	}
	if len(turn.Checkpoint) == 0 {
		turn.Checkpoint = json.RawMessage(`{}`)
	}
	if len(turn.Error) == 0 {
		turn.Error = json.RawMessage(`{}`)
	}
	if !json.Valid(turn.Checkpoint) || !json.Valid(turn.Error) {
		return AssistantTurn{}, errors.New("assistant turn checkpoint and error must be valid json")
	}
	return turn, nil
}

func prepareAssistantThreadEvent(event AssistantThreadEvent) (AssistantThreadEvent, error) {
	event.ThreadID = strings.TrimSpace(event.ThreadID)
	event.TurnID = strings.TrimSpace(event.TurnID)
	event.Type = strings.TrimSpace(event.Type)
	event.ItemID = strings.TrimSpace(event.ItemID)
	event.RequestID = strings.TrimSpace(event.RequestID)
	if event.ThreadID == "" || event.Type == "" {
		return AssistantThreadEvent{}, errors.New("assistant thread event thread id and type are required")
	}
	if len(event.Payload) == 0 {
		event.Payload = json.RawMessage(`{}`)
	}
	if !json.Valid(event.Payload) {
		return AssistantThreadEvent{}, errors.New("assistant thread event payload must be valid json")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	} else {
		event.CreatedAt = event.CreatedAt.UTC()
	}
	return event, nil
}

func assistantTurnStatusValid(status AssistantTurnStatus) bool {
	switch status {
	case AssistantTurnStatusInProgress, AssistantTurnStatusCompleted, AssistantTurnStatusInterrupted, AssistantTurnStatusFailed:
		return true
	default:
		return false
	}
}

func assistantTurnStatusTerminal(status AssistantTurnStatus) bool {
	return status == AssistantTurnStatusCompleted || status == AssistantTurnStatusInterrupted || status == AssistantTurnStatusFailed
}
