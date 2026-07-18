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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/provider-edges/internal/haclient"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

// rootMCPImpl is advertised on `initialize` for the provider aggregate endpoint.
var rootMCPImpl = &mcp.Implementation{
	Name:    "kedge-edges",
	Title:   "Kedge edges provider MCP",
	Version: "v1alpha1",
}

// RootMCPHandler serves the provider's aggregate MCP endpoint (mounted at /mcp,
// federated by the hub MCP aggregate). It merges the kube toolset (across
// connected KubernetesCluster edges) with the Home Assistant tools of every
// Ready home-assistant Service in the caller's tenant, so an AI agent with
// the edges tool family sees both without any agents-provider change.
func (s *Server) RootMCPHandler() http.Handler {
	return s.buildRootMCPHandler()
}

func (p *Server) buildRootMCPHandler() http.Handler {
	// The kube backend handler is the existing containers/kubernetes-mcp-server
	// endpoint; we federate it in-process (different SDK, so merge at JSON-RPC).
	kubeHandler := p.buildProviderMCPHandler()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		cluster := r.Header.Get("X-Kedge-Cluster")

		r.Host = "localhost" // MCP SDK DNS-rebinding guard (see buildMCPHandler).
		handler := mcp.NewStreamableHTTPHandler(
			func(req *http.Request) *mcp.Server {
				return p.buildRootMCPServer(req.Context(), cluster, token, kubeHandler)
			},
			&mcp.StreamableHTTPOptions{Stateless: true},
		)
		handler.ServeHTTP(w, r)
	})
}

// buildRootMCPServer builds the merged aggregate server for one request.
func (p *Server) buildRootMCPServer(ctx context.Context, cluster, token string, kubeHandler http.Handler) *mcp.Server {
	logger := klog.FromContext(ctx).WithName("root-mcp")

	// 1. Resolve which Ready Services (Home Assistant + catalog apps) we can
	//    actually dial right now (a Ready Service whose edge tunnel is down can't
	//    be driven). Done before building instructions so each registered service
	//    can contribute its spec.instructions to the endpoint's ambient guidance.
	type svcReg struct {
		reg    readyHAService
		prefix string
		dialer haclient.Dialer
	}
	var toRegister []svcReg
	svcs := p.listReadyServices(ctx, cluster, token)
	logger.Info("service tool discovery", "cluster", cluster, "readyServices", len(svcs))
	for _, es := range svcs {
		key := edgeConnKey(es.view.connResource(), cluster, es.view.Spec.EdgeRef.Name)
		dialer, ok := p.edgeConnManager.Load(key)
		if !ok {
			// The Service is Ready but its edge tunnel isn't connected right now,
			// so we can't dial it — skip its tools this round. Logged (not silent)
			// so a missing-tools report is diagnosable.
			logger.Info("service tools skipped: no live edge dialer", "service", es.name, "type", es.view.Spec.Type, "edge", es.view.Spec.EdgeRef.Name, "connKey", key)
			continue
		}
		toRegister = append(toRegister, svcReg{reg: es, prefix: sanitizeToolPrefix(es.name) + "_", dialer: dialer})
	}

	instructions := fmt.Sprintf(
		"You are connected to the kedge edges provider MCP endpoint for tenant workspace %q. "+
			"It exposes Kubernetes tools across connected KubernetesCluster edges and, for each Ready "+
			"Service, tools named \"<service>_*\" (e.g. a Home Assistant service \"ha\" gives ha_states/ha_call_service; a qBittorrent service \"qb\" gives qb_torrents/qb_add).",
		cluster,
	)
	// Append each registered service's own instructions so operator-authored
	// context (entity naming, indexer prefs, safety notes) reaches the model.
	for _, h := range toRegister {
		if extra := strings.TrimSpace(h.reg.view.Spec.Instructions); extra != "" {
			instructions += fmt.Sprintf("\n\nService %q (type %q, tools \"%s*\"):\n%s", h.reg.name, h.reg.view.Spec.Type, h.prefix, extra)
		}
	}

	srv := mcp.NewServer(rootMCPImpl, &mcp.ServerOptions{Instructions: instructions})

	// 2. Register each service's tools by type. The list and per-tool Secret
	//    reads act as the caller (token), since the provider SA has no direct
	//    RBAC on Service objects in tenant workspaces.
	for _, h := range toRegister {
		switch {
		case h.reg.view.Spec.Type == "home-assistant":
			p.registerHomeAssistantTools(srv, h.prefix, cluster, token, h.reg.view, h.dialer)
		case catalogServiceType(h.reg.view.Spec.Type):
			p.registerCatalogTools(srv, h.prefix, cluster, token, h.reg.view, h.dialer)
		}
		logger.Info("service tools registered", "service", h.reg.name, "type", h.reg.view.Spec.Type, "prefix", h.prefix)
	}

	// 3. Federate the kube toolset in-process.
	if err := p.federateKubeTools(ctx, srv, kubeHandler, token, cluster); err != nil {
		logger.V(2).Info("kube tool federation failed (kube tools omitted)", "err", err.Error())
	}
	return srv
}

