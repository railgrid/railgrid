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
	"errors"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

func TestRuntimeOperationKeepsDurableProjectOwner(t *testing.T) {
	ctx := context.Background()
	shared := store.NewMemoryStore()
	first, second := &Server{store: shared}, &Server{store: shared}
	first.projectAssistantSupervisor().SetReplicaIdentity("first", "")
	second.projectAssistantSupervisor().SetReplicaIdentity("second", "")
	defer first.projectAssistantSupervisor().Shutdown(ctx)
	defer second.projectAssistantSupervisor().Shutdown(ctx)
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	release, err := first.acquireProjectRuntimeOperation(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.acquireProjectRuntimeOperation(ctx, scope); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("foreign runtime owner = %v", err)
	}
	release()
	releaseSecond, err := second.acquireProjectRuntimeOperation(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	releaseSecond()
	storeScope := store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID}
	releaseExclusive, err := first.projectAssistantSupervisor().ReserveWorkspace(ctx, storeScope)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseExclusive()
	if _, err := first.acquireProjectRuntimeOperation(ctx, scope); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("unreserved nested runtime = %v", err)
	}
	releaseNested, err := first.acquireProjectRuntimeOperation(contextWithProjectRuntimeOwner(ctx, scope), scope)
	if err != nil {
		t.Fatal(err)
	}
	releaseNested()
}

func TestRuntimeOperationSerializesProjectAndAllowsOtherProject(t *testing.T) {
	s := &Server{}
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	release, err := s.acquireProjectRuntimeOperation(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.acquireProjectRuntimeOperation(waiting, scope); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contending runtime operation = %v", err)
	}
	other := scope
	other.ProjectUID = "other-uid"
	releaseOther, err := s.acquireProjectRuntimeOperation(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	releaseOther()
	release()
	releaseAgain, err := s.acquireProjectRuntimeOperation(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	releaseAgain()
	if len(s.projectRuntimeOperations) != 0 {
		t.Fatal("runtime operation references leaked")
	}
}

func TestRuntimeOperationRejectsAlreadyCanceledRequest(t *testing.T) {
	s := &Server{}
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 100 {
		if release, err := s.acquireProjectRuntimeOperation(ctx, scope); !errors.Is(err, context.Canceled) || release != nil {
			t.Fatalf("canceled runtime operation = %v, release present = %t", err, release != nil)
		}
	}
	if len(s.projectRuntimeOperations) != 0 {
		t.Fatal("canceled runtime operation references leaked")
	}
}
