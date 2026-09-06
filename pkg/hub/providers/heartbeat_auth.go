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
	"strings"
	"sync"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/faroshq/faros/pkg/apiurl"
)

// HeartbeatAuthMode selects what the heartbeat handler does when a beat fails
// authentication.
type HeartbeatAuthMode string

const (
	// HeartbeatAuthWarn records the beat anyway and logs the failure with the
	// provider name and reason. It is the default for this release so that
	// providers deployed from charts that predate the authenticated heartbeat
	// keep working while operators roll them forward; the next release flips
	// the default to HeartbeatAuthEnforce.
	HeartbeatAuthWarn HeartbeatAuthMode = "warn"
	// HeartbeatAuthEnforce rejects unauthenticated beats. A provider that
	// cannot prove it holds its own service-account token goes stale after
	// HeartbeatTTL, which is the correct outcome.
	HeartbeatAuthEnforce HeartbeatAuthMode = "enforce"
)

// ParseHeartbeatAuthMode validates a --provider-heartbeat-auth flag value.
func ParseHeartbeatAuthMode(s string) (HeartbeatAuthMode, error) {
	switch m := HeartbeatAuthMode(strings.ToLower(strings.TrimSpace(s))); m {
	case HeartbeatAuthWarn, HeartbeatAuthEnforce:
		return m, nil
	default:
		return "", fmt.Errorf("invalid provider heartbeat auth mode %q (want %q or %q)", s, HeartbeatAuthWarn, HeartbeatAuthEnforce)
	}
}

// HeartbeatAuthenticator decides whether r is allowed to heartbeat on behalf
// of providerName. A nil error accepts the beat. Errors wrap one of the
// ErrHeartbeat* sentinels so the handler can pick the status code; anything
// else is treated as "could not verify" (503), which the provider retries.
type HeartbeatAuthenticator func(ctx context.Context, r *http.Request, providerName string) error

var (
	// ErrHeartbeatNoBearer means the request carried no bearer token (401).
	ErrHeartbeatNoBearer = errors.New("heartbeat has no bearer token")
	// ErrHeartbeatTokenRejected means the workspace did not authenticate the
	// token at all (401).
	ErrHeartbeatTokenRejected = errors.New("heartbeat bearer token was not authenticated")
	// ErrHeartbeatWrongIdentity means the token authenticated as something
	// other than the provider's own service account (403).
	ErrHeartbeatWrongIdentity = errors.New("heartbeat bearer token is not the provider's service account")
	// ErrHeartbeatAuthUnavailable means the hub has no way to verify the
	// token right now: no kcp, or the provider's workspace is not known yet
	// (503).
	ErrHeartbeatAuthUnavailable = errors.New("heartbeat authentication unavailable")
)

// heartbeatAuthCacheTTL bounds how long a verified (token, provider) pair is
// trusted without a fresh TokenReview. It is deliberately shorter than
// HeartbeatTTL so a revoked token cannot outlive the liveness window it would
// otherwise keep extending, while still collapsing a 30s beat cadence into a
// TokenReview every other beat.
const heartbeatAuthCacheTTL = HeartbeatTTL / 2

// ProviderSAUsername is the username kcp reports for the provider's own
// service account: the one MintProviderKubeconfigAtPath issues the provider
// kubeconfig for. Every provider's heartbeat must authenticate as exactly it.
const ProviderSAUsername = "system:serviceaccount:" + ProviderSANamespace + ":" + ProviderSAName

// tokenReviewAuthenticator verifies heartbeat bearers with a TokenReview in
// the provider's own workspace.
//
// The provider kubeconfig token is a legacy kubernetes.io/service-account-token
// Secret token (see ensureLegacySAToken). Legacy tokens carry no audience
// claim, and kcp validates them for the API server's implicit audience, so the
// review is issued without a requested audience: asking for a specific one
// (such as the workload-identity audience) would reject the very token the hub
// minted for the provider.
//
// Running the review inside the provider workspace is what scopes the check:
// kcp binds service-account tokens to the logical cluster they were issued in,
// so a "provider" SA token from any other workspace does not authenticate here
// at all, let alone as ProviderSAUsername.
type tokenReviewAuthenticator struct {
	clusters  ClusterResolver
	newClient func(cluster string) (kubernetes.Interface, error)
	now       func() time.Time
	cacheTTL  time.Duration

	mu    sync.Mutex
	cache map[heartbeatAuthCacheKey]time.Time
}

