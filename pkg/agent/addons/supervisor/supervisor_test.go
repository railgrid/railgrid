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

package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The supervised child is always a real process, so these tests re-exec the
// TEST BINARY in a helper mode rather than pulling in the runner or Codex.
// TestMain intercepts the mode before the test framework starts, which is the
// standard way to get a controllable child without shipping a fixture binary.
const (
	helperModeEnv = "RAILGRID_TEST_SUPERVISOR_MODE"
	helperOutEnv  = "RAILGRID_TEST_SUPERVISOR_OUT"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(runHelper(mode))
	}
	os.Exit(m.Run())
}

func runHelper(mode string) int {
	out := os.Getenv(helperOutEnv)
	switch mode {
	case "exit":
		// An immediate non-zero exit: the crash loop the supervisor backs off.
		return 3
	case "dump-env":
		var b strings.Builder
		for _, item := range os.Environ() {
			b.WriteString(item)
			b.WriteString("\n")
		}
		if err := os.WriteFile(out, []byte(b.String()), 0600); err != nil {
			return 1
		}
		blockForever()
	case "spawn":
		// Start a grandchild in the SAME process group and record its pid, so a
		// test can prove the whole group is signalled on stop.
		cmd := exec.Command(os.Args[0])
		cmd.Env = []string{helperModeEnv + "=sleep"}
		if err := cmd.Start(); err != nil {
			return 1
		}
		if err := os.WriteFile(out, []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
			return 1
		}
		blockForever()
	case "sleep":
		blockForever()
	}
	return 0
}

// blockForever keeps a helper alive until the supervisor signals it. A bare
// select{} would trip Go's deadlock detector and exit, which would make the
// helper look like a crashing child instead of a running one.
func blockForever() {
	time.Sleep(10 * time.Minute)
}

func helperConfig(t *testing.T, mode string, extraEnv ...string) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{
		Name:           "test/" + mode,
		Executable:     os.Args[0],
		Dir:            dir,
		Env:            append([]string{helperModeEnv + "=" + mode}, extraEnv...),
		UID:            InheritUID,
		GID:            InheritUID,
		LogPath:        filepath.Join(dir, "child.log"),
		InitialBackoff: 20 * time.Millisecond,
		StopGrace:      2 * time.Second,
	}
}

// TestNewRefusesRootTargets: the supervisor exists so a ROOT agent can launch a
// code-execution child without handing it root. A uid-0 target defeats the
// entire point, so it is refused at construction rather than at exec time.
func TestNewRefusesRootTargets(t *testing.T) {
	base := Config{Executable: "/bin/true", LogPath: "/tmp/x.log"}

	for name, cfg := range map[string]Config{
		"uid 0":              {Executable: base.Executable, LogPath: base.LogPath, UID: 0, GID: 20},
		"gid 0":              {Executable: base.Executable, LogPath: base.LogPath, UID: 501, GID: 0},
		"uid 0 and gid 0":    {Executable: base.Executable, LogPath: base.LogPath, UID: 0, GID: 0},
		"uid set, gid unset": {Executable: base.Executable, LogPath: base.LogPath, UID: 501, GID: InheritUID},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted a root/incoherent credential", name)
		}
	}

	if _, err := New(Config{Executable: base.Executable, LogPath: base.LogPath, UID: 501, GID: 20}); err != nil {
		t.Errorf("New rejected a valid non-root credential: %v", err)
	}
}

