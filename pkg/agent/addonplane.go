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
	"os/user"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/pkg/agent/addons"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
	"github.com/railgrid/railgrid/pkg/util/localuser"
)

// NormalizeAllowedAddons cleans and validates the --allow-addon list: values
// may be repeated or comma-separated, are trimmed and de-duplicated, and must
// name a type this agent build can actually materialize. A typo is an error
// rather than a silently empty allow list — "I allowed it and nothing happened"
// is the worst possible failure mode for an opt-in.
func NormalizeAllowedAddons(raw []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, entry := range raw {
		for _, value := range strings.Split(entry, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if !slices.Contains(addons.KnownTypes, value) {
				return nil, fmt.Errorf("unknown add-on type %q; known types: %s", value, strings.Join(addons.KnownTypes, ", "))
			}
			if seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out, nil
}

// resolveAddonAccount decides which local account add-on children run as.
//
// A root agent (the systemd unit has no User=) MUST name a separate non-root
// account: the whole point of the add-on plane is that a code-execution child
// does not inherit the agent's privileges. A non-root agent (the macOS
// LaunchDaemon worker, or a hand-started agent) runs children as itself, and is
// not allowed to claim a different account — it could not become one anyway.
func (a *Agent) resolveAddonAccount() (addons.RunAsAccount, error) {
	requested := strings.TrimSpace(a.opts.AddonUser)
	if os.Geteuid() == 0 {
		if requested == "" {
			return addons.RunAsAccount{}, fmt.Errorf("--addon-user is required when the agent runs as root and an add-on is allowed")
		}
		account, err := localuser.Resolve(requested, "", "")
		if err != nil {
			return addons.RunAsAccount{}, err
		}
		return addons.RunAsAccount{Home: account.Home, UID: account.UID, GID: account.GID}, nil
	}
	if requested != "" {
		current, err := user.Current()
		if err != nil {
			return addons.RunAsAccount{}, fmt.Errorf("resolving the current account: %w", err)
		}
		if current.Username != requested {
			return addons.RunAsAccount{}, fmt.Errorf(
				"--addon-user=%q but this agent runs as %q and cannot change user; run the agent as root to use a separate add-on account",
				requested, current.Username)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return addons.RunAsAccount{}, fmt.Errorf("resolving the agent's home directory: %w", err)
	}
	if !filepath.IsAbs(home) || home == "/" {
		return addons.RunAsAccount{}, fmt.Errorf("the agent's home directory %q cannot hold add-on state", home)
	}
	return addons.RunAsAccount{Home: home, UID: addons.InheritUID, GID: addons.InheritUID}, nil
}

// startAddonManager starts the edge add-on plane for a host edge and returns
// the add-on types this agent will actually materialize — which is what the
// heartbeat publishes on the edge's status.allowedAddons.
//
// It returns nil (and publishes nothing) when no add-on is allowed, or when the
// plane could not be built. Reporting a type the agent cannot serve would be a
// lie a portal would then show to a tenant.
func (a *Agent) startAddonManager(ctx context.Context, logger klog.Logger) []string {
	if len(a.opts.AllowedAddons) == 0 {
		return nil
	}
	logger = logger.WithName("addon-plane")

	account, err := a.resolveAddonAccount()
	if err != nil {
		logger.Error(err, "add-on plane disabled: cannot resolve the account add-on children run as")
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		logger.Error(err, "add-on plane disabled: cannot resolve this executable")
		return nil
	}
	// Follow symlinks the same way the installers do, so an upgrade that
	// replaces the target is picked up on the next child restart rather than
	// re-execing a deleted inode.
	if resolved, rerr := filepath.EvalSymlinks(executable); rerr == nil {
		executable = resolved
	}

	hubDynamic, err := dynamic.NewForConfig(a.hubConfig)
	if err != nil {
		logger.Error(err, "add-on plane disabled: cannot build a hub dynamic client")
		return nil
	}
	hubKube, err := kubernetes.NewForConfig(a.hubConfig)
	if err != nil {
		logger.Error(err, "add-on plane disabled: cannot build a hub clientset")
		return nil
	}

	manager, err := addons.NewManager(hubDynamic, addons.Options{
		EdgeKind: railgridclient.EdgeKindForType(string(a.agentType)),
		EdgeName: a.opts.EdgeName,
		Allowed:  a.opts.AllowedAddons,
	})
	if err != nil {
		logger.Error(err, "add-on plane disabled: cannot build the add-on manager")
		return nil
	}

	runnerFactory, err := addons.NewRunnerFactory(addons.RunnerOptions{
		EdgeName:   a.opts.EdgeName,
		Executable: executable,
		Account:    account,
		Kube:       hubKube,
	})
	if err != nil {
		logger.Error(err, "add-on plane disabled: cannot build the runner add-on")
		return nil
	}
	manager.Register(addons.TypeRunner, runnerFactory)

	go func() {
		if err := manager.Run(ctx); err != nil {
			logger.Error(err, "add-on manager failed")
		}
	}()
	logger.Info("Add-on plane started",
		"allowed", manager.AllowedTypes(),
		"runAs", addonAccountDescription(account),
		"executable", executable)
	return manager.AllowedTypes()
}

// addonAccountDescription is a log-safe description of the add-on account.
func addonAccountDescription(account addons.RunAsAccount) string {
	if account.Inherited() {
		return "the agent's own account (home " + account.Home + ")"
	}
	return fmt.Sprintf("uid %d gid %d (home %s)", account.UID, account.GID, account.Home)
}
