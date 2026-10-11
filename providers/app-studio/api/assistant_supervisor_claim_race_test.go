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
	"sync"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/store"
)

type assistantRunStopGateStore struct {
	store.Store
	stopStarted  chan struct{}
	continueStop chan struct{}
	stopOnce     sync.Once
	continueOnce sync.Once
}

func (s *assistantRunStopGateStore) RequestAssistantRunStopWithAssistantMessage(ctx context.Context, scope store.Scope, runID string, expectedRevision int64, assistant store.Message, now time.Time) (store.AssistantRun, error) {
	run, err := s.Store.RequestAssistantRunStopWithAssistantMessage(ctx, scope, runID, expectedRevision, assistant, now)
	if err != nil {
		return run, err
	}
	s.stopOnce.Do(func() {
		close(s.stopStarted)
		select {
		case <-s.continueStop:
		case <-ctx.Done():
		}
	})
	return run, nil
}

func (s *assistantRunStopGateStore) unblockStop() {
	s.continueOnce.Do(func() { close(s.continueStop) })
}

type assistantRunClaimGateStore struct {
	store.Store
	runClaimKey string

	claimStarted      chan struct{}
	continueClaim     chan struct{}
	claimErr          error
	claimOnce         sync.Once
	claimContinueOnce sync.Once

	releaseStarted      chan struct{}
	continueRelease     chan struct{}
	releaseFailed       chan struct{}
	failFirstRelease    bool
	releaseOnce         sync.Once
	releaseContinueOnce sync.Once
	mu                  sync.Mutex
	releaseCalls        int
}

func (s *assistantRunClaimGateStore) TryClaimReplica(ctx context.Context, claim store.ReplicaClaim, staleAfter time.Duration) (store.ReplicaClaim, bool, error) {
	if claim.Key == s.runClaimKey && s.claimStarted != nil {
		s.claimOnce.Do(func() {
			close(s.claimStarted)
			select {
			case <-s.continueClaim:
			case <-ctx.Done():
			}
		})
		if err := ctx.Err(); err != nil {
			return store.ReplicaClaim{}, false, err
		}
		if s.claimErr != nil {
			return store.ReplicaClaim{}, false, s.claimErr
		}
	}
	return s.Store.TryClaimReplica(ctx, claim, staleAfter)
}

func (s *assistantRunClaimGateStore) ReleaseReplicaClaim(ctx context.Context, key, ownerReplica string) error {
	if key == s.runClaimKey {
		s.mu.Lock()
		s.releaseCalls++
		call := s.releaseCalls
		s.mu.Unlock()
		if call == 1 && s.releaseStarted != nil {
			s.releaseOnce.Do(func() {
				close(s.releaseStarted)
				if s.continueRelease != nil {
					select {
					case <-s.continueRelease:
					case <-ctx.Done():
					}
				}
				if s.failFirstRelease {
					close(s.releaseFailed)
				}
			})
			if err := ctx.Err(); err != nil {
				return err
			}
			if s.failFirstRelease {
				return errors.New("simulated run-claim release failure")
			}
		}
	}
	return s.Store.ReleaseReplicaClaim(ctx, key, ownerReplica)
}

func (s *assistantRunClaimGateStore) unblockClaim() {
	s.claimContinueOnce.Do(func() { close(s.continueClaim) })
}

func (s *assistantRunClaimGateStore) unblockRelease() {
	s.releaseContinueOnce.Do(func() { close(s.continueRelease) })
}

func assistantRunForClaimRace(scope store.Scope, threadID, runID string) (store.AssistantRun, store.Message, store.Message) {
	now := time.Now().UTC()
	run := store.AssistantRun{
		ID: runID, ThreadID: threadID, Mode: store.AssistantRunModeDefault,
		Status: store.AssistantRunStatusRunning, ClientRequestID: "request-" + runID,
		UserMessageID: "user-" + runID, ActiveMessageID: "assistant-" + runID,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "actor", Content: "run", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now.Add(time.Microsecond), UpdatedAt: now.Add(time.Microsecond)}
	return run, user, assistant
}

