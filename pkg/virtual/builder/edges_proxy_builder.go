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

package builder

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	utilssh "github.com/faroshq/faros-kedge/pkg/util/ssh"
)

// buildEdgesProxyHandler creates the HTTP handler for user-facing access to
// Edge resources (the user-side of the new Edge workflow).
//
// Path (relative to /services/edges-proxy/ mount point):
//
//	/clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/{subresource}[/...]
//
// Supported subresources:
//   - k8s  — reverse-proxy to the Kubernetes API of a type=kubernetes edge
//   - ssh  — WebSocket SSH terminal session on a type=server edge
func (p *virtualWorkspaces) buildEdgesProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Authenticate: require a valid bearer token.
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 2. Parse cluster, name, and subresource from the URL path.
		cluster, name, subresource, ok := parseEdgesProxyPath(r.URL.Path)
		if !ok {
			http.Error(w, "invalid path: expected /clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/{subresource}[/...]", http.StatusBadRequest)
			return
		}

		// 3. Delegated authorization via kcp (if configured).
		if p.kcpConfig != nil {
			if claims, ok := parseServiceAccountToken(token); ok {
				if err := authorize(r.Context(), p.kcpConfig, token, claims.ClusterName, "proxy", "edges", name); err != nil {
					p.logger.Error(err, "edges proxy authorization failed",
						"cluster", cluster, "name", name, "subresource", subresource)
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
			}
			// TODO(#63): OIDC tokens bypass authorization here — same issue as
			// agent_proxy_builder.go. Track in https://github.com/faroshq/kedge/issues/63
		}

		// 4. Look up the dialer registered by the agent-proxy-v2 handler.
		key := edgeConnKey(cluster, name)
		dialer, found := p.edgeConnManager.Load(key)
		if !found {
			http.Error(w, fmt.Sprintf("no active tunnel for edge %s/%s", cluster, name), http.StatusBadGateway)
			return
		}

		// 5. Route to the appropriate subresource handler.
		switch subresource {
		case "k8s":
			p.edgesK8sHandler(r.Context(), w, r, key, dialer)
		case "ssh":
			p.edgesSSHHandler(r.Context(), w, r, key, dialer)
		default:
			http.Error(w, fmt.Sprintf("unknown subresource: %s", subresource), http.StatusNotFound)
		}
	})
}

// edgesK8sHandler reverse-proxies HTTP to the edge agent's local K8s API.
// It dials the agent via the revdial.Dialer obtained from edgeConnManager.
func (p *virtualWorkspaces) edgesK8sHandler(ctx context.Context, w http.ResponseWriter, r *http.Request, key string, dialer interface {
	Dial(context.Context) (net.Conn, error)
}) {
	logger := klog.FromContext(ctx)

	deviceConn, err := dialer.Dial(ctx)
	if err != nil {
		logger.Error(err, "failed to dial edge agent for k8s", "key", key)
		http.Error(w, "failed to connect to edge agent", http.StatusBadGateway)
		return
	}

	// Handle upgrade requests (exec, port-forward) via raw hijacking.
	if isUpgradeRequest(r) {
		p.edgesHandleK8sUpgrade(ctx, w, r, deviceConn)
		return
	}

	// Reverse-proxy to the agent's Kubernetes API server.
	transport := &edgeDeviceConnTransport{conn: deviceConn}
	path := extractEdgeK8sPath(r.URL.Path)
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "edge-agent"
			req.URL.Path = path // path already includes /k8s/ prefix
		},
		Transport: transport,
	}
	proxy.ServeHTTP(w, r)
}

