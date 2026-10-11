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

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

func browserMCPTestEventStreamResponse(request *http.Request) *http.Response {
	return browserMCPTestEventStreamResponseWithBody(request, &testBrowserEventStreamBody{closed: make(chan struct{})})
}

func browserMCPTestEventStreamResponseWithBody(request *http.Request, body io.ReadCloser) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "text/event-stream")
	if sessionID := request.Header.Get("Mcp-Session-Id"); sessionID != "" {
		header.Set("Mcp-Session-Id", sessionID)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       body,
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

func TestLegacyPreviewInspectionRejectsUnverifiedBrowserIsolation(t *testing.T) {
	server := &Server{}
	transportCalled := false
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		transportCalled = true
		return &http.Client{Transport: sandboxRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected browser request")
		})}
	}
	_, err := server.inspectPreviewViaBrowserMCP(
		context.Background(),
		identity{clusterID: "cluster-a"},
		dataPlaneRef{Resource: "instances", Name: "browser"},
		projectAssistantPreviewInspectionRequest{URL: "https://preview.example/"},
	)
	if !errors.Is(err, errProjectAssistantBrowserIsolationUnverified) {
		t.Fatalf("inspection error = %v, want unverified-isolation error", err)
	}
	if transportCalled {
		t.Fatal("unverified browser isolation reached the data-plane transport")
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
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
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
			server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
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

func TestBrowserMCPSessionDetectsOversizedRPCResponses(t *testing.T) {
	responseWithSize := func(size int) []byte {
		raw := []byte(`{"jsonrpc":"2.0","id":2,"result":null}`)
		if len(raw) > size {
			t.Fatalf("test response base length %d exceeds requested size %d", len(raw), size)
		}
		return append(raw, []byte(strings.Repeat(" ", size-len(raw)))...)
	}

	for _, tc := range []struct {
		name         string
		method       string
		responseSize int
		wantOverflow bool
	}{
		{name: "exact limit is accepted", method: "tools/list", responseSize: projectAssistantPreviewInspectionMaxResponse},
		{name: "over limit is explicit and dispatched once", method: "tools/call", responseSize: projectAssistantPreviewInspectionMaxResponse + 1, wantOverflow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
			dispatchedCalls := 0
			response := responseWithSize(tc.responseSize)
			server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
				return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					if request.Method == http.MethodGet {
						return browserMCPTestEventStreamResponse(request), nil
					}
					if request.Method == http.MethodDelete {
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
						recorder.Header().Set("Mcp-Session-Id", "response-size-session")
						_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
					case "notifications/initialized":
						recorder.WriteHeader(http.StatusAccepted)
					case tc.method:
						if envelope.Method == "tools/call" {
							dispatchedCalls++
						}
						_, _ = recorder.Write(response)
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
			t.Cleanup(session.close)

			_, err = session.rpc(context.Background(), tc.method, map[string]any{"name": "browser_click"})
			if !tc.wantOverflow {
				if err != nil {
					t.Fatalf("RPC at response limit failed: %v", err)
				}
				return
			}
			var overflowErr *browserMCPResponseTooLargeError
			if !errors.As(err, &overflowErr) {
				t.Fatalf("over-limit RPC error = %v, want typed response overflow", err)
			}
			if overflowErr.method != "tools/call" || overflowErr.limit != int64(projectAssistantPreviewInspectionMaxResponse) {
				t.Fatalf("response overflow details = %#v", overflowErr)
			}
			if !strings.Contains(err.Error(), "may have been applied") || !strings.Contains(err.Error(), "outcome is unverified") {
				t.Fatalf("response overflow error = %q, want explicit uncertain-action guidance", err)
			}
			if dispatchedCalls != 1 {
				t.Fatalf("tools/call dispatches = %d, want exactly one", dispatchedCalls)
			}
		})
	}
}

type testBrowserEventStreamBody struct {
	payload   []byte
	closed    chan struct{}
	once      sync.Once
	mu        sync.Mutex
	sent      bool
	started   chan struct{}
	startOnce sync.Once
}

func (body *testBrowserEventStreamBody) Read(p []byte) (int, error) {
	if body.started != nil {
		body.startOnce.Do(func() { close(body.started) })
	}
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

type testBrowserEventStreamEOFBody struct {
	started chan struct{}
	once    sync.Once
}

func (body *testBrowserEventStreamEOFBody) Read([]byte) (int, error) {
	if body.started != nil {
		body.once.Do(func() { close(body.started) })
	}
	return 0, io.EOF
}

func (body *testBrowserEventStreamEOFBody) Close() error { return nil }

type testBrowserBlockedErrorBody struct {
	readStarted chan struct{}
	closed      chan struct{}
	readOnce    sync.Once
	closeOnce   sync.Once
}

func (body *testBrowserBlockedErrorBody) Read([]byte) (int, error) {
	body.readOnce.Do(func() { close(body.readStarted) })
	<-body.closed
	return 0, io.EOF
}

func (body *testBrowserBlockedErrorBody) Close() error {
	body.closeOnce.Do(func() { close(body.closed) })
	return nil
}

func TestBrowserMCPSessionRecoversEventStreamWithoutReplayingToolCalls(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	firstStream := &testBrowserEventStreamErrorBody{started: make(chan struct{})}
	recoveredStream := &testBrowserEventStreamBody{closed: make(chan struct{}), started: make(chan struct{})}
	var countsMu sync.Mutex
	getCalls, initializeCalls, toolCallCalls, listCalls, deleteCalls := 0, 0, 0, 0, 0
	counts := func() (int, int, int, int, int) {
		countsMu.Lock()
		defer countsMu.Unlock()
		return getCalls, initializeCalls, toolCallCalls, listCalls, deleteCalls
	}
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				countsMu.Lock()
				getCalls++
				call := getCalls
				countsMu.Unlock()
				switch call {
				case 1:
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: firstStream}, nil
				case 2:
					return nil, context.DeadlineExceeded
				default:
					return browserMCPTestEventStreamResponseWithBody(request, recoveredStream), nil
				}
			}
			if request.Method == http.MethodDelete {
				countsMu.Lock()
				deleteCalls++
				countsMu.Unlock()
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
				countsMu.Lock()
				initializeCalls++
				countsMu.Unlock()
				recorder.Header().Set("Mcp-Session-Id", "stream-error-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			case "tools/call":
				countsMu.Lock()
				toolCallCalls++
				countsMu.Unlock()
				return nil, errors.New("simulated lost tools/call response")
			case "tools/list":
				countsMu.Lock()
				listCalls++
				countsMu.Unlock()
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []any{}}})
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
	t.Cleanup(session.close)
	select {
	case <-firstStream.started:
	case <-time.After(time.Second):
		t.Fatal("initial event stream reader did not start")
	}
	if _, err := session.rpc(context.Background(), "tools/call", map[string]any{"name": "browser_click"}); err == nil || !strings.Contains(err.Error(), "simulated lost tools/call response") {
		t.Fatalf("dispatched tools/call error = %v, want the simulated lost response", err)
	}
	select {
	case <-recoveredStream.started:
	case <-time.After(2 * time.Second):
		t.Fatalf("event stream did not recover; last error: %v", session.eventStreamFailure())
	}
	if session.eventStreamFailure() != nil || session.currentSessionID() != "stream-error-session" {
		t.Fatalf("recovered session state: streamErr=%v sessionID=%q", session.eventStreamFailure(), session.currentSessionID())
	}
	if _, err := session.rpc(context.Background(), "tools/list", map[string]any{}); err != nil {
		t.Fatalf("tools/list after event-stream recovery: %v", err)
	}
	getCount, initializeCount, toolCallCount, listCount, deleteCount := counts()
	if getCount != 3 || initializeCount != 1 || toolCallCount != 1 || listCount != 1 || deleteCount != 0 {
		t.Fatalf("recovery request counts = GET %d initialize %d tools/call %d tools/list %d DELETE %d; want 3/1/1/1/0", getCount, initializeCount, toolCallCount, listCount, deleteCount)
	}
	session.close()
	_, _, _, _, deleteCount = counts()
	if deleteCount != 1 {
		t.Fatalf("DELETE calls = %d, want one", deleteCount)
	}
}

