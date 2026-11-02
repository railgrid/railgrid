// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	utiluuid "k8s.io/apimachinery/pkg/util/uuid"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
)

var orgQuotaGVR = schema.GroupVersionResource{Version: "v1", Resource: "resourcequotas"}

const orgQuotaName = "railgrid-provider-storage"

const OrgProviderWorkspaceLimit int64 = 8
const OrgProviderPrivateResourceLimit = 8
const orgProviderPrivateObjectLimit int64 = 32
const orgQuotaProbePrefix = "railgrid-quota-probe-"
const orgQuotaProbeLabel = "providers.railgrid.ai/quota-probe"
const orgQuotaProbeExpiry = "providers.railgrid.ai/quota-probe-expires"

// kcp's KCPKubeResourceQuota admission adds this protected admin-namespace
// quota to every request in the logical cluster, including cluster-scoped CRs.
// Keep old resource keys: dropping a declaration must never uncap old objects.
func orgProviderBaseQuota() map[string]string {
	return map[string]string{
		"count/secrets": "64", "count/configmaps": "64",
		"count/leases.coordination.k8s.io":                     "32",
		"count/apiresourceschemas.apis.kcp.io":                 "64",
		"count/apiexports.apis.kcp.io":                         "1",
		"count/apiexportendpointslices.apis.kcp.io":            "1",
		"count/customresourcedefinitions.apiextensions.k8s.io": strconv.Itoa(OrgProviderPrivateResourceLimit + 1),
		"count/catalogentries.providers.railgrid.ai":           "1",
	}
}

func ensureOrgProviderQuota(ctx context.Context, cl dynamic.Interface, resources []schema.GroupResource) error {
	hard := orgProviderBaseQuota()
	for _, resource := range resources {
		if err := ValidateOrgProviderPrivateResource(resource); err != nil {
			return err
		}
		hard["count/"+resource.Resource+"."+resource.Group] = strconv.FormatInt(orgProviderPrivateObjectLimit, 10)
	}
	return ensureWorkspaceQuota(ctx, cl, orgQuotaName, hard)
}

func ensureWorkspaceQuota(ctx context.Context, cl dynamic.Interface, quotaName string, hard map[string]string) error {
	return ensureWorkspaceQuotaWithLimits(ctx, cl, quotaName, hard, nil)
}

func ensureWorkspaceQuotaWithLimits(ctx context.Context, cl dynamic.Interface, quotaName string, hard map[string]string, limits *ConsumerQuotaLimits) error {
	return ensureWorkspaceQuotaWithPendingStatus(ctx, cl, quotaName, hard, limits, nil)
}

