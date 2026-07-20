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

// Package svccatalog is the single source of truth for edge Service types. Each
// entry declares how to reach the service (port/scheme/host defaults), how it
// authenticates (auth kind + the credential fields the UI collects), how to
// health-check it (probe path + mode, used by the validation reconciler), and —
// for the popular self-hosted apps — the MCP tool bundle exposed for it.
//
// Three consumers share this table so it never drifts:
//   - internal/tunnel builds MCP tools from Definition.Tools and authenticates
//     proxied requests via Apply (auth.go).
//   - internal/servicectrl probes ProbePath with Apply to stamp the Service's
//     Ready / CredentialsValid conditions.
//   - the provider's /catalog HTTP endpoint serves the UI-facing subset (the
//     json-tagged fields) so the portal renders the "add/configure service"
//     form from data instead of a hand-maintained mirror.
package svccatalog

import (
	"net/http"
	"sort"
)

// AuthKind selects how the service's credential authenticates a request. The
// string values are stable (serialized to the UI and, implicitly, matched by
// Apply) — do not rename without updating both.
type AuthKind string

const (
	AuthNone         AuthKind = "none"         // no credential
	AuthBearer       AuthKind = "bearer"       // Authorization: Bearer <token>   (Home Assistant, Grafana)
	AuthAPIKeyHeader AuthKind = "apiKeyHeader" // <AuthParam>: <token>            (*arr X-Api-Key, Plex, UniFi)
	AuthAPIKeyQuery  AuthKind = "apiKeyQuery"  // ?<AuthParam>=<token>
	AuthBasic        AuthKind = "basic"        // Authorization: Basic base64(user:pass)   (AdGuard Home)
	AuthProxmox      AuthKind = "proxmox"      // Authorization: PVEAPIToken=<token>        (Proxmox VE)
	AuthPihole       AuthKind = "pihole"       // POST /api/auth {password} -> X-FTL-SID     (Pi-hole v6)
	AuthQBittorrent  AuthKind = "qbittorrent"  // POST /api/v2/auth/login -> SID cookie
)

// Packing tells the UI (and Apply) how the credential fields combine into the
// single Secret "token" value the provider reads.
type Packing string

const (
	// PackSingle: a single field is stored verbatim as the token.
	PackSingle Packing = "single"
	// PackUserPass: username + password are stored as "username:password".
	PackUserPass Packing = "userpass"
)

// CredentialField is one input the UI collects for a service's credential.
type CredentialField struct {
	// Key is the logical field id ("token", "username", "password", "apiKey").
	Key string `json:"key"`
	// Label is the form label.
	Label string `json:"label"`
	// Help is optional helper text under the field.
	Help string `json:"help,omitempty"`
	// Secret renders the input as a password field.
	Secret bool `json:"secret,omitempty"`
}

// CredentialModel describes what the UI collects and how it packs into the
// Secret "token" key the provider reads (see servicectrl.readToken /
// tunnel.readServiceToken).
type CredentialModel struct {
	// Optional means the service may be used unauthenticated (e.g. Prometheus),
	// so the form must not require a credential.
	Optional bool `json:"optional,omitempty"`
	// Packing is how Fields combine into the token value.
	Packing Packing `json:"packing,omitempty"`
	// Fields are the inputs to render, in order.
	Fields []CredentialField `json:"fields,omitempty"`
	// Hint is a short human explanation of what to paste (replaces the portal's
	// old per-type tokenHint strings).
	Hint string `json:"hint,omitempty"`
}

// Tool is one HTTP operation exposed as an MCP tool. Name + Desc are serialized
// to the UI (so the portal can show what an AI agent can do with this service);
// Method + Path are backend-only.
type Tool struct {
	Name   string `json:"name"`
	Desc   string `json:"description,omitempty"`
	Method string `json:"-"`
	Path   string `json:"-"`
}

// ProbeMode selects how the validation reconciler interprets the probe result.
type ProbeMode string

