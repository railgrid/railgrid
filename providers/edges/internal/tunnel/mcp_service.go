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

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/klog/v2"

	"github.com/faroshq/provider-edges/internal/haclient"
	"github.com/faroshq/provider-edges/internal/svccatalog"
)

// serviceMCPImpl is advertised on `initialize` for a per-Service MCP
// endpoint.
var serviceMCPImpl = &mcp.Implementation{
	Name:    "kedge-edgeservice",
	Title:   "Kedge Service",
	Version: "v1alpha1",
}

// haStatesTrimLimit caps how many entities ha_states returns by default; a real
// HA install has thousands of entities and full payloads blow the model context.
const haStatesDefaultLimit = 100

// buildServiceMCPHandler serves the per-Service MCP endpoint (streamable
// HTTP, stateless). The tool bundle is keyed by spec.type; "home-assistant"
// gets the HA tools, everything else gets none (proxy-only).
// kcpToken is the caller's kcp bearer token, threaded through so the service's
// own auth token can be read from its Secret as the caller (see userClusterConfig).
func (p *Server) buildServiceMCPHandler(cluster, name, kcpToken string, svc *serviceView, dialer haclient.Dialer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Normalize Host to loopback to satisfy the MCP SDK's DNS-rebinding
		// guard (this endpoint is reached only through the hub's authenticated
		// proxy). See buildMCPHandler for the full rationale.
		r.Host = "localhost"
		handler := mcp.NewStreamableHTTPHandler(
			func(req *http.Request) *mcp.Server {
				return p.buildServiceMCPServer(cluster, name, kcpToken, svc, dialer)
			},
			&mcp.StreamableHTTPOptions{Stateless: true},
		)
		handler.ServeHTTP(w, r)
	})
}

// buildServiceMCPServer constructs the MCP server for one Service.
func (p *Server) buildServiceMCPServer(cluster, name, kcpToken string, svc *serviceView, dialer haclient.Dialer) *mcp.Server {
	instructions := fmt.Sprintf(
		"You are connected to the kedge Service %q (type %q) in tenant workspace %q. "+
			"Tools here drive a real service running next to an edge agent. "+
			"The call_service tool actuates physical devices — treat it with care.",
		name, svc.Spec.Type, cluster,
	)
	// Backend-authored default guidance for the type (quirks, tool sequences),
	// then the operator's own spec.instructions, which extend or override it.
	if def := strings.TrimSpace(svccatalog.DefaultInstructions(svc.Spec.Type)); def != "" {
		instructions += "\n\n" + def
	}
	if extra := strings.TrimSpace(svc.Spec.Instructions); extra != "" {
		instructions += "\n\n" + extra
	}
	srv := mcp.NewServer(serviceMCPImpl, &mcp.ServerOptions{Instructions: instructions})

	switch {
	case svc.Spec.Type == "home-assistant":
		p.registerHomeAssistantTools(srv, "", cluster, kcpToken, svc, dialer)
	case svccatalog.IsDataDriven(svc.Spec.Type):
		p.registerCatalogTools(srv, "", cluster, kcpToken, svc, dialer)
	}
	return srv
}

// --- Home Assistant tools ---------------------------------------------------

type haStatesInput struct {
	Domain string `json:"domain,omitempty" jsonschema:"filter to a single HA domain, e.g. cover, light, switch"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max entities to return (default 100)"`
}

type haGetStateInput struct {
	EntityID string `json:"entity_id" jsonschema:"the entity id, e.g. cover.gate"`
}

type haCallServiceInput struct {
	Domain   string         `json:"domain" jsonschema:"HA service domain, e.g. cover"`
	Service  string         `json:"service" jsonschema:"HA service name, e.g. open_cover"`
	EntityID string         `json:"entity_id,omitempty" jsonschema:"target entity id, e.g. cover.gate"`
	Data     map[string]any `json:"data,omitempty" jsonschema:"extra service data merged into the request body"`
}

// haEntityState is the trimmed projection returned by ha_states.
type haEntityState struct {
	EntityID     string `json:"entity_id"`
	State        string `json:"state"`
	FriendlyName string `json:"friendly_name,omitempty"`
}

