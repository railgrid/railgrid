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
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestExecWorkerExitsForEveryUnprovenCleanupFailure(t *testing.T) {
	if !execCleanupRequiresWorkerExit(fmt.Errorf("inspect failed: %w", errExecCleanupUnproven)) {
		t.Fatal("wrapped cleanup sentinel did not require worker exit")
	}
	if execCleanupRequiresWorkerExit(context.Canceled) {
		t.Fatal("ordinary cancellation incorrectly requires worker exit")
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

func TestExecWorkerIdempotencyCallerBindingAndRestartRecovery(t *testing.T) {
	workspace := t.TempDir()
	lock, err := acquireWorkspaceMutationLock(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := newExecWorker(workspace, "token", false)
	if err != nil {
		t.Fatal(err)
	}
	worker.exit = func(int) { t.Error("worker unexpectedly requested exit") }
	req := testWorkerRequest()
	started, err := worker.start(req)
	if err != nil || started.State != "queued" {
		t.Fatalf("start = %+v, %v", started, err)
	}
	duplicate, err := worker.start(req)
	if err != nil || duplicate.SessionID != started.SessionID {
		t.Fatalf("duplicate = %+v, %v", duplicate, err)
	}
	changed := req
	changed.Fingerprint = strings.Repeat("d", 64)
	if _, err := worker.start(changed); err == nil {
		t.Fatal("changed fingerprint was accepted")
	}
	wrongCaller := req
	wrongCaller.Action = "poll"
	wrongCaller.CallerKey = "caller-2"
	if _, err := worker.poll(wrongCaller); err == nil {
		t.Fatal("caller mismatch was accepted")
	}

	// A fresh worker marks the durable queued record interrupted and never
	// redispatches it. The same Start returns that terminal record.
	restarted, err := newExecWorker(workspace, "token", false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.start(req)
	if err != nil || result.State != "failed" || !strings.Contains(result.Stderr, "not redispatched") {
		t.Fatalf("recovered start = %+v, %v", result, err)
	}
	_ = lock.Close()
}

func TestExecWorkerCancelDoesNotWaitForWorkspaceLock(t *testing.T) {
	workspace := t.TempDir()
	lock, err := acquireWorkspaceMutationLock(context.Background(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := newExecWorker(workspace, "token", false)
	if err != nil {
		t.Fatal(err)
	}
	worker.exit = func(int) { t.Error("worker unexpectedly requested exit") }
	req := testWorkerRequest()
	if _, err := worker.start(req); err != nil {
		t.Fatal(err)
	}
	cancelReq := req
	cancelReq.Action = "cancel"
	started := time.Now()
	result, err := worker.cancel(cancelReq)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "canceled" || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("cancel while lock held = %+v after %s", result, time.Since(started))
	}
	_ = lock.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result, err = worker.poll(cancelReq)
		if err == nil && result.State == "canceled" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("canceled record did not remain terminal: %+v, %v", result, err)
}
