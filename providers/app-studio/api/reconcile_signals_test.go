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
	"testing"
	"time"

	"k8s.io/client-go/util/workqueue"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/railgrid/provider-app-studio/store"
)

func drainSignal(t *testing.T, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) mcreconcile.Request {
	t.Helper()
	got := make(chan mcreconcile.Request, 1)
	go func() {
		item, shutdown := q.Get()
		if !shutdown {
			q.Done(item)
			got <- item
		}
	}()
	select {
	case item := <-got:
		return item
	case <-time.After(2 * time.Second):
		t.Fatal("no reconcile request published")
		return mcreconcile.Request{}
	}
}

func TestSignalsAddressTheWorkspaceClusterLastSeen(t *testing.T) {
	s := &Server{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer sessions.ShutDown()
	projects := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer projects.ShutDown()
	if err := s.SessionSignals().Source().Start(ctx, sessions); err != nil {
		t.Fatal(err)
	}
	if err := s.ProjectSignals().Source().Start(ctx, projects); err != nil {
		t.Fatal(err)
	}
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "shop", ProjectUID: "uid-a"}

	// Before any request from the workspace arrived, its cluster is unknown
	// and the signal is dropped (the reconcilers' resync covers it).
	s.signalSession(scope, "thread-1")
	s.signalProject(scope.WorkspaceUUID, scope.ProjectName)
	if sessions.Len() != 0 || projects.Len() != 0 {
		t.Fatalf("signals for an unknown workspace were enqueued: sessions %d projects %d", sessions.Len(), projects.Len())
	}

	s.noteWorkspaceCluster(identity{workspaceUUID: "ws-a", clusterID: "cluster-a"})
	s.signalSession(scope, "thread-1")
	if req := drainSignal(t, sessions); string(req.ClusterName) != "cluster-a" || req.Name != "thread-1" {
		t.Fatalf("session request = %+v", req)
	}
	s.signalProject(scope.WorkspaceUUID, scope.ProjectName)
	if req := drainSignal(t, projects); string(req.ClusterName) != "cluster-a" || req.Name != "shop" {
		t.Fatalf("project request = %+v", req)
	}

	// Incomplete identities do not poison the index; a nil server is inert.
	s.noteWorkspaceCluster(identity{workspaceUUID: "ws-a"})
	if got := s.clusterForWorkspace("ws-a"); got != "cluster-a" {
		t.Fatalf("clusterForWorkspace = %q", got)
	}
	var nilServer *Server
	nilServer.signalSession(scope, "thread-1")
	nilServer.signalProject("ws-a", "shop")
	if nilServer.SessionSignals() != nil || nilServer.clusterForWorkspace("ws-a") != "" {
		t.Fatal("nil server produced signal state")
	}
}
