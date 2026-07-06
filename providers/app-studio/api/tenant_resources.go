/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/faroshq/provider-app-studio/tenant"
)

// tenant.Resource descriptors for the workspace resources App Studio accesses
// through asclient.Client.Resource. Kind/Plural drive the GraphQL field names
// (<Kind>Yaml / <Plural>Yaml / delete<Kind>); they must match the CRD's kind
// and its pluralization.
var (
	secretResource         = tenant.Resource{GVR: secretGVR, Kind: "Secret", Plural: "Secrets", Namespaced: true}
	codeConnectionResource = tenant.Resource{GVR: codeConnectionsGVR, Kind: "Connection", Plural: "Connections"}
	codeRepositoryResource = tenant.Resource{GVR: codeRepositoriesGVR, Kind: "Repository", Plural: "Repositories"}
)

// codeResourceFor maps a code-provider GVR to its descriptor, for the
// repository-view getter/lister closures that are keyed by GVR.
func codeResourceFor(gvr schema.GroupVersionResource) tenant.Resource {
	switch gvr {
	case codeRepositoriesGVR:
		return codeRepositoryResource
	case codeConnectionsGVR:
		return codeConnectionResource
	default:
		return tenant.Resource{GVR: gvr, Kind: "", Plural: ""}
	}
}

// providerBindingResource builds a descriptor for a project provider-binding's
// target CR. The kind comes from the binding's ResourceRef; these CRs are
// cluster-scoped in the workspace.
func providerBindingResource(gvr schema.GroupVersionResource, kind string) tenant.Resource {
	return tenant.Resource{GVR: gvr, Kind: kind, Plural: kind + "s"}
}
