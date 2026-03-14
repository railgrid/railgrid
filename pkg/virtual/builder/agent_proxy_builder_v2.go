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
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/function61/holepunch-server/pkg/wsconnadapter"
	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/apiurl"
	utilhttp "github.com/faroshq/faros-kedge/pkg/util/http"
	"github.com/faroshq/faros-kedge/pkg/util/revdial"
)

var secretGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}

// buildEdgeAgentProxyHandler creates the HTTP handler for Edge agent tunnel
// registration (the agent-facing side of the new Edge workflow).
//
// Agents connect via WebSocket to:
//
//	/services/agent-proxy/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/proxy
//
// The hub upgrades the connection, wraps it in a revdial.Dialer, and stores
// it in p.edgeConnManager keyed by "edges/{cluster}/{name}". Subsequent
// user-facing requests (buildEdgesProxyHandler) look up that dialer to open
// back-connections to the agent.
//
// A separate /proxy endpoint (relative to the mount point) handles revdial
// pick-up connections initiated by the agent side.
func (p *virtualWorkspaces) buildEdgeAgentProxyHandler() http.Handler {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return utilhttp.CheckSameOrAllowedOrigin(r, []url.URL{})
		},
	}

	mux := http.NewServeMux()

	// /proxy — revdial pick-up endpoint.
	// When the hub dials the agent (Dialer.Dial), it sends a "conn-ready"
	// message to the agent telling it to open a new WebSocket to this path.
	// The path passed to revdial.NewDialer below must match the absolute URL
	// path where this handler is mounted.
	mux.Handle("/proxy", revdial.ConnHandler(upgrader))

	// / — initial agent connection handler.
	// Path (after mount-prefix stripping):
	//   /{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/proxy
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Dispatch MCP requests before agent auth — MCP handler has its own auth.
		if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/mcp") {
			cluster, name, ok := parseEdgeMCPPath(r.URL.Path)
			if !ok {
				http.Error(w, "invalid path: expected /{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/mcp", http.StatusBadRequest)
				return
			}
			p.buildMCPHandler(cluster, name).ServeHTTP(w, r)
			return
		}

		// 1. Authenticate: require a valid bearer token.
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 2. Parse cluster and name from the URL path.
		cluster, name, ok := parseEdgeAgentPath(r.URL.Path)
		if !ok {
			http.Error(w, "invalid path: expected /{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/proxy", http.StatusBadRequest)
			return
		}

		// 3. Authentication: static tokens bypass JWT SA requirement.
		//    SA tokens go through kcp delegated authorization.
		//    Bootstrap join tokens are accepted if they match edge.Status.JoinToken.
		_, isStaticToken := p.staticTokens[token]
		// authenticatedByJoinToken tracks whether the agent was authenticated via a
		// bootstrap join token. When true, the hub echoes the token back in the
		// X-Kedge-Agent-Token upgrade response header so the agent can persist it
		// as its durable credential (token-exchange flow).
		authenticatedByJoinToken := false
		if !isStaticToken {
			if _, ok := parseServiceAccountToken(token); !ok {
				// Not a SA token — check if it's a valid bootstrap join token for this edge.
				if p.kcpConfig == nil {
					p.logger.Info("Rejected edge agent tunnel: invalid or missing SA token (no kcp configured)",
						"cluster", cluster, "name", name)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				if err := p.authorizeByJoinToken(r.Context(), token, cluster, name); err != nil {
					p.logger.Info("Rejected edge agent tunnel: invalid join token",
						"cluster", cluster, "name", name, "err", err)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				authenticatedByJoinToken = true
			} else if p.kcpConfig != nil {
				// Always use cluster from URL path — do NOT use JWT's clusterName claim
				// (it's unverified and not yet validated by kcp). The TokenReview performed
				// inside authorizeFn will reject tokens not issued for this cluster.
				// Fixes https://github.com/faroshq/kedge/issues/68
				if err := p.authorizeFn(r.Context(), p.kcpConfig, token, cluster, "get", "edges", name); err != nil {
					p.logger.Error(err, "edge agent proxy authorization failed",
						"cluster", cluster, "name", name)
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
			}
		}

		// 4. Upgrade to WebSocket.
		// When the agent authenticated via a bootstrap join token, build a minimal
		// kubeconfig and include it in the upgrade response so the agent can save it
		// as its durable credential and reconnect without the join token on restart.
		var upgradeHeaders http.Header
		kubeconfigDelivered := false
		if authenticatedByJoinToken {
			kubeconfigHeader := p.buildAgentKubeconfigHeader(cluster, name, token)
			upgradeHeaders = http.Header{}
			if kubeconfigHeader != "" {
				upgradeHeaders.Set("X-Kedge-Agent-Kubeconfig", kubeconfigHeader)
				kubeconfigDelivered = true
			}
		}
		wsConn, err := upgrader.Upgrade(w, r, upgradeHeaders)
		if err != nil {
			p.logger.Error(err, "failed to upgrade WebSocket connection",
				"cluster", cluster, "name", name)
			return
		}

		// 5. Register the revdial tunnel.
		// The pick-up path must match the absolute path at which the /proxy
		// endpoint is reachable (i.e. the mount point + /proxy).
		key := edgeConnKey(cluster, name)
		p.logger.Info("Edge agent connecting", "key", key)

		conn := wsconnadapter.New(wsConn)
		dialer := revdial.NewDialer(conn, "/services/agent-proxy/proxy")
		p.edgeConnManager.Store(key, dialer)
		p.logger.Info("Edge agent tunnel established", "key", key)

		// The hub is authoritative for edge connectivity state regardless of how
		// the agent authenticated.  In the join-token flow the agent's
		// edge_reporter cannot reach the kcp API directly (the join token is not
		// a valid kcp credential).  In the kubeconfig flow (e.g. after an
		// in-cluster pod restart where the agent loads its saved kubeconfig from
		// a Secret) the edge_reporter may fail due to RBAC propagation lag.
		// Marking the edge Ready here on every tunnel open is safe and ensures
		// the hub view is always up-to-date.
		// SSH credentials are passed via headers for server-type edges.
		//
		// clearJoinToken: only clear the bootstrap join token if we successfully
		// delivered a kubeconfig to the agent. If the RBAC controller hasn't
		// provisioned the SA secret yet, the agent won't have a durable credential
		// and needs the join token to remain valid for the next reconnect attempt.
		clearJoinToken := !authenticatedByJoinToken || kubeconfigDelivered
		sshCreds := extractSSHCredsFromHeaders(r)
		go p.markEdgeConnected(context.Background(), cluster, name, sshCreds, clearJoinToken)

		// Block until the tunnel closes, then clean up the entry so stale
		// look-ups don't succeed.
		<-dialer.Done()
		p.edgeConnManager.Delete(key)
		p.logger.Info("Edge agent tunnel closed", "key", key)

		// Proactively mark the Edge as Disconnected in the hub.  Agents may die
		// without sending a clean disconnect heartbeat (e.g. SIGKILL), so the
		// hub must be the authoritative source for connectivity state.
		go p.markEdgeDisconnected(context.Background(), cluster, name)
	})

	return mux
}

