// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// sessionTitle is an internal claim record. The first-message coordinate is
// part of the claim so a late title result cannot attach to a reused session ID.
type sessionTitle struct {
	FirstMessageID string
	Title          string
}

func firstPersistedUserMessage(messages []Message) (Message, bool) {
	var first Message
	found := false
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		if !found || message.Sequence < first.Sequence {
			first, found = message, true
		}
	}
	return first, found
}

func transcriptLockKey(scope Scope, sessionID string) string {
	return scope.OrgUUID + "/" + scope.WorkspaceUUID + "/" + scope.AgentName + "/" + sessionID
}

func (m *MemoryStore) ClaimSessionTitle(_ context.Context, scope Scope, sessionID, firstUserMessageID string) (bool, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return false, err
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(firstUserMessageID) == "" {
		return false, fmt.Errorf("session and first user message IDs are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := sessionKey(scope, sessionID)
	first, ok := firstPersistedUserMessage(m.messages[key])
	if !ok || first.ID != firstUserMessageID {
		return false, nil
	}
	if _, exists := m.sessionTitles[key]; exists {
		return false, nil
	}
	m.sessionTitles[key] = sessionTitle{FirstMessageID: firstUserMessageID}
	return true, nil
}

func (m *MemoryStore) SetSessionTitleIfEmpty(_ context.Context, scope Scope, sessionID, firstUserMessageID, title string) (bool, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return false, err
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(firstUserMessageID) == "" {
		return false, fmt.Errorf("session and first user message IDs are required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := sessionKey(scope, sessionID)
	claim, exists := m.sessionTitles[key]
	if !exists || claim.FirstMessageID != firstUserMessageID || claim.Title != "" {
		return false, nil
	}
	first, ok := firstPersistedUserMessage(m.messages[key])
	if !ok || first.ID != firstUserMessageID {
		return false, nil
	}
	claim.Title = title
	m.sessionTitles[key] = claim
	return true, nil
}

func (p *PostgresStore) ClaimSessionTitle(ctx context.Context, scope Scope, sessionID, firstUserMessageID string) (bool, error) {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return false, err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.withAgent(); err != nil {
		return false, err
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(firstUserMessageID) == "" {
		return false, fmt.Errorf("session and first user message IDs are required")
	}
	if mutation.tx == nil {
		mutation.tx, err = p.db.BeginTx(ctx, nil)
		if err != nil {
			return false, err
		}
		mutation.executor = mutation.tx
	}
	if err := lockTranscriptSession(ctx, mutation.executor, scope, sessionID); err != nil {
		return false, err
	}
	var firstID string
	err = mutation.executor.QueryRowContext(ctx, `
		SELECT id FROM agents_messages
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4 AND role='user'
		ORDER BY append_sequence ASC LIMIT 1 FOR SHARE`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID).Scan(&firstID)
	if err == sql.ErrNoRows || (err == nil && firstID != firstUserMessageID) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := mutation.executor.ExecContext(ctx, `
		INSERT INTO agents_session_titles
			(org_uuid,workspace_uuid,agent_name,session_id,first_message_id,title)
		VALUES ($1,$2,$3,$4,$5,'') ON CONFLICT (org_uuid,workspace_uuid,agent_name,session_id) DO NOTHING`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID, firstUserMessageID)
	if err != nil {
		return false, err
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := mutation.commit(); err != nil {
		return false, err
	}
	return claimed == 1, nil
}

func (p *PostgresStore) SetSessionTitleIfEmpty(ctx context.Context, scope Scope, sessionID, firstUserMessageID, title string) (bool, error) {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return false, err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.withAgent(); err != nil {
		return false, err
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(firstUserMessageID) == "" {
		return false, fmt.Errorf("session and first user message IDs are required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return false, nil
	}
	if mutation.tx == nil {
		mutation.tx, err = p.db.BeginTx(ctx, nil)
		if err != nil {
			return false, err
		}
		mutation.executor = mutation.tx
	}
	if err := lockTranscriptSession(ctx, mutation.executor, scope, sessionID); err != nil {
		return false, err
	}
	result, err := mutation.executor.ExecContext(ctx, `
		UPDATE agents_session_titles AS title_row SET title=$6
		WHERE title_row.org_uuid=$1 AND title_row.workspace_uuid=$2 AND title_row.agent_name=$3
			AND title_row.session_id=$4 AND title_row.first_message_id=$5 AND title_row.title=''
			AND EXISTS (
				SELECT 1 FROM agents_messages first_message
				WHERE first_message.org_uuid=$1 AND first_message.workspace_uuid=$2
					AND first_message.agent_name=$3 AND first_message.session_id=$4
					AND first_message.role='user' AND first_message.id=$5
					AND NOT EXISTS (
						SELECT 1 FROM agents_messages prior
						WHERE prior.org_uuid=$1 AND prior.workspace_uuid=$2 AND prior.agent_name=$3
							AND prior.session_id=$4 AND prior.role='user'
							AND prior.append_sequence < first_message.append_sequence
					)
			)`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID, firstUserMessageID, title)
	if err != nil {
		return false, err
	}
	written, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := mutation.commit(); err != nil {
		return false, err
	}
	return written == 1, nil
}

func lockTranscriptSession(ctx context.Context, executor scopedExecutor, scope Scope, sessionID string) error {
	if _, err := executor.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, transcriptLockKey(scope, sessionID)); err != nil {
		return fmt.Errorf("lock transcript session: %w", err)
	}
	return nil
}