// readyHAService pairs a Service name with its decoded view.
type readyHAService struct {
	name string
	view *serviceView
}

// mcpServiceType reports whether a Service type has an MCP tool bundle (Home
// Assistant or any catalog app). Types without one are proxy-only and don't
// contribute tools to the endpoint.
func mcpServiceType(t string) bool {
	return t == "home-assistant" || catalogServiceType(t)
}

// listReadyServices lists Ready Services in the tenant that expose MCP tools
// (Home Assistant + catalog apps), reading as the caller (token) — the provider
// SA has no direct RBAC on Service objects in tenant workspaces (see
// userClusterConfig).
func (p *Server) listReadyServices(ctx context.Context, cluster, token string) []readyHAService {
	if p.kcpConfig == nil || cluster == "" {
		return nil
	}
	dynClient, err := dynamic.NewForConfig(p.userClusterConfig(cluster, token))
	if err != nil {
		return nil
	}
	logger := klog.FromContext(ctx).WithName("service-discovery")
	gvr := schema.GroupVersionResource{Group: p.group, Version: p.version, Resource: serviceResource}
	list, err := dynClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		// As-caller list failed (RBAC or transient) — the token can't see
		// Service objects, so no tools. Logged so it's diagnosable.
		logger.Info("service discovery: listing Services failed", "err", err.Error())
		return nil
	}
	logger.V(2).Info("service discovery: listed Services", "count", len(list.Items))
	var out []readyHAService
	for i := range list.Items {
		item := &list.Items[i]
		name := item.GetName()
		view := &serviceView{Name: name}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, view); err != nil {
			logger.Info("service discovery: skip (decode failed)", "service", name, "err", err.Error())
			continue
		}
		if !mcpServiceType(view.Spec.Type) {
			continue // proxy-only type — contributes no tools, not noteworthy
		}
		if view.Spec.EdgeRef.Name == "" || view.Spec.Port == 0 {
			logger.Info("service discovery: skip (missing edgeRef.name or port)", "service", name, "type", view.Spec.Type, "edgeRef", view.Spec.EdgeRef.Name, "port", view.Spec.Port)
			continue
		}
		// A kube Service with no targetRef has no cluster-DNS name to dial;
		// skip rather than fall back to the agent pod's loopback.
		if view.isKube() && (view.Spec.TargetRef == nil || view.Spec.TargetRef.Name == "" || view.Spec.TargetRef.Namespace == "") {
			logger.Info("service discovery: skip (kube service without spec.targetRef name+namespace)", "service", name, "type", view.Spec.Type)
			continue
		}
		if !serviceReady(item.Object) {
			phase, _, _ := unstructuredString(item.Object, "status", "phase")
			logger.Info("service discovery: skip (status.phase != Ready)", "service", name, "type", view.Spec.Type, "phase", phase)
			continue
		}
		out = append(out, readyHAService{name: name, view: view})
	}
	return out
}

