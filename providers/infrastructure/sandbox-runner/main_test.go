/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSyncWritesAndDeletesOnlyWorkspaceRelativePaths(t *testing.T) {
	root := t.TempDir()
	s := newRunnerServer(&runnerConfig{WorkDir: root, StartCommand: "", ControlToken: "test-token"})
	body := syncRequest{
		Files: []syncFile{
			{Path: "public/style.css", Content: "button { color: red }\n"},
			{Path: "src/App.tsx", Content: "export default function App() { return null }\n"},
		},
		DeletePaths: []string{"src/App.tsx"},
		Restart:     "auto",
	}
	resp := postJSON(t, s, "/sync", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", resp.Code, resp.Body.String())
	}
	if got := readFile(t, root, "public/style.css"); got != "button { color: red }\n" {
		t.Fatalf("style.css = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "App.tsx")); !os.IsNotExist(err) {
		t.Fatalf("deleted file stat err = %v, want not exist", err)
	}
}

func TestSyncSkipsUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src.ts"), []byte("same"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	before, err := os.Stat(filepath.Join(root, "src.ts"))
	if err != nil {
		t.Fatalf("stat initial file: %v", err)
	}

	s := newRunnerServer(&runnerConfig{WorkDir: root, ControlToken: "test-token"})
	resp := postJSON(t, s, "/sync", syncRequest{
		Files: []syncFile{{Path: "src.ts", Content: "same"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", resp.Code, resp.Body.String())
	}
	var body syncResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode sync response: %v", err)
	}
	if len(body.Changed) != 0 {
		t.Fatalf("changed = %#v, want empty for unchanged content", body.Changed)
	}
	after, err := os.Stat(filepath.Join(root, "src.ts"))
	if err != nil {
		t.Fatalf("stat after sync: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("mtime changed for unchanged file: before=%s after=%s", before.ModTime(), after.ModTime())
	}
}

func TestSyncRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	s := newRunnerServer(&runnerConfig{WorkDir: root, ControlToken: "test-token"})
	cases := []syncRequest{
		{Files: []syncFile{{Path: "/etc/passwd", Content: "nope"}}},
		{Files: []syncFile{{Path: "../escape", Content: "nope"}}},
		{DeletePaths: []string{"../../escape"}},
	}
	for _, tc := range cases {
		resp := postJSON(t, s, "/sync", tc)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("sync %#v status = %d, want 400; body=%s", tc, resp.Code, resp.Body.String())
		}
	}
}

func TestSyncRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "escape.txt")
	if err := os.Symlink(outsideFile, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	s := newRunnerServer(&runnerConfig{WorkDir: root, ControlToken: "test-token"})
	resp := postJSON(t, s, "/sync", syncRequest{
		Files: []syncFile{{Path: "link.txt", Content: "nope"}},
	})
	if resp.Code == http.StatusOK {
		t.Fatalf("sync through symlink status = %d, want non-2xx", resp.Code)
	}
	if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
		t.Fatalf("outside file stat err = %v, want not exist", err)
	}
}

func TestSyncRejectsSymlinkParentDeleteEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(outsideFile, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	s := newRunnerServer(&runnerConfig{WorkDir: root, ControlToken: "test-token"})
	resp := postJSON(t, s, "/sync", syncRequest{
		DeletePaths: []string{"linked-dir/victim.txt"},
	})
	if resp.Code == http.StatusOK {
		t.Fatalf("delete through symlink status = %d, want non-2xx", resp.Code)
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("outside file was deleted or unreadable: %v", err)
	}
}

func TestLogsReturnsRingBuffer(t *testing.T) {
	s := newRunnerServer(&runnerConfig{WorkDir: t.TempDir(), ControlToken: "test-token"})
	s.logs.append("one")
	s.logs.append("two")

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.Header.Set("X-Sandbox-Control-Token", "test-token")
	resp := httptest.NewRecorder()
	s.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("logs status = %d", resp.Code)
	}
	if body := resp.Body.String(); !strings.Contains(body, "one") || !strings.Contains(body, "two") {
		t.Fatalf("logs body = %q", body)
	}
}

func TestControlRequiresConfiguredToken(t *testing.T) {
	s := newRunnerServer(&runnerConfig{WorkDir: t.TempDir()})
	resp := postJSON(t, s, "/sync", syncRequest{})
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("sync without configured token status = %d, want 401", resp.Code)
	}

	s = newRunnerServer(&runnerConfig{WorkDir: t.TempDir(), ControlToken: "test-token"})
	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	resp = httptest.NewRecorder()
	s.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("logs without token status = %d, want 401", resp.Code)
	}
}

func TestSyncAutoRestartsStoppedProcess(t *testing.T) {
	root := t.TempDir()
	s := newRunnerServer(&runnerConfig{WorkDir: root, StartCommand: "sleep 30", ControlToken: "test-token"})
	resp := postJSON(t, s, "/sync", syncRequest{Restart: "auto"})
	if resp.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", resp.Code, resp.Body.String())
	}
	defer func() { _ = s.supervisor.stop() }()

	s.supervisor.mu.Lock()
	cmd := s.supervisor.cmd
	s.supervisor.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatal("auto sync did not start the process")
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("auto-started process is not alive: %v", err)
	}
}

