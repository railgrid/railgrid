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

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// workspaceStartRequest is the conversational shape: a persistent workspace and
// no repository, which is what an agent turn looks like.
func workspaceStartRequest(workspaceID string) StartRequest {
	return StartRequest{
		ProtocolVersion:   ProtocolVersion,
		RequestID:         "start-ws-1",
		TaskID:            "session-1",
		AttemptID:         "turn-1",
		AttemptEpoch:      1,
		WorkspaceID:       workspaceID,
		Instructions:      "answer the question",
		ApprovedInput:     json.RawMessage(`{"provenance":{"source":"agents"}}`),
		HarnessCredential: testHarnessCredential(),
	}
}

func TestWorkspaceAttemptRunsInAPersistentDirectoryWithoutGit(t *testing.T) {
	source, commit := testGitSource(t)
	var workdirs []string
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		workdirs = append(workdirs, launch.Workdir)
		// A turn leaves files behind; the next turn must see them.
		if err := os.WriteFile(filepath.Join(launch.Workdir, "notes.md"), []byte("kept\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: "sess-a"}, nil
	}}
	r := newTestRunner(t, adapter, source, commit)

	first, err := r.Start(context.Background(), workspaceStartRequest("agent-alpha"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, r, first.AttemptID, PhaseCompleted)

	second := workspaceStartRequest("agent-alpha")
	second.RequestID = "start-ws-2"
	second.AttemptID = "turn-2"
	// Each turn is a new dispatch of the same task, so the epoch increments:
	// that is what makes the previous turn unable to write over this one. A
	// conversational caller therefore maps session -> taskID, run -> attemptID,
	// and turn number -> epoch.
	second.AttemptEpoch = 2
	// The second turn continues the session the first one reported, which is
	// how consecutive turns stay one conversation.
	second.SessionID = "sess-a"
	receipt, err := r.Start(context.Background(), second)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	waitForPhase(t, r, receipt.AttemptID, PhaseCompleted)

	if len(workdirs) != 2 || workdirs[0] != workdirs[1] {
		t.Fatalf("workspace turns ran in %v, want one shared directory", workdirs)
	}
	if !strings.Contains(workdirs[0], filepath.Join("worktrees", workspacesDir, "agent-alpha")) {
		t.Fatalf("workspace path = %q, want it under the managed worktree root", workdirs[0])
	}
	if _, err := os.Stat(filepath.Join(workdirs[0], "notes.md")); err != nil {
		t.Fatalf("workspace did not survive the turn: %v", err)
	}
	// A workspace has no Git at all: nothing was cloned and nothing is pinned.
	if _, err := os.Stat(filepath.Join(workdirs[0], ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git in a workspace attempt: err=%v", err)
	}
}

func TestWorkspaceAttemptPassesTheRequestedSessionToTheHarness(t *testing.T) {
	source, commit := testGitSource(t)
	var launched []string
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		launched = append(launched, launch.SessionID)
		return harness.Result{Phase: string(PhaseCompleted), SessionID: "sess-forked"}, nil
	}}
	r := newTestRunner(t, adapter, source, commit)

	request := workspaceStartRequest("agent-beta")
	request.SessionID = "sess-previous"
	receipt, err := r.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, r, receipt.AttemptID, PhaseCompleted)

	if len(launched) != 1 || launched[0] != "sess-previous" {
		t.Fatalf("harness was launched with sessions %v, want the requested one", launched)
	}
	// A harness that forks the session on resume reports the new id, and the
	// receipt carries it so the caller chains the NEXT turn onto the real one
	// rather than onto a session that no longer exists.
	inspected, err := r.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inspected.SessionID != "sess-forked" {
		t.Fatalf("receipt session = %q, want the session the harness reported", inspected.SessionID)
	}
}

func TestStartRefusesAmbiguousAndIncompleteAttemptShapes(t *testing.T) {
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)

	for name, mutate := range map[string]func(*StartRequest){
		"both shapes": func(request *StartRequest) {
			request.WorkspaceID = "agent-gamma"
			request.RepositoryID = "repo"
			request.BaseCommit = commit
		},
		"neither shape": func(request *StartRequest) {
			request.WorkspaceID = ""
			request.RepositoryID = ""
			request.BaseCommit = ""
		},
		"workspace with a commit": func(request *StartRequest) {
			request.WorkspaceID = "agent-gamma"
			request.BaseCommit = commit
		},
		"workspace exporting a git result": func(request *StartRequest) {
			request.WorkspaceID = "agent-gamma"
			request.ExportGitResult = true
		},
		"workspace with a clone source": func(request *StartRequest) {
			request.WorkspaceID = "agent-gamma"
			request.Repository = &RepositorySource{RemoteURL: "https://github.example/o/r.git"}
		},
		"invalid workspace id": func(request *StartRequest) {
			request.WorkspaceID = "../escape"
		},
		"invalid session id": func(request *StartRequest) {
			request.WorkspaceID = "agent-gamma"
			request.SessionID = "not a session"
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := workspaceStartRequest("")
			mutate(&request)
			if _, err := r.Start(context.Background(), request); err == nil {
				t.Fatal("Start admitted an invalid attempt shape")
			} else {
				assertProtocolCode(t, err, ErrorInvalidRequest)
			}
		})
	}
}

