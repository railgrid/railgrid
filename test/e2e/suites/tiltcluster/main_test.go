/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package tiltcluster is an end-to-end suite that runs against the
// operator-deployed, multi-shard Tilt stack (`make tilt-cluster` /
// Tiltfile.cluster): kcp-operator + root/theseus shards + front-proxy +
// the in-cluster hub + the host-run providers (infrastructure, quickstart)
// + the kro runtime cluster.
//
// Unlike the standalone/provider suites, this one does NOT spin up its own
// processes — that topology is owned by Tiltfile.cluster. The suite assumes
// the stack is ALREADY up and connects to it:
//
//	Terminal 1:  make tilt-cluster        # bring the stack up, leave running
//	Terminal 2:  make e2e-tilt-cluster    # run this suite against it
//
// If the stack isn't reachable, every test SKIPs with a clear message (so a
// stray `go test ./...` stays green and `make test` — which excludes
// test/e2e — is unaffected). The `make e2e-tilt-cluster` target prechecks
// readiness and fails fast with guidance.
//
// Connection points (overridable via env for non-default setups):
//   - kcp admin            tilt-frontproxy.kubeconfig          (KEDGE_E2E_TILT_KUBECONFIG)
//   - hub REST + MCP        https://localhost:9443             (KEDGE_E2E_HUB_URL)
//   - infrastructure /mcp   http://localhost:8082              (KEDGE_E2E_INFRA_URL)
//   - hub static token      dev-token                          (KEDGE_E2E_STATIC_TOKEN)
package tiltcluster

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"testing"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Suite-shared state, populated by TestMain.
var (
	repoRoot     string
	frontproxyKC string // path to tilt-frontproxy.kubeconfig (kcp admin)
	hubURL       string // https://localhost:9443
	infraURL     string // http://localhost:8082
	staticToken  string // hub static-auth token (dev-token)

	kcpBaseHost string // front-proxy host with any /clusters/<x> suffix stripped
	kcpTLS      rest.TLSClientConfig

	// stackReady gates every test. When false the stack wasn't detected and
	// tests t.Skip rather than fail, so the suite is safe under `go test ./...`.
	stackReady bool
)

const (
	// providerName is the slug the infrastructure provider registers under;
	// the MCP aggregate federates its tools as "<providerName>__<tool>".
	providerName       = "infrastructure"
	providerWorkspace  = "root:kedge:providers:infrastructure"
	providersWorkspace = "root:kedge:providers"
	infraGroup         = "infrastructure.kedge.faros.sh"
	infraAPIExportName = "infrastructure.providers.kedge.faros.sh"
)

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	frontproxyKC = envOr("KEDGE_E2E_TILT_KUBECONFIG", filepath.Join(repoRoot, "tilt-frontproxy.kubeconfig"))
	hubURL = strings.TrimRight(envOr("KEDGE_E2E_HUB_URL", "https://localhost:9443"), "/")
	infraURL = strings.TrimRight(envOr("KEDGE_E2E_INFRA_URL", "http://localhost:8082"), "/")
	staticToken = envOr("KEDGE_E2E_STATIC_TOKEN", "dev-token")

	stackReady = detectStack()
	if !stackReady {
		fmt.Fprintf(os.Stderr,
			"\n[tiltcluster] stack not detected — tests will be SKIPPED.\n"+
				"  kubeconfig: %s\n  hub:        %s\n  provider:   %s\n"+
				"Bring it up first in another terminal:  make tilt-cluster\n\n",
			frontproxyKC, hubURL, infraURL)
	}

	os.Exit(m.Run())
}

// detectStack returns true only when the kcp admin kubeconfig is present and
// both the hub and infrastructure provider answer /healthz. It also captures
// the kcp front-proxy base host + TLS for the admin client.
func detectStack() bool {
	if _, err := os.Stat(frontproxyKC); err != nil {
		return false
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", frontproxyKC)
	if err != nil {
		return false
	}
	host, err := stripClusterSuffix(cfg.Host)
	if err != nil {
		return false
	}
	kcpBaseHost = host
	kcpTLS = cfg.TLSClientConfig

	if !httpOK(hubURL+"/healthz", 30*time.Second) {
		return false
	}
	if !httpOK(infraURL+"/healthz", 10*time.Second) {
		return false
	}
	return true
}

func requireStack(t *testing.T) {
	t.Helper()
	if !stackReady {
		t.Skip("tilt-cluster stack not up; run `make tilt-cluster` first")
	}
}

// kcpAdminDynamic returns an admin dynamic client scoped to clusterPath,
// reusing the front-proxy host + TLS + credentials from tilt-frontproxy.kubeconfig.
func kcpAdminDynamic(t *testing.T, clusterPath string) dynamic.Interface {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", frontproxyKC)
	if err != nil {
		t.Fatalf("load %s: %v", frontproxyKC, err)
	}
	cfg.Host = kcpBaseHost + "/clusters/" + clusterPath
	d, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("admin dynamic client for %s: %v", clusterPath, err)
	}
	return d
}

// --- small helpers ---------------------------------------------------------

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func insecureClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev self-signed certs
		},
	}
}

func httpOK(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	c := insecureClient(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := c.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// stripClusterSuffix turns "https://host:8443/clusters/root:foo" into
// "https://host:8443" so a different /clusters/<path> can be attached per call.
func stripClusterSuffix(host string) (string, error) {
	idx := strings.Index(host, "/clusters/")
	if idx < 0 {
		return strings.TrimRight(host, "/"), nil
	}
	return strings.TrimRight(host[:idx], "/"), nil
}
