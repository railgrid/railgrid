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
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// stateTTL bounds the authorize round-trip through the hub and IdP.
	stateTTL = 5 * time.Minute
	// maxUsedStates / maxSessions bound the two in-memory maps. Used states are
	// only recorded for callbacks that already matched their cookie, so the cap
	// plus the time-gated sweep keeps anonymous request floods from growing the
	// map or buying O(N) work per request.
	maxUsedStates = 5000
	maxSessions   = 20000
	// sweepInterval gates how often either map is fully swept.
	sweepInterval = 30 * time.Second
	// maxReturnPathBytes caps the deep link carried in the return cookie so the
	// cookie stays well inside browser size limits. A longer path resumes at
	// "/" — resuming the exact deep link is a convenience, signing in is not.
	maxReturnPathBytes = 1024
	// maxReturnCookieBytes bounds the work a forged cookie can buy.
	maxReturnCookieBytes = 4096
)

// Proxy is a host-bound reverse proxy for one published template instance.
// It is safe for concurrent HTTP requests and contains no credentials.
type Proxy struct {
	config normalizedConfig
	now    func() time.Time
	random RandomSource

	mu               sync.Mutex
	usedStates       map[string]time.Time
	sessions         map[string]appSession
	lastUsedSweep    time.Time
	lastSessionSweep time.Time
}

// returnStatePayload is the entire content of the return cookie: the nonce
// echoed to the hub as `state`, the clean path to resume on success, and the
// absolute expiry. Keeping it in the cookie rather than a server-side map is
// what lets a sign-in survive a gate restart or land on a different replica.
type returnStatePayload struct {
	Nonce     string `json:"n"`
	Path      string `json:"p"`
	ExpiresAt int64  `json:"e"`
}

type appSession struct {
	userID    string
	expiresAt time.Time
}

// exchangeRequest / exchangeResponse mirror pkg/hub/appauth's wire contract.
type exchangeRequest struct {
	Code     string `json:"code"`
	Host     string `json:"host"`
	Cluster  string `json:"cluster"`
	Group    string `json:"group"`
	Resource string `json:"resource"`
	Name     string `json:"name"`
}

type exchangeResponse struct {
	Allowed           bool   `json:"allowed"`
	UserID            string `json:"userId"`
	SessionTTLSeconds int64  `json:"sessionTtlSeconds"`
}

// New constructs a configured access proxy.  No listener is started until
// Serve or the returned Handler is used.
func New(config Config) (*Proxy, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	proxy := &Proxy{
		config:     normalized,
		now:        time.Now,
		random:     config.Random,
		usedStates: make(map[string]time.Time),
		sessions:   make(map[string]appSession),
	}
	if proxy.random == nil {
		proxy.random = rand.Reader
	}
	return proxy, nil
}

// Handler returns the reusable HTTP handler.
func (p *Proxy) Handler() http.Handler { return p }

// Serve starts an HTTP listener and shuts it down when ctx is cancelled.
func (p *Proxy) Serve(ctx context.Context) error {
	if p == nil {
		return errors.New("nil access proxy")
	}
	server := &http.Server{
		Addr:              p.config.ListenAddress,
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ServeHTTP enforces the exact app host, then either forwards directly
// (public) or gates on the proxy-local session (private). Application bodies
// never transit the hub in either mode.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p == nil {
		http.Error(w, "access proxy unavailable", http.StatusServiceUnavailable)
		return
	}
	host, err := canonicalHost(requestHost(r))
	if err != nil || host != p.config.host {
		w.WriteHeader(http.StatusMisdirectedRequest)
		return
	}
	// The platform callback path is reserved in every mode so an app can never
	// shadow it, and so login completion keeps working while a pod rolls from
	// public to private configuration.
	if r.URL.Path == CallbackPath {
		if p.config.Mode != ModePrivate {
			http.NotFound(w, r)
			return
		}
		p.handleCallback(w, r)
		return
	}
	route, ok := p.routeFor(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if p.config.Mode == ModePrivate {
		if _, ok := p.currentSession(r); !ok {
			p.redirectToAuthorize(w, r, cleanReturnPath(r.URL.RequestURI()))
			return
		}
	}
	p.forward(w, r, route)
}

// currentSession resolves the request's session cookie against the local
// bounded session store. No network calls are involved.
func (p *Proxy) currentSession(r *http.Request) (appSession, bool) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return appSession{}, false
	}
	key := sessionKey(cookie.Value)
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepSessionsLocked(now)
	session, ok := p.sessions[key]
	if !ok || !now.Before(session.expiresAt) {
		delete(p.sessions, key)
		return appSession{}, false
	}
	return session, true
}