func TestStartRequiresAUsableHarnessCredentialAndNeverPersistsIt(t *testing.T) {
	source, commit := testGitSource(t)
	var seen []harness.Credential
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		seen = append(seen, launch.Credential)
		return harness.Result{Phase: string(PhaseCompleted), SessionID: "sess-c"}, nil
	}}
	r := newTestRunner(t, adapter, source, commit)

	for name, credential := range map[string]*HarnessCredential{
		"absent":                           nil,
		"empty value":                      {Kind: string(harness.CredentialClaudeOAuth), Value: "  "},
		"unknown kind":                     {Kind: "vendor-token", Value: "x"},
		"newline in an env-injected value": {Kind: string(harness.CredentialClaudeAPIKey), Value: "sk-a\nsk-b"},
		"nul in a session file":            {Kind: string(harness.CredentialCodexAuth), Value: "{\"a\":\"b\x00\"}"},
	} {
		t.Run(name, func(t *testing.T) {
			request := workspaceStartRequest("agent-delta")
			request.HarnessCredential = credential
			if _, err := r.Start(context.Background(), request); err == nil {
				t.Fatal("Start admitted an attempt with no usable credential")
			} else {
				assertProtocolCode(t, err, ErrorInvalidRequest)
			}
		})
	}

	// A newline is legal INSIDE a Codex session file, which is a JSON document.
	accepted := workspaceStartRequest("agent-delta")
	accepted.HarnessCredential = &HarnessCredential{Kind: string(harness.CredentialCodexAuth), Value: "{\n  \"token\": \"t\"\n}"}
	receipt, err := r.Start(context.Background(), accepted)
	if err != nil {
		t.Fatalf("Start with a multi-line session file: %v", err)
	}
	waitForPhase(t, r, receipt.AttemptID, PhaseCompleted)
	if len(seen) != 1 || seen[0].Kind != harness.CredentialCodexAuth || seen[0].Value == "" {
		t.Fatalf("adapter received %+v, want the caller's credential", seen)
	}

	// The credential is the caller's, not the machine's: it must not be
	// anywhere in the durable journal a restart would read back.
	journal, err := os.ReadFile(filepath.Join(r.cfg.StateDir, "state.json"))
	if err != nil {
		// The journal's filename is an implementation detail; scan the whole
		// state directory rather than guessing wrong and passing vacuously.
		var found bool
		err = filepath.WalkDir(r.cfg.StateDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return nil //nolint:nilerr // an unreadable entry cannot hold the credential
			}
			body, readErr := os.ReadFile(path) //nolint:gosec // a test-owned state directory
			if readErr == nil && strings.Contains(string(body), "\"token\": \"t\"") {
				found = true
				t.Errorf("credential found in durable state at %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk state directory: %v", err)
		}
		if found {
			t.Fatal("the harness credential reached durable state")
		}
		return
	}
	if strings.Contains(string(journal), "\"token\": \"t\"") {
		t.Fatal("the harness credential reached the durable journal")
	}
}

func TestResumeAfterARestartRequiresTheCredentialAgain(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		return harness.Result{Phase: string(PhaseNeedsInput), SessionID: "sess-d", Blocker: "question"}, nil
	}}
	r := newTestRunner(t, adapter, source, commit)
	receipt, err := r.Start(context.Background(), workspaceStartRequest("agent-epsilon"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, r, receipt.AttemptID, PhaseNeedsInput)

	resume := ResumeRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "resume-nocred",
		TaskID:          receipt.TaskID,
		AttemptID:       receipt.AttemptID,
		AttemptEpoch:    receipt.AttemptEpoch,
		SessionID:       "sess-d",
		Resolution:      "approved",
	}
	if _, err := r.Resume(context.Background(), resume); err == nil {
		t.Fatal("Resume was admitted without a credential")
	} else {
		assertProtocolCode(t, err, ErrorInvalidRequest)
	}

	resume.RequestID = "resume-withcred"
	resume.HarnessCredential = testHarnessCredential()
	if _, err := r.Resume(context.Background(), resume); err != nil {
		t.Fatalf("Resume with a credential: %v", err)
	}
}
