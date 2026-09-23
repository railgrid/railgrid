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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	utilhttp "github.com/railgrid/provider-edges/internal/wsutil"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/revdial"
)

// buildEdgeAgentProxyHandler serves Pillar 2 class (f), the agent tunnel.
//
// Agents connect via WebSocket to the grammar route, on the same shape every
// other class-(a)/(f) route uses — only the root differs, because the
// credential is the edge's own rather than a tenant caller's:
//
//	/agent/clusters/{cluster}/{resource}/{name}/proxy
//
// The provider upgrades the connection, wraps it in a revdial.Dialer, and
// stores it in p.edgeConnManager keyed by "{resource}/{cluster}/{name}".
// Consumer requests (buildEdgesProxyHandler) look up that dialer to open
// back-connections to the agent.
//
// Two further routes carry revdial pick-up connections the agent opens when
// the provider dials it. They are not grammar routes and never name an
// object: the dialer id in the query IS the capability.
//
//	/agent/proxy                 single-replica / pre-routing pickup
//	/agent/proxy/{replica}       replica-addressed pickup
func (p *Server) buildEdgeAgentProxyHandler() http.Handler {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return utilhttp.CheckSameOrAllowedOrigin(r, []url.URL{})
		},
	}

	// Dispatched on the RAW path, never through an http.ServeMux: ServeMux
	// cleans the path and answers a non-clean one with a redirect, and ".."
	// and "//" are exactly what the grammar must refuse rather than rewrite.
	pickup := revdial.ConnHandler(upgrader)
	routedPickup := p.pickupRouter(pickup)
	tunnelHandler := p.agentTunnelHandler(upgrader)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == agentPickupRoute:
			pickup.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, agentPickupRoute+"/"):
			routedPickup.ServeHTTP(w, r)
		default:
			tunnelHandler.ServeHTTP(w, r)
		}
	})
}

// agentPickupRoute is the revdial pick-up path under the class (f) mount.
const agentPickupRoute = "/" + AgentRoot + "/proxy"

