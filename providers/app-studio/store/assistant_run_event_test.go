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
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMemoryStoreAssistantRunEventsAreAppendOnlyAndScoped(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	otherScope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-b"}
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	for _, candidate := range []Scope{scope, otherScope} {
		if err := s.SaveAssistantRun(ctx, candidate, AssistantRun{ID: "run-1", Mode: AssistantRunModeDefault, Status: AssistantRunStatusRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("SaveAssistantRun(%s): %v", candidate.ProjectUID, err)
		}
	}

	payload := json.RawMessage(`{"path":"src/App.vue"}`)
	first, err := s.AppendAssistantRunEvent(ctx, scope, AssistantRunEvent{
		RunID:      "run-1",
		Type:       "tool_call",
		CallID:     "call-1",
		ToolName:   "read_file",
		ArgsDigest: "sha256:first",
		Payload:    payload,
		CreatedAt:  now,
	}, 0)
	if err != nil {
		t.Fatalf("AppendAssistantRunEvent first: %v", err)
	}
	if first.Sequence != 1 || first.ProjectUID != scope.ProjectUID || !first.CreatedAt.Equal(now) {
		t.Fatalf("first event = %#v, want scoped sequence 1", first)
	}
	payload[2] = 'X'
	first.Payload[2] = 'Y'

	second, err := s.AppendAssistantRunEvent(ctx, scope, AssistantRunEvent{
		RunID:     "run-1",
		Sequence:  2,
		Type:      "tool_result",
		CallID:    "call-1",
		ToolName:  "read_file",
		Payload:   json.RawMessage(`{"bytes":42}`),
		CreatedAt: now.Add(time.Second),
	}, 1)
	if err != nil {
		t.Fatalf("AppendAssistantRunEvent second: %v", err)
	}
	if second.Sequence != 2 {
		t.Fatalf("second sequence = %d, want 2", second.Sequence)
	}

	if _, err := s.AppendAssistantRunEvent(ctx, scope, AssistantRunEvent{RunID: "run-1", Type: "duplicate"}, 1); !errors.Is(err, ErrAssistantRunEventConflict) {
		t.Fatalf("duplicate expected sequence error = %v, want ErrAssistantRunEventConflict", err)
	}
	if _, err := s.AppendAssistantRunEvent(ctx, scope, AssistantRunEvent{RunID: "run-1", Sequence: 4, Type: "skip"}, 2); !errors.Is(err, ErrAssistantRunEventConflict) {
		t.Fatalf("skipped event sequence error = %v, want ErrAssistantRunEventConflict", err)
	}

	page, err := s.ListAssistantRunEvents(ctx, scope, "run-1", 0, 1)
	if err != nil {
		t.Fatalf("ListAssistantRunEvents first page: %v", err)
	}
	if len(page) != 1 || page[0].Sequence != 1 || string(page[0].Payload) != `{"path":"src/App.vue"}` {
		t.Fatalf("first event page = %#v", page)
	}
	next, err := s.ListAssistantRunEvents(ctx, scope, "run-1", page[0].Sequence, 10)
	if err != nil {
		t.Fatalf("ListAssistantRunEvents next page: %v", err)
	}
	if len(next) != 1 || next[0].Sequence != 2 {
		t.Fatalf("next event page = %#v", next)
	}
	isolated, err := s.ListAssistantRunEvents(ctx, otherScope, "run-1", 0, 10)
	if err != nil {
		t.Fatalf("ListAssistantRunEvents other project: %v", err)
	}
	if len(isolated) != 0 {
		t.Fatalf("other project events = %#v, want none", isolated)
	}
	if err := s.DeleteProjectMessages(ctx, scope); err != nil {
		t.Fatalf("DeleteProjectMessages: %v", err)
	}
	if _, err := s.ListAssistantRunEvents(ctx, scope, "run-1", 0, 10); !errors.Is(err, ErrAssistantRunNotFound) {
		t.Fatalf("deleted project event listing error = %v, want ErrAssistantRunNotFound", err)
	}
	if _, err := s.GetAssistantRun(ctx, otherScope, "run-1"); err != nil {
		t.Fatalf("DeleteProjectMessages removed another project run: %v", err)
	}
}

func TestAssistantPointLookupsAreScopedAndEncrypted(t *testing.T) {
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
			scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
			otherScope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-b"}
			now := time.Now().UTC()
			for _, candidate := range []Scope{scope, otherScope} {
				if err := s.AppendMessage(ctx, candidate, Message{
					ID: "assistant-1", Role: "assistant", Content: candidate.ProjectUID,
					Metadata: map[string]any{"scope": candidate.ProjectUID}, CreatedAt: now, UpdatedAt: now,
				}); err != nil {
					t.Fatal(err)
				}
				for _, runID := range []string{"run-1", "run-2"} {
					if err := s.SaveAssistantRun(ctx, candidate, AssistantRun{ID: runID, Mode: AssistantRunModeDefault, Status: AssistantRunStatusCompleted, CreatedAt: now, UpdatedAt: now}); err != nil {
						t.Fatal(err)
					}
				}
			}

			messages, err := s.GetMessagesByIDs(ctx, scope, []string{"missing", "assistant-1", "assistant-1"})
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != 1 || messages[0].Content != scope.ProjectUID || messages[0].Metadata["scope"] != scope.ProjectUID {
				t.Fatalf("scoped message lookup = %#v", messages)
			}

			for _, event := range []AssistantRunEvent{
				{RunID: "run-1", Type: "tool_result", CallID: "call-old", ToolName: "interact_development_preview", Payload: json.RawMessage(`{"result":"old"}`)},
				{RunID: "run-1", Type: "tool_result", CallID: "call-new", ToolName: "interact_development_preview", Payload: json.RawMessage(`{"result":"new"}`)},
				{RunID: "run-2", Type: "tool_result", CallID: "call-two", ToolName: "interact_development_preview", Payload: json.RawMessage(`{"result":"two"}`)},
			} {
				existing, listErr := s.ListAssistantRunEvents(ctx, scope, event.RunID, 0, 500)
				if listErr != nil {
					t.Fatal(listErr)
				}
				if _, appendErr := s.AppendAssistantRunEvent(ctx, scope, event, int64(len(existing))); appendErr != nil {
					t.Fatal(appendErr)
				}
			}
			if _, err := s.AppendAssistantRunEvent(ctx, otherScope, AssistantRunEvent{
				RunID: "run-1", Type: "tool_result", CallID: "other-call", ToolName: "interact_development_preview", Payload: json.RawMessage(`{"result":"other"}`),
			}, 0); err != nil {
				t.Fatal(err)
			}

			events, err := s.ListAssistantRunEventsByRuns(ctx, scope, []string{"run-1", "run-2", "run-1"}, "tool_result", 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 || events[0].CallID != "call-new" || string(events[0].Payload) != `{"result":"new"}` || events[1].CallID != "call-two" {
				t.Fatalf("scoped run event lookup = %#v", events)
			}
		})
	}
}

