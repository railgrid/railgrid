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

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFetchRemoteURL(t *testing.T) {
	local := filepath.Join(t.TempDir(), "remote.git")
	fileURL := "file://" + filepath.ToSlash(local)
	valid := []string{
		local,
		fileURL,
		"https://github.com/faroshq/faros.git",
		"ssh://git@example.com/faroshq/faros.git",
		"git@example.com:faroshq/faros.git",
	}
	for _, value := range valid {
		t.Run("valid/"+value, func(t *testing.T) {
			if _, err := validateFetchRemoteURL(value); err != nil {
				t.Fatalf("validateFetchRemoteURL(%q): %v", value, err)
			}
		})
	}
	invalid := []string{
		"--upload-pack=sh -c evil",
		"ext::sh -c evil",
		"ext::/bin/sh",
		"http://example.com/repo.git",
		"https://user@example.com/repo.git",
		"https://user:secret@example.com/repo.git",
		"https://example.com/repo.git?token=secret",
		"ssh://git:secret@example.com/repo.git",
		"git@example.com:repo.git --upload-pack=evil",
		"https://example.com/repo.git\nX-Injected: yes",
	}
	for _, value := range invalid {
		t.Run("invalid/"+strings.NewReplacer("/", "_", ":", "_", "?", "_").Replace(value), func(t *testing.T) {
			if _, err := validateFetchRemoteURL(value); err == nil {
				t.Fatalf("validateFetchRemoteURL(%q) accepted an unsafe remote", value)
			}
		})
	}
}

