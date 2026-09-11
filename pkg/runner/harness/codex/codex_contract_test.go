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

package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/faroshq/faros/pkg/runner/harness"
)

func TestNewUsesContractDefaultVersionPin(t *testing.T) {
	adapter, ok := New(Config{Home: t.TempDir()}).(*Adapter)
	if !ok {
		t.Fatal("New did not return the Codex adapter")
	}
	if adapter.cfg.ExpectedVersion != "0.147.0" {
		t.Fatalf("default expected Codex version = %q, want 0.147.0", adapter.cfg.ExpectedVersion)
	}
}

func TestProbeAuthenticationNeedsInputWithoutStartingModelWork(t *testing.T) {
	binary := fakeCodexBinary(t, "auth")
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.147.0"})

	info, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Ready {
		t.Fatalf("auth-required probe reported ready: %+v", info)
	}
	if len(info.Reasons) == 0 || !strings.Contains(strings.ToLower(strings.Join(info.Reasons, " ")), "authentication") {
		t.Fatalf("auth-required probe reasons = %v", info.Reasons)
	}
	methods := readMethods(t, filepath.Join(filepath.Dir(binary), "methods"))
	if strings.Contains(strings.Join(methods, ","), "thread/start") || strings.Contains(strings.Join(methods, ","), "turn/start") {
		t.Fatalf("auth probe started model work: %v", methods)
	}
}

