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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

type projectAssistantNativeBrowserTestRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip projectAssistantNativeBrowserTestRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type nativeBrowserSensitiveRPCError struct {
	cause error
}

func (*nativeBrowserSensitiveRPCError) Error() string {
	return "native browser RPC failed for https://hub.example/auth/apps/authorize?client_id=studio&state=AUTHORIZE_STATE_SENTINEL; " +
		"callback https://preview.example/__railgrid/auth/callback?code=ONE_TIME_CODE_SENTINEL&state=CALLBACK_STATE_SENTINEL; " +
		"handoff https://hub.example/auth/apps/preview-handoff?code=HANDOFF_CODE_SENTINEL; " +
		"ordinary https://preview.example/assets.js?state=open&filter=active"
}

func (e *nativeBrowserSensitiveRPCError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type fakeNativeBrowserToolPort struct {
	tools      []projectAssistantTool
	browserErr error
}

type countingNativeBrowserToolPort struct {
	server               *Server
	browserTools         []projectAssistantTool
	discoverMCPCalls     int
	discoverBrowserCalls int
}

type managedPrivatePreviewTestHarness struct {
	server        *Server
	request       projectAssistantToolCallRequest
	entry         *projectAssistantBrowserSessionEntry
	previewURL    string
	appName       string
	handoffBody   string
	handoffStatus int
	handoffCalls  int
	toolCalls     []string
	navigatedURLs []string
}

func newManagedPrivatePreviewTestHarness(t *testing.T, handoffBody string) *managedPrivatePreviewTestHarness {
	t.Helper()
	hub := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("private handoff unexpectedly reached the real hub server")
	}))
	t.Cleanup(hub.Close)
	var h *managedPrivatePreviewTestHarness
	preview := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := url.Values{
			"cluster":      {"cluster-a"},
			"group":        {"infrastructure.railgrid.ai"},
			"resource":     {"instances"},
			"name":         {h.appName},
			"redirect_uri": {"https://" + r.Host + privateAppCallbackPath},
		}
		http.Redirect(w, r, hub.URL+privateAppAuthorizePath+"?"+query.Encode(), http.StatusFound)
	}))
	t.Cleanup(preview.Close)
	h = &managedPrivatePreviewTestHarness{
		previewURL:    strings.TrimRight(preview.URL, "/") + "/",
		appName:       "demo-dev",
		handoffBody:   handoffBody,
		handoffStatus: http.StatusOK,
	}
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		tenantProviders:  defaultTestProviders,
		projectIdentityTokenFor: func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
			return "project-token", nil
		},
		hubBase: hub.URL, callers: newTestCallers(nil, hub.URL), hubPublicURL: hub.URL,
		previewInsecureSkipTLSVerify: true,
	}
	manager := newProjectAssistantBrowserSessionManager()
	server.browserSessions = manager
	t.Cleanup(manager.closeAll)
	manager.handoffNow = time.Now
	server.sandboxDataPlaneClientFactory = nativeBrowserTestClient(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost && request.URL.Path == browserSessionHandoffPath {
			h.handoffCalls++
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Content-Type", "application/json")
			recorder.WriteHeader(h.handoffStatus)
			_, _ = recorder.WriteString(h.handoffBody)
			return recorder.Result(), nil
		}
		var envelope nativeBrowserTestEnvelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			return nil, err
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		if envelope.Method == "initialize" {
			recorder.Header().Set("Mcp-Session-Id", "private-renewal-session")
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			return recorder.Result(), nil
		}
		if envelope.Method == "notifications/initialized" {
			recorder.WriteHeader(http.StatusAccepted)
			return recorder.Result(), nil
		}
		if envelope.Method == "tools/call" {
			h.toolCalls = append(h.toolCalls, envelope.Params.Name)
			if envelope.Params.Name == browserMCPToolNavigate {
				h.navigatedURLs = append(h.navigatedURLs, projectToolString(envelope.Params.Arguments["url"]))
			}
		}
		text := ""
		switch envelope.Params.Name {
		case browserMCPToolNavigate:
			if strings.HasPrefix(projectToolString(envelope.Params.Arguments["url"]), hub.URL) {
				text = "handoff complete"
			} else {
				text = "- Page URL: " + h.previewURL + "\n- Page Snapshot:\n- generic [ref=e1]:"
			}
		case browserMCPToolSnapshot:
			text = "- Page URL: " + h.previewURL + "\n- Page Snapshot:\n- generic [ref=e1]:"
		case "browser_tabs":
			text = "- 0: " + h.previewURL
		}
		_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": text}}}})
		return recorder.Result(), nil
	})
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-private-handoff-renewal",
	}
	ref := dataPlaneRef{Resource: "instances", Name: "browser"}
	h.server = server
	h.request = request
	h.entry = manager.entry(server.nativeBrowserOwner(request), ref)
	return h
}

func (h *managedPrivatePreviewTestHarness) call(name string, args map[string]any, previewURL string) (string, error) {
	return h.server.callProjectAssistantNativeBrowserSession(context.Background(), h.request, h.entry, h.entry.ref, name, args, true, previewURL, false)
}

func (p fakeNativeBrowserToolPort) DiscoverMCP(context.Context, identity, *aiv1alpha1.Project, projectLLMSettings) ([]projectAssistantTool, bool, error) {
	return nil, false, nil
}

func (p fakeNativeBrowserToolPort) Invoke(context.Context, projectAssistantTool, projectAssistantToolCallRequest) (string, error) {
	return "{}", nil
}

func (p fakeNativeBrowserToolPort) DiscoverBrowser(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, error) {
	return p.tools, p.browserErr
}

func (p *countingNativeBrowserToolPort) DiscoverMCP(context.Context, identity, *aiv1alpha1.Project, projectLLMSettings) ([]projectAssistantTool, bool, error) {
	p.discoverMCPCalls++
	return []projectAssistantTool{projectAssistantToolFunc{spec: projectAssistantToolSpec{
		Name:       "mcp_refresh_" + string(rune('0'+p.discoverMCPCalls)),
		Parameters: json.RawMessage(`{"type":"object"}`),
		Risk:       projectAssistantToolRiskRead,
	}}}, false, nil
}

func (p *countingNativeBrowserToolPort) Invoke(context.Context, projectAssistantTool, projectAssistantToolCallRequest) (string, error) {
	return "{}", nil
}

func (p *countingNativeBrowserToolPort) DiscoverBrowser(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, error) {
	p.discoverBrowserCalls++
	return p.browserTools, nil
}

func nativeBrowserCatalogTestTools(server *Server) []projectAssistantTool {
	tools := []projectMCPTool{
		{Name: "browser_navigate", Description: "navigate", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_snapshot", Description: "snapshot", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_console_messages", Description: "console", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_click", Description: "click", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_fill_form", Description: "fill", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	return projectAssistantNativeBrowserToolsForSpecs(server, tools)
}

func TestProjectAssistantBrowserCapabilityValidation(t *testing.T) {
	tool := func(name string) projectMCPTool {
		return projectMCPTool{Name: name}
	}
	complete := []projectMCPTool{
		tool("browser_navigate"),
		tool("browser_snapshot"),
		tool("browser_console_messages"),
		tool("browser_click"),
		tool("browser_tabs"),
		tool("browser_fill_form"),
	}
	if err := validateProjectAssistantBrowserCapabilities(complete); err != nil {
		t.Fatalf("complete native capability set rejected: %v", err)
	}
	withType := append([]projectMCPTool(nil), complete[:5]...)
	withType = append(withType, tool("browser_type"))
	if err := validateProjectAssistantBrowserCapabilities(withType); err != nil {
		t.Fatalf("browser_type alternative rejected: %v", err)
	}

	missing := append([]projectMCPTool(nil), complete[:3]...)
	err := validateProjectAssistantBrowserCapabilities(missing)
	var mismatch *projectAssistantBrowserCapabilityMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("capability error = %v, want structured mismatch", err)
	}
	if got, want := mismatch.Missing, []string{"browser_click", "browser_tabs", "browser_type or browser_fill_form"}; !equalStrings(got, want) {
		t.Fatalf("missing capabilities = %#v, want %#v", got, want)
	}
	if got, want := mismatch.Available, []string{"browser_console_messages", "browser_navigate", "browser_snapshot"}; !equalStrings(got, want) {
		t.Fatalf("available capabilities = %#v, want %#v", got, want)
	}
	if got, want := mismatch.Required, []string{"browser_navigate", "browser_snapshot", "browser_console_messages", "browser_click", "browser_tabs", "browser_type or browser_fill_form"}; !equalStrings(got, want) {
		t.Fatalf("required capabilities = %#v, want %#v", got, want)
	}
	if got, want := mismatch.Error(), "native Playwright browser capability mismatch: missing browser_click, browser_tabs, browser_type or browser_fill_form (available: browser_console_messages, browser_navigate, browser_snapshot)"; got != want {
		t.Fatalf("mismatch error = %q, want %q", got, want)
	}
}

func TestProjectAssistantBrowserDiscoveryFailureIsPrompted(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}
	configurePreviewInteractionBrowserTestServer(t, server, nil)
	mismatch := &projectAssistantBrowserCapabilityMismatchError{
		Required:  []string{"browser_snapshot"},
		Available: []string{"browser_navigate"},
		Missing:   []string{"browser_snapshot"},
	}
	discovery := projectEinoAssistantDiscoverTools(context.Background(), server, projectAssistantRunRequest{
		ToolPort: fakeNativeBrowserToolPort{browserErr: mismatch},
		TurnPolicy: projectAssistantTurnPolicyForProfile(
			projectAssistantTurnProfileImplementation,
		),
	})
	if len(discovery.BrowserTools) != 0 {
		t.Fatalf("failed browser discovery exposed tools: %#v", discovery.BrowserTools)
	}
	if !strings.Contains(discovery.Prompt, "Native browser tools are unavailable in this turn") {
		t.Fatalf("discovery prompt omitted browser failure guidance: %q", discovery.Prompt)
	}
	if !strings.Contains(discovery.Prompt, "missing browser_snapshot") {
		t.Fatalf("discovery prompt omitted structured mismatch detail: %q", discovery.Prompt)
	}
	if strings.Contains(discovery.Prompt, "inspect_development_preview") || strings.Contains(discovery.Prompt, "interact_development_preview") {
		t.Fatalf("native discovery failure prompt retained retired wrappers: %q", discovery.Prompt)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestProjectAssistantNativeBrowserToolSpecAllowlist(t *testing.T) {
	approved, ok := projectAssistantNativeBrowserToolSpec(projectMCPTool{
		Name:        "browser_snapshot",
		Description: "native snapshot",
		InputSchema: []byte(`{"type":"object","properties":{"filename":{"type":"string"}}}`),
	})
	if !ok || approved.Name != "browser_snapshot" || approved.Risk != projectAssistantToolRiskRead || string(approved.Parameters) == "" {
		t.Fatalf("approved native browser spec = %#v, ok=%v", approved, ok)
	}
	if !strings.Contains(approved.Description, projectAssistantBrowserSnapshotPreviewGuidance) {
		t.Fatalf("browser_snapshot description does not explain automatic preview setup: %q", approved.Description)
	}
	if _, ok := projectAssistantNativeBrowserToolSpec(projectMCPTool{Name: "browser_evaluate"}); ok {
		t.Fatal("arbitrary browser evaluation must not be exposed")
	}
	for _, name := range []string{"browser_file_upload", "browser_pdf_save"} {
		if _, ok := projectAssistantNativeBrowserToolSpec(projectMCPTool{Name: name}); ok {
			t.Fatalf("browser tool %q must not be exposed", name)
		}
	}
	if _, ok := projectAssistantNativeBrowserToolSpec(projectMCPTool{Name: "browser_tabs"}); ok {
		t.Fatal("browser_tabs is an internal safety capability and must not be exposed")
	}
}

func TestProjectAssistantNativeBrowserAuthURLsAreRedactedBeforeAudit(t *testing.T) {
	const oneTimeCode = "ONE_TIME_CODE_SENTINEL"
	const handoffCode = "HANDOFF_CODE_SENTINEL"
	const authorizeState = "AUTHORIZE_STATE_SENTINEL"
	const callbackState = "CALLBACK_STATE_SENTINEL"
	const ordinaryQuery = "https://preview.example/assets.js?state=open&filter=active"
	const ordinaryAppCodeQuery = "https://preview.example/items?code=application-data&filter=active"
	receipt := `{"isError":false,"content":[{"type":"text","text":"GET https://hub.example/auth/apps/authorize?client_id=studio&state=` + authorizeState + ` returned 302; GET https://preview.example/__railgrid/auth/callback?code=` + oneTimeCode + `&state=` + callbackState + ` returned 302; GET https://hub.example/auth/apps/preview-handoff?code=` + handoffCode + ` returned 200; GET ` + ordinaryQuery + ` returned 200; GET ` + ordinaryAppCodeQuery + ` returned 200"}],"structuredContent":{"requests":[{"url":"https://hub.example/auth/apps/authorize?client_id=studio&state=` + authorizeState + `","status":302},{"url":"https://preview.example/__railgrid/auth/callback?code=` + oneTimeCode + `&state=` + callbackState + `","status":302},{"url":"https://hub.example/auth/apps/preview-handoff?code=` + handoffCode + `","status":200},{"url":"` + ordinaryQuery + `","status":200},{"url":"` + ordinaryAppCodeQuery + `","status":200}]}}`

	sanitized := projectAssistantRedactNativeBrowserAuthURLs(receipt)
	for _, sensitive := range []string{oneTimeCode, handoffCode, authorizeState, callbackState} {
		if strings.Contains(sanitized, sensitive) {
			t.Fatal("native browser receipt retained an authentication secret")
		}
	}
	if !strings.Contains(sanitized, `"status":302`) || !strings.Contains(sanitized, ordinaryQuery) || !strings.Contains(sanitized, ordinaryAppCodeQuery) {
		t.Fatalf("redaction changed redirect status or ordinary query data: %s", sanitized)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(sanitized), &decoded); err != nil {
		t.Fatalf("redacted receipt is not valid JSON: %v", err)
	}
	if !strings.Contains(sanitized, "client_id=studio") {
		t.Fatal("redaction removed ordinary query data from the platform auth URL")
	}

	var nativeReceipt map[string]any
	if err := json.Unmarshal([]byte(receipt), &nativeReceipt); err != nil {
		t.Fatalf("decode native receipt fixture: %v", err)
	}
	server := newNativeBrowserTestServer(t)
	originalFactory := server.sandboxDataPlaneClientFactory
	server.sandboxDataPlaneClientFactory = func(timeout time.Duration) *http.Client {
		client := originalFactory(timeout)
		next := client.Transport
		client.Transport = projectAssistantNativeBrowserTestRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.Body != nil {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				request.Body = io.NopCloser(bytes.NewReader(body))
				var envelope nativeBrowserTestEnvelope
				if json.Unmarshal(body, &envelope) == nil && envelope.Method == "tools/call" && envelope.Params.Name == "browser_network_requests" {
					recorder := httptest.NewRecorder()
					recorder.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(recorder).Encode(map[string]any{
						"jsonrpc": "2.0", "id": envelope.ID, "result": nativeReceipt,
					})
					return recorder.Result(), nil
				}
			}
			return next.RoundTrip(request)
		})
		return client
	}
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		Arguments:      map[string]any{"includeStatic": true},
		AssistantRunID: "run-native-browser-auth-redaction",
	}
	boundaryResult, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_network_requests", projectAssistantToolRiskRead)
	if err != nil {
		t.Fatalf("native browser response boundary: %v", err)
	}
	for _, sensitive := range []string{oneTimeCode, handoffCode, authorizeState, callbackState} {
		if strings.Contains(boundaryResult, sensitive) {
			t.Fatal("native browser response boundary returned an authentication secret")
		}
	}
	if !strings.Contains(boundaryResult, `"status":302`) || !strings.Contains(boundaryResult, ordinaryQuery) {
		t.Fatal("native browser response boundary changed redirect status or ordinary query data")
	}

	modelResult := (&projectEinoAssistantRunState{}).RegisterNativeBrowserReceipt("browser_network_requests", boundaryResult)
	durableResult := projectEinoAssistantPersistentToolResult("browser_network_requests", boundaryResult)
	messageStore, scope := newAssistantRunEventLedgerTestStore(t, "run-native-browser-auth-redaction")
	ledger := newProjectAssistantRunEventLedger(messageStore, scope, "run-native-browser-auth-redaction")
	decision, err := ledger.BeginToolCall(context.Background(), "browser-call", projectAssistantToolSpec{
		Name: "browser_network_requests", Risk: projectAssistantToolRiskRead,
	}, map[string]any{"includeStatic": true})
	if err != nil {
		t.Fatalf("BeginToolCall: %v", err)
	}
	if _, err := ledger.FinishToolCall(context.Background(), decision.Token, durableResult, nil); err != nil {
		t.Fatalf("FinishToolCall: %v", err)
	}
	audit, err := json.Marshal(listAssistantRunEventLedgerEvents(t, messageStore, scope, "run-native-browser-auth-redaction"))
	if err != nil {
		t.Fatalf("marshal audit events: %v", err)
	}
	for _, serialized := range []string{modelResult, durableResult, string(audit)} {
		for _, sensitive := range []string{oneTimeCode, handoffCode, authorizeState, callbackState} {
			if strings.Contains(serialized, sensitive) {
				t.Fatal("native browser authentication secret reached model or audit data")
			}
		}
	}
}