// parseEdgeAgentPath extracts {cluster} and {name} from the path that the
// handler sees after the "/services/agent-proxy" prefix has been stripped.
//
// Expected format:
//
//	/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/proxy
func parseEdgeAgentPath(path string) (cluster, name string, ok bool) {
	// Strip any leading slash.
	path = strings.TrimPrefix(path, "/")

	// Expected segments:
	//   [0] cluster
	//   [1] "apis"
	//   [2] "kedge.faros.sh"
	//   [3] "v1alpha1"
	//   [4] "edges"
	//   [5] name
	//   [6] "proxy"
	parts := strings.SplitN(path, "/", 8) // cap at 8 to handle extra segments gracefully
	if len(parts) < 7 {
		return "", "", false
	}
	if parts[1] != "apis" || parts[2] != "kedge.faros.sh" ||
		parts[3] != "v1alpha1" || parts[4] != "edges" || parts[6] != "proxy" {
		return "", "", false
	}
	return parts[0], parts[5], true
}

// parseEdgeMCPPath extracts {cluster} and {name} from the path seen by the
// agent-proxy handler for MCP requests.
//
// Expected format:
//
//	/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/mcp
func parseEdgeMCPPath(path string) (cluster, name string, ok bool) {
	// Strip any leading slash.
	path = strings.TrimPrefix(path, "/")

	// Expected segments:
	//   [0] cluster
	//   [1] "apis"
	//   [2] "kedge.faros.sh"
	//   [3] "v1alpha1"
	//   [4] "edges"
	//   [5] name
	//   [6] "mcp"
	parts := strings.SplitN(path, "/", 8) // cap at 8 to handle extra segments gracefully
	if len(parts) < 7 {
		return "", "", false
	}
	if parts[1] != "apis" || parts[2] != "kedge.faros.sh" ||
		parts[3] != "v1alpha1" || parts[4] != "edges" || parts[6] != "mcp" {
		return "", "", false
	}
	return parts[0], parts[5], true
}

// edgeConnKey returns the ConnManager key for an Edge tunnel.
// Format: "edges/{cluster}/{name}"
func edgeConnKey(cluster, name string) string {
	return "edges/" + cluster + "/" + name
}

