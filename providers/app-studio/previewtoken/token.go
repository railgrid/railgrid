/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package previewtoken

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	QueryParam = "kedgePreviewToken"
)

const tokenTTL = time.Hour

type Payload struct {
	ProjectName        string `json:"projectName"`
	TenantPath         string `json:"tenantPath"`
	ResourceName       string `json:"resourceName"`
	Subject            string `json:"subject,omitempty"`
	Host               string `json:"host"`
	RuntimeNamespace   string `json:"runtimeNamespace"`
	PreviewServiceName string `json:"previewServiceName"`
	PreviewPortName    string `json:"previewPortName"`
	AccessMode         string `json:"accessMode,omitempty"`
	ExpiresAt          int64  `json:"expiresAt"`
}

type Signer struct {
	secret []byte
	now    func() time.Time
}

func NewSigner(secret []byte) *Signer {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			sum := sha256.Sum256([]byte(time.Now().String()))
			secret = sum[:]
		}
	}
	return &Signer{secret: append([]byte(nil), secret...), now: time.Now}
}

func (s *Signer) Sign(payload Payload) (string, time.Time, error) {
	if payload.ProjectName == "" || payload.TenantPath == "" || payload.ResourceName == "" ||
		payload.Host == "" || payload.RuntimeNamespace == "" || payload.PreviewServiceName == "" || payload.PreviewPortName == "" {
		return "", time.Time{}, fmt.Errorf("preview token payload is incomplete")
	}
	expiresAt := time.Unix(payload.ExpiresAt, 0).UTC()
	if payload.ExpiresAt == 0 {
		expiresAt = s.now().Add(tokenTTL).UTC()
		payload.ExpiresAt = expiresAt.Unix()
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + sig, expiresAt, nil
}

func (s *Signer) Verify(token string) (Payload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Payload{}, fmt.Errorf("invalid preview token")
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(parts[0]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, want) {
		return Payload{}, fmt.Errorf("invalid preview token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Payload{}, fmt.Errorf("invalid preview token")
	}
	var payload Payload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Payload{}, fmt.Errorf("invalid preview token")
	}
	if payload.ProjectName == "" || payload.TenantPath == "" || payload.ResourceName == "" ||
		payload.Host == "" || payload.RuntimeNamespace == "" || payload.PreviewServiceName == "" || payload.PreviewPortName == "" {
		return Payload{}, fmt.Errorf("preview token is incomplete")
	}
	if s.now().Unix() > payload.ExpiresAt {
		return Payload{}, fmt.Errorf("preview token expired")
	}
	return payload, nil
}

func NormalizeHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		raw = parsed.Host
	}
	if i := strings.Index(raw, "/"); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, "?"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimPrefix(raw, "/")
	raw = strings.TrimSuffix(raw, "/")
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return host
	}
	return raw
}

func CookieNameForHost(host string) string {
	sum := sha256.Sum256([]byte(NormalizeHost(host)))
	return "kedge_app_studio_preview_" + hex.EncodeToString(sum[:])[:16]
}
