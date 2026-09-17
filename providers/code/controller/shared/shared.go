/*
Copyright 2026 The Railgrid Authors.

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
	"errors"
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/tenant"
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

// RateLimitWait reports whether err is (or wraps) a *backend.RateLimitError
// and, if so, how long to wait before retrying — until the host's reset, at
// least a second — plus a Ready-condition message naming the reset. Callers
// return ctrl.Result{RequeueAfter: wait} with a nil error: the backend refuses
// requests locally until the reset, so an error-driven workqueue retry would
// only spin.
//
// The message carries the absolute reset time rather than a countdown so that
// rewriting it on every attempt is a no-op. A message that changed each time
// would bump the object on every status write and re-enqueue it immediately,
// defeating the wait.
func RateLimitWait(err error, now time.Time) (time.Duration, string, bool) {
	var limited *backend.RateLimitError
	if !errors.As(err, &limited) {
		return 0, "", false
	}
	wait := max(limited.RetryAt.Sub(now), time.Second)
	return wait, "GitHub rate limit; retrying at " + limited.RetryAt.UTC().Format(time.RFC3339), true
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

// Credentials resolves Connection credentials for every controller. main
// installs the OAuth refresher when "Connect with GitHub" is configured.
var Credentials tenant.CredentialResolver

// ResolveCredential reads the Connection's referenced Secret via the typed
// tenant-scoped client and returns the backend credential, renewing an
// expiring OAuth token in place. The secrets read and write are authorized by
// the provider's APIExport secrets permission claim.
func ResolveCredential(ctx context.Context, c client.Client, conn *codev1alpha1.Connection) (backend.Credential, error) {
	ns := conn.Spec.SecretRef.Namespace
	if ns == "" {
		ns = tenant.DefaultCredentialsNamespace()
	}
	store := &secretStore{c: c, key: types.NamespacedName{Namespace: ns, Name: conn.Spec.SecretRef.Name}}
	data, _, err := store.Load(ctx)
	if err != nil {
		return backend.Credential{}, err
	}
	return Credentials.ResolveStored(ctx, conn, tenant.CredentialSecretID(conn, ns), data, store)
}

// secretStore adapts a typed client to tenant.SecretStore. Save rewrites the
// object Load returned, so a concurrent change fails with a conflict.
type secretStore struct {
	c      client.Client
	key    types.NamespacedName
	secret corev1.Secret
}

func (s *secretStore) Load(ctx context.Context) (map[string][]byte, string, error) {
	s.secret = corev1.Secret{}
	if err := s.c.Get(ctx, s.key, &s.secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", tenant.ErrCredentialsMissing
		}
		if apierrors.IsForbidden(err) {
			return nil, "", tenant.ErrAPIBindingMissing
		}
		return nil, "", fmt.Errorf("get credential secret %s: %w", s.key, err)
	}
	data := make(map[string][]byte, len(s.secret.Data))
	maps.Copy(data, s.secret.Data)
	return data, s.secret.ResourceVersion, nil
}

func (s *secretStore) Save(ctx context.Context, data map[string][]byte, resourceVersion string) error {
	if s.secret.ResourceVersion != resourceVersion {
		return fmt.Errorf("credential secret %s changed since it was read", s.key)
	}
	updated := s.secret.DeepCopy()
	if updated.Data == nil {
		updated.Data = map[string][]byte{}
	}
	maps.Copy(updated.Data, data)
	return s.c.Update(ctx, updated)
}
