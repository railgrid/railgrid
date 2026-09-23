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

// Package claude adapts headless Claude Code to the runner harness.
//
// One Run is one `claude --print` invocation: one turn, one process, one
// session. The process speaks `--output-format stream-json`, which this
// adapter parses line by line into bounded harness.Events and one terminal
// harness.Result. There is no long-lived server as there is for Codex.
//
// # Credentials
//
// Headless Claude Code authenticates through ONE environment variable, and
// this adapter is the only thing that ever sets it. The value is read from a
// runner-owned 0600 file named by Config.CredentialFile — never from the
// runner's own environment, which is why blockedEnvKey strips every inherited
// ANTHROPIC_*/CLAUDE_* variable before the child is started. Two kinds are
// supported:
//
//	oauth-token -> CLAUDE_CODE_OAUTH_TOKEN (produced by `claude setup-token`)
//	api-key     -> ANTHROPIC_API_KEY
//
// The value never appears in an event, a blocker, a log line, an error or the
// capabilities response: every string that leaves this package goes through
// redact() first.
//
// # Isolation: what is enforced by a flag, and what is not
//
// Enforced by flags (verified against `claude --help`, 2.1.273):
//
//	--print                       non-interactive; the workspace trust dialog is skipped
//	--output-format stream-json   machine-readable events instead of prose
//	--verbose                     required for stream-json to emit per-turn records
//	--permission-mode <mode>      Config.PermissionMode: acceptEdits (default) lets the
//	                              model edit files inside the worktree without asking;
//	                              bypassPermissions additionally allows every tool,
//	                              including arbitrary shell, and is for sandboxed hosts
//	--permission-prompts none     anything that would still prompt is DENIED, not asked
//	--allowedTools <list>         Config.AllowedTools: tool patterns granted up front
//	                              (e.g. "Bash(git *)") so acceptEdits can run tests
//	--safe-mode                   disables CLAUDE.md, skills, plugins, hooks, MCP servers,
//	                              custom commands/agents, output styles, workflows — i.e.
//	                              every project-controlled code and config path. Auth,
//	                              model selection, built-in tools and permissions still work.
//	--strict-mcp-config           with no --mcp-config, no MCP server is loaded at all
//	--disable-slash-commands      no skills
//	--no-chrome                   no browser integration
//	--setting-sources ""          no user, project or local settings files
//	--session-id / --resume       the session identity is ours, never discovered
//
// Enforced by environment (variable names verified present in the 2.1.273
// binary): CLAUDE_CONFIG_DIR pins the config directory to the runner-owned
// home; DISABLE_AUTOUPDATER, DISABLE_TELEMETRY, DISABLE_ERROR_REPORTING,
// DISABLE_BUG_COMMAND, DISABLE_COST_WARNINGS and
// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC turn off self-update and
// non-essential network traffic. The IDE integration is opt-in (--ide), so not
// passing it is the disabled state.
//
// NOT available, and therefore NOT enforced:
//
//   - No max-turns flag exists in this release, so a turn is bounded by the
//     runner's own execution limits and by context cancellation, not by the
//     harness.
//   - No network sandbox. Unlike the Codex adapter's workspace-write sandbox,
//     Claude Code has no flag that disables network access for the turn. Tools
//     that reach the network are reachable unless removed with --restricted,
//     which also removes Bash and would make a coding runner useless.
//   - No filesystem sandbox beyond the working directory: file tools are
//     confined to the cwd and --add-dir entries (we pass none), but Bash can
//     still reach anything the runner account can.
//
// The privilege boundary for an add-on-managed runner is therefore the
// dedicated non-root account the agent supervises it under, not this adapter.
// See docs/edge-addons.md.
package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
	"github.com/railgrid/railgrid/pkg/runner/harness/internal/proc"
)

