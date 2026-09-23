// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
)

func cloneConnection(kind api.ConnectionCredentialType, baseURL string) *api.Connection {
	return &api.Connection{Spec: api.ConnectionSpec{Provider: api.ProviderGitHub, Type: kind, Owner: "example", BaseURL: baseURL, SecretRef: api.LocalSecretReference{Name: "git-key"}}}
}

func cloneRepository(cloneURL string) *api.Repository {
	return &api.Repository{Spec: api.RepositorySpec{ConnectionRef: "git", Name: "product"}, Status: api.RepositoryStatus{RepoID: "123", CloneURL: cloneURL}}
}

// A remote is handed to a runner, which writes it into .git/config and prints
// it in every error. It must therefore be the plain HTTPS URL of the one
// repository and must never carry the credential inline.
func TestCloneRemoteIsHTTPSAndCredentialFree(t *testing.T) {
	for _, test := range []struct {
		name     string
		conn     *api.Connection
		repo     *api.Repository
		expected string
	}{
		{"host reported", cloneConnection(api.CredentialTypePAT, ""), cloneRepository("https://github.com/example/product.git"), "https://github.com/example/product.git"},
		// A host (or a hand-edited status) that echoed a credential back must
		// not have it forwarded.
		{"embedded credential stripped", cloneConnection(api.CredentialTypePAT, ""), cloneRepository("https://user:secret@github.com/example/product.git"), "https://github.com/example/product.git"},
		// git@ and git:// are not clone URLs this credential works with, so
		// the derivation answers instead.
		{"ssh url ignored", cloneConnection(api.CredentialTypePAT, ""), cloneRepository("git@github.com:example/product.git"), "https://github.com/example/product.git"},
		{"derived when unreported", cloneConnection(api.CredentialTypePAT, ""), cloneRepository(""), "https://github.com/example/product.git"},
		// The git transport uses the GHES origin, not its REST /api/v3 prefix.
		{"derived on enterprise", cloneConnection(api.CredentialTypePAT, "https://git.example.com/api/v3"), cloneRepository(""), "https://git.example.com/example/product.git"},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote, err := CloneRemoteFor(test.conn, test.repo)
			if err != nil || remote != test.expected {
				t.Fatalf("remote = %q, err = %v", remote, err)
			}
			if strings.Contains(remote, "@") {
				t.Fatalf("remote carries a credential: %q", remote)
			}
		})
	}

	// A repository whose owner comes from neither side cannot be addressed.
	repo := cloneRepository("")
	conn := cloneConnection(api.CredentialTypePAT, "")
	conn.Spec.Owner = ""
	if _, err := CloneRemoteFor(conn, repo); err == nil {
		t.Fatal("a repository with no owner produced a remote")
	}
}

// The whole point of the action: a GitHub App connection hands out a token
// that can read one repository's contents, not the credential that can also
// push to every repository the installation reaches.
func TestMintCloneTokenNarrowsGitHubAppToContentsRead(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	var requested map[string]map[string]string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if json.Unmarshal(body, &requested) != nil {
			t.Error("installation token request is not JSON")
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "clone-token", "expires_at": now.Add(time.Hour)})
	}))
	defer server.Close()

	conn := cloneConnection(api.CredentialTypeGitHubApp, server.URL)
	data := map[string][]byte{"appID": []byte("123"), "installationID": []byte("456"), "privateKey": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})}
	resolver := CredentialResolver{Client: server.Client(), Now: func() time.Time { return now }}

	token, err := resolver.MintCloneToken(context.Background(), conn, cloneRepository(""), "cluster/default/git-key", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(requested["permissions"]) != 1 || requested["permissions"]["contents"] != "read" {
		t.Fatalf("installation token was not narrowed to contents:read: %#v", requested)
	}
	if token.Token != "clone-token" || !token.Scoped || !token.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("clone credential = %#v", token)
	}
	if token.Username != CloneUsername || token.RemoteURL != server.URL+"/example/product.git" {
		t.Fatalf("clone remote = %q username = %q", token.RemoteURL, token.Username)
	}
}

// A PAT or OAuth connection has no narrowing API. The action still keeps the
// Secret inside this provider, and says out loud that what it returned is not
// least-privilege instead of implying a token it did not issue.
func TestMintCloneTokenReportsUnscopedStoredCredential(t *testing.T) {
	expiry := time.Date(2026, 9, 23, 20, 0, 0, 0, time.UTC)
	conn := cloneConnection(api.CredentialTypeOAuth, "")
	data := map[string][]byte{DefaultTokenKey: []byte("stored-token\n"), OAuthExpiryKey: []byte(expiry.Format(time.RFC3339))}

	token, err := CredentialResolver{}.MintCloneToken(context.Background(), conn, cloneRepository(""), "cluster/default/git-key", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "stored-token" || token.Scoped {
		t.Fatalf("stored credential = %#v", token)
	}
	if !token.ExpiresAt.Equal(expiry) {
		t.Fatalf("known expiry was dropped: %v", token.ExpiresAt)
	}
	if token.RemoteURL != "https://github.com/example/product.git" || token.Username != CloneUsername {
		t.Fatalf("clone remote = %q username = %q", token.RemoteURL, token.Username)
	}
}

// A connection holding no token is not a failure: a public repository clones
// anonymously, so the remote is reported and the caller decides.
func TestMintCloneTokenWithoutCredentialStillReportsRemote(t *testing.T) {
	for _, data := range []map[string][]byte{nil, {DefaultTokenKey: []byte(" \n")}} {
		token, err := CredentialResolver{}.MintCloneToken(context.Background(), cloneConnection(api.CredentialTypePAT, ""), cloneRepository(""), "cluster/default/git-key", data, nil)
		if err != nil {
			t.Fatalf("a credential-less connection failed: %v", err)
		}
		if token.Token != "" || token.Scoped || !token.ExpiresAt.IsZero() {
			t.Fatalf("empty credential = %#v", token)
		}
		if token.RemoteURL != "https://github.com/example/product.git" {
			t.Fatalf("clone remote = %q", token.RemoteURL)
		}
	}
}
