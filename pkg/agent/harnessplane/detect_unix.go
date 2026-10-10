//go:build unix

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

package harnessplane

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// accessibleBy reports whether an account with exactly uid and primary gid can
// execute path: every directory on the way must grant it search (x) and the
// file itself execute (x). The agent runs as root and cannot simply try — root
// passes every check — so this evaluates the classic owner/group/other bits
// against the ids the supervisor hands the child, which gets that one group and
// no supplementary ones (see supervisor.configureChild).
//
// Both the path as given and its symlink-resolved form are walked: the kernel
// needs search permission along the spelling the runner uses AND along the
// target a symlink points at. A per-user Claude Code install is exactly that —
// ~/.local/bin/claude -> ~/.local/share/claude/versions/<v>.
func accessibleBy(path string, uid, gid int) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if !walkPermits(abs, uid, gid) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false
	}
	return resolved == abs || walkPermits(resolved, uid, gid)
}

// walkPermits checks the search bit on every directory from the root down and
// the execute bit on the final component.
func walkPermits(abs string, uid, gid int) bool {
	parts := strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator))
	current := string(filepath.Separator)
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Stat(current)
		if err != nil {
			return false
		}
		if !permits(info, uid, gid, 0o1) {
			return false
		}
	}
	return true
}

// permits applies one permission bit (as the "other" bit, e.g. 0o1 for
// execute/search) to info for the given ids.
func permits(info os.FileInfo, uid, gid int, bit os.FileMode) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// No ownership to compare against; the mode is all there is.
		return info.Mode().Perm()&bit != 0
	}
	mode := info.Mode().Perm()
	switch {
	case int(st.Uid) == uid:
		return mode&(bit<<6) != 0
	case int(st.Gid) == gid:
		return mode&(bit<<3) != 0
	default:
		return mode&bit != 0
	}
}
