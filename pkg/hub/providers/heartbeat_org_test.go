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

package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/client-go/rest"
)

func orgHeartbeatToken(cluster string) string {
	payload := fmt.Sprintf(`{"kubernetes.io/serviceaccount/clusterName":%q}`, cluster)
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
}

func TestHeartbeatTargetsTokenWorkspaceAndPreservesOtherScopes(t *testing.T) {
	for _, withPlatform := range []bool{false, true} {
		t.Run(fmt.Sprintf("platform=%t", withPlatform), func(t *testing.T) {
			reg := NewRegistry()
			if withPlatform {
				reg.Upsert(Provider{Name: "planner", CatalogEntryCluster: "platform-cluster"})
			}
			reg.Upsert(Provider{Name: "planner", OrgUUID: "org-a", CatalogEntryCluster: "org-a-cluster"})
			reg.Upsert(Provider{Name: "planner", OrgUUID: "org-b", CatalogEntryCluster: "org-b-cluster"})
			now := time.Now()
			f := &tokenReviewFake{status: authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: ProviderSAUsername}}}
			a := newTestTokenReviewAuthenticator(reg, f, &now)
			persisted := false
			handler := NewHeartbeatHandler(reg, func(ctx context.Context, name, version string, at time.Time) error {
				// The real recorder must get the same target the authenticator verified.
				cluster, ok := heartbeatCatalogCluster(ctx, reg, name)
				if !ok || cluster != "org-a-cluster" {
					t.Errorf("record cluster = %q, %t", cluster, ok)
				}
				persisted = true
				return nil
			}, a.Authenticate, HeartbeatAuthEnforce, logr.Discard())
			req := heartbeatRequestWithBearer("planner", orgHeartbeatToken("org-a-cluster"))
			// Caller-supplied tenant headers cannot change the token's workspace.
			req.Header.Set("X-Railgrid-Org", "org-b")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if !persisted || len(f.clusters) != 1 || f.clusters[0] != "org-a-cluster" {
				t.Fatalf("persisted=%t reviewed=%v", persisted, f.clusters)
			}
			p, _ := reg.GetForOrg("org-a", "planner")
			if !p.HeartbeatRequired || p.LastHeartbeat.IsZero() {
				t.Fatal("org-a heartbeat was not recorded")
			}
			other, _ := reg.GetForOrg("org-b", "planner")
			if other.HeartbeatRequired {
				t.Fatal("org-b heartbeat changed")
			}
			if platform, ok := reg.Get("planner"); ok && platform.HeartbeatRequired {
				t.Fatal("platform heartbeat changed")
			}
			reg.SweepStale(time.Now().Add(2*HeartbeatTTL), HeartbeatTTL)
			p, _ = reg.GetForOrg("org-a", "planner")
			if !p.HeartbeatStale {
				t.Fatal("org heartbeat was not subject to expiry")
			}
		})
	}
}

func TestHeartbeatClusterHintDoesNotAuthenticateOrBroadenTarget(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "planner", OrgUUID: "org-a", CatalogEntryCluster: "org-a-cluster"})
	now := time.Now()
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{Authenticated: false}}
	a := newTestTokenReviewAuthenticator(reg, f, &now)
	h := NewHeartbeatHandler(reg, nil, a.Authenticate, HeartbeatAuthEnforce, logr.Discard())
	for _, cluster := range []string{"org-a-cluster", "unregistered-cluster"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, heartbeatRequestWithBearer("planner", orgHeartbeatToken(cluster)))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("cluster %s: status = %d", cluster, w.Code)
		}
	}
	if f.reviews != 1 {
		t.Fatalf("reviews = %d, want only the registered cluster reviewed", f.reviews)
	}
	p, _ := reg.GetForOrg("org-a", "planner")
	if p.HeartbeatRequired {
		t.Fatal("an unverified token changed heartbeat state")
	}
}

