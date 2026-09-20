/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/internal/codecommit"
	"github.com/railgrid/provider-app-studio/workspace"
)

// The assistant's commit tool is the Code provider's repositories/commit/v1
// action now, not the code__commit_files MCP tool, so the fake it is tested
// against is a hub serving an ACTION route rather than a JSON-RPC aggregate.

const (
	testCommitRepositoryRef = "demo-repo"
	testCommitRepositoryUID = "repository-uid-1"
	testCommitProvider      = "code"
)

// codeCommitActionCall is one decoded action invocation.
type codeCommitActionCall struct {
	Verb  string
	Input struct {
		RepositoryUID string            `json:"repositoryUID"`
		Message       string            `json:"message"`
		Branch        string            `json:"branch"`
		Files         []codecommit.File `json:"files"`
		BundleRef     string            `json:"bundleRef"`
		BundleDigest  string            `json:"bundleDigest"`
	}
}

// codeCommitHub fakes the hub in front of the Code provider's action routes.
type codeCommitHub struct {
	mu sync.Mutex
	// errorCode, when set, makes every commit answer the actionwire error
	// envelope with that typed code, the way a refused or failed action does.
	errorCode string
	calls     []codeCommitActionCall
}

func (h *codeCommitHub) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(segments) < 2 {
			t.Fatalf("unexpected action path %q", r.URL.Path)
		}
		call := codeCommitActionCall{Verb: segments[len(segments)-2]}
		var body struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode action body: %v", err)
		}
		if err := json.Unmarshal(body.Input, &call.Input); err != nil {
			t.Fatalf("decode action input: %v", err)
		}
		h.mu.Lock()
		h.calls = append(h.calls, call)
		code := h.errorCode
		h.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if code != "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code}})
			return
		}
		switch call.Verb {
		case codecommit.StageBundleAction:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
				"bundleRef": "bundle-1", "bundleDigest": "digest-1",
				"fileCount": len(call.Input.Files), "size": 1,
			}})
		case codecommit.Action:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
				"commit": map[string]any{"name": "commit-1", "uid": "commit-uid-1"},
			}})
		default:
			t.Fatalf("unexpected action verb %q", call.Verb)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func (h *codeCommitHub) commits() []codeCommitActionCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]codeCommitActionCall, 0, len(h.calls))
	for _, call := range h.calls {
		if call.Verb == codecommit.Action {
			out = append(out, call)
		}
	}
	return out
}

// testCodeRepository is the Repository the commit is pinned to: the tool reads
// it as the caller to learn the UID the action refuses to commit without.
func testCodeRepository(name, uid string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "code.railgrid.ai/v1alpha1",
		"kind":       "Repository",
		"metadata":   map[string]any{"name": name, "uid": uid},
		"spec":       map[string]any{"name": name},
	}}
	return object
}

// newCommitActionServer wires a Server whose commit tool reaches hub: the Code
// provider is resolved from the workspace binding (faked), the Repository is
// readable as the caller, and the action route answers on hub.
func newCommitActionServer(t *testing.T, workspaces *workspace.FileStore, hub string, objects ...runtime.Object) *Server {
	t.Helper()
	if len(objects) == 0 {
		objects = []runtime.Object{testCodeRepository(testCommitRepositoryRef, testCommitRepositoryUID)}
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[k8sschema.GroupVersionResource]string{codeRepositoriesGVR: "RepositoryList"},
		objects...,
	)
	server := NewWithWorkspace(nil, nil, workspaces, hub, false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	server.tenantProviders = testProviders(testCommitProvider)
	server.projectClientFor = func(identity) (*asclient.Client, error) { return asclient.NewFromDynamic(dyn), nil }
	return server
}

func testCommitIdentity() identity {
	return identity{tenant: "root:org-a:ws-1", clusterID: "cluster-ws-1", orgUUID: "org-a", workspaceUUID: "ws-1", token: "caller-token"}
}

// TestCommitToolCallsTheActionAsTheCaller is the shape of Cut D.1 on the
// assistant side: the human's bearer, the action route, the Repository pinned
// by UID, and a RepositoryCommit name to follow rather than a landed SHA.
func TestCommitToolCallsTheActionAsTheCaller(t *testing.T) {
	ctx := context.Background()
	hub := &codeCommitHub{}
	upstream := hub.serve(t)
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{Path: "index.html", Content: "hello\n"}})
	server := newCommitActionServer(t, workspaces, upstream.URL)

	result, err := server.commitProjectWorkspaceFiles(ctx, testCommitIdentity(), scope, nil, testCommitRepositoryRef,
		map[string]any{"repositoryRef": testCommitRepositoryRef, "paths": []any{"index.html"}, "message": "Initial app"})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	commits := hub.commits()
	if len(commits) != 1 {
		t.Fatalf("commit action calls = %d, want 1", len(commits))
	}
	if commits[0].Input.RepositoryUID != testCommitRepositoryUID {
		t.Fatalf("repositoryUID = %q, want the Repository's own UID", commits[0].Input.RepositoryUID)
	}
	if len(commits[0].Input.Files) != 1 || commits[0].Input.Files[0].Path != "index.html" || commits[0].Input.Files[0].Content != "hello\n" {
		t.Fatalf("files = %#v", commits[0].Input.Files)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("decode tool result %q: %v", result, err)
	}
	if decoded["name"] != "commit-1" {
		t.Fatalf("tool result names %v, want the RepositoryCommit to follow", decoded["name"])
	}
	// Nothing landed, so nothing is settled: the path stays dirty and the
	// commit is recorded as pending for the reconciler's watch to converge.
	pending, ok, err := workspaces.PendingCommit(ctx, scope)
	if err != nil || !ok {
		t.Fatalf("pending commit = %v, %v, %v", pending, ok, err)
	}
	if pending.Name != "commit-1" || pending.RepositoryRef != testCommitRepositoryRef {
		t.Fatalf("pending commit = %#v", pending)
	}
}

