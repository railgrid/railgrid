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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-edges/internal/agentidentity"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/identityclient"
)

// AgentCredentialHeader carries the enrolment bundle on the WebSocket upgrade
// response of a join-token connect. It replaces X-Railgrid-Agent-Kubeconfig.
//
// The old header shipped a kubeconfig whose AuthInfo held a legacy
// ServiceAccount token that never expired, which the agent then wrote to disk
// and, in Kubernetes mode, into a Secret in its own cluster. Three copies of a
// permanent credential, and nothing that could revoke any of them.
//
// What travels now is an addressing bundle plus a TTL'd token: where the hub
// is, which object this agent is, and a credential that stops working. The
// agent re-mints it through the provider (see AgentTokenVerb) rather than
// persisting something permanent.
const AgentCredentialHeader = "X-Railgrid-Agent-Credential"

// AgentCredential is the enrolment bundle, base64(JSON) in the header above
// and the body of an agent-token refresh.
//
// It is deliberately self-describing: an agent that holds one knows the hub
// URL, the exact route to refresh at and when to do it, so none of that is
// compiled into the agent or guessed from a format string (contract 3,
// rule 5).
type AgentCredential struct {
	// Token is the bearer the agent presents on every reconnect and on every
	// data-plane call it makes. It is TokenRequest-minted by the hub and it
	// expires.
	Token     string    `json:"token"`
	TokenType string    `json:"tokenType"`
	ExpiresAt time.Time `json:"expiresAt"`

	// HubURL is where the agent reaches the platform. It is what the agent
	// used to read out of the kubeconfig's cluster stanza.
	HubURL string `json:"hubURL"`
	// CACertData is the hub's serving CA (PEM), when the provider has one.
	// Empty means "trust the system pool", and a dev provider may say so.
	CACertData []byte `json:"caCertData,omitempty"`

	// The addressing tuple: which object this credential is for.
	Provider  string `json:"provider"`
	ClusterID string `json:"clusterID"`
	Resource  string `json:"resource"`
	Name      string `json:"name"`

	// RefreshPath is the hub-relative route the agent re-mints at, rendered by
	// the provider that serves it. The agent appends nothing and parses
	// nothing.
	RefreshPath string `json:"refreshPath"`
	// SSHCredentialsPath is the route a host agent hands its SSH credentials
	// to, empty for a kind that has no SSH data plane.
	SSHCredentialsPath string `json:"sshCredentialsPath,omitempty"`
}

// Encode renders the bundle for the upgrade header.
func (c AgentCredential) Encode() (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encoding the agent credential: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// DecodeAgentCredential parses what Encode produced. It lives here so the
// agent and the provider cannot drift on the wire format.
func DecodeAgentCredential(encoded string) (AgentCredential, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return AgentCredential{}, fmt.Errorf("decoding the agent credential: %w", err)
	}
	var credential AgentCredential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return AgentCredential{}, fmt.Errorf("parsing the agent credential: %w", err)
	}
	if strings.TrimSpace(credential.Token) == "" {
		return AgentCredential{}, fmt.Errorf("the agent credential carries no token")
	}
	return credential, nil
}

// SetIdentityClient wires the hub scoped-identity client the tunnel mints
// agent credentials through. Nil leaves the join path without a credential to
// hand out, which is a dev/test posture: the agent then keeps using whatever
// it connected with.
func (s *Server) SetIdentityClient(client *identityclient.Client) { s.identities = client }

