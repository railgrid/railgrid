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
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/providers"
	kcpproxy "github.com/faroshq/faros-kedge/pkg/server/proxy"
)

// Headers the portal sends alongside Authorization on provider-proxy
// calls to scope the tenant context to a workspace the user picked in
// the sidebar (rather than always landing in the personal-org default).
// The resolver verifies BOTH against the user's UserMembershipIndex
// before honoring them — a client can't read or write a workspace it
// doesn't have a Membership in just by setting these headers.
const (
	headerKedgeOrg       = "X-Kedge-Org"
	headerKedgeWorkspace = "X-Kedge-Workspace"
)

// workspacePathRoot is the prefix every org / workspace path lives
// under in kcp. Kept as a constant so the format stays in sync with
// the bootstrap controllers (orgWorkspaceParent in
// pkg/hub/controllers/organization/controller.go).
const workspacePathRoot = "root:kedge:tenants"

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
	client   *kedgeclient.Client

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
// through the kedge typed client. Returns ErrAnonymousProviderCaller
// for unauthenticated requests; the backend proxy maps that to
// "forward without injecting X-Kedge-*" so anonymous /healthz reads
// keep working.
func newKCPTenantResolver(kcpProxy *kcpproxy.KCPProxy, client *kedgeclient.Client) providers.TenantResolver {
	r := &kcpTenantResolver{
		kcpProxy: kcpProxy,
		client:   client,
		hot:      make(map[string]kcpResolverEntry),
	}
	return providers.TenantResolverFunc(r.resolve)
}

// ErrAnonymousProviderCaller is returned by the tenant resolver when
// the caller carries no bearer token. The backend proxy treats this
// as a non-error "forward without identity headers" so unauthenticated
// probes (health checks, smoke tests) work without provider auth.
var ErrAnonymousProviderCaller = errors.New("anonymous caller")

func (r *kcpTenantResolver) resolve(req *http.Request) (string, string, error) {
	user, err := r.kcpProxy.IdentifyUser(req)
	if err != nil {
		if errors.Is(err, kcpproxy.ErrIdentifyNoBearer) {
			return "", "", ErrAnonymousProviderCaller
		}
		return "", "", err
	}

	// Honor the portal's sidebar selection before falling back to
	// the personal-org default. Verifying via UserMembershipIndex
	// prevents header spoofing — a stranger setting X-Kedge-Org +
	// X-Kedge-Workspace to someone else's IDs is rejected because
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

// resolveFromHeaders honors the portal's sidebar-driven X-Kedge-Org
// (+ optional X-Kedge-Workspace) headers when present. Returns:
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
	orgUUID := req.Header.Get(headerKedgeOrg)
	if orgUUID == "" {
		return "", false, nil
	}
	wsUUID := req.Header.Get(headerKedgeWorkspace)

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
	// the SAME kedge-tenants-<hash> namespace.
	if wsUUID == "" {
		return workspacePathRoot + ":" + orgUUID, true, nil
	}
	return workspacePathRoot + ":" + orgUUID + ":" + wsUUID, true, nil
}
