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

package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The runner is not given a checkout on the host: it is given a remote and
// keeps its own clone, which is the ordinary way a managed runner works.
func TestRunnerClonesARepositoryItWasNeverEnrolledWith(t *testing.T) {
	remote, commit := testGitSource(t)
	stateDir := t.TempDir()
	cfg := Config{StateDir: stateDir}
	request := StartRequest{TaskID: "task-clone", AttemptID: "attempt-clone", RepositoryID: "repo", BaseCommit: commit}

	workdir, err := prepareWorkspace(context.Background(), cfg, request, &RepositorySource{RemoteURL: remote})
	if err != nil {
		t.Fatalf("prepareWorkspace: %v", err)
	}
	if head := strings.TrimSpace(string(runGit(t, workdir, "rev-parse", "HEAD"))); head != commit {
		t.Fatalf("worktree HEAD = %s, want %s", head, commit)
	}
	cache := filepath.Join(stateDir, "repositories", "repo.git")
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("repository cache %s: err=%v", cache, err)
	}
}

// The clone is kept between attempts, so a second attempt on the same commit
// does not go back to the remote.
func TestASecondAttemptWorksFromTheRunnersOwnClone(t *testing.T) {
	remote, commit := testGitSource(t)
	stateDir := t.TempDir()
	cfg := Config{StateDir: stateDir}
	first := StartRequest{TaskID: "task-cached", AttemptID: "attempt-one", RepositoryID: "repo", BaseCommit: commit}
	if _, err := prepareWorkspace(context.Background(), cfg, first, &RepositorySource{RemoteURL: remote}); err != nil {
		t.Fatalf("first prepareWorkspace: %v", err)
	}
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}
	second := StartRequest{TaskID: "task-cached", AttemptID: "attempt-two", RepositoryID: "repo", BaseCommit: commit}
	workdir, err := prepareWorkspace(context.Background(), cfg, second, &RepositorySource{RemoteURL: remote})
	if err != nil {
		t.Fatalf("second prepareWorkspace with the remote gone: %v", err)
	}
	if head := strings.TrimSpace(string(runGit(t, workdir, "rev-parse", "HEAD"))); head != commit {
		t.Fatalf("worktree HEAD = %s, want %s", head, commit)
	}
}

func TestAnUnenrolledRepositoryWithoutACloneSourceIsRefused(t *testing.T) {
	_, commit := testGitSource(t)
	_, err := prepareWorkspace(context.Background(), Config{StateDir: t.TempDir()},
		StartRequest{TaskID: "task-none", AttemptID: "attempt-none", RepositoryID: "repo", BaseCommit: commit}, nil)
	if err == nil || !strings.Contains(err.Error(), "no clone source") {
		t.Fatalf("error = %v, want a refusal naming the missing clone source", err)
	}
}

// The credential is dispatch data. It must not reach the runner's durable
// state, and a retry that carries a freshly minted one is the same request,
// not an idempotency conflict.
func TestTheCloneCredentialIsNeitherPersistedNorFingerprinted(t *testing.T) {
	source, commit := testGitSource(t)
	stateDir := t.TempDir()
	// The checkout already holds the commit, so nothing is fetched here and
	// the test is about what the request leaves behind, not about Git.
	r, err := New(Config{RunnerID: "clone-credential", StateDir: stateDir, Token: "token", Listen: "127.0.0.1:0", Repositories: map[string]RepositoryConfig{
		"repo": {Source: source},
	}}, &fakeAdapter{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close() }()

	request := StartRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "start-credential",
		TaskID:          "task-credential",
		AttemptID:       "attempt-credential",
		AttemptEpoch:    1,
		RepositoryID:    "repo",
		BaseCommit:      commit,
		Instructions:    "work",
		ApprovedInput:   json.RawMessage(`{"provenance":{"source":"test"}}`),
		Repository:      &RepositorySource{RemoteURL: "https://github.example/owner/repo.git", Username: "x-access-token", Token: "first-secret"},
	}
	receipt, err := r.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	retry := request
	retry.Repository = &RepositorySource{RemoteURL: "https://github.example/owner/repo.git", Username: "x-access-token", Token: "second-secret"}
	again, err := r.Start(context.Background(), retry)
	if err != nil {
		t.Fatalf("retry with a freshly minted credential: %v", err)
	}
	if again.AttemptID != receipt.AttemptID {
		t.Fatalf("retry returned attempt %s, want %s", again.AttemptID, receipt.AttemptID)
	}

	state, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
	if err != nil {
		t.Fatalf("read runner state: %v", err)
	}
	for _, secret := range []string{"first-secret", "second-secret"} {
		if strings.Contains(string(state), secret) {
			t.Fatalf("runner state contains the clone credential %q", secret)
		}
	}
}