const (
	// HarnessName is what the runner advertises in its capabilities response.
	HarnessName = "claude-code"

	defaultBinary = "claude"
	// There is deliberately no default version pin: unlike Codex, Claude Code
	// self-updates on a fast cadence and an operator who has not chosen a pin
	// is better served by "whatever is installed" than by a stale constant.
	maxEventData       = 64 << 10
	maxWireLine        = 2 << 20
	maxStreamBytes     = 64 << 20
	maxCredentialBytes = 64 << 10
	maxMessageText     = 8 << 10
	processStopGrace   = 750 * time.Millisecond
	processStopTimeout = 2 * time.Second
)

// CredentialKind selects which environment variable carries the credential.
type CredentialKind string

const (
	// CredentialOAuthToken injects CLAUDE_CODE_OAUTH_TOKEN, the long-lived
	// token `claude setup-token` mints for a Claude subscription.
	CredentialOAuthToken CredentialKind = "oauth-token"
	// CredentialAPIKey injects ANTHROPIC_API_KEY.
	CredentialAPIKey CredentialKind = "api-key"
)

// CredentialKinds are the accepted spellings, for flag and API validation.
var CredentialKinds = []string{string(CredentialOAuthToken), string(CredentialAPIKey)} //nolint:gochecknoglobals

// EnvVar returns the environment variable a credential kind is injected as.
func (k CredentialKind) EnvVar() string {
	switch k {
	case CredentialOAuthToken:
		return "CLAUDE_CODE_OAUTH_TOKEN"
	case CredentialAPIKey:
		return "ANTHROPIC_API_KEY"
	default:
		return ""
	}
}

// Valid reports whether k is a supported credential kind.
func (k CredentialKind) Valid() bool { return k.EnvVar() != "" }

// A Claude Code home may hold session state and the config the adapter itself
// pins, but never anything that would execute project-controlled code. These
// entries are refused outright rather than ignored: their presence means the
// home is not dedicated to the runner.
var unsafeHomeEntries = map[string]struct{}{ //nolint:gochecknoglobals // immutable worker-home policy
	".claude":          {},
	".mcp.json":        {},
	"agents":           {},
	"claude.md":        {},
	"commands":         {},
	"hooks":            {},
	"mcp.json":         {},
	"mcp_servers.json": {},
	"output-styles":    {},
	"plugins":          {},
	"settings.json":    {},
	"skills":           {},
}

var versionPattern = regexp.MustCompile(`([0-9]+\.[0-9]+\.[0-9]+)`) //nolint:gochecknoglobals

// Config selects the Claude Code executable, its isolated home and the single
// credential the child is given.
type Config struct {
	Binary string
	// Home is a runner-owned CLAUDE_CONFIG_DIR. It must be dedicated to the
	// worker and is created 0700.
	Home string
	// WorktreeRoot is the managed checkout root; Launch.Workdir must be a task
	// directory beneath it. Shares the Codex adapter's rule.
	WorktreeRoot string
	// Model is passed as --model when set; empty leaves the account default.
	Model string
	// ExpectedVersion, when set, produces a readiness Reason on mismatch.
	ExpectedVersion string
	// CredentialFile is an absolute, runner-owned 0600 file holding the raw
	// credential value. It is read on every Probe and Run so a rotated
	// credential is picked up without recreating the adapter.
	CredentialFile string
	// CredentialKind selects the environment variable the value is injected as.
	CredentialKind CredentialKind
	// PermissionMode is what the model may do without asking. Empty means
	// PermissionAcceptEdits. A coding runner cannot work under "dontAsk": with
	// prompts denied, every file edit is refused and each turn ends with no
	// changes.
	PermissionMode PermissionMode
	// AllowedTools are Claude Code tool patterns granted for the session, e.g.
	// "Bash(git *)" or "Bash(npm test)". They matter under PermissionAcceptEdits,
	// where shell commands are otherwise denied.
	AllowedTools []string
}

// PermissionMode names a Claude Code --permission-mode value the runner supports.
type PermissionMode string

