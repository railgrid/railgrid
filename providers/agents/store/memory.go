// Copyright 2026 The Railgrid Authors.
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
	"strings"
	"sync"
	"time"
)

// MemoryStore is a non-durable in-process Store for development and tests. It
// is the fallback when no database URL is configured; production uses Postgres.
type MemoryStore struct {
	mu                 sync.Mutex
	messages           map[string][]Message      // key: scope|session
	runs               map[string]Run            // key: scope|runID
	memories           map[string]Memory         // key: scope|memoryID
	inbox              map[string]InboxItem      // key: scope|itemID
	toolCalls          map[string][]ToolCall     // key: scope
	usage              map[string]Usage          // key: scope|agent|windowStart
	tenants            map[string]TenantRef      // key: clusterID
	summaries          map[string]SessionSummary // key: scope|session
	harness            map[string]HarnessSession // key: scope|session
	idempotencyPrimary map[string]bool           // key: scope|runID; one stable winner per tenant/agent/key
	messageSequence    int64
	// runScopes remembers each run's scope so ListUnfinishedRuns can report it,
	// mirroring the org/workspace columns the Postgres rows carry.
	runScopes map[string]Scope // key: scope|runID
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		messages:           map[string][]Message{},
		runs:               map[string]Run{},
		memories:           map[string]Memory{},
		inbox:              map[string]InboxItem{},
		toolCalls:          map[string][]ToolCall{},
		usage:              map[string]Usage{},
		tenants:            map[string]TenantRef{},
		summaries:          map[string]SessionSummary{},
		harness:            map[string]HarnessSession{},
		idempotencyPrimary: map[string]bool{},
		runScopes:          map[string]Scope{},
	}
}

func (m *MemoryStore) FindClusterForScope(_ context.Context, orgUUID, workspaceUUID string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for clusterID, ref := range m.tenants {
		if ref.OrgUUID == orgUUID && ref.WorkspaceUUID == workspaceUUID {
			return clusterID, true, nil
		}
	}
	return "", false, nil
}

// normalizeScope resolves a legacy fallback scope after a tenant mapping is
// learned. Mutations recheck the mapping under the write mutex before changing
// state, so a mapping transaction cannot miss a delayed fallback write.
func (m *MemoryStore) normalizeScope(scope Scope) Scope {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.normalizeScopeLocked(scope)
}

// normalizeScopeLocked rechecks the cluster mapping while the caller holds the
// store mutex. Mutations call it after acquiring the same mutex they use for
// the write, closing the gap between an earlier fallback lookup and the write.
func (m *MemoryStore) normalizeScopeLocked(scope Scope) Scope {
	if scope.OrgUUID != UnmappedOrg || scope.WorkspaceUUID == "" {
		return scope
	}
	clusterID := scope.ClusterID
	if clusterID == "" {
		clusterID = scope.WorkspaceUUID
	}
	ref, ok := m.tenants[clusterID]
	if !ok || ref.OrgUUID == "" || ref.WorkspaceUUID == "" {
		return scope
	}
	m.migrateUnmappedScope(clusterID, ref)
	scope.OrgUUID, scope.WorkspaceUUID, scope.ClusterID = ref.OrgUUID, ref.WorkspaceUUID, clusterID
	return scope
}

func (m *MemoryStore) PutSessionSummary(_ context.Context, scope Scope, s SessionSummary) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if strings.TrimSpace(s.SessionID) == "" {
		return fmt.Errorf("session ID is required")
	}
	if err := validateSessionCheckpoint(s.Checkpoint); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	k := sessionKey(scope, s.SessionID)
	if current, ok := m.summaries[k]; ok && current.Checkpoint != nil {
		if s.Checkpoint == nil || s.Checkpoint.ThroughSequence < current.Checkpoint.ThroughSequence {
			return ErrSessionCheckpointStale
		}
	}
	m.summaries[k] = cloneSessionSummary(s)
	return nil
}

