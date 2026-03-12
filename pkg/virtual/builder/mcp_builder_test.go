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

package builder

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/rest"
)

const (
	testCluster  = "root:kedge:user-default"
	testEdgeName = "my-edge"
)

// newTestMCPVirtualWorkspaces builds a minimal virtualWorkspaces for MCP handler tests.
func newTestMCPVirtualWorkspaces() *virtualWorkspaces {
	return &virtualWorkspaces{
		kcpConfig:       &rest.Config{Host: "https://kcp.example.com"},
		staticTokens:    make(map[string]struct{}),
		edgeConnManager: NewConnManager(),
		hubExternalURL:  "https://kedge.example.com",
	}
}

// TestMCPHandler_missingToken verifies that a request without an Authorization
// header is rejected with HTTP 401.
func TestMCPHandler_missingToken(t *testing.T) {
	vws := newTestMCPVirtualWorkspaces()
	handler := vws.buildMCPHandler(testCluster, testEdgeName)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	// No Authorization header.
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 Unauthorized, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// TestMCPHandler_withToken verifies that a request with a valid bearer token
// reaches the MCP server layer (which initialises even with no connected edges).
// A fake kcp API server is used so the MCP server can be fully initialised
// without making real network calls.
func TestMCPHandler_withToken(t *testing.T) {
	// Start a fake kcp server that returns an empty response for all requests.
	fakeKCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"List","items":[],"metadata":{}}`))
	}))
	defer fakeKCP.Close()

	vws := &virtualWorkspaces{
		kcpConfig:       &rest.Config{Host: fakeKCP.URL},
		staticTokens:    make(map[string]struct{}),
		edgeConnManager: NewConnManager(),
		hubExternalURL:  "https://kedge.example.com",
	}
	handler := vws.buildMCPHandler(testCluster, testEdgeName)

	// A valid MCP initialize request.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set("Content-Type", "application/json")
	// MCP streamable HTTP spec requires both content types in Accept.
	req.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// MCP server should return 200 (request processed) or at least not 401.
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected request with token to pass auth, got 401")
	}
}

// TestMCPHandler_edgeNotConnected verifies that a handler for a non-connected
// edge still initialises the MCP server (with zero targets).
func TestMCPHandler_edgeNotConnected(t *testing.T) {
	vws := newTestMCPVirtualWorkspaces()
	handler := vws.buildMCPHandler(testCluster, "non-existent-edge")

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set("Content-Type", "application/json")
	// MCP streamable HTTP spec requires both content types in Accept.
	req.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Should not return 401 (auth passed) — MCP server handles the request.
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected auth to pass with bearer token, got 401")
	}
}

// TestMCPHandler_toolsListNotEmpty verifies that after connect+initialize via the
// go-sdk StreamableClientTransport, tools/list returns a non-empty list that
// includes "namespaces_list". This exercises the toolset side-effect imports.
func TestMCPHandler_toolsListNotEmpty(t *testing.T) {
	fakeKCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"List","items":[],"metadata":{}}`))
	}))
	defer fakeKCP.Close()

	vws := &virtualWorkspaces{
		kcpConfig:       &rest.Config{Host: fakeKCP.URL},
		staticTokens:    make(map[string]struct{}),
		edgeConnManager: NewConnManager(),
		hubExternalURL:  "https://kedge.example.com",
	}

	// Wrap the MCP handler so requests carry a valid Authorization header.
	inner := vws.buildMCPHandler(testCluster, testEdgeName)
	authedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer valid-token")
		inner.ServeHTTP(w, r)
	})

	// Serve on a real (local) httptest server so the go-sdk transport works.
	srv := httptest.NewServer(authedHandler)
	defer srv.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: srv.URL + "/"}
	session, err := client.Connect(t.Context(), transport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(result.Tools) == 0 {
		t.Fatal("tools/list: expected non-empty tools list — toolset side-effect imports may be missing")
	}
	t.Logf("tools/list returned %d tools", len(result.Tools))

	found := false
	for _, tool := range result.Tools {
		if tool.Name == "namespaces_list" {
			found = true
			break
		}
	}
	if !found {
		names := make([]string, 0, len(result.Tools))
		for _, tool := range result.Tools {
			names = append(names, tool.Name)
		}
		t.Errorf("tools/list: expected 'namespaces_list' in tools, got: %v", names)
	}
}
