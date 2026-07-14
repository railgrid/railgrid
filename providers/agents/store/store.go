// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Package store is the agents provider's durable persistence boundary. It owns
// chat transcripts, resumable run checkpoints, long-term memory notes, the
// scheduler/trigger working sets, the cross-agent approvals inbox, OAuth token
// state, usage accounting, and the tool-call audit log. The provider's only
// hard dependency beyond the hub is a Store backend (Postgres in production,
// in-memory for dev). Spec lives in the tenant workspace as CRs; this owns
// state.
package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Scope isolates all data to one tenant + agent boundary. Every query includes
// org and workspace; AgentName narrows to a single agent where relevant.
type Scope struct {
	OrgUUID       string
	WorkspaceUUID string
	AgentName     string
}

func (s Scope) validate() error {
	if strings.TrimSpace(s.OrgUUID) == "" || strings.TrimSpace(s.WorkspaceUUID) == "" {
		return fmt.Errorf("scope is incomplete: org and workspace are required")
	}
	return nil
}

func (s Scope) withAgent() error {
	if err := s.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(s.AgentName) == "" {
		return fmt.Errorf("scope is incomplete: agent name is required")
	}
	return nil
}

// Message is a persisted transcript record. Content may be encrypted at rest.
type Message struct {
	ID               string         `json:"id"`
	AgentName        string         `json:"agentName,omitempty"`
	SessionID        string         `json:"sessionID,omitempty"`
	RunID            string         `json:"runID,omitempty"`
	Role             string         `json:"role"` // user | assistant | tool | system
	Content          string         `json:"content"`
	ContentEncrypted bool           `json:"contentEncrypted,omitempty"`
	ContentKeyID     string         `json:"contentKeyID,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
}

// RunPhase mirrors the Run status phases the store tracks for resume.
type RunPhase string

const (
	RunPhasePending         RunPhase = "Pending"
	RunPhaseRunning         RunPhase = "Running"
	RunPhasePendingApproval RunPhase = "PendingApproval"
	RunPhaseSucceeded       RunPhase = "Succeeded"
	RunPhaseFailed          RunPhase = "Failed"
	RunPhaseAborted         RunPhase = "Aborted"
)

// Run is the durable execution record. Checkpoint is an opaque engine-owned
// JSON payload (Eino interrupt/resume state) so the store needs no knowledge of
// chat/tool types. ParentRunID links sub-agent runs for delegation lineage.
type Run struct {
	ID           string          `json:"id"`
	AgentName    string          `json:"agentName"`
	SessionID    string          `json:"sessionID,omitempty"`
	Trigger      string          `json:"trigger"`
	ParentRunID  string          `json:"parentRunID,omitempty"`
	Phase        RunPhase        `json:"phase"`
	Attempt      int             `json:"attempt,omitempty"`
	Input        string          `json:"input,omitempty"`
	Message      string          `json:"message,omitempty"`
	Checkpoint   json.RawMessage `json:"checkpoint,omitempty"`
	InputTokens  int64           `json:"inputTokens,omitempty"`
	OutputTokens int64           `json:"outputTokens,omitempty"`
	USDMicros    int64           `json:"usdMicros,omitempty"` // cost in millionths of a USD
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
	StartedAt    *time.Time      `json:"startedAt,omitempty"`
	FinishedAt   *time.Time      `json:"finishedAt,omitempty"`
}

// Memory is a long-term note the agent writes and later recalls.
type Memory struct {
	ID               string    `json:"id"`
	AgentName        string    `json:"agentName"`
	Title            string    `json:"title"`
	Body             string    `json:"body"`
	ContentEncrypted bool      `json:"contentEncrypted,omitempty"`
	ContentKeyID     string    `json:"contentKeyID,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// InboxItemKind distinguishes approval requests from open questions.
type InboxItemKind string

const (
	InboxKindApproval InboxItemKind = "approval"
	InboxKindQuestion InboxItemKind = "question"
)

// InboxItemState is the lifecycle of an inbox item.
type InboxItemState string

const (
	InboxStatePending  InboxItemState = "pending"
	InboxStateApproved InboxItemState = "approved"
	InboxStateDenied   InboxItemState = "denied"
	InboxStateAnswered InboxItemState = "answered"
)

// InboxItem is one pending approval or question, resolvable from the portal or
// a channel. Resolving it resumes the referenced run's checkpoint.
type InboxItem struct {
	ID        string         `json:"id"`
	AgentName string         `json:"agentName"`
	RunID     string         `json:"runID"`
	Kind      InboxItemKind  `json:"kind"`
	State     InboxItemState `json:"state"`
	Prompt    string         `json:"prompt"` // "agent wants to run github: merge PR #42"
	Payload   map[string]any `json:"payload,omitempty"`
	Response  string         `json:"response,omitempty"` // answer text, or approver note
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// ToolCall is one audit-log entry. ArgsDigest is a redacted/hashed summary, not
// raw arguments, so secrets never land in the log.
type ToolCall struct {
	ID         string    `json:"id"`
	AgentName  string    `json:"agentName"`
	RunID      string    `json:"runID"`
	Trigger    string    `json:"trigger"`
	Tool       string    `json:"tool"`
	ArgsDigest string    `json:"argsDigest,omitempty"`
	Outcome    string    `json:"outcome"` // ok | error | denied
	Error      string    `json:"error,omitempty"`
	DurationMS int64     `json:"durationMS,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Usage is a rolling-window accounting row per agent for budget enforcement.
type Usage struct {
	AgentName    string    `json:"agentName"`
	WindowStart  time.Time `json:"windowStart"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	USDMicros    int64     `json:"usdMicros"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Page is an ordered slice of messages plus the next cursor.
type Page struct {
	Items      []Message `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

// TenantRef maps a kcp logical-cluster ID to the org/workspace scope the UI
// reads with. Recorded on every authenticated request; consumed by background
// execution (which only knows the cluster ID from the APIExport virtual
// workspace) so scheduled-run transcripts land in the same scope the portal
// lists.
type TenantRef struct {
	OrgUUID       string    `json:"orgUUID"`
	WorkspaceUUID string    `json:"workspaceUUID"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Store is the agents provider persistence boundary. Implementations: Postgres
// (production) and an in-memory backend (dev/tests).
type Store interface {
	EnsureSchema(ctx context.Context) error

	// Transcript.
	AppendMessage(ctx context.Context, scope Scope, msg Message) error
	ListMessages(ctx context.Context, scope Scope, sessionID string, limit int, cursor string) (Page, error)
	LoadRecentMessages(ctx context.Context, scope Scope, sessionID string, limit int) ([]Message, error)
	// DeleteSession wipes one session's transcript (the "/new" channel command).
	DeleteSession(ctx context.Context, scope Scope, sessionID string) error

	// Runs (durable, resumable).
	SaveRun(ctx context.Context, scope Scope, run Run) error
	GetRun(ctx context.Context, scope Scope, id string) (Run, error)
	// ClaimRun atomically marks a resumable run as owned by requestID so only
	// one replica resumes it.
	ClaimRun(ctx context.Context, scope Scope, id, requestID string, now time.Time) (Run, error)
	ListRuns(ctx context.Context, scope Scope, limit int) ([]Run, error)

	// Long-term memory.
	PutMemory(ctx context.Context, scope Scope, m Memory) error
	ListMemories(ctx context.Context, scope Scope, limit int) ([]Memory, error)
	DeleteMemory(ctx context.Context, scope Scope, id string) error

	// Approvals inbox.
	AddInboxItem(ctx context.Context, scope Scope, item InboxItem) error
	ListInbox(ctx context.Context, scope Scope, state InboxItemState) ([]InboxItem, error)
	ResolveInboxItem(ctx context.Context, scope Scope, id string, state InboxItemState, response string, now time.Time) (InboxItem, error)

	// Audit + usage.
	AppendToolCall(ctx context.Context, scope Scope, tc ToolCall) error
	AddUsage(ctx context.Context, scope Scope, agentName string, in, out, usdMicros int64, now time.Time, window time.Duration) (Usage, error)
	GetUsage(ctx context.Context, scope Scope, agentName string, now time.Time, window time.Duration) (Usage, error)

	// Tenant mapping (cluster ID → org/workspace scope) for background runs.
	SaveTenantRef(ctx context.Context, clusterID string, ref TenantRef) error
	GetTenantRef(ctx context.Context, clusterID string) (TenantRef, bool, error)

	// Retention / teardown.
	DeleteAgentData(ctx context.Context, scope Scope, agentName string) error

	Close() error
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

// windowStart truncates now to the start of the rolling window.
func windowStart(now time.Time, window time.Duration) time.Time {
	if window <= 0 {
		window = 30 * 24 * time.Hour
	}
	return now.UTC().Truncate(window)
}
