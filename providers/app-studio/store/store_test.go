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
	"testing"
	"time"
)

func TestMemoryStorePaginationAndCleanup(t *testing.T) {
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "customer-portal"}
	base := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		if err := s.AppendMessage(context.Background(), scope, Message{
			ID:        string(rune('a' + i)),
			Role:      "user",
			Content:   string(rune('1' + i)),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
			UpdatedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("AppendMessage %d: %v", i, err)
		}
	}
	if err := s.SaveAssistantRun(context.Background(), scope, AssistantRun{
		ID:        "run-old",
		Status:    AssistantRunStatusPendingPermission,
		RequestID: "perm-old",
		CreatedAt: base,
		UpdatedAt: base,
	}); err != nil {
		t.Fatalf("SaveAssistantRun old: %v", err)
	}
	if err := s.SaveAssistantRun(context.Background(), scope, AssistantRun{
		ID:        "run-new",
		Status:    AssistantRunStatusPendingPermission,
		RequestID: "perm-new",
		CreatedAt: base.Add(2 * time.Minute),
		UpdatedAt: base.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("SaveAssistantRun new: %v", err)
	}

	page, err := s.ListMessages(context.Background(), scope, 2, "")
	if err != nil {
		t.Fatalf("ListMessages page 1: %v", err)
	}
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("unexpected first page: %#v", page)
	}
	if page.Items[0].ID != "a" || page.Items[1].ID != "b" {
		t.Fatalf("unexpected first page order: %#v", page.Items)
	}

	next, err := s.ListMessages(context.Background(), scope, 2, page.NextCursor)
	if err != nil {
		t.Fatalf("ListMessages page 2: %v", err)
	}
	if len(next.Items) != 1 || next.Items[0].ID != "c" {
		t.Fatalf("unexpected second page: %#v", next)
	}

	recent, err := s.LoadRecentMessages(context.Background(), scope, 2)
	if err != nil {
		t.Fatalf("LoadRecentMessages: %v", err)
	}
	if len(recent) != 2 || recent[0].ID != "b" || recent[1].ID != "c" {
		t.Fatalf("unexpected recent messages: %#v", recent)
	}

	deleted, err := s.DeleteMessagesOlderThan(context.Background(), base.Add(90*time.Second))
	if err != nil {
		t.Fatalf("DeleteMessagesOlderThan: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("expected 2 deleted messages and 1 assistant run, got %d", deleted)
	}
	remaining, err := s.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatalf("ListMessages after cleanup: %v", err)
	}
	if len(remaining.Items) != 1 || remaining.Items[0].ID != "c" {
		t.Fatalf("unexpected remaining messages: %#v", remaining)
	}
	if _, err := s.GetAssistantRun(context.Background(), scope, "run-old"); err == nil {
		t.Fatal("old assistant run still exists after cleanup")
	}
	if _, err := s.GetAssistantRun(context.Background(), scope, "run-new"); err != nil {
		t.Fatalf("new assistant run missing after cleanup: %v", err)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	when := time.Date(2026, 6, 12, 13, 0, 0, 123456000, time.UTC)
	cur := encodeCursor(when, "msg-1")
	gotWhen, gotID, err := decodeCursor(cur)
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if !gotWhen.Equal(when) || gotID != "msg-1" {
		t.Fatalf("cursor round trip mismatch: %v %q", gotWhen, gotID)
	}
}

func TestEncryptedStoreEncryptsAtRestAndDecryptsOnRead(t *testing.T) {
	base := NewMemoryStore()
	rawKey := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	keys, err := ParseEncryptionKeys("primary:" + rawKey)
	if err != nil {
		t.Fatalf("ParseEncryptionKeys: %v", err)
	}
	store, err := NewEncryptedStore(base, keys)
	if err != nil {
		t.Fatalf("NewEncryptedStore: %v", err)
	}

	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "customer-portal"}
	msg := Message{
		ID:        "msg-1",
		Role:      "user",
		Content:   "deploy the customer portal",
		CreatedAt: time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC),
	}
	if err := store.AppendMessage(context.Background(), scope, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	rawPage, err := base.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatalf("raw ListMessages: %v", err)
	}
	if len(rawPage.Items) != 1 {
		t.Fatalf("raw items: %#v", rawPage.Items)
	}
	if rawPage.Items[0].Content == msg.Content || !rawPage.Items[0].ContentEncrypted || rawPage.Items[0].ContentKeyID != "primary" {
		t.Fatalf("message was not encrypted at rest: %#v", rawPage.Items[0])
	}

	page, err := store.ListMessages(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatalf("encrypted ListMessages: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("decrypted items: %#v", page.Items)
	}
	if page.Items[0].Content != msg.Content || page.Items[0].ContentEncrypted {
		t.Fatalf("message was not decrypted on read: %#v", page.Items[0])
	}
}

func TestEncryptedStoreEncryptsAssistantRunCheckpointAtRest(t *testing.T) {
	base := NewMemoryStore()
	rawKey := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	keys, err := ParseEncryptionKeys("primary:" + rawKey)
	if err != nil {
		t.Fatalf("ParseEncryptionKeys: %v", err)
	}
	store, err := NewEncryptedStore(base, keys)
	if err != nil {
		t.Fatalf("NewEncryptedStore: %v", err)
	}

	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "customer-portal"}
	run := AssistantRun{
		ID:         "run-1",
		Status:     AssistantRunStatusPendingPermission,
		RequestID:  "perm-1",
		Checkpoint: json.RawMessage(`{"tool":"write_file","content":"secret"}`),
		Audit:      json.RawMessage(`{"decisions":[{"editedArguments":{"content":"secret"}}]}`),
	}
	if err := store.SaveAssistantRun(context.Background(), scope, run); err != nil {
		t.Fatalf("SaveAssistantRun: %v", err)
	}
	rawRun, err := base.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatalf("raw GetAssistantRun: %v", err)
	}
	if string(rawRun.Checkpoint) == string(run.Checkpoint) || !bytes.Contains(rawRun.Checkpoint, []byte(`"encrypted":true`)) {
		t.Fatalf("checkpoint was not encrypted at rest: %s", rawRun.Checkpoint)
	}
	if string(rawRun.Audit) == string(run.Audit) || !bytes.Contains(rawRun.Audit, []byte(`"encrypted":true`)) {
		t.Fatalf("audit was not encrypted at rest: %s", rawRun.Audit)
	}
	got, err := store.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatalf("encrypted GetAssistantRun: %v", err)
	}
	if string(got.Checkpoint) != string(run.Checkpoint) {
		t.Fatalf("checkpoint = %s, want %s", got.Checkpoint, run.Checkpoint)
	}
	if string(got.Audit) != string(run.Audit) {
		t.Fatalf("audit = %s, want %s", got.Audit, run.Audit)
	}
}
