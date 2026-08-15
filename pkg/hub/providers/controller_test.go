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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
	"github.com/faroshq/faros/utils/testfakes"
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
			Description: "Persistent AI project workspace.",
			Dependencies: []providersv1alpha1.ProviderDependency{
				{Name: "code"},
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
	if len(got.Dependencies) != 1 || got.Dependencies[0].Name != "code" {
		t.Fatalf("Dependencies = %#v, want [code]", got.Dependencies)
	}
	// The portal's catalog cards and first-run welcome flow render this; if the
	// reconciler drops it, both fall back to showing a bare provider name.
	if got.Description != "Persistent AI project workspace." {
		t.Fatalf("Description = %q, want the spec value", got.Description)
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

func TestCatalogReconcilerRejectsInvalidActionDeclarations(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-actions"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Invalid actions",
			UI:          &providersv1alpha1.ProviderUI{URL: "http://provider.invalid"},
			Actions: []providersv1alpha1.ProviderActionSpec{{
				ID:          "query_table/latest",
				DisplayName: "Invalid action",
			}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "invalid-actions")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := reg.Get("invalid-actions"); ok {
		t.Fatal("invalid action declaration must not enter the provider registry")
	}

	var updated providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "invalid-actions"}, &updated); err != nil {
		t.Fatalf("get updated entry: %v", err)
	}
	if len(updated.Status.Conditions) != 1 {
		t.Fatalf("conditions = %#v, want one Ready condition", updated.Status.Conditions)
	}
	condition := updated.Status.Conditions[0]
	if condition.Type != "Ready" || condition.Status != metav1.ConditionFalse || condition.Reason != "InvalidActions" {
		t.Fatalf("condition = %#v, want Ready=False/InvalidActions", condition)
	}
}

func TestCatalogReconcilerOmitsInvalidAssistantSkillAndKeepsValidSibling(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	valid := providersv1alpha1.ProviderAssistantSkillSpec{
		PackageName: "valid",
		Version:     "1.0.0",
		Skill:       "---\nname: valid\ndescription: valid guidance\n---\nbody\n",
	}
	digest, err := providersv1alpha1.ProviderAssistantSkillDigest(valid)
	if err != nil {
		t.Fatalf("skill digest: %v", err)
	}
	valid.Digest = digest
	invalid := valid
	invalid.PackageName = "invalid"
	invalid.Skill += "tampered"
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "skills"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Skills",
			UI:          &providersv1alpha1.ProviderUI{URL: "http://skills.invalid"},
			AssistantSkills: []providersv1alpha1.ProviderAssistantSkillSpec{
				invalid,
				valid,
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "skills")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got, ok := reg.Get("skills")
	if !ok {
		t.Fatal("expected provider in registry")
	}
	if len(got.AssistantSkills) != 1 || got.AssistantSkills[0].PackageName != "valid" || got.AssistantSkills[0].Digest != digest {
		t.Fatalf("assistant skills = %#v, want only valid sibling", got.AssistantSkills)
	}
}

// Every replica runs the catalog reconciler, so an unconditional status write
// is a cross-replica write storm: each Update bumps the resource version, every
// peer's watch fires, and they all write again. A steady-state reconcile must
// therefore leave the object untouched.
func TestCatalogReconcilerDoesNotRewriteUnchangedStatus(t *testing.T) {
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "cost"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			DisplayName: "Cost",
			Backend:     &providersv1alpha1.ProviderBackend{URL: "http://cost.invalid"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	req := testfakes.NewRequest("cluster", "", "cost")
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	var afterFirst providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cost"}, &afterFirst); err != nil {
		t.Fatalf("get after first reconcile: %v", err)
	}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	var afterSecond providersv1alpha1.CatalogEntry
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cost"}, &afterSecond); err != nil {
		t.Fatalf("get after second reconcile: %v", err)
	}

	if afterFirst.ResourceVersion != afterSecond.ResourceVersion {
		t.Fatalf("status rewritten with no change: resourceVersion %s -> %s",
			afterFirst.ResourceVersion, afterSecond.ResourceVersion)
	}
}

// The status heartbeat is how a beat served by one replica reaches the others;
// the reconciler has to carry it into the registry.
func TestCatalogReconcilerAdoptsStatusHeartbeat(t *testing.T) {
	beat := metav1.NewTime(time.Now().Add(-time.Second).Truncate(time.Second))
	reg := NewRegistry()
	scheme := newProviderTestScheme(t)
	entry := &providersv1alpha1.CatalogEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "cost"},
		Spec: providersv1alpha1.CatalogEntrySpec{
			Backend: &providersv1alpha1.ProviderBackend{URL: "http://cost.invalid"},
		},
		Status: providersv1alpha1.CatalogEntryStatus{
			LastHeartbeat:   &beat,
			ReportedVersion: "v4.5.6",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&providersv1alpha1.CatalogEntry{}).
		WithObjects(entry).
		Build()

	r := &CatalogReconciler{mgr: testfakes.NewManager(c), reg: reg, noKCP: true}
	if _, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "", "cost")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got, ok := reg.Get("cost")
	if !ok {
		t.Fatal("cost missing from registry")
	}
	if !got.LastHeartbeat.Equal(beat.Time) || got.ReportedVersion != "v4.5.6" || !got.HeartbeatRequired {
		t.Fatalf("registry did not adopt status heartbeat: %+v", got)
	}
	if !got.Ready() {
		t.Fatal("provider with a fresh heartbeat should be Ready on every replica")
	}
}
