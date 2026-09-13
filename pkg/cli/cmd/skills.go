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
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	pkgversion "github.com/faroshq/faros/pkg/version"
)

// faros skills: fetch the skills/ directory of the faros repository straight
// from GitHub and lay it down where Claude Code and Codex look for skills.
// Nothing is embedded in the binary, so an old CLI still installs the
// current skill; the source archive is read once per invocation and only
// the skills/ subtree is kept.

const (
	skillsDefaultRepo = "faroshq/faros"
	skillsDefaultRef  = "main"
	skillsSourceDir   = "skills"

	// skillsMarkerFile records what `faros skills install` wrote so a later
	// run can tell its own directories from ones the user authored.
	skillsMarkerFile = ".faros-skill.json"

	skillsMaxFileBytes  = 8 << 20
	skillsMaxTotalBytes = 64 << 20

	skillsTargetClaude = "claude"
	skillsTargetCodex  = "codex"
	skillsTargetAll    = "all"

	skillsScopeUser    = "user"
	skillsScopeProject = "project"
)

// skillsArchiveURL is the GitHub source archive for a branch, tag or commit.
// A package variable so tests can point it at a local server.
var skillsArchiveURL = func(repo, ref string) string {
	return fmt.Sprintf("https://codeload.github.com/%s/tar.gz/%s", repo, ref)
}

var skillNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

type skillFile struct {
	// Path is slash-separated and relative to the skill directory.
	Path string
	Data []byte
}

type skillPackage struct {
	Name        string
	Description string
	Files       []skillFile
}

func (p *skillPackage) size() int {
	n := 0
	for _, f := range p.Files {
		n += len(f.Data)
	}
	return n
}

type skillsArchive struct {
	Repo   string
	Ref    string
	Commit string
	Skills map[string]*skillPackage
}

func (a *skillsArchive) names() []string {
	names := make([]string, 0, len(a.Skills))
	for n := range a.Skills {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// skillsMarker is the sidecar written next to SKILL.md.
type skillsMarker struct {
	Repo        string    `json:"repo"`
	Ref         string    `json:"ref"`
	Commit      string    `json:"commit,omitempty"`
	Skill       string    `json:"skill"`
	InstalledAt time.Time `json:"installedAt"`
	CLIVersion  string    `json:"cliVersion,omitempty"`
	Files       []string  `json:"files"`
}

func newSkillsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Install agent skills from the faros repository into Claude Code and Codex",
		Long: `Fetch the skills/ directory of github.com/faroshq/faros (or another
repository with the same layout) and install each skill where Claude Code
and Codex discover them:

  Claude Code   ~/.claude/skills/<name>   (--scope user, the default)
                .claude/skills/<name>    (--scope project)
  Codex         ~/.agents/skills/<name>
                .agents/skills/<name>

The skills are read from GitHub at install time, so you always get the
current main branch (or the --ref you name) regardless of the CLI version.
Re-run install to update. Directories that faros installed are replaced;
directories you wrote yourself are left alone unless you pass --force.

Skills load when an agent session starts, so restart Claude Code or Codex
after installing.`,
	}
	cmd.AddCommand(newSkillsListCommand(), newSkillsInstallCommand())
	return cmd
}

func addSkillsSourceFlags(cmd *cobra.Command, repo, ref *string) {
	cmd.Flags().StringVar(repo, "repo", skillsDefaultRepo, "GitHub repository (owner/name) whose skills/ directory to read")
	cmd.Flags().StringVar(ref, "ref", skillsDefaultRef, "Branch, tag or commit to read")
}

func newSkillsListCommand() *cobra.Command {
	var repo, ref string
	out := newOutputFlags(outputJSON, outputYAML, outputName)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the skills available in the repository",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			archive, err := fetchSkillsArchive(cmd.Context(), repo, ref)
			if err != nil {
				return err
			}
			type row struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Files       int    `json:"files"`
				Bytes       int    `json:"bytes"`
				Repo        string `json:"repo"`
				Ref         string `json:"ref"`
				Commit      string `json:"commit,omitempty"`
			}
			rows := make([]row, 0, len(archive.Skills))
			for _, n := range archive.names() {
				p := archive.Skills[n]
				rows = append(rows, row{n, p.Description, len(p.Files), p.size(), archive.Repo, archive.Ref, archive.Commit})
			}
			w := cmd.OutOrStdout()
			switch {
			case out.structured():
				return out.printStructured(w, rows)
			case out.names():
				return printNames(w, archive.names())
			}
			t := &table{headers: []string{"NAME", "FILES", "DESCRIPTION"}}
			for _, r := range rows {
				t.add(r.Name, fmt.Sprint(r.Files), truncate(r.Description, 100))
			}
			return t.write(w)
		},
	}
	addSkillsSourceFlags(cmd, &repo, &ref)
	out.addFlag(cmd)
	return cmd
}

