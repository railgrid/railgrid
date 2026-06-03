/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package aggregatemcp

// External providers (Helm-installed binaries that register with the
// hub via a CatalogEntry, like providers/infrastructure/) can NOT
// participate in the in-tree ToolFamily registry — that's init()-only
// and tied to specific edge types. This file gives them a second seam:
// each Ready provider's own MCP endpoint is FEDERATED into the
// aggregator at request build time.
//
// Flow (per MCP request):
//   1. newServer() calls registerProviderTools(srv, cfg)
//   2. cfg.Providers(ctx) returns the live Ready set from the hub's
//      providers.Registry (wired via builder.Deps.ProviderEnumerator)
//   3. For each provider with an MCPURL:
//      a. POST tools/list to {MCPURL} with the caller's bearer
//      b. For each tool returned, register a proxy tool on srv named
//         "<provider-slug>__<original>" whose handler POSTs tools/call
//         back to {MCPURL}
//   4. Tool name collisions across providers are prevented by the
//      slug prefix; collisions WITHIN a provider would have failed
//      the provider's own MCP server already.
//
// What we deliberately do NOT do (yet):
//   - cache tools/list across requests (it's a single HTTP round-trip
//     per provider; if it becomes a hot spot, add a TTL keyed on
//     provider.Version)
//   - federate resources/* or prompts/* — only tools today

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/pkg/virtual/builder"
)

// ProviderEnumerator returns the live set of Ready providers that
// expose an MCP endpoint. Called once per MCP request from
// registerProviderTools. Type-aliased to builder.ProviderTarget so the
// aggregator stays decoupled from pkg/hub/providers.
type ProviderEnumerator = func(ctx context.Context) []builder.ProviderTarget

// registerProviderTools fetches each Ready provider's tools/list and
// registers them on srv as proxies. Errors against any one provider
// are logged + skipped — one broken provider must not poison the
// whole aggregate. The aggregator stays stateless: a fresh server is
// built per request, so a provider that just became Ready shows up on
// the very next tools/list from the client.
func registerProviderTools(ctx context.Context, srv *mcp.Server, cfg Config) {
	log := klogFromCfg(cfg.Deps)
	if cfg.Providers == nil {
		log.V(2).Info("provider federation skipped: cfg.Providers is nil")
		return
	}
	providers := cfg.Providers(ctx)
	log.Info("provider federation: enumerated", "count", len(providers))
	if len(providers) == 0 {
		return
	}

	// cfg.Cluster is the tenant workspace path (e.g.
	// root:kedge:orgs:<uuid>:<wsUUID>) parsed off the MCPServer URL.
	// The federation client forwards it as X-Kedge-Tenant on every
	// call so the provider sees the same tenant header it would have
	// received via the hub backend proxy. Without this, kro_provision
	// (and any other tenant-scoped tool) 400s with "X-Kedge-Tenant
	// header required" — same failure mode as the UI hit before we
	// fixed the bearer-forwarding bug.
	cli := newProviderMCPClient(cfg.BearerToken, cfg.Cluster)
	for _, p := range providers {
		if !p.Ready || p.MCPURL == "" {
			log.Info("provider federation: skip (not ready or no MCP URL)", "provider", p.Name, "ready", p.Ready, "mcpURL", p.MCPURL)
			continue
		}
		tools, err := cli.listTools(ctx, p.MCPURL)
		if err != nil {
			log.Info("provider federation: tools/list failed (skipping)", "provider", p.Name, "mcpURL", p.MCPURL, "err", err.Error())
			continue
		}
		log.Info("provider federation: registering tools", "provider", p.Name, "count", len(tools))
		for _, t := range tools {
			func() {
				// AddTool can panic on schema validation failures
				// (missing input schema, non-object type, etc).
				// Recover so one bad provider doesn't poison the
				// entire aggregate's tool list.
				defer func() {
					if r := recover(); r != nil {
						log.Info("provider federation: AddTool panic recovered", "provider", p.Name, "tool", t.Name, "panic", fmt.Sprint(r))
					}
				}()
				registerOneProxyTool(srv, cli, p, t)
			}()
		}
	}
}

// registerOneProxyTool installs a single proxy tool on srv. Naming:
// "<provider-slug>__<original>" so a model browsing tools/list can
// see at a glance which provider owns which tool. The double
// underscore is intentional — provider tools typically already use
// single underscore (kro_provision, etc), so the double separator
// keeps the two segments visually distinct.
func registerOneProxyTool(srv *mcp.Server, cli *providerMCPClient, p builder.ProviderTarget, t discoveredTool) {
	proxyName := p.Name + "__" + t.Name

	// Preserve the original tool metadata so the AI client sees the
	// same description / annotations it would see calling the provider
	// directly. Title gets the provider's display name appended for
	// disambiguation in the MCP-server pickers some clients render.
	title := t.Title
	if title == "" {
		title = t.Name
	}
	if p.DisplayName != "" {
		title = title + " — " + p.DisplayName
	}
	tool := &mcp.Tool{
		Name:        proxyName,
		Title:       title,
		Description: t.Description,
		Annotations: t.Annotations,
		InputSchema: t.InputSchema,
	}

	srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Pull the caller's arguments OUT of the proxied tool name and
		// forward to the provider under its original name.
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, fmt.Errorf("decode arguments: %w", err)
			}
		}
		res, err := cli.callTool(ctx, p.MCPURL, t.Name, args)
		if err != nil {
			return nil, fmt.Errorf("provider %q tool %q: %w", p.Name, t.Name, err)
		}
		return res, nil
	})
}

