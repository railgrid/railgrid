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

package metering

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
)

const meteringWS = "root:railgrid:system:metering"

type installedAt string

func (w installedAt) Current() (Endpoints, bool) {
	if w == "" {
		return Endpoints{}, false
	}
	return Endpoints{PortalURL: "http://metering", Workspace: string(w)}, true
}

type providerMap map[string]*adminv1alpha1.Provider

func (m providerMap) Get(_ context.Context, _ multicluster.ClusterName, key types.NamespacedName) (*adminv1alpha1.Provider, error) {
	p, ok := m[key.Name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "admin.railgrid.ai", Resource: "providers"}, key.Name)
	}
	return p, nil
}

func provider(name, workspacePath string) *adminv1alpha1.Provider {
	p := &adminv1alpha1.Provider{ObjectMeta: metav1.ObjectMeta{Name: name}}
	p.Status.WorkspacePath = workspacePath
	return p
}

func boundBinding(path, name string) *unstructured.Unstructured {
	u := providerBinding(path)
	_ = unstructured.SetNestedField(u.Object, name, "spec", "reference", "export", "name")
	_ = unstructured.SetNestedField(u.Object, "Bound", "status", "phase")
	return u
}

func newProviderFixture(gate Installed, providers providerMap, existing ...runtime.Object) (*ProviderReconciler, dynamic.Interface) {
	ws := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvrAPIBindings: "APIBindingList"}, existing...)
	r := &ProviderReconciler{opts: ProviderOptions{
		Discovery:  gate,
		Providers:  providers,
		Workspaces: func(string) (dynamic.Interface, error) { return ws, nil },
		Logger:     klog.Background(),
	}}
	return r, ws
}

func reconcileProvider(t *testing.T, r *ProviderReconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), mcreconcile.Request{
		ClusterName: "system-providers",
		Request:     ctrl.Request{NamespacedName: types.NamespacedName{Name: name}},
	})
	if err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
	return res
}

func getBinding(t *testing.T, ws dynamic.Interface) *unstructured.Unstructured {
	t.Helper()
	u, err := ws.Resource(gvrAPIBindings).Get(context.Background(), ProviderBindingName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	return u
}

func TestProviderReconcilerBindsMeteringExport(t *testing.T) {
	r, ws := newProviderFixture(installedAt(meteringWS), providerMap{"agents": provider("agents", "root:railgrid:providers:agents")})

	res := reconcileProvider(t, r, "agents")
	if res.RequeueAfter != bindingSettle {
		t.Errorf("fresh binding should be re-checked for Bound, got %+v", res)
	}
	b := getBinding(t, ws)
	path, _, _ := unstructured.NestedString(b.Object, "spec", "reference", "export", "path")
	name, _, _ := unstructured.NestedString(b.Object, "spec", "reference", "export", "name")
	if path != meteringWS || name != ProviderBindingName {
		t.Errorf("binding references %s:%s, want %s:%s", path, name, meteringWS, ProviderBindingName)
	}
	if b.GetLabels()[ProviderBindingLabel] != FieldManager {
		t.Errorf("binding labels = %v, want %s=%s", b.GetLabels(), ProviderBindingLabel, FieldManager)
	}

	// Second pass: still not Bound (the fake has no kcp), so it keeps checking
	// without writing again.
	before := b.GetResourceVersion()
	res = reconcileProvider(t, r, "agents")
	if res.RequeueAfter != bindingSettle {
		t.Errorf("unbound binding should be re-checked, got %+v", res)
	}
	if getBinding(t, ws).GetResourceVersion() != before {
		t.Error("existing binding was rewritten")
	}
}

func TestProviderReconcilerSettlesOnceBound(t *testing.T) {
	r, _ := newProviderFixture(installedAt(meteringWS), providerMap{"agents": provider("agents", "root:railgrid:providers:agents")},
		boundBinding(meteringWS, ProviderBindingName))
	if res := reconcileProvider(t, r, "agents"); res != (ctrl.Result{}) {
		t.Errorf("bound binding should settle, got %+v", res)
	}
}

func TestProviderReconcilerGatedOnDiscovery(t *testing.T) {
	r, ws := newProviderFixture(installedAt(""), providerMap{"agents": provider("agents", "root:railgrid:providers:agents")})
	res := reconcileProvider(t, r, "agents")
	if res.RequeueAfter != notInstalledRetry {
		t.Errorf("without metering the provider should be retried later, got %+v", res)
	}
	if _, err := ws.Resource(gvrAPIBindings).Get(context.Background(), ProviderBindingName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("binding written while metering is not installed: %v", err)
	}
}

func TestProviderReconcilerWaitsForProvisioning(t *testing.T) {
	r, ws := newProviderFixture(installedAt(meteringWS), providerMap{"agents": provider("agents", "")})
	if res := reconcileProvider(t, r, "agents"); res != (ctrl.Result{}) {
		t.Errorf("unprovisioned provider should wait for its status update, got %+v", res)
	}
	if _, err := ws.Resource(gvrAPIBindings).Get(context.Background(), ProviderBindingName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("binding written before the workspace exists: %v", err)
	}
}

func TestProviderReconcilerIgnoresMissingAndForeign(t *testing.T) {
	// A deleted Provider is nothing to do: the workspace goes with it.
	r, _ := newProviderFixture(installedAt(meteringWS), providerMap{})
	if res := reconcileProvider(t, r, "gone"); res != (ctrl.Result{}) {
		t.Errorf("missing provider should be a no-op, got %+v", res)
	}

	// A binding of the same name to another export is not ours to change.
	foreign := boundBinding("root:elsewhere", "metering-provider")
	r, ws := newProviderFixture(installedAt(meteringWS), providerMap{"agents": provider("agents", "root:railgrid:providers:agents")}, foreign)
	if res := reconcileProvider(t, r, "agents"); res != (ctrl.Result{}) {
		t.Errorf("foreign binding should be left alone, got %+v", res)
	}
	path, _, _ := unstructured.NestedString(getBinding(t, ws).Object, "spec", "reference", "export", "path")
	if path != "root:elsewhere" {
		t.Errorf("foreign binding rewritten to %s", path)
	}
}