func (p *Proxy) routeFor(path string) (normalizedRoute, bool) {
	for _, route := range p.config.routes {
		if pathMatches(route.prefix, path) {
			return route, true
		}
	}
	return normalizedRoute{}, false
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, route normalizedRoute) {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			target := *route.target
			target.Path = joinTargetPath(route.target.Path, routeSuffix(route.prefix, request.In.URL.Path))
			target.RawPath = ""
			target.RawQuery = request.In.URL.RawQuery
			// SetURL intentionally joins the target base path with the entire
			// incoming path.  We have already selected and stripped the route
			// prefix, so assign the complete rewritten URL directly.
			request.Out.URL = &target
			request.Out.Host = target.Host
			stripUpstreamRequestHeaders(request.Out.Header)
		},
		ModifyResponse: func(response *http.Response) error {
			stripUpstreamResponseHeaders(response.Header)
			return nil
		},
		ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, err error) {
			http.Error(writer, "published app upstream unavailable", http.StatusBadGateway)
		},
		FlushInterval: -1,
	}
	proxy.ServeHTTP(w, r)
}

// redirectToAuthorize starts the hub login flow: it packs the clean return
// path and a fresh nonce into a short-lived cookie, echoes the nonce to the hub
// as `state`, and sends the browser to the hub authorize endpoint.
func (p *Proxy) redirectToAuthorize(w http.ResponseWriter, r *http.Request, path string) {
	if path == "" {
		path = "/"
	}
	handle, cookieValue, err := p.newReturnState(path)
	if err != nil {
		http.Error(w, "app access state unavailable", http.StatusInternalServerError)
		return
	}
	p.setReturnCookie(w, handle, cookieValue, requestUsesPartitionedCookies(r))
	callbackURL := p.config.publicScheme + "://" + p.config.host + CallbackPath
	query := url.Values{}
	query.Set("cluster", p.config.Instance.Cluster)
	query.Set("group", p.config.Instance.Group)
	query.Set("resource", p.config.Instance.Resource)
	query.Set("name", p.config.Instance.Name)
	query.Set("redirect_uri", callbackURL)
	query.Set("state", handle)
	location := p.config.hubPublicURL + hubAuthorizePath + "?" + query.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, location, http.StatusFound)
}

