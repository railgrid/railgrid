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
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
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

	"github.com/faroshq/faros-kedge/pkg/util/revdial"
)

// StartProxyTunnel establishes a reverse tunnel to the hub server.
// It runs an exponential backoff retry loop to maintain the connection.
// tlsConfig controls TLS verification for the WebSocket connection to the hub.
// Pass nil to use a default (secure) TLS config; use InsecureSkipVerify only
// in development environments.
//
// resourceType must be either "sites" (Kubernetes cluster agent) or "servers"
// (bare-metal / systemd host agent). It controls the query parameter sent to
// the hub's tunnel endpoint so that Sites and Servers are stored under
// distinct connection-manager keys and never alias each other.
//
// cluster is the kcp logical cluster path (e.g., "root:kedge:user-default").
// If empty, it's extracted from the token (for SA tokens) or defaults to "default".
func StartProxyTunnel(ctx context.Context, hubURL string, token string, siteName string, resourceType string, downstream *rest.Config, tlsConfig *tls.Config, stateChannel chan<- bool, sshPort int, cluster string) {
	logger := klog.FromContext(ctx)
	logger.Info("Starting proxy tunnel", "hubURL", hubURL, "siteName", siteName, "resourceType", resourceType)

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

		err := startTunneler(ctx, hubURL, token, siteName, resourceType, downstream, tlsConfig, stateChannel, sshPort, cluster)
		if err != nil {
			logger.Error(err, "tunnel connection failed, reconnecting")
		}

		if stateChannel != nil {
			stateChannel <- false
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff.Step()):
		}
	}
}

func startTunneler(ctx context.Context, hubURL string, token string, siteName string, resourceType string, downstream *rest.Config, tlsConfig *tls.Config, stateChannel chan<- bool, sshPort int, cluster string) error {
	logger := klog.FromContext(ctx)

	// Connect to hub's tunnel endpoint.
	// Use explicit cluster if provided, otherwise extract from SA token.
	clusterName := cluster
	if clusterName == "" {
		clusterName = extractClusterNameFromToken(token)
	}

	// All edge types (kubernetes and server) use the unified agent-proxy virtual
	// workspace path introduced in Phase 3.
	// resourceType is retained for legacy callers but no longer affects the URL.
	_ = resourceType
	edgeProxyURL := fmt.Sprintf("%s/services/agent-proxy/%s/apis/kedge.faros.sh/v1alpha1/edges/%s/proxy",
		hubURL, clusterName, siteName)

	conn, err := initiateConnection(ctx, edgeProxyURL, token, tlsConfig)
	if err != nil {
		return fmt.Errorf("failed to initiate connection: %w", err)
	}

	logger.Info("Tunnel connection established")
	if stateChannel != nil {
		stateChannel <- true
	}

	// Create revdial listener
	ln := revdial.NewListener(conn, revdialFunc(hubURL, token, tlsConfig))
	defer ln.Close() //nolint:errcheck

	// Create and serve local HTTP server
	server, err := newRemoteServer(downstream, sshPort)
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

// initiateConnection dials the hub via WebSocket and returns the underlying net.Conn.
func initiateConnection(ctx context.Context, wsURL string, token string, tlsConfig *tls.Config) (net.Conn, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, err
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

	wsConn, _, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		return nil, fmt.Errorf("WebSocket dial failed: %w", err)
	}

	return wsconnadapter.New(wsConn), nil
}

// extractClusterNameFromToken decodes a kcp ServiceAccount JWT (without
// signature verification) and returns the clusterName claim. Returns "default"
// if the token cannot be parsed or lacks the claim.
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
// pick up new connections from the hub.
func revdialFunc(baseURL string, token string, tlsConfig *tls.Config) func(context.Context, string) (*websocket.Conn, *http.Response, error) {
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
		if token != "" {
			header.Set("Authorization", "Bearer "+token)
		}

		return dialer.DialContext(ctx, u.String(), header)
	}
}