// ensureWorkspaceQuotaWithPendingStatus keeps the public quota path strict by
// default. A pending key must already have a hard limit and may omit only its
// status.used observation; status.hard must still match before this returns.
func ensureWorkspaceQuotaWithPendingStatus(ctx context.Context, cl dynamic.Interface, quotaName string, hard map[string]string, limits *ConsumerQuotaLimits, pendingStatus map[string]bool) error {
	for key, pending := range pendingStatus {
		if !pending {
			continue
		}
		if _, ok := hard[key]; !ok {
			return fmt.Errorf("pending quota status key %q has no hard limit", key)
		}
		if _, err := quotaGroupResource(key); err != nil {
			return err
		}
	}
	ns := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "admin"}}}
	if _, err := cl.Resource(namespaceGVR).Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	quotas := cl.Resource(orgQuotaGVR).Namespace("admin")
	err := retry.OnError(retry.DefaultRetry, func(err error) bool {
		return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
	}, func() error {
		current, err := quotas.Get(ctx, quotaName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			current = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ResourceQuota", "metadata": map[string]any{"name": quotaName, "namespace": "admin", "annotations": map[string]any{"experimental.quota.kcp.io/cluster-scoped": "true"}}, "spec": map[string]any{}}}
			if strings.HasPrefix(quotaName, orgQuotaProbePrefix) {
				current.SetLabels(map[string]string{orgQuotaProbeLabel: "true"})
				annotations := current.GetAnnotations()
				annotations[orgQuotaProbeExpiry] = time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
				current.SetAnnotations(annotations)
			}
			if limits != nil {
				if err := boundConsumerQuotaHard(hard, *limits); err != nil {
					return err
				}
			}
			if err := unstructured.SetNestedStringMap(current.Object, hard, "spec", "hard"); err != nil {
				return err
			}
			_, err = quotas.Create(ctx, current, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		prior, _, err := unstructured.NestedStringMap(current.Object, "spec", "hard")
		if err != nil {
			return err
		}
		if prior == nil {
			prior = map[string]string{}
		}
		for key, value := range hard {
			prior[key] = value
		}
		if limits != nil {
			if err := boundConsumerQuotaHard(prior, *limits); err != nil {
				return err
			}
		}
		if err := unstructured.SetNestedStringMap(current.Object, prior, "spec", "hard"); err != nil {
			return err
		}
		annotations := current.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["experimental.quota.kcp.io/cluster-scoped"] = "true"
		current.SetAnnotations(annotations)
		_, err = quotas.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("installing provider storage quota: %w", err)
	}
	// By default no grant proceeds until quota-controller has initialized every
	// counter. The narrow pending-status variant may defer only status.used for
	// a newly reserved, not-yet-served consumer family; its hard status and every
	// mandatory counter must still be observed before return. A missing/disabled
	// quota controller therefore leaves enrollment unavailable.
	return wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		current, err := quotas.Get(ctx, quotaName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		wanted, _, err := unstructured.NestedStringMap(current.Object, "spec", "hard")
		if err != nil {
			return false, err
		}
		observed, _, err := unstructured.NestedStringMap(current.Object, "status", "hard")
		if err != nil {
			return false, err
		}
		used, _, err := unstructured.NestedStringMap(current.Object, "status", "used")
		if err != nil {
			return false, err
		}
		for key, value := range wanted {
			if observed[key] != value {
				return false, nil
			}
			usedValue, initialized := used[key]
			if !initialized {
				if pendingStatus[key] {
					continue
				}
				return false, nil
			}
			if limits != nil {
				quantity, err := apiresource.ParseQuantity(usedValue)
				if err != nil {
					return false, fmt.Errorf("invalid observed consumer quota usage for %s: %w", key, err)
				}
				count, exact := quantity.AsInt64()
				if !exact || count < 0 || count > limits.MaxObjectsPerResource {
					return false, fmt.Errorf("consumer storage already exceeds configured object limit for %s", key)
				}
			}
		}
		return true, nil
	})
}

// EnsureOrgProviderRegistrationQuota bounds provider workspaces per organization.
func EnsureOrgProviderRegistrationQuota(ctx context.Context, cl dynamic.Interface) error {
	if err := ensureWorkspaceQuota(ctx, cl, "railgrid-provider-registrations", map[string]string{"count/workspaces.tenancy.kcp.io": strconv.FormatInt(OrgProviderWorkspaceLimit, 10)}); err != nil {
		return err
	}
	return verifyOrgQuotaAdmission(ctx, cl)
}

// An initialized quota proves the controller is running, but not that admission
// is enabled. A temporary zero quota plus a dry-run create verifies enforcement
// without persisting data. The random name and UID precondition isolate cleanup
// across concurrent enrollment requests.
func verifyOrgQuotaAdmission(ctx context.Context, cl dynamic.Interface) (result error) {
	if err := cleanupExpiredOrgQuotaProbes(ctx, cl, time.Now()); err != nil {
		return err
	}
	name := orgQuotaProbePrefix + string(utiluuid.NewUUID())
	quotas := cl.Resource(orgQuotaGVR).Namespace("admin")
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		current, err := quotas.Get(cleanupCtx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		if err != nil {
			if result == nil {
				result = err
			}
			return
		}
		uid := current.GetUID()
		if err := quotas.Delete(cleanupCtx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) && result == nil {
			result = err
		}
	}()
	if err := ensureWorkspaceQuota(ctx, cl, name, map[string]string{"count/configmaps": "0"}); err != nil {
		return err
	}
	configmap := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": name, "namespace": "default"}}}
	// A different namespace proves kcp's cluster-scoped extension is active.
	// Retry briefly because admission's quota informer may trail status reads.
	err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 3*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := cl.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace("default").Create(ctx, configmap, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if apierrors.IsForbidden(err) {
			// A different exhausted quota (including a namespace-local quota)
			// does not prove this cluster-scoped probe was enforced.
			_, detail, found := strings.Cut(err.Error(), "exceeded quota: ")
			quotaName, _, _ := strings.Cut(detail, ",")
			if found && strings.TrimSpace(quotaName) == name {
				return true, nil
			}
		}
		if err != nil {
			return false, err
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("provider quota admission could not be verified: %w", err)
	}
	return nil
}

// Remove only identifiable probe artifacts whose lease has expired. This is
// enrollment-time recovery for a hub that stopped before its deferred cleanup.
func cleanupExpiredOrgQuotaProbes(ctx context.Context, cl dynamic.Interface, now time.Time) error {
	quotas := cl.Resource(orgQuotaGVR).Namespace("admin")
	list, err := quotas.List(ctx, metav1.ListOptions{LabelSelector: orgQuotaProbeLabel + "=true"})
	if err != nil {
		return err
	}
	for i := range list.Items {
		quota := &list.Items[i]
		if !strings.HasPrefix(quota.GetName(), orgQuotaProbePrefix) || quota.GetLabels()[orgQuotaProbeLabel] != "true" {
			continue
		}
		expires, err := time.Parse(time.RFC3339, quota.GetAnnotations()[orgQuotaProbeExpiry])
		if err != nil || now.Before(expires) {
			continue
		}
		hard, _, _ := unstructured.NestedStringMap(quota.Object, "spec", "hard")
		if len(hard) != 1 || hard["count/configmaps"] != "0" || quota.GetAnnotations()["experimental.quota.kcp.io/cluster-scoped"] != "true" {
			continue
		}
		uid := quota.GetUID()
		if err := quotas.Delete(ctx, quota.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
