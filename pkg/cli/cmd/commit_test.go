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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestBuildCommitMessage(t *testing.T) {
	if got := buildCommitMessage(nil); got != "" {
		t.Fatalf("empty: %q", got)
	}
	if got := buildCommitMessage([]string{"Add cart"}); got != "Add cart" {
		t.Fatalf("single: %q", got)
	}
	got := buildCommitMessage([]string{"Add cart", " ", "Fix totals", "Style checkout"})
	want := "Add cart\n\n- Fix totals\n- Style checkout"
	if got != want {
		t.Fatalf("multi: %q, want %q", got, want)
	}

	// 300 two-byte runes: the cap must cut on a rune boundary within 512 bytes.
	long := buildCommitMessage([]string{strings.Repeat("é", 300)})
	if len(long) > commitMessageLimit || !utf8.ValidString(long) {
		t.Fatalf("long message: %d bytes, valid=%v", len(long), utf8.ValidString(long))
	}
	if len(long) != 512 {
		t.Fatalf("long message: %d bytes, want 512", len(long))
	}
	odd := buildCommitMessage([]string{"x" + strings.Repeat("é", 300)})
	if len(odd) != 511 || !utf8.ValidString(odd) {
		t.Fatalf("odd-offset message: %d bytes, valid=%v", len(odd), utf8.ValidString(odd))
	}
}

