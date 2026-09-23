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
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/function61/holepunch-server/pkg/wsconnadapter"
	"github.com/gorilla/websocket"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/railgrid/provider-sdk/revdial"

	"github.com/railgrid/railgrid/pkg/apiurl"
)

// StartProxyTunnel establishes a reverse tunnel to the hub server.
// It runs an exponential backoff retry loop to maintain the connection.
// tlsConfig controls TLS verification for the WebSocket connection to the hub.
// Pass nil to use a default (secure) TLS config; use InsecureSkipVerify only
// in development environments.
//
// resourceType is "kubernetes" (Kubernetes cluster agent), "server"
// (Linux bare-metal / systemd host agent), or "macos" (service-only macOS
// host agent). It selects which resource the agent dials on the single
// `edges` provider via apiurl.ProviderAgentProxyURL.
//
// cluster is the tenant workspace's kcp LOGICAL-CLUSTER ID (e.g.
// "2hx82dl9ncmepp5l"), never a workspace path: the agent-ingress route is the
// shared grammar, and provider-sdk/dataplane refuses a path-form cluster
// segment because the hub proxy will not serve one either. If empty, it is
// extracted from the token (for SA tokens) or defaults to "default".
//
// credentials holds the agent's scoped identity and re-mints it on the
// reconnect path. It supplies the bearer for every connect attempt — the
// bootstrap join token until the agent has enrolled, the TTL'd credential
// afterwards — and adopts the enrolment bundle the provider returns on the
// upgrade response of a join-token connect.
//
// A nil store is a caller that manages its own bearer (tests).
//
// svc is the /svc proxy host policy (--svc-allow-cidr / --svc-policy); see
// SvcProxyOptions and newSvcProxyHandler.
func StartProxyTunnel(ctx context.Context, hubURL string, credentials *CredentialStore, edgeName string, resourceType string, downstream *rest.Config, tlsConfig *tls.Config, stateChannel chan bool, sshPort int, svc SvcProxyOptions, cluster string, onEnrolled func(Credential), extraHeaders http.Header) {
	logger := klog.FromContext(ctx)
	logger.Info("Starting proxy tunnel", "hubURL", hubURL, "edgeName", edgeName, "resourceType", resourceType)

	backoff := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    math.MaxInt32,
		Cap:      30 * time.Second,
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := startTunneler(ctx, hubURL, credentials, edgeName, resourceType, downstream, tlsConfig, stateChannel, sshPort, svc, cluster, onEnrolled, extraHeaders)
		if err != nil {
			logger.Error(err, "tunnel connection failed, reconnecting")
		}

		sendTunnelState(stateChannel, false)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff.Step()):
		}
	}
}

// sendTunnelState publishes the current tunnel state on the cap-1 channel
// without ever blocking. If a previous value is still buffered it is dropped
// first, so the reader always sees the most recent state. We rely on the
// single-sender invariant (only StartProxyTunnel writes to stateChannel) to
// guarantee the post-drain send fits.
//
// A blocking send here used to deadlock the reconnect loop: if the reader
// (EdgeReporter) was stuck inside a slow status PATCH, the tunnel goroutine
// would wedge on the send and stop retrying entirely, leaving the agent
// permanently disconnected even though its heartbeat goroutine kept running.
func sendTunnelState(c chan bool, v bool) {
	if c == nil {
		return
	}
	select {
	case <-c:
	default:
	}
	select {
	case c <- v:
	default:
	}
}

