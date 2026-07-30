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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// durableAssistantRunStore makes the Task 1 contract explicit for parity tests.
type durableAssistantRunStore interface {
	Store
	CreateAssistantRun(context.Context, Scope, Message, Message, AssistantRun) (AssistantRun, error)
	SaveAssistantRunSnapshot(context.Context, Scope, AssistantRun, []Message, int64) error
	FindAssistantRunByClientRequestID(context.Context, Scope, string) (AssistantRun, error)
	LatestAssistantRun(context.Context, Scope) (AssistantRun, error)
}

func TestMemoryStoreImplementsDurableAssistantRunContract(t *testing.T) {
	if _, ok := any(NewMemoryStore()).(durableAssistantRunStore); !ok {
		t.Fatal("MemoryStore does not implement durable assistant-run store contract")
	}
}

func TestEncryptedStoreImplementsDurableAssistantRunContract(t *testing.T) {
	base := NewMemoryStore()
	store, err := NewEncryptedStore(base, testEncryptionKeys(t))
	if err != nil {
		t.Fatalf("NewEncryptedStore: %v", err)
	}
	if _, ok := store.(durableAssistantRunStore); !ok {
		t.Fatal("encrypted store does not implement durable assistant-run store contract")
	}
}

func TestMemoryStoreCreateAssistantRunIsAtomicAndIdempotent(t *testing.T) {
	store := mustDurableAssistantRunStore(t, NewMemoryStore())
	scope := testAssistantRunScope()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	first := testAssistantRun(t, "run-1", "request-1", "assistant-1", createdAt)

	created, err := store.CreateAssistantRun(
		context.Background(), scope,
		Message{ID: "user-1", Role: "user", ActorID: "actor-1", Content: "Build a dashboard", CreatedAt: createdAt, UpdatedAt: createdAt},
		Message{ID: "assistant-1", Role: "assistant", Content: "", CreatedAt: createdAt.Add(time.Microsecond), UpdatedAt: createdAt.Add(time.Microsecond)},
		first,
	)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	if created.ID != first.ID || assistantRunRevision(t, created) != 1 || assistantRunString(t, created, "ActiveMessageID") != "assistant-1" || assistantRunString(t, created, "UserMessageID") != "user-1" {
		t.Fatalf("created run = %#v, want durable initial snapshot", created)
	}

	page, err := store.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatalf("ListMessages after create: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != "user-1" || page.Items[1].ID != "assistant-1" {
		t.Fatalf("messages after create = %#v, want both placeholder and user message", page.Items)
	}

	recovered, err := store.CreateAssistantRun(
		context.Background(), scope,
		Message{ID: "user-retry", Role: "user", ActorID: "actor-1", Content: "different retry payload", CreatedAt: createdAt.Add(time.Minute), UpdatedAt: createdAt.Add(time.Minute)},
		Message{ID: "assistant-retry", Role: "assistant", Content: "different retry payload", CreatedAt: createdAt.Add(time.Minute), UpdatedAt: createdAt.Add(time.Minute)},
		testAssistantRun(t, "run-retry", "request-1", "assistant-retry", createdAt.Add(time.Minute)),
	)
	if err != nil {
		t.Fatalf("duplicate CreateAssistantRun: %v", err)
	}
	if recovered.ID != first.ID {
		t.Fatalf("duplicate request recovered run %q, want %q", recovered.ID, first.ID)
	}
	page, err = store.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatalf("ListMessages after duplicate create: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("duplicate request created messages: %#v", page.Items)
	}
}

func TestMemoryAndEncryptedAssistantRunPreserveOriginatingUserMessageIDOnRetry(t *testing.T) {
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
			durable := mustDurableAssistantRunStore(t, tt.new(t))
			scope := testAssistantRunScope()
			now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
			run := testAssistantRun(t, "run-1", "request-1", "assistant-1", now)
			created, err := durable.CreateAssistantRun(context.Background(), scope,
				Message{ID: "user-1", Role: "user", ActorID: "actor-1", Content: "origin", CreatedAt: now, UpdatedAt: now},
				Message{ID: "assistant-1", Role: "assistant", CreatedAt: now, UpdatedAt: now}, run)
			if err != nil {
				t.Fatalf("CreateAssistantRun: %v", err)
			}
			retry := testAssistantRun(t, "run-retry", "request-1", "assistant-retry", now.Add(time.Minute))
			recovered, err := durable.CreateAssistantRun(context.Background(), scope,
				Message{ID: "user-retry", Role: "user", ActorID: "actor-1", Content: "retry", CreatedAt: now, UpdatedAt: now},
				Message{ID: "assistant-retry", Role: "assistant", CreatedAt: now, UpdatedAt: now}, retry)
			if err != nil {
				t.Fatalf("retry CreateAssistantRun: %v", err)
			}
			if created.UserMessageID != "user-1" || recovered.UserMessageID != "user-1" {
				t.Fatalf("originating user ID was not preserved: created=%#v recovered=%#v", created, recovered)
			}
		})
	}
}

