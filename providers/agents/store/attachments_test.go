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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/railgrid/provider-sdk/attachments"
)

func TestMemoryAgentAttachmentStore(t *testing.T) {
	mem := NewMemoryStore()
	runAgentAttachmentStoreTests(t, mem, mem, attachmentTestBaseScope())
}

func TestPostgresAgentAttachmentStore(t *testing.T) {
	pg := openTestPostgres(t)
	base := attachmentTestBaseScope()
	runAgentAttachmentStoreTests(t, pg, pg, base)
	t.Cleanup(func() { purgeAgentAttachmentTestOrg(t, pg, base.OrgUUID) })
}

func TestPostgresAgentAttachmentFallbackMappingRace(t *testing.T) {
	pg := openTestPostgres(t)
	ctx := context.Background()
	t.Run("upload adopts the tenant mapping published after fallback resolution", func(t *testing.T) {
		clusterID := "attachment-map-upload-" + uuid.NewString()
		fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: clusterID, AgentName: "assistant", ClusterID: clusterID}
		mapped := Scope{OrgUUID: "org-" + uuid.NewString(), WorkspaceUUID: "workspace-" + uuid.NewString(), AgentName: fallback.AgentName, ClusterID: clusterID}
		defer purgeAttachmentFallbackRace(t, pg, clusterID, mapped.OrgUUID)

		lockTx := holdTenantMappingLock(t, pg, clusterID)
		defer func() { _ = lockTx.Rollback() }()
		createdAt := time.Now().UTC().Truncate(time.Microsecond)
		attachment := testAgentAttachment(fallback, "uid-race", "session", "alice", "racing-upload", []byte("racing bytes"), createdAt, createdAt.Add(time.Hour))
		done := make(chan error, 1)
		go func() {
			_, _, err := pg.CreateAgentAttachment(ctx, fallback, attachment)
			done <- err
		}()
		waitForAdvisoryLockWaiter(t, pg)
		publishTenantMappingUnderLock(t, ctx, lockTx, clusterID, mapped)
		if err := lockTx.Commit(); err != nil {
			t.Fatalf("publish tenant mapping: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("upload after mapping publication: %v", err)
		}
		if _, err := pg.GetAgentAttachment(ctx, mapped, "uid-race", "session", "alice", attachment.ID); err != nil {
			t.Fatalf("upload was not stored in canonical tenant scope: %v", err)
		}
		if exists := postgresAttachmentExists(t, pg, fallback, attachment.ID); exists {
			t.Fatal("upload was also written to stale fallback scope")
		}
	})

	t.Run("atomic admission adopts the tenant mapping after fallback resolution", func(t *testing.T) {
		clusterID := "attachment-map-admission-" + uuid.NewString()
		fallback := Scope{OrgUUID: UnmappedOrg, WorkspaceUUID: clusterID, AgentName: "assistant", ClusterID: clusterID}
		mapped := Scope{OrgUUID: "org-" + uuid.NewString(), WorkspaceUUID: "workspace-" + uuid.NewString(), AgentName: fallback.AgentName, ClusterID: clusterID}
		defer purgeAttachmentFallbackRace(t, pg, clusterID, mapped.OrgUUID)

		now := time.Now().UTC().Truncate(time.Microsecond)
		attachment := testAgentAttachment(fallback, "uid-race", "session", "alice", "admission-upload", []byte("admission bytes"), now, now.Add(time.Hour))
		created, _, err := pg.CreateAgentAttachment(ctx, fallback, attachment)
		if err != nil {
			t.Fatalf("create fallback draft before mapping: %v", err)
		}
		lockTx := holdTenantMappingLock(t, pg, clusterID)
		defer func() { _ = lockTx.Rollback() }()
		message, run := testChatAdmission(fallback, "racing-run", "session", now.Add(time.Second))
		done := make(chan error, 1)
		go func() {
			done <- pg.CreateAgentChatRun(ctx, fallback, "uid-race", message, run, []AgentAttachmentReceipt{created.Receipt()}, "alice", now.Add(time.Second))
		}()
		waitForAdvisoryLockWaiter(t, pg)
		publishTenantMappingUnderLock(t, ctx, lockTx, clusterID, mapped)
		if err := lockTx.Commit(); err != nil {
			t.Fatalf("publish tenant mapping: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("atomic admission after mapping publication: %v", err)
		}
		if run, err := pg.GetRun(ctx, mapped, run.ID); err != nil || run.ID != "racing-run" {
			t.Fatalf("run was not admitted in canonical tenant scope: run=%+v err=%v", run, err)
		}
		page, err := pg.ListMessages(ctx, mapped, "session", 10, "")
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != message.ID {
			t.Fatalf("message was not admitted in canonical tenant scope: %+v err=%v", page.Items, err)
		}
		bound, err := pg.GetAgentAttachment(ctx, mapped, "uid-race", "session", "alice", created.ID)
		if err != nil || bound.Draft || bound.BindingID != run.ID {
			t.Fatalf("attachment was not atomically bound in canonical scope: %+v err=%v", bound, err)
		}
		if exists := postgresAttachmentExists(t, pg, fallback, created.ID); exists {
			t.Fatal("atomic admission left the attachment in stale fallback scope")
		}
	})
}

func runAgentAttachmentStoreTests(t *testing.T, blobs AgentAttachmentStore, admission AgentChatAdmissionStore, base Scope) {
	t.Helper()
	ctx := context.Background()

	t.Run("isolation by tenant agent uid actor and session", func(t *testing.T) {
		scope := attachmentTestScope(base, "isolation")
		now := time.Now().UTC().Truncate(time.Microsecond)
		if _, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-current", "session-a", "alice", "same-id", []byte("alice/current"), now, now.Add(time.Hour))); err != nil {
			t.Fatalf("create current Agent attachment: %v", err)
		}
		if _, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-current", "session-a", "bob", "bob-only", []byte("bob"), now, now.Add(time.Hour))); err != nil {
			t.Fatalf("create other-owner attachment: %v", err)
		}
		if _, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-current", "session-b", "alice", "other-session", []byte("session-b"), now, now.Add(time.Hour))); err != nil {
			t.Fatalf("create other-session attachment: %v", err)
		}

		recreated := scope
		if _, _, err := blobs.CreateAgentAttachment(ctx, recreated, testAgentAttachment(recreated, "uid-recreated", "session-a", "alice", "recreated-only", []byte("new Agent UID"), now, now.Add(time.Hour))); err != nil {
			t.Fatalf("create same-named recreated Agent attachment: %v", err)
		}
		otherTenant := scope
		otherTenant.OrgUUID += "-other"
		otherTenant.WorkspaceUUID += "-other"
		if _, _, err := blobs.CreateAgentAttachment(ctx, otherTenant, testAgentAttachment(otherTenant, "uid-current", "session-a", "alice", "same-id", []byte("other tenant"), now, now.Add(time.Hour))); err != nil {
			t.Fatalf("create identically named Agent attachment in another tenant: %v", err)
		}
		if _, _, err := blobs.CreateAgentAttachment(ctx, otherTenant, testAgentAttachment(otherTenant, "uid-current", "session-a", "alice", "tenant-only", []byte("tenant only"), now, now.Add(time.Hour))); err != nil {
			t.Fatalf("create tenant-only attachment: %v", err)
		}
		otherAgent := scope
		otherAgent.AgentName = "another-agent"
		if _, _, err := blobs.CreateAgentAttachment(ctx, otherAgent, testAgentAttachment(otherAgent, "uid-current", "session-a", "alice", "other-agent-only", []byte("other Agent name"), now, now.Add(time.Hour))); err != nil {
			t.Fatalf("create another Agent attachment: %v", err)
		}

		got, err := blobs.GetAgentAttachment(ctx, scope, "uid-current", "session-a", "alice", "same-id")
		if err != nil || string(got.Data) != "alice/current" {
			t.Fatalf("get exact owner/session/UID row = %q, err=%v", got.Data, err)
		}
		for _, tc := range []struct {
			name      string
			scope     Scope
			agentUID  string
			sessionID string
			owner     string
			id        string
		}{
			{name: "different owner", scope: scope, agentUID: "uid-current", sessionID: "session-a", owner: "alice", id: "bob-only"},
			{name: "different session", scope: scope, agentUID: "uid-current", sessionID: "session-b", owner: "bob", id: "same-id"},
			{name: "different Agent UID", scope: scope, agentUID: "uid-current", sessionID: "session-a", owner: "alice", id: "recreated-only"},
			{name: "different tenant", scope: scope, agentUID: "uid-current", sessionID: "session-a", owner: "bob", id: "same-id"},
			{name: "tenant-only ID", scope: scope, agentUID: "uid-current", sessionID: "session-a", owner: "alice", id: "tenant-only"},
			{name: "different Agent name", scope: scope, agentUID: "uid-current", sessionID: "session-a", owner: "alice", id: "other-agent-only"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := blobs.GetAgentAttachment(ctx, tc.scope, tc.agentUID, tc.sessionID, tc.owner, tc.id); !errors.Is(err, ErrAgentAttachmentNotFound) {
					t.Fatalf("cross-boundary get error = %v, want ErrAgentAttachmentNotFound", err)
				}
			})
		}
		items, err := blobs.ListAgentAttachments(ctx, scope, "uid-current", "session-a", "alice", 50)
		if err != nil || len(items) != 1 || items[0].ID != "same-id" {
			t.Fatalf("list exact owner/session/UID returned %+v, err=%v", items, err)
		}
		if len(items[0].Data) != 0 {
			t.Fatalf("list loaded attachment bytes: got %d bytes, want metadata only", len(items[0].Data))
		}
		if got, err := blobs.GetAgentAttachment(ctx, scope, "uid-current", "session-a", "alice", "same-id"); err != nil || string(got.Data) != "alice/current" {
			t.Fatalf("get after metadata-only list returned %q, err=%v", got.Data, err)
		}
		otherTenantItem, err := blobs.GetAgentAttachment(ctx, otherTenant, "uid-current", "session-a", "alice", "same-id")
		if err != nil || string(otherTenantItem.Data) != "other tenant" {
			t.Fatalf("same-named Agent in another tenant returned %q, err=%v", otherTenantItem.Data, err)
		}
	})

	t.Run("collection read reclaims expired workspace drafts", func(t *testing.T) {
		scope := attachmentTestScope(base, "idle-expiry")
		otherTenant := scope
		otherTenant.OrgUUID += "-other"
		otherTenant.WorkspaceUUID += "-other"
		now := time.Now().UTC().Truncate(time.Microsecond)
		createdAt := now.Add(-2 * time.Hour)
		expiresAt := now.Add(-time.Hour)
		expired, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-expired", "idle-session", "alice", "idle-expired", []byte("expired"), createdAt, expiresAt))
		if err != nil {
			t.Fatalf("create expired draft: %v", err)
		}
		foreign, _, err := blobs.CreateAgentAttachment(ctx, otherTenant, testAgentAttachment(otherTenant, "uid-expired", "idle-session", "alice", "foreign-expired", []byte("foreign"), createdAt, expiresAt))
		if err != nil {
			t.Fatalf("create other-tenant expired draft: %v", err)
		}
		if !attachmentRowExists(t, blobs, scope, expired.ID) || !attachmentRowExists(t, blobs, otherTenant, foreign.ID) {
			t.Fatal("expired drafts were not stored before the collection read")
		}
		items, err := blobs.ListAgentAttachments(ctx, scope, "uid-current", "idle-session", "alice", 50)
		if err != nil || len(items) != 0 {
			t.Fatalf("idle collection read returned items %+v, err=%v", items, err)
		}
		if attachmentRowExists(t, blobs, scope, expired.ID) {
			t.Fatal("collection read left the expired workspace draft stored")
		}
		if !attachmentRowExists(t, blobs, otherTenant, foreign.ID) {
			t.Fatal("collection read cleaned an expired draft from another tenant")
		}
	})

	t.Run("same client id upload retry ignores server timestamp", func(t *testing.T) {
		scope := attachmentTestScope(base, "retry")
		// Real HTTP uploads carry nanoseconds; PostgreSQL stores microseconds.
		// Keep that difference visible rather than normalizing the fixture.
		firstAt := time.Now().UTC().Truncate(time.Second).Add(123456789 * time.Nanosecond)
		first := testAgentAttachment(scope, "uid-retry", "session", "alice", "client-id", []byte("same upload"), firstAt, firstAt.Add(attachments.DefaultDraftRetention))
		created, inserted, err := blobs.CreateAgentAttachment(ctx, scope, first)
		if err != nil || !inserted {
			t.Fatalf("create first upload = inserted %t, err=%v", inserted, err)
		}
		stored, err := blobs.GetAgentAttachment(ctx, scope, "uid-retry", "session", "alice", first.ID)
		if err != nil || !attachments.ReceiptsMatch(created.Receipt(), stored.Receipt()) {
			t.Fatalf("created receipt differs from persisted receipt: created=%+v stored=%+v err=%v", created.Receipt(), stored.Receipt(), err)
		}
		retryAt := firstAt.Add(time.Minute)
		retry := testAgentAttachment(scope, "uid-retry", "session", "alice", "client-id", []byte("same upload"), retryAt, retryAt.Add(attachments.DefaultDraftRetention))
		retried, inserted, err := blobs.CreateAgentAttachment(ctx, scope, retry)
		if err != nil || inserted {
			t.Fatalf("retry same upload = inserted %t, err=%v", inserted, err)
		}
		if !retried.CreatedAt.Equal(created.CreatedAt) || retried.SHA256 != created.SHA256 || string(retried.Data) != string(created.Data) {
			t.Fatalf("retry changed the original immutable upload: first=%+v retry=%+v", created.Receipt(), retried.Receipt())
		}
		changed := retry
		changed.Data = []byte("different bytes")
		changed.SizeBytes = int64(len(changed.Data))
		changed.SHA256 = agentAttachmentDigest(changed.Data)
		if _, _, err := blobs.CreateAgentAttachment(ctx, scope, changed); !errors.Is(err, ErrAgentAttachmentConflict) {
			t.Fatalf("same client ID with different bytes error = %v, want conflict", err)
		}
	})

	t.Run("quota bounds and expired draft cleanup", func(t *testing.T) {
		scope := attachmentTestScope(base, "quota")
		if err := configureAttachmentQuota(blobs, AgentAttachmentQuota{WorkspaceMaxBytes: 10, DraftAgentMaxBytes: 100, DraftAgentMaxCount: 10}); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		if _, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-quota", "session", "alice", "workspace-base", []byte("12345678"), now, now.Add(time.Hour))); err != nil {
			t.Fatalf("create workspace quota base: %v", err)
		}
		if _, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-quota", "session", "bob", "workspace-over", []byte("123"), now, now.Add(time.Hour))); !errors.Is(err, ErrAgentAttachmentQuota) {
			t.Fatalf("workspace over-quota upload = %v, want ErrAgentAttachmentQuota", err)
		}

		draftScope := attachmentTestScope(base, "draft-quota")
		if err := configureAttachmentQuota(blobs, AgentAttachmentQuota{WorkspaceMaxBytes: 100, DraftAgentMaxBytes: 8, DraftAgentMaxCount: 10}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := blobs.CreateAgentAttachment(ctx, draftScope, testAgentAttachment(draftScope, "uid-quota", "session", "alice", "draft-base", []byte("12345678"), now, now.Add(time.Hour))); err != nil {
			t.Fatalf("create draft quota base: %v", err)
		}
		if _, _, err := blobs.CreateAgentAttachment(ctx, draftScope, testAgentAttachment(draftScope, "uid-quota", "session", "alice", "draft-over", []byte("x"), now, now.Add(time.Hour))); !errors.Is(err, ErrAgentAttachmentQuota) {
			t.Fatalf("per-owner draft over-quota upload = %v, want ErrAgentAttachmentQuota", err)
		}

		countScope := attachmentTestScope(base, "count-quota")
		if err := configureAttachmentQuota(blobs, AgentAttachmentQuota{WorkspaceMaxBytes: 100, DraftAgentMaxBytes: 100, DraftAgentMaxCount: 1}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := blobs.CreateAgentAttachment(ctx, countScope, testAgentAttachment(countScope, "uid-count", "session", "alice", "count-one", []byte("a"), now, now.Add(time.Hour))); err != nil {
			t.Fatalf("create count quota base: %v", err)
		}
		if _, _, err := blobs.CreateAgentAttachment(ctx, countScope, testAgentAttachment(countScope, "uid-count", "session", "alice", "count-two", []byte("b"), now, now.Add(time.Hour))); !errors.Is(err, ErrAgentAttachmentQuota) {
			t.Fatalf("per-owner draft over-count upload = %v, want ErrAgentAttachmentQuota", err)
		}

		expiredScope := attachmentTestScope(base, "expired")
		if err := configureAttachmentQuota(blobs, AgentAttachmentQuota{WorkspaceMaxBytes: 100, DraftAgentMaxBytes: 100, DraftAgentMaxCount: 10}); err != nil {
			t.Fatal(err)
		}
		createdAt := now.Add(-2 * time.Hour)
		expiresAt := now.Add(-time.Hour)
		expired, _, err := blobs.CreateAgentAttachment(ctx, expiredScope, testAgentAttachment(expiredScope, "uid-expired", "session", "alice", "expired", []byte("stale"), createdAt, expiresAt))
		if err != nil {
			t.Fatalf("seed expired draft: %v", err)
		}
		if ok := attachmentRowExists(t, blobs, expiredScope, expired.ID); !ok {
			t.Fatal("expired draft was not present before reconciliation")
		}
		if err := blobs.ReconcileAgentAttachmentBindings(ctx); err != nil {
			t.Fatalf("reconcile expired drafts: %v", err)
		}
		if ok := attachmentRowExists(t, blobs, expiredScope, expired.ID); ok {
			t.Fatal("expired draft remained stored after reconciliation")
		}

		oversized := testAgentAttachment(expiredScope, "uid-expired", "session", "alice", "oversized-text", make([]byte, attachments.AttachmentMaxTextBytes+1), now, now.Add(time.Hour))
		oversized.ContentType = "text/plain"
		oversized.Filename = "oversized.txt"
		if _, _, err := blobs.CreateAgentAttachment(ctx, expiredScope, oversized); err == nil {
			t.Fatal("store admitted text larger than the shared attachment limit")
		}
	})

	t.Run("atomic chat admission rejects forged receipts without writes", func(t *testing.T) {
		scope := attachmentTestScope(base, "atomic-reject")
		now := time.Now().UTC().Truncate(time.Microsecond)
		first, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-atomic", "session", "alice", "first", []byte("first"), now, now.Add(time.Hour)))
		if err != nil {
			t.Fatalf("create first upload: %v", err)
		}
		second, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-atomic", "session", "alice", "second", []byte("second"), now, now.Add(time.Hour)))
		if err != nil {
			t.Fatalf("create second upload: %v", err)
		}
		valid := []AgentAttachmentReceipt{first.Receipt(), second.Receipt()}
		cases := []struct {
			name   string
			mutate func([]AgentAttachmentReceipt) []AgentAttachmentReceipt
		}{
			{name: "forged digest", mutate: func(in []AgentAttachmentReceipt) []AgentAttachmentReceipt {
				in[1].SHA256 = strings.Repeat("0", 64)
				return in
			}},
			{name: "forged createdAt", mutate: func(in []AgentAttachmentReceipt) []AgentAttachmentReceipt {
				in[1].CreatedAt = in[1].CreatedAt.Add(time.Second)
				return in
			}},
		}
		for i, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				runID := fmt.Sprintf("rejected-%d", i)
				message, run := testChatAdmission(scope, runID, "session", now)
				if err := admission.CreateAgentChatRun(ctx, scope, "uid-atomic", message, run, tc.mutate(append([]AgentAttachmentReceipt(nil), valid...)), "alice", now); !errors.Is(err, ErrAgentAttachmentReceipt) {
					t.Fatalf("forged receipt admission error = %v, want ErrAgentAttachmentReceipt", err)
				}
				if item, err := blobs.GetAgentAttachment(ctx, scope, "uid-atomic", "session", "alice", first.ID); err != nil || !item.Draft || item.BindingID != "" {
					t.Fatalf("first upload was changed after rejection: item=%+v err=%v", item, err)
				}
				if item, err := blobs.GetAgentAttachment(ctx, scope, "uid-atomic", "session", "alice", second.ID); err != nil || !item.Draft || item.BindingID != "" {
					t.Fatalf("second upload was changed after rejection: item=%+v err=%v", item, err)
				}
				if _, err := admission.(Store).GetRun(ctx, scope, runID); err == nil {
					t.Fatal("rejected admission persisted a run")
				}
				page, err := (admission.(Store)).ListMessages(ctx, scope, "session", 20, "")
				if err != nil || len(page.Items) != 0 {
					t.Fatalf("rejected admission persisted messages %+v, err=%v", page.Items, err)
				}
			})
		}
	})

	t.Run("atomic admission commits message run and attachment bindings together", func(t *testing.T) {
		scope := attachmentTestScope(base, "atomic-success")
		now := time.Now().UTC().Truncate(time.Microsecond).Add(-2 * time.Hour)
		first, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-atomic", "session", "alice", "first", []byte("first"), now, now.Add(time.Hour)))
		if err != nil {
			t.Fatalf("create first upload: %v", err)
		}
		second, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-atomic", "session", "alice", "second", []byte("second"), now, now.Add(time.Hour)))
		if err != nil {
			t.Fatalf("create second upload: %v", err)
		}
		runID := "accepted-run"
		message, run := testChatAdmission(scope, runID, "session", now)
		message.Metadata = map[string]any{"contentParts": []map[string]string{
			{"type": "attachment", "id": first.ID}, {"type": "attachment", "id": second.ID},
		}}
		if err := admission.CreateAgentChatRun(ctx, scope, "uid-atomic", message, run, []AgentAttachmentReceipt{first.Receipt(), second.Receipt()}, "alice", now); err != nil {
			t.Fatalf("atomic chat admission: %v", err)
		}
		gotRun, err := (admission.(Store)).GetRun(ctx, scope, runID)
		if err != nil || gotRun.ID != runID || gotRun.Phase != RunPhasePending {
			t.Fatalf("admitted run = %+v, err=%v", gotRun, err)
		}
		page, err := (admission.(Store)).ListMessages(ctx, scope, "session", 20, "")
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != message.ID || page.Items[0].RunID != runID {
			t.Fatalf("admitted message page = %+v, err=%v", page.Items, err)
		}
		metadataJSON, err := json.Marshal(page.Items[0].Metadata)
		if err != nil || !strings.Contains(string(metadataJSON), first.ID) {
			t.Fatalf("admitted transcript lost content part metadata %s, err=%v", metadataJSON, err)
		}
		for _, id := range []string{first.ID, second.ID} {
			item, err := blobs.GetAgentAttachment(ctx, scope, "uid-atomic", "session", "alice", id)
			if err != nil || item.Draft || item.BindingID != runID || item.DraftExpiresAt == nil {
				t.Fatalf("admitted attachment %q = %+v, err=%v", id, item, err)
			}
		}
		listed, err := blobs.ListAgentAttachments(ctx, scope, "uid-atomic", "session", "alice", 20)
		if err != nil || len(listed) != 2 {
			t.Fatalf("collection read removed accepted-run attachments: items=%+v err=%v", listed, err)
		}
		if item, err := blobs.GetAgentAttachment(ctx, scope, "uid-atomic", "session", "alice", first.ID); err != nil || string(item.Data) != "first" {
			t.Fatalf("accepted-run blob was reclaimed after draft expiry: %q err=%v", item.Data, err)
		}

		if err := (admission.(Store)).DeleteRunData(ctx, scope, runID); err != nil {
			t.Fatalf("delete admitted run data: %v", err)
		}
		if _, err := blobs.GetAgentAttachment(ctx, scope, "uid-atomic", "session", "alice", first.ID); !errors.Is(err, ErrAgentAttachmentNotFound) {
			t.Fatalf("attachment survived run cleanup: %v", err)
		}
	})

	t.Run("failed run insert rolls back the transcript and bindings", func(t *testing.T) {
		scope := attachmentTestScope(base, "atomic-rollback")
		now := time.Now().UTC().Truncate(time.Microsecond)
		attachment, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-rollback", "session", "alice", "rollback", []byte("bytes"), now, now.Add(time.Hour)))
		if err != nil {
			t.Fatalf("create upload: %v", err)
		}
		runID := "duplicate-run"
		existing := testChatRun(scope, runID, "session", now)
		if err := (admission.(Store)).SaveRun(ctx, scope, existing); err != nil {
			t.Fatalf("seed conflicting run: %v", err)
		}
		message, run := testChatAdmission(scope, runID, "session", now.Add(time.Second))
		if err := admission.CreateAgentChatRun(ctx, scope, "uid-rollback", message, run, []AgentAttachmentReceipt{attachment.Receipt()}, "alice", now.Add(time.Second)); err == nil {
			t.Fatal("atomic admission succeeded despite conflicting run ID")
		}
		page, err := (admission.(Store)).ListMessages(ctx, scope, "session", 20, "")
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("failed atomic admission left transcript rows %+v, err=%v", page.Items, err)
		}
		item, err := blobs.GetAgentAttachment(ctx, scope, "uid-rollback", "session", "alice", attachment.ID)
		if err != nil || !item.Draft || item.BindingID != "" {
			t.Fatalf("failed atomic admission changed attachment binding: item=%+v err=%v", item, err)
		}
	})

	t.Run("session and Agent cleanup remove uploaded bytes", func(t *testing.T) {
		scope := attachmentTestScope(base, "cleanup")
		now := time.Now().UTC().Truncate(time.Microsecond)
		sessionDraft, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-cleanup", "wipe-session", "alice", "session-draft", []byte("draft"), now, now.Add(time.Hour)))
		if err != nil {
			t.Fatalf("create session draft: %v", err)
		}
		if err := (admission.(Store)).DeleteSession(ctx, scope, "wipe-session"); err != nil {
			t.Fatalf("delete session: %v", err)
		}
		if _, err := blobs.GetAgentAttachment(ctx, scope, "uid-cleanup", "wipe-session", "alice", sessionDraft.ID); !errors.Is(err, ErrAgentAttachmentNotFound) {
			t.Fatalf("session cleanup left attachment: %v", err)
		}

		agentDraft, _, err := blobs.CreateAgentAttachment(ctx, scope, testAgentAttachment(scope, "uid-cleanup", "other-session", "alice", "agent-draft", []byte("draft"), now, now.Add(time.Hour)))
		if err != nil {
			t.Fatalf("create Agent draft: %v", err)
		}
		if err := (admission.(Store)).DeleteAgentData(ctx, scope, scope.AgentName); err != nil {
			t.Fatalf("delete Agent data: %v", err)
		}
		if _, err := blobs.GetAgentAttachment(ctx, scope, "uid-cleanup", "other-session", "alice", agentDraft.ID); !errors.Is(err, ErrAgentAttachmentNotFound) {
			t.Fatalf("Agent cleanup left attachment: %v", err)
		}
	})
}

