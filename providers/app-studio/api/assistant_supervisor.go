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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/railgrid/provider-app-studio/store"
)

const projectAssistantTextSnapshotInterval = 250 * time.Millisecond
const projectAssistantSteeringQueueCapacity = 64

// Activity-claim timings: the durable, fleet-wide twin of the local
// runs/reservations maps (store.ReplicaClaimKindActivity). A claim not
// renewed within the TTL is dead — its replica crashed — and only then may
// the orphan reconciler interrupt the run or a peer take the project over.
const (
	assistantActivityClaimTTL           = 60 * time.Second
	assistantActivityClaimRenewInterval = 15 * time.Second
)

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
	server       *Server
	ctx          context.Context
	cancel       context.CancelFunc
	lifecycleLog func(string, store.Scope, store.AssistantRun)

	// replicaID identifies this process in durable activity claims;
	// replicaAddr (podIP:internalPort, may be empty) lets peers forward to
	// it. Set via SetReplicaIdentity before serving; the default identity is
	// process-unique so claims from a crashed predecessor age out.
	replicaID   string
	replicaAddr string

	mu             sync.Mutex
	runs           map[projectAssistantRunKey]*projectAssistantSupervisedRun
	reservations   map[projectAssistantRunKey]store.Scope
	activityOwners map[projectAssistantRunKey]*projectAssistantActivityOwner
}

type projectAssistantActivityOwner struct {
	scope       store.Scope
	refs        int
	exclusive   bool
	releasing   bool
	ready       chan struct{}
	releaseDone chan struct{}
	err         error
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
	releaseActivityOwner   func()
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
		store: msgStore, ctx: ctx, cancel: cancel, lifecycleLog: logProjectAssistantLifecycle,
		runs: map[projectAssistantRunKey]*projectAssistantSupervisedRun{}, reservations: map[projectAssistantRunKey]store.Scope{},
		activityOwners: map[projectAssistantRunKey]*projectAssistantActivityOwner{},
		replicaID:      defaultReplicaIdentity(),
	}
	go func() {
		select {
		case <-parent.Done():
			supervisor.Shutdown(context.Background())
		case <-ctx.Done():
		}
	}()
	go supervisor.renewActivityClaims()
	return supervisor
}

// defaultReplicaIdentity is process-unique (hostname + pid) so a crashed
// predecessor's claims age out instead of being mistaken for our own.
func defaultReplicaIdentity() string {
	host, err := os.Hostname()
	if err != nil {
		host = "replica"
	}
	return fmt.Sprintf("%s_%d", host, os.Getpid())
}

// SetReplicaIdentity overrides the claim identity and records the address
// peers can forward to. Call before serving.
func (s *projectAssistantSupervisor) SetReplicaIdentity(replicaID, replicaAddr string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if replicaID != "" {
		s.replicaID = replicaID
	}
	s.replicaAddr = replicaAddr
}

// claimActivity records this replica as the owner of the project's assistant
// activity. A fresh foreign claim means another replica is mid-run or
// mid-operation → ErrAssistantRunConflict, same signal the local maps give.
// Store failures also conflict: exclusivity cannot be verified, so fail
// closed rather than risk two writers.
func (s *projectAssistantSupervisor) claimActivity(scope store.Scope, detail string) error {
	if s == nil || s.store == nil {
		return errors.New("assistant supervisor store not configured")
	}
	s.mu.Lock()
	replicaID, replicaAddr := s.replicaID, s.replicaAddr
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, held, err := s.store.TryClaimReplica(ctx, store.ReplicaClaim{
		Key:          store.ActivityClaimKey(scope),
		Kind:         store.ReplicaClaimKindActivity,
		ScopeKey:     store.ReplicaClaimScopeKey(scope),
		OwnerReplica: replicaID,
		OwnerAddr:    replicaAddr,
		Detail:       detail,
	}, assistantActivityClaimTTL)
	if err != nil {
		return fmt.Errorf("assistant activity claim: %w", err)
	}
	if !held {
		return store.ErrAssistantRunConflict
	}
	return nil
}

