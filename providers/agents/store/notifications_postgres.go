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
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (p *PostgresStore) ClaimSessionOwner(ctx context.Context, scope Scope, agentUID, sessionID, owner string, now time.Time) error {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if err := validateSessionAccess(agentUID, sessionID, owner); err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	m, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer m.rollback()
	if m.tx == nil {
		tx, beginErr := p.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		m.tx, m.executor = tx, tx
	}
	scope = m.scope
	if err := lockTranscriptSession(ctx, m.executor, scope, sessionID); err != nil {
		return err
	}
	var current string
	err = m.executor.QueryRowContext(ctx, `SELECT subject FROM agents_session_access WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND session_id=$5 AND is_owner=TRUE`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, sessionID).Scan(&current)
	if err == nil {
		if current != owner {
			return ErrSessionOwnerConflict
		}
		return m.commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var exists bool
	err = m.executor.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM agents_messages WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4
		UNION ALL SELECT 1 FROM agents_session_titles WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4
		UNION ALL SELECT 1 FROM agents_session_models WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4
		UNION ALL SELECT 1 FROM agents_session_summaries WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4
	)`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, sessionID).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return ErrSessionOwnerConflict
	}
	_, err = m.executor.ExecContext(ctx, `INSERT INTO agents_session_access (org_uuid,workspace_uuid,agent_name,agent_uid,session_id,subject,is_owner,created_at) VALUES ($1,$2,$3,$4,$5,$6,TRUE,$7)`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, sessionID, owner, now.UTC())
	if err != nil {
		return err
	}
	return m.commit()
}

func (p *PostgresStore) GrantSessionAccess(ctx context.Context, scope Scope, agentUID, sessionID, owner, grantee string, now time.Time) error {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if err := validateSessionAccess(agentUID, sessionID, owner); err != nil {
		return err
	}
	if grantee == "" || grantee == owner {
		return fmt.Errorf("a distinct grantee subject is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	m, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer m.rollback()
	if m.tx == nil {
		tx, beginErr := p.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		m.tx, m.executor = tx, tx
	}
	if err := lockTranscriptSession(ctx, m.executor, m.scope, sessionID); err != nil {
		return err
	}
	var isOwner bool
	err = m.executor.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM agents_session_access WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND session_id=$5 AND subject=$6 AND is_owner=TRUE)`, m.scope.OrgUUID, m.scope.WorkspaceUUID, m.scope.AgentName, agentUID, sessionID, owner).Scan(&isOwner)
	if err != nil {
		return err
	}
	if !isOwner {
		return ErrSessionAccessDenied
	}
	_, err = m.executor.ExecContext(ctx, `INSERT INTO agents_session_access (org_uuid,workspace_uuid,agent_name,agent_uid,session_id,subject,is_owner,created_at) VALUES ($1,$2,$3,$4,$5,$6,FALSE,$7) ON CONFLICT DO NOTHING`, m.scope.OrgUUID, m.scope.WorkspaceUUID, m.scope.AgentName, agentUID, sessionID, grantee, now.UTC())
	if err != nil {
		return err
	}
	return m.commit()
}

func (p *PostgresStore) RevokeSessionAccess(ctx context.Context, scope Scope, agentUID, sessionID, owner, grantee string) error {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	m, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer m.rollback()
	if m.tx == nil {
		tx, beginErr := p.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		m.tx, m.executor = tx, tx
	}
	if err := lockTranscriptSession(ctx, m.executor, m.scope, sessionID); err != nil {
		return err
	}
	var isOwner bool
	err = m.executor.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM agents_session_access WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND session_id=$5 AND subject=$6 AND is_owner=TRUE)`, m.scope.OrgUUID, m.scope.WorkspaceUUID, m.scope.AgentName, agentUID, sessionID, owner).Scan(&isOwner)
	if err != nil {
		return err
	}
	if !isOwner {
		return ErrSessionAccessDenied
	}
	_, err = m.executor.ExecContext(ctx, `DELETE FROM agents_session_access WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND session_id=$5 AND subject=$6 AND is_owner=FALSE`, m.scope.OrgUUID, m.scope.WorkspaceUUID, m.scope.AgentName, agentUID, sessionID, grantee)
	if err != nil {
		return err
	}
	return m.commit()
}

func (p *PostgresStore) HasSessionAccess(ctx context.Context, scope Scope, agentUID, sessionID, subject string) (bool, error) {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return false, err
	}
	if err := validateSessionAccess(agentUID, sessionID, subject); err != nil {
		return false, err
	}
	var ok bool
	err := p.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM agents_session_access WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND session_id=$5 AND subject=$6)`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, sessionID, subject).Scan(&ok)
	return ok, err
}

