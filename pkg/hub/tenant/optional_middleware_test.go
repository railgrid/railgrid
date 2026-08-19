/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tenant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

// serveOptional runs one request through OptionalOrgMiddleware and reports the
// status plus whatever TenantContext reached the handler.
func serveOptional(t *testing.T, resolver UserResolver, lookup MembershipLookup, headers map[string]string) (int, TenantContext, bool) {
	t.Helper()
	var got TenantContext
	var reached bool
	handler := OptionalOrgMiddleware(resolver, lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		if tc, ok := FromContext(r.Context()); ok {
			got = tc
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, got, reached
}

func okResolver(user string) UserResolver {
	return UserResolverFunc(func(*http.Request) (string, error) { return user, nil })
}

func okLookup(index *tenancyv1alpha1.UserMembershipIndex) MembershipLookup {
	return MembershipLookupFunc(func(context.Context, string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		return index, nil
	})
}

func failingLookup() MembershipLookup {
	return MembershipLookupFunc(func(context.Context, string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		return nil, errors.New("backend down")
	})
}

// No credential: refused. This is what stops the catalog being scraped.
func TestOptionalOrgMiddleware_AnonymousIsRefused(t *testing.T) {
	resolver := UserResolverFunc(func(*http.Request) (string, error) {
		return "", ErrUserNotResolved
	})
	code, _, reached := serveOptional(t, resolver, okLookup(nil), nil)

	if reached {
		t.Error("handler was reached without a credential")
	}
	if code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", code)
	}
}

// A credential that fails verification is refused too — an unrecognized or
// forged token must not read the catalog.
func TestOptionalOrgMiddleware_BadCredentialIsRefused(t *testing.T) {
	resolver := UserResolverFunc(func(*http.Request) (string, error) {
		return "", errors.New("verifying OIDC token: signature is invalid")
	})
	code, _, reached := serveOptional(t, resolver, okLookup(nil), nil)

	if reached {
		t.Error("handler was reached with an unverifiable credential")
	}
	if code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", code)
	}
}

// The CI regression, now expressed precisely: the credential was ACCEPTED and
// only the User record lookup failed. That caller is legitimate, so they get
// the platform catalog rather than a 500 — but no tenant context, so no org
// data.
func TestOptionalOrgMiddleware_AcceptedCredentialWithoutRecordStillServes(t *testing.T) {
	resolver := UserResolverFunc(func(*http.Request) (string, error) {
		return "", fmt.Errorf("%w: listing users: the server could not find the requested resource",
			ErrUserRecordUnavailable)
	})
	code, tc, reached := serveOptional(t, resolver, okLookup(nil),
		map[string]string{HeaderFarosOrg: "org-1"})

	if !reached {
		t.Fatal("handler not reached — an authenticated caller was refused")
	}
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if tc.User != "" || tc.OrgUUID != "" {
		t.Errorf("context = %+v, want empty (identity is unknown)", tc)
	}
}

func TestOptionalOrgMiddleware_VerifiedOrgAttaches(t *testing.T) {
	index := fakeIndex("alice", tenancyv1alpha1.MembershipIndexEntry{
		OrgUUID: "org-1", Role: tenancyv1alpha1.MembershipRoleAdmin,
	})
	code, tc, _ := serveOptional(t, okResolver("alice"), okLookup(index),
		map[string]string{HeaderFarosOrg: "org-1"})

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if tc.OrgUUID != "org-1" || tc.Role != tenancyv1alpha1.MembershipRoleAdmin {
		t.Errorf("context = %+v, want org-1/admin", tc)
	}
}

// An Org header the caller has no membership for must be dropped, not honored
// and not rejected: dropping it can only ever show less.
func TestOptionalOrgMiddleware_UnverifiedOrgIsDropped(t *testing.T) {
	index := fakeIndex("alice", tenancyv1alpha1.MembershipIndexEntry{
		OrgUUID: "org-1", Role: tenancyv1alpha1.MembershipRoleMember,
	})
	code, tc, reached := serveOptional(t, okResolver("alice"), okLookup(index),
		map[string]string{HeaderFarosOrg: "org-someone-elses"})

	if !reached || code != http.StatusOK {
		t.Fatalf("request rejected: reached=%v code=%d", reached, code)
	}
	if tc.OrgUUID != "" {
		t.Errorf("unverified Org was attached: %+v", tc)
	}
	if tc.User != "alice" {
		t.Errorf("User = %q, want the resolved caller", tc.User)
	}
}

// A stale Org UUID in a browser's localStorage must not break the shell when
// the membership backend is unavailable.
func TestOptionalOrgMiddleware_LookupErrorDropsOrgOnly(t *testing.T) {
	code, tc, reached := serveOptional(t, okResolver("alice"), failingLookup(),
		map[string]string{HeaderFarosOrg: "org-1"})

	if !reached || code != http.StatusOK {
		t.Fatalf("request rejected: reached=%v code=%d", reached, code)
	}
	if tc.OrgUUID != "" {
		t.Errorf("Org attached despite lookup failure: %+v", tc)
	}
	if tc.User != "alice" {
		t.Errorf("User = %q, want the resolved caller", tc.User)
	}
}

func TestOptionalOrgMiddleware_WorkspaceScopeAttaches(t *testing.T) {
	index := fakeIndex("alice", tenancyv1alpha1.MembershipIndexEntry{
		OrgUUID: "org-1", WorkspaceUUID: "ws-1", Role: tenancyv1alpha1.MembershipRoleMember,
	})
	_, tc, _ := serveOptional(t, okResolver("alice"), okLookup(index), map[string]string{
		HeaderFarosOrg:       "org-1",
		HeaderFarosWorkspace: "ws-1",
	})

	if tc.OrgUUID != "org-1" || tc.WorkspaceUUID != "ws-1" {
		t.Errorf("context = %+v, want org-1/ws-1", tc)
	}
}
