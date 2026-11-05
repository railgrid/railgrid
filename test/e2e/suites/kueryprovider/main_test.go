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
// Enable through the Hub with SavedView CRUD plus the reconciler's Ready
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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
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
	hubPort                = "19493"
	kcpPort                = "16493"
	defaultPPort           = "18118"
	edgesStandInPath       = "root:railgrid:providers:edges"
	edgesStandInExportName = "edges.providers.railgrid.ai"
)

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	providerPort = defaultPPort
	hubURL = "http://127.0.0.1:" + hubPort
	kcpServer = "https://127.0.0.1:" + kcpPort
	etcdClientPort := os.Getenv("RAILGRID_E2E_ETCD_CLIENT_PORT")
	if etcdClientPort == "" {
		etcdClientPort = "2379"
	}
	etcdPeerPort := os.Getenv("RAILGRID_E2E_ETCD_PEER_PORT")
	if etcdPeerPort == "" {
		etcdPeerPort = "2380"
	}

	// Fail fast if a previous run left ports bound.
	for _, p := range []string{hubPort, kcpPort, providerPort, etcdClientPort, etcdPeerPort} {
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
		"--dev-mode",
		"--embedded-kcp",
		"--kcp-bind-address", "127.0.0.1",
		"--kcp-root-dir", filepath.Join(dataDir, "kcp"),
		"--kcp-etcd-client-port", etcdClientPort,
		"--kcp-etcd-peer-port", etcdPeerPort,
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

	// Kuery depends on edges. Register an API-only edges copy so this suite can
	// exercise the Hub's normal dependency-checked Enable flow without starting
	// an additional provider process or a kind cluster.
	if err := registerEdgesStandIn(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "register edges stand-in:", err)
		os.Exit(1)
	}

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
		// Where a kcp shard reaches this provider when it forwards a custom
		// subresource. The export declares "<resource>/<verb>" entries, so init
		// writes a DataPlaneEndpointSlice for them to resolve through and refuses
		// to guess the address.
		//
		// Given explicitly rather than read from the CatalogEntry: the committed
		// manifest targets the dev-loop port, and applyKueryManifests rewrites
		// spec.serving.backend.url to this same address when it registers the
		// entry, so the slice and the entry agree on where the provider is.
		"RAILGRID_DATAPLANE_URL=http://localhost:"+providerPort,
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

