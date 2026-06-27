/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package proxy

import (
	"context"
	"strings"
	"sync"
	"time"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// clusterAuthorizer decides whether a user may address a given workspace
// cluster through the kcp proxy. It implements the membership-gated model from
// docs/hub-proxy-workspace-access.md (Option A):
//
//   - A request for /clusters/{id} is allowed when {id} is the cluster of a
//     child workspace the caller is a member of — either a workspace-scope
//     Membership for that exact workspace, or an org-scope Membership for its
//     org (org-scope implies access to every child workspace, per O-15).
//   - Edges ({id}:{edge}) are authorized by their parent workspace {id}.
//
// It maintains a lazily-populated reverse topology cache (clusterID → (org,
// ws)) filled by forward-resolving the *caller's own* memberships, so a request
// never resolves another user's workspaces, and the warm path is an in-memory
// lookup. Membership itself is read fresh per request (matching the tenant
// middleware), so revocation takes effect immediately; cluster→owner mappings
// are stable for a workspace's lifetime and safe to keep.
type clusterAuthorizer struct {
	members  membershipGetter
	resolve  clusterResolver
	children childLister

	mu       sync.RWMutex
	reverse  map[string]ownerKey // clusterID → (org, ws), stable
	forward  map[string]string   // "org/ws" → clusterID, stable
	childTTL time.Duration
	childExp map[string]childCacheEntry // org → child workspace UUIDs (TTL)
	now      func() time.Time
}

type ownerKey struct {
	org string
	ws  string
}

type childCacheEntry struct {
	ws  []string
	exp time.Time
}

type membershipGetter func(ctx context.Context, userName string) (*tenancyv1alpha1.UserMembershipIndex, error)
type clusterResolver func(ctx context.Context, orgUUID, wsUUID string) (string, error)
type childLister func(ctx context.Context, orgUUID string) ([]string, error)

func newClusterAuthorizer(members membershipGetter, resolve clusterResolver, children childLister) *clusterAuthorizer {
	return &clusterAuthorizer{
		members:  members,
		resolve:  resolve,
		children: children,
		reverse:  map[string]ownerKey{},
		forward:  map[string]string{},
		childTTL: 30 * time.Second,
		childExp: map[string]childCacheEntry{},
		now:      time.Now,
	}
}

// authorize reports whether userName may reach clusterID (a child-workspace
// cluster, or an edge {cluster}:{edge} under one). Failure is closed: any error
// or unknown cluster denies.
func (a *clusterAuthorizer) authorize(ctx context.Context, userName, clusterID string) bool {
	base := clusterID
	if i := strings.IndexByte(clusterID, ':'); i >= 0 {
		base = clusterID[:i] // edge {cluster}:{edge} → authorize the parent cluster
	}
	if base == "" {
		return false
	}

	idx, err := a.members(ctx, userName)
	if err != nil || idx == nil {
		return false
	}

	// Fast path: the cluster's owner is already known.
	if owner, ok := a.reverseGet(base); ok {
		return membershipCovers(idx, owner)
	}

	// Slow path: resolve the caller's own reachable workspaces into the cache,
	// then re-check. This only ever resolves workspaces in the caller's index.
	a.populateForUser(ctx, idx)
	if owner, ok := a.reverseGet(base); ok {
		return membershipCovers(idx, owner)
	}
	return false
}

// membershipCovers reports whether the index grants access to (owner.org,
// owner.ws): a workspace-scope entry for that workspace, or an org-scope entry
// (empty WorkspaceUUID) for its org.
func membershipCovers(idx *tenancyv1alpha1.UserMembershipIndex, owner ownerKey) bool {
	if idx == nil {
		return false
	}
	for _, e := range idx.Spec.Entries {
		if e.OrgUUID != owner.org {
			continue
		}
		if e.WorkspaceUUID == "" || e.WorkspaceUUID == owner.ws {
			return true
		}
	}
	return false
}

// populateForUser forward-resolves every workspace the index can reach into the
// topology caches: each workspace-scope entry's workspace, and every child
// workspace of each org-scope entry's org.
func (a *clusterAuthorizer) populateForUser(ctx context.Context, idx *tenancyv1alpha1.UserMembershipIndex) {
	for _, e := range idx.Spec.Entries {
		if e.WorkspaceUUID != "" {
			a.resolveAndStore(ctx, e.OrgUUID, e.WorkspaceUUID)
			continue
		}
		for _, ws := range a.childrenOf(ctx, e.OrgUUID) {
			a.resolveAndStore(ctx, e.OrgUUID, ws)
		}
	}
}

// resolveAndStore resolves (org, ws) → clusterID (cached, stable) and records
// the reverse mapping.
func (a *clusterAuthorizer) resolveAndStore(ctx context.Context, org, ws string) {
	key := org + "/" + ws
	a.mu.RLock()
	cid, ok := a.forward[key]
	a.mu.RUnlock()
	if !ok {
		var err error
		cid, err = a.resolve(ctx, org, ws)
		if err != nil || cid == "" {
			return
		}
	}
	a.mu.Lock()
	a.forward[key] = cid
	a.reverse[cid] = ownerKey{org: org, ws: ws}
	a.mu.Unlock()
}

// childrenOf returns an org's child workspace UUIDs, cached with a short TTL so
// a newly created child becomes reachable for org-scope members promptly.
func (a *clusterAuthorizer) childrenOf(ctx context.Context, org string) []string {
	a.mu.RLock()
	entry, ok := a.childExp[org]
	a.mu.RUnlock()
	if ok && a.now().Before(entry.exp) {
		return entry.ws
	}
	ws, err := a.children(ctx, org)
	if err != nil {
		if ok {
			return entry.ws // serve stale on error rather than dropping access
		}
		return nil
	}
	a.mu.Lock()
	a.childExp[org] = childCacheEntry{ws: ws, exp: a.now().Add(a.childTTL)}
	a.mu.Unlock()
	return ws
}

func (a *clusterAuthorizer) reverseGet(clusterID string) (ownerKey, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	owner, ok := a.reverse[clusterID]
	return owner, ok
}
