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

package tunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

// pipeAgent fakes the agent side of a locally held tunnel: every Dial hands
// back one end of a net.Pipe and the "agent" echoes everything it reads.
type pipeAgent struct{}

func (pipeAgent) Dial(ctx context.Context) (net.Conn, error) {
	local, remote := net.Pipe()
	go func() {
		_, _ = io.Copy(remote, remote) // echo
		_ = remote.Close()
	}()
	return local, nil
}

// relayServer runs the owner-side relay over a real TCP listener with a fake
// locally held tunnel, returning the relay address.
func relayServer(t *testing.T, s *Server) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.relayHandler()))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func newRelayTestServer(t *testing.T, token string) *Server {
	t.Helper()
	s, err := New(Config{
		Kinds: []KindConfig{{
			GVR:  schema.GroupVersionResource{Group: "edges.faros.sh", Version: "v1alpha1", Resource: "kubernetesclusters"},
			Kind: "KubernetesCluster",
		}},
		AgentPickupPath: "/services/providers/edges/agent/proxy",
		Logger:          klog.Background(),
	})
	if err != nil {
		t.Fatalf("tunnel server: %v", err)
	}
	s.relayToken = token
	return s
}

// The full remote path: a peer-held key resolves to a remoteDialer whose Dial
// relays through the owner and carries bytes both ways.
func TestRelayRoundTrip(t *testing.T) {
	owner := newRelayTestServer(t, "shared-token")

	// The relay only serves LOCALLY held tunnels; inject a live one by
	// storing a real revdial dialer is heavy, so go through the map with a
	// fake — the relay handler only needs Dial.
	const key = "kubernetesclusters/cl-1/edge-1"
	owner.edgeConnManager.storeLocalForTest(key, pipeAgent{})

	addr := relayServer(t, owner)

	d := &remoteDialer{addr: addr, key: key, token: "shared-token"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.Dial(ctx)
	if err != nil {
		t.Fatalf("remote dial: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	payload := []byte("kubectl get pods --through-the-relay")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
}

func TestRelayRefusesBadTokenAndUnknownKey(t *testing.T) {
	owner := newRelayTestServer(t, "shared-token")
	owner.edgeConnManager.storeLocalForTest("kubernetesclusters/cl-1/edge-1", pipeAgent{})
	addr := relayServer(t, owner)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bad := &remoteDialer{addr: addr, key: "kubernetesclusters/cl-1/edge-1", token: "wrong"}
	if _, err := bad.Dial(ctx); err == nil {
		t.Fatal("relay accepted a wrong bearer token")
	}
	missing := &remoteDialer{addr: addr, key: "kubernetesclusters/cl-1/edge-9", token: "shared-token"}
	if _, err := missing.Dial(ctx); err == nil {
		t.Fatal("relay accepted a key it does not hold")
	}
}

// Pickup routing: the local replica serves pickups itself; a peer-addressed
// pickup is forwarded to the peer's internal listener, an unknown replica 502s.
func TestPickupRouterDispatch(t *testing.T) {
	s := newRelayTestServer(t, "shared-token")
	s.replicaID = "replica-a"

	localHits := 0
	router := s.pickupRouter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localHits++
		w.WriteHeader(http.StatusTeapot) // sentinel
	}))

	// Without a registry every pickup is local (single-replica mode).
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proxy/replica-a?revdial.dialer=abc", nil))
	if localHits != 1 || rec.Code != http.StatusTeapot {
		t.Fatalf("self pickup: hits=%d code=%d, want local dispatch", localHits, rec.Code)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proxy/replica-b?revdial.dialer=abc", nil))
	if localHits != 2 {
		t.Fatal("registry-less router must serve every pickup locally")
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proxy/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty replica segment = %d, want 400", rec.Code)
	}
}

// storeLocalForTest lets tests install a fake locally held tunnel without a
// live revdial socket.
func (c *ConnManager) storeLocalForTest(key string, d Dialer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dials[key] = d
}
