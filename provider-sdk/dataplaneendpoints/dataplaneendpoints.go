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

// Package dataplaneendpoints holds the one endpoint kind a railgrid provider
// owns, and the coordinates everything else names it by.
//
// A kcp CUSTOM SUBRESOURCE is an APIExport spec.resources[] entry of its own,
// named "<resource>/<verb>" in RBAC style, whose storage.virtual.reference
// points at an object that says where the subresource is served. kcp resolves
// that reference in the APIExport's own workspace, reads the object
// unstructured, and takes status.endpoints[] to be a list of kcp Endpoint
// values — {url, shards} — picking one per shard
// (pkg/endpointslice.ListEndpointsFromUnstructured, PickURL).
//
// ANY kind with that status shape works, and ours deliberately is not kcp's
// APIExportEndpointSlice. That one publishes the URL of kcp's own APIExport
// virtual workspace; a railgrid data-plane verb is served by the PROVIDER's
// HTTP server, at the very address the hub already reverse-proxies
// /services/providers/<name>/* to. Reusing kcp's slice would point every verb
// at kcp instead of at the provider.
//
// There is exactly one instance per provider, named after the provider's
// APIExport, so the reference apiexportgen writes into every generated
// subresource entry is derivable from the manifest alone and never needs a
// lookup. provider-sdk/install applies the CRD and writes the URL into
// status.endpoints[0] at bootstrap; nothing reconciles it afterwards.
package dataplaneendpoints

import _ "embed"

const (
	// Group is the API group of the endpoint kind. It is deliberately its own
	// group: providers.railgrid.ai is served by an APIExport bound INTO the
	// provider workspace, and a CRD may not shadow a bound export's group.
	Group = "dataplane.railgrid.ai"
	// Version is the CRD's single served version.
	Version = "v1alpha1"
	// Kind is the endpoint kind kcp's storage.virtual.reference names.
	Kind = "DataPlaneEndpointSlice"
	// Resource is the plural, as the dynamic client needs it.
	Resource = "dataplaneendpointslices"
	// CRDName is metadata.name of the CustomResourceDefinition.
	CRDName = Resource + "." + Group
)

//go:embed crd.yaml
var crdYAML []byte

// CRDYAML returns the CustomResourceDefinition provider-sdk ships, as written.
// install applies it into the provider's own workspace
// (root:railgrid:providers:<name>) before the APIExport that references the
// kind: kcp replicates a referenced object to the shards through a
// ClusterCachedResource it creates for it, and an export that points at a kind
// which is not established yet is never replicated at all.
func CRDYAML() []byte {
	return append([]byte(nil), crdYAML...)
}

// SliceName is the deterministic name of the one slice a provider publishes.
//
// The provider's APIExport name is the key. It is unique per workspace, it is
// already the name of the APIExportEndpointSlice install creates, and — the
// point here — apiexportgen knows it from manifest.yaml, so the reference on
// every generated subresource entry can be written without asking a cluster
// anything.
func SliceName(exportName string) string {
	return exportName
}

// Reference renders kcp's storage.virtual.reference for a subresource entry: a
// core/v1 TypedLocalObjectReference, which serializes as {apiGroup, kind, name}
// and resolves within the APIExport's own workspace.
func Reference(exportName string) map[string]any {
	return map[string]any{
		"apiGroup": Group,
		"kind":     Kind,
		"name":     SliceName(exportName),
	}
}
