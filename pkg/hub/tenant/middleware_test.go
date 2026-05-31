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

package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// fakeIndex is a small fixture builder for UserMembershipIndex.
func fakeIndex(user string, entries ...tenancyv1alpha1.MembershipIndexEntry) *tenancyv1alpha1.UserMembershipIndex {
	return &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: user},
		Spec:       tenancyv1alpha1.UserMembershipIndexSpec{Entries: entries},
	}
}

// captureNext returns an http.Handler that records the TenantContext
// from the request and signals via the channel that it was reached.
func captureNext(got *TenantContext, reached chan<- struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tc, ok := FromContext(r.Context()); ok {
			*got = tc
		}
		close(reached)
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddleware_OrgScopeHappyPath(t *testing.T) {
	user := "alice"
	orgUUID := "org-uuid"
	index := fakeIndex(user,
		tenancyv1alpha1.MembershipIndexEntry{
			OrgUUID: orgUUID,
			Role:    tenancyv1alpha1.MembershipRoleAdmin,
		},
	)
	resolver := UserResolverFunc(func(_ *http.Request) (string, error) { return user, nil })
	lookup := MembershipLookupFunc(func(_ context.Context, name string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		if name != user {
			t.Fatalf("lookup called with %q, want %q", name, user)
		}
		return index, nil
	})

	var got TenantContext
	reached := make(chan struct{}, 1)
	h := Middleware(resolver, lookup)(captureNext(&got, reached))

	req := httptest.NewRequest(http.MethodGet, "/api/orgs/x/anything", nil)
	req.Header.Set(HeaderKedgeOrg, orgUUID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-reached:
	default:
		t.Fatal("next handler was not invoked")
	}
	want := TenantContext{User: user, OrgUUID: orgUUID, Role: tenancyv1alpha1.MembershipRoleAdmin}
	if got != want {
		t.Errorf("TenantContext: got %#v, want %#v", got, want)
	}
}

func TestMiddleware_WorkspaceScopeHappyPath(t *testing.T) {
	user := "bob"
	orgUUID := "org-uuid"
	wsUUID := "ws-uuid"
	index := fakeIndex(user,
		tenancyv1alpha1.MembershipIndexEntry{
			OrgUUID: orgUUID,
			Role:    tenancyv1alpha1.MembershipRoleAdmin,
		},
		tenancyv1alpha1.MembershipIndexEntry{
			OrgUUID:       orgUUID,
			WorkspaceUUID: wsUUID,
			Role:          tenancyv1alpha1.MembershipRoleMember,
		},
	)
	resolver := UserResolverFunc(func(_ *http.Request) (string, error) { return user, nil })
	lookup := MembershipLookupFunc(func(_ context.Context, _ string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		return index, nil
	})

	var got TenantContext
	reached := make(chan struct{}, 1)
	h := Middleware(resolver, lookup)(captureNext(&got, reached))

	req := httptest.NewRequest(http.MethodGet, "/api/orgs/x/workspaces/y/anything", nil)
	req.Header.Set(HeaderKedgeOrg, orgUUID)
	req.Header.Set(HeaderKedgeWorkspace, wsUUID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-reached:
	default:
		t.Fatal("next handler was not invoked")
	}
	want := TenantContext{
		User:          user,
		OrgUUID:       orgUUID,
		WorkspaceUUID: wsUUID,
		Role:          tenancyv1alpha1.MembershipRoleMember,
	}
	if got != want {
		t.Errorf("TenantContext: got %#v, want %#v", got, want)
	}
}

func TestMiddleware_MissingOrgHeader(t *testing.T) {
	resolver := UserResolverFunc(func(_ *http.Request) (string, error) { return "alice", nil })
	lookup := MembershipLookupFunc(func(_ context.Context, _ string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		t.Fatal("lookup should not be called when X-Kedge-Org is missing")
		return nil, nil
	})

	nextCalled := false
	h := Middleware(resolver, lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
	if nextCalled {
		t.Error("next handler should not be called on missing header")
	}
	assertStatusEnvelope(t, rec, "BadRequest", HeaderKedgeOrg)
}

func TestMiddleware_NoMatchingMembership(t *testing.T) {
	resolver := UserResolverFunc(func(_ *http.Request) (string, error) { return "alice", nil })
	lookup := MembershipLookupFunc(func(_ context.Context, _ string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		// Index exists, but only carries an entry for a different Org.
		return fakeIndex("alice",
			tenancyv1alpha1.MembershipIndexEntry{OrgUUID: "other-org", Role: tenancyv1alpha1.MembershipRoleAdmin},
		), nil
	})

	h := Middleware(resolver, lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called on no-match")
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderKedgeOrg, "asked-for-org")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rec.Code)
	}
	assertStatusEnvelope(t, rec, "Forbidden", "asked-for-org")
}

