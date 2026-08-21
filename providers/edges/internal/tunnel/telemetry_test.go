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

package tunnel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clientgotesting "k8s.io/client-go/testing"
	"k8s.io/klog/v2"

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"

	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"
)

type telemetryRecorder struct {
	mu     sync.Mutex
	events []producttelemetry.Event
	errs   []error
}

func (r *telemetryRecorder) Track(_ context.Context, event producttelemetry.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	if len(r.errs) == 0 {
		return nil
	}
	err := r.errs[0]
	r.errs = r.errs[1:]
	return err
}

func (r *telemetryRecorder) Close() error { return nil }

func (r *telemetryRecorder) snapshot() []producttelemetry.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]producttelemetry.Event(nil), r.events...)
}

var telemetryTestKinds = []KindConfig{
	{GVR: schema.GroupVersionResource{Group: "edges.faros.sh", Version: "v1alpha1", Resource: "kubernetesclusters"}, Kind: "KubernetesCluster"},
	{GVR: schema.GroupVersionResource{Group: "edges.faros.sh", Version: "v1alpha1", Resource: "linuxservers"}, Kind: "LinuxServer"},
}

func newTelemetryStatusServer(t *testing.T, tracker producttelemetry.Tracker, gvr schema.GroupVersionResource, object *unstructured.Unstructured) (*Server, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "EdgeList"},
		object,
	)
	srv, err := New(Config{Kinds: telemetryTestKinds, Logger: klog.Background(), Telemetry: tracker})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	srv.tenantConfig = func(context.Context, string) (*rest.Config, error) { return &rest.Config{}, nil }
	srv.dynamicClientFor = func(*rest.Config) (dynamic.Interface, error) { return dyn, nil }
	return srv, dyn
}

func telemetryEdgeObject(gvr schema.GroupVersionResource, name, cluster string, connected bool, phase string) *unstructured.Unstructured {
	kind := "Edge"
	switch gvr.Resource {
	case "kubernetesclusters":
		kind = "KubernetesCluster"
	case "linuxservers":
		kind = "LinuxServer"
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": gvr.GroupVersion().String(),
		"kind":       kind,
		"metadata": map[string]interface{}{
			"name": name,
		},
		"spec": map[string]interface{}{
			"cluster": cluster,
		},
		"status": map[string]interface{}{
			"connected": connected,
			"phase":     phase,
		},
	}}
}

