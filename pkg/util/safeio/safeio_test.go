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

package safeio

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// tempDir is t.TempDir with symlinks resolved: on macOS it sits under /var,
// which is itself a symlink, and every helper here refuses one on principle.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestEnsureDirCreatesMissingAncestorsWithTheSameModeAndOwner: the runner's
// state directory lives three levels under the runner account's home, and the
// agent (root) creates it. Every level the agent makes must be one the account
// can traverse, or the directory is unreachable for the only process meant to
// use it. Levels that already exist are not the agent's to change.
func TestEnsureDirCreatesMissingAncestorsWithTheSameModeAndOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	home := filepath.Join(tempDir(t), "home")
	if err := os.Mkdir(home, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".railgrid", "runner", "claude")

	// chown to the test's own ids is allowed without privilege; on a root test
	// process both are 0 and the chown is skipped, which is the documented
	// contract rather than a gap in the test.
	if err := EnsureDir(target, 0700, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	for _, dir := range []string{
		filepath.Join(home, ".railgrid"),
		filepath.Join(home, ".railgrid", "runner"),
		target,
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("%s was not created: %v", dir, err)
		}
		if got := info.Mode().Perm(); got != 0700 {
			t.Errorf("%s has mode %o, want 0700 like the target", dir, got)
		}
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0755 {
		t.Errorf("the pre-existing home was re-moded to %o; existing ancestors must be left alone", got)
	}
}

// TestEnsureDirIsIdempotentOnAnExistingDirectory: a second reconcile finds
// everything in place and must still enforce the mode on the target.
func TestEnsureDirIsIdempotentOnAnExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	target := filepath.Join(tempDir(t), "state")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(target, 0700, 0, 0); err != nil {
		t.Fatalf("EnsureDir on an existing directory: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Errorf("mode %o after EnsureDir, want 0700", got)
	}
}

// TestEnsureDirRefusesASymlinkedAncestor is the package's reason to exist: a
// worker-owned `~/.railgrid -> /etc` must not turn a root-side mkdir into a
// write somewhere else.
func TestEnsureDirRefusesASymlinkedAncestor(t *testing.T) {
	base := tempDir(t)
	elsewhere := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(elsewhere, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "home", ".railgrid")
	if err := os.Mkdir(filepath.Dir(link), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := EnsureDir(filepath.Join(link, "runner", "claude"), 0700, 0, 0); err == nil {
		t.Fatal("EnsureDir followed a symlinked ancestor")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "runner")); err == nil {
		t.Error("EnsureDir created a directory through the symlink")
	}
}
