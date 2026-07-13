// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/engine"
)

// githubMCPEndpoint is the hosted GitHub MCP server; a github Connection with
// a PAT gets its full toolset without any MCP configuration.
const githubMCPEndpoint = "https://api.githubcopilot.com/mcp"

// MCPSession wraps one live MCP connection's discovered tools. Close after
// the run completes.
type MCPSession struct {
	Tools   []engine.Tool
	session *mcp.ClientSession
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
	endpoint := strings.TrimSpace(conn.Spec.BaseURL)
	if endpoint == "" {
		if conn.Spec.Type == agentsv1alpha1.ConnectionTypeGitHub {
			endpoint = githubMCPEndpoint
		} else {
			return nil, fmt.Errorf("mcp connection %q has no baseURL", conn.Name)
		}
	}
	return ConnectMCPEndpoint(ctx, endpoint, d.connToken(ctx, conn.Name), conn.Name, false)
}

// ConnectMCPEndpoint dials an arbitrary MCP server over streamable HTTP with
// an optional bearer token and exposes its tools as <prefix>__<tool>. Used by
// connection-backed families and the edges family (the hub's aggregate MCP
// virtual endpoint, dialed as the calling user).
func ConnectMCPEndpoint(ctx context.Context, endpoint, bearer, prefix string, insecureTLS bool) (*MCPSession, error) {
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
	client := mcp.NewClient(&mcp.Implementation{Name: "kedge-agents", Version: "0.1.0"}, nil)
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

	out := &MCPSession{session: session}
	conn := struct{ Name string }{Name: prefix}
	for _, t := range listed.Tools {
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
			Exec: func(ctx context.Context, argsJSON string) (string, error) {
				var args map[string]any
				if argsJSON != "" {
					if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
						return "", fmt.Errorf("invalid arguments: %w", err)
					}
				}
				res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: args})
				if err != nil {
					return "", err
				}
				text := mcpResultText(res)
				if res.IsError {
					return "", fmt.Errorf("%s", clip(text, 2000))
				}
				return clip(text, webFetchMaxReturn), nil
			},
		})
	}
	return out, nil
}

func mcpResultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
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
