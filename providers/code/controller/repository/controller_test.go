/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package repository

import (
	"context"
	"errors"
	"fmt"
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
	codescheme "github.com/faroshq/provider-code/scheme"
)

func TestReconcileRequeuesRateLimitedEnsure(t *testing.T) {
	f := newRepositoryFixture(t, false)
	reset := time.Now().Add(6 * time.Minute).Truncate(time.Second)
	f.backend.ensureErr = fmt.Errorf("wrapped: %w", &backend.RateLimitError{RetryAt: reset})

	result, err := f.r.Reconcile(f.ctx, f.req)
	if err != nil {
		t.Fatalf("Reconcile returned error %v; a rate limit must requeue, not error", err)
	}
	if result.RequeueAfter < 5*time.Minute || result.RequeueAfter > 6*time.Minute {
		t.Fatalf("RequeueAfter = %s, want until the reset (~6m)", result.RequeueAfter)
	}
	first := f.ready(t)
	want := "GitHub rate limit; retrying at " + reset.UTC().Format(time.RFC3339)
	if first.Status != metav1.ConditionFalse || first.Reason != codev1alpha1.ReasonRateLimited || first.Message != want {
		t.Fatalf("Ready = %+v, want False/RateLimited %q", first, want)
	}

	// Retrying before the reset rewrites an identical condition, so the status
	// write cannot re-enqueue the Repository and restart the loop.
	if _, err := f.r.Reconcile(f.ctx, f.req); err != nil {
		t.Fatalf("second Reconcile returned error: %v", err)
	}
	if second := f.ready(t); second.Message != first.Message || !second.LastTransitionTime.Equal(&first.LastTransitionTime) {
		t.Fatalf("Ready changed between attempts: %+v then %+v", first, second)
	}

	// The limit lifts: the retry ensures the repository.
	f.backend.ensureErr = nil
	if result, err := f.r.Reconcile(f.ctx, f.req); err != nil || result.RequeueAfter != 0 {
		t.Fatalf("retry = %+v, %v; want success", result, err)
	}
	if ready := f.ready(t); ready.Status != metav1.ConditionTrue {
		t.Fatalf("Ready after reset = %+v, want True", ready)
	}
}

func TestReconcileRequeuesRateLimitedDelete(t *testing.T) {
	f := newRepositoryFixture(t, true)
	f.backend.deleteErr = &backend.RateLimitError{RetryAt: time.Now().Add(time.Minute)}

	result, err := f.r.Reconcile(f.ctx, f.req)
	if err != nil {
		t.Fatalf("Reconcile returned error %v; a rate limit must requeue, not error", err)
	}
	if result.RequeueAfter < 50*time.Second || result.RequeueAfter > time.Minute {
		t.Fatalf("RequeueAfter = %s, want ~1m", result.RequeueAfter)
	}
	var got codev1alpha1.Repository
	if err := f.c.Get(f.ctx, client.ObjectKey{Name: "demo"}, &got); err != nil {
		t.Fatalf("rate-limited delete released the finalizer: %v", err)
	}
	if ready := apimeta.FindStatusCondition(got.Status.Conditions, codev1alpha1.ConditionReady); ready == nil || ready.Reason != codev1alpha1.ReasonRateLimited {
		t.Fatalf("Ready = %+v, want RateLimited", ready)
	}
}

func TestReconcileReturnsOtherEnsureErrors(t *testing.T) {
	f := newRepositoryFixture(t, false)
	f.backend.ensureErr = errors.New("github: 500")

	if _, err := f.r.Reconcile(f.ctx, f.req); err == nil {
		t.Fatal("Reconcile swallowed a non-rate-limit error")
	}
	if ready := f.ready(t); ready.Reason != "EnsureFailed" {
		t.Fatalf("Ready = %+v, want EnsureFailed", ready)
	}
}

type repositoryFixture struct {
	ctx     context.Context
	c       client.Client
	backend *fakeBackend
	r       *Reconciler
	req     mcreconcile.Request
}

// newRepositoryFixture builds a tenant with a finalized Repository, its
// Connection and credential. deleting marks the Repository for deletion.
func newRepositoryFixture(t *testing.T, deleting bool) *repositoryFixture {
	t.Helper()
	repo := &codev1alpha1.Repository{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Finalizers: []string{codev1alpha1.FinalizerRepository}},
		Spec:       codev1alpha1.RepositorySpec{ConnectionRef: "conn", Name: "demo"},
	}
	if deleting {
		now := metav1.Now()
		repo.DeletionTimestamp = &now
	}
	c := fake.NewClientBuilder().
		WithScheme(codescheme.NewScheme()).
		WithStatusSubresource(&codev1alpha1.Repository{}).
		WithObjects(
			repo,
			&codev1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: "conn"}, Spec: codev1alpha1.ConnectionSpec{Provider: codev1alpha1.ProviderGitHub, SecretRef: codev1alpha1.LocalSecretReference{Name: "credential", Namespace: "default", Key: "token"}}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}},
		).
		Build()
	b := &fakeBackend{}
	registry := backend.NewRegistry()
	if err := registry.Register(b); err != nil {
		t.Fatal(err)
	}
	return &repositoryFixture{
		ctx:     context.Background(),
		c:       c,
		backend: b,
		r:       &Reconciler{Manager: fakeManager{c: c}, Backends: registry},
		req:     mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "demo"}}},
	}
}

func (f *repositoryFixture) ready(t *testing.T) metav1.Condition {
	t.Helper()
	var got codev1alpha1.Repository
	if err := f.c.Get(f.ctx, client.ObjectKey{Name: "demo"}, &got); err != nil {
		t.Fatal(err)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, codev1alpha1.ConditionReady)
	if ready == nil {
		t.Fatal("Repository has no Ready condition")
	}
	return *ready
}

type fakeManager struct {
	mcmanager.Manager
	c client.Client
}

func (m fakeManager) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return fakeCluster{c: m.c}, nil
}

type fakeCluster struct {
	cluster.Cluster
	c client.Client
}

func (c fakeCluster) GetClient() client.Client { return c.c }

type fakeBackend struct {
	backend.GitBackend
	ensureErr error
	deleteErr error
}

func (b *fakeBackend) Name() string { return "github" }
func (b *fakeBackend) EnsureRepository(context.Context, *codev1alpha1.Connection, backend.Credential, *codev1alpha1.Repository) (backend.RepositoryResult, error) {
	if b.ensureErr != nil {
		return backend.RepositoryResult{}, b.ensureErr
	}
	return backend.RepositoryResult{RepoID: "42", HTMLURL: "https://github.com/acme/demo"}, nil
}
func (b *fakeBackend) DeleteRepository(context.Context, *codev1alpha1.Connection, backend.Credential, *codev1alpha1.Repository) error {
	return b.deleteErr
}
