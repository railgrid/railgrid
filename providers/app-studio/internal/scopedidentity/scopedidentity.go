/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package scopedidentity holds the refreshing hub-minted identities App
// Studio's reconcilers act as: one per Project, one per Studio.
//
// It exists because two reconcilers need the same three things — a token that
// is always valid, a rebuild when what the owner may reach changes, and a
// revocation on the owner's delete path — and none of them is a property of
// either controller.
//
// What a source IS matters more than what it caches. It is the identity: it
// refreshes lazily at 80% of the token's TTL on use, so an owner nobody
// reconciles stops refreshing and its identity lapses on its own, and an owner
// whose references change has its source rebuilt so the next token carries the
// new rules rather than the old ones.
package scopedidentity

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/railgrid/provider-sdk/identityclient"
)

// Cache keeps one refreshing token source per (cluster, owner).
type Cache struct {
	client *identityclient.Client

	mu      sync.Mutex
	sources map[string]*entry
}

// entry is one owner's source plus the fingerprint of the rules it was built
// from, so a change is detectable without minting a token to find out.
type entry struct {
	source      *identityclient.TokenSource
	fingerprint string
}

// New wraps a hub identity client. A nil client yields a cache that hands back
// no token at all, which is what a REST-only deployment with no hub to ask
// gets; callers check Enabled rather than relying on an empty string.
func New(client *identityclient.Client) *Cache {
	if client == nil {
		return nil
	}
	return &Cache{client: client, sources: map[string]*entry{}}
}

// Enabled reports whether identities can be minted at all.
func (c *Cache) Enabled() bool { return c != nil && c.client != nil }

// Token returns a valid token for owner under rules, minting or refreshing as
// needed. Rules are re-stated on every mint, so a narrowed grant actually
// narrows: that is the difference from the create-if-absent ClusterRole this
// replaced, where a removed binding never removed the access it carried.
func (c *Cache) Token(ctx context.Context, owner identityclient.Owner, rules []rbacv1.PolicyRule) (string, error) {
	if !c.Enabled() {
		return "", errors.New("the hub identity service is not configured")
	}
	token, err := c.sourceFor(owner, rules).Token(ctx)
	if err != nil {
		return "", err
	}
	return token.Token, nil
}

// sourceFor returns the owner's source, rebuilding it when the rules it was
// created with no longer match. Rebuilding on a change is what makes a removed
// reference actually remove access: the old source would go on refreshing the
// old rules, and the hub would go on honouring them, because a refresh is
// idempotent on the owner tuple and says nothing about what the owner
// references today.
func (c *Cache) sourceFor(owner identityclient.Owner, rules []rbacv1.PolicyRule) *identityclient.TokenSource {
	key := Key(owner)
	print := Fingerprint(rules)

	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.sources[key]; ok && existing.fingerprint == print {
		return existing.source
	}
	source := identityclient.NewTokenSource(c.client, identityclient.Request{
		Owner:     owner,
		ClusterID: owner.ClusterID,
		Rules:     rules,
	})
	c.sources[key] = &entry{source: source, fingerprint: print}
	return source
}

// Release drops the owner's identity, here and at the hub. Call it from the
// owner's delete path: the hub's own sweep would collect it anyway, but only
// after up to one token TTL, and revocation should not wait.
func (c *Cache) Release(ctx context.Context, owner identityclient.Owner) error {
	if !c.Enabled() {
		return nil
	}
	c.mu.Lock()
	delete(c.sources, Key(owner))
	c.mu.Unlock()
	return c.client.Release(ctx, owner.ClusterID, owner)
}

// Invalidate forgets an owner's source so the next Token mints fresh. For the
// case where the token in hand has stopped working.
func (c *Cache) Invalidate(owner identityclient.Owner) {
	if !c.Enabled() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sources, Key(owner))
}

// Key identifies one identity: the workspace, the kind and the owner's UID.
// The UID is in the key because a deleted and recreated object is a different
// owner with a different identity, and must not inherit the predecessor's
// source.
func Key(owner identityclient.Owner) string {
	return owner.ClusterID + "/" + owner.Kind + "/" + owner.Name + "/" + owner.UID
}

// Sorted returns names in a stable, deduplicated order. Rule sets are compared
// by their rendering, so an unstable name order would re-mint an identity that
// has not actually changed.
func Sorted(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Fingerprint renders a rule set so two of them can be compared cheaply. It is
// not a hash: the rules are few and short, and a readable fingerprint makes a
// rebuild explicable in a log line.
func Fingerprint(rules []rbacv1.PolicyRule) string {
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		parts = append(parts, strings.Join(rule.APIGroups, ",")+"|"+
			strings.Join(rule.Resources, ",")+"|"+
			strings.Join(rule.ResourceNames, ",")+"|"+
			strings.Join(rule.Verbs, ","))
	}
	return strings.Join(parts, ";")
}
