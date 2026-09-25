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

package edgesconn

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/railgrid/pkg/apiurl"
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
// The far end is the suite's own probe backend (probe_backend_test.go): its
// identity route reports the headers and a fingerprint of the Authorization it
// was handed, and its stream route flushes chunks. Those three answers are
// exactly E-5 (passthrough auth, not substitution), E-6 (identity survives) and
// E-7 (streaming is not buffered). Nothing here is specific to a provider —
// the transport is what is under test, and a real provider on the far end would
// only add its own failure modes to the ones being measured.
//
// This exercises the transport with spec.host, which the host-run agent in this
// suite can dial. The hub-owned Services the catalog controller writes use
// spec.targetRef (cluster DNS); proving that shape needs the agent running as a
// pod inside the edge cluster, which this suite does not do yet.

var edgeServiceGVR = schema.GroupVersionResource{
	Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "services",
}

func TestBYOProviderBackendThroughTunnel(t *testing.T) {
	edgeName := "byo-server"
	workDir := suiteTempDir(t, "byo-provider")
	kubeconfig := filepath.Join(workDir, "railgrid.kubeconfig")

	// Same bring-up as the kubectl-through-tunnel case: a tenant with edges
	// enabled, an edge registered, and an agent connected to it.
	runCLI(t, kubeconfig, railgridBin, "login", "--hub-url", hubURL, "--insecure-skip-tls-verify", "--token", staticToken)
	tenantWS := clusterFromKubeconfig(t, kubeconfig)
	t.Logf("tenant workspace = %s", tenantWS)

	tenantAdmin := kcpDynamic(t, tenantWS, adminToken)
	enableEdges(t, tenantAdmin)
	grantEdgeProxy(t, tenantAdmin)

	runCLI(t, kubeconfig, railgridBin, "edge", "create", edgeName, "--type", "server")
	t.Cleanup(func() {
		_ = tenantAdmin.Resource(linuxServerGVR).Delete(context.Background(), edgeName, metav1.DeleteOptions{})
	})
	joinToken := waitForJoinToken(t, tenantAdmin, linuxServerGVR, edgeName)
	startAgent(t, edgeName, joinToken, tenantWS, "--type", "server")
	waitForConnected(t, tenantAdmin, linuxServerGVR, edgeName)

	// The backend a tenant would be self-hosting, standing in for a workload in
	// the tenant's cluster: a host-local HTTP server the agent can dial.
	probePort := startProbeBackend(t)

	// A LinuxServer edge, because the agent here runs as a host process and
	// spec.host is the shape that supports: for a KubernetesCluster edge the
	// agent is a pod, so the proxy requires spec.targetRef (cluster DNS) and
	// refuses host outright — loopback there would mean the agent pod itself.
	//
	// The transport properties under test are edge-kind-independent: the same
	// serviceHTTPProxy, the same revdial stream. auth=passthrough is the field
	// that matters — the default, "secret", substitutes a token and would break
	// the far end's entire authorization model.
	const svcName = "provider-probe"
	svc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "edges.railgrid.ai/v1alpha1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": svcName},
		"spec": map[string]any{
			"edgeRef": map[string]any{"kind": "LinuxServer", "name": edgeName},
			"host":    "127.0.0.1",
			"port":    probePort,
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

	// The Service's proxy verb is a kcp custom subresource on the edges
	// APIExport, addressed on the hub's kcp front door like any kube path.
	base := apiurl.EdgeServiceProxyURL(hubURL, tenantWS, svcName, "proxy")

	t.Run("identity and passthrough auth survive the tunnel", func(t *testing.T) {
		var seen probeIdentity
		body := getThroughTunnel(t, base+probeIdentityPath)
		if err := json.Unmarshal(body, &seen); err != nil {
			t.Fatalf("decode %s (%q): %v", probeIdentityPath, string(body), err)
		}
		t.Logf("probe backend saw: %+v", seen)

		if seen.Probe != probeName {
			t.Fatalf("reached something other than the probe backend: %q", seen.Probe)
		}
		// E-6: the far end authorizes the CALLER, so it has to learn who that is.
		if seen.UserHeader == "" {
			t.Error("X-Railgrid-User did not survive the tunnel; the provider cannot attribute the call")
		}
		if seen.TenantHeader == "" {
			t.Error("X-Railgrid-Tenant did not survive the tunnel; the provider cannot scope the call")
		}
		// E-5: the credential meant for the far end must arrive, not the
		// Service's. On the kube path the request's Authorization is the
		// caller's kcp credential and never reaches the provider, so the
		// far-end credential travels in X-Railgrid-Upstream-Authorization and
		// auth=passthrough forwards it as the upstream Authorization.
		// Presence or a length check would accept a substituted token, so
		// compare the fingerprint of exactly what we sent.
		if !seen.AuthorizationPresent {
			t.Error("no Authorization reached the far end; auth=passthrough dropped the upstream credential")
		}
		want := fingerprint(upstreamAuthorization)
		if seen.TokenFingerprint != want {
			t.Errorf("Authorization was not passed through: fingerprint %q, want %q (length seen %d) — "+
				"a substituted token means per-user RBAC collapsed into 'anyone who can reach the tunnel'",
				seen.TokenFingerprint, want, seen.TokenLength)
		}
	})

	t.Run("responses stream rather than buffer", func(t *testing.T) {
		// Read chunk-by-chunk and time each arrival. A buffered proxy delivers
		// everything at once at the end, which is indistinguishable from
		// streaming if you only compare the final body — the failure only shows
		// in WHEN bytes arrive.
		const chunks = 4
		url := fmt.Sprintf("%s%s?chunks=%d", base, probeStreamPath, chunks)
		req, err := http.NewRequestWithContext(ctxWithTimeout(t, 60*time.Second), http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("build stream request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+staticToken)
		req.Header.Set(dataplane.HeaderUpstreamAuthorization, upstreamAuthorization)
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

		if len(arrivals) < chunks {
			t.Fatalf("got %d chunks, want %d", len(arrivals), chunks)
		}
		// The backend spaces chunks probeChunkInterval apart. If anything
		// buffered, they all land together at the end; require the last to be
		// meaningfully later than the first.
		if spread := arrivals[len(arrivals)-1] - arrivals[0]; spread < 200*time.Millisecond {
			t.Errorf("all chunks arrived within %v — the response was buffered somewhere on the tunnel, "+
				"which turns log tailing into a hang", spread)
		}
	})
}

// getThroughTunnel issues one authenticated GET through the hub and returns the
// body, failing the test on any non-200.
// upstreamAuthorization is the credential the far-end backend is meant to see:
// what a caller puts in X-Railgrid-Upstream-Authorization, and what the hub's
// edge hop puts there for an org-owned provider (the delegated token). The
// request's own Authorization authenticates the caller to kcp and stops there.
const upstreamAuthorization = "Bearer far-end-credential-for-the-probe"

func getThroughTunnel(t *testing.T, url string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(ctxWithTimeout(t, 60*time.Second), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+staticToken)
	req.Header.Set(dataplane.HeaderUpstreamAuthorization, upstreamAuthorization)
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
