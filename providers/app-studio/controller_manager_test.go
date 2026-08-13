// Copyright 2026 The Faros Authors.
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

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

func TestControllerReadyRunnableMarksHealthWhenLaunched(t *testing.T) {
	health := newControllerHealth(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		if err := controllerReadyRunnable(health).Start(ctx); err != nil {
			t.Errorf("controller ready runnable: %v", err)
		}
		close(done)
	}()

	waitForControllerState(t, health, controllerStateReady)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("controller ready runnable did not stop after cancellation")
	}
}

func TestControllerHealthLifecycle(t *testing.T) {
	health := newControllerHealth(true)
	if got := health.snapshot(); got.State != controllerStateStarting || !got.Required {
		t.Fatalf("initial controller health = %+v, want required/starting", got)
	}
	if health.ready() || health.heartbeatStatus() != "starting" {
		t.Fatal("required controller should not be ready before start")
	}

	health.markFailed(errors.New("endpoint slice unavailable"))
	if got := health.snapshot(); got.State != controllerStateFailed || got.Error != "endpoint slice unavailable" {
		t.Fatalf("failed controller health = %+v, want failure", got)
	}
	if health.ready() || health.heartbeatStatus() != "unhealthy" {
		t.Fatal("failed required controller should not be ready or healthy")
	}

	health.markStarting()
	health.markReady()
	if got := health.snapshot(); got.State != controllerStateReady || !health.ready() || health.heartbeatStatus() != "healthy" {
		t.Fatalf("running controller health = %+v, want ready/healthy", got)
	}

	health.markStopped(context.Canceled)
	if got := health.snapshot(); got.State != controllerStateStopped || got.Error != context.Canceled.Error() || health.ready() {
		t.Fatalf("stopped controller health = %+v, want stopped/not ready", got)
	}
}

func TestRunControllerManagerRetriesSetupAndPostStartFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	health := newControllerHealth(true)
	var loadCalls atomic.Int32
	var startCalls atomic.Int32
	firstLoadDone := make(chan struct{})
	firstStartDone := make(chan struct{})
	firstRetryGateEntered := make(chan struct{})
	allowFirstRetry := make(chan struct{})
	secondRetryGateEntered := make(chan struct{})
	allowSecondRetry := make(chan struct{})
	secondReady := make(chan struct{})
	done := make(chan struct{})

	loadConfig := func() (*rest.Config, error) {
		if loadCalls.Add(1) == 1 {
			close(firstLoadDone)
			return nil, errors.New("provider kubeconfig is not bootstrapped")
		}
		return &rest.Config{}, nil
	}
	start := func(startCtx context.Context, _ *rest.Config, _ controllerDeps) error {
		if startCalls.Add(1) == 1 {
			close(firstStartDone)
			return errors.New("manager exited after start")
		}
		health.markReady()
		close(secondReady)
		<-startCtx.Done()
		return startCtx.Err()
	}
	var retryGateCalls atomic.Int32
	retryGate := func(retryCtx context.Context, _ time.Duration) bool {
		call := retryGateCalls.Add(1)
		switch call {
		case 1:
			close(firstRetryGateEntered)
		case 2:
			close(secondRetryGateEntered)
		default:
			return false
		}
		var release <-chan struct{}
		if call == 1 {
			release = allowFirstRetry
		} else {
			release = allowSecondRetry
		}
		select {
		case <-release:
			return true
		case <-retryCtx.Done():
			return false
		}
	}

	go func() {
		runControllerManagerWithRetryGate(ctx, health, loadConfig, start, controllerDeps{}, 15*time.Second, retryGate)
		close(done)
	}()

	select {
	case <-firstLoadDone:
	case <-time.After(time.Second):
		t.Fatal("controller loop did not attempt initial setup")
	}
	select {
	case <-firstRetryGateEntered:
	case <-time.After(time.Second):
		t.Fatal("controller loop did not enter the first retry gate")
	}
	if got := health.snapshot(); got.State != controllerStateFailed || got.Error != "provider kubeconfig is not bootstrapped" {
		t.Fatalf("first failed controller health = %+v", got)
	}
	close(allowFirstRetry)
	select {
	case <-firstStartDone:
	case <-time.After(time.Second):
		t.Fatal("controller loop did not retry after setup failure")
	}
	select {
	case <-secondRetryGateEntered:
	case <-time.After(time.Second):
		t.Fatal("controller loop did not enter the second retry gate")
	}
	if got := health.snapshot(); got.State != controllerStateFailed || got.Error != "manager exited after start" {
		t.Fatalf("second failed controller health = %+v", got)
	}
	close(allowSecondRetry)
	select {
	case <-secondReady:
	case <-time.After(time.Second):
		t.Fatal("controller loop did not re-enter after manager exit")
	}
	if got := startCalls.Load(); got != 2 {
		t.Fatalf("manager starts = %d, want 2 before cancellation", got)
	}
	if !health.ready() {
		t.Fatal("controller should be ready after the recovered manager start")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("controller loop did not stop after context cancellation")
	}
	if got := health.snapshot(); got.State != controllerStateStopped {
		t.Fatalf("final controller health = %+v, want stopped", got)
	}
}

func TestRunControllerManagerKeepsRESTOnlyModeAvailable(t *testing.T) {
	health := newControllerHealth(false)
	var loadCalls atomic.Int32
	var startCalls atomic.Int32
	runControllerManager(
		context.Background(),
		health,
		func() (*rest.Config, error) {
			loadCalls.Add(1)
			return nil, errors.New("must not load kubeconfig")
		},
		func(context.Context, *rest.Config, controllerDeps) error {
			startCalls.Add(1)
			return errors.New("must not start controller")
		},
		controllerDeps{},
		0,
	)
	if got := health.snapshot(); got.State != controllerStateRESTOnly || !health.ready() || health.heartbeatStatus() != "healthy" {
		t.Fatalf("REST-only controller health = %+v, want ready/rest-only", got)
	}
	if loadCalls.Load() != 0 || startCalls.Load() != 0 {
		t.Fatalf("REST-only lifecycle called dependencies: load=%d start=%d", loadCalls.Load(), startCalls.Load())
	}
}

func waitForControllerState(t *testing.T, health *controllerHealth, want controllerState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := health.snapshot().State; got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("controller state = %q, want %q", health.snapshot().State, want)
}