const (
	// ProbeValidate: the probe path requires auth, so a 2xx proves the
	// credential is valid and 401/403 proves it is not.
	ProbeValidate ProbeMode = "validate"
	// ProbeReachable: any HTTP answer (even 401/404) proves the service is up;
	// the credential is not verified (marked Unknown unless the probe returned
	// 2xx with a token set).
	ProbeReachable ProbeMode = "reachable"
)

// Definition is one catalog entry. Fields with a json tag form the UI form
// schema served at /catalog; json:"-" fields are backend-only (tools, probe,
// always-sent headers).
type Definition struct {
	// Type is the Service.spec.type value (the map key).
	Type string `json:"type"`
	// DisplayName is the human name shown in the UI.
	DisplayName string `json:"displayName"`
	// Description is a one-line explanation for the UI.
	Description string `json:"description,omitempty"`
	// Category groups types in the UI picker ("Home", "Media", "Monitoring",
	// "Network", "Infrastructure", "Other").
	Category string `json:"category,omitempty"`

	// DefaultPort seeds the port field.
	DefaultPort int32 `json:"defaultPort,omitempty"`
	// DefaultScheme seeds the scheme field ("http" | "https").
	DefaultScheme string `json:"defaultScheme,omitempty"`
	// SchemeLocked forces DefaultScheme (e.g. UniFi is always https).
	SchemeLocked bool `json:"schemeLocked,omitempty"`
	// HostRequired means the service is not on the agent loopback and the user
	// must supply spec.host (e.g. a UniFi console at 192.168.1.1).
	HostRequired bool `json:"hostRequired,omitempty"`
	// HostHelp is helper text for the host field when HostRequired.
	HostHelp string `json:"hostHelp,omitempty"`

	// Auth is how the credential authenticates a request.
	Auth AuthKind `json:"auth"`
	// AuthParam is the header name (AuthAPIKeyHeader) or query key
	// (AuthAPIKeyQuery). Empty for the other kinds.
	AuthParam string `json:"authParam,omitempty"`
	// Credential is the UI form model for the credential.
	Credential CredentialModel `json:"credential"`

	// ExtraHeaders are always sent (e.g. Accept: application/json for Plex).
	ExtraHeaders map[string]string `json:"-"`
	// ProbePath is the health endpoint the validation reconciler hits. Empty =>
	// reachability probe on "/".
	ProbePath string `json:"-"`
	// ProbeMode selects how the probe status is interpreted (default validate
	// when a ProbePath is set, reachable otherwise).
	ProbeMode ProbeMode `json:"-"`
	// Handcoded marks types whose MCP tools are executed by hand-written Go
	// (Home Assistant) rather than driven by Tools. A Handcoded type may still
	// list Tools for display only (the UI shows them; they are not registered by
	// the data-driven registrar — see IsDataDriven).
	Handcoded bool `json:"-"`
	// Tools are the MCP operations exposed for this type. Serialized to the UI
	// (name + description) so the portal can show what the agent can do.
	Tools []Tool `json:"tools,omitempty"`

	// Instructions is backend-authored default guidance for AI clients about
	// this service type: quirks, gotchas, and recommended tool sequences (e.g.
	// "doorbell snapshots can transiently 500 — retry, or fall back to events").
	// It is composed into the MCP endpoint's "initialize" instructions ahead of
	// any operator-authored spec.instructions, which extend or override it. Not
	// user-provided — edit it here in the catalog — but serialized so the portal
	// can display it as the default context an agent already has.
	Instructions string `json:"instructions,omitempty"`
}

// singleField is a helper for the common "one opaque token" credential.
func singleField(key, label, help string) []CredentialField {
	return []CredentialField{{Key: key, Label: label, Help: help, Secret: true}}
}

// userPassFields is the username + password pair.
func userPassFields() []CredentialField {
	return []CredentialField{
		{Key: "username", Label: "Username"},
		{Key: "password", Label: "Password", Secret: true},
	}
}

