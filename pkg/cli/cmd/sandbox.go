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
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// sandboxExecMaxTimeout is the data plane's exec ceiling.
const sandboxExecMaxTimeout = 120 * time.Second

// sandboxPollInterval is the exec poll / log follow cadence.
var sandboxPollInterval = time.Second

// syncFile / syncRequest / syncResponse mirror the dev agent's /sync contract
// (providers/infrastructure/dev-agent). Encoding is omitted for UTF-8 text
// (the default) and "base64" for binary content; only agents that advertise
// base64 in their status syncEncodings receive base64 entries.
type syncFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
}

// localSyncFile is one file read from the synced directory: its raw bytes and
// whether they travel as JSON text (valid UTF-8 without NUL).
type localSyncFile struct {
	Path string
	Data []byte
	Text bool
}

type syncRequest struct {
	Files          []syncFile `json:"files"`
	DeletePaths    []string   `json:"deletePaths"`
	Restart        string     `json:"restart"`
	SourceRevision uint64     `json:"sourceRevision,omitempty"`
	SourceDigest   string     `json:"sourceDigest,omitempty"`
}

type syncResponse struct {
	Phase          string   `json:"phase"`
	Changed        []string `json:"changed"`
	Deleted        []string `json:"deleted,omitempty"`
	Restarted      bool     `json:"restarted"`
	ReloadError    string   `json:"reloadError,omitempty"`
	SourceRevision uint64   `json:"sourceRevision,omitempty"`
	SourceDigest   string   `json:"sourceDigest,omitempty"`
}

// processStatus mirrors the dev agent's process status (the component
// "process" verb).
type processStatus struct {
	AttemptID      uint64 `json:"attemptID"`
	Configured     bool   `json:"configured"`
	Running        bool   `json:"running"`
	Port           string `json:"port,omitempty"`
	PortReachable  bool   `json:"portReachable,omitempty"`
	SourceRevision uint64 `json:"sourceRevision,omitempty"`
	SourceDigest   string `json:"sourceDigest,omitempty"`
	// SyncEncodings lists the file encodings the agent's /sync accepts. An
	// agent that predates binary sync omits it (UTF-8 text only).
	SyncEncodings []string `json:"syncEncodings,omitempty"`
}

// supportsEncoding reports whether the agent advertised enc for /sync.
func (p processStatus) supportsEncoding(enc string) bool {
	for _, e := range p.SyncEncodings {
		if strings.EqualFold(strings.TrimSpace(e), enc) {
			return true
		}
	}
	return false
}

// execRequest / execResult mirror the data plane's exec contract.
type execRequest struct {
	Action         string   `json:"action"`
	SessionID      string   `json:"sessionID,omitempty"`
	Argv           []string `json:"argv,omitempty"`
	Workdir        string   `json:"workdir,omitempty"`
	TimeoutSeconds int32    `json:"timeoutSeconds,omitempty"`
	SourceRevision uint64   `json:"sourceRevision,omitempty"`
	SourceDigest   string   `json:"sourceDigest,omitempty"`
}

