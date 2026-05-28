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

package linuxmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultImpl is the fallback Implementation advertised on `initialize` when
// the caller doesn't override Meta.
var defaultImpl = &mcp.Implementation{
	Name:    "kedge-linux-mcp",
	Title:   "Kedge — Linux Edge Hub",
	Version: "v0.1.0",
}

// defaultInstructions is the ambient-context blurb Linux MCP servers ship
// when the caller passes Meta.Instructions == "".  Hosts (Claude, Cursor,
// ...) forward this to the LLM so the model knows what kedge endpoint it's
// talking to before invoking any tool.
const defaultInstructions = `This is a kedge Linux MCP endpoint.  It runs shell-style tools over SSH against the server-type edges this LinuxMCP CR selects.

  - Use the "target" argument on any tool to pick a specific edge.  Omit it and the first connected edge is used.
  - Read-only tools (run_command, read_file, list_dir, stat_path, systemd_unit_status, ...) are always available.
  - Mutating tools (write_file, systemctl lifecycle, pkg install/remove) are gated by spec.readOnly.

For Kubernetes edges, use the kedge KubernetesMCP endpoint instead.  For a single endpoint covering both kinds of edges with a "list_targets" discovery tool, use the kedge aggregate MCPServer endpoint.`

// Meta lets callers override the Implementation + Instructions returned on
// `initialize` and supplies the structured AboutDoc served at kedge://about.
// Zero value falls back to defaultImpl + defaultInstructions, with the
// about resource synthesised from sensible defaults (role=linux).
type Meta struct {
	Name         string
	Title        string
	Version      string
	WebsiteURL   string
	Instructions string
	// About is the structured payload served at kedge://about.  It's the
	// machine-readable counterpart to Instructions and lets AI clients
	// pull a fresh JSON snapshot of role / tenant / capabilities / live
	// edge count via `resources/read` instead of parsing prose.  Zero
	// value triggers the handler-side defaults.
	About AboutDoc
}

// AboutDoc is the JSON document served from the `kedge://about` MCP resource
// on a LinuxMCP endpoint.  Mirrors the aggregate AboutDoc shape so AI
// clients can use the same parser across kedge endpoints.
type AboutDoc struct {
	SchemaVersion  string         `json:"schemaVersion"`
	Role           string         `json:"role"`
	Capabilities   []string       `json:"capabilities"`
	Tenant         string         `json:"tenant,omitempty"`
	LinuxMCP       string         `json:"linuxMCP,omitempty"`
	EndpointURL    string         `json:"endpointUrl,omitempty"`
	ConnectedEdges map[string]int `json:"connectedEdges,omitempty"`
	Toolsets       []string       `json:"toolsets,omitempty"`
	ReadOnly       bool           `json:"readOnly,omitempty"`
	HumanReadme    string         `json:"humanReadme,omitempty"`
}

