/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package providers

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
)

// newScopedRegistry builds a registry holding one platform provider and one
// provider each for two different Orgs.
func newScopedRegistry() *Registry {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "edges", DisplayName: "Edges", EndpointsValid: true})
	reg.Upsert(Provider{Name: "vault", OrgUUID: "org1", DisplayName: "Org1 Vault", EndpointsValid: true})
	reg.Upsert(Provider{Name: "vault", OrgUUID: "org2", DisplayName: "Org2 Vault", EndpointsValid: true})
	return reg
}

func names(ps []Provider) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

func TestRegistryScopesByOrg(t *testing.T) {
	reg := newScopedRegistry()

	// Two Orgs registering the same provider name must not collide.
	org1, ok := reg.GetForOrg("org1", "vault")
	if !ok || org1.DisplayName != "Org1 Vault" {
		t.Fatalf("GetForOrg(org1, vault) = %+v, %v; want Org1 Vault", org1, ok)
	}
	org2, ok := reg.GetForOrg("org2", "vault")
	if !ok || org2.DisplayName != "Org2 Vault" {
		t.Fatalf("GetForOrg(org2, vault) = %+v, %v; want Org2 Vault", org2, ok)
	}

	// An Org must not see another Org's provider.
	if _, ok := reg.GetForOrg("org3", "vault"); ok {
		t.Error("GetForOrg(org3, vault) resolved another Org's provider")
	}

	// Platform providers stay visible to every Org.
	if _, ok := reg.GetForOrg("org1", "edges"); !ok {
		t.Error("GetForOrg(org1, edges) did not resolve the platform provider")
	}
}

// Get backs the bare-name request paths (UI/backend proxy, heartbeat), which
// carry no tenant context. It must never reach an org-owned record, or an Org
// could capture a platform provider's route by name.
func TestRegistryGetIsGlobalOnly(t *testing.T) {
	reg := newScopedRegistry()

	if _, ok := reg.Get("vault"); ok {
		t.Error("Get(vault) resolved an org-owned provider")
	}
	if got, ok := reg.Get("edges"); !ok || got.OrgUUID != "" {
		t.Errorf("Get(edges) = %+v, %v; want the platform provider", got, ok)
	}
}

func TestRegistryListForOrg(t *testing.T) {
	reg := newScopedRegistry()

	if got, want := names(reg.ListForOrg("org1")), []string{"edges", "vault"}; !equalStrings(got, want) {
		t.Errorf("ListForOrg(org1) = %v, want %v", got, want)
	}
	for _, p := range reg.ListForOrg("org1") {
		if p.Name == "vault" && p.DisplayName != "Org1 Vault" {
			t.Errorf("ListForOrg(org1) returned the wrong vault: %q", p.DisplayName)
		}
	}

	// An Org with no providers of its own sees only the platform catalog.
	if got, want := names(reg.ListForOrg("org3")), []string{"edges"}; !equalStrings(got, want) {
		t.Errorf("ListForOrg(org3) = %v, want %v", got, want)
	}

	// No Org context degrades to the platform catalog, never to everything.
	if got, want := names(reg.ListForOrg("")), []string{"edges"}; !equalStrings(got, want) {
		t.Errorf("ListForOrg(\"\") = %v, want %v", got, want)
	}
}

// An org-owned provider named after a platform one shadows it inside that Org
// only, and yields exactly one card rather than two.
func TestRegistryListForOrgShadowsPlatformProvider(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "edges", DisplayName: "Platform Edges", EndpointsValid: true})
	reg.Upsert(Provider{Name: "edges", OrgUUID: "org1", DisplayName: "Org1 Edges", EndpointsValid: true})

	listed := reg.ListForOrg("org1")
	if len(listed) != 1 {
		t.Fatalf("ListForOrg(org1) returned %d entries, want 1", len(listed))
	}
	if listed[0].DisplayName != "Org1 Edges" {
		t.Errorf("ListForOrg(org1) = %q, want the Org's own provider to win", listed[0].DisplayName)
	}
	// Another Org still sees the platform one.
	other := reg.ListForOrg("org2")
	if len(other) != 1 || other[0].DisplayName != "Platform Edges" {
		t.Errorf("ListForOrg(org2) = %+v, want only the platform provider", names(other))
	}
}

func TestRegistryDeleteScoped(t *testing.T) {
	reg := newScopedRegistry()

	// Deleting one Org's provider leaves the other Org's alone.
	if !reg.DeleteScoped("org1", "vault") {
		t.Fatal("DeleteScoped(org1, vault) reported nothing removed")
	}
	if _, ok := reg.GetForOrg("org1", "vault"); ok {
		t.Error("org1 vault survived DeleteScoped")
	}
	if _, ok := reg.GetForOrg("org2", "vault"); !ok {
		t.Error("DeleteScoped(org1, ...) also removed org2's provider")
	}
	// Delete without a scope only ever touches the platform record.
	if reg.Delete("vault") {
		t.Error("Delete(vault) removed an org-owned provider")
	}
}

