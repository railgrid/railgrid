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
	ID          string   `json:"id"`
	AgentName   string   `json:"agentName"`
	SessionID   string   `json:"sessionID,omitempty"`
	Trigger     string   `json:"trigger"`
	ParentRunID string   `json:"parentRunID,omitempty"`
	Phase       RunPhase `json:"phase"`
	Attempt     int      `json:"attempt,omitempty"`
	Input       string   `json:"input,omitempty"`
	// Output is the run's final answer. Kept on the run record (not only as a
	// transcript row) so a programmatic caller — the parent of a spawned worker,
	// or GET /api/runs/{id} — reads the result where it read the phase, instead
	// of having to locate the run's session and dig the last message out of it.
	Output string `json:"output,omitempty"`
	// Sources are the URLs a run reported relying on, parsed from the trailing
	// "Sources:" block a worker is asked to emit. Nil when it named none.
	Sources []string `json:"sources,omitempty"`
	// IdempotencyKey is a caller-supplied de-duplication token, unique per
	// (org, workspace, agent). An at-least-once caller retrying a delivery gets
	// the original run back instead of a second one doing the same work twice.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// Delivery records where this run's answer was headed. Persisted because the
	// goroutine that knew is exactly what a crash destroys: without it, a run that
	// dies mid-flight can never tell the person waiting in a channel that it is
	// not coming, and they wait forever.
	Delivery     *RunDelivery    `json:"delivery,omitempty"`
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

// RunDelivery is where a run's output goes: the connection to answer on, the
// exact chat within it, and the agent-channel role for unattended runs.
type RunDelivery struct {
	// SourceName is the schedule, trigger, or channel connection that started it.
	SourceName string `json:"sourceName,omitempty"`
	// ReplyTarget pins the exact chat inside the source connection (the Discord
	// channel or Telegram chat the message came from), so the answer lands where
	// the question was asked rather than on the connection's default target.
	ReplyTarget string `json:"replyTarget,omitempty"`
	// NotifyChannel is the agent-channel role an unattended run reports to.
	NotifyChannel string `json:"notifyChannel,omitempty"`
	// Kind distinguishes a channel conversation (answer in the chat) from a
	// schedule/trigger (notify the configured channel).
	Kind string `json:"kind,omitempty"`
}