func TestPrepareWorkspaceFetchesExactMissingCommitWithoutMutatingSource(t *testing.T) {
	source, sourceCommit := testGitSource(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, t.TempDir(), "clone", "--bare", source, remote)

	publisher := filepath.Join(t.TempDir(), "publisher")
	runGit(t, t.TempDir(), "clone", source, publisher)
	if err := os.WriteFile(filepath.Join(publisher, "remote-only.txt"), []byte("fetched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publisher, "add", "remote-only.txt")
	runGit(t, publisher, "-c", "user.name=Runner Test", "-c", "user.email=runner-test@example.invalid", "commit", "-m", "remote commit")
	targetCommit := strings.TrimSpace(string(runGit(t, publisher, "rev-parse", "HEAD")))
	runGit(t, publisher, "remote", "set-url", "origin", remote)
	runGit(t, publisher, "push", "origin", targetCommit+":refs/heads/main")

	stateDir := t.TempDir()
	workdir, err := prepareWorkspace(context.Background(), Config{
		StateDir: stateDir,
		Repositories: map[string]RepositoryConfig{
			"repo": {Source: source, FetchRemoteURL: remote},
		},
	}, StartRequest{TaskID: "task-fetch", AttemptID: "attempt-fetch", RepositoryID: "repo", BaseCommit: targetCommit})
	if err != nil {
		t.Fatalf("prepareWorkspace: %v", err)
	}
	if head := strings.TrimSpace(string(runGit(t, workdir, "rev-parse", "HEAD"))); head != targetCommit {
		t.Fatalf("fetched worktree HEAD = %s, want %s", head, targetCommit)
	}
	if status := string(runGit(t, workdir, "status", "--porcelain=v1")); status != "" {
		t.Fatalf("fetched worktree status = %q", status)
	}
	if head := strings.TrimSpace(string(runGit(t, source, "rev-parse", "HEAD"))); head != sourceCommit {
		t.Fatalf("enrolled source HEAD = %s, want %s", head, sourceCommit)
	}
	if status := string(runGit(t, source, "status", "--porcelain=v1", "--untracked-files=all")); status != "" {
		t.Fatalf("enrolled source status changed: %q", status)
	}
}

func TestPrepareWorkspaceRefetchesSourceObjectOmittedByClone(t *testing.T) {
	source, sourceCommit := testGitSource(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, t.TempDir(), "clone", "--bare", source, remote)

	publisher := filepath.Join(t.TempDir(), "publisher")
	runGit(t, t.TempDir(), "clone", source, publisher)
	if err := os.WriteFile(filepath.Join(publisher, "unreachable.txt"), []byte("fetch head only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, publisher, "add", "unreachable.txt")
	runGit(t, publisher, "-c", "user.name=Runner Test", "-c", "user.email=runner-test@example.invalid", "commit", "-m", "unreachable commit")
	targetCommit := strings.TrimSpace(string(runGit(t, publisher, "rev-parse", "HEAD")))
	runGit(t, publisher, "remote", "set-url", "origin", remote)
	runGit(t, publisher, "push", "origin", targetCommit+":refs/heads/main")

	// Put the target only in the enrolled source's object database and
	// FETCH_HEAD. A clone advertises the source refs, so it must not inherit
	// this unreachable object and must use the opted-in remote again.
	runGit(t, source, "fetch", remote, targetCommit)
	if got := strings.TrimSpace(string(runGit(t, source, "rev-parse", "FETCH_HEAD"))); got != targetCommit {
		t.Fatalf("source FETCH_HEAD = %s, want %s", got, targetCommit)
	}
	if got := strings.TrimSpace(string(runGit(t, source, "rev-parse", "HEAD"))); got != sourceCommit {
		t.Fatalf("source HEAD = %s, want %s", got, sourceCommit)
	}

	stateDir := t.TempDir()
	workdir, err := prepareWorkspace(context.Background(), Config{
		StateDir: stateDir,
		Repositories: map[string]RepositoryConfig{
			"repo": {Source: source, FetchRemoteURL: remote},
		},
	}, StartRequest{TaskID: "task-unreachable", AttemptID: "attempt-unreachable", RepositoryID: "repo", BaseCommit: targetCommit})
	if err != nil {
		t.Fatalf("prepareWorkspace: %v", err)
	}
	if got := strings.TrimSpace(string(runGit(t, workdir, "rev-parse", "HEAD"))); got != targetCommit {
		t.Fatalf("refetched worktree HEAD = %s, want %s", got, targetCommit)
	}
}

func TestPrepareWorkspaceDoesNotFetchWhenDisabledOrBaseCommitIsNotEnrolled(t *testing.T) {
	source, sourceCommit := testGitSource(t)
	missingCommit := strings.Repeat("f", 40)
	stateDir := t.TempDir()
	_, err := prepareWorkspace(context.Background(), Config{
		StateDir: stateDir,
		Repositories: map[string]RepositoryConfig{
			"repo": {Source: source, BaseCommit: sourceCommit},
		},
	}, StartRequest{TaskID: "task-disabled", AttemptID: "attempt-disabled", RepositoryID: "repo", BaseCommit: missingCommit})
	if err == nil || !strings.Contains(err.Error(), "exact commit enrolled") {
		t.Fatalf("disabled or unenrolled commit error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "worktrees")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("disabled fetch created worktree state: %v", statErr)
	}
}

func TestPrepareWorkspaceRejectsMissingCommitWithoutFetchRemote(t *testing.T) {
	source, sourceCommit := testGitSource(t)
	missingCommit := strings.Repeat("f", 40)
	stateDir := t.TempDir()
	_, err := prepareWorkspace(context.Background(), Config{
		StateDir: stateDir,
		Repositories: map[string]RepositoryConfig{
			"repo": {Source: source, BaseCommit: ""},
		},
	}, StartRequest{TaskID: "task-no-fetch", AttemptID: "attempt-no-fetch", RepositoryID: "repo", BaseCommit: missingCommit})
	if err == nil || !strings.Contains(err.Error(), "not available in the enrolled source") {
		t.Fatalf("missing commit error = %v", err)
	}
	if head := strings.TrimSpace(string(runGit(t, source, "rev-parse", "HEAD"))); head != sourceCommit {
		t.Fatalf("source HEAD changed after disabled fetch: %s", head)
	}
}

func TestPrepareWorkspaceMissingFetchedCommitReturnsFixedError(t *testing.T) {
	source, _ := testGitSource(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, t.TempDir(), "clone", "--bare", source, remote)
	missingCommit := strings.Repeat("f", 40)
	_, err := prepareWorkspace(context.Background(), Config{
		StateDir: t.TempDir(),
		Repositories: map[string]RepositoryConfig{
			"repo": {Source: source, FetchRemoteURL: remote},
		},
	}, StartRequest{TaskID: "task-fetch-missing", AttemptID: "attempt-fetch-missing", RepositoryID: "repo", BaseCommit: missingCommit})
	if err == nil || err.Error() != "git fetch failed" {
		t.Fatalf("missing fetched commit error = %v, want fixed fetch error", err)
	}
}

func TestFetchCapabilityRequiresOptedInRepository(t *testing.T) {
	source, commit := testGitSource(t)
	without, err := New(Config{RunnerID: "without-fetch", StateDir: t.TempDir(), Token: "token", Listen: "127.0.0.1:0", Repositories: map[string]RepositoryConfig{
		"repo": {Source: source, BaseCommit: commit},
	}}, &fakeAdapter{})
	if err != nil {
		t.Fatalf("New without fetch: %v", err)
	}
	defer func() { _ = without.Close() }()
	if contains(without.Capabilities().Verification, gitFetchCapability) {
		t.Fatal("git-fetch capability advertised without an opted-in repository")
	}

	with, err := New(Config{RunnerID: "with-fetch", StateDir: t.TempDir(), Token: "token", Listen: "127.0.0.1:0", Repositories: map[string]RepositoryConfig{
		"repo": {Source: source, BaseCommit: commit, FetchRemoteURL: source},
	}}, &fakeAdapter{})
	if err != nil {
		t.Fatalf("New with fetch: %v", err)
	}
	defer func() { _ = with.Close() }()
	if !contains(with.Capabilities().Verification, gitFetchCapability) {
		t.Fatal("git-fetch capability omitted for an opted-in repository")
	}
}

func TestFetchGitEnvironmentScopesCredentialsToSSHFetch(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "github-secret")
	t.Setenv("GH_TOKEN", "gh-secret")
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /secret/key")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/runner-agent.sock")
	t.Setenv("SSH_ASKPASS", "/tmp/interactive-askpass")
	t.Setenv("SSH_ASKPASS_REQUIRE", "force")
	sshEnv := environmentMap(fetchGitEnvironment("ssh://git@example.com/repo.git"))
	if sshEnv["SSH_AUTH_SOCK"] != "/tmp/runner-agent.sock" {
		t.Fatalf("SSH fetch environment omitted local agent: %q", sshEnv["SSH_AUTH_SOCK"])
	}
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GIT_SSH_COMMAND", "SSH_AGENT_PID", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE"} {
		if sshEnv[key] != "" {
			t.Fatalf("SSH fetch environment leaked %s", key)
		}
	}
	if sshEnv["GIT_ALLOW_PROTOCOL"] != "ssh" {
		t.Fatalf("SSH fetch protocol allowlist = %q", sshEnv["GIT_ALLOW_PROTOCOL"])
	}
	for _, option := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "ForwardAgent=no", "ClearAllForwardings=yes"} {
		if !strings.Contains(secureSSHCommand, option) {
			t.Fatalf("secure SSH command omitted %s: %q", option, secureSSHCommand)
		}
	}

	httpsEnv := environmentMap(fetchGitEnvironment("https://example.com/repo.git"))
	if httpsEnv["SSH_AUTH_SOCK"] != "" {
		t.Fatal("HTTPS fetch environment forwarded SSH agent")
	}
	if httpsEnv["GIT_ALLOW_PROTOCOL"] != "https" {
		t.Fatalf("HTTPS fetch protocol allowlist = %q", httpsEnv["GIT_ALLOW_PROTOCOL"])
	}
}

func TestBoundedGitFetchErrorIsSanitized(t *testing.T) {
	if err := boundedGitFetchError("remote", strings.Repeat("server secret ", maxGitFetchErrorBytes), errors.New("raw failure"), nil); err == nil || err.Error() != "git fetch failed" {
		t.Fatalf("fetch failure = %v, want fixed error", err)
	}
	if err := boundedGitFetchError("remote", "server secret", errors.New("raw failure"), context.DeadlineExceeded); err == nil || err.Error() != "git fetch timed out" {
		t.Fatalf("fetch timeout = %v, want fixed error", err)
	}
	if err := boundedGitFetchError("remote", "server secret", errors.New("raw failure"), context.Canceled); err == nil || err.Error() != "git fetch canceled" {
		t.Fatalf("fetch cancellation = %v, want fixed error", err)
	}
}

func environmentMap(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		if ok {
			result[key] = item
		}
	}
	return result
}
