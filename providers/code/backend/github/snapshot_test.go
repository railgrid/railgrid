// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package github

import (
	"context"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/provider-code/backend"
)

// snapshotDate is when the fixture runner took its snapshot: now, as a real
// runner stamps it.
var snapshotDate = time.Now().UTC().Format(time.RFC3339)

func snapshotCommand(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Stdin = strings.NewReader(stdin)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Railgrid Runner", "GIT_AUTHOR_EMAIL=runner@localhost", "GIT_AUTHOR_DATE=" + snapshotDate, "GIT_COMMITTER_NAME=Railgrid Runner", "GIT_COMMITTER_EMAIL=runner@localhost", "GIT_COMMITTER_DATE=" + snapshotDate}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// rawCommit writes a commit object verbatim, for shapes git's porcelain would
// never produce, and bundles it as the runner would.
func rawCommit(t *testing.T, source, object string) (string, []byte) {
	t.Helper()
	commit := snapshotCommand(t, source, object, "hash-object", "-t", "commit", "-w", "--stdin")
	snapshotCommand(t, source, "", "update-ref", "refs/heads/runner-result", commit)
	path := filepath.Join(source, commit+".bundle")
	parent := strings.TrimPrefix(strings.Split(object, "\n")[1], "parent ")
	snapshotCommand(t, source, "", "bundle", "create", "--version=2", path, "refs/heads/runner-result", "^"+parent)
	bundle, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return commit, bundle
}

func TestSnapshotVerifiesActualGitBundleAndCanonicalMetadata(t *testing.T) {
	source := t.TempDir()
	snapshotCommand(t, source, "", "init", "--bare", ".")
	tree := snapshotCommand(t, source, "", "mktree")
	base := snapshotCommand(t, source, "base\n", "commit-tree", tree)
	commit := snapshotCommand(t, source, "Implementation snapshot\n", "commit-tree", tree, "-p", base)
	snapshotCommand(t, source, "", "update-ref", "refs/heads/runner-result", commit)
	bundlePath := filepath.Join(source, "result.bundle")
	snapshotCommand(t, source, "", "bundle", "create", "--version=2", bundlePath, "refs/heads/runner-result", "^"+base)
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	identity := func(unix int64) string {
		return "Railgrid Runner <runner@localhost> " + strconv.FormatInt(unix, 10) + " +0000"
	}
	canonical := func(author, committer string) string {
		return "tree " + tree + "\nparent " + base + "\nauthor " + author + "\ncommitter " + committer + "\n\nImplementation snapshot\n"
	}
	now := time.Now().Unix()
	for _, kind := range []string{"valid", "wrong tree", "wrong base", "wrong commit", "private metadata", "truncated bundle", "epoch date", "future date", "author and committer differ", "extra header"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			snapshotCommand(t, dir, "", "init", "--bare", ".")
			snapshotCommand(t, dir, "", "fetch", source, base)
			input := backend.Snapshot{BaseCommit: base, Commit: commit, Tree: tree, Bundle: bundle}
			switch kind {
			case "wrong tree":
				input.Tree = strings.Repeat("1", 40)
			case "wrong base":
				input.BaseCommit = strings.Repeat("1", 40)
			case "wrong commit":
				input.Commit = strings.Repeat("1", 40)
			case "truncated bundle":
				input.Bundle = bundle[:len(bundle)/2]
			case "private metadata":
				input.Commit = snapshotCommand(t, source, "private implementation notes\n", "commit-tree", tree, "-p", base)
				snapshotCommand(t, source, "", "update-ref", "refs/heads/runner-result", input.Commit)
				other := filepath.Join(source, "private.bundle")
				snapshotCommand(t, source, "", "bundle", "create", "--version=2", other, "refs/heads/runner-result", "^"+base)
				input.Bundle, err = os.ReadFile(other)
				if err != nil {
					t.Fatal(err)
				}
			case "epoch date":
				// The pre-2026 fixed stamp: a commit "from 2000" is a fabrication now.
				input.Commit, input.Bundle = rawCommit(t, source, canonical(identity(946684800), identity(946684800)))
			case "future date":
				input.Commit, input.Bundle = rawCommit(t, source, canonical(identity(now+2*int64(snapshotClockSkew/time.Second)), identity(now+2*int64(snapshotClockSkew/time.Second))))
			case "author and committer differ":
				input.Commit, input.Bundle = rawCommit(t, source, canonical(identity(now), identity(now-60)))
			case "extra header":
				object := "tree " + tree + "\nparent " + base + "\nauthor " + identity(now) + "\ncommitter " + identity(now) + "\nencoding utf-8\n\nImplementation snapshot\n"
				input.Commit, input.Bundle = rawCommit(t, source, object)
			}
			err := (&snapshotGit{dir: dir}).verify(context.Background(), input)
			if kind == "valid" && err != nil {
				t.Fatal(err)
			}
			if kind != "valid" && err == nil {
				t.Fatalf("accepted %s", kind)
			}
		})
	}
}
func TestBranchValidationRejectsRefAndOptionInjection(t *testing.T) {
	for _, branch := range []string{"", "--all", "refs/../main", "main.lock", ".hidden/main", "head@{1}", "main\nother", "a//b", "main:other"} {
		if validBranch(branch) {
			t.Errorf("accepted %q", branch)
		}
	}
	for _, branch := range []string{"main", "feature/example", "factory/attempt-1"} {
		if !validBranch(branch) {
			t.Errorf("rejected %q", branch)
		}
	}
}

func TestSnapshotPushUsesAtomicAbsentAndExpectedHeadLeases(t *testing.T) {
	root := t.TempDir()
	remoteDir := filepath.Join(root, "remote.git")
	if err := os.Mkdir(remoteDir, 0700); err != nil {
		t.Fatal(err)
	}
	snapshotCommand(t, remoteDir, "", "init", "--bare", ".")
	snapshotCommand(t, remoteDir, "", "config", "http.receivepack", "true")
	executable, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(&cgi.Handler{Path: executable, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}})
	defer server.Close()
	dir := t.TempDir()
	snapshotCommand(t, dir, "", "init", "--bare", ".")
	tree := snapshotCommand(t, dir, "", "mktree")
	first := snapshotCommand(t, dir, "first\n", "commit-tree", tree)
	second := snapshotCommand(t, dir, "second\n", "commit-tree", tree, "-p", first)
	third := snapshotCommand(t, dir, "third\n", "commit-tree", tree, "-p", first)
	g := &snapshotGit{dir: dir}
	remote := server.URL + "/remote.git"
	ctx := context.Background()
	if err := g.advance(ctx, remote, first, "feature", ""); err != nil {
		t.Fatal(err)
	}
	if err := g.advance(ctx, remote, second, "feature", ""); err == nil {
		t.Fatal("absent lease overwrote existing branch")
	}
	if err := g.advance(ctx, remote, second, "feature", first); err != nil {
		t.Fatal(err)
	}
	if err := g.advance(ctx, remote, third, "feature", first); err == nil {
		t.Fatal("stale lease overwrote concurrent branch update")
	}
	if got := snapshotCommand(t, remoteDir, "", "rev-parse", "refs/heads/feature"); got != second {
		t.Fatalf("unexpected remote head %s", got)
	}
}