// buildAgentKubeconfigHeader reads the ServiceAccount token from the kubeconfig
// secret created by the RBAC controller, builds a minimal kubeconfig with it,
// and returns the result base64-encoded for the X-Kedge-Agent-Kubeconfig header.
// Returns an empty string if the SA token is not yet available.
func (p *virtualWorkspaces) buildAgentKubeconfigHeader(cluster, edgeName, _ string) string {
	if p.kcpConfig == nil {
		p.logger.Info("Cannot build agent kubeconfig: no kcp config")
		return ""
	}

	// Read the SA token from the kubeconfig secret created by the RBAC controller.
	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = apiurl.HubServerURL(cfg.Host, cluster)
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		p.logger.Error(err, "failed to create dynamic client for SA token lookup")
		return ""
	}

	secretName := "edge-" + edgeName + "-kubeconfig"
	secret, err := dynClient.Resource(secretGVR).Namespace("kedge-system").Get(
		context.Background(), secretName, metav1.GetOptions{})
	if err != nil {
		p.logger.Error(err, "failed to get kubeconfig secret for token-exchange",
			"secret", "kedge-system/"+secretName)
		return ""
	}

	tokenB64, found, _ := unstructured.NestedString(secret.Object, "data", "token")
	if !found || tokenB64 == "" {
		p.logger.Info("SA token not yet populated in kubeconfig secret", "secret", secretName)
		return ""
	}
	tokenBytes, err := base64.StdEncoding.DecodeString(tokenB64)
	if err != nil {
		p.logger.Error(err, "failed to decode SA token from secret", "secret", secretName)
		return ""
	}
	saToken := string(tokenBytes)

	hubURL := p.hubExternalURL
	if hubURL == "" {
		hubURL = "https://localhost:8443"
	}
	kubecfg := buildAgentKubeconfig(hubURL, cluster, edgeName, saToken)
	data, err := clientcmd.Write(*kubecfg)
	if err != nil {
		p.logger.Error(err, "failed to serialise agent kubeconfig")
		return ""
	}
	return base64.StdEncoding.EncodeToString(data)
}

// buildAgentKubeconfig constructs a minimal kubeconfig that the agent can use
// to authenticate against the hub with a ServiceAccount token.
func buildAgentKubeconfig(hubURL, cluster, edgeName, token string) *clientcmdapi.Config {
	// Include the cluster path in the server URL so the agent reconnects to the
	// correct kcp logical cluster on restart (mirrors how existing agents work).
	serverURL := hubURL
	if cluster != "" && cluster != "default" {
		serverURL = strings.TrimRight(hubURL, "/") + "/clusters/" + cluster
	}
	contextName := "kedge-" + edgeName
	return &clientcmdapi.Config{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: map[string]*clientcmdapi.Cluster{
			"kedge-hub": {Server: serverURL, InsecureSkipTLSVerify: true},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			contextName: {Token: token},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default": {Cluster: "kedge-hub", AuthInfo: contextName},
		},
		CurrentContext: "default",
	}
}

// authorizeByJoinToken looks up the Edge by cluster+name and performs a
// constant-time comparison of the provided token against edge.Status.JoinToken.
// Returns nil if the token is valid, or an error otherwise.
func (p *virtualWorkspaces) authorizeByJoinToken(ctx context.Context, token, cluster, name string) error {
	if p.kcpConfig == nil {
		return fmt.Errorf("kcp config not available")
	}
	if token == "" {
		return fmt.Errorf("empty token")
	}

	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = apiurl.HubServerURL(cfg.Host, cluster)

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	u, err := dynClient.Resource(edgeGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting edge %s/%s: %w", cluster, name, err)
	}

	var edge kedgev1alpha1.Edge
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &edge); err != nil {
		return fmt.Errorf("converting edge from unstructured: %w", err)
	}

	if edge.Status.JoinToken == "" {
		return fmt.Errorf("edge %s/%s has no join token set", cluster, name)
	}

	// Constant-time comparison to prevent timing attacks.
	if subtle.ConstantTimeCompare([]byte(token), []byte(edge.Status.JoinToken)) != 1 {
		return fmt.Errorf("join token mismatch for edge %s/%s", cluster, name)
	}

	return nil
}

// sshCredsFromAgent holds SSH credentials passed by the agent via WebSocket
// upgrade headers during join-token registration.
type sshCredsFromAgent struct {
	User       string
	Password   string
	PrivateKey []byte
}

// extractSSHCredsFromHeaders reads SSH credential headers set by the agent.
func extractSSHCredsFromHeaders(r *http.Request) *sshCredsFromAgent {
	user := r.Header.Get("X-Kedge-SSH-User")
	if user == "" {
		return nil
	}
	creds := &sshCredsFromAgent{User: user}
	if pw := r.Header.Get("X-Kedge-SSH-Password"); pw != "" {
		decoded, err := base64.StdEncoding.DecodeString(pw)
		if err == nil {
			creds.Password = string(decoded)
		}
	}
	if pk := r.Header.Get("X-Kedge-SSH-PrivateKey"); pk != "" {
		decoded, err := base64.StdEncoding.DecodeString(pk)
		if err == nil {
			creds.PrivateKey = decoded
		}
	}
	return creds
}
