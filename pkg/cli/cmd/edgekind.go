/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

// edgeKindGVRs are the connectable kinds a `kedge edge`/`kedge agent` command
// may address by name. KubernetesCluster is tried first (the common case).
var edgeKindGVRs = []schema.GroupVersionResource{
	kedgeclient.KubernetesClusterGVR,
	kedgeclient.LinuxServerGVR,
}

// getEdgeByName fetches a connectable resource by name across both kinds
// (KubernetesCluster, LinuxServer), returning the object and the GVR it was
// found under. The CLI addresses edges by name; the kind is discovered here.
func getEdgeByName(ctx context.Context, dyn dynamic.Interface, name string) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	for _, gvr := range edgeKindGVRs {
		u, err := dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			return u, gvr, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, gvr, err
		}
	}
	return nil, schema.GroupVersionResource{}, fmt.Errorf("edge %q not found (searched KubernetesCluster + LinuxServer)", name)
}

// listAllEdges lists every connectable resource across both kinds, merged.
func listAllEdges(ctx context.Context, dyn dynamic.Interface) ([]unstructured.Unstructured, error) {
	var items []unstructured.Unstructured
	for _, gvr := range edgeKindGVRs {
		list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", gvr.Resource, err)
		}
		items = append(items, list.Items...)
	}
	return items, nil
}
