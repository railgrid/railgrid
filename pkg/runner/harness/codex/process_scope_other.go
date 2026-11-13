//go:build !linux

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

package codex

import (
	"context"
	"errors"
	"os/exec"
)

type userSystemdScope struct {
	unit    string
	started bool
}

func newUserSystemdScope([]string) (*userSystemdScope, error) {
	return nil, errors.New("systemd user scope is supported only on Linux")
}

func (*userSystemdScope) command(string, []string, []string) *exec.Cmd { return nil }

func (*userSystemdScope) stop() error { return nil }

func recoverUserSystemdScope(context.Context, string) (bool, error) {
	return false, errors.New("systemd scope recovery is supported only on Linux")
}
