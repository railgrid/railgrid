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
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAssistantThreadTurnItemContract(t *testing.T) {
	for _, fixture := range []struct {
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
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			s := fixture.new(t)
			scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
			now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
			thread, err := s.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-1", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, []AssistantThreadEvent{
				{Type: "thread.created", Payload: json.RawMessage(`{"title":""}`), CreatedAt: now},
			})
			if err != nil {
				t.Fatal(err)
			}
			if thread.Status != AssistantThreadStatusIdle {
				t.Fatalf("thread status = %q", thread.Status)
			}

			turn1 := AssistantTurn{ID: "turn-1", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-1", Mode: AssistantRunModeDefault, Status: AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
			created, err := s.CreateAssistantTurn(ctx, scope, turn1, []AssistantThreadEvent{
				{Type: "turn.started", Payload: json.RawMessage(`{"turn":"turn-1"}`), CreatedAt: now},
				{Type: "item.completed", ItemID: "user-1", Payload: json.RawMessage(`{"content":"build it"}`), CreatedAt: now},
			})
			if err != nil {
				t.Fatal(err)
			}
			if created.ApprovalMode != AssistantApprovalModeOnRequest {
				t.Fatalf("approval mode = %q", created.ApprovalMode)
			}
			if replay, err := s.CreateAssistantTurn(ctx, scope, turn1, nil); err != nil || replay.ID != turn1.ID {
				t.Fatalf("idempotent turn = %#v, %v", replay, err)
			}
			if _, err := s.CreateAssistantTurn(ctx, scope, AssistantTurn{ID: "turn-conflict", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-conflict"}, nil); !errors.Is(err, ErrAssistantTurnConflict) {
				t.Fatalf("active turn conflict = %v", err)
			}

			created.Status = AssistantTurnStatusCompleted
			created.UpdatedAt = now.Add(time.Second)
			if err := s.SaveAssistantTurnWithEvent(ctx, scope, created, AssistantThreadEvent{Type: "turn.completed", Payload: json.RawMessage(`{"turn":"turn-1"}`)}, 3); err != nil {
				t.Fatal(err)
			}
			turn2, err := s.CreateAssistantTurn(ctx, scope, AssistantTurn{ID: "turn-2", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-2", CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)}, []AssistantThreadEvent{
				{Type: "turn.started", Payload: json.RawMessage(`{"turn":"turn-2"}`)},
			})
			if err != nil {
				t.Fatal(err)
			}
			if turn2.ID != "turn-2" {
				t.Fatalf("turn 2 = %#v", turn2)
			}
			events, err := s.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 5 {
				t.Fatalf("events = %#v", events)
			}
			for index, event := range events {
				if event.Sequence != int64(index+1) {
					t.Fatalf("event %d sequence = %d", index, event.Sequence)
				}
			}
			recent, err := s.ListAssistantThreadEventsBefore(ctx, scope, thread.ID, 0, 2)
			if err != nil {
				t.Fatal(err)
			}
			if len(recent) != 2 || recent[0].Sequence != 4 || recent[1].Sequence != 5 {
				t.Fatalf("recent reverse page = %#v, want chronological sequences 4 and 5", recent)
			}
			older, err := s.ListAssistantThreadEventsBefore(ctx, scope, thread.ID, recent[0].Sequence, 2)
			if err != nil {
				t.Fatal(err)
			}
			if len(older) != 2 || older[0].Sequence != 2 || older[1].Sequence != 3 {
				t.Fatalf("older reverse page = %#v, want chronological sequences 2 and 3", older)
			}
			turnStart, err := s.GetAssistantThreadTurnStartSequence(ctx, scope, thread.ID, turn2.ID)
			if err != nil {
				t.Fatal(err)
			}
			if turnStart != 5 {
				t.Fatalf("turn 2 start sequence = %d, want 5", turnStart)
			}
		})
	}
}

