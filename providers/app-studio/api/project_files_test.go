/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
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
	bindTestProjectLedgerTo(fixture.workspaces, client)
	fixture.server = &Server{
		tenantWorkspaces: staticWorkspaces{"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a")}.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders,
		store:                   store.NewMemoryStore(),
		workspaces:              fixture.workspaces,
		projectClientFor:        func(identity) (*asclient.Client, error) { return client, nil },
		projectIdentityTokenFor: testProjectIdentityToken,
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
	request.Header.Set("X-Railgrid-Tenant", "cluster-a")
	request = stampTestCaller(request, testUserForToken("test-token"))
	request.Header.Set("X-Railgrid-Cluster", "cluster-a")
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

type projectFileReplaceOnResponseWriter struct {
	*httptest.ResponseRecorder
	beforeSuccess func()
}

func (w *projectFileReplaceOnResponseWriter) WriteHeader(status int) {
	if status == http.StatusOK && w.beforeSuccess != nil {
		w.beforeSuccess()
		w.beforeSuccess = nil
	}
	w.ResponseRecorder.WriteHeader(status)
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
	if missingVersion := f.request(http.MethodPut, target, bytes.NewReader(image[:10]), nil); missingVersion.Code != http.StatusPreconditionRequired {
		t.Fatalf("replace without version = %d, want 428", missingVersion.Code)
	}
	if missingDeleteVersion := f.request(http.MethodDelete, target, nil, nil); missingDeleteVersion.Code != http.StatusPreconditionRequired {
		t.Fatalf("delete without version = %d, want 428", missingDeleteVersion.Code)
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

	var replacementResult projectFileWriteResult
	if err := json.Unmarshal(replaced.Body.Bytes(), &replacementResult); err != nil {
		t.Fatal(err)
	}
	if deleted := f.request(http.MethodDelete, target, nil, map[string]string{"If-Match": replacementResult.Version}); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	f.waitForSyncs(t, 3)
	if missing := f.request(http.MethodDelete, target, nil, map[string]string{"If-Match": replacementResult.Version}); missing.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d, want 404", missing.Code)
	}
	if missing := f.request(http.MethodGet, target, nil, nil); missing.Code != http.StatusNotFound {
		t.Fatalf("read missing = %d, want 404", missing.Code)
	}
}

func TestProjectFileRawETagMatchesBytesAfterSameSizeReplacement(t *testing.T) {
	f := newProjectFilesFixture(t)
	ctx := context.Background()
	original := []byte("same-size-v1")
	replacement := []byte("same-size-v2")
	if len(original) != len(replacement) {
		t.Fatal("test fixture contents must have equal size")
	}
	if _, err := f.workspaces.PutFile(ctx, f.scope, workspace.PutOptions{Path: "public/asset.bin", Data: original}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/projects/shop/files/raw?path=public/asset.bin", nil)
	request = mux.SetURLVars(request, map[string]string{"project": "shop"})
	request.Header.Set("X-Railgrid-Tenant", "cluster-a")
	request = stampTestCaller(request, testUserForToken("test-token"))
	request.Header.Set("X-Railgrid-Cluster", "cluster-a")
	recorder := &projectFileReplaceOnResponseWriter{
		ResponseRecorder: httptest.NewRecorder(),
		beforeSuccess: func() {
			if _, err := f.workspaces.PutFile(ctx, f.scope, workspace.PutOptions{Path: "public/asset.bin", Data: replacement}); err != nil {
				t.Errorf("replace path after raw descriptor was opened: %v", err)
			}
		},
	}
	f.server.readProjectFileRaw(recorder, request)

	digest := sha256.Sum256(original)
	wantETag := `"sha256:` + hex.EncodeToString(digest[:]) + `"`
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), original) || recorder.Header().Get("ETag") != wantETag {
		t.Fatalf("raw response = status %d body %q ETag %q; want original bytes and matching ETag %q", recorder.Code, recorder.Body.Bytes(), recorder.Header().Get("ETag"), wantETag)
	}
	current, err := f.workspaces.ReadFileBytes(ctx, f.scope, "public/asset.bin", 0)
	if err != nil || !bytes.Equal(current, replacement) {
		t.Fatalf("path after response = %q err=%v; want concurrent replacement", current, err)
	}
}