type execResult struct {
	SessionID string `json:"sessionID,omitempty"`
	State     string `json:"state"`
	ExitCode  *int32 `json:"exitCode,omitempty"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

func newSandboxCommand() *cobra.Command {
	var target hubTarget
	cmd := &cobra.Command{
		Use:     "sandbox",
		Aliases: []string{"sbx"},
		Short:   "Drive a development-mode instance: sync, exec, logs, restart, status",
		Long: `Drive a development-mode infrastructure Instance (for an App Studio project,
<project>-dev) through the hub data plane, as you.

Component paths are relative to the component's workspacePath: for the
application template, sync api/ to component "api" and web/ to "web".
Production instances answer 409.

  faros sandbox sync    shop-dev api ./api
  faros sandbox exec    shop-dev api -- node -e 'console.log(1)'
  faros sandbox logs    shop-dev api -f
  faros sandbox restart shop-dev api
  faros sandbox status  shop-dev [api]`,
	}
	target.addFlags(cmd)
	cmd.AddCommand(
		newSandboxSyncCommand(&target),
		newSandboxExecCommand(&target),
		newSandboxLogsCommand(&target),
		newSandboxRestartCommand(&target),
		newSandboxStatusCommand(&target),
	)
	return cmd
}

// dataPlaneURL is the instance's data-plane base on the hub.
func dataPlaneURL(s *hubSession, instance string) string {
	return fmt.Sprintf("%s/services/providers/infrastructure/dataplane/clusters/%s/instances/%s",
		s.Hub, url.PathEscape(s.Cluster), url.PathEscape(instance))
}

func componentURL(s *hubSession, instance, component, verb string) string {
	return dataPlaneURL(s, instance) + "/components/" + url.PathEscape(component) + "/" + verb
}

func newSandboxSyncCommand(target *hubTarget) *cobra.Command {
	var restart, output string
	cmd := &cobra.Command{
		Use:   "sync <instance> <component> [dir]",
		Short: "Push a directory into the component workspace (authoritative)",
		Long: `Push every non-ignored file under dir (default ".") into the component
workspace. Inside a git repository the file list is 'git ls-files -co
--exclude-standard'; elsewhere every file minus node_modules/, dist/ and .git/.

The sync is authoritative: it carries a source revision and a digest over the
whole file set, replaces the managed file set, and is what exec verifies
against. Binary files (not UTF-8, or containing NUL) are sent base64-encoded
when the component's dev agent advertises base64 sync (at most 25 MiB per
binary file, 48 MiB per sync); against an older agent they are skipped with a
warning.

For an App Studio project's <project>-dev instance, 'faros app sync <project>'
pushes App Studio's own file set instead; a sandbox sync replaces that set.`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(output); err != nil {
				return err
			}
			dir := "."
			if len(args) == 3 {
				dir = args[2]
			}
			return runSandboxSync(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), *target, args[0], args[1], dir, restart, output)
		},
	}
	cmd.Flags().StringVar(&restart, "restart", "auto", "Restart policy after sync: auto or always")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

func runSandboxSync(ctx context.Context, out, errOut io.Writer, target hubTarget, instance, component, dir, restart, output string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	paths, err := listSyncFiles(ctx, dir)
	if err != nil {
		return err
	}
	local, err := readSyncFiles(dir, paths)
	if err != nil {
		return err
	}
	if len(local) == 0 {
		return fmt.Errorf("no files to sync under %s", dir)
	}

	s, err := newHubSession(ctx, target)
	if err != nil {
		return err
	}
	// Revisions must increase. Unix seconds does that across sessions; bump
	// past the applied revision so two syncs within a second still work. The
	// same status says whether the agent accepts base64 (binary) files; when
	// it cannot be read, assume it does not.
	revision := uint64(time.Now().Unix())
	var proc processStatus
	if err := s.do(ctx, http.MethodGet, componentURL(s, instance, component, "process"), nil, &proc); err != nil {
		proc = processStatus{}
	} else if proc.SourceRevision >= revision {
		revision = proc.SourceRevision + 1
	}
	files, skipped, err := buildSyncFiles(local, proc.supportsEncoding(encodingBase64))
	if err != nil {
		return err
	}
	if len(skipped) > 0 {
		_, _ = fmt.Fprintf(errOut, "faros sandbox: skipping %d binary file(s); %s/%s's dev agent does not advertise base64 sync (update the instance to sync them): %s\n",
			len(skipped), instance, component, strings.Join(skipped, ", "))
	}
	if len(files) == 0 {
		return fmt.Errorf("no text files to sync under %s", dir)
	}
	req := syncRequest{
		Files:          files,
		DeletePaths:    []string{},
		Restart:        restart,
		SourceRevision: revision,
		SourceDigest:   sourceDigest(files),
	}
	_, _ = fmt.Fprintf(errOut, "faros sandbox: syncing %d file(s) to %s/%s\n", len(files), instance, component)
	var raw json.RawMessage
	if err := s.do(ctx, http.MethodPost, componentURL(s, instance, component, "sync"), req, &raw); err != nil {
		return err
	}
	if output == "json" {
		return printJSON(out, raw)
	}
	var res syncResponse
	_ = json.Unmarshal(raw, &res)
	_, err = fmt.Fprintf(out, "%s: %d changed, %d deleted, restarted=%v, revision %d\n",
		formatStringOrDash(res.Phase), len(res.Changed), len(res.Deleted), res.Restarted, res.SourceRevision)
	if res.ReloadError != "" {
		_, _ = fmt.Fprintf(errOut, "faros sandbox: reload error: %s\n", res.ReloadError)
	}
	return err
}