func TestProjectAssistantNativeBrowserAuthErrorsAreRedactedBeforeModelAndDurability(t *testing.T) {
	ctx := context.Background()
	h := newProjectAssistantV2ToolHarness(t, "native-browser-error-auth-redaction")
	server := h.server
	server.hubBase = "https://hub.example"
	server.callers = newTestCallers(nil, "")
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	defer server.browserSessions.closeAll()
	configurePreviewInteractionBrowserTestServer(t, server, nil)
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}

	rpcErrorText := (&nativeBrowserSensitiveRPCError{}).Error()
	server.sandboxDataPlaneClientFactory = nativeBrowserTestClient(func(request *http.Request) (*http.Response, error) {
		var envelope nativeBrowserTestEnvelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			return nil, err
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "initialize":
			recorder.Header().Set("Mcp-Session-Id", "native-browser-error-session")
			_ = json.NewEncoder(recorder).Encode(map[string]any{
				"jsonrpc": "2.0", "id": envelope.ID,
				"result": map[string]any{"protocolVersion": browserMCPProtocolVersion},
			})
		case "notifications/initialized":
			recorder.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if envelope.Params.Name == "browser_tabs" {
				_ = json.NewEncoder(recorder).Encode(map[string]any{
					"jsonrpc": "2.0", "id": envelope.ID,
					"error": map[string]string{"message": rpcErrorText},
				})
				return recorder.Result(), nil
			}
			text := "ok"
			if envelope.Params.Name == browserMCPToolNavigate || envelope.Params.Name == browserMCPToolSnapshot {
				text = "- Page URL: https://demo.preview.example/\n- Page Snapshot:\n- generic [ref=e1]:"
			}
			_ = json.NewEncoder(recorder).Encode(map[string]any{
				"jsonrpc": "2.0", "id": envelope.ID,
				"result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": text}}},
			})
		default:
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": map[string]any{}})
		}
		return recorder.Result(), nil
	})

	identity := h.req.Identity
	identity.clusterID = "cluster-a"
	h.req.Identity = identity
	var streamEvents []projectToolCallStreamEvent
	h.req.StreamCallbacks.OnToolCall = func(event projectToolCallStreamEvent) {
		streamEvents = append(streamEvents, event)
	}
	browserTools := projectAssistantNativeBrowserToolsForSpecs(server, []projectMCPTool{{
		Name: "browser_snapshot", InputSchema: json.RawMessage(`{"type":"object"}`),
	}})
	if len(browserTools) != 1 {
		t.Fatalf("native browser tools = %d, want one snapshot tool", len(browserTools))
	}
	var boundaryErr error
	tool := projectAssistantToolFunc{
		spec: browserTools[0].Spec(),
		call: func(callCtx context.Context, request projectAssistantToolCallRequest) (string, error) {
			result, err := browserTools[0].Call(callCtx, request)
			boundaryErr = err
			return result, err
		},
	}
	runState := newProjectEinoAssistantRunState()
	wrapped := projectEinoAssistantTool{
		server: server, tool: tool, req: h.req, runState: runState, discoveredBrowserBound: true,
	}
	modelResult, err := wrapped.invokeAllowedTool(ctx, "call-browser-error", tool.Spec(), map[string]any{})
	if err != nil {
		t.Fatalf("browser tool returned an execution error instead of a model result: %v", err)
	}
	if boundaryErr == nil {
		t.Fatal("native browser error path returned nil error")
	}
	var boundarySafetyError *projectAssistantNativeBrowserSafetyError
	if !errors.As(boundaryErr, &boundarySafetyError) || boundarySafetyError.Stage != "browser_tabs" {
		t.Fatalf("boundary error = %T %v, want browser_tabs safety error", boundaryErr, boundaryErr)
	}

	const ordinaryQuery = "https://preview.example/assets.js?state=open&filter=active"
	for _, serialized := range []string{boundaryErr.Error(), modelResult} {
		for _, sensitive := range []string{"ONE_TIME_CODE_SENTINEL", "HANDOFF_CODE_SENTINEL", "AUTHORIZE_STATE_SENTINEL", "CALLBACK_STATE_SENTINEL"} {
			if strings.Contains(serialized, sensitive) {
				t.Fatalf("native browser auth secret reached boundary or model text: %s", serialized)
			}
		}
		if !strings.Contains(serialized, ordinaryQuery) {
			t.Fatalf("ordinary query data was changed: %s", serialized)
		}
	}

	outcome, ok, err := h.req.eventLedger.ToolCallOutcome(ctx, "call-browser-error")
	if err != nil || !ok || !outcome.Failed {
		t.Fatalf("durable browser failure outcome = %#v, found %t, err %v", outcome, ok, err)
	}
	events, err := json.Marshal(listAssistantRunEventLedgerEvents(t, h.messages, h.scope, h.req.AssistantRun.ID))
	if err != nil {
		t.Fatalf("marshal durable events: %v", err)
	}
	conversationItems, err := h.messages.ListAssistantConversationItems(ctx, h.scope, 0, 100)
	if err != nil {
		t.Fatalf("list conversation items: %v", err)
	}
	stream, err := json.Marshal(streamEvents)
	if err != nil {
		t.Fatalf("marshal streamed tool events: %v", err)
	}
	serialized := []string{outcome.Result, outcome.Error, string(events), string(stream)}
	for _, item := range conversationItems {
		serialized = append(serialized, string(item.Payload))
	}
	for _, value := range serialized {
		for _, sensitive := range []string{"ONE_TIME_CODE_SENTINEL", "HANDOFF_CODE_SENTINEL", "AUTHORIZE_STATE_SENTINEL", "CALLBACK_STATE_SENTINEL"} {
			if strings.Contains(value, sensitive) {
				t.Fatalf("native browser auth secret reached durable/model event data: %s", value)
			}
		}
	}
}

func TestProjectAssistantNativeBrowserAuthErrorRedactionPreservesCancellationAndSafetyTypes(t *testing.T) {
	cause := errors.New("native browser cause")
	underlying := &nativeBrowserSensitiveRPCError{cause: cause}
	safety := projectAssistantNativeBrowserSafetyErrorAt("browser_tabs", underlying)
	got := projectAssistantRedactNativeBrowserAuthError(safety)
	for _, sensitive := range []string{"ONE_TIME_CODE_SENTINEL", "HANDOFF_CODE_SENTINEL", "AUTHORIZE_STATE_SENTINEL", "CALLBACK_STATE_SENTINEL"} {
		if strings.Contains(got.Error(), sensitive) {
			t.Fatalf("safety error retained auth secret %q: %s", sensitive, got)
		}
	}
	var safetyError *projectAssistantNativeBrowserSafetyError
	if !errors.As(got, &safetyError) || safetyError.Stage != "browser_tabs" || !errors.Is(got, cause) {
		t.Fatalf("redacted error lost safety/cause identity: %#v", got)
	}
	var rpcError *nativeBrowserSensitiveRPCError
	if !errors.As(got, &rpcError) {
		t.Fatal("redacted error lost the underlying typed RPC error")
	}

	canceled := fmt.Errorf("browser request failed at https://hub.example/auth/apps/authorize?state=AUTHORIZE_STATE_SENTINEL: %w", context.Canceled)
	redactedCanceled := projectAssistantRedactNativeBrowserAuthError(canceled)
	if strings.Contains(redactedCanceled.Error(), "AUTHORIZE_STATE_SENTINEL") || !errors.Is(redactedCanceled, context.Canceled) {
		t.Fatalf("redacted cancellation = %v; want hidden state and errors.Is(context.Canceled)", redactedCanceled)
	}

	plain := errors.New("ordinary browser failure")
	if got := projectAssistantRedactNativeBrowserAuthError(plain); got != plain {
		t.Fatal("error without an auth URL was needlessly wrapped")
	}
}

