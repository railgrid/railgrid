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
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"
	utilssh "github.com/faroshq/provider-edges/internal/ssh"
	utilhttp "github.com/faroshq/provider-edges/internal/wsutil"
)

// buildEdgesProxyHandler creates the HTTP handler for user-facing access to
// Edge resources (the user-side of the new Edge workflow).
//
// Path (relative to /services/edges-proxy/ mount point):
//
//	/clusters/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/edges/{name}/{subresource}[/...]
//
// Supported subresources:
//   - k8s  — reverse-proxy to the Kubernetes API of a type=kubernetes edge
//   - ssh  — WebSocket SSH terminal session on a type=server edge
func (p *Server) buildEdgesProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Authenticate: require a valid bearer token.
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 1b. Service subresources (proxy/mcp) are branched here BEFORE
		// parseEdgesProxyPath: "services" is not a tunnel Kind, so that
		// parser (which validates against gvrForResource) would reject it.
		if esCluster, esName, esSub, esRest, ok := p.parseServicePath(r.URL.Path); ok {
			p.serveService(w, r, token, esCluster, esName, esSub, esRest)
			return
		}

		// 2. Parse cluster, resource (kind), name, and subresource from the URL path.
		cluster, resource, name, subresource, ok := p.parseEdgesProxyPath(r.URL.Path)
		if !ok {
			http.Error(w, "invalid path: expected /clusters/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/{kubernetesclusters|linuxservers}/{name}/{subresource}[/...]", http.StatusBadRequest)
			return
		}

		// 3. Delegated authorization via kcp (if configured).
		// Static tokens bypass authorizeFn entirely — they are pre-authenticated
		// server-side credentials that do not go through kcp SubjectAccessReview.
		_, isStaticToken := p.staticTokens[token]
		if !isStaticToken && p.kcpConfig != nil {
			tenantCfg, err := p.tenantConfigFor(r.Context(), cluster)
			if err != nil {
				p.logger.Error(err, "edges proxy authorization: resolving tenant config failed",
					"cluster", cluster, "name", name, "subresource", subresource)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			if err := p.authorizeFn(r.Context(), tenantCfg, p.kcpConfig, token, cluster, "proxy", p.group, resource, name); err != nil {
				p.logger.Error(err, "edges proxy authorization failed",
					"cluster", cluster, "name", name, "subresource", subresource)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}

		// 4. Look up the dialer registered by the agent-proxy-v2 handler.
		key := edgeConnKey(resource, cluster, name)
		dialer, found := p.edgeConnManager.Load(key)
		if !found {
			p.logger.Info("no active tunnel found for edge", "cluster", cluster, "name", name)
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}

		// 5. Route to the appropriate subresource handler.
		switch subresource {
		case "k8s":
			p.edgesK8sHandler(r.Context(), w, r, key, dialer)
		case "ssh":
			// Resolve caller identity for identity-mode SSH mapping.
			// Best-effort: empty string is fine for inherited/provided modes.
			callerIdentity := resolveCallerIdentity(r.Context(), p.kcpConfig, token, p.logger)
			gvr, _, _ := p.gvrForResource(resource)
			p.edgesSSHHandler(r.Context(), w, r, key, dialer, callerIdentity, gvr)
		default:
			p.logger.Info("unknown subresource requested", "subresource", subresource, "cluster", cluster, "name", name)
			http.Error(w, "unknown subresource", http.StatusNotFound)
		}
	})
}

// edgesK8sHandler reverse-proxies HTTP to the edge agent's local K8s API.
// It dials the agent via the revdial.Dialer obtained from edgeConnManager.
func (p *Server) edgesK8sHandler(ctx context.Context, w http.ResponseWriter, r *http.Request, key string, dialer interface {
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
func (p *Server) edgesSSHHandler(ctx context.Context, w http.ResponseWriter, r *http.Request, key string, dialer interface {
	Dial(context.Context) (net.Conn, error)
}, callerIdentity string, gvr schema.GroupVersionResource) {
	logger := klog.FromContext(ctx)

	// Parse cluster and edge name from the key (format: "edges/{cluster}/{name}")
	cluster, edgeName := parseEdgeConnKey(key)

	// Optional non-interactive exec mode (e.g. `kedge ssh <name> -- <cmd>`).
	remoteCmd := r.URL.Query().Get("cmd")

	// Fetch SSH credentials from Edge status, applying the configured user mapping.
	creds, err := p.fetchSSHCredentials(ctx, cluster, edgeName, callerIdentity, gvr, logger)
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

	// The consumer terminal connects from the portal, which is served at the
	// hub's external origin — NOT at this provider's host (the request reaches us
	// through the hub backend proxy, so r.Host is the internal provider address).
	// Allow the hub external origin in addition to same-origin. This request is
	// already authenticated by the bearer token in the query param, so the origin
	// check is defense-in-depth, not the primary auth boundary.
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return utilhttp.CheckSameOrAllowedOrigin(r, allowedOriginsFor(p.hubExternalURL))
		},
	}
	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error(err, "failed to upgrade caller connection to WebSocket")
		return
	}
	defer wsConn.Close() //nolint:errcheck

	// Extract the host key from the credentials (may be empty for older agents).
	var sshHostKey string
	if creds != nil {
		sshHostKey = creds.SSHHostKey
	}

	// Build the SSH client over the tunnelled raw connection.
	sshClient, err := newSSHClient(ctx, sshConn, creds, sshHostKey, logger)
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
	if len(parts) >= 3 {
		return parts[1], parts[2]
	}
	return "", ""
}

