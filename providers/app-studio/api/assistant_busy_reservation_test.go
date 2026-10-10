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

package api

import (
	"context"
	"testing"

	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

func TestAssistantBusySeesThreadReservationBeforeProjectClaim(t *testing.T) {
	ctx := context.Background()
	shared := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(ctx, shared)
	defer supervisor.Shutdown(ctx)

	storeScope := store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	workspaceScope := workspace.Scope{OrgUUID: storeScope.OrgUUID, WorkspaceUUID: storeScope.WorkspaceUUID, ProjectName: storeScope.ProjectName, ProjectUID: storeScope.ProjectUID}
	threadKey := projectAssistantThreadKey(storeScope, "thread-a")
	supervisor.mu.Lock()
	supervisor.reservations[threadKey] = storeScope
	supervisor.mu.Unlock()

	if _, exists, err := shared.GetReplicaClaim(ctx, store.ActivityClaimKey(storeScope)); err != nil {
		t.Fatal(err)
	} else if exists {
		t.Fatal("test setup unexpectedly persisted the project owner claim")
	}
	server := &Server{store: shared, assistantSupervisor: supervisor}
	if !server.AssistantBusy(workspaceScope) {
		t.Fatal("project was reported idle while a thread reservation awaited its durable project claim")
	}
	if !supervisor.reserved(storeScope, "thread-a") {
		t.Fatal("thread-specific reservation query lost the reservation")
	}
}
