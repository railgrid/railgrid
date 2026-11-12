// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/railgrid/provider-agents/store"
)

const runLeaseHeartbeatInterval = store.RunLeaseDuration / 3
const runLeaseHeartbeatTimeout = 10 * time.Second

func (s *Server) acquireRunningLease(ctx context.Context, scope store.Scope, persisted *store.Run, run *taskRun, runCtx *context.Context, release *func()) error {
	if s.cfg.ProcessID == "" {
		s.projectRun(ctx, scope, *persisted)
		return nil
	}
	leased, leaseCtx, stop, err := s.startRunLease(*runCtx, scope, *persisted)
	if err != nil {
		return err
	}
	*persisted = leased
	run.Attempt = leased.Attempt
	run.ExecutionOwner = leased.ExecutionOwner
	*runCtx = leaseCtx
	*release = stop
	return nil
}

func (s *Server) claimFreshRunLease(ctx context.Context, scope store.Scope, runID string) (store.Run, context.Context, func(), error) {
	if s.cfg.ProcessID == "" {
		return store.Run{}, ctx, func() {}, nil
	}
	pending, err := s.store.GetRun(ctx, scope, runID)
	if err != nil {
		return store.Run{}, ctx, func() {}, err
	}
	owner := newExecutionOwner(s.cfg.ProcessID)
	started, err := s.store.ClaimFreshRun(ctx, scope, runID, pending.UpdatedAt, owner, time.Now().UTC())
	if err != nil {
		return store.Run{}, ctx, func() {}, err
	}
	return s.startRunLease(ctx, scope, started)
}

// startRunLease takes the durable execution lease before any model or runner
// dispatch. Heartbeats change only the lease timestamp, leaving UpdatedAt as
// the checkpoint/recovery compare token. A lost lease cancels the run context.
func (s *Server) startRunLease(ctx context.Context, scope store.Scope, run store.Run) (store.Run, context.Context, func(), error) {
	if s.cfg.ProcessID == "" {
		return run, ctx, func() {}, nil
	}
	owner := run.ExecutionOwner
	if owner == "" || owner == s.cfg.ProcessID {
		owner = newExecutionOwner(s.cfg.ProcessID)
	}
	if !executionOwnerBelongsToProcess(owner, s.cfg.ProcessID) {
		return store.Run{}, ctx, func() {}, store.ErrRunLeaseLost
	}
	run.ExecutionOwner = owner
	leased, err := s.store.AcquireRunLease(ctx, scope, run.ID, owner, run.Attempt, time.Now().UTC())
	if err != nil {
		return store.Run{}, ctx, func() {}, errors.Join(store.ErrRunLeaseLost, err)
	}
	run = leased
	deadline := run.ExecutionLeaseAt.Add(store.RunLeaseDuration)
	if run.ExecutionLeaseAt.IsZero() || !deadline.After(time.Now().UTC()) {
		return store.Run{}, ctx, func() {}, fmt.Errorf("%w: acquired lease is already expired", store.ErrRunLeaseLost)
	}
	s.projectRun(ctx, scope, leased)
	if !deadline.After(time.Now().UTC()) {
		return store.Run{}, ctx, func() {}, fmt.Errorf("%w: lease expired while projecting the run", store.ErrRunLeaseLost)
	}

	leaseCtx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel(context.Canceled)
			<-done
			persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), projectionTimeout)
			defer persistCancel()
			if _, err := s.store.ReleaseRunLease(persistCtx, scope, run.ID, owner, run.Attempt); err != nil {
				log.Printf("run %s: releasing execution lease: %v", run.ID, err)
			}
		})
	}
	go func() {
		defer close(done)
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				cancel(store.ErrRunLeaseLost)
				return
			}
			wait := min(runLeaseHeartbeatInterval, remaining)
			timer := time.NewTimer(wait)
			select {
			case <-leaseCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			remaining = time.Until(deadline)
			if remaining <= 0 {
				cancel(store.ErrRunLeaseLost)
				return
			}
			heartbeatCtx, heartbeatCancel := context.WithTimeout(leaseCtx, min(runLeaseHeartbeatTimeout, remaining))
			refreshed, ok, renewErr := s.store.RenewRunLease(heartbeatCtx, scope, run.ID, owner, run.Attempt, time.Now().UTC())
			heartbeatCancel()
			if errors.Is(renewErr, store.ErrRunCancellationRequested) {
				cancel(context.Canceled)
				return
			}
			if renewErr != nil {
				if time.Until(deadline) <= 0 {
					cancel(store.ErrRunLeaseLost)
					return
				}
				log.Printf("run %s: renewing execution lease: %v", run.ID, renewErr)
				continue
			}
			if !ok || refreshed.ExecutionOwner != owner || refreshed.Attempt != run.Attempt || refreshed.ExecutionLeaseAt.IsZero() {
				cancel(store.ErrRunLeaseLost)
				return
			}
			deadline = refreshed.ExecutionLeaseAt.Add(store.RunLeaseDuration)
			if !deadline.After(time.Now().UTC()) {
				cancel(store.ErrRunLeaseLost)
				return
			}
			s.projectRun(leaseCtx, scope, refreshed)
		}
	}()
	return run, leaseCtx, stop, nil
}

func leaseWasLost(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), store.ErrRunLeaseLost)
}

func newExecutionOwner(processID string) string {
	if processID == "" {
		return ""
	}
	return processID + "/" + uuid.NewString()
}

func executionOwnerBelongsToProcess(owner, processID string) bool {
	return owner == processID || processID != "" && strings.HasPrefix(owner, processID+"/")
}
