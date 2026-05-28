/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package aggregatemcp

import (
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/pkg/virtual/builder"
)

// ToolFamily is one set of MCP tools a provider contributes to the
// aggregate endpoint. The aggregator owns the lifecycle (one server per
// request, edge resolution, list_targets); a ToolFamily just plugs in
// tool registration for one edge kind.
//
// Providers register a ToolFamily in init() via RegisterToolFamily.
// The aggregator looks up registered families at handler-build time,
// gives each one its share of the resolved edge list (filtered by
// EdgeType + connected) plus a FamilyContext carrying the rest of
// the per-request inputs, and lets the family call AddTool on srv.
//
// Name uniqueness is enforced; collisions panic at init().
type ToolFamily struct {
	// Name is the family identifier (e.g. "kubernetes", "linux"). Used
	// for AboutDoc.Toolsets keys, list_targets bucketing, and the per-
	// family entry in Config.EdgesByFamily / PerFamilyToolsets.
	Name string

	// EdgeType matches Edge.spec.type. The aggregator partitions the
	// MCPServer's resolved edges by this value and only invokes Register
	// with the matching slice. An empty EdgeType means "no edge filter"
	// (currently unused; future cross-cutting families could use it).
	EdgeType string

	// Register installs this family's tools into srv given the per-
	// request FamilyContext. Called once per MCP request because the
	// aggregator runs stateless servers. Implementations should be
	// idempotent and fast — they're on the hot path.
	Register func(srv *mcp.Server, fctx FamilyContext)
}

// FamilyContext is the per-request bundle each ToolFamily.Register
// receives. Carries everything a provider needs to build its tool
// closures: the live edge subset for this family, auth context, hub
// callback URLs, framework deps (for SSH session opening, etc.), and
// any extension knobs the user set on the MCPServer CR that don't have
// a typed slot here.
type FamilyContext struct {
	// Cluster is the kcp logical cluster name (e.g. "root:kedge:user-…").
	// Tools use it to scope edges-proxy and kcp API calls.
	Cluster string

	// EdgeNames is the family's resolved edge subset — already filtered
	// to ToolFamily.EdgeType AND known-connected. Empty means the
	// family has no edges to operate on; Register should still install
	// its tools so AI clients can see them (with a "no targets" error
	// at invocation time) rather than the toolset silently disappearing.
	EdgeNames []string

	// BearerToken is the caller's authorization. Forwarded to edge
	// proxies so per-edge requests preserve the user's identity.
	BearerToken string

	// HubBaseURL is the internal-or-external URL the family uses when
	// constructing callback URLs (e.g. kube tools call back into
	// edges-proxy via this base).
	HubBaseURL string

	// ReadOnly mirrors MCPServer.spec.readOnly. Families should honor it
	// even though tool annotations exist — defense in depth.
	ReadOnly bool

	// Toolsets is the per-family toolset selection from the CR (e.g.
	// MCPServer.spec.kubernetesToolsets, .linuxToolsets). Empty = family
	// defaults.
	Toolsets []string

	// Deps is the framework dependency bundle. Families that need to
	// open SSH sessions, fetch SSH credentials, or talk to kcp directly
	// reach for these.
	Deps *builder.Deps

	// CallerIdentity is the resolved RBAC identity (e.g. "kedge:<sub>"
	// from the OIDC token). Required for SSHUserMapping=identity flows.
	CallerIdentity string

	// Extras carries free-form CR fields the aggregator doesn't model
	// typedly (e.g. linux's commandTimeoutSeconds, maxOutputBytes).
	// Family Register pulls out what it knows about; unknown keys are
	// ignored.
	Extras map[string]any
}

var (
	familyMu       sync.RWMutex
	familyRegistry []ToolFamily
	familyByName   = map[string]int{}
)

// RegisterToolFamily adds f to the global registry. Called from
// provider init() functions; panics on duplicate names (which would be
// a programmer error). After the hub has bound to a port the registry
// is read-only in practice — registrations all complete during package
// init before the first MCP request lands.
func RegisterToolFamily(f ToolFamily) {
	if f.Name == "" {
		panic("aggregatemcp.RegisterToolFamily: Name is required")
	}
	if f.Register == nil {
		panic(fmt.Sprintf("aggregatemcp.RegisterToolFamily: %q has nil Register", f.Name))
	}
	familyMu.Lock()
	defer familyMu.Unlock()
	if _, dup := familyByName[f.Name]; dup {
		panic(fmt.Sprintf("aggregatemcp.RegisterToolFamily: duplicate name %q", f.Name))
	}
	familyByName[f.Name] = len(familyRegistry)
	familyRegistry = append(familyRegistry, f)
}

// RegisteredFamilies returns a snapshot of every registered family in
// registration order (which == cmd/kedge-hub blank-import order). Used
// by the aggregator's Handler and by tests that need to introspect the
// registry without mutating it.
func RegisteredFamilies() []ToolFamily {
	familyMu.RLock()
	defer familyMu.RUnlock()
	out := make([]ToolFamily, len(familyRegistry))
	copy(out, familyRegistry)
	return out
}
