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

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/mcpaggregate"
)

// proxyRun feeds lines to a proxy aimed at srv and returns what it wrote to
// stdout, one JSON-RPC message per element, sorted (requests are relayed
// concurrently, so replies may come back in any order).
func proxyRun(t *testing.T, resolve func(context.Context) (*mcpProxyTarget, error), lines ...string) []string {
	t.Helper()
	var out, errOut bytes.Buffer
	p := newMCPProxy(resolve, &out, &errOut)
	if err := p.run(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatalf("run: %v", err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l != "" {
			got = append(got, l)
		}
	}
	sort.Strings(got)
	return got
}

func staticTarget(srv *httptest.Server) func(context.Context) (*mcpProxyTarget, error) {
	return func(context.Context) (*mcpProxyTarget, error) {
		return &mcpProxyTarget{url: srv.URL + "/mcp", client: srv.Client()}, nil
	}
}

func TestMCPProxyRelaysJSONAndSSEReplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json, text/event-stream" {
			t.Errorf("Accept = %q", got)
		}
		var msg jsonRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		switch msg.Method {
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			// A multi-line body must come out as one line.
			_, _ = fmt.Fprintf(w, "{\n  \"jsonrpc\": \"2.0\",\n  \"id\": %s,\n  \"result\": {\"tools\": []}\n}\n", msg.ID)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
			_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[]}}\n\n", msg.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()

	got := proxyRun(t, staticTarget(srv),
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"x"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	)
	want := []string{
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"content":[]}}`,
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{}}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("stdout =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The protocol version the hub negotiated on initialize is declared on every
// later request.
func TestMCPProxyDeclaresNegotiatedProtocolVersion(t *testing.T) {
	var seen sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg jsonRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		seen.Store(msg.Method, r.Header.Get("MCP-Protocol-Version"))
		w.Header().Set("Content-Type", "application/json")
		if msg.Method == "initialize" {
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, msg.ID)
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, msg.ID)
	}))
	defer srv.Close()

	var out bytes.Buffer
	p := newMCPProxy(staticTarget(srv), &out, io.Discard)
	p.handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	p.handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))

	if v, _ := seen.Load("initialize"); v != "" {
		t.Errorf("initialize declared protocol version %q before negotiating one", v)
	}
	if v, _ := seen.Load("tools/list"); v != "2025-06-18" {
		t.Errorf("tools/list MCP-Protocol-Version = %q, want 2025-06-18", v)
	}
}

// A 401 reloads the credentials and retries once; a second 401 becomes a
// JSON-RPC error telling the user to log in, and the next message resolves
// the credentials afresh.
func TestMCPProxyRetriesOnceAfter401(t *testing.T) {
	var accept atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !accept.Load() {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var msg jsonRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, msg.ID)
	}))
	defer srv.Close()

	var resolves atomic.Int32
	resolve := func(ctx context.Context) (*mcpProxyTarget, error) {
		// The retry after the first 401 is when a fresh login shows up.
		if resolves.Add(1) == 2 {
			accept.Store(true)
		}
		return staticTarget(srv)(ctx)
	}
	got := proxyRun(t, resolve, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if len(got) != 1 || got[0] != `{"jsonrpc":"2.0","id":1,"result":{}}` {
		t.Fatalf("stdout = %v, want the reply to the retried request", got)
	}
	if n := resolves.Load(); n != 2 {
		t.Fatalf("resolves = %d, want 2", n)
	}

	accept.Store(false)
	resolves.Store(10)
	got = proxyRun(t, resolve, `{"jsonrpc":"2.0","id":"a","method":"tools/list","params":{}}`)
	if len(got) != 1 || !strings.Contains(got[0], `"id":"a"`) || !strings.Contains(got[0], "run 'railgrid login'") {
		t.Fatalf("stdout = %v, want a JSON-RPC error for id \"a\" with a login hint", got)
	}
}

// Failures reach the client as a JSON-RPC error on the request's id — never
// silence, which would hang the client until its own timeout.
func TestMCPProxyReportsFailuresAsJSONRPCErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	got := proxyRun(t, staticTarget(srv), `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`)
	var reply struct {
		ID    int `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if len(got) != 1 || json.Unmarshal([]byte(got[0]), &reply) != nil || reply.ID != 7 ||
		reply.Error.Code != mcpProxyErrorCode || !strings.Contains(reply.Error.Message, "HTTP 403") {
		t.Fatalf("stdout = %v, want a JSON-RPC error for id 7 carrying the 403", got)
	}

	// Not logged in yet: the error says why, and a later message tries again.
	var calls atomic.Int32
	resolve := func(context.Context) (*mcpProxyTarget, error) {
		calls.Add(1)
		return nil, errors.New(`no "railgrid" context found in kubeconfig — run 'railgrid login' first`)
	}
	got = proxyRun(t, resolve,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	)
	if len(got) != 2 || !strings.Contains(got[0], "railgrid login") {
		t.Fatalf("stdout = %v, want two JSON-RPC errors with the login hint", got)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("resolve calls = %d, want 2 (a failed resolution is retried)", n)
	}
}

