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

package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/store"
)

type blockingAssistantThreadEventsStore struct {
	store.Store
	entered chan struct{}
	release chan struct{}
}

func (s *blockingAssistantThreadEventsStore) ListAssistantThreadEvents(ctx context.Context, scope store.Scope, threadID string, afterSequence int64, limit int) ([]store.AssistantThreadEvent, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.Store.ListAssistantThreadEvents(ctx, scope, threadID, afterSequence, limit)
}

func TestSanitizeAssistantThreadTitleBoundsAndNormalizesModelOutput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "prefix and quotes", raw: "Title: \"Build a calmer project dashboard\"", want: "Build a calmer project dashboard"},
		{name: "fenced", raw: "```\nPlan the customer portal\n```", want: "Plan the customer portal"},
		{name: "too many words", raw: "Build a dashboard with charts filters and sharing", want: "Build a dashboard with charts filters and"},
		{name: "control whitespace", raw: "Improve\nthread\tsearch experience", want: "Improve thread search experience"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeAssistantThreadTitle(tt.raw); got != tt.want {
				t.Fatalf("sanitizeAssistantThreadTitle(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
	if got := sanitizeAssistantThreadTitle("Use two"); got != "" {
		t.Fatalf("short title = %q, want empty", got)
	}
	if got := sanitizeAssistantThreadTitle(strings.Repeat("verylongword", 20)); got != "" {
		t.Fatalf("oversized title = %q, want empty", got)
	}
}

func TestAssistantThreadTitleGeneratorUsesServerSeam(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, assistantThreadTitleGenerator: func(_ context.Context, _ *asclient.Client, prompt string) (string, error) {
		return "Summarize " + prompt + " request", nil
	}}
	// The seam is intentionally exercised through the sanitizer boundary; the
	// concrete client is nil because tests do not need a tenant connection.
	got, err := server.generateAssistantThreadTitle(context.Background(), nil, "three")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Summarize three request" {
		t.Fatalf("title seam result = %q", got)
	}
}

func TestTruncateAssistantThreadTitlePromptPreservesUTF8(t *testing.T) {
	got := truncateAssistantThreadTitlePrompt(strings.Repeat("é", projectAssistantThreadTitlePromptMaxBytes))
	if !utf8.ValidString(got) {
		t.Fatal("truncated title prompt is invalid UTF-8")
	}
	if len(got) > projectAssistantThreadTitlePromptMaxBytes {
		t.Fatalf("truncated prompt bytes = %d, want <= %d", len(got), projectAssistantThreadTitlePromptMaxBytes)
	}
}

func TestAssistantThreadTitleEligibilityUsesCanonicalUserItem(t *testing.T) {
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	thread, err := memory.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: "thread-title", ActorID: "alice"}, []store.AssistantThreadEvent{{Type: assistantThreadEventThreadCreated}})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, store: memory}
	if !server.assistantThreadTitleNeedsGeneration(context.Background(), scope, thread, "") {
		t.Fatal("new untitled thread should be eligible")
	}
	payload, err := json.Marshal(map[string]any{"item": map[string]any{"id": "user-current", "type": assistantThreadEventUserMessage}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := memory.UpdateAssistantThreadWithEvent(context.Background(), scope, thread, store.AssistantThreadEvent{Type: assistantThreadEventItemCompleted, Payload: payload}, 1); err != nil {
		t.Fatal(err)
	}
	if !server.assistantThreadTitleNeedsGeneration(context.Background(), scope, thread, "user-current") {
		t.Fatal("the first user item for this turn should remain eligible after admission")
	}
	if server.assistantThreadTitleNeedsGeneration(context.Background(), scope, thread, "user-next") {
		t.Fatal("a later user item must not request another title")
	}
}

func TestStartAssistantThreadTitleGenerationPersistsStreamedUpdate(t *testing.T) {
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	thread, err := memory.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: "thread-async-title", ActorID: "alice"}, []store.AssistantThreadEvent{{Type: assistantThreadEventThreadCreated}})
	if err != nil {
		t.Fatal(err)
	}
	userPayload, err := json.Marshal(map[string]any{"item": map[string]any{"id": "user-current", "type": assistantThreadEventUserMessage}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := memory.UpdateAssistantThreadWithEvent(context.Background(), scope, thread, store.AssistantThreadEvent{Type: assistantThreadEventItemCompleted, ItemID: "user-current", Payload: userPayload}, 1); err != nil {
		t.Fatal(err)
	}
	blockingStore := &blockingAssistantThreadEventsStore{
		Store: memory, entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseHistory := func() { releaseOnce.Do(func() { close(blockingStore.release) }) }
	t.Cleanup(releaseHistory)
	generated := make(chan struct{})
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders,
		store: blockingStore,
		assistantThreadTitleGenerator: func(_ context.Context, _ *asclient.Client, prompt string) (string, error) {
			if prompt != "build a compact thread pane" {
				t.Errorf("title prompt = %q", prompt)
			}
			close(generated)
			return "Build Compact Thread Pane", nil
		},
	}
	startReturned := make(chan struct{})
	go func() {
		server.startAssistantThreadTitleGeneration(nil, scope, identity{user: "alice"}, thread, "user-current", "build a compact thread pane")
		close(startReturned)
	}()
	select {
	case <-startReturned:
	case <-time.After(time.Second):
		releaseHistory()
		<-startReturned
		t.Fatal("title history lookup blocked turn-start scheduling")
	}
	select {
	case <-blockingStore.entered:
	case <-time.After(time.Second):
		t.Fatal("detached title task did not check thread history")
	}
	select {
	case <-generated:
		t.Fatal("title generation started before the history check completed")
	default:
	}
	releaseHistory()
	select {
	case <-generated:
	case <-time.After(time.Second):
		t.Fatal("detached title generator did not start")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		updated, getErr := memory.GetAssistantThread(context.Background(), scope, thread.ID)
		if getErr == nil && updated.Title == "Build Compact Thread Pane" {
			events, listErr := memory.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 10)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(events) != 3 || events[2].Type != assistantThreadEventThreadUpdated || !strings.Contains(string(events[2].Payload), "Build Compact Thread Pane") {
				t.Fatalf("title update events = %#v", events)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("generated title was not persisted")
}