// catalog is the registry. Keys are Service.spec.type values. Home Assistant is
// included (Handcoded) so it participates in the UI form + probe even though its
// MCP tools are hand-written in internal/tunnel/mcp_service.go.
var catalog = map[string]Definition{
	"home-assistant": {
		Type: "home-assistant", DisplayName: "Home Assistant", Category: "Home",
		Description: "Home automation hub — lights, sensors, scenes, and services.",
		DefaultPort: 8123, DefaultScheme: "http",
		Auth: AuthBearer,
		Credential: CredentialModel{
			Packing: PackSingle,
			Fields:  singleField("token", "Long-lived access token", "Profile → Security → Long-lived access tokens → Create."),
			Hint:    "A Home Assistant long-lived access token.",
		},
		ProbePath: "/api/config", ProbeMode: ProbeValidate,
		Handcoded: true,
		// Display-only: Home Assistant's tools are executed by hand-written Go
		// (mcp_service.go), not the data-driven registrar (Handcoded excludes it
		// from IsDataDriven). Listed here so the UI shows what the agent can do.
		Tools: []Tool{
			{Name: "states", Desc: "List entity states (lights, sensors, switches…)."},
			{Name: "get_state", Desc: "Read one entity's state by entity_id."},
			{Name: "call_service", Desc: "Call a service, e.g. turn a light on/off or open a cover."},
		},
	},
	"qbittorrent": {
		Type: "qbittorrent", DisplayName: "qBittorrent", Category: "Media",
		Description: "BitTorrent client WebUI.",
		DefaultPort: 8080, DefaultScheme: "http",
		Auth: AuthQBittorrent,
		Credential: CredentialModel{
			Packing: PackUserPass,
			Fields:  userPassFields(),
			Hint:    "The qBittorrent WebUI username and password.",
		},
		ProbePath: "/api/v2/app/version", ProbeMode: ProbeValidate,
		Tools: []Tool{
			{"torrents", "List torrents (filter/category via query, e.g. {\"filter\":\"downloading\"}).", http.MethodGet, "/api/v2/torrents/info"},
			{"transfer", "Global transfer stats (speeds, totals).", http.MethodGet, "/api/v2/transfer/info"},
			{"add", "Add a torrent. Pass form {\"urls\":\"magnet:?...\"}.", http.MethodPost, "/api/v2/torrents/add"},
			{"pause", "Pause torrents. Pass form {\"hashes\":\"<hash>|all\"}.", http.MethodPost, "/api/v2/torrents/pause"},
			{"resume", "Resume torrents. Pass form {\"hashes\":\"<hash>|all\"}.", http.MethodPost, "/api/v2/torrents/resume"},
			{"delete", "Delete torrents. Form {\"hashes\":\"<hash>\",\"deleteFiles\":\"false\"}.", http.MethodPost, "/api/v2/torrents/delete"},
		},
	},
	"prowlarr": {
		Type: "prowlarr", DisplayName: "Prowlarr", Category: "Media",
		Description: "Indexer manager for the *arr apps.",
		DefaultPort: 9696, DefaultScheme: "http",
		Auth: AuthAPIKeyHeader, AuthParam: "X-Api-Key",
		Credential: CredentialModel{Packing: PackSingle, Fields: singleField("apiKey", "API key", "Settings → General → API Key."), Hint: "The Prowlarr API key."},
		ProbePath: "/api/v1/system/status", ProbeMode: ProbeValidate,
		Tools: []Tool{
			{"indexers", "List configured Prowlarr indexers.", http.MethodGet, "/api/v1/indexer"},
			{"search", "Search across indexers. Pass query params, e.g. {\"query\":\"ubuntu\",\"type\":\"search\"}.", http.MethodGet, "/api/v1/search"},
			{"status", "Prowlarr system status/version.", http.MethodGet, "/api/v1/system/status"},
		},
	},
	"sonarr": {
		Type: "sonarr", DisplayName: "Sonarr", Category: "Media",
		Description: "TV series library and download manager.",
		DefaultPort: 8989, DefaultScheme: "http",
		Auth: AuthAPIKeyHeader, AuthParam: "X-Api-Key",
		Credential: CredentialModel{Packing: PackSingle, Fields: singleField("apiKey", "API key", "Settings → General → API Key."), Hint: "The Sonarr API key."},
		ProbePath: "/api/v3/system/status", ProbeMode: ProbeValidate,
		Tools: []Tool{
			{"series", "List all TV series in Sonarr.", http.MethodGet, "/api/v3/series"},
			{"queue", "List the download queue.", http.MethodGet, "/api/v3/queue"},
			{"calendar", "Upcoming/recent episodes (query: start, end as ISO dates).", http.MethodGet, "/api/v3/calendar"},
			{"lookup", "Search for a series to add. Pass query {\"term\":\"the wire\"}.", http.MethodGet, "/api/v3/series/lookup"},
		},
	},
	"radarr": {
		Type: "radarr", DisplayName: "Radarr", Category: "Media",
		Description: "Movie library and download manager.",
		DefaultPort: 7878, DefaultScheme: "http",
		Auth: AuthAPIKeyHeader, AuthParam: "X-Api-Key",
		Credential: CredentialModel{Packing: PackSingle, Fields: singleField("apiKey", "API key", "Settings → General → API Key."), Hint: "The Radarr API key."},
		ProbePath: "/api/v3/system/status", ProbeMode: ProbeValidate,
		Tools: []Tool{
			{"movies", "List all movies in Radarr.", http.MethodGet, "/api/v3/movie"},
			{"queue", "List the download queue.", http.MethodGet, "/api/v3/queue"},
			{"lookup", "Search for a movie to add. Pass query {\"term\":\"dune 2021\"}.", http.MethodGet, "/api/v3/movie/lookup"},
		},
	},
	"grafana": {
		Type: "grafana", DisplayName: "Grafana", Category: "Monitoring",
		Description: "Dashboards and data-source explorer.",
		DefaultPort: 3000, DefaultScheme: "http",
		Auth: AuthBearer,
		Credential: CredentialModel{Packing: PackSingle, Fields: singleField("token", "Service account token", "Administration → Service accounts → Add token."), Hint: "A Grafana service-account (or API) token."},
		ProbePath: "/api/org", ProbeMode: ProbeValidate,
		Tools: []Tool{
			{"search", "Search dashboards/folders. Query: {\"query\":\"cpu\",\"type\":\"dash-db\"}.", http.MethodGet, "/api/search"},
			{"datasources", "List configured data sources.", http.MethodGet, "/api/datasources"},
			{"health", "Grafana health/version.", http.MethodGet, "/api/health"},
			{"query", "Run a data-source query. Pass a JSON body per the Grafana /api/ds/query schema.", http.MethodPost, "/api/ds/query"},
		},
	},
	"grafana-loki": {
		Type: "grafana-loki", DisplayName: "Grafana Loki", Category: "Monitoring",
		Description: "Log aggregation (LogQL).",
		DefaultPort: 3100, DefaultScheme: "http",
		Auth: AuthBearer,
		Credential: CredentialModel{Optional: true, Packing: PackSingle, Fields: singleField("token", "Bearer token (optional)", "Leave empty if Loki is unauthenticated behind the tunnel."), Hint: "Optional bearer token; often unauthenticated behind the tunnel."},
		ProbePath: "/ready", ProbeMode: ProbeReachable,
		Tools: []Tool{
			{"query", "LogQL instant query. Query {\"query\":\"{app=\\\"x\\\"}\"}.", http.MethodGet, "/loki/api/v1/query"},
			{"query_range", "LogQL range query. Query {\"query\":\"...\",\"start\":\"...\",\"end\":\"...\"}.", http.MethodGet, "/loki/api/v1/query_range"},
			{"labels", "List log stream labels.", http.MethodGet, "/loki/api/v1/labels"},
		},
	},
	"prometheus": {
		Type: "prometheus", DisplayName: "Prometheus", Category: "Monitoring",
		Description: "Metrics and alerting (PromQL).",
		DefaultPort: 9090, DefaultScheme: "http",
		Auth: AuthBearer,
		Credential: CredentialModel{Optional: true, Packing: PackSingle, Fields: singleField("token", "Bearer token (optional)", "Leave empty if Prometheus is unauthenticated behind the tunnel."), Hint: "Optional bearer token; often unauthenticated behind the tunnel."},
		ProbePath: "/-/healthy", ProbeMode: ProbeReachable,
		Tools: []Tool{
			{"query", "Instant query. Pass query {\"query\":\"up\"}.", http.MethodGet, "/api/v1/query"},
			{"query_range", "Range query. Query: {\"query\":\"rate(...)\",\"start\":\"...\",\"end\":\"...\",\"step\":\"60\"}.", http.MethodGet, "/api/v1/query_range"},
			{"targets", "List scrape targets and their health.", http.MethodGet, "/api/v1/targets"},
			{"alerts", "List active alerts.", http.MethodGet, "/api/v1/alerts"},
		},
	},
	"jellyfin": {
		Type: "jellyfin", DisplayName: "Jellyfin", Category: "Media",
		Description: "Media server.",
		DefaultPort: 8096, DefaultScheme: "http",
		Auth: AuthAPIKeyHeader, AuthParam: "X-Emby-Token",
		Credential: CredentialModel{Packing: PackSingle, Fields: singleField("apiKey", "API key", "Dashboard → API Keys."), Hint: "A Jellyfin API key."},
		ProbePath: "/System/Info", ProbeMode: ProbeValidate,
		Tools: []Tool{
			{"sessions", "List active playback sessions.", http.MethodGet, "/Sessions"},
			{"system", "Server system info/version.", http.MethodGet, "/System/Info"},
			{"search", "Search the library. Pass query {\"searchTerm\":\"dune\"}.", http.MethodGet, "/Search/Hints"},
		},
	},
	"plex": {
		Type: "plex", DisplayName: "Plex", Category: "Media",
		Description: "Media server.",
		DefaultPort: 32400, DefaultScheme: "http",
		Auth: AuthAPIKeyHeader, AuthParam: "X-Plex-Token",
		ExtraHeaders: map[string]string{"Accept": "application/json"},
		Credential:   CredentialModel{Packing: PackSingle, Fields: singleField("token", "X-Plex-Token", "See Plex support: 'Finding an authentication token'."), Hint: "A Plex authentication token (X-Plex-Token)."},
		ProbePath:    "/identity", ProbeMode: ProbeReachable,
		Tools: []Tool{
			{"sessions", "List what's currently playing.", http.MethodGet, "/status/sessions"},
			{"libraries", "List library sections.", http.MethodGet, "/library/sections"},
			{"identity", "Server identity/version.", http.MethodGet, "/identity"},
		},
	},
	"portainer": {
		Type: "portainer", DisplayName: "Portainer", Category: "Infrastructure",
		Description: "Container management UI.",
		DefaultPort: 9000, DefaultScheme: "http",
		Auth: AuthAPIKeyHeader, AuthParam: "X-API-Key",
		Credential: CredentialModel{Packing: PackSingle, Fields: singleField("apiKey", "API key", "My account → Access tokens → Add access token."), Hint: "A Portainer API access token."},
		ProbePath: "/api/endpoints", ProbeMode: ProbeValidate,
		Tools: []Tool{
			{"endpoints", "List environments (endpoints).", http.MethodGet, "/api/endpoints"},
			{"stacks", "List deployed stacks.", http.MethodGet, "/api/stacks"},
			{"status", "Portainer status/version.", http.MethodGet, "/api/status"},
		},
	},
	"adguard": {
		Type: "adguard", DisplayName: "AdGuard Home", Category: "Network",
		Description: "Network-wide DNS ad/tracker blocking.",
		DefaultPort: 80, DefaultScheme: "http",
		Auth: AuthBasic,
		Credential: CredentialModel{Packing: PackUserPass, Fields: userPassFields(), Hint: "The AdGuard Home web interface username and password."},
		ProbePath: "/control/status", ProbeMode: ProbeValidate,
		Tools: []Tool{
			{"status", "Protection status + version.", http.MethodGet, "/control/status"},
			{"stats", "DNS query stats (top clients/domains, counts).", http.MethodGet, "/control/stats"},
			{"filtering", "Filtering (blocklists) status.", http.MethodGet, "/control/filtering/status"},
			{"protection", "Toggle protection. Body {\"enabled\":false,\"duration\":0}.", http.MethodPost, "/control/protection"},
		},
	},
	"proxmox": {
		Type: "proxmox", DisplayName: "Proxmox VE", Category: "Infrastructure",
		Description: "Virtualization management (VMs, containers, storage).",
		DefaultPort: 8006, DefaultScheme: "https",
		Auth: AuthProxmox,
		Credential: CredentialModel{Packing: PackSingle, Fields: singleField("token", "API token", "Datacenter → Permissions → API Tokens. Format USER@REALM!TOKENID=UUID."), Hint: "A Proxmox API token: USER@REALM!TOKENID=UUID."},
		ProbePath: "/api2/json/version", ProbeMode: ProbeValidate,
		Tools: []Tool{
			{"nodes", "List cluster nodes.", http.MethodGet, "/api2/json/nodes"},
			{"resources", "List cluster resources (VMs, storage, nodes).", http.MethodGet, "/api2/json/cluster/resources"},
			{"cluster_status", "Cluster/quorum status.", http.MethodGet, "/api2/json/cluster/status"},
		},
	},
	"pihole": {
		Type: "pihole", DisplayName: "Pi-hole", Category: "Network",
		Description: "Network-wide DNS ad blocking (v6).",
		DefaultPort: 80, DefaultScheme: "http",
		Auth: AuthPihole,
		Credential: CredentialModel{Packing: PackSingle, Fields: singleField("password", "Web password", "The Pi-hole admin web interface password."), Hint: "The Pi-hole v6 web interface password."},
		ProbePath: "/api/dns/blocking", ProbeMode: ProbeValidate,
		Tools: []Tool{
			{"summary", "Query/blocking summary stats.", http.MethodGet, "/api/stats/summary"},
			{"blocking", "Blocking enabled/disabled status.", http.MethodGet, "/api/dns/blocking"},
			{"disable", "Pause blocking. Body {\"blocking\":false,\"timer\":300}.", http.MethodPost, "/api/dns/blocking"},
			{"top_domains", "Top permitted/blocked domains.", http.MethodGet, "/api/stats/top_domains"},
		},
	},
	"unifi-network": {
		Type: "unifi-network", DisplayName: "UniFi Network", Category: "Network",
		Description: "UniFi OS console — clients, devices, sites.",
		DefaultPort: 443, DefaultScheme: "https", SchemeLocked: true,
		HostRequired: true, HostHelp: "The UniFi console address, e.g. 192.168.1.1.",
		Auth: AuthAPIKeyHeader, AuthParam: "X-API-KEY",
		Credential: CredentialModel{Packing: PackSingle, Fields: singleField("apiKey", "API key", "UniFi OS → Control Plane → Integrations → create a local API key."), Hint: "A UniFi OS local API key."},
		ProbePath: "/proxy/network/integration/v1/sites", ProbeMode: ProbeValidate,
		Tools: []Tool{
			{"sites", "List UniFi Network sites (each site's id + name).", http.MethodGet, "/proxy/network/integration/v1/sites"},
			{"clients", "List connected clients for a site. Query {\"siteId\":\"<id>\"}.", http.MethodGet, "/proxy/network/integration/v1/sites/{siteId}/clients"},
			{"devices", "List UniFi devices (APs/switches/gateways) for a site. Query {\"siteId\":\"<id>\"}.", http.MethodGet, "/proxy/network/integration/v1/sites/{siteId}/devices"},
		},
	},
	"unifi-protect": {
		Type: "unifi-protect", DisplayName: "UniFi Protect", Category: "Network",
		Description: "UniFi OS console — cameras and snapshots.",
		DefaultPort: 443, DefaultScheme: "https", SchemeLocked: true,
		HostRequired: true, HostHelp: "The UniFi console address, e.g. 192.168.1.1.",
		Auth: AuthAPIKeyHeader, AuthParam: "X-API-KEY",
		Credential: CredentialModel{Packing: PackSingle, Fields: singleField("apiKey", "API key", "UniFi OS → Control Plane → Integrations → create a local API key."), Hint: "A UniFi OS local API key (same key works for Network + Protect)."},
		ProbePath: "/proxy/protect/integration/v1/cameras", ProbeMode: ProbeValidate,
		Instructions: "The snapshot tool captures a live frame on demand, so it depends on the camera's current state. " +
			"Battery- or power-managed cameras (notably G4/G5 doorbells) drop to standby and their snapshot may return a 500 \"UNKNOWN_ERROR\" until they wake — this is UniFi Protect's response, not a credential or connectivity fault. " +
			"On a snapshot 500, retry the same camera a couple of times (waking it usually succeeds), or take a snapshot from another camera covering the same area. " +
			"Snapshots are returned as images you can view directly. Omit highQuality unless you specifically need full resolution — it makes the doorbell 500 more likely. " +
			"The events tool returns recent motion/ring/smart-detect events captured from Protect's live WebSocket feed (the provider subscribes in the background and buffers them). It is a rolling recent-history buffer, not a full archive: it only holds events seen since the subscription connected, most recent first. To see who/what triggered a camera, query events and then snapshot that camera.",
		Tools: []Tool{
			{"cameras", "List Protect cameras (each camera's id, name, state).", http.MethodGet, "/proxy/protect/integration/v1/cameras"},
			{"snapshot", "Current snapshot (JPEG) from a camera. Query {\"cameraId\":\"<id>\"} (optional {\"highQuality\":\"true\"}). Live capture: may 500 on a sleeping doorbell — retry or try another camera.", http.MethodGet, "/proxy/protect/integration/v1/cameras/{cameraId}/snapshot"},
		},
	},
	"generic": {
		Type: "generic", DisplayName: "Generic HTTP service", Category: "Other",
		Description: "Any HTTP service — proxied through the tunnel, no MCP tools.",
		DefaultPort: 80, DefaultScheme: "http",
		Auth: AuthBearer,
		Credential: CredentialModel{Optional: true, Packing: PackSingle, Fields: singleField("token", "Bearer token (optional)", "Injected as Authorization: Bearer when set."), Hint: "Optional bearer token injected on proxied requests."},
		ProbeMode: ProbeReachable,
	},
}

