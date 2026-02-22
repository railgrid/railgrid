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
	"fmt"
	"testing"

	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/virtual/builder"
	"github.com/faroshq/faros-kedge/utils/testfakes"
)

func newMountScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kedgev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding kedge scheme: %v", err)
	}
	return s
}

var noopWorkspace = func(_ context.Context, _ klog.Logger, _ string, _ *kedgev1alpha1.Site) error {
	return nil
}

func TestMountReconciler_SiteNotFound(t *testing.T) {
	routes := builder.NewSiteRouteMap()
	scheme := newMountScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &MountReconciler{
		mgr:               testfakes.NewManager(c),
		hubExternalURL:    testHubURL,
		siteRoutes:        routes,
		workspaceEnsureFn: noopWorkspace,
	}

	result, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "ghost"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected empty Result, got: %+v", result)
	}
	if _, ok := routes.Get("test-cluster:ghost"); ok {
		t.Error("route should not be registered for not-found site")
	}
}

func TestMountReconciler_NotReadySite(t *testing.T) {
	routes := builder.NewSiteRouteMap()
	scheme := newMountScheme(t)
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-notready"},
		Status:     kedgev1alpha1.SiteStatus{Phase: kedgev1alpha1.SitePhaseNotReady},
	}
	ensureCalled := false
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(site).WithObjects(site).Build()
	r := &MountReconciler{
		mgr:            testfakes.NewManager(c),
		hubExternalURL: testHubURL,
		siteRoutes:     routes,
		workspaceEnsureFn: func(_ context.Context, _ klog.Logger, _ string, _ *kedgev1alpha1.Site) error {
			ensureCalled = true
			return nil
		},
	}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "site-notready"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if _, ok := routes.Get("test-cluster:site-notready"); ok {
		t.Error("route must not be registered for non-ready site")
	}
	if ensureCalled {
		t.Error("workspace ensurer must not be called for non-ready site")
	}
}

func TestMountReconciler_ReadySite_FirstReconcile(t *testing.T) {
	routes := builder.NewSiteRouteMap()
	scheme := newMountScheme(t)
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-ready"},
		Status:     kedgev1alpha1.SiteStatus{Phase: kedgev1alpha1.SitePhaseReady},
	}
	ensureCalled := false
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(site).
		WithObjects(site).
		Build()
	r := &MountReconciler{
		mgr:            testfakes.NewManager(fakeClient),
		hubExternalURL: testHubURL,
		siteRoutes:     routes,
		workspaceEnsureFn: func(_ context.Context, _ klog.Logger, _ string, _ *kedgev1alpha1.Site) error {
			ensureCalled = true
			return nil
		},
	}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "site-ready"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Route must be registered.
	if _, ok := routes.Get("test-cluster:site-ready"); !ok {
		t.Error("expected route to be registered in SiteRouteMap")
	}

	// URL must be set on site status.
	var updated kedgev1alpha1.Site
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "site-ready"}, &updated); err != nil {
		t.Fatalf("get site: %v", err)
	}
	expectedURL := testHubURL + "/services/site-proxy/test-cluster/site-ready"
	if updated.Status.URL != expectedURL {
		t.Errorf("expected URL=%q, got=%q", expectedURL, updated.Status.URL)
	}
	if !ensureCalled {
		t.Error("expected workspace ensurer to be called for ready site")
	}
}

func TestMountReconciler_ReadySite_URLAlreadyCorrect(t *testing.T) {
	routes := builder.NewSiteRouteMap()
	scheme := newMountScheme(t)
	expectedURL := testHubURL + "/services/site-proxy/test-cluster/site-stable"
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-stable", ResourceVersion: "42"},
		Status: kedgev1alpha1.SiteStatus{
			Phase: kedgev1alpha1.SitePhaseReady,
			URL:   expectedURL,
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(site).
		WithObjects(site).
		Build()
	r := &MountReconciler{
		mgr:               testfakes.NewManager(fakeClient),
		hubExternalURL:    testHubURL,
		siteRoutes:        routes,
		workspaceEnsureFn: noopWorkspace,
	}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "site-stable"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var after kedgev1alpha1.Site
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "site-stable"}, &after); err != nil {
		t.Fatalf("get: %v", err)
	}
	// No status write should have occurred — ResourceVersion unchanged.
	if after.ResourceVersion != site.ResourceVersion {
		t.Errorf("expected no status write when URL already correct, ResourceVersion changed: %s → %s",
			site.ResourceVersion, after.ResourceVersion)
	}
}

func TestMountReconciler_WorkspaceEnsureError_PropagatesError(t *testing.T) {
	routes := builder.NewSiteRouteMap()
	scheme := newMountScheme(t)
	site := &kedgev1alpha1.Site{
		ObjectMeta: metav1.ObjectMeta{Name: "site-err"},
		Status:     kedgev1alpha1.SiteStatus{Phase: kedgev1alpha1.SitePhaseReady},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(site).
		WithObjects(site).
		Build()
	r := &MountReconciler{
		mgr:            testfakes.NewManager(fakeClient),
		hubExternalURL: testHubURL,
		siteRoutes:     routes,
		workspaceEnsureFn: func(_ context.Context, _ klog.Logger, _ string, _ *kedgev1alpha1.Site) error {
			return fmt.Errorf("kcp unavailable")
		},
	}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("test-cluster", "", "site-err"))
	if err == nil {
		t.Fatal("expected error from workspace ensurer to propagate, got nil")
	}
}

// TestToUnstructured verifies that toUnstructured round-trips a typed Workspace.
func TestToUnstructured(t *testing.T) {
	ws := &kcptenancyv1alpha1.Workspace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kcptenancyv1alpha1.SchemeGroupVersion.String(),
			Kind:       "Workspace",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "my-ws"},
	}

	u, err := toUnstructured(ws)
	if err != nil {
		t.Fatalf("toUnstructured: %v", err)
	}
	if u.GetKind() != "Workspace" {
		t.Errorf("expected Kind=Workspace, got %q", u.GetKind())
	}
	if u.GetName() != "my-ws" {
		t.Errorf("expected Name=my-ws, got %q", u.GetName())
	}
}
