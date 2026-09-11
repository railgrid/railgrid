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

package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSourceDigestKnownVector(t *testing.T) {
	// printf 'a.txt\0hello\n\0b/c.txt\0world\0' | shasum -a 256
	const want = "e46dba8009cafa79f5b813de5d7e654255bc0d907d1e2f2ee18150c778972994"
	files := []syncFile{{Path: "b/c.txt", Content: "world"}, {Path: "a.txt", Content: "hello\n"}}
	if got := sourceDigest(files); got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
	if files[0].Path != "b/c.txt" {
		t.Fatal("sourceDigest reordered its input")
	}
	// sha256 of the empty string.
	if got := sourceDigest(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty digest = %s", got)
	}
}

func TestListSyncFilesPlainDir(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	dir := t.TempDir()
	for _, p := range []string{"package.json", "src/index.js", "node_modules/x/index.js", "dist/bundle.js", "sub/dist.txt"} {
		writeTestFile(t, filepath.Join(dir, filepath.FromSlash(p)), "x")
	}
	got, err := listSyncFiles(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"package.json", "src/index.js", "sub/dist.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestListSyncFilesGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitTest(t, root, "init", "-q")
	writeTestFile(t, filepath.Join(root, ".gitignore"), "ignored.txt\n")
	writeTestFile(t, filepath.Join(root, "api", "tracked.js"), "t")
	writeTestFile(t, filepath.Join(root, "api", "ignored.txt"), "i")
	writeTestFile(t, filepath.Join(root, "web", "other.js"), "o")
	gitTest(t, root, "add", "api/tracked.js")
	writeTestFile(t, filepath.Join(root, "api", "untracked.js"), "u")

	// Paths are relative to the synced directory, not the repository root.
	got, err := listSyncFiles(context.Background(), filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tracked.js", "untracked.js"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSourceDigestHashesDecodedBinaryBytes(t *testing.T) {
	raw := []byte{0x89, 'P', 'N', 'G', 0, 0xff}
	h := sha256.New()
	for _, part := range [][]byte{[]byte("a.txt"), {0}, []byte("hello\n"), {0}, []byte("logo.png"), {0}, raw, {0}} {
		h.Write(part)
	}
	want := hex.EncodeToString(h.Sum(nil))
	files := []syncFile{
		{Path: "logo.png", Content: base64.StdEncoding.EncodeToString(raw), Encoding: "base64"},
		{Path: "a.txt", Content: "hello\n"},
	}
	if got := sourceDigest(files); got != want {
		t.Fatalf("digest = %s, want %s (sha256 over decoded bytes)", got, want)
	}
}

func TestReadSyncFilesClassifiesAndSkipsReserved(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.js"), "a")
	writeTestFile(t, filepath.Join(dir, "logo.png"), "\x89PNG\x00")
	writeTestFile(t, filepath.Join(dir, ".git", "config"), "x")
	files, err := readSyncFiles(dir, []string{"a.js", "logo.png", ".git/config", "deleted.js"})
	if err != nil {
		t.Fatal(err)
	}
	want := []localSyncFile{{Path: "a.js", Data: []byte("a"), Text: true}, {Path: "logo.png", Data: []byte("\x89PNG\x00")}}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files=%+v", files)
	}
}

func TestBuildSyncFilesGating(t *testing.T) {
	bin := []byte{0x89, 'P', 'N', 'G', 0}
	local := []localSyncFile{{Path: "a.js", Data: []byte("a"), Text: true}, {Path: "logo.png", Data: bin}}

	files, skipped, err := buildSyncFiles(local, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []syncFile{{Path: "a.js", Content: "a"}}) || !reflect.DeepEqual(skipped, []string{"logo.png"}) {
		t.Fatalf("without base64: files=%+v skipped=%v", files, skipped)
	}

	files, skipped, err = buildSyncFiles(local, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []syncFile{{Path: "a.js", Content: "a"}, {Path: "logo.png", Content: base64.StdEncoding.EncodeToString(bin), Encoding: "base64"}}
	if !reflect.DeepEqual(files, want) || len(skipped) != 0 {
		t.Fatalf("with base64: files=%+v skipped=%v", files, skipped)
	}
	// Text entries carry no encoding field on the wire (old agents' format).
	b, _ := json.Marshal(files[0])
	if strings.Contains(string(b), "encoding") {
		t.Fatalf("text entry = %s", b)
	}
}

func TestBuildSyncFilesLimits(t *testing.T) {
	buf := make([]byte, binaryFileLimit+1) // NUL-filled: binary
	if _, _, err := buildSyncFiles([]localSyncFile{{Path: "ok.bin", Data: buf[:binaryFileLimit]}}, true); err != nil {
		t.Fatalf("25 MiB binary: %v", err)
	}
	if _, _, err := buildSyncFiles([]localSyncFile{{Path: "big.bin", Data: buf}}, true); err == nil || !strings.Contains(err.Error(), "big.bin") || !strings.Contains(err.Error(), "25 MiB") {
		t.Fatalf("over per-file limit: err = %v", err)
	}
	// An oversized binary is still just skipped for an agent without base64.
	if _, skipped, err := buildSyncFiles([]localSyncFile{{Path: "big.bin", Data: buf}}, false); err != nil || !reflect.DeepEqual(skipped, []string{"big.bin"}) {
		t.Fatalf("skip oversized binary: skipped=%v err=%v", skipped, err)
	}
	two := []localSyncFile{{Path: "a.bin", Data: buf[:binaryFileLimit]}, {Path: "b.bin", Data: buf[:binaryFileLimit]}}
	if _, _, err := buildSyncFiles(two, true); err == nil || !strings.Contains(err.Error(), "48 MiB") || !strings.Contains(err.Error(), "b.bin") {
		t.Fatalf("over total limit: err = %v", err)
	}
}

// dataPlanePrefix is the fake hub path of instance shop-dev on cluster cl-b.
const dataPlanePrefix = "/services/providers/infrastructure/dataplane/clusters/cl-b/instances/shop-dev"

func TestSandboxSyncSendsAuthoritativeDigest(t *testing.T) {
	hub := newFakeHub(t)
	hub.useKubeconfig("cl-b")
	hub.handle("GET "+dataPlanePrefix+"/components/api/process", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"running": true, "sourceRevision": 1})
	})
	var got syncRequest
	hub.handle("POST "+dataPlanePrefix+"/components/api/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Faros-Workspace") != "ws-b1" {
			http.Error(w, "missing workspace header", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeTestJSON(w, map[string]any{"phase": "Synced", "changed": []string{"index.js"}, "restarted": true, "sourceRevision": got.SourceRevision})
	})

	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	writeTestFile(t, filepath.Join(dir, "index.js"), "console.log(1)\n")
	writeTestFile(t, filepath.Join(dir, "lib", "util.js"), "export {}\n")

	var out, errOut bytes.Buffer
	if err := runSandboxSync(context.Background(), &out, &errOut, hubTarget{}, "shop-dev", "api", dir, "auto", ""); err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 || got.Restart != "auto" || got.DeletePaths == nil {
		t.Fatalf("request = %+v", got)
	}
	if got.SourceDigest != sourceDigest(got.Files) || got.SourceRevision < uint64(time.Now().Add(-time.Minute).Unix()) {
		t.Fatalf("revision/digest = %d %s", got.SourceRevision, got.SourceDigest)
	}
	if !strings.Contains(out.String(), "Synced: 1 changed") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSandboxSyncBinaryGating(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0xff}
	for _, tc := range []struct {
		name      string
		encodings []string
		wantBin   bool
	}{
		{name: "agent advertises base64", encodings: []string{"utf-8", "base64"}, wantBin: true},
		{name: "agent without syncEncodings", wantBin: false},
		{name: "agent with utf-8 only", encodings: []string{"utf-8"}, wantBin: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newFakeHub(t)
			hub.useKubeconfig("cl-b")
			hub.handle("GET "+dataPlanePrefix+"/components/api/process", func(w http.ResponseWriter, r *http.Request) {
				st := map[string]any{"running": true}
				if tc.encodings != nil {
					st["syncEncodings"] = tc.encodings
				}
				writeTestJSON(w, st)
			})
			var got syncRequest
			var rawBody []byte
			hub.handle("POST "+dataPlanePrefix+"/components/api/sync", func(w http.ResponseWriter, r *http.Request) {
				var buf bytes.Buffer
				_, _ = buf.ReadFrom(r.Body)
				rawBody = buf.Bytes()
				_ = json.Unmarshal(rawBody, &got)
				writeTestJSON(w, map[string]any{"phase": "Synced", "sourceRevision": got.SourceRevision})
			})

			dir := t.TempDir()
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
			writeTestFile(t, filepath.Join(dir, "index.js"), "console.log(1)\n")
			if err := os.WriteFile(filepath.Join(dir, "logo.png"), png, 0o644); err != nil {
				t.Fatal(err)
			}

			var out, errOut bytes.Buffer
			if err := runSandboxSync(context.Background(), &out, &errOut, hubTarget{}, "shop-dev", "api", dir, "auto", ""); err != nil {
				t.Fatal(err)
			}
			h := sha256.New()
			h.Write([]byte("index.js\x00console.log(1)\n\x00"))
			if tc.wantBin {
				want := []syncFile{
					{Path: "index.js", Content: "console.log(1)\n"},
					{Path: "logo.png", Content: base64.StdEncoding.EncodeToString(png), Encoding: "base64"},
				}
				if !reflect.DeepEqual(got.Files, want) {
					t.Fatalf("files = %+v", got.Files)
				}
				h.Write([]byte("logo.png\x00"))
				h.Write(png)
				h.Write([]byte{0})
				if strings.Contains(errOut.String(), "skipping") {
					t.Fatalf("unexpected skip warning: %s", errOut.String())
				}
			} else {
				if !reflect.DeepEqual(got.Files, []syncFile{{Path: "index.js", Content: "console.log(1)\n"}}) {
					t.Fatalf("files = %+v", got.Files)
				}
				if bytes.Contains(rawBody, []byte("base64")) {
					t.Fatalf("base64 sent to an agent that does not advertise it: %s", rawBody)
				}
				if !strings.Contains(errOut.String(), "skipping 1 binary file(s)") || !strings.Contains(errOut.String(), "logo.png") {
					t.Fatalf("stderr = %q", errOut.String())
				}
			}
			if want := hex.EncodeToString(h.Sum(nil)); got.SourceDigest != want {
				t.Fatalf("digest = %s, want %s", got.SourceDigest, want)
			}
		})
	}
}

