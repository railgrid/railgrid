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

package kcp

import "embed"

// RootWorkspaceFS contains the kedge workspace definition applied to the root workspace.
//
//go:embed workspace-kedge.yaml
var RootWorkspaceFS embed.FS

// KedgeWorkspaceFS contains workspace definitions for children of root:kedge.
//
//go:embed workspace-providers.yaml workspace-tenants.yaml workspace-users.yaml
var KedgeWorkspaceFS embed.FS

// ProvidersFS contains APIResourceSchemas and APIExport applied to root:kedge:providers.
//
//go:embed apiresourceschema-*.yaml apiexport-*.yaml
var ProvidersFS embed.FS
