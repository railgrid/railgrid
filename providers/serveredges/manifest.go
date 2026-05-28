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

// Package serveredges is the first-party provider that surfaces Linux
// server edges connected over SSH in the portal and contributes a
// "linux" MCP tool family to the aggregate MCPServer endpoint.
//
// The provider used to ship its own dedicated /services/linux-mcp/
// .../linuxmcps/{name}/mcp endpoint (with LinuxMCP CRD + reconciler +
// virtual-workspace handler); that surface has been collapsed into
// the single MCPServer aggregator. The linux tools (core, diag, net,
// pkg, systemd) are now registered via the providers/mcp/aggregate
// registry by the blank-imported serveredges/mcp subpackage at init().
//
// Imported for its side effects (RegisterBuiltin + ToolFamily
// registration) from cmd/kedge-hub/main.go.
package serveredges

import (
	"github.com/faroshq/faros-kedge/pkg/hub/providers"

	// Side-effect import: serveredges/mcp registers the linux ToolFamily
	// with providers/mcp/aggregate at init() so the MCP aggregator picks
	// up linuxmcp tools without explicit wiring in server.go.
	_ "github.com/faroshq/faros-kedge/providers/serveredges/mcp"
)

func init() {
	providers.RegisterBuiltin(providers.BuiltinSpec{
		Name:        "server-edges",
		DisplayName: "Servers",
		Description: "Manage Linux server edges connected over SSH.",
		Category:    "Edges",
		// No BuiltinRoute — the portal loads this provider through
		// ProviderFrame which fetches /ui/providers/server-edges/main.js.
		// The script defines <kedge-provider-server-edges>; rendered
		// inline by the host portal in light DOM.

		// No VirtualWorkspaceMount — the dedicated LinuxMCP endpoint was
		// removed. Linux MCP tools live on the aggregate MCPServer
		// endpoint, contributed by the side-effect import above.

		LocalUIAssets: localUIAssets(),
	})
}
