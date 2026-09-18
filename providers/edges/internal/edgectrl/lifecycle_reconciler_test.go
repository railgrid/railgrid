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

package edgectrl

import (
	"context"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
	edgeapi "github.com/railgrid/provider-edges/internal/edgeapi"
	"github.com/railgrid/provider-edges/internal/tunnel"
)

const (
	testCluster = "1ngen6o0so3jwz2h"
	testEdge    = "build-01"
)

// fakeRegistry is a TunnelRegistry with a scripted observation per key.
type fakeRegistry struct {
	obs map[string]tunnel.TunnelObservation
}

func (f *fakeRegistry) Observe(_ context.Context, key string) (tunnel.TunnelObservation, error) {
	return f.obs[key], nil
}

func heldAt(renew time.Time, holder string) tunnel.TunnelObservation {
	return tunnel.TunnelObservation{Held: true, Holder: holder, RenewTime: renew, ExpiresAt: renew.Add(tunnel.RegistryLeaseTTL)}
}

func newLifecycleClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := edgesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&edgesv1alpha1.LinuxServer{}).
		Build()
}

func linuxServer(mutate ...func(*edgesv1alpha1.LinuxServer)) *edgesv1alpha1.LinuxServer {
	ls := &edgesv1alpha1.LinuxServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:   testEdge,
			Labels: map[string]string{edgesv1alpha1.LabelName: testEdge},
		},
	}
	for _, m := range mutate {
		m(ls)
	}
	return ls
}

func newReconciler(reg TunnelRegistry, now time.Time) *LifecycleReconciler {
	return &LifecycleReconciler{
		registry: reg,
		newObj:   edgesv1alpha1.NewLinuxServer,
		resource: edgesv1alpha1.LinuxServerResource,
		now:      func() time.Time { return now },
	}
}

func getLinuxServer(t *testing.T, c client.Client) *edgesv1alpha1.LinuxServer {
	t.Helper()
	ls := &edgesv1alpha1.LinuxServer{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: testEdge}, ls); err != nil {
		t.Fatal(err)
	}
	return ls
}

func TestFreshLeaseConnectsAndRegistersTheEdge(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	renew := now.Add(-20 * time.Second).Add(123 * time.Microsecond)
	key := tunnel.EdgeConnKey(edgesv1alpha1.LinuxServerResource, testCluster, testEdge)
	reg := &fakeRegistry{obs: map[string]tunnel.TunnelObservation{key: heldAt(renew, "10.0.0.7:8090")}}

	c := newLifecycleClient(t, linuxServer(func(ls *edgesv1alpha1.LinuxServer) {
		ls.Status.Phase = edgeapi.ConnectionPhaseScheduling
		ls.Status.AgentVersion = "v1.2.3" // agent-owned, must survive
		meta.SetStatusCondition(&ls.Status.Conditions, metav1.Condition{
			Type: edgeapi.ConnectionConditionRegistered, Status: metav1.ConditionFalse, Reason: "AwaitingAgent",
		})
	}))
	r := newReconciler(reg, now)

	res, err := r.reconcileEdge(context.Background(), c, getLinuxServer(t, c), testCluster)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getLinuxServer(t, c)
	if !got.Status.Connected || got.Status.Phase != edgeapi.ConnectionPhaseReady {
		t.Fatalf("status = connected:%v phase:%q, want connected Ready", got.Status.Connected, got.Status.Phase)
	}
	if got.Status.LastHeartbeatTime == nil || !got.Status.LastHeartbeatTime.Time.Equal(renew.Truncate(time.Second)) {
		t.Fatalf("lastHeartbeatTime = %v, want lease renewTime %v", got.Status.LastHeartbeatTime, renew.Truncate(time.Second))
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, edgeapi.ConnectionConditionRegistered) {
		t.Fatalf("Registered condition not True: %+v", got.Status.Conditions)
	}
	if got.Status.AgentVersion != "v1.2.3" {
		t.Fatalf("agent-owned agentVersion clobbered: %q", got.Status.AgentVersion)
	}
	// Re-check is scheduled at lease expiry, not on a fixed 30s tick.
	wantRequeue := renew.Add(tunnel.RegistryLeaseTTL).Sub(now) + leaseExpirySlack
	if res.RequeueAfter != wantRequeue {
		t.Fatalf("RequeueAfter = %v, want lease expiry %v", res.RequeueAfter, wantRequeue)
	}
}

