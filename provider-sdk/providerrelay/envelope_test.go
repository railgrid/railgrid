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
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testResponse(req *http.Request, status int, header http.Header, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

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
	keyClient := &http.Client{Transport: testRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Path != PublicKeyPath {
			t.Errorf("unexpected key path %s", r.URL.Path)
		}
		return testResponse(r, http.StatusOK, make(http.Header), base64.RawURLEncoding.EncodeToString(current)), nil
	})}
	v, err := NewRemoteVerifier("https://hub.example", keyClient)
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
	redirect := &http.Client{Transport: testRoundTripper(func(r *http.Request) (*http.Response, error) {
		return testResponse(r, http.StatusFound, http.Header{"Location": {"https://other.example" + PublicKeyPath}}, ""), nil
	})}
	v, _ = NewRemoteVerifier("https://hub.example", redirect)
	if verify() == nil {
		t.Fatal("followed public-key redirect")
	}
	if requests != 2 {
		t.Fatal("redirect reached another key origin")
	}
}

func TestRemoteVerifierBacksOffFailedRefreshAndRejectsMalformedProof(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: testRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests++
		return testResponse(r, http.StatusServiceUnavailable, make(http.Header), "unavailable\n"), nil
	})}
	verifier, err := NewRemoteVerifier("https://hub.example", client)
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

func TestRemoteVerifierCoalescesRefreshAndAllowsWaiterCancellation(t *testing.T) {
	key, _ := SigningKey(bytes.Repeat([]byte{3}, 32))
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	keyClient := &http.Client{Transport: testRoundTripper(func(r *http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			close(started)
		}
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-release:
			return testResponse(r, http.StatusOK, make(http.Header), base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))), nil
		}
	})}
	verifier, err := NewRemoteVerifier("https://hub.example", keyClient)
	if err != nil {
		t.Fatal(err)
	}
	c := exampleClaims()
	proof, _ := Sign(key, c, time.Now())
	verify := func(ctx context.Context) error {
		_, err := verifier.Verify(ctx, proof, c.Cluster, c.Service, c.Method, c.Path, c.Query)
		return err
	}

	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	owner := make(chan error, 1)
	go func() { owner <- verify(ownerCtx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("key refresh did not start")
	}

	// Every caller, including the refresh owner, can stop waiting without
	// canceling the bounded shared fetch or starting another key request.
	const waiters = 8
	results := make(chan error, waiters)
	for range waiters {
		go func() { results <- verify(context.Background()) }()
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := verify(waitCtx); err != context.DeadlineExceeded {
		t.Fatalf("waiting verify error = %v, want context deadline exceeded", err)
	}
	cancelOwner()
	if err := <-owner; err != context.Canceled {
		t.Fatalf("refresh owner error = %v, want context canceled", err)
	}
	close(release)
	for range waiters {
		err := <-results
		if err != nil {
			t.Errorf("coalesced verify: %v", err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("concurrent refreshes made %d key requests, want 1", got)
	}
	if err := verify(context.Background()); err != nil {
		t.Fatalf("cached verify after refresh: %v", err)
	}
}