// edgesSSHHandler establishes a WebSocket SSH session to the edge agent.
// It dials the agent via the revdial.Dialer, opens the agent-side SSH tunnel,
// and then bridges the caller's WebSocket to the SSH session.
func (p *virtualWorkspaces) edgesSSHHandler(ctx context.Context, w http.ResponseWriter, r *http.Request, key string, dialer interface {
	Dial(context.Context) (net.Conn, error)
}) {
	logger := klog.FromContext(ctx)

	// Parse cluster and edge name from the key (format: "edges/{cluster}/{name}")
	cluster, edgeName := parseEdgeConnKey(key)

	// Optional non-interactive exec mode (e.g. `kedge ssh <name> -- <cmd>`).
	remoteCmd := r.URL.Query().Get("cmd")

	// Fetch SSH credentials from Edge status.
	creds, err := p.fetchSSHCredentials(ctx, cluster, edgeName, logger)
	if err != nil {
		logger.Error(err, "failed to fetch SSH credentials", "key", key)
		// Continue with nil credentials - will fall back to empty password auth
	}

	logger.V(4).Info("Edges SSH handler", "key", key, "hasCredentials", creds != nil, "exec", remoteCmd != "")

	// Dial the agent via the reverse tunnel.
	deviceConn, err := dialer.Dial(ctx)
	if err != nil {
		logger.Error(err, "failed to dial edge agent for SSH", "key", key)
		http.Error(w, "failed to connect to edge agent", http.StatusBadGateway)
		return
	}

	// Open the SSH tunnel over the raw reverse-tunnel connection.
	sshConn, err := openAgentSSHTunnel(ctx, deviceConn)
	if err != nil {
		logger.Error(err, "failed to open SSH tunnel to edge agent", "key", key)
		http.Error(w, "failed to open SSH tunnel", http.StatusBadGateway)
		return
	}

	// TODO(#67): CheckOrigin returns true for all origins — MITM risk.
	// https://github.com/faroshq/kedge/issues/67
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error(err, "failed to upgrade caller connection to WebSocket")
		return
	}
	defer wsConn.Close() //nolint:errcheck

	// Build the SSH client over the tunnelled raw connection.
	sshClient, err := newSSHClient(ctx, sshConn, creds, logger)
	if err != nil {
		logger.Error(err, "failed to create SSH client for edge")
		return
	}
	defer sshClient.Close() //nolint:errcheck

	if remoteCmd != "" {
		// Non-interactive exec: run command, stream output, close.
		p.sshExec(ctx, wsConn, sshClient, remoteCmd, logger)
		return
	}

	// Interactive PTY + shell session over WebSocket.
	session, err := utilssh.NewSocketSSHSession(logger, 120, 40, sshClient, wsConn)
	if err != nil {
		logger.Error(err, "failed to create SSH session for edge")
		return
	}
	defer session.Close()

	if err := session.Run(ctx); err != nil {
		logger.Error(err, "SSH session error for edge")
	}
}

// parseEdgeConnKey extracts cluster and name from the connection key.
// Key format: "edges/{cluster}/{name}"
func parseEdgeConnKey(key string) (cluster, name string) {
	parts := strings.Split(key, "/")
	if len(parts) >= 3 && parts[0] == "edges" {
		return parts[1], parts[2]
	}
	return "", ""
}

// fetchSSHCredentials retrieves SSH credentials from the Edge status and referenced secrets.
// The cluster parameter is used to scope the kcp API calls to the correct logical cluster.
func (p *virtualWorkspaces) fetchSSHCredentials(ctx context.Context, cluster, edgeName string, logger klog.Logger) (*SSHClientCredentials, error) {
	if p.kcpConfig == nil {
		logger.V(4).Info("No kcp config, skipping credential fetch")
		return nil, nil
	}

	// Create cluster-scoped clients by modifying the host URL to include the cluster path.
	clusterConfig := rest.CopyConfig(p.kcpConfig)
	clusterConfig.Host = appendClusterPath(clusterConfig.Host, cluster)

	kedgeClient, err := kedgeclient.NewForConfig(clusterConfig)
	if err != nil {
		return nil, fmt.Errorf("creating cluster-scoped kedge client: %w", err)
	}

	k8sClient, err := kubernetes.NewForConfig(clusterConfig)
	if err != nil {
		return nil, fmt.Errorf("creating cluster-scoped k8s client: %w", err)
	}

	// Fetch the Edge resource.
	edge, err := kedgeClient.Edges().Get(ctx, edgeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("fetching edge %s: %w", edgeName, err)
	}

	if edge.Status.SSHCredentials == nil {
		logger.V(4).Info("No SSH credentials configured for edge", "edge", edgeName)
		return nil, nil
	}

	creds := &SSHClientCredentials{
		Username: edge.Status.SSHCredentials.Username,
	}

	// Fetch password from secret if referenced.
	if ref := edge.Status.SSHCredentials.PasswordSecretRef; ref != nil {
		secret, err := k8sClient.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("fetching password secret %s/%s: %w", ref.Namespace, ref.Name, err)
		}
		if pw, ok := secret.Data["password"]; ok {
			creds.Password = string(pw)
		}
	}

	// Fetch private key from secret if referenced.
	if ref := edge.Status.SSHCredentials.PrivateKeySecretRef; ref != nil {
		secret, err := k8sClient.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("fetching private key secret %s/%s: %w", ref.Namespace, ref.Name, err)
		}
		if key, ok := secret.Data["privateKey"]; ok {
			creds.PrivateKey = key
		}
	}

	logger.V(4).Info("Fetched SSH credentials", "edge", edgeName, "user", creds.Username,
		"hasPassword", creds.Password != "", "hasPrivateKey", len(creds.PrivateKey) > 0)

	return creds, nil
}