func TestSandboxExecStartsAndPolls(t *testing.T) {
	prev := sandboxPollInterval
	sandboxPollInterval = time.Millisecond
	t.Cleanup(func() { sandboxPollInterval = prev })

	hub := newFakeHub(t)
	hub.useKubeconfig("cl-b")
	hub.handle("GET "+dataPlanePrefix+"/components/api/process", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"running": true, "sourceRevision": 42, "sourceDigest": "abc"})
	})
	var mu sync.Mutex
	polls := 0
	var start execRequest
	var startKey string
	hub.handle("POST "+dataPlanePrefix+"/components/api/exec", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req execRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Action {
		case "start":
			start, startKey = req, r.Header.Get("Idempotency-Key")
			writeTestJSON(w, map[string]any{"sessionID": "s1", "state": "queued"})
		case "poll":
			polls++
			if req.SessionID != "s1" {
				http.Error(w, "unknown session", http.StatusNotFound)
				return
			}
			if polls < 2 {
				writeTestJSON(w, map[string]any{"sessionID": "s1", "state": "running"})
				return
			}
			writeTestJSON(w, map[string]any{"sessionID": "s1", "state": "exited", "exitCode": 3, "stdout": "out\n", "stderr": "err\n"})
		}
	})

	var out, errOut bytes.Buffer
	code, err := runSandboxExec(context.Background(), &out, &errOut, hubTarget{}, "shop-dev", "api", []string{"node", "-e", "x"}, "", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if code != 3 || out.String() != "out\n" || errOut.String() != "err\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	want := execRequest{Action: "start", Argv: []string{"node", "-e", "x"}, TimeoutSeconds: 30, SourceRevision: 42, SourceDigest: "abc"}
	if !reflect.DeepEqual(start, want) || !strings.HasPrefix(startKey, "faros-cli-") || polls != 2 {
		t.Fatalf("start=%+v key=%q polls=%d", start, startKey, polls)
	}
}