// providerMCPClient is a hand-rolled MCP-over-HTTP client just sturdy
// enough for tools/list + tools/call. We don't reuse the SDK's
// streamable-HTTP client to avoid pulling in its lifecycle machinery
// (session ID negotiation, sampling, etc.) — federation only needs
// fire-and-forget request/response.
type providerMCPClient struct {
	http        *http.Client
	bearerToken string
	tenantPath  string // forwarded as X-Kedge-Tenant on each request
}

func newProviderMCPClient(bearerToken, tenantPath string) *providerMCPClient {
	return &providerMCPClient{
		http: &http.Client{
			Timeout: 15 * time.Second,
			// The hub is on the same machine in dev (Tilt) and same
			// service mesh in prod, so we don't expect TLS errors;
			// upgrade this transport if a provider lives off-cluster
			// behind a self-signed cert.
		},
		bearerToken: bearerToken,
		tenantPath:  tenantPath,
	}
}

// discoveredTool is the subset of mcp.Tool we keep from tools/list.
// We carry InputSchema as raw json.RawMessage so we don't have to
// round-trip through the SDK's schema struct.
type discoveredTool struct {
	Name        string               `json:"name"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	InputSchema json.RawMessage      `json:"inputSchema"`
	Annotations *mcp.ToolAnnotations `json:"annotations,omitempty"`
}

func (c *providerMCPClient) listTools(ctx context.Context, mcpURL string) ([]discoveredTool, error) {
	body, err := c.rpc(ctx, mcpURL, "tools/list", json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []discoveredTool `json:"tools"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode tools/list result: %w", err)
	}
	return out.Tools, nil
}

func (c *providerMCPClient) callTool(ctx context.Context, mcpURL, name string, args map[string]any) (*mcp.CallToolResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	paramsJSON, err := json.Marshal(map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, fmt.Errorf("encode tools/call params: %w", err)
	}
	body, err := c.rpc(ctx, mcpURL, "tools/call", paramsJSON)
	if err != nil {
		return nil, err
	}
	var res mcp.CallToolResult
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("decode tools/call result: %w", err)
	}
	return &res, nil
}

// rpc does one JSON-RPC POST to mcpURL and returns the `result` field
// as raw bytes. Handles BOTH application/json bodies (simple POST
// response) and text/event-stream bodies (the SDK's default
// streamable-HTTP transport response: lines `data: {json}\n\n`).
func (c *providerMCPClient) rpc(ctx context.Context, mcpURL, method string, paramsJSON json.RawMessage) (json.RawMessage, error) {
	reqBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1, // not used downstream — single-shot calls
		"method":  method,
		"params":  paramsJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Accept BOTH so the server picks whichever is cheaper (the SDK
	// defaults to SSE; smaller hand-rolled servers may return JSON).
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}
	// X-Kedge-Tenant is what the hub backend proxy would normally
	// inject. The federation HTTP path bypasses that proxy (we POST
	// directly to the provider's :PORT/mcp), so we replicate the
	// header here. Provider's tenant-scoped tools (kro_provision,
	// kro_list_instances, …) 400 without it.
	if c.tenantPath != "" {
		req.Header.Set("X-Kedge-Tenant", c.tenantPath)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", mcpURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8MB ceiling
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("provider returned %d: %s", resp.StatusCode, snippet(respBytes))
	}

	rawJSON := respBytes
	// SSE? Strip the framing and pick the first `data:` line.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var ok bool
		rawJSON, ok = firstSSEData(respBytes)
		if !ok {
			return nil, fmt.Errorf("no data: line in SSE response")
		}
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rawJSON, &env); err != nil {
		return nil, fmt.Errorf("decode JSON-RPC envelope: %w (body=%s)", err, snippet(rawJSON))
	}
	if env.Error != nil {
		return nil, fmt.Errorf("provider error %d: %s", env.Error.Code, env.Error.Message)
	}
	return env.Result, nil
}

// firstSSEData scans a text/event-stream body for the first `data: …`
// payload. The aggregator's request flow is one-shot (a single
// JSON-RPC method per HTTP POST), so a single data line is what we
// expect from a well-behaved server.
func firstSSEData(body []byte) (json.RawMessage, bool) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "data: ") {
			return json.RawMessage(strings.TrimPrefix(line, "data: ")), true
		}
	}
	return nil, false
}

func snippet(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
