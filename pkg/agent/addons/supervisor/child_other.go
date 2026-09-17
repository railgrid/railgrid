//go:build !unix

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
	"os/exec"
	"syscall"
)

// errUnsupported keeps `go build ./...` green for the platforms the CLI is
// released for (the kubectl plugin ships a Windows build) without pretending
// the guarantees hold there. Edge add-ons are a Linux/macOS feature: the agent
// refuses to allow one on any other platform before it ever reaches here.
var errUnsupported = errors.New("edge add-ons are only supported on Linux and macOS hosts")

func configureChild(_ *exec.Cmd, uid, gid int) error {
	if uid >= 0 || gid >= 0 {
		return fmt.Errorf("dropping privileges for an add-on child: %w", errUnsupported)
	}
	return errUnsupported
}

func signalProcessGroup(_ int, _ syscall.Signal) error {
	return errUnsupported
}