func TestMemoryStoreRejectsSecondNonterminalAssistantRun(t *testing.T) {
	store := mustDurableAssistantRunStore(t, NewMemoryStore())
	scope := testAssistantRunScope()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	if _, err := store.CreateAssistantRun(context.Background(), scope,
		Message{ID: "user-1", Role: "user", ActorID: "actor-1", Content: "first", CreatedAt: createdAt, UpdatedAt: createdAt},
		Message{ID: "assistant-1", Role: "assistant", CreatedAt: createdAt, UpdatedAt: createdAt},
		testAssistantRun(t, "run-1", "request-1", "assistant-1", createdAt),
	); err != nil {
		t.Fatalf("first CreateAssistantRun: %v", err)
	}

	_, err := store.CreateAssistantRun(context.Background(), scope,
		Message{ID: "user-2", Role: "user", ActorID: "actor-1", Content: "second", CreatedAt: createdAt.Add(time.Minute), UpdatedAt: createdAt.Add(time.Minute)},
		Message{ID: "assistant-2", Role: "assistant", CreatedAt: createdAt.Add(time.Minute), UpdatedAt: createdAt.Add(time.Minute)},
		testAssistantRun(t, "run-2", "request-2", "assistant-2", createdAt.Add(time.Minute)),
	)
	if !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("second nonterminal CreateAssistantRun error = %v, want conflict", err)
	}
}

func TestMemoryStoreConcurrentCreateAllowsOneNonterminalRun(t *testing.T) {
	store := mustDurableAssistantRunStore(t, NewMemoryStore())
	scope := testAssistantRunScope()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		i := i
		go func() {
			<-start
			_, err := store.CreateAssistantRun(context.Background(), scope,
				Message{ID: "user-" + string(rune('0'+i)), Role: "user", ActorID: "actor-1", Content: "request", CreatedAt: createdAt, UpdatedAt: createdAt},
				Message{ID: "assistant-" + string(rune('0'+i)), Role: "assistant", CreatedAt: createdAt, UpdatedAt: createdAt},
				testAssistantRun(t, "run-"+string(rune('0'+i)), "request-"+string(rune('0'+i)), "assistant-"+string(rune('0'+i)), createdAt),
			)
			errs <- err
		}()
	}
	close(start)
	var success, conflict int
	for range 2 {
		if err := <-errs; err == nil {
			success++
		} else if errors.Is(err, ErrAssistantRunConflict) {
			conflict++
		} else {
			t.Fatalf("concurrent CreateAssistantRun error = %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent create results: %d success, %d conflict; want one each", success, conflict)
	}
}

func TestMemoryStoreLegacyWritersRejectSecondNonterminalAssistantRun(t *testing.T) {
	store := NewMemoryStore()
	scope := testAssistantRunScope()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	if err := store.SaveAssistantRun(context.Background(), scope, AssistantRun{
		ID:        "legacy-1",
		Status:    AssistantRunStatusRunning,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}); err != nil {
		t.Fatalf("first SaveAssistantRun: %v", err)
	}
	if err := store.SaveAssistantRun(context.Background(), scope, AssistantRun{
		ID:        "legacy-2",
		Status:    AssistantRunStatusPendingInput,
		CreatedAt: createdAt.Add(time.Minute),
		UpdatedAt: createdAt.Add(time.Minute),
	}); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("second SaveAssistantRun error = %v, want conflict", err)
	}
}

