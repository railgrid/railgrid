/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"errors"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/railgrid/railgrid/pkg/hub/providers"
)

// The rest of the policy suite runs against a hand-written catalog so the
// table reads as a policy statement. These tests run the SAME clauses against
// the real RegistryCatalog over a real Registry, because the bug this file
// exists for lived entirely in that adapter: every provider was assumed to
// serve the group its APIExport is NAMED after.
//
// The registry below is the platform as actually shipped. Note how few
// providers have export name == served group.
func platformRegistry(t *testing.T) *providers.Registry {
	t.Helper()
	registry := providers.NewRegistry()
	for _, provider := range []providers.Provider{{
		Name:          "edges",
		APIExportName: "edges.providers.railgrid.ai",
		APIGroups:     []string{"edges.railgrid.ai"},
		DataPlaneVerbs: []providers.ProviderDataPlaneVerb{
			{Resource: "kubernetesclusters", Verb: "k8s"},
			{Resource: "kubernetesclusters", Verb: "ssh"},
			{Resource: "services", Verb: "proxy"},
		},
	}, {
		Name:          "infrastructure",
		APIExportName: "infrastructure.providers.railgrid.ai",
		APIGroups:     []string{"infrastructure.railgrid.ai"},
	}, {
		Name:          "code",
		APIExportName: "code.providers.railgrid.ai",
		APIGroups:     []string{"code.railgrid.ai"},
	}, {
		// kuery is the one first-party provider whose export name and served
		// group really are the same string. It has to keep working too.
		Name:          "kuery",
		APIExportName: "kuery.providers.railgrid.ai",
		APIGroups:     []string{"kuery.providers.railgrid.ai"},
		Dependencies: []providers.Dependency{{
			Name: "edges",
			Composes: []providers.Composition{{
				Group: "edges.railgrid.ai", Resource: "kubernetesclusters",
				Verbs: []string{"get", "list", "watch"},
			}},
		}},
	}, {
		Name:          "app-studio",
		APIExportName: "ai.railgrid.ai",
		APIGroups:     []string{"ai.railgrid.ai"},
		Dependencies: []providers.Dependency{{
			Name: "infrastructure",
			Composes: []providers.Composition{{
				Group: "infrastructure.railgrid.ai", Resource: "instances",
				Verbs: []string{"get", "list", "watch", "create", "update", "delete"},
			}},
		}, {
			Name: "code",
			Composes: []providers.Composition{{
				Group: "code.railgrid.ai", Resource: "repositories",
				Verbs: []string{"get", "list", "watch", "create", "update"},
			}, {
				Group: "code.railgrid.ai", Resource: "repositorycommits",
				Verbs: []string{"get", "list", "watch"},
			}},
		}},
	}, {
		// Registered, but its APIExport has never been readable — the
		// fail-closed state.
		Name:          "unread",
		APIExportName: "unread.providers.railgrid.ai",
	}} {
		registry.Upsert(provider)
	}
	return registry
}

func platformPolicy(t *testing.T) *Policy {
	t.Helper()
	return NewPolicy(
		NewRegistryCatalog(platformRegistry(t)),
		fakeBindings{bound: map[string]bool{
			"edges": true, "infrastructure": true, "code": true,
			"kuery": true, "app-studio": true,
		}},
		fakeCompositions{granted: map[string]bool{
			"cluster-1|kuery|edges.railgrid.ai/kubernetesclusters":      true,
			"cluster-1|app-studio|infrastructure.railgrid.ai/instances": true,
			"cluster-1|app-studio|code.railgrid.ai/repositories":        true,
			"cluster-1|app-studio|code.railgrid.ai/repositorycommits":   true,
		}},
	)
}

