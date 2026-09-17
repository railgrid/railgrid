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

// Package safeio writes files and directories into a location that a less
// privileged account can influence, without letting that account redirect the
// write. Every helper refuses a symlink anywhere along the path before it
// creates or changes anything: a worker-owned `~/.railgrid -> /etc` would
// otherwise turn a root-side install into an arbitrary-file overwrite.
//
// These started life in pkg/cli/cmd/launchd.go for the macOS LaunchDaemon
// installer. The edge add-on manager needs exactly the same guarantees when it
// prepares an add-on state directory under another account's home, so they live
// here rather than being duplicated.
package safeio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RejectSymlink fails when path itself is a symlink. A missing path is fine —
// the caller is about to create it.
func RejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to overwrite symlink at %s", path)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	return nil
}

// RejectSymlinkPath checks every existing component of a directory path before
// anything is created or changed below it. Components that do not exist yet are
// fine; an existing component that is a symlink, or is not a directory, is not.
func RejectSymlinkPath(path string) error {
	path = filepath.Clean(path)
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to use symlink directory %s", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("path component %s is not a directory", current)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking path component %s: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

// EnsureDir creates path with mode, then enforces mode and ownership on it.
// uid/gid of 0 means "leave ownership alone" so a non-root caller preparing its
// own directories does not have to special-case the chown.
func EnsureDir(path string, mode os.FileMode, uid, gid int) error {
	if err := RejectSymlinkPath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if uid != 0 || gid != 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

// WriteFile replaces path atomically: it writes a temporary file in the same
// directory with the requested mode and ownership, then renames it over the
// destination. The permissions and owner are set BEFORE the content is written,
// so the data is never briefly readable by the wrong account. uid/gid of 0
// leaves ownership to the calling process.
func WriteFile(path string, data []byte, mode os.FileMode, uid, gid int) error {
	if err := RejectSymlink(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := RejectSymlinkPath(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".railgrid-safeio-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if uid != 0 || gid != 0 {
		if err := tmp.Chown(uid, gid); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