// heartbeatAuthCacheKey is the sha256 of the token plus the provider it was
// verified for, so a token verified for one provider never vouches for
// another.
type heartbeatAuthCacheKey struct {
	tokenHash [sha256.Size]byte
	provider  string
}

// NewTokenReviewHeartbeatAuthenticator returns a HeartbeatAuthenticator that
// accepts a beat for provider N only when its bearer token authenticates, via
// TokenReview in N's own workspace, as that workspace's provider service
// account. Successful verifications are cached for heartbeatAuthCacheTTL.
func NewTokenReviewHeartbeatAuthenticator(kcpConfig *rest.Config, clusters ClusterResolver) (HeartbeatAuthenticator, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("kcp config is required")
	}
	if clusters == nil {
		return nil, fmt.Errorf("cluster resolver is required")
	}
	a := &tokenReviewAuthenticator{
		clusters: clusters,
		newClient: func(cluster string) (kubernetes.Interface, error) {
			cfg := rest.CopyConfig(kcpConfig)
			cfg.Host = apiurl.KCPClusterURL(cfg.Host, cluster)
			return kubernetes.NewForConfig(cfg)
		},
		now:      time.Now,
		cacheTTL: heartbeatAuthCacheTTL,
		cache:    map[heartbeatAuthCacheKey]time.Time{},
	}
	return a.Authenticate, nil
}

// Authenticate implements HeartbeatAuthenticator.
func (a *tokenReviewAuthenticator) Authenticate(ctx context.Context, r *http.Request, providerName string) error {
	token, ok := bearerToken(r)
	if !ok {
		return ErrHeartbeatNoBearer
	}
	key := heartbeatAuthCacheKey{tokenHash: sha256.Sum256([]byte(token)), provider: providerName}
	now := a.now()
	if a.cached(key, now) {
		return nil
	}

	cluster, ok := a.clusters.CatalogEntryCluster(providerName)
	if !ok || cluster == "" {
		return fmt.Errorf("%w: no CatalogEntry cluster known for provider %q yet", ErrHeartbeatAuthUnavailable, providerName)
	}
	client, err := a.newClient(cluster)
	if err != nil {
		return fmt.Errorf("%w: creating client for cluster %s: %v", ErrHeartbeatAuthUnavailable, cluster, err)
	}
	review, err := client.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{Token: token},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("%w: reviewing token in cluster %s: %v", ErrHeartbeatAuthUnavailable, cluster, err)
	}
	if !review.Status.Authenticated {
		return ErrHeartbeatTokenRejected
	}
	if review.Status.User.Username != ProviderSAUsername {
		return fmt.Errorf("%w: authenticated as %q, want %q", ErrHeartbeatWrongIdentity, review.Status.User.Username, ProviderSAUsername)
	}

	a.mu.Lock()
	a.cache[key] = now.Add(a.cacheTTL)
	a.mu.Unlock()
	return nil
}

// cached reports whether key was verified recently enough to trust, dropping
// expired entries as it goes so the map stays bounded by live tokens.
func (a *tokenReviewAuthenticator) cached(key heartbeatAuthCacheKey, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, until := range a.cache {
		if !now.Before(until) {
			delete(a.cache, k)
		}
	}
	until, ok := a.cache[key]
	return ok && now.Before(until)
}

// bearerToken extracts the token from an Authorization: Bearer header.
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}

// heartbeatAuthFailureBody is the whole body the handler returns for a beat it
// refused in enforce mode: one short string per status class, and nothing that
// varies with the hub's state.
//
// The authenticator's errors are written for the log, not for the wire — they
// name the logical cluster the TokenReview was issued in, the service account
// username the beat was expected to authenticate as, and whether a CatalogEntry
// cluster is known for the provider at all. This endpoint is mounted with no
// auth middleware in front of it, so returning any of that would hand an
// anonymous caller a map of the deployment. The detail stays in the handler's
// log lines, which is where an operator debugs a provider that cannot beat.
func heartbeatAuthFailureBody(status int) string {
	switch status {
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusUnauthorized:
		return "heartbeat not authenticated"
	default:
		return "heartbeat verification unavailable"
	}
}

// heartbeatAuthStatus maps an authenticator error to the HTTP status the
// handler returns in enforce mode.
func heartbeatAuthStatus(err error) int {
	switch {
	case errors.Is(err, ErrHeartbeatNoBearer), errors.Is(err, ErrHeartbeatTokenRejected):
		return http.StatusUnauthorized
	case errors.Is(err, ErrHeartbeatWrongIdentity):
		return http.StatusForbidden
	default:
		return http.StatusServiceUnavailable
	}
}