func TestOrgHeartbeatPersistsInTheAuthenticatedCatalogWorkspace(t *testing.T) {
	var path string
	var patch struct {
		Status struct {
			LastHeartbeat   string `json:"lastHeartbeat"`
			ReportedVersion string `json:"reportedVersion"`
		} `json:"status"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"providers.railgrid.ai/v1alpha1","kind":"CatalogEntry","metadata":{"name":"planner"}}`))
	}))
	defer srv.Close()
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "planner", CatalogEntryCluster: "platform-cluster"})
	reg.Upsert(Provider{Name: "planner", OrgUUID: "org-a", CatalogEntryCluster: "org-a-cluster"})
	record, err := NewCatalogHeartbeatRecorder(&rest.Config{Host: srv.URL + "/clusters/root"}, reg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: ProviderSAUsername}}}
	a := newTestTokenReviewAuthenticator(reg, f, &now)
	h := NewHeartbeatHandler(reg, record, a.Authenticate, HeartbeatAuthEnforce, logr.Discard())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, heartbeatRequestWithBearer("planner", orgHeartbeatToken("org-a-cluster")))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	want := "/clusters/org-a-cluster/apis/providers.railgrid.ai/v1alpha1/catalogentries/planner/status"
	if path != want {
		t.Fatalf("PATCH path = %q, want %q", path, want)
	}
	if patch.Status.LastHeartbeat == "" {
		t.Fatal("no persisted heartbeat timestamp")
	}
}

func TestOrgHeartbeatRejectsAnotherServiceAccount(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "planner", OrgUUID: "org-a", CatalogEntryCluster: "org-a-cluster"})
	now := time.Now()
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: "system:serviceaccount:default:other"}}}
	a := newTestTokenReviewAuthenticator(reg, f, &now)
	h := NewHeartbeatHandler(reg, nil, a.Authenticate, HeartbeatAuthEnforce, logr.Discard())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, heartbeatRequestWithBearer("planner", orgHeartbeatToken("org-a-cluster")))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	p, _ := reg.GetForOrg("org-a", "planner")
	if p.HeartbeatRequired {
		t.Fatal("another service account moved the provider heartbeat")
	}
}

func TestOrgHeartbeatWarnModeStillRequiresAuthentication(t *testing.T) {
	for _, status := range []authnv1.TokenReviewStatus{
		{Authenticated: false},
		{Authenticated: true, User: authnv1.UserInfo{Username: "system:serviceaccount:default:other"}},
	} {
		reg := NewRegistry()
		reg.Upsert(Provider{Name: "planner", CatalogEntryCluster: "platform-cluster"})
		reg.Upsert(Provider{Name: "planner", OrgUUID: "org-a", CatalogEntryCluster: "org-a-cluster"})
		now := time.Now()
		f := &tokenReviewFake{status: status}
		a := newTestTokenReviewAuthenticator(reg, f, &now)
		persisted := false
		h := NewHeartbeatHandler(reg, func(context.Context, string, string, time.Time) error { persisted = true; return nil }, a.Authenticate, HeartbeatAuthWarn, logr.Discard())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, heartbeatRequestWithBearer("planner", orgHeartbeatToken("org-a-cluster")))
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want rejection even in warn mode", w.Code)
		}
		p, _ := reg.GetForOrg("org-a", "planner")
		if persisted || p.HeartbeatRequired {
			t.Fatal("unverified org heartbeat changed state in warn mode")
		}
		platform, _ := reg.Get("planner")
		if platform.HeartbeatRequired {
			t.Fatal("rejected org heartbeat fell back to the platform provider")
		}
	}
}

