//go:build !windows

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

package claude

import (
	"bufio"
	"context"
	"fmt"
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

// These tests drive a FAKE claude: a shell script that re-execs this test
// binary in a scenario mode. Nothing here ever runs the real Claude Code, so
// no credential, network call or session is involved.
const (
	fakeChildEnv    = "RAILGRID_FAKE_CLAUDE_CHILD"
	fakeScenarioEnv = "RAILGRID_FAKE_CLAUDE_SCENARIO"
	fakeArgvEnv     = "RAILGRID_FAKE_CLAUDE_ARGV"
	fakePromptEnv   = "RAILGRID_FAKE_CLAUDE_PROMPT"
	fakeEnvFileEnv  = "RAILGRID_FAKE_CLAUDE_ENVFILE"
	fakePIDFileEnv  = "RAILGRID_FAKE_CLAUDE_PIDFILE"
	fakeVersionEnv  = "RAILGRID_FAKE_CLAUDE_VERSION"
	fakeSecretEnv   = "RAILGRID_FAKE_CLAUDE_SECRET"
)

// fakeClaude writes the script and points the adapter at it. The returned dir
// is where the scenario's recordings land.
func fakeClaude(t *testing.T, scenario string) (binary, dir string) {
	t.Helper()
	dir = t.TempDir()
	script := filepath.Join(dir, "claude")
	content := fmt.Sprintf(
		"#!/bin/sh\n"+
			"if [ \"$1\" = \"--version\" ]; then echo \"${%s:-2.1.273} (Claude Code)\"; exit 0; fi\n"+
			"printf '%%s\\n' \"$@\" > %q\n"+
			"cat > %q\n"+
			"%s=1 exec %q -test.run=TestFakeClaudeProcess\n",
		fakeVersionEnv, filepath.Join(dir, "argv"), filepath.Join(dir, "prompt"), fakeChildEnv, os.Args[0])
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeScenarioEnv, scenario)
	t.Setenv(fakeArgvEnv, filepath.Join(dir, "argv"))
	t.Setenv(fakePromptEnv, filepath.Join(dir, "prompt"))
	t.Setenv(fakeEnvFileEnv, filepath.Join(dir, "env"))
	t.Setenv(fakePIDFileEnv, filepath.Join(dir, "pid"))
	return script, dir
}