func TestMemoryAndEncryptedStoresRejectDuplicateLegacyClientRequestID(t *testing.T) {
	for _, tt := range []struct {
		name string
		new  func(*testing.T) Store
	}{
		{name: "memory", new: func(*testing.T) Store { return NewMemoryStore() }},
		{name: "encrypted", new: func(t *testing.T) Store {
			wrapped, err := NewEncryptedStore(NewMemoryStore(), testEncryptionKeys(t))
			if err != nil {
				t.Fatalf("NewEncryptedStore: %v", err)
			}
			return wrapped
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.new(t)
			scope := testAssistantRunScope()
			createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
			first := AssistantRun{
				ID:              "run-1",
				Status:          AssistantRunStatusCompleted,
				ClientRequestID: "request-1",
				Checkpoint:      json.RawMessage(`{"checkpoint":"first"}`),
				Audit:           json.RawMessage(`{"audit":"first"}`),
				CreatedAt:       createdAt,
				UpdatedAt:       createdAt,
			}
			if err := store.SaveAssistantRun(context.Background(), scope, first); err != nil {
				t.Fatalf("first SaveAssistantRun: %v", err)
			}
			if err := store.SaveAssistantRun(context.Background(), scope, AssistantRun{
				ID:              "run-2",
				Status:          AssistantRunStatusCompleted,
				ClientRequestID: first.ClientRequestID,
				CreatedAt:       createdAt.Add(time.Minute),
				UpdatedAt:       createdAt.Add(time.Minute),
			}); !errors.Is(err, ErrAssistantRunConflict) {
				t.Fatalf("duplicate SaveAssistantRun error = %v, want conflict", err)
			}
			first.Audit = json.RawMessage(`{"audit":"updated"}`)
			first.UpdatedAt = createdAt.Add(2 * time.Minute)
			if err := store.SaveAssistantRun(context.Background(), scope, first); err != nil {
				t.Fatalf("same-run SaveAssistantRun update: %v", err)
			}
			got, err := store.GetAssistantRun(context.Background(), scope, first.ID)
			if err != nil {
				t.Fatalf("GetAssistantRun: %v", err)
			}
			if string(got.Audit) != string(first.Audit) {
				t.Fatalf("same-run audit = %s, want %s", got.Audit, first.Audit)
			}
		})
	}
}

func TestMemoryStoreSaveAssistantRunSnapshotRequiresNextRevision(t *testing.T) {
	store := mustDurableAssistantRunStore(t, NewMemoryStore())
	scope := testAssistantRunScope()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	run := createTestAssistantRun(t, store, scope, createdAt)
	run = withAssistantRunRevision(t, run, 2)
	run.Status = AssistantRunStatusRunning
	run.UpdatedAt = createdAt.Add(time.Minute)
	message := Message{
		ID:        assistantRunString(t, run, "ActiveMessageID"),
		Role:      "assistant",
		Content:   "I am working on it.",
		Metadata:  map[string]any{"assistantRunID": run.ID, "assistantRevision": int64(2)},
		CreatedAt: createdAt,
		UpdatedAt: run.UpdatedAt,
	}
	if err := store.SaveAssistantRunSnapshot(context.Background(), scope, run, []Message{message}, 1); err != nil {
		t.Fatalf("SaveAssistantRunSnapshot: %v", err)
	}

	stale := run
	stale.Status = AssistantRunStatusCompleted
	stale.UpdatedAt = createdAt.Add(2 * time.Minute)
	if err := store.SaveAssistantRunSnapshot(context.Background(), scope, stale, nil, 1); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("stale SaveAssistantRunSnapshot error = %v, want conflict", err)
	}
	got, err := store.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun: %v", err)
	}
	if assistantRunRevision(t, got) != 2 || got.Status != AssistantRunStatusRunning {
		t.Fatalf("run after stale snapshot = %#v, want revision 2 running", got)
	}
	page, err := store.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatalf("ListMessages after snapshot: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != assistantRunString(t, run, "ActiveMessageID") || page.Items[0].Content != message.Content {
		t.Fatalf("snapshot did not persist assistant display message: %#v", page.Items)
	}
}

