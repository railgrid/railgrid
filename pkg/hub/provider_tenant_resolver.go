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

package hub

// Lives here (in pkg/hub) rather than in pkg/hub/providers because it
// imports pkg/server/proxy, and proxy → pkg/hub/kcp → pkg/hub/providers
// would form a cycle. The providers package keeps only the
// TenantResolver interface; the concrete kcp-backed implementation is
// composed by the hub at startup and injected via SetTenantResolver.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	farosclient "github.com/faroshq/faros/pkg/client"
	"github.com/faroshq/faros/pkg/hub/providers"
	"github.com/faroshq/faros/pkg/hub/serviceaccounts"
	kcpproxy "github.com/faroshq/faros/pkg/server/proxy"
)

// Headers the portal sends alongside Authorization on provider-proxy
// calls to scope the tenant context to a workspace the user picked in
// the sidebar (rather than always landing in the personal-org default).
// The resolver verifies BOTH against the user's UserMembershipIndex
// before honoring them — a client can't read or write a workspace it
// doesn't have a Membership in just by setting these headers.
const (
	headerFarosOrg       = "X-Faros-Org"
	headerFarosWorkspace = "X-Faros-Workspace"
)

// workspacePathRoot is the prefix every org / workspace path lives
// under in kcp. Kept as a constant so the format stays in sync with
// the bootstrap controllers (orgWorkspaceParent in
// pkg/hub/controllers/organization/controller.go).
const workspacePathRoot = "root:faros:tenants"

// kcpTenantResolver implements providers.TenantResolver against the
// same identity store the rest of the hub uses: bearer token → User CR
// → personal Organization → kcp WorkspacePath.
//
// Cost: each cold call does one IdentifyUser (token verify), one User
// Get and one Organization Get. To keep the warm path cheap we cache
// (user → tenantPath) per-user with a 5-minute TTL. PersonalOrg and
// WorkspacePath are set-once by the bootstrap controller and never
// reassigned, so the cache value is safe to keep around for that long.
type kcpTenantResolver struct {
	kcpProxy *kcpproxy.KCPProxy
	client   *farosclient.Client
	// identifyUser is kept as a narrow seam for tests. Production wiring uses
	// kcpProxy.IdentifyUser; workload ServiceAccount identities are then
	// re-verified online in the selected child workspace below.
	identifyUser func(*http.Request) (string, error)
	// workloadConfig enables online verification of Faros-audience workload
	// tokens in the concrete tenant selected by X-Faros-Org/Workspace.
	workloadConfig serviceaccounts.WorkspaceConfigBuilder

	mu  sync.RWMutex
	hot map[string]kcpResolverEntry
}

type kcpResolverEntry struct {
	tenantPath string
	expiresAt  time.Time
}

const kcpResolverTTL = 5 * time.Minute

// newKCPTenantResolver builds a providers.TenantResolver that derives
// identity from the request's bearer token via kcpProxy.IdentifyUser,
// then resolves the caller's personal organization workspace path
// through the faros typed client. Returns ErrAnonymousProviderCaller
// for unauthenticated requests; the backend proxy maps that to
// "forward without injecting X-Faros-*" so anonymous /healthz reads
// keep working.
func newKCPTenantResolver(kcpProxy *kcpproxy.KCPProxy, client *farosclient.Client, workloadConfig ...serviceaccounts.WorkspaceConfigBuilder) providers.TenantResolver {
	r := &kcpTenantResolver{
		kcpProxy: kcpProxy,
		client:   client,
		hot:      make(map[string]kcpResolverEntry),
	}
	if kcpProxy != nil {
		r.identifyUser = kcpProxy.IdentifyUser
	}
	if len(workloadConfig) > 0 {
		r.workloadConfig = workloadConfig[0]
	}
	return providers.TenantResolverFunc(r.resolve)
}

// ErrAnonymousProviderCaller is returned by the tenant resolver when
// the caller carries no bearer token. The backend proxy treats this
// as a non-error "forward without identity headers" so unauthenticated
// probes (health checks, smoke tests) work without provider auth.
var ErrAnonymousProviderCaller = errors.New("anonymous caller")

