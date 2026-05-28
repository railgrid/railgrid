/*
Copyright 2026 The Faros Authors.

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

// Package providerflags exercises the kedge-hub `--providers` flag in
// isolation. Unlike test/e2e/suites/provider this suite does NOT start a
// long-lived shared hub — each test spawns its own kedge-hub subprocess
// with custom flags and tears it down afterwards. The reason is purely
// practical: embedded kcp binds etcd on a hard-coded port (2380), so two
// hubs cannot coexist on one host.
//
// Run via `make e2e-provider-flags`. Mutually exclusive with
// `e2e-provider` — port 2380 must be free.
package providerflags

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Shared across tests.
var (
	repoRoot  string
	hubBinary string
)

const staticToken = "test:user-default"

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
	hubBinary = filepath.Join(repoRoot, "bin", "kedge-hub")

	// Fail fast if the etcd port is busy. The suite can't function with a
	// concurrent hub running.
	if portInUse("2380") {
		fmt.Fprintln(os.Stderr,
			"port 2380 is in use; this suite needs an exclusive embedded etcd. "+
				"Stop any running kedge-hub (e.g. `pkill kedge-hub`) and retry.")
		os.Exit(2)
	}

	// Build once at suite start — saves test-runtime build cost.
	cmd := exec.Command("make", "-C", repoRoot, "build-hub")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build-hub failed:", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func portInUse(p string) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+p, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
