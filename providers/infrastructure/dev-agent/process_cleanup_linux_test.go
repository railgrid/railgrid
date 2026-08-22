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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestCleanupExecProcessesTargetsOnlyPostBaselineExecutionProcesses(t *testing.T) {
	marker := fmt.Sprintf("test-%d", time.Now().UnixNano())
	unmarked := exec.Command("/bin/sleep", "30")
	unmarked.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := unmarked.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = unmarked.Process.Kill()
		_ = unmarked.Wait()
	}()

	baseline, err := captureExecProcessBaseline()
	if err != nil {
		t.Fatal(err)
	}

	marked := exec.Command("/bin/sleep", "30")
	marked.Env = append(os.Environ(), "FAROS_EXEC_SESSION="+marker)
	marked.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := marked.Start(); err != nil {
		t.Fatal(err)
	}
	markedDone := make(chan error, 1)
	go func() { markedDone <- marked.Wait() }()
	unmarkedDescendant := exec.Command("/bin/sleep", "30")
	unmarkedDescendant.Env = append(os.Environ(), "FAROS_EXEC_SESSION=")
	unmarkedDescendant.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := unmarkedDescendant.Start(); err != nil {
		t.Fatal(err)
	}
	unmarkedDescendantDone := make(chan error, 1)
	go func() { unmarkedDescendantDone <- unmarkedDescendant.Wait() }()

	if err := cleanupExecProcesses(marker, baseline, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-markedDone:
	case <-time.After(time.Second):
		t.Fatal("marked process was not reaped")
	}
	select {
	case <-unmarkedDescendantDone:
	case <-time.After(time.Second):
		t.Fatal("post-baseline descendant without a marker was not reaped")
	}
	if err := syscall.Kill(marked.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("marked process remains after cleanup: %v", err)
	}
	if err := syscall.Kill(unmarked.Process.Pid, 0); err != nil {
		t.Fatalf("pre-baseline process was touched: %v", err)
	}
}

func TestSelectExecProcessIDsIgnoresForeignUnreadableProcessesWhenExecutorIsPID1(t *testing.T) {
	stats := map[int]execProcessStat{
		1: {execProcessIdentity: execProcessIdentity{pid: 1, startTime: 1}, parentPID: 0},
		2: {execProcessIdentity: execProcessIdentity{pid: 2, startTime: 2}, parentPID: 1},
		3: {execProcessIdentity: execProcessIdentity{pid: 3, startTime: 3}, parentPID: 99},
		4: {execProcessIdentity: execProcessIdentity{pid: 4, startTime: 4}, parentPID: 99},
	}
	baseline := map[execProcessIdentity]struct{}{}
	hasMarker := func(pid int, _ string) (bool, error) {
		switch pid {
		case 2:
			return true, nil
		case 3:
			return false, os.ErrPermission
		default:
			return false, nil
		}
	}
	got, err := selectExecProcessIDs(1, "marker", baseline, stats, hasMarker)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].pid != 2 {
		t.Fatalf("selected processes = %+v, want only marked pid 2", got)
	}
}

func TestSelectExecProcessIDsRetainsUnreadableExecutorDescendantByAncestry(t *testing.T) {
	stats := map[int]execProcessStat{
		1: {execProcessIdentity: execProcessIdentity{pid: 1, startTime: 1}, parentPID: 0},
		2: {execProcessIdentity: execProcessIdentity{pid: 2, startTime: 2}, parentPID: 1},
	}
	hasMarker := func(int, string) (bool, error) { return false, os.ErrPermission }
	got, err := selectExecProcessIDs(1, "marker", nil, stats, hasMarker)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].pid != 2 {
		t.Fatalf("selected processes = %+v, want unreadable descendant pid 2", got)
	}
}
