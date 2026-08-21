// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/store"
)

// Service-to-service invocation.
//
// Every other way into this provider assumes a human: the hub authenticates the
// caller, resolves their workspace, and injects X-Faros-* headers. That chain
// runs on a User CR and a Membership, so a caller with neither — another
// provider, a CI job, a cron — has no way in, and the only workaround was to
// borrow a user's token.
//
//	POST /s2s/clusters/{cluster}/agents/{name}/runs
//	GET  /s2s/clusters/{cluster}/runs/{id}
//	GET  /s2s/clusters/{cluster}/runs/{id}/wait
//
// The caller presents its OWN ServiceAccount token and names the target
// workspace by logical-cluster ID in the path (the same addressing app-studio
// uses for the infrastructure data plane — a path the prod hub proxy is known to
// pass through, unlike a re-rooted workspace URL). This provider then does its
// own authn/authz, following kcp's auth-delegator pattern:
//
//  1. TokenReview resolves the identity, in the workspace that issued the token.
//  2. SubjectAccessReview asks the TARGET workspace's RBAC whether that identity
//     may run this agent — verb create (or get, to read a run) on
//     agents.faros.sh/agents/delegate, named for the agent.
//
// Both go through the APIExport virtual workspace scoped to the target cluster.
// Neither re-roots the provider's own kubeconfig at the tenant path: that is the
// approach the production hub proxy answers with an opaque 404 (kcp#4279), and
// it is why this claims tokenreviews + subjectaccessreviews instead.
//
// A tenant grants access by writing ordinary RBAC in their workspace, e.g.
//
//	kind: ClusterRole            rules:
//	  - apiGroups: [agents.faros.sh]
//	    resources: [agents/delegate]
//	    verbs: [create, get]
//	    resourceNames: [researcher]   # optional: one agent only
//
// bound to the calling identity. Nothing is granted by default: an unknown
// caller is denied, and so is a known one the tenant has not bound.
//
// The run itself executes as the AGENT's own ServiceAccount, exactly like a
// scheduled run — the caller's token authorizes the request, it does not become
// the identity the agent acts with.
const (
	// s2sAuthTTL bounds how long an authorization decision is reused. Short: a
	// revoked binding should stop working promptly, and the reviews are two API
	// calls that would otherwise run on every poll of a long wait.
	s2sAuthTTL = 2 * time.Minute

	// s2sDelegateResource is the RBAC hook a tenant binds to allow invocation. It
	// is a subresource of agents rather than a real API: it exists so permission
	// to RUN an agent is expressible, and revocable, separately from permission to
	// read or edit its configuration.
	s2sDelegateResource    = "agents"
	s2sDelegateSubresource = "delegate"
)

// s2sAuthCache memoizes authorization decisions by (token digest, cluster, verb,
// agent). The token itself is never stored — only a digest of it.
type s2sAuthCache struct {
	mu      sync.Mutex
	entries map[string]s2sAuthEntry
}

type s2sAuthEntry struct {
	user string
	err  error
	at   time.Time
}

func newS2SAuthCache() *s2sAuthCache { return &s2sAuthCache{entries: map[string]s2sAuthEntry{}} }

func (c *s2sAuthCache) get(key string) (s2sAuthEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) > s2sAuthTTL {
		return s2sAuthEntry{}, false
	}
	return e, true
}

func (c *s2sAuthCache) put(key string, e s2sAuthEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e.at = time.Now()
	c.entries[key] = e
	// Bound the map: authorization decisions are small and short-lived, but a
	// caller rotating tokens would otherwise grow it without limit.
	if len(c.entries) > 512 {
		for k, v := range c.entries {
			if time.Since(v.at) > s2sAuthTTL {
				delete(c.entries, k)
			}
		}
	}
}

// saTokenClaims are the claims that identify a kcp ServiceAccount token and,
// crucially, the logical cluster that minted it.
type saTokenClaims struct {
	Issuer      string `json:"iss"`
	ClusterName string `json:"kubernetes.io/serviceaccount/clusterName"`
}

