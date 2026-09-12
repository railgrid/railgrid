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
// edges, agents — all first-class provider binaries) has its
// tools proxied into this aggregate at request-build time. There is no
// in-process tool registry and no edge-specific machinery here — edges
// register the same way every other provider does.
//
// Flow (per MCP request):
//   1. the handler verifies the bearer and resolves the caller's tenant
//   2. buildServer calls the enumerator with that verified Caller; it returns
//      the live Ready set visible to the caller's Org (see RegistryEnumerator)
//   3. for each provider with an MCP URL:
//      a. POST tools/list to {MCPURL} — a platform provider with the caller's
//         bearer; an org-owned provider through its Transport, which carries a
//         delegated token instead and never the caller's bearer
//      b. for each tool, register a proxy tool "<provider>__<original>" whose
//         handler POSTs tools/call back to {MCPURL} the same way
//   4. name collisions across providers are prevented by the slug prefix, and
//      within one Org by shadowing: an org's own provider replaces the
//      platform provider of the same name, so at most one of them is listed.

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

	// OrgUUID is the owning Org of an org-owned ("bring your own") provider;
	// empty for a platform provider. An org-owned target is only ever reached
	// through Transport: one without a Transport is not contacted at all.
	OrgUUID string
	// Transport, when set, carries every request to this target and owns its
	// authorization: the federation client never attaches the caller's bearer
	// to a request that goes through it. Org-owned targets get one from
	// providers.ProviderProxy.OrgProviderRoute (edge hop + delegated token).
	// Nil for a platform provider, which is dialled directly with the caller's
	// bearer as before.
	Transport http.RoundTripper
}

// ProviderEnumerator returns the live set of Ready providers exposing an MCP
// endpoint that the verified caller may see. Called once per MCP request from
// buildServer with the Caller its BearerVerifier returned, never with anything
// taken from request headers.
type ProviderEnumerator func(ctx context.Context, caller Caller) []ProviderTarget

// providerDiscoveryTimeout bounds how long the aggregate waits on ONE
// provider's tools/list. Discovery runs in parallel, so a slow or hung
// provider costs at most this long and never blocks the healthy ones — it
// just drops out of this tools/list and reappears on the next one once it
// recovers. A var (not a const) only so tests can lower it.
var providerDiscoveryTimeout = 8 * time.Second

