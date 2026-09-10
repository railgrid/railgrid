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
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

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

type pollingManager struct {
	mcmanager.Manager
	c client.Client
}

func (m pollingManager) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return pollingCluster{c: m.c}, nil
}

type pollingCluster struct {
	cluster.Cluster
	c client.Client
}

func (c pollingCluster) GetClient() client.Client { return c.c }

type pollingLister struct {
	backend.GitBackend
	infos []backend.PackageInfo
	err   error
	calls int
	token string
	fresh bool
}

func (b *pollingLister) Name() string { return "github" }
func (b *pollingLister) ListPackages(ctx context.Context, _ *codev1alpha1.Connection, cred backend.Credential, _ *codev1alpha1.Repository) ([]backend.PackageInfo, error) {
	b.calls++
	b.token = cred.Token
	b.fresh = backend.FreshContainerPackages(ctx)
	return b.infos, b.err
}

func TestReconcileRetainsLastKnownPackagesOnFailureAndRecovers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := testRepo()
		conn := &codev1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: "conn"}, Spec: codev1alpha1.ConnectionSpec{Provider: codev1alpha1.ProviderGitHub, SecretRef: codev1alpha1.LocalSecretReference{Name: "credential", Namespace: "default", Key: "token"}}}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}}
		c := newFakeClient(repo, conn, secret)
		b := &pollingLister{infos: []backend.PackageInfo{{Name: "image", Type: "container", Versions: []backend.PackageVersion{{Digest: "sha256:known", Tags: []string{"latest"}}}}}}
		registry := backend.NewRegistry()
		if err := registry.Register(b); err != nil {
			t.Fatal(err)
		}
		r := &Reconciler{Manager: pollingManager{c: c}, Backends: registry, CrawlInterval: defaultCrawlInterval}
		req := mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}}}
		ctx := context.Background()
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
		before := listPackages(t, c, repo.Name)
		if len(before) != 1 {
			t.Fatal("initial crawl did not create Package")
		}
		b.err = errors.New("transient version lookup failure")
		b.infos = nil
		result, err := r.Reconcile(ctx, req)
		if !errors.Is(err, b.err) || result.RequeueAfter != 0 || b.calls != 2 {
			t.Fatalf("retry=%v err=%v calls=%d", result, err, b.calls)
		}
		after := listPackages(t, c, repo.Name)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("failed crawl changed last known state: before=%v after=%v", before, after)
		}
		// A wrapped throttle uses its deadline rather than the polling interval
		// or workqueue error backoff, and cannot mutate the existing Package.
		b.err = fmt.Errorf("list failed: %w", &backend.RateLimitError{RetryAt: time.Now().Add(10 * time.Minute)})
		result, err = r.Reconcile(ctx, req)
		if err != nil || result.RequeueAfter != 10*time.Minute {
			t.Fatalf("throttle result=%v err=%v", result, err)
		}
		if got := listPackages(t, c, repo.Name); !reflect.DeepEqual(before, got) {
			t.Fatal("throttle changed last known state")
		}
		b.err = &backend.RateLimitError{RetryAt: time.Now().Add(-time.Second)}
		result, err = r.Reconcile(ctx, req)
		if err != nil || result.RequeueAfter != time.Second {
			t.Fatalf("expired deadline stopped retries: %v %v", result, err)
		}
		b.err = nil
		result, err = r.Reconcile(ctx, req)
		if err != nil || result.RequeueAfter != jitter(defaultCrawlInterval, repo) {
			t.Fatalf("recovery poll=%v err=%v", result, err)
		}
		if got := listPackages(t, c, repo.Name); len(got) != 0 {
			t.Fatal("successful empty refresh did not remove stale package")
		}
		for _, object := range []client.Object{secret, conn} {
			if err := c.Delete(ctx, object); err != nil {
				t.Fatal(err)
			}
			result, err = r.Reconcile(ctx, req)
			if err == nil || result.RequeueAfter != 0 {
				t.Fatalf("resolution failure bypassed workqueue retry: %v %v", result, err)
			}
		}
	})
}