func TestProjectAssistantNativeBrowserSafetyOutcomesRedactAuthSecrets(t *testing.T) {
	underlying := &nativeBrowserSensitiveRPCError{cause: errors.New("native browser cause")}
	safetyErr := projectAssistantNativeBrowserSafetyErrorAt("browser_snapshot", underlying)
	preflightErr := &projectAssistantNativeBrowserPreflightError{Err: safetyErr}
	for status, result := range map[string]string{
		"not_executed":    projectAssistantNativeBrowserOutcomeNotExecuted(preflightErr),
		"outcome_unknown": projectAssistantNativeBrowserOutcomeUnknown(safetyErr),
	} {
		sanitized := projectAssistantRedactNativeBrowserAuthURLs(result)
		for _, sensitive := range []string{"ONE_TIME_CODE_SENTINEL", "HANDOFF_CODE_SENTINEL", "AUTHORIZE_STATE_SENTINEL", "CALLBACK_STATE_SENTINEL"} {
			if strings.Contains(sanitized, sensitive) {
				t.Fatalf("%s safety outcome retained auth secret %q: %s", status, sensitive, sanitized)
			}
		}
		var outcome map[string]any
		if err := json.Unmarshal([]byte(sanitized), &outcome); err != nil {
			t.Fatalf("%s safety outcome is not valid JSON: %v", status, err)
		}
		if outcome["status"] != status {
			t.Fatalf("sanitized safety outcome status = %#v, want %q", outcome["status"], status)
		}
	}
}

func TestProjectAssistantNativeBrowserToolsJoinEinoDiscovery(t *testing.T) {
	browser := projectAssistantToolFunc{spec: projectAssistantToolSpec{
		Name:        "browser_snapshot",
		Description: "Take an accessibility snapshot",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Risk:        projectAssistantToolRiskRead,
	}}
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, previewInspector: &fakeProjectAssistantPreviewInspector{}}
	discovery := projectEinoAssistantDiscoverTools(context.Background(), server, projectAssistantRunRequest{
		ToolPort:   fakeNativeBrowserToolPort{tools: []projectAssistantTool{browser}},
		TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	})
	if len(discovery.BrowserTools) != 1 || discovery.BrowserTools[0].Spec().Name != "browser_snapshot" {
		t.Fatalf("browser discovery = %#v", discovery.BrowserTools)
	}
	if len(discovery.MCPTools) != 0 || discovery.Prompt == "" {
		t.Fatalf("discovery = %#v", discovery)
	}
	runState := newProjectEinoAssistantRunState()
	runState.SetToolDiscovery(discovery)
	tools, err := projectEinoAssistantToolsForDiscovery(context.Background(), server, projectAssistantRunRequest{
		ToolPort:   fakeNativeBrowserToolPort{tools: []projectAssistantTool{browser}},
		TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}, runState, discovery)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		info, infoErr := tool.Info(context.Background())
		if infoErr == nil && info.Name == "browser_snapshot" {
			return
		}
	}
	t.Fatalf("native browser tool missing from Eino tools: %#v", tools)
}

func TestProjectAssistantCatalogOmitsLegacyPreviewWrappers(t *testing.T) {
	registry := projectAssistantLocalToolRegistry(nil)
	for _, name := range []string{projectToolInspectDevelopmentPreview, projectToolInteractDevelopmentPreview} {
		if registry.Has(name) {
			t.Fatalf("legacy preview wrapper %q remains in the current catalog", name)
		}
		for _, tool := range registry.Tools(false) {
			if tool != nil && projectToolBaseName(tool.Spec().Name) == name {
				t.Fatalf("legacy preview wrapper %q remains in Tools", name)
			}
		}
	}
	// Historical lookup is retained for decoding old event/checkpoint payloads;
	// it is intentionally not the model-facing catalog.
	if _, ok := registry.Get(projectToolInspectDevelopmentPreview); !ok {
		t.Fatal("legacy lookup disappeared; old event decoding needs it")
	}
}

const nativeBrowserValidSnapshotReceipt = `{"isError":false,"content":[{"type":"text","text":"- Page URL: https://demo.preview.example/\n- Page Snapshot:\n- generic [ref=e1]:"}]}`

func TestProjectAssistantNativeBrowserSnapshotEvidenceRequiresValidReceipt(t *testing.T) {
	cases := []struct {
		name    string
		receipt string
		valid   bool
	}{
		{name: "empty object", receipt: `{}`},
		{name: "empty content", receipt: `{"isError":false,"content":[]}`},
		{name: "tool error", receipt: `{"isError":true,"content":[{"type":"text","text":"browser snapshot failed"}]}`},
		{name: "valid accessibility snapshot", receipt: nativeBrowserValidSnapshotReceipt, valid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := newProjectEinoAssistantRunState()
			state.RecordSourceMutation()
			state.RecordToolMessage(chatMessage{Role: "tool", Name: browserMCPToolSnapshot, Content: tc.receipt})
			evidence := state.CompletionEvidence()
			if evidence.PreviewRenderedStateObserved != tc.valid || (tc.valid && evidence.PreviewEvidenceOutcome != "rendered_verified") {
				t.Fatalf("snapshot receipt evidence = %#v, valid=%v", evidence, tc.valid)
			}
			if !tc.valid && evidence.PreviewInteractionVerified {
				t.Fatalf("invalid snapshot claimed interaction evidence: %#v", evidence)
			}
		})
	}
}

func TestProjectAssistantNativeBrowserInvalidSnapshotKeepsInteractionPending(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	state.RecordToolMessage(chatMessage{Role: "tool", Name: "browser_click", Content: `{"isError":false,"content":[{"type":"text","text":"clicked"}]}`})
	for _, receipt := range []string{
		`{}`,
		`{"isError":false,"content":[]}`,
		`{"isError":true,"content":[{"type":"text","text":"snapshot failed"}]}`,
	} {
		state.RecordToolMessage(chatMessage{Role: "tool", Name: browserMCPToolSnapshot, Content: receipt})
		if evidence := state.CompletionEvidence(); evidence.PreviewInteractionVerified {
			t.Fatalf("invalid snapshot promoted pending interaction: %#v", evidence)
		}
	}
	state.RecordToolMessage(chatMessage{Role: "tool", Name: browserMCPToolSnapshot, Content: nativeBrowserValidSnapshotReceipt})
	if evidence := state.CompletionEvidence(); !evidence.PreviewInteractionVerified || evidence.PreviewEvidenceOutcome != "interactions_verified" {
		t.Fatalf("valid follow-up snapshot did not verify interaction: %#v", evidence)
	}
}

func TestProjectAssistantNativeBrowserEvidenceUsesReceipts(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	state.RecordToolMessage(chatMessage{
		Role: "tool", Name: "browser_snapshot", ToolCallID: "snapshot-1",
		Content: nativeBrowserValidSnapshotReceipt,
	})
	evidence := state.CompletionEvidence()
	if evidence.PreviewEvidenceScope != "native_browser_receipt" || evidence.PreviewEvidenceOutcome != "rendered_verified" || !evidence.PreviewRenderedStateObserved {
		t.Fatalf("snapshot evidence = %#v", evidence)
	}
	if evidence.PreviewAssertionsObserved || evidence.PreviewAssertionsPassed {
		t.Fatal("native browser receipt must not manufacture assertion evidence")
	}

	state.RecordToolMessage(chatMessage{
		Role: "tool", Name: "browser_click", ToolCallID: "click-1",
		Content: `{"isError":false,"content":[{"type":"text","text":"clicked"}]}`,
	})
	evidence = state.CompletionEvidence()
	if evidence.PreviewInteractionVerified || evidence.PreviewEvidenceOutcome == "interactions_verified" {
		t.Fatalf("click receipt must await a successful snapshot: %#v", evidence)
	}
	checkpoint := state.CheckpointState()
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	restored.RecordToolMessage(chatMessage{
		Role: "tool", Name: "browser_snapshot", ToolCallID: "snapshot-restored",
		Content: nativeBrowserValidSnapshotReceipt,
	})
	if evidence := restored.CompletionEvidence(); !evidence.PreviewInteractionVerified {
		t.Fatalf("restored snapshot did not verify pending interaction: %#v", evidence)
	}

	state.RecordToolMessage(chatMessage{
		Role: "tool", Name: "browser_snapshot", ToolCallID: "snapshot-verify",
		Content: nativeBrowserValidSnapshotReceipt,
	})
	evidence = state.CompletionEvidence()
	if !evidence.PreviewInteractionVerified || evidence.PreviewEvidenceOutcome != "interactions_verified" {
		t.Fatalf("snapshot verification evidence = %#v", evidence)
	}

	state.RecordToolMessage(chatMessage{
		Role: "tool", Name: "browser_snapshot", ToolCallID: "snapshot-2",
		Content: `{"isError":true,"content":[{"type":"text","text":"session not found"}]}`,
	})
	evidence = state.CompletionEvidence()
	if evidence.PreviewEvidenceOutcome != "failed" || evidence.PreviewRenderedStateObserved || evidence.PreviewInteractionVerified {
		t.Fatalf("failed receipt evidence = %#v", evidence)
	}
}

func TestProjectAssistantBrowserSessionOwnerIncludesProjectAndCaller(t *testing.T) {
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "project-a", UID: types.UID("uid-a")}}
	base := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        project,
		AssistantRunID: "run-a",
	}
	owner := (&Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}).nativeBrowserOwner(base)
	otherUser := base
	otherUser.Identity.user = "bob"
	otherProject := base
	otherProject.Project = &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "project-b", UID: types.UID("uid-b")}}
	if owner.key() == (&Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}).nativeBrowserOwner(otherUser).key() {
		t.Fatal("different caller inherited the same browser session key")
	}
	if owner.key() == (&Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}).nativeBrowserOwner(otherProject).key() {
		t.Fatal("different project inherited the same browser session key")
	}
}

func TestProjectAssistantNativeBrowserCallReusesSessionPerRun(t *testing.T) {
	server := newNativeBrowserTestServer(t)
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}}
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        project,
		AssistantRunID: "run-a",
	}
	for i := 0; i < 2; i++ {
		if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_snapshot", projectAssistantToolRiskRead); err != nil {
			t.Fatalf("snapshot %d: %v", i+1, err)
		}
	}
	if server.browserSessions == nil || len(server.browserSessions.sessions) != 1 {
		t.Fatalf("browser sessions = %#v, want one owner session", server.browserSessions)
	}
	other := request
	other.Identity.user = "bob"
	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), other, "browser_snapshot", projectAssistantToolRiskRead); err != nil {
		t.Fatalf("other owner snapshot: %v", err)
	}
	if got := len(server.browserSessions.sessions); got != 1 {
		t.Fatalf("browser sessions after other owner = %d, want one active owner", got)
	}
}

func TestProjectAssistantNativeBrowserFirstNonNavigationStartsAtPreview(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	defer server.browserSessions.closeAll()
	var toolCalls []string
	configurePreviewInteractionBrowserTestServer(t, server, func(method, tool string) {
		if method == "tools/call" {
			toolCalls = append(toolCalls, tool)
		}
	})
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-first-preview",
	}
	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolSnapshot, projectAssistantToolRiskRead); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if got, want := strings.Join(toolCalls, ","), "browser_navigate,browser_snapshot,browser_tabs"; got != want {
		t.Fatalf("first non-navigation tool calls = %q, want %q", got, want)
	}
}

func TestProjectAssistantNativeBrowserPrivateHandoffThenFirstNonNavigationStartsAtPreview(t *testing.T) {
	var hub *httptest.Server
	var preview *httptest.Server
	hub = httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("private handoff unexpectedly reached the real hub server")
	}))
	defer hub.Close()
	preview = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := url.Values{
			"cluster": {"cluster-a"},
			"group":   {"infrastructure.railgrid.ai"}, "resource": {"instances"}, "name": {"demo-dev"},
			"redirect_uri": {preview.URL + privateAppCallbackPath},
		}
		http.Redirect(w, r, hub.URL+privateAppAuthorizePath+"?"+query.Encode(), http.StatusFound)
	}))
	defer preview.Close()

	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders,
		projectIdentityTokenFor: func(context.Context, identity, *aiv1alpha1.Project) (string, error) { return "project-token", nil },
		hubBase:                 hub.URL, callers: newTestCallers(nil, hub.URL),
		hubPublicURL:                 hub.URL,
		previewInsecureSkipTLSVerify: true,
	}
	manager := newProjectAssistantBrowserSessionManager()
	server.browserSessions = manager
	defer manager.closeAll()
	previewURL := strings.TrimRight(preview.URL, "/") + "/"
	var toolCalls []string
	var initializeCalls int
	server.sandboxDataPlaneClientFactory = nativeBrowserTestClient(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost && request.URL.Path == browserSessionHandoffPath {
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Content-Type", "application/json")
			_, _ = recorder.WriteString(`{"path":"/auth/apps/preview-handoff?code=one-use"}`)
			return recorder.Result(), nil
		}
		var envelope nativeBrowserTestEnvelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			return nil, err
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "initialize":
			initializeCalls++
			recorder.Header().Set("Mcp-Session-Id", "private-session")
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": initializeCalls, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
		case "notifications/initialized":
			recorder.WriteHeader(http.StatusAccepted)
		case "tools/call":
			toolCalls = append(toolCalls, envelope.Params.Name)
			content := "- Page URL: " + previewURL + "\n- Page Snapshot:\n- generic [ref=e1]:"
			switch envelope.Params.Name {
			case browserMCPToolNavigate:
				if strings.HasPrefix(projectToolString(envelope.Params.Arguments["url"]), hub.URL) {
					content = "handoff complete"
				}
			case "browser_tabs":
				content = "- 0: " + previewURL
			}
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": len(toolCalls), "result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": content}}}})
		default:
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
		}
		return recorder.Result(), nil
	})
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-private-first-preview",
	}
	ref := dataPlaneRef{Resource: "instances", Name: "browser"}
	entry := manager.entry(server.nativeBrowserOwner(request), ref)
	if _, err := server.callProjectAssistantNativeBrowserSession(context.Background(), request, entry, ref, browserMCPToolSnapshot, map[string]any{}, true, previewURL, false); err != nil {
		t.Fatalf("private first snapshot: %v", err)
	}
	if initializeCalls != 1 {
		t.Fatalf("private first snapshot initialize calls = %d, want one", initializeCalls)
	}
	if got, want := strings.Join(toolCalls, ","), "browser_navigate,browser_navigate,browser_snapshot,browser_tabs"; got != want {
		t.Fatalf("private first non-navigation tool calls = %q, want %q", got, want)
	}
}

