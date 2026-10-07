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

package hubaccess

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/actionproof"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

// Caller is a verified workload identity presented with the tenant headers.
type Caller struct {
	// User is the person a delegated token stands for.
	User          string
	OrgUUID       string
	WorkspaceUUID string
	// Delegated is true for a delegated user token; Provider and Legacy are
	// meaningful only then.
	Delegated bool
	Provider  string
	// ProviderOrgUUID is the provider's owner org ("" = platform), covered by
	// the hub's proof unless Legacy.
	ProviderOrgUUID string
	// Legacy marks a token whose proof predates the owner org; the owner is
	// then resolved the way the proxy resolved it when issuing.
	Legacy bool
}

// VerifyFunc verifies the request's bearer as a workload identity bound to the
// tenant named by X-Railgrid-Org / X-Railgrid-Workspace.
type VerifyFunc func(*http.Request) (Caller, error)

// ProviderLookup is the part of the provider registry the gate needs.
type ProviderLookup interface {
	Get(name string) (providers.Provider, bool)
	GetForOrg(orgUUID, name string) (providers.Provider, bool)
}

// GrantReader reads recorded grants.
type GrantReader interface {
	Get(ctx context.Context, key GrantKey) (*tenancyv1alpha1.Grant, error)
}

// DefaultInvitesPerHour bounds memberships.invite calls per provider per
// Organization on one replica.
const DefaultInvitesPerHour = 60

// Gate admits provider calls made with a delegated user token or a narrow
// hub-signed action proof to the REST routes the provider contract allows.
type Gate struct {
	// Human resolves the caller's own credential (OIDC, static token, kcp
	// ServiceAccount). When it succeeds the request is not a delegated call.
	Human  tenant.UserResolver
	Verify VerifyFunc
	// ActionProof verifies a request-scoped proof minted at the authenticated
	// kcp front door. Unlike a delegated ServiceAccount token, it is accepted
	// only for the App Studio membership flows bound to the original action.
	ActionProof *actionproof.Verifier
	Providers   ProviderLookup
	Grants      GrantReader
	// PlatformDefault grants a platform provider the capabilities it declares
	// in a workspace where no decision was recorded yet (no grant object).
	// Platform providers are operator-installed and, under the default
	// --provider-delegated-tokens=off, already hold the caller's own bearer;
	// this keeps them working across the upgrade. Org-owned providers always
	// need an explicit grant.
	PlatformDefault bool
	// InvitesPerHour bounds memberships.invite per (provider, org); 0 uses
	// DefaultInvitesPerHour.
	InvitesPerHour int

	limiter windowLimiter
}

// Middleware wraps the tenant-scoped routes. It runs before tenant.Middleware.
// Action proofs are verified and constrained first; requests without one use
// the existing delegated-token path or pass through for the tenant middleware.
func (g *Gate) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values(actionproof.Header)) != 0 {
			if !bearerLooksLikeServiceAccount(r) || g.ActionProof == nil {
				writeStatus(w, http.StatusUnauthorized, "invalid hub action proof")
				return
			}
			claims, err := g.ActionProof.Verify(r.Context(), r)
			if err != nil {
				klog.FromContext(r.Context()).V(2).Info("Hub action proof refused", "error", err, "method", r.Method, "path", r.URL.Path)
				writeStatus(w, http.StatusUnauthorized, "invalid hub action proof")
				return
			}
			if !actionProofAllowsMembershipRoute(claims, r) {
				writeStatus(w, http.StatusForbidden, "the App Studio action proof does not authorize this membership request")
				return
			}
			caller := Caller{
				User:            claims.UserID,
				OrgUUID:         claims.OrgUUID,
				WorkspaceUUID:   claims.WorkspaceUUID,
				Delegated:       true,
				Provider:        claims.Provider,
				ProviderOrgUUID: claims.ProviderOrgUUID,
			}
			call, status, reason := g.admit(r, caller)
			logger := klog.FromContext(r.Context()).WithValues(
				"provider", caller.Provider, "providerOrg", caller.ProviderOrgUUID, "user", caller.User,
				"org", caller.OrgUUID, "workspace", caller.WorkspaceUUID, "method", r.Method, "path", r.URL.Path)
			if status != 0 {
				logger.Info("Action-proof hub access refused", "status", status, "reason", reason)
				writeStatus(w, status, reason)
				return
			}
			call.ActionProof = true
			logger.V(2).Info("Action-proof hub access", "capability", call.Capability, "scope", call.Scope)
			next.ServeHTTP(w, r.WithContext(tenant.WithDelegatedCall(r.Context(), call)))
			return
		}
		if !bearerLooksLikeServiceAccount(r) {
			next.ServeHTTP(w, r)
			return
		}
		if g.Human != nil {
			if _, err := g.Human.ResolveUser(r); err == nil {
				next.ServeHTTP(w, r)
				return
			}
		}
		caller, err := g.Verify(r)
		if err != nil || !caller.Delegated {
			// Not a delegated token (or not a valid one): the tenant
			// middleware refuses it with the human resolver's own error.
			next.ServeHTTP(w, r)
			return
		}
		call, status, reason := g.admit(r, caller)
		logger := klog.FromContext(r.Context()).WithValues(
			"provider", caller.Provider, "providerOrg", caller.ProviderOrgUUID, "user", caller.User,
			"org", caller.OrgUUID, "workspace", caller.WorkspaceUUID, "method", r.Method, "path", r.URL.Path)
		if status != 0 {
			logger.Info("Delegated hub access refused", "status", status, "reason", reason)
			writeStatus(w, status, reason)
			return
		}
		if r.Method == http.MethodGet {
			logger.V(2).Info("Delegated hub access", "capability", call.Capability, "scope", call.Scope)
		} else {
			logger.Info("Delegated hub access", "capability", call.Capability, "scope", call.Scope)
		}
		next.ServeHTTP(w, r.WithContext(tenant.WithDelegatedCall(r.Context(), call)))
	})
}

