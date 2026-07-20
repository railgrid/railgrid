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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/provider-edges/internal/events"
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
	// UniFi Protect events are not a REST endpoint — the provider subscribes to
	// Protect's WebSocket feed in the background and buffers them per tenant+
	// service. Expose that buffer as a tool when a store is wired.
	if svc.Spec.Type == "unifi-protect" && p.eventStore != nil {
		p.registerEdgeEventsTool(srv, prefix, cluster, svc)
	}
}

// registerEdgeEventsTool exposes the per-tenant, per-service event buffer (fed
// by the WebSocket subscribers) as a read-only MCP tool. Events are isolated by
// events.Key{cluster, service}, so a caller only ever sees its own workspace's
// events.
func (p *Server) registerEdgeEventsTool(srv *mcp.Server, prefix, cluster string, svc *serviceView) {
	name := svc.Name
	mcp.AddTool(srv, &mcp.Tool{
		Name: prefix + "events",
		Description: "UniFi Protect — recent camera events (motion/ring/smart-detect) from the live event stream, most recent first. " +
			"All query params optional: {\"since\":\"30m\"} (a duration back from now), {\"types\":\"motion,ring\"}, {\"cameraId\":\"<id>\"}, {\"limit\":\"20\"}. " +
			"Rolling recent buffer, not a full archive.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in catToolInput) (*mcp.CallToolResult, any, error) {
		evs, err := p.eventStore.List(ctx, events.Key{Cluster: cluster, Service: name}, eventsFilterFromQuery(in.Query))
		if err != nil {
			return toolErr(err.Error()), nil, nil
		}
		if len(evs) == 0 {
			return &mcp.CallToolResult{Content: withServiceNote(svc, &mcp.TextContent{Text: "no recent events buffered for this service"})}, nil, nil
		}
		b, err := json.Marshal(evs)
		if err != nil {
			return toolErr("encode events: " + err.Error()), nil, nil
		}
		return &mcp.CallToolResult{Content: withServiceNote(svc, &mcp.TextContent{Text: string(b)})}, nil, nil
	})
}

// eventsFilterFromQuery maps the generic tool query params to an events.Filter.
func eventsFilterFromQuery(q map[string]string) events.Filter {
	var f events.Filter
	if s := strings.TrimSpace(q["since"]); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			f.Since = time.Now().Add(-d)
		}
	}
	if s := strings.TrimSpace(q["start"]); s != "" { // epoch millis, matches the old REST param
		if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
			f.Since = time.UnixMilli(ms)
		}
	}
	if s := strings.TrimSpace(q["types"]); s != "" {
		for _, t := range strings.Split(s, ",") {
			if t = strings.TrimSpace(t); t != "" {
				f.Types = append(f.Types, t)
			}
		}
	}
	f.CameraID = strings.TrimSpace(q["cameraId"])
	if s := strings.TrimSpace(q["limit"]); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			f.Limit = n
		}
	}
	return f
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

	// Idempotent reads (GET with no body) are retried on a 5xx: some upstreams —
	// notably UniFi Protect doorbell snapshots, which capture a live frame and
	// 500 while the camera is waking — succeed on a second attempt. Anything with
	// a body (POST actions) is sent once so we never repeat a side effect.
	retryable := tool.Method == http.MethodGet && bodyReader == nil
	var resp *http.Response
	var respBody []byte
	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		var err error
		resp, err = haclient.DoWith(ctx, dialer, target, tool.Method, path, header, bodyReader)
		if err != nil {
			return toolErr(err.Error()), nil, nil
		}
		respBody, _ = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close() //nolint:errcheck
		if retryable && resp.StatusCode >= 500 && attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return toolErr(ctx.Err().Error()), nil, nil
			case <-time.After(time.Duration(attempt) * 400 * time.Millisecond):
			}
			continue
		}
		break
	}
	if resp.StatusCode >= 400 {
		msg := fmt.Sprintf("%s returned %d: %s", def.DisplayName, resp.StatusCode, snippet(respBody))
		// On a persistent upstream error, surface the type's default guidance so
		// the model knows the sanctioned fallback (e.g. Protect: use events).
		if resp.StatusCode >= 500 {
			if hint := strings.TrimSpace(def.Instructions); hint != "" {
				msg += "\n\nGuidance: " + hint
			}
		}
		return toolErr(msg), nil, nil
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
