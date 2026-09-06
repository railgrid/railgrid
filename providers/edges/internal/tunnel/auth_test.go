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

package tunnel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/rest"
)

// The hub replaces a caller's own bearer with a delegated ServiceAccount token
// before a request reaches a provider through the backend proxy — always for an
// org-owned provider (which rides this provider's tunnel), and for platform
// providers when --provider-delegated-tokens selects them. These tests pin what
// authorize() does with such a token: it must resolve to a usable identity in
// the workspace that minted it and pass the `proxy` SAR the tenant's RBAC
// grants that account.

// delegatedSAToken builds a token shaped like the one
// pkg/hub/serviceaccounts.IssueDelegatedUserToken mints: a bound (TokenRequest)
// ServiceAccount JWT, whose cluster lives in the NESTED kubernetes.io
// claim and whose issuer is the kcp API server, not "kubernetes/serviceaccount".
func delegatedSAToken(t *testing.T, cluster, name string) string {
	t.Helper()
	claims := map[string]any{
		"iss": "https://kcp.default.svc",
		"sub": "system:serviceaccount:default:" + name,
		"kubernetes.io": map[string]any{
			"clusterName":    cluster,
			"namespace":      "default",
			"serviceaccount": map[string]any{"name": name},
		},
	}
	return encodeJWT(t, claims)
}

// legacySAToken builds the older, secret-backed kcp SA token shape: flat
// clusterName claim and the "kubernetes/serviceaccount" issuer.
func legacySAToken(t *testing.T, cluster, name string) string {
	t.Helper()
	return encodeJWT(t, map[string]any{
		"iss": "kubernetes/serviceaccount",
		"kubernetes.io/serviceaccount/clusterName":          cluster,
		"kubernetes.io/serviceaccount/service-account.name": name,
	})
}

func encodeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// authRecorder is a fake kcp endpoint that answers TokenReview and
// SubjectAccessReview, recording what it was asked. authorize() builds its own
// clients from *rest.Config, so the only seam is the Host.
type authRecorder struct {
	// username / groups are what TokenReview reports back.
	username string
	groups   []string
	// authenticated and allowed drive the two answers.
	authenticated bool
	allowed       bool

	tokenReviewPaths []string
	reviewedTokens   []string
	sarPaths         []string
	sarUsers         []string
	sarGroups        [][]string
	sarAttributes    []authorizationv1.ResourceAttributes
}

func (a *authRecorder) start(t *testing.T) *rest.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/tokenreviews"):
			var in authenticationv1.TokenReview
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("decode TokenReview: %v", err)
			}
			a.tokenReviewPaths = append(a.tokenReviewPaths, r.URL.Path)
			a.reviewedTokens = append(a.reviewedTokens, in.Spec.Token)
			in.Status = authenticationv1.TokenReviewStatus{
				Authenticated: a.authenticated,
				User:          authenticationv1.UserInfo{Username: a.username, Groups: a.groups},
			}
			writeJSON(t, w, &in)
		case strings.HasSuffix(r.URL.Path, "/subjectaccessreviews"):
			var in authorizationv1.SubjectAccessReview
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("decode SubjectAccessReview: %v", err)
			}
			a.sarPaths = append(a.sarPaths, r.URL.Path)
			a.sarUsers = append(a.sarUsers, in.Spec.User)
			a.sarGroups = append(a.sarGroups, in.Spec.Groups)
			if in.Spec.ResourceAttributes != nil {
				a.sarAttributes = append(a.sarAttributes, *in.Spec.ResourceAttributes)
			}
			in.Status = authorizationv1.SubjectAccessReviewStatus{Allowed: a.allowed, Reason: "test"}
			writeJSON(t, w, &in)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &rest.Config{Host: srv.URL}
}