// agentTunnelHandler serves /agent/clusters/{cluster}/{resource}/{name}/proxy.
func (p *Server) agentTunnelHandler(upgrader websocket.Upgrader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 0. Parse the grammar. The same parser the consumer plane uses, so a
		// malformed or traversal-carrying path is refused identically on both
		// classes; the verb is pinned to "proxy" because class (f) has one.
		req, ok := dataplane.ParseRequest(AgentRoot, r)
		if !ok || req.Verb != AgentVerb || req.Tail != "" {
			http.Error(w, "invalid path: expected /"+AgentRoot+"/clusters/{cluster}/{resource}/{name}/"+AgentVerb, http.StatusBadRequest)
			return
		}
		cluster, resource, name := req.ClusterID, req.Resource, req.Name
		gvr, kind, known := p.gvrForResource(resource)
		if !known {
			http.Error(w, "invalid path: unknown resource", http.StatusBadRequest)
			return
		}

		// 1. Authenticate: require a valid bearer token.
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 2. Authentication: SA tokens go through kcp delegated authorization;
		//    bootstrap join tokens are accepted if they match edge.Status.JoinToken.
		//    The test-only static-token set only stands in for an agent credential
		//    when there is no kcp config at all (see Config.AllowStaticTokenBypass);
		//    with kcp configured every token is validated by kcp.
		isStaticToken := false
		if p.kcpConfig == nil {
			_, isStaticToken = p.staticTokens[token]
		}
		// authenticatedByJoinToken tracks whether the agent was authenticated via a
		// bootstrap join token. When true, the hub echoes the token back in the
		// X-Railgrid-Agent-Token upgrade response header so the agent can persist it
		// as its durable credential (token-exchange flow).
		authenticatedByJoinToken := false
		if !isStaticToken {
			if !isServiceAccountJWT(token) {
				// Not a SA token — check if it's a valid bootstrap join token for this edge.
				if p.kcpConfig == nil {
					p.logger.Info("Rejected edge agent tunnel: invalid or missing SA token (no kcp configured)",
						"cluster", cluster, "name", name)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				if err := p.authorizeByJoinToken(r.Context(), gvr, token, cluster, name); err != nil {
					p.logger.Info("Rejected edge agent tunnel: invalid join token",
						"cluster", cluster, "name", name, "err", err)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				authenticatedByJoinToken = true
			} else {
				// SA token: this is a post-exchange reconnect. Validate it with a
				// delegated TokenReview + SubjectAccessReview against the consumer
				// workspace, served on the provider's APIExport virtual workspace
				// (kcp#4279 / kcp#4280). The agent SA authenticates natively where
				// it was minted, and the per-edge "proxy" grant the RBAC reconciler
				// created (ensureEdgeProxyGrant) authorizes it for THIS edge only.
				if err := p.authorizeByIssuedToken(r.Context(), gvr, cluster, name, token); err != nil {
					p.logger.Info("Rejected edge agent tunnel: SA token failed delegated authorization",
						"cluster", cluster, "name", name, "err", err)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
			}
		}

		// 3. Upgrade to WebSocket.
		//
		// An agent that authenticated with its bootstrap join token is
		// ENROLLING: this is the one moment it has no durable credential, so
		// the provider mints its scoped identity and hands the whole enrolment
		// bundle back on the upgrade response — the TTL'd token, the hub URL
		// and CA, and the addressing tuple with the routes it refreshes at.
		//
		// This replaces X-Railgrid-Agent-Kubeconfig, which shipped a legacy,
		// non-expiring ServiceAccount token the agent then wrote to disk and
		// into a Secret. Nothing here is permanent: the agent re-mints through
		// the agent-token verb at 80% of the TTL.
		var upgradeHeaders http.Header
		credentialDelivered := false
		if authenticatedByJoinToken {
			upgradeHeaders = http.Header{}
			if header, cerr := p.enrolmentHeader(r.Context(), gvr, kind, cluster, name); cerr != nil {
				// Not fatal: the tunnel is up and useful, and the join token
				// stays valid (see clearJoinToken) so the next attempt can
				// enrol. An agent with no credential simply cannot reconnect
				// on its own yet.
				p.logger.Error(cerr, "could not mint the agent credential on join; the join token stays valid for the next attempt",
					"cluster", cluster, "name", name)
			} else {
				upgradeHeaders.Set(AgentCredentialHeader, header)
				credentialDelivered = true
			}
		}
		wsConn, err := upgrader.Upgrade(w, r, upgradeHeaders)
		if err != nil {
			p.logger.Error(err, "failed to upgrade WebSocket connection",
				"cluster", cluster, "name", name)
			return
		}

		// 4. Register the revdial tunnel.
		// The pick-up path must match the absolute path at which the /proxy
		// endpoint is reachable (i.e. the mount point + /proxy).
		key := edgeConnKey(resource, cluster, name)
		p.logger.Info("Edge agent connecting", "key", key)

		conn := wsconnadapter.New(wsConn)
		// With replica routing enabled the pickup path is replica-addressed so
		// pickups reach THIS process (the dialer closes over the socket) no
		// matter which replica the Service hands them to.
		pickupPath := p.agentPickupPath
		if p.replicaID != "" {
			pickupPath += "/" + p.replicaID
		}
		dialer := revdial.NewDialer(conn, pickupPath)
		p.edgeConnManager.Store(key, dialer)
		p.logger.Info("Edge agent tunnel established", "key", key)

		// Connectivity state (status.connected / phase / lastHeartbeatTime /
		// Registered) is NOT written here. Store above claimed the edge's
		// registry Lease, which the sweeper renews while the tunnel lives; the
		// edge lifecycle reconciler (internal/edgectrl) is the single writer
		// that derives those fields from the Lease, on every replica. The
		// handler only records what it alone observes on tunnel open: joinToken
		// clearing, hostname, SSH credentials / host key (server kinds), URL.
		//
		// clearJoinToken: only clear the bootstrap join token if the agent
		// actually received a credential. If the mint failed, the agent has no
		// durable credential and needs the join token to stay valid for its
		// next attempt — clearing it there would strand the edge.
		clearJoinToken := !authenticatedByJoinToken || credentialDelivered
		// MacOSServer is Service-only, so ignore SSH headers for it and preserve
		// the existing LinuxServer/KubernetesCluster handling.
		var sshCreds *sshCredsFromAgent
		if resource != macOSServerResource {
			sshCreds = extractSSHCredsFromHeaders(r)
		}
		hostname := agentHostnameFromHeader(r)
		go p.markEdgeConnected(context.Background(), gvr, cluster, name, sshCreds, hostname, clearJoinToken)

		// Block until the tunnel closes, then clean up the entry so stale
		// look-ups don't succeed. revdial's keep-alive/pong loop detects a dead
		// tunnel within ~60s; DeleteIf then releases the Lease and the lifecycle
		// reconciler flips the edge to Disconnected. If this process dies
		// instead, the Lease simply stops being renewed and expires after
		// RegistryLeaseTTL — no hub-side status write is needed either way.
		<-dialer.Done()

		// Identity-checked: the key is stable across reconnects, so if the agent
		// has already dialled a replacement tunnel it — not us — owns this entry
		// and the edge's status. Deleting unconditionally here erased a live
		// registration and left a healthy agent unroutable until it restarted.
		if !p.edgeConnManager.DeleteIf(key, dialer) {
			p.logger.Info("Superseded edge agent tunnel closed; a newer tunnel owns this edge", "key", key)
			return
		}
		p.logger.Info("Edge agent tunnel closed", "key", key)
	})
}

// edgeConnKey returns the ConnManager key for an Edge tunnel.
// Format: "edges/{cluster}/{name}"
func edgeConnKey(resource, cluster, name string) string {
	return resource + "/" + cluster + "/" + name
}

// enrolmentHeader mints this edge's agent credential and renders it for the
// WebSocket upgrade response.
//
// The UID is read here, not taken from the request: the hub binds the identity
// to the edge's UID so a deleted and recreated edge cannot inherit its
// predecessor's credential, and the only trustworthy source of that UID is the
// object itself.
func (p *Server) enrolmentHeader(ctx context.Context, gvr schema.GroupVersionResource, kind, cluster, name string) (string, error) {
	cfg, err := p.tenantConfigFor(ctx, cluster)
	if err != nil {
		return "", fmt.Errorf("resolving tenant config: %w", err)
	}
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("creating dynamic client: %w", err)
	}
	edge, err := dynClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading %s %s/%s: %w", gvr.Resource, cluster, name, err)
	}

	credential, err := p.mintAgentCredential(ctx, gvr, kind, cluster, name, string(edge.GetUID()))
	if err != nil {
		return "", err
	}
	return credential.Encode()
}

