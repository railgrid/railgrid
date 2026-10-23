// Copyright 2026 The Railgrid Authors.
// SPDX-License-Identifier: Apache-2.0

package mcpaggregate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPAppFederationScopesResourcesAndPreservesCaller(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "wrong-resource"}[mismatch], func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-Railgrid-Cluster") == "" {
					t.Error("resource lost caller/tenant")
				}
				var req struct {
					Method string `json:"method"`
					Params struct {
						URI string `json:"uri"`
					} `json:"params"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				var result any
				switch req.Method {
				case "tools/list":
					result = map[string]any{"tools": []any{map[string]any{"name": "report", "inputSchema": map[string]string{"type": "object"}, "_meta": map[string]any{"ui": map[string]any{"resourceUri": "ui://reports/v1"}, "private": "not-forwarded"}}, map[string]any{"name": "private", "inputSchema": map[string]string{"type": "object"}, "_meta": map[string]any{"ui": map[string]any{"visibility": []string{"app"}, "resourceUri": "ui://reports/private"}}}}}
				case "resources/read":
					if req.Params.URI != "ui://reports/v1" {
						t.Errorf("wrong upstream URI %q", req.Params.URI)
					}
					uri := req.Params.URI
					if mismatch {
						uri = "ui://another"
					}
					result = map[string]any{"_meta": map[string]any{"private": "result-secret"}, "contents": []any{map[string]any{"uri": uri, "mimeType": "text/html;profile=mcp-app", "text": "<html>report</html>", "_meta": map[string]any{"private": "resource-secret", "ui": map[string]any{"csp": map[string]any{}}}}}}
				default:
					result = map[string]any{}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
			}))
			defer upstream.Close()
			h := New(Options{Providers: func(context.Context, Caller) []ProviderTarget {
				return []ProviderTarget{{Name: "reports", MCPURL: upstream.URL}}
			}, Verifier: allowAll})
			raw, _ := jsonrpc(t, h, "tools/list", `{}`)
			var listing struct {
				Tools []struct {
					Name string         `json:"name"`
					Meta map[string]any `json:"_meta"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(raw, &listing); err != nil {
				t.Fatal(err)
			}
			var uri string
			for _, tool := range listing.Tools {
				if tool.Name == "reports__private" {
					t.Error("app-only tool leaked to model")
				}
				if tool.Name == "reports__report" {
					uri = tool.Meta["ui"].(map[string]any)["resourceUri"].(string)
					if tool.Meta["private"] != nil {
						t.Error("private metadata leaked")
					}
				}
			}
			if uri == "" || uri == "ui://reports/v1" {
				t.Fatalf("resource not namespaced: %s", uri)
			}
			params, _ := json.Marshal(map[string]string{"uri": uri})
			raw, _ = jsonrpc(t, h, "resources/read", string(params))
			if mismatch {
				if len(raw) > 0 && string(raw) != "null" {
					t.Fatalf("mismatched upstream resource returned: %s", raw)
				}
				return
			}
			if strings.Contains(string(raw), "secret") || !strings.Contains(string(raw), `"csp"`) {
				t.Fatalf("resource metadata was not filtered: %s", raw)
			}
			var resource struct {
				Contents []struct{ URI, Text, MIMEType string } `json:"contents"`
			}
			if err := json.Unmarshal(raw, &resource); err != nil {
				t.Fatal(err)
			}
			if len(resource.Contents) != 1 || resource.Contents[0].URI != uri || resource.Contents[0].Text != "<html>report</html>" {
				t.Fatalf("wrong resource: %s", raw)
			}
			params, _ = json.Marshal(map[string]string{"uri": strings.Replace(uri, "/reports/", "/other/", 1)})
			raw, _ = jsonrpc(t, h, "resources/read", string(params))
			if len(raw) > 0 && string(raw) != "null" {
				t.Fatalf("undeclared resource returned: %s", raw)
			}
		})
	}
}

func TestFederatedAppContentRequiresHTMLAndIndependentBound(t *testing.T) {
	for name, content := range map[string]*mcp.ResourceContents{
		"nil":        nil,
		"wrong MIME": {MIMEType: "text/html", Text: "<html/>"},
		"empty":      {MIMEType: "text/html;profile=mcp-app"},
		"ambiguous":  {MIMEType: "text/html;profile=mcp-app", Text: "<html/>", Blob: []byte("<html/>")},
		"oversized":  {MIMEType: "text/html;profile=mcp-app", Text: strings.Repeat("x", (1<<20)+1)},
	} {
		if validFederatedAppContent(content) {
			t.Errorf("accepted %s app resource", name)
		}
	}
	if !validFederatedAppContent(&mcp.ResourceContents{MIMEType: "text/html;profile=mcp-app", Text: "<!doctype html><html/>"}) {
		t.Fatal("valid standard app rejected")
	}
}
