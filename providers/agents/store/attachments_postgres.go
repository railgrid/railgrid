/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/railgrid/provider-sdk/attachments"
)

type attachmentRowScanner interface {
	Scan(...any) error
}

const agentAttachmentMetadataColumns = `id,agent_name,agent_uid,session_id,owner,filename,content_type,size_bytes,sha256,draft,created_at,expires_at,draft_expires_at,binding_id`
const agentAttachmentColumns = agentAttachmentMetadataColumns + `,data`

func scanAgentAttachment(row attachmentRowScanner) (AgentAttachment, error) {
	return scanAgentAttachmentFields(row, true)
}

func scanAgentAttachmentMetadata(row attachmentRowScanner) (AgentAttachment, error) {
	return scanAgentAttachmentFields(row, false)
}

func scanAgentAttachmentFields(row attachmentRowScanner, includeData bool) (AgentAttachment, error) {
	var out AgentAttachment
	var expires, draftExpires sql.NullTime
	fields := []any{&out.ID, &out.AgentName, &out.AgentUID, &out.SessionID, &out.ActorID,
		&out.Filename, &out.ContentType, &out.SizeBytes, &out.SHA256, &out.Draft,
		&out.CreatedAt, &expires, &draftExpires, &out.BindingID}
	if includeData {
		fields = append(fields, &out.Data)
	}
	err := row.Scan(fields...)
	if err != nil {
		return AgentAttachment{}, err
	}
	if expires.Valid {
		at := expires.Time.UTC()
		out.ExpiresAt = &at
	}
	if draftExpires.Valid {
		at := draftExpires.Time.UTC()
		out.DraftExpiresAt = &at
	}
	out.CreatedAt = out.CreatedAt.UTC()
	return out, nil
}

func (p *PostgresStore) ConfigureAgentAttachmentQuota(quota AgentAttachmentQuota) error {
	if err := agentAttachmentQuotaValid(quota); err != nil {
		return err
	}
	p.attachmentQuota = quota
	return nil
}