// TestFakeClaudeProcess is the fake harness. It only does anything when the
// wrapper script re-execs the test binary with the child marker set.
func TestFakeClaudeProcess(t *testing.T) {
	if os.Getenv(fakeChildEnv) != "1" {
		return
	}
	scenario := os.Getenv(fakeScenarioEnv)
	secret := os.Getenv(fakeSecretEnv)
	out := bufio.NewWriter(os.Stdout)
	defer func() { _ = out.Flush() }()

	emit := func(format string, args ...any) {
		_, _ = fmt.Fprintf(out, format+"\n", args...)
		_ = out.Flush()
	}
	const session = "11111111-2222-3333-4444-555555555555"

	if envFile := os.Getenv(fakeEnvFileEnv); envFile != "" {
		_ = os.WriteFile(envFile, []byte(strings.Join(os.Environ(), "\n")), 0600)
	}

	switch scenario {
	case "env-dump", "success":
		emit(`{"type":"system","subtype":"init","session_id":%q,"model":"sonnet"}`, session)
		emit(`{"type":"assistant","session_id":%q,"message":{"role":"assistant","content":[{"type":"text","text":"working on it"},{"type":"tool_use","name":"Bash","id":"tu_1"}]}}`, session)
		emit(`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"session_id":%q,"result":"done"}`, session)
	case "resume":
		// A resume names the session on the command line; the stream still
		// carries it, and the adapter must not treat that as a new session.
		emit(`{"type":"system","subtype":"init","session_id":%q}`, session)
		emit(`{"type":"result","subtype":"success","is_error":false,"session_id":%q,"result":"resumed"}`, session)
	case "clarification":
		emit(`{"type":"system","subtype":"init","session_id":%q}`, session)
		emit(`{"type":"result","subtype":"success","is_error":false,"session_id":%q,"result":%q}`,
			session, clarificationOpen+"\nWhich database should the migration target?\n"+clarificationClose)
	case "prose-marker":
		// The marker mentioned inside ordinary prose must NOT be a question.
		emit(`{"type":"system","subtype":"init","session_id":%q}`, session)
		emit(`{"type":"result","subtype":"success","is_error":false,"session_id":%q,"result":%q}`,
			session, "I considered emitting "+clarificationOpen+" but did not need to.")
	case "error":
		emit(`{"type":"system","subtype":"init","session_id":%q}`, session)
		emit(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":%q,"result":"the tool call failed"}`, session)
	case "max-turns":
		emit(`{"type":"system","subtype":"init","session_id":%q}`, session)
		emit(`{"type":"result","subtype":"error_max_turns","is_error":false,"session_id":%q}`, session)
	case "foreign-session":
		emit(`{"type":"system","subtype":"init","session_id":%q}`, session)
		emit(`{"type":"assistant","session_id":"99999999-9999-9999-9999-999999999999","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
	case "no-result":
		emit(`{"type":"system","subtype":"init","session_id":%q}`, session)
		fmt.Fprintln(os.Stderr, "claude: upstream refused the request")
	case "oversized-line":
		emit(`{"type":"system","subtype":"init","session_id":%q}`, session)
		emit(`{"type":"assistant","session_id":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`,
			session, strings.Repeat("x", maxWireLine+1024))
	case "huge-message":
		emit(`{"type":"system","subtype":"init","session_id":%q}`, session)
		emit(`{"type":"assistant","session_id":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`,
			session, strings.Repeat("y", maxMessageText*2))
		emit(`{"type":"result","subtype":"success","is_error":false,"session_id":%q,"result":"done"}`, session)
	case "leak-secret":
		// The harness printing the credential back at us is the case redaction
		// exists for.
		emit(`{"type":"system","subtype":"init","session_id":%q}`, session)
		emit(`{"type":"assistant","session_id":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`,
			session, "I read the token "+secret+" from the environment")
		emit(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":%q,"result":%q}`,
			session, "failed while using "+secret)
	case "hang":
		emit(`{"type":"system","subtype":"init","session_id":%q}`, session)
		// A grandchild in the same process group, so a test can prove the whole
		// group is killed rather than just the direct child.
		child := exec.Command(os.Args[0], "-test.run=TestFakeClaudeProcess")
		child.Env = []string{fakeChildEnv + "=1", fakeScenarioEnv + "=sleep"}
		if err := child.Start(); err == nil {
			if pidFile := os.Getenv(fakePIDFileEnv); pidFile != "" {
				_ = os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0600)
			}
		}
		time.Sleep(5 * time.Minute)
	case "sleep":
		time.Sleep(5 * time.Minute)
	}
	os.Exit(0)
}

// credentialFile writes an owner-only credential and returns its path.
func credentialFile(t *testing.T, value string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credential")
	if err := os.WriteFile(path, []byte(value+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testAdapter(t *testing.T, binary string, mutate ...func(*Config)) *Adapter {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Binary:         binary,
		Home:           filepath.Join(home, "claude-home"),
		CredentialFile: credentialFile(t, "sk-test-credential-value"),
		CredentialKind: CredentialOAuthToken,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	adapter, ok := New(cfg).(*Adapter)
	if !ok {
		t.Fatal("New did not return the Claude adapter")
	}
	return adapter
}

func launch(mutate ...func(*harness.Launch)) harness.Launch {
	l := harness.Launch{
		AttemptID:    "attempt-1",
		Workdir:      "",
		Instructions: "make the tests pass",
	}
	for _, m := range mutate {
		m(&l)
	}
	return l
}

func collect(events *[]harness.Event) harness.Emit {
	return func(e harness.Event) error {
		*events = append(*events, e)
		return nil
	}
}

func TestProbeReportsVersionAndCredential(t *testing.T) {
	binary, _ := fakeClaude(t, "success")
	adapter := testAdapter(t, binary)

	info, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Name != HarnessName {
		t.Errorf("harness name = %q, want %q", info.Name, HarnessName)
	}
	if info.Version != "2.1.273" {
		t.Errorf("version = %q", info.Version)
	}
	if !info.Ready || len(info.Reasons) != 0 {
		t.Errorf("probe = %+v, want ready", info)
	}
}

// TestProbeVersionPinIsAReasonNotAnError mirrors the Codex adapter: a pin
// mismatch leaves the harness unready with an explanation, it does not make the
// runner fail to start.
func TestProbeVersionPinIsAReasonNotAnError(t *testing.T) {
	binary, _ := fakeClaude(t, "success")
	adapter := testAdapter(t, binary, func(c *Config) { c.ExpectedVersion = "9.9.9" })

	info, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Ready {
		t.Fatal("a version-pin mismatch reported ready")
	}
	if len(info.Reasons) == 0 || !strings.Contains(info.Reasons[0], "9.9.9") {
		t.Errorf("reasons = %v, want the expected version named", info.Reasons)
	}
}

// TestProbeWithoutACredentialIsNotReady: the credential is what makes the
// harness usable, so its absence is a readiness reason — and the reason must
// not quote the file's contents.
func TestProbeWithoutACredentialIsNotReady(t *testing.T) {
	binary, _ := fakeClaude(t, "success")
	adapter := testAdapter(t, binary, func(c *Config) {
		c.CredentialFile = filepath.Join(filepath.Dir(c.CredentialFile), "absent")
	})

	info, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Ready {
		t.Fatal("probe reported ready with no credential")
	}
	if len(info.Reasons) == 0 || !strings.Contains(strings.ToLower(info.Reasons[0]), "authentication") {
		t.Errorf("reasons = %v", info.Reasons)
	}
}

// TestProbeRejectsAWorldReadableCredential: a credential another local account
// can read is not a credential.
func TestProbeRejectsAWorldReadableCredential(t *testing.T) {
	binary, _ := fakeClaude(t, "success")
	adapter := testAdapter(t, binary)
	if err := os.Chmod(adapter.cfg.CredentialFile, 0644); err != nil {
		t.Fatal(err)
	}
	info, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Ready {
		t.Fatal("probe accepted a world-readable credential")
	}
}

func TestRunEmitsBoundedEventsAndCompletes(t *testing.T) {
	binary, dir := fakeClaude(t, "success")
	adapter := testAdapter(t, binary)
	var events []harness.Event

	result, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir() }), collect(&events))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "completed" {
		t.Fatalf("result = %+v, want completed", result)
	}
	if result.SessionID == "" {
		t.Error("a completed run returned no session id; resume would be impossible")
	}

	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Type)
	}
	for _, want := range []string{"session", "message", "tool_use", "result"} {
		if !contains(kinds, want) {
			t.Errorf("event %q missing from %v", want, kinds)
		}
	}

	// The command line is the isolation contract; pin it.
	argv := readLines(t, filepath.Join(dir, "argv"))
	for _, want := range []string{
		"--print", "--output-format", "stream-json", "--verbose",
		"--permission-mode", "dontAsk", "--permission-prompts", "none",
		"--safe-mode", "--strict-mcp-config", "--disable-slash-commands",
		"--no-chrome", "--setting-sources",
	} {
		if !contains(argv, want) {
			t.Errorf("argv %v lacks %q", argv, want)
		}
	}
	for _, never := range []string{"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions", "--ide", "--mcp-config"} {
		if contains(argv, never) {
			t.Errorf("argv must never contain %q: %v", never, argv)
		}
	}

	// The prompt goes over stdin, never as an argv element.
	prompt, err := os.ReadFile(filepath.Join(dir, "prompt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "make the tests pass") {
		t.Errorf("the instructions did not reach stdin: %q", prompt)
	}
	if !strings.Contains(string(prompt), clarificationOpen) {
		t.Error("the clarification preamble was not prepended")
	}
	for _, arg := range argv {
		if strings.Contains(arg, "make the tests pass") {
			t.Fatalf("the instructions were passed as an argument: %v", argv)
		}
	}
}

// TestRunResumePassesTheSessionID: without --resume a "continue this work"
// attempt would silently start a second conversation.
func TestRunResumePassesTheSessionID(t *testing.T) {
	binary, dir := fakeClaude(t, "resume")
	adapter := testAdapter(t, binary)
	const session = "11111111-2222-3333-4444-555555555555"

	result, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) {
		l.Workdir = t.TempDir()
		l.SessionID = session
	}), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "completed" || result.SessionID != session {
		t.Fatalf("result = %+v", result)
	}
	argv := readLines(t, filepath.Join(dir, "argv"))
	index := indexOf(argv, "--resume")
	if index < 0 || index+1 >= len(argv) || argv[index+1] != session {
		t.Fatalf("argv did not resume the session: %v", argv)
	}
	if contains(argv, "--session-id") {
		t.Errorf("a resume must not also pin a new session id: %v", argv)
	}
}

