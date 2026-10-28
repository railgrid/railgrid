// Copyright 2026 The Railgrid Authors.
// SPDX-License-Identifier: Apache-2.0

package mcpaggregate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
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
					result = map[string]any{"_meta": map[string]any{"private": "result-secret"}, "contents": []any{map[string]any{"uri": uri, "mimeType": "Text/HTML; profile=mcp-app", "text": "<html>report</html>", "_meta": map[string]any{"private": "resource-secret", "ui": map[string]any{"csp": map[string]any{}}}}}}
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
			raw := searchJS(t, h, "test-token", `() => ({tools: spec.tools.map(t => ({name: t.name, _meta: t._meta}))})`)
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
			raw, rpcError := appRPC(t, h, "resources/read", string(params))
			if mismatch {
				if !strings.Contains(rpcError, "identity mismatch") {
					t.Fatalf("expected identity mismatch, got %q (%s)", rpcError, raw)
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
			if len(resource.Contents) != 1 || resource.Contents[0].URI != uri || resource.Contents[0].Text != "<html>report</html>" || resource.Contents[0].MIMEType != providerMCPAppMIMEType {
				t.Fatalf("wrong resource: %s", raw)
			}
			params, _ = json.Marshal(map[string]string{"uri": strings.Replace(uri, "/reports/", "/other/", 1)})
			raw, rpcError = appRPC(t, h, "resources/read", string(params))
			if !strings.Contains(rpcError, "not found") {
				t.Fatalf("expected resource-not-found, got %q (%s)", rpcError, raw)
			}
			_, rpcError = appRPC(t, h, "tools/call", `{"name":"reports__private","arguments":{}}`)
			if !strings.Contains(rpcError, "unknown tool") {
				t.Fatalf("app-only calls must be unsupported: %q", rpcError)
			}
			if got := string(callViaExecute(t, h, "test-token", "reports__private", `{}`)); !strings.Contains(got, "unknown tool") {
				t.Fatalf("app-only tool reachable through execute: %s", got)
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
	if !validFederatedAppContent(&mcp.ResourceContents{MIMEType: "Text/HTML; profile=mcp-app", Text: "<!doctype html><html/>"}) {
		t.Fatal("valid standard app rejected")
	}
}

// Unlike the general helper, preserve JSON-RPC errors so negative cases prove
// the intended boundary rather than accidentally passing on unrelated errors.
func appRPC(t *testing.T, h http.Handler, method, params string) (json.RawMessage, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, testMCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("RPC HTTP status %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.Bytes()
	if strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/event-stream") {
		var ok bool
		body, ok = firstSSEData(body)
		if !ok {
			t.Fatal("missing RPC SSE data")
		}
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error != nil {
		return envelope.Result, envelope.Error.Message
	}
	return envelope.Result, ""
}

func TestAppResourceInputAndMetadataBounds(t *testing.T) {
	for _, uri := range []string{"ui://host/a b", "ui://host/a\n", "ui://host/\x7f", "ui:opaque", "ui://user:password@host/x", "ui://host/x#fragment"} {
		if got := appResourceURI(discoveredTool{Meta: map[string]any{"ui": map[string]any{"resourceUri": uri}}}); got != "" {
			t.Errorf("accepted unsafe URI %q", uri)
		}
	}
	// Queries are valid resource identifiers and retained exactly, never URLs to dial.
	if got := appResourceURI(discoveredTool{Meta: map[string]any{"ui": map[string]any{"resourceUri": "ui://host/report?version=3"}}}); got != "ui://host/report?version=3" {
		t.Fatal(got)
	}
	if appResourceMeta(mcp.Meta{"ui": map[string]any{"csp": strings.Repeat("x", providerMCPAppMetaBytes)}}) != nil {
		t.Fatal("accepted oversized UI metadata")
	}
}

func TestAppResourceTransportBoundDeadlineAndSSE(t *testing.T) {
	for _, scenario := range []string{"oversized", "deadline", "SSE"} {
		t.Run(scenario, func(t *testing.T) {
			cli := newProviderMCPClientWithTimeouts("", "tenant", time.Hour, 20*time.Millisecond)
			cli.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Header.Get("Authorization") != "" || request.Header.Get("X-Railgrid-Cluster") != "tenant" {
					t.Error("transport caller policy changed")
				}
				if scenario == "deadline" {
					<-request.Context().Done()
					return nil, request.Context().Err()
				}
				body := `{"jsonrpc":"2.0","id":1,"result":{"contents":[{"uri":"ui://example/report","mimeType":"text/html;profile=mcp-app","text":"<main>OK</main>"}]}}`
				kind := "application/json"
				if scenario == "oversized" {
					body = strings.Repeat("x", int(providerMCPAppEnvelopeBytes)+1)
				}
				if scenario == "SSE" {
					body = "event: message\ndata: " + body + "\n\n"
					kind = "text/event-stream"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{kind}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			target := &providerTools{provider: ProviderTarget{Name: "example", MCPURL: "http://not-a-network-destination.invalid"}, client: cli, tools: []discoveredTool{{Meta: map[string]any{"ui": map[string]any{"resourceUri": "ui://example/report"}}}}}
			srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			registerAppResources(t.Context(), srv, target, logr.Discard(), make(chan struct{}, providerMCPAppConcurrentReads))
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
			params, _ := json.Marshal(map[string]string{"uri": federatedAppURI("example", "ui://example/report")})
			result, rpcError := appRPC(t, handler, "resources/read", string(params))
			switch scenario {
			case "oversized":
				if !strings.Contains(rpcError, "response exceeds") {
					t.Fatalf("wrong transport bound error: %q", rpcError)
				}
			case "deadline":
				if !strings.Contains(rpcError, "deadline exceeded") {
					t.Fatalf("wrong timeout error: %q", rpcError)
				}
			default:
				if rpcError != "" || !strings.Contains(string(result), "OK") {
					t.Fatalf("SSE resource failed: %s %s", result, rpcError)
				}
			}
		})
	}
}

func TestInstructionFetchDoesNotClaimSessionAppNegotiation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			Params struct {
				Capabilities map[string]any `json:"capabilities"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Method != "initialize" || len(request.Params.Capabilities) != 0 {
			t.Errorf("unexpected instruction negotiation: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"instructions":"provider rules"}}`)
	}))
	defer upstream.Close()
	if got := newProviderMCPClient("caller", "tenant").fetchInstructions(t.Context(), upstream.URL); got != "provider rules" {
		t.Fatalf("instructions=%q", got)
	}
}

