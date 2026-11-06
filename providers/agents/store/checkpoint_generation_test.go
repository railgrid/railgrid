// Copyright 2026 The Railgrid Authors.
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
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryDeleteRunInvalidatesSessionCheckpoint(t *testing.T) {
	mem := NewMemoryStore()
	runDeleteInvalidatesSessionCheckpoint(t, mem, mem, attachmentTestBaseScope())
}

func TestPostgresDeleteRunInvalidatesSessionCheckpoint(t *testing.T) {
	pg := openTestPostgres(t)
	base := attachmentTestBaseScope()
	runDeleteInvalidatesSessionCheckpoint(t, pg, pg, base)
	t.Cleanup(func() { purgeAgentAttachmentTestOrg(t, pg, base.OrgUUID) })
}

func TestMemoryScopeMigrationAdvancesCheckpointGeneration(t *testing.T) {
	mem := NewMemoryStore()
	checkpointGenerationAdvancesDuringScopeMigration(t, mem, nil)
}

func TestPostgresScopeMigrationAdvancesCheckpointGeneration(t *testing.T) {
	pg := openTestPostgres(t)
	checkpointGenerationAdvancesDuringScopeMigration(t, pg, pg)
}

func checkpointGenerationAdvancesDuringScopeMigration(t *testing.T, data Store, pg *PostgresStore) {
	t.Helper()
	ctx := context.Background()
	clusterID := "checkpoint-migration-" + uuid.NewString()
	fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: clusterID, AgentName: "assistant", ClusterID: clusterID}
	canonical := Scope{OrgUUID: "org-" + uuid.NewString(), WorkspaceUUID: "workspace-" + uuid.NewString(), AgentName: fallback.AgentName, ClusterID: clusterID}
	sessionID := "session-" + uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if pg != nil {
		t.Cleanup(func() {
			for _, scope := range []Scope{fallback, canonical} {
				for _, table := range []string{"agents_session_summaries", "agents_session_checkpoint_generations"} {
					if _, err := pg.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE org_uuid=$1 AND workspace_uuid=$2", scope.OrgUUID, scope.WorkspaceUUID); err != nil {
						t.Logf("cleanup scope-migration checkpoint rows from %s: %v", table, err)
					}
				}
			}
			if _, err := pg.db.ExecContext(ctx, `DELETE FROM agents_tenants WHERE cluster_id=$1`, clusterID); err != nil {
				t.Logf("cleanup scope-migration tenant mapping: %v", err)
			}
		})
	}
	fallbackSummary := SessionSummary{
		SessionID: sessionID, Summary: "fallback history", ThroughAt: now,
		MessageCount: 1, CreatedAt: now, UpdatedAt: now,
	}
	canonicalSummary := fallbackSummary
	canonicalSummary.Summary = "canonical history"
	canonicalSummary.UpdatedAt = now.Add(time.Second)
	if err := data.PutSessionSummary(ctx, fallback, fallbackSummary); err != nil {
		t.Fatalf("put fallback summary: %v", err)
	}
	if err := data.PutSessionSummary(ctx, canonical, canonicalSummary); err != nil {
		t.Fatalf("put canonical summary: %v", err)
	}
	staleWriter := canonicalSummary
	ref := TenantRef{OrgUUID: canonical.OrgUUID, WorkspaceUUID: canonical.WorkspaceUUID, UpdatedAt: now.Add(2 * time.Second)}
	if pg != nil {
		lockTx, err := pg.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin destination checkpoint lock: %v", err)
		}
		defer func() { _ = lockTx.Rollback() }()
		if err := lockTranscriptSession(ctx, lockTx, canonical, sessionID); err != nil {
			t.Fatalf("lock destination checkpoint session: %v", err)
		}
		migrationDone := make(chan error, 1)
		go func() { migrationDone <- data.SaveTenantRef(ctx, clusterID, ref) }()
		waitForAdvisoryLockWaiter(t, pg)
		if err := lockTx.Commit(); err != nil {
			t.Fatalf("release destination checkpoint lock: %v", err)
		}
		if err := <-migrationDone; err != nil {
			t.Fatalf("migrate fallback scope: %v", err)
		}
	} else if err := data.SaveTenantRef(ctx, clusterID, ref); err != nil {
		t.Fatalf("migrate fallback scope: %v", err)
	}
	merged, found, err := data.GetSessionSummary(ctx, canonical, sessionID)
	if err != nil || !found {
		t.Fatalf("get merged checkpoint after migration: found=%t summary=%+v err=%v", found, merged, err)
	}
	if merged.Generation != 1 {
		t.Fatalf("merged checkpoint generation = %d, want 1", merged.Generation)
	}
	if err := data.PutSessionSummary(ctx, canonical, staleWriter); !errors.Is(err, ErrSessionCheckpointStale) {
		t.Fatalf("pre-migration compactor write error = %v, want ErrSessionCheckpointStale", err)
	}
	merged.UpdatedAt = now.Add(3 * time.Second)
	if err := data.PutSessionSummary(ctx, canonical, merged); err != nil {
		t.Fatalf("fresh compactor write after migration: %v", err)
	}
}

