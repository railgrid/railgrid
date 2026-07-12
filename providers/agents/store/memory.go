// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is a non-durable in-process Store for development and tests. It
// is the fallback when no database URL is configured; production uses Postgres.
type MemoryStore struct {
	mu        sync.Mutex
	messages  map[string][]Message  // key: scope|session
	runs      map[string]Run        // key: scope|runID
	memories  map[string]Memory     // key: scope|memoryID
	inbox     map[string]InboxItem  // key: scope|itemID
	toolCalls map[string][]ToolCall // key: scope
	usage     map[string]Usage      // key: scope|agent|windowStart
	tenants   map[string]TenantRef  // key: clusterID
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		messages:  map[string][]Message{},
		runs:      map[string]Run{},
		memories:  map[string]Memory{},
		inbox:     map[string]InboxItem{},
		toolCalls: map[string][]ToolCall{},
		usage:     map[string]Usage{},
		tenants:   map[string]TenantRef{},
	}
}

func (m *MemoryStore) SaveTenantRef(_ context.Context, clusterID string, ref TenantRef) error {
	if clusterID == "" {
		return fmt.Errorf("cluster ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tenants[clusterID] = ref
	return nil
}

func (m *MemoryStore) GetTenantRef(_ context.Context, clusterID string) (TenantRef, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ref, ok := m.tenants[clusterID]
	return ref, ok, nil
}

func (m *MemoryStore) EnsureSchema(context.Context) error { return nil }
func (m *MemoryStore) Close() error                       { return nil }

func tenantKey(s Scope) string { return s.OrgUUID + "|" + s.WorkspaceUUID }
func sessionKey(s Scope, session string) string {
	return tenantKey(s) + "|" + s.AgentName + "|" + session
}

func (m *MemoryStore) AppendMessage(_ context.Context, scope Scope, msg Message) error {
	if err := scope.withAgent(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := sessionKey(scope, msg.SessionID)
	if msg.CreatedAt.IsZero() {
		return fmt.Errorf("message CreatedAt is required")
	}
	m.messages[k] = append(m.messages[k], msg)
	return nil
}

func (m *MemoryStore) ListMessages(_ context.Context, scope Scope, sessionID string, limit int, cursor string) (Page, error) {
	if err := scope.withAgent(); err != nil {
		return Page{}, err
	}
	before, beforeID, err := decodeCursor(cursor)
	if err != nil {
		return Page{}, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all := append([]Message(nil), m.messages[sessionKey(scope, sessionID)]...)
	// Newest first for cursor pagination.
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	out := make([]Message, 0, limit)
	for _, msg := range all {
		if !before.IsZero() {
			if msg.CreatedAt.After(before) || (msg.CreatedAt.Equal(before) && msg.ID >= beforeID) {
				continue
			}
		}
		out = append(out, msg)
		if len(out) == limit {
			break
		}
	}
	page := Page{Items: out}
	if len(out) == limit {
		last := out[len(out)-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func (m *MemoryStore) LoadRecentMessages(_ context.Context, scope Scope, sessionID string, limit int) ([]Message, error) {
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all := append([]Message(nil), m.messages[sessionKey(scope, sessionID)]...)
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID < all[j].ID
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

func (m *MemoryStore) SaveRun(_ context.Context, scope Scope, run Run) error {
	if err := scope.withAgent(); err != nil {
		return err
	}
	if run.ID == "" {
		return fmt.Errorf("run ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[tenantKey(scope)+"|"+run.ID] = run
	return nil
}

func (m *MemoryStore) GetRun(_ context.Context, scope Scope, id string) (Run, error) {
	if err := scope.validate(); err != nil {
		return Run{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[tenantKey(scope)+"|"+id]
	if !ok {
		return Run{}, fmt.Errorf("run %q not found", id)
	}
	return run, nil
}

func (m *MemoryStore) ClaimRun(_ context.Context, scope Scope, id, requestID string, now time.Time) (Run, error) {
	if err := scope.validate(); err != nil {
		return Run{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := tenantKey(scope) + "|" + id
	run, ok := m.runs[k]
	if !ok {
		return Run{}, fmt.Errorf("run %q not found", id)
	}
	if run.Phase == RunPhaseRunning {
		return Run{}, fmt.Errorf("run %q already claimed", id)
	}
	run.Phase = RunPhaseRunning
	run.UpdatedAt = now.UTC()
	if run.StartedAt == nil {
		t := now.UTC()
		run.StartedAt = &t
	}
	m.runs[k] = run
	return run, nil
}

func (m *MemoryStore) ListRuns(_ context.Context, scope Scope, limit int) ([]Run, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := tenantKey(scope) + "|"
	var out []Run
	for k, run := range m.runs {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if scope.AgentName != "" && run.AgentName != scope.AgentName {
				continue
			}
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemoryStore) PutMemory(_ context.Context, scope Scope, mem Memory) error {
	if err := scope.withAgent(); err != nil {
		return err
	}
	if mem.ID == "" {
		return fmt.Errorf("memory ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.memories[tenantKey(scope)+"|"+mem.ID] = mem
	return nil
}

func (m *MemoryStore) ListMemories(_ context.Context, scope Scope, limit int) ([]Memory, error) {
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := tenantKey(scope) + "|"
	var out []Memory
	for k, mem := range m.memories {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix && mem.AgentName == scope.AgentName {
			out = append(out, mem)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemoryStore) DeleteMemory(_ context.Context, scope Scope, id string) error {
	if err := scope.withAgent(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.memories, tenantKey(scope)+"|"+id)
	return nil
}

func (m *MemoryStore) AddInboxItem(_ context.Context, scope Scope, item InboxItem) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if item.ID == "" {
		return fmt.Errorf("inbox item ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inbox[tenantKey(scope)+"|"+item.ID] = item
	return nil
}

func (m *MemoryStore) ListInbox(_ context.Context, scope Scope, state InboxItemState) ([]InboxItem, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := tenantKey(scope) + "|"
	var out []InboxItem
	for k, it := range m.inbox {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if state != "" && it.State != state {
				continue
			}
			if scope.AgentName != "" && it.AgentName != scope.AgentName {
				continue
			}
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) ResolveInboxItem(_ context.Context, scope Scope, id string, state InboxItemState, response string, now time.Time) (InboxItem, error) {
	if err := scope.validate(); err != nil {
		return InboxItem{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := tenantKey(scope) + "|" + id
	it, ok := m.inbox[k]
	if !ok {
		return InboxItem{}, fmt.Errorf("inbox item %q not found", id)
	}
	it.State = state
	it.Response = response
	it.UpdatedAt = now.UTC()
	m.inbox[k] = it
	return it, nil
}

func (m *MemoryStore) AppendToolCall(_ context.Context, scope Scope, tc ToolCall) error {
	if err := scope.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := tenantKey(scope)
	m.toolCalls[k] = append(m.toolCalls[k], tc)
	return nil
}

func (m *MemoryStore) AddUsage(_ context.Context, scope Scope, agentName string, in, out, usdMicros int64, now time.Time, window time.Duration) (Usage, error) {
	if err := scope.validate(); err != nil {
		return Usage{}, err
	}
	ws := windowStart(now, window)
	m.mu.Lock()
	defer m.mu.Unlock()
	k := fmt.Sprintf("%s|%s|%d", tenantKey(scope), agentName, ws.Unix())
	u := m.usage[k]
	u.AgentName = agentName
	u.WindowStart = ws
	u.InputTokens += in
	u.OutputTokens += out
	u.USDMicros += usdMicros
	u.UpdatedAt = now.UTC()
	m.usage[k] = u
	return u, nil
}

func (m *MemoryStore) GetUsage(_ context.Context, scope Scope, agentName string, now time.Time, window time.Duration) (Usage, error) {
	if err := scope.validate(); err != nil {
		return Usage{}, err
	}
	ws := windowStart(now, window)
	m.mu.Lock()
	defer m.mu.Unlock()
	k := fmt.Sprintf("%s|%s|%d", tenantKey(scope), agentName, ws.Unix())
	u, ok := m.usage[k]
	if !ok {
		return Usage{AgentName: agentName, WindowStart: ws}, nil
	}
	return u, nil
}

func (m *MemoryStore) DeleteAgentData(_ context.Context, scope Scope, agentName string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tk := tenantKey(scope)
	msgPrefix := tk + "|" + agentName + "|"
	for k := range m.messages {
		if len(k) >= len(msgPrefix) && k[:len(msgPrefix)] == msgPrefix {
			delete(m.messages, k)
		}
	}
	for k, run := range m.runs {
		if run.AgentName == agentName && hasPrefix(k, tk+"|") {
			delete(m.runs, k)
		}
	}
	for k, mem := range m.memories {
		if mem.AgentName == agentName && hasPrefix(k, tk+"|") {
			delete(m.memories, k)
		}
	}
	for k, it := range m.inbox {
		if it.AgentName == agentName && hasPrefix(k, tk+"|") {
			delete(m.inbox, k)
		}
	}
	return nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
