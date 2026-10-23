// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/util/validation"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/resulttypes"
)

// githubMCPEndpoint is the hosted GitHub MCP server; a github Connection with
// a PAT gets its full toolset without any MCP configuration.
const githubMCPEndpoint = "https://api.githubcopilot.com/mcp"

// MCP results can include query pages and other structured data that exceeds
// the chat-sized web fetch budget. Keep the complete result up to this hard
// limit, then reject it rather than passing a misleading partial prefix.
const mcpResultMaxReturn = 64 * 1024

const (
	mcpAppsExtensionID        = "io.modelcontextprotocol/ui"
	mcpAppMIMEType            = "text/html;profile=mcp-app"
	mcpRailgridMetaKey        = "railgrid"
	mcpSourcesMaxBytes        = 32 * 1024
	mcpAppResourceReadTimeout = 10 * time.Second
	mcpAppResourceRequestPeek = 8 * 1024
	mcpAppResourceResponseMax = 2 << 20
)

// MCPSession wraps one live MCP connection's discovered tools. Close after
// the run completes.
type MCPSession struct {
	Tools []engine.Tool
	// Instructions is the server's ambient guidance from the MCP `initialize`
	// response (e.g. an edges Service's spec.instructions describing its entity
	// layout). The caller folds it into the agent's system context.
	Instructions string
	session      *mcp.ClientSession
	hubAggregate bool
	appResources map[string]resulttypes.MCPAppReference
}

func (s *MCPSession) Close() {
	if s != nil && s.session != nil {
		_ = s.session.Close()
	}
}

// ConnectMCP dials a Connection of type mcp or github, lists its tools, and
// exposes each as an engine tool named <connection>__<tool>. Errors are
// returned (not fatal to the run) so a dead MCP server degrades to "tools
// missing" rather than breaking chat.
func ConnectMCP(ctx context.Context, d Deps, conn *agentsv1alpha1.Connection) (*MCPSession, error) {
	// An instance-backed connection names a workload provisioned by the
	// infrastructure provider and is reached over the platform's internal data
	// plane — no public hostname, and authorized as THIS PROVIDER through the
	// instances/proxy claim on its own export, not by a credential of its own.
	// The verb root IS the MCP endpoint: the template pins the upstream path
	// (the browser template pins /mcp), so nothing is appended here.
	if instance, resource := instanceRef(conn, browserResource); instance != "" {
		endpoint, err := d.DataPlane.ProxyURL(ctx, "mcp", conn.Name, resource, instance, "")
		if err != nil {
			return nil, err
		}
		client, err := d.DataPlane.HTTPClient()
		if err != nil {
			return nil, err
		}
		return ConnectMCPEndpointWithClient(ctx, endpoint, client, conn.Name)
	}
	endpoint := strings.TrimSpace(conn.Spec.BaseURL)
	if endpoint == "" {
		if conn.Spec.Type == agentsv1alpha1.ConnectionTypeGitHub {
			endpoint = githubMCPEndpoint
		} else {
			return nil, fmt.Errorf("mcp connection %q names neither an instance nor a baseURL — point it at a browser instance in this workspace, or at an external MCP server's URL", conn.Name)
		}
	}
	return ConnectMCPEndpoint(ctx, endpoint, d.connToken(ctx, conn.Name), conn.Name, false)
}

// browserResource is the flattened infrastructure instance resource — every
// template's instances (the browser included) are served as
// instances.infrastructure.railgrid.ai. config.instanceResource overrides it
// for a provider serving a different resource.
const browserResource = "instances"

// ConnectMCPEndpoint dials an arbitrary MCP server over streamable HTTP with
// an optional bearer token and exposes its tools as <prefix>__<tool>. Used by
// connection-backed families and the edges family (the hub's aggregate MCP
// virtual endpoint, dialed as the calling user).
func ConnectMCPEndpoint(ctx context.Context, endpoint, bearer, prefix string, insecureTLS bool) (*MCPSession, error) {
	return connectMCPEndpoint(ctx, endpoint, bearer, prefix, insecureTLS, false)
}

