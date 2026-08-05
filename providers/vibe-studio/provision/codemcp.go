// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Minimal MCP JSON-RPC client for code provider tools — the canonical (and
// only) cross-provider path for pushing file contents, since RepositoryCommit
// bundles live on the code provider's own filesystem.
//
// Talks to the hub's AGGREGATE MCP endpoint (the per-tenant "default"
// MCPServer) as the caller, with provider-namespaced tool names
// (code__commit_files). The raw /services/providers/code/mcp proxy path is
// unusable here: the MCP SDK's DNS-rebinding guard 403s ("invalid Host
// header") behind the hub proxy in dev; the aggregate's federation client
// normalizes Host, so it works in every topology.

type mcpRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type mcpRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// callCodeMCPTool invokes one code__-namespaced tool via the hub aggregate.
func (c *Client) CallCodeTool(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	// apiurl.MCPServerPath pattern (hub module): /services/mcpserver/{cluster}/
	// apis/kedge.faros.sh/v1alpha1/mcpservers/{name}/mcp — "default" is the
	// per-tenant aggregate the hub bootstraps.
	endpoint := fmt.Sprintf("%s/services/mcpserver/%s/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp",
		c.HubBase, c.ClusterID)

	post := func(sessionID string, req mcpRPCRequest) (json.RawMessage, string, error) {
		body, err := json.Marshal(req)
		if err != nil {
			return nil, "", err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, "", err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json, text/event-stream")
		if c.Token != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.Token)
		}
		if sessionID != "" {
			httpReq.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, err := c.http.Do(httpReq)
		if err != nil {
			return nil, "", fmt.Errorf("code mcp %s: %w", req.Method, err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
		if err != nil {
			return nil, "", err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", fmt.Errorf("code mcp %s: status %d: %s", req.Method, resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		// Streamable-HTTP servers may answer as a single SSE event; unwrap.
		payload := raw
		if strings.HasPrefix(strings.TrimSpace(string(raw)), "event:") || strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			for _, line := range strings.Split(string(raw), "\n") {
				if data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
					payload = []byte(strings.TrimSpace(data))
					break
				}
			}
		}
		var rpc mcpRPCResponse
		if err := json.Unmarshal(payload, &rpc); err != nil {
			return nil, "", fmt.Errorf("code mcp %s: bad response: %w", req.Method, err)
		}
		if rpc.Error != nil {
			return nil, "", fmt.Errorf("code mcp %s: %s", req.Method, rpc.Error.Message)
		}
		return rpc.Result, resp.Header.Get("Mcp-Session-Id"), nil
	}

	// Initialize (best effort — stateless servers accept the call regardless;
	// stateful ones hand back a session id we echo).
	_, sessionID, err := post("", mcpRPCRequest{
		JSONRPC: "2.0", ID: 1, Method: "initialize",
		Params: map[string]any{
			"protocolVersion": "2025-03-26",
			"clientInfo":      map[string]any{"name": "vibe-studio", "version": "0.1.0"},
			"capabilities":    map[string]any{},
		},
	})
	if err != nil {
		return nil, err
	}

	result, _, err := post(sessionID, mcpRPCRequest{
		JSONRPC: "2.0", ID: 2, Method: "tools/call",
		Params: map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		return nil, err
	}
	// Tool results wrap content blocks; surface isError as a failure.
	var call struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &call); err != nil {
		return nil, fmt.Errorf("code mcp tool %s: bad result: %w", tool, err)
	}
	var text string
	for _, c := range call.Content {
		if c.Type == "text" {
			text = c.Text
			break
		}
	}
	if call.IsError {
		return nil, fmt.Errorf("code mcp tool %s failed: %s", tool, text)
	}
	return json.RawMessage(text), nil
}
