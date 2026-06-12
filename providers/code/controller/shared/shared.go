/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package shared holds helpers common to the code provider's reconcilers:
// resolving the per-tenant client from the multicluster manager, condition
// bookkeeping, and credential resolution from a Connection's secretRef.
package shared

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
	"github.com/faroshq/provider-code/tenant"
)

// ClusterClient resolves the controller-runtime client scoped to the tenant
// workspace named by clusterName (the kcp logical cluster the CR lives in).
func ClusterClient(ctx context.Context, mgr mcmanager.Manager, clusterName multicluster.ClusterName) (client.Client, error) {
	cl, err := mgr.GetCluster(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("getting cluster %s: %w", clusterName, err)
	}
	return cl.GetClient(), nil
}

// SetCondition upserts a condition keyed by type. It delegates to apimachinery's
// meta.SetStatusCondition, which manages LastTransitionTime (set to now when the
// status changes, preserved otherwise) — a required field the API server does
// NOT default, so it must be stamped client-side.
func SetCondition(conds *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, msg string, observedGen int64) {
	apimeta.SetStatusCondition(conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: observedGen,
	})
}

// ResolveConnection fetches the Connection named ref in the same (cluster-scoped)
// workspace. Returns a not-found-friendly error the caller can requeue on.
func ResolveConnection(ctx context.Context, c client.Client, ref string) (*codev1alpha1.Connection, error) {
	var conn codev1alpha1.Connection
	if err := c.Get(ctx, types.NamespacedName{Name: ref}, &conn); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("connection %q not found", ref)
		}
		return nil, fmt.Errorf("get connection %q: %w", ref, err)
	}
	return &conn, nil
}

// ResolveRepository fetches the Repository named ref in the same workspace.
func ResolveRepository(ctx context.Context, c client.Client, ref string) (*codev1alpha1.Repository, error) {
	var repo codev1alpha1.Repository
	if err := c.Get(ctx, types.NamespacedName{Name: ref}, &repo); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("repository %q not found", ref)
		}
		return nil, fmt.Errorf("get repository %q: %w", ref, err)
	}
	return &repo, nil
}

// ResolveCredential reads the Connection's referenced Secret via the typed
// tenant-scoped client and returns the backend credential. The secrets read is
// authorized by the provider's APIExport secrets permission claim.
func ResolveCredential(ctx context.Context, c client.Client, conn *codev1alpha1.Connection) (backend.Credential, error) {
	ns := conn.Spec.SecretRef.Namespace
	if ns == "" {
		ns = tenant.DefaultCredentialsNamespace()
	}
	key := conn.Spec.SecretRef.Key
	if key == "" {
		key = tenant.DefaultTokenKey
	}
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: conn.Spec.SecretRef.Name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return backend.Credential{}, tenant.ErrCredentialsMissing
		}
		if apierrors.IsForbidden(err) {
			return backend.Credential{}, tenant.ErrAPIBindingMissing
		}
		return backend.Credential{}, fmt.Errorf("get credential secret %s/%s: %w", ns, conn.Spec.SecretRef.Name, err)
	}
	tok, ok := secret.Data[key]
	if !ok || len(tok) == 0 {
		return backend.Credential{}, fmt.Errorf("credential secret %s/%s has no non-empty key %q", ns, conn.Spec.SecretRef.Name, key)
	}
	return backend.Credential{Token: string(tok)}, nil
}
