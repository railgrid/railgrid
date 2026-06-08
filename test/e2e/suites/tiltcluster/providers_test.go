/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tiltcluster

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	catalogEntryGVR   = schema.GroupVersionResource{Group: "providers.kedge.faros.sh", Version: "v1alpha1", Resource: "catalogentries"}
	apiExportGVR      = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	cachedResGVR      = schema.GroupVersionResource{Group: "cache.kcp.io", Version: "v1alpha1", Resource: "cachedresources"}
	templatesGVR      = schema.GroupVersionResource{Group: infraGroup, Version: "v1alpha1", Resource: "templates"}
	cachedTemplates   = "publish-templates"
	wantTemplateNames = []string{"redis-cache", "simple-webapp"}
)

// TestInfrastructureProviderRegistered asserts the out-of-process
// infrastructure provider bootstrapped its workspace against the operator
// kcp: its CatalogEntry is Ready and its APIExport carries the templates
// resource. This is the "provider comes up" gate.
func TestInfrastructureProviderRegistered(t *testing.T) {
	requireStack(t)
	ctx := context.Background()

	ce, err := kcpAdminDynamic(t, providersWorkspace).
		Resource(catalogEntryGVR).Get(ctx, providerName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CatalogEntry %q in %s: %v", providerName, providersWorkspace, err)
	}
	if !conditionTrue(ce.Object, "Ready") {
		t.Fatalf("CatalogEntry %q not Ready; conditions=%v", providerName, conditionsOf(ce.Object))
	}

	ex, err := kcpAdminDynamic(t, providerWorkspace).
		Resource(apiExportGVR).Get(ctx, infraAPIExportName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get APIExport %q in %s: %v", infraAPIExportName, providerWorkspace, err)
	}
	if !apiExportHasResource(ex.Object, "templates", infraGroup) {
		t.Fatalf("APIExport %q missing templates resource; spec.resources=%v",
			infraAPIExportName, nestedSlice(ex.Object, "spec", "resources"))
	}
	t.Logf("infrastructure provider registered: CatalogEntry Ready + APIExport %s exports templates", infraAPIExportName)
}

// TestTemplatesCatalogProjected asserts the broker catalog is materialized:
// the seeded Templates exist in the provider workspace and the CachedResource
// that projects them into tenant workspaces is Ready with replicated objects.
// This is the catalog/projection half of the templates broker chain.
func TestTemplatesCatalogProjected(t *testing.T) {
	requireStack(t)
	ctx := context.Background()
	cl := kcpAdminDynamic(t, providerWorkspace)

	list, err := cl.Resource(templatesGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list templates in %s: %v", providerWorkspace, err)
	}
	got := map[string]bool{}
	for _, it := range list.Items {
		got[it.GetName()] = true
	}
	for _, want := range wantTemplateNames {
		if !got[want] {
			t.Fatalf("expected template %q in provider workspace; got %v", want, keys(got))
		}
	}

	cr, err := cl.Resource(cachedResGVR).Get(ctx, cachedTemplates, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CachedResource %q: %v", cachedTemplates, err)
	}
	if !conditionTrue(cr.Object, "ReplicationStarted") && phaseOf(cr.Object) != "Ready" {
		t.Fatalf("CachedResource %q not ready: phase=%s conditions=%v", cachedTemplates, phaseOf(cr.Object), conditionsOf(cr.Object))
	}
	t.Logf("templates catalog projected: %d templates, CachedResource %q phase=%s", len(list.Items), cachedTemplates, phaseOf(cr.Object))
}

// TestInfraMCPToolsFederatable asserts the infrastructure provider exposes its
// MCP tools over /mcp — the source the hub aggregate federates as
// `infrastructure__<tool>`. We hit the provider's /mcp directly with the same
// JSON-RPC shape the hub's federation client uses.
func TestInfraMCPToolsFederatable(t *testing.T) {
	requireStack(t)
	tools, err := mcpToolNames(infraURL+"/mcp", "", "")
	if err != nil {
		t.Fatalf("tools/list against %s/mcp: %v", infraURL, err)
	}
	for _, want := range []string{"list_templates", "describe_template", "provision"} {
		if !slices.Contains(tools, want) {
			t.Fatalf("infrastructure MCP missing tool %q; got %v", want, tools)
		}
	}
	t.Logf("infrastructure MCP federatable: tools=%v (aggregate exposes them as %s__<tool>)", tools, providerName)
}

