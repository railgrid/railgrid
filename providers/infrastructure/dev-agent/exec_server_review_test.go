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

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	delayedExecHelperEnv   = "RAILGRID_TEST_DELAYED_EXEC_HELPER"
	delayedExecHelperReady = "RAILGRID_TEST_DELAYED_EXEC_HELPER_READY"
	delayedExecHelperWait  = 3500 * time.Millisecond
)

// TestExecHelperDelayedReceiptProcess runs as a subprocess in
// TestExecHelperCancellationGraceIncludesDelayedReceipt. It ignores SIGTERM
// after announcing readiness, then returns a valid terminal receipt after the
// old three-second grace would have expired.
func TestExecHelperDelayedReceiptProcess(t *testing.T) {
	if os.Getenv(delayedExecHelperEnv) != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	readyPath := os.Getenv(delayedExecHelperReady)
	if readyPath == "" {
		t.Fatal("delayed helper readiness path is required")
	}
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(delayedExecHelperWait)
	if _, err := fmt.Fprintln(os.Stdout, `{"phase":"completed","response":{"phase":"completed","exitCode":0}}`); err != nil {
		t.Fatal(err)
	}
}

func TestExecHelperCancellationGraceIncludesDelayedReceipt(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestExecHelperDelayedReceiptProcess$")
	cmd.Env = append(filteredEnvironment(delayedExecHelperEnv, delayedExecHelperReady),
		delayedExecHelperEnv+"=1", delayedExecHelperReady+"="+readyPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr := newExecOutputBuffer(4 << 10)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitStarted := false
	t.Cleanup(func() {
		if cmd.Process == nil || cmd.ProcessState != nil {
			return
		}
		_ = cmd.Process.Kill()
		if !waitStarted {
			_ = cmd.Wait()
		}
	})
	waitForReviewFile(t, readyPath)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	execution := &execWorkerExecution{cmd: cmd, reader: bufio.NewReader(stdout), stderr: stderr}
	started := time.Now()
	waitStarted = true
	response, err := execution.Wait(ctx)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("wait for delayed terminal receipt: %v", err)
	}
	if response.Phase != "completed" || response.ExitCode != 0 {
		t.Fatalf("delayed helper response = %+v, want completed exit 0", response)
	}
	if elapsed < 3*time.Second {
		t.Fatalf("helper completed in %s; cancellation did not wait beyond the former three-second grace", elapsed)
	}
	if elapsed >= execHelperCancelGrace {
		t.Fatalf("helper took %s, beyond cancellation grace %s", elapsed, execHelperCancelGrace)
	}
}

