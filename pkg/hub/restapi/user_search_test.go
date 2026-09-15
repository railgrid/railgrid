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

package restapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

func searchTestUser(name, email, displayName string) *tenancyv1alpha1.User {
	return &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       tenancyv1alpha1.UserSpec{Email: email, Name: displayName, RBACIdentity: "railgrid:" + email},
	}
}

func getUserSearch(t *testing.T, srv *httptest.Server, q string) (int, http.Header, []UserSuggestion) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/users/search?q=" + url.QueryEscape(q))
	if err != nil {
		t.Fatalf("GET /api/users/search: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body ListResponse[UserSuggestion]
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decoding: %v", err)
		}
	}
	return resp.StatusCode, resp.Header, body.Items
}

func TestSearchUsers(t *testing.T) {
	deleting := searchTestUser("user-gone", "carol.gone@example.com", "Carol Gone")
	now := metav1.Now()
	deleting.Status.DeletionRequestedAt = &now
	static := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "static-user-47b9dce0e91570a1"},
		Spec:       tenancyv1alpha1.UserSpec{Name: "railgrid:static:47b9dce0e91570a1", RBACIdentity: "railgrid:static:47b9dce0e91570a1"},
	}
	mgr, _, _ := newTestManager(t,
		searchTestUser("user-alice", "alice@example.com", "Alice"),
		searchTestUser("user-carol", "Carol.Smith@example.com", "Carol Smith"),
		searchTestUser("user-carl", "carl@example.com", "Carolina Jones"),
		deleting,
		static,
	)
	srv := newTestServer(t, mgr, adminTC("user-alice", "", ""))
	defer srv.Close()

	for _, tc := range []struct {
		name string
		q    string
		want []string
	}{
		// Case-insensitive email prefix; a soft-deleted account is left out.
		{name: "email prefix", q: "CAROL.", want: []string{"user-carol"}},
		// Email and display-name matches combine, sorted by email.
		{name: "email or name prefix", q: "carol", want: []string{"user-carl", "user-carol"}},
		// Display-name prefix matches too.
		{name: "name prefix", q: "carolina", want: []string{"user-carl"}},
		// The caller is never suggested to themselves.
		{name: "self excluded", q: "alice@", want: nil},
		// Static-token users have no email and are never suggested, even
		// though their display names share this prefix.
		{name: "static excluded", q: "railgrid:static:", want: nil},
		// Prefix only, not substring.
		{name: "no substring match", q: "example.com", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, items := getUserSearch(t, srv, tc.q)
			if code != http.StatusOK {
				t.Fatalf("status %d, want 200", code)
			}
			var got []string
			for _, s := range items {
				got = append(got, s.User)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("q=%q suggested %v, want %v", tc.q, got, tc.want)
			}
		})
	}

	if code, _, _ := getUserSearch(t, srv, "caro"); code != http.StatusBadRequest {
		t.Errorf("4-character query: status %d, want 400", code)
	}
}

func TestSearchUsersCapsResults(t *testing.T) {
	var objs []runtime.Object
	for i := 7; i >= 0; i-- {
		objs = append(objs, searchTestUser(fmt.Sprintf("user-%d", i), fmt.Sprintf("teammate%d@example.com", i), ""))
	}
	mgr, _, _ := newTestManager(t, objs...)
	srv := newTestServer(t, mgr, adminTC("someone", "", ""))
	defer srv.Close()

	code, _, items := getUserSearch(t, srv, "teammate")
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	var got []string
	for _, s := range items {
		got = append(got, s.Email)
	}
	want := []string{"teammate0@example.com", "teammate1@example.com", "teammate2@example.com", "teammate3@example.com", "teammate4@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want the first %d by email", got, UserSearchMaxResults)
	}
}

func TestSearchUsersRateLimited(t *testing.T) {
	mgr, _, _ := newTestManager(t, searchTestUser("user-bob", "bob@example.com", "Bob"))
	srv := newTestServer(t, mgr, adminTC("user-alice", "", ""))
	defer srv.Close()

	// Invalid queries are charged too, so probing costs the same.
	for i := range userSearchBurst {
		if code, _, _ := getUserSearch(t, srv, "b"); code != http.StatusBadRequest {
			t.Fatalf("request %d: status %d, want 400", i, code)
		}
	}
	code, hdr, _ := getUserSearch(t, srv, "bob@example")
	if code != http.StatusTooManyRequests {
		t.Fatalf("request past the burst: status %d, want 429", code)
	}
	if hdr.Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
}

func TestSearchUsersRefusesDelegatedCalls(t *testing.T) {
	mgr, _, _ := newTestManager(t, searchTestUser("user-bob", "bob@example.com", "Bob"))
	h := NewHandler(mgr)
	r := mux.NewRouter()
	sub := r.PathPrefix("/api").Subrouter()
	sub.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := tenant.WithContext(req.Context(), tenant.TenantContext{User: "user-alice"})
			ctx = tenant.WithDelegatedCall(ctx, tenant.DelegatedCall{User: "user-alice", Provider: "app-studio"})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	h.RegisterUserOnly(sub)
	srv := httptest.NewServer(r)
	defer srv.Close()

	if code, _, _ := getUserSearch(t, srv, "bob@example"); code != http.StatusForbidden {
		t.Fatalf("delegated call: status %d, want 403", code)
	}
}
