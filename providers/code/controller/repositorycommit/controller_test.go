/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package repositorycommit

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
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
	"github.com/faroshq/provider-code/commitbundle"
	codescheme "github.com/faroshq/provider-code/scheme"
)

func TestBundleArrivalTimedOut(t *testing.T) {
	now := time.Unix(100, 0)
	if bundleArrivalTimedOut(nil, now) {
		t.Fatal("nil start unexpectedly timed out")
	}
	recent := metav1.NewTime(now.Add(-bundleArrivalTimeout + time.Second))
	if bundleArrivalTimedOut(&recent, now) {
		t.Fatal("recent start unexpectedly timed out")
	}
	old := metav1.NewTime(now.Add(-bundleArrivalTimeout))
	if !bundleArrivalTimedOut(&old, now) {
		t.Fatal("old start did not time out")
	}
}

func TestFailAndDeleteBundleRemovesStoredBundle(t *testing.T) {
	ctx := context.Background()
	store, err := commitbundle.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore returned error: %v", err)
	}
	ref, err := store.Put(ctx, "logical-cluster", []commitbundle.File{{Path: "index.html", Content: "<h1>demo</h1>"}})
	if err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	commit := &codev1alpha1.RepositoryCommit{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-commit"},
		Spec: codev1alpha1.RepositoryCommitSpec{
			Source: codev1alpha1.RepositoryCommitSource{
				BundleRef: codev1alpha1.RepositoryCommitBundleReference{Name: ref.Name, Digest: ref.Digest},
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(codescheme.NewScheme()).
		WithStatusSubresource(&codev1alpha1.RepositoryCommit{}).
		WithObjects(commit).
		Build()
	r := &Reconciler{Bundles: store}

	if err := r.failAndDeleteBundle(ctx, c, commit, "missing connection", "logical-cluster", commit.Spec.Source.BundleRef); err != nil {
		t.Fatalf("failAndDeleteBundle returned error: %v", err)
	}
	var got codev1alpha1.RepositoryCommit
	if err := c.Get(ctx, client.ObjectKey{Name: commit.Name}, &got); err != nil {
		t.Fatalf("get RepositoryCommit returned error: %v", err)
	}
	if got.Status.Phase != codev1alpha1.RepositoryCommitPhaseFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
	if _, err := store.Get(ctx, "logical-cluster", ref.Name, ref.Digest); err == nil {
		t.Fatal("bundle still exists after failed RepositoryCommit")
	} else if !commitbundle.IsNotFound(err) {
		t.Fatalf("Get returned %v, want not found", err)
	}
}

func TestFailUpdatesCurrentObjectStatus(t *testing.T) {
	ctx := context.Background()
	current := &codev1alpha1.RepositoryCommit{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-commit", ResourceVersion: "2"},
		Status:     codev1alpha1.RepositoryCommitStatus{Phase: codev1alpha1.RepositoryCommitPhaseRunning},
	}
	c := &recordingStatusClient{Client: fake.NewClientBuilder().
		WithScheme(codescheme.NewScheme()).
		WithStatusSubresource(&codev1alpha1.RepositoryCommit{}).
		WithObjects(current).
		Build()}
	stale := current.DeepCopy()
	stale.ResourceVersion = "1"

	if err := (&Reconciler{}).fail(ctx, c, stale, "github failed"); err != nil {
		t.Fatalf("fail returned error: %v", err)
	}
	if c.updated == nil {
		t.Fatal("Status().Update was not called")
	}
	if got := c.updated.GetResourceVersion(); got != "2" {
		t.Fatalf("updated object resourceVersion = %q, want 2", got)
	}
}

func TestUpdateStatusIfChangedUpdatesCurrentObject(t *testing.T) {
	ctx := context.Background()
	current := &codev1alpha1.RepositoryCommit{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-commit", ResourceVersion: "2"},
		Status:     codev1alpha1.RepositoryCommitStatus{Phase: codev1alpha1.RepositoryCommitPhasePending},
	}
	c := &recordingStatusClient{Client: fake.NewClientBuilder().
		WithScheme(codescheme.NewScheme()).
		WithStatusSubresource(&codev1alpha1.RepositoryCommit{}).
		WithObjects(current).
		Build()}
	stale := current.DeepCopy()
	stale.ResourceVersion = "1"
	stale.Status.Phase = codev1alpha1.RepositoryCommitPhaseRunning

	if err := updateStatusIfChanged(ctx, c, stale); err != nil {
		t.Fatalf("updateStatusIfChanged returned error: %v", err)
	}
	if c.updated == nil {
		t.Fatal("Status().Update was not called")
	}
	if got := c.updated.GetResourceVersion(); got != "2" {
		t.Fatalf("updated object resourceVersion = %q, want 2", got)
	}
}

type commitManager struct {
	mcmanager.Manager
	c client.Client
}

func (m commitManager) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return commitCluster{c: m.c}, nil
}

type commitCluster struct {
	cluster.Cluster
	c client.Client
}

func (c commitCluster) GetClient() client.Client { return c.c }

type fakeCommitter struct {
	backend.GitBackend
	ensureErr error
	commitErr error
	commits   int
	files     []backend.RepositoryCommitFile
}

func (b *fakeCommitter) Name() string { return "github" }
func (b *fakeCommitter) EnsureRepository(context.Context, *codev1alpha1.Connection, backend.Credential, *codev1alpha1.Repository) (backend.RepositoryResult, error) {
	return backend.RepositoryResult{}, b.ensureErr
}
func (b *fakeCommitter) CommitFiles(_ context.Context, _ *codev1alpha1.Connection, _ backend.Credential, _ *codev1alpha1.Repository, input backend.RepositoryCommitInput) (backend.RepositoryCommitResult, error) {
	b.commits++
	b.files = input.Files
	if b.commitErr != nil {
		return backend.RepositoryCommitResult{}, b.commitErr
	}
	return backend.RepositoryCommitResult{CommitSHA: "abc123", Branch: "main", Files: []string{input.Files[0].Path}}, nil
}

type commitFixture struct {
	ctx     context.Context
	c       client.Client
	store   commitbundle.Store
	backend *fakeCommitter
	r       *Reconciler
	req     mcreconcile.Request
	ref     commitbundle.BundleRef
}

// newCommitFixture builds a tenant with a Repository, Connection, credential
// and one RepositoryCommit whose bundle is already stored. startedAt, when
// set, marks the commit as picked up by an earlier reconcile.
func newCommitFixture(t *testing.T, startedAt *metav1.Time) *commitFixture {
	t.Helper()
	return newCommitFixtureWithFiles(t, startedAt, []commitbundle.File{{Path: "index.html", Content: "<h1>demo</h1>"}})
}

// newCommitFixtureWithFiles is newCommitFixture with a caller-chosen bundle.
func newCommitFixtureWithFiles(t *testing.T, startedAt *metav1.Time, files []commitbundle.File) *commitFixture {
	t.Helper()
	ctx := context.Background()
	store, err := commitbundle.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(ctx, "tenant-a", files)
	if err != nil {
		t.Fatal(err)
	}
	commit := &codev1alpha1.RepositoryCommit{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-commit", UID: "commit-uid"},
		Spec: codev1alpha1.RepositoryCommitSpec{
			RepositoryRef: "demo",
			Source:        codev1alpha1.RepositoryCommitSource{BundleRef: codev1alpha1.RepositoryCommitBundleReference{Name: ref.Name, Digest: ref.Digest}},
		},
	}
	if startedAt != nil {
		commit.Status = codev1alpha1.RepositoryCommitStatus{Phase: codev1alpha1.RepositoryCommitPhaseRunning, StartedAt: startedAt}
	}
	c := fake.NewClientBuilder().
		WithScheme(codescheme.NewScheme()).
		WithStatusSubresource(&codev1alpha1.RepositoryCommit{}).
		WithObjects(
			commit,
			&codev1alpha1.Repository{ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: codev1alpha1.RepositorySpec{ConnectionRef: "conn", Name: "demo"}},
			&codev1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: "conn"}, Spec: codev1alpha1.ConnectionSpec{Provider: codev1alpha1.ProviderGitHub, SecretRef: codev1alpha1.LocalSecretReference{Name: "credential", Namespace: "default", Key: "token"}}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}},
		).
		Build()
	b := &fakeCommitter{}
	registry := backend.NewRegistry()
	if err := registry.Register(b); err != nil {
		t.Fatal(err)
	}
	return &commitFixture{
		ctx:     ctx,
		c:       c,
		store:   store,
		backend: b,
		r:       &Reconciler{Manager: commitManager{c: c}, Backends: registry, Bundles: store},
		req:     mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: commit.Name}}},
		ref:     ref,
	}
}

