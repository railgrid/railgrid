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

func (s *PostgresStore) CreateAssistantThread(ctx context.Context, scope Scope, thread AssistantThread, events []AssistantThreadEvent) (AssistantThread, error) {
	if s == nil || s.db == nil {
		return AssistantThread{}, errors.New("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return AssistantThread{}, err
	}
	prepared, err := prepareAssistantThread(thread)
	if err != nil {
		return AssistantThread{}, err
	}
	preparedActor := prepared.ActorID
	preparedEvents := make([]AssistantThreadEvent, len(events))
	for index, event := range events {
		event.ThreadID = prepared.ID
		preparedEvents[index], err = prepareAssistantThreadEvent(event)
		if err != nil {
			return AssistantThread{}, err
		}
		preparedEvents[index].Sequence = int64(index) + 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantThread{}, fmt.Errorf("begin create assistant thread: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `INSERT INTO app_studio_assistant_threads (
		org_uuid, workspace_uuid, project_name, project_uid, thread_id, title, status, actor_id, created_at, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	ON CONFLICT (org_uuid, workspace_uuid, project_name, project_uid, thread_id) DO UPDATE SET thread_id=EXCLUDED.thread_id
	RETURNING title,status,actor_id,created_at,updated_at`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		prepared.ID, prepared.Title, prepared.Status, prepared.ActorID, prepared.CreatedAt, prepared.UpdatedAt)
	var status string
	if err := row.Scan(&prepared.Title, &status, &prepared.ActorID, &prepared.CreatedAt, &prepared.UpdatedAt); err != nil {
		return AssistantThread{}, fmt.Errorf("create assistant thread: %w", err)
	}
	prepared.Status = AssistantThreadStatus(status)
	prepared.CreatedAt, prepared.UpdatedAt = prepared.CreatedAt.UTC(), prepared.UpdatedAt.UTC()
	if prepared.ActorID != preparedActor {
		return AssistantThread{}, ErrAssistantThreadConflict
	}
	for _, event := range preparedEvents {
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_studio_assistant_thread_events (
			org_uuid,workspace_uuid,project_name,project_uid,thread_id,turn_id,sequence,event_type,item_id,request_id,payload,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING`, scope.OrgUUID, scope.WorkspaceUUID,
			scope.ProjectName, scope.ProjectUID, event.ThreadID, event.TurnID, event.Sequence, event.Type, event.ItemID, event.RequestID, event.Payload, event.CreatedAt); err != nil {
			return AssistantThread{}, fmt.Errorf("append initial assistant thread event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return AssistantThread{}, fmt.Errorf("commit assistant thread: %w", err)
	}
	return prepared, nil
}

func (s *PostgresStore) GetAssistantThread(ctx context.Context, scope Scope, threadID string) (AssistantThread, error) {
	if err := scope.validate(); err != nil {
		return AssistantThread{}, err
	}
	thread := AssistantThread{ID: strings.TrimSpace(threadID)}
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT title,status,actor_id,created_at,updated_at FROM app_studio_assistant_threads
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, thread.ID,
	).Scan(&thread.Title, &status, &thread.ActorID, &thread.CreatedAt, &thread.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AssistantThread{}, ErrAssistantThreadNotFound
	}
	if err != nil {
		return AssistantThread{}, fmt.Errorf("get assistant thread: %w", err)
	}
	thread.Status = AssistantThreadStatus(status)
	thread.CreatedAt, thread.UpdatedAt = thread.CreatedAt.UTC(), thread.UpdatedAt.UTC()
	return thread, nil
}

func (s *PostgresStore) ListAssistantThreads(ctx context.Context, scope Scope, actorID string, includeArchived bool, limit int, cursor string) (AssistantThreadPage, error) {
	if err := scope.validate(); err != nil {
		return AssistantThreadPage{}, err
	}
	limit = normalizeLimit(limit)
	cursorTime, cursorID, err := decodeThreadCursor(cursor)
	if err != nil {
		return AssistantThreadPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id,title,status,actor_id,created_at,updated_at
		FROM app_studio_assistant_threads WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND actor_id=$5
		AND ($6 OR status <> 'archived') AND ($7::timestamptz IS NULL OR updated_at < $7 OR (updated_at=$7 AND thread_id < $8))
		ORDER BY updated_at DESC, thread_id DESC LIMIT $9`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		strings.TrimSpace(actorID), includeArchived, nullableThreadCursorTime(cursorTime), cursorID, limit+1)
	if err != nil {
		return AssistantThreadPage{}, fmt.Errorf("list assistant threads: %w", err)
	}
	defer rows.Close()
	page := AssistantThreadPage{Items: make([]AssistantThread, 0, limit)}
	for rows.Next() {
		var thread AssistantThread
		var status string
		if err := rows.Scan(&thread.ID, &thread.Title, &status, &thread.ActorID, &thread.CreatedAt, &thread.UpdatedAt); err != nil {
			return AssistantThreadPage{}, fmt.Errorf("scan assistant thread: %w", err)
		}
		thread.Status = AssistantThreadStatus(status)
		thread.CreatedAt, thread.UpdatedAt = thread.CreatedAt.UTC(), thread.UpdatedAt.UTC()
		page.Items = append(page.Items, thread)
	}
	if err := rows.Err(); err != nil {
		return AssistantThreadPage{}, fmt.Errorf("iterate assistant threads: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeThreadCursor(last.UpdatedAt, last.ID)
	}
	return page, nil
}

func nullableThreadCursorTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func (s *PostgresStore) UpdateAssistantThread(ctx context.Context, scope Scope, thread AssistantThread) (AssistantThread, error) {
	if err := scope.validate(); err != nil {
		return AssistantThread{}, err
	}
	prepared, err := prepareAssistantThread(thread)
	if err != nil {
		return AssistantThread{}, err
	}
	row := s.db.QueryRowContext(ctx, `UPDATE app_studio_assistant_threads SET title=$6,status=$7,updated_at=$8
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5 AND actor_id=$9
		RETURNING created_at`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, prepared.ID,
		prepared.Title, prepared.Status, prepared.UpdatedAt, prepared.ActorID)
	if err := row.Scan(&prepared.CreatedAt); errors.Is(err, sql.ErrNoRows) {
		return AssistantThread{}, ErrAssistantThreadNotFound
	} else if err != nil {
		return AssistantThread{}, fmt.Errorf("update assistant thread: %w", err)
	}
	prepared.CreatedAt = prepared.CreatedAt.UTC()
	return prepared, nil
}

func (s *PostgresStore) UpdateAssistantThreadWithEvent(ctx context.Context, scope Scope, thread AssistantThread, event AssistantThreadEvent, expectedSequence int64) (AssistantThread, AssistantThreadEvent, error) {
	if err := scope.validate(); err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	prepared, err := prepareAssistantThread(thread)
	if err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	event.ThreadID = prepared.ID
	preparedEvent, err := prepareAssistantThreadEvent(event)
	if err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, fmt.Errorf("begin update assistant thread with event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, assistantThreadLockKey(scope, prepared.ID)); err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, fmt.Errorf("lock assistant thread projection: %w", err)
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM app_studio_assistant_thread_events
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, prepared.ID).Scan(&current); err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, fmt.Errorf("read assistant thread projection sequence: %w", err)
	}
	if current != expectedSequence {
		return AssistantThread{}, AssistantThreadEvent{}, ErrAssistantThreadEventConflict
	}
	row := tx.QueryRowContext(ctx, `UPDATE app_studio_assistant_threads SET title=$6,status=$7,updated_at=$8
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5 AND actor_id=$9
		RETURNING created_at`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, prepared.ID,
		prepared.Title, prepared.Status, prepared.UpdatedAt, prepared.ActorID)
	if err := row.Scan(&prepared.CreatedAt); errors.Is(err, sql.ErrNoRows) {
		return AssistantThread{}, AssistantThreadEvent{}, ErrAssistantThreadNotFound
	} else if err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, fmt.Errorf("update assistant thread with event: %w", err)
	}
	prepared.CreatedAt = prepared.CreatedAt.UTC()
	preparedEvent.Sequence = current + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_studio_assistant_thread_events (
		org_uuid,workspace_uuid,project_name,project_uid,thread_id,turn_id,sequence,event_type,item_id,request_id,payload,created_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		preparedEvent.ThreadID, preparedEvent.TurnID, preparedEvent.Sequence, preparedEvent.Type, preparedEvent.ItemID, preparedEvent.RequestID, preparedEvent.Payload, preparedEvent.CreatedAt); err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, fmt.Errorf("append assistant thread projection event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, fmt.Errorf("commit assistant thread projection: %w", err)
	}
	return prepared, preparedEvent, nil
}

func (s *PostgresStore) CreateAssistantTurn(ctx context.Context, scope Scope, turn AssistantTurn, events []AssistantThreadEvent) (AssistantTurn, error) {
	if err := scope.validate(); err != nil {
		return AssistantTurn{}, err
	}
	prepared, err := prepareAssistantTurn(turn)
	if err != nil {
		return AssistantTurn{}, err
	}
	preparedEvents := make([]AssistantThreadEvent, len(events))
	for index, event := range events {
		event.ThreadID, event.TurnID = prepared.ThreadID, prepared.ID
		preparedEvents[index], err = prepareAssistantThreadEvent(event)
		if err != nil {
			return AssistantTurn{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantTurn{}, fmt.Errorf("begin create assistant turn: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT actor_id FROM app_studio_assistant_threads
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5 FOR UPDATE`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, prepared.ThreadID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return AssistantTurn{}, ErrAssistantThreadNotFound
	} else if err != nil {
		return AssistantTurn{}, fmt.Errorf("lock assistant thread: %w", err)
	}
	if owner != prepared.ActorID {
		return AssistantTurn{}, ErrAssistantTurnConflict
	}
	var baseSequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) FROM app_studio_assistant_thread_events
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, prepared.ThreadID).Scan(&baseSequence); err != nil {
		return AssistantTurn{}, fmt.Errorf("load assistant thread event sequence: %w", err)
	}
	row := tx.QueryRowContext(ctx, `INSERT INTO app_studio_assistant_turns (
		org_uuid,workspace_uuid,project_name,project_uid,thread_id,turn_id,actor_id,client_user_message_id,mode,approval_mode,status,checkpoint,terminal_error,created_at,updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
	ON CONFLICT (org_uuid,workspace_uuid,project_name,project_uid,thread_id,client_user_message_id) DO NOTHING RETURNING turn_id`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, prepared.ThreadID, prepared.ID, prepared.ActorID,
		prepared.ClientUserMessageID, prepared.Mode, prepared.ApprovalMode, prepared.Status, prepared.Checkpoint, prepared.Error, prepared.CreatedAt, prepared.UpdatedAt)
	var insertedID string
	if err := row.Scan(&insertedID); errors.Is(err, sql.ErrNoRows) {
		if err := tx.QueryRowContext(ctx, `SELECT turn_id,actor_id,mode,approval_mode,status,checkpoint,terminal_error,created_at,updated_at
			FROM app_studio_assistant_turns WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5 AND client_user_message_id=$6`,
			scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, prepared.ThreadID, prepared.ClientUserMessageID,
		).Scan(&prepared.ID, &prepared.ActorID, &prepared.Mode, &prepared.ApprovalMode, &prepared.Status, &prepared.Checkpoint, &prepared.Error, &prepared.CreatedAt, &prepared.UpdatedAt); err != nil {
			return AssistantTurn{}, fmt.Errorf("load idempotent assistant turn: %w", err)
		}
		return prepared, tx.Commit()
	} else if err != nil {
		if strings.Contains(err.Error(), "assistant_turns_active_idx") {
			return AssistantTurn{}, ErrAssistantTurnConflict
		}
		return AssistantTurn{}, fmt.Errorf("create assistant turn: %w", err)
	}
	for index, event := range preparedEvents {
		event.Sequence = baseSequence + int64(index) + 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_studio_assistant_thread_events (
			org_uuid,workspace_uuid,project_name,project_uid,thread_id,turn_id,sequence,event_type,item_id,request_id,payload,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
			event.ThreadID, event.TurnID, event.Sequence, event.Type, event.ItemID, event.RequestID, event.Payload, event.CreatedAt); err != nil {
			return AssistantTurn{}, fmt.Errorf("append initial assistant thread event: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_studio_assistant_threads SET status='active',updated_at=$6
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, prepared.ThreadID, prepared.UpdatedAt); err != nil {
		return AssistantTurn{}, fmt.Errorf("activate assistant thread: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AssistantTurn{}, fmt.Errorf("commit assistant turn: %w", err)
	}
	return prepared, nil
}

func (s *PostgresStore) GetAssistantTurn(ctx context.Context, scope Scope, threadID, turnID string) (AssistantTurn, error) {
	return s.getAssistantTurn(ctx, scope, threadID, "turn_id=$6", strings.TrimSpace(turnID))
}

func (s *PostgresStore) FindAssistantTurnByClientUserMessageID(ctx context.Context, scope Scope, threadID, clientUserMessageID string) (AssistantTurn, error) {
	return s.getAssistantTurn(ctx, scope, threadID, "client_user_message_id=$6", strings.TrimSpace(clientUserMessageID))
}

func (s *PostgresStore) getAssistantTurn(ctx context.Context, scope Scope, threadID, predicate, value string) (AssistantTurn, error) {
	if err := scope.validate(); err != nil {
		return AssistantTurn{}, err
	}
	turn := AssistantTurn{ThreadID: strings.TrimSpace(threadID)}
	query := `SELECT turn_id,actor_id,client_user_message_id,mode,approval_mode,status,checkpoint,terminal_error,created_at,updated_at
		FROM app_studio_assistant_turns WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5 AND ` + predicate
	err := s.db.QueryRowContext(ctx, query, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, turn.ThreadID, value).Scan(
		&turn.ID, &turn.ActorID, &turn.ClientUserMessageID, &turn.Mode, &turn.ApprovalMode, &turn.Status, &turn.Checkpoint, &turn.Error, &turn.CreatedAt, &turn.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AssistantTurn{}, ErrAssistantTurnNotFound
	}
	if err != nil {
		return AssistantTurn{}, fmt.Errorf("get assistant turn: %w", err)
	}
	turn.CreatedAt, turn.UpdatedAt = turn.CreatedAt.UTC(), turn.UpdatedAt.UTC()
	return turn, nil
}

func (s *PostgresStore) ActiveAssistantTurn(ctx context.Context, scope Scope, threadID string) (AssistantTurn, error) {
	if err := scope.validate(); err != nil {
		return AssistantTurn{}, err
	}
	turn := AssistantTurn{ThreadID: strings.TrimSpace(threadID)}
	err := s.db.QueryRowContext(ctx, `SELECT turn_id,actor_id,client_user_message_id,mode,approval_mode,status,checkpoint,terminal_error,created_at,updated_at
		FROM app_studio_assistant_turns WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5 AND status='in_progress'`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, turn.ThreadID).Scan(
		&turn.ID, &turn.ActorID, &turn.ClientUserMessageID, &turn.Mode, &turn.ApprovalMode, &turn.Status, &turn.Checkpoint, &turn.Error, &turn.CreatedAt, &turn.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AssistantTurn{}, ErrAssistantTurnNotFound
	}
	if err != nil {
		return AssistantTurn{}, fmt.Errorf("get active assistant turn: %w", err)
	}
	return turn, nil
}

func (s *PostgresStore) SaveAssistantTurn(ctx context.Context, scope Scope, turn AssistantTurn) error {
	if err := scope.validate(); err != nil {
		return err
	}
	prepared, err := prepareAssistantTurn(turn)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save assistant turn: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE app_studio_assistant_turns SET status=$7,checkpoint=$8,terminal_error=$9,updated_at=$10
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5 AND turn_id=$6
		AND actor_id=$11 AND client_user_message_id=$12 AND mode=$13 AND approval_mode=$14`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		prepared.ThreadID, prepared.ID, prepared.Status, prepared.Checkpoint, prepared.Error, prepared.UpdatedAt, prepared.ActorID,
		prepared.ClientUserMessageID, prepared.Mode, prepared.ApprovalMode)
	if err != nil {
		return fmt.Errorf("save assistant turn: %w", err)
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return ErrAssistantTurnConflict
	}
	threadStatus := AssistantThreadStatusActive
	if assistantTurnStatusTerminal(prepared.Status) {
		threadStatus = AssistantThreadStatusIdle
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_studio_assistant_threads SET status=$6,updated_at=$7
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		prepared.ThreadID, threadStatus, prepared.UpdatedAt); err != nil {
		return fmt.Errorf("project assistant thread status: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresStore) SaveAssistantTurnWithEvent(ctx context.Context, scope Scope, turn AssistantTurn, event AssistantThreadEvent, expectedSequence int64) error {
	if err := scope.validate(); err != nil {
		return err
	}
	prepared, err := prepareAssistantTurn(turn)
	if err != nil {
		return err
	}
	event.ThreadID, event.TurnID = prepared.ThreadID, prepared.ID
	preparedEvent, err := prepareAssistantThreadEvent(event)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save assistant turn with event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, assistantThreadLockKey(scope, prepared.ThreadID)); err != nil {
		return fmt.Errorf("lock assistant thread terminal event: %w", err)
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM app_studio_assistant_thread_events
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, prepared.ThreadID).Scan(&current); err != nil {
		return fmt.Errorf("read assistant thread terminal sequence: %w", err)
	}
	if current != expectedSequence {
		return ErrAssistantThreadEventConflict
	}
	res, err := tx.ExecContext(ctx, `UPDATE app_studio_assistant_turns SET status=$7,checkpoint=$8,terminal_error=$9,updated_at=$10
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5 AND turn_id=$6
		AND actor_id=$11 AND client_user_message_id=$12 AND mode=$13 AND approval_mode=$14`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		prepared.ThreadID, prepared.ID, prepared.Status, prepared.Checkpoint, prepared.Error, prepared.UpdatedAt, prepared.ActorID,
		prepared.ClientUserMessageID, prepared.Mode, prepared.ApprovalMode)
	if err != nil {
		return fmt.Errorf("save assistant turn with event: %w", err)
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return ErrAssistantTurnConflict
	}
	threadStatus := AssistantThreadStatusActive
	if assistantTurnStatusTerminal(prepared.Status) {
		threadStatus = AssistantThreadStatusIdle
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_studio_assistant_threads SET status=$6,updated_at=$7
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		prepared.ThreadID, threadStatus, prepared.UpdatedAt); err != nil {
		return fmt.Errorf("project assistant thread terminal status: %w", err)
	}
	preparedEvent.Sequence = current + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_studio_assistant_thread_events (
		org_uuid,workspace_uuid,project_name,project_uid,thread_id,turn_id,sequence,event_type,item_id,request_id,payload,created_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		preparedEvent.ThreadID, preparedEvent.TurnID, preparedEvent.Sequence, preparedEvent.Type, preparedEvent.ItemID, preparedEvent.RequestID, preparedEvent.Payload, preparedEvent.CreatedAt); err != nil {
		return fmt.Errorf("append assistant turn terminal event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit assistant turn terminal event: %w", err)
	}
	return nil
}

func (s *PostgresStore) AppendAssistantThreadEvent(ctx context.Context, scope Scope, event AssistantThreadEvent, expectedSequence int64) (AssistantThreadEvent, error) {
	if err := scope.validate(); err != nil {
		return AssistantThreadEvent{}, err
	}
	prepared, err := prepareAssistantThreadEvent(event)
	if err != nil {
		return AssistantThreadEvent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantThreadEvent{}, fmt.Errorf("begin append assistant thread event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, assistantThreadLockKey(scope, prepared.ThreadID)); err != nil {
		return AssistantThreadEvent{}, fmt.Errorf("lock assistant thread event stream: %w", err)
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM app_studio_assistant_thread_events
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, prepared.ThreadID).Scan(&current); err != nil {
		return AssistantThreadEvent{}, fmt.Errorf("read assistant thread event sequence: %w", err)
	}
	if current != expectedSequence {
		return AssistantThreadEvent{}, ErrAssistantThreadEventConflict
	}
	prepared.Sequence = current + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_studio_assistant_thread_events (
		org_uuid,workspace_uuid,project_name,project_uid,thread_id,turn_id,sequence,event_type,item_id,request_id,payload,created_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		prepared.ThreadID, prepared.TurnID, prepared.Sequence, prepared.Type, prepared.ItemID, prepared.RequestID, prepared.Payload, prepared.CreatedAt); err != nil {
		return AssistantThreadEvent{}, fmt.Errorf("append assistant thread event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AssistantThreadEvent{}, fmt.Errorf("commit assistant thread event: %w", err)
	}
	return prepared, nil
}

func assistantThreadLockKey(scope Scope, threadID string) string {
	// PostgreSQL text values cannot contain NUL bytes. Length-prefix every
	// component instead: this remains unambiguous when identifiers contain the
	// separator characters themselves and is safe to pass to hashtextextended.
	var key strings.Builder
	for _, component := range []string{
		scope.OrgUUID,
		scope.WorkspaceUUID,
		scope.ProjectName,
		scope.ProjectUID,
		strings.TrimSpace(threadID),
	} {
		_, _ = fmt.Fprintf(&key, "%d:%s", len(component), component)
	}
	return key.String()
}

func (s *PostgresStore) ListAssistantThreadEvents(ctx context.Context, scope Scope, threadID string, afterSequence int64, limit int) ([]AssistantThreadEvent, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	threadID = strings.TrimSpace(threadID)
	rows, err := s.db.QueryContext(ctx, `SELECT turn_id,sequence,event_type,item_id,request_id,payload,created_at
		FROM app_studio_assistant_thread_events WHERE org_uuid=$1 AND workspace_uuid=$2 AND project_name=$3 AND project_uid=$4 AND thread_id=$5 AND sequence>$6
		ORDER BY sequence LIMIT $7`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, threadID, afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("list assistant thread events: %w", err)
	}
	defer rows.Close()
	events := make([]AssistantThreadEvent, 0, limit)
	for rows.Next() {
		event := AssistantThreadEvent{ThreadID: threadID}
		if err := rows.Scan(&event.TurnID, &event.Sequence, &event.Type, &event.ItemID, &event.RequestID, &event.Payload, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan assistant thread event: %w", err)
		}
		event.CreatedAt = event.CreatedAt.UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assistant thread events: %w", err)
	}
	if len(events) == 0 {
		if _, err := s.GetAssistantThread(ctx, scope, threadID); err != nil {
			return nil, err
		}
	}
	return events, nil
}