const (
	// PermissionAcceptEdits auto-approves file edits within the working
	// directory; anything else that would prompt is denied.
	PermissionAcceptEdits PermissionMode = "acceptEdits"
	// PermissionBypass approves every tool call. The dedicated runner account
	// and the worktree are then the only isolation; Claude Code documents it
	// as suitable only for sandboxes.
	PermissionBypass PermissionMode = "bypassPermissions"
)

// permissionMode resolves the configured mode, validating it.
func (a *Adapter) permissionMode() (PermissionMode, error) {
	switch a.cfg.PermissionMode {
	case "", PermissionAcceptEdits:
		return PermissionAcceptEdits, nil
	case PermissionBypass:
		return PermissionBypass, nil
	}
	return "", fmt.Errorf("unsupported Claude Code permission mode %q (use %s or %s)", a.cfg.PermissionMode, PermissionAcceptEdits, PermissionBypass)
}

// Adapter is the Claude Code implementation of harness.Adapter.
type Adapter struct {
	cfg Config
}

var _ harness.Adapter = (*Adapter)(nil)

// New constructs a Claude Code harness adapter. The returned adapter starts no
// process and reads no credential until Probe or Run is called.
func New(cfg Config) harness.Adapter {
	if strings.TrimSpace(cfg.Binary) == "" {
		cfg.Binary = defaultBinary
	}
	// A managed runner's PATH is the service account's, not a login shell's;
	// resolve the usual install locations so a normal install just works.
	cfg.Binary = harness.ResolveBinary(cfg.Binary)
	return &Adapter{cfg: cfg}
}

// Probe checks the executable's version and that a credential is present,
// without making a model call. It never prints, returns or logs the credential
// itself — only whether one could be read.
func (a *Adapter) Probe(ctx context.Context) (harness.Info, error) {
	info := harness.Info{Name: HarnessName}
	if err := a.ensureHome(); err != nil {
		return info, err
	}
	version, err := a.probeVersion(ctx)
	if err != nil {
		return info, err
	}
	info.Version = version
	if a.cfg.ExpectedVersion != "" && version != a.cfg.ExpectedVersion {
		info.Reasons = append(info.Reasons, fmt.Sprintf("expected Claude Code %s, found %s", a.cfg.ExpectedVersion, version))
	}
	if _, err := a.permissionMode(); err != nil {
		info.Reasons = append(info.Reasons, err.Error())
	}
	if _, err := a.readCredential(); err != nil {
		// The reason is the CLASS of failure, never the path's contents.
		info.Reasons = append(info.Reasons, "Claude Code authentication is not configured: "+err.Error())
	}
	info.Ready = len(info.Reasons) == 0
	return info, nil
}

func (a *Adapter) probeVersion(ctx context.Context) (string, error) {
	credential, _ := a.readCredential()
	cmd := exec.CommandContext(ctx, a.cfg.Binary, "--version")
	cmd.Env = a.childEnv(credential)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("running Claude Code version probe: %w: %s", credential.redactError(err), redact(trimOutput(output), credential))
	}
	match := versionPattern.FindSubmatch(output)
	if len(match) != 2 {
		return "", fmt.Errorf("parsing Claude Code version from %q", redact(trimOutput(output), credential))
	}
	return string(match[1]), nil
}

