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

// ResolveStored resolves the credential held in data, first renewing an
// expiring OAuth token and persisting it through store. secretKey identifies
// the Secret (cluster/namespace/name) so concurrent refreshes serialize.
func (r CredentialResolver) ResolveStored(ctx context.Context, conn *api.Connection, secretKey string, data map[string][]byte, store SecretStore) (backend.Credential, error) {
	if r.OAuth.refreshable(conn) {
		key := conn.Spec.SecretRef.Key
		if key == "" {
			key = DefaultTokenKey
		}
		fresh, err := r.OAuth.Fresh(ctx, secretKey, key, data, store)
		if err != nil {
			return backend.Credential{}, err
		}
		data = fresh
	}
	return r.Resolve(ctx, conn, data)
}

func (r CredentialResolver) Resolve(ctx context.Context, conn *api.Connection, data map[string][]byte) (backend.Credential, error) {
	if conn.Spec.Type != api.CredentialTypeGitHubApp {
		if conn.Spec.Type != api.CredentialTypePAT && conn.Spec.Type != api.CredentialTypeOAuth && conn.Spec.Type != "" {
			return backend.Credential{}, errors.New("unsupported Code credential type")
		}
		key := conn.Spec.SecretRef.Key
		if key == "" {
			key = DefaultTokenKey
		}
		// Trim: a Secret created from a file or `cmd | kubectl create secret
		// --from-file` keeps the trailing newline, which net/http rejects as an
		// invalid Authorization header value before GitHub is ever called.
		token := strings.TrimSpace(string(data[key]))
		if token == "" {
			return backend.Credential{}, errors.New("code credential token missing")
		}
		return backend.Credential{Token: token}, nil
	}
	appID, err := strconv.ParseInt(strings.TrimSpace(string(data["appID"])), 10, 64)
	if err != nil || appID <= 0 {
		return backend.Credential{}, errors.New("GitHub App appID must be positive")
	}
	installationID, err := strconv.ParseInt(strings.TrimSpace(string(data["installationID"])), 10, 64)
	if err != nil || installationID <= 0 {
		return backend.Credential{}, errors.New("GitHub App installationID must be positive")
	}
	if len(data["privateKey"]) > 32768 {
		return backend.Credential{}, errors.New("GitHub App private key exceeds limit")
	}
	block, rest := pem.Decode(data["privateKey"])
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return backend.Credential{}, errors.New("GitHub App privateKey must contain one RSA private key")
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
		return backend.Credential{}, errors.New("GitHub App requires a valid RSA key of at least 2048 bits")
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iss": strconv.FormatInt(appID, 10), "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix()})
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return backend.Credential{}, errors.New("GitHub App signing failed")
	}
	base := "https://api.github.com"
	if conn.Spec.BaseURL != "" {
		u, err := url.Parse(conn.Spec.BaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return backend.Credential{}, errors.New("GitHub App baseURL must be an HTTPS API endpoint")
		}
		base = strings.TrimRight(u.String(), "/")
		if !strings.HasSuffix(base, "/api/v3") {
			base += "/api/v3"
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/app/installations/%d/access_tokens", base, installationID), strings.NewReader("{}"))
	if err != nil {
		return backend.Credential{}, err
	}
	req.Header.Set("Authorization", "Bearer "+unsigned+"."+base64.RawURLEncoding.EncodeToString(signature))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := &http.Client{Timeout: 20 * time.Second}
	if r.Client != nil {
		copied := *r.Client
		client = &copied
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("credential redirects forbidden") }
	response, err := client.Do(req)
	if err != nil {
		return backend.Credential{}, errors.New("GitHub installation token request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated {
		return backend.Credential{}, fmt.Errorf("GitHub installation token returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return backend.Credential{}, errors.New("invalid installation token response")
	}
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if json.Unmarshal(body, &result) != nil || result.Token == "" || !result.ExpiresAt.After(now.Add(5*time.Minute)) {
		return backend.Credential{}, errors.New("invalid or expiring installation token")
	}
	return backend.Credential{Token: result.Token}, nil
}
