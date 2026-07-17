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

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testMCPPath = "/some-cluster/apis/kedge.faros.sh/v1alpha1/mcpservers/default/mcp"

func TestParseMCPServerPath(t *testing.T) {
	cluster, name, ok := parseMCPServerPath(testMCPPath)
	if !ok || cluster != "some-cluster" || name != "default" {
		t.Fatalf("parse = (%q,%q,%v), want (some-cluster, default, true)", cluster, name, ok)
	}
	for _, bad := range []string{
		"/",
		"/cluster/apis/kedge.faros.sh/v1alpha1/mcpservers",           // too short
		"/cluster/apis/wrong.group/v1alpha1/mcpservers/default/mcp",  // wrong group
		"/cluster/apis/kedge.faros.sh/v1alpha1/mcpservers/default/x", // not /mcp
	} {
		if _, _, ok := parseMCPServerPath(bad); ok {
			t.Errorf("parseMCPServerPath(%q) = ok, want !ok", bad)
		}
	}
}

// jsonrpc POSTs a single JSON-RPC method to the handler (prefix already
// stripped) and returns the decoded envelope result + HTTP status.
func jsonrpc(t *testing.T, h http.Handler, method string, params string) (json.RawMessage, int) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	req := httptest.NewRequest(http.MethodPost, testMCPPath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	raw, _ := io.ReadAll(rr.Body)
	if rr.Code != http.StatusOK {
		return nil, rr.Code
	}
	payload := raw
	if strings.HasPrefix(rr.Header().Get("Content-Type"), "text/event-stream") {
		d, ok := firstSSEData(raw)
		if !ok {
			t.Fatalf("no SSE data line in response: %s", raw)
		}
		payload = d
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, payload)
	}
	return env.Result, http.StatusOK
}

// TestAlwaysOnEmptyAggregate is the core guarantee: with zero providers the
// endpoint still initializes and serves an (empty) tools/list — never 501.
func TestAlwaysOnEmptyAggregate(t *testing.T) {
	h := New(Options{Providers: func(context.Context) []ProviderTarget { return nil }})

	if _, code := jsonrpc(t, h, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`); code != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200 (endpoint must be always-on)", code)
	}

	result, code := jsonrpc(t, h, "tools/list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("tools/list status = %d, want 200", code)
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if len(out.Tools) != 0 {
		t.Fatalf("empty aggregate returned %d tools, want 0", len(out.Tools))
	}
}

// TestUnauthorizedAndBadPath covers the two request-level guards.
func TestUnauthorizedAndBadPath(t *testing.T) {
	h := New(Options{Providers: func(context.Context) []ProviderTarget { return nil }})

	noAuth := httptest.NewRequest(http.MethodPost, testMCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, noAuth)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("missing bearer: status = %d, want 401", rr.Code)
	}

	badPath := httptest.NewRequest(http.MethodPost, "/nope", strings.NewReader(`{}`))
	badPath.Header.Set("Authorization", "Bearer t")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, badPath)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad path: status = %d, want 400", rr.Code)
	}
}

// TestFederatesReadyProvider stands up a fake provider MCP server and checks
// its tool shows up namespaced as "<provider>__<tool>" in the aggregate, and
// that calling it proxies through.
func TestFederatesReadyProvider(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"provision","description":"make a thing","inputSchema":{"type":"object"}}]}}`))
		case "tools/call":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"provisioned"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	defer provider.Close()

	h := New(Options{Providers: func(context.Context) []ProviderTarget {
		return []ProviderTarget{{Name: "infra", DisplayName: "Infrastructure", MCPURL: provider.URL}}
	}})

	result, code := jsonrpc(t, h, "tools/list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("tools/list status = %d", code)
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, tl := range out.Tools {
		if tl.Name == "infra__provision" {
			found = true
		}
	}
	if !found {
		t.Fatalf("federated tool infra__provision not in aggregate; got %+v", out.Tools)
	}

	callResult, code := jsonrpc(t, h, "tools/call", `{"name":"infra__provision","arguments":{}}`)
	if code != http.StatusOK {
		t.Fatalf("tools/call status = %d", code)
	}
	if !strings.Contains(string(callResult), "provisioned") {
		t.Fatalf("tools/call did not proxy through; got %s", callResult)
	}
}
