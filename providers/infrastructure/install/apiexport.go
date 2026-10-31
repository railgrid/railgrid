/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
)

// APIExportName matches CatalogEntry.spec.export.name.
const APIExportName = "infrastructure.providers.railgrid.ai"

func ptrTo[T any](v T) *T { return &v }

// TemplateVirtualStorage supplies the catalog projection to SDK bootstrap before
// its first export write, so repeated init/operator passes never expose an empty
// tenant-local templates CRD in between two export updates.
func TemplateVirtualStorage() map[schema.GroupResource]corev1.TypedLocalObjectReference {
	return map[schema.GroupResource]corev1.TypedLocalObjectReference{
		{Group: infrav1alpha1.GroupName, Resource: "templates"}: {
			APIGroup: ptrTo("cache.kcp.io"), Kind: "ClusterCachedResourceEndpointSlice", Name: EndpointSliceTemplatesName,
		},
	}
}