// actionProofAllowsMembershipRoute binds the narrow hub-access route to the
// signed App Studio action. A publishing-grants write may read the roster
// while validating a member, but only a POST action may invite; the roster
// action itself is read-only. The target tenant is checked against the
// signed tenant even when the request is routed through an org-level path.
func actionProofAllowsMembershipRoute(claims actionproof.Claims, r *http.Request) bool {
	if r == nil || claims.Provider != "app-studio" || claims.Group != "ai.railgrid.ai" ||
		claims.Version != "v1alpha1" || claims.Resource != "projects" || claims.ParentName == "" {
		return false
	}

	switch claims.Verb {
	case "publishing-members":
		if claims.Method != http.MethodGet {
			return false
		}
	case "publishing-grants", "preview-grants":
		if claims.Method != http.MethodGet && claims.Method != http.MethodPost {
			return false
		}
	default:
		return false
	}

	requirement, ok := Match(r.Method, r.URL.Path)
	if !ok {
		return false
	}
	parts := strings.Split(strings.Trim(path.Clean("/"+r.URL.Path), "/"), "/")
	if len(parts) < 4 || parts[2] != claims.OrgUUID {
		return false
	}
	if requirement.Scope == providersv1alpha1.HubAccessScopeWorkspace &&
		(len(parts) != 6 || parts[4] != claims.WorkspaceUUID) {
		return false
	}
	if r.Method == http.MethodPost {
		return claims.Method == http.MethodPost &&
			(claims.Verb == "publishing-grants" || claims.Verb == "preview-grants") &&
			actionProofInviteBodyMatches(claims, r)
	}
	return r.Method == http.MethodGet
}

