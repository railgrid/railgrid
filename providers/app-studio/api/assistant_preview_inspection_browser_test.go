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

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func browserMCPTestEventStreamResponse(request *http.Request) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "text/event-stream")
	if sessionID := request.Header.Get("Mcp-Session-Id"); sessionID != "" {
		header.Set("Mcp-Session-Id", sessionID)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       &testBrowserEventStreamBody{closed: make(chan struct{})},
	}
}

// A representative Playwright MCP browser_snapshot result: the "Page URL" /
// "Page Title" preamble plus a fenced YAML accessibility tree.
const browserMCPSampleSnapshot = "- Page URL: https://demo.preview.example/admin\n" +
	"- Page Title: Vireo Admin\n" +
	"- Page Snapshot:\n" +
	"```yaml\n" +
	"- generic [ref=e1]:\n" +
	"  - heading \"Admin Login\" [level=1] [ref=e2]\n" +
	"  - textbox \"Email\" [ref=e3]\n" +
	"  - textbox \"Password\" [ref=e4]\n" +
	"  - button \"Sign in\" [ref=e5]\n" +
	"  - link \"Back to store\" [ref=e6]\n" +
	"```\n"

func TestBrowserMCPParseFieldsAndTree(t *testing.T) {
	if got := browserMCPParseField(browserMCPSampleSnapshot, "Page URL", "fallback"); got != "https://demo.preview.example/admin" {
		t.Fatalf("Page URL = %q", got)
	}
	if got := browserMCPParseField(browserMCPSampleSnapshot, "Page Title", ""); got != "Vireo Admin" {
		t.Fatalf("Page Title = %q", got)
	}
	if got := browserMCPParseField(browserMCPSampleSnapshot, "Absent", "fallback"); got != "fallback" {
		t.Fatalf("missing field should fall back, got %q", got)
	}
	tree := browserMCPExtractSnapshotTree(browserMCPSampleSnapshot)
	if want := "- generic [ref=e1]:"; tree == "" || tree[:len(want)] != want {
		t.Fatalf("extracted tree = %q", tree)
	}
	nodes := browserMCPParseAccessibilityNodes(tree)
	if len(nodes) != 6 {
		t.Fatalf("parsed %d nodes, want 6: %+v", len(nodes), nodes)
	}
	if nodes[1].role != "heading" || nodes[1].name != "Admin Login" {
		t.Fatalf("node[1] = %+v", nodes[1])
	}
}

func TestBrowserMCPEvaluateAssertion(t *testing.T) {
	tree := browserMCPExtractSnapshotTree(browserMCPSampleSnapshot)
	nodes := browserMCPParseAccessibilityNodes(tree)
	intp := func(n int) *int { return &n }

	cases := []struct {
		name      string
		assertion projectAssistantPreviewInspectionAssertion
		want      bool
		wantCount int
	}{
		{"text present in a name", projectAssistantPreviewInspectionAssertion{Kind: "text_present", Text: "Admin Login"}, true, 1},
		{"text absent", projectAssistantPreviewInspectionAssertion{Kind: "text_present", Text: "Checkout"}, false, 0},
		{"role present", projectAssistantPreviewInspectionAssertion{Kind: "role_present", Role: "button"}, true, 1},
		{"role present by name", projectAssistantPreviewInspectionAssertion{Kind: "role_present", Role: "button", Name: "Sign in"}, true, 1},
		{"role present missing", projectAssistantPreviewInspectionAssertion{Kind: "role_present", Role: "checkbox"}, false, 0},
		{"role count in range", projectAssistantPreviewInspectionAssertion{Kind: "role_count", Role: "textbox", Min: intp(2), Max: intp(2)}, true, 2},
		{"role count under min", projectAssistantPreviewInspectionAssertion{Kind: "role_count", Role: "textbox", Min: intp(3)}, false, 2},
		{"role count over max", projectAssistantPreviewInspectionAssertion{Kind: "role_count", Role: "textbox", Max: intp(1)}, false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := browserMCPEvaluateAssertion(tc.assertion, tree, nodes)
			if got.Passed != tc.want {
				t.Fatalf("Passed = %v, want %v (message %q)", got.Passed, tc.want, got.Message)
			}
			if got.ActualCount == nil || *got.ActualCount != tc.wantCount {
				t.Fatalf("ActualCount = %v, want %d", got.ActualCount, tc.wantCount)
			}
		})
	}
}

