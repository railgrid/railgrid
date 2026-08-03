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

// Package store persists App Studio project messages.
package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrAssistantRunNotFound = errors.New("assistant run not found")
var ErrAssistantRunConflict = errors.New("assistant run version conflict")
var ErrAssistantRunEventConflict = errors.New("assistant run event sequence conflict")
var ErrAssistantConversationItemConflict = errors.New("assistant conversation item conflict")
var ErrAssistantApprovalModeInvalid = errors.New("assistant approval mode is invalid")
var ErrProjectBootstrapPermitConflict = errors.New("project bootstrap permit conflict")

// Scope identifies a tenant/project boundary. Every query must include all
// three fields to keep App Studio data isolated per org/workspace/project.
type Scope struct {
	OrgUUID       string
	WorkspaceUUID string
	ProjectName   string
	ProjectUID    string
}

func (s Scope) validate() error {
	if strings.TrimSpace(s.OrgUUID) == "" || strings.TrimSpace(s.WorkspaceUUID) == "" || strings.TrimSpace(s.ProjectName) == "" || strings.TrimSpace(s.ProjectUID) == "" {
		return fmt.Errorf("scope is incomplete")
	}
	return nil
}

// Message is the persisted chat transcript record.
type Message struct {
	ID               string         `json:"id"`
	ProjectName      string         `json:"projectName,omitempty"`
	ProjectUID       string         `json:"projectUID,omitempty"`
	ActorID          string         `json:"actorID,omitempty"`
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ContentEncrypted bool           `json:"contentEncrypted,omitempty"`
	ContentKeyID     string         `json:"contentKeyID,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

type AssistantRunStatus string

type AssistantRunAbortReason string

const (
	AssistantRunAbortReasonInterrupted      AssistantRunAbortReason = "interrupted"
	AssistantRunAbortReasonReplaced         AssistantRunAbortReason = "replaced"
	AssistantRunAbortReasonBudgetLimited    AssistantRunAbortReason = "budget_limited"
	AssistantRunAbortReasonIterationLimited AssistantRunAbortReason = "iteration_limited"
)

// AssistantRunMode is derived by the server from the user-selected action.
type AssistantRunMode string

// AssistantApprovalMode controls whether registered assistant actions stop for
// an explicit user decision. It never bypasses tool validation or authorization.
type AssistantApprovalMode string

const (
	AssistantRunModeDefault AssistantRunMode = "default"
	AssistantRunModePlan    AssistantRunMode = "plan"
	AssistantRunModeReview  AssistantRunMode = "review"
)

const (
	AssistantApprovalModeOnRequest AssistantApprovalMode = "on_request"
	AssistantApprovalModeAlwaysAsk AssistantApprovalMode = "always_ask"
	AssistantApprovalModeNever     AssistantApprovalMode = "never"
	// AssistantApprovalModeAutoApprove is accepted only while the legacy run
	// API remains during the Thread/Turn cutover. New Turns never persist it.
	AssistantApprovalModeAutoApprove AssistantApprovalMode = "auto_approve"
)

// AssistantApprovalPreference is scoped to one authenticated actor and Project.
type AssistantApprovalPreference struct {
	ActorID   string                `json:"-"`
	Mode      AssistantApprovalMode `json:"mode"`
	UpdatedAt time.Time             `json:"updatedAt,omitempty"`
}

const (
	AssistantRunStatusPendingPermission AssistantRunStatus = "pending_permission"
	AssistantRunStatusPendingInput      AssistantRunStatus = "pending_input"
	AssistantRunStatusRunning           AssistantRunStatus = "running"
	AssistantRunStatusStopping          AssistantRunStatus = "stopping"
	AssistantRunStatusCompleted         AssistantRunStatus = "completed"
	AssistantRunStatusFailed            AssistantRunStatus = "failed"
	AssistantRunStatusInterrupted       AssistantRunStatus = "interrupted"
	// AssistantRunStatusAborted is read only for rows written before the Codex
	// terminal-state cutover. New transitions use Failed or Interrupted.
	AssistantRunStatusAborted AssistantRunStatus = "aborted"
)

// AssistantRun stores resumable assistant execution state. Checkpoint is an
// App Studio API-owned JSON payload so store implementations do not need to
// know private chat/tool types.
type AssistantRun struct {
	ID              string                  `json:"id"`
	ProjectName     string                  `json:"projectName,omitempty"`
	ProjectUID      string                  `json:"projectUID,omitempty"`
	Mode            AssistantRunMode        `json:"mode,omitempty"`
	ApprovalMode    AssistantApprovalMode   `json:"approvalMode"`
	Status          AssistantRunStatus      `json:"status"`
	ClientRequestID string                  `json:"clientRequestID,omitempty"`
	UserMessageID   string                  `json:"userMessageID,omitempty"`
	ActiveMessageID string                  `json:"activeMessageID,omitempty"`
	Revision        int64                   `json:"revision,omitempty"`
	RequestID       string                  `json:"requestID,omitempty"`
	Checkpoint      json.RawMessage         `json:"checkpoint,omitempty"`
	Audit           json.RawMessage         `json:"audit,omitempty"`
	Error           json.RawMessage         `json:"error,omitempty"`
	AbortReason     AssistantRunAbortReason `json:"abortReason,omitempty"`
	CreatedAt       time.Time               `json:"createdAt"`
	UpdatedAt       time.Time               `json:"updatedAt"`
}

// AssistantRunEvent is one immutable entry in a run's durable execution log.
// Sequence is scoped by the tenant/project/run key and must increase by one.
// Payload remains an opaque JSON document owned by the assistant engine.
type AssistantRunEvent struct {
	RunID       string          `json:"runID"`
	ProjectName string          `json:"projectName,omitempty"`
	ProjectUID  string          `json:"projectUID,omitempty"`
	Sequence    int64           `json:"sequence"`
	Type        string          `json:"type"`
	CallID      string          `json:"callID,omitempty"`
	ToolName    string          `json:"toolName,omitempty"`
	ArgsDigest  string          `json:"argsDigest,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// AssistantConversationItem is one encrypted, append-only response item used
// to reconstruct model context without discarding tool evidence.
type AssistantConversationItem struct {
	ID          string          `json:"id"`
	RunID       string          `json:"runID"`
	ProjectName string          `json:"projectName,omitempty"`
	ProjectUID  string          `json:"projectUID,omitempty"`
	Sequence    int64           `json:"sequence"`
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// Page is an ordered slice of messages plus the next cursor for pagination.
type Page struct {
	Items      []Message `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

// Store is the App Studio message persistence boundary.
type Store interface {
	EnsureSchema(ctx context.Context) error
	CreateAssistantThread(ctx context.Context, scope Scope, thread AssistantThread, events []AssistantThreadEvent) (AssistantThread, error)
	GetAssistantThread(ctx context.Context, scope Scope, threadID string) (AssistantThread, error)
	ListAssistantThreads(ctx context.Context, scope Scope, actorID string, includeArchived bool, limit int, cursor string) (AssistantThreadPage, error)
	UpdateAssistantThread(ctx context.Context, scope Scope, thread AssistantThread) (AssistantThread, error)
	// UpdateAssistantThreadWithEvent commits a thread projection change and its
	// canonical event in one store transaction. expectedSequence protects the
	// append-only stream from a concurrent writer.
	UpdateAssistantThreadWithEvent(ctx context.Context, scope Scope, thread AssistantThread, event AssistantThreadEvent, expectedSequence int64) (AssistantThread, AssistantThreadEvent, error)
	CreateAssistantTurn(ctx context.Context, scope Scope, turn AssistantTurn, events []AssistantThreadEvent) (AssistantTurn, error)
	GetAssistantTurn(ctx context.Context, scope Scope, threadID, turnID string) (AssistantTurn, error)
	FindAssistantTurnByClientUserMessageID(ctx context.Context, scope Scope, threadID, clientUserMessageID string) (AssistantTurn, error)
	ActiveAssistantTurn(ctx context.Context, scope Scope, threadID string) (AssistantTurn, error)
	SaveAssistantTurn(ctx context.Context, scope Scope, turn AssistantTurn) error
	SaveAssistantTurnWithEvent(ctx context.Context, scope Scope, turn AssistantTurn, event AssistantThreadEvent, expectedSequence int64) error
	AppendAssistantThreadEvent(ctx context.Context, scope Scope, event AssistantThreadEvent, expectedSequence int64) (AssistantThreadEvent, error)
	ListAssistantThreadEvents(ctx context.Context, scope Scope, threadID string, afterSequence int64, limit int) ([]AssistantThreadEvent, error)
	AppendMessage(ctx context.Context, scope Scope, msg Message) error
	ListMessages(ctx context.Context, scope Scope, limit int, cursor string) (Page, error)
	LoadRecentMessages(ctx context.Context, scope Scope, limit int) ([]Message, error)
	GetAssistantApprovalPreference(ctx context.Context, scope Scope, actor string) (AssistantApprovalPreference, error)
	SetAssistantApprovalPreference(ctx context.Context, scope Scope, preference AssistantApprovalPreference) (AssistantApprovalPreference, error)
	CreateProjectBootstrapPermit(ctx context.Context, scope Scope, actor, promptDigest string) error
	ConsumeProjectBootstrapPermit(ctx context.Context, scope Scope, actor, promptDigest, clientRequestID string, now time.Time) (bool, error)
	SaveAssistantRun(ctx context.Context, scope Scope, run AssistantRun) error
	CreateAssistantRun(ctx context.Context, scope Scope, user Message, assistant Message, run AssistantRun) (AssistantRun, error)
	RequestAssistantRunStop(ctx context.Context, scope Scope, runID string, expectedRunRevision int64, now time.Time) (AssistantRun, error)
	RequestAssistantRunStopWithAssistantMessage(ctx context.Context, scope Scope, runID string, expectedRunRevision int64, assistant Message, now time.Time) (AssistantRun, error)
	SaveAssistantRunSnapshot(ctx context.Context, scope Scope, run AssistantRun, messages []Message, expectedRevision int64) error
	ClaimAssistantRun(ctx context.Context, scope Scope, id string, requestID string, now time.Time) (AssistantRun, error)
	GetAssistantRun(ctx context.Context, scope Scope, id string) (AssistantRun, error)
	FindAssistantRunByClientRequestID(ctx context.Context, scope Scope, clientRequestID string) (AssistantRun, error)
	LatestAssistantRun(ctx context.Context, scope Scope) (AssistantRun, error)
	AppendAssistantRunEvent(ctx context.Context, scope Scope, event AssistantRunEvent, expectedSequence int64) (AssistantRunEvent, error)
	ListAssistantRunEvents(ctx context.Context, scope Scope, runID string, afterSequence int64, limit int) ([]AssistantRunEvent, error)
	AppendAssistantConversationItem(ctx context.Context, scope Scope, item AssistantConversationItem) (AssistantConversationItem, error)
	ListAssistantConversationItems(ctx context.Context, scope Scope, afterSequence int64, limit int) ([]AssistantConversationItem, error)
	DeleteProjectMessages(ctx context.Context, scope Scope) error
	DeleteMessagesOlderThan(ctx context.Context, before time.Time) (int64, error)
}

func prepareAssistantConversationItem(scope Scope, item AssistantConversationItem) (AssistantConversationItem, error) {
	if err := scope.validate(); err != nil {
		return AssistantConversationItem{}, err
	}
	item.ID = strings.TrimSpace(item.ID)
	item.RunID = strings.TrimSpace(item.RunID)
	item.Type = strings.TrimSpace(item.Type)
	if item.ID == "" || item.RunID == "" || item.Type == "" {
		return AssistantConversationItem{}, errors.New("assistant conversation item id, run id, and type are required")
	}
	item.ProjectName, item.ProjectUID = scope.ProjectName, scope.ProjectUID
	if len(item.Payload) == 0 {
		item.Payload = json.RawMessage(`{}`)
	}
	if !json.Valid(item.Payload) {
		return AssistantConversationItem{}, errors.New("assistant conversation item payload must be valid json")
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	} else {
		item.CreatedAt = item.CreatedAt.UTC()
	}
	return item, nil
}

// assistantConversationItemsMatch defines an idempotent append. An item ID is
// only reusable for the same run, type, and semantic JSON payload; accepting a
// mismatched replay would silently splice one run's evidence into another.
func assistantConversationItemsMatch(existing, candidate AssistantConversationItem) bool {
	return strings.TrimSpace(existing.RunID) == strings.TrimSpace(candidate.RunID) &&
		strings.TrimSpace(existing.Type) == strings.TrimSpace(candidate.Type) &&
		assistantConversationPayloadEqual(existing.Payload, candidate.Payload)
}

func assistantConversationPayloadEqual(left, right json.RawMessage) bool {
	leftCanonical, leftErr := canonicalAssistantConversationPayload(left)
	rightCanonical, rightErr := canonicalAssistantConversationPayload(right)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}

func canonicalAssistantConversationPayload(payload json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("assistant conversation payload has trailing value")
	}
	return json.Marshal(value)
}

// NormalizeAssistantApprovalMode validates a persisted or API-supplied mode.
// The user-facing default is supplied by GetAssistantApprovalPreference.
func NormalizeAssistantApprovalMode(mode AssistantApprovalMode) (AssistantApprovalMode, error) {
	mode = AssistantApprovalMode(strings.ToLower(strings.TrimSpace(string(mode))))
	if mode == "" {
		return AssistantApprovalModeOnRequest, nil
	}
	switch mode {
	case AssistantApprovalModeOnRequest, AssistantApprovalModeAlwaysAsk, AssistantApprovalModeNever, AssistantApprovalModeAutoApprove:
		return mode, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrAssistantApprovalModeInvalid, mode)
	}
}

func assistantRunStatusTerminal(status AssistantRunStatus) bool {
	switch status {
	case AssistantRunStatusCompleted, AssistantRunStatusFailed, AssistantRunStatusInterrupted, AssistantRunStatusAborted:
		return true
	default:
		return false
	}
}

func prepareAssistantRunEvent(scope Scope, event AssistantRunEvent, expectedSequence int64) (AssistantRunEvent, error) {
	if err := scope.validate(); err != nil {
		return AssistantRunEvent{}, err
	}
	event.RunID = strings.TrimSpace(event.RunID)
	event.Type = strings.TrimSpace(event.Type)
	event.CallID = strings.TrimSpace(event.CallID)
	event.ToolName = strings.TrimSpace(event.ToolName)
	event.ArgsDigest = strings.TrimSpace(event.ArgsDigest)
	if event.RunID == "" || event.Type == "" {
		return AssistantRunEvent{}, fmt.Errorf("assistant run event run id and type are required")
	}
	if expectedSequence < 0 || expectedSequence == 1<<63-1 {
		return AssistantRunEvent{}, fmt.Errorf("%w: invalid expected sequence %d", ErrAssistantRunEventConflict, expectedSequence)
	}
	nextSequence := expectedSequence + 1
	if event.Sequence != 0 && event.Sequence != nextSequence {
		return AssistantRunEvent{}, fmt.Errorf("%w: event sequence must advance from %d to %d", ErrAssistantRunEventConflict, expectedSequence, nextSequence)
	}
	event.Sequence = nextSequence
	if len(event.Payload) == 0 {
		event.Payload = json.RawMessage(`{}`)
	}
	if !json.Valid(event.Payload) {
		return AssistantRunEvent{}, fmt.Errorf("assistant run event payload is not valid json")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	} else {
		event.CreatedAt = event.CreatedAt.UTC()
	}
	event.ProjectName = scope.ProjectName
	event.ProjectUID = scope.ProjectUID
	event.Payload = cloneRawMessage(event.Payload)
	return event, nil
}

func validateAssistantRunEventList(scope Scope, runID string, afterSequence int64) (string, error) {
	if err := scope.validate(); err != nil {
		return "", err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "", fmt.Errorf("assistant run event run id is required")
	}
	if afterSequence < 0 {
		return "", fmt.Errorf("%w: invalid after sequence %d", ErrAssistantRunEventConflict, afterSequence)
	}
	return runID, nil
}

type cursorPayload struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

func encodeCursor(createdAt time.Time, id string) string {
	payload, _ := json.Marshal(cursorPayload{CreatedAt: createdAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeCursor(raw string) (time.Time, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, "", nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("decode cursor: %w", err)
	}
	var cur cursorPayload
	if err := json.Unmarshal(payload, &cur); err != nil {
		return time.Time{}, "", fmt.Errorf("decode cursor json: %w", err)
	}
	if cur.CreatedAt.IsZero() || strings.TrimSpace(cur.ID) == "" {
		return time.Time{}, "", fmt.Errorf("cursor is missing createdAt or id")
	}
	return cur.CreatedAt.UTC(), cur.ID, nil
}
