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

package providers

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
	"github.com/railgrid/railgrid/utils/testfakes"
)

type provisioningStub struct {
	workspaceErr error
	secretName   string
}

func (p *provisioningStub) EnsureProviderWorkspace(context.Context, string) (string, error) {
	return "provider-cluster", p.workspaceErr
}

func (*provisioningStub) EnsureProviderSA(context.Context, string) error { return nil }

func (*provisioningStub) MintProviderKubeconfig(context.Context, string, string) ([]byte, error) {
	return []byte("test-kubeconfig"), nil
}

func (p *provisioningStub) WriteKubeconfigSecret(_ context.Context, _, name, _ string, _ []byte, _ string) error {
	p.secretName = name
	return nil
}

func (*provisioningStub) DeleteProviderWorkspace(context.Context, string) error { return nil }

func (*provisioningStub) DeleteKubeconfigSecret(context.Context, string, string) error { return nil }

func TestProviderReconcilerConvergesAfterItsOwnStatusEvent(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	if err := adminv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	p := &adminv1alpha1.Provider{ObjectMeta: metav1.ObjectMeta{
		Name: "example", Generation: 1, Finalizers: []string{providerFinalizer},
	}}
	statusWrites := 0
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(p).
		WithStatusSubresource(&adminv1alpha1.Provider{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, name string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				statusWrites++
				return c.SubResource(name).Update(ctx, obj, opts...)
			},
		}).Build()
	prov := &provisioningStub{}
	r := &ProviderReconciler{mgr: testfakes.NewManager(c), prov: prov}
	request := testfakes.NewRequest("system-providers", "", p.Name)
	read := func() *adminv1alpha1.Provider {
		t.Helper()
		got := &adminv1alpha1.Provider{}
		if err := c.Get(ctx, types.NamespacedName{Name: p.Name}, got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	reconcile := func() {
		t.Helper()
		result, err := r.Reconcile(ctx, request)
		if err != nil || !result.IsZero() {
			t.Fatalf("reconcile = %v, %v; want success without requeue", result, err)
		}
	}
	reconcile()
	first := read()
	if first.Status.LastProvisioned == nil || first.Status.WorkspaceCluster != "provider-cluster" || prov.secretName != "example-kubeconfig" {
		t.Fatalf("initial provisioning incomplete: status=%+v, secret=%q", first.Status, prov.secretName)
	}
	// Deliver the controller's status event and unrelated resyncs. None may
	// change status or timestamps, otherwise the watch can run forever.
	for range 3 {
		reconcile()
	}
	if statusWrites != 1 || !equality.Semantic.DeepEqual(first.Status, read().Status) {
		t.Fatalf("settled reconciliation rewrote status: writes=%d", statusWrites)
	}

	// A real spec change must still provision and advance observed generation.
	changed := read()
	changed.Generation++
	changed.Spec.SecretName = "replacement-kubeconfig"
	if err := c.Update(ctx, changed); err != nil {
		t.Fatal(err)
	}
	reconcile()
	got := read()
	ready := conditionByType(got.Status.Conditions, "Ready")
	if statusWrites != 2 || prov.secretName != changed.Spec.SecretName || got.Status.SecretRef.Name != changed.Spec.SecretName || ready == nil || ready.ObservedGeneration != 2 {
		t.Fatalf("spec change was not reconciled: writes=%d, status=%+v", statusWrites, got.Status)
	}
	reconcile()
	if statusWrites != 2 {
		t.Fatalf("spec update did not converge: writes=%d", statusWrites)
	}

	// Repeated failures retain their error for workqueue backoff, without
	// generating status events that would circumvent that backoff.
	prov.workspaceErr = errors.New("workspace unavailable")
	for range 3 {
		if _, err := r.Reconcile(ctx, request); !errors.Is(err, prov.workspaceErr) {
			t.Fatalf("failure = %v, want underlying workspace error", err)
		}
	}
	failed := read()
	if statusWrites != 3 || !equality.Semantic.DeepEqual(got.Status.LastProvisioned, failed.Status.LastProvisioned) {
		t.Fatalf("unchanged failure rewrote status or last success: writes=%d", statusWrites)
	}
	prov.workspaceErr = nil
	reconcile()
	reconcile()
	ready = conditionByType(read().Status.Conditions, "Ready")
	if statusWrites != 4 || ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("recovery did not converge: writes=%d, Ready=%v", statusWrites, ready)
	}
}

func TestProviderReconcilerRetriesStatusConflict(t *testing.T) {
	s := runtime.NewScheme()
	if err := adminv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	p := &adminv1alpha1.Provider{ObjectMeta: metav1.ObjectMeta{
		Name: "example", Generation: 1, Finalizers: []string{providerFinalizer},
	}}
	attempts := 0
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(p).
		WithStatusSubresource(&adminv1alpha1.Provider{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, name string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				attempts++
				if attempts == 1 {
					return apierrors.NewConflict(schema.GroupResource{Group: "admin.railgrid.ai", Resource: "providers"}, obj.GetName(), errors.New("concurrent update"))
				}
				return c.SubResource(name).Update(ctx, obj, opts...)
			},
		}).Build()
	r := &ProviderReconciler{mgr: testfakes.NewManager(c), prov: &provisioningStub{}}
	request := testfakes.NewRequest("system-providers", "", p.Name)
	result, err := r.Reconcile(context.Background(), request)
	if err != nil || result.IsZero() {
		t.Fatalf("conflict = %v, %v; want requeue", result, err)
	}
	for range 2 {
		if _, err := r.Reconcile(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if attempts != 2 {
		t.Fatalf("status update attempts = %d, want conflict, success, then no write", attempts)
	}
}