// notifications/cancelled aborts the in-flight POST, and the cancelled
// request gets no reply.
func TestMCPProxyCancelsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	aborted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server only notices the client going away once the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(aborted)
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	p := newMCPProxy(staticTarget(srv), &out, io.Discard)
	done := make(chan struct{})
	go func() {
		p.handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{}}`))
		close(done)
	}()
	<-started
	p.handle(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}`))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled request is still in flight")
	}
	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("the hub never saw the request aborted")
	}
	if out.Len() != 0 {
		t.Fatalf("cancelled request got a reply: %s", out.String())
	}
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// The proxy against the real aggregate handler: the handshake, a
// notification, tools/list and a federated tools/call all go through the
// SDK's stateless streamable-HTTP server as an MCP client would drive them.
func TestMCPProxyAgainstAggregate(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg jsonRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"provision","inputSchema":{"type":"object"}}]}}`))
		case "tools/call":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"provisioned"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	defer provider.Close()

	var bearers sync.Map
	agg := mcpaggregate.New(mcpaggregate.Options{
		Providers: func(context.Context, mcpaggregate.Caller) []mcpaggregate.ProviderTarget {
			return []mcpaggregate.ProviderTarget{{Name: "infra", MCPURL: provider.URL}}
		},
		Verifier: mcpaggregate.BearerVerifierFunc(func(_ *http.Request, token, _, _ string) (mcpaggregate.Caller, error) {
			bearers.Store(token, true)
			return mcpaggregate.Caller{User: "alice"}, nil
		}),
	})
	hub := httptest.NewServer(http.StripPrefix(mcpaggregate.PathPrefix, agg))
	defer hub.Close()

	resolve := func(context.Context) (*mcpProxyTarget, error) {
		return &mcpProxyTarget{
			url:    apiurl.MCPServerURL(hub.URL, "some-cluster", "default"),
			client: &http.Client{Transport: bearerTransport{token: "alice-token"}},
		}, nil
	}
	var out bytes.Buffer
	p := newMCPProxy(resolve, &out, io.Discard)
	for _, msg := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"infra__provision","arguments":{}}}`,
	} {
		p.handle(context.Background(), []byte(msg))
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout has %d messages, want 3 (no reply to the notification):\n%s", len(lines), out.String())
	}
	for i, want := range []string{`"protocolVersion":"2025-06-18"`, `"name":"infra__provision"`, `provisioned`} {
		if !strings.Contains(lines[i], want) || !strings.Contains(lines[i], fmt.Sprintf(`"id":%d`, i+1)) {
			t.Errorf("reply %d = %s, want id %d containing %s", i+1, lines[i], i+1, want)
		}
	}
	if _, ok := bearers.Load("alice-token"); !ok {
		t.Error("the aggregate never saw the user's bearer")
	}
}