func TestBrowserMCPSessionBoundsRepeatedImmediateEOFRecovery(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	firstStream := &testBrowserEventStreamBody{closed: make(chan struct{}), started: make(chan struct{})}
	var countsMu sync.Mutex
	getTimes := make([]time.Time, 0, 5)
	getCalls, deleteCalls := 0, 0
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				countsMu.Lock()
				getCalls++
				call := getCalls
				getTimes = append(getTimes, time.Now())
				countsMu.Unlock()
				if call == 1 {
					return browserMCPTestEventStreamResponseWithBody(request, firstStream), nil
				}
				return browserMCPTestEventStreamResponseWithBody(request, &testBrowserEventStreamEOFBody{}), nil
			}
			if request.Method == http.MethodDelete {
				countsMu.Lock()
				deleteCalls++
				countsMu.Unlock()
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
				recorder.Header().Set("Mcp-Session-Id", "immediate-eof-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			default:
				return nil, fmt.Errorf("unexpected MCP method %q", envelope.Method)
			}
			return recorder.Result(), nil
		})}
	}
	policy := browserMCPEventStreamRecoveryPolicy{
		window:        500 * time.Millisecond,
		headerTimeout: 50 * time.Millisecond,
		initialDelay:  20 * time.Millisecond,
		maxDelay:      40 * time.Millisecond,
		maxAttempts:   4,
		stableWindow:  25 * time.Millisecond,
	}
	session, err := server.newBrowserMCPSessionWithRecoveryPolicy(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"}, projectAssistantBrowserSessionRoleUnspecified, policy)
	if err != nil {
		t.Fatalf("new browser MCP session: %v", err)
	}
	t.Cleanup(session.close)
	select {
	case <-firstStream.started:
	case <-time.After(time.Second):
		t.Fatal("initial event stream reader did not start")
	}
	_ = firstStream.Close()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		countsMu.Lock()
		count := getCalls
		countsMu.Unlock()
		if count == 1+policy.maxAttempts {
			break
		}
		time.Sleep(time.Millisecond)
	}
	countsMu.Lock()
	count := getCalls
	times := append([]time.Time(nil), getTimes...)
	countsMu.Unlock()
	if count != 1+policy.maxAttempts {
		t.Fatalf("GET calls = %d, want initial GET plus %d bounded recovery attempts", count, policy.maxAttempts)
	}
	if len(times) != count {
		t.Fatalf("recorded %d GET timestamps for %d calls", len(times), count)
	}
	if got := times[2].Sub(times[1]); got < 15*time.Millisecond {
		t.Fatalf("first repeated-EOF retry interval = %s, want backoff of at least 15ms", got)
	}
	if got := times[3].Sub(times[2]); got < 30*time.Millisecond {
		t.Fatalf("second repeated-EOF retry interval = %s, want backoff of at least 30ms", got)
	}
	time.Sleep(100 * time.Millisecond)
	countsMu.Lock()
	finalCount := getCalls
	countsMu.Unlock()
	if finalCount != count {
		t.Fatalf("GET calls after the bounded episode = %d, want %d", finalCount, count)
	}
	session.close()
	countsMu.Lock()
	finalDeleteCalls := deleteCalls
	countsMu.Unlock()
	if finalDeleteCalls != 1 {
		t.Fatalf("DELETE calls = %d, want one", finalDeleteCalls)
	}
}