func (p *PostgresStore) CreateAgentAttachment(ctx context.Context, scope Scope, attachment AgentAttachment) (AgentAttachment, bool, error) {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return AgentAttachment{}, false, err
	}
	if attachment.AgentName != scope.AgentName || !attachment.Draft || attachment.BindingID != "" {
		return AgentAttachment{}, false, fmt.Errorf("uploads must be unbound drafts for the addressed Agent")
	}
	if err := attachment.validate(); err != nil {
		return AgentAttachment{}, false, err
	}
	attachment.CreatedAt = attachment.CreatedAt.UTC()
	attachment.ContentType, _ = attachments.NormalizeAttachmentContentType(attachment.ContentType)
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return AgentAttachment{}, false, err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if mutation.tx == nil {
		tx, beginErr := p.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return AgentAttachment{}, false, beginErr
		}
		mutation.tx, mutation.executor = tx, tx
	}
	if _, err := mutation.executor.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
		"agents-attachment-quota:"+scope.OrgUUID+":"+scope.WorkspaceUUID); err != nil {
		return AgentAttachment{}, false, err
	}
	if _, err := mutation.executor.ExecContext(ctx, `
		DELETE FROM agents_agent_attachments
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND draft=TRUE AND (expires_at IS NULL OR expires_at <= $3)`,
		scope.OrgUUID, scope.WorkspaceUUID, attachment.CreatedAt); err != nil {
		return AgentAttachment{}, false, err
	}
	keyQuery := `SELECT ` + agentAttachmentColumns + ` FROM agents_agent_attachments
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND id=$5 FOR UPDATE`
	existing, readErr := scanAgentAttachment(mutation.executor.QueryRowContext(ctx, keyQuery,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, attachment.AgentUID, attachment.ID))
	if readErr == nil {
		if existing.ActorID == attachment.ActorID && existing.SessionID == attachment.SessionID &&
			attachments.UploadMetadataMatches(existing.Receipt(), attachment.Receipt()) && string(existing.Data) == string(attachment.Data) {
			if err := mutation.commit(); err != nil {
				return AgentAttachment{}, false, err
			}
			return existing, false, nil
		}
		return AgentAttachment{}, false, ErrAgentAttachmentConflict
	}
	if !errors.Is(readErr, sql.ErrNoRows) {
		return AgentAttachment{}, false, readErr
	}
	quota := p.attachmentQuota
	if quota == (AgentAttachmentQuota{}) {
		quota = DefaultAgentAttachmentQuota()
	}
	var workspaceBytes, draftBytes int64
	var draftCount int
	if err := mutation.executor.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(size_bytes),0) FROM agents_agent_attachments
		WHERE org_uuid=$1 AND workspace_uuid=$2`, scope.OrgUUID, scope.WorkspaceUUID).Scan(&workspaceBytes); err != nil {
		return AgentAttachment{}, false, err
	}
	if err := mutation.executor.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(size_bytes),0), COUNT(*) FROM agents_agent_attachments
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND owner=$4 AND draft=TRUE`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, attachment.ActorID).Scan(&draftBytes, &draftCount); err != nil {
		return AgentAttachment{}, false, err
	}
	if quota.WorkspaceMaxBytes > 0 && workspaceBytes+attachment.SizeBytes > quota.WorkspaceMaxBytes ||
		quota.DraftAgentMaxBytes > 0 && draftBytes+attachment.SizeBytes > quota.DraftAgentMaxBytes ||
		quota.DraftAgentMaxCount > 0 && draftCount+1 > quota.DraftAgentMaxCount {
		return AgentAttachment{}, false, ErrAgentAttachmentQuota
	}
	err = mutation.executor.QueryRowContext(ctx, `
		INSERT INTO agents_agent_attachments
			(id,org_uuid,workspace_uuid,agent_name,agent_uid,session_id,owner,filename,content_type,size_bytes,sha256,draft,created_at,expires_at,binding_id,data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,TRUE,$12,$13,'',$14)
		RETURNING created_at`,
		attachment.ID, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, attachment.AgentUID, attachment.SessionID,
		attachment.ActorID, attachment.Filename, attachment.ContentType, attachment.SizeBytes, attachment.SHA256,
		attachment.CreatedAt.UTC(), attachment.ExpiresAt.UTC(), attachment.Data).Scan(&attachment.CreatedAt)
	if err != nil {
		return AgentAttachment{}, false, err
	}
	if err := mutation.commit(); err != nil {
		return AgentAttachment{}, false, err
	}
	// The returned receipt must use the database's timestamp precision, so a
	// later exact receipt check agrees with a freshly read row.
	attachment.CreatedAt = attachment.CreatedAt.UTC()
	return attachment.clone(), true, nil
}

