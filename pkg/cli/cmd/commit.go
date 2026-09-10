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
	"io"
	"net/http"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

// commitMessageLimit is the RepositoryCommit CRD's spec.message cap. Bytes
// and characters coincide for ASCII; truncation stays on a rune boundary so
// the result is within the limit either way.
const commitMessageLimit = 512

// Binary file transport, shared by 'faros commit' and 'faros sandbox sync':
// a file that is not UTF-8 text travels base64-encoded (standard alphabet,
// padded) with encoding "base64". Limits count the raw (decoded) bytes.
const (
	encodingBase64 = "base64"
	// binaryFileLimit caps one binary file (25 MiB).
	binaryFileLimit = 25 << 20
	// payloadTotalLimit caps all files of one commit or sync (48 MiB).
	payloadTotalLimit = 48 << 20
)

// commitFilesTool is the aggregate MCP name of the code provider's
// commit_files tool.
const commitFilesTool = "code__commit_files"

// commitFilesArgs mirrors the code provider's commit_files tool input.
type commitFilesArgs struct {
	RepositoryRef string          `json:"repositoryRef"`
	Branch        string          `json:"branch,omitempty"`
	Message       string          `json:"message,omitempty"`
	Files         []commitFileArg `json:"files,omitempty"`
	DeletePaths   []string        `json:"deletePaths,omitempty"`
}

// commitFileArg is one file entry. Encoding is omitted for UTF-8 text and is
// "base64" for binary content, which is only sent when the tool's input
// schema declares the encoding property.
type commitFileArg struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
}

// commitFilesResult mirrors the fields of the commit_files tool output that
// the command checks.
type commitFilesResult struct {
	RepositoryRef string `json:"repositoryRef"`
	Name          string `json:"name"`
	Phase         string `json:"phase"`
	CommitSHA     string `json:"commitSHA"`
	CommitURL     string `json:"commitURL"`
	Branch        string `json:"branch"`
}

// diffEntry is one line of `git diff --raw --no-renames --no-abbrev -z`.
type diffEntry struct {
	OldMode string
	NewMode string
	OldSHA  string
	NewSHA  string
	Status  string
	Path    string
}

