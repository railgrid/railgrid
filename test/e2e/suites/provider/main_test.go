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

// Package provider implements an end-to-end suite for the railgrid provider
// extension surface. It starts the railgrid-hub with embedded kcp and the
// reference quickstart provider as host subprocesses, following the current
// bootstrap flow: Provider + CatalogEntry applied into
// root:railgrid:system:providers (the hub's Provider controller materializes
// the sub-workspace + SA + provider-token), then `quickstart-provider init`
// with the minted SA kubeconfig (APIExport + schemas + bind grant), then
// serve. The tests exercise the full lifecycle: catalog provisioning, the
// /api/providers and /ui|services/providers proxies, tenant Enable via direct
// APIBinding, the reconciler stamping status in two independent tenant
// workspaces, the data-plane greet verb as a kcp custom subresource — through
// the hub's front door, straight at kcp, and across a provider claim — with its
// cross-workspace denial, and heartbeat freshness.
//
// Runs without kind/Helm/Dex. Intentionally lighter-weight than the
// standalone suite so iteration on the provider plumbing is fast.
package provider

import (
	"context"
	"crypto/tls"
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

	"github.com/railgrid/railgrid/pkg/util/identity"
)

// Suite-shared state populated by TestMain.
var (
	repoRoot   string
	hubURL     string // http://127.0.0.1:<port>
	kcpServer  string // https://127.0.0.1:<port> (admin kubeconfig)
	adminToken string // kcp admin token (from .kcp/admin.kubeconfig)
	// Two static tokens, so the suite has two distinct users and therefore two
	// distinct tenant workspaces. That is what makes "workspace A's token
	// cannot greet workspace B's Greeting" a real assertion rather than a
	// self-comparison: each static token maps to its own kcp identity
	// (pkg/hub/kcp/embedded.go writes one line per token into kcp's token auth
	// file) and each user gets cluster-admin only in their own workspace.
	staticToken  = "test:user-default"
	secondToken  = "test:user-second"
	providerPort string
	// providerURL is the provider's own port. A verb is only ever reached
	// through kcp; the suite addresses this directly only to prove that a
	// bearer presented here is refused.
	providerURL string // http://127.0.0.1:<providerPort>
	// runtimeKubeconfig is the provider ServiceAccount's kubeconfig init and
	// serve run with; a test that needs to act AS the provider (the
	// cross-provider hop) borrows its token.
	runtimeKubeconfig string
	// providerLogPath is where serve writes; a test that needs to see what
	// reached the provider, and as whom, reads it.
	providerLogPath string
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
	providerURL = "http://127.0.0.1:" + providerPort

	// Fail fast if a previous run left ports bound.
	// 2380 is the embedded server's etcd peer port; a kcp run from an image
	// keeps its etcd inside the container.
	ports := []string{hubPort, kcpPort, providerPort}
	if strings.TrimSpace(os.Getenv("RAILGRID_E2E_KCP_IMAGE")) == "" {
		ports = append(ports, "2380")
	}
	for _, p := range ports {
		if portInUse(p) {
			fmt.Fprintf(os.Stderr, "port :%s already in use; run `pkill railgrid-hub; pkill quickstart-provider` and retry\n", p)
			os.Exit(2)
		}
	}

	// Build binaries up-front so test-runtime startup is just process exec.
	if err := build(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}

	dataDir, err := os.MkdirTemp("", "railgrid-e2e-provider-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	// Don't auto-clean dataDir on failure — useful for post-mortem.
	keepData := os.Getenv("RAILGRID_E2E_KEEP_DATA") == "true"

	// RAILGRID_E2E_KCP_IMAGE runs kcp from a published image (a PR build, say)
	// instead of the server compiled into the hub, exactly as `make tilt
	// KCP_IMAGE=…` does: hack/kcp-external.sh starts it on the suite's kcp port
	// and the hub is pointed at its admin kubeconfig. The static-token users the
	// embedded server would have written into its token-auth-file are written
	// here first, so every test that talks to kcp directly as a tenant works
	// the same either way.
	kcpImage := strings.TrimSpace(os.Getenv("RAILGRID_E2E_KCP_IMAGE"))
	var kcpCmd *exec.Cmd
	hubArgs := []string{
		"--listen-addr", ":" + hubPort,
		"--data-dir", dataDir,
		"--static-auth-token", staticToken,
		"--static-auth-token", secondToken,
	}
	adminKubeconfig := filepath.Join(dataDir, "kcp", "admin.kubeconfig")
	if kcpImage == "" {
		hubArgs = append(hubArgs, "--embedded-kcp", "--kcp-bind-address", "127.0.0.1", "--kcp-secure-port", kcpPort)
	} else {
		kcpRoot := filepath.Join(dataDir, "kcp-external")
		if err := os.MkdirAll(kcpRoot, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "kcp root:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.Join(kcpRoot, "token-auth-file.csv"), []byte(staticTokenAuthFile(staticToken, secondToken)), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "token auth file:", err)
			os.Exit(1)
		}
		kcpLog, _ := os.Create(filepath.Join(dataDir, "kcp-external.log"))
		kcpCmd = exec.Command(filepath.Join(repoRoot, "hack", "kcp-external.sh"), kcpImage, kcpRoot, kcpPort, externalKCPContainer)
		if v := strings.TrimSpace(os.Getenv("RAILGRID_E2E_HUB_VERBOSITY")); v != "" {
			// The same knob raises the containerised kcp's log level.
			kcpCmd.Env = append(os.Environ(), "KCP_EXTERNAL_EXTRA_ARGS=--v="+v)
		}
		kcpCmd.Stdout = kcpLog
		kcpCmd.Stderr = kcpLog
		kcpCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := kcpCmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "start external kcp:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "external kcp started from %s (pid=%d, log=%s)\n", kcpImage, kcpCmd.Process.Pid, kcpLog.Name())
		adminKubeconfig = filepath.Join(kcpRoot, "admin.kubeconfig")
		if err := waitForRewrittenKubeconfig(adminKubeconfig, "https://localhost:"+kcpPort, 3*time.Minute); err != nil {
			killGroup(kcpCmd)
			stopExternalKCP()
			fmt.Fprintln(os.Stderr, "external kcp never ready:", err)
			os.Exit(1)
		}
		// The kubeconfig appears before kcp has written its bootstrap RBAC; a
		// hub started in that window is refused its first CRD install as
		// kcp-admin and exits. Gate on the exact call the hub makes first.
		if err := waitForKCPAdmin(adminKubeconfig, "https://127.0.0.1:"+kcpPort, 3*time.Minute); err != nil {
			killGroup(kcpCmd)
			stopExternalKCP()
			fmt.Fprintln(os.Stderr, "external kcp never authorized its admin:", err)
			os.Exit(1)
		}
		hubArgs = append(hubArgs, "--external-kcp-kubeconfig", adminKubeconfig)
	}

	hubLog, _ := os.Create(filepath.Join(dataDir, "hub.log"))
	hubCmd := exec.Command(filepath.Join(repoRoot, "bin", "railgrid-hub"), hubArgs...)
	// RAILGRID_E2E_HUB_VERBOSITY raises the hub's klog level (shared with the
	// embedded kcp). At 4 kcp's authorizer decorators log every step and its
	// reason, which is the only place a virtual-workspace denial explains
	// itself: the HTTP error a provider sees is anonymized to "access denied".
	if v := strings.TrimSpace(os.Getenv("RAILGRID_E2E_HUB_VERBOSITY")); v != "" {
		hubCmd.Args = append(hubCmd.Args, "--v="+v)
	}
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
		if kcpCmd != nil {
			killGroup(kcpCmd)
			stopExternalKCP()
		}
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
	tok, err := extractToken(adminKubeconfig)
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "extract admin token:", err)
		os.Exit(1)
	}
	adminToken = tok

	// Provisioning: Provider + CatalogEntry into root:railgrid:system:providers
	// (mirrors `make install-provider-quickstart`); the hub's Provider
	// controller materializes root:railgrid:providers:quickstart, the provider
	// SA, and the provider-token Secret.
	if err := applyQuickstartManifests(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "apply quickstart manifests:", err)
		os.Exit(1)
	}

	// Mint the SA runtime kubeconfig from the provider-token Secret (mirrors
	// `make init-provider-quickstart`) and run `quickstart-provider init` —
	// the APIExport/schemas/bind-grant come from init, not the hub.
	runtimeKubeconfig = filepath.Join(dataDir, "quickstart-runtime.kubeconfig")
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
	// The address a kcp SHARD reaches the provider at when it forwards a custom
	// subresource. Embedded kcp shares the host with the provider; a kcp run
	// from an image is a container, where "localhost" is the container itself.
	dataPlaneURL := "http://localhost:" + providerPort
	if kcpImage != "" {
		dataPlaneURL = "http://host.docker.internal:" + providerPort
	}
	initCmd := exec.Command(filepath.Join(repoRoot, "bin", "quickstart-provider"), "init")
	initCmd.Env = append(os.Environ(),
		"RAILGRID_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		"QUICKSTART_WORKSPACE_PATH="+workspacePath,
		// The greetings APIResourceSchema the chart ships — init reads the
		// schemas dir to author the APIExport's resources.
		"RAILGRID_KCP_DIR="+filepath.Join(repoRoot, "providers", "quickstart", "deploy", "chart", "files"),
		// The suite registers the CatalogEntry itself (provider_test.go, with
		// the :18081 backend URL), so init gets no manifest to read the
		// data-plane address from and is told it directly. It is what the
		// generated export's "<resource>/<verb>" entries resolve to.
		"RAILGRID_DATAPLANE_URL="+dataPlaneURL,
	)
	initCmd.Stdout = initLog
	initCmd.Stderr = initLog
	if err := initCmd.Run(); err != nil {
		tail := tailInitLog(initLog.Name(), 60)
		cleanup()
		fmt.Fprintf(os.Stderr, "quickstart init failed: %v (log: %s)\n%s\n", err, initLog.Name(), tail)
		os.Exit(1)
	}

	providerLogPath = filepath.Join(dataDir, "provider.log")
	provLog, err := os.Create(providerLogPath)
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "create provider.log:", err)
		os.Exit(1)
	}
	provCmd = exec.Command(filepath.Join(repoRoot, "bin", "quickstart-provider"))
	provCmd.Env = append(os.Environ(),
		"PORT="+providerPort,
		"RAILGRID_HUB_URL="+hubURL,
		"RAILGRID_HUB_TOKEN="+staticToken,
		"RAILGRID_PROVIDER_NAME=quickstart",
		// The same credential the chart mounts into the serve container: the
		// controller manager watches tenant workspaces with it, and the
		// data-plane verb acts as the provider through the same export
		// virtual workspace when its gate reviews and reads for a caller.
		"RAILGRID_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		"RAILGRID_KCP_DIR="+filepath.Join(repoRoot, "providers", "quickstart", "deploy", "chart", "files"),
		// The chart mounts the rendered CatalogEntry on the serve container as
		// RAILGRID_CATALOGENTRY_FILE; here the manifest itself plays that part.
		// serve derives its custom-subresource route table from it, so the
		// verbs it answers are exactly the ones declared — and REQUIRES it: a
		// verb has no other spelling, so without a manifest serve refuses to
		// start rather than come up with no data plane.
		"RAILGRID_CATALOGENTRY_FILE="+filepath.Join(repoRoot, "providers", "quickstart", "manifest.yaml"),
	)
	provCmd.Stdout = provLog
	provCmd.Stderr = provLog
	provCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := provCmd.Start(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "start provider:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "quickstart-provider started (pid=%d, port=:%s)\n", provCmd.Process.Pid, providerPort)

	if err := waitReady("http://127.0.0.1:"+providerPort+"/healthz", 30*time.Second); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "quickstart never ready:", err)
		os.Exit(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}

