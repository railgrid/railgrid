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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchExactCommitUsesNonInteractiveSSHAndHidesRemoteDiagnostics(t *testing.T) {
	tmp := t.TempDir()
	argsPath := filepath.Join(tmp, "args")
	envPath := filepath.Join(tmp, "env")
	fakeGit := filepath.Join(tmp, "git")
	script := "#!/bin/sh\n" +
		"set -eu\n" +
		"printf '%s\\n' \"$@\" > \"$FETCH_ARGS_PATH\"\n" +
		"env | sort > \"$FETCH_ENV_PATH\"\n" +
		"printf '%s\\n' 'remote credential and host diagnostic' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FETCH_ARGS_PATH", argsPath)
	t.Setenv("FETCH_ENV_PATH", envPath)
	t.Setenv("SSH_AUTH_SOCK", "/tmp/runner-agent.sock")
	t.Setenv("SSH_AGENT_PID", "1234")
	t.Setenv("SSH_ASKPASS", "/tmp/askpass")
	t.Setenv("SSH_ASKPASS_REQUIRE", "force")
	t.Setenv("GIT_ASKPASS", "/tmp/git-askpass")
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /tmp/private-key")
	t.Setenv("GH_TOKEN", "gh-secret")
	t.Setenv("GITHUB_TOKEN", "github-secret")

	commit := strings.Repeat("a", 40)
	err := fetchExactCommit(context.Background(), tmp, "ssh://git@example.com/repo.git", commit)
	if err == nil || err.Error() != "git fetch failed" {
		t.Fatalf("fetchExactCommit error = %v, want fixed fetch error", err)
	}
	if strings.Contains(err.Error(), "remote credential") || strings.Contains(err.Error(), "github-secret") {
		t.Fatalf("fetchExactCommit leaked remote diagnostic: %v", err)
	}

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	argLines := strings.Split(strings.TrimSpace(string(args)), "\n")
	for _, want := range []string{
		"-c",
		"core.sshCommand=" + secureSSHCommand,
		"fetch",
		"--no-tags",
		"--no-prune",
		"--no-write-fetch-head",
		"ssh://git@example.com/repo.git",
		commit,
	} {
		if !contains(argLines, want) {
			t.Fatalf("git invocation args = %q, missing %q", argLines, want)
		}
	}

	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	env := environmentMap(strings.Split(strings.TrimSpace(string(envBytes)), "\n"))
	if env["SSH_AUTH_SOCK"] != "/tmp/runner-agent.sock" {
		t.Fatalf("SSH_AUTH_SOCK = %q, want explicit SSH agent", env["SSH_AUTH_SOCK"])
	}
	for _, key := range []string{
		"SSH_AGENT_PID",
		"SSH_ASKPASS",
		"SSH_ASKPASS_REQUIRE",
		"GIT_ASKPASS",
		"GIT_SSH_COMMAND",
		"GH_TOKEN",
		"GITHUB_TOKEN",
	} {
		if env[key] != "" {
			t.Fatalf("fetch environment leaked %s=%q", key, env[key])
		}
	}
	if env["GIT_TERMINAL_PROMPT"] != "0" || env["GIT_ALLOW_PROTOCOL"] != "ssh" {
		t.Fatalf("fetch environment interactivity/protocol = terminal=%q protocol=%q", env["GIT_TERMINAL_PROMPT"], env["GIT_ALLOW_PROTOCOL"])
	}
}
