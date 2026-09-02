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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

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

func (p fakeNativeBrowserToolPort) DiscoverMCP(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, bool, error) {
	return nil, false, nil
}

func (p fakeNativeBrowserToolPort) Invoke(context.Context, projectAssistantTool, projectAssistantToolCallRequest) (string, error) {
	return "{}", nil
}

func (p fakeNativeBrowserToolPort) DiscoverBrowser(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, error) {
	return p.tools, p.browserErr
}

func (p *countingNativeBrowserToolPort) DiscoverMCP(context.Context, identity, projectLLMSettings) ([]projectAssistantTool, bool, error) {
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
	server := &Server{}
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

func TestProjectAssistantNativeBrowserToolsJoinEinoDiscovery(t *testing.T) {
	browser := projectAssistantToolFunc{spec: projectAssistantToolSpec{
		Name:        "browser_snapshot",
		Description: "Take an accessibility snapshot",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Risk:        projectAssistantToolRiskRead,
	}}
	server := &Server{}
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
		Identity:       identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        project,
		AssistantRunID: "run-a",
	}
	owner := (&Server{}).nativeBrowserOwner(base)
	otherUser := base
	otherUser.Identity.user = "bob"
	otherProject := base
	otherProject.Project = &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "project-b", UID: types.UID("uid-b")}}
	if owner.key() == (&Server{}).nativeBrowserOwner(otherUser).key() {
		t.Fatal("different caller inherited the same browser session key")
	}
	if owner.key() == (&Server{}).nativeBrowserOwner(otherProject).key() {
		t.Fatal("different project inherited the same browser session key")
	}
}

func TestProjectAssistantNativeBrowserCallReusesSessionPerRun(t *testing.T) {
	server := &Server{hubBase: "https://hub.example"}
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	configurePreviewInteractionBrowserTestServer(t, server, nil)
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}}
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
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
	server := &Server{hubBase: "https://hub.example"}
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
		Identity:       identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
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
			"cluster":      {"cluster-a"},
			"redirect_uri": {preview.URL + privateAppCallbackPath},
		}
		http.Redirect(w, r, hub.URL+privateAppAuthorizePath+"?"+query.Encode(), http.StatusFound)
	}))
	defer preview.Close()

	server := &Server{
		hubBase:                      hub.URL,
		hubPublicURL:                 hub.URL,
		previewInsecureSkipTLSVerify: true,
	}
	manager := newProjectAssistantBrowserSessionManager()
	server.browserSessions = manager
	defer manager.closeAll()
	previewURL := strings.TrimRight(preview.URL, "/") + "/"
	var toolCalls []string
	var initializeCalls int
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
			if request.Method == http.MethodPost && request.URL.Path == browserSessionHandoffPath {
				recorder := httptest.NewRecorder()
				recorder.Header().Set("Content-Type", "application/json")
				_, _ = recorder.WriteString(`{"path":"/auth/session/handoff?code=one-use"}`)
				return recorder.Result(), nil
			}
			var envelope struct {
				Method string `json:"method"`
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
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
		})}
	}
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
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

func TestProjectAssistantNativeBrowserFirstNavigationIsNotDuplicated(t *testing.T) {
	server := &Server{hubBase: "https://hub.example"}
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
		Identity:       identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
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
	server := &Server{previewInspector: &fakeProjectAssistantPreviewInspector{}}
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
	server := &Server{hubBase: "https://hub.example", previewInspector: &fakeProjectAssistantPreviewInspector{}}
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
		Identity:   identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
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
	server := &Server{hubBase: "https://hub.example"}
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	configurePreviewInteractionBrowserTestServer(t, server, nil)
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
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
			var envelope struct {
				Method string `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
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
		Identity:       identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
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
	server := &Server{hubBase: "https://hub.example"}
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	configurePreviewInteractionBrowserTestServer(t, server, nil)
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
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
			var envelope struct {
				Method string `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
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
	identity := identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"}
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
			server := &Server{hubBase: "https://hub.example"}
			server.browserSessions = newProjectAssistantBrowserSessionManager()
			configurePreviewInteractionBrowserTestServer(t, server, nil)
			server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
				return "https://demo.preview.example/", nil
			}
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
						Params struct {
							Name string `json:"name"`
						} `json:"params"`
					}
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
						switch envelope.Params.Name {
						case browserMCPToolSnapshot:
							content = tc.snapshot
						case "browser_tabs":
							content = tc.tabs
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
				Identity:       identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
				Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
				AssistantRunID: "run-safety",
			}
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
	if got := expanded[1].UserInputMultiContent[1].Image.MessagePartCommon.Base64Data; got == nil || *got != "aW1hZ2UtcGl4ZWxz" {
		t.Fatalf("vision bridge data = %v", got)
	}
	state.RecordToolMessage(chatMessage{Role: "tool", Name: "browser_take_screenshot", ToolCallID: "browser-shot", Content: raw})
	if strings.Contains(state.ModelMessages()[0].Content, "aW1hZ2UtcGl4ZWxz") {
		t.Fatal("durable native screenshot receipt retained pixels")
	}
}

