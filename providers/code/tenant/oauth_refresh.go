// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"k8s.io/klog/v2"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
)

// Secret keys the "Connect with GitHub" flow writes next to the access token
// when the OAuth App issues expiring user tokens (8h access token plus a
// refresh token). Empty or absent values mean the token does not expire.
const (
	OAuthRefreshTokenKey = "refreshToken"
	OAuthExpiryKey       = "expiry" // RFC 3339
)

// CredentialSecretID names a Connection's credential Secret across every
// caller, so refreshes of one Secret serialize within the process.
func CredentialSecretID(conn *api.Connection, namespace string) string {
	return conn.Annotations["kcp.io/cluster"] + "/" + namespace + "/" + conn.Spec.SecretRef.Name
}

// oauthRefreshSkew renews a token this long before it expires, so a request
// started with it does not fail mid-flight.
const oauthRefreshSkew = 5 * time.Minute

// ErrOAuthReconnectRequired means the stored token expired and could not be
// renewed; only a new "Connect with GitHub" authorization can recover.
var ErrOAuthReconnectRequired = errors.New("GitHub OAuth token expired and could not be refreshed; reconnect GitHub")

// SecretStore reads and conditionally rewrites one credential Secret.
type SecretStore interface {
	// Load returns the Secret's decoded data and its resourceVersion.
	Load(ctx context.Context) (data map[string][]byte, resourceVersion string, err error)
	// Save replaces the given keys, failing if the Secret changed since
	// resourceVersion (another replica refreshed first).
	Save(ctx context.Context, data map[string][]byte, resourceVersion string) error
}

// OAuthRefresher renews expiring GitHub OAuth user tokens with the provider's
// OAuth App credentials and stores the rotated pair back into the Secret.
type OAuthRefresher struct {
	// Config holds the OAuth App client ID, secret and token endpoint.
	Config *oauth2.Config
	Now    func() time.Time

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (r *OAuthRefresher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// lock serializes refreshes of one Secret within this process. GitHub refresh
// tokens are single-use, so a second concurrent refresh would fail.
func (r *OAuthRefresher) lock(key string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks == nil {
		r.locks = map[string]*sync.Mutex{}
	}
	l := r.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		r.locks[key] = l
	}
	return l
}

// expiring reports whether data carries a refreshable token that expires
// within the skew. A token without refresh data never expires.
func (r *OAuthRefresher) expiring(data map[string][]byte) (bool, error) {
	if strings.TrimSpace(string(data[OAuthRefreshTokenKey])) == "" {
		return false, nil
	}
	raw := strings.TrimSpace(string(data[OAuthExpiryKey]))
	if raw == "" {
		return false, nil
	}
	expiry, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false, fmt.Errorf("credential %s is not RFC 3339: %w", OAuthExpiryKey, err)
	}
	return !expiry.After(r.now().Add(oauthRefreshSkew)), nil
}

// refreshable reports whether the connection's credential may be renewed here:
// an OAuth token for github.com (the OAuth App cannot mint GHES tokens).
func (r *OAuthRefresher) refreshable(conn *api.Connection) bool {
	return r != nil && r.Config != nil && conn.Spec.Type == api.CredentialTypeOAuth && conn.Spec.BaseURL == ""
}

// Fresh returns the Secret's data with a token valid beyond the skew,
// refreshing and persisting it first when needed. key identifies the Secret
// across calls (cluster, namespace and name).
func (r *OAuthRefresher) Fresh(ctx context.Context, key, tokenKey string, data map[string][]byte, store SecretStore) (map[string][]byte, error) {
	if due, err := r.expiring(data); err != nil || !due {
		return data, err
	}
	l := r.lock(key)
	l.Lock()
	defer l.Unlock()

	// Re-read under the lock: another caller may have refreshed already.
	current, version, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	if due, err := r.expiring(current); err != nil || !due {
		return current, err
	}
	expired := &oauth2.Token{RefreshToken: strings.TrimSpace(string(current[OAuthRefreshTokenKey])), Expiry: r.now().Add(-time.Minute)}
	token, err := r.Config.TokenSource(ctx, expired).Token()
	var rejected *oauth2.RetrieveError
	if errors.As(err, &rejected) || (err == nil && token.AccessToken == "") {
		// GitHub refused the refresh token. Another replica may have used the
		// single-use token first; otherwise the authorization is gone.
		return r.reloadRefreshed(ctx, store)
	}
	if err != nil {
		return nil, fmt.Errorf("refresh GitHub OAuth token: %w", err)
	}
	updated := map[string][]byte{
		tokenKey:             []byte(token.AccessToken),
		OAuthRefreshTokenKey: []byte(token.RefreshToken),
		OAuthExpiryKey:       nil,
	}
	if !token.Expiry.IsZero() {
		updated[OAuthExpiryKey] = []byte(token.Expiry.UTC().Format(time.RFC3339))
	}
	if err := store.Save(ctx, updated, version); err != nil {
		// Prefer a pair another writer stored meanwhile. Otherwise the rotated
		// refresh token exists only here: the old one is spent, so serve the new
		// access token rather than fail now, and report why the next renewal
		// will need a reconnect.
		if refreshed, reloadErr := r.reloadRefreshed(ctx, store); reloadErr == nil {
			return refreshed, nil
		}
		klog.FromContext(ctx).Error(err, "storing refreshed GitHub OAuth token failed; the connection will need a reconnect when this token expires", "secret", key)
	}
	maps.Copy(current, updated)
	return current, nil
}

// reloadRefreshed returns the stored data if another writer renewed it.
func (r *OAuthRefresher) reloadRefreshed(ctx context.Context, store SecretStore) (map[string][]byte, error) {
	data, _, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	if due, err := r.expiring(data); err == nil && !due {
		return data, nil
	}
	return nil, ErrOAuthReconnectRequired
}