func TestProjectAssistantManagedPrivatePreviewHandoffRenewal(t *testing.T) {
	newBody := func(expiry any) string {
		payload := map[string]any{"path": browserSessionHandoffPath + "?code=one-use"}
		if expiry != nil {
			payload["expiresAt"] = expiry
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	callSnapshot := func(t *testing.T, h *managedPrivatePreviewTestHarness, previewURL string) {
		t.Helper()
		if _, err := h.call(browserMCPToolSnapshot, map[string]any{}, previewURL); err != nil {
			t.Fatalf("private snapshot: %v", err)
		}
	}

	t.Run("missing and malformed expiry are usable once but never cached", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			body string
		}{
			{name: "missing", body: newBody(nil)},
			{name: "malformed", body: `{"path":"/auth/apps/preview-handoff?code=one-use","expiresAt":"not-a-timestamp"}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := newManagedPrivatePreviewTestHarness(t, tc.body)
				callSnapshot(t, h, h.previewURL)
				callSnapshot(t, h, h.previewURL)
				if h.handoffCalls != 2 {
					t.Fatalf("handoff calls = %d, want one per call without a reusable expiry", h.handoffCalls)
				}
				if got := countNativeBrowserToolCalls(h.toolCalls, browserMCPToolSnapshot); got != 2 {
					t.Fatalf("snapshot calls = %d, want both requested actions to dispatch", got)
				}
			})
		}
	})

	t.Run("valid expiry is reused until the refresh margin", func(t *testing.T) {
		expiresAt := time.Now().Add(5 * time.Minute).Unix()
		h := newManagedPrivatePreviewTestHarness(t, newBody(expiresAt))
		virtualNow := time.Now()
		h.server.browserSessionManager().handoffNow = func() time.Time { return virtualNow }
		callSnapshot(t, h, h.previewURL)
		virtualNow = time.Unix(expiresAt, 0).Add(-projectAssistantPrivatePreviewHandoffRefreshMargin - time.Nanosecond)
		callSnapshot(t, h, h.previewURL)
		if h.handoffCalls != 1 {
			t.Fatalf("handoff calls before refresh margin = %d, want one", h.handoffCalls)
		}
		virtualNow = time.Unix(expiresAt, 0).Add(-projectAssistantPrivatePreviewHandoffRefreshMargin)
		callSnapshot(t, h, h.previewURL)
		if h.handoffCalls != 2 {
			t.Fatalf("handoff calls at refresh margin = %d, want renewal", h.handoffCalls)
		}
	})

	t.Run("app reference and preview base changes renew scope", func(t *testing.T) {
		expiresAt := time.Now().Add(time.Hour).Unix()
		h := newManagedPrivatePreviewTestHarness(t, newBody(expiresAt))
		callSnapshot(t, h, h.previewURL)
		h.appName = "demo-dev-next"
		callSnapshot(t, h, h.previewURL)
		callSnapshot(t, h, h.previewURL+"next/")
		if h.handoffCalls != 3 {
			t.Fatalf("handoff calls after scope/base changes = %d, want three", h.handoffCalls)
		}
		for _, want := range []string{h.previewURL, h.previewURL + "next/"} {
			if !containsNavigatedURL(h.navigatedURLs, want) {
				t.Fatalf("renewal did not restore selected preview %q; navigations=%v", want, h.navigatedURLs)
			}
		}
	})

	t.Run("expired response and failed renewal block requested action", func(t *testing.T) {
		t.Run("expired response", func(t *testing.T) {
			h := newManagedPrivatePreviewTestHarness(t, newBody(time.Now().Add(-time.Minute).Unix()))
			_, err := h.call("browser_click", map[string]any{}, h.previewURL)
			var preflight *projectAssistantNativeBrowserPreflightError
			if !errors.As(err, &preflight) {
				t.Fatalf("expired handoff error = %v, want preflight failure", err)
			}
			if outcome := decodeNativeBrowserOutcome(t, projectAssistantNativeBrowserOutcomeNotExecuted(err)); outcome["status"] != "not_executed" {
				t.Fatalf("expired handoff outcome = %#v, want not_executed", outcome)
			}
			if countNativeBrowserToolCalls(h.toolCalls, "browser_click") != 0 || countNativeBrowserToolCalls(h.toolCalls, browserMCPToolNavigate) != 0 {
				t.Fatalf("expired handoff dispatched browser calls: %v", h.toolCalls)
			}
		})
		t.Run("failed renewal", func(t *testing.T) {
			expiresAt := time.Now().Add(time.Hour).Unix()
			h := newManagedPrivatePreviewTestHarness(t, newBody(expiresAt))
			callSnapshot(t, h, h.previewURL)
			h.appName = "demo-dev-next"
			h.handoffStatus = http.StatusServiceUnavailable
			_, err := h.call("browser_click", map[string]any{}, h.previewURL)
			var preflight *projectAssistantNativeBrowserPreflightError
			if !errors.As(err, &preflight) {
				t.Fatalf("failed renewal error = %v, want preflight failure", err)
			}
			if outcome := decodeNativeBrowserOutcome(t, projectAssistantNativeBrowserOutcomeNotExecuted(err)); outcome["status"] != "not_executed" {
				t.Fatalf("failed renewal outcome = %#v, want not_executed", outcome)
			}
			if got := countNativeBrowserToolCalls(h.toolCalls, "browser_click"); got != 0 {
				t.Fatalf("click calls after failed renewal = %d, want zero", got)
			}
		})
	})

	t.Run("renewal requires a fresh model-visible page context", func(t *testing.T) {
		expiresAt := time.Now().Add(5 * time.Minute).Unix()
		h := newManagedPrivatePreviewTestHarness(t, newBody(expiresAt))

		result, err := h.call("browser_click", map[string]any{"element": "e1"}, h.previewURL)
		if err != nil {
			t.Fatalf("click after handoff renewal returned error: %v", err)
		}
		outcome := decodeNativeBrowserOutcome(t, result)
		if outcome["status"] != "not_executed" || outcome["reason"] != "preview_context_changed" || outcome["replayed"] != false {
			t.Fatalf("renewed-page click outcome = %#v", outcome)
		}
		message, _ := outcome["message"].(string)
		if !strings.Contains(message, "fresh browser_snapshot") || !strings.Contains(message, "references are stale") {
			t.Fatalf("renewed-page click guidance = %q", message)
		}
		if got := countNativeBrowserToolCalls(h.toolCalls, "browser_click"); got != 0 {
			t.Fatalf("click dispatched after renewal = %d, want zero", got)
		}
		if got := countNativeBrowserToolCalls(h.toolCalls, browserMCPToolSnapshot); got != 1 {
			t.Fatalf("renewal preflight snapshots = %d, want one", got)
		}
		assertManagedPrivatePreviewEntryRetained(t, h)

		callSnapshot(t, h, h.previewURL)
		result, err = h.call("browser_click", map[string]any{"element": "e1"}, h.previewURL)
		if err != nil {
			t.Fatalf("click after fresh snapshot returned error: %v", err)
		}
		if projectAssistantNativeBrowserReceiptIsError(result) {
			t.Fatalf("click after fresh snapshot returned tool error: %s", result)
		}
		if got := countNativeBrowserToolCalls(h.toolCalls, "browser_click"); got != 1 {
			t.Fatalf("click dispatches after fresh snapshot = %d, want exactly one", got)
		}
		if h.handoffCalls != 1 {
			t.Fatalf("handoff calls after fresh snapshot = %d, want one reusable handoff", h.handoffCalls)
		}
		assertManagedPrivatePreviewEntryRetained(t, h)
	})

	t.Run("history action after renewal uses explicit navigation guidance", func(t *testing.T) {
		expiresAt := time.Now().Add(5 * time.Minute).Unix()
		h := newManagedPrivatePreviewTestHarness(t, newBody(expiresAt))
		result, err := h.call("browser_navigate_back", map[string]any{}, h.previewURL)
		if err != nil {
			t.Fatalf("history action after renewal returned error: %v", err)
		}
		outcome := decodeNativeBrowserOutcome(t, result)
		if outcome["status"] != "not_executed" || outcome["reason"] != "preview_context_changed" || outcome["replayed"] != false {
			t.Fatalf("renewed-page history outcome = %#v", outcome)
		}
		message, _ := outcome["message"].(string)
		if !strings.Contains(message, "browser history") || !strings.Contains(message, "browser_navigate") {
			t.Fatalf("renewed-page history guidance = %q", message)
		}
		if got := countNativeBrowserToolCalls(h.toolCalls, "browser_navigate_back"); got != 0 {
			t.Fatalf("history-back dispatches after renewal = %d, want zero", got)
		}
		assertManagedPrivatePreviewEntryRetained(t, h)
	})

	t.Run("missing or malformed expiry blocks page actions without snapshot retry guidance", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			body string
		}{
			{name: "missing", body: newBody(nil)},
			{name: "malformed", body: `{"path":"/auth/apps/preview-handoff?code=one-use","expiresAt":"not-a-timestamp"}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := newManagedPrivatePreviewTestHarness(t, tc.body)
				result, err := h.call("browser_click", map[string]any{"element": "e1"}, h.previewURL)
				if err != nil {
					t.Fatalf("click without reusable expiry returned error: %v", err)
				}
				outcome := decodeNativeBrowserOutcome(t, result)
				if outcome["status"] != "not_executed" || outcome["reason"] != "handoff_expiry_unavailable" || outcome["replayed"] != false {
					t.Fatalf("missing-expiry click outcome = %#v", outcome)
				}
				message, _ := outcome["message"].(string)
				if !strings.Contains(message, "expiry metadata") || strings.Contains(message, "fresh browser_snapshot") {
					t.Fatalf("missing-expiry guidance = %q", message)
				}
				if got := countNativeBrowserToolCalls(h.toolCalls, "browser_click"); got != 0 {
					t.Fatalf("click dispatches without reusable expiry = %d, want zero", got)
				}
				assertManagedPrivatePreviewEntryRetained(t, h)

				// Read-only observation remains available for compatibility, but a
				// subsequent page action still fails closed while expiry is absent.
				callSnapshot(t, h, h.previewURL)
				result, err = h.call("browser_click", map[string]any{"element": "e1"}, h.previewURL)
				if err != nil {
					t.Fatalf("second click without reusable expiry returned error: %v", err)
				}
				outcome = decodeNativeBrowserOutcome(t, result)
				if outcome["status"] != "not_executed" || outcome["reason"] != "handoff_expiry_unavailable" {
					t.Fatalf("second missing-expiry click outcome = %#v", outcome)
				}
				if got := countNativeBrowserToolCalls(h.toolCalls, "browser_click"); got != 0 {
					t.Fatalf("click dispatches after read-only compatibility calls = %d, want zero", got)
				}
			})
		}
	})
}

func assertManagedPrivatePreviewEntryRetained(t *testing.T, h *managedPrivatePreviewTestHarness) {
	t.Helper()
	manager := h.server.browserSessionManager()
	manager.mu.Lock()
	retained := manager.sessions[h.entry.owner.key()] == h.entry
	manager.mu.Unlock()
	if !retained {
		t.Fatal("managed preview session was removed after a not_executed page-context receipt")
	}
	h.entry.mu.Lock()
	ready := h.entry.session != nil && h.entry.previewReady
	h.entry.mu.Unlock()
	if !ready {
		t.Fatal("renewed managed preview session is not retained and ready")
	}
}

func countNativeBrowserToolCalls(calls []string, tool string) int {
	count := 0
	for _, call := range calls {
		if call == tool {
			count++
		}
	}
	return count
}

func containsNavigatedURL(calls []string, target string) bool {
	for _, call := range calls {
		if call == target {
			return true
		}
	}
	return false
}