func TestProjectAssistantBrowserSessionManagerReapsIdleEntries(t *testing.T) {
	manager := newProjectAssistantBrowserSessionManager()
	owner := browserSessionOwner{Identity: identity{tenantPath: "tenant", clusterID: "cluster", user: "alice"}, AssistantRunID: "run"}
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
	idA := identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", orgUUID: "org-a", workspaceUUID: "ws-a", user: "alice"}
	idB := identity{tenantPath: "root:faros:tenants:org-b:ws-b", clusterID: "cluster-b", orgUUID: "org-b", workspaceUUID: "ws-b", user: "bob"}
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
	server := &Server{hubBase: "https://hub.example"}
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	configurePreviewInteractionBrowserTestServer(t, server, nil)
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	var initializeCalls, toolCalls int
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
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
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
		})}
	}
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
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
	server := &Server{hubBase: "https://hub.example"}
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
			var envelope struct {
				Method string `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
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
		Identity:       identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
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
	server := &Server{hubBase: "https://hub.example"}
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	configurePreviewInteractionBrowserTestServer(t, server, nil)
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	var initializeCalls, toolCalls int
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
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
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
				if toolCalls == 5 {
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
		})}
	}
	state := newProjectEinoAssistantRunState()
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-pending-read",
		RunState:       state,
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
	if initializeCalls != 1 || toolCalls != 5 {
		t.Fatalf("pending read calls = initialize %d, tools/call %d; want 1/5 without retry", initializeCalls, toolCalls)
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
	if initializeCalls != 2 || toolCalls != 8 {
		t.Fatalf("fresh replacement snapshot calls = initialize %d, tools/call %d; want 2/8", initializeCalls, toolCalls)
	}
	if evidence := state.CompletionEvidence(); evidence.PreviewInteractionVerified || evidence.PreviewEvidenceOutcome == "interactions_verified" {
		t.Fatalf("fresh replacement-session snapshot verified lost interaction: %#v", evidence)
	}
}

func TestProjectAssistantNativeBrowserMutationDoesNotReplayLostSession(t *testing.T) {
	server := &Server{hubBase: "https://hub.example"}
	server.browserSessions = newProjectAssistantBrowserSessionManager()
	configurePreviewInteractionBrowserTestServer(t, server, nil)
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	var toolCalls int
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
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
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
				toolCalls++
				if envelope.Params.Name == browserMCPToolNavigate {
					_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": "- Page URL: https://demo.preview.example/"}}}})
					return recorder.Result(), nil
				}
				recorder.WriteHeader(http.StatusGone)
				_, _ = recorder.WriteString("session expired")
			default:
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
			}
			return recorder.Result(), nil
		})}
	}
	request := projectAssistantToolCallRequest{
		Identity:       identity{tenantPath: "root:faros:tenants:org-a:ws-a", clusterID: "cluster-a", user: "alice"},
		Project:        &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")}},
		AssistantRunID: "run-mutation",
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
	if toolCalls != 2 {
		t.Fatalf("mutating calls = %d, want preview navigation plus one non-replayed call", toolCalls)
	}
}
