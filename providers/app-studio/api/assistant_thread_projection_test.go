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

package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

func TestConcurrentAssistantThreadPatchesMergeFieldIntent(t *testing.T) {
	inner := store.NewMemoryStore()
	wrapped := &concurrentAssistantThreadPatchStore{
		Store:       inner,
		getReady:    make(chan struct{}),
		updateReady: make(chan struct{}),
	}
	server := NewWithWorkspace(nil, wrapped, nil, "", false)
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