func TestSandboxExecRequiresAuthoritativeSync(t *testing.T) {
	hub := newFakeHub(t)
	hub.useKubeconfig("cl-b")
	hub.handle("GET "+dataPlanePrefix+"/components/api/process", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"running": true})
	})
	_, err := runSandboxExec(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, hubTarget{}, "shop-dev", "api", []string{"ls"}, "", time.Minute)
	if err == nil || !strings.Contains(err.Error(), "faros sandbox sync") {
		t.Fatalf("err = %v", err)
	}
	if _, err := runSandboxExec(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, hubTarget{}, "shop-dev", "api", []string{"ls"}, "", 5*time.Minute); err == nil {
		t.Fatal("expected a timeout above the 120s ceiling to be rejected")
	}
}

// instanceAPIPath is the fake hub's kube API path of instance shop-dev.
const instanceAPIPath = "/clusters/cl-b/apis/infrastructure.faros.sh/v1alpha1/instances/shop-dev"

func TestSandboxExecHintForAppStudioInstance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		instance http.HandlerFunc
		project  http.HandlerFunc
		want     string
		notWant  string
	}{
		{
			name: "project label",
			instance: func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(w, map[string]any{"metadata": map[string]any{"name": "shop-dev", "labels": map[string]any{"app-studio.faros.sh/project": "shop"}}})
			},
			want:    "run 'faros app sync shop' first",
			notWant: "run 'faros sandbox sync",
		},
		{
			name: "project owner reference",
			instance: func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(w, map[string]any{"metadata": map[string]any{"name": "shop-dev", "ownerReferences": []map[string]any{{"apiVersion": "ai.faros.sh/v1alpha1", "kind": "Project", "name": "shop"}}}})
			},
			want: "run 'faros app sync shop' first",
		},
		{
			name: "unreadable instance, App Studio project of the prefix exists",
			instance: func(w http.ResponseWriter, r *http.Request) {
				writeTestStatus(w, http.StatusForbidden, "Forbidden", "cannot get instances")
			},
			project: func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(w, map[string]any{"name": "shop"})
			},
			want: "run 'faros app sync shop' first",
		},
		{
			name: "plain instance",
			instance: func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(w, map[string]any{"metadata": map[string]any{"name": "shop-dev", "labels": map[string]any{"faros.sh/template": "application"}}})
			},
			// A readable instance without the marker is not App Studio's,
			// whatever its name.
			project: func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(w, map[string]any{"name": "shop"})
			},
			want:    "run 'faros sandbox sync shop-dev api <dir>' first",
			notWant: "faros app sync",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newFakeHub(t)
			hub.useKubeconfig("cl-b")
			hub.handle("GET "+dataPlanePrefix+"/components/api/process", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(w, map[string]any{"running": true})
			})
			hub.handle("GET "+instanceAPIPath, tc.instance)
			if tc.project != nil {
				hub.handle("GET "+appStudioPrefix+"/shop", tc.project)
			}
			_, err := runSandboxExec(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, hubTarget{}, "shop-dev", "api", []string{"ls"}, "", time.Minute)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if tc.notWant != "" && strings.Contains(err.Error(), tc.notWant) {
				t.Fatalf("err = %v, must not contain %q", err, tc.notWant)
			}
		})
	}
}

