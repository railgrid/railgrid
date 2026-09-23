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

package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

// The runner is started by the Edge agent under a service account whose PATH
// is the init system's. A per-user install then looks missing although the
// machine has it, which is what "executable file not found in $PATH" meant on
// a host where claude sat in ~/.local/bin.
func TestResolveBinaryFindsAPerUserInstallOutsidePATH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // a PATH that resolves nothing
	installed := filepath.Join(home, ".local", "bin", "claude")
	write(t, installed, 0o755)

	if got := ResolveBinary("claude"); got != installed {
		t.Fatalf("ResolveBinary(claude) = %q, want the per-user install %q", got, installed)
	}
}

// What PATH already answers wins: an operator who set a working PATH or a
// pinned build keeps exactly that.
func TestResolveBinaryPrefersPATH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	onPath := t.TempDir()
	write(t, filepath.Join(onPath, "codex"), 0o755)
	write(t, filepath.Join(home, ".local", "bin", "codex"), 0o755)
	t.Setenv("PATH", onPath)

	if got := ResolveBinary("codex"); got != filepath.Join(onPath, "codex") {
		t.Fatalf("ResolveBinary(codex) = %q, want the PATH entry", got)
	}
}

func TestResolveBinaryLeavesExplicitPathsAndUnknownNamesAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	explicit := filepath.Join("/opt", "vendor", "claude")
	if got := ResolveBinary(explicit); got != explicit {
		t.Fatalf("an explicit path was rewritten to %q", got)
	}
	// Nothing found: the caller's own "not found in $PATH" is the clearest
	// message, so the name comes back unchanged.
	if got := ResolveBinary("claude"); got != "claude" {
		t.Fatalf("ResolveBinary(claude) = %q, want the name unchanged", got)
	}
}

// A directory or a non-executable file with the right name is not a candidate.
func TestResolveBinarySkipsNonExecutables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	write(t, filepath.Join(home, ".local", "bin", "claude"), 0o644)
	if err := os.MkdirAll(filepath.Join(home, "bin", "claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := ResolveBinary("claude"); got != "claude" {
		t.Fatalf("ResolveBinary(claude) = %q, want no match for a non-executable file or a directory", got)
	}
}