// TestRegistryCatalogGroupOwner: ownership answers from the groups the
// APIExport SERVES. The export's own name owns nothing — that was the bug.
func TestRegistryCatalogGroupOwner(t *testing.T) {
	catalog := NewRegistryCatalog(platformRegistry(t))
	for _, tc := range []struct {
		group     string
		wantOwner string
	}{
		{group: "edges.railgrid.ai", wantOwner: "edges"},
		{group: "infrastructure.railgrid.ai", wantOwner: "infrastructure"},
		{group: "code.railgrid.ai", wantOwner: "code"},
		{group: "ai.railgrid.ai", wantOwner: "app-studio"},
		{group: "kuery.providers.railgrid.ai", wantOwner: "kuery"},

		// The export NAMES. A provider serves kinds, not its own export name,
		// so nobody owns these.
		{group: "edges.providers.railgrid.ai"},
		{group: "infrastructure.providers.railgrid.ai"},
		{group: "code.providers.railgrid.ai"},

		// Registered but never read: fail closed rather than guess.
		{group: "unread.providers.railgrid.ai"},
		{group: "unread.railgrid.ai"},
		{group: ""},
	} {
		t.Run(tc.group, func(t *testing.T) {
			owner, ok := catalog.GroupOwner(tc.group)
			if tc.wantOwner == "" {
				if ok {
					t.Fatalf("GroupOwner(%q) = %q, want no owner", tc.group, owner)
				}
				return
			}
			if !ok || owner != tc.wantOwner {
				t.Fatalf("GroupOwner(%q) = (%q, %v), want %q", tc.group, owner, ok, tc.wantOwner)
			}
		})
	}
}

// ExportedGroups is clause A's input, and it is the same list.
func TestRegistryCatalogExportedGroups(t *testing.T) {
	catalog := NewRegistryCatalog(platformRegistry(t))
	groups := catalog.ExportedGroups("edges")
	if len(groups) != 1 || groups[0] != "edges.railgrid.ai" {
		t.Fatalf("ExportedGroups(edges) = %v, want [edges.railgrid.ai]", groups)
	}
	if groups := catalog.ExportedGroups("unread"); len(groups) != 0 {
		t.Fatalf("ExportedGroups(unread) = %v, want none until its export is read", groups)
	}
	if groups := catalog.ExportedGroups("nobody"); len(groups) != 0 {
		t.Fatalf("ExportedGroups(nobody) = %v, want none", groups)
	}
}

// Clause A over the real registry: the edges agent identity asks for `proxy`
// on its OWN edge. Before the groups were read off the APIExport this was
// refused with unknown_group, because the registry only knew the export NAME.
func TestRegistryCatalogClauseA(t *testing.T) {
	got, err := platformPolicy(t).Authorize("edges", "cluster-1", []rbacv1.PolicyRule{
		rule("edges.railgrid.ai", []string{"kubernetesclusters"}, []string{"get", "proxy"}, []string{"edge-1"}),
	})
	if err != nil {
		t.Fatalf("the edges agent identity was refused its own group: %v", err)
	}
	if len(got) != 1 || got[0].APIGroups[0] != "edges.railgrid.ai" {
		t.Fatalf("rules = %#v", got)
	}

	// And the export name is still not a group anyone owns.
	_, err = platformPolicy(t).Authorize("edges", "cluster-1", []rbacv1.PolicyRule{
		rule("edges.providers.railgrid.ai", []string{"kubernetesclusters"}, []string{"proxy"}, []string{"edge-1"}),
	})
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeUnknownProvider {
		t.Fatalf("the APIExport's own name must own no group, got %v", err)
	}
}