func TestMemoryStoreSnapshotPreservesClientRequestAndCreationTime(t *testing.T) {
	store := mustDurableAssistantRunStore(t, NewMemoryStore())
	scope := testAssistantRunScope()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	run := createTestAssistantRun(t, store, scope, createdAt)
	run.ClientRequestID = "malicious-replacement"
	run.CreatedAt = createdAt.Add(24 * time.Hour)
	run.Revision = 2
	run.UpdatedAt = createdAt.Add(time.Minute)
	if err := store.SaveAssistantRunSnapshot(context.Background(), scope, run, nil, 1); err != nil {
		t.Fatalf("SaveAssistantRunSnapshot: %v", err)
	}
	got, err := store.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun: %v", err)
	}
	if got.ClientRequestID != "request-1" || !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("snapshot changed immutable run identity: %#v", got)
	}
	if _, err := store.FindAssistantRunByClientRequestID(context.Background(), scope, "request-1"); err != nil {
		t.Fatalf("FindAssistantRunByClientRequestID original: %v", err)
	}
	if _, err := store.FindAssistantRunByClientRequestID(context.Background(), scope, "malicious-replacement"); !errors.Is(err, ErrAssistantRunNotFound) {
		t.Fatalf("FindAssistantRunByClientRequestID replacement error = %v, want not found", err)
	}
}

func TestMemoryStoreFindsLatestRunAndClientRequest(t *testing.T) {
	store := mustDurableAssistantRunStore(t, NewMemoryStore())
	scope := testAssistantRunScope()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	first := createTestAssistantRun(t, store, scope, createdAt)
	first.Status = AssistantRunStatusCompleted
	first = withAssistantRunRevision(t, first, 2)
	first.UpdatedAt = createdAt.Add(time.Minute)
	if err := store.SaveAssistantRunSnapshot(context.Background(), scope, first, nil, 1); err != nil {
		t.Fatalf("complete first run: %v", err)
	}
	second := testAssistantRun(t, "run-2", "request-2", "assistant-2", createdAt.Add(2*time.Minute))
	if _, err := store.CreateAssistantRun(context.Background(), scope,
		Message{ID: "user-2", Role: "user", ActorID: "actor-1", Content: "second", CreatedAt: second.CreatedAt, UpdatedAt: second.CreatedAt},
		Message{ID: assistantRunString(t, second, "ActiveMessageID"), Role: "assistant", CreatedAt: second.CreatedAt, UpdatedAt: second.CreatedAt},
		second,
	); err != nil {
		t.Fatalf("CreateAssistantRun second: %v", err)
	}

	found, err := store.FindAssistantRunByClientRequestID(context.Background(), scope, assistantRunString(t, first, "ClientRequestID"))
	if err != nil {
		t.Fatalf("FindAssistantRunByClientRequestID: %v", err)
	}
	if found.ID != first.ID {
		t.Fatalf("found run = %q, want %q", found.ID, first.ID)
	}
	latest, err := store.LatestAssistantRun(context.Background(), scope)
	if err != nil {
		t.Fatalf("LatestAssistantRun: %v", err)
	}
	if latest.ID != second.ID {
		t.Fatalf("latest run = %q, want %q", latest.ID, second.ID)
	}
}

func TestMemoryAndEncryptedStoresKeepPausedLegacyRunsResumable(t *testing.T) {
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
			ctx := context.Background()
			store := tt.new(t)
			scope := testAssistantRunScope()
			now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
			legacy := AssistantRun{
				ID:         "legacy-paused",
				Status:     AssistantRunStatusPendingPermission,
				RequestID:  "permission-1",
				Checkpoint: json.RawMessage(`{"checkpoint":"legacy"}`),
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if err := store.SaveAssistantRun(ctx, scope, legacy); err != nil {
				t.Fatalf("SaveAssistantRun legacy paused: %v", err)
			}
			claimed, err := store.ClaimAssistantRun(ctx, scope, legacy.ID, legacy.RequestID, now.Add(time.Minute))
			if err != nil {
				t.Fatalf("ClaimAssistantRun legacy paused: %v", err)
			}
			if claimed.Status != AssistantRunStatusRunning {
				t.Fatalf("claimed legacy status = %q, want running", claimed.Status)
			}
		})
	}
}

