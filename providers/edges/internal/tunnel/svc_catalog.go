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

// This file adapts the shared service catalog (internal/svccatalog) to MCP: it
// registers each catalog type's tools and executes them through the edge
// tunnel. The catalog data (auth styles, ports, tool tables) and the auth
// application (including the qBittorrent/Pi-hole session logins) live in
// svccatalog so the validation reconciler and the /catalog UI endpoint share
// one source of truth. Home Assistant's richer, reshaped tools stay hand-written
// in mcp_service.go.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/provider-edges/internal/haclient"
	"github.com/faroshq/provider-edges/internal/svccatalog"
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

// catToolInput is the single, generic tool input. A tool's description tells the
// model which of these to fill; reads typically need none.
type catToolInput struct {
	Query map[string]string `json:"query,omitempty" jsonschema:"Optional query-string parameters, e.g. {\"query\":\"ubuntu\"} for a search."`
	Form  map[string]string `json:"form,omitempty" jsonschema:"Optional form fields for form-encoded APIs (qBittorrent actions), e.g. {\"urls\":\"magnet:?...\"}."`
	Body  string            `json:"body,omitempty" jsonschema:"Optional raw JSON request body for JSON POST/PUT actions."`
}

// registerCatalogTools installs the MCP tools for a catalog service type.
func (p *Server) registerCatalogTools(srv *mcp.Server, prefix, cluster, kcpToken string, svc *serviceView, dialer haclient.Dialer) {
	def, ok := svccatalog.Get(svc.Spec.Type)
	if !ok {
		return
	}
	for _, t := range def.Tools {
		tool := t // capture
		mcp.AddTool(srv, &mcp.Tool{
			Name:        prefix + tool.Name,
			Description: def.DisplayName + " — " + tool.Desc,
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in catToolInput) (*mcp.CallToolResult, any, error) {
			return p.callCatalogTool(ctx, cluster, kcpToken, svc, dialer, def, tool, in)
		})
	}
}

// callCatalogTool executes one catalog tool: resolve the token, apply the auth
// (svccatalog.Apply), build the request (query/form/body), proxy it through the
// edge, and return the response body verbatim.
func (p *Server) callCatalogTool(ctx context.Context, cluster, kcpToken string, svc *serviceView, dialer haclient.Dialer, def svccatalog.Definition, tool svccatalog.Tool, in catToolInput) (*mcp.CallToolResult, any, error) {
	token, err := p.readServiceToken(ctx, cluster, svc, kcpToken)
	if err != nil {
		return toolErr("service credentials: " + err.Error()), nil, nil
	}
	if token == "" && !def.Credential.Optional {
		return toolErr("no auth token configured for this service (set spec.authSecretRef)"), nil, nil
	}

	// Path parameters: any {key} in the tool path is filled from the query map
	// (e.g. {"siteId":"..."} → /sites/{siteId}/clients); everything else becomes
	// the query string.
	path := tool.Path
	q := url.Values{}
	for k, v := range in.Query {
		if ph := "{" + k + "}"; strings.Contains(path, ph) {
			path = strings.ReplaceAll(path, ph, url.PathEscape(v))
		} else {
			q.Set(k, v)
		}
	}

	// Auth (headers + query) is applied by the shared catalog, which also runs
	// the session-login round-trips for qBittorrent/Pi-hole.
	target := haclient.Target{Scheme: svc.scheme(), Host: svc.targetHost(), Port: svc.Spec.Port}
	header := http.Header{}
	if err := svccatalog.Apply(ctx, dialer, target, def, token, header, q); err != nil {
		return toolErr(err.Error()), nil, nil
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

	resp, err := haclient.DoWith(ctx, dialer, target, tool.Method, path, header, bodyReader)
	if err != nil {
		return toolErr(err.Error()), nil, nil
	}
	defer resp.Body.Close() //nolint:errcheck
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return toolErr(fmt.Sprintf("%s returned %d: %s", def.DisplayName, resp.StatusCode, snippet(respBody))), nil, nil
	}
	// Binary image responses (e.g. a UniFi Protect camera snapshot) are returned
	// as MCP image content so the model can actually see them.
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "image/") {
		return &mcp.CallToolResult{Content: withServiceNote(svc, &mcp.ImageContent{Data: respBody, MIMEType: ct})}, nil, nil
	}
	text := string(respBody)
	if strings.TrimSpace(text) == "" {
		text = fmt.Sprintf("OK (%d)", resp.StatusCode)
	}
	return &mcp.CallToolResult{Content: withServiceNote(svc, &mcp.TextContent{Text: text})}, nil, nil
}