// TestRunRejectsAForeignSession: a record naming another conversation means the
// turn is not running where the caller thinks it is.
func TestRunRejectsAForeignSession(t *testing.T) {
	binary, _ := fakeClaude(t, "foreign-session")
	adapter := testAdapter(t, binary)

	result, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir() }), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "failed" {
		t.Fatalf("result = %+v, want failed", result)
	}
	if !strings.Contains(result.Blocker, "foreign session") {
		t.Errorf("blocker = %q", result.Blocker)
	}
}

func TestRunMapsTerminalRecords(t *testing.T) {
	for name, expect := range map[string]struct {
		scenario string
		phase    string
	}{
		"error":         {"error", "failed"},
		"turn limit":    {"max-turns", "needs_input"},
		"no result":     {"no-result", "failed"},
		"prose marker":  {"prose-marker", "completed"},
		"clarification": {"clarification", "needs_input"},
	} {
		t.Run(name, func(t *testing.T) {
			binary, _ := fakeClaude(t, expect.scenario)
			adapter := testAdapter(t, binary)
			result, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir() }), nil)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Phase != expect.phase {
				t.Fatalf("phase = %q, want %q (blocker %q)", result.Phase, expect.phase, result.Blocker)
			}
			if result.SessionID == "" && expect.scenario != "no-result" {
				t.Error("no session id was returned; the conversation could not be resumed")
			}
			switch expect.scenario {
			case "clarification":
				if result.Clarification == nil || !strings.Contains(result.Clarification.Text, "Which database") {
					t.Fatalf("clarification = %+v", result.Clarification)
				}
				if strings.Contains(result.Clarification.Text, clarificationOpen) {
					t.Error("the marker leaked into the question text")
				}
			case "prose-marker":
				if result.Clarification != nil {
					t.Errorf("prose mentioning the marker produced a question: %+v", result.Clarification)
				}
			}
		})
	}
}

