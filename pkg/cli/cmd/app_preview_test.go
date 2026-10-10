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
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/apiurl"
)

func TestAppPreviewCommand(t *testing.T) {
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")

	var method, body string
	preview := func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		body = b.String()
		mode := "restricted"
		if r.Method == http.MethodPost {
			mode = "public"
		}
		writeTestJSON(w, map[string]any{"mode": mode, "url": "https://shop-dev.example.com", "converged": r.Method == http.MethodGet, "supported": true})
	}
	for _, m := range []string{"GET", "POST", "DELETE"} {
		hub.handle(m+" "+appStudioProjects+"/shop/preview", preview)
	}

	out, err := runRoot(t, path, "app", "preview", "shop")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || !strings.Contains(out, "shop: restricted  https://shop-dev.example.com") {
		t.Fatalf("method=%s out=%q", method, out)
	}
	out, err = runRoot(t, path, "app", "preview", "shop", "--mode", "public")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || body != `{"mode":"public"}` || !strings.Contains(out, "public  https://shop-dev.example.com  (converging)  (anonymous requests may still be redirected") {
		t.Fatalf("method=%s body=%s out=%q", method, body, out)
	}
	if _, err := runRoot(t, path, "app", "preview", "shop", "--mode", "private"); err != nil || method != http.MethodDelete {
		t.Fatalf("private: err=%v method=%s", err, method)
	}
	if _, err := runRoot(t, path, "app", "preview", "shop", "--mode", "everyone"); err == nil || !strings.Contains(err.Error(), "--mode must be") {
		t.Fatalf("bad mode err = %v", err)
	}
}

