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

package aggregatemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/pkg/virtual/builder"
)

// defaultImpl is the fallback MCP Implementation advertised on `initialize`
// when the caller doesn't override Config.ServerMetadata.
var defaultImpl = &mcp.Implementation{
	Name:    "kedge-mcpserver",
	Title:   "Kedge — Kubernetes & Linux Edge Hub",
	Version: "v0.1.0",
}

// defaultInstructions is the system-prompt-style hint passed back on
// `initialize` in ServerCapabilities.  Per the MCP spec, hosts SHOULD
// forward this to the LLM, so it's the cleanest channel for "what is this
// server, what can it do, how should you use it" guidance.  Tools also have
// their own per-tool descriptions for finer-grained discovery.
const defaultInstructions = `This is the kedge aggregate MCP endpoint.  It manages two kinds of edge fleets behind one connection:

  * Kubernetes clusters (any kube-type edge registered with the hub) — driven via the upstream kubernetes-mcp-server toolsets.  Pass the edge name as the "cluster" argument to any kube tool to target a specific cluster.

  * Linux servers (any server-type edge registered with the hub) — driven via the in-tree linux toolsets over SSH.  Pass the edge name as the "target" argument to any linux tool (run_command, read_file, systemd_unit_status, ...).

Always call the "list_targets" tool first to enumerate every reachable edge with its kind, labels, and live connection state.  Pick a target from that list before invoking any kube or linux tool — never guess names.  Tool families do not cross types: kube tools only work against kubernetes edges, linux tools only work against server edges.`

// ServerMetadata is the dressing the aggregator advertises on `initialize`
// — name/title/version/website + a free-form Instructions blurb that hosts
// like Claude Desktop pass to the LLM as ambient context.  This is the
// primary channel for explaining what an aggregate MCP server *is* and how
// it should be used, so the AI knows it can reach kube clusters and Linux
// servers from one endpoint without trial-and-error.
type ServerMetadata struct {
	// Name is the machine identifier shown in serverInfo.name (clients use
	// this for logging / config lookups).  Defaults to "kedge-mcpserver".
	Name string
	// Title is the human-readable name shown in MCP-server pickers in some
	// clients (Claude Desktop, Cursor).  Defaults to a kedge-branded string.
	Title string
	// Version pins the implementation version reported on initialize.
	Version string
	// WebsiteURL is the optional landing page for the server.  Some MCP
	// clients render this as a clickable link in the server picker.
	WebsiteURL string
	// Instructions is the ambient context blurb hosts forward to the LLM
	// as part of the system prompt for this MCP server.  Empty string uses
	// defaultInstructions.
	Instructions string
}

// implementation merges the caller-supplied metadata over the defaults so
// the result is always a fully-populated *mcp.Implementation.
func (m ServerMetadata) implementation() *mcp.Implementation {
	out := *defaultImpl
	if m.Name != "" {
		out.Name = m.Name
	}
	if m.Title != "" {
		out.Title = m.Title
	}
	if m.Version != "" {
		out.Version = m.Version
	}
	if m.WebsiteURL != "" {
		out.WebsiteURL = m.WebsiteURL
	}
	return &out
}

func (m ServerMetadata) instructions() string {
	if m.Instructions != "" {
		return m.Instructions
	}
	return defaultInstructions
}

