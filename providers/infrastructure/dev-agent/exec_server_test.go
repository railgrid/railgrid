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
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 2 && os.Args[1] == execWorkerArg {
		if err := runExecWorker(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func writeExecTestManifest(t *testing.T, workspace string, manifest workspaceManifest) {
	t.Helper()
	root, err := openWorkspaceRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := writeWorkspaceManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
}

func waitForExecTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func waitForExecTestState(t *testing.T, coordinator *execCoordinator, req workerExecRequest, want string) workerExecResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err := coordinator.poll(req)
		if err != nil {
			t.Fatal(err)
		}
		if result.State == want {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, err := coordinator.poll(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("execution state = %q, want %q", result.State, want)
	return workerExecResult{}
}

func TestStatelessExecutorHelpersRunInParallelAcrossSourceSync(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is unavailable")
	}
	workspace := t.TempDir()
	digest, err := digestSyncFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeExecTestManifest(t, workspace, workspaceManifest{SourceRevision: 1, SourceDigest: digest})
	executor := &statelessExecutor{workspace: workspace, startExec: startPersistentExecHelper}
	executorServer := httptest.NewServer(executor)
	defer executorServer.Close()
	mutationMu := &sync.Mutex{}
	coordinator, err := newExecCoordinator(workspace, t.TempDir(), "token", &httpExecDispatcher{
		url: executorServer.URL + "/internal/exec", client: executorServer.Client(),
	}, mutationMu)
	if err != nil {
		t.Fatal(err)
	}

	first := testWorkerRequest()
	first.persistentExecRequest = persistentExecRequest{
		Argv:    []string{"/bin/sh", "-c", "setsid /bin/sh -c 'echo $$ > escaped.pid; exec sleep 30' </dev/null >/dev/null 2>&1 & echo $! > escaped-parent.pid; touch first.started; sleep 30"},
		WorkDir: ".", TimeoutMS: 30_000, SourceRevision: 1, SourceDigest: digest,
	}
	second := first
	second.SessionID = strings.Repeat("d", 32)
	second.RequestID = "request-2"
	second.Fingerprint = strings.Repeat("e", 64)
	second.persistentExecRequest = persistentExecRequest{
		Argv:    []string{"/bin/sh", "-c", "touch second.started; while [ ! -f release-second ]; do sleep 0.02; done; echo done > second.done"},
		WorkDir: ".", TimeoutMS: 30_000, SourceRevision: 1, SourceDigest: digest,
	}
	// Ensure failures in the test do not leave commands behind or waiting on the
	// release marker.
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(workspace, "release-second"), nil, 0o600)
		firstCancel := first
		firstCancel.Action = "cancel"
		_, _ = coordinator.cancel(firstCancel)
		secondCancel := second
		secondCancel.Action = "cancel"
		_, _ = coordinator.cancel(secondCancel)
		waitForExecCoordinatorInactive(t, coordinator, first.SessionID)
		waitForExecCoordinatorInactive(t, coordinator, second.SessionID)
	})
	if _, err := coordinator.start(first); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.start(second); err != nil {
		t.Fatal(err)
	}

	waitForExecTestFile(t, filepath.Join(workspace, "first.started"))
	waitForExecTestFile(t, filepath.Join(workspace, "second.started"))
	// This is the same lock used by source sync. It must become available after
	// the helper acknowledges cmd.Start, even though both commands are active.
	mutationLocked := make(chan struct{})
	go func() {
		mutationMu.Lock()
		close(mutationLocked)
	}()
	select {
	case <-mutationLocked:
	case <-time.After(time.Second):
		t.Fatal("source mutation lock stayed held for the command lifetime")
	}
	func() {
		defer mutationMu.Unlock()
		writeExecTestManifest(t, workspace, workspaceManifest{SourceRevision: 2, SourceDigest: digest})
	}()

	waitForExecTestFile(t, filepath.Join(workspace, "escaped.pid"))
	secondPoll := second
	secondPoll.Action = "poll"
	if result, err := coordinator.poll(secondPoll); err != nil || result.State != "running" {
		t.Fatalf("second command before first cancellation = %+v, %v; want running", result, err)
	}
	firstCancel := first
	firstCancel.Action = "cancel"
	if result, err := coordinator.cancel(firstCancel); err != nil || result.State != "canceled" {
		t.Fatalf("cancel first command = %+v, %v", result, err)
	}
	waitForExecTestState(t, coordinator, first, "canceled")
	waitForExecCoordinatorInactive(t, coordinator, first.SessionID)
	rawPID, err := os.ReadFile(filepath.Join(workspace, "escaped.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("canceled command's escaped descendant pid %d is still alive: %v", pid, err)
	}
	if result, err := coordinator.poll(secondPoll); err != nil || result.State != "running" {
		t.Fatalf("second command after first cancellation = %+v, %v; want running", result, err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "release-second"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForExecTestState(t, coordinator, second, "succeeded")
	waitForExecCoordinatorInactive(t, coordinator, second.SessionID)
}

func TestStatelessExecutorRejectsStaleRevisionBeforeDispatch(t *testing.T) {
	workspace := t.TempDir()
	digest, err := digestSyncFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeExecTestManifest(t, workspace, workspaceManifest{SourceRevision: 2, SourceDigest: digest})
	executor := &statelessExecutor{workspace: workspace, startExec: startPersistentExecHelper}
	request, err := json.Marshal(persistentExecRequest{
		Argv: []string{"/bin/sh", "-c", "touch must-not-run"}, WorkDir: ".",
		SourceRevision: 1, SourceDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	executor.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/internal/exec", bytes.NewReader(request)))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale dispatch status = %d, want %d; body=%s", response.Code, http.StatusConflict, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(workspace, "must-not-run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale command ran: stat error = %v", err)
	}
}

type gatedExecExecution struct {
	release        chan struct{}
	waitEntered    chan struct{}
	cancelObserved chan struct{}
	waitOnce       sync.Once
	cancelOnce     sync.Once
	releaseOnce    sync.Once
}

func (e *gatedExecExecution) Wait(ctx context.Context) (execResponse, error) {
	e.waitOnce.Do(func() { close(e.waitEntered) })
	select {
	case <-ctx.Done():
		e.cancelOnce.Do(func() { close(e.cancelObserved) })
		<-e.release
		return execResponse{Phase: "cancelled", Cancelled: true}, nil
	case <-e.release:
		return execResponse{Phase: "completed", ExitCode: 0}, nil
	}
}

func (e *gatedExecExecution) finishCleanup() {
	e.releaseOnce.Do(func() { close(e.release) })
}

func TestStatelessExecutorAdmissionWaitsForCleanup(t *testing.T) {
	started := make(chan *gatedExecExecution, execMaxConcurrentCommands+1)
	executor := &statelessExecutor{
		startExec: func(context.Context, string, execContext, persistentExecRequest) (execExecution, error) {
			execution := &gatedExecExecution{
				release: make(chan struct{}), waitEntered: make(chan struct{}), cancelObserved: make(chan struct{}),
			}
			started <- execution
			return execution, nil
		},
	}
	body, err := json.Marshal(persistentExecRequest{
		Argv: []string{"true"}, SourceRevision: 1, SourceDigest: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	type requestRun struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	var requests []*requestRun
	var executions []*gatedExecExecution
	t.Cleanup(func() {
		for _, request := range requests {
			request.cancel()
		}
		for {
			select {
			case execution := <-started:
				executions = append(executions, execution)
			default:
				goto releaseExecutions
			}
		}
	releaseExecutions:
		for _, execution := range executions {
			execution.finishCleanup()
		}
		for _, request := range requests {
			select {
			case <-request.done:
			case <-time.After(2 * time.Second):
				t.Errorf("executor request did not finish during cleanup")
			}
		}
	})

	startRequest := func() *requestRun {
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequest(http.MethodPost, "/internal/exec", bytes.NewReader(body)).WithContext(ctx)
		run := &requestRun{cancel: cancel, done: make(chan struct{})}
		requests = append(requests, run)
		go func() {
			defer close(run.done)
			executor.ServeHTTP(httptest.NewRecorder(), request)
		}()
		return run
	}
	awaitStart := func() *gatedExecExecution {
		t.Helper()
		select {
		case execution := <-started:
			executions = append(executions, execution)
			select {
			case <-execution.waitEntered:
			case <-time.After(time.Second):
				t.Fatal("executor did not begin waiting for the running command")
			}
			return execution
		case <-time.After(time.Second):
			t.Fatal("executor did not start a command")
			return nil
		}
	}

	for range execMaxConcurrentCommands {
		startRequest()
		awaitStart()
	}
	startRequest()
	select {
	case <-started:
		t.Fatal("executor started more commands than its admission limit")
	case <-time.After(100 * time.Millisecond):
	}

	first := executions[0]
	requests[0].cancel()
	select {
	case <-first.cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("running command did not observe cancellation")
	}
	select {
	case <-started:
		t.Fatal("canceled command released its slot before cleanup finished")
	case <-time.After(100 * time.Millisecond):
	}
	first.finishCleanup()
	awaitStart()
}

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

func TestStatelessExecutorSerializesCommands(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	srv := &statelessExecutor{
		workspace: t.TempDir(),
		execute: func(context.Context, string, persistentExecRequest) (execResponse, error) {
			call := calls.Add(1)
			current := active.Add(1)
			for observed := maxActive.Load(); current > observed && !maxActive.CompareAndSwap(observed, current); observed = maxActive.Load() {
			}
			defer active.Add(-1)
			if call == 1 {
				close(firstStarted)
				<-releaseFirst
			}
			return execResponse{Phase: "completed", ExitCode: 0}, nil
		},
	}
	request := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "/internal/exec", strings.NewReader(
			`{"argv":["/bin/true"],"sourceRevision":1,"sourceDigest":"sha256:test"}`,
		))
	}
	done := make(chan struct{}, 2)
	for range 2 {
		go func() {
			srv.ServeHTTP(httptest.NewRecorder(), request())
			done <- struct{}{}
		}()
		if calls.Load() == 0 {
			<-firstStarted
		}
	}
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent execute calls before release = %d, want 1", got)
	}
	close(releaseFirst)
	<-done
	<-done
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent executions = %d, want 1", got)
	}
}

func TestPersistentExecVerifiesAppliedRevisionDigestAndSanitizesEnvironment(t *testing.T) {
	workdir := t.TempDir()
	srv := newTestAgent(t, &agentConfig{WorkDir: workdir, ControlToken: "test-token"})
	executor := &statelessExecutor{workspace: workdir}
	files := []syncFile{{Path: "main.sh", Content: "#!/bin/sh\nprintf '%s\\n' \"$NPM_CONFIG_CACHE\"\nprintf '%s\\n' \"$ONLY_EXPLICIT\"\nprintf '%s\\n' \"$AWS_SECRET_ACCESS_KEY\"\n"}}
	digest, err := digestSyncFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := doSync(t, srv, syncRequest{Files: files, SourceRevision: 7, SourceDigest: digest}); rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", rec.Code, rec.Body.String())
	}
	t.Setenv("ONLY_EXPLICIT", "must-not-inherit")
	t.Setenv("NPM_CONFIG_CACHE", "/tmp/caller-selected-cache")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-inherit")
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
	if got.ExitCode != 0 || got.Stdout != "/tmp/railgrid-cache/npm\n\n\n" || got.SourceRevision != 7 || got.SourceDigest != digest {
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

func TestPersistentExecExposesAppPortAndComponentButNotAppEnvironment(t *testing.T) {
	workdir := t.TempDir()
	srv := newTestAgent(t, &agentConfig{WorkDir: workdir, ControlToken: "test-token"})
	executor := &statelessExecutor{workspace: workdir, env: execContext{Port: "5173", Component: "web"}}
	files := []syncFile{{Path: "env.sh", Content: "#!/bin/sh\nprintf '%s|%s|%s|%s\\n' \"$PORT\" \"$RAILGRID_COMPONENT\" \"$DATABASE_URL\" \"$RAILGRID_DEV_PORT\"\n"}}
	digest, err := digestSyncFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := doSync(t, srv, syncRequest{Files: files, SourceRevision: 1, SourceDigest: digest}); rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", rec.Code, rec.Body.String())
	}
	// The executor process itself may carry app-like variables; none of them
	// may reach the command.
	t.Setenv("DATABASE_URL", "postgres://must-not-inherit")
	t.Setenv("RAILGRID_DEV_PORT", "9999")
	t.Setenv("PORT", "1111")
	raw, err := json.Marshal(persistentExecRequest{Argv: []string{"/bin/sh", "env.sh"}, SourceRevision: 1, SourceDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	executor.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/internal/exec", bytes.NewReader(raw)))
	if res.Code != http.StatusOK {
		t.Fatalf("exec status = %d body=%s", res.Code, res.Body.String())
	}
	var got execResponse
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 0 || got.Stdout != "5173|web||\n" {
		t.Fatalf("exec env output = %q (exit %d), want only PORT and RAILGRID_COMPONENT", got.Stdout, got.ExitCode)
	}
}

func TestSanitizedExecEnvironmentDropsUnconfiguredOrMalformedContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  execContext
		want []string
		deny []string
	}{
		{name: "unconfigured", deny: []string{"PORT=", "RAILGRID_COMPONENT="}},
		{name: "configured", ctx: execContext{Port: "8080", Component: "api"}, want: []string{"PORT=8080", "RAILGRID_COMPONENT=api"}},
		{name: "malformed port", ctx: execContext{Port: "${schema.spec.port}"}, deny: []string{"PORT="}},
		{name: "out of range port", ctx: execContext{Port: "70000"}, deny: []string{"PORT="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := strings.Join(sanitizedExecEnvironment("/workspace", tc.ctx), "\n") + "\n"
			for _, want := range tc.want {
				if !strings.Contains(env, want+"\n") {
					t.Errorf("env lacks %s:\n%s", want, env)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains("\n"+env, "\n"+deny) {
					t.Errorf("env unexpectedly contains %s:\n%s", deny, env)
				}
			}
		})
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
		Argv: []string{"/bin/sh", "-c", "while :; do printf output; printf error >&2; done"}, WorkDir: ".",
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
