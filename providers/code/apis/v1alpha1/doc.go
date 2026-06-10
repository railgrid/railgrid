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
// +groupName=code.kedge.faros.sh

// Package v1alpha1 contains the tenant-facing API for the code provider —
// a backend-neutral way to manage source-code repositories across git
// hosting sub-providers (GitHub today; GitLab and others later).
//
// Unlike the infrastructure provider (whose Templates are platform-authored
// and projected read-only to tenants), every kind in this group is
// TENANT-AUTHORED: tenants create Connection, Repository, DeployKey, and
// Collaborator CRs directly in their own workspace via the APIBinding, and
// the provider's controllers reconcile them against the chosen git host.
//
// See docs/code-provider-architecture.md for the full design.
package v1alpha1