func waitForAssistantClaimCondition(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestProjectAssistantSupervisorStopDuringBlockedRunClaim(t *testing.T) {
	for _, claimErr := range []error{nil, errors.New("simulated run claim failure")} {
		name := "claim succeeds late"
		if claimErr != nil {
			name = "claim fails late"
		}
		t.Run(name, func(t *testing.T) {
			inner := store.NewMemoryStore()
			scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
			run, user, assistant := assistantRunForClaimRace(scope, "thread-a", "run-a")
			if _, err := inner.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
				t.Fatal(err)
			}
			claimStore := &assistantRunClaimGateStore{
				Store: inner, runClaimKey: store.AssistantRunClaimKey(scope, run.ThreadID, run.ID),
				claimStarted: make(chan struct{}), continueClaim: make(chan struct{}), claimErr: claimErr,
			}
			supervisor := newProjectAssistantSupervisor(context.Background(), claimStore)
			t.Cleanup(func() {
				claimStore.unblockClaim()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				supervisor.Shutdown(ctx)
			})

			startResult := make(chan error, 1)
			workerStarted := make(chan struct{}, 1)
			go func() {
				startResult <- supervisor.Start(context.Background(), scope, run, assistant, func(context.Context, *projectAssistantSnapshotAccumulator) {
					workerStarted <- struct{}{}
				})
			}()
			select {
			case <-claimStore.claimStarted:
			case <-time.After(time.Second):
				t.Fatal("run claim did not block after Attach published the owner")
			}

			stopped, found, err := supervisor.Stop(scope, run.ID, run.ThreadID)
			if err != nil || !found {
				t.Fatalf("Stop found=%v err=%v", found, err)
			}
			if stopped.Status != store.AssistantRunStatusInterrupted {
				t.Fatalf("Stop status=%q, want interrupted", stopped.Status)
			}
			claimStore.unblockClaim()
			select {
			case startErr := <-startResult:
				if claimErr == nil && !errors.Is(startErr, store.ErrAssistantRunConflict) {
					t.Fatalf("late successful claim Start error=%v, want conflict", startErr)
				}
				if claimErr != nil && !errors.Is(startErr, claimErr) {
					t.Fatalf("late failed claim Start error=%v, want %v", startErr, claimErr)
				}
			case <-time.After(time.Second):
				t.Fatal("Start did not finish after its blocked claim returned")
			}
			select {
			case <-workerStarted:
				t.Fatal("worker started after Stop detached its exact run owner")
			default:
			}
			persisted, err := inner.GetAssistantRun(context.Background(), scope, run.ID)
			if err != nil || persisted.Status != store.AssistantRunStatusInterrupted {
				t.Fatalf("persisted run=%#v err=%v, want interrupted", persisted, err)
			}
			waitForAssistantClaimCondition(t, "late run claim release", func() bool {
				_, held, claimErr := inner.GetReplicaClaim(context.Background(), store.AssistantRunClaimKey(scope, run.ThreadID, run.ID))
				return claimErr == nil && !held
			})
		})
	}
}

func TestProjectAssistantSupervisorFailedRunClaimReleaseKeepsSameRunGated(t *testing.T) {
	inner := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	runA, userA, assistantA := assistantRunForClaimRace(scope, "thread-a", "run-a")
	runB, userB, assistantB := assistantRunForClaimRace(scope, "thread-b", "run-b")
	for _, fixture := range []struct {
		user      store.Message
		assistant store.Message
		run       store.AssistantRun
	}{{userA, assistantA, runA}, {userB, assistantB, runB}} {
		if _, err := inner.CreateAssistantRun(context.Background(), scope, fixture.user, fixture.assistant, fixture.run); err != nil {
			t.Fatal(err)
		}
	}
	claimStore := &assistantRunClaimGateStore{
		Store: inner, runClaimKey: store.AssistantRunClaimKey(scope, runA.ThreadID, runA.ID),
		releaseStarted: make(chan struct{}), continueRelease: make(chan struct{}), releaseFailed: make(chan struct{}), failFirstRelease: true,
	}
	supervisor := newProjectAssistantSupervisor(context.Background(), claimStore)
	t.Cleanup(func() {
		claimStore.unblockRelease()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		supervisor.Shutdown(ctx)
	})
	accA, err := supervisor.Attach(scope, runA, assistantA)
	if err != nil {
		t.Fatal(err)
	}
	if !supervisor.Abort(scope, runA.ID, runA.ThreadID) {
		t.Fatal("Abort did not detach run-a")
	}
	select {
	case <-claimStore.releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("run claim release did not start")
	}
	claimStore.unblockRelease()
	select {
	case <-claimStore.releaseFailed:
	case <-time.After(time.Second):
		t.Fatal("first run claim release did not fail as arranged")
	}

	if _, err := supervisor.Attach(scope, runA, assistantA); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("same-run Attach during uncertain release error=%v, want conflict", err)
	}
	if _, err := supervisor.Attach(scope, runB, assistantB); err != nil {
		t.Fatalf("sibling thread Attach while run-a release is uncertain: %v", err)
	}
	if _, ok, err := inner.GetReplicaClaim(context.Background(), store.ActivityClaimKey(scope)); err != nil || !ok {
		t.Fatalf("project activity claim lost during uncertain release: held=%v err=%v", ok, err)
	}
	waitForAssistantClaimCondition(t, "failed release attempt to become retryable", func() bool {
		supervisor.mu.Lock()
		defer supervisor.mu.Unlock()
		return !accA.owner.runClaimLease.releaseStarted
	})

	supervisor.retryPendingRunClaimReleases()
	select {
	case <-accA.owner.runClaimLease.done:
	case <-time.After(time.Second):
		t.Fatal("retry did not finish the durable run claim release")
	}
	if _, err := supervisor.Attach(scope, runA, assistantA); err != nil {
		t.Fatalf("same-run Attach after confirmed release: %v", err)
	}
	if _, ok, err := inner.GetReplicaClaim(context.Background(), store.AssistantRunClaimKey(scope, runA.ThreadID, runA.ID)); err != nil || !ok {
		t.Fatalf("reattached run claim missing: held=%v err=%v", ok, err)
	}
}

