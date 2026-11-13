//go:build linux

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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func TestSystemdScopeCommandUsesExactUnitAndSanitizedControlEnvironment(t *testing.T) {
	unit := "railgrid-codex-0123456789abcdef0123456789abcdef.scope"
	scope := &userSystemdScope{
		unit:        unit,
		systemdRun:  "/usr/bin/systemd-run",
		envPath:     "/usr/bin/env",
		runtimePath: "/run/user/1000",
	}
	cmd := scope.command("/opt/codex", []string{"app-server", "--listen", "stdio://"}, []string{
		"PATH=/usr/bin:/bin",
		"HOME=/runner/codex-home",
		"GH_TOKEN=launch-only-secret",
		"MCP_TOKEN_server=another-secret",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
	})
	args := strings.Join(cmd.Args, "\x00")
	for _, want := range []string{"--scope", "--collect", "--unit\x00" + unit, "KillMode=control-group", "TimeoutStopSec=2s", "\x00/usr/bin/env\x00-u\x00XDG_RUNTIME_DIR\x00-u\x00DBUS_SESSION_BUS_ADDRESS\x00--\x00/opt/codex"} {
		if !strings.Contains(args, want) {
			t.Fatalf("systemd-run args missing %q: %q", want, cmd.Args)
		}
	}
	if strings.Contains(args, "launch-only-secret") || strings.Contains(args, "another-secret") {
		t.Fatal("app credentials leaked into systemd-run argv")
	}
	if envValue(cmd.Env, "GH_TOKEN") != "launch-only-secret" || envValue(cmd.Env, "MCP_TOKEN_server") != "another-secret" {
		t.Fatal("systemd-run did not preserve the existing app-server credential environment")
	}
	if envValue(cmd.Env, "XDG_RUNTIME_DIR") != "/run/user/1000" || envValue(cmd.Env, "DBUS_SESSION_BUS_ADDRESS") != "" {
		t.Fatalf("systemd control environment = %v", cmd.Env)
	}
	if envValue(scope.controlEnv, "GH_TOKEN") != "" || envValue(scope.controlEnv, "MCP_TOKEN_server") != "" {
		t.Fatalf("systemctl control environment contains launch credentials: %v", scope.controlEnv)
	}
}

func TestScopeStopRunsEvenWhenAppServerExitedEarly(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "stopped")
	scope := fakeSystemdScope(t, marker, false)
	p := &serverProcess{stdin: failingStdin{}, done: make(chan error, 1), scope: scope}
	p.done <- nil
	if err := p.stop(); err != nil {
		t.Fatalf("stop after early app-server exit: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("scope stop was not called before returning: %v", err)
	}
}

func TestScopeStopFailureIsExplicitlyUnconfirmed(t *testing.T) {
	scope := fakeSystemdScope(t, filepath.Join(t.TempDir(), "stopped"), true)
	err := scope.stop()
	if !errors.Is(err, harness.ErrProcessCleanupUnconfirmed) {
		t.Fatalf("scope stop error = %v, want cleanup-unconfirmed sentinel", err)
	}
	var cleanupErr *harness.ProcessCleanupError
	if !errors.As(err, &cleanupErr) || cleanupErr.Reference != scope.unit {
		t.Fatalf("cleanup error = %#v, want persisted scope reference %q", cleanupErr, scope.unit)
	}
}

func TestRecoverSystemdScopeStopsAndConfirmsSavedUnit(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "stopped")
	systemctl := filepath.Join(dir, "systemctl")
	script := "#!/bin/sh\ncase \"$2\" in\n" +
		"show) if [ -e " + shellQuote(marker) + " ]; then echo not-found; else echo loaded; fi ;;\n" +
		"stop) : > " + shellQuote(marker) + ";;\n*) exit 91 ;;\nesac\n"
	if err := os.WriteFile(systemctl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	scope := &userSystemdScope{unit: "railgrid-codex-0123456789abcdef0123456789abcdef.scope", systemctl: systemctl, controlEnv: []string{"PATH=/usr/bin:/bin"}}
	done, err := recoverSystemdScope(context.Background(), scope)
	if err != nil || !done {
		t.Fatalf("recovery = %v, %v; want confirmed cleanup", done, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("saved scope was not stopped: %v", err)
	}
}

func TestStopKillsSameGroupDescendantsAfterParentExitsEarly(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	cmd := exec.Command("sh", "-c", `sleep 60 >/dev/null 2>&1 & echo $! > "$1"; exit 0`, "sh", pidFile)
	configureProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			childPID, readErr = strconv.Atoi(strings.TrimSpace(string(data)))
			if readErr == nil && childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("child process did not publish its pid")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for early parent exit: %v", err)
	}
	p := &serverProcess{cmd: cmd, stdin: stdin, done: make(chan error, 1)}
	p.done <- nil
	if err := p.stop(); err != nil {
		t.Fatalf("stop after parent exit: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if processExited(childPID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("same-group child %d survived stop after parent exit", childPID)
}

func processExited(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	closeParen := strings.LastIndexByte(string(data), ')')
	return closeParen >= 0 && strings.HasPrefix(strings.TrimSpace(string(data[closeParen+1:])), "Z ")
}

func fakeSystemdScope(t *testing.T, marker string, failStop bool) *userSystemdScope {
	t.Helper()
	dir := t.TempDir()
	systemctl := filepath.Join(dir, "systemctl")
	stopExit := "0"
	if failStop {
		stopExit = "1"
	}
	script := "#!/bin/sh\n" +
		"case \"$2\" in\n" +
		"show) echo loaded ;;\n" +
		"stop) if [ -n \"${GH_TOKEN:-}\" ] || [ -n \"${MCP_TOKEN_server:-}\" ]; then exit 90; fi; : > " + shellQuote(marker) + "; exit " + stopExit + " ;;\n" +
		"*) exit 91 ;;\n" +
		"esac\n"
	if err := os.WriteFile(systemctl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return &userSystemdScope{
		unit:      "railgrid-codex-0123456789abcdef0123456789abcdef.scope",
		systemctl: systemctl,
		controlEnv: []string{
			"PATH=/usr/bin:/bin",
			"XDG_RUNTIME_DIR=/run/user/1000",
			"LC_ALL=C",
		},
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
