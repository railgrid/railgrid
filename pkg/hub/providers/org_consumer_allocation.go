// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"fmt"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

var orgConsumerLeaseGVR = schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}

const (
	orgConsumerAllocationLabel   = "providers.railgrid.ai/consumer-allocation"
	orgConsumerAllocationOrg     = "providers.railgrid.ai/consumer-org"
	orgConsumerAllocationPath    = "providers.railgrid.ai/consumer-path"
	orgConsumerAllocationCluster = "providers.railgrid.ai/consumer-cluster"
)

// EnsureOrgConsumerAllocation allocates one persistent consumer slot under the
// org's protected providers-container workspace. parent must be the hub's
// client for that container; the caller must resolve the path and cluster from
// authoritative workspace records. Native quota admission serializes capacity
// across hub replicas. No Lease renewal/expiry is used: disabling a provider
// does not erase stored objects or retired kinds, and cannot release capacity.
func EnsureOrgConsumerAllocation(ctx context.Context, parent dynamic.Interface, orgUUID, consumerPath, consumerCluster string, maxConsumers int64) error {
	pathOrg, workspace, ok := kcppaths.SplitWorkspacePath(consumerPath)
	if !ok || pathOrg != orgUUID || !kcppaths.IsTeamWorkspaceName(workspace) || len(validation.IsDNS1123Label(orgUUID)) != 0 || len(validation.IsDNS1123Label(consumerCluster)) != 0 || consumerCluster == "root" || maxConsumers <= 0 {
		return fmt.Errorf("consumer allocation requires an authoritative team workspace, canonical cluster, and explicit positive capacity")
	}
	if err := ensureWorkspaceQuota(ctx, parent, "railgrid-provider-registrations", map[string]string{"count/workspaces.tenancy.kcp.io": strconv.FormatInt(OrgProviderWorkspaceLimit, 10), "count/leases.coordination.k8s.io": strconv.FormatInt(maxConsumers, 10)}); err != nil {
		return err
	}
	quota, err := parent.Resource(orgQuotaGVR).Namespace("admin").Get(ctx, "railgrid-provider-registrations", metav1.GetOptions{})
	if err != nil {
		return err
	}
	used, _, err := unstructured.NestedString(quota.Object, "status", "used", "count/leases.coordination.k8s.io")
	if err != nil {
		return err
	}
	quantity, err := apiresource.ParseQuantity(used)
	if err != nil {
		return fmt.Errorf("invalid consumer allocation quota usage: %w", err)
	}
	count, exact := quantity.AsInt64()
	if !exact || count < 0 || count > maxConsumers {
		return fmt.Errorf("existing consumer allocations exceed configured capacity")
	}
	if err := verifyOrgQuotaAdmission(ctx, parent); err != nil {
		return err
	}
	name := "consumer-" + consumerCluster
	leases := parent.Resource(orgConsumerLeaseGVR).Namespace("admin")
	expected := map[string]string{orgConsumerAllocationOrg: orgUUID, orgConsumerAllocationPath: consumerPath, orgConsumerAllocationCluster: consumerCluster}
	return retry.OnError(retry.DefaultRetry, func(err error) bool { return apierrors.IsAlreadyExists(err) || apierrors.IsConflict(err) }, func() error {
		existing, err := leases.Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			if existing.GetDeletionTimestamp() != nil || existing.GetLabels()[orgConsumerAllocationLabel] != "true" {
				return fmt.Errorf("consumer allocation %s is not an active hub allocation", name)
			}
			for key, value := range expected {
				if existing.GetAnnotations()[key] != value {
					return fmt.Errorf("consumer allocation %s identity mismatch", name)
				}
			}
			return nil
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		lease := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": map[string]any{"name": name, "namespace": "admin"}, "spec": map[string]any{}}}
		lease.SetLabels(map[string]string{orgConsumerAllocationLabel: "true"})
		lease.SetAnnotations(expected)
		_, err = leases.Create(ctx, lease, metav1.CreateOptions{})
		return err
	})
}
