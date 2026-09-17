//go:build !windows

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

package proc

import (
	"errors"
	"os/exec"
	"syscall"
)

// Configure puts the harness child in its own process group so KillGroup can
// take out everything it spawned without signalling the runner itself.
func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// KillGroup signals the whole group led by cmd. A harness starts compilers,
// test runners and git; signalling only the direct child leaves those holding
// the task worktree.
func KillGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return errors.Join(err, cmd.Process.Kill())
	}
	return nil
}
