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

var (
	namespaceGVR  = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	limitRangeGVR = schema.GroupVersionResource{Version: "v1", Resource: "limitranges"}
)

const (
	// tenantLimitRangeName is the LimitRange every tenant namespace gets on
	// creation. Create-only: an operator may hand-tune a tenant's copy and we
	// never fight them over it.
	tenantLimitRangeName = "kedge-defaults"

	// tenantLimitRangeDisableEnv opts the platform out of stamping the
	// LimitRange (KEDGE_TENANT_LIMITRANGE=disabled) — for runtime clusters
	// that manage resource policy themselves (their own LimitRange/Kyverno/
	// quota tooling).
	tenantLimitRangeDisableEnv = "KEDGE_TENANT_LIMITRANGE"
)

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
	if err := c.ensureTenantLimitRange(ctx, tenantPath, name); err != nil {
		return "", err
	}
	c.nsCache.Store(tenantPath, name)
	return name, nil
}

// ensureTenantLimitRange stamps the default container resource policy into a
// tenant namespace. No seed template pins its own CPU/memory (except where a
// knob is explicitly wired, e.g. redis-cache's size), so without this a
// tenant workload on the shared runtime cluster is unbounded — the
// noisy-neighbor hole. The LimitRange closes it at admission time:
//
//   - containers with no resources get defaultRequest 50m/128Mi and a
//     default limit of 500m/512Mi;
//   - a container may still ask for more explicitly, but never past max
//     (2 CPU / 2Gi) — the per-container ceiling of the platform's dev-grade
//     tier.
//
// Create-only and sitting BEFORE the nsCache store, so a provider restart
// retrofits namespaces created before this policy existed, while an
// operator's hand-tuned copy is never overwritten. Disable with
// KEDGE_TENANT_LIMITRANGE=disabled when the runtime cluster manages its own
// resource policy.
func (c *realClient) ensureTenantLimitRange(ctx context.Context, tenantPath, namespace string) error {
	if os.Getenv(tenantLimitRangeDisableEnv) == "disabled" {
		return nil
	}
	lr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "LimitRange",
		"metadata": map[string]any{
			"name":      tenantLimitRangeName,
			"namespace": namespace,
			"labels": map[string]any{
				LabelTenant:    tenantHash(tenantPath),
				LabelManagedBy: ManagedByValue,
			},
		},
		"spec": map[string]any{
			"limits": []any{map[string]any{
				"type": "Container",
				"defaultRequest": map[string]any{
					"cpu":    "50m",
					"memory": "128Mi",
				},
				"default": map[string]any{
					"cpu":    "500m",
					"memory": "512Mi",
				},
				"max": map[string]any{
					"cpu":    "2",
					"memory": "2Gi",
				},
			}},
		},
	}}
	_, err := c.dyn.Resource(limitRangeGVR).Namespace(namespace).Create(ctx, lr, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create limitrange %s/%s: %w", namespace, tenantLimitRangeName, err)
	}
	return nil
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
