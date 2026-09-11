/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package deploykey

import (
	"context"
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

func TestReconcileRequeuesRateLimitedHostCalls(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		name := "ensure"
		if deleting {
			name = "delete"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			key := &codev1alpha1.DeployKey{
				ObjectMeta: metav1.ObjectMeta{Name: "ci", Finalizers: []string{codev1alpha1.FinalizerDeployKey}},
				Spec:       codev1alpha1.DeployKeySpec{RepositoryRef: "demo", PublicKey: "ssh-ed25519 AAAA test"},
			}
			if deleting {
				now := metav1.Now()
				key.DeletionTimestamp = &now
				key.Status.KeyID = "7"
			}
			c := fake.NewClientBuilder().
				WithScheme(codescheme.NewScheme()).
				WithStatusSubresource(&codev1alpha1.DeployKey{}).
				WithObjects(
					key,
					&codev1alpha1.Repository{ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: codev1alpha1.RepositorySpec{ConnectionRef: "conn", Name: "demo"}},
					&codev1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: "conn"}, Spec: codev1alpha1.ConnectionSpec{Provider: codev1alpha1.ProviderGitHub, SecretRef: codev1alpha1.LocalSecretReference{Name: "credential", Namespace: "default", Key: "token"}}},
					&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}},
				).
				Build()
			registry := backend.NewRegistry()
			if err := registry.Register(&fakeBackend{err: &backend.RateLimitError{RetryAt: time.Now().Add(time.Minute)}}); err != nil {
				t.Fatal(err)
			}
			r := &Reconciler{Manager: fakeManager{c: c}, Backends: registry}

			result, err := r.Reconcile(ctx, mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "ci"}}})
			if err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			if result.RequeueAfter < 50*time.Second || result.RequeueAfter > time.Minute {
				t.Fatalf("RequeueAfter = %s, want until the reset (~1m)", result.RequeueAfter)
			}
			var got codev1alpha1.DeployKey
			if err := c.Get(ctx, client.ObjectKey{Name: "ci"}, &got); err != nil {
				t.Fatalf("get DeployKey (finalizer must be kept): %v", err)
			}
			if ready := apimeta.FindStatusCondition(got.Status.Conditions, codev1alpha1.ConditionReady); ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != codev1alpha1.ReasonRateLimited {
				t.Fatalf("Ready = %+v, want False/RateLimited", ready)
			}
		})
	}
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
	err error
}

func (b *fakeBackend) Name() string { return "github" }
func (b *fakeBackend) EnsureDeployKey(context.Context, *codev1alpha1.Connection, backend.Credential, *codev1alpha1.Repository, *codev1alpha1.DeployKey, string) (backend.DeployKeyResult, error) {
	return backend.DeployKeyResult{}, b.err
}
func (b *fakeBackend) DeleteDeployKey(context.Context, *codev1alpha1.Connection, backend.Credential, *codev1alpha1.Repository, string) error {
	return b.err
}