func TestLeaseRenewalOnlyPatchesTheHeartbeat(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	first := now.Add(-40 * time.Second)
	second := now.Add(-10 * time.Second)
	key := tunnel.EdgeConnKey(edgesv1alpha1.LinuxServerResource, testCluster, testEdge)
	reg := &fakeRegistry{obs: map[string]tunnel.TunnelObservation{key: heldAt(second, "10.0.0.7:8090")}}

	firstHB := metav1.NewTime(first)
	c := newLifecycleClient(t, linuxServer(func(ls *edgesv1alpha1.LinuxServer) {
		ls.Status.Connected = true
		ls.Status.Phase = edgeapi.ConnectionPhaseReady
		ls.Status.LastHeartbeatTime = &firstHB
		ls.Status.Labels = map[string]string{"zone": "a"} // agent-owned
		ls.Status.Hostname = "build-01.local"             // handler-owned
		meta.SetStatusCondition(&ls.Status.Conditions, metav1.Condition{
			Type: edgeapi.ConnectionConditionRegistered, Status: metav1.ConditionTrue, Reason: "AgentRegistered",
		})
	}))
	before := getLinuxServer(t, c)
	registeredBefore := meta.FindStatusCondition(before.Status.Conditions, edgeapi.ConnectionConditionRegistered)

	if _, err := newReconciler(reg, now).reconcileEdge(context.Background(), c, before, testCluster); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getLinuxServer(t, c)
	if got.Status.LastHeartbeatTime == nil || !got.Status.LastHeartbeatTime.Time.Equal(second) {
		t.Fatalf("lastHeartbeatTime = %v, want %v", got.Status.LastHeartbeatTime, second)
	}
	if got.Status.Labels["zone"] != "a" || got.Status.Hostname != "build-01.local" {
		t.Fatalf("fields owned by others were clobbered: labels=%v hostname=%q", got.Status.Labels, got.Status.Hostname)
	}
	registeredAfter := meta.FindStatusCondition(got.Status.Conditions, edgeapi.ConnectionConditionRegistered)
	if registeredAfter == nil || !registeredAfter.LastTransitionTime.Equal(&registeredBefore.LastTransitionTime) {
		t.Fatalf("Registered condition was rewritten on a heartbeat-only change: %+v", registeredAfter)
	}

	// A second reconcile with the same lease is a no-op: nothing to patch.
	if _, err := newReconciler(reg, now).reconcileEdge(context.Background(), c, got, testCluster); err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
	if again := getLinuxServer(t, c); again.ResourceVersion != got.ResourceVersion {
		t.Fatalf("resourceVersion moved %s -> %s on an unchanged lease", got.ResourceVersion, again.ResourceVersion)
	}
}

func TestExpiredOrMissingLeaseDisconnectsButKeepsHistory(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-5 * time.Minute)
	key := tunnel.EdgeConnKey(edgesv1alpha1.LinuxServerResource, testCluster, testEdge)
	for name, obs := range map[string]tunnel.TunnelObservation{
		"expired": {Held: false, Holder: "10.0.0.7:8090", RenewTime: lastSeen, ExpiresAt: lastSeen.Add(tunnel.RegistryLeaseTTL)},
		"missing": {},
	} {
		t.Run(name, func(t *testing.T) {
			reg := &fakeRegistry{obs: map[string]tunnel.TunnelObservation{key: obs}}
			hb := metav1.NewTime(lastSeen)
			c := newLifecycleClient(t, linuxServer(func(ls *edgesv1alpha1.LinuxServer) {
				ls.Status.Connected = true
				ls.Status.Phase = edgeapi.ConnectionPhaseReady
				ls.Status.LastHeartbeatTime = &hb
				meta.SetStatusCondition(&ls.Status.Conditions, metav1.Condition{
					Type: edgeapi.ConnectionConditionRegistered, Status: metav1.ConditionTrue, Reason: "AgentRegistered",
				})
			}))

			res, err := newReconciler(reg, now).reconcileEdge(context.Background(), c, getLinuxServer(t, c), testCluster)
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			got := getLinuxServer(t, c)
			if got.Status.Connected || got.Status.Phase != edgeapi.ConnectionPhaseDisconnected {
				t.Fatalf("status = connected:%v phase:%q, want Disconnected", got.Status.Connected, got.Status.Phase)
			}
			if got.Status.LastHeartbeatTime == nil || !got.Status.LastHeartbeatTime.Time.Equal(lastSeen) {
				t.Fatalf("lastHeartbeatTime = %v, want last-seen %v preserved", got.Status.LastHeartbeatTime, lastSeen)
			}
			if !meta.IsStatusConditionTrue(got.Status.Conditions, edgeapi.ConnectionConditionRegistered) {
				t.Fatal("Registered must stay True across a disconnect")
			}
			if res.RequeueAfter != lifecycleResync {
				t.Fatalf("RequeueAfter = %v, want safety resync %v", res.RequeueAfter, lifecycleResync)
			}
		})
	}
}