func newSkillsInstallCommand() *cobra.Command {
	var (
		repo, ref     string
		target, scope string
		dir           string
		force         bool
	)
	out := newOutputFlags(outputJSON, outputYAML)
	cmd := &cobra.Command{
		Use:   "install [skill...]",
		Short: "Install skills for Claude Code and Codex (all skills by default)",
		Example: `  faros skills install                          # every skill, for Claude Code and Codex, for this user
  faros skills install faros --target claude    # one skill, one client
  faros skills install --scope project          # into ./.claude/skills and ./.agents/skills
  faros skills install --dir ~/.cursor/skills   # any other directory
  faros skills install --ref v0.1.30            # pin to a tag`,
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := skillsTargets(target, scope, dir)
			if err != nil {
				return err
			}
			archive, err := fetchSkillsArchive(cmd.Context(), repo, ref)
			if err != nil {
				return err
			}
			names := archive.names()
			if len(args) > 0 {
				names = nil
				for _, a := range args {
					if _, ok := archive.Skills[a]; !ok {
						return fmt.Errorf("no skill %q under %s/ at %s@%s (have: %s)", a, skillsSourceDir, archive.Repo, archive.Ref, strings.Join(archive.names(), ", "))
					}
					names = append(names, a)
				}
			}

			type result struct {
				Skill  string `json:"skill"`
				Client string `json:"client"`
				Dir    string `json:"dir"`
				Files  int    `json:"files"`
				Repo   string `json:"repo"`
				Ref    string `json:"ref"`
				Commit string `json:"commit,omitempty"`
			}
			var results []result
			for _, t := range targets {
				for _, n := range names {
					dest, err := installSkill(archive, archive.Skills[n], t.dir, force)
					if err != nil {
						return err
					}
					results = append(results, result{n, t.client, dest, len(archive.Skills[n].Files), archive.Repo, archive.Ref, archive.Commit})
				}
			}
			w := cmd.OutOrStdout()
			if out.structured() {
				return out.printStructured(w, results)
			}
			src := archive.Repo + "@" + archive.Ref
			if archive.Commit != "" {
				src += " (" + shortCommit(archive.Commit) + ")"
			}
			_, _ = fmt.Fprintf(w, "Installed from %s:\n", src)
			for _, r := range results {
				_, _ = fmt.Fprintf(w, "  %-12s %-7s %s (%d files)\n", r.Skill, r.Client, r.Dir, r.Files)
			}
			_, _ = fmt.Fprintln(w, "Skills load at the next session start; restart Claude Code or Codex to pick them up.")
			return nil
		},
	}
	addSkillsSourceFlags(cmd, &repo, &ref)
	cmd.Flags().StringVar(&target, "target", skillsTargetAll, "Which client to install for: claude, codex or all")
	cmd.Flags().StringVar(&scope, "scope", skillsScopeUser, "user (home directory) or project (current directory)")
	cmd.Flags().StringVar(&dir, "dir", "", "Install into this directory instead of the client locations (one <dir>/<skill> per skill)")
	cmd.Flags().BoolVar(&force, "force", false, "Replace directories that were not installed by faros skills")
	_ = cmd.RegisterFlagCompletionFunc("target", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{skillsTargetClaude, skillsTargetCodex, skillsTargetAll}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("scope", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{skillsScopeUser, skillsScopeProject}, cobra.ShellCompDirectiveNoFileComp
	})
	out.addFlag(cmd)
	return cmd
}

type skillsTarget struct {
	client string
	dir    string
}

// skillsTargets resolves --target/--scope/--dir to the directories that
// receive one subdirectory per skill.
func skillsTargets(target, scope, dir string) ([]skillsTarget, error) {
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		return []skillsTarget{{client: "dir", dir: abs}}, nil
	}
	var base string
	switch scope {
	case skillsScopeUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolving the home directory: %w", err)
		}
		base = home
	case skillsScopeProject:
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		base = wd
	default:
		return nil, fmt.Errorf("unsupported --scope %q (want %s or %s)", scope, skillsScopeUser, skillsScopeProject)
	}
	claude := skillsTarget{client: skillsTargetClaude, dir: filepath.Join(base, ".claude", "skills")}
	codex := skillsTarget{client: skillsTargetCodex, dir: filepath.Join(base, ".agents", "skills")}
	switch target {
	case skillsTargetClaude:
		return []skillsTarget{claude}, nil
	case skillsTargetCodex:
		return []skillsTarget{codex}, nil
	case skillsTargetAll:
		return []skillsTarget{claude, codex}, nil
	}
	return nil, fmt.Errorf("unsupported --target %q (want %s, %s or %s)", target, skillsTargetClaude, skillsTargetCodex, skillsTargetAll)
}

