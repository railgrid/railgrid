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
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCoordinatorStateIsRequiredAndIndependentFromWorkspace(t *testing.T) {
	workspace := t.TempDir()
	for _, stateDir := range []string{"", workspace, filepath.Join(workspace, "state")} {
		if _, err := prepareCoordinatorState(stateDir, workspace); err == nil {
			t.Fatalf("state directory %q was accepted", stateDir)
		}
	}
	parentState := t.TempDir()
	if _, err := prepareCoordinatorState(parentState, filepath.Join(parentState, "workspace")); err == nil {
		t.Fatal("state directory containing the workspace was accepted")
	}
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o770); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareCoordinatorState(stateDir, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if prepared == workspace {
		t.Fatal("coordinator state aliases workspace")
	}
	if _, err := os.Stat(filepath.Join(prepared, stateSessionsDir)); err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if rootInfo.Mode().Perm() != 0o770 {
		t.Fatalf("fsGroup-style mount mode changed to %o", rootInfo.Mode().Perm())
	}
	sessionsInfo, err := os.Stat(filepath.Join(prepared, stateSessionsDir))
	if err != nil {
		t.Fatal(err)
	}
	if sessionsInfo.Mode().Perm() != 0o700 {
		t.Fatalf("coordinator-owned sessions mode = %o", sessionsInfo.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(workspace, ".faros-platform")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace platform state exists: %v", err)
	}
}

func TestSyncUsesCoordinatorMutationLock(t *testing.T) {
	workspace := t.TempDir()
	server := newTestAgent(t, &agentConfig{WorkDir: workspace, ControlToken: "test-token"})
	server.mutationMu = &sync.Mutex{}
	server.mutationMu.Lock()
	done := make(chan int, 1)
	go func() {
		recorder, _ := doSync(t, server, syncRequest{Files: []syncFile{{Path: "main.go", Content: "package main\n"}}})
		done <- recorder.Code
	}()
	select {
	case status := <-done:
		t.Fatalf("sync completed with %d while mutation lock was held", status)
	case <-time.After(50 * time.Millisecond):
	}
	server.mutationMu.Unlock()
	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Fatalf("sync status = %d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("sync did not resume after lock release")
	}
	if _, err := os.Stat(filepath.Join(workspace, ".faros-platform")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sync created workspace platform state: %v", err)
	}
}
