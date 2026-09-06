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

// Package edgesconn is the edges DATA-PLANE connectivity suite: it drives a
// real agent through the reverse tunnel and proves kubectl streams down it.
//
// Unlike the embedded edges suite (which covers the control-plane + auth
// surface without an agent), this suite needs a live edge target, so it stands
// up a kind cluster as the KubernetesCluster agent's backing cluster. It runs
// the faros-hub with embedded kcp over HTTPS + the edges-provider (init then
// serve) as host subprocesses, mirroring `make run-hub-embedded-static` +
// `make run-provider-edges`, then:
//
//   - enables edges in a tenant workspace (APIBinding) + the edge-proxy grant
//     the hub REST /enable path would create (EnsureProviderEdgeProxyGrant),
//   - registers a KubernetesCluster, runs `faros agent run` against the kind
//     cluster, waits for the tunnel to come up, and
//   - fetches the edge kubeconfig and runs `kubectl get nodes` through the
//     tunnel.
//
// Requires kind + docker on PATH (the CI job installs kind).
package edgesconn

import (
	"bytes"
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
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	"github.com/faroshq/faros/test/e2e/framework"
)

// Suite-shared state populated by TestMain.
var (
	repoRoot     string
	hubURL       string // https://127.0.0.1:<port>
	kcpServer    string // https://127.0.0.1:<port> (admin kubeconfig)
	adminToken   string
	farosBin     string
	suiteDataDir string
	staticToken  = "dev-token"
)

const (
	// Ports distinct from the other embedded-kcp suites (provider 19443/16443,
	// infra 19453/16453, edges 19463/16463). Embedded etcd still binds :2380,
	// so this suite cannot run concurrently with those — it lives in its own
	// kind-enabled CI job.
	hubPort      = "19473"
	kcpPort      = "16473"
	providerPort = "18098"

	edgesWorkspacePath = "root:faros:providers:edges"
	edgesAPIExportName = "edges.providers.faros.sh"
)

var secretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	hubURL = "https://127.0.0.1:" + hubPort
	kcpServer = "https://127.0.0.1:" + kcpPort

	for _, p := range []string{hubPort, kcpPort, providerPort, "2380"} {
		if portInUse(p) {
			fmt.Fprintf(os.Stderr, "port :%s already in use; stop stray faros-hub/edges-provider and retry\n", p)
			os.Exit(2)
		}
	}

	if err := build(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}
	farosBin = filepath.Join(repoRoot, "bin", "faros")

	dataDir, err := os.MkdirTemp("", "faros-e2e-edgesconn-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	suiteDataDir = dataDir
	keepData := os.Getenv("FAROS_E2E_KEEP_DATA") == "true"
	artifactDir := os.Getenv("FAROS_E2E_ARTIFACT_DIR")

	hubLog, _ := os.Create(filepath.Join(dataDir, "hub.log"))
	hubCmd := exec.Command(filepath.Join(repoRoot, "bin", "faros-hub"),
		"--serving-cert-file", filepath.Join(repoRoot, "certs", "apiserver.crt"),
		"--serving-key-file", filepath.Join(repoRoot, "certs", "apiserver.key"),
		"--hub-external-url", hubURL,
		"--dev-mode", "-v", "4",
		"--static-auth-token", staticToken,
		"--embedded-kcp",
		"--kcp-bind-address", "127.0.0.1",
		"--kcp-root-dir", filepath.Join(dataDir, "kcp"),
		"--kcp-secure-port", kcpPort,
		"--listen-addr", ":"+hubPort,
		"--data-dir", dataDir,
	)
	hubCmd.Stdout = hubLog
	hubCmd.Stderr = hubLog
	hubCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := hubCmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start hub:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "hub started (pid=%d, log=%s)\n", hubCmd.Process.Pid, hubLog.Name())

	var initLog, provLog *os.File
	var provCmd *exec.Cmd
	cleanup := func() {
		killGroup(hubCmd)
		killGroup(provCmd)
		_ = hubLog.Close()
		if initLog != nil {
			_ = initLog.Close()
		}
		if provLog != nil {
			_ = provLog.Close()
		}
		if artifactDir != "" {
			if err := copyDir(dataDir, artifactDir); err != nil {
				fmt.Fprintf(os.Stderr, "copy e2e artifacts: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "e2e artifacts copied to %s\n", artifactDir)
			}
		}
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

	tok, err := extractToken(filepath.Join(dataDir, "kcp", "admin.kubeconfig"))
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "extract admin token:", err)
		os.Exit(1)
	}
	adminToken = tok

	// Embedded kcp can report /readyz before the tenancy APIBinding has settled.
	// Gate every test's first CLI login on the shared readiness check so a
	// transient "failed to create user" 500 is not attributed to connectivity.
	tenantClient := framework.NewFarosClient(repoRoot, filepath.Join(dataDir, "tenant-api.kubeconfig"), hubURL)
	apiCtx, apiCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	if err := framework.WaitForTenantAPI(apiCtx, tenantClient, hubURL, staticToken); err != nil {
		apiCancel()
		cleanup()
		fmt.Fprintln(os.Stderr, "tenant API never ready:", err)
		os.Exit(1)
	}
	apiCancel()

	if err := applyEdgesManifests(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "apply edges manifests:", err)
		os.Exit(1)
	}

	runtimeKubeconfig := filepath.Join(dataDir, "edges-runtime.kubeconfig")
	if err := mintRuntimeKubeconfig(runtimeKubeconfig, 2*time.Minute); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "mint runtime kubeconfig:", err)
		os.Exit(1)
	}

	initLog, _ = os.Create(filepath.Join(dataDir, "init.log"))
	initCmd := exec.Command(filepath.Join(repoRoot, "bin", "edges-provider"), "init")
	initCmd.Env = append(os.Environ(),
		"FAROS_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		"EDGES_WORKSPACE_PATH="+edgesWorkspacePath,
		"FAROS_SCHEMAS_DIR="+filepath.Join(repoRoot, "providers", "edges", "deploy", "chart", "files", "schemas"),
	)
	initCmd.Stdout = initLog
	initCmd.Stderr = initLog
	if err := initCmd.Run(); err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "edges init failed: %v (log: %s)\n", err, initLog.Name())
		os.Exit(1)
	}

	provLog, _ = os.Create(filepath.Join(dataDir, "provider.log"))
	provCmd = exec.Command(filepath.Join(repoRoot, "bin", "edges-provider"), "serve")
	provCmd.Env = append(os.Environ(),
		"PORT="+providerPort,
		"FAROS_HUB_URL="+hubURL,
		"FAROS_HUB_EXTERNAL_URL="+hubURL,
		"FAROS_HUB_TOKEN="+staticToken,
		"FAROS_HUB_INSECURE=true",
		"FAROS_PROVIDER_NAME=edges",
		"FAROS_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		"FAROS_DEV_MODE=true",
	)
	provCmd.Stdout = provLog
	provCmd.Stderr = provLog
	provCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := provCmd.Start(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "start provider:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "edges-provider started (pid=%d, port=:%s)\n", provCmd.Process.Pid, providerPort)

	if err := waitReady("http://127.0.0.1:"+providerPort+"/healthz", 30*time.Second); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "edges provider never ready:", err)
		os.Exit(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}