func TestMemoryStoreRecoversCompletedRequestWhileAnotherRunIsActive(t *testing.T) {
	store := mustDurableAssistantRunStore(t, NewMemoryStore())
	scope := testAssistantRunScope()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	completed := createTestAssistantRun(t, store, scope, createdAt)
	completed.Status = AssistantRunStatusCompleted
	completed.Revision = 2
	completed.UpdatedAt = createdAt.Add(time.Minute)
	if err := store.SaveAssistantRunSnapshot(context.Background(), scope, completed, nil, 1); err != nil {
		t.Fatalf("complete first run: %v", err)
	}
	active := testAssistantRun(t, "run-2", "request-2", "assistant-2", createdAt.Add(2*time.Minute))
	if _, err := store.CreateAssistantRun(context.Background(), scope,
		Message{ID: "user-2", Role: "user", ActorID: "actor-1", Content: "second", CreatedAt: active.CreatedAt, UpdatedAt: active.CreatedAt},
		Message{ID: active.ActiveMessageID, Role: "assistant", CreatedAt: active.CreatedAt, UpdatedAt: active.CreatedAt},
		active,
	); err != nil {
		t.Fatalf("CreateAssistantRun active: %v", err)
	}
	for attempt := 0; attempt < 100; attempt++ {
		recovered, err := store.CreateAssistantRun(context.Background(), scope,
			Message{ID: "user-retry", Role: "user", ActorID: "actor-1", Content: "retry", CreatedAt: createdAt.Add(3 * time.Minute), UpdatedAt: createdAt.Add(3 * time.Minute)},
			Message{ID: "assistant-retry", Role: "assistant", CreatedAt: createdAt.Add(3 * time.Minute), UpdatedAt: createdAt.Add(3 * time.Minute)},
			testAssistantRun(t, "run-retry", completed.ClientRequestID, "assistant-retry", createdAt.Add(3*time.Minute)),
		)
		if err != nil {
			t.Fatalf("retry completed request on attempt %d: %v", attempt, err)
		}
		if recovered.ID != completed.ID {
			t.Fatalf("retry recovered %q, want %q", recovered.ID, completed.ID)
		}
	}
}

func TestMemoryAndEncryptedStoresDeleteDurableRunAndMessages(t *testing.T) {
	for _, tt := range []struct {
		name string
		new  func(*testing.T) Store
	}{
		{name: "memory", new: func(*testing.T) Store { return NewMemoryStore() }},
		{name: "encrypted", new: func(t *testing.T) Store {
			base := NewMemoryStore()
			wrapped, err := NewEncryptedStore(base, testEncryptionKeys(t))
			if err != nil {
				t.Fatalf("NewEncryptedStore: %v", err)
			}
			return wrapped
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := mustDurableAssistantRunStore(t, tt.new(t))
			scope := testAssistantRunScope()
			createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
			run := testAssistantRun(t, "run-1", "request-1", "assistant-1", createdAt)
			if _, err := store.CreateAssistantRun(context.Background(), scope,
				Message{ID: "user-1", Role: "user", ActorID: "actor-1", Content: "delete me", CreatedAt: createdAt, UpdatedAt: createdAt},
				Message{ID: run.ActiveMessageID, Role: "assistant", Content: "delete me too", CreatedAt: createdAt, UpdatedAt: createdAt},
				run,
			); err != nil {
				t.Fatalf("CreateAssistantRun: %v", err)
			}
			if err := store.DeleteProjectMessages(context.Background(), scope); err != nil {
				t.Fatalf("DeleteProjectMessages: %v", err)
			}
			if _, err := store.GetAssistantRun(context.Background(), scope, run.ID); !errors.Is(err, ErrAssistantRunNotFound) {
				t.Fatalf("GetAssistantRun after deletion error = %v, want not found", err)
			}
			page, err := store.ListMessages(context.Background(), scope, 10, "")
			if err != nil {
				t.Fatalf("ListMessages after deletion: %v", err)
			}
			if len(page.Items) != 0 {
				t.Fatalf("messages after deletion = %#v, want empty", page.Items)
			}
		})
	}
}

