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

package accessproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Programmatic (non-browser) access to a private app.
//
// A CLI, CI job or agent first exchanges its hub bearer AT THE HUB for an app
// access token bound to this one instance (POST <hub>/auth/apps/token), then
// presents that token here. A gate may be operated by someone the user does
// not trust with a hub credential (an org's self-hosted infrastructure
// provider), so this proxy accepts ONLY app access tokens: anything without
// the app-token prefix is refused locally and never relayed.
//
// The proxy cannot validate an app token itself — it holds no keys and no
// delegated TokenReview/SubjectAccessReview permission — so it relays it once
// to the hub's POST /auth/apps/verify, which checks the seal, the instance
// binding and expiry, and re-runs the SubjectAccessReview. The verdict is
// cached in the bounded session store under the token's SHA-256 (never the
// token itself): allows for min(hub TTL, 15m), refusals for 30s. The token is
// stripped before the upstream like any Authorization header, and a
// successful verification never mints a session cookie.
//
// Hub outage semantics match the cookie flow: cached allows keep working,
// uncached tokens fail closed with 502.

const (
	// bearerAllowMaxTTL caps how long an allow verdict is reused. It is the
	// revocation window for programmatic access (the browser flow's session
	// TTL is the same 15 minutes).
	bearerAllowMaxTTL = 15 * time.Minute
	// bearerDenyTTL is how long a 401/403 verdict is replayed without asking
	// the hub again. Short, so a fresh grant takes effect quickly, but long
	// enough that a client retry loop cannot turn into hub load.
	bearerDenyTTL = 30 * time.Second
	// maxBearerTokenBytes rejects absurd credentials before any hub call.
	maxBearerTokenBytes = 16 << 10
	// bearerKeyPrefix namespaces bearer verdicts in the session map. Cookie
	// sessions are keyed by bare hex SHA-256, which never has this prefix.
	bearerKeyPrefix = "bearer:"
	// appTokenPrefix marks a hub-minted app access token. It must stay in
	// lockstep with pkg/hub/appauth.AppTokenPrefix.
	appTokenPrefix = "fapp_"
	// hubTokenPath is where callers mint app access tokens; it is advertised
	// in 401 bodies. Mirrors pkg/hub/appauth.TokenPath.
	hubTokenPath = "/auth/apps/token"
)

type verifyRequest struct {
	Cluster  string `json:"cluster"`
	Group    string `json:"group"`
	Resource string `json:"resource"`
	Name     string `json:"name"`
}

// bearerCredential extracts a Bearer credential. isBearer reports whether the
// request is a bearer request at all (scheme compared case-insensitively);
// token is empty when the credential is malformed or ambiguous, which the
// caller answers with 401 rather than a browser redirect. Other schemes are
// not bearer requests and keep the browser behaviour.
func bearerCredential(r *http.Request) (token string, isBearer bool) {
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		return "", false
	}
	bearerSeen := false
	for _, value := range values {
		scheme, _, _ := strings.Cut(strings.TrimSpace(value), " ")
		if strings.EqualFold(scheme, "Bearer") {
			bearerSeen = true
		}
	}
	if !bearerSeen {
		return "", false
	}
	if len(values) != 1 {
		// Several Authorization headers: refuse to pick one.
		return "", true
	}
	_, rest, _ := strings.Cut(strings.TrimSpace(values[0]), " ")
	token = strings.TrimSpace(rest)
	if token == "" || len(token) > maxBearerTokenBytes || strings.ContainsAny(token, " \t\r\n") {
		return "", true
	}
	return token, true
}

func bearerKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return bearerKeyPrefix + hex.EncodeToString(sum[:])
}

// authorizeBearer reports whether the request may be forwarded. When it
// returns false the response has been written.
func (p *Proxy) authorizeBearer(w http.ResponseWriter, r *http.Request, token string) bool {
	if token == "" || !strings.HasPrefix(token, appTokenPrefix) {
		// Most likely a raw hub token. It is never relayed anywhere, and the
		// 401 body tells the caller how to mint the right credential.
		p.writeBearerError(w, http.StatusUnauthorized, "")
		return false
	}
	key := bearerKey(token)
	if verdict, ok := p.cachedBearerVerdict(key); ok {
		if verdict.denyStatus == 0 {
			return true
		}
		p.writeBearerError(w, verdict.denyStatus, "")
		return false
	}
	if !p.bearerRelayAllowed {
		p.logf("bearer access refused: hub URL %s is plain http, which would carry the caller's token in cleartext (host=%s)", p.config.hubURL, p.config.host)
		p.writeBearerError(w, http.StatusBadGateway, "")
		return false
	}

	status, grant, retryAfter := p.verifyBearer(r.Context(), token)
	switch status {
	case http.StatusOK:
		ttl := time.Duration(grant.SessionTTLSeconds) * time.Second
		if ttl <= 0 || ttl > bearerAllowMaxTTL {
			ttl = bearerAllowMaxTTL
		}
		p.putBearerVerdict(key, appSession{userID: grant.UserID, bearer: true}, ttl)
		return true
	case http.StatusUnauthorized, http.StatusForbidden:
		p.putBearerVerdict(key, appSession{bearer: true, denyStatus: status}, bearerDenyTTL)
		p.writeBearerError(w, status, "")
		return false
	case http.StatusTooManyRequests:
		// The hub is throttling this gate's failed verifications. Not cached:
		// the verdict says nothing about this token.
		p.writeBearerError(w, status, retryAfter)
		return false
	default:
		p.writeBearerError(w, http.StatusBadGateway, "")
		return false
	}
}

