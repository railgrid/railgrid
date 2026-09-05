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

package mcpaggregate

// MCPIdentityNamespace is the tenant-workspace namespace the per-MCPServer
// ServiceAccount and token Secret live in. The mcpserver controller provisions
// the identity here and the aggregate handler verifies bearers against it, so
// the two sides share one definition.
const MCPIdentityNamespace = "default"

// ServiceAccountName returns the name of the ServiceAccount the mcpserver
// controller provisions for the named MCPServer.
func ServiceAccountName(mcpServerName string) string {
	return mcpServerName + "-mcp"
}

// ServiceAccountUsername returns the Kubernetes username a TokenReview reports
// for the named MCPServer's ServiceAccount token.
func ServiceAccountUsername(mcpServerName string) string {
	return "system:serviceaccount:" + MCPIdentityNamespace + ":" + ServiceAccountName(mcpServerName)
}
