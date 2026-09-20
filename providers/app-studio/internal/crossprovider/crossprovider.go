/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package crossprovider names App Studio's dependencies the way the tenant's
// own workspace names them.
//
// Which provider serves a dependency is a fact about the TENANT's workspace,
// not about this provider's configuration, and the object that carries it is
// the workspace's APIBinding for the dependency's APIExport. The hub creates
// one binding per enabled provider and names it after that provider
// (pkg/hub/restapi/providers_enable.go), so the binding's own name IS the
// segment /services/providers/{name}/ takes.
//
// The name is therefore a candidate derived from the export, confirmed against
// the binding before anything is built from it — never a constant, and never a
// list of everything the workspace has enabled: a scoped identity may `get` an
// APIBinding it names and may not list them
// (docs/provider-connectivity-contract.md §"Scoped identities", clause D).
package crossprovider

import (
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// InfrastructureAPIExport is the APIExport the infrastructure provider
	// serves. App Studio's dependency on it is declared in manifest.yaml
	// (spec.dependencies); this is the name of the export a workspace binds
	// when it enables that dependency, whichever copy of the provider it is.
	InfrastructureAPIExport = "infrastructure.providers.railgrid.ai"
	// CodeAPIExport is the APIExport the code provider serves.
	CodeAPIExport = "code.providers.railgrid.ai"
)

// APIBindingsGVR is kcp's APIBinding, one per enabled provider in the tenant's
// workspace.
var APIBindingsGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}

// ProviderNameForExport is the candidate binding name for an APIExport: the
// export's first label. It is the convention the hub follows when it enables a
// provider, and it is only ever a candidate — the reader confirms it against
// the binding's own export reference before returning it.
func ProviderNameForExport(exportName string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(exportName), ".")
	return name
}
