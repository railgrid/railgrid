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

// Package providers backs the hub's pluggable-provider extension surface:
// an in-memory routing table, reverse proxies for /ui/providers/{name}/*
// and /services/providers/{name}/*, and the controller that keeps the
// table in sync with ProviderCatalogEntry resources.
package providers

import (
	"fmt"
	"io/fs"
	"net/url"
	"sync"
	"time"
)

// HeartbeatTTL is how long a provider's last heartbeat is considered fresh.
// After this duration, the sweeper flips the registry record's
// HeartbeatStale flag, which the Ready computation observes.
const HeartbeatTTL = 90 * time.Second

// SweepInterval is how often the sweeper goroutine walks the registry to
// evict stale heartbeats. Should comfortably divide HeartbeatTTL.
const SweepInterval = 30 * time.Second

// Provider is the in-memory record the proxies consult to route a request.
// Fields are nil-able to reflect that UI/backend/VW are independently optional
// in the source ProviderCatalogEntry.
type Provider struct {
	Name             string
	DisplayName      string // human-readable label, surfaced to the portal
	IconURL          string // optional, defaults to /ui/providers/{name}/icon.svg
	Category         string // optional grouping in the portal nav; empty = top-level
	Dependencies     []Dependency
	UIURL            *url.URL   // proxy target for /ui/providers/{name}/*; nil → 404
	BackendURL       *url.URL   // proxy target for /services/providers/{name}/*; nil → 404
	BuiltinRoute     string     // when set, portal renders this Vue route instead of loading /main.js
	Children         []NavChild // sub-nav entries surfaced indented under this provider
	Version          string     // CatalogEntry.spec.version (chart-declared)
	APIExportPath    string     // kcp workspace path hosting the APIExport (e.g. root:kedge:providers:cost)
	APIExportName    string     // APIExport name (e.g. cost.providers.kedge.faros.sh)
	PermissionClaims []PermissionClaim

	// EdgeProxyAccess mirrors CatalogEntry.spec.edgeProxyAccess: on tenant
	// Enable, the hub grants the provider SA the "proxy" verb on edges in
	// the tenant workspace (see pkg/hub/restapi/providers_enable.go).
	EdgeProxyAccess bool
	// WorkspaceCluster is the logical cluster ID of the provider's
	// sub-workspace (Workspace.spec.cluster of root:kedge:providers:{name}).
	// It anchors the qualified RBAC subject the edge-proxy grant binds —
	// the same cluster ID kcp puts in the provider SA's token claims. Set
	// via SetWorkspaceCluster after provisioning; empty until then.
	WorkspaceCluster string

	// LocalUIAssets, when non-nil, is an embedded fs.FS that the UI proxy
	// serves under /ui/providers/{Name}/* instead of forwarding to UIURL.
	// Populated for first-party providers whose Vite-built portal/dist is
	// baked into the hub binary via //go:embed. Third-party providers leave
	// it nil and rely on UIURL.
	LocalUIAssets fs.FS

	// EndpointsValid is true when spec.ui.url/spec.backend.url parsed cleanly
	// and at least one endpoint was declared (or LocalUIAssets is set).
	// The catalog controller sets this; the sweeper does not touch it.
	EndpointsValid bool

	// LastHeartbeat is updated by the POST /api/providers/{name}/heartbeat
	// handler. Zero until the first heartbeat (or for providers that don't
	// heartbeat at all).
	LastHeartbeat time.Time
	// ReportedVersion is the version string the provider pod sent in its
	// most recent heartbeat — may diverge from Version during a chart
	// upgrade.
	ReportedVersion string
	// HeartbeatRequired distinguishes "heartbeats-not-configured" from
	// "stale heartbeat". It flips to true on the first heartbeat received,
	// and stays true. From then on, missing heartbeats mark the provider
	// not Ready.
	HeartbeatRequired bool
	// HeartbeatStale is maintained by the sweeper. When HeartbeatRequired
	// is true and now - LastHeartbeat exceeds HeartbeatTTL, the sweeper
	// flips this to true.
	HeartbeatStale bool
}

// Dependency mirrors CatalogEntry.spec.dependencies so the portal and hub can
// gate provider enablement without coupling callers to CRD types.
type Dependency struct {
	Name string
}