// mintAgentCredential asks the hub for this edge's scoped identity and renders
// the enrolment bundle.
//
// It is the ONE place a token is produced, shared by the join path (where the
// agent authenticated with its bootstrap join token) and the agent-token verb
// (where it authenticated with the credential this returned last time). Ensure
// is idempotent on the owner tuple, so a refresh reuses the same hub-side
// ServiceAccount and only the token is new — which is what makes the refresh a
// rotation rather than an accumulation of identities.
func (p *Server) mintAgentCredential(ctx context.Context, gvr schema.GroupVersionResource, kind, cluster, name, uid string) (AgentCredential, error) {
	if p.identities == nil {
		return AgentCredential{}, fmt.Errorf("no hub identity client configured")
	}
	if uid == "" {
		// The hub verifies the owner exists WITH THIS UID before it mints
		// anything; an empty one would be refused, and guessing would defeat
		// the point of binding the credential to this incarnation of the edge.
		return AgentCredential{}, fmt.Errorf("edge %s/%s has no UID to bind the identity to", cluster, name)
	}

	owner := agentidentity.OwnerFor(gvr, kind, name, uid)
	owner.ClusterID = cluster
	token, err := p.identities.Ensure(ctx, identityclient.Request{
		Owner:     owner,
		ClusterID: cluster,
		Rules:     agentidentity.Rules(gvr, name),
		TTL:       agentidentity.TokenTTL * time.Second,
	})
	if err != nil {
		return AgentCredential{}, fmt.Errorf("minting the agent identity: %w", err)
	}

	credential := AgentCredential{
		Token:       token.Token,
		TokenType:   token.TokenType,
		ExpiresAt:   token.ExpiresAt,
		HubURL:      p.hubExternalURL,
		CACertData:  p.hubCAData,
		Provider:    agentidentity.ProviderName,
		ClusterID:   cluster,
		Resource:    gvr.Resource,
		Name:        name,
		RefreshPath: p.publicVerbPath(cluster, gvr.Resource, name, VerbAgentToken),
	}
	if verbServed(gvr.Resource, VerbSSHCredentials) {
		credential.SSHCredentialsPath = p.publicVerbPath(cluster, gvr.Resource, name, VerbSSHCredentials)
	}
	return credential, nil
}

// publicVerbPath renders a hub-relative data-plane route for the agent, from
// the same public base an edge's status.URL is stamped from.
func (p *Server) publicVerbPath(cluster, resource, name, verb string) string {
	if p.edgeProxyPublicPath == "" {
		return ""
	}
	return edgeProxyPath(p.edgeProxyPublicPath, cluster, resource, name, verb)
}

// serveAgentToken handles the agent-token verb: an agent refreshing its own
// credential.
//
// Authorization is the ordinary two gates, run before this is reached, with
// the agent's CURRENT token as the caller: gate 1 is a real GET of its own
// edge, gate 2 an SSAR for create on {resource}/agent-token, name-scoped. Both
// are satisfied by exactly one identity in the workspace — this edge's — so an
// agent can refresh its own credential and nothing else's.
//
// The agent cannot call the hub identity service itself: that endpoint
// authenticates providers, and an edge agent is not one. This verb is the
// provider standing in for it, which is why the re-mint below runs with the
// PROVIDER's identity client while the authorization ran entirely as the
// agent.
func (p *Server) serveAgentToken(w http.ResponseWriter, r *http.Request, req dataplane.Request, edge *unstructured.Unstructured) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	gvr, kind, ok := p.gvrForResource(req.Resource)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if p.identities == nil {
		p.logger.Info("refusing an agent-token refresh: no hub identity client configured",
			"cluster", req.ClusterID, "edge", req.Name)
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
		return
	}

	// The UID comes off the object gate 1 read AS THE AGENT, so the credential
	// is bound to the incarnation the caller could actually see.
	credential, err := p.mintAgentCredential(r.Context(), gvr, kind, req.ClusterID, req.Name, string(edge.GetUID()))
	if err != nil {
		p.logger.Error(err, "agent-token refresh failed", "cluster", req.ClusterID, "edge", req.Name)
		http.Error(w, "could not mint a credential", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(credential); err != nil {
		p.logger.Error(err, "writing the refreshed agent credential")
	}
}

// sshCredentialsRequest is what a host agent POSTs to the ssh-credentials
// verb. The agent holds these because it generated or was configured with
// them locally; it has never been able to prove anything about them, which is
// why the provider records them against the edge the caller just proved it is.
type sshCredentialsRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	PrivateKey []byte `json:"privateKey,omitempty"`
	// HostKey is the machine's sshd host public key in authorized_keys form,
	// for strict host-key verification. Independent of the credentials above.
	HostKey string `json:"hostKey,omitempty"`
}