// authorizeByJoinToken looks up the Edge by cluster+name and performs a
// constant-time comparison of the provided token against edge.Status.JoinToken.
// Returns nil if the token is valid, or an error otherwise.
func (p *Server) authorizeByJoinToken(ctx context.Context, gvr schema.GroupVersionResource, token, cluster, name string) error {
	if p.kcpConfig == nil {
		return fmt.Errorf("kcp config not available")
	}
	if token == "" {
		return fmt.Errorf("empty token")
	}

	// Resolve the Edge in its tenant workspace via the APIExport virtual
	// workspace. Re-rooting the provider's own workspace-scoped SA config would
	// be rejected by kcp, which is what broke join-token registration in prod.
	cfg, err := p.tenantConfigFor(ctx, cluster)
	if err != nil {
		return fmt.Errorf("resolving tenant config: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	u, err := dynClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting %s %s/%s: %w", gvr.Resource, cluster, name, err)
	}

	// status.joinToken is a shared ConnectionStatus field present on both kinds,
	// so read it directly from the unstructured object (kind-agnostic).
	joinToken, _, _ := unstructured.NestedString(u.Object, "status", "joinToken")
	if joinToken == "" {
		return fmt.Errorf("%s %s/%s has no join token set", gvr.Resource, cluster, name)
	}

	// Constant-time comparison to prevent timing attacks.
	if subtle.ConstantTimeCompare([]byte(token), []byte(joinToken)) != 1 {
		return fmt.Errorf("join token mismatch for %s %s/%s", gvr.Resource, cluster, name)
	}

	return nil
}

