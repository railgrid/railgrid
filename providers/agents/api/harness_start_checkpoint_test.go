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
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/dispatch"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/internal/edgeref"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

func TestInitialHarnessCheckpointContainsOnlySafeCancellationCoordinates(t *testing.T) {
	agent := harnessAgent("edge-test")
	run := taskRun{
		Scope: store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: agent.Name},
		Agent: agent, RunID: "run-start-checkpoint", SessionID: "chat", ClusterID: "cluster-test",
		Trigger: "chat", SourceName: "schedule-source", NotifyChannel: "primary",
	}
	turn := startCheckpointHarnessTurn(run, 3, "native-session")
	checkpointRaw, err := initialHarnessCheckpoint(run, turn)
	if err != nil {
		t.Fatalf("initialHarnessCheckpoint: %v", err)
	}
	var checkpoint runCheckpoint
	if err := json.Unmarshal(checkpointRaw, &checkpoint); err != nil {
		t.Fatalf("decode run checkpoint: %v", err)
	}
	var state backendharness.State
	if err := json.Unmarshal(checkpoint.Harness, &state); err != nil {
		t.Fatalf("decode harness state: %v", err)
	}
	if checkpoint.Backend != agentsv1alpha1.AgentBackendHarness || checkpoint.SourceName != run.SourceName || checkpoint.NotifyChannel != run.NotifyChannel {
		t.Fatalf("initial run checkpoint metadata = %+v", checkpoint)
	}
	if state.TaskID != harnessTaskID(agent.Name, run.SessionID) || state.AttemptID != run.RunID || state.Epoch != 3 || state.SessionID != "native-session" || state.BackendKey == "" {
		t.Fatalf("initial harness coordinates = %+v", state)
	}
	if state.Snapshot != nil {
		t.Fatalf("initial harness state unexpectedly contains a resume snapshot: %s", state.Snapshot)
	}
	wantTarget := &harnessCancelTarget{
		ClusterID: "cluster-test", EdgeKind: edgeref.KindLinuxServer, EdgeName: "edge-test",
		Service: "edge-test-codex", RunnerID: "edge-test-codex",
	}
	if checkpoint.HarnessRunner == nil || *checkpoint.HarnessRunner != *wantTarget {
		t.Fatalf("safe runner target = %+v, want %+v", checkpoint.HarnessRunner, wantTarget)
	}
	if strings.Contains(string(checkpointRaw), "harness-login-test-secret") || strings.Contains(string(checkpointRaw), "runner-bearer-test-secret") {
		t.Fatal("initial checkpoint contains credential material")
	}
}

