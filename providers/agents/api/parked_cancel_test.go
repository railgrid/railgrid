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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/identityclient"
	"github.com/railgrid/railgrid/pkg/runner"
	runnerclient "github.com/railgrid/railgrid/pkg/runner/client"
	"github.com/railgrid/railgrid/pkg/runner/dispatch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/internal/edgeref"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant"
)

const parkedCancelAgentToken = "SENSITIVE_AGENT_BEARER_FOR_TEST"

type parkedCancelCall struct {
	request     runner.CancelRequest
	ctxErr      error
	hasDeadline bool
}

type parkedCancelDispatcher struct {
	calls       chan parkedCancelCall
	release     chan struct{}
	once        sync.Once
	failCancels int
}

func (d *parkedCancelDispatcher) Start(context.Context, runner.StartRequest) (runner.Receipt, error) {
	return runner.Receipt{}, errorsNewParkedCancel("unexpected Start")
}

func (d *parkedCancelDispatcher) Resume(context.Context, runner.ResumeRequest) (runner.Receipt, error) {
	return runner.Receipt{}, errorsNewParkedCancel("unexpected Resume")
}

func (d *parkedCancelDispatcher) Cancel(ctx context.Context, req runner.CancelRequest) (runner.Receipt, error) {
	_, deadline := ctx.Deadline()
	d.calls <- parkedCancelCall{request: req, ctxErr: ctx.Err(), hasDeadline: deadline}
	if d.failCancels > 0 {
		d.failCancels--
		return runner.Receipt{}, errors.New("simulated runner cancellation failure")
	}
	select {
	case <-d.release:
		return runner.Receipt{AttemptID: req.AttemptID, AttemptEpoch: req.AttemptEpoch, Phase: runner.PhaseCancelled}, nil
	case <-ctx.Done():
		return runner.Receipt{}, ctx.Err()
	}
}

func (*parkedCancelDispatcher) Inspect(context.Context, string) (runner.Receipt, error) {
	return runner.Receipt{}, errorsNewParkedCancel("unexpected Inspect")
}

func (*parkedCancelDispatcher) Events(context.Context, string, uint64) (dispatch.Stream, error) {
	return parkedCancelStream{}, nil
}

type parkedCancelStream struct{}

func (parkedCancelStream) Next(context.Context) (runner.Event, error) { return runner.Event{}, io.EOF }
func (parkedCancelStream) Close() error                               { return nil }

type parkedCancelFixture struct {
	server       *Server
	dynamic      *dynamicfake.FakeDynamicClient
	dispatcher   *parkedCancelDispatcher
	dialed       chan parkedCancelDial
	scope        store.Scope
	identity     identity
	run          store.Run
	initialAgent *agentsv1alpha1.Agent
	secretGets   *atomic.Int32
}

type parkedCancelDial struct {
	ref   runnerclient.ServiceRef
	token string
}

