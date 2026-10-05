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
	"context"
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/store"
)

func TestHarnessRunnerIdentityIsScopedToAgentIncarnation(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	first := harnessIdentityAgent("agent-uid-first")
	recreated := harnessIdentityAgent("agent-uid-recreated")

	firstSession, err := s.harnessSessionFor(ctx, first, scope, "chat", "backend", nil)
	if err != nil {
		t.Fatal(err)
	}
	firstTask := firstSession.TaskID
	firstWorkspace := harnessWorkspaceIDForTask(first, "run-1", firstTask)
	if firstTask != harnessTaskIDFor(first.Name, string(first.UID), "chat") {
		t.Fatalf("new session task ID = %q, want UID-scoped identity", firstTask)
	}

	continued, err := s.harnessSessionFor(ctx, first, scope, "chat", "backend", nil)
	if err != nil {
		t.Fatal(err)
	}
	if continued.TaskID != firstTask {
		t.Fatalf("later turn task ID = %q, want durable marker %q", continued.TaskID, firstTask)
	}
	if got := harnessWorkspaceIDForTask(first, "run-2", continued.TaskID); got != firstWorkspace {
		t.Fatalf("later turn workspace = %q, want persistent workspace %q", got, firstWorkspace)
	}

	// Deleting the Agent purges its session marker. Recreating the same name
	// starts a new runner task and workspace for the new UID.
	if err := st.DeleteAgentData(ctx, scope, first.Name); err != nil {
		t.Fatal(err)
	}
	recreatedSession, err := s.harnessSessionFor(ctx, recreated, scope, "chat", "backend", nil)
	if err != nil {
		t.Fatal(err)
	}
	recreatedWorkspace := harnessWorkspaceIDForTask(recreated, "run-1", recreatedSession.TaskID)
	if firstTask == recreatedSession.TaskID || firstWorkspace == recreatedWorkspace {
		t.Fatalf("recreated Agent reused prior task/workspace: task %q/%q workspace %q/%q", recreatedSession.TaskID, firstTask, recreatedWorkspace, firstWorkspace)
	}
}

func TestUIDTaskIdentityDisambiguatesSanitizedSessionIDs(t *testing.T) {
	left := harnessTaskIDFor("coder", "agent-uid", "a/b")
	right := harnessTaskIDFor("coder", "agent-uid", "a-b")
	if left == right {
		t.Fatalf("distinct raw session IDs collapsed to task %q", left)
	}
	if len(left) > protocolIdentifierMax || len(right) > protocolIdentifierMax {
		t.Fatalf("task IDs exceed runner limit: %d / %d", len(left), len(right))
	}
	legacy := harnessTaskID("coder", "a/b")
	if legacy != protocolIdentifier("agent-coder-a/b") {
		t.Fatalf("legacy identity changed from its pre-upgrade format: %q", legacy)
	}
}

func TestLegacyHarnessSessionKeepsNativeAndWorkspaceIdentityAcrossUpgrade(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	agent := harnessIdentityAgent("agent-uid-current")
	agent.CreationTimestamp = metav1.NewTime(time.Now().UTC().Add(-3 * time.Second).Truncate(time.Second))
	legacyTask := harnessTaskID(agent.Name, "chat")
	legacyRow, err := st.NextHarnessTurn(ctx, scope, "chat", time.Now(), legacyHarnessIdentityFor(scope, "chat"))
	if err != nil {
		t.Fatal(err)
	}
	legacyRow.Turns = 1
	legacyRow.HarnessSessionID = "native-thread"
	legacyRow.BackendKey = "backend"
	legacyRow.ObservedEpoch = 1
	legacyRow.UpdatedAt = time.Now().UTC()
	if err := st.PutHarnessSession(ctx, scope, legacyRow); err != nil {
		t.Fatal(err)
	}

	first, err := s.harnessSessionFor(ctx, agent, scope, "chat", "backend", nil)
	if err != nil || first.TaskID != legacyTask || first.HarnessSessionID != "native-thread" {
		t.Fatalf("first post-upgrade turn = %+v, err=%v; want legacy task and native session", first, err)
	}
	if got := harnessWorkspaceIDForTask(agent, "run-1", first.TaskID); got != legacyTask {
		t.Fatalf("post-upgrade workspace = %q, want existing legacy directory %q", got, legacyTask)
	}
	next, err := s.harnessSessionFor(ctx, agent, scope, "chat", "backend", nil)
	if err != nil || next.TaskID != legacyTask || next.HarnessSessionID != "native-thread" {
		t.Fatalf("later post-upgrade turn = %+v, err=%v; want same task/native session", next, err)
	}
}

