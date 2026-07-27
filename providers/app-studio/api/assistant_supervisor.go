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
	"fmt"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/faroshq/provider-app-studio/store"
)

const projectAssistantTextSnapshotInterval = 250 * time.Millisecond

// projectAssistantRunSnapshot is the complete durable view sent to a
// subscriber. A consumer replaces its current view; it never needs event
// replay to reconstruct assistant state.
type projectAssistantRunSnapshot struct {
	Run     store.AssistantRun `json:"run"`
	Message store.Message      `json:"message"`
}

type projectAssistantSupervisor struct {
	store        store.Store
	ctx          context.Context
	cancel       context.CancelFunc
	lifecycleLog func(string, store.Scope, store.AssistantRun)

	mu           sync.Mutex
	runs         map[projectAssistantRunKey]*projectAssistantSupervisedRun
	reservations map[projectAssistantRunKey]struct{}
}

type projectAssistantSupervisedRun struct {
	transitionMu     sync.Mutex
	scope            store.Scope
	run              store.AssistantRun
	message          store.Message
	committedRun     store.AssistantRun
	committedMessage store.Message
	cancel           context.CancelCauseFunc
	subscribers      map[uint64]chan projectAssistantRunSnapshot
	nextSubID        uint64
	lastText         time.Time
	textFlush        *time.Timer
	// beforeTextFlushPersist makes the timer/chunk ordering test deterministic.
	// Production leaves it nil.
	beforeTextFlushPersist func()
	workerStarted          bool
}

type projectAssistantSnapshotAccumulator struct {
	supervisor *projectAssistantSupervisor
	key        projectAssistantRunKey
	runID      string
}

// CommittedRun returns the exact durable revision most recently persisted for
// this accumulator. Lifecycle logs must use this rather than a stale starter
// copy of the run.
func (a *projectAssistantSnapshotAccumulator) CommittedRun() (store.AssistantRun, bool) {
	if a == nil || a.supervisor == nil {
		return store.AssistantRun{}, false
	}
	a.supervisor.mu.Lock()
	defer a.supervisor.mu.Unlock()
	active := a.supervisor.runs[a.key]
	if active == nil || active.run.ID != a.runID {
		return store.AssistantRun{}, false
	}
	return active.committedRun, true
}

// logProjectAssistantLifecycle deliberately records only durable routing and
// state fields. Never add prompts, assistant content, tool arguments, or
// credentials here.
func logProjectAssistantLifecycle(event string, scope store.Scope, run store.AssistantRun) {
	klog.Background().Info("app studio assistant lifecycle", "event", event,
		"org", scope.OrgUUID, "workspace", scope.WorkspaceUUID, "project", scope.ProjectName,
		"run", run.ID, "revision", run.Revision, "status", run.Status)
}

func newProjectAssistantSupervisor(parent context.Context, msgStore store.Store) *projectAssistantSupervisor {
	if parent == nil {
		parent = context.Background()
	}
	// Do not derive worker contexts directly from parent. On process shutdown
	// the signal context is cancelled before main can call Shutdown; deriving
	// from it lets workers record "aborted" before Shutdown can durably mark
	// the server-owned work "interrupted".
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := &projectAssistantSupervisor{
		store: msgStore, ctx: ctx, cancel: cancel, lifecycleLog: logProjectAssistantLifecycle, runs: map[projectAssistantRunKey]*projectAssistantSupervisedRun{}, reservations: map[projectAssistantRunKey]struct{}{},
	}
	go func() {
		select {
		case <-parent.Done():
			supervisor.Shutdown(context.Background())
		case <-ctx.Done():
		}
	}()
	return supervisor
}

func (s *projectAssistantSupervisor) log(event string, scope store.Scope, run store.AssistantRun) {
	if s != nil && s.lifecycleLog != nil {
		s.lifecycleLog(event, scope, run)
	}
}

