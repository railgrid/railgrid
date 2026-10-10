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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// The environment an identity brings is dispatch data exactly like the
// identity itself: it reaches the adapter's launch, it never reaches anything
// the runner writes down, and a retry that brings a different value is the
// same request rather than an idempotency conflict.
func TestCredentialEnvironmentReachesTheLaunchAndNothingDurable(t *testing.T) {
	source, commit := testGitSource(t)
	seen := make(chan []harness.EnvironmentVariable, 1)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		seen <- append([]harness.EnvironmentVariable(nil), launch.Credential.Environment...)
		return harness.Result{Phase: string(PhaseCompleted), SessionID: "session-env"}, nil
	}}
	stateDir := t.TempDir()
	runner := newTestRunnerAt(t, adapter, stateDir, source, commit)

	const secret = "ghp_brought_for_one_launch"
	request := testStartRequest(commit)
	request.HarnessCredential.Environment = []EnvironmentVariable{{Name: "GH_TOKEN", Value: secret}}
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)

	got := <-seen
	if len(got) != 1 || got[0].Name != "GH_TOKEN" || got[0].Value != secret {
		t.Fatalf("launch environment = %+v, want the brought GH_TOKEN", got)
	}

	// Nothing durable: not the journal, not the state, not a worktree.
	if err := filepath.Walk(stateDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), secret) {
			t.Errorf("%s holds the brought credential", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A retry carrying a freshly minted value is the same request.
	request.HarnessCredential.Environment[0].Value = "ghp_minted_again"
	if _, err := runner.Start(context.Background(), request); err != nil {
		t.Fatalf("a retry with a different brought value was refused: %v", err)
	}
}

func TestCredentialEnvironmentValidation(t *testing.T) {
	for _, test := range []struct {
		name      string
		variables []EnvironmentVariable
		wantErr   string
	}{
		{"none", nil, ""},
		{"github token", []EnvironmentVariable{{Name: "GH_TOKEN", Value: "x"}, {Name: "GITHUB_TOKEN", Value: "x"}}, ""},
		{"lower-case name", []EnvironmentVariable{{Name: "gh_token", Value: "x"}}, "not an upper-case variable name"},
		{"empty name", []EnvironmentVariable{{Name: "", Value: "x"}}, "not an upper-case variable name"},
		// Everything outside the allow-list is refused, whatever it would do:
		// relocate the child, re-authenticate it, run code at start, redirect
		// its traffic, or send the token to another host.
		{"home", []EnvironmentVariable{{Name: "HOME", Value: "/tmp"}}, "not allowed"},
		{"path", []EnvironmentVariable{{Name: "PATH", Value: "/evil"}}, "not allowed"},
		{"harness credential", []EnvironmentVariable{{Name: "ANTHROPIC_API_KEY", Value: "x"}}, "not allowed"},
		{"claude config", []EnvironmentVariable{{Name: "CLAUDE_CONFIG_DIR", Value: "/x"}}, "not allowed"},
		{"git config", []EnvironmentVariable{{Name: "GIT_CONFIG_GLOBAL", Value: "/x"}}, "not allowed"},
		{"git transport", []EnvironmentVariable{{Name: "GIT_SSH_COMMAND", Value: "sh"}}, "not allowed"},
		{"loader", []EnvironmentVariable{{Name: "LD_PRELOAD", Value: "/x.so"}}, "not allowed"},
		{"dyld", []EnvironmentVariable{{Name: "DYLD_INSERT_LIBRARIES", Value: "/x"}}, "not allowed"},
		{"xdg", []EnvironmentVariable{{Name: "XDG_CONFIG_HOME", Value: "/x"}}, "not allowed"},
		{"node options", []EnvironmentVariable{{Name: "NODE_OPTIONS", Value: "--require /x.js"}}, "not allowed"},
		{"bash env", []EnvironmentVariable{{Name: "BASH_ENV", Value: "/x.sh"}}, "not allowed"},
		{"proxy", []EnvironmentVariable{{Name: "HTTPS_PROXY", Value: "http://mitm:8080"}}, "not allowed"},
		{"extra ca certs", []EnvironmentVariable{{Name: "NODE_EXTRA_CA_CERTS", Value: "/x.pem"}}, "not allowed"},
		{"gh host", []EnvironmentVariable{{Name: "GH_HOST", Value: "evil.example"}}, "not allowed"},
		{"gh config dir", []EnvironmentVariable{{Name: "GH_CONFIG_DIR", Value: "/x"}}, "not allowed"},
		{"github api url", []EnvironmentVariable{{Name: "GITHUB_API_URL", Value: "https://evil.example"}}, "not allowed"},
		{"duplicate", []EnvironmentVariable{{Name: "GH_TOKEN", Value: "a"}, {Name: "GH_TOKEN", Value: "b"}}, "twice"},
		{"empty value", []EnvironmentVariable{{Name: "GH_TOKEN", Value: ""}}, "is empty"},
		{"newline", []EnvironmentVariable{{Name: "GH_TOKEN", Value: "a\nb"}}, "invalid whitespace"},
		{"nul", []EnvironmentVariable{{Name: "GH_TOKEN", Value: "a\x00b"}}, "invalid whitespace"},
		{"oversized", []EnvironmentVariable{{Name: "GH_TOKEN", Value: strings.Repeat("x", maxHarnessCredentialBytes+1)}}, "oversized"},
		{"too many", func() []EnvironmentVariable {
			out := make([]EnvironmentVariable, 0, maxCredentialEnvironment+1)
			for i := 0; i <= maxCredentialEnvironment; i++ {
				out = append(out, EnvironmentVariable{Name: "GH_TOKEN", Value: "x"})
			}
			return out
		}(), "more than"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := harnessCredentialOf(&HarnessCredential{Kind: string(harness.CredentialClaudeOAuth), Value: "tok", Environment: test.variables})
			switch {
			case test.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)):
				t.Fatalf("error = %v, want one containing %q", err, test.wantErr)
			}
		})
	}
}
