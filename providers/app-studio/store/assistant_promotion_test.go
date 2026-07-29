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
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestMemoryAndEncryptedStorePromoteAssistantRunToWorkItemContract(t *testing.T) {
	for _, tt := range []struct {
		name string
		new  func(*testing.T) Store
	}{
		{name: "memory", new: func(*testing.T) Store { return NewMemoryStore() }},
		{name: "encrypted", new: func(t *testing.T) Store {
			wrapped, err := NewEncryptedStore(NewMemoryStore(), testEncryptionKeys(t))
			if err != nil {
				t.Fatal(err)
			}
			return wrapped
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testPromoteAssistantRunToWorkItemContract(t, tt.new(t))
		})
	}
}

func TestPostgresStorePromoteAssistantRunToWorkItemContractExternalDSN(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("APP_STUDIO_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("APP_STUDIO_TEST_POSTGRES_DSN is not set")
	}

	ctx := context.Background()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()

	schemaName := "app_studio_promotion_" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
	})

	s, err := OpenPostgres(ctx, postgresDSNWithSearchPath(t, dsn, schemaName))
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()

	testPromoteAssistantRunToWorkItemContract(t, s)
	testConcurrentPromotionReplay(t, s)
}

func testPromoteAssistantRunToWorkItemContract(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	scope := promotionTestScope("success")
	createdAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	promotedAt := createdAt.Add(2 * time.Minute)
	run := promotionTestRun(createdAt)
	user := promotionTestUser(createdAt)
	assistant := promotionTestAssistant(createdAt)
	if _, err := s.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}

	item, promoted, err := s.PromoteAssistantRunToWorkItem(
		ctx, scope, run.ID, user.ActorID, "work-item-1", run.Revision, promotedAt,
	)
	if err != nil {
		t.Fatalf("PromoteAssistantRunToWorkItem: %v", err)
	}
	if item.ID != "work-item-1" || item.RootMessageID != user.ID || item.CreatedBy != user.ActorID ||
		item.Status != AssistantWorkItemStatusActive || item.Revision != 1 || item.ActiveRunID != run.ID {
		t.Fatalf("promoted work item = %#v", item)
	}
	if !item.CreatedAt.Equal(createdAt) || !item.UpdatedAt.Equal(promotedAt) {
		t.Fatalf("promoted work item timestamps = (%v, %v), want (%v, %v)", item.CreatedAt, item.UpdatedAt, createdAt, promotedAt)
	}
	if promoted.WorkItemID != item.ID || promoted.Mode != AssistantRunModeNew ||
		promoted.Status != AssistantRunStatusRunning || promoted.Revision != run.Revision+1 ||
		promoted.UserMessageID != run.UserMessageID || promoted.ActiveMessageID != run.ActiveMessageID ||
		promoted.ClientRequestID != run.ClientRequestID ||
		!jsonSemanticallyEqual(promoted.Checkpoint, run.Checkpoint) ||
		!jsonSemanticallyEqual(promoted.Audit, run.Audit) {
		t.Fatalf("promoted run = %#v, original = %#v", promoted, run)
	}

	messages, err := s.LoadMessagesForWorkItem(ctx, scope, item.ID, 10)
	if err != nil {
		t.Fatalf("LoadMessagesForWorkItem: %v", err)
	}
	byID := make(map[string]Message, len(messages))
	for _, message := range messages {
		byID[message.ID] = message
	}
	promotedUser, userOK := byID[user.ID]
	promotedAssistant, assistantOK := byID[assistant.ID]
	if len(messages) != 2 || !userOK || !assistantOK ||
		promotedUser.WorkItemID != item.ID || promotedAssistant.WorkItemID != item.ID ||
		promotedUser.ActorID != user.ActorID || promotedUser.Content != user.Content ||
		promotedAssistant.Content != assistant.Content {
		t.Fatalf("promoted messages = %#v", messages)
	}

	replayedItem, replayedRun, err := s.PromoteAssistantRunToWorkItem(
		ctx, scope, run.ID, user.ActorID, item.ID, run.Revision, promotedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("idempotent promotion replay: %v", err)
	}
	if replayedItem.Revision != item.Revision || replayedRun.Revision != promoted.Revision ||
		!replayedItem.UpdatedAt.Equal(item.UpdatedAt) || !replayedRun.UpdatedAt.Equal(promoted.UpdatedAt) {
		t.Fatalf("promotion replay mutated state: item=%#v run=%#v", replayedItem, replayedRun)
	}
	items, err := s.ListAssistantWorkItems(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("promotion replay created %d work items, want one", len(items))
	}

	if _, _, err := s.PromoteAssistantRunToWorkItem(
		ctx, scope, run.ID, user.ActorID, item.ID, promoted.Revision, promotedAt,
	); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("promotion replay with wrong CAS revision error = %v, want %v", err, ErrAssistantRunConflict)
	}
	if _, _, err := s.PromoteAssistantRunToWorkItem(
		ctx, scope, run.ID, user.ActorID, "work-item-2", run.Revision, promotedAt,
	); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("different promotion target error = %v, want %v", err, ErrAssistantRunConflict)
	}
	otherScope := scope
	otherScope.ProjectUID = "other-project-uid"
	if _, _, err := s.PromoteAssistantRunToWorkItem(
		ctx, otherScope, run.ID, user.ActorID, item.ID, run.Revision, promotedAt,
	); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("cross-project promotion error = %v, want %v", err, ErrAssistantRunConflict)
	}

	assertPromotionConflictIsAtomic(t, s, promotionTestScope("actor"), "other-actor", 1, ErrAssistantWorkItemConflict)
	assertPromotionConflictIsAtomic(t, s, promotionTestScope("revision"), user.ActorID, 2, ErrAssistantRunConflict)
}

