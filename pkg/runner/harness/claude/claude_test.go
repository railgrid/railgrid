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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func TestCredentialKindEnvVars(t *testing.T) {
	if got := CredentialOAuthToken.EnvVar(); got != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("oauth-token env = %q", got)
	}
	if got := CredentialAPIKey.EnvVar(); got != "ANTHROPIC_API_KEY" {
		t.Errorf("api-key env = %q", got)
	}
	if CredentialKind("password").Valid() {
		t.Error("an unknown credential kind was accepted")
	}
}

// TestBlockedEnvKeyStripsTheWholeAnthropicSpace: the deny list has to cover the
// PREFIX, not a list of known names. A new ANTHROPIC_* knob in a future Claude
// Code release must not become an inherited configuration channel.
func TestBlockedEnvKeyStripsTheWholeAnthropicSpace(t *testing.T) {
	for _, key := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
		"ANTHROPIC_SOMETHING_INVENTED_LATER",
		"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR", "CLAUDE_SOMETHING_NEW",
		"HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "SSH_AUTH_SOCK",
		"GITHUB_TOKEN", "GH_TOKEN", "OPENAI_API_KEY", "CODEX_HOME",
		"DISABLE_TELEMETRY",
	} {
		if !blockedEnvKey(key) {
			t.Errorf("%s is not blocked", key)
		}
	}
	for _, key := range []string{"PATH", "LANG", "TMPDIR", "RAILGRID_EDGE"} {
		if blockedEnvKey(key) {
			t.Errorf("%s should pass through", key)
		}
	}
}

func TestRedact(t *testing.T) {
	cred := credential{env: "CLAUDE_CODE_OAUTH_TOKEN", value: "sk-secret"}
	if got := redact("the token sk-secret failed", cred); strings.Contains(got, "sk-secret") {
		t.Errorf("redact left the value in %q", got)
	}
	// An empty credential must not turn every string into "[redacted]".
	if got := redact("nothing to hide", credential{}); got != "nothing to hide" {
		t.Errorf("redact mangled a string with no credential: %q", got)
	}
}

// TestParseClarificationOnlyAcceptsAWholeBlock: a missed clarification just
// completes the attempt, while a false one parks work forever on a question
// nobody asked — so the parser is deliberately strict.
func TestParseClarificationOnlyAcceptsAWholeBlock(t *testing.T) {
	good := clarificationOpen + "\nWhich API version?\n" + clarificationClose
	c := parseClarification("session-1", good)
	if c == nil || c.Text != "Which API version?" {
		t.Fatalf("clarification = %+v", c)
	}
	if !strings.HasPrefix(c.ID, "clarification-") {
		t.Errorf("id = %q", c.ID)
	}
	// Stable across repeats, distinct per question and per session.
	if parseClarification("session-1", good).ID != c.ID {
		t.Error("the id is not stable for the same question")
	}
	if parseClarification("session-2", good).ID == c.ID {
		t.Error("the id does not distinguish sessions")
	}

	for name, text := range map[string]string{
		"prose around the block": "I think " + good + " is what I would ask.",
		"opener only":            clarificationOpen + "\nWhich API version?",
		"closer only":            "Which API version?\n" + clarificationClose,
		"empty question":         clarificationOpen + "\n   \n" + clarificationClose,
		"two blocks":             good + "\n" + good,
		"plain prose":            "Which API version should I target?",
		"empty":                  "",
		"oversized":              clarificationOpen + "\n" + strings.Repeat("q", maxClarificationText+1) + "\n" + clarificationClose,
	} {
		if got := parseClarification("session-1", text); got != nil {
			t.Errorf("%s was accepted as a clarification: %+v", name, got)
		}
	}
}

// TestPromptPreambleTeachesTheMarker: the convention only works if the model is
// told about it on every turn, resumes included.
func TestPromptPreambleTeachesTheMarker(t *testing.T) {
	if !strings.Contains(promptPreamble, clarificationOpen) || !strings.Contains(promptPreamble, clarificationClose) {
		t.Fatal("the preamble does not document the marker it parses")
	}
	if !strings.HasSuffix(promptPreamble, "Task:\n") {
		t.Error("the preamble must end by handing over to the caller's instructions")
	}
}

func TestValidateLaunch(t *testing.T) {
	if err := validateLaunch(harness.Launch{Instructions: "do it"}); err == nil {
		t.Error("a launch without a workdir was accepted")
	}
	if err := validateLaunch(harness.Launch{Workdir: "/tmp"}); err == nil {
		t.Error("a launch without instructions was accepted")
	}
}

