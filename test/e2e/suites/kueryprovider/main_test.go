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

// Package kueryprovider implements an end-to-end suite for the kuery
// provider. It starts railgrid-hub with embedded kcp and kuery-provider as
// host subprocesses, following the standard provider bootstrap: Provider +
// CatalogEntry applied into root:railgrid:system:providers (the hub's
// Provider controller materializes the sub-workspace + SA + provider-token),
// then `kuery-provider init` with the minted SA kubeconfig (APIExport +
// SavedView schema + bind grant), then serve.
//
// Coverage is the provider-plumbing half of kuery: provisioning, init
// bootstrap, the /api/providers DTO, the hub's backend + UI proxies, tenant
// Enable via APIBinding with SavedView CRUD plus the reconciler's Ready
// verdict, Disable, the readiness surface, and the query verb's GATES —
// driven against the provider pod directly, because the proxy is not what
// authorizes.
//
// It deliberately does NOT cover fleet queries over real objects: the
// engagement controller syncs from edge clusters through the hub's
// edges-proxy, which needs the kind-based harness the edgesconn suite uses.
// Here a granted query would answer from an empty index, so the assertions
// are on who is refused and with what status.
//
// Runs without kind/Helm/Dex, matching the other subprocess provider suites.
package kueryprovider

import (
	"context"
	"encoding/base64"
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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// Ports follow the +10 convention the other subprocess suites use (provider
// 19443, infraprovider 19453, edges 19463, edgesconn 19473, cli 19483). Like
// them, this suite shares the embedded-kcp etcd port 2380 and must not run
// concurrently with the others.
const (
	hubPort      = "19493"
	kcpPort      = "16493"
	defaultPPort = "18118"
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
			fmt.Fprintf(os.Stderr, "port :%s already in use; run `pkill railgrid-hub; pkill kuery-provider` and retry\n", p)
			os.Exit(2)
		}
	}

	// Build binaries up-front so test-runtime startup is just process exec.
	if err := build(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}

	dataDir, err := os.MkdirTemp("", "railgrid-e2e-kuery-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	// Don't auto-clean dataDir on failure — useful for post-mortem.
	keepData := os.Getenv("RAILGRID_E2E_KEEP_DATA") == "true"

	hubLog, _ := os.Create(filepath.Join(dataDir, "hub.log"))
	hubCmd := exec.Command(filepath.Join(repoRoot, "bin", "railgrid-hub"),
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

	// Wait for hub readiness (embedded kcp bootstrap takes ~30-60s).
	if err := waitReady(hubURL+"/readyz", 3*time.Minute); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "hub never ready:", err)
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

	// Provisioning: Provider + CatalogEntry into root:railgrid:system:providers
	// (mirrors `make install-provider-kuery`); the hub's Provider controller
	// materializes root:railgrid:providers:kuery, the provider SA, and the
	// provider-token Secret.
	if err := applyKueryManifests(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "apply kuery manifests:", err)
		os.Exit(1)
	}

	// Mint the SA runtime kubeconfig from the provider-token Secret and run
	// `kuery-provider init` — the APIExport, the SavedView schema and the
	// bind grant come from init, not the hub.
	runtimeKubeconfig := filepath.Join(dataDir, "kuery-runtime.kubeconfig")
	if err := mintRuntimeKubeconfig(runtimeKubeconfig, 2*time.Minute); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "mint runtime kubeconfig:", err)
		os.Exit(1)
	}
	initLog, err := os.Create(filepath.Join(dataDir, "init.log"))
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "create init.log:", err)
		os.Exit(1)
	}
	initCmd := exec.Command(filepath.Join(repoRoot, "bin", "kuery-provider"), "init")
	initCmd.Env = append(os.Environ(),
		"RAILGRID_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		"KUERY_WORKSPACE_PATH="+workspacePath,
		// The SavedView APIResourceSchema the chart ships — init reads the
		// schemas dir to author the APIExport's resources.
		"RAILGRID_KCP_DIR="+filepath.Join(repoRoot, "providers", "kuery", "deploy", "chart", "files"),
	)
	initCmd.Stdout = initLog
	initCmd.Stderr = initLog
	if err := initCmd.Run(); err != nil {
		tail := tailInitLog(initLog.Name(), 60)
		cleanup()
		fmt.Fprintf(os.Stderr, "kuery init failed: %v (log: %s)\n%s\n", err, initLog.Name(), tail)
		os.Exit(1)
	}

	provLog, err := os.Create(filepath.Join(dataDir, "provider.log"))
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "create provider.log:", err)
		os.Exit(1)
	}
	provCmd = exec.Command(filepath.Join(repoRoot, "bin", "kuery-provider"))
	provCmd.Env = append(os.Environ(),
		"PORT="+providerPort,
		"RAILGRID_HUB_URL="+hubURL,
		"RAILGRID_HUB_TOKEN="+staticToken,
		"RAILGRID_PROVIDER_NAME=kuery",
		"RAILGRID_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		// The CatalogEntry manifest is where serve reads the
		// "<resource>/<verb>" coordinates it answers on the kcp
		// custom-subresource path — the only way a verb is reached — so the
		// provider refuses to start without it. init already applied the
		// entry (applyKueryManifests), so this is not repeated there.
		"RAILGRID_CATALOGENTRY_FILE="+filepath.Join(repoRoot, "providers", "kuery", "manifest.yaml"),
	)
	provCmd.Stdout = provLog
	provCmd.Stderr = provLog
	provCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := provCmd.Start(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "start provider:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "kuery-provider started (pid=%d, port=:%s)\n", provCmd.Process.Pid, providerPort)

	if err := waitReady("http://127.0.0.1:"+providerPort+"/healthz", 60*time.Second); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "kuery never ready:", err)
		os.Exit(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}