// Clause C over the real registry: kuery's engagement controller takes
// `create` on the edges data-plane coordinate kubernetesclusters/k8s, in
// edges' group, name-scoped to the edge it engages.
func TestRegistryCatalogClauseC(t *testing.T) {
	got, err := platformPolicy(t).Authorize("kuery", "cluster-1", []rbacv1.PolicyRule{
		rule("edges.railgrid.ai", []string{"kubernetesclusters/k8s"}, []string{"create"}, []string{"edge-1"}),
	})
	if err != nil {
		t.Fatalf("kuery was refused the declared edges verb it engages through: %v", err)
	}
	if len(got) != 1 || got[0].Resources[0] != "kubernetesclusters/k8s" {
		t.Fatalf("rules = %#v", got)
	}

	// A verb edges does not declare is still refused, so reading the groups
	// off the export did not widen clause C.
	_, err = platformPolicy(t).Authorize("kuery", "cluster-1", []rbacv1.PolicyRule{
		rule("edges.railgrid.ai", []string{"kubernetesclusters/invented"}, []string{"create"}, []string{"edge-1"}),
	})
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeUndeclaredVerb {
		t.Fatalf("want %s, got %v", CodeUndeclaredVerb, err)
	}
}

// Clause E over the real registry: the two `composes` declarations the
// platform actually ships. Both name a dependency whose export is named
// differently from the group they compose, which is what used to make
// composition.Dependency != owner and refuse the rule.
func TestRegistryCatalogClauseE(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requester string
		rule      rbacv1.PolicyRule
	}{{
		name:      "kuery watches the edges it engages",
		requester: "kuery",
		rule:      rule("edges.railgrid.ai", []string{"kubernetesclusters"}, []string{"list", "watch"}, nil),
	}, {
		name:      "app-studio creates the infrastructure Instance a project is",
		requester: "app-studio",
		rule:      rule("infrastructure.railgrid.ai", []string{"instances"}, []string{"create"}, nil),
	}, {
		name:      "app-studio updates the code Repository it created",
		requester: "app-studio",
		rule:      rule("code.railgrid.ai", []string{"repositories"}, []string{"get", "update"}, []string{"proj-1"}),
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := platformPolicy(t).Authorize(tc.requester, "cluster-1", []rbacv1.PolicyRule{tc.rule})
			if err != nil {
				t.Fatalf("a shipped composition was refused: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d rules, want 1", len(got))
			}
		})
	}
}

// A provider whose APIExport has never been read owns nothing, so a rule on
// the group it will eventually serve is refused rather than guessed at. Its
// OWN rules are refused too: clause A reads the same empty list.
func TestRegistryCatalogUnreadExportFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requester string
		group     string
	}{
		{name: "its own group", requester: "unread", group: "unread.railgrid.ai"},
		{name: "its export name", requester: "unread", group: "unread.providers.railgrid.ai"},
		{name: "another provider reading it", requester: "edges", group: "unread.railgrid.ai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := platformPolicy(t).Authorize(tc.requester, "cluster-1", []rbacv1.PolicyRule{
				rule(tc.group, []string{"widgets"}, []string{"get"}, []string{"w-1"}),
			})
			var refusal Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodeUnknownProvider {
				t.Fatalf("want %s for a provider whose export was never read, got %v", CodeUnknownProvider, err)
			}
		})
	}
}

// An org-owned provider must not be able to take a platform group's ownership
// away from it by declaring the same group, and the answer must not depend on
// map iteration order: one identity request succeeding and the next refusing
// is worse than either answer.
func TestRegistryCatalogGroupOwnerPrefersThePlatformProvider(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Upsert(providers.Provider{
		Name: "code", APIExportName: "code.providers.railgrid.ai",
		APIGroups: []string{"code.railgrid.ai"},
	})
	registry.Upsert(providers.Provider{
		Name: "byo-code", OrgUUID: "org-1", APIExportName: "code.providers.railgrid.ai",
		APIGroups: []string{"code.railgrid.ai"},
	})
	catalog := NewRegistryCatalog(registry)
	for i := 0; i < 32; i++ {
		owner, ok := catalog.GroupOwner("code.railgrid.ai")
		if !ok || owner != "code" {
			t.Fatalf("GroupOwner = (%q, %v), want the platform provider every time", owner, ok)
		}
	}
}
