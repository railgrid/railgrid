/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func newExecDispatcherForAddress(address string, attempts *atomic.Int32, firstDialErr chan<- error) *httpExecDispatcher {
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		attempts.Add(1)
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if firstDialErr != nil {
			select {
			case firstDialErr <- err:
			default:
			}
		}
		return conn, err
	}}
	return &httpExecDispatcher{
		url:    "http://" + address + "/internal/exec",
		client: &http.Client{Transport: transport},
	}
}

func closedExecListenerAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve executor listener address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release executor listener address: %v", err)
	}
	return address
}

func TestHTTPExecDispatcherRetriesDelayedExecutorAfterDialRefusal(t *testing.T) {
	address := closedExecListenerAddress(t)
	var attempts atomic.Int32
	firstDialErr := make(chan error, 1)
	dispatcher := newExecDispatcherForAddress(address, &attempts, firstDialErr)
	defer dispatcher.client.CloseIdleConnections()

	type dispatchResult struct {
		response execResponse
		err      error
	}
	resultCh := make(chan dispatchResult, 1)
	go func() {
		response, err := dispatcher.Execute(context.Background(), persistentExecRequest{Argv: []string{"/bin/true"}})
		resultCh <- dispatchResult{response: response, err: err}
	}()

	if err := <-firstDialErr; !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("first dial error = %v, want connection refused", err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("start delayed executor listener: %v", err)
	}
	var requests atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"exitCode":0}`))
	})}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = server.Serve(listener)
	}()
	defer func() {
		_ = server.Close()
		<-serveDone
	}()

	select {
	case result := <-resultCh:
		if result.err != nil || result.response.ExitCode != 0 {
			t.Fatalf("dispatch result = %+v, %v", result.response, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch did not succeed after executor listener started")
	}
	if got := attempts.Load(); got < 2 {
		t.Fatalf("dial attempts = %d, want refusal followed by successful dial", got)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("executor requests = %d, want exactly one dispatched command", got)
	}
}

func TestHTTPExecDispatcherRetryWaitHonorsContext(t *testing.T) {
	for _, tt := range []struct {
		name string
		want error
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{
			name: "canceled",
			want: context.Canceled,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
		},
		{
			name: "deadline",
			want: context.DeadlineExceeded,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 50*time.Millisecond)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			address := closedExecListenerAddress(t)
			var attempts atomic.Int32
			firstDialErr := make(chan error, 1)
			dispatcher := newExecDispatcherForAddress(address, &attempts, firstDialErr)
			defer dispatcher.client.CloseIdleConnections()
			ctx, cancel := tt.ctx()
			defer cancel()

			errCh := make(chan error, 1)
			go func() {
				_, err := dispatcher.Execute(ctx, persistentExecRequest{Argv: []string{"/bin/true"}})
				errCh <- err
			}()
			if err := <-firstDialErr; !errors.Is(err, syscall.ECONNREFUSED) {
				t.Fatalf("first dial error = %v, want connection refused", err)
			}
			if tt.name == "canceled" {
				cancel()
			}
			select {
			case err := <-errCh:
				if !errors.Is(err, tt.want) {
					t.Fatalf("dispatch error = %v, want %v", err, tt.want)
				}
			case <-time.After(time.Second):
				t.Fatal("dispatch did not stop after context cancellation")
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("dial attempts = %d, want 1 after canceled retry wait", got)
			}
		})
	}
}

func TestHTTPExecDispatcherDoesNotRetryAmbiguousTransportFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()
	dispatcher := &httpExecDispatcher{url: server.URL + "/internal/exec", client: server.Client()}

	_, err := dispatcher.Execute(context.Background(), persistentExecRequest{Argv: []string{"/bin/true"}})
	if err == nil {
		t.Fatal("dispatch unexpectedly succeeded after executor closed the connection")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("executor requests = %d, want no replay after ambiguous response failure", got)
	}
}

func TestCoordinatorStartTimeoutBoundsMutationLock(t *testing.T) {
	var requests atomic.Int32
	requestSeen := make(chan struct{})
	requestCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(requestSeen)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		select {
		case <-requestCanceled:
		default:
			close(requestCanceled)
		}
	}))
	t.Cleanup(server.Close)
	dispatcher := &httpExecDispatcher{
		url: server.URL + "/internal/exec", client: server.Client(), startTimeout: 400 * time.Millisecond,
	}
	defer dispatcher.client.CloseIdleConnections()

	mutationMu := &sync.Mutex{}
	coordinator, err := newExecCoordinator(t.TempDir(), t.TempDir(), "token", dispatcher, mutationMu)
	if err != nil {
		t.Fatal(err)
	}
	req := testWorkerRequest()
	if _, err := coordinator.start(req); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not reach the stalled executor")
	}

	lockAcquired := make(chan struct{})
	go func() {
		mutationMu.Lock()
		close(lockAcquired)
		mutationMu.Unlock()
	}()
	select {
	case <-lockAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled executor kept the workspace mutation lock past the startup budget")
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("executor request context was not canceled at the startup deadline")
	}

	poll := req
	poll.Action = "poll"
	deadline := time.Now().Add(2 * time.Second)
	var result workerExecResult
	for time.Now().Before(deadline) {
		result, err = coordinator.poll(poll)
		if err != nil {
			t.Fatal(err)
		}
		if workerTerminal(result.State) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result.State != "failed" || result.ExitCode != nil || !strings.Contains(result.Stderr, context.DeadlineExceeded.Error()) {
		t.Fatalf("stalled startup result = %+v, want failed with no exit code and startup timeout", result)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("executor requests = %d, want one request with no ambiguous replay", got)
	}
}

func TestHTTPExecDispatcherBoundsUnacknowledgedResponseBodies(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
	}{
		{name: "missing started acknowledgement", status: http.StatusOK},
		{name: "error response body", status: http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			bodyStalled := make(chan struct{})
			requestCanceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(tt.status)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				close(bodyStalled)
				<-r.Context().Done()
				close(requestCanceled)
			}))
			t.Cleanup(server.Close)
			dispatcher := &httpExecDispatcher{
				url: server.URL + "/internal/exec", client: server.Client(), startTimeout: 100 * time.Millisecond,
			}
			defer dispatcher.client.CloseIdleConnections()

			started := time.Now()
			_, err := dispatcher.Start(context.Background(), persistentExecRequest{Argv: []string{"/bin/true"}})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Start error = %v, want startup deadline exceeded", err)
			}
			if time.Since(started) > time.Second {
				t.Fatalf("Start took %s to time out an unacknowledged body", time.Since(started))
			}
			select {
			case <-bodyStalled:
			case <-time.After(time.Second):
				t.Fatal("executor did not flush its response headers")
			}
			select {
			case <-requestCanceled:
			case <-time.After(time.Second):
				t.Fatal("executor request context was not canceled at the startup deadline")
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("executor requests = %d, want one request with no ambiguous replay", got)
			}
		})
	}
}

func TestHTTPExecDispatcherStartBudgetDoesNotCancelAcknowledgedExecution(t *testing.T) {
	const startTimeout = 100 * time.Millisecond
	acknowledged := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseCommand := func() { releaseOnce.Do(func() { close(release) }) }
	requestCanceled := make(chan struct{})
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(execStartedHeader, "true")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(acknowledged)
		select {
		case <-release:
			_, _ = w.Write([]byte(`{"phase":"completed","exitCode":0}`))
		case <-r.Context().Done():
			close(requestCanceled)
		}
	}))
	t.Cleanup(server.Close)
	defer releaseCommand()
	dispatcher := &httpExecDispatcher{
		url: server.URL + "/internal/exec", client: server.Client(), startTimeout: startTimeout,
	}
	defer dispatcher.client.CloseIdleConnections()

	execution, err := dispatcher.Start(context.Background(), persistentExecRequest{Argv: []string{"/bin/true"}})
	if err != nil {
		t.Fatalf("Start failed after explicit acknowledgement: %v", err)
	}
	select {
	case <-acknowledged:
	case <-time.After(time.Second):
		t.Fatal("executor did not send the explicit start acknowledgement")
	}
	select {
	case <-requestCanceled:
		t.Fatal("startup timer canceled an acknowledged running command")
	case <-time.After(3 * startTimeout):
	}
	releaseCommand()
	response, err := execution.Wait(context.Background())
	if err != nil || response.ExitCode != 0 || response.Phase != "completed" {
		t.Fatalf("Wait = %+v, %v; want completed exit code 0", response, err)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("executor handler did not finish after response body was read")
	}
}

func TestCoordinatorStreamingExecReceiptsPersistOnlyKnownExitCodes(t *testing.T) {
	int32ptr := func(value int32) *int32 { return &value }
	tests := []struct {
		name               string
		body               string
		legacy             bool
		contentLengthExtra int
		wantState          string
		wantExitCode       *int32
		wantErrorInStderr  string
	}{
		{name: "completed zero", body: `{"phase":"completed","exitCode":0}`, wantState: "succeeded", wantExitCode: int32ptr(0)},
		{name: "completed nonzero", body: `{"phase":"completed","exitCode":7}`, wantState: "failed", wantExitCode: int32ptr(7)},
		{name: "legacy error receipt without stderr", body: `{"exitCode":2,"error":"legacy execution failed"}`, legacy: true, wantState: "failed", wantExitCode: int32ptr(2), wantErrorInStderr: "legacy execution failed"},
		{name: "timed out", body: `{"phase":"timed_out","exitCode":-1,"timedOut":true}`, wantState: "timed_out", wantExitCode: int32ptr(-1)},
		{name: "canceled receipt", body: `{"phase":"cancelled","exitCode":-1,"cancelled":true}`, wantState: "canceled", wantExitCode: int32ptr(-1)},
		{name: "failed receipt", body: `{"phase":"failed","exitCode":-1,"error":"helper failed"}`, wantState: "failed", wantExitCode: int32ptr(-1), wantErrorInStderr: "helper failed"},
		{name: "cleanup unproven", body: `{"phase":"failed","exitCode":-1,"error":"process cleanup could not be proven","cleanupUnproven":true}`, wantState: "failed", wantErrorInStderr: "process cleanup could not be proven"},
		{name: "legacy cleanup unproven without error", body: `{"exitCode":0,"cleanupUnproven":true}`, legacy: true, wantState: "failed", wantErrorInStderr: "process cleanup could not be proven"},
		{name: "malformed terminal JSON", body: `not json`, wantState: "failed", wantErrorInStderr: "decode stateless executor response"},
		{name: "truncated HTTP body", body: `{"phase":"completed","exitCode":0}`, contentLengthExtra: 1, wantState: "failed", wantErrorInStderr: "unexpected EOF"},
		{name: "truncated terminal JSON", body: `{"phase":"completed","exitCode":`, wantState: "failed", wantErrorInStderr: "decode stateless executor response"},
		{name: "start acknowledgement only", body: `{"phase":"started","exitCode":0}`, wantState: "failed", wantErrorInStderr: "missing a terminal execution receipt"},
		{name: "empty JSON receipt", body: `{}`, wantState: "failed", wantErrorInStderr: "exitCode is absent"},
		{name: "missing phase zero", body: `{"exitCode":0}`, wantState: "failed", wantErrorInStderr: "missing a terminal execution receipt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if !tt.legacy {
					w.Header().Set(execStartedHeader, "true")
				}
				if tt.contentLengthExtra != 0 {
					w.Header().Set("Content-Length", fmt.Sprintf("%d", len(tt.body)+tt.contentLengthExtra))
				}
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			dispatcher := &httpExecDispatcher{url: server.URL + "/internal/exec", client: server.Client()}
			defer dispatcher.client.CloseIdleConnections()

			coordinator, err := newExecCoordinator(t.TempDir(), t.TempDir(), "token", dispatcher, &sync.Mutex{})
			if err != nil {
				t.Fatal(err)
			}
			req := testWorkerRequest()
			if started, err := coordinator.start(req); err != nil || started.State != "queued" {
				t.Fatalf("start = %+v, %v", started, err)
			}
			poll := req
			poll.Action = "poll"
			deadline := time.Now().Add(5 * time.Second)
			var result workerExecResult
			for time.Now().Before(deadline) {
				result, err = coordinator.poll(poll)
				if err != nil {
					t.Fatal(err)
				}
				if workerTerminal(result.State) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if result.State != tt.wantState {
				t.Fatalf("result state = %q, want %q (result: %+v)", result.State, tt.wantState, result)
			}
			if tt.wantExitCode == nil {
				if result.ExitCode != nil {
					t.Fatalf("result exit code = %d, want omitted", *result.ExitCode)
				}
			} else if result.ExitCode == nil || *result.ExitCode != *tt.wantExitCode {
				t.Fatalf("result exit code = %v, want %d", result.ExitCode, *tt.wantExitCode)
			}
			if tt.wantErrorInStderr != "" && !strings.Contains(result.Stderr, tt.wantErrorInStderr) {
				t.Fatalf("stderr = %q, want %q", result.Stderr, tt.wantErrorInStderr)
			}

			raw, err := os.ReadFile(coordinator.recordPath(req.SessionID))
			if err != nil {
				t.Fatalf("read durable session result: %v", err)
			}
			var persisted struct {
				Result map[string]json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(raw, &persisted); err != nil {
				t.Fatalf("decode durable session result: %v", err)
			}
			durableExitCode, found := persisted.Result["exitCode"]
			if tt.wantExitCode == nil {
				if found {
					t.Fatalf("durable result has fabricated exit code %s: %s", durableExitCode, raw)
				}
			} else if !found {
				t.Fatalf("durable result omitted exit code %d: %s", *tt.wantExitCode, raw)
			} else {
				var got int32
				if err := json.Unmarshal(durableExitCode, &got); err != nil || got != *tt.wantExitCode {
					t.Fatalf("durable exit code = %s, decode error %v; want %d", durableExitCode, err, *tt.wantExitCode)
				}
			}

			duplicate, err := coordinator.start(req)
			if err != nil || duplicate.State != tt.wantState {
				t.Fatalf("idempotent duplicate start = %+v, %v", duplicate, err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("executor requests = %d, want one dispatch after duplicate start", got)
			}
		})
	}
}

func TestCoordinatorCanceledStreamingWaitDoesNotInventExitCode(t *testing.T) {
	startAck := make(chan struct{})
	handlerDone := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(execStartedHeader, "true")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(startAck)
		<-r.Context().Done()
	}))
	t.Cleanup(func() { server.Close() })
	dispatcher := &httpExecDispatcher{url: server.URL + "/internal/exec", client: server.Client()}
	defer dispatcher.client.CloseIdleConnections()
	coordinator, err := newExecCoordinator(t.TempDir(), t.TempDir(), "token", dispatcher, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	req := testWorkerRequest()
	// Always cancel a started request before the server cleanup waits for its
	// handler. This also covers assertion failures before the explicit cancel.
	t.Cleanup(func() {
		coordinator.mu.Lock()
		cancel := coordinator.active[req.SessionID]
		coordinator.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
	if _, err := coordinator.start(req); err != nil {
		t.Fatal(err)
	}
	select {
	case <-startAck:
	case <-time.After(time.Second):
		t.Fatal("streaming executor did not send its start acknowledgement")
	}

	coordinator.mu.Lock()
	cancel := coordinator.active[req.SessionID]
	coordinator.mu.Unlock()
	if cancel == nil {
		t.Fatal("running execution has no cancellation function")
	}
	cancel()
	poll := req
	poll.Action = "poll"
	deadline := time.Now().Add(5 * time.Second)
	var result workerExecResult
	for time.Now().Before(deadline) {
		result, err = coordinator.poll(poll)
		if err != nil {
			t.Fatal(err)
		}
		if workerTerminal(result.State) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result.State != "canceled" || result.ExitCode != nil {
		t.Fatalf("canceled streaming wait result = %+v, want canceled with no exit code", result)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("executor handler did not stop after wait cancellation")
	}
	raw, err := os.ReadFile(coordinator.recordPath(req.SessionID))
	if err != nil {
		t.Fatalf("read durable canceled result: %v", err)
	}
	var persisted struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode durable canceled result: %v", err)
	}
	if _, found := persisted.Result["exitCode"]; found {
		t.Fatalf("durable canceled result has synthetic exitCode: %s", raw)
	}
	if duplicate, err := coordinator.start(req); err != nil || duplicate.State != "canceled" {
		t.Fatalf("idempotent duplicate start = %+v, %v", duplicate, err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("executor requests = %d, want no redispatch after canceled wait", got)
	}
}

type blockingDispatcher struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   int
	mu      sync.Mutex
}

func (d *blockingDispatcher) Start(_ context.Context, _ persistentExecRequest) (execExecution, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	d.once.Do(func() { close(d.started) })
	return blockingExecution{release: d.release}, nil
}

type blockingExecution struct {
	release <-chan struct{}
}

func (e blockingExecution) Wait(ctx context.Context) (execResponse, error) {
	select {
	case <-e.release:
		return execResponse{ExitCode: 0}, nil
	case <-ctx.Done():
		return execResponse{ExitCode: -1, Cancelled: true}, ctx.Err()
	}
}

type rejectedStartDispatcher struct {
	err   error
	calls atomic.Int32
}

func (d *rejectedStartDispatcher) Start(context.Context, persistentExecRequest) (execExecution, error) {
	d.calls.Add(1)
	return nil, d.err
}

type parallelDispatcher struct {
	started  chan string
	releases map[string]chan struct{}
}

func (d *parallelDispatcher) Start(_ context.Context, req persistentExecRequest) (execExecution, error) {
	key := req.Argv[1]
	d.started <- key
	return blockingExecution{release: d.releases[key]}, nil
}

func testWorkerRequest() workerExecRequest {
	return workerExecRequest{
		Action: "start", SessionID: strings.Repeat("a", 32), RequestID: "request-1",
		CallerKey: "caller-1", Fingerprint: strings.Repeat("b", 64),
		persistentExecRequest: persistentExecRequest{
			Argv: []string{"/bin/true"}, WorkDir: ".", TimeoutMS: 1000,
			SourceRevision: 1, SourceDigest: strings.Repeat("c", 64),
		},
	}
}

func TestCoordinatorStartRejectionOmitsExitCodeFromDurableResult(t *testing.T) {
	for _, tt := range []struct {
		name        string
		dispatchErr error
		wantState   string
	}{
		{name: "rejected", dispatchErr: errors.New("stateless executor rejected request: working directory does not exist"), wantState: "failed"},
		{name: "canceled", dispatchErr: context.Canceled, wantState: "canceled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace, stateDir := t.TempDir(), t.TempDir()
			dispatch := &rejectedStartDispatcher{err: tt.dispatchErr}
			coordinator, err := newExecCoordinator(workspace, stateDir, "token", dispatch, &sync.Mutex{})
			if err != nil {
				t.Fatal(err)
			}

			req := testWorkerRequest()
			if started, err := coordinator.start(req); err != nil || started.State != "queued" {
				t.Fatalf("start = %+v, %v", started, err)
			}
			poll := req
			poll.Action = "poll"
			deadline := time.Now().Add(5 * time.Second)
			var result workerExecResult
			for time.Now().Before(deadline) {
				result, err = coordinator.poll(poll)
				if err != nil {
					t.Fatal(err)
				}
				if workerTerminal(result.State) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if result.State != tt.wantState {
				t.Fatalf("result state = %q, want %q (result: %+v)", result.State, tt.wantState, result)
			}
			if result.ExitCode != nil {
				t.Fatalf("start rejection result has exit code %d; no process completed", *result.ExitCode)
			}
			if !strings.Contains(result.Stderr, tt.dispatchErr.Error()) {
				t.Fatalf("stderr = %q, want dispatch error %q", result.Stderr, tt.dispatchErr)
			}
			if got := dispatch.calls.Load(); got != 1 {
				t.Fatalf("dispatcher start calls = %d, want 1", got)
			}

			raw, err := os.ReadFile(coordinator.recordPath(req.SessionID))
			if err != nil {
				t.Fatalf("read durable session result: %v", err)
			}
			var persisted struct {
				Result map[string]json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(raw, &persisted); err != nil {
				t.Fatalf("decode durable session result: %v", err)
			}
			if _, found := persisted.Result["exitCode"]; found {
				t.Fatalf("durable start rejection contains exitCode: %s", raw)
			}
		})
	}

	success := workerResultFromExec(execResponse{ExitCode: 0}, nil)
	if success.ExitCode == nil || *success.ExitCode != 0 {
		t.Fatalf("successful waited execution lost its real zero exit code: %+v", success)
	}
}

func TestHTTPExecDispatcherUsesOnlyExplicitInvalidWorkdirCode(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantTyped bool
	}{
		{
			name:      "explicit code",
			body:      `{"rejectionCode":"invalid_workdir","error":"workdir does not exist"}`,
			wantTyped: true,
		},
		{
			name: "stderr-like text only",
			body: `invalid_workdir: workdir does not exist`,
		},
		{
			name: "error field text only",
			body: `{"error":"invalid_workdir: workdir does not exist"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			dispatcher := &httpExecDispatcher{url: server.URL + "/internal/exec", client: server.Client()}
			_, err := dispatcher.Start(context.Background(), persistentExecRequest{Argv: []string{"true"}})
			if err == nil {
				t.Fatal("dispatcher accepted a pre-start rejection")
			}
			if got := errors.Is(err, errExecInvalidWorkdir); got != tt.wantTyped {
				t.Fatalf("typed invalid workdir = %v, want %v; error=%v", got, tt.wantTyped, err)
			}
		})
	}
}

