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
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

func sessionAccessKey(scope Scope, agentUID, sessionID string) string {
	return sessionKey(scope, sessionID) + "|" + agentUID
}

func (m *MemoryStore) ClaimSessionOwner(_ context.Context, scope Scope, agentUID, sessionID, owner string, _ time.Time) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if err := validateSessionAccess(agentUID, sessionID, owner); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := sessionAccessKey(scope, agentUID, sessionID)
	if current := m.sessionOwners[key]; current != "" {
		if current == owner {
			return nil
		}
		return ErrSessionOwnerConflict
	}
	transcript := sessionKey(scope, sessionID)
	_, hasTitle := m.sessionTitles[transcript]
	_, hasModelSettings := m.sessionModels[transcript]
	if len(m.messages[transcript]) != 0 || hasTitle || hasModelSettings {
		return ErrSessionOwnerConflict
	}
	if _, exists := m.summaries[transcript]; exists {
		return ErrSessionOwnerConflict
	}
	m.sessionOwners[key] = owner
	return nil
}

func (m *MemoryStore) GrantSessionAccess(_ context.Context, scope Scope, agentUID, sessionID, owner, grantee string, _ time.Time) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if err := validateSessionAccess(agentUID, sessionID, owner); err != nil {
		return err
	}
	if grantee == "" || grantee == owner {
		return fmt.Errorf("a distinct grantee subject is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := sessionAccessKey(scope, agentUID, sessionID)
	if m.sessionOwners[key] != owner {
		return ErrSessionAccessDenied
	}
	if m.sessionGrants[key] == nil {
		m.sessionGrants[key] = map[string]bool{}
	}
	m.sessionGrants[key][grantee] = true
	return nil
}

func (m *MemoryStore) RevokeSessionAccess(_ context.Context, scope Scope, agentUID, sessionID, owner, grantee string) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if err := validateSessionAccess(agentUID, sessionID, owner); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := sessionAccessKey(scope, agentUID, sessionID)
	if m.sessionOwners[key] != owner {
		return ErrSessionAccessDenied
	}
	delete(m.sessionGrants[key], grantee)
	return nil
}

func (m *MemoryStore) HasSessionAccess(_ context.Context, scope Scope, agentUID, sessionID, subject string) (bool, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return false, err
	}
	if err := validateSessionAccess(agentUID, sessionID, subject); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := sessionAccessKey(scope, agentUID, sessionID)
	return m.sessionOwners[key] == subject || m.sessionGrants[key][subject], nil
}

func (m *MemoryStore) CreateSessionNotification(_ context.Context, scope Scope, agentUID, sessionID, subject, key, payload string, now time.Time, expectedOwnerArg ...string) (SessionNotification, bool, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return SessionNotification{}, false, err
	}
	if err := validateSessionAccess(agentUID, sessionID, subject); err != nil {
		return SessionNotification{}, false, err
	}
	if len(expectedOwnerArg) > 1 {
		return SessionNotification{}, false, fmt.Errorf("%w: at most one expected owner is allowed", ErrNotificationInput)
	}
	expectedOwner := ""
	if len(expectedOwnerArg) == 1 {
		expectedOwner = expectedOwnerArg[0]
		if strings.TrimSpace(expectedOwner) == "" {
			return SessionNotification{}, false, fmt.Errorf("%w: expected owner cannot be empty", ErrNotificationInput)
		}
	}
	if key == "" || len(key) > 256 || payload == "" || len(payload) > 16384 {
		return SessionNotification{}, false, fmt.Errorf("%w: notification key and payload are required; key is limited to 256 bytes and payload to 16384 bytes", ErrNotificationInput)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	access := sessionAccessKey(scope, agentUID, sessionID)
	if expectedOwner != "" && m.sessionOwners[access] != expectedOwner {
		return SessionNotification{}, false, ErrSessionAccessDenied
	}
	if m.sessionOwners[access] != subject && !m.sessionGrants[access][subject] {
		return SessionNotification{}, false, ErrSessionAccessDenied
	}
	keyed := access + "|" + subject + "|" + key
	if existing, ok := m.notificationKeys[keyed]; ok {
		if notificationFingerprint(existing.Payload) != notificationFingerprint(payload) {
			return SessionNotification{}, false, ErrNotificationConflict
		}
		return existing, true, nil
	}
	item := SessionNotification{ID: uuid.NewString(), SessionID: sessionID, Key: key, Producer: subject, Payload: payload, CreatedAt: now.UTC()}
	m.sessionNotifications[access] = append(m.sessionNotifications[access], item)
	m.notificationKeys[keyed] = item
	return item, false, nil
}

func (m *MemoryStore) ListSessionNotifications(_ context.Context, scope Scope, agentUID, sessionID, subject string, limit int) ([]SessionNotification, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	if err := validateSessionAccess(agentUID, sessionID, subject); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	access := sessionAccessKey(scope, agentUID, sessionID)
	if m.sessionOwners[access] != subject && !m.sessionGrants[access][subject] {
		return nil, ErrSessionAccessDenied
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	items := slices.Clone(m.sessionNotifications[access])
	slices.SortFunc(items, func(a, b SessionNotification) int {
		if order := b.CreatedAt.Compare(a.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(b.ID, a.ID)
	})
	return items[:min(limit, len(items))], nil
}

var _ SessionNotificationStore = (*MemoryStore)(nil)