// ConnectHubAggregateMCPEndpoint dials the Hub's authenticated aggregate MCP
// endpoint. Only this explicit path advertises MCP Apps support because the
// Hub namespaces provider resources before exposing them to callers.
func ConnectHubAggregateMCPEndpoint(ctx context.Context, endpoint, bearer, prefix string, insecureTLS bool) (*MCPSession, error) {
	return connectMCPEndpoint(ctx, endpoint, bearer, prefix, insecureTLS, true)
}

func connectMCPEndpoint(ctx context.Context, endpoint, bearer, prefix string, insecureTLS, hubAggregate bool) (*MCPSession, error) {
	base := http.DefaultTransport
	if insecureTLS {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev hubs use self-signed certs; opt-in
		base = t
	}
	httpClient := &http.Client{Timeout: 60 * time.Second, Transport: base}
	if bearer != "" {
		httpClient.Transport = &bearerTransport{token: bearer, base: base}
	}
	return connectMCPEndpointWithClient(ctx, endpoint, httpClient, prefix, hubAggregate)
}

// ConnectMCPEndpointWithClient is ConnectMCPEndpoint with the HTTP client
// supplied: the way an instance-backed connection is dialed, where the client
// authenticates as this provider (DataPlane.HTTPClient) rather than with a
// bearer of the connection's own.
func ConnectMCPEndpointWithClient(ctx context.Context, endpoint string, httpClient *http.Client, prefix string) (*MCPSession, error) {
	return connectMCPEndpointWithClient(ctx, endpoint, httpClient, prefix, false)
}

// ConnectHubAggregateMCPEndpointWithClient is the explicit trusted-Hub form of
// ConnectMCPEndpointWithClient. Ordinary connection callers must use the
// latter, which never advertises MCP Apps or fetches UI resources.
func ConnectHubAggregateMCPEndpointWithClient(ctx context.Context, endpoint string, httpClient *http.Client, prefix string) (*MCPSession, error) {
	return connectMCPEndpointWithClient(ctx, endpoint, httpClient, prefix, true)
}

func connectMCPEndpointWithClient(ctx context.Context, endpoint string, httpClient *http.Client, prefix string, hubAggregate bool) (*MCPSession, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("connecting to MCP server %q: no HTTP client", prefix)
	}
	if hubAggregate {
		client := *httpClient
		base := client.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		client.Transport = &mcpAppResourceResponseLimitTransport{base: base}
		httpClient = &client
	}
	var options *mcp.ClientOptions
	if hubAggregate {
		options = &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{Extensions: map[string]any{
			mcpAppsExtensionID: map[string]any{"mimeTypes": []string{mcpAppMIMEType}},
		}}}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "railgrid-agents", Version: "0.1.0"}, options)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to MCP server %q: %w", prefix, err)
	}

	listed, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("listing tools on %q: %w", prefix, err)
	}

	out := &MCPSession{session: session, hubAggregate: hubAggregate, appResources: map[string]resulttypes.MCPAppReference{}}
	if ir := session.InitializeResult(); ir != nil {
		out.Instructions = strings.TrimSpace(ir.Instructions)
	}
	conn := struct{ Name string }{Name: prefix}
	for _, t := range listed.Tools {
		if hubAggregate && !mcpToolVisibleToAgent(t.Meta) {
			continue
		}
		if hubAggregate {
			if ref, ok := hubMCPAppIdentity(t.Meta); ok {
				out.appResources[ref.ResourceURI] = ref
			}
		}
		toolName := t.Name
		full := conn.Name + "__" + toolName
		var js map[string]any
		if t.InputSchema != nil {
			if raw, err := json.Marshal(t.InputSchema); err == nil {
				_ = json.Unmarshal(raw, &js)
			}
		}
		out.Tools = append(out.Tools, engine.Tool{
			Name:       full,
			Desc:       clip(t.Description, 1000),
			JSONSchema: js,
			// Rich executor: MCP tools can return images (e.g. a UniFi Protect
			// snapshot), which the engine feeds to vision-capable models.
			ExecRich: func(ctx context.Context, argsJSON string) (engine.Observation, error) {
				args, err := decodeMCPToolArguments(argsJSON)
				if err != nil {
					return engine.Observation{}, fmt.Errorf("invalid arguments: %w", err)
				}
				res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: args})
				if err != nil {
					return engine.Observation{}, err
				}
				text, images := mcpResultParts(res)
				if res.IsError {
					return engine.Observation{}, fmt.Errorf("%s", clip(text, 2000))
				}
				// Preserve the complete text result. StructuredContent is decoded
				// through any by the SDK and can round large integer values.
				if len(text) > mcpResultMaxReturn {
					return engine.Observation{}, fmt.Errorf(
						"MCP tool returned %d bytes (limit %d); result withheld, not truncated, and not passed to the model. The tool already executed, so do not repeat side-effecting calls. For read-only queries, narrow filters or use a smaller page if supported",
						len(text), mcpResultMaxReturn,
					)
				}
				var sources []resulttypes.SourceReference
				var presentation *resulttypes.ProviderPresentation
				if hubAggregate {
					sources, err = mcpResultSources(res.Meta)
					if err != nil {
						return engine.Observation{}, fmt.Errorf("MCP tool returned invalid Railgrid source metadata: %w", err)
					}
					presentation = mcpResultPresentation(res.Meta)
				}
				// Only the explicit aggregate path may ask the Hub to resolve a
				// federated provider resource. A source claim is required so app
				// HTML stays coupled to later caller revalidation.
				if hubAggregate && len(sources) > 0 {
					if ref, ok := hubMCPAppIdentity(t.Meta); ok {
						if app, err := out.ReadHubAggregateMCPAppResource(ctx, ref); err == nil {
							if presentation == nil {
								presentation = &resulttypes.ProviderPresentation{Version: 1}
							}
							ref.SHA256 = app.SHA256
							presentation.MCPAppReference = &ref
						}
					}
				}
				return engine.Observation{Text: text, Images: images, Sources: sources, Presentation: presentation}, nil
			},
		})
	}
	return out, nil
}

