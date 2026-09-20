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

// Package claimscope holds the one label that narrows a provider's permission
// claims from "every object of this resource in the tenant's workspace" to
// "the objects this provider owns".
//
// The problem it solves is the Secrets side-door
// (docs/cross-provider-simplification.md, mechanism M3 and remediation X-4).
// A claim is per resource, not per name, so a provider that needs to write one
// Secret used to be granted every Secret in every workspace that enabled it —
// including credentials written by the tenant and by other providers. That is
// how a provider came to read another provider's backend credential without
// the owning provider ever being consulted.
//
// The fix has two halves and both are required:
//
//  1. Every Secret a provider writes carries OwnerLabel = its own name. The
//     provider sets it at write time; this package is the single spelling of
//     that key so a manifest, a hub-written APIBinding selector and a Go
//     writer cannot drift.
//  2. The provider's manifest claim carries
//     `selector.matchLabels: {railgrid.ai/owner: <provider>}`, which
//     apiexportgen stamps onto the generated APIExport as defaultSelector and
//     the hub writes onto the accepted claim in each tenant's APIBinding.
//
// kcp then enforces it, on both directions of the APIExport virtual workspace:
//
//   - its permission-claim labeler only stamps the internal
//     `claimed.internal.apis.kcp.io/<export>` marker on objects the accepted
//     claim's selector matches (kcp pkg/permissionclaim, LabelsFor);
//   - the virtual workspace ANDs that marker into every LIST/WATCH and
//     404s a GET on an object without it (virtual-workspace-framework
//     forwardingregistry.WithLabelSelector), so an unlabelled Secret is not
//     merely unreadable, it does not exist as far as the provider is
//     concerned;
//   - its virtual-workspace admission refuses a CREATE/UPDATE/DELETE of an
//     object that does not match the selector, stamping the matchLabels when
//     they are simply absent (kcp pkg/virtual/apiexport/admission).
//
// That last point is why setting the label in Go is belt-and-braces for a
// write that goes through the export: kcp would add it. It is NOT redundant
// for a Secret written any other way — by the tenant through the portal, or by
// the provider's own workspace-admin client — because nothing stamps those,
// and an unlabelled Secret silently drops out of the provider's view.
package claimscope

// OwnerLabel marks the provider that owns an object, by provider name (the
// CatalogEntry's metadata.name). It is the key every railgrid permission-claim
// selector matches on.
const OwnerLabel = "railgrid.ai/owner"

// OwnerLabels returns the label set an object must carry to fall inside
// provider's claim selector. Use it when building an object from scratch.
func OwnerLabels(provider string) map[string]string {
	return map[string]string{OwnerLabel: provider}
}

// WithOwner returns labels with the owner label set to provider, without
// mutating the input (a nil input yields a fresh map). Use it when a caller
// may already have labels to preserve.
func WithOwner(labels map[string]string, provider string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	for key, value := range labels {
		out[key] = value
	}
	out[OwnerLabel] = provider
	return out
}