// mintRuntimeKubeconfig waits for the Provider controller to populate the
// provider-token Secret in the sub-workspace and writes a workspace-scoped
// kubeconfig around it — the same credential the provider pod mounts.
// externalKCPContainer names the docker container RAILGRID_E2E_KCP_IMAGE runs
// in — not the Tilt one, which hack/kcp-external.sh would otherwise replace.
const externalKCPContainer = "railgrid-kcp-external-e2e"

// staticTokenAuthFile is the token-auth-file the embedded server writes for its
// static tokens (pkg/hub/kcp/embedded.go), for an external kcp: same identity
// per token, so RBAC written for railgrid:static:<uid> matches on both.
func staticTokenAuthFile(tokens ...string) string {
	var b strings.Builder
	for _, token := range tokens {
		id := identity.NewStaticToken(token)
		fmt.Fprintf(&b, "%s,%s,%s,\"system:authenticated\"\n", token, id.RBACIdentity, id.UID)
	}
	return b.String()
}

// waitForRewrittenKubeconfig waits until hack/kcp-external.sh has written the
// admin kubeconfig AND rewritten its server to the published port: the file
// first appears with the container's own address.
func waitForRewrittenKubeconfig(path, server string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil && strings.Contains(string(raw), "server: "+server) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s did not appear with server %s within %s", path, server, timeout)
}