func (p *PostgresStore) CreateSessionNotification(ctx context.Context, scope Scope, agentUID, sessionID, subject, key, payload string, now time.Time, expectedOwnerArg ...string) (SessionNotification, bool, error) {
	scope = p.normalizeScope(ctx, scope)
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
	if now.IsZero() {
		now = time.Now().UTC()
	}
	fingerprint := notificationFingerprint(payload)
	id := uuid.NewString()
	var item SessionNotification
	item.ID, item.SessionID, item.Key, item.Producer, item.Payload, item.CreatedAt = id, sessionID, key, subject, payload, now.UTC()
	m, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return SessionNotification{}, false, err
	}
	defer m.rollback()
	if m.tx == nil {
		tx, beginErr := p.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return SessionNotification{}, false, beginErr
		}
		m.tx, m.executor = tx, tx
	}
	scope = m.scope
	if err := lockTranscriptSession(ctx, m.executor, scope, sessionID); err != nil {
		return SessionNotification{}, false, err
	}
	if expectedOwner != "" {
		var actualOwner string
		err = m.executor.QueryRowContext(ctx, `SELECT subject FROM agents_session_access WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND session_id=$5 AND is_owner=TRUE`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, sessionID).Scan(&actualOwner)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return SessionNotification{}, false, ErrSessionAccessDenied
			}
			return SessionNotification{}, false, err
		}
		if actualOwner != expectedOwner {
			return SessionNotification{}, false, ErrSessionAccessDenied
		}
	}
	var authorized bool
	err = m.executor.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM agents_session_access WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND session_id=$5 AND subject=$6)`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, sessionID, subject).Scan(&authorized)
	if err != nil {
		return SessionNotification{}, false, err
	}
	if !authorized {
		return SessionNotification{}, false, ErrSessionAccessDenied
	}
	result, err := m.executor.ExecContext(ctx, `INSERT INTO agents_session_notifications (id,org_uuid,workspace_uuid,agent_name,agent_uid,session_id,subject,idempotency_key,fingerprint,payload,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (org_uuid,workspace_uuid,agent_name,agent_uid,session_id,subject,idempotency_key) DO NOTHING`, id, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, sessionID, subject, key, fingerprint, payload, now.UTC())
	if err != nil {
		return SessionNotification{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return SessionNotification{}, false, err
	}
	if inserted == 0 {
		var existingFingerprint string
		err = m.executor.QueryRowContext(ctx, `SELECT id,fingerprint,payload,created_at FROM agents_session_notifications WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND session_id=$5 AND subject=$6 AND idempotency_key=$7`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, sessionID, subject, key).Scan(&item.ID, &existingFingerprint, &item.Payload, &item.CreatedAt)
		if err != nil {
			return SessionNotification{}, false, err
		}
		if existingFingerprint != fingerprint {
			return SessionNotification{}, false, ErrNotificationConflict
		}
		return item, true, m.commit()
	}
	return item, false, m.commit()
}

func (p *PostgresStore) ListSessionNotifications(ctx context.Context, scope Scope, agentUID, sessionID, subject string, limit int) ([]SessionNotification, error) {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	if err := validateSessionAccess(agentUID, sessionID, subject); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var authorized bool
	err := p.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM agents_session_access WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND session_id=$5 AND subject=$6)`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, sessionID, subject).Scan(&authorized)
	if err != nil {
		return nil, err
	}
	if !authorized {
		return nil, ErrSessionAccessDenied
	}
	rows, err := p.db.QueryContext(ctx, `SELECT id,session_id,idempotency_key,subject,payload,created_at FROM agents_session_notifications WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND session_id=$5 ORDER BY created_at DESC,id DESC LIMIT $6`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []SessionNotification{}
	for rows.Next() {
		var item SessionNotification
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Key, &item.Producer, &item.Payload, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

var _ SessionNotificationStore = (*PostgresStore)(nil)
