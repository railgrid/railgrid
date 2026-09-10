/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

// codeBinaryHub fakes the tenant MCP aggregate: tools/list optionally
// advertises base64 on commit_files/checkout, and tools/call records the
// arguments it receives.
type codeBinaryHub struct {
	advertise bool
	calls     []map[string]any
	checkout  string
}

func (h *codeBinaryHub) serve(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode MCP request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		result := map[string]any{}
		switch req.Method {
		case "tools/list":
			commitItem := map[string]any{"path": map[string]any{}, "content": map[string]any{}}
			checkoutProps := map[string]any{"repositoryRef": map[string]any{}}
			if h.advertise {
				commitItem["encoding"] = map[string]any{"type": "string"}
				checkoutProps["binaryEncoding"] = map[string]any{"type": "string"}
			}
			result["tools"] = []any{
				map[string]any{"name": projectToolCodeCommitFiles, "inputSchema": map[string]any{"properties": map[string]any{"files": map[string]any{"items": map[string]any{"properties": commitItem}}}}},
				map[string]any{"name": projectToolCodeCheckoutRepository, "inputSchema": map[string]any{"properties": checkoutProps}},
			}
		case "tools/call":
			h.calls = append(h.calls, map[string]any{"name": req.Params.Name, "arguments": req.Params.Arguments})
			text := `{"name":"commit-1","phase":"Succeeded","commitSHA":"0123456789abcdef"}`
			if req.Params.Name == projectToolCodeCheckoutRepository {
				text = h.checkout
			}
			result["content"] = []any{map[string]any{"type": "text", "text": text}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(server.Close)
	return server
}

func commitBinaryFixture(t *testing.T, advertise bool) (*Server, *codeBinaryHub, *httptest.Server, workspace.Scope, []byte) {
	t.Helper()
	hub := &codeBinaryHub{advertise: advertise}
	upstream := hub.serve(t)
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	image := testPNG(1024)
	ctx := context.Background()
	if _, err := workspaces.PutFile(ctx, scope, workspace.PutOptions{Path: "public/logo.png", Data: image}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaces.PutFile(ctx, scope, workspace.PutOptions{Path: "src/app.ts", Data: []byte("export {}\n")}); err != nil {
		t.Fatal(err)
	}
	server := NewWithWorkspace(nil, nil, workspaces, upstream.URL, false)
	return server, hub, upstream, scope, image
}

func TestCommitProjectFilesSendsBase64WhenProviderAdvertisesEncoding(t *testing.T) {
	server, hub, upstream, scope, image := commitBinaryFixture(t, true)
	result, err := server.commitProjectWorkspaceFiles(context.Background(), identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1"}, scope, nil, "demo", upstream.URL,
		httptest.NewRequest(http.MethodPost, "/", nil), map[string]any{"repositoryRef": "demo", "paths": []any{"public/logo.png", "src/app.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(projectCommitSkippedBinaryPaths(result)) != 0 {
		t.Fatalf("supported provider result reports skipped binaries: %s", result)
	}
	files := hub.calls[len(hub.calls)-1]["arguments"].(map[string]any)["files"].([]any)
	var sawBinary bool
	for _, raw := range files {
		file := raw.(map[string]any)
		if file["path"] == "public/logo.png" {
			decoded, err := base64.StdEncoding.DecodeString(file["content"].(string))
			sawBinary = file["encoding"] == "base64" && err == nil && bytes.Equal(decoded, image)
		} else if _, ok := file["encoding"]; ok {
			t.Fatalf("text entry has encoding: %v", file)
		}
	}
	if !sawBinary {
		t.Fatalf("binary entry missing or malformed: %v", files)
	}
}

func TestCommitProjectFilesSkipsBinariesForOlderProvider(t *testing.T) {
	server, hub, upstream, scope, _ := commitBinaryFixture(t, false)
	id := identity{tenantPath: "root:org-a:ws-1", clusterID: "cluster-ws-1"}
	result, err := server.commitProjectWorkspaceFiles(context.Background(), id, scope, nil, "demo", upstream.URL,
		httptest.NewRequest(http.MethodPost, "/", nil), map[string]any{"repositoryRef": "demo", "paths": []any{"public/logo.png", "src/app.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	if skipped := projectCommitSkippedBinaryPaths(result); strings.Join(skipped, ",") != "public/logo.png" {
		t.Fatalf("skipped = %v in %s", skipped, result)
	}
	raw, _ := json.Marshal(hub.calls[len(hub.calls)-1]["arguments"])
	if strings.Contains(string(raw), "logo.png") || strings.Contains(string(raw), "base64") {
		t.Fatalf("older provider received a binary: %s", raw)
	}
	if _, err := server.commitProjectWorkspaceFiles(context.Background(), id, scope, nil, "demo", upstream.URL,
		httptest.NewRequest(http.MethodPost, "/", nil), map[string]any{"repositoryRef": "demo", "paths": []any{"public/logo.png"}}); err == nil || !strings.Contains(err.Error(), "only binary files changed") {
		t.Fatalf("binary-only commit error = %v", err)
	}
}

func TestCommitSettlementKeepsSkippedBinariesDirty(t *testing.T) {
	ctx := context.Background()
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	for _, file := range []workspace.PutOptions{{Path: "src/App.tsx", Data: []byte("app\n")}, {Path: "public/logo.png", Data: testPNG(64)}} {
		if _, err := workspaces.PutFile(ctx, scope, file); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{"public/logo.png", "src/App.tsx"}
	if _, err := workspaces.AddUncommittedPaths(ctx, scope, paths); err != nil {
		t.Fatal(err)
	}
	digest, err := workspaces.WorkspaceDigest(ctx, scope, paths)
	if err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.RecordSourceMutation()
	tool := projectEinoAssistantTool{req: projectAssistantRunRequest{Workspace: workspaces, WorkspaceScope: scope}, runState: runState}
	result := `{"commitSHA":"abc","skippedBinaryPaths":["public/logo.png"]}`
	if err := tool.recordV2CommitSettlement(ctx, projectAssistantToolSpec{Name: projectToolCommitProjectFiles, Risk: projectAssistantToolRiskCommit}, map[string]any{
		"paths":           []any{"public/logo.png", "src/App.tsx"},
		"workspaceDigest": digest,
	}, true, result); err != nil {
		t.Fatal(err)
	}
	dirty, err := workspaces.UncommittedPaths(ctx, scope)
	if err != nil || strings.Join(dirty, ",") != "public/logo.png" {
		t.Fatalf("dirty after settlement = %v, %v; want only the skipped binary", dirty, err)
	}
}

func TestHydrateRequestsAndWritesBase64Binaries(t *testing.T) {
	image := testPNG(2048)
	checkout, _ := json.Marshal(checkoutToolResult{Ref: "main", CommitSHA: "sha", Files: []checkoutToolFile{
		{Path: "public/logo.png", Content: base64.StdEncoding.EncodeToString(image), Encoding: "base64"},
		{Path: "index.html", Content: "<html></html>\n"},
	}})
	hub := &codeBinaryHub{advertise: true, checkout: string(checkout)}
	upstream := hub.serve(t)
	f := newProjectFilesFixture(t)
	f.server.hubBase = upstream.URL
	f.server.developmentSyncAfterMutation = func(identity, *aiv1alpha1.Project, string) error { return nil }
	resp, err := f.server.hydrateWorkspaceFromRepository(context.Background(), identity{orgUUID: "org-a", workspaceUUID: "workspace-a", clusterID: "cluster-a"}, f.project, httptest.NewRequest(http.MethodPost, "/", nil), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Written) != 2 || len(resp.Skipped) != 0 {
		t.Fatalf("hydrate = %#v", resp)
	}
	args := hub.calls[len(hub.calls)-1]["arguments"].(map[string]any)
	if args["binaryEncoding"] != "base64" {
		t.Fatalf("checkout args = %v, want binaryEncoding opt-in", args)
	}
	got, err := f.workspaces.ReadFileBytes(context.Background(), f.scope, "public/logo.png", 0)
	if err != nil || !bytes.Equal(got, image) {
		t.Fatalf("hydrated binary mismatch: %v", err)
	}
}