// fetchSSHCredentials retrieves SSH credentials for the edge, applying the
// configured SSHUserMapping mode.  callerIdentity is the kcp/OIDC username of
// the caller and is required when SSHUserMapping=identity.
func (p *Server) fetchSSHCredentials(ctx context.Context, cluster, edgeName, callerIdentity string, gvr schema.GroupVersionResource, logger klog.Logger) (*SSHClientCredentials, error) {
	if p.kcpConfig == nil {
		logger.V(4).Info("No kcp config, skipping credential fetch")
		return nil, nil
	}

	// Create cluster-scoped clients via the APIExport virtual workspace (the
	// provider SA cannot read tenant Edge/Secret objects by re-rooting its own
	// workspace-scoped config).
	clusterConfig, err := p.tenantConfigFor(ctx, cluster)
	if err != nil {
		return nil, fmt.Errorf("resolving tenant config: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(clusterConfig)
	if err != nil {
		return nil, fmt.Errorf("creating cluster-scoped dynamic client: %w", err)
	}

	k8sClient, err := kubernetes.NewForConfig(clusterConfig)
	if err != nil {
		return nil, fmt.Errorf("creating cluster-scoped k8s client: %w", err)
	}

	// SSH is a server-kind concern; fetch this Server's configured kind (the
	// edges-servers provider configures it with the LinuxServer GVR) and decode
	// only the ssh-relevant fields into a local view — the SDK stays independent
	// of any provider's concrete type.
	u, err := dynClient.Resource(gvr).Get(ctx, edgeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("fetching edge %s: %w", edgeName, err)
	}
	edge := &sshEdgeView{Name: edgeName}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, edge); err != nil {
		return nil, fmt.Errorf("decoding edge %s: %w", edgeName, err)
	}

	// SSHHostKey is carried through regardless of mapping mode.
	hostKey := edge.Status.SSHHostKey

	switch edge.Spec.SSHUserMapping {
	case edgeapi.SSHUserMappingProvided:
		// Use credentials entirely from spec.sshCredentialsRef.
		ref := edge.Spec.SSHCredentialsRef
		if ref == nil {
			return nil, fmt.Errorf("sshUserMapping=provided but spec.sshCredentialsRef is not set for linuxserver %s", edgeName)
		}
		creds, err := p.readSSHCredsFromSecret(ctx, k8sClient, ref, "", logger)
		if err != nil {
			return nil, err
		}
		if creds != nil {
			creds.SSHHostKey = hostKey
		}
		return creds, nil

	case edgeapi.SSHUserMappingIdentity:
		// Username = caller identity; key from sshCredentialsRef or status creds.
		if callerIdentity == "" {
			return nil, fmt.Errorf("sshUserMapping=identity but caller identity is empty for edge %s", edgeName)
		}
		if ref := edge.Spec.SSHCredentialsRef; ref != nil {
			creds, err := p.readSSHCredsFromSecret(ctx, k8sClient, ref, callerIdentity, logger)
			if err != nil {
				return nil, err
			}
			if creds != nil {
				creds.SSHHostKey = hostKey
			}
			return creds, nil
		}
		// Fall back to status credentials but override the username.
		creds, err := p.readStatusSSHCreds(ctx, k8sClient, edge, logger)
		if err != nil {
			return nil, err
		}
		if creds == nil {
			return nil, fmt.Errorf("sshUserMapping=identity: no credentials available for edge %s (set sshCredentialsRef or ensure agent reports SSHCredentials)", edgeName)
		}
		creds.Username = callerIdentity
		creds.SSHHostKey = hostKey
		return creds, nil

	default:
		// "inherited" (or empty default) → existing behavior: use agent-reported creds.
		creds, err := p.readStatusSSHCreds(ctx, k8sClient, edge, logger)
		if err != nil {
			return nil, err
		}
		if creds != nil {
			creds.SSHHostKey = hostKey
		}
		return creds, nil
	}
}

// sshEdgeView is the ssh-relevant projection of a server-kind CR (e.g.
// LinuxServer). The SDK decodes the unstructured object into this local view so
// it need not import any provider's concrete type. Field paths mirror the
// LinuxServer CRD (spec.sshUserMapping / spec.sshCredentialsRef and
// status.sshHostKey / status.sshCredentials).
// allowedOriginsFor parses the hub external URL into the allowed-origin list for
// the consumer-egress WebSocket upgrader. Returns an empty slice (same-origin
// only) when the URL is unset or unparseable.
func allowedOriginsFor(hubExternalURL string) []url.URL {
	if hubExternalURL == "" {
		return nil
	}
	u, err := url.Parse(hubExternalURL)
	if err != nil || u.Host == "" {
		return nil
	}
	return []url.URL{*u}
}

type sshEdgeView struct {
	// Name is set by the caller (metadata.name), for logging. It MUST be
	// exported with json:"-": runtime.DefaultUnstructuredConverter.FromUnstructured
	// reflects over every struct field and panics ("reflect.Value.Set using value
	// obtained using unexported field") on an unexported field; json:"-" keeps the
	// converter from trying to populate it from the object.
	Name string `json:"-"`
	Spec struct {
		SSHUserMapping    edgeapi.SSHUserMappingMode `json:"sshUserMapping,omitempty"`
		SSHCredentialsRef *corev1.SecretReference    `json:"sshCredentialsRef,omitempty"`
	} `json:"spec"`
	Status struct {
		SSHHostKey     string                  `json:"sshHostKey,omitempty"`
		SSHCredentials *edgeapi.SSHCredentials `json:"sshCredentials,omitempty"`
	} `json:"status"`
}

// readStatusSSHCreds reads SSH credentials from status.sshCredentials
// and dereferences the referenced secrets.
func (p *Server) readStatusSSHCreds(ctx context.Context, k8sClient kubernetes.Interface, edge *sshEdgeView, logger klog.Logger) (*SSHClientCredentials, error) {
	if edge.Status.SSHCredentials == nil {
		logger.V(4).Info("No SSH credentials in edge status", "edge", edge.Name)
		return nil, nil
	}

	creds := &SSHClientCredentials{
		Username: edge.Status.SSHCredentials.Username,
	}

	if ref := edge.Status.SSHCredentials.PasswordSecretRef; ref != nil {
		secret, err := k8sClient.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("fetching password secret %s/%s: %w", ref.Namespace, ref.Name, err)
		}
		if pw, ok := secret.Data["password"]; ok {
			creds.Password = string(pw)
		}
	}

	if ref := edge.Status.SSHCredentials.PrivateKeySecretRef; ref != nil {
		secret, err := k8sClient.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("fetching private key secret %s/%s: %w", ref.Namespace, ref.Name, err)
		}
		if key, ok := secret.Data["privateKey"]; ok {
			creds.PrivateKey = key
		}
	}

	logger.V(4).Info("Fetched SSH credentials from status", "edge", edge.Name, "user", creds.Username,
		"hasPassword", creds.Password != "", "hasPrivateKey", len(creds.PrivateKey) > 0)
	return creds, nil
}

