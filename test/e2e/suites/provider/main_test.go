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

// Package provider implements an end-to-end suite for the kedge provider
// extension surface. It starts the kedge-hub with embedded kcp and the
// reference quickstart provider as host subprocesses, then exercises the
// full lifecycle: catalog provisioning (sub-workspace + APIResourceSchema
// + APIExport + RBAC), the /api/providers and /ui|services/providers
// proxies, tenant Enable via direct APIBinding, and heartbeat freshness.
//
// Runs without kind/Helm/Dex. Intentionally lighter-weight than the
// standalone suite so iteration on the provider plumbing is fast.
package provider

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Suite-shared state populated by TestMain.
var (
	repoRoot     string
	hubURL       string // http://127.0.0.1:<port>
	kcpServer    string // https://127.0.0.1:<port> (admin kubeconfig)
	adminToken   string // kcp admin token (from .kcp/admin.kubeconfig)
	staticToken  = "test:user-default"
	providerPort string
)

const (
	hubPort      = "19443"
	kcpPort      = "16443"
	defaultPPort = "18081"
)

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	providerPort = defaultPPort
	hubURL = "http://127.0.0.1:" + hubPort
	kcpServer = "https://127.0.0.1:" + kcpPort

	// Fail fast if a previous run left ports bound.
	for _, p := range []string{hubPort, kcpPort, providerPort, "2380"} {
		if portInUse(p) {
			fmt.Fprintf(os.Stderr, "port :%s already in use; run `pkill kedge-hub; pkill quickstart-provider` and retry\n", p)
			os.Exit(2)
		}
	}

	// Build binaries up-front so test-runtime startup is just process exec.
	if err := build(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}

	dataDir, err := os.MkdirTemp("", "kedge-e2e-provider-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	// Don't auto-clean dataDir on failure — useful for post-mortem.
	keepData := os.Getenv("KEDGE_E2E_KEEP_DATA") == "true"

	hubLog, _ := os.Create(filepath.Join(dataDir, "hub.log"))
	hubCmd := exec.Command(filepath.Join(repoRoot, "bin", "kedge-hub"),
		"--embedded-kcp",
		"--kcp-bind-address", "127.0.0.1",
		"--kcp-secure-port", kcpPort,
		"--listen-addr", ":"+hubPort,
		"--data-dir", dataDir,
		"--static-auth-token", staticToken,
	)
	hubCmd.Stdout = hubLog
	hubCmd.Stderr = hubLog
	hubCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := hubCmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start hub:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "hub started (pid=%d, log=%s)\n", hubCmd.Process.Pid, hubLog.Name())

	provLog, _ := os.Create(filepath.Join(dataDir, "provider.log"))
	provCmd := exec.Command(filepath.Join(repoRoot, "bin", "quickstart-provider"))
	provCmd.Env = append(os.Environ(),
		"PORT="+providerPort,
		"KEDGE_HUB_URL="+hubURL,
		"KEDGE_HUB_TOKEN="+staticToken,
		"KEDGE_PROVIDER_NAME=quickstart",
	)
	provCmd.Stdout = provLog
	provCmd.Stderr = provLog
	provCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := provCmd.Start(); err != nil {
		killGroup(hubCmd)
		fmt.Fprintln(os.Stderr, "start provider:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "quickstart-provider started (pid=%d, port=:%s)\n", provCmd.Process.Pid, providerPort)

	// Cleanup on any exit path.
	cleanup := func() {
		killGroup(hubCmd)
		killGroup(provCmd)
		if !keepData {
			_ = os.RemoveAll(dataDir)
		} else {
			fmt.Fprintf(os.Stderr, "logs preserved under %s\n", dataDir)
		}
	}

	// Wait for hub readiness (embedded kcp bootstrap takes ~30-60s).
	if err := waitReady(hubURL+"/readyz", 3*time.Minute); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "hub never ready:", err)
		os.Exit(1)
	}
	if err := waitReady("http://127.0.0.1:"+providerPort+"/healthz", 10*time.Second); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "quickstart never ready:", err)
		os.Exit(1)
	}

	// Snapshot the admin token from the kubeconfig the hub just wrote.
	tok, err := extractToken(filepath.Join(dataDir, "kcp", "admin.kubeconfig"))
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "extract admin token:", err)
		os.Exit(1)
	}
	adminToken = tok

	code := m.Run()
	cleanup()
	os.Exit(code)
}

// build runs `make build-hub build-quickstart-provider` so the test runs
// against current source even when the user hasn't built manually.
func build(root string) error {
	cmd := exec.Command("make", "-C", root, "build-hub", "build-quickstart-provider")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func portInUse(p string) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+p, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func killGroup(c *exec.Cmd) {
	if c == nil || c.Process == nil {
		return
	}
	// Negative PID signals the process group.
	_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	_, _ = c.Process.Wait()
}

func waitReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), "ok") {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout after %s waiting for %s", timeout, url)
}

// extractToken pulls the first `token:` value out of the kcp admin
// kubeconfig. Cheap and avoids pulling clientcmd in just for parsing.
func extractToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "token:") {
			return strings.TrimSpace(strings.TrimPrefix(s, "token:")), nil
		}
	}
	return "", fmt.Errorf("no token: line in %s", path)
}

// ctxWithTimeout is a helper used across tests.
func ctxWithTimeout(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
