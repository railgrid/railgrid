// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package shared

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
)

const (
	DefaultCredentialsNamespace = "default"
	DefaultTokenKey             = "token"
	DependencyRetryAfter        = 15 * time.Second
	ValidationRefreshAfter      = 5 * time.Minute
)

func ClusterClient(ctx context.Context, mgr mcmanager.Manager, clusterName multicluster.ClusterName) (client.Client, error) {
	cl, err := mgr.GetCluster(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("getting cluster %s: %w", clusterName, err)
	}
	return cl.GetClient(), nil
}

func SetCondition(conds *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, msg string, observedGen int64) {
	apimeta.SetStatusCondition(conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: observedGen,
	})
}

func ResolveConnection(ctx context.Context, c client.Client, name string) (*databricksv1alpha1.Connection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("connectionRef is required")
	}
	var conn databricksv1alpha1.Connection
	if err := c.Get(ctx, types.NamespacedName{Name: name}, &conn); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("connection %q not found", name)
		}
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("connection %q is not readable; check the provider APIBinding claims", name)
		}
		return nil, fmt.Errorf("get connection %q: %w", name, err)
	}
	return &conn, nil
}

func ResolveWarehouse(ctx context.Context, c client.Client, name string) (*databricksv1alpha1.Warehouse, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("warehouseRef is required")
	}
	var wh databricksv1alpha1.Warehouse
	if err := c.Get(ctx, types.NamespacedName{Name: name}, &wh); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("warehouse %q not found", name)
		}
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("warehouse %q is not readable; check the provider APIBinding claims", name)
		}
		return nil, fmt.Errorf("get warehouse %q: %w", name, err)
	}
	return &wh, nil
}

func ResolveBearerToken(ctx context.Context, c client.Client, conn *databricksv1alpha1.Connection) (string, error) {
	ns := conn.Spec.SecretRef.Namespace
	if ns == "" {
		ns = DefaultCredentialsNamespace
	}
	key := conn.Spec.SecretRef.Key
	if key == "" {
		key = DefaultTokenKey
	}
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: conn.Spec.SecretRef.Name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("credential secret %s/%s not found", ns, conn.Spec.SecretRef.Name)
		}
		if apierrors.IsForbidden(err) {
			return "", fmt.Errorf("credential secret %s/%s is not readable; check the provider APIBinding secrets claim", ns, conn.Spec.SecretRef.Name)
		}
		return "", fmt.Errorf("get credential secret %s/%s: %w", ns, conn.Spec.SecretRef.Name, err)
	}
	token, ok := secret.Data[key]
	if !ok || len(token) == 0 {
		return "", fmt.Errorf("credential secret %s/%s missing non-empty key %q", ns, conn.Spec.SecretRef.Name, key)
	}
	return string(token), nil
}