// ReadHubAggregateMCPAppResource reopens only a resource advertised by the
// current, explicitly trusted Hub aggregate session. Ordinary MCP connections
// cannot use this method, and a saved URI is never interpreted as a network
// destination.
func (s *MCPSession) ReadHubAggregateMCPAppResource(ctx context.Context, ref resulttypes.MCPAppReference) (*resulttypes.MCPAppResource, error) {
	if s == nil || s.session == nil || !s.hubAggregate {
		return nil, fmt.Errorf("trusted Hub aggregate MCP session is unavailable")
	}
	current, found := s.appResources[ref.ResourceURI]
	if !found || current.Provider != ref.Provider {
		return nil, fmt.Errorf("MCP App is not advertised by a currently reachable provider")
	}
	app := readMCPAppResource(ctx, s.session, ref.ResourceURI)
	if app == nil {
		return nil, fmt.Errorf("MCP App resource is unavailable")
	}
	return app, nil
}

// mcpAppResourceResponseLimitTransport reads resources/read responses with a
// hard cap before the MCP SDK can buffer an unbounded body. Other MCP methods
// retain their existing response behavior.
type mcpAppResourceResponseLimitTransport struct {
	base http.RoundTripper
}

type replayRequestBody struct {
	io.Reader
	original io.Closer
}

func (b *replayRequestBody) Close() error { return b.original.Close() }

func (t *mcpAppResourceResponseLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.Body == nil || req.Method != http.MethodPost {
		return t.base.RoundTrip(req)
	}
	original := req.Body
	prefix, err := io.ReadAll(io.LimitReader(original, mcpAppResourceRequestPeek))
	if err != nil {
		_ = original.Close()
		return nil, fmt.Errorf("inspect MCP request: %w", err)
	}
	req.Body = &replayRequestBody{Reader: io.MultiReader(bytes.NewReader(prefix), original), original: original}
	var rpc struct {
		Method string `json:"method"`
	}
	bounded := json.Unmarshal(prefix, &rpc) == nil && rpc.Method == "resources/read"
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil || !bounded {
		return resp, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, mcpAppResourceResponseMax+1))
	closeErr := resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read bounded MCP App response: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close MCP App response: %w", closeErr)
	}
	if len(body) > mcpAppResourceResponseMax {
		return nil, fmt.Errorf("MCP App resource response exceeds %d bytes", mcpAppResourceResponseMax)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Del("Content-Encoding")
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	return resp, nil
}