func TestAppCheckpointsAndStatusBlocked(t *testing.T) {
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")

	checkpoints := map[string]any{"items": []map[string]any{
		{"key": "template", "label": "Template", "state": "done", "reason": "Bound to template simple-webapp."},
		{"key": "ci", "label": "Source", "state": "blocked", "reason": "The repository has no .github/workflows/build.yaml.",
			"remediation": map[string]any{"kind": "manual", "message": "POST the project's scaffold verb."}},
		{"key": "production", "label": "Production", "state": "blocked", "reason": "list published packages: forbidden"},
	}}
	hub.handle("GET "+appStudioProjects+"/shop/checkpoints", func(w http.ResponseWriter, r *http.Request) { writeTestJSON(w, checkpoints) })
	hub.handle("GET "+appStudioProjects+"/shop/view", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"name": "shop", "phase": "Ready", "template": "simple-webapp",
			"repository": map[string]any{"ref": "shop", "ready": true}})
	})
	hub.handle("GET "+appStudioProjects+"/shop/promotion", func(w http.ResponseWriter, r *http.Request) {
		writeTestStatus(w, http.StatusForbidden, "Forbidden", "list published packages: forbidden")
	})
	hub.handle("GET "+appStudioProjects+"/shop/publishing", func(w http.ResponseWriter, r *http.Request) { writeTestJSON(w, map[string]any{"published": false}) })
	hub.handle("GET "+appStudioProjects+"/shop/preview", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"mode": "public", "url": "https://shop-dev.example.com", "converged": true, "supported": true})
	})

	out, err := runRoot(t, path, "app", "checkpoints", "shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Template:", "done", "Source:", "blocked  The repository has no", "fix: POST the project's scaffold verb.", "Production:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("checkpoints output missing %q:\n%s", want, out)
		}
	}

	out, err = runRoot(t, path, "app", "status", "shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Dev URL:      -",
		"development instance is still coming up",
		"Preview:      public  https://shop-dev.example.com",
		"Promotion:    unavailable: GET",
		"Blocked:      Source blocked: The repository has no .github/workflows/build.yaml.",
		"Production blocked: list published packages: forbidden",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
	out, err = runRoot(t, path, "app", "status", "shop", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var st appStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil || len(st.Checkpoints) == 0 || len(st.Preview) == 0 || st.PromotionError == "" {
		t.Fatalf("json status = %s (%v)", out, err)
	}
}

func TestAppFilesCommands(t *testing.T) {
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")

	hub.handle("GET "+appStudioProjects+"/shop/files", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"files": []map[string]any{{"path": "api/server.mjs", "size": 120}, {"path": "web/public/logo.png", "size": 3}}})
	})
	hub.handle("GET "+appStudioProjects+"/shop/files-raw", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") != "web/public/logo.png" {
			writeTestStatus(w, http.StatusNotFound, "NotFound", "file not found")
			return
		}
		w.Header().Set("ETag", `"sha256:abcdef0123456789"`)
		_, _ = w.Write([]byte{0x89, 'P', 'N'})
	})
	var putPath, putBody, putMatch string
	hub.handle("PUT "+appStudioProjects+"/shop/files-content", func(w http.ResponseWriter, r *http.Request) {
		putPath = r.URL.Query().Get("path")
		putMatch = r.Header.Get("If-None-Match")
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		putBody = b.String()
		w.WriteHeader(http.StatusCreated)
		writeTestJSON(w, map[string]any{"path": putPath, "size": len(putBody), "version": "sha256:abcdef0123456789", "binary": true})
	})
	var deleted, deleteVersion string
	hub.handle("DELETE "+appStudioProjects+"/shop/files-content", func(w http.ResponseWriter, r *http.Request) {
		deleted = r.URL.Query().Get("path")
		deleteVersion = r.Header.Get("If-Match")
		w.WriteHeader(http.StatusNoContent)
	})

	out, err := runRoot(t, path, "app", "files", "ls", "shop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "120   api/server.mjs") || !strings.Contains(out, "3     web/public/logo.png") {
		t.Fatalf("ls output = %q", out)
	}
	out, err = runRoot(t, path, "app", "files", "get", "shop", "web/public/logo.png")
	if err != nil {
		t.Fatal(err)
	}
	if out != "\x89PN" {
		t.Fatalf("get output = %q", out)
	}
	if version, err := runRoot(t, path, "app", "files", "get", "shop", "web/public/logo.png", "--version-only"); err != nil || version != "sha256:abcdef0123456789\n" {
		t.Fatalf("file version = %q, %v", version, err)
	}
	local := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(local, []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runRoot(t, path, "app", "files", "put", "shop", "web/public/logo.png", local, "--create-only")
	if err != nil {
		t.Fatal(err)
	}
	if putPath != "web/public/logo.png" || putBody != "\x01\x02\x03" || putMatch != "*" || !strings.Contains(out, "wrote web/public/logo.png (3 bytes, binary") {
		t.Fatalf("put: path=%s body=%q match=%q out=%q", putPath, putBody, putMatch, out)
	}
	if _, err := runRoot(t, path, "app", "files", "rm", "shop", "web/public/old.png"); err == nil || deleted != "" {
		t.Fatalf("versionless delete: err=%v deleted=%s", err, deleted)
	}
	if _, err := runRoot(t, path, "app", "files", "rm", "shop", "web/public/old.png", "--expected-version", "sha256:abcdef0123456789"); err != nil || deleted != "web/public/old.png" || deleteVersion != "sha256:abcdef0123456789" {
		t.Fatalf("rm: err=%v deleted=%s", err, deleted)
	}
}

func TestAppSyncFromPushesThroughDevSync(t *testing.T) {
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")

	var called string
	var files []syncFile
	hub.handle("POST "+apiurl.MCPServerPath("cl-b", "default"), func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					Instance string     `json:"instance"`
					Files    []syncFile `json:"files"`
				} `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		called = req.Params.Name + "@" + req.Params.Arguments.Instance
		files = req.Params.Arguments.Files
		writeTestJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"structuredContent": map[string]any{
			"instance":   "shop-dev",
			"components": map[string]any{"web": map[string]any{"files": 2, "response": map[string]any{"phase": "Synced", "changed": []string{"src/App.jsx", "public/logo.png"}, "sourceRevision": 4}}},
		}}})
	})

	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if err := os.MkdirAll(filepath.Join(dir, "web", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "src", "App.jsx"), []byte("export default 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "web", "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "public", "logo.png"), []byte{0x89, 'P', 'N', 'G', 0}, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runRoot(t, path, "app", "sync", "shop", "--from", dir)
	if err != nil {
		t.Fatal(err)
	}
	if called != "infrastructure__dev_sync@shop-dev" {
		t.Fatalf("called = %q", called)
	}
	if len(files) != 2 || files[0].Path != "web/public/logo.png" || files[0].Encoding != "base64" || files[1].Path != "web/src/App.jsx" || files[1].Encoding != "" {
		t.Fatalf("files = %+v", files)
	}
	for _, want := range []string{"shop-dev/web: Synced, 2 changed, 0 deleted, restarted=false, revision 4", "nothing was committed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}