func TestBrowserMCPSessionReconnectJoinsAutomaticRecovery(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	firstStream := &testBrowserEventStreamErrorBody{started: make(chan struct{})}
	recoveredStream := &testBrowserEventStreamBody{closed: make(chan struct{})}
	secondGetStarted := make(chan struct{})
	allowSecondGet := make(chan struct{})
	var startOnce sync.Once
	var countsMu sync.Mutex
	getCalls := 0
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				countsMu.Lock()
				getCalls++
				call := getCalls
				countsMu.Unlock()
				switch call {
				case 1:
					return browserMCPTestEventStreamResponseWithBody(request, firstStream), nil
				case 2:
					startOnce.Do(func() { close(secondGetStarted) })
					select {
					case <-allowSecondGet:
						return browserMCPTestEventStreamResponseWithBody(request, recoveredStream), nil
					case <-request.Context().Done():
						return nil, request.Context().Err()
					}
				default:
					return nil, fmt.Errorf("unexpected duplicate event-stream GET %d", call)
				}
			}
			if request.Method == http.MethodDelete {
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
				recorder.Header().Set("Mcp-Session-Id", "joined-recovery-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			default:
				return nil, fmt.Errorf("unexpected MCP method %q", envelope.Method)
			}
			return recorder.Result(), nil
		})}
	}
	policy := browserMCPEventStreamRecoveryPolicy{
		window:        time.Second,
		headerTimeout: 500 * time.Millisecond,
		initialDelay:  20 * time.Millisecond,
		maxDelay:      40 * time.Millisecond,
		maxAttempts:   3,
		stableWindow:  150 * time.Millisecond,
	}
	session, err := server.newBrowserMCPSessionWithRecoveryPolicy(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"}, projectAssistantBrowserSessionRoleUnspecified, policy)
	if err != nil {
		t.Fatalf("new browser MCP session: %v", err)
	}
	t.Cleanup(session.close)
	select {
	case <-firstStream.started:
	case <-time.After(time.Second):
		t.Fatal("initial event stream reader did not start")
	}
	joined := make(chan error, 1)
	go func() { joined <- session.reconnectEventStream(context.Background()) }()
	select {
	case <-secondGetStarted:
	case <-time.After(time.Second):
		t.Fatal("automatic recovery did not start its GET")
	}
	time.Sleep(50 * time.Millisecond)
	countsMu.Lock()
	countWhileRecoveryActive := getCalls
	countsMu.Unlock()
	if countWhileRecoveryActive != 2 {
		t.Fatalf("GET calls while native joined recovery = %d, want initial plus one worker GET", countWhileRecoveryActive)
	}
	close(allowSecondGet)
	openDeadline := time.Now().Add(time.Second)
	for !session.eventStreamIsOpen() && time.Now().Before(openDeadline) {
		time.Sleep(time.Millisecond)
	}
	if !session.eventStreamIsOpen() {
		t.Fatal("recovery GET did not open its event stream")
	}
	joinedWhileOpen := make(chan error, 1)
	go func() { joinedWhileOpen <- session.reconnectEventStream(context.Background()) }()
	select {
	case err := <-joinedWhileOpen:
		t.Fatalf("reconnectEventStream returned before the active recovery stability window: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case err := <-joined:
		if err != nil {
			t.Fatalf("reconnectEventStream joining automatic recovery: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native reconnect caller did not receive the recovery result")
	}
	select {
	case err := <-joinedWhileOpen:
		if err != nil {
			t.Fatalf("reconnectEventStream joining already-open recovery: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native reconnect caller did not receive the already-open recovery result")
	}
	countsMu.Lock()
	finalGetCalls := getCalls
	countsMu.Unlock()
	if finalGetCalls != 2 || !session.eventStreamIsOpen() {
		t.Fatalf("joined recovery state: GET calls=%d streamOpen=%v; want 2/true", finalGetCalls, session.eventStreamIsOpen())
	}
}

func TestBrowserMCPSessionCloseCancelsImmediateEOFRecoveryBackoff(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	firstStream := &testBrowserEventStreamBody{closed: make(chan struct{}), started: make(chan struct{})}
	secondStreamRead := make(chan struct{})
	var countsMu sync.Mutex
	getCalls := 0
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				countsMu.Lock()
				getCalls++
				call := getCalls
				countsMu.Unlock()
				if call == 1 {
					return browserMCPTestEventStreamResponseWithBody(request, firstStream), nil
				}
				return browserMCPTestEventStreamResponseWithBody(request, &testBrowserEventStreamEOFBody{started: secondStreamRead}), nil
			}
			if request.Method == http.MethodDelete {
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
				recorder.Header().Set("Mcp-Session-Id", "cancel-eof-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			default:
				return nil, fmt.Errorf("unexpected MCP method %q", envelope.Method)
			}
			return recorder.Result(), nil
		})}
	}
	policy := browserMCPEventStreamRecoveryPolicy{
		window:        2 * time.Second,
		headerTimeout: 100 * time.Millisecond,
		initialDelay:  500 * time.Millisecond,
		maxDelay:      500 * time.Millisecond,
		maxAttempts:   5,
		stableWindow:  time.Second,
	}
	session, err := server.newBrowserMCPSessionWithRecoveryPolicy(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"}, projectAssistantBrowserSessionRoleUnspecified, policy)
	if err != nil {
		t.Fatalf("new browser MCP session: %v", err)
	}
	t.Cleanup(session.close)
	select {
	case <-firstStream.started:
	case <-time.After(time.Second):
		t.Fatal("initial event stream reader did not start")
	}
	_ = firstStream.Close()
	select {
	case <-secondStreamRead:
	case <-time.After(time.Second):
		t.Fatal("recovery event stream did not immediately reach EOF")
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline) && session.eventStreamFailure() == nil; {
		time.Sleep(time.Millisecond)
	}
	if session.eventStreamFailure() == nil {
		t.Fatal("recovery stream EOF was not recorded")
	}
	// Give the worker time to enter its half-second backoff before closing.
	time.Sleep(25 * time.Millisecond)
	session.close()
	time.Sleep(550 * time.Millisecond)
	countsMu.Lock()
	count := getCalls
	countsMu.Unlock()
	if count != 2 {
		t.Fatalf("GET calls after close canceled recovery backoff = %d, want 2", count)
	}
}

