// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");

package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/railgrid/provider-agents/store"
)

const runLeaseHeartbeatInterval = store.RunLeaseDuration / 3
const runLeaseHeartbeatTimeout = 10 * time.Second

type runLeaseContextKey struct{}

type runLeaseIdentity struct {
	runID string
	owner string
	epoch uint64
}

func newExecutionOwner(processID string) string {
	if processID == "" {
		return ""
	}
	return processID + "/" + uuid.NewString()
}

func (s *Server) claimFreshRun(ctx context.Context, scope store.Scope, runID, agentUID string, recovery store.RecoveryIdentity) (store.Run, context.Context, func(), error) {
	if s.cfg.ProcessID == "" {
		return store.Run{}, ctx, func() {}, nil
	}
	prior, err := s.store.GetRun(ctx, scope, runID)
	if err != nil {
		return store.Run{}, ctx, func() {}, err
	}
	owner := newExecutionOwner(s.cfg.ProcessID)
	claimed, err := s.store.ClaimFreshRun(ctx, scope, runID, prior.UpdatedAt, owner, agentUID, recovery, time.Now().UTC())
	if err != nil {
		return store.Run{}, ctx, func() {}, err
	}
	return s.startRunLease(ctx, scope, claimed)
}

func (s *Server) startRunLease(ctx context.Context, scope store.Scope, run store.Run) (store.Run, context.Context, func(), error) {
	if s.cfg.ProcessID == "" || run.ExecutionOwner == "" {
		s.projectRun(ctx, scope, run)
		return run, ctx, func() {}, nil
	}
	deadline := run.ExecutionLeaseAt.Add(store.RunLeaseDuration)
	if run.ExecutionLeaseAt.IsZero() || !deadline.After(time.Now().UTC()) {
		return store.Run{}, ctx, func() {}, fmt.Errorf("%w: acquired lease is expired", store.ErrRunLeaseLost)
	}
	s.projectRun(ctx, scope, run)

	leaseCtx, cancel := context.WithCancelCause(ctx)
	leaseCtx = context.WithValue(leaseCtx, runLeaseContextKey{}, runLeaseIdentity{runID: run.ID, owner: run.ExecutionOwner, epoch: run.ExecutionEpoch})
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel(context.Canceled)
			<-done
			releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), projectionTimeout)
			defer releaseCancel()
			_, err := s.store.ReleaseRunLease(releaseCtx, scope, run.ID, run.ExecutionOwner, run.ExecutionEpoch)
			if err != nil {
				log.Printf("run %s: release lease: %v", run.ID, err)
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
			timer := time.NewTimer(min(runLeaseHeartbeatInterval, remaining))
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
			refreshed, ok, err := s.store.RenewRunLease(heartbeatCtx, scope, run.ID, run.ExecutionOwner, run.ExecutionEpoch, time.Now().UTC())
			heartbeatCancel()
			if errors.Is(err, store.ErrRunCancellationRequested) {
				cancel(context.Canceled)
				return
			}
			if err != nil {
				if time.Until(deadline) <= 0 {
					cancel(store.ErrRunLeaseLost)
					return
				}
				log.Printf("run %s: renew lease: %v", run.ID, err)
				continue
			}
			if !ok || refreshed.ExecutionOwner != run.ExecutionOwner || refreshed.ExecutionEpoch != run.ExecutionEpoch || refreshed.ExecutionLeaseAt.IsZero() {
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

func leaseIdentityFromContext(ctx context.Context, runID string) (runLeaseIdentity, bool) {
	identity, ok := ctx.Value(runLeaseContextKey{}).(runLeaseIdentity)
	return identity, ok && identity.runID == runID && identity.owner != "" && identity.epoch != 0
}
