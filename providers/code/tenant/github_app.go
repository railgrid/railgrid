// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
)

// CredentialResolver resolves a Connection credential. Installation keys stay
// inside Code; consumers receive neither the key nor the short-lived token.
type CredentialResolver struct {
	Client *http.Client
	Now    func() time.Time
	// OAuth renews expiring "Connect with GitHub" tokens. Nil leaves stored
	// OAuth tokens as they are.
	OAuth *OAuthRefresher
}

// CredentialTokenKey names the Secret data key holding the Connection's
// token, falling back to the convention when secretRef.Key is empty.
func CredentialTokenKey(conn *api.Connection) string {
	if key := conn.Spec.SecretRef.Key; key != "" {
		return key
	}
	return DefaultTokenKey
}

// refreshed returns data with a usable token in it, renewing an expiring OAuth
// token and persisting it through store first. secretKey identifies the Secret
// (cluster/namespace/name) so concurrent refreshes serialize.
func (r CredentialResolver) refreshed(ctx context.Context, conn *api.Connection, secretKey string, data map[string][]byte, store SecretStore) (map[string][]byte, error) {
	if !r.OAuth.refreshable(conn) {
		return data, nil
	}
	return r.OAuth.Fresh(ctx, secretKey, CredentialTokenKey(conn), data, store)
}

// ResolveStored resolves the credential held in data, first renewing an
// expiring OAuth token and persisting it through store.
func (r CredentialResolver) ResolveStored(ctx context.Context, conn *api.Connection, secretKey string, data map[string][]byte, store SecretStore) (backend.Credential, error) {
	fresh, err := r.refreshed(ctx, conn, secretKey, data, store)
	if err != nil {
		return backend.Credential{}, err
	}
	return r.Resolve(ctx, conn, fresh)
}

func (r CredentialResolver) Resolve(ctx context.Context, conn *api.Connection, data map[string][]byte) (backend.Credential, error) {
	if conn.Spec.Type != api.CredentialTypeGitHubApp {
		if conn.Spec.Type != api.CredentialTypePAT && conn.Spec.Type != api.CredentialTypeOAuth && conn.Spec.Type != "" {
			return backend.Credential{}, errors.New("unsupported Code credential type")
		}
		key := CredentialTokenKey(conn)
		// Trim: a Secret created from a file or `cmd | kubectl create secret
		// --from-file` keeps the trailing newline, which net/http rejects as an
		// invalid Authorization header value before GitHub is ever called.
		token := strings.TrimSpace(string(data[key]))
		if token == "" {
			return backend.Credential{}, errors.New("code credential token missing")
		}
		return backend.Credential{Token: token}, nil
	}
	token, _, err := r.installationToken(ctx, conn, data, nil)
	if err != nil {
		return backend.Credential{}, err
	}
	return backend.Credential{Token: token}, nil
}

// ResolveActor resolves an authenticated write identity only for operations
// that need ownership checks. App installations cannot use GET /user, so their
// app slug comes from GET /app authenticated with the Connection's JWT.
func (r CredentialResolver) ResolveActor(ctx context.Context, conn *api.Connection, data map[string][]byte) (string, string, error) {
	if conn.Spec.Type == api.CredentialTypeGitHubApp {
		slug, err := r.appSlug(ctx, conn, data)
		if err != nil {
			return "", "", err
		}
		return slug + "[bot]", "Bot", nil
	}
	return "", "", nil
}

func (r CredentialResolver) appSlug(ctx context.Context, conn *api.Connection, data map[string][]byte) (string, error) {
	now := r.now()
	base, err := githubAppAPIBase(conn)
	if err != nil {
		return "", err
	}
	// The slug of an App never changes, so it is not fetched for every write.
	// The key includes the private key's digest, so a slug is only reused for
	// the credential that fetched it.
	keyDigest := sha256.Sum256(data["privateKey"])
	cacheKey := base + "\x00" + strings.TrimSpace(string(data["appID"])) + "\x00" + string(keyDigest[:])
	if slug, ok := appSlugs.get(cacheKey, now); ok {
		return slug, nil
	}
	jwt, err := githubAppJWT(data, now)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/app", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := r.githubAppHTTPClient().Do(request)
	if err != nil {
		return "", errors.New("GitHub App identity request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub App identity returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Slug string `json:"slug"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 65537)).Decode(&result) != nil || result.Slug == "" || len(result.Slug) > 100 {
		return "", errors.New("invalid GitHub App identity response")
	}
	appSlugs.put(cacheKey, result.Slug, now)
	return result.Slug, nil
}

// appSlugTTL bounds how long a resolved App slug is reused.
const (
	appSlugTTL     = time.Hour
	appSlugEntries = 1024
)

type appSlugEntry struct {
	slug    string
	expires time.Time
}

type appSlugCache struct {
	mu      sync.Mutex
	entries map[string]appSlugEntry
}

var appSlugs appSlugCache

func (c *appSlugCache) get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !now.Before(entry.expires) {
		delete(c.entries, key)
		return "", false
	}
	return entry.slug, true
}

