/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package connection

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

// A rate-limited validation must not read as a rejected credential
// (ValidationFailed, never retried) and must retry once the limit resets.
func TestReconcileRequeuesRateLimitedValidation(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().
		WithScheme(codescheme.NewScheme()).
		WithStatusSubresource(&codev1alpha1.Connection{}).
		WithObjects(
			&codev1alpha1.Connection{
				ObjectMeta: metav1.ObjectMeta{Name: "conn", Finalizers: []string{codev1alpha1.FinalizerConnection}},
				Spec:       codev1alpha1.ConnectionSpec{Provider: codev1alpha1.ProviderGitHub, SecretRef: codev1alpha1.LocalSecretReference{Name: "credential", Namespace: "default", Key: "token"}},
			},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}, Data: map[string][]byte{"token": []byte("test-token")}},
		).
		Build()
	b := &fakeBackend{err: &backend.RateLimitError{RetryAt: time.Now().Add(time.Minute)}}
	registry := backend.NewRegistry()
	if err := registry.Register(b); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Manager: fakeManager{c: c}, Backends: registry}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "conn"}}}

	result, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.RequeueAfter < 50*time.Second || result.RequeueAfter > time.Minute {
		t.Fatalf("RequeueAfter = %s, want until the reset (~1m)", result.RequeueAfter)
	}
	var got codev1alpha1.Connection
	if err := c.Get(ctx, client.ObjectKey{Name: "conn"}, &got); err != nil {
		t.Fatal(err)
	}
	for _, condType := range []string{codev1alpha1.ConditionValidated, codev1alpha1.ConditionReady} {
		cond := apimeta.FindStatusCondition(got.Status.Conditions, condType)
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != codev1alpha1.ReasonRateLimited {
			t.Fatalf("%s = %+v, want False/RateLimited", condType, cond)
		}
	}

	b.err = nil
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("retry returned error: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "conn"}, &got); err != nil {
		t.Fatal(err)
	}
	if !apimeta.IsStatusConditionTrue(got.Status.Conditions, codev1alpha1.ConditionValidated) || got.Status.Login != "octocat" {
		t.Fatalf("status after reset = %+v, want validated as octocat", got.Status)
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
func (b *fakeBackend) ValidateConnection(context.Context, *codev1alpha1.Connection, backend.Credential) (string, []string, error) {
	if b.err != nil {
		return "", nil, b.err
	}
	return "octocat", []string{"repo"}, nil
}
