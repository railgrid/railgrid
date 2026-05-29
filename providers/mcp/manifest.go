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

// Package mcp is the first-party MCP aggregator provider. The init()
// below registers it with the hub's builtin registry: it declares both
// the catalog entry (display name, category, dependencies) and the
// virtual-workspace HTTP handler the hub mounts at /services/mcpserver/.
//
// The actual code lives in this provider's subpackages:
//   - controllers/  MCPServer reconciler (Edge-type-agnostic)
//   - virtual/      /services/mcpserver/ HTTP handler builder
//   - aggregate/    runtime fusing kubernetes-mcp-server + linuxmcp toolsets
//
// Imported for its side effect (RegisterBuiltin) from cmd/kedge-hub/main.go.
package mcp

import (
	"github.com/faroshq/faros-kedge/pkg/apiurl"
	"github.com/faroshq/faros-kedge/pkg/hub/providers"
	mcpvirtual "github.com/faroshq/faros-kedge/providers/mcp/virtual"
)

func init() {
	providers.RegisterBuiltin(providers.BuiltinSpec{
		Name:        "mcp",
		DisplayName: "MCP",
		Description: "Aggregated Model Context Protocol endpoints exposed by connected edges.",
		Category:    "AI",
		// No BuiltinRoute — the portal loads this provider through
		// ProviderFrame (the third-party path) which fetches
		// /ui/providers/mcp/main.js. That script defines a
		// <kedge-provider-mcp> custom element rendered inline; assets
		// are served from the embedded portal/dist below.
		//
		// No Requires: mcp is a pure aggregator of whatever ToolFamilies
		// register themselves via providers/mcp/aggregate. Any provider
		// (built-in or BYO) can contribute a family; with zero registered
		// the endpoint serves an empty aggregate (list_targets returns
		// nothing) and Build logs a one-time warning.

		VirtualWorkspaceMount:   apiurl.PathPrefixMCPServer,
		VirtualWorkspaceHandler: mcpvirtual.Build,

		// Embedded Vite-built micro-frontend; served by the hub's UI
		// proxy under /ui/providers/mcp/* (see pkg/hub/providers/proxy.go
		// LocalUIAssets branch).
		LocalUIAssets: localUIAssets(),
	})
}