// fetchSkillsArchive downloads the source archive for repo@ref and keeps
// the skills/ subtree.
func fetchSkillsArchive(ctx context.Context, repo, ref string) (*skillsArchive, error) {
	if !strings.Contains(repo, "/") || strings.Count(repo, "/") != 1 {
		return nil, fmt.Errorf("--repo must be owner/name, got %q", repo)
	}
	if ref == "" {
		return nil, errors.New("--ref must not be empty")
	}
	url := skillsArchiveURL(repo, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "faros-cli/"+pkgversion.Version)
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s has no branch, tag or commit %q (HTTP 404 from %s)", repo, ref, url)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	archive, err := readSkillsArchive(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	archive.Repo, archive.Ref = repo, ref
	if len(archive.Skills) == 0 {
		return nil, fmt.Errorf("%s@%s has no skills: no %s/<name>/SKILL.md in the archive", repo, ref, skillsSourceDir)
	}
	return archive, nil
}

// readSkillsArchive streams a GitHub-style tar.gz (one top-level directory,
// optionally a pax global header carrying the commit) and collects every
// regular file under <top>/skills/<name>/.
func readSkillsArchive(r io.Reader) (*skillsArchive, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close() //nolint:errcheck
	tr := tar.NewReader(gz)
	a := &skillsArchive{Skills: map[string]*skillPackage{}}
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			if c := strings.TrimSpace(hdr.PAXRecords["comment"]); c != "" {
				a.Commit = c
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(hdr.Name, "./"), "/", 4)
		if len(parts) < 4 || parts[1] != skillsSourceDir {
			continue
		}
		name, rel := parts[2], parts[3]
		if !skillNameRE.MatchString(name) || !safeSkillPath(rel) {
			return nil, fmt.Errorf("archive entry %q has an unsafe path", hdr.Name)
		}
		if hdr.Size > skillsMaxFileBytes {
			return nil, fmt.Errorf("%s is %d bytes, over the %d-byte per-file limit", hdr.Name, hdr.Size, skillsMaxFileBytes)
		}
		total += hdr.Size
		if total > skillsMaxTotalBytes {
			return nil, fmt.Errorf("skills exceed the %d-byte total limit", skillsMaxTotalBytes)
		}
		data, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", hdr.Name, err)
		}
		p := a.Skills[name]
		if p == nil {
			p = &skillPackage{Name: name}
			a.Skills[name] = p
		}
		p.Files = append(p.Files, skillFile{Path: rel, Data: data})
	}
	for name, p := range a.Skills {
		var doc []byte
		for _, f := range p.Files {
			if f.Path == "SKILL.md" {
				doc = f.Data
				break
			}
		}
		if doc == nil {
			// A directory under skills/ without SKILL.md is not a skill.
			delete(a.Skills, name)
			continue
		}
		p.Description = skillFrontmatter(doc)["description"]
		sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	}
	return a, nil
}

// safeSkillPath accepts slash-separated relative paths with no traversal.
func safeSkillPath(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\\\x00") {
		return false
	}
	if path.Clean(rel) != rel {
		return false
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// skillFrontmatter reads the flat key: value pairs between the leading ---
// fences. Only name and description are meaningful, and both are single
// lines in every skill this command installs; anything fancier is ignored.
func skillFrontmatter(doc []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(doc))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return out
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// installSkill writes p under base/<name>, replacing a directory this
// command installed earlier and refusing (without force) one it did not.
// Files land in a sibling temp directory first so a failed write never
// leaves a half-updated skill behind.
func installSkill(a *skillsArchive, p *skillPackage, base string, force bool) (string, error) {
	dest := filepath.Join(base, p.Name)
	if fi, err := os.Lstat(dest); err == nil {
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			return "", fmt.Errorf("%s is a symlink; remove it or install elsewhere with --dir", dest)
		case !fi.IsDir():
			return "", fmt.Errorf("%s exists and is not a directory", dest)
		}
		if _, err := os.Stat(filepath.Join(dest, skillsMarkerFile)); err != nil && !force {
			return "", fmt.Errorf("%s exists and was not installed by 'faros skills'; pass --force to replace it", dest)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(base, "."+p.Name+".faros-tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp) //nolint:errcheck

	marker := skillsMarker{
		Repo:        a.Repo,
		Ref:         a.Ref,
		Commit:      a.Commit,
		Skill:       p.Name,
		InstalledAt: time.Now().UTC().Truncate(time.Second),
		CLIVersion:  pkgversion.Version,
	}
	for _, f := range p.Files {
		target := filepath.Join(tmp, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(target, f.Data, 0o644); err != nil { //nolint:gosec
			return "", err
		}
		marker.Files = append(marker.Files, f.Path)
	}
	mb, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, skillsMarkerFile), append(mb, '\n'), 0o644); err != nil { //nolint:gosec
		return "", err
	}
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("replacing %s: %w", dest, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", fmt.Errorf("moving %s into place: %w", dest, err)
	}
	return dest, nil
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
