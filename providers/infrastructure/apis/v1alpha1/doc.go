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
// +groupName=infrastructure.kedge.faros.sh

// Package v1alpha1 contains the platform-facing API for the
// infrastructure provider — a small, backend-neutral catalog system
// that publishes Templates to tenant workspaces and lets tenants
// provision per-template Kubernetes resources without knowing which
// backend (kro today; terraform / cloud later) actually materializes
// them.
//
// Only Template is in this group today. The per-template CRDs
// (e.g. Redis, Postgres) are registered dynamically by the Template
// controller in providers/infrastructure/controller/template; they
// share the group but carry their own kinds.
//
// See docs/infrastructure-architecture.md for the full design.
package v1alpha1
