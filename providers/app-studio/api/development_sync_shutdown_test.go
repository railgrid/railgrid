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
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/tenant"
	"github.com/railgrid/provider-app-studio/workspace"
)

func TestShutdownCancelsDevelopmentSyncWorkersWaitingForRuntimeOperation(t *testing.T) {
	var syncPosts atomic.Int32
	f := newDevelopmentSyncReadyFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				syncPosts.Add(1)
			}
			next.ServeHTTP(w, r)
		})
	})

	f.project.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}
	if _, err := f.server.workspaces.PutFile(context.Background(), projectWorkspaceScope(f.id, f.project), workspace.PutOptions{
		Path: "web/package.json",
		Data: []byte(`{"scripts":{"dev":"vite"}}`),
	}); err != nil {
		t.Fatal(err)
	}
	f.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		asclient.ProjectGVR:     "ProjectList",
		publishingTestTargetGVR: "InstanceList",
		templateResource.GVR:    "TemplateList",
	},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
			"kind":       "Instance",
			"metadata":   map[string]any{"name": "demo-dev"},
		}},
		publishingTestProjectObject(t, f.project),
		applicationTemplateObject(),
	)
	f.client = asclient.NewFromDynamic(f.dyn)
	f.server.projectClientFor = func(identity) (*asclient.Client, error) { return f.client, nil }
	f.server.tenant = tenant.NewClient(nil)
	f.server.developmentSyncOperationAcquireWait = time.Minute
	f.server.developmentSyncReadyTimeout = 5 * time.Minute

	scope := projectWorkspaceScope(f.id, f.project)
	releaseRuntime, err := f.server.acquireProjectRuntimeOperation(context.Background(), scope)
	if err != nil {
		t.Fatalf("hold runtime operation: %v", err)
	}
	defer releaseRuntime()

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	if !f.server.scheduleDevelopmentSyncAfterMutationWithCompletion(f.id, f.project, projectToolEditFile, func(err error) {
		firstDone <- err
	}) {
		t.Fatal("first development sync was not scheduled")
	}
	if !f.server.scheduleDevelopmentSyncAfterMutationWithCompletion(f.id, f.project, projectToolSelectTemplate, func(err error) {
		secondDone <- err
	}) {
		t.Fatal("queued development sync was not scheduled")
	}

	workerKey := developmentSyncFailureKey(f.id, f.project)
	waitForShutdownWorkers(t, f, workerKey, scope)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	f.server.Shutdown(shutdownCtx)
	if elapsed := time.Since(started); elapsed >= 3*time.Second {
		t.Fatalf("Shutdown took %s, want canceled runtime waiters to stop promptly", elapsed)
	}

	for name, done := range map[string]<-chan error{"active": firstDone, "queued": secondDone} {
		select {
		case err := <-done:
			if err != context.Canceled {
				t.Errorf("%s worker error = %v, want context.Canceled", name, err)
			}
		case <-time.After(time.Second):
			t.Errorf("%s development sync did not finish after Shutdown", name)
		}
	}

	f.server.mu.Lock()
	remainingWorkers := len(f.server.developmentSyncWorkers[workerKey])
	remainingTails := len(f.server.developmentSyncTails)
	f.server.mu.Unlock()
	if remainingWorkers != 0 || remainingTails != 0 {
		t.Errorf("worker state after Shutdown: workers=%d tails=%d, want both empty", remainingWorkers, remainingTails)
	}
	if f.server.scheduleDevelopmentSyncAfterMutationWithCompletion(f.id, f.project, projectToolEditFile, nil) {
		t.Error("development sync was accepted after Shutdown")
	}

	// The test-held token deliberately remains until after Shutdown. Releasing it
	// must not let either canceled worker reach the sandbox sync endpoint.
	releaseRuntime()
	if got := syncPosts.Load(); got != 0 {
		t.Errorf("sandbox sync POST count = %d after Shutdown, want 0", got)
	}
	f.server.projectRuntimeOperationsMu.Lock()
	remainingRuntimeOperations := len(f.server.projectRuntimeOperations)
	f.server.projectRuntimeOperationsMu.Unlock()
	if remainingRuntimeOperations != 0 {
		t.Errorf("runtime operation entries after release = %d, want none", remainingRuntimeOperations)
	}
}

func waitForShutdownWorkers(t *testing.T, f *developmentSyncReadyFixture, workerKey string, scope workspace.Scope) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		f.server.mu.Lock()
		workerCount := len(f.server.developmentSyncWorkers[workerKey])
		f.server.mu.Unlock()
		f.server.projectRuntimeOperationsMu.Lock()
		op := f.server.projectRuntimeOperations[scope]
		waitingForRuntime := op != nil && op.references >= 2
		f.server.projectRuntimeOperationsMu.Unlock()
		if workerCount == 2 && waitingForRuntime {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("workers did not reach the runtime wait: worker count=%d, waiting=%v", workerCount, waitingForRuntime)
		}
	}
}
