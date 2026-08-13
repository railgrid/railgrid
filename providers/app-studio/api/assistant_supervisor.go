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
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/faroshq/provider-app-studio/store"
)

const projectAssistantTextSnapshotInterval = 250 * time.Millisecond
const projectAssistantSteeringQueueCapacity = 64

const projectAssistantSteeringRequestMetadata = "assistantSteeringRequestID"
const projectAssistantSteeringRunMetadata = "assistantSteeringRunID"
const projectAssistantSteeringDigestMetadata = "assistantSteeringRequestDigest"

var errProjectAssistantUserStop = fmt.Errorf("assistant stopped by user: %w", context.Canceled)

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
	queuedContinuation     func(context.Context, *projectAssistantSnapshotAccumulator)
	steering               chan projectAssistantSteeringInput
	steeringReceipts       map[string]store.Message
	acceptingSteering      bool
}

type projectAssistantSnapshotAccumulator struct {
	supervisor *projectAssistantSupervisor
	key        projectAssistantRunKey
	runID      string
}

// FailPersistence terminalizes a supervised run when a durable side stream
// (for example the canonical conversation stream) cannot be appended. Snapshot
// writes already invoke the same path internally; exposing it here keeps every
// worker-owned persistence failure from leaving a running run without a worker.
func (a *projectAssistantSnapshotAccumulator) FailPersistence(cause error) {
	if a == nil || a.supervisor == nil || cause == nil {
		return
	}
	a.supervisor.recordPersistenceFailure(a.key, a.runID, cause)
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

func logProjectAssistantFailure(ctx context.Context, event string, scope store.Scope, run store.AssistantRun, cause error) {
	failure := projectAssistantAuditFailureForError(cause)
	if failure == nil {
		return
	}
	klog.FromContext(ctx).Info("app studio assistant failure",
		"event", event,
		"org", scope.OrgUUID,
		"workspace", scope.WorkspaceUUID,
		"project", scope.ProjectName,
		"run", run.ID,
		"revision", run.Revision,
		"status", run.Status,
		"failureKind", failure.Kind,
		"failureSummary", failure.Summary,
		"modelCalls", failure.Calls,
		"modelCallLimit", failure.Limit,
	)
}

func newProjectAssistantSupervisor(parent context.Context, msgStore store.Store) *projectAssistantSupervisor {
	if parent == nil {
		parent = context.Background()
	}
	// Do not derive worker contexts directly from parent. On process shutdown
	// the signal context is cancelled before main can call Shutdown; deriving
	// from it lets workers observe cancellation before Shutdown can durably mark
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
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
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
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
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
		if active.run.Status == store.AssistantRunStatusRunning || active.run.Status == store.AssistantRunStatusStopping {
			accumulators = append(accumulators, interruptedRun{accumulator: &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: active.run.ID}, scope: active.scope, run: active.run})
		}
	}
	s.mu.Unlock()
	for _, interrupted := range accumulators {
		_, err := s.AbortWith(interrupted.scope, interrupted.run.ID, func(run *store.AssistantRun, _ *store.Message) error {
			run.AbortReason = store.AssistantRunAbortReasonInterrupted
			return nil
		})
		if err == nil {
			interrupted.run.Status = store.AssistantRunStatusInterrupted
			interrupted.run.AbortReason = store.AssistantRunAbortReasonInterrupted
			interrupted.run.Revision++
			_ = appendProjectAssistantInterruptedBoundary(ctx, s.store, interrupted.scope, interrupted.run)
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
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	if !key.valid() || run.ID == "" {
		return nil, errors.New("assistant supervisor scope and run id are required")
	}
	run.ProjectName = scope.ProjectName
	message.ProjectName = scope.ProjectName
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
	s.runs[key] = &projectAssistantSupervisedRun{
		scope:            scope,
		run:              run,
		message:          message,
		committedRun:     run,
		committedMessage: message,
		cancel:           cancel,
		subscribers:      map[uint64]chan projectAssistantRunSnapshot{},
		steering:         make(chan projectAssistantSteeringInput, projectAssistantSteeringQueueCapacity),
		steeringReceipts: map[string]store.Message{},
	}
	return &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: run.ID}, nil
}