// handleCallback completes a sign-in: it binds the callback to the browser
// that started it (state handle + cookie, one-use), exchanges the one-use
// code at the hub, and mints the proxy-local session.
func (p *Proxy) handleCallback(w http.ResponseWriter, r *http.Request) {
	partitioned := requestUsesPartitionedCookies(r)
	state := r.URL.Query().Get("state")
	outcome := p.consumeReturnState(w, r, state, partitioned)
	if !outcome.OK {
		// A stale sign-in is recoverable without involving the user: send the
		// browser back through authorize with a fresh nonce and cookie. The
		// restart is safe from looping because it is only offered when the
		// browser demonstrably returned our cookie — see returnStateOutcome.
		if outcome.Restart {
			p.redirectToAuthorize(w, r, outcome.RestartPath)
			return
		}
		http.Error(w, "invalid app access state", http.StatusBadRequest)
		return
	}
	returnPath := outcome.Path
	returnCookie := outcome.CookieName
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		p.clearReturnCookie(w, returnCookie, partitioned)
		http.Error(w, "missing app access code", http.StatusBadRequest)
		return
	}
	body, err := json.Marshal(exchangeRequest{
		Code:     code,
		Host:     p.config.host,
		Cluster:  p.config.Instance.Cluster,
		Group:    p.config.Instance.Group,
		Resource: p.config.Instance.Resource,
		Name:     p.config.Instance.Name,
	})
	if err != nil {
		p.clearReturnCookie(w, returnCookie, partitioned)
		http.Error(w, "app access exchange unavailable", http.StatusInternalServerError)
		return
	}
	requestCtx, cancel := context.WithTimeout(r.Context(), p.config.hubTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, p.config.hubURL+hubExchangePath, bytes.NewReader(body))
	if err != nil {
		p.clearReturnCookie(w, returnCookie, partitioned)
		http.Error(w, "app access exchange unavailable", http.StatusBadGateway)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.config.hubClient.Do(request)
	if err != nil {
		// Includes the TLS case: a hub certificate that does not cover the
		// in-cluster URL this gate was configured with fails every sign-in
		// here, and the 502 reaches the browser as a generic bad-gateway page
		// with no hint of the cause. Name the URL and the error.
		p.logf("app access exchange failed: %s is unreachable from this gate (host=%s): %v", p.config.hubURL+hubExchangePath, p.config.host, err)
		p.clearReturnCookie(w, returnCookie, partitioned)
		p.clearSessionCookie(w, partitioned)
		http.Error(w, "app access exchange unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusGone {
		// Expired or replayed code: restart the flow. With a live hub shared
		// session this is a silent redirect loop of exactly one extra hop.
		p.clearReturnCookie(w, returnCookie, partitioned)
		p.redirectToAuthorize(w, r, returnPath)
		return
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		p.logf("app access exchange refused: hub answered %d (host=%s)", response.StatusCode, p.config.host)
		p.clearReturnCookie(w, returnCookie, partitioned)
		p.clearSessionCookie(w, partitioned)
		http.Error(w, "app access exchange denied", http.StatusBadGateway)
		return
	}
	var exchange exchangeResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&exchange); err != nil {
		p.logf("app access exchange returned an unreadable body (host=%s): %v", p.config.host, err)
		p.clearReturnCookie(w, returnCookie, partitioned)
		http.Error(w, "invalid app access exchange", http.StatusBadGateway)
		return
	}
	if !exchange.Allowed || strings.TrimSpace(exchange.UserID) == "" {
		p.logf("app access exchange returned no grant: allowed=%t userID-empty=%t (host=%s)", exchange.Allowed, strings.TrimSpace(exchange.UserID) == "", p.config.host)
		p.clearReturnCookie(w, returnCookie, partitioned)
		http.Error(w, "invalid app access exchange", http.StatusBadGateway)
		return
	}
	sessionValue, err := p.putSession(exchange.UserID, exchange.SessionTTLSeconds)
	if err != nil {
		p.clearReturnCookie(w, returnCookie, partitioned)
		http.Error(w, "app session unavailable", http.StatusInternalServerError)
		return
	}
	p.clearReturnCookie(w, returnCookie, partitioned)
	http.SetCookie(w, &http.Cookie{
		Name:        SessionCookieName,
		Value:       sessionValue,
		Path:        "/",
		Secure:      true,
		HttpOnly:    true,
		SameSite:    appCookieSameSite(partitioned),
		Partitioned: partitioned,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, returnPath, http.StatusFound)
}

// consumeReturnState binds a callback to the browser that started the app's
// authorization redirect. The cookie carries the state itself, so the check is
// purely local: a callback completes against whichever pod happens to serve it,
// including one that started after the redirect was issued. Copied callback
// URLs (no cookie), forged or mismatched pairs, and expired states are all
// rejected before the hub exchange is reached.
//
// The state is nonce-bound but deliberately unsigned — the proxy holds no keys
// (see the package doc), and every field is either compared against the query
// parameter or re-sanitised here, so a tampered cookie can only produce a
// same-origin return path.
//
// A mismatched cookie/state pair does NOT clear the cookie: the browser may
// still be able to complete its own sign-in. Single use is enforced by the
// usedStates cache below, which is a best-effort optimisation rather than the
// authority — see markStateUsed.
func (p *Proxy) consumeReturnState(w http.ResponseWriter, r *http.Request, state string, partitioned bool) returnStateOutcome {
	cookieName := returnCookieName(state)
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		// Accept the original unsuffixed cookie during a rolling upgrade. New
		// redirects only issue nonce-specific cookies.
		cookieName = ReturnCookieName
		cookie, err = r.Cookie(cookieName)
	}
	if err != nil {
		// Deliberately NOT restartable. A restart would mint another cookie
		// this browser also fails to send back, and the app would bounce
		// between the gate and the hub for as long as the user waits.
		p.logf("app access callback rejected: request carried no %s cookie (host=%s) — a copied callback URL, or the cookie was blocked", ReturnCookieName, p.config.host)
		return returnStateOutcome{}
	}
	payload, ok := decodeReturnState(cookie.Value)
	if !ok {
		// A cookie came back, so the browser does round-trip ours; it is just
		// unreadable (truncated, or written by an older gate). Start over.
		p.logf("app access callback recovering: %s cookie is malformed (host=%s) — restarting sign-in", ReturnCookieName, p.config.host)
		p.clearReturnCookie(w, cookieName, partitioned)
		return returnStateOutcome{Restart: true, RestartPath: "/"}
	}
	if state == "" || subtle.ConstantTimeCompare([]byte(payload.Nonce), []byte(state)) != 1 {
		// Not restarted on purpose: the cookie may belong to a different
		// sign-in this browser still has in flight, and overwriting it here
		// would break that one to fix a callback that is probably forged or
		// hand-copied.
		p.logf("app access callback rejected: state parameter does not match the %s cookie (host=%s)", ReturnCookieName, p.config.host)
		return returnStateOutcome{}
	}
	if !p.now().Before(time.Unix(payload.ExpiresAt, 0)) {
		// The overwhelmingly common cause is a tab left open past stateTTL.
		// The cookie proves the browser stores ours, so a fresh round trip
		// will succeed — and the deep link survives it.
		p.logf("app access callback recovering: sign-in state expired (host=%s) after %s — restarting sign-in", p.config.host, stateTTL)
		p.clearReturnCookie(w, cookieName, partitioned)
		return returnStateOutcome{Restart: true, RestartPath: cleanReturnPath(payload.Path)}
	}
	if !p.markStateUsed(payload.Nonce, time.Unix(payload.ExpiresAt, 0)) {
		// Not restarted: a replay is an attack signal, and a successful
		// sign-in already cleared the cookie, so the benign refresh case
		// lands in the no-cookie branch above rather than here.
		p.logf("app access callback rejected: sign-in state already used (host=%s) — replayed callback", p.config.host)
		p.clearReturnCookie(w, cookieName, partitioned)
		return returnStateOutcome{}
	}
	// Re-sanitise rather than trust: the path round-tripped through a cookie.
	path := cleanReturnPath(payload.Path)
	if path == "" {
		path = "/"
	}
	return returnStateOutcome{Path: path, CookieName: cookieName, OK: true}
}

