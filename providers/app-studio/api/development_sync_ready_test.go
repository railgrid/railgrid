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
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/workspace"
)

// developmentSyncReadyFixture is a project with one component, a fake data
// plane and a fake cluster, wired to a Server with a short readiness window.
type developmentSyncReadyFixture struct {
	server  *Server
	client  *asclient.Client
	dyn     *fake.FakeDynamicClient
	agent   *fakeSkipAgent
	id      identity
	project *aiv1alpha1.Project
	target  projectDevelopmentSyncTargetInfo
}

func newDevelopmentSyncReadyFixture(t *testing.T, handler func(http.Handler) http.Handler) *developmentSyncReadyFixture {
	t.Helper()
	workspaces := workspace.NewFileStore(t.TempDir())
	project := &aiv1alpha1.Project{}
	project.Name, project.UID = "demo", "uid"
	id := identity{clusterID: "cluster-a", orgUUID: "org-a", workspaceUUID: "ws-a"}
	if _, err := workspaces.PutFile(context.Background(), projectWorkspaceScope(id, project), workspace.PutOptions{Path: "web/index.html", Data: []byte("<html></html>")}); err != nil {
		t.Fatal(err)
	}
	agent := &fakeSkipAgent{
		encodings: map[string][]string{"web": {"utf-8"}},
		received:  map[string][]projectSandboxSyncFile{},
	}
	var h http.Handler = agent
	if handler != nil {
		h = handler(agent)
	}
	hub := httptest.NewServer(h)
	t.Cleanup(hub.Close)

	dyn := publishingTestDynamic(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
		"kind":       "Instance",
		"metadata":   map[string]any{"name": "demo-dev"},
	}})
	return &developmentSyncReadyFixture{
		server: &Server{
			tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders,
			hubBase: hub.URL, callers: newTestCallers(nil, hub.URL),
			workspaces:                  workspaces,
			developmentSyncReadyTimeout: 2 * time.Second,
			developmentSyncReadyBackoff: 5 * time.Millisecond,
		},
		client:  asclient.NewFromDynamic(dyn),
		dyn:     dyn,
		agent:   agent,
		id:      id,
		project: project,
		target: projectDevelopmentSyncTargetInfo{
			ResourceName: "demo-dev",
			Resource:     "instances",
			Kind:         "Instance",
			APIVersion:   "infrastructure.railgrid.ai/v1alpha1",
			Components:   map[string]projectTemplateComponent{"web": {WorkspacePath: "web"}},
		},
	}
}

// instanceMissingFor makes the first n Gets of the Instance return NotFound,
// as when the sync runs before the Project reconciler has created it. It
// returns the number of Gets seen.
func (f *developmentSyncReadyFixture) instanceMissingFor(n int32) *atomic.Int32 {
	var gets atomic.Int32
	f.dyn.PrependReactor("get", "instances", func(k8stesting.Action) (bool, runtime.Object, error) {
		if gets.Add(1) <= n {
			return true, nil, apierrors.NewNotFound(publishingTestTargetGVR.GroupResource(), "demo-dev")
		}
		return false, nil, nil
	})
	return &gets
}

func (f *developmentSyncReadyFixture) sync() error {
	_, err := f.server.syncProjectDevelopmentTargetWhenReady(context.Background(), f.client, f.id, f.project, f.target)
	return err
}

// The regression: select_project_template schedules the sync before the
// template instance exists. The sync must wait for it rather than give up and
// leave the sandbox without the scaffold.
func TestSyncProjectDevelopmentTargetWhenReadyWaitsForInstance(t *testing.T) {
	f := newDevelopmentSyncReadyFixture(t, nil)
	gets := f.instanceMissingFor(3)

	if err := f.sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := gets.Load(); got != 4 {
		t.Fatalf("instance fetched %d times, want 3 misses then a hit", got)
	}
	if got := f.agent.received["web"]; len(got) != 1 || got[0].Path != "index.html" {
		t.Fatalf("web received %+v, want the scaffold", got)
	}
}

func TestSyncProjectDevelopmentTargetWhenReadyGivesUpAtDeadline(t *testing.T) {
	f := newDevelopmentSyncReadyFixture(t, nil)
	f.server.developmentSyncReadyTimeout = 30 * time.Millisecond
	f.instanceMissingFor(1 << 30)

	start := time.Now()
	err := f.sync()
	if !apierrors.IsNotFound(err) {
		t.Fatalf("sync error = %v, want the NotFound kept for classification", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("sync took %s, want it bounded by the readiness timeout", elapsed)
	}
	if len(f.agent.received) != 0 {
		t.Fatalf("agent received %+v without an instance", f.agent.received)
	}
}

func TestSyncProjectDevelopmentTargetWhenReadyRetriesUnavailableComponent(t *testing.T) {
	var syncs atomic.Int32
	f := newDevelopmentSyncReadyFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && syncs.Add(1) <= 2 {
				http.Error(w, "component is starting", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	if err := f.sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := syncs.Load(); got != 3 {
		t.Fatalf("sync posted %d times, want two 503s then success", got)
	}
}

func TestSyncProjectDevelopmentTargetWhenReadyDoesNotRetryConflicts(t *testing.T) {
	var syncs atomic.Int32
	f := newDevelopmentSyncReadyFixture(t, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				syncs.Add(1)
				http.Error(w, "source revision 3 is older than applied revision 5", http.StatusConflict)
				return
			}
			_, _ = w.Write([]byte(`{"running":true,"syncEncodings":["utf-8"]}`))
		})
	})

	if err := f.sync(); err == nil {
		t.Fatal("sync succeeded against a revision conflict")
	}
	if got := syncs.Load(); got != 1 {
		t.Fatalf("sync posted %d times, want a revision conflict to fail without retrying", got)
	}
}
