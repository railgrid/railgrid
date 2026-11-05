// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package providerrelay carries shard-authenticated identity across the platform
// edge service proxy. Its proof is consumed by edges, never by tenant code.
package providerrelay

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"
)

const (
	Header           = "X-Railgrid-Provider-Relay"
	PublicKeyPath    = "/api/provider-relay/public-key"
	Lifetime         = 30 * time.Second
	MaxEnvelopeBytes = 32 * 1024
)

var ErrInvalid = errors.New("invalid provider relay proof")

// Claims binds an identity to one exact request and one platform tunnel target.
type Claims struct {
	Version   int                       `json:"v"`
	Cluster   string                    `json:"cluster"`
	Service   string                    `json:"service"`
	Method    string                    `json:"method"`
	Path      string                    `json:"path"`
	Query     string                    `json:"query"`
	Identity  dataplane.ProxiedIdentity `json:"identity"`
	Hops      int                       `json:"hops"`
	IssuedAt  int64                     `json:"iat"`
	ExpiresAt int64                     `json:"exp"`
}

// SigningKey derives a key used ONLY for provider relay envelopes. The hub's
// root proof key is never distributed; edges needs only the public key.
func SigningKey(root []byte) (ed25519.PrivateKey, error) {
	if len(root) < 32 {
		return nil, errors.New("provider relay root key is too short")
	}
	h := sha256.New()
	_, _ = h.Write([]byte("railgrid-provider-relay-ed25519-v1\x00"))
	_, _ = h.Write(root)
	return ed25519.NewKeyFromSeed(h.Sum(nil)), nil
}

func Sign(key ed25519.PrivateKey, c Claims, now time.Time) (string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", ErrInvalid
	}
	c.Version = 1
	c.IssuedAt, c.ExpiresAt = now.Unix(), now.Add(Lifetime).Unix()
	if !valid(c, now) {
		return "", ErrInvalid
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, raw))
	if len(token) > MaxEnvelopeBytes {
		return "", ErrInvalid
	}
	return token, nil
}

func Verify(key ed25519.PublicKey, token, cluster, service, method, path, query string, now time.Time) (Claims, error) {
	if len(key) != ed25519.PublicKeySize || len(token) > MaxEnvelopeBytes {
		return Claims{}, ErrInvalid
	}
	payload, signature, ok := strings.Cut(token, ".")
	if !ok {
		return Claims{}, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Claims{}, ErrInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !ed25519.Verify(key, raw, sig) {
		return Claims{}, ErrInvalid
	}
	var c Claims
	if json.Unmarshal(raw, &c) != nil || !valid(c, now) || c.Cluster != cluster || c.Service != service || c.Method != method || c.Path != path || c.Query != query {
		return Claims{}, ErrInvalid
	}
	return c, nil
}

func valid(c Claims, now time.Time) bool {
	if c.Version != 1 || !dataplane.IsClusterID(c.Cluster) || c.Service == "" || strings.ContainsAny(c.Service, "/%?#\\\r\n") || len(c.Service) > 253 || c.Identity.User == "" || c.Hops < 1 || c.Hops > dataplane.MaxHops || c.IssuedAt > now.Unix()+5 || c.ExpiresAt <= now.Unix() || c.ExpiresAt <= c.IssuedAt || c.ExpiresAt-c.IssuedAt > int64(Lifetime/time.Second) {
		return false
	}
	if _, err := dataplane.ParseSubresourcePath(c.Path); err != nil {
		return false
	}
	if path.Clean(c.Path) != c.Path || strings.ContainsAny(c.Path, "%\\") {
		return false
	}
	if len(c.Query) > 8*1024 || strings.ContainsAny(c.Query, "\r\n") {
		return false
	}
	if !dataplane.IsClusterID(c.Service) || len(c.Identity.User) > 1024 || strings.ContainsAny(c.Identity.User, "\r\n\x00") || len(c.Identity.Groups) > 128 || len(c.Identity.Extra) > 128 {
		return false
	}
	validValue := func(v string) bool { return len(v) <= 4096 && !strings.ContainsAny(v, "\r\n\x00") }
	for _, group := range c.Identity.Groups {
		if !validValue(group) {
			return false
		}
	}
	for key, values := range c.Identity.Extra {
		if key == "" || len(key) > 512 || !validValue(key) || len(values) > 128 {
			return false
		}
		for _, value := range values {
			if !validValue(value) {
				return false
			}
		}
	}
	switch c.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		return false
	}
	return true
}