// Reserve closes the narrow interval between atomically creating a durable run
// and attaching it to this process. Reconciliation must treat that interval as
// owned, otherwise a concurrent latest/stream request can incorrectly mark a
// freshly-created run interrupted.
func (s *projectAssistantSupervisor) Reserve(scope store.Scope) (func(), error) {
	if s == nil {
		return nil, errors.New("assistant supervisor not configured")
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName}
	if !key.valid() {
		return nil, errors.New("assistant supervisor scope is required")
	}
	s.mu.Lock()
	if s.runs[key] != nil {
		s.mu.Unlock()
		return nil, store.ErrAssistantRunConflict
	}
	if s.reservations == nil {
		s.reservations = map[projectAssistantRunKey]struct{}{}
	}
	if _, exists := s.reservations[key]; exists {
		s.mu.Unlock()
		return nil, store.ErrAssistantRunConflict
	}
	s.reservations[key] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.reservations, key)
			s.mu.Unlock()
		})
	}, nil
}

func (s *projectAssistantSupervisor) reserved(scope store.Scope) bool {
	if s == nil {
		return false
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName}
	s.mu.Lock()
	_, reserved := s.reservations[key]
	s.mu.Unlock()
	return reserved
}

func (s *projectAssistantSupervisor) Shutdown(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	type interruptedRun struct {
		accumulator *projectAssistantSnapshotAccumulator
		scope       store.Scope
		run         store.AssistantRun
	}
	accumulators := make([]interruptedRun, 0, len(s.runs))
	for key, active := range s.runs {
		if active.run.Status == store.AssistantRunStatusRunning {
			accumulators = append(accumulators, interruptedRun{accumulator: &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: active.run.ID}, scope: active.scope, run: active.run})
		}
	}
	s.mu.Unlock()
	for _, interrupted := range accumulators {
		if err := interrupted.accumulator.SetStatus(ctx, store.AssistantRunStatusInterrupted); err == nil {
			interrupted.run.Status = store.AssistantRunStatusInterrupted
			interrupted.run.Revision++
			s.log("interrupted", interrupted.scope, interrupted.run)
		}
	}
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *projectAssistantSupervisor) Attach(scope store.Scope, run store.AssistantRun, message store.Message) (*projectAssistantSnapshotAccumulator, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("assistant supervisor store not configured")
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName}
	if !key.valid() || run.ID == "" {
		return nil, errors.New("assistant supervisor scope and run id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.runs[key]; existing != nil {
		if existing.run.ID == run.ID {
			return &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: run.ID}, nil
		}
		return nil, store.ErrAssistantRunConflict
	}
	delete(s.reservations, key)
	_, cancel := context.WithCancelCause(s.ctx)
	s.runs[key] = &projectAssistantSupervisedRun{scope: scope, run: run, message: message, committedRun: run, committedMessage: message, cancel: cancel, subscribers: map[uint64]chan projectAssistantRunSnapshot{}}
	return &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: run.ID}, nil
}

func (s *projectAssistantSupervisor) accumulatorFor(scope store.Scope, runID string) *projectAssistantSnapshotAccumulator {
	if s == nil {
		return nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName}
	s.mu.Lock()
	defer s.mu.Unlock()
	if active := s.runs[key]; active != nil && active.run.ID == runID {
		return &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: runID}
	}
	return nil
}

func (s *projectAssistantSupervisor) accumulatorForActiveMessage(scope store.Scope, messageID string) *projectAssistantSnapshotAccumulator {
	if s == nil {
		return nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName}
	s.mu.Lock()
	defer s.mu.Unlock()
	if active := s.runs[key]; active != nil && active.message.ID == messageID {
		return &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: active.run.ID}
	}
	return nil
}

