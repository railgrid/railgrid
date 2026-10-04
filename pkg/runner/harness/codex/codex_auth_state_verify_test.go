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

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func TestProbeStaysReadyWithoutACodexLogin(t *testing.T) {
	// The credential arrives per attempt, so a host that has never signed in
	// must still probe ready or the runner would be permanently unready.
	binary := fakeCodexBinary(t, "auth")
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.155.1"})

	info, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !info.Ready || len(info.Reasons) != 0 {
		t.Fatalf("signed-out host reported unready: %+v", info)
	}
	methods := readMethods(t, filepath.Join(filepath.Dir(binary), "methods"))
	for _, method := range methods {
		if method == "thread/start" || method == "thread/resume" || method == "turn/start" {
			t.Fatalf("probe started model work: %v", methods)
		}
	}
}

func TestRunMaterializesCredentialAsAuthSessionAndRemovesIt(t *testing.T) {
	const secret = "codex-session-value-that-must-not-escape"
	session := `{"tokens":{"access_token":"` + secret + `"},"last_refresh":"2026-01-01T00:00:00Z"}`
	binary := fakeCodexBinary(t, "success")
	home := t.TempDir()
	adapter := New(Config{Binary: binary, Home: home, ExpectedVersion: "0.155.1"})
	authPath := filepath.Join(home, codexAuthFileName)

	var events []harness.Event
	observed := false
	result, err := adapter.Run(context.Background(), harness.Launch{
		AttemptID:    "attempt-auth",
		Workdir:      t.TempDir(),
		Instructions: "say hello",
		Credential:   harness.Credential{Kind: harness.CredentialCodexAuth, Value: session},
	}, func(event harness.Event) error {
		events = append(events, event)
		if observed {
			return nil
		}
		observed = true
		info, statErr := os.Lstat(authPath)
		if statErr != nil {
			t.Errorf("auth session missing while the launch was running: %v", statErr)
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Errorf("auth session mode = %v, want a regular 0600 file", info.Mode())
		}
		data, readErr := os.ReadFile(authPath)
		if readErr != nil || string(data) != session {
			t.Errorf("auth session contents = %q (err %v), want the launch credential", data, readErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "completed" || result.SessionID != "thread-1" {
		t.Fatalf("credentialed run result = %+v", result)
	}
	if !observed {
		t.Fatal("no event arrived while the launch was running")
	}
	if _, statErr := os.Lstat(authPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("auth session survived the launch: %v", statErr)
	}
	reported, err := json.Marshal(struct {
		Events []harness.Event `json:"events"`
		Result harness.Result  `json:"result"`
	}{Events: events, Result: result})
	if err != nil {
		t.Fatalf("marshal reported launch data: %v", err)
	}
	if strings.Contains(string(reported), secret) {
		t.Fatalf("launch reporting exposed the credential: %s", reported)
	}
}

func TestRunRefusesUnusableCredentialWithoutStartingAProcess(t *testing.T) {
	for _, tc := range []struct {
		name        string
		credential  harness.Credential
		wantBlocker string
	}{
		{
			name:        "missing",
			wantBlocker: "was not supplied",
		},
		{
			name:        "wrong kind",
			credential:  harness.Credential{Kind: harness.CredentialClaudeOAuth, Value: `{"tokens":{}}`},
			wantBlocker: "requires a codex-auth credential",
		},
		{
			name:        "not a json object",
			credential:  harness.Credential{Kind: harness.CredentialCodexAuth, Value: "not-json"},
			wantBlocker: "not a Codex auth.json object",
		},
		{
			name:        "trailing content",
			credential:  harness.Credential{Kind: harness.CredentialCodexAuth, Value: `{"tokens":{}} {"tokens":{}}`},
			wantBlocker: "not a Codex auth.json object",
		},
		{
			name:        "oversized",
			credential:  harness.Credential{Kind: harness.CredentialCodexAuth, Value: `{"t":"` + strings.Repeat("x", maxCodexAuthBytes) + `"}`},
			wantBlocker: "exceeds the supported size",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := fakeCodexBinary(t, "success")
			home := t.TempDir()
			adapter := New(Config{Binary: binary, Home: home, ExpectedVersion: "0.155.1"})
			var events []harness.Event
			result, err := adapter.Run(context.Background(), harness.Launch{
				AttemptID:    "attempt-no-credential",
				Workdir:      t.TempDir(),
				SessionID:    "thread-existing",
				Instructions: "say hello",
				Credential:   tc.credential,
			}, func(event harness.Event) error {
				events = append(events, event)
				return nil
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Phase != "needs_input" || result.SessionID != "thread-existing" {
				t.Fatalf("refusal result = %+v, want needs_input for the existing session", result)
			}
			if !strings.Contains(result.Blocker, tc.wantBlocker) {
				t.Fatalf("refusal blocker = %q, want it to mention %q", result.Blocker, tc.wantBlocker)
			}
			if result.Clarification != nil {
				t.Fatalf("refusal became a product question: %+v", result.Clarification)
			}
			if len(events) != 1 || events[0].Type != "auth_failure" || events[0].Message != result.Blocker {
				t.Fatalf("refusal events = %+v, want one auth_failure carrying the blocker", events)
			}
			if _, statErr := os.Lstat(filepath.Join(home, codexAuthFileName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("refused launch wrote an auth session: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(binary), "methods")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("refused launch started an app-server: %v", statErr)
			}
		})
	}
}

func TestRunRefusesAuthSessionPathThatIsNotARegularFile(t *testing.T) {
	binary := fakeCodexBinary(t, "success")
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, codexAuthFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := New(Config{Binary: binary, Home: home, ExpectedVersion: "0.155.1"})
	_, err := adapter.Run(context.Background(), harness.Launch{
		AttemptID:    "attempt-bad-auth-path",
		Workdir:      t.TempDir(),
		Instructions: "say hello",
		Credential:   testCredential(),
	}, nil)
	if err == nil {
		t.Fatal("Run accepted an auth session path that is not a regular file")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(binary), "methods")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected auth session path still started an app-server: %v", statErr)
	}
}
