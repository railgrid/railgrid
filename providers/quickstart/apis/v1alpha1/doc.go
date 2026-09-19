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

// +k8s:deepcopy-gen=package,register
// +groupName=quickstart.providers.railgrid.ai

// Package v1alpha1 holds the one tenant-facing kind the quickstart provider
// exports: Greeting.
//
// This is Pillar 1 of the provider contract in its smallest honest form. A
// tenant-visible, persistent resource is a kcp API: Go types here,
// APIResourceSchemas generated from them by `make codegen-quickstart-provider`
// into deploy/chart/files/schemas/, applied together with the
// quickstart.providers.railgrid.ai APIExport by this provider's own `init`
// (provider-sdk/install.Bootstrap). Tenants get the kind by binding that
// export; nothing about a Greeting lives in a database, a PVC or process
// memory.
//
// Copy this package when you start a provider. The shape that matters is: one
// spec field the tenant sets, one status field the reconciler stamps, and a
// Ready condition that says whether the reconciler agrees with the spec.
package v1alpha1
