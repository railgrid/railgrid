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

// Command railgrid-runner serves the loopback-only generic runner protocol.
//
// The launch logic lives in pkg/runner/runnercli so this binary and the
// `railgrid runner run` subcommand of the main CLI stay byte-for-byte the same
// behaviour; only flag parsing differs.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/railgrid/railgrid/pkg/runner/harness/claude"
	"github.com/railgrid/railgrid/pkg/runner/runnercli"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "railgrid-runner:", err)
		os.Exit(1)
	}
}

func run() error {
	opts := runnercli.DefaultOptions()
	var showVersion bool
	flags := flag.NewFlagSet("railgrid-runner", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.BoolVar(&showVersion, "version", false, "print build and protocol metadata as JSON, then exit")
	flags.StringVar(&opts.Config, "config", "", "path to the JSON runner enrollment/configuration file")
	flags.StringVar(&opts.StateDir, "state-dir", "", "durable runner state directory")
	flags.StringVar(&opts.Listen, "listen", "", "loopback listen address (default 127.0.0.1:8787)")
	flags.StringVar(&opts.TokenFile, "token-file", "", "file containing the runner bearer token")
	flags.StringVar(&opts.Harness, "harness", runnercli.HarnessCodex,
		"coding harness to serve: "+strings.Join(runnercli.Harnesses, " or ")+" (one per runner process)")
	flags.StringVar(&opts.VersionPin, "version-pin", "",
		"expected harness version; empty uses the selected harness default (Codex "+runnercli.DefaultCodexVersionPin+"; Claude Code unpinned)")
	flags.StringVar(&opts.CodexHome, "codex-home", "", "runner-owned CODEX_HOME directory")
	flags.StringVar(&opts.CodexBinary, "codex-binary", runnercli.DefaultCodexBinary, "Codex executable")
	flags.StringVar(&opts.ClaudeHome, "claude-home", "", "runner-owned CLAUDE_CONFIG_DIR directory")
	flags.StringVar(&opts.ClaudeBinary, "claude-binary", runnercli.DefaultClaudeBinary, "Claude Code executable")
	flags.StringVar(&opts.ClaudeCredentialFile, "claude-credential-file", "",
		"absolute owner-only file holding the Claude Code credential (required with -harness=claude)")
	flags.StringVar(&opts.ClaudeCredentialKind, "claude-credential-kind", "",
		"how to inject the Claude Code credential: "+strings.Join(claude.CredentialKinds, " or "))
	flags.StringVar(&opts.ClaudeModel, "claude-model", "", "model for Claude Code turns (empty uses the account default)")
	flags.StringVar(&opts.ClaudePermissionMode, "claude-permission-mode", "", "acceptEdits (default) or bypassPermissions")
	flags.Func("claude-allowed-tool", "Claude Code tool pattern granted for every turn (repeatable)", func(v string) error { opts.ClaudeAllowedTools = append(opts.ClaudeAllowedTools, v); return nil })
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if showVersion {
		return runnercli.WriteVersion(os.Stdout)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runnercli.Run(ctx, opts)
}
