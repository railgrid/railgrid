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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// credential is the single secret the child is given. It is deliberately a
// value type with no String method: anything that wants to print it has to go
// through redact, which is the whole point.
type credential struct {
	env   string
	value string
}

// readCredential loads the credential from the runner-owned file. It is read
// on every Probe and Run rather than cached, so rotating the file is picked up
// by the next turn without restarting the runner.
//
// The file's mode is checked, not just its contents: a credential another local
// account can read is not a credential.
func (a *Adapter) readCredential() (credential, error) {
	kind := a.cfg.CredentialKind
	if !kind.Valid() {
		return credential{}, fmt.Errorf("credential kind must be one of %s", strings.Join(CredentialKinds, ", "))
	}
	path := strings.TrimSpace(a.cfg.CredentialFile)
	if path == "" {
		return credential{}, errors.New("credential file is required")
	}
	if !filepath.IsAbs(path) {
		return credential{}, fmt.Errorf("credential file must be absolute: %q", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return credential{}, fmt.Errorf("credential file %s does not exist", path)
		}
		return credential{}, fmt.Errorf("inspecting credential file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return credential{}, fmt.Errorf("credential file %s must be a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return credential{}, fmt.Errorf("credential file %s must not be readable by other accounts", path)
	}
	f, err := os.Open(path) //nolint:gosec // an operator-configured enrollment path
	if err != nil {
		return credential{}, fmt.Errorf("opening credential file: %w", err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	if err != nil {
		return credential{}, fmt.Errorf("reading credential file: %w", err)
	}
	if len(raw) > maxCredentialBytes {
		return credential{}, errors.New("credential file is oversized")
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return credential{}, errors.New("credential file is empty")
	}
	// A credential with a newline or a NUL in it cannot be an environment
	// value, and trying anyway would truncate it into something that silently
	// authenticates as nobody.
	if strings.ContainsAny(value, "\r\n\x00") {
		return credential{}, errors.New("credential value contains invalid whitespace")
	}
	return credential{env: kind.EnvVar(), value: value}, nil
}

// redact removes the credential value from any text that is about to leave the
// adapter. Every event message, blocker and error goes through it.
func redact(text string, cred credential) string {
	if text == "" || cred.value == "" {
		return text
	}
	return strings.ReplaceAll(text, cred.value, "[redacted]")
}

// redactError wraps an error so its message cannot carry the credential.
func (c credential) redactError(err error) error {
	if err == nil || c.value == "" {
		return err
	}
	if msg := err.Error(); strings.Contains(msg, c.value) {
		return errors.New(redact(msg, c))
	}
	return err
}

// childEnv builds the environment for the Claude Code child.
//
// It is derived from the runner's own environment with an explicit deny list
// (blockedEnvKey) and then has every isolation variable set from scratch, which
// is the same shape as the Codex adapter's safeEnv. The credential is the ONE
// ANTHROPIC_*/CLAUDE_* variable that survives, and it comes from the credential
// file — never from the parent, which is exactly why the deny list strips the
// whole prefix first. An empty credential (the version probe before one is
// configured) simply injects nothing.
func (a *Adapter) childEnv(cred credential) []string {
	home := strings.TrimSpace(a.cfg.Home)
	env := make([]string, 0, len(os.Environ())+16)
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if !ok || blockedEnvKey(strings.ToUpper(key)) {
			continue
		}
		env = append(env, item)
	}
	if home != "" {
		cacheHome := filepath.Join(home, "cache")
		env = append(env,
			"HOME="+home,
			// The config directory Claude Code reads and writes. Verified
			// present in the 2.1.273 binary.
			"CLAUDE_CONFIG_DIR="+home,
			"XDG_CONFIG_HOME="+home,
			"XDG_DATA_HOME="+home,
			"XDG_STATE_HOME="+home,
			"XDG_CACHE_HOME="+cacheHome,
			// Neutralize global and system git configuration exactly as the
			// Codex adapter does: a hostile ~/.gitconfig is a code-execution
			// vector through git's own hooks and aliases.
			"GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_CONFIG_SYSTEM="+os.DevNull,
			"GIT_CONFIG_NOSYSTEM=1",
		)
	}
	env = append(env,
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_TELEMETRY=1",
		"DISABLE_ERROR_REPORTING=1",
		"DISABLE_BUG_COMMAND=1",
		"DISABLE_COST_WARNINGS=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1",
	)
	if cred.env != "" && cred.value != "" {
		env = append(env, cred.env+"="+cred.value)
	}
	return env
}

// blockedEnvKey is the Codex adapter's deny list plus the whole
// ANTHROPIC_*/CLAUDE_* space. The prefix rule is not optional: an operator who
// happens to have ANTHROPIC_API_KEY exported would otherwise authenticate the
// tenant's turn with their own personal credential, and an inherited
// ANTHROPIC_BASE_URL would silently redirect it somewhere else entirely.
func blockedEnvKey(key string) bool {
	if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_") {
		return true
	}
	if key == "HOME" || strings.HasPrefix(key, "XDG_") {
		return true
	}
	if key == "CODEX_HOME" || key == "CODEX_API_KEY" {
		return true
	}
	if key == "GH_TOKEN" || strings.HasPrefix(key, "GH_") || key == "GITHUB_TOKEN" || strings.HasPrefix(key, "GITHUB_") {
		return true
	}
	if strings.HasPrefix(key, "GIT_CONFIG_") {
		return true
	}
	if key == "GIT_SSH" || key == "GIT_SSH_COMMAND" || key == "GIT_ASKPASS" || key == "SSH_AUTH_SOCK" || key == "SSH_AGENT_PID" {
		return true
	}
	if strings.HasPrefix(key, "DISABLE_") {
		return true
	}
	return key == "OPENAI_API_KEY" || key == "CHATGPT_API_KEY" || key == "AWS_SECRET_ACCESS_KEY" || key == "AWS_SESSION_TOKEN"
}