// TestRunBoundsEventSizes: a harness that streams without limit must not be
// able to exhaust the runner.
func TestRunBoundsEventSizes(t *testing.T) {
	t.Run("oversized line", func(t *testing.T) {
		binary, _ := fakeClaude(t, "oversized-line")
		adapter := testAdapter(t, binary)
		result, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir() }), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Phase != "failed" || !strings.Contains(result.Blocker, "oversized") {
			t.Fatalf("result = %+v, want a bounded-stream failure", result)
		}
	})

	t.Run("huge message", func(t *testing.T) {
		binary, _ := fakeClaude(t, "huge-message")
		adapter := testAdapter(t, binary)
		var events []harness.Event
		if _, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir() }), collect(&events)); err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, e := range events {
			if len(e.Message) > maxMessageText+32 {
				t.Fatalf("event %q message is %d bytes, over the bound", e.Type, len(e.Message))
			}
			if len(e.Data) > maxEventData {
				t.Fatalf("event %q data is %d bytes, over the bound", e.Type, len(e.Data))
			}
		}
	})
}

// TestRunRedactsTheCredential is the test the credential design exists for: a
// harness that echoes its token back must not get it into an event, a blocker
// or a replayable record.
func TestRunRedactsTheCredential(t *testing.T) {
	const secret = "sk-ant-oat01-super-secret-value"
	binary, _ := fakeClaude(t, "leak-secret")
	t.Setenv(fakeSecretEnv, secret)
	adapter := testAdapter(t, binary, func(c *Config) {
		c.CredentialFile = credentialFile(t, secret)
	})

	var events []harness.Event
	result, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir() }), collect(&events))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "failed" {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(result.Blocker, secret) {
		t.Fatalf("the blocker leaked the credential: %q", result.Blocker)
	}
	for _, e := range events {
		if strings.Contains(e.Message, secret) {
			t.Errorf("event %q leaked the credential in its message", e.Type)
		}
		if strings.Contains(string(e.Data), secret) {
			t.Errorf("event %q leaked the credential in its data", e.Type)
		}
	}
}

