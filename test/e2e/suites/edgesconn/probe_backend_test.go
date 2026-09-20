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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// The far end of the tunnel for the transport assertions in
// byo_provider_test.go.
//
// What E-5/E-6/E-7 need at the far end is an HTTP server that reports what it
// received and one that writes in flushed pieces. That is a measuring
// instrument, not a provider: it exists to observe the hop, it asserts nothing
// about how a provider should behave, and its routes are shaped by what the
// test has to read rather than by the provider contract.
//
// It used to be the quickstart provider, reached at /api/hello and /api/stream.
// That coupled the transport suite to routes the reference provider only
// carried on the suite's behalf — and the contract says a provider serves
// verbs, not an echo endpoint (providers/quickstart/README.md, pillar 2). The
// suite now brings its own backend, so the properties under test stay proven
// and no provider has to keep a route alive for a test it does not know about.
//
// Running in-process also removes a build of another Go module (and its
// embedded portal bundle) from this test's critical path; what is under test
// is the hub → edges-provider → revdial → backend path, and any HTTP server on
// the far end exercises it identically.
const (
	// A fixed port, not an ephemeral one, so a stray listener fails the test
	// with "port in use" up front rather than as a confusing tunnel error —
	// and so the `make e2e-edges-connectivity` precheck can name it.
	probePort = "18099"

	probeIdentityPath = "/probe/identity"
	probeStreamPath   = "/probe/stream"

	// Chunk spacing on probeStreamPath. The streaming assertion compares
	// arrival times against it, so the two have to agree.
	probeChunkInterval = 150 * time.Millisecond
)

// probeName is echoed on the identity route so the test can tell it reached
// THIS server. Without it a 200 from anything at all on the other end of the
// tunnel — including an error page rendered by a hop in between — could be
// mistaken for a successful round trip.
const probeName = "railgrid-e2e-probe"

// probeIdentity is what the backend saw. It carries no token: this is echoed
// back over the very hop it describes.
type probeIdentity struct {
	Probe    string    `json:"probe"`
	ServedAt time.Time `json:"servedAt"`

	// The identity the hub injects and the agent must not disturb. All three
	// come from the hub's own resolvers, never from the caller (E-6);
	// tenant and cluster both carry the tenant workspace's kcp logical-cluster
	// ID, which is how the hub names a tenant.
	UserHeader    string `json:"userHeader,omitempty"`
	TenantHeader  string `json:"tenantHeader,omitempty"`
	ClusterHeader string `json:"clusterHeader,omitempty"`

	// AuthorizationPresent and TokenLength say a credential arrived; neither
	// can say WHOSE, which is the whole question on this path — a Service left
	// at auth=secret substitutes its own token and a presence or length check
	// still passes. TokenFingerprint is the first 12 hex characters of SHA-256
	// over the whole Authorization header value, so a caller that knows what
	// it sent can prove the bytes arrived unchanged (E-5).
	AuthorizationPresent bool   `json:"authorizationPresent"`
	TokenLength          int    `json:"tokenLength,omitempty"`
	TokenFingerprint     string `json:"tokenFingerprint,omitempty"`
}

// startProbeBackend runs the probe on probePort and returns its port as the
// edges Service expects it. It listens on 127.0.0.1, which is what the
// host-run agent in this suite dials.
func startProbeBackend(t *testing.T) int64 {
	t.Helper()
	if portInUse(probePort) {
		t.Fatalf("port :%s already in use; stop the stray process and retry", probePort)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc(probeIdentityPath, func(w http.ResponseWriter, r *http.Request) {
		resp := probeIdentity{
			Probe:         probeName,
			ServedAt:      time.Now().UTC(),
			UserHeader:    r.Header.Get("X-Railgrid-User"),
			TenantHeader:  r.Header.Get("X-Railgrid-Tenant"),
			ClusterHeader: r.Header.Get("X-Railgrid-Cluster"),
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			resp.AuthorizationPresent = true
			resp.TokenLength = len(auth)
			resp.TokenFingerprint = fingerprint(auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Numbered chunks with an explicit flush between each, so a caller can
	// tell a streamed response from a buffered one by WHEN bytes arrive
	// rather than only by what they contain. A proxy that buffers turns "tail
	// my logs" into "hang until the process exits" (E-7).
	mux.HandleFunc(probeStreamPath, func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		chunks := 3
		if n := r.URL.Query().Get("chunks"); n != "" {
			if parsed, err := strconv.Atoi(n); err == nil && parsed > 0 && parsed <= 100 {
				chunks = parsed
			}
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		for i := 1; i <= chunks; i++ {
			_, _ = fmt.Fprintf(w, "chunk %d\n", i)
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(probeChunkInterval):
			}
		}
	})

	srv := httptest.NewUnstartedServer(mux)
	// httptest picks an ephemeral port; swap in the fixed one before Start.
	_ = srv.Listener.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:"+probePort)
	if err != nil {
		t.Fatalf("listen on :%s: %v", probePort, err)
	}
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	t.Logf("probe backend listening at %s", srv.URL)

	// Prove it serves before the tunnel is pointed at it, so a failure here
	// reads as "the probe did not start" rather than a tunnel error.
	if !waitFor(t, 15*time.Second, func() (bool, string) {
		resp, err := http.Get(srv.URL + "/healthz") //nolint:noctx // short-lived readiness probe
		if err != nil {
			return false, err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusOK, fmt.Sprintf("status %d", resp.StatusCode)
	}) {
		t.Fatal("probe backend never became healthy")
	}

	port, err := strconv.ParseInt(probePort, 10, 32)
	if err != nil {
		t.Fatalf("parse probe port %q: %v", probePort, err)
	}
	return port
}

// fingerprint is the backend's tokenFingerprint, called from both sides: the
// probe hashes what it received, the test hashes what it sent, and the
// assertion is that the two agree.
func fingerprint(authorization string) string {
	if authorization == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(authorization))
	return hex.EncodeToString(sum[:])[:12]
}
