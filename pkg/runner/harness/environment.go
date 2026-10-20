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

package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// EnvironmentConfig is local operator policy, never part of a task request.
// Values are literal: no shell evaluation, tilde expansion, or env-file loading.
type EnvironmentConfig struct {
	PathPrepend []string          `json:"pathPrepend,omitempty"`
	Variables   map[string]string `json:"variables,omitempty"`
}

// LoadEnvironmentConfig reads a bounded, explicit operator file. Only an absent
// default file is optional; a misspelled explicit path is an error.
func LoadEnvironmentConfig(path string, optional bool) (EnvironmentConfig, error) {
	var document struct {
		Environment EnvironmentConfig `json:"environment"`
	}
	info, err := os.Lstat(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return document.Environment, nil
	}
	if err != nil {
		return document.Environment, fmt.Errorf("open runner environment config: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !TrustedFileOwner(info) {
		return document.Environment, errors.New("runner environment config must be a trusted-owner regular file not writable by group or others")
	}
	f, err := os.Open(path)
	if err != nil {
		return document.Environment, fmt.Errorf("open runner environment config: %w", err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0022 != 0 || !TrustedFileOwner(opened) {
		return document.Environment, errors.New("runner environment config changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return document.Environment, errors.New("runner environment config unreadable or larger than 64 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return EnvironmentConfig{}, errors.New("invalid runner environment config JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return EnvironmentConfig{}, errors.New("runner environment config must contain one JSON object")
	}
	if err := document.Environment.Validate(); err != nil {
		return EnvironmentConfig{}, err
	}
	return document.Environment, nil
}

func (e EnvironmentConfig) Validate() error {
	return e.ValidateForWorktreeRoot("")
}

// ValidateForWorktreeRoot also excludes tools controlled by managed tasks.
func (e EnvironmentConfig) ValidateForWorktreeRoot(worktreeRoot string) error {
	for _, path := range e.PathPrepend {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, string(os.PathListSeparator)+"\x00\r\n") {
			return errors.New("runner environment pathPrepend requires clean absolute paths")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 || !TrustedFileOwner(info) {
			return fmt.Errorf("runner environment pathPrepend %q must be a trusted-owner real directory not writable by group or others", path)
		}
		if worktreeRoot != "" {
			realPath, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("resolve runner tool directory: %w", err)
			}
			root, err := filepath.Abs(worktreeRoot)
			if err != nil {
				return err
			}
			if resolved, err := filepath.EvalSymlinks(root); err == nil {
				root = resolved
			}
			rel, err := filepath.Rel(root, realPath)
			if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
				return errors.New("runner environment pathPrepend must be outside the managed worktree root")
			}
		}
	}
	for name, value := range e.Variables {
		if !environmentNamePattern.MatchString(name) || strings.ContainsRune(value, 0) {
			return errors.New("runner environment contains an invalid variable")
		}
		if reservedEnvironmentKey(strings.ToUpper(name)) {
			return fmt.Errorf("runner environment variable %s is reserved; use pathPrepend for PATH", name)
		}
	}
	return nil
}

func reservedEnvironmentKey(key string) bool {
	switch key {
	case "PATH", "HOME", "GOPATH", "GOBIN", "GOCACHE", "GOMODCACHE", "GOTMPDIR", "TMPDIR", "TMP", "TEMP", "NPM_CONFIG_CACHE", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GOENV", "GOFLAGS", "GOROOT", "GOTOOLCHAIN", "NODE_OPTIONS", "NODE_PATH", "BASH_ENV", "ENV", "PROMPT_COMMAND", "PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME", "PERL5OPT", "RUBYOPT", "SHELLOPTS", "GOWORK", "GOEXPERIMENT", "GODEBUG", "CC", "CXX", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "ALL_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY":
		return true
	}
	for _, prefix := range []string{"NPM_CONFIG_", "CODEX_", "CLAUDE_", "ANTHROPIC_", "OPENAI_", "XDG_", "GIT_", "GH_", "GITHUB_", "RAILGRID_", "LD_", "DYLD_", "DISABLE_", "BASH_FUNC_", "CGO_", "JAVA_", "_JAVA_"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return strings.Contains(key, "TOKEN") || strings.Contains(key, "SECRET") || strings.Contains(key, "PASSWORD") || strings.HasSuffix(key, "API_KEY")
}

// Apply preserves the adapter's sanitized environment, overrides explicitly
// configured variables, and prepends tool paths without changing process env.
func (e EnvironmentConfig) Apply(base []string) []string {
	values := make(map[string]string, len(base)+len(e.Variables))
	for _, item := range base {
		if k, v, ok := strings.Cut(item, "="); ok {
			values[k] = v
		}
	}
	for k, v := range e.Variables {
		values[k] = v
	}
	if len(e.PathPrepend) > 0 {
		parts := append([]string(nil), e.PathPrepend...)
		if values["PATH"] != "" {
			parts = append(parts, values["PATH"])
		}
		values["PATH"] = strings.Join(parts, string(os.PathListSeparator))
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(values))
	for _, k := range keys {
		result = append(result, k+"="+values[k])
	}
	return result
}
