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
var ErrAssistantWorkItemNotFound = errors.New("assistant work item not found")
var ErrAssistantWorkItemConflict = errors.New("assistant work item conflict")
var ErrAssistantApprovalModeInvalid = errors.New("assistant approval mode is invalid")

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
	WorkItemID       string         `json:"workItemID,omitempty"`
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ContentEncrypted bool           `json:"contentEncrypted,omitempty"`
	ContentKeyID     string         `json:"contentKeyID,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

type AssistantRunStatus string

// AssistantRunMode is derived by the server from the user-selected action.
type AssistantRunMode string

// AssistantApprovalMode controls whether registered assistant actions stop for
// an explicit user decision. It never bypasses tool validation or authorization.
type AssistantApprovalMode string

const (
	// AssistantRunIDApprovedPlanGrant is retained only so API code can be moved
	// to WorkItem grants in a later task. Store implementations no longer
	// reserve or special-case this value.
	// Deprecated: use AssistantWorkItem.PlanGrant.
	AssistantRunIDApprovedPlanGrant = "approved-plan-grant"

	AssistantRunModeDiscussion AssistantRunMode = "discussion"
	AssistantRunModeAdaptive   AssistantRunMode = "adaptive"
	AssistantRunModeNew        AssistantRunMode = "new"
	AssistantRunModeContinue   AssistantRunMode = "continue"
)

const (
	AssistantApprovalModeAlwaysAsk   AssistantApprovalMode = "always_ask"
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
	AssistantRunStatusAborted           AssistantRunStatus = "aborted"
	AssistantRunStatusFailed            AssistantRunStatus = "failed"
	AssistantRunStatusInterrupted       AssistantRunStatus = "interrupted"
)

// AssistantRun stores resumable assistant execution state. Checkpoint is an
// App Studio API-owned JSON payload so store implementations do not need to
// know private chat/tool types.
type AssistantRun struct {
	ID                    string                `json:"id"`
	ProjectName           string                `json:"projectName,omitempty"`
	ProjectUID            string                `json:"projectUID,omitempty"`
	WorkItemID            string                `json:"workItemID,omitempty"`
	Mode                  AssistantRunMode      `json:"mode,omitempty"`
	ApprovalMode          AssistantApprovalMode `json:"approvalMode"`
	ExpectedGrantRevision string                `json:"expectedGrantRevision,omitempty"`
	Status                AssistantRunStatus    `json:"status"`
	ClientRequestID       string                `json:"clientRequestID,omitempty"`
	UserMessageID         string                `json:"userMessageID,omitempty"`
	ActiveMessageID       string                `json:"activeMessageID,omitempty"`
	Revision              int64                 `json:"revision,omitempty"`
	RequestID             string                `json:"requestID,omitempty"`
	Checkpoint            json.RawMessage       `json:"checkpoint,omitempty"`
	Audit                 json.RawMessage       `json:"audit,omitempty"`
	CreatedAt             time.Time             `json:"createdAt"`
	UpdatedAt             time.Time             `json:"updatedAt"`
}

// AssistantRunIsConversation reports whether a run is user-visible. Every
// durable run is now a conversation run; grants live on AssistantWorkItem.
func AssistantRunIsConversation(run AssistantRun) bool {
	return run.ID != ""
}

type AssistantWorkItemStatus string

const (
	AssistantWorkItemStatusActive    AssistantWorkItemStatus = "active"
	AssistantWorkItemStatusSuspended AssistantWorkItemStatus = "suspended"
	AssistantWorkItemStatusCompleted AssistantWorkItemStatus = "completed"
	AssistantWorkItemStatusCancelled AssistantWorkItemStatus = "cancelled"
)

// AssistantWorkItem is the durable owner of a user-selected mutation task and
// its cross-run plan grant.
type AssistantWorkItem struct {
	ID                    string                  `json:"id"`
	ProjectName           string                  `json:"projectName,omitempty"`
	ProjectUID            string                  `json:"projectUID,omitempty"`
	RootMessageID         string                  `json:"rootMessageID"`
	CreatedBy             string                  `json:"createdBy"`
	Status                AssistantWorkItemStatus `json:"status"`
	StatusReason          string                  `json:"statusReason,omitempty"`
	Revision              int64                   `json:"revision"`
	ActiveRunID           string                  `json:"activeRunID,omitempty"`
	PlanGrant             json.RawMessage         `json:"planGrant,omitempty"`
	GrantRevision         string                  `json:"grantRevision,omitempty"`
	ExecutionPlan         json.RawMessage         `json:"executionPlan,omitempty"`
	ExecutionPlanRevision string                  `json:"executionPlanRevision,omitempty"`
	CreatedAt             time.Time               `json:"createdAt"`
	UpdatedAt             time.Time               `json:"updatedAt"`
}

// Page is an ordered slice of messages plus the next cursor for pagination.
type Page struct {
	Items      []Message `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

