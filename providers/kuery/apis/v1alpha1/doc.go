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

// +kubebuilder:object:generate=true
// +groupName=kuery.providers.railgrid.ai

// Package v1alpha1 defines kuery's API group. It holds two kinds with very
// different audiences, and the difference matters:
//
//   - SavedView is the provider's ONE exported kind. It is the named resource
//     the query verb hangs off — POST
//     /dataplane/clusters/{id}/savedviews/{name}/run — so a tenant's query is
//     authorized as a verb on an object they can see, not as a flat REST route
//     whose tenant comes from a header. Its schema ships in
//     deploy/chart/files/schemas/ and is attached to the APIExport by `init`.
//
//   - Engagement is PROVIDER-PRIVATE. It lives only in kuery's own workspace,
//     is installed as a plain CRD by `init` (see the install package), and is
//     deliberately absent from the APIExport and from the chart's schemas
//     directory: tenants can neither bind nor claim it. It replaces the tenant
//     label, active/stale status and lease owner that used to be columns on the
//     SQL clusters table.
//
// Nothing here may be added to deploy/chart/files/schemas/ except SavedView —
// `init` applies that whole directory to the APIExport.
package v1alpha1