// TestChildEnvIsBuiltFromScratch: the child's environment must contain nothing
// from the agent's. The agent holds the hub bearer token and the path to the
// agent kubeconfig; a code-execution add-on that inherited either would be a
// privilege escalation from "run code on this box" to "drive the tenant's
// workspace".
func TestChildEnvIsBuiltFromScratch(t *testing.T) {
	poison := map[string]string{
		"RAILGRID_HUB_TOKEN":    "hub-bearer-token",
		"KUBECONFIG":            "/root/.railgrid/agent-edge.kubeconfig",
		"OPENAI_API_KEY":        "sk-should-never-be-inherited",
		"GITHUB_TOKEN":          "ghp_should-never-be-inherited",
		"SSH_AUTH_SOCK":         "/tmp/agent.sock",
		"AWS_SECRET_ACCESS_KEY": "aws-secret",
	}
	for k, v := range poison {
		t.Setenv(k, v)
	}

	env := ChildEnv("/home/runner", "")
	if len(env) != 2 || env[0] != "HOME=/home/runner" || env[1] != "PATH="+DefaultPath {
		t.Fatalf("ChildEnv = %v, want exactly HOME and PATH", env)
	}

	// End to end: what the child actually sees.
	outPath := filepath.Join(t.TempDir(), "env.txt")
	cfg := helperConfig(t, "dump-env", helperOutEnv+"="+outPath)
	cfg.Env = append(ChildEnv(cfg.Dir, ""), cfg.Env...)
	sup, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sup.Start(context.Background())
	t.Cleanup(func() { _ = sup.Stop(context.Background()) })

	data := waitForFile(t, outPath, 10*time.Second)
	for key, value := range poison {
		if strings.Contains(string(data), key+"=") {
			t.Errorf("child environment leaked %s", key)
		}
		if strings.Contains(string(data), value) {
			t.Errorf("child environment leaked the value of %s", key)
		}
	}
}

// TestRestartBackoff covers the pure schedule: a crashing child is retried with
// a doubling pause capped at a minute, and a child that managed a stable run
// starts over from the initial pause instead of inheriting the old one.
func TestRestartBackoff(t *testing.T) {
	initial := time.Second
	cur := initial
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, expect := range want {
		cur = nextBackoff(cur, initial, 0)
		if cur != expect {
			t.Fatalf("backoff[%d] = %s, want %s", i, cur, expect)
		}
	}
	cur = 40 * time.Second
	if got := nextBackoff(cur, initial, 0); got != MaxBackoff {
		t.Errorf("backoff is not capped: %s", got)
	}
	if got := nextBackoff(MaxBackoff, initial, 0); got != MaxBackoff {
		t.Errorf("backoff exceeded the cap: %s", got)
	}
	if got := nextBackoff(MaxBackoff, initial, stableRun); got != initial {
		t.Errorf("a stable run did not reset the backoff: %s", got)
	}
}

// TestSupervisorRestartsACrashingChild: the schedule above is only useful if
// the loop actually re-launches. A child that exits immediately must be
// retried, and its exit recorded.
func TestSupervisorRestartsACrashingChild(t *testing.T) {
	sup, err := New(helperConfig(t, "exit"))
	if err != nil {
		t.Fatal(err)
	}
	sup.Start(context.Background())
	t.Cleanup(func() { _ = sup.Stop(context.Background()) })

	deadline := time.Now().Add(15 * time.Second)
	for sup.Starts() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("child was started only %d times; want at least 3 restarts", sup.Starts())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sup.LastError() == nil {
		t.Error("a crashing child left no recorded error")
	}
}

// TestStopKillsTheProcessGroup: the runner starts Codex, which starts git.
// Signalling only the direct child would leave those holding the add-on's
// worktrees, so Stop must take out the whole group.
func TestStopKillsTheProcessGroup(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "grandchild.pid")
	cfg := helperConfig(t, "spawn", helperOutEnv+"="+outPath)
	sup, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sup.Start(context.Background())

	raw := waitForFile(t, outPath, 10*time.Second)
	grandchild, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("grandchild pid %q: %v", raw, err)
	}
	if !processAlive(grandchild) {
		t.Fatalf("grandchild %d was never alive", grandchild)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := sup.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if sup.Alive() {
		t.Error("supervisor still reports a live child after Stop")
	}

	deadline := time.Now().Add(10 * time.Second)
	for processAlive(grandchild) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived Stop; the process group was not signalled", grandchild)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestBoundedLogTruncatesInsteadOfGrowing: a crash-looping add-on that prints
// on every start must not fill the host's disk.
func TestBoundedLogTruncatesInsteadOfGrowing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded.log")
	log, err := openBoundedLog(path, 64, InheritUID, InheritUID)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close() //nolint:errcheck

	for i := 0; i < 50; i++ {
		if _, err := fmt.Fprintf(log, "line %02d ............................\n", i); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 64 {
		t.Errorf("log grew to %d bytes past its 64 byte budget", info.Size())
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("log mode = %v, want 0600", perm)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
		if err == nil && len(data) > 0 {
			return data
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