// releaseActivity drops the durable claim (owner-checked in the store).
func (s *projectAssistantSupervisor) releaseActivity(scope store.Scope) {
	if s == nil || s.store == nil {
		return
	}
	s.mu.Lock()
	replicaID := s.replicaID
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.ReleaseReplicaClaim(ctx, store.ActivityClaimKey(scope), replicaID); err != nil {
		klog.Background().Error(err, "releasing assistant activity claim",
			"org", scope.OrgUUID, "workspace", scope.WorkspaceUUID, "project", scope.ProjectName)
	}
}

func projectAssistantWorkspaceKey(scope store.Scope) projectAssistantRunKey {
	return projectAssistantRunKey{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
}

func projectAssistantThreadKey(scope store.Scope, threadID string) projectAssistantRunKey {
	key := projectAssistantWorkspaceKey(scope)
	key.ThreadID = strings.TrimSpace(threadID)
	return key
}

func (s *projectAssistantSupervisor) runKey(scope store.Scope, runID string, threadIDs ...string) projectAssistantRunKey {
	if len(threadIDs) > 0 {
		return projectAssistantThreadKey(scope, threadIDs[0])
	}
	workspaceKey := projectAssistantWorkspaceKey(scope)
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, active := range s.runs {
		if key.OrgUUID == workspaceKey.OrgUUID && key.WorkspaceUUID == workspaceKey.WorkspaceUUID &&
			key.ProjectName == workspaceKey.ProjectName && key.ProjectUID == workspaceKey.ProjectUID && active.run.ID == runID {
			return key
		}
	}
	return workspaceKey
}

// acquireActivityOwner keeps one durable project claim shared by concurrent
// thread runs and short source mutations. Exclusive workspace operations use
// the same owner and are admitted only when no other reference exists.
func (s *projectAssistantSupervisor) acquireActivityOwner(ctx context.Context, scope store.Scope, exclusive bool) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key := projectAssistantWorkspaceKey(scope)
	if !key.valid() || s == nil || s.store == nil {
		return nil, errors.New("assistant workspace owner scope and store are required")
	}
	for {
		s.mu.Lock()
		if owner := s.activityOwners[key]; owner != nil {
			if owner.releasing {
				released := owner.releaseDone
				s.mu.Unlock()
				select {
				case <-released:
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if exclusive || owner.exclusive {
				s.mu.Unlock()
				return nil, store.ErrAssistantRunConflict
			}
			owner.refs++
			ready := owner.ready
			s.mu.Unlock()
			select {
			case <-ready:
				if owner.err != nil {
					s.releaseActivityOwnerRef(key, owner)
					return nil, owner.err
				}
			case <-ctx.Done():
				s.releaseActivityOwnerRef(key, owner)
				return nil, ctx.Err()
			}
			return s.activityOwnerRelease(key, owner), nil
		}
		owner := &projectAssistantActivityOwner{scope: scope, refs: 1, exclusive: exclusive, ready: make(chan struct{})}
		s.activityOwners[key] = owner
		s.mu.Unlock()

		err := s.claimActivity(scope, "workspace-owner")
		s.mu.Lock()
		owner.err = err
		close(owner.ready)
		if err != nil && s.activityOwners[key] == owner {
			delete(s.activityOwners, key)
		}
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return s.activityOwnerRelease(key, owner), nil
	}
}

func (s *projectAssistantSupervisor) activityOwnerRelease(key projectAssistantRunKey, owner *projectAssistantActivityOwner) func() {
	var once sync.Once
	return func() {
		once.Do(func() { s.releaseActivityOwnerRef(key, owner) })
	}
}

func (s *projectAssistantSupervisor) releaseActivityOwnerRef(key projectAssistantRunKey, owner *projectAssistantActivityOwner) {
	if s == nil || owner == nil {
		return
	}
	s.mu.Lock()
	current := s.activityOwners[key]
	if current != owner {
		s.mu.Unlock()
		return
	}
	owner.refs--
	release := owner.refs <= 0 && owner.err == nil
	if release {
		// Keep the owner visible until the durable release finishes. A new
		// reservation for this project must not renew the old claim and then
		// have this delayed release delete that fresh claim.
		owner.releasing = true
		owner.releaseDone = make(chan struct{})
	} else if owner.refs <= 0 {
		delete(s.activityOwners, key)
	}
	s.mu.Unlock()
	if release {
		s.releaseActivity(owner.scope)
		s.mu.Lock()
		if s.activityOwners[key] == owner {
			delete(s.activityOwners, key)
		}
		close(owner.releaseDone)
		s.mu.Unlock()
	}
}

func (s *projectAssistantSupervisor) claimRunActivity(scope store.Scope, run store.AssistantRun) error {
	if s == nil || s.store == nil {
		return errors.New("assistant supervisor store not configured")
	}
	s.mu.Lock()
	replicaID, replicaAddr := s.replicaID, s.replicaAddr
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, held, err := s.store.TryClaimReplica(ctx, store.ReplicaClaim{
		Key:          store.AssistantRunClaimKey(scope, run.ThreadID, run.ID),
		Kind:         store.ReplicaClaimKindActivity,
		ScopeKey:     store.ReplicaClaimScopeKey(scope),
		OwnerReplica: replicaID,
		OwnerAddr:    replicaAddr,
		Detail:       run.ID,
	}, assistantActivityClaimTTL)
	if err != nil {
		return fmt.Errorf("assistant run activity claim: %w", err)
	}
	if !held {
		return store.ErrAssistantRunConflict
	}
	return nil
}

func (s *projectAssistantSupervisor) releaseRunActivity(scope store.Scope, run store.AssistantRun) {
	if s == nil || s.store == nil {
		return
	}
	s.mu.Lock()
	replicaID := s.replicaID
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.ReleaseReplicaClaim(ctx, store.AssistantRunClaimKey(scope, run.ThreadID, run.ID), replicaID); err != nil {
		klog.Background().Error(err, "releasing assistant run claim", "run", run.ID, "thread", run.ThreadID)
	}
}

func (s *projectAssistantSupervisor) claimThreadReservation(scope store.Scope, threadID string) error {
	if s == nil || s.store == nil || strings.TrimSpace(threadID) == "" {
		return nil
	}
	s.mu.Lock()
	replicaID, replicaAddr := s.replicaID, s.replicaAddr
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, held, err := s.store.TryClaimReplica(ctx, store.ReplicaClaim{
		Key:          store.AssistantThreadClaimKey(scope, threadID),
		Kind:         store.ReplicaClaimKindActivity,
		ScopeKey:     store.ReplicaClaimScopeKey(scope),
		OwnerReplica: replicaID,
		OwnerAddr:    replicaAddr,
		Detail:       "thread-reservation",
	}, assistantActivityClaimTTL)
	if err != nil {
		return fmt.Errorf("assistant thread reservation claim: %w", err)
	}
	if !held {
		return store.ErrAssistantRunConflict
	}
	return nil
}

func (s *projectAssistantSupervisor) releaseThreadReservation(scope store.Scope, threadID string) {
	if s == nil || s.store == nil || strings.TrimSpace(threadID) == "" {
		return
	}
	s.mu.Lock()
	replicaID := s.replicaID
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.ReleaseReplicaClaim(ctx, store.AssistantThreadClaimKey(scope, threadID), replicaID); err != nil {
		klog.Background().Error(err, "releasing assistant thread reservation claim", "thread", threadID)
	}
}

// renewActivityClaims heartbeats the durable claim for every live local run
// and reservation until the supervisor shuts down. A lost renewal (a peer
// took the claim over after this replica stalled past the TTL) is logged —
// the peer's orphan reconciliation owns the durable outcome.
func (s *projectAssistantSupervisor) renewActivityClaims() {
	ticker := time.NewTicker(assistantActivityClaimRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		replicaID := s.replicaID
		owners := make([]*projectAssistantActivityOwner, 0, len(s.activityOwners))
		type threadClaimRef struct {
			scope    store.Scope
			threadID string
		}
		threadClaims := make([]threadClaimRef, 0, len(s.reservations))
		type runClaimRef struct {
			scope store.Scope
			run   store.AssistantRun
		}
		activeRuns := make([]runClaimRef, 0, len(s.runs))
		for _, owner := range s.activityOwners {
			owners = append(owners, owner)
		}
		for _, active := range s.runs {
			activeRuns = append(activeRuns, runClaimRef{scope: active.scope, run: active.run})
		}
		for key, scope := range s.reservations {
			if key.ThreadID != "" {
				threadClaims = append(threadClaims, threadClaimRef{scope: scope, threadID: key.ThreadID})
			}
		}
		s.mu.Unlock()
		for _, owner := range owners {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			held, err := s.store.RenewReplicaClaim(ctx, store.ActivityClaimKey(owner.scope), replicaID)
			if err != nil {
				klog.Background().Error(err, "renewing assistant activity claim",
					"org", owner.scope.OrgUUID, "workspace", owner.scope.WorkspaceUUID, "project", owner.scope.ProjectName)
			} else if !held {
				klog.Background().Info("assistant activity claim lost to another replica",
					"org", owner.scope.OrgUUID, "workspace", owner.scope.WorkspaceUUID, "project", owner.scope.ProjectName)
			}
			// A long turn can outlive the request-path renewals of the project
			// PIN (the run writes the workspace without further HTTP traffic);
			// renew it alongside so the project cannot be adopted mid-turn.
			// Owner-checked: a pin legitimately held elsewhere is untouched.
			_, _ = s.store.RenewReplicaClaim(ctx, projectClaimKey(owner.scope.OrgUUID, owner.scope.WorkspaceUUID, owner.scope.ProjectName), replicaID)
			cancel()
		}
		for _, reservation := range threadClaims {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if held, err := s.store.RenewReplicaClaim(ctx, store.AssistantThreadClaimKey(reservation.scope, reservation.threadID), replicaID); err != nil || !held {
				klog.Background().Info("assistant thread reservation claim lost", "thread", reservation.threadID, "held", held, "error", err)
			}
			cancel()
		}
		for _, active := range activeRuns {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if held, err := s.store.RenewReplicaClaim(ctx, store.AssistantRunClaimKey(active.scope, active.run.ThreadID, active.run.ID), replicaID); err != nil || !held {
				klog.Background().Info("assistant run activity claim lost", "thread", active.run.ThreadID, "run", active.run.ID, "held", held, "error", err)
			}
			cancel()
		}
	}
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
	return s.ReserveWorkspace(context.Background(), scope)
}

// ReserveThread owns the start boundary for one thread while sharing the
// project's durable workspace owner with turns on other threads.
func (s *projectAssistantSupervisor) ReserveThread(scope store.Scope, threadID string) (func(), error) {
	if s == nil {
		return nil, errors.New("assistant supervisor not configured")
	}
	key := projectAssistantThreadKey(scope, threadID)
	threadID = key.ThreadID
	if !key.valid() {
		return nil, errors.New("assistant supervisor scope is required")
	}
	s.mu.Lock()
	if s.runs[key] != nil {
		s.mu.Unlock()
		return nil, store.ErrAssistantRunConflict
	}
	if s.reservations == nil {
		s.reservations = map[projectAssistantRunKey]store.Scope{}
	}
	if _, exists := s.reservations[key]; exists {
		s.mu.Unlock()
		return nil, store.ErrAssistantRunConflict
	}
	s.reservations[key] = scope
	s.mu.Unlock()
	releaseOwner, err := s.acquireActivityOwner(context.Background(), scope, false)
	if err != nil {
		s.mu.Lock()
		delete(s.reservations, key)
		s.mu.Unlock()
		return nil, err
	}
	if err := s.claimThreadReservation(scope, threadID); err != nil {
		s.mu.Lock()
		delete(s.reservations, key)
		s.mu.Unlock()
		releaseOwner()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.releaseThreadReservation(scope, threadID)
			s.mu.Lock()
			delete(s.reservations, key)
			s.mu.Unlock()
			releaseOwner()
		})
	}, nil
}

