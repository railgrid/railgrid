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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarEntry struct {
	name string
	data string
	mode byte
}

// githubTarball mimics codeload.github.com: a pax global header carrying
// the commit, then every file under one top-level directory.
func githubTarball(t *testing.T, commit string, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if commit != "" {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": commit}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = tar.TypeReg
		}
		hdr := &tar.Header{Typeflag: mode, Name: e.name, Mode: 0o644, Size: int64(len(e.data))}
		if mode == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if mode == tar.TypeSymlink {
			hdr.Linkname = e.data
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if mode == tar.TypeReg {
			if _, err := tw.Write([]byte(e.data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const farosSkillDoc = "---\nname: faros\ndescription: Use when driving a faros hub as a user.\n---\n\n# faros\n"

func sampleEntries() []tarEntry {
	return []tarEntry{
		{name: "faros-main/", mode: tar.TypeDir},
		{name: "faros-main/README.md", data: "top-level readme, not a skill"},
		{name: "faros-main/skills/README.md", data: "about the skills dir, not a skill"},
		{name: "faros-main/skills/faros/", mode: tar.TypeDir},
		{name: "faros-main/skills/faros/SKILL.md", data: farosSkillDoc},
		{name: "faros-main/skills/faros/references/", mode: tar.TypeDir},
		{name: "faros-main/skills/faros/references/cli.md", data: "# cli\n"},
		{name: "faros-main/skills/faros/.claude-plugin/plugin.json", data: `{"name":"faros"}`},
		{name: "faros-main/skills/not-a-skill/notes.md", data: "no SKILL.md here"},
		{name: "faros-main/skills/kedge/SKILL.md", data: "---\nname: kedge\ndescription: \"Quoted description\"\n---\n"},
		{name: "faros-main/pkg/cli/cmd/skills.go", data: "package cmd"},
	}
}

// serveTarball points skillsArchiveURL at a test server for the test's
// lifetime and records the last requested path.
func serveTarball(t *testing.T, status int, body []byte) *string {
	t.Helper()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "faros-cli/") {
			t.Errorf("User-Agent = %q, want faros-cli/…", ua)
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	prev := skillsArchiveURL
	skillsArchiveURL = func(repo, ref string) string { return srv.URL + "/" + repo + "/tar.gz/" + ref }
	t.Cleanup(func() { skillsArchiveURL = prev })
	return &gotPath
}

func runSkills(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"skills"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestReadSkillsArchive(t *testing.T) {
	a, err := readSkillsArchive(bytes.NewReader(githubTarball(t, "0123456789abcdef0123456789abcdef01234567", sampleEntries())))
	if err != nil {
		t.Fatal(err)
	}
	if a.Commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("commit = %q", a.Commit)
	}
	if got := a.names(); strings.Join(got, ",") != "faros,kedge" {
		t.Errorf("skills = %v, want faros and kedge only (README.md and dirs without SKILL.md are not skills)", got)
	}
	f := a.Skills["faros"]
	if f.Description != "Use when driving a faros hub as a user." {
		t.Errorf("description = %q", f.Description)
	}
	var paths []string
	for _, file := range f.Files {
		paths = append(paths, file.Path)
	}
	if want := ".claude-plugin/plugin.json,SKILL.md,references/cli.md"; strings.Join(paths, ",") != want {
		t.Errorf("files = %v, want %s", paths, want)
	}
	if a.Skills["kedge"].Description != "Quoted description" {
		t.Errorf("quoted description not unwrapped: %q", a.Skills["kedge"].Description)
	}
}

func TestReadSkillsArchiveRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{
		"faros-main/skills/faros/../../etc/passwd",
		"faros-main/skills/faros/refs/../../x",
		"faros-main/skills/../x/SKILL.md",
		"faros-main/skills/Bad Name/SKILL.md",
		"faros-main/skills/faros//double",
	} {
		_, err := readSkillsArchive(bytes.NewReader(githubTarball(t, "", []tarEntry{{name: name, data: "x"}})))
		if err == nil || !strings.Contains(err.Error(), "unsafe path") {
			t.Errorf("%q: err = %v, want unsafe path", name, err)
		}
	}
	// Symlinks and other non-regular entries are skipped, never followed.
	a, err := readSkillsArchive(bytes.NewReader(githubTarball(t, "", []tarEntry{
		{name: "faros-main/skills/faros/SKILL.md", data: farosSkillDoc},
		{name: "faros-main/skills/faros/evil", data: "/etc/passwd", mode: tar.TypeSymlink},
	})))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(a.Skills["faros"].Files); n != 1 {
		t.Errorf("symlink was kept: %d files", n)
	}
}

func TestReadSkillsArchiveLimits(t *testing.T) {
	big := strings.Repeat("x", skillsMaxFileBytes+1)
	_, err := readSkillsArchive(bytes.NewReader(githubTarball(t, "", []tarEntry{{name: "faros-main/skills/faros/SKILL.md", data: big}})))
	if err == nil || !strings.Contains(err.Error(), "per-file limit") {
		t.Errorf("err = %v, want per-file limit", err)
	}
}

func TestSkillsListAndInstall(t *testing.T) {
	gotPath := serveTarball(t, http.StatusOK, githubTarball(t, "abcdef0123456789", sampleEntries()))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	out, err := runSkills(t, "list", "-o", "name", "--ref", "v9")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if out != "faros\nkedge\n" {
		t.Errorf("list -o name = %q", out)
	}
	if *gotPath != "/faroshq/faros/tar.gz/v9" {
		t.Errorf("fetched %q, want the v9 archive of the default repo", *gotPath)
	}

	out, err = runSkills(t, "install", "faros")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	for _, dir := range []string{
		filepath.Join(home, ".claude", "skills", "faros"),
		filepath.Join(home, ".agents", "skills", "faros"),
	} {
		doc, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			t.Fatalf("%s: %v\n%s", dir, err, out)
		}
		if string(doc) != farosSkillDoc {
			t.Errorf("%s: SKILL.md content differs", dir)
		}
		if _, err := os.Stat(filepath.Join(dir, "references", "cli.md")); err != nil {
			t.Errorf("%s: nested file missing: %v", dir, err)
		}
		mb, err := os.ReadFile(filepath.Join(dir, skillsMarkerFile))
		if err != nil {
			t.Fatalf("%s: marker: %v", dir, err)
		}
		var m skillsMarker
		if err := json.Unmarshal(mb, &m); err != nil {
			t.Fatal(err)
		}
		if m.Repo != "faroshq/faros" || m.Ref != "main" || m.Commit != "abcdef0123456789" || m.Skill != "faros" || len(m.Files) != 3 {
			t.Errorf("%s: marker = %+v", dir, m)
		}
		if !strings.Contains(out, dir) {
			t.Errorf("output does not mention %s:\n%s", dir, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "kedge")); !os.IsNotExist(err) {
		t.Errorf("kedge was installed although only faros was named")
	}
	if !strings.Contains(out, "faroshq/faros@main (abcdef012345)") {
		t.Errorf("output lacks the source line:\n%s", out)
	}

	// Re-running replaces what faros installed, dropping files that are gone.
	stale := filepath.Join(home, ".claude", "skills", "faros", "references", "old.md")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runSkills(t, "install", "--target", "claude"); err != nil {
		t.Fatalf("reinstall: %v\n%s", err, out)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file survived reinstall")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "kedge", "SKILL.md")); err != nil {
		t.Errorf("install with no names should install every skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "kedge")); !os.IsNotExist(err) {
		t.Errorf("--target claude touched the codex directory")
	}
}

func TestSkillsInstallRefusesForeignDirectory(t *testing.T) {
	serveTarball(t, http.StatusOK, githubTarball(t, "", sampleEntries()))
	dir := t.TempDir()
	mine := filepath.Join(dir, "faros")
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "SKILL.md"), []byte("hand-written"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runSkills(t, "install", "faros", "--dir", dir)
	if err == nil || !strings.Contains(err.Error(), "was not installed by 'faros skills'; pass --force") {
		t.Fatalf("err = %v, want refusal\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(mine, "SKILL.md")); string(b) != "hand-written" {
		t.Errorf("hand-written skill was modified")
	}

	if out, err := runSkills(t, "install", "faros", "--dir", dir, "--force", "-o", "json"); err != nil {
		t.Fatalf("--force: %v\n%s", err, out)
	} else {
		var res []map[string]any
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("json output: %v\n%s", err, out)
		}
		if len(res) != 1 || res[0]["client"] != "dir" || res[0]["dir"] != mine || res[0]["files"] != float64(3) {
			t.Errorf("json = %v", res)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(mine, "SKILL.md")); string(b) != farosSkillDoc {
		t.Errorf("--force did not replace the skill")
	}

	// A symlink (like the repo's own .agents/skills/faros) is never replaced.
	link := filepath.Join(t.TempDir(), "faros")
	if err := os.Symlink(mine, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := runSkills(t, "install", "faros", "--dir", filepath.Dir(link), "--force"); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Errorf("err = %v, want symlink refusal", err)
	}
}

func TestSkillsInstallErrors(t *testing.T) {
	serveTarball(t, http.StatusNotFound, nil)
	if _, err := runSkills(t, "install", "--ref", "nope", "--dir", t.TempDir()); err == nil || !strings.Contains(err.Error(), `no branch, tag or commit "nope"`) {
		t.Errorf("404: err = %v", err)
	}

	serveTarball(t, http.StatusOK, githubTarball(t, "", sampleEntries()))
	if _, err := runSkills(t, "install", "missing", "--dir", t.TempDir()); err == nil || !strings.Contains(err.Error(), `no skill "missing"`) || !strings.Contains(err.Error(), "have: faros, kedge") {
		t.Errorf("unknown skill: err = %v", err)
	}
	if _, err := runSkills(t, "install", "--target", "vim", "--dir", ""); err == nil || !strings.Contains(err.Error(), `unsupported --target "vim"`) {
		t.Errorf("bad target: err = %v", err)
	}
	if _, err := runSkills(t, "install", "--scope", "global"); err == nil || !strings.Contains(err.Error(), `unsupported --scope "global"`) {
		t.Errorf("bad scope: err = %v", err)
	}

	serveTarball(t, http.StatusOK, githubTarball(t, "", []tarEntry{{name: "faros-main/README.md", data: "x"}}))
	if _, err := runSkills(t, "list"); err == nil || !strings.Contains(err.Error(), "has no skills") {
		t.Errorf("empty archive: err = %v", err)
	}
}

func TestSkillsTargetsProjectScope(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	targets, err := skillsTargets(skillsTargetAll, skillsScopeProject, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].dir != filepath.Join(wd, ".claude", "skills") || targets[1].dir != filepath.Join(wd, ".agents", "skills") {
		t.Errorf("targets = %+v", targets)
	}
}