// Run executes exactly one headless turn. A Launch that carries a SessionID
// resumes that session; one that does not starts a new one. The session id is
// returned in every outcome — including failures — so a caller can always
// resume rather than silently starting a second conversation.
func (a *Adapter) Run(ctx context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
	var result harness.Result
	if err := a.ensureHome(); err != nil {
		return result, err
	}
	if err := validateLaunch(launch); err != nil {
		return result, err
	}
	if err := a.validateWorkdir(launch.Workdir); err != nil {
		return result, err
	}
	if _, err := a.permissionMode(); err != nil {
		return result, err
	}
	credential, err := a.readCredential()
	if err != nil {
		if emit != nil {
			_ = emit(harness.Event{Type: "auth_failure", Message: "Claude Code authentication is not configured"})
		}
		return harness.Result{
			Phase:     "needs_input",
			SessionID: launch.SessionID,
			Blocker:   "Claude Code authentication is not configured: " + err.Error(),
		}, nil
	}

	cmd := exec.Command(a.cfg.Binary, a.args(launch)...) //nolint:gosec // the binary is operator-configured enrollment, never request data
	cmd.Env = a.childEnv(credential)
	cmd.Dir = launch.Workdir
	proc.Configure(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, fmt.Errorf("opening Claude Code stdout: %w", err)
	}
	stderr := &limitedBuffer{limit: 32 << 10}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return result, fmt.Errorf("opening Claude Code stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return result, fmt.Errorf("starting Claude Code: %w", err)
	}

	// The instructions go over stdin as the prompt, never as an argv element
	// and never through a shell: a prompt is caller-influenced text and must
	// not be able to become part of a command line.
	writeErr := writePrompt(stdin, promptPreamble+launch.Instructions)

	state := &runState{emit: emit, sessionID: launch.SessionID, credential: credential}
	parseErr := a.consume(ctx, stdout, state, cmd)

	// Wait only after the stream has been drained: cmd.Wait closes the stdout
	// pipe as soon as the process exits, and a reader still draining buffered
	// records would then fail with "file already closed".
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	stopErr := stopProcess(cmd, waited)
	if ctx.Err() != nil {
		return harness.Result{Phase: "cancelled", SessionID: state.sessionID}, nil
	}
	if writeErr != nil && state.result == nil {
		return failed(state, fmt.Errorf("writing the Claude Code prompt: %w", writeErr)), nil
	}
	if parseErr != nil {
		return failed(state, parseErr), nil
	}
	if state.result == nil {
		detail := redact(strings.TrimSpace(stderr.String()), credential)
		if detail == "" {
			detail = "no result record was produced"
		}
		return failed(state, errors.New("the Claude Code process exited without a result: "+detail)), errors.Join(stopErr)
	}
	return state.terminalResult(), nil
}

// args builds the headless command line. See the package comment for what each
// flag is protecting against; the ordering is stable so contract tests can pin
// it.
func (a *Adapter) args(launch harness.Launch) []string {
	mode, _ := a.permissionMode()
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		// Nothing may prompt: the mode decides what is pre-approved, and "none"
		// means a request that would still have prompted is denied outright
		// rather than parked.
		"--permission-mode", string(mode),
		"--permission-prompts", "none",
		// Every project-controlled execution path off.
		"--safe-mode",
		"--strict-mcp-config",
		"--disable-slash-commands",
		"--no-chrome",
		// An empty source list means "load no user, project or local settings
		// file". The flag and its three accepted values are documented; that an
		// EMPTY value is accepted has not been verified against a real binary,
		// because doing so needs a live invocation. If a Claude Code release
		// ever rejects it, every turn fails immediately and visibly rather than
		// silently loading settings — which is the safe direction for a flag
		// whose job is to remove a project-controlled input.
		"--setting-sources", "",
	}
	if launch.SessionID != "" {
		args = append(args, "--resume", launch.SessionID)
	} else if launch.AttemptID != "" && isUUID(launch.AttemptID) {
		// A caller whose attempt id is already a UUID gets a session id it
		// chose; otherwise Claude Code mints one and we learn it from the
		// stream's init record.
		args = append(args, "--session-id", launch.AttemptID)
	}
	if model := a.model(launch); model != "" {
		args = append(args, "--model", model)
	}
	// One flag per pattern: a pattern may contain spaces ("Bash(git *)"), and
	// each argv element is passed through unchanged.
	for _, tool := range a.cfg.AllowedTools {
		if tool = strings.TrimSpace(tool); tool != "" {
			args = append(args, "--allowedTools", tool)
		}
	}
	return args
}

func (a *Adapter) model(launch harness.Launch) string {
	if model := strings.TrimSpace(launch.Model); model != "" {
		return model
	}
	return strings.TrimSpace(a.cfg.Model)
}

