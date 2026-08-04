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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPersistentExecReapsSetsidDescendantOnEveryCompletionPath(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is unavailable")
	}
	for _, tc := range []struct {
		name      string
		command   string
		timeoutMS int
		cancel    bool
	}{
		{name: "normal", command: "setsid /bin/sh -c 'sleep 30' </dev/null >/dev/null 2>&1 & echo $! > daemon.pid"},
		{name: "timeout", command: "setsid /bin/sh -c 'sleep 30' </dev/null >/dev/null 2>&1 & echo $! > daemon.pid; sleep 30", timeoutMS: 40},
		{name: "cancel", command: "setsid /bin/sh -c 'sleep 30' </dev/null >/dev/null 2>&1 & echo $! > daemon.pid; sleep 30", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			digest, _ := digestSyncFiles(nil)
			root, err := openWorkspaceRoot(workspace)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeWorkspaceManifest(root, workspaceManifest{SourceRevision: 1, SourceDigest: digest}); err != nil {
				t.Fatal(err)
			}
			_ = root.Close()
			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancel {
				go func() {
					time.Sleep(40 * time.Millisecond)
					cancel()
				}()
			} else {
				defer cancel()
			}
			result, err := runPersistentExec(ctx, workspace, persistentExecRequest{
				Argv: []string{"/bin/sh", "-c", tc.command}, WorkDir: ".", TimeoutMS: tc.timeoutMS,
				SourceRevision: 1, SourceDigest: digest,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.timeoutMS > 0 && !result.TimedOut {
				t.Fatalf("result = %+v, want timeout", result)
			}
			if tc.cancel && !result.Cancelled {
				t.Fatalf("result = %+v, want cancel", result)
			}
			rawPID, err := os.ReadFile(filepath.Join(workspace, "daemon.pid"))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("setsid descendant pid %d remains after %s completion: %v", pid, tc.name, err)
			}
		})
	}
}

func TestStatelessExecutorIsNarrowAndRejectsUnknownFields(t *testing.T) {
	workspace := t.TempDir()
	srv := &statelessExecutor{workspace: workspace}
	for name, body := range map[string]string{
		"wrong path":          `{"argv":["/bin/true"],"sourceRevision":1,"sourceDigest":"sha256:test"}`,
		"shell command field": `{"command":"echo unsafe","argv":["/bin/true"],"sourceRevision":1,"sourceDigest":"sha256:test"}`,
	} {
		t.Run(name, func(t *testing.T) {
			requestPath := "/internal/exec"
			if name == "wrong path" {
				requestPath = "/exec"
			}
			req := httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader(body))
			res := httptest.NewRecorder()
			srv.ServeHTTP(res, req)
			want := http.StatusBadRequest
			if name == "wrong path" {
				want = http.StatusNotFound
			}
			if res.Code != want {
				t.Fatalf("status = %d, want %d; body=%s", res.Code, want, res.Body.String())
			}
		})
	}
}

func TestStatelessExecutorFailStopsWhenCleanupCannotBeProven(t *testing.T) {
	exited := make(chan int, 1)
	executions := 0
	srv := &statelessExecutor{
		workspace: t.TempDir(),
		execute: func(context.Context, string, persistentExecRequest) (execResponse, error) {
			executions++
			return execResponse{}, fmt.Errorf("cleanup escaped process: %w", errExecCleanupUnproven)
		},
		exit: func(code int) { exited <- code },
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/exec", strings.NewReader(
		`{"argv":["/bin/true"],"sourceRevision":1,"sourceDigest":"sha256:test"}`,
	))
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", res.Code, http.StatusInternalServerError, res.Body.String())
	}
	res = httptest.NewRecorder()
	srv.ServeHTTP(res, req.Clone(context.Background()))
	if res.Code != http.StatusServiceUnavailable || executions != 1 {
		t.Fatalf("poisoned executor status = %d, executions = %d; want %d, 1", res.Code, executions, http.StatusServiceUnavailable)
	}
	select {
	case code := <-exited:
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	case <-time.After(time.Second):
		t.Fatal("unproven cleanup did not fail-stop the executor")
	}
}