func TestProjectFilePutRejectsEmptyIfMatchVersion(t *testing.T) {
	f := newProjectFilesFixture(t)
	ctx := context.Background()
	target := "/api/projects/shop/files/content?path=existing.txt"
	created := f.request(http.MethodPut, target, strings.NewReader("original content\n"), map[string]string{"If-None-Match": "*"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create existing target = %d %s", created.Code, created.Body.String())
	}

	for _, tc := range []struct {
		name   string
		path   string
		tag    string
		exists bool
	}{
		{name: "quoted empty existing target", path: "existing.txt", tag: `""`, exists: true},
		{name: "quoted whitespace missing target", path: "missing.txt", tag: `"   "`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := f.request(
				http.MethodPut,
				"/api/projects/shop/files/content?path="+tc.path,
				strings.NewReader("unconditional replacement\n"),
				map[string]string{"If-Match": tc.tag},
			)
			if response.Code != http.StatusPreconditionRequired {
				t.Fatalf("PUT with If-Match %q = %d %s, want 428", tc.tag, response.Code, response.Body.String())
			}
			exists, err := f.workspaces.FileExists(ctx, f.scope, tc.path)
			if err != nil {
				t.Fatalf("FileExists: %v", err)
			}
			if exists != tc.exists {
				t.Fatalf("target exists = %t, want %t", exists, tc.exists)
			}
			if tc.exists {
				file, err := f.workspaces.ReadFile(ctx, f.scope, workspace.ReadOptions{Path: tc.path})
				if err != nil {
					t.Fatalf("ReadFile: %v", err)
				}
				if string(file.Content) != "original content\n" {
					t.Fatalf("existing file content = %q, want original content", file.Content)
				}
			}
		})
	}
}

func TestProjectFilePutBoundsAndPathValidation(t *testing.T) {
	f := newProjectFilesFixture(t)
	tooBig := f.request(http.MethodPut, "/api/projects/shop/files/content?path=big.bin", bytes.NewReader(testPNG(workspace.MaxBinaryWriteBytes+1)), map[string]string{"If-None-Match": "*"})
	if tooBig.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized binary = %d, want 413", tooBig.Code)
	}
	bigText := f.request(http.MethodPut, "/api/projects/shop/files/content?path=big.json", strings.NewReader(strings.Repeat("x", workspace.MaxWriteBytes+1)), map[string]string{"If-None-Match": "*"})
	if bigText.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized text = %d, want 413", bigText.Code)
	}
	for _, target := range []string{"../escape.txt", ".git/config", "node_modules/x.js", ""} {
		response := f.request(http.MethodPut, "/api/projects/shop/files/content?path="+target, strings.NewReader("x"), map[string]string{"If-None-Match": "*"})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("PUT %q = %d, want 400", target, response.Code)
		}
	}
	text := f.request(http.MethodPut, "/api/projects/shop/files/content?path=src/app.ts", strings.NewReader("export {}\n"), map[string]string{"If-None-Match": "*"})
	if text.Code != http.StatusCreated || strings.Contains(text.Body.String(), `"binary":true`) {
		t.Fatalf("text upsert = %d %s", text.Code, text.Body.String())
	}
}

