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
// +groupName=providers.kedge.faros.sh

// Package v1alpha1 contains the platform-owner-only API for managing kedge
// providers (extensions). These types are deliberately NOT bound into tenant
// workspaces: the APIExport providers.kedge.faros.sh is bound only in
// root:kedge:providers and is reachable to platform administrators (and the
// hub's catalog controller). Tenants interact with providers via the portal,
// which mediates Enable/Disable through hub APIs — not by directly creating
// ProviderBinding objects.
package v1alpha1
