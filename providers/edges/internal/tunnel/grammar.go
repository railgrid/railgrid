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
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/railgrid/provider-sdk/dataplane"
)

// The two route shapes this provider serves:
//
//	(a) data-plane verb  /clusters/{id}/apis/edges.railgrid.ai/v1alpha1/{resource}/{name}/{verb}[/{tail}]
//	(f) agent tunnel     /agent/clusters/{id}/{resource}/{name}/proxy
//
// A verb is a kcp custom subresource on this provider's APIExport: the caller
// addresses it on the hub's kcp front door like any other kube path, kcp
// authenticates the caller and authorizes the HTTP method as the RBAC verb on
// {resource}/{verb}, and the shard reverse-proxies the request here with the
// caller's identity stamped in requestheader headers. provider-sdk/serve
// parses it (dataplane.RouteFrom) before the handler runs; there is no
// hub-proxied spelling of a verb any more.
//
// The agent tunnel is the one route that still carries a bearer, because an
// agent's connection never passes through kcp: it is terminated here, behind
// the hub backend proxy, and the credential is TokenReview'd by this provider.
const (
	// AgentRoot is class (f).
	AgentRoot = "agent"
)

// The verbs this provider serves, and the only ones it will gate. This table
// is the code half of spec.export.resources[].verbs in manifest.yaml: a verb
// the manifest declares but this table omits is served by nobody, and a verb
// here that the manifest omits is never reached (serve's adapter refuses a
// coordinate the declaration lacks) and cannot have a cross-provider
// capability minted for it (docs/provider-connectivity-contract.md §"Scoped
// identities", clause C). The two are pinned to each other by
// TestDeclaredDataPlaneVerbsMatchWhatIsServed, which reads the manifest and
// compares it with DataPlaneVerbs() below, so they cannot drift silently.
const (
	// VerbK8s proxies the edge's Kubernetes API through its tunnel.
	VerbK8s = "k8s"
	// VerbSSH opens an SSH session to the edge through its tunnel.
	VerbSSH = "ssh"
	// VerbMCP reaches the edge's (or a published Service's) MCP endpoint.
	VerbMCP = "mcp"
	// VerbProxy proxies HTTP to the edge or to a Service published from it.
	VerbProxy = "proxy"
	// VerbAgentToken re-mints the caller's own agent credential. It is how an
	// edge agent rotates a TTL'd token it cannot renew any other way: the hub
	// identity service authenticates providers, and an agent is not one, so
	// this provider stands in for it behind the ordinary gate.
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
//
// addon-credentials is on the two HOST kinds, and only those: an Addon may
// only name a LinuxServer or a MacOSServer (the API's own CEL rule refuses
// KubernetesCluster, and internal/addonctrl refuses it again), so declaring it
// on kubernetesclusters would mint a capability for a coordinate no Addon can
// ever address.
//
// There is no "ticket" verb. A browser cannot set Authorization on a WebSocket
// upgrade, so it presents the bearer as the Kubernetes subprotocol
// (base64url.bearer.authorization.k8s.io.<token>) and kcp authenticates the
// upgrade like any other request; nothing here mints or redeems anything.
var dataPlaneVerbs = map[string]map[string]bool{
	kubernetesClusterResource: {VerbK8s: true, VerbSSH: true, VerbMCP: true, VerbAgentToken: true},
	linuxServerResource:       {VerbK8s: true, VerbSSH: true, VerbAgentToken: true, VerbSSHCredentials: true, VerbAddonCredentials: true},
	macOSServerResource:       {VerbAgentToken: true, VerbAddonCredentials: true},
	serviceResource:           {VerbProxy: true, VerbMCP: true},
}

// verbServed reports whether this provider serves {resource}/{verb}.
func verbServed(resource, verb string) bool {
	return dataPlaneVerbs[resource][verb]
}

// DataPlaneVerbs returns the served {resource}/{verb} coordinates, sorted by
// neither — callers that need an order impose one. It exists so a test can
// assert that this table and the manifest's spec.export.resources[].verbs
// declare the same set.
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

// verbPath renders the hub-relative kube path of one verb on this provider's
// export:
//
//	/clusters/{cluster}/apis/{group}/{version}/{resource}/{name}/{verb}
//
// It is the spelling that goes into an edge's or a Service's status.URL and
// into an agent's credential bundle: relative to the hub, so a CLI client
// swaps in the hub host and lands on the kcp front door, which routes the
// custom subresource back here. dataplane.SubresourcePath is the one renderer
// of the grammar; it refuses a coordinate that would not parse back.
func (p *Server) verbPath(cluster, resource, name, verb string) (string, error) {
	path, err := dataplane.SubresourcePath(p.group, p.version, dataplane.Request{
		ClusterID: cluster, Resource: resource, Name: name, Verb: verb,
	})
	if err != nil {
		return "", fmt.Errorf("rendering %s/%s on %s/%s: %w", resource, verb, cluster, name, err)
	}
	return path, nil
}

// parseAgentPath parses the class (f) route,
//
//	/agent/clusters/{cluster}/{resource}/{name}/proxy
//
// and refuses anything else: a missing or extra segment, an empty or dotted
// one, a cluster that is not a kcp logical-cluster ID (a workspace path never
// is), or a verb other than AgentVerb. The path is matched exactly as sent —
// never cleaned — so ".." and "//" are refused rather than reinterpreted.
func parseAgentPath(path string) (cluster, resource, name string, ok bool) {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segments) != 6 || segments[0] != AgentRoot || segments[1] != "clusters" || segments[5] != AgentVerb {
		return "", "", "", false
	}
	cluster, resource, name = segments[2], segments[3], segments[4]
	if !dataplane.IsClusterID(cluster) {
		return "", "", "", false
	}
	for _, segment := range []string{resource, name} {
		if segment == "" || segment == "." || segment == ".." || len(segment) > 253 {
			return "", "", "", false
		}
	}
	return cluster, resource, name, true
}

// gate runs the contract's gate for req — provider-sdk's dataplane.Gate — and
// returns the addressed object together with a client acting as the provider
// in the tenant workspace.
//
// There is no caller bearer on a verb. kcp authenticated the caller, authorized
// the method on {resource}/{verb} with ordinary RBAC, and stamped the identity
// serve's adapter put on the request context. Gate 1 (visibility of the parent
// object) is a SubjectAccessReview run on the caller's behalf through this
// provider's export virtual workspace, then a read as the provider; an object
// on its way out is refused. Gate 2 (the verb grant) is not repeated: kcp
// decided it before forwarding.
//
// Any further question about the caller — "may they read this second object",
// "may they act as this SSH user" — is dataplane.Authorize with the identity
// dataplane.ProxiedIdentityFrom returns, never a client built from a token.
func (p *Server) gate(ctx context.Context, req dataplane.Request) (*unstructured.Unstructured, dynamic.Interface, error) {
	if p.callers == nil {
		return nil, nil, fmt.Errorf("%w: no provider caller factory (tunnel.Config.Callers) to gate with", dataplane.ErrDenied)
	}
	gvr := schema.GroupVersionResource{Group: p.group, Version: p.version, Resource: req.Resource}
	return dataplane.Gate(ctx, p.callers, gvr, req)
}
