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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/store"
)

type assistantThreadPatchEventBarrierStore struct {
	store.Store
	started atomic.Bool
	blocked chan struct{}
	release chan struct{}
}

func (s *assistantThreadPatchEventBarrierStore) ListAssistantThreadEvents(ctx context.Context, scope store.Scope, threadID string, afterSequence int64, limit int) ([]store.AssistantThreadEvent, error) {
	if s.started.CompareAndSwap(false, true) {
		close(s.blocked)
		<-s.release
	}
	return s.Store.ListAssistantThreadEvents(ctx, scope, threadID, afterSequence, limit)
}

func TestConcurrentAssistantThreadReasoningPatchesRetainDistinctModels(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemoryStore()
	wrapper := &assistantThreadPatchEventBarrierStore{
		Store: inner, blocked: make(chan struct{}), release: make(chan struct{}),
	}
	server := NewWithWorkspace(nil, wrapper, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	if _, err := inner.CreateAssistantThread(ctx, scope, store.AssistantThread{ID: "thread-reasoning-merge", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}

	firstResult := make(chan error, 1)
	go func() {
		_, _, err := server.patchAssistantThreadWithEvent(ctx, scope, "thread-reasoning-merge", "alice", assistantThreadPatchRequest{
			Reasoning: &assistantThreadReasoningPatch{ModelID: "model-one", Effort: reasoningEffortPointer("high")},
		})
		firstResult <- err
	}()
	<-wrapper.blocked
	if _, _, err := server.patchAssistantThreadWithEvent(ctx, scope, "thread-reasoning-merge", "alice", assistantThreadPatchRequest{
		Reasoning: &assistantThreadReasoningPatch{ModelID: "model-two", Effort: reasoningEffortPointer("none")},
	}); err != nil {
		t.Fatalf("patch second model preference: %v", err)
	}
	close(wrapper.release)
	if err := <-firstResult; err != nil {
		t.Fatalf("patch first model preference: %v", err)
	}

	thread, err := inner.GetAssistantThread(ctx, scope, "thread-reasoning-merge")
	if err != nil {
		t.Fatal(err)
	}
	if thread.ReasoningEffortByModel["model-one"] != "high" || thread.ReasoningEffortByModel["model-two"] != "none" {
		t.Fatalf("merged reasoning preferences = %#v, want both model keys", thread.ReasoningEffortByModel)
	}
	events, err := inner.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("thread events = %d, want two preference updates", len(events))
	}
	var projected struct {
		Thread store.AssistantThread `json:"thread"`
	}
	if err := json.Unmarshal(events[len(events)-1].Payload, &projected); err != nil {
		t.Fatalf("decode canonical thread projection: %v", err)
	}
	if projected.Thread.ReasoningEffortByModel["model-one"] != "high" || projected.Thread.ReasoningEffortByModel["model-two"] != "none" {
		t.Fatalf("canonical projection preferences = %#v, want both model keys", projected.Thread.ReasoningEffortByModel)
	}
}

func TestAssistantThreadReasoningResetProjectionEmitsEmptyMap(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	if _, err := inner.CreateAssistantThread(ctx, scope, store.AssistantThread{
		ID: "thread-reasoning-reset", ActorID: "alice", ReasoningEffortByModel: map[string]string{"model-a": "high"}, CreatedAt: now, UpdatedAt: now,
	}, []store.AssistantThreadEvent{{Type: "thread.created", Payload: json.RawMessage(`{"thread":{"id":"thread-reasoning-reset"}}`), CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	_, event, err := server.patchAssistantThreadWithEvent(ctx, scope, "thread-reasoning-reset", "alice", assistantThreadPatchRequest{
		Reasoning: &assistantThreadReasoningPatch{ModelID: "model-a", Effort: reasoningEffortPointer("")},
	})
	if err != nil {
		t.Fatalf("reset thread preference: %v", err)
	}
	var projection map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &projection); err != nil {
		t.Fatalf("decode projection event: %v", err)
	}
	var thread map[string]json.RawMessage
	if err := json.Unmarshal(projection["thread"], &thread); err != nil {
		t.Fatalf("decode thread metadata: %v", err)
	}
	preferencesJSON, exists := thread["reasoningEffortByModel"]
	if !exists {
		t.Fatal("reset projection omitted reasoningEffortByModel; clients could retain stale preference")
	}
	var preferences map[string]string
	if err := json.Unmarshal(preferencesJSON, &preferences); err != nil || len(preferences) != 0 {
		t.Fatalf("reset projection preference map = %s err=%v, want empty object", preferencesJSON, err)
	}
}

func TestAssistantThreadReasoningPatchRejectsMissingEffort(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	if _, err := inner.CreateAssistantThread(ctx, scope, store.AssistantThread{
		ID: "thread-reasoning-missing-effort", ActorID: "alice", ReasoningEffortByModel: map[string]string{"model-a": "high"}, CreatedAt: now, UpdatedAt: now,
	}, []store.AssistantThreadEvent{{Type: "thread.created", Payload: json.RawMessage(`{"thread":{"id":"thread-reasoning-missing-effort"}}`), CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	var request assistantThreadPatchRequest
	if err := json.Unmarshal([]byte(`{"reasoningEffort":{"modelID":"model-a"}}`), &request); err != nil {
		t.Fatalf("decode incomplete reasoning patch: %v", err)
	}
	if request.Reasoning == nil || request.Reasoning.Effort != nil {
		t.Fatalf("incomplete reasoning patch = %#v, want absent effort", request.Reasoning)
	}
	if _, _, err := server.patchAssistantThreadWithEvent(ctx, scope, "thread-reasoning-missing-effort", "alice", request); err == nil {
		t.Fatal("patch without effort unexpectedly succeeded")
	}
	thread, err := inner.GetAssistantThread(ctx, scope, "thread-reasoning-missing-effort")
	if err != nil {
		t.Fatal(err)
	}
	if thread.ReasoningEffortByModel["model-a"] != "high" {
		t.Fatalf("missing effort cleared saved preference: %#v", thread.ReasoningEffortByModel)
	}
	events, err := inner.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("incomplete patch appended %d events, want no update", len(events)-1)
	}
}

func TestConcurrentAssistantThreadPatchesMergeFieldIntent(t *testing.T) {
	inner := store.NewMemoryStore()
	wrapped := &concurrentAssistantThreadPatchStore{
		Store:       inner,
		getReady:    make(chan struct{}),
		updateReady: make(chan struct{}),
	}
	server := NewWithWorkspace(nil, wrapped, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	if _, err := inner.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: "thread-merge", ActorID: "alice", Title: "before", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	title := "after"
	archived := true
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, err := server.patchAssistantThreadWithEvent(context.Background(), scope, "thread-merge", "alice", assistantThreadPatchRequest{Title: &title})
		results <- err
	}()
	go func() {
		defer wg.Done()
		_, _, err := server.patchAssistantThreadWithEvent(context.Background(), scope, "thread-merge", "alice", assistantThreadPatchRequest{Archived: &archived})
		results <- err
	}()
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	thread, err := inner.GetAssistantThread(context.Background(), scope, "thread-merge")
	if err != nil {
		t.Fatal(err)
	}
	if thread.Title != "after" || thread.Status != store.AssistantThreadStatusArchived {
		t.Fatalf("merged thread = %#v, want title and archived status preserved", thread)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, "thread-merge", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("thread update events = %d, want two committed patches", len(events))
	}
}
