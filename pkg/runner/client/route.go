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

package client

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/railgrid/provider-sdk/dataplane"
)

// The edges Service coordinate is FOREIGN to this package: what a consumer may
// read of the group, and the one verb it may invoke, are declared under the
// consuming provider's manifest spec.requires and reach a workspace only once
// an admin accepted them. The literals are spelled here rather than imported
// from providers/edges/apis/v1alpha1 so that pkg/runner, which is also compiled
// into the runner agent, never links a provider's controller API packages.
// They must stay in step with providers/edges/apis/v1alpha1 (GroupName,
// Version, ServiceResource) and with the `proxy` verb in
// providers/edges/manifest.yaml.
const (
	edgesAPIGroup        = "edges.railgrid.ai"
	edgesAPIVersion      = "v1alpha1"
	edgesServiceResource = "services"

	// edgesProxyVerb is the verb edges declares for a published Service. The
	// hub's identity policy admits it only because that declaration exists,
	// and re-checks it on every mint.
	edgesProxyVerb = "proxy"
)

// Host Edge kinds a runner may be enrolled on. A runner is a process on a
// machine, so a Service backed by a KubernetesCluster edge can never be one:
// refusing the kind here turns a mis-enrollment into a startup error instead of
// a dispatch that reaches something that is not a runner at all.
const (
	EdgeKindLinuxServer = "LinuxServer"
	EdgeKindMacOSServer = "MacOSServer"
)

// proxyPath renders the kube path of the Service's proxy verb.
//
// A verb is a kcp CUSTOM SUBRESOURCE and is reached only as one, so the
// coordinate is
//
//	/clusters/{cluster}/apis/edges.railgrid.ai/v1alpha1/services/{name}/proxy
//
// relative to whichever front door the caller holds. It is rendered by
// dataplane.SubresourcePath — the inverse of the parser the owning provider
// runs — and never string-built, so the grammar lives in exactly one package
// and a coordinate that would not parse back to itself is refused here rather
// than 404ing somewhere downstream. No provider NAME appears in it: kcp
// resolves the group through this workspace's own APIBinding, which is what
// keeps an organization that self-hosts edges working.
func proxyPath(cluster, service string) (string, error) {
	return dataplane.SubresourcePath(edgesAPIGroup, edgesAPIVersion, dataplane.Request{
		ClusterID: cluster,
		Resource:  edgesServiceResource,
		Name:      service,
		Verb:      edgesProxyVerb,
	})
}

// publishedProxy returns the proxy path for service after checking that edges
// really published it at that coordinate, for this edge, in this workspace.
//
// service is the Service object as an ordinary read returned it. ref pins the
// enrollment: a Service that has been re-pointed at another machine must not
// keep serving an enrolled caller's dispatch, which is the difference between
// "the runner is down" and "the dispatch went to someone else's laptop".
func publishedProxy(ref ServiceRef, service *unstructured.Unstructured) (string, error) {
	if service == nil {
		return "", errors.New("runner client: no Service")
	}
	if service.GetName() != ref.Service {
		return "", errors.New("runner client: the published Service does not match the enrolled service")
	}
	if service.GetDeletionTimestamp() != nil {
		return "", errors.New("runner client: the published Service is being deleted")
	}
	name, _, _ := unstructured.NestedString(service.Object, "spec", "edgeRef", "name")
	kind, _, _ := unstructured.NestedString(service.Object, "spec", "edgeRef", "kind")
	if name != ref.EdgeName || kind != ref.EdgeKind {
		return "", fmt.Errorf("runner client: Service %q no longer belongs to the enrolled machine (edgeRef=%s/%s, enrolled=%s/%s)",
			ref.Service, kind, name, ref.EdgeKind, ref.EdgeName)
	}
	want, err := proxyPath(ref.Cluster, service.GetName())
	if err != nil {
		return "", err
	}
	// status.url is the edges provider's own rendering of the same coordinate
	// (providers/edges/internal/servicectrl/validation_reconciler.go statusURL).
	// Comparing the two answers "is this Service actually reachable through
	// this workspace" from the object rather than from a guess.
	published, _, _ := unstructured.NestedString(service.Object, "status", "url")
	if published != want {
		return "", fmt.Errorf("runner client: Service %q is not published at %s (status.url=%q)", service.GetName(), want, published)
	}
	return want, nil
}
