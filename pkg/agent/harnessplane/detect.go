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
	"path/filepath"
	"strings"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// Detection is what the machine has installed: the harness name mapped to the
// executable the runner should launch.
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

// Detect reports which harnesses are installed on this machine.
//
// It is re-run on every reconcile rather than once at onboarding, because a
// harness installed after a machine joined must become usable without anyone
// touching the hub. runnerHome is the home of the account the runner child runs
// as: harness.ResolveBinary expands its "~/" search paths against the CALLING
// process's home, which for a root systemd agent is /root — not where the
// runner account's per-user install of Claude Code lives. The second pass
// closes exactly that gap.
func Detect(runnerHome string) Detection {
	out := Detection{}
	for _, name := range Names {
		if path, ok := detectOne(name, runnerHome); ok {
			out[name] = path
		}
	}
	return out
}

// detectOne resolves one harness executable, or reports that it is absent.
func detectOne(name, runnerHome string) (string, bool) {
	// What the environment already resolves, including every absolute
	// well-known install directory.
	if resolved := harness.ResolveBinary(name); resolved != name {
		return resolved, true
	}
	if runnerHome == "" {
		return "", false
	}
	for _, dir := range harness.WellKnownBinDirs {
		after, isUnderHome := strings.CutPrefix(dir, "~/")
		if !isUnderHome {
			// Already searched by ResolveBinary, and searching it again with a
			// different home would mean nothing.
			continue
		}
		candidate := filepath.Join(runnerHome, after, name)
		if executable(candidate) {
			return candidate, true
		}
	}
	return "", false
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
