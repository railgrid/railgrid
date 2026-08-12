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
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const prSetChildSubreaper = 36

const execCleanupStableEmptyScans = 3

var errExecCleanupUnproven = errors.New("exec process cleanup could not be proven")

func enableChildSubreaper() error {
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// cleanupExecProcesses proves that no process from this execution remains.
// In the production worker PID namespace the worker is PID 1 and therefore
// kills every other PID. Unit-level callers use an inherited per-session
// marker so setsid and reparented descendants are still found without touching
// unrelated host processes.
func cleanupExecProcesses(marker string, bound time.Duration) error {
	deadline := time.Now().Add(bound)
	emptyScans := 0
	for {
		pids, err := execProcessIDs(marker)
		if err != nil {
			return fmt.Errorf("%w: inspect process namespace: %v", errExecCleanupUnproven, err)
		}
		if len(pids) == 0 {
			emptyScans++
			if emptyScans >= execCleanupStableEmptyScans {
				return nil
			}
		} else {
			emptyScans = 0
			for _, pid := range pids {
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					return fmt.Errorf("%w: kill residual pid %d: %v", errExecCleanupUnproven, pid, err)
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w within %s (remaining pids %v, stable empty scans %d/%d)", errExecCleanupUnproven, bound, pids, emptyScans, execCleanupStableEmptyScans)
		}
		reapExitedChildren()
		time.Sleep(10 * time.Millisecond)
	}
}

func execProcessIDs(marker string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	pidOne := self == 1
	want := []byte("FAROS_EXEC_SESSION=" + marker)
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		if pidOne {
			pids = append(pids, pid)
			continue
		}
		environ, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				continue
			}
			// Outside the worker's PID namespace tests can observe unrelated
			// host processes owned by another user. They cannot carry our marker.
			if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
				continue
			}
			return nil, err
		}
		for _, field := range bytes.Split(environ, []byte{0}) {
			if bytes.Equal(field, want) {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids, nil
}

func reapExitedChildren() {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}
