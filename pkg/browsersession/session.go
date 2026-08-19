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
//
// Where the server-side records live is a Backend decision.  The default is
// process-local memory, which is correct for a single hub replica.  A hub
// scaled to several replicas must supply a shared Backend (see
// pkg/hub/sharedstore): a cookie is issued by whichever replica handled the
// login, and every other replica has to resolve and revoke it.
package browsersession

import (
	"context"
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

// Record is what a Backend persists for one session.  The raw opaque handle is
// returned transiently to the caller that needs to set a cookie; a Backend only
// ever sees the hash-derived key, never the handle itself.
type Record struct {
	Identity  Identity
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// Backend is the authoritative store for session records.  Implementations are
// keyed by an opaque, already-hashed lookup key and must be safe for concurrent
// use.
//
// Get reports ErrNotFound, ErrExpired, or ErrRevoked; any other error is
// treated as a store failure and fails the request closed.
type Backend interface {
	Put(ctx context.Context, key string, record Record) error
	Get(ctx context.Context, key string) (Record, error)
	// Revoke invalidates key.  It is idempotent, which makes logout safe to
	// retry.  expiresAt is the horizon past which the implementation may drop
	// any tombstone it keeps.
	Revoke(ctx context.Context, key string, expiresAt time.Time) error
}

// Config configures a Store.  Now and Random are test seams; production uses
// time.Now and crypto/rand.Reader.  MaxEntries bounds the default in-memory
// backend so an unbounded stream of logins cannot grow it indefinitely; it is
// ignored when Backend is supplied.
type Config struct {
	TTL        time.Duration
	MaxEntries int
	Now        func() time.Time
	Random     io.Reader
	// Backend, when set, replaces the process-local memory backend.  Required
	// for a hub running more than one replica.
	Backend Backend
}

// Store is the concurrency-safe front end for browser sessions: it owns handle
// generation, the cookie contract, and TTL policy, and delegates record storage
// to a Backend.
type Store struct {
	ttl     time.Duration
	now     func() time.Time
	random  io.Reader
	backend Backend
}

// New creates a browser-session store.  Without Config.Backend it keeps records
// in bounded process-local memory.
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
	backend := config.Backend
	if backend == nil {
		backend = newMemoryBackend(maxEntries, now)
	}
	return &Store{ttl: ttl, now: now, random: random, backend: backend}
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
func (s *Store) Issue(ctx context.Context, identity Identity) (string, Session, error) {
	return s.issueWithTTL(ctx, identity, s.ttl)
}

// IssueTransient creates an opaque session handle with a caller-selected,
// shorter lifetime. It is used for one-time browser handoffs: the handle is
// redeemed immediately for a normal session cookie and must expire quickly if
// delivery never completes.
func (s *Store) IssueTransient(ctx context.Context, identity Identity, ttl time.Duration) (string, Session, error) {
	if s == nil || ttl <= 0 || ttl > s.ttl {
		return "", Session{}, fmt.Errorf("%w: transient ttl must be positive and no longer than the store ttl", ErrInvalid)
	}
	return s.issueWithTTL(ctx, identity, ttl)
}

func (s *Store) issueWithTTL(ctx context.Context, identity Identity, ttl time.Duration) (string, Session, error) {
	if s == nil || strings.TrimSpace(identity.UserID) == "" {
		return "", Session{}, fmt.Errorf("%w: user id is required", ErrInvalid)
	}
	now := s.now()
	for attempt := 0; attempt < maxIssueAttempts; attempt++ {
		raw, err := s.randomSecret()
		if err != nil {
			return "", Session{}, err
		}
		key := tokenKey(raw)
		// Never overwrite a live session or resurrect a revoked handle.  A
		// collision is exceptionally unlikely with crypto/rand, but the
		// bounded retry keeps the invariant true even with a faulty source.
		if _, err := s.backend.Get(ctx, key); err == nil ||
			errors.Is(err, ErrRevoked) || errors.Is(err, ErrExpired) {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return "", Session{}, fmt.Errorf("checking browser session handle: %w", err)
		}
		record := Record{Identity: identity, IssuedAt: now, ExpiresAt: now.Add(ttl)}
		if err := s.backend.Put(ctx, key, record); err != nil {
			return "", Session{}, fmt.Errorf("storing browser session: %w", err)
		}
		return raw, Session{ID: raw, Identity: identity, IssuedAt: record.IssuedAt, ExpiresAt: record.ExpiresAt}, nil
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
func (s *Store) IssueHTTP(ctx context.Context, w http.ResponseWriter, identity Identity) (Session, error) {
	value, session, err := s.Issue(ctx, identity)
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
func (s *Store) Resolve(ctx context.Context, value string) (Session, error) {
	if s == nil || strings.TrimSpace(value) == "" {
		return Session{}, ErrNotFound
	}
	record, err := s.backend.Get(ctx, tokenKey(value))
	if err != nil {
		return Session{}, err
	}
	if !s.now().Before(record.ExpiresAt) {
		return Session{}, ErrExpired
	}
	return Session{ID: value, Identity: record.Identity, IssuedAt: record.IssuedAt, ExpiresAt: record.ExpiresAt}, nil
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
	return s.Resolve(r.Context(), cookie.Value)
}

// Revoke invalidates a session immediately.  Repeated revocation is
// idempotent, which makes logout safe to retry.
func (s *Store) Revoke(ctx context.Context, value string) error {
	if s == nil || strings.TrimSpace(value) == "" {
		return nil
	}
	return s.backend.Revoke(ctx, tokenKey(value), s.now().Add(s.ttl))
}

// RevokeRequest revokes the shared cookie, if one is present, and emits an
// expired cookie.  It is safe for anonymous requests.
func (s *Store) RevokeRequest(w http.ResponseWriter, r *http.Request) {
	if r != nil {
		if cookie, err := r.Cookie(CookieName); err == nil {
			_ = s.Revoke(r.Context(), cookie.Value)
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

// memoryBackend is the default, process-local Backend.  It is correct for a
// single hub replica only; see the package doc.
type memoryBackend struct {
	mu         sync.Mutex
	maxEntries int
	now        func() time.Time
	sessions   map[string]Record
	revoked    map[string]time.Time
	// revokedUnknown identifies markers created for handles that did not map
	// to a live session. Unknown markers are the first entries evicted when
	// the bounded revocation map reaches capacity, preserving known logout
	// revocations during arbitrary-cookie floods.
	revokedUnknown map[string]struct{}
}

func newMemoryBackend(maxEntries int, now func() time.Time) *memoryBackend {
	return &memoryBackend{
		maxEntries: maxEntries, now: now,
		sessions: map[string]Record{}, revoked: map[string]time.Time{},
		revokedUnknown: map[string]struct{}{},
	}
}

func (m *memoryBackend) Put(_ context.Context, key string, record Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(m.now())
	if len(m.sessions) >= m.maxEntries {
		m.evictOldestLocked()
	}
	m.sessions[key] = record
	return nil
}

func (m *memoryBackend) Get(_ context.Context, key string) (Record, error) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	// Inspect the requested entry before sweeping the rest of the bounded
	// store so callers can distinguish an expired cookie from an unknown one.
	if record, ok := m.sessions[key]; ok {
		if !now.Before(record.ExpiresAt) {
			delete(m.sessions, key)
			return Record{}, ErrExpired
		}
		m.cleanupLocked(now)
		return record, nil
	}
	if _, ok := m.revoked[key]; ok {
		return Record{}, ErrRevoked
	}
	m.cleanupLocked(now)
	return Record{}, ErrNotFound
}

func (m *memoryBackend) Revoke(_ context.Context, key string, expiresAt time.Time) error {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	if record, ok := m.sessions[key]; ok {
		delete(m.sessions, key)
		m.addRevocationLocked(key, record.ExpiresAt, false)
		return nil
	}
	// Keep a short marker for an unknown value so a stale cookie cannot be
	// accepted if a random-value collision ever occurs after logout.
	m.addRevocationLocked(key, expiresAt, true)
	return nil
}

func (m *memoryBackend) cleanupLocked(now time.Time) {
	for key, record := range m.sessions {
		if !now.Before(record.ExpiresAt) {
			delete(m.sessions, key)
		}
	}
	for key, expiresAt := range m.revoked {
		if !now.Before(expiresAt) {
			delete(m.revoked, key)
			delete(m.revokedUnknown, key)
		}
	}
}

// addRevocationLocked bounds the replay/revocation marker map independently
// from live sessions. Revoking an arbitrary stream of unknown cookie values
// must not create an unbounded memory sink. Unknown markers are preferentially
// evicted; if the map contains only known logout revocations, a new unknown
// marker is dropped rather than weakening those revocations.
func (m *memoryBackend) addRevocationLocked(key string, expiresAt time.Time, unknown bool) {
	if _, exists := m.revoked[key]; exists {
		m.revoked[key] = expiresAt
		if !unknown {
			delete(m.revokedUnknown, key)
		}
		return
	}
	if len(m.revoked) >= m.maxEntries {
		if !m.evictUnknownRevocationLocked() && unknown {
			return
		}
		if len(m.revoked) >= m.maxEntries {
			// A newly revoked live session takes precedence over an older
			// marker once every slot is occupied by known revocations.
			for oldestKey := range m.revoked {
				delete(m.revoked, oldestKey)
				delete(m.revokedUnknown, oldestKey)
				break
			}
		}
	}
	m.revoked[key] = expiresAt
	if unknown {
		m.revokedUnknown[key] = struct{}{}
	}
}

func (m *memoryBackend) evictUnknownRevocationLocked() bool {
	for key := range m.revokedUnknown {
		delete(m.revokedUnknown, key)
		delete(m.revoked, key)
		return true
	}
	return false
}

func (m *memoryBackend) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, record := range m.sessions {
		if oldestKey == "" || record.IssuedAt.Before(oldest) {
			oldestKey, oldest = key, record.IssuedAt
		}
	}
	if oldestKey != "" {
		delete(m.sessions, oldestKey)
	}
}

// ConstantTimeEqual is a small helper for callers comparing a cookie value
// against a request-bound value without introducing a second implementation.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