func TestBrowserMCPTextPresentFallsBackToRawSnapshot(t *testing.T) {
	// "Back to store" is a link name; a substring only present in raw copy still
	// counts via the snapshot-containment fallback (non-exact).
	tree := browserMCPExtractSnapshotTree(browserMCPSampleSnapshot)
	nodes := browserMCPParseAccessibilityNodes(tree)
	got := browserMCPEvaluateAssertion(projectAssistantPreviewInspectionAssertion{Kind: "text_present", Text: "ref=e1"}, tree, nodes)
	if !got.Passed {
		t.Fatalf("raw-snapshot containment should pass, message %q", got.Message)
	}
}

func TestBrowserMCPParseConsole(t *testing.T) {
	events := browserMCPParseConsole("[LOG] booted\n[ERROR] boom\nplain line\n")
	if len(events) != 3 {
		t.Fatalf("parsed %d events, want 3: %+v", len(events), events)
	}
	if events[0].Level != "log" || events[0].Message != "booted" {
		t.Fatalf("event[0] = %+v", events[0])
	}
	if events[1].Level != "error" || events[1].Message != "boom" {
		t.Fatalf("event[1] = %+v", events[1])
	}
	if events[2].Level != "log" || events[2].Message != "plain line" {
		t.Fatalf("event[2] = %+v", events[2])
	}
}

func TestBrowserMCPSessionSendsProtocolVersionAfterInitialize(t *testing.T) {
	server := &Server{hubBase: "https://hub.example"}
	seen := map[string]string{}
	var traceMu sync.Mutex
	var trace []projectAssistantBrowserTraceEvent
	restoreTrace := setProjectAssistantBrowserTraceHook(func(event projectAssistantBrowserTraceEvent) {
		traceMu.Lock()
		defer traceMu.Unlock()
		trace = append(trace, event)
	})
	defer restoreTrace()
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				seen[http.MethodGet] = request.Header.Get("MCP-Protocol-Version")
				return browserMCPTestEventStreamResponse(request), nil
			}
			if request.Method == http.MethodDelete {
				seen[http.MethodDelete] = request.Header.Get("MCP-Protocol-Version")
				recorder := httptest.NewRecorder()
				recorder.WriteHeader(http.StatusNoContent)
				return recorder.Result(), nil
			}
			var envelope struct {
				Method string `json:"method"`
			}
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				return nil, err
			}
			seen[envelope.Method] = request.Header.Get("MCP-Protocol-Version")
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Content-Type", "application/json")
			switch envelope.Method {
			case "initialize":
				recorder.Header().Set("Mcp-Session-Id", "protocol-test-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{
					"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2025-06-18"},
				})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			case "tools/list":
				_ = json.NewEncoder(recorder).Encode(map[string]any{
					"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []any{}},
				})
			default:
				return nil, errors.New("unexpected MCP method " + envelope.Method)
			}
			return recorder.Result(), nil
		})}
	}
	session, err := server.newBrowserMCPSession(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"})
	if err != nil {
		t.Fatalf("new browser MCP session: %v", err)
	}
	if _, err := session.rpc(context.Background(), "tools/list", map[string]any{}); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	session.close()
	if got := seen["initialize"]; got != "" {
		t.Fatalf("initialize protocol header = %q, want absent before session negotiation", got)
	}
	for _, method := range []string{"notifications/initialized", "tools/list", http.MethodGet, http.MethodDelete} {
		if got, want := seen[method], "2025-06-18"; got != want {
			t.Fatalf("%s protocol header = %q, want %q", method, got, want)
		}
	}
	traceMu.Lock()
	traceSnapshot := append([]projectAssistantBrowserTraceEvent(nil), trace...)
	traceMu.Unlock()
	var created, listed, closed bool
	for _, event := range traceSnapshot {
		if strings.Contains(event.SessionHash, "protocol-test-session") || strings.Contains(event.RefHash, "https://") {
			t.Fatalf("browser trace leaked raw session or URL: %#v", event)
		}
		switch {
		case event.Event == "session_create" && event.Role == projectAssistantBrowserSessionRoleUnspecified:
			created = true
		case event.Event == "rpc_response" && event.Method == "tools/list" && event.Status == http.StatusOK && event.SessionHeaderBeforeHash != "" && event.SessionHeaderAfterHash != "":
			listed = true
		case event.Event == "session_close_response" && event.Status == http.StatusNoContent && event.Reason == "unspecified":
			closed = true
		}
	}
	if !created || !listed || !closed {
		t.Fatalf("browser trace missing create/list/close lifecycle: %#v", trace)
	}
}