func TestCloneCredentialsTravelInTheEnvironmentAndOnlyToTheirOwnHost(t *testing.T) {
	credential := &gitCredential{Username: "x-access-token", Token: "s3cret"}

	https := environmentMap(fetchGitEnvironment("https://github.example/owner/repo.git", credential))
	if got := https["GIT_CONFIG_KEY_0"]; got != "http.https://github.example/.extraheader" {
		t.Fatalf("config key = %q", got)
	}
	if value := https["GIT_CONFIG_VALUE_0"]; !strings.HasPrefix(value, "Authorization: Basic ") {
		t.Fatalf("config value = %q, want a basic authorization header", value)
	}
	if https["GIT_CONFIG_COUNT"] != "1" {
		t.Fatalf("config count = %q", https["GIT_CONFIG_COUNT"])
	}

	// An SSH or local remote takes no bearer credential, and the runner must
	// not hand one to a transport that was never meant to carry it.
	ssh := environmentMap(fetchGitEnvironment("ssh://git@github.example/owner/repo.git", credential))
	if _, ok := ssh["GIT_CONFIG_COUNT"]; ok {
		t.Fatal("credential configured for an SSH remote")
	}
}

func TestACloneSourceMustBeAPlainURLWithoutEmbeddedCredentials(t *testing.T) {
	for name, source := range map[string]RepositorySource{
		"credentials in the URL": {RemoteURL: "https://user:pass@github.example/owner/repo.git"},
		"unsupported transport":  {RemoteURL: "ext::sh -c whoami"},
		"token over SSH":         {RemoteURL: "ssh://git@github.example/owner/repo.git", Token: "s3cret"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateCloneSource(source); err == nil {
				t.Fatal("clone source was accepted")
			}
		})
	}
}

// An approved base is often a pull-request head, which no branch points at.
// Asking the remote for the commit by ID is the only way to reach it.
func TestTheRunnerClonesACommitNoBranchPointsAt(t *testing.T) {
	remote, base := testGitSource(t)
	if err := os.WriteFile(filepath.Join(remote, "CHANGE.md"), []byte("proposed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, remote, "add", "CHANGE.md")
	runGit(t, remote, "commit", "-m", "proposed change")
	head := strings.TrimSpace(string(runGit(t, remote, "rev-parse", "HEAD")))
	runGit(t, remote, "update-ref", "refs/pull/1/head", head)
	runGit(t, remote, "reset", "--hard", base)

	stateDir := t.TempDir()
	workdir, err := prepareWorkspace(context.Background(), Config{StateDir: stateDir},
		StartRequest{TaskID: "task-pr", AttemptID: "attempt-pr", RepositoryID: "repo", BaseCommit: head},
		&RepositorySource{RemoteURL: remote})
	if err != nil {
		t.Fatalf("prepareWorkspace: %v", err)
	}
	if got := strings.TrimSpace(string(runGit(t, workdir, "rev-parse", "HEAD"))); got != head {
		t.Fatalf("worktree HEAD = %s, want the pull-request head %s", got, head)
	}
}
