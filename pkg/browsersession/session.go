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

// Package browsersession owns the hub's shared browser login session.
//
// A browser receives only a random, host-only cookie.  The value is never a
// user identity or a platform credential: the store keeps a SHA-256 lookup
// key and the identity server-side.  This package deliberately has no OIDC,
// Kubernetes, or hub dependencies so every HTTP boundary can share the same
// session contract without creating an import cycle.
package browsersession

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// CookieName is the one shared portal/browser session cookie.  The __Host-
	// prefix requires Secure, Path=/, and no Domain attribute, which makes the
	// cookie host-only even when an app is served from a sibling subdomain.
	CookieName = "__Host-faros-session"
	// SessionCookieName is retained as a descriptive alias for callers that
	// already use the access-proxy naming convention.
	SessionCookieName = CookieName
	defaultTTL        = 8 * time.Hour
	defaultMaxEntries = 10000
	secretSize        = 32
	// maxIssueAttempts bounds work spent recovering from a random-source
	// collision.  A cryptographically random source should never approach
	// this limit; it is primarily a fail-closed guard for a faulty source or
	// a deterministic test seam.
	maxIssueAttempts = 8
)

var (
	ErrInvalid  = errors.New("invalid browser session")
	ErrNotFound = errors.New("browser session not found")
	ErrExpired  = errors.New("browser session expired")
	ErrRevoked  = errors.New("browser session revoked")
)

// Identity is the minimum server-side identity needed by hub and provider
// authorization.  Credentials, ID tokens, refresh tokens, and kubeconfigs
// are intentionally not representable here.
type Identity struct {
	UserID string
	Email  string
	Name   string
	// RBACIdentity is the kcp username this account authenticates as inside
	// tenant workspaces (User.Spec.RBACIdentity, e.g. "faros:<email>"). All
	// workspace RBAC — admin ClusterRoleBindings and app-access grants — is
	// written against this string, so authorization checks (SAR) must use it,
	// never the User CR name. Not a credential.
	RBACIdentity string
	Issuer       string
	Subject      string
	AuthType     string
}

