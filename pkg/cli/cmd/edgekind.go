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

	farosclient "github.com/faroshq/faros/pkg/client"
)

// edgeKindGVRs are the connectable kinds a `faros edge`/`faros agent` command
// may address by name. KubernetesCluster is tried first (the common case).
var edgeKindGVRs = []schema.GroupVersionResource{
	farosclient.KubernetesClusterGVR,
	farosclient.LinuxServerGVR,
	farosclient.MacOSServerGVR,
}

func edgeGVRForKind(kind string) schema.GroupVersionResource {
	switch kind {
	case "LinuxServer":
		return farosclient.LinuxServerGVR
	case "MacOSServer":
		return farosclient.MacOSServerGVR
	default:
		return farosclient.KubernetesClusterGVR
	}
}

// getEdgeByName fetches a connectable resource by name across all connectable
// kinds (KubernetesCluster, LinuxServer, MacOSServer), returning the object and the GVR it was
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
	return nil, schema.GroupVersionResource{}, fmt.Errorf("edge %q not found (searched KubernetesCluster + LinuxServer + MacOSServer)", name)
}

// listAllEdges lists every connectable resource across all kinds, merged.
func listAllEdges(ctx context.Context, dyn dynamic.Interface) ([]unstructured.Unstructured, error) {
	var items []unstructured.Unstructured
	served := 0
	for _, gvr := range edgeKindGVRs {
		list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			// A 404 for the resource means this workspace's edges API does
			// not serve that kind (an older provider without macosservers,
			// say); skip it. Only when no kind is served is edges disabled.
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("listing %s: %w", gvr.Resource, err)
		}
		served++
		// Some dynamic API responses omit per-item TypeMeta. Preserve the GVR
		// that produced each item so callers can still render a MacOSServer or
		// LinuxServer correctly instead of falling back to Kubernetes.
		for i := range list.Items {
			if list.Items[i].GetKind() == "" {
				list.Items[i].SetKind(farosclient.EdgeKindForType(farosclient.EdgeTypeForGVR(gvr)))
			}
			if list.Items[i].GetAPIVersion() == "" {
				list.Items[i].SetAPIVersion(gvr.GroupVersion().String())
			}
		}
		items = append(items, list.Items...)
	}
	if served == 0 {
		return nil, errEdgesNotEnabled
	}
	return items, nil
}

// errEdgesNotEnabled is returned when the edges API is absent from the
// workspace: the provider has not been enabled there.
var errEdgesNotEnabled = fmt.Errorf("the edges provider is not enabled in this workspace (no edges.faros.sh API); enable it in the console's Providers page, then retry")
