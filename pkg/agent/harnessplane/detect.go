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

package harnessplane

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// Detection is what the machine has installed: the harness name mapped to the
// executable the runner should launch. Only an executable the runner ACCOUNT can
// launch is in it; one the machine has but that account cannot run is reported
// through Blocked instead (see Detect).
type Detection map[string]string

// Names returns the detected harness names in canonical order.
func (d Detection) Names() []string {
	out := make([]string, 0, len(d))
	for _, name := range Names {
		if d[name] != "" {
			out = append(out, name)
		}
	}
	return out
}

// Blocked maps a harness name to an executable that exists on this machine but
// that the runner account cannot execute. It is only set for a harness that
// Detection lacks: a usable install anywhere else wins, and the blocked one is
// then of no interest.
type Blocked map[string]string

// Detect reports which harnesses are installed on this machine and launchable
// by the runner account.
//
// It is re-run on every reconcile rather than once at onboarding, because a
// harness installed after a machine joined must become usable without anyone
// touching the hub.
//
// The search order, most specific to the runner account first:
//
//  1. the "~/" entries of harness.WellKnownBinDirs against the runner account's
//     home — a per-user install made AS that account;
//  2. the agent's PATH;
//  3. harness.WellKnownBinDirs in order, "~/" expanded against the agent's own
//     home — which for a root systemd agent is /root.
//
// Every candidate must be a regular executable file that the runner account can
// reach. The last step is where that matters: an operator who installs Claude
// Code as root gets /root/.local/bin/claude, behind a /root nobody else can
// traverse. The agent used to pick it, and the runner — running as the account
// precisely so it is not root — failed with "permission denied" from a path it
// never chose. Such an install is skipped and reported in Blocked, so the
// machine's harness status says what to move where.
func Detect(account RunAsAccount) (Detection, Blocked) {
	found, blocked := Detection{}, Blocked{}
	for _, name := range Names {
		path, unusable := detectFrom(account, candidates(name, account))
		switch {
		case path != "":
			found[name] = path
		case unusable != "":
			blocked[name] = unusable
		}
	}
	return found, blocked
}

// detectFrom picks the first candidate that is an executable file the runner
// account can run. When none is, it also reports the first executable it had to
// skip, so the caller can say "installed, but not for this account".
func detectFrom(account RunAsAccount, candidates []string) (path, unusable string) {
	for _, candidate := range candidates {
		if !executable(candidate) {
			continue
		}
		if account.Inherited() || accessibleBy(candidate, account.UID, account.GID) {
			return candidate, ""
		}
		if unusable == "" {
			unusable = candidate
		}
	}
	return "", unusable
}

// candidates lists where name may be installed, in the order Detect documents.
// An entry may repeat (a runner home that is the agent's own); a repeat costs a
// stat and nothing else.
func candidates(name string, account RunAsAccount) []string {
	var out []string
	perUser := func(home string) {
		if home == "" {
			return
		}
		for _, dir := range harness.WellKnownBinDirs {
			if after, ok := strings.CutPrefix(dir, "~/"); ok {
				out = append(out, filepath.Join(home, after, name))
			}
		}
	}
	if !account.Inherited() {
		perUser(account.Home)
	}
	if resolved, err := exec.LookPath(name); err == nil {
		out = append(out, resolved)
	}
	agentHome, _ := os.UserHomeDir()
	for _, dir := range harness.WellKnownBinDirs {
		if after, ok := strings.CutPrefix(dir, "~/"); ok {
			if agentHome != "" {
				out = append(out, filepath.Join(agentHome, after, name))
			}
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// executable reports whether path is a regular file with an execute bit. A
// directory or a non-executable file of the right name is not a harness.
func executable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