// Start deliberately ignores starterCtx. The worker is derived from the
// provider lifecycle so an HTTP disconnect can only detach a subscriber.
func (s *projectAssistantSupervisor) Start(_ context.Context, scope store.Scope, run store.AssistantRun, message store.Message, worker func(context.Context, *projectAssistantSnapshotAccumulator)) error {
	acc, err := s.Attach(scope, run, message)
	if err != nil {
		return err
	}
	s.mu.Lock()
	active := s.runs[acc.key]
	if active.workerStarted {
		s.mu.Unlock()
		return store.ErrAssistantRunConflict
	}
	active.workerStarted = true
	workerCtx, _ := context.WithCancelCause(s.ctx)
	// Use the active cancellation function created by Attach; the context must
	// share it, rather than derive from the initiating request.
	workerCtx, active.cancel = context.WithCancelCause(s.ctx)
	s.mu.Unlock()
	s.log("start", scope, run)
	go func() {
		defer s.finish(acc.key, run.ID)
		worker(workerCtx, acc)
	}()
	return nil
}

func (s *projectAssistantSupervisor) finish(key projectAssistantRunKey, runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active := s.runs[key]; active != nil && active.run.ID == runID {
		// Permission/input checkpoints deliberately retain the in-memory
		// snapshot, but no worker owns them once the Eino segment returns.
		active.workerStarted = false
		if assistantRunTerminal(active.run.Status) {
			delete(s.runs, key)
		}
	}
}

func assistantRunTerminal(status store.AssistantRunStatus) bool {
	switch status {
	case store.AssistantRunStatusCompleted, store.AssistantRunStatusAborted, store.AssistantRunStatusFailed, store.AssistantRunStatusInterrupted:
		return true
	}
	return false
}

func (s *projectAssistantSupervisor) Abort(scope store.Scope, runID string) bool {
	ok, _ := s.AbortWith(scope, runID, nil)
	return ok
}

// AbortWith applies the caller's synchronous terminal bookkeeping (audit and
// pending-action sanitization) inside the same serialized transition that
// persists the aborted snapshot.
func (s *projectAssistantSupervisor) AbortWith(scope store.Scope, runID string, mutate func(*store.AssistantRun, *store.Message) error) (bool, error) {
	if s == nil {
		return false, nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName}
	s.mu.Lock()
	active := s.runs[key]
	if active == nil || active.run.ID != runID {
		s.mu.Unlock()
		return false, nil
	}
	s.mu.Unlock()
	active.transitionMu.Lock()
	defer active.transitionMu.Unlock()
	s.mu.Lock()
	if current := s.runs[key]; current != active || active.run.ID != runID {
		s.mu.Unlock()
		return false, nil
	}
	// A worker may have finished between the initial lookup and transition
	// ownership. Terminal state is immutable: Abort never revives it.
	if assistantRunTerminal(active.run.Status) {
		s.mu.Unlock()
		return active.run.Status == store.AssistantRunStatusAborted, nil
	}
	wasPaused := active.run.Status == store.AssistantRunStatusPendingPermission || active.run.Status == store.AssistantRunStatusPendingInput
	if mutate != nil {
		if err := mutate(&active.run, &active.message); err != nil {
			s.mu.Unlock()
			return false, err
		}
	}
	active.run.Status = store.AssistantRunStatusAborted
	active.run.Revision++
	active.run.UpdatedAt = time.Now().UTC()
	active.message.UpdatedAt = active.run.UpdatedAt
	active.message.Metadata = projectAssistantDurableMetadataFromExisting(active.run, "Aborted", false, active.message.Metadata)
	run, message := active.run, active.message
	active.cancel(context.Canceled)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), projectMessagePersistTimeout)
	defer cancel()
	if err := s.store.SaveAssistantRunSnapshot(ctx, scope, run, []store.Message{message}, run.Revision-1); err != nil {
		return false, err
	}
	s.mu.Lock()
	if current := s.runs[key]; current != nil && current.run.ID == runID && current.run.Revision == run.Revision {
		current.committedRun, current.committedMessage = run, message
		for _, subscriber := range current.subscribers {
			s.sendCoalesced(subscriber, projectAssistantRunSnapshot{Run: run, Message: message})
		}
		if wasPaused && !current.workerStarted {
			delete(s.runs, key)
		}
	}
	s.mu.Unlock()
	s.log("abort", scope, run)
	return true, nil
}