func TestRunResumeUsesExactExistingSession(t *testing.T) {
	binary := fakeCodexBinary(t, "success")
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.147.0"})
	result, err := adapter.Run(context.Background(), harness.Launch{
		AttemptID:    "attempt-resume",
		Workdir:      t.TempDir(),
		SessionID:    "thread-existing",
		Instructions: "continue the existing session",
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "completed" || result.SessionID != "thread-existing" {
		t.Fatalf("resume result = %+v", result)
	}
	methods := readMethods(t, filepath.Join(filepath.Dir(binary), "methods"))
	if strings.Contains(strings.Join(methods, ","), "thread/start") {
		t.Fatalf("resume started a new thread: %v", methods)
	}
	requests := readJSONLines(t, filepath.Join(filepath.Dir(binary), "requests"))
	if len(requests) < 4 || requests[2]["method"] != "thread/resume" {
		t.Fatalf("resume requests = %v", requests)
	}
	threadParams, ok := requests[2]["params"].(map[string]any)
	if !ok || threadParams["threadId"] != "thread-existing" {
		t.Fatalf("resume thread params = %v", requests[2]["params"])
	}
	turnParams, ok := requests[3]["params"].(map[string]any)
	if !ok || turnParams["threadId"] != "thread-existing" {
		t.Fatalf("resume turn params = %v", requests[3]["params"])
	}
}

func TestRunRejectsForeignResumeResponseAndCompletion(t *testing.T) {
	t.Run("resume response", func(t *testing.T) {
		binary := fakeCodexBinary(t, "foreign-resume")
		adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.147.0"})
		result, err := adapter.Run(context.Background(), harness.Launch{
			AttemptID:    "attempt-foreign-resume",
			Workdir:      t.TempDir(),
			SessionID:    "thread-existing",
			Instructions: "continue the approved session",
		}, nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Phase != "failed" {
			t.Fatalf("foreign resume result = %+v, want failed", result)
		}
		methods := readMethods(t, filepath.Join(filepath.Dir(binary), "methods"))
		if strings.Contains(strings.Join(methods, ","), "turn/start") {
			t.Fatalf("foreign resume started a turn: %v", methods)
		}
	})

	t.Run("completion event", func(t *testing.T) {
		binary := fakeCodexBinary(t, "foreign-completion")
		adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.147.0"})
		result, err := adapter.Run(context.Background(), harness.Launch{
			AttemptID:    "attempt-foreign-completion",
			Workdir:      t.TempDir(),
			Instructions: "run the approved turn",
		}, nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Phase != "failed" {
			t.Fatalf("foreign completion result = %+v, want failed", result)
		}
	})
}

func TestEnsureHomeRejectsInteractiveConfiguration(t *testing.T) {
	for _, name := range []string{"config.toml", "mcp.json", "hooks"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, name)
			if filepath.Ext(name) != "" {
				if err := os.WriteFile(path, []byte("interactive"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			adapter, ok := New(Config{Home: home}).(*Adapter)
			if !ok {
				t.Fatal("New did not return the Codex adapter")
			}
			if err := adapter.ensureHome(); err == nil {
				t.Fatalf("ensureHome accepted unsafe entry %q", name)
			}
		})
	}
}

func TestSafeEnvDropsAllInteractiveCredentialsAndGlobalConfiguration(t *testing.T) {
	secrets := map[string]string{
		"HOME":                "/interactive/home",
		"CODEX_HOME":          "/interactive/codex",
		"XDG_CONFIG_HOME":     "/interactive/config",
		"XDG_DATA_HOME":       "/interactive/data",
		"XDG_CACHE_HOME":      "/interactive/cache",
		"GIT_CONFIG_GLOBAL":   "/interactive/gitconfig",
		"GIT_CONFIG_SYSTEM":   "/interactive/system-gitconfig",
		"GIT_CONFIG_NOSYSTEM": "0",
		"GIT_CONFIG_COUNT":    "1",
		"GIT_SSH":             "/interactive/ssh",
		"GIT_SSH_COMMAND":     "ssh -i /interactive/key",
		"GIT_ASKPASS":         "/interactive/askpass",
		"GH_TOKEN":            "gh-secret",
		"GH_ENTERPRISE_TOKEN": "gh-enterprise-secret",
		"GITHUB_TOKEN":        "github-secret",
		"GITHUB_ACTIONS":      "true",
		"OPENAI_API_KEY":      "openai-secret",
		"CODEX_API_KEY":       "codex-secret",
		"CHATGPT_API_KEY":     "chatgpt-secret",
		"SSH_AUTH_SOCK":       "/interactive/agent.sock",
		"SSH_AGENT_PID":       "1234",
	}
	for key, value := range secrets {
		t.Setenv(key, value)
	}

	runnerHome := filepath.Join(t.TempDir(), "codex-home")
	env := safeEnv(runnerHome)
	values := map[string][]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = append(values[key], value)
		}
	}
	for key := range secrets {
		if key == "HOME" || key == "CODEX_HOME" || strings.HasPrefix(key, "XDG_") || key == "GIT_CONFIG_GLOBAL" || key == "GIT_CONFIG_SYSTEM" || key == "GIT_CONFIG_NOSYSTEM" {
			continue
		}
		if got := values[key]; len(got) != 0 {
			t.Fatalf("blocked environment key %s leaked values %q", key, got)
		}
	}
	for _, key := range []string{"GH_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_TOKEN", "GITHUB_ACTIONS", "OPENAI_API_KEY", "CODEX_API_KEY", "CHATGPT_API_KEY", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_ASKPASS", "GIT_CONFIG_COUNT"} {
		if got := values[key]; len(got) != 0 {
			t.Fatalf("interactive credential/config key %s leaked values %q", key, got)
		}
	}
	for key, want := range map[string]string{
		"HOME":                runnerHome,
		"CODEX_HOME":          runnerHome,
		"XDG_CONFIG_HOME":     runnerHome,
		"XDG_DATA_HOME":       runnerHome,
		"XDG_CACHE_HOME":      filepath.Join(runnerHome, "cache"),
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_SYSTEM":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
	} {
		if got := values[key]; len(got) != 1 || got[0] != want {
			t.Fatalf("isolated %s = %q, want [%q]", key, got, want)
		}
	}
}

func TestRunCancellationKillsProcessGroupDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups use the Unix implementation")
	}
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	logFile := filepath.Join(t.TempDir(), "cleanup.log")
	binary := fakeCleanupCodexBinary(t, pidFile, logFile)
	adapter := New(Config{Binary: binary, Home: t.TempDir(), ExpectedVersion: "0.153.4"})
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	resultCh := make(chan harness.Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := adapter.Run(ctx, harness.Launch{AttemptID: "attempt-cleanup", Workdir: t.TempDir(), Instructions: "wait"}, func(event harness.Event) error {
			if event.Type == "turn_started" {
				close(started)
			}
			return nil
		})
		resultCh <- result
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup harness did not start a turn")
	}
	var descendantPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			if descendantPID, err = strconv.Atoi(strings.TrimSpace(string(data))); err == nil && descendantPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if descendantPID == 0 {
		t.Fatal("cleanup harness did not publish descendant pid")
	}
	cancel()
	select {
	case result := <-resultCh:
		if result.Phase != "cancelled" {
			t.Fatalf("cancellation result = %+v", result)
		}
		if err := <-errCh; err != nil {
			log, _ := os.ReadFile(logFile)
			t.Fatalf("Run: %v (child log=%q)", err, log)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(descendantPID, 0); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived Codex process-group cleanup", descendantPID)
}

func fakeCleanupCodexBinary(t *testing.T, pidFile, logFile string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	content := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo codex-cli 0.153.4; exit 0; fi\nFAROS_FAKE_CLEANUP_CHILD=1 FAROS_FAKE_CLEANUP_PID=%q FAROS_FAKE_CLEANUP_LOG=%q exec %q -test.run=TestFakeCleanupAppServerProcess\n", pidFile, logFile, os.Args[0])
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestFakeCleanupAppServerProcess(t *testing.T) {
	if os.Getenv("FAROS_FAKE_CLEANUP_CHILD") != "1" {
		return
	}
	logFile := os.Getenv("FAROS_FAKE_CLEANUP_LOG")
	log := func(message string) {
		_ = os.WriteFile(logFile, []byte(message), 0o600)
	}
	child := exec.Command("sh", "-c", "sleep 60")
	if err := child.Start(); err != nil {
		log("child start: " + err.Error())
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("FAROS_FAKE_CLEANUP_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		log("pid write: " + err.Error())
		os.Exit(2)
	}
	log("ready")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]any
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		method, _ := request["method"].(string)
		id := request["id"]
		switch method {
		case "initialize":
			writeResponse(map[string]any{"id": id, "result": map[string]any{}})
		case "thread/start":
			writeResponse(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "thread-cleanup"}}})
		case "turn/start":
			writeResponse(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-cleanup", "status": "inProgress"}}})
			writeNotification("turn/started", map[string]any{"threadId": "thread-cleanup", "turn": map[string]any{"id": "turn-cleanup", "status": "inProgress"}})
		case "turn/interrupt":
			// Keep the app-server alive until the adapter's process-group kill.
		default:
			if id != nil {
				writeResponse(map[string]any{"id": id, "result": map[string]any{}})
			}
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestPluginCacheDoesNotEnableExtensions(t *testing.T) {
	binary := fakeCodexBinary(t, "success")
	home := t.TempDir()
	for _, name := range []string{"cache", ".remote-plugin-install-staging"} {
		if err := os.MkdirAll(filepath.Join(home, "plugins", name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(home, "plugins", "cache", "fixture.txt")
	if err := os.WriteFile(marker, []byte("preserve cache"), 0600); err != nil {
		t.Fatal(err)
	}
	adapter := New(Config{Home: home, Binary: binary, ExpectedVersion: "0.147.0"})
	checkArgs := func() {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "argv"))
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(strings.TrimSpace(string(raw)), "\n")
		for _, feature := range []string{"apps", "plugins", "hooks"} {
			found := false
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "--disable" && args[i+1] == feature {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s not disabled: %v", feature, args)
			}
		}
	}
	for range 2 {
		info, err := adapter.Probe(context.Background())
		if err != nil || !info.Ready {
			t.Fatalf("probe=%+v err=%v", info, err)
		}
		checkArgs()
	}
	for _, method := range readMethods(t, filepath.Join(filepath.Dir(binary), "methods")) {
		if method == "thread/start" || method == "thread/resume" || method == "turn/start" {
			t.Fatal("probe started model")
		}
	}
	for _, session := range []string{"", "original-session"} {
		result, err := adapter.Run(context.Background(), harness.Launch{Workdir: t.TempDir(), Instructions: "approved", SessionID: session}, nil)
		if err != nil || result.Phase != "completed" {
			t.Fatalf("run=%+v err=%v", result, err)
		}
		if session != "" && result.SessionID != session {
			t.Fatal("resume replaced session")
		}
		checkArgs()
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "preserve cache" {
		t.Fatal("cache changed")
	}
}

func TestPluginCacheRejectsUnsafeHomeEntries(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "case-variant"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "plugins")
			switch kind {
			case "file":
				if err := os.WriteFile(path, []byte("not a cache directory"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			case "case-variant":
				if err := os.Mkdir(filepath.Join(home, "Plugins"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			a := &Adapter{cfg: Config{Home: home}}
			if err := a.ensureHome(); err == nil {
				t.Fatal("unsafe plugin entry accepted")
			}
		})
	}
}
