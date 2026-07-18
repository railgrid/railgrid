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

// Package mcpaggregate serves the hub's always-on aggregate MCP endpoint.
//
// This is a base-layer capability of the hub: the endpoint is mounted
// unconditionally at apiurl.PathPrefixMCPServer and always answers, even when
// no providers are registered (it just serves an empty tool list). It never
// depends on edges — edges are a first-class provider that federates its tools
// in exactly like every other provider (kuery, code, infrastructure, …).
//
// Per request the handler parses the tenant cluster + MCPServer name out of the
// path, authenticates the caller's bearer, builds a fresh stateless mcp.Server,
// federates every Ready provider's own /mcp endpoint into it, and serves the
// MCP protocol over streamable HTTP.
package mcpaggregate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
)

// impl is the MCP Implementation advertised on `initialize`.
var impl = &mcp.Implementation{
	Name:    "kedge-mcpserver",
	Title:   "Kedge aggregate MCP",
	Version: "v1alpha1",
}

// Options configures the aggregate handler.
type Options struct {
	// Providers enumerates the live Ready providers to federate. Required.
	Providers ProviderEnumerator
	// ExternalURL is the hub's externally reachable base URL, used only to
	// self-describe the endpoint in the kedge://about resource. Optional.
	ExternalURL string
	// Logger is used for federation diagnostics. Optional.
	Logger logr.Logger
}

// New returns the http.Handler mounted at apiurl.PathPrefixMCPServer. The
// handler expects the prefix to have been stripped, so it sees
// /{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp.
func New(opts Options) http.Handler {
	log := opts.Logger
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, name, ok := parseMCPServerPath(r.URL.Path)
		if !ok {
			http.Error(w, "invalid path: expected /{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp", http.StatusBadRequest)
			return
		}
		token := extractBearer(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Fresh, stateless server per request so a provider that just became
		// Ready shows up on the very next tools/list.
		handler := mcp.NewStreamableHTTPHandler(
			func(req *http.Request) *mcp.Server {
				return buildServer(req.Context(), buildParams{
					cluster:     cluster,
					name:        name,
					token:       token,
					externalURL: opts.ExternalURL,
					enumerate:   opts.Providers,
					log:         log,
				})
			},
			&mcp.StreamableHTTPOptions{Stateless: true},
		)
		handler.ServeHTTP(w, r)
	})
}

type buildParams struct {
	cluster     string
	name        string
	token       string
	externalURL string
	enumerate   ProviderEnumerator
	log         logr.Logger
}

// buildServer constructs the aggregate mcp.Server for one request: generic
// per-tenant metadata, the kedge://about resource, and every Ready provider's
// federated tools. It never fails — with no providers it serves an empty but
// valid MCP server.
func buildServer(ctx context.Context, p buildParams) *mcp.Server {
	title := fmt.Sprintf("Kedge — %s (tenant %s)", p.name, p.cluster)
	instructions := fmt.Sprintf(
		"You are connected to the kedge aggregate MCP endpoint %q in tenant workspace %q.\n\n"+
			"This single endpoint federates the tools of every enabled kedge provider in this tenant "+
			"(for example infrastructure, code, and edge access). Provider tools are namespaced as "+
			"\"<provider>__<tool>\". Call tools/list to enumerate what is currently reachable — the set "+
			"reflects which providers are enabled and healthy right now.",
		p.name, p.cluster,
	)

	var targets []ProviderTarget
	if p.enumerate != nil {
		targets = p.enumerate(ctx)
	}

	// Merge each provider's own instructions (e.g. a Home Assistant Service's
	// operator-authored entity/room guidance) into the aggregate's instructions,
	// so that context reaches the model here — not only on the provider's direct
	// endpoint. Fetched before the server is built (instructions are fixed at
	// construction).
	if extra := FederatedInstructions(ctx, targets, p.token, p.cluster); extra != "" {
		instructions += "\n\n--- Provider guidance ---\n\n" + extra
	}

	srv := mcp.NewServer(impl, &mcp.ServerOptions{Instructions: instructions})

	registerAboutResource(srv, aboutDoc{
		Role:        "aggregate",
		Tenant:      p.cluster,
		MCPServer:   p.name,
		Title:       title,
		EndpointURL: p.externalURL + apiurl.MCPServerPath(p.cluster, p.name),
	})

	registerProviderTools(ctx, srv, p.log, targets, p.token, p.cluster)
	return srv
}

// aboutDoc is the structured self-description served at kedge://about.
type aboutDoc struct {
	Role        string `json:"role"`
	Tenant      string `json:"tenant"`
	MCPServer   string `json:"mcpServer"`
	Title       string `json:"title"`
	EndpointURL string `json:"endpointURL,omitempty"`
}

const aboutResourceURI = "kedge://about"

func registerAboutResource(srv *mcp.Server, about aboutDoc) {
	srv.AddResource(&mcp.Resource{
		URI:         aboutResourceURI,
		Name:        "kedge-about",
		Title:       "About this kedge MCP endpoint",
		MIMEType:    "application/json",
		Description: "Structured JSON describing this endpoint's role, tenant context, and URL. Read once on connect.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		payload, err := json.MarshalIndent(about, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      aboutResourceURI,
				MIMEType: "application/json",
				Text:     string(payload),
			}},
		}, nil
	})
}

// extractBearer pulls the token from an Authorization: Bearer header.
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// parseMCPServerPath extracts cluster + MCPServer name from the path seen after
// the apiurl.PathPrefixMCPServer prefix is stripped.
//
// Expected format:
//
//	/{cluster}/apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp
func parseMCPServerPath(path string) (cluster, name string, ok bool) {
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 8)
	if len(parts) < 7 {
		return "", "", false
	}
	if parts[1] != "apis" || parts[2] != "kedge.faros.sh" || parts[3] != "v1alpha1" ||
		parts[4] != "mcpservers" || parts[6] != "mcp" {
		return "", "", false
	}
	return parts[0], parts[5], true
}

// PathPrefix is the router prefix this handler mounts under. Re-exported for
// the hub server wiring so the prefix and the handler live together.
var PathPrefix = apiurl.PathPrefixMCPServer
