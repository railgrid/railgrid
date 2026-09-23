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

package tunnel

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/railgrid/provider-sdk/dataplane"
)

// The two grammar roots this provider serves, both from provider-sdk so there
// is one spelling of each in the tree:
//
//	(a) data-plane verb  /dataplane/clusters/{id}/{resource}/{name}/{verb}[/{tail}]
//	(f) agent tunnel     /agent/clusters/{id}/{resource}/{name}/proxy
//
// The consumer plane deliberately sits on the SHARED root rather than a
// provider-private "/edgeproxy": provider-sdk/serve mounts class (a) at
// /dataplane/ and refuses anything else, and a provider with its own root is
// exactly the dialect docs/provider-contract-review.md §3.5 records. The old
// ".../clusters/{id}/apis/edges.railgrid.ai/v1alpha1/{resource}/..." shape is
// gone, not aliased: dataplane.ParsePath refuses "apis" in the resource
// position outright.
const (
	// DataPlaneRoot is class (a).
	DataPlaneRoot = dataplane.DataplaneRoot
	// AgentRoot is class (f).
	AgentRoot = "agent"
)

// The verbs this provider serves, and the only ones it will gate. This table
// is the code half of spec.dataPlane.verbs in manifest.yaml: a verb the
// manifest declares but this table omits is served by nobody, and a verb here
// that the manifest omits cannot have a cross-provider capability minted for
// it (docs/provider-connectivity-contract.md §"Scoped identities", clause C).
// Keep the two in lockstep.
const (
	// VerbK8s proxies the edge's Kubernetes API through its tunnel.
	VerbK8s = "k8s"
	// VerbSSH opens an SSH session to the edge through its tunnel.
	VerbSSH = "ssh"
	// VerbMCP reaches the edge's (or a published Service's) MCP endpoint.
	VerbMCP = "mcp"
	// VerbProxy proxies HTTP to the edge or to a Service published from it.
	VerbProxy = "proxy"
	// VerbTicket mints the short-lived WebSocket ticket a browser presents as
	// a subprotocol, because a browser cannot set Authorization on an upgrade.
	VerbTicket = "ticket"
	// VerbAgentToken re-mints the caller's own agent credential. It is how an
	// edge agent rotates a TTL'd token it cannot renew any other way: the hub
	// identity service authenticates providers, and an agent is not one, so
	// this provider stands in for it behind the ordinary two gates.
	VerbAgentToken = "agent-token"
	// VerbSSHCredentials hands a host agent's SSH credentials to the provider,
	// which writes them into the tenant workspace on the agent's behalf. It
	// replaces the agent holding core-group Secrets and Namespaces access,
	// which the hub's identity policy refuses to mint for anyone.
	VerbSSHCredentials = "ssh-credentials"
	// VerbAddonCredentials exchanges credentials for a runner bound to this edge.
	VerbAddonCredentials = "addon-credentials"
)

// dataPlaneVerbs is the closed {resource} × {verb} matrix. A pair that is not
// in it is a 404 before any gate runs, so an un-served verb can never reach a
// handler and can never be probed for the existence of an object.
// MacOSServer carries agent-token and addon-credentials: it is a Service-only host
// edge with no Kubernetes API and no SSH data plane, so it serves no CONSUMER
// verb — but its agent still has a credential to rotate, and an edge whose
// agent cannot refresh would lock itself out at TTL. Its host-local services
// are reached as "services", like every other edge's.
//
// ssh-credentials is on linuxservers alone for the same reason in reverse:
// MacOSServer has no SSH data plane to hold credentials for, and a
// KubernetesCluster agent reaches its host through the Kubernetes API.
var dataPlaneVerbs = map[string]map[string]bool{
	kubernetesClusterResource: {VerbK8s: true, VerbSSH: true, VerbMCP: true, VerbTicket: true, VerbAgentToken: true},
	linuxServerResource:       {VerbK8s: true, VerbSSH: true, VerbTicket: true, VerbAgentToken: true, VerbSSHCredentials: true, VerbAddonCredentials: true},
	macOSServerResource:       {VerbAgentToken: true, VerbAddonCredentials: true},
	serviceResource:           {VerbProxy: true, VerbMCP: true, VerbTicket: true},
}

// verbServed reports whether this provider serves {resource}/{verb}.
func verbServed(resource, verb string) bool {
	return dataPlaneVerbs[resource][verb]
}

// DataPlaneVerbs returns the served {resource}/{verb} coordinates, sorted by
// neither — callers that need an order impose one. It exists so a test can
// assert the manifest and this table agree.
func DataPlaneVerbs() map[string][]string {
	out := make(map[string][]string, len(dataPlaneVerbs))
	for resource, verbs := range dataPlaneVerbs {
		for verb := range verbs {
			out[resource] = append(out[resource], verb)
		}
	}
	return out
}

