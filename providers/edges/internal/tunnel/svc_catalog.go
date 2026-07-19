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

package tunnel

// The service catalog turns a handful of popular self-hosted apps into MCP
// tools without bespoke Go per app: each is a data table (auth style + a list
// of HTTP operations), and one generic registrar/executor drives them all.
// Home Assistant stays hand-written (mcp_service.go) because its tools are
// trimmed/reshaped; everything here is a thin, faithful proxy of the app's API.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/provider-edges/internal/haclient"
)

// snippet trims a byte slice for inclusion in an error message.
func snippet(b []byte) string {
	const max = 240
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// authKind selects how the service's token authenticates a request.
type authKind int

const (
	authBearer       authKind = iota // Authorization: Bearer <token>   (Grafana, Prometheus)
	authAPIKeyHeader                 // <headerName>: <token>            (*arr apps: X-Api-Key; Jellyfin, Plex, Portainer)
	authAPIKeyQuery                  // ?<param>=<token>
	authQBittorrent                  // POST /api/v2/auth/login -> SID cookie
	authBasic                        // Authorization: Basic base64("user:pass")   (AdGuard Home)
	authProxmox                      // Authorization: PVEAPIToken=<token>          (Proxmox VE API token)
	authPihole                       // POST /api/auth {password} -> SID; sent as X-FTL-SID (Pi-hole v6)
)

// catToolInput is the single, generic tool input. A tool's description tells the
// model which of these to fill; reads typically need none.
type catToolInput struct {
	Query map[string]string `json:"query,omitempty" jsonschema:"description=Optional query-string parameters, e.g. {\"query\":\"ubuntu\"} for a search."`
	Form  map[string]string `json:"form,omitempty" jsonschema:"description=Optional form fields for form-encoded APIs (qBittorrent actions), e.g. {\"urls\":\"magnet:?...\"}."`
	Body  string            `json:"body,omitempty" jsonschema:"description=Optional raw JSON request body for JSON POST/PUT actions."`
}

// catTool is one HTTP operation exposed as an MCP tool.
type catTool struct {
	name   string // suffix appended after the service prefix
	desc   string
	method string
	path   string
}

// svcDef is a catalog entry: how to auth + which operations to expose.
type svcDef struct {
	displayName   string
	defaultPort   int32
	auth          authKind
	authParam     string            // header name (authAPIKeyHeader) or query key (authAPIKeyQuery)
	tokenOptional bool              // service may be unauthenticated (e.g. Prometheus) — don't require a token
	extraHeaders  map[string]string // always-sent headers, e.g. Accept: application/json for Plex
	tools         []catTool
}

// svcCatalog is the registry of data-driven service types. Keys are the
// Service.spec.type values. Home Assistant is intentionally absent (hand-coded).
var svcCatalog = map[string]svcDef{
	"prowlarr": {
		displayName: "Prowlarr", defaultPort: 9696, auth: authAPIKeyHeader, authParam: "X-Api-Key",
		tools: []catTool{
			{"indexers", "List configured Prowlarr indexers.", http.MethodGet, "/api/v1/indexer"},
			{"search", "Search across indexers. Pass query params, e.g. {\"query\":\"ubuntu\",\"type\":\"search\"}.", http.MethodGet, "/api/v1/search"},
			{"status", "Prowlarr system status/version.", http.MethodGet, "/api/v1/system/status"},
		},
	},
	"sonarr": {
		displayName: "Sonarr", defaultPort: 8989, auth: authAPIKeyHeader, authParam: "X-Api-Key",
		tools: []catTool{
			{"series", "List all TV series in Sonarr.", http.MethodGet, "/api/v3/series"},
			{"queue", "List the download queue.", http.MethodGet, "/api/v3/queue"},
			{"calendar", "Upcoming/recent episodes (query: start, end as ISO dates).", http.MethodGet, "/api/v3/calendar"},
			{"lookup", "Search for a series to add. Pass query {\"term\":\"the wire\"}.", http.MethodGet, "/api/v3/series/lookup"},
		},
	},
	"radarr": {
		displayName: "Radarr", defaultPort: 7878, auth: authAPIKeyHeader, authParam: "X-Api-Key",
		tools: []catTool{
			{"movies", "List all movies in Radarr.", http.MethodGet, "/api/v3/movie"},
			{"queue", "List the download queue.", http.MethodGet, "/api/v3/queue"},
			{"lookup", "Search for a movie to add. Pass query {\"term\":\"dune 2021\"}.", http.MethodGet, "/api/v3/movie/lookup"},
		},
	},
	"grafana": {
		displayName: "Grafana", defaultPort: 3000, auth: authBearer,
		tools: []catTool{
			{"search", "Search dashboards/folders. Query: {\"query\":\"cpu\",\"type\":\"dash-db\"}.", http.MethodGet, "/api/search"},
			{"datasources", "List configured data sources.", http.MethodGet, "/api/datasources"},
			{"health", "Grafana health/version.", http.MethodGet, "/api/health"},
			{"query", "Run a data-source query. Pass a JSON body per the Grafana /api/ds/query schema.", http.MethodPost, "/api/ds/query"},
		},
	},
	"qbittorrent": {
		displayName: "qBittorrent", defaultPort: 8080, auth: authQBittorrent,
		tools: []catTool{
			{"torrents", "List torrents (filter/category via query, e.g. {\"filter\":\"downloading\"}).", http.MethodGet, "/api/v2/torrents/info"},
			{"transfer", "Global transfer stats (speeds, totals).", http.MethodGet, "/api/v2/transfer/info"},
			{"add", "Add a torrent. Pass form {\"urls\":\"magnet:?...\"}.", http.MethodPost, "/api/v2/torrents/add"},
			{"pause", "Pause torrents. Pass form {\"hashes\":\"<hash>|all\"}.", http.MethodPost, "/api/v2/torrents/pause"},
			{"resume", "Resume torrents. Pass form {\"hashes\":\"<hash>|all\"}.", http.MethodPost, "/api/v2/torrents/resume"},
			{"delete", "Delete torrents. Form {\"hashes\":\"<hash>\",\"deleteFiles\":\"false\"}.", http.MethodPost, "/api/v2/torrents/delete"},
		},
	},
	"jellyfin": {
		displayName: "Jellyfin", defaultPort: 8096, auth: authAPIKeyHeader, authParam: "X-Emby-Token",
		tools: []catTool{
			{"sessions", "List active playback sessions.", http.MethodGet, "/Sessions"},
			{"system", "Server system info/version.", http.MethodGet, "/System/Info"},
			{"search", "Search the library. Pass query {\"searchTerm\":\"dune\"}.", http.MethodGet, "/Search/Hints"},
		},
	},
	"plex": {
		// Plex speaks XML by default; ask for JSON. Token is the X-Plex-Token.
		displayName: "Plex", defaultPort: 32400, auth: authAPIKeyHeader, authParam: "X-Plex-Token",
		extraHeaders: map[string]string{"Accept": "application/json"},
		tools: []catTool{
			{"sessions", "List what's currently playing.", http.MethodGet, "/status/sessions"},
			{"libraries", "List library sections.", http.MethodGet, "/library/sections"},
			{"identity", "Server identity/version.", http.MethodGet, "/identity"},
		},
	},
	"portainer": {
		displayName: "Portainer", defaultPort: 9000, auth: authAPIKeyHeader, authParam: "X-API-Key",
		tools: []catTool{
			{"endpoints", "List environments (endpoints).", http.MethodGet, "/api/endpoints"},
			{"stacks", "List deployed stacks.", http.MethodGet, "/api/stacks"},
			{"status", "Portainer status/version.", http.MethodGet, "/api/status"},
		},
	},
	"prometheus": {
		// Often unauthenticated behind the tunnel; token (Bearer) is optional.
		displayName: "Prometheus", defaultPort: 9090, auth: authBearer, tokenOptional: true,
		tools: []catTool{
			{"query", "Instant query. Pass query {\"query\":\"up\"}.", http.MethodGet, "/api/v1/query"},
			{"query_range", "Range query. Query: {\"query\":\"rate(...)\",\"start\":\"...\",\"end\":\"...\",\"step\":\"60\"}.", http.MethodGet, "/api/v1/query_range"},
			{"targets", "List scrape targets and their health.", http.MethodGet, "/api/v1/targets"},
			{"alerts", "List active alerts.", http.MethodGet, "/api/v1/alerts"},
		},
	},
	"grafana-loki": {
		displayName: "Grafana Loki", defaultPort: 3100, auth: authBearer, tokenOptional: true,
		tools: []catTool{
			{"query", "LogQL instant query. Query {\"query\":\"{app=\\\"x\\\"}\"}.", http.MethodGet, "/loki/api/v1/query"},
			{"query_range", "LogQL range query. Query {\"query\":\"...\",\"start\":\"...\",\"end\":\"...\"}.", http.MethodGet, "/loki/api/v1/query_range"},
			{"labels", "List log stream labels.", http.MethodGet, "/loki/api/v1/labels"},
		},
	},
	"adguard": {
		// AdGuard Home uses HTTP Basic auth; token Secret holds "user:password".
		displayName: "AdGuard Home", defaultPort: 80, auth: authBasic,
		tools: []catTool{
			{"status", "Protection status + version.", http.MethodGet, "/control/status"},
			{"stats", "DNS query stats (top clients/domains, counts).", http.MethodGet, "/control/stats"},
			{"filtering", "Filtering (blocklists) status.", http.MethodGet, "/control/filtering/status"},
			{"protection", "Toggle protection. Body {\"enabled\":false,\"duration\":0}.", http.MethodPost, "/control/protection"},
		},
	},
	"proxmox": {
		// Proxmox VE: token Secret holds an API token "USER@REALM!TOKENID=UUID".
		// Almost always https + self-signed — set the Service spec.scheme=https.
		displayName: "Proxmox VE", defaultPort: 8006, auth: authProxmox,
		tools: []catTool{
			{"nodes", "List cluster nodes.", http.MethodGet, "/api2/json/nodes"},
			{"resources", "List cluster resources (VMs, storage, nodes).", http.MethodGet, "/api2/json/cluster/resources"},
			{"cluster_status", "Cluster/quorum status.", http.MethodGet, "/api2/json/cluster/status"},
		},
	},
	"pihole": {
		// Pi-hole v6 REST API: token Secret holds the web-password; we exchange it
		// for a session SID (see piholeLogin) sent as X-FTL-SID.
		displayName: "Pi-hole", defaultPort: 80, auth: authPihole,
		tools: []catTool{
			{"summary", "Query/blocking summary stats.", http.MethodGet, "/api/stats/summary"},
			{"blocking", "Blocking enabled/disabled status.", http.MethodGet, "/api/dns/blocking"},
			{"disable", "Pause blocking. Body {\"blocking\":false,\"timer\":300}.", http.MethodPost, "/api/dns/blocking"},
			{"top_domains", "Top permitted/blocked domains.", http.MethodGet, "/api/stats/top_domains"},
		},
	},
	"unifi-network": {
		// UniFi OS console (UDM/UDR/Cloud Key). Always https + self-signed → set
		// the Service spec.scheme=https, spec.port=443. Auth is a UniFi OS local
		// API key (X-API-KEY) against the official Network integration API. Most
		// home setups have a single site — call "sites" first to get its id, then
		// pass it: {"siteId":"<id>"}. Paths of the form {siteId} are filled from
		// the tool's query map.
		displayName: "UniFi Network", defaultPort: 443, auth: authAPIKeyHeader, authParam: "X-API-KEY",
		tools: []catTool{
			{"sites", "List UniFi Network sites (each site's id + name).", http.MethodGet, "/proxy/network/integration/v1/sites"},
			{"clients", "List connected clients for a site. Query {\"siteId\":\"<id>\"}.", http.MethodGet, "/proxy/network/integration/v1/sites/{siteId}/clients"},
			{"devices", "List UniFi devices (APs/switches/gateways) for a site. Query {\"siteId\":\"<id>\"}.", http.MethodGet, "/proxy/network/integration/v1/sites/{siteId}/devices"},
		},
	},
	"unifi-protect": {
		// UniFi Protect on the same UniFi OS console (host:443, https). Auth is a
		// UniFi OS local API key (X-API-KEY) against the official Protect
		// integration API. "snapshot" returns a JPEG the agent can see — call
		// "cameras" first to get camera ids.
		displayName: "UniFi Protect", defaultPort: 443, auth: authAPIKeyHeader, authParam: "X-API-KEY",
		tools: []catTool{
			{"cameras", "List Protect cameras (each camera's id, name, state).", http.MethodGet, "/proxy/protect/integration/v1/cameras"},
			{"snapshot", "Current snapshot (JPEG) from a camera. Query {\"cameraId\":\"<id>\"} (optional {\"highQuality\":\"true\"}).", http.MethodGet, "/proxy/protect/integration/v1/cameras/{cameraId}/snapshot"},
			{"events", "Recent Protect events (motion/person/vehicle). Query: start,end (ms epoch), types.", http.MethodGet, "/proxy/protect/integration/v1/events"},
		},
	},
}

// catalogServiceType reports whether a Service type is served by the catalog.
func catalogServiceType(t string) bool {
	_, ok := svcCatalog[t]
	return ok
}

// registerCatalogTools installs the MCP tools for a catalog service type.
func (p *Server) registerCatalogTools(srv *mcp.Server, prefix, cluster, kcpToken string, svc *serviceView, dialer haclient.Dialer) {
	def, ok := svcCatalog[svc.Spec.Type]
	if !ok {
		return
	}
	for _, t := range def.tools {
		tool := t // capture
		mcp.AddTool(srv, &mcp.Tool{
			Name:        prefix + tool.name,
			Description: def.displayName + " — " + tool.desc,
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in catToolInput) (*mcp.CallToolResult, any, error) {
			return p.callCatalogTool(ctx, cluster, kcpToken, svc, dialer, def, tool, in)
		})
	}
}

// callCatalogTool executes one catalog tool: resolve the token, apply the auth
// style, build the request (query/form/body), proxy it through the edge, and
// return the response body verbatim.
func (p *Server) callCatalogTool(ctx context.Context, cluster, kcpToken string, svc *serviceView, dialer haclient.Dialer, def svcDef, tool catTool, in catToolInput) (*mcp.CallToolResult, any, error) {
	token, err := p.readServiceToken(ctx, cluster, svc, kcpToken)
	if err != nil {
		return toolErr("service credentials: " + err.Error()), nil, nil
	}
	if token == "" && !def.tokenOptional {
		return toolErr("no auth token configured for this service (set spec.authSecretRef)"), nil, nil
	}

	header := http.Header{}
	for k, v := range def.extraHeaders {
		header.Set(k, v)
	}
	// Path parameters: any {key} in the tool path is filled from the query map
	// (e.g. {"siteId":"..."} → /sites/{siteId}/clients); everything else becomes
	// the query string.
	path := tool.path
	q := url.Values{}
	for k, v := range in.Query {
		if ph := "{" + k + "}"; strings.Contains(path, ph) {
			path = strings.ReplaceAll(path, ph, url.PathEscape(v))
		} else {
			q.Set(k, v)
		}
	}

	if token != "" {
		switch def.auth {
		case authBearer:
			header.Set("Authorization", "Bearer "+token)
		case authAPIKeyHeader:
			header.Set(def.authParam, token)
		case authAPIKeyQuery:
			q.Set(def.authParam, token)
		case authBasic:
			header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(token)))
		case authProxmox:
			header.Set("Authorization", "PVEAPIToken="+token)
		case authQBittorrent:
			sid, err := p.qbitLogin(ctx, cluster, svc, dialer, token)
			if err != nil {
				return toolErr("qBittorrent login failed: " + err.Error()), nil, nil
			}
			header.Set("Cookie", "SID="+sid)
			header.Set("Referer", (haclient.Target{Scheme: svc.scheme(), Host: svc.targetHost(), Port: svc.Spec.Port}).SvcTarget())
		case authPihole:
			sid, err := p.piholeLogin(ctx, cluster, svc, dialer, token)
			if err != nil {
				return toolErr("Pi-hole login failed: " + err.Error()), nil, nil
			}
			header.Set("X-FTL-SID", sid)
		}
	}

	// Request body: form-encoded (qBittorrent actions) or raw JSON.
	var bodyReader io.Reader
	switch {
	case len(in.Form) > 0:
		form := url.Values{}
		for k, v := range in.Form {
			form.Set(k, v)
		}
		bodyReader = strings.NewReader(form.Encode())
		header.Set("Content-Type", "application/x-www-form-urlencoded")
	case strings.TrimSpace(in.Body) != "":
		bodyReader = strings.NewReader(in.Body)
		header.Set("Content-Type", "application/json")
	}

	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	target := haclient.Target{Scheme: svc.scheme(), Host: svc.targetHost(), Port: svc.Spec.Port}
	resp, err := haclient.DoWith(ctx, dialer, target, tool.method, path, header, bodyReader)
	if err != nil {
		return toolErr(err.Error()), nil, nil
	}
	defer resp.Body.Close() //nolint:errcheck
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return toolErr(fmt.Sprintf("%s returned %d: %s", def.displayName, resp.StatusCode, snippet(respBody))), nil, nil
	}
	// Binary image responses (e.g. a UniFi Protect camera snapshot) are returned
	// as MCP image content so the model can actually see them.
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "image/") {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: respBody, MIMEType: ct}}}, nil, nil
	}
	text := string(respBody)
	if strings.TrimSpace(text) == "" {
		text = fmt.Sprintf("OK (%d)", resp.StatusCode)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
}