func (c *appSlugCache) put(key, slug string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= appSlugEntries {
		c.entries = map[string]appSlugEntry{}
	}
	c.entries[key] = appSlugEntry{slug: slug, expires: now.Add(appSlugTTL)}
}

func (r CredentialResolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// githubAppJWT signs the short-lived App JWT GitHub requires before it
// issues an installation token or describes the App.
func githubAppJWT(data map[string][]byte, now time.Time) (string, error) {
	appID, err := strconv.ParseInt(strings.TrimSpace(string(data["appID"])), 10, 64)
	if err != nil || appID <= 0 {
		return "", errors.New("GitHub App appID must be positive")
	}
	if len(data["privateKey"]) > 32768 {
		return "", errors.New("GitHub App private key exceeds limit")
	}
	block, rest := pem.Decode(data["privateKey"])
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return "", errors.New("GitHub App privateKey must contain one RSA private key")
	}
	var key *rsa.PrivateKey
	if block.Type == "RSA PRIVATE KEY" {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	} else {
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 {
		return "", errors.New("GitHub App requires a valid RSA key of at least 2048 bits")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iss": strconv.FormatInt(appID, 10), "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix()})
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("GitHub App signing failed")
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// githubAppAPIBase is the REST API root for the Connection: api.github.com,
// or a GitHub Enterprise Server's /api/v3.
func githubAppAPIBase(conn *api.Connection) (string, error) {
	if conn.Spec.BaseURL == "" {
		return "https://api.github.com", nil
	}
	u, err := url.Parse(conn.Spec.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("GitHub App baseURL must be an HTTPS API endpoint")
	}
	base := strings.TrimRight(u.String(), "/")
	if !strings.HasSuffix(base, "/api/v3") {
		base += "/api/v3"
	}
	return base, nil
}

// githubAppHTTPClient never follows a redirect: a credential request must
// not be replayed to another host.
func (r CredentialResolver) githubAppHTTPClient() *http.Client {
	client := &http.Client{Timeout: 20 * time.Second}
	if r.Client != nil {
		copied := *r.Client
		client = &copied
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("credential redirects forbidden") }
	return client
}

// installationToken mints a GitHub App installation access token for the
// Connection and returns it with its expiry.
//
// permissions, when non-empty, asks GitHub to issue the token with LESS than
// the installation holds — the documented way to get a narrow, short-lived
// credential out of an App. It is how mint-registry-token hands out a
// packages:read token instead of the credential that can also push code — a
// pull secret sits on a runtime cluster for as long as the workload does, so
// it must not be able to do anything but pull — and how mint-clone-token hands
// out a contents:read token that can fetch one repository and nothing more.
func (r CredentialResolver) installationToken(ctx context.Context, conn *api.Connection, data map[string][]byte, permissions map[string]string) (string, time.Time, error) {
	installationID, err := strconv.ParseInt(strings.TrimSpace(string(data["installationID"])), 10, 64)
	if err != nil || installationID <= 0 {
		return "", time.Time{}, errors.New("GitHub App installationID must be positive")
	}
	now := r.now()
	jwt, err := githubAppJWT(data, now)
	if err != nil {
		return "", time.Time{}, err
	}
	base, err := githubAppAPIBase(conn)
	if err != nil {
		return "", time.Time{}, err
	}
	body := "{}"
	if len(permissions) > 0 {
		encoded, err := json.Marshal(map[string]any{"permissions": permissions})
		if err != nil {
			return "", time.Time{}, errors.New("GitHub App permission request could not be encoded")
		}
		body = string(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/app/installations/%d/access_tokens", base, installationID), strings.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := r.githubAppHTTPClient().Do(req)
	if err != nil {
		return "", time.Time{}, errors.New("GitHub installation token request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated {
		return "", time.Time{}, fmt.Errorf("GitHub installation token returned HTTP %d", response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(payload) > 65536 {
		return "", time.Time{}, errors.New("invalid installation token response")
	}
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if json.Unmarshal(payload, &result) != nil || result.Token == "" || !result.ExpiresAt.After(now.Add(5*time.Minute)) {
		return "", time.Time{}, errors.New("invalid or expiring installation token")
	}
	return result.Token, result.ExpiresAt, nil
}