func TestBrowserMCPSessionStopsRecoveryWhenGETReportsSessionGone(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	firstStream := &testBrowserEventStreamErrorBody{started: make(chan struct{})}
	blockedErrorBody := &testBrowserBlockedErrorBody{readStarted: make(chan struct{}), closed: make(chan struct{})}
	var countsMu sync.Mutex
	getCalls, initializeCalls, toolCalls, deleteCalls := 0, 0, 0, 0
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				countsMu.Lock()
				getCalls++
				call := getCalls
				countsMu.Unlock()
				switch call {
				case 1:
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: firstStream}, nil
				case 2:
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: blockedErrorBody}, nil
				default:
					recorder := httptest.NewRecorder()
					recorder.WriteHeader(http.StatusNotFound)
					return recorder.Result(), nil
				}
			}
			if request.Method == http.MethodDelete {
				countsMu.Lock()
				deleteCalls++
				countsMu.Unlock()
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
			if envelope.Method == "initialize" {
				countsMu.Lock()
				initializeCalls++
				countsMu.Unlock()
				recorder.Header().Set("Mcp-Session-Id", "gone-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			} else if strings.HasPrefix(envelope.Method, "tools/") {
				countsMu.Lock()
				toolCalls++
				countsMu.Unlock()
			}
			return recorder.Result(), nil
		})}
	}
	session, err := server.newBrowserMCPSession(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"})
	if err != nil {
		t.Fatalf("new browser MCP session: %v", err)
	}
	t.Cleanup(func() {
		_ = blockedErrorBody.Close()
		session.close()
	})
	select {
	case <-firstStream.started:
	case <-time.After(time.Second):
		t.Fatal("initial event stream reader did not start")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && session.eventStreamSessionGone() == nil {
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(session.eventStreamSessionGone(), errBrowserMCPSessionGone) {
		_ = blockedErrorBody.Close()
		session.close()
		t.Fatalf("session loss classification = %v, want authoritative GET 404", session.eventStreamSessionGone())
	}
	select {
	case <-blockedErrorBody.closed:
	default:
		t.Fatal("recovery did not close the transient error response body")
	}
	select {
	case <-blockedErrorBody.readStarted:
		t.Fatal("recovery tried to drain an unbounded GET error response")
	default:
	}
	time.Sleep(50 * time.Millisecond)
	countsMu.Lock()
	gotGets, gotInitializes, gotToolCalls := getCalls, initializeCalls, toolCalls
	countsMu.Unlock()
	if gotGets != 3 || gotInitializes != 1 || gotToolCalls != 0 {
		t.Fatalf("session-gone recovery counts = GET %d initialize %d tool calls %d; want 3/1/0", gotGets, gotInitializes, gotToolCalls)
	}
	session.close()
	countsMu.Lock()
	gotDeletes := deleteCalls
	countsMu.Unlock()
	if gotDeletes != 1 {
		t.Fatalf("DELETE calls after session loss = %d, want one", gotDeletes)
	}
}

func TestBrowserMCPSessionCloseCancelsInFlightRecoveryGET(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	firstStream := &testBrowserEventStreamErrorBody{started: make(chan struct{})}
	secondGetStarted := make(chan struct{})
	secondGetCanceled := make(chan struct{})
	var secondGetStartOnce, secondGetCancelOnce sync.Once
	var countsMu sync.Mutex
	getCalls, deleteCalls := 0, 0
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				countsMu.Lock()
				getCalls++
				call := getCalls
				countsMu.Unlock()
				if call == 1 {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: firstStream}, nil
				}
				secondGetStartOnce.Do(func() { close(secondGetStarted) })
				<-request.Context().Done()
				secondGetCancelOnce.Do(func() { close(secondGetCanceled) })
				return nil, request.Context().Err()
			}
			if request.Method == http.MethodDelete {
				countsMu.Lock()
				deleteCalls++
				countsMu.Unlock()
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
				recorder.Header().Set("Mcp-Session-Id", "closing-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
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
	t.Cleanup(session.close)
	select {
	case <-firstStream.started:
	case <-time.After(time.Second):
		t.Fatal("initial event stream reader did not start")
	}
	select {
	case <-secondGetStarted:
	case <-time.After(time.Second):
		t.Fatal("automatic recovery did not start a replacement GET")
	}
	closed := make(chan struct{})
	go func() {
		session.close()
		close(closed)
	}()
	select {
	case <-secondGetCanceled:
	case <-time.After(time.Second):
		t.Fatal("closing the browser session did not cancel the recovery GET")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("browser session close did not finish after canceling recovery")
	}
	countsMu.Lock()
	gotGets, gotDeletes := getCalls, deleteCalls
	countsMu.Unlock()
	if gotGets != 2 || gotDeletes != 1 || !session.isClosed() {
		t.Fatalf("recovery close state: GET %d DELETE %d closed=%v; want 2/1/true", gotGets, gotDeletes, session.isClosed())
	}
}

func TestBrowserMCPSessionClampsRecoveryGETToRemainingWindow(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	firstStream := &testBrowserEventStreamBody{closed: make(chan struct{}), started: make(chan struct{})}
	secondGetStarted := make(chan struct{})
	secondGetCanceled := make(chan time.Duration, 1)
	var startOnce sync.Once
	var countsMu sync.Mutex
	getCalls := 0
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				countsMu.Lock()
				getCalls++
				call := getCalls
				countsMu.Unlock()
				if call == 1 {
					return browserMCPTestEventStreamResponseWithBody(request, firstStream), nil
				}
				start := time.Now()
				startOnce.Do(func() { close(secondGetStarted) })
				<-request.Context().Done()
				secondGetCanceled <- time.Since(start)
				return nil, request.Context().Err()
			}
			if request.Method == http.MethodDelete {
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
				recorder.Header().Set("Mcp-Session-Id", "bounded-window-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			default:
				return nil, fmt.Errorf("unexpected MCP method %q", envelope.Method)
			}
			return recorder.Result(), nil
		})}
	}
	policy := browserMCPEventStreamRecoveryPolicy{
		window:        80 * time.Millisecond,
		headerTimeout: 2 * time.Second,
		initialDelay:  500 * time.Millisecond,
		maxDelay:      500 * time.Millisecond,
		maxAttempts:   5,
		stableWindow:  time.Second,
	}
	session, err := server.newBrowserMCPSessionWithRecoveryPolicy(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"}, projectAssistantBrowserSessionRoleUnspecified, policy)
	if err != nil {
		t.Fatalf("new browser MCP session: %v", err)
	}
	t.Cleanup(session.close)
	select {
	case <-firstStream.started:
	case <-time.After(time.Second):
		t.Fatal("initial event stream reader did not start")
	}
	_ = firstStream.Close()
	select {
	case <-secondGetStarted:
	case <-time.After(time.Second):
		t.Fatal("automatic recovery did not start a replacement GET")
	}
	select {
	case elapsed := <-secondGetCanceled:
		if elapsed > 500*time.Millisecond {
			t.Fatalf("recovery GET remained open for %s, want it clamped to the 80ms remaining window", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery GET was not canceled by its remaining-window deadline")
	}
	time.Sleep(100 * time.Millisecond)
	countsMu.Lock()
	count := getCalls
	countsMu.Unlock()
	if count != 2 {
		t.Fatalf("GET calls after the bounded window = %d, want 2", count)
	}
	session.close()
}

func TestBrowserMCPSessionRecoveryStabilityWaitUsesRemainingBudget(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	firstStream := &testBrowserEventStreamBody{closed: make(chan struct{}), started: make(chan struct{})}
	recoveredStream := &testBrowserEventStreamBody{closed: make(chan struct{})}
	secondGetStarted := make(chan struct{})
	var startOnce sync.Once
	var countsMu sync.Mutex
	getCalls := 0
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				countsMu.Lock()
				getCalls++
				call := getCalls
				countsMu.Unlock()
				if call == 1 {
					return browserMCPTestEventStreamResponseWithBody(request, firstStream), nil
				}
				startOnce.Do(func() { close(secondGetStarted) })
				timer := time.NewTimer(180 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
					return browserMCPTestEventStreamResponseWithBody(request, recoveredStream), nil
				case <-request.Context().Done():
					return nil, request.Context().Err()
				}
			}
			if request.Method == http.MethodDelete {
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
				recorder.Header().Set("Mcp-Session-Id", "late-stream-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			default:
				return nil, fmt.Errorf("unexpected MCP method %q", envelope.Method)
			}
			return recorder.Result(), nil
		})}
	}
	policy := browserMCPEventStreamRecoveryPolicy{
		window:        300 * time.Millisecond,
		headerTimeout: 250 * time.Millisecond,
		initialDelay:  0,
		maxDelay:      10 * time.Millisecond,
		maxAttempts:   3,
		stableWindow:  500 * time.Millisecond,
	}
	session, err := server.newBrowserMCPSessionWithRecoveryPolicy(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"}, projectAssistantBrowserSessionRoleUnspecified, policy)
	if err != nil {
		t.Fatalf("new browser MCP session: %v", err)
	}
	t.Cleanup(session.close)
	select {
	case <-firstStream.started:
	case <-time.After(time.Second):
		t.Fatal("initial event stream reader did not start")
	}
	started := time.Now()
	_ = firstStream.Close()
	select {
	case <-secondGetStarted:
	case <-time.After(time.Second):
		t.Fatal("automatic recovery did not start a replacement GET")
	}
	err = session.reconnectEventStream(context.Background())
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recovery result = %v, want the episode deadline", err)
	}
	if elapsed > policy.window+250*time.Millisecond {
		t.Fatalf("recovery episode took %s, want it bounded by %s plus scheduling tolerance", elapsed, policy.window)
	}
	countsMu.Lock()
	count := getCalls
	countsMu.Unlock()
	if count != 2 {
		t.Fatalf("GET calls after the bounded episode = %d, want initial plus one late recovery GET", count)
	}
	session.close()
}

