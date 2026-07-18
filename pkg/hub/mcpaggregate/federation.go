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

package mcpaggregate

// Provider federation is the ONLY seam the aggregate has: every Ready
// provider that exposes its own MCP endpoint (kuery, code, infrastructure,
// and now the edges provider — all first-class provider binaries) has its
// tools proxied into this aggregate at request-build time. There is no
// in-process tool registry and no edge-specific machinery here — edges
// register the same way every other provider does.
//
// Flow (per MCP request):
//   1. buildServer calls registerProviderTools
//   2. the enumerator returns the live Ready set from the hub's provider Registry
//   3. for each provider with an MCP URL:
//      a. POST tools/list to {MCPURL} with the caller's bearer
//      b. for each tool, register a proxy tool "<provider>__<original>" whose
//         handler POSTs tools/call back to {MCPURL}
//   4. name collisions across providers are prevented by the slug prefix.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// errNoMCPEndpoint marks a provider that returns 404 for its /mcp path — it
// exposes no MCP server (it consumes MCP or has none). Callers drop it from the
// aggregate silently rather than reporting a federation failure.
var errNoMCPEndpoint = errors.New("provider exposes no MCP endpoint")

// ProviderTarget is one federation target: a Ready provider and the URL of
// its own MCP endpoint.
type ProviderTarget struct {
	Name        string
	DisplayName string
	MCPURL      string
}

// ProviderEnumerator returns the live set of Ready providers exposing an MCP
// endpoint. Called once per MCP request from registerProviderTools.
type ProviderEnumerator func(ctx context.Context) []ProviderTarget

// providerDiscoveryTimeout bounds how long the aggregate waits on ONE
// provider's tools/list. Discovery runs in parallel, so a slow or hung
// provider costs at most this long and never blocks the healthy ones — it
// just drops out of this tools/list and reappears on the next one once it
// recovers. A var (not a const) only so tests can lower it.
var providerDiscoveryTimeout = 8 * time.Second

// FederatedProvider is the introspection view of one federation target: a Ready
// provider, whether its MCP endpoint answered, and the tools it advertised. It
// is what the portal renders so a user can see what the aggregate is federating
// live, without connecting an MCP client.
type FederatedProvider struct {
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName,omitempty"`
	MCPURL      string          `json:"mcpURL"`
	Reachable   bool            `json:"reachable"`
	Error       string          `json:"error,omitempty"`
	Tools       []FederatedTool `json:"tools"`

	// noMCP flags a provider that returned 404 (no MCP endpoint); such entries
	// are filtered out of the result rather than serialized.
	noMCP bool
}

// FederatedTool is one tool advertised by a federated provider. Names are the
// provider-local names; the aggregate prefixes them "<provider>__" when it
// proxies, but introspection shows the raw names the provider reports.
type FederatedTool struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// DiscoverFederation runs the same concurrent tools/list discovery the aggregate
// performs at request-build time, but returns a structured snapshot instead of
// registering proxy tools. It never fails as a whole: an unreachable provider is
// reported with Reachable=false and a populated Error, so the UI can show
// partial state. Providers are returned in enumeration order (deterministic).
func DiscoverFederation(ctx context.Context, targets []ProviderTarget, bearerToken, cluster string) []FederatedProvider {
	out := make([]FederatedProvider, len(targets))
	cli := newProviderMCPClient(bearerToken, cluster, cluster)

	var wg sync.WaitGroup
	for i := range targets {
		p := targets[i]
		out[i] = FederatedProvider{
			Name:        p.Name,
			DisplayName: p.DisplayName,
			MCPURL:      p.MCPURL,
			Tools:       []FederatedTool{},
		}
		if p.MCPURL == "" {
			out[i].Error = "provider exposes no MCP endpoint"
			continue
		}
		wg.Add(1)
		go func(i int, p ProviderTarget) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					out[i].Error = fmt.Sprintf("discovery panic: %v", r)
				}
			}()
			dctx, cancel := context.WithTimeout(ctx, providerDiscoveryTimeout)
			defer cancel()
			tools, err := cli.listTools(dctx, p.MCPURL)
			if err != nil {
				if errors.Is(err, errNoMCPEndpoint) {
					out[i].noMCP = true
					return
				}
				out[i].Error = err.Error()
				return
			}
			ft := make([]FederatedTool, 0, len(tools))
			for _, t := range tools {
				ft = append(ft, FederatedTool{Name: t.Name, Title: t.Title, Description: t.Description})
			}
			out[i].Reachable = true
			out[i].Tools = ft
		}(i, p)
	}
	wg.Wait()

	// Drop providers that expose no MCP endpoint — they contribute nothing and
	// shouldn't clutter the federated list.
	filtered := out[:0]
	for _, p := range out {
		if p.noMCP {
			continue
		}
		filtered = append(filtered, p)
	}
	return filtered
}

