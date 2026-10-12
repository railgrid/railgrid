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

package accessproxy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maxIdentityAssertionBytes  = 16 << 10
	maxIdentityJWKSBytes       = 64 << 10
	maxIdentityJWKSKeys        = 32
	maxIdentityTTL             = 15 * time.Minute
	identityJWKSCacheTTL       = 5 * time.Minute
	identityJWKSRefreshBackoff = time.Second
	identityClockSkew          = 30 * time.Second
	maxIdentityCodeBytes       = 4096
	identityPKCEVerifierMin    = 43
	identityPKCEVerifierMax    = 128
	identityReturnCookiePrefix = "__Host-railgrid-app-auth-return"
	// legacyIdentityReturnCookiePrefix is retained for upstream cookie
	// stripping only. Identity callbacks accept only identityReturnCookiePrefix.
	legacyIdentityReturnCookiePrefix = "railgrid-app-auth-return"
)

// IdentityHeaderName carries the hub-signed assertion from the gate to the
// app API. The gate strips caller-supplied X-Railgrid-* headers first and sets
// this only on requests under /api.
const IdentityHeaderName = "X-Railgrid-App-Identity"

type identityVerifier struct {
	issuer      string
	audience    string
	environment string
	instanceUID string
	hubURL      string
	client      *http.Client
	timeout     time.Duration
	now         func() time.Time

	mu                     sync.Mutex
	keys                   map[string]ed25519.PublicKey
	keysUntil              time.Time
	refreshFailureUntil    time.Time
	unknownKidRefreshUntil time.Time
}

type identityJWTHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}

type identityJWTClaims struct {
	Issuer      string          `json:"iss"`
	Audience    json.RawMessage `json:"aud"`
	Subject     string          `json:"sub"`
	IssuedAt    int64           `json:"iat"`
	ExpiresAt   int64           `json:"exp"`
	NotBefore   int64           `json:"nbf,omitempty"`
	AppID       string          `json:"railgrid_app_id"`
	Environment string          `json:"railgrid_environment"`
	InstanceUID string          `json:"railgrid_instance_uid"`
	Name        string          `json:"name,omitempty"`
	Email       string          `json:"email,omitempty"`
}

type identityJWKSet struct {
	Keys []identityJWK `json:"keys"`
}

type identityJWK struct {
	KeyType   string `json:"kty"`
	Curve     string `json:"crv"`
	Use       string `json:"use"`
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	X         string `json:"x"`
}

type identityExchangeRequest struct {
	Code         string `json:"code"`
	Origin       string `json:"origin"`
	Cluster      string `json:"cluster"`
	Group        string `json:"group"`
	Resource     string `json:"resource"`
	Name         string `json:"name"`
	AppID        string `json:"app_id"`
	CodeVerifier string `json:"code_verifier"`
}

type identityExchangeResponse struct {
	Assertion string `json:"assertion"`
	ExpiresIn int64  `json:"expiresIn"`
}

type identityProfile struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
}

func isIdentityEndpoint(path string) bool {
	switch path {
	case IdentityLoginPath, IdentityLogoutPath, IdentitySessionPath, IdentityCallbackPath:
		return true
	default:
		return false
	}
}

func identityEndpointMethod(path string) string {
	if path == IdentityLogoutPath {
		return http.MethodPost
	}
	return http.MethodGet
}

func (p *Proxy) serveIdentityEndpoint(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case IdentityLoginPath:
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		path := cleanIdentityReturnPath(r.URL.Query().Get("returnTo"))
		if path == "" {
			// Accept the early snake-case spelling during local development; new
			// SDKs and generated links use the documented camel-case query.
			path = cleanIdentityReturnPath(r.URL.Query().Get("return_to"))
		}
		if path == "" {
			path = "/"
		}
		p.redirectIdentityToAuthorize(w, r, path, requestUsesPartitionedCookies(r))
	case IdentityCallbackPath:
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		p.handleIdentityCallback(w, r)
	case IdentitySessionPath:
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		claims, _, ok := p.identitySessionFromRequest(r)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			if hasIdentitySessionCookie(r) {
				p.clearIdentitySessionCookie(w, requestUsesPartitionedCookies(r))
			}
			writeUnauthenticatedJSON(w)
			return
		}
		_ = json.NewEncoder(w).Encode(identityProfile{ID: claims.Subject, DisplayName: claims.Name})
	case IdentityLogoutPath:
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if !hasExactOrigin(r, p.config.authOrigin) || strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
			http.Error(w, "origin check failed", http.StatusForbidden)
			return
		}
		p.clearIdentitySessionCookie(w, requestUsesPartitionedCookies(r))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	}
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func writeUnauthenticatedJSON(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthenticated"}`))
}