func TestProjectAssistantNativeBrowserFirstNavigationIsNotDuplicated(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	defer server.browserSessions.closeAll()
	var toolCalls []string
	configurePreviewInteractionBrowserTestServer(t, server, func(method, tool string) {
		if method == "tools/call" {
			toolCalls = append(toolCalls, tool)
		}
	})
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-first-navigation",
		Arguments:      map[string]any{"url": "/tasks"},
	}
	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolNavigate, projectAssistantToolRiskRead); err != nil {
		t.Fatalf("first navigation: %v", err)
	}
	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolSnapshot, projectAssistantToolRiskRead); err != nil {
		t.Fatalf("snapshot after first navigation: %v", err)
	}
	if got, want := strings.Join(toolCalls, ","), "browser_navigate,browser_snapshot,browser_tabs,browser_snapshot,browser_tabs"; got != want {
		t.Fatalf("first navigation tool calls = %q, want %q", got, want)
	}
}

func TestProjectAssistantNativeBrowserNavigationStaysOnPreviewOrigin(t *testing.T) {
	args := map[string]any{"url": "https://attacker.example/"}
	if err := validateProjectAssistantNativeBrowserArguments("browser_navigate", args, "https://demo.preview.example/"); err == nil {
		t.Fatal("cross-origin native browser navigation was accepted")
	}
	args = map[string]any{"url": "/tasks#ignored"}
	if err := validateProjectAssistantNativeBrowserArguments("browser_navigate", args, "https://demo.preview.example/"); err != nil {
		t.Fatal(err)
	}
	if got := args["url"]; got != "https://demo.preview.example/tasks" {
		t.Fatalf("normalized native browser URL = %v", got)
	}
}

func TestProjectAssistantNativeBrowserPageOriginIgnoresDiagnosticURLs(t *testing.T) {
	onOriginWithExternalNetworkURL := `{"isError":false,"content":[{"type":"text","text":"- Page URL: https://demo.preview.example/tasks"}],"structuredContent":{"networkRequests":[{"url":"https://cdn.example/assets/app.js"}]}}`
	if err := validateProjectAssistantNativeBrowserPageOrigin(onOriginWithExternalNetworkURL, "https://demo.preview.example/"); err != nil {
		t.Fatalf("on-origin page with external diagnostic URL rejected: %v", err)
	}
	for _, name := range []string{"browser_network_requests", "browser_console_messages", "browser_click"} {
		if projectAssistantNativeBrowserReceiptReportsPageLocation(name) {
			t.Fatalf("diagnostic tool %q unexpectedly requires page-location validation", name)
		}
	}
}

func TestProjectAssistantNativeBrowserPageOriginRejectsNavigationEscape(t *testing.T) {
	sameOrigin := `{"isError":false,"content":[{"type":"text","text":"- Page URL: https://demo.preview.example/tasks"}]}`
	if err := validateProjectAssistantNativeBrowserPageOrigin(sameOrigin, "https://demo.preview.example/"); err != nil {
		t.Fatalf("same-origin receipt rejected: %v", err)
	}
	escaped := `{"isError":false,"content":[{"type":"text","text":"- Page URL: https://attacker.example/"}]}`
	if err := validateProjectAssistantNativeBrowserPageOrigin(escaped, "https://demo.preview.example/"); err == nil {
		t.Fatal("cross-origin navigation receipt was accepted")
	}
	for _, name := range []string{browserMCPToolNavigate, "browser_navigate_back", "browser_navigate_forward", browserMCPToolSnapshot} {
		if !projectAssistantNativeBrowserReceiptReportsPageLocation(name) {
			t.Fatalf("page-location tool %q was not selected for origin validation", name)
		}
	}
}

func TestProjectAssistantNativeBrowserTabsRemainOriginValidated(t *testing.T) {
	structured := `{"structuredContent":{"tabs":[{"url":"https://attacker.example/"}]}}`
	if err := validateProjectAssistantNativeBrowserSafetyTabs(structured, "https://demo.preview.example/"); err == nil {
		t.Fatal("cross-origin tab URL was accepted")
	}
}

func TestProjectAssistantNativeBrowserTabsParseOfficialMarkdownReceipt(t *testing.T) {
	receipt := `{"isError":false,"content":[{"type":"text","text":"### Result\n- 0: (current) [Example Domain](https://example.com/)"}]}`
	if got, want := projectAssistantNativeBrowserTabURLs(receipt), []string{"https://example.com/"}; !equalStrings(got, want) {
		t.Fatalf("tab URLs = %#v, want %#v", got, want)
	}
	if err := validateProjectAssistantNativeBrowserSafetyTabs(receipt, "https://example.com/"); err != nil {
		t.Fatalf("official browser_tabs receipt rejected: %v", err)
	}

	for name, text := range map[string]string{
		"blank page":  "### Result\n- 0: (current) [](about:blank)",
		"malformed":   "### Result\n- 0: (current) [Broken](not-a-url)",
		"missing URL": "### Result\n- 0: (current) [No URL]",
	} {
		t.Run(name, func(t *testing.T) {
			badReceipt := `{"isError":false,"content":[{"type":"text","text":"` + text + `"}]}`
			if err := validateProjectAssistantNativeBrowserSafetyTabs(badReceipt, "https://example.com/"); err == nil {
				t.Fatalf("invalid browser_tabs receipt was accepted: %s", text)
			}
		})
	}
}

func TestProjectEinoAssistantBrowserDiscoveryCachesAcrossModelBoundariesAndCheckpoint(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, previewInspector: &fakeProjectAssistantPreviewInspector{}}
	port := &countingNativeBrowserToolPort{
		server:       server,
		browserTools: nativeBrowserCatalogTestTools(server),
	}
	req := projectAssistantRunRequest{
		ToolPort:   port,
		TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	state := newProjectEinoAssistantRunState()
	first := projectEinoAssistantRefreshToolDiscovery(context.Background(), server, req, state)
	if !first.BrowserCatalogCached || len(first.BrowserTools) == 0 {
		t.Fatalf("initial browser discovery = %#v", first)
	}
	if port.discoverMCPCalls != 1 || port.discoverBrowserCalls != 1 {
		t.Fatalf("initial discovery calls = MCP %d, browser %d; want 1/1", port.discoverMCPCalls, port.discoverBrowserCalls)
	}

	state.NextModelCallOrdinal()
	second := projectEinoAssistantRefreshToolDiscovery(context.Background(), server, req, state)
	if !second.BrowserCatalogCached || len(second.BrowserTools) != len(first.BrowserTools) {
		t.Fatalf("refreshed browser discovery = %#v, want cached browser tools", second)
	}
	if port.discoverMCPCalls != 2 || port.discoverBrowserCalls != 1 {
		t.Fatalf("refreshed discovery calls = MCP %d, browser %d; want 2/1", port.discoverMCPCalls, port.discoverBrowserCalls)
	}

	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(state.CheckpointState())
	if _, ok := restored.NativeBrowserToolCatalog(); !ok {
		t.Fatal("successful browser catalog was not retained in the checkpoint")
	}
	projectEinoAssistantRefreshToolDiscovery(context.Background(), server, req, restored)
	if port.discoverMCPCalls != 3 || port.discoverBrowserCalls != 1 {
		t.Fatalf("checkpoint refresh calls = MCP %d, browser %d; want 3/1", port.discoverMCPCalls, port.discoverBrowserCalls)
	}
}

func TestProjectAssistantNativeBrowserManagedSessionSurvivesModelRefresh(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, ""), previewInspector: &fakeProjectAssistantPreviewInspector{}}
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	var initializeCalls int
	configurePreviewInteractionBrowserTestServer(t, server, func(method, _ string) {
		if method == "initialize" {
			initializeCalls++
		}
	})
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	port := &countingNativeBrowserToolPort{
		server:       server,
		browserTools: nativeBrowserCatalogTestTools(server),
	}
	req := projectAssistantRunRequest{
		ToolPort:   port,
		Identity:   identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:    &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	const assistantRunID = "run-refresh"
	state := newProjectEinoAssistantRunState()
	discovery := projectEinoAssistantRefreshToolDiscovery(context.Background(), server, req, state)
	call := func(name string, args map[string]any) {
		t.Helper()
		for _, tool := range discovery.BrowserTools {
			if strings.EqualFold(tool.Spec().Name, name) {
				if _, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
					Identity:       req.Identity,
					Project:        req.Project,
					AssistantRunID: assistantRunID,
					Arguments:      args,
				}); err != nil {
					t.Fatalf("browser_%s call: %v", name, err)
				}
				return
			}
		}
		t.Fatalf("browser tool %q missing from discovery", name)
	}
	call("browser_navigate", map[string]any{"url": "/"})
	state.NextModelCallOrdinal()
	discovery = projectEinoAssistantRefreshToolDiscovery(context.Background(), server, req, state)
	call("browser_snapshot", nil)
	call("browser_click", map[string]any{})
	if port.discoverBrowserCalls != 1 {
		t.Fatalf("browser discovery calls = %d, want one across model samples", port.discoverBrowserCalls)
	}
	if initializeCalls != 1 {
		t.Fatalf("managed browser session initialized %d times, want one", initializeCalls)
	}
}

func TestProjectAssistantBrowserDiscoveryDoesNotOpenOrCloseManagedSession(t *testing.T) {
	server := newNativeBrowserTestServer(t)
	var methods []string
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				return browserMCPTestEventStreamResponse(request), nil
			}
			if request.Method == http.MethodDelete {
				methods = append(methods, "DELETE")
				recorder := httptest.NewRecorder()
				recorder.WriteHeader(http.StatusNoContent)
				return recorder.Result(), nil
			}
			var envelope nativeBrowserTestEnvelope
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				return nil, err
			}
			methods = append(methods, envelope.Method)
			if envelope.Method == "tools/list" {
				return nil, errors.New("unexpected second browser discovery")
			}
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Content-Type", "application/json")
			switch envelope.Method {
			case "initialize":
				recorder.Header().Set("Mcp-Session-Id", "managed-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			case "tools/call":
				content := "- Page URL: https://demo.preview.example/\n- Page Snapshot:\n- generic [ref=e1]:"
				if envelope.Params.Name == "browser_tabs" {
					content = "- 0: https://demo.preview.example/"
				}
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": content}}}})
			default:
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
			}
			return recorder.Result(), nil
		})}
	}
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-discovery-guard",
		Arguments:      map[string]any{"url": "/"},
	}
	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolNavigate, projectAssistantToolRiskRead); err != nil {
		t.Fatalf("managed browser call failed: %v", err)
	}

	port := projectAssistantHTTPToolPort{server: server, request: httptest.NewRequest(http.MethodPost, "/", nil)}
	if _, err := port.DiscoverBrowser(context.Background(), request.Identity, projectLLMSettings{}); err == nil || !strings.Contains(err.Error(), "managed browser session is active") {
		t.Fatalf("discovery while managed session active = %v, want guarded failure", err)
	}
	for _, method := range methods {
		if method == "tools/list" || method == "DELETE" {
			t.Fatalf("managed-session discovery performed destructive method %q; methods=%v", method, methods)
		}
	}

	ref := dataPlaneRef{Resource: "instances", Name: "browser"}
	server.browserSessions.setBrowserCatalog(request.Identity, ref, []projectMCPTool{
		{Name: "browser_navigate", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_snapshot", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_console_messages", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_click", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "browser_fill_form", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	if tools, err := port.DiscoverBrowser(context.Background(), request.Identity, projectLLMSettings{}); err != nil || len(tools) != 5 {
		t.Fatalf("cached browser discovery = %d tools, err=%v; want five tools without transport", len(tools), err)
	}
}

func TestProjectAssistantLegacyInspectionCannotCloseManagedSessionAtModelBoundary(t *testing.T) {
	server := newNativeBrowserTestServer(t)
	var initializeCalls, deleteCalls int
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				return browserMCPTestEventStreamResponse(request), nil
			}
			if request.Method == http.MethodDelete {
				deleteCalls++
				recorder := httptest.NewRecorder()
				recorder.WriteHeader(http.StatusNoContent)
				return recorder.Result(), nil
			}
			var envelope nativeBrowserTestEnvelope
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				return nil, err
			}
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Content-Type", "application/json")
			switch envelope.Method {
			case "initialize":
				initializeCalls++
				recorder.Header().Set("Mcp-Session-Id", fmt.Sprintf("model-boundary-session-%d", initializeCalls))
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			case "tools/call":
				content := "ok"
				switch envelope.Params.Name {
				case browserMCPToolSnapshot:
					content = "- Page URL: https://demo.preview.example/\n- Page Title: Demo\n- button \\\"Save\\\" [ref=e1]\n"
				case "browser_tabs":
					content = "- 0: https://demo.preview.example/\n"
				}
				_ = json.NewEncoder(recorder).Encode(map[string]any{
					"jsonrpc": "2.0", "id": 1,
					"result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": content}}},
				})
			default:
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
			}
			return recorder.Result(), nil
		})}
	}
	identity := identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}}
	request := projectAssistantToolCallRequest{
		Identity:       identity,
		Project:        project,
		AssistantRunID: "run-model-boundary",
		Arguments:      map[string]any{"url": "/"},
	}
	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolNavigate, projectAssistantToolRiskRead); err != nil {
		t.Fatalf("managed navigate: %v", err)
	}
	clickRequest := request
	clickRequest.Arguments = map[string]any{}
	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), clickRequest, "browser_click", projectAssistantToolRiskRuntime); err != nil {
		t.Fatalf("managed click: %v", err)
	}

	inspectionDone := make(chan error, 1)
	go func() {
		_, err := server.inspectPreviewViaBrowserMCP(context.Background(), identity, dataPlaneRef{Resource: "instances", Name: "browser"}, projectAssistantPreviewInspectionRequest{URL: "https://demo.preview.example/"})
		inspectionDone <- err
	}()
	select {
	case err := <-inspectionDone:
		if !errors.Is(err, errProjectAssistantBrowserSessionBusy) {
			t.Fatalf("legacy inspection error = %v, want managed-session busy", err)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy inspection did not return while managed session was active")
	}
	if initializeCalls != 1 || deleteCalls != 0 {
		t.Fatalf("legacy inspection while active initialized %d times and deleted %d sessions; want 1/0", initializeCalls, deleteCalls)
	}

	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolSnapshot, projectAssistantToolRiskRead); err != nil {
		t.Fatalf("next model-boundary snapshot: %v", err)
	}

	server.browserSessions.releaseRun(request.AssistantRunID)
	if deleteCalls != 1 {
		t.Fatalf("managed release deleted %d sessions, want one", deleteCalls)
	}
	if _, err := server.inspectPreviewViaBrowserMCP(context.Background(), identity, dataPlaneRef{Resource: "instances", Name: "browser"}, projectAssistantPreviewInspectionRequest{URL: "https://demo.preview.example/"}); err != nil {
		t.Fatalf("legacy inspection after managed release: %v", err)
	}
	if initializeCalls != 2 || deleteCalls != 2 {
		t.Fatalf("legacy inspection after release initialized %d times and deleted %d sessions; want 2/2", initializeCalls, deleteCalls)
	}
}