func newParkedCancelFixture(t *testing.T, initialAgent *agentsv1alpha1.Agent, credential *agentsv1alpha1.ModelCredential) *parkedCancelFixture {
	t.Helper()

	identityServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != identityclient.PathIdentities {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(identityclient.Token{
			Token: parkedCancelAgentToken, TokenType: "Bearer",
			ExpiresAt: time.Now().Add(time.Hour).UTC(), ServiceAccount: "agent", Name: "agent-test",
		})
	}))
	t.Cleanup(identityServer.Close)
	identityClient, err := identityclient.New(identityclient.Options{
		HubURL: identityServer.URL, Provider: providerName, Token: "provider-bootstrap-token",
		HTTPClient: identityServer.Client(),
	})
	if err != nil {
		t.Fatalf("create fake identity client: %v", err)
	}

	objects := []runtime.Object{parkedCancelUnstructured(t, agentsclient.AgentGVR, "Agent", initialAgent)}
	if credential != nil {
		objects = append(objects, parkedCancelUnstructured(t, agentsclient.ModelCredentialGVR, "ModelCredential", credential))
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		agentsclient.AgentGVR:           "AgentList",
		agentsclient.ModelCredentialGVR: "ModelCredentialList",
		agentsclient.ConnectionGVR:      "ConnectionList",
		agentsclient.ToolsetGVR:         "ToolsetList",
	}, objects...)
	secretGets := &atomic.Int32{}
	dyn.PrependReactor("get", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		secretGets.Add(1)
		return true, nil, errors.New("secret read was not expected during cancel")
	})
	dispatcher := &parkedCancelDispatcher{calls: make(chan parkedCancelCall, 2), release: make(chan struct{})}
	t.Cleanup(func() { dispatcher.once.Do(func() { close(dispatcher.release) }) })
	dialed := make(chan parkedCancelDial, 2)
	st := store.NewMemoryStore()
	s := &Server{
		store: st, events: newEventBus(), liveRuns: newRunRegistry(),
		bg: &background{
			identities: newAgentIdentities(identityClient),
			scopedFn:   func(context.Context, string) (dynamic.Interface, error) { return dyn, nil },
		},
		runners: func(_ context.Context, ref runnerclient.ServiceRef, token string) (dispatch.Runner, error) {
			dialed <- parkedCancelDial{ref: ref, token: token}
			return dispatcher, nil
		},
	}
	scope := store.Scope{OrgUUID: "org-test", WorkspaceUUID: "workspace-test", AgentName: initialAgent.Name}
	run := store.Run{
		ID: "run-parked-cancel", AgentName: initialAgent.Name, SessionID: "session-parked-cancel",
		Trigger: "chat", Backend: agentsv1alpha1.AgentBackendHarness, AttemptID: "run-parked-cancel",
		Phase: store.RunPhaseRunning, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.SaveRun(context.Background(), scope, run); err != nil {
		t.Fatalf("save fixture run: %v", err)
	}
	return &parkedCancelFixture{
		server: s, dynamic: dyn, dispatcher: dispatcher, dialed: dialed,
		scope: scope, identity: identity{
			clusterID: "cluster-test", orgUUID: scope.OrgUUID, workspaceUUID: scope.WorkspaceUUID,
		}, run: run, initialAgent: initialAgent, secretGets: secretGets,
	}
}

func parkedCancelUnstructured(t *testing.T, gvr schema.GroupVersionResource, kind string, obj runtime.Object) *unstructured.Unstructured {
	t.Helper()
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatalf("convert %s: %v", kind, err)
	}
	u := &unstructured.Unstructured{Object: content}
	u.SetAPIVersion(gvr.GroupVersion().String())
	u.SetKind(kind)
	return u
}

func (f *parkedCancelFixture) park(t *testing.T, saveRunnerTarget bool) store.Run {
	t.Helper()
	state := backendharness.State{
		TaskID: harnessTaskID(f.run.AgentName, f.run.SessionID), AttemptID: f.run.ID, Epoch: 2,
		BackendKey: harnessBackendKey("cluster-test", edgeref.KindLinuxServer, "edge-test", llm.HarnessAdvertisedName(agentsv1alpha1.ModelProviderCodex)),
		SessionID:  "native-session",
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := runCheckpoint{Backend: agentsv1alpha1.AgentBackendHarness, Harness: stateJSON}
	if saveRunnerTarget {
		checkpoint.HarnessRunner = &harnessCancelTarget{
			ClusterID: "cluster-test", EdgeKind: edgeref.KindLinuxServer, EdgeName: "edge-test",
			Service: "edge-test-codex", RunnerID: "edge-test-codex",
		}
	}
	checkpointJSON, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	f.run.Phase = store.RunPhasePendingApproval
	f.run.Checkpoint = checkpointJSON
	if err := f.server.store.SaveRun(context.Background(), f.scope, f.run); err != nil {
		t.Fatalf("save parked run: %v", err)
	}
	return f.run
}

func (f *parkedCancelFixture) cancel(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsv1alpha1.SchemeGroupVersion.String(), "kind": "Run",
		"metadata": map[string]any{"name": f.run.ID},
		"spec":     map[string]any{"agentRef": f.run.AgentName},
	}}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	ctx := withGate(requestCtx, &gateInfo{provider: f.dynamic, identity: f.identity, object: object})
	req := httptest.NewRequest(http.MethodPost, "/clusters/cluster-test/apis/agents/runs/run-parked-cancel/cancel", nil).WithContext(ctx)
	req.SetPathValue("name", f.run.ID)
	w := httptest.NewRecorder()
	f.server.cancelRun(w, req)
	cancelRequest()
	return w
}

