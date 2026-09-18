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

// Package runnercli holds the launch logic shared by the standalone
// railgrid-runner binary (cmd/railgrid-runner) and the `railgrid runner run`
// subcommand of the main CLI (pkg/cli/cmd/runner.go). Both entry points parse
// their own flags — one with stdlib flag, the other with pflag — and then hand
// the identical Options to Run, so the two can never diverge on refusals
// (uid 0, non-loopback listener) or on how a harness adapter is configured.
package runnercli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/harness"
	"github.com/railgrid/railgrid/pkg/runner/harness/claude"
	"github.com/railgrid/railgrid/pkg/runner/harness/codex"
	"github.com/railgrid/railgrid/pkg/version"
)

const (
	// HarnessCodex is the default coding harness, for backwards compatibility:
	// a command line that predates --harness keeps behaving exactly as it did.
	HarnessCodex = "codex"
	// HarnessClaude runs headless Claude Code.
	HarnessClaude = "claude"

	// DefaultCodexBinary is the Codex executable looked up on PATH.
	DefaultCodexBinary = "codex"
	// DefaultCodexVersionPin is the Codex version the adapter probes for. It
	// applies ONLY to the Codex harness: Claude Code has no default pin.
	DefaultCodexVersionPin = "0.147.0"
	// DefaultClaudeBinary is the Claude Code executable looked up on PATH.
	DefaultClaudeBinary = "claude"
)

// Harnesses are the accepted --harness values.
var Harnesses = []string{HarnessCodex, HarnessClaude} //nolint:gochecknoglobals

// Options are the runner's launch inputs. Everything durable (identity,
// enrollment, token) lives in the JSON configuration file; these only select
// the file, the harness, and a handful of paths.
//
// One runner process drives exactly ONE harness. That is deliberate: the
// capabilities response advertises a single harness entry, and a coordinator
// that dispatches to a runner needs to know what it will get without
// negotiating.
type Options struct {
	Config    string
	StateDir  string
	Listen    string
	TokenFile string

	// Harness selects the adapter: "codex" (default) or "claude".
	Harness string
	// VersionPin applies to the SELECTED harness. Empty means the harness
	// default, which is a pinned version for Codex and no pin for Claude Code.
	VersionPin string

	CodexHome   string
	CodexBinary string

	ClaudeHome           string
	ClaudeBinary         string
	ClaudeCredentialFile string
	ClaudeCredentialKind string
	ClaudeModel          string
	// ClaudePermissionMode is acceptEdits (default) or bypassPermissions.
	ClaudePermissionMode string
	// ClaudeAllowedTools are tool patterns granted up front, e.g. "Bash(git *)".
	ClaudeAllowedTools []string
}

// DefaultOptions returns the defaults both front ends advertise.
func DefaultOptions() Options {
	return Options{
		Harness:      HarnessCodex,
		CodexBinary:  DefaultCodexBinary,
		ClaudeBinary: DefaultClaudeBinary,
	}
}

// WriteVersion emits the build and protocol metadata as JSON. It deliberately
// loads no enrollment, credentials, or harness state, so it stays usable as a
// local health check on a host whose configuration is broken.
func WriteVersion(w io.Writer) error {
	return json.NewEncoder(w).Encode(map[string]string{
		"version": version.Get(), "commit": version.GitCommit, "buildDate": version.BuildDate,
		"protocolVersion": runner.ProtocolVersion, "os": runtime.GOOS, "arch": runtime.GOARCH,
	})
}