func writeJSON(t *testing.T, w http.ResponseWriter, obj any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(obj); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

// The delegated account lives in the caller's own team workspace, which is the
// same workspace the request addresses. So the token is not "foreign": it
// authenticates natively through the APIExport virtual workspace scoped to that
// cluster, keeps its real groups, and is authorized under its plain
// system:serviceaccount: name — the subject the hub's cluster-admin binding for
// faros-du-* names.
func TestAuthorizeAcceptsADelegatedUserToken(t *testing.T) {
	const cluster = "260dym853j73uupr"
	const sa = "faros-du-9f2c1a7b5e4d3c2b1a09"

	rec := &authRecorder{
		username:      "system:serviceaccount:default:" + sa,
		groups:        []string{"system:serviceaccounts", "system:serviceaccounts:default", "system:authenticated"},
		authenticated: true,
		allowed:       true,
	}
	tenantCfg := rec.start(t)
	// A distinct kcpConfig, so a test that wrongly took the foreign-SA branch
	// would review against a server that is not running.
	kcpCfg := &rest.Config{Host: "https://provider-workspace.invalid"}

	token := delegatedSAToken(t, cluster, sa)
	err := authorize(context.Background(), tenantCfg, kcpCfg, token, cluster,
		"proxy", "edges.faros.sh", "services", "provider-infrastructure")
	if err != nil {
		t.Fatalf("authorize() = %v, want nil — a delegated token must reach an edge the caller may use", err)
	}

	if len(rec.tokenReviewPaths) != 1 || len(rec.sarPaths) != 1 {
		t.Fatalf("got %d TokenReviews and %d SARs, want 1 each", len(rec.tokenReviewPaths), len(rec.sarPaths))
	}
	if rec.reviewedTokens[0] != token {
		t.Error("TokenReview did not carry the delegated token")
	}
	// Both reviews go to the tenant config (the APIExport VW for the addressed
	// cluster), never to the provider's own workspace.
	if strings.Contains(rec.tokenReviewPaths[0], "provider-workspace") {
		t.Errorf("TokenReview path %q, want the tenant VW", rec.tokenReviewPaths[0])
	}
	if got := rec.sarUsers[0]; got != "system:serviceaccount:default:"+sa {
		t.Errorf("SAR user = %q, want the plain SA subject the workspace binding names", got)
	}
	if len(rec.sarGroups[0]) != len(rec.groups) {
		t.Errorf("SAR groups = %v, want the reviewed groups kept for a workspace-local SA", rec.sarGroups[0])
	}
	want := authorizationv1.ResourceAttributes{
		Verb: "proxy", Group: "edges.faros.sh", Version: "v1alpha1",
		Resource: "services", Name: "provider-infrastructure",
	}
	if rec.sarAttributes[0] != want {
		t.Errorf("SAR attributes = %+v, want %+v", rec.sarAttributes[0], want)
	}
}

// The tenant's RBAC is the whole gate. A delegated token whose account has no
// proxy grant on the named edge is refused, not waved through because the
// credential authenticated.
func TestAuthorizeRefusesADelegatedTokenWithoutTheProxyGrant(t *testing.T) {
	const cluster = "260dym853j73uupr"
	rec := &authRecorder{
		username:      "system:serviceaccount:default:faros-du-9f2c1a7b5e4d3c2b1a09",
		authenticated: true,
		allowed:       false,
	}
	cfg := rec.start(t)

	err := authorize(context.Background(), cfg, cfg, delegatedSAToken(t, cluster, "faros-du-9f2c1a7b5e4d3c2b1a09"), cluster,
		"proxy", "edges.faros.sh", "linuxservers", "prod-eu")
	if err == nil {
		t.Fatal("authorize() = nil, want a denial when the SAR says no")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("err = %v, want an access-denied error", err)
	}
}

func TestAuthorizeRefusesAnUnauthenticatedToken(t *testing.T) {
	rec := &authRecorder{authenticated: false, allowed: true}
	cfg := rec.start(t)

	err := authorize(context.Background(), cfg, cfg, delegatedSAToken(t, "260dym853j73uupr", "faros-du-dead"), "260dym853j73uupr",
		"proxy", "edges.faros.sh", "services", "svc")
	if err == nil {
		t.Fatal("authorize() = nil, want a refusal for an unauthenticated token")
	}
	if len(rec.sarPaths) != 0 {
		t.Error("a SAR was issued for a token that did not authenticate")
	}
}

// parseServiceAccountToken decides which workspace a token is reviewed in, so
// the shapes it recognizes are load-bearing. A bound token — the shape
// TokenRequest mints, and the shape every delegated token has — carries its
// cluster in a nested claim under a different issuer, so it is deliberately NOT
// treated as a foreign SA: it authenticates in the workspace being addressed,
// which is where it was minted.
func TestParseServiceAccountTokenShapes(t *testing.T) {
	const cluster = "260dym853j73uupr"

	t.Run("legacy secret-backed token is recognized", func(t *testing.T) {
		claims, ok := parseServiceAccountToken(legacySAToken(t, cluster, "provider-edges"))
		if !ok {
			t.Fatal("legacy SA token not recognized")
		}
		if claims.ClusterName != cluster {
			t.Errorf("ClusterName = %q, want %q", claims.ClusterName, cluster)
		}
	})

	t.Run("bound delegated token is not a foreign SA", func(t *testing.T) {
		if _, ok := parseServiceAccountToken(delegatedSAToken(t, cluster, "faros-du-abc")); ok {
			t.Error("a bound delegated token was classed as a foreign SA; it would be reviewed in the wrong workspace")
		}
	})

	t.Run("non-JWT credentials are not SA tokens", func(t *testing.T) {
		for _, token := range []string{"", "opaque-static-token", "a.b", "not.base64!.sig"} {
			if _, ok := parseServiceAccountToken(token); ok {
				t.Errorf("parseServiceAccountToken(%q) reported an SA token", token)
			}
		}
	})
}

// A legacy SA token minted somewhere else — the provider's own credential, say
// — is reviewed in its home workspace and re-qualified before the SAR, so a
// tenant's binding for their OWN "default" SA cannot be matched by a stranger's.
func TestAuthorizeRequalifiesAForeignServiceAccount(t *testing.T) {
	const consumer = "260dym853j73uupr"
	const home = "1a2b3c4d5e6f7g8h"

	rec := &authRecorder{
		username:      "system:serviceaccount:default:provider-edges",
		groups:        []string{"system:serviceaccounts"},
		authenticated: true,
		allowed:       true,
	}
	cfg := rec.start(t)

	// Both configs point at the recorder so the foreign branch (which re-roots
	// kcpConfig at the SA's home cluster) still reaches it.
	if err := authorize(context.Background(), cfg, cfg, legacySAToken(t, home, "provider-edges"), consumer,
		"proxy", "edges.faros.sh", "services", "svc"); err != nil {
		t.Fatalf("authorize() = %v, want nil", err)
	}

	if got := rec.tokenReviewPaths[0]; !strings.Contains(got, "/clusters/"+home+"/") {
		t.Errorf("TokenReview path = %q, want the SA's home cluster %q", got, home)
	}
	if got := rec.sarUsers[0]; got != "system:kcp:serviceaccount:"+home+":default:provider-edges" {
		t.Errorf("SAR user = %q, want the cluster-qualified name", got)
	}
	if len(rec.sarGroups[0]) != 0 {
		t.Errorf("SAR groups = %v, want none — a foreign SA must not match the tenant's own group bindings", rec.sarGroups[0])
	}
}

// A foreign-looking token whose home workspace resolves it to a human is
// refused rather than authorized under an identity that cannot be encoded
// unambiguously.
func TestAuthorizeRefusesAForeignTokenThatResolvesToANonServiceAccount(t *testing.T) {
	rec := &authRecorder{username: "alice@example.com", authenticated: true, allowed: true}
	cfg := rec.start(t)

	err := authorize(context.Background(), cfg, cfg, legacySAToken(t, "1a2b3c4d5e6f7g8h", "x"), "260dym853j73uupr",
		"proxy", "edges.faros.sh", "services", "svc")
	if err == nil {
		t.Fatal("authorize() = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "expected ServiceAccount identity") {
		t.Errorf("err = %v, want the identity-shape refusal", err)
	}
	if len(rec.sarPaths) != 0 {
		t.Error("a SAR was issued for an identity that could not be qualified")
	}
}