func TestPrintProcessStatusSyncLine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		want   string
	}{
		{name: "no syncEncodings", status: `{"running":true}`, want: "utf-8 only (binary files are not synced to this component)"},
		{name: "utf-8 only", status: `{"running":true,"syncEncodings":["utf-8"]}`, want: "utf-8 only (binary files are not synced to this component)"},
		{name: "base64", status: `{"running":true,"syncEncodings":["utf-8","base64"]}`, want: "utf-8, base64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := printProcessStatus(&buf, json.RawMessage(tc.status)); err != nil {
				t.Fatal(err)
			}
			var line string
			for _, l := range strings.Split(buf.String(), "\n") {
				if strings.HasPrefix(l, "Sync:") {
					line = l
				}
			}
			if line == "" || !strings.HasSuffix(line, tc.want) {
				t.Fatalf("Sync line = %q, want suffix %q; output:\n%s", line, tc.want, buf.String())
			}
			if tc.want == "utf-8, base64" && strings.Contains(line, "not synced") {
				t.Fatalf("Sync line = %q", line)
			}
		})
	}
}

func TestSandboxStatusCommand(t *testing.T) {
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")
	hub.handle("GET "+dataPlanePrefix+"/status", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"phase": "Ready", "url": "https://shop-dev.example.com", "conditions": []map[string]any{{"type": "Ready", "status": "True", "reason": "AllGood"}}})
	})
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(os.Stderr)
	root.SetArgs([]string{"sandbox", "status", "shop-dev", "--kubeconfig", path})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Phase:", "Ready", "https://shop-dev.example.com", "AllGood"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}