// parseServiceAccountToken decodes a JWT WITHOUT verifying its signature, only to
// learn where to send it for verification. kcp checks the signature during
// TokenReview; nothing here trusts these claims for anything else.
func parseServiceAccountToken(token string) (saTokenClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return saTokenClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return saTokenClaims{}, false
	}
	var claims saTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return saTokenClaims{}, false
	}
	if claims.Issuer != "kubernetes/serviceaccount" || claims.ClusterName == "" {
		return saTokenClaims{}, false
	}
	return claims, true
}

// qualifyServiceAccount re-encodes a ServiceAccount identity with its home
// cluster, so an SA from one workspace cannot be mistaken for a same-named SA in
// the workspace being authorized. Mirrors pkg/util/identity.
func qualifyServiceAccount(cluster, username string) (string, bool) {
	const prefix = "system:serviceaccount:"
	if !strings.HasPrefix(username, prefix) {
		return "", false
	}
	return prefix + cluster + ":" + strings.TrimPrefix(username, prefix), true
}

// s2sVWConfig returns a rest.Config addressing the target workspace through the
// APIExport virtual workspace. Requires the background executor, which owns the
// shard discovery.
func (s *Server) s2sVWConfig(ctx context.Context, clusterID string) (*rest.Config, error) {
	bg := s.bg
	if bg == nil || !bg.ready() {
		return nil, fmt.Errorf("service-to-service access is unavailable: this provider has no virtual-workspace connection yet")
	}
	shard, err := bg.shardFor(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("locating workspace %s: %w", clusterID, err)
	}
	cfg := rest.CopyConfig(bg.base)
	cfg.Host = shard + "/clusters/" + clusterID
	return cfg, nil
}

// authorizeS2S authenticates the caller's token and authorizes it for verb on
// the named agent in clusterID. Returns the resolved identity for the audit
// trail. Every failure is an error — nothing is allowed by default.
func (s *Server) authorizeS2S(ctx context.Context, clusterID, token, verb, agentName string) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("a bearer token is required")
	}
	digest := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(digest[:8]) + "|" + clusterID + "|" + verb + "|" + agentName
	if e, ok := s.s2sAuth.get(key); ok {
		return e.user, e.err
	}

	user, err := s.reviewS2S(ctx, clusterID, token, verb, agentName)
	// Negative results are cached too, so a misconfigured caller retrying in a
	// loop cannot turn itself into load on kcp.
	s.s2sAuth.put(key, s2sAuthEntry{user: user, err: err})
	return user, err
}

func (s *Server) reviewS2S(ctx context.Context, clusterID, token, verb, agentName string) (string, error) {
	tenantCfg, err := s.s2sVWConfig(ctx, clusterID)
	if err != nil {
		return "", err
	}

	// 1. Authenticate, in the workspace that issued the token. A kcp
	// ServiceAccount token only authenticates in its home logical cluster, so an
	// SA minted somewhere other than the target workspace is reviewed there
	// instead — and its identity is then cluster-qualified so it cannot be
	// confused with a same-named SA in the target.
	claims, isSA := parseServiceAccountToken(token)
	foreign := isSA && claims.ClusterName != clusterID
	trCfg := tenantCfg
	if foreign {
		trCfg = rest.CopyConfig(tenantCfg)
		trCfg.Host = shardBase(tenantCfg.Host) + "/clusters/" + claims.ClusterName
	}
	trClient, err := kubernetes.NewForConfig(trCfg)
	if err != nil {
		return "", fmt.Errorf("creating token-review client: %w", err)
	}
	tr, err := trClient.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("authenticating the caller: %w", err)
	}
	if !tr.Status.Authenticated {
		return "", fmt.Errorf("the presented token is not valid in this platform")
	}

	sarUser, sarGroups := tr.Status.User.Username, tr.Status.User.Groups
	if foreign {
		qualified, ok := qualifyServiceAccount(claims.ClusterName, sarUser)
		if !ok {
			// It claimed to be a ServiceAccount token but its home cluster resolved
			// it to something else. Refuse rather than authorize an identity that
			// cannot be encoded unambiguously.
			return "", fmt.Errorf("expected a ServiceAccount identity, got %q", sarUser)
		}
		sarUser = qualified
		// Drop groups: system:serviceaccounts and friends would otherwise match
		// group-targeted bindings the tenant wrote for their OWN ServiceAccounts.
		sarGroups = nil
	}

	// 2. Authorize against the TARGET workspace's RBAC. The tenant decides, in
	// their own workspace, who may run their agents.
	sarClient, err := kubernetes.NewForConfig(tenantCfg)
	if err != nil {
		return "", fmt.Errorf("creating access-review client: %w", err)
	}
	sar, err := sarClient.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   sarUser,
			Groups: sarGroups,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb:        verb,
				Group:       agentsv1alpha1.SchemeGroupVersion.Group,
				Version:     agentsv1alpha1.SchemeGroupVersion.Version,
				Resource:    s2sDelegateResource,
				Subresource: s2sDelegateSubresource,
				Name:        agentName,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("checking the caller's access: %w", err)
	}
	if !sar.Status.Allowed {
		return sarUser, fmt.Errorf("%q may not %s agent %q in this workspace: %s "+
			"(grant it %s on %s/%s in the workspace's RBAC)",
			sarUser, verb, agentName, sar.Status.Reason, verb,
			s2sDelegateResource, s2sDelegateSubresource)
	}
	return sarUser, nil
}