func TestProjectAssistantNativeBrowserMutationReportsUnknownAndFailsClosedWhenSafetyObservationFails(t *testing.T) {
	cases := []struct {
		name     string
		snapshot string
		tabs     string
	}{
		{
			name:     "missing current URL",
			snapshot: "- Page Snapshot:\n- generic [ref=e1]:",
			tabs:     "- 0: https://demo.preview.example/",
		},
		{
			name:     "escaped popup tab",
			snapshot: "- Page URL: https://demo.preview.example/\n- Page Snapshot:\n- generic [ref=e1]:",
			tabs:     "- 0: https://attacker.example/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
			server.browserSessions = newProjectAssistantBrowserSessionManager()
			configurePreviewInteractionBrowserTestServer(t, server, nil)
			server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
				return "https://demo.preview.example/", nil
			}
			toolCalls := map[string]int{}
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
					var envelope nativeBrowserTestEnvelope
					if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
						return nil, err
					}
					recorder := httptest.NewRecorder()
					recorder.Header().Set("Content-Type", "application/json")
					switch envelope.Method {
					case "initialize":
						recorder.Header().Set("Mcp-Session-Id", "safety-test-session")
						_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
					case "notifications/initialized":
						recorder.WriteHeader(http.StatusAccepted)
					case "tools/call":
						content := "clicked"
						toolCalls[envelope.Params.Name]++
						switch envelope.Params.Name {
						case browserMCPToolSnapshot:
							content = "- Page URL: https://demo.preview.example/\n- Page Snapshot:\n- generic [ref=e1]:"
							if toolCalls[browserMCPToolSnapshot] > 1 {
								content = tc.snapshot
							}
						case "browser_tabs":
							content = "- 0: https://demo.preview.example/"
							if toolCalls["browser_tabs"] > 1 {
								content = tc.tabs
							}
						}
						_ = json.NewEncoder(recorder).Encode(map[string]any{
							"jsonrpc": "2.0", "id": 1,
							"result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": content}}},
						})
					default:
						return nil, errors.New("unexpected MCP method " + envelope.Method)
					}
					return recorder.Result(), nil
				})}
			}
			request := projectAssistantToolCallRequest{
				Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
				Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
				AssistantRunID: "run-safety",
			}
			navigateRequest := request
			navigateRequest.Arguments = map[string]any{"url": "/"}
			if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), navigateRequest, browserMCPToolNavigate, projectAssistantToolRiskRead); err != nil {
				t.Fatalf("establish preview before safety test: %v", err)
			}
			// Keep the click preflight safe and make only its postcheck fail.
			toolCalls[browserMCPToolSnapshot] = 0
			toolCalls["browser_tabs"] = 0
			result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_click", projectAssistantToolRiskRuntime)
			if err != nil {
				t.Fatalf("post-effect safety failure returned transport error: %v", err)
			}
			var outcome map[string]any
			if decodeErr := json.Unmarshal([]byte(result), &outcome); decodeErr != nil {
				t.Fatalf("outcome-unknown result = %q: %v", result, decodeErr)
			}
			if outcome["status"] != "outcome_unknown" || outcome["outcome"] != "unknown" || outcome["replayed"] != false {
				t.Fatalf("post-effect safety outcome = %#v", outcome)
			}
			if got := len(server.browserSessions.sessions); got != 0 {
				t.Fatalf("unsafe browser session count = %d, want discarded session", got)
			}
		})
	}
}

func TestProjectAssistantNativeBrowserReceiptBridgesScreenshotTransiently(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	raw := `{"isError":false,"content":[{"type":"text","text":"Page Snapshot"},{"type":"image","data":"aW1hZ2UtcGl4ZWxz","mimeType":"image/png"}]}`
	placeholder := state.RegisterNativeBrowserReceipt("browser_take_screenshot", raw)
	if strings.Contains(placeholder, "aW1hZ2UtcGl4ZWxz") || !strings.Contains(placeholder, "transientImageReference") {
		t.Fatalf("native screenshot placeholder leaked pixels: %s", placeholder)
	}
	message := schema.ToolMessage(placeholder, "browser-shot")
	message.ToolName = "browser_take_screenshot"
	expanded := state.ExpandTransientToolMessages([]*schema.Message{message})
	if len(expanded) != 2 || expanded[1].UserInputMultiContent[1].Image == nil {
		t.Fatalf("native screenshot was not bridged to vision input: %#v", expanded)
	}
	if got := expanded[1].UserInputMultiContent[1].Image.Base64Data; got == nil || *got != "aW1hZ2UtcGl4ZWxz" {
		t.Fatalf("vision bridge data = %v", got)
	}
	state.RecordToolMessage(chatMessage{Role: "tool", Name: "browser_take_screenshot", ToolCallID: "browser-shot", Content: raw})
	if strings.Contains(state.ModelMessages()[0].Content, "aW1hZ2UtcGl4ZWxz") {
		t.Fatal("durable native screenshot receipt retained pixels")
	}
}

func TestProjectAssistantBrowserSessionManagerReapsIdleEntries(t *testing.T) {
	manager := newProjectAssistantBrowserSessionManager()
	owner := browserSessionOwner{Identity: identity{tenant: "tenant", clusterID: "cluster", user: "alice"}, AssistantRunID: "run"}
	entry := manager.entry(owner, dataPlaneRef{Resource: "instances", Name: "browser"})
	if entry == nil {
		t.Fatal("manager did not create an entry")
	}
	manager.mu.Lock()
	if entry.idleTimer != nil {
		entry.idleTimer.Stop()
	}
	entry.lastUsed = time.Now().Add(-projectAssistantBrowserSessionIdleTimeout - time.Second)
	manager.mu.Unlock()
	manager.reapIdle(time.Now())
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.sessions) != 0 || len(manager.activeByRef) != 0 {
		t.Fatalf("idle manager state = sessions %d, active refs %d", len(manager.sessions), len(manager.activeByRef))
	}
}

func TestProjectAssistantBrowserSessionManagerScopesRefsAndCatalogsByWorkspace(t *testing.T) {
	manager := newProjectAssistantBrowserSessionManager()
	defer manager.closeAll()
	ref := dataPlaneRef{Resource: "instances", Name: "browser"}
	idA := identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", orgUUID: "org-a", workspaceUUID: "ws-a", user: "alice"}
	idB := identity{tenant: "root:railgrid:tenants:org-b:ws-b", clusterID: "cluster-b", orgUUID: "org-b", workspaceUUID: "ws-b", user: "bob"}
	ownerA := browserSessionOwner{Identity: idA, AssistantRunID: "run-a"}
	ownerB := browserSessionOwner{Identity: idB, AssistantRunID: "run-b"}

	entryA := manager.entry(ownerA, ref)
	entryB := manager.entry(ownerB, ref)
	if entryA == nil || entryB == nil || entryA == entryB {
		t.Fatalf("workspace entries = %p and %p, want distinct entries", entryA, entryB)
	}
	if got := len(manager.sessions); got != 2 {
		t.Fatalf("managed sessions = %d, want two isolated workspace sessions", got)
	}
	if got := len(manager.activeByRef); got != 2 {
		t.Fatalf("active browser scopes = %d, want two", got)
	}
	if !manager.hasActiveRef(idA, ref) || !manager.hasActiveRef(idB, ref) {
		t.Fatal("workspace-scoped active browser was not retained")
	}

	catalog := []projectMCPTool{{Name: browserMCPToolSnapshot, InputSchema: json.RawMessage(`{"type":"object"}`)}}
	manager.setBrowserCatalog(idA, ref, catalog)
	if _, ok := manager.browserCatalog(idA, ref); !ok {
		t.Fatal("workspace A catalog was not retained")
	}
	if _, ok := manager.browserCatalog(idB, ref); ok {
		t.Fatal("workspace A catalog leaked into workspace B")
	}

	idASecondCaller := idA
	idASecondCaller.user = "carol"
	entryASecondCaller := manager.entry(browserSessionOwner{Identity: idASecondCaller, AssistantRunID: "run-a-2"}, ref)
	if entryASecondCaller == nil || entryASecondCaller == entryA {
		t.Fatalf("same-endpoint handoff entry = %p, prior %p", entryASecondCaller, entryA)
	}
	if _, retained := manager.sessions[ownerA.key()]; retained {
		t.Fatal("same-endpoint owner handoff retained the prior workspace A owner")
	}
	if _, retained := manager.sessions[ownerB.key()]; !retained {
		t.Fatal("workspace A owner handoff evicted isolated workspace B")
	}
}

func TestProjectAssistantNativeBrowserReadRetriesLostSessionOnce(t *testing.T) {
	server := newNativeBrowserTestServer(t)
	var initializeCalls, toolCalls int
	server.sandboxDataPlaneClientFactory = nativeBrowserTestClient(func(request *http.Request) (*http.Response, error) {
		var envelope nativeBrowserTestEnvelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			return nil, err
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "initialize":
			initializeCalls++
			recorder.Header().Set("Mcp-Session-Id", string(rune('a'+initializeCalls)))
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": initializeCalls, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
		case "notifications/initialized":
			recorder.WriteHeader(http.StatusAccepted)
		case "tools/call":
			toolCalls++
			if toolCalls == 1 {
				recorder.WriteHeader(http.StatusNotFound)
				_, _ = recorder.WriteString("session not found")
				return recorder.Result(), nil
			}
			content := "- 0: https://demo.preview.example/"
			switch envelope.Params.Name {
			case browserMCPToolNavigate, browserMCPToolSnapshot:
				content = "- Page URL: https://demo.preview.example/\n- Page Snapshot:\n- generic [ref=e1]:"
			case "browser_tabs":
				content = "- 0: https://demo.preview.example/"
			}
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": toolCalls, "result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": content}}}})
		default:
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
		}
		return recorder.Result(), nil
	})
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-read",
	}
	result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_snapshot", projectAssistantToolRiskRead)
	if err != nil || result == "" {
		t.Fatalf("read result = %q, err = %v", result, err)
	}
	if initializeCalls != 2 || toolCalls != 4 {
		t.Fatalf("read retry calls = initialize %d, tools/call %d; want 2/4 (preview restore, snapshot, safety tabs)", initializeCalls, toolCalls)
	}
}