// returnStateOutcome is the verdict on a callback's state/cookie pair.
//
// The distinction that matters is Restart: a stale sign-in is worth retrying
// automatically, a blocked cookie is not. Both used to collapse into the same
// opaque "invalid app access state" 400, which stranded users whose only fault
// was leaving the tab open past stateTTL.
type returnStateOutcome struct {
	// Path is the sanitised return path. Meaningful only when OK.
	Path       string
	CookieName string
	OK         bool
	// Restart reports that beginning a fresh sign-in can plausibly succeed.
	// It is set ONLY when a cookie actually came back, because that is the
	// evidence the browser round-trips ours — without it a restart loops.
	Restart bool
	// RestartPath is the deep link to preserve across a restart. Empty is
	// fine; redirectToAuthorize substitutes "/".
	RestartPath string
}

// newReturnState mints the nonce echoed to the hub and the cookie value that
// carries the whole state back to the callback.
func (p *Proxy) newReturnState(path string) (nonce, cookieValue string, err error) {
	if path == "" {
		return "", "", errors.New("empty return path")
	}
	if len(path) > maxReturnPathBytes {
		path = "/"
	}
	nonce, err = p.randomString(32)
	if err != nil {
		return "", "", err
	}
	raw, err := json.Marshal(returnStatePayload{
		Nonce:     nonce,
		Path:      path,
		ExpiresAt: p.now().Add(stateTTL).Unix(),
	})
	if err != nil {
		return "", "", err
	}
	return nonce, base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeReturnState(value string) (returnStatePayload, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxReturnCookieBytes {
		return returnStatePayload{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return returnStatePayload{}, false
	}
	var payload returnStatePayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Nonce == "" || payload.ExpiresAt == 0 {
		return returnStatePayload{}, false
	}
	return payload, true
}

// markStateUsed records a nonce as spent and reports whether it was still
// unused. Unlike the map it replaces, losing this cache costs nothing but
// replay detection: the hub's authorization code is itself one-use, so a replay
// that slips through a restart or an eviction is answered with 410 and simply
// restarts the flow.
func (p *Proxy) markStateUsed(nonce string, expiresAt time.Time) bool {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepUsedStatesLocked(now)
	if _, used := p.usedStates[nonce]; used {
		return false
	}
	for len(p.usedStates) >= maxUsedStates {
		evictOldestUsedLocked(p.usedStates)
	}
	p.usedStates[nonce] = expiresAt
	return true
}

func (p *Proxy) putSession(userID string, ttlSeconds int64) (string, error) {
	value, err := p.randomString(32)
	if err != nil {
		return "", err
	}
	ttl := time.Duration(ttlSeconds) * time.Second
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	if ttl > maxSessionTTL {
		ttl = maxSessionTTL
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepSessionsLocked(now)
	for len(p.sessions) >= maxSessions {
		evictOldestSessionLocked(p.sessions)
	}
	p.sessions[sessionKey(value)] = appSession{userID: userID, expiresAt: now.Add(ttl)}
	return value, nil
}

// sweepUsedStatesLocked and sweepSessionsLocked are time-gated full sweeps:
// at most one O(N) pass per sweepInterval, so per-request work stays O(1)
// even under an anonymous request flood.
func (p *Proxy) sweepUsedStatesLocked(now time.Time) {
	if now.Sub(p.lastUsedSweep) < sweepInterval && len(p.usedStates) < maxUsedStates {
		return
	}
	p.lastUsedSweep = now
	for nonce, expiresAt := range p.usedStates {
		if !now.Before(expiresAt) {
			delete(p.usedStates, nonce)
		}
	}
}

func (p *Proxy) sweepSessionsLocked(now time.Time) {
	if now.Sub(p.lastSessionSweep) < sweepInterval && len(p.sessions) < maxSessions {
		return
	}
	p.lastSessionSweep = now
	for key, session := range p.sessions {
		if !now.Before(session.expiresAt) {
			delete(p.sessions, key)
		}
	}
}

func evictOldestUsedLocked(states map[string]time.Time) {
	var oldestKey string
	var oldest time.Time
	for key, expiresAt := range states {
		if oldestKey == "" || expiresAt.Before(oldest) {
			oldestKey, oldest = key, expiresAt
		}
	}
	if oldestKey != "" {
		delete(states, oldestKey)
	}
}

func evictOldestSessionLocked(sessions map[string]appSession) {
	var oldestKey string
	var oldest time.Time
	for key, session := range sessions {
		if oldestKey == "" || session.expiresAt.Before(oldest) {
			oldestKey, oldest = key, session.expiresAt
		}
	}
	if oldestKey != "" {
		delete(sessions, oldestKey)
	}
}

// logf reports an operational event. Callback rejections are otherwise
// invisible: the browser sees one opaque 400 whichever check failed, so the
// reason has to reach the operator through the log.  Never log the nonce or
// the state parameter — they are the browser's binding secret.
func (p *Proxy) logf(format string, args ...any) {
	if p.config.Logf != nil {
		p.config.Logf(format, args...)
		return
	}
	log.Printf("faros-access-proxy: "+format, args...)
}

func sessionKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (p *Proxy) randomString(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := io.ReadFull(p.random, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// requestUsesPartitionedCookies reports whether the browser is navigating the
// app inside an iframe. Local development deliberately serves the portal and
// app from different sites (localhost vs sslip.io), so SameSite=Lax cookies
// are withheld on the private-preview login callback. CHIPS lets that embedded
// flow retain a cookie scoped to the portal's top-level site without weakening
// ordinary top-level app sessions.
func requestUsesPartitionedCookies(r *http.Request) bool {
	return r != nil && strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Dest")), "iframe")
}

func appCookieSameSite(partitioned bool) http.SameSite {
	if partitioned {
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
}

func returnCookieName(state string) string {
	digest := sha256.Sum256([]byte(state))
	return ReturnCookieName + "-" + hex.EncodeToString(digest[:16])
}

func (p *Proxy) setReturnCookie(w http.ResponseWriter, state, handle string, partitioned bool) {
	http.SetCookie(w, &http.Cookie{
		Name:        returnCookieName(state),
		Value:       handle,
		Path:        CallbackPath,
		Secure:      true,
		HttpOnly:    true,
		SameSite:    appCookieSameSite(partitioned),
		Partitioned: partitioned,
		MaxAge:      int(stateTTL / time.Second),
	})
}

func (p *Proxy) clearReturnCookie(w http.ResponseWriter, name string, partitioned bool) {
	path := CallbackPath
	if strings.EqualFold(name, ReturnCookieName) {
		// Legacy unsuffixed cookies were issued for the whole app origin.
		path = "/"
	}
	http.SetCookie(w, &http.Cookie{
		Name:        name,
		Value:       "",
		Path:        path,
		Secure:      true,
		HttpOnly:    true,
		SameSite:    appCookieSameSite(partitioned),
		Partitioned: partitioned,
		MaxAge:      -1,
	})
}

func (p *Proxy) clearSessionCookie(w http.ResponseWriter, partitioned bool) {
	http.SetCookie(w, &http.Cookie{
		Name:        SessionCookieName,
		Value:       "",
		Path:        "/",
		Secure:      true,
		HttpOnly:    true,
		SameSite:    appCookieSameSite(partitioned),
		Partitioned: partitioned,
		MaxAge:      -1,
	})
}

func requestHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	if strings.TrimSpace(r.Host) != "" {
		return r.Host
	}
	if r.URL != nil {
		return r.URL.Host
	}
	return ""
}

func cleanReturnPath(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\r\n") {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" || parsed.Path == "" {
		return ""
	}
	return parsed.RequestURI()
}

func joinTargetPath(base, suffix string) string {
	if base == "" {
		base = "/"
	}
	if suffix == "" {
		suffix = "/"
	}
	if base == "/" {
		return suffix
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(suffix, "/")
}

func stripUpstreamRequestHeaders(headers http.Header) {
	for key := range headers {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-faros-") || isProxyReservedHeader(lower) {
			delete(headers, key)
		}
	}
	filterCookieHeader(headers)
}

func isProxyReservedHeader(lower string) bool {
	switch lower {
	case "authorization", "proxy-authorization", "forwarded", "via", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto", "x-forwarded-port", "x-real-ip", "x-original-url", "x-rewrite-url":
		return true
	default:
		return false
	}
}

func filterCookieHeader(headers http.Header) {
	values := headers.Values("Cookie")
	if len(values) == 0 {
		return
	}
	var keep []string
	for _, value := range values {
		for _, part := range strings.Split(value, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, _, ok := strings.Cut(part, "=")
			if !ok || isInternalCookie(strings.TrimSpace(name)) {
				continue
			}
			keep = append(keep, part)
		}
	}
	if len(keep) == 0 {
		headers.Del("Cookie")
		return
	}
	headers.Set("Cookie", strings.Join(keep, "; "))
}

func stripUpstreamResponseHeaders(headers http.Header) {
	for key := range headers {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-faros-") || lower == "authorization" || lower == "proxy-authenticate" || lower == "www-authenticate" {
			delete(headers, key)
		}
	}
	values := headers.Values("Set-Cookie")
	if len(values) == 0 {
		return
	}
	headers.Del("Set-Cookie")
	for _, value := range values {
		name := strings.TrimSpace(strings.SplitN(value, "=", 2)[0])
		if isInternalCookie(name) {
			continue
		}
		// Published apps share a platform DNS suffix. Never let an upstream
		// response widen its cookie to that parent and poison sibling apps;
		// omitting Domain makes the browser bind it to this exact host.
		headers.Add("Set-Cookie", stripCookieDomain(value))
	}
}

func stripCookieDomain(value string) string {
	parts := strings.Split(value, ";")
	if len(parts) < 2 {
		return value
	}
	keep := []string{strings.TrimSpace(parts[0])}
	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		attribute, _, _ := strings.Cut(part, "=")
		if strings.EqualFold(strings.TrimSpace(attribute), "domain") {
			continue
		}
		if part != "" {
			keep = append(keep, part)
		}
	}
	return strings.Join(keep, "; ")
}

func isInternalCookie(name string) bool {
	return strings.EqualFold(name, SessionCookieName) ||
		strings.EqualFold(name, ReturnCookieName) ||
		strings.HasPrefix(strings.ToLower(name), strings.ToLower(ReturnCookieName)+"-")
}
