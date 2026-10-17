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
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// Opt-in acceptance test. It uses an isolated home and logs only model/effort
// metadata; credentials and session contents are never printed.
func TestLiveReasoningEffortSessionMetadata(t *testing.T) {
	authFile := os.Getenv("RAILGRID_CODEX_LIVE_AUTH_FILE")
	if authFile == "" {
		t.Skip("set RAILGRID_CODEX_LIVE_AUTH_FILE to run live acceptance")
	}
	auth, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatal("cannot read live credential")
	}
	home, workdir := t.TempDir(), t.TempDir()
	adapter := New(Config{Home: home})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	launch := harness.Launch{AttemptID: "effort-acceptance", Workdir: workdir, Instructions: "Reply with OK. Do not use any tools.", Model: "gpt-6-luna", ReasoningEffort: "max", Credential: harness.Credential{Kind: harness.CredentialCodexAuth, Value: string(auth)}}
	for turn := 0; turn < 2; turn++ {
		result, err := adapter.Run(ctx, launch, func(harness.Event) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		if result.Phase != "completed" {
			t.Fatalf("turn %d phase=%s", turn, result.Phase)
		}
		launch.SessionID = result.SessionID
	}
	contexts := 0
	err = filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			var item struct {
				Type    string `json:"type"`
				Payload struct {
					Model             string `json:"model"`
					Effort            string `json:"effort"`
					CollaborationMode struct {
						Settings struct {
							ReasoningEffort string `json:"reasoning_effort"`
						} `json:"settings"`
					} `json:"collaboration_mode"`
				} `json:"payload"`
			}
			if json.Unmarshal(scanner.Bytes(), &item) != nil || item.Type != "turn_context" {
				continue
			}
			effort := item.Payload.CollaborationMode.Settings.ReasoningEffort
			if effort == "" {
				effort = item.Payload.Effort
			}
			if item.Payload.Model != "gpt-6-luna" || effort != "max" {
				t.Fatalf("session metadata model=%q effort=%q", item.Payload.Model, effort)
			}
			contexts++
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if contexts < 2 {
		t.Fatalf("observed %d turn contexts, want new and resumed turns", contexts)
	}
	t.Logf("Verified %d persisted turn contexts: model=gpt-6-luna, reasoning effort=max", contexts)
}