// registerHomeAssistantTools registers the HA tools on srv. prefix is prepended
// to each tool name (used by the aggregate to disambiguate multiple services);
// pass "" for the per-service endpoint.
func (p *Server) registerHomeAssistantTools(srv *mcp.Server, prefix, cluster, kcpToken string, svc *serviceView, dialer haclient.Dialer) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        prefix + "states",
		Description: "List Home Assistant entity states (trimmed to entity_id, state, friendly_name). Optionally filter by domain.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in haStatesInput) (*mcp.CallToolResult, any, error) {
		return p.haStates(ctx, cluster, kcpToken, svc, dialer, in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        prefix + "get_state",
		Description: "Get the full state (including attributes) of one Home Assistant entity.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in haGetStateInput) (*mcp.CallToolResult, any, error) {
		if in.EntityID == "" {
			return toolErr("entity_id is required"), nil, nil
		}
		return p.haPassthrough(ctx, cluster, kcpToken, svc, dialer, http.MethodGet, "/api/states/"+in.EntityID, nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        prefix + "call_service",
		Description: "Call a Home Assistant service (e.g. cover.open_cover) to actuate a device. This performs a real action.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in haCallServiceInput) (*mcp.CallToolResult, any, error) {
		if in.Domain == "" || in.Service == "" {
			return toolErr("domain and service are required"), nil, nil
		}
		body := map[string]any{}
		for k, v := range in.Data {
			body[k] = v
		}
		if in.EntityID != "" {
			body["entity_id"] = in.EntityID
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return toolErr("encode body: " + err.Error()), nil, nil
		}
		return p.haPassthrough(ctx, cluster, kcpToken, svc, dialer,
			http.MethodPost, "/api/services/"+in.Domain+"/"+in.Service, raw)
	})
}

// haStates fetches /api/states and returns a trimmed, optionally domain-filtered
// and limited list.
func (p *Server) haStates(ctx context.Context, cluster, kcpToken string, svc *serviceView, dialer haclient.Dialer, in haStatesInput) (*mcp.CallToolResult, any, error) {
	resp, err := p.haDo(ctx, cluster, kcpToken, svc, dialer, http.MethodGet, "/api/states", nil)
	if err != nil {
		return toolErr(err.Error()), nil, nil
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode >= 400 {
		return toolErr(fmt.Sprintf("home assistant returned %d", resp.StatusCode)), nil, nil
	}

	var raw []struct {
		EntityID   string         `json:"entity_id"`
		State      string         `json:"state"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&raw); err != nil {
		return toolErr("decode states: " + err.Error()), nil, nil
	}

	limit := in.Limit
	if limit <= 0 {
		limit = haStatesDefaultLimit
	}
	domain := strings.ToLower(in.Domain)
	out := make([]haEntityState, 0, limit)
	for _, e := range raw {
		if domain != "" && !strings.HasPrefix(e.EntityID, domain+".") {
			continue
		}
		fn, _ := e.Attributes["friendly_name"].(string)
		out = append(out, haEntityState{EntityID: e.EntityID, State: e.State, FriendlyName: fn})
		if len(out) >= limit {
			break
		}
	}
	return toolJSON(out)
}

// haPassthrough issues a request to HA and returns the response body verbatim.
func (p *Server) haPassthrough(ctx context.Context, cluster, kcpToken string, svc *serviceView, dialer haclient.Dialer, method, path string, body []byte) (*mcp.CallToolResult, any, error) {
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	resp, err := p.haDo(ctx, cluster, kcpToken, svc, dialer, method, path, rdr)
	if err != nil {
		return toolErr(err.Error()), nil, nil
	}
	defer resp.Body.Close() //nolint:errcheck
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return toolErr(fmt.Sprintf("home assistant returned %d: %s", resp.StatusCode, string(respBody))), nil, nil
	}
	text := string(respBody)
	if text == "" {
		text = fmt.Sprintf("OK (%d)", resp.StatusCode)
	}
	return &mcp.CallToolResult{Content: withServiceNote(svc, &mcp.TextContent{Text: text})}, nil, nil
}

// haDo resolves the service token (from the Service's Secret, read as the
// caller) and issues one request via haclient.
func (p *Server) haDo(ctx context.Context, cluster, kcpToken string, svc *serviceView, dialer haclient.Dialer, method, path string, body io.Reader) (*http.Response, error) {
	token, err := p.readServiceToken(ctx, cluster, svc, kcpToken)
	if err != nil {
		return nil, fmt.Errorf("service credentials: %w", err)
	}
	if token == "" {
		return nil, fmt.Errorf("no auth token configured for this service (set spec.authSecretRef)")
	}
	target := haclient.Target{Scheme: svc.scheme(), Host: svc.targetHost(), Port: svc.Spec.Port, Token: token}
	resp, err := haclient.Do(ctx, dialer, target, method, path, body)
	if err != nil {
		klog.FromContext(ctx).Error(err, "home assistant request failed", "method", method, "path", path)
		return nil, err
	}
	return resp, nil
}

// toolErr returns a tool-level error result (IsError=true) so the model can
// see and self-correct, per MCP guidance.
func toolErr(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// withServiceNote prepends the Service's operator note (spec.instructions) to a
// tool result's content. Server-level MCP `initialize` instructions are surfaced
// inconsistently by clients (and only read once at connect); echoing the note in
// the result delivers it to ANY MCP client — internal or external — at the exact
// moment the service is used.
func withServiceNote(svc *serviceView, content ...mcp.Content) []mcp.Content {
	if note := strings.TrimSpace(svc.Spec.Instructions); note != "" {
		return append([]mcp.Content{&mcp.TextContent{Text: "Operator note for service " + svc.Name + ":\n" + note + "\n---"}}, content...)
	}
	return content
}

// toolJSON marshals v into a JSON text content result.
func toolJSON(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return toolErr("encode result: " + err.Error()), nil, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}
