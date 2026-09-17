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
	"fmt"
	"os/exec"
	"syscall"
)

// configureChild puts the child in its own process group and, when the agent
// runs as root, drops it to the add-on account.
//
// Setpgid is unconditional. Without it the child shares the agent's group, and
// the group kill in stopChild would signal the agent itself.
func configureChild(cmd *exec.Cmd, uid, gid int) error {
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
	}
	attr.Setpgid = true
	if uid >= 0 && gid >= 0 {
		if uid == 0 || gid == 0 {
			return fmt.Errorf("refusing to run a child as uid %d gid %d", uid, gid)
		}
		attr.Credential = &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
			// The add-on account's supplementary groups are deliberately not
			// inherited from the agent (root's), and are not looked up: the
			// child gets exactly its own primary group.
			NoSetGroups: false,
			Groups:      []uint32{uint32(gid)},
		}
	} else if uid >= 0 || gid >= 0 {
		return fmt.Errorf("uid and gid must both be set or both be inherited")
	}
	cmd.SysProcAttr = attr
	return nil
}

// signalProcessGroup sends sig to the whole group led by pid. Setpgid above
// makes the child its own group leader, so the group id equals its pid.
func signalProcessGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	return syscall.Kill(-pid, sig)
}