func TestMiddleware_IndexNotFound(t *testing.T) {
	resolver := UserResolverFunc(func(_ *http.Request) (string, error) { return "alice", nil })
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "tenancy.kedge.faros.sh", Resource: "usermembershipindices"}, "alice")
	lookup := MembershipLookupFunc(func(_ context.Context, _ string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		return nil, notFound
	})

	h := Middleware(resolver, lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called when the index is not found")
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderKedgeOrg, "org")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rec.Code)
	}
	assertStatusEnvelope(t, rec, "Forbidden", "no memberships")
}

func TestMiddleware_LookupError(t *testing.T) {
	resolver := UserResolverFunc(func(_ *http.Request) (string, error) { return "alice", nil })
	lookup := MembershipLookupFunc(func(_ context.Context, _ string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		return nil, fmt.Errorf("kcp unreachable")
	})

	h := Middleware(resolver, lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called on lookup error")
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderKedgeOrg, "org")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", rec.Code)
	}
	assertStatusEnvelope(t, rec, "InternalError", "kcp unreachable")
}

func TestMiddleware_Unauthenticated(t *testing.T) {
	resolver := UserResolverFunc(func(_ *http.Request) (string, error) { return "", ErrUserNotResolved })
	lookup := MembershipLookupFunc(func(_ context.Context, _ string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		t.Fatal("lookup should not be called when caller is unauthenticated")
		return nil, nil
	})

	h := Middleware(resolver, lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called when caller is unauthenticated")
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderKedgeOrg, "org") // header present but irrelevant
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rec.Code)
	}
	assertStatusEnvelope(t, rec, "Unauthorized", "")
}

func TestMiddleware_ResolverInternalError(t *testing.T) {
	resolver := UserResolverFunc(func(_ *http.Request) (string, error) {
		return "", errors.New("kcp unreachable")
	})
	lookup := MembershipLookupFunc(func(_ context.Context, _ string) (*tenancyv1alpha1.UserMembershipIndex, error) {
		t.Fatal("lookup should not be called on resolver error")
		return nil, nil
	})

	h := Middleware(resolver, lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called on resolver error")
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderKedgeOrg, "org")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", rec.Code)
	}
}

func TestMiddleware_NilResolverPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected Middleware(nil, lookup) to panic")
		}
	}()
	_ = Middleware(nil, MembershipLookupFunc(func(_ context.Context, _ string) (*tenancyv1alpha1.UserMembershipIndex, error) { return nil, nil }))
}

func TestMiddleware_NilLookupPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected Middleware(resolver, nil) to panic")
		}
	}()
	_ = Middleware(UserResolverFunc(func(_ *http.Request) (string, error) { return "a", nil }), nil)
}

func TestMatchEntry(t *testing.T) {
	idx := fakeIndex("alice",
		tenancyv1alpha1.MembershipIndexEntry{OrgUUID: "o1", Role: "admin"},
		tenancyv1alpha1.MembershipIndexEntry{OrgUUID: "o2", WorkspaceUUID: "w1", Role: "member"},
		tenancyv1alpha1.MembershipIndexEntry{OrgUUID: "o2", WorkspaceUUID: "w2", Role: "admin"},
	)

	cases := []struct {
		name     string
		orgUUID  string
		wsUUID   string
		wantRole string
		wantOK   bool
	}{
		{"org-scope match (admin)", "o1", "", "admin", true},
		{"org-scope on wrong org", "o2", "", "", false},
		{"workspace-scope match (member)", "o2", "w1", "member", true},
		{"workspace-scope match (admin)", "o2", "w2", "admin", true},
		{"workspace-scope on wrong workspace", "o2", "w999", "", false},
		{"unknown org", "ghost", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRole, gotOK := matchEntry(idx, tc.orgUUID, tc.wsUUID)
			if gotOK != tc.wantOK || gotRole != tc.wantRole {
				t.Errorf("matchEntry(_, %q, %q) = (%q, %v), want (%q, %v)", tc.orgUUID, tc.wsUUID, gotRole, gotOK, tc.wantRole, tc.wantOK)
			}
		})
	}
}

func TestMatchEntry_NilIndex(t *testing.T) {
	if _, ok := matchEntry(nil, "o", ""); ok {
		t.Error("matchEntry(nil, _, _) should return ok=false")
	}
}

// assertStatusEnvelope verifies the response body is a valid v1.Status
// envelope and (optionally) that its message carries the expected
// substring.
func assertStatusEnvelope(t *testing.T, rec *httptest.ResponseRecorder, wantReason, wantMsgSubstr string) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", got)
	}
	var status map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("body is not valid JSON: %v — body: %s", err, rec.Body.String())
	}
	if status["kind"] != "Status" || status["apiVersion"] != "v1" {
		t.Errorf("envelope: got %#v, want v1.Status", status)
	}
	if wantReason != "" && status["reason"] != wantReason {
		t.Errorf("reason: got %v, want %q", status["reason"], wantReason)
	}
	if wantMsgSubstr != "" {
		msg, _ := status["message"].(string)
		if !strings.Contains(msg, wantMsgSubstr) {
			t.Errorf("message: %q does not contain %q", msg, wantMsgSubstr)
		}
	}
}
