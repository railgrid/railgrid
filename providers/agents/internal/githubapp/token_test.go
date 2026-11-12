// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMintSignsAppJWTAndRenewsEveryTurn(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string][]byte{"appID": []byte("123"), "installationID": []byte("456"), "privateKey": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), "token": []byte("personal-token-must-not-be-used")}
	now := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
	calls := 0
	c := Client{RepositoryIDs: []int64{99}, Now: func() time.Time { return now }, Permissions: map[string]string{"contents": "read", "pull_requests": "write"}}
	c.HTTPClient = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != "https://api.github.com/app/installations/456/access_tokens" || req.Method != "POST" {
			t.Fatalf("unexpected target %s %s", req.Method, req.URL)
		}
		parts := strings.Split(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) != 3 {
			t.Fatal("expected app JWT")
		}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
			t.Fatal(err)
		}
		raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		if err := json.Unmarshal(raw, &claims); err != nil {
			t.Fatal(err)
		}
		if claims["iss"] != "123" || int64(claims["exp"].(float64)) != now.Add(9*time.Minute).Unix() {
			t.Fatal("incorrect JWT identity or lifetime")
		}
		var body struct {
			Permissions   map[string]string `json:"permissions"`
			RepositoryIDs []int64           `json:"repository_ids"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.RepositoryIDs) != 1 || body.RepositoryIDs[0] != 99 {
			t.Fatal("repository scope lost")
		}
		if body.Permissions["contents"] != "read" || body.Permissions["pull_requests"] != "write" {
			t.Fatal("permission narrowing lost")
		}
		payload := fmt.Sprintf(`{"token":"installation-%d","expires_at":%q}`, calls, now.Add(time.Hour).Format(time.RFC3339))
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})}
	for i := 1; i <= 2; i++ {
		token, expiry, err := c.Mint(context.Background(), data)
		if err != nil {
			t.Fatal(err)
		}
		if token != fmt.Sprintf("installation-%d", i) || !expiry.Equal(now.Add(time.Hour)) {
			t.Fatal("token not freshly minted")
		}
	}
}

func TestMintRefusesInvalidCredentialsWithoutPersonalFallback(t *testing.T) {
	for _, data := range []map[string][]byte{
		{"token": []byte("personal-secret")},
		{"appID": []byte("123"), "installationID": []byte("456"), "privateKey": []byte("private-secret"), "token": []byte("personal-secret")},
	} {
		_, _, err := (Client{}).Mint(context.Background(), data)
		if err == nil || strings.Contains(err.Error(), "personal-secret") || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("expected sanitized credential failure")
		}
	}
}

func TestCheckoutAuthorityCannotPublishOrEscapeRepository(t *testing.T) {
	p, err := ForRole("checkout", "1408771335")
	if err != nil || len(p.RepositoryIDs) != 1 || p.RepositoryIDs[0] != 1408771335 {
		t.Fatalf("repository scope: %+v %v", p, err)
	}
	if len(p.Permissions) != 2 || p.Permissions["contents"] != "read" || p.Permissions["metadata"] != "read" {
		t.Fatalf("checkout can publish: %+v", p.Permissions)
	}
	for _, id := range []string{"", "0", "-1", "other/repo"} {
		if _, err := ForRole("checkout", id); err == nil {
			t.Fatalf("invalid repo %q", id)
		}
	}
}
