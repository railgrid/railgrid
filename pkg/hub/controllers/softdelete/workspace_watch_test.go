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

package softdelete

import (
	"context"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
)

// fakeWatchingProvisioner is the fake provisioner plus one fake watch per
// organization, which the test feeds Workspace events.
type fakeWatchingProvisioner struct {
	*fakeProvisioner
	watches map[string]*watch.FakeWatcher
}

func (f *fakeWatchingProvisioner) WatchChildWorkspaces(_ context.Context, orgUUID string) (watch.Interface, error) {
	if w, ok := f.watches[orgUUID]; ok {
		return w, nil
	}
	return watch.NewFake(), nil
}

// fakeClock is a settable now() that is safe to move from the test while
// the watch workers read it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.t = t }

func childWorkspaceObject(name string, requestedAt *time.Time, forced bool) *unstructured.Unstructured {
	annos := map[string]any{}
	if requestedAt != nil {
		annos[kcp.WorkspaceDeletionAnnotation] = requestedAt.UTC().Format(time.RFC3339)
	}
	if forced {
		annos[kcp.WorkspaceDeletionForcedAnnotation] = "true"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1", "kind": "Workspace",
		"metadata": map[string]any{"name": name, "annotations": annos},
	}}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func workspaceEntrySoftDeletedAt(t *testing.T, r *Reconciler, user, wsUUID string) (*time.Time, bool) {
	t.Helper()
	var idx tenancyv1alpha1.UserMembershipIndex
	if err := r.client.Get(context.Background(), types.NamespacedName{Name: user}, &idx); err != nil {
		return nil, false
	}
	for i := range idx.Spec.Entries {
		if idx.Spec.Entries[i].WorkspaceUUID == wsUUID {
			if idx.Spec.Entries[i].SoftDeletedAt == nil {
				return nil, true
			}
			tt := idx.Spec.Entries[i].SoftDeletedAt.Time
			return &tt, true
		}
	}
	return nil, false
}

// The watch-driven branch makes the same decisions as the sweep, from events:
// within grace it marks UMI rows and waits for the grace to end; after the
// grace it cascades; an annotation removed in between undeletes.
func TestWorkspaceWatch_GraceThenCascadeFromEvents(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-1 * time.Hour)
	clock := &fakeClock{t: now}

	prov := newFakeProvisioner()
	prov.orgs = []string{"alice-org"}
	prov.orgMembers["alice-org"] = []string{"alice"}
	fw := watch.NewFake()
	wp := &fakeWatchingProvisioner{fakeProvisioner: prov, watches: map[string]*watch.FakeWatcher{"alice-org": fw}}

	umi := newUMI("alice", "alice-org", "alice-ws", true, tenancyv1alpha1.MembershipRoleAdmin)
	r, _ := newReconciler(t, wp, umi)
	r.now = clock.Now
	ww := newWorkspaceWatch(r, wp)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = ww.Run(ctx); close(done) }()

	// Within grace: the event marks the member's UMI row, nothing is deleted.
	fw.Add(childWorkspaceObject("alice-ws", &requestedAt, false))
	eventually(t, "UMI marked within grace", func() bool {
		at, ok := workspaceEntrySoftDeletedAt(t, r, "alice", "alice-ws")
		return ok && at != nil
	})
	prov.mu.Lock()
	deleted := len(prov.deleteChildCalls)
	prov.mu.Unlock()
	if deleted != 0 {
		t.Fatalf("nothing may be deleted inside the grace period, got %d deletes", deleted)
	}

	// Undelete: the annotation disappears, the marker is cleared.
	fw.Modify(childWorkspaceObject("alice-ws", nil, false))
	eventually(t, "UMI marker cleared on undelete", func() bool {
		at, ok := workspaceEntrySoftDeletedAt(t, r, "alice", "alice-ws")
		return ok && at == nil
	})

	// Requested again, and the grace has passed: cascade.
	clock.Set(now.Add(DefaultGracePeriod + 2*time.Hour))
	fw.Modify(childWorkspaceObject("alice-ws", &requestedAt, false))
	eventually(t, "cascade after grace", func() bool {
		prov.mu.Lock()
		defer prov.mu.Unlock()
		for _, k := range prov.deleteChildCalls {
			if k.Org == "alice-org" && k.WS == "alice-ws" {
				return true
			}
		}
		return false
	})

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace watch did not stop")
	}
}

// A forced request cascades at once, grace or not; a deleted Workspace is
// dropped from the queue without a reconcile.
func TestWorkspaceWatch_ForcedCascadesInsideGrace(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-time.Minute)
	clock := &fakeClock{t: now}

	prov := newFakeProvisioner()
	prov.orgs = []string{"alice-org"}
	prov.orgMembers["alice-org"] = []string{"alice"}
	fw := watch.NewFake()
	wp := &fakeWatchingProvisioner{fakeProvisioner: prov, watches: map[string]*watch.FakeWatcher{"alice-org": fw}}
	r, _ := newReconciler(t, wp, newUMI("alice", "alice-org", "alice-ws", true, tenancyv1alpha1.MembershipRoleAdmin))
	r.now = clock.Now
	ww := newWorkspaceWatch(r, wp)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ww.Run(ctx) }()

	fw.Add(childWorkspaceObject("alice-ws", &requestedAt, true))
	eventually(t, "forced cascade inside grace", func() bool {
		prov.mu.Lock()
		defer prov.mu.Unlock()
		return len(prov.deleteChildCalls) == 1
	})
	fw.Delete(childWorkspaceObject("alice-ws", &requestedAt, true))
	eventually(t, "deleted workspace dropped", func() bool {
		ww.mu.Lock()
		defer ww.mu.Unlock()
		_, still := ww.seen[childWorkspaceKey{org: "alice-org", ws: "alice-ws"}]
		return !still
	})
}

// The grace end is a timer on the queue, not a sweep: when the clock reaches
// it the Workspace is reconciled again with no new event.
func TestWorkspaceWatch_RequeuesAtGraceEnd(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: now}
	prov := newFakeProvisioner()
	prov.orgMembers["alice-org"] = []string{"alice"}
	r, _ := newReconciler(t, prov, newUMI("alice", "alice-org", "alice-ws", true, tenancyv1alpha1.MembershipRoleAdmin))
	r.now = clock.Now
	r.gracePeriod = 50 * time.Millisecond
	requestedAt := now
	again, err := r.reconcileChildWorkspace(context.Background(), childWorkspaceKey{org: "alice-org", ws: "alice-ws"}, childWorkspaceState{requestedAt: &requestedAt})
	if err != nil {
		t.Fatal(err)
	}
	if again != 50*time.Millisecond {
		t.Errorf("within grace must ask to be looked at again when the grace ends, got %v", again)
	}
	clock.Set(now.Add(time.Second))
	again, err = r.reconcileChildWorkspace(context.Background(), childWorkspaceKey{org: "alice-org", ws: "alice-ws"}, childWorkspaceState{requestedAt: &requestedAt})
	if err != nil || again != 0 {
		t.Errorf("after grace: again=%v err=%v", again, err)
	}
	if len(prov.deleteChildCalls) != 1 {
		t.Errorf("after grace the workspace must be deleted, got %d deletes", len(prov.deleteChildCalls))
	}
}