func TestMarkEdgeConnectedTracksFirstReadyForBothKinds(t *testing.T) {
	const cluster = "11tcw27t4rdtnacy"
	for _, tc := range telemetryTestKinds {
		t.Run(tc.Kind, func(t *testing.T) {
			const name = "edge-one"
			recorder := &telemetryRecorder{}
			object := telemetryEdgeObject(tc.GVR, name, cluster, false, string(edgeapi.ConnectionPhaseDisconnected))
			srv, dyn := newTelemetryStatusServer(t, recorder, tc.GVR, object)

			srv.markEdgeConnected(context.Background(), tc.GVR, cluster, name, nil, false)

			got, err := dyn.Resource(tc.GVR).Get(context.Background(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if !edgeStatusReady(got.Object) {
				t.Fatalf("status = %#v, want connected=true phase=Ready", got.Object["status"])
			}
			events := recorder.snapshot()
			if len(events) != 1 {
				t.Fatalf("telemetry events = %d, want 1", len(events))
			}
			event := events[0]
			if event.Action != edgesFirstReadyAction || event.ScopeID != cluster || event.OrgID != "" || event.WorkspaceID != "" {
				t.Fatalf("event identity = %#v", event)
			}
			if event.Actor != "" {
				t.Fatalf("event actor = %q, want empty", event.Actor)
			}
			if event.ResourceID != edgeResourceID(cluster, tc.GVR, name) || event.ResourceID == name || len(event.ResourceID) != 64 {
				t.Fatalf("resource ID = %q, want opaque domain-separated digest", event.ResourceID)
			}
			if len(event.Properties) != 2 || event.Properties["edge_type"] != map[string]string{
				"KubernetesCluster": "kubernetes_cluster",
				"LinuxServer":       "linux_server",
			}[tc.Kind] || event.Properties["outcome"] != "ready" {
				t.Fatalf("event properties = %#v", event.Properties)
			}
		})
	}
}

func TestMarkEdgeConnectedDoesNotTrackWhenStatusUpdateFails(t *testing.T) {
	gvr := telemetryTestKinds[0].GVR
	recorder := &telemetryRecorder{}
	object := telemetryEdgeObject(gvr, "edge-one", "tenant-a", false, string(edgeapi.ConnectionPhaseDisconnected))
	srv, dyn := newTelemetryStatusServer(t, recorder, gvr, object)
	dyn.PrependReactor("update", gvr.Resource, func(_ clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("status update failed")
	})

	srv.markEdgeConnected(context.Background(), gvr, "tenant-a", "edge-one", nil, false)
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("telemetry events after failed status update = %#v, want none", events)
	}
}

func TestMarkEdgeConnectedSkipsAlreadyReadyAndReconnects(t *testing.T) {
	gvr := telemetryTestKinds[0].GVR
	recorder := &telemetryRecorder{}
	object := telemetryEdgeObject(gvr, "edge-one", "tenant-a", true, string(edgeapi.ConnectionPhaseReady))
	srv, _ := newTelemetryStatusServer(t, recorder, gvr, object)

	srv.markEdgeConnected(context.Background(), gvr, "tenant-a", "edge-one", nil, false)
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("already-ready telemetry events = %#v, want none", events)
	}

	// A first ready transition in this process claims the tenant, and repeated
	// status updates/reconnects remain deduplicated.
	recorder2 := &telemetryRecorder{}
	obj2 := telemetryEdgeObject(gvr, "edge-one", "tenant-a", false, string(edgeapi.ConnectionPhaseDisconnected))
	srv2, dyn2 := newTelemetryStatusServer(t, recorder2, gvr, obj2)
	srv2.markEdgeConnected(context.Background(), gvr, "tenant-a", "edge-one", nil, false)
	current2, err := dyn2.Resource(gvr).Get(context.Background(), "edge-one", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("fresh ready Get() error = %v", err)
	}
	second := telemetryEdgeObject(gvr, "edge-two", "tenant-a", false, string(edgeapi.ConnectionPhaseDisconnected))
	if _, err := dyn2.Resource(gvr).Create(context.Background(), second, metav1.CreateOptions{}); err != nil {
		t.Fatalf("second edge Create() error = %v", err)
	}
	srv2.markEdgeConnected(context.Background(), gvr, "tenant-a", "edge-two", nil, false)
	if events := recorder2.snapshot(); len(events) != 1 {
		t.Fatalf("same-tenant second edge events = %d, want 1", len(events))
	}
	status2, _, _ := unstructured.NestedMap(current2.Object, "status")
	status2["connected"] = false
	status2["phase"] = string(edgeapi.ConnectionPhaseDisconnected)
	if _, err := dyn2.Resource(gvr).UpdateStatus(context.Background(), current2, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("disconnect UpdateStatus() error = %v", err)
	}
	srv2.markEdgeConnected(context.Background(), gvr, "tenant-a", "edge-one", nil, false)
	if events := recorder2.snapshot(); len(events) != 1 {
		t.Fatalf("first-ready plus reconnect events = %d, want 1", len(events))
	}
	srv2.markEdgeConnected(context.Background(), gvr, "tenant-a", "edge-one", nil, false)
	if events := recorder2.snapshot(); len(events) != 1 {
		t.Fatalf("repeated reconnect events = %d, want 1", len(events))
	}
}

func TestEdgeReadyTelemetryRetriesAfterTrackError(t *testing.T) {
	gvr := telemetryTestKinds[1].GVR
	recorder := &telemetryRecorder{errs: []error{errors.New("queue full")}}
	object := telemetryEdgeObject(gvr, "server-one", "tenant-a", false, string(edgeapi.ConnectionPhaseDisconnected))
	srv, _ := newTelemetryStatusServer(t, recorder, gvr, object)

	// The status transition succeeds, but the failed Track must not consume the
	// process-local claim. The second already-Ready call retries and succeeds.
	srv.markEdgeConnected(context.Background(), gvr, "tenant-a", "server-one", nil, false)
	srv.markEdgeConnected(context.Background(), gvr, "tenant-a", "server-one", nil, false)
	if events := recorder.snapshot(); len(events) != 2 {
		t.Fatalf("Track attempts = %d, want failed attempt plus retry", len(events))
	}
	srv.markEdgeConnected(context.Background(), gvr, "tenant-a", "server-one", nil, false)
	if events := recorder.snapshot(); len(events) != 2 {
		t.Fatalf("Track attempts after successful retry = %d, want 2", len(events))
	}
}

func TestNewServerDefaultsEdgeTelemetryToNoop(t *testing.T) {
	srv, err := New(Config{Kinds: telemetryTestKinds, Logger: klog.Background()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := srv.readyTelemetry.tracker.(producttelemetry.NoopTracker); !ok {
		t.Fatalf("default tracker = %T, want telemetry.NoopTracker", srv.readyTelemetry.tracker)
	}
	if got := edgeResourceID("tenant-a", telemetryTestKinds[0].GVR, "edge-one"); strings.Contains(got, "edge-one") {
		t.Fatalf("resource ID contains raw edge name: %q", got)
	}
}