func TestRegistryDeleteByCluster(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "vault", OrgUUID: "org1", CatalogEntryCluster: "cluster-a"})
	reg.Upsert(Provider{Name: "vault", OrgUUID: "org2", CatalogEntryCluster: "cluster-b"})

	// This is the delete path the catalog reconciler takes: the object is gone,
	// so only the cluster it came from identifies the record.
	if !reg.DeleteByCluster("cluster-a", "vault") {
		t.Fatal("DeleteByCluster(cluster-a, vault) reported nothing removed")
	}
	if _, ok := reg.GetForOrg("org1", "vault"); ok {
		t.Error("org1 vault survived DeleteByCluster")
	}
	if _, ok := reg.GetForOrg("org2", "vault"); !ok {
		t.Error("DeleteByCluster removed the record from the wrong cluster")
	}
	if reg.DeleteByCluster("", "vault") {
		t.Error("DeleteByCluster with an empty cluster removed something")
	}
}

// Heartbeats arrive by bare name with no tenant context, so they must only ever
// mark a platform provider alive.
func TestRegistryHeartbeatIsGlobalOnly(t *testing.T) {
	reg := newScopedRegistry()
	now := time.Now()

	if reg.Heartbeat("vault", "1.0.0", now) {
		t.Error("Heartbeat(vault) accepted a beat for an org-owned provider")
	}
	if !reg.Heartbeat("edges", "1.0.0", now) {
		t.Error("Heartbeat(edges) rejected a beat for a platform provider")
	}
}

// A heartbeat must be vouched for by the provider's own service account,
// reviewed in the provider's own workspace. A token that the hub has verified
// for one provider must not keep another one alive, and an org-owned provider
// has no workspace the bare-name endpoint could review in at all.
func TestHeartbeatRequiresOwnProviderSA(t *testing.T) {
	reg := newScopedRegistry()
	reg.Upsert(Provider{Name: "edges", EndpointsValid: true, CatalogEntryCluster: "edges-cluster"})
	reg.Upsert(Provider{Name: "code", EndpointsValid: true, CatalogEntryCluster: "code-cluster"})
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{
		Authenticated: true, User: authnv1.UserInfo{Username: ProviderSAUsername},
	}}
	now := time.Now()
	a := newTestTokenReviewAuthenticator(reg, f, &now)

	if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("edges", "edges-token"), "edges"); err != nil {
		t.Fatalf("edges' own token rejected: %v", err)
	}
	if len(f.clusters) != 1 || f.clusters[0] != "edges-cluster" {
		t.Fatalf("reviewed in %v, want edges' own workspace", f.clusters)
	}

	// The same token presented for a different provider is reviewed afresh in
	// THAT provider's workspace, where kcp would not know it; the cache entry
	// earned for edges does not carry over.
	if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", "edges-token"), "code"); err != nil {
		t.Fatalf("Authenticate(code) = %v", err)
	}
	if len(f.clusters) != 2 || f.clusters[1] != "code-cluster" {
		t.Fatalf("reviewed in %v, want a fresh review in code's workspace", f.clusters)
	}

	// Org-owned providers cannot beat here: the bare-name endpoint only
	// resolves platform providers, so there is no workspace to review in.
	err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("vault", "vault-token"), "vault")
	if !errors.Is(err, ErrHeartbeatAuthUnavailable) {
		t.Fatalf("Authenticate(vault) = %v, want ErrHeartbeatAuthUnavailable", err)
	}
}

func TestRegistryUpsertMergesWithinScope(t *testing.T) {
	reg := NewRegistry()
	beat := time.Now()
	reg.Upsert(Provider{Name: "vault", OrgUUID: "org1", WorkspaceCluster: "cluster-a", LastHeartbeat: beat})
	// A later reconcile carries no cluster and no beat; neither may be lost.
	reg.Upsert(Provider{Name: "vault", OrgUUID: "org1", DisplayName: "Renamed"})

	got, ok := reg.GetForOrg("org1", "vault")
	if !ok {
		t.Fatal("provider missing after re-upsert")
	}
	if got.DisplayName != "Renamed" {
		t.Errorf("DisplayName = %q, want the newer spec value", got.DisplayName)
	}
	if got.WorkspaceCluster != "cluster-a" {
		t.Errorf("WorkspaceCluster = %q, want it preserved across reconciles", got.WorkspaceCluster)
	}
	if !got.LastHeartbeat.Equal(beat) {
		t.Errorf("LastHeartbeat = %v, want the earlier beat preserved", got.LastHeartbeat)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