// verifyBearer asks the hub to authorize token for this instance. It returns
// 200 with a grant, 401, 403, 429 (with the hub's Retry-After), or 502 for
// anything else — an unreachable hub, an unexpected status, a redirect, or a
// response that does not unambiguously grant access.
func (p *Proxy) verifyBearer(ctx context.Context, token string) (int, exchangeResponse, string) {
	body, err := json.Marshal(verifyRequest{
		Cluster:  p.config.Instance.Cluster,
		Group:    p.config.Instance.Group,
		Resource: p.config.Instance.Resource,
		Name:     p.config.Instance.Name,
	})
	if err != nil {
		return http.StatusBadGateway, exchangeResponse{}, ""
	}
	requestCtx, cancel := context.WithTimeout(ctx, p.config.hubTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, p.config.hubURL+hubVerifyPath, bytes.NewReader(body))
	if err != nil {
		return http.StatusBadGateway, exchangeResponse{}, ""
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := p.verifyClient.Do(request)
	if err != nil {
		p.logf("bearer verification failed: %s is unreachable from this gate (host=%s): %v", p.config.hubURL+hubVerifyPath, p.config.host, err)
		return http.StatusBadGateway, exchangeResponse{}, ""
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
		var grant exchangeResponse
		if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&grant); err != nil {
			p.logf("bearer verification returned an unreadable body (host=%s): %v", p.config.host, err)
			return http.StatusBadGateway, exchangeResponse{}, ""
		}
		if !grant.Allowed || strings.TrimSpace(grant.UserID) == "" {
			p.logf("bearer verification returned no grant: allowed=%t userID-empty=%t (host=%s)", grant.Allowed, strings.TrimSpace(grant.UserID) == "", p.config.host)
			return http.StatusBadGateway, exchangeResponse{}, ""
		}
		return http.StatusOK, grant, ""
	case http.StatusUnauthorized, http.StatusForbidden:
		return response.StatusCode, exchangeResponse{}, ""
	case http.StatusTooManyRequests:
		p.logf("bearer verification throttled by the hub (host=%s): too many failed verifications from this gate", p.config.host)
		return response.StatusCode, exchangeResponse{}, response.Header.Get("Retry-After")
	default:
		p.logf("bearer verification refused: hub answered %d (host=%s)", response.StatusCode, p.config.host)
		return http.StatusBadGateway, exchangeResponse{}, ""
	}
}

func (p *Proxy) cachedBearerVerdict(key string) (appSession, bool) {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepSessionsLocked(now)
	verdict, ok := p.sessions[key]
	if !ok {
		return appSession{}, false
	}
	if !verdict.bearer || !now.Before(verdict.expiresAt) {
		delete(p.sessions, key)
		return appSession{}, false
	}
	return verdict, true
}

func (p *Proxy) putBearerVerdict(key string, verdict appSession, ttl time.Duration) {
	now := p.now()
	verdict.expiresAt = now.Add(ttl)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepSessionsLocked(now)
	if _, exists := p.sessions[key]; !exists {
		for len(p.sessions) >= maxSessions {
			evictOldestSessionLocked(p.sessions)
		}
	}
	p.sessions[key] = verdict
}

// bearerErrorBody is the JSON answer to a refused bearer request. On 401 it
// carries what a client needs to mint a valid app access token: the hub
// token endpoint and this gate's instance coordinates (the same ones every
// anonymous request already sees in the sign-in redirect).
type bearerErrorBody struct {
	Error         string         `json:"error"`
	Message       string         `json:"message"`
	TokenEndpoint string         `json:"tokenEndpoint,omitempty"`
	Instance      *verifyRequest `json:"instance,omitempty"`
}

// writeBearerError answers a bearer request. It never redirects and never
// echoes the credential.
func (p *Proxy) writeBearerError(w http.ResponseWriter, status int, retryAfter string) {
	body := bearerErrorBody{}
	switch status {
	case http.StatusUnauthorized:
		body.Error = "invalid_token"
		body.Message = "This app accepts only app access tokens. Mint one for this app at the token endpoint with your platform token, then send it as a Bearer token."
		body.TokenEndpoint = p.config.hubPublicURL + hubTokenPath
		body.Instance = &verifyRequest{
			Cluster:  p.config.Instance.Cluster,
			Group:    p.config.Instance.Group,
			Resource: p.config.Instance.Resource,
			Name:     p.config.Instance.Name,
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="faros", error="invalid_token"`)
	case http.StatusForbidden:
		body.Error, body.Message = "access_denied", "This account does not have access to this application. Ask the app owner to share it with you."
	case http.StatusTooManyRequests:
		body.Error, body.Message = "rate_limited", "Too many failed verifications. Try again shortly."
		if retryAfter == "" || len(retryAfter) > 6 || strings.Trim(retryAfter, "0123456789") != "" {
			retryAfter = "30"
		}
		w.Header().Set("Retry-After", retryAfter)
	default:
		status = http.StatusBadGateway
		body.Error, body.Message = "unavailable", "App access verification is unavailable. Try again shortly."
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// hubURLProtectsTokens reports whether relaying a caller's bearer to hubURL
// keeps it off the network in cleartext: https, or http to loopback only.
func hubURLProtectsTokens(hubURL string) bool {
	u, err := url.Parse(hubURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	default:
		return false
	}
}