func TestProjectAssistantSupervisorShutdownRetainsWorkspaceClaimUntilMutationReturns(t *testing.T) {
	inner := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(context.Background(), inner)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	releaseMutation, err := supervisor.ReserveMutation(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	supervisor.Shutdown(shutdownCtx)
	if _, held, err := inner.GetReplicaClaim(context.Background(), store.ActivityClaimKey(scope)); err != nil || !held {
		t.Fatalf("Shutdown released workspace while admitted mutation remained: held=%v err=%v", held, err)
	}

	releaseMutation()
	waitForAssistantClaimCondition(t, "workspace owner release after mutation returns", func() bool {
		_, held, err := inner.GetReplicaClaim(context.Background(), store.ActivityClaimKey(scope))
		return err == nil && !held
	})
	select {
	case <-supervisor.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("supervisor context was not canceled after owner drain")
	}
}

func TestProjectAssistantSupervisorStartWaitsForDurableStopTransition(t *testing.T) {
	inner := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	run, user, assistant := assistantRunForClaimRace(scope, "thread-a", "run-a")
	created, err := inner.CreateAssistantRun(context.Background(), scope, user, assistant, run)
	if err != nil {
		t.Fatal(err)
	}
	stopStore := &assistantRunStopGateStore{
		Store: inner, stopStarted: make(chan struct{}), continueStop: make(chan struct{}),
	}
	supervisor := newProjectAssistantSupervisor(context.Background(), stopStore)
	if _, err := supervisor.Attach(scope, created, assistant); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopStore.unblockStop()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		supervisor.Shutdown(ctx)
	})

	stopResult := make(chan error, 1)
	go func() {
		_, _, stopErr := supervisor.Stop(scope, created.ID, created.ThreadID)
		stopResult <- stopErr
	}()
	select {
	case <-stopStore.stopStarted:
	case <-time.After(time.Second):
		t.Fatal("Stop did not reach the blocked durable Stopping transition")
	}
	persisted, err := inner.GetAssistantRun(context.Background(), scope, created.ID)
	if err != nil || persisted.Status != store.AssistantRunStatusStopping {
		t.Fatalf("durable run status=%q err=%v, want stopping", persisted.Status, err)
	}

	workerStarted := make(chan struct{}, 1)
	startResult := make(chan error, 1)
	startAttempted := make(chan struct{})
	go func() {
		close(startAttempted)
		startResult <- supervisor.Start(context.Background(), scope, created, assistant, func(context.Context, *projectAssistantSnapshotAccumulator) {
			workerStarted <- struct{}{}
		})
	}()
	<-startAttempted
	select {
	case startErr := <-startResult:
		if startErr == nil {
			t.Fatal("Start returned success while Stop held the durable transition")
		}
		if !errors.Is(startErr, store.ErrAssistantRunConflict) {
			t.Fatalf("Start during durable stop error=%v, want conflict", startErr)
		}
	case <-workerStarted:
		t.Fatal("worker started while the durable run was already stopping")
	case <-time.After(50 * time.Millisecond):
	}

	stopStore.unblockStop()
	select {
	case err := <-stopResult:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish after the store gate opened")
	}
	select {
	case startErr := <-startResult:
		if !errors.Is(startErr, store.ErrAssistantRunConflict) {
			t.Fatalf("Start after Stop error=%v, want conflict", startErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not finish after Stop released the transition")
	}
	select {
	case <-workerStarted:
		t.Fatal("worker started after Stop completed")
	default:
	}
	persisted, err = inner.GetAssistantRun(context.Background(), scope, created.ID)
	if err != nil || persisted.Status != store.AssistantRunStatusInterrupted {
		t.Fatalf("final durable run status=%q err=%v, want interrupted", persisted.Status, err)
	}
}