func (p *Server) readSSHCredsFromSecret(ctx context.Context, k8sClient kubernetes.Interface, ref *corev1.SecretReference, usernameOverride string, logger klog.Logger) (*SSHClientCredentials, error) {
	secret, err := k8sClient.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("fetching SSH credentials secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	creds := &SSHClientCredentials{}

	if usernameOverride != "" {
		creds.Username = usernameOverride
	} else if u, ok := secret.Data["username"]; ok {
		creds.Username = string(u)
	}

	if pk, ok := secret.Data["privateKey"]; ok {
		creds.PrivateKey = pk
	}
	if pw, ok := secret.Data["password"]; ok {
		creds.Password = string(pw)
	}

	logger.V(4).Info("Fetched SSH credentials from secret", "secret", ref.Name, "namespace", ref.Namespace,
		"user", creds.Username, "hasPassword", creds.Password != "", "hasPrivateKey", len(creds.PrivateKey) > 0)
	return creds, nil
}

// resolveCallerIdentity performs a kcp TokenReview to extract the caller's username.
// Returns empty string on any error (non-fatal: inherited/provided modes don't need it).
func resolveCallerIdentity(ctx context.Context, kcpConfig *rest.Config, token string, logger klog.Logger) string {
	if kcpConfig == nil || token == "" {
		return ""
	}
	client, err := kubernetes.NewForConfig(kcpConfig)
	if err != nil {
		logger.V(4).Info("resolveCallerIdentity: failed to create client", "err", err)
		return ""
	}
	// This runs on the SSH data-plane path BEFORE the tunnel dial, and the
	// request context is a WebSocket upgrade with no deadline. A TokenReview that
	// hangs (e.g. cross-shard routing) would block the whole SSH session before
	// the agent is ever dialed — the browser terminal just shows "session ended".
	// The caller identity is only an optimization for identity-mode SSH mapping;
	// bound it hard and fall back to empty (inherited/provided modes still work).
	trCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tr := &authv1.TokenReview{
		Spec: authv1.TokenReviewSpec{Token: token},
	}
	result, err := client.AuthenticationV1().TokenReviews().Create(trCtx, tr, metav1.CreateOptions{})
	if err != nil || !result.Status.Authenticated {
		logger.V(4).Info("resolveCallerIdentity: token review failed or unauthenticated", "err", err)
		return ""
	}
	return result.Status.User.Username
}

// edgesHandleK8sUpgrade handles upgrade requests (exec, port-forward) to an
// edge agent by hijacking the client connection and doing a bidirectional copy.
func (p *Server) edgesHandleK8sUpgrade(ctx context.Context, w http.ResponseWriter, r *http.Request, deviceConn net.Conn) {
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

	// Rewrite the URL path to the /k8s/... form the agent's mux expects.
	// Without this the agent router sees the full hub path and returns 404.
	r.URL.Path = extractEdgeK8sPath(r.URL.Path)
	r.RequestURI = r.URL.RequestURI()

	// Strip user credentials before forwarding to the edge agent to prevent
	// the user's OIDC token from unnecessarily transiting the reverse tunnel.
	r.Header.Del("Authorization")

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

// parseEdgesProxyPath extracts {cluster}, {resource}, {name}, and {subresource}
// from the path the handler sees after "/services/providers/edges/edgeproxy"
// has been stripped (hub backend proxy strips /services/providers/edges, the
// provider mux strips /edgeproxy).
//
// Expected format:
//
//	/clusters/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/{kubernetesclusters|linuxservers}/{name}/{subresource}[/...]
func (p *Server) parseEdgesProxyPath(path string) (cluster, resource, name, subresource string, ok bool) {
	// Segments: [0]clusters [1]cluster [2]apis [3]group [4]version [5]resource
	//           [6]name [7]subresource (may have more after for k8s pass-through)
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 9)
	if len(parts) < 8 {
		return "", "", "", "", false
	}
	if _, _, known := p.gvrForResource(parts[5]); !known {
		return "", "", "", "", false
	}
	if parts[0] != "clusters" || parts[2] != "apis" || parts[3] != p.group ||
		parts[4] != p.version {
		return "", "", "", "", false
	}
	return parts[1], parts[5], parts[6], parts[7], true
}

// edgeProxyStatusURL builds the public consumer-egress path stamped into an
// edge's status.URL. It is the inverse of parseEdgesProxyPath, prefixed with
// the public path the hub backend proxy mounts this provider at
// (edgeProxyPublicPath, e.g. /services/providers/edges/edgeproxy). CLI clients
// read status.URL, swap in the hub host, and land back on the {k8s|ssh}
// subresource handler here.
//
// The default subresource is derived from the kind: KubernetesCluster is
// reached over "k8s" (its Kubernetes API), LinuxServer over "ssh". Returns ""
// when edgeProxyPublicPath is unset, so callers skip stamping.
//
// Pattern: {edgeProxyPublicPath}/clusters/{cluster}/apis/{group}/{version}/{resource}/{name}/{subresource}
func (p *Server) edgeProxyStatusURL(gvr schema.GroupVersionResource, cluster, name string) string {
	if p.edgeProxyPublicPath == "" {
		return ""
	}
	subresource := "k8s"
	if gvr.Resource == "linuxservers" {
		subresource = "ssh"
	}
	return fmt.Sprintf("%s/clusters/%s/apis/%s/%s/%s/%s/%s",
		strings.TrimRight(p.edgeProxyPublicPath, "/"),
		cluster, gvr.Group, gvr.Version, gvr.Resource, name, subresource)
}

// extractEdgeK8sPath strips the edges-proxy prefix from the request path,
// keeping the /k8s/ prefix that the agent expects.
//
// Input:  /clusters/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/edges/{name}/k8s/api/v1/pods
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
