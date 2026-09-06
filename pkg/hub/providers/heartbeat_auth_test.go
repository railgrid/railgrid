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

package providers

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func heartbeatRequestWithBearer(name, token string) *http.Request {
	req := heartbeatRequestFor(name)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// The endpoint has no auth middleware in front of it, so a beat with no bearer
// must be turned away before it touches the registry: otherwise anyone who can
// reach the hub keeps a dead provider Ready.
func TestHeartbeatEnforceRejectsMissingBearerAndLeavesRegistryUntouched(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true})

	persisted := false
	handler := NewHeartbeatHandler(reg, func(context.Context, string, string, time.Time) error {
		persisted = true
		return nil
	}, func(_ context.Context, r *http.Request, _ string) error {
		if _, ok := bearerToken(r); !ok {
			return ErrHeartbeatNoBearer
		}
		return nil
	}, HeartbeatAuthEnforce, logr.Discard())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestFor("code"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if persisted {
		t.Fatal("unauthenticated beat was persisted")
	}
	got, _ := reg.Get("code")
	if got.HeartbeatRequired || !got.LastHeartbeat.IsZero() {
		t.Fatalf("registry moved on an unauthenticated beat: %+v", got)
	}
}

func TestHeartbeatEnforceRejectsWrongIdentityWith403(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true})

	handler := NewHeartbeatHandler(reg, nil, func(context.Context, *http.Request, string) error {
		return fmt.Errorf("%w: authenticated as %q", ErrHeartbeatWrongIdentity, "system:serviceaccount:default:someone-else")
	}, HeartbeatAuthEnforce, logr.Discard())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestWithBearer("code", "tok"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got, _ := reg.Get("code"); got.HeartbeatRequired {
		t.Fatal("registry recorded a beat from the wrong identity")
	}
}

// A verification outage is not an identity failure: the provider should retry,
// not be told its token is bad.
func TestHeartbeatEnforceReports503WhenItCannotVerify(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true})

	handler := NewHeartbeatHandler(reg, nil, func(context.Context, *http.Request, string) error {
		return fmt.Errorf("%w: kcp down", ErrHeartbeatAuthUnavailable)
	}, HeartbeatAuthEnforce, logr.Discard())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestWithBearer("code", "tok"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	// With no authenticator at all (no kcp) enforce mode must fail closed the
	// same way rather than silently accepting everything.
	handler = NewHeartbeatHandler(reg, nil, nil, HeartbeatAuthEnforce, logr.Discard())
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestWithBearer("code", "tok"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil authenticator: status = %d, want 503", rec.Code)
	}
}

func TestHeartbeatEnforceRecordsTheProvidersOwnIdentity(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true})

	var seen []string
	handler := NewHeartbeatHandler(reg, nil, func(_ context.Context, r *http.Request, name string) error {
		token, _ := bearerToken(r)
		seen = append(seen, name+":"+token)
		return nil
	}, HeartbeatAuthEnforce, logr.Discard())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestWithBearer("code", "sa-token"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(seen) != 1 || seen[0] != "code:sa-token" {
		t.Fatalf("authenticator saw %v, want the provider name and bearer from the request", seen)
	}
	if got, _ := reg.Get("code"); !got.HeartbeatRequired || got.LastHeartbeat.IsZero() {
		t.Fatalf("authenticated beat not recorded: %+v", got)
	}
}

// Warn mode is the bridge for providers on charts that predate the
// authenticated heartbeat: the beat still counts, but the failure is logged at
// the default verbosity with the provider name so operators can find them.
func TestHeartbeatWarnRecordsAndLogsFailures(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true})

	var logged []string
	log := funcr.New(func(prefix, args string) { logged = append(logged, prefix+" "+args) }, funcr.Options{})

	handler := NewHeartbeatHandler(reg, nil, func(context.Context, *http.Request, string) error {
		return ErrHeartbeatNoBearer
	}, HeartbeatAuthWarn, log)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestFor("code"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 in warn mode", rec.Code)
	}
	if got, _ := reg.Get("code"); !got.HeartbeatRequired {
		t.Fatal("warn mode dropped the beat")
	}
	var found bool
	for _, line := range logged {
		if strings.Contains(line, `"provider"="code"`) && strings.Contains(line, ErrHeartbeatNoBearer.Error()) && strings.Contains(line, "warn") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warn-mode log line naming the provider and reason; got %q", logged)
	}
}