// appendClusterPath sets the /clusters/<path> segment on a kcp URL.
// If the host already contains a /clusters/ path, it is replaced.
func appendClusterPath(host, clusterPath string) string {
	host = strings.TrimSuffix(host, "/")
	if idx := strings.Index(host, "/clusters/"); idx != -1 {
		host = host[:idx]
	}
	return host + "/clusters/" + clusterPath
}

// edgesHandleK8sUpgrade handles upgrade requests (exec, port-forward) to an
// edge agent by hijacking the client connection and doing a bidirectional copy.
func (p *virtualWorkspaces) edgesHandleK8sUpgrade(ctx context.Context, w http.ResponseWriter, r *http.Request, deviceConn net.Conn) {
	logger := klog.FromContext(ctx)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		logger.Error(err, "failed to hijack client connection for edge k8s upgrade")
		return
	}
	defer clientConn.Close() //nolint:errcheck
	defer deviceConn.Close() //nolint:errcheck

	if err := r.Write(deviceConn); err != nil {
		logger.Error(err, "failed to forward upgrade request to edge agent")
		return
	}

	// Bidirectional pipe.
	errc := make(chan error, 2)
	go func() { _, err := io.Copy(deviceConn, clientConn); errc <- err }()
	go func() { _, err := io.Copy(clientConn, deviceConn); errc <- err }()
	<-errc
}

// edgeDeviceConnTransport implements http.RoundTripper using an already-opened
// connection to the edge agent.
type edgeDeviceConnTransport struct {
	conn net.Conn
}

func (t *edgeDeviceConnTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Write(t.conn); err != nil {
		return nil, err
	}
	return http.ReadResponse(bufio.NewReader(t.conn), req)
}

// parseEdgesProxyPath extracts {cluster}, {name}, and {subresource} from the
// path that the handler sees after "/services/edges-proxy" has been stripped.
//
// Expected format:
//
//	/clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/{subresource}[/...]
func parseEdgesProxyPath(path string) (cluster, name, subresource string, ok bool) {
	// Strip leading slash.
	path = strings.TrimPrefix(path, "/")

	// Expected segments after split:
	//   [0] "clusters"
	//   [1] cluster
	//   [2] "apis"
	//   [3] "kedge.faros.sh"
	//   [4] "v1alpha1"
	//   [5] "edges"
	//   [6] name
	//   [7] subresource        (may have more segments after this for k8s pass-through)
	parts := strings.SplitN(path, "/", 9) // cap to avoid unbounded allocation
	if len(parts) < 8 {
		return "", "", "", false
	}
	if parts[0] != "clusters" || parts[2] != "apis" || parts[3] != "kedge.faros.sh" ||
		parts[4] != "v1alpha1" || parts[5] != "edges" {
		return "", "", "", false
	}
	return parts[1], parts[6], parts[7], true
}

// extractEdgeK8sPath strips the edges-proxy prefix from the request path,
// keeping the /k8s/ prefix that the agent expects.
//
// Input:  /clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/k8s/api/v1/pods
// Output: /k8s/api/v1/pods
func extractEdgeK8sPath(path string) string {
	idx := strings.Index(path, "/k8s/")
	if idx >= 0 {
		return path[idx:] // keep "/k8s/api/..."
	}
	// Handle case where path ends with just "/k8s" (no trailing slash)
	if strings.HasSuffix(path, "/k8s") {
		return "/k8s/"
	}
	return "/k8s/"
}
