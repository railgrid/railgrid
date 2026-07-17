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

// Package edges implements an end-to-end suite for the standalone edges
// provider (group edges.kedge.faros.sh, kinds KubernetesCluster + LinuxServer)
// that was extracted out of the hub core. It mirrors the provider suite: the
// kedge-hub runs with embedded kcp and the edges-provider runs as a host
// subprocess, following the current bootstrap flow — Provider + CatalogEntry
// applied into root:kedge:system:providers (the hub's Provider controller
// materializes the sub-workspace + SA + provider-token), then `edges-provider
// init` with the minted SA kubeconfig (APIExport + schemas + bind grant), then
// `edges-provider serve`.
//
// This suite covers the CONTROL-PLANE + AUTH surface that is testable without a
// live agent or edge target: catalog provisioning, tenant Enable via APIBinding,
// KubernetesCluster/LinuxServer CR lifecycle, and the edge-proxy authorization
// boundary through the hub backend proxy. The data-plane tunnel (kubectl/ssh
// streaming, edge Connected/Ready) needs a running agent + edge target and is
// out of scope for this embedded suite — see docs/edges-providers-testing.md.
//
// Runs without kind/Helm/Dex.
package edges

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
)

// Suite-shared state populated by TestMain.
var (
	repoRoot    string
	hubURL      string // http://127.0.0.1:<port>
	kcpServer   string // https://127.0.0.1:<port> (admin kubeconfig)
	adminToken  string // kcp admin token (from .kcp/admin.kubeconfig)
	staticToken = "test:user-default"
)

const (
	// Distinct from the provider suite's ports so a stray process from one
	// suite doesn't silently poison the other. Embedded etcd still binds the
	// fixed :2380, so the two embedded suites cannot run concurrently on one
	// host — in CI they are separate jobs.
	hubPort      = "19463"
	kcpPort      = "16463"
	providerPort = "18088"

	edgesWorkspacePath = "root:kedge:providers:edges"
	edgesAPIExportName = "edges.providers.kedge.faros.sh"
)

var secretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	hubURL = "http://127.0.0.1:" + hubPort
	kcpServer = "https://127.0.0.1:" + kcpPort

	for _, p := range []string{hubPort, kcpPort, providerPort, "2380"} {
		if portInUse(p) {
			fmt.Fprintf(os.Stderr, "port :%s already in use; run `pkill kedge-hub; pkill edges-provider` and retry\n", p)
			os.Exit(2)
		}
	}

	if err := build(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}

	dataDir, err := os.MkdirTemp("", "kedge-e2e-edges-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
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

	tok, err := extractToken(filepath.Join(dataDir, "kcp", "admin.kubeconfig"))
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "extract admin token:", err)
		os.Exit(1)
	}
	adminToken = tok

	// Provisioning: Provider + CatalogEntry into root:kedge:system:providers
	// (mirrors `make install-provider-edges`); the hub's Provider controller
	// materializes root:kedge:providers:edges + the provider SA + provider-token.
	if err := applyEdgesManifests(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "apply edges manifests:", err)
		os.Exit(1)
	}

	// Mint the SA runtime kubeconfig from the provider-token Secret (mirrors
	// `make init-provider-edges`), then run `edges-provider init` — the
	// APIExport/schemas/bind-grant come from init, not the hub.
	runtimeKubeconfig := filepath.Join(dataDir, "edges-runtime.kubeconfig")
	if err := mintRuntimeKubeconfig(runtimeKubeconfig, 2*time.Minute); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "mint runtime kubeconfig:", err)
		os.Exit(1)
	}

	initLog, _ := os.Create(filepath.Join(dataDir, "init.log"))
	initCmd := exec.Command(filepath.Join(repoRoot, "bin", "edges-provider"), "init")
	initCmd.Env = append(os.Environ(),
		"KEDGE_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		"EDGES_WORKSPACE_PATH="+edgesWorkspacePath,
		"KEDGE_SCHEMAS_DIR="+filepath.Join(repoRoot, "providers", "edges", "deploy", "chart", "files", "schemas"),
	)
	initCmd.Stdout = initLog
	initCmd.Stderr = initLog
	if err := initCmd.Run(); err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "edges init failed: %v (log: %s)\n", err, initLog.Name())
		os.Exit(1)
	}

	// Serve. Unlike the quickstart provider, the edges serve process needs the
	// runtime kubeconfig at serve time (the tunnel token validation + the edge
	// controller manager both read the provider's kcp credential).
	provLog, _ := os.Create(filepath.Join(dataDir, "provider.log"))
	provCmd = exec.Command(filepath.Join(repoRoot, "bin", "edges-provider"), "serve")
	provCmd.Env = append(os.Environ(),
		"PORT="+providerPort,
		"KEDGE_HUB_URL="+hubURL,
		"KEDGE_HUB_EXTERNAL_URL="+hubURL,
		"KEDGE_HUB_TOKEN="+staticToken,
		"KEDGE_HUB_INSECURE=true",
		"KEDGE_PROVIDER_NAME=edges",
		"KEDGE_PROVIDER_KUBECONFIG="+runtimeKubeconfig,
		"KEDGE_STATIC_TOKENS="+staticToken,
		"KEDGE_DEV_MODE=true",
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

// applyEdgesManifests applies provider.yaml (kind Provider) + manifest.yaml
// (kind CatalogEntry) into root:kedge:system:providers, mirroring `make
// install-provider-edges`. The CatalogEntry's ui/backend url is overridden to
// the test provider port. Retried until the API answers (the hub reports
// /readyz before those APIs are fully servable).
func applyEdgesManifests() error {
	cl, err := kcpDynamicRaw("root:kedge:system:providers", adminToken)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	gvrByKind := map[string]schema.GroupVersionResource{
		"Provider":     {Group: "admin.kedge.faros.sh", Version: "v1alpha1", Resource: "providers"},
		"CatalogEntry": {Group: "providers.kedge.faros.sh", Version: "v1alpha1", Resource: "catalogentries"},
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

// mintRuntimeKubeconfig waits for the Provider controller to populate the
// provider-token Secret in root:kedge:providers:edges and writes a
// workspace-scoped kubeconfig around it.
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
- name: kedge
  cluster:
    server: %s/clusters/%s
    insecure-skip-tls-verify: true
contexts:
- name: kedge
  context:
    cluster: kedge
    user: kedge
current-context: kedge
users:
- name: kedge
  user:
    token: %s
`, kcpServer, edgesWorkspacePath, token)
	return os.WriteFile(path, []byte(kc), 0o600)
}

// --- shared helpers (mirrors the provider suite) ---

func build(root string) error {
	cmd := exec.Command("make", "-C", root, "build-hub", "build-edges-provider")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
