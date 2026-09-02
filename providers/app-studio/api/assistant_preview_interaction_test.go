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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestBrowserMCPParseNodesCapturesRefs(t *testing.T) {
	nodes := browserMCPParseAccessibilityNodes(browserMCPExtractSnapshotTree(browserMCPSampleSnapshot))
	byName := map[string]browserMCPNode{}
	for _, n := range nodes {
		byName[n.name] = n
	}
	if got := byName["Sign in"]; got.role != "button" || got.ref != "e5" {
		t.Fatalf("button node = %+v, want role=button ref=e5", got)
	}
	if got := byName["Email"]; got.role != "textbox" || got.ref != "e3" {
		t.Fatalf("textbox node = %+v, want role=textbox ref=e3", got)
	}
}

func TestFindInteractionTarget(t *testing.T) {
	tree := browserMCPExtractSnapshotTree(browserMCPSampleSnapshot)

	node, ok := findInteractionTarget(tree, projectAssistantPreviewInteractionStep{Action: "click", Role: "button", Name: "Sign in"})
	if !ok || node.ref != "e5" {
		t.Fatalf("click target = %+v ok=%v, want ref=e5", node, ok)
	}
	// Role-only match returns the first of that role.
	node, ok = findInteractionTarget(tree, projectAssistantPreviewInteractionStep{Action: "type", Role: "textbox"})
	if !ok || node.ref != "e3" {
		t.Fatalf("textbox target = %+v ok=%v, want first textbox ref=e3", node, ok)
	}
	// Name-only (substring) match across roles.
	node, ok = findInteractionTarget(tree, projectAssistantPreviewInteractionStep{Action: "click", Name: "Back to"})
	if !ok || node.ref != "e6" {
		t.Fatalf("name-only target = %+v ok=%v, want ref=e6", node, ok)
	}
	// No match.
	if _, ok := findInteractionTarget(tree, projectAssistantPreviewInteractionStep{Action: "click", Role: "checkbox"}); ok {
		t.Fatal("checkbox unexpectedly matched")
	}
}

func TestProjectAssistantPreviewInteractionStepsValidation(t *testing.T) {
	// Valid script parses.
	steps, err := projectAssistantPreviewInteractionSteps([]any{
		map[string]any{"action": "type", "role": "textbox", "name": "Email", "value": "a@b.c"},
		map[string]any{"action": "click", "role": "button", "name": "Sign in"},
		map[string]any{"action": "press", "key": "Enter"},
	})
	if err != nil {
		t.Fatalf("valid steps rejected: %v", err)
	}
	if len(steps) != 3 || steps[0].Action != "type" || steps[2].Key != "Enter" {
		t.Fatalf("parsed steps = %+v", steps)
	}

	bad := []struct {
		name  string
		value any
	}{
		{"empty", []any{}},
		{"nil", nil},
		{"press without key", []any{map[string]any{"action": "press"}}},
		{"click without target", []any{map[string]any{"action": "click"}}},
		{"select without values", []any{map[string]any{"action": "select", "role": "combobox", "name": "Country"}}},
		{"unknown action", []any{map[string]any{"action": "scroll"}}},
		{"unknown field", []any{map[string]any{"action": "click", "role": "button", "bogus": 1}}},
	}
	for _, tc := range bad {
		if _, err := projectAssistantPreviewInteractionSteps(tc.value); err == nil {
			t.Fatalf("%s: expected validation error", tc.name)
		}
	}
}

func TestProjectAssistantPreviewInteractionStepsCap(t *testing.T) {
	many := make([]any, projectAssistantPreviewInteractionMaxSteps+1)
	for i := range many {
		many[i] = map[string]any{"action": "press", "key": "Tab"}
	}
	_, err := projectAssistantPreviewInteractionSteps(many)
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("step cap not enforced: %v", err)
	}
}

