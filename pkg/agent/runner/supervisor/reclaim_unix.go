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

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ReclaimStaleHolder terminates the process that holds lockPath when that lock
// belongs to a child of an agent that no longer exists.
//
// The lock is a flock whose content is the holder's pid, written by the child
// that acquired it. A child outlives its agent when the agent is killed rather
// than stopped (macOS has no parent-death signal), and it then keeps the lock
// and the listening port: the new agent's own child can never start, while
// the orphan keeps answering the port with a stale build and configuration.
// This is only called before the first child of an agent process is started,
// so anything holding the lock at that point is such an orphan; a lock taken
// while the agent runs is never touched here.
//
// It returns the pid it terminated, or 0 when the lock was free.
func ReclaimStaleHolder(lockPath string, grace time.Duration) (int, error) {
	f, err := os.OpenFile(lockPath, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", lockPath, err)
	}
	defer f.Close() //nolint:errcheck
	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB); err == nil {
		// A shared lock succeeded, so no runner holds the exclusive one.
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		return 0, nil
	}
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", lockPath, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return 0, fmt.Errorf("%s is held but records no holder pid", lockPath)
	}
	if pid == os.Getpid() || !processAlive(pid) {
		return 0, nil
	}
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	// The orphan is its own group leader (configureChild), and its harness and
	// git children sit in that group.
	if err := signalProcessGroup(pid, syscall.SIGTERM); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	if waitForExit(pid, grace) {
		return pid, nil
	}
	if err := signalProcessGroup(pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	if !waitForExit(pid, 5*time.Second) {
		return pid, fmt.Errorf("pid %d holding %s did not exit", pid, lockPath)
	}
	return pid, nil
}

func waitForExit(pid int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !processAlive(pid)
}