type failingRequestCancelStore struct {
	store.Store
	err error
}

func (s failingRequestCancelStore) RequestCancel(context.Context, store.Scope, string, time.Time) error {
	return s.err
}

func (f *parkedCancelFixture) replaceAgent(t *testing.T, replacement *agentsv1alpha1.Agent) {
	t.Helper()
	if _, err := f.dynamic.Resource(agentsclient.AgentGVR).Update(context.Background(), parkedCancelUnstructured(t, agentsclient.AgentGVR, "Agent", replacement), metav1.UpdateOptions{}); err != nil {
		t.Fatalf("replace current Agent: %v", err)
	}
}

func TestCancelParkedHarnessStopsRemoteAttemptAndLateApprovalCannotRevive(t *testing.T) {
	initial := harnessAgent("edge-test")
	initial.UID = "agent-uid"
	initial.Spec.Backend.Harness.CredentialRef = "credential-removed-after-park"
	f := newParkedCancelFixture(t, initial, nil)
	run := f.park(t, true)
	// A new-format checkpoint is sufficient to find the original runner after
	// the Agent has been changed to a model backend and its credential removed.
	reconfigured := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: initial.Name, UID: initial.UID}}
	f.replaceAgent(t, reconfigured)
	if err := f.server.store.AddInboxItem(context.Background(), store.Scope{OrgUUID: f.scope.OrgUUID, WorkspaceUUID: f.scope.WorkspaceUUID}, store.InboxItem{
		ID: "inbox-parked-cancel", AgentName: run.AgentName, RunID: run.ID, Kind: store.InboxKindApproval,
		State: store.InboxStatePending, Prompt: "Approve harness request",
		Payload:   map[string]any{"tool": "Bash", "args": `{"command":"true"}`},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("add pending inbox item: %v", err)
	}

	f.dispatcher.once.Do(func() { close(f.dispatcher.release) })
	w := f.cancel(t)
	if w.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), parkedCancelAgentToken) {
		t.Fatal("cancel response exposed the runner identity token")
	}
	stored, err := f.server.store.GetRun(context.Background(), f.scope, run.ID)
	if err != nil || stored.Phase != store.RunPhaseAborted {
		t.Fatalf("run after cancel = %+v, %v; want Aborted", stored, err)
	}
	select {
	case dial := <-f.dialed:
		if dial.ref.Cluster != "cluster-test" || dial.ref.Service != "edge-test-codex" || dial.ref.EdgeName != "edge-test" {
			t.Fatalf("runner dial ref = %+v, want the checkpoint's saved edge service", dial.ref)
		}
		if dial.token != parkedCancelAgentToken {
			t.Fatal("runner dial did not use the fresh scoped identity")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parked cancel did not reach the saved runner target")
	}
	select {
	case call := <-f.dispatcher.calls:
		if call.ctxErr != nil || !call.hasDeadline {
			t.Fatalf("remote cancel context err=%v deadline=%t; want detached and bounded", call.ctxErr, call.hasDeadline)
		}
		if call.request.TaskID != harnessTaskID(run.AgentName, run.SessionID) || call.request.AttemptID != run.ID || call.request.AttemptEpoch != 2 {
			t.Fatalf("remote cancel request = %+v, want checkpoint coordinates", call.request)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parked cancel did not send remote Cancel")
	}

	workspaceScope := store.Scope{OrgUUID: f.scope.OrgUUID, WorkspaceUUID: f.scope.WorkspaceUUID}
	item, err := f.server.resolveInboxDecision(context.Background(), workspaceScope, "inbox-parked-cancel", store.InboxStateApproved, "", time.Now().UTC())
	if err != nil {
		t.Fatalf("resolve late approval: %v", err)
	}
	f.server.resumeApprovedRun(workspaceScope, item, runAccess{}, true, "")
	stored, err = f.server.store.GetRun(context.Background(), f.scope, run.ID)
	if err != nil || stored.Phase != store.RunPhaseAborted {
		t.Fatalf("run after late approval = %+v, %v; want it to stay Aborted", stored, err)
	}
	if got := f.secretGets.Load(); got != 0 {
		t.Fatalf("cancel read %d Secret objects; it must use only the scoped runner identity", got)
	}
	select {
	case <-f.dispatcher.calls:
		t.Fatal("late approval dispatched another remote cancellation/resume call")
	default:
	}
}

func TestCancelLegacyParkedHarnessUsesCurrentAgentAndCredentialMetadata(t *testing.T) {
	agent := harnessAgent("edge-test")
	agent.UID = "agent-uid"
	agent.Spec.Backend.Harness.CredentialRef = "codex-credential"
	credential := &agentsv1alpha1.ModelCredential{
		ObjectMeta: metav1.ObjectMeta{Name: "codex-credential"},
		Spec:       agentsv1alpha1.ModelCredentialSpec{Provider: agentsv1alpha1.ModelProviderCodex},
	}
	f := newParkedCancelFixture(t, agent, credential)
	f.park(t, false)
	f.dispatcher.once.Do(func() { close(f.dispatcher.release) })
	w := f.cancel(t)
	if w.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d: %s", w.Code, w.Body.String())
	}
	select {
	case dial := <-f.dialed:
		if dial.ref.Service != "edge-test-codex" || dial.token != parkedCancelAgentToken {
			t.Fatalf("legacy runner dial = %+v token-set=%t", dial.ref, dial.token == parkedCancelAgentToken)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("legacy parked cancel did not resolve its runner from the Agent and ModelCredential")
	}
	select {
	case call := <-f.dispatcher.calls:
		if call.request.AttemptID != f.run.ID || call.request.AttemptEpoch != 2 || call.ctxErr != nil || !call.hasDeadline {
			t.Fatalf("legacy remote cancel = %+v", call)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("legacy parked cancel did not send remote Cancel")
	}
	if got := f.secretGets.Load(); got != 0 {
		t.Fatalf("legacy cancel read %d Secret objects", got)
	}
}

func TestCancelParkedHarnessFailureStaysRetryableUntilRemoteStopSucceeds(t *testing.T) {
	agent := harnessAgent("edge-test")
	agent.UID = "agent-uid"
	f := newParkedCancelFixture(t, agent, nil)
	run := f.park(t, true)
	f.dispatcher.failCancels = 1

	first := f.cancel(t)
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed remote cancel status = %d: %s; want retryable 503", first.Code, first.Body.String())
	}
	stored, err := f.server.store.GetRun(context.Background(), f.scope, run.ID)
	if err != nil || stored.Phase != store.RunPhasePendingApproval || !stored.CancelRequested {
		t.Fatalf("run after failed remote cancel = %+v, %v; want pending approval with durable cancel request", stored, err)
	}

	f.dispatcher.once.Do(func() { close(f.dispatcher.release) })
	second := f.cancel(t)
	if second.Code != http.StatusAccepted {
		t.Fatalf("successful retry status = %d: %s; want 202", second.Code, second.Body.String())
	}
	stored, err = f.server.store.GetRun(context.Background(), f.scope, run.ID)
	if err != nil || stored.Phase != store.RunPhaseAborted {
		t.Fatalf("run after successful remote cancel = %+v, %v; want Aborted", stored, err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-f.dispatcher.calls:
		case <-time.After(time.Second):
			t.Fatalf("remote cancel attempt %d did not reach the dispatcher", i+1)
		}
	}
}

func TestCancelRunReturnsUnavailableWhenCancelFlagCannotBeStored(t *testing.T) {
	agent := harnessAgent("edge-test")
	f := newParkedCancelFixture(t, agent, nil)
	run := f.park(t, true)
	f.server.store = failingRequestCancelStore{Store: f.server.store, err: errors.New("store unavailable")}

	w := f.cancel(t)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("cancel storage failure status = %d: %s; want 503", w.Code, w.Body.String())
	}
	stored, err := f.server.store.GetRun(context.Background(), f.scope, run.ID)
	if err != nil || stored.Phase != store.RunPhasePendingApproval || stored.CancelRequested {
		t.Fatalf("run after cancel storage failure = %+v, %v; want unchanged and not cancelled", stored, err)
	}
	select {
	case call := <-f.dispatcher.calls:
		t.Fatalf("remote cancellation ran before cancel flag was stored: %+v", call.request)
	default:
	}
}