// shardBase strips a trailing /clusters/<id> so another cluster path can be
// composed onto the same shard.
func shardBase(host string) string {
	if i := strings.Index(host, "/clusters/"); i >= 0 {
		return host[:i]
	}
	return strings.TrimRight(host, "/")
}

// s2sScope resolves the store scope for a target cluster. Unlike background
// execution there is no fallback scope: a caller invoking into an unmapped
// workspace would have its run recorded somewhere the portal never reads, which
// is worse than a clear refusal.
func (s *Server) s2sScope(ctx context.Context, clusterID, agentName string) (store.Scope, error) {
	ref, ok, err := s.store.GetTenantRef(ctx, clusterID)
	if err != nil {
		return store.Scope{}, err
	}
	if !ok {
		return store.Scope{}, fmt.Errorf("workspace %s is not mapped yet — open the agents UI in it once, then retry", clusterID)
	}
	return store.Scope{OrgUUID: ref.OrgUUID, WorkspaceUUID: ref.WorkspaceUUID, AgentName: agentName}, nil
}

// s2sInvoke serves POST /s2s/clusters/{cluster}/agents/{name}/runs.
func (s *Server) s2sInvoke(w http.ResponseWriter, r *http.Request) {
	clusterID := r.PathValue("cluster")
	name := r.PathValue("name")

	var req invokeRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	req.Task = strings.TrimSpace(req.Task)
	if req.Task == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "task is required")
		return
	}

	if err := req.Callback.validate(); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}

	caller, err := s.authorizeS2S(r.Context(), clusterID, bearerToken(r), "create", name)
	if err != nil {
		s.writeS2SAuthError(w, err)
		return
	}
	scope, err := s.s2sScope(r.Context(), clusterID, name)
	if err != nil {
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
		return
	}

	// Everything from here runs as the platform, not as the caller: the agent
	// executes with its own ServiceAccount exactly as a scheduled run does.
	dyn, err := s.bg.scoped(r.Context(), clusterID)
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "reaching workspace "+clusterID+": "+err.Error())
		return
	}
	au, err := dyn.Resource(agentsclient.AgentGVR).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeResourceError(w, err)
		return
	}
	agent, err := fromU[agentsv1alpha1.Agent](au)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	if key := strings.TrimSpace(req.IdempotencyKey); key != "" {
		if existing, found, ferr := s.store.FindRunByIdempotencyKey(r.Context(), scope, key); ferr == nil && found {
			resp := invokeRunResponse{RunID: existing.ID, Phase: string(existing.Phase), Reused: true}
			if detail, derr := s.runDetailFor(r.Context(), scope, existing.ID); derr == nil {
				resp.Run = &detail
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	runID := s.startDetachedVWRun(r, dyn, clusterID, scope, agent, taskRun{
		SessionID: strings.TrimSpace(req.SessionID), Task: req.Task,
		Trigger:        agentsv1alpha1.RunTriggerAPI,
		IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
		Callback:       req.Callback,
		SourceName:     "s2s:" + caller,
	})
	log.Printf("s2s: %s started run %s on agent %s in %s", caller, runID, name, clusterID)

	if req.Wait > 0 {
		wait := min(time.Duration(req.Wait)*time.Second, invokeMaxWait)
		if run, settled := s.waitForRun(r.Context(), scope, runID, wait); settled {
			resp := invokeRunResponse{RunID: runID, Phase: string(run.Phase)}
			if detail, derr := s.runDetailFor(r.Context(), scope, runID); derr == nil {
				resp.Run = &detail
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeJSON(w, http.StatusAccepted, invokeRunResponse{RunID: runID, Phase: string(store.RunPhaseRunning)})
		return
	}
	writeJSON(w, http.StatusAccepted, invokeRunResponse{RunID: runID, Phase: string(store.RunPhasePending)})
}

// s2sGetRun serves GET /s2s/clusters/{cluster}/runs/{id} and its /wait variant.
func (s *Server) s2sGetRun(w http.ResponseWriter, r *http.Request) {
	clusterID := r.PathValue("cluster")
	runID := r.PathValue("id")

	// Read the run first, unscoped to an agent, so the SAR can be asked about the
	// agent this run actually belongs to rather than about a name the caller
	// asserted.
	lookupScope, err := s.s2sScope(r.Context(), clusterID, "")
	if err != nil {
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	run, err := s.store.GetRun(r.Context(), lookupScope, runID)
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		return
	}
	if _, err := s.authorizeS2S(r.Context(), clusterID, bearerToken(r), "get", run.AgentName); err != nil {
		s.writeS2SAuthError(w, err)
		return
	}

	if strings.HasSuffix(r.URL.Path, "/wait") {
		timeout := 60 * time.Second
		if v := strings.TrimSpace(r.URL.Query().Get("timeoutSeconds")); v != "" {
			if n, cerr := time.ParseDuration(v + "s"); cerr == nil && n > 0 {
				timeout = min(n, waitMaxTimeout)
			}
		}
		s.waitForRun(r.Context(), lookupScope, runID, timeout)
	}

	detail, err := s.runDetailFor(r.Context(), lookupScope, runID)
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// writeS2SAuthError maps an authorization failure to a status. Availability
// problems are 503 (retry later); everything else is 403 — the caller reached the
// right place and was refused, which is not a 401 invitation to try other
// credentials.
func (s *Server) writeS2SAuthError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unavailable"), strings.Contains(msg, "locating workspace"):
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", msg)
	case strings.Contains(msg, "bearer token is required"):
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", msg)
	default:
		writeStatus(w, http.StatusForbidden, "Forbidden", msg)
	}
}

// startDetachedVWRun starts a run through the APIExport virtual workspace, as the
// agent's own identity. The sibling of startDetachedRun for callers that are not
// a signed-in user: same detachment and same pre-written record, different
// credentials.
func (s *Server) startDetachedVWRun(r *http.Request, dyn dynamic.Interface, clusterID string, scope store.Scope, agent *agentsv1alpha1.Agent, tr taskRun) string {
	runID := uuid.NewString()
	now := time.Now().UTC()
	tr.RunID = runID
	tr.Creds = vwSecrets{dyn}
	tr.CR = vwCR{dyn}
	tr.Scope = scope
	tr.Agent = agent
	tr.ClusterID = clusterID
	// The agent's own ServiceAccount, as for any unattended run — the caller's
	// token authorized the request, it does not become the identity the agent
	// acts with. Edges stays absent: nobody is watching (see buildToolset).
	tr.HubToken = s.bg.agentToken(r.Context(), dyn, clusterID, agent.Name)

	ctx := context.WithoutCancel(r.Context())
	_ = s.store.SaveRun(ctx, scope, store.Run{
		ID: runID, AgentName: agent.Name, SessionID: tr.SessionID, Trigger: tr.Trigger,
		IdempotencyKey: tr.IdempotencyKey,
		Phase:          store.RunPhasePending, Input: tr.Task, CreatedAt: now, UpdatedAt: now,
	})
	go func() {
		if _, err := s.executeTask(ctx, tr); err != nil {
			s.finalizeDetachedRun(ctx, scope, runID, agent.Name, tr.Trigger, err)
			log.Printf("s2s: run %s on agent %s failed: %v", runID, agent.Name, err)
		}
		s.deliverRunCallback(ctx, scope, runID, tr.Callback)
	}()
	return runID
}