// The endpoint answers anonymous callers, so a rejection must not describe the
// deployment back to them: the authenticator's errors name logical cluster IDs
// and the service account username a beat was expected to carry.
func TestHeartbeatEnforceRejectionBodiesRevealNothing(t *testing.T) {
	const (
		cluster  = "1axwjxprfb96jgta"
		otherSA  = "system:serviceaccount:kube-system:sneaky"
		provider = "code"
	)
	cases := map[string]struct {
		err    error
		status int
		body   string
	}{
		"no bearer": {ErrHeartbeatNoBearer, http.StatusUnauthorized, "heartbeat not authenticated"},
		"token rejected": {
			fmt.Errorf("%w in cluster %s", ErrHeartbeatTokenRejected, cluster),
			http.StatusUnauthorized, "heartbeat not authenticated",
		},
		"wrong identity": {
			fmt.Errorf("%w: authenticated as %q, want %q", ErrHeartbeatWrongIdentity, otherSA, ProviderSAUsername),
			http.StatusForbidden, "forbidden",
		},
		"cannot verify": {
			fmt.Errorf("%w: reviewing token in cluster %s: %v", ErrHeartbeatAuthUnavailable, cluster, errors.New("connection refused")),
			http.StatusServiceUnavailable, "heartbeat verification unavailable",
		},
	}
	for name, tc := range cases {
		reg := NewRegistry()
		reg.Upsert(Provider{Name: provider, EndpointsValid: true, CatalogEntryCluster: cluster})
		handler := NewHeartbeatHandler(reg, nil, func(context.Context, *http.Request, string) error {
			return tc.err
		}, HeartbeatAuthEnforce, logr.Discard())

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, heartbeatRequestWithBearer(provider, "tok"))
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d", name, rec.Code, tc.status)
		}
		body := strings.TrimSpace(rec.Body.String())
		if body != tc.body {
			t.Errorf("%s: body = %q, want %q", name, body, tc.body)
		}
		for _, secret := range []string{cluster, otherSA, ProviderSAUsername, ProviderSAName, ProviderSANamespace, "connection refused"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s: body %q leaks %q", name, body, secret)
			}
		}
	}
}

// Enforce mode must not answer differently for a name that exists and a name
// that does not; otherwise the endpoint enumerates the platform's providers for
// anyone who can reach the hub.
func TestHeartbeatEnforceHidesUnknownProviders(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true, CatalogEntryCluster: "code-cluster"})
	handler := NewHeartbeatHandler(reg, nil, func(context.Context, *http.Request, string) error {
		return ErrHeartbeatTokenRejected
	}, HeartbeatAuthEnforce, logr.Discard())

	known := httptest.NewRecorder()
	handler.ServeHTTP(known, heartbeatRequestWithBearer("code", "garbage"))
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, heartbeatRequestWithBearer("no-such-provider", "garbage"))

	if known.Code != unknown.Code {
		t.Fatalf("status: known provider %d, unknown provider %d; they must match", known.Code, unknown.Code)
	}
	if known.Body.String() != unknown.Body.String() {
		t.Fatalf("body: known provider %q, unknown provider %q; they must match", known.Body, unknown.Body)
	}
	if unknown.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", unknown.Code)
	}
	if strings.Contains(unknown.Body.String(), "not found") {
		t.Fatalf("body %q admits the provider is unknown", unknown.Body)
	}
}