// FederatedInstructions fetches each provider's server-level MCP instructions
// (from `initialize`) in parallel and returns a merged block to append to the
// aggregate's own instructions — so operator-authored provider guidance (e.g. a
// Home Assistant Service's spec.instructions describing its entity layout)
// reaches the model connecting to the aggregate, not just the provider's direct
// endpoint. Providers with no instructions or an error contribute nothing.
// Enumeration order is preserved for deterministic output.
func FederatedInstructions(ctx context.Context, targets []ProviderTarget, bearerToken, cluster string) string {
	cli := newProviderMCPClient(bearerToken, cluster, cluster)
	parts := make([]string, len(targets))
	var wg sync.WaitGroup
	for i := range targets {
		p := targets[i]
		if p.MCPURL == "" {
			continue
		}
		wg.Add(1)
		go func(i int, p ProviderTarget) {
			defer wg.Done()
			defer func() { _ = recover() }()
			dctx, cancel := context.WithTimeout(ctx, providerDiscoveryTimeout)
			defer cancel()
			instr := strings.TrimSpace(cli.fetchInstructions(dctx, p.MCPURL))
			if instr == "" {
				return
			}
			label := p.DisplayName
			if label == "" {
				label = p.Name
			}
			parts[i] = fmt.Sprintf("## %s\n%s", label, instr)
		}(i, p)
	}
	wg.Wait()
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n\n")
}