func TestParseRawDiff(t *testing.T) {
	out := ":100644 100644 aaa bbb M\x00src/app.js\x00" +
		":000000 100644 000 ccc A\x00new file.txt\x00" +
		":100644 000000 ddd 000 D\x00old.txt\x00"
	got, err := parseRawDiff([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []diffEntry{
		{OldMode: "100644", NewMode: "100644", OldSHA: "aaa", NewSHA: "bbb", Status: "M", Path: "src/app.js"},
		{OldMode: "000000", NewMode: "100644", OldSHA: "000", NewSHA: "ccc", Status: "A", Path: "new file.txt"},
		{OldMode: "100644", NewMode: "000000", OldSHA: "ddd", NewSHA: "000", Status: "D", Path: "old.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if _, err := parseRawDiff([]byte("garbage\x00")); err == nil {
		t.Fatal("expected an error for malformed output")
	}
}

func TestBuildCommitPayload(t *testing.T) {
	blobs := map[string][]byte{
		"b1": []byte("console.log(1)\n"),
		"b2": []byte("# readme\n"),
		"b3": {0x89, 'P', 'N', 'G', 0, 1},
		"b4": []byte("#!/bin/sh\n"),
	}
	read := func(sha string) ([]byte, error) {
		b, ok := blobs[sha]
		if !ok {
			return nil, fmt.Errorf("no blob %s", sha)
		}
		return b, nil
	}

	entries := []diffEntry{
		{OldMode: "100644", NewMode: "100644", NewSHA: "b1", Status: "M", Path: "api/index.js"},
		{OldMode: "000000", NewMode: "100644", NewSHA: "b2", Status: "A", Path: "README.md"},
		{OldMode: "100644", NewMode: "000000", Status: "D", Path: "old.txt"},
		{OldMode: "000000", NewMode: "100755", NewSHA: "b4", Status: "A", Path: "run.sh"},
	}
	args, warnings, err := buildCommitPayload("shop", "main", "Add cart", entries, read)
	if err != nil {
		t.Fatal(err)
	}
	want := commitFilesArgs{
		RepositoryRef: "shop", Branch: "main", Message: "Add cart",
		Files: []commitFileArg{
			{Path: "api/index.js", Content: "console.log(1)\n"},
			{Path: "README.md", Content: "# readme\n"},
			{Path: "run.sh", Content: "#!/bin/sh\n"},
		},
		DeletePaths: []string{"old.txt"},
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("got %+v\nwant %+v", args, want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "run.sh is executable") {
		t.Fatalf("warnings = %v", warnings)
	}

	// Binary content travels base64-encoded; gating on the tool schema
	// happens at send time.
	bin, _, err := buildCommitPayload("shop", "main", "m", []diffEntry{{NewMode: "100644", NewSHA: "b3", Status: "A", Path: "logo.png"}}, read)
	if err != nil {
		t.Fatal(err)
	}
	wantBin := []commitFileArg{{Path: "logo.png", Content: base64.StdEncoding.EncodeToString(blobs["b3"]), Encoding: "base64"}}
	if !reflect.DeepEqual(bin.Files, wantBin) {
		t.Fatalf("binary files = %+v, want %+v", bin.Files, wantBin)
	}
	if got := (&commitPlan{args: bin}).binaryPaths(); !reflect.DeepEqual(got, []string{"logo.png"}) {
		t.Fatalf("binaryPaths = %v", got)
	}

	for name, e := range map[string]diffEntry{
		"is a symlink": {NewMode: "120000", NewSHA: "b1", Status: "A", Path: "link"},
		"submodule":    {NewMode: "160000", NewSHA: "b1", Status: "A", Path: "vendor/x"},
	} {
		if _, _, err := buildCommitPayload("shop", "main", "m", []diffEntry{e}, read); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: err = %v", e.Path, err)
		}
	}
}

// gitTest runs git in dir with a fixed identity, failing the test on error.
func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newCommitTestRepo creates a bare "origin" with one commit on main and a
// clone of it, returning the clone's path.
func newCommitTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	clone := filepath.Join(root, "clone")
	gitTest(t, root, "init", "-q", "--bare", "-b", "main", origin)
	gitTest(t, root, "clone", "-q", origin, clone)
	writeTestFile(t, filepath.Join(clone, "keep.txt"), "keep\n")
	writeTestFile(t, filepath.Join(clone, "old.txt"), "old\n")
	writeTestFile(t, filepath.Join(clone, "api", "index.js"), "v1\n")
	gitTest(t, clone, "add", "-A")
	gitTest(t, clone, "commit", "-q", "-m", "scaffold")
	gitTest(t, clone, "push", "-q", "origin", "HEAD:main")
	gitTest(t, clone, "branch", "-q", "--set-upstream-to=origin/main")
	return clone
}

func TestPlanCommitFromGit(t *testing.T) {
	clone := newCommitTestRepo(t)
	g := gitRunner{dir: clone}
	ctx := context.Background()

	plan, err := planCommit(ctx, g, "shop", "main", "origin")
	if err != nil || plan != nil {
		t.Fatalf("up to date: plan=%v err=%v", plan, err)
	}

	writeTestFile(t, filepath.Join(clone, "api", "index.js"), "v2\n")
	writeTestFile(t, filepath.Join(clone, "web", "app.js"), "app\n")
	if err := os.Remove(filepath.Join(clone, "old.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := planCommit(ctx, g, "shop", "main", "origin"); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty tree: err = %v", err)
	}
	gitTest(t, clone, "add", "-A")
	gitTest(t, clone, "commit", "-q", "-m", "Bump api")
	writeTestFile(t, filepath.Join(clone, "web", "app.js"), "app v2\n")
	gitTest(t, clone, "commit", "-q", "-am", "Tweak web")

	plan, err = planCommit(ctx, g, "shop", "main", "origin")
	if err != nil {
		t.Fatal(err)
	}
	want := commitFilesArgs{
		RepositoryRef: "shop", Branch: "main", Message: "Bump api\n\n- Tweak web",
		Files: []commitFileArg{
			{Path: "api/index.js", Content: "v2\n"},
			{Path: "web/app.js", Content: "app v2\n"},
		},
		DeletePaths: []string{"old.txt"},
	}
	if !reflect.DeepEqual(plan.args, want) {
		t.Fatalf("got %+v\nwant %+v", plan.args, want)
	}
}

func TestPlanCommitRejectsDivergedBase(t *testing.T) {
	clone := newCommitTestRepo(t)
	// Someone else lands a commit upstream.
	other := filepath.Join(t.TempDir(), "other")
	gitTest(t, filepath.Dir(other), "clone", "-q", filepath.Join(filepath.Dir(clone), "origin.git"), other)
	writeTestFile(t, filepath.Join(other, "keep.txt"), "theirs\n")
	gitTest(t, other, "commit", "-q", "-am", "Upstream change")
	gitTest(t, other, "push", "-q", "origin", "HEAD:main")

	writeTestFile(t, filepath.Join(clone, "api", "index.js"), "mine\n")
	gitTest(t, clone, "commit", "-q", "-am", "Local change")

	_, err := planCommit(context.Background(), gitRunner{dir: clone}, "shop", "main", "origin")
	if err == nil || !strings.Contains(err.Error(), "git rebase origin/main") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildCommitPayloadLimits(t *testing.T) {
	// One shared zero-filled buffer (NUL makes it binary); entries slice it.
	buf := make([]byte, binaryFileLimit+1)
	read := func(sha string) ([]byte, error) {
		switch sha {
		case "at-limit":
			return buf[:binaryFileLimit], nil
		case "over-limit":
			return buf, nil
		case "text":
			return bytes.Repeat([]byte("a"), 1<<20), nil
		}
		return nil, fmt.Errorf("no blob %s", sha)
	}
	entry := func(path, sha string) diffEntry {
		return diffEntry{OldMode: "000000", NewMode: "100644", NewSHA: sha, Status: "A", Path: path}
	}

	args, _, err := buildCommitPayload("shop", "main", "m", []diffEntry{entry("a.bin", "at-limit")}, read)
	if err != nil {
		t.Fatalf("25 MiB binary: %v", err)
	}
	if got, err := base64.StdEncoding.DecodeString(args.Files[0].Content); err != nil || len(got) != binaryFileLimit {
		t.Fatalf("decoded %d bytes, err %v", len(got), err)
	}

	if _, _, err := buildCommitPayload("shop", "main", "m", []diffEntry{entry("big.bin", "over-limit")}, read); err == nil || !strings.Contains(err.Error(), "big.bin") || !strings.Contains(err.Error(), "25 MiB") {
		t.Fatalf("over per-file limit: err = %v", err)
	}

	// 25 + 25 = 50 MiB of binaries exceeds the 48 MiB total.
	_, _, err = buildCommitPayload("shop", "main", "m", []diffEntry{entry("a.bin", "at-limit"), entry("b.bin", "at-limit")}, read)
	if err == nil || !strings.Contains(err.Error(), "48 MiB") || !strings.Contains(err.Error(), "b.bin") {
		t.Fatalf("over total limit: err = %v", err)
	}
}

func TestSchemaFileItemsHaveEncoding(t *testing.T) {
	for name, tc := range map[string]struct {
		schema string
		want   bool
	}{
		"inline encoding": {`{"type":"object","properties":{"files":{"type":"array","items":{"type":"object","properties":{"path":{},"content":{},"encoding":{"enum":["utf-8","base64"]}}}}}}`, true},
		"$defs ref":       {`{"type":"object","$defs":{"f":{"type":"object","properties":{"path":{},"encoding":{}}}},"properties":{"files":{"type":"array","items":{"$ref":"#/$defs/f"}}}}`, true},
		"text only":       {`{"type":"object","properties":{"files":{"type":"array","items":{"type":"object","properties":{"path":{},"content":{}}}}}}`, false},
		"top-level only":  {`{"type":"object","properties":{"encoding":{},"files":{"type":"array"}}}`, false},
		"dangling ref":    {`{"properties":{"files":{"items":{"$ref":"#/$defs/missing"}}}}`, false},
		"not json":        {`nope`, false},
		"empty":           {``, false},
	} {
		if got := schemaFileItemsHaveEncoding(json.RawMessage(tc.schema)); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

// fakeCommitMCP serves tools/list (with or without the files[].encoding
// property) and code__commit_files on the fake hub's /mcp. A successful
// commit pushes the clone's HEAD to its origin, standing in for the provider.
type fakeCommitMCP struct {
	mu        sync.Mutex
	listCalls int
	calls     []commitFilesArgs
}

func (f *fakeCommitMCP) install(t *testing.T, hub *fakeHub, clone string, withEncoding bool) {
	itemProps := map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}
	if withEncoding {
		itemProps["encoding"] = map[string]any{"type": "string", "enum": []string{"utf-8", "base64"}}
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"repositoryRef": map[string]any{"type": "string"},
		"files":         map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": itemProps}},
	}}
	hub.handle("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		var result any
		switch req.Method {
		case "tools/list":
			f.listCalls++
			result = map[string]any{"tools": []map[string]any{
				{"name": "infrastructure__list_instances", "inputSchema": map[string]any{"type": "object"}},
				{"name": commitFilesTool, "inputSchema": schema},
			}}
		case "tools/call":
			var args commitFilesArgs
			_ = json.Unmarshal(req.Params.Arguments, &args)
			f.calls = append(f.calls, args)
			c := exec.Command("git", "push", "-q", "origin", "HEAD:main")
			c.Dir = clone
			if out, err := c.CombinedOutput(); err != nil {
				t.Errorf("fake provider push: %v\n%s", err, out)
			}
			text, _ := json.Marshal(map[string]any{"phase": "Succeeded", "commitSHA": "abc123"})
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}
		}
		writeTestJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	})
}

func commitBinaryInClone(t *testing.T, clone string) []byte {
	t.Helper()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0x0d, 0xff, 0xfe}
	if err := os.WriteFile(filepath.Join(clone, "logo.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(clone, "api", "index.js"), "v2\n")
	gitTest(t, clone, "add", "-A")
	gitTest(t, clone, "commit", "-q", "-m", "Add logo")
	return png
}

func TestRunCommitSendsBinaryWhenToolSupportsEncoding(t *testing.T) {
	clone := newCommitTestRepo(t)
	png := commitBinaryInClone(t, clone)
	t.Chdir(clone)
	hub := newFakeHub(t)
	hub.useKubeconfig("cl-b")
	mcp := &fakeCommitMCP{}
	mcp.install(t, hub, clone, true)

	var out, errOut bytes.Buffer
	if err := runCommit(context.Background(), &out, &errOut, hubTarget{}, "shop", "main", "origin", false); err != nil {
		t.Fatalf("runCommit: %v\n%s", err, errOut.String())
	}
	if mcp.listCalls != 1 || len(mcp.calls) != 1 {
		t.Fatalf("tools/list calls = %d, commit calls = %d", mcp.listCalls, len(mcp.calls))
	}
	want := []commitFileArg{
		{Path: "api/index.js", Content: "v2\n"},
		{Path: "logo.png", Content: base64.StdEncoding.EncodeToString(png), Encoding: "base64"},
	}
	if !reflect.DeepEqual(mcp.calls[0].Files, want) {
		t.Fatalf("files = %+v, want %+v", mcp.calls[0].Files, want)
	}
	if strings.TrimSpace(out.String()) != "abc123" {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestRunCommitRejectsBinaryWhenToolLacksEncoding(t *testing.T) {
	clone := newCommitTestRepo(t)
	commitBinaryInClone(t, clone)
	t.Chdir(clone)
	hub := newFakeHub(t)
	hub.useKubeconfig("cl-b")
	mcp := &fakeCommitMCP{}
	mcp.install(t, hub, clone, false)

	err := runCommit(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, hubTarget{}, "shop", "main", "origin", false)
	if err == nil || !strings.Contains(err.Error(), "doesn't support binary files yet") || !strings.Contains(err.Error(), "logo.png") {
		t.Fatalf("err = %v", err)
	}
	if len(mcp.calls) != 0 {
		t.Fatalf("commit_files was called %d time(s) despite no binary support", len(mcp.calls))
	}
}

func TestRunCommitTextOnlySkipsSchemaCheck(t *testing.T) {
	clone := newCommitTestRepo(t)
	writeTestFile(t, filepath.Join(clone, "api", "index.js"), "v2\n")
	gitTest(t, clone, "commit", "-q", "-am", "Bump api")
	t.Chdir(clone)
	hub := newFakeHub(t)
	hub.useKubeconfig("cl-b")
	mcp := &fakeCommitMCP{}
	mcp.install(t, hub, clone, false)

	if err := runCommit(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, hubTarget{}, "shop", "main", "origin", false); err != nil {
		t.Fatal(err)
	}
	if mcp.listCalls != 0 || len(mcp.calls) != 1 {
		t.Fatalf("tools/list calls = %d, commit calls = %d; a text-only commit needs no schema check", mcp.listCalls, len(mcp.calls))
	}
}