// ReserveMutation shares the project's owner for a short source mutation.
// Its caller must hold the workspace store's transaction lock as well.
func (s *projectAssistantSupervisor) ReserveMutation(ctx context.Context, scope store.Scope) (func(), error) {
	return s.acquireActivityOwner(ctx, scope, false)
}

// ReserveWorkspace excludes turns and source transactions while a destructive
// or runtime-wide operation changes the project workspace.
func (s *projectAssistantSupervisor) ReserveWorkspace(ctx context.Context, scope store.Scope) (func(), error) {
	return s.acquireActivityOwner(ctx, scope, true)
}

func (s *projectAssistantSupervisor) reserved(scope store.Scope, threadIDs ...string) bool {
	if s == nil {
		return false
	}
	key := projectAssistantWorkspaceKey(scope)
	if len(threadIDs) > 0 {
		key.ThreadID = strings.TrimSpace(threadIDs[0])
		s.mu.Lock()
		_, reserved := s.reservations[key]
		s.mu.Unlock()
		return reserved
	}
	s.mu.Lock()
	reserved := false
	for reservation := range s.reservations {
		if reservation.OrgUUID == key.OrgUUID && reservation.WorkspaceUUID == key.WorkspaceUUID &&
			reservation.ProjectName == key.ProjectName && reservation.ProjectUID == key.ProjectUID {
			reserved = true
			break
		}
	}
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
	type threadReservation struct {
		scope    store.Scope
		threadID string
	}
	threadReservations := make([]threadReservation, 0, len(s.reservations))
	for key, scope := range s.reservations {
		if key.ThreadID != "" {
			threadReservations = append(threadReservations, threadReservation{scope: scope, threadID: key.ThreadID})
		}
	}
	s.mu.Unlock()
	for _, interrupted := range accumulators {
		_, err := s.AbortWith(interrupted.scope, interrupted.run.ID, func(run *store.AssistantRun, _ *store.Message) error {
			run.AbortReason = store.AssistantRunAbortReasonInterrupted
			return nil
		}, interrupted.run.ThreadID)
		if err == nil {
			interrupted.run.Status = store.AssistantRunStatusInterrupted
			interrupted.run.AbortReason = store.AssistantRunAbortReasonInterrupted
			interrupted.run.Revision++
			_ = appendProjectAssistantInterruptedBoundary(ctx, s.store, interrupted.scope, interrupted.run)
		}
	}
	for _, reservation := range threadReservations {
		s.releaseThreadReservation(reservation.scope, reservation.threadID)
	}
	if s.cancel != nil {
		s.cancel()
	}
	// Shutdown has durably interrupted every active run, so release each
	// project owner once regardless of how many thread and mutation refs it
	// carried. Any deferred holder release after this point is idempotent.
	s.mu.Lock()
	ownerScopes := make([]store.Scope, 0, len(s.activityOwners))
	for _, owner := range s.activityOwners {
		ownerScopes = append(ownerScopes, owner.scope)
	}
	s.activityOwners = map[projectAssistantRunKey]*projectAssistantActivityOwner{}
	s.mu.Unlock()
	for _, scope := range ownerScopes {
		s.releaseActivity(scope)
	}
}