func assertPromotionConflictIsAtomic(
	t *testing.T,
	s Store,
	scope Scope,
	actor string,
	expectedRevision int64,
	want error,
) {
	t.Helper()
	ctx := context.Background()
	createdAt := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	run := promotionTestRun(createdAt)
	user := promotionTestUser(createdAt)
	assistant := promotionTestAssistant(createdAt)
	if _, err := s.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatalf("CreateAssistantRun for conflict: %v", err)
	}

	if _, _, err := s.PromoteAssistantRunToWorkItem(
		ctx, scope, run.ID, actor, "conflict-work-item", expectedRevision, createdAt.Add(time.Minute),
	); !errors.Is(err, want) {
		t.Fatalf("promotion conflict error = %v, want %v", err, want)
	}
	persistedRun, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedRun.WorkItemID != "" || persistedRun.Mode != AssistantRunModeAdaptive || persistedRun.Revision != run.Revision {
		t.Fatalf("promotion conflict changed run: %#v", persistedRun)
	}
	page, err := s.ListMessages(ctx, scope, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].WorkItemID != "" || page.Items[1].WorkItemID != "" {
		t.Fatalf("promotion conflict changed message membership: %#v", page.Items)
	}
	items, err := s.ListAssistantWorkItems(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("promotion conflict created work items: %#v", items)
	}
}

func TestMemoryStorePromoteAssistantRunToWorkItemConcurrentReplay(t *testing.T) {
	testConcurrentPromotionReplay(t, NewMemoryStore())
}

func testConcurrentPromotionReplay(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	scope := promotionTestScope("concurrent")
	now := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	run := promotionTestRun(now)
	user := promotionTestUser(now)
	if _, err := s.CreateAssistantRun(ctx, scope, user, promotionTestAssistant(now), run); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.PromoteAssistantRunToWorkItem(
				ctx, scope, run.ID, user.ActorID, "concurrent-work-item", run.Revision, now.Add(time.Minute),
			)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent idempotent promotion: %v", err)
		}
	}

	items, err := s.ListAssistantWorkItems(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	persistedRun, err := s.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || persistedRun.Revision != 2 || persistedRun.WorkItemID != items[0].ID {
		t.Fatalf("concurrent promotion state: items=%#v run=%#v", items, persistedRun)
	}
}