// Session is the server-side representation associated with the opaque
// browser cookie.  ID is returned to trusted hub callers for diagnostics; it
// is never serialized into the cookie itself.
type Session struct {
	ID        string
	Identity  Identity
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// storedSession is deliberately separate from Session: the raw opaque handle
// is returned transiently to the caller that needs to set a cookie, but the
// in-memory store retains only the hash-keyed identity and lifecycle fields.
type storedSession struct {
	Identity  Identity
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// Config configures a Store.  Now and Random are test seams; production uses
// time.Now and crypto/rand.Reader.  MaxEntries bounds process-local memory so
// an unbounded stream of logins cannot grow the store indefinitely.
type Config struct {
	TTL        time.Duration
	MaxEntries int
	Now        func() time.Time
	Random     io.Reader
}

// Store is a concurrency-safe, process-local browser-session store.  It is
// intentionally an in-memory implementation: deployments that need durable
// sessions can replace the store behind the same HTTP integration without
// changing the browser contract.
type Store struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
	random     io.Reader
	sessions   map[string]storedSession
	revoked    map[string]time.Time
	// revokedUnknown identifies markers created for handles that did not map
	// to a live session. Unknown markers are the first entries evicted when
	// the bounded revocation map reaches capacity, preserving known logout
	// revocations during arbitrary-cookie floods.
	revokedUnknown map[string]struct{}
}

// New creates a bounded browser-session store.
func New(config Config) *Store {
	ttl := config.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	maxEntries := config.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	random := config.Random
	if random == nil {
		random = rand.Reader
	}
	return &Store{
		ttl: ttl, maxEntries: maxEntries, now: now, random: random,
		sessions: make(map[string]storedSession), revoked: make(map[string]time.Time),
		revokedUnknown: make(map[string]struct{}),
	}
}

// TTL returns the configured lifetime for newly issued sessions.
func (s *Store) TTL() time.Duration {
	if s == nil {
		return 0
	}
	return s.ttl
}

// Issue stores identity and returns the opaque cookie value plus its
// server-side view.  It rejects empty stable identities so callers cannot
// accidentally mint an anonymous session.
func (s *Store) Issue(identity Identity) (string, Session, error) {
	if s == nil || strings.TrimSpace(identity.UserID) == "" {
		return "", Session{}, fmt.Errorf("%w: user id is required", ErrInvalid)
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	for attempt := 0; attempt < maxIssueAttempts; attempt++ {
		raw, err := s.randomSecret()
		if err != nil {
			return "", Session{}, err
		}
		key := tokenKey(raw)
		// Never overwrite a live session or resurrect a revoked handle.  A
		// collision is exceptionally unlikely with crypto/rand, but the
		// bounded retry keeps the invariant true even with a faulty source.
		if _, exists := s.sessions[key]; exists {
			continue
		}
		if _, revoked := s.revoked[key]; revoked {
			continue
		}
		if len(s.sessions) >= s.maxEntries {
			s.evictOldestLocked()
		}
		session := Session{ID: raw, Identity: identity, IssuedAt: now, ExpiresAt: now.Add(s.ttl)}
		s.sessions[key] = storedSession{Identity: identity, IssuedAt: now, ExpiresAt: session.ExpiresAt}
		return raw, session, nil
	}
	return "", Session{}, fmt.Errorf("generating browser session secret: no fresh handle after %d attempts", maxIssueAttempts)
}

// setCookie is the store-owned writer. It receives the session lifetime
// directly, so injected clocks and wall-clock skew cannot change the browser's
// authoritative Max-Age.
func setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: value, Path: "/", Secure: true,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

// IssueHTTP creates a session and writes its secure cookie.
func (s *Store) IssueHTTP(w http.ResponseWriter, identity Identity) (Session, error) {
	value, session, err := s.Issue(identity)
	if err != nil {
		return Session{}, err
	}
	maxAge := int(session.ExpiresAt.Sub(session.IssuedAt) / time.Second)
	if maxAge < 1 {
		maxAge = 1
	}
	setCookie(w, value, maxAge)
	return session, nil
}

// Resolve returns a live session for an opaque cookie value.
func (s *Store) Resolve(value string) (Session, error) {
	if s == nil || strings.TrimSpace(value) == "" {
		return Session{}, ErrNotFound
	}
	now := s.now()
	key := tokenKey(value)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Inspect the requested entry before sweeping the rest of the bounded
	// store so callers can distinguish an expired cookie from an unknown one.
	if stored, ok := s.sessions[key]; ok {
		if !now.Before(stored.ExpiresAt) {
			delete(s.sessions, key)
			return Session{}, ErrExpired
		}
		s.cleanupLocked(now)
		return Session{ID: value, Identity: stored.Identity, IssuedAt: stored.IssuedAt, ExpiresAt: stored.ExpiresAt}, nil
	}
	if _, ok := s.revoked[key]; ok {
		return Session{}, ErrRevoked
	}
	s.cleanupLocked(now)
	return Session{}, ErrNotFound
}

// ResolveRequest resolves the shared cookie from an HTTP request.
func (s *Store) ResolveRequest(r *http.Request) (Session, error) {
	if r == nil {
		return Session{}, ErrNotFound
	}
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return Session{}, ErrNotFound
	}
	return s.Resolve(cookie.Value)
}

// Revoke invalidates a session immediately.  Repeated revocation is
// idempotent, which makes logout safe to retry.
func (s *Store) Revoke(value string) error {
	if s == nil || strings.TrimSpace(value) == "" {
		return nil
	}
	now := s.now()
	key := tokenKey(value)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	if session, ok := s.sessions[key]; ok {
		delete(s.sessions, key)
		s.addRevocationLocked(key, session.ExpiresAt, false)
		return nil
	}
	// Keep a short marker for an unknown value so a stale cookie cannot be
	// accepted if a random-value collision ever occurs after logout.
	s.addRevocationLocked(key, now.Add(s.ttl), true)
	return nil
}

// RevokeRequest revokes the shared cookie, if one is present, and emits an
// expired cookie.  It is safe for anonymous requests.
func (s *Store) RevokeRequest(w http.ResponseWriter, r *http.Request) {
	if r != nil {
		if cookie, err := r.Cookie(CookieName); err == nil {
			_ = s.Revoke(cookie.Value)
		}
	}
	if w != nil {
		http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	}
}

// ClearCookie writes an expired shared cookie without touching server state.
func ClearCookie(w http.ResponseWriter) {
	if w == nil {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (s *Store) randomSecret() (string, error) {
	buf := make([]byte, secretSize)
	if _, err := io.ReadFull(s.random, buf); err != nil {
		return "", fmt.Errorf("generating browser session secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func tokenKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Store) cleanupLocked(now time.Time) {
	for key, session := range s.sessions {
		if !now.Before(session.ExpiresAt) {
			delete(s.sessions, key)
		}
	}
	for key, expiresAt := range s.revoked {
		if !now.Before(expiresAt) {
			delete(s.revoked, key)
			delete(s.revokedUnknown, key)
		}
	}
}

// addRevocationLocked bounds the replay/revocation marker map independently
// from live sessions. Revoking an arbitrary stream of unknown cookie values
// must not create an unbounded memory sink. Unknown markers are preferentially
// evicted; if the map contains only known logout revocations, a new unknown
// marker is dropped rather than weakening those revocations.
func (s *Store) addRevocationLocked(key string, expiresAt time.Time, unknown bool) {
	if _, exists := s.revoked[key]; exists {
		s.revoked[key] = expiresAt
		if !unknown {
			delete(s.revokedUnknown, key)
		}
		return
	}
	if len(s.revoked) >= s.maxEntries {
		if !s.evictUnknownRevocationLocked() && unknown {
			return
		}
		if len(s.revoked) >= s.maxEntries {
			// A newly revoked live session takes precedence over an older
			// marker once every slot is occupied by known revocations.
			for oldestKey := range s.revoked {
				delete(s.revoked, oldestKey)
				delete(s.revokedUnknown, oldestKey)
				break
			}
		}
	}
	s.revoked[key] = expiresAt
	if unknown {
		s.revokedUnknown[key] = struct{}{}
	}
}

func (s *Store) evictUnknownRevocationLocked() bool {
	for key := range s.revokedUnknown {
		delete(s.revokedUnknown, key)
		delete(s.revoked, key)
		return true
	}
	return false
}

func (s *Store) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, session := range s.sessions {
		if oldestKey == "" || session.IssuedAt.Before(oldest) {
			oldestKey, oldest = key, session.IssuedAt
		}
	}
	if oldestKey != "" {
		delete(s.sessions, oldestKey)
	}
}

// ConstantTimeEqual is a small helper for callers comparing a cookie value
// against a request-bound value without introducing a second implementation.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