func TestCoordinatorPersistsInvalidWorkdirRejectionWithoutExitCode(t *testing.T) {
	workspace := t.TempDir()
	digest, err := digestSyncFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeExecTestManifest(t, workspace, workspaceManifest{SourceRevision: 1, SourceDigest: digest})
	executor := httptest.NewServer(&statelessExecutor{workspace: workspace, startExec: startPersistentExecHelper})
	defer executor.Close()
	coordinator, err := newExecCoordinator(workspace, t.TempDir(), "token", &httpExecDispatcher{
		url: executor.URL + "/internal/exec", client: executor.Client(),
	}, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	req := testWorkerRequest()
	req.WorkDir = "missing-workdir"
	req.SourceDigest = digest
	if result, err := coordinator.start(req); err != nil || result.State != "queued" {
		t.Fatalf("start = %+v, %v; want queued", result, err)
	}
	poll := req
	poll.Action = "poll"
	result := waitForExecTestState(t, coordinator, poll, "failed")
	waitForExecCoordinatorInactive(t, coordinator, req.SessionID)
	if result.RejectionCode != execRejectionInvalidWorkdir {
		t.Fatalf("terminal rejection code = %q, want %q; result=%+v", result.RejectionCode, execRejectionInvalidWorkdir, result)
	}
	if result.ExitCode != nil {
		t.Fatalf("pre-start workdir rejection has exit code %d", *result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "workdir") {
		t.Fatalf("diagnostic = %q, want workdir detail", result.Stderr)
	}

	raw, err := os.ReadFile(coordinator.recordPath(req.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	var persisted execSessionRecord
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode durable session result: %v", err)
	}
	if persisted.Result.RejectionCode != execRejectionInvalidWorkdir || persisted.Result.ExitCode != nil {
		t.Fatalf("durable result = %+v, want typed rejection without exit code", persisted.Result)
	}
}

func waitForExecCoordinatorInactive(t *testing.T, coordinator *execCoordinator, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		coordinator.mu.Lock()
		_, active := coordinator.active[sessionID]
		coordinator.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("execution session %s remained active", sessionID)
}

func TestCoordinatorIdempotencyCallerBindingAndRestartRecovery(t *testing.T) {
	workspace, stateDir := t.TempDir(), t.TempDir()
	dispatch := &blockingDispatcher{started: make(chan struct{}), release: make(chan struct{})}
	coordinator, err := newExecCoordinator(workspace, stateDir, "token", dispatch, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	req := testWorkerRequest()
	started, err := coordinator.start(req)
	if err != nil || started.State != "queued" {
		t.Fatalf("start = %+v, %v", started, err)
	}
	<-dispatch.started
	duplicate, err := coordinator.start(req)
	if err != nil || duplicate.SessionID != started.SessionID {
		t.Fatalf("duplicate = %+v, %v", duplicate, err)
	}
	changed := req
	changed.Fingerprint = strings.Repeat("d", 64)
	if _, err := coordinator.start(changed); err == nil {
		t.Fatal("changed fingerprint was accepted")
	}
	wrongCaller := req
	wrongCaller.Action, wrongCaller.CallerKey = "poll", "caller-2"
	if _, err := coordinator.poll(wrongCaller); err == nil {
		t.Fatal("caller mismatch was accepted")
	}

	// Simulate coordinator restart while a durable nonterminal record exists.
	restarted, err := newExecCoordinator(workspace, stateDir, "token", dispatch, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.start(req)
	if err != nil || result.State != "failed" || !strings.Contains(result.Stderr, "not redispatched") {
		t.Fatalf("recovered start = %+v, %v", result, err)
	}
	dispatch.mu.Lock()
	if dispatch.calls != 1 {
		t.Fatalf("dispatch calls = %d, want 1", dispatch.calls)
	}
	dispatch.mu.Unlock()
	close(dispatch.release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		coordinator.mu.Lock()
		_, active := coordinator.active[req.SessionID]
		coordinator.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("original coordinator dispatch did not finish")
}

func TestCoordinatorMutationLockEndsAtDispatchAndCancelDoesNotWait(t *testing.T) {
	workspace, stateDir := t.TempDir(), t.TempDir()
	mutationMu := &sync.Mutex{}
	dispatch := &blockingDispatcher{started: make(chan struct{}), release: make(chan struct{})}
	coordinator, err := newExecCoordinator(workspace, stateDir, "token", dispatch, mutationMu)
	if err != nil {
		t.Fatal(err)
	}
	req := testWorkerRequest()
	if _, err := coordinator.start(req); err != nil {
		t.Fatal(err)
	}
	<-dispatch.started
	locked := make(chan struct{})
	go func() { mutationMu.Lock(); close(locked); mutationMu.Unlock() }()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("workspace mutation remained blocked for the command lifetime")
	}
	cancelReq := req
	cancelReq.Action = "cancel"
	started := time.Now()
	result, err := coordinator.cancel(cancelReq)
	if err != nil || result.State != "canceled" || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("cancel = %+v, %v after %s", result, err, time.Since(started))
	}
	waitForExecCoordinatorInactive(t, coordinator, req.SessionID)
}

func TestCoordinatorRunsCommandsConcurrentlyAndCancellationIsScoped(t *testing.T) {
	workspace, stateDir := t.TempDir(), t.TempDir()
	dispatch := &parallelDispatcher{
		started: make(chan string, 2),
		releases: map[string]chan struct{}{
			"first":  make(chan struct{}),
			"second": make(chan struct{}),
		},
	}
	coordinator, err := newExecCoordinator(workspace, stateDir, "token", dispatch, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	first := testWorkerRequest()
	first.Argv = []string{"test-command", "first"}
	second := first
	second.SessionID = strings.Repeat("d", 32)
	second.RequestID = "request-2"
	second.Fingerprint = strings.Repeat("e", 64)
	second.Argv = []string{"test-command", "second"}
	if _, err := coordinator.start(first); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.start(second); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case key := <-dispatch.started:
			seen[key] = true
		case <-time.After(time.Second):
			t.Fatal("second command did not reach dispatch while the first was still running")
		}
	}
	if !seen["first"] || !seen["second"] {
		t.Fatalf("dispatched commands = %v, want both", seen)
	}

	cancelReq := first
	cancelReq.Action = "cancel"
	if result, err := coordinator.cancel(cancelReq); err != nil || result.State != "canceled" {
		t.Fatalf("cancel first = %+v, %v", result, err)
	}
	secondPoll := second
	secondPoll.Action = "poll"
	if result, err := coordinator.poll(secondPoll); err != nil || result.State != "running" {
		t.Fatalf("second command after first cancellation = %+v, %v; want running", result, err)
	}
	close(dispatch.releases["second"])
	deadline := time.Now().Add(time.Second)
	finished := false
	for time.Now().Before(deadline) {
		result, err := coordinator.poll(secondPoll)
		if err != nil {
			t.Fatal(err)
		}
		if result.State == "succeeded" {
			finished = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !finished {
		t.Fatal("second command did not finish successfully after the first was canceled")
	}
	waitForExecCoordinatorInactive(t, coordinator, first.SessionID)
}

func TestCoordinatorBoundsConcurrentCommandTrees(t *testing.T) {
	workspace, stateDir := t.TempDir(), t.TempDir()
	dispatch := &parallelDispatcher{
		started:  make(chan string, execMaxConcurrentCommands+1),
		releases: map[string]chan struct{}{},
	}
	for i := range execMaxConcurrentCommands + 1 {
		key := fmt.Sprintf("command-%d", i)
		dispatch.releases[key] = make(chan struct{})
	}
	coordinator, err := newExecCoordinator(workspace, stateDir, "token", dispatch, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]workerExecRequest, execMaxConcurrentCommands+1)
	t.Cleanup(func() {
		for _, gate := range dispatch.releases {
			select {
			case <-gate:
			default:
				close(gate)
			}
		}
		for _, request := range requests {
			waitForExecCoordinatorInactive(t, coordinator, request.SessionID)
		}
	})
	for i := range requests {
		key := fmt.Sprintf("command-%d", i)
		request := testWorkerRequest()
		request.SessionID = fmt.Sprintf("%032x", i+1)
		request.RequestID = key
		request.Fingerprint = fmt.Sprintf("%064x", i+1)
		request.Argv = []string{"test-command", key}
		requests[i] = request
		if _, err := coordinator.start(request); err != nil {
			t.Fatal(err)
		}
	}
	started := map[string]bool{}
	for range execMaxConcurrentCommands {
		select {
		case key := <-dispatch.started:
			started[key] = true
		case <-time.After(time.Second):
			t.Fatal("concurrency limit prevented an available command from dispatching")
		}
	}
	select {
	case key := <-dispatch.started:
		t.Fatalf("command %s dispatched beyond the concurrency bound", key)
	case <-time.After(50 * time.Millisecond):
	}
	var releasedKey string
	for key := range started {
		releasedKey = key
		break
	}
	var queuedKey string
	for i := range requests {
		key := requests[i].Argv[1]
		if !started[key] {
			queuedKey = key
			break
		}
	}
	close(dispatch.releases[releasedKey])
	select {
	case key := <-dispatch.started:
		if key != queuedKey {
			t.Fatalf("queued command = %q, want %q", key, queuedKey)
		}
	case <-time.After(time.Second):
		t.Fatal("queued command did not dispatch when a slot became available")
	}
	for _, request := range requests {
		key := request.Argv[1]
		if key != releasedKey {
			close(dispatch.releases[key])
		}
	}
	for _, request := range requests {
		poll := request
		poll.Action = "poll"
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			result, err := coordinator.poll(poll)
			if err != nil {
				t.Fatal(err)
			}
			if result.State == "succeeded" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		result, err := coordinator.poll(poll)
		if err != nil || result.State != "succeeded" {
			t.Fatalf("command %s state = %+v, %v; want succeeded", request.RequestID, result, err)
		}
	}
}

func TestCoordinatorCancelBeforeStartIsDurableAndPreventsDispatch(t *testing.T) {
	workspace, stateDir := t.TempDir(), t.TempDir()
	dispatch := &blockingDispatcher{started: make(chan struct{}), release: make(chan struct{})}
	coordinator, err := newExecCoordinator(workspace, stateDir, "token", dispatch, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	start := testWorkerRequest()
	cancel := start
	cancel.Action = "cancel"
	canceled, err := coordinator.cancel(cancel)
	if err != nil || canceled.State != "canceled" || canceled.SessionID != start.SessionID || canceled.RequestID != start.RequestID {
		t.Fatalf("request-ID cancel before start = %+v, %v", canceled, err)
	}
	record, found, err := coordinator.readRecord(start.SessionID)
	if err != nil || !found || !record.CanceledBeforeStart {
		t.Fatalf("durable tombstone = %+v, found=%t, err=%v", record, found, err)
	}

	// The marker must survive coordinator replacement; a delayed START for the
	// exact same caller and request cannot reach the dispatcher.
	restarted, err := newExecCoordinator(workspace, stateDir, "token", dispatch, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	started, err := restarted.start(start)
	if err != nil || started.State != "canceled" {
		t.Fatalf("delayed start = %+v, %v; want canceled", started, err)
	}
	select {
	case <-dispatch.started:
		t.Fatal("delayed START was dispatched after its cancellation tombstone")
	case <-time.After(50 * time.Millisecond):
	}
	dispatch.mu.Lock()
	if dispatch.calls != 0 {
		t.Fatalf("dispatcher calls = %d, want 0", dispatch.calls)
	}
	dispatch.mu.Unlock()
}

func TestCoordinatorCancelBeforeStartRejectsDifferentSessionBinding(t *testing.T) {
	workspace, stateDir := t.TempDir(), t.TempDir()
	dispatch := &blockingDispatcher{started: make(chan struct{}), release: make(chan struct{})}
	coordinator, err := newExecCoordinator(workspace, stateDir, "token", dispatch, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	first := testWorkerRequest()
	if _, err := coordinator.cancel(workerExecRequest{Action: "cancel", SessionID: first.SessionID, RequestID: first.RequestID, CallerKey: first.CallerKey}); err != nil {
		t.Fatal(err)
	}
	otherSession := first
	otherSession.SessionID = strings.Repeat("c", 32)
	if _, err := coordinator.cancel(workerExecRequest{Action: "cancel", SessionID: otherSession.SessionID, RequestID: otherSession.RequestID, CallerKey: otherSession.CallerKey}); err == nil {
		t.Fatal("same request ID was allowed to cancel a different session binding")
	} else {
		var httpErr *workerHTTPError
		if !errors.As(err, &httpErr) || httpErr.status != http.StatusConflict {
			t.Fatalf("cross-session cancel error = %v, want 409", err)
		}
	}
}

func TestCoordinatorCancelTombstonesHaveBoundedCapacityAndRetention(t *testing.T) {
	workspace, stateDir := t.TempDir(), t.TempDir()
	dispatch := &blockingDispatcher{started: make(chan struct{}), release: make(chan struct{})}
	coordinator, err := newExecCoordinator(workspace, stateDir, "token", dispatch, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]workerExecRequest, 0, execSessionCapacity)
	for i := 0; i < execSessionCapacity; i++ {
		request := workerExecRequest{
			Action: "cancel", SessionID: fmt.Sprintf("%032x", i+1),
			RequestID: fmt.Sprintf("cancel-%d", i), CallerKey: "caller-1",
		}
		if _, err := coordinator.cancel(request); err != nil {
			t.Fatalf("cancel %d: %v", i, err)
		}
		requests = append(requests, request)
	}
	if _, err := coordinator.cancel(workerExecRequest{
		Action: "cancel", SessionID: strings.Repeat("f", 32), RequestID: "cancel-over-capacity", CallerKey: "caller-1",
	}); err == nil {
		t.Fatal("cancel beyond tombstone capacity unexpectedly succeeded")
	} else {
		var httpErr *workerHTTPError
		if !errors.As(err, &httpErr) || httpErr.status != http.StatusServiceUnavailable {
			t.Fatalf("over-capacity cancel error = %v, want 503", err)
		}
	}
	start := testWorkerRequest()
	start.SessionID, start.RequestID, start.CallerKey = requests[0].SessionID, requests[0].RequestID, requests[0].CallerKey
	if got, err := coordinator.start(start); err != nil || got.State != "canceled" {
		t.Fatalf("first tombstone after capacity pressure = %+v, %v; want canceled", got, err)
	}

	old := time.Now().Add(-execSessionRetention - time.Second)
	for _, request := range requests {
		record, found, err := coordinator.readRecord(request.SessionID)
		if err != nil || !found {
			t.Fatalf("read tombstone %s: found=%t err=%v", request.SessionID, found, err)
		}
		record.CreatedAt, record.UpdatedAt = old, old
		if err := coordinator.persistRecord(record); err != nil {
			t.Fatalf("age tombstone %s: %v", request.SessionID, err)
		}
	}
	if err := coordinator.gcLocked(time.Now()); err != nil {
		t.Fatal(err)
	}
	if count, err := coordinator.recordCount(); err != nil || count != 0 {
		t.Fatalf("records after tombstone retention = %d, %v; want 0", count, err)
	}
}

func TestCoordinatorTerminalPersistenceFailureLogsAndExits(t *testing.T) {
	workspace, stateDir := t.TempDir(), t.TempDir()
	dispatch := &blockingDispatcher{started: make(chan struct{}), release: make(chan struct{})}
	coordinator, err := newExecCoordinator(workspace, stateDir, "token", dispatch, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	originalWrite := coordinator.writeSession
	coordinator.writeSession = func(record execSessionRecord) error {
		if workerTerminal(record.Result.State) {
			return errors.New("injected durable write failure")
		}
		return originalWrite(record)
	}
	exited := make(chan int, 1)
	logged := make(chan string, 1)
	coordinator.exit = func(code int) { exited <- code }
	coordinator.logf = func(format string, args ...any) { logged <- fmt.Sprintf(format, args...) }

	if _, err := coordinator.start(testWorkerRequest()); err != nil {
		t.Fatal(err)
	}
	<-dispatch.started
	close(dispatch.release)
	select {
	case code := <-exited:
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal persistence failure did not exit")
	}
	select {
	case message := <-logged:
		if !strings.Contains(message, "FATAL") || !strings.Contains(message, "injected durable write failure") {
			t.Fatalf("fatal log = %q", message)
		}
	default:
		t.Fatal("terminal persistence failure was not logged")
	}
}

func TestCoordinatorRunningPersistenceFailureLogsExitsAndDoesNotDispatch(t *testing.T) {
	workspace, stateDir := t.TempDir(), t.TempDir()
	dispatch := &blockingDispatcher{started: make(chan struct{}), release: make(chan struct{})}
	coordinator, err := newExecCoordinator(workspace, stateDir, "token", dispatch, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	originalWrite := coordinator.writeSession
	coordinator.writeSession = func(record execSessionRecord) error {
		if record.Result.State == "running" {
			return errors.New("injected running write failure")
		}
		return originalWrite(record)
	}
	exited := make(chan int, 1)
	logged := make(chan string, 1)
	coordinator.exit = func(code int) { exited <- code }
	coordinator.logf = func(format string, args ...any) { logged <- fmt.Sprintf(format, args...) }

	req := testWorkerRequest()
	if _, err := coordinator.start(req); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-exited:
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	case <-time.After(time.Second):
		t.Fatal("running persistence failure did not exit")
	}
	select {
	case message := <-logged:
		if !strings.Contains(message, "FATAL") || !strings.Contains(message, "injected running write failure") {
			t.Fatalf("fatal log = %q", message)
		}
	default:
		t.Fatal("running persistence failure was not logged")
	}
	dispatch.mu.Lock()
	defer dispatch.mu.Unlock()
	if dispatch.calls != 0 {
		t.Fatalf("dispatch calls = %d, want 0", dispatch.calls)
	}
	coordinator.mu.Lock()
	_, active := coordinator.active[req.SessionID]
	coordinator.mu.Unlock()
	if active {
		t.Fatal("failed queued session remains active")
	}
}