func (s *projectAssistantSupervisor) Subscribe(scope store.Scope, runID string, afterRevision int64) (<-chan projectAssistantRunSnapshot, func(), error) {
	if s == nil {
		return nil, nil, errors.New("assistant supervisor not configured")
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName}
	s.mu.Lock()
	active := s.runs[key]
	if active == nil || active.run.ID != runID {
		s.mu.Unlock()
		return nil, nil, store.ErrAssistantRunNotFound
	}
	// A reconnect at the terminal cursor has no future revision to receive.
	// Return a closed (not nil) channel so the HTTP stream exits immediately;
	// a nil channel would disable its receive case and leak keepalives forever.
	if afterRevision >= active.committedRun.Revision && assistantRunTerminal(active.committedRun.Status) {
		closed := make(chan projectAssistantRunSnapshot)
		close(closed)
		s.mu.Unlock()
		return closed, func() {}, nil
	}
	id := active.nextSubID
	active.nextSubID++
	ch := make(chan projectAssistantRunSnapshot, 1)
	active.subscribers[id] = ch
	snapshot := projectAssistantRunSnapshot{Run: active.committedRun, Message: active.committedMessage}
	s.log("subscribe", scope, active.committedRun)
	s.sendCoalesced(ch, snapshot)
	s.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			if current := s.runs[key]; current != nil && current.run.ID == runID {
				delete(current.subscribers, id)
			}
			s.mu.Unlock()
		})
	}, nil
}

func (s *projectAssistantSupervisor) sendCoalesced(ch chan projectAssistantRunSnapshot, snapshot projectAssistantRunSnapshot) {
	select {
	case ch <- snapshot:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- snapshot:
	default:
	}
}

func (a *projectAssistantSnapshotAccumulator) UpdateText(ctx context.Context, content string, immediate bool) error {
	return a.update(ctx, func(active *projectAssistantSupervisedRun) { active.message.Content = content }, immediate)
}

func (a *projectAssistantSnapshotAccumulator) SetStatus(ctx context.Context, status store.AssistantRunStatus) error {
	return a.UpdateSnapshot(ctx, func(run *store.AssistantRun, message *store.Message) {
		run.Status = status
		next := *run
		next.Revision++
		provisional, _ := message.Metadata[projectAssistantMetadataProvisional].(bool)
		if assistantRunTerminal(status) {
			provisional = false
		}
		message.Metadata = projectAssistantDurableMetadataFromExisting(next, projectAssistantRunDisplayStatus(status, "Working"), provisional, message.Metadata)
	})
}

func (a *projectAssistantSnapshotAccumulator) SetMessageMetadata(ctx context.Context, metadata map[string]any) error {
	return a.update(ctx, func(active *projectAssistantSupervisedRun) { active.message.Metadata = metadata }, true)
}

// UpdateSnapshot keeps run state and its durable assistant-message metadata in
// one revisioned persistence transition. Callers that publish metadata derived
// from the run must use this rather than separate status and metadata updates.
func (a *projectAssistantSnapshotAccumulator) UpdateSnapshot(ctx context.Context, mutate func(*store.AssistantRun, *store.Message)) error {
	return a.update(ctx, func(active *projectAssistantSupervisedRun) { mutate(&active.run, &active.message) }, true)
}

func (a *projectAssistantSnapshotAccumulator) UpdateMessage(ctx context.Context, content string, metadata map[string]any) error {
	return a.update(ctx, func(active *projectAssistantSupervisedRun) {
		active.message.Content = content
		active.message.Metadata = metadata
	}, true)
}

func (a *projectAssistantSnapshotAccumulator) UpdateRun(ctx context.Context, mutate func(*store.AssistantRun)) error {
	return a.update(ctx, func(active *projectAssistantSupervisedRun) { mutate(&active.run) }, true)
}