// listSyncFiles returns the slash-separated paths under dir to sync.
func listSyncFiles(ctx context.Context, dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	g := gitRunner{dir: dir}
	var paths []string
	if out, err := g.run(ctx, "rev-parse", "--is-inside-work-tree"); err == nil && strings.TrimSpace(string(out)) == "true" {
		listed, err := g.run(ctx, "ls-files", "-z", "-co", "--exclude-standard", ".")
		if err != nil {
			return nil, err
		}
		for _, p := range strings.Split(string(listed), "\x00") {
			if p != "" {
				paths = append(paths, p)
			}
		}
	} else {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				switch rel {
				case "node_modules", "dist", ".git":
					return filepath.SkipDir
				}
				return nil
			}
			paths = append(paths, rel)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// readSyncFiles reads each listed path. Missing files (deleted but still in
// the index), non-regular files and reserved paths are left out.
func readSyncFiles(dir string, paths []string) ([]localSyncFile, error) {
	var files []localSyncFile
	for _, p := range paths {
		if reservedWorkspacePath(p) {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		info, err := os.Stat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		files = append(files, localSyncFile{Path: p, Data: content, Text: isUTF8Text(content)})
	}
	return files, nil
}

// buildSyncFiles turns local files into /sync entries. Text goes as-is;
// binary files go base64-encoded when the agent accepts base64, and are
// otherwise left out and returned in skipped. Limits are checked on the raw
// bytes that are sent: binaryFileLimit per binary file, payloadTotalLimit
// for the whole sync.
func buildSyncFiles(local []localSyncFile, base64OK bool) ([]syncFile, []string, error) {
	var files []syncFile
	var skipped []string
	var total int64
	for _, f := range local {
		if !f.Text && !base64OK {
			skipped = append(skipped, f.Path)
			continue
		}
		if !f.Text && len(f.Data) > binaryFileLimit {
			return nil, nil, fmt.Errorf("%s is %s; binary files sync at most %s each", f.Path, formatMiB(int64(len(f.Data))), formatMiB(binaryFileLimit))
		}
		total += int64(len(f.Data))
		if total > payloadTotalLimit {
			return nil, nil, fmt.Errorf("sync is over %s in total (reached at %s); sync a smaller directory or ignore large files", formatMiB(payloadTotalLimit), f.Path)
		}
		if f.Text {
			files = append(files, syncFile{Path: f.Path, Content: string(f.Data)})
			continue
		}
		files = append(files, syncFile{Path: f.Path, Content: base64.StdEncoding.EncodeToString(f.Data), Encoding: encodingBase64})
	}
	return files, skipped, nil
}

// reservedWorkspacePath mirrors the dev agent's refusal of .git and
// node_modules path components.
func reservedWorkspacePath(p string) bool {
	for _, part := range strings.Split(p, "/") {
		switch strings.ToLower(part) {
		case ".git", "node_modules", ".assistant-snapshots":
			return true
		}
	}
	return false
}

// sourceDigest is the dev agent's authoritative-sync digest: hex sha256 over
// "<path>\0<bytes>\0" for every file, sorted by path (byte order). The bytes
// are the decoded file content, so a base64 entry hashes its raw bytes and
// text digests are unchanged.
func sourceDigest(files []syncFile) string {
	sorted := append([]syncFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		_, _ = io.WriteString(h, f.Path)
		_, _ = h.Write([]byte{0})
		if f.Encoding == encodingBase64 {
			// Entries come from buildSyncFiles, so this always decodes; a bad
			// entry would make the agent reject the digest, not pass silently.
			raw, _ := base64.StdEncoding.DecodeString(f.Content)
			_, _ = h.Write(raw)
		} else {
			_, _ = io.WriteString(h, f.Content)
		}
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func newSandboxExecCommand(target *hubTarget) *cobra.Command {
	var timeout time.Duration
	var workdir string
	cmd := &cobra.Command{
		Use:   "exec <instance> <component> -- <argv...>",
		Short: "Run a command in the component and exit with its exit code",
		Long: `Run argv (no shell) against the component's last authoritative sync, print its
stdout and stderr, and exit with its exit code. Run 'faros sandbox sync' first
(for an App Studio <project>-dev instance, 'faros app sync <project>').
The command gets PORT (the component's dev server port, so it can reach the
running app) and FAROS_COMPONENT, but not the app's own environment or
secrets (DATABASE_URL and the like).`,
		Args: cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dash := cmd.ArgsLenAtDash(); dash >= 0 && dash != 2 {
				return fmt.Errorf("usage: faros sandbox exec <instance> <component> -- <argv...>")
			}
			code, err := runSandboxExec(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), *target, args[0], args[1], args[2:], workdir, timeout)
			if err != nil {
				return err
			}
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", sandboxExecMaxTimeout, "Command timeout (at most 120s)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory relative to the component workspace")
	return cmd
}

// runSandboxExec starts argv and polls it to completion, returning the
// command's exit code.
func runSandboxExec(ctx context.Context, out, errOut io.Writer, target hubTarget, instance, component string, argv []string, workdir string, timeout time.Duration) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 || timeout > sandboxExecMaxTimeout {
		return 0, fmt.Errorf("--timeout must be between 1s and %s", sandboxExecMaxTimeout)
	}
	if len(argv) == 0 {
		return 0, fmt.Errorf("exec needs argv after --")
	}
	s, err := newHubSession(ctx, target)
	if err != nil {
		return 0, err
	}
	var proc processStatus
	if err := s.do(ctx, http.MethodGet, componentURL(s, instance, component, "process"), nil, &proc); err != nil {
		return 0, err
	}
	if proc.SourceRevision == 0 || proc.SourceDigest == "" {
		if project := appStudioProjectForInstance(ctx, s, instance); project != "" {
			return 0, fmt.Errorf("%s/%s has no source revision; run 'faros app sync %s' first (exec needs an authoritative sync, and %s is managed by App Studio project %s, so 'faros sandbox sync' would replace its file set)", instance, component, project, instance, project)
		}
		return 0, fmt.Errorf("%s/%s has no source revision; run 'faros sandbox sync %s %s <dir>' first (exec needs an authoritative sync)", instance, component, instance, component)
	}
	execURL := componentURL(s, instance, component, "exec")
	start := execRequest{
		Action:         "start",
		Argv:           argv,
		Workdir:        workdir,
		TimeoutSeconds: int32(timeout / time.Second),
		SourceRevision: proc.SourceRevision,
		SourceDigest:   proc.SourceDigest,
	}
	var res execResult
	if err := s.doWithHeaders(ctx, http.MethodPost, execURL, start, http.Header{"Idempotency-Key": {newIdempotencyKey()}}, &res); err != nil {
		return 0, fmt.Errorf("exec start failed: %w", err)
	}
	if res.SessionID == "" && !execTerminal(res.State) {
		return 0, fmt.Errorf("exec start returned no session (state %q)", res.State)
	}
	deadline := time.Now().Add(timeout + 10*time.Second)
	for !execTerminal(res.State) {
		if time.Now().After(deadline) {
			return 124, fmt.Errorf("exec still %s after %s (session %s)", res.State, timeout, res.SessionID)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(sandboxPollInterval):
		}
		sessionID := res.SessionID
		if err := s.do(ctx, http.MethodPost, execURL, execRequest{Action: "poll", SessionID: sessionID}, &res); err != nil {
			return 0, fmt.Errorf("exec poll failed: %w", err)
		}
		if res.SessionID == "" {
			res.SessionID = sessionID
		}
	}
	_, _ = io.WriteString(out, res.Stdout)
	_, _ = io.WriteString(errOut, res.Stderr)
	if res.Truncated {
		_, _ = fmt.Fprintln(errOut, "faros sandbox: output truncated")
	}
	if res.ExitCode == nil {
		_, _ = fmt.Fprintf(errOut, "faros sandbox: command ended in state %q without an exit code\n", res.State)
		return 1, nil
	}
	return int(*res.ExitCode), nil
}

// appStudioInstanceProjectLabel mirrors providers/app-studio/bindings.ProjectLabel:
// App Studio labels every instance it creates for a project (<project>-dev,
// <project>-prod) with the project name.
const appStudioInstanceProjectLabel = "app-studio.faros.sh/project"

// instanceAPIURL is the tenant kube API path of an infrastructure Instance
// (cluster-scoped) in the session's workspace.
func instanceAPIURL(s *hubSession, name string) string {
	return fmt.Sprintf("%s/clusters/%s/apis/infrastructure.faros.sh/v1alpha1/instances/%s",
		s.Hub, url.PathEscape(s.Cluster), url.PathEscape(name))
}

// appStudioProjectForInstance returns the App Studio project that manages
// instance, or "" when it is not App Studio-managed. It reads the instance's
// project label (or its Project owner reference). When the instance cannot be
// read, it falls back to the naming convention: "<project>-dev" where an App
// Studio project <project> exists.
func appStudioProjectForInstance(ctx context.Context, s *hubSession, instance string) string {
	var inst struct {
		Metadata struct {
			Labels          map[string]string `json:"labels"`
			OwnerReferences []struct {
				APIVersion string `json:"apiVersion"`
				Kind       string `json:"kind"`
				Name       string `json:"name"`
			} `json:"ownerReferences"`
		} `json:"metadata"`
	}
	if err := s.do(ctx, http.MethodGet, instanceAPIURL(s, instance), nil, &inst); err == nil {
		if project := strings.TrimSpace(inst.Metadata.Labels[appStudioInstanceProjectLabel]); project != "" {
			return project
		}
		for _, o := range inst.Metadata.OwnerReferences {
			if o.Kind == "Project" && strings.HasPrefix(o.APIVersion, "ai.faros.sh/") && o.Name != "" {
				return o.Name
			}
		}
		return ""
	}
	base, ok := strings.CutSuffix(instance, "-dev")
	if !ok || base == "" {
		return ""
	}
	if err := s.do(ctx, http.MethodGet, projectURL(s, base), nil, nil); err != nil {
		return ""
	}
	return base
}

func execTerminal(state string) bool {
	return state != "queued" && state != "running"
}

func newIdempotencyKey() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "faros-cli-" + hex.EncodeToString(b)
}

func newSandboxLogsCommand(target *hubTarget) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <instance> <component>",
		Short: "Print the dev process log",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSandboxLogs(cmd.Context(), cmd.OutOrStdout(), *target, args[0], args[1], follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Keep polling and print new output (Ctrl-C to stop)")
	return cmd
}

func runSandboxLogs(ctx context.Context, out io.Writer, target hubTarget, instance, component string, follow bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := newHubSession(ctx, target)
	if err != nil {
		return err
	}
	logURL := componentURL(s, instance, component, "log")
	if !follow {
		resp, err := s.stream(ctx, http.MethodGet, logURL)
		if err != nil {
			return err
		}
		defer resp.Body.Close() //nolint:errcheck
		_, err = io.Copy(out, resp.Body)
		return err
	}
	// The log verb returns the current attempt's output; follow re-reads it
	// and prints what is new.
	prev := ""
	for {
		var raw json.RawMessage
		if err := s.do(ctx, http.MethodGet, logURL, nil, &raw); err != nil {
			return err
		}
		cur := string(raw)
		switch {
		case strings.HasPrefix(cur, prev):
			_, _ = io.WriteString(out, cur[len(prev):])
		default:
			_, _ = fmt.Fprintln(out, "--- log restarted ---")
			_, _ = io.WriteString(out, cur)
		}
		prev = cur
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * sandboxPollInterval):
		}
	}
}