// A registered provider whose CatalogEntry has not been observed yet is a
// transient startup state, not an unknown name: it must keep producing the 503
// the provider retries rather than the permanent 401 an unknown name gets.
func TestHeartbeatEnforceStillReports503BeforeTheClusterIsKnown(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true}) // no CatalogEntryCluster yet
	f := &tokenReviewFake{}
	now := time.Now()
	a := newTestTokenReviewAuthenticator(reg, f, &now)
	handler := NewHeartbeatHandler(reg, nil, a.Authenticate, HeartbeatAuthEnforce, logr.Discard())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestWithBearer("code", "tok"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while the cluster is still unknown", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "heartbeat verification unavailable" || strings.Contains(body, "code") {
		t.Fatalf("body = %q, want the generic unavailable message", body)
	}

	// Once the entry is observed the same beat authenticates and is recorded.
	reg.Upsert(Provider{Name: "code", EndpointsValid: true, CatalogEntryCluster: "code-cluster"})
	f.status = authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: ProviderSAUsername}}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, heartbeatRequestWithBearer("code", "tok"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got, _ := reg.Get("code"); !got.HeartbeatRequired || got.LastHeartbeat.IsZero() {
		t.Fatalf("authenticated beat not recorded: %+v", got)
	}
}

// The detail the response no longer carries has to stay somewhere an operator
// can read it.
func TestHeartbeatEnforceLogsTheDetailedReason(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true})

	var logged []string
	log := funcr.New(func(prefix, args string) { logged = append(logged, prefix+" "+args) },
		funcr.Options{Verbosity: 1})
	handler := NewHeartbeatHandler(reg, nil, func(context.Context, *http.Request, string) error {
		return fmt.Errorf("%w: authenticated as %q, want %q", ErrHeartbeatWrongIdentity, "system:serviceaccount:kube-system:sneaky", ProviderSAUsername)
	}, HeartbeatAuthEnforce, log)

	handler.ServeHTTP(httptest.NewRecorder(), heartbeatRequestWithBearer("code", "tok"))
	handler.ServeHTTP(httptest.NewRecorder(), heartbeatRequestWithBearer("no-such-provider", "tok"))

	var identity, unknown bool
	for _, line := range logged {
		if strings.Contains(line, `"provider"="code"`) && strings.Contains(line, "system:serviceaccount:kube-system:sneaky") && strings.Contains(line, ProviderSAUsername) {
			identity = true
		}
		if strings.Contains(line, `"provider"="no-such-provider"`) && strings.Contains(line, "not registered") {
			unknown = true
		}
	}
	if !identity {
		t.Errorf("no log line carrying the wrong-identity detail; got %q", logged)
	}
	if !unknown {
		t.Errorf("no log line saying the provider is not registered; got %q", logged)
	}
}

// Warn mode logs a rejected identity per beat because that is the list of
// providers an operator has to fix before enforce becomes the default. It must
// not do the same for "cannot verify": that repeats for every provider on
// every tick and says nothing about any of them.
func TestHeartbeatWarnKeepsIdentityFailuresLoudAndVerificationOutagesQuiet(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantAtV0 bool
	}{
		{"wrong identity", ErrHeartbeatWrongIdentity, true},
		{"token rejected", ErrHeartbeatTokenRejected, true},
		{"no bearer", ErrHeartbeatNoBearer, true},
		{"cannot verify", fmt.Errorf("%w: no kcp configured", ErrHeartbeatAuthUnavailable), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry()
			reg.Upsert(Provider{Name: "code", EndpointsValid: true})

			var logged []string
			// Verbosity 0: only what an operator sees by default.
			log := funcr.New(func(prefix, args string) { logged = append(logged, prefix+" "+args) },
				funcr.Options{Verbosity: 0})
			handler := NewHeartbeatHandler(reg, nil, func(context.Context, *http.Request, string) error {
				return tc.err
			}, HeartbeatAuthWarn, log)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, heartbeatRequestWithBearer("code", "tok"))
			if rec.Code != http.StatusOK {
				t.Fatalf("warn mode must still accept the beat; got %d", rec.Code)
			}

			var sawAcceptedWarning bool
			for _, line := range logged {
				if strings.Contains(line, "accepting because --provider-heartbeat-auth=warn") {
					sawAcceptedWarning = true
				}
			}
			if sawAcceptedWarning != tc.wantAtV0 {
				t.Errorf("logged at default verbosity = %v, want %v; got %q", sawAcceptedWarning, tc.wantAtV0, logged)
			}
		})
	}
}