func startTunneler(ctx context.Context, hubURL string, credentials *CredentialStore, edgeName string, resourceType string, downstream *rest.Config, tlsConfig *tls.Config, stateChannel chan bool, sshPort int, svc SvcProxyOptions, cluster string, onEnrolled func(Credential), extraHeaders http.Header) error {
	logger := klog.FromContext(ctx)

	// Refresh before dialling, not from a timer: the reconnect path is the
	// only place the agent is guaranteed to be doing something, and a
	// credential it has stopped using is one its identity should be allowed to
	// lapse with. A refresh failure is logged and the connect proceeds with
	// the token in hand — it is still valid for the remaining 20% of its TTL,
	// and the next attempt tries again.
	if credentials != nil {
		if err := credentials.EnsureFresh(ctx); err != nil {
			logger.Error(err, "could not refresh the agent credential; continuing with the one in hand")
		}
	}

	// The bearer for this attempt: the bootstrap join token while enrolling,
	// the scoped identity once the provider has issued one (the hub clears
	// edge.Status.JoinToken on the first successful join, so the join token
	// stops working at exactly that point).
	token := ""
	if credentials != nil {
		token = credentials.Token()
	}

	// Connect to hub's tunnel endpoint.
	// Determine the kcp cluster name and the base hub URL (without /clusters/ path).
	// Priority: explicit cluster arg > URL-embedded cluster > SA-token claim.
	baseHubURL, urlCluster := SplitBaseAndCluster(hubURL)
	clusterName := cluster
	if clusterName == "" {
		clusterName = urlCluster
	}
	if clusterName == "default" {
		// Last-resort fallback: extract from SA token JWT claim (works for
		// kubeconfig-based auth where the bearer token is a kcp ServiceAccount).
		if sa := extractClusterNameFromToken(token); sa != "default" {
			clusterName = sa
		}
	}

	// The agent dials the single `edges` provider's agent-ingress route —
	// Pillar 2 class (f), /agent/clusters/{id}/{resource}/{name}/proxy —
	// choosing the resource (kubernetesclusters, linuxservers, or
	// macosservers) by type, routed through the hub backend proxy.
	// resourceType is the agent type ("kubernetes" | "server" | "macos").
	//
	// This path changed shape: the old route carried
	// /apis/edges.railgrid.ai/v1alpha1 between the cluster and the resource.
	// There is no compatibility window — an agent built before this change
	// gets a 400 and must be upgraded.
	edgeProxyURL := apiurl.ProviderAgentProxyURL(baseHubURL, resourceType, clusterName, edgeName, "proxy")

	conn, resp, err := initiateConnection(ctx, edgeProxyURL, token, tlsConfig, extraHeaders)
	if err != nil {
		// A 401 is the provider refusing this bearer outright. When the agent
		// holds both a saved credential and a join token, try the other one
		// next: a credential saved by an earlier enrolment is dead once the
		// hub or the edge behind it was recreated, and the join token the
		// operator just handed us is the way back in.
		var he *HandshakeError
		if errors.As(err, &he) && he.StatusCode == http.StatusUnauthorized && credentials != nil && credentials.Rejected() {
			logger.Info("the provider refused the agent's bearer; the next attempt presents the other credential (saved credential vs join token)")
		}
		return fmt.Errorf("failed to initiate connection: %w", err)
	}

	// Enrolment: a join-token connect comes back with the scoped identity the
	// provider just minted, plus the hub URL, its CA and the routes this agent
	// refreshes at. Adopt it before serving anything — from here the join
	// token is neither needed nor valid.
	//
	// This replaces X-Railgrid-Agent-Kubeconfig, which carried a permanent
	// ServiceAccount token the agent wrote to disk and into a Secret.
	credentialIssued := false
	if resp != nil && credentials != nil {
		if encoded := resp.Header.Get(CredentialHeader); encoded != "" {
			credential, cerr := DecodeCredential(encoded)
			if cerr != nil {
				logger.Error(cerr, "the provider returned an unusable agent credential")
			} else {
				if aerr := credentials.Adopt(credential); aerr != nil {
					logger.Error(aerr, "could not persist the agent credential; it is held in memory only")
				}
				credentialIssued = true
				logger.Info("agent credential issued", "expiresAt", credential.ExpiresAt)
			}
		}
	}
	// A restarted agent already has its identity: the provider sends no new
	// enrollment header when that bearer reconnects. Release startup after
	// the successful handshake so reporters and add-ons can start. A join-token
	// connection without a new bundle must not release a stale saved identity.
	if credentials != nil && onEnrolled != nil {
		if credential, ok := credentials.Current(); ok && (credentialIssued || credential.Token == token) {
			onEnrolled(credential)
		}
	}

	logger.Info("Tunnel connection established")
	sendTunnelState(stateChannel, true)

	// Create revdial listener. Pickups read the credential through the store,
	// so a sub-connection opened after a refresh carries the new token rather
	// than the one this tunnel was established with.
	pickupToken := func() string {
		if credentials == nil {
			return ""
		}
		return credentials.Token()
	}
	ln := revdial.NewListener(conn, revdialFunc(hubURL, pickupToken, tlsConfig))
	defer ln.Close() //nolint:errcheck

	// Create and serve local HTTP server
	server, err := newRemoteServer(downstream, sshPort, svc)
	if err != nil {
		return fmt.Errorf("failed to create remote server: %w", err)
	}

	// Serve on the revdial listener
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		_ = server.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}