func newSandboxRestartCommand(target *hubTarget) *cobra.Command {
	return &cobra.Command{
		Use:   "restart <instance> <component>",
		Short: "Restart the component's dev process",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			s, err := newHubSession(ctx, *target)
			if err != nil {
				return err
			}
			if err := s.do(ctx, http.MethodPost, componentURL(s, args[0], args[1], "restart"), nil, nil); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "restarted %s/%s\n", args[0], args[1])
			return err
		},
	}
}

func newSandboxStatusCommand(target *hubTarget) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "status <instance> [component]",
		Short: "Show the instance status, or a component's process state",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(output); err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			s, err := newHubSession(ctx, *target)
			if err != nil {
				return err
			}
			statusURL := dataPlaneURL(s, args[0]) + "/status"
			if len(args) == 2 {
				statusURL = componentURL(s, args[0], args[1], "process")
			}
			var raw json.RawMessage
			if err := s.do(ctx, http.MethodGet, statusURL, nil, &raw); err != nil {
				return err
			}
			if output == "json" {
				return printJSON(cmd.OutOrStdout(), raw)
			}
			if len(args) == 2 {
				return printProcessStatus(cmd.OutOrStdout(), raw)
			}
			return printInstanceStatus(cmd.OutOrStdout(), raw)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format: json")
	return cmd
}