// SessionSummary is a compacted stand-in for the older part of one session's
// transcript. A long-lived session (a channel conversation, a schedule that
// replies turn after turn) eventually exceeds the model's context window; rather
// than truncating and silently forgetting, the provider summarizes everything up
// to ThroughAt and replays the summary in place of those messages.
//
// One row per (scope, session): compacting again folds the previous summary into
// the new one, so the row always describes the whole prefix of the session.
type SessionSummary struct {
	SessionID string `json:"sessionID"`
	Summary   string `json:"summary"`
	// ThroughAt is the CreatedAt of the newest message folded in. Messages at or
	// before it are represented by Summary and are not replayed.
	ThroughAt time.Time `json:"throughAt"`
	// MessageCount is how many messages the summary stands for, for display and
	// for deciding whether compaction is making progress.
	MessageCount int       `json:"messageCount"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
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

// ToolCall is one audit-log entry and the step record behind the run trace
// view. Args and Result are the full payloads (secret-looking JSON values
// redacted, oversized payloads truncated with a marker) so a run's steps can
// be inspected after the fact.
type ToolCall struct {
	ID         string    `json:"id"`
	AgentName  string    `json:"agentName"`
	RunID      string    `json:"runID"`
	Trigger    string    `json:"trigger"`
	Tool       string    `json:"tool"`
	Args       string    `json:"args,omitempty"`
	Result     string    `json:"result,omitempty"`
	Outcome    string    `json:"outcome"` // ok | error | denied | pending_approval
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

// RunQuery filters a run listing. Zero values mean "any". Scope.AgentName (when
// set) narrows to one agent; Cursor/Limit page newest-first.
type RunQuery struct {
	Phase       RunPhase
	Trigger     string
	SessionID   string
	ParentRunID string
	Limit       int
	Cursor      string
}

// RunPage is an ordered slice of runs plus the next cursor.
type RunPage struct {
	Items      []Run  `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// ScopedRun pairs a run with the tenant it belongs to. Only the recovery sweep
// uses it: every other read already knows its scope from the request, but a
// restart has to discover both.
type ScopedRun struct {
	Scope Scope
	Run   Run
}

// Session summarizes one chat thread of an agent: its ID, activity bounds,
// message count, and a short preview taken from the first user message. It
// backs the portal's session picker.
type Session struct {
	ID           string    `json:"id"`
	Preview      string    `json:"preview,omitempty"`
	MessageCount int       `json:"messageCount"`
	CreatedAt    time.Time `json:"createdAt"`
	LastActivity time.Time `json:"lastActivity"`
}

// sessionPreviewMax bounds the preview label length (runes).
const sessionPreviewMax = 80

// previewText normalizes whitespace and rune-truncates a message body into a
// session label.
func previewText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > sessionPreviewMax {
		return string(r[:sessionPreviewMax]) + "…"
	}
	return s
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
	// ListSessions returns the agent's chat sessions, most-recently-active first.
	ListSessions(ctx context.Context, scope Scope, limit int) ([]Session, error)
	// DeleteSession wipes one session's transcript (the "/new" channel command),
	// including any compaction summary for it.
	DeleteSession(ctx context.Context, scope Scope, sessionID string) error

	// Compaction. PutSessionSummary upserts the summary standing in for a
	// session's older messages; GetSessionSummary reports ok=false when the
	// session has never been compacted.
	PutSessionSummary(ctx context.Context, scope Scope, s SessionSummary) error
	GetSessionSummary(ctx context.Context, scope Scope, sessionID string) (SessionSummary, bool, error)

	// Runs (durable, resumable).
	SaveRun(ctx context.Context, scope Scope, run Run) error
	GetRun(ctx context.Context, scope Scope, id string) (Run, error)
	// ClaimRun atomically marks a resumable run as owned by requestID so only
	// one replica resumes it.
	ClaimRun(ctx context.Context, scope Scope, id, requestID string, now time.Time) (Run, error)
	ListRuns(ctx context.Context, scope Scope, limit int) ([]Run, error)
	// QueryRuns lists runs newest-first with filters and cursor pagination.
	QueryRuns(ctx context.Context, scope Scope, q RunQuery) (RunPage, error)
	// FindRunByIdempotencyKey returns the run a caller already started under this
	// key, so a retried request is answered with the original run rather than
	// starting the same work again. Scope must name the agent.
	FindRunByIdempotencyKey(ctx context.Context, scope Scope, key string) (Run, bool, error)

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
	// ListToolCalls returns one run's tool calls in execution order.
	ListToolCalls(ctx context.Context, scope Scope, runID string) ([]ToolCall, error)
	AddUsage(ctx context.Context, scope Scope, agentName string, in, out, usdMicros int64, now time.Time, window time.Duration) (Usage, error)
	GetUsage(ctx context.Context, scope Scope, agentName string, now time.Time, window time.Duration) (Usage, error)

	// Tenant mapping (cluster ID → org/workspace scope) for background runs.
	SaveTenantRef(ctx context.Context, clusterID string, ref TenantRef) error
	GetTenantRef(ctx context.Context, clusterID string) (TenantRef, bool, error)
	// FindClusterForScope is the reverse mapping: which logical cluster backs
	// this org/workspace. The recovery sweep needs it because a stored run knows
	// its scope but not the cluster whose virtual workspace can resume it.
	FindClusterForScope(ctx context.Context, orgUUID, workspaceUUID string) (string, bool, error)

	// Recovery. ListUnfinishedRuns returns runs left in a non-terminal phase
	// across EVERY tenant — the one query that deliberately ignores Scope,
	// because a restart has to find work it has no request context for. Ordered
	// oldest-first so the longest-stranded run is handled first.
	ListUnfinishedRuns(ctx context.Context, phases []RunPhase, updatedBefore time.Time, limit int) ([]ScopedRun, error)

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
