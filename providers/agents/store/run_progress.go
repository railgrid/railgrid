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
	"encoding/json"
	"fmt"
)

// PutRunProgress replaces the presentation overlay on the run row so tenant
// migration, retention and deletion follow the run automatically. Lifecycle
// writes deliberately leave it alone.
func (m *MemoryStore) PutRunProgress(_ context.Context, scope Scope, runID string, messages []Message) error {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	raw, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	key := tenantKey(scope) + "|" + runID
	run, ok := m.runs[key]
	if !ok || run.AgentName != scope.AgentName {
		return fmt.Errorf("run %q not found", runID)
	}
	run.progress = raw
	m.runs[key] = run
	return nil
}

func (m *MemoryStore) GetRunProgress(_ context.Context, scope Scope, runID string) ([]Message, error) {
	scope = m.normalizeScope(scope)
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = m.normalizeScopeLocked(scope)
	run, ok := m.runs[tenantKey(scope)+"|"+runID]
	if !ok || run.AgentName != scope.AgentName {
		return nil, fmt.Errorf("run %q not found", runID)
	}
	return decodeRunProgress(run.progress)
}

func (p *PostgresStore) PutRunProgress(ctx context.Context, scope Scope, runID string, messages []Message) error {
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if err := scope.withAgent(); err != nil {
		return err
	}
	raw, err := marshalJSONB(messages)
	if err != nil {
		return err
	}
	result, err := mutation.executor.ExecContext(ctx, `UPDATE agents_runs SET progress=$5
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, runID, raw)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("run %q not found", runID)
	}
	return mutation.commit()
}

func (p *PostgresStore) GetRunProgress(ctx context.Context, scope Scope, runID string) ([]Message, error) {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	var raw []byte
	if err := p.db.QueryRowContext(ctx, `SELECT progress FROM agents_runs
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND id=$4`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, runID).Scan(&raw); err != nil {
		return nil, err
	}
	return decodeRunProgress(raw)
}

func decodeRunProgress(raw []byte) ([]Message, error) {
	var messages []Message
	if len(raw) == 0 {
		return nil, nil
	}
	err := json.Unmarshal(raw, &messages)
	return messages, err
}