func TestProjectFileWritesShareOwnerWithActiveAssistantRun(t *testing.T) {
	f := newProjectFilesFixture(t)
	release, err := f.server.projectAssistantSupervisor().ReserveThread(projectMessageScope("org-a", "workspace-a", f.project), "thread-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	response := f.request(http.MethodPut, "/api/projects/shop/files/content?path=a.bin", bytes.NewReader([]byte{0, 1}), map[string]string{"If-None-Match": "*"})
	if response.Code != http.StatusCreated {
		t.Fatalf("write during run = %d, want 201", response.Code)
	}
	if exists, _ := f.workspaces.FileExists(context.Background(), f.scope, "a.bin"); !exists {
		t.Fatal("disjoint write did not land under the shared project owner")
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
			versions := map[string]string{}
			for name := range files {
				target := strings.TrimSuffix(dir, "/") + "/" + name
				meta, err := f.workspaces.InspectFile(context.Background(), f.scope, target)
				if err != nil {
					t.Fatal(err)
				}
				versions[target] = meta.Version
			}
			encoded, _ := json.Marshal(versions)
			_ = writer.WriteField("expectedVersions", string(encoded))
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
	first := upload("public/assets/", false, map[string][]byte{"jeep.glb": model, "readme.txt": []byte("hello\n"), "empty.txt": {}})
	if first.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", first.Code, first.Body.String())
	}
	var result struct {
		Files []projectFileWriteResult `json:"files"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil || len(result.Files) != 3 {
		t.Fatalf("upload body = %s", first.Body.String())
	}
	writeResults := make(map[string]projectFileWriteResult, len(result.Files))
	for _, file := range result.Files {
		writeResults[file.Path] = file
	}
	if _, ok := writeResults["public/assets/jeep.glb"]; !ok {
		t.Fatal("upload response omitted public/assets/jeep.glb")
	}
	if _, ok := writeResults["public/assets/readme.txt"]; !ok {
		t.Fatal("upload response omitted public/assets/readme.txt")
	}
	if _, ok := writeResults["public/assets/empty.txt"]; !ok {
		t.Fatal("upload response omitted public/assets/empty.txt")
	}
	if !writeResults["public/assets/jeep.glb"].Binary || writeResults["public/assets/readme.txt"].Binary {
		t.Fatalf("upload binary classifications = jeep.glb:%t readme.txt:%t, want true/false", writeResults["public/assets/jeep.glb"].Binary, writeResults["public/assets/readme.txt"].Binary)
	}
	if writeResults["public/assets/empty.txt"].Binary || writeResults["public/assets/empty.txt"].Size != 0 || writeResults["public/assets/empty.txt"].Version == "" {
		t.Fatalf("empty upload result = %#v, want zero-byte text with a version", writeResults["public/assets/empty.txt"])
	}
	f.waitForSyncs(t, 1)
	dirty, _ := f.workspaces.UncommittedPaths(context.Background(), f.scope)
	if strings.Join(dirty, ",") != "public/assets/empty.txt,public/assets/jeep.glb,public/assets/readme.txt" {
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

func TestProjectFileUploadIsAtomicAndReportsChangedFiles(t *testing.T) {
	f := newProjectFilesFixture(t)
	ctx := context.Background()
	if _, err := f.workspaces.PutFile(ctx, f.scope, workspace.PutOptions{Path: "uploads/existing.txt", Data: []byte("before")}); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("dir", "uploads")
	_ = writer.WriteField("overwrite", "true")
	expectedVersions, _ := json.Marshal(map[string]string{"uploads/existing.txt": "sha256:stale"})
	_ = writer.WriteField("expectedVersions", string(expectedVersions))
	for name, data := range map[string][]byte{"new.txt": []byte("new file"), "existing.txt": []byte("after")} {
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	response := f.request(http.MethodPost, "/api/projects/shop/files/upload", &body, map[string]string{"Content-Type": writer.FormDataContentType()})
	if response.Code != http.StatusConflict {
		t.Fatalf("stale upload = %d %s, want 409", response.Code, response.Body.String())
	}
	var envelope struct {
		Kind       string                  `json:"kind"`
		APIVersion string                  `json:"apiVersion"`
		Metadata   map[string]any          `json:"metadata"`
		Status     string                  `json:"status"`
		Reason     string                  `json:"reason"`
		Code       int                     `json:"code"`
		Conflict   workspace.MutationError `json:"conflict"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode upload conflict: %v (%s)", err, response.Body.String())
	}
	if envelope.Kind != "Status" || envelope.APIVersion != "v1" || envelope.Metadata == nil || len(envelope.Metadata) != 0 || envelope.Status != "Failure" || envelope.Reason != "Conflict" || envelope.Code != http.StatusConflict {
		t.Fatalf("upload conflict Status envelope = %#v, want the shared conflict Status shape", envelope)
	}
	if len(envelope.Conflict.ChangedFiles) != 1 || envelope.Conflict.ChangedFiles[0] != "uploads/existing.txt" {
		t.Fatalf("conflict changedFiles = %v, want [uploads/existing.txt]", envelope.Conflict.ChangedFiles)
	}
	if exists, err := f.workspaces.FileExists(ctx, f.scope, "uploads/new.txt"); err != nil || exists {
		t.Fatalf("new path after rejected transaction exists=%t err=%v", exists, err)
	}
	current, err := f.workspaces.ReadFileBytes(ctx, f.scope, "uploads/existing.txt", 0)
	if err != nil || string(current) != "before" {
		t.Fatalf("existing path after rejected transaction = %q err=%v", current, err)
	}
}
