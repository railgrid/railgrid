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

package builder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/rest"
)

// fakeKCP simulates the two kcp endpoints authorize() talks to —
// TokenReview and SubjectAccessReview — per logical cluster, recording
// which cluster each review was created in and what identity the SAR
// carried.
type fakeKCP struct {
	// identities maps "cluster|token" to the username TokenReview resolves.
	// Tokens without an entry for the requested cluster are unauthenticated
	// (mirrors kcp's cluster-scoped SA authn).
	identities map[string]string

	trCluster  string // cluster the TokenReview was created in
	sarCluster string // cluster the SAR was created in
	sarUser    string
	sarGroups  []string
	sarAllowed bool
}

func (f *fakeKCP) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Paths look like /clusters/{cluster}/apis/{group}/v1/{resource}.
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/clusters/"), "/", 2)
		if len(parts) != 2 {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		cluster := parts[0]
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/tokenreviews"):
			var tr authenticationv1.TokenReview
			if err := json.NewDecoder(r.Body).Decode(&tr); err != nil {
				t.Errorf("decoding TokenReview: %v", err)
			}
			f.trCluster = cluster
			username, ok := f.identities[cluster+"|"+tr.Spec.Token]
			tr.Status.Authenticated = ok
			tr.Status.User.Username = username
			if ok {
				tr.Status.User.Groups = []string{"system:serviceaccounts", "system:authenticated"}
			}
			_ = json.NewEncoder(w).Encode(&tr)
		case strings.HasSuffix(r.URL.Path, "/subjectaccessreviews"):
			var sar authorizationv1.SubjectAccessReview
			if err := json.NewDecoder(r.Body).Decode(&sar); err != nil {
				t.Errorf("decoding SAR: %v", err)
			}
			f.sarCluster = cluster
			f.sarUser = sar.Spec.User
			f.sarGroups = sar.Spec.Groups
			sar.Status.Allowed = f.sarAllowed
			_ = json.NewEncoder(w).Encode(&sar)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

// TestAuthorize_ForeignSAToken verifies the provider-SA path: the
// TokenReview runs in the token's home cluster (kcp SA authn is cluster-
// scoped), while the SAR runs in the TARGET cluster — the #68 invariant —
// under the cluster-qualified synthetic identity with groups dropped.
func TestAuthorize_ForeignSAToken(t *testing.T) {
	const (
		homeCluster   = "provider-ws-cluster"
		targetCluster = "tenant-ws-cluster"
	)
	token := fakeSAToken(homeCluster)

	fake := &fakeKCP{
		identities: map[string]string{
			// The token only authenticates in its home cluster.
			homeCluster + "|" + token: "system:serviceaccount:default:provider",
		},
		sarAllowed: true,
	}
	srv := fake.server(t)
	defer srv.Close()

	err := authorize(context.Background(), &rest.Config{Host: srv.URL}, token, targetCluster, "proxy", "edges", "my-edge")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if fake.trCluster != homeCluster {
		t.Errorf("TokenReview cluster = %q, want home cluster %q", fake.trCluster, homeCluster)
	}
	if fake.sarCluster != targetCluster {
		t.Errorf("SAR cluster = %q, want target cluster %q (the #68 invariant)", fake.sarCluster, targetCluster)
	}
	// kcp.s native cross-workspace SA form (GlobalServiceAccount aliasing) —
	// the same subject kcp.s own RBAC resolution would match.
	wantUser := "system:kcp:serviceaccount:" + homeCluster + ":default:provider"
	if fake.sarUser != wantUser {
		t.Errorf("SAR user = %q, want qualified %q", fake.sarUser, wantUser)
	}
	if len(fake.sarGroups) != 0 {
		t.Errorf("SAR groups = %v, want none (foreign SA groups must be dropped)", fake.sarGroups)
	}
}

// TestAuthorize_ForeignSADenied verifies a foreign SA without the grant is
// refused: the SAR runs but RBAC says no.
func TestAuthorize_ForeignSADenied(t *testing.T) {
	const homeCluster = "provider-ws-cluster"
	token := fakeSAToken(homeCluster)
	fake := &fakeKCP{
		identities: map[string]string{
			homeCluster + "|" + token: "system:serviceaccount:default:provider",
		},
		sarAllowed: false,
	}
	srv := fake.server(t)
	defer srv.Close()

	if err := authorize(context.Background(), &rest.Config{Host: srv.URL}, token, "tenant-ws", "proxy", "edges", "e"); err == nil {
		t.Fatal("authorize succeeded, want denial")
	}
}

// TestAuthorize_ForeignSANonSAIdentity verifies a token that CLAIMS to be a
// kcp SA token but resolves to a non-SA identity in its home cluster is
// refused rather than authorized under an unqualifiable name.
func TestAuthorize_ForeignSANonSAIdentity(t *testing.T) {
	const homeCluster = "weird-cluster"
	token := fakeSAToken(homeCluster)
	fake := &fakeKCP{
		identities: map[string]string{
			homeCluster + "|" + token: "alice@example.com",
		},
		sarAllowed: true,
	}
	srv := fake.server(t)
	defer srv.Close()

	if err := authorize(context.Background(), &rest.Config{Host: srv.URL}, token, "tenant-ws", "proxy", "edges", "e"); err == nil {
		t.Fatal("authorize succeeded for SA-claimed token with non-SA identity, want refusal")
	}
}

// TestAuthorize_NonSAToken verifies the pre-existing path is untouched:
// both reviews run in the target cluster and the SAR carries the
// TokenReview identity verbatim.
func TestAuthorize_NonSAToken(t *testing.T) {
	const targetCluster = "tenant-ws-cluster"
	token := "opaque-oidc-token"
	fake := &fakeKCP{
		identities: map[string]string{
			targetCluster + "|" + token: "alice@example.com",
		},
		sarAllowed: true,
	}
	srv := fake.server(t)
	defer srv.Close()

	if err := authorize(context.Background(), &rest.Config{Host: srv.URL}, token, targetCluster, "proxy", "edges", "e"); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if fake.trCluster != targetCluster {
		t.Errorf("TokenReview cluster = %q, want %q", fake.trCluster, targetCluster)
	}
	if fake.sarCluster != targetCluster {
		t.Errorf("SAR cluster = %q, want %q", fake.sarCluster, targetCluster)
	}
	if fake.sarUser != "alice@example.com" {
		t.Errorf("SAR user = %q, want alice@example.com", fake.sarUser)
	}
	if len(fake.sarGroups) == 0 {
		t.Errorf("SAR groups empty, want TokenReview groups passed through for non-SA tokens")
	}
}

// TestAuthorize_SameClusterSAToken verifies an SA token whose home IS the
// target cluster takes the plain path: no qualification, groups intact.
func TestAuthorize_SameClusterSAToken(t *testing.T) {
	const cluster = "tenant-ws-cluster"
	token := fakeSAToken(cluster)
	fake := &fakeKCP{
		identities: map[string]string{
			cluster + "|" + token: "system:serviceaccount:default:tenant-sa",
		},
		sarAllowed: true,
	}
	srv := fake.server(t)
	defer srv.Close()

	if err := authorize(context.Background(), &rest.Config{Host: srv.URL}, token, cluster, "proxy", "edges", "e"); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if fake.sarUser != "system:serviceaccount:default:tenant-sa" {
		t.Errorf("SAR user = %q, want unqualified local SA name", fake.sarUser)
	}
	if len(fake.sarGroups) == 0 {
		t.Errorf("SAR groups empty, want groups intact for same-cluster SA")
	}
}
