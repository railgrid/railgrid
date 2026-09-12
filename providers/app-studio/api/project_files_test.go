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
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

type projectFilesFixture struct {
	server     *Server
	workspaces *workspace.FileStore
	scope      workspace.Scope
	project    *aiv1alpha1.Project
	syncs      atomic.Int32
}

func newProjectFilesFixture(t *testing.T) *projectFilesFixture {
	t.Helper()
	project := projectForPromoteWithRepository("shop", "repo-a")
	project.UID = types.UID("project-uid")
	client := newProjectBuildProvenanceClient(project, nil, nil)
	fixture := &projectFilesFixture{
		workspaces: workspace.NewFileStore(t.TempDir()),
		scope:      workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "shop", ProjectUID: string(project.UID)},
		project:    project,
	}
	fixture.server = &Server{
		tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a")}.lookup,
		store:            store.NewMemoryStore(),
		workspaces:       fixture.workspaces,
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
		developmentSyncAfterMutation: func(_ identity, _ *aiv1alpha1.Project, action string) error {
			if action != projectActionWorkspaceFileWrite {
				t.Errorf("sync action = %q", action)
			}
			fixture.syncs.Add(1)
			return nil
		},
	}
	return fixture
}

func (f *projectFilesFixture) request(method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, body)
	request = mux.SetURLVars(request, map[string]string{"project": "shop"})
	request.Header.Set("X-Faros-Tenant", "cluster-a")
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("X-Faros-Cluster", "cluster-a")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	switch {
	case method == http.MethodPut:
		f.server.writeProjectFile(response, request)
	case method == http.MethodDelete:
		f.server.deleteProjectFile(response, request)
	case method == http.MethodPost:
		f.server.uploadProjectFiles(response, request)
	case strings.Contains(target, "/files/raw"):
		f.server.readProjectFileRaw(response, request)
	default:
		f.server.readProjectFile(response, request)
	}
	return response
}

func (f *projectFilesFixture) waitForSyncs(t *testing.T, want int32) {
	t.Helper()
	for i := 0; i < 200 && f.syncs.Load() < want; i++ {
		time.Sleep(time.Millisecond)
	}
	if got := f.syncs.Load(); got != want {
		t.Fatalf("development syncs = %d, want %d", got, want)
	}
}

func testPNG(n int) []byte {
	data := make([]byte, n)
	copy(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	for i := 8; i < n; i++ {
		data[i] = byte(i)
	}
	return data
}

func TestProjectFilePutReadRawAndDelete(t *testing.T) {
	f := newProjectFilesFixture(t)
	image := testPNG(workspace.MaxWriteBytes + 1000)
	target := "/api/projects/shop/files/content?path=public/logo.png"

	created := f.request(http.MethodPut, target, bytes.NewReader(image), map[string]string{"If-None-Match": "*", "Content-Type": "image/png"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var write projectFileWriteResult
	if err := json.Unmarshal(created.Body.Bytes(), &write); err != nil {
		t.Fatal(err)
	}
	if write.Path != "public/logo.png" || !write.Binary || write.Size != int64(len(image)) || !strings.HasPrefix(write.Version, "sha256:") {
		t.Fatalf("create result = %#v", write)
	}
	f.waitForSyncs(t, 1)
	dirty, err := f.workspaces.UncommittedPaths(context.Background(), f.scope)
	if err != nil || strings.Join(dirty, ",") != "public/logo.png" {
		t.Fatalf("dirty paths = %v, %v", dirty, err)
	}

	if again := f.request(http.MethodPut, target, bytes.NewReader(image), map[string]string{"If-None-Match": "*"}); again.Code != http.StatusPreconditionFailed {
		t.Fatalf("create over existing = %d, want 412", again.Code)
	}
	if stale := f.request(http.MethodPut, target, bytes.NewReader(image[:10]), map[string]string{"If-Match": `"sha256:stale"`}); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale replace = %d, want 412", stale.Code)
	}

	read := f.request(http.MethodGet, target, nil, nil)
	var content map[string]any
	if err := json.Unmarshal(read.Body.Bytes(), &content); err != nil || read.Code != http.StatusOK {
		t.Fatalf("read = %d %s", read.Code, read.Body.String())
	}
	if content["binary"] != true || content["content"] != "" || content["truncated"] != false || content["version"] != write.Version || content["size"] != float64(len(image)) {
		t.Fatalf("read body = %v", content)
	}

	raw := f.request(http.MethodGet, "/api/projects/shop/files/raw?path=public/logo.png&download=1", nil, nil)
	if raw.Code != http.StatusOK || !bytes.Equal(raw.Body.Bytes(), image) {
		t.Fatalf("raw = %d (%d bytes)", raw.Code, raw.Body.Len())
	}
	if got := raw.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("raw content type = %q", got)
	}
	if raw.Header().Get("ETag") != `"`+write.Version+`"` || !strings.HasPrefix(raw.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("raw headers = %v", raw.Header())
	}
	if cached := f.request(http.MethodGet, "/api/projects/shop/files/raw?path=public/logo.png", nil, map[string]string{"If-None-Match": `"` + write.Version + `"`}); cached.Code != http.StatusNotModified {
		t.Fatalf("conditional raw = %d, want 304", cached.Code)
	}

	replacement := testPNG(64)
	replaced := f.request(http.MethodPut, target, bytes.NewReader(replacement), map[string]string{"If-Match": write.Version})
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace = %d %s", replaced.Code, replaced.Body.String())
	}
	f.waitForSyncs(t, 2)

	if deleted := f.request(http.MethodDelete, target, nil, nil); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	f.waitForSyncs(t, 3)
	if missing := f.request(http.MethodDelete, target, nil, nil); missing.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d, want 404", missing.Code)
	}
	if missing := f.request(http.MethodGet, target, nil, nil); missing.Code != http.StatusNotFound {
		t.Fatalf("read missing = %d, want 404", missing.Code)
	}
}