// The quiet case must still be readable when an operator turns verbosity up.
func TestHeartbeatWarnVerificationOutageLogsAtV1(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true})

	var logged []string
	log := funcr.New(func(prefix, args string) { logged = append(logged, prefix+" "+args) },
		funcr.Options{Verbosity: 1})
	handler := NewHeartbeatHandler(reg, nil, func(context.Context, *http.Request, string) error {
		return fmt.Errorf("%w: no kcp configured", ErrHeartbeatAuthUnavailable)
	}, HeartbeatAuthWarn, log)

	handler.ServeHTTP(httptest.NewRecorder(), heartbeatRequestWithBearer("code", "tok"))

	for _, line := range logged {
		if strings.Contains(line, "accepting because --provider-heartbeat-auth=warn") && strings.Contains(line, "no kcp configured") {
			return
		}
	}
	t.Errorf("no V(1) line carrying the verification-unavailable reason; got %q", logged)
}

func TestParseHeartbeatAuthMode(t *testing.T) {
	for in, want := range map[string]HeartbeatAuthMode{"warn": HeartbeatAuthWarn, " Enforce ": HeartbeatAuthEnforce} {
		got, err := ParseHeartbeatAuthMode(in)
		if err != nil || got != want {
			t.Fatalf("ParseHeartbeatAuthMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "off", "audit"} {
		if _, err := ParseHeartbeatAuthMode(in); err == nil {
			t.Fatalf("ParseHeartbeatAuthMode(%q) accepted", in)
		}
	}
}

// tokenReviewFake returns a fake clientset whose TokenReview answers with the
// given status, counting the reviews it served and recording which cluster it
// was asked to serve.
type tokenReviewFake struct {
	reviews  int
	clusters []string
	status   authnv1.TokenReviewStatus
}

func (f *tokenReviewFake) newClient(cluster string) (kubernetes.Interface, error) {
	f.clusters = append(f.clusters, cluster)
	cs := fake.NewClientset()
	cs.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		f.reviews++
		in := action.(k8stesting.CreateAction).GetObject().(*authnv1.TokenReview)
		if len(in.Spec.Audiences) != 0 {
			// The provider kubeconfig token is a legacy Secret token with no
			// audience claim; asking kcp for a specific audience would reject it.
			return true, nil, fmt.Errorf("unexpected audiences %v requested", in.Spec.Audiences)
		}
		out := in.DeepCopy()
		out.Status = f.status
		return true, out, nil
	})
	return cs, nil
}

func newTestTokenReviewAuthenticator(reg *Registry, f *tokenReviewFake, now *time.Time) *tokenReviewAuthenticator {
	return &tokenReviewAuthenticator{
		clusters:    reg,
		newClient:   f.newClient,
		now:         func() time.Time { return *now },
		cacheTTL:    heartbeatAuthCacheTTL,
		negativeTTL: heartbeatAuthNegativeCacheTTL,
		cache:       map[heartbeatAuthCacheKey]time.Time{},
		negative:    map[heartbeatAuthCacheKey]heartbeatAuthRejection{},
	}
}

func TestTokenReviewAuthenticatorRejectsAuthenticatedWrongUser(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true, CatalogEntryCluster: "code-cluster"})
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{
		Authenticated: true, User: authnv1.UserInfo{Username: "system:serviceaccount:default:not-the-provider"},
	}}
	now := time.Now()
	a := newTestTokenReviewAuthenticator(reg, f, &now)

	err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", "tok"), "code")
	if !errors.Is(err, ErrHeartbeatWrongIdentity) {
		t.Fatalf("err = %v, want ErrHeartbeatWrongIdentity", err)
	}
	if heartbeatAuthStatus(err) != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", heartbeatAuthStatus(err))
	}
	// A definitive rejection is remembered briefly: the same token is not
	// re-reviewed on the next beat, and it still fails the same way.
	if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", "tok"), "code"); !errors.Is(err, ErrHeartbeatWrongIdentity) {
		t.Fatalf("second err = %v, want ErrHeartbeatWrongIdentity", err)
	}
	if f.reviews != 1 {
		t.Fatalf("reviews = %d, want 1 (rejections are negatively cached)", f.reviews)
	}
}

