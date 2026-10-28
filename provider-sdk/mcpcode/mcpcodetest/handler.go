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

// Package mcpcodetest is a stand-in for the hub's aggregate MCP endpoint in
// tests: a stateless streamable-HTTP JSON-RPC server that lists search and
// execute, answers search with the configured catalog, runs the one program
// CallCode writes, and refuses a direct tools/call on anything but a
// consent-gated tool — exactly as the real aggregate does, so a consumer
// test fails the way production would if it still calls tools by name.
package mcpcodetest

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/railgrid/provider-sdk/mcpcode"
)

// Tool is one federated tool the fake serves.
type Tool struct {
	mcpcode.CatalogTool
	// Call answers an invocation with the tool's own tools/call result JSON
	// ({"content": [...], "structuredContent": ..., "isError": ...}). Nil
	// answers with an empty text result.
	Call func(args json.RawMessage) (json.RawMessage, error)
}

// Resource is an MCP resource the fake serves on resources/read.
type Resource struct {
	MIMEType string
	Text     string
}

// Invocation records one request the fake answered.
type Invocation struct {
	// Method is "search", "execute", "direct" (a plain tools/call), or the
	// JSON-RPC method for anything else.
	Method string
	// Tool is the federated tool name an execute or direct call addressed.
	Tool string
	Args json.RawMessage
}

// Server is the fake. Configure its fields, then serve it over HTTP.
type Server struct {
	Tools        []Tool
	Resources    map[string]Resource
	Instructions string
	// Observe, when set, is called for every invocation as it is served.
	Observe func(Invocation)

	mu          sync.Mutex
	invocations []Invocation
}

// Catalog parses the JSON array a fake used to return from tools/list into
// Tools with no Call handlers.
func Catalog(toolsJSON json.RawMessage) []Tool {
	var cat []mcpcode.CatalogTool
	if err := json.Unmarshal(toolsJSON, &cat); err != nil {
		panic("mcpcodetest: bad catalog JSON: " + err.Error())
	}
	out := make([]Tool, 0, len(cat))
	for _, c := range cat {
		out = append(out, Tool{CatalogTool: c})
	}
	return out
}

// Invocations returns every request served so far.
func (s *Server) Invocations() []Invocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Invocation(nil), s.invocations...)
}

func (s *Server) record(inv Invocation) {
	s.mu.Lock()
	s.invocations = append(s.invocations, inv)
	s.mu.Unlock()
	if s.Observe != nil {
		s.Observe(inv)
	}
}

func (s *Server) tool(name string) (Tool, bool) {
	for _, t := range s.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name            string          `json:"name"`
		Arguments       json.RawMessage `json:"arguments"`
		URI             string          `json:"uri"`
		ProtocolVersion string          `json:"protocolVersion"`
	} `json:"params"`
}

// ServeHTTP answers one JSON-RPC request per POST, application/json framed.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad JSON-RPC", http.StatusBadRequest)
		return
	}
	if len(req.ID) == 0 || strings.HasPrefix(req.Method, "notifications/") {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rpcErr := s.dispatch(req)
	w.Header().Set("Content-Type", "application/json")
	msg := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rpcErr != nil {
		msg["error"] = rpcErr
	} else {
		msg["result"] = result
	}
	_ = json.NewEncoder(w).Encode(msg)
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) dispatch(req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		version := req.Params.ProtocolVersion
		if version == "" {
			version = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
			"serverInfo":      map[string]any{"name": "mcpcodetest", "version": "1"},
			"instructions":    s.Instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		s.record(Invocation{Method: req.Method})
		tools := []map[string]any{
			{"name": mcpcode.ToolSearch, "description": "search the catalog with code", "inputSchema": codeSchema()},
			{"name": mcpcode.ToolExecute, "description": "call tools with code", "inputSchema": codeSchema()},
		}
		for _, t := range s.Tools {
			if t.ConsentRequired {
				tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schemaOrEmpty(t.InputSchema)})
			}
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		return s.callTool(req)
	case "resources/list":
		s.record(Invocation{Method: req.Method})
		list := []map[string]any{}
		for uri, res := range s.Resources {
			list = append(list, map[string]any{"uri": uri, "name": uri, "mimeType": res.MIMEType})
		}
		return map[string]any{"resources": list}, nil
	case "resources/read":
		s.record(Invocation{Method: req.Method, Tool: req.Params.URI})
		res, ok := s.Resources[req.Params.URI]
		if !ok {
			return nil, &rpcError{Code: -32002, Message: "resource not found: " + req.Params.URI}
		}
		return map[string]any{"contents": []map[string]any{{"uri": req.Params.URI, "mimeType": res.MIMEType, "text": res.Text}}}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func (s *Server) callTool(req rpcRequest) (any, *rpcError) {
	var args struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(req.Params.Arguments, &args)
	switch req.Params.Name {
	case mcpcode.ToolSearch:
		s.record(Invocation{Method: "search"})
		if strings.TrimSpace(args.Code) != mcpcode.CatalogCode {
			return json.RawMessage(mcpcode.EncodeRunError("mcpcodetest runs only mcpcode.CatalogCode")), nil
		}
		cat := make([]mcpcode.CatalogTool, 0, len(s.Tools))
		for _, t := range s.Tools {
			cat = append(cat, t.CatalogTool)
		}
		return json.RawMessage(mcpcode.EncodeCatalogResult(cat)), nil
	case mcpcode.ToolExecute:
		tool, callArgs, ok := mcpcode.ParseCallCode(args.Code)
		if !ok {
			s.record(Invocation{Method: "execute"})
			return json.RawMessage(mcpcode.EncodeRunError("mcpcodetest runs only programs written by mcpcode.CallCode")), nil
		}
		s.record(Invocation{Method: "execute", Tool: tool, Args: callArgs})
		t, found := s.tool(tool)
		if !found {
			return json.RawMessage(mcpcode.EncodeRunError(mcpcode.RefusalMessage(tool, "unknown"))), nil
		}
		if t.ConsentRequired {
			return json.RawMessage(mcpcode.EncodeRunError(mcpcode.RefusalMessage(tool, "consent"))), nil
		}
		inner, err := invoke(t, callArgs)
		if err != nil {
			return json.RawMessage(mcpcode.EncodeRunError(mcpcode.RefusalMessage(tool, err.Error()))), nil
		}
		out, err := mcpcode.EncodeCallResult(tool, inner)
		if err != nil {
			return nil, &rpcError{Code: -32603, Message: err.Error()}
		}
		return out, nil
	default:
		s.record(Invocation{Method: "direct", Tool: req.Params.Name, Args: req.Params.Arguments})
		t, found := s.tool(req.Params.Name)
		if !found || !t.ConsentRequired {
			return nil, &rpcError{Code: -32602, Message: "unknown tool " + strconvQuote(req.Params.Name)}
		}
		inner, err := invoke(t, req.Params.Arguments)
		if err != nil {
			return nil, &rpcError{Code: -32603, Message: err.Error()}
		}
		return inner, nil
	}
}

func invoke(t Tool, args json.RawMessage) (json.RawMessage, error) {
	if t.Call == nil {
		return json.RawMessage(`{"content":[{"type":"text","text":""}]}`), nil
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return t.Call(args)
}

func codeSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}}, "required": []string{"code"}}
}

func schemaOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{"type":"object"}`)
	}
	return raw
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
