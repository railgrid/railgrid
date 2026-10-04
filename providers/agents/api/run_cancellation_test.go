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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/railgrid/provider-sdk/identityclient"

	"github.com/railgrid/railgrid/pkg/runner"
	runnerclient "github.com/railgrid/railgrid/pkg/runner/client"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/store"
)

func TestRunTurnStopsRemoteHarnessWhenCallerCancels(t *testing.T) {
	dispatcher := newBlockingRunLifecycleDispatcher()
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runHarnessTurnAsync(ctx, s, run)
	awaitRunLifecycleSignal(t, dispatcher.eventsOpened, "the harness event stream to open")

	cancel()
	call := awaitRunLifecycleCancel(t, dispatcher)
	assertDetachedRunLifecycleCancel(t, call, run.RunID)

	// Keep the local follower inside Events until the watcher has asked the
	// remote runner to stop. This proves cancellation does not depend on the
	// backend returning first.
	close(dispatcher.releaseStream)
	result := awaitRunLifecycleTurn(t, done)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("runTurn error = %v, want context cancellation", result.err)
	}
	if result.result.Phase != store.RunPhaseAborted {
		t.Fatalf("run phase = %q, want %q", result.result.Phase, store.RunPhaseAborted)
	}

	close(dispatcher.releaseCancel)
	awaitRunLifecycleSignal(t, dispatcher.cancelReturned, "the remote cancel request to return")
	if got := dispatcher.cancelCalls.Load(); got != 1 {
		t.Fatalf("remote cancel calls = %d, want exactly one", got)
	}
}

func TestRunTurnStopsRemoteHarnessWhenRunDeadlineExpires(t *testing.T) {
	dispatcher := newBlockingRunLifecycleDispatcher()
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 1)

	done := runHarnessTurnAsync(context.Background(), s, run)
	awaitRunLifecycleSignal(t, dispatcher.eventsOpened, "the harness event stream to open")
	call := awaitRunLifecycleCancel(t, dispatcher)
	assertDetachedRunLifecycleCancel(t, call, run.RunID)

	close(dispatcher.releaseStream)
	result := awaitRunLifecycleTurn(t, done)
	if !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatalf("runTurn error = %v, want the run deadline", result.err)
	}
	if result.result.Phase != store.RunPhaseAborted {
		t.Fatalf("run phase = %q, want %q", result.result.Phase, store.RunPhaseAborted)
	}

	close(dispatcher.releaseCancel)
	awaitRunLifecycleSignal(t, dispatcher.cancelReturned, "the remote cancel request to return")
	if got := dispatcher.cancelCalls.Load(); got != 1 {
		t.Fatalf("remote cancel calls = %d, want exactly one", got)
	}
}

func TestRunTurnDoesNotCancelHarnessAfterPermissionPark(t *testing.T) {
	dispatcher := &parkedRunLifecycleDispatcher{
		cancelCalls: make(chan runLifecycleCancelCall, 1),
		receipt: runner.Receipt{
			AttemptID: "run-parked", AttemptEpoch: 1, SessionID: "thread",
			Phase:      runner.PhaseNeedsInput,
			Permission: &runner.PermissionRequest{ID: "permission-1", Tool: "Bash", Input: `{"command":"true"}`},
		},
	}
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 0)

	result, err := s.runTurn(context.Background(), run, nil)
	if err != nil {
		t.Fatalf("runTurn: %v", err)
	}
	if result.Pending == nil || result.Pending.Kind != string(store.InboxKindApproval) {
		t.Fatalf("run result pending = %+v, want the parked permission approval", result.Pending)
	}
	// runTurn's deferred cleanup cancels its own context after a successful park.
	// That cleanup must not revoke the harness's still-live permission request.
	select {
	case call := <-dispatcher.cancelCalls:
		t.Fatalf("successful permission park called remote Cancel: %+v", call)
	case <-time.After(100 * time.Millisecond):
	}
}

type runLifecycleTurnResult struct {
	result runResult
	err    error
}

func runHarnessTurnAsync(ctx context.Context, s *Server, run taskRun) <-chan runLifecycleTurnResult {
	done := make(chan runLifecycleTurnResult, 1)
	go func() {
		result, err := s.runTurn(ctx, run, nil)
		done <- runLifecycleTurnResult{result: result, err: err}
	}()
	return done
}

