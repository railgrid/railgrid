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

// The relay plane: how a replica that does NOT hold an edge's tunnel still
// serves requests for it. ConnManager.Load returns a remoteDialer for
// peer-held tunnels; its Dial opens a raw byte stream to the owning replica's
// internal listener (/relay), which bridges it onto a local revdial
// Dial — so every consumer (edgeproxy k8s/ssh, service proxy, MCP tools,
// reconcilers, event subscribers) works unchanged from any replica, at the
// cost of one intra-cluster hop.
//
// The internal listener is pod-to-pod only (its port is not in the provider's
// Service) and the relay additionally requires the provider's own bearer
// token, which every replica shares via the minted provider kubeconfig.

import (
	"bufio"
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// relayPath is the internal-listener endpoint that bridges a raw stream onto
// a locally held tunnel.
const relayPath = "/relay"

// relayUpgradeProto marks the 101 handshake so a misrouted HTTP client gets a
// clean error instead of half a byte stream.
const relayUpgradeProto = "faros-tunnel-relay"

// remoteDialer opens connections to an edge whose tunnel is held by a peer
// replica, by relaying through that peer's internal listener.
type remoteDialer struct {
	addr  string // peer relay address ("ip:internalPort")
	key   string // edge conn key
	token string // shared provider bearer authenticating the relay
}

func (d *remoteDialer) Dial(ctx context.Context) (net.Conn, error) {
	var nd net.Dialer
	conn, err := nd.DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return nil, fmt.Errorf("dialing peer replica %s: %w", d.addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		// Bound the handshake; cleared once the stream is established.
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	}
	req := fmt.Sprintf("GET %s?key=%s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: %s\r\n\r\n",
		relayPath, url.QueryEscape(d.key), d.addr, d.token, relayUpgradeProto)
	if _, err := io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("relay handshake write to %s: %w", d.addr, err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("relay handshake read from %s: %w", d.addr, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("peer replica %s refused relay for %s: %s %s", d.addr, d.key, resp.Status, strings.TrimSpace(string(msg)))
	}
	_ = conn.SetDeadline(time.Time{})
	return &relayConn{Conn: conn, r: br}, nil
}

// relayConn drains bytes the handshake reader buffered past the 101 before
// reading from the socket.
type relayConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *relayConn) Read(p []byte) (int, error) {
	if c.r.Buffered() > 0 {
		return c.r.Read(p)
	}
	return c.Conn.Read(p)
}

// pickupRouter dispatches replica-addressed pickup connections
// (/proxy/{replicaID}?revdial.dialer=...): local replica → the revdial
// ConnHandler; a peer → a proxied WebSocket upgrade to that peer's internal
// listener, resolved through its presence lease. Unknown or dead replicas get
// 502 — the agent reports pickup-failed and the pending Dial errors cleanly.
func (s *Server) pickupRouter(local http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		replicaID := strings.TrimPrefix(r.URL.Path, "/proxy/")
		if replicaID == "" || strings.Contains(replicaID, "/") {
			http.Error(w, "invalid pickup path", http.StatusBadRequest)
			return
		}
		if s.registry == nil || replicaID == s.replicaID {
			local.ServeHTTP(w, r)
			return
		}
		addr, ok := s.registry.ReplicaAddr(r.Context(), replicaID)
		if !ok {
			s.logger.Info("pickup for unknown or dead replica", "replica", replicaID)
			http.Error(w, "unknown replica", http.StatusBadGateway)
			return
		}
		target := &url.URL{Scheme: "http", Host: addr}
		proxy := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.Out.URL.Path = "/agent-pickup"
				// Query (the revdial dialer id) rides along untouched.
			},
		}
		proxy.ServeHTTP(w, r)
	})
}

// relayHandler serves the owning side of the relay: authenticate the peer,
// dial the LOCAL tunnel (never recursing into another relay), hijack, 101,
// and pipe until either side closes.
func (s *Server) relayHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.relayToken == "" ||
			subtle.ConstantTimeCompare([]byte(extractBearerToken(r)), []byte(s.relayToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "missing key", http.StatusBadRequest)
			return
		}
		dialer, ok := s.edgeConnManager.LoadLocal(key)
		if !ok {
			// The forwarding replica acted on a stale lease; it will re-resolve
			// after its cache TTL.
			http.Error(w, "no local tunnel for key", http.StatusBadGateway)
			return
		}
		down, err := dialer.Dial(r.Context())
		if err != nil {
			http.Error(w, "tunnel dial failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			_ = down.Close()
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		up, bufrw, err := hj.Hijack()
		if err != nil {
			_ = down.Close()
			return
		}
		_, _ = bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + relayUpgradeProto + "\r\n\r\n")
		_ = bufrw.Flush()

		logger := klog.Background().WithName("tunnel-relay").WithValues("key", key)
		logger.V(4).Info("relay stream open")
		done := make(chan struct{}, 2)
		go func() {
			// Drain anything the hijacked reader buffered before splicing.
			if n := bufrw.Reader.Buffered(); n > 0 {
				buffered, _ := bufrw.Reader.Peek(n)
				_, _ = down.Write(buffered)
				_, _ = bufrw.Reader.Discard(n)
			}
			_, _ = io.Copy(down, up)
			done <- struct{}{}
		}()
		go func() {
			_, _ = io.Copy(up, down)
			done <- struct{}{}
		}()
		<-done
		_ = up.Close()
		_ = down.Close()
		<-done
		logger.V(4).Info("relay stream closed")
	}
}