func TestEncryptedStoreEncryptsDurableAssistantRunSnapshots(t *testing.T) {
	base := NewMemoryStore()
	store, err := NewEncryptedStore(base, testEncryptionKeys(t))
	if err != nil {
		t.Fatalf("NewEncryptedStore: %v", err)
	}
	durable := mustDurableAssistantRunStore(t, store)
	scope := testAssistantRunScope()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	run := testAssistantRun(t, "run-1", "request-1", "assistant-1", createdAt)
	run.Checkpoint = json.RawMessage(`{"tool":"write_file","content":"secret"}`)
	run.Audit = json.RawMessage(`{"result":"secret"}`)
	if _, err := durable.CreateAssistantRun(context.Background(), scope,
		Message{ID: "user-1", Role: "user", ActorID: "actor-1", Content: "secret prompt", CreatedAt: createdAt, UpdatedAt: createdAt},
		Message{ID: assistantRunString(t, run, "ActiveMessageID"), Role: "assistant", Content: "", CreatedAt: createdAt, UpdatedAt: createdAt},
		run,
	); err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	raw, err := base.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatalf("raw GetAssistantRun: %v", err)
	}
	if bytes.Equal(raw.Checkpoint, run.Checkpoint) || bytes.Equal(raw.Audit, run.Audit) {
		t.Fatalf("encrypted store persisted plaintext run blobs: %#v", raw)
	}
	rawMessages, err := base.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatalf("raw ListMessages after create: %v", err)
	}
	if len(rawMessages.Items) != 2 || rawMessages.Items[1].Content == "secret prompt" || !rawMessages.Items[1].ContentEncrypted {
		t.Fatalf("encrypted create persisted plaintext user message: %#v", rawMessages.Items)
	}
	got, err := durable.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun: %v", err)
	}
	if !bytes.Equal(got.Checkpoint, run.Checkpoint) || !bytes.Equal(got.Audit, run.Audit) {
		t.Fatalf("encrypted run round trip = %#v, want original blobs", got)
	}
	run.Revision = 2
	run.Checkpoint = json.RawMessage(`{"tool":"write_file","content":"new secret"}`)
	run.Audit = json.RawMessage(`{"result":"new secret"}`)
	run.UpdatedAt = createdAt.Add(time.Minute)
	snapshotMessage := Message{
		ID:        run.ActiveMessageID,
		Role:      "assistant",
		Content:   "snapshot secret",
		CreatedAt: createdAt,
		UpdatedAt: run.UpdatedAt,
	}
	if err := durable.SaveAssistantRunSnapshot(context.Background(), scope, run, []Message{snapshotMessage}, 1); err != nil {
		t.Fatalf("SaveAssistantRunSnapshot: %v", err)
	}
	raw, err = base.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatalf("raw GetAssistantRun after snapshot: %v", err)
	}
	if bytes.Equal(raw.Checkpoint, run.Checkpoint) || bytes.Equal(raw.Audit, run.Audit) {
		t.Fatalf("encrypted snapshot persisted plaintext run blobs: %#v", raw)
	}
	rawMessages, err = base.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatalf("raw ListMessages after snapshot: %v", err)
	}
	if rawMessages.Items[0].Content == snapshotMessage.Content || !rawMessages.Items[0].ContentEncrypted {
		t.Fatalf("encrypted snapshot persisted plaintext assistant message: %#v", rawMessages.Items[0])
	}
	page, err := durable.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatalf("ListMessages after snapshot: %v", err)
	}
	if page.Items[0].Content != snapshotMessage.Content || page.Items[0].ContentEncrypted {
		t.Fatalf("encrypted snapshot message read = %#v, want decrypted snapshot", page.Items[0])
	}
	got, err = durable.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatalf("GetAssistantRun after snapshot: %v", err)
	}
	if !bytes.Equal(got.Checkpoint, run.Checkpoint) || !bytes.Equal(got.Audit, run.Audit) {
		t.Fatalf("encrypted snapshot round trip = %#v, want original blobs", got)
	}
}

func mustDurableAssistantRunStore(t *testing.T, value any) durableAssistantRunStore {
	t.Helper()
	store, ok := value.(durableAssistantRunStore)
	if !ok {
		t.Fatalf("store %T does not implement durable assistant-run contract", value)
	}
	return store
}

func createTestAssistantRun(t *testing.T, store durableAssistantRunStore, scope Scope, createdAt time.Time) AssistantRun {
	t.Helper()
	run := testAssistantRun(t, "run-1", "request-1", "assistant-1", createdAt)
	created, err := store.CreateAssistantRun(context.Background(), scope,
		Message{ID: "user-1", Role: "user", ActorID: "actor-1", Content: "first", CreatedAt: createdAt, UpdatedAt: createdAt},
		Message{ID: assistantRunString(t, run, "ActiveMessageID"), Role: "assistant", CreatedAt: createdAt, UpdatedAt: createdAt},
		run,
	)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	return created
}

func testAssistantRun(t *testing.T, id, clientRequestID, activeMessageID string, createdAt time.Time) AssistantRun {
	t.Helper()
	return withAssistantRunFields(t, AssistantRun{
		ID:        id,
		Mode:      AssistantRunModeDiscussion,
		Status:    AssistantRunStatusRunning,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}, map[string]any{
		"ClientRequestID": clientRequestID,
		"ActiveMessageID": activeMessageID,
		"UserMessageID":   strings.Replace(activeMessageID, "assistant", "user", 1),
		"Revision":        int64(1),
	})
}