func awaitRunLifecycleSignal(t *testing.T, ch <-chan struct{}, want string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", want)
	}
}

func awaitRunLifecycleCancel(t *testing.T, dispatcher *blockingRunLifecycleDispatcher) runLifecycleCancelCall {
	t.Helper()
	select {
	case call := <-dispatcher.cancelEntered:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for remote runner cancellation")
		return runLifecycleCancelCall{}
	}
}

func awaitRunLifecycleTurn(t *testing.T, done <-chan runLifecycleTurnResult) runLifecycleTurnResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("runTurn did not finish after its event stream was released")
		return runLifecycleTurnResult{}
	}
}

func assertDetachedRunLifecycleCancel(t *testing.T, call runLifecycleCancelCall, runID string) {
	t.Helper()
	if call.ctxErr != nil {
		t.Errorf("remote cancel context error = %v, want a live detached context", call.ctxErr)
	}
	if !call.hasDeadline {
		t.Error("remote cancel context has no timeout deadline")
	}
	if call.request.AttemptID != runID || call.request.AttemptEpoch != 1 {
		t.Errorf("remote cancel request = %+v, want attempt %q epoch 1", call.request, runID)
	}
}

func newHarnessRunLifecycleFixture(t *testing.T, dispatcher backendharness.Dispatcher, timeoutSeconds int32) (*Server, taskRun) {
	t.Helper()

	identityServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != identityclient.PathIdentities {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(identityclient.Token{
			Token: "agent-runner-token", TokenType: "Bearer",
			ExpiresAt: time.Now().Add(time.Hour).UTC(), ServiceAccount: "agent", Name: "agent-test",
		})
	}))
	t.Cleanup(identityServer.Close)

	identityClient, err := identityclient.New(identityclient.Options{
		HubURL: identityServer.URL, Provider: providerName, Token: "provider-token",
		HTTPClient: identityServer.Client(),
	})
	if err != nil {
		t.Fatalf("create fake identity client: %v", err)
	}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	s := &Server{
		store:    store.NewMemoryStore(),
		bg:       &background{identities: newAgentIdentities(identityClient), scopedFn: func(context.Context, string) (dynamic.Interface, error) { return dyn, nil }},
		liveRuns: newRunRegistry(),
		events:   newEventBus(),
		runners: func(_ context.Context, _ runnerclient.ServiceRef, _ string) (backendharness.Dispatcher, error) {
			return dispatcher, nil
		},
	}

	agent := harnessAgent("edge")
	agent.Spec.Limits.TimeoutSeconds = timeoutSeconds
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: agent.Name}
	return s, taskRun{
		Creds: runLifecycleHarnessCredentials{}, Scope: scope, Agent: agent,
		RunID: "run-lifecycle", SessionID: "chat", Task: "continue the work", Trigger: "chat",
		ClusterID: "cluster-id",
	}
}

type runLifecycleHarnessCredentials struct{}

func (runLifecycleHarnessCredentials) GetModelCredential(_ context.Context, name string) (*agentsv1alpha1.ModelCredential, error) {
	return &agentsv1alpha1.ModelCredential{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: agentsv1alpha1.ModelCredentialSpec{
			Provider:  agentsv1alpha1.ModelProviderCodex,
			SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "harness-auth"},
		},
	}, nil
}

func (runLifecycleHarnessCredentials) GetSecret(_ context.Context, _, name string) (*corev1.Secret, error) {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Data: map[string][]byte{
			agentsv1alpha1.HarnessSecretKeyCodexAuth: []byte(`{"tokens":{"access_token":"test"}}`),
		},
	}, nil
}

type runLifecycleCancelCall struct {
	request     runner.CancelRequest
	ctxErr      error
	hasDeadline bool
}

type blockingRunLifecycleDispatcher struct {
	eventsOpened   chan struct{}
	releaseStream  chan struct{}
	cancelEntered  chan runLifecycleCancelCall
	releaseCancel  chan struct{}
	cancelReturned chan struct{}
	cancelCalls    atomic.Int32
}

func newBlockingRunLifecycleDispatcher() *blockingRunLifecycleDispatcher {
	return &blockingRunLifecycleDispatcher{
		eventsOpened:  make(chan struct{}),
		releaseStream: make(chan struct{}), cancelEntered: make(chan runLifecycleCancelCall, 4),
		releaseCancel: make(chan struct{}), cancelReturned: make(chan struct{}, 4),
	}
}

