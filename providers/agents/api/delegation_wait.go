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
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant"
	"github.com/railgrid/provider-agents/tools"
)

// This is a bounded synchronous join on existing execution records, not an
// external-condition checkpoint suspension. The caller's deadline remains in
// force. A replay of the SAME engine tool call resolves its existing child.
func (s *Server) delegateAndWait(ctx context.Context, d tools.Deps, target, task string) (store.Run, error) {
	call := backend.ToolCallIDFromContext(ctx)
	if d.RunID == "" || call == "" {
		return store.Run{}, fmt.Errorf("delegation requires parent and engine tool-call identities")
	}
	scope := d.Scope
	scope.AgentName = target
	key := "delegate:" + d.RunID + ":" + call
	existing, found, err := s.store.FindRunByIdempotencyKey(ctx, scope, key)
	if err != nil {
		return store.Run{}, err
	}
	active, err := s.parentRunActive(ctx, d.Scope, d.RunID)
	if err != nil {
		return store.Run{}, err
	}
	if !active {
		if found {
			if err := s.cancelDescendantRun(ctx, d.Scope, d.RunID, existing); err != nil {
				return store.Run{}, err
			}
		}
		return store.Run{}, fmt.Errorf("parent Run %s is no longer active", d.RunID)
	}
	id := existing.ID
	if found {
		if existing.ParentRunID != d.RunID {
			return store.Run{}, fmt.Errorf("delegated execution lineage changed")
		}
	} else {
		if err := s.checkDelegationBudget(ctx, d.Scope, d.RunID); err != nil {
			return store.Run{}, err
		}
		child, err := d.CR.GetAgent(ctx, target)
		if err != nil {
			return store.Run{}, err
		}
		if child == nil {
			return store.Run{}, fmt.Errorf("delegate %q does not exist", target)
		}
		repository, err := s.delegatedRepository(ctx, d, child)
		if err != nil {
			return store.Run{}, err
		}
		active, err = s.parentRunActive(ctx, d.Scope, d.RunID)
		if err != nil {
			return store.Run{}, err
		}
		if !active {
			return store.Run{}, fmt.Errorf("parent Run %s is no longer active", d.RunID)
		}
		tr := taskRun{SessionID: key, Task: task, Trigger: agentsv1alpha1.RunTriggerDelegation, SourceName: d.Agent.Name, ParentRunID: d.RunID, IdempotencyKey: key}
		if d.CodeScope != nil {
			reviewScope, err := tools.DelegateReviewScope(ctx, d, key, child.Name, string(child.UID))
			if err != nil {
				return store.Run{}, err
			}
			raw, _ := json.Marshal(reviewScope)
			tr.allowCodeScope = true
			tr.Task = tools.CodeScopePrefix + string(raw) + "\n" + tr.Task + "\nYou may publish actionable findings directly with code_pull_request through Code; observe first, use the exact assigned expectedHead and a stable key. Zero findings creates no review. Do not post progress comments; Factory maintains one PR status comment."
		}
		tr.Repository = repository
		if repository != nil {
			tr.SessionID = ""
			tr.Task += "\nThe runner has checked out the assigned base already. Work only in its current repository. Do not clone, commit, create bundles or push: the runner exports the canonical result after you finish. Return your findings and verification as your final answer."
		}
		admitted, err := s.startRun(ctx, scope, child, tr, runAccess{Creds: d.Secrets, CR: d.CR, ClusterID: d.DataPlane.ClusterID, RecoveryIdentity: d.RecoveryIdentity})
		if err != nil {
			return store.Run{}, err
		}
		id = admitted.ID
	}
	out, err := s.waitDelegation(ctx, scope, id)
	if err != nil && ctx.Err() != nil && !errors.Is(context.Cause(ctx), store.ErrRunLeaseLost) {
		// A cancelled parent must not leave its parked child running independently.
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backendCancelTimeout)
		defer cancel()
		if child, e := s.store.GetRun(stopCtx, scope, id); e == nil && runCancellable(child.Phase) {
			var client *agentsclient.Client
			if dyn, e := s.backgroundScoped(stopCtx, d.DataPlane.ClusterID); e == nil {
				client = agentsclient.NewFromScope(tenant.NewScopeFromDynamic(d.DataPlane.ClusterID, dyn))
			}
			if client == nil && child.Backend == agentsv1alpha1.AgentBackendHarness && len(child.Checkpoint) > 0 {
				return store.Run{}, fmt.Errorf("parent stopped; parked child cancellation unavailable")
			}
			if _, e := s.cancelStoredRun(stopCtx, client, identity{orgUUID: scope.OrgUUID, workspaceUUID: scope.WorkspaceUUID, clusterID: d.DataPlane.ClusterID, user: "parent:" + d.RunID}, child); e != nil {
				return store.Run{}, e
			}
		}
	}
	return out, err
}

func (s *Server) waitDelegation(ctx context.Context, scope store.Scope, id string) (store.Run, error) {
	tick := time.NewTicker(waitPollInterval)
	defer tick.Stop()
	for {
		run, err := s.store.GetRun(ctx, scope, id)
		if err != nil {
			return store.Run{}, err
		}
		switch run.Phase {
		case store.RunPhaseSucceeded, store.RunPhaseFailed, store.RunPhaseAborted:
			return run, nil
		}
		// PendingApproval is not a completed delegation: its result must not send
		// the orchestrator into more invocations while the worker is still parked.
		select {
		case <-ctx.Done():
			return store.Run{}, ctx.Err()
		case <-tick.C:
		}
	}
}
