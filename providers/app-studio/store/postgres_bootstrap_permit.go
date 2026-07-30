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

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *PostgresStore) CreateProjectBootstrapPermit(ctx context.Context, scope Scope, actor, promptDigest string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	actor, promptDigest = strings.TrimSpace(actor), strings.TrimSpace(promptDigest)
	if actor == "" || promptDigest == "" {
		return fmt.Errorf("bootstrap permit actor and prompt digest are required")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO app_studio_project_bootstrap_permits (
		org_uuid, workspace_uuid, project_name, project_uid, actor_id, prompt_digest, created_at
	) VALUES ($1,$2,$3,$4,$5,$6,now())
	ON CONFLICT DO NOTHING`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, actor, promptDigest)
	if err != nil {
		return fmt.Errorf("create bootstrap permit: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var existingActor, existingDigest string
	if err := s.db.QueryRowContext(ctx, `SELECT actor_id, prompt_digest FROM app_studio_project_bootstrap_permits WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID).Scan(&existingActor, &existingDigest); err != nil {
		return fmt.Errorf("read bootstrap permit: %w", err)
	}
	if existingActor != actor || existingDigest != promptDigest {
		return ErrProjectBootstrapPermitConflict
	}
	return nil
}

func (s *PostgresStore) ConsumeProjectBootstrapPermit(ctx context.Context, scope Scope, actor, promptDigest, clientRequestID string, now time.Time) (bool, error) {
	if err := scope.validate(); err != nil {
		return false, err
	}
	actor, promptDigest, clientRequestID = strings.TrimSpace(actor), strings.TrimSpace(promptDigest), strings.TrimSpace(clientRequestID)
	if actor == "" || promptDigest == "" || clientRequestID == "" {
		return false, fmt.Errorf("bootstrap permit actor, prompt digest, and client request ID are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin consume bootstrap permit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var permitActor, permitDigest, consumed string
	err = tx.QueryRowContext(ctx, `SELECT actor_id, prompt_digest, consumed_client_request_id
		FROM app_studio_project_bootstrap_permits WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 FOR UPDATE`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID).Scan(&permitActor, &permitDigest, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read bootstrap permit: %w", err)
	}
	if permitActor != actor || permitDigest != promptDigest {
		return false, nil
	}
	if consumed != "" && consumed != clientRequestID {
		return false, ErrProjectBootstrapPermitConflict
	}
	if consumed == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE app_studio_project_bootstrap_permits SET consumed_client_request_id=$5, consumed_at=$6 WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, clientRequestID, now.UTC()); err != nil {
			return false, fmt.Errorf("consume bootstrap permit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit bootstrap permit: %w", err)
	}
	return true, nil
}