func applyEdgesManifests() error {
	cl, err := kcpDynamicRaw("root:faros:system:providers", adminToken)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	gvrByKind := map[string]schema.GroupVersionResource{
		"Provider":     {Group: "admin.faros.sh", Version: "v1alpha1", Resource: "providers"},
		"CatalogEntry": {Group: "providers.faros.sh", Version: "v1alpha1", Resource: "catalogentries"},
	}
	overrideURL := "http://localhost:" + providerPort
	for _, file := range []string{"provider.yaml", "manifest.yaml"} {
		raw, err := os.ReadFile(filepath.Join(repoRoot, "providers", "edges", file))
		if err != nil {
			return fmt.Errorf("read %s: %w", file, err)
		}
		for _, doc := range bytes.Split(raw, []byte("\n---")) {
			if !bytes.Contains(doc, []byte("apiVersion:")) {
				continue
			}
			obj := &unstructured.Unstructured{}
			if err := yaml.Unmarshal(doc, &obj.Object); err != nil {
				return fmt.Errorf("parse %s: %w", file, err)
			}
			if obj.GetKind() == "" {
				continue
			}
			gvr, ok := gvrByKind[obj.GetKind()]
			if !ok {
				return fmt.Errorf("%s: unexpected kind %q", file, obj.GetKind())
			}
			if obj.GetKind() == "CatalogEntry" {
				_ = unstructured.SetNestedField(obj.Object, overrideURL, "spec", "ui", "url")
				_ = unstructured.SetNestedField(obj.Object, overrideURL, "spec", "backend", "url")
			}
			deadline := time.Now().Add(90 * time.Second)
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				_, err = cl.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
				cancel()
				if err == nil || apierrors.IsAlreadyExists(err) {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("create %s %s: %w", obj.GetKind(), obj.GetName(), err)
				}
				time.Sleep(2 * time.Second)
			}
		}
	}
	return nil
}

func mintRuntimeKubeconfig(path string, timeout time.Duration) error {
	cl, err := kcpDynamicRaw(edgesWorkspacePath, adminToken)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	var token, lastErr string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		sec, err := cl.Resource(secretGVR).Namespace("default").Get(ctx, "provider-token", metav1.GetOptions{})
		cancel()
		if err != nil {
			lastErr = err.Error()
		} else if enc, _, _ := unstructured.NestedString(sec.Object, "data", "token"); enc != "" {
			raw, derr := base64.StdEncoding.DecodeString(enc)
			if derr != nil {
				return fmt.Errorf("decode provider-token: %w", derr)
			}
			token = string(raw)
			break
		} else {
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
- name: faros
  cluster:
    server: %s/clusters/%s
    insecure-skip-tls-verify: true
contexts:
- name: faros
  context:
    cluster: faros
    user: faros
current-context: faros
users:
- name: faros
  user:
    token: %s
`, kcpServer, edgesWorkspacePath, token)
	return os.WriteFile(path, []byte(kc), 0o600)
}

// --- shared helpers ---

func build(root string) error {
	cmd := exec.Command("make", "-C", root, "build-hub", "build-edges-provider", "build-faros", "certs")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// suiteTempDir keeps test workdirs and their kubeconfigs below the data
// directory copied by TestMain on failure. t.TempDir would remove those files
// before TestMain can preserve them for CI artifacts.
func suiteTempDir(t *testing.T, pattern string) string {
	t.Helper()
	dir, err := os.MkdirTemp(suiteDataDir, pattern+"-")
	if err != nil {
		t.Fatalf("create suite temp dir: %v", err)
	}
	return dir
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		// The embedded kcp state can be large and is not needed to diagnose a
		// failed suite; hub/provider/agent logs and kubeconfigs are the useful
		// artifacts. Keep the copy bounded for CI uploads.
		if rel == "kcp" && info.IsDir() {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to copy symlink %s", path)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close() //nolint:errcheck // best-effort artifact copy
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func kcpDynamicRaw(clusterPath, token string) (dynamic.Interface, error) {
	return dynamic.NewForConfig(&rest.Config{
		Host:            kcpServer + "/clusters/" + clusterPath,
		BearerToken:     token,
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	})
}

func kcpDynamic(t *testing.T, clusterPath, token string) dynamic.Interface {
	t.Helper()
	c, err := kcpDynamicRaw(clusterPath, token)
	if err != nil {
		t.Fatalf("dynamic client for %s: %v", clusterPath, err)
	}
	return c
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
	client := insecureClient(2 * time.Second)
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
