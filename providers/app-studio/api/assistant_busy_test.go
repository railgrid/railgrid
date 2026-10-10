// Copyright 2026 The Railgrid Authors.
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

package api

import (
	"context"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

func TestStopAssistantForDeletedProjectStopsEveryThread(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	t.Cleanup(func() { server.assistantSupervisor.Shutdown(context.Background()) })
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	wsScope := workspace.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	now := time.Now().UTC()
	started := make(chan string, 2)
	finished := make(chan string, 2)
	for _, threadID := range []string{"thread-a", "thread-b"} {
		run := store.AssistantRun{
			ID: "run-" + threadID, ThreadID: threadID, Mode: store.AssistantRunModeDefault,
			Status: store.AssistantRunStatusRunning, ClientRequestID: "request-" + threadID,
			UserMessageID: "user-" + threadID, ActiveMessageID: "assistant-" + threadID,
			Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
		user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "actor-a", Content: "build", CreatedAt: now, UpdatedAt: now}
		assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now.Add(time.Microsecond), UpdatedAt: now.Add(time.Microsecond)}
		if _, err := messages.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
			t.Fatalf("create run for %s: %v", threadID, err)
		}
		threadID := threadID
		if err := server.assistantSupervisor.Start(context.Background(), scope, run, assistant, func(ctx context.Context, _ *projectAssistantSnapshotAccumulator) {
			started <- threadID
			<-ctx.Done()
			_, _ = server.assistantSupervisor.AbortWith(scope, run.ID, nil, threadID)
			finished <- threadID
		}); err != nil {
			t.Fatalf("start run for %s: %v", threadID, err)
		}
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("both thread runs did not start")
		}
	}

	if err := server.StopAssistantForDeletedProject(context.Background(), wsScope); err != nil {
		t.Fatalf("StopAssistantForDeletedProject: %v", err)
	}
	stopped := map[string]bool{}
	for range 2 {
		select {
		case threadID := <-finished:
			stopped[threadID] = true
		case <-time.After(time.Second):
			t.Fatal("project deletion did not stop every thread run")
		}
	}
	if !stopped["thread-a"] || !stopped["thread-b"] {
		t.Fatalf("stopped threads = %#v, want both thread-a and thread-b", stopped)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		active, err := messages.ListActiveAssistantRuns(context.Background(), scope)
		if err != nil {
			t.Fatalf("ListActiveAssistantRuns: %v", err)
		}
		if len(active) == 0 && !server.AssistantBusy(wsScope) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("project remained busy after all thread workers settled")
}
