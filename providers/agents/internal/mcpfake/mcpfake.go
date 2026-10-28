/*
Copyright 2026 The Railgrid Authors.

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

// Package mcpfake turns an MCP SDK server into a stand-in for the hub's
// aggregate in tests: it registers search and execute (the aggregate's code
// mode) and dispatches railgrid.call to the named tool handlers it was given,
// so a test exercising the agents provider's aggregate client sees the real
// wire shape, resources included.
package mcpfake

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/railgrid/provider-sdk/mcpcode"
)

// Tool is one federated tool the fake aggregate serves through execute.
type Tool struct {
	Tool    *mcp.Tool
	Handler mcp.ToolHandler
}

// Register installs search and execute on server over tools. Consent-gated
// tools (listed directly by the real aggregate) are not modelled.
func Register(server *mcp.Server, tools ...Tool) {
	codeSchema := map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}}, "required": []string{"code"}}
	byName := map[string]Tool{}
	catalog := make([]mcpcode.CatalogTool, 0, len(tools))
	for _, t := range tools {
		byName[t.Tool.Name] = t
		entry := mcpcode.CatalogTool{Name: t.Tool.Name, Title: t.Tool.Title, Description: t.Tool.Description, Meta: map[string]any(t.Tool.Meta)}
		if t.Tool.InputSchema != nil {
			entry.InputSchema, _ = json.Marshal(t.Tool.InputSchema)
		}
		catalog = append(catalog, entry)
	}
	server.AddTool(&mcp.Tool{Name: mcpcode.ToolSearch, Description: "search", InputSchema: codeSchema}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(req.Params.Arguments, &args)
		if args.Code != mcpcode.CatalogCode {
			return asResult(mcpcode.EncodeRunError("mcpfake runs only mcpcode.CatalogCode"))
		}
		return asResult(mcpcode.EncodeCatalogResult(catalog))
	})
	server.AddTool(&mcp.Tool{Name: mcpcode.ToolExecute, Description: "execute", InputSchema: codeSchema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(req.Params.Arguments, &args)
		name, callArgs, ok := mcpcode.ParseCallCode(args.Code)
		if !ok {
			return asResult(mcpcode.EncodeRunError("mcpfake runs only programs written by mcpcode.CallCode"))
		}
		t, found := byName[name]
		if !found {
			return asResult(mcpcode.EncodeRunError(mcpcode.RefusalMessage(name, "unknown")))
		}
		inner, err := t.Handler(ctx, &mcp.CallToolRequest{Session: req.Session, Params: &mcp.CallToolParamsRaw{Name: name, Arguments: callArgs}})
		if err != nil {
			return asResult(mcpcode.EncodeRunError(mcpcode.RefusalMessage(name, err.Error())))
		}
		innerJSON, err := json.Marshal(inner)
		if err != nil {
			return nil, fmt.Errorf("mcpfake: encoding inner result: %w", err)
		}
		out, err := mcpcode.EncodeCallResult(name, innerJSON)
		if err != nil {
			return nil, err
		}
		return asResult(out)
	})
}

func asResult(raw json.RawMessage) (*mcp.CallToolResult, error) {
	var out mcp.CallToolResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcpfake: decoding result: %w", err)
	}
	return &out, nil
}
