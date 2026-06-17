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
	"fmt"
	"strings"
	"time"
)

// Scope identifies a tenant/project boundary. Every query must include all
// three fields to keep App Studio data isolated per org/workspace/project.
type Scope struct {
	OrgUUID       string
	WorkspaceUUID string
	ProjectName   string
}

func (s Scope) validate() error {
	if strings.TrimSpace(s.OrgUUID) == "" || strings.TrimSpace(s.WorkspaceUUID) == "" || strings.TrimSpace(s.ProjectName) == "" {
		return fmt.Errorf("scope is incomplete")
	}
	return nil
}

// Message is the persisted chat transcript record.
type Message struct {
	ID               string         `json:"id"`
	ProjectName      string         `json:"projectName,omitempty"`
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ContentEncrypted bool           `json:"contentEncrypted,omitempty"`
	ContentKeyID     string         `json:"contentKeyID,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

type AssistantRunStatus string

const (
	AssistantRunStatusPendingPermission AssistantRunStatus = "pending_permission"
	AssistantRunStatusRunning           AssistantRunStatus = "running"
	AssistantRunStatusCompleted         AssistantRunStatus = "completed"
	AssistantRunStatusAborted           AssistantRunStatus = "aborted"
)

// AssistantRun stores resumable assistant execution state. Checkpoint is an
// App Studio API-owned JSON payload so store implementations do not need to
// know private chat/tool types.
type AssistantRun struct {
	ID          string             `json:"id"`
	ProjectName string             `json:"projectName,omitempty"`
	Status      AssistantRunStatus `json:"status"`
	RequestID   string             `json:"requestID,omitempty"`
	Checkpoint  json.RawMessage    `json:"checkpoint,omitempty"`
	Audit       json.RawMessage    `json:"audit,omitempty"`
	CreatedAt   time.Time          `json:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`
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
	SaveAssistantRun(ctx context.Context, scope Scope, run AssistantRun) error
	ClaimAssistantRun(ctx context.Context, scope Scope, id string, requestID string, now time.Time) (AssistantRun, error)
	GetAssistantRun(ctx context.Context, scope Scope, id string) (AssistantRun, error)
	DeleteProjectMessages(ctx context.Context, scope Scope) error
	DeleteMessagesOlderThan(ctx context.Context, before time.Time) (int64, error)
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
