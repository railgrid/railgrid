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

// +k8s:deepcopy-gen=package,register
// +groupName=edges.kedge.faros.sh

// Package v1alpha1 holds the edges provider's two connectable kinds:
// KubernetesCluster (a managed Kubernetes cluster) and LinuxServer (a bare-metal
// / VM Linux host), both reachable through the hub over the agent's reverse
// tunnel. Each Status embeds the SDK's edgeapi.ConnectionStatus so the SDK
// tunnel + controllers manage connection state generically. Both live in one
// group (edges.kedge.faros.sh) and one APIExport.
package v1alpha1