func attachmentTestBaseScope() Scope {
	return Scope{OrgUUID: "org-attachment-" + uuid.NewString(), WorkspaceUUID: "workspace-" + uuid.NewString(), AgentName: "assistant"}
}

func attachmentTestScope(base Scope, suffix string) Scope {
	scope := base
	scope.WorkspaceUUID += "-" + suffix
	return scope
}

func testAgentAttachment(scope Scope, agentUID, sessionID, owner, id string, data []byte, createdAt, expiresAt time.Time) AgentAttachment {
	return AgentAttachment{
		ID: id, AgentName: scope.AgentName, AgentUID: agentUID, SessionID: sessionID, ActorID: owner,
		Filename: id + ".bin", ContentType: "application/octet-stream", SizeBytes: int64(len(data)),
		SHA256: agentAttachmentDigest(data), Draft: true, CreatedAt: createdAt.UTC(), ExpiresAt: timePointer(expiresAt.UTC()),
		Data: append([]byte(nil), data...),
	}
}

func testChatAdmission(scope Scope, runID, sessionID string, now time.Time) (Message, Run) {
	message := Message{
		ID: "message-" + runID, AgentName: scope.AgentName, SessionID: sessionID, RunID: runID,
		Role: "user", Content: "inspect the selected files", CreatedAt: now.UTC(),
		Metadata: map[string]any{"contentParts": []map[string]string{{"type": "attachment", "id": "selected"}}},
	}
	return message, testChatRun(scope, runID, sessionID, now)
}

