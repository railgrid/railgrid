// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package install creates kuery's PROVIDER-PRIVATE storage in its own kcp
// workspace.
//
// The Engagement CRD is applied here rather than shipped in
// deploy/chart/files/schemas/, and that separation is the point: `init`
// applies that whole directory to the APIExport, so anything placed there
// becomes bindable by every tenant. Engagement is kuery's own bookkeeping —
// which replica syncs which tenant's edge — and no tenant should be able to
// bind it, claim it, read it or write it. A plain CRD in the provider
// workspace gets exactly that: visible to the provider's own credential,
// invisible through the export.
//
// Same shape as the planner provider's ActionReceipt storage. No RBAC objects
// are created alongside it: the hub grants the provider ServiceAccount
// workspace-scoped cluster-admin when it provisions the workspace
// (pkg/hub/providers/provision.go EnsureProviderSAAtPath), so the credential
// that applies the CRD is already the one that will use it.
package install

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

// fieldManager identifies this package's server-side-apply writes.
const fieldManager = "kuery-private-storage"

// engagementCRDName is the CRD the Engagement records live under.
const engagementCRDName = "engagements." + kueryv1alpha1.GroupName

var crdGVR = schema.GroupVersionResource{
	Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
}

// establishTimeout bounds the wait for the API server to serve the new kind.
// `init` is an init container: failing loudly here is better than letting
// serve start against a kind that is not there yet.
const establishTimeout = time.Minute

// EnsureEngagementCRD applies the Engagement CRD into the workspace cfg points
// at and waits for it to be Established. Idempotent.
//
// The schema is written out here rather than generated into the chart because
// it must NOT travel with the exported schemas; config/crds carries the
// controller-gen rendering of the same type for reference. Keep the two in
// step: the reconciler reads these fields by name.
func EnsureEngagementCRD(ctx context.Context, cfg *rest.Config) error {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("kuery private storage: building client: %w", err)
	}
	crds := client.Resource(crdGVR)

	body, err := json.Marshal(engagementCRD())
	if err != nil {
		return fmt.Errorf("kuery private storage: encoding the Engagement CRD: %w", err)
	}
	if _, err := crds.Patch(ctx, engagementCRDName, types.ApplyPatchType, body, metav1.PatchOptions{FieldManager: fieldManager}); err != nil {
		return fmt.Errorf("kuery private storage: applying the Engagement CRD: %w", err)
	}

	if err := wait.PollUntilContextTimeout(ctx, time.Second, establishTimeout, true, func(ctx context.Context) (bool, error) {
		crd, err := crds.Get(ctx, engagementCRDName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		conditions, _, _ := unstructured.NestedSlice(crd.Object, "status", "conditions")
		for _, entry := range conditions {
			condition, ok := entry.(map[string]any)
			if ok && condition["type"] == "Established" && condition["status"] == "True" {
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("kuery private storage: waiting for the Engagement CRD to be established: %w", err)
	}
	return nil
}

// engagementCRD is the applied document. Cluster-scoped, with a status
// subresource, mirroring apis/v1alpha1.Engagement field for field.
func engagementCRD() map[string]any {
	timestamp := map[string]any{"type": "string", "format": "date-time"}
	return map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": engagementCRDName},
		"spec": map[string]any{
			"group": kueryv1alpha1.GroupName,
			"scope": "Cluster",
			"names": map[string]any{
				"plural":     "engagements",
				"singular":   "engagement",
				"kind":       "Engagement",
				"listKind":   "EngagementList",
				"shortNames": []any{"keng"},
			},
			"versions": []any{map[string]any{
				"name":         kueryv1alpha1.Version,
				"served":       true,
				"storage":      true,
				"subresources": map[string]any{"status": map[string]any{}},
				"additionalPrinterColumns": []any{
					map[string]any{"name": "Cluster", "type": "string", "jsonPath": ".spec.cluster"},
					map[string]any{"name": "Edge", "type": "string", "jsonPath": ".spec.edge"},
					map[string]any{"name": "Phase", "type": "string", "jsonPath": ".status.phase"},
					map[string]any{"name": "Owner", "type": "string", "jsonPath": ".status.owner"},
					map[string]any{"name": "Last seen", "type": "date", "jsonPath": ".status.lastSeen"},
				},
				"schema": map[string]any{"openAPIV3Schema": map[string]any{
					"description": "Engagement records that kuery syncs one tenant's edge, and which replica is doing it. Provider-private: never exported.",
					"type":        "object",
					"properties": map[string]any{
						"apiVersion": map[string]any{"type": "string"},
						"kind":       map[string]any{"type": "string"},
						"metadata":   map[string]any{"type": "object"},
						"spec": map[string]any{
							"type":     "object",
							"required": []any{"cluster", "edge"},
							"properties": map[string]any{
								"cluster": map[string]any{
									"description": "The tenant workspace's kcp logical-cluster ID. Never a workspace path.",
									"type":        "string", "maxLength": int64(63),
									"pattern": "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$",
								},
								"edge": map[string]any{
									"description": "The KubernetesCluster edge's name in that workspace.",
									"type":        "string", "maxLength": int64(253),
								},
							},
						},
						"status": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"phase": map[string]any{
									"description": "Only Engaged is queryable.",
									"type":        "string",
									"enum": []any{
										string(kueryv1alpha1.EngagementPhasePending),
										string(kueryv1alpha1.EngagementPhaseEngaged),
										string(kueryv1alpha1.EngagementPhaseStale),
										string(kueryv1alpha1.EngagementPhaseDisengaged),
									},
								},
								"owner":    map[string]any{"type": "string", "maxLength": int64(253)},
								"lastSeen": timestamp,
								"message":  map[string]any{"type": "string", "maxLength": int64(1024)},
							},
						},
					},
				}},
			}},
		},
	}
}