// The endpoint is unauthenticated and a registered provider name is easy to
// guess, so every beat with a bad token used to cost kcp one TokenReview. A
// hammering caller must hit the negative cache instead, without that cache
// ever vouching for a token or outliving its short TTL.
func TestTokenReviewAuthenticatorNegativelyCachesRejections(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true, CatalogEntryCluster: "code-cluster"})
	reg.Upsert(Provider{Name: "edges", EndpointsValid: true, CatalogEntryCluster: "edges-cluster"})
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{Authenticated: false}}
	now := time.Now()
	a := newTestTokenReviewAuthenticator(reg, f, &now)

	for range 100 {
		if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", "garbage"), "code"); !errors.Is(err, ErrHeartbeatTokenRejected) {
			t.Fatalf("err = %v, want ErrHeartbeatTokenRejected", err)
		}
	}
	if f.reviews != 1 {
		t.Fatalf("reviews = %d, want 1 (100 beats with the same bad token collapse into one TokenReview)", f.reviews)
	}

	// Keyed by token and provider: the same garbage for another provider, or
	// different garbage for the same one, is its own review.
	if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("edges", "garbage"), "edges"); !errors.Is(err, ErrHeartbeatTokenRejected) {
		t.Fatalf("other provider: err = %v", err)
	}
	if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", "garbage2"), "code"); !errors.Is(err, ErrHeartbeatTokenRejected) {
		t.Fatalf("other token: err = %v", err)
	}
	if f.reviews != 3 {
		t.Fatalf("reviews = %d, want 3", f.reviews)
	}

	// The negative entry lapses quickly, so a token that becomes valid (a
	// provider re-minted right after a bad beat) is not locked out for long:
	// once kcp accepts it, the very next review says so.
	if heartbeatAuthNegativeCacheTTL > heartbeatAuthCacheTTL {
		t.Fatalf("negative cache TTL %v must not exceed the positive TTL %v", heartbeatAuthNegativeCacheTTL, heartbeatAuthCacheTTL)
	}
	now = now.Add(heartbeatAuthNegativeCacheTTL + time.Second)
	f.status = authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: ProviderSAUsername}}
	if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", "garbage"), "code"); err != nil {
		t.Fatalf("after the negative entry expired: %v", err)
	}
	if f.reviews != 4 {
		t.Fatalf("reviews = %d, want 4 (expired negative entry is re-reviewed)", f.reviews)
	}

	// A verification outage is not a rejection and is never cached: the
	// provider retries and the next beat asks kcp again.
	reg.Upsert(Provider{Name: "late", EndpointsValid: true}) // no cluster yet
	for range 2 {
		if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("late", "tok"), "late"); !errors.Is(err, ErrHeartbeatAuthUnavailable) {
			t.Fatalf("err = %v, want ErrHeartbeatAuthUnavailable", err)
		}
	}
	reg.Upsert(Provider{Name: "late", EndpointsValid: true, CatalogEntryCluster: "late-cluster"})
	if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("late", "tok"), "late"); err != nil {
		t.Fatalf("once the cluster is known: %v", err)
	}
}