func TestBrowserMCPSessionRejectsInvalidNegotiatedProtocolVersion(t *testing.T) {
	cases := []struct {
		name   string
		result map[string]any
	}{
		{name: "missing", result: map[string]any{}},
		{name: "unsupported", result: map[string]any{"protocolVersion": "2099-01-01"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{hubBase: "https://hub.example"}
			server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
				return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					recorder := httptest.NewRecorder()
					if request.Method == http.MethodDelete {
						recorder.WriteHeader(http.StatusNoContent)
						return recorder.Result(), nil
					}
					recorder.Header().Set("Content-Type", "application/json")
					recorder.Header().Set("Mcp-Session-Id", "invalid-protocol-session")
					_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": tc.result})
					return recorder.Result(), nil
				})}
			}
			_, err := server.newBrowserMCPSession(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"})
			if err == nil || !strings.Contains(err.Error(), "protocolVersion") {
				t.Fatalf("invalid negotiated protocol error = %v", err)
			}
		})
	}
}

type testBrowserEventStreamBody struct {
	payload []byte
	closed  chan struct{}
	once    sync.Once
	mu      sync.Mutex
	sent    bool
}

func (body *testBrowserEventStreamBody) Read(p []byte) (int, error) {
	body.mu.Lock()
	if !body.sent && len(body.payload) > 0 {
		body.sent = true
		n := copy(p, body.payload)
		body.mu.Unlock()
		return n, nil
	}
	body.mu.Unlock()
	<-body.closed
	return 0, io.EOF
}

func (body *testBrowserEventStreamBody) Close() error {
	body.once.Do(func() { close(body.closed) })
	return nil
}

type testBrowserEventStreamErrorBody struct {
	started chan struct{}
	once    sync.Once
}

func (body *testBrowserEventStreamErrorBody) Read([]byte) (int, error) {
	body.once.Do(func() { close(body.started) })
	return 0, io.ErrUnexpectedEOF
}

func (body *testBrowserEventStreamErrorBody) Close() error { return nil }

func TestBrowserMCPSessionInvalidatesUnexpectedEventStreamReadFailure(t *testing.T) {
	server := &Server{hubBase: "https://hub.example"}
	streamBody := &testBrowserEventStreamErrorBody{started: make(chan struct{})}
	deleteCalls := 0
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       streamBody,
				}, nil
			}
			if request.Method == http.MethodDelete {
				deleteCalls++
				recorder := httptest.NewRecorder()
				recorder.WriteHeader(http.StatusNoContent)
				return recorder.Result(), nil
			}
			var envelope struct {
				Method string `json:"method"`
			}
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				return nil, err
			}
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Content-Type", "application/json")
			switch envelope.Method {
			case "initialize":
				recorder.Header().Set("Mcp-Session-Id", "stream-error-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			default:
				return nil, fmt.Errorf("unexpected MCP method %q after stream failure", envelope.Method)
			}
			return recorder.Result(), nil
		})}
	}
	session, err := server.newBrowserMCPSession(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"})
	if err != nil {
		t.Fatalf("new browser MCP session: %v", err)
	}
	<-streamBody.started
	var streamErr error
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		streamErr = session.eventStreamFailure()
		if streamErr != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if streamErr == nil {
		t.Fatal("event-stream read failure did not invalidate the session")
	}
	if _, err := session.rpc(context.Background(), "tools/list", map[string]any{}); err == nil || !browserMCPErrorLooksLikeSessionLoss(err.Error()) {
		t.Fatalf("RPC after event-stream failure = %v, want session-loss error", err)
	}
	session.close()
	if deleteCalls != 1 {
		t.Fatalf("DELETE calls = %d, want one", deleteCalls)
	}
}