// Steering returns the run-scoped input queue owned by the supervisor. It is
// intentionally receive-only outside the supervisor: EnqueueSteering persists
// the user message and advances the durable run revision before delivery.
func (s *projectAssistantSupervisor) Steering(scope store.Scope, runID string) <-chan projectAssistantSteeringInput {
	if s == nil {
		return nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	s.mu.Lock()
	defer s.mu.Unlock()
	if active := s.runs[key]; active != nil && active.run.ID == runID {
		return active.steering
	}
	return nil
}

// SealSteering atomically closes the active turn's input boundary when its
// queue is empty. EnqueueSteering uses the same transition lock, so an input is
// either durably queued before this boundary or rejected for a later run.
func (s *projectAssistantSupervisor) SealSteering(scope store.Scope, runID string) bool {
	if s == nil {
		return true
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	s.mu.Lock()
	active := s.runs[key]
	s.mu.Unlock()
	if active == nil || active.run.ID != runID {
		return true
	}
	active.transitionMu.Lock()
	defer active.transitionMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.runs[key]; current != active || active.run.ID != runID {
		return true
	}
	if len(active.steering) > 0 {
		return false
	}
	active.acceptingSteering = false
	return true
}

// EnqueueSteering appends user input to the active durable run instead of
// preempting it or manufacturing a second run. The active Eino loop observes
// the message at its next model-safe boundary. Retry identity is run-scoped and
// idempotent while the run is attached to this process.
func (s *projectAssistantSupervisor) EnqueueSteering(
	ctx context.Context,
	scope store.Scope,
	expectedRunID string,
	actor string,
	content string,
	clientRequestID string,
	mode store.AssistantRunMode,
) (store.AssistantRun, store.Message, store.Message, bool, error) {
	if s == nil || s.store == nil {
		return store.AssistantRun{}, store.Message{}, store.Message{}, false, nil
	}
	expectedRunID = strings.TrimSpace(expectedRunID)
	if expectedRunID == "" {
		return store.AssistantRun{}, store.Message{}, store.Message{}, false, nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	s.mu.Lock()
	active := s.runs[key]
	if active == nil || active.run.ID != expectedRunID {
		s.mu.Unlock()
		return store.AssistantRun{}, store.Message{}, store.Message{}, false, nil
	}
	s.mu.Unlock()

	active.transitionMu.Lock()
	defer active.transitionMu.Unlock()
	s.mu.Lock()
	if current := s.runs[key]; current != active {
		s.mu.Unlock()
		return store.AssistantRun{}, store.Message{}, store.Message{}, false, store.ErrAssistantRunConflict
	}
	paused := active.run.Status == store.AssistantRunStatusPendingPermission || active.run.Status == store.AssistantRunStatusPendingInput
	if !paused && (active.run.Status != store.AssistantRunStatusRunning || !active.workerStarted) {
		s.mu.Unlock()
		return store.AssistantRun{}, store.Message{}, store.Message{}, false, nil
	}
	if active.run.Mode != mode {
		s.mu.Unlock()
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, fmt.Errorf("%w: active assistant collaboration mode is %q", store.ErrAssistantRunConflict, active.run.Mode)
	}
	if !projectAssistantRunActorMatches(active.run, actor) {
		s.mu.Unlock()
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, fmt.Errorf("%w: assistant steering actor does not own the active run", store.ErrAssistantRunConflict)
	}
	if receipt, ok := active.steeringReceipts[clientRequestID]; ok {
		if receipt.ActorID != actor || receipt.Content != content {
			s.mu.Unlock()
			return store.AssistantRun{}, store.Message{}, store.Message{}, true, fmt.Errorf("%w: steering request %q does not match its durable input", store.ErrAssistantRunConflict, clientRequestID)
		}
		run, assistant := active.run, active.message
		s.mu.Unlock()
		return run, receipt, assistant, true, nil
	}
	run, assistant := active.run, active.message
	s.mu.Unlock()
	durableReceipt, found, receiptErr := findProjectAssistantSteeringReceipt(ctx, s.store, scope, run, actor, content, clientRequestID)
	s.mu.Lock()
	if current := s.runs[key]; current != active || active.run.ID != expectedRunID {
		s.mu.Unlock()
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, store.ErrAssistantRunConflict
	}
	if receiptErr != nil {
		s.mu.Unlock()
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, receiptErr
	}
	if found {
		active.steeringReceipts[clientRequestID] = durableReceipt
		run, assistant = active.run, active.message
		s.mu.Unlock()
		return run, durableReceipt, assistant, true, nil
	}
	if !active.acceptingSteering && !paused {
		s.mu.Unlock()
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, store.ErrAssistantRunConflict
	}
	if len(active.steering) >= cap(active.steering) {
		s.mu.Unlock()
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, fmt.Errorf("%w: active assistant steering queue is full", store.ErrAssistantRunConflict)
	}

	now := time.Now().UTC()
	if !now.After(active.message.CreatedAt) {
		now = active.message.CreatedAt.Add(time.Microsecond)
	}
	user := store.Message{
		ID:      newMessageID(),
		Role:    "user",
		ActorID: actor,
		Content: content,
		Metadata: map[string]any{
			projectAssistantSteeringRequestMetadata: clientRequestID,
			projectAssistantSteeringRunMetadata:     expectedRunID,
			projectAssistantSteeringDigestMetadata:  projectAssistantStartRequestDigest(actor, content, mode),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	run = active.run
	assistant = active.message
	s.mu.Unlock()

	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), projectMessagePersistTimeout)
	defer cancel()
	if err := s.store.AppendMessage(persistCtx, scope, user); err != nil {
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, fmt.Errorf("persist assistant steering input: %w", err)
	}
	if err := appendProjectAssistantConversationMessage(persistCtx, s.store, scope, run.ID, "steering-"+clientRequestID, projectAssistantConversationSteering, chatMessage{Role: "user", Content: content}); err != nil {
		persistErr := fmt.Errorf("persist assistant steering conversation item: %w", err)
		s.recordPersistenceFailure(key, run.ID, persistErr)
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, persistErr
	}

	s.mu.Lock()
	if current := s.runs[key]; current != active {
		s.mu.Unlock()
		return store.AssistantRun{}, store.Message{}, store.Message{}, true, store.ErrAssistantRunConflict
	}
	active.steeringReceipts[clientRequestID] = user
	active.steering <- projectAssistantSteeringInput{MessageID: user.ID, ClientRequestID: clientRequestID, Content: content}
	s.mu.Unlock()
	return run, user, assistant, true, nil
}

// ActivateSteering rotates the public assistant segment at the exact
// model-safe boundary that consumes the queued user inputs. EnqueueSteering
// persists durable receipts without changing the output target, so callbacks
// from an in-flight sample or tool call cannot land in the post-steering
// assistant segment.
func (s *projectAssistantSupervisor) ActivateSteering(
	ctx context.Context,
	scope store.Scope,
	runID string,
	inputs []projectAssistantSteeringInput,
) error {
	if s == nil || len(inputs) == 0 {
		return nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	s.mu.Lock()
	active := s.runs[key]
	s.mu.Unlock()
	if active == nil || active.run.ID != runID {
		return store.ErrAssistantRunNotFound
	}
	active.transitionMu.Lock()
	defer active.transitionMu.Unlock()

	s.mu.Lock()
	if current := s.runs[key]; current != active || active.run.ID != runID {
		s.mu.Unlock()
		return store.ErrAssistantRunNotFound
	}
	if assistantRunTerminal(active.run.Status) || active.run.Status == store.AssistantRunStatusStopping {
		s.mu.Unlock()
		return store.ErrAssistantRunConflict
	}
	if active.textFlush != nil {
		active.textFlush.Stop()
		active.textFlush = nil
	}
	now := time.Now().UTC()
	if !now.After(active.message.UpdatedAt) {
		now = active.message.UpdatedAt.Add(time.Microsecond)
	}
	oldAssistant := active.message
	run := active.run
	run.Revision++
	run.UpdatedAt = now
	oldAssistant.UpdatedAt = now
	oldAssistant.Metadata = projectAssistantDurableMetadataFromExisting(run, "", false, oldAssistant.Metadata)
	assistantAt := now.Add(time.Microsecond)
	assistant := store.Message{
		ID:        newMessageID(),
		Role:      "assistant",
		CreatedAt: assistantAt,
		UpdatedAt: assistantAt,
		Metadata:  projectAssistantDurableMetadataForTransition(run, "Working", true, false, nil, nil),
	}
	run.ActiveMessageID = assistant.ID
	s.mu.Unlock()

	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), projectMessagePersistTimeout)
	err := s.store.SaveAssistantRunSnapshot(persistCtx, scope, run, []store.Message{oldAssistant, assistant}, run.Revision-1)
	cancel()
	if err != nil {
		s.recordPersistenceFailure(key, runID, err)
		return fmt.Errorf("activate assistant steering boundary: %w", err)
	}
	if strings.TrimSpace(oldAssistant.Content) != "" {
		persistCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), projectMessagePersistTimeout)
		err = appendProjectAssistantConversationMessage(persistCtx, s.store, scope, run.ID,
			"assistant-"+oldAssistant.ID, projectAssistantConversationAssistant,
			chatMessage{Role: "assistant", Content: oldAssistant.Content})
		cancel()
		if err != nil {
			s.mu.Lock()
			if current := s.runs[key]; current == active {
				active.run, active.message = run, assistant
				active.committedRun, active.committedMessage = run, assistant
			}
			s.mu.Unlock()
			s.recordPersistenceFailure(key, runID, err)
			return fmt.Errorf("persist pre-steering assistant response item: %w", err)
		}
	}

	s.mu.Lock()
	if current := s.runs[key]; current != active {
		s.mu.Unlock()
		return store.ErrAssistantRunConflict
	}
	active.run, active.message = run, assistant
	active.committedRun, active.committedMessage = run, assistant
	for _, subscriber := range active.subscribers {
		s.sendCoalesced(subscriber, projectAssistantRunSnapshot{Run: run, Message: assistant})
	}
	s.mu.Unlock()
	return nil
}