func testChatRun(scope Scope, runID, sessionID string, now time.Time) Run {
	return Run{
		ID: runID, AgentName: scope.AgentName, SessionID: sessionID, Trigger: "chat",
		Phase: RunPhasePending, Input: "inspect the selected files", CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
}

func configureAttachmentQuota(blobs AgentAttachmentStore, quota AgentAttachmentQuota) error {
	return blobs.(AgentAttachmentQuotaConfigurer).ConfigureAgentAttachmentQuota(quota)
}

func attachmentRowExists(t *testing.T, blobs AgentAttachmentStore, scope Scope, id string) bool {
	t.Helper()
	switch store := blobs.(type) {
	case *MemoryStore:
		store.mu.Lock()
		defer store.mu.Unlock()
		_, ok := store.agentAttachments[agentAttachmentScopeKey(scope, "uid-expired", id)]
		return ok
	case *PostgresStore:
		return postgresAttachmentExists(t, store, scope, id)
	default:
		t.Fatalf("unsupported attachment store test type %T", blobs)
		return false
	}
}

func postgresAttachmentExists(t *testing.T, pg *PostgresStore, scope Scope, id string) bool {
	t.Helper()
	var count int
	err := pg.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM agents_agent_attachments
		WHERE org_uuid=$1 AND workspace_uuid=$2 AND agent_name=$3 AND id=$4`, scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, id).Scan(&count)
	if err != nil {
		t.Fatalf("count attachment row: %v", err)
	}
	return count > 0
}

func holdTenantMappingLock(t *testing.T, pg *PostgresStore, clusterID string) *sql.Tx {
	t.Helper()
	tx, err := pg.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tenant mapping lock transaction: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(), `SELECT pg_advisory_xact_lock(hashtext($1))`, "agents-tenant-map:"+clusterID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("hold tenant mapping advisory lock: %v", err)
	}
	return tx
}

func publishTenantMappingUnderLock(t *testing.T, ctx context.Context, tx *sql.Tx, clusterID string, mapped Scope) {
	t.Helper()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents_tenants (cluster_id,org_uuid,workspace_uuid,updated_at)
		VALUES ($1,$2,$3,$4)`, clusterID, mapped.OrgUUID, mapped.WorkspaceUUID, time.Now().UTC()); err != nil {
		t.Fatalf("insert tenant mapping under lock: %v", err)
	}
}