func (r *kcpTenantResolver) resolve(req *http.Request) (string, string, error) {
	if r == nil || req == nil {
		return "", "", errors.New("tenant resolver unavailable")
	}
	identifyUser := r.identifyUser
	if identifyUser == nil && r.kcpProxy != nil {
		identifyUser = r.kcpProxy.IdentifyUser
	}
	if identifyUser == nil {
		return "", "", errors.New("tenant identity resolver unavailable")
	}
	user, err := identifyUser(req)
	if err == nil && isServiceAccountIdentity(user) {
		// Never pass an SA-shaped identity through human User/Membership
		// resolution. It must be validated as a workload token against the
		// selected tenant, or the request is denied.
		workloadUser, workloadTenant, workloadErr := r.resolveWorkloadServiceAccount(req)
		if workloadErr != nil {
			return "", "", workloadErr
		}
		return workloadUser, workloadTenant, nil
	}
	if err != nil {
		// KCPProxy's identity path intentionally accepts human/OIDC users, not
		// ServiceAccount tokens. Workload capabilities are accepted only via
		// this explicit online TokenReview path and concrete tenant headers.
		workloadUser, workloadTenant, workloadErr := r.resolveWorkloadServiceAccount(req)
		if workloadErr == nil {
			return workloadUser, workloadTenant, nil
		}
		if errors.Is(err, kcpproxy.ErrIdentifyNoBearer) {
			// A request with any supplied Authorization value is not an
			// anonymous health probe. If the normal identity path cannot
			// recognize it, the workload verifier's failure must be surfaced
			// rather than forwarding an unauthenticated request to a provider.
			if strings.TrimSpace(req.Header.Get("Authorization")) != "" {
				return "", "", workloadErr
			}
			return "", "", ErrAnonymousProviderCaller
		}
		return "", "", err
	}

	// Honor the portal's sidebar selection before falling back to
	// the personal-org default. Verifying via UserMembershipIndex
	// prevents header spoofing — a stranger setting X-Faros-Org +
	// X-Faros-Workspace to someone else's IDs is rejected because
	// the index is keyed by the (authenticated) user.
	if path, ok, err := r.resolveFromHeaders(req.Context(), user, req); err != nil {
		// Auth failures (membership missing, header malformed) drop
		// the headers and let the personal-org default win. Log at
		// V(2) so the path isn't silent but doesn't spam logs on
		// every probe; the proxy itself surfaces the final
		// resolution at default verbosity.
		klog.FromContext(req.Context()).V(2).Info("tenant header validation failed; falling back to personal org", "user", user, "err", err.Error())
	} else if ok {
		return user, path, nil
	}

	now := time.Now()
	r.mu.RLock()
	entry, ok := r.hot[user]
	r.mu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		return user, entry.tenantPath, nil
	}

	ctx := req.Context()
	u, err := r.client.Users().Get(ctx, user, metav1.GetOptions{})
	if err != nil {
		return user, "", err
	}
	if u.Status.PersonalOrg == "" {
		return user, "", nil
	}
	org, err := r.client.Organizations().Get(ctx, u.Status.PersonalOrg, metav1.GetOptions{})
	if err != nil {
		return user, "", err
	}
	tenantPath := org.Status.WorkspacePath
	if tenantPath == "" {
		return user, "", nil
	}

	r.mu.Lock()
	r.hot[user] = kcpResolverEntry{tenantPath: tenantPath, expiresAt: now.Add(kcpResolverTTL)}
	r.mu.Unlock()
	return user, tenantPath, nil
}

func isServiceAccountIdentity(user string) bool {
	return strings.HasPrefix(strings.TrimSpace(user), "system:serviceaccount:")
}

