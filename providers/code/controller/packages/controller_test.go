/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package packages

import (
	"context"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
	codescheme "github.com/faroshq/provider-code/scheme"
)

func testRepo() *codev1alpha1.Repository {
	return &codev1alpha1.Repository{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("repo-uid-1")},
		Spec:       codev1alpha1.RepositorySpec{ConnectionRef: "conn", Name: "demo"},
	}
}

func newFakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(codescheme.NewScheme()).
		WithStatusSubresource(&codev1alpha1.Package{}).
		WithObjects(objs...).
		Build()
}

func listPackages(t *testing.T, c client.Client, repo string) []codev1alpha1.Package {
	t.Helper()
	var list codev1alpha1.PackageList
	if err := c.List(context.Background(), &list, client.MatchingLabels{codev1alpha1.LabelRepository: repo}); err != nil {
		t.Fatalf("list packages: %v", err)
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list.Items
}

// TestSyncCreatesUpdatesDeletes drives the crawler diff through three passes:
// create from empty, update a changed field while leaving an unchanged one
// alone, and delete a package that disappeared from the host.
func TestSyncCreatesUpdatesDeletes(t *testing.T) {
	ctx := context.Background()
	repo := testRepo()
	c := newFakeClient(repo)
	r := &Reconciler{}

	// Pass 1: two packages appear.
	first := []backend.PackageInfo{
		{Name: "api", Type: "container", Visibility: "private", VersionCount: 3, HTMLURL: "https://h/api"},
		{Name: "cli", Type: "npm", Visibility: "public", VersionCount: 1},
	}
	if err := r.sync(ctx, c, repo, first); err != nil {
		t.Fatalf("sync pass 1: %v", err)
	}
	got := listPackages(t, c, repo.Name)
	if len(got) != 2 {
		t.Fatalf("pass 1: want 2 packages, got %d", len(got))
	}
	for _, p := range got {
		if p.Spec.RepositoryRef != repo.Name {
			t.Errorf("package %q: spec.repositoryRef = %q, want %q", p.Name, p.Spec.RepositoryRef, repo.Name)
		}
		if p.Labels[codev1alpha1.LabelRepository] != repo.Name {
			t.Errorf("package %q: missing repository label", p.Name)
		}
		if len(p.OwnerReferences) != 1 || p.OwnerReferences[0].Kind != "Repository" || p.OwnerReferences[0].UID != repo.UID {
			t.Errorf("package %q: ownerRef = %+v, want Repository/%s", p.Name, p.OwnerReferences, repo.UID)
		}
		if p.Status.PackageName == "" || p.Status.LastSyncTime == nil {
			t.Errorf("package %q: status not populated: %+v", p.Name, p.Status)
		}
	}

	// Pass 2: "api" bumps its version count, "cli" is unchanged. Capture the
	// resourceVersion of "cli" to prove it isn't rewritten.
	cliRVBefore := findByPackageName(t, got, "cli").ResourceVersion
	second := []backend.PackageInfo{
		{Name: "api", Type: "container", Visibility: "private", VersionCount: 5, HTMLURL: "https://h/api"},
		{Name: "cli", Type: "npm", Visibility: "public", VersionCount: 1},
	}
	if err := r.sync(ctx, c, repo, second); err != nil {
		t.Fatalf("sync pass 2: %v", err)
	}
	got = listPackages(t, c, repo.Name)
	if len(got) != 2 {
		t.Fatalf("pass 2: want 2 packages, got %d", len(got))
	}
	if api := findByPackageName(t, got, "api"); api.Status.VersionCount != 5 {
		t.Errorf("pass 2: api versionCount = %d, want 5", api.Status.VersionCount)
	}
	if cli := findByPackageName(t, got, "cli"); cli.ResourceVersion != cliRVBefore {
		t.Errorf("pass 2: unchanged cli was rewritten (rv %s -> %s)", cliRVBefore, cli.ResourceVersion)
	}

	// Pass 3: "cli" disappears from the host; only "api" should remain.
	third := []backend.PackageInfo{
		{Name: "api", Type: "container", Visibility: "private", VersionCount: 5, HTMLURL: "https://h/api"},
	}
	if err := r.sync(ctx, c, repo, third); err != nil {
		t.Fatalf("sync pass 3: %v", err)
	}
	got = listPackages(t, c, repo.Name)
	if len(got) != 1 || got[0].Status.PackageName != "api" {
		t.Fatalf("pass 3: want only 'api', got %+v", names(got))
	}
}

// TestSyncEmptyDeletesAll confirms an empty host list clears existing packages.
func TestSyncEmptyDeletesAll(t *testing.T) {
	ctx := context.Background()
	repo := testRepo()
	c := newFakeClient(repo)
	r := &Reconciler{}
	if err := r.sync(ctx, c, repo, []backend.PackageInfo{{Name: "x", Type: "npm"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := r.sync(ctx, c, repo, nil); err != nil {
		t.Fatalf("sync empty: %v", err)
	}
	if got := listPackages(t, c, repo.Name); len(got) != 0 {
		t.Fatalf("want 0 packages, got %d", len(got))
	}
}

func TestPackageObjectName(t *testing.T) {
	// Deterministic and stable.
	a := packageObjectName("demo", "container", "ghcr.io/Org/My_App")
	b := packageObjectName("demo", "container", "ghcr.io/Org/My_App")
	if a != b {
		t.Fatalf("not deterministic: %q != %q", a, b)
	}
	// RFC1123-safe: lower-case alnum and '-' only, no leading/trailing dash.
	for i, ch := range a {
		ok := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-'
		if !ok {
			t.Fatalf("name %q has invalid char %q at %d", a, ch, i)
		}
	}
	if a[0] == '-' || a[len(a)-1] == '-' {
		t.Fatalf("name %q has leading/trailing dash", a)
	}
	// Distinct inputs produce distinct names (hash suffix disambiguates).
	if packageObjectName("demo", "npm", "x") == packageObjectName("demo", "container", "x") {
		t.Fatal("different types collided")
	}
	if len(packageObjectName("demo", "container", string(make([]byte, 500)))) > 253 {
		t.Fatal("name exceeds 253 chars")
	}
}

func findByPackageName(t *testing.T, pkgs []codev1alpha1.Package, name string) codev1alpha1.Package {
	t.Helper()
	for _, p := range pkgs {
		if p.Status.PackageName == name {
			return p
		}
	}
	t.Fatalf("package with status.packageName %q not found in %v", name, names(pkgs))
	return codev1alpha1.Package{}
}

func names(pkgs []codev1alpha1.Package) []string {
	out := make([]string, len(pkgs))
	for i, p := range pkgs {
		out[i] = p.Status.PackageName
	}
	return out
}