const (
	// providerMCPDiscoveryTimeout bounds an individual provider MCP request
	// used for discovery. registerProviderTools also applies the shorter
	// providerDiscoveryTimeout per-provider deadline around discovery, while
	// this bound protects direct callers such as FederatedInstructions.
	providerMCPDiscoveryTimeout = 15 * time.Second
	// providerMCPCallTimeout allows bounded long-running provider actions (for
	// example, a warehouse-backed query) without allowing one call to hang the
	// aggregate indefinitely. It remains below the App Studio request budget.
	providerMCPCallTimeout = 90 * time.Second
	// providerMCPMaxResponseBytes caps one provider MCP response body. It is
	// sized for tool results that carry base64-encoded binary files (a 48 MiB
	// checkout grows by 4/3 when base64-encoded, plus JSON framing), so the
	// cap is 96 MiB. A response over the cap is a loud error, never a
	// silently truncated body that later fails to decode.
	providerMCPMaxResponseBytes int64 = 96 << 20
)

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
	cli := newProviderMCPClient(bearerToken, cluster)

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
		tc, ok := cli.forTarget(p)
		if !ok {
			out[i].Error = "org-owned provider has no delegated transport"
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
			tools, err := tc.listTools(dctx, p.MCPURL)
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
	cli := newProviderMCPClient(bearerToken, cluster)
	parts := make([]string, len(targets))
	var wg sync.WaitGroup
	for i := range targets {
		p := targets[i]
		if p.MCPURL == "" {
			continue
		}
		tc, ok := cli.forTarget(p)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(i int, p ProviderTarget) {
			defer wg.Done()
			defer func() { _ = recover() }()
			dctx, cancel := context.WithTimeout(ctx, providerDiscoveryTimeout)
			defer cancel()
			instr := strings.TrimSpace(tc.fetchInstructions(dctx, p.MCPURL))
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
	params := json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"faros-aggregate","version":"v1"}}`)
	body, err := c.rpc(ctx, mcpURL, "initialize", params, c.discoveryTimeout)
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
	// MCPServer URL. It is the tenant's identity towards providers: the
	// federation client forwards it as BOTH X-Faros-Tenant and X-Faros-Cluster,
	// the same pair the hub backend proxy injects on /services/providers/*
	// (this federation path POSTs directly, so it sets them itself).
	cli := newProviderMCPClient(bearerToken, cluster)

	results := make([]*providerTools, len(targets))
	var wg sync.WaitGroup
	for i := range targets {
		p := targets[i]
		if p.MCPURL == "" {
			continue
		}
		tc, ok := cli.forTarget(p)
		if !ok {
			log.V(1).Info("provider federation: org-owned provider has no delegated transport (skipping)", "provider", p.Name, "org", p.OrgUUID)
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
			tools, err := tc.listTools(dctx, p.MCPURL)
			if err != nil {
				if errors.Is(err, errNoMCPEndpoint) {
					log.V(2).Info("provider federation: no MCP endpoint (skipping)", "provider", p.Name)
				} else {
					log.Info("provider federation: tools/list failed (skipping)", "provider", p.Name, "org", p.OrgUUID, "mcpURL", p.MCPURL, "err", err.Error())
				}
				return
			}
			results[i] = &providerTools{provider: p, tools: tools, client: tc}
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
				registerOneProxyTool(srv, r.client, r.provider, t)
			}()
		}
	}
}

// providerTools is one provider's discovered tool set, carried from the
// concurrent discovery fan-out to the sequential registration pass.
type providerTools struct {
	provider ProviderTarget
	tools    []discoveredTool
	// client is the per-target federation client discovery used; tools/call
	// goes out the same way (same transport, same credential rule).
	client *providerMCPClient
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
	// clusterID is the tenant workspace's kcp logical-cluster ID, forwarded
	// as both X-Faros-Tenant and X-Faros-Cluster. Workspace paths are never
	// sent: the ID is the only tenant identity a provider receives.
	clusterID        string
	discoveryTimeout time.Duration
	callTimeout      time.Duration
	// maxResponseBytes bounds one response body; see
	// providerMCPMaxResponseBytes. A field so tests can lower it.
	maxResponseBytes int64
}

func newProviderMCPClient(bearerToken, clusterID string) *providerMCPClient {
	return newProviderMCPClientWithTimeouts(
		bearerToken,
		clusterID,
		providerMCPDiscoveryTimeout,
		providerMCPCallTimeout,
	)
}

// newProviderMCPClientWithTimeouts constructs a federation client with
// operation-specific bounds. Keeping the durations on the client makes the
// timeout policy explicit and lets tests use short deterministic deadlines
// without changing production defaults or global state.
func newProviderMCPClientWithTimeouts(bearerToken, clusterID string, discoveryTimeout, callTimeout time.Duration) *providerMCPClient {
	return &providerMCPClient{
		http:             &http.Client{},
		bearerToken:      bearerToken,
		clusterID:        clusterID,
		discoveryTimeout: discoveryTimeout,
		callTimeout:      callTimeout,
		maxResponseBytes: providerMCPMaxResponseBytes,
	}
}

// forTarget returns the client to use for one target. A platform target
// (no OrgUUID, no Transport) gets c unchanged: dialled directly with the
// caller's bearer, exactly as before org-owned providers were federated.
//
// A target with a Transport gets a copy whose bearer is cleared and whose HTTP
// client uses that Transport, so the caller's credential is not even present
// on the request the transport sees — the transport sets the delegated token
// itself. An org-owned target WITHOUT a Transport is refused (ok=false): the
// only way to reach one is the edge route with a delegated token, and the
// alternative — the default transport with the caller's bearer, aimed at an
// address inside a tenant's cluster — is the leak this whole path exists to
// prevent.
func (c *providerMCPClient) forTarget(p ProviderTarget) (*providerMCPClient, bool) {
	if p.Transport == nil {
		if p.OrgUUID != "" {
			return nil, false
		}
		return c, true
	}
	tc := *c
	tc.bearerToken = ""
	tc.http = &http.Client{Transport: p.Transport}
	return &tc, true
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
	body, err := c.rpc(ctx, mcpURL, "tools/list", json.RawMessage(`{}`), c.discoveryTimeout)
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
	body, err := c.rpc(ctx, mcpURL, "tools/call", paramsJSON, c.callTimeout)
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
func (c *providerMCPClient) rpc(ctx context.Context, mcpURL, method string, paramsJSON json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
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
	if c.clusterID != "" {
		req.Header.Set("X-Faros-Tenant", c.clusterID)
		req.Header.Set("X-Faros-Cluster", c.clusterID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", mcpURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	limit := c.maxResponseBytes
	if limit <= 0 {
		limit = providerMCPMaxResponseBytes
	}
	// Read one byte past the cap so an oversized body is detected instead of
	// being truncated into a confusing JSON decode error.
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		// No usable MCP endpoint at this path: 404 = no route (a provider that
		// only consumes MCP); 405 = a route exists but doesn't accept the
		// streamable-HTTP POST (e.g. app-studio's mux). Not an error — the
		// provider contributes no tools and is dropped silently.
		return nil, errNoMCPEndpoint
	}
	if int64(len(respBytes)) > limit {
		return nil, fmt.Errorf("provider %s response exceeds the %s limit; the result is too large to federate", method, formatByteLimit(limit))
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

// formatByteLimit renders a byte cap for error messages ("96 MiB", or plain
// bytes when the cap is not a whole number of MiB).
func formatByteLimit(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d-byte", n)
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