func TestCancelParkedHarnessDoesNotRedirectInvalidSavedTarget(t *testing.T) {
	agent := harnessAgent("edge-test")
	agent.UID = "agent-uid"
	agent.Spec.Backend.Harness.CredentialRef = "codex-credential"
	credential := &agentsv1alpha1.ModelCredential{
		ObjectMeta: metav1.ObjectMeta{Name: "codex-credential"},
		Spec:       agentsv1alpha1.ModelCredentialSpec{Provider: agentsv1alpha1.ModelProviderCodex},
	}
	f := newParkedCancelFixture(t, agent, credential)
	run := f.park(t, true)
	var checkpoint runCheckpoint
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint.HarnessRunner.EdgeName = "different-edge"
	run.Checkpoint, _ = json.Marshal(checkpoint)
	if err := f.server.store.SaveRun(context.Background(), f.scope, run); err != nil {
		t.Fatal(err)
	}

	client := agentsclient.NewFromScope(tenant.NewScopeFromDynamic("cluster-test", f.dynamic))
	err := f.server.cancelParkedHarness(context.Background(), client, f.identity, run)
	if err == nil {
		t.Fatal("cancel accepted a saved runner target that did not match the checkpoint")
	}
	select {
	case dial := <-f.dialed:
		t.Fatalf("invalid saved target redirected cancellation to %+v", dial.ref)
	default:
	}
	select {
	case call := <-f.dispatcher.calls:
		t.Fatalf("invalid saved target sent remote cancellation %+v", call.request)
	default:
	}
	if got := f.secretGets.Load(); got != 0 {
		t.Fatalf("invalid saved target fallback read %d Secret objects", got)
	}
}

