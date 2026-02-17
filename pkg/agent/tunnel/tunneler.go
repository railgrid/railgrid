package tunnel

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/function61/holepunch-server/pkg/wsconnadapter"
	"github.com/gorilla/websocket"

	"github.com/faroshq/faros-kedge/pkg/util/revdial"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// StartProxyTunnel establishes a reverse tunnel to the hub server.
// It runs an exponential backoff retry loop to maintain the connection.
func StartProxyTunnel(ctx context.Context, hubURL string, token string, siteName string, downstream *rest.Config, stateChannel chan<- bool) {
	logger := klog.FromContext(ctx)
	logger.Info("Starting proxy tunnel", "hubURL", hubURL, "siteName", siteName)

	backoff := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    10,
		Cap:      5 * time.Minute,
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := startTunneler(ctx, hubURL, token, siteName, downstream, stateChannel)
		if err != nil {
			logger.Error(err, "tunnel connection failed, retrying")
		}

		if stateChannel != nil {
			stateChannel <- false
		}

		// Backoff before retry
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff.Step()):
		}
	}
}

func startTunneler(ctx context.Context, hubURL string, token string, siteName string, downstream *rest.Config, stateChannel chan<- bool) error {
	logger := klog.FromContext(ctx)

	// Connect to hub's tunnel endpoint.
	// Extract the real KCP logical cluster name from the SA token so the tunnel
	// key matches what the mount controller expects.
	clusterName := extractClusterNameFromToken(token)
	edgeProxyURL := fmt.Sprintf("%s/tunnel/?cluster=%s&site=%s", hubURL, clusterName, siteName)

	conn, err := initiateConnection(ctx, edgeProxyURL, token)
	if err != nil {
		return fmt.Errorf("failed to initiate connection: %w", err)
	}

	logger.Info("Tunnel connection established")
	if stateChannel != nil {
		stateChannel <- true
	}

	// Create revdial listener
	ln := revdial.NewListener(conn, revdialFunc(hubURL, token))
	defer ln.Close()

	// Create and serve local HTTP server
	server, err := newRemoteServer(downstream)
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
		server.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}

// initiateConnection dials the hub via WebSocket and returns the underlying net.Conn.
func initiateConnection(ctx context.Context, wsURL string, token string) (net.Conn, error) {
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
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
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

// extractClusterNameFromToken decodes a KCP ServiceAccount JWT (without
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
func revdialFunc(baseURL string, token string) func(context.Context, string) (*websocket.Conn, *http.Response, error) {
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
		u.Path = path

		dialer := websocket.Dialer{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			HandshakeTimeout: 30 * time.Second,
		}

		header := http.Header{}
		if token != "" {
			header.Set("Authorization", "Bearer "+token)
		}

		return dialer.DialContext(ctx, u.String(), header)
	}
}