func TestReconcileFastCrawlAfterRecentCommit(t *testing.T) {
	now := time.Now()
	commit := func(name, repositoryRef string, phase codev1alpha1.RepositoryCommitPhase, completed time.Time) *codev1alpha1.RepositoryCommit {
		c := &codev1alpha1.RepositoryCommit{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{codev1alpha1.LabelRepository: "demo"}},
			Spec:       codev1alpha1.RepositoryCommitSpec{RepositoryRef: repositoryRef},
			Status:     codev1alpha1.RepositoryCommitStatus{Phase: phase},
		}
		if !completed.IsZero() {
			c.Status.CompletedAt = &metav1.Time{Time: completed}
		}
		return c
	}
	for _, tc := range []struct {
		name    string
		commits []client.Object
		fast    bool
	}{
		{name: "no commits"},
		{name: "recent success", commits: []client.Object{commit("c1", "demo", codev1alpha1.RepositoryCommitPhaseSucceeded, now.Add(-time.Minute))}, fast: true},
		{name: "success outside window", commits: []client.Object{commit("c1", "demo", codev1alpha1.RepositoryCommitPhaseSucceeded, now.Add(-recentCommitWindow-time.Second))}},
		{name: "recent failure", commits: []client.Object{commit("c1", "demo", codev1alpha1.RepositoryCommitPhaseFailed, now.Add(-time.Minute))}},
		{name: "still running", commits: []client.Object{commit("c1", "demo", codev1alpha1.RepositoryCommitPhaseRunning, time.Time{})}},
		{name: "label for another repository", commits: []client.Object{commit("c1", "other", codev1alpha1.RepositoryCommitPhaseSucceeded, now.Add(-time.Minute))}},
		{name: "old and recent", commits: []client.Object{
			commit("c1", "demo", codev1alpha1.RepositoryCommitPhaseSucceeded, now.Add(-time.Hour)),
			commit("c2", "demo", codev1alpha1.RepositoryCommitPhaseSucceeded, now.Add(-9*time.Minute)),
		}, fast: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo()
			conn := &codev1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: "conn"}, Spec: codev1alpha1.ConnectionSpec{Provider: codev1alpha1.ProviderGitHub, SecretRef: codev1alpha1.LocalSecretReference{Name: "credential", Namespace: "default", Key: "token"}}}
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}}
			c := newFakeClient(append([]client.Object{repo, conn, secret}, tc.commits...)...)
			b := &pollingLister{}
			registry := backend.NewRegistry()
			if err := registry.Register(b); err != nil {
				t.Fatal(err)
			}
			r := &Reconciler{Manager: pollingManager{c: c}, Backends: registry, CrawlInterval: defaultCrawlInterval}
			result, err := r.Reconcile(context.Background(), mcreconcile.Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: repo.Name}}})
			if err != nil {
				t.Fatal(err)
			}
			want := jitter(defaultCrawlInterval, repo)
			if tc.fast {
				want = recentCommitCrawlInterval
			}
			if result.RequeueAfter != want || b.fresh != tc.fast {
				t.Fatalf("RequeueAfter=%s fresh=%v, want %s fresh=%v", result.RequeueAfter, b.fresh, want, tc.fast)
			}
		})
	}
}

func TestNextCrawlKeepsShorterConfiguredInterval(t *testing.T) {
	repo := testRepo()
	r := &Reconciler{CrawlInterval: 10 * time.Second}
	if got := r.nextCrawl(repo, true); got != jitter(10*time.Second, repo) {
		t.Fatalf("nextCrawl = %s, want the configured jittered interval", got)
	}
}

func TestCrawlIntervalDefaultsAndOverride(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{{"", 2 * time.Minute}, {"invalid", 2 * time.Minute}, {"-1s", 2 * time.Minute}, {"30s", 30 * time.Second}} {
		t.Setenv("CODE_PACKAGE_CRAWL_INTERVAL", tc.raw)
		if got := crawlIntervalFromEnv(); got != tc.want {
			t.Fatalf("interval(%q)=%s want %s", tc.raw, got, tc.want)
		}
	}
}