func printProcessStatus(w io.Writer, raw json.RawMessage) error {
	var p processStatus
	if err := json.Unmarshal(raw, &p); err != nil {
		return printJSON(w, raw)
	}
	tw := newTabWriter(w)
	printRow(tw, "Running:", fmt.Sprintf("%v (configured=%v, attempt %d)", p.Running, p.Configured, p.AttemptID))
	if p.Port != "" {
		printRow(tw, "Port:", fmt.Sprintf("%s (reachable=%v)", p.Port, p.PortReachable))
	}
	rev := "- (not synced authoritatively yet)"
	if p.SourceRevision != 0 {
		rev = fmt.Sprintf("%d %s", p.SourceRevision, shortDigest(p.SourceDigest))
	}
	printRow(tw, "Source:", rev)
	printRow(tw, "Sync:", syncEncodingsSummary(p))
	return tw.Flush()
}

// syncEncodingsSummary says which files a sync can carry to the component.
// An agent that omits syncEncodings, or lists no base64, takes text only.
func syncEncodingsSummary(p processStatus) string {
	if p.supportsEncoding(encodingBase64) {
		return strings.Join(p.SyncEncodings, ", ")
	}
	return "utf-8 only (binary files are not synced to this component)"
}

func printInstanceStatus(w io.Writer, raw json.RawMessage) error {
	var st struct {
		Phase      string `json:"phase"`
		URL        string `json:"url"`
		Message    string `json:"message"`
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return printJSON(w, raw)
	}
	tw := newTabWriter(w)
	printRow(tw, "Phase:", formatStringOrDash(st.Phase))
	printRow(tw, "URL:", formatStringOrDash(st.URL))
	if st.Message != "" {
		printRow(tw, "Message:", st.Message)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(st.Conditions) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w)
	tw = newTabWriter(w)
	printRow(tw, "CONDITION", "STATUS", "REASON", "MESSAGE")
	for _, c := range st.Conditions {
		printRow(tw, c.Type, c.Status, formatStringOrDash(c.Reason), formatStringOrDash(oneLine(c.Message, 100)))
	}
	return tw.Flush()
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return "sha256:" + d[:12]
	}
	if d == "" {
		return ""
	}
	return "sha256:" + d
}

// oneLine collapses whitespace and caps s at n runes for table cells.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
