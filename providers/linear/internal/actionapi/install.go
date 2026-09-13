// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package actionapi

import (
	"context"
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Install creates provider-local persistence. This CRD is deliberately absent
// from the exported schemas and CatalogEntry; tenants cannot bind or claim it.
func Install(ctx context.Context, cfg *rest.Config) error {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	crds := client.Resource(schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"})
	record := map[string]any{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]any{"name": "actionreceipts.linear.internal.faros.sh"}, "spec": map[string]any{"group": "linear.internal.faros.sh", "scope": "Cluster", "names": map[string]any{"plural": "actionreceipts", "singular": "actionreceipt", "kind": "ActionReceipt", "listKind": "ActionReceiptList"}, "versions": []any{map[string]any{"name": "v1alpha1", "served": true, "storage": true, "subresources": map[string]any{"status": map[string]any{}}, "schema": map[string]any{"openAPIV3Schema": map[string]any{"type": "object", "properties": map[string]any{"spec": map[string]any{"type": "object", "x-kubernetes-preserve-unknown-fields": true}, "status": map[string]any{"type": "object", "x-kubernetes-preserve-unknown-fields": true}}}}}}}}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err = crds.Patch(ctx, "actionreceipts.linear.internal.faros.sh", types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: "linear-private-storage"}); err != nil {
		return err
	}
	if err := wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		u, err := crds.Get(ctx, "actionreceipts.linear.internal.faros.sh", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		for _, v := range conditions {
			if m, ok := v.(map[string]any); ok && m["type"] == "Established" && m["status"] == "True" {
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		return err
	}
	objects := []struct {
		resource string
		body     map[string]any
	}{
		{"clusterroles", map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": map[string]any{"name": "linear-private-actions"}, "rules": []any{map[string]any{"apiGroups": []any{"linear.internal.faros.sh"}, "resources": []any{"actionreceipts", "actionreceipts/status"}, "verbs": []any{"get", "list", "create", "update", "delete"}}}}},
		{"clusterrolebindings", map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": "linear-private-actions"}, "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "linear-private-actions"}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "provider", "namespace": "default"}}}},
	}
	for _, object := range objects {
		body, err := json.Marshal(object.body)
		if err != nil {
			return err
		}
		if _, err = client.Resource(schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: object.resource}).Patch(ctx, "linear-private-actions", types.ApplyPatchType, body, metav1.PatchOptions{FieldManager: "linear-private-storage"}); err != nil {
			return err
		}
	}
	return nil
}