func registerEdgesStandIn() error {
	system, err := kcpDynamicRaw("root:railgrid:system:providers", adminToken)
	if err != nil {
		return fmt.Errorf("system providers client: %w", err)
	}
	providerGVR := schema.GroupVersionResource{Group: "admin.railgrid.ai", Version: "v1alpha1", Resource: "providers"}
	catalogEntryGVR := schema.GroupVersionResource{Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries"}
	provider := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admin.railgrid.ai/v1alpha1",
		"kind":       "Provider",
		"metadata":   map[string]any{"name": "edges"},
		"spec":       map[string]any{"displayName": "Edges (kuery e2e stand-in)"},
	}}
	if err := createKueryObjectWithRetry(system, providerGVR, provider, 90*time.Second); err != nil {
		return fmt.Errorf("create edges Provider: %w", err)
	}

	adminSub, err := waitForWorkspaceClient(edgesStandInPath, adminToken, 2*time.Minute)
	if err != nil {
		return err
	}
	providerToken, err := waitForProviderToken(adminSub, 2*time.Minute)
	if err != nil {
		return err
	}
	providerSub, err := kcpDynamicRaw(edgesStandInPath, providerToken)
	if err != nil {
		return fmt.Errorf("edges provider client: %w", err)
	}

	filesDir := filepath.Join(repoRoot, "providers", "edges", "deploy", "chart", "files")
	rawExport, err := os.ReadFile(filepath.Join(filesDir, "apiexport.yaml"))
	if err != nil {
		return fmt.Errorf("read edges APIExport: %w", err)
	}
	export := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(rawExport, &export.Object); err != nil {
		return fmt.Errorf("parse edges APIExport: %w", err)
	}
	if export.GetName() != edgesStandInExportName {
		return fmt.Errorf("edges APIExport is named %q, want %q", export.GetName(), edgesStandInExportName)
	}
	resources, _, _ := unstructured.NestedSlice(export.Object, "spec", "resources")
	var kept []any
	wantSchemas := map[string]bool{}
	for _, raw := range resources {
		resource, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, virtual, _ := unstructured.NestedMap(resource, "storage", "virtual"); virtual {
			continue
		}
		kept = append(kept, resource)
		if name, _ := resource["schema"].(string); name != "" {
			wantSchemas[name] = true
		}
	}
	if len(kept) == 0 {
		return fmt.Errorf("edges APIExport has no CRD-stored resources")
	}
	if err := unstructured.SetNestedSlice(export.Object, kept, "spec", "resources"); err != nil {
		return err
	}
	unstructured.RemoveNestedField(export.Object, "spec", "permissionClaims")

	schemaFiles, err := filepath.Glob(filepath.Join(filesDir, "schemas", "*.yaml"))
	if err != nil {
		return err
	}
	for _, filename := range schemaFiles {
		raw, err := os.ReadFile(filename)
		if err != nil {
			return fmt.Errorf("read edges schema %s: %w", filename, err)
		}
		resourceSchema := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(raw, &resourceSchema.Object); err != nil {
			return fmt.Errorf("parse edges schema %s: %w", filename, err)
		}
		if !wantSchemas[resourceSchema.GetName()] {
			continue
		}
		delete(wantSchemas, resourceSchema.GetName())
		if err := createKueryObjectWithRetry(providerSub, schemaGVR, resourceSchema, 60*time.Second); err != nil {
			return fmt.Errorf("create edges APIResourceSchema %s: %w", resourceSchema.GetName(), err)
		}
	}
	if len(wantSchemas) != 0 {
		return fmt.Errorf("edges APIExport names schemas that are not in chart files: %v", wantSchemas)
	}
	exportGVR := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	if err := createKueryObjectWithRetry(providerSub, exportGVR, export, 60*time.Second); err != nil {
		return fmt.Errorf("create edges APIExport: %w", err)
	}

	clusterRoleGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	clusterRoleBindingGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
	bindRoleName := "railgrid:providers:bind:" + edgesStandInExportName
	bindRole := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata":   map[string]any{"name": bindRoleName},
		"rules": []any{map[string]any{
			"apiGroups": []any{"apis.kcp.io"}, "resources": []any{"apiexports"},
			"verbs": []any{"bind"}, "resourceNames": []any{edgesStandInExportName},
		}},
	}}
	if err := createKueryObjectWithRetry(providerSub, clusterRoleGVR, bindRole, 60*time.Second); err != nil {
		return fmt.Errorf("create edges bind ClusterRole: %w", err)
	}
	bindGrant := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   map[string]any{"name": bindRoleName},
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": bindRoleName,
		},
		"subjects": []any{map[string]any{
			"apiGroup": "rbac.authorization.k8s.io", "kind": "Group", "name": "system:authenticated",
		}},
	}}
	if err := createKueryObjectWithRetry(providerSub, clusterRoleBindingGVR, bindGrant, 60*time.Second); err != nil {
		return fmt.Errorf("create edges bind ClusterRoleBinding: %w", err)
	}

	catalogEntry := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "providers.railgrid.ai/v1alpha1",
		"kind":       "CatalogEntry",
		"metadata":   map[string]any{"name": "edges"},
		"spec": map[string]any{
			"displayName": "Edges (kuery e2e stand-in)",
			"description": "API-only stand-in for kuery's declared dependency.",
			"vendor":      "railgrid", "version": "0.0.1",
			"export": map[string]any{"name": edgesStandInExportName},
		},
	}}
	if err := createKueryObjectWithRetry(system, catalogEntryGVR, catalogEntry, 90*time.Second); err != nil {
		return fmt.Errorf("create edges CatalogEntry: %w", err)
	}
	return nil
}

func waitForWorkspaceClient(path, token string, timeout time.Duration) (dynamic.Interface, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		client, err := kcpDynamicRaw(path, token)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err = client.Resource(schemaGVR).List(ctx, metav1.ListOptions{})
			cancel()
			if err == nil {
				return client, nil
			}
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("workspace %s never became usable: %w", path, lastErr)
}

func waitForProviderToken(client dynamic.Interface, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		secret, err := client.Resource(secretGVR).Namespace("default").Get(ctx, "provider-token", metav1.GetOptions{})
		cancel()
		if err == nil {
			encoded, _, _ := unstructured.NestedString(secret.Object, "data", "token")
			if encoded != "" {
				token, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					return "", fmt.Errorf("decode edges provider-token: %w", err)
				}
				return string(token), nil
			}
			lastErr = fmt.Errorf("provider-token exists but is not populated")
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("edges provider-token never populated: %w", lastErr)
}

func createKueryObjectWithRetry(client dynamic.Interface, resource schema.GroupVersionResource, object *unstructured.Unstructured, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := client.Resource(resource).Create(ctx, object, metav1.CreateOptions{})
		cancel()
		if err == nil || apierrors.IsAlreadyExists(err) {
			return nil
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	return fmt.Errorf("create %s/%s: %w", resource.Resource, object.GetName(), lastErr)
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