// qbitLogin performs qBittorrent's cookie login and returns the SID. The token
// Secret holds "username:password" (the WebUI credentials). Returns an error if
// the app rejects the login (wrong creds, or CSRF/host-header protection — in
// which case whitelist the edge or disable those checks in qBittorrent).
func (p *Server) qbitLogin(ctx context.Context, cluster string, svc *serviceView, dialer haclient.Dialer, cred string) (string, error) {
	user, pass, ok := strings.Cut(cred, ":")
	if !ok {
		return "", fmt.Errorf("qBittorrent credential must be \"username:password\"")
	}
	form := url.Values{"username": {user}, "password": {pass}}
	target := haclient.Target{Scheme: svc.scheme(), Host: svc.targetHost(), Port: svc.Spec.Port}
	header := http.Header{
		"Content-Type": {"application/x-www-form-urlencoded"},
		"Referer":      {target.SvcTarget()},
	}
	resp, err := haclient.DoWith(ctx, dialer, target, http.MethodPost, "/api/v2/auth/login", header, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), "Fails") {
		return "", fmt.Errorf("login rejected (%d): %s", resp.StatusCode, snippet(body))
	}
	for _, c := range resp.Cookies() {
		if c.Name == "SID" {
			return c.Value, nil
		}
	}
	return "", fmt.Errorf("no SID cookie in login response")
}

// piholeLogin performs Pi-hole v6's session login and returns the SID. The token
// Secret holds the web-interface password. Returns an error if the app rejects
// it (wrong password, or the API session limit is hit).
func (p *Server) piholeLogin(ctx context.Context, cluster string, svc *serviceView, dialer haclient.Dialer, password string) (string, error) {
	target := haclient.Target{Scheme: svc.scheme(), Host: svc.targetHost(), Port: svc.Spec.Port}
	header := http.Header{"Content-Type": {"application/json"}}
	reqBody, err := json.Marshal(map[string]string{"password": password})
	if err != nil {
		return "", err
	}
	resp, err := haclient.DoWith(ctx, dialer, target, http.MethodPost, "/api/auth", header, strings.NewReader(string(reqBody)))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login rejected (%d): %s", resp.StatusCode, snippet(body))
	}
	var out struct {
		Session struct {
			Valid bool   `json:"valid"`
			SID   string `json:"sid"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode login response: %w", err)
	}
	if !out.Session.Valid || out.Session.SID == "" {
		return "", fmt.Errorf("login not valid: %s", snippet(body))
	}
	return out.Session.SID, nil
}