// Get returns the definition for a service type, if known.
func Get(t string) (Definition, bool) {
	d, ok := catalog[t]
	return d, ok
}

// IsKnown reports whether t is a catalog type.
func IsKnown(t string) bool {
	_, ok := catalog[t]
	return ok
}

// DefaultInstructions returns the backend-authored default agent guidance for a
// service type, or "" if the type is unknown or has none. Callers compose it
// into the MCP instructions ahead of the operator's spec.instructions.
func DefaultInstructions(t string) string {
	return catalog[t].Instructions
}

// HasTools reports whether a type exposes any MCP tools (data-driven or the
// hand-coded Home Assistant bundle). Replaces the old mcpServiceType.
func HasTools(t string) bool {
	d, ok := catalog[t]
	return ok && (d.Handcoded || len(d.Tools) > 0)
}

// IsDataDriven reports whether a type's MCP tools are driven by the catalog
// registrar (Definition.Tools) rather than hand-coded. Handcoded types (Home
// Assistant) are excluded even when they list Tools for display. Replaces the
// old catalogServiceType.
func IsDataDriven(t string) bool {
	d, ok := catalog[t]
	return ok && !d.Handcoded && len(d.Tools) > 0
}

// All returns every definition, sorted by category then display name — the
// order the UI renders. Definitions are copied so callers cannot mutate the
// registry.
func All() []Definition {
	out := make([]Definition, 0, len(catalog))
	for _, d := range catalog {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].DisplayName < out[j].DisplayName
	})
	return out
}