// fetchInstructions returns a provider's server-level MCP instructions from its
// `initialize` response, or "" if it has none or the call fails.
func (c *providerMCPClient) fetchInstructions(ctx context.Context, mcpURL string) string {
	params := json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"kedge-aggregate","version":"v1"}}`)
	body, err := c.rpc(ctx, mcpURL, "initialize", params)
	if err != nil {
		return ""
	}
	var out struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return ""
	}
	return out.Instructions
}

// registerProviderTools fetches each Ready provider's tools/list and registers
// them on srv as proxies. Errors against any one provider are logged + skipped
// — one broken provider must not poison the whole aggregate. Discovery is
// fanned out concurrently with a per-provider deadline. The aggregate stays
// stateless: a fresh server is built per request, so a provider that just
// became Ready shows up on the very next tools/list from the client.
func registerProviderTools(ctx context.Context, srv *mcp.Server, log logr.Logger, targets []ProviderTarget, bearerToken, cluster string) {
	log.Info("provider federation: enumerated", "count", len(targets))
	if len(targets) == 0 {
		return
	}

	// cluster is the workspace's kcp logical-cluster ID parsed off the
	// MCPServer URL. The federation client forwards it as BOTH X-Kedge-Tenant
	// and X-Kedge-Cluster so the provider sees the same identity headers it
	// would have received via the hub backend proxy (that proxy injects them
	// on /services/providers/*, but this federation path POSTs directly).
	cli := newProviderMCPClient(bearerToken, cluster, cluster)

	results := make([]*providerTools, len(targets))
	var wg sync.WaitGroup
	for i := range targets {
		p := targets[i]
		if p.MCPURL == "" {
			continue
		}
		wg.Add(1)
		go func(i int, p ProviderTarget) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Info("provider federation: discovery panic recovered", "provider", p.Name, "panic", fmt.Sprint(r))
				}
			}()
			dctx, cancel := context.WithTimeout(ctx, providerDiscoveryTimeout)
			defer cancel()
			tools, err := cli.listTools(dctx, p.MCPURL)
			if err != nil {
				if errors.Is(err, errNoMCPEndpoint) {
					log.V(2).Info("provider federation: no MCP endpoint (skipping)", "provider", p.Name)
				} else {
					log.Info("provider federation: tools/list failed (skipping)", "provider", p.Name, "mcpURL", p.MCPURL, "err", err.Error())
				}
				return
			}
			results[i] = &providerTools{provider: p, tools: tools}
		}(i, p)
	}
	wg.Wait()

	// Register sequentially, in the original provider order, so the aggregate
	// tool list is deterministic across requests. AddTool on a shared server
	// is not guaranteed goroutine-safe, so registration stays on this goroutine.
	for _, r := range results {
		if r == nil {
			continue
		}
		log.Info("provider federation: registering tools", "provider", r.provider.Name, "count", len(r.tools))
		for _, t := range r.tools {
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						log.Info("provider federation: AddTool panic recovered", "provider", r.provider.Name, "tool", t.Name, "panic", fmt.Sprint(rec))
					}
				}()
				registerOneProxyTool(srv, cli, r.provider, t)
			}()
		}
	}
}

// providerTools is one provider's discovered tool set, carried from the
// concurrent discovery fan-out to the sequential registration pass.
type providerTools struct {
	provider ProviderTarget
	tools    []discoveredTool
}

// registerOneProxyTool installs a single proxy tool on srv, named
// "<provider>__<original>" so a model browsing tools/list can see which
// provider owns which tool.
func registerOneProxyTool(srv *mcp.Server, cli *providerMCPClient, p ProviderTarget, t discoveredTool) {
	proxyName := p.Name + "__" + t.Name

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

// providerMCPClient is a hand-rolled MCP-over-HTTP client just sturdy enough
// for tools/list + tools/call — federation only needs request/response, not
// the SDK client's session/sampling lifecycle machinery.
type providerMCPClient struct {
	http        *http.Client
	bearerToken string
	tenantPath  string // forwarded as X-Kedge-Tenant
	clusterID   string // forwarded as X-Kedge-Cluster
}

func newProviderMCPClient(bearerToken, tenantPath, clusterID string) *providerMCPClient {
	return &providerMCPClient{
		http:        &http.Client{Timeout: 15 * time.Second},
		bearerToken: bearerToken,
		tenantPath:  tenantPath,
		clusterID:   clusterID,
	}
}

// discoveredTool is the subset of mcp.Tool we keep from tools/list. InputSchema
// is kept raw so we don't round-trip through the SDK's schema struct.
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
	paramsJSON, err := json.Marshal(map[string]any{"name": name, "arguments": args})
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

// rpc does one JSON-RPC POST and returns the `result` field. Handles both
// application/json and text/event-stream (SSE `data: {json}`) responses.
func (c *providerMCPClient) rpc(ctx context.Context, mcpURL, method string, paramsJSON json.RawMessage) (json.RawMessage, error) {
	reqBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
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
	// The MCP SDK's streamable handler has DNS-rebinding protection that 403s
	// ("invalid Host header") when the provider listens on loopback (dev) but
	// the request Host isn't loopback — federation POSTs directly to the
	// provider's backend URL (e.g. host.docker.internal:8082), tripping it.
	// The guard is for browser-facing localhost servers; this path is the hub's
	// own authenticated federation, so normalize Host to loopback. In prod the
	// provider listens on a pod IP (non-loopback) and the guard is skipped, so
	// this is a no-op there. (The connection target is still req.URL.Host.)
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}
	if c.tenantPath != "" {
		req.Header.Set("X-Kedge-Tenant", c.tenantPath)
	}
	if c.clusterID != "" {
		req.Header.Set("X-Kedge-Cluster", c.clusterID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", mcpURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		// No usable MCP endpoint at this path: 404 = no route (e.g. agents,
		// which consumes MCP rather than serving it); 405 = a route exists but
		// doesn't accept the streamable-HTTP POST (e.g. app-studio's mux). Not
		// an error — the provider contributes no tools and is dropped silently.
		return nil, errNoMCPEndpoint
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("provider returned %d: %s", resp.StatusCode, snippet(respBytes))
	}

	rawJSON := respBytes
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
