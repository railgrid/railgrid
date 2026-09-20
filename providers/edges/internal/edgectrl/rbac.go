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

package edgectrl

const (
	rbacControllerName = "edge-rbac"
)

// edgeNamespace and edgeAgentClusterRole are gone with the ServiceAccount,
// ClusterRole, binding and token Secret this provider used to mint per edge:
// an agent's credential is now a hub-minted scoped identity (agentidentity.go)
// and this provider writes nothing into the tenant workspace to create it.
// The tenant namespace that still exists, railgrid-system, holds only the SSH
// credential Secret the provider writes on the agent's behalf after the two
// gates — see internal/tunnel/agentcred.go.