// ClaimPending serializes the resume compare-and-swap with the rest of this
// run's durable transitions. ClaimAssistantRun alone changes status without a
// new snapshot revision, so publish a committed running revision only after
// the active assistant message and run have been saved together.
func (a *projectAssistantSnapshotAccumulator) ClaimPending(ctx context.Context, requestID string) (store.AssistantRun, error) {
	if a == nil || a.supervisor == nil {
		return store.AssistantRun{}, errors.New("assistant snapshot accumulator not configured")
	}
	s := a.supervisor
	s.mu.Lock()
	active := s.runs[a.key]
	if active == nil || active.run.ID != a.runID {
		s.mu.Unlock()
		return store.AssistantRun{}, store.ErrAssistantRunNotFound
	}
	active.transitionMu.Lock()
	s.mu.Unlock()
	defer active.transitionMu.Unlock()

	s.mu.Lock()
	if current := s.runs[a.key]; current != active || active.run.ID != a.runID {
		s.mu.Unlock()
		return store.AssistantRun{}, store.ErrAssistantRunNotFound
	}
	scope, runID := active.scope, active.run.ID
	s.mu.Unlock()
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), projectMessagePersistTimeout)
	claimed, err := s.store.ClaimAssistantRun(persistCtx, scope, runID, requestID, time.Now().UTC())
	if err != nil {
		cancel()
		return store.AssistantRun{}, err
	}

	s.mu.Lock()
	if current := s.runs[a.key]; current != active || active.run.ID != a.runID {
		s.mu.Unlock()
		cancel()
		return store.AssistantRun{}, store.ErrAssistantRunNotFound
	}
	// ClaimAssistantRun intentionally preserves its revision. The following
	// snapshot is the observable state transition from pending to running.
	claimed.Revision++
	claimed.UpdatedAt = time.Now().UTC()
	active.run = claimed
	active.message.UpdatedAt = claimed.UpdatedAt
	active.message.Metadata = projectAssistantDurableMetadataFromExisting(claimed, "Working", false, active.message.Metadata)
	delete(active.message.Metadata, projectMessageMetadataAssistantInterrupt)
	run, message := active.run, active.message
	s.mu.Unlock()
	err = s.store.SaveAssistantRunSnapshot(persistCtx, scope, run, []store.Message{message}, run.Revision-1)
	cancel()
	if err != nil {
		s.recordPersistenceFailure(a.key, a.runID, err)
		return store.AssistantRun{}, fmt.Errorf("persist claimed assistant snapshot: %w", err)
	}

	s.mu.Lock()
	if current := s.runs[a.key]; current != nil && current.run.ID == a.runID && current.run.Revision == run.Revision {
		current.committedRun, current.committedMessage = run, message
		for _, subscriber := range current.subscribers {
			s.sendCoalesced(subscriber, projectAssistantRunSnapshot{Run: run, Message: message})
		}
	}
	s.mu.Unlock()
	return run, nil
}