// waitForKCPAdmin polls until the admin token in kubeconfig can list CRDs in
// root — the first request the hub makes — so the hub never starts against a
// kcp that is up but has not finished bootstrapping its RBAC.
func waitForKCPAdmin(kubeconfig, server string, timeout time.Duration) error {
	token, err := extractToken(kubeconfig)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: 5 * time.Second} //nolint:gosec // dev cert
	deadline := time.Now().Add(timeout)
	last := "no attempt"
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, server+"/clusters/root/apis/apiextensions.k8s.io/v1/customresourcedefinitions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = resp.Status
		} else {
			last = err.Error()
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("kcp-admin could not list CRDs in root within %s (last: %s)", timeout, last)
}

// stopExternalKCP removes the image-run kcp container; `docker run --rm` on a
// killed script does not always get to.
func stopExternalKCP() {
	_ = exec.Command("docker", "rm", "-f", externalKCPContainer).Run()
}

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

// build runs `make build-hub build-quickstart-provider` so the test runs
// against current source even when the user hasn't built manually.
// build compiles the two binaries the suite spawns.
//
// RAILGRID_E2E_HUB_GOFLAGS, when set, is applied to the HUB build alone — for
// example "-modfile=/path/to/experiment/go.mod" to embed a kcp pull-request
// build instead of the pinned release. It must not reach the provider build:
// the provider is its own Go module, and a modfile written for the root module
// does not describe it.
func build(root string) error {
	hubFlags := strings.TrimSpace(os.Getenv("RAILGRID_E2E_HUB_GOFLAGS"))
	if hubFlags == "" {
		return runMake(root, nil, "build-hub", "build-quickstart-provider")
	}
	if err := runMake(root, []string{"GOFLAGS=" + hubFlags}, "build-hub"); err != nil {
		return err
	}
	return runMake(root, nil, "build-quickstart-provider")
}

func runMake(root string, extraEnv []string, targets ...string) error {
	cmd := exec.Command("make", append([]string{"-C", root}, targets...)...)
	cmd.Env = append(os.Environ(), extraEnv...)
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