func (f *commitFixture) commit(t *testing.T) codev1alpha1.RepositoryCommit {
	t.Helper()
	var got codev1alpha1.RepositoryCommit
	if err := f.c.Get(f.ctx, client.ObjectKey{Name: "demo-commit"}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *commitFixture) bundleExists(t *testing.T) bool {
	t.Helper()
	_, err := f.store.Get(f.ctx, "tenant-a", f.ref.Name, f.ref.Digest)
	if err != nil && !commitbundle.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestReconcileRequeuesRateLimitedCommit(t *testing.T) {
	for _, step := range []string{"ensure repository", "commit files"} {
		t.Run(step, func(t *testing.T) {
			f := newCommitFixture(t, nil)
			limited := fmt.Errorf("wrapped: %w", &backend.RateLimitError{RetryAt: time.Now().Add(30 * time.Second)})
			if step == "ensure repository" {
				f.backend.ensureErr = limited
			} else {
				f.backend.commitErr = limited
			}
			result, err := f.r.Reconcile(f.ctx, f.req)
			if err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			if result.RequeueAfter < 28*time.Second || result.RequeueAfter > 30*time.Second {
				t.Fatalf("RequeueAfter = %s, want ~30s", result.RequeueAfter)
			}
			got := f.commit(t)
			if got.Status.Phase != codev1alpha1.RepositoryCommitPhaseRunning || got.Status.CompletedAt != nil {
				t.Fatalf("status = %+v, want Running", got.Status)
			}
			ready := apimeta.FindStatusCondition(got.Status.Conditions, codev1alpha1.ConditionReady)
			if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != codev1alpha1.ReasonRateLimited || !strings.HasPrefix(ready.Message, "GitHub rate limit; retrying in ") {
				t.Fatalf("Ready condition = %+v, want RateLimited", ready)
			}
			if !f.bundleExists(t) {
				t.Fatal("rate-limited commit deleted its bundle")
			}

			// The limit lifts: the retry commits and cleans up.
			f.backend.ensureErr, f.backend.commitErr = nil, nil
			if _, err := f.r.Reconcile(f.ctx, f.req); err != nil {
				t.Fatalf("retry returned error: %v", err)
			}
			if got := f.commit(t); got.Status.Phase != codev1alpha1.RepositoryCommitPhaseSucceeded || got.Status.CommitSHA != "abc123" {
				t.Fatalf("retry status = %+v, want Succeeded", got.Status)
			}
			if f.bundleExists(t) {
				t.Fatal("succeeded commit kept its bundle")
			}
		})
	}
}

func TestReconcilePassesBinaryFilesThroughEncoded(t *testing.T) {
	logo := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}
	encoded := base64.StdEncoding.EncodeToString(logo)
	f := newCommitFixtureWithFiles(t, nil, []commitbundle.File{
		{Path: "index.html", Content: "<h1>demo</h1>"},
		{Path: "public/logo.png", Content: encoded, Encoding: commitbundle.EncodingBase64},
	})
	if _, err := f.r.Reconcile(f.ctx, f.req); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	want := []backend.RepositoryCommitFile{
		{Path: "index.html", Content: "<h1>demo</h1>"},
		{Path: "public/logo.png", Content: encoded, Encoding: backend.EncodingBase64},
	}
	if len(f.backend.files) != 2 || f.backend.files[0] != want[0] || f.backend.files[1] != want[1] {
		t.Fatalf("backend files = %#v, want %#v", f.backend.files, want)
	}
	got := f.commit(t)
	sum := sha256.Sum256(logo)
	if len(got.Status.Files) != 2 || got.Status.Files[1].Size != int64(len(logo)) || got.Status.Files[1].Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("status files = %+v, want decoded size/digest for the binary", got.Status.Files)
	}
}

