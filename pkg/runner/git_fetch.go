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

package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const maxGitFetchErrorBytes = 1024

const secureSSHCommand = "ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ForwardAgent=no -o ClearAllForwardings=yes"

type fetchRemoteKind string

const (
	fetchRemoteLocal fetchRemoteKind = "file"
	fetchRemoteHTTPS fetchRemoteKind = "https"
	fetchRemoteSSH   fetchRemoteKind = "ssh"
)

// validateFetchRemoteURL accepts only an operator-configured remote that Git
// can use without interpreting caller-controlled options. Local paths are
// useful for the enrolled-host acceptance fixture; network remotes are limited
// to public HTTPS and ordinary SSH URLs.
func validateFetchRemoteURL(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("fetch remote URL is empty")
	}
	if strings.TrimSpace(raw) != raw {
		return "", errors.New("fetch remote URL must not have surrounding whitespace")
	}
	if strings.HasPrefix(raw, "-") {
		return "", errors.New("fetch remote URL must not begin with an option")
	}
	if hasControlCharacter(raw) {
		return "", errors.New("fetch remote URL contains a control character")
	}

	if isWindowsAbsolutePath(raw) {
		if !filepath.IsAbs(raw) {
			return "", errors.New("windows fetch remote paths are only valid on Windows")
		}
		return filepath.Abs(raw)
	}
	if filepath.IsAbs(raw) {
		return filepath.Abs(raw)
	}
	if isSCPRemote(raw) {
		return raw, nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("fetch remote URL is malformed")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "":
		// Relative paths are resolved once at enrollment time. This keeps the
		// fetch target fixed if the runner later changes its working directory.
		return filepath.Abs(raw)
	case "file":
		return localPathFromFileURL(parsed)
	case "https":
		if err := validateNetworkRemote(parsed, false); err != nil {
			return "", err
		}
		return raw, nil
	case "ssh":
		if err := validateNetworkRemote(parsed, true); err != nil {
			return "", err
		}
		return raw, nil
	default:
		return "", errors.New("fetch remote URL scheme is not allowed")
	}
}

func localPathFromFileURL(parsed *url.URL) (string, error) {
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("file fetch remote URL has unsupported credentials or options")
	}
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		return "", errors.New("file fetch remote URL must not name a remote host")
	}
	if parsed.Path == "" || hasControlCharacter(parsed.Path) || hasControlCharacter(parsed.RawPath) {
		return "", errors.New("file fetch remote URL has an invalid path")
	}
	path := filepath.FromSlash(parsed.Path)
	if isWindowsAbsolutePath(path) && runtimeIsWindows() && !filepath.IsAbs(path) {
		return "", errors.New("file fetch remote URL has an invalid Windows path")
	}
	return filepath.Abs(path)
}

func validateNetworkRemote(parsed *url.URL, ssh bool) error {
	if parsed.Opaque != "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("fetch remote URL has unsupported options")
	}
	if parsed.User != nil {
		_, hasPassword := parsed.User.Password()
		if !ssh || hasPassword {
			return errors.New("fetch remote URL must not contain embedded credentials")
		}
		if hasControlCharacter(parsed.User.Username()) {
			return errors.New("fetch remote URL contains an invalid SSH user")
		}
	}
	host := parsed.Hostname()
	if host == "" || strings.HasPrefix(host, "-") || hasControlCharacter(host) {
		return errors.New("fetch remote URL has an invalid host")
	}
	if parsed.Path == "" || hasControlCharacter(parsed.Path) || hasControlCharacter(parsed.RawPath) {
		return errors.New("fetch remote URL has an invalid path")
	}
	return nil
}

func isSCPRemote(value string) bool {
	if strings.HasPrefix(strings.ToLower(value), "ext::") {
		return false
	}
	separator := strings.IndexByte(value, ':')
	if separator <= 0 || separator == len(value)-1 || strings.Contains(value[:separator], "/") {
		return false
	}
	if strings.HasPrefix(value[separator+1:], "//") {
		return false
	}
	if strings.ContainsAny(value, " \t\r\n?#") || hasControlCharacter(value) {
		return false
	}
	userHost := value[:separator]
	host := userHost
	if at := strings.LastIndexByte(userHost, '@'); at >= 0 {
		if at == 0 || at == len(userHost)-1 || strings.Contains(userHost[:at], ":") {
			return false
		}
		host = userHost[at+1:]
	}
	return validSSHHost(host)
}