// hubMCPAppIdentity accepts only the Hub's provider-namespaced resource
// identifier. It validates the encoded upstream MCP URI without using it as a
// destination. The explicit aggregate connector is still required before this
// helper may be used.
func hubMCPAppIdentity(meta mcp.Meta) (resulttypes.MCPAppReference, bool) {
	ui, ok := meta["ui"].(map[string]any)
	if !ok {
		return resulttypes.MCPAppReference{}, false
	}
	value, ok := ui["resourceUri"].(string)
	if !ok || len(value) > 4096 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n\t ") {
		return resulttypes.MCPAppReference{}, false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "ui" || u.Host != "railgrid-provider" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return resulttypes.MCPAppReference{}, false
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return resulttypes.MCPAppReference{}, false
	}
	provider, err := url.PathUnescape(parts[0])
	if err != nil || provider != parts[0] || len(validation.IsDNS1123Label(provider)) != 0 {
		return resulttypes.MCPAppReference{}, false
	}
	encoded, err := url.PathUnescape(parts[1])
	if err != nil || encoded != parts[1] || encoded == "" || strings.ContainsAny(encoded, "/\\%") {
		return resulttypes.MCPAppReference{}, false
	}
	original, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(original) != encoded || !validMCPAppSourceURI(string(original)) {
		return resulttypes.MCPAppReference{}, false
	}
	return resulttypes.MCPAppReference{Provider: provider, ResourceURI: value}, true
}

func hubMCPAppResourceURI(meta mcp.Meta) string {
	ref, ok := hubMCPAppIdentity(meta)
	if !ok {
		return ""
	}
	return ref.ResourceURI
}

func validMCPAppSourceURI(value string) bool {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n\t ") {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "ui" && u.Host != "" && u.User == nil && u.Opaque == "" && u.Fragment == ""
}

// mcpToolVisibleToAgent applies MCP Apps visibility before exposing a tool to
// the model. An app-only tool remains a UI capability and is not made callable
// simply because it arrived through the aggregate endpoint.
func mcpToolVisibleToAgent(meta mcp.Meta) bool {
	ui, ok := meta["ui"].(map[string]any)
	if !ok {
		return true
	}
	raw, found := ui["visibility"]
	if !found {
		return true
	}
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return false
	}
	modelVisible := false
	for _, value := range values {
		name, ok := value.(string)
		if !ok || name != "app" && name != "model" {
			return false
		}
		modelVisible = modelVisible || name == "model"
	}
	return modelVisible
}

func mcpRailgridField(meta mcp.Meta, field string) (json.RawMessage, bool, error) {
	value, found := meta[mcpRailgridMetaKey]
	if !found || value == nil {
		return nil, false, nil
	}
	container, ok := value.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("_meta.railgrid must be an object")
	}
	value, found = container[field]
	if !found {
		return nil, false, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, true, fmt.Errorf("encode _meta.railgrid.%s: %w", field, err)
	}
	return encoded, true, nil
}

func mcpResultSources(meta mcp.Meta) ([]resulttypes.SourceReference, error) {
	raw, found, err := mcpRailgridField(meta, "sources")
	if err != nil || !found {
		return nil, err
	}
	if len(raw) > mcpSourcesMaxBytes {
		return nil, fmt.Errorf("_meta.railgrid.sources exceeds %d bytes", mcpSourcesMaxBytes)
	}
	var sources []resulttypes.SourceReference
	if err := json.Unmarshal(raw, &sources); err != nil {
		return nil, fmt.Errorf("decode _meta.railgrid.sources: %w", err)
	}
	if len(sources) > resulttypes.MaxAgentArtifactSources {
		return nil, fmt.Errorf("_meta.railgrid.sources exceeds %d source entries", resulttypes.MaxAgentArtifactSources)
	}
	for i, source := range sources {
		if err := source.Validate(); err != nil {
			return nil, fmt.Errorf("_meta.railgrid.sources[%d]: %w", i, err)
		}
	}
	return sources, nil
}

