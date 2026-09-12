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

// Command faros-runner serves the loopback-only generic runner protocol.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/faroshq/faros/pkg/runner"
	"github.com/faroshq/faros/pkg/runner/harness/codex"
	"github.com/faroshq/faros/pkg/version"
)

type options struct {
	config      string
	stateDir    string
	listen      string
	tokenFile   string
	codexHome   string
	codexBinary string
	versionPin  string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "faros-runner:", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	var opts options
	var showVersion bool
	flags := flag.NewFlagSet("faros-runner", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.BoolVar(&showVersion, "version", false, "print build and protocol metadata as JSON, then exit")
	flags.StringVar(&opts.config, "config", "", "path to the JSON runner enrollment/configuration file")
	flags.StringVar(&opts.stateDir, "state-dir", "", "durable runner state directory")
	flags.StringVar(&opts.listen, "listen", "", "loopback listen address (default 127.0.0.1:8787)")
	flags.StringVar(&opts.tokenFile, "token-file", "", "file containing the runner bearer token")
	flags.StringVar(&opts.codexHome, "codex-home", "", "runner-owned CODEX_HOME directory")
	flags.StringVar(&opts.codexBinary, "codex-binary", "codex", "Codex executable")
	flags.StringVar(&opts.versionPin, "version-pin", "0.147.0", "expected Codex version")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if showVersion {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{
			"version": version.Get(), "commit": version.GitCommit, "buildDate": version.BuildDate,
			"protocolVersion": runner.ProtocolVersion, "os": runtime.GOOS, "arch": runtime.GOARCH,
		})
	}
	if os.Geteuid() == 0 {
		return errors.New("faros-runner must run as a non-root user")
	}
	cfg, err := runner.LoadConfig(opts.config)
	if err != nil {
		return err
	}
	// Build identity is executable-owned, not enrollment configuration.
	cfg.Version = version.Get()
	if opts.stateDir != "" {
		cfg.StateDir = opts.stateDir
	}
	if opts.listen != "" {
		cfg.Listen = opts.listen
	}
	if opts.tokenFile != "" {
		cfg.TokenFile = opts.tokenFile
		cfg.Token = ""
	}
	if cfg.StateDir == "" {
		base, resolveErr := os.UserConfigDir()
		if resolveErr != nil {
			return resolveErr
		}
		cfg.StateDir = filepath.Join(base, "faros-runner")
	}
	if opts.codexHome == "" {
		opts.codexHome = filepath.Join(cfg.StateDir, "codex-home")
	}
	stateRoot, err := filepath.Abs(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("resolve runner state directory: %w", err)
	}
	adapter := codex.New(codex.Config{
		Binary:          opts.codexBinary,
		Home:            opts.codexHome,
		WorktreeRoot:    filepath.Join(stateRoot, "worktrees"),
		ExpectedVersion: opts.versionPin,
	})
	r, err := runner.New(cfg, adapter)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, r.Close()) }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := r.ListenAndServe(ctx); err != nil {
		return fmt.Errorf("serve runner version %s: %w", version.Get(), err)
	}
	return nil
}