func (m *MemoryStore) GetSessionSummary(_ context.Context, scope Scope, sessionID string) (SessionSummary, bool, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return SessionSummary{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.summaries[sessionKey(scope, sessionID)]
	return cloneSessionSummary(s), ok, nil
}

func (m *MemoryStore) FindRunByIdempotencyKey(_ context.Context, scope Scope, key string) (Run, bool, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return Run{}, false, err
	}
	if strings.TrimSpace(key) == "" {
		return Run{}, false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, run := range m.runs {
		if run.IdempotencyKey == key && run.AgentName == scope.AgentName && m.idempotencyPrimary[k] && hasPrefix(k, tenantKey(scope)+"|") {
			return run, true, nil
		}
	}
	return Run{}, false, nil
}

func (m *MemoryStore) SaveTenantRef(_ context.Context, clusterID string, ref TenantRef) error {
	if err := validateTenantRef(clusterID, ref); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.tenants[clusterID]; ok &&
		(current.OrgUUID != ref.OrgUUID || current.WorkspaceUUID != ref.WorkspaceUUID) {
		return fmt.Errorf("tenant mapping for cluster %s is immutable (%s/%s already mapped)", clusterID, current.OrgUUID, current.WorkspaceUUID)
	}
	m.migrateUnmappedScope(clusterID, ref)
	if _, ok := m.tenants[clusterID]; !ok {
		m.tenants[clusterID] = ref
	}
	return nil
}

// migrateUnmappedScope re-keys data written before a caller resolved this
// cluster's tenant identity. It is safe to run more than once: moving removes
// every source key, and merging usage consumes the source row exactly once.
// The caller holds m.mu for the whole operation.
func (m *MemoryStore) migrateUnmappedScope(clusterID string, ref TenantRef) {
	const unmappedOrg = "unmapped"
	fromTenant := unmappedOrg + "|" + clusterID
	toTenant := ref.OrgUUID + "|" + ref.WorkspaceUUID
	if clusterID == "" || ref.OrgUUID == "" || ref.WorkspaceUUID == "" || fromTenant == toTenant {
		return
	}
	fromPrefix, toPrefix := fromTenant+"|", toTenant+"|"
	if !m.hasUnmappedRows(fromTenant, fromPrefix) {
		return
	}
	moveKey := func(key string) string { return toPrefix + strings.TrimPrefix(key, fromPrefix) }

	for key, rows := range m.messages {
		if !hasPrefix(key, fromPrefix) {
			continue
		}
		target := moveKey(key)
		m.messages[target] = append(m.messages[target], rows...)
		delete(m.messages, key)
	}
	for key, run := range m.runs {
		if !hasPrefix(key, fromPrefix) {
			continue
		}
		target := moveKey(key)
		if current, ok := m.runs[target]; ok {
			m.runs[target] = mergeMigratedRun(current, run)
		} else {
			m.runs[target] = run
		}
		scope := m.runScopes[key]
		scope.OrgUUID, scope.WorkspaceUUID, scope.ClusterID = ref.OrgUUID, ref.WorkspaceUUID, clusterID
		m.runScopes[target] = scope
		delete(m.runs, key)
		delete(m.runScopes, key)
		delete(m.idempotencyPrimary, key)
	}
	for key, memory := range m.memories {
		if !hasPrefix(key, fromPrefix) {
			continue
		}
		target := moveKey(key)
		if current, ok := m.memories[target]; !ok || memory.UpdatedAt.After(current.UpdatedAt) {
			m.memories[target] = memory
		}
		delete(m.memories, key)
	}
	for key, item := range m.inbox {
		if !hasPrefix(key, fromPrefix) {
			continue
		}
		target := moveKey(key)
		if current, ok := m.inbox[target]; !ok || item.UpdatedAt.After(current.UpdatedAt) {
			m.inbox[target] = item
		}
		delete(m.inbox, key)
	}
	for key, calls := range m.toolCalls {
		if key != fromTenant {
			continue
		}
		m.toolCalls[toTenant] = append(m.toolCalls[toTenant], calls...)
		delete(m.toolCalls, key)
	}
	for key, usage := range m.usage {
		if !hasPrefix(key, fromPrefix) {
			continue
		}
		target := moveKey(key)
		if current, ok := m.usage[target]; ok {
			current.InputTokens += usage.InputTokens
			current.OutputTokens += usage.OutputTokens
			current.USDMicros += usage.USDMicros
			if usage.UpdatedAt.After(current.UpdatedAt) {
				current.UpdatedAt = usage.UpdatedAt
			}
			m.usage[target] = current
		} else {
			m.usage[target] = usage
		}
		delete(m.usage, key)
	}
	for key, summary := range m.summaries {
		if !hasPrefix(key, fromPrefix) {
			continue
		}
		target := moveKey(key)
		if current, ok := m.summaries[target]; ok {
			m.summaries[target] = mergeMigratedSummary(current, summary)
		} else {
			m.summaries[target] = cloneSessionSummary(summary)
		}
		delete(m.summaries, key)
	}
	for key, session := range m.harness {
		if !hasPrefix(key, fromPrefix) {
			continue
		}
		target := moveKey(key)
		if current, ok := m.harness[target]; ok {
			m.harness[target] = mergeMigratedHarnessSession(current, session)
		} else {
			m.harness[target] = session
		}
		delete(m.harness, key)
	}

	// If both scopes already contain the same idempotency key, retain both run
	// records but make the oldest run the stable lookup winner. This restores
	// retry semantics without discarding either transcript or audit trail.
	winners := map[string]string{}
	for key, run := range m.runs {
		if !hasPrefix(key, toPrefix) || run.IdempotencyKey == "" {
			continue
		}
		group := run.AgentName + "|" + run.IdempotencyKey
		winner, ok := winners[group]
		if !ok || earlierRun(run, m.runs[winner]) {
			winners[group] = key
		}
	}
	for key, run := range m.runs {
		if !hasPrefix(key, toPrefix) || run.IdempotencyKey == "" {
			continue
		}
		group := run.AgentName + "|" + run.IdempotencyKey
		m.idempotencyPrimary[key] = winners[group] == key
	}
}

func (m *MemoryStore) hasUnmappedRows(tenant, prefix string) bool {
	for key := range m.messages {
		if hasPrefix(key, prefix) {
			return true
		}
	}
	for key := range m.runs {
		if hasPrefix(key, prefix) {
			return true
		}
	}
	for key := range m.memories {
		if hasPrefix(key, prefix) {
			return true
		}
	}
	for key := range m.inbox {
		if hasPrefix(key, prefix) {
			return true
		}
	}
	for key := range m.toolCalls {
		if key == tenant {
			return true
		}
	}
	for key := range m.usage {
		if hasPrefix(key, prefix) {
			return true
		}
	}
	for key := range m.summaries {
		if hasPrefix(key, prefix) {
			return true
		}
	}
	for key := range m.harness {
		if hasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func mergeMigratedRun(current, incoming Run) Run {
	if incoming.UpdatedAt.After(current.UpdatedAt) {
		if incoming.Checkpoint == nil {
			incoming.Checkpoint = current.Checkpoint
		}
		if incoming.CancelRequested || current.CancelRequested {
			incoming.CancelRequested = true
			if incoming.CancelRequestedAt == nil || (current.CancelRequestedAt != nil && current.CancelRequestedAt.Before(*incoming.CancelRequestedAt)) {
				incoming.CancelRequestedAt = current.CancelRequestedAt
			}
		}
		if incoming.IdempotencyKey == "" {
			incoming.IdempotencyKey = current.IdempotencyKey
		}
		return incoming
	}
	if current.Checkpoint == nil {
		current.Checkpoint = incoming.Checkpoint
	}
	if incoming.CancelRequested {
		current.CancelRequested = true
		if current.CancelRequestedAt == nil || (incoming.CancelRequestedAt != nil && incoming.CancelRequestedAt.Before(*current.CancelRequestedAt)) {
			current.CancelRequestedAt = incoming.CancelRequestedAt
		}
	}
	if current.IdempotencyKey == "" {
		current.IdempotencyKey = incoming.IdempotencyKey
	}
	return current
}

func mergeMigratedSummary(current, incoming SessionSummary) SessionSummary {
	merged := cloneSessionSummary(current)
	if incoming.Checkpoint != nil && (merged.Checkpoint == nil || incoming.Checkpoint.ThroughSequence > merged.Checkpoint.ThroughSequence ||
		(incoming.Checkpoint.ThroughSequence == merged.Checkpoint.ThroughSequence && incoming.UpdatedAt.After(merged.UpdatedAt))) {
		merged = cloneSessionSummary(incoming)
	} else if merged.Checkpoint == nil && incoming.Checkpoint == nil && incoming.UpdatedAt.After(merged.UpdatedAt) {
		merged = cloneSessionSummary(incoming)
	}
	if incoming.MessageCount > merged.MessageCount {
		merged.MessageCount = incoming.MessageCount
	}
	if incoming.CreatedAt.Before(merged.CreatedAt) {
		merged.CreatedAt = incoming.CreatedAt
	}
	if incoming.UpdatedAt.After(merged.UpdatedAt) {
		merged.UpdatedAt = incoming.UpdatedAt
	}
	return merged
}

func mergeMigratedHarnessSession(current, incoming HarnessSession) HarnessSession {
	if incoming.ObservedEpoch > current.ObservedEpoch ||
		(incoming.ObservedEpoch == current.ObservedEpoch && incoming.UpdatedAt.After(current.UpdatedAt)) {
		current.HarnessSessionID, current.BackendKey = incoming.HarnessSessionID, incoming.BackendKey
		current.ObservedEpoch = incoming.ObservedEpoch
	}
	if incoming.Turns > current.Turns {
		current.Turns = incoming.Turns
	}
	if incoming.UpdatedAt.After(current.UpdatedAt) {
		current.UpdatedAt = incoming.UpdatedAt
	}
	return current
}

func earlierRun(a, b Run) bool {
	if a.CreatedAt.Equal(b.CreatedAt) {
		return a.ID < b.ID
	}
	return a.CreatedAt.Before(b.CreatedAt)
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
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	k := sessionKey(scope, msg.SessionID)
	if msg.CreatedAt.IsZero() {
		return fmt.Errorf("message CreatedAt is required")
	}
	m.messageSequence++
	msg.Sequence = m.messageSequence
	m.messages[k] = append(m.messages[k], msg)
	return nil
}

func cloneSessionSummary(s SessionSummary) SessionSummary {
	if s.Checkpoint == nil {
		return s
	}
	checkpoint := *s.Checkpoint
	checkpoint.ReplacementHistory = append([]SessionCheckpointMessage(nil), s.Checkpoint.ReplacementHistory...)
	for i := range checkpoint.ReplacementHistory {
		checkpoint.ReplacementHistory[i].ToolCalls = append([]SessionCheckpointToolCall(nil), checkpoint.ReplacementHistory[i].ToolCalls...)
	}
	s.Checkpoint = &checkpoint
	return s
}

func (m *MemoryStore) ListMessages(_ context.Context, scope Scope, sessionID string, limit int, cursor string) (Page, error) {
	scope = m.normalizeScope(scope)
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
	scope = m.normalizeScope(scope)
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

func (m *MemoryStore) ListSessions(_ context.Context, scope Scope, limit int) ([]Session, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	prefix := tenantKey(scope) + "|" + scope.AgentName + "|"
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Session
	for k, msgs := range m.messages {
		if !strings.HasPrefix(k, prefix) || len(msgs) == 0 {
			continue
		}
		cp := append([]Message(nil), msgs...)
		sort.Slice(cp, func(i, j int) bool {
			if cp[i].CreatedAt.Equal(cp[j].CreatedAt) {
				return cp[i].ID < cp[j].ID
			}
			return cp[i].CreatedAt.Before(cp[j].CreatedAt)
		})
		s := Session{
			ID:           strings.TrimPrefix(k, prefix),
			MessageCount: len(cp),
			CreatedAt:    cp[0].CreatedAt.UTC(),
			LastActivity: cp[len(cp)-1].CreatedAt.UTC(),
		}
		for _, msg := range cp {
			if msg.Role == "user" && !msg.ContentEncrypted {
				s.Preview = previewText(msg.Content)
				break
			}
		}
		out = append(out, s)
	}
	// Most-recently-active first, then apply the limit.
	sort.Slice(out, func(i, j int) bool { return out[i].LastActivity.After(out[j].LastActivity) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemoryStore) DeleteSession(_ context.Context, scope Scope, sessionID string) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	delete(m.messages, sessionKey(scope, sessionID))
	// The summary stands for messages that no longer exist; keeping it would
	// replay a wiped conversation back into the model after "/new".
	delete(m.summaries, sessionKey(scope, sessionID))
	// The harness session belongs to the conversation that was just wiped. A
	// "/new" that kept it would chain the next turn onto a harness session
	// holding the transcript the user asked to be rid of.
	key := sessionKey(scope, sessionID)
	if row, ok := m.harness[key]; ok {
		// Keep a monotonic epoch tombstone while discarding the native session.
		// Otherwise reusing the same user-facing ID sends attempt 1 to a runner
		// that has already observed attempt 1, and a late old receipt could restore
		// the native session the user just deleted.
		fence := row.Turns + 1
		if row.ObservedEpoch > fence {
			fence = row.ObservedEpoch
		}
		row.Turns = fence - 1
		row.HarnessSessionID, row.BackendKey = "", ""
		row.ObservedEpoch = fence
		row.UpdatedAt = time.Now().UTC()
		m.harness[key] = row
	}
	return nil
}

func (m *MemoryStore) NextHarnessTurn(_ context.Context, scope Scope, sessionID string, now time.Time) (HarnessSession, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return HarnessSession{}, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return HarnessSession{}, fmt.Errorf("session ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := sessionKey(scope, sessionID)
	row := m.harness[key]
	row.SessionID = sessionID
	row.Turns++
	row.UpdatedAt = now.UTC()
	m.harness[key] = row
	return row, nil
}

func (m *MemoryStore) PutHarnessSession(_ context.Context, scope Scope, s HarnessSession) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if strings.TrimSpace(s.SessionID) == "" {
		return fmt.Errorf("session ID is required")
	}
	if strings.TrimSpace(s.HarnessSessionID) != "" {
		if strings.TrimSpace(s.BackendKey) == "" {
			return fmt.Errorf("backend key is required when a harness session ID is observed")
		}
		if s.ObservedEpoch <= 0 {
			return fmt.Errorf("observed harness session epoch must be positive")
		}
	} else {
		s.HarnessSessionID = ""
		s.BackendKey = ""
		s.ObservedEpoch = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := sessionKey(scope, s.SessionID)
	row := m.harness[key]
	row.SessionID = s.SessionID
	// A slower earlier turn must not replace the session reported by a newer
	// observed receipt. Allocation is independent: a completed earlier turn may
	// report its native session after a later turn has already claimed an epoch.
	// A write from the same epoch is allowed so a retry can record its receipt;
	// an empty ID never clears the saved identity.
	if strings.TrimSpace(s.HarnessSessionID) != "" && s.ObservedEpoch >= row.ObservedEpoch {
		row.HarnessSessionID = s.HarnessSessionID
		row.BackendKey = s.BackendKey
		row.ObservedEpoch = s.ObservedEpoch
	}
	// Turns only ever moves forward: a writer recording the session id it
	// observed must not roll the epoch back to whatever it read earlier.
	if s.Turns > row.Turns {
		row.Turns = s.Turns
	}
	row.UpdatedAt = s.UpdatedAt.UTC()
	m.harness[key] = row
	return nil
}

func (m *MemoryStore) GetHarnessSession(_ context.Context, scope Scope, sessionID string) (HarnessSession, bool, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return HarnessSession{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.harness[sessionKey(scope, sessionID)]
	return row, ok, nil
}

func (m *MemoryStore) SaveRun(_ context.Context, scope Scope, run Run) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if run.ID == "" {
		return fmt.Errorf("run ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := tenantKey(scope) + "|" + run.ID
	_, exists := m.runs[key]
	if !exists && run.IdempotencyKey != "" {
		prefix := tenantKey(scope) + "|"
		for existingKey, existing := range m.runs {
			if hasPrefix(existingKey, prefix) && existing.AgentName == run.AgentName &&
				existing.IdempotencyKey == run.IdempotencyKey && m.idempotencyPrimary[existingKey] {
				return fmt.Errorf("duplicate idempotency key %q", run.IdempotencyKey)
			}
		}
	}
	// Mirror the Postgres upsert: the cancel columns are RequestCancel's alone,
	// so a save from a copy read before the cancel keeps the flag.
	if old, ok := m.runs[key]; ok && old.CancelRequested {
		run.CancelRequested, run.CancelRequestedAt = true, old.CancelRequestedAt
	}
	m.runs[key] = run
	if !exists && run.IdempotencyKey != "" {
		m.idempotencyPrimary[key] = true
	} else if run.IdempotencyKey == "" {
		delete(m.idempotencyPrimary, key)
	}
	if scope.ClusterID != "" {
		m.runScopes[key] = scope
	} else if oldScope, ok := m.runScopes[key]; ok {
		// A status checkpoint usually arrives from a child scope reconstructed
		// from org/workspace. Preserve the transient cluster coordinate that the
		// original request supplied so the live run can keep being projected.
		m.runScopes[key] = oldScope
	} else {
		m.runScopes[key] = scope
	}
	return nil
}

func (m *MemoryStore) RequestCancel(_ context.Context, scope Scope, id string, now time.Time) error {
	scope = m.normalizeScope(scope)
	if err := scope.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	k := tenantKey(scope) + "|" + id
	run, ok := m.runs[k]
	if !ok {
		return fmt.Errorf("run %q not found", id)
	}
	switch run.Phase {
	case RunPhasePending, RunPhaseRunning, RunPhasePendingApproval:
	default:
		return nil
	}
	if !run.CancelRequested {
		t := now.UTC()
		run.CancelRequested, run.CancelRequestedAt = true, &t
		m.runs[k] = run
	}
	return nil
}

func (m *MemoryStore) GetRun(_ context.Context, scope Scope, id string) (Run, error) {
	scope = m.normalizeScope(scope)
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
	scope = m.normalizeScope(scope)
	if err := scope.validate(); err != nil {
		return Run{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	k := tenantKey(scope) + "|" + id
	run, ok := m.runs[k]
	if !ok {
		return Run{}, fmt.Errorf("run %q not found", id)
	}
	if run.CancelRequested || (run.Phase != RunPhasePending && run.Phase != RunPhasePendingApproval) {
		return Run{}, fmt.Errorf("run %q is no longer resumable", id)
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
	scope = m.normalizeScope(scope)
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

func (m *MemoryStore) QueryRuns(_ context.Context, scope Scope, q RunQuery) (RunPage, error) {
	scope = m.normalizeScope(scope)
	if err := scope.validate(); err != nil {
		return RunPage{}, err
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	before, beforeID, err := decodeCursor(q.Cursor)
	if err != nil {
		return RunPage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := tenantKey(scope) + "|"
	var all []Run
	for k, run := range m.runs {
		if !hasPrefix(k, prefix) {
			continue
		}
		if scope.AgentName != "" && run.AgentName != scope.AgentName {
			continue
		}
		if q.Phase != "" && run.Phase != q.Phase {
			continue
		}
		if q.Trigger != "" && run.Trigger != q.Trigger {
			continue
		}
		if q.SessionID != "" && run.SessionID != q.SessionID {
			continue
		}
		if q.ParentRunID != "" && run.ParentRunID != q.ParentRunID {
			continue
		}
		all = append(all, run)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	out := make([]Run, 0, limit)
	for _, run := range all {
		if !before.IsZero() {
			if run.CreatedAt.After(before) || (run.CreatedAt.Equal(before) && run.ID >= beforeID) {
				continue
			}
		}
		out = append(out, run)
		if len(out) == limit {
			break
		}
	}
	page := RunPage{Items: out}
	if len(out) == limit {
		last := out[len(out)-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func (m *MemoryStore) PutMemory(_ context.Context, scope Scope, mem Memory) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if mem.ID == "" {
		return fmt.Errorf("memory ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	m.memories[tenantKey(scope)+"|"+mem.ID] = mem
	return nil
}

func (m *MemoryStore) ListMemories(_ context.Context, scope Scope, limit int) ([]Memory, error) {
	scope = m.normalizeScope(scope)
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
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	delete(m.memories, tenantKey(scope)+"|"+id)
	return nil
}

func (m *MemoryStore) AddInboxItem(_ context.Context, scope Scope, item InboxItem) error {
	scope = m.normalizeScope(scope)
	if err := scope.validate(); err != nil {
		return err
	}
	if item.ID == "" {
		return fmt.Errorf("inbox item ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	m.inbox[tenantKey(scope)+"|"+item.ID] = item
	return nil
}

func (m *MemoryStore) GetInboxItem(_ context.Context, scope Scope, id string) (InboxItem, error) {
	scope = m.normalizeScope(scope)
	if err := scope.validate(); err != nil {
		return InboxItem{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.inbox[tenantKey(scope)+"|"+id]
	if !ok {
		return InboxItem{}, fmt.Errorf("inbox item %q not found", id)
	}
	return it, nil
}

func (m *MemoryStore) ListInbox(_ context.Context, scope Scope, state InboxItemState) ([]InboxItem, error) {
	scope = m.normalizeScope(scope)
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
	scope = m.normalizeScope(scope)
	if err := scope.validate(); err != nil {
		return InboxItem{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
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
	scope = m.normalizeScope(scope)
	if err := scope.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	k := tenantKey(scope)
	m.toolCalls[k] = append(m.toolCalls[k], tc)
	return nil
}

func (m *MemoryStore) ListToolCalls(_ context.Context, scope Scope, runID string) ([]ToolCall, error) {
	scope = m.normalizeScope(scope)
	if err := scope.validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ToolCall
	for _, tc := range m.toolCalls[tenantKey(scope)] {
		if tc.RunID == runID {
			out = append(out, tc)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (m *MemoryStore) AddUsage(_ context.Context, scope Scope, agentName string, in, out, usdMicros int64, now time.Time, window time.Duration) (Usage, error) {
	scope = m.normalizeScope(scope)
	if err := scope.validate(); err != nil {
		return Usage{}, err
	}
	ws := windowStart(now, window)
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
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
	scope = m.normalizeScope(scope)
	if err := scope.validate(); err != nil {
		return Usage{}, err
	}
	ws := windowStart(now, window)
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	k := fmt.Sprintf("%s|%s|%d", tenantKey(scope), agentName, ws.Unix())
	u, ok := m.usage[k]
	if !ok {
		return Usage{AgentName: agentName, WindowStart: ws}, nil
	}
	return u, nil
}

func (m *MemoryStore) DeleteAgentData(_ context.Context, scope Scope, agentName string) error {
	scope = m.normalizeScope(scope)
	if err := scope.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	tk := tenantKey(scope)
	msgPrefix := tk + "|" + agentName + "|"
	for k := range m.messages {
		if len(k) >= len(msgPrefix) && k[:len(msgPrefix)] == msgPrefix {
			delete(m.messages, k)
		}
	}
	for k := range m.summaries {
		if hasPrefix(k, msgPrefix) {
			delete(m.summaries, k)
		}
	}
	for k := range m.harness {
		if hasPrefix(k, msgPrefix) {
			delete(m.harness, k)
		}
	}
	for k, run := range m.runs {
		if run.AgentName == agentName && hasPrefix(k, tk+"|") {
			delete(m.runs, k)
			delete(m.runScopes, k)
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

// DeleteRunData removes one run's rows. See Store.DeleteRunData for why usage
// is not among them.
func (m *MemoryStore) DeleteRunData(_ context.Context, scope Scope, runID string) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("run ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := tenantKey(scope) + "|" + runID
	delete(m.runs, key)
	delete(m.runScopes, key)
	for k, msgs := range m.messages {
		if !hasPrefix(k, tenantKey(scope)+"|") {
			continue
		}
		kept := msgs[:0]
		for _, msg := range msgs {
			if msg.RunID != runID {
				kept = append(kept, msg)
			}
		}
		if len(kept) == 0 {
			delete(m.messages, k)
			continue
		}
		m.messages[k] = kept
	}
	for k, calls := range m.toolCalls {
		kept := calls[:0]
		for _, call := range calls {
			if call.RunID != runID {
				kept = append(kept, call)
			}
		}
		m.toolCalls[k] = kept
	}
	return nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