func (m Meta) implementation() *mcp.Implementation {
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

func (m Meta) instructions() string {
	if m.Instructions != "" {
		return m.Instructions
	}
	return defaultInstructions
}

// Handler builds an http.Handler that serves the LinuxMCP streamable-HTTP
// endpoint.  One Provider == one request; the SDK's StreamableHTTPHandler
// invokes the getServer callback per request.
//
// Toolsets are filtered by `enabled` (matching LinuxMCPSpec.Toolsets); an
// empty `enabled` slice enables the default set (currently just "core").
//
// Pass meta to advertise per-endpoint Implementation + Instructions; zero
// value falls back to defaults.  Callers should set meta to give AI clients
// instance-specific context (which tenant, which LinuxMCP CR, etc.).
func Handler(p *Provider, enabled []string, meta Meta) http.Handler {
	if len(enabled) == 0 {
		enabled = []string{"core"}
	}
	return mcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *mcp.Server {
			return newServer(p, enabled, meta)
		},
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
}

// newServer constructs a fresh mcp.Server and registers the requested
// toolsets against it.  Returning a fresh server per request matches the
// stateless model used by kube-mcp.
func newServer(p *Provider, enabled []string, meta Meta) *mcp.Server {
	srv := mcp.NewServer(meta.implementation(), &mcp.ServerOptions{
		Instructions: meta.instructions(),
	})
	for _, name := range enabled {
		if reg, ok := registry[name]; ok {
			reg(srv, p)
		}
	}
	registerAbout(srv, p, enabled, meta)
	return srv
}

// aboutURI is the canonical kedge metadata resource exposed by every linux
// MCP endpoint.  Stable so AI clients can hard-code the resource lookup.
const aboutURI = "kedge://about"

// registerAbout adds the kedge://about resource so AI clients can fetch
// structured JSON metadata about this LinuxMCP endpoint (role, tenant,
// toolsets, live edge count) without parsing the free-form Instructions.
func registerAbout(srv *mcp.Server, p *Provider, enabled []string, meta Meta) {
	srv.AddResource(&mcp.Resource{
		URI:         aboutURI,
		Name:        "kedge-about",
		Title:       "About this kedge Linux MCP endpoint",
		MIMEType:    "application/json",
		Description: "Structured JSON describing this LinuxMCP endpoint's role (always 'linux'), tenant, enabled toolsets, read-only state, and a live count of connected server-type edges.",
	}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		doc := buildAboutSnapshot(p, enabled, meta)
		body, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("kedge://about: marshal: %w", err)
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      aboutURI,
				MIMEType: "application/json",
				Text:     string(body),
			}},
		}, nil
	})
}

// buildAboutSnapshot merges the caller-supplied Meta.About with sensible
// defaults derived from the Provider + toolset list.  Connected-edge count
// is taken from the Provider's resolved edge set at read time.
func buildAboutSnapshot(p *Provider, enabled []string, meta Meta) AboutDoc {
	doc := meta.About
	if doc.SchemaVersion == "" {
		doc.SchemaVersion = "kedge.faros.sh/about/v1"
	}
	if doc.Role == "" {
		doc.Role = "linux"
	}
	if len(doc.Capabilities) == 0 {
		doc.Capabilities = []string{"linux", "ssh"}
	}
	if len(doc.Toolsets) == 0 {
		if len(enabled) > 0 {
			doc.Toolsets = enabled
		} else {
			doc.Toolsets = []string{"core"}
		}
	}
	if p != nil && doc.ConnectedEdges == nil {
		doc.ConnectedEdges = map[string]int{"linux": len(p.Targets())}
	}
	if doc.HumanReadme == "" {
		doc.HumanReadme = "# kedge Linux MCP\n\n" +
			"This MCP endpoint runs shell-style tools over SSH against every " +
			"server-type kedge edge the backing LinuxMCP CR's edgeSelector " +
			"matches. Use the `target` argument on any tool to pick a specific " +
			"edge. For Kubernetes clusters use the kedge KubernetesMCP endpoint; " +
			"for a single endpoint covering both kinds, use the kedge aggregate " +
			"MCPServer endpoint."
	}
	return doc
}

// ToolsetRegistrar wires a named bundle of tools onto an mcp.Server.
// Toolsets register themselves in init() via Register().
type ToolsetRegistrar func(srv *mcp.Server, p *Provider)

var registry = map[string]ToolsetRegistrar{}

// Register adds a toolset under `name`.  Intended to be called from init().
func Register(name string, fn ToolsetRegistrar) {
	registry[name] = fn
}

// ToolsetNames returns the registered toolset names (for validation /
// discovery, e.g. in the controller).
func ToolsetNames() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}

// RegisterTo wires linux toolsets onto an externally-owned mcp.Server.  Used
// by the aggregate MCPServer endpoint to mount linux tools alongside kube
// tools on a single shared server.  An empty `enabled` slice enables
// ["core"], matching Handler().
func RegisterTo(srv *mcp.Server, p *Provider, enabled []string) {
	if len(enabled) == 0 {
		enabled = []string{"core"}
	}
	for _, name := range enabled {
		if reg, ok := registry[name]; ok {
			reg(srv, p)
		}
	}
}
