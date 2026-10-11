/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"errors"
	"testing"
	"time"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/store"
)

func TestQueuedDevelopmentSyncRetriesExclusiveWorkspaceLease(t *testing.T) {
	ctx := context.Background()
	server := &Server{
		store:                       store.NewMemoryStore(),
		developmentSyncReadyTimeout: time.Second,
		developmentSyncReadyBackoff: 5 * time.Millisecond,
	}
	server.projectAssistantSupervisor().SetReplicaIdentity("sync-test", "")
	defer server.projectAssistantSupervisor().Shutdown(ctx)

	project := &aiv1alpha1.Project{}
	project.Name, project.UID = "app", "project-uid"
	id := identity{orgUUID: "org", workspaceUUID: "ws"}
	scope := projectWorkspaceScope(id, project)
	releaseExclusive, err := server.projectAssistantSupervisor().ReserveWorkspace(ctx, store.Scope{
		OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID,
		ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer releaseExclusive()

	firstAttempt := make(chan error, 1)
	completed := make(chan error, 1)
	attempts := 0
	server.developmentSyncAfterMutation = func(_ identity, _ *aiv1alpha1.Project, _ string) error {
		attempts++
		releaseRuntime, err := server.acquireProjectRuntimeOperation(context.Background(), scope)
		if attempts == 1 {
			firstAttempt <- err
		}
		if err != nil {
			return err
		}
		releaseRuntime()
		return nil
	}
	if !server.scheduleDevelopmentSyncAfterMutationWithCompletion(id, project, projectActionWorkspaceSync, func(err error) {
		completed <- err
	}) {
		t.Fatal("workspace sync was not scheduled")
	}
	select {
	case err := <-firstAttempt:
		if !errors.Is(err, store.ErrAssistantRunConflict) {
			t.Fatalf("first sync attempt under exclusive lease = %v, want ErrAssistantRunConflict", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued sync did not attempt while the exclusive lease was held")
	}

	// The handler releases its exclusive reservation after it has queued the
	// background sync. The worker must reacquire normally and then complete.
	releaseExclusive()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("queued sync after exclusive lease release = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued sync did not retry after the exclusive lease was released")
	}
	if attempts < 2 {
		t.Fatalf("sync attempts = %d, want a retry after the lease conflict", attempts)
	}
}