func TestAssistantThreadTurnEventWindowExcludesThreadMetadata(t *testing.T) {
	for _, fixture := range []struct {
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
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			s := fixture.new(t)
			scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
			now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
			thread, err := s.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-turn-events", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, []AssistantThreadEvent{
				{Type: "thread.created", CreatedAt: now},
				{TurnID: "turn-old", Type: "turn.started", CreatedAt: now},
				{TurnID: "turn-old", Type: "turn.completed", CreatedAt: now},
				{Type: "thread.updated", CreatedAt: now},
				{Type: "thread.updated", CreatedAt: now},
			})
			if err != nil {
				t.Fatal(err)
			}

			events, err := s.ListAssistantThreadTurnEventsBefore(ctx, scope, thread.ID, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 || events[0].Sequence != 2 || events[1].Sequence != 3 {
				t.Fatalf("turn event window = %#v, want chronological sequences 2 and 3", events)
			}
			if events[0].TurnID != "turn-old" || events[1].TurnID != "turn-old" {
				t.Fatalf("turn event IDs = %#v, want only turn-old", events)
			}
		})
	}
}

func TestMemoryAssistantThreadSequenceIndexesTrackAndCleanUp(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	events := []AssistantThreadEvent{
		{Type: "thread.created", CreatedAt: now},
		{TurnID: "turn-old", Type: "turn.started", CreatedAt: now},
		{TurnID: "turn-old", Type: "turn.completed", CreatedAt: now},
	}
	for index := 0; index < 10_001; index++ {
		events = append(events, AssistantThreadEvent{Type: "thread.updated", CreatedAt: now})
	}
	thread, err := memory.CreateAssistantThread(ctx, scope, AssistantThread{
		ID: "thread-indexed", ActorID: "alice", CreatedAt: now, UpdatedAt: now,
	}, events)
	if err != nil {
		t.Fatal(err)
	}

	forward, err := memory.ListAssistantThreadEvents(ctx, scope, thread.ID, 10_002, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(forward) != 2 || forward[0].Sequence != 10_003 || forward[1].Sequence != 10_004 {
		t.Fatalf("indexed forward page = %#v, want sequences 10003 and 10004", forward)
	}
	if _, err := memory.AppendAssistantThreadEvent(ctx, scope, AssistantThreadEvent{
		ThreadID: thread.ID, TurnID: "turn-old", Type: "turn.started", CreatedAt: now,
	}, 10_004); err != nil {
		t.Fatal(err)
	}
	start, err := memory.GetAssistantThreadTurnStartSequence(ctx, scope, thread.ID, "turn-old")
	if err != nil || start != 2 {
		t.Fatalf("indexed turn start = %d, %v; want 2", start, err)
	}
	if memory.threadTurnStarts[scope][thread.ID]["turn-old"] != 2 {
		t.Fatalf("maintained turn-start index = %#v", memory.threadTurnStarts[scope][thread.ID])
	}

	if err := memory.DeleteAssistantThread(ctx, scope, thread.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := memory.threadTurnStarts[scope]; ok {
		t.Fatalf("turn-start scope index retained after deleting its final thread: %#v", memory.threadTurnStarts[scope])
	}
	if _, ok := memory.threadTurnEvents[scope]; ok {
		t.Fatalf("turn-event scope index retained after deleting its final thread: %#v", memory.threadTurnEvents[scope])
	}
}

func TestAssistantThreadListingIsActorScoped(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	for _, actor := range []string{"alice", "bob"} {
		if _, err := s.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-" + actor, ActorID: actor}, nil); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListAssistantThreads(ctx, scope, "alice", false, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ActorID != "alice" {
		t.Fatalf("actor-scoped threads = %#v", page.Items)
	}
}

func TestDeleteAssistantThreadDeletesTurnOwnedAttachments(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC().Truncate(time.Microsecond)
	thread, err := s.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-delete", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := s.CreateAssistantTurn(ctx, scope, AssistantTurn{ID: "turn-delete", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-delete", Status: AssistantTurnStatusCompleted, CreatedAt: now, UpdatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	expires := now.Add(time.Hour)
	attachment := attachmentTestRecord(now, true, &expires)
	attachment.ID = "turn-owned"
	if _, err := s.CreateAttachment(ctx, scope, attachment); err != nil {
		t.Fatal(err)
	}
	receipt := AttachmentReceipt{ID: attachment.ID, Filename: attachment.Filename, ContentType: attachment.ContentType, SizeBytes: attachment.SizeBytes, SHA256: attachment.SHA256, CreatedAt: attachment.CreatedAt}
	if _, err := s.BindAttachments(ctx, scope, []AttachmentReceipt{receipt}, "alice", turn.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAssistantThread(ctx, scope, thread.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAttachment(ctx, scope, attachment.ID); !errors.Is(err, ErrAttachmentNotFound) {
		t.Fatalf("turn-owned attachment after thread deletion = %v", err)
	}
}

func TestAssistantRetentionDeletesAttachmentsWithExpiredConversation(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid-retention"}
	old := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	thread, err := s.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-retention", ActorID: "alice", CreatedAt: old, UpdatedAt: old}, nil)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := s.CreateAssistantTurn(ctx, scope, AssistantTurn{ID: "turn-retention", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-retention", Status: AssistantTurnStatusCompleted, CreatedAt: old, UpdatedAt: old}, nil)
	if err != nil {
		t.Fatal(err)
	}
	thread.Status = AssistantThreadStatusIdle
	thread.UpdatedAt = old
	if _, err := s.UpdateAssistantThread(ctx, scope, thread); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Hour)
	attachment := attachmentTestRecord(old, true, &expires)
	attachment.ID = "retained-until-conversation"
	if _, err := s.CreateAttachment(ctx, scope, attachment); err != nil {
		t.Fatal(err)
	}
	receipt := AttachmentReceipt{ID: attachment.ID, Filename: attachment.Filename, ContentType: attachment.ContentType, SizeBytes: attachment.SizeBytes, SHA256: attachment.SHA256, CreatedAt: attachment.CreatedAt}
	if _, err := s.BindAttachments(ctx, scope, []AttachmentReceipt{receipt}, "alice", turn.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteMessagesOlderThan(ctx, old.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAttachment(ctx, scope, attachment.ID); !errors.Is(err, ErrAttachmentNotFound) {
		t.Fatalf("attachment after conversation retention = %v", err)
	}
}

func TestUpdateAssistantThreadWithEventIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	thread, err := s.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-atomic", ActorID: "alice", Title: "before", CreatedAt: now, UpdatedAt: now}, []AssistantThreadEvent{{Type: "thread.created", CreatedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	thread.Title = "after"
	thread.UpdatedAt = now.Add(time.Second)
	_, _, err = s.UpdateAssistantThreadWithEvent(ctx, scope, thread, AssistantThreadEvent{Type: "thread.updated", Payload: json.RawMessage(`{"title":"after"}`)}, 0)
	if !errors.Is(err, ErrAssistantThreadEventConflict) {
		t.Fatalf("wrong sequence error = %v, want conflict", err)
	}
	unchanged, err := s.GetAssistantThread(ctx, scope, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Title != "before" {
		t.Fatalf("thread title after failed transaction = %q, want before", unchanged.Title)
	}
	events, err := s.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events after failed transaction = %d, want 1", len(events))
	}
	updated, created, err := s.UpdateAssistantThreadWithEvent(ctx, scope, thread, AssistantThreadEvent{Type: "thread.updated", Payload: json.RawMessage(`{"title":"after"}`)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "after" || created.Sequence != 2 {
		t.Fatalf("successful projection = %#v event=%#v", updated, created)
	}
}

func TestAssistantThreadLockKeyIsPostgresTextSafeAndUnambiguous(t *testing.T) {
	first := assistantThreadLockKey(
		Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"},
		"thread-1",
	)
	if bytes.IndexByte([]byte(first), 0) >= 0 {
		t.Fatalf("assistant thread lock key contains a PostgreSQL-invalid NUL byte: %q", first)
	}
	second := assistantThreadLockKey(
		Scope{OrgUUID: "org1", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"},
		"thread-1",
	)
	third := assistantThreadLockKey(
		Scope{OrgUUID: "org", WorkspaceUUID: "1workspace", ProjectName: "demo", ProjectUID: "uid"},
		"thread-1",
	)
	if second == third {
		t.Fatalf("length-prefixed assistant thread lock keys collided: %q", second)
	}
}

func TestEncryptedAssistantThreadPayloadIsNotPlaintextAtRest(t *testing.T) {
	base := NewMemoryStore()
	wrapped, err := NewEncryptedStore(base, testEncryptionKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	secret := json.RawMessage(`{"content":"secret prompt"}`)
	if _, err := wrapped.CreateAssistantThread(context.Background(), scope, AssistantThread{ID: "thread-1", ActorID: "alice"}, []AssistantThreadEvent{{Type: "thread.created", Payload: secret}}); err != nil {
		t.Fatal(err)
	}
	raw, err := base.ListAssistantThreadEvents(context.Background(), scope, "thread-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || bytes.Contains(raw[0].Payload, []byte("secret prompt")) {
		t.Fatalf("plaintext persisted: %s", raw[0].Payload)
	}
	clear, err := wrapped.ListAssistantThreadEvents(context.Background(), scope, "thread-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(clear) != 1 || !bytes.Equal(clear[0].Payload, secret) {
		t.Fatalf("decrypted payload = %s", clear[0].Payload)
	}
}

func TestEncryptedAssistantThreadTitleIsProtectedAcrossAllProjectionPaths(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryStore()
	wrapper, err := NewEncryptedStore(base, testEncryptionKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	created, err := wrapper.CreateAssistantThread(ctx, scope, AssistantThread{
		ID: "thread-title", ActorID: "alice", Title: "private title",
	}, []AssistantThreadEvent{{Type: "thread.created"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Title != "private title" {
		t.Fatalf("created title = %q, want clear title", created.Title)
	}
	assertEncryptedAssistantThreadTitle(t, base, scope, "private title")
	got, err := wrapper.GetAssistantThread(ctx, scope, created.ID)
	if err != nil || got.Title != "private title" {
		t.Fatalf("GetAssistantThread = %#v, %v", got, err)
	}
	page, err := wrapper.ListAssistantThreads(ctx, scope, "alice", false, 10, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Title != "private title" {
		t.Fatalf("ListAssistantThreads = %#v, %v", page, err)
	}

	updated := created
	updated.Title = "private update"
	updated.UpdatedAt = created.UpdatedAt.Add(time.Second)
	if _, err := wrapper.UpdateAssistantThread(ctx, scope, updated); err != nil {
		t.Fatal(err)
	}
	assertEncryptedAssistantThreadTitle(t, base, scope, "private update")
	got, err = wrapper.GetAssistantThread(ctx, scope, created.ID)
	if err != nil || got.Title != "private update" {
		t.Fatalf("updated GetAssistantThread = %#v, %v", got, err)
	}

	updated.Title = "private event update"
	updated.UpdatedAt = updated.UpdatedAt.Add(time.Second)
	if got, _, err := wrapper.UpdateAssistantThreadWithEvent(ctx, scope, updated, AssistantThreadEvent{
		Type: "thread.updated", Payload: json.RawMessage(`{"title":"private event update"}`),
	}, 1); err != nil || got.Title != "private event update" {
		t.Fatalf("UpdateAssistantThreadWithEvent = %#v, %v", got, err)
	}
	assertEncryptedAssistantThreadTitle(t, base, scope, "private event update")
}

func assertEncryptedAssistantThreadTitle(t *testing.T, base *MemoryStore, scope Scope, plaintext string) {
	t.Helper()
	raw, err := base.GetAssistantThread(context.Background(), scope, "thread-title")
	if err != nil {
		t.Fatal(err)
	}
	if raw.Title == plaintext || bytes.Contains([]byte(raw.Title), []byte(plaintext)) {
		t.Fatalf("thread title persisted in plaintext: %q", raw.Title)
	}
}

func TestMemoryStoreDeleteProjectMessagesRemovesCanonicalAssistantTranscript(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	thread, err := memory.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-delete", ActorID: "alice"}, []AssistantThreadEvent{{Type: "thread.created"}})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := memory.CreateAssistantTurn(ctx, scope, AssistantTurn{
		ID: "turn-delete", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-delete",
	}, []AssistantThreadEvent{{Type: "turn.started"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.AppendAssistantThreadEvent(ctx, scope, AssistantThreadEvent{ThreadID: thread.ID, TurnID: turn.ID, Type: "item.completed"}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{ID: "item-delete", RunID: "run-delete", Type: "assistant_message"}); err != nil {
		t.Fatal(err)
	}
	if err := memory.DeleteProjectMessages(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.GetAssistantThread(ctx, scope, thread.ID); !errors.Is(err, ErrAssistantThreadNotFound) {
		t.Fatalf("GetAssistantThread after delete = %v, want not found", err)
	}
	if _, err := memory.GetAssistantTurn(ctx, scope, thread.ID, turn.ID); !errors.Is(err, ErrAssistantTurnNotFound) {
		t.Fatalf("GetAssistantTurn after delete = %v, want not found", err)
	}
	if _, err := memory.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 10); !errors.Is(err, ErrAssistantThreadNotFound) {
		t.Fatalf("ListAssistantThreadEvents after delete = %v, want not found", err)
	}
	items, err := memory.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("conversation items after delete = %#v, %v; want empty", items, err)
	}
	created, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{ID: "item-after-delete", RunID: "run-after-delete", Type: "assistant_message"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Sequence != 1 {
		t.Fatalf("sequence after project deletion = %d, want reset to 1", created.Sequence)
	}
}

func TestMemoryStoreDeleteAssistantThreadEnforcesOwnershipAndActiveTurns(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	thread, err := memory.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-owned", ActorID: "alice"}, []AssistantThreadEvent{{Type: "thread.created"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.DeleteAssistantThread(ctx, scope, thread.ID, "bob"); !errors.Is(err, ErrAssistantThreadConflict) {
		t.Fatalf("wrong-actor delete error = %v, want ownership conflict", err)
	}
	if _, err := memory.GetAssistantThread(ctx, scope, thread.ID); err != nil {
		t.Fatalf("thread after wrong-actor delete = %v", err)
	}
	turn, err := memory.CreateAssistantTurn(ctx, scope, AssistantTurn{ID: "turn-owned", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-owned"}, []AssistantThreadEvent{{Type: "turn.started"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.DeleteAssistantThread(ctx, scope, thread.ID, "alice"); !errors.Is(err, ErrAssistantThreadActive) {
		t.Fatalf("active delete error = %v, want active conflict", err)
	}
	turn.Status = AssistantTurnStatusCompleted
	turn.UpdatedAt = time.Now().UTC()
	if err := memory.SaveAssistantTurn(ctx, scope, turn); err != nil {
		t.Fatal(err)
	}
	if err := memory.DeleteAssistantThread(ctx, scope, thread.ID, "alice"); err != nil {
		t.Fatalf("terminal delete error = %v", err)
	}
	if _, err := memory.GetAssistantThread(ctx, scope, thread.ID); !errors.Is(err, ErrAssistantThreadNotFound) {
		t.Fatalf("thread after delete = %v, want not found", err)
	}
	if _, err := memory.GetAssistantTurn(ctx, scope, thread.ID, turn.ID); !errors.Is(err, ErrAssistantTurnNotFound) {
		t.Fatalf("turn after delete = %v, want not found", err)
	}
	if _, err := memory.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 20); !errors.Is(err, ErrAssistantThreadNotFound) {
		t.Fatalf("events after delete = %v, want not found", err)
	}
}

func TestMemoryStoreDeleteAssistantThreadRemovesOnlyOwnedRunEventLookups(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	createThreadRun := func(threadID, turnID, callID string) {
		t.Helper()
		thread, err := memory.CreateAssistantThread(ctx, scope, AssistantThread{
			ID: threadID, ActorID: "alice", CreatedAt: now, UpdatedAt: now,
		}, []AssistantThreadEvent{{Type: "thread.created", CreatedAt: now}})
		if err != nil {
			t.Fatal(err)
		}
		turn, err := memory.CreateAssistantTurn(ctx, scope, AssistantTurn{
			ID: turnID, ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-" + turnID,
			Status: AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now,
		}, []AssistantThreadEvent{{Type: "turn.started", CreatedAt: now}})
		if err != nil {
			t.Fatal(err)
		}
		turn.Status = AssistantTurnStatusCompleted
		if err := memory.SaveAssistantTurn(ctx, scope, turn); err != nil {
			t.Fatal(err)
		}
		if err := memory.SaveAssistantRun(ctx, scope, AssistantRun{
			ID: turnID, Mode: AssistantRunModeDefault, Status: AssistantRunStatusCompleted, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := memory.AppendAssistantRunEvent(ctx, scope, AssistantRunEvent{
			RunID: turnID, Type: "tool_result", CallID: callID,
			ToolName: "interact_development_preview", Payload: json.RawMessage(`{"status":"succeeded"}`), CreatedAt: now,
		}, 0); err != nil {
			t.Fatal(err)
		}
	}
	createThreadRun("thread-delete", "turn-delete", "call-delete")
	createThreadRun("thread-keep", "turn-keep", "call-keep")

	before, err := memory.ListAssistantRunEventsByRuns(ctx, scope, []string{"turn-delete", "turn-keep"}, "tool_result", 1)
	if err != nil || len(before) != 2 {
		t.Fatalf("run event lookups before delete = %#v, %v", before, err)
	}
	if err := memory.DeleteAssistantThread(ctx, scope, "thread-delete", "alice"); err != nil {
		t.Fatal(err)
	}
	after, err := memory.ListAssistantRunEventsByRuns(ctx, scope, []string{"turn-delete", "turn-keep"}, "tool_result", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].RunID != "turn-keep" || after[0].CallID != "call-keep" {
		t.Fatalf("run event lookups after delete = %#v, want only retained thread", after)
	}
	for key := range memory.assistantEventLookup[scope] {
		if key.runID == "turn-delete" {
			t.Fatalf("deleted turn lookup key retained: %#v", key)
		}
	}
	if len(memory.assistantEventLookup[scope]) != 1 {
		t.Fatalf("lookup map size after delete = %d, want retained thread entry only", len(memory.assistantEventLookup[scope]))
	}
}

func TestMemoryStoreSetAssistantThreadTitleIfEmptyIsCompareAndSet(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	thread, err := memory.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-title-cas", ActorID: "alice"}, []AssistantThreadEvent{{Type: "thread.created"}})
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := memory.SetAssistantThreadTitleIfEmpty(ctx, scope, thread.ID, "alice", "Generated project title", AssistantThreadEvent{Type: "thread.updated"})
	if err != nil || !changed || updated.Title != "Generated project title" {
		t.Fatalf("first title CAS = %#v changed=%t err=%v", updated, changed, err)
	}
	updated, changed, err = memory.SetAssistantThreadTitleIfEmpty(ctx, scope, thread.ID, "alice", "Stale model title", AssistantThreadEvent{Type: "thread.updated"})
	if err != nil || changed || updated.Title != "Generated project title" {
		t.Fatalf("second title CAS = %#v changed=%t err=%v", updated, changed, err)
	}
	events, err := memory.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 20)
	if err != nil || len(events) != 2 {
		t.Fatalf("title events = %#v err=%v, want created+updated", events, err)
	}
	var payload struct {
		Thread AssistantThread `json:"thread"`
	}
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil || payload.Thread.Title != "Generated project title" {
		t.Fatalf("title event payload = %s err=%v", events[1].Payload, err)
	}
}

func TestEncryptedStoreSetAssistantThreadTitleIfEmptyProtectsTitleAndEvent(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryStore()
	wrapper, err := NewEncryptedStore(base, testEncryptionKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	thread, err := wrapper.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-encrypted-title", ActorID: "alice"}, []AssistantThreadEvent{{Type: "thread.created"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := wrapper.SetAssistantThreadTitleIfEmpty(ctx, scope, thread.ID, "alice", "Private generated title", AssistantThreadEvent{Type: "thread.updated"}); err != nil || !changed {
		t.Fatalf("encrypted title CAS changed=%t err=%v", changed, err)
	}
	raw, err := base.GetAssistantThread(ctx, scope, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw.Title, "Private generated title") {
		t.Fatalf("encrypted title persisted plaintext: %q", raw.Title)
	}
	events, err := wrapper.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 10)
	if err != nil || len(events) != 2 || !bytes.Contains(events[1].Payload, []byte("Private generated title")) {
		t.Fatalf("decrypted title event = %#v err=%v", events, err)
	}
}
