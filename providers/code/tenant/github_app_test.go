// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tenant

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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "github.com/faroshq/provider-code/apis/v1alpha1"
)

func TestGitHubInstallationCredentialIsSignedAndBounded(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v3/app/installations/456/access_tokens" || r.Method != http.MethodPost {
			t.Errorf("unexpected token route: %s %s", r.Method, r.URL.Path)
		}
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) != 3 {
			t.Error("missing signed JWT")
			return
		}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		signature, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil || rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature) != nil {
			t.Error("JWT signature invalid")
		}
		claims, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Error(err)
		}
		var got struct {
			Issuer  string `json:"iss"`
			Issued  int64  `json:"iat"`
			Expires int64  `json:"exp"`
		}
		if json.Unmarshal(claims, &got) != nil || got.Issuer != "123" || got.Issued != now.Add(-time.Minute).Unix() || got.Expires != now.Add(9*time.Minute).Unix() {
			t.Errorf("wrong JWT claims: %#v", got)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation-token", "expires_at": now.Add(time.Hour)})
	}))
	defer server.Close()
	conn := &api.Connection{Spec: api.ConnectionSpec{Type: api.CredentialTypeGitHubApp, BaseURL: server.URL}}
	data := map[string][]byte{"appID": []byte("123"), "installationID": []byte("456"), "privateKey": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})}
	resolver := CredentialResolver{Client: server.Client(), Now: func() time.Time { return now }}
	credential, err := resolver.Resolve(context.Background(), conn, data)
	if err != nil || credential.Token != "installation-token" || calls != 1 {
		t.Fatalf("credential resolution failed: calls=%d error=%v", calls, err)
	}
	for _, field := range []string{"appID", "installationID", "privateKey"} {
		original := data[field]
		data[field] = []byte("invalid")
		if _, err := resolver.Resolve(context.Background(), conn, data); err == nil {
			t.Errorf("invalid %s accepted", field)
		}
		data[field] = original
	}
	if calls != 1 {
		t.Fatal("invalid credentials reached remote")
	}
}

func TestCredentialTypesAndMissingToken(t *testing.T) {
	for _, kind := range []api.ConnectionCredentialType{api.CredentialTypePAT, api.CredentialTypeOAuth} {
		conn := &api.Connection{Spec: api.ConnectionSpec{Type: kind}}
		if _, err := (CredentialResolver{}).Resolve(context.Background(), conn, nil); err == nil {
			t.Fatal("empty token accepted")
		}
		cred, err := (CredentialResolver{}).Resolve(context.Background(), conn, map[string][]byte{"token": []byte("test-token")})
		if err != nil || cred.Token != "test-token" {
			t.Fatalf("credential kind %s: %v", kind, err)
		}
	}
}