func TestReconcileGivesUpOnRateLimitAfterWindow(t *testing.T) {
	for _, tc := range []struct {
		name      string
		startedAt time.Time
		retryAt   time.Time
	}{
		{name: "window elapsed", startedAt: time.Now().Add(-codev1alpha1.RepositoryCommitRateLimitWindow - time.Second), retryAt: time.Now().Add(10 * time.Second)},
		{name: "reset beyond window", startedAt: time.Now(), retryAt: time.Now().Add(40 * time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			startedAt := metav1.NewTime(tc.startedAt)
			f := newCommitFixture(t, &startedAt)
			f.backend.commitErr = &backend.RateLimitError{RetryAt: tc.retryAt}
			result, err := f.r.Reconcile(f.ctx, f.req)
			if err != nil || result.RequeueAfter != 0 {
				t.Fatalf("Reconcile = %+v, %v; want terminal failure", result, err)
			}
			got := f.commit(t)
			if got.Status.Phase != codev1alpha1.RepositoryCommitPhaseFailed {
				t.Fatalf("phase = %q, want Failed", got.Status.Phase)
			}
			if ready := apimeta.FindStatusCondition(got.Status.Conditions, codev1alpha1.ConditionReady); ready == nil || ready.Reason != codev1alpha1.ReasonError || !strings.Contains(ready.Message, "github: rate limited") {
				t.Fatalf("Ready condition = %+v, want rate limit failure", ready)
			}
			if f.bundleExists(t) {
				t.Fatal("failed commit kept its bundle")
			}
		})
	}
}

