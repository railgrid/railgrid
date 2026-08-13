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

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/oauth2"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros/pkg/browsersession"
)

func TestBrowserSessionBootstrapAndLogout(t *testing.T) {
	store := browsersession.New(browsersession.Config{TTL: time.Hour})
	handler := NewBrowserSessionHandler(store, func(r *http.Request) (browsersession.Identity, error) {
		if r.Header.Get("Authorization") != "Bearer portal-token" {
			return browsersession.Identity{}, browsersession.ErrInvalid
		}
		return browsersession.Identity{UserID: "user-1", Email: "one@example.test"}, nil
	})
	router := mux.NewRouter()
	handler.RegisterBrowserSessionRoutes(router)
	bootstrap := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/session/bootstrap", nil)
	request.Header.Set("Authorization", "Bearer portal-token")
	router.ServeHTTP(bootstrap, request)
	if bootstrap.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d; body=%s", bootstrap.Code, bootstrap.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(bootstrap.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["userId"] != "user-1" || body["email"] != "one@example.test" || body["authenticated"] != true {
		t.Fatalf("bootstrap body = %#v", body)
	}
	cookie := bootstrap.Result().Cookies()[0]
	if cookie.Name != browsersession.CookieName || !cookie.HttpOnly || !cookie.Secure || cookie.Value == "user-1" {
		t.Fatalf("bootstrap cookie = %#v", cookie)
	}
	logout := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	logoutRequest.AddCookie(cookie)
	router.ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d; body=%s", logout.Code, logout.Body.String())
	}
	if _, err := store.Resolve(context.Background(), cookie.Value); err == nil {
		t.Fatal("logout left browser session live")
	}
}

func TestBrowserSessionBootstrapFallsBackToLiveCookieWhenBearerIsUnavailable(t *testing.T) {
	store := browsersession.New(browsersession.Config{TTL: time.Hour})
	value, _, err := store.Issue(context.Background(), browsersession.Identity{UserID: "user-1", Email: "one@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewBrowserSessionHandler(store, func(*http.Request) (browsersession.Identity, error) {
		return browsersession.Identity{}, browsersession.ErrInvalid
	})
	router := mux.NewRouter()
	handler.RegisterBrowserSessionRoutes(router)
	request := httptest.NewRequest(http.MethodPost, "/auth/session/bootstrap", nil)
	request.Header.Set("Authorization", "Bearer expired-portal-token")
	request.AddCookie(&http.Cookie{Name: browsersession.CookieName, Value: value})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d; body=%s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["authenticated"] != true || body["userId"] != "user-1" || body["email"] != "one@example.test" {
		t.Fatalf("cookie fallback body = %#v", body)
	}
	if got := response.Header().Get("Set-Cookie"); got != "" {
		t.Fatalf("cookie fallback unexpectedly replaced session: %q", got)
	}
	if strings.Contains(response.Body.String(), "expired-portal-token") {
		t.Fatal("bootstrap response echoed bearer credential")
	}
}

func TestBrowserSessionLogoutGETRedirectsAndExpiresCookie(t *testing.T) {
	store := browsersession.New(browsersession.Config{TTL: time.Hour})
	value, _, err := store.Issue(context.Background(), browsersession.Identity{UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewBrowserSessionHandler(store, nil)
	router := mux.NewRouter()
	handler.RegisterBrowserSessionRoutes(router)
	request := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	request.AddCookie(&http.Cookie{Name: browsersession.CookieName, Value: value})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	// The portal SPA is mounted under /ui/ — a root-level /login 404s.
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/ui/login" {
		t.Fatalf("logout redirect status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logout Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("logout cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != browsersession.CookieName || cookie.MaxAge != -1 || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" {
		t.Fatalf("logout cookie = %#v", cookie)
	}
	if _, err := store.Resolve(context.Background(), value); !errors.Is(err, browsersession.ErrRevoked) {
		t.Fatalf("logout session error = %v, want ErrRevoked", err)
	}
}

func TestLegacyAuthorizeForceRequestsFreshIdentityProviderLogin(t *testing.T) {
	handler := &Handler{
		oauth2Config:   &oauth2.Config{ClientID: "faros", Endpoint: oauth2.Endpoint{AuthURL: "https://idp.example.test/auth"}},
		hubExternalURL: "https://hub.example.test",
		rateLimiter:    newRateLimiter(defaultRateLimit, defaultBurstDuration, klog.Background()),
	}
	request := httptest.NewRequest(http.MethodGet, "/auth/authorize?s=session&v=verifier&p=1234&force=1", nil)
	response := httptest.NewRecorder()
	handler.HandleAuthorize(response, request)
	if response.Code != http.StatusFound {
		t.Fatalf("authorize status = %d", response.Code)
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("prompt") != "login" || location.Query().Get("max_age") != "0" {
		t.Fatalf("force authorize query = %s", location.RawQuery)
	}
}