// The negative cache is filled by whoever sends distinct bad tokens, so it has
// a hard cap: at capacity the oldest rejection (the one expiring first) makes
// room, the newest is still cached, and the map never exceeds the cap. A
// caller flooding it therefore only ever evicts its own garbage, and a token
// evicted early costs kcp one review again — the pre-cache cost, not more.
func TestTokenReviewAuthenticatorNegativeCacheIsBounded(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true, CatalogEntryCluster: "code-cluster"})
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{Authenticated: false}}
	now := time.Now()
	a := newTestTokenReviewAuthenticator(reg, f, &now)
	a.negativeMax = 8
	beat := func(token string) {
		t.Helper()
		if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", token), "code"); !errors.Is(err, ErrHeartbeatTokenRejected) {
			t.Fatalf("beat with %q: err = %v, want ErrHeartbeatTokenRejected", token, err)
		}
	}
	key := func(token string) heartbeatAuthCacheKey {
		return heartbeatAuthCacheKey{tokenHash: sha256.Sum256([]byte(token)), provider: "code"}
	}

	for i := range a.negativeMax {
		beat(fmt.Sprintf("garbage-%d", i))
	}
	if len(a.negative) != a.negativeMax {
		t.Fatalf("len(negative) = %d, want %d", len(a.negative), a.negativeMax)
	}

	// One past capacity: still cached (the repeat costs no review), the map
	// does not grow, and the oldest entry is what made room.
	beat("garbage-8")
	beat("garbage-8")
	if f.reviews != a.negativeMax+1 {
		t.Fatalf("reviews = %d, want %d (a rejection at capacity is still cached)", f.reviews, a.negativeMax+1)
	}
	if len(a.negative) > a.negativeMax {
		t.Fatalf("len(negative) = %d, exceeds the cap %d", len(a.negative), a.negativeMax)
	}
	if _, ok := a.negative[key("garbage-8")]; !ok {
		t.Fatal("the newest rejection is not in the cache")
	}
	if _, ok := a.negative[key("garbage-0")]; ok {
		t.Fatal("the oldest rejection is still in the cache; it should have been evicted")
	}
	if _, ok := a.negative[key("garbage-1")]; !ok {
		t.Fatal("only the oldest rejection should have been evicted")
	}
	beat("garbage-0")
	if f.reviews != a.negativeMax+2 {
		t.Fatalf("reviews = %d, want %d (an evicted token is reviewed once more)", f.reviews, a.negativeMax+2)
	}

	// A flood of distinct tokens never grows either the map or its order
	// queue past the cap.
	for i := range 1000 {
		beat(fmt.Sprintf("flood-%d", i))
	}
	if len(a.negative) > a.negativeMax || len(a.negativeOrder) > a.negativeMax {
		t.Fatalf("after a flood: len(negative) = %d, len(negativeOrder) = %d, cap %d", len(a.negative), len(a.negativeOrder), a.negativeMax)
	}
	if heartbeatAuthNegativeCacheMax <= 0 {
		t.Fatalf("heartbeatAuthNegativeCacheMax = %d, want a positive default cap", heartbeatAuthNegativeCacheMax)
	}
}

// Two beats with the same bad token can both miss the negative cache while
// the first TokenReview is in flight, and both then record the rejection. The
// second must not take a second order slot: the slice is the bound, and a
// flood of one token at high concurrency must not grow it by the in-flight
// count every TTL.
func TestTokenReviewAuthenticatorNegativeCacheRepeatRejectionKeepsOneSlot(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true, CatalogEntryCluster: "code-cluster"})
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{Authenticated: false}}
	now := time.Now()
	a := newTestTokenReviewAuthenticator(reg, f, &now)
	a.negativeMax = 8
	key := func(token string) heartbeatAuthCacheKey {
		return heartbeatAuthCacheKey{tokenHash: sha256.Sum256([]byte(token)), provider: "code"}
	}
	garbage := key("garbage")

	// A hundred beats that all saw no cached rejection and all came back
	// from kcp with the same answer.
	for range 100 {
		if err := a.reject(garbage, now, ErrHeartbeatTokenRejected); !errors.Is(err, ErrHeartbeatTokenRejected) {
			t.Fatalf("reject: err = %v, want ErrHeartbeatTokenRejected", err)
		}
	}
	if len(a.negative) != 1 || len(a.negativeOrder) != 1 {
		t.Fatalf("after 100 in-flight rejections of one token: len(negative) = %d, len(negativeOrder) = %d, want 1 each", len(a.negative), len(a.negativeOrder))
	}
	first := a.negative[garbage].until

	// A repeat does not extend how long the token is remembered.
	_ = a.reject(garbage, now.Add(a.negativeTTL/2), ErrHeartbeatTokenRejected)
	if got := a.negative[garbage].until; !got.Equal(first) {
		t.Fatalf("a repeat rejection moved the expiry from %v to %v", first, got)
	}
	if len(a.negativeOrder) != 1 {
		t.Fatalf("a repeat rejection took a slot: len(negativeOrder) = %d, want 1", len(a.negativeOrder))
	}

	// Once the entry lapses the token is recorded anew, again in one slot.
	lapsed := first.Add(time.Nanosecond)
	_ = a.reject(garbage, lapsed, ErrHeartbeatTokenRejected)
	if len(a.negative) != 1 || len(a.negativeOrder) != 1 {
		t.Fatalf("after the entry lapsed: len(negative) = %d, len(negativeOrder) = %d, want 1 each", len(a.negative), len(a.negativeOrder))
	}
	if got := a.negative[garbage].until; !got.After(first) {
		t.Fatalf("a rejection after the entry lapsed kept the old expiry %v", got)
	}

	// Interleaved with a flood of distinct tokens, the repeats still cost no
	// slots and the queue never exceeds the cap.
	for i := range 1000 {
		_ = a.reject(key(fmt.Sprintf("flood-%d", i)), lapsed, ErrHeartbeatTokenRejected)
		_ = a.reject(garbage, lapsed, ErrHeartbeatTokenRejected)
	}
	if len(a.negative) > a.negativeMax || len(a.negativeOrder) > a.negativeMax {
		t.Fatalf("after a flood with repeats: len(negative) = %d, len(negativeOrder) = %d, cap %d", len(a.negative), len(a.negativeOrder), a.negativeMax)
	}
}

