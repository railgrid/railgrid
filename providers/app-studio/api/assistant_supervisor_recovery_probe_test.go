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
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProbeOldSubscriberCleanupCannotTouchReplacementOwner(t *testing.T) {
	inner := store.NewMemoryStore()
	failing := &failAssistantSnapshotSavesStore{Store: inner, failures: 2}
	supervisor := newProjectAssistantSupervisor(context.Background(), failing)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-replaced", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "actor-1", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	accumulator, err := supervisor.Attach(scope, run, assistant)
	if err != nil {
		t.Fatal(err)
	}
	_, oldUnsubscribe, err := supervisor.Subscribe(scope, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	accumulator.FailPersistence(errors.New("snapshot unavailable"))
	if _, err := supervisor.Attach(scope, run, assistant); err != nil {
		t.Fatalf("replacement Attach: %v", err)
	}
	_, replacementUnsubscribe, err := supervisor.Subscribe(scope, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer replacementUnsubscribe()

	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	supervisor.mu.Lock()
	before := len(supervisor.runs[key].subscribers)
	supervisor.mu.Unlock()
	oldUnsubscribe()
	supervisor.mu.Lock()
	after := len(supervisor.runs[key].subscribers)
	supervisor.mu.Unlock()
	if before != 1 || after != before {
		t.Fatalf("replacement subscribers before=%d after old cleanup=%d, want unchanged one subscriber", before, after)
	}
}

type blockingFallbackSnapshotStore struct {
	store.Store
	fallbackEntered chan struct{}
	releaseFallback chan struct{}
	mu              sync.Mutex
	calls           int
}

func (s *blockingFallbackSnapshotStore) SaveAssistantRunSnapshot(ctx context.Context, scope store.Scope, run store.AssistantRun, messages []store.Message, expectedRevision int64) error {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	switch call {
	case 1:
		return errors.New("primary snapshot unavailable")
	case 2:
		close(s.fallbackEntered)
		<-s.releaseFallback
		return errors.New("fallback snapshot unavailable")
	default:
		return s.Store.SaveAssistantRunSnapshot(ctx, scope, run, messages, expectedRevision)
	}
}

func TestProbeFallbackFailureAfterWorkerFinishClosesSubscribers(t *testing.T) {
	inner := store.NewMemoryStore()
	failing := &blockingFallbackSnapshotStore{
		Store:           inner,
		fallbackEntered: make(chan struct{}),
		releaseFallback: make(chan struct{}),
	}
	supervisor := newProjectAssistantSupervisor(context.Background(), failing)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-subscriber-leak", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "actor-1", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	workerCanceled := make(chan struct{})
	if err := supervisor.Start(context.Background(), scope, run, assistant, func(ctx context.Context, _ *projectAssistantSnapshotAccumulator) {
		<-ctx.Done()
		close(workerCanceled)
	}); err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe, err := supervisor.Subscribe(scope, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive its initial snapshot")
	}
	accumulator := supervisor.accumulatorFor(scope, run.ID)
	if accumulator == nil {
		t.Fatal("worker accumulator was not attached")
	}
	updateErr := make(chan error, 1)
	go func() { updateErr <- accumulator.UpdateText(context.Background(), "partial", true) }()
	select {
	case <-failing.fallbackEntered:
	case <-time.After(time.Second):
		t.Fatal("fallback save was not reached")
	}
	select {
	case <-workerCanceled:
	case <-time.After(time.Second):
		t.Fatal("persistence failure did not cancel the worker")
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	deadline := time.Now().Add(time.Second)
	for {
		supervisor.mu.Lock()
		_, attached := supervisor.runs[key]
		supervisor.mu.Unlock()
		if !attached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker finish did not release the terminal owner")
		}
		time.Sleep(time.Millisecond)
	}
	close(failing.releaseFallback)
	select {
	case <-updateErr:
	case <-time.After(time.Second):
		t.Fatal("snapshot update remained blocked after fallback release")
	}
	select {
	case _, ok := <-updates:
		if ok {
			t.Fatal("subscriber received an unexpected post-failure snapshot")
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber channel leaked after worker finish preceded fallback failure")
	}
}