func waitForAdvisoryLockWaiter(t *testing.T, pg *PostgresStore) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := pg.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pg_locks
			WHERE locktype='advisory' AND granted=FALSE AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&waiting); err != nil {
			t.Fatalf("inspect advisory lock waiters: %v", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("store mutation did not wait on tenant mapping lock")
}

func purgeAttachmentFallbackRace(t *testing.T, pg *PostgresStore, clusterID, mappedOrg string) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"agents_agent_attachments", "agents_messages", "agents_runs"} {
		if _, err := pg.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE org_uuid=$1 AND workspace_uuid=$2", UnmappedOrg, clusterID); err != nil {
			t.Logf("cleanup fallback attachment race rows from %s: %v", table, err)
		}
		if _, err := pg.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE org_uuid=$1", mappedOrg); err != nil {
			t.Logf("cleanup canonical attachment race rows from %s: %v", table, err)
		}
	}
	if _, err := pg.db.ExecContext(ctx, `DELETE FROM agents_tenants WHERE cluster_id=$1`, clusterID); err != nil {
		t.Logf("cleanup attachment race tenant mapping: %v", err)
	}
}

func purgeAgentAttachmentTestOrg(t *testing.T, pg *PostgresStore, org string) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"agents_agent_attachments", "agents_messages", "agents_runs", "agents_session_summaries", "agents_session_checkpoint_generations", "agents_tenants"} {
		if _, err := pg.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE org_uuid=$1", org); err != nil {
			t.Logf("cleanup attachment test rows from %s for %s: %v", table, org, err)
		}
	}
	if _, err := pg.db.ExecContext(ctx, `DELETE FROM agents_agent_attachments WHERE org_uuid=$1`, org+"-other"); err != nil {
		t.Logf("cleanup attachments for second tenant %s: %v", org+"-other", err)
	}
}

func timePointer(value time.Time) *time.Time { return &value }
