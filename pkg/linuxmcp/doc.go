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

// Package linuxmcp implements the kedge "Linux MCP" server: an MCP-protocol
// front-end that exposes shell-style tools (run_command, read_file, …) which
// execute non-interactively over SSH against one or more server-type Edges.
//
// Architecture (mirrors pkg/virtual/builder/mcp_builder.go for kube-mcp):
//
//   - The hub mounts an HTTP handler at
//     /services/linux-mcp/{cluster}/apis/kedge.faros.sh/v1alpha1/linuxmcps/{name}/mcp
//   - Each request opens a stateless MCP server (modelcontextprotocol/go-sdk's
//     streamable HTTP transport) with a Provider that resolves the set of
//     candidate server-type edges from the LinuxMCP CR's edge selector and the
//     ConnManager's active tunnel set.
//   - Each tool invocation dials the agent's reverse tunnel via the existing
//     ConnManager, opens an SSH session (reusing pkg/util/ssh helpers and the
//     SSH credentials stored on the Edge), runs the command non-interactively,
//     and returns stdout/stderr/exit-code.
//
// The package is deliberately split from the virtual-workspace builder so the
// MCP/toolset logic can be unit-tested without spinning up the full hub.
package linuxmcp
