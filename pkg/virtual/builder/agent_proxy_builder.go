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
	gossh "golang.org/x/crypto/ssh"

	"k8s.io/klog/v2"

	utilssh "github.com/faroshq/faros-kedge/pkg/util/ssh"
)

// buildAgentProxyHandler creates the HTTP handler for accessing agent resources.
// Subresources: k8s (kubectl proxy), ssh (web terminal), exec, logs.
//
// Authentication: requires a valid bearer token (OIDC or SA). When kcp is
// configured and an SA token is provided, the token is verified against kcp
// by checking access to the site resource.
func (p *virtualWorkspaces) buildAgentProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Authenticate: require a valid bearer token.
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Parse path: /services/agent-proxy/{cluster}/apis/kedge.faros.sh/v1alpha1/sites/{name}/{subresource}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")

		if len(parts) < 2 {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		// Find subresource from path
		var clusterName, siteName, subresource string
		for i, part := range parts {
			if part == "sites" && i+2 < len(parts) {
				siteName = parts[i+1]
				subresource = parts[i+2]
				break
			}
		}

		// Get cluster from query or path
		clusterName = r.URL.Query().Get("cluster")
		if clusterName == "" {
			clusterName = "default"
		}

		if siteName == "" {
			http.Error(w, "site name required", http.StatusBadRequest)
			return
		}

		// 2. Delegated authorization: TokenReview + SubjectAccessReview via kcp.
		// Checks that the caller can "proxy" the site (same verb as faros-core agent proxy).
		if p.kcpConfig != nil {
			if claims, ok := parseServiceAccountToken(token); ok {
				if err := authorize(r.Context(), p.kcpConfig, token, claims.ClusterName, "proxy", "sites", siteName); err != nil {
					p.logger.Error(err, "agent proxy authorization failed", "site", siteName)
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
			}
			// TODO(#63): OIDC tokens bypass authorization — any valid OIDC token can access
			// any site's subresources. Add SubjectAccessReview using user.Spec.RBACIdentity
			// before allowing OIDC-authenticated requests through.
			// https://github.com/faroshq/kedge/issues/63
		}

		key := p.getKey(clusterName, siteName)

		switch subresource {
		case "k8s":
			p.k8sHandler(r.Context(), w, r, key)
		case "ssh":
			p.sshHandler(r.Context(), w, r, key)
		case "exec":
			p.k8sHandler(r.Context(), w, r, key)
		case "logs":
			p.k8sHandler(r.Context(), w, r, key)
		default:
			http.Error(w, fmt.Sprintf("unknown subresource: %s", subresource), http.StatusNotFound)
		}
	})
}

// k8sHandler reverse-proxies HTTP to the agent's local K8s API handler.
func (p *virtualWorkspaces) k8sHandler(ctx context.Context, w http.ResponseWriter, r *http.Request, key string) {
	logger := klog.FromContext(ctx)

	deviceConn, err := p.connManager.Dial(ctx, key)
	if err != nil {
		logger.Error(err, "failed to dial agent", "key", key)
		http.Error(w, "failed to connect to agent", http.StatusBadGateway)
		return
	}

	// Check if this is an upgrade request (exec, port-forward)
	if isUpgradeRequest(r) {
		p.handleK8sUpgrade(ctx, w, r, deviceConn)
		return
	}

	// Reverse proxy to agent
	transport := &deviceConnTransport{conn: deviceConn}
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "agent"
			// Strip the prefix path, forward just the k8s API path
			req.URL.Path = extractK8sPath(r.URL.Path)
		},
		Transport: transport,
	}
	proxy.ServeHTTP(w, r)
}

// sshHandler establishes an SSH session over WebSocket to the agent.
func (p *virtualWorkspaces) sshHandler(ctx context.Context, w http.ResponseWriter, r *http.Request, key string) {
	logger := klog.FromContext(ctx)

	deviceConn, err := p.connManager.Dial(ctx, key)
	if err != nil {
		logger.Error(err, "failed to dial agent for SSH", "key", key)
		http.Error(w, "failed to connect to agent", http.StatusBadGateway)
		return
	}

	// TODO(#67): CheckOrigin returns true for all origins, allowing cross-site WebSocket
	// hijacking. Replace with utilhttp.CheckSameOrAllowedOrigin (same as edge proxy).
	// https://github.com/faroshq/kedge/issues/67
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error(err, "failed to upgrade to WebSocket")
		return
	}
	defer wsConn.Close() //nolint:errcheck

	// Create SSH client through the device connection
	sshClient, err := newSSHClient(ctx, deviceConn, logger)
	if err != nil {
		logger.Error(err, "failed to create SSH client")
		return
	}
	defer sshClient.Close() //nolint:errcheck

	// Create SSH session over WebSocket
	session, err := utilssh.NewSocketSSHSession(logger, 120, 40, sshClient, wsConn)
	if err != nil {
		logger.Error(err, "failed to create SSH session")
		return
	}
	defer session.Close()

	if err := session.Run(ctx); err != nil {
		logger.Error(err, "SSH session error")
	}
}

// handleK8sUpgrade handles upgrade requests (exec, port-forward) by hijacking
// the connection and doing bidirectional copy.
func (p *virtualWorkspaces) handleK8sUpgrade(ctx context.Context, w http.ResponseWriter, r *http.Request, deviceConn net.Conn) {
	logger := klog.FromContext(ctx)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		logger.Error(err, "failed to hijack connection")
		return
	}
	defer clientConn.Close() //nolint:errcheck
	defer deviceConn.Close() //nolint:errcheck

	// Write the original request to the device connection
	if err := r.Write(deviceConn); err != nil {
		logger.Error(err, "failed to write request to agent")
		return
	}

	// Bidirectional copy
	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(deviceConn, clientConn)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(clientConn, deviceConn)
		errc <- err
	}()

	// Wait for one side to finish
	<-errc
}

// deviceConnTransport implements http.RoundTripper using a device connection.
type deviceConnTransport struct {
	conn net.Conn
}

func (t *deviceConnTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Write(t.conn); err != nil {
		return nil, err
	}
	return http.ReadResponse(bufio.NewReader(t.conn), req)
}

// newSSHClient creates an SSH client through a device connection.
func newSSHClient(_ context.Context, deviceConn net.Conn, _ klog.Logger) (*gossh.Client, error) {
	// TODO(#64): InsecureIgnoreHostKey accepts any SSH host key — MITM risk.
	// Store a known-good public key in the Site/Server CRD at registration time
	// and use gossh.FixedHostKey or a custom HostKeyCallback here.
	// https://github.com/faroshq/kedge/issues/64
	//
	// TODO(#64): User is hardcoded to "root". See also issue #53 for OIDC identity → SSH
	// username mapping. https://github.com/faroshq/kedge/issues/64
	sshConfig := &gossh.ClientConfig{
		User:            "root",
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // tracked in #64
	}

	sshConn, chans, reqs, err := gossh.NewClientConn(deviceConn, "agent:22", sshConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH client connection: %w", err)
	}

	return gossh.NewClient(sshConn, chans, reqs), nil
}

// isUpgradeRequest checks if the request is a protocol upgrade.
func isUpgradeRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Connection"), "Upgrade")
}

// extractK8sPath extracts the Kubernetes API path from the full URL path.
func extractK8sPath(path string) string {
	idx := strings.Index(path, "/k8s/")
	if idx >= 0 {
		return path[idx+4:]
	}
	return path
}
