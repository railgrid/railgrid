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
	// maxReturnStates / maxSessions bound the two in-memory maps. Return
	// states are minted for unauthenticated traffic, so the cap plus the
	// time-gated sweep keeps anonymous request floods from growing the map or
	// buying O(N) work per request.
	maxReturnStates = 5000
	maxSessions     = 20000
	// sweepInterval gates how often either map is fully swept.
	sweepInterval = 30 * time.Second
)

// Proxy is a host-bound reverse proxy for one published template instance.
// It is safe for concurrent HTTP requests and contains no credentials.
type Proxy struct {
	config normalizedConfig
	now    func() time.Time
	random RandomSource

	mu               sync.Mutex
	returnStates     map[string]returnState
	sessions         map[string]appSession
	lastReturnSweep  time.Time
	lastSessionSweep time.Time
}

type returnState struct {
	path      string
	expiresAt time.Time
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
		config:       normalized,
		now:          time.Now,
		random:       config.Random,
		returnStates: make(map[string]returnState),
		sessions:     make(map[string]appSession),
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

// redirectToAuthorize starts the hub login flow: it parks the clean return
// path server-side under a one-use state handle, mirrors the handle in a
// short-lived cookie, and sends the browser to the hub authorize endpoint.
func (p *Proxy) redirectToAuthorize(w http.ResponseWriter, r *http.Request, path string) {
	if path == "" {
		path = "/"
	}
	handle, err := p.putReturnState(path)
	if err != nil {
		http.Error(w, "app access state unavailable", http.StatusInternalServerError)
		return
	}
	p.setReturnCookie(w, handle)
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
	state := r.URL.Query().Get("state")
	returnPath, ok := p.consumeReturnState(w, r, state)
	if !ok {
		http.Error(w, "invalid app access state", http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		p.clearReturnCookie(w)
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
		p.clearReturnCookie(w)
		http.Error(w, "app access exchange unavailable", http.StatusInternalServerError)
		return
	}
	requestCtx, cancel := context.WithTimeout(r.Context(), p.config.hubTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, p.config.hubURL+hubExchangePath, bytes.NewReader(body))
	if err != nil {
		p.clearReturnCookie(w)
		http.Error(w, "app access exchange unavailable", http.StatusBadGateway)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.config.hubClient.Do(request)
	if err != nil {
		p.clearReturnCookie(w)
		p.clearSessionCookie(w)
		http.Error(w, "app access exchange unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusGone {
		// Expired or replayed code: restart the flow. With a live hub shared
		// session this is a silent redirect loop of exactly one extra hop.
		p.clearReturnCookie(w)
		p.redirectToAuthorize(w, r, returnPath)
		return
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		p.clearReturnCookie(w)
		p.clearSessionCookie(w)
		http.Error(w, "app access exchange denied", http.StatusBadGateway)
		return
	}
	var exchange exchangeResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&exchange); err != nil || !exchange.Allowed || strings.TrimSpace(exchange.UserID) == "" {
		p.clearReturnCookie(w)
		http.Error(w, "invalid app access exchange", http.StatusBadGateway)
		return
	}
	sessionValue, err := p.putSession(exchange.UserID, exchange.SessionTTLSeconds)
	if err != nil {
		p.clearReturnCookie(w)
		http.Error(w, "app session unavailable", http.StatusInternalServerError)
		return
	}
	p.clearReturnCookie(w)
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sessionValue,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, returnPath, http.StatusFound)
}

// consumeReturnState binds a callback to a browser that completed the app's
// authorization redirect. The cookie is only an opaque lookup key; the live
// server-side state is the authority. Taking the state under the map lock
// consumes it exactly once, so copied callback URLs, forged cookies, expired
// handles, and replayed callbacks cannot reach the hub exchange endpoint.
// A mismatched cookie/state pair deliberately does NOT consume the victim's
// live state.
func (p *Proxy) consumeReturnState(w http.ResponseWriter, r *http.Request, state string) (string, bool) {
	cookie, err := r.Cookie(ReturnCookieName)
	if err != nil {
		p.clearReturnCookie(w)
		return "", false
	}
	handle := strings.TrimSpace(cookie.Value)
	if state == "" || subtle.ConstantTimeCompare([]byte(handle), []byte(state)) != 1 {
		return "", false
	}
	path, ok := p.takeReturnState(handle)
	if !ok {
		p.clearReturnCookie(w)
		return "", false
	}
	return path, true
}

func (p *Proxy) putReturnState(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty return path")
	}
	handle, err := p.randomString(32)
	if err != nil {
		return "", err
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepReturnStatesLocked(now)
	for len(p.returnStates) >= maxReturnStates {
		evictOldestReturnLocked(p.returnStates)
	}
	p.returnStates[handle] = returnState{path: path, expiresAt: now.Add(stateTTL)}
	return handle, nil
}

func (p *Proxy) takeReturnState(handle string) (string, bool) {
	if handle == "" {
		return "", false
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.returnStates[handle]
	if !ok || !now.Before(state.expiresAt) {
		delete(p.returnStates, handle)
		return "", false
	}
	delete(p.returnStates, handle)
	return state.path, true
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

// sweepReturnStatesLocked and sweepSessionsLocked are time-gated full sweeps:
// at most one O(N) pass per sweepInterval, so per-request work stays O(1)
// even under an anonymous request flood.
func (p *Proxy) sweepReturnStatesLocked(now time.Time) {
	if now.Sub(p.lastReturnSweep) < sweepInterval && len(p.returnStates) < maxReturnStates {
		return
	}
	p.lastReturnSweep = now
	for handle, state := range p.returnStates {
		if !now.Before(state.expiresAt) {
			delete(p.returnStates, handle)
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

func evictOldestReturnLocked(states map[string]returnState) {
	var oldestKey string
	var oldest time.Time
	for key, state := range states {
		if oldestKey == "" || state.expiresAt.Before(oldest) {
			oldestKey, oldest = key, state.expiresAt
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

func (p *Proxy) setReturnCookie(w http.ResponseWriter, handle string) {
	http.SetCookie(w, &http.Cookie{
		Name:     ReturnCookieName,
		Value:    handle,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(stateTTL / time.Second),
	})
}

func (p *Proxy) clearReturnCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     ReturnCookieName,
		Value:    "",
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (p *Proxy) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
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
		if strings.HasPrefix(lower, "x-kedge-") || isProxyReservedHeader(lower) {
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
		if strings.HasPrefix(lower, "x-kedge-") || lower == "authorization" || lower == "proxy-authenticate" || lower == "www-authenticate" {
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
	return strings.EqualFold(name, SessionCookieName) || strings.EqualFold(name, ReturnCookieName)
}