func (a *projectAssistantSnapshotAccumulator) update(ctx context.Context, mutate func(*projectAssistantSupervisedRun), immediate bool) error {
	if a == nil || a.supervisor == nil {
		return errors.New("assistant snapshot accumulator not configured")
	}
	s := a.supervisor
	s.mu.Lock()
	active := s.runs[a.key]
	if active == nil || active.run.ID != a.runID {
		s.mu.Unlock()
		return store.ErrAssistantRunNotFound
	}
	active.transitionMu.Lock()
	s.mu.Unlock()
	defer active.transitionMu.Unlock()
	s.mu.Lock()
	if current := s.runs[a.key]; current != active || active.run.ID != a.runID {
		s.mu.Unlock()
		return store.ErrAssistantRunNotFound
	}
	if assistantRunTerminal(active.run.Status) {
		s.mu.Unlock()
		return nil
	}
	if !immediate && !active.lastText.IsZero() && time.Since(active.lastText) < projectAssistantTextSnapshotInterval {
		mutate(active)
		if active.textFlush == nil {
			active.textFlush = time.AfterFunc(projectAssistantTextSnapshotInterval-time.Since(active.lastText), a.flushText)
		}
		s.mu.Unlock()
		return nil
	}
	if active.textFlush != nil {
		active.textFlush.Stop()
		active.textFlush = nil
	}
	mutate(active)
	active.run.Revision++
	active.run.UpdatedAt = time.Now().UTC()
	active.message.UpdatedAt = active.run.UpdatedAt
	if !immediate {
		active.lastText = active.run.UpdatedAt
	}
	run, message, scope := active.run, active.message, active.scope
	s.mu.Unlock()
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), projectMessagePersistTimeout)
	err := s.store.SaveAssistantRunSnapshot(persistCtx, scope, run, []store.Message{message}, run.Revision-1)
	cancel()
	if err != nil {
		s.recordPersistenceFailure(a.key, a.runID, err)
		return fmt.Errorf("persist assistant snapshot: %w", err)
	}
	s.mu.Lock()
	if current := s.runs[a.key]; current != nil && current.run.ID == a.runID && current.run.Revision == run.Revision {
		current.committedRun, current.committedMessage = run, message
		for _, subscriber := range current.subscribers {
			s.sendCoalesced(subscriber, projectAssistantRunSnapshot{Run: run, Message: message})
		}
	}
	s.mu.Unlock()
	return nil
}

func (a *projectAssistantSnapshotAccumulator) flushText() {
	if a == nil || a.supervisor == nil {
		return
	}
	a.supervisor.mu.Lock()
	active := a.supervisor.runs[a.key]
	if active == nil || active.run.ID != a.runID {
		a.supervisor.mu.Unlock()
		return
	}
	active.textFlush = nil
	beforePersist := active.beforeTextFlushPersist
	a.supervisor.mu.Unlock()
	if beforePersist != nil {
		beforePersist()
	}
	// Do not capture content before releasing the lock: a chunk can arrive
	// between this timer firing and the durable save. The transition below
	// snapshots whatever text is current when it takes transition ownership.
	_ = a.update(context.Background(), func(*projectAssistantSupervisedRun) {}, true)
}

// recordPersistenceFailure makes a best effort to leave an explicit terminal
// state. The failing save may be transient (for example a dropped database
// connection); a second detached save is therefore useful, but never permits
// orchestration to continue as though a snapshot had been durable.
func (s *projectAssistantSupervisor) recordPersistenceFailure(key projectAssistantRunKey, runID string, _ error) {
	s.mu.Lock()
	active := s.runs[key]
	if active == nil || active.run.ID != runID {
		s.mu.Unlock()
		return
	}
	active.run, active.message = active.committedRun, active.committedMessage
	active.run.Status = store.AssistantRunStatusFailed
	active.run.Revision++
	active.run.UpdatedAt = time.Now().UTC()
	active.message.UpdatedAt = active.run.UpdatedAt
	active.message.Metadata = projectAssistantDurableMetadataFromExisting(active.run, "Failed", false, active.message.Metadata)
	run, message, scope := active.run, active.message, active.scope
	active.cancel(errors.New("assistant snapshot persistence failed"))
	s.mu.Unlock()
	s.log("persistence_failure", scope, run)
	ctx, cancel := context.WithTimeout(context.Background(), projectMessagePersistTimeout)
	defer cancel()
	if s.store.SaveAssistantRunSnapshot(ctx, scope, run, []store.Message{message}, run.Revision-1) != nil {
		return
	}
	s.mu.Lock()
	if current := s.runs[key]; current != nil && current.run.ID == runID && current.run.Revision == run.Revision {
		current.committedRun, current.committedMessage = run, message
		for _, subscriber := range current.subscribers {
			s.sendCoalesced(subscriber, projectAssistantRunSnapshot{Run: run, Message: message})
		}
	}
	s.mu.Unlock()
}