// TestChildEnvironmentIsolation: the harness child sees the credential and
// nothing else from the runner's own environment — in particular no inherited
// ANTHROPIC_*/CLAUDE_* variable, which would otherwise silently authenticate
// as the operator or redirect the API base URL.
func TestChildEnvironmentIsolation(t *testing.T) {
	poison := map[string]string{
		"ANTHROPIC_API_KEY":        "operator-personal-key",
		"ANTHROPIC_BASE_URL":       "https://attacker.example",
		"ANTHROPIC_AUTH_TOKEN":     "operator-auth-token",
		"CLAUDE_CODE_OAUTH_TOKEN":  "operator-oauth-token",
		"CLAUDE_CONFIG_DIR":        "/home/operator/.claude",
		"GITHUB_TOKEN":             "ghp_operator",
		"SSH_AUTH_SOCK":            "/tmp/agent.sock",
		"OPENAI_API_KEY":           "sk-openai",
		"GIT_CONFIG_GLOBAL":        "/home/operator/.gitconfig",
		"RAILGRID_UNRELATED_VALUE": "kept",
	}
	for k, v := range poison {
		t.Setenv(k, v)
	}

	const secret = "sk-ant-oat01-the-only-credential"
	binary, dir := fakeClaude(t, "env-dump")
	adapter := testAdapter(t, binary, func(c *Config) { c.CredentialFile = credentialFile(t, secret) })

	if _, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir() }), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	env := map[string]string{}
	for _, line := range readLines(t, filepath.Join(dir, "env")) {
		if key, value, ok := strings.Cut(line, "="); ok {
			env[key] = value
		}
	}

	// The credential is the ONE Anthropic/Claude variable.
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != secret {
		t.Errorf("the credential was not injected: %q", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	if env["ANTHROPIC_API_KEY"] != "" {
		t.Errorf("an inherited ANTHROPIC_API_KEY reached the child: %q", env["ANTHROPIC_API_KEY"])
	}
	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "OPENAI_API_KEY"} {
		if env[key] != "" {
			t.Errorf("%s leaked into the child: %q", key, env[key])
		}
	}
	for _, value := range []string{"operator-personal-key", "operator-oauth-token", "ghp_operator", "sk-openai"} {
		for key, got := range env {
			if got == value {
				t.Errorf("%s carries an inherited secret value", key)
			}
		}
	}
	// The isolation variables are set from scratch, not inherited.
	if env["CLAUDE_CONFIG_DIR"] != adapter.cfg.Home || env["HOME"] != adapter.cfg.Home {
		t.Errorf("home = %q / %q, want %q", env["CLAUDE_CONFIG_DIR"], env["HOME"], adapter.cfg.Home)
	}
	if env["GIT_CONFIG_GLOBAL"] != os.DevNull || env["GIT_CONFIG_NOSYSTEM"] != "1" {
		t.Errorf("git configuration was not neutralized: %q", env["GIT_CONFIG_GLOBAL"])
	}
	if env["DISABLE_AUTOUPDATER"] != "1" || env["DISABLE_TELEMETRY"] != "1" {
		t.Error("self-update and telemetry were not disabled")
	}
	// Unrelated variables still pass through, as they do for Codex: the deny
	// list is targeted, not a whitelist.
	if env["RAILGRID_UNRELATED_VALUE"] != "kept" {
		t.Error("the deny list dropped an unrelated variable")
	}
}

// TestRunCancelKillsTheProcessGroup: Claude Code starts compilers, test runners
// and git. Cancelling the attempt has to take out the whole group.
func TestRunCancelKillsTheProcessGroup(t *testing.T) {
	binary, dir := fakeClaude(t, "hang")
	adapter := testAdapter(t, binary)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan harness.Result, 1)
	go func() {
		result, err := adapter.Run(ctx, launch(func(l *harness.Launch) { l.Workdir = t.TempDir() }), nil)
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- result
	}()

	pidPath := filepath.Join(dir, "pid")
	grandchild := 0
	deadline := time.Now().Add(20 * time.Second)
	for grandchild == 0 {
		if raw, err := os.ReadFile(pidPath); err == nil {
			grandchild, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake harness never started a grandchild")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	select {
	case result := <-done:
		if result.Phase != "cancelled" {
			t.Fatalf("result = %+v, want cancelled", result)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	deadline = time.Now().Add(20 * time.Second)
	for syscall.Kill(grandchild, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived cancellation; the process group was not killed", grandchild)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRunWithoutACredentialStartsNothing: an unauthenticated turn would fail
// against the API anyway, so it is held as needs_input instead of burning an
// attempt.
func TestRunWithoutACredentialStartsNothing(t *testing.T) {
	binary, dir := fakeClaude(t, "success")
	adapter := testAdapter(t, binary, func(c *Config) {
		c.CredentialFile = filepath.Join(filepath.Dir(c.CredentialFile), "absent")
	})

	result, err := adapter.Run(context.Background(), launch(func(l *harness.Launch) { l.Workdir = t.TempDir() }), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Phase != "needs_input" {
		t.Fatalf("result = %+v, want needs_input", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "argv")); err == nil {
		t.Error("the harness was started without a credential")
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func contains(values []string, want string) bool { return indexOf(values, want) >= 0 }

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}
