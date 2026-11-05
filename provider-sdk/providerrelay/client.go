// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providerrelay

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// RemoteVerifier reads only the public key from the configured hub origin.
// Entries expire even when the old key continues validating signatures.
type RemoteVerifier struct {
	url         string
	client      *http.Client
	mu          sync.Mutex
	key         ed25519.PublicKey
	expires     time.Time
	retryAfter  time.Time
	refreshErr  error
	refreshing  bool
	refreshDone chan struct{}
}

func NewRemoteVerifier(hub string, client *http.Client) (*RemoteVerifier, error) {
	u, err := url.Parse(hub)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("provider relay requires a configured hub URL")
	}
	u.Path, u.RawPath = strings.TrimRight(u.Path, "/")+PublicKeyPath, ""
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	c.Timeout = 5 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &RemoteVerifier{url: u.String(), client: &c}, nil
}

func (v *RemoteVerifier) Verify(ctx context.Context, token, cluster, service, method, path, query string) (Claims, error) {
	payload, signature, split := strings.Cut(token, ".")
	sig, decodeErr := base64.RawURLEncoding.DecodeString(signature)
	if len(token) > MaxEnvelopeBytes || !split || payload == "" || decodeErr != nil || len(sig) != ed25519.SignatureSize {
		return Claims{}, ErrInvalid
	}
	key, err := v.currentKey(ctx)
	if err != nil {
		return Claims{}, err
	}
	now := time.Now()
	return Verify(key, token, cluster, service, method, path, query, now)
}

// currentKey returns a key only while its cache lifetime is valid. A refresh
// is coalesced without holding mu over network I/O, so waiters can cancel and
// state readers are not serialized behind the HTTP request.
func (v *RemoteVerifier) currentKey(ctx context.Context) (ed25519.PublicKey, error) {
	for {
		now := time.Now()
		v.mu.Lock()
		if len(v.key) != 0 && now.Before(v.expires) {
			key := v.key
			v.mu.Unlock()
			return key, nil
		}
		if now.Before(v.retryAfter) {
			err := v.refreshErr
			v.mu.Unlock()
			return nil, err
		}
		if v.refreshing {
			done := v.refreshDone
			v.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}

		done := make(chan struct{})
		refreshStarted := now
		v.refreshing = true
		v.refreshDone = done
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		v.mu.Unlock()
		go v.refresh(refreshCtx, cancel, refreshStarted, done)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
			continue
		}
	}
}

func (v *RemoteVerifier) refresh(ctx context.Context, cancel context.CancelFunc, started time.Time, done chan struct{}) {
	defer cancel()
	key, err := v.fetchKey(ctx)
	v.mu.Lock()
	if err != nil {
		// Fail closed without turning queued requests into serial network
		// retries when the hub is unavailable. Expired keys stay unusable.
		v.retryAfter, v.refreshErr = time.Now().Add(time.Second), err
	} else {
		v.key, v.expires = key, started.Add(30*time.Second)
		v.retryAfter, v.refreshErr = time.Time{}, nil
	}
	v.refreshing = false
	v.refreshDone = nil
	close(done)
	v.mu.Unlock()
}

func (v *RemoteVerifier) fetchKey(ctx context.Context) (ed25519.PublicKey, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider relay key unavailable (%d)", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
	if err != nil || len(raw) > 1024 {
		return nil, ErrInvalid
	}
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, ErrInvalid
	}
	return ed25519.PublicKey(key), nil
}
