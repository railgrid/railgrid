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

package api

// The actor — who owns a thread, an attachment, an approval decision, an audit
// entry — is derived from the caller's own bearer token, never from a header.
//
// X-Railgrid-User used to be that actor. It is injected by the hub's backend
// proxy, so it is trustworthy on the hub's own path, but it is a header: it
// travels beside the credential rather than being derived from it, and nothing
// in this process could tell a forwarded one from a fabricated one. Ownership
// checks (attachment.ActorID != id.user) and audit records therefore rested on
// a value App Studio never verified.
//
// A SelfSubjectReview asks the API server the one question that has a
// verifiable answer: "who am I, with this token?". kcp answers it on the
// workspace cluster the request names, as the caller, so the reply is the same
// identity every subsequent tenant write is authorized as. The header remains
// as a display label only.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/railgrid/provider-sdk/tenantaccess"
)

// actorLookup resolves the authenticated username behind token on clusterID.
// Production wires ActorResolver.Resolve over the hub; tests substitute a
// table, exactly as workspaceLookup does.
type actorLookup func(ctx context.Context, clusterID, token string) (string, error)

// DefaultActorResolverTTL bounds how long a token's resolved identity is
// reused. Short on purpose: it exists to collapse the many calls one page load
// makes into a single review, not to cache an identity across a session. A
// revoked or rotated token stops being an actor within this long.
const DefaultActorResolverTTL = 60 * time.Second

// errNoActorLookup is the actorErr when there is nothing to ask: no hub URL
// configured, or the request carries no cluster ID / token.
var errNoActorLookup = errors.New("actor lookup unavailable (no hub URL configured or no caller credentials on the request)")

// ActorResolver memoizes SelfSubjectReview per (cluster, token).
//
// The cache key is a hash of the token, never the token itself: the map is
// read by every request and lives for the process lifetime, and a bearer in a
// long-lived data structure is a credential waiting to be logged. Hashing the
// cluster in with it keeps one token's identity from leaking across
// workspaces, which matters because the review is answered by whichever kcp
// shard serves that cluster.
type ActorResolver struct {
	hubBase  string
	insecure bool
	ttl      time.Duration
	// review is the seam unit tests replace; production leaves it nil and
	// posts a real SelfSubjectReview.
	review func(ctx context.Context, clusterID, token string) (string, error)

	mu  sync.Mutex
	hot map[string]actorEntry
}

type actorEntry struct {
	user      string
	expiresAt time.Time
}

// NewActorResolver returns a resolver against the hub at hubBase. ttl <= 0
// selects DefaultActorResolverTTL.
func NewActorResolver(hubBase string, insecure bool, ttl time.Duration) *ActorResolver {
	if ttl <= 0 {
		ttl = DefaultActorResolverTTL
	}
	return &ActorResolver{
		hubBase:  strings.TrimRight(hubBase, "/"),
		insecure: insecure,
		ttl:      ttl,
		hot:      map[string]actorEntry{},
	}
}

func actorCacheKey(clusterID, token string) string {
	sum := sha256.Sum256([]byte(clusterID + "\x00" + token))
	return hex.EncodeToString(sum[:])
}

// Resolve returns the username kcp authenticates token as on clusterID.
//
// Only successful reviews are cached, so a transient hub failure self-heals on
// the next request instead of pinning a caller as anonymous for a whole TTL.
func (r *ActorResolver) Resolve(ctx context.Context, clusterID, token string) (string, error) {
	clusterID, token = strings.TrimSpace(clusterID), strings.TrimSpace(token)
	if clusterID == "" || token == "" {
		return "", errNoActorLookup
	}
	key := actorCacheKey(clusterID, token)
	now := time.Now()
	r.mu.Lock()
	entry, ok := r.hot[key]
	r.mu.Unlock()
	if ok && now.Before(entry.expiresAt) {
		return entry.user, nil
	}

	review := r.review
	if review == nil {
		review = r.selfSubjectReview
	}
	user, err := review(ctx, clusterID, token)
	if err != nil {
		return "", err
	}
	user = strings.TrimSpace(user)
	if user == "" {
		return "", fmt.Errorf("SelfSubjectReview on cluster %q returned no username", clusterID)
	}

	r.mu.Lock()
	r.hot[key] = actorEntry{user: user, expiresAt: now.Add(r.ttl)}
	r.mu.Unlock()
	return user, nil
}

// selfSubjectReview posts an authentication.k8s.io/v1 SelfSubjectReview to
// {hubBase}/clusters/{clusterID} with the caller's bearer. The hub's kcp proxy
// authorizes the caller against their workspace membership and forwards to
// kcp as that identity, so the returned UserInfo is exactly the subject every
// other request on this identity acts as.
func (r *ActorResolver) selfSubjectReview(ctx context.Context, clusterID, token string) (string, error) {
	cfg, err := tenantaccess.RESTConfig(r.hubBase, clusterID, token, r.insecure)
	if err != nil {
		return "", err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("building authentication client for cluster %q: %w", clusterID, err)
	}
	out, err := client.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("SelfSubjectReview on cluster %q: %w", clusterID, err)
	}
	return strings.TrimSpace(out.Status.UserInfo.Username), nil
}

// actorLookupFor wires the production resolver, or nil when there is no hub to
// ask. A nil lookup leaves every request's actor unresolved, which the project
// endpoints refuse — the same failure mode as an unresolvable workspace.
func actorLookupFor(hubBase string, insecure bool) actorLookup {
	if strings.TrimSpace(hubBase) == "" {
		return nil
	}
	return NewActorResolver(hubBase, insecure, 0).Resolve
}