// TargetInfo is one entry in the list_targets output: a single edge with its
// kind, live connection state, and labels.  The hub-side enumerator builds
// these from the live edge list + ConnManager.
type TargetInfo struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"` // "kubernetes" | "linux"
	Connected bool              `json:"connected"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// TargetEnumerator returns the current kube + linux edge inventories.
// Called on every list_targets tool invocation so the AI sees fresh state
// rather than a snapshot taken when the MCP session opened.
type TargetEnumerator func(ctx context.Context) (kube, linux []TargetInfo, err error)

// Config wires the per-request inputs the aggregate handler needs to
// build a fresh mcp.Server. Edge resolution + selector filtering
// happens BEFORE this struct is constructed (the caller — see
// providers/mcp/virtual/builder.go — owns CR fetch + edgeSelector
// evaluation); the aggregator partitions the resolved edges by family
// EdgeType, builds a FamilyContext per registered ToolFamily, and
// invokes each family's Register callback on the shared mcp.Server.
//
// Adding a new edge kind (e.g. windows-edges, ESXi-edges, …) requires
// only a new RegisterToolFamily call from that provider's init() —
// this Config doesn't grow per-kind fields.
type Config struct {
	// Cluster is the kcp logical cluster name (tenant workspace path
	// segment) the MCPServer CR was fetched from. Forwarded to family
	// contexts; tools use it to scope edges-proxy / kcp calls.
	Cluster string

	// EdgesByType is the resolved edge inventory partitioned by
	// Edge.spec.type. Only includes edges that match the MCPServer's
	// edgeSelector AND have a live tunnel. Keys correspond to
	// ToolFamily.EdgeType values; families with no matching edges get
	// an empty slice (still invoked so their tools register).
	EdgesByType map[string][]string

	// ToolsetsByFamily is the per-family toolset selection from the CR
	// (MCPServer.spec.kubernetesToolsets, .linuxToolsets, etc.). Keyed
	// by family Name. Empty = family defaults.
	ToolsetsByFamily map[string][]string

	// ExtrasByFamily lets the CR carry family-specific extension knobs
	// (e.g. linux's commandTimeoutSeconds, maxOutputBytes) without the
	// aggregator needing to know about them. Family Register pulls out
	// what it understands; unknown keys are ignored.
	ExtrasByFamily map[string]map[string]any

	// BearerToken is the caller's auth. Forwarded to family contexts.
	BearerToken string

	// HubBaseURL is the URL families use for callback URLs (kube tools
	// looping back to edges-proxy, for example). Trimmed of trailing
	// slash, no /services prefix.
	HubBaseURL string

	// ReadOnly mirrors MCPServer.spec.readOnly. Threaded into every
	// FamilyContext; families that honor it skip / hide mutating tools.
	ReadOnly bool

	// Deps is the framework dependency bundle. Forwarded so families
	// can open SSH sessions, fetch credentials, etc.
	Deps *builder.Deps

	// CallerIdentity is the resolved RBAC identity (e.g. "kedge:<sub>")
	// for the bearer token. Used by linux family for SSHUserMapping=identity.
	CallerIdentity string

	// Enumerate is invoked on every list_targets call.  Must not be nil.
	Enumerate TargetEnumerator

	// Providers, when set, returns the live set of Ready external
	// providers exposing an MCP endpoint. newServer() federates each
	// provider's tools/list into the aggregate as
	// "<provider-slug>__<tool-name>" so MCP clients can invoke
	// provider tools (e.g. infrastructure's kro_provision) over the
	// same connection they use for edges. nil = disabled. See
	// provider_proxy.go.
	Providers ProviderEnumerator

	// Metadata controls the MCP serverInfo + Instructions returned on
	// `initialize`.  Hosts forward Instructions to the LLM as ambient
	// context, so this is the primary place to explain what the endpoint
	// is and how to use it.  Zero value uses defaultImpl + defaultInstructions.
	Metadata ServerMetadata

	// About is the structured "what is this server" document published as
	// the `kedge://about` MCP resource.  AI clients can `resources/read`
	// it to fetch a JSON snapshot of capabilities, tenant context, edge
	// counts, etc. — a machine-friendly counterpart to Metadata.Instructions.
	// When zero, the handler synthesises sensible defaults from the rest of
	// Config so the resource is always present.
	About AboutDoc
}

// AboutDoc is the JSON document served from the `kedge://about` resource.
// AI clients call `resources/read kedge://about` to enumerate kedge-specific
// facts they can rely on without reading prose:
//
//   - What kinds of edges this endpoint controls (kubernetes / linux).
//   - Which tenant workspace + MCPServer object this endpoint represents.
//   - Live edge counts at the moment the resource is read.
//   - The companion `list_targets` tool name so the AI can chain reads → tool calls.
//
// Adding new fields here is backward-compatible: clients ignore unknown JSON
// keys.  Bump SchemaVersion when an existing field changes meaning.
type AboutDoc struct {
	// SchemaVersion is "kedge.faros.sh/about/v1" — bump on breaking changes.
	SchemaVersion string `json:"schemaVersion"`
	// Role is "aggregate" | "kubernetes" | "linux" — clarifies which family
	// of tools this endpoint exposes.
	Role string `json:"role"`
	// Capabilities lists the edge kinds reachable through this endpoint
	// ("kubernetes", "linux").  AI clients use this to decide whether the
	// server is the right one to dispatch a given user request to.
	Capabilities []string `json:"capabilities"`
	// Tenant is the kcp logical cluster name this endpoint serves.
	Tenant string `json:"tenant,omitempty"`
	// MCPServer is the metadata.name of the backing CR.
	MCPServer string `json:"mcpServer,omitempty"`
	// EndpointURL is the canonical HTTP URL this server is reachable at.
	EndpointURL string `json:"endpointUrl,omitempty"`
	// ConnectedEdges is a live snapshot of currently-connected edges by kind.
	ConnectedEdges map[string]int `json:"connectedEdges,omitempty"`
	// DiscoveryTool names the canonical "list everything reachable" tool.
	// Always "list_targets" for the aggregate endpoint.
	DiscoveryTool string `json:"discoveryTool,omitempty"`
	// Toolsets enumerates the toolset bundles enabled for each kind.
	Toolsets AboutToolsets `json:"toolsets,omitempty"`
	// ReadOnly mirrors spec.readOnly — when true, every mutating tool is
	// either hidden or rejects requests.
	ReadOnly bool `json:"readOnly,omitempty"`
	// HumanReadme is a markdown explainer suitable for rendering in a
	// client's "about this server" panel.  Distinct from Instructions
	// (which is system-prompt context for the model) — this one is for
	// the operator.
	HumanReadme string `json:"humanReadme,omitempty"`
}