func TestRecreatedAgentResetsStaleUnpurgedLegacySession(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	old := harnessIdentityAgent("agent-uid-old")
	created := time.Now().UTC()
	newAgent := harnessIdentityAgent("agent-uid-new")
	newAgent.CreationTimestamp = metav1.NewTime(created)
	oldActivity := created.Add(-time.Minute)
	if err := st.PutHarnessSession(ctx, scope, store.HarnessSession{
		SessionID: "chat", TaskID: harnessTaskID(old.Name, "chat"), HarnessSessionID: "old-native-thread",
		BackendKey: "old-backend", Turns: 9, ObservedEpoch: 9, UpdatedAt: oldActivity,
	}); err != nil {
		t.Fatal(err)
	}

	turn, err := s.harnessSessionFor(ctx, newAgent, scope, "chat", "new-backend", nil)
	if err != nil {
		t.Fatal(err)
	}
	wantTask := harnessTaskIDFor(newAgent.Name, string(newAgent.UID), "chat")
	if turn.TaskID != wantTask || turn.Turns != 1 || turn.HarnessSessionID != "" {
		t.Fatalf("recreated Agent reused stale legacy state: %+v; want fresh task %q at epoch 1", turn, wantTask)
	}
	workspace := harnessWorkspaceIDForTask(newAgent, "run-new", turn.TaskID)
	if workspace == harnessTaskID(old.Name, "chat") {
		t.Fatalf("recreated Agent reused the old persistent directory %q", workspace)
	}
}

func TestLegacyHarnessCheckpointResumeRequiresDurableSessionMarker(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	agent := harnessIdentityAgent("agent-uid-current")
	agent.CreationTimestamp = metav1.NewTime(time.Now().UTC().Add(-3 * time.Second).Truncate(time.Second))
	legacyTask := harnessTaskID(agent.Name, "chat")
	legacy := harnessContinuation(t, backendharness.State{
		TaskID: legacyTask, AttemptID: "run-legacy", Epoch: 1, SessionID: "native-thread",
	})
	row, err := st.NextHarnessTurn(ctx, scope, "chat", time.Now(), legacyHarnessIdentityFor(scope, "chat"))
	if err != nil {
		t.Fatal(err)
	}
	row.Turns = 1
	row.HarnessSessionID = "native-thread"
	row.BackendKey = "backend"
	row.ObservedEpoch = 1
	row.UpdatedAt = time.Now().UTC()
	if err := st.PutHarnessSession(ctx, scope, row); err != nil {
		t.Fatal(err)
	}

	resumed, err := s.harnessSessionFor(ctx, agent, scope, "chat", "backend", legacy)
	if err != nil || resumed.TaskID != legacyTask || resumed.HarnessSessionID != "native-thread" {
		t.Fatalf("verified legacy resume = %+v, err=%v", resumed, err)
	}
	marked, ok, err := st.GetHarnessSession(ctx, scope, "chat")
	if err != nil || !ok || marked.TaskID != legacyTask {
		t.Fatalf("legacy resume did not persist task marker: %+v, ok=%v err=%v", marked, ok, err)
	}

	// Agent purge removes the identity marker. A same-name replacement cannot
	// claim an old legacy checkpoint because its incarnation is unknowable.
	if err := st.DeleteAgentData(ctx, scope, agent.Name); err != nil {
		t.Fatal(err)
	}
	recreated := harnessIdentityAgent("agent-uid-recreated")
	if _, err := s.harnessSessionFor(ctx, recreated, scope, "chat", "backend", legacy); err == nil {
		t.Fatal("same-name recreated Agent resumed a legacy checkpoint after purge")
	}
}

