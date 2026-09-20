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

package mcpaggregate

// Capability discovery: railgrid://providers/capabilities.
//
// tools/list answers "what can I call right now"; it does not answer "what
// does this platform declare it can do". The second question is the contract
// question, and its answer is the validated provider registry — the same
// CatalogEntry declarations the hub already admits, validates and enforces
// against (spec.actions and spec.dataPlane.verbs). This resource serves that
// declaration, per Ready provider visible to the caller's Org.
//
// What it is NOT: a directory of endpoints. It carries no provider URL, no
// data-plane path, no credential and no transport handle of any kind. A
// declared coordinate is an identifier the hub owns; reaching it still goes
// through the federated tool, the action transport or the data-plane route,
// and is still authorized there. Publishing a coordinate grants nothing.
//
// The resource and the tool list are joined by _meta: a federated tool that
// backs a declared coordinate carries _meta.railgrid.action or
// _meta.railgrid.verb naming it (see toolCoordinateMeta), so a model that
// read the resource can tell which tool implements which declaration without
// guessing from names.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CapabilitiesResourceURI is the MCP resource URI the declared capability
// document is served at.
const CapabilitiesResourceURI = "railgrid://providers/capabilities"

// DeclaredBoundResource is the provider-owned resource an action is bound to,
// as declared on the CatalogEntry. It is a coordinate, not an address.
type DeclaredBoundResource struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Resource   string `json:"resource,omitempty"`
}

// DeclaredConsent mirrors the action's declared consent requirement so a
// client can tell, before invoking anything, whether a human has to approve.
type DeclaredConsent struct {
	Required bool   `json:"required"`
	Prompt   string `json:"prompt,omitempty"`
	Scope    string `json:"scope,omitempty"`
}

// DeclaredAction is one validated CatalogEntry action, projected for
// discovery. Schemas themselves are not carried — the digest identifies the
// contract, and the tool that backs the action carries the live input schema.
type DeclaredAction struct {
	// ID is the canonical coordinate, "<name>/<version>" (e.g. branches/v1).
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Version       string                `json:"version"`
	DisplayName   string                `json:"displayName,omitempty"`
	Description   string                `json:"description,omitempty"`
	BoundResource DeclaredBoundResource `json:"boundResource"`
	ReadOnly      bool                  `json:"readOnly"`
	Risk          string                `json:"risk,omitempty"`
	Consent       DeclaredConsent       `json:"consent"`
	// SchemaDigest is the catalog-validated digest of the action's
	// input/output schema pair. It is what pins a tool to a contract version.
	SchemaDigest string `json:"schemaDigest,omitempty"`
}

// DeclaredVerb is one validated CatalogEntry data-plane verb, projected for
// discovery. A verb is a coordinate and a transport, not a request/response
// contract, so it carries no schema and no digest.
type DeclaredVerb struct {
	// Coordinate is "<resource>/<verb>" — the same spelling the hub uses for
	// the RBAC subresource and for scoped-identity capabilities.
	Coordinate  string `json:"coordinate"`
	Resource    string `json:"resource"`
	Verb        string `json:"verb"`
	Description string `json:"description,omitempty"`
	Stream      bool   `json:"stream"`
	ReadOnly    bool   `json:"readOnly"`
}

// ProviderCapabilities is one provider's declared surface.
type ProviderCapabilities struct {
	Provider    string           `json:"provider"`
	DisplayName string           `json:"displayName,omitempty"`
	Actions     []DeclaredAction `json:"actions"`
	Verbs       []DeclaredVerb   `json:"verbs"`
}

// capabilitiesDoc is the JSON served at CapabilitiesResourceURI.
type capabilitiesDoc struct {
	Tenant    string `json:"tenant"`
	MCPServer string `json:"mcpServer"`
	// Providers is in enumeration order (provider name), so the document is
	// byte-stable for an unchanged catalog.
	Providers []ProviderCapabilities `json:"providers"`
}

