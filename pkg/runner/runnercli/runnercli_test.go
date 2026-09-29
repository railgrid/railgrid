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

package runnercli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultsAreTheCodexPath: every command line that predates --harness must
// keep behaving exactly as it did, including the Codex version pin.
func TestDefaultsAreTheCodexPath(t *testing.T) {
	opts := DefaultOptions()
	if opts.Harness != HarnessCodex {
		t.Fatalf("default harness = %q, want codex", opts.Harness)
	}
	if err := opts.Validate(); err != nil {
		t.Fatalf("the default options were rejected: %v", err)
	}
	if opts.VersionPin != DefaultCodexVersionPin {
		t.Errorf("version pin = %q, want the Codex default %q", opts.VersionPin, DefaultCodexVersionPin)
	}
	if opts.CodexBinary != DefaultCodexBinary {
		t.Errorf("codex binary = %q", opts.CodexBinary)
	}

	// An empty harness is the same as "codex": an Addon or a script written
	// before the flag existed must not start failing.
	empty := Options{}
	if err := empty.Validate(); err != nil {
		t.Fatalf("an unset harness was rejected: %v", err)
	}
	if empty.Harness != HarnessCodex || empty.VersionPin != DefaultCodexVersionPin {
		t.Errorf("unset harness normalized to %+v", empty)
	}
}

// TestCodexKeepsItsPinAndClaudeDoesNot: the Codex pin is a real contract with a
// tested app-server protocol; Claude Code self-updates, so pinning it by
// default would leave every runner unready out of the box.
func TestCodexKeepsItsPinAndClaudeDoesNot(t *testing.T) {
	claudeOpts := Options{Harness: HarnessClaude}
	if err := claudeOpts.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claudeOpts.VersionPin != "" {
		t.Errorf("claude version pin = %q, want unpinned", claudeOpts.VersionPin)
	}
	if claudeOpts.ClaudeBinary != DefaultClaudeBinary {
		t.Errorf("claude binary = %q", claudeOpts.ClaudeBinary)
	}

	// An explicit pin is honoured for either harness.
	pinned := Options{Harness: HarnessClaude, VersionPin: "2.1.273"}
	if err := pinned.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if pinned.VersionPin != "2.1.273" {
		t.Errorf("an explicit pin was overwritten: %q", pinned.VersionPin)
	}
}

// TestNoHarnessTakesACredentialFlag: the caller sends its own identity with
// every attempt, so a runner that could be configured with one would be a
// shared model account waiting to happen. Launching must need no credential.
func TestNoHarnessTakesACredentialFlag(t *testing.T) {
	for _, harnessName := range Harnesses {
		opts := Options{Harness: harnessName}
		if err := opts.Validate(); err != nil {
			t.Errorf("%s needs configuration beyond its name: %v", harnessName, err)
		}
	}
}

// TestUnknownHarnessIsRejected: a typo must fail loudly rather than silently
// falling back to Codex with a Claude credential configured.
func TestUnknownHarnessIsRejected(t *testing.T) {
	opts := Options{Harness: "gpt"}
	err := opts.Validate()
	if err == nil {
		t.Fatal("an unknown harness was accepted")
	}
	if !strings.Contains(err.Error(), "gpt") || !strings.Contains(err.Error(), "codex") {
		t.Errorf("error should name the bad value and the known set: %v", err)
	}
}

// TestAdapterSelectionUsesManagedPaths: both harnesses get the runner's own
// managed worktree root and a home under the state directory, so neither can be
// pointed at a directory the runner did not create.
func TestAdapterSelectionUsesManagedPaths(t *testing.T) {
	stateRoot := filepath.Join(string(filepath.Separator), "var", "lib", "railgrid", "runner")

	codexOpts := DefaultOptions()
	if err := codexOpts.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := codexOpts.adapter(stateRoot); err != nil {
		t.Fatalf("codex adapter: %v", err)
	}

	claudeAdapterOpts := Options{Harness: HarnessClaude}
	if err := claudeAdapterOpts.Validate(); err != nil {
		t.Fatal(err)
	}
	adapter, err := claudeAdapterOpts.adapter(stateRoot)
	if err != nil {
		t.Fatalf("claude adapter: %v", err)
	}
	if adapter == nil {
		t.Fatal("no adapter was built")
	}
}