func (s *projectAssistantSupervisor) Attach(scope store.Scope, run store.AssistantRun, message store.Message) (*projectAssistantSnapshotAccumulator, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("assistant supervisor store not configured")
	}
	run.ThreadID = strings.TrimSpace(run.ThreadID)
	key := projectAssistantThreadKey(scope, run.ThreadID)
	if !key.valid() || run.ID == "" {
		return nil, errors.New("assistant supervisor scope and run id are required")
	}
	releaseOwner, err := s.acquireActivityOwner(context.Background(), scope, false)
	if err != nil {
		return nil, err
	}
	run.ProjectName = scope.ProjectName
	message.ProjectName = scope.ProjectName
	s.mu.Lock()
	if existing := s.runs[key]; existing != nil {
		s.mu.Unlock()
		releaseOwner()
		if existing.run.ID == run.ID {
			return &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: run.ID}, nil
		}
		return nil, store.ErrAssistantRunConflict
	}
	_, cancel := context.WithCancelCause(s.ctx)
	inserted := &projectAssistantSupervisedRun{
		scope:                scope,
		run:                  run,
		message:              message,
		committedRun:         run,
		committedMessage:     message,
		cancel:               cancel,
		subscribers:          map[uint64]chan projectAssistantRunSnapshot{},
		steering:             make(chan projectAssistantSteeringInput, projectAssistantSteeringQueueCapacity),
		steeringReceipts:     map[string]store.Message{},
		releaseActivityOwner: releaseOwner,
	}
	s.runs[key] = inserted
	s.mu.Unlock()
	// Keep an independent execution lease for thread-scoped recovery while the
	// project claim continues fencing the shared workspace.
	if err := s.claimRunActivity(scope, run); err != nil {
		s.mu.Lock()
		s.removeRunLocked(key, inserted)
		s.mu.Unlock()
		return nil, err
	}
	return &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: run.ID}, nil
}

