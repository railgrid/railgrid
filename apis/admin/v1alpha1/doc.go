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
// +groupName=admin.kedge.faros.sh

// Package v1alpha1 contains the platform-owner-only admin API. It holds the
// Provider type, which declaratively provisions a provider's kcp sub-workspace
// + ServiceAccount + kubeconfig Secret.
//
// The group name carries "admin" deliberately: the admin.kedge.faros.sh
// APIExport is bound ONLY in root:kedge:providers (admin/hub). This is distinct
// from providers.kedge.faros.sh, which serves CatalogEntry and IS bound into
// provider sub-workspaces so providers can self-register their catalog entry.
// Because Provider lives behind the admin-only export, a provider running in
// its own sub-workspace (with cluster-admin over that workspace) cannot create
// a Provider and thereby bootstrap sibling provider workspaces.
package v1alpha1
