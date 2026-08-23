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

package edgesconn

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The BYO-provider data plane, proven against a real tunnel.
//
// A self-hosted provider runs in a tenant's own cluster, so the hub reaches its
// backend over the edge agent's reverse tunnel rather than by dialling a URL
// (docs/byo-provider-edge-transport.md). The hub's half of that is unit-tested;
// what unit tests cannot reach is what the far end actually RECEIVES after the
// revdial hop. This suite can, because it runs a real hub, a real edges
// provider, a real agent and a real edge.
//
// The far end here is the quickstart provider, whose /api/hello reports the
// identity headers and a fingerprint of the Authorization it was given, and
// whose /api/stream flushes chunks. Those three answers are exactly E-5
// (passthrough auth, not substitution), E-6 (identity survives) and E-7
// (streaming is not buffered).
//
// This exercises the transport with spec.host, which the host-run agent in this
// suite can dial. The hub-owned Services the catalog controller writes use
// spec.targetRef (cluster DNS); proving that shape needs the agent running as a
// pod inside the edge cluster, which this suite does not do yet.

var edgeServiceGVR = schema.GroupVersionResource{
	Group: "edges.faros.sh", Version: "v1alpha1", Resource: "services",
}

const quickstartPort = "18099"

func TestBYOProviderBackendThroughTunnel(t *testing.T) {
	if portInUse(quickstartPort) {
		t.Fatalf("port :%s already in use; stop the stray process and retry", quickstartPort)
	}

	edgeName := "byo-server"
	workDir := suiteTempDir(t, "byo-provider")
	kubeconfig := filepath.Join(workDir, "faros.kubeconfig")

	// Same bring-up as the kubectl-through-tunnel case: a tenant with edges
	// enabled, an edge registered, and an agent connected to it.
	runCLI(t, kubeconfig, farosBin, "login", "--hub-url", hubURL, "--insecure-skip-tls-verify", "--token", staticToken)
	tenantWS := clusterFromKubeconfig(t, kubeconfig)
	t.Logf("tenant workspace = %s", tenantWS)

	tenantAdmin := kcpDynamic(t, tenantWS, adminToken)
	enableEdges(t, tenantAdmin)
	grantEdgeProxy(t, tenantAdmin)

	runCLI(t, kubeconfig, farosBin, "edge", "create", edgeName, "--type", "server")
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(linuxServerGVR).Delete(context.Background(), edgeName, metav1.DeleteOptions{})
	})
	joinToken := waitForJoinToken(t, tenantAdmin, linuxServerGVR, edgeName)
	startAgent(t, edgeName, joinToken, tenantWS, "--type", "server")
	waitForConnected(t, tenantAdmin, linuxServerGVR, edgeName)

	// The provider a tenant would be self-hosting. Run as a host process the
	// agent can dial, standing in for a workload in the tenant's cluster.
	startQuickstart(t)

	// A LinuxServer edge, because the agent here runs as a host process and
	// spec.host is the shape that supports: for a KubernetesCluster edge the
	// agent is a pod, so the proxy requires spec.targetRef (cluster DNS) and
	// refuses host outright — loopback there would mean the agent pod itself.
	//
	// The transport properties under test are edge-kind-independent: the same
	// serviceHTTPProxy, the same revdial stream. auth=passthrough is the field
	// that matters — the default, "secret", substitutes a token and would break
	// the far end's entire authorization model.
	const svcName = "provider-quickstart"
	svc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "edges.faros.sh/v1alpha1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": svcName},
		"spec": map[string]any{
			"edgeRef": map[string]any{"kind": "LinuxServer", "name": edgeName},
			"host":    "127.0.0.1",
			"port":    int64(mustAtoi(t, quickstartPort)),
			"scheme":  "http",
			"type":    "generic",
			"auth":    "passthrough",
		},
	}}
	if _, err := tenantAdmin.Resource(edgeServiceGVR).Create(ctxWithTimeout(t, 15*time.Second), svc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create edges Service: %v", err)
	}
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(edgeServiceGVR).Delete(context.Background(), svcName, metav1.DeleteOptions{})
	})

	base := fmt.Sprintf("%s/services/providers/edges/edgeproxy/clusters/%s/apis/edges.faros.sh/v1alpha1/services/%s/proxy",
		hubURL, tenantWS, svcName)

	t.Run("identity and passthrough auth survive the tunnel", func(t *testing.T) {
		var hello struct {
			Provider         string `json:"provider"`
			UserHeader       string `json:"userHeader"`
			TenantHeader     string `json:"tenantHeader"`
			TokenFingerprint string `json:"tokenFingerprint"`
			TokenLength      int    `json:"tokenLength"`
		}
		body := getThroughTunnel(t, base+"/api/hello")
		if err := json.Unmarshal(body, &hello); err != nil {
			t.Fatalf("decode /api/hello (%q): %v", string(body), err)
		}
		t.Logf("quickstart saw: %+v", hello)

		if hello.Provider != "quickstart" {
			t.Fatalf("reached something other than the quickstart provider: %q", hello.Provider)
		}
		// E-6: the far end authorizes the CALLER, so it has to learn who that is.
		if hello.UserHeader == "" {
			t.Error("X-Faros-User did not survive the tunnel; the provider cannot attribute the call")
		}
		if hello.TenantHeader == "" {
			t.Error("X-Faros-Tenant did not survive the tunnel; the provider cannot scope the call")
		}
		// E-5: the caller's own bearer must arrive, not the Service's. A length
		// check would accept a substituted token of the same size, so compare
		// the fingerprint of exactly what we sent.
		want := fingerprint("Bearer " + staticToken)
		if hello.TokenFingerprint != want {
			t.Errorf("Authorization was not passed through: fingerprint %q, want %q (length seen %d) — "+
				"a substituted token means per-user RBAC collapsed into 'anyone who can reach the tunnel'",
				hello.TokenFingerprint, want, hello.TokenLength)
		}
	})

	t.Run("responses stream rather than buffer", func(t *testing.T) {
		// Read chunk-by-chunk and time each arrival. A buffered proxy delivers
		// everything at once at the end, which is indistinguishable from
		// streaming if you only compare the final body — the failure only shows
		// in WHEN bytes arrive.
		req, err := http.NewRequestWithContext(ctxWithTimeout(t, 60*time.Second), http.MethodGet, base+"/api/stream?chunks=4", nil)
		if err != nil {
			t.Fatalf("build stream request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+staticToken)
		resp, err := insecureClient(90 * time.Second).Do(req)
		if err != nil {
			t.Fatalf("stream request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream status = %d", resp.StatusCode)
		}

		start := time.Now()
		var arrivals []time.Duration
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "" {
				continue
			}
			arrivals = append(arrivals, time.Since(start))
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("reading stream: %v", err)
		}
		t.Logf("chunk arrivals: %v", arrivals)

		if len(arrivals) < 4 {
			t.Fatalf("got %d chunks, want 4", len(arrivals))
		}
		// The provider spaces chunks 150ms apart. If anything buffered, they
		// all land together at the end; require the last to be meaningfully
		// later than the first.
		if spread := arrivals[len(arrivals)-1] - arrivals[0]; spread < 200*time.Millisecond {
			t.Errorf("all chunks arrived within %v — the response was buffered somewhere on the tunnel, "+
				"which turns log tailing into a hang", spread)
		}
	})
}

