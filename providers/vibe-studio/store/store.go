// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Package store is the vibe-studio persistence boundary: the append-only
// session event log plus session listings. The event log is the source of
// truth (docs/vibe-studio-design.md §4.2); session state is a fold over it.
// Implementations: Postgres (production) and in-memory (dev/tests). Appends
// carry an expected-last-ordinal so concurrent writers conflict instead of
// interleaving — the optimistic-CAS analog of the agents provider's run claim.
package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/faroshq/provider-vibe-studio/session"
)

// Scope isolates all data to one tenant boundary. Tenant is the hub-verified
// tenant identifier (X-Faros-Tenant); every query includes it.
type Scope struct {
	Tenant string
}

func (s Scope) validate() error {
	if strings.TrimSpace(s.Tenant) == "" {
		return errors.New("scope is incomplete: tenant is required")
	}
	return nil
}

// SessionRecord is one session's listing row. State is derived (fold of the
// log) — the log stays authoritative.
type SessionRecord struct {
	ID        string        `json:"id"`
	Preview   string        `json:"preview,omitempty"`
	Phase     session.Phase `json:"phase"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

// ErrOrdinalConflict reports a concurrent append: the caller's fold is stale.
// Refold from the log and retry the command.
var ErrOrdinalConflict = errors.New("event ordinal conflict: session advanced concurrently")

// ErrNotFound reports an unknown session.
var ErrNotFound = errors.New("session not found")

// Store is the vibe-studio persistence boundary.
type Store interface {
	EnsureSchema(ctx context.Context) error

	// CreateSession registers a session row. The caller appends the
	// session.created event separately (same transaction is not required —
	// a session row without events folds to the zero state and is invisible
	// to NextAction).
	CreateSession(ctx context.Context, scope Scope, id, preview string, now time.Time) error

	// AppendEvents appends events atomically iff the log's current last
	// ordinal equals expectedLast; returns the assigned ordinals' new last.
	// Returns ErrOrdinalConflict on a stale expectedLast.
	AppendEvents(ctx context.Context, scope Scope, sessionID string, expectedLast int64, events []session.Event) (int64, error)

	// ListEvents returns a session's events with ordinal > after, ascending.
	// limit <= 0 means no limit.
	ListEvents(ctx context.Context, scope Scope, sessionID string, after int64, limit int) ([]session.Event, error)

	// ListSessions returns the tenant's sessions, most recently active first.
	ListSessions(ctx context.Context, scope Scope, limit int) ([]SessionRecord, error)

	// TouchSession updates the listing row's derived phase + activity time.
	TouchSession(ctx context.Context, scope Scope, sessionID string, phase session.Phase, now time.Time) error

	// Workspace files: the session's editable source tree (seeded from the
	// scaffold, mutated by the engine's file tools, pushed to the sandbox via
	// dev_sync). Paths are workspace-relative, content UTF-8 text.
	PutWorkspaceFiles(ctx context.Context, scope Scope, sessionID string, files []WorkspaceFile, now time.Time) error
	GetWorkspaceFile(ctx context.Context, scope Scope, sessionID, path string) (WorkspaceFile, error)
	ListWorkspaceFiles(ctx context.Context, scope Scope, sessionID string) ([]string, error)
	// ListWorkspaceContents returns every file with content (sync + commits).
	ListWorkspaceContents(ctx context.Context, scope Scope, sessionID string) ([]WorkspaceFile, error)
	DeleteWorkspaceFile(ctx context.Context, scope Scope, sessionID, path string) error

	// WorkspaceRevision is a monotonic counter bumped on every workspace
	// mutation. The Session reconciler compares it against the revision it
	// last committed to git: desired vs observed, converge — no change
	// feed, no hooks, and a burst of edits collapses into one commit.
	WorkspaceRevision(ctx context.Context, scope Scope, sessionID string) (int64, error)

	// PurgeSession removes EVERYTHING the store holds for one session —
	// events, workspace files, the listing row. Called by the Session
	// reconciler's finalizer when the Session CR is deleted. Idempotent:
	// purging an unknown session is not an error.
	PurgeSession(ctx context.Context, scope Scope, sessionID string) error

	Close() error
}

// WorkspaceFile is one workspace-relative text file.
type WorkspaceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// MaxWorkspaceFileBytes bounds one file; MaxWorkspaceFiles bounds a session
// tree (matches the code provider's commit-bundle caps).
const (
	MaxWorkspaceFileBytes = 256 << 10
	MaxWorkspaceFiles     = 500
)

// previewMax bounds the listing preview label length (runes).
const previewMax = 80

// Preview normalizes whitespace and rune-truncates intent text into a session
// listing label.
func Preview(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > previewMax {
		return string(r[:previewMax]) + "…"
	}
	return s
}
