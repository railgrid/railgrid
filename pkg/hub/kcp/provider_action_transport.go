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

package kcp

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/kcp-dev/kcp/pkg/server"
	"github.com/kcp-dev/logicalcluster/v3"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/railgrid/provider-sdk/dataplane"
)

// SetProviderActionTransport installs the Edge hop used ONLY after the shard
// has authenticated and authorized a custom subresource. No public hub route
// accepts caller identity headers. The binding selects the export, not a name
// inferred from the backend hostname (which can repeat across organizations).
func (e *EmbeddedKCP) SetProviderActionTransport(fn func(*http.Request, string, string, string) (*http.Response, error)) {
	e.actionMu.Lock()
	defer e.actionMu.Unlock()
	e.actionTransport = fn
}

type providerActionTransport struct {
	base    http.RoundTripper
	resolve func(*http.Request) (string, string, string, error)
	forward func(*http.Request, string, string, string) (*http.Response, error)
}

func (t *providerActionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	exportPath, exportName, exportCluster, err := t.resolve(r)
	if err != nil {
		return nil, err
	}
	if exportPath == "" {
		return t.base.RoundTrip(r)
	}
	return t.forward(r, exportPath, exportName, exportCluster)
}

func (e *EmbeddedKCP) wrapProviderActions(c *server.Config) {
	if c.VirtualResources == nil {
		return
	}
	extra := c.VirtualResources.Extra
	cfg := extra.VWClientConfig
	old := cfg.WrapTransport
	cfg.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		if old != nil {
			base = old(base)
		}
		return &providerActionTransport{base: base, resolve: func(r *http.Request) (string, string, string, error) {
			if _, err := dataplane.ParseSubresourceRequest(r); err != nil {
				return "", "", "", nil
			}
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(parts) < 8 || parts[2] != "apis" {
				return "", "", "", nil
			}
			bindings, err := extra.APIBindingInformer.Cluster(logicalcluster.Name(parts[1])).Lister().List(labels.Everything())
			if err != nil {
				return "", "", "", err
			}
			for _, binding := range bindings {
				for _, resource := range binding.Status.BoundResources {
					if resource.Group != parts[3] || resource.Resource != parts[5] {
						continue
					}
					ref := binding.Spec.Reference.Export
					if ref == nil {
						return "", "", "", nil
					}
					path := string(ref.Path)
					if !strings.HasPrefix(path, "root:railgrid:tenants:") {
						return "", "", "", nil
					}
					segments := strings.Split(path, ":")
					if len(segments) != 6 || segments[4] != "providers" {
						return "", "", "", nil
					}
					// Paths can be reused after a provider workspace is removed.
					// Carry the native resolved cluster to fence replacements.
					if binding.Status.APIExportClusterName == "" {
						return "", "", "", fmt.Errorf("self-hosted action export identity is missing")
					}
					return path, ref.Name, binding.Status.APIExportClusterName, nil
				}
			}
			return "", "", "", nil
		}, forward: func(r *http.Request, path, name, cluster string) (*http.Response, error) {
			e.actionMu.RLock()
			fn := e.actionTransport
			e.actionMu.RUnlock()
			if fn == nil {
				return nil, fmt.Errorf("self-hosted action transport is not ready")
			}
			return fn(r, path, name, cluster)
		}}
	}
}
