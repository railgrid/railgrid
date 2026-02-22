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

package site

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/utils/testfakes"
)

func newLifecycleScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kedgev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding kedge scheme: %v", err)
	}
	return s
}

func TestLifecycleReconciler_SiteNotFound(t *testing.T) {
	scheme := newLifecycleScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &LifecycleReconciler{mgr: testfakes.NewManager(c)}

	result, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "missing-site"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected empty Result, got: %+v", result)
	}
}

func TestLifecycleReconciler_NoHeartbeat(t *testing.T) {
	scheme := newLifecycleScheme(t)
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-a"},
		Status:     kedgev1alpha1.SiteStatus{LastHeartbeatTime: nil},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(site).
		WithObjects(site).
		Build()
	r := &LifecycleReconciler{mgr: testfakes.NewManager(c)}

	result, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "site-a"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter 30s, got: %v", result.RequeueAfter)
	}

	var updated kedgev1alpha1.Site
	if err := c.Get(context.Background(), types.NamespacedName{Name: "site-a"}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.Phase != "" {
		t.Errorf("expected phase unchanged (empty), got: %q", updated.Status.Phase)
	}
}

func TestLifecycleReconciler_FreshHeartbeat(t *testing.T) {
	scheme := newLifecycleScheme(t)
	now := metav1.NewTime(time.Now().Add(-1 * time.Minute))
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-b"},
		Status: kedgev1alpha1.SiteStatus{
			Phase:             kedgev1alpha1.SitePhaseConnected,
			LastHeartbeatTime: &now,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(site).
		WithObjects(site).
		Build()
	r := &LifecycleReconciler{mgr: testfakes.NewManager(c)}

	result, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "site-b"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter 30s, got: %v", result.RequeueAfter)
	}

	var updated kedgev1alpha1.Site
	if err := c.Get(context.Background(), types.NamespacedName{Name: "site-b"}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.Phase != kedgev1alpha1.SitePhaseConnected {
		t.Errorf("expected phase unchanged (%q), got: %q", kedgev1alpha1.SitePhaseConnected, updated.Status.Phase)
	}
}

func TestLifecycleReconciler_StaleHeartbeat(t *testing.T) {
	scheme := newLifecycleScheme(t)
	stale := metav1.NewTime(time.Now().Add(-10 * time.Minute)) // > HeartbeatTimeout (5m)
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-c"},
		Status: kedgev1alpha1.SiteStatus{
			Phase:             kedgev1alpha1.SitePhaseConnected,
			TunnelConnected:   true,
			LastHeartbeatTime: &stale,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(site).
		WithObjects(site).
		Build()
	r := &LifecycleReconciler{mgr: testfakes.NewManager(c)}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "site-c"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var updated kedgev1alpha1.Site
	if err := c.Get(context.Background(), types.NamespacedName{Name: "site-c"}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.Phase != kedgev1alpha1.SitePhaseDisconnected {
		t.Errorf("expected phase Disconnected, got: %q", updated.Status.Phase)
	}
	if updated.Status.TunnelConnected {
		t.Error("expected TunnelConnected=false after stale heartbeat")
	}
}

func TestLifecycleReconciler_AlreadyDisconnected(t *testing.T) {
	scheme := newLifecycleScheme(t)
	stale := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-d"},
		Status: kedgev1alpha1.SiteStatus{
			Phase:             kedgev1alpha1.SitePhaseDisconnected,
			TunnelConnected:   false,
			LastHeartbeatTime: &stale,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(site).
		WithObjects(site).
		Build()
	r := &LifecycleReconciler{mgr: testfakes.NewManager(c)}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "site-d"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var after kedgev1alpha1.Site
	if err := c.Get(context.Background(), types.NamespacedName{Name: "site-d"}, &after); err != nil {
		t.Fatalf("get: %v", err)
	}
	// ResourceVersion must not change — no status write for already-disconnected site.
	if after.ResourceVersion != site.ResourceVersion {
		t.Errorf("expected no status write for already-disconnected site, but ResourceVersion changed: %s → %s",
			site.ResourceVersion, after.ResourceVersion)
	}
}
