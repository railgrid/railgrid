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

// Package infraprovider implements an end-to-end suite for the infrastructure
// provider's kcp-side surface. It starts the kedge-hub with embedded kcp and
// the infrastructure provider (init + serve) as host subprocesses — the same
// shape `suites/provider` uses for quickstart — then exercises what the
// kind/kro template e2e (make e2e-infrastructure) cannot: provisioning
// (Provider + CatalogEntry → workspace), `init` bootstrap (CRDs, APIExport,
// template seeding), the Template controller's full reconcile chain
// (per-template CRD + APIResourceSchema + APIExport sync, via the stub
// backend), retirement of removed platform templates, and the tenant catalog
// path (APIBinding → list templates from a tenant workspace).
//
// Runs without kind/Helm/Dex/kro: with KRO_KUBECONFIG unset the provider
// registers only the stub backend, so `backend: kro` seed templates park at
// BackendNotFound (asserted as catalog presence, not readiness) while
// `backend: stub` test templates drive the controller end-to-end.
package infraprovider

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
	repoRoot    string
	hubURL      string // http://127.0.0.1:<port>
	kcpServer   string // https://127.0.0.1:<port> (admin kubeconfig)
	adminToken  string // kcp admin token (from <dataDir>/kcp/admin.kubeconfig)
	staticToken = "test:user-default"
)

// Ports deliberately distinct from suites/provider (19443/16443/18081) so the
// two subprocess suites never collide on a shared machine. Both still need
// the embedded kcp's fixed etcd port 2380, so they cannot run concurrently.
const (
	hubPort       = "19453"
	kcpPort       = "16453"
	providerPort  = "18086"
	workspacePath = "root:kedge:providers:infrastructure"
)

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	hubURL = "http://127.0.0.1:" + hubPort
	kcpServer = "https://127.0.0.1:" + kcpPort

	for _, p := range []string{hubPort, kcpPort, providerPort, "2380"} {
		if portInUse(p) {
			fmt.Fprintf(os.Stderr, "port :%s already in use; run `pkill kedge-hub; pkill infrastructure-provider` and retry\n", p)
			os.Exit(2)
		}
	}

	if err := build(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}

	dataDir, err := os.MkdirTemp("", "kedge-e2e-infraprovider-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	keepData := os.Getenv("KEDGE_E2E_KEEP_DATA") == "true"

	hubLog, err := os.Create(filepath.Join(dataDir, "hub.log"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "create hub.log:", err)
		os.Exit(1)
	}
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

	var provCmd *exec.Cmd
	cleanup := func() {
		killGroup(hubCmd)
		killGroup(provCmd)
		if !keepData {
			_ = os.RemoveAll(dataDir)
		} else {
			fmt.Fprintf(os.Stderr, "logs preserved under %s\n", dataDir)
		}
	}

	if err := waitReady(hubURL+"/readyz", 3*time.Minute); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "hub never ready:", err)
		os.Exit(1)
	}

	adminKubeconfig := filepath.Join(dataDir, "kcp", "admin.kubeconfig")
	tok, err := extractToken(adminKubeconfig)
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "extract admin token:", err)
		os.Exit(1)
	}
	adminToken = tok

	// Provisioning: apply the Provider + CatalogEntry (mirrors
	// `make install-provider-infrastructure`). The hub's Provider controller
	// then materializes root:kedge:providers:infrastructure.
	if err := applyProviderManifests(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "apply provider manifests:", err)
		os.Exit(1)
	}
	if err := waitWorkspace(90 * time.Second); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "provider workspace never appeared:", err)
		os.Exit(1)
	}

	// Bootstrap: `infrastructure-provider init` installs the CRDs, APIExport,
	// CachedResource and seeds the templates (mirrors the chart init
	// container / `make init-provider-infrastructure`). It also mints the
	// workspace-scoped ServiceAccount kubeconfig serve runs with.
	mintedKubeconfig := filepath.Join(dataDir, "infrastructure.kubeconfig")
	initLog, err := os.Create(filepath.Join(dataDir, "init.log"))
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "create init.log:", err)
		os.Exit(1)
	}
	initCmd := exec.Command(filepath.Join(repoRoot, "bin", "infrastructure-provider"), "init")
	initCmd.Env = append(os.Environ(),
		"INFRASTRUCTURE_ADMIN_KUBECONFIG="+adminKubeconfig,
		"INFRASTRUCTURE_WORKSPACE_PATH="+workspacePath,
		"INFRASTRUCTURE_KUBECONFIG="+mintedKubeconfig,
	)
	initCmd.Stdout = initLog
	initCmd.Stderr = initLog
	if err := initCmd.Run(); err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "provider init failed: %v (log: %s)\n", err, initLog.Name())
		os.Exit(1)
	}

	// Serve: REST + MCP + the Template controller (stub backend only — no
	// KRO_KUBECONFIG). Runs with the SA kubeconfig init minted — NOT the
	// admin one — so the suite exercises the RBAC init actually granted,
	// exactly like the chart's serve container.
	provLog, err := os.Create(filepath.Join(dataDir, "provider.log"))
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "create provider.log:", err)
		os.Exit(1)
	}
	provCmd = exec.Command(filepath.Join(repoRoot, "bin", "infrastructure-provider"))
	provCmd.Env = append(os.Environ(),
		"PORT="+providerPort,
		"KEDGE_HUB_URL="+hubURL,
		"KEDGE_HUB_TOKEN="+staticToken,
		"KEDGE_HUB_INSECURE=true",
		"KEDGE_PROVIDER_NAME=infrastructure",
		"INFRASTRUCTURE_KUBECONFIG="+mintedKubeconfig,
	)
	provCmd.Stdout = provLog
	provCmd.Stderr = provLog
	provCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := provCmd.Start(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "start provider:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "infrastructure-provider started (pid=%d, port=:%s)\n", provCmd.Process.Pid, providerPort)

	if err := waitReady("http://127.0.0.1:"+providerPort+"/healthz", 30*time.Second); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "provider never ready:", err)
		os.Exit(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}

func build(root string) error {
	cmd := exec.Command("make", "-C", root, "build-hub", "build-infrastructure-provider")
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
// kubeconfig — same cheap parse as suites/provider.
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

func ctxWithTimeout(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