// A lookup must not pay for the cache's size: after a flood of distinct
// rejections, a beat must be a map lookup, not a sweep of the flood. Expired
// entries are dropped when a rejection is recorded, off the front of the
// order queue, never by scanning the map on a lookup.
func TestTokenReviewAuthenticatorNegativeCacheLookupDoesNotSweep(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true, CatalogEntryCluster: "code-cluster"})
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{Authenticated: false}}
	now := time.Now()
	a := newTestTokenReviewAuthenticator(reg, f, &now)
	// One clientset for the whole test: building one per beat is what would
	// dominate ten thousand beats, not the cache.
	cs, err := f.newClient("code-cluster")
	if err != nil {
		t.Fatal(err)
	}
	a.newClient = func(string) (kubernetes.Interface, error) { return cs, nil }
	beat := func(token string) {
		t.Helper()
		if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", token), "code"); !errors.Is(err, ErrHeartbeatTokenRejected) {
			t.Fatalf("beat with %q: err = %v, want ErrHeartbeatTokenRejected", token, err)
		}
	}

	const flood = 10_000
	a.negativeMax = flood
	for i := range flood {
		beat(fmt.Sprintf("flood-%d", i))
	}
	if f.reviews != flood || len(a.negative) != flood {
		t.Fatalf("reviews = %d, len(negative) = %d, want %d each", f.reviews, len(a.negative), flood)
	}

	visited := a.negativeVisited
	for i := range flood {
		beat(fmt.Sprintf("flood-%d", i))
	}
	if f.reviews != flood {
		t.Fatalf("reviews = %d after repeating the flood, want %d (every repeat is a cache hit)", f.reviews, flood)
	}
	if a.negativeVisited != visited {
		t.Fatalf("%d cache hits examined %d queue entries, want 0: a lookup must not sweep the cache", flood, a.negativeVisited-visited)
	}

	// Once the flood has expired a repeat is a miss, still without a sweep;
	// the review it triggers records a rejection, and that is what prunes
	// the expired flood — every expired entry once, off the front.
	now = now.Add(heartbeatAuthNegativeCacheTTL + time.Second)
	beat("flood-0")
	if f.reviews != flood+1 {
		t.Fatalf("reviews = %d after the flood expired, want %d", f.reviews, flood+1)
	}
	if len(a.negative) != 1 || len(a.negativeOrder) != 1 {
		t.Fatalf("after pruning: len(negative) = %d, len(negativeOrder) = %d, want 1 each", len(a.negative), len(a.negativeOrder))
	}
	if examined := a.negativeVisited - visited; examined != flood {
		t.Fatalf("pruning examined %d queue entries, want %d (each expired entry exactly once)", examined, flood)
	}
}

