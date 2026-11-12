// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant"
)

const (
	maxRunDescendants = 1000
	maxRunLineageRows = 10000
	runDescendantPage = 200
)

type descendantReconcileContextKey struct{}

// parentRunActive is the admission and resume fence for a child Run. A parent
// missing from the same tenant scope, terminal, or already cancelled cannot
// authorize a new child dispatch or approval continuation.
func (s *Server) parentRunActive(ctx context.Context, scope store.Scope, parentID string) (bool, error) {
	if parentID == "" {
		return false, fmt.Errorf("delegated execution has no parent Run")
	}
	parent, found, err := s.parentRun(ctx, scope, parentID)
	if err != nil || !found {
		return false, err
	}
	if parent.Phase != store.RunPhaseRunning || parent.CancelRequested {
		return false, nil
	}
	if parent.ParentRunID == "" {
		return true, nil
	}
	return s.parentRunAllowsResume(ctx, scope, parent)
}

func (s *Server) parentRun(ctx context.Context, scope store.Scope, parentID string) (store.Run, bool, error) {
	if parentID == "" {
		return store.Run{}, false, fmt.Errorf("child Run has no parent Run")
	}
	parentScope := scope
	parentScope.AgentName = ""
	page, err := s.store.QueryRuns(ctx, parentScope, store.RunQuery{ID: parentID, Limit: 1})
	if err != nil {
		return store.Run{}, false, fmt.Errorf("reading parent Run %s: %w", parentID, err)
	}
	if len(page.Items) != 1 {
		return store.Run{}, false, nil
	}
	return page.Items[0], true, nil
}

// parentRunAllowsResume preserves an attended spawn worker after its parent
// completed successfully. Delegated runs and new child admissions still
// require a live parent; only already-persisted spawn work may continue.
func (s *Server) parentRunAllowsResume(ctx context.Context, scope store.Scope, child store.Run) (bool, error) {
	return s.parentRunAllowsResumeFrom(ctx, scope, child, map[string]bool{}, false)
}

func (s *Server) parentRunAllowsResumeFrom(ctx context.Context, scope store.Scope, child store.Run, visited map[string]bool, historical bool) (bool, error) {
	if child.CancelRequested {
		return false, nil
	}
	parent, found, err := s.parentRun(ctx, scope, child.ParentRunID)
	if err != nil || !found || parent.CancelRequested {
		return false, err
	}
	if visited[parent.ID] {
		return false, nil
	}
	if parent.Phase != store.RunPhaseRunning && parent.Phase != store.RunPhasePendingApproval && parent.Phase != store.RunPhaseSucceeded {
		return false, nil
	}
	historicalSuccess := historical && child.Phase == store.RunPhaseSucceeded
	if parent.Phase == store.RunPhaseSucceeded && !historicalSuccess && !successfulSpawnContinuation(child) {
		return false, nil
	}
	if parent.ParentRunID == "" {
		return true, nil
	}
	visited[parent.ID] = true
	return s.parentRunAllowsResumeFrom(ctx, scope, parent, visited, true)
}

func successfulSpawnContinuation(child store.Run) bool {
	if child.CancelRequested || child.Trigger != agentsv1alpha1.RunTriggerSpawn || (child.Phase != store.RunPhasePendingApproval && child.Phase != store.RunPhaseRunning) {
		return false
	}
	var checkpoint runCheckpoint
	return json.Unmarshal(child.Checkpoint, &checkpoint) == nil && checkpoint.Worker != nil
}

// cancelDescendantRun records cancellation durably, interrupts a local run or
// stops a parked runner, and refuses to count cleanup complete while the child
// still has a live phase. The parent identity is internal attribution only.
func (s *Server) cancelDescendantRun(ctx context.Context, scope store.Scope, parentID string, child store.Run) error {
	if !runCancellable(child.Phase) {
		return nil
	}
	var client *agentsclient.Client
	if child.Backend == agentsv1alpha1.AgentBackendHarness && len(child.Checkpoint) > 0 {
		if dyn, err := s.backgroundScoped(ctx, scope.ClusterID); err == nil {
			client = agentsclient.NewFromScope(tenant.NewScopeFromDynamic(scope.ClusterID, dyn))
		}
	}
	id := identity{
		orgUUID: scope.OrgUUID, workspaceUUID: scope.WorkspaceUUID,
		clusterID: scope.ClusterID, user: parentTerminalCancelUser(parentID),
	}
	if _, err := s.cancelStoredRun(ctx, client, id, child); err != nil {
		return fmt.Errorf("cancelling child Run %s: %w", child.ID, err)
	}
	childScope := scope
	childScope.AgentName = child.AgentName
	latest, err := s.store.GetRun(ctx, childScope, child.ID)
	if err != nil {
		return fmt.Errorf("checking cancellation of child Run %s: %w", child.ID, err)
	}
	if runCancellable(latest.Phase) {
		return fmt.Errorf("child Run %s is still active after cancellation was requested", child.ID)
	}
	return nil
}

