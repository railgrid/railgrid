// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"
)

func exampleClaims() Claims {
	return Claims{Cluster: "hosting", Service: "provider-planner", Method: "POST", Path: "/clusters/consumer/apis/planner.example/v1/boards/demo/run", Query: "component=main", Identity: dataplane.ProxiedIdentity{User: "alice", Groups: []string{"team"}, Extra: map[string][]string{dataplane.ClusterNameExtra: {"caller-cluster"}}}, Hops: 2}
}

func TestProofBindsIdentityAndExactRequest(t *testing.T) {
	key, _ := SigningKey(bytes.Repeat([]byte{1}, 32))
	now := time.Now()
	claims := exampleClaims()
	proof, err := Sign(key, claims, now)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(token string, c Claims, at time.Time) error {
		_, err := Verify(key.Public().(ed25519.PublicKey), token, c.Cluster, c.Service, c.Method, c.Path, c.Query, at)
		return err
	}
	if err := verify(proof, claims, now); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"cluster", "service", "method", "path", "query", "signature", "expired"} {
		t.Run(field, func(t *testing.T) {
			c := claims
			p := proof
			at := now
			switch field {
			case "cluster":
				c.Cluster = "other"
			case "service":
				c.Service = "other"
			case "method":
				c.Method = "GET"
			case "path":
				c.Path = strings.Replace(c.Path, "demo", "other", 1)
			case "query":
				c.Query = "component=other"
			case "signature":
				p = "e30." + strings.Split(proof, ".")[1]
			case "expired":
				at = now.Add(Lifetime)
			}
			if verify(p, c, at) == nil {
				t.Fatal("accepted invalid proof")
			}
		})
	}
	for _, path := range []string{claims.Path + "/../other", claims.Path + "/%2e%2e/other", claims.Path + "//other"} {
		c := claims
		c.Path = path
		if _, err := Sign(key, c, now); err == nil {
			t.Fatalf("signed noncanonical %q", path)
		}
	}
}

func TestRemoteKeyCacheExpiresAndNeverRedirects(t *testing.T) {
	key, _ := SigningKey(bytes.Repeat([]byte{1}, 32))
	replacement, _ := SigningKey(bytes.Repeat([]byte{2}, 32))
	current := key.Public().(ed25519.PublicKey)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != PublicKeyPath {
			t.Errorf("unexpected key path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(base64.RawURLEncoding.EncodeToString(current)))
	}))
	defer srv.Close()
	v, err := NewRemoteVerifier(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c := exampleClaims()
	proof, _ := Sign(key, c, time.Now())
	verify := func() error {
		_, err := v.Verify(context.Background(), proof, c.Cluster, c.Service, c.Method, c.Path, c.Query)
		return err
	}
	if err := verify(); err != nil {
		t.Fatal(err)
	}
	if err := verify(); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("key fetched %d times", requests)
	}
	current = replacement.Public().(ed25519.PublicKey)
	v.expires = time.Now().Add(-time.Second)
	if verify() == nil {
		t.Fatal("accepted old signing key after cache TTL")
	}
	if requests != 2 {
		t.Fatalf("key fetch count %d", requests)
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+PublicKeyPath, http.StatusFound)
	}))
	defer redirect.Close()
	v, _ = NewRemoteVerifier(redirect.URL, redirect.Client())
	if verify() == nil {
		t.Fatal("followed public-key redirect")
	}
	if requests != 2 {
		t.Fatal("redirect reached another key origin")
	}
}

func TestRemoteVerifierBacksOffFailedRefreshAndRejectsMalformedProof(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	verifier, err := NewRemoteVerifier(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	c := exampleClaims()
	if _, err := verifier.Verify(context.Background(), "garbage", c.Cluster, c.Service, c.Method, c.Path, c.Query); err == nil {
		t.Fatal("accepted malformed proof")
	}
	if requests != 0 {
		t.Fatal("malformed proof triggered a key request")
	}
	key, _ := SigningKey(bytes.Repeat([]byte{1}, 32))
	proof, _ := Sign(key, c, time.Now())
	for range 3 {
		if _, err := verifier.Verify(context.Background(), proof, c.Cluster, c.Service, c.Method, c.Path, c.Query); err == nil {
			t.Fatal("accepted proof without a current key")
		}
	}
	if requests != 1 {
		t.Fatalf("failed refresh requested %d times", requests)
	}
	verifier.retryAfter = time.Now().Add(-time.Second)
	if _, err := verifier.Verify(context.Background(), proof, c.Cluster, c.Service, c.Method, c.Path, c.Query); err == nil {
		t.Fatal("accepted unavailable key")
	}
	if requests != 2 {
		t.Fatal("failed refresh did not retry after backoff")
	}
}