func TestBrowserMCPSessionKeepsEventStreamAliveAndClosesAfterDelete(t *testing.T) {
	const negotiatedProtocol = "2025-06-18"
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
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

func TestBrowserMCPSessionRejectsChangedPOSTSessionHeader(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	streamBody := &testBrowserEventStreamBody{closed: make(chan struct{})}
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				return browserMCPTestEventStreamResponseWithBody(request, streamBody), nil
			}
			if request.Method == http.MethodDelete {
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
				recorder.Header().Set("Mcp-Session-Id", "negotiated-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			case "tools/list":
				if got := request.Header.Get("Mcp-Session-Id"); got != "negotiated-session" {
					return nil, fmt.Errorf("tools/list request session = %q, want negotiated-session", got)
				}
				recorder.Header().Set("Mcp-Session-Id", "changed-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []any{}}})
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
	t.Cleanup(session.close)
	if _, err := session.rpc(context.Background(), "tools/list", map[string]any{}); err == nil || !strings.Contains(err.Error(), "changed the negotiated MCP session") {
		t.Fatalf("tools/list session header change error = %v", err)
	}
	if got := session.currentSessionID(); got != "negotiated-session" {
		t.Fatalf("session ID after changed POST response = %q, want negotiated-session", got)
	}
}

func TestBrowserMCPSessionDoesNotReplayPOSTOn307Redirect(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	streamBody := &testBrowserEventStreamBody{closed: make(chan struct{})}
	var toolDispatches int
	sharedClient := &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return browserMCPTestEventStreamResponseWithBody(request, streamBody), nil
		}
		if request.Method == http.MethodDelete {
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
			recorder.Header().Set("Mcp-Session-Id", "redirect-session")
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
		case "notifications/initialized":
			recorder.WriteHeader(http.StatusAccepted)
		case "tools/call":
			toolDispatches++
			if toolDispatches == 1 {
				recorder.Header().Set("Location", request.URL.String()+"?redirected=1")
				recorder.WriteHeader(http.StatusTemporaryRedirect)
				return recorder.Result(), nil
			}
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"isError": false}})
		default:
			return nil, fmt.Errorf("unexpected MCP method %q", envelope.Method)
		}
		return recorder.Result(), nil
	})}
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client { return sharedClient }
	session, err := server.newBrowserMCPSession(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"})
	if err != nil {
		t.Fatalf("new browser MCP session: %v", err)
	}
	t.Cleanup(session.close)
	if _, err := session.callToolReceipt(context.Background(), "browser_click", map[string]any{}); err == nil || !strings.Contains(err.Error(), "status 307") {
		t.Fatalf("redirected tools/call error = %v, want visible 307 response", err)
	}
	if toolDispatches != 1 {
		t.Fatalf("tools/call dispatches after 307 = %d, want one; redirect replayed a stateful action", toolDispatches)
	}
	if sharedClient.CheckRedirect != nil {
		t.Fatal("browser MCP redirect policy mutated the shared HTTP client")
	}
}