// actionProofInviteBodyMatches limits proof-backed membership writes to the
// one invitation the original App Studio action named. The body is restored
// for the normal membership handler, and malformed, oversized, or ambiguous
// payloads fail closed before its authorization and mutation logic runs.
func actionProofInviteBodyMatches(claims actionproof.Claims, r *http.Request) bool {
	if r == nil || r.Body == nil || claims.InviteUser == "" || claims.InviteUser != strings.TrimSpace(claims.InviteUser) {
		return false
	}
	const maxInviteBodyBytes = 64 << 10
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInviteBodyBytes+1))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) > maxInviteBodyBytes {
		return false
	}
	var intent struct {
		User   string `json:"user"`
		Role   string `json:"role"`
		Invite bool   `json:"invite"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return false
	}
	return intent.User != "" && intent.User == strings.TrimSpace(intent.User) &&
		intent.User == claims.InviteUser && intent.Role == tenancyv1alpha1.MembershipRoleMember && intent.Invite
}

// admit decides one delegated call. A non-zero status refuses it.
func (g *Gate) admit(r *http.Request, caller Caller) (tenant.DelegatedCall, int, string) {
	if caller.User == "" || strings.HasPrefix(caller.User, "system:serviceaccount:") {
		return tenant.DelegatedCall{}, http.StatusForbidden, "a delegated token must stand for a person"
	}
	req, ok := Match(r.Method, r.URL.Path)
	if !ok {
		return tenant.DelegatedCall{}, http.StatusForbidden,
			fmt.Sprintf("provider %q cannot call %s %s with a delegated token: the route is not part of the provider hub-access contract", caller.Provider, r.Method, r.URL.Path)
	}

	prov, ok := g.resolveProvider(caller)
	if !ok {
		return tenant.DelegatedCall{}, http.StatusForbidden, fmt.Sprintf("provider %q is not registered", caller.Provider)
	}
	// An org-owned provider acts only inside its own Organization. The proxy
	// never issues it a token elsewhere; this holds even if one existed.
	if prov.OrgUUID != "" && prov.OrgUUID != caller.OrgUUID {
		return tenant.DelegatedCall{}, http.StatusForbidden, fmt.Sprintf("provider %q belongs to another organization", prov.Name)
	}

	declared, ok := Declared(prov.HubAccess, req)
	if !ok {
		return tenant.DelegatedCall{}, http.StatusForbidden,
			fmt.Sprintf("provider %q does not declare hub access %s (%s scope)", prov.Name, req.Capability, req.Scope)
	}

	key := GrantKey{OrgUUID: caller.OrgUUID, WorkspaceUUID: caller.WorkspaceUUID, Provider: prov.Name, ProviderOrgUUID: prov.OrgUUID}
	grant, err := g.Grants.Get(r.Context(), key)
	if err != nil {
		return tenant.DelegatedCall{}, http.StatusServiceUnavailable, "cannot read provider hub-access grants; retry"
	}
	// Accepted allows, declined refuses, and an undecided capability falls back
	// to the platform default — which only ever applies to platform providers.
	granted, ok, _ := Allowed(grant, declared, prov.OrgUUID == "", g.PlatformDefault)
	if !ok {
		return tenant.DelegatedCall{}, http.StatusForbidden,
			fmt.Sprintf("hub access %s (%s scope) for provider %q has not been accepted in this workspace; an admin can accept it by enabling the provider again", req.Capability, req.Scope, prov.Name)
	}

	if req.Capability == providersv1alpha1.HubCapabilityMembershipsInvite {
		limit := g.InvitesPerHour
		if limit <= 0 {
			limit = DefaultInvitesPerHour
		}
		if !g.limiter.allow(prov.OrgUUID+"/"+prov.Name+"@"+caller.OrgUUID, limit, time.Hour) {
			return tenant.DelegatedCall{}, http.StatusTooManyRequests,
				fmt.Sprintf("provider %q exceeded %d invitations per hour in this organization", prov.Name, limit)
		}
	}

	limits := Effective(declared, granted)
	return tenant.DelegatedCall{
		User:            caller.User,
		Provider:        prov.Name,
		ProviderOrgUUID: prov.OrgUUID,
		Capability:      string(req.Capability),
		Scope:           string(req.Scope),
		MaxRole:         limits.MaxRole,
		AllowInvite:     limits.AllowInvite,
	}, 0, ""
}

// resolveProvider finds the registry entry the token was issued for. A
// current token names its owner under the proof; a legacy one is resolved the
// way the proxy resolved it at issue time (the org's own provider shadows the
// platform one).
func (g *Gate) resolveProvider(caller Caller) (providers.Provider, bool) {
	if g.Providers == nil || caller.Provider == "" {
		return providers.Provider{}, false
	}
	if caller.Legacy {
		return g.Providers.GetForOrg(caller.OrgUUID, caller.Provider)
	}
	if caller.ProviderOrgUUID == "" {
		return g.Providers.Get(caller.Provider)
	}
	prov, ok := g.Providers.GetForOrg(caller.ProviderOrgUUID, caller.Provider)
	if !ok || prov.OrgUUID != caller.ProviderOrgUUID {
		return providers.Provider{}, false
	}
	return prov, true
}

// DelegatedUserResolver wraps the human resolver for tenant.Middleware: a
// request the gate admitted resolves to the person the delegated token stands
// for; anything else is the human resolver's decision.
func DelegatedUserResolver(human tenant.UserResolver) tenant.UserResolver {
	return tenant.UserResolverFunc(func(r *http.Request) (string, error) {
		if call, ok := tenant.DelegatedCallFrom(r.Context()); ok && call.ActionProof && call.User != "" {
			return call.User, nil
		}
		user, err := human.ResolveUser(r)
		if err == nil {
			return user, nil
		}
		if call, ok := tenant.DelegatedCallFrom(r.Context()); ok && call.User != "" {
			return call.User, nil
		}
		return user, err
	})
}

// bearerLooksLikeServiceAccount reports whether the bearer is a JWT carrying
// Kubernetes ServiceAccount claims. It only routes the request to the
// delegated path; nothing is trusted from the unverified payload.
func bearerLooksLikeServiceAccount(r *http.Request) bool {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) <= len("Bearer ") || !strings.EqualFold(auth[:len("Bearer ")], "Bearer ") {
		return false
	}
	parts := strings.Split(strings.TrimSpace(auth[len("Bearer "):]), ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil {
		return false
	}
	for k := range claims {
		if k == "kubernetes.io" || strings.HasPrefix(k, "kubernetes.io/") {
			return true
		}
	}
	return false
}

func writeStatus(w http.ResponseWriter, code int, message string) {
	reason := http.StatusText(code)
	switch code {
	case http.StatusForbidden:
		reason = "Forbidden"
	case http.StatusTooManyRequests:
		reason = "TooManyRequests"
	case http.StatusServiceUnavailable:
		reason = "ServiceUnavailable"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"kind": "Status", "apiVersion": "v1", "status": "Failure",
		"reason": reason, "message": message, "code": code,
	})
}

// windowLimiter is a per-key fixed-window counter. Process-local: it bounds
// a runaway provider on one replica, not a determined one across replicas.
type windowLimiter struct {
	mu      sync.Mutex
	windows map[string]*limiterWindow
	now     func() time.Time
}

type limiterWindow struct {
	start time.Time
	count int
}

func (l *windowLimiter) allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.windows == nil {
		l.windows = map[string]*limiterWindow{}
	}
	w := l.windows[key]
	if w == nil || now.Sub(w.start) >= window {
		w = &limiterWindow{start: now}
		l.windows[key] = w
	}
	if w.count >= limit {
		return false
	}
	w.count++
	return true
}
