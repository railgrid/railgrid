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
	"os"
	"path/filepath"
	"strings"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// credential is the single secret the child is given. It is deliberately a
// value type with no String method: anything that wants to print it has to go
// through redact, which is the whole point.
type credential struct {
	env   string
	value string
	// extra is what else the identity brought (harness.Credential.Environment):
	// exported beside the model credential and redacted exactly like it.
	extra []harness.EnvironmentVariable
}

// values lists every secret the credential carries, for redaction.
func (c credential) values() []string {
	out := make([]string, 0, 1+len(c.extra))
	if c.value != "" {
		out = append(out, c.value)
	}
	for _, variable := range c.extra {
		if variable.Value != "" {
			out = append(out, variable.Value)
		}
	}
	return out
}

// credentialFor resolves the credential the caller sent with this launch.
//
// It is per-launch by design: the credential is the CALLER's identity, not the
// machine's, so nothing on the host holds one and a turn with no credential
// runs as nobody rather than as whoever configured the runner. The adapter
// refuses a credential meant for another harness instead of trying it.
func credentialFor(launch harness.Launch) (credential, error) {
	cred := launch.Credential
	if cred.Empty() {
		return credential{}, errors.New("the launch carries no Claude Code credential")
	}
	var env string
	switch cred.Kind {
	case harness.CredentialClaudeOAuth:
		env = "CLAUDE_CODE_OAUTH_TOKEN"
	case harness.CredentialClaudeAPIKey:
		env = "ANTHROPIC_API_KEY"
	default:
		return credential{}, fmt.Errorf("credential kind %q is not a Claude Code credential", cred.Kind)
	}
	value := strings.TrimSpace(cred.Value)
	if value == "" {
		return credential{}, errors.New("the Claude Code credential is empty")
	}
	if len(value) > maxCredentialBytes {
		return credential{}, errors.New("the Claude Code credential is oversized")
	}
	// A credential with a newline or a NUL in it cannot be an environment
	// value, and trying anyway would truncate it into something that silently
	// authenticates as nobody.
	if strings.ContainsAny(value, "\r\n\x00") {
		return credential{}, errors.New("the Claude Code credential contains invalid whitespace")
	}
	// The runner already allow-listed the names; this is the same list again,
	// so an adapter reached by a runner that forgot still exports nothing but a
	// credential. Exact names, no prefixes: GH_HOST would send the token to
	// another host, and it must never pass because it starts with GH_.
	for _, variable := range cred.Environment {
		if _, ok := allowedBroughtEnvironment[variable.Name]; !ok {
			return credential{}, fmt.Errorf("the identity brings %s, which is not a credential this adapter exports", variable.Name)
		}
	}
	return credential{env: env, value: value, extra: append([]harness.EnvironmentVariable(nil), cred.Environment...)}, nil
}

// allowedBroughtEnvironment mirrors the runner's allow-list for the variables
// an identity may bring (see runner.allowedCredentialEnvironment).
var allowedBroughtEnvironment = map[string]struct{}{
	"GH_TOKEN":     {},
	"GITHUB_TOKEN": {},
}

// redact removes the credential value from any text that is about to leave the
// adapter. Every event message, blocker and error goes through it.
func redact(text string, cred credential) string {
	if text == "" {
		return text
	}
	for _, value := range cred.values() {
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	return text
}

// redactError wraps an error so its message cannot carry the credential.
func (c credential) redactError(err error) error {
	if err == nil {
		return err
	}
	msg := err.Error()
	for _, value := range c.values() {
		if strings.Contains(msg, value) {
			return errors.New(redact(msg, c))
		}
	}
	return err
}

// childEnv builds the environment for the Claude Code child.
//
// It is derived from the runner's own environment with an explicit deny list
// (blockedEnvKey) and then has every isolation variable set from scratch, which
// is the same shape as the Codex adapter's safeEnv. The credential is the ONE
// ANTHROPIC_*/CLAUDE_* variable that survives, and it comes from the launch,
// never from the parent, which is exactly why the deny list strips the whole
// prefix first. An empty credential (the version probe, which makes no model
// call) simply injects nothing.
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
	// What else the identity brought, after the deny list has run: a GH_TOKEN
	// the caller sent survives where one inherited from the runner's own
	// environment did not, because the first is the tenant's and the second is
	// the machine owner's.
	for _, variable := range cred.extra {
		env = append(env, variable.Name+"="+variable.Value)
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
