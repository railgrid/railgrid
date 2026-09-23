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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WellKnownBinDirs are the directories a coding agent is commonly installed
// into, searched in this order when PATH does not already resolve it.
//
// A managed runner is started by the Edge agent under a service account whose
// PATH is whatever the init system gave it — usually /usr/bin:/bin, never the
// interactive shell's. An operator who installed Claude Code or Codex the
// normal way (a per-user install under ~/.local/bin, Homebrew on macOS, a
// global npm prefix) then sees only `exec: "claude": executable file not found
// in $PATH`, with nothing on the machine actually missing. Looking in these
// places costs one stat each and turns that into a working runner.
//
// Entries beginning with "~/" are expanded against the RUNNER's home, which is
// the account the harness process itself runs as.
var WellKnownBinDirs = []string{
	"~/.local/bin",
	"~/bin",
	"/usr/local/bin",
	"/opt/homebrew/bin",
	"/opt/local/bin",
	"/usr/bin",
	"/bin",
	"/snap/bin",
	"~/.npm-global/bin",
	"~/.bun/bin",
	"~/.volta/bin",
	"~/.asdf/shims",
	"~/.nvm/current/bin",
	"~/node_modules/.bin",
}

// ResolveBinary returns the path the harness should execute for name.
//
// It prefers what the environment already resolves, so an operator who set an
// explicit path or a working PATH keeps exactly what they configured. Only
// when that fails does it search WellKnownBinDirs. A name that is already a
// path is returned untouched, and a name found nowhere is returned untouched
// too: the caller's own "not found in $PATH" error is the clearest thing a
// reader can act on, and inventing a path would only move the failure.
func ResolveBinary(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || strings.ContainsRune(trimmed, os.PathSeparator) {
		return name
	}
	if resolved, err := exec.LookPath(trimmed); err == nil {
		return resolved
	}
	home, _ := os.UserHomeDir()
	for _, dir := range WellKnownBinDirs {
		expanded := dir
		if after, found := strings.CutPrefix(dir, "~/"); found {
			if home == "" {
				continue
			}
			expanded = filepath.Join(home, after)
		}
		candidate := filepath.Join(expanded, trimmed)
		if isExecutableFile(candidate) {
			return candidate
		}
	}
	return name
}

// isExecutableFile reports whether path is a regular file this process may
// execute. A directory or a mode without any execute bit is not a candidate.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