// AgentVerb is the one verb class (f) carries. It is a data-plane verb like
// any other — declared in the manifest, gated by the same SAR coordinate — so
// an agent reconnecting with its own ServiceAccount is authorized by
// "create on {resource}/proxy", name-scoped to its own edge.
const AgentVerb = VerbProxy

// edgeProxyPath renders the public consumer-egress path for one verb:
//
//	{base}/clusters/{cluster}/{resource}/{name}/{verb}
//
// base is the provider's public data-plane mount behind the hub backend proxy
// (/services/providers/edges/dataplane). It is the inverse of
// dataplane.ParsePath and is what goes into an edge's or a Service's
// status.URL, so a CLI client swaps in the hub host and lands back here.
func edgeProxyPath(base, cluster, resource, name, verb string) string {
	return fmt.Sprintf("%s/clusters/%s/%s/%s/%s",
		strings.TrimRight(base, "/"), cluster, resource, name, verb)
}

// gateFnType is the signature of the two-gate check; injectable so tests can
// exercise the routing without a kcp server.
type gateFnType func(ctx context.Context, p *Server, token string, req dataplane.Request) (*unstructured.Unstructured, error)

// gateAsCaller runs the contract's two gates for req, as the CALLER, and
// returns the addressed object.
//
//	Gate 1 is a real GET of {resource}/{name} in the caller's workspace with
//	the caller's own bearer. It proves the caller can see the object and
//	yields the object, so the handler pins the spec it acts on to what the
//	caller could read rather than to what the provider can read. An object on
//	its way out is refused: a verb must not run against a deleting edge.
//
//	Gate 2 is a SelfSubjectAccessReview for "create" on the virtual
//	subresource {resource}/{verb}, scoped to the object's name. The hub
//	materializes every data-plane grant as exactly that rule
//	(pkg/hub/serviceaccounts/workload_identity.go), so no other verb string
//	works for a workload identity. This replaces the single wildcard "proxy"
//	verb the edges dialect used for k8s, ssh, service proxy and MCP alike.
//
// Both run through the caller's credential and nothing else, so the provider
// is never a confused deputy on this path: it cannot read or reach anything
// the caller could not have read or reached itself.
func gateAsCaller(ctx context.Context, p *Server, token string, req dataplane.Request) (*unstructured.Unstructured, error) {
	if p.kcpConfig == nil {
		return nil, fmt.Errorf("no kcp config available to build a caller client")
	}
	cfg := p.userClusterConfig(req.ClusterID, token)

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating caller dynamic client: %w", err)
	}
	gvr := schema.GroupVersionResource{Group: p.group, Version: p.version, Resource: req.Resource}
	obj, err := dynClient.Resource(gvr).Get(ctx, req.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: caller cannot get %s/%s: %w", dataplane.ErrDenied, req.Resource, req.Name, err)
	}
	if obj.GetDeletionTimestamp() != nil {
		return nil, fmt.Errorf("%w: %s/%s is being deleted", dataplane.ErrDenied, req.Resource, req.Name)
	}

	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating caller review client: %w", err)
	}
	review, err := k8sClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group:       p.group,
				Version:     p.version,
				Resource:    req.Resource,
				Subresource: req.Verb,
				Name:        req.Name,
				Verb:        dataplane.SSARVerb,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: access review refused: %w", dataplane.ErrDenied, err)
	}
	if !review.Status.Allowed {
		return nil, fmt.Errorf("%w: caller may not %s %s/%s on %q",
			dataplane.ErrDenied, dataplane.SSARVerb, req.Resource, req.Verb, req.Name)
	}
	return obj, nil
}

// gate parses nothing and assumes req came from dataplane.ParseRequest. It
// refuses a request whose path cluster disagrees with the hub-injected
// X-Railgrid-Cluster before either gate runs — the path is authoritative, and
// a disagreement means the request was assembled wrong or tampered with.
func (p *Server) gate(ctx context.Context, r *http.Request, token string, req dataplane.Request) (*unstructured.Unstructured, error) {
	if headerCluster := strings.TrimSpace(r.Header.Get(dataplane.HeaderCluster)); headerCluster != "" && headerCluster != req.ClusterID {
		return nil, fmt.Errorf("%w: path %q, header %q", dataplane.ErrClusterMismatch, req.ClusterID, headerCluster)
	}
	fn := p.gateFn
	if fn == nil {
		fn = gateAsCaller
	}
	return fn(ctx, p, token, req)
}
