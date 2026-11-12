// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package githubapp mints short-lived installation credentials for Agents.
// The application private key remains inside the provider; only the token is
// handed to a coding harness, freshly minted for each turn.
package githubapp

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
	"strconv"
	"strings"
	"time"
)

// Client signs installation requests against GitHub's fixed API endpoint.
// HTTPClient and Now are injectable for isolated protocol tests.
type Client struct {
	HTTPClient    *http.Client
	Now           func() time.Time
	RepositoryIDs []int64
	Permissions   map[string]string
}

// Mint requests the permissions granted to this installation. The token is
// obtained once per harness turn, including resumed turns; it is never reused
// from a previous turn or silently replaced with a personal credential.
func (r Client) Mint(ctx context.Context, data map[string][]byte) (string, time.Time, error) {
	permissions := r.Permissions
	key, appID, installationID, err := parse(data)
	if err != nil {
		return "", time.Time{}, err
	}

	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iss": appID, "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix()})
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", time.Time{}, errors.New("GitHub App signing failed")
	}
	base := "https://api.github.com"

	body := "{}"
	if len(permissions) > 0 {
		requestBody := map[string]any{"permissions": permissions}
		if len(r.RepositoryIDs) > 0 {
			requestBody["repository_ids"] = r.RepositoryIDs
		}
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return "", time.Time{}, errors.New("GitHub App permission request could not be encoded")
		}
		body = string(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/app/installations/%d/access_tokens", base, installationID), strings.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+unsigned+"."+base64.RawURLEncoding.EncodeToString(signature))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := &http.Client{Timeout: 20 * time.Second}
	if r.HTTPClient != nil {
		copied := *r.HTTPClient
		client = &copied
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("credential redirects forbidden") }
	response, err := client.Do(req)
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

// Validate checks the installation credential shape without a network request.
func Validate(data map[string][]byte) error {
	_, _, _, err := parse(data)
	return err
}

func parse(data map[string][]byte) (*rsa.PrivateKey, string, int64, error) {
	appID, err := strconv.ParseInt(strings.TrimSpace(string(data["appID"])), 10, 64)
	if err != nil || appID <= 0 {
		return nil, "", 0, errors.New("GitHub App appID must be positive")
	}
	installationID, err := strconv.ParseInt(strings.TrimSpace(string(data["installationID"])), 10, 64)
	if err != nil || installationID <= 0 {
		return nil, "", 0, errors.New("GitHub App installationID must be positive")
	}
	if len(data["privateKey"]) > 32768 {
		return nil, "", 0, errors.New("GitHub App private key exceeds limit")
	}
	block, rest := pem.Decode(data["privateKey"])
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, "", 0, errors.New("GitHub App privateKey must contain one RSA private key")
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
		return nil, "", 0, errors.New("GitHub App requires a valid RSA key of at least 2048 bits")
	}
	return key, strconv.FormatInt(appID, 10), installationID, nil
}

// ForRole defines the token authority before minting; prompts cannot widen it.
func ForRole(role, repositoryID string) (Client, error) {
	if role == "checkout" {
		id, err := strconv.ParseInt(repositoryID, 10, 64)
		if err != nil || id <= 0 {
			return Client{}, fmt.Errorf("checkout connection requires a numeric repositoryID")
		}
		return Client{RepositoryIDs: []int64{id}, Permissions: map[string]string{"contents": "read", "metadata": "read"}}, nil
	}
	if role != "reviewer" && role != "fixer" {
		return Client{}, fmt.Errorf("githubRole must be reviewer, fixer or checkout")
	}
	contents := "read"
	if role == "fixer" {
		contents = "write"
	}
	return Client{Permissions: map[string]string{"contents": contents, "pull_requests": "write", "issues": "write", "actions": "read", "checks": "read", "statuses": "read", "metadata": "read"}}, nil
}