// AboutToolsets groups the enabled toolset bundles by kind.
type AboutToolsets struct {
	Kubernetes []string `json:"kubernetes,omitempty"`
	Linux      []string `json:"linux,omitempty"`
}

// Handler returns an http.Handler that serves the aggregate MCP endpoint.
// Uses the SDK's streamable-HTTP transport in stateless mode so each request
// gets a fresh mcp.Server — matches the per-kind handlers and keeps auth
// scoping clean.
//
// Returns an error only for misconfiguration the caller can fix
// (missing Enumerate); transient build failures inside per-request servers
// are surfaced as MCP-level errors instead.
func Handler(cfg Config) (http.Handler, error) {
	if cfg.Enumerate == nil {
		return nil, fmt.Errorf("aggregatemcp.Handler: Config.Enumerate is required")
	}
	return mcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *mcp.Server {
			return newServer(cfg)
		},
		&mcp.StreamableHTTPOptions{Stateless: true},
	), nil
}

// newServer constructs a fresh aggregate mcp.Server with every
// registered ToolFamily's tools + list_targets + the kedge://about
// resource. Tool families live in their respective providers and
// register themselves at init() time (see RegisterToolFamily); the
// aggregator only knows about Names and EdgeTypes here.
//
// The server's Implementation + Instructions come from cfg.Metadata
// so AI clients see kedge-specific branding the moment they connect.
func newServer(cfg Config) *mcp.Server {
	srv := mcp.NewServer(cfg.Metadata.implementation(), &mcp.ServerOptions{
		Instructions: cfg.Metadata.instructions(),
	})
	for _, fam := range RegisteredFamilies() {
		fctx := familyContextFor(cfg, fam)
		fam.Register(srv, fctx)
	}
	registerListTargets(srv, cfg)
	registerAboutResource(srv, cfg)
	// Federate external-provider MCP endpoints. Runs after the in-tree
	// families so collisions (a provider that ships a tool named
	// "list_targets") would manifest as an aggregate-side AddTool
	// duplicate-name error, NOT silently overwrite the platform tool.
	// Background context: stateless mode means each request gets a
	// fresh server; passing context.Background() here lets the proxy
	// outlive the per-request scope for tools/list fetching (handlers
	// receive the per-call ctx independently).
	registerProviderTools(context.Background(), srv, cfg)
	return srv
}

// familyContextFor builds the per-request FamilyContext threaded into
// a ToolFamily.Register call. Edges are pre-filtered to the family's
// EdgeType (the per-request caller does the heavy resolution in
// cfg.EdgesByType); extras and toolsets are looked up by family Name.
func familyContextFor(cfg Config, fam ToolFamily) FamilyContext {
	return FamilyContext{
		Cluster:        cfg.Cluster,
		EdgeNames:      cfg.EdgesByType[fam.EdgeType],
		BearerToken:    cfg.BearerToken,
		HubBaseURL:     cfg.HubBaseURL,
		ReadOnly:       cfg.ReadOnly,
		Toolsets:       cfg.ToolsetsByFamily[fam.Name],
		Deps:           cfg.Deps,
		CallerIdentity: cfg.CallerIdentity,
		Extras:         cfg.ExtrasByFamily[fam.Name],
	}
}

// aboutResourceURI is the canonical URI AI clients pass to `resources/read`
// to fetch the structured AboutDoc.  Stable across versions so prompts and
// downstream MCP clients can hard-code it.
const aboutResourceURI = "kedge://about"

