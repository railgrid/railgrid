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
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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

type blockingDispatcher struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   int
	mu      sync.Mutex
}

func (d *blockingDispatcher) Execute(ctx context.Context, _ persistentExecRequest) (execResponse, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	d.once.Do(func() { close(d.started) })
	select {
	case <-d.release:
		return execResponse{ExitCode: 0}, nil
	case <-ctx.Done():
		return execResponse{ExitCode: -1, Cancelled: true}, ctx.Err()
	}
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

func TestCoordinatorMutationLockCoversCompleteDispatchAndCancelDoesNotWait(t *testing.T) {
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
		t.Fatal("mutation lock released before complete dispatch")
	case <-time.After(50 * time.Millisecond):
	}
	cancelReq := req
	cancelReq.Action = "cancel"
	started := time.Now()
	result, err := coordinator.cancel(cancelReq)
	if err != nil || result.State != "canceled" || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("cancel = %+v, %v after %s", result, err, time.Since(started))
	}
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("dispatch did not release mutation lock after cancellation")
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
