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
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
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

type execProcessIdentity struct {
	pid       int
	startTime uint64
}

type execProcessStat struct {
	execProcessIdentity
	parentPID int
	state     byte
}

// cleanupExecProcesses proves that no process from this execution remains.
// New descendants of the executor are execution-owned even if they created a
// new session or changed their environment; the inherited session marker is a
// second ownership signal. Processes present in the pre-execution baseline are
// never signaled. This remains safe when the executor is PID 1 and a runtime
// exposes unrelated helper processes in the same namespace.
func cleanupExecProcesses(marker string, baseline map[execProcessIdentity]struct{}, bound time.Duration) error {
	deadline := time.Now().Add(bound)
	emptyScans := 0
	for {
		pids, err := execProcessIDs(marker, baseline)
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
			for _, process := range pids {
				if err := killExecProcess(process); err != nil {
					return fmt.Errorf("%w: kill residual pid %d: %v", errExecCleanupUnproven, process.pid, err)
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

func captureExecProcessBaseline() (map[execProcessIdentity]struct{}, error) {
	stats, err := readExecProcessStats()
	if err != nil {
		return nil, err
	}
	baseline := make(map[execProcessIdentity]struct{}, len(stats))
	for _, process := range stats {
		baseline[process.execProcessIdentity] = struct{}{}
	}
	return baseline, nil
}

func execProcessIDs(marker string, baseline map[execProcessIdentity]struct{}) ([]execProcessIdentity, error) {
	stats, err := readExecProcessStats()
	if err != nil {
		return nil, err
	}
	return selectExecProcessIDs(os.Getpid(), marker, baseline, stats, execProcessHasMarker)
}

func selectExecProcessIDs(self int, marker string, baseline map[execProcessIdentity]struct{}, stats map[int]execProcessStat, hasMarker func(int, string) (bool, error)) ([]execProcessIdentity, error) {
	var pids []execProcessIdentity
	for pid, process := range stats {
		if pid <= 1 || pid == self {
			continue
		}
		// A zombie has already terminated and cannot escape the execution
		// boundary. reapExitedChildren below will collect it when it is ours.
		// Its /proc/environ is intentionally unavailable, so do not turn an
		// ordinary exited child into an unproven-cleanup failure.
		if process.state == 'Z' {
			continue
		}
		if _, existed := baseline[process.execProcessIdentity]; existed {
			continue
		}
		if execProcessDescendsFrom(pid, self, stats) {
			pids = append(pids, process.execProcessIdentity)
			continue
		}
		marked, err := hasMarker(pid, marker)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			// Processes outside this worker's process domain can be visible in
			// /proc (notably in host-side tests). The ancestry check above is
			// authoritative for executor-owned processes, including descendants
			// that clear the marker. An unreadable non-descendant is outside this
			// process domain and cannot be selected for cleanup.
			if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
				continue
			}
			return nil, err
		}
		if marked {
			pids = append(pids, process.execProcessIdentity)
		}
	}
	return pids, nil
}

func readExecProcessStats() (map[int]execProcessStat, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	stats := make(map[int]execProcessStat, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		process, err := readExecProcessStat(pid)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				continue
			}
			return nil, err
		}
		stats[pid] = process
	}
	return stats, nil
}

func execProcessDescendsFrom(pid, ancestor int, stats map[int]execProcessStat) bool {
	seen := make(map[int]struct{})
	for pid > 0 {
		if pid == ancestor {
			return true
		}
		if _, repeated := seen[pid]; repeated {
			return false
		}
		seen[pid] = struct{}{}
		process, ok := stats[pid]
		if !ok || process.parentPID == pid {
			return false
		}
		pid = process.parentPID
	}
	return false
}

// killExecProcess opens a pidfd, then revalidates the process start time before
// signaling it. This prevents a PID reused between the /proc scan and signal
// from receiving the execution's cleanup signal.
func killExecProcess(process execProcessIdentity) error {
	pidfd, err := unix.PidfdOpen(process.pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open pidfd: %w", err)
	}
	defer func() { _ = unix.Close(pidfd) }()

	stat, err := readExecProcessStat(process.pid)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("revalidate start time: %w", err)
	}
	if stat.startTime != process.startTime {
		return nil
	}
	if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// killExecProcessGroup first signals the command leader through its pidfd,
// then uses the leader's process group to stop ordinary descendants in one
// operation. The group signal is issued only after pidfd signalling proves the
// captured leader is still the live process, so a reused leader PID cannot
// redirect the group kill to an unrelated process group. Descendants that call
// setsid are handled by the marker/pidfd scan in cleanupExecProcesses.
func killExecProcessGroup(pid int) error {
	pidfd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open command pidfd: %w", err)
	}
	defer func() { _ = unix.Close(pidfd) }()
	// Stop the leader before addressing the numeric process group. A stopped
	// leader cannot exit and be reaped between this identity-safe pidfd lookup
	// and the group signal, so the PGID cannot be recycled to an unrelated
	// process during the kill.
	if err := unix.PidfdSendSignal(pidfd, unix.SIGSTOP, nil, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("stop command leader: %w", err)
	}
	// Let already-started descendants drain a bounded amount of output before
	// the group kill. The leader remains stopped, so the process-group identity
	// stays pinned while this brief grace period preserves timeout diagnostics.
	time.Sleep(time.Millisecond)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal command process group: %w", err)
	}
	if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill command leader: %w", err)
	}
	return nil
}

func execProcessHasMarker(pid int, marker string) (bool, error) {
	environ, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return false, err
	}
	want := []byte("FAROS_EXEC_SESSION=" + marker)
	for _, field := range bytes.Split(environ, []byte{0}) {
		if bytes.Equal(field, want) {
			return true, nil
		}
	}
	return false, nil
}

func readExecProcessStat(pid int) (execProcessStat, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return execProcessStat{}, err
	}
	// The command name is parenthesized and may itself contain spaces or ')'.
	// starttime is field 22, which is index 19 after the final ')'.
	closing := bytes.LastIndexByte(raw, ')')
	if closing < 0 {
		return execProcessStat{}, errors.New("process stat is missing command terminator")
	}
	fields := strings.Fields(string(raw[closing+1:]))
	if len(fields) <= 19 {
		return execProcessStat{}, errors.New("process stat is missing start time")
	}
	parentPID, err := strconv.Atoi(fields[1])
	if err != nil {
		return execProcessStat{}, fmt.Errorf("parse process parent pid: %w", err)
	}
	if len(fields[0]) != 1 {
		return execProcessStat{}, errors.New("process stat is missing state")
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return execProcessStat{}, fmt.Errorf("parse process start time: %w", err)
	}
	return execProcessStat{
		execProcessIdentity: execProcessIdentity{pid: pid, startTime: startTime},
		parentPID:           parentPID,
		state:               fields[0][0],
	}, nil
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