// Store is the App Studio message persistence boundary.
type Store interface {
	EnsureSchema(ctx context.Context) error
	AppendMessage(ctx context.Context, scope Scope, msg Message) error
	ListMessages(ctx context.Context, scope Scope, limit int, cursor string) (Page, error)
	LoadRecentMessages(ctx context.Context, scope Scope, limit int) ([]Message, error)
	GetAssistantApprovalPreference(ctx context.Context, scope Scope, actor string) (AssistantApprovalPreference, error)
	SetAssistantApprovalPreference(ctx context.Context, scope Scope, preference AssistantApprovalPreference) (AssistantApprovalPreference, error)
	SaveAssistantRun(ctx context.Context, scope Scope, run AssistantRun) error
	CreateAssistantRun(ctx context.Context, scope Scope, user Message, assistant Message, run AssistantRun) (AssistantRun, error)
	CreateWorkItemAndAssistantRun(ctx context.Context, scope Scope, item AssistantWorkItem, user Message, assistant Message, run AssistantRun) (AssistantWorkItem, error)
	PromoteAssistantRunToWorkItem(ctx context.Context, scope Scope, runID, actor, workItemID string, expectedRunRevision int64, now time.Time) (AssistantWorkItem, AssistantRun, error)
	ResumeWorkItemAndCreateAssistantRun(ctx context.Context, scope Scope, workItemID, actor string, expectedRevision int64, user Message, assistant Message, run AssistantRun) (AssistantWorkItem, error)
	GetAssistantWorkItem(ctx context.Context, scope Scope, id string) (AssistantWorkItem, error)
	ListAssistantWorkItems(ctx context.Context, scope Scope) ([]AssistantWorkItem, error)
	CompareAndSwapAssistantWorkItem(ctx context.Context, scope Scope, item AssistantWorkItem, expectedRevision int64) error
	SaveWorkItemExecutionPlan(ctx context.Context, scope Scope, workItemID, runID string, expectedRevision int64, executionPlanRevision string, executionPlan json.RawMessage, now time.Time) (AssistantWorkItem, error)
	ApproveWorkItemPlan(ctx context.Context, scope Scope, workItemID, runID string, expectedRevision int64, grantRevision string, planGrant json.RawMessage, now time.Time) (AssistantWorkItem, error)
	RetireWorkItemPlan(ctx context.Context, scope Scope, workItemID, runID, actor string, expectedWorkItemRevision int64, expectedGrantRevision, tombstoneGrantRevision string, now time.Time) (AssistantWorkItem, error)
	RequestAssistantRunStop(ctx context.Context, scope Scope, workItemID, runID string, expectedWorkItemRevision, expectedRunRevision int64, now time.Time) (AssistantRun, error)
	TransitionWorkItemAndRun(ctx context.Context, scope Scope, workItemID string, expectedWorkItemRevision int64, run AssistantRun, status AssistantWorkItemStatus, reason string, now time.Time) error
	LoadMessagesForWorkItem(ctx context.Context, scope Scope, workItemID string, limit int) ([]Message, error)
	LatestAssistantRunForWorkItem(ctx context.Context, scope Scope, workItemID string) (AssistantRun, error)
	SaveAssistantRunSnapshot(ctx context.Context, scope Scope, run AssistantRun, messages []Message, expectedRevision int64) error
	CompareAndSwapAssistantRun(ctx context.Context, scope Scope, run AssistantRun, expectedRequestID string) error
	ClaimAssistantRun(ctx context.Context, scope Scope, id string, requestID string, now time.Time) (AssistantRun, error)
	GetAssistantRun(ctx context.Context, scope Scope, id string) (AssistantRun, error)
	FindAssistantRunByClientRequestID(ctx context.Context, scope Scope, clientRequestID string) (AssistantRun, error)
	LatestAssistantRun(ctx context.Context, scope Scope) (AssistantRun, error)
	DeleteProjectMessages(ctx context.Context, scope Scope) error
	DeleteMessagesOlderThan(ctx context.Context, before time.Time) (int64, error)
}

// NormalizeAssistantApprovalMode validates a persisted or API-supplied mode.
// Empty values safely fall back to Always ask.
func NormalizeAssistantApprovalMode(mode AssistantApprovalMode) (AssistantApprovalMode, error) {
	mode = AssistantApprovalMode(strings.ToLower(strings.TrimSpace(string(mode))))
	if mode == "" {
		return AssistantApprovalModeAlwaysAsk, nil
	}
	switch mode {
	case AssistantApprovalModeAlwaysAsk, AssistantApprovalModeAutoApprove:
		return mode, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrAssistantApprovalModeInvalid, mode)
	}
}

func assistantRunStatusTerminal(status AssistantRunStatus) bool {
	switch status {
	case AssistantRunStatusCompleted, AssistantRunStatusAborted, AssistantRunStatusFailed, AssistantRunStatusInterrupted:
		return true
	default:
		return false
	}
}

// assistantWorkItemTerminalTransitionValid keeps WorkItem lifecycle ownership
// explicit: a successful run completes its item; an aborted, failed, or
// interrupted run suspends it for a later explicit continuation. Cancellation
// is reserved for user discard, which does not use this terminal run path.
func assistantWorkItemTerminalTransitionValid(itemStatus AssistantWorkItemStatus, runStatus AssistantRunStatus) bool {
	switch itemStatus {
	case AssistantWorkItemStatusCompleted:
		return runStatus == AssistantRunStatusCompleted
	case AssistantWorkItemStatusSuspended:
		return runStatus == AssistantRunStatusAborted || runStatus == AssistantRunStatusFailed || runStatus == AssistantRunStatusInterrupted
	default:
		return false
	}
}

func validateAssistantRunPromotionRequest(runID, actor, workItemID string, expectedRunRevision int64) error {
	if strings.TrimSpace(runID) == "" || expectedRunRevision < 1 {
		return fmt.Errorf("%w: assistant run and revision are required", ErrAssistantRunConflict)
	}
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(workItemID) == "" {
		return fmt.Errorf("%w: actor and work item are required", ErrAssistantWorkItemConflict)
	}
	return nil
}

func newPromotedAssistantWorkItem(run AssistantRun, actor, workItemID string, now time.Time) AssistantWorkItem {
	createdAt := run.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	return AssistantWorkItem{
		ID:            workItemID,
		RootMessageID: run.UserMessageID,
		CreatedBy:     actor,
		Status:        AssistantWorkItemStatusActive,
		Revision:      1,
		ActiveRunID:   run.ID,
		CreatedAt:     createdAt.UTC(),
		UpdatedAt:     now.UTC(),
	}
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