func TestMemoryStoreAssistantRunEventCASSerializesWriters(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	if err := s.SaveAssistantRun(ctx, scope, AssistantRun{ID: "run-1", Mode: AssistantRunModeDefault, Status: AssistantRunStatusRunning}); err != nil {
		t.Fatalf("SaveAssistantRun: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.AppendAssistantRunEvent(ctx, scope, AssistantRunEvent{RunID: "run-1", Type: "race"}, 0)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var successes, conflicts int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrAssistantRunEventConflict):
			conflicts++
		default:
			t.Fatalf("concurrent append error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent appends successes=%d conflicts=%d, want 1 and 1", successes, conflicts)
	}
}

func TestMemoryStoreAssistantRunContractAndEventRetention(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	run := AssistantRun{
		ID:              "run-1",
		Mode:            AssistantRunModeDefault,
		ApprovalMode:    AssistantApprovalModeAutoApprove,
		Status:          AssistantRunStatusRunning,
		ClientRequestID: "request-1",
		UserMessageID:   "user-1",
		ActiveMessageID: "assistant-1",
		Revision:        1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	user := Message{ID: run.UserMessageID, Role: "user", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	assistant := Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := s.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	changed := created
	changed.Mode = AssistantRunModePlan
	changed.Revision++
	changed.UpdatedAt = now.Add(time.Second)
	if err := s.SaveAssistantRunSnapshot(ctx, scope, changed, nil, created.Revision); !errors.Is(err, ErrAssistantRunConflict) {
		t.Fatalf("changed run contract snapshot error = %v, want ErrAssistantRunConflict", err)
	}

	terminal := created
	terminal.Status = AssistantRunStatusCompleted
	terminal.UpdatedAt = now.Add(2 * time.Second)
	if err := s.SaveAssistantRun(ctx, scope, terminal); err != nil {
		t.Fatalf("SaveAssistantRun terminal: %v", err)
	}
	if _, err := s.AppendAssistantRunEvent(ctx, scope, AssistantRunEvent{RunID: run.ID, Type: "completed"}, 0); err != nil {
		t.Fatalf("AppendAssistantRunEvent: %v", err)
	}
	deleted, err := s.DeleteMessagesOlderThan(ctx, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("DeleteMessagesOlderThan: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted = %d, want two unassigned messages plus one run", deleted)
	}
	if _, err := s.ListAssistantRunEvents(ctx, scope, run.ID, 0, 10); !errors.Is(err, ErrAssistantRunNotFound) {
		t.Fatalf("events after retention error = %v, want ErrAssistantRunNotFound", err)
	}
}

func TestMemoryStoreRetentionProtectsActiveTranscriptAndCleansEveryTerminalState(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	old := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for _, messageID := range []string{"active-user", "active-assistant", "completed-message", "failed-message", "interrupted-message", "aborted-message"} {
		if err := memory.AppendMessage(ctx, scope, Message{ID: messageID, Role: "assistant", CreatedAt: old, UpdatedAt: old}); err != nil {
			t.Fatalf("AppendMessage %s: %v", messageID, err)
		}
	}
	if err := memory.SaveAssistantRun(ctx, scope, AssistantRun{
		ID: "run-active", Mode: AssistantRunModeDefault, Status: AssistantRunStatusRunning,
		UserMessageID: "active-user", ActiveMessageID: "active-assistant", CreatedAt: old, UpdatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	terminal := []struct {
		status    AssistantRunStatus
		runID     string
		messageID string
	}{
		{AssistantRunStatusCompleted, "run-completed", "completed-message"},
		{AssistantRunStatusFailed, "run-failed", "failed-message"},
		{AssistantRunStatusInterrupted, "run-interrupted", "interrupted-message"},
		{AssistantRunStatusAborted, "run-aborted", "aborted-message"},
	}
	for _, item := range terminal {
		if err := memory.SaveAssistantRun(ctx, scope, AssistantRun{
			ID: item.runID, Mode: AssistantRunModeDefault, Status: item.status,
			CreatedAt: old, UpdatedAt: old,
		}); err != nil {
			t.Fatalf("SaveAssistantRun %s: %v", item.status, err)
		}
		if _, err := memory.AppendAssistantRunEvent(ctx, scope, AssistantRunEvent{RunID: item.runID, Type: "terminal", CreatedAt: old}, 0); err != nil {
			t.Fatalf("AppendAssistantRunEvent %s: %v", item.status, err)
		}
		if _, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
			ID: item.runID + "-item", RunID: item.runID, Type: "assistant_message", CreatedAt: old,
		}); err != nil {
			t.Fatalf("AppendAssistantConversationItem %s: %v", item.status, err)
		}
	}
	if _, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "active-item", RunID: "run-active", Type: "assistant_message", CreatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}

	deleted, err := memory.DeleteMessagesOlderThan(ctx, old.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != int64(len(terminal))*2 {
		t.Fatalf("deleted = %d, want stale terminal messages and terminal runs", deleted)
	}
	page, err := memory.ListMessages(ctx, scope, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != "active-assistant" || page.Items[1].ID != "active-user" {
		t.Fatalf("active transcript messages = %#v, want both active messages", page.Items)
	}
	if _, err := memory.GetAssistantRun(ctx, scope, "run-active"); err != nil {
		t.Fatalf("active run was removed: %v", err)
	}
	for _, item := range terminal {
		if _, err := memory.GetAssistantRun(ctx, scope, item.runID); !errors.Is(err, ErrAssistantRunNotFound) {
			t.Fatalf("terminal run %s after retention = %v, want not found", item.status, err)
		}
		if _, err := memory.ListAssistantRunEvents(ctx, scope, item.runID, 0, 10); !errors.Is(err, ErrAssistantRunNotFound) {
			t.Fatalf("terminal event stream %s after retention = %v, want not found", item.status, err)
		}
	}
	items, err := memory.ListAssistantConversationItems(ctx, scope, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RunID != "run-active" {
		t.Fatalf("conversation items after retention = %#v, want active item only", items)
	}
}

func TestEncryptedStoreEncryptsAssistantRunEventPayload(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryStore()
	rawKey := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	keys, err := ParseEncryptionKeys("primary:" + rawKey)
	if err != nil {
		t.Fatalf("ParseEncryptionKeys: %v", err)
	}
	encrypted, err := NewEncryptedStore(base, keys)
	if err != nil {
		t.Fatalf("NewEncryptedStore: %v", err)
	}
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	if err := encrypted.SaveAssistantRun(ctx, scope, AssistantRun{ID: "run-1", Mode: AssistantRunModeDefault, Status: AssistantRunStatusRunning}); err != nil {
		t.Fatalf("SaveAssistantRun: %v", err)
	}
	wantPayload := json.RawMessage(`{"secret":"tool-result-secret"}`)
	if _, err := encrypted.AppendAssistantRunEvent(ctx, scope, AssistantRunEvent{RunID: "run-1", Type: "tool_result", Payload: wantPayload}, 0); err != nil {
		t.Fatalf("AppendAssistantRunEvent: %v", err)
	}
	raw, err := base.ListAssistantRunEvents(ctx, scope, "run-1", 0, 10)
	if err != nil {
		t.Fatalf("raw ListAssistantRunEvents: %v", err)
	}
	if len(raw) != 1 || strings.Contains(string(raw[0].Payload), "tool-result-secret") {
		t.Fatalf("raw event payload was not encrypted: %#v", raw)
	}
	got, err := encrypted.ListAssistantRunEvents(ctx, scope, "run-1", 0, 10)
	if err != nil {
		t.Fatalf("encrypted ListAssistantRunEvents: %v", err)
	}
	if len(got) != 1 || string(got[0].Payload) != string(wantPayload) {
		t.Fatalf("decrypted events = %#v, want payload %s", got, wantPayload)
	}
}

func TestEncryptedStoreConversationItemsAreAppendOnlyAndEncrypted(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryStore()
	encrypted, err := NewEncryptedStore(base, testEncryptionKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	if err := encrypted.SaveAssistantRun(ctx, scope, AssistantRun{ID: "run-1", Mode: AssistantRunModeDefault, Status: AssistantRunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"role":"user","content":"private question"}`)
	first, err := encrypted.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{ID: "item-1", RunID: "run-1", Type: "user_message", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := encrypted.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{ID: "item-1", RunID: "run-1", Type: "user_message", Payload: payload})
	if err != nil || replayed.Sequence != first.Sequence {
		t.Fatalf("idempotent append = (%#v, %v), want sequence %d", replayed, err, first.Sequence)
	}
	second, err := encrypted.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "item-1", RunID: "run-2", Type: "tool_result", Payload: json.RawMessage(`{"content":"different run"}`),
	})
	if err != nil || second.Sequence != first.Sequence+1 {
		t.Fatalf("run-scoped append = (%#v, %v), want next sequence", second, err)
	}
	raw, err := base.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil || len(raw) != 2 || strings.Contains(string(raw[0].Payload), "private question") || strings.Contains(string(raw[1].Payload), "different run") {
		t.Fatalf("raw conversation items = %#v, err=%v", raw, err)
	}
	got, err := encrypted.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil || len(got) != 2 || string(got[0].Payload) != string(payload) || string(got[1].Payload) != `{"content":"different run"}` {
		t.Fatalf("decrypted conversation items = %#v, err=%v", got, err)
	}
	if err := encrypted.DeleteProjectMessages(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if remaining, err := encrypted.ListAssistantConversationItems(ctx, scope, 0, 10); err != nil || len(remaining) != 0 {
		t.Fatalf("conversation items after project deletion = %#v, err=%v", remaining, err)
	}
}

func TestMemoryStoreConversationItemIdentityIsRunScopedAndReplayChecked(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	first, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "tool-result-call-1", RunID: "run-1", Type: "tool_result", Payload: json.RawMessage(`{"result":"one"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "tool-result-call-1", RunID: "run-1", Type: "tool_result", Payload: json.RawMessage(` { "result": "one" } `),
	})
	if err != nil || replayed.Sequence != first.Sequence {
		t.Fatalf("semantic replay = (%#v, %v), want sequence %d", replayed, err, first.Sequence)
	}
	second, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "tool-result-call-1", RunID: "run-2", Type: "tool_result", Payload: json.RawMessage(`{"result":"two"}`),
	})
	if err != nil || second.Sequence != first.Sequence+1 {
		t.Fatalf("cross-run item = (%#v, %v), want next sequence", second, err)
	}
	for _, collision := range []AssistantConversationItem{
		{ID: first.ID, RunID: first.RunID, Type: "assistant_message", Payload: first.Payload},
		{ID: first.ID, RunID: first.RunID, Type: first.Type, Payload: json.RawMessage(`{"result":"changed"}`)},
	} {
		if _, err := memory.AppendAssistantConversationItem(ctx, scope, collision); !errors.Is(err, ErrAssistantConversationItemConflict) {
			t.Errorf("collision %#v error = %v, want ErrAssistantConversationItemConflict", collision, err)
		}
	}
	items, err := memory.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil || len(items) != 2 || items[0].RunID != "run-1" || items[1].RunID != "run-2" {
		t.Fatalf("stored items = %#v, err=%v", items, err)
	}
}

func TestMemoryStoreConversationSequenceUsesHighWaterMarkAfterRetentionGap(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	old := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := memory.SaveAssistantRun(ctx, scope, AssistantRun{
		ID: "run-old", Mode: AssistantRunModeDefault, Status: AssistantRunStatusCompleted,
		UpdatedAt: old, CreatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveAssistantRun(ctx, scope, AssistantRun{
		ID: "run-active", Mode: AssistantRunModeDefault, Status: AssistantRunStatusRunning,
		UpdatedAt: old, CreatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "old-item", RunID: "run-old", Type: "assistant_message", CreatedAt: old,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 {
		t.Fatalf("first sequence = %d, want 1", first.Sequence)
	}
	second, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "active-item", RunID: "run-active", Type: "assistant_message", CreatedAt: old,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Sequence != 2 {
		t.Fatalf("second sequence = %d, want 2", second.Sequence)
	}
	if _, err := memory.DeleteMessagesOlderThan(ctx, old.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	third, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "new-item", RunID: "run-active", Type: "tool_result", CreatedAt: old.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if third.Sequence != 3 {
		t.Fatalf("sequence after retention gap = %d, want 3", third.Sequence)
	}
}

func TestMemoryStoreConversationSequenceSurvivesAllItemsRetention(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	old := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := memory.SaveAssistantRun(ctx, scope, AssistantRun{
		ID: "run-old", Mode: AssistantRunModeDefault, Status: AssistantRunStatusCompleted,
		CreatedAt: old, UpdatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "old-item", RunID: "run-old", Type: "assistant_message", CreatedAt: old,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 {
		t.Fatalf("first sequence = %d, want 1", first.Sequence)
	}
	if _, err := memory.DeleteMessagesOlderThan(ctx, old.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	items, err := memory.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("conversation items after retention = %#v, want empty", items)
	}
	second, err := memory.AppendAssistantConversationItem(ctx, scope, AssistantConversationItem{
		ID: "new-item", RunID: "run-new", Type: "assistant_message", CreatedAt: old.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Sequence != 2 {
		t.Fatalf("sequence after all-items retention = %d, want 2", second.Sequence)
	}
	items, err = memory.ListAssistantConversationItems(ctx, scope, first.Sequence, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Sequence != 2 {
		t.Fatalf("items after old cursor = %#v, want sequence 2", items)
	}
}

func TestMemoryStoreAcceptsCodexTerminalRunStatuses(t *testing.T) {
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	for _, status := range []AssistantRunStatus{AssistantRunStatusFailed, AssistantRunStatusInterrupted} {
		if err := s.SaveAssistantRun(context.Background(), scope, AssistantRun{ID: "run-" + string(status), Mode: AssistantRunModeDefault, Status: status}); err != nil {
			t.Fatalf("SaveAssistantRun rejected Codex terminal status %q: %v", status, err)
		}
	}
}

func TestAssistantConversationLockKeyIsPostgresTextSafeAndScopeSpecific(t *testing.T) {
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	key := assistantConversationLockKey(scope)
	if len(key) != 64 || strings.ContainsRune(key, '\x00') {
		t.Fatalf("conversation lock key = %q, want 64-character text-safe digest", key)
	}
	other := scope
	other.ProjectUID = "project-b"
	if assistantConversationLockKey(other) == key {
		t.Fatal("conversation lock key did not change across project scopes")
	}
}