// serviceReady reports whether status.phase == "Ready".
func serviceReady(obj map[string]any) bool {
	phase, _, _ := unstructuredString(obj, "status", "phase")
	return phase == "Ready"
}

// unstructuredString reads a nested string field.
func unstructuredString(obj map[string]any, fields ...string) (string, bool, error) {
	cur := obj
	for i, f := range fields {
		v, ok := cur[f]
		if !ok {
			return "", false, nil
		}
		if i == len(fields)-1 {
			s, ok := v.(string)
			return s, ok, nil
		}
		next, ok := v.(map[string]any)
		if !ok {
			return "", false, nil
		}
		cur = next
	}
	return "", false, nil
}

// sanitizeToolPrefix makes a Service name safe as an MCP tool-name prefix
// (MCP names should stick to [a-z0-9_-]).
func sanitizeToolPrefix(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// --- in-process federation of the kube MCP handler -------------------------

// federateKubeTools lists tools from the in-process kube handler and registers a
// proxy tool for each, forwarding tools/call back to that handler.
func (p *Server) federateKubeTools(ctx context.Context, srv *mcp.Server, kubeHandler http.Handler, token, cluster string) error {
	tools, err := p.kubeListTools(ctx, kubeHandler, token, cluster)
	if err != nil {
		return err
	}
	for _, t := range tools {
		t := t
		tool := &mcp.Tool{
			Name:        t.Name,
			Title:       t.Title,
			Description: t.Description,
			Annotations: t.Annotations,
		}
		if len(t.InputSchema) > 0 {
			// InputSchema is `any`; pass the raw JSON schema through so tools/list
			// echoes it verbatim (matches the hub federation approach).
			tool.InputSchema = t.InputSchema
		}
		srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args map[string]any
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
					return nil, fmt.Errorf("decode arguments: %w", err)
				}
			}
			return p.kubeCallTool(ctx, kubeHandler, token, cluster, t.Name, args)
		})
	}
	return nil
}

type kubeTool struct {
	Name        string               `json:"name"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	InputSchema json.RawMessage      `json:"inputSchema"`
	Annotations *mcp.ToolAnnotations `json:"annotations,omitempty"`
}

func (p *Server) kubeListTools(ctx context.Context, h http.Handler, token, cluster string) ([]kubeTool, error) {
	raw, err := p.kubeRPC(ctx, h, token, cluster, "tools/list", json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []kubeTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return out.Tools, nil
}

func (p *Server) kubeCallTool(ctx context.Context, h http.Handler, token, cluster, name string, args map[string]any) (*mcp.CallToolResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	raw, err := p.kubeRPC(ctx, h, token, cluster, "tools/call", params)
	if err != nil {
		return nil, err
	}
	var res mcp.CallToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode tools/call: %w", err)
	}
	return &res, nil
}

// kubeRPC does one JSON-RPC call to the in-process kube handler via httptest and
// returns the `result` field. Handles both JSON and SSE responses.
func (p *Server) kubeRPC(ctx context.Context, h http.Handler, token, cluster, method string, params json.RawMessage) (json.RawMessage, error) {
	reqBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/mcp", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if cluster != "" {
		req.Header.Set("X-Kedge-Cluster", cluster)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		return nil, fmt.Errorf("kube MCP returned %d", rec.Code)
	}

	body := rec.Body.Bytes()
	rawJSON := body
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		d, ok := firstSSEDataLine(body)
		if !ok {
			return nil, fmt.Errorf("no data line in SSE response")
		}
		rawJSON = d
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rawJSON, &env); err != nil {
		return nil, fmt.Errorf("decode JSON-RPC envelope: %w", err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("kube MCP error %d: %s", env.Error.Code, env.Error.Message)
	}
	return env.Result, nil
}

// firstSSEDataLine returns the JSON payload of the first `data:` line in an SSE body.
func firstSSEDataLine(body []byte) (json.RawMessage, bool) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			return json.RawMessage(strings.TrimSpace(after)), true
		}
	}
	return nil, false
}