func (a *projectAssistantSnapshotAccumulator) ActiveMessageID() string {
	if a == nil || a.supervisor == nil {
		return ""
	}
	a.supervisor.mu.Lock()
	defer a.supervisor.mu.Unlock()
	if active := a.supervisor.runs[a.key]; active != nil && active.run.ID == a.runID {
		return active.message.ID
	}
	return ""
}

func (s *projectAssistantSupervisor) accumulatorFor(scope store.Scope, runID string) *projectAssistantSnapshotAccumulator {
	if s == nil {
		return nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
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
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	s.mu.Lock()
	defer s.mu.Unlock()
	if active := s.runs[key]; active != nil && active.message.ID == messageID {
		return &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: active.run.ID}
	}
	return nil
}

// BindStopRequest durably reserves the retry identity for a supervised Stop.
// It shares the lifecycle transition lock so concurrent callers cannot replace
// one another's receipt before Stop changes the run status.
func (s *projectAssistantSupervisor) BindStopRequest(ctx context.Context, scope store.Scope, runID, actor, clientRequestID string) (bool, error) {
	if s == nil {
		return false, nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
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
	run, message := active.run, active.message
	if err := bindProjectAssistantStopRequest(&run, actor, clientRequestID); err != nil {
		s.mu.Unlock()
		return true, err
	}
	if bytes.Equal(run.Audit, active.run.Audit) {
		s.mu.Unlock()
		return true, nil
	}
	run.Revision++
	run.UpdatedAt = time.Now().UTC()
	message.UpdatedAt = run.UpdatedAt
	s.mu.Unlock()

	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), projectMessagePersistTimeout)
	err := s.store.SaveAssistantRunSnapshot(persistCtx, scope, run, []store.Message{message}, run.Revision-1)
	cancel()
	if err != nil {
		s.recordPersistenceFailure(key, runID, err)
		return true, fmt.Errorf("persist assistant stop receipt: %w", err)
	}
	s.mu.Lock()
	if current := s.runs[key]; current == active && active.run.ID == runID {
		active.run, active.message = run, message
		active.committedRun, active.committedMessage = run, message
		for _, subscriber := range active.subscribers {
			s.sendCoalesced(subscriber, projectAssistantRunSnapshot{Run: run, Message: message})
		}
	}
	s.mu.Unlock()
	return true, nil
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
		// A permission/input snapshot becomes externally actionable as soon as
		// it is durable, just before the segment goroutine runs its deferred
		// finish. An approval arriving in that narrow handoff window must not be
		// rejected as a duplicate worker: reserve exactly one continuation and
		// let finish transfer ownership without overlapping the Eino segments.
		if active.queuedContinuation == nil &&
			(active.run.Status == store.AssistantRunStatusPendingPermission || active.run.Status == store.AssistantRunStatusPendingInput) {
			active.queuedContinuation = worker
			queuedRun := active.run
			s.mu.Unlock()
			s.log("resume_queued", scope, queuedRun)
			return nil
		}
		s.mu.Unlock()
		return store.ErrAssistantRunConflict
	}
	active.workerStarted = true
	active.acceptingSteering = true
	// Use the active cancellation function created by Attach; the context must
	// share it, rather than derive from the initiating request.
	workerCtx, cancel := context.WithCancelCause(s.ctx)
	active.cancel = cancel
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
	active := s.runs[key]
	if active == nil || active.run.ID != runID {
		s.mu.Unlock()
		return
	}
	if continuation := active.queuedContinuation; continuation != nil &&
		(active.run.Status == store.AssistantRunStatusPendingPermission || active.run.Status == store.AssistantRunStatusPendingInput) {
		active.queuedContinuation = nil
		active.acceptingSteering = true
		workerCtx, cancel := context.WithCancelCause(s.ctx)
		active.cancel = cancel
		acc := &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: runID}
		run := active.run
		scope := active.scope
		s.mu.Unlock()
		s.log("resume_start", scope, run)
		go func() {
			defer s.finish(key, runID)
			continuation(workerCtx, acc)
		}()
		return
	}
	// Permission/input checkpoints deliberately retain the in-memory
	// snapshot, but no worker owns them once the Eino segment returns.
	active.queuedContinuation = nil
	active.workerStarted = false
	active.acceptingSteering = false
	if assistantRunTerminal(active.run.Status) {
		s.removeRunLocked(key, active)
	}
	s.mu.Unlock()
}

