// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/faroshq/provider-vibe-studio/session"
)

// MemoryStore is the in-memory Store for dev and tests. Safe for concurrent
// use; data does not survive a restart.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]map[string]*memorySession // tenant → session id → session
}

type memorySession struct {
	record   SessionRecord
	events   []session.Event
	files    map[string]string // path → content
	revision int64             // bumped on every file mutation
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: map[string]map[string]*memorySession{}}
}

func (m *MemoryStore) EnsureSchema(context.Context) error { return nil }
func (m *MemoryStore) Close() error                       { return nil }

func (m *MemoryStore) CreateSession(_ context.Context, scope Scope, id, preview string, now time.Time) error {
	if err := scope.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tenant := m.sessions[scope.Tenant]
	if tenant == nil {
		tenant = map[string]*memorySession{}
		m.sessions[scope.Tenant] = tenant
	}
	if _, exists := tenant[id]; exists {
		return session.ErrConflict
	}
	tenant[id] = &memorySession{record: SessionRecord{
		ID: id, Preview: preview, Phase: session.PhaseIntake,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}}
	return nil
}

func (m *MemoryStore) AppendEvents(_ context.Context, scope Scope, sessionID string, expectedLast int64, events []session.Event) (int64, error) {
	if err := scope.validate(); err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return expectedLast, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[scope.Tenant][sessionID]
	if s == nil {
		return 0, ErrNotFound
	}
	last := int64(0)
	if n := len(s.events); n > 0 {
		last = s.events[n-1].Ordinal
	}
	if last != expectedLast {
		return 0, ErrOrdinalConflict
	}
	for i := range events {
		e := events[i]
		e.SessionID = sessionID
		e.Ordinal = last + int64(i) + 1
		s.events = append(s.events, e)
	}
	return s.events[len(s.events)-1].Ordinal, nil
}

func (m *MemoryStore) ListEvents(_ context.Context, scope Scope, sessionID string, after int64, limit int) ([]session.Event, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[scope.Tenant][sessionID]
	if s == nil {
		return nil, ErrNotFound
	}
	var out []session.Event
	for _, e := range s.events {
		if e.Ordinal > after {
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MemoryStore) ListSessions(_ context.Context, scope Scope, limit int) ([]SessionRecord, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []SessionRecord
	for _, s := range m.sessions[scope.Tenant] {
		out = append(out, s.record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// session returns the tenant's session or nil.
func (m *MemoryStore) session(scope Scope, sessionID string) *memorySession {
	return m.sessions[scope.Tenant][sessionID]
}

func (m *MemoryStore) PutWorkspaceFiles(_ context.Context, scope Scope, sessionID string, files []WorkspaceFile, _ time.Time) error {
	if err := scope.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(scope, sessionID)
	if s == nil {
		return ErrNotFound
	}
	if s.files == nil {
		s.files = map[string]string{}
	}
	for _, f := range files {
		s.files[f.Path] = f.Content
	}
	s.revision++
	return nil
}

func (m *MemoryStore) GetWorkspaceFile(_ context.Context, scope Scope, sessionID, path string) (WorkspaceFile, error) {
	if err := scope.validate(); err != nil {
		return WorkspaceFile{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(scope, sessionID)
	if s == nil {
		return WorkspaceFile{}, ErrNotFound
	}
	content, ok := s.files[path]
	if !ok {
		return WorkspaceFile{}, ErrNotFound
	}
	return WorkspaceFile{Path: path, Content: content}, nil
}

func (m *MemoryStore) ListWorkspaceFiles(_ context.Context, scope Scope, sessionID string) ([]string, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(scope, sessionID)
	if s == nil {
		return nil, ErrNotFound
	}
	paths := make([]string, 0, len(s.files))
	for p := range s.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

func (m *MemoryStore) ListWorkspaceContents(_ context.Context, scope Scope, sessionID string) ([]WorkspaceFile, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(scope, sessionID)
	if s == nil {
		return nil, ErrNotFound
	}
	out := make([]WorkspaceFile, 0, len(s.files))
	for p, c := range s.files {
		out = append(out, WorkspaceFile{Path: p, Content: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (m *MemoryStore) DeleteWorkspaceFile(_ context.Context, scope Scope, sessionID, path string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(scope, sessionID)
	if s == nil {
		return ErrNotFound
	}
	delete(s.files, path)
	s.revision++
	return nil
}

func (m *MemoryStore) WorkspaceRevision(_ context.Context, scope Scope, sessionID string) (int64, error) {
	if err := scope.validate(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(scope, sessionID)
	if s == nil {
		return 0, ErrNotFound
	}
	return s.revision, nil
}

func (m *MemoryStore) PurgeSession(_ context.Context, scope Scope, sessionID string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions[scope.Tenant], sessionID)
	return nil
}

func (m *MemoryStore) TouchSession(_ context.Context, scope Scope, sessionID string, phase session.Phase, now time.Time) error {
	if err := scope.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[scope.Tenant][sessionID]
	if s == nil {
		return ErrNotFound
	}
	s.record.Phase = phase
	s.record.UpdatedAt = now.UTC()
	return nil
}