// startQuickstart builds and runs the quickstart provider as a host process.
// It is its own Go module, so it cannot be imported — running the real binary
// is also closer to what a self-hoster deploys.
func startQuickstart(t *testing.T) {
	t.Helper()
	// Via the Makefile, not a bare `go build`: the provider embeds its portal
	// with //go:embed all:portal/dist, and that directory is a build artifact
	// rather than something in git. A clean checkout has no dist, so compiling
	// directly fails — which is invisible on a dev machine that still has one
	// lying around from an earlier build, and fails only in CI.
	build := exec.Command("make", "build-quickstart-provider")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build quickstart provider: %v\n%s", err, out)
	}
	bin := filepath.Join(repoRoot, "bin", "quickstart-provider")

	logDir := suiteTempDir(t, "quickstart-provider")
	logf, _ := os.Create(filepath.Join(logDir, "quickstart.log"))
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), "PORT="+quickstartPort)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start quickstart provider: %v", err)
	}
	t.Cleanup(func() { killGroup(cmd) })

	// Wait for it to serve before the tunnel is pointed at it, so a failure
	// here reads as "the provider did not start" rather than a tunnel error.
	if !waitFor(t, 30*time.Second, func() (bool, string) {
		resp, err := http.Get("http://127.0.0.1:" + quickstartPort + "/healthz") //nolint:noctx // short-lived readiness probe
		if err != nil {
			return false, err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusOK, fmt.Sprintf("status %d", resp.StatusCode)
	}) {
		t.Fatalf("quickstart provider never became healthy (log=%s)", logf.Name())
	}
}

// getThroughTunnel issues one authenticated GET through the hub and returns the
// body, failing the test on any non-200.
func getThroughTunnel(t *testing.T, url string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(ctxWithTimeout(t, 60*time.Second), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+staticToken)
	resp, err := insecureClient(90 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := make([]byte, 0, 4096)
	buf := make([]byte, 1024)
	for {
		n, rerr := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", url, resp.StatusCode, string(body))
	}
	return body
}

// fingerprint mirrors the quickstart provider's tokenFingerprint. Duplicated
// rather than imported because the provider is a separate Go module; the
// provider's own test pins the algorithm on its side.
func fingerprint(authorization string) string {
	sum := sha256.Sum256([]byte(authorization))
	return hex.EncodeToString(sum[:])[:12]
}

func mustAtoi(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}