// authorizeByIssuedToken validates a reconnecting agent's ServiceAccount token
// with the standard delegated auth-delegator pattern against the consumer
// workspace, served on the provider's APIExport virtual workspace (kcp#4279 /
// kcp#4280): a TokenReview authenticates the token where it was minted, and a
// SubjectAccessReview authorizes the resolved identity for "create" on the
// virtual subresource {resource}/proxy, scoped to this edge object — the same
// coordinate every other data-plane verb is gated on.
//
// The agent SA is provisioned in the consumer workspace by the RBAC reconciler
// (rbac_reconciler.go), which also grants it a per-edge ClusterRole scoped via
// resourceNames to this edge alone (ensureEdgeProxyGrant). So the
// review proves both authenticity (a live consumer-workspace credential) and
// that it is the right edge (only this edge's SA is granted "proxy" on this
// name). Revocation is by edge deletion, which GCs the SA and its per-edge
// grant — the same model the earlier issued-token byte-compare had.
func (p *Server) authorizeByIssuedToken(ctx context.Context, gvr schema.GroupVersionResource, cluster, name, token string) error {
	if token == "" {
		return fmt.Errorf("empty token")
	}
	tenantCfg, err := p.tenantConfigFor(ctx, cluster)
	if err != nil {
		return fmt.Errorf("resolving tenant config: %w", err)
	}
	return authorize(ctx, tenantCfg, p.kcpConfig, token, cluster,
		dataplane.SSARVerb, gvr.Group, gvr.Resource, AgentVerb, name)
}

// AgentHostnameHeader is the WebSocket upgrade header on which the agent
// reports the hostname of the machine it runs on; the provider records it in
// status.hostname (edgeapi.ConnectionStatus.Hostname) on every tunnel open.
// Absent or empty leaves the recorded value untouched.
const AgentHostnameHeader = "X-Railgrid-Agent-Hostname"

// maxAgentHostnameLen bounds the agent-asserted value stored in status: a
// hostname is at most 253 characters (RFC 1035), and anything longer is not
// one.
const maxAgentHostnameLen = 253

// agentHostnameFromHeader reads the agent's reported hostname, or "" when it
// did not send one (older agents) or sent something that is not a hostname.
func agentHostnameFromHeader(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get(AgentHostnameHeader))
	if h == "" || len(h) > maxAgentHostnameLen {
		return ""
	}
	for _, c := range h {
		if c > 0x7e || c < 0x21 { // printable ASCII only, no whitespace
			return ""
		}
	}
	return h
}

// sshCredsFromAgent holds SSH credentials passed by the agent via WebSocket
// upgrade headers during join-token registration. HostKey is the agent's
// sshd host public key in authorized_keys format; it is independent of
// authentication credentials and is used by the hub for strict host-key
// verification.
type sshCredsFromAgent struct {
	User       string
	Password   string
	PrivateKey []byte
	HostKey    string
}

// extractSSHCredsFromHeaders reads SSH credential headers set by the agent.
func extractSSHCredsFromHeaders(r *http.Request) *sshCredsFromAgent {
	user := r.Header.Get("X-Railgrid-SSH-User")
	if user == "" {
		return nil
	}
	creds := &sshCredsFromAgent{User: user}
	if pw := r.Header.Get("X-Railgrid-SSH-Password"); pw != "" {
		decoded, err := base64.StdEncoding.DecodeString(pw)
		if err == nil {
			creds.Password = string(decoded)
		}
	}
	if pk := r.Header.Get("X-Railgrid-SSH-PrivateKey"); pk != "" {
		decoded, err := base64.StdEncoding.DecodeString(pk)
		if err == nil {
			creds.PrivateKey = decoded
		}
	}
	if hk := r.Header.Get("X-Railgrid-SSH-HostKey"); hk != "" {
		decoded, err := base64.StdEncoding.DecodeString(hk)
		if err == nil {
			creds.HostKey = string(decoded)
		}
	}
	return creds
}
