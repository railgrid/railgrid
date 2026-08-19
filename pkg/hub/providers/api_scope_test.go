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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/hub/tenant"
)

// These tests wire the REAL list handler behind the REAL OptionalOrgMiddleware.
// The security-critical property is that an org-owned provider reaches a
// response ONLY when the caller's membership in that org was verified against
// their UserMembershipIndex — a header alone must never be enough, and neither
// must an unresolvable identity.

func scopedRegistry() *Registry {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "edges", DisplayName: "Edges", EndpointsValid: true})
	reg.Upsert(Provider{Name: "acme-secrets", OrgUUID: "org-1", DisplayName: "Acme Secrets", EndpointsValid: true})
	reg.Upsert(Provider{Name: "other-corp", OrgUUID: "org-2", DisplayName: "Other Corp", EndpointsValid: true})
	return reg
}

func listWith(t *testing.T, reg *Registry, resolver tenant.UserResolver, lookup tenant.MembershipLookup, headers map[string]string) []string {
	t.Helper()
	code, names := listStatus(t, reg, resolver, lookup, headers)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	return names
}

func listStatus(t *testing.T, reg *Registry, resolver tenant.UserResolver, lookup tenant.MembershipLookup, headers map[string]string) (int, []string) {
	t.Helper()
	h := NewListHandler(reg)
	h.SetMiddleware(tenant.OptionalOrgMiddleware(resolver, lookup))

	req := httptest.NewRequest(http.MethodGet, PathListProviders, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var body struct {
		Items []struct {
			Name     string `json:"name"`
			Scope    string `json:"scope"`
			OwnerOrg string `json:"ownerOrg"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	names := make([]string, 0, len(body.Items))
	for _, it := range body.Items {
		// Guard the invariant on every item, not just the set: an org-owned
		// entry must always be labelled with its owner.
		if it.Scope == ScopeOrg && it.OwnerOrg == "" {
			t.Errorf("org-scoped item %q has no ownerOrg", it.Name)
		}
		names = append(names, it.Name)
	}
	return rec.Code, names
}

func hasName(names []string, want string) bool {
	return slices.Contains(names, want)
}

// The catalog is not scrapeable: no credential, no answer.
func TestListHandler_AnonymousIsRefused(t *testing.T) {
	resolver := tenant.UserResolverFunc(func(*http.Request) (string, error) {
		return "", tenant.ErrUserNotResolved
	})
	lookup := tenant.MembershipLookupFunc(func(context.Context, string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		return nil, errors.New("should not be consulted")
	})

	code, names := listStatus(t, scopedRegistry(), resolver, lookup, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (names: %v)", code, names)
	}
}

// A credential accepted but not resolvable to a User record still gets the
// platform catalog — and never org data. (The CI regression, made precise.)
func TestListHandler_AcceptedCredentialWithoutRecordSeesOnlyPlatform(t *testing.T) {
	resolver := tenant.UserResolverFunc(func(*http.Request) (string, error) {
		return "", fmt.Errorf("%w: listing users: the server could not find the requested resource",
			tenant.ErrUserRecordUnavailable)
	})
	lookup := tenant.MembershipLookupFunc(func(context.Context, string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		return nil, errors.New("should not be consulted")
	})

	names := listWith(t, scopedRegistry(), resolver, lookup,
		map[string]string{tenant.HeaderFarosOrg: "org-1"})
	if hasName(names, "acme-secrets") {
		t.Errorf("org provider leaked when identity could not be resolved: %v", names)
	}
	if !hasName(names, "edges") {
		t.Errorf("platform catalog not served: %v", names)
	}
}

// Spoofing the header is not enough: the membership index is the authority.
func TestListHandler_HeaderAloneCannotClaimAnotherOrg(t *testing.T) {
	resolver := tenant.UserResolverFunc(func(*http.Request) (string, error) { return "mallory", nil })
	lookup := tenant.MembershipLookupFunc(func(_ context.Context, user string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		// Mallory is a real, authenticated user — but only in org-2.
		return &tenancyv1alpha1.UserMembershipIndex{
			ObjectMeta: metav1.ObjectMeta{Name: user},
			Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{
				{OrgUUID: "org-2", Role: tenancyv1alpha1.MembershipRoleAdmin},
			}},
		}, nil
	})

	names := listWith(t, scopedRegistry(), resolver, lookup,
		map[string]string{tenant.HeaderFarosOrg: "org-1"})

	if hasName(names, "acme-secrets") {
		t.Fatalf("org-1's provider leaked to a member of org-2 who spoofed the header: %v", names)
	}
	// Nor does the failed claim silently fall back to her own org's providers.
	if hasName(names, "other-corp") {
		t.Errorf("unverified org header resolved to the caller's own org: %v", names)
	}
}

// The positive case, so the tests above cannot pass by the handler simply
// never returning org providers at all.
func TestListHandler_VerifiedMemberSeesOwnOrg(t *testing.T) {
	resolver := tenant.UserResolverFunc(func(*http.Request) (string, error) { return "alice", nil })
	lookup := tenant.MembershipLookupFunc(func(_ context.Context, user string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		return &tenancyv1alpha1.UserMembershipIndex{
			ObjectMeta: metav1.ObjectMeta{Name: user},
			Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{
				{OrgUUID: "org-1", Role: tenancyv1alpha1.MembershipRoleMember},
			}},
		}, nil
	})

	names := listWith(t, scopedRegistry(), resolver, lookup,
		map[string]string{tenant.HeaderFarosOrg: "org-1"})

	if !hasName(names, "acme-secrets") {
		t.Errorf("verified member did not see their own org's provider: %v", names)
	}
	if !hasName(names, "edges") {
		t.Errorf("platform provider missing: %v", names)
	}
	if hasName(names, "other-corp") {
		t.Errorf("another org's provider leaked: %v", names)
	}
}