func validSSHHost(host string) bool {
	return host != "" && !strings.HasPrefix(host, "-") && !hasControlCharacter(host) && !strings.ContainsAny(host, " \t\r\n/?#")
}

func hasControlCharacter(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func isWindowsAbsolutePath(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func runtimeIsWindows() bool { return filepath.Separator == '\\' }

func fetchRemoteKindOf(remote string) fetchRemoteKind {
	if isSCPRemote(remote) || strings.HasPrefix(strings.ToLower(remote), "ssh://") {
		return fetchRemoteSSH
	}
	if strings.HasPrefix(strings.ToLower(remote), "https://") {
		return fetchRemoteHTTPS
	}
	return fetchRemoteLocal
}

// fetchGitEnvironment is intentionally separate from sanitizedGitEnvironment.
// SSH_AUTH_SOCK is allowed only for the one explicitly configured SSH fetch;
// it remains absent from all source-only Git operations and the harness.
func fetchGitEnvironment(remote string) []string {
	env := make([]string, 0, len(os.Environ())+7)
	for _, value := range os.Environ() {
		key, _, ok := strings.Cut(value, "=")
		if !ok || strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "GH_") || strings.HasPrefix(key, "GITHUB_") || key == "SSH_AUTH_SOCK" || key == "SSH_AGENT_PID" || key == "SSH_ASKPASS" || key == "SSH_ASKPASS_REQUIRE" {
			continue
		}
		env = append(env, value)
	}
	allowProtocol := string(fetchRemoteKindOf(remote))
	if allowProtocol == "" {
		allowProtocol = string(fetchRemoteLocal)
	}
	env = append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL="+allowProtocol,
	)
	if fetchRemoteKindOf(remote) == fetchRemoteSSH {
		if socket, ok := os.LookupEnv("SSH_AUTH_SOCK"); ok && socket != "" {
			env = append(env, "SSH_AUTH_SOCK="+socket)
		}
	}
	return env
}

func fetchExactCommit(ctx context.Context, workdir, remote, commit string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	remote, err := validateFetchRemoteURL(remote)
	if err != nil {
		return fmt.Errorf("fetch remote URL is not allowed: %w", err)
	}
	if !commitPattern.MatchString(commit) {
		return errors.New("fetch commit is not a full 40-character commit")
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	args := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "credential.helper=",
		"-c", "fetch.recurseSubmodules=false",
	}
	if fetchRemoteKindOf(remote) == fetchRemoteSSH {
		args = append(args, "-c", "core.sshCommand="+secureSSHCommand)
	}
	args = append(args, "fetch", "--no-tags", "--no-prune", "--no-write-fetch-head", remote, strings.ToLower(commit))
	cmd := exec.CommandContext(cmdCtx, "git", args...)
	cmd.Dir = workdir
	cmd.Env = fetchGitEnvironment(remote)
	var stderr boundedGitBuffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if runErr := cmd.Run(); runErr != nil {
		return boundedGitFetchError(remote, stderr.String(), runErr, cmdCtx.Err())
	}
	return nil
}

func boundedGitFetchError(_ string, _ string, _ error, ctxErr error) error {
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return errors.New("git fetch timed out")
	case errors.Is(ctxErr, context.Canceled):
		return errors.New("git fetch canceled")
	default:
		return errors.New("git fetch failed")
	}
}

type boundedGitBuffer struct {
	value     strings.Builder
	truncated bool
}

func (b *boundedGitBuffer) Write(p []byte) (int, error) {
	remaining := maxGitFetchErrorBytes - b.value.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.value.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.value.Write(p)
	return len(p), nil
}

func (b *boundedGitBuffer) String() string {
	value := b.value.String()
	if b.truncated {
		return value + "..."
	}
	return value
}
