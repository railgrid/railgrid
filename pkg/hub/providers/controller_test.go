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

package providers

import (
	"context"
	"testing"
	"testing/fstest"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	providersv1alpha1 "github.com/faroshq/faros-kedge/apis/providers/v1alpha1"
	"github.com/faroshq/faros-kedge/utils/testfakes"
)

func newProviderTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("adding core scheme: %v", err)
	}
	if err := providersv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding providers scheme: %v", err)
	}
	return s
}

func registerAppStudioBuiltin(t *testing.T) {
	t.Helper()
	if _, ok := BuiltinByName("app-studio"); ok {
		return
	}
	RegisterBuiltin(BuiltinSpec{
		Name:          "app-studio",
		DisplayName:   "App Studio",
		LocalUIAssets: fstest.MapFS{"main.js": &fstest.MapFile{Data: []byte("bundle")}},
	})
}

func TestCatalogReconciler_PreservesChartOwnedUIRoutingForBuiltinName(t *testing.T) {
	registerAppStudioBuiltin(t)
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "app-studio"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "App Studio from Chart",
			Dependencies: []string{
				"code",
			},
			UI: &providersv1alpha1.ProviderUI{
				URL: "http://app-studio.invalid",
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "app-studio")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got, ok := reg.Get("app-studio")
	if !ok {
		t.Fatal("expected app-studio in registry")
	}
	if got.UIURL == nil || got.UIURL.String() != "http://app-studio.invalid" {
		t.Fatalf("UIURL = %v, want http://app-studio.invalid", got.UIURL)
	}
	if got.LocalUIAssets != nil {
		t.Fatal("expected chart-owned provider to keep proxy routing, not embedded assets")
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0] != "code" {
		t.Fatalf("Dependencies = %#v, want [code]", got.Dependencies)
	}
	if !got.EndpointsValid {
		t.Fatal("expected endpoints to be valid when ui.url is present")
	}

	var updated providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app-studio"}, &updated); err != nil {
		t.Fatalf("get updated entry: %v", err)
	}
	if updated.Status.Endpoints == nil || updated.Status.Endpoints.UI != "http://app-studio.invalid" {
		t.Fatalf("status endpoints = %#v, want UI=http://app-studio.invalid", updated.Status.Endpoints)
	}
}
