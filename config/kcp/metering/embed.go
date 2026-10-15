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

// Package metering embeds the metering-gated kcp manifests that must NOT be
// swept into config/kcp's always-applied ProvidersFS. The embed directive lives
// in this subdirectory so the FS root contains the yaml files directly —
// confighelpers.Bootstrap does embedFS.ReadDir(".") and skips subdirectories, so
// a parent-package embed of `metering/*.yaml` would apply nothing.
package metering

import "embed"

// CensusFS contains the census.railgrid.ai APIExport (claims-only, carrying a
// read-only tenancy.kcp.io/workspaces claim) and its APIExportEndpointSlice.
// Applied by bootstrapMetering into root:railgrid:system:metering only when
// --enable-metering is set. The __TENANCY_IDENTITY_HASH__ and __METERING_PATH__
// placeholders are substituted at apply time.
//
//go:embed apiexport-census.railgrid.ai.yaml apiexportendpointslice-census.railgrid.ai.yaml
var CensusFS embed.FS