func TestPersistentExecVerifiesAppliedRevisionDigestAndSanitizesEnvironment(t *testing.T) {
	workdir := t.TempDir()
	srv := newTestAgent(t, &agentConfig{WorkDir: workdir, ControlToken: "test-token"})
	executor := &statelessExecutor{workspace: workdir}
	files := []syncFile{{Path: "main.sh", Content: "#!/bin/sh\nprintf '%s\\n' \"$ONLY_EXPLICIT\"\n"}}
	digest, err := digestSyncFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := doSync(t, srv, syncRequest{Files: files, SourceRevision: 7, SourceDigest: digest}); rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", rec.Code, rec.Body.String())
	}
	t.Setenv("ONLY_EXPLICIT", "must-not-inherit")
	raw, err := json.Marshal(persistentExecRequest{
		Argv: []string{"/bin/sh", "main.sh"}, WorkDir: ".", SourceRevision: 7, SourceDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/exec", bytes.NewReader(raw))
	res := httptest.NewRecorder()
	executor.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("persistent exec status = %d body=%s", res.Code, res.Body.String())
	}
	var got execResponse
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 0 || got.Stdout != "\n" || got.SourceRevision != 7 || got.SourceDigest != digest {
		t.Fatalf("persistent exec response = %+v", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/internal/exec", bytes.NewReader([]byte(`{"argv":["/bin/true"],"sourceRevision":7,"sourceDigest":"sha256:bad"}`)))
	res = httptest.NewRecorder()
	executor.ServeHTTP(res, req)
	if res.Code != http.StatusConflict {
		t.Fatalf("digest mismatch status = %d body=%s, want 409", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/internal/exec", bytes.NewReader([]byte(`{"argv":["/bin/true"],"sourceRevision":8,"sourceDigest":"`+digest+`"}`)))
	res = httptest.NewRecorder()
	executor.ServeHTTP(res, req)
	if res.Code != http.StatusConflict {
		t.Fatalf("revision mismatch status = %d body=%s, want 409", res.Code, res.Body.String())
	}
}

func TestPersistentExecBoundsOutputAndKillsTimedOutProcess(t *testing.T) {
	workdir := t.TempDir()
	digest, err := digestSyncFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestAgent(t, &agentConfig{WorkDir: workdir, ControlToken: "test-token"})
	if rec, _ := doSync(t, srv, syncRequest{Files: []syncFile{}, SourceRevision: 1, SourceDigest: digest}); rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", rec.Code, rec.Body.String())
	}
	result, err := runPersistentExec(context.Background(), workdir, persistentExecRequest{
		Argv: []string{"/bin/sh", "-c", "yes output & yes error >&2"}, WorkDir: ".",
		TimeoutMS: 30, MaxOutputBytes: 128, SourceRevision: 1, SourceDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.ExitCode == 0 {
		t.Fatalf("result = %+v, want timeout and nonzero exit", result)
	}
	if !result.StdoutTruncated || len(result.Stdout) != 128 || !result.StderrTruncated || len(result.Stderr) != 128 {
		t.Fatalf("bounded output = stdout %d/%t stderr %d/%t", len(result.Stdout), result.StdoutTruncated, len(result.Stderr), result.StderrTruncated)
	}
}

func TestPersistentExecCancellationAndWorkspaceConfinement(t *testing.T) {
	workdir := t.TempDir()
	digest, err := digestSyncFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestAgent(t, &agentConfig{WorkDir: workdir, ControlToken: "test-token"})
	if rec, _ := doSync(t, srv, syncRequest{Files: []syncFile{}, SourceRevision: 1, SourceDigest: digest}); rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := runPersistentExec(context.Background(), workdir, persistentExecRequest{
		Argv: []string{"/bin/true"}, WorkDir: "../escape", SourceRevision: 1, SourceDigest: digest,
	}); err == nil {
		t.Fatal("escaping workdir was accepted")
	}
	outside := t.TempDir()
	rootLink := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(outside, rootLink); err != nil {
		t.Fatal(err)
	}
	if _, err := runPersistentExec(context.Background(), rootLink, persistentExecRequest{
		Argv: []string{"/bin/true"}, WorkDir: ".", SourceRevision: 1, SourceDigest: digest,
	}); err == nil {
		t.Fatal("symbolic-link workspace root was accepted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan execResponse, 1)
	errs := make(chan error, 1)
	go func() {
		result, runErr := runPersistentExec(ctx, workdir, persistentExecRequest{
			Argv: []string{"/bin/sleep", "30"}, WorkDir: ".", SourceRevision: 1, SourceDigest: digest,
		})
		done <- result
		errs <- runErr
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case result := <-done:
		if !result.Cancelled {
			t.Fatalf("result = %+v, want canceled", result)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled process did not exit")
	}
	if runErr := <-errs; runErr != nil && !errors.Is(runErr, context.Canceled) {
		t.Fatalf("run error = %v", runErr)
	}
}