func TestEncryptedStorePromotionPreservesCiphertextAtRest(t *testing.T) {
	base := NewMemoryStore()
	wrapped, err := NewEncryptedStore(base, testEncryptionKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := promotionTestScope("ciphertext")
	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	run := promotionTestRun(now)
	user := promotionTestUser(now)
	assistant := promotionTestAssistant(now)
	if _, err := wrapped.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	if _, _, err := wrapped.PromoteAssistantRunToWorkItem(
		ctx, scope, run.ID, user.ActorID, "ciphertext-work-item", run.Revision, now.Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}

	rawMessages, err := base.LoadMessagesForWorkItem(ctx, scope, "ciphertext-work-item", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range rawMessages {
		if !message.ContentEncrypted || message.ContentKeyID == "" ||
			message.Content == user.Content || message.Content == assistant.Content {
			t.Fatalf("message is not ciphertext at rest: %#v", message)
		}
	}
	rawRun, err := base.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var checkpointEnvelope, auditEnvelope encryptedAssistantRunCheckpoint
	if json.Unmarshal(rawRun.Checkpoint, &checkpointEnvelope) != nil || !checkpointEnvelope.Encrypted ||
		json.Unmarshal(rawRun.Audit, &auditEnvelope) != nil || !auditEnvelope.Encrypted {
		t.Fatalf("run blobs are plaintext at rest: %#v", rawRun)
	}
	decryptedRun, err := wrapped.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonSemanticallyEqual(decryptedRun.Checkpoint, run.Checkpoint) ||
		!jsonSemanticallyEqual(decryptedRun.Audit, run.Audit) {
		t.Fatalf("decrypted promoted run = %#v, original = %#v", decryptedRun, run)
	}
}

func TestEncryptedStorePromotionReplayReturnsDecryptedPlanGrant(t *testing.T) {
	base := NewMemoryStore()
	keys := testEncryptionKeys(t)
	wrapped, err := NewEncryptedStore(base, keys)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := promotionTestScope("encrypted-replay")
	now := time.Date(2026, 7, 28, 15, 30, 0, 0, time.UTC)
	run := promotionTestRun(now)
	user := promotionTestUser(now)
	assistant := promotionTestAssistant(now)
	if _, err := wrapped.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	item, _, err := wrapped.PromoteAssistantRunToWorkItem(
		ctx, scope, run.ID, user.ActorID, "encrypted-replay-work-item", run.Revision, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	grant := json.RawMessage(`{"capabilities":["workspace_mutate"]}`)
	if _, err := wrapped.ApproveWorkItemPlan(
		ctx, scope, item.ID, run.ID, item.Revision, "grant-1", grant, now.Add(2*time.Minute),
	); err != nil {
		t.Fatal(err)
	}

	replayed, _, err := wrapped.PromoteAssistantRunToWorkItem(
		ctx, scope, run.ID, user.ActorID, item.ID, run.Revision, now.Add(3*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonSemanticallyEqual(replayed.PlanGrant, grant) {
		t.Fatalf("replayed plan grant = %s, want %s", replayed.PlanGrant, grant)
	}
}

func TestEncryptedStorePromotionReplayKeepsConcurrentGrantAndRevisionPaired(t *testing.T) {
	base := NewMemoryStore()
	keys := testEncryptionKeys(t)
	wrapped, err := NewEncryptedStore(base, keys)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := promotionTestScope("encrypted-concurrent-replay")
	now := time.Date(2026, 7, 28, 15, 45, 0, 0, time.UTC)
	run := promotionTestRun(now)
	user := promotionTestUser(now)
	assistant := promotionTestAssistant(now)
	if _, err := wrapped.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	item, _, err := wrapped.PromoteAssistantRunToWorkItem(
		ctx, scope, run.ID, user.ActorID, "encrypted-concurrent-replay-work-item", run.Revision, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	firstGrant := json.RawMessage(`{"capabilities":["workspace_read"]}`)
	approved, err := wrapped.ApproveWorkItemPlan(
		ctx, scope, item.ID, run.ID, item.Revision, "grant-1", firstGrant, now.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}

	blocking := &blockingPromotionStore{
		Store:   base,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	replayStore, err := NewEncryptedStore(blocking, keys)
	if err != nil {
		t.Fatal(err)
	}
	type replayResult struct {
		item AssistantWorkItem
		err  error
	}
	result := make(chan replayResult, 1)
	go func() {
		replayed, _, err := replayStore.PromoteAssistantRunToWorkItem(
			ctx, scope, run.ID, user.ActorID, item.ID, run.Revision, now.Add(3*time.Minute),
		)
		result <- replayResult{item: replayed, err: err}
	}()
	<-blocking.entered

	secondGrant := json.RawMessage(`{"capabilities":["workspace_mutate"]}`)
	updated, err := wrapped.ApproveWorkItemPlan(
		ctx, scope, item.ID, run.ID, approved.Revision, "grant-2", secondGrant, now.Add(4*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	close(blocking.release)
	replayed := <-result
	if replayed.err != nil {
		t.Fatal(replayed.err)
	}
	if replayed.item.Revision != updated.Revision || replayed.item.GrantRevision != updated.GrantRevision ||
		!jsonSemanticallyEqual(replayed.item.PlanGrant, updated.PlanGrant) {
		t.Fatalf("replayed grant and revision are not paired: replayed=%#v updated=%#v", replayed.item, updated)
	}
}

func TestEncryptedStoreRejectsCorruptRunBeforePromotionCommits(t *testing.T) {
	base := NewMemoryStore()
	wrapped, err := NewEncryptedStore(base, testEncryptionKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := promotionTestScope("corrupt-ciphertext")
	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	run := promotionTestRun(now)
	user := promotionTestUser(now)
	assistant := promotionTestAssistant(now)
	if _, err := wrapped.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	rawRun, err := base.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rawRun.Checkpoint = json.RawMessage(`{"encrypted":true,"keyID":"missing","nonce":"bad","ciphertext":"bad"}`)
	if err := base.SaveAssistantRun(ctx, scope, rawRun); err != nil {
		t.Fatal(err)
	}

	if _, _, err := wrapped.PromoteAssistantRunToWorkItem(
		ctx, scope, run.ID, user.ActorID, "corrupt-work-item", run.Revision, now.Add(time.Minute),
	); err == nil {
		t.Fatal("PromoteAssistantRunToWorkItem error = nil, want decryption failure")
	}
	persisted, err := base.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.WorkItemID != "" || persisted.Mode != AssistantRunModeAdaptive || persisted.Revision != run.Revision {
		t.Fatalf("failed encrypted promotion mutated run: %#v", persisted)
	}
	items, err := base.ListAssistantWorkItems(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("failed encrypted promotion created work items: %#v", items)
	}
}

type blockingPromotionStore struct {
	Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingPromotionStore) PromoteAssistantRunToWorkItem(
	ctx context.Context,
	scope Scope,
	runID, actor, workItemID string,
	expectedRunRevision int64,
	now time.Time,
) (AssistantWorkItem, AssistantRun, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.Store.PromoteAssistantRunToWorkItem(
		ctx, scope, runID, actor, workItemID, expectedRunRevision, now,
	)
}

func promotionTestScope(suffix string) Scope {
	return Scope{
		OrgUUID:       "promotion-org",
		WorkspaceUUID: "promotion-workspace",
		ProjectName:   "promotion-" + suffix,
		ProjectUID:    "promotion-project-" + suffix,
	}
}

func promotionTestUser(now time.Time) Message {
	return Message{
		ID:        "adaptive-user-1",
		ActorID:   "actor-1",
		Role:      "user",
		Content:   "Build the dashboard once this becomes implementation work.",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func promotionTestAssistant(now time.Time) Message {
	return Message{
		ID:        "adaptive-assistant-1",
		Role:      "assistant",
		Content:   "I will inspect the project first.",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func promotionTestRun(now time.Time) AssistantRun {
	return AssistantRun{
		ID:              "adaptive-run-1",
		Mode:            AssistantRunModeAdaptive,
		Status:          AssistantRunStatusRunning,
		ClientRequestID: "adaptive-request-1",
		UserMessageID:   "adaptive-user-1",
		ActiveMessageID: "adaptive-assistant-1",
		Revision:        1,
		Checkpoint:      json.RawMessage(`{"phase":"discussion"}`),
		Audit:           json.RawMessage(`{"events":[{"kind":"created"}]}`),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}
