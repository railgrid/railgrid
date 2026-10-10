//go:build unix

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

package harnessplane

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// stubBinary writes an executable file at dir/name and returns its path.
func stubBinary(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeInfo is an os.FileInfo with chosen ownership and mode, so the bit logic
// can be pinned without a second account on the test machine.
type fakeInfo struct {
	os.FileInfo
	mode os.FileMode
	stat syscall.Stat_t
}

func (f fakeInfo) Mode() os.FileMode { return f.mode }
func (f fakeInfo) Sys() any          { return &f.stat }

// TestPermitsEvaluatesOwnerGroupAndOther pins the permission model to the one
// the supervisor gives the child: its uid, its one primary gid, nothing else.
func TestPermitsEvaluatesOwnerGroupAndOther(t *testing.T) {
	owned := func(uid, gid int, mode os.FileMode) os.FileInfo {
		return fakeInfo{mode: mode, stat: syscall.Stat_t{Uid: uint32(uid), Gid: uint32(gid)}} //nolint:gosec // test ids
	}
	cases := []struct {
		name     string
		info     os.FileInfo
		uid, gid int
		want     bool
	}{
		{"owner with x", owned(997, 983, 0o700), 997, 1, true},
		{"owner without x, even if others have it", owned(997, 983, 0o601), 997, 983, false},
		{"group member with x", owned(1, 983, 0o750), 997, 983, true},
		{"group member without x", owned(1, 983, 0o740), 997, 983, false},
		{"anybody else with x", owned(1, 1, 0o751), 997, 983, true},
		{"anybody else without x", owned(1, 1, 0o750), 997, 983, false},
	}
	for _, tc := range cases {
		if got := permits(tc.info, tc.uid, tc.gid, 0o1); got != tc.want {
			t.Errorf("%s: permits = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// unsearchable removes the owner's search bit from dir for the rest of the
// test, restoring it before t.TempDir's cleanup needs to list the directory.
func unsearchable(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
}

// TestAccessibleByWalksEveryDirectoryOnTheWay: the file's own bits are not
// enough; a directory above it that the account cannot search blocks it just
// as surely. The test's own ids are used throughout — the temp directory is
// 0700 and owned by the test, so no other id could ever reach the fixture —
// and the block is made by taking the owner's search bit away.
func TestAccessibleByWalksEveryDirectoryOnTheWay(t *testing.T) {
	me, myGroup := os.Getuid(), os.Getgid()
	base := t.TempDir()
	dir := filepath.Join(base, "private", "bin")
	bin := stubBinary(t, dir, HarnessClaude)

	if !accessibleBy(bin, me, myGroup) {
		t.Fatalf("accessibleBy(%s) refused the owner a 0755 file in searchable directories", bin)
	}
	unsearchable(t, filepath.Join(base, "private"))
	if accessibleBy(bin, me, myGroup) {
		t.Errorf("accessibleBy(%s) ignored an ancestor without the search bit", bin)
	}
	if err := os.Chmod(filepath.Join(base, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	// A file nobody can execute is not accessible to anybody, whatever the dirs.
	if err := os.Chmod(bin, 0644); err != nil {
		t.Fatal(err)
	}
	if accessibleBy(bin, me, myGroup) {
		t.Errorf("a 0644 file was reported executable by its owner")
	}
}

// TestAccessibleByFollowsTheSymlinkTarget: a per-user Claude Code install is a
// symlink into ~/.local/share, and the kernel needs search permission along
// that path too, not only along the link's own.
func TestAccessibleByFollowsTheSymlinkTarget(t *testing.T) {
	me, myGroup := os.Getuid(), os.Getgid()
	base := t.TempDir()
	private := filepath.Join(base, "share")
	target := stubBinary(t, private, "versions-2.1.296")
	link := filepath.Join(base, "bin", HarnessClaude)
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !accessibleBy(link, me, myGroup) {
		t.Fatalf("accessibleBy(%s) refused a link whose target is reachable", link)
	}
	unsearchable(t, private)
	if accessibleBy(link, me, myGroup) {
		t.Errorf("accessibleBy(%s) reported a link usable whose target sits behind an unsearchable directory", link)
	}
}

// TestDetectFromSkipsABlockedInstallAndReportsIt is the whole of the bug: the
// agent (root) found /root/.local/bin/claude first and handed the runner a path
// the runner could not execute. A usable install later in the order must win,
// and when there is none the blocked one must be named rather than dropped.
func TestDetectFromSkipsABlockedInstallAndReportsIt(t *testing.T) {
	account := RunAsAccount{Home: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}

	base := t.TempDir()
	// Executable for others, not for its owner: stat succeeds (as it does for the
	// root agent looking at /root/.local/bin/claude) while the account the test
	// stands in for cannot run it.
	blocked := stubBinary(t, filepath.Join(base, "root", ".local", "bin"), HarnessClaude)
	if err := os.Chmod(blocked, 0o011); err != nil {
		t.Fatal(err)
	}
	usable := stubBinary(t, filepath.Join(base, "usr", "local", "bin"), HarnessClaude)

	path, unusable := detectFrom(account, []string{blocked, usable})
	if path != usable || unusable != "" {
		t.Errorf("detectFrom([blocked, usable]) = (%q, %q), want (%q, \"\")", path, unusable, usable)
	}
	path, unusable = detectFrom(account, []string{blocked})
	if path != "" || unusable != blocked {
		t.Errorf("detectFrom([blocked]) = (%q, %q), want (\"\", %q)", path, unusable, blocked)
	}
	// The agent's own account runs whatever is an executable file; the walk is
	// for a DIFFERENT account, whose reach the agent cannot try out itself.
	own := RunAsAccount{Home: account.Home, UID: InheritUID, GID: InheritUID}
	if path, _ := detectFrom(own, []string{blocked}); path != blocked {
		t.Errorf("an inherited account did not accept its own install: %q", path)
	}
}

// TestCandidatesLookInTheRunnerAccountsHomeFirst: an install made AS the runner
// account is the one most certainly usable by it, so it comes before whatever
// the agent's PATH or the agent's own home offer.
func TestCandidatesLookInTheRunnerAccountsHomeFirst(t *testing.T) {
	home := t.TempDir()
	account := RunAsAccount{Home: home, UID: os.Getuid() + 1, GID: os.Getgid() + 1}
	got := candidates(HarnessClaude, account)
	if len(got) == 0 || got[0] != filepath.Join(home, ".local", "bin", HarnessClaude) {
		t.Fatalf("candidates = %v; want the runner's ~/.local/bin first", got)
	}
	own := RunAsAccount{Home: home, UID: InheritUID, GID: InheritUID}
	for _, candidate := range candidates(HarnessClaude, own) {
		if filepath.Dir(filepath.Dir(filepath.Dir(candidate))) == home {
			t.Fatalf("an inherited account searched a separate runner home: %s", candidate)
		}
	}
}