func TestProjectFilePutBoundsAndPathValidation(t *testing.T) {
	f := newProjectFilesFixture(t)
	tooBig := f.request(http.MethodPut, "/api/projects/shop/files/content?path=big.bin", bytes.NewReader(testPNG(workspace.MaxBinaryWriteBytes+1)), nil)
	if tooBig.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized binary = %d, want 413", tooBig.Code)
	}
	bigText := f.request(http.MethodPut, "/api/projects/shop/files/content?path=big.json", strings.NewReader(strings.Repeat("x", workspace.MaxWriteBytes+1)), nil)
	if bigText.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized text = %d, want 413", bigText.Code)
	}
	for _, target := range []string{"../escape.txt", ".git/config", "node_modules/x.js", ""} {
		response := f.request(http.MethodPut, "/api/projects/shop/files/content?path="+target, strings.NewReader("x"), nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("PUT %q = %d, want 400", target, response.Code)
		}
	}
	text := f.request(http.MethodPut, "/api/projects/shop/files/content?path=src/app.ts", strings.NewReader("export {}\n"), nil)
	if text.Code != http.StatusCreated || strings.Contains(text.Body.String(), `"binary":true`) {
		t.Fatalf("text upsert = %d %s", text.Code, text.Body.String())
	}
}

func TestProjectFileWritesConflictWithActiveAssistantRun(t *testing.T) {
	f := newProjectFilesFixture(t)
	release, err := f.server.projectAssistantSupervisor().Reserve(projectMessageScope("org-a", "workspace-a", f.project))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	response := f.request(http.MethodPut, "/api/projects/shop/files/content?path=a.bin", bytes.NewReader([]byte{0, 1}), nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("write during run = %d, want 409", response.Code)
	}
	if exists, _ := f.workspaces.FileExists(context.Background(), f.scope, "a.bin"); exists {
		t.Fatal("write landed while an assistant run owned the project")
	}
}

func TestProjectFileUpload(t *testing.T) {
	f := newProjectFilesFixture(t)
	upload := func(dir string, overwrite bool, files map[string][]byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		_ = writer.WriteField("dir", dir)
		if overwrite {
			_ = writer.WriteField("overwrite", "true")
		}
		for name, data := range files {
			part, err := writer.CreateFormFile("file", name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = part.Write(data)
		}
		_ = writer.Close()
		return f.request(http.MethodPost, "/api/projects/shop/files/upload", &body, map[string]string{"Content-Type": writer.FormDataContentType()})
	}
	model := testPNG(workspace.MaxWriteBytes * 4)
	first := upload("public/assets/", false, map[string][]byte{"jeep.glb": model, "readme.txt": []byte("hello\n")})
	if first.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", first.Code, first.Body.String())
	}
	var result struct {
		Files []projectFileWriteResult `json:"files"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil || len(result.Files) != 2 {
		t.Fatalf("upload body = %s", first.Body.String())
	}
	f.waitForSyncs(t, 1)
	dirty, _ := f.workspaces.UncommittedPaths(context.Background(), f.scope)
	if strings.Join(dirty, ",") != "public/assets/jeep.glb,public/assets/readme.txt" {
		t.Fatalf("dirty after upload = %v", dirty)
	}
	if conflict := upload("public/assets", false, map[string][]byte{"jeep.glb": model}); conflict.Code != http.StatusConflict {
		t.Fatalf("upload over existing = %d, want 409", conflict.Code)
	}
	if replaced := upload("public/assets", true, map[string][]byte{"jeep.glb": testPNG(100)}); replaced.Code != http.StatusOK {
		t.Fatalf("overwrite upload = %d %s", replaced.Code, replaced.Body.String())
	}
	if escape := upload("../outside", false, map[string][]byte{"x.bin": {0}}); escape.Code != http.StatusBadRequest {
		t.Fatalf("escaping upload = %d, want 400", escape.Code)
	}
}
