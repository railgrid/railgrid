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

package appauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/faroshq/faros/pkg/browsersession"
)

const (
	// verifyFailureBurst / verifyFailureRefill bound failed credential checks
	// (mint and verify) per source address: a source may fail
	// verifyFailureBurst times back to back, then earns one more attempt per
	// verifyFailureRefill. While a source is over budget EVERY request from it
	// is refused, valid credentials included — otherwise the budget would not
	// bound a guessing attack. Successful checks never consume budget.
	verifyFailureBurst  = 20
	verifyFailureRefill = 3 * time.Second
	// maxVerifyFailureSources bounds the limiter's memory.
	maxVerifyFailureSources = 10000
	// maxVerifyTokenBytes rejects absurd credentials before any validator
	// (or JWKS fetch) runs.
	maxVerifyTokenBytes = 16 << 10
)

// MintRequest is the body of POST /auth/apps/token: the published instance
// to mint an app access token for, in the same coordinates the access gate
// uses (they appear in the query of the gate's sign-in redirect and in its
// 401 body). TTLSeconds is optional: 0 means the default (600), otherwise
// 60..900.
type MintRequest struct {
	Cluster    string `json:"cluster"`
	Group      string `json:"group"`
	Resource   string `json:"resource"`
	Name       string `json:"name"`
	TTLSeconds int64  `json:"ttlSeconds,omitempty"`
}

// MintResponse is the body of a successful mint. Token is sent to the app as
// `Authorization: Bearer <token>`; Host is the instance's published host.
type MintResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	Host      string    `json:"host"`
}

// verifyRequest is the gate's server-to-server verify payload: the instance
// coordinates the gate fronts. The app token travels in Authorization.
type verifyRequest struct {
	Cluster  string `json:"cluster"`
	Group    string `json:"group"`
	Resource string `json:"resource"`
	Name     string `json:"name"`
}

// VerifyResponse is returned to the gate on an allowed verify.
type VerifyResponse struct {
	Allowed           bool   `json:"allowed"`
	UserID            string `json:"userId"`
	SessionTTLSeconds int64  `json:"sessionTtlSeconds"`
}

// HandleMintToken exchanges the caller's hub bearer for an app access token.
//
//	POST /auth/apps/token
//	Authorization: Bearer <hub token>
//	{"cluster":..., "group":..., "resource":..., "name":..., "ttlSeconds":600}
//
// The caller is authenticated with the same validator that mints browser
// sessions from a bearer (Config.BearerIdentity) and must pass the same
// SubjectAccessReview as a browser sign-in. Answers 200 MintResponse, 400
// malformed, 401 not a hub user credential, 403 no access grant, 404 instance
// not published, 429 source over its failure budget, 503 dependency outage.
func (h *Handler) HandleMintToken(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	logger := klog.FromContext(r.Context())
	source := h.clientIP(r)
	if h.verifyFailures.blocked(source) {
		writeThrottled(w)
		return
	}
	var req MintRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		writeVerifyJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed token request"})
		return
	}
	ref := InstanceRef{Cluster: req.Cluster, Group: req.Group, Resource: req.Resource, Name: req.Name}
	ttl := defaultAppTokenTTL
	if req.TTLSeconds != 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	if ref.validate() != nil || ttl < minAppTokenTTL || ttl > maxAppTokenTTL {
		writeVerifyJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed token request"})
		return
	}

	identity, bearer, ok := h.authenticateHubBearer(w, r, source, ref)
	if !ok {
		return
	}
	allowed, err := h.authorize(r.Context(), identity, ref)
	if err != nil {
		logger.Error(err, "app token: access policy unavailable", "app", ref.Name)
		writeVerifyJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "access policy unavailable"})
		return
	}
	if !allowed {
		writeVerifyJSON(w, http.StatusForbidden, map[string]any{"error": "access denied"})
		return
	}
	// Only after the SAR, as in HandleAuthorize: the instance read costs a kcp
	// round trip and reveals whether the instance exists.
	host, err := h.instanceHost(r.Context(), ref)
	if err != nil {
		if errors.Is(err, ErrInstanceNotPublished) {
			writeVerifyJSON(w, http.StatusNotFound, map[string]any{"error": "instance has no published host"})
			return
		}
		logger.Error(err, "app token: instance host unavailable", "app", ref.Name)
		writeVerifyJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "instance host unavailable"})
		return
	}

	now := h.now()
	expiresAt := now.Add(ttl)
	// A derived credential never outlives the one it was minted from.
	if parentExp, ok := jwtExpiry(bearer); ok && parentExp.Before(expiresAt) {
		expiresAt = parentExp
	}
	secret, err := h.appTokenSecret(r.Context())
	if err != nil {
		logger.Error(err, "app token: signing key unavailable")
		writeVerifyJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "token service unavailable"})
		return
	}
	token, err := sealAppToken(secret, h.random, appTokenClaims{
		Cluster: ref.Cluster, Group: ref.Group, Resource: ref.Resource, Name: ref.Name,
		UserID: identity.UserID, RBACIdentity: identity.RBACIdentity,
		IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix(),
	})
	if err != nil {
		logger.Error(err, "app token: sealing failed")
		writeVerifyJSON(w, http.StatusInternalServerError, map[string]any{"error": "token service unavailable"})
		return
	}
	writeVerifyJSON(w, http.StatusOK, MintResponse{Token: token, ExpiresAt: time.Unix(expiresAt.Unix(), 0).UTC(), Host: host})
}