func TestStatelessExecutorCancellationWaitsForEscapedOutputDescendant(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is unavailable")
	}
	workspace := t.TempDir()
	digest, err := digestSyncFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeExecTestManifest(t, workspace, workspaceManifest{SourceRevision: 1, SourceDigest: digest})

	tracked := map[string]*reviewTrackedExecution{}
	var trackedMu sync.Mutex
	exited := make(chan int, 1)
	executor := &statelessExecutor{
		workspace: workspace,
		startExec: func(ctx context.Context, workspace string, env execContext, req persistentExecRequest) (execExecution, error) {
			execution, err := startPersistentExecHelper(ctx, workspace, env, req)
			if err != nil {
				return nil, err
			}
			key := req.Argv[3]
			wrapped := &reviewTrackedExecution{execExecution: execution, done: make(chan struct{})}
			trackedMu.Lock()
			tracked[key] = wrapped
			trackedMu.Unlock()
			return wrapped, nil
		},
		exit: func(code int) { exited <- code },
	}
	server := httptest.NewServer(executor)
	t.Cleanup(server.Close)
	dispatcher := &httpExecDispatcher{url: server.URL + "/internal/exec", client: server.Client()}
	defer dispatcher.client.CloseIdleConnections()
	coordinator, err := newExecCoordinator(workspace, t.TempDir(), "token", dispatcher, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}

	first := testWorkerRequest()
	first.persistentExecRequest = persistentExecRequest{
		Argv:    []string{"/bin/sh", "-c", "setsid /bin/sh -c 'echo $$ > escaped.pid; exec sleep 30' & touch first.started; sleep 30", "first"},
		WorkDir: ".", TimeoutMS: 30_000, SourceRevision: 1, SourceDigest: digest,
	}
	second := first
	second.SessionID = strings.Repeat("d", 32)
	second.RequestID = "request-2"
	second.Fingerprint = strings.Repeat("e", 64)
	second.persistentExecRequest = persistentExecRequest{
		Argv:    []string{"/bin/sh", "-c", "touch sibling.started; while [ ! -f sibling.release ]; do sleep 0.02; done; echo done > sibling.done", "sibling"},
		WorkDir: ".", TimeoutMS: 30_000, SourceRevision: 1, SourceDigest: digest,
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(workspace, "sibling.release"), nil, 0o600)
		for _, req := range []workerExecRequest{first, second} {
			cancelReq := req
			cancelReq.Action = "cancel"
			_, _ = coordinator.cancel(cancelReq)
		}
		for _, key := range []string{"first", "sibling"} {
			if execution := getTrackedExecution(&trackedMu, tracked, key); execution != nil {
				select {
				case <-execution.done:
				case <-time.After(execHelperCancelGrace + time.Second):
					t.Errorf("%s helper did not finish during cleanup", key)
				}
			}
		}
	})
	if _, err := coordinator.start(first); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.start(second); err != nil {
		t.Fatal(err)
	}
	waitForReviewFile(t, filepath.Join(workspace, "first.started"))
	waitForReviewFile(t, filepath.Join(workspace, "escaped.pid"))
	waitForReviewFile(t, filepath.Join(workspace, "sibling.started"))
	firstExec := getTrackedExecution(&trackedMu, tracked, "first")
	secondExec := getTrackedExecution(&trackedMu, tracked, "sibling")
	if firstExec == nil || secondExec == nil {
		t.Fatal("executor did not track both started helpers")
	}

	cancelReq := first
	cancelReq.Action = "cancel"
	if result, err := coordinator.cancel(cancelReq); err != nil || result.State != "canceled" {
		t.Fatalf("cancel first command = %+v, %v", result, err)
	}
	cancelStarted := time.Now()
	if err := os.WriteFile(filepath.Join(workspace, "sibling.release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForReviewFile(t, filepath.Join(workspace, "sibling.done"))
	select {
	case <-secondExec.done:
	case <-time.After(2 * time.Second):
		t.Fatal("sibling command was not able to finish while cancellation cleanup ran")
	}
	select {
	case <-firstExec.done:
	case <-time.After(execHelperCancelGrace + time.Second):
		t.Fatal("canceled helper did not finish descendant cleanup within the shared grace")
	}
	if elapsed := time.Since(cancelStarted); elapsed < execCommandWaitDelay-250*time.Millisecond {
		t.Fatalf("cancellation cleanup completed in %s; expected the escaped output pipe to exercise WaitDelay %s", elapsed, execCommandWaitDelay)
	}
	pidRaw, err := os.ReadFile(filepath.Join(workspace, "escaped.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil {
		t.Fatal(err)
	}
	waitForReviewPIDGone(t, pid)
	waitForExecTestState(t, coordinator, second, "succeeded")
	if result, err := coordinator.poll(workerPollRequest(first)); err != nil || result.State != "canceled" {
		t.Fatalf("canceled session state = %+v, %v; want canceled", result, err)
	}
	if result, err := coordinator.poll(workerPollRequest(second)); err != nil || result.State != "succeeded" {
		t.Fatalf("sibling session state = %+v, %v; want succeeded", result, err)
	}
	select {
	case code := <-exited:
		t.Fatalf("executor fail-stopped with exit %d after proven cancellation cleanup", code)
	case <-time.After(100 * time.Millisecond):
	}
}

type reviewTrackedExecution struct {
	execExecution
	done chan struct{}
}

func (e *reviewTrackedExecution) Wait(ctx context.Context) (execResponse, error) {
	defer close(e.done)
	return e.execExecution.Wait(ctx)
}

func getTrackedExecution(mu *sync.Mutex, tracked map[string]*reviewTrackedExecution, key string) *reviewTrackedExecution {
	mu.Lock()
	defer mu.Unlock()
	return tracked[key]
}

func TestWriteExecStartErrorUsesTypedSourceConflict(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			name:       "ordinary validation text mentions source revision",
			err:        errors.New("source revision is required by the request"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "typed conflict has no legacy keywords",
			err:        fmt.Errorf("%w: fence rejected", errExecSourceConflict),
			wantStatus: http.StatusConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			(&statelessExecutor{}).writeExecStartError(response, tt.err)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantStatus, response.Body.String())
			}
		})
	}
}