func TestHeartbeatUnmatchedWorkspaceHintPreservesPlatformAuthPolicy(t *testing.T) {
	for _, tc := range []struct {
		mode   HeartbeatAuthMode
		status int
	}{
		{HeartbeatAuthWarn, http.StatusOK},
		{HeartbeatAuthEnforce, http.StatusForbidden},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			reg := NewRegistry()
			reg.Upsert(Provider{Name: "planner", CatalogEntryCluster: "platform-cluster"})
			reg.Upsert(Provider{Name: "planner", OrgUUID: "org-a", CatalogEntryCluster: "org-a-cluster"})
			now := time.Now()
			f := &tokenReviewFake{omitClusterExtra: true, status: authnv1.TokenReviewStatus{
				Authenticated: true,
				User: authnv1.UserInfo{Username: ProviderSAUsername, Extra: map[string]authnv1.ExtraValue{
					"authentication.kcp.io/cluster-name": {"operator-cluster"},
				}},
			}}
			a := newTestTokenReviewAuthenticator(reg, f, &now)
			persisted := false
			h := NewHeartbeatHandler(reg, func(ctx context.Context, name, _ string, _ time.Time) error {
				cluster, ok := heartbeatCatalogCluster(ctx, reg, name)
				if !ok || cluster != "platform-cluster" {
					t.Errorf("record cluster = %q, %t", cluster, ok)
				}
				persisted = true
				return nil
			}, a.Authenticate, tc.mode, logr.Discard())
			w := httptest.NewRecorder()
			h.ServeHTTP(w, heartbeatRequestWithBearer("planner", orgHeartbeatToken("operator-cluster")))
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if f.reviews != 1 || f.clusters[0] != "platform-cluster" {
				t.Fatalf("reviews=%d clusters=%v", f.reviews, f.clusters)
			}
			platform, _ := reg.Get("planner")
			accepted := tc.mode == HeartbeatAuthWarn
			if persisted != accepted || platform.HeartbeatRequired != accepted {
				t.Fatalf("persisted=%t heartbeatRequired=%t, want %t", persisted, platform.HeartbeatRequired, accepted)
			}
			org, _ := reg.GetForOrg("org-a", "planner")
			if org.HeartbeatRequired {
				t.Fatal("unmatched workspace hint changed org heartbeat")
			}
		})
	}
}

func TestHeartbeatRealJWTBeforeCatalogObservationIsRetryable(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "planner"})
	now := time.Now()
	f := &tokenReviewFake{}
	a := newTestTokenReviewAuthenticator(reg, f, &now)
	h := NewHeartbeatHandler(reg, nil, a.Authenticate, HeartbeatAuthEnforce, logr.Discard())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, heartbeatRequestWithBearer("planner", orgHeartbeatToken("new-cluster")))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want retryable 503", w.Code)
	}
	if f.reviews != 0 {
		t.Fatal("reviewed an unobserved workspace")
	}
}

func TestHeartbeatCacheDoesNotSurviveCatalogWorkspaceReplacement(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "planner", CatalogEntryCluster: "old-cluster"})
	now := time.Now()
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: ProviderSAUsername}}}
	a := newTestTokenReviewAuthenticator(reg, f, &now)
	req := heartbeatRequestWithBearer("planner", "same-token")
	if err := a.Authenticate(context.Background(), req, "planner"); err != nil {
		t.Fatal(err)
	}
	reg.Upsert(Provider{Name: "planner", CatalogEntryCluster: "new-cluster"})
	f.status.Authenticated = false
	if err := a.Authenticate(context.Background(), req, "planner"); err == nil {
		t.Fatal("old workspace authentication cache accepted replacement workspace")
	}
	if f.reviews != 2 || f.clusters[1] != "new-cluster" {
		t.Fatalf("reviews=%d clusters=%v", f.reviews, f.clusters)
	}
}

func TestHeartbeatRejectsVerifiedServiceAccountFromAnotherWorkspace(t *testing.T) {
	for _, extra := range []map[string]authnv1.ExtraValue{
		nil,
		{"authentication.kcp.io/cluster-name": {"another-cluster"}},
		{"authentication.kcp.io/cluster-name": {"org-a-cluster", "another-cluster"}},
	} {
		reg := NewRegistry()
		reg.Upsert(Provider{Name: "planner", OrgUUID: "org-a", CatalogEntryCluster: "org-a-cluster"})
		now := time.Now()
		f := &tokenReviewFake{omitClusterExtra: true, status: authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: ProviderSAUsername, Extra: extra}}}
		a := newTestTokenReviewAuthenticator(reg, f, &now)
		h := NewHeartbeatHandler(reg, nil, a.Authenticate, HeartbeatAuthEnforce, logr.Discard())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, heartbeatRequestWithBearer("planner", orgHeartbeatToken("org-a-cluster")))
		if w.Code != http.StatusForbidden {
			t.Fatalf("verified extra %v: status = %d, want 403", extra, w.Code)
		}
		p, _ := reg.GetForOrg("org-a", "planner")
		if p.HeartbeatRequired {
			t.Fatal("another workspace changed the provider heartbeat")
		}
	}
}