func TestLegacyHarnessCheckpointRejectsAmbiguousCreationSecond(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	createdAt := time.Now().UTC().Truncate(time.Second)
	agent := harnessIdentityAgent("agent-uid-current")
	agent.CreationTimestamp = metav1.NewTime(time.Now().UTC().Add(-3 * time.Second).Truncate(time.Second))
	agent.CreationTimestamp = metav1.NewTime(createdAt)
	legacyTask := harnessTaskID(agent.Name, "chat")
	row := store.HarnessSession{
		SessionID: "chat", TaskID: legacyTask, HarnessSessionID: "native-thread", BackendKey: "backend",
		Turns: 1, ObservedEpoch: 1, UpdatedAt: createdAt.Add(500 * time.Millisecond),
	}
	if err := st.PutHarnessSession(ctx, scope, row); err != nil {
		t.Fatal(err)
	}
	legacy := harnessContinuation(t, backendharness.State{
		TaskID: legacyTask, AttemptID: "run-legacy", Epoch: 1, SessionID: "native-thread",
	})
	if _, err := s.harnessSessionFor(ctx, agent, scope, "chat", "backend", legacy); err == nil {
		t.Fatal("resumed a legacy checkpoint whose ownership is ambiguous at CreationTimestamp precision")
	}
}

func TestHarnessResumeFencesDifferentIncarnationsAndTaskMarkers(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	s := &Server{store: st}
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "coder"}
	agent := harnessIdentityAgent("agent-uid-current")
	currentTask := harnessTaskIDFor(agent.Name, string(agent.UID), "chat")
	current := harnessContinuation(t, backendharness.State{
		TaskID: currentTask, AgentUID: string(agent.UID), AttemptID: "run-current", Epoch: 2, SessionID: "native-thread",
	})
	row, err := s.harnessSessionFor(ctx, agent, scope, "chat", "backend", nil)
	if err != nil {
		t.Fatal(err)
	}
	row.TaskID = currentTask
	if err := st.PutHarnessSession(ctx, scope, row); err != nil {
		t.Fatal(err)
	}
	resumed, err := s.harnessSessionFor(ctx, agent, scope, "chat", "backend", current)
	if err != nil || resumed.TaskID != currentTask {
		t.Fatalf("current-incarnation resume = %+v, err=%v", resumed, err)
	}
	legacyOnCurrent := harnessContinuation(t, backendharness.State{
		TaskID: harnessTaskID(agent.Name, "legacy-chat"), AgentUID: string(agent.UID),
		AttemptID: "run-legacy-current", Epoch: 1,
	})
	legacyTaskID, ownerUID, err := harnessTaskIdentity(agent, "legacy-chat", legacyOnCurrent)
	if err != nil || legacyTaskID != harnessTaskID(agent.Name, "legacy-chat") || ownerUID != string(agent.UID) {
		t.Fatalf("current-owner legacy task identity = %q/%q, err=%v", legacyTaskID, ownerUID, err)
	}

	stale := harnessContinuation(t, backendharness.State{
		TaskID:   harnessTaskIDFor(agent.Name, "agent-uid-previous", "chat"),
		AgentUID: "agent-uid-previous", AttemptID: "run-previous", Epoch: 1,
	})
	if _, _, err := harnessTaskIdentity(agent, "chat", stale); err == nil {
		t.Fatal("resume accepted a checkpoint belonging to a different Agent incarnation")
	}
	malformedLegacy := harnessContinuation(t, backendharness.State{
		TaskID: "agent-some-other-session", AttemptID: "run-wrong", Epoch: 1,
	})
	if _, _, err := harnessTaskIdentity(agent, "chat", malformedLegacy); err == nil {
		t.Fatal("resume accepted a legacy task ID that does not match this Agent session")
	}

	wrongMarker := harnessContinuation(t, backendharness.State{
		TaskID: harnessTaskID(agent.Name, "chat"), AttemptID: "run-old", Epoch: 1,
	})
	if _, err := s.harnessSessionFor(ctx, agent, scope, "chat", "backend", wrongMarker); err == nil {
		t.Fatal("resume accepted a checkpoint whose task ID differs from the durable session marker")
	}
}

func harnessIdentityAgent(uid string) *agentsv1alpha1.Agent {
	agent := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "coder", UID: types.UID(uid), CreationTimestamp: metav1.Now()}}
	agent.Spec.Backend = agentsv1alpha1.AgentBackendSpec{Type: agentsv1alpha1.AgentBackendHarness}
	return agent
}

func harnessContinuation(t *testing.T, state backendharness.State) *continuation {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return &continuation{Checkpoint: runCheckpoint{Backend: agentsv1alpha1.AgentBackendHarness, Harness: raw}}
}
