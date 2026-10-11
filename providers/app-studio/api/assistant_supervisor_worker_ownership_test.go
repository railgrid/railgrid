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
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/store"
)

type workerOwnershipReleaseAttempt struct {
	key string
	err error
}

type workerOwnershipStore struct {
	store.Store

	mu                    sync.Mutex
	failingSnapshots      int
	failingRunRelease     bool
	runClaimKey           string
	runReleaseAttempts    int
	releaseAttemptResults chan workerOwnershipReleaseAttempt
}

func (s *workerOwnershipStore) SaveAssistantRunSnapshot(ctx context.Context, scope store.Scope, run store.AssistantRun, messages []store.Message, expectedRevision int64) error {
	s.mu.Lock()
	if s.failingSnapshots > 0 {
		s.failingSnapshots--
		s.mu.Unlock()
		return errors.New("injected assistant snapshot failure")
	}
	s.mu.Unlock()
	return s.Store.SaveAssistantRunSnapshot(ctx, scope, run, messages, expectedRevision)
}

func (s *workerOwnershipStore) ReleaseReplicaClaim(ctx context.Context, key, ownerReplica string) error {
	if key != s.runClaimKey {
		return s.Store.ReleaseReplicaClaim(ctx, key, ownerReplica)
	}
	s.mu.Lock()
	s.runReleaseAttempts++
	fail := s.failingRunRelease
	if fail {
		s.failingRunRelease = false
	}
	s.mu.Unlock()
	if fail {
		err := errors.New("injected run claim release failure")
		s.releaseAttemptResults <- workerOwnershipReleaseAttempt{key: key, err: err}
		return err
	}
	err := s.Store.ReleaseReplicaClaim(ctx, key, ownerReplica)
	s.releaseAttemptResults <- workerOwnershipReleaseAttempt{key: key, err: err}
	return err
}

func (s *workerOwnershipStore) attempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runReleaseAttempts
}

func waitForWorkerOwnershipOwnerDrain(t *testing.T, supervisor *projectAssistantSupervisor, scope store.Scope) {
	t.Helper()
	key := projectAssistantWorkspaceKey(scope)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		supervisor.mu.Lock()
		owner := supervisor.activityOwners[key]
		changed := supervisor.activityOwnersChanged
		supervisor.mu.Unlock()
		if owner == nil {
			return
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatal("project activity owner did not drain after run-claim release")
		}
	}
}

