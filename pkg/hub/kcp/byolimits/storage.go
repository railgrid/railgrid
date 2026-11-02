// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package byolimits

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// ConsumerQuotaRequiredAnnotation is written on protected consumer
	// LogicalClusters by trusted workspace lifecycle code. Admission never
	// derives protection from request headers or the caller identity.
	ConsumerQuotaRequiredAnnotation      = "providers.railgrid.ai/consumer-quota-required"
	ConsumerQuotaRequiredValue           = "true"
	ConsumerQuotaNamespace               = "admin"
	ConsumerQuotaName                    = "railgrid-org-consumer-storage"
	ConsumerQuotaClusterScopedAnnotation = "experimental.quota.kcp.io/cluster-scoped"
)

func consumerQuotaCountKey(gr schema.GroupResource) string {
	key := "count/" + gr.Resource
	if gr.Group != "" {
		key += "." + gr.Group
	}
	return key
}

// safeStatusProof verifies that the named, initialized workspace quota
// actually covers a resource family. It intentionally keys by GroupResource,
// so API version changes share one stored-object budget.
func safeStatusProof(quota *corev1.ResourceQuota, gr schema.GroupResource) error {
	if quota == nil || quota.Name != ConsumerQuotaName || quota.Namespace != ConsumerQuotaNamespace {
		return fmt.Errorf("protected consumer quota is missing")
	}
	if quota.Annotations[ConsumerQuotaClusterScopedAnnotation] != "true" || len(quota.Spec.Scopes) != 0 || quota.Spec.ScopeSelector != nil {
		return fmt.Errorf("protected consumer quota is not cluster-scoped")
	}
	key := consumerQuotaCountKey(gr)
	resourceName := corev1.ResourceName(key)
	specHard, ok := quota.Spec.Hard[resourceName]
	if !ok || specHard.Sign() <= 0 {
		return fmt.Errorf("protected consumer quota does not cover %s", gr.String())
	}
	statusHard, ok := quota.Status.Hard[resourceName]
	if !ok || statusHard.Sign() <= 0 || statusHard.Cmp(specHard) != 0 {
		return fmt.Errorf("protected consumer quota hard status is not initialized for %s", gr.String())
	}
	used, ok := quota.Status.Used[resourceName]
	if !ok || used.Sign() < 0 {
		return fmt.Errorf("protected consumer quota usage is not initialized for %s", gr.String())
	}
	if used.Cmp(statusHard) > 0 {
		return fmt.Errorf("protected consumer quota usage exceeds hard limit for %s", gr.String())
	}
	return nil
}
