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

// Package aggregatemcp serves the unified MCPServer endpoint that fuses
// kubernetes-mcp-server (kube-type edges) and the in-tree linuxmcp toolsets
// (server-type edges) behind a single Model Context Protocol HTTP server.
//
// Why this exists: AI clients (Claude, etc.) deal poorly with N separate MCP
// server entries — they cannot enumerate them, can't reason across them, and
// each one only exposes a partial view.  An aggregate server presents one
// endpoint, one tools/list, plus a built-in `list_targets` tool the model
// can call to discover what edges are reachable before invoking anything
// else.
//
// Internals:
//   - We instantiate a kubernetes-mcp-server `Server` purely as a *tool
//     factory* — we never serve from it.  Instead we enumerate its toolsets,
//     convert each tool to a go-sdk tool via the upstream's public helper
//     (`ServerToolToGoSdkTool`), and register the result on our own
//     `mcp.Server` from the model context protocol go-sdk.
//   - We then register the linuxmcp toolsets on the *same* `mcp.Server` via
//     the existing in-tree toolset registry.
//   - Finally we add the `list_targets` tool which is a thin enumerator over
//     the resolved kube + server edge sets.
package aggregatemcp