func (d *blockingRunLifecycleDispatcher) Start(_ context.Context, req runner.StartRequest) (runner.Receipt, error) {
	return runner.Receipt{AttemptID: req.AttemptID, AttemptEpoch: req.AttemptEpoch, Phase: runner.PhaseRunning, SessionID: "thread"}, nil
}

func (*blockingRunLifecycleDispatcher) Resume(context.Context, runner.ResumeRequest) (runner.Receipt, error) {
	return runner.Receipt{}, errors.New("unexpected harness resume")
}

func (d *blockingRunLifecycleDispatcher) Cancel(ctx context.Context, req runner.CancelRequest) (runner.Receipt, error) {
	_, hasDeadline := ctx.Deadline()
	d.cancelCalls.Add(1)
	d.cancelEntered <- runLifecycleCancelCall{request: req, ctxErr: ctx.Err(), hasDeadline: hasDeadline}
	<-d.releaseCancel
	d.cancelReturned <- struct{}{}
	return runner.Receipt{AttemptID: req.AttemptID, AttemptEpoch: req.AttemptEpoch, Phase: runner.PhaseCancelled, SessionID: "thread"}, nil
}

func (d *blockingRunLifecycleDispatcher) Inspect(_ context.Context, attemptID string) (runner.Receipt, error) {
	return runner.Receipt{AttemptID: attemptID, AttemptEpoch: 1, Phase: runner.PhaseRunning, SessionID: "thread"}, nil
}

func (d *blockingRunLifecycleDispatcher) Events(context.Context, string, uint64) (backendharness.Stream, error) {
	return &blockingRunLifecycleStream{opened: d.eventsOpened, release: d.releaseStream}, nil
}

type blockingRunLifecycleStream struct {
	opened  chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *blockingRunLifecycleStream) Next(ctx context.Context) (runner.Event, error) {
	s.once.Do(func() { close(s.opened) })
	<-ctx.Done()
	<-s.release
	return runner.Event{}, ctx.Err()
}

func (*blockingRunLifecycleStream) Close() error { return nil }

type parkedRunLifecycleDispatcher struct {
	receipt     runner.Receipt
	cancelCalls chan runLifecycleCancelCall
}

func (d *parkedRunLifecycleDispatcher) Start(_ context.Context, req runner.StartRequest) (runner.Receipt, error) {
	d.receipt.AttemptID = req.AttemptID
	d.receipt.AttemptEpoch = req.AttemptEpoch
	return d.receipt, nil
}

func (*parkedRunLifecycleDispatcher) Resume(context.Context, runner.ResumeRequest) (runner.Receipt, error) {
	return runner.Receipt{}, errors.New("unexpected harness resume")
}

func (d *parkedRunLifecycleDispatcher) Cancel(ctx context.Context, req runner.CancelRequest) (runner.Receipt, error) {
	if d.cancelCalls != nil {
		_, hasDeadline := ctx.Deadline()
		select {
		case d.cancelCalls <- runLifecycleCancelCall{request: req, ctxErr: ctx.Err(), hasDeadline: hasDeadline}:
		default:
		}
	}
	return runner.Receipt{AttemptID: req.AttemptID, AttemptEpoch: req.AttemptEpoch, Phase: runner.PhaseCancelled}, nil
}

func (d *parkedRunLifecycleDispatcher) Inspect(context.Context, string) (runner.Receipt, error) {
	return d.receipt, nil
}

func (*parkedRunLifecycleDispatcher) Events(context.Context, string, uint64) (backendharness.Stream, error) {
	return emptyRunLifecycleStream{}, nil
}

type emptyRunLifecycleStream struct{}

func (emptyRunLifecycleStream) Next(context.Context) (runner.Event, error) {
	return runner.Event{}, io.EOF
}
func (emptyRunLifecycleStream) Close() error { return nil }

var _ backendharness.Dispatcher = (*blockingRunLifecycleDispatcher)(nil)
var _ backendharness.Dispatcher = (*parkedRunLifecycleDispatcher)(nil)
var _ backendharness.Stream = (*blockingRunLifecycleStream)(nil)