// Validate normalizes and checks the harness selection without touching the
// filesystem or starting anything. Run calls it; the CLI front ends can call it
// early to fail on a bad flag combination before any other work.
func (o *Options) Validate() error {
	harnessName := strings.TrimSpace(o.Harness)
	if harnessName == "" {
		harnessName = HarnessCodex
	}
	if !slices.Contains(Harnesses, harnessName) {
		return fmt.Errorf("unknown --harness %q; must be one of %s", o.Harness, strings.Join(Harnesses, ", "))
	}
	o.Harness = harnessName

	switch harnessName {
	case HarnessCodex:
		if o.CodexBinary == "" {
			o.CodexBinary = DefaultCodexBinary
		}
		if o.VersionPin == "" {
			o.VersionPin = DefaultCodexVersionPin
		}
	case HarnessClaude:
		if o.ClaudeBinary == "" {
			o.ClaudeBinary = DefaultClaudeBinary
		}
		// Claude Code authenticates through an environment variable that this
		// runner injects from a file. Without both the file and its kind there
		// is nothing to inject, and the runner would come up permanently
		// unready — so refuse at launch, where the operator can see it.
		if strings.TrimSpace(o.ClaudeCredentialFile) == "" {
			return errors.New("--claude-credential-file is required with --harness=claude")
		}
		if !filepath.IsAbs(o.ClaudeCredentialFile) {
			return fmt.Errorf("--claude-credential-file must be absolute: %q", o.ClaudeCredentialFile)
		}
		kind := claude.CredentialKind(strings.TrimSpace(o.ClaudeCredentialKind))
		if !kind.Valid() {
			return fmt.Errorf("--claude-credential-kind must be one of %s", strings.Join(claude.CredentialKinds, ", "))
		}
		o.ClaudeCredentialKind = string(kind)
		switch mode := claude.PermissionMode(strings.TrimSpace(o.ClaudePermissionMode)); mode {
		case "", claude.PermissionAcceptEdits, claude.PermissionBypass:
			o.ClaudePermissionMode = string(mode)
		default:
			return fmt.Errorf("--claude-permission-mode must be %s or %s", claude.PermissionAcceptEdits, claude.PermissionBypass)
		}
	}
	return nil
}

// Run serves the loopback runner until ctx is cancelled. It refuses to run as
// root: the runner executes a coding harness, and the harness boundaries are
// configuration isolation, not a privilege boundary.
func Run(ctx context.Context, opts Options) (runErr error) {
	if os.Geteuid() == 0 {
		return errors.New("railgrid-runner must run as a non-root user")
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	cfg, err := runner.LoadConfig(opts.Config)
	if err != nil {
		return err
	}
	// Build identity is executable-owned, not enrollment configuration.
	cfg.Version = version.Get()
	if opts.StateDir != "" {
		cfg.StateDir = opts.StateDir
	}
	if opts.Listen != "" {
		cfg.Listen = opts.Listen
	}
	if opts.TokenFile != "" {
		cfg.TokenFile = opts.TokenFile
		cfg.Token = ""
	}
	if cfg.StateDir == "" {
		base, resolveErr := os.UserConfigDir()
		if resolveErr != nil {
			return resolveErr
		}
		cfg.StateDir = filepath.Join(base, "railgrid-runner")
	}
	stateRoot, err := filepath.Abs(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("resolve runner state directory: %w", err)
	}
	adapter, err := opts.adapter(stateRoot)
	if err != nil {
		return err
	}
	r, err := runner.New(cfg, adapter)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, r.Close()) }()
	if err := r.ListenAndServe(ctx); err != nil {
		return fmt.Errorf("serve runner version %s: %w", version.Get(), err)
	}
	return nil
}

// adapter builds the selected harness. Both adapters get the same managed
// worktree root, so neither will run a turn in a directory the runner did not
// create.
func (o Options) adapter(stateRoot string) (harness.Adapter, error) {
	worktreeRoot := filepath.Join(stateRoot, "worktrees")
	switch o.Harness {
	case HarnessClaude:
		home := o.ClaudeHome
		if home == "" {
			home = filepath.Join(stateRoot, "claude-home")
		}
		return claude.New(claude.Config{
			Binary:          o.ClaudeBinary,
			Home:            home,
			WorktreeRoot:    worktreeRoot,
			Model:           o.ClaudeModel,
			ExpectedVersion: o.VersionPin,
			CredentialFile:  o.ClaudeCredentialFile,
			CredentialKind:  claude.CredentialKind(o.ClaudeCredentialKind),
			PermissionMode:  claude.PermissionMode(o.ClaudePermissionMode),
			AllowedTools:    o.ClaudeAllowedTools,
		}), nil
	case HarnessCodex:
		home := o.CodexHome
		if home == "" {
			home = filepath.Join(stateRoot, "codex-home")
		}
		return codex.New(codex.Config{
			Binary:          o.CodexBinary,
			Home:            home,
			WorktreeRoot:    worktreeRoot,
			ExpectedVersion: o.VersionPin,
		}), nil
	default:
		return nil, fmt.Errorf("unknown harness %q", o.Harness)
	}
}