// TestTenantIsolationRequiresIdentity asserts the provider refuses tenant work
// without a caller identity: a tools/call with neither X-Kedge-Tenant nor a
// bearer token is rejected, rather than silently acting cross-tenant. This is
// the per-tenant isolation gate that backs the "act as the caller" model.
func TestTenantIsolationRequiresIdentity(t *testing.T) {
	requireStack(t)
	res, rpcErr, err := mcpCallTool(infraURL+"/mcp", "", "", "list_templates", map[string]any{})
	if err != nil {
		// A transport/protocol error (e.g. 400) is also an acceptable refusal.
		if looksLikeIdentityRefusal(err.Error()) {
			t.Logf("identity gate enforced (transport): %v", err)
			return
		}
		t.Fatalf("list_templates without identity: unexpected transport error: %v", err)
	}
	if rpcErr != "" && looksLikeIdentityRefusal(rpcErr) {
		t.Logf("identity gate enforced (rpc error): %s", rpcErr)
		return
	}
	if res != "" && looksLikeIdentityRefusal(res) {
		t.Logf("identity gate enforced (tool error): %s", res)
		return
	}
	t.Fatalf("expected list_templates without tenant/token to be refused; rpcErr=%q result=%q", rpcErr, res)
}

func looksLikeIdentityRefusal(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "tenant") || strings.Contains(s, "bearer token") ||
		strings.Contains(s, "identity") || strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "x-kedge-tenant")
}

// --- MCP-over-HTTP (mirrors providers/mcp/aggregate/provider_proxy.go rpc) --

func mcpToolNames(mcpURL, token, tenant string) ([]string, error) {
	raw, _, err := mcpRPC(mcpURL, "tools/list", json.RawMessage(`{}`), token, tenant)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Tools))
	for _, tl := range out.Tools {
		names = append(names, tl.Name)
	}
	return names, nil
}

// mcpCallTool returns (resultText, rpcErrorMessage, transportErr).
func mcpCallTool(mcpURL, token, tenant, name string, args map[string]any) (string, string, error) {
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	raw, rpcErr, err := mcpRPC(mcpURL, "tools/call", params, token, tenant)
	if err != nil {
		return "", "", err
	}
	return string(raw), rpcErr, nil
}

// mcpRPC does one JSON-RPC POST and returns (result, jsonrpcErrorMessage, transportErr).
func mcpRPC(mcpURL, method string, params json.RawMessage, token, tenant string) (json.RawMessage, string, error) {
	reqBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant != "" {
		req.Header.Set("X-Kedge-Tenant", tenant)
	}
	resp, err := insecureClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return nil, "", &httpError{code: resp.StatusCode, body: string(body)}
	}
	raw := body
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		if d, ok := firstSSEData(body); ok {
			raw = d
		}
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, "", err
	}
	if env.Error != nil {
		return nil, env.Error.Message, nil
	}
	return env.Result, "", nil
}

type httpError struct {
	code int
	body string
}

func (e *httpError) Error() string { return "http " + strconv.Itoa(e.code) + ": " + e.body }

func firstSSEData(b []byte) ([]byte, bool) {
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), true
		}
	}
	return nil, false
}

// --- unstructured field helpers -------------------------------------------

func conditionsOf(obj map[string]any) []any { return nestedSlice(obj, "status", "conditions") }

func conditionTrue(obj map[string]any, condType string) bool {
	for _, c := range conditionsOf(obj) {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cm["type"] == condType && cm["status"] == "True" {
			return true
		}
	}
	return false
}

func phaseOf(obj map[string]any) string {
	if s, ok := obj["status"].(map[string]any); ok {
		if p, ok := s["phase"].(string); ok {
			return p
		}
	}
	return ""
}

func apiExportHasResource(obj map[string]any, name, group string) bool {
	for _, r := range nestedSlice(obj, "spec", "resources") {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if rm["name"] == name && rm["group"] == group {
			return true
		}
	}
	return false
}

func nestedSlice(obj map[string]any, path ...string) []any {
	cur := obj
	for i, k := range path {
		v, ok := cur[k]
		if !ok {
			return nil
		}
		if i == len(path)-1 {
			if s, ok := v.([]any); ok {
				return s
			}
			return nil
		}
		cur, ok = v.(map[string]any)
		if !ok {
			return nil
		}
	}
	return nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