func TestBrowserMCPEventStreamBoundsLineAndEventMemory(t *testing.T) {
	eventPayload := strings.Builder{}
	longDataLine := strings.Repeat("x", browserMCPEventStreamMaxLineBytes-16)
	for range browserMCPEventStreamMaxEventBytes/(len(longDataLine)+1) + 2 {
		eventPayload.WriteString("data: ")
		eventPayload.WriteString(longDataLine)
		eventPayload.WriteByte('\n')
	}
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "line",
			payload: strings.Repeat("x", browserMCPEventStreamMaxLineBytes+1),
			want:    "line exceeds",
		},
		{
			name:    "event",
			payload: eventPayload.String(),
			want:    "event exceeds",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := &browserMCPSession{recoveryWake: make(chan struct{}, 1)}
			done := make(chan struct{})
			session.drainEventStream(context.Background(), io.NopCloser(strings.NewReader(tt.payload)), done)
			<-done
			if err := session.eventStreamFailure(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("event stream overflow failure = %v, want substring %q", err, tt.want)
			}
		})
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
		tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders,
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
		tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders,
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
			_, err := (&Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubPublicURL: tc.url}).privatePreviewConfiguredHubOrigin()
			if err == nil || !strings.Contains(err.Error(), "RAILGRID_HUB_PUBLIC_URL") || !strings.Contains(err.Error(), "hub.publicURL") {
				t.Fatalf("configured origin %q error = %v, want missing/invalid public URL naming the chart value", tc.url, err)
			}
		})
	}
}