// removeRunLocked releases a supervised owner and closes every subscriber
// exactly once. The caller must hold s.mu. expected, when non-nil, prevents a
// late finish or persistence callback from removing a replacement owner that
// reused the same project key.
func (s *projectAssistantSupervisor) removeRunLocked(key projectAssistantRunKey, expected *projectAssistantSupervisedRun) {
	active := s.runs[key]
	if active == nil || (expected != nil && active != expected) {
		return
	}
	delete(s.runs, key)
	for _, subscriber := range active.subscribers {
		close(subscriber)
	}
	active.subscribers = nil
}

func assistantRunTerminal(status store.AssistantRunStatus) bool {
	switch status {
	case store.AssistantRunStatusCompleted, store.AssistantRunStatusFailed,
		store.AssistantRunStatusInterrupted, store.AssistantRunStatusAborted:
		return true
	}
	return false
}

func (s *projectAssistantSupervisor) Abort(scope store.Scope, runID string) bool {
	ok, _ := s.AbortWith(scope, runID, nil)
	return ok
}

// Stop makes cancellation observable before asking Eino to unwind. Pending
// runs have no active loop, so they use the existing synchronous terminal path.
func (s *projectAssistantSupervisor) Stop(scope store.Scope, runID string) (store.AssistantRun, bool, error) {
	if s == nil {
		return store.AssistantRun{}, false, nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	s.mu.Lock()
	active := s.runs[key]
	if active == nil || active.run.ID != runID {
		s.mu.Unlock()
		return store.AssistantRun{}, false, nil
	}
	if assistantRunTerminal(active.run.Status) || active.run.Status == store.AssistantRunStatusStopping {
		run := active.run
		s.mu.Unlock()
		return run, true, nil
	}
	if active.run.Status == store.AssistantRunStatusPendingPermission || active.run.Status == store.AssistantRunStatusPendingInput {
		s.mu.Unlock()
		ok, err := s.AbortWith(scope, runID, nil)
		if !ok || err != nil {
			return store.AssistantRun{}, ok, err
		}
		run, getErr := s.store.GetAssistantRun(context.Background(), scope, runID)
		return run, true, getErr
	}
	s.mu.Unlock()
	active.transitionMu.Lock()
	defer active.transitionMu.Unlock()

	s.mu.Lock()
	if current := s.runs[key]; current != active || active.run.ID != runID {
		s.mu.Unlock()
		return store.AssistantRun{}, false, store.ErrAssistantRunNotFound
	}
	if assistantRunTerminal(active.run.Status) || active.run.Status == store.AssistantRunStatusStopping {
		run := active.run
		s.mu.Unlock()
		return run, true, nil
	}
	if active.run.Status != store.AssistantRunStatusRunning {
		s.mu.Unlock()
		return store.AssistantRun{}, false, store.ErrAssistantRunConflict
	}
	currentRun := active.run
	s.mu.Unlock()

	now := time.Now().UTC()
	s.mu.Lock()
	if current := s.runs[key]; current != active || active.run.ID != runID {
		s.mu.Unlock()
		return store.AssistantRun{}, false, store.ErrAssistantRunNotFound
	}
	stoppingCandidate := currentRun
	stoppingCandidate.Status = store.AssistantRunStatusStopping
	stoppingCandidate.Revision++
	stoppingCandidate.UpdatedAt = now
	message := active.message
	message.UpdatedAt = now
	provisional, _ := message.Metadata[projectAssistantMetadataProvisional].(bool)
	message.Metadata = projectAssistantDurableMetadataFromExisting(stoppingCandidate, projectAssistantRunDisplayStatus(store.AssistantRunStatusStopping, "Working"), provisional, message.Metadata)
	s.mu.Unlock()
	persistCtx, cancelPersist := context.WithTimeout(context.Background(), projectMessagePersistTimeout)
	stoppingRun, err := s.store.RequestAssistantRunStopWithAssistantMessage(
		persistCtx, scope, runID, currentRun.Revision, message, now,
	)
	cancelPersist()
	if err != nil {
		return store.AssistantRun{}, false, err
	}

	s.mu.Lock()
	if current := s.runs[key]; current != active || active.run.ID != runID {
		s.mu.Unlock()
		return store.AssistantRun{}, false, store.ErrAssistantRunNotFound
	}
	active.run = stoppingRun
	active.message = message
	active.committedRun, active.committedMessage = stoppingRun, message
	active.cancel(errProjectAssistantUserStop)
	for _, subscriber := range active.subscribers {
		s.sendCoalesced(subscriber, projectAssistantRunSnapshot{Run: stoppingRun, Message: message})
	}
	s.mu.Unlock()
	s.log("stopping", scope, stoppingRun)
	return stoppingRun, true, nil
}

// AdmitMutation serializes the final durable authorization check with Stop.
// Releasing transitionMu is the admission point of no return: a call admitted
// before Stop may execute, while Stop closes the durable run before any later
// caller can pass this check.
func (s *projectAssistantSupervisor) AdmitMutation(ctx context.Context, scope store.Scope, runID, actor string) error {
	if s == nil || s.store == nil {
		return store.ErrAssistantRunConflict
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	s.mu.Lock()
	active := s.runs[key]
	actor = strings.TrimSpace(actor)
	if actor == "" || active == nil || active.run.ID != runID || !projectAssistantRunActorMatches(active.run, actor) {
		s.mu.Unlock()
		return store.ErrAssistantRunConflict
	}
	s.mu.Unlock()
	active.transitionMu.Lock()
	defer active.transitionMu.Unlock()

	s.mu.Lock()
	if current := s.runs[key]; current != active || active.run.ID != runID || active.run.Status != store.AssistantRunStatusRunning || !projectAssistantRunActorMatches(active.run, actor) {
		s.mu.Unlock()
		return store.ErrAssistantRunConflict
	}
	s.mu.Unlock()
	run, err := s.store.GetAssistantRun(ctx, scope, runID)
	if err != nil {
		return err
	}
	if run.Status != store.AssistantRunStatusRunning || !projectAssistantRunActorMatches(run, actor) {
		return store.ErrAssistantRunConflict
	}
	return nil
}

// AbortWith applies the caller's synchronous terminal bookkeeping (audit and
// pending-action sanitization) inside the same serialized transition that
// persists the interrupted snapshot. Its name is retained for API stability.
func (s *projectAssistantSupervisor) AbortWith(scope store.Scope, runID string, mutate func(*store.AssistantRun, *store.Message) error) (bool, error) {
	if s == nil {
		return false, nil
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
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
		return active.run.Status == store.AssistantRunStatusInterrupted || active.run.Status == store.AssistantRunStatusAborted, nil
	}
	wasPaused := active.run.Status == store.AssistantRunStatusPendingPermission || active.run.Status == store.AssistantRunStatusPendingInput
	if mutate != nil {
		if err := mutate(&active.run, &active.message); err != nil {
			s.mu.Unlock()
			return false, err
		}
	}
	active.run.Status = store.AssistantRunStatusInterrupted
	if active.run.AbortReason == "" {
		active.run.AbortReason = store.AssistantRunAbortReasonInterrupted
	}
	active.run.Revision++
	active.run.UpdatedAt = time.Now().UTC()
	active.message.UpdatedAt = active.run.UpdatedAt
	active.message.Metadata = projectAssistantDurableMetadataFromExisting(active.run, "Interrupted", false, active.message.Metadata)
	run, message := active.run, active.message
	active.cancel(context.Canceled)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), projectMessagePersistTimeout)
	defer cancel()
	if err := s.store.SaveAssistantRunSnapshot(ctx, scope, run, []store.Message{message}, run.Revision-1); err != nil {
		return false, err
	}
	// Persist the model-visible boundary before notifying the thread mirror of
	// the interrupted terminal state. This mirrors Codex's rollout flush before
	// publishing TurnAborted and prevents a continuation from racing ahead of
	// the interruption marker.
	if err := appendProjectAssistantInterruptedBoundary(ctx, s.store, scope, run); err != nil {
		s.recordPersistenceFailure(key, runID, err)
	}
	s.mu.Lock()
	if current := s.runs[key]; current != nil && current.run.ID == runID && current.run.Revision == run.Revision {
		current.committedRun, current.committedMessage = run, message
		for _, subscriber := range current.subscribers {
			s.sendCoalesced(subscriber, projectAssistantRunSnapshot{Run: run, Message: message})
		}
		if wasPaused && !current.workerStarted {
			s.removeRunLocked(key, current)
		}
	}
	s.mu.Unlock()
	s.log("interrupted", scope, run)
	return true, nil
}