func (p *PostgresStore) ListAgentAttachments(ctx context.Context, scope Scope, agentUID, sessionID, actorID string, limit int) ([]AgentAttachment, error) {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(agentUID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(actorID) == "" {
		return nil, fmt.Errorf("agent UID, session, and actor are required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	now := time.Now().UTC()
	if err := p.pruneExpiredAgentAttachmentDrafts(ctx, scope, now); err != nil {
		return nil, err
	}
	rows, err := p.db.QueryContext(ctx, `SELECT `+agentAttachmentMetadataColumns+`
		FROM agents_agent_attachments
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND session_id=$5 AND owner=$6
			AND (draft=FALSE OR expires_at>$7)
		ORDER BY created_at DESC,id ASC LIMIT $8`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, sessionID, actorID, now, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]AgentAttachment, 0)
	for rows.Next() {
		item, scanErr := scanAgentAttachmentMetadata(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// pruneExpiredAgentAttachmentDrafts bounds cleanup work to one batch in the
// caller's resolved workspace. Listing a collection can therefore reclaim
// abandoned drafts without adding a global timer sweep.
func (p *PostgresStore) pruneExpiredAgentAttachmentDrafts(ctx context.Context, scope Scope, now time.Time) error {
	_, err := p.db.ExecContext(ctx, `
		WITH expired AS (
			SELECT ctid FROM agents_agent_attachments
			WHERE org_uuid=$1 AND workspace_uuid=$2 AND draft=TRUE AND (expires_at IS NULL OR expires_at<=$3)
			ORDER BY expires_at NULLS FIRST, created_at ASC, id ASC
			LIMIT $4
		)
		DELETE FROM agents_agent_attachments AS attachments
		USING expired
		WHERE attachments.ctid=expired.ctid AND attachments.org_uuid=$1 AND attachments.workspace_uuid=$2
			AND attachments.draft=TRUE AND (attachments.expires_at IS NULL OR attachments.expires_at<=$3)`,
		scope.OrgUUID, scope.WorkspaceUUID, now, agentAttachmentDraftCleanupBatch)
	return err
}

func (p *PostgresStore) GetAgentAttachment(ctx context.Context, scope Scope, agentUID, sessionID, actorID, id string) (AgentAttachment, error) {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return AgentAttachment{}, err
	}
	item, err := scanAgentAttachment(p.db.QueryRowContext(ctx, `SELECT `+agentAttachmentColumns+`
		FROM agents_agent_attachments
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND id=$5
			AND session_id=$6 AND owner=$7 AND (draft=FALSE OR expires_at>$8)`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, id, sessionID, actorID, time.Now().UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentAttachment{}, ErrAgentAttachmentNotFound
	}
	return item, err
}

func (p *PostgresStore) DeleteAgentAttachment(ctx context.Context, scope Scope, agentUID, sessionID, actorID, id string) error {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	res, err := p.db.ExecContext(ctx, `DELETE FROM agents_agent_attachments
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND id=$5
			AND session_id=$6 AND owner=$7 AND draft=TRUE AND expires_at>$8`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, id, sessionID, actorID, time.Now().UTC())
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err != nil || affected > 0 {
		return err
	}
	var draft bool
	err = p.db.QueryRowContext(ctx, `SELECT draft FROM agents_agent_attachments
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND id=$5 AND session_id=$6 AND owner=$7`,
		scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, id, sessionID, actorID).Scan(&draft)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAgentAttachmentNotFound
	}
	if err != nil {
		return err
	}
	if !draft {
		return ErrAgentAttachmentImmutable
	}
	return ErrAgentAttachmentNotFound
}

func (p *PostgresStore) ReconcileAgentAttachmentBindings(ctx context.Context) error {
	now := time.Now().UTC()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM agents_agent_attachments
		WHERE draft=FALSE AND binding_id<>'' AND
			(draft_expires_at IS NULL OR draft_expires_at<=$1) AND NOT EXISTS (
				SELECT 1 FROM agents_runs runs WHERE runs.org_uuid=agents_agent_attachments.org_uuid AND
					runs.workspace_uuid=agents_agent_attachments.workspace_uuid AND runs.agent_name=agents_agent_attachments.agent_name AND
					runs.id=agents_agent_attachments.binding_id)`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents_agent_attachments AS attachments SET
			draft=TRUE, expires_at=attachments.draft_expires_at, draft_expires_at=NULL, binding_id=''
		WHERE attachments.draft=FALSE AND attachments.binding_id<>'' AND attachments.draft_expires_at>$1 AND NOT EXISTS (
			SELECT 1 FROM agents_runs runs WHERE runs.org_uuid=attachments.org_uuid AND
				runs.workspace_uuid=attachments.workspace_uuid AND runs.agent_name=attachments.agent_name AND runs.id=attachments.binding_id)`, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM agents_agent_attachments WHERE draft=TRUE AND (expires_at IS NULL OR expires_at<=$1)`, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (p *PostgresStore) CreateAgentChatRun(ctx context.Context, scope Scope, agentUID string, msg Message, run Run, receipts []AgentAttachmentReceipt, actorID string, now time.Time) error {
	scope = p.normalizeScope(ctx, scope)
	if err := scope.withAgent(); err != nil {
		return err
	}
	if err := validateAgentAttachmentAdmission(receipts, scope, agentUID, msg.SessionID, actorID, run.ID); err != nil {
		return err
	}
	if msg.ID == "" || msg.Role != "user" || msg.RunID != run.ID || msg.SessionID != run.SessionID || run.AgentName != scope.AgentName || run.Phase == "" || msg.CreatedAt.IsZero() {
		return fmt.Errorf("atomic chat admission needs a matching user message and run")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	mutation, err := p.beginScopedMutation(ctx, scope)
	if err != nil {
		return err
	}
	defer mutation.rollback()
	scope = mutation.scope
	if mutation.tx == nil {
		tx, beginErr := p.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		mutation.tx, mutation.executor = tx, tx
	}
	if err := lockTranscriptSession(ctx, mutation.executor, scope, msg.SessionID); err != nil {
		return err
	}
	items := make([]AgentAttachment, 0, len(receipts))
	for _, receipt := range receipts {
		item, scanErr := scanAgentAttachment(mutation.executor.QueryRowContext(ctx, `SELECT `+agentAttachmentColumns+`
			FROM agents_agent_attachments
			WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND id=$5 AND session_id=$6
			FOR UPDATE`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, receipt.ID, msg.SessionID))
		if errors.Is(scanErr, sql.ErrNoRows) {
			return ErrAgentAttachmentNotFound
		}
		if scanErr != nil {
			return scanErr
		}
		items = append(items, item)
	}
	bound, err := bindAgentAttachmentBatch(items, receipts, actorID, run.ID, now.UTC())
	if err != nil {
		return err
	}
	metadata, err := marshalJSONB(msg.Metadata)
	if err != nil {
		return err
	}
	if _, err := mutation.executor.ExecContext(ctx, `
		INSERT INTO agents_messages (id,org_uuid,workspace_uuid,agent_name,session_id,run_id,role,content,content_encrypted,content_key_id,metadata,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		msg.ID, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, msg.SessionID, msg.RunID, msg.Role,
		msg.Content, msg.ContentEncrypted, msg.ContentKeyID, metadata, msg.CreatedAt.UTC()); err != nil {
		return err
	}
	if err := insertAgentChatRun(ctx, mutation.executor, scope, run); err != nil {
		return err
	}
	for _, item := range bound {
		if _, err := mutation.executor.ExecContext(ctx, `UPDATE agents_agent_attachments
			SET draft=FALSE, expires_at=NULL, draft_expires_at=$8, binding_id=$9
			WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND agent_uid=$4 AND id=$5 AND session_id=$6 AND owner=$7`,
			scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, item.ID, msg.SessionID, actorID, item.DraftExpiresAt, run.ID); err != nil {
			return err
		}
	}
	return mutation.commit()
}

func insertAgentChatRun(ctx context.Context, executor scopedExecutor, scope Scope, run Run) error {
	sources, err := marshalJSONB(run.Sources)
	if err != nil {
		return err
	}
	var delivery, harnessSettings, repository, result, modelSettings any
	if run.Delivery != nil {
		delivery, err = marshalJSONB(run.Delivery)
	}
	if err == nil && run.HarnessSettings != nil {
		harnessSettings, err = marshalJSONB(run.HarnessSettings)
	}
	if err == nil && run.Repository != nil {
		repository, err = marshalJSONB(run.Repository)
	}
	if err == nil && run.Result != nil {
		result, err = marshalJSONB(run.Result)
	}
	if err == nil && run.ModelSettings != nil {
		modelSettings, err = marshalJSONB(run.ModelSettings)
	}
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(ctx, `
		INSERT INTO agents_runs
			(id,org_uuid,workspace_uuid,agent_name,session_id,trigger_kind,parent_run_id,phase,attempt,input,output,sources,idempotency_key,delivery,message,checkpoint,input_tokens,output_tokens,usd_micros,created_at,updated_at,started_at,finished_at,worked_duration_ms,backend,attempt_id,harness_session_id,repository,result,retries,retry_at,harness_settings,model_settings)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33)`,
		run.ID, scope.OrgUUID, scope.WorkspaceUUID, run.AgentName, run.SessionID, run.Trigger, run.ParentRunID,
		string(run.Phase), run.Attempt, run.Input, run.Output, sources, run.IdempotencyKey, delivery, run.Message,
		nullBytes(run.Checkpoint), run.InputTokens, run.OutputTokens, run.USDMicros,
		run.CreatedAt.UTC(), run.UpdatedAt.UTC(), nullTime(run.StartedAt), nullTime(run.FinishedAt), nullInt64(run.WorkedDurationMS),
		run.Backend, run.AttemptID, run.HarnessSessionID, repository, result, run.Retries, nullTime(run.RetryAt), harnessSettings, modelSettings)
	return err
}

var _ AgentAttachmentStore = (*PostgresStore)(nil)
var _ AgentChatAdmissionStore = (*PostgresStore)(nil)
var _ AgentAttachmentQuotaConfigurer = (*PostgresStore)(nil)