// capabilitiesFor projects the enumerated targets into the discovery
// document. Targets are already the Ready set visible to the verified
// caller's Org (see RegistryEnumerator), so no visibility decision is made
// here; a provider that declares neither an action nor a verb is omitted
// rather than listed as an empty entry.
func capabilitiesFor(cluster, mcpServer string, targets []ProviderTarget) capabilitiesDoc {
	doc := capabilitiesDoc{Tenant: cluster, MCPServer: mcpServer, Providers: []ProviderCapabilities{}}
	for _, t := range targets {
		if len(t.Actions) == 0 && len(t.Verbs) == 0 {
			continue
		}
		actions := append([]DeclaredAction(nil), t.Actions...)
		sort.Slice(actions, func(i, j int) bool { return actions[i].ID < actions[j].ID })
		verbs := append([]DeclaredVerb(nil), t.Verbs...)
		sort.Slice(verbs, func(i, j int) bool { return verbs[i].Coordinate < verbs[j].Coordinate })
		if actions == nil {
			actions = []DeclaredAction{}
		}
		if verbs == nil {
			verbs = []DeclaredVerb{}
		}
		doc.Providers = append(doc.Providers, ProviderCapabilities{
			Provider:    t.Name,
			DisplayName: t.DisplayName,
			Actions:     actions,
			Verbs:       verbs,
		})
	}
	return doc
}

func registerCapabilitiesResource(srv *mcp.Server, doc capabilitiesDoc) {
	srv.AddResource(&mcp.Resource{
		URI:      CapabilitiesResourceURI,
		Name:     "railgrid-provider-capabilities",
		Title:    "Declared provider capabilities",
		MIMEType: "application/json",
		Description: "Structured JSON listing, for every enabled and healthy railgrid provider in this tenant, " +
			"the actions and data-plane verbs it declares in its catalog entry. Coordinates only — no URLs and " +
			"no credentials. Read it to learn what the platform can do; call tools/list to see what is wired up " +
			"right now. A tool that implements a declared coordinate names it in _meta.railgrid.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		payload, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      CapabilitiesResourceURI,
				MIMEType: "application/json",
				Text:     string(payload),
			}},
		}, nil
	})
}

// metaNamespace is the single key every railgrid-owned _meta entry lives
// under, on the aggregate's tools as on the resource.
const metaNamespace = "railgrid"

// toolCoordinateMeta returns the _meta a proxied tool carries, or nil.
//
// A provider names the declared coordinate its tool implements in the tool's
// own _meta, as either
//
//	"_meta": {"railgrid": {"action": "branches/v1"}}
//	"_meta": {"railgrid": {"verb":   "instances/log"}}
//
// That claim is a hint, never trusted as given: it is resolved against the
// coordinates the hub has ADMITTED for that provider from its CatalogEntry,
// and the value written here is the registry's, not the provider's. A tool
// that claims a coordinate its provider does not declare gets no _meta at
// all, so a provider cannot advertise a contract it never published, borrow
// another provider's coordinate, or claim a schema digest it did not
// register. Nothing else from the provider's _meta is forwarded.
func toolCoordinateMeta(p ProviderTarget, t discoveredTool) mcp.Meta {
	actionClaim, verbClaim := railgridToolClaim(t.Meta)
	if actionClaim != "" {
		if a, ok := p.declaredAction(actionClaim); ok {
			return mcp.Meta{metaNamespace: map[string]any{
				"provider": p.Name,
				"action":   a,
			}}
		}
	}
	if verbClaim != "" {
		if v, ok := p.declaredVerb(verbClaim); ok {
			return mcp.Meta{metaNamespace: map[string]any{
				"provider": p.Name,
				"verb":     v,
			}}
		}
	}
	return nil
}

// railgridToolClaim reads the provider's coordinate claim out of a discovered
// tool's _meta. Anything malformed is simply no claim.
func railgridToolClaim(meta map[string]any) (action, verb string) {
	ns, ok := meta[metaNamespace].(map[string]any)
	if !ok {
		return "", ""
	}
	if s, ok := ns["action"].(string); ok {
		action = strings.TrimSpace(s)
	}
	if s, ok := ns["verb"].(string); ok {
		verb = strings.TrimSpace(s)
	}
	return action, verb
}

// declaredAction resolves an action ID against what this provider declares.
func (p ProviderTarget) declaredAction(id string) (DeclaredAction, bool) {
	for _, a := range p.Actions {
		if a.ID == id {
			return a, true
		}
	}
	return DeclaredAction{}, false
}

// declaredVerb resolves a "<resource>/<verb>" coordinate against what this
// provider declares.
func (p ProviderTarget) declaredVerb(coordinate string) (DeclaredVerb, bool) {
	for _, v := range p.Verbs {
		if v.Coordinate == coordinate {
			return v, true
		}
	}
	return DeclaredVerb{}, false
}
