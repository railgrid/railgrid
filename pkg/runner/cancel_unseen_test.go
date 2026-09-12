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

package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCancelUnseenFencesDelayedStartAcrossRestart(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{}
	stateDir := t.TempDir()
	r := newTestRunnerAt(t, adapter, stateDir, source, commit)
	start := testStartRequest(commit)
	cancel := CancelRequest{ProtocolVersion: ProtocolVersion, RequestID: "cancel-before-start", TaskID: start.TaskID, AttemptID: start.AttemptID, AttemptEpoch: start.AttemptEpoch}
	receipt, err := r.Cancel(context.Background(), cancel)
	if err != nil || receipt.Phase != PhaseCancelled || receipt.Workdir != "" || receipt.SessionID != "" || !receipt.AcceptedAt.IsZero() {
		t.Fatalf("cancellation: %+v %v", receipt, err)
	}
	for i := 0; i < 2; i++ {
		replay, err := r.Cancel(context.Background(), cancel)
		if err != nil || replay.Cursor != receipt.Cursor || replay.Phase != PhaseCancelled {
			t.Fatalf("replay: %+v %v", replay, err)
		}
		if _, err := r.Start(context.Background(), start); err == nil {
			t.Fatal("delayed start accepted")
		}
		if adapter.runs.Load() != 0 || len(r.state.Reservations) != 0 {
			t.Fatal("cancelled attempt consumed execution resources")
		}
		foreign := cancel
		foreign.TaskID = "another-task"
		if _, err := r.Cancel(context.Background(), foreign); err == nil {
			t.Fatal("foreign identity accepted")
		}
		foreign = cancel
		foreign.AttemptEpoch++
		if _, err := r.Cancel(context.Background(), foreign); err == nil {
			t.Fatal("changed epoch accepted")
		}
		if i == 0 {
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			r = newTestRunnerAt(t, adapter, stateDir, source, commit)
		}
	}
	stale := cancel
	stale.AttemptID = "different-attempt"
	stale.RequestID = "stale-cancel"
	if _, err := r.Cancel(context.Background(), stale); err == nil {
		t.Fatal("same epoch accepted for different attempt")
	}
	inspected, err := r.Inspect(context.Background(), start.AttemptID)
	if err != nil || inspected.Phase != PhaseCancelled {
		t.Fatalf("inspect: %+v %v", inspected, err)
	}
}

func TestCancelUnseenWriteFailureDoesNotAcknowledge(t *testing.T) {
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	start := testStartRequest(commit)
	request := CancelRequest{ProtocolVersion: ProtocolVersion, RequestID: "cancel-write-failure", TaskID: start.TaskID, AttemptID: start.AttemptID, AttemptEpoch: start.AttemptEpoch}
	original := r.store.path
	blocked := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	r.store.path = blocked
	for i := 0; i < 2; i++ {
		if _, err := r.Cancel(context.Background(), request); err == nil {
			t.Fatal("failed persistence acknowledged")
		}
		if _, err := r.Inspect(context.Background(), start.AttemptID); err == nil {
			t.Fatal("undurable receipt exposed")
		}
		if len(r.state.Operations) != 0 || len(r.state.Attempts) != 0 || len(r.state.Events) != 0 {
			t.Fatal("failed write leaked journal state")
		}
	}
	r.store.path = original
	receipt, err := r.Cancel(context.Background(), request)
	if err != nil || receipt.Phase != PhaseCancelled {
		t.Fatalf("retry: %+v %v", receipt, err)
	}
	if _, err := r.Start(context.Background(), start); err == nil {
		t.Fatal("start bypassed successful retry")
	}
}