func TestCheckpointRecorderRetainsInitialHarnessCancellationTarget(t *testing.T) {
	ctx := context.Background()
	storeImpl := store.NewMemoryStore()
	agent := harnessAgent("edge-test")
	run := taskRun{
		Scope: store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: agent.Name},
		Agent: agent, RunID: "run-checkpoint-preserve", SessionID: "chat", ClusterID: "cluster-test",
		Trigger: "chat",
	}
	initial, err := initialHarnessCheckpoint(run, startCheckpointHarnessTurn(run, 4, "native-session"))
	if err != nil {
		t.Fatalf("initialHarnessCheckpoint: %v", err)
	}
	stored := store.Run{
		ID: run.RunID, AgentName: agent.Name, SessionID: run.SessionID, Trigger: run.Trigger,
		Backend: agentsv1alpha1.AgentBackendHarness, AttemptID: run.RunID,
		Phase: store.RunPhaseRunning, Checkpoint: initial, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := storeImpl.SaveRun(ctx, run.Scope, stored); err != nil {
		t.Fatal(err)
	}
	nextState, err := json.Marshal(backendharness.State{
		TaskID: harnessTaskID(agent.Name, run.SessionID), AttemptID: run.RunID,
		Epoch: 4, SessionID: "native-session", BackendKey: harnessBackendKey("cluster-test", edgeref.KindLinuxServer, "edge-test", llm.HarnessAdvertisedName(agentsv1alpha1.ModelProviderCodex)),
		Cursor: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: storeImpl}
	s.checkpointRecorder(ctx, run, run.SessionID, agentsv1alpha1.AgentBackendHarness)(nextState)
	updated, err := storeImpl.GetRun(ctx, run.Scope, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint runCheckpoint
	if err := json.Unmarshal(updated.Checkpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.HarnessRunner == nil || checkpoint.HarnessRunner.Service != "edge-test-codex" {
		t.Fatalf("event checkpoint dropped the safe cancel target: %+v", checkpoint.HarnessRunner)
	}
	var state backendharness.State
	if err := json.Unmarshal(checkpoint.Harness, &state); err != nil || state.Cursor != 1 {
		t.Fatalf("event checkpoint state = %+v, err=%v", state, err)
	}
}

func TestRunTurnDoesNotDispatchWhenInitialHarnessRecordCannotBeSaved(t *testing.T) {
	dispatcher := &startCountingDispatcher{}
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 60)
	if err := s.store.SaveRun(t.Context(), run.Scope, store.Run{
		ID: run.RunID, AgentName: run.Agent.Name, SessionID: run.SessionID,
		Trigger: run.Trigger, Phase: store.RunPhasePending, Input: run.Task,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("simulated durable write failure")
	s.store = failPhaseSaveStore{Store: s.store, phase: store.RunPhaseRunning, err: failure}

	result, err := s.runTurn(t.Context(), run, nil)
	if !errors.Is(err, failure) || result.Phase != store.RunPhaseFailed {
		t.Fatalf("runTurn = %+v, %v; want durable setup failure", result, err)
	}
	if dispatcher.starts != 0 {
		t.Fatalf("runner Start called %d times after its initial cancellation checkpoint failed to persist", dispatcher.starts)
	}
	stored, getErr := s.store.GetRun(t.Context(), run.Scope, run.RunID)
	if getErr != nil || stored.Phase != store.RunPhaseFailed {
		t.Fatalf("run after failed start checkpoint = %+v, %v", stored, getErr)
	}
}

func TestRunDeadlineStopCancelsFromInitialHarnessCheckpoint(t *testing.T) {
	agent := harnessAgent("edge-test")
	agent.UID = "agent-uid"
	f := newParkedCancelFixture(t, agent, nil)
	run := taskRun{
		Scope: f.scope, Agent: agent, RunID: f.run.ID, SessionID: f.run.SessionID,
		ClusterID: "cluster-test", Trigger: "chat",
	}
	checkpoint, err := initialHarnessCheckpoint(run, startCheckpointHarnessTurn(run, 2, ""))
	if err != nil {
		t.Fatalf("initialHarnessCheckpoint: %v", err)
	}
	f.run.Backend = agentsv1alpha1.AgentBackendHarness
	f.run.Checkpoint = checkpoint
	if err := f.server.store.SaveRun(context.Background(), f.scope, f.run); err != nil {
		t.Fatal(err)
	}
	if err := f.server.store.SaveTenantRef(context.Background(), "cluster-test", store.TenantRef{
		OrgUUID: f.scope.OrgUUID, WorkspaceUUID: f.scope.WorkspaceUUID,
	}); err != nil {
		t.Fatal(err)
	}
	f.server.bg.server = f.server

	result := make(chan error, 1)
	go func() {
		result <- f.server.bg.StopRun(context.Background(), "cluster-test", f.run.AgentName, f.run.ID, "deadline exceeded")
	}()
	select {
	case call := <-f.dispatcher.calls:
		if call.request.TaskID != harnessTaskID(f.run.AgentName, f.run.SessionID) || call.request.AttemptID != f.run.ID || call.request.AttemptEpoch != 2 {
			t.Fatalf("StopRun cancellation = %+v; want initial checkpoint coordinates", call.request)
		}
		if call.ctxErr != nil || !call.hasDeadline {
			t.Fatalf("StopRun context err=%v deadline=%t", call.ctxErr, call.hasDeadline)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopRun did not cancel the remote harness from its initial checkpoint")
	}
	stored, err := f.server.store.GetRun(context.Background(), f.scope, f.run.ID)
	if err != nil || stored.Phase != store.RunPhaseRunning || !stored.CancelRequested {
		t.Fatalf("run while remote cancellation is pending = %+v, %v; StopRun must remain retryable", stored, err)
	}
	f.dispatcher.once.Do(func() { close(f.dispatcher.release) })
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("StopRun: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopRun did not finish after the runner accepted cancellation")
	}
	stored, err = f.server.store.GetRun(context.Background(), f.scope, f.run.ID)
	if err != nil || stored.Phase != store.RunPhaseAborted {
		t.Fatalf("run after StopRun = %+v, %v; want Aborted", stored, err)
	}
	if got := f.secretGets.Load(); got != 0 {
		t.Fatalf("StopRun read %d Secrets; remote cancellation must not load a harness credential", got)
	}
}

func TestRunDeadlineStopLeavesRunRetryableWhenRemoteCancelFails(t *testing.T) {
	agent := harnessAgent("edge-test")
	f := newParkedCancelFixture(t, agent, nil)
	f.dispatcher.failCancels = 1
	run := taskRun{
		Scope: f.scope, Agent: agent, RunID: f.run.ID, SessionID: f.run.SessionID,
		ClusterID: "cluster-test", Trigger: "chat",
	}
	checkpoint, err := initialHarnessCheckpoint(run, startCheckpointHarnessTurn(run, 2, ""))
	if err != nil {
		t.Fatalf("initialHarnessCheckpoint: %v", err)
	}
	f.run.Backend = agentsv1alpha1.AgentBackendHarness
	f.run.Checkpoint = checkpoint
	if err := f.server.store.SaveRun(context.Background(), f.scope, f.run); err != nil {
		t.Fatal(err)
	}
	if err := f.server.store.SaveTenantRef(context.Background(), "cluster-test", store.TenantRef{
		OrgUUID: f.scope.OrgUUID, WorkspaceUUID: f.scope.WorkspaceUUID,
	}); err != nil {
		t.Fatal(err)
	}
	f.server.bg.server = f.server

	err = f.server.bg.StopRun(context.Background(), "cluster-test", f.run.AgentName, f.run.ID, "deadline exceeded")
	if err == nil || strings.Contains(err.Error(), "simulated runner cancellation failure") {
		t.Fatalf("StopRun error = %v; want a generic retryable failure", err)
	}
	if _, ok := <-f.dispatcher.calls; !ok {
		t.Fatal("failed remote cancel was not attempted")
	}
	stored, err := f.server.store.GetRun(context.Background(), f.scope, f.run.ID)
	if err != nil || stored.Phase != store.RunPhaseRunning || !stored.CancelRequested {
		t.Fatalf("run after failed remote cancel = %+v, %v; it must stay retryable", stored, err)
	}

	result := make(chan error, 1)
	go func() {
		result <- f.server.bg.StopRun(context.Background(), "cluster-test", f.run.AgentName, f.run.ID, "deadline exceeded")
	}()
	select {
	case <-f.dispatcher.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("retry did not reach the remote runner")
	}
	f.dispatcher.once.Do(func() { close(f.dispatcher.release) })
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("StopRun retry: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopRun retry did not finish")
	}
	stored, err = f.server.store.GetRun(context.Background(), f.scope, f.run.ID)
	if err != nil || stored.Phase != store.RunPhaseAborted {
		t.Fatalf("run after successful retry = %+v, %v", stored, err)
	}
}

func startCheckpointHarnessTurn(run taskRun, epoch uint64, session string) *harnessTurn {
	config := run.Agent.Spec.Harness()
	backendKey := harnessBackendKey(run.ClusterID, config.EdgeRef.Kind, config.EdgeRef.Name, llm.HarnessAdvertisedName(agentsv1alpha1.ModelProviderCodex))
	service := "edge-test-codex"
	return &harnessTurn{
		backend: backendharness.New(backendharness.Config{
			Runner: &parkedCancelDispatcher{}, TaskID: harnessTaskID(run.Agent.Name, run.SessionID),
			AttemptID: run.RunID, Epoch: epoch, SessionID: session, BackendKey: backendKey,
			WorkspaceID: "agent-coder-chat", RequiredHarness: llm.HarnessAdvertisedName(agentsv1alpha1.ModelProviderCodex),
			Credential: llm.HarnessIdentity{Kind: "codex-auth", Value: "harness-login-test-secret"},
		}),
		Session: store.HarnessSession{SessionID: run.SessionID, Turns: int64(epoch), HarnessSessionID: session, BackendKey: backendKey},
		Service: service, Harness: llm.HarnessAdvertisedName(agentsv1alpha1.ModelProviderCodex),
	}
}

type failPhaseSaveStore struct {
	store.Store
	phase store.RunPhase
	err   error
}

func (s failPhaseSaveStore) SaveRun(ctx context.Context, scope store.Scope, run store.Run) error {
	if run.Phase == s.phase {
		return s.err
	}
	return s.Store.SaveRun(ctx, scope, run)
}

type startCountingDispatcher struct {
	dispatch.Runner
	starts int
}

func (d *startCountingDispatcher) Start(_ context.Context, req runner.StartRequest) (runner.Receipt, error) {
	d.starts++
	return runner.Receipt{AttemptID: req.AttemptID, AttemptEpoch: req.AttemptEpoch, Phase: runner.PhaseRunning}, errors.New("unexpected Runner.Start")
}

var _ backend.Backend = (*backendharness.Backend)(nil)