// registerAboutResource exposes the structured AboutDoc as an MCP resource
// at kedge://about so the AI can discover "what is this server" through the
// protocol's first-class discovery mechanism instead of having to parse
// Instructions prose.
func registerAboutResource(srv *mcp.Server, cfg Config) {
	srv.AddResource(&mcp.Resource{
		URI:         aboutResourceURI,
		Name:        "kedge-about",
		Title:       "About this kedge MCP endpoint",
		MIMEType:    "application/json",
		Description: "Structured JSON document describing this endpoint's role, capabilities, tenant context, enabled toolsets, and live edge counts. Read this once on connect to learn what the server can do.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		about := buildAboutSnapshot(ctx, cfg)
		payload, err := json.MarshalIndent(about, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("about resource: %w", err)
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(payload),
			}},
		}, nil
	})
}

// buildAboutSnapshot fills out an AboutDoc using cfg + a live edge
// enumeration so connectedEdges reflects state at read time, not at MCP
// session start.
func buildAboutSnapshot(ctx context.Context, cfg Config) AboutDoc {
	out := cfg.About
	if out.SchemaVersion == "" {
		out.SchemaVersion = "kedge.faros.sh/about/v1"
	}
	if out.Role == "" {
		out.Role = "aggregate"
	}
	if len(out.Capabilities) == 0 {
		out.Capabilities = []string{"kubernetes", "linux"}
	}
	if out.DiscoveryTool == "" {
		out.DiscoveryTool = "list_targets"
	}
	out.ReadOnly = out.ReadOnly || cfg.ReadOnly
	// Surface per-family toolset selections via the structured fields
	// AboutToolsets exposes for known families. New families' selections
	// land in the generic map further down so AI clients can still see
	// what's enabled without an aggregator change.
	if len(out.Toolsets.Kubernetes) == 0 {
		out.Toolsets.Kubernetes = cfg.ToolsetsByFamily["kubernetes"]
	}
	if len(out.Toolsets.Linux) == 0 {
		out.Toolsets.Linux = cfg.ToolsetsByFamily["linux"]
	}
	if cfg.Enumerate != nil {
		kube, linux, err := cfg.Enumerate(ctx)
		if err == nil {
			counts := map[string]int{}
			for _, t := range kube {
				if t.Connected {
					counts["kubernetes"]++
				}
			}
			for _, t := range linux {
				if t.Connected {
					counts["linux"]++
				}
			}
			out.ConnectedEdges = counts
		}
	}
	if out.HumanReadme == "" {
		out.HumanReadme = "# kedge MCP\n\n" +
			"This is the kedge aggregate Model Context Protocol endpoint. " +
			"It exposes every Kubernetes cluster and Linux server an authenticated " +
			"user has access to in their kedge tenant workspace, behind a single MCP URL. " +
			"AI agents should call the `list_targets` tool first to enumerate reachable " +
			"edges, then use `cluster=<name>` for Kubernetes tools or `target=<name>` " +
			"for Linux tools.\n\n" +
			"See https://kedge.faros.sh for project documentation."
	}
	return out
}

// ─── list_targets ───────────────────────────────────────────────────────────

// ListTargetsOutput is the structured payload returned to the AI from a
// list_targets call.
type ListTargetsOutput struct {
	Kubernetes []TargetInfo `json:"kubernetes"`
	Linux      []TargetInfo `json:"linux"`
	// Counts is a convenience summary so the model can decide quickly
	// whether anything is reachable without iterating both slices.
	Counts struct {
		KubernetesConnected int `json:"kubernetesConnected"`
		LinuxConnected      int `json:"linuxConnected"`
		Total               int `json:"total"`
	} `json:"counts"`
}

func registerListTargets(srv *mcp.Server, cfg Config) {
	yes := true
	no := false
	mcp.AddTool(srv, &mcp.Tool{
		Name:  "list_targets",
		Title: "List every kedge edge reachable through this endpoint",
		Description: "List every edge this MCP server can route to. " +
			"Returns each target's name, kind (kubernetes|linux), live " +
			"connection state, and labels.  Use the `type` field to pick " +
			"which tool family applies to a given target.  Always call this " +
			"first when working with an aggregate kedge endpoint — never " +
			"guess target names.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "List every kedge edge reachable through this endpoint",
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &no,
			OpenWorldHint:   &yes,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, ListTargetsOutput, error) {
		kube, linux, err := cfg.Enumerate(ctx)
		if err != nil {
			return nil, ListTargetsOutput{}, fmt.Errorf("list_targets: %w", err)
		}
		out := ListTargetsOutput{Kubernetes: kube, Linux: linux}
		for _, e := range kube {
			if e.Connected {
				out.Counts.KubernetesConnected++
			}
		}
		for _, e := range linux {
			if e.Connected {
				out.Counts.LinuxConnected++
			}
		}
		out.Counts.Total = len(kube) + len(linux)
		return nil, out, nil
	})
}