// consume reads the stream-json output until the process ends, the stream is
// exhausted, or the context is cancelled. Cancellation kills the whole process
// group: Claude Code starts compilers, test runners and git.
func (a *Adapter) consume(ctx context.Context, stdout io.Reader, state *runState, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- state.consume(stdout) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Killing the group ends the stream; the reader then reaches EOF and
		// the caller's Wait reaps the process.
		_ = proc.KillGroup(cmd)
		select {
		case <-done:
		case <-time.After(processStopTimeout):
		}
		return nil
	}
}

func stopProcess(cmd *exec.Cmd, waited <-chan error) error {
	select {
	case err := <-waited:
		return err
	case <-time.After(processStopGrace):
	}
	killErr := proc.KillGroup(cmd)
	select {
	case <-waited:
		return killErr
	case <-time.After(processStopTimeout):
		return errors.Join(killErr, errors.New("timed out stopping the Claude Code process"))
	}
}

func writePrompt(stdin io.WriteCloser, prompt string) error {
	_, writeErr := io.WriteString(stdin, prompt)
	closeErr := stdin.Close()
	return errors.Join(writeErr, closeErr)
}

// ensureHome creates and validates the runner-owned CLAUDE_CONFIG_DIR. The
// rules mirror the Codex adapter's worker home: absolute, not a symlink, 0700,
// and free of anything that would load project-controlled code.
func (a *Adapter) ensureHome() error {
	home := strings.TrimSpace(a.cfg.Home)
	if home == "" {
		return errors.New("claude runner home is required and must be dedicated to the worker")
	}
	if !filepath.IsAbs(home) {
		return fmt.Errorf("claude runner home must be absolute: %q", home)
	}
	if info, err := os.Lstat(home); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("claude runner home must not be a symlink: %q", home)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect claude runner home: %w", err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("creating claude runner home: %w", err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return fmt.Errorf("protecting claude runner home: %w", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return fmt.Errorf("reading claude runner home: %w", err)
	}
	for _, entry := range entries {
		if _, unsafe := unsafeHomeEntries[strings.ToLower(entry.Name())]; unsafe {
			return fmt.Errorf("claude runner home contains interactive configuration %q; use a dedicated worker home", entry.Name())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("claude runner home contains symlink %q; use a dedicated worker home", entry.Name())
		}
	}
	return nil
}

// validateWorkdir requires the launch directory to be a managed task checkout
// under WorktreeRoot when a root is configured. Library callers that leave the
// root empty opt out, exactly as the Codex adapter allows.
func (a *Adapter) validateWorkdir(workdir string) error {
	if strings.TrimSpace(a.cfg.WorktreeRoot) == "" {
		return nil
	}
	root, err := filepath.Abs(a.cfg.WorktreeRoot)
	if err != nil {
		return errWorkdirRejected
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(workdir) || filepath.Clean(workdir) != workdir {
		return errWorkdirRejected
	}
	rel, err := filepath.Rel(root, workdir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errWorkdirRejected
	}
	// <root>/<taskID>/<attemptID>, the shape the runner's workspace builds.
	if len(strings.FieldsFunc(rel, func(r rune) bool { return r == filepath.Separator })) != 2 {
		return errWorkdirRejected
	}
	current := root
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == filepath.Separator }) {
		current = filepath.Join(current, part)
		info, lerr := os.Lstat(current)
		if lerr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errWorkdirRejected
		}
	}
	return nil
}

var errWorkdirRejected = errors.New("claude launch workdir is not a managed task checkout")

func validateLaunch(launch harness.Launch) error {
	if strings.TrimSpace(launch.Workdir) == "" {
		return errors.New("claude launch workdir is required")
	}
	if strings.TrimSpace(launch.Instructions) == "" {
		return errors.New("claude launch instructions are required")
	}
	return nil
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

type limitedBuffer struct {
	buf   strings.Builder
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		b.buf.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

func trimOutput(data []byte) string {
	text := strings.TrimSpace(string(data))
	if len(text) > 512 {
		return text[:512]
	}
	return text
}
