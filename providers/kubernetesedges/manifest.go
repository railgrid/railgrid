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

// Package kubernetesedges is the first-party provider that surfaces
// connected Kubernetes cluster edges in the portal and contributes a
// "kubernetes" MCP tool family to the aggregate MCPServer endpoint.
//
// The provider used to ship its own dedicated /services/mcp/.../
// kubernetesmcps/{name}/mcp endpoint (with KubernetesMCP CRD +
// reconciler + virtual-workspace handler); that surface has been
// collapsed into the single MCPServer aggregator. The kube tools are
// now registered via the providers/mcp/aggregate registry by the
// blank-imported kubernetesedges/mcp subpackage at init().
//
// Imported for its side effects (RegisterBuiltin + ToolFamily
// registration) from cmd/kedge-hub/main.go.
package kubernetesedges

import (
	"github.com/faroshq/faros-kedge/pkg/hub/providers"

	// Side-effect import: kubernetesedges/mcp registers the kubernetes
	// ToolFamily with providers/mcp/aggregate at init() so the MCP
	// aggregator picks up this provider's kube tools without explicit
	// wiring in server.go.
	_ "github.com/faroshq/faros-kedge/providers/kubernetesedges/mcp"
)

func init() {
	providers.RegisterBuiltin(providers.BuiltinSpec{
		Name:        "kubernetes-edges",
		DisplayName: "Kubernetes",
		Description: "Manage connected Kubernetes cluster edges.",
		Category:    "Edges",
		// No BuiltinRoute — the portal loads this provider through
		// ProviderFrame (the third-party path) which fetches
		// /ui/providers/kubernetes-edges/main.js. That script defines a
		// <kedge-provider-kubernetes-edges> custom element rendered
		// inline; assets are served from the embedded portal/dist below.
		//
		// Workloads is conceptually a feature OF Kubernetes edges, so it
		// nests under this provider in the side nav. With the custom-
		// element rewrite the child's path becomes an internal sub-route
		// (/providers/kubernetes-edges/workloads); the portal store
		// composes that URL from the parent's `to` + the child's
		// builtinRoute.
		Children: []providers.BuiltinChild{
			{DisplayName: "Workloads", BuiltinRoute: "workloads"},
		},

		// No VirtualWorkspaceMount — the dedicated KubernetesMCP endpoint
		// was removed. Kube MCP tools live on the aggregate MCPServer
		// endpoint, contributed by the side-effect import above.

		// Embedded Vite-built micro-frontend served under
		// /ui/providers/kubernetes-edges/* by the hub's UI proxy.
		LocalUIAssets: localUIAssets(),
	})
}
