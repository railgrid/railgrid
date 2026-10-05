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
	"encoding/json"
	"errors"
	"strings"

	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
)

// initialHarnessCheckpoint persists only the attempt coordinates needed to
// stop a remote dispatch if this process disappears before the runner emits a
// resumable event snapshot. It contains no URL, bearer, or harness credential.
func initialHarnessCheckpoint(run taskRun, turn *harnessTurn) (json.RawMessage, error) {
	if run.Agent == nil || turn == nil || turn.backend == nil {
		return nil, errors.New("cannot checkpoint a harness dispatch before it is resolved")
	}
	stateRaw, err := turn.backend.InitialState()
	if err != nil {
		return nil, err
	}
	var state backendharness.State
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		return nil, err
	}
	sessionID := turn.Session.SessionID
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("harness start state has no effective session identity")
	}
	if run.SessionID != "" && run.SessionID != sessionID {
		return nil, errors.New("harness start state uses a different session than the run")
	}
	taskID := turn.Session.TaskID
	if taskID == "" {
		return nil, errors.New("harness start state has no durable task identity")
	}
	agentUID, err := harnessAgentUIDForTask(run.Agent, sessionID, taskID)
	if err != nil {
		return nil, err
	}
	if state.TaskID != taskID || state.AgentUID != agentUID {
		return nil, errors.New("harness start state does not match its Agent identity")
	}
	if state.TaskID == "" || state.AttemptID != run.RunID || state.Epoch == 0 || state.BackendKey == "" {
		return nil, errors.New("harness start state is missing its durable cancellation coordinates")
	}
	config := run.Agent.Spec.Harness()
	if config == nil || turn.Service == "" {
		return nil, errors.New("harness start state has no safe runner target")
	}
	target := &harnessCancelTarget{
		ClusterID: run.ClusterID,
		EdgeKind:  config.EdgeRef.Kind,
		EdgeName:  config.EdgeRef.Name,
		Service:   turn.Service,
		RunnerID:  turn.Service,
	}
	if _, _, ok := parkedHarnessRunner(run.ClusterID, run.Agent, target, state.BackendKey); !ok {
		return nil, errors.New("harness start state does not match its resolved runner target")
	}
	checkpoint := runCheckpoint{
		Backend: agentsv1alpha1.AgentBackendHarness, Harness: stateRaw, HarnessRunner: target,
		SourceName: run.SourceName, NotifyChannel: run.NotifyChannel,
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// stopHarnessFromRun reaches a run's saved target after the process that held
// its executor is gone. The provider's scoped identity is minted from the
// current Agent; no model credential or runner URL is loaded from storage.
func (b *background) stopHarnessFromRun(ctx context.Context, clusterID string, scope store.Scope, run store.Run) error {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backendCancelTimeout)
	defer cancel()
	dyn, err := b.scoped(stopCtx, clusterID)
	if err != nil {
		return err
	}
	client := agentsclient.NewFromScope(tenant.NewScopeFromDynamic(clusterID, dyn))
	return b.server.cancelParkedHarness(stopCtx, client, identity{
		clusterID: clusterID, orgUUID: scope.OrgUUID, workspaceUUID: scope.WorkspaceUUID,
	}, run)
}