func TestProjectAssistantWorkerLeaseOutlivesDetachedWorkerAndFencesReplacement(t *testing.T) {
	ctx := context.Background()
	scope := store.Scope{OrgUUID: "org-worker", WorkspaceUUID: "workspace-worker", ProjectName: "demo", ProjectUID: "project-worker"}
	memoryStore := store.NewMemoryStore()
	now := time.Now().UTC()
	run := store.AssistantRun{
		ID: "run-detached", ThreadID: "thread-a", Mode: store.AssistantRunModeDefault,
		Status: store.AssistantRunStatusRunning, ClientRequestID: "request-a",
		UserMessageID: "user-a", ActiveMessageID: "assistant-a", Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "actor", Content: "hello", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := memoryStore.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}

	claimKey := store.AssistantRunClaimKey(scope, run.ThreadID, run.ID)
	claimStore := &workerOwnershipStore{
		Store:                 memoryStore,
		failingSnapshots:      2,
		failingRunRelease:     true,
		runClaimKey:           claimKey,
		releaseAttemptResults: make(chan workerOwnershipReleaseAttempt, 4),
	}
	supervisor := newProjectAssistantSupervisor(context.Background(), claimStore)
	supervisor.SetReplicaIdentity("worker-ownership-test", "")
	oldWorkerStarted := make(chan *projectAssistantSnapshotAccumulator, 1)
	oldWorkerCanceled := make(chan struct{})
	allowOldWorkerExit := make(chan struct{})
	oldWorkerExited := make(chan struct{})
	var allowOldExitOnce sync.Once
	t.Cleanup(func() {
		allowOldExitOnce.Do(func() { close(allowOldWorkerExit) })
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		supervisor.Shutdown(ctx)
	})

	if err := supervisor.Start(ctx, scope, created, assistant, func(workerCtx context.Context, acc *projectAssistantSnapshotAccumulator) {
		oldWorkerStarted <- acc
		<-workerCtx.Done()
		close(oldWorkerCanceled)
		<-allowOldWorkerExit
		close(oldWorkerExited)
	}); err != nil {
		t.Fatalf("Start old worker: %v", err)
	}
	var oldAccumulator *projectAssistantSnapshotAccumulator
	select {
	case oldAccumulator = <-oldWorkerStarted:
	case <-time.After(time.Second):
		t.Fatal("old worker did not start")
	}

	if err := oldAccumulator.UpdateText(ctx, "undurable text", true); err == nil {
		t.Fatal("UpdateText succeeded despite both snapshot writes failing")
	}
	select {
	case <-oldWorkerCanceled:
	case <-time.After(time.Second):
		t.Fatal("persistence failure did not cancel the old worker")
	}

	claimKeyForRun := projectAssistantRunClaimKeyFor(scope, created)
	supervisor.mu.Lock()
	lease := supervisor.runClaimLeases[claimKeyForRun]
	active := supervisor.runs[oldAccumulator.key]
	supervisor.mu.Unlock()
	if lease == nil {
		t.Fatal("detached worker lost its local run lease")
	}
	if active != nil {
		t.Fatal("persistence failure did not detach the old run")
	}

	// A heartbeat retry must leave a detached lease alone while its worker is
	// still unwinding. The eligibility state is changed synchronously by the
	// scheduler, so this assertion does not depend on observing a goroutine.
	supervisor.retryPendingRunClaimReleases()
	supervisor.mu.Lock()
	releaseEligible, releaseStarted := lease.releaseEligible, lease.releaseStarted
	supervisor.mu.Unlock()
	if releaseEligible || releaseStarted || claimStore.attempts() != 0 {
		t.Fatalf("live worker run lease became releasable: eligible=%v started=%v attempts=%d", releaseEligible, releaseStarted, claimStore.attempts())
	}

	reservationCtx, cancelReservation := context.WithTimeout(ctx, time.Second)
	if release, reserveErr := supervisor.ReserveWorkspace(reservationCtx, scope); !errors.Is(reserveErr, store.ErrAssistantRunConflict) {
		if release != nil {
			release()
		}
		cancelReservation()
		t.Fatalf("ReserveWorkspace while detached worker is alive = %v, want conflict", reserveErr)
	}
	if acc, attachErr := supervisor.Attach(scope, created, assistant); !errors.Is(attachErr, store.ErrAssistantRunConflict) {
		if acc != nil {
			t.Fatal("same-ID Attach unexpectedly returned an accumulator")
		}
		cancelReservation()
		t.Fatalf("same-ID Attach while detached worker is alive = %v, want conflict", attachErr)
	}
	cancelReservation()

	allowOldExitOnce.Do(func() { close(allowOldWorkerExit) })
	select {
	case <-oldWorkerExited:
	case <-time.After(time.Second):
		t.Fatal("old worker did not unwind after release signal")
	}
	var firstRelease workerOwnershipReleaseAttempt
	select {
	case firstRelease = <-claimStore.releaseAttemptResults:
	case <-time.After(time.Second):
		t.Fatal("old worker exit did not attempt its deferred run-claim release")
	}
	if firstRelease.key != claimKey || firstRelease.err == nil {
		t.Fatalf("first run-claim release = %#v, want injected failure for %q", firstRelease, claimKey)
	}
	retryDeadline := time.Now().Add(time.Second)
	for {
		supervisor.mu.Lock()
		retryReady := lease.releaseEligible && !lease.releaseStarted && !lease.finished
		supervisor.mu.Unlock()
		if retryReady {
			break
		}
		if time.Now().After(retryDeadline) {
			t.Fatal("failed run-claim release did not become eligible for retry")
		}
		runtime.Gosched()
	}

	// Exercise the heartbeat retry path deterministically after the failed
	// deferred release. No wall-clock wait for the regular renewal interval is
	// needed.
	supervisor.retryPendingRunClaimReleases()
	var secondRelease workerOwnershipReleaseAttempt
	select {
	case secondRelease = <-claimStore.releaseAttemptResults:
	case <-time.After(time.Second):
		t.Fatal("retry scheduler did not retry the eligible run-claim release")
	}
	if secondRelease.key != claimKey || secondRelease.err != nil {
		t.Fatalf("retried run-claim release = %#v, want successful release for %q", secondRelease, claimKey)
	}
	select {
	case <-lease.done:
	case <-time.After(time.Second):
		t.Fatal("successful run-claim release did not finish its local lease")
	}
	waitForWorkerOwnershipOwnerDrain(t, supervisor, scope)

	reservationCtx, cancelReservation = context.WithTimeout(ctx, time.Second)
	releaseWorkspace, err := supervisor.ReserveWorkspace(reservationCtx, scope)
	cancelReservation()
	if err != nil {
		t.Fatalf("ReserveWorkspace after old worker and release finished: %v", err)
	}
	releaseWorkspace()

	replacementStarted := make(chan *projectAssistantSnapshotAccumulator, 1)
	allowReplacementExit := make(chan struct{})
	var allowReplacementExitOnce sync.Once
	replacementExited := make(chan struct{})
	if err := supervisor.Start(ctx, scope, created, assistant, func(_ context.Context, acc *projectAssistantSnapshotAccumulator) {
		replacementStarted <- acc
		<-allowReplacementExit
		close(replacementExited)
	}); err != nil {
		t.Fatalf("Start same-ID replacement after old worker exit: %v", err)
	}
	t.Cleanup(func() { allowReplacementExitOnce.Do(func() { close(allowReplacementExit) }) })
	var replacementAccumulator *projectAssistantSnapshotAccumulator
	select {
	case replacementAccumulator = <-replacementStarted:
	case <-time.After(time.Second):
		t.Fatal("replacement worker did not start")
	}

	if err := oldAccumulator.UpdateText(ctx, "stale callback", true); !errors.Is(err, store.ErrAssistantRunNotFound) {
		t.Fatalf("stale old accumulator UpdateText = %v, want not found", err)
	}
	oldAccumulator.FailPersistence(errors.New("stale persistence callback"))
	if committed, ok := replacementAccumulator.CommittedRun(); !ok || committed.Status != store.AssistantRunStatusRunning || committed.Revision != created.Revision {
		t.Fatalf("replacement committed run changed after stale callback: run=%#v ok=%v", committed, ok)
	}
	supervisor.mu.Lock()
	replacement := supervisor.runs[replacementAccumulator.key]
	replacementStillOwned := replacement != nil && replacement == replacementAccumulator.owner && replacement.workerStarted &&
		replacement.run.Status == store.AssistantRunStatusRunning && replacement.run.Revision == created.Revision
	supervisor.mu.Unlock()
	if !replacementStillOwned {
		t.Fatal("stale old-worker callback changed or removed the replacement owner")
	}

	if !supervisor.Abort(scope, created.ID, created.ThreadID) {
		t.Fatal("Abort did not find replacement worker")
	}
	allowReplacementExitOnce.Do(func() { close(allowReplacementExit) })
	select {
	case <-replacementExited:
	case <-time.After(time.Second):
		t.Fatal("replacement worker did not exit")
	}
}