// TestCommitToolSendsDeletionsInTheFileList proves the action's single file
// list carries deletions, which is what retired the tool's deletePaths member.
func TestCommitToolSendsDeletionsInTheFileList(t *testing.T) {
	ctx := context.Background()
	hub := &codeCommitHub{}
	upstream := hub.serve(t)
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{
		{Path: "src/old.ts", Content: "old\n"},
		{Path: "src/new.ts", Content: "new\n"},
	})
	readOld, err := workspaces.ReadFile(ctx, scope, workspace.ReadOptions{Path: "src/old.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspaces.DeleteFile(ctx, scope, workspace.DeleteOptions{Path: "src/old.ts", ExpectedVersion: readOld.Version}); err != nil {
		t.Fatal(err)
	}
	server := newCommitActionServer(t, workspaces, upstream.URL)
	if _, err := server.commitProjectWorkspaceFiles(ctx, testCommitIdentity(), scope, nil, testCommitRepositoryRef,
		map[string]any{"repositoryRef": testCommitRepositoryRef, "paths": []any{"src/old.ts", "src/new.ts"}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	commits := hub.commits()
	if len(commits) != 1 {
		t.Fatalf("commit action calls = %d, want 1", len(commits))
	}
	var sawDelete, sawWrite bool
	for _, file := range commits[0].Input.Files {
		switch file.Path {
		case "src/old.ts":
			sawDelete = file.Delete && file.Content == ""
		case "src/new.ts":
			sawWrite = !file.Delete && file.Content == "new\n"
		}
	}
	if !sawDelete || !sawWrite {
		t.Fatalf("files = %#v, want one write and one deletion", commits[0].Input.Files)
	}
}

// TestCommitToolReportsTheProviderError keeps the typed actionwire code as the
// tool's failure, rather than a prose message parsed out of a tool result.
func TestCommitToolReportsTheProviderError(t *testing.T) {
	ctx := context.Background()
	hub := &codeCommitHub{errorCode: "bundle_unavailable"}
	upstream := hub.serve(t)
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{Path: "index.html", Content: "hello\n"}})
	server := newCommitActionServer(t, workspaces, upstream.URL)
	_, err := server.commitProjectWorkspaceFiles(ctx, testCommitIdentity(), scope, nil, testCommitRepositoryRef,
		map[string]any{"repositoryRef": testCommitRepositoryRef, "paths": []any{"index.html"}})
	if err == nil || !strings.Contains(err.Error(), "bundle_unavailable") {
		t.Fatalf("commit error = %v, want the typed action error", err)
	}
	if _, ok, _ := workspaces.PendingCommit(ctx, scope); ok {
		t.Fatal("a refused commit was recorded as pending")
	}
}