func TestParkRunPersistsHarnessCancelTargetWithoutCredentials(t *testing.T) {
	agent := harnessAgent("edge-test")
	agent.UID = "agent-uid"
	f := newParkedCancelFixture(t, agent, nil)
	state := backendharness.State{TaskID: harnessTaskID(agent.Name, f.run.SessionID), AttemptID: f.run.ID, Epoch: 1}
	stateJSON, _ := json.Marshal(state)
	tracker := newTurnProgressTracker(0)
	_, err := f.server.parkRun(context.Background(), taskRun{
		Scope: f.scope, Agent: agent, RunID: f.run.ID, SessionID: f.run.SessionID, ClusterID: "cluster-test",
	}, f.run.SessionID, time.Now().UTC(), time.Now().UTC(), tracker,
		&harnessTurn{Service: "edge-test-codex", backend: backendharness.New(backendharness.Config{})},
		backendOutcomeParked(stateJSON))
	if err != nil {
		t.Fatalf("parkRun: %v", err)
	}
	stored, err := f.server.store.GetRun(context.Background(), f.scope, f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint runCheckpoint
	if err := json.Unmarshal(stored.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	if checkpoint.HarnessRunner == nil || checkpoint.HarnessRunner.ClusterID != "cluster-test" || checkpoint.HarnessRunner.Service != "edge-test-codex" || checkpoint.HarnessRunner.RunnerID != checkpoint.HarnessRunner.Service {
		t.Fatalf("persisted harness cancellation target = %+v", checkpoint.HarnessRunner)
	}
	if strings.Contains(string(stored.Checkpoint), parkedCancelAgentToken) {
		t.Fatal("parked checkpoint contains an identity token")
	}
}

func backendOutcomeParked(state []byte) backend.Outcome {
	return backend.Outcome{Parked: &backend.Parked{
		RequestID: "inbox-parked-cancel", Tool: "Bash", Args: `{"command":"true"}`, State: state,
	}}
}

var _ dispatch.Runner = (*parkedCancelDispatcher)(nil)
var _ dynamic.Interface = (dynamic.Interface)(nil)

func errorsNewParkedCancel(message string) error { return errors.New(message) }
