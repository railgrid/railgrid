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
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/railgrid/railgrid/pkg/runner/harness/claude"
	"github.com/railgrid/railgrid/pkg/runner/runnercli"
)

// newRunnerCommand returns the "railgrid runner" group. It exists so the one
// railgrid binary an edge already has can also BE the coding runner: the edge
// add-on manager supervises `<this executable> runner run ...` rather than
// asking an operator to distribute a second artifact (see docs/edge-addons.md).
// The standalone cmd/railgrid-runner binary remains, and both share
// pkg/runner/runnercli.
func newRunnerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runner",
		Short: "Run the loopback coding runner on this host",
	}
	cmd.AddCommand(newRunnerRunCommand())
	return cmd
}

// newRunnerRunCommand is `railgrid runner run`: the exact behaviour of the
// standalone railgrid-runner binary, including its refusal to run as root and
// its loopback-only listener.
func newRunnerRunCommand() *cobra.Command {
	opts := runnercli.DefaultOptions()
	var showVersion bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Serve the runner/v1 protocol on a loopback listener (foreground)",
		Long: `Serve the railgrid runner/v1 protocol on a loopback-only HTTP listener.

The runner is a single-execution coding runner for a host that is already
enrolled by its operator. Every request carries a bearer token; the listener
cannot be pointed at a non-loopback address; and the command refuses to run as
root because the harness boundaries isolate configuration, not privileges.

One runner process serves one coding harness, chosen with --harness: "codex"
(the default) or "claude" for headless Claude Code. Claude Code additionally
needs --claude-credential-file and --claude-credential-kind; the credential is
read from that file and injected into the harness child alone.

Normally an edge Addon of type "runner" supervises this command for you — see
docs/edge-addons.md. Run it by hand for a local fixture or an unmanaged host,
as described in docs/local-runner.md.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				return runnercli.WriteVersion(cmd.OutOrStdout())
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runnercli.Run(ctx, opts)
		},
	}

	cmd.Flags().BoolVar(&showVersion, "version", false, "Print build and protocol metadata as JSON, then exit")
	cmd.Flags().StringVar(&opts.Config, "config", "", "Path to the JSON runner enrollment/configuration file")
	cmd.Flags().StringVar(&opts.StateDir, "state-dir", "", "Durable runner state directory")
	cmd.Flags().StringVar(&opts.Listen, "listen", "", "Loopback listen address (default 127.0.0.1:8787)")
	cmd.Flags().StringVar(&opts.TokenFile, "token-file", "", "File containing the runner bearer token")
	cmd.Flags().StringVar(&opts.Harness, "harness", runnercli.HarnessCodex,
		"Coding harness to serve: "+strings.Join(runnercli.Harnesses, " or ")+". One harness per runner process.")
	cmd.Flags().StringVar(&opts.VersionPin, "version-pin", "",
		"Expected harness version; empty uses the selected harness default (Codex "+runnercli.DefaultCodexVersionPin+"; Claude Code unpinned)")
	cmd.Flags().StringVar(&opts.CodexHome, "codex-home", "", "Runner-owned CODEX_HOME directory (default <state-dir>/codex-home)")
	cmd.Flags().StringVar(&opts.CodexBinary, "codex-binary", runnercli.DefaultCodexBinary, "Codex executable")
	cmd.Flags().StringVar(&opts.ClaudeHome, "claude-home", "", "Runner-owned CLAUDE_CONFIG_DIR directory (default <state-dir>/claude-home)")
	cmd.Flags().StringVar(&opts.ClaudeBinary, "claude-binary", runnercli.DefaultClaudeBinary, "Claude Code executable")
	cmd.Flags().StringVar(&opts.ClaudeCredentialFile, "claude-credential-file", "",
		"Absolute owner-only file holding the Claude Code credential (required with --harness=claude)")
	cmd.Flags().StringVar(&opts.ClaudeCredentialKind, "claude-credential-kind", "",
		"How to inject the Claude Code credential: "+strings.Join(claude.CredentialKinds, " or "))
	cmd.Flags().StringVar(&opts.ClaudeModel, "claude-model", "", "Model for Claude Code turns (empty uses the account default)")
	return cmd
}
