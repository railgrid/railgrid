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

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	platformMetadataDir = ".kedge-platform"
	workspaceLockName   = "workspace.lock"
	platformStateGID    = 2000
	workspaceProcessUID = 1000
	workspaceProcessGID = 1000
)

func preparePlatformState(workspace string) error {
	dir := filepath.Join(workspace, platformMetadataDir)
	for _, path := range []string{dir, filepath.Join(dir, "exec-sessions")} {
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(path, 0o770); err != nil {
				return err
			}
		case err != nil:
			return err
		case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("protected platform state path %s is not a directory", path)
		default:
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != 0 || stat.Gid != platformStateGID {
				return fmt.Errorf("protected platform state path %s has untrusted ownership", path)
			}
		}
		if err := os.Chown(path, 0, platformStateGID); err != nil {
			return fmt.Errorf("chown %s: %w", path, err)
		}
		if err := os.Chmod(path, 0o770); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}
	lockPath := filepath.Join(dir, workspaceLockName)
	if info, err := os.Lstat(lockPath); err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != 0 || stat.Gid != platformStateGID {
			return fmt.Errorf("protected workspace lock has untrusted type or ownership")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o660)
	if err != nil {
		return err
	}
	if err := lock.Close(); err != nil {
		return err
	}
	if err := os.Chown(lockPath, 0, platformStateGID); err != nil {
		return err
	}
	return os.Chmod(lockPath, 0o660)
}

func platformChildProcAttr(setProcessGroup bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: setProcessGroup,
		Credential: &syscall.Credential{
			Uid: workspaceProcessUID, Gid: workspaceProcessGID,
			Groups: []uint32{workspaceProcessGID},
		},
	}
}

// workspaceMutationLock is stored on the component PVC, so the dev-agent and
// exec-worker serialize mutations even though they run in different containers.
type workspaceMutationLock struct {
	file *os.File
}

func validateProtectedPlatformState(workspace string) error {
	for _, path := range []string{
		filepath.Join(workspace, platformMetadataDir),
		filepath.Join(workspace, platformMetadataDir, "exec-sessions"),
	} {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect protected platform state %s: %w", path, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != 0 || stat.Gid != platformStateGID {
			return fmt.Errorf("protected platform state path %s has untrusted type or ownership", path)
		}
	}
	lockPath := filepath.Join(workspace, platformMetadataDir, workspaceLockName)
	info, err := os.Lstat(lockPath)
	if err != nil {
		return fmt.Errorf("inspect protected workspace lock: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != 0 || stat.Gid != platformStateGID {
		return fmt.Errorf("protected workspace lock has untrusted type or ownership")
	}
	return nil
}

func acquireWorkspaceMutationLock(ctx context.Context, workspace string, requireProtected bool) (*workspaceMutationLock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dir := filepath.Join(workspace, platformMetadataDir)
	flags := os.O_RDWR | syscall.O_NOFOLLOW
	if requireProtected {
		if err := validateProtectedPlatformState(workspace); err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(filepath.Join(dir, "exec-sessions"), 0o700); err != nil {
			return nil, fmt.Errorf("create platform metadata directory: %w", err)
		}
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(filepath.Join(dir, workspaceLockName), flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open workspace mutation lock: %w", err)
	}
	if requireProtected {
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("inspect opened workspace lock: %w", statErr)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || stat.Uid != 0 || stat.Gid != platformStateGID {
			_ = file.Close()
			return nil, fmt.Errorf("opened workspace lock has untrusted type or ownership")
		}
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &workspaceMutationLock{file: file}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock workspace mutations: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (l *workspaceMutationLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
