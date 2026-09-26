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

package serve

import (
	"net/http"
	"sort"

	"github.com/go-logr/logr"

	"github.com/railgrid/provider-sdk/dataplane"
)

// SubresourcePrefix is where a kcp shard reaches a provider for a custom
// subresource: the endpoint object's URL is this provider's base address, and
// the shard appends /clusters/{id} plus the request's own kube path.
const SubresourcePrefix = "/clusters/"

// SubresourceRoute says how one declared verb is served. The same handler
// answers the hub-proxied grammar and the shard-forwarded kube path; this is
// only the piece of addressing the kube path does not carry, which is whether
// the verb is a catalogued action and, if so, at which version.
type SubresourceRoute struct {
	// Action is true for a catalogued action, served by Options.Actions under
	// /actions/ with a version segment; false for a data-plane verb served by
	// Options.DataPlane.
	Action bool
	// Version is the action's contract version, e.g. "v1". Ignored for a verb.
	Version string
}

// subresourceAdapter is the one parser in front of a provider's verb handlers.
//
// The path a shard sends is
//
//	/clusters/{id}/apis/{group}/{version}/{resource}/{name}/{verb}[/{tail}][?component={component}]
//
// The adapter parses it, refuses a coordinate the provider did not declare,
// restores an action's contract version from the declaration, and dispatches
// to Options.DataPlane or Options.Actions with the URL untouched and the parsed
// route in the request context (dataplane.RouteFrom). Nothing about a verb's
// behaviour lives here.
//
// What does live here is the trust boundary. The caller on this path is not a
// bearer but the identity kcp stamped into requestheader headers after
// authenticating the user itself. The adapter reads it, refuses a request that
// carries none, honours the hop counter, and hands the identity to Gate through
// the request context — the only place that context value is ever set, so
// nothing else in the process can forge one.
type subresourceAdapter struct {
	routes    map[string]SubresourceRoute
	dataPlane http.Handler
	actions   http.Handler
	logger    logr.Logger
}

func (a *subresourceAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := dataplane.CheckHops(r); err != nil {
		http.Error(w, "too many proxy hops", http.StatusLoopDetected)
		return
	}
	route, err := dataplane.ParseSubresourceRequest(r)
	if err != nil {
		dataplane.WriteError(w, err)
		return
	}
	identity, err := dataplane.ProxiedCaller(r)
	if err != nil {
		// Not anonymous, and never the provider's own identity: without a
		// stamped caller there is nobody to authorize.
		http.Error(w, "no caller identity", http.StatusUnauthorized)
		return
	}
	// Who the shard says is asking, before any gate runs. Extra VALUES stay
	// out of the log: kcp forwards its warrant there, and a warrant is a
	// credential-shaped thing. The keys alone say whether one was carried.
	extraKeys := make([]string, 0, len(identity.Extra))
	for k := range identity.Extra {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	a.logger.Info("verb request", "cluster", route.ClusterID, "coordinate", route.Resource+"/"+route.Verb, "name", route.Name, "component", route.Component,
		"user", identity.User, "groups", identity.Groups, "extraKeys", extraKeys)
	declared, ok := a.routes[route.Resource+"/"+route.Verb]
	if !ok {
		// A verb the provider did not declare is not served, even if a handler
		// would have answered it: the declaration is the contract.
		http.NotFound(w, r)
		return
	}
	handler := a.dataPlane
	if declared.Action {
		handler = a.actions
		route.Version = declared.Version
	}
	if handler == nil {
		http.NotFound(w, r)
		return
	}

	ctx := dataplane.WithProxiedIdentity(r.Context(), identity)
	ctx = dataplane.WithRoute(ctx, route)
	forwarded := r.Clone(ctx)
	// The shard's cluster segment is the only truth about which workspace
	// this addresses; the addressing header is made to agree for handlers
	// that still label by it, and no credential travels past this point.
	forwarded.Header.Set(dataplane.HeaderCluster, route.ClusterID)
	forwarded.Header.Del("Authorization")
	handler.ServeHTTP(w, forwarded)
}