func TestBrowserMCPSessionKeepsEventStreamAliveAndClosesAfterDelete(t *testing.T) {
	const negotiatedProtocol = "2025-06-18"
	server := &Server{hubBase: "https://hub.example"}
	streamBody := &testBrowserEventStreamBody{
		payload: []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"ping\",\"params\":{}}\n\n"),
		closed:  make(chan struct{}),
	}
	var eventsMu sync.Mutex
	var events []string
	appendEvent := func(event string) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	}
	pingResponse := make(chan struct{}, 1)
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.Method {
			case http.MethodGet:
				appendEvent("GET")
				if got := request.Header.Get("Accept"); got != "text/event-stream" {
					return nil, fmt.Errorf("GET Accept = %q", got)
				}
				if got := request.Header.Get("Mcp-Session-Id"); got != "stream-session" {
					return nil, fmt.Errorf("GET session = %q", got)
				}
				if got := request.Header.Get("MCP-Protocol-Version"); got != negotiatedProtocol {
					return nil, fmt.Errorf("GET protocol = %q", got)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       streamBody,
				}, nil
			case http.MethodDelete:
				if got := request.Header.Get("MCP-Protocol-Version"); got != negotiatedProtocol {
					return nil, fmt.Errorf("DELETE protocol = %q", got)
				}
				select {
				case <-streamBody.closed:
					appendEvent("DELETE-after-stream-close")
				default:
					appendEvent("DELETE-before-stream-close")
				}
				recorder := httptest.NewRecorder()
				recorder.WriteHeader(http.StatusNoContent)
				return recorder.Result(), nil
			}

			var envelope struct {
				Method string          `json:"method"`
				ID     json.RawMessage `json:"id"`
			}
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				return nil, err
			}
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Content-Type", "application/json")
			switch envelope.Method {
			case "initialize":
				recorder.Header().Set("Mcp-Session-Id", "stream-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": negotiatedProtocol}})
			case "notifications/initialized":
				if got := request.Header.Get("MCP-Protocol-Version"); got != negotiatedProtocol {
					return nil, fmt.Errorf("initialized protocol = %q", got)
				}
				recorder.WriteHeader(http.StatusAccepted)
			case "tools/list":
				if got := request.Header.Get("MCP-Protocol-Version"); got != negotiatedProtocol {
					return nil, fmt.Errorf("tools/list protocol = %q", got)
				}
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []any{}}})
			case "":
				if got := request.Header.Get("MCP-Protocol-Version"); got != negotiatedProtocol {
					return nil, fmt.Errorf("ping response protocol = %q", got)
				}
				if string(envelope.ID) != "99" {
					return nil, fmt.Errorf("ping response id = %s", envelope.ID)
				}
				pingResponse <- struct{}{}
				recorder.WriteHeader(http.StatusAccepted)
			default:
				return nil, fmt.Errorf("unexpected MCP method %q", envelope.Method)
			}
			return recorder.Result(), nil
		})}
	}
	session, err := server.newBrowserMCPSession(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"})
	if err != nil {
		t.Fatalf("new browser MCP session: %v", err)
	}
	select {
	case <-pingResponse:
	case <-time.After(time.Second):
		t.Fatal("event stream ping was not answered")
	}
	select {
	case <-streamBody.closed:
		t.Fatal("event stream closed before session close")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := session.rpc(context.Background(), "tools/list", map[string]any{}); err != nil {
		t.Fatalf("tools/list with live event stream: %v", err)
	}
	session.close()
	select {
	case <-streamBody.closed:
	case <-time.After(time.Second):
		t.Fatal("event stream did not close with session")
	}
	eventsMu.Lock()
	gotEvents := append([]string(nil), events...)
	eventsMu.Unlock()
	getCount := 0
	for _, event := range gotEvents {
		if event == "GET" {
			getCount++
		}
	}
	if getCount != 1 {
		t.Fatalf("event stream GET count = %d, want one; events=%v", getCount, gotEvents)
	}
	deleteBefore, deleteAfter := 0, 0
	for _, event := range gotEvents {
		switch event {
		case "DELETE-before-stream-close":
			deleteBefore++
		case "DELETE-after-stream-close":
			deleteAfter++
		}
	}
	if deleteBefore != 1 || deleteAfter != 0 {
		t.Fatalf("session close ordering = before:%d after:%d; events=%v", deleteBefore, deleteAfter, gotEvents)
	}
}

func TestBrowserMCPNavigationSummarySkipsHeadingsAndKeepsError(t *testing.T) {
	text := "### Result\n### Error\nError: page.goto: net::ERR_CONNECTION_REFUSED at https://preview.example/" + strings.Repeat(" details", 80)
	got := browserMCPNavigationSummary(text, "fallback")
	if strings.HasPrefix(got, "#") || strings.HasPrefix(got, "Result") {
		t.Fatalf("navigation summary retained Markdown scaffolding: %q", got)
	}
	if !strings.Contains(got, "ERR_CONNECTION_REFUSED") {
		t.Fatalf("navigation summary lost substantive error: %q", got)
	}
	if len([]rune(got)) > browserMCPNavigationSummaryMaxChars {
		t.Fatalf("navigation summary length = %d, want <= %d", len([]rune(got)), browserMCPNavigationSummaryMaxChars)
	}
}

func TestBrowserMCPNavigationSummaryFallsBackWhenResponseHasNoDetail(t *testing.T) {
	if got := browserMCPNavigationSummary("### Result\n### Error\n", "the preview did not load"); got != "the preview did not load" {
		t.Fatalf("heading-only navigation summary = %q", got)
	}
}

func TestPrivatePreviewHubOriginAcceptsConfiguredPublicHubRedirect(t *testing.T) {
	const publicHubURL = "https://console.example.test"
	var preview *httptest.Server
	preview = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := url.Values{
			"cluster":      {"workspace-1"},
			"redirect_uri": {preview.URL + privateAppCallbackPath},
		}
		http.Redirect(w, r, publicHubURL+privateAppAuthorizePath+"?"+query.Encode(), http.StatusFound)
	}))
	defer preview.Close()

	server := &Server{
		hubBase:                      "https://internal-hub.example.test",
		hubPublicURL:                 publicHubURL,
		previewInsecureSkipTLSVerify: true,
	}
	origin, err := server.privatePreviewHubOrigin(context.Background(), identity{clusterID: "workspace-1"}, preview.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := origin.String(), publicHubURL; got != want {
		t.Fatalf("hub origin = %q, want %q", got, want)
	}
}

