//go:build linux

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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

const systemdScopeStopTimeout = 8 * time.Second

type userSystemdScope struct {
	unit        string
	systemdRun  string
	systemctl   string
	envPath     string
	controlEnv  []string
	runtimePath string
	started     bool
}

func newUserSystemdScope(baseEnv []string) (*userSystemdScope, error) {
	runtimePath := filepath.Join("/run/user", fmt.Sprint(os.Geteuid()))
	info, err := os.Lstat(runtimePath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("systemd user scope requires an owned private XDG runtime directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("systemd user scope runtime directory is not owned by the runner")
	}
	runPath, err := exec.LookPath("systemd-run")
	if err != nil {
		return nil, errors.New("systemd user scope requires systemd-run on PATH")
	}
	systemctlPath, err := exec.LookPath("systemctl")
	if err != nil {
		return nil, errors.New("systemd user scope requires systemctl on PATH")
	}
	envPath, err := exec.LookPath("env")
	if err != nil {
		return nil, errors.New("systemd user scope requires env on PATH")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, errors.New("generating systemd user scope identity failed")
	}
	unit := "railgrid-codex-" + hex.EncodeToString(id[:]) + ".scope"
	path := envValue(baseEnv, "PATH")
	if path == "" {
		path = os.Getenv("PATH")
	}
	return &userSystemdScope{
		unit:       unit,
		systemdRun: runPath,
		systemctl:  systemctlPath,
		controlEnv: []string{
			"PATH=" + path,
			"XDG_RUNTIME_DIR=" + runtimePath,
			"LC_ALL=C",
		},
		runtimePath: runtimePath,
		envPath:     envPath,
	}, nil
}

func (s *userSystemdScope) command(binary string, args []string, env []string) *exec.Cmd {
	unitArgs := []string{
		"--user", "--scope", "--quiet", "--collect",
		"--unit", s.unit,
		"--property=KillMode=control-group",
		"--property=TimeoutStopSec=2s",
		"--",
		s.envPath, "-u", "XDG_RUNTIME_DIR", "-u", "DBUS_SESSION_BUS_ADDRESS", "--",
	}
	unitArgs = append(unitArgs, binary)
	unitArgs = append(unitArgs, args...)
	cmd := exec.Command(s.systemdRun, unitArgs...)
	cmd.Env = append(withoutSystemdSessionEnv(env), "XDG_RUNTIME_DIR="+s.runtimePath)
	return cmd
}

func (s *userSystemdScope) stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), systemdScopeStopTimeout)
	defer cancel()
	state, err := s.loadState(ctx)
	if err != nil {
		if !s.started {
			// The app-server did not complete its handshake, so no user command
			// could have started. A failed scope setup is a launch error, not an
			// unconfirmed teardown of an executing turn.
			return nil
		}
		return &harness.ProcessCleanupError{Reference: s.unit}
	}
	if state == "" || state == "not-found" {
		return nil
	}
	cmd := exec.CommandContext(ctx, s.systemctl, "--user", "stop", s.unit)
	cmd.Env = append([]string(nil), s.controlEnv...)
	if err := cmd.Run(); err == nil {
		return nil
	}
	// --collect may remove the transient unit between show and stop. Treat only
	// that exact disappearance as success; a bus or stop failure is not settled.
	state, stateErr := s.loadState(ctx)
	if stateErr == nil && (state == "" || state == "not-found") {
		return nil
	}
	return &harness.ProcessCleanupError{Reference: s.unit}
}

func (s *userSystemdScope) loadState(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, s.systemctl, "--user", "show", "--property=LoadState", "--value", s.unit)
	cmd.Env = append([]string(nil), s.controlEnv...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func envValue(env []string, name string) string {
	prefix := name + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func withoutSystemdSessionEnv(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "XDG_RUNTIME_DIR" || name == "DBUS_SESSION_BUS_ADDRESS" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// recoverUserSystemdScope re-probes only a unit name generated by this
// adapter. It stops a surviving unit and confirms disappearance before
// releasing the runner's quarantine.
func recoverUserSystemdScope(ctx context.Context, unit string) (bool, error) {
	if !systemdScopeUnitPattern.MatchString(unit) {
		return false, errors.New("invalid saved Codex systemd scope reference")
	}
	runtimePath := filepath.Join("/run/user", fmt.Sprint(os.Geteuid()))
	info, err := os.Lstat(runtimePath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return false, errors.New("systemd user scope runtime directory is unavailable or unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return false, errors.New("systemd user scope runtime directory is not owned by the runner")
	}
	ctl, err := exec.LookPath("systemctl")
	if err != nil {
		return false, err
	}
	controlEnv := []string{"PATH=" + os.Getenv("PATH"), "XDG_RUNTIME_DIR=" + runtimePath, "LC_ALL=C"}
	scope := &userSystemdScope{unit: unit, systemctl: ctl, controlEnv: controlEnv, runtimePath: runtimePath}
	return recoverSystemdScope(ctx, scope)
}

func recoverSystemdScope(ctx context.Context, scope *userSystemdScope) (bool, error) {
	state, err := scope.loadState(ctx)
	if err != nil {
		return false, fmt.Errorf("probe saved systemd scope: %w", err)
	}
	if state == "" || state == "not-found" {
		return true, nil
	}
	cmd := exec.CommandContext(ctx, scope.systemctl, "--user", "stop", scope.unit)
	cmd.Env = scope.controlEnv
	if err := cmd.Run(); err != nil {
		state, probeErr := scope.loadState(ctx)
		if probeErr != nil || (state != "" && state != "not-found") {
			return false, fmt.Errorf("stop saved systemd scope: %w", err)
		}
	}
	state, err = scope.loadState(ctx)
	if err != nil {
		return false, fmt.Errorf("confirm saved systemd scope stopped: %w", err)
	}
	return state == "" || state == "not-found", nil
}
