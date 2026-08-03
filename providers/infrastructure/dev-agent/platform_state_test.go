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
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProtectedPlatformStateFailsClosedWhenMountIsMissing(t *testing.T) {
	workspace := t.TempDir()
	if _, err := acquireWorkspaceMutationLock(t.Context(), workspace, true); err == nil {
		t.Fatal("protected lock acquisition recreated missing platform state")
	}
	if _, err := newExecWorker(workspace, "token", true); err == nil || !strings.Contains(err.Error(), "protected platform state") {
		t.Fatalf("protected worker startup error = %v, want missing protected state rejection", err)
	}
	if _, err := acquireWorkspaceMutationLock(t.Context(), workspace, false); err != nil {
		t.Fatalf("unprotected unit fixture lock: %v", err)
	}
	if _, err := acquireWorkspaceMutationLock(t.Context(), workspace, true); err == nil {
		t.Fatal("protected lock acquisition accepted caller-owned replacement state")
	}
}

func TestSyncUsesPVCBackedMutationLockAndRejectsPlatformMetadata(t *testing.T) {
	workspace := t.TempDir()
	server := newTestAgent(t, &agentConfig{WorkDir: workspace, ControlToken: "test-token"})
	lock, err := acquireWorkspaceMutationLock(t.Context(), workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		recorder, _ := doSync(t, server, syncRequest{Files: []syncFile{{Path: "main.go", Content: "package main\n"}}})
		done <- recorder.Code
	}()
	select {
	case status := <-done:
		t.Fatalf("sync completed with %d while exec lock was held", status)
	case <-time.After(50 * time.Millisecond):
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Fatalf("sync status = %d after lock release", status)
		}
	case <-time.After(time.Second):
		t.Fatal("sync did not resume after workspace lock release")
	}

	recorder, _ := doSync(t, server, syncRequest{Files: []syncFile{{Path: platformMetadataDir + "/forged.json", Content: "{}"}}})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("platform metadata sync status = %d, want 400", recorder.Code)
	}
}