// Steering returns the run-scoped input queue owned by the supervisor. It is
// intentionally receive-only outside the supervisor: EnqueueSteering persists
// the user message and advances the durable run revision before delivery.
func (s *projectAssistantSupervisor) Steering(scope store.Scope, runID string, threadIDs ...string) <-chan projectAssistantSteeringInput {
	if s == nil {
		return nil
	}
	key := s.runKey(scope, runID, threadIDs...)
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
func (s *projectAssistantSupervisor) SealSteering(scope store.Scope, runID string, threadIDs ...string) bool {
	if s == nil {
		return true
	}
	key := s.runKey(scope, runID, threadIDs...)
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
	threadIDs ...string,
) (store.AssistantRun, store.Message, store.Message, bool, error) {
	if s == nil || s.store == nil {
		return store.AssistantRun{}, store.Message{}, store.Message{}, false, nil
	}
	expectedRunID = strings.TrimSpace(expectedRunID)
	if expectedRunID == "" {
		return store.AssistantRun{}, store.Message{}, store.Message{}, false, nil
	}
	key := s.runKey(scope, expectedRunID, threadIDs...)
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
	run := active.run
	var assistant store.Message
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
	threadIDs ...string,
) error {
	if s == nil || len(inputs) == 0 {
		return nil
	}
	key := s.runKey(scope, runID, threadIDs...)
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

func (s *projectAssistantSupervisor) accumulatorFor(scope store.Scope, runID string, threadIDs ...string) *projectAssistantSnapshotAccumulator {
	if s == nil {
		return nil
	}
	key := s.runKey(scope, runID, threadIDs...)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, active := range s.runs {
		if key.OrgUUID == scope.OrgUUID && key.WorkspaceUUID == scope.WorkspaceUUID && key.ProjectName == scope.ProjectName && key.ProjectUID == scope.ProjectUID && active.message.ID == messageID {
			return &projectAssistantSnapshotAccumulator{supervisor: s, key: key, runID: active.run.ID}
		}
	}
	return nil
}

// BindStopRequest durably reserves the retry identity for a supervised Stop.
// It shares the lifecycle transition lock so concurrent callers cannot replace
// one another's receipt before Stop changes the run status.
func (s *projectAssistantSupervisor) BindStopRequest(ctx context.Context, scope store.Scope, runID, actor, clientRequestID string, threadIDs ...string) (bool, error) {
	if s == nil {
		return false, nil
	}
	key := s.runKey(scope, runID, threadIDs...)
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
	// Release this run's recovery lease and one reference to the shared project
	// owner. The last run, mutation, or reservation releases the project claim.
	go s.releaseRunActivity(active.scope, active.run)
	if active.releaseActivityOwner != nil {
		go active.releaseActivityOwner()
	}
}

func assistantRunTerminal(status store.AssistantRunStatus) bool {
	switch status {
	case store.AssistantRunStatusCompleted, store.AssistantRunStatusFailed,
		store.AssistantRunStatusInterrupted, store.AssistantRunStatusAborted:
		return true
	}
	return false
}

func (s *projectAssistantSupervisor) Abort(scope store.Scope, runID string, threadIDs ...string) bool {
	ok, _ := s.AbortWith(scope, runID, nil, threadIDs...)
	return ok
}

// Stop makes cancellation observable before asking Eino to unwind. Pending
// runs have no active loop, so they use the existing synchronous terminal path.
// Callers with the authenticated request identity should use
// StopWithIdentity so a suspended run sandbox can be deleted in the run's
// workspace cluster.
func (s *projectAssistantSupervisor) Stop(scope store.Scope, runID string, threadIDs ...string) (store.AssistantRun, bool, error) {
	return s.stopWithIdentity(context.Background(), identity{}, scope, runID, threadIDs...)
}

func (s *projectAssistantSupervisor) StopWithIdentity(ctx context.Context, id identity, scope store.Scope, runID string, threadIDs ...string) (store.AssistantRun, bool, error) {
	return s.stopWithIdentity(ctx, id, scope, runID, threadIDs...)
}

func (s *projectAssistantSupervisor) stopWithIdentity(ctx context.Context, id identity, scope store.Scope, runID string, threadIDs ...string) (store.AssistantRun, bool, error) {
	if s == nil {
		return store.AssistantRun{}, false, nil
	}
	key := s.runKey(scope, runID, threadIDs...)
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
		ok, err := s.AbortWith(scope, runID, nil, threadIDs...)
		if !ok || err != nil {
			return store.AssistantRun{}, ok, err
		}
		run, getErr := s.store.GetAssistantRun(context.Background(), scope, runID)
		if getErr != nil {
			return run, true, getErr
		}
		if s.server != nil {
			cleanupCtx := ctx
			if cleanupCtx == nil {
				cleanupCtx = context.Background()
			}
			if err := s.server.cleanupInterruptedProjectAssistantRunSandbox(cleanupCtx, id, scope, run); err != nil {
				return run, true, err
			}
		}
		return run, true, nil
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
func (s *projectAssistantSupervisor) AdmitMutation(ctx context.Context, scope store.Scope, runID, actor string, threadIDs ...string) error {
	if s == nil || s.store == nil {
		return store.ErrAssistantRunConflict
	}
	key := s.runKey(scope, runID, threadIDs...)
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
func (s *projectAssistantSupervisor) AbortWith(scope store.Scope, runID string, mutate func(*store.AssistantRun, *store.Message) error, threadIDs ...string) (bool, error) {
	if s == nil {
		return false, nil
	}
	key := s.runKey(scope, runID, threadIDs...)
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

func (s *projectAssistantSupervisor) Subscribe(scope store.Scope, runID string, afterRevision int64, threadIDs ...string) (<-chan projectAssistantRunSnapshot, func(), error) {
	if s == nil {
		return nil, nil, errors.New("assistant supervisor not configured")
	}
	key := s.runKey(scope, runID, threadIDs...)
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
	return a.update(ctx, func(active *projectAssistantSupervisedRun) { active.message.Content = content }, immediate, false)
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
	return a.update(ctx, func(active *projectAssistantSupervisedRun) { mutate(&active.run, &active.message) }, true, false)
}

// UpdateStoppingToolSnapshot permits only an already-produced terminal tool
// result to settle while Stop owns the run's stopping -> interrupted
// transition. Callers must not use this for model text, plans, or new work.
func (a *projectAssistantSnapshotAccumulator) UpdateStoppingToolSnapshot(ctx context.Context, mutate func(*store.AssistantRun, *store.Message)) error {
	return a.update(ctx, func(active *projectAssistantSupervisedRun) { mutate(&active.run, &active.message) }, true, true)
}

func (a *projectAssistantSnapshotAccumulator) UpdateMessage(ctx context.Context, content string, metadata map[string]any) error {
	return a.update(ctx, func(active *projectAssistantSupervisedRun) {
		active.message.Content = content
		active.message.Metadata = metadata
	}, true, false)
}

func (a *projectAssistantSnapshotAccumulator) UpdateRun(ctx context.Context, mutate func(*store.AssistantRun)) error {
	return a.update(ctx, func(active *projectAssistantSupervisedRun) { mutate(&active.run) }, true, false)
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

func (a *projectAssistantSnapshotAccumulator) update(ctx context.Context, mutate func(*projectAssistantSupervisedRun), immediate, allowStoppingToolSettlement bool) error {
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
	if active.run.Status == store.AssistantRunStatusStopping && !allowStoppingToolSettlement {
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
	_ = a.update(context.Background(), func(*projectAssistantSupervisedRun) {}, true, false)
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
