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

package plugin

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// A provider's APIExportEndpointSlice gets its virtual-workspace endpoint only
// once the export has a consumer, and kcp re-checks the slice when an
// APIBinding appears by looking it up under the binding's export reference.
// Tenant bindings reference the export by workspace path
// (root:railgrid:providers:<name>), while a slice written with no spec.export.path
// — what current provider charts do, so one chart works in the platform and in
// an org's own workspace — is indexed only under its own cluster ID. The first
// binding therefore never reaches the slice: it keeps "no endpoints", the
// provider never engages a tenant workspace, and nothing it owns reconciles
// (App Studio's Studio, its search/browser/dev instances; agents).
//
// Any later event on the slice makes kcp reconcile it against the bindings it
// now has, and once one endpoint is published it stays while any binding
// exists. So after enabling providers, touch each slice that still has no
// endpoint until one appears.
const (
	devSliceResyncAnnotation = "railgrid.ai/dev-endpoint-resync"
	devSliceResyncTimeout    = 2 * time.Minute
)

var apiExportEndpointSliceGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
}

// resyncProviderEndpointSlices nudges the provider's endpoint slices that
// have no endpoints, retrying until kcp publishes one or the timeout passes.
func (o *DevOptions) resyncProviderEndpointSlices(ctx context.Context, clientset kubernetes.Interface, provider string) error {
	cfg, err := o.providerWorkspaceConfig(ctx, clientset, provider)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	if err := resyncEndpointSlices(ctx, dyn.Resource(apiExportEndpointSliceGVR), 5*time.Second, devSliceResyncTimeout); err != nil {
		return fmt.Errorf("provider %s: %w", provider, err)
	}
	return nil
}

// resyncEndpointSlices touches every slice without endpoints, every interval,
// until all have one or timeout passes.
func resyncEndpointSlices(ctx context.Context, slices dynamic.ResourceInterface, interval, timeout time.Duration) error {
	var pending []string
	return pollUntil(ctx, interval, timeout, func(ctx context.Context) (bool, error) {
		list, err := slices.List(ctx, metav1.ListOptions{})
		if err != nil {
			pending = []string{err.Error()}
			return false, nil
		}
		pending = pending[:0]
		for _, s := range list.Items {
			if sliceHasEndpoints(&s) {
				continue
			}
			pending = append(pending, s.GetName())
			patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, devSliceResyncAnnotation, strconv.FormatInt(time.Now().UnixNano(), 10))
			if _, err := slices.Patch(ctx, s.GetName(), types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
				pending[len(pending)-1] += ": " + err.Error()
			}
		}
		return len(pending) == 0, nil
	}, func() error {
		return fmt.Errorf("endpoint slices still publish no endpoints: %v", pending)
	})
}

func sliceHasEndpoints(s *unstructured.Unstructured) bool {
	endpoints, _, _ := unstructured.NestedSlice(s.Object, "status", "endpoints")
	return len(endpoints) > 0
}

// providerWorkspaceConfig is a client for the provider's own workspace: its
// minted kubeconfig (ServiceAccount token, admin in that workspace), with
// the in-cluster hub address swapped for the host-mapped one so this process
// can use it.
func (o *DevOptions) providerWorkspaceConfig(ctx context.Context, clientset kubernetes.Interface, provider string) (*rest.Config, error) {
	name := "railgrid-" + provider + "-kubeconfig"
	secret, err := clientset.CoreV1().Secrets(devProvidersNS).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading %s/%s: %w", devProvidersNS, name, err)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(secret.Data["kubeconfig"])
	if err != nil {
		return nil, fmt.Errorf("parsing %s/%s: %w", devProvidersNS, name, err)
	}
	host, err := rehostURL(cfg.Host, o.hubLocalURL())
	if err != nil {
		return nil, err
	}
	cfg.Host = host
	cfg.Insecure = true
	cfg.CAData, cfg.CAFile = nil, ""
	return cfg, nil
}

// rehostURL keeps rawURL's path (the /clusters/<id> the hub routes on) and
// takes scheme and host from base.
func rehostURL(rawURL, base string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Scheme, u.Host = b.Scheme, b.Host
	return u.String(), nil
}