// TestCommitToolSendsBinariesBase64: the action's schema declares the
// encoding, so binaries are always accepted and never probed for.
func TestCommitToolSendsBinariesBase64(t *testing.T) {
	ctx := context.Background()
	hub := &codeCommitHub{}
	upstream := hub.serve(t)
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	image := testPNG(1024)
	if _, err := workspaces.PutFile(ctx, scope, workspace.PutOptions{Path: "public/logo.png", Data: image}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaces.PutFile(ctx, scope, workspace.PutOptions{Path: "src/app.ts", Data: []byte("export {}\n")}); err != nil {
		t.Fatal(err)
	}
	server := newCommitActionServer(t, workspaces, upstream.URL)
	if _, err := server.commitProjectWorkspaceFiles(ctx, testCommitIdentity(), scope, nil, testCommitRepositoryRef,
		map[string]any{"repositoryRef": testCommitRepositoryRef, "paths": []any{"public/logo.png", "src/app.ts"}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	commits := hub.commits()
	if len(commits) != 1 {
		t.Fatalf("commit action calls = %d, want 1", len(commits))
	}
	var sawBinary bool
	for _, file := range commits[0].Input.Files {
		switch file.Path {
		case "public/logo.png":
			sawBinary = file.Encoding == "base64"
		case "src/app.ts":
			if file.Encoding == "base64" {
				t.Fatalf("text entry sent base64: %#v", file)
			}
		}
	}
	if !sawBinary {
		t.Fatalf("binary entry missing or not base64: %#v", commits[0].Input.Files)
	}
}

// TestCommitToolRejectsRepositoryMismatch is a local refusal: the model cannot
// aim the commit at a repository the Project is not bound to, and the Code
// provider is never reached.
func TestCommitToolRejectsRepositoryMismatch(t *testing.T) {
	ctx := context.Background()
	hub := &codeCommitHub{}
	upstream := hub.serve(t)
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, []workspace.File{{Path: "index.html", Content: "hello\n"}})
	server := newCommitActionServer(t, workspaces, upstream.URL)
	_, err := server.commitProjectWorkspaceFiles(ctx, testCommitIdentity(), scope, nil, testCommitRepositoryRef,
		map[string]any{"repositoryRef": "other-repo", "paths": []any{"index.html"}})
	if err == nil || !strings.Contains(err.Error(), "does not match this Project") {
		t.Fatalf("commit error = %v, want a deterministic repository mismatch", err)
	}
	if len(hub.calls) != 0 {
		t.Fatal("a mismatched repositoryRef reached the Code provider")
	}
}

// TestCommitToolBoundsPayloadBeforeTheProvider keeps every local bound ahead
// of the network call.
func TestCommitToolBoundsPayloadBeforeTheProvider(t *testing.T) {
	ctx := context.Background()
	hub := &codeCommitHub{}
	upstream := hub.serve(t)
	workspaces := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	server := newCommitActionServer(t, workspaces, upstream.URL)

	tooManyPaths := make([]any, 0, projectCommitProjectFilesMax+1)
	for i := 0; i < projectCommitProjectFilesMax+1; i++ {
		tooManyPaths = append(tooManyPaths, fmt.Sprintf("src/file-%03d.txt", i))
	}
	if _, err := server.commitProjectWorkspaceFiles(ctx, testCommitIdentity(), scope, nil, testCommitRepositoryRef,
		map[string]any{"repositoryRef": testCommitRepositoryRef, "paths": tooManyPaths}); err == nil || !strings.Contains(err.Error(), "too many paths") {
		t.Fatalf("too many paths error = %v, want a bounded path count", err)
	}

	count := projectCommitProjectFilesMaxSize/workspace.MaxWriteBytes + 1
	files := make([]workspace.File, 0, count)
	paths := make([]any, 0, count)
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("src/large-%03d.txt", i)
		files = append(files, workspace.File{Path: path, Content: strings.Repeat("x", workspace.MaxWriteBytes)})
		paths = append(paths, path)
	}
	writeTestWorkspaceFiles(t, ctx, workspaces, scope, files)
	if _, err := server.commitProjectWorkspaceFiles(ctx, testCommitIdentity(), scope, nil, testCommitRepositoryRef,
		map[string]any{"repositoryRef": testCommitRepositoryRef, "paths": paths}); err == nil || !strings.Contains(err.Error(), "payload is too large") {
		t.Fatalf("payload size error = %v, want a bounded aggregate size", err)
	}
	if len(hub.calls) != 0 {
		t.Fatal("the Code provider was called after a local bounds failure")
	}
}