// TestValidateWorkdirRequiresAManagedCheckout mirrors the Codex adapter: a turn
// must run in a directory the RUNNER created, two levels under the managed
// worktree root, never somewhere a request chose.
func TestValidateWorkdirRequiresAManagedCheckout(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(root, "task-1", "attempt-1")
	if err := os.MkdirAll(managed, 0700); err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{cfg: Config{WorktreeRoot: root}}

	if err := adapter.validateWorkdir(managed); err != nil {
		t.Fatalf("a managed checkout was rejected: %v", err)
	}
	for name, dir := range map[string]string{
		"the root itself":  root,
		"one level down":   filepath.Join(root, "task-1"),
		"outside the root": filepath.Dir(root),
		"relative":         "task-1/attempt-1",
		"traversal":        filepath.Join(root, "task-1", "..", "..", "etc"),
	} {
		if err := adapter.validateWorkdir(dir); err == nil {
			t.Errorf("%s was accepted as a managed checkout", name)
		}
	}

	// A symlinked component is refused even when the target is inside.
	link := filepath.Join(root, "task-2")
	if err := os.Symlink(filepath.Join(root, "task-1"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := adapter.validateWorkdir(filepath.Join(link, "attempt-1")); err == nil {
		t.Error("a symlinked path component was accepted")
	}

	// No configured root means a library caller opted out, as for Codex.
	none := &Adapter{cfg: Config{}}
	if err := none.validateWorkdir("/anywhere"); err != nil {
		t.Errorf("an unset worktree root should not constrain the workdir: %v", err)
	}
}

// TestEnsureHomeRejectsAnInteractiveHome: a home carrying project-controlled
// configuration is not a dedicated worker home, and running there would load
// settings, hooks or MCP servers this adapter exists to keep out.
func TestEnsureHomeRejectsAnInteractiveHome(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	clean := filepath.Join(base, "clean")
	adapter := &Adapter{cfg: Config{Home: clean}}
	if err := adapter.ensureHome(); err != nil {
		t.Fatalf("a fresh home was rejected: %v", err)
	}
	info, err := os.Stat(clean)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("home mode = %v, want 0700", perm)
	}

	for _, entry := range []string{"settings.json", "hooks", "plugins", "mcp.json", "skills"} {
		dirty := filepath.Join(base, "dirty-"+strings.TrimSuffix(entry, ".json"))
		if err := os.MkdirAll(dirty, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dirty, entry), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		a := &Adapter{cfg: Config{Home: dirty}}
		if err := a.ensureHome(); err == nil {
			t.Errorf("a home containing %q was accepted", entry)
		}
	}

	if err := (&Adapter{cfg: Config{Home: "relative/home"}}).ensureHome(); err == nil {
		t.Error("a relative home was accepted")
	}
	if err := (&Adapter{cfg: Config{}}).ensureHome(); err == nil {
		t.Error("an empty home was accepted")
	}
}

func TestNewDefaultsTheBinaryAndLeavesTheVersionUnpinned(t *testing.T) {
	adapter, ok := New(Config{Home: t.TempDir()}).(*Adapter)
	if !ok {
		t.Fatal("New did not return the Claude adapter")
	}
	// The default is the bare name, resolved through the usual install
	// locations (harness.ResolveBinary) so a runner started by the Edge agent
	// finds a per-user install its PATH does not list. On a host without
	// Claude Code installed it stays the bare name.
	if filepath.Base(adapter.cfg.Binary) != "claude" {
		t.Errorf("binary = %q, want a path ending in claude", adapter.cfg.Binary)
	}
	// Unlike Codex there is no built-in pin: Claude Code self-updates fast
	// enough that a constant here would make every runner unready by default.
	if adapter.cfg.ExpectedVersion != "" {
		t.Errorf("expected version = %q, want unpinned", adapter.cfg.ExpectedVersion)
	}
}

func TestBoundedTextStaysValidUTF8(t *testing.T) {
	text := strings.Repeat("é", maxMessageText)
	got := boundedText(text)
	if len(got) > maxMessageText+32 {
		t.Fatalf("bounded text is %d bytes", len(got))
	}
	if !strings.HasSuffix(got, "…[truncated]") {
		t.Errorf("truncation was not marked: %q", got[len(got)-20:])
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("truncation split a rune")
		}
	}
}

func TestIsUUID(t *testing.T) {
	if !isUUID("11111111-2222-3333-4444-555555555555") {
		t.Error("a valid uuid was rejected")
	}
	for _, bad := range []string{"", "attempt-1", "1111111122223333444455555555555", "gggggggg-2222-3333-4444-555555555555"} {
		if isUUID(bad) {
			t.Errorf("%q was accepted as a uuid", bad)
		}
	}
}