func TestValidateNewAssistantRunRequiresOriginAndConsistentMode(t *testing.T) {
	user := Message{ID: "user", Role: "user", ActorID: "actor-1"}
	assistant := Message{ID: "assistant", Role: "assistant"}
	base := AssistantRun{ID: "run", Mode: AssistantRunModeDiscussion, Status: AssistantRunStatusRunning, ClientRequestID: "request", UserMessageID: user.ID, ActiveMessageID: assistant.ID, Revision: 1}
	if err := validateNewAssistantRun(user, assistant, base); err != nil {
		t.Fatalf("valid discussion run: %v", err)
	}
	for name, run := range map[string]AssistantRun{
		"missing origin":        func() AssistantRun { r := base; r.UserMessageID = ""; return r }(),
		"mismatched origin":     func() AssistantRun { r := base; r.UserMessageID = "other"; return r }(),
		"missing mode":          func() AssistantRun { r := base; r.Mode = ""; return r }(),
		"discussion work item":  func() AssistantRun { r := base; r.WorkItemID = "item"; return r }(),
		"new without work item": func() AssistantRun { r := base; r.Mode = AssistantRunModeNew; return r }(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateNewAssistantRun(user, assistant, run); err == nil {
				t.Fatal("accepted invalid run")
			}
		})
	}
	for name, messages := range map[string]struct{ user, assistant Message }{
		"user role":        {user: Message{ID: user.ID, Role: "assistant", ActorID: user.ActorID}, assistant: assistant},
		"blank user actor": {user: Message{ID: user.ID, Role: "user", ActorID: "  "}, assistant: assistant},
		"assistant role":   {user: user, assistant: Message{ID: assistant.ID, Role: "user"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateNewAssistantRun(messages.user, messages.assistant, base); err == nil {
				t.Fatal("accepted invalid messages")
			}
		})
	}
	mutation := base
	mutation.Mode, mutation.WorkItemID = AssistantRunModeNew, "item"
	user.WorkItemID, assistant.WorkItemID = mutation.WorkItemID, mutation.WorkItemID
	if err := validateNewAssistantRun(user, assistant, mutation); err != nil {
		t.Fatalf("valid mutation run: %v", err)
	}
	for name, messages := range map[string]struct{ user, assistant Message }{
		"user work item":      {user: Message{ID: user.ID, Role: "user", ActorID: user.ActorID}, assistant: assistant},
		"assistant work item": {user: user, assistant: Message{ID: assistant.ID, Role: "assistant"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateNewAssistantRun(messages.user, messages.assistant, mutation); err == nil {
				t.Fatal("accepted mutation messages with mismatched work item")
			}
		})
	}
}

func withAssistantRunRevision(t *testing.T, run AssistantRun, revision int64) AssistantRun {
	t.Helper()
	return withAssistantRunFields(t, run, map[string]any{"Revision": revision})
}

func withAssistantRunFields(t *testing.T, run AssistantRun, fields map[string]any) AssistantRun {
	t.Helper()
	value := reflect.ValueOf(&run).Elem()
	for name, want := range fields {
		field := value.FieldByName(name)
		if !field.IsValid() {
			t.Fatalf("AssistantRun is missing %s", name)
		}
		field.Set(reflect.ValueOf(want).Convert(field.Type()))
	}
	return run
}

func assistantRunString(t *testing.T, run AssistantRun, name string) string {
	t.Helper()
	field := reflect.ValueOf(run).FieldByName(name)
	if !field.IsValid() {
		t.Fatalf("AssistantRun is missing %s", name)
	}
	return field.String()
}

func assistantRunRevision(t *testing.T, run AssistantRun) int64 {
	t.Helper()
	field := reflect.ValueOf(run).FieldByName("Revision")
	if !field.IsValid() {
		t.Fatal("AssistantRun is missing Revision")
	}
	return field.Int()
}

func testAssistantRunScope() Scope {
	return Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-1"}
}

func testEncryptionKeys(t *testing.T) []EncryptionKey {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	keys, err := ParseEncryptionKeys("primary:" + encoded)
	if err != nil {
		t.Fatalf("ParseEncryptionKeys: %v", err)
	}
	return keys
}
