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

package organization

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

// Organizations created before catalogEntryCreation defaulted to admin relied
// on an empty field meaning "members". The backfill pins that reading onto
// them explicitly, once, and leaves every other Organization's choice alone.
func TestBackfillCatalogEntryCreation(t *testing.T) {
	legacyUnset := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-unset"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "Legacy"},
	}
	legacyAdmin := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-admin"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "Locked", CatalogEntryCreation: tenancyv1alpha1.CatalogEntryCreationAdmin},
	}
	alreadyMigrated := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "migrated",
			Annotations: map[string]string{AnnotationCatalogEntryCreationMigrated: "2026-09-01T00:00:00Z"},
		},
		// Unset AND annotated: created after the flip by something that
		// stamped it, so the empty field genuinely means admin now. The
		// backfill must not rewrite it to members.
		Spec: tenancyv1alpha1.OrganizationSpec{DisplayName: "New"},
	}
	c := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(legacyUnset, legacyAdmin, alreadyMigrated).
		Build()
	ctx := context.Background()

	updated, err := BackfillCatalogEntryCreation(ctx, c, c)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if updated != 2 {
		t.Fatalf("updated = %d, want 2 (the two unannotated Organizations)", updated)
	}

	get := func(name string) tenancyv1alpha1.Organization {
		var org tenancyv1alpha1.Organization
		if err := c.Get(ctx, types.NamespacedName{Name: name}, &org); err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		return org
	}
	if got := get("legacy-unset"); got.Spec.CatalogEntryCreation != tenancyv1alpha1.CatalogEntryCreationMembers || got.Annotations[AnnotationCatalogEntryCreationMigrated] == "" {
		t.Errorf("legacy-unset = %q / %v, want members pinned and annotated", got.Spec.CatalogEntryCreation, got.Annotations)
	}
	if got := get("legacy-admin"); got.Spec.CatalogEntryCreation != tenancyv1alpha1.CatalogEntryCreationAdmin || got.Annotations[AnnotationCatalogEntryCreationMigrated] == "" {
		t.Errorf("legacy-admin = %q / %v, want admin kept and annotated", got.Spec.CatalogEntryCreation, got.Annotations)
	}
	if got := get("migrated"); got.Spec.CatalogEntryCreation != "" || got.Annotations[AnnotationCatalogEntryCreationMigrated] != "2026-09-01T00:00:00Z" {
		t.Errorf("migrated = %q / %v, want untouched", got.Spec.CatalogEntryCreation, got.Annotations)
	}

	// Idempotent: a second run finds nothing to do.
	updated, err = BackfillCatalogEntryCreation(ctx, c, c)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if updated != 0 {
		t.Errorf("second run updated %d, want 0", updated)
	}
}

// coldCacheReader stands in for a cache-backed client.Reader that has not been
// populated: List answers with an empty set and no error. The backfill stops
// after one successful pass, so reading through such a client would leave
// every legacy Organization with an empty spec.catalogEntryCreation — which
// now reads as "admin", silently taking provider registration away from
// members. The runnable therefore lists through mgr.GetAPIReader().
type coldCacheReader struct {
	client.Reader
	lists int
}

func (r *coldCacheReader) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	r.lists++
	orgs, ok := list.(*tenancyv1alpha1.OrganizationList)
	if !ok {
		return fmt.Errorf("unexpected list type %T", list)
	}
	orgs.Items = nil
	return nil
}

func TestBackfillCatalogEntryCreationDependsOnTheReaderNotACache(t *testing.T) {
	legacy := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-unset"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "Legacy"},
	}
	c := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(legacy).
		Build()
	ctx := context.Background()

	// A cold cache reports success with nothing in it, and the caller cannot
	// tell that apart from an empty cluster — which is exactly why the
	// runnable must not use one.
	cold := &coldCacheReader{}
	updated, err := BackfillCatalogEntryCreation(ctx, cold, c)
	if err != nil || updated != 0 {
		t.Fatalf("cold read = %d, %v; want 0, nil (the failure mode being guarded against)", updated, err)
	}
	var org tenancyv1alpha1.Organization
	if err := c.Get(ctx, types.NamespacedName{Name: "legacy-unset"}, &org); err != nil {
		t.Fatalf("get: %v", err)
	}
	if org.Spec.CatalogEntryCreation != "" {
		t.Fatalf("cold read migrated %q; the test's premise is wrong", org.Spec.CatalogEntryCreation)
	}

	// The live reader sees it and pins the pre-flip behaviour.
	updated, err = BackfillCatalogEntryCreation(ctx, c, c)
	if err != nil {
		t.Fatalf("live backfill: %v", err)
	}
	if updated != 1 {
		t.Fatalf("live backfill updated %d, want 1", updated)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: "legacy-unset"}, &org); err != nil {
		t.Fatalf("get: %v", err)
	}
	if org.Spec.CatalogEntryCreation != tenancyv1alpha1.CatalogEntryCreationMembers {
		t.Errorf("catalogEntryCreation = %q, want members", org.Spec.CatalogEntryCreation)
	}
	if org.Annotations[AnnotationCatalogEntryCreationMigrated] == "" {
		t.Error("migrated annotation not stamped")
	}
}

// A List error must not be mistaken for "nothing to do": the runnable's poll
// keeps retrying until a pass actually succeeds.
func TestBackfillCatalogEntryCreationRetriesOnListError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	if _, err := BackfillCatalogEntryCreation(context.Background(), failingReader{}, c); err == nil {
		t.Fatal("a failed list reported success; the backfill would exit without visiting anything")
	}
}

type failingReader struct{ client.Reader }

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return fmt.Errorf("apiserver unavailable")
}