func TestNeverConnectedEdgeIsLeftAlone(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	c := newLifecycleClient(t, linuxServer(func(ls *edgesv1alpha1.LinuxServer) {
		ls.Status.Phase = edgeapi.ConnectionPhaseScheduling
		ls.Status.JoinToken = "bootstrap"
	}))
	before := getLinuxServer(t, c)

	if _, err := newReconciler(&fakeRegistry{}, now).reconcileEdge(context.Background(), c, before, testCluster); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := getLinuxServer(t, c)
	if got.ResourceVersion != before.ResourceVersion {
		t.Fatalf("a never-connected edge was written to: %+v", got.Status)
	}
	if got.Status.Phase != edgeapi.ConnectionPhaseScheduling || meta.FindStatusCondition(got.Status.Conditions, edgeapi.ConnectionConditionRegistered) != nil {
		t.Fatalf("status = %+v, want Scheduling with no Registered condition", got.Status)
	}
}

func TestNameLabelIsStampedFirst(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	c := newLifecycleClient(t, linuxServer(func(ls *edgesv1alpha1.LinuxServer) { ls.Labels = nil }))

	res, err := newReconciler(&fakeRegistry{}, now).reconcileEdge(context.Background(), c, getLinuxServer(t, c), testCluster)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !res.Requeue {
		t.Fatal("label stamp should requeue for the status pass")
	}
	if got := getLinuxServer(t, c); got.Labels[edgesv1alpha1.LabelName] != testEdge {
		t.Fatalf("name label = %v", got.Labels)
	}
}

func TestLeaseMapsToItsEdgeAndOnlyItsKind(t *testing.T) {
	r := newReconciler(&fakeRegistry{}, time.Now())
	lease := func(key string) *coordinationv1.Lease {
		return &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
			Name:        tunnel.TunnelLeaseName(key),
			Namespace:   tunnel.RegistryNamespace,
			Labels:      map[string]string{tunnel.TunnelLeaseLabel: "true"},
			Annotations: map[string]string{tunnel.TunnelLeaseKeyAnnotation: key},
		}}
	}

	reqs := r.mapLease(context.Background(), lease(tunnel.EdgeConnKey(edgesv1alpha1.LinuxServerResource, testCluster, testEdge)))
	if len(reqs) != 1 || reqs[0].Name != testEdge || string(reqs[0].ClusterName) != testCluster || reqs[0].Namespace != "" {
		t.Fatalf("mapped requests = %+v, want one for %s in %s", reqs, testEdge, testCluster)
	}
	for _, key := range []string{
		tunnel.EdgeConnKey(edgesv1alpha1.KubernetesClusterResource, testCluster, testEdge), // another kind's controller
		"garbage",
		"",
	} {
		if got := r.mapLease(context.Background(), lease(key)); len(got) != 0 {
			t.Fatalf("key %q mapped to %+v, want nothing", key, got)
		}
	}
	// The local ConnManager notification path shares the mapping.
	if req, ok := r.requestForKey(tunnel.EdgeConnKey(edgesv1alpha1.LinuxServerResource, testCluster, testEdge)); !ok || req.Name != testEdge {
		t.Fatalf("requestForKey = %+v/%v", req, ok)
	}
}

func TestConnectivityPatchCarriesOnlyChangedOwnedFields(t *testing.T) {
	hb := metav1.NewTime(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC))
	current := &edgeapi.ConnectionStatus{Connected: true, Phase: edgeapi.ConnectionPhaseReady, LastHeartbeatTime: &hb, AgentVersion: "v1"}

	if p := connectivityPatch(current, current); p != nil {
		t.Fatalf("unchanged status produced patch %s", p)
	}
	desired := *current
	desired.Connected = false
	desired.Phase = edgeapi.ConnectionPhaseDisconnected
	desired.AgentVersion = "v2" // not ours: must never be patched
	got := string(connectivityPatch(current, &desired))
	if got != `{"status":{"connected":false,"phase":"Disconnected"}}` {
		t.Fatalf("patch = %s", got)
	}
}
