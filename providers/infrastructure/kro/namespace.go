// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package kro

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var namespaceGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// tenantNamespaceName derives a deterministic namespace name from a
// kcp workspace path. SHA256-truncated to 12 hex chars so it's
// idempotent across restarts but short enough to leave room for the
// prefix under Kubernetes' 63-char limit on label values that may
// reference the namespace name.
func tenantNamespaceName(tenantPath string) string {
	prefix := os.Getenv("KRO_NAMESPACE_PREFIX")
	if prefix == "" {
		prefix = "kedge-tenants-"
	}
	return prefix + tenantHash(tenantPath)
}

// tenantHash returns a 12-char SHA prefix of a workspace path. Used
// both for the namespace name AND for label values (kedge.faros.sh/tenant)
// — the raw `root:kedge:orgs:<uuid>` form contains `:` which Kubernetes
// rejects in label values (allowed: alphanumeric + `-_.` only). The full
// path is preserved as an annotation
// (kedge.faros.sh/tenant-workspace-path) for human debugging; programmatic
// label-selector lookups go through this hash so writer + reader agree
// without having to parse colons.
func tenantHash(tenantPath string) string {
	sum := sha256.Sum256([]byte(tenantPath))
	return hex.EncodeToString(sum[:6])
}

// LabelTenantValue is the public form of tenantHash. The server
// handlers + MCP tools build label selectors with this so they match
// what EnsureTenantNamespace / CreateInstance / UpsertCredentialsSecret
// wrote.
func LabelTenantValue(tenantPath string) string { return tenantHash(tenantPath) }

// TenantNamespace is the public form of tenantNamespaceName: the
// per-tenant namespace on the runtime cluster a tenant's workloads land
// in. The Application instance controller writes the bridged OIDC Secret
// here so it sits beside the oauth2-proxy pod the kro fork materializes.
func TenantNamespace(tenantPath string) string { return tenantNamespaceName(tenantPath) }

// EnsureTenantNamespace creates the per-tenant namespace in the central
// kro cluster if absent. Idempotent. Returns the namespace name. Caches
// the result so the warm path skips the kcp roundtrip. Annotations
// record the source tenant path for human debugging — a `kubectl
// describe ns` from an admin should make the kedge link obvious.
func (c *realClient) EnsureTenantNamespace(ctx context.Context, tenantPath string) (string, error) {
	name := tenantNamespaceName(tenantPath)
	if _, ok := c.nsCache.Load(tenantPath); ok {
		return name, nil
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				LabelTenant:    tenantHash(tenantPath), // raw path has `:` — invalid in labels
				LabelManagedBy: ManagedByValue,
			},
			Annotations: map[string]string{
				// Full path here so `kubectl describe ns` surfaces the
				// source workspace; programmatic lookups use the
				// LabelTenant hash above.
				"kedge.faros.sh/tenant-workspace-path": tenantPath,
			},
		},
	}
	u, err := toUnstructured(ns)
	if err != nil {
		return "", fmt.Errorf("marshal namespace: %w", err)
	}
	_, err = c.dyn.Resource(namespaceGVR).Create(ctx, u, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create namespace %s: %w", name, err)
	}
	c.nsCache.Store(tenantPath, name)
	return name, nil
}

// toUnstructured marshals a typed object into the unstructured shape
// the dynamic client expects. The corev1 schemes don't always set
// TypeMeta so we patch it explicitly to avoid surprising "missing
// apiVersion/kind" failures from the apiserver.
func toUnstructured(obj any) (*unstructured.Unstructured, error) {
	switch v := obj.(type) {
	case *corev1.Namespace:
		return &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "Namespace",
				"metadata": map[string]any{
					"name":        v.Name,
					"labels":      stringMapToAny(v.Labels),
					"annotations": stringMapToAny(v.Annotations),
				},
			},
		}, nil
	case *corev1.Secret:
		dataAny := make(map[string]any, len(v.Data))
		for k, b := range v.Data {
			// Secret.Data is []byte; Unstructured wants base64
			// string. k8s.io/apimachinery/pkg/apis/meta/v1
			// converters know this but the dynamic client uses raw
			// JSON, so we encode here. Importing encoding/base64 in
			// the consumer rather than here keeps the helper tight.
			dataAny[k] = b // dynamic client encodes []byte → base64 on marshal
		}
		out := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata": map[string]any{
					"name":        v.Name,
					"namespace":   v.Namespace,
					"labels":      stringMapToAny(v.Labels),
					"annotations": stringMapToAny(v.Annotations),
				},
				"type": string(v.Type),
				"data": dataAny,
			},
		}
		if len(v.OwnerReferences) > 0 {
			refs := make([]any, len(v.OwnerReferences))
			for i, r := range v.OwnerReferences {
				refs[i] = map[string]any{
					"apiVersion":         r.APIVersion,
					"kind":               r.Kind,
					"name":               r.Name,
					"uid":                string(r.UID),
					"controller":         ptrTrue(r.Controller),
					"blockOwnerDeletion": ptrTrue(r.BlockOwnerDeletion),
				}
			}
			meta := out.Object["metadata"].(map[string]any)
			meta["ownerReferences"] = refs
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported type %T for toUnstructured", obj)
}

func stringMapToAny(in map[string]string) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func ptrTrue(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}