func TestPrivatePreviewUnconfiguredHubOriginIsActionableForModelAndFeed(t *testing.T) {
	_, err := (&Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}).privatePreviewConfiguredHubOrigin()
	if err == nil {
		t.Fatal("missing RAILGRID_HUB_PUBLIC_URL was accepted")
	}
	// The model sees the tool failure text; it must say what to fix.
	result := projectEinoAssistantSafeToolFailureResult(browserMCPToolNavigate, err)
	if !strings.Contains(result, "private preview inspection is unavailable") || !strings.Contains(result, "hub.publicURL") {
		t.Fatalf("model-facing tool result = %q, want the configuration cause", result)
	}
	// The action feed classifies it as a preview problem with the same cause,
	// not as an unknown failure.
	diagnostic := projectAssistantActionFeedDiagnosticForTool("navigate-call", browserMCPToolNavigate, projectEinoAssistantSafeErrorText(err))
	if diagnostic == nil || diagnostic.Category != "runtime" || !strings.Contains(diagnostic.Message, "RAILGRID_HUB_PUBLIC_URL") || !strings.Contains(diagnostic.Message, "hub.publicURL") {
		t.Fatalf("diagnostic = %#v, want runtime preview diagnostic naming RAILGRID_HUB_PUBLIC_URL", diagnostic)
	}
}

func TestBrowserSessionHandoffURLMintsAsTheProject(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != browserSessionHandoffPath || r.Header.Get("Authorization") != "Bearer project-token" || r.Header.Get("X-Railgrid-User") != "" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"path":"/auth/apps/preview-handoff?code=one-use"}`))
	}))
	defer hub.Close()
	origin, _ := url.Parse("https://console.example.test/auth/apps/authorize?cluster=workspace-1&group=infrastructure.railgrid.ai&resource=instances&name=demo-dev")
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: hub.URL, hubToken: "provider-hub-token", hubPublicURL: "https://console.example.test", projectIdentityTokenFor: func(context.Context, identity, *aiv1alpha1.Project) (string, error) { return "project-token", nil }}
	handoff, err := server.browserSessionHandoffURL(context.Background(), identity{user: "alice", clusterID: "workspace-1"}, &aiv1alpha1.Project{}, origin)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := handoff, "https://console.example.test/auth/apps/preview-handoff?code=one-use"; got != want {
		t.Fatalf("handoff URL = %q, want %q", got, want)
	}
	if strings.Contains(handoff, "provider-hub-token") {
		t.Fatal("provider bearer leaked into browser handoff URL")
	}
}