// authenticateHubBearer resolves the request's hub bearer to an identity.
// On failure it has written the response (401 charged to source's budget, or
// 503 for a hub-side identity outage) and returns ok=false.
func (h *Handler) authenticateHubBearer(w http.ResponseWriter, r *http.Request, source string, ref InstanceRef) (browsersession.Identity, string, bool) {
	logger := klog.FromContext(r.Context())
	token, ok := bearerFromRequest(r)
	if !ok {
		h.rejectBearer(w, source)
		return browsersession.Identity{}, "", false
	}
	// kcp ServiceAccount tokens (workload, delegated and MCPServer identities)
	// never hold a browser session, so the browser flow can never admit them;
	// neither may this one. BearerIdentity refuses them today because they
	// fail OIDC verification — this check keeps that true even if the
	// validator grows a ServiceAccount branch, and skips a JWKS round trip.
	if looksLikeServiceAccountToken(token) {
		logger.V(2).Info("app token: ServiceAccount token refused", "app", ref.Name, "tokenHash", tokenHashPrefix(token))
		h.rejectBearer(w, source)
		return browsersession.Identity{}, "", false
	}
	identity, err := h.bearerIdentity(r)
	if err != nil {
		if h.identityUnavailable != nil && errors.Is(err, h.identityUnavailable) {
			// The credential was accepted; the hub could not resolve the user.
			// Not the caller's fault and not a guess — do not charge the budget.
			logger.Error(err, "app token: identity unavailable", "app", ref.Name)
			writeVerifyJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "identity unavailable"})
			return browsersession.Identity{}, "", false
		}
		logger.V(2).Info("app token: bearer rejected", "app", ref.Name, "tokenHash", tokenHashPrefix(token), "err", err.Error())
		h.rejectBearer(w, source)
		return browsersession.Identity{}, "", false
	}
	return identity, token, true
}