func TestTokenReviewAuthenticatorRejectsUnauthenticatedToken(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true, CatalogEntryCluster: "code-cluster"})
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{Authenticated: false}}
	now := time.Now()
	a := newTestTokenReviewAuthenticator(reg, f, &now)

	err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", "garbage"), "code")
	if !errors.Is(err, ErrHeartbeatTokenRejected) || heartbeatAuthStatus(err) != http.StatusUnauthorized {
		t.Fatalf("err = %v (status %d), want ErrHeartbeatTokenRejected/401", err, heartbeatAuthStatus(err))
	}
	if err := a.Authenticate(context.Background(), heartbeatRequestFor("code"), "code"); !errors.Is(err, ErrHeartbeatNoBearer) {
		t.Fatalf("no bearer: err = %v, want ErrHeartbeatNoBearer", err)
	}
	if f.reviews != 1 {
		t.Fatalf("reviews = %d, want 1 (a missing bearer never reaches kcp)", f.reviews)
	}
}

func TestTokenReviewAuthenticatorAcceptsProviderSAAndCachesIt(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true, CatalogEntryCluster: "code-cluster"})
	f := &tokenReviewFake{status: authnv1.TokenReviewStatus{
		Authenticated: true, User: authnv1.UserInfo{Username: ProviderSAUsername},
	}}
	now := time.Now()
	a := newTestTokenReviewAuthenticator(reg, f, &now)

	for i := range 3 {
		if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", "tok"), "code"); err != nil {
			t.Fatalf("beat %d: %v", i, err)
		}
	}
	if f.reviews != 1 {
		t.Fatalf("reviews = %d, want 1 (cache hit for the repeated token)", f.reviews)
	}
	if len(f.clusters) != 1 || f.clusters[0] != "code-cluster" {
		t.Fatalf("reviewed in clusters %v, want only the provider's own", f.clusters)
	}

	// A different token for the same provider is its own review.
	if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", "other"), "code"); err != nil {
		t.Fatal(err)
	}
	if f.reviews != 2 {
		t.Fatalf("reviews = %d, want 2 (different token is not a cache hit)", f.reviews)
	}

	// The cache must lapse well before the liveness window does, so a
	// revoked token cannot keep a provider Ready indefinitely.
	if heartbeatAuthCacheTTL >= HeartbeatTTL {
		t.Fatalf("cache TTL %v must be shorter than heartbeat TTL %v", heartbeatAuthCacheTTL, HeartbeatTTL)
	}
	now = now.Add(heartbeatAuthCacheTTL + time.Second)
	if err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", "tok"), "code"); err != nil {
		t.Fatal(err)
	}
	if f.reviews != 3 {
		t.Fatalf("reviews = %d, want 3 (expired entry is re-reviewed)", f.reviews)
	}
}

func TestTokenReviewAuthenticatorNeedsTheProviderCluster(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code", EndpointsValid: true})
	f := &tokenReviewFake{}
	now := time.Now()
	a := newTestTokenReviewAuthenticator(reg, f, &now)

	err := a.Authenticate(context.Background(), heartbeatRequestWithBearer("code", "tok"), "code")
	if !errors.Is(err, ErrHeartbeatAuthUnavailable) || heartbeatAuthStatus(err) != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want ErrHeartbeatAuthUnavailable/503", err)
	}
	if f.reviews != 0 {
		t.Fatal("reviewed a token without knowing which cluster to ask")
	}
}

func TestNewTokenReviewHeartbeatAuthenticatorRequiresInputs(t *testing.T) {
	if _, err := NewTokenReviewHeartbeatAuthenticator(nil, NewRegistry()); err == nil {
		t.Fatal("nil kcp config accepted")
	}
}

func TestBearerToken(t *testing.T) {
	for header, want := range map[string]string{
		"Bearer abc":  "abc",
		"bearer abc ": "abc",
		"Basic abc":   "",
		"Bearer":      "",
		"Bearer  ":    "",
		"":            "",
	} {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		got, ok := bearerToken(req)
		if got != want || ok != (want != "") {
			t.Errorf("bearerToken(%q) = %q, %t; want %q", header, got, ok, want)
		}
	}
}