// initiateConnection dials the hub via WebSocket and returns the underlying
// net.Conn together with the HTTP upgrade response. The response headers may
// contain hub-provided metadata such as X-Railgrid-Agent-Token (token-exchange).
func initiateConnection(ctx context.Context, wsURL string, token string, tlsConfig *tls.Config, extraHeaders http.Header) (net.Conn, *http.Response, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, nil, err
	}

	// Convert http(s) to ws(s)
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}

	dialer := websocket.Dialer{
		TLSClientConfig:  tlsConfig,
		HandshakeTimeout: 30 * time.Second,
	}

	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	for k, vals := range extraHeaders {
		for _, v := range vals {
			header.Add(k, v)
		}
	}

	wsConn, resp, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		// gorilla returns a non-nil resp when the upgrade was answered with
		// something other than 101, and collapses every one of them into the
		// same "bad handshake" error. Surface the status so a rejection by an
		// intermediary (a proxy 403, a 502 with no origin behind it) is
		// distinguishable from the hub genuinely refusing the agent's
		// credentials — otherwise the only signal is an unattributable
		// "websocket: bad handshake" in the journal.
		if resp != nil {
			return nil, nil, &HandshakeError{StatusCode: resp.StatusCode, Err: err}
		}
		return nil, nil, fmt.Errorf("WebSocket dial failed: %w", err)
	}

	return wsconnadapter.New(wsConn), resp, nil
}

// HandshakeError is a WebSocket upgrade the hub answered with a non-101 status.
// It carries the status so the reconnect loop can tell a refused bearer (401)
// from an intermediary failure (a proxy 403, a 502 with no origin behind it).
type HandshakeError struct {
	StatusCode int
	Err        error
}

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("WebSocket dial failed (hub returned HTTP %d %s): %v", e.StatusCode, http.StatusText(e.StatusCode), e.Err)
}

func (e *HandshakeError) Unwrap() error { return e.Err }

// SplitBaseAndCluster splits a hub URL into the base URL (scheme+host only) and
// the kcp cluster name embedded in the path.
//
// For "https://railgrid.localhost:9443/clusters/abc123" it returns:
//
//	base    = "https://railgrid.localhost:9443"
//	cluster = "abc123"
//
// For "https://railgrid.localhost:9443" (no /clusters/ path segment) it returns:
//
//	base    = "https://railgrid.localhost:9443"
//	cluster = "default"
//
// The base URL is always used when dialling the hub so that /services/agent-proxy/
// is routed by the hub's own mux before reaching the kcp reverse-proxy.
//
// Deprecated: use apiurl.SplitBaseAndCluster directly.
func SplitBaseAndCluster(hubURL string) (base, cluster string) {
	return apiurl.SplitBaseAndCluster(hubURL)
}

// extractClusterNameFromToken decodes a kcp ServiceAccount JWT (without
// signature verification) and returns the clusterName claim. Returns "default"
// if the token cannot be parsed or lacks the claim.
//
// This is a last-resort fallback; prefer extracting the cluster from the hub
// URL via splitBaseAndCluster when a kubeconfig-provided server URL is available.
func extractClusterNameFromToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "default"
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "default"
	}
	var claims struct {
		ClusterName string `json:"kubernetes.io/serviceaccount/clusterName"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.ClusterName == "" {
		return "default"
	}
	return claims.ClusterName
}

// revdialFunc returns the dial function used by the revdial.Listener to
// pick up new connections from the hub. getToken is invoked on every dial so
// pick-up connections track the latest bearer token (e.g. the SA token issued
// via token-exchange) rather than the original join token.
func revdialFunc(baseURL string, getToken func() string, tlsConfig *tls.Config) func(context.Context, string) (*websocket.Conn, *http.Response, error) {
	return func(ctx context.Context, path string) (*websocket.Conn, *http.Response, error) {
		u, err := url.Parse(baseURL)
		if err != nil {
			return nil, nil, err
		}

		switch u.Scheme {
		case "https":
			u.Scheme = "wss"
		case "http":
			u.Scheme = "ws"
		}

		// Parse path+query separately so the query string is preserved
		// correctly (setting u.Path directly would escape "?" as "%3F").
		pathURL, err := url.Parse(path)
		if err != nil {
			return nil, nil, err
		}
		u.Path = pathURL.Path
		u.RawQuery = pathURL.RawQuery

		dialer := websocket.Dialer{
			TLSClientConfig:  tlsConfig,
			HandshakeTimeout: 30 * time.Second,
		}

		header := http.Header{}
		token := ""
		if getToken != nil {
			token = getToken()
		}
		if token != "" {
			header.Set("Authorization", "Bearer "+token)
		}

		return dialer.DialContext(ctx, u.String(), header)
	}
}