func newCommitCommand() *cobra.Command {
	var target hubTarget
	var branch, remote string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "commit <repositoryRef>",
		Short: "Record local git commits through faros (code__commit_files)",
		Long: `Send the commits on HEAD that are not yet on <remote>/<branch> to the
code provider's commit_files tool, so the commit is faros-recorded and App
Studio can build and promote it. Never 'git push' to a faros-managed repo.

Run inside a clone of the repository:

  git add -A && git commit -m "Add cart"     # commit locally, do not push
  faros commit shop                          # <repositoryRef> is the code Repository name

Every file that differs between <remote>/<branch> and HEAD is sent (deletions
as deletePaths), with the local commit subjects as the message (capped at 512
characters). Binary files are sent base64-encoded (at most 25 MiB each, 48 MiB
per commit) when the hub's code provider supports them; otherwise the command
refuses the change. When faros reports Succeeded the command
fetches, checks that <remote>/<branch> now has exactly your HEAD tree, and
resets the local branch onto it, so your clone carries the faros-recorded SHA.
The commit SHA is printed on stdout.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommit(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), target, args[0], branch, remote, dryRun)
		},
	}
	target.addFlags(cmd)
	cmd.Flags().StringVar(&branch, "branch", "main", "Branch to commit to")
	cmd.Flags().StringVar(&remote, "remote", "origin", "Git remote that tracks the faros-managed repository")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would be sent without calling faros")
	return cmd
}

func runCommit(ctx context.Context, out, errOut io.Writer, target hubTarget, repo, branch, remote string, dryRun bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	g := gitRunner{}
	plan, err := planCommit(ctx, g, repo, branch, remote)
	if err != nil {
		return err
	}
	if plan == nil {
		_, _ = fmt.Fprintf(errOut, "faros commit: nothing to send; HEAD matches %s/%s\n", remote, branch)
		return nil
	}
	for _, w := range plan.warnings {
		_, _ = fmt.Fprintf(errOut, "faros commit: warning: %s\n", w)
	}
	_, _ = fmt.Fprintf(errOut, "faros commit: sending %d file(s), %d deletion(s) to %s@%s\n", len(plan.args.Files), len(plan.args.DeletePaths), repo, branch)
	if dryRun {
		for _, f := range plan.args.Files {
			if f.Encoding == encodingBase64 {
				_, _ = fmt.Fprintf(out, "write  %s (%d bytes, binary)\n", f.Path, base64.StdEncoding.DecodedLen(len(f.Content)))
				continue
			}
			_, _ = fmt.Fprintf(out, "write  %s (%d bytes)\n", f.Path, len(f.Content))
		}
		for _, p := range plan.args.DeletePaths {
			_, _ = fmt.Fprintf(out, "delete %s\n", p)
		}
		_, _ = fmt.Fprintf(out, "\nmessage:\n%s\n", plan.args.Message)
		return nil
	}

	s, err := newHubSession(ctx, target)
	if err != nil {
		return err
	}
	mcp, err := s.newMCPClient(ctx)
	if err != nil {
		return err
	}
	if binaries := plan.binaryPaths(); len(binaries) > 0 {
		ok, err := mcp.toolFileEncodingSupported(ctx, commitFilesTool)
		if err != nil {
			return fmt.Errorf("checking whether %s accepts binary files: %w", commitFilesTool, err)
		}
		if !ok {
			return fmt.Errorf("%s: binary file(s) not supported: the hub's code provider doesn't support binary files yet (%s accepts UTF-8 text only) — drop them from this change or commit them another way",
				strings.Join(binaries, ", "), commitFilesTool)
		}
	}
	raw, err := mcp.callTool(ctx, commitFilesTool, plan.args)
	if err != nil {
		return fmt.Errorf("commit_files failed: %w", err)
	}
	var res commitFilesResult
	_ = json.Unmarshal(raw, &res)
	if res.Phase != "Succeeded" || res.CommitSHA == "" {
		return fmt.Errorf("commit not confirmed (phase=%s); result: %s\ncheck: kubectl get repositorycommits.code.faros.sh -l code.faros.sh/repository=%s",
			formatStringOrDash(res.Phase), strings.TrimSpace(string(raw)), repo)
	}

	base := remote + "/" + branch
	if _, err := g.run(ctx, "fetch", "-q", remote, branch); err != nil {
		return fmt.Errorf("recorded %s, but fetching it failed: %w", res.CommitSHA, err)
	}
	headTree, err := g.revParse(ctx, "HEAD^{tree}")
	if err != nil {
		return err
	}
	baseTree, err := g.revParse(ctx, base+"^{tree}")
	if err != nil {
		return err
	}
	if headTree != baseTree {
		return fmt.Errorf("recorded %s, but %s's tree differs from your HEAD (someone else committed, or a file mode was not preserved); left your branch alone — reconcile with 'git rebase %s'", res.CommitSHA, base, base)
	}
	if _, err := g.run(ctx, "reset", "-q", "--hard", base); err != nil {
		return fmt.Errorf("recorded %s, but resetting onto %s failed: %w", res.CommitSHA, base, err)
	}
	if res.CommitURL != "" {
		_, _ = fmt.Fprintf(errOut, "faros commit: recorded %s (%s); local branch reset onto %s\n", res.CommitSHA, res.CommitURL, base)
	} else {
		_, _ = fmt.Fprintf(errOut, "faros commit: recorded %s; local branch reset onto %s\n", res.CommitSHA, base)
	}
	_, err = fmt.Fprintln(out, res.CommitSHA)
	return err
}

type commitPlan struct {
	args     commitFilesArgs
	warnings []string
}

// binaryPaths lists the files the plan sends base64-encoded.
func (p *commitPlan) binaryPaths() []string {
	var out []string
	for _, f := range p.args.Files {
		if f.Encoding == encodingBase64 {
			out = append(out, f.Path)
		}
	}
	return out
}

// planCommit gathers the change set between <remote>/<branch> and HEAD. It
// returns nil (and no error) when there is nothing to send.
func planCommit(ctx context.Context, g gitRunner, repo, branch, remote string) (*commitPlan, error) {
	status, err := g.run(ctx, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(status)) > 0 {
		return nil, fmt.Errorf("uncommitted changes; run 'git add -A && git commit' first")
	}
	if _, err := g.run(ctx, "fetch", "-q", remote, branch); err != nil {
		return nil, err
	}
	base := remote + "/" + branch
	headTree, err := g.revParse(ctx, "HEAD^{tree}")
	if err != nil {
		return nil, err
	}
	baseTree, err := g.revParse(ctx, base+"^{tree}")
	if err != nil {
		return nil, err
	}
	if headTree == baseTree {
		return nil, nil
	}
	// A diff against a base HEAD does not contain would revert whatever
	// landed upstream since, so require HEAD to be ahead of it.
	if _, err := g.run(ctx, "merge-base", "--is-ancestor", base, "HEAD"); err != nil {
		return nil, fmt.Errorf("HEAD does not contain %s (it moved upstream); run 'git rebase %s' first", base, base)
	}
	subjects, err := g.run(ctx, "log", "--reverse", "--format=%s", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	rawDiff, err := g.run(ctx, "diff", "--raw", "--no-renames", "--no-abbrev", "-z", base, "HEAD")
	if err != nil {
		return nil, err
	}
	entries, err := parseRawDiff(rawDiff)
	if err != nil {
		return nil, err
	}
	args, warnings, err := buildCommitPayload(repo, branch, buildCommitMessage(splitLines(string(subjects))), entries, func(sha string) ([]byte, error) {
		return g.run(ctx, "cat-file", "blob", sha)
	})
	if err != nil {
		return nil, err
	}
	return &commitPlan{args: args, warnings: warnings}, nil
}

// buildCommitMessage joins local commit subjects (oldest first): the first
// becomes the title and the rest a bullet list, capped at commitMessageLimit.
func buildCommitMessage(subjects []string) string {
	var clean []string
	for _, s := range subjects {
		if s = strings.TrimSpace(s); s != "" {
			clean = append(clean, s)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	msg := clean[0]
	if len(clean) > 1 {
		msg += "\n\n- " + strings.Join(clean[1:], "\n- ")
	}
	return truncateUTF8(msg, commitMessageLimit)
}

// truncateUTF8 cuts s to at most limit bytes without splitting a rune, and
// drops trailing whitespace left by the cut.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " \t\r\n")
}

// parseRawDiff parses `git diff --raw -z` output: ":<old mode> <new mode>
// <old sha> <new sha> <status>\0<path>\0" per entry (renames disabled).
func parseRawDiff(out []byte) ([]diffEntry, error) {
	fields := strings.Split(string(out), "\x00")
	var entries []diffEntry
	for i := 0; i < len(fields); i++ {
		meta := fields[i]
		if meta == "" {
			continue
		}
		if !strings.HasPrefix(meta, ":") {
			return nil, fmt.Errorf("unexpected git diff output near %q", meta)
		}
		parts := strings.Fields(strings.TrimPrefix(meta, ":"))
		if len(parts) != 5 {
			return nil, fmt.Errorf("unexpected git diff entry %q", meta)
		}
		if i+1 >= len(fields) || fields[i+1] == "" {
			return nil, fmt.Errorf("git diff entry %q has no path", meta)
		}
		i++
		entries = append(entries, diffEntry{
			OldMode: parts[0], NewMode: parts[1], OldSHA: parts[2], NewSHA: parts[3],
			Status: parts[4][:1], Path: fields[i],
		})
	}
	return entries, nil
}

// buildCommitPayload turns diff entries into commit_files arguments, reading
// each written file's content with readBlob(newSHA). Symlinks and submodules
// are rejected because the tool writes regular files only. Content that is
// not UTF-8 text is base64-encoded (encoding "base64"); whether the hub's
// code provider accepts that is checked before sending. Limits count raw
// bytes: binaryFileLimit per binary file, payloadTotalLimit per commit.
func buildCommitPayload(repo, branch, message string, entries []diffEntry, readBlob func(sha string) ([]byte, error)) (commitFilesArgs, []string, error) {
	args := commitFilesArgs{RepositoryRef: repo, Branch: branch, Message: message}
	var warnings []string
	var total int64
	for _, e := range entries {
		switch e.Status {
		case "D":
			args.DeletePaths = append(args.DeletePaths, e.Path)
			continue
		case "A", "M", "T":
		default:
			return commitFilesArgs{}, nil, fmt.Errorf("%s: unsupported change status %q", e.Path, e.Status)
		}
		switch e.NewMode {
		case "120000":
			return commitFilesArgs{}, nil, fmt.Errorf("%s is a symlink; commit_files writes regular text files only", e.Path)
		case "160000":
			return commitFilesArgs{}, nil, fmt.Errorf("%s is a submodule; commit_files writes regular text files only", e.Path)
		case "100755":
			if e.OldMode != "100755" {
				warnings = append(warnings, fmt.Sprintf("%s is executable; commit_files may not preserve the mode, which fails the final tree check", e.Path))
			}
		}
		content, err := readBlob(e.NewSHA)
		if err != nil {
			return commitFilesArgs{}, nil, fmt.Errorf("reading %s: %w", e.Path, err)
		}
		total += int64(len(content))
		if total > payloadTotalLimit {
			return commitFilesArgs{}, nil, fmt.Errorf("change is over %s in total (reached at %s); split it into smaller commits", formatMiB(payloadTotalLimit), e.Path)
		}
		if isUTF8Text(content) {
			args.Files = append(args.Files, commitFileArg{Path: e.Path, Content: string(content)})
			continue
		}
		if len(content) > binaryFileLimit {
			return commitFilesArgs{}, nil, fmt.Errorf("%s is binary and %s; binary files are limited to %s each", e.Path, formatMiB(int64(len(content))), formatMiB(binaryFileLimit))
		}
		args.Files = append(args.Files, commitFileArg{Path: e.Path, Content: base64.StdEncoding.EncodeToString(content), Encoding: encodingBase64})
	}
	if len(args.Files) == 0 && len(args.DeletePaths) == 0 {
		return commitFilesArgs{}, nil, fmt.Errorf("no file changes between the base and HEAD")
	}
	return args, warnings, nil
}

// isUTF8Text reports whether content is valid UTF-8 without NUL bytes — what
// survives a JSON string round trip unchanged.
func isUTF8Text(content []byte) bool {
	return utf8.Valid(content) && bytes.IndexByte(content, 0) < 0
}

// formatMiB renders a byte count for limit messages, e.g. "25 MiB" or
// "25.3 MiB".
func formatMiB(n int64) string {
	if n%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// toolFileEncodingSupported reports whether the named tool's input schema
// (from tools/list) declares an "encoding" property on its files[] items —
// the code provider's signal that it accepts base64 file content. A tool
// that is missing or has no such property is reported as unsupported.
func (c *mcpClient) toolFileEncodingSupported(ctx context.Context, name string) (bool, error) {
	schema, found, err := c.toolInputSchema(ctx, name)
	if err != nil || !found {
		return false, err
	}
	return schemaFileItemsHaveEncoding(schema), nil
}

// toolInputSchema fetches tools/list and returns the named tool's
// inputSchema. The aggregate answers statelessly, so no initialize is needed.
func (c *mcpClient) toolInputSchema(ctx context.Context, name string) (json.RawMessage, bool, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", cliUserAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("listing MCP tools: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	reply, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, false, fmt.Errorf("reading tools/list reply: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, false, decodeAPIError(http.MethodPost, req.URL.Path, resp.StatusCode, reply)
	}
	for _, payload := range jsonRPCPayloads(reply) {
		var msg struct {
			Result *struct {
				Tools []struct {
					Name        string          `json:"name"`
					InputSchema json.RawMessage `json:"inputSchema"`
				} `json:"tools"`
			} `json:"result"`
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}
		if msg.Error != nil {
			return nil, false, fmt.Errorf("tools/list: MCP error %d: %s", msg.Error.Code, msg.Error.Message)
		}
		if msg.Result == nil {
			continue
		}
		for _, t := range msg.Result.Tools {
			if t.Name == name {
				return t.InputSchema, true, nil
			}
		}
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("no JSON-RPC result in tools/list reply")
}

// schemaFileItemsHaveEncoding reports whether a JSON Schema has
// properties.files.items.properties.encoding, following local "#/..." $refs.
func schemaFileItemsHaveEncoding(schema json.RawMessage) bool {
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		return false
	}
	node := root
	for _, key := range []string{"properties", "files", "items", "properties"} {
		node = resolveSchemaRef(root, node)
		if node == nil {
			return false
		}
		node, _ = node[key].(map[string]any)
	}
	_, ok := node["encoding"]
	return ok
}

// resolveSchemaRef follows a local JSON pointer $ref ("#/$defs/x") in node,
// a few levels deep; anything else is returned unchanged.
func resolveSchemaRef(root, node map[string]any) map[string]any {
	for i := 0; i < 8 && node != nil; i++ {
		ref, _ := node["$ref"].(string)
		if !strings.HasPrefix(ref, "#/") {
			return node
		}
		var cur any = root
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			m, _ := cur.(map[string]any)
			cur = m[part]
		}
		node, _ = cur.(map[string]any)
	}
	return node
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// gitRunner shells out to git in dir (the working directory when empty).
type gitRunner struct {
	dir string
}

func (g gitRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = g.dir
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, detail)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func (g gitRunner) revParse(ctx context.Context, rev string) (string, error) {
	out, err := g.run(ctx, "rev-parse", "--verify", "-q", rev)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}