func hasExactOrigin(r *http.Request, expected string) bool {
	if r == nil {
		return false
	}
	origins := r.Header.Values("Origin")
	return len(origins) == 1 && origins[0] == expected
}

func cleanIdentityReturnPath(raw string) string {
	path := cleanReturnPath(raw)
	if path == "" || pathMatches("/__railgrid/auth", path) {
		return ""
	}
	return path
}

func identityReturnCookieName(state string) string {
	digest := sha256Sum(state)
	return identityReturnCookiePrefix + "-" + digest
}

func validIdentityCodeVerifier(verifier string) bool {
	if len(verifier) < identityPKCEVerifierMin || len(verifier) > identityPKCEVerifierMax {
		return false
	}
	for i := 0; i < len(verifier); i++ {
		c := verifier[i]
		if ('A' > c || c > 'Z') && ('a' > c || c > 'z') && ('0' > c || c > '9') &&
			c != '-' && c != '.' && c != '_' && c != '~' {
			return false
		}
	}
	return true
}

func identityCodeChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func sha256Sum(value string) string {
	return sessionKey(value)[:32]
}

func (p *Proxy) redirectIdentityToAuthorize(w http.ResponseWriter, r *http.Request, path string, partitioned bool) {
	verifier, err := p.randomString(32)
	if err != nil {
		http.Error(w, "app identity state unavailable", http.StatusInternalServerError)
		return
	}
	state, value, err := p.newReturnStateWithVerifier(path, partitioned, verifier)
	if err != nil {
		http.Error(w, "app identity state unavailable", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:        identityReturnCookieName(state),
		Value:       value,
		Path:        "/",
		Secure:      true,
		HttpOnly:    true,
		SameSite:    appCookieSameSite(partitioned),
		Partitioned: partitioned,
		MaxAge:      int(stateTTL / time.Second),
	})
	callbackURL := p.config.authOrigin + IdentityCallbackPath
	query := url.Values{}
	query.Set("cluster", p.config.Instance.Cluster)
	query.Set("group", p.config.Instance.Group)
	query.Set("resource", p.config.Instance.Resource)
	query.Set("name", p.config.Instance.Name)
	query.Set("app_id", p.config.AuthAudience)
	query.Set("redirect_uri", callbackURL)
	query.Set("state", state)
	query.Set("code_challenge", identityCodeChallenge(verifier))
	query.Set("code_challenge_method", "S256")
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, p.config.hubPublicURL+identityAuthorizePath+"?"+query.Encode(), http.StatusFound)
}