func TestWriteExecStartErrorEncodesTypedInvalidWorkdir(t *testing.T) {
	err := fmt.Errorf("%w: workdir %q does not exist", errExecInvalidWorkdir, "generated")
	response := httptest.NewRecorder()
	(&statelessExecutor{}).writeExecStartError(response, err)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type = %q, want JSON", got)
	}
	var rejection execStartRejection
	if err := json.Unmarshal(response.Body.Bytes(), &rejection); err != nil {
		t.Fatalf("decode rejection %q: %v", response.Body.String(), err)
	}
	if rejection.RejectionCode != execRejectionInvalidWorkdir || !strings.Contains(rejection.Error, "does not exist") {
		t.Fatalf("rejection = %+v, want typed code and diagnostic", rejection)
	}
}

func TestStatelessExecutorClassifiesSourceFencesAcrossHelperProtocol(t *testing.T) {
	validDigest, err := digestSyncFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	otherDigest := strings.Repeat("b", 64)
	tests := []struct {
		name       string
		manifest   *workspaceManifest
		request    persistentExecRequest
		wantStatus int
	}{
		{
			name:       "missing manifest",
			request:    persistentExecRequest{Argv: []string{"/bin/true"}, SourceRevision: 1, SourceDigest: validDigest},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "revision mismatch",
			manifest:   &workspaceManifest{SourceRevision: 2, SourceDigest: validDigest},
			request:    persistentExecRequest{Argv: []string{"/bin/true"}, SourceRevision: 1, SourceDigest: validDigest},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "digest mismatch",
			manifest:   &workspaceManifest{SourceRevision: 1, SourceDigest: validDigest},
			request:    persistentExecRequest{Argv: []string{"/bin/true"}, SourceRevision: 1, SourceDigest: otherDigest},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "dependency reload pending",
			manifest:   &workspaceManifest{SourceRevision: 1, SourceDigest: validDigest, PendingReloadCommands: []string{"npm install"}},
			request:    persistentExecRequest{Argv: []string{"/bin/true"}, SourceRevision: 1, SourceDigest: validDigest},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "missing source revision is request validation",
			manifest:   &workspaceManifest{SourceRevision: 1, SourceDigest: validDigest},
			request:    persistentExecRequest{Argv: []string{"/bin/true"}, SourceDigest: validDigest},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed digest is request validation",
			manifest:   &workspaceManifest{SourceRevision: 1, SourceDigest: validDigest},
			request:    persistentExecRequest{Argv: []string{"/bin/true"}, SourceRevision: 1, SourceDigest: "sha256:bad"},
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			if tt.manifest != nil {
				writeExecTestManifest(t, workspace, *tt.manifest)
			}
			executor := &statelessExecutor{workspace: workspace}
			server := httptest.NewServer(executor)
			defer server.Close()
			body, err := json.Marshal(tt.request)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Client().Post(server.URL+"/internal/exec", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			responseBody, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.StatusCode, tt.wantStatus, responseBody)
			}
		})
	}
}

func waitForReviewFile(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(name); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", name)
}

func waitForReviewPIDGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("escaped descendant pid %d remains alive", pid)
}

func workerPollRequest(req workerExecRequest) workerExecRequest {
	req.Action = "poll"
	return req
}

func filteredEnvironment(keys ...string) []string {
	blocked := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		blocked[key] = struct{}{}
	}
	result := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, found := blocked[key]; !found {
			result = append(result, item)
		}
	}
	return result
}