// reconcileRunDescendants walks the complete workspace-scoped lineage in
// pages. It is invoked on terminal transitions and retried by the Run
// reconciler until every descendant is terminal. The explicit cap bounds one
// reconcile; exceeding it returns an error instead of silently leaving later
// children running.
func (s *Server) reconcileRunDescendants(ctx context.Context, scope store.Scope, parentID string) error {
	if s.store == nil || parentID == "" || ctx.Value(descendantReconcileContextKey{}) == true {
		return nil
	}
	parent, parentFound, err := s.parentRun(ctx, scope, parentID)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, descendantReconcileContextKey{}, true)
	scope.AgentName = ""
	queue := []string{parentID}
	visited := map[string]bool{parentID: true}
	lineageRuns := map[string]store.Run{}
	forceCascade := map[string]bool{}
	if parentFound {
		lineageRuns[parentID] = parent
	}
	forceCascade[parentID] = !parentFound || parent.Phase != store.RunPhaseSucceeded || parent.CancelRequested
	var firstErr error
	lineageRows, newCancelRequests := 0, 0
	for head := 0; head < len(queue); head++ {
		cursor := ""
		for {
			page, err := s.store.QueryRuns(ctx, scope, store.RunQuery{ParentRunID: queue[head], Limit: runDescendantPage, Cursor: cursor})
			if err != nil {
				return errors.Join(firstErr, fmt.Errorf("listing descendants of Run %s: %w", queue[head], err))
			}
			for _, child := range page.Items {
				if visited[child.ID] {
					continue
				}
				visited[child.ID] = true
				lineageRuns[child.ID] = child
				lineageRows++
				if lineageRows > maxRunLineageRows {
					return errors.Join(firstErr, fmt.Errorf("run %s has more than %d lineage rows; cancellation stopped at the safety bound", parentID, maxRunLineageRows))
				}
				parentID := queue[head]
				immediateParent, immediateParentFound := lineageRuns[parentID]
				parentIsCurrent := immediateParentFound && (immediateParent.Phase == store.RunPhaseRunning || immediateParent.Phase == store.RunPhaseSucceeded) && !immediateParent.CancelRequested
				preserve := !forceCascade[parentID] && parentIsCurrent && immediateParent.Phase == store.RunPhaseSucceeded && successfulSpawnContinuation(child)
				if preserve {
					// This active worker now owns its remaining lineage. Its own Run
					// reconciliation will clean up descendants when it terminates.
					continue
				}
				if runCancellable(child.Phase) {
					if !child.CancelRequested {
						newCancelRequests++
						if newCancelRequests > maxRunDescendants {
							return errors.Join(firstErr, fmt.Errorf("run %s has more than %d new active descendants; cancellation will continue on retry", parentID, maxRunDescendants))
						}
					}
					if err := s.cancelDescendantRun(ctx, scope, child.ParentRunID, child); err != nil {
						firstErr = errors.Join(firstErr, err)
					}
				}
				forceCascade[child.ID] = forceCascade[parentID] || child.Phase != store.RunPhaseSucceeded || child.CancelRequested
				queue = append(queue, child.ID)
			}
			if page.NextCursor == "" {
				break
			}
			if page.NextCursor == cursor {
				return errors.Join(firstErr, fmt.Errorf("listing descendants of Run %s returned a repeated cursor", queue[head]))
			}
			cursor = page.NextCursor
		}
	}
	return firstErr
}

func isTerminalRunPhase(phase store.RunPhase) bool {
	switch phase {
	case store.RunPhaseSucceeded, store.RunPhaseFailed, store.RunPhaseAborted:
		return true
	default:
		return false
	}
}

func (s *Server) reconcileTerminalRunDescendants(ctx context.Context, clusterID, agentName, runID string) error {
	if s.bg == nil {
		return nil
	}
	scope := s.bg.scopeFor(ctx, clusterID, agentName)
	return s.reconcileRunDescendants(ctx, scope, runID)
}

// reconcileRunParent retries cleanup for an active child whose parent was
// deleted or became terminal elsewhere. Parked harness cancellation can fail
// when the workspace client is unavailable; the Run controller retries this
// callback until the child is actually terminal.
func (s *Server) reconcileRunParent(ctx context.Context, scope store.Scope, runID string) error {
	child, err := s.store.GetRun(ctx, scope, runID)
	if err != nil {
		return err
	}
	if child.ParentRunID == "" || !runCancellable(child.Phase) {
		return nil
	}
	allowed, err := s.parentRunAllowsResume(ctx, scope, child)
	if err != nil {
		return err
	}
	if allowed {
		return nil
	}
	return s.cancelDescendantRun(ctx, scope, child.ParentRunID, child)
}

func parentTerminalCancelUser(parentID string) string { return "parent:" + parentID }
