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

package hub

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"

	"github.com/railgrid/railgrid/pkg/browsersession"
	"github.com/railgrid/railgrid/pkg/hub/appauth"
)

func TestOptionalAppIdentityIssuerPreservesPublishedAppRoutes(t *testing.T) {
	for _, tc := range []struct{ hubURL, issuer string }{
		{"https://Hub.Example.com:443/", "https://hub.example.com/auth/apps/identity"},
		{"https://localhost:9443/", "https://localhost:9443/auth/apps/identity"},
		{"http://localhost:9443", ""},
		{"https://hub.example.com/prefix", ""},
		{"", ""},
	} {
		t.Run(tc.hubURL, func(t *testing.T) {
			cfg := appauth.Config{
				Sessions:     browsersession.New(browsersession.Config{}),
				SARClient:    func(string) (authorizationv1client.SubjectAccessReviewInterface, error) { return nil, nil },
				InstanceHost: func(context.Context, appauth.InstanceRef) (string, error) { return "app.example.test", nil },
				IdentityApp: func(context.Context, appauth.InstanceRef) (appauth.IdentityApp, error) {
					return appauth.IdentityApp{}, nil
				},
				IdentityAccountLookup: func(context.Context, string) (appauth.IdentityAccount, error) { return appauth.IdentityAccount{}, nil },
				IdentityKeys: func(context.Context) (appauth.IdentityKeySnapshot, error) {
					key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
					return appauth.IdentityKeySnapshot{SigningKey: key, SubjectKey: make([]byte, 32), PublicKeys: []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}}, nil
				},
			}
			err := configureAppIdentityIssuer(&cfg, tc.hubURL)
			if (err != nil) != (tc.issuer == "") || cfg.IdentityIssuer != tc.issuer {
				t.Fatalf("issuer/error=%q/%v", cfg.IdentityIssuer, err)
			}
			handler, err := appauth.New(cfg)
			if err != nil {
				t.Fatalf("optional auth prevented handler startup: %v", err)
			}
			router := mux.NewRouter()
			handler.RegisterRoutes(router, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, appauth.AuthorizePath, nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("published-app authorize unavailable: %d", response.Code)
			}
			response = httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, appauth.IdentityJWKSPath, nil))
			want := http.StatusOK
			if tc.issuer == "" {
				want = http.StatusNotFound
			}
			if response.Code != want {
				t.Fatalf("identity JWKS status=%d, want %d: %s", response.Code, want, response.Body.String())
			}
		})
	}
}