func TestRateLimitRetry(t *testing.T) {
	now := time.Unix(10_000, 0)
	started := metav1.NewTime(now.Add(-time.Minute))
	limited := func(at time.Time) error { return &backend.RateLimitError{RetryAt: at} }
	deadline := started.Add(codev1alpha1.RepositoryCommitRateLimitWindow)
	for _, tc := range []struct {
		name    string
		err     error
		started *metav1.Time
		want    time.Time
		wantOK  bool
	}{
		{name: "not rate limited", err: errors.New("boom"), started: &started},
		{name: "not started", err: limited(now.Add(time.Minute))},
		{name: "future reset", err: limited(now.Add(time.Minute)), started: &started, want: now.Add(time.Minute), wantOK: true},
		{name: "expired reset", err: limited(now.Add(-time.Minute)), started: &started, want: now.Add(time.Second), wantOK: true},
		{name: "reset at deadline", err: limited(deadline), started: &started, want: deadline, wantOK: true},
		{name: "reset after deadline", err: limited(deadline.Add(time.Second)), started: &started},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := rateLimitRetry(tc.err, tc.started, now)
			if ok != tc.wantOK || !got.Equal(tc.want) {
				t.Fatalf("rateLimitRetry = %s, %v; want %s, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

type recordingStatusClient struct {
	client.Client
	updated client.Object
}

func (c *recordingStatusClient) Status() client.SubResourceWriter {
	return recordingStatusWriter{SubResourceWriter: c.Client.Status(), updated: &c.updated}
}

type recordingStatusWriter struct {
	client.SubResourceWriter
	updated *client.Object
}

func (w recordingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if copied, ok := obj.DeepCopyObject().(client.Object); ok {
		*w.updated = copied
	} else {
		*w.updated = obj
	}
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}