func TestProjectAssistantNativeBrowserReadRetriesAfterUnexpectedEventStreamEOF(t *testing.T) {
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	manager := newProjectAssistantBrowserSessionManager()
	server.browserSessions = manager
	defer manager.closeAll()
	configurePreviewInteractionBrowserTestServer(t, server, nil)
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	firstStream := &testBrowserEventStreamErrorBody{started: make(chan struct{})}
	var initializeCalls, toolCalls, deleteCalls int
	server.sandboxDataPlaneClientFactory = func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodGet {
				if initializeCalls == 1 {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       firstStream,
					}, nil
				}
				return browserMCPTestEventStreamResponse(request), nil
			}
			if request.Method == http.MethodDelete {
				deleteCalls++
				recorder := httptest.NewRecorder()
				recorder.WriteHeader(http.StatusNoContent)
				return recorder.Result(), nil
			}
			var envelope nativeBrowserTestEnvelope
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				return nil, err
			}
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Content-Type", "application/json")
			switch envelope.Method {
			case "initialize":
				initializeCalls++
				recorder.Header().Set("Mcp-Session-Id", fmt.Sprintf("eof-session-%d", initializeCalls))
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": initializeCalls, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			case "tools/call":
				toolCalls++
				content := "- 0: https://demo.preview.example/"
				switch envelope.Params.Name {
				case browserMCPToolNavigate, browserMCPToolSnapshot:
					content = "- Page URL: https://demo.preview.example/\n- Page Snapshot:\n- generic [ref=e1]:"
				}
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": toolCalls, "result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": content}}}})
			default:
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
			}
			return recorder.Result(), nil
		})}
	}
	first, err := server.newBrowserMCPSession(context.Background(), identity{clusterID: "cluster-a"}, dataPlaneRef{Resource: "instances", Name: "browser"})
	if err != nil {
		t.Fatalf("new initial browser MCP session: %v", err)
	}
	<-firstStream.started
	deadline := time.Now().Add(time.Second)
	for first.eventStreamFailure() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if first.eventStreamFailure() == nil {
		t.Fatal("unexpected event-stream EOF did not invalidate initial session")
	}
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-eof-read",
	}
	ref := dataPlaneRef{Resource: "instances", Name: "browser"}
	entry := manager.entry(server.nativeBrowserOwner(request), ref)
	entry.session = first
	result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolSnapshot, projectAssistantToolRiskRead)
	if err != nil || result == "" {
		t.Fatalf("EOF-invalidated read retry result = %q, err = %v", result, err)
	}
	if initializeCalls != 2 || toolCalls != 3 {
		t.Fatalf("EOF read retry calls = initialize %d, tools/call %d; want 2/3 (fresh preview navigation, snapshot, safety tabs)", initializeCalls, toolCalls)
	}
	if deleteCalls != 1 {
		t.Fatalf("EOF-invalidated session DELETE calls before cleanup = %d, want one", deleteCalls)
	}
}

func TestProjectAssistantNativeBrowserLostReadWithPendingInteractionIsUnverifiable(t *testing.T) {
	server := newNativeBrowserTestServer(t)
	var initializeCalls, toolCalls int
	server.sandboxDataPlaneClientFactory = nativeBrowserTestClient(func(request *http.Request) (*http.Response, error) {
		var envelope nativeBrowserTestEnvelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			return nil, err
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "initialize":
			initializeCalls++
			recorder.Header().Set("Mcp-Session-Id", "pending-session")
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": initializeCalls, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
		case "notifications/initialized":
			recorder.WriteHeader(http.StatusAccepted)
		case "tools/call":
			toolCalls++
			if toolCalls == 9 {
				recorder.WriteHeader(http.StatusNotFound)
				_, _ = recorder.WriteString("Session not found")
				return recorder.Result(), nil
			}
			content := "clicked"
			switch envelope.Params.Name {
			case browserMCPToolSnapshot:
				content = "- Page URL: https://demo.preview.example/\n- Page Snapshot:\n- generic [ref=e1]:"
			case "browser_tabs":
				content = "- 0: https://demo.preview.example/"
			}
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": toolCalls, "result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": content}}}})
		default:
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
		}
		return recorder.Result(), nil
	})
	state := newProjectEinoAssistantRunState()
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-pending-read",
		RunState:       state,
	}
	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolSnapshot, projectAssistantToolRiskRead); err != nil {
		t.Fatalf("initial preview snapshot: %v", err)
	}
	click, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_click", projectAssistantToolRiskRuntime)
	if err != nil {
		t.Fatalf("initial click returned error: %v", err)
	}
	state.RecordToolMessage(chatMessage{Role: "tool", Name: "browser_click", ToolCallID: "click", Content: click})
	if !state.NativeBrowserInteractionPending() {
		t.Fatal("successful interaction did not remain pending before a follow-up snapshot")
	}

	result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolSnapshot, projectAssistantToolRiskRead)
	if err != nil {
		t.Fatalf("pending read returned transport error: %v", err)
	}
	var outcome map[string]any
	if decodeErr := json.Unmarshal([]byte(result), &outcome); decodeErr != nil {
		t.Fatalf("unverifiable result = %q: %v", result, decodeErr)
	}
	if outcome["status"] != "unverifiable" || outcome["outcome"] != "unknown" || outcome["replayed"] != false || outcome["requiresSnapshot"] != true {
		t.Fatalf("pending read outcome = %#v", outcome)
	}
	state.RecordToolMessage(chatMessage{Role: "tool", Name: browserMCPToolSnapshot, ToolCallID: "lost-snapshot", Content: result})
	if initializeCalls != 1 || toolCalls != 9 {
		t.Fatalf("pending read calls = initialize %d, tools/call %d; want 1/9 without retry", initializeCalls, toolCalls)
	}
	if state.NativeBrowserInteractionPending() {
		t.Fatal("unverifiable session-loss receipt retained pending interaction")
	}

	// The next snapshot initializes a replacement MCP session and auto-navigates
	// it to the preview. That fresh document must not certify the click made in
	// the lost session.
	freshResult, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolSnapshot, projectAssistantToolRiskRead)
	if err != nil {
		t.Fatalf("fresh snapshot returned transport error: %v", err)
	}
	state.RecordToolMessage(chatMessage{Role: "tool", Name: browserMCPToolSnapshot, ToolCallID: "fresh-snapshot", Content: freshResult})
	if initializeCalls != 2 || toolCalls != 12 {
		t.Fatalf("fresh replacement snapshot calls = initialize %d, tools/call %d; want 2/12", initializeCalls, toolCalls)
	}
	if evidence := state.CompletionEvidence(); evidence.PreviewInteractionVerified || evidence.PreviewEvidenceOutcome == "interactions_verified" {
		t.Fatalf("fresh replacement-session snapshot verified lost interaction: %#v", evidence)
	}
}

func TestProjectAssistantNativeBrowserMutationDoesNotReplayLostSession(t *testing.T) {
	server := newNativeBrowserTestServer(t)
	toolCalls := map[string]int{}
	server.sandboxDataPlaneClientFactory = nativeBrowserTestClient(func(request *http.Request) (*http.Response, error) {
		var envelope nativeBrowserTestEnvelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			return nil, err
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "initialize":
			recorder.Header().Set("Mcp-Session-Id", "mutation-session")
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
		case "notifications/initialized":
			recorder.WriteHeader(http.StatusAccepted)
		case "tools/call":
			toolCalls[envelope.Params.Name]++
			switch envelope.Params.Name {
			case browserMCPToolNavigate:
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": "- Page URL: https://demo.preview.example/"}}}})
			case browserMCPToolSnapshot:
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": "- Page URL: https://demo.preview.example/\n- Page Snapshot:\n- generic [ref=e1]:"}}}})
			case "browser_tabs":
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": "- 0: https://demo.preview.example/"}}}})
			case "browser_click":
				recorder.WriteHeader(http.StatusGone)
				_, _ = recorder.WriteString("session expired")
			default:
				return nil, errors.New("unexpected browser tool " + envelope.Params.Name)
			}
		default:
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
		}
		return recorder.Result(), nil
	})
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-mutation",
	}
	navigateRequest := request
	navigateRequest.Arguments = map[string]any{"url": "/"}
	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), navigateRequest, browserMCPToolNavigate, projectAssistantToolRiskRead); err != nil {
		t.Fatalf("establish preview before mutation: %v", err)
	}
	result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_click", projectAssistantToolRiskRuntime)
	if err != nil {
		t.Fatalf("mutating browser session loss returned transport error: %v", err)
	}
	var outcome map[string]any
	if decodeErr := json.Unmarshal([]byte(result), &outcome); decodeErr != nil {
		t.Fatalf("outcome-unknown result = %q: %v", result, decodeErr)
	}
	if outcome["status"] != "outcome_unknown" || outcome["outcome"] != "unknown" || outcome["replayed"] != false {
		t.Fatalf("mutating browser session loss outcome = %#v", outcome)
	}
	if toolCalls["browser_click"] != 1 {
		t.Fatalf("browser_click calls = %d, want one non-replayed dispatched call; all calls=%v", toolCalls["browser_click"], toolCalls)
	}
}

type nativeBrowserOriginGuardTestState struct {
	pageURL                 string
	toolCalls               map[string]int
	errorAfterClick         bool
	errorOnNavigate         bool
	escapeAfterClick        bool
	escapeAfterNavigate     bool
	unsafeNavigationReceipt bool
	failNextSnapshotCall    bool
	sessionLossOnTool       string
}

func newNativeBrowserOriginGuardTestServer(t *testing.T, state *nativeBrowserOriginGuardTestState) (*Server, projectAssistantToolCallRequest) {
	t.Helper()
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		tenantProviders:  defaultTestProviders,
		hubBase:          "https://hub.example",
		callers:          newTestCallers(nil, ""),
	}
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	t.Cleanup(server.browserSessions.closeAll)
	configurePreviewInteractionBrowserTestServer(t, server, nil)
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	if state.toolCalls == nil {
		state.toolCalls = map[string]int{}
	}
	server.sandboxDataPlaneClientFactory = nativeBrowserTestClient(func(request *http.Request) (*http.Response, error) {
		var envelope nativeBrowserTestEnvelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			return nil, err
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "initialize":
			recorder.Header().Set("Mcp-Session-Id", "origin-guard-session")
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
		case "notifications/initialized":
			recorder.WriteHeader(http.StatusAccepted)
		case "tools/call":
			state.toolCalls[envelope.Params.Name]++
			if envelope.Params.Name == state.sessionLossOnTool {
				recorder.WriteHeader(http.StatusGone)
				_, _ = recorder.WriteString("session expired during " + envelope.Params.Name)
				return recorder.Result(), nil
			}
			content := "ok"
			isError := false
			switch envelope.Params.Name {
			case browserMCPToolNavigate:
				state.pageURL = projectToolString(envelope.Params.Arguments["url"])
				if state.unsafeNavigationReceipt {
					state.pageURL = "https://attacker.example/setup-navigation"
				}
				content = "- Page URL: " + state.pageURL
				if state.errorOnNavigate {
					content = "preview establishment failed"
					isError = true
				}
				if state.escapeAfterNavigate {
					state.pageURL = "https://attacker.example/after-navigate"
				}
			case browserMCPToolSnapshot:
				if state.failNextSnapshotCall {
					state.failNextSnapshotCall = false
					recorder.WriteHeader(http.StatusGone)
					_, _ = recorder.WriteString("session expired during safety snapshot")
					return recorder.Result(), nil
				}
				content = "- Page URL: " + state.pageURL + "\n- Page Snapshot:\n- generic [ref=e1]:"
			case "browser_tabs":
				content = "- 0: " + state.pageURL
			case "browser_click":
				if state.escapeAfterClick {
					state.pageURL = "https://attacker.example/after-click"
				}
				if state.errorAfterClick {
					content = "click timed out after dispatch"
					isError = true
				}
			case "browser_navigate_back", "browser_navigate_forward":
				content = "history action completed"
			}
			_ = json.NewEncoder(recorder).Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{"isError": isError, "content": []map[string]string{{"type": "text", "text": content}}},
			})
		default:
			_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
		}
		return recorder.Result(), nil
	})
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenant: "root:railgrid:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-origin-guard",
	}
	return server, request
}

func warmNativeBrowserOriginGuardTestSession(t *testing.T, server *Server, request projectAssistantToolCallRequest) {
	t.Helper()
	request.Arguments = map[string]any{"url": "https://demo.preview.example/"}
	if _, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolNavigate, projectAssistantToolRiskRead); err != nil {
		t.Fatalf("establish explicit preview navigation: %v", err)
	}
}

func decodeNativeBrowserOutcome(t *testing.T, result string) map[string]any {
	t.Helper()
	var outcome map[string]any
	if err := json.Unmarshal([]byte(result), &outcome); err != nil {
		t.Fatalf("decode browser outcome %q: %v", result, err)
	}
	return outcome
}

