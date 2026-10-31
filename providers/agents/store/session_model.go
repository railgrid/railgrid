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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"
)

const MaxSessionModelSettingsPerAgent = 1024

var ErrSessionModelLimit = errors.New("session model preference limit reached")

// SessionModelSettings belongs to one conversation, never to its Agent.
// An empty credential inherits the Agent's chat credential. Efforts are keyed
// by credential so switching models cannot carry an incompatible effort over.
type SessionModelSettings struct {
	ModelCredential        string            `json:"modelCredential,omitempty"`
	ReasoningEffortByModel map[string]string `json:"reasoningEffortByModel,omitempty"`
}

// ModelSettings pins the choices used by a run, including approval resumes.
type ModelSettings struct {
	ModelCredential string `json:"modelCredential"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

func (m *MemoryStore) GetSessionModelSettings(_ context.Context, scope Scope, session string) (SessionModelSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	if err := scope.withAgent(); err != nil {
		return SessionModelSettings{}, err
	}
	settings := m.sessionModels[sessionKey(scope, session)].settings
	settings.ReasoningEffortByModel = maps.Clone(settings.ReasoningEffortByModel)
	return settings, nil
}

func (m *MemoryStore) PutSessionModelSettings(_ context.Context, scope Scope, session string, settings SessionModelSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	key := sessionKey(scope, session)
	if _, exists := m.sessionModels[key]; !exists {
		prefix := tenantKey(scope) + "|" + scope.AgentName + "|"
		count := 0
		for existing := range m.sessionModels {
			if strings.HasPrefix(existing, prefix) {
				count++
			}
		}
		if count >= MaxSessionModelSettingsPerAgent {
			return ErrSessionModelLimit
		}
	}
	settings.ReasoningEffortByModel = maps.Clone(settings.ReasoningEffortByModel)
	m.sessionModels[key] = memorySessionModel{settings: settings, updatedAt: time.Now().UTC()}
	return nil
}

func (p *PostgresStore) GetSessionModelSettings(ctx context.Context, scope Scope, session string) (SessionModelSettings, error) {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return SessionModelSettings{}, err
	}
	var data []byte
	err := p.db.QueryRowContext(ctx, `SELECT settings FROM agents_session_models WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, session).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionModelSettings{}, nil
	}
	if err != nil {
		return SessionModelSettings{}, err
	}
	var settings SessionModelSettings
	err = json.Unmarshal(data, &settings)
	return settings, err
}

func (p *PostgresStore) PutSessionModelSettings(ctx context.Context, scope Scope, session string, settings SessionModelSettings) error {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.withAgent(); err != nil {
		return err
	}
	if mutation.tx == nil {
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin session model preference mutation: %w", err)
		}
		mutation.tx, mutation.executor = tx, tx
	}
	lockKey := fmt.Sprintf("agents-session-model-limit:%q:%q:%q", scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName)
	if _, err := mutation.executor.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return fmt.Errorf("lock session model preference limit: %w", err)
	}
	var existingSession string
	err = mutation.executor.QueryRowContext(ctx, `
		SELECT session_id FROM agents_session_models
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND session_id=$4
		FOR UPDATE`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, session).Scan(&existingSession)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !exists {
		var count int64
		if err := mutation.executor.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM agents_session_models
			WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3`,
			scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName).Scan(&count); err != nil {
			return err
		}
		if count >= MaxSessionModelSettingsPerAgent {
			return ErrSessionModelLimit
		}
	}
	data, err := marshalJSONB(settings)
	if err != nil {
		return err
	}
	_, err = mutation.executor.ExecContext(ctx, `INSERT INTO agents_session_models (org_uuid,workspace_uuid,agent_name,session_id,settings) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (org_uuid,workspace_uuid,agent_name,session_id) DO UPDATE SET settings=EXCLUDED.settings, updated_at=NOW()`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, session, data)
	if err != nil {
		return err
	}
	return mutation.commit()
}