func (r *kcpTenantResolver) resolveWorkloadServiceAccount(req *http.Request) (string, string, error) {
	if r == nil || r.workloadConfig == nil || req == nil {
		return "", "", errors.New("workload identity resolver unavailable")
	}
	orgUUID := strings.TrimSpace(req.Header.Get(headerFarosOrg))
	wsUUID := strings.TrimSpace(req.Header.Get(headerFarosWorkspace))
	if orgUUID == "" || wsUUID == "" || strings.ContainsAny(orgUUID+wsUUID, ":\r\n") {
		return "", "", errors.New("workload identity requires a concrete tenant selection")
	}
	authHeader := strings.TrimSpace(req.Header.Get("Authorization"))
	if len(authHeader) <= len("Bearer ") || !strings.EqualFold(authHeader[:len("Bearer ")], "Bearer ") {
		return "", "", errors.New("workload identity requires a bearer token")
	}
	token := strings.TrimSpace(authHeader[len("Bearer "):])
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", "", errors.New("workload identity bearer is malformed")
	}
	tenantPath := workspacePathRoot + ":" + orgUUID + ":" + wsUUID
	cfg := r.workloadConfig.ChildWorkspaceConfig(orgUUID, wsUUID)
	username, err := serviceaccounts.VerifyWorkloadServiceAccount(req.Context(), cfg, token, tenantPath)
	if err != nil {
		return "", "", err
	}
	return username, tenantPath, nil
}

// resolveFromHeaders honors the portal's sidebar-driven X-Faros-Org
// (+ optional X-Faros-Workspace) headers when present. Returns:
//
//	path, true, nil   — headers valid, user is a member, scope used
//	"",   false, nil  — no headers (caller falls back to default)
//	"",   false, err  — headers present but invalid (caller falls back +
//	                    logs); error gives the reason for diagnostics.
//
// Authorization model: the UserMembershipIndex is the source of truth
// for "what (org, workspace) tuples is this user allowed to operate
// on". An entry with empty WorkspaceUUID grants ORG-scope access
// (uses the org workspace itself); an entry with WorkspaceUUID set
// grants that specific child workspace.
//
// We do NOT cache here: the same user can legitimately switch
// workspaces between requests, and the cache is keyed by user.
// Adding workspace to the cache key would complicate invalidation
// (membership revocation, workspace deletion) for very little win on
// the warm path — the index Get is one apiserver round-trip.
func (r *kcpTenantResolver) resolveFromHeaders(ctx context.Context, user string, req *http.Request) (string, bool, error) {
	orgUUID := req.Header.Get(headerFarosOrg)
	if orgUUID == "" {
		return "", false, nil
	}
	wsUUID := req.Header.Get(headerFarosWorkspace)

	idx, err := r.client.UserMembershipIndices().Get(ctx, user, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, errors.New("no membership index for user")
		}
		return "", false, err
	}
	// Membership model:
	//   - Entry with WorkspaceUUID=""  → ORG-scope (admin/member of
	//     the whole org → access to every child workspace).
	//   - Entry with WorkspaceUUID=X   → WORKSPACE-scope (admin/member
	//     of just that one child workspace).
	//
	// Access rules used here:
	//   wsUUID == ""     → any entry for the org (org-default landing).
	//   wsUUID set       → either an explicit workspace-scope entry
	//                      that matches OR ANY org-scope entry for the
	//                      org (org-scope implies access to every
	//                      workspace under it). Without this implicit
	//                      grant, an org-admin who hasn't been
	//                      explicitly added to each child workspace
	//                      would always fall back to the personal-org
	//                      default — symptom: switching workspaces in
	//                      the sidebar appears to do nothing.
	allowed := false
	for _, e := range idx.Spec.Entries {
		if e.OrgUUID != orgUUID {
			continue
		}
		if wsUUID == "" || e.WorkspaceUUID == "" || e.WorkspaceUUID == wsUUID {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", false, errors.New("user has no Membership in (org=" + orgUUID + ", workspace=" + wsUUID + ")")
	}

	// Compose the workspace PATH. Matches the form the bootstrap
	// controllers write into Organization.Status.WorkspacePath and
	// matches what the MCPServer controller now writes into
	// status.URL after the kcp.io/path lookup — so UI + MCP land in
	// the SAME faros-tenants-<hash> namespace.
	if wsUUID == "" {
		return workspacePathRoot + ":" + orgUUID, true, nil
	}
	return workspacePathRoot + ":" + orgUUID + ":" + wsUUID, true, nil
}