func (p *Proxy) handleIdentityCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	outcome := p.consumeIdentityReturnState(w, r, state)
	if !outcome.OK {
		if outcome.Restart {
			p.redirectIdentityToAuthorize(w, r, outcome.RestartPath, outcome.Partitioned)
			return
		}
		http.Error(w, "invalid app identity state", http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" || len(code) > maxIdentityCodeBytes || strings.ContainsAny(code, "\r\n") {
		p.clearIdentityReturnCookie(w, outcome.CookieName, outcome.Partitioned)
		http.Error(w, "invalid app identity code", http.StatusBadRequest)
		return
	}
	body, err := json.Marshal(identityExchangeRequest{
		Code:         code,
		Origin:       p.config.authOrigin,
		Cluster:      p.config.Instance.Cluster,
		Group:        p.config.Instance.Group,
		Resource:     p.config.Instance.Resource,
		Name:         p.config.Instance.Name,
		AppID:        p.config.AuthAudience,
		CodeVerifier: outcome.CodeVerifier,
	})
	if err != nil {
		p.clearIdentityReturnCookie(w, outcome.CookieName, outcome.Partitioned)
		http.Error(w, "app identity exchange unavailable", http.StatusInternalServerError)
		return
	}
	requestCtx, cancel := context.WithTimeout(r.Context(), p.config.hubTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, p.config.hubURL+identityExchangePath, bytes.NewReader(body))
	if err != nil {
		p.clearIdentityReturnCookie(w, outcome.CookieName, outcome.Partitioned)
		http.Error(w, "app identity exchange unavailable", http.StatusBadGateway)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.verifyClient.Do(request)
	if err != nil {
		p.logf("app identity exchange failed: %s is unavailable (host=%s): %v", p.config.hubURL+identityExchangePath, p.config.host, err)
		p.clearIdentityReturnCookie(w, outcome.CookieName, outcome.Partitioned)
		http.Error(w, "app identity exchange unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusGone {
		p.clearIdentityReturnCookie(w, outcome.CookieName, outcome.Partitioned)
		p.redirectIdentityToAuthorize(w, r, outcome.Path, outcome.Partitioned)
		return
	}
	if response.StatusCode != http.StatusOK {
		p.logf("app identity exchange refused: hub answered %d (host=%s)", response.StatusCode, p.config.host)
		p.clearIdentityReturnCookie(w, outcome.CookieName, outcome.Partitioned)
		http.Error(w, "app identity exchange denied", http.StatusBadGateway)
		return
	}
	var exchange identityExchangeResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&exchange); err != nil || exchange.Assertion == "" || exchange.ExpiresIn <= 0 || exchange.ExpiresIn > int64(maxIdentityTTL/time.Second) {
		p.clearIdentityReturnCookie(w, outcome.CookieName, outcome.Partitioned)
		http.Error(w, "invalid app identity exchange", http.StatusBadGateway)
		return
	}
	claims, err := p.identityVerifier.verify(r.Context(), exchange.Assertion)
	if err != nil {
		p.logf("app identity assertion rejected (host=%s): %v", p.config.host, err)
		p.clearIdentityReturnCookie(w, outcome.CookieName, outcome.Partitioned)
		http.Error(w, "invalid app identity assertion", http.StatusBadGateway)
		return
	}
	expiresAt := time.Unix(claims.ExpiresAt, 0)
	maxAge := int(expiresAt.Sub(p.now()).Seconds())
	if maxAge < 1 {
		p.clearIdentityReturnCookie(w, outcome.CookieName, outcome.Partitioned)
		http.Error(w, "expired app identity assertion", http.StatusBadGateway)
		return
	}
	p.clearIdentityReturnCookie(w, outcome.CookieName, outcome.Partitioned)
	http.SetCookie(w, &http.Cookie{
		Name:        IdentitySessionCookieName,
		Value:       exchange.Assertion,
		Path:        "/",
		Secure:      true,
		HttpOnly:    true,
		SameSite:    appCookieSameSite(outcome.Partitioned),
		Partitioned: outcome.Partitioned,
		Expires:     expiresAt,
		MaxAge:      maxAge,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, outcome.Path, http.StatusFound)
}

func (p *Proxy) consumeIdentityReturnState(w http.ResponseWriter, r *http.Request, state string) returnStateOutcome {
	cookieName := identityReturnCookieName(state)
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		p.logf("app identity callback rejected: request carried no state cookie (host=%s)", p.config.host)
		return returnStateOutcome{}
	}
	payload, ok := decodeReturnState(cookie.Value)
	if !ok {
		p.clearIdentityReturnCookie(w, cookieName, requestUsesPartitionedCookies(r))
		p.logf("app identity callback rejected: state cookie is malformed (host=%s)", p.config.host)
		return returnStateOutcome{}
	}
	if state == "" || subtle.ConstantTimeCompare([]byte(payload.Nonce), []byte(state)) != 1 {
		p.logf("app identity callback rejected: state does not match its cookie (host=%s)", p.config.host)
		return returnStateOutcome{}
	}
	if !validIdentityCodeVerifier(payload.CodeVerifier) {
		p.clearIdentityReturnCookie(w, cookieName, payload.Partitioned)
		p.logf("app identity callback rejected: PKCE verifier is missing or malformed (host=%s)", p.config.host)
		return returnStateOutcome{}
	}
	if !p.now().Before(time.Unix(payload.ExpiresAt, 0)) {
		p.clearIdentityReturnCookie(w, cookieName, payload.Partitioned)
		return returnStateOutcome{Restart: true, RestartPath: cleanIdentityReturnPath(payload.Path), Partitioned: payload.Partitioned}
	}
	if !p.markStateUsed(payload.Nonce, time.Unix(payload.ExpiresAt, 0)) {
		p.clearIdentityReturnCookie(w, cookieName, payload.Partitioned)
		p.logf("app identity callback rejected: state was already used (host=%s)", p.config.host)
		return returnStateOutcome{}
	}
	path := cleanIdentityReturnPath(payload.Path)
	if path == "" {
		path = "/"
	}
	return returnStateOutcome{OK: true, Path: path, CookieName: cookieName, Partitioned: payload.Partitioned, CodeVerifier: payload.CodeVerifier}
}

func (p *Proxy) clearIdentityReturnCookie(w http.ResponseWriter, name string, partitioned bool) {
	http.SetCookie(w, &http.Cookie{
		Name:        name,
		Value:       "",
		Path:        "/",
		Secure:      true,
		HttpOnly:    true,
		SameSite:    appCookieSameSite(partitioned),
		Partitioned: partitioned,
		MaxAge:      -1,
	})
}

func (p *Proxy) clearIdentitySessionCookie(w http.ResponseWriter, partitioned bool) {
	for _, partitionedMode := range []bool{partitioned, !partitioned} {
		http.SetCookie(w, &http.Cookie{
			Name:        IdentitySessionCookieName,
			Value:       "",
			Path:        "/",
			Secure:      true,
			HttpOnly:    true,
			SameSite:    appCookieSameSite(partitionedMode),
			Partitioned: partitionedMode,
			MaxAge:      -1,
		})
	}
}

func hasIdentitySessionCookie(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, cookie := range r.Cookies() {
		if cookie.Name == IdentitySessionCookieName {
			return true
		}
	}
	return false
}

func (p *Proxy) identityAssertion(r *http.Request) string {
	_, assertion, ok := p.identitySessionFromRequest(r)
	if !ok {
		return ""
	}
	return assertion
}

func (p *Proxy) identitySessionFromRequest(r *http.Request) (identityJWTClaims, string, bool) {
	if p.identityVerifier == nil || r == nil {
		return identityJWTClaims{}, "", false
	}
	var selected identityJWTClaims
	var selectedAssertion string
	valid := false
	for _, cookie := range r.Cookies() {
		if cookie.Name != IdentitySessionCookieName {
			continue
		}
		claims, err := p.identityVerifier.verify(r.Context(), cookie.Value)
		if err != nil {
			continue
		}
		if valid && (claims.Subject != selected.Subject || claims.InstanceUID != selected.InstanceUID) {
			// Browsers can send both partitioned and unpartitioned cookies.
			// Accept duplicates only when they resolve to the same user and
			// instance, so account switching cannot pick an arbitrary identity.
			return identityJWTClaims{}, "", false
		}
		if !valid || claims.ExpiresAt > selected.ExpiresAt {
			selected = claims
			selectedAssertion = cookie.Value
		}
		valid = true
	}
	return selected, selectedAssertion, valid
}

func (v *identityVerifier) verify(ctx context.Context, assertion string) (identityJWTClaims, error) {
	if len(assertion) == 0 || len(assertion) > maxIdentityAssertionBytes {
		return identityJWTClaims{}, errors.New("identity assertion size is invalid")
	}
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return identityJWTClaims{}, errors.New("identity assertion is malformed")
	}
	var header identityJWTHeader
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(headerBytes, &header) != nil || header.Algorithm != "EdDSA" || header.KeyID == "" || (header.Type != "" && header.Type != "JWT") {
		return identityJWTClaims{}, errors.New("identity assertion header is invalid")
	}
	if strings.ContainsAny(header.KeyID, "\r\n") {
		return identityJWTClaims{}, errors.New("identity assertion key ID is invalid")
	}
	key, err := v.signingKey(ctx, header.KeyID)
	if err != nil {
		return identityJWTClaims{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return identityJWTClaims{}, errors.New("identity assertion signature is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return identityJWTClaims{}, errors.New("identity assertion payload is malformed")
	}
	var claims identityJWTClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return identityJWTClaims{}, errors.New("identity assertion claims are malformed")
	}
	if claims.Issuer != v.issuer || claims.Subject == "" || claims.AppID != v.audience || claims.Environment != v.environment || claims.InstanceUID != v.instanceUID {
		return identityJWTClaims{}, errors.New("identity assertion scope is invalid")
	}
	var audience string
	if err := json.Unmarshal(claims.Audience, &audience); err != nil {
		var values []string
		if arrayErr := json.Unmarshal(claims.Audience, &values); arrayErr != nil {
			return identityJWTClaims{}, errors.New("identity assertion audience is invalid")
		}
		if len(values) != 1 || values[0] != v.audience {
			return identityJWTClaims{}, errors.New("identity assertion audience does not match")
		}
	} else if audience != v.audience {
		return identityJWTClaims{}, errors.New("identity assertion audience does not match")
	}
	now := v.now()
	if claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt || claims.ExpiresAt-claims.IssuedAt > int64(maxIdentityTTL/time.Second) || now.Add(identityClockSkew).Before(time.Unix(claims.IssuedAt, 0)) || !now.Before(time.Unix(claims.ExpiresAt, 0)) {
		return identityJWTClaims{}, errors.New("identity assertion time window is invalid")
	}
	if claims.NotBefore != 0 && now.Add(identityClockSkew).Before(time.Unix(claims.NotBefore, 0)) {
		return identityJWTClaims{}, errors.New("identity assertion is not active yet")
	}
	return claims, nil
}

func (v *identityVerifier) signingKey(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
	now := v.now()
	v.mu.Lock()
	defer v.mu.Unlock()
	key, known := v.keys[keyID]
	if known && now.Before(v.keysUntil) {
		return key, nil
	}
	if !known && now.Before(v.unknownKidRefreshUntil) {
		return nil, errors.New("identity assertion key ID is unknown")
	}
	if now.Before(v.refreshFailureUntil) {
		return v.cachedKeyAfterRefreshFailure(keyID, now, known)
	}

	requestCtx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, v.hubURL+identityJWKSPath, nil)
	if err != nil {
		return nil, fmt.Errorf("create identity JWKS request: %w", err)
	}
	response, err := v.client.Do(request)
	if err != nil {
		return v.failJWKSRefresh(keyID, known)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return v.failJWKSRefresh(keyID, known)
	}
	var document identityJWKSet
	if err := json.NewDecoder(io.LimitReader(response.Body, maxIdentityJWKSBytes)).Decode(&document); err != nil || len(document.Keys) == 0 || len(document.Keys) > maxIdentityJWKSKeys {
		return v.failJWKSRefresh(keyID, known)
	}
	keys := make(map[string]ed25519.PublicKey, len(document.Keys))
	for _, jwk := range document.Keys {
		if jwk.KeyType != "OKP" || jwk.Curve != "Ed25519" || jwk.Use != "sig" || jwk.Algorithm != "EdDSA" || strings.TrimSpace(jwk.KeyID) == "" || strings.ContainsAny(jwk.KeyID, "\r\n") {
			return v.failJWKSRefresh(keyID, known)
		}
		publicKey, err := base64.RawURLEncoding.DecodeString(jwk.X)
		if err != nil || len(publicKey) != ed25519.PublicKeySize {
			return v.failJWKSRefresh(keyID, known)
		}
		if _, duplicate := keys[jwk.KeyID]; duplicate {
			return v.failJWKSRefresh(keyID, known)
		}
		keys[jwk.KeyID] = ed25519.PublicKey(publicKey)
	}
	v.keys = keys
	v.keysUntil = v.now().Add(identityJWKSCacheTTL)
	v.refreshFailureUntil = time.Time{}
	key, ok := keys[keyID]
	if !ok {
		v.unknownKidRefreshUntil = v.now().Add(identityJWKSRefreshBackoff)
		return nil, errors.New("identity assertion key ID is unknown")
	}
	v.unknownKidRefreshUntil = time.Time{}
	return key, nil
}

// failJWKSRefresh briefly suppresses repeated hub requests during an outage.
// A known key may remain usable through one maximum assertion lifetime after
// the last successful JWKS fetch; the JWT's own exp is still checked on every
// verification. Unknown kids never inherit trust from a cached key set.
func (v *identityVerifier) failJWKSRefresh(keyID string, known bool) (ed25519.PublicKey, error) {
	v.refreshFailureUntil = v.now().Add(identityJWKSRefreshBackoff)
	return v.cachedKeyAfterRefreshFailure(keyID, v.now(), known)
}

func (v *identityVerifier) cachedKeyAfterRefreshFailure(keyID string, now time.Time, known bool) (ed25519.PublicKey, error) {
	if known && !now.After(v.keysUntil.Add(maxIdentityTTL)) {
		if key, ok := v.keys[keyID]; ok {
			return key, nil
		}
	}
	if !known {
		return nil, errors.New("identity assertion key ID is unknown")
	}
	return nil, errors.New("identity signing keys are unavailable")
}