// mintRuntimeKubeconfig waits for the Provider controller to populate the
// provider-token Secret in the sub-workspace and writes a workspace-scoped
// kubeconfig around it — the same credential the provider pod mounts.
func mintRuntimeKubeconfig(path string, timeout time.Duration) error {
	cl, err := kcpDynamicRaw(workspacePath, adminToken)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	var token string
	var lastErr string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		sec, err := cl.Resource(secretGVR).Namespace("default").Get(ctx, "provider-token", metav1.GetOptions{})
		cancel()
		if err != nil {
			lastErr = err.Error()
		} else {
			enc, _, _ := unstructured.NestedString(sec.Object, "data", "token")
			if enc != "" {
				raw, err := base64.StdEncoding.DecodeString(enc)
				if err != nil {
					return fmt.Errorf("decode provider-token: %w", err)
				}
				token = string(raw)
				break
			}
			lastErr = "provider-token Secret exists but token not yet populated"
		}
		time.Sleep(2 * time.Second)
	}
	if token == "" {
		return fmt.Errorf("provider-token never populated: %s", lastErr)
	}
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: railgrid
  cluster:
    server: %s/clusters/%s
    insecure-skip-tls-verify: true
contexts:
- name: railgrid
  context:
    cluster: railgrid
    user: railgrid
current-context: railgrid
users:
- name: railgrid
  user:
    token: %s
`, kcpServer, workspacePath, token)
	return os.WriteFile(path, []byte(kc), 0o600)
}

// build runs `make build-hub build-kuery-provider` so the test runs against
// current source even when the user hasn't built manually.
func build(root string) error {
	cmd := exec.Command("make", "-C", root, "build-hub", "build-kuery-provider")
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

// tailInitLog returns the last n lines of a bootstrap log, so a failure reports
// what went wrong instead of only an exit status. It must be read BEFORE
// cleanup, which removes the data directory; CI does not upload that directory
// either, so stderr is the only place the reason survives.
func tailInitLog(path string, n int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "(" + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
