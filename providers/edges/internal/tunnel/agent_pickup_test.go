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

package tunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/railgrid/provider-sdk/revdial"
)

// Exercise the production dialer and real revdial control protocol, then send
// its advertised pickup through another replica to the owner's internal handler.
// Successful bidirectional bytes prove the random dialer capability survived
// alongside the scope; parsing a hand-built URL would miss that integration.
func TestScopedAgentPickupWireThroughPeer(t *testing.T) {
	const cluster = "consumer-cluster"
	const resource = "kubernetesclusters"
	const edge = "edge.with-dots"
	const bearer = "Bearer issued-agent-token"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	owner := newRelayTestServer(t, "provider-peer-token")
	peer := newRelayTestServer(t, "provider-peer-token")
	type receivedPickup struct{ path, rawQuery, authorization string }
	received := make(chan receivedPickup, 1)
	internal := owner.InternalHandler()
	ownerHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- receivedPickup{r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")}
		internal.ServeHTTP(w, r)
	}))
	defer ownerHTTP.Close()
	cs := kubefake.NewClientset()
	ownerRegistry := testRegistry("owner-replica", ownerHTTP.Listener.Addr().String(), cs, time.Now)
	peerRegistry := testRegistry("peer-replica", "unused-peer-address", cs, time.Now)
	owner.EnableReplicaRouting(ownerRegistry, "provider-peer-token")
	peer.EnableReplicaRouting(peerRegistry, "provider-peer-token")
	ownerRegistry.RenewOwned(ctx, nil)
	peerHTTP := httptest.NewServer(http.StripPrefix("/services/providers/edges", peer.pickupRouter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("pickup incorrectly dispatched to non-owning replica")
		w.WriteHeader(http.StatusTeapot)
	}))))
	defer peerHTTP.Close()
	control, agentControl := net.Pipe()
	dialer := owner.newAgentDialer(control, cluster, resource, edge)
	defer dialer.Close() //nolint:errcheck
	advertised := make(chan string, 1)
	listener := revdial.NewListener(agentControl, func(ctx context.Context, path string) (*websocket.Conn, *http.Response, error) {
		advertised <- path
		return websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(peerHTTP.URL, "http")+path, http.Header{"Authorization": {bearer}})
	})
	defer listener.Close() //nolint:errcheck
	type acceptedConn struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan acceptedConn, 1)
	go func() { conn, err := listener.Accept(); accepted <- acceptedConn{conn, err} }()
	providerConn, err := dialer.Dial(ctx)
	if err != nil {
		t.Fatalf("pickup handshake through peer: %v", err)
	}
	defer providerConn.Close() //nolint:errcheck
	var agentConn net.Conn
	select {
	case result := <-accepted:
		if result.err != nil {
			t.Fatal(result.err)
		}
		agentConn = result.conn
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer agentConn.Close() //nolint:errcheck
	parsed, err := url.Parse(<-advertised)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/services/providers/edges/agent/proxy/owner-replica" {
		t.Fatalf("replica pickup path %q", parsed.Path)
	}
	query := parsed.Query()
	for key, want := range map[string]string{"railgrid.agent.cluster": cluster, "railgrid.agent.resource": resource, "railgrid.agent.name": edge} {
		if len(query[key]) != 1 || query.Get(key) != want {
			t.Errorf("wire pickup %s=%v, want %q", key, query[key], want)
		}
	}
	if len(query["revdial.dialer"]) != 1 || query.Get("revdial.dialer") == "" {
		t.Fatal("missing real dialer capability")
	}
	ownerRequest := <-received
	if ownerRequest.path != "/agent-pickup" || ownerRequest.rawQuery != parsed.RawQuery || ownerRequest.authorization != bearer {
		t.Fatalf("peer lost pickup scope or agent header: %+v", ownerRequest)
	}
	for _, direction := range []struct {
		from, to net.Conn
		payload  string
	}{{providerConn, agentConn, "request through scoped pickup"}, {agentConn, providerConn, "response through scoped pickup"}} {
		writeResult := make(chan error, 1)
		go func() { _, err := io.WriteString(direction.from, direction.payload); writeResult <- err }()
		_ = direction.to.SetReadDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, len(direction.payload))
		if _, err := io.ReadFull(direction.to, got); err != nil {
			t.Fatal(err)
		}
		if err := <-writeResult; err != nil {
			t.Fatal(err)
		}
		if string(got) != direction.payload {
			t.Fatalf("pickup payload=%q", got)
		}
	}
}
