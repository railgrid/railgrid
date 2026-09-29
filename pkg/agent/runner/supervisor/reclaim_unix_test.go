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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A held lock whose recorded holder is a process this agent does not
// supervise is an orphan of a previous agent: it is terminated. A free lock,
// a missing lock file and a dead holder are left alone.
func TestReclaimStaleHolderTerminatesAnOrphanedRunner(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "runner.lock")

	if pid, err := ReclaimStaleHolder(lockPath, time.Second); pid != 0 || err != nil {
		t.Fatalf("missing lock file: pid=%d err=%v", pid, err)
	}

	// The orphan: its own process group, like a supervised child, holding
	// the lock. The test process takes the flock on the orphan's behalf and
	// records the orphan's pid, which is what the runner does for itself.
	orphan := exec.Command("sleep", "60")
	orphan.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	// A real orphan is reaped by init the moment it exits. Here the test
	// process is its parent, so it must reap too: an unreaped child lingers as
	// a zombie that still answers signal 0 and would look alive forever.
	exited := make(chan *os.ProcessState, 1)
	go func() { state, _ := orphan.Process.Wait(); exited <- state }()
	t.Cleanup(func() { _ = orphan.Process.Kill() })
	held, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close() //nolint:errcheck
	if _, err := held.WriteString(strconv.Itoa(orphan.Process.Pid) + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(held.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	pid, err := ReclaimStaleHolder(lockPath, 2*time.Second)
	if err != nil || pid != orphan.Process.Pid {
		t.Fatalf("reclaim = pid %d, %v; want the orphan %d", pid, err, orphan.Process.Pid)
	}
	select {
	case state := <-exited:
		if state == nil || state.Success() {
			t.Fatalf("orphan was not terminated: %v", state)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("orphan did not exit")
	}

	// Its pid is still recorded, but it no longer exists: nothing to do.
	if pid, err := ReclaimStaleHolder(lockPath, time.Second); pid != 0 || err != nil {
		t.Fatalf("dead holder: pid=%d err=%v", pid, err)
	}

	// A held lock that names nobody cannot be reclaimed safely, and says so.
	if err := held.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := ReclaimStaleHolder(lockPath, time.Second); err == nil {
		t.Fatal("a held lock without a holder pid must be reported, not guessed at")
	}

	// A free lock is left alone even when it records a live pid.
	if err := unix.Flock(int(held.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if _, err := held.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		t.Fatal(err)
	}
	if pid, err := ReclaimStaleHolder(lockPath, time.Second); pid != 0 || err != nil {
		t.Fatalf("free lock: pid=%d err=%v", pid, err)
	}
}
