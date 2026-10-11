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
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/util/workqueue"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
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
			return fmt.Errorf("%w: %w", errProjectRuntimeOperationContention, err)
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

func TestQueuedDevelopmentSyncDoesNotRetryConflictAfterAttempt(t *testing.T) {
	project := &aiv1alpha1.Project{}
	project.Name, project.UID = "app", "project-uid"
	id := identity{orgUUID: "org", workspaceUUID: "ws"}
	server := &Server{}
	attempts := 0
	err := server.syncDevelopmentAfterMutationWithReservationRetry(context.Background(), id, project, projectActionWorkspaceSync, func(identity, *aiv1alpha1.Project, string) error {
		attempts++
		// A naked store conflict from the body may follow a side effect. Only
		// the runtime-acquisition marker is safe for the worker to replay.
		return store.ErrAssistantRunConflict
	})
	if !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("sync error = %v, want the original conflict", err)
	}
	if attempts != 1 {
		t.Fatalf("sync attempts = %d, want one attempt because the conflict was not marked as pre-side-effect contention", attempts)
	}
}

func TestRuntimeOperationAcquireTimeoutCanRetryAndDoesNotLimitRunningSync(t *testing.T) {
	postStarted := make(chan struct{}, 1)
	f := newDevelopmentSyncReadyFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				postStarted <- struct{}{}
				select {
				case <-time.After(40 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	f.server.developmentSyncReadyTimeout = 500 * time.Millisecond
	f.server.developmentSyncReadyBackoff = time.Millisecond
	scope := projectWorkspaceScope(f.id, f.project)
	release, err := f.server.acquireProjectRuntimeOperation(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	retryCtx, cancelRetry := context.WithCancel(context.Background())
	defer cancelRetry()
	firstAttempt := make(chan error, 1)
	result := make(chan error, 1)
	var attempts atomic.Int32
	go func() {
		result <- f.server.syncDevelopmentAfterMutationWithReservationRetry(retryCtx, f.id, f.project, projectActionWorkspaceSync, func(identity, *aiv1alpha1.Project, string) error {
			attempt := attempts.Add(1)
			acquireCtx, cancelAcquire := context.WithTimeout(retryCtx, 15*time.Millisecond)
			defer cancelAcquire()
			_, syncErr := f.server.syncProjectDevelopmentTargetWithAcquireContext(retryCtx, acquireCtx, f.client, f.id, f.project, f.target)
			if attempt == 1 {
				firstAttempt <- syncErr
			}
			return syncErr
		})
	}()
	select {
	case err := <-firstAttempt:
		if !errors.Is(err, errProjectRuntimeOperationWaitTimeout) {
			t.Fatalf("sync while token held = %v, want a retryable acquisition timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sync attempt did not time out while waiting for the held runtime token")
	}
	select {
	case <-postStarted:
		t.Fatal("sync posted to the runtime before acquiring its local token")
	default:
	}
	release()

	// The queued worker retries after the real token timeout. Once it acquires
	// the token, the POST takes longer than the acquisition budget but retains
	// its separate operation context and completes successfully.
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("sync after runtime token became available: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued sync did not retry after the runtime token became available")
	}
	if got := attempts.Load(); got < 2 {
		t.Fatalf("sync attempts = %d, want at least one real timeout and a retry", got)
	}
	select {
	case <-postStarted:
	default:
		t.Fatal("sync did not reach the runtime")
	}
}

func TestQueuedDevelopmentSyncContentionExpiryIsVisible(t *testing.T) {
	server := &Server{developmentSyncReadyTimeout: 35 * time.Millisecond, developmentSyncReadyBackoff: 5 * time.Millisecond}
	project := &aiv1alpha1.Project{}
	project.Name, project.UID = "app", "project-uid"
	id := identity{orgUUID: "org", workspaceUUID: "ws"}
	attempts := 0
	err := server.syncDevelopmentAfterMutationWithReservationRetry(context.Background(), id, project, projectActionWorkspaceSync, func(identity, *aiv1alpha1.Project, string) error {
		attempts++
		return fmt.Errorf("%w: %w", errProjectRuntimeOperationContention, store.ErrAssistantRunConflict)
	})
	if !errors.Is(err, errProjectRuntimeOperationContention) {
		t.Fatalf("sync error = %v, want exhausted contention error", err)
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d, want at least one retry before expiry", attempts)
	}
	failure := server.lastDevelopmentSyncFailure(id, project)
	if !strings.Contains(failure, "could not acquire") || strings.Contains(failure, "previous code") {
		t.Fatalf("recorded failure = %q, want visible bounded contention without claiming the sandbox state", failure)
	}
}

func TestDeletedProjectCancelsQueuedDevelopmentSyncsByUID(t *testing.T) {
	ctx := context.Background()
	server := NewWithWorkspace(nil, nil, nil, "", false)
	server.developmentSyncReadyTimeout = time.Minute
	server.developmentSyncReadyBackoff = 30 * time.Second
	oldProject := &aiv1alpha1.Project{}
	oldProject.Name, oldProject.UID = "app", "old-uid"
	id := identity{orgUUID: "org", workspaceUUID: "ws", clusterID: "cluster-a"}
	firstAttempt := make(chan struct{}, 1)
	var attempts atomic.Int32
	server.developmentSyncAfterMutation = func(_ identity, p *aiv1alpha1.Project, _ string) error {
		attempts.Add(1)
		if p.UID == oldProject.UID {
			firstAttempt <- struct{}{}
			return fmt.Errorf("%w: %w", errProjectRuntimeOperationContention, store.ErrAssistantRunConflict)
		}
		return nil
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	if !server.scheduleDevelopmentSyncAfterMutationWithCompletion(id, oldProject, projectActionWorkspaceSync, func(err error) { firstDone <- err }) {
		t.Fatal("first sync was not scheduled")
	}
	select {
	case <-firstAttempt:
	case <-time.After(time.Second):
		t.Fatal("first sync did not begin")
	}
	if !server.scheduleDevelopmentSyncAfterMutationWithCompletion(id, oldProject, projectActionWorkspaceSync, func(err error) { secondDone <- err }) {
		t.Fatal("queued sync was not scheduled")
	}

	projects := server.ProjectSignals()
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer queue.ShutDown()
	signalCtx, cancelSignals := context.WithCancel(ctx)
	defer cancelSignals()
	if err := projects.Source().Start(signalCtx, queue); err != nil {
		t.Fatal(err)
	}
	server.noteWorkspaceCluster(id)
	if err := server.StopAssistantForDeletedProject(ctx, workspace.Scope{
		OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID,
		ProjectName: oldProject.Name, ProjectUID: string(oldProject.UID),
	}); err != nil {
		t.Fatalf("StopAssistantForDeletedProject: %v", err)
	}
	for name, done := range map[string]<-chan error{"active": firstDone, "queued": secondDone} {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s sync error = %v, want cancellation", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s sync did not stop after deletion cancellation", name)
		}
	}
	request := drainSignal(t, queue)
	if string(request.ClusterName) != id.clusterID || request.Name != oldProject.Name {
		t.Fatalf("deletion completion signal = %+v, want %s/%s", request, id.clusterID, oldProject.Name)
	}
	server.mu.Lock()
	oldWorkers := len(server.developmentSyncWorkers[developmentSyncFailureKey(identity{orgUUID: id.orgUUID, workspaceUUID: id.workspaceUUID}, oldProject)])
	server.mu.Unlock()
	if oldWorkers != 0 {
		t.Fatalf("old Project UID retained %d development sync workers after cancellation", oldWorkers)
	}

	newProject := oldProject.DeepCopy()
	newProject.UID = "new-uid"
	newDone := make(chan error, 1)
	if !server.scheduleDevelopmentSyncAfterMutationWithCompletion(id, newProject, projectActionWorkspaceSync, func(err error) { newDone <- err }) {
		t.Fatal("replacement Project sync was not scheduled")
	}
	select {
	case err := <-newDone:
		if err != nil {
			t.Fatalf("replacement Project sync error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement Project sync did not complete")
	}
	if got := attempts.Load(); got < 2 {
		t.Fatalf("sync attempts = %d, want old attempt plus replacement attempt", got)
	}
}
