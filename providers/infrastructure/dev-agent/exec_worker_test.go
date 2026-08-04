/*
Copyright 2026 The Faros Authors.

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
	"strings"
	"sync"
	"testing"
	"time"
)

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