// HandleVerify authorizes a non-browser request at a private app's gate.
//
//	POST /auth/apps/verify
//	Authorization: Bearer <app access token>
//	{"cluster":..., "group":..., "resource":..., "name":...}
//
// Only app access tokens minted by HandleMintToken are accepted — never a hub
// bearer, which a gate must not be trusted with. The token must be bound to
// exactly the instance the gate names and unexpired, and the
// SubjectAccessReview is re-run so a revoked grant stops working within the
// gate's cache window. Answers 200 VerifyResponse, 400 malformed, 401 not a
// valid token for this instance, 403 no access grant, 429 source over its
// failure budget, 503 dependency outage.
//
// Like HandleExchange this endpoint does not authenticate the calling gate —
// gates hold no credentials by design. It is safe to expose because the
// request is self-authenticating: nothing is disclosed unless the caller holds
// a valid app token, and then only the token's own user ID and its yes/no.
func (h *Handler) HandleVerify(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	logger := klog.FromContext(r.Context())
	source := h.clientIP(r)
	if h.verifyFailures.blocked(source) {
		writeThrottled(w)
		return
	}
	var req verifyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		writeVerifyJSON(w, http.StatusBadRequest, map[string]any{"allowed": false, "error": "malformed verify request"})
		return
	}
	ref := InstanceRef(req)
	if ref.validate() != nil {
		writeVerifyJSON(w, http.StatusBadRequest, map[string]any{"allowed": false, "error": "malformed verify request"})
		return
	}
	token, ok := bearerFromRequest(r)
	if !ok {
		h.rejectBearer(w, source)
		return
	}
	secret, err := h.appTokenSecret(r.Context())
	if err != nil {
		logger.Error(err, "app verify: signing key unavailable")
		writeVerifyJSON(w, http.StatusServiceUnavailable, map[string]any{"allowed": false, "error": "token service unavailable"})
		return
	}
	claims, err := openAppToken(secret, token)
	if err != nil {
		logger.V(2).Info("app verify: token rejected", "app", ref.Name, "tokenHash", tokenHashPrefix(token), "reason", err.Error())
		h.rejectBearer(w, source)
		return
	}
	if claims.ref().key() != ref.key() {
		logger.Info("app verify: token presented at a different instance's gate", "app", ref.Name, "tokenApp", claims.Name, "tokenID", claims.ID)
		h.rejectBearer(w, source)
		return
	}
	now := h.now()
	remaining := time.Unix(claims.ExpiresAt, 0).Sub(now)
	if remaining <= 0 {
		logger.V(2).Info("app verify: token expired", "app", ref.Name, "tokenID", claims.ID)
		h.rejectBearer(w, source)
		return
	}
	allowed, err := h.authorize(r.Context(), browsersession.Identity{UserID: claims.UserID, RBACIdentity: claims.RBACIdentity}, ref)
	if err != nil {
		logger.Error(err, "app verify: access policy unavailable", "app", ref.Name)
		writeVerifyJSON(w, http.StatusServiceUnavailable, map[string]any{"allowed": false, "error": "access policy unavailable"})
		return
	}
	if !allowed {
		writeVerifyJSON(w, http.StatusForbidden, map[string]any{"allowed": false})
		return
	}
	ttl := min(remaining, sessionTTL)
	if ttl < time.Second {
		// The gate reads 0 as "no TTL sent".
		ttl = time.Second
	}
	writeVerifyJSON(w, http.StatusOK, VerifyResponse{
		Allowed:           true,
		UserID:            claims.UserID,
		SessionTTLSeconds: int64(ttl / time.Second),
	})
}

// appTokenSecret returns the hub secret app tokens are derived from.
func (h *Handler) appTokenSecret(ctx context.Context) ([]byte, error) {
	secret, err := h.tokenKey(ctx)
	if err != nil {
		return nil, err
	}
	if len(secret) < minTokenKeyLen {
		return nil, errors.New("app token key is too short")
	}
	return secret, nil
}

// rejectBearer answers 401 and charges the source's failure budget.
func (h *Handler) rejectBearer(w http.ResponseWriter, source string) {
	h.verifyFailures.record(source)
	w.Header().Set("WWW-Authenticate", `Bearer realm="faros", error="invalid_token"`)
	writeVerifyJSON(w, http.StatusUnauthorized, map[string]any{"allowed": false, "error": "invalid bearer token"})
}

func writeThrottled(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(int(verifyFailureRefill/time.Second)))
	writeVerifyJSON(w, http.StatusTooManyRequests, map[string]any{"allowed": false, "error": "too many failed attempts"})
}

func writeVerifyJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// bearerFromRequest returns the request's single bearer credential. Several
// Authorization headers are ambiguous and refused.
func bearerFromRequest(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if token == "" || len(token) > maxVerifyTokenBytes || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

// jwtPayload decodes a JWT's claims WITHOUT verifying it. Callers only use it
// to classify a token or to read claims of a token already verified.
func jwtPayload(token string) (map[string]json.RawMessage, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

// looksLikeServiceAccountToken mirrors the hub proxy's classification of kcp
// ServiceAccount JWTs (bound tokens nest the logical cluster under
// "kubernetes.io"; legacy tokens use flat "kubernetes.io/serviceaccount/*"
// claims and the "kubernetes/serviceaccount" issuer). Dex id_tokens carry
// none of these.
func looksLikeServiceAccountToken(token string) bool {
	claims, ok := jwtPayload(token)
	if !ok {
		return false
	}
	if _, ok := claims["kubernetes.io"]; ok {
		return true
	}
	for key := range claims {
		if strings.HasPrefix(key, "kubernetes.io/serviceaccount/") {
			return true
		}
	}
	var iss string
	if raw, ok := claims["iss"]; ok && json.Unmarshal(raw, &iss) == nil && iss == "kubernetes/serviceaccount" {
		return true
	}
	return false
}

// jwtExpiry reads the exp claim of a token that has ALREADY been verified.
func jwtExpiry(token string) (time.Time, bool) {
	claims, ok := jwtPayload(token)
	if !ok {
		return time.Time{}, false
	}
	raw, ok := claims["exp"]
	if !ok {
		return time.Time{}, false
	}
	var exp json.Number
	if err := json.Unmarshal(raw, &exp); err != nil {
		return time.Time{}, false
	}
	seconds, err := exp.Float64()
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), 0), true
}

// tokenHashPrefix identifies a credential in logs without disclosing it.
func tokenHashPrefix(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:16]
}

// peerAddress is the default limiter key: the connection peer, without port.
func peerAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// failureLimiter is a per-source token bucket charged only by failures.
// Buckets that have fully refilled carry no information and are swept when
// the table is full; past that, the least recently charged source is dropped,
// so a flood of distinct sources cannot grow memory without bound.
type failureLimiter struct {
	burst      float64
	refill     time.Duration
	maxSources int
	now        func() time.Time

	mu      sync.Mutex
	buckets map[string]*failureBucket
}

type failureBucket struct {
	tokens float64
	// last anchors the continuous refill; lastFailure orders eviction.
	last        time.Time
	lastFailure time.Time
}

func newFailureLimiter(burst int, refill time.Duration, maxSources int, now func() time.Time) *failureLimiter {
	return &failureLimiter{
		burst:      float64(burst),
		refill:     refill,
		maxSources: maxSources,
		now:        now,
		buckets:    map[string]*failureBucket{},
	}
}

func (l *failureLimiter) refillLocked(b *failureBucket, now time.Time) {
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(l.burst, b.tokens+float64(elapsed)/float64(l.refill))
		b.last = now
	}
}

// blocked reports whether source has exhausted its failure budget.
func (l *failureLimiter) blocked(source string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[source]
	if !ok {
		return false
	}
	l.refillLocked(b, now)
	return b.tokens < 1
}

// record charges one failure to source.
func (l *failureLimiter) record(source string) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[source]
	if !ok {
		if len(l.buckets) >= l.maxSources {
			l.evictLocked(now)
		}
		b = &failureBucket{tokens: l.burst, last: now}
		l.buckets[source] = b
	}
	l.refillLocked(b, now)
	b.tokens = max(0, b.tokens-1)
	b.lastFailure = now
}

func (l *failureLimiter) evictLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for key, b := range l.buckets {
		l.refillLocked(b, now)
		if b.tokens >= l.burst {
			delete(l.buckets, key)
			continue
		}
		if oldestKey == "" || b.lastFailure.Before(oldest) {
			oldestKey, oldest = key, b.lastFailure
		}
	}
	if len(l.buckets) >= l.maxSources && oldestKey != "" {
		delete(l.buckets, oldestKey)
	}
}
