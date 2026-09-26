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

// Package infraprovider implements an end-to-end suite for the infrastructure
// provider's kcp-side surface. It starts the railgrid-hub with embedded kcp and
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
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
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
	workspacePath = "root:railgrid:providers:infrastructure"
)

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	hubURL = "http://127.0.0.1:" + hubPort
	kcpServer = "https://127.0.0.1:" + kcpPort

	for _, p := range []string{hubPort, kcpPort, providerPort, "2380"} {
		if portInUse(p) {
			fmt.Fprintf(os.Stderr, "port :%s already in use; run `pkill railgrid-hub; pkill infrastructure-provider` and retry\n", p)
			os.Exit(2)
		}
	}

	if err := build(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}

	dataDir, err := os.MkdirTemp("", "railgrid-e2e-infraprovider-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	keepData := os.Getenv("RAILGRID_E2E_KEEP_DATA") == "true"

	hubLog, err := os.Create(filepath.Join(dataDir, "hub.log"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "create hub.log:", err)
		os.Exit(1)
	}
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
	// then materializes root:railgrid:providers:infrastructure.
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

	// init runs as the PROVIDER's ServiceAccount, not as kcp admin. The
	// platform's PermissionClaimPolicy reserves every API group it names, and
	// only the subjects it lists as providers may export one, so creating the
	// APIExport as kcp-admin is refused with
	//
	//   spec.resources[0].group: Forbidden: API group
	//   "infrastructure.railgrid.ai" is reserved by PermissionClaimPolicy
	//   "railgrid"; only its providers may export it
	//
	// The hub's Provider controller provisions that ServiceAccount and publishes
	// its token as the provider-token Secret when it materializes the workspace,
	// which is what `make init-provider-infrastructure` and the chart's init
	// container both read. Every other provider suite mints it the same way.
	bootstrapKubeconfig := filepath.Join(dataDir, "infrastructure-bootstrap.kubeconfig")
	if err := mintProviderKubeconfig(bootstrapKubeconfig, 2*time.Minute); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "mint provider kubeconfig:", err)
		os.Exit(1)
	}
	initLog, err := os.Create(filepath.Join(dataDir, "init.log"))
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "create init.log:", err)
		os.Exit(1)
	}
	initCmd := exec.Command(filepath.Join(repoRoot, "bin", "infrastructure-provider"), "init")
	initCmd.Env = append(os.Environ(),
		"INFRASTRUCTURE_ADMIN_KUBECONFIG="+bootstrapKubeconfig,
		"INFRASTRUCTURE_WORKSPACE_PATH="+workspacePath,
		"INFRASTRUCTURE_KUBECONFIG="+mintedKubeconfig,
		// The generated APIExport shell (name + permission claims, written by
		// codegen from manifest.yaml) is what init materializes the export
		// from — a claim is written in one place, not three. The image bakes
		// deploy/chart/files at /etc/railgrid/kcp; on a host run there is no
		// such directory, so point RAILGRID_KCP_DIR at the same files in the
		// tree. Without it init fails with "reading APIExport file
		// /etc/railgrid/kcp/apiexport.yaml: no such file or directory".
		"RAILGRID_KCP_DIR="+filepath.Join(repoRoot, "providers", "infrastructure", "deploy", "chart", "files"),
		// The manifest is the declaration of the provider's verbs; init reads
		// it to know which coordinates the export publishes, and the
		// DataPlaneEndpointSlice those coordinates route through needs the
		// shard-facing address of the serve process started below.
		"RAILGRID_CATALOGENTRY_FILE="+filepath.Join(repoRoot, "providers", "infrastructure", "manifest.yaml"),
		"RAILGRID_DATAPLANE_URL=http://127.0.0.1:"+providerPort,
	)
	initCmd.Stdout = initLog
	initCmd.Stderr = initLog
	if err := initCmd.Run(); err != nil {
		// Read the log BEFORE cleanup: it removes the whole data dir, so a tail
		// taken afterwards reports only that the file is gone. CI does not
		// upload this directory either, so stderr is the only place the reason
		// survives — without it the failure says just "exit status 1".
		tail := tailFile(initLog.Name(), 60)
		cleanup()
		fmt.Fprintf(os.Stderr, "provider init failed: %v (log: %s)\n%s\n", err, initLog.Name(), tail)
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
		// serve refuses to start without the manifest: a verb exists only as
		// a declared kcp custom subresource.
		"RAILGRID_CATALOGENTRY_FILE="+filepath.Join(repoRoot, "providers", "infrastructure", "manifest.yaml"),
		"RAILGRID_HUB_URL="+hubURL,
		"RAILGRID_HUB_TOKEN="+staticToken,
		"RAILGRID_HUB_INSECURE=true",
		"RAILGRID_PROVIDER_NAME=infrastructure",
		"RAILGRID_PROVIDER_KUBECONFIG="+mintedKubeconfig,
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
		tail := tailFile(provLog.Name(), 60)
		cleanup()
		fmt.Fprintf(os.Stderr, "provider never ready: %v (log: %s)\n%s\n", err, provLog.Name(), tail)
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
// mintProviderKubeconfig writes a kubeconfig for the provider workspace's
// ServiceAccount, reading its token from the provider-token Secret the hub's
// Provider controller publishes there. It polls: the Secret appears once the
// controller has finished materializing the workspace.
func mintProviderKubeconfig(path string, timeout time.Duration) error {
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // dev cert
	}
	url := kcpServer + "/clusters/" + workspacePath + "/api/v1/namespaces/default/secrets/provider-token"

	var token, lastErr string
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && token == "" {
		token, lastErr = readProviderToken(client, url)
		if token == "" {
			time.Sleep(2 * time.Second)
		}
	}
	if token == "" {
		return fmt.Errorf("provider-token never populated: %s", lastErr)
	}

	kubeconfig := fmt.Sprintf(`apiVersion: v1
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
	return os.WriteFile(path, []byte(kubeconfig), 0o600)
}

// readProviderToken returns the decoded token, or the reason it is not there
// yet.
func readProviderToken(client *http.Client, url string) (string, string) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Sprintf("GET provider-token: HTTP %d: %s", resp.StatusCode, truncate(body))
	}
	var secret struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &secret); err != nil {
		return "", err.Error()
	}
	if secret.Data.Token == "" {
		return "", "provider-token Secret exists but its token is not populated yet"
	}
	raw, err := base64.StdEncoding.DecodeString(secret.Data.Token)
	if err != nil {
		return "", fmt.Sprintf("decode provider-token: %v", err)
	}
	return string(raw), ""
}

// truncate bounds an error body so a failure stays readable.
func truncate(b []byte) string {
	const max = 200
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}

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

// tailFile returns the last n lines of a log file, so a bootstrap failure
// reports what went wrong instead of only an exit status.
func tailFile(path string, n int) string {
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