func TestPrivatePreviewHubOriginRejectsUntrustedHubRedirect(t *testing.T) {
	var preview *httptest.Server
	preview = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := url.Values{
			"cluster":      {"workspace-1"},
			"redirect_uri": {preview.URL + privateAppCallbackPath},
		}
		http.Redirect(w, r, "https://attacker.example.test"+privateAppAuthorizePath+"?"+query.Encode(), http.StatusFound)
	}))
	defer preview.Close()

	server := &Server{
		hubBase:                      "https://internal-hub.example.test",
		hubPublicURL:                 "https://trusted-hub.example.test",
		previewInsecureSkipTLSVerify: true,
	}
	_, err := server.privatePreviewHubOrigin(context.Background(), identity{clusterID: "workspace-1"}, preview.URL)
	if err == nil || !strings.Contains(err.Error(), "configured public hub origin") {
		t.Fatalf("untrusted redirect error = %v, want configured-public-origin rejection", err)
	}
}

func TestPrivatePreviewConfiguredHubOriginRequiresAbsoluteHTTPSOrigin(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "missing", url: ""},
		{name: "http", url: "http://hub.example.test"},
		{name: "path", url: "https://hub.example.test/private"},
		{name: "query", url: "https://hub.example.test?tenant=one"},
		{name: "fragment", url: "https://hub.example.test#private"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&Server{hubPublicURL: tc.url}).privatePreviewConfiguredHubOrigin()
			if err == nil || !strings.Contains(err.Error(), "FAROS_HUB_PUBLIC_URL") {
				t.Fatalf("configured origin %q error = %v, want missing/invalid public URL", tc.url, err)
			}
		})
	}
}

func TestBrowserSessionHandoffURLMintsWithCallerBearer(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != browserSessionHandoffPath || r.Header.Get("Authorization") != "Bearer caller-token" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"path":"/auth/session/handoff?code=one-use"}`))
	}))
	defer hub.Close()
	origin, _ := url.Parse("https://console.example.test")
	server := &Server{hubBase: hub.URL, hubPublicURL: origin.String()}
	handoff, err := server.browserSessionHandoffURL(context.Background(), identity{token: "caller-token"}, origin)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := handoff, "https://console.example.test/auth/session/handoff?code=one-use"; got != want {
		t.Fatalf("handoff URL = %q, want %q", got, want)
	}
	if strings.Contains(handoff, "caller-token") {
		t.Fatal("caller bearer leaked into browser handoff URL")
	}
}
