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

package browsersession

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

type repeatingSecretReader struct {
	secret []byte
	reads  int
}

func (r *repeatingSecretReader) Read(p []byte) (int, error) {
	r.reads++
	if len(p) != len(r.secret) {
		return 0, fmt.Errorf("unexpected secret buffer length %d", len(p))
	}
	copy(p, r.secret)
	return len(p), nil
}

func secretBytes(value byte) []byte {
	return bytes.Repeat([]byte{value}, secretSize)
}

func TestIssueUsesOpaqueHostOnlyCookieAndServerSideIdentity(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	store := New(Config{TTL: time.Hour, Now: clock.Now})
	response := httptest.NewRecorder()
	session, err := store.IssueHTTP(response, Identity{UserID: "user-1", Email: "one@example.test", AuthType: "oidc"})
	if err != nil {
		t.Fatal(err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != CookieName || cookie.Value == "user-1" || cookie.Value == "one@example.test" {
		t.Fatalf("cookie leaked identity: %#v", cookie)
	}
	if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie security attributes = %#v", cookie)
	}
	stored, ok := store.sessions[tokenKey(cookie.Value)]
	if !ok {
		t.Fatalf("stored session missing for issued cookie")
	}
	if strings.Contains(fmt.Sprintf("%#v", stored), cookie.Value) {
		t.Fatalf("raw cookie handle retained in stored session: %#v", stored)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	resolved, err := store.ResolveRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != session.ID || resolved.Identity.UserID != "user-1" {
		t.Fatalf("resolved session = %#v", resolved)
	}
}

func TestIssueHTTPCookieMaxAgeMatchesStoreTTL(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	const ttl = time.Hour
	store := New(Config{TTL: ttl, Now: clock.Now})
	response := httptest.NewRecorder()
	if _, err := store.IssueHTTP(response, Identity{UserID: "user-1"}); err != nil {
		t.Fatal(err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v", cookies)
	}
	if got, want := cookies[0].MaxAge, int(ttl/time.Second); got != want {
		t.Fatalf("cookie MaxAge = %d, want %d seconds", got, want)
	}
}

func TestExpiryAndRevokeAreAuthoritative(t *testing.T) {
	clock := &testClock{now: time.Unix(200, 0)}
	store := New(Config{TTL: time.Minute, Now: clock.Now})
	value, _, err := store.Issue(Identity{UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(value); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(value); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked resolve error = %v, want ErrRevoked", err)
	}
	value, _, err = store.Issue(Identity{UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if _, err := store.Resolve(value); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired resolve error = %v, want ErrExpired", err)
	}
}

func TestStoreEvictsOldestAtBound(t *testing.T) {
	clock := &testClock{now: time.Unix(300, 0)}
	store := New(Config{TTL: time.Hour, MaxEntries: 1, Now: clock.Now})
	first, _, err := store.Issue(Identity{UserID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Second)
	second, _, err := store.Issue(Identity{UserID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("evicted first session error = %v, want ErrNotFound", err)
	}
	if _, err := store.Resolve(second); err != nil {
		t.Fatal(err)
	}
}

func TestRevokedValuesAreBounded(t *testing.T) {
	clock := &testClock{now: time.Unix(400, 0)}
	store := New(Config{TTL: time.Hour, MaxEntries: 2, Now: clock.Now})
	for _, value := range []string{"unknown-1", "unknown-2", "unknown-3"} {
		if err := store.Revoke(value); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := len(store.revoked), 2; got > want {
		t.Fatalf("revoked entries = %d, want at most %d", got, want)
	}
}

func TestIssueRetriesExistingCollisionWithoutOverwriting(t *testing.T) {
	clock := &testClock{now: time.Unix(500, 0)}
	random := bytes.NewReader(append(append(secretBytes(1), secretBytes(1)...), secretBytes(2)...))
	store := New(Config{TTL: time.Hour, Now: clock.Now, Random: random})
	first, _, err := store.Issue(Identity{UserID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.Issue(Identity{UserID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("collision retry returned the existing handle %q", first)
	}
	resolvedFirst, err := store.Resolve(first)
	if err != nil || resolvedFirst.Identity.UserID != "first" {
		t.Fatalf("first session after collision = %#v, err=%v", resolvedFirst, err)
	}
	resolvedSecond, err := store.Resolve(second)
	if err != nil || resolvedSecond.Identity.UserID != "second" {
		t.Fatalf("second session after collision = %#v, err=%v", resolvedSecond, err)
	}
}

func TestIssueRetriesRevokedCollisionWithoutResurrection(t *testing.T) {
	clock := &testClock{now: time.Unix(600, 0)}
	random := bytes.NewReader(append(append(secretBytes(3), secretBytes(3)...), secretBytes(4)...))
	store := New(Config{TTL: time.Hour, Now: clock.Now, Random: random})
	revoked, _, err := store.Issue(Identity{UserID: "revoked"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(revoked); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := store.Issue(Identity{UserID: "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	if fresh == revoked {
		t.Fatalf("collision retry resurrected revoked handle %q", revoked)
	}
	if _, err := store.Resolve(revoked); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked handle after collision = %v, want ErrRevoked", err)
	}
	resolved, err := store.Resolve(fresh)
	if err != nil || resolved.Identity.UserID != "fresh" {
		t.Fatalf("fresh session after revoked collision = %#v, err=%v", resolved, err)
	}
}

func TestIssueFailsClosedAfterBoundedCollisions(t *testing.T) {
	clock := &testClock{now: time.Unix(700, 0)}
	random := &repeatingSecretReader{secret: secretBytes(5)}
	store := New(Config{TTL: time.Hour, Now: clock.Now, Random: random})
	first, _, err := store.Issue(Identity{UserID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Issue(Identity{UserID: "second"}); err == nil {
		t.Fatal("repeated token collision unexpectedly issued a session")
	}
	if got, want := random.reads, 1+maxIssueAttempts; got != want {
		t.Fatalf("random reads after collision exhaustion = %d, want %d", got, want)
	}
	resolved, err := store.Resolve(first)
	if err != nil || resolved.Identity.UserID != "first" {
		t.Fatalf("first session after collision exhaustion = %#v, err=%v", resolved, err)
	}
}