// Ready returns true when the proxy should forward to the provider. The
// catalog controller's URL parse must have succeeded AND, if the provider
// has heartbeated at least once, its most recent heartbeat must be fresh.
func (p Provider) Ready() bool {
	if !p.EndpointsValid {
		return false
	}
	if p.HeartbeatRequired && p.HeartbeatStale {
		return false
	}
	return true
}

// PermissionClaim mirrors CatalogEntry.spec.apiExport.permissionClaims so the
// portal can render the Enable confirmation dialog without coupling to the
// CRD types.
type PermissionClaim struct {
	Group        string
	Resource     string
	Verbs        []string
	TenantScoped bool
}

// NavChild mirrors CatalogEntry.spec.ui.children — a single sub-nav
// entry the portal renders indented under its parent provider.
type NavChild struct {
	DisplayName  string
	BuiltinRoute string
}

// Registry is the hub's source of truth for provider routing. It is updated
// by the catalog controller on Watch events and read by the proxies on each
// request. Reads vastly outnumber writes; an RWMutex is sufficient.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]*Provider
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]*Provider{}}
}

// Get returns a copy of the Provider record (or false if unknown). A copy is
// returned so callers can safely inspect fields without holding the lock.
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byName[name]
	if !ok {
		return Provider{}, false
	}
	return *p, true
}

// List returns a snapshot of all registered providers.
func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.byName))
	for _, p := range r.byName {
		out = append(out, *p)
	}
	return out
}

// Upsert replaces the spec-derived fields of the registry record for p.Name
// (or inserts a new record). Heartbeat-tracked fields (LastHeartbeat,
// ReportedVersion, HeartbeatRequired, HeartbeatStale) are preserved across
// upserts so reconcile churn doesn't lose a provider's liveness state.
func (r *Registry) Upsert(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.byName[p.Name]; ok {
		p.LastHeartbeat = existing.LastHeartbeat
		p.ReportedVersion = existing.ReportedVersion
		p.HeartbeatRequired = existing.HeartbeatRequired
		p.HeartbeatStale = existing.HeartbeatStale
		if p.WorkspaceCluster == "" {
			// Provisioning sets this after the Upsert in the same reconcile;
			// don't lose it on the next reconcile's fresh Provider value.
			p.WorkspaceCluster = existing.WorkspaceCluster
		}
	}
	cp := p
	r.byName[p.Name] = &cp
}

// SetWorkspaceCluster records the logical cluster ID of the provider's
// sub-workspace. Called by the catalog controller once provisioning has
// resolved it (the workspace must be Ready before spec.cluster is set).
// Returns false if the name is not in the registry.
func (r *Registry) SetWorkspaceCluster(name, cluster string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byName[name]
	if !ok {
		return false
	}
	p.WorkspaceCluster = cluster
	return true
}

// Heartbeat records a heartbeat for a known provider. Returns false if the
// name is not in the registry (caller should reject with 404). reportedVersion
// is optional; pass empty to leave the field untouched.
func (r *Registry) Heartbeat(name, reportedVersion string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byName[name]
	if !ok {
		return false
	}
	p.LastHeartbeat = now
	p.HeartbeatRequired = true
	p.HeartbeatStale = false
	if reportedVersion != "" {
		p.ReportedVersion = reportedVersion
	}
	return true
}

// SweepStale walks the registry and marks providers whose last heartbeat is
// older than ttl as stale. Providers that have never heartbeated
// (HeartbeatRequired=false) are left alone — they're treated as "doesn't
// heartbeat" not "missed a beat". Returns the number of records flipped.
func (r *Registry) SweepStale(now time.Time, ttl time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	flipped := 0
	for _, p := range r.byName {
		if !p.HeartbeatRequired {
			continue
		}
		stale := now.Sub(p.LastHeartbeat) > ttl
		if stale != p.HeartbeatStale {
			p.HeartbeatStale = stale
			flipped++
		}
	}
	return flipped
}

// Delete removes the record for name. Returns true if anything was removed.
func (r *Registry) Delete(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.byName[name]
	delete(r.byName, name)
	return ok
}

// ParseURL is a small helper for the controller that converts a spec string
// into a *url.URL with the requirements the proxies rely on (non-empty host,
// absolute).
func ParseURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("url %q must be absolute (scheme + host)", raw)
	}
	return u, nil
}