func TestProjectAssistantNativeBrowserPreflightBlocksOffOriginAndHistoryActions(t *testing.T) {
	t.Run("interaction is not dispatched", func(t *testing.T) {
		state := &nativeBrowserOriginGuardTestState{}
		server, request := newNativeBrowserOriginGuardTestServer(t, state)
		warmNativeBrowserOriginGuardTestSession(t, server, request)
		state.pageURL = "https://attacker.example/already-moved"

		result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_click", projectAssistantToolRiskRuntime)
		if err != nil {
			t.Fatalf("off-origin preflight call returned transport error: %v", err)
		}
		if outcome := decodeNativeBrowserOutcome(t, result); outcome["status"] != "not_executed" || outcome["replayed"] != false {
			t.Fatalf("off-origin preflight outcome = %#v", outcome)
		}
		if state.toolCalls["browser_click"] != 0 {
			t.Fatalf("browser_click dispatched %d times after off-origin preflight failure", state.toolCalls["browser_click"])
		}
		if state.toolCalls[browserMCPToolSnapshot] != 2 {
			t.Fatalf("trusted snapshots = %d, want explicit-navigation postcheck and blocked-action preflight", state.toolCalls[browserMCPToolSnapshot])
		}
	})

	for _, name := range []string{"browser_navigate_back", "browser_navigate_forward"} {
		t.Run(name+" is preflighted", func(t *testing.T) {
			state := &nativeBrowserOriginGuardTestState{}
			server, request := newNativeBrowserOriginGuardTestServer(t, state)
			warmNativeBrowserOriginGuardTestSession(t, server, request)
			state.pageURL = "https://attacker.example/already-moved"

			result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, name, projectAssistantToolRiskRead)
			if err != nil {
				t.Fatalf("off-origin history preflight returned transport error: %v", err)
			}
			if outcome := decodeNativeBrowserOutcome(t, result); outcome["status"] != "not_executed" {
				t.Fatalf("off-origin history outcome = %#v", outcome)
			}
			if state.toolCalls[name] != 0 {
				t.Fatalf("%s dispatched %d times after off-origin preflight failure", name, state.toolCalls[name])
			}
		})
	}
}

func TestProjectAssistantNativeBrowserPreflightSessionLossIsNotExecuted(t *testing.T) {
	state := &nativeBrowserOriginGuardTestState{}
	server, request := newNativeBrowserOriginGuardTestServer(t, state)
	warmNativeBrowserOriginGuardTestSession(t, server, request)
	state.failNextSnapshotCall = true

	result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_click", projectAssistantToolRiskRuntime)
	if err != nil {
		t.Fatalf("preflight session loss returned transport error: %v", err)
	}
	if outcome := decodeNativeBrowserOutcome(t, result); outcome["status"] != "not_executed" || outcome["replayed"] != false {
		t.Fatalf("preflight session-loss outcome = %#v", outcome)
	}
	if state.toolCalls["browser_click"] != 0 {
		t.Fatalf("browser_click dispatched %d times after preflight session loss", state.toolCalls["browser_click"])
	}
	manager := server.browserSessionManager()
	manager.mu.Lock()
	remainingSessions := len(manager.sessions)
	manager.mu.Unlock()
	if remainingSessions != 0 {
		t.Fatalf("preflight safety failure retained %d browser sessions, want it discarded", remainingSessions)
	}
}

func TestProjectAssistantNativeBrowserSetupNavigationFailureIsNotExecuted(t *testing.T) {
	t.Run("session loss before click", func(t *testing.T) {
		state := &nativeBrowserOriginGuardTestState{sessionLossOnTool: browserMCPToolNavigate}
		server, request := newNativeBrowserOriginGuardTestServer(t, state)

		result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_click", projectAssistantToolRiskRuntime)
		if err != nil {
			t.Fatalf("setup-navigation session loss returned transport error: %v", err)
		}
		if outcome := decodeNativeBrowserOutcome(t, result); outcome["status"] != "not_executed" || outcome["replayed"] != false {
			t.Fatalf("setup-navigation session-loss outcome = %#v", outcome)
		} else if message, _ := outcome["message"].(string); !strings.Contains(message, "requested browser action") || !strings.Contains(message, "may have changed the browser") {
			t.Fatalf("not-executed message does not distinguish the requested action from preview setup: %q", message)
		}
		if state.toolCalls["browser_click"] != 0 || state.toolCalls[browserMCPToolNavigate] != 1 {
			t.Fatalf("setup navigation/click calls = %d/%d, want one setup navigation and no click", state.toolCalls[browserMCPToolNavigate], state.toolCalls["browser_click"])
		}
	})

	t.Run("unsafe setup navigation receipt before click", func(t *testing.T) {
		state := &nativeBrowserOriginGuardTestState{unsafeNavigationReceipt: true}
		server, request := newNativeBrowserOriginGuardTestServer(t, state)

		result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_click", projectAssistantToolRiskRuntime)
		if err != nil {
			t.Fatalf("unsafe setup-navigation receipt returned transport error: %v", err)
		}
		if outcome := decodeNativeBrowserOutcome(t, result); outcome["status"] != "not_executed" || outcome["replayed"] != false {
			t.Fatalf("unsafe setup-navigation outcome = %#v", outcome)
		}
		if state.toolCalls["browser_click"] != 0 || state.toolCalls[browserMCPToolNavigate] != 1 {
			t.Fatalf("setup navigation/click calls = %d/%d, want one setup navigation and no click", state.toolCalls[browserMCPToolNavigate], state.toolCalls["browser_click"])
		}
	})

	t.Run("initialized session with previewReady false", func(t *testing.T) {
		state := &nativeBrowserOriginGuardTestState{}
		server, request := newNativeBrowserOriginGuardTestServer(t, state)
		warmNativeBrowserOriginGuardTestSession(t, server, request)
		ref, ok := server.resolveBrowserDataPlaneRef(context.Background(), request.Identity)
		if !ok {
			t.Fatal("resolve browser data-plane reference")
		}
		entry := server.browserSessionManager().entry(server.nativeBrowserOwner(request), ref)
		if entry == nil || entry.session == nil {
			t.Fatal("expected an initialized managed browser session")
		}
		entry.mu.Lock()
		entry.previewReady = false
		entry.mu.Unlock()
		state.sessionLossOnTool = browserMCPToolNavigate

		result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_click", projectAssistantToolRiskRuntime)
		if err != nil {
			t.Fatalf("preview restoration loss returned transport error: %v", err)
		}
		if outcome := decodeNativeBrowserOutcome(t, result); outcome["status"] != "not_executed" || outcome["replayed"] != false {
			t.Fatalf("preview-restoration outcome = %#v", outcome)
		}
		if state.toolCalls["browser_click"] != 0 || state.toolCalls[browserMCPToolNavigate] != 2 {
			t.Fatalf("setup navigation/click calls = %d/%d, want one original navigation, one restoration attempt and no click", state.toolCalls[browserMCPToolNavigate], state.toolCalls["browser_click"])
		}
	})

	t.Run("setup tool error before history action", func(t *testing.T) {
		state := &nativeBrowserOriginGuardTestState{errorOnNavigate: true}
		server, request := newNativeBrowserOriginGuardTestServer(t, state)

		result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_navigate_back", projectAssistantToolRiskRead)
		if err != nil {
			t.Fatalf("setup-navigation tool error returned transport error: %v", err)
		}
		if outcome := decodeNativeBrowserOutcome(t, result); outcome["status"] != "not_executed" || outcome["replayed"] != false {
			t.Fatalf("setup-navigation tool-error outcome = %#v", outcome)
		}
		if state.toolCalls["browser_navigate_back"] != 0 || state.toolCalls[browserMCPToolNavigate] != 1 {
			t.Fatalf("setup navigation/history calls = %d/%d, want one setup navigation and no history action", state.toolCalls[browserMCPToolNavigate], state.toolCalls["browser_navigate_back"])
		}
	})
}

func TestProjectAssistantNativeBrowserHistoryNavigationDoesNotRetryLostSession(t *testing.T) {
	for _, name := range []string{"browser_navigate_back", "browser_navigate_forward"} {
		t.Run(name, func(t *testing.T) {
			state := &nativeBrowserOriginGuardTestState{sessionLossOnTool: name}
			server, request := newNativeBrowserOriginGuardTestServer(t, state)
			warmNativeBrowserOriginGuardTestSession(t, server, request)

			result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, name, projectAssistantToolRiskRead)
			if err != nil {
				t.Fatalf("history action session loss returned transport error: %v", err)
			}
			if outcome := decodeNativeBrowserOutcome(t, result); outcome["status"] != "outcome_unknown" || outcome["replayed"] != false {
				t.Fatalf("history action session-loss outcome = %#v", outcome)
			}
			if state.toolCalls[name] != 1 {
				t.Fatalf("%s dispatched %d times after session loss, want one non-replayed attempt", name, state.toolCalls[name])
			}
		})
	}
}

func TestProjectAssistantNativeBrowserChecksErrorReceiptsAndDoesNotRetryUnsafeNavigation(t *testing.T) {
	t.Run("isError interaction is postchecked", func(t *testing.T) {
		state := &nativeBrowserOriginGuardTestState{errorAfterClick: true}
		server, request := newNativeBrowserOriginGuardTestServer(t, state)
		warmNativeBrowserOriginGuardTestSession(t, server, request)
		snapshotsBefore, tabsBefore := state.toolCalls[browserMCPToolSnapshot], state.toolCalls["browser_tabs"]

		result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_click", projectAssistantToolRiskRuntime)
		if err != nil {
			t.Fatalf("isError click returned transport error: %v", err)
		}
		if !projectAssistantNativeBrowserReceiptIsError(result) {
			t.Fatalf("safe isError receipt was not preserved: %s", result)
		}
		if state.toolCalls[browserMCPToolSnapshot] != snapshotsBefore+2 || state.toolCalls["browser_tabs"] != tabsBefore+2 {
			t.Fatalf("preflight and postcheck calls = snapshots %d (+%d), tabs %d (+%d); want one each for both checks", state.toolCalls[browserMCPToolSnapshot], state.toolCalls[browserMCPToolSnapshot]-snapshotsBefore, state.toolCalls["browser_tabs"], state.toolCalls["browser_tabs"]-tabsBefore)
		}
	})

	t.Run("isError after origin escape becomes unknown", func(t *testing.T) {
		state := &nativeBrowserOriginGuardTestState{errorAfterClick: true, escapeAfterClick: true}
		server, request := newNativeBrowserOriginGuardTestServer(t, state)
		warmNativeBrowserOriginGuardTestSession(t, server, request)

		result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, "browser_click", projectAssistantToolRiskRuntime)
		if err != nil {
			t.Fatalf("unsafe isError click returned transport error: %v", err)
		}
		if outcome := decodeNativeBrowserOutcome(t, result); outcome["status"] != "outcome_unknown" || outcome["replayed"] != false {
			t.Fatalf("unsafe isError click outcome = %#v", outcome)
		}
		if state.toolCalls["browser_click"] != 1 {
			t.Fatalf("browser_click dispatched %d times; want one attempt without replay", state.toolCalls["browser_click"])
		}
	})

	t.Run("explicit navigation starts from about blank and postcheck never retries", func(t *testing.T) {
		state := &nativeBrowserOriginGuardTestState{escapeAfterNavigate: true}
		server, request := newNativeBrowserOriginGuardTestServer(t, state)
		request.Arguments = map[string]any{"url": "https://demo.preview.example/"}

		result, err := server.callProjectAssistantNativeBrowserTool(context.Background(), request, browserMCPToolNavigate, projectAssistantToolRiskRead)
		if err != nil {
			t.Fatalf("validated explicit navigate returned transport error: %v", err)
		}
		if outcome := decodeNativeBrowserOutcome(t, result); outcome["status"] != "outcome_unknown" || outcome["replayed"] != false {
			t.Fatalf("unsafe explicit navigation outcome = %#v", outcome)
		}
		if state.toolCalls[browserMCPToolNavigate] != 1 || state.toolCalls[browserMCPToolSnapshot] != 1 {
			t.Fatalf("explicit navigate/snapshot calls = %d/%d, want one dispatch and one postcheck without preflight or retry", state.toolCalls[browserMCPToolNavigate], state.toolCalls[browserMCPToolSnapshot])
		}
	})
}

// Keep protocol plumbing in one place; each test still owns its failure injection
// and assertions. Tests with unusual GET/DELETE behavior use their own transport.
type nativeBrowserTestEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"params"`
}

func newNativeBrowserTestServer(t *testing.T) *Server {
	t.Helper()
	server := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, hubBase: "https://hub.example", callers: newTestCallers(nil, "")}
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	t.Cleanup(server.browserSessions.closeAll)
	configurePreviewInteractionBrowserTestServer(t, server, nil)
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	return server
}

func nativeBrowserTestClient(post func(*http.Request) (*http.Response, error)) func(time.Duration) *http.Client {
	return func(time.Duration) *http.Client {
		return &http.Client{Transport: sandboxRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.Method {
			case http.MethodGet:
				return browserMCPTestEventStreamResponse(request), nil
			case http.MethodDelete:
				recorder := httptest.NewRecorder()
				recorder.WriteHeader(http.StatusNoContent)
				return recorder.Result(), nil
			default:
				return post(request)
			}
		})}
	}
}