func mcpResultPresentation(meta mcp.Meta) *resulttypes.ProviderPresentation {
	raw, found, err := mcpRailgridField(meta, "presentation")
	if err != nil || !found || len(raw) > resulttypes.MaxProviderPresentationWireBytes {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil
	}
	delete(fields, "mcpApp") // HTML is accepted only from resources/read.
	withoutApp, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	presentation, err := resulttypes.ParseProviderPresentation(withoutApp)
	if err != nil {
		return nil
	}
	return presentation
}

func readMCPAppResource(ctx context.Context, session *mcp.ClientSession, uri string) *resulttypes.MCPAppResource {
	readCtx, cancel := context.WithTimeout(ctx, mcpAppResourceReadTimeout)
	defer cancel()
	result, err := session.ReadResource(readCtx, &mcp.ReadResourceParams{URI: uri})
	if err != nil || result == nil || len(result.Contents) != 1 || result.Contents[0] == nil {
		return nil
	}
	content := result.Contents[0]
	if content.URI != uri || content.MIMEType != mcpAppMIMEType || !mcpAppResourceSecuritySupported(content.Meta) {
		return nil
	}
	var html []byte
	switch {
	case content.Text != "" && len(content.Blob) == 0:
		html = []byte(content.Text)
	case content.Text == "" && len(content.Blob) > 0:
		html = content.Blob
	default:
		return nil
	}
	if len(html) == 0 || len(html) > resulttypes.MaxMCPAppHTMLBytes || !utf8.Valid(html) {
		return nil
	}
	digest := sha256.Sum256(html)
	app := &resulttypes.MCPAppResource{ResourceURI: uri, MIMEType: mcpAppMIMEType, HTML: string(html), SHA256: hex.EncodeToString(digest[:])}
	if app.Validate() != nil {
		return nil
	}
	return app
}

// Only the reviewed zero-origin policy subset is accepted from MCP App
// metadata. This narrows requested capabilities but does not claim that an
// app's own JavaScript cannot navigate or initiate network requests.
func mcpAppResourceSecuritySupported(meta mcp.Meta) bool {
	if len(meta) == 0 {
		return true
	}
	raw, found := meta["ui"]
	if !found {
		return true
	}
	ui, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	for key, value := range ui {
		switch key {
		case "csp":
			csp, ok := value.(map[string]any)
			if !ok {
				return false
			}
			for directive, rawDomains := range csp {
				if directive != "connectDomains" && directive != "resourceDomains" && directive != "frameDomains" && directive != "baseUriDomains" {
					return false
				}
				domains, ok := rawDomains.([]any)
				if !ok || len(domains) != 0 {
					return false
				}
			}
		case "permissions":
			permissions, ok := value.(map[string]any)
			if !ok || len(permissions) != 0 {
				return false
			}
		case "domain":
			if domain, ok := value.(string); !ok || domain != "" {
				return false
			}
		case "prefersBorder":
			if _, ok := value.(bool); !ok {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// mcpResultParts splits an MCP tool result into its concatenated text and any
// image content (raw bytes + MIME type) for vision-capable models.
func mcpResultParts(res *mcp.CallToolResult) (string, []engine.ToolImage) {
	var b strings.Builder
	var imgs []engine.ToolImage
	for _, c := range res.Content {
		switch tc := c.(type) {
		case *mcp.TextContent:
			b.WriteString(tc.Text)
			b.WriteString("\n")
		case *mcp.ImageContent:
			imgs = append(imgs, engine.ToolImage{MIMEType: tc.MIMEType, Data: tc.Data})
		}
	}
	return strings.TrimSpace(b.String()), imgs
}

// decodeMCPToolArguments preserves JSON number spelling while requiring one
// top-level object. The MCP SDK accepts map arguments; decoding through
// interface{} without UseNumber would round large integers before the request
// reaches the server.
func decodeMCPToolArguments(raw string) (map[string]any, error) {
	// Preserve the existing no-arguments invocation path.
	if raw == "" {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()

	var args map[string]any
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("expected one JSON object: %w", err)
	}
	if args == nil {
		return nil, fmt.Errorf("expected one JSON object")
	}

	var trailing any
	err := decoder.Decode(&trailing)
	if !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("expected one JSON object: %w", err)
		}
		return nil, fmt.Errorf("expected one JSON object")
	}
	return args, nil
}

// bearerTransport injects the connection token as a Bearer Authorization
// header on every MCP request.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}
