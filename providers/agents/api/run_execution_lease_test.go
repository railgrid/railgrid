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
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/store"
)

func TestAPIStartedRunLeaseFencesStaleCompletion(t *testing.T) {
	entered := make(chan struct{})
	responseDone := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		writeSSE(w, textReply("late answer from the old executor"))
		close(responseDone)
	}))
	defer model.Close()
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	st := store.NewMemoryStore()
	liveRuns := newRunRegistry()
	s := &Server{
		cfg: Config{ProcessID: "api-process-incarnation-a"}, store: st, engine: engine.New(),
		events: newEventBus(), liveRuns: liveRuns,
	}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "researcher"}
	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: scope.AgentName, UID: "agent-incarnation-a"}}
	agent.Spec.Backend.Model = &agentsv1alpha1.AgentModelBackend{Credentials: map[string]string{"chat": "primary"}}
	agent.Spec.Autonomy = agentsv1alpha1.AutonomyAuto
	agent.Spec.Tools.Interactive.Families = []string{"core"}

	admission, err := s.startRun(context.Background(), scope, agent, taskRun{
		SessionID: "lease-test", Task: "wait for the response", Trigger: agentsv1alpha1.RunTriggerAPI,
	}, runAccess{
		Creds: credsFor(model.URL, map[string]string{"primary": "gpt-4o"}), CR: fakeCR{},
		RecoveryIdentity: store.RecoveryIdentityCaller,
	})
	if err != nil {
		t.Fatal(err)
	}
	if admission.Phase != store.RunPhaseRunning {
		t.Fatalf("admitted phase = %q, want Running while the API process owns the lease", admission.Phase)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("API-started run did not reach the blocked model request")
	}

	active, err := st.GetRun(t.Context(), scope, admission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Phase != store.RunPhaseRunning || !executionOwnerBelongsToProcess(active.ExecutionOwner, s.cfg.ProcessID) || active.ExecutionLeaseAt.IsZero() || active.AgentUID != string(agent.UID) || active.RecoveryIdentity != store.RecoveryIdentityCaller {
		t.Fatalf("API-started run is not leased with frozen identity: %+v", active)
	}
	if !liveRuns.has(admission.ID) {
		t.Fatal("API-started run was not registered as live while its model call was blocked")
	}
	if s.finishRun(t.Context(), scope, active.ID, runOutcome{Phase: store.RunPhaseFailed, Message: "stale administrative close"}, time.Now().UTC()) {
		t.Fatal("an unowned administrative close settled a leased run")
	}
	stillActive, err := st.GetRun(t.Context(), scope, admission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillActive.Phase != store.RunPhaseRunning || !executionOwnerBelongsToProcess(stillActive.ExecutionOwner, s.cfg.ProcessID) {
		t.Fatalf("unowned administrative close changed the active lease: phase=%q owner=%q", stillActive.Phase, stillActive.ExecutionOwner)
	}

	newOwner := "api-process-incarnation-b"
	claimed, err := st.ClaimRecovery(t.Context(), scope, active.ID, active.UpdatedAt, newOwner, active.ExecutionLeaseAt.Add(store.RunLeaseDuration))
	if err != nil {
		t.Fatalf("transferring an expired API execution lease: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-responseDone:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked model request did not return after release")
	}
	deadline := time.Now().Add(5 * time.Second)
	for liveRuns.has(admission.ID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if liveRuns.has(admission.ID) {
		t.Fatal("old API executor did not leave its live run registry")
	}

	latest, err := st.GetRun(t.Context(), scope, admission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Phase != store.RunPhaseRunning || latest.ExecutionOwner != newOwner || latest.Attempt != claimed.Attempt {
		t.Fatalf("stale API executor settled or overwrote the recovered epoch: phase=%q owner=%q attempt=%d; claimed owner=%q attempt=%d", latest.Phase, latest.ExecutionOwner, latest.Attempt, newOwner, claimed.Attempt)
	}
	if latest.InputTokens != 0 || latest.OutputTokens != 0 || latest.USDMicros != 0 {
		t.Fatalf("stale API executor recorded run usage after lease transfer: %d/%d tokens, %d USD micros", latest.InputTokens, latest.OutputTokens, latest.USDMicros)
	}
	usage, err := st.GetUsage(t.Context(), scope, agent.Name, time.Now().UTC(), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.USDMicros != 0 {
		t.Fatalf("stale API executor charged the rolling budget after lease transfer: %+v", usage)
	}
	messages, err := st.LoadRecentMessages(t.Context(), scope, "lease-test", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.RunID == admission.ID && message.Metadata["kind"] == "final" {
			t.Fatalf("stale API executor appended a final transcript row after lease transfer: %#v", message)
		}
	}
}