func TestSyncAutoRestartsRunningProcessWhenPackageManifestChanges(t *testing.T) {
	root := t.TempDir()
	s := newRunnerServer(&runnerConfig{WorkDir: root, StartCommand: "sleep 30", ControlToken: "test-token"})
	if err := s.supervisor.start(context.Background()); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	defer func() { _ = s.supervisor.stop() }()

	resp := postJSON(t, s, "/sync", syncRequest{
		Files:   []syncFile{{Path: "package.json", Content: `{"scripts":{"start":"node server.js"}}`}},
		Restart: "auto",
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", resp.Code, resp.Body.String())
	}
	var body syncResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode sync response: %v", err)
	}
	if !body.Restarted {
		t.Fatal("auto sync did not restart after package.json changed")
	}
}

func TestSupervisorRestartIgnoresCanceledRequestContext(t *testing.T) {
	root := t.TempDir()
	s := newRunnerServer(&runnerConfig{WorkDir: root, StartCommand: "sleep 30"})
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()

	if err := s.supervisor.restart(requestCtx); err != nil {
		t.Fatalf("restart returned error: %v", err)
	}
	defer func() { _ = s.supervisor.stop() }()

	s.supervisor.mu.Lock()
	cmd := s.supervisor.cmd
	s.supervisor.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatal("restart did not leave a process running")
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("restarted process is not alive: %v", err)
	}
}

func TestSupervisorDoesNotExposeControlTokenToChildProcess(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SANDBOX_CONTROL_TOKEN", "leaky-token")
	s := newRunnerServer(&runnerConfig{
		WorkDir:      root,
		StartCommand: "env > child.env",
		ControlToken: "leaky-token",
	})
	if err := s.supervisor.start(context.Background()); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	defer func() { _ = s.supervisor.stop() }()

	envPath := filepath.Join(root, "child.env")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(envPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child env file was not written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read child env: %v", err)
	}
	if strings.Contains(string(raw), "SANDBOX_CONTROL_TOKEN=") {
		t.Fatalf("child env leaked control token: %s", raw)
	}
}

func TestMergeChildEnvOverridesAppendsAndDropsSandbox(t *testing.T) {
	base := []string{"PATH=/bin", "APP_MODE=prod", "SANDBOX_CONTROL_TOKEN=secret"}
	got := mergeChildEnv(base, map[string]string{
		"APP_MODE":     "dev",     // overrides existing in place
		"FEATURE_FLAG": "on",      // appended
		"SANDBOX_HACK": "nope",    // dropped: SANDBOX_ prefix
		"":             "ignored", // dropped: empty name
	})
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "APP_MODE=dev") || strings.Contains(joined, "APP_MODE=prod") {
		t.Fatalf("APP_MODE was not overridden in place: %v", got)
	}
	if !strings.Contains(joined, "FEATURE_FLAG=on") {
		t.Fatalf("FEATURE_FLAG was not appended: %v", got)
	}
	if strings.Contains(joined, "SANDBOX_HACK") {
		t.Fatalf("SANDBOX_ custom env must be dropped: %v", got)
	}
	if strings.Contains(joined, "SANDBOX_CONTROL_TOKEN") {
		t.Fatalf("control token must stay stripped: %v", got)
	}
}

func TestEnvSetsChildEnvironmentAndRestarts(t *testing.T) {
	root := t.TempDir()
	s := newRunnerServer(&runnerConfig{
		WorkDir:      root,
		StartCommand: "env > child.env",
		ControlToken: "test-token",
	})
	resp := postJSON(t, s, "/env", envRequest{
		Env:     map[string]string{"APP_MODE": "dev", "FEATURE_FLAG": "on"},
		Restart: true,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("env status = %d, body %s", resp.Code, resp.Body.String())
	}
	var decoded envResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode env response: %v", err)
	}
	if !decoded.Restarted {
		t.Fatalf("expected restart, got %+v", decoded)
	}
	if strings.Join(decoded.Applied, ",") != "APP_MODE,FEATURE_FLAG" {
		t.Fatalf("applied = %v, want [APP_MODE FEATURE_FLAG] sorted", decoded.Applied)
	}
	defer func() { _ = s.supervisor.stop() }()

	envPath := filepath.Join(root, "child.env")
	deadline := time.Now().Add(3 * time.Second)
	var child string
	for {
		if raw, err := os.ReadFile(envPath); err == nil {
			child = string(raw)
			if strings.Contains(child, "APP_MODE=dev") && strings.Contains(child, "FEATURE_FLAG=on") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process did not receive the runtime env:\n%s", child)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEnvRejectsInvalidReservedAndSecretNames(t *testing.T) {
	s := newRunnerServer(&runnerConfig{WorkDir: t.TempDir(), ControlToken: "test-token"})
	cases := map[string]envRequest{
		"empty":           {Env: map[string]string{}},
		"invalid name":    {Env: map[string]string{"BAD NAME": "x"}},
		"invalid equals":  {Env: map[string]string{"A=B": "x"}},
		"reserved prefix": {Env: map[string]string{"SANDBOX_PORT": "9999"}},
		"secret-like":     {Env: map[string]string{"SHARED_SECRET": "dev-setup"}},
		"token-like":      {Env: map[string]string{"API_TOKEN": "abc"}},
	}
	for name, req := range cases {
		resp := postJSON(t, s, "/env", req)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400; body=%s", name, resp.Code, resp.Body.String())
		}
	}
}

func postJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if s, ok := h.(*runnerServer); ok && s.config != nil && s.config.ControlToken != "" {
		req.Header.Set("X-Sandbox-Control-Token", s.config.ControlToken)
	}
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	return resp
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}
