/*
Copyright 2026 The Railgrid Authors.

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

package install

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/railgrid/provider-sdk/dataplaneendpoints"
)

var (
	crdGVR = schema.GroupVersionResource{
		Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
	}
	dataPlaneEndpointSliceGVR = schema.GroupVersionResource{
		Group:    dataplaneendpoints.Group,
		Version:  dataplaneendpoints.Version,
		Resource: dataplaneendpoints.Resource,
	}
)

// ExportDeclaresSubresources reports whether the generated APIExport carries at
// least one custom subresource entry — a spec.resources[] entry named
// "<resource>/<verb>". Those are the only entries that resolve through the
// endpoint object, so a provider that declares none needs neither the CRD nor
// the slice, and bootstrap does not ask it for a URL it would never publish.
func ExportDeclaresSubresources(export *unstructured.Unstructured) bool {
	resources, _, err := unstructured.NestedSlice(export.Object, "spec", "resources")
	if err != nil {
		return false
	}
	for _, resource := range resources {
		entry, ok := resource.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := entry["name"].(string); isSubresourceName(name) {
			return true
		}
	}
	return false
}

// isSubresourceName reports whether a spec.resources[].name names a custom
// subresource. kcp's own rule: the "/" is what makes it one.
func isSubresourceName(name string) bool {
	for i := range name {
		if name[i] == '/' {
			return true
		}
	}
	return false
}

// ApplyDataPlaneEndpointCRD applies the CustomResourceDefinition for the
// endpoint kind provider-sdk owns, and waits for it to be Established.
//
// The wait is not politeness. kcp gives an APIExport that references an object
// a ClusterCachedResource for the referenced KIND, so that the object reaches
// every shard; a reference to a kind that is not established yet is never
// replicated, and the subresource is then routed nowhere with no error
// anywhere. So the CRD is established, the slice written, and only then is the
// APIExport that points at them applied.
func ApplyDataPlaneEndpointCRD(ctx context.Context, cl dynamic.Interface) error {
	crd := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(dataplaneendpoints.CRDYAML(), &crd.Object); err != nil {
		return fmt.Errorf("parsing embedded %s: %w", dataplaneendpoints.CRDName, err)
	}
	if err := applyUnstructured(ctx, cl, crdGVR, crd); err != nil {
		return fmt.Errorf("applying CustomResourceDefinition %s: %w", dataplaneendpoints.CRDName, err)
	}
	return waitForCRDEstablished(ctx, cl, dataplaneendpoints.CRDName)
}

// waitForCRDEstablished polls until the CRD reports Established, which is when
// its kind can actually be created.
func waitForCRDEstablished(ctx context.Context, cl dynamic.Interface, name string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		crd, err := cl.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		// A just-created CRD serialises status.conditions as an explicit null
		// until the establishing controller fills it; NestedSlice reports that
		// as a type error, and it only means "not yet".
		raw, found, err := unstructured.NestedFieldNoCopy(crd.Object, "status", "conditions")
		if err != nil {
			return false, err
		}
		if !found || raw == nil {
			return false, nil
		}
		conditions, ok := raw.([]any)
		if !ok {
			return false, fmt.Errorf("CustomResourceDefinition %s: .status.conditions is %T, expected a list", name, raw)
		}
		for _, condition := range conditions {
			entry, ok := condition.(map[string]any)
			if !ok {
				continue
			}
			if entry["type"] == "Established" && entry["status"] == "True" {
				return true, nil
			}
		}
		return false, nil
	})
}

// EnsureDataPlaneEndpointSlice create-or-updates the one DataPlaneEndpointSlice
// a provider publishes, and writes baseURL into status.endpoints[0].url.
//
// That URL is the address kcp will reverse-proxy a custom subresource request
// to, with the path intact, so it is the SAME address the hub already proxies
// /services/providers/<name>/* to: the provider's own HTTP server. Nothing
// reconciles the object afterwards — it changes when the provider is
// reinstalled, which is when init runs.
//
// shards.matchAll is set because one railgrid provider serves the whole
// installation from one address. Without it kcp adopts a lone URL only when it
// already carries the serving shard's own prefix (pkg/endpointslice.PickURL),
// on the grounds that an unqualified URL reads the same whether it is
// installation-wide, another shard's, or stale.
func EnsureDataPlaneEndpointSlice(ctx context.Context, cl dynamic.Interface, exportName, baseURL string) error {
	if exportName == "" {
		return fmt.Errorf("DataPlaneEndpointSlice: export name is required")
	}
	if baseURL == "" {
		return fmt.Errorf("DataPlaneEndpointSlice: no data-plane base URL (set Options.DataPlaneURL, or point Options.CatalogEntryFile at a CatalogEntry with spec.serving.backend.url)")
	}
	name := dataplaneendpoints.SliceName(exportName)
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": dataplaneendpoints.Group + "/" + dataplaneendpoints.Version,
		"kind":       dataplaneendpoints.Kind,
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"apiExport": exportName},
	}}
	status := map[string]any{"endpoints": []any{map[string]any{
		"url":    baseURL,
		"shards": map[string]any{"matchAll": true},
	}}}

	existing, err := cl.Resource(dataPlaneEndpointSliceGVR).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		created, err := cl.Resource(dataPlaneEndpointSliceGVR).Create(ctx, desired, metav1.CreateOptions{})
		if err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("creating %s %s: %w", dataplaneendpoints.Kind, name, err)
			}
			if created, err = cl.Resource(dataPlaneEndpointSliceGVR).Get(ctx, name, metav1.GetOptions{}); err != nil {
				return fmt.Errorf("getting %s %s: %w", dataplaneendpoints.Kind, name, err)
			}
		}
		existing = created
	case err != nil:
		return fmt.Errorf("getting %s %s: %w", dataplaneendpoints.Kind, name, err)
	default:
		if !reflect.DeepEqual(existing.Object["spec"], desired.Object["spec"]) {
			desired.SetResourceVersion(existing.GetResourceVersion())
			updated, err := cl.Resource(dataPlaneEndpointSliceGVR).Update(ctx, desired, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("updating %s %s: %w", dataplaneendpoints.Kind, name, err)
			}
			existing = updated
		}
	}

	// status is a subresource on this CRD, so a create never carries it and a
	// spec update never clobbers it: it is always written on its own.
	if reflect.DeepEqual(existing.Object["status"], status) {
		return nil
	}
	existing.Object["status"] = status
	if _, err := cl.Resource(dataPlaneEndpointSliceGVR).UpdateStatus(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("publishing %s %s endpoint %s: %w", dataplaneendpoints.Kind, name, baseURL, err)
	}
	return nil
}

// CatalogEntryBackendURL reads spec.serving.backend.url out of a CatalogEntry
// file.
//
// This is where the data-plane URL comes from, and it is deliberately not a new
// flag. spec.serving.backend.url IS the provider's externally reachable base
// address:
// it is what the hub's backend proxy forwards /services/providers/<name>/* to,
// it is already rendered per environment by each chart (the in-cluster Service
// DNS in a cluster, the loopback port in dev), and install already reads and
// applies this very file. A second place to write the same address is a second
// place for it to be wrong.
func CatalogEntryBackendURL(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading CatalogEntry file %s: %w", path, err)
	}
	var entry struct {
		Spec struct {
			Serving struct {
				Backend struct {
					URL string `json:"url"`
				} `json:"backend"`
			} `json:"serving"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &entry); err != nil {
		return "", fmt.Errorf("parsing CatalogEntry file %s: %w", path, err)
	}
	return entry.Spec.Serving.Backend.URL, nil
}