func runDeleteInvalidatesSessionCheckpoint(t *testing.T, data Store, blobs AgentAttachmentStore, base Scope) {
	t.Helper()
	ctx := context.Background()
	scope := attachmentTestScope(base, "checkpoint-delete")
	sessionID := "session-" + uuid.NewString()
	runID := "run-" + uuid.NewString()
	agentUID := "uid-" + uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)

	attachmentID := "att-" + uuid.NewString()
	attachment, _, err := blobs.CreateAgentAttachment(ctx, scope,
		testAgentAttachment(scope, agentUID, sessionID, "alice", attachmentID, []byte("upload"), now, now.Add(time.Hour)))
	if err != nil {
		t.Fatalf("create attachment draft: %v", err)
	}
	message, run := testChatAdmission(scope, runID, sessionID, now.Add(time.Second))
	if err := data.(AgentChatAdmissionStore).CreateAgentChatRun(ctx, scope, agentUID, message, run,
		[]AgentAttachmentReceipt{attachment.Receipt()}, "alice", now.Add(time.Second)); err != nil {
		t.Fatalf("admit attached turn: %v", err)
	}
	page, err := data.ListMessages(ctx, scope, sessionID, 20, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list attached transcript: items=%+v err=%v", page.Items, err)
	}
	attachedMessage := page.Items[0]
	oldSummary := SessionSummary{
		SessionID: sessionID, Summary: "folded conversation", ThroughAt: attachedMessage.CreatedAt,
		MessageCount: 1, CreatedAt: now, UpdatedAt: now,
		Checkpoint: &SessionCheckpoint{
			Version: 1, ThroughSequence: attachedMessage.Sequence, ThroughMessageID: attachedMessage.ID,
			ThroughAt: attachedMessage.CreatedAt,
			ReplacementHistory: []SessionCheckpointMessage{{
				ID: attachedMessage.ID, Sequence: attachedMessage.Sequence, Role: "user", Content: attachedMessage.Content,
				InputAttachments: []AgentAttachmentReceipt{attachment.Receipt()}, InputAttachmentOwner: "alice", InputAttachmentBindingID: runID,
			}},
		},
	}
	if err := data.PutSessionSummary(ctx, scope, oldSummary); err != nil {
		t.Fatalf("persist compacted attachment checkpoint: %v", err)
	}

	survivor := Message{
		ID: "survivor-" + uuid.NewString(), AgentName: scope.AgentName, SessionID: sessionID,
		RunID: "surviving-run", Role: "user", Content: "keep this turn", CreatedAt: now.Add(2 * time.Second),
	}
	if err := data.AppendMessage(ctx, scope, survivor); err != nil {
		t.Fatalf("append surviving turn: %v", err)
	}
	if err := data.DeleteRunData(ctx, scope, runID); err != nil {
		t.Fatalf("delete attached run: %v", err)
	}
	if _, err := blobs.GetAgentAttachment(ctx, scope, agentUID, sessionID, "alice", attachmentID); !errors.Is(err, ErrAgentAttachmentNotFound) {
		t.Fatalf("deleted run attachment still exists: %v", err)
	}
	invalidated, found, err := data.GetSessionSummary(ctx, scope, sessionID)
	if err != nil || found {
		t.Fatalf("deleted run left a replayable summary: found=%t summary=%+v err=%v", found, invalidated, err)
	}
	if invalidated.Generation != 1 {
		t.Fatalf("checkpoint generation after deletion = %d, want 1", invalidated.Generation)
	}
	if err := data.PutSessionSummary(ctx, scope, oldSummary); !errors.Is(err, ErrSessionCheckpointStale) {
		t.Fatalf("stale compactor write error = %v, want ErrSessionCheckpointStale", err)
	}

	page, err = data.ListMessages(ctx, scope, sessionID, 20, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != survivor.ID {
		t.Fatalf("surviving session transcript = %+v, err=%v", page.Items, err)
	}
	fresh := SessionSummary{
		SessionID: sessionID, Summary: "surviving conversation", ThroughAt: page.Items[0].CreatedAt,
		MessageCount: 1, CreatedAt: now, UpdatedAt: now.Add(3 * time.Second), Generation: invalidated.Generation,
		Checkpoint: &SessionCheckpoint{
			Version: 1, ThroughSequence: page.Items[0].Sequence, ThroughMessageID: page.Items[0].ID,
			ThroughAt: page.Items[0].CreatedAt,
			ReplacementHistory: []SessionCheckpointMessage{{
				ID: page.Items[0].ID, Sequence: page.Items[0].Sequence, Role: "user", Content: page.Items[0].Content,
			}},
		},
	}
	if err := data.PutSessionSummary(ctx, scope, fresh); err != nil {
		t.Fatalf("recompact surviving session at new generation: %v", err)
	}
	if got, found, err := data.GetSessionSummary(ctx, scope, sessionID); err != nil || !found || got.Checkpoint == nil || got.Checkpoint.ThroughMessageID != survivor.ID {
		t.Fatalf("new surviving-session checkpoint = %+v found=%t err=%v", got, found, err)
	}
}