func TestMalformedAppRegistrationDoesNotPanic(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	handler := func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{}, nil
	}
	if err := addAppResource(server, &mcp.Resource{URI: ":invalid"}, handler); err == nil {
		t.Fatal("malformed registration accepted")
	}
	if err := addAppResource(server, &mcp.Resource{URI: "ui://valid/resource"}, handler); err != nil {
		t.Fatal(err)
	}
}

func TestProxyToolMetaValidatesVisibilityIndependently(t *testing.T) {
	for _, visibility := range []any{nil, "model", []any{}, []any{"model", 42}, []string{"model", "private"}, map[string]any{"private": "metadata"}} {
		tool := discoveredTool{Meta: map[string]any{"ui": map[string]any{"resourceUri": "ui://reports/v1", "visibility": visibility}}}
		if meta := proxyToolMeta(ProviderTarget{Name: "reports"}, tool); meta["ui"] != nil {
			t.Fatalf("forwarded invalid visibility %#v", visibility)
		}
	}
	values := []string{"app", "model"}
	tool := discoveredTool{Meta: map[string]any{"ui": map[string]any{"resourceUri": "ui://reports/v1", "visibility": values}}}
	meta := proxyToolMeta(ProviderTarget{Name: "reports"}, tool)
	values[0] = "private"
	encoded, err := json.Marshal(meta["ui"])
	if err != nil || !strings.Contains(string(encoded), `"visibility":["model","app"]`) {
		t.Fatalf("visibility was not normalized and copied: %s (%v)", encoded, err)
	}
}

func TestAppResourceNamesAndSharedReadCapacity(t *testing.T) {
	var resourceReads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string               `json:"method"`
			Params struct{ URI string } `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var result any = map[string]any{}
		switch request.Method {
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "report", "inputSchema": map[string]string{"type": "object"}, "_meta": map[string]any{"ui": map[string]any{"resourceUri": "ui://reports/v1"}}},
				map[string]any{"name": "chart", "inputSchema": map[string]string{"type": "object"}, "_meta": map[string]any{"ui": map[string]any{"resourceUri": "ui://reports/chart"}}},
			}}
		case "resources/read":
			resourceReads.Add(1)
			result = map[string]any{"contents": []any{map[string]any{"uri": request.Params.URI, "mimeType": providerMCPAppMIMEType, "text": "<html>report</html>"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer upstream.Close()
	h := New(Options{Providers: func(context.Context, Caller) []ProviderTarget {
		return []ProviderTarget{{Name: "reports", MCPURL: upstream.URL}}
	}, Verifier: allowAll}).(*handler)
	listing, rpcError := appRPC(t, h, "resources/list", `{}`)
	if rpcError != "" || !strings.Contains(string(listing), `"name":"reports report app"`) || !strings.Contains(string(listing), `"name":"reports chart app"`) {
		t.Fatalf("indistinguishable resources: %s (%s)", listing, rpcError)
	}
	for range providerMCPAppConcurrentReads {
		h.appReadSlots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	params, _ := json.Marshal(map[string]string{"uri": federatedAppURI("reports", "ui://reports/v1")})
	request := httptest.NewRequest(http.MethodPost, testMCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":`+string(params)+`}`)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	recorder := httptest.NewRecorder()
	started := time.Now()
	h.ServeHTTP(recorder, request)
	if time.Since(started) > time.Second {
		t.Fatal("caller cancellation did not promptly release the waiting read")
	}
	if ctx.Err() == nil || resourceReads.Load() != 0 {
		t.Fatalf("saturated read did not wait for caller cancellation: err=%v reads=%d", ctx.Err(), resourceReads.Load())
	}
	<-h.appReadSlots
	result, rpcError := appRPC(t, h, "resources/read", string(params))
	if rpcError != "" || !strings.Contains(string(result), "report</html>") {
		t.Fatalf("read did not recover after capacity returned: %s (%s)", result, rpcError)
	}
	if len(h.appReadSlots) != providerMCPAppConcurrentReads-1 {
		t.Fatal("read did not release its capacity")
	}
}
