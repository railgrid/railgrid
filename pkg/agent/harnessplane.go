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

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/pkg/agent/harnessplane"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
	"github.com/railgrid/railgrid/pkg/util/localuser"
)

// DefaultRunnerUser is the dedicated system account harness runners run as when
// the operator named none. Creating it is what keeps the DEFAULT path working:
// with mode auto, a machine that has Claude Code installed is a harness host, so
// refusing to start for lack of a flag would turn the default into a failure.
const DefaultRunnerUser = "railgrid-runner"

// runnerAccountHomeBase is where a created runner account's home lives. It is
// under /var/lib rather than /home because the account is a service identity,
// not a person, and it must exist on a machine with no /home at all.
const runnerAccountHomeBase = "/var/lib"

// ParseHarnessSetting validates a --harness value ("auto", "none", or a
// comma-separated list of harness names) into the setting the plane seeds its
// cache with. Exported so `railgrid agent join`, `railgrid agent install` and
// `railgrid edge create` all refuse the same typos at the same moment.
func ParseHarnessSetting(raw string) (harnessplane.Setting, error) {
	return harnessplane.ParseSetting(raw)
}

// EnsureRunnerAccount resolves the local account harness runners run as,
// CREATING a dedicated system account when name is the default and does not
// exist yet. A named account is never created: an operator who mistyped
// --runner-user must be told, not given a new account they did not ask for.
//
// Linux only. On macOS the LaunchDaemon's worker account is the runner account,
// so there is nothing to create and --runner-user is not accepted.
func EnsureRunnerAccount(name string) (localuser.Account, error) {
	name = strings.TrimSpace(name)
	create := false
	if name == "" {
		name, create = DefaultRunnerUser, true
	}
	account, err := localuser.Resolve(name, "", "")
	if err == nil {
		return account, nil
	}
	if !create {
		return localuser.Account{}, err
	}
	if runtime.GOOS != "linux" {
		return localuser.Account{}, fmt.Errorf(
			"no %q account on this %s host: name an existing non-root account with --runner-user", name, runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return localuser.Account{}, fmt.Errorf(
			"account %q does not exist and only root can create it: run as root or name an existing account with --runner-user", name)
	}
	if err := createRunnerAccount(name); err != nil {
		return localuser.Account{}, err
	}
	return localuser.Resolve(name, "", "")
}

// createRunnerAccount adds a locked-down system account. The shell is a nologin
// binary and the home is created, because the runner keeps its state — and a
// per-user harness install — under it.
func createRunnerAccount(name string) error {
	home := filepath.Join(runnerAccountHomeBase, name)
	// Not every distribution ships /usr/sbin/nologin, and an invalid shell fails
	// the whole useradd. Try the hardened form first and fall back to whatever
	// the distribution's default is: a system account with a login shell is
	// still far better than running a harness as root.
	attempts := [][]string{
		{"--system", "--shell", "/usr/sbin/nologin", "--home-dir", home, "--create-home", name},
		{"--system", "--home-dir", home, "--create-home", name},
	}
	var lastErr error
	for _, args := range attempts {
		out, err := exec.Command("useradd", args...).CombinedOutput() //nolint:gosec // fixed argv, name is validated by the lookup that follows
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("useradd %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return fmt.Errorf("creating the runner account %q: %w", name, lastErr)
}

// resolveRunnerAccount decides which local account runner children run as.
//
// A root agent (the systemd unit has no User=) hands the child a separate
// non-root account: the whole point is that a code-execution child does not
// inherit the agent's privileges. A non-root agent (the macOS LaunchDaemon
// worker, or a hand-started agent) runs children as itself, and is not allowed
// to claim a different account — it could not become one anyway.
func (a *Agent) resolveRunnerAccount() (harnessplane.RunAsAccount, error) {
	requested := strings.TrimSpace(a.opts.RunnerUser)
	if os.Geteuid() == 0 {
		account, err := EnsureRunnerAccount(requested)
		if err != nil {
			return harnessplane.RunAsAccount{}, err
		}
		return harnessplane.RunAsAccount{Home: account.Home, UID: account.UID, GID: account.GID}, nil
	}
	if requested != "" {
		current, err := user.Current()
		if err != nil {
			return harnessplane.RunAsAccount{}, fmt.Errorf("resolving the current account: %w", err)
		}
		if current.Username != requested {
			return harnessplane.RunAsAccount{}, fmt.Errorf(
				"--runner-user=%q but this agent runs as %q and cannot change user; run the agent as root to use a separate runner account",
				requested, current.Username)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return harnessplane.RunAsAccount{}, fmt.Errorf("resolving the agent's home directory: %w", err)
	}
	if !filepath.IsAbs(home) || home == "/" {
		return harnessplane.RunAsAccount{}, fmt.Errorf("the agent's home directory %q cannot hold runner state", home)
	}
	return harnessplane.RunAsAccount{Home: home, UID: harnessplane.InheritUID, GID: harnessplane.InheritUID}, nil
}

// startHarnessPlane starts the harness plane for a host edge and returns the
// manager, or nil when this build or this host cannot supervise a harness.
//
// The returned manager is the single source of three things: the heartbeat's
// status.harnesses, the runner entries on /api/v1/services, and the bearer the
// /svc proxy injects. Nothing else in the agent knows a runner exists.
func (a *Agent) startHarnessPlane(ctx context.Context, logger klog.Logger) *harnessplane.Manager {
	logger = logger.WithName("harness-plane")
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		logger.Info("Harness plane disabled: supervising a harness is a Linux and macOS feature", "os", runtime.GOOS)
		return nil
	}

	setting, err := ParseHarnessSetting(a.opts.Harness)
	if err != nil {
		logger.Error(err, "harness plane disabled: --harness is not usable")
		return nil
	}

	account, err := a.resolveRunnerAccount()
	if err != nil {
		logger.Error(err, "harness plane disabled: cannot resolve the account runner children run as")
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		logger.Error(err, "harness plane disabled: cannot resolve this executable")
		return nil
	}
	// Follow symlinks the same way the installers do, so an upgrade that
	// replaces the target is picked up on the next child restart rather than
	// re-execing a deleted inode.
	if resolved, rerr := filepath.EvalSymlinks(executable); rerr == nil {
		executable = resolved
	}
	agentHome, err := os.UserHomeDir()
	if err != nil {
		logger.Error(err, "harness plane disabled: cannot resolve the agent's home for the harness cache")
		return nil
	}

	// A nil client is legitimate here only in tests; in the agent it always has
	// one, and without it the plane would never see a spec.harness change.
	hubDynamic, err := dynamic.NewForConfig(a.hubConfig)
	if err != nil {
		logger.Error(err, "harness plane disabled: cannot build a hub dynamic client")
		return nil
	}

	manager, err := harnessplane.NewManager(hubDynamic, harnessplane.Options{
		EdgeName:   a.opts.EdgeName,
		EdgeGVR:    railgridclient.EdgeGVRForType(string(a.agentType)),
		Executable: executable,
		Account:    account,
		CachePath:  harnessplane.CachePath(agentHome, a.opts.EdgeName),
		Seed:       setting,
	})
	if err != nil {
		logger.Error(err, "harness plane disabled: cannot build the harness manager")
		return nil
	}

	go func() {
		if err := manager.Run(ctx); err != nil {
			logger.Error(err, "harness plane failed")
		}
	}()
	return manager
}