func TestInteractProjectDevelopmentPreviewCheckpointsDirtySandboxBeforeBrowser(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "main.go", Content: "old\n"}}); err != nil {
		t.Fatal(err)
	}
	current, err := files.ReadFile(ctx, scope, workspace.ReadOptions{Path: "main.go", MaxBytes: workspace.MaxReadMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := files.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "old\n"}})
	newDigest := projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "new\n"}})
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	state.RecordSourceMutation()
	fakeSandbox := &sandboxClientFake{response: projectAssistantSandboxWorkspaceResponse{
		SourceRevision: revision + 1,
		SourceDigest:   newDigest,
		Changes: []projectAssistantSandboxWorkspaceChange{{
			Path: "main.go", Operation: string(workspace.ManagedFileReplace), Content: "new\n", ExpectedVersion: current.Version,
		}},
	}}
	project := &aiv1alpha1.Project{}
	project.Name = "shop"
	project.UID = "project-uid"
	project.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}
	id := identity{clusterID: "cluster", token: "token", orgUUID: "org", workspaceUUID: "ws"}
	server := NewWithWorkspace(nil, store.NewMemoryStore(), files, "http://sandbox.test", false)
	var syncCalls int
	server.developmentSyncAfterMutation = func(_ identity, _ *aiv1alpha1.Project, name string) error {
		if name != projectActionWorkspaceSync {
			t.Fatalf("sync action = %q, want %q", name, projectActionWorkspaceSync)
		}
		syncCalls++
		return nil
	}
	sandbox := &projectAssistantRunSandbox{
		server: server, client: fakeSandbox, id: id, project: project, scope: scope, runState: state,
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: revision, SourceDigest: oldDigest,
			RemoteRevision: revision + 1, RemoteDigest: newDigest, RemoteCheckpointID: "baseline",
		},
	}
	state.SetSandbox(sandbox)
	var browserCalls int
	var browserSyncStatus string
	configurePreviewInteractionBrowserTestServer(t, server, func(method, _ string) {
		browserCalls++
		if browserCalls == 1 {
			browserSyncStatus, _ = state.DevelopmentSyncEvidence(1)
		}
		if method == "initialize" && browserSyncStatus != "succeeded" {
			t.Errorf("browser initialized before sync evidence succeeded: %q", browserSyncStatus)
		}
	})
	server.previewInspectionResolveURL = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "https://demo.preview.example/", nil
	}
	result, err := server.interactProjectDevelopmentPreviewResult(ctx, projectAssistantToolCallRequest{
		Identity: id, Project: project, WorkspaceScope: scope, RunState: state,
		Arguments: map[string]any{
			"steps": []any{map[string]any{"action": "press", "key": "Enter"}},
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || len(result.Steps) != 1 || !result.Steps[0].Applied {
		t.Fatalf("interaction result = %#v, want successful browser interaction", result)
	}
	if fakeSandbox.workspaceCalls != 2 {
		t.Fatalf("checkpoint worker calls = %d, want diff plus baseline create", fakeSandbox.workspaceCalls)
	}
	if syncCalls != 1 || browserCalls == 0 || browserSyncStatus != "succeeded" {
		t.Fatalf("interaction ordering = sync calls %d, browser calls %d, browser sync status %q", syncCalls, browserCalls, browserSyncStatus)
	}
}

func TestInteractProjectDevelopmentPreviewFailsClosedOnCheckpointConflict(t *testing.T) {
	ctx := context.Background()
	files := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "shop", ProjectUID: "uid"}
	if err := files.ApplyFiles(ctx, scope, []workspace.File{{Path: "main.go", Content: "old\n"}}); err != nil {
		t.Fatal(err)
	}
	revision, err := files.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.CreateFile(ctx, scope, workspace.CreateOptions{Path: "drift.txt", Content: "drift"}); err != nil {
		t.Fatal(err)
	}
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	state.RecordSourceMutation()
	project := &aiv1alpha1.Project{}
	project.Name = "shop"
	project.UID = "project-uid"
	id := identity{clusterID: "cluster", token: "token", orgUUID: "org", workspaceUUID: "ws"}
	server := NewWithWorkspace(nil, store.NewMemoryStore(), files, "http://sandbox.test", false)
	fakeSandbox := &sandboxClientFake{response: projectAssistantSandboxWorkspaceResponse{SourceRevision: revision + 1, SourceDigest: "new"}}
	sandbox := &projectAssistantRunSandbox{
		server: server, client: fakeSandbox, id: id, project: project, scope: scope, runState: state,
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: revision, SourceDigest: projectSandboxSyncDigest([]projectSandboxSyncFile{{Path: "main.go", Content: "old\n"}}),
			RemoteRevision: revision + 1, RemoteDigest: "new", RemoteCheckpointID: "baseline",
		},
	}
	state.SetSandbox(sandbox)
	browserCalls := 0
	configurePreviewInteractionBrowserTestServer(t, server, func(string, string) { browserCalls++ })
	result, err := server.interactProjectDevelopmentPreviewResult(ctx, projectAssistantToolCallRequest{
		Identity: id, Project: project, WorkspaceScope: scope, RunState: state,
		Arguments: map[string]any{
			"steps": []any{map[string]any{"action": "press", "key": "Enter"}},
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.FailureKind != "not_current" || !strings.Contains(result.Summary, "not current") {
		t.Fatalf("conflicting interaction result = %#v, want truthful not-current failure", result)
	}
	if browserCalls != 0 || fakeSandbox.workspaceCalls != 0 {
		t.Fatalf("checkpoint conflict reached browser: browser calls=%d worker calls=%d", browserCalls, fakeSandbox.workspaceCalls)
	}
}

func configurePreviewInteractionBrowserTestServer(t *testing.T, server *Server, observe func(method, tool string)) {
	t.Helper()
	studio := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Studio",
		"metadata":   map[string]any{"name": aiv1alpha1.StudioName},
		"status": map[string]any{
			"browser": map[string]any{
				"phase":    aiv1alpha1.StudioServiceReady,
				"resource": "instances",
				"instance": "browser",
			},
		},
	}}
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(), studio)
	server.projectClientFor = func(identity) (*asclient.Client, error) {
		return asclient.NewFromDynamic(dynamicClient), nil
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
			if observe != nil {
				observe(envelope.Method, envelope.Params.Name)
			}
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Content-Type", "application/json")
			switch envelope.Method {
			case "initialize":
				recorder.Header().Set("Mcp-Session-Id", "preview-session")
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": browserMCPProtocolVersion}})
			case "notifications/initialized":
				recorder.WriteHeader(http.StatusAccepted)
			case "tools/call":
				content := "ok"
				switch envelope.Params.Name {
				case browserMCPToolSnapshot:
					content = "- Page URL: https://demo.preview.example/\n- Page Title: Demo\n- button \"Save\" [ref=e1]\n"
				case "browser_tabs":
					content = "- 0: https://demo.preview.example/\n"
				}
				_ = json.NewEncoder(recorder).Encode(map[string]any{
					"jsonrpc": "2.0", "id": 1,
					"result": map[string]any{"content": []map[string]string{{"type": "text", "text": content}}},
				})
			default:
				_ = json.NewEncoder(recorder).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
			}
			return recorder.Result(), nil
		})}
	}
}