// maxSSHCredentialsBody bounds the request: a username, a password and a
// private key, not an upload channel.
const maxSSHCredentialsBody = 1 << 20

// serveSSHCredentials handles the ssh-credentials verb: a host agent handing
// over the SSH credentials the data plane will later use to reach it.
//
// This exists because the agent used to do the write itself, and to do that it
// held get/create on namespaces and get/create/update on secrets in the tenant
// workspace. That is the broadest grant an edge agent had, it is the one the
// hub's identity policy refuses to mint (clause X-4: no Secrets, for anyone),
// and it was reachable with a credential that never expired.
//
// So the write moved to where the authority already is. The agent proves, with
// the two gates, that it is this edge; the provider then performs the write
// with its own claimed credential, into the one namespace and the one Secret
// name derived from the edge — nothing in the request names a destination. An
// agent that lies about its credentials only ever lies about its own.
func (p *Server) serveSSHCredentials(w http.ResponseWriter, r *http.Request, req dataplane.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	gvr, _, ok := p.gvrForResource(req.Resource)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var body sshCredentialsRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSSHCredentialsBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Username) == "" {
		http.Error(w, "username is required", http.StatusBadRequest)
		return
	}
	if body.Password == "" && len(body.PrivateKey) == 0 && body.HostKey == "" {
		http.Error(w, "no credentials supplied", http.StatusBadRequest)
		return
	}

	cfg, err := p.tenantConfigFor(r.Context(), req.ClusterID)
	if err != nil {
		p.logger.Error(err, "ssh-credentials: resolving tenant config", "cluster", req.ClusterID, "edge", req.Name)
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	status := map[string]any{}
	if body.Password != "" || len(body.PrivateKey) > 0 {
		creds := &sshCredsFromAgent{User: body.Username, Password: body.Password, PrivateKey: body.PrivateKey}
		if err := p.storeSSHCredentials(r.Context(), cfg, req.ClusterID, req.Name, creds, status); err != nil {
			p.logger.Error(err, "ssh-credentials: storing the secret", "cluster", req.ClusterID, "edge", req.Name)
			http.Error(w, "could not store the credentials", http.StatusServiceUnavailable)
			return
		}
	}

	if err := p.patchSSHStatus(r.Context(), cfg, gvr, req.ClusterID, req.Name, status); err != nil {
		p.logger.Error(err, "ssh-credentials: recording the reference on the edge", "cluster", req.ClusterID, "edge", req.Name)
		http.Error(w, "could not record the credentials", http.StatusServiceUnavailable)
		return
	}

	// The host key is recorded write-once by the existing path, which owns the
	// pinning policy; a re-post of the same key is a no-op there.
	if body.HostKey != "" {
		if err := p.recordSSHHostKey(r.Context(), gvr, req.ClusterID, req.Name, body.HostKey); err != nil {
			p.logger.Error(err, "ssh-credentials: recording the host key", "cluster", req.ClusterID, "edge", req.Name)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// patchSSHStatus merges the sshCredentials reference storeSSHCredentials built
// into the edge's status. A merge patch, not a read-modify-write: it races the
// agent's own status heartbeat and the lifecycle reconciler, and it owns
// exactly one field.
func (p *Server) patchSSHStatus(ctx context.Context, cfg *rest.Config, gvr schema.GroupVersionResource, cluster, name string, status map[string]any) error {
	if len(status) == 0 {
		return nil
	}
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}
	patch, err := json.Marshal(map[string]any{"status": status})
	if err != nil {
		return fmt.Errorf("encoding the status patch: %w", err)
	}
	if _, err := dynClient.Resource(gvr).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}, "status"); err != nil {
		return fmt.Errorf("patching %s/%s status: %w", cluster, name, err)
	}
	return nil
}

// ensureEdgeNamespace is used by storeSSHCredentials; kept here so the one
// tenant-workspace write this provider still performs on an agent's behalf is
// visible in one file.
func ensureEdgeNamespace(ctx context.Context, client kubernetes.Interface, namespace string) error {
	_, err := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking namespace %s: %w", namespace, err)
	}
	_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %s: %w", namespace, err)
	}
	return nil
}