func (s *projectAssistantSupervisor) Subscribe(scope store.Scope, runID string, afterRevision int64) (<-chan projectAssistantRunSnapshot, func(), error) {
	if s == nil {
		return nil, nil, errors.New("assistant supervisor not configured")
	}
	key := projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
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
	owner := active
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
			if current := s.runs[key]; current == owner && current.run.ID == runID {
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
		if assistantRunTerminal(status) {
			// A terminal snapshot is no longer resumable. Clear the permission or
			// input checkpoint in the same revisioned run/message transition so a
			// completed resumed segment cannot retain stale pre-terminal evidence.
			run.Checkpoint = nil
		}
		next := *run
		next.Revision++
		provisional, _ := message.Metadata[projectAssistantMetadataProvisional].(bool)
		if assistantRunTerminal(status) {
			provisional = false
		}
		message.Metadata = projectAssistantDurableMetadataFromExisting(next, projectAssistantRunDisplayStatus(status, "Working"), provisional, message.Metadata)
	})
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
	s.mu.Unlock()
	active.transitionMu.Lock()
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
	s.mu.Unlock()
	active.transitionMu.Lock()
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
	// Stop owns the transition from stopping to a terminal state. Generic
	// snapshots (including late permission/input interrupts) must not revive
	// the run after its durable stop request has been accepted. Treat them as
	// successful no-ops so worker unwinding can still perform the terminal
	// transition.
	if active.run.Status == store.AssistantRunStatusStopping {
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
	if err != nil {
		cancel()
		s.recordPersistenceFailure(a.key, a.runID, err)
		return fmt.Errorf("persist assistant snapshot: %w", err)
	}
	// Any supervised path that terminalizes through the accumulator (including
	// cancellation while resuming an approval/input checkpoint) must publish
	// the same model-visible interruption boundary before subscribers observe
	// the interrupted snapshot. The explicit Stop path does this in AbortWith;
	// this covers the other durable status transition path.
	if run.Status == store.AssistantRunStatusInterrupted {
		if err := appendProjectAssistantInterruptedBoundary(persistCtx, s.store, scope, run); err != nil {
			cancel()
			s.recordPersistenceFailure(a.key, a.runID, err)
			return fmt.Errorf("persist interrupted assistant boundary: %w", err)
		}
	}
	cancel()
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
func (s *projectAssistantSupervisor) recordPersistenceFailure(key projectAssistantRunKey, runID string, cause error) {
	s.mu.Lock()
	active := s.runs[key]
	if active == nil || active.run.ID != runID {
		s.mu.Unlock()
		return
	}
	// A persistence failure is terminal for this in-memory owner. A second
	// callback can race the first one while the detached fallback save is in
	// flight; do not manufacture another revision or overwrite a successfully
	// recorded terminal state.
	if assistantRunTerminal(active.run.Status) {
		s.mu.Unlock()
		return
	}
	active.run, active.message = active.committedRun, active.committedMessage
	active.run.Status = store.AssistantRunStatusFailed
	active.run.Error = projectAssistantRunErrorJSON(cause, "internal_server_error")
	active.run.Revision++
	active.run.UpdatedAt = time.Now().UTC()
	active.message.UpdatedAt = active.run.UpdatedAt
	active.message.Metadata = projectAssistantDurableMetadataFromExisting(active.run, "Failed", false, active.message.Metadata)
	run, message, scope := active.run, active.message, active.scope
	active.cancel(errors.New("assistant snapshot persistence failed"))
	s.mu.Unlock()
	s.log("persistence_failure", scope, run)
	logProjectAssistantFailure(context.Background(), "persistence_failure", scope, run, cause)
	ctx, cancel := context.WithTimeout(context.Background(), projectMessagePersistTimeout)
	defer cancel()
	if s.store.SaveAssistantRunSnapshot(ctx, scope, run, []store.Message{message}, run.Revision-1) != nil {
		// The durable row is still running because both the originating snapshot
		// and the detached terminal fallback failed. The worker has already been
		// canceled above, so it no longer owns this run. Remove this process-local
		// owner to let the next authenticated read/action reconcile the stale row.
		// Match the original object and revision: a replacement owner must never
		// be stolen by a late persistence failure from the old worker.
		s.mu.Lock()
		if current := s.runs[key]; current == active && current.run.ID == runID && current.run.Revision == run.Revision {
			s.removeRunLocked(key, active)
		}
		s.mu.Unlock()
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
